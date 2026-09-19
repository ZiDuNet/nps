package file

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientGetConnReservesSlotsAtomically(t *testing.T) {
	client := NewClient("test-vkey", false, false)
	client.MaxConn = 4
	const workers = 64

	start := make(chan struct{})
	release := make(chan struct{})
	var accepted int32
	var attempted int32
	allAttempted := make(chan struct{})
	var group sync.WaitGroup
	group.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer group.Done()
			<-start
			reserved := client.GetConn()
			if reserved {
				atomic.AddInt32(&accepted, 1)
			}
			if atomic.AddInt32(&attempted, 1) == workers {
				close(allAttempted)
			}
			if reserved {
				// Accepted callers hold their slot until every worker has tried.
				<-release
				client.AddConn()
			}
		}()
	}
	close(start)
	<-allAttempted
	if got := atomic.LoadInt32(&accepted); got != 4 {
		t.Fatalf("accepted %d concurrent connections, want exactly 4", got)
	}
	close(release)
	group.Wait()
	if got := atomic.LoadInt32(&client.NowConn); got != 0 {
		t.Fatalf("connection slots were not released, NowConn=%d", got)
	}
}

func TestFlowRateSnapshotUsesCounterDelta(t *testing.T) {
	flow := &Flow{InletFlow: 100, ExportFlow: 40}
	if inRate, outRate := flow.RateSnapshot(); inRate != 0 || outRate != 0 {
		t.Fatalf("first rate sample = (%d, %d), want zero baseline", inRate, outRate)
	}
	flow.Lock()
	flow.rateSampleAt = time.Now().Add(-time.Second)
	flow.rateSampleIn = 100
	flow.rateSampleOut = 40
	flow.Unlock()
	flow.Add(200, 80)
	inRate, outRate := flow.RateSnapshot()
	if inRate < 100 || outRate < 30 {
		t.Fatalf("rate sample = (%d, %d), want positive counter deltas", inRate, outRate)
	}
}

func TestFlowLimitUsesDirectionalTotal(t *testing.T) {
	flow := &Flow{FlowLimit: 1}
	flow.Add(600<<10, 0)
	flow.Add(0, 600<<10)
	if !flow.Exceeded() {
		t.Fatal("combined inbound and outbound flow should exceed one MiB")
	}
	if got := flow.Total(); got != 1200<<10 {
		t.Fatalf("total flow = %d, want %d", got, 1200<<10)
	}
}

func TestFlowMigratesLegacyMirroredCountersWithoutDoublingQuota(t *testing.T) {
	flow := &Flow{InletFlow: 900 << 10, ExportFlow: 900 << 10, FlowLimit: 1}
	if !flow.NormalizeLegacyAccounting() {
		t.Fatal("expected legacy flow migration")
	}
	inlet, export, _ := flow.Snapshot()
	if inlet != 0 || export != 0 {
		t.Fatalf("directional counters after migration = (%d, %d), want zero", inlet, export)
	}
	if got := flow.LegacySnapshot(); got != 900<<10 {
		t.Fatalf("legacy total = %d, want %d", got, 900<<10)
	}
	if flow.Exceeded() {
		t.Fatal("900 KiB historical usage must not exceed a 1 MiB quota")
	}
	flow.Add(200<<10, 0)
	if !flow.Exceeded() {
		t.Fatal("historical and new directional traffic should share the quota")
	}
}
