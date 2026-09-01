package controllers

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestReserveLoginAttemptDoesNotCountImplicitLoginPageChecks(t *testing.T) {
	const ip = "implicit-login-page"
	ipRecord.Delete(ip)
	t.Cleanup(func() { ipRecord.Delete(ip) })

	if _, exists := ipRecord.Load(ip); exists {
		t.Fatal("test setup left an existing login record")
	}
	if !allowLoginAttempt(ip, false, time.Now()) {
		t.Fatal("implicit login page checks should remain allowed")
	}
	if _, exists := ipRecord.Load(ip); exists {
		t.Fatal("implicit login page checks must not create a failure record")
	}
}

func TestReserveLoginAttemptLimitsConcurrentFailures(t *testing.T) {
	const ip = "concurrent-login-attempts"
	ipRecord.Delete(ip)
	t.Cleanup(func() { ipRecord.Delete(ip) })

	var allowed int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < maxLoginFailures*4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if reserveLoginAttempt(ip, time.Now()) {
				atomic.AddInt32(&allowed, 1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := int(atomic.LoadInt32(&allowed)); got != maxLoginFailures {
		t.Fatalf("allowed concurrent login attempts = %d, want %d", got, maxLoginFailures)
	}
}

func TestReserveLoginAttemptExpiresFailureWindow(t *testing.T) {
	const ip = "expired-login-attempts"
	ipRecord.Delete(ip)
	t.Cleanup(func() { ipRecord.Delete(ip) })

	now := time.Unix(1_700_000_000, 0)
	for i := 0; i < maxLoginFailures; i++ {
		if !reserveLoginAttempt(ip, now) {
			t.Fatalf("attempt %d unexpectedly rejected", i+1)
		}
	}
	if reserveLoginAttempt(ip, now) {
		t.Fatal("attempt after the failure limit should be rejected")
	}
	if !reserveLoginAttempt(ip, now.Add(loginFailureWindow)) {
		t.Fatal("attempt after the failure window should be admitted")
	}
}

func TestLoginAttemptIP(t *testing.T) {
	if got := loginAttemptIP("127.0.0.1:8080"); got != "127.0.0.1" {
		t.Fatalf("IPv4 login IP = %q, want 127.0.0.1", got)
	}
	if got := loginAttemptIP("[2001:db8::1]:8080"); got != "2001:db8::1" {
		t.Fatalf("IPv6 login IP = %q, want 2001:db8::1", got)
	}
	if got := loginAttemptIP("malformed"); got != "malformed" {
		t.Fatalf("malformed login address = %q, want fallback", got)
	}
}

func TestRegenerateSessionWithoutManagerIsSafe(t *testing.T) {
	var controller LoginController
	controller.regenerateSession()
}
