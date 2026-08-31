package controllers

import (
	"encoding/hex"
	"github.com/astaxie/beego/logs"
	"html"
	"strings"
	"time"

	"ehang.io/nps/lib/file"

	"ehang.io/nps/lib/crypt"
	"github.com/astaxie/beego"
)

type AuthController struct {
	beego.Controller
}

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
		expected := s.Ctx.Input.Scheme() + "://" + s.Ctx.Input.Host()
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

	vkey := s.getEscapeString("vkey")
	ip := s.getEscapeString("ip")
	password := s.getEscapeString("pass")

	if vkey == "" || password == "" {
		s.Data["json"] = map[string]interface{}{"success": false, "message": "参数错误"}
		s.ServeJSON()
		return
	}

	// 如果未提供 ip，则使用请求中的客户端 IP（支持代理头）
	if ip == "" {
		ip = s.Ctx.Input.IP()
		ip = html.EscapeString(ip)
	}

	c, err := file.GetDb().GetClientByVkey(vkey)
	if err != nil {
		s.Data["json"] = map[string]interface{}{"success": false, "message": "客户端密钥错误"}
		s.ServeJSON()
		logs.Error("客户端IP白名单认证失败,客户端密钥错误:ip [%s]", ip)
		return
	}

	if c.IpWhitePass != password {
		s.Data["json"] = map[string]interface{}{"success": false, "message": "授权密码错误"}
		s.ServeJSON()
		logs.Error("客户端IP白名单认证失败,授权密码错误:client_id [%d] ip [%s]", c.Id, ip)
		return
	}

	ipExists := false
	for _, existingIp := range c.IpWhiteList {
		if existingIp == ip {
			ipExists = true
			break
		}
	}

	if !ipExists {
		c.IpWhiteList = append(c.IpWhiteList, ip)
		file.GetDb().UpdateClient(c)
	}

	s.Data["json"] = map[string]interface{}{"success": true, "message": "授权成功"}
	s.ServeJSON()

	logs.Info("客户端IP白名单认证授权成功:client_id [%d] ip [%s]", c.Id, ip)

}

func (s *AuthController) getEscapeString(key string) string {
	return html.EscapeString(s.GetString(key))
}
