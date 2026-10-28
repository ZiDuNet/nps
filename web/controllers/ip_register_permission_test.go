package controllers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"ehang.io/nps/lib/file"
	"ehang.io/nps/lib/ipreg"
	beego "github.com/astaxie/beego"
	beecontext "github.com/astaxie/beego/context"
)

func newGlobalPermissionController(t *testing.T, action string, form url.Values, sessionValues map[interface{}]interface{}) (*GlobalController, *httptest.ResponseRecorder) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "http://console.test/global/"+action, nil)
	request.Host = "console.test"
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Form = cloneURLValues(form)
	request.PostForm = cloneURLValues(form)
	recorder := httptest.NewRecorder()
	ctx := beecontext.NewContext()
	ctx.Reset(recorder, request)
	controller := &GlobalController{}
	controller.Init(ctx, "GlobalController", action, controller)
	session := &clientPermissionSession{values: sessionValues}
	controller.CruSession = session
	ctx.Input.CruSession = session
	return controller, recorder
}

func cloneURLValues(values url.Values) url.Values {
	result := make(url.Values, len(values))
	for key, items := range values {
		result[key] = append([]string(nil), items...)
	}
	return result
}

func runGlobalPermissionAction(t *testing.T, action func()) {
	t.Helper()
	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("controller action did not stop after writing a response")
		}
		if recovered != beego.ErrAbort {
			t.Fatalf("controller action panicked unexpectedly: %#v", recovered)
		}
	}()
	action()
}

func decodeGlobalPermissionResponse(t *testing.T, recorder *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var payload map[string]interface{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response %q: %v", recorder.Body.String(), err)
	}
	return payload
}

func prepareGlobalPermissionFixture(t *testing.T) {
	t.Helper()
	utils := useClientPermissionTestDb(t)
	utils.JsonDb.Users.Store(7, &file.User{Id: 7, UserName: "alice", Status: true})
	utils.JsonDb.Users.Store(8, &file.User{Id: 8, UserName: "bob", Status: true})
	utils.JsonDb.Clients.Store(1, &file.Client{Id: 1, UserId: 7, Remark: "alice-1", Status: true, Cnf: &file.Config{}, Flow: &file.Flow{}})
	utils.JsonDb.Clients.Store(2, &file.Client{Id: 2, UserId: 8, Remark: "bob-1", Status: true, Cnf: &file.Config{}, Flow: &file.Flow{}})
	ipreg.Reset()
	t.Cleanup(ipreg.Reset)
	for _, item := range []struct {
		clientID int
		ip       string
	}{
		{1, "198.51.100.11"},
		{2, "198.51.100.22"},
		{ipreg.AnyClient, "203.0.113.33"},
	} {
		address, ok := ipreg.ParseIP(item.ip)
		if !ok {
			t.Fatalf("parse test IP %q", item.ip)
		}
		ipreg.Register(item.clientID, address, time.Hour)
	}
}

func TestUserIPRegistrationListOnlyContainsOwnedClients(t *testing.T) {
	prepareGlobalPermissionFixture(t)
	controller, recorder := newGlobalPermissionController(t, "IPRegisterList", url.Values{"offset": {"0"}, "limit": {"50"}}, testUserSession(7))
	runGlobalPermissionAction(t, func() {
		controller.Prepare()
		controller.IPRegisterList()
	})
	payload := decodeGlobalPermissionResponse(t, recorder)
	rows, ok := payload["rows"].([]interface{})
	if !ok || len(rows) != 1 {
		t.Fatalf("user IP rows = %#v", payload["rows"])
	}
	row, _ := rows[0].(map[string]interface{})
	if row["client_id"] != float64(1) || row["client"] != "alice-1" {
		t.Fatalf("user IP row = %#v, want only client 1", row)
	}
}

func TestUserCannotRevokeAnotherUsersIPRegistration(t *testing.T) {
	prepareGlobalPermissionFixture(t)
	controller, recorder := newGlobalPermissionController(t, "IPRegisterRevoke", url.Values{"client_id": {"2"}, "ip": {"198.51.100.22"}}, testUserSession(7))
	runGlobalPermissionAction(t, func() {
		controller.Prepare()
		controller.IPRegisterRevoke()
	})
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("cross-owner revoke status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if !ipreg.RegisteredString(2, "198.51.100.22") {
		t.Fatal("cross-owner revoke changed another user's registration")
	}
}

func TestUserClearOnlyRemovesOwnedClientRegistrations(t *testing.T) {
	prepareGlobalPermissionFixture(t)
	controller, recorder := newGlobalPermissionController(t, "IPRegisterClear", nil, testUserSession(7))
	runGlobalPermissionAction(t, func() {
		controller.Prepare()
		controller.IPRegisterClear()
	})
	payload := decodeGlobalPermissionResponse(t, recorder)
	if payload["status"] != float64(1) {
		t.Fatalf("clear response = %#v", payload)
	}
	if ipreg.RegisteredString(1, "198.51.100.11") {
		t.Fatal("owned registration was not cleared")
	}
	if !ipreg.RegisteredString(2, "198.51.100.22") || !ipreg.RegisteredString(ipreg.AnyClient, "203.0.113.33") {
		t.Fatal("scoped clear removed another user's or the public registration")
	}
}

func TestNonAdminGlobalActionAllowList(t *testing.T) {
	for _, action := range []string{"ipregisterpage", "ipregisterlist", "ipregisterrevoke", "ipregisterclean", "ipregisterclear"} {
		if !nonAdminGlobalActionAllowed(action) {
			t.Errorf("ordinary users must be allowed to use %q", action)
		}
	}
	for _, action := range []string{"", "index", "save", "config", "saveconfig", "geo", "updategeo", "banlist"} {
		if nonAdminGlobalActionAllowed(action) {
			t.Errorf("ordinary users must not be allowed to use %q", action)
		}
	}
}
