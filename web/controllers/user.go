package controllers

import (
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
	MaxTunnelNum int
	ExpireTime   string
	CreateTime   string
}

func newUserListRows(users []*file.User) []*userListRow {
	rows := make([]*userListRow, 0, len(users))
	for _, user := range users {
		if user == nil {
			continue
		}
		user.RLock()
		id, userName, status := user.Id, user.UserName, user.Status
		remark, maxTunnelNum, expireTime, createTime := user.Remark, user.MaxTunnelNum, user.ExpireTime, user.CreateTime
		user.RUnlock()
		rows = append(rows, &userListRow{
			Id:           id,
			UserName:     html.UnescapeString(userName),
			Password:     "",
			Status:       status,
			Remark:       html.UnescapeString(remark),
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

func newUserUpdateCandidate(existing *file.User, username, password, remark string, maxTunnelNum int, expireTime string) *file.User {
	existing.RLock()
	id, existingPassword, status, createTime := existing.Id, existing.Password, existing.Status, existing.CreateTime
	existing.RUnlock()
	updated := &file.User{
		Id:           id,
		UserName:     username,
		Password:     existingPassword,
		Status:       status,
		Remark:       remark,
		MaxTunnelNum: maxTunnelNum,
		ExpireTime:   expireTime,
		CreateTime:   createTime,
	}
	if password != "" {
		updated.Password = password
	}
	return updated
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
	s.AjaxTable(newUserListRows(list), cnt, cnt, nil)
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
		normalizeUserTunnelLimit(s.GetIntNoErr("max_tunnel")),
		expireTime,
	)
	if err := file.GetDb().UpdateUser(updated); err != nil {
		s.AjaxErr(err.Error())
		return
	}
	s.AjaxOk("save success")
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
