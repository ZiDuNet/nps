package audit

import "strings"

// Labels are presentation metadata only. Stable action codes remain in JSONL
// files and API responses, including when a newer server reads older events.
type ActionDefinition struct {
	Code, Name, NameEN string
}

type ActionGroup struct {
	Name, NameEN string
	Actions      []ActionDefinition
}

var actionGroups = []ActionGroup{
	{"登录与账户安全", "Sign-in and security", []ActionDefinition{
		{"auth.login", "登录", "Sign in"},
		{"auth.logout", "退出登录", "Sign out"},
		{"auth.remember.restore", "免登录恢复", "Restore remembered sign-in"},
		{"auth.remember.issue", "签发免登录凭证", "Issue remembered sign-in"},
		{"auth.register", "注册账号", "Register account"},
		{"auth.register.email_code", "发送注册验证码", "Send registration code"},
		{"security.remember_device.revoke", "撤销登录设备", "Revoke sign-in device"},
		{"security.remember_device.revoke_all", "退出所有设备", "Sign out all devices"},
		{"security.totp.bind", "绑定动态验证码", "Bind TOTP"},
		{"security.totp.disable", "关闭动态验证码", "Disable TOTP"},
		{"security.pow.update", "修改 PoW 配置", "Update PoW settings"},
	}},
	{"客户端", "Clients", []ActionDefinition{
		{"client.create", "创建客户端", "Create client"}, {"client.update", "修改客户端", "Update client"},
		{"client.delete", "删除客户端", "Delete client"}, {"client.status_change", "启停客户端", "Change client status"},
		{"client.remote_update", "下发客户端升级", "Request client update"},
		{"client.instance.disconnect", "断开客户端实例", "Disconnect client instance"},
	}},
	{"隧道与域名", "Tunnels and hosts", []ActionDefinition{
		{"tunnel.create", "创建隧道", "Create tunnel"}, {"tunnel.update", "修改隧道", "Update tunnel"},
		{"tunnel.copy", "复制隧道", "Copy tunnel"}, {"tunnel.start", "启动隧道", "Start tunnel"},
		{"tunnel.stop", "停止隧道", "Stop tunnel"}, {"tunnel.delete", "删除隧道", "Delete tunnel"},
		{"host.create", "新增域名反代", "Create host"}, {"host.update", "修改域名反代", "Update host"},
		{"host.start", "启用域名反代", "Enable host"}, {"host.stop", "停用域名反代", "Disable host"},
		{"host.delete", "删除域名反代", "Delete host"},
	}},
	{"用户与密钥", "Users and credentials", []ActionDefinition{
		{"user.create", "创建用户", "Create user"}, {"user.update", "修改用户", "Update user"},
		{"user.delete", "删除用户", "Delete user"}, {"user.status_change", "启停用户", "Change user status"},
		{"user.password_change", "修改密码", "Change password"},
		{"user.api_key_rotate", "生成用户 Token", "Rotate user token"}, {"user.api_key_revoke", "撤销用户 Token", "Revoke user token"},
		{"admin.api_key_rotate", "生成管理员密钥", "Rotate admin key"}, {"admin.api_key_revoke", "撤销管理员密钥", "Revoke admin key"},
		{"dashboard.key_rotate", "生成大屏密钥", "Rotate dashboard key"}, {"dashboard.key_revoke", "撤销大屏密钥", "Revoke dashboard key"},
	}},
	{"系统与证书", "System and certificates", []ActionDefinition{
		{"server_config.update", "修改服务配置", "Update server configuration"},
		{"server_config.source_update", "修改配置源码", "Update configuration source"}, {"server.restart", "重启服务", "Restart server"},
		{"global.update", "修改全局参数", "Update global settings"}, {"global.geodata.update", "更新 Geo 数据", "Update Geo data"},
		{"global.registration_config.update", "修改注册配置", "Update registration settings"},
		{"global.certificate_config.update", "修改证书设置", "Update certificate settings"},
		{"global.bridge_certificate.update", "更新 Bridge 证书", "Update Bridge certificate"},
		{"global.platform_certificates.update", "更新泛域名与证书", "Update platform certificates"},
		{"global.ip_registration.revoke", "撤销 IP 登记", "Revoke IP registration"},
		{"global.ip_registration.clean", "清理过期 IP 登记", "Clean IP registrations"},
		{"global.ip_registration.clear", "清空 IP 登记", "Clear IP registrations"},
		{"global.login_ban.remove", "解除登录封禁", "Remove login ban"},
		{"global.login_ban.clean", "清理过期登录封禁", "Clean login bans"},
		{"global.login_ban.clear", "清空登录封禁", "Clear login bans"},
		// Historical spellings remain discoverable and readable.
		{"ip_registration.revoke", "撤销 IP 登记（历史）", "Revoke IP registration (legacy)"},
		{"ip_registration.clean", "清理 IP 登记（历史）", "Clean IP registrations (legacy)"},
		{"login_ban.remove", "解除登录封禁（历史）", "Remove login ban (legacy)"},
	}},
	{"客户端与代理连接", "Client and proxy connections", []ActionDefinition{
		{"bridge.connected", "主连接成功", "Main connected"}, {"bridge.disconnected", "主连接断开", "Main disconnected"},
		{"bridge.auth_failed", "客户端认证失败", "Client authentication failed"}, {"bridge.failed", "Bridge 阶段失败", "Bridge stage failed"},
		{"bridge.channel", "建立子通道", "Bridge channel"}, {"bridge.update", "客户端升级结果", "Client update result"},
		{"proxy.connection", "代理会话", "Proxy session"},
	}},
}

