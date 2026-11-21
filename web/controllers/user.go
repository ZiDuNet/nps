package controllers

import (
	"crypto/subtle"
	"errors"
	"html"
	"strconv"
	"strings"
	"time"

	"ehang.io/nps/lib/file"
	"ehang.io/nps/lib/rate"
	"ehang.io/nps/server"
	"github.com/astaxie/beego/logs"
)

type UserController struct {
	BaseController
}

// userListRow deliberately keeps the historical response shape while never
// sending stored credentials to the browser.
type userListRow struct {
	Id             int
	UserName       string
	Password       string
	Email          string
	Status         bool
	Remark         string
	ClientCount    int
	TunnelCount    int
	HostCount      int
	MaxClientNum   *int
	MaxTunnelNum   *int
	MaxHostNum     *int
	MaxConn        *int
	BandwidthLimit *int64
	MaxClientState string
	MaxTunnelState string
	MaxHostState   string
	MaxConnState   string
	BandwidthState string
	BandwidthMbps  string
	Connections    int64
	UploadRate     int64
	DownloadRate   int64
	ExpireTime     string
	CreateTime     string
	APIKeyEnabled  bool
}

func newUserListRows(users []*file.User) []*userListRow {
	return newUserListRowsWithResourceCounts(users, nil)
}

func newUserListRowsWithResourceCounts(users []*file.User, counts map[int]file.UserResourceCounts) []*userListRow {
	rows := make([]*userListRow, 0, len(users))
	for _, user := range users {
		if user == nil {
			continue
		}
		user.RLock()
		id, userName, status := user.Id, user.UserName, user.Status
		email, remark := user.Email, user.Remark
		maxClientNum := file.IntQuotaSnapshot(user.MaxClientNum)
		maxTunnelNum := file.IntQuotaSnapshot(user.MaxTunnelNum)
		maxHostNum := file.IntQuotaSnapshot(user.MaxHostNum)
		maxConn := file.IntQuotaSnapshot(user.MaxConn)
		bandwidthLimit := file.Int64QuotaSnapshot(user.BandwidthLimit)
		expireTime, createTime, apiKeyHash := user.ExpireTime, user.CreateTime, user.APIKeyHash
		user.RUnlock()
		connections, _, uploadRate, downloadRate := file.UserRuntimeSnapshot(id)
		resourceCounts := counts[id]
		bandwidthMbps := ""
		if bandwidthLimit != nil {
			bandwidthMbps = rate.FormatMbps(*bandwidthLimit)
		}
		rows = append(rows, &userListRow{
			Id:             id,
			UserName:       html.UnescapeString(userName),
			Password:       "",
			Email:          html.UnescapeString(email),
			Status:         status,
			Remark:         html.UnescapeString(remark),
			ClientCount:    resourceCounts.ClientCount,
			TunnelCount:    resourceCounts.TunnelCount,
			HostCount:      resourceCounts.HostCount,
			MaxClientNum:   maxClientNum,
			MaxTunnelNum:   maxTunnelNum,
			MaxHostNum:     maxHostNum,
			MaxConn:        maxConn,
			BandwidthLimit: bandwidthLimit,
			MaxClientState: quotaState(maxClientNum),
			MaxTunnelState: quotaState(maxTunnelNum),
			MaxHostState:   quotaState(maxHostNum),
			MaxConnState:   quotaState(maxConn),
			BandwidthState: quotaInt64State(bandwidthLimit),
			BandwidthMbps:  bandwidthMbps,
			Connections:    connections,
			UploadRate:     uploadRate,
			DownloadRate:   downloadRate,
			ExpireTime:     expireTime,
			CreateTime:     createTime,
			APIKeyEnabled:  strings.TrimSpace(apiKeyHash) != "",
		})
	}
	return rows
}

// quotaState exposes the three-state user quota as a stable wire value so the
// dashboard never has to infer "0 means not included" from a numeric field.
func quotaState(value *int) string {
	switch {
	case value == nil:
		return "unlimited"
	case *value == 0:
		return "not_included"
	default:
		return "limited"
	}
}

func quotaInt64State(value *int64) string {
	switch {
	case value == nil:
		return "unlimited"
	case *value == 0:
		return "not_included"
	default:
		return "limited"
	}
}

func normalizeUserExpireTime(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	expireTime := normalizeExpireTime(value)
	if expireTime == "" {
		return "", errors.New("invalid expiration time")
	}
	return expireTime, nil
}

