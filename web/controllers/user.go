package controllers

import (
	"crypto/subtle"
	"errors"
	"html"
	"strconv"
	"strings"
	"time"

	"ehang.io/nps/lib/file"
	"ehang.io/nps/server"
)

type UserController struct {
	BaseController
}

// userListRow deliberately keeps the historical response shape while never
// sending stored credentials to the browser.
type userListRow struct {
	Id           int
	UserName     string
	Password     string
	Status       bool
	Remark       string
	ClientCount  int
	TunnelCount  int
	MaxClientNum int
	MaxTunnelNum int
	ExpireTime   string
	CreateTime   string
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
		remark, maxClientNum, maxTunnelNum, expireTime, createTime := user.Remark, user.MaxClientNum, user.MaxTunnelNum, user.ExpireTime, user.CreateTime
		user.RUnlock()
		resourceCounts := counts[id]
		rows = append(rows, &userListRow{
			Id:           id,
			UserName:     html.UnescapeString(userName),
			Password:     "",
			Status:       status,
			Remark:       html.UnescapeString(remark),
			ClientCount:  resourceCounts.ClientCount,
			TunnelCount:  resourceCounts.TunnelCount,
			MaxClientNum: maxClientNum,
			MaxTunnelNum: maxTunnelNum,
			ExpireTime:   expireTime,
			CreateTime:   createTime,
		})
	}
	return rows
}

func normalizeUserTunnelLimit(limit int) int {
	if limit < 0 {
		return 0
	}
	return limit
}

func normalizeUserClientLimit(limit int) int {
	if limit < 0 {
		return 0
	}
	return limit
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

func newUserUpdateCandidate(existing *file.User, username, password, remark string, maxClientNum, maxTunnelNum int, expireTime string) *file.User {
	existing.RLock()
	id, existingPassword, status, createTime := existing.Id, existing.Password, existing.Status, existing.CreateTime
	existing.RUnlock()
	updated := &file.User{
		Id:           id,
		UserName:     username,
		Password:     existingPassword,
		Status:       status,
		Remark:       remark,
		MaxClientNum: maxClientNum,
		MaxTunnelNum: maxTunnelNum,
		ExpireTime:   expireTime,
		CreateTime:   createTime,
	}
	if password != "" {
		updated.Password = password
	}
	return updated
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
	list, cnt := file.GetDb().GetUserList(start, length, s.getEscapeString("search"))
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
	u := &file.User{
		UserName:     s.getEscapeString("username"),
		Password:     s.GetString("password"),
		Status:       true,
		Remark:       s.getEscapeString("remark"),
		MaxClientNum: normalizeUserClientLimit(s.GetIntNoErr("max_client")),
		MaxTunnelNum: normalizeUserTunnelLimit(s.GetIntNoErr("max_tunnel")),
		ExpireTime:   expireTime,
		CreateTime:   time.Now().Format("2006-01-02 15:04:05"),
	}
	if err := file.GetDb().NewUser(u); err != nil {
		s.AjaxErr(err.Error())
		return
	}
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
	updated := newUserUpdateCandidate(
		u,
		s.getEscapeString("username"),
		s.GetString("password"),
		s.getEscapeString("remark"),
		normalizeUserClientLimit(s.GetIntNoErr("max_client")),
		normalizeUserTunnelLimit(s.GetIntNoErr("max_tunnel")),
		expireTime,
	)
	if err := file.GetDb().UpdateUser(updated); err != nil {
		s.AjaxErr(err.Error())
		return
	}
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
	maxClientNum, maxTunnelNum, expireTime, createTime := user.MaxClientNum, user.MaxTunnelNum, user.ExpireTime, user.CreateTime
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
		Id:           targetID,
		UserName:     username,
		Password:     newPassword,
		Status:       status,
		Remark:       remark,
		MaxClientNum: maxClientNum,
		MaxTunnelNum: maxTunnelNum,
		ExpireTime:   expireTime,
		CreateTime:   createTime,
	}
	if err := file.GetDb().UpdateUser(updated); err != nil {
		s.AjaxErr(err.Error())
		return
	}
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
	user.Status = status
	user.Unlock()
	if !status {
		server.RevokeUserClients(id)
	}
	file.GetDb().JsonDb.StoreUsersToJsonFile()
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
	user.Status = false
	user.Unlock()
	file.GetDb().JsonDb.StoreUsersToJsonFile()
	server.RevokeUserClients(id)
	if err := file.GetDb().DelUser(id); err != nil {
		s.AjaxErr("delete error")
		return
	}
	s.AjaxOk("delete success")
}
