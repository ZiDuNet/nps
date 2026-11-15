package controllers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ehang.io/nps/lib/connectionlog"
	"ehang.io/nps/lib/file"
	"ehang.io/nps/web/audit"
	beecontext "github.com/astaxie/beego/context"
)

func TestAuditConnectionsScopesOrdinaryUsersToOwnedClients(t *testing.T) {
	db := useClientPermissionTestDb(t)
	db.JsonDb.Users.Store(7, &file.User{Id: 7, UserName: "alice", Status: true})
	db.JsonDb.Clients.Store(101, &file.Client{Id: 101, UserId: 7, Remark: "visible"})
	db.JsonDb.Clients.Store(102, &file.Client{Id: 102, UserId: 9, Remark: "hidden"})

	dir := t.TempDir()
	connectionlog.Configure(filepath.Join(dir, "connection.log"))
	t.Cleanup(func() { connectionlog.Configure("") })
	for _, event := range []connectionlog.Event{
		{
			ClientID: 101, UserID: 7, UserName: "alice", ClientRemark: "visible",
			Action: connectionlog.ActionBridgeConnected, BridgeTransport: "wss",
			BridgeChannel: "main", HandshakeStage: "connected",
			SourceIP: "198.51.100.9", RemoteAddr: "198.51.100.9:50123",
			ServerAddr: "192.0.2.1:8024", ClientPublicIP: "198.51.100.9",
			ClientLocalAddr:       "10.0.0.8",
			ClientLocalAddrSource: connectionlog.ClientLocalAddrSourceCurrentReport,
			ClientVersion:         "1.1.37", ClientStatus: "enabled",
			ClientExpireTime:          "2030-01-01 00:00:00",
			ClientEffectiveExpireTime: "2030-01-01 00:00:00",
			UserStatus:                "enabled", UserExpireTime: "2030-01-01 00:00:00",
		},
		{ClientID: 102, UserID: 9, ClientRemark: "hidden", Action: connectionlog.ActionBridgeConnected},
	} {
		if err := connectionlog.Append(event); err != nil {
			t.Fatal(err)
		}
	}
	connectionlog.Flush()

	controller, recorder := newAuditTestController(t, "Connections", testUserSession(7))
	runClientPermissionAction(t, controller.Connections)
	rows := auditRows(t, recorder)
	if len(rows) != 1 || rows[0]["Client"] != "visible #101" {
		t.Fatalf("ordinary connection rows = %#v", rows)
	}
	if rows[0]["RemoteAddr"] != "198.51.100.9:50123" ||
		rows[0]["ServerAddr"] != "192.0.2.1:8024" ||
		rows[0]["ClientLocalAddr"] != "10.0.0.8" ||
		rows[0]["ClientLocalAddrSource"] != connectionlog.ClientLocalAddrSourceCurrentReport ||
		rows[0]["BridgeChannel"] != "main" ||
		rows[0]["HandshakeStage"] != "connected" {
		t.Fatalf("ordinary connection detail row = %#v", rows[0])
	}

	admin, adminRecorder := newAuditTestController(t, "Connections", map[interface{}]interface{}{"auth": true, "isAdmin": true})
	runClientPermissionAction(t, admin.Connections)
	if rows := auditRows(t, adminRecorder); len(rows) != 2 {
		t.Fatalf("admin connection rows = %d, want 2: %#v", len(rows), rows)
	}
}