func parseUserStatus(value string) (bool, error) {
	value = strings.TrimSpace(value)
	if value == "0" {
		return false, nil
	}
	if value == "1" {
		return true, nil
	}
	return strconv.ParseBool(value)
}

// userQuotaForm parses the three-state user quota fields. A missing field keeps
// the existing stored value, a blank field clears it to unlimited, and zero is
// preserved as the explicit "not included" state.
type userQuotaForm struct {
	maxClientNum *int
	maxTunnelNum *int
	maxHostNum   *int
	maxConn      *int
	bandwidthBps *int64
}

func parseUserQuotaForm(s *UserController, existing *file.User) (userQuotaForm, error) {
	form := userQuotaForm{}
	if existing != nil {
		existing.RLock()
		form.maxClientNum = file.IntQuotaSnapshot(existing.MaxClientNum)
		form.maxTunnelNum = file.IntQuotaSnapshot(existing.MaxTunnelNum)
		form.maxHostNum = file.IntQuotaSnapshot(existing.MaxHostNum)
		form.maxConn = file.IntQuotaSnapshot(existing.MaxConn)
		form.bandwidthBps = file.Int64QuotaSnapshot(existing.BandwidthLimit)
		existing.RUnlock()
	}
	if s == nil || s.Ctx == nil || s.Ctx.Request == nil {
		return form, nil
	}
	if err := s.Ctx.Request.ParseForm(); err != nil {
		return form, err
	}
	intFields := []struct {
		key     string
		target  **int
		current *int
	}{
		{"max_client", &form.maxClientNum, form.maxClientNum},
		{"max_tunnel", &form.maxTunnelNum, form.maxTunnelNum},
		{"max_host", &form.maxHostNum, form.maxHostNum},
		{"max_conn", &form.maxConn, form.maxConn},
	}
	for _, field := range intFields {
		values, submitted := s.Ctx.Request.Form[field.key]
		if !submitted {
			continue
		}
		raw := ""
		if len(values) > 0 {
			raw = values[len(values)-1]
		}
		parsed, _, parseErr := file.ParseIntQuotaInput(raw)
		if parseErr != nil {
			return form, parseErr
		}
		*field.target = parsed
	}
	if values, submitted := s.Ctx.Request.Form["bandwidth_limit"]; submitted {
		raw := ""
		if len(values) > 0 {
			raw = values[len(values)-1]
		}
		parsed, _, parseErr := file.ParseMbpsQuotaInput(raw)
		if parseErr != nil {
			return form, parseErr
		}
		form.bandwidthBps = parsed
	}
	return form, nil
}

func newUserUpdateCandidate(existing *file.User, username, password, email, remark string, quotas userQuotaForm, expireTime string) *file.User {
	existing.RLock()
	id, existingPassword, existingAPIKeyHash, existingAPIKeyCiphertext, existingDashboardKeyHash, existingDashboardKeyCiphertext, status, createTime := existing.Id, existing.Password, existing.APIKeyHash, existing.APIKeyCiphertext, existing.DashboardKeyHash, existing.DashboardKeyCiphertext, existing.Status, existing.CreateTime
	existing.RUnlock()
	updated := &file.User{
		Id:                     id,
		UserName:               username,
		Password:               existingPassword,
		Email:                  email,
		APIKeyHash:             existingAPIKeyHash,
		APIKeyCiphertext:       existingAPIKeyCiphertext,
		DashboardKeyHash:       existingDashboardKeyHash,
		DashboardKeyCiphertext: existingDashboardKeyCiphertext,
		Status:                 status,
		Remark:                 remark,
		MaxClientNum:           quotas.maxClientNum,
		MaxTunnelNum:           quotas.maxTunnelNum,
		MaxHostNum:             quotas.maxHostNum,
		MaxConn:                quotas.maxConn,
		BandwidthLimit:         quotas.bandwidthBps,
		ExpireTime:             expireTime,
		CreateTime:             createTime,
	}
	if password != "" {
		updated.Password = password
	}
	return updated
}

