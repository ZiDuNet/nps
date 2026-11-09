package controllers

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/file"
	"ehang.io/nps/lib/instancekey"
	"ehang.io/nps/web/audit"
	"github.com/astaxie/beego"
	beecontext "github.com/astaxie/beego/context"
	"github.com/astaxie/beego/session"
)

const (
	rememberLoginCookieName = "nps_remember_login"
	rememberLoginVersion    = 1
	rememberLoginDuration   = 72 * time.Hour
	rememberLoginTokenLimit = 4096
	sessionSecurityEpochKey = "authEpoch"
)

var errRememberLoginInvalid = errors.New("remember login token is invalid")

// rememberLoginClaims contains only opaque identifiers and a credential-bound
// fingerprint. Passwords and VKEYs never enter the browser cookie.
type rememberLoginClaims struct {
	Version     int    `json:"v"`
	Principal   string `json:"p"`
	UserID      int    `json:"u,omitempty"`
	ClientID    int    `json:"c,omitempty"`
	Username    string `json:"n,omitempty"`
	Fingerprint string `json:"f"`
	ExpiresAt   int64  `json:"e"`
	DeviceID    string `json:"j,omitempty"`
	Epoch       uint64 `json:"g,omitempty"`
	IssuedAt    int64  `json:"i,omitempty"`
}

type rememberLoginCredential struct {
	Username string
	Secret   string
}

func rememberLoginKey() ([]byte, error) {
	keyPath := strings.TrimSpace(beego.AppConfig.String("instance_key_file"))
	if keyPath == "" {
		keyPath = filepath.Join(common.GetRunPath(), "conf", "instance.key")
	} else if !filepath.IsAbs(keyPath) {
		keyPath = filepath.Join(common.GetRunPath(), keyPath)
	}
	key, err := instancekey.LoadOrCreate(keyPath)
	if err != nil {
		return nil, err
	}
	return key.DeriveKey("web-remember-login", 32)
}

func currentRememberLoginCredential(controller *beego.Controller) (rememberLoginClaims, rememberLoginCredential, error) {
	if controller == nil || !sessionBool(controller.GetSession("auth")) {
		return rememberLoginClaims{}, rememberLoginCredential{}, errRememberLoginInvalid
	}
	if sessionBool(controller.GetSession("isAdmin")) {
		username := beego.AppConfig.String("web_username")
		secret := beego.AppConfig.String("web_password") + "\x00" + beego.AppConfig.String("totp_secret")
		if username == "" && secret == "\x00" {
			return rememberLoginClaims{}, rememberLoginCredential{}, errRememberLoginInvalid
		}
		return rememberLoginClaims{Version: rememberLoginVersion, Principal: sessionPrincipalUser, Username: username},
			rememberLoginCredential{Username: username, Secret: secret}, nil
	}

	principal, _ := controller.GetSession(sessionPrincipalKey).(string)
	switch principal {
	case sessionPrincipalUser:
		userID, _ := sessionInt(controller.GetSession("userId"))
		user, err := file.GetDb().GetUser(userID)
		if err != nil || user == nil || !file.GetDb().IsUserActive(userID) {
			return rememberLoginClaims{}, rememberLoginCredential{}, errRememberLoginInvalid
		}
		user.RLock()
		username, password := user.UserName, user.Password
		user.RUnlock()
		return rememberLoginClaims{
			Version: rememberLoginVersion, Principal: principal, UserID: userID, Username: username,
		}, rememberLoginCredential{Username: username, Secret: password}, nil
	case sessionPrincipalClient:
		clientID, _ := sessionInt(controller.GetSession("clientId"))
		client, err := file.GetDb().GetClient(clientID)
		if err != nil || client == nil {
			return rememberLoginClaims{}, rememberLoginCredential{}, errRememberLoginInvalid
		}
		client.RLock()
		status, noDisplay, ownerID := client.Status, client.NoDisplay, client.UserId
		webUsername, webPassword, verifyKey := client.WebUserName, client.WebPassword, client.VerifyKey
		client.RUnlock()
		if !status || noDisplay || (ownerID != 0 && !file.GetDb().IsUserActive(ownerID)) {
			return rememberLoginClaims{}, rememberLoginCredential{}, errRememberLoginInvalid
		}
		username := webUsername
		if webUsername == "" && webPassword == "" {
			username = "user"
		}
		return rememberLoginClaims{
				Version: rememberLoginVersion, Principal: principal, ClientID: clientID, Username: username,
			}, rememberLoginCredential{
				Username: username,
				Secret:   webUsername + "\x00" + webPassword + "\x00" + verifyKey,
			}, nil
	default:
		return rememberLoginClaims{}, rememberLoginCredential{}, errRememberLoginInvalid
	}
}

