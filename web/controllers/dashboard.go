package controllers

import (
	"net/http"
	"strings"

	"ehang.io/nps/lib/file"
	"ehang.io/nps/lib/version"
	"ehang.io/nps/server"
	"github.com/astaxie/beego"
)

// DashboardController is deliberately not a BaseController. The public shell
// must be able to read a key from the browser URL fragment; fragments are not
// sent to the server, so an authenticated request is established by the data
// endpoint instead of by the initial HTML request.
type DashboardController struct {
	beego.Controller
}

func (s *DashboardController) Prepare() {
	s.Data["web_base_url"] = beego.AppConfig.String("web_base_url")
	s.Data["version"] = version.VERSION
}

func (s *DashboardController) Entry() {
	if principal, ok := s.sessionPrincipal(); ok {
		s.renderPrincipal(principal)
		return
	}
	s.TplName = "index/overview-access.html"
}

func (s *DashboardController) Admin() {
	s.Data["overview_account_name"] = "管理员"
	s.TplName = "index/overview-admin.html"
}

func (s *DashboardController) User() {
	accountName := "当前用户"
	if name, ok := s.GetSession("username").(string); ok && strings.TrimSpace(name) != "" {
		accountName = strings.TrimSpace(name)
	}
	s.Data["overview_account_name"] = accountName
	s.TplName = "index/overview.html"
}

func (s *DashboardController) AccessRole() {
	principal, ok := lookupDashboardAccessKey(dashboardAccessKeyFromRequest(s.Ctx.Request))
	if !ok {
		s.Ctx.Output.SetStatus(http.StatusUnauthorized)
		s.Data["json"] = map[string]interface{}{"status": 0, "msg": "大屏密钥无效或已撤销"}
		s.ServeJSON()
		s.StopRun()
		return
	}
	s.Data["json"] = map[string]interface{}{"status": 1, "admin": principal.Admin}
	s.ServeJSON()
	s.StopRun()
}

func (s *DashboardController) TopologyData() {
	principal, ok := s.sessionPrincipal()
	if !ok {
		principal, ok = lookupDashboardAccessKey(dashboardAccessKeyFromRequest(s.Ctx.Request))
	}
	if !ok {
		s.Ctx.Output.SetStatus(http.StatusUnauthorized)
		s.Data["json"] = map[string]interface{}{"status": 0, "msg": "登录状态或大屏密钥已失效"}
		s.ServeJSON()
		s.StopRun()
		return
	}
	var data server.TopologySnapshot
	if principal.Admin {
		data = server.GetTopologyData(nil, true)
	} else if principal.UserID > 0 {
		data = server.GetTopologyData(file.GetDb().UserClientIds(principal.UserID), false)
	} else {
		data = server.GetTopologyData(map[int]struct{}{principal.ClientID: {}}, false)
	}
	s.Data["json"] = map[string]interface{}{"status": 1, "data": data, "admin": principal.Admin}
	s.ServeJSON()
	s.StopRun()
}

func (s *DashboardController) sessionPrincipal() (dashboardPrincipal, bool) {
	if !sessionBool(s.GetSession("auth")) {
		return dashboardPrincipal{}, false
	}
	if sessionBool(s.GetSession("isAdmin")) {
		return dashboardPrincipal{Admin: true}, true
	}
	principal, _ := s.GetSession(sessionPrincipalKey).(string)
	if principal == sessionPrincipalUser {
		userID, _ := sessionInt(s.GetSession("userId"))
		if userID > 0 && file.GetDb().IsUserActive(userID) {
			return dashboardPrincipal{UserID: userID}, true
		}
	}
	if principal == sessionPrincipalClient {
		clientID, _ := sessionInt(s.GetSession("clientId"))
		if clientID > 0 {
			if client, err := file.GetDb().GetClient(clientID); err == nil && file.GetDb().IsClientActive(client) {
				return dashboardPrincipal{ClientID: clientID}, true
			}
		}
	}
	return dashboardPrincipal{}, false
}

func (s *DashboardController) renderPrincipal(principal dashboardPrincipal) {
	s.Data["overview_account_name"] = "管理员"
	if !principal.Admin {
		if name, ok := s.GetSession("username").(string); ok && strings.TrimSpace(name) != "" {
			s.Data["overview_account_name"] = strings.TrimSpace(name)
		} else {
			s.Data["overview_account_name"] = "当前用户"
		}
	}
	if principal.Admin {
		s.TplName = "index/overview-admin.html"
	} else {
		s.TplName = "index/overview.html"
	}
}
