package controllers

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"ehang.io/nps/lib/crypt"
	"ehang.io/nps/lib/file"
	"ehang.io/nps/lib/version"
	"ehang.io/nps/server"
	"github.com/astaxie/beego"
	beecontext "github.com/astaxie/beego/context"
	"github.com/google/uuid"
)

const (
	apiV1MaxBodyBytes = 10 << 20
	apiV1DefaultLimit = 20
	apiV1MaxLimit     = 500
)

// ApiV1Controller implements the versioned public API at /api/v1. It
// deliberately reuses the existing controller actions for every mutation so
// validation, authorization, quotas and audit stay single-sourced with the
// management console. Only the transport conventions are new: JSON or form
// input, a uniform envelope, and credential-only authentication.
type ApiV1Controller struct {
	BaseController
}

// ApiV1InfoController serves the public capability document and the
// credential-free bootstrap endpoints. It is the only /api/v1 controller that
// works without authentication.
type ApiV1InfoController struct {
	beego.Controller
}

// Prepare enforces the /api/v1 authentication contract. Unlike the console,
// this namespace never accepts a browser cookie session: calls must carry an
// administrator timestamp signature or a user API token.
func (s *ApiV1Controller) Prepare() {
	if strings.TrimSpace(s.requestID) == "" {
		s.requestID = uuid.NewString()
	}
	if s.Ctx != nil && s.Ctx.ResponseWriter != nil {
		s.Ctx.ResponseWriter.Header().Set("X-Request-ID", s.requestID)
	}

	// Parse JSON bodies before any credential lookup so API clients can send
	// auth_key/api_token inside the JSON payload as well as in the query.
	if err := s.mergeJSONBody(); err != nil {
		s.apiRespond(http.StatusBadRequest, map[string]interface{}{
			"status": 0, "code": "error", "error_code": "invalid_json", "msg": err.Error(),
		})
		return
	}

	hasSignature := strings.TrimSpace(s.GetString("auth_key")) != ""
	hasToken := extractUserAPIToken(s.Ctx.Request.Header.Get("Authorization"), s.GetString("api_token")) != ""
	if !hasSignature && !hasToken {
		s.apiRespond(http.StatusUnauthorized, map[string]interface{}{
			"status": 0, "code": "error", "error_code": "unauthorized",
			"msg": "缺少 API 凭据：请使用 auth_key+timestamp 签名或 Authorization: Bearer <token>",
		})
		return
	}

	// BaseController.Prepare redirects browser-style unauthorized requests and
	// ends with StopRun. Run it against a discard writer and swallow only the
	// StopRun sentinel so an invalid credential can never leak a login
	// redirect from an API route.
	discard := newResponseRecorder()
	originalWriter := s.Ctx.ResponseWriter
	s.Ctx.ResponseWriter = &beecontext.Response{ResponseWriter: discard}
	func() {
		defer func() {
			if recovered := recover(); recovered != nil && recovered != beego.ErrAbort {
				panic(recovered)
			}
		}()
		s.BaseController.Prepare()
	}()
	s.Ctx.ResponseWriter = originalWriter

	if !s.apiAuthorized && !s.userAPIAuthorized {
		s.apiRespond(http.StatusUnauthorized, map[string]interface{}{
			"status": 0, "code": "error", "error_code": "unauthorized",
			"msg": "API 凭据无效或已过期",
		})
		return
	}
	s.applyPaginationDefaults()
}

// apiRespond writes the uniform /api/v1 envelope and stops the action chain.
func (s *ApiV1Controller) apiRespond(status int, payload map[string]interface{}) {
	if s.requestID != "" {
		if _, exists := payload["request_id"]; !exists {
			payload["request_id"] = s.requestID
		}
		if s.Ctx != nil && s.Ctx.ResponseWriter != nil {
			s.Ctx.ResponseWriter.Header().Set("X-Request-ID", s.requestID)
		}
	}
	s.Ctx.Output.SetStatus(status)
	s.Data["json"] = payload
	s.ServeJSON()
	s.StopRun()
}

func (s *ApiV1Controller) apiData(data interface{}) {
	s.apiRespond(http.StatusOK, map[string]interface{}{
		"status": 1, "code": "ok", "msg": "ok", "data": data,
	})
}

func (s *ApiV1Controller) apiFail(status int, errorCode, msg string) {
	s.apiRespond(status, map[string]interface{}{
		"status": 0, "code": "error", "error_code": errorCode, "msg": msg,
	})
}