// The synthetic principal marks the administrator branch without exposing
// administrator identity data in the cookie.
func rememberLoginPrincipal(claims *rememberLoginClaims) string {
	if claims == nil {
		return ""
	}
	if claims.Principal == sessionPrincipalUser && claims.UserID == 0 && claims.ClientID == 0 {
		return "admin"
	}
	return claims.Principal
}

func rememberLoginPrincipalKey(claims *rememberLoginClaims) string {
	if claims == nil {
		return ""
	}
	switch rememberLoginPrincipal(claims) {
	case "admin":
		return "admin"
	case sessionPrincipalUser:
		if claims.UserID > 0 {
			return sessionPrincipalUser + ":" + strconv.Itoa(claims.UserID)
		}
	case sessionPrincipalClient:
		if claims.ClientID > 0 {
			return sessionPrincipalClient + ":" + strconv.Itoa(claims.ClientID)
		}
	}
	return ""
}

func currentSessionPrincipalKey(controller *beego.Controller) string {
	if controller == nil || !sessionBool(controller.GetSession("auth")) {
		return ""
	}
	if sessionBool(controller.GetSession("isAdmin")) {
		return "admin"
	}
	principal, _ := controller.GetSession(sessionPrincipalKey).(string)
	switch principal {
	case sessionPrincipalUser:
		userID, _ := sessionInt(controller.GetSession("userId"))
		if userID > 0 {
			return sessionPrincipalUser + ":" + strconv.Itoa(userID)
		}
	case sessionPrincipalClient:
		clientID, _ := sessionInt(controller.GetSession("clientId"))
		if clientID > 0 {
			return sessionPrincipalClient + ":" + strconv.Itoa(clientID)
		}
	}
	return ""
}

func sessionSecurityEpoch(controller *beego.Controller) uint64 {
	if controller == nil {
		return 0
	}
	value, _ := sessionInt(controller.GetSession(sessionSecurityEpochKey))
	if value < 0 {
		return 0
	}
	return uint64(value)
}

func rememberLoginCookieClaims(ctx *beecontext.Context, now time.Time) (*rememberLoginClaims, error) {
	if ctx == nil || ctx.Request == nil {
		return nil, errRememberLoginInvalid
	}
	cookie, err := ctx.Request.Cookie(rememberLoginCookieName)
	if err != nil || cookie == nil || strings.TrimSpace(cookie.Value) == "" {
		return nil, errRememberLoginInvalid
	}
	key, err := rememberLoginKey()
	if err != nil {
		return nil, err
	}
	claims, err := decodeRememberLoginToken(key, cookie.Value, now)
	if err != nil {
		return nil, err
	}
	if err := validateRememberLoginClaims(key, claims); err != nil {
		return nil, err
	}
	return claims, nil
}

func rememberLoginFingerprint(key []byte, claims rememberLoginClaims, secret string) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("nps-remember-login-credential-v1\x00"))
	_, _ = mac.Write([]byte(rememberLoginPrincipal(&claims)))
	_, _ = mac.Write([]byte("\x00" + strconv.Itoa(claims.UserID)))
	_, _ = mac.Write([]byte("\x00" + strconv.Itoa(claims.ClientID)))
	_, _ = mac.Write([]byte("\x00" + claims.Username + "\x00" + secret))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func encodeRememberLoginToken(key []byte, claims rememberLoginClaims, now time.Time) (string, error) {
	if len(key) == 0 {
		return "", errors.New("remember login key is empty")
	}
	claims.Version = rememberLoginVersion
	claims.ExpiresAt = now.Add(rememberLoginDuration).Unix()
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(encodedPayload))
	encodedSignature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return encodedPayload + "." + encodedSignature, nil
}

