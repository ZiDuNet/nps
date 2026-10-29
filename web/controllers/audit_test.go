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
		{ClientID: 101, UserID: 7, ClientRemark: "visible", Action: connectionlog.ActionBridgeConnected},
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
		{Time: now, OwnerUserID: 7, Action: "client.create"},
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