// requireAdmin rejects non-administrator credentials with a JSON 403. The
// delegated action still performs its own check; this gate only guarantees the
// correct API shape when it fails.
func (s *ApiV1Controller) requireAdmin() bool {
	if s.IsAdmin() {
		return true
	}
	s.apiFail(http.StatusForbidden, "forbidden", "该接口仅限管理员调用")
	return false
}

// mergeJSONBody accepts an application/json request body and merges scalar
// values into the request form so existing controllers parse it unchanged.
func (s *ApiV1Controller) mergeJSONBody() error {
	request := s.Ctx.Request
	if request == nil || request.Body == nil {
		return nil
	}
	if !strings.Contains(strings.ToLower(request.Header.Get("Content-Type")), "application/json") {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, apiV1MaxBodyBytes))
	if err != nil {
		return fmt.Errorf("读取 JSON 请求体失败: %w", err)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	var values map[string]interface{}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&values); err != nil {
		return fmt.Errorf("JSON 请求体无效: %w", err)
	}
	if request.Form == nil {
		request.Form = url.Values{}
	}
	if request.PostForm == nil {
		request.PostForm = url.Values{}
	}
	for key, value := range values {
		encoded, encodeErr := encodeJSONParam(key, value)
		if encodeErr != nil {
			return encodeErr
		}
		for _, item := range encoded {
			request.Form.Add(key, item)
			request.PostForm.Add(key, item)
		}
	}
	return nil
}

// encodeJSONParam converts one decoded JSON value into form values. Arrays
// use newline separation for legacy list fields and commas for port/server
// lists that reject embedded newlines.
func encodeJSONParam(key string, value interface{}) ([]string, error) {
	switch typed := value.(type) {
	case nil:
		return nil, nil
	case string:
		return []string{typed}, nil
	case bool:
		return []string{strconv.FormatBool(typed)}, nil
	case json.Number:
		return []string{typed.String()}, nil
	case float64:
		return []string{strconv.FormatFloat(typed, 'f', -1, 64)}, nil
	case []interface{}:
		separator := "\r\n"
		switch key {
		case "allow_ports", "p2p_stun_servers":
			separator = ","
		}
		items := make([]string, 0, len(typed))
		for _, element := range typed {
			encoded, err := encodeJSONParam(key, element)
			if err != nil {
				return nil, err
			}
			items = append(items, encoded...)
		}
		return []string{strings.Join(items, separator)}, nil
	default:
		return nil, fmt.Errorf("字段 %s 的类型不受支持，请使用字符串、数字、布尔或数组", key)
	}
}

// pathFormValue copies a route parameter into the request form so the
// delegated legacy action can read it with its usual getters.
func (s *ApiV1Controller) pathFormValue(key string) string {
	value := strings.TrimSpace(s.Ctx.Input.Param(":" + key))
	if value == "" {
		return ""
	}
	if s.Ctx.Request.Form == nil {
		s.Ctx.Request.Form = url.Values{}
	}
	if s.Ctx.Request.PostForm == nil {
		s.Ctx.Request.PostForm = url.Values{}
	}
	s.Ctx.Request.Form.Set(key, value)
	s.Ctx.Request.PostForm.Set(key, value)
	return value
}

// applyPaginationDefaults keeps list calls useful for API clients, which do
// not send the DataTables offset/limit pair. page/page_size are accepted as an
// alternative to offset/limit; values are clamped to keep a single request from
// forcing an unbounded scan.
func (s *ApiV1Controller) applyPaginationDefaults() {
	if s.Ctx == nil || s.Ctx.Request == nil {
		return
	}
	if s.Ctx.Request.Form == nil {
		s.Ctx.Request.Form = url.Values{}
	}
	if s.Ctx.Request.PostForm == nil {
		s.Ctx.Request.PostForm = url.Values{}
	}

	limit := s.GetIntNoErr("limit")
	if limit <= 0 {
		limit = s.GetIntNoErr("page_size")
	}
	if limit <= 0 {
		limit = apiV1DefaultLimit
	}
	if limit > apiV1MaxLimit {
		limit = apiV1MaxLimit
	}

	offset := s.GetIntNoErr("offset")
	if strings.TrimSpace(s.GetString("offset")) == "" {
		offset = 0
		if page := s.GetIntNoErr("page"); page > 1 {
			offset = (page - 1) * limit
		}
	}
	if offset < 0 {
		offset = 0
	}

	s.Ctx.Request.Form.Set("limit", strconv.Itoa(limit))
	s.Ctx.Request.Form.Set("offset", strconv.Itoa(offset))
	s.Ctx.Request.PostForm.Set("limit", strconv.Itoa(limit))
	s.Ctx.Request.PostForm.Set("offset", strconv.Itoa(offset))
}