// RegenerateAPIKey rotates the per-user API credential. The raw token is
// returned exactly once; only its SHA-256 hash is persisted in users.json.
// Administrators may target any user, while an API-authenticated user may
// rotate its own token. Browser users can use this endpoint for themselves.
func (s *UserController) RegenerateAPIKey() {
	if !s.RequirePost() {
		return
	}
	targetID := s.GetIntNoErr("id")
	if !s.IsAdmin() {
		if s.userAPIAuthorized {
			targetID = s.apiUserID
		} else {
			principal, _ := s.GetSession(sessionPrincipalKey).(string)
			sessionUserID, _ := sessionInt(s.GetSession("userId"))
			if principal != sessionPrincipalUser || sessionUserID <= 0 || (targetID != 0 && targetID != sessionUserID) {
				s.AjaxErr("无权操作该用户")
				return
			}
			targetID = sessionUserID
		}
	}
	if targetID <= 0 {
		s.AjaxErr("user ID not found")
		return
	}
	user, err := file.GetDb().GetUser(targetID)
	if err != nil || user == nil {
		s.AjaxErr("user ID not found")
		return
	}
	token, tokenHash, err := generateUserAPIToken()
	if err != nil {
		s.AjaxErr("API 密钥生成失败")
		return
	}
	ciphertext := ""
	if encrypted, encryptErr := encryptUserAPIToken(token); encryptErr == nil {
		ciphertext = encrypted
	} else if !errors.Is(encryptErr, errUserAPITokenEncryptionKeyMissing) {
		s.AjaxErr("API 密钥加密保存失败，请检查 auth_key 或 auth_crypt_key 配置")
		return
	}
	user.Lock()
	previousHash, previousCiphertext := user.APIKeyHash, user.APIKeyCiphertext
	user.APIKeyHash = tokenHash
	user.APIKeyCiphertext = ciphertext
	username := user.UserName
	user.Unlock()
	if err := file.GetDb().JsonDb.StoreUsersToJsonFileErr(); err != nil {
		user.Lock()
		user.APIKeyHash, user.APIKeyCiphertext = previousHash, previousCiphertext
		user.Unlock()
		s.AjaxErr("API 密钥保存失败")
		return
	}
	s.auditMutation("user.api_key_rotate", "user", targetID, targetID, "", nil, map[string]string{"username": username})
	s.Data["json"] = map[string]interface{}{"status": 1, "msg": "API Token 已生成，可随时在接入与安全页面再次查看", "token": token}
	s.ServeJSON()
	s.StopRun()
}

// APIKeyStatus reports whether the target credential exists. The raw token is
// returned only when the caller is the account owner or an administrator and
// the stored ciphertext decrypts and matches the persisted digest; otherwise
// only the enabled flag and a masked placeholder are returned.
func (s *UserController) APIKeyStatus() {
	if !s.IsAdmin() {
		if _, ok := s.currentUserPrincipalID(); !ok {
			s.AjaxErr("当前会话不是用户账号")
			return
		}
	}
	userID := s.GetIntNoErr("id")
	if s.IsAdmin() {
		if userID <= 0 {
			s.AjaxErr("user ID not found")
			return
		}
	} else {
		userID, _ = s.currentUserPrincipalID()
	}
	user, err := file.GetDb().GetUser(userID)
	if err != nil || user == nil {
		s.AjaxErr("user ID not found")
		return
	}
	user.RLock()
	enabled := strings.TrimSpace(user.APIKeyHash) != ""
	ciphertext := user.APIKeyCiphertext
	user.RUnlock()
	response := map[string]interface{}{"status": 1, "enabled": enabled, "masked": maskedUserAPIToken(enabled)}
	if enabled {
		if token, decryptErr := decryptUserAPIToken(ciphertext); decryptErr == nil && validUserAPIToken(fileUserAPIKeyHash(user), token) {
			response["token"] = token
		}
	}
	s.Data["json"] = response
	s.ServeJSON()
	s.StopRun()
}

func maskedUserAPIToken(enabled bool) string {
	if !enabled {
		return ""
	}
	return userAPITokenPrefix + "••••••••"
}

func fileUserAPIKeyHash(user *file.User) string {
	if user == nil {
		return ""
	}
	user.RLock()
	hash := user.APIKeyHash
	user.RUnlock()
	return hash
}

