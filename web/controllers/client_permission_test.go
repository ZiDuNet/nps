package controllers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"ehang.io/nps/lib/file"
	beego "github.com/astaxie/beego"
	beecontext "github.com/astaxie/beego/context"
)

type clientPermissionSession struct {
	values map[interface{}]interface{}
}

func (s *clientPermissionSession) Set(key, value interface{}) error {
	s.values[key] = value
	return nil
}

func (s *clientPermissionSession) Get(key interface{}) interface{} {
	return s.values[key]
}

func (s *clientPermissionSession) Delete(key interface{}) error {
	delete(s.values, key)
	return nil
}

func (s *clientPermissionSession) SessionID() string {
	return "client-permission-test"
}

func (s *clientPermissionSession) SessionRelease(http.ResponseWriter) {}

func (s *clientPermissionSession) Flush() error {
	clear(s.values)
	return nil
}

func useClientPermissionTestDb(t *testing.T) *file.DbUtils {
	t.Helper()
	previous := file.GetDb()
	utils := &file.DbUtils{JsonDb: file.NewJsonDb(t.TempDir())}
	file.Db = utils
	t.Cleanup(func() {
		file.Db = previous
	})
	return utils
}

func newClientPermissionController(t *testing.T, action string, form url.Values, sessionValues map[interface{}]interface{}) (*ClientController, *httptest.ResponseRecorder) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "http://console.test/client/"+strings.ToLower(action), strings.NewReader(form.Encode()))
	request.Host = "console.test"
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", "http://console.test")
	recorder := httptest.NewRecorder()
	ctx := beecontext.NewContext()
	ctx.Reset(recorder, request)
	controller := &ClientController{}
	controller.Init(ctx, "ClientController", action, controller)
	session := &clientPermissionSession{values: sessionValues}
	controller.CruSession = session
	ctx.Input.CruSession = session
	return controller, recorder
}

