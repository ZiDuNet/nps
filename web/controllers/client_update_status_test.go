package controllers

import (
	"net/http"
	"net/url"
	"testing"

	"ehang.io/nps/lib/file"
)

func TestUpdateStatusRequiresAdminAndDoesNotFetchRelease(t *testing.T) {
	db := useClientPermissionTestDb(t)
	db.JsonDb.Clients.Store(1, &file.Client{Id: 1, UserId: 7, Status: true})
	controller, recorder := newClientPermissionController(t, "UpdateStatus", url.Values{"id": {"1"}}, testUserSession(7))
	runClientPermissionAction(t, controller.UpdateStatus)
	if result := readClientPermissionResponse(t, recorder); result["status"] == float64(1) {
		t.Fatal("user can inspect admin upgrade tasks")
	}
	controller, recorder = newClientPermissionController(t, "UpdateStatus", url.Values{"id": {"1"}}, map[interface{}]interface{}{"isAdmin": true, "auth": true})
	runClientPermissionAction(t, controller.UpdateStatus)
	if result := readClientPermissionResponse(t, recorder); result["status"] != float64(1) {
		t.Fatalf("admin status=%v", result)
	}
}

func TestAPIV1UpdateStatusRequiresAdmin(t *testing.T) {
	db := useClientPermissionTestDb(t)
	token, hash, err := generateUserAPIToken()
	if err != nil {
		t.Fatal(err)
	}
	db.JsonDb.Users.Store(7, &file.User{Id: 7, UserName: "alice", Status: true, APIKeyHash: hash})
	controller, recorder := newAPIV1TestController(t, http.MethodGet, "/api/v1/clients/1/update-status", nil, "")
	controller.Ctx.Request.Header.Set("Authorization", "Bearer "+token)
	controller.Prepare()
	runClientPermissionAction(t, controller.ClientUpdateStatus)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status=%d response=%s", recorder.Code, recorder.Body.String())
	}
}
