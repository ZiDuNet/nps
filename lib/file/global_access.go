package file

import "ehang.io/nps/lib/acl"

// GlobalIPDenied evaluates the administrator-owned source policy using the
// same compiler as the proxy and Bridge checks. Invalid legacy rules are
// reported by the compiler and skipped, matching the historical behavior.
func (s *DbUtils) GlobalIPDenied(ipPort string) bool {
	if s == nil || s.JsonDb == nil {
		return false
	}
	return s.JsonDb.GlobalIPDenied(ipPort)
}

// GlobalIPDenied evaluates the global policy on a standalone JSON database.
// Keeping the method here is useful to listeners and tests that construct a
// database without installing the process-wide DbUtils singleton.
func (s *JsonDb) GlobalIPDenied(ipPort string) bool {
	if s == nil {
		return false
	}
	global := s.getGlobal()
	if global == nil {
		return false
	}
	if source := global.SourceACL(); source != nil {
		return !acl.AllowsString(ipPort, source, nil, nil)
	}
	return false
}
