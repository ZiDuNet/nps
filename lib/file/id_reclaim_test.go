package file

import (
	"sync/atomic"
	"testing"
)

func TestReclaimPersistentIDOnlyResetsAnEmptyTable(t *testing.T) {
	db := &JsonDb{}
	db.Tasks.Store(7, &Tunnel{Id: 7})
	atomic.StoreInt32(&db.TaskIncreaseId, 7)

	db.reclaimPersistentID(&db.TaskIncreaseId, &db.Tasks)
	if got := atomic.LoadInt32(&db.TaskIncreaseId); got != 7 {
		t.Fatalf("counter reset while records remained: %d", got)
	}

	db.Tasks.Delete(7)
	db.reclaimPersistentID(&db.TaskIncreaseId, &db.Tasks)
	if got := atomic.LoadInt32(&db.TaskIncreaseId); got != 0 {
		t.Fatalf("counter after deleting the last record = %d, want 0", got)
	}
}
