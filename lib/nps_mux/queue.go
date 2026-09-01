package nps_mux

import (
	"errors"
	"io"
	"math"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

type priorityQueue struct {
	highestChain *bufChain
	middleChain  *bufChain
	lowestChain  *bufChain
	starving     uint8
	stop         atomic.Bool
	cond         *sync.Cond
}

func (Self *priorityQueue) New() {
	Self.highestChain = new(bufChain)
	Self.highestChain.new(4)
	Self.middleChain = new(bufChain)
	Self.middleChain.new(32)
	Self.lowestChain = new(bufChain)
	Self.lowestChain.new(256)
	locker := new(sync.Mutex)
	Self.cond = sync.NewCond(locker)
}

func (Self *priorityQueue) Push(packager *muxPackager) {
	Self.cond.L.Lock()
	stopped := Self.stop.Load()
	if !stopped {
		Self.push(packager)
		// The predicate update and notification must be protected by cond.L.
		// Otherwise a consumer can observe an empty queue immediately before
		// Wait and miss this broadcast permanently.
		Self.cond.Broadcast()
	}
	Self.cond.L.Unlock()
	if stopped {
		// Close may race with a producer that already allocated a package.
		// Return it here rather than leaving its pooled buffers live forever.
		releaseMuxPack(packager)
		return
	}
}

func (Self *priorityQueue) push(packager *muxPackager) {
	switch packager.flag {
	case muxPingFlag, muxPingReturn:
		Self.highestChain.pushHead(unsafe.Pointer(packager))
	// the ping package need highest priority
	// prevent ping calculation error
	case muxNewConn, muxNewConnOk, muxNewConnFail:
		// the New conn package need some priority too
		Self.middleChain.pushHead(unsafe.Pointer(packager))
	default:
		Self.lowestChain.pushHead(unsafe.Pointer(packager))
	}
}

const maxStarving uint8 = 8

func (Self *priorityQueue) Pop() (packager *muxPackager) {
	Self.cond.L.Lock()
	defer Self.cond.L.Unlock()
	for {
		packager = Self.tryPopLocked()
		if packager != nil {
			return
		}
		if Self.stop.Load() {
			return
		}
		Self.cond.Wait()
	}
}

func (Self *priorityQueue) TryPop() (packager *muxPackager) {
	Self.cond.L.Lock()
	defer Self.cond.L.Unlock()
	return Self.tryPopLocked()
}

// tryPopLocked consumes a package while Self.cond.L is held. bufChain is derived
// from sync.Pool's single-producer queue, while sendInfo has multiple
// producers and shutdown may inspect the queue concurrently.
func (Self *priorityQueue) tryPopLocked() (packager *muxPackager) {
	ptr, ok := Self.highestChain.popTail()
	if ok {
		packager = (*muxPackager)(ptr)
		return
	}
	if Self.starving < maxStarving {
		// not pop too much, lowestChain will wait too long
		ptr, ok = Self.middleChain.popTail()
		if ok {
			packager = (*muxPackager)(ptr)
			Self.starving++
			return
		}
	}
	ptr, ok = Self.lowestChain.popTail()
	if ok {
		packager = (*muxPackager)(ptr)
		if Self.starving > 0 {
			Self.starving = Self.starving / 2
		}
		return
	}
	if Self.starving > 0 {
		ptr, ok = Self.middleChain.popTail()
		if ok {
			packager = (*muxPackager)(ptr)
			Self.starving++
			return
		}
	}
	return
}

func (Self *priorityQueue) Stop() {
	Self.cond.L.Lock()
	Self.stop.Store(true)
	Self.cond.Broadcast()
	Self.cond.L.Unlock()
}

type connQueue struct {
	chain *bufChain
	stop  atomic.Bool
	cond  *sync.Cond
}

func (Self *connQueue) New() {
	Self.chain = new(bufChain)
	Self.chain.new(32)
	locker := new(sync.Mutex)
	Self.cond = sync.NewCond(locker)
}

func (Self *connQueue) Push(connection *conn) {
	Self.cond.L.Lock()
	stopped := Self.stop.Load()
	if !stopped {
		Self.chain.pushHead(unsafe.Pointer(connection))
		Self.cond.Broadcast()
	}
	Self.cond.L.Unlock()
	if stopped {
		_ = connection.Close()
	}
}

func (Self *connQueue) Pop() (connection *conn) {
	Self.cond.L.Lock()
	defer Self.cond.L.Unlock()
	for {
		connection = Self.tryPopLocked()
		if connection != nil {
			return
		}
		if Self.stop.Load() {
			return
		}
		Self.cond.Wait()
	}
}

func (Self *connQueue) TryPop() (connection *conn) {
	Self.cond.L.Lock()
	defer Self.cond.L.Unlock()
	return Self.tryPopLocked()
}

func (Self *connQueue) tryPopLocked() (connection *conn) {
	ptr, ok := Self.chain.popTail()
	if ok {
		connection = (*conn)(ptr)
		return
	}
	return
}

func (Self *connQueue) Stop() {
	Self.cond.L.Lock()
	Self.stop.Store(true)
	Self.cond.Broadcast()
	Self.cond.L.Unlock()
}

type listElement struct {
	Buf  []byte
	L    uint16
	Part bool
}

func (Self *listElement) Reset() {
	Self.L = 0
	Self.Buf = nil
	Self.Part = false
}

func newListElement(buf []byte, l uint16, part bool) (element *listElement, err error) {
	if uint16(len(buf)) != l {
		err = errors.New("listElement: buf length not match")
		return
	}
	element = listEle.Get()
	element.Buf = buf
	element.L = l
	element.Part = part
	return
}

type receiveWindowQueue struct {
	mu        sync.Mutex
	length    uint32
	chain     *bufChain
	stopOp    chan struct{}
	stopOnce  sync.Once
	stopped   atomic.Bool
	readOp    chan struct{}
	timeoutMu sync.RWMutex
	timeout   time.Time
}

func newReceiveWindowQueue() *receiveWindowQueue {
	queue := receiveWindowQueue{
		chain:  new(bufChain),
		stopOp: make(chan struct{}),
		// A single coalesced notification closes the check-then-wait gap:
		// a producer can publish data before a consumer enters waitPush.
		readOp: make(chan struct{}, 1),
	}
	queue.chain.new(64)
	return &queue
}

func (Self *receiveWindowQueue) Push(element *listElement) {
	if element == nil {
		return
	}
	Self.mu.Lock()
	if Self.stopped.Load() {
		Self.mu.Unlock()
		// receiveWindow normally serializes Stop and Write. Keep the queue
		// safe for callers that race them as well, without retaining a buffer
		// that can never be consumed.
		discardListElement(element)
		return
	}
	Self.chain.pushHead(unsafe.Pointer(element))
	Self.length += uint32(element.L)
	Self.mu.Unlock()
	Self.allowPop()
	return
}

func (Self *receiveWindowQueue) Pop() (element *listElement, err error) {
	for {
		Self.mu.Lock()
		element = Self.popLocked()
		stopped := Self.stopped.Load()
		if element == nil && !stopped {
			// Any token left from a previously consumed element is stale. Drain
			// it while holding the predicate lock; a producer cannot publish
			// between this drain and the subsequent wait setup.
			Self.drainNotificationsLocked()
		}
		Self.mu.Unlock()
		if element != nil {
			return
		}
		if stopped {
			return nil, io.EOF
		}

		err = Self.waitPush()
		if err == nil {
			continue
		}
		// A notification and a deadline may become ready together. Recheck
		// the predicate before returning the timeout, otherwise data published
		// at the deadline would be stranded until a later read.
		Self.mu.Lock()
		stopped = Self.stopped.Load()
		if !stopped {
			element = Self.popLocked()
		}
		Self.mu.Unlock()
		if element != nil {
			return element, nil
		}
		if stopped {
			return nil, io.EOF
		}
		return nil, err
	}
}

func (Self *receiveWindowQueue) TryPop() (element *listElement) {
	Self.mu.Lock()
	defer Self.mu.Unlock()
	return Self.popLocked()
}

func (Self *receiveWindowQueue) popLocked() (element *listElement) {
	ptr, ok := Self.chain.popTail()
	if ok {
		element = (*listElement)(ptr)
		if Self.length >= uint32(element.L) {
			Self.length -= uint32(element.L)
		} else {
			// Keep Len bounded even if a malformed element enters the queue.
			Self.length = 0
		}
		return
	}
	return nil
}

func (Self *receiveWindowQueue) allowPop() (closed bool) {
	select {
	case Self.readOp <- struct{}{}:
		return false
	case <-Self.stopOp:
		return true
	default:
		// Notifications are coalesced. A consumer always checks the queue
		// predicate after waking, so a full channel means no signal is needed.
		return false
	}
}

func (Self *receiveWindowQueue) drainNotificationsLocked() {
	for {
		select {
		case <-Self.readOp:
		default:
			return
		}
	}
}

func (Self *receiveWindowQueue) waitPush() (err error) {
	deadline := Self.getTimeOut()
	if deadline.IsZero() {
		// No deadline: wait until a producer or Stop, like a TCP connection.
		select {
		case <-Self.readOp:
			return nil
		case <-Self.stopOp:
			err = io.EOF
			return
		}
	}
	t := time.Until(deadline)
	if t <= 0 {
		return os.ErrDeadlineExceeded
	}
	timer := time.NewTimer(t)
	defer timer.Stop()
	select {
	case <-Self.readOp:
		return nil
	case <-Self.stopOp:
		err = io.EOF
		return
	case <-timer.C:
		err = os.ErrDeadlineExceeded
		return
	}
}

func (Self *receiveWindowQueue) Len() (n uint32) {
	Self.mu.Lock()
	n = Self.length
	Self.mu.Unlock()
	return n
}

func (Self *receiveWindowQueue) Stop() {
	Self.stopOnce.Do(func() {
		Self.mu.Lock()
		Self.stopped.Store(true)
		close(Self.stopOp)
		Self.mu.Unlock()
	})
}

func (Self *receiveWindowQueue) SetTimeOut(t time.Time) {
	Self.timeoutMu.Lock()
	Self.timeout = t
	Self.timeoutMu.Unlock()
}

func (Self *receiveWindowQueue) getTimeOut() time.Time {
	Self.timeoutMu.RLock()
	defer Self.timeoutMu.RUnlock()
	return Self.timeout
}

func discardListElement(element *listElement) {
	if element == nil {
		return
	}
	if element.Buf != nil {
		// Buffers created by receiveWindow are pool-sized. Tests and callers
		// may provide smaller buffers, which cannot be returned to that pool.
		if cap(element.Buf) >= poolSizeWindow {
			windowBuff.Put(element.Buf)
		}
		element.Buf = nil
	}
	listEle.Put(element)
}

// https://golang.org/src/sync/poolqueue.go

type bufDequeue struct {
	// headTail packs together a 32-bit head index and a 32-bit
	// tail index. Both are indexes into vals modulo len(vals)-1.
	//
	// tail = index of oldest data in queue
	// head = index of next slot to fill
	//
	// Slots in the range [tail, head) are owned by consumers.
	// A consumer continues to own a slot outside this range until
	// it nils the slot, at which point ownership passes to the
	// producer.
	//
	// The head index is stored in the most-significant bits so
	// that we can atomically add to it and the overflow is
	// harmless.
	headTail uint64

	// vals is a ring buffer of interface{} values stored in this
	// dequeue. The size of this must be a power of 2.
	//
	// A slot is still in use until *both* the tail
	// index has moved beyond it and typ has been Set to nil. This
	// is Set to nil atomically by the consumer and read
	// atomically by the producer.
	vals     []unsafe.Pointer
	starving uint32
}

const dequeueBits = 32

// dequeueLimit is the maximum size of a bufDequeue.
//
// This must be at most (1<<dequeueBits)/2 because detecting fullness
// depends on wrapping around the ring buffer without wrapping around
// the index. We divide by 4 so this fits in an int on 32-bit.
const dequeueLimit = (1 << dequeueBits) / 4

func (d *bufDequeue) unpack(ptrs uint64) (head, tail uint32) {
	const mask = 1<<dequeueBits - 1
	head = uint32((ptrs >> dequeueBits) & mask)
	tail = uint32(ptrs & mask)
	return
}

func (d *bufDequeue) pack(head, tail uint32) uint64 {
	const mask = 1<<dequeueBits - 1
	return (uint64(head) << dequeueBits) |
		uint64(tail&mask)
}

// pushHead adds val at the head of the queue. It returns false if the
// queue is full.
func (d *bufDequeue) pushHead(val unsafe.Pointer) bool {
	var slot *unsafe.Pointer
	var starve uint8
	if atomic.LoadUint32(&d.starving) > 0 {
		runtime.Gosched()
	}
	for {
		ptrs := atomic.LoadUint64(&d.headTail)
		head, tail := d.unpack(ptrs)
		if (tail+uint32(len(d.vals)))&(1<<dequeueBits-1) == head {
			// Queue is full.
			return false
		}
		ptrs2 := d.pack(head+1, tail)
		if atomic.CompareAndSwapUint64(&d.headTail, ptrs, ptrs2) {
			slot = &d.vals[head&uint32(len(d.vals)-1)]
			if starve >= 3 && atomic.LoadUint32(&d.starving) > 0 {
				atomic.StoreUint32(&d.starving, 0)
			}
			break
		}
		starve++
		if starve >= 3 {
			atomic.StoreUint32(&d.starving, 1)
		}
	}
	// Publish the value atomically. Consumers may observe the slot as soon as
	// headTail advances, so a plain pointer write is a data race on weakly
	// ordered architectures (and under the race detector).
	atomic.StorePointer(slot, val)
	return true
}

// popTail removes and returns the element at the tail of the queue.
// It returns false if the queue is empty. It may be called by any
// number of consumers.
func (d *bufDequeue) popTail() (unsafe.Pointer, bool) {
	var val unsafe.Pointer
	var head, tail uint32
	for {
		ptrs := atomic.LoadUint64(&d.headTail)
		head, tail = d.unpack(ptrs)
		if tail == head {
			// Queue is empty.
			return nil, false
		}
		slot := &d.vals[tail&uint32(len(d.vals)-1)]
		val = atomic.LoadPointer(slot)
		if val != nil {
			// We now get a slot.
			if atomic.CompareAndSwapPointer(slot, val, nil) {
				break
				// Tell pushHead that we're done with this slot. Zeroing the
				// slot is also important so we don't leave behind references
				// that could keep this object live longer than necessary.
				//
				// We write to val first and then publish that we're done with
			}
		}
		// Maybe the value was taken by other goroutine or not push yet.
	}
	// At this point pushHead owns the slot.
	if tail < math.MaxUint32 {
		atomic.AddUint64(&d.headTail, 1)
	} else {
		atomic.AddUint64(&d.headTail, ^uint64(math.MaxUint32-1))
	}
	return val, true
}

// bufChain is a dynamically-sized version of bufDequeue.
//
// This is implemented as a doubly-linked list queue of poolDequeues
// where each dequeue is double the size of the previous one. Once a
// dequeue fills up, this allocates a New one and only ever pushes to
// the latest dequeue. Pops happen from the other end of the list and
// once a dequeue is exhausted, it gets removed from the list.
type bufChain struct {
	// head is the bufDequeue to push to. This is only accessed
	// by the producer, so doesn't need to be synchronized.
	head *bufChainElt

	// tail is the bufDequeue to popTail from. This is accessed
	// by consumers, so reads and writes must be atomic.
	tail     *bufChainElt
	newChain uint32
}

type bufChainElt struct {
	bufDequeue

	// next and prev link to the adjacent poolChainElts in this
	// bufChain.
	//
	// next is written atomically by the producer and read
	// atomically by the consumer. It only transitions from nil to
	// non-nil.
	//
	// prev is written atomically by the consumer and read
	// atomically by the producer. It only transitions from
	// non-nil to nil.
	next, prev *bufChainElt
}

func storePoolChainElt(pp **bufChainElt, v *bufChainElt) {
	atomic.StorePointer((*unsafe.Pointer)(unsafe.Pointer(pp)), unsafe.Pointer(v))
}

func loadPoolChainElt(pp **bufChainElt) *bufChainElt {
	return (*bufChainElt)(atomic.LoadPointer((*unsafe.Pointer)(unsafe.Pointer(pp))))
}

func (c *bufChain) new(initSize int) {
	// Initialize the chain.
	// initSize must be a power of 2
	d := new(bufChainElt)
	d.vals = make([]unsafe.Pointer, initSize)
	storePoolChainElt(&c.head, d)
	storePoolChainElt(&c.tail, d)
}

func (c *bufChain) pushHead(val unsafe.Pointer) {
startPush:
	for {
		if atomic.LoadUint32(&c.newChain) > 0 {
			runtime.Gosched()
		} else {
			break
		}
	}

	d := loadPoolChainElt(&c.head)

	if d.pushHead(val) {
		return
	}

	// The current dequeue is full. Allocate a New one of twice
	// the size.
	if atomic.CompareAndSwapUint32(&c.newChain, 0, 1) {
		newSize := len(d.vals) * 2
		if newSize >= dequeueLimit {
			// Can't make it any bigger.
			newSize = dequeueLimit
		}

		d2 := &bufChainElt{prev: d}
		d2.vals = make([]unsafe.Pointer, newSize)
		d2.pushHead(val)
		storePoolChainElt(&c.head, d2)
		storePoolChainElt(&d.next, d2)
		atomic.StoreUint32(&c.newChain, 0)
		return
	}
	goto startPush
}

func (c *bufChain) popTail() (unsafe.Pointer, bool) {
	d := loadPoolChainElt(&c.tail)
	if d == nil {
		return nil, false
	}

	for {
		// It's important that we load the next pointer
		// *before* popping the tail. In general, d may be
		// transiently empty, but if next is non-nil before
		// the TryPop and the TryPop fails, then d is permanently
		// empty, which is the only condition under which it's
		// safe to drop d from the chain.
		d2 := loadPoolChainElt(&d.next)

		if val, ok := d.popTail(); ok {
			return val, ok
		}

		if d2 == nil {
			// This is the only dequeue. It's empty right
			// now, but could be pushed to in the future.
			return nil, false
		}

		// The tail of the chain has been drained, so move on
		// to the next dequeue. Try to drop it from the chain
		// so the next TryPop doesn't have to look at the empty
		// dequeue again.
		if atomic.CompareAndSwapPointer((*unsafe.Pointer)(unsafe.Pointer(&c.tail)), unsafe.Pointer(d), unsafe.Pointer(d2)) {
			// We won the race. Clear the prev pointer so
			// the garbage collector can collect the empty
			// dequeue and so popHead doesn't back up
			// further than necessary.
			storePoolChainElt(&d2.prev, nil)
		}
		d = d2
	}
}