func decodeRememberLoginToken(key []byte, token string, now time.Time) (*rememberLoginClaims, error) {
	token = strings.TrimSpace(token)
	if token == "" || len(token) > rememberLoginTokenLimit {
		return nil, errRememberLoginInvalid
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, errRememberLoginInvalid
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errRememberLoginInvalid
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(parts[0]))
	if subtle.ConstantTimeCompare(signature, mac.Sum(nil)) != 1 {
		return nil, errRememberLoginInvalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, errRememberLoginInvalid
	}
	var claims rememberLoginClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, errRememberLoginInvalid
	}
	if claims.Version != rememberLoginVersion || claims.Fingerprint == "" ||
		claims.ExpiresAt <= now.Unix() || claims.Principal == "" {
		return nil, errRememberLoginInvalid
	}
	return &claims, nil
}

func rememberedLoginCredential(claims *rememberLoginClaims) (rememberLoginCredential, error) {
	if claims == nil {
		return rememberLoginCredential{}, errRememberLoginInvalid
	}
	if rememberLoginPrincipal(claims) == "admin" {
		username := beego.AppConfig.String("web_username")
		if username != claims.Username {
			return rememberLoginCredential{}, errRememberLoginInvalid
		}
		return rememberLoginCredential{
			Username: username,
			Secret:   beego.AppConfig.String("web_password") + "\x00" + beego.AppConfig.String("totp_secret"),
		}, nil
	}
	switch claims.Principal {
	case sessionPrincipalUser:
		if claims.UserID <= 0 || !file.GetDb().IsUserActive(claims.UserID) {
			return rememberLoginCredential{}, errRememberLoginInvalid
		}
		user, err := file.GetDb().GetUser(claims.UserID)
		if err != nil || user == nil {
			return rememberLoginCredential{}, errRememberLoginInvalid
		}
		user.RLock()
		username, password := user.UserName, user.Password
		user.RUnlock()
		if username != claims.Username {
			return rememberLoginCredential{}, errRememberLoginInvalid
		}
		return rememberLoginCredential{Username: username, Secret: password}, nil
	case sessionPrincipalClient:
		client, err := file.GetDb().GetClient(claims.ClientID)
		if err != nil || client == nil {
			return rememberLoginCredential{}, errRememberLoginInvalid
		}
		client.RLock()
		status, noDisplay, ownerID := client.Status, client.NoDisplay, client.UserId
		webUsername, webPassword, verifyKey := client.WebUserName, client.WebPassword, client.VerifyKey
		client.RUnlock()
		if !status || noDisplay || (ownerID != 0 && !file.GetDb().IsUserActive(ownerID)) {
			return rememberLoginCredential{}, errRememberLoginInvalid
		}
		username := webUsername
		if webUsername == "" && webPassword == "" {
			username = "user"
		}
		if username != claims.Username {
			return rememberLoginCredential{}, errRememberLoginInvalid
		}
		return rememberLoginCredential{
			Username: username,
			Secret:   webUsername + "\x00" + webPassword + "\x00" + verifyKey,
		}, nil
	default:
		return rememberLoginCredential{}, errRememberLoginInvalid
	}
}

func validateRememberLoginClaims(key []byte, claims *rememberLoginClaims) error {
	principalKey := rememberLoginPrincipalKey(claims)
	if principalKey == "" {
		return errRememberLoginInvalid
	}
	if !rememberLoginDevices.SessionEpochValid(principalKey, claims.Epoch) {
		return errRememberLoginInvalid
	}
	credential, err := rememberedLoginCredential(claims)
	if err != nil {
		return err
	}
	expected := rememberLoginFingerprint(key, *claims, credential.Secret)
	if subtle.ConstantTimeCompare([]byte(expected), []byte(claims.Fingerprint)) != 1 {
		return errRememberLoginInvalid
	}
	if !rememberLoginDevices.Validate(claims.DeviceID, principalKey, claims.Epoch, time.Now()) {
		return errRememberLoginInvalid
	}
	return nil
}

