package file

import "errors"

var ErrDatabaseUnavailable = errors.New("数据库未初始化")

// SetGlobalDashboardKey updates the administrator's topology credential
// without replacing any other global setting. The raw credential never enters
// the JSON store; callers provide its digest and encrypted-at-rest value.
func (s *DbUtils) SetGlobalDashboardKey(hash, ciphertext string) error {
	if s == nil || s.JsonDb == nil {
		return ErrDatabaseUnavailable
	}
	global := s.GetGlobal()
	if global == nil {
		global = &Glob{}
	}
	global.Lock()
	global.DashboardKeyHash = hash
	global.DashboardKeyCiphertext = ciphertext
	global.Unlock()
	s.JsonDb.setGlobal(global)
	s.JsonDb.StoreGlobalToJsonFile()
	return nil
}

// SetGlobalDashboardKeyHash is retained for callers that only need to manage
// the legacy hash field, including older integrations and focused tests.
func (s *DbUtils) SetGlobalDashboardKeyHash(hash string) error {
	if s == nil || s.JsonDb == nil {
		return ErrDatabaseUnavailable
	}
	global := s.GetGlobal()
	ciphertext := ""
	if global != nil {
		global.RLock()
		ciphertext = global.DashboardKeyCiphertext
		global.RUnlock()
	}
	return s.SetGlobalDashboardKey(hash, ciphertext)
}
