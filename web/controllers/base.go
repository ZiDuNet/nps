package controllers

import (
	"crypto/subtle"
	"errors"
	"html"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"ehang.io/nps/bridge"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/connectionlog"
	"ehang.io/nps/lib/crypt"
	"ehang.io/nps/lib/file"
	"ehang.io/nps/lib/version"
	"ehang.io/nps/server"
	"ehang.io/nps/web/audit"
	"github.com/astaxie/beego"
	"github.com/astaxie/beego/logs"
	"github.com/google/uuid"
)

type BaseController struct {
	beego.Controller
	controllerName    string
	actionName        string
	apiAuthorized     bool
	userAPIAuthorized bool
	apiUserID         int
	requestID         string
}

const (
	sessionPrincipalKey    = "authPrincipal"
	sessionPrincipalUser   = "user"
	sessionPrincipalClient = "client"
)

// accountQuotaSummary is presentation data for a normal dashboard User. It
// intentionally excludes legacy client-login principals because their single
// client credential is not a user-owned account with self-service quotas.
type accountQuotaSummary struct {
	Visible     bool
	ClientUsed  int
	ClientLimit int
	TunnelUsed  int
	TunnelLimit int
}

// 初始化参数
func (s *BaseController) Prepare() {
	s.Data["web_base_url"] = beego.AppConfig.String("web_base_url")
	// API layers may allocate the request ID before delegating to this
	// controller; reuse it so the header and response envelope always agree.
	if strings.TrimSpace(s.requestID) == "" {
		s.requestID = uuid.NewString()
	}
	if s.Ctx != nil && s.Ctx.ResponseWriter != nil {
		s.Ctx.ResponseWriter.Header().Set("X-Request-ID", s.requestID)
	}
	audit.Configure(beego.AppConfig.String("audit_log_path"))
	connectionlog.Configure(beego.AppConfig.String("connection_log_path"))
	controllerName, actionName := s.GetControllerAndAction()
	if len(controllerName) > len("Controller") {
		s.controllerName = strings.ToLower(controllerName[0 : len(controllerName)-len("Controller")])
	} else {
		s.controllerName = strings.ToLower(controllerName)
	}
	s.actionName = strings.ToLower(actionName)
	// web api verify
	// param 1 is md5(authKey+Current timestamp)
	// param 2 is timestamp (It's limited to 20 seconds.)
	md5Key := s.getEscapeString("auth_key")
	timestamp := s.GetIntNoErr("timestamp")
	configKey := strings.TrimSpace(beego.AppConfig.String("auth_key"))
	timeNowUnix := time.Now().Unix()
	expectedKey := crypt.Md5(configKey + strconv.Itoa(timestamp))
	apiAuthorized := configKey != "" && md5Key != "" &&
		(math.Abs(float64(timeNowUnix-int64(timestamp))) <= 20) &&
		subtle.ConstantTimeCompare([]byte(expectedKey), []byte(md5Key)) == 1
	// The web-managed administrator key is equivalent to auth_key: the same
	// signed timestamp protocol accepts either secret, so API clients written
	// for the configuration file key keep working after an online rotation.
	if !apiAuthorized && md5Key != "" && math.Abs(float64(timeNowUnix-int64(timestamp))) <= 20 {
		if managedKey, ok := managedAdminAPIKey(); ok {
			managedExpected := crypt.Md5(managedKey + strconv.Itoa(timestamp))
			apiAuthorized = subtle.ConstantTimeCompare([]byte(managedExpected), []byte(md5Key)) == 1
		}
	}
	// API credentials authorize this request only. Persisting this flag in a
	// browser session turns a short-lived signed API call into a lasting admin
	// login, which is both surprising and unsafe.
	s.apiAuthorized = apiAuthorized
	if !apiAuthorized {
		userToken := extractUserAPIToken(s.Ctx.Request.Header.Get("Authorization"), s.GetString("api_token"))
		if userID, ok := lookupUserByAPIToken(userToken); ok {
			s.userAPIAuthorized = true
			s.apiUserID = userID
		}
	}
	authorizedRequest := apiAuthorized || s.userAPIAuthorized || sessionBool(s.GetSession("auth"))
	if !authorizedRequest {
		// A redirect does not stop Beego from invoking the action by itself, so
		// StopRun is required before returning from Prepare.
		s.Redirect(beego.AppConfig.String("web_base_url")+"/login/index", 302)
		s.StopRun()
		return
	}
	isAdminSession := s.GetSession("isAdmin")
	isAdmin := s.IsAdmin()
	if !apiAuthorized && !s.userAPIAuthorized {
		if _, ok := isAdminSession.(bool); !ok {
			// Keep downstream controllers compatible with their historical bool
			// assertions even when a session store returns a string or nil.
			s.SetSession("isAdmin", isAdmin)
		}
	}
	if !isAdmin {
		// A non-admin session must still map to an active principal on every
		// request. User/client status or ownership can change after login, so a
		// cached session alone is not sufficient authorization.
		if !s.userAPIAuthorized && !s.hasActiveNonAdminPrincipal() {
			clearAuthenticationSession(s.DelSession)
			s.Redirect(beego.AppConfig.String("web_base_url")+"/login/index", 302)
			s.StopRun()
			return
		}
		s.Data["isAdmin"] = false
		if s.userAPIAuthorized {
			if user, err := file.GetDb().GetUser(s.apiUserID); err == nil && user != nil {
				user.RLock()
				s.Data["username"] = user.UserName
				user.RUnlock()
			}
		} else {
			s.Data["username"] = s.GetSession("username")
		}
		s.Data["accountExpireTime"] = s.accountExpireTime()
		s.Data["accountQuota"] = s.accountQuota()
		// Ordinary users cannot browse the administrative user/global pages.
		// They may only open the API access page and manage their own token or
		// dashboard password through the narrowly-scoped endpoints below.
		if (s.controllerName == "global" && !nonAdminGlobalActionAllowed(s.actionName)) ||
			(s.controllerName == "user" && !nonAdminUserActionAllowed(s.actionName)) {
			s.StopRun()
			return
		}
		s.CheckUserAuth()
	} else {
		s.Data["isAdmin"] = true
		s.Data["accountExpireTime"] = ""
		s.Data["accountQuota"] = accountQuotaSummary{}
	}
	s.Data["allow_user_login"], _ = beego.AppConfig.Bool("allow_user_login")
	s.Data["allow_flow_limit"], _ = beego.AppConfig.Bool("allow_flow_limit")
	s.Data["allow_rate_limit"], _ = beego.AppConfig.Bool("allow_rate_limit")
	s.Data["allow_connection_num_limit"], _ = beego.AppConfig.Bool("allow_connection_num_limit")
	s.Data["allow_multi_ip"], _ = beego.AppConfig.Bool("allow_multi_ip")
	s.Data["system_info_display"], _ = beego.AppConfig.Bool("system_info_display")
	s.Data["allow_tunnel_num_limit"], _ = beego.AppConfig.Bool("allow_tunnel_num_limit")
	s.Data["allow_local_proxy"], _ = beego.AppConfig.Bool("allow_local_proxy")
	s.Data["allow_user_local"], _ = beego.AppConfig.Bool("allow_user_local")
	s.Data["allow_user_change_username"], _ = beego.AppConfig.Bool("allow_user_change_username")
	showHttpProxyPort := beego.AppConfig.DefaultBool("show_http_proxy_port", true)
	httpPort := publicAccessPort("http_proxy_access_port", "http_proxy_port")
	if httpPort != "80" && showHttpProxyPort {
		s.Data["http_proxy_port"] = ":" + httpPort
	}
	httpsPort := publicAccessPort("https_proxy_access_port", "https_proxy_port")
	if httpsPort != "" && httpsPort != "443" {
		s.Data["https_proxy_port"] = ":" + httpsPort
	}
}

