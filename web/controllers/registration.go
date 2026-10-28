package controllers

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/astaxie/beego"
)

const (
	defaultRegistrationWindowSeconds = 60
	defaultRegistrationRateLimit     = 5
	defaultRegistrationIPLimit       = 3
	defaultRegistrationExpireDays    = 7
	defaultEmailCodeTTLSeconds       = 600
	defaultEmailCodeCooldownSeconds  = 60
	maxRegistrationRateRecords       = 4096
	maxRegistrationEmailCodes        = 1024
	maxRegistrationEmailAttempts     = 5
	registrationCleanupPeriod        = 15 * time.Second
)

type registrationPolicy struct {
	window   time.Duration
	attempts int
	accounts int
}

func configuredRegistrationPolicy() registrationPolicy {
	windowSeconds := beego.AppConfig.DefaultInt("register_rate_window", defaultRegistrationWindowSeconds)
	if windowSeconds <= 0 {
		windowSeconds = defaultRegistrationWindowSeconds
	}
	return registrationPolicy{
		window:   time.Duration(windowSeconds) * time.Second,
		attempts: max(beego.AppConfig.DefaultInt("register_rate_limit", defaultRegistrationRateLimit), 0),
		accounts: max(beego.AppConfig.DefaultInt("register_ip_rate_limit", defaultRegistrationIPLimit), 0),
	}
}

type registrationCounter struct {
	count       int
	windowStart time.Time
	lastAttempt time.Time
}

type registrationRateLimiter struct {
	mu                sync.Mutex
	attemptsByIP      map[string]*registrationCounter
	attemptsByAccount map[string]*registrationCounter
	successByIP       map[string]*registrationCounter
	capacity          int
	nextCleanup       time.Time
}

func newRegistrationRateLimiter(capacity int) *registrationRateLimiter {
	if capacity < 1 {
		capacity = 1
	}
	return &registrationRateLimiter{
		attemptsByIP:      make(map[string]*registrationCounter),
		attemptsByAccount: make(map[string]*registrationCounter),
		successByIP:       make(map[string]*registrationCounter),
		capacity:          capacity,
	}
}

func (s *registrationRateLimiter) cleanupLocked(now time.Time, policy registrationPolicy) {
	if now.Before(s.nextCleanup) {
		return
	}
	s.removeExpiredLocked(now, policy)
	s.nextCleanup = now.Add(registrationCleanupPeriod)
}

func (s *registrationRateLimiter) removeExpiredLocked(now time.Time, policy registrationPolicy) {
	remove := func(records map[string]*registrationCounter) {
		for key, record := range records {
			if record == nil || !now.Before(record.windowStart.Add(policy.window)) {
				delete(records, key)
			}
		}
	}
	remove(s.attemptsByIP)
	remove(s.attemptsByAccount)
	remove(s.successByIP)
}

func (s *registrationRateLimiter) counterForLocked(records map[string]*registrationCounter, key string, now time.Time, policy registrationPolicy) (*registrationCounter, bool) {
	record, exists := records[key]
	if exists && record != nil && now.Before(record.windowStart.Add(policy.window)) {
		return record, true
	}
	if exists {
		delete(records, key)
	}
	if len(records) >= s.capacity {
		s.removeExpiredLocked(now, policy)
		if len(records) >= s.capacity {
			return nil, false
		}
	}
	record = &registrationCounter{windowStart: now}
	records[key] = record
	return record, true
}