func issueRememberLoginCookie(controller *beego.Controller, ctx *beecontext.Context, now time.Time) error {
	if controller == nil || ctx == nil || ctx.ResponseWriter == nil || ctx.Request == nil {
		return errors.New("remember login context is unavailable")
	}
	key, err := rememberLoginKey()
	if err != nil {
		return err
	}
	claims, credential, err := currentRememberLoginCredential(controller)
	if err != nil {
		return err
	}
	principalKey := rememberLoginPrincipalKey(&claims)
	if principalKey == "" {
		return errRememberLoginInvalid
	}
	if existing, existingErr := rememberLoginCookieClaims(ctx, now); existingErr == nil &&
		existing.DeviceID != "" && rememberLoginPrincipalKey(existing) == principalKey {
		_, _ = rememberLoginDevices.Revoke(principalKey, existing.DeviceID)
	}
	deviceID, err := newWebSessionDeviceID()
	if err != nil {
		return err
	}
	claims.DeviceID = deviceID
	claims.Epoch = rememberLoginDevices.Epoch(principalKey)
	claims.IssuedAt = now.Unix()
	claims.Fingerprint = rememberLoginFingerprint(key, claims, credential.Secret)
	token, err := encodeRememberLoginToken(key, claims, now)
	if err != nil {
		return err
	}
	record := webSessionDeviceRecord{
		ID:         deviceID,
		Principal:  principalKey,
		Username:   claims.Username,
		UserID:     claims.UserID,
		ClientID:   claims.ClientID,
		IP:         common.GetIpByAddr(ctx.Request.RemoteAddr),
		UserAgent:  trimUserAgent(ctx.Request.UserAgent()),
		CreatedAt:  now.Unix(),
		LastSeenAt: now.Unix(),
		ExpiresAt:  now.Add(rememberLoginDuration).Unix(),
		Epoch:      claims.Epoch,
	}
	if err := rememberLoginDevices.Register(record); err != nil {
		return err
	}
	http.SetCookie(ctx.ResponseWriter, rememberLoginCookie(ctx.Request, token, now))
	return nil
}

func restoreRememberLogin(controller *beego.Controller, ctx *beecontext.Context, now time.Time) (restored bool, restoreErr error) {
	if controller == nil || ctx == nil || ctx.Request == nil {
		return false, nil
	}
	cookie, err := ctx.Request.Cookie(rememberLoginCookieName)
	if err != nil || cookie == nil || strings.TrimSpace(cookie.Value) == "" {
		return false, nil
	}
	wasAuthenticated := sessionBool(controller.GetSession("auth"))
	auditCode := "remember_unavailable"
	auditRequestMetadata(ctx, &audit.Event{})
	defer func() {
		if !wasAuthenticated {
			appendAuthenticationAudit(controller, ctx, "auth.remember.restore", "", restored, auditCode, "remember", nil)
		}
	}()
	key, err := rememberLoginKey()
	if err != nil {
		return false, err
	}
	claims, err := decodeRememberLoginToken(key, cookie.Value, now)
	if err == nil {
		err = validateRememberLoginClaims(key, claims)
	}
	if err != nil {
		auditCode = "remember_invalid"
		if ctx.ResponseWriter != nil {
			clearRememberLoginCookie(ctx.Request, ctx.ResponseWriter)
		}
		return false, err
	}
	store := controller.CruSession
	if store == nil {
		store = controller.StartSession()
	}
	if store == nil {
		return false, errors.New("session store is unavailable")
	}
	if err := store.Flush(); err != nil {
		return false, err
	}
	values := map[interface{}]interface{}{"auth": true, "username": claims.Username}
	values[sessionSecurityEpochKey] = int(claims.Epoch)
	values["rememberDeviceId"] = claims.DeviceID
	if rememberLoginPrincipal(claims) == "admin" {
		values["isAdmin"] = true
	} else {
		values["isAdmin"] = false
		values[sessionPrincipalKey] = claims.Principal
		values["username"] = claims.Username
		if claims.UserID > 0 {
			values["userId"] = claims.UserID
		}
		if claims.ClientID > 0 {
			values["clientId"] = claims.ClientID
			values["clientIds"] = map[int]struct{}{claims.ClientID: {}}
		}
	}
	for sessionKey, value := range values {
		if err := store.Set(sessionKey, value); err != nil {
			return false, err
		}
	}
	if _, ok := store.(*session.CookieSessionStore); ok && ctx.ResponseWriter != nil {
		store.SessionRelease(ctx.ResponseWriter)
	}
	return true, nil
}

