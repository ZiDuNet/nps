package controllers

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"ehang.io/nps/lib/file"
	"github.com/astaxie/beego"
)

func TestAPIV1PlatformDomainsOmitsCertificatePaths(t *testing.T) {
	db := useClientPermissionTestDb(t)
	previousKey := beego.AppConfig.String("auth_key")
	previousHMAC := beego.AppConfig.DefaultBool("api_hmac_enabled", true)
	previousLegacy := beego.AppConfig.DefaultBool("api_legacy_md5_enabled", false)
	_ = beego.AppConfig.Set("auth_key", apiV1TestAuthKey)
	_ = beego.AppConfig.Set("api_hmac_enabled", "true")
	_ = beego.AppConfig.Set("api_legacy_md5_enabled", "false")
	t.Cleanup(func() {
		_ = beego.AppConfig.Set("auth_key", previousKey)
		_ = beego.AppConfig.Set("api_hmac_enabled", strconv.FormatBool(previousHMAC))
		_ = beego.AppConfig.Set("api_legacy_md5_enabled", strconv.FormatBool(previousLegacy))
	})
	db.JsonDb.Global = &file.Glob{PlatformDomains: []file.PlatformDomain{
		{ID: "pd-http", Wildcard: "*.example.com"},
	}}

	controller, recorder := newAPIV1TestController(
		t,
		http.MethodGet,
		"/api/v1/platform-domains",
		nil,
		"",
	)
	request := controller.Ctx.Request
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce, err := newNPSHMACNonce()
	if err != nil {
		t.Fatal(err)
	}
	bodySum := sha256.Sum256(nil)
	keyID := npsHMACAuthKeyID
	signature, err := signNPSHMAC(apiV1TestAuthKey, request, hex.EncodeToString(bodySum[:]), timestamp, nonce, keyID)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", npsHMACAuthorizationScheme+" "+signature)
	request.Header.Set("X-NPS-Timestamp", timestamp)
	request.Header.Set("X-NPS-Nonce", nonce)
	request.Header.Set("X-NPS-Key-Id", keyID)
	controller.Prepare()
	runClientPermissionAction(t, controller.PlatformDomains)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	payload := readAPIV1JSON(t, recorder)
	rows, ok := payload["data"].([]interface{})
	if !ok || len(rows) != 1 {
		t.Fatalf("data = %#v, want one platform domain", payload["data"])
	}
	domain, ok := rows[0].(map[string]interface{})
	if !ok {
		t.Fatalf("row = %#v", rows[0])
	}
	if domain["id"] != "pd-http" || domain["wildcard"] != "*.example.com" {
		t.Fatalf("unexpected row: %#v", domain)
	}
	if domain["certificate_configured"] != false {
		t.Fatalf("certificate_configured = %#v, want false", domain["certificate_configured"])
	}
	body := strings.ToLower(recorder.Body.String())
	for _, forbidden := range []string{"certfile", "keyfile", "privkey", "begin certificate"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, recorder.Body.String())
		}
	}
}