// publicAccessPort is deliberately separate from the listener port.  NPS can
// listen on an internal port while a reverse proxy exposes the same Host rule
// on another public port; the setting is used only when building links for
// users, never when binding sockets or routing requests.
func publicAccessPort(accessKey, listenerKey string) string {
	access := strings.TrimSpace(beego.AppConfig.String(accessKey))
	if access != "" {
		return access
	}
	return strings.TrimSpace(beego.AppConfig.String(listenerKey))
}

// auditMutation records a control-plane action after authorization has been
// checked. Details are intentionally supplied by each caller rather than
// serializing request parameters, preventing passwords, keys, and other
// credentials from entering the audit journal.
func (s *BaseController) auditMutation(action, resourceType string, resourceID, ownerUserID int, result string, err error, details map[string]string) {
	actorType, actorID, actorName := s.auditActor()
	errorText := ""
	if err != nil {
		errorText = err.Error()
		result = audit.ResultFailure
	}
	if result == "" {
		result = audit.ResultSuccess
	}
	method, path, sourceIP := "", "", ""
	if s.Ctx != nil && s.Ctx.Request != nil {
		method = s.Ctx.Request.Method
		path = s.Ctx.Request.URL.Path
		sourceIP = s.Ctx.Input.IP()
	}
	if writeErr := audit.Append(audit.Event{
		ActorType: actorType, ActorID: actorID, ActorName: actorName,
		SourceIP: sourceIP, Method: method, Path: path,
		Action: action, ResourceType: resourceType, ResourceID: resourceID,
		OwnerUserID: ownerUserID, Result: result, Error: errorText, Details: details,
	}); writeErr != nil {
		logs.Error("写入操作审计日志失败: %v", writeErr)
	}
}

