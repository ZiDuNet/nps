package controllers

import (
	"crypto/subtle"
	"errors"
	"strings"

	"ehang.io/nps/lib/file"
	"github.com/astaxie/beego"
)

// Api renders the standalone API-access page. Regular users manage their
// personal token; administrators additionally manage the configuration-file
// key and a web-managed key that is equivalent to it.
func (s *UserController) Api() {
	s.Data["menu"] = "api"
	configKey := strings.TrimSpace(beego.AppConfig.String("auth_key"))
	s.Data["configAPIKeyConfigured"] = configKey != ""
	s.Data["configAPIKeyMasked"] = maskAPIKey(configKey)
	s.SetInfo("api")
	s.display("user/api")
}

// maskAPIKey keeps a credential recognizable without revealing it. Values
// shorter than eight characters are fully masked because the first-four and
// last-three rule would leak most of the secret.
func maskAPIKey(value string) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= 8 {
		return "••••••••"
	}
	return string(runes[:4]) + "••••••" + string(runes[len(runes)-3:])
}

// ConfigAPIKeyView verifies the administrator's login password before
// returning the full auth_key from nps.conf. The value is never rendered on
// the page itself.
func (s *UserController) ConfigAPIKeyView() {
	if !s.RequireAdmin() || !s.RequirePost() {
		return
	}
	password := s.GetString("password")
	expected := beego.AppConfig.String("web_password")
	if password == "" || subtle.ConstantTimeCompare([]byte(password), []byte(expected)) != 1 {
		s.AjaxErr("登录密码验证失败")
		return
	}
	configKey := strings.TrimSpace(beego.AppConfig.String("auth_key"))
	s.Data["json"] = map[string]interface{}{"status": 1, "msg": "验证通过", "key": configKey}
	s.ServeJSON()
	s.StopRun()
}

// AdminAPIKeyStatus reports whether the web-managed administrator key exists.
// The raw value is returned only to an already-authorized administrator so the
// credential can be viewed again after generation.
func (s *UserController) AdminAPIKeyStatus() {
	if !s.RequireAdmin() {
		return
	}
	enabled, raw := adminAPIKeySnapshot()
	response := map[string]interface{}{
		"status":  1,
		"enabled": enabled,
		"masked":  maskedAdminAPIToken(enabled),
	}
	if enabled && raw != "" {
		response["token"] = raw
	}
	s.Data["json"] = response
	s.ServeJSON()
	s.StopRun()
}

// AdminAPIKeyGenerate rotates the web-managed administrator API credential.
// The raw token is returned exactly once and is equivalent to auth_key for the
// signed timestamp protocol.
func (s *UserController) AdminAPIKeyGenerate() {
	if !s.RequireAdmin() || !s.RequirePost() {
		return
	}
	token, tokenHash, err := generateAdminAPIToken()
	if err != nil {
		s.AjaxErr("管理员 API 密钥生成失败")
		return
	}
	ciphertext := ""
	if encrypted, encryptErr := encryptAdminAPIToken(token); encryptErr == nil {
		ciphertext = encrypted
	} else if !errors.Is(encryptErr, errUserAPITokenEncryptionKeyMissing) {
		s.AjaxErr("管理员 API 密钥加密保存失败，请检查 auth_key 或 auth_crypt_key 配置")
		return
	}
	if err := updateAdminAPIKeyFields(tokenHash, ciphertext); err != nil {
		s.AjaxErr(err.Error())
		return
	}
	s.auditMutation("admin.api_key_rotate", "global", 0, 0, "", nil, map[string]string{"source": "web"})
	s.Data["json"] = map[string]interface{}{"status": 1, "msg": "管理员 API 密钥已生成，可随时在 API 接入页面再次查看", "token": token}
	s.ServeJSON()
	s.StopRun()
}

// AdminAPIKeyRevoke immediately disables the web-managed administrator key.
// The auth_key from nps.conf is not affected.
func (s *UserController) AdminAPIKeyRevoke() {
	if !s.RequireAdmin() || !s.RequirePost() {
		return
	}
	if err := updateAdminAPIKeyFields("", ""); err != nil {
		s.AjaxErr(err.Error())
		return
	}
	s.auditMutation("admin.api_key_revoke", "global", 0, 0, "", nil, map[string]string{"source": "web"})
	s.AjaxOk("管理员 API 密钥已撤销")
}

// updateAdminAPIKeyFields persists the managed administrator key while keeping
// every other global setting untouched. SaveGlobal re-runs the same
// platform-domain validation as a full settings save.
func updateAdminAPIKeyFields(hash, ciphertext string) error {
	global := file.GetDb().GetGlobal()
	if global == nil {
		return errors.New("全局参数不存在，无法保存 API 密钥")
	}
	global.AdminAPIKeyHash = hash
	global.AdminAPIKeyCiphertext = ciphertext
	if err := file.GetDb().SaveGlobal(global); err != nil {
		return err
	}
	return nil
}
