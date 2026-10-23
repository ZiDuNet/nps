package controllers

import (
	"crypto/sha256"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/astaxie/beego"
)

func TestVerifyLoginPoWConsumesShortLivedChallenge(t *testing.T) {
	oldBits := beego.AppConfig.String("pow_bits")
	oldForce := beego.AppConfig.String("force_pow")
	_ = beego.AppConfig.Set("pow_bits", "1")
	_ = beego.AppConfig.Set("force_pow", "true")
	t.Cleanup(func() {
		_ = beego.AppConfig.Set("pow_bits", oldBits)
		_ = beego.AppConfig.Set("force_pow", oldForce)
	})
	nonce := "pow-test"
	now := time.Now()
	if !powChallenges.store(nonce, now.Add(time.Minute), now) {
		t.Fatal("could not store test challenge")
	}
	solution := ""
	for i := 0; i < 100; i++ {
		candidate := string(rune('0' + i))
		digest := sha256.Sum256([]byte(nonce + "\x00" + candidate))
		if digest[0]&0x80 == 0 {
			solution = candidate
			break
		}
	}
	if solution == "" {
		t.Fatal("could not find a one-bit PoW test solution")
	}
	if !verifyLoginPoW("alice", nonce, solution) {
		t.Fatal("valid PoW solution was rejected")
	}
	if verifyLoginPoW("alice", nonce, solution) {
		t.Fatal("PoW challenge was reusable")
	}
}

func TestPoWChallengeStoreIsBoundedAndReclaimsExpiredEntries(t *testing.T) {
	store := newPoWChallengeStore(2)
	base := time.Unix(1_700_000_000, 0)
	for _, nonce := range []string{"one", "two"} {
		if !store.store(nonce, base.Add(time.Minute), base) {
			t.Fatalf("could not store challenge %q", nonce)
		}
	}
	if store.store("three", base.Add(time.Minute), base) {
		t.Fatal("challenge store exceeded its configured capacity")
	}
	if got := store.len(); got != 2 {
		t.Fatalf("challenge count = %d, want capacity 2", got)
	}

	if !store.store("three", base.Add(2*time.Minute), base.Add(time.Minute)) {
		t.Fatal("expired challenges should be reclaimed before rejecting a new one")
	}
	if got := store.len(); got != 1 {
		t.Fatalf("challenge count after expiry cleanup = %d, want 1", got)
	}
	if !store.consume("three", base.Add(time.Minute+time.Second)) {
		t.Fatal("fresh challenge was not consumable")
	}
	if store.consume("three", base.Add(time.Minute+time.Second)) {
		t.Fatal("challenge was reusable")
	}
}

func TestPoWChallengeStoreConcurrentCapacity(t *testing.T) {
	const capacity = 64
	store := newPoWChallengeStore(capacity)
	now := time.Now()
	var accepted atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for index := 0; index < capacity*2; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			if store.store(fmt.Sprintf("challenge-%d", index), now.Add(time.Minute), now) {
				accepted.Add(1)
			}
		}(index)
	}
	close(start)
	wg.Wait()
	if got := int(accepted.Load()); got != capacity {
		t.Fatalf("accepted challenges = %d, want %d", got, capacity)
	}
	if got := store.len(); got != capacity {
		t.Fatalf("stored challenges = %d, want %d", got, capacity)
	}
}

func TestReserveLoginAttemptDoesNotCountImplicitLoginPageChecks(t *testing.T) {
	limiter := newLoginAttemptLimiter(8)
	if !allowLoginAttemptWith(limiter, "implicit-login-page", "nobody", false, time.Now()) {
		t.Fatal("implicit login page checks should remain allowed")
	}
	if ipCount, userCount := limiter.lengths(); ipCount != 0 || userCount != 0 {
		t.Fatal("implicit login page checks must not create a failure record")
	}
}

func TestReserveLoginAttemptLimitsConcurrentFailures(t *testing.T) {
	const ip = "concurrent-login-attempts"
	limiter := newLoginAttemptLimiter(32)

	var allowed int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < maxLoginFailures*4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if limiter.reserve(ip, "alice", time.Now()) {
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
	limiter := newLoginAttemptLimiter(8)

	now := time.Unix(1_700_000_000, 0)
	for i := 0; i < maxLoginFailures; i++ {
		if !limiter.reserve(ip, "alice", now) {
			t.Fatalf("attempt %d unexpectedly rejected", i+1)
		}
	}
	if limiter.reserve(ip, "alice", now) {
		t.Fatal("attempt after the failure limit should be rejected")
	}
	if !limiter.reserve(ip, "alice", now.Add(loginFailureWindow)) {
		t.Fatal("attempt after the failure window should be admitted")
	}
}

func TestLoginAttemptLimiterAppliesIPAndUsernameDimensions(t *testing.T) {
	limiter := newLoginAttemptLimiter(32)
	base := time.Unix(1_700_000_000, 0)
	for i := 0; i < maxLoginFailures; i++ {
		if !limiter.reserve("192.0.2.1", "Alice", base.Add(time.Duration(i)*time.Millisecond)) {
			t.Fatalf("attempt %d was unexpectedly rejected", i+1)
		}
	}
	if limiter.reserve("192.0.2.2", "ALICE", base.Add(time.Second)) {
		t.Fatal("username limit should apply across source IPs and case variants")
	}
	if !limiter.reserve("192.0.2.2", "Bob", base.Add(time.Second)) {
		t.Fatal("a different username and IP should have an independent allowance")
	}
	limiter.clear("192.0.2.1", "Alice")
	if !limiter.reserve("192.0.2.2", "alice", base.Add(2*time.Second)) {
		t.Fatal("successful authentication should clear the IP and username reservations")
	}
}

func TestLoginAttemptLimiterBoundsStateAndReclaimsExpiredEntries(t *testing.T) {
	limiter := newLoginAttemptLimiter(2)
	base := time.Unix(1_700_000_000, 0)
	if !limiter.reserve("192.0.2.1", "one", base) || !limiter.reserve("192.0.2.2", "two", base) {
		t.Fatal("initial reservations were rejected")
	}
	if limiter.reserve("192.0.2.3", "three", base) {
		t.Fatal("limiter exceeded its configured capacity")
	}
	if ipCount, userCount := limiter.lengths(); ipCount != 2 || userCount != 2 {
		t.Fatalf("store lengths = (%d,%d), want (2,2)", ipCount, userCount)
	}
	if !limiter.reserve("192.0.2.3", "three", base.Add(loginFailureWindow)) {
		t.Fatal("expired entries should be reclaimed before a new attempt")
	}
	if ipCount, userCount := limiter.lengths(); ipCount > 2 || userCount > 2 {
		t.Fatalf("store exceeded capacity after reclamation: (%d,%d)", ipCount, userCount)
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

func TestCaptchaFilterPathFollowsWebBaseURL(t *testing.T) {
	tests := []struct {
		name string
		base string
		want string
	}{
		{name: "root", base: "", want: "/captcha/"},
		{name: "subpath", base: "/nps", want: "/nps/captcha/"},
		{name: "trailing slash", base: "/nps/", want: "/nps/captcha/"},
		{name: "missing leading slash", base: "nps", want: "/nps/captcha/"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := captchaFilterPath(tt.base); got != tt.want {
				t.Fatalf("captchaFilterPath(%q) = %q, want %q", tt.base, got, tt.want)
			}
		})
	}
}