func (s *BaseController) auditActor() (string, int, string) {
	if s.userAPIAuthorized {
		name := "api-user"
		if user, err := file.GetDb().GetUser(s.apiUserID); err == nil && user != nil {
			user.RLock()
			name = user.UserName
			user.RUnlock()
		}
		return "api-user", s.apiUserID, name
	}
	if s.apiAuthorized {
		return "api", 0, "api"
	}
	name, _ := s.GetSession("username").(string)
	if s.IsAdmin() {
		if strings.TrimSpace(name) == "" {
			name = "admin"
		}
		return "admin", 0, name
	}
	principal, _ := s.GetSession(sessionPrincipalKey).(string)
	if principal == sessionPrincipalClient {
		id, _ := sessionInt(s.GetSession("clientId"))
		return "client", id, name
	}
	id, _ := sessionInt(s.GetSession("userId"))
	return "user", id, name
}

// IsAdmin returns the effective privilege for the current request. Signed API
// authentication is deliberately request-scoped; browser session elevation is
// kept separate from it.
func (s *BaseController) IsAdmin() bool {
	return isAdminAuthorized(s.apiAuthorized, s.GetSession("isAdmin"))
}

func isAdminAuthorized(apiAuthorized bool, sessionValue interface{}) bool {
	return apiAuthorized || sessionBool(sessionValue)
}

// nonAdminUserActionAllowed narrows the user controller to the pages and
// endpoints an ordinary account may use for its own credentials.
func nonAdminUserActionAllowed(action string) bool {
	switch action {
	case "api", "changepassword", "regenerateapikey", "revokeapikey", "apikeystatus":
		return true
	default:
		return false
	}
}

// nonAdminGlobalActionAllowed exposes only the account-scoped IP registration
// registry. Every listed action performs a second ownership check in
// GlobalController, so a user cannot widen the scope through a crafted
// client_id parameter.
func nonAdminGlobalActionAllowed(action string) bool {
	switch action {
	case "ipregisterpage", "ipregisterlist", "ipregisterrevoke", "ipregisterclean", "ipregisterclear":
		return true
	default:
		return false
	}
}

func (s *BaseController) hasActiveNonAdminPrincipal() bool {
	principal, _ := s.GetSession(sessionPrincipalKey).(string)
	userID, _ := sessionInt(s.GetSession("userId"))
	clientID, _ := sessionInt(s.GetSession("clientId"))

	userActive := userID > 0 && file.GetDb().IsUserActive(userID)
	clientActive := false
	if clientID > 0 {
		if client, err := file.GetDb().GetClient(clientID); err == nil {
			client.RLock()
			clientActive = client.Status && !client.NoDisplay
			clientUserID := client.UserId
			client.RUnlock()
			if clientActive && clientUserID != 0 {
				clientActive = file.GetDb().IsUserActive(clientUserID)
			}
		}
	}
	return activeNonAdminPrincipal(principal, userID, clientID, userActive, clientActive)
}

func activeNonAdminPrincipal(principal string, userID, clientID int, userActive, clientActive bool) bool {
	switch principal {
	case sessionPrincipalUser:
		return userID > 0 && userActive
	case sessionPrincipalClient:
		return clientID > 0 && clientActive
	default:
		// Sessions created before the principal marker existed are deliberately
		// invalidated. They can contain stale user/client identifiers from a
		// previous login and cannot be attributed safely.
		return false
	}
}