// RevokeAPIKey immediately disables the selected user's API credential. The
// same endpoint is available to the account owner and administrators; an
// ordinary user cannot submit another user's ID.
func (s *UserController) RevokeAPIKey() {
	if !s.RequirePost() {
		return
	}
	targetID := s.GetIntNoErr("id")
	if !s.IsAdmin() {
		if s.userAPIAuthorized {
			targetID = s.apiUserID
		} else {
			principal, _ := s.GetSession(sessionPrincipalKey).(string)
			sessionUserID, _ := sessionInt(s.GetSession("userId"))
			if principal != sessionPrincipalUser || sessionUserID <= 0 || (targetID != 0 && targetID != sessionUserID) {
				s.AjaxErr("无权操作该用户")
				return
			}
			targetID = sessionUserID
		}
	}
	if targetID <= 0 {
		s.AjaxErr("user ID not found")
		return
	}
	user, err := file.GetDb().GetUser(targetID)
	if err != nil || user == nil {
		s.AjaxErr("user ID not found")
		return
	}
	user.Lock()
	previousHash, previousCiphertext := user.APIKeyHash, user.APIKeyCiphertext
	user.APIKeyHash = ""
	user.APIKeyCiphertext = ""
	username := user.UserName
	user.Unlock()
	if err := file.GetDb().JsonDb.StoreUsersToJsonFileErr(); err != nil {
		user.Lock()
		user.APIKeyHash, user.APIKeyCiphertext = previousHash, previousCiphertext
		user.Unlock()
		s.AjaxErr("API 密钥撤销失败")
		return
	}
	s.auditMutation("user.api_key_revoke", "user", targetID, targetID, "", nil, map[string]string{"username": username})
	s.AjaxOk("API 密钥已撤销")
}

// parsePasswordChangeInput accepts the descriptive account-popover field
// names as well as the existing user-form names. A confirmation is optional
// for administrator resets, but when supplied it must match exactly.
func parsePasswordChangeInput(newPassword, password, confirmation, passwordConfirmation string) (string, error) {
	if newPassword == "" {
		newPassword = password
	}
	if strings.TrimSpace(newPassword) == "" {
		return "", errors.New("新密码不能为空")
	}
	if len([]byte(newPassword)) < 6 {
		return "", errors.New("密码至少需要 6 个字符")
	}
	if confirmation == "" {
		confirmation = passwordConfirmation
	}
	if confirmation != "" && confirmation != newPassword {
		return "", errors.New("两次输入的密码不一致")
	}
	return newPassword, nil
}

func (s *UserController) List() {
	if s.Ctx.Request.Method == "GET" {
		s.Data["menu"] = "user"
		s.SetInfo("user")
		s.display("user/list")
		return
	}
	start, length := s.GetAjaxParams()
	list, cnt := file.GetDb().GetUserListSorted(start, length, s.getEscapeString("search"), s.getEscapeString("sort"), s.getEscapeString("order"))
	s.AjaxTable(newUserListRowsWithResourceCounts(list, file.GetDb().GetUserResourceCounts()), cnt, cnt, nil)
}

func (s *UserController) Add() {
	if s.Ctx.Request.Method == "GET" {
		s.Data["menu"] = "user"
		s.SetInfo("add user")
		s.display()
		return
	}
	if !s.RequirePost() {
		return
	}
	expireTime, err := normalizeUserExpireTime(s.GetString("expire_time"))
	if err != nil {
		s.AjaxErr(err.Error())
		return
	}
	quotas, err := parseUserQuotaForm(s, nil)
	if err != nil {
		s.AjaxErr(err.Error())
		return
	}
	u := &file.User{
		UserName:       s.getEscapeString("username"),
		Password:       s.GetString("password"),
		Email:          strings.TrimSpace(s.GetString("email")),
		Status:         true,
		Remark:         s.getEscapeString("remark"),
		MaxClientNum:   quotas.maxClientNum,
		MaxTunnelNum:   quotas.maxTunnelNum,
		MaxHostNum:     quotas.maxHostNum,
		MaxConn:        quotas.maxConn,
		BandwidthLimit: quotas.bandwidthBps,
		ExpireTime:     expireTime,
		CreateTime:     time.Now().Format("2006-01-02 15:04:05"),
	}
	if err := file.GetDb().NewUser(u); err != nil {
		s.AjaxErr(err.Error())
		return
	}
	s.auditMutation("user.create", "user", u.Id, u.Id, "", nil, map[string]string{"username": u.UserName, "remark": u.Remark})
	s.AjaxOkWithId("add success", u.Id)
}

