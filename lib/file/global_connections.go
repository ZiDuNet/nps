package file

import "sync/atomic"

// The global connection counter is deliberately kept outside the JSON model:
// active connections are runtime state and must never survive a restart.
var (
	globalMaxConn int64
	globalNowConn int64
)

// SetGlobalMaxConn changes admission for new forwarding connections. Existing
// connections are not disconnected; lowering the limit only affects future
// attempts until the active count drops below it.
func SetGlobalMaxConn(limit int) {
	if limit < 0 {
		limit = 0
	}
	atomic.StoreInt64(&globalMaxConn, int64(limit))
}

// GlobalConnections reports the active forwarding slots and configured limit.
func GlobalConnections() (active, limit int64) {
	return atomic.LoadInt64(&globalNowConn), atomic.LoadInt64(&globalMaxConn)
}

// acquireGlobalConn reserves one global slot. A zero limit means unlimited,
// but the active counter is still maintained for monitoring.
func acquireGlobalConn() bool {
	for {
		limit := atomic.LoadInt64(&globalMaxConn)
		current := atomic.LoadInt64(&globalNowConn)
		if limit > 0 && current >= limit {
			return false
		}
		if atomic.CompareAndSwapInt64(&globalNowConn, current, current+1) {
			// A concurrent settings update may lower the limit between the check
			// and the reservation. Re-check and roll back rather than allowing a
			// connection over the newly enforced ceiling.
			if limit > 0 && atomic.LoadInt64(&globalMaxConn) > 0 && current+1 > atomic.LoadInt64(&globalMaxConn) {
				atomic.AddInt64(&globalNowConn, -1)
				return false
			}
			return true
		}
	}
}

func releaseGlobalConn() {
	for {
		current := atomic.LoadInt64(&globalNowConn)
		if current <= 0 {
			return
		}
		if atomic.CompareAndSwapInt64(&globalNowConn, current, current-1) {
			return
		}
	}
}
