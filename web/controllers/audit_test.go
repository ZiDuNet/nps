package controllers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
