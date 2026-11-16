package controllers

import (
	"testing"
	"time"
)

func TestVersionCheckPayloadExposesLatestAndUpdateFlag(t *testing.T) {
	serverVersionCheck.mu.Lock()
	previousLatest := serverVersionCheck.latest
	previousCheckedAt := serverVersionCheck.checkedAt
	previousErr := serverVersionCheck.errMsg
	serverVersionCheck.latest = "v999.0.0"
	serverVersionCheck.checkedAt = time.Now()
	serverVersionCheck.errMsg = ""
	serverVersionCheck.mu.Unlock()
	t.Cleanup(func() {
		serverVersionCheck.mu.Lock()
		serverVersionCheck.latest = previousLatest
		serverVersionCheck.checkedAt = previousCheckedAt
		serverVersionCheck.errMsg = previousErr
		serverVersionCheck.mu.Unlock()
	})

	payload := versionCheckPayload()
	if got := payload["latest"]; got != "v999.0.0" {
		t.Fatalf("payload latest = %#v, want v999.0.0", got)
	}
	if got := payload["update_available"]; got != true {
		t.Fatalf("payload update_available = %#v, want true", got)
	}
}
