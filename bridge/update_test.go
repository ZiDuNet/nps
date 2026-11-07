package bridge

import (
	"encoding/base64"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/conn"
	"ehang.io/nps/lib/nps_mux"
	"github.com/astaxie/beego"
)

func updateBridgeFixture(t *testing.T, modern bool) (*Bridge, *Client, *conn.Conn) {
	t.Helper()
	old := beego.AppConfig.String("allow_remote_update")
	_ = beego.AppConfig.Set("allow_remote_update", "true")
	t.Cleanup(func() { _ = beego.AppConfig.Set("allow_remote_update", old) })
	left, right := net.Pipe()
	t.Cleanup(func() { left.Close(); right.Close() })
	session := NewClient(nil, nil, conn.NewConn(left), "1.1.42")
	if modern {
		session.Instance = common.ClientIdentity{InstanceID: "initial", Updater: &common.UpdateCapability{Protocol: 2, Kind: "cli", Enabled: true}}
	}
	b := &Bridge{}
	b.Client.Store(17, session)
	return b, session, conn.NewConn(right)
}

func TestUpdateNegotiatesModernAndLegacyFrames(t *testing.T) {
	for _, modern := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "modern"}[modern], func(t *testing.T) {
			b, _, peer := updateBridgeFixture(t, modern)
			frames := make(chan common.ClientUpdateRequest, 1)
			go func() {
				flag, err := peer.ReadFlag()
				if err != nil {
					frames <- common.ClientUpdateRequest{}
					return
				}
				if !modern {
					if flag == common.CLIENT_UPDATE {
						frames <- common.ClientUpdateRequest{JobID: "legacy"}
					} else {
						frames <- common.ClientUpdateRequest{}
					}
					return
				}
				if flag != common.CLIENT_UPDATE_REQUEST {
					frames <- common.ClientUpdateRequest{}
					return
				}
				raw, _ := peer.GetShortLenContent()
				var request common.ClientUpdateRequest
				_ = json.Unmarshal(raw, &request)
				frames <- request
			}()
			job, err := b.StartClientUpdate(17, "99.0.0")
			if err != nil {
				t.Fatal(err)
			}
			request := <-frames
			if modern && (request.JobID != job.JobID || request.Version != "99.0.0") || !modern && request.JobID != "legacy" {
				t.Fatalf("frame=%+v job=%+v", request, job)
			}
			if _, err = b.StartClientUpdate(17, "99.0.0"); err == nil {
				t.Fatal("duplicate update accepted")
			}
		})
	}
}

func TestUpdateRejectsUnsupportedAndAmbiguousInstances(t *testing.T) {
	b, session, _ := updateBridgeFixture(t, true)
	session.Instance.Updater.Enabled = false
	session.Instance.Updater.Reason = "container"
	if _, err := b.StartClientUpdate(17, "99.0.0"); err == nil {
		t.Fatal("disabled updater accepted")
	}
	session.Instance.Updater.Enabled = true
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	second := NewClient(nil, nil, conn.NewConn(left), "1.1.42")
	second.parent = session
	session.sessions = []*Client{session, second}
	if _, err := b.StartClientUpdate(17, "99.0.0"); err == nil || !strings.Contains(err.Error(), "多个在线实例") {
		t.Fatalf("ambiguous update error=%v", err)
	}
}

func TestUpdateRequiresMatchingReceiptAndDataChannel(t *testing.T) {
	b, session, _ := updateBridgeFixture(t, true)
	b.updateJobs = map[int]*ClientUpdateJob{17: {JobID: "expected", ClientID: 17, Target: "99.0.0", Stage: "restarting", CreatedAt: time.Now()}}
	session.Version = "99.0.0"
	session.Instance.UpdateJobID = "wrong"
	b.confirmClientUpdate(17)
	if b.ClientUpdateStatus(17).Finished {
		t.Fatal("wrong receipt confirmed")
	}
	session.Instance.UpdateJobID = "expected"
	b.confirmClientUpdate(17)
	if b.ClientUpdateStatus(17).Finished {
		t.Fatal("MAIN without data confirmed")
	}
	left, right := net.Pipe()
	defer right.Close()
	session.tunnel = nps_mux.NewMux(left, "tcp", 60)
	defer session.tunnel.Close()
	session.DataVersion = "1.1.42"
	b.confirmClientUpdate(17)
	if b.ClientUpdateStatus(17).Finished {
		t.Fatal("old data channel confirmed")
	}
	session.DataVersion = "99.0.0"
	b.confirmClientUpdate(17)
	if job := b.ClientUpdateStatus(17); !job.Finished || job.Stage != "completed" {
		t.Fatalf("job=%+v", job)
	}
}

func TestUpdateProgressBindsToJobAndSignal(t *testing.T) {
	b, session, _ := updateBridgeFixture(t, true)
	b.updateJobs = map[int]*ClientUpdateJob{17: {JobID: "expected", ClientID: 17, Stage: "sent", CreatedAt: time.Now()}}
	encode := func(job, stage string) string {
		data, _ := json.Marshal(common.ClientUpdateProgress{JobID: job, Stage: stage, Message: "test"})
		return common.ClientUpdateHealthPrefix + base64.RawURLEncoding.EncodeToString(data)
	}
	if b.receiveUpdateProgress(17, session.signal, "normal target") {
		t.Fatal("normal health intercepted")
	}
	b.receiveUpdateProgress(17, session.signal, encode("wrong", "failed"))
	if b.ClientUpdateStatus(17).Stage != "sent" {
		t.Fatal("wrong job mutated task")
	}
	b.receiveUpdateProgress(17, session.signal, encode("expected", "completed"))
	if b.ClientUpdateStatus(17).Stage != "sent" {
		t.Fatal("client self-confirmed completion")
	}
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	b.receiveUpdateProgress(17, conn.NewConn(left), encode("expected", "failed"))
	if b.ClientUpdateStatus(17).Finished {
		t.Fatal("different signal mutated task")
	}
	b.receiveUpdateProgress(17, session.signal, encode("expected", "downloading"))
	if b.ClientUpdateStatus(17).Stage != "downloading" {
		t.Fatal("progress not accepted")
	}
	b.updateJobs[17].CreatedAt = time.Now().Add(-16 * time.Minute)
	if job := b.ClientUpdateStatus(17); !job.Finished || job.Stage != "unconfirmed" {
		t.Fatalf("timeout was not unconfirmed: %+v", job)
	}
}
