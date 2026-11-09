package controllers

import (
	"strconv"
	"strings"

	"ehang.io/nps/lib/file"
	"ehang.io/nps/web/audit"
	"github.com/astaxie/beego"
	beecontext "github.com/astaxie/beego/context"
	"github.com/google/uuid"
)

// Only selected metadata is captured. In particular, never serialize a login
// form, query string, Cookie, Authorization, password, nonce or TOTP value.
func auditRequestMetadata(ctx *beecontext.Context, event *audit.Event) {
	if ctx == nil || ctx.Request == nil {
		return
	}
	event.Method, event.Path = ctx.Request.Method, ctx.Request.URL.Path
	event.SourceIP, event.IPSource = requestVisitorIPDetails(ctx.Request)
	event.RemoteAddr = ctx.Request.RemoteAddr
	event.UserAgent = trimUserAgent(strings.NewReplacer("\r", " ", "\n", " ").Replace(ctx.Request.UserAgent()))
	if ctx.ResponseWriter != nil {
		event.RequestID = ctx.ResponseWriter.Header().Get("X-Request-ID")
		if event.RequestID == "" {
			event.RequestID = uuid.NewString()
			ctx.ResponseWriter.Header().Set("X-Request-ID", event.RequestID)
		}
	}
}

// Identity is attributed from an authenticated session only. A supplied
// username or unverified remember-cookie claim must never grant ownership.
func authenticatedAuditIdentity(controller *beego.Controller, event *audit.Event) {
	if controller == nil || !sessionBool(controller.GetSession("auth")) {
		return
	}
	name, _ := controller.GetSession("username").(string)
	if sessionBool(controller.GetSession("isAdmin")) {
		event.ActorType, event.ActorID = "admin", 0
		event.ActorName = beego.AppConfig.String("web_username")
		return
	}
	principal, _ := controller.GetSession(sessionPrincipalKey).(string)
	if principal == sessionPrincipalUser {
		id, _ := sessionInt(controller.GetSession("userId"))
		event.ActorType, event.ActorID, event.OwnerUserID, event.ActorName = "user", id, id, name
	} else if principal == sessionPrincipalClient {
		id, _ := sessionInt(controller.GetSession("clientId"))
		event.ActorType, event.ActorID = "client", id
		if name == "" {
			name = "user"
		}
		event.ActorName = name
		if client, err := file.GetDb().GetClient(id); err == nil && client != nil {
			client.RLock()
			event.OwnerUserID = client.UserId
			client.RUnlock()
		}
	}
}

func appendAuthenticationAudit(controller *beego.Controller, ctx *beecontext.Context, action, username string, success bool, code, method string, details map[string]string) {
	audit.Configure(beego.AppConfig.String("audit_log_path"))
	event := audit.Event{ActorType: "anonymous", ActorName: boundedAuditName(username), Action: action,
		ResourceType: "session", Result: audit.ResultFailure, ErrorCode: code, AuthMethod: method,
		Stage: audit.AuthStage(code), Details: details}
	if success {
		event.Result, event.Stage, event.ErrorCode = audit.ResultSuccess, "complete", ""
		// Registration and mail delivery do not authenticate the supplied name.
		if action != "auth.register" && action != "auth.register.email_code" {
			authenticatedAuditIdentity(controller, &event)
		} else if action == "auth.register" {
			if user, err := file.GetDb().GetUserByName(username); err == nil && user != nil {
				user.RLock()
				event.ActorType, event.ActorID, event.OwnerUserID, event.ActorName = "user", user.Id, user.Id, user.UserName
				user.RUnlock()
			}
		}
	} else {
		event.Error = audit.FailureName(code, false)
	}
	auditRequestMetadata(ctx, &event)
	if err := audit.Append(event); err != nil {
		beego.Error("写入认证审计日志失败:", err)
	}
}

func boundedAuditName(value string) string {
	value = strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ").Replace(value))
	runes := []rune(value)
	if len(runes) > 128 {
		return string(runes[:128])
	}
	return value
}

// Defer at the POST entry point so every early validation return is recorded
// once. The browser still receives its existing generic/localized response.
func (self *LoginController) auditAuthResponse(action, username, overrideCode string) {
	response, ok := self.Data["json"].(map[string]interface{})
	if !ok {
		return
	}
	status, _ := response["status"].(int)
	success := status == 1
	code, _ := response["code"].(string)
	if overrideCode != "" && !success {
		code = overrideCode
	}
	if !success && code == "" {
		code = "invalid_credentials"
	}
	method := "password"
	if action == "auth.register" {
		method = "registration"
	}
	if action == "auth.register.email_code" {
		method = "email_code"
	}
	if code == "totp_invalid" || (success && sessionBool(self.GetSession("isAdmin")) && strings.TrimSpace(beego.AppConfig.String("totp_secret")) != "") {
		method = "password+totp"
	}
	var details map[string]string
	if action == "auth.login" {
		details = map[string]string{"remember_requested": strconv.FormatBool(self.Ctx.Request.PostForm.Get("remember_login") == "1")}
	}
	appendAuthenticationAudit(&self.Controller, self.Ctx, action, username, success, code, method, details)
}