func (s *UserController) Edit() {
	id := s.GetIntNoErr("id")
	if s.Ctx.Request.Method == "GET" {
		s.Data["menu"] = "user"
		u, err := file.GetDb().GetUser(id)
		if err != nil {
			s.error()
			return
		}
		s.Data["u"] = u
		u.RLock()
		bandwidth := int64(0)
		if u.BandwidthLimit != nil {
			bandwidth = *u.BandwidthLimit
		}
		u.RUnlock()
		s.Data["bandwidthMbps"] = rate.FormatMbps(bandwidth)
		s.SetInfo("edit user")
		s.display()
		return
	}
	if !s.RequirePost() {
		return
	}
	u, err := file.GetDb().GetUser(id)
	if err != nil {
		s.AjaxErr("user ID not found")
		return
	}
	expireTime, err := normalizeUserExpireTime(s.GetString("expire_time"))
	if err != nil {
		s.AjaxErr(err.Error())
		return
	}
	quotas, err := parseUserQuotaForm(s, u)
	if err != nil {
		s.AjaxErr(err.Error())
		return
	}
	updated := newUserUpdateCandidate(
		u,
		s.getEscapeString("username"),
		s.GetString("password"),
		strings.TrimSpace(s.GetString("email")),
		s.getEscapeString("remark"),
		quotas,
		expireTime,
	)
	if err := file.GetDb().UpdateUser(updated); err != nil {
		s.AjaxErr(err.Error())
		return
	}
	s.auditMutation("user.update", "user", id, id, "", nil, map[string]string{"username": updated.UserName, "remark": updated.Remark})
	s.AjaxOk("save success")
}

// ChangePassword updates a dashboard user's password without exposing the
// existing credential to the browser. Ordinary users may only change their
// own password and must prove knowledge of the current one; administrators
// may reset any user account by supplying its id. The dedicated endpoint is
// used by the account popover so users do not need to leave the current page.
func (s *UserController) ChangePassword() {
	if !s.RequirePost() {
		return
	}

	targetID := s.GetIntNoErr("id")
	if !s.IsAdmin() {
		principal, _ := s.GetSession(sessionPrincipalKey).(string)
		sessionUserID, _ := sessionInt(s.GetSession("userId"))
		if principal != sessionPrincipalUser || sessionUserID <= 0 {
			s.AjaxErr("当前会话不是用户账号")
			return
		}
		if targetID != 0 && targetID != sessionUserID {
			s.AjaxErr("无权修改其他用户密码")
			return
		}
		targetID = sessionUserID
	}
	if targetID <= 0 {
		s.AjaxErr("user ID not found")
		return
	}

	user, err := file.GetDb().GetUser(targetID)
	if err != nil || user == nil {
		s.AjaxErr("user ID not found")
		return
	}

	// Accept both the descriptive field names used by the account popover and
	// the shorter names used by the existing user form. Preserve the submitted
	// password bytes (including intentional spaces); only blank values are
	// rejected.
	newPassword, passwordErr := parsePasswordChangeInput(
		s.GetString("new_password"),
		s.GetString("password"),
		s.GetString("confirm_password"),
		s.GetString("password_confirm"),
	)
	if passwordErr != nil {
		s.AjaxErr(passwordErr.Error())
		return
	}

	user.RLock()
	username, currentPassword, status, remark := user.UserName, user.Password, user.Status, user.Remark
	email := user.Email
	maxConn := file.IntQuotaSnapshot(user.MaxConn)
	bandwidthLimit := file.Int64QuotaSnapshot(user.BandwidthLimit)
	maxClientNum := file.IntQuotaSnapshot(user.MaxClientNum)
	maxTunnelNum := file.IntQuotaSnapshot(user.MaxTunnelNum)
	maxHostNum := file.IntQuotaSnapshot(user.MaxHostNum)
	expireTime, createTime, apiKeyHash, apiKeyCiphertext, dashboardKeyHash, dashboardKeyCiphertext := user.ExpireTime, user.CreateTime, user.APIKeyHash, user.APIKeyCiphertext, user.DashboardKeyHash, user.DashboardKeyCiphertext
	user.RUnlock()
	if !s.IsAdmin() {
		current := s.GetString("current_password")
		if current == "" {
			current = s.GetString("old_password")
		}
		if current == "" || subtle.ConstantTimeCompare([]byte(currentPassword), []byte(current)) != 1 {
			s.AjaxErr("当前密码不正确")
			return
		}
	}

	updated := &file.User{
		Id:                     targetID,
		UserName:               username,
		Password:               newPassword,
		Email:                  email,
		MaxConn:                maxConn,
		BandwidthLimit:         bandwidthLimit,
		APIKeyHash:             apiKeyHash,
		APIKeyCiphertext:       apiKeyCiphertext,
		DashboardKeyHash:       dashboardKeyHash,
		DashboardKeyCiphertext: dashboardKeyCiphertext,
		Status:                 status,
		Remark:                 remark,
		MaxClientNum:           maxClientNum,
		MaxTunnelNum:           maxTunnelNum,
		MaxHostNum:             maxHostNum,
		ExpireTime:             expireTime,
		CreateTime:             createTime,
	}
	if err := file.GetDb().UpdateUser(updated); err != nil {
		s.AjaxErr(err.Error())
		return
	}
	s.auditMutation("user.password_change", "user", targetID, targetID, "", nil, nil)
	s.AjaxOk("密码修改成功")
}

