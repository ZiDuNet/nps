package controllers

import (
	"time"

	"ehang.io/nps/lib/file"
)

type UserController struct {
	BaseController
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
	s.AjaxTable(list, cnt, cnt, nil)
}

func (s *UserController) Add() {
	if s.Ctx.Request.Method == "GET" {
		s.Data["menu"] = "user"
		s.SetInfo("add user")
		s.display()
		return
	}
	u := &file.User{
		UserName:     s.getEscapeString("username"),
		Password:     s.getEscapeString("password"),
		Status:       true,
		Remark:       s.getEscapeString("remark"),
		MaxTunnelNum: s.GetIntNoErr("max_tunnel"),
		ExpireTime:   normalizeExpireTime(s.getEscapeString("expire_time")),
		CreateTime:   time.Now().Format("2006-01-02 15:04:05"),
	}
	if err := file.GetDb().NewUser(u); err != nil {
		s.AjaxErr(err.Error())
	}
	s.AjaxOkWithId("add success", u.Id)
}

func (s *UserController) Edit() {
	id := s.GetIntNoErr("id")
	if s.Ctx.Request.Method == "GET" {
		s.Data["menu"] = "user"
		if u, err := file.GetDb().GetUser(id); err != nil {
			s.error()
		} else {
			s.Data["u"] = u
		}
		s.SetInfo("edit user")
		s.display()
		return
	}
	u, err := file.GetDb().GetUser(id)
	if err != nil {
		s.AjaxErr("user ID not found")
		return
	}
	u.UserName = s.getEscapeString("username")
	u.Password = s.getEscapeString("password")
	u.Remark = s.getEscapeString("remark")
	u.MaxTunnelNum = s.GetIntNoErr("max_tunnel")
	u.ExpireTime = normalizeExpireTime(s.getEscapeString("expire_time"))
	if err := file.GetDb().UpdateUser(u); err != nil {
		s.AjaxErr(err.Error())
	}
	s.AjaxOk("save success")
}

func (s *UserController) ChangeStatus() {
	id := s.GetIntNoErr("id")
	if user, err := file.GetDb().GetUser(id); err == nil {
		user.Status = s.GetBoolNoErr("status")
		file.GetDb().JsonDb.StoreUsersToJsonFile()
		s.AjaxOk("modified success")
	}
	s.AjaxErr("modified fail")
}

func (s *UserController) Del() {
	id := s.GetIntNoErr("id")
	if err := file.GetDb().DelUser(id); err != nil {
		s.AjaxErr("delete error")
	}
	s.AjaxOk("delete success")
}