func ActionGroups(connections bool) []ActionGroup {
	groups := make([]ActionGroup, 0, len(actionGroups))
	for _, group := range actionGroups {
		if (group.NameEN == "Client and proxy connections") == connections {
			groups = append(groups, ActionGroup{group.Name, group.NameEN, append([]ActionDefinition(nil), group.Actions...)})
		}
	}
	return groups
}

func ActionName(code string, english bool) string {
	for _, group := range actionGroups {
		for _, action := range group.Actions {
			if action.Code == code {
				if english {
					return action.NameEN
				}
				return action.Name
			}
		}
	}
	return code
}

func ResourceName(code string, english bool) string {
	labels := map[string][2]string{
		"session": {"登录会话", "Sign-in session"}, "client": {"客户端", "Client"},
		"tunnel": {"隧道", "Tunnel"}, "host": {"域名反代", "Host"}, "user": {"用户", "User"},
		"global": {"全局参数", "Global settings"}, "nps.conf": {"服务配置", "Server configuration"},
		"nps": {"服务端", "Server"}, "dashboard": {"大屏密钥", "Dashboard key"},
		"ip_registration": {"IP 登记", "IP registration"}, "login_ban": {"登录封禁", "Login ban"},
	}
	if pair, ok := labels[code]; ok {
		if english {
			return pair[1]
		}
		return pair[0]
	}
	return code
}

func FailureName(code string, english bool) string {
	labels := map[string][2]string{
		"request_too_large":            {"登录请求过大或格式无效", "Sign-in request too large or malformed"},
		"pow_invalid":                  {"PoW 安全校验失败", "PoW verification failed"},
		"captcha_invalid":              {"图片验证码错误", "Image verification failed"},
		"login_expired":                {"登录请求已过期或已使用", "Sign-in request expired or already used"},
		"invalid_credentials":          {"账号或密码错误", "Incorrect account or password"},
		"totp_invalid":                 {"动态验证码错误或缺失", "TOTP code incorrect or missing"},
		"login_rate_limited":           {"登录尝试过于频繁或已被封禁", "Sign-in rate limited or blocked"},
		"remember_invalid":             {"免登录凭证无效、过期或已撤销", "Remembered sign-in invalid, expired or revoked"},
		"remember_unavailable":         {"免登录会话恢复失败", "Remembered session unavailable"},
		"remember_issue_failed":        {"免登录凭证签发失败", "Could not issue remembered sign-in"},
		"registration_disabled":        {"用户注册未开放", "Registration disabled"},
		"registration_rate_limited":    {"注册请求过于频繁", "Registration rate limited"},
		"registration_captcha_invalid": {"注册图片验证码错误", "Registration image verification failed"},
		"registration_ip_limit":        {"来源 IP 注册数量已达上限", "Registration IP limit reached"},
		"register_expired":             {"注册请求已过期", "Registration request expired"},
		"invalid_registration_input":   {"注册信息不符合要求", "Invalid registration details"},
		"username_taken":               {"注册用户名已被使用", "Username already registered"},
		"email_already_bound":          {"邮箱已绑定其他账号", "Email already linked"},
		"email_in_use":                 {"邮箱已绑定其他账号", "Email already linked"},
		"invalid_email":                {"邮箱格式无效", "Invalid email address"},
		"email_required":               {"未填写邮箱", "Email required"},
		"email_code_invalid":           {"邮箱验证码错误或已过期", "Email verification code incorrect or expired"},
		"registration_failed":          {"账号创建失败", "Account creation failed"},
		"email_verification_disabled":  {"注册邮箱验证未开启", "Registration email verification disabled"},
		"email_code_rate_limited":      {"邮件验证码请求过于频繁", "Email verification rate limited"},
		"email_code_unavailable":       {"暂时无法签发邮件验证码", "Email verification unavailable"},
		"smtp_incomplete":              {"注册邮件服务配置不完整", "Registration email configuration incomplete"},
		"email_send_failed":            {"注册验证码邮件发送失败", "Registration email delivery failed"},
	}
	if pair, ok := labels[code]; ok {
		if english {
			return pair[1]
		}
		return pair[0]
	}
	return code
}

func AuthStage(code string) string {
	switch code {
	case "request_too_large":
		return "request"
	case "pow_invalid":
		return "pow"
	case "captcha_invalid", "registration_captcha_invalid":
		return "captcha"
	case "login_expired", "register_expired":
		return "login_payload"
	case "login_rate_limited":
		return "rate_limit"
	case "totp_invalid":
		return "totp"
	case "invalid_credentials":
		return "password"
	case "registration_rate_limited", "registration_ip_limit", "email_code_rate_limited":
		return "rate_limit"
	case "email_code_invalid", "email_send_failed", "smtp_incomplete", "email_code_unavailable":
		return "email"
	}
	if strings.HasPrefix(code, "remember_") {
		return "remember"
	}
	if code != "" {
		return "validation"
	}
	return "complete"
}