// accountExpireTime returns the effective expiry that applies to the signed-in
// non-admin principal. A normal dashboard user is governed by the User
// record. Legacy client-logins can also be bounded by their assigned user, so
// the earlier valid expiry is shown instead of incorrectly claiming that the
// account never expires.
func (s *BaseController) accountExpireTime() string {
	if s.userAPIAuthorized {
		if user, err := file.GetDb().GetUser(s.apiUserID); err == nil && user != nil {
			user.RLock()
			expire := user.ExpireTime
			user.RUnlock()
			return strings.TrimSpace(expire)
		}
		return ""
	}
	principal, _ := s.GetSession(sessionPrincipalKey).(string)
	userID, _ := sessionInt(s.GetSession("userId"))
	clientID, _ := sessionInt(s.GetSession("clientId"))

	var userExpire, clientExpire string
	if userID > 0 {
		if user, err := file.GetDb().GetUser(userID); err == nil && user != nil {
			user.RLock()
			userExpire = user.ExpireTime
			user.RUnlock()
		}
	}
	if clientID > 0 {
		if client, err := file.GetDb().GetClient(clientID); err == nil && client != nil {
			client.RLock()
			clientExpire = client.ExpireTime
			if userID == 0 {
				userID = client.UserId
			}
			client.RUnlock()
		}
		if userID > 0 && userExpire == "" {
			if user, err := file.GetDb().GetUser(userID); err == nil && user != nil {
				user.RLock()
				userExpire = user.ExpireTime
				user.RUnlock()
			}
		}
	}

	if principal == sessionPrincipalUser {
		return strings.TrimSpace(userExpire)
	}
	if principal != sessionPrincipalClient {
		return ""
	}
	return earliestExpireTime(clientExpire, userExpire)
}

// accountQuota returns the same allocation counters the server uses when a
// user creates resources. Disabled or expired clients are still counted: they
// remain allocated credentials until an administrator deletes or reassigns
// them, so the sidebar cannot imply that a new slot is available prematurely.
func (s *BaseController) accountQuota() accountQuotaSummary {
	userID, ok := s.currentUserPrincipalID()
	if !ok {
		return accountQuotaSummary{}
	}
	user, err := file.GetDb().GetUser(userID)
	if err != nil || user == nil {
		return accountQuotaSummary{}
	}
	user.RLock()
	clientLimit, tunnelLimit := user.MaxClientNum, user.MaxTunnelNum
	user.RUnlock()
	return accountQuotaSummary{
		Visible:     true,
		ClientUsed:  file.GetDb().GetUserClientNum(userID),
		ClientLimit: clientLimit,
		TunnelUsed:  file.GetDb().GetUserTunnelNum(userID),
		TunnelLimit: tunnelLimit,
	}
}

func earliestExpireTime(values ...string) string {
	var earliest time.Time
	var fallback string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if fallback == "" {
			fallback = value
		}
		if parsed, ok := ParseExpireTime(value); ok && (earliest.IsZero() || parsed.Before(earliest)) {
			earliest = parsed
		}
	}
	if !earliest.IsZero() {
		return earliest.Format("2006-01-02 15:04:05")
	}
	return fallback
}

func clearAuthenticationSession(deleteSession func(interface{})) {
	for _, key := range []string{
		"auth",
		"isAdmin",
		"clientId",
		"clientIds",
		"userId",
		"username",
		sessionPrincipalKey,
	} {
		deleteSession(key)
	}
}

// RequirePost guards state-changing management actions so accidental GET
// requests cannot mutate state. Origin/same-origin validation is deliberately
// absent: reverse proxies and mixed HTTP/HTTPS deployments produced too many
// false rejections, and the panel relies on its session and API credentials
// for authorization instead.
func (s *BaseController) RequirePost() bool {
	if s.Ctx.Request.Method != http.MethodPost {
		s.rejectRequest(http.StatusMethodNotAllowed, "method not allowed")
		return false
	}
	return true
}

// RequireAdmin protects operations that alter the client ownership boundary.
// Hiding their buttons in the UI is not authorization; signed API requests
// continue to pass through IsAdmin's request-scoped credential check.
func (s *BaseController) RequireAdmin() bool {
	if s.IsAdmin() {
		return true
	}
	s.rejectRequest(http.StatusForbidden, "permission denied")
	return false
}

