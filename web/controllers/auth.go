package controllers

import (
	"encoding/hex"
	"net/http"
	"time"

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
	if s.Ctx != nil && s.Ctx.ResponseWriter != nil {
		s.Ctx.ResponseWriter.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
		s.Ctx.ResponseWriter.Header().Set("Pragma", "no-cache")
		s.Ctx.ResponseWriter.Header().Set("Expires", "0")
	}
	m := make(map[string]interface{})
	m["time"] = time.Now().Unix()
	s.Data["json"] = m
	s.ServeJSON()
}

// Challenge returns a fresh, stateless, signed login challenge. The login,
// registration and password-recovery pages fetch it over an uncacheable
// endpoint so the anti-replay nonce never has to be embedded in cacheable HTML.
func (s *AuthController) Challenge() {
	setNoStore(s.Ctx)
	challenge, err := crypt.IssueLoginChallenge(time.Now())
	if err != nil {
		s.Ctx.Output.SetStatus(http.StatusServiceUnavailable)
		s.Data["json"] = authJSONError(
			"challenge_unavailable", "登录安全挑战生成失败，请稍后重试。", "Unable to create a sign-in challenge. Try again later.",
		)
		s.ServeJSON()
		return
	}
	s.Data["json"] = map[string]interface{}{
		"status":    1,
		"nonce":     challenge.Nonce,
		"expires":   challenge.Expires,
		"signature": challenge.Signature,
	}
	s.ServeJSON()
}
