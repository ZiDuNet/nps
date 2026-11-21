package file

import (
	"sync"
	"sync/atomic"
	"testing"

	"ehang.io/nps/lib/rate"
)

var userRuntimeTestID int64 = 10_000

func nextUserRuntimeTestID(t *testing.T) int {
	t.Helper()
	id := int(atomic.AddInt64(&userRuntimeTestID, 1))
	CleanupUserRuntime(id)
	t.Cleanup(func() {
		CleanupUserRuntime(id)
	})
	return id
}

func TestUserConnectionLimitIsSharedAcrossClients(t *testing.T) {
	userID := nextUserRuntimeTestID(t)
	SetUserRuntimeLimits(userID, LimitInt(2), nil)
	first := NewClient("first", false, false)
	second := NewClient("second", false, false)
	third := NewClient("third", false, false)
	first.UserId = userID
	second.UserId = userID
	third.UserId = userID

	if !first.GetConn() || !second.GetConn() {
		t.Fatal("the first two connections should fit the shared user limit")
	}
	if third.GetConn() {
		t.Fatal("a third client bypassed the shared two-connection user limit")
	}
	first.AddConn()
	if !third.GetConn() {
		t.Fatal("releasing one client connection should make the shared slot reusable")
	}
	second.AddConn()
	third.AddConn()
	if current, _ := userConnections(userID); current != 0 {
		t.Fatalf("shared user connection count = %d after release, want 0", current)
	}
}

func TestUserRuntimeSnapshotDoesNotCreateState(t *testing.T) {
	const userID = 9101
	CleanupUserRuntime(userID)
	t.Cleanup(func() { CleanupUserRuntime(userID) })
	if connections, limit, up, down := UserRuntimeSnapshot(userID); connections != 0 || limit != 0 || up != 0 || down != 0 {
		t.Fatalf("empty snapshot = %d/%d %d/%d", connections, limit, up, down)
	}
	if state := userRuntimeFor(userID, false); state != nil {
		t.Fatal("read-only snapshot created runtime state")
	}
	SetUserRuntimeLimits(userID, LimitInt(3), LimitInt64(rate.BpsFromKBPerSecond(128+256)))
	if connections, limit, _, _ := UserRuntimeSnapshot(userID); connections != 0 || limit != 3 {
		t.Fatalf("configured snapshot = %d/%d, want 0/3", connections, limit)
	}
}

func TestUserRuntimeLimitsPreserveThreeStateSemantics(t *testing.T) {
	userID := nextUserRuntimeTestID(t)

	SetUserRuntimeLimits(userID, nil, nil)
	if UserQuotaExcluded(userID, QuotaConnection) || UserQuotaExcluded(userID, QuotaBandwidth) {
		t.Fatal("nil limits must represent unlimited resources")
	}
	if owner, accounted, allowed := acquireUserConnection(userID); !allowed {
		t.Fatal("an unlimited user connection was rejected")
	} else if accounted {
		releaseUserConnection(owner)
	}

	SetUserRuntimeLimits(userID, LimitInt(0), LimitInt64(0))
	if !UserQuotaExcluded(userID, QuotaConnection) || !UserQuotaExcluded(userID, QuotaBandwidth) {
		t.Fatal("zero limits must represent excluded resources")
	}
	if _, _, allowed := acquireUserConnection(userID); allowed {
		t.Fatal("a user connection excluded by plan was admitted")
	}

	SetUserRuntimeLimits(userID, LimitInt(2), LimitInt64(rate.BpsFromKBPerSecond(64)))
	if UserQuotaExcluded(userID, QuotaConnection) || UserQuotaExcluded(userID, QuotaBandwidth) {
		t.Fatal("positive limits must represent limited resources")
	}
	if owner, accounted, allowed := acquireUserConnection(userID); !allowed {
		t.Fatal("a connection below the configured limit was rejected")
	} else if accounted {
		releaseUserConnection(owner)
	}
}

func TestUserConnectionAdmissionIsAtomicUnderConcurrency(t *testing.T) {
	userID := nextUserRuntimeTestID(t)
	SetUserRuntimeLimits(userID, LimitInt(5), nil)

	const workers = 64
	clients := make([]*Client, workers)
	for index := range clients {
		clients[index] = NewClient("concurrent", false, false)
		clients[index].UserId = userID
	}

	start := make(chan struct{})
	release := make(chan struct{})
	allAttempted := make(chan struct{})
	var attempted int32
	var accepted int32
	var wait sync.WaitGroup
	wait.Add(workers)
	for _, client := range clients {
		go func(client *Client) {
			defer wait.Done()
			<-start
			if client.GetConn() {
				atomic.AddInt32(&accepted, 1)
			}
			if atomic.AddInt32(&attempted, 1) == workers {
				close(allAttempted)
			}
			if client.NowConn > 0 {
				<-release
				client.AddConn()
			}
		}(client)
	}
	close(start)
	<-allAttempted
	if got := atomic.LoadInt32(&accepted); got != 5 {
		t.Fatalf("accepted %d concurrent connections for a five-connection user, want 5", got)
	}
	close(release)
	wait.Wait()
	if current, _ := userConnections(userID); current != 0 {
		t.Fatalf("shared user connection count = %d after concurrent release, want 0", current)
	}
}