// runLegacyAction runs an existing controller action against a response
// recorder. It shares the current request context so the action sees the same
// form, session and API principal as this /api/v1 request. A Beego StopRun
// panic is a normal action termination, not an error.
func runLegacyAction(ctx *beecontext.Context, target beego.ControllerInterface, controllerName, actionName string, forcePost bool) (recorder *responseRecorder, err error) {
	if ctx == nil || ctx.Request == nil {
		return nil, errors.New("请求上下文为空")
	}
	if target == nil {
		return nil, errors.New("目标控制器为空")
	}
	controllerValue := reflect.ValueOf(target)
	if controllerValue.Kind() != reflect.Ptr || controllerValue.IsNil() {
		return nil, errors.New("目标控制器必须是非空指针")
	}
	actionMethod := controllerValue.MethodByName(actionName)
	if !actionMethod.IsValid() {
		return nil, fmt.Errorf("未找到动作 %s.%s", controllerName, actionName)
	}

	recorder = newResponseRecorder()
	originalWriter := ctx.ResponseWriter
	ctx.ResponseWriter = &beecontext.Response{ResponseWriter: recorder}
	originalMethod := ctx.Request.Method
	if forcePost {
		ctx.Request.Method = http.MethodPost
	}
	defer func() {
		ctx.ResponseWriter = originalWriter
		ctx.Request.Method = originalMethod
	}()

	// The outer request has already started its session (Beego does this for
	// every routed request), and the delegated controller shares the same
	// context, so there is no need to start a second session store.
	target.Init(ctx, controllerName, actionName, target)

	func() {
		defer func() {
			if recovered := recover(); recovered != nil && recovered != beego.ErrAbort {
				err = fmt.Errorf("执行动作 %s.%s 时发生异常: %v", controllerName, actionName, recovered)
			}
		}()
		target.Prepare()
		actionMethod.Call(nil)
	}()
	if err != nil {
		return recorder, err
	}
	return recorder, nil
}

// invokeLegacy is the ApiV1Controller-facing wrapper around runLegacyAction.
func (s *ApiV1Controller) invokeLegacy(controllerName, actionName string, target interface{}, forcePost bool) (*responseRecorder, error) {
	controller, ok := target.(beego.ControllerInterface)
	if !ok || controller == nil {
		return nil, errors.New("目标控制器未实现 ControllerInterface")
	}
	return runLegacyAction(s.Ctx, controller, controllerName, actionName, forcePost)
}

// emitLegacy converts a legacy controller response into the /api/v1 envelope.
func (s *ApiV1Controller) emitLegacy(recorder *responseRecorder) {
	emitLegacyEnvelope(&s.Controller, s.requestID, recorder)
}

