package controllers

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
	"time"

	"github.com/astaxie/beego"
)

func TestRegistrationExpireTimeUsesConfiguredDays(t *testing.T) {
	now := time.Date(2026, time.October, 29, 8, 30, 0, 0, time.Local)
	if got := registrationExpireTime(now, 0); got != "" {
		t.Fatalf("zero registration days = %q, want empty/unlimited", got)
	}
	if got, want := registrationExpireTime(now, 7), "2026-11-05 08:30:00"; got != want {
		t.Fatalf("seven-day registration expiry = %q, want %q", got, want)
	}
}

func TestRegistrationPageShowsUnlimitedOnlyForZeroQuota(t *testing.T) {
	tpl := template.Must(template.New("register").Funcs(template.FuncMap{
		"create_captcha": func() template.HTML { return "" },
	}).ParseFiles("../views/login/register.html"))
	tests := []struct {
		name      string
		clients   int
		tunnels   int
		days      int
		required  []string
		forbidden []string
	}{
		{
			name:     "zero means unlimited",
			clients:  0,
			tunnels:  0,
			days:     0,
			required: []string{"0 个客户端（不限）", "0 个隧道（不限）", "有效期：永久"},
		},
		{
			name:      "positive values omit unlimited note",
			clients:   1,
			tunnels:   2,
			days:      7,
			required:  []string{"1 个客户端、2 个隧道", "有效期：7 天"},
			forbidden: []string{"1 个客户端（不限）", "2 个隧道（不限）"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			err := tpl.ExecuteTemplate(&output, "register.html", map[string]interface{}{
				"register_max_clients": tt.clients,
				"register_max_tunnels": tt.tunnels,
				"register_expire_days": tt.days,
				"web_base_url":         "",
				"version":              "test",
			})
			if err != nil {
				t.Fatalf("render registration page: %v", err)
			}
			page := output.String()
			for _, marker := range tt.required {
				if !strings.Contains(page, marker) {
					t.Fatalf("registration page misses %q", marker)
				}
			}
			for _, marker := range tt.forbidden {
				if strings.Contains(page, marker) {
					t.Fatalf("registration page unexpectedly contains %q", marker)
				}
			}
		})
	}
}

func TestRegistrationRateLimiterEnforcesAttemptAndAccountWindows(t *testing.T) {
	limiter := newRegistrationRateLimiter(16)
	policy := registrationPolicy{
		window:   time.Minute,
		attempts: 2,
		accounts: 1,
	}
	now := time.Date(2026, time.October, 29, 8, 30, 0, 0, time.Local)
	if !limiter.reserveAttempt("192.0.2.10", "alice", now, policy) ||
		!limiter.reserveAttempt("192.0.2.10", "alice", now, policy) {
		t.Fatal("registration attempts below the limit were rejected")
	}
	if limiter.reserveAttempt("192.0.2.10", "alice", now, policy) {
		t.Fatal("registration attempt limit was not enforced")
	}

	if !limiter.reserveSuccess("192.0.2.10", now, policy) {
		t.Fatal("first registration account was rejected")
	}
	if limiter.reserveSuccess("192.0.2.10", now, policy) {
		t.Fatal("per-IP successful registration limit was not enforced")
	}
	if !limiter.reserveSuccess("192.0.2.10", now.Add(time.Minute), policy) {
		t.Fatal("registration account limit did not reset after its window")
	}
}

func TestRegistrationEmailCodeStoreExpiryCooldownAndSingleUse(t *testing.T) {
	store := newRegistrationEmailCodeStore(8)
	now := time.Date(2026, time.October, 29, 8, 30, 0, 0, time.Local)
	code, _, err := store.issue("Alice@Example.com", "192.0.2.20", now, 10*time.Minute, time.Minute)
	if err != nil {
		t.Fatalf("issue registration email code: %v", err)
	}
	if len(code) != 6 {
		t.Fatalf("email code length = %d, want 6", len(code))
	}
	if _, retryAfter, err := store.issue("alice@example.com", "192.0.2.20", now.Add(10*time.Second), 10*time.Minute, time.Minute); err == nil || retryAfter <= 0 {
		t.Fatalf("email code cooldown was not enforced: retry=%v err=%v", retryAfter, err)
	}
	if store.verify("alice@example.com", "000000"+code, now.Add(time.Minute)) {
		t.Fatal("invalid email code was accepted")
	}
	if !store.verify("ALICE@example.com", code, now.Add(time.Minute)) {
		t.Fatal("valid email code was rejected")
	}
	if store.verify("alice@example.com", code, now.Add(time.Minute)) {
		t.Fatal("email code was reusable")
	}

	expiredCode, _, err := store.issue("bob@example.com", "192.0.2.21", now, time.Minute, time.Minute)
	if err != nil {
		t.Fatalf("issue expiring email code: %v", err)
	}
	if store.verify("bob@example.com", expiredCode, now.Add(time.Minute)) {
		t.Fatal("expired email code was accepted")
	}
}

func TestRegistrationSMTPConfigurationValidation(t *testing.T) {
	oldHost := beego.AppConfig.String("smtp_host")
	oldFrom := beego.AppConfig.String("smtp_from")
	t.Cleanup(func() {
		_ = beego.AppConfig.Set("smtp_host", oldHost)
		_ = beego.AppConfig.Set("smtp_from", oldFrom)
	})
	_ = beego.AppConfig.Set("smtp_host", "")
	_ = beego.AppConfig.Set("smtp_from", "")
	if _, err := configuredRegistrationSMTP(); err == nil {
		t.Fatal("empty SMTP configuration should be rejected")
	}
}
