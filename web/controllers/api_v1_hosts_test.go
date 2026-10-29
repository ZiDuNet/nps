package controllers

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/file"
)

func newAPIV1HostJSONController(t *testing.T, method, target string, payload map[string]interface{}) (*ApiV1Controller, *httptest.ResponseRecorder) {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal host payload: %v", err)
	}
	return newAPIV1TestController(t, method, target, bytes.NewReader(body), "application/json")
}

func prepareAPIV1CustomHostTest(t *testing.T) *file.DbUtils {
	t.Helper()
	db := useClientPermissionTestDb(t)
	withAPIV1TestAuthKey(t)
	db.JsonDb.Clients.Store(1, &file.Client{
		Id:        1,
		VerifyKey: "api-host-client",
		Remark:    "api host client",
		Status:    true,
	})
	return db
}

func runAPIV1HostCreate(t *testing.T, payload map[string]interface{}) map[string]interface{} {
	t.Helper()
	controller, recorder := newAPIV1HostJSONController(
		t,
		http.MethodPost,
		"/api/v1/hosts?"+apiV1SignedQuery().Encode(),
		payload,
	)
	controller.Prepare()
	runClientPermissionAction(t, controller.HostsCreate)
	if recorder.Code != http.StatusOK {
		t.Fatalf("create host status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	return readAPIV1JSON(t, recorder)
}

func TestAPIV1CustomHostRejectsCertificatePathMode(t *testing.T) {
	db := prepareAPIV1CustomHostTest(t)

	payload := map[string]interface{}{
		"client_id":      1,
		"remark":         "path mode must fail",
		"domain_mode":    "custom",
		"host":           "app.example.com",
		"scheme":         "https",
		"location":       "/",
		"target":         "127.0.0.1:8080",
		"cert_mode":      "path",
		"cert_file_path": "/etc/nps/custom.pem",
		"key_file_path":  "/etc/nps/custom.key",
	}
	controller, recorder := newAPIV1HostJSONController(
		t,
		http.MethodPost,
		"/api/v1/hosts?"+apiV1SignedQuery().Encode(),
		payload,
	)
	controller.Prepare()
	runClientPermissionAction(t, controller.HostsCreate)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("path mode status = %d, want %d: %s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
	response := readAPIV1JSON(t, recorder)
	if response["status"] != float64(0) || response["error_code"] != "operation_failed" {
		t.Fatalf("path mode response = %#v", response)
	}
	if message, _ := response["msg"].(string); !strings.Contains(message, "不支持服务器路径模式") {
		t.Fatalf("path mode message = %q", message)
	}
	if _, exists := db.JsonDb.Hosts.Load(1); exists {
		t.Fatal("rejected custom host with a certificate path was persisted")
	}
}

func TestAPIV1CustomHostUpdateRejectsCertificatePathMode(t *testing.T) {
	db := prepareAPIV1CustomHostTest(t)
	created := runAPIV1HostCreate(t, map[string]interface{}{
		"client_id":   1,
		"remark":      "api update path test",
		"domain_mode": "custom",
		"host":        "update-path.example.com",
		"scheme":      "http",
		"location":    "/",
		"target":      "127.0.0.1:8080",
	})
	data, ok := created["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("created host data = %#v", created["data"])
	}
	hostID, ok := data["id"].(float64)
	if !ok || hostID <= 0 {
		t.Fatalf("created host id = %#v", data["id"])
	}

	controller, recorder := newAPIV1HostJSONController(
		t,
		http.MethodPatch,
		"/api/v1/hosts/"+strconv.Itoa(int(hostID))+"?"+apiV1SignedQuery().Encode(),
		map[string]interface{}{
			"client_id":      1,
			"remark":         "path mode update must fail",
			"domain_mode":    "custom",
			"host":           "update-path.example.com",
			"scheme":         "https",
			"location":       "/",
			"target":         "127.0.0.1:8080",
			"cert_mode":      "path",
			"cert_file_path": "/etc/nps/update.pem",
			"key_file_path":  "/etc/nps/update.key",
		},
	)
	controller.Prepare()
	controller.Ctx.Input.SetParam(":id", strconv.Itoa(int(hostID)))
	runClientPermissionAction(t, controller.HostUpdate)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("path mode update status = %d, want %d: %s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
	response := readAPIV1JSON(t, recorder)
	if response["status"] != float64(0) || response["error_code"] != "operation_failed" {
		t.Fatalf("path mode update response = %#v", response)
	}
	if message, _ := response["msg"].(string); !strings.Contains(message, "不支持服务器路径模式") {
		t.Fatalf("path mode update message = %q", message)
	}

	storedValue, exists := db.JsonDb.Hosts.Load(int(hostID))
	if !exists {
		t.Fatalf("host %d disappeared after rejected update", int(hostID))
	}
	stored, ok := storedValue.(*file.Host)
	if !ok || stored == nil {
		t.Fatalf("stored host = %#v", storedValue)
	}
	stored.RLock()
	scheme, certFile, keyFile := stored.Scheme, stored.CertFilePath, stored.KeyFilePath
	stored.RUnlock()
	if scheme != "http" || certFile != "" || keyFile != "" {
		t.Fatalf("host changed after rejected update: scheme=%q cert=%q key=%q", scheme, certFile, keyFile)
	}
}

func TestAPIV1CustomHostAcceptsUploadedCertificateAndHidesPaths(t *testing.T) {
	db := prepareAPIV1CustomHostTest(t)
	previousRunPath := common.ConfPath
	common.ConfPath = t.TempDir()
	t.Cleanup(func() {
		common.ConfPath = previousRunPath
	})
	if err := os.MkdirAll(filepath.Join(common.ConfPath, "conf"), 0755); err != nil {
		t.Fatal(err)
	}

	certPath, keyPath := writeControllerTestCertificate(t, []string{"app.example.com"})
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}

	payload := map[string]interface{}{
		"client_id":   1,
		"remark":      "uploaded certificate",
		"domain_mode": "custom",
		"host":        "app.example.com",
		"scheme":      "https",
		"location":    "/",
		"target":      "127.0.0.1:8080",
		"cert_mode":   "upload",
		"cert":        base64.StdEncoding.EncodeToString(certPEM),
		"key":         base64.StdEncoding.EncodeToString(keyPEM),
	}
	response := runAPIV1HostCreate(t, payload)
	data, ok := response["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("created host data = %#v", response["data"])
	}
	hostID, ok := data["id"].(float64)
	if !ok || hostID <= 0 {
		t.Fatalf("created host id = %#v", data["id"])
	}

	storedValue, exists := db.JsonDb.Hosts.Load(int(hostID))
	if !exists {
		t.Fatalf("created host %d was not persisted", int(hostID))
	}
	stored, ok := storedValue.(*file.Host)
	if !ok || stored == nil {
		t.Fatalf("stored host = %#v", storedValue)
	}
	stored.RLock()
	certFile, keyFile := stored.CertFilePath, stored.KeyFilePath
	stored.RUnlock()
	if certFile != "conf/certs/host_1.pem" || keyFile != "conf/certs/host_1.key" {
		t.Fatalf("managed certificate paths = %q, %q", certFile, keyFile)
	}
	for _, path := range []string{certFile, keyFile} {
		if _, err := os.Stat(filepath.Join(common.ConfPath, filepath.FromSlash(path))); err != nil {
			t.Fatalf("managed certificate file %q is unavailable: %v", path, err)
		}
	}

	payload["remark"] = "updated uploaded certificate"
	updateController, updateRecorder := newAPIV1HostJSONController(
		t,
		http.MethodPatch,
		"/api/v1/hosts/"+strconv.Itoa(int(hostID))+"?"+apiV1SignedQuery().Encode(),
		payload,
	)
	updateController.Prepare()
	updateController.Ctx.Input.SetParam(":id", strconv.Itoa(int(hostID)))
	runClientPermissionAction(t, updateController.HostUpdate)
	if updateRecorder.Code != http.StatusOK {
		t.Fatalf("update host status = %d, want %d: %s", updateRecorder.Code, http.StatusOK, updateRecorder.Body.String())
	}
	stored.RLock()
	updatedRemark := stored.Remark
	stored.RUnlock()
	if updatedRemark != "updated uploaded certificate" {
		t.Fatalf("updated host remark = %q", updatedRemark)
	}

	getController, getRecorder := newAPIV1HostJSONController(
		t,
		http.MethodGet,
		"/api/v1/hosts/"+strconv.Itoa(int(hostID))+"?"+apiV1SignedQuery().Encode(),
		nil,
	)
	getController.Prepare()
	getController.Ctx.Input.SetParam(":id", strconv.Itoa(int(hostID)))
	runClientPermissionAction(t, getController.HostGet)
	if getRecorder.Code != http.StatusOK {
		t.Fatalf("get host status = %d, want %d: %s", getRecorder.Code, http.StatusOK, getRecorder.Body.String())
	}
	getResponse := readAPIV1JSON(t, getRecorder)
	hostData, ok := getResponse["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("get host data = %#v", getResponse["data"])
	}
	for _, key := range []string{"CertFilePath", "KeyFilePath", "cert_file_path", "key_file_path"} {
		if _, exists := hostData[key]; exists {
			t.Fatalf("host API leaked server certificate field %q: %#v", key, hostData)
		}
	}
}
