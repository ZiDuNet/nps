package install

import (
	"errors"
	"testing"
	"time"
)

func TestStartStoppedServiceDoesNotStopItAgain(t *testing.T) {
	phase := updateServiceStopped
	starts, stops := 0, 0
	ops := updateServiceOps{query: func() (updateServicePhase, error) { return phase, nil }, start: func() error { starts++; phase = updateServiceRunning; return nil }, stop: func() error { stops++; return errors.New("already stopped") }}
	if err := ensureUpdateService(ops, true, time.Second, 0, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if starts != 1 || stops != 0 {
		t.Fatalf("start=%d stop=%d", starts, stops)
	}
}

func TestStartWaitsForPendingStopAndPropagatesFailure(t *testing.T) {
	queries := 0
	started := false
	ops := updateServiceOps{query: func() (updateServicePhase, error) {
		queries++
		if queries == 1 {
			return updateServiceStopPending, nil
		}
		if started {
			return updateServiceRunning, nil
		}
		return updateServiceStopped, nil
	}, start: func() error { started = true; return nil }}
	if err := ensureUpdateService(ops, true, time.Second, 0, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if !started {
		t.Fatal("pending stop never restarted")
	}
	ops.query = func() (updateServicePhase, error) { return updateServiceStopped, nil }
	ops.start = func() error { return errors.New("access denied") }
	if err := ensureUpdateService(ops, true, time.Second, 0, time.Millisecond); err == nil {
		t.Fatal("start failure hidden")
	}
}

func TestStopServiceIsIdempotent(t *testing.T) {
	stops := 0
	ops := updateServiceOps{query: func() (updateServicePhase, error) { return updateServiceStopped, nil }, stop: func() error { stops++; return nil }}
	if err := ensureUpdateService(ops, false, time.Second, 0, 0); err != nil {
		t.Fatal(err)
	}
	if stops != 0 {
		t.Fatal("sent stop to already-stopped service")
	}
}
