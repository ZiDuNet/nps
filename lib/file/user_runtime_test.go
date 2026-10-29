package file

import (
	"sync"
	"sync/atomic"
	"testing"
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
	SetUserRuntimeLimits(userID, 2, 0, 0)
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
	SetUserRuntimeLimits(userID, 3, 128, 256)
	if connections, limit, _, _ := UserRuntimeSnapshot(userID); connections != 0 || limit != 3 {
		t.Fatalf("configured snapshot = %d/%d, want 0/3", connections, limit)
	}
}

func TestUserConnectionAdmissionIsAtomicUnderConcurrency(t *testing.T) {
	userID := nextUserRuntimeTestID(t)
	SetUserRuntimeLimits(userID, 5, 0, 0)

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

func TestUserBandwidthLimiterIsSharedAndUpdatesInPlace(t *testing.T) {
	userID := nextUserRuntimeTestID(t)
	SetUserRuntimeLimits(userID, 0, 128, 256)
	first := &Client{UserId: userID}
	second := &Client{UserId: userID}
	firstLimiter := UserRateLimiterForClient(first)
	secondLimiter := UserRateLimiterForClient(second)
	if firstLimiter == nil || firstLimiter != secondLimiter {
		t.Fatal("clients owned by the same user must share one bandwidth limiter")
	}
	upload, download := firstLimiter.Limits()
	if upload != 128*1024 || download != 256*1024 {
		t.Fatalf("initial user limits = %d/%d bytes/s, want %d/%d", upload, download, 128*1024, 256*1024)
	}

	SetUserRuntimeLimits(userID, 0, 64, 0)
	upload, download = firstLimiter.Limits()
	if upload != 64*1024 || download != 0 {
		t.Fatalf("updated user limits = %d/%d bytes/s, want %d/0", upload, download, 64*1024)
	}
	if secondLimiter != UserRateLimiter(userID) {
		t.Fatal("existing connections must retain the shared live limiter after an edit")
	}
}

func TestLoweringUserConnectionLimitOnlyAffectsNewAdmissions(t *testing.T) {
	userID := nextUserRuntimeTestID(t)
	SetUserRuntimeLimits(userID, 3, 0, 0)
	first := &Client{UserId: userID}
	second := &Client{UserId: userID}
	third := &Client{UserId: userID}
	fourth := &Client{UserId: userID}
	if !first.GetConn() || !second.GetConn() || !third.GetConn() {
		t.Fatal("connections below the initial user limit were rejected")
	}

	SetUserRuntimeLimits(userID, 1, 0, 0)
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