// RequireAdminOrClientOwner allows ordinary dashboard users to remove only
// clients currently assigned to their own account. Legacy per-client login
// sessions are intentionally excluded from this self-service operation.
func (s *BaseController) RequireAdminOrClientOwner(clientID int) bool {
	if s.IsAdmin() {
		return true
	}
	userID, ok := s.currentUserPrincipalID()
	if ok && file.GetDb().IsClientBelongToUser(clientID, userID) {
		return true
	}
	s.rejectRequest(http.StatusForbidden, "permission denied")
	return false
}

func (s *BaseController) rejectRequest(status int, message string) {
	s.Ctx.Output.SetStatus(status)
	s.Data["json"] = s.withRequestMeta(ajax(message, 0))
	s.ServeJSON()
	// ServeJSON only writes the response. StopRun prevents a controller action
	// from continuing into a state mutation after an authorization failure.
	s.StopRun()
}

func useForwardedAuthority(request *http.Request) bool {
	if request == nil {
		return false
	}
	if requestMayUseForwardedIP(request) {
		return true
	}
	// A reverse proxy can preserve the public authority even when the operator
	// does not enable real-IP handling. The proxy must overwrite these headers
	// with its own values.
	return firstForwardedHost(request) != "" ||
		strings.TrimSpace(request.Header.Get("X-Forwarded-Proto")) != "" ||
		strings.TrimSpace(request.Header.Get("Forwarded")) != ""
}

func requestScheme(request *http.Request, trustedProxy ...bool) string {
	trustForwarded := len(trustedProxy) > 0 && trustedProxy[0]
	if trustForwarded {
		if forwarded := strings.ToLower(strings.TrimSpace(strings.Split(request.Header.Get("X-Forwarded-Proto"), ",")[0])); forwarded == "http" || forwarded == "https" {
			return forwarded
		}
		if forwarded := strings.ToLower(strings.TrimSpace(forwardedParameter(request.Header.Get("Forwarded"), "proto"))); forwarded == "http" || forwarded == "https" {
			return forwarded
		}
	}
	if request.TLS != nil {
		return "https"
	}
	return "http"
}

func firstForwardedHost(request *http.Request) string {
	if request == nil {
		return ""
	}
	if host := firstHeaderValue(request.Header.Get("X-Forwarded-Host")); host != "" {
		return host
	}
	return firstHeaderValue(forwardedParameter(request.Header.Get("Forwarded"), "host"))
}

func firstHeaderValue(value string) string {
	return strings.TrimSpace(strings.Split(value, ",")[0])
}

// forwardedParameter reads the first RFC 7239 Forwarded element. Quoted
// values are accepted because common reverse proxies quote IPv6 authorities.
func forwardedParameter(value, key string) string {
	value = strings.TrimSpace(strings.Split(value, ",")[0])
	for _, part := range strings.Split(value, ";") {
		pieces := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(pieces) != 2 || !strings.EqualFold(strings.TrimSpace(pieces[0]), key) {
			continue
		}
		return strings.Trim(strings.TrimSpace(pieces[1]), "\"")
	}
	return ""
}

// sessionBool accepts the values produced by Beego's session backends. Some
// deployments serialize booleans as strings or numeric flags.
func sessionBool(value interface{}) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		if typed == "1" {
			return true
		}
		if typed == "0" {
			return false
		}
		parsed, err := strconv.ParseBool(typed)
		return err == nil && parsed
	case int:
		return typed != 0
	case int8:
		return typed != 0
	case int16:
		return typed != 0
	case int32:
		return typed != 0
	case int64:
		return typed != 0
	case uint:
		return typed != 0
	case uint8:
		return typed != 0
	case uint16:
		return typed != 0
	case uint32:
		return typed != 0
	case uint64:
		return typed != 0
	default:
		return false
	}
}

func sessionInt(value interface{}) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int8:
		return int(typed), true
	case int16:
		return int(typed), true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case uint:
		return int(typed), true
	case uint8:
		return int(typed), true
	case uint16:
		return int(typed), true
	case uint32:
		return int(typed), true
	case uint64:
		return int(typed), true
	default:
		return 0, false
	}
}

