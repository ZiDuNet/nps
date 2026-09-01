package controllers

import (
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"github.com/astaxie/beego/logs"
	"html"
	"net"
	"net/http"
	"strings"
	"time"

	"ehang.io/nps/lib/file"

	"ehang.io/nps/lib/crypt"
	"github.com/astaxie/beego"
)

type AuthController struct {
	beego.Controller
}

var errIPWhiteAuthMethod = errors.New("ip whitelist authorization requires POST")

func (s *AuthController) GetAuthKey() {
	m := make(map[string]interface{})
	defer func() {
		s.Data["json"] = m
		s.ServeJSON()
	}()
	if cryptKey := beego.AppConfig.String("auth_crypt_key"); len(cryptKey) != 16 {
		m["status"] = 0
		return
	} else {
		b, err := crypt.AesEncrypt([]byte(beego.AppConfig.String("auth_key")), []byte(cryptKey))
		if err != nil {
			m["status"] = 0
			return
		}
		m["status"] = 1
		m["crypt_auth_key"] = hex.EncodeToString(b)
		m["crypt_type"] = "aes cbc"
		return
	}
}

func (s *AuthController) GetTime() {
	m := make(map[string]interface{})
	m["time"] = time.Now().Unix()
	s.Data["json"] = m
	s.ServeJSON()
}

func (s *AuthController) IpWhiteAuth() {
	// The authorization page is same-origin. Do not grant every site access to
	// this credential-bearing endpoint; echo an Origin only for the current host.
	if origin := strings.TrimSpace(s.Ctx.Input.Header("Origin")); origin != "" {
		// Input.Host intentionally strips the port, while a browser Origin keeps
		// non-default ports. Use Request.Host so the comparison is exact.
		expected := s.Ctx.Input.Scheme() + "://" + s.Ctx.Request.Host
		if origin == expected {
			s.Ctx.ResponseWriter.Header().Set("Access-Control-Allow-Origin", origin)
			s.Ctx.ResponseWriter.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
			s.Ctx.ResponseWriter.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			s.Ctx.ResponseWriter.Header().Add("Vary", "Origin")
		}
	}
	if s.Ctx.Request.Method == "OPTIONS" {
		s.Ctx.ResponseWriter.WriteHeader(204)
		return
	}
	if s.Ctx.Request.Method != http.MethodPost {
		s.respondIPWhiteAuth(http.StatusMethodNotAllowed, false, "method not allowed")
		return
	}

	vkey, ip, password, err := ipWhiteAuthPostForm(s.Ctx.Request)
	if err != nil {
		s.respondIPWhiteAuth(http.StatusBadRequest, false, "参数错误")
		return
	}
	vkey = strings.TrimSpace(vkey)
	ip = strings.TrimSpace(ip)

	if vkey == "" || password == "" {
		s.respondIPWhiteAuth(http.StatusBadRequest, false, "参数错误")
		return
	}

	// If an API caller does not name an address, retain the existing proxy-aware
	// behavior. Explicit values and forwarded values are both normalized before
	// they reach the persisted whitelist.
	if ip == "" {
		ip = strings.TrimSpace(s.Ctx.Input.IP())
	}
	ip = normalizeIPWhiteIP(ip)
	if ip == "" {
		s.respondIPWhiteAuth(http.StatusBadRequest, false, "IP 地址错误")
		return
	}

	c, err := file.GetDb().GetClientByVkey(vkey)
	if err != nil {
		s.respondIPWhiteAuth(http.StatusUnauthorized, false, "客户端密钥错误")
		logs.Error("客户端IP白名单认证失败,客户端密钥错误:ip [%s]", ip)
		return
	}

	if !clientIPWhitePasswordMatches(c, password) {
		s.respondIPWhiteAuth(http.StatusUnauthorized, false, "授权密码错误")
		logs.Error("客户端IP白名单认证失败,授权密码错误:client_id [%d] ip [%s]", c.Id, ip)
		return
	}

	if addClientIPWhiteList(c, ip) {
		file.GetDb().JsonDb.StoreClientsToJsonFile()
	}

	s.respondIPWhiteAuth(http.StatusOK, true, "授权成功")

	logs.Info("客户端IP白名单认证授权成功:client_id [%d] ip [%s]", c.Id, ip)

}

func (s *AuthController) respondIPWhiteAuth(status int, success bool, message string) {
	s.Ctx.Output.SetStatus(status)
	s.Data["json"] = map[string]interface{}{"success": success, "message": message}
	s.ServeJSON()
}

// ipWhiteAuthPostForm deliberately reads Request.PostForm rather than a
// framework helper that also accepts query parameters. Credentials in a URL
// are routinely retained by browser history, reverse proxies, and access logs.
func ipWhiteAuthPostForm(request *http.Request) (vkey, ip, password string, err error) {
	if request.Method != http.MethodPost {
		return "", "", "", errIPWhiteAuthMethod
	}
	if err = request.ParseForm(); err != nil {
		return "", "", "", err
	}
	return request.PostForm.Get("vkey"), request.PostForm.Get("ip"), request.PostForm.Get("pass"), nil
}

func normalizeIPWhiteIP(value string) string {
	ip := net.ParseIP(strings.TrimSpace(value))
	if ip == nil {
		return ""
	}
	return ip.String()
}

func clientIPWhitePasswordMatches(client *file.Client, password string) bool {
	client.RLock()
	storedPassword := client.IpWhitePass
	client.RUnlock()
	// Existing web forms stored this field HTML-escaped. Decode once at the
	// comparison boundary so old and new configurations use their real secret.
	return subtle.ConstantTimeCompare([]byte(html.UnescapeString(storedPassword)), []byte(password)) == 1
}

func addClientIPWhiteList(client *file.Client, ip string) bool {
	client.Lock()
	defer client.Unlock()
	for _, existingIP := range client.IpWhiteList {
		if existingIP == ip {
			return false
		}
	}
	client.IpWhiteList = append(client.IpWhiteList, ip)
	return true
}