func TestAuditListScopesOrdinaryUsersToOwnerAndKeepsAdminGlobal(t *testing.T) {
	useClientPermissionTestDb(t)
	dir := t.TempDir()
	audit.Configure(filepath.Join(dir, "audit.log"))
	t.Cleanup(func() { audit.Configure("") })
	now := time.Now()
	for _, event := range []audit.Event{
		{
			Time: now, ID: "owned", ActorType: "user", ActorID: 7, ActorName: "alice",
			OwnerUserID: 7, SourceIP: "198.51.100.20", Action: "client.create",
			ResourceType: "client", ResourceID: 101, Result: audit.ResultSuccess,
			Details: map[string]string{"remark": "visible"},
			Method:  "POST", Path: "/client/add", RequestID: "owned-request", UserAgent: "Browser/1.0",
		},
		{Time: now, OwnerUserID: 9, Action: "client.delete"},
		{Time: now, OwnerUserID: 0, Action: "auth.login"},
	} {
		if err := audit.Append(event); err != nil {
			t.Fatal(err)
		}
	}

	controller, recorder := newAuditTestController(t, "List", testUserSession(7))
	runClientPermissionAction(t, controller.List)
	rows := auditRows(t, recorder)
	if len(rows) != 1 || rows[0]["Action"] != "client.create" {
		t.Fatalf("ordinary audit rows = %#v", rows)
	}
	if rows[0]["ActorType"] != "user" || rows[0]["ActorID"] != float64(7) ||
		rows[0]["OwnerUserID"] != float64(7) || rows[0]["ResourceType"] != "client" ||
		rows[0]["ResourceID"] != float64(101) || rows[0]["SourceIP"] != "198.51.100.20" ||
		rows[0]["Details"] != `{"remark":"visible"}` {
		t.Fatalf("ordinary audit raw fields = %#v", rows[0])
	}
	if rows[0]["Method"] != "POST" || rows[0]["Path"] != "/client/add" || rows[0]["RequestID"] != "owned-request" || rows[0]["ActionLabel"] != "创建客户端" {
		t.Fatalf("additive audit metadata: %#v", rows[0])
	}
	var userResponse map[string]interface{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &userResponse); err != nil {
		t.Fatal(err)
	}
	if _, exists := userResponse["log_path"]; exists {
		t.Fatal("ordinary user received server filesystem path")
	}
	if userResponse["log_enabled"] != true {
		t.Fatal("missing log configuration status")
	}

	admin, adminRecorder := newAuditTestController(t, "List", map[interface{}]interface{}{"auth": true, "isAdmin": true})
	runClientPermissionAction(t, admin.List)
	if rows := auditRows(t, adminRecorder); len(rows) != 3 {
		t.Fatalf("admin audit rows = %d, want 3", len(rows))
	}
}

func newAuditTestController(t *testing.T, action string, sessionValues map[interface{}]interface{}) (*AuditController, *httptest.ResponseRecorder) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "http://console.test/audit/"+strings.ToLower(action), strings.NewReader("offset=0&limit=10"))
	request.Host = "console.test"
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	ctx := beecontext.NewContext()
	ctx.Reset(recorder, request)
	controller := &AuditController{}
	controller.Init(ctx, "AuditController", action, controller)
	session := &clientPermissionSession{values: sessionValues}
	controller.CruSession = session
	ctx.Input.CruSession = session
	return controller, recorder
}