func TestUserRuntimeLimitsConcurrentSetQueryAndAdmission(t *testing.T) {
	userID := nextUserRuntimeTestID(t)
	SetUserRuntimeLimits(userID, nil, nil)

	connectionLimits := []*int{nil, LimitInt(0), LimitInt(1)}
	bandwidthLimits := []*int64{nil, LimitInt64(0), LimitInt64(rate.BpsFromKBPerSecond(64))}

	const (
		writers    = 4
		readers    = 8
		iterations = 1000
	)

	start := make(chan struct{})
	stop := make(chan struct{})
	var writerWait sync.WaitGroup
	var readerWait sync.WaitGroup

	writerWait.Add(writers)
	for writer := 0; writer < writers; writer++ {
		go func(offset int) {
			defer writerWait.Done()
			<-start
			for index := 0; index < iterations; index++ {
				SetUserRuntimeLimits(
					userID,
					connectionLimits[(offset+index)%len(connectionLimits)],
					bandwidthLimits[(offset+index)%len(bandwidthLimits)],
				)
			}
		}(writer)
	}

	readerWait.Add(readers)
	for reader := 0; reader < readers; reader++ {
		go func() {
			defer readerWait.Done()
			<-start
			for {
				select {
				case <-stop:
					return
				default:
				}
				UserQuotaExcluded(userID, QuotaConnection)
				UserQuotaExcluded(userID, QuotaBandwidth)
				if owner, accounted, allowed := acquireUserConnection(userID); accounted && allowed {
					releaseUserConnection(owner)
				}
			}
		}()
	}

	close(start)
	writerWait.Wait()
	close(stop)
	readerWait.Wait()

	SetUserRuntimeLimits(userID, nil, nil)
	if current, _ := userConnections(userID); current != 0 {
		t.Fatalf("shared user connection count = %d after concurrent access, want 0", current)
	}
}

func TestUserBandwidthLimiterIsSharedAndUpdatesInPlace(t *testing.T) {
	userID := nextUserRuntimeTestID(t)
	SetUserRuntimeLimits(userID, nil, LimitInt64(rate.BpsFromKBPerSecond(128+256)))
	first := &Client{UserId: userID}
	second := &Client{UserId: userID}
	firstLimiter := UserRateLimiterForClient(first)
	secondLimiter := UserRateLimiterForClient(second)
	if firstLimiter == nil || firstLimiter != secondLimiter {
		t.Fatal("clients owned by the same user must share one bandwidth limiter")
	}
	if got, want := firstLimiter.Limit(), int64((128+256)*1024); got != want {
		t.Fatalf("initial user shared limit = %d bytes/s, want %d", got, want)
	}

	SetUserRuntimeLimits(userID, nil, LimitInt64(rate.BpsFromKBPerSecond(64)))
	if got, want := firstLimiter.Limit(), int64(64*1024); got != want {
		t.Fatalf("updated user shared limit = %d bytes/s, want %d", got, want)
	}
	if secondLimiter != UserRateLimiter(userID) {
		t.Fatal("existing connections must retain the shared live limiter after an edit")
	}
}

func TestLoweringUserConnectionLimitOnlyAffectsNewAdmissions(t *testing.T) {
	userID := nextUserRuntimeTestID(t)
	SetUserRuntimeLimits(userID, LimitInt(3), nil)
	first := &Client{UserId: userID}
	second := &Client{UserId: userID}
	third := &Client{UserId: userID}
	fourth := &Client{UserId: userID}
	if !first.GetConn() || !second.GetConn() || !third.GetConn() {
		t.Fatal("connections below the initial user limit were rejected")
	}

	SetUserRuntimeLimits(userID, LimitInt(1), nil)
	if fourth.GetConn() {
		t.Fatal("a new connection bypassed the lowered user limit")
	}
	first.AddConn()
	if fourth.GetConn() {
		t.Fatal("new admissions must still respect active connections above the lowered limit")
	}
	second.AddConn()
	third.AddConn()
	if !fourth.GetConn() {
		t.Fatal("new admission should recover after active connections fall below the lowered limit")
	}
	fourth.AddConn()
}
