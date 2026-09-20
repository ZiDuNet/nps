package controllers

import (
	"os"
	"path/filepath"
	"testing"

	"ehang.io/nps/web/audit"
)

// Keep controller tests isolated from a host installation's /etc/nps path.
// Production startup configures the real journal; tests only need to verify
// controller behavior without writing outside the temporary test directory.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "nps-controller-audit-")
	if err != nil {
		os.Exit(1)
	}
	audit.Configure(filepath.Join(dir, "audit.log"))
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
