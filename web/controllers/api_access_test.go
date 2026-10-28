package controllers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ehang.io/nps/lib/file"
	beecontext "github.com/astaxie/beego/context"
)

func newUserPageController(t *testing.T, action string, sessionValues map[interface{}]interface{}) *UserController {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "http://console.test/user/"+strings.ToLower(action), nil)
	request.Host = "console.test"
	recorder := httptest.NewRecorder()
	ctx := beecontext.NewContext()
	ctx.Reset(recorder, request)
	controller := &UserController{}
	controller.Init(ctx, "UserController", action, controller)
	session := &clientPermissionSession{values: sessionValues}
	controller.CruSession = session
	ctx.Input.CruSession = session
	return controller
}

func TestNonAdminCanOpenAPIAccessPage(t *testing.T) {
	utils := useClientPermissionTestDb(t)
	utils.JsonDb.Users.Store(7, &file.User{Id: 7, UserName: "alice", Status: true})

	controller := newUserPageController(t, "Api", testUserSession(7))
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("non-admin API access page was blocked: %#v", recovered)
		}
	}()
	controller.Prepare()
}

func TestNonAdminUserActionAllowList(t *testing.T) {
	for _, action := range []string{"api", "changepassword", "regenerateapikey", "revokeapikey", "apikeystatus"} {
		if !nonAdminUserActionAllowed(action) {
			t.Errorf("ordinary users must be allowed to use %q", action)
		}
	}
	for _, action := range []string{"", "list", "add", "edit", "adminapikeystatus", "adminapikeygenerate", "adminapikeyrevoke", "configapikeyview"} {
		if nonAdminUserActionAllowed(action) {
			t.Errorf("ordinary users must not be allowed to use %q", action)
		}
	}
}
