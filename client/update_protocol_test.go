package client

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/config"
	"ehang.io/nps/lib/conn"
)

func preserveUpdateMetadata(t *testing.T) {
	t.Helper()
	updaterMetadata.RLock()
	capability, handler, result := updaterMetadata.capability, updaterMetadata.handler, updaterMetadata.result
	updaterMetadata.RUnlock()
	remoteUpdateHandler.RLock()
	legacy := remoteUpdateHandler.fn
	remoteUpdateHandler.RUnlock()
	t.Cleanup(func() {
		updaterMetadata.Lock()
		updaterMetadata.capability, updaterMetadata.handler, updaterMetadata.result = capability, handler, result
		updaterMetadata.Unlock()
		SetRemoteUpdateHandler(legacy)
	})
}

func TestUpdateCapabilityDefaultsAndLocalOptOut(t *testing.T) {
	preserveUpdateMetadata(t)
	ConfigureRemoteUpdater(common.UpdateCapability{}, nil)
	s := &TRPClient{}
	capability, _ := s.updateIdentity()
	if capability.Enabled || capability.Kind != "embedded" {
		t.Fatalf("unregistered capability=%+v", capability)
	}
	ConfigureRemoteUpdater(common.UpdateCapability{Protocol: 2, Kind: "cli", Enabled: true}, nil)
	s.cnf = &config.Config{CommonConfig: &config.CommonConfig{DisableRemoteUpdate: true}}
	capability, _ = s.updateIdentity()
	if capability.Enabled {
		t.Fatal("local opt-out ignored")
	}
}

func TestModernUpdateReportsHealthFramedFailureAndReleasesGuard(t *testing.T) {
	preserveUpdateMetadata(t)
	finished := make(chan struct{})
	ConfigureRemoteUpdater(common.UpdateCapability{Protocol: 2, Kind: "cli", Enabled: true}, func(ctx UpdateContext) (bool, error) {
		defer close(finished)
		ctx.Report("downloading", ctx.Version, "keep old connection")
		return false, errors.New("fixture download failed")
	})
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	_ = right.SetReadDeadline(time.Now().Add(3 * time.Second))
	s := &TRPClient{vKey: "not-for-logs"}
	peer := conn.NewConn(right)
	requestRemoteUpdateV2(s, conn.NewConn(left), common.ClientUpdateRequest{JobID: "job-123", Version: "99.0.0"})
	for _, want := range []string{"accepted", "downloading", "failed"} {
		info, _, err := peer.GetHealthInfo()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(info, common.ClientUpdateHealthPrefix) {
			t.Fatal("not an existing health frame")
		}
		data, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(info, common.ClientUpdateHealthPrefix))
		if err != nil {
			t.Fatal(err)
		}
		var result common.ClientUpdateProgress
		_ = json.Unmarshal(data, &result)
		if result.Stage != want || result.JobID != "job-123" {
			t.Fatalf("progress=%+v want=%s", result, want)
		}
	}
	<-finished
	for deadline := time.Now().Add(time.Second); remoteUpdateInProgress.Load() && time.Now().Before(deadline); {
		time.Sleep(time.Millisecond)
	}
	if remoteUpdateInProgress.Load() {
		t.Fatal("failed update did not release process guard")
	}
}

func TestUpdateErrorDoesNotExposeVKeyOrProxyPassword(t *testing.T) {
	text := safeUpdateError("failed secretkey via http://user:secretpass@proxy.test:8080 password secretpass", "secretkey", "http://user:secretpass@proxy.test:8080")
	if strings.Contains(text, "secretkey") || strings.Contains(text, "secretpass") {
		t.Fatal("credential leaked", text)
	}
}

func TestLegacyUpdateUsesRegisteredUpdaterContextAndReleasesGuard(t *testing.T) {
	for _, panics := range []bool{false, true} {
		t.Run(map[bool]string{false: "failure", true: "panic"}[panics], func(t *testing.T) {
			preserveUpdateMetadata(t)
			contextOK := make(chan bool, 1)
			ConfigureRemoteUpdater(common.UpdateCapability{Protocol: 2, Kind: "cli", Enabled: true}, func(ctx UpdateContext) (bool, error) {
				transport, ok := ctx.HTTPClient.Transport.(*http.Transport)
				contextOK <- ok && transport.Proxy == nil && transport.DialContext != nil && ctx.VKey == "fixture-key" && ctx.Report != nil
				if panics {
					panic("fixture updater panic")
				}
				return false, errors.New("fixture download failed")
			})
			requestRemoteUpdate(&TRPClient{vKey: "fixture-key", proxyUrl: "socks5://127.0.0.1:1080", localIP: "127.0.0.1"})
			select {
			case ok := <-contextOK:
				if !ok {
					t.Fatal("legacy request lost the actual connection's update context")
				}
			case <-time.After(time.Second):
				t.Fatal("legacy request was not handled")
			}
			for deadline := time.Now().Add(time.Second); remoteUpdateInProgress.Load() && time.Now().Before(deadline); {
				time.Sleep(time.Millisecond)
			}
			if remoteUpdateInProgress.Load() {
				t.Fatal("legacy update failure did not release guard")
			}
		})
	}
}

func TestLegacyUpdateRespectsDisabledCapability(t *testing.T) {
	preserveUpdateMetadata(t)
	called := make(chan struct{}, 1)
	ConfigureRemoteUpdater(common.UpdateCapability{Protocol: 2, Kind: "container", Reason: "use image update"}, func(UpdateContext) (bool, error) {
		called <- struct{}{}
		return false, nil
	})
	requestRemoteUpdate(&TRPClient{})
	for deadline := time.Now().Add(time.Second); remoteUpdateInProgress.Load() && time.Now().Before(deadline); {
		time.Sleep(time.Millisecond)
	}
	if remoteUpdateInProgress.Load() {
		t.Fatal("ignored legacy request retained guard")
	}
	select {
	case <-called:
		t.Fatal("disabled updater was called by a legacy request")
	default:
	}
}
