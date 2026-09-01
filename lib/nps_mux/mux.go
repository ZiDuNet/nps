package nps_mux

import (
	"errors"
	"io"
	"log"
	"math"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

const (
	muxPingFlag uint8 = iota
	muxNewConnOk
	muxNewConnFail
	muxNewMsg
	muxNewMsgPart
	muxMsgSendOk
	muxNewConn
	muxConnClose
	muxPingReturn
	muxPing            int32 = -1
	maximumSegmentSize       = poolSizeWindow
	maximumWindowSize        = 1 << 27 // 1<<31-1 TCP slide window size is very large,
	// we use 128M, reduce memory usage
)

type Mux struct {
	latency uint64 // we store latency in bits, but it's float64
	net.Listener
	conn               net.Conn
	fd                 *os.File
	connMap            *connMap
	newConnCh          chan *conn
	id                 int32
	closeChan          chan struct{}
	closeOnce          sync.Once
	sendMu             sync.RWMutex
	newConnMu          sync.RWMutex
	isClose            atomic.Bool
	counter            *latencyCounter
	bw                 *bandwidth
	pingCh             chan []byte
	pingCheckTime      uint32 // we check the ping per 5s
	pingCheckThreshold uint32
	connType           string
	writeQueue         priorityQueue
	newConnQueue       connQueue
}

func (s *Mux) IsClose() bool {
	return s.isClose.Load()
}

func NewMux(c net.Conn, connType string, pingCheckThreshold int) *Mux {
	//c.(*net.TCPConn).SetReadBuffer(0)
	//c.(*net.TCPConn).SetWriteBuffer(0)
	fd, err := getConnFd(c)
	if err != nil {
		log.Println(err)
	}
	var checkThreshold uint32
	if pingCheckThreshold <= 0 {
		if connType == "kcp" {
			checkThreshold = 20
		} else {
			checkThreshold = 60
		}
	} else {
		checkThreshold = uint32(pingCheckThreshold)
	}
	m := &Mux{
		conn:               c,
		fd:                 fd,
		connMap:            NewConnMap(),
		id:                 0,
		closeChan:          make(chan struct{}, 1),
		newConnCh:          make(chan *conn),
		bw:                 NewBandwidth(fd),
		connType:           connType,
		pingCh:             make(chan []byte),
		pingCheckThreshold: checkThreshold,
		counter:            newLatencyCounter(),
	}
	m.writeQueue.New()
	m.newConnQueue.New()
	//read session by flag
	m.readSession()
	//ping
	m.ping()
	m.writeSession()
	return m
}

func (s *Mux) NewConn() (*conn, error) {
	if s.isClose.Load() {
		return nil, errors.New("the mux has closed")
	}
	conn := NewConn(s.getId(), s)
	//it must be Set before send
	s.connMap.Set(conn.connId, conn)
	if !s.sendInfo(muxNewConn, conn.connId, nil) {
		_ = conn.Close()
		return nil, errors.New("the mux has closed")
	}
	//Set a timer timeout 120 second
	timer := time.NewTimer(time.Minute * 2)
	defer timer.Stop()
	select {
	case <-s.closeChan:
		_ = conn.Close()
		return nil, errors.New("the mux has closed")
	default:
	}
	select {
	case <-conn.connStatusOkCh:
		if s.isClose.Load() {
			_ = conn.Close()
			return nil, errors.New("the mux has closed")
		}
		return conn, nil
	case <-conn.connStatusFailCh:
		_ = conn.Close()
		return nil, errors.New("create connection rejected by remote")
	case <-s.closeChan:
		_ = conn.Close()
		return nil, errors.New("the mux has closed")
	case <-timer.C:
		_ = conn.Close()
	}
	return nil, errors.New("create connection fail，the server refused the connection")
}

func (s *Mux) Accept() (net.Conn, error) {
	const closedErr = "accpet error,the mux has closed"
	if s.isClose.Load() {
		return nil, errors.New(closedErr)
	}
	select {
	case <-s.closeChan:
		return nil, errors.New(closedErr)
	default:
	}
	select {
	case <-s.closeChan:
		return nil, errors.New(closedErr)
	case conn := <-s.newConnCh:
		if conn == nil {
			return nil, errors.New("accpet error,the conn has closed")
		}
		select {
		case <-s.closeChan:
			_ = conn.Close()
			return nil, errors.New(closedErr)
		default:
		}
		return conn, nil
	}
}

func (s *Mux) Addr() net.Addr {
	if s.conn == nil {
		return nil
	}
	return s.conn.LocalAddr()
}

func (s *Mux) sendInfo(flag uint8, id int32, data interface{}) bool {
	var err error
	pack := muxPack.Get()
	err = pack.Set(flag, id, data)
	if err != nil {
		releaseMuxPack(pack)
		log.Println("mux: New Pack err", err)
		_ = s.Close()
		return false
	}
	s.sendMu.RLock()
	if s.isClose.Load() {
		s.sendMu.RUnlock()
		releaseMuxPack(pack)
		return false
	}
	s.writeQueue.Push(pack)
	s.sendMu.RUnlock()
	return true
}

func (s *Mux) writeSession() {
	go func() {
		defer s.releaseWriteQueue()
		for {
			if s.isClose.Load() {
				break
			}
			pack := s.writeQueue.Pop()
			if pack == nil {
				return
			}
			if s.isClose.Load() {
				releaseMuxPack(pack)
				break
			}
			//if pack.flag == muxNewMsg || pack.flag == muxNewMsgPart {
			//	if pack.length >= 100 {
			//		log.Println("write session id", pack.id, "\n", string(pack.content[:100]))
			//	} else {
			//		log.Println("write session id", pack.id, "\n", string(pack.content[:pack.length]))
			//	}
			//}
			err := pack.Pack(s.conn)
			muxPack.Put(pack)
			if err != nil {
				log.Println("mux: Pack err", err)
				_ = s.Close()
				break
			}
		}
	}()
}

func (s *Mux) ping() {
	go func() {
		now, _ := time.Now().UTC().MarshalText()
		s.sendInfo(muxPingFlag, muxPing, now)
		// send the ping flag and Get the latency first
		ticker := time.NewTicker(time.Second * 5)
		defer ticker.Stop()
		for {
			if s.isClose.Load() {
				break
			}
			select {
			case <-ticker.C:
			case <-s.closeChan:
				return
			}
			checkTime := atomic.LoadUint32(&s.pingCheckTime)
			if checkTime > s.pingCheckThreshold {
				log.Println("mux: ping time out, checktime", checkTime, "threshold", s.pingCheckThreshold)
				_ = s.Close()
				// more than limit times not receive the ping return package,
				// mux conn is damaged, maybe a packet drop, close it
				break
			}
			now, _ = time.Now().UTC().MarshalText()
			s.sendInfo(muxPingFlag, muxPing, now)
			atomic.AddUint32(&s.pingCheckTime, 1)
		}
		return
	}()

	go func() {
		var now time.Time
		var data []byte
		for {
			if s.isClose.Load() {
				break
			}
			select {
			case data = <-s.pingCh:
				atomic.StoreUint32(&s.pingCheckTime, 0)
			case <-s.closeChan:
				return
			}
			_ = now.UnmarshalText(data)
			latency := time.Now().UTC().Sub(now).Seconds()
			if latency > 0 {
				atomic.StoreUint64(&s.latency, math.Float64bits(s.counter.Latency(latency)))
				// convert float64 to bits, store it atomic
				//log.Println("ping", math.Float64frombits(atomic.LoadUint64(&s.latency)))
			}
			if cap(data) > 0 {
				windowBuff.Put(data)
			}
		}
	}()
}

func (s *Mux) readSession() {
	go func() {
		defer s.releaseNewConnQueue()
		var connection *conn
		for {
			if s.isClose.Load() {
				break
			}
			connection = s.newConnQueue.Pop()
			if connection == nil {
				return
			}
			if s.isClose.Load() {
				_ = connection.Close()
				break // make sure that is closed
			}
			s.connMap.Set(connection.connId, connection) //it has been Set before send ok
			select {
			case s.newConnCh <- connection:
			case <-s.closeChan:
				_ = connection.Close()
				return
			}
			s.sendInfo(muxNewConnOk, connection.connId, nil)
		}
	}()
	go func() {
		var pack *muxPackager
		var l uint16
		var err error
		for {
			if s.isClose.Load() {
				return
			}
			pack = muxPack.Get()
			s.bw.StartRead()
			if l, err = pack.UnPack(s.conn); err != nil {
				log.Println("mux: read session unpack from connection err", err)
				releaseMuxPack(pack)
				_ = s.Close()
				break
			}
			s.bw.SetCopySize(l)
			//if pack.flag == muxNewMsg || pack.flag == muxNewMsgPart {
			//	if pack.length >= 100 {
			//		log.Printf("read session id %d pointer %p\n%v", pack.id, pack.content, string(pack.content[:100]))
			//	} else {
			//		log.Printf("read session id %d pointer %p\n%v", pack.id, pack.content, string(pack.content[:pack.length]))
			//	}
			//}
			switch pack.flag {
			case muxNewConn: //New connection
				connection := NewConn(pack.id, s)
				s.newConnMu.RLock()
				if s.isClose.Load() {
					s.newConnMu.RUnlock()
					_ = connection.Close()
					releaseMuxPack(pack)
					return
				}
				s.newConnQueue.Push(connection)
				s.newConnMu.RUnlock()
				releaseMuxPack(pack)
				continue
			case muxPingFlag: //ping
				s.sendInfo(muxPingReturn, muxPing, pack.content)
				releaseMuxPack(pack)
				continue
			case muxPingReturn:
				data := pack.content
				pack.content = nil // ownership moves to the ping consumer
				select {
				case s.pingCh <- data:
				case <-s.closeChan:
					windowBuff.Put(data)
					muxPack.Put(pack)
					return
				}
				muxPack.Put(pack)
				continue
			}
			if connection, ok := s.connMap.Get(pack.id); ok && !connection.isClose.Load() {
				switch pack.flag {
				case muxNewMsg, muxNewMsgPart: //New msg from remote connection
					err = s.newMsg(connection, pack)
					if err == nil {
						pack.content = nil // receiveWindow now owns the payload buffer
					}
					releaseMuxPack(pack)
					if err != nil {
						log.Println("mux: read session connection New msg err", err)
						_ = connection.Close()
					}
					continue
				case muxNewConnOk: //connection ok
					select {
					case connection.connStatusOkCh <- struct{}{}:
					case <-s.closeChan:
					default:
					}
					releaseMuxPack(pack)
					continue
				case muxNewConnFail:
					select {
					case connection.connStatusFailCh <- struct{}{}:
					case <-s.closeChan:
					default:
					}
					releaseMuxPack(pack)
					continue
				case muxMsgSendOk:
					if connection.isClose.Load() {
						releaseMuxPack(pack)
						continue
					}
					connection.sendWindow.SetSize(pack.window)
					releaseMuxPack(pack)
					continue
				case muxConnClose: //close the connection
					connection.closingFlag.Store(true)
					connection.receiveWindow.Stop() // close signal to receive window
					_ = connection.Close()
					releaseMuxPack(pack)
					continue
				}
			} else if pack.flag == muxConnClose {
				releaseMuxPack(pack)
				continue
			}
			releaseMuxPack(pack)
		}
	}()
}

// releaseMuxPack returns a decoded packet and any payload it still owns to
// their pools. Callers that transfer content ownership clear pack.content
// before invoking this helper.
func releaseMuxPack(pack *muxPackager) {
	if pack == nil {
		return
	}
	if pack.content != nil {
		windowBuff.Put(pack.content)
		pack.content = nil
	}
	if pack.buf != nil {
		windowBuff.Put(pack.buf)
		pack.buf = nil
	}
	muxPack.Put(pack)
}

func (s *Mux) newMsg(connection *conn, pack *muxPackager) (err error) {
	if connection.isClose.Load() {
		err = io.ErrClosedPipe
		return
	}
	//insert into queue
	if pack.flag == muxNewMsgPart {
		err = connection.receiveWindow.Write(pack.content, pack.length, true, pack.id)
	}
	if pack.flag == muxNewMsg {
		err = connection.receiveWindow.Write(pack.content, pack.length, false, pack.id)
	}
	return
}

func (s *Mux) Close() (err error) {
	s.sendMu.Lock()
	if !s.isClose.CompareAndSwap(false, true) {
		s.sendMu.Unlock()
		return errors.New("the mux has closed")
	}
	s.closeOnce.Do(func() { close(s.closeChan) })
	// Stop producers before releasing queued packets. sendInfo holds the read
	// lock while publishing, so no packet can be enqueued after this point.
	s.writeQueue.Stop()
	s.sendMu.Unlock()
	s.newConnMu.Lock()
	s.newConnQueue.Stop()
	s.newConnMu.Unlock()
	log.Println("close mux")
	s.connMap.Close()
	// newConnCh intentionally remains open. Accept selects closeChan, while
	// the reader may still be finishing a send; closing it here would race with
	// that sender and panic.
	// while target host close socket without finish steps, conn.Close method maybe blocked
	// and tcp status change to CLOSE WAIT or TIME WAIT, so we close it in other goroutine
	if s.conn != nil {
		_ = s.conn.SetDeadline(time.Now().Add(time.Second * 5))
		go s.conn.Close()
	}
	if s.fd != nil {
		_ = s.fd.Close()
		s.fd = nil
	}
	return
}

func (s *Mux) releaseWriteQueue() {
	for {
		pack := s.writeQueue.TryPop()
		if pack == nil {
			break
		}
		releaseMuxPack(pack)
	}
}

func (s *Mux) releaseNewConnQueue() {
	for {
		connection := s.newConnQueue.TryPop()
		if connection == nil {
			break
		}
		_ = connection.Close()
	}
}

// Get New connId as unique flag
func (s *Mux) getId() (id int32) {
	for {
		// Avoid a plain read racing with atomic increments, and only reset the
		// counter if this goroutine still owns the observed value.
		current := atomic.LoadInt32(&s.id)
		if int64(math.MaxInt32)-int64(current) < 10000 {
			if !atomic.CompareAndSwapInt32(&s.id, current, 0) {
				continue
			}
		}
		id = atomic.AddInt32(&s.id, 1)
		if _, ok := s.connMap.Get(id); !ok {
			return id
		}
	}
}

type bandwidth struct {
	readBandwidth uint64 // store in bits, but it's float64
	readStart     time.Time
	lastReadStart time.Time
	bufLength     uint32
	fd            *os.File
	calcThreshold uint32
}

func NewBandwidth(fd *os.File) *bandwidth {
	return &bandwidth{fd: fd}
}

func (Self *bandwidth) StartRead() {
	if Self.readStart.IsZero() {
		Self.readStart = time.Now()
	}
	if Self.bufLength >= Self.calcThreshold {
		Self.lastReadStart, Self.readStart = Self.readStart, time.Now()
		Self.calcBandWidth()
	}
}

func (Self *bandwidth) SetCopySize(n uint16) {
	Self.bufLength += uint32(n)
}

func (Self *bandwidth) calcBandWidth() {
	t := Self.readStart.Sub(Self.lastReadStart)
	bufferSize, err := sysGetSock(Self.fd)
	if err != nil {
		log.Println(err)
		Self.bufLength = 0
		return
	}
	if Self.bufLength >= uint32(bufferSize) {
		atomic.StoreUint64(&Self.readBandwidth, math.Float64bits(float64(Self.bufLength)/t.Seconds()))
		// calculate the whole socket buffer, the time meaning to fill the buffer
	} else {
		Self.calcThreshold = uint32(bufferSize)
	}
	// socket buffer size is bigger than bufLength, so we don't calculate it
	Self.bufLength = 0
}

func (Self *bandwidth) Get() (bw float64) {
	// The zero value, 0 for numeric types
	bw = math.Float64frombits(atomic.LoadUint64(&Self.readBandwidth))
	if bw <= 0 {
		bw = 0
	}
	return
}

const counterBits = 4
const counterMask = 1<<counterBits - 1

func newLatencyCounter() *latencyCounter {
	return &latencyCounter{
		buf:     make([]float64, 1<<counterBits, 1<<counterBits),
		headMin: 0,
	}
}

type latencyCounter struct {
	buf []float64 //buf is a fixed length ring buffer,
	// if buffer is full, New value will replace the oldest one.
	headMin uint8 //head indicate the head in ring buffer,
	// in meaning, slot in list will be replaced;
	// min indicate this slot value is minimal in list.

	// we delineate the effective range with three times the minimum latency
	// average of effective latency for all current data as a mux latency
	mu sync.Mutex
}

func (Self *latencyCounter) unpack(idxs uint8) (head, min uint8) {
	head = (idxs >> counterBits) & counterMask
	// we Set head is 4 bits
	min = idxs & counterMask
	return
}

func (Self *latencyCounter) pack(head, min uint8) uint8 {
	return head<<counterBits |
		min&counterMask
}

func (Self *latencyCounter) add(value float64) {
	head, min := Self.unpack(Self.headMin)
	Self.buf[head] = value
	if head == min {
		min = Self.minimal()
		//if head equals min, means the min slot already be replaced,
		// so we need to find another minimal value in the list,
		// and change the min indicator
	}
	if Self.buf[min] > value {
		min = head
	}
	head++
	Self.headMin = Self.pack(head, min)
}

func (Self *latencyCounter) minimal() (min uint8) {
	val := math.Inf(1)
	found := false
	var i uint8
	for i = 0; i < uint8(len(Self.buf)); i++ {
		if Self.buf[i] > 0 && Self.buf[i] < val {
			val = Self.buf[i]
			min = i
			found = true
		}
	}
	if !found {
		return 0
	}
	return
}

func (Self *latencyCounter) Latency(value float64) (latency float64) {
	Self.mu.Lock()
	defer Self.mu.Unlock()
	if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	Self.add(value)
	latency = Self.countSuccess()
	return
}

const lossRatio = 3

func (Self *latencyCounter) countSuccess() (successRate float64) {
	var i, success uint8
	_, min := Self.unpack(Self.headMin)
	for i = 0; i < uint8(len(Self.buf)); i++ {
		if Self.buf[i] <= lossRatio*Self.buf[min] && Self.buf[i] > 0 {
			success++
			successRate += Self.buf[i]
		}
	}
	// counting all the data in the ring buf, except zero
	if success == 0 {
		return 0
	}
	successRate = successRate / float64(success)
	return
}
