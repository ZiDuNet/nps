package controllers

import (
	"sync/atomic"
	"testing"

	"ehang.io/nps/lib/file"
)

func TestParseLiveIDSetAcceptsCommaSeparatedValues(t *testing.T) {
	ids := parseLiveIDSet([]string{"1,2", "bad", "2", "3"})
	if len(ids) != 3 {
		t.Fatalf("parsed ids = %#v, want 1,2,3", ids)
	}
	for _, id := range []int{1, 2, 3} {
		if _, ok := ids[id]; !ok {
			t.Fatalf("parsed ids missing %d: %#v", id, ids)
		}
	}
}

func TestLiveClientSnapshotsHonorsAllowlist(t *testing.T) {
	const (
		visibleID   = 990001
		hiddenID    = 990002
		noDisplayID = 990003
	)
	visible := &file.Client{Id: visibleID, UserId: 42, MaxConn: 20, Flow: &file.Flow{}}
	hidden := &file.Client{Id: hiddenID, UserId: 42, MaxConn: 10, Flow: &file.Flow{}}
	noDisplay := &file.Client{Id: noDisplayID, UserId: 42, NoDisplay: true, Flow: &file.Flow{}}
	atomic.StoreInt32(&visible.NowConn, 3)
	atomic.StoreInt32(&hidden.NowConn, 1)
	db := file.GetDb().JsonDb.Clients
	db.Store(visibleID, visible)
	db.Store(hiddenID, hidden)
	db.Store(noDisplayID, noDisplay)
	t.Cleanup(func() {
		db.Delete(visibleID)
		db.Delete(hiddenID)
		db.Delete(noDisplayID)
	})

	rows := liveClientSnapshots(map[int]struct{}{visibleID: {}})
	if len(rows) != 1 || rows[0].ClientID != visibleID {
		t.Fatalf("visible live clients = %#v", rows)
	}
	if rows[0].Connections != 3 || rows[0].ConnectionLimit != 20 || rows[0].UserID != 42 {
		t.Fatalf("visible client snapshot = %#v", rows[0])
	}
}