func runClientPermissionAction(t *testing.T, action func()) {
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

func readClientPermissionResponse(t *testing.T, recorder *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var response map[string]interface{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode controller response %q: %v", recorder.Body.String(), err)
	}
	return response
}

func testUserSession(userID int) map[interface{}]interface{} {
	return map[interface{}]interface{}{
		"isAdmin":           false,
		sessionPrincipalKey: sessionPrincipalUser,
		"userId":            userID,
		"auth":              true,
	}
}

func TestDashboardUserClientAddIgnoresAdministrativeFields(t *testing.T) {
	utils := useClientPermissionTestDb(t)
	utils.JsonDb.Users.Store(7, &file.User{Id: 7, UserName: "alice", Status: true})

	form := url.Values{
		"remark":            {"owned client"},
		"u":                 {"basic-user"},
		"p":                 {"basic-password"},
		"compress":          {"1"},
		"crypt":             {"true"},
		"ipwhite":           {"true"},
		"ipwhitepass":       {"grant-token"},
		"ipwhitelist":       {"10.0.0.1\r\n10.0.0.2"},
		"blackiplist":       {"192.0.2.1\r\n192.0.2.2"},
		"user_id":           {"99"},
		"vkey":              {"attacker-vkey"},
		"flow_limit":        {"999"},
		"rate_limit":        {"888"},
		"max_conn":          {"777"},
		"max_tunnel":        {"666"},
		"expire_time":       {"2040-01-01 00:00:00"},
		"web_username":      {"legacy-attacker"},
		"web_password":      {"legacy-secret"},
		"config_conn_allow": {"false"},
	}
	controller, recorder := newClientPermissionController(t, "Add", form, testUserSession(7))
	runClientPermissionAction(t, controller.Add)

	response := readClientPermissionResponse(t, recorder)
	if response["status"] != float64(1) {
		t.Fatalf("ordinary user add response = %#v", response)
	}

	var created *file.Client
	utils.JsonDb.Clients.Range(func(_, value interface{}) bool {
		created, _ = value.(*file.Client)
		return false
	})
	if created == nil {
		t.Fatal("ordinary user add did not persist a client")
	}
	t.Cleanup(func() {
		created.RLock()
		rate := created.Rate
		created.RUnlock()
		if rate != nil {
			rate.Stop()
		}
	})

	created.RLock()
	defer created.RUnlock()
	if created.UserId != 7 {
		t.Fatalf("client owner = %d, want current user 7", created.UserId)
	}
	if created.VerifyKey == "" || created.VerifyKey == "attacker-vkey" {
		t.Fatalf("verification key was not generated safely: %q", created.VerifyKey)
	}
	if created.RateLimit != 0 || created.MaxConn != 0 || created.MaxTunnelNum != 0 || created.Flow == nil || created.Flow.FlowLimit != 0 {
		t.Fatalf("ordinary add applied administrative limits: %#v", created)
	}
	if created.ExpireTime != "" || created.WebUserName != "" || created.WebPassword != "" {
		t.Fatalf("ordinary add applied protected expiry or legacy login: %#v", created)
	}
	if !created.ConfigConnAllow {
		t.Fatal("ordinary add did not retain the server default config connection policy")
	}
	if created.Cnf == nil || created.Cnf.U != "basic-user" || created.Cnf.P != "basic-password" || !created.Cnf.Compress || !created.Cnf.Crypt {
		t.Fatalf("ordinary add did not retain permitted basic-auth settings: %#v", created.Cnf)
	}
	if !created.IpWhite || created.IpWhitePass != "grant-token" || strings.Join(created.IpWhiteList, ",") != "10.0.0.1,10.0.0.2" || strings.Join(created.BlackIpList, ",") != "192.0.2.1,192.0.2.2" {
		t.Fatalf("ordinary add did not retain permitted IP controls: %#v", created)
	}
}

func TestLegacyClientPrincipalCannotAddClient(t *testing.T) {
	utils := useClientPermissionTestDb(t)
	form := url.Values{"remark": {"should not exist"}}
	sessionValues := map[interface{}]interface{}{
		"isAdmin":           false,
		sessionPrincipalKey: sessionPrincipalClient,
		"clientId":          5,
		"auth":              true,
	}
	controller, recorder := newClientPermissionController(t, "Add", form, sessionValues)
	runClientPermissionAction(t, controller.Add)

	response := readClientPermissionResponse(t, recorder)
	if response["status"] != float64(0) {
		t.Fatalf("legacy client principal add response = %#v", response)
	}
	created := false
	utils.JsonDb.Clients.Range(func(_, _ interface{}) bool {
		created = true
		return false
	})
	if created {
		t.Fatal("legacy client principal created a new client")
	}
}

func TestDashboardUserClientEditPreservesAdministrativeFields(t *testing.T) {
	utils := useClientPermissionTestDb(t)
	utils.JsonDb.Users.Store(7, &file.User{Id: 7, UserName: "alice", Status: true})
	client := &file.Client{
		Id:              1,
		UserId:          7,
		VerifyKey:       "server-vkey",
		Status:          true,
		Remark:          "before",
		Cnf:             &file.Config{U: "old-user", P: "old-password"},
		ConfigConnAllow: false,
		RateLimit:       120,
		MaxConn:         12,
		MaxTunnelNum:    4,
		Flow:            &file.Flow{FlowLimit: 256},
		WebUserName:     "legacy-user",
		WebPassword:     "legacy-password",
		ExpireTime:      "2030-01-01 00:00:00",
	}
	utils.JsonDb.Clients.Store(client.Id, client)

	form := url.Values{
		"id":                     {"1"},
		"remark":                 {"after"},
		"u":                      {"new-user"},
		"p":                      {"new-password"},
		"compress":               {"1"},
		"crypt":                  {"true"},
		"ipwhite":                {"true"},
		"ipwhitepass":            {"new-token"},
		"ipwhitelist":            {"10.0.0.5"},
		"blackiplist":            {"192.0.2.5"},
		"vkey":                   {"attacker-vkey"},
		"user_id":                {"99"},
		"flow_limit":             {"999"},
		"rate_limit":             {"888"},
		"max_conn":               {"777"},
		"max_tunnel":             {"666"},
		"expire_time":            {"2040-01-01 00:00:00"},
		"web_username":           {"legacy-attacker"},
		"web_password":           {"legacy-secret"},
		"clear_legacy_web_login": {"1"},
		"config_conn_allow":      {"true"},
	}
	controller, recorder := newClientPermissionController(t, "Edit", form, testUserSession(7))
	runClientPermissionAction(t, controller.Edit)

	response := readClientPermissionResponse(t, recorder)
	if response["status"] != float64(1) {
		t.Fatalf("ordinary user edit response = %#v", response)
	}
	t.Cleanup(func() {
		client.RLock()
		rate := client.Rate
		client.RUnlock()
		if rate != nil {
			rate.Stop()
		}
	})

	client.RLock()
	defer client.RUnlock()
	if client.UserId != 7 || client.VerifyKey != "server-vkey" || client.RateLimit != 120 || client.MaxConn != 12 || client.MaxTunnelNum != 4 || client.Flow.FlowLimit != 256 {
		t.Fatalf("ordinary edit changed administrative client fields: %#v", client)
	}
	if client.ExpireTime != "2030-01-01 00:00:00" || client.WebUserName != "legacy-user" || client.WebPassword != "legacy-password" || client.ConfigConnAllow {
		t.Fatalf("ordinary edit changed protected expiry, legacy login, or config policy: %#v", client)
	}
	if client.Remark != "after" || client.Cnf == nil || client.Cnf.U != "new-user" || client.Cnf.P != "new-password" || !client.Cnf.Compress || !client.Cnf.Crypt {
		t.Fatalf("ordinary edit did not apply permitted fields: %#v", client)
	}
	if !client.IpWhite || client.IpWhitePass != "new-token" || strings.Join(client.IpWhiteList, ",") != "10.0.0.5" || strings.Join(client.BlackIpList, ",") != "192.0.2.5" {
		t.Fatalf("ordinary edit did not apply permitted IP controls: %#v", client)
	}
}

func TestDashboardUserCannotEditAnotherUsersClient(t *testing.T) {
	utils := useClientPermissionTestDb(t)
	utils.JsonDb.Users.Store(7, &file.User{Id: 7, UserName: "alice", Status: true})
	client := &file.Client{Id: 2, UserId: 8, Status: true, Remark: "other user", Cnf: &file.Config{}, Flow: &file.Flow{}}
	utils.JsonDb.Clients.Store(client.Id, client)

	controller, recorder := newClientPermissionController(t, "Edit", url.Values{"id": {"2"}, "remark": {"forged"}}, testUserSession(7))
	runClientPermissionAction(t, controller.Edit)

	response := readClientPermissionResponse(t, recorder)
	if response["status"] != float64(0) {
		t.Fatalf("cross-owner edit response = %#v", response)
	}
	client.RLock()
	remark := client.Remark
	client.RUnlock()
	if remark != "other user" {
		t.Fatalf("cross-owner edit changed remark to %q", remark)
	}
}