// reserveAttempt limits repeated registration submissions by both source IP
// and requested username. A zero configured attempts value disables this
// dimension while the success-account limiter can still remain active.
func (s *registrationRateLimiter) reserveAttempt(ip, username string, now time.Time, policy registrationPolicy) bool {
	if policy.attempts <= 0 {
		return true
	}
	ip = strings.TrimSpace(ip)
	if ip == "" {
		ip = "unknown"
	}
	account := loginUsernameKey(username)
	if strings.TrimSpace(username) == "" {
		account = "unknown"
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(now, policy)
	ipRecord, ok := s.counterForLocked(s.attemptsByIP, ip, now, policy)
	if !ok {
		return false
	}
	accountRecord, ok := s.counterForLocked(s.attemptsByAccount, account, now, policy)
	if !ok {
		return false
	}
	if ipRecord.count >= policy.attempts || accountRecord.count >= policy.attempts {
		return false
	}
	ipRecord.count++
	ipRecord.lastAttempt = now
	accountRecord.count++
	accountRecord.lastAttempt = now
	return true
}

// reserveSuccess atomically reserves one successful account slot for an IP.
// The caller must release it if user persistence fails.
func (s *registrationRateLimiter) reserveSuccess(ip string, now time.Time, policy registrationPolicy) bool {
	if policy.accounts <= 0 {
		return true
	}
	ip = strings.TrimSpace(ip)
	if ip == "" {
		ip = "unknown"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(now, policy)
	record, ok := s.counterForLocked(s.successByIP, ip, now, policy)
	if !ok || record.count >= policy.accounts {
		return false
	}
	record.count++
	record.lastAttempt = now
	return true
}

func (s *registrationRateLimiter) releaseSuccess(ip string) {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		ip = "unknown"
	}
	s.mu.Lock()
	if record := s.successByIP[ip]; record != nil {
		record.count--
		if record.count <= 0 {
			delete(s.successByIP, ip)
		}
	}
	s.mu.Unlock()
}

var registrationAttempts = newRegistrationRateLimiter(maxRegistrationRateRecords)

type registrationEmailCodeRecord struct {
	digest    [sha256.Size]byte
	expiresAt time.Time
	attempts  int
}

type registrationEmailCodeStore struct {
	mu              sync.Mutex
	codes           map[string]*registrationEmailCodeRecord
	lastSentByEmail map[string]time.Time
	lastSentByIP    map[string]time.Time
	capacity        int
}

func newRegistrationEmailCodeStore(capacity int) *registrationEmailCodeStore {
	if capacity < 1 {
		capacity = 1
	}
	return &registrationEmailCodeStore{
		codes:           make(map[string]*registrationEmailCodeRecord),
		lastSentByEmail: make(map[string]time.Time),
		lastSentByIP:    make(map[string]time.Time),
		capacity:        capacity,
	}
}

func (s *registrationEmailCodeStore) cleanupLocked(now time.Time, cooldown time.Duration) {
	for key, record := range s.codes {
		if record == nil || !now.Before(record.expiresAt) {
			delete(s.codes, key)
		}
	}
	for key, sentAt := range s.lastSentByEmail {
		if !now.Before(sentAt.Add(cooldown)) {
			delete(s.lastSentByEmail, key)
		}
	}
	for key, sentAt := range s.lastSentByIP {
		if !now.Before(sentAt.Add(cooldown)) {
			delete(s.lastSentByIP, key)
		}
	}
}

func (s *registrationEmailCodeStore) issue(emailAddress, ip string, now time.Time, ttl, cooldown time.Duration) (string, time.Duration, error) {
	emailAddress = strings.ToLower(strings.TrimSpace(emailAddress))
	ip = strings.TrimSpace(ip)
	if ip == "" {
		ip = "unknown"
	}
	if emailAddress == "" {
		return "", 0, errors.New("邮箱不能为空")
	}
	code, err := newRegistrationEmailCode()
	if err != nil {
		return "", 0, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(now, cooldown)
	retryAfter := time.Duration(0)
	if cooldown > 0 {
		if sentAt, exists := s.lastSentByEmail[emailAddress]; exists && now.Before(sentAt.Add(cooldown)) {
			retryAfter = sentAt.Add(cooldown).Sub(now)
		}
		if sentAt, exists := s.lastSentByIP[ip]; exists && now.Before(sentAt.Add(cooldown)) {
			ipRetry := sentAt.Add(cooldown).Sub(now)
			if ipRetry > retryAfter {
				retryAfter = ipRetry
			}
		}
		if retryAfter > 0 {
			return "", retryAfter, fmt.Errorf("验证码发送过于频繁，请在 %d 秒后重试", int((retryAfter+time.Second-1)/time.Second))
		}
	}
	if len(s.codes) >= s.capacity {
		return "", 0, errors.New("验证码请求过多，请稍后重试")
	}
	s.codes[emailAddress] = &registrationEmailCodeRecord{
		digest:    sha256.Sum256([]byte(code)),
		expiresAt: now.Add(ttl),
	}
	s.lastSentByEmail[emailAddress] = now
	s.lastSentByIP[ip] = now
	return code, 0, nil
}

func (s *registrationEmailCodeStore) discard(emailAddress, ip string) {
	emailAddress = strings.ToLower(strings.TrimSpace(emailAddress))
	ip = strings.TrimSpace(ip)
	if ip == "" {
		ip = "unknown"
	}
	s.mu.Lock()
	delete(s.codes, emailAddress)
	delete(s.lastSentByEmail, emailAddress)
	delete(s.lastSentByIP, ip)
	s.mu.Unlock()
}

func (s *registrationEmailCodeStore) verify(emailAddress, code string, now time.Time) bool {
	emailAddress = strings.ToLower(strings.TrimSpace(emailAddress))
	code = strings.TrimSpace(code)
	s.mu.Lock()
	defer s.mu.Unlock()
	record := s.codes[emailAddress]
	if record == nil || !now.Before(record.expiresAt) {
		delete(s.codes, emailAddress)
		return false
	}
	record.attempts++
	digest := sha256.Sum256([]byte(code))
	if code != "" && subtle.ConstantTimeCompare(record.digest[:], digest[:]) == 1 {
		delete(s.codes, emailAddress)
		return true
	}
	if record.attempts >= maxRegistrationEmailAttempts {
		delete(s.codes, emailAddress)
	}
	return false
}

func newRegistrationEmailCode() (string, error) {
	value, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", errors.New("验证码生成失败，请稍后重试")
	}
	return fmt.Sprintf("%06d", value.Int64()), nil
}

var registrationEmailCodes = newRegistrationEmailCodeStore(maxRegistrationEmailCodes)

func registrationCaptchaEnabled() bool {
	return beego.AppConfig.DefaultBool("register_captcha", true)
}

func registrationEmailVerifyEnabled() bool {
	return beego.AppConfig.DefaultBool("register_email_verify", false)
}

func registrationEmailCodeTTL() time.Duration {
	seconds := beego.AppConfig.DefaultInt("register_email_code_ttl", defaultEmailCodeTTLSeconds)
	if seconds < 60 {
		seconds = defaultEmailCodeTTLSeconds
	}
	if seconds > 86400 {
		seconds = 86400
	}
	return time.Duration(seconds) * time.Second
}

func registrationEmailCodeCooldown() time.Duration {
	seconds := max(beego.AppConfig.DefaultInt("register_email_code_cooldown", defaultEmailCodeCooldownSeconds), 0)
	if seconds > 86400 {
		seconds = 86400
	}
	return time.Duration(seconds) * time.Second
}

func registrationMaxClients() int {
	return max(beego.AppConfig.DefaultInt("register_max_clients", 0), 0)
}

func registrationMaxTunnels() int {
	return max(beego.AppConfig.DefaultInt("register_max_tunnels", 0), 0)
}

func registrationExpireDays() int {
	return max(beego.AppConfig.DefaultInt("register_expire_days", defaultRegistrationExpireDays), 0)
}

func registrationExpireTime(now time.Time, days int) string {
	if days <= 0 {
		return ""
	}
	return now.AddDate(0, 0, days).Format("2006-01-02 15:04:05")
}

type registrationSMTPConfig struct {
	Host     string
	Port     int
	Security string
	Username string
	Password string
	From     string
}

func configuredRegistrationSMTP() (registrationSMTPConfig, error) {
	config := registrationSMTPConfig{
		Host:     strings.TrimSpace(beego.AppConfig.String("smtp_host")),
		Port:     beego.AppConfig.DefaultInt("smtp_port", 587),
		Security: strings.ToLower(strings.TrimSpace(beego.AppConfig.String("smtp_security"))),
		Username: strings.TrimSpace(beego.AppConfig.String("smtp_username")),
		Password: beego.AppConfig.String("smtp_password"),
		From:     strings.TrimSpace(beego.AppConfig.String("smtp_from")),
	}
	if config.Host == "" {
		return config, errors.New("SMTP 主机未配置")
	}
	if config.Port <= 0 || config.Port > 65535 {
		return config, errors.New("SMTP 端口无效")
	}
	if config.Security == "" {
		config.Security = "starttls"
	}
	switch config.Security {
	case "starttls", "ssl", "none":
	default:
		return config, errors.New("SMTP 加密方式无效")
	}
	from, err := mail.ParseAddress(config.From)
	if err != nil || from == nil || from.Address == "" {
		return config, errors.New("SMTP 发件人邮箱无效")
	}
	config.From = from.Address
	if config.Username != "" && config.Password == "" {
		return config, errors.New("SMTP 用户名已配置，但密码为空")
	}
	return config, nil
}

type plainSMTPAuth struct {
	username string
	password string
}

func (a *plainSMTPAuth) Start(*smtp.ServerInfo) (string, []byte, error) {
	return "PLAIN", []byte("\x00" + a.username + "\x00" + a.password), nil
}

func (a *plainSMTPAuth) Next(_ []byte, more bool) ([]byte, error) {
	if more {
		return nil, errors.New("SMTP 认证协议错误")
	}
	return nil, nil
}

func sendRegistrationVerificationEmail(recipient, code string, config registrationSMTPConfig) error {
	recipientAddress, err := mail.ParseAddress(strings.TrimSpace(recipient))
	if err != nil || recipientAddress == nil || recipientAddress.Address == "" {
		return errors.New("收件人邮箱无效")
	}
	client, err := openRegistrationSMTPClient(config)
	if err != nil {
		return err
	}
	defer client.Close()

	if config.Username != "" {
		var auth smtp.Auth
		if config.Security == "none" {
			auth = &plainSMTPAuth{username: config.Username, password: config.Password}
		} else {
			auth = smtp.PlainAuth("", config.Username, config.Password, config.Host)
		}
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("SMTP 认证失败: %w", err)
		}
	}
	if err := client.Mail(config.From); err != nil {
		return fmt.Errorf("SMTP 发件人被拒绝: %w", err)
	}
	if err := client.Rcpt(recipientAddress.Address); err != nil {
		return fmt.Errorf("SMTP 收件人被拒绝: %w", err)
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("SMTP 无法开始发送邮件: %w", err)
	}
	subject := mime.QEncoding.Encode("UTF-8", "NPS 注册验证码")
	body := fmt.Sprintf("您的 NPS 注册验证码是：%s\r\n\r\n验证码 %d 分钟内有效。若非本人操作，请忽略本邮件。\r\n",
		code, int(registrationEmailCodeTTL()/time.Minute))
	message := strings.Join([]string{
		"From: " + config.From,
		"To: " + recipientAddress.Address,
		"Subject: " + subject,
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"Content-Transfer-Encoding: base64",
		"",
		base64.StdEncoding.EncodeToString([]byte(body)),
		"",
	}, "\r\n")
	if _, err := writer.Write([]byte(message)); err != nil {
		_ = writer.Close()
		return fmt.Errorf("写入验证码邮件失败: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("发送验证码邮件失败: %w", err)
	}
	_ = client.Quit()
	return nil
}

func openRegistrationSMTPClient(config registrationSMTPConfig) (*smtp.Client, error) {
	address := net.JoinHostPort(config.Host, strconv.Itoa(config.Port))
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	tlsConfig := &tls.Config{ServerName: config.Host, MinVersion: tls.VersionTLS12}
	if config.Security == "ssl" {
		connection, err := tls.DialWithDialer(dialer, "tcp", address, tlsConfig)
		if err != nil {
			return nil, fmt.Errorf("连接 SMTP TLS 服务器失败: %w", err)
		}
		client, err := smtp.NewClient(connection, config.Host)
		if err != nil {
			_ = connection.Close()
			return nil, fmt.Errorf("初始化 SMTP TLS 客户端失败: %w", err)
		}
		return client, nil
	}

	connection, err := dialer.Dial("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("连接 SMTP 服务器失败: %w", err)
	}
	client, err := smtp.NewClient(connection, config.Host)
	if err != nil {
		_ = connection.Close()
		return nil, fmt.Errorf("初始化 SMTP 客户端失败: %w", err)
	}
	if config.Security == "starttls" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			_ = client.Close()
			return nil, errors.New("SMTP 服务器不支持 STARTTLS；如服务器使用 465 端口，请选择 ssl")
		}
		if err := client.StartTLS(tlsConfig); err != nil {
			_ = client.Close()
			return nil, fmt.Errorf("SMTP STARTTLS 失败: %w", err)
		}
	}
	return client, nil
}