// 加载模板
func (s *BaseController) display(tpl ...string) {
	s.Data["web_base_url"] = beego.AppConfig.String("web_base_url")
	var tplname string
	if s.Data["menu"] == nil {
		s.Data["menu"] = s.actionName
	}
	if len(tpl) > 0 {
		tplname = strings.Join([]string{tpl[0], "html"}, ".")
	} else {
		tplname = s.controllerName + "/" + s.actionName + ".html"
	}
	global := file.GetDb().GetGlobal()
	serverURL := ""
	if global != nil {
		serverURL = global.ServerUrl
	}
	s.Data["ip"] = publicBridgeHost(s.Ctx.Request.Host, serverURL, beego.AppConfig.String("bridge_addr"))

	s.Data["bridgeType"] = beego.AppConfig.String("bridge_type")
	if common.IsWindows() {
		s.Data["win"] = "npc.exe"
	} else {
		s.Data["win"] = "./npc"
	}

	s.Data["p"] = strconv.Itoa(server.Bridge.TunnelPort)
	s.Data["version"] = version.VERSION
	s.Data["github_repository"] = version.GitHubRepository

	if bridge.ServerTlsEnable {
		tlsPort := strconv.Itoa(beego.AppConfig.DefaultInt("tls_bridge_port", 8025))
		s.Data["tls_p"] = tlsPort
		s.Data["tls_enable"] = true
		s.Data["p1"] = strconv.Itoa(server.Bridge.TunnelPort) + " / " + tlsPort
	} else {
		s.Data["tls_enable"] = false
		s.Data["p1"] = strconv.Itoa(server.Bridge.TunnelPort)
	}

	s.Data["proxyPort"] = beego.AppConfig.String("hostPort")
	s.Layout = "public/layout.html"
	s.TplName = tplname
}

// publicServerHost extracts only the public hostname/IP used to build TCP
// tunnel access addresses. The tunnel's own port is appended separately; a
// URL scheme or management/bridge port must never become part of that address.
func publicServerHost(serverAddress string) string {
	serverAddress = strings.TrimSpace(serverAddress)
	if serverAddress == "" {
		return ""
	}
	if ip := net.ParseIP(strings.Trim(serverAddress, "[]")); ip != nil {
		return ip.String()
	}
	candidate := serverAddress
	parseValue := serverAddress
	if !strings.Contains(parseValue, "://") {
		parseValue = "//" + parseValue
	}
	if parsed, err := url.Parse(parseValue); err == nil && parsed.Host != "" {
		candidate = parsed.Host
	}
	return common.GetIpByAddr(candidate)
}

// publicBridgeHost resolves the public hostname used in generated NPC
// commands. bridge_addr has precedence for installations behind NAT or a
// reverse proxy; the global server URL and current request host are fallbacks.
func publicBridgeHost(requestHost, serverURL, bridgeAddr string) string {
	for _, candidate := range []string{bridgeAddr, serverURL, requestHost} {
		if host := publicServerHost(candidate); host != "" {
			return host
		}
	}
	return ""
}

// 错误
func (s *BaseController) error() {
	s.Data["code"] = 404
	s.Data["title"] = "页面未找到"
	s.Data["message"] = "您访问的地址不存在、已被移动，或对应的资源已被删除。"
	s.Data["web_base_url"] = beego.AppConfig.String("web_base_url")
	s.Data["version"] = version.VERSION
	s.Data["github_repository"] = version.GitHubRepository
	s.TplName = "public/error.html"
}

// getEscapeString
func (s *BaseController) getEscapeString(key string) string {
	return html.EscapeString(s.GetString(key))
}

// 去掉没有err返回值的int
func (s *BaseController) GetIntNoErr(key string, def ...int) int {
	strv := s.Ctx.Input.Query(key)
	if len(strv) == 0 && len(def) > 0 {
		return def[0]
	}
	val, _ := strconv.Atoi(strv)
	return val
}

// 获取去掉错误的bool值
func (s *BaseController) GetBoolNoErr(key string, def ...bool) bool {
	strv := s.Ctx.Input.Query(key)
	if len(strv) == 0 && len(def) > 0 {
		return def[0]
	}
	val, _ := strconv.ParseBool(strv)
	return val
}

// ajax正确返回
func (s *BaseController) AjaxOk(str string) {
	s.Data["json"] = s.withRequestMeta(ajax(str, 1))
	s.ServeJSON()
	s.StopRun()
}

// ajax正确返回
func (s *BaseController) AjaxOkWithId(str string, id int) {
	s.Data["json"] = s.withRequestMeta(ajaxWithId(str, 1, id))
	s.ServeJSON()
	s.StopRun()
}

