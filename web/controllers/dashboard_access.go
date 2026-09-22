package controllers

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"ehang.io/nps/lib/file"
	"github.com/astaxie/beego"
)

const dashboardAccessKeyPrefix = "npsd_"

var dashboardAccessKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

type dashboardPrincipal struct {
	Admin    bool
	UserID   int
	ClientID int
}

// dashboardAccessKey is intentionally separate from the management API token:
// it can read topology data only and can never authorize a mutation.
func generateDashboardAccessKey(custom string) (string, string, error) {
	custom = strings.TrimSpace(custom)
	if custom != "" {
		custom = strings.TrimPrefix(custom, dashboardAccessKeyPrefix)
		if !dashboardAccessKeyPattern.MatchString(custom) {
			return "", "", errors.New("大屏密钥只能使用 16-128 位字母、数字、下划线或连字符")
		}
		key := dashboardAccessKeyPrefix + custom
		return key, hashDashboardAccessKey(key), nil
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	key := dashboardAccessKeyPrefix + base64.RawURLEncoding.EncodeToString(raw)
	return key, hashDashboardAccessKey(key), nil
}

func hashDashboardAccessKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func validDashboardAccessKey(storedHash, key string) bool {
	key = strings.TrimSpace(key)
	if strings.TrimSpace(storedHash) == "" || !strings.HasPrefix(key, dashboardAccessKeyPrefix) {
		return false
	}
	want := hashDashboardAccessKey(key)
	return subtle.ConstantTimeCompare([]byte(storedHash), []byte(want)) == 1
}

func lookupDashboardAccessKey(key string) (dashboardPrincipal, bool) {
	key = strings.TrimSpace(key)
	if key == "" {
		return dashboardPrincipal{}, false
	}
	if global := file.GetDb().GetGlobal(); global != nil {
		global.RLock()
		adminHash := global.DashboardKeyHash
		global.RUnlock()
		if validDashboardAccessKey(adminHash, key) {
			return dashboardPrincipal{Admin: true}, true
		}
	}
	var principal dashboardPrincipal
	file.GetDb().JsonDb.Users.Range(func(_, value interface{}) bool {
		user, ok := value.(*file.User)
		if !ok || user == nil {
			return true
		}
		user.RLock()
		id, status, hash := user.Id, user.Status, user.DashboardKeyHash
		user.RUnlock()
		if status && file.GetDb().IsUserActive(id) && validDashboardAccessKey(hash, key) {
			principal = dashboardPrincipal{UserID: id}
			return false
		}
		return true
	})
	return principal, principal.Admin || principal.UserID > 0
}

func dashboardAccessKeyFromRequest(request *http.Request) string {
	if request == nil {
		return ""
	}
	authorization := strings.TrimSpace(request.Header.Get("Authorization"))
	if len(authorization) >= len("Bearer ") && strings.EqualFold(authorization[:len("Bearer ")], "Bearer ") {
		candidate := strings.TrimSpace(authorization[len("Bearer "):])
		if strings.HasPrefix(candidate, dashboardAccessKeyPrefix) {
			return candidate
		}
	}
	return ""
}

func dashboardAccessURL(key string) string {
	base := strings.TrimRight(beego.AppConfig.String("web_base_url"), "/")
	return base + "/overview#access_key=" + key
}

type DashboardAccessController struct {
	BaseController
}

func (s *DashboardAccessController) currentOwner() (bool, int, error) {
	if s.IsAdmin() {
		return true, 0, nil
	}
	userID, ok := s.currentUserPrincipalID()
	if !ok {
		return false, 0, errors.New("只有用户账号可以管理大屏密钥")
	}
	return false, userID, nil
}

func (s *DashboardAccessController) Status() {
	isAdmin, userID, err := s.currentOwner()
	if err != nil {
		s.AjaxErr(err.Error())
		return
	}
	enabled := false
	if isAdmin {
		if global := file.GetDb().GetGlobal(); global != nil {
			global.RLock()
			enabled = strings.TrimSpace(global.DashboardKeyHash) != ""
			global.RUnlock()
		}
	} else if user, getErr := file.GetDb().GetUser(userID); getErr == nil && user != nil {
		user.RLock()
		enabled = strings.TrimSpace(user.DashboardKeyHash) != ""
		user.RUnlock()
	}
	s.Data["json"] = map[string]interface{}{"status": 1, "enabled": enabled, "masked": maskedDashboardAccessKey(enabled)}
	s.ServeJSON()
	s.StopRun()
}

func maskedDashboardAccessKey(enabled bool) string {
	if !enabled {
		return ""
	}
	return dashboardAccessKeyPrefix + "••••••••"
}

func (s *DashboardAccessController) Generate() {
	if !s.RequirePost() {
		return
	}
	isAdmin, userID, err := s.currentOwner()
	if err != nil {
		s.AjaxErr(err.Error())
		return
	}
	key, hash, err := generateDashboardAccessKey(s.GetString("key"))
	if err != nil {
		s.AjaxErr(err.Error())
		return
	}
	if isAdmin {
		if err := file.GetDb().SetGlobalDashboardKeyHash(hash); err != nil {
			s.AjaxErr("大屏密钥保存失败")
			return
		}
		s.auditMutation("dashboard.key_rotate", "dashboard", 0, 0, "", nil, nil)
	} else {
		user, getErr := file.GetDb().GetUser(userID)
		if getErr != nil || user == nil {
			s.AjaxErr("用户不存在")
			return
		}
		user.Lock()
		user.DashboardKeyHash = hash
		user.Unlock()
		file.GetDb().JsonDb.StoreUsersToJsonFile()
		s.auditMutation("dashboard.key_rotate", "dashboard", userID, userID, "", nil, nil)
	}
	s.Data["json"] = map[string]interface{}{
		"status": 1,
		"msg":    "大屏密钥已生成，请立即保存",
		"key":    key,
		"url":    dashboardAccessURL(key),
	}
	s.ServeJSON()
	s.StopRun()
}

func (s *DashboardAccessController) Revoke() {
	if !s.RequirePost() {
		return
	}
	isAdmin, userID, err := s.currentOwner()
	if err != nil {
		s.AjaxErr(err.Error())
		return
	}
	if isAdmin {
		if err := file.GetDb().SetGlobalDashboardKeyHash(""); err != nil {
			s.AjaxErr("大屏密钥撤销失败")
			return
		}
		s.auditMutation("dashboard.key_revoke", "dashboard", 0, 0, "", nil, nil)
	} else {
		user, getErr := file.GetDb().GetUser(userID)
		if getErr != nil || user == nil {
			s.AjaxErr("用户不存在")
			return
		}
		user.Lock()
		user.DashboardKeyHash = ""
		user.Unlock()
		file.GetDb().JsonDb.StoreUsersToJsonFile()
		s.auditMutation("dashboard.key_revoke", "dashboard", userID, userID, "", nil, nil)
	}
	s.AjaxOk("大屏密钥已撤销")
}