// emitLegacyEnvelope normalizes a legacy JSON response captured in a recorder
// into the uniform /api/v1 shape and writes it through the supplied controller.
func emitLegacyEnvelope(controller *beego.Controller, requestID string, recorder *responseRecorder) {
	fail := func(status int, errorCode, msg string) {
		payload := map[string]interface{}{
			"status": 0, "code": "error", "error_code": errorCode, "msg": msg,
		}
		if requestID != "" {
			payload["request_id"] = requestID
			controller.Ctx.ResponseWriter.Header().Set("X-Request-ID", requestID)
		}
		controller.Ctx.Output.SetStatus(status)
		controller.Data["json"] = payload
		controller.ServeJSON()
		controller.StopRun()
	}
	success := func(status int, payload map[string]interface{}) {
		if requestID != "" {
			if _, exists := payload["request_id"]; !exists {
				payload["request_id"] = requestID
			}
			controller.Ctx.ResponseWriter.Header().Set("X-Request-ID", requestID)
		}
		controller.Ctx.Output.SetStatus(status)
		controller.Data["json"] = payload
		controller.ServeJSON()
		controller.StopRun()
	}

	if recorder == nil {
		fail(http.StatusBadGateway, "upstream_error", "旧控制器未返回响应")
		return
	}
	body := bytes.TrimSpace(recorder.BodyBytes())
	if len(body) == 0 {
		status := recorder.StatusCode()
		// Legacy Prepare/CheckUserAuth uses a bare StopRun for unauthorized
		// resources; a redirect, an empty 200 or no status all mean "not
		// allowed" rather than an upstream failure.
		if recorder.Header().Get("Location") != "" || status == 0 || status == http.StatusOK {
			fail(http.StatusForbidden, "forbidden", "凭据无权访问该资源")
			return
		}
		fail(status, "upstream_error", "旧控制器未返回响应")
		return
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		if recorder.Header().Get("Location") != "" {
			fail(http.StatusForbidden, "forbidden", "凭据无权访问该资源，或登录会话已失效")
			return
		}
		fail(http.StatusBadGateway, "upstream_error", "旧控制器返回了无法解析的响应")
		return
	}

	if raw, ok := payload["request_id"].(string); ok && raw != "" {
		requestID = raw
	}

	if status, ok := numericStatus(payload["status"]); ok {
		if status == 0 {
			msg, _ := payload["msg"].(string)
			if msg == "" {
				msg = "操作失败"
			}
			httpStatus := http.StatusBadRequest
			switch {
			case strings.Contains(strings.ToLower(msg), "permission") || strings.Contains(msg, "无权") || strings.Contains(msg, "仅限管理员"):
				httpStatus = http.StatusForbidden
			case strings.Contains(strings.ToLower(msg), "not found") || strings.Contains(msg, "不存在") || strings.Contains(msg, "未找到"):
				httpStatus = http.StatusNotFound
			}
			if recorder.StatusCode() >= http.StatusBadRequest {
				httpStatus = recorder.StatusCode()
			}
			fail(httpStatus, "operation_failed", msg)
			return
		}
	}

	if _, isList := payload["rows"]; isList {
		response := map[string]interface{}{
			"status": 1, "code": "ok", "msg": "ok",
			"rows":  payload["rows"],
			"total": payload["total"],
		}
		for _, key := range []string{"meta", "ip", "bridgeType", "bridgePort", "addr", "traffic"} {
			if value, exists := payload[key]; exists {
				response[key] = value
			}
		}
		success(http.StatusOK, response)
		return
	}

	// Code/data is the historical single-resource shape; status/msg is the
	// management shape. Both normalize to the same success envelope.
	code, hasCode := numericStatus(payload["code"])
	if !hasCode || code == 1 {
		msg := "ok"
		if raw, ok := payload["msg"].(string); ok && raw != "" {
			msg = raw
		}
		data := map[string]interface{}{}
		if nested, ok := payload["data"]; ok {
			if mapped, ok := nested.(map[string]interface{}); ok {
				data = mapped
			}
		}
		for key, value := range payload {
			switch key {
			case "status", "msg", "code", "request_id", "data":
				continue
			default:
				data[key] = value
			}
		}
		success(http.StatusOK, map[string]interface{}{
			"status": 1, "code": "ok", "msg": msg, "data": data,
		})
		return
	}

	msg := "操作失败"
	if raw, ok := payload["msg"].(string); ok && raw != "" {
		msg = raw
	}
	fail(http.StatusBadRequest, "operation_failed", msg)
}

func numericStatus(value interface{}) (int, bool) {
	switch typed := value.(type) {
	case float64:
		return int(typed), true
	case int:
		return typed, true
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, false
		}
		return int(parsed), true
	default:
		return 0, false
	}
}

// infoRespond writes the public /api/v1 envelope from the unauthenticated
// discovery controller.
func (s *ApiV1InfoController) infoRespond(status int, payload map[string]interface{}) {
	s.Ctx.Output.SetStatus(status)
	s.Data["json"] = payload
	s.ServeJSON()
	s.StopRun()
}

// Info renders the public capability document.
func (s *ApiV1InfoController) Info() {
	s.infoRespond(http.StatusOK, map[string]interface{}{
		"status":      1,
		"code":        "ok",
		"msg":         "ok",
		"version":     version.VERSION,
		"github":      version.GitHubRepository,
		"auth":        []string{"admin_md5_timestamp", "user_bearer_token"},
		"description": "NPS 对外管理 API v1。旧 /client、/index、/user、/global、/v1/auth 接口继续兼容。",
		"resources": []string{
			"clients", "tunnels", "hosts", "users", "global", "status", "overview", "audit",
		},
	})
}

// Time returns the server timestamp for timestamp-signature clock sync.
func (s *ApiV1InfoController) Time() {
	s.Ctx.ResponseWriter.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	s.Ctx.ResponseWriter.Header().Set("Pragma", "no-cache")
	s.Ctx.ResponseWriter.Header().Set("Expires", "0")
	s.infoRespond(http.StatusOK, map[string]interface{}{
		"status": 1,
		"code":   "ok",
		"msg":    "ok",
		"data":   map[string]interface{}{"time": time.Now().Unix()},
	})
}

