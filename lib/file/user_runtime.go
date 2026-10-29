package file

import (
	"sync"
	"sync/atomic"

	"ehang.io/nps/lib/rate"
)

// userRuntimeState keeps limits that apply across every client owned by one
// dashboard user. It is intentionally runtime-only: active connections and
// token buckets must start clean after a restart, while the configured values
// remain persisted on User.
type userRuntimeState struct {
	maxConn int64
	nowConn int64
	rate    *rate.Directional
}

var userRuntimeRegistry = struct {
	sync.RWMutex
	states map[int]*userRuntimeState
}{
	states: make(map[int]*userRuntimeState),
}

func userRuntimeFor(userID int, create bool) *userRuntimeState {
	if userID <= 0 {
		return nil
	}
	userRuntimeRegistry.RLock()
	state := userRuntimeRegistry.states[userID]
	userRuntimeRegistry.RUnlock()
	if state != nil || !create {
		return state
	}
	userRuntimeRegistry.Lock()
	defer userRuntimeRegistry.Unlock()
	if state = userRuntimeRegistry.states[userID]; state == nil {
		state = &userRuntimeState{rate: rate.NewDirectional(0, 0)}
		userRuntimeRegistry.states[userID] = state
	}
	return state
}

// SetUserRuntimeLimits applies persistent user limits to the shared runtime
// object. Existing connections observe bandwidth changes immediately; a
// lowered connection limit applies to new admissions and does not disconnect
// connections that were already active.
func SetUserRuntimeLimits(userID, maxConn, uploadLimit, downloadLimit int) {
	state := userRuntimeFor(userID, true)
	if state == nil {
		return
	}
	if maxConn < 0 {
		maxConn = 0
	}
	atomic.StoreInt64(&state.maxConn, int64(maxConn))
	state.rate.SetLimits(limitBytes(uploadLimit), limitBytes(downloadLimit))
}

// CleanupUserRuntime forgets runtime counters for a deleted user. Existing
// connections remain safe because release operations ignore missing states.
func CleanupUserRuntime(userID int) {
	if userID <= 0 {
		return
	}
	userRuntimeRegistry.Lock()
	delete(userRuntimeRegistry.states, userID)
	userRuntimeRegistry.Unlock()
}

// acquireUserConnection reserves one cross-client connection slot. The first
// return value identifies the owner to release later, the second reports
// whether this reservation was accounted, and the third reports whether the
// connection is admitted. Positive user IDs create a default-unlimited state
// lazily so startup ordering cannot bypass a persisted user limit.
func acquireUserConnection(userID int) (int, bool, bool) {
	state := userRuntimeFor(userID, true)
	if state == nil {
		return 0, false, true
	}
	for {
		limit := atomic.LoadInt64(&state.maxConn)
		current := atomic.LoadInt64(&state.nowConn)
		if limit > 0 && current >= limit {
			return 0, false, false
		}
		if atomic.CompareAndSwapInt64(&state.nowConn, current, current+1) {
			// User settings may lower the limit concurrently. Roll back a
			// reservation that would exceed the newly observed ceiling.
			updatedLimit := atomic.LoadInt64(&state.maxConn)
			if updatedLimit > 0 && current+1 > updatedLimit {
				atomic.AddInt64(&state.nowConn, -1)
				return 0, false, false
			}
			return userID, true, true
		}
	}
}

func releaseUserConnection(userID int) {
	state := userRuntimeFor(userID, false)
	if state == nil {
		return
	}
	for {
		current := atomic.LoadInt64(&state.nowConn)
		if current <= 0 {
			return
		}
		if atomic.CompareAndSwapInt64(&state.nowConn, current, current-1) {
			return
		}
	}
}

// userConnections reports the current shared connection count. It is kept
// package-private for invariant tests; callers should use resource snapshots
// for user-facing status values.
func userConnections(userID int) (current, limit int64) {
	state := userRuntimeFor(userID, false)
	if state == nil {
		return 0, 0
	}
	return atomic.LoadInt64(&state.nowConn), atomic.LoadInt64(&state.maxConn)
}

// UserRuntimeSnapshot returns the shared connection and bandwidth counters for
// one dashboard user without creating a new runtime state as a side effect.
func UserRuntimeSnapshot(userID int) (connections, connectionLimit, uploadRate, downloadRate int64) {
	state := userRuntimeFor(userID, false)
	if state == nil {
		return 0, 0, 0, 0
	}
	connections = atomic.LoadInt64(&state.nowConn)
	connectionLimit = atomic.LoadInt64(&state.maxConn)
	if state.rate != nil {
		uploadRate, downloadRate = state.rate.Throughput()
	}
	return connections, connectionLimit, uploadRate, downloadRate
}

// UserRateLimiter returns the shared upload/download buckets for a user.
// Positive user IDs lazily receive an unlimited runtime state so a later
// settings update always applies to the same bucket object.
func UserRateLimiter(userID int) *rate.Directional {
	state := userRuntimeFor(userID, true)
	if state == nil {
		return nil
	}
	return state.rate
}

// UserRateLimiterForClient snapshots the current owner before returning the
// shared limiter, so a console edit cannot race a connection setup.
func UserRateLimiterForClient(client *Client) *rate.Directional {
	if client == nil {
		return nil
	}
	client.RLock()
	userID := client.UserId
	client.RUnlock()
	return UserRateLimiter(userID)
}
