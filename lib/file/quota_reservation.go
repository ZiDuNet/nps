package file

import (
	"errors"
	"sync"
)

// QuotaKind identifies one user-level allocation dimension. Reservations are
// deliberately separate from the persisted counters: they cover the short
// window between "the caller passed its quota check" and "the record has been
// stored", and are rebuilt from persisted data on startup.
type QuotaKind string

const (
	QuotaTunnel QuotaKind = "tunnel"
	QuotaHost   QuotaKind = "host"
	// Connection and bandwidth are runtime admission checks rather than
	// reserved entity slots. They share the kind type so dashboards and the
	// future subscription resolver can express all four dimensions uniformly.
	QuotaConnection QuotaKind = "connection"
	QuotaBandwidth  QuotaKind = "bandwidth"
)

var (
	quotaReservationMu sync.Mutex
	quotaReservations  = map[int]map[QuotaKind]int{}
)

// quotaMutationMu serializes entity creation with its quota reservation and
// persistence. A simply released reservation would briefly count the same
// record twice: once in the persisted usage query and once in the pending
// reservation. Holding this lock until CommitQuota removes that window and
// also keeps an owner reassignment from moving the reservation to another
// user between the check and the store.
var quotaMutationMu sync.Mutex

// ErrQuotaExceeded is returned when an allocation would exceed the user's
// persisted limit. Callers should surface it as a quota rejection, not as a
// persistence failure.
var ErrQuotaExceeded = errors.New("用户额度已用完")

func reservationKey(userID int, kind QuotaKind) (int, bool) {
	if userID <= 0 {
		return 0, false
	}
	return userID, true
}

// ReserveQuota atomically reserves one unit of a user-level quota. The caller
// must call ReleaseQuota if the subsequent creation fails. A successful
// creation converts the reservation back to zero only after the record has
// been stored, so concurrent creators cannot all pass the same stale count.
func (s *DbUtils) ReserveQuota(userID int, kind QuotaKind) error {
	if _, ok := reservationKey(userID, kind); !ok {
		return nil
	}
	if kind == QuotaConnection || kind == QuotaBandwidth {
		return errors.New("运行时额度不能使用实体预留接口")
	}
	user, err := s.GetUser(userID)
	if err != nil || user == nil {
		return errors.New("所属用户不存在")
	}
	user.RLock()
	maxTunnelNum, maxHostNum := user.MaxTunnelNum, user.MaxHostNum
	user.RUnlock()

	quotaReservationMu.Lock()
	defer quotaReservationMu.Unlock()
	byKind := quotaReservations[userID]
	if byKind == nil {
		byKind = map[QuotaKind]int{}
		quotaReservations[userID] = byKind
	}
	switch kind {
	case QuotaTunnel:
		if IntQuotaReached(s.GetUserTunnelNum(userID)+byKind[kind], maxTunnelNum) {
			return ErrQuotaExceeded
		}
	case QuotaHost:
		if IntQuotaReached(s.GetUserHostNum(userID)+byKind[kind], maxHostNum) {
			return ErrQuotaExceeded
		}
	default:
		return errors.New("未知额度类型")
	}
	byKind[kind]++
	return nil
}

// ReleaseQuota returns a reservation that did not result in a persisted
// record. It is safe to call after a failed validation or a failed store.
func (s *DbUtils) ReleaseQuota(userID int, kind QuotaKind) {
	if _, ok := reservationKey(userID, kind); !ok {
		return
	}
	quotaReservationMu.Lock()
	defer quotaReservationMu.Unlock()
	byKind := quotaReservations[userID]
	if byKind == nil {
		return
	}
	if byKind[kind] > 0 {
		byKind[kind]--
	}
	if byKind[kind] == 0 {
		delete(byKind, kind)
	}
	if len(byKind) == 0 {
		delete(quotaReservations, userID)
	}
}

// CommitQuota releases the in-memory reservation after the entity has been
// stored. Entity creation is serialized by the reservation lock through the
// store step, so the persisted count includes the new record by the time the
// reservation is dropped.
func (s *DbUtils) CommitQuota(userID int, kind QuotaKind) {
	s.ReleaseQuota(userID, kind)
}

func resetQuotaReservations() {
	quotaReservationMu.Lock()
	quotaReservations = map[int]map[QuotaKind]int{}
	quotaReservationMu.Unlock()
}