func revokeCurrentRememberLoginDevice(controller *beego.Controller, ctx *beecontext.Context, now time.Time) bool {
	claims, err := rememberLoginCookieClaims(ctx, now)
	if err != nil || claims == nil || claims.DeviceID == "" {
		return false
	}
	principalKey := rememberLoginPrincipalKey(claims)
	if principalKey == "" {
		return false
	}
	revoked, err := rememberLoginDevices.Revoke(principalKey, claims.DeviceID)
	if err == nil && revoked && controller != nil {
		controller.DelSession("rememberDeviceId")
	}
	return revoked
}

func browserSessionSecurityValid(controller *beego.Controller, ctx *beecontext.Context, now time.Time) bool {
	if controller == nil || !sessionBool(controller.GetSession("auth")) {
		return false
	}
	principalKey := currentSessionPrincipalKey(controller)
	if principalKey == "" || !rememberLoginDevices.SessionEpochValid(principalKey, sessionSecurityEpoch(controller)) {
		return false
	}
	if ctx == nil || ctx.Request == nil {
		return true
	}
	if _, err := ctx.Request.Cookie(rememberLoginCookieName); err != nil {
		return true
	}
	claims, err := rememberLoginCookieClaims(ctx, now)
	if err != nil {
		return false
	}
	return rememberLoginPrincipalKey(claims) == principalKey
}

func releaseControllerCookieSession(controller *beego.Controller, ctx *beecontext.Context) {
	if controller == nil || controller.CruSession == nil || ctx == nil || ctx.ResponseWriter == nil {
		return
	}
	if _, ok := controller.CruSession.(*session.CookieSessionStore); !ok {
		return
	}
	controller.CruSession.SessionRelease(ctx.ResponseWriter)
}

func trimUserAgent(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 512 {
		return value[:512]
	}
	return value
}

func clearRememberLoginCookie(request *http.Request, response http.ResponseWriter) {
	if response == nil {
		return
	}
	http.SetCookie(response, rememberLoginDeletionCookie(request))
}

func rememberLoginCookie(request *http.Request, value string, now time.Time) *http.Cookie {
	return &http.Cookie{
		Name:     rememberLoginCookieName,
		Value:    value,
		Path:     rememberLoginCookiePath(),
		MaxAge:   int(rememberLoginDuration / time.Second),
		Expires:  now.Add(rememberLoginDuration),
		HttpOnly: true,
		Secure:   rememberLoginCookieSecure(request),
		SameSite: http.SameSiteLaxMode,
	}
}

func rememberLoginDeletionCookie(request *http.Request) *http.Cookie {
	return &http.Cookie{
		Name:     rememberLoginCookieName,
		Value:    "",
		Path:     rememberLoginCookiePath(),
		MaxAge:   -1,
		Expires:  time.Unix(1, 0),
		HttpOnly: true,
		Secure:   rememberLoginCookieSecure(request),
		SameSite: http.SameSiteLaxMode,
	}
}

func rememberLoginCookiePath() string {
	base := strings.TrimSpace(beego.AppConfig.String("web_base_url"))
	if base == "" || base == "/" {
		return "/"
	}
	parsed, err := url.Parse(base)
	if err == nil && parsed.Path != "" {
		base = parsed.Path
	}
	base = "/" + strings.Trim(strings.TrimSpace(base), "/")
	if base == "/" {
		return "/"
	}
	return base
}

func rememberLoginCookieSecure(request *http.Request) bool {
	if beego.AppConfig.String("web_open_ssl") == "true" || (request != nil && request.TLS != nil) {
		return true
	}
	if request == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(request.Header.Get("X-Forwarded-Proto")), "https")
}

func describeRememberLoginError(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("remember login: %v", err)
}
