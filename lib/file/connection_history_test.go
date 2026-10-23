package file

import (
	"encoding/json"
	"sync"
	"testing"
	"time"
)

func TestConnectionHistoryPairsSessionsAndIgnoresDuplicateTransitions(t *testing.T) {
	SetConnectionHistoryLimit(5)
	t.Cleanup(func() { SetConnectionHistoryLimit(5) })

	history := new(ConnectionHistory)
	start := time.Date(2026, 10, 20, 1, 2, 3, 0, time.UTC)
	history.record(false, start) // An offline event without an online session is ignored.
	history.record(true, start)
	history.record(true, start.Add(time.Second))
	history.record(false, start.Add(2*time.Second))
	history.record(false, start.Add(3*time.Second))

	got := history.snapshot()
	if len(got) != 1 {
		t.Fatalf("session count = %d, want 1: %+v", len(got), got)
	}
	if got[0].OnlineTime != "2026-10-20 01:02:03" || got[0].OfflineTime != "2026-10-20 01:02:05" {
		t.Fatalf("session = %+v, want paired online/offline timestamps", got[0])
	}
}

func TestConnectionHistoryKeepsConfiguredNumberOfRecentSessions(t *testing.T) {
	SetConnectionHistoryLimit(2)
	t.Cleanup(func() { SetConnectionHistoryLimit(5) })

	history := new(ConnectionHistory)
	start := time.Date(2026, 10, 20, 1, 0, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		online := start.Add(time.Duration(i*2) * time.Minute)
		history.record(true, online)
		history.record(false, online.Add(time.Minute))
	}

	got := history.snapshot()
	if len(got) != 2 {
		t.Fatalf("retained sessions = %d, want 2: %+v", len(got), got)
	}
	if got[0].OnlineTime != "2026-10-20 01:04:00" || got[1].OnlineTime != "2026-10-20 01:06:00" {
		t.Fatalf("retained sessions are not the newest two: %+v", got)
	}
}

func TestConnectionHistoryRoundTripAndLegacyFormat(t *testing.T) {
	SetConnectionHistoryLimit(5)
	t.Cleanup(func() { SetConnectionHistoryLimit(5) })

	var history ConnectionHistory
	if err := json.Unmarshal([]byte(`[{"OnlineTime":"2026-10-20 01:00:00","OfflineTime":"2026-10-20 01:05:00"},{"OnlineTime":"2026-10-20 02:00:00"}]`), &history); err != nil {
		t.Fatalf("unmarshal current history: %v", err)
	}
	// A process restart cannot assume that the previously-open session is still online.
	history.record(false, time.Date(2026, 10, 20, 2, 1, 0, 0, time.UTC))
	history.record(true, time.Date(2026, 10, 20, 3, 0, 0, 0, time.UTC))
	history.record(false, time.Date(2026, 10, 20, 3, 5, 0, 0, time.UTC))

	raw, err := json.Marshal(&history)
	if err != nil {
		t.Fatalf("marshal history: %v", err)
	}
	var roundTrip []ConnectionEvent
	if err := json.Unmarshal(raw, &roundTrip); err != nil {
		t.Fatalf("unmarshal round-tripped history: %v", err)
	}
	if len(roundTrip) != 3 || roundTrip[1].OfflineTime != "" || roundTrip[2].OfflineTime == "" {
		t.Fatalf("round-tripped history = %+v", roundTrip)
	}

	var legacy ConnectionHistory
	legacyJSON := `[{"Time":"2026-10-19 23:00:00","Online":true},{"Time":"2026-10-19 23:05:00","Online":false}]`
	if err := json.Unmarshal([]byte(legacyJSON), &legacy); err != nil {
		t.Fatalf("unmarshal legacy history: %v", err)
	}
	got := legacy.snapshot()
	if len(got) != 1 || got[0].OnlineTime != "2026-10-19 23:00:00" || got[0].OfflineTime != "2026-10-19 23:05:00" {
		t.Fatalf("legacy history = %+v", got)
	}
}

func TestConnectionHistoryCanBeDisabledAndReenabled(t *testing.T) {
	SetConnectionHistoryLimit(0)
	t.Cleanup(func() { SetConnectionHistoryLimit(5) })

	history := new(ConnectionHistory)
	history.record(true, time.Now())
	if got := history.snapshot(); len(got) != 0 {
		t.Fatalf("disabled history retained %d sessions", len(got))
	}

	SetConnectionHistoryLimit(1)
	history.record(false, time.Now())
	history.record(true, time.Now())
	if got := history.snapshot(); len(got) != 1 || got[0].OnlineTime == "" {
		t.Fatalf("history did not resume after re-enabling: %+v", got)
	}
}

func TestConnectionHistoryConcurrentRecordAndSnapshot(t *testing.T) {
	SetConnectionHistoryLimit(5)
	t.Cleanup(func() { SetConnectionHistoryLimit(5) })

	history := new(ConnectionHistory)
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				history.record((worker+i)%2 == 0, time.Now())
				_ = history.snapshot()
				if _, err := json.Marshal(history); err != nil {
					t.Errorf("marshal history: %v", err)
					return
				}
			}
		}(worker)
	}
	wg.Wait()
	if got := history.snapshot(); len(got) > 5 {
		t.Fatalf("history grew beyond configured limit: %d", len(got))
	}
}
