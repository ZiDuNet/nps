package file

import "errors"

var ErrDatabaseUnavailable = errors.New("数据库未初始化")

// SetGlobalDashboardKeyHash updates the administrator's topology credential
// without replacing any other global setting. The raw credential never enters
// the JSON store; callers must provide its SHA-256 digest.
func (s *DbUtils) SetGlobalDashboardKeyHash(hash string) error {
	if s == nil || s.JsonDb == nil {
		return ErrDatabaseUnavailable
	}
	global := s.GetGlobal()
	if global == nil {
		global = &Glob{}
	}
	global.Lock()
	global.DashboardKeyHash = hash
	global.Unlock()
	s.JsonDb.setGlobal(global)
	s.JsonDb.StoreGlobalToJsonFile()
	return nil
}
