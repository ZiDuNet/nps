package controllers

import (
	"bytes"
	"html/template"
	"os"
	"strings"
	"testing"
)

func TestMobileAuthenticationTemplatesKeepExistingFeatures(t *testing.T) {
	for _, tt := range []struct {
		file     string
		features []string
	}{
		{"index.html", []string{
			`id="login-form"`, `onsubmit="return login()"`, `name="remember_login"`,
			`name="pow_nonce"`, `name="pow_solution"`, `name="totp"`, `name="captcha"`,
			`autocomplete="current-password"`, `href="/panel/ip/register"`,
			`href="/panel/login/register"`, `autocapitalize="none" spellcheck="false"`,
		}},
		{"register.html", []string{
			`id="register-form"`, `onsubmit="return register()"`, `name="email"`,
			`name="email_code"`, `id="auth-send-email-code"`, `name="captcha"`,
			`autocomplete="new-password"`, `inputmode="email"`, `inputmode="numeric"`,
			`href="/panel/login/index"`, `默认权益`, `有效期：永久`,
		}},
		{"ipregister.html", []string{
			`id="ip-register-form"`, `onsubmit="return registerIP()"`, `name="key"`,
			`type="number" inputmode="numeric" min="1" step="1"`,
			`href="/panel/login/index"`, `2001:db8:85a3:1234:5678:8a2e:370:7334`,
			`X-Forwarded-For`, `role="status"`,
		}},
	} {
		t.Run(tt.file, func(t *testing.T) {
			tpl, err := template.New(tt.file).Funcs(template.FuncMap{
				"create_captcha": func() template.HTML { return `<img class="captcha-img" alt="验证码">` },
			}).ParseFiles("../views/login/" + tt.file)
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			if err := tpl.Execute(&output, map[string]interface{}{
				"web_base_url": "/panel", "version": "test", "login_url": "/panel/login/index",
				"captcha_open": true, "pow_enabled": true, "totp_enabled": true, "register_allow": true,
				"email_verify": true, "email_ready": true, "email_code_cooldown": 60,
				"ip": "2001:db8:85a3:1234:5678:8a2e:370:7334", "ip_source": "X-Forwarded-For",
				"default_hours": 24, "ip_registration_configured": true, "ip_proxy_warning": true,
			}); err != nil {
				t.Fatal(err)
			}
			page := output.String()
			for _, marker := range append([]string{
				`content="width=device-width, initial-scale=1.0, viewport-fit=cover"`,
				`nps-login-v2.css?v=test&ui=20261110-1`, `class="auth-login-footer"`,
				`nps-auth-shell.js`, `motion=on`,
			}, tt.features...) {
				if !strings.Contains(page, marker) {
					t.Errorf("mobile authentication page must retain %q", marker)
				}
			}
			for _, forbidden := range []string{"user-scalable=no", "maximum-scale=1"} {
				if strings.Contains(page, forbidden) {
					t.Errorf("mobile authentication must not disable zoom: %q", forbidden)
				}
			}
		})
	}
}

func TestMobileAuthenticationLayoutKeepsOneScrollerAndReadableControls(t *testing.T) {
	content, err := os.ReadFile("../static/css/nps-login-v2.css")
	if err != nil {
		t.Fatal(err)
	}
	_, mobile, found := strings.Cut(string(content), "@media (max-width: 820px), (max-width: 1024px) and (max-height: 500px) {")
	if !found {
		t.Fatal("authentication layout must adapt to phones in portrait and landscape")
	}
	mobile, _, _ = strings.Cut(mobile, "@media (max-width: 560px)")
	for _, rule := range []struct {
		selector string
		want     []string
	}{
		{".auth-login-v2", []string{"flex-direction: column;", "min-height: 100dvh;", "env(safe-area-inset-bottom"}},
		{".auth-login-toolbar", []string{"position: relative;", "flex: 0 0 auto;"}},
		{".auth-login-shell--ip", []string{"height: auto;", "min-height: 0;", "grid-template-columns: minmax(0, 1fr);"}},
		{".auth-login-shell--ip .auth-login-panel", []string{"overflow: visible;"}},
		{".auth-login-protocols", []string{"display: none;"}},
		{".auth-login-control .form-control", []string{"font-size: 16px;", "min-height: 46px;"}},
		{".auth-login-captcha", []string{"grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);"}},
		{".auth-login-email-code-button", []string{"min-height: 46px;", "white-space: normal;"}},
		{".auth-login-ip-meta__source", []string{"color: var(--auth-muted);", "font-size: 12px;"}},
		{".auth-login-footer", []string{"position: static;", "flex: 0 0 auto;", "flex-wrap: wrap;"}},
	} {
		t.Run(rule.selector, func(t *testing.T) {
			_, body, ok := strings.Cut(mobile, "body.auth-login-page "+rule.selector+" {")
			if !ok {
				t.Fatalf("missing responsive rule for %s", rule.selector)
			}
			body, _, _ = strings.Cut(body, "}")
			for _, marker := range rule.want {
				if !strings.Contains(body, marker) {
					t.Errorf("mobile %s must keep %q", rule.selector, marker)
				}
			}
		})
	}
	for _, marker := range []string{"overflow-wrap: anywhere;", "text-overflow: clip;", "white-space: normal;"} {
		_, rule, _ := strings.Cut(mobile, ".auth-login-ip-meta__value,\n    body.auth-login-page .auth-login-ip-meta__source {")
		rule, _, _ = strings.Cut(rule, "}")
		if !strings.Contains(rule, marker) {
			t.Errorf("mobile IPv6 and its source must display in full: missing %q", marker)
		}
	}
}