func (s *UserController) ChangeStatus() {
	if !s.RequirePost() {
		return
	}
	id := s.GetIntNoErr("id")
	if id <= 0 {
		s.AjaxErr("user ID not found")
		return
	}
	status, err := parseUserStatus(s.GetString("status"))
	if err != nil {
		s.AjaxErr("invalid status")
		return
	}
	user, err := file.GetDb().GetUser(id)
	if err != nil {
		s.AjaxErr("user ID not found")
		return
	}
	user.Lock()
	previousStatus := user.Status
	user.Status = status
	user.Unlock()
	if err := file.GetDb().JsonDb.StoreUsersToJsonFileErr(); err != nil {
		user.Lock()
		user.Status = previousStatus
		user.Unlock()
		s.AjaxErr("用户状态保存失败")
		return
	}
	if !status {
		server.PauseUserClients(id)
	}
	s.auditMutation("user.status_change", "user", id, id, "", nil, map[string]string{"status": strconv.FormatBool(status)})
	s.AjaxOk("modified success")
}

func (s *UserController) Del() {
	if !s.RequirePost() {
		return
	}
	id := s.GetIntNoErr("id")
	if id <= 0 {
		s.AjaxErr("user ID not found")
		return
	}
	if _, err := file.GetDb().GetUser(id); err != nil {
		s.AjaxErr("user ID not found")
		return
	}
	// Mark the account inactive before revocation so a reconnect cannot win a
	// race between disconnecting the old session and removing its ownership
	// link. DelUser also disables the persisted clients for direct callers.
	user, err := file.GetDb().GetUser(id)
	if err != nil {
		s.AjaxErr("user ID not found")
		return
	}
	user.Lock()
	previousStatus := user.Status
	user.Status = false
	user.Unlock()
	if err := file.GetDb().JsonDb.StoreUsersToJsonFileErr(); err != nil {
		user.Lock()
		user.Status = previousStatus
		user.Unlock()
		s.AjaxErr("停用用户失败")
		return
	}
	restoreStatus := func() {
		user.Lock()
		user.Status = previousStatus
		user.Unlock()
		if persistErr := file.GetDb().JsonDb.StoreUsersToJsonFileErr(); persistErr != nil {
			logs.Error("restore user %d status after delete failure: %v", id, persistErr)
		}
	}
	var committedClients []*file.Client
	var committedTunnels []*file.Tunnel
	afterCommit := func(clients []*file.Client, tasks []*file.Tunnel, _ []*file.Host) {
		committedClients = clients
		committedTunnels = tasks
	}
	if err := file.GetDb().DelUserCascadeWithHook(id, afterCommit); err != nil {
		restoreStatus()
		s.AjaxErr("delete error")
		return
	}
	if len(committedClients) > 0 {
		tasksByClient := make(map[int][]*file.Tunnel)
		for _, task := range committedTunnels {
			if task == nil || task.Client == nil {
				continue
			}
			task.RLock()
			clientID := task.Client.Id
			task.RUnlock()
			tasksByClient[clientID] = append(tasksByClient[clientID], task)
		}
		for _, client := range committedClients {
			if client == nil {
				continue
			}
			if cleanupErr := server.FinalizeCommittedClientDeletion(client.Id, tasksByClient[client.Id]); cleanupErr != nil {
				logs.Warn("user %d was deleted but client %d runtime cleanup failed: %v", id, client.Id, cleanupErr)
			}
		}
	}
	s.auditMutation("user.delete", "user", id, id, "", nil, nil)
	s.AjaxOk("delete success")
}