func auditRows(t *testing.T, recorder *httptest.ResponseRecorder) []map[string]interface{} {
	t.Helper()
	var response struct {
		Rows []map[string]interface{} `json:"rows"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode audit response %q: %v", recorder.Body.String(), err)
	}
	return response.Rows
}

func TestAuditCleanupRejectsUsersGetAndUnconfirmedRequests(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	audit.Configure(path)
	t.Cleanup(func() { audit.Configure("") })
	if err := audit.Append(audit.Event{Time: time.Now().AddDate(0, 0, -90), Action: "client.create"}); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	admin := map[interface{}]interface{}{"auth": true, "isAdmin": true}
	for _, tc := range []struct {
		name, method, days, kind, confirm string
		session                           map[interface{}]interface{}
		intent                            bool
		status                            int
	}{
		{"ordinary user", "POST", "30", "operations", "1", testUserSession(7), true, 403},
		{"VKey client", "POST", "30", "operations", "1", map[interface{}]interface{}{"auth": true, "isAdmin": false, sessionPrincipalKey: sessionPrincipalClient, "clientId": 42}, true, 403},
		{"anonymous", "POST", "30", "operations", "1", nil, true, 403},
		{"get", "GET", "30", "operations", "1", admin, true, 405},
		{"missing header", "POST", "30", "operations", "1", admin, false, 400},
		{"missing confirmation", "POST", "30", "operations", "", admin, true, 400},
		{"zero days", "POST", "0", "operations", "1", admin, true, 400},
		{"negative days", "POST", "-1", "operations", "1", admin, true, 400},
		{"fractional days", "POST", "1.5", "operations", "1", admin, true, 400},
		{"excessive days", "POST", "36501", "operations", "1", admin, true, 400},
		{"unknown log", "POST", "30", "../../nps.log", "1", admin, true, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			form := url.Values{"days": {tc.days}, "kind": {tc.kind}, "confirm": {tc.confirm}}
			controller, recorder := cleanupTestController(t, tc.method, tc.session, form, tc.intent)
			runClientPermissionAction(t, controller.Cleanup)
			if recorder.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, tc.status, recorder.Body.String())
			}
			if data, err := os.ReadFile(path); err != nil || string(data) != string(original) {
				t.Fatal("rejected request changed the journal", err)
			}
		})
	}
}

func TestAuditCleanupOnlyDeletesSelectedJournalAndAuditsResult(t *testing.T) {
	for _, kind := range []string{"operations", "connections"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			audit.Configure(filepath.Join(dir, "audit.log"))
			connectionlog.Configure(filepath.Join(dir, "connection.log"))
			t.Cleanup(func() { audit.Configure(""); connectionlog.Configure("") })
			for _, at := range []time.Time{time.Now().AddDate(0, 0, -90), time.Now()} {
				if err := audit.Append(audit.Event{Time: at, Action: "client.create"}); err != nil {
					t.Fatal(err)
				}
				if err := connectionlog.Append(connectionlog.Event{Time: at, Action: connectionlog.ActionBridgeConnected}); err != nil {
					t.Fatal(err)
				}
			}
			connectionlog.Flush()
			controller, recorder := cleanupTestController(t, http.MethodPost, map[interface{}]interface{}{"auth": true, "isAdmin": true, "username": "admin"}, url.Values{"kind": {kind}, "days": {"30"}, "confirm": {"1"}}, true)
			runClientPermissionAction(t, controller.Cleanup)
			var result struct {
				Status  int
				Removed int
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil || result.Status != 1 || result.Removed != 1 {
				t.Fatalf("cleanup response = %s %v", recorder.Body.String(), err)
			}
			auditPage, err := audit.List(audit.Filter{})
			if err != nil {
				t.Fatal(err)
			}
			connectionPage, err := connectionlog.List(connectionlog.Filter{})
			if err != nil {
				t.Fatal(err)
			}
			wantAudit, wantConnections := 2, 2
			if kind == "connections" {
				wantAudit, wantConnections = 3, 1
			}
			if auditPage.Total != wantAudit || connectionPage.Total != wantConnections {
				t.Fatalf("unexpected journal totals: operations=%d connections=%d", auditPage.Total, connectionPage.Total)
			}
			cleanupPage, err := audit.List(audit.Filter{Action: "audit.cleanup"})
			if err != nil || cleanupPage.Total != 1 || cleanupPage.Events[0].Details["removed"] != "1" || cleanupPage.Events[0].Details["kind"] != kind || cleanupPage.Events[0].ActorType != "admin" {
				t.Fatalf("cleanup audit missing: %+v %v", cleanupPage, err)
			}
		})
	}
}

func TestAuditCleanupRejectsDuplicateWhileAnotherCleanupIsRunning(t *testing.T) {
	auditCleanupMu.Lock()
	defer auditCleanupMu.Unlock()
	controller, recorder := cleanupTestController(t, http.MethodPost, map[interface{}]interface{}{"auth": true, "isAdmin": true}, url.Values{"kind": {"operations"}, "days": {"30"}, "confirm": {"1"}}, true)
	runClientPermissionAction(t, controller.Cleanup)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("concurrent cleanup status=%d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestAuditPanelLimitsLatestWindowWithoutChangingAPISemantics(t *testing.T) {
	useClientPermissionTestDb(t)
	audit.Configure(filepath.Join(t.TempDir(), "audit.log"))
	t.Cleanup(func() { audit.Configure("") })
	var data strings.Builder
	for i := 0; i < 2005; i++ {
		event := audit.Event{ID: fmt.Sprintf("%04d", i), Time: time.Now(), Action: "auth.login", OwnerUserID: 7}
		encoded, _ := json.Marshal(event)
		data.Write(encoded)
		data.WriteByte('\n')
	}
	if err := os.WriteFile(audit.Path(), []byte(data.String()), 0600); err != nil {
		t.Fatal(err)
	}
	for _, full := range []bool{false, true} {
		controller, recorder := newAuditTestController(t, "List", testUserSession(7))
		controller.fullHistory = full
		runClientPermissionAction(t, controller.List)
		var response struct {
			Total       int `json:"total"`
			RecentLimit int `json:"recent_limit"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if full {
			if response.Total != 2005 || response.RecentLimit != 0 {
				t.Fatalf("API history changed: %+v", response)
			}
		} else if response.Total != 2000 || response.RecentLimit != 2000 {
			t.Fatalf("panel not limited to tail window: %+v", response)
		}
	}
}

func cleanupTestController(t *testing.T, method string, session map[interface{}]interface{}, form url.Values, intent bool) (*AuditController, *httptest.ResponseRecorder) {
	t.Helper()
	controller, recorder := newAuditTestController(t, "Cleanup", session)
	controller.Ctx.Request.Method = method
	controller.Ctx.Request.Form = form
	controller.Ctx.Request.PostForm = form
	if intent {
		controller.Ctx.Request.Header.Set("X-NPS-Log-Cleanup", "1")
	}
	return controller, recorder
}