// ajaxDetail serializes a single-resource response with both envelopes.
// Legacy callers keep reading code/data while newer clients get the standard
// status/msg/request_id fields used by every other management endpoint.
func (s *BaseController) ajaxDetail(data interface{}, err error) map[string]interface{} {
	if err != nil {
		return s.withRequestMeta(map[string]interface{}{
			"code":   0,
			"status": 0,
			"msg":    err.Error(),
		})
	}
	return s.withRequestMeta(map[string]interface{}{
		"code":   1,
		"status": 1,
		"msg":    "ok",
		"data":   data,
	})
}

// ajax错误返回
func (s *BaseController) AjaxErr(str string) {
	s.Data["json"] = s.withRequestMeta(ajax(str, 0))
	s.ServeJSON()
	s.StopRun()
}

// withRequestMeta adds stable, non-breaking metadata to legacy AJAX responses.
// Existing callers continue to consume status/msg/id, while API clients can
// correlate an error or mutation with the server log using X-Request-ID.
func (s *BaseController) withRequestMeta(payload map[string]interface{}) map[string]interface{} {
	if payload == nil {
		payload = map[string]interface{}{}
	}
	if _, exists := payload["code"]; !exists {
		status, statusOK := payload["status"].(int)
		_, isList := payload["rows"]
		if (statusOK && status == 1) || isList {
			payload["code"] = "ok"
		} else {
			payload["code"] = "error"
		}
	}
	if s.requestID != "" {
		payload["request_id"] = s.requestID
	}
	return payload
}

// 组装ajax
func ajax(str string, status int) map[string]interface{} {
	json := make(map[string]interface{})
	json["status"] = status
	json["msg"] = str
	return json
}

// 组装ajax
func ajaxWithId(str string, status int, id int) map[string]interface{} {
	json := make(map[string]interface{})
	json["status"] = status
	json["msg"] = str
	json["id"] = id
	return json
}

// ajax table返回
func (s *BaseController) AjaxTable(list interface{}, cnt int, recordsTotal int, kwargs map[string]interface{}) {
	json := make(map[string]interface{})
	json["rows"] = list
	json["total"] = recordsTotal
	offset, limit := s.GetAjaxParams()
	json["meta"] = map[string]interface{}{
		"offset":    offset,
		"limit":     limit,
		"page":      pageNumber(offset, limit),
		"page_size": limit,
		"has_more":  hasMoreRows(offset, limit, recordsTotal),
	}
	if kwargs != nil {
		for k, v := range kwargs {
			if v != nil {
				json[k] = v
			}
		}
	}
	s.Data["json"] = s.withRequestMeta(json)
	s.ServeJSON()
	s.StopRun()
}

func pageNumber(offset, limit int) int {
	if limit <= 0 || offset < 0 {
		return 1
	}
	return offset/limit + 1
}

func hasMoreRows(offset, limit, total int) bool {
	if limit <= 0 || total <= 0 || offset < 0 {
		return false
	}
	return offset+limit < total
}

// ajax table参数
func (s *BaseController) GetAjaxParams() (start, limit int) {
	return s.GetIntNoErr("offset"), s.GetIntNoErr("limit")
}

// getOwnerListFilter parses the optional administrator list filter. The empty
// value means "all owners"; an explicit user_id=0 selects resources attached
// to unassigned clients. Controllers call this only after their admin check,
// so ordinary users cannot widen the ownership scope of their list requests.
func (s *BaseController) getOwnerListFilter() (*file.OwnerFilter, error) {
	raw := strings.TrimSpace(s.GetString("user_id"))
	if raw == "" {
		if s.GetBoolNoErr("unassigned") {
			unassigned := 0
			return &file.OwnerFilter{UserID: &unassigned}, nil
		}
		return nil, nil
	}
	userID, err := strconv.Atoi(raw)
	if err != nil || userID < 0 {
		return nil, errors.New("所属用户筛选无效")
	}
	return &file.OwnerFilter{UserID: &userID}, nil
}

func (s *BaseController) SetInfo(name string) {
	s.Data["name"] = name
}

// setOwnerFilterData supplies the administrator-only owner options used by
// resource list toolbars. Keeping this in the shared controller avoids
// duplicating the user lookup across client, tunnel, and host list actions;
// ordinary users never receive the list because their UI is already scoped to
// their own principal.
func (s *BaseController) setOwnerFilterData() {
	if !s.IsAdmin() {
		return
	}
	s.Data["users"], _ = file.GetDb().GetUserList(0, 10000, "")
}

