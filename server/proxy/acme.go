package proxy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/file"
	"github.com/astaxie/beego"
	"github.com/astaxie/beego/logs"
	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
)

const zeroSSLACMEDirectory = "https://acme.zerossl.com/v2/DV90"

// initACME creates the optional certificate manager before listeners are
// created. The manager is intentionally only enabled when the global switch
// is on; a host's AutoSSL flag remains an opt-in when force_auto_ssl is off.
func (s *httpServer) initACME() error {
	if s == nil {
		return nil
	}
	force := beego.AppConfig.DefaultBool("force_auto_ssl", false)
	if !force && s.httpPort != 80 && s.httpsPort != 443 {
		return nil
	}
	email := strings.TrimSpace(beego.AppConfig.String("ssl_email"))
	cachePath := strings.TrimSpace(beego.AppConfig.String("ssl_path"))
	if cachePath == "" {
		cachePath = filepath.Join(common.GetRunPath(), "conf", "ssl")
	} else if !filepath.IsAbs(cachePath) {
		cachePath = filepath.Join(common.GetRunPath(), cachePath)
	}
	if err := os.MkdirAll(cachePath, 0o700); err != nil {
		return err
	}
	manager := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		Email:      email,
		Cache:      autocert.DirCache(cachePath),
		HostPolicy: acmeHostPolicy,
	}
	ca := strings.ToLower(strings.TrimSpace(beego.AppConfig.String("ssl_ca")))
	if ca == "zerossl" || ca == "zero_ssl" || ca == "zero ssl" {
		apiKey := strings.TrimSpace(beego.AppConfig.String("ssl_zerossl_api"))
		if apiKey == "" {
			return errors.New("ssl_ca=ZeroSSL 时必须配置 ssl_zerossl_api")
		}
		eab, err := fetchZeroSSLEAB(apiKey)
		if err != nil {
			return fmt.Errorf("获取 ZeroSSL EAB 凭据失败: %w", err)
		}
		manager.Client = &acme.Client{DirectoryURL: zeroSSLACMEDirectory, UserAgent: "nps"}
		manager.ExternalAccountBinding = eab
		logs.Info("[证书] 使用 ZeroSSL ACME 目录")
	}
	s.acmeManager = manager
	logs.Info("[证书] ACME 自动证书已启用，缓存目录 %s", cachePath)
	return nil
}

// fetchZeroSSLEAB exchanges the ZeroSSL API key for the ACME external account
// binding required by the ZeroSSL directory. The API key itself is never sent
// to the ACME endpoint or written to logs/cache.
func fetchZeroSSLEAB(apiKey string) (*acme.ExternalAccountBinding, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	endpoint := "https://api.zerossl.com/acme/eab-credentials?access_key=" + url.QueryEscape(apiKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Success bool `json:"success"`
		Result  struct {
			ID      string `json:"id"`
			HMACKey string `json:"hmac_key"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	if !payload.Success || strings.TrimSpace(payload.Result.ID) == "" || strings.TrimSpace(payload.Result.HMACKey) == "" {
		return nil, errors.New("ZeroSSL 返回的 EAB 凭据不完整")
	}
	keyText := strings.TrimSpace(payload.Result.HMACKey)
	key, err := base64.RawURLEncoding.DecodeString(keyText)
	if err != nil {
		key, err = base64.StdEncoding.DecodeString(keyText)
	}
	if err != nil || len(key) == 0 {
		return nil, errors.New("ZeroSSL EAB HMAC 密钥不是有效的 Base64")
	}
	return &acme.ExternalAccountBinding{KID: strings.TrimSpace(payload.Result.ID), Key: key}, nil
}

func acmeHostPolicy(_ context.Context, host string) error {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "" || strings.Contains(host, ":") {
		return errors.New("ACME 只允许使用域名，不接受 IP 或带端口主机名")
	}
	r := &http.Request{Host: host, RequestURI: "*", URL: &url.URL{Scheme: "https", Path: "/"}}
	entry, err := file.GetDb().GetInfoByHost(host, r)
	if err != nil || entry == nil {
		return errors.New("域名未配置 Host 规则")
	}
	entry.RLock()
	closed, autoSSL := entry.IsClose, entry.AutoSSL
	justProxy, tlsOffload := entry.HTTPSJustProxy, entry.TLSOffload
	entry.RUnlock()
	if closed || !autoSSL || justProxy || tlsOffload {
		return errors.New("当前 Host 规则不支持 ACME 终止 TLS")
	}
	force := beego.AppConfig.DefaultBool("force_auto_ssl", false)
	httpPort := beego.AppConfig.DefaultInt("http_proxy_port", 80)
	httpsPort := beego.AppConfig.DefaultInt("https_proxy_port", 443)
	if !force && httpPort != 80 && httpsPort != 443 {
		return errors.New("非标准 HTTP/HTTPS 端口需要启用 force_auto_ssl")
	}
	return nil
}

func (s *httpServer) autoSSLHostEnabled(host *file.Host) bool {
	if s == nil || host == nil || s.acmeManager == nil {
		return false
	}
	host.RLock()
	enabled, closed := host.AutoSSL, host.IsClose
	justProxy, tlsOffload := host.HTTPSJustProxy, host.TLSOffload
	host.RUnlock()
	if !enabled || closed || justProxy || tlsOffload {
		return false
	}
	return s.httpPort == 80 || s.httpsPort == 443 || beego.AppConfig.DefaultBool("force_auto_ssl", false)
}