// AuthKey returns the encrypted auth_key for upstream-compatible clients.
func (s *ApiV1InfoController) AuthKey() {
	cryptKey := beego.AppConfig.String("auth_crypt_key")
	if len(cryptKey) != 16 {
		s.infoRespond(http.StatusBadRequest, map[string]interface{}{
			"status": 0, "code": "error", "error_code": "auth_crypt_key_invalid",
			"msg": "auth_crypt_key 未配置或长度必须为 16 位",
		})
		return
	}
	encrypted, err := crypt.AesEncrypt([]byte(beego.AppConfig.String("auth_key")), []byte(cryptKey))
	if err != nil {
		s.infoRespond(http.StatusInternalServerError, map[string]interface{}{
			"status": 0, "code": "error", "error_code": "encrypt_failed", "msg": "加密 auth_key 失败",
		})
		return
	}
	s.infoRespond(http.StatusOK, map[string]interface{}{
		"status": 1,
		"code":   "ok",
		"msg":    "ok",
		"data": map[string]interface{}{
			"crypt_auth_key": hex.EncodeToString(encrypted),
			"crypt_type":     "aes cbc",
		},
	})
}

// IPWhiteAuth registers the caller's public IP for a client whitelist. It is
// intentionally unauthenticated: the client vkey plus its whitelist password
// are the credentials.
func (s *ApiV1InfoController) IPWhiteAuth() {
	echoIPWhiteAuthCORS(s.Ctx)
	if s.Ctx.Request.Method == http.MethodOptions {
		s.Ctx.ResponseWriter.WriteHeader(http.StatusNoContent)
		return
	}
	handleIPWhiteAuth(s.Ctx, func(status int, success bool, message string) {
		if success {
			s.infoRespond(http.StatusOK, map[string]interface{}{
				"status": 1, "code": "ok", "msg": message, "data": map[string]interface{}{},
			})
			return
		}
		s.infoRespond(status, map[string]interface{}{
			"status": 0, "code": "error", "error_code": "ip_white_auth_failed", "msg": message,
		})
	})
}

// responseRecorder is a small http.ResponseWriter that keeps the optional
// Started flag Beego's output layer expects on context.Response.
type responseRecorder struct {
	header http.Header
	body   *bytes.Buffer
	code   int
}

func newResponseRecorder() *responseRecorder {
	return &responseRecorder{header: http.Header{}, body: &bytes.Buffer{}}
}

func (r *responseRecorder) Header() http.Header            { return r.header }
func (r *responseRecorder) Write(data []byte) (int, error) { return r.body.Write(data) }
func (r *responseRecorder) BodyBytes() []byte              { return r.body.Bytes() }
func (r *responseRecorder) StatusCode() int                { return r.code }
func (r *responseRecorder) WriteHeader(statusCode int) {
	if r.code == 0 {
		r.code = statusCode
	}
}

// Status exposes the account-scoped dashboard snapshot.
func (s *ApiV1Controller) Status() {
	var data interface{}
	if s.IsAdmin() {
		data = server.GetDashboardData()
	} else {
		data = server.GetDashboardDataForClients(s.GetAllowedClientIds())
	}
	s.apiData(data)
}

// Overview exposes the account-scoped resource topology.
func (s *ApiV1Controller) Overview() {
	var data server.TopologySnapshot
	if s.IsAdmin() {
		data = server.GetTopologyData(nil, true)
	} else {
		data = server.GetTopologyData(s.GetAllowedClientIds(), false)
	}
	s.apiData(data)
}

// UserGet returns one user row without exposing stored credentials.
func (s *ApiV1Controller) UserGet() {
	if !s.requireAdmin() {
		return
	}
	id := s.GetIntNoErr("id")
	if id <= 0 {
		s.apiFail(http.StatusNotFound, "not_found", "user ID not found")
		return
	}
	user, err := file.GetDb().GetUser(id)
	if err != nil || user == nil {
		s.apiFail(http.StatusNotFound, "not_found", "user ID not found")
		return
	}
	rows := newUserListRowsWithResourceCounts([]*file.User{user}, file.GetDb().GetUserResourceCounts())
	if len(rows) != 1 {
		s.apiFail(http.StatusNotFound, "not_found", "user ID not found")
		return
	}
	s.apiData(rows[0])
}