// setClientFilterData supplies only the clients visible to the current
// principal. The list endpoint independently enforces this same ownership
// boundary for every request; these options are presentation data only.
func (s *BaseController) setClientFilterData() {
	if !s.IsAdmin() {
		s.Data["clients"], _ = file.GetDb().GetClientListForAllowedIds(0, 10000, "", "", "", 0, s.GetAllowedClientIds())
		return
	}
	s.Data["clients"], _ = file.GetDb().GetClientList(0, 10000, "", "", "", 0)
}

func (s *BaseController) SetType(name string) {
	s.Data["type"] = name
}

func (s *BaseController) CheckUserAuth() {
	allowedClientIds := s.GetAllowedClientIds()
	if s.controllerName == "client" {
		if s.actionName == "add" {
			// A dashboard User may create an additional client for itself. A
			// legacy client-login principal only represents one existing client
			// and must never be able to mint another one.
			if _, ok := s.currentUserPrincipalID(); !ok {
				s.StopRun()
			}
			return
		}
		if id := s.GetIntNoErr("id"); id != 0 {
			if !isAllowedClient(id, allowedClientIds) {
				s.StopRun()
				return
			}
		}
	}
	if s.controllerName == "index" {
		if clientId := s.GetIntNoErr("client_id"); clientId != 0 && !isAllowedClient(clientId, allowedClientIds) {
			s.StopRun()
			return
		}
		if id := s.GetIntNoErr("id"); id != 0 {
			belong := false
			if strings.Contains(s.actionName, "h") {
				if v, ok := file.GetDb().JsonDb.Hosts.Load(id); ok {
					host, valid := v.(*file.Host)
					if valid && host != nil {
						host.RLock()
						client := host.Client
						host.RUnlock()
						if client != nil {
							client.RLock()
							clientID := client.Id
							client.RUnlock()
							belong = isAllowedClient(clientID, allowedClientIds)
						}
					}
				}
			} else {
				if v, ok := file.GetDb().JsonDb.Tasks.Load(id); ok {
					task, valid := v.(*file.Tunnel)
					if valid && task != nil {
						task.RLock()
						client := task.Client
						task.RUnlock()
						if client != nil {
							client.RLock()
							clientID := client.Id
							client.RUnlock()
							belong = isAllowedClient(clientID, allowedClientIds)
						}
					}
				}
			}
			if !belong {
				s.StopRun()
			}
		}
	}
}

// currentUserPrincipalID returns the persisted dashboard User identity for
// the current session. It deliberately excludes the legacy per-client login
// principal, which has a client ID but no authority to create new clients.
func (s *BaseController) currentUserPrincipalID() (int, bool) {
	if s.userAPIAuthorized && s.apiUserID > 0 {
		return s.apiUserID, true
	}
	principal, _ := s.GetSession(sessionPrincipalKey).(string)
	userID, _ := sessionInt(s.GetSession("userId"))
	if principal != sessionPrincipalUser || userID <= 0 {
		return 0, false
	}
	return userID, true
}

func (s *BaseController) GetAllowedClientIds() map[int]struct{} {
	if s.userAPIAuthorized && s.apiUserID > 0 {
		return file.GetDb().UserClientIds(s.apiUserID)
	}
	principal, _ := s.GetSession(sessionPrincipalKey).(string)
	userID, _ := sessionInt(s.GetSession("userId"))
	clientID, _ := sessionInt(s.GetSession("clientId"))
	legacyIDs, _ := s.GetSession("clientIds").(map[int]struct{})
	return allowedClientIDsForPrincipal(principal, userID, clientID, legacyIDs, file.GetDb().UserClientIds)
}

// allowedClientIDsForPrincipal reads a user's membership from the database on
// every request. This makes reassignment and revocation effective immediately
// rather than when the next session is created.
func allowedClientIDsForPrincipal(principal string, userID, clientID int, legacyIDs map[int]struct{}, loadUserClientIDs func(int) map[int]struct{}) map[int]struct{} {
	switch principal {
	case sessionPrincipalUser:
		if userID > 0 {
			return loadUserClientIDs(userID)
		}
	case sessionPrincipalClient:
		if clientID > 0 {
			return map[int]struct{}{clientID: {}}
		}
	}
	if legacyIDs != nil {
		return legacyIDs
	}
	return map[int]struct{}{}
}

func isAllowedClient(id int, allowedClientIds map[int]struct{}) bool {
	_, ok := allowedClientIds[id]
	return ok
}
