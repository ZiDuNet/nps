package routers

import (
	"ehang.io/nps/web/controllers"
	"github.com/astaxie/beego"
)

func Init() {
	controllers.InitCaptcha()
	beego.ErrorController(&controllers.ErrorController{})
	web_base_url := beego.AppConfig.String("web_base_url")
	if len(web_base_url) > 0 {
		ns := beego.NewNamespace(web_base_url,
			beego.NSRouter("/ip/register", &controllers.LoginController{}, "*:IPRegister"),
			beego.NSRouter("/", &controllers.IndexController{}, "*:Index"),
			beego.NSRouter("/overview", &controllers.DashboardController{}, "*:Entry"),
			beego.NSRouter("/overview/admin", &controllers.DashboardController{}, "*:Admin"),
			beego.NSRouter("/overview/user", &controllers.DashboardController{}, "*:User"),
			beego.NSRouter("/overview/data", &controllers.DashboardController{}, "*:TopologyData"),
			beego.NSRouter("/overview/access/role", &controllers.DashboardController{}, "*:AccessRole"),
			beego.NSRouter("/overview/accesskey", &controllers.DashboardAccessController{}, "GET:Status;POST:Generate"),
			beego.NSRouter("/overview/accesskey/revoke", &controllers.DashboardAccessController{}, "POST:Revoke"),
			beego.NSAutoRouter(&controllers.IndexController{}),
			beego.NSAutoRouter(&controllers.LoginController{}),
			beego.NSAutoRouter(&controllers.ClientController{}),
			beego.NSAutoRouter(&controllers.UserController{}),
			beego.NSAutoRouter(&controllers.AuthController{}),
			beego.NSRouter("/auth/ipwhiteauth", &controllers.AuthController{}, "*:IpWhiteAuth"),
			beego.NSRouter("/global/config", &controllers.GlobalController{}, "GET:Config"),
			beego.NSRouter("/global/config/save", &controllers.GlobalController{}, "POST:SaveConfig"),
			beego.NSRouter("/global/config/restart", &controllers.GlobalController{}, "POST:Restart"),
			beego.NSRouter("/global/banlist", &controllers.GlobalController{}, "GET:Banlist;POST:Banlist"),
			beego.NSRouter("/global/unban", &controllers.GlobalController{}, "POST:Unban"),
			beego.NSRouter("/global/unbanall", &controllers.GlobalController{}, "POST:Unbanall"),
			beego.NSRouter("/global/banclean", &controllers.GlobalController{}, "POST:Banclean"),
			beego.NSRouter("/global/ipregister", &controllers.GlobalController{}, "GET:IPRegisterPage"),
			beego.NSRouter("/global/ipregister/list", &controllers.GlobalController{}, "POST:IPRegisterList"),
			beego.NSRouter("/global/ipregister/revoke", &controllers.GlobalController{}, "POST:IPRegisterRevoke"),
			beego.NSRouter("/global/ipregister/clean", &controllers.GlobalController{}, "POST:IPRegisterClean"),
			beego.NSRouter("/global/ipregister/clear", &controllers.GlobalController{}, "POST:IPRegisterClear"),
			beego.NSRouter("/global/geo", &controllers.GlobalController{}, "GET:GeoStatus"),
			beego.NSRouter("/global/updategeo", &controllers.GlobalController{}, "POST:UpdateGeoData"),
			beego.NSAutoRouter(&controllers.GlobalController{}),
			beego.NSAutoRouter(&controllers.AuditController{}),
			beego.NSNamespace("/api/v1", apiV1Routers()...),
		)
		beego.AddNamespace(ns)
	} else {
		beego.Router("/ip/register", &controllers.LoginController{}, "*:IPRegister")
		beego.Router("/", &controllers.IndexController{}, "*:Index")
		beego.Router("/overview", &controllers.DashboardController{}, "*:Entry")
		beego.Router("/overview/admin", &controllers.DashboardController{}, "*:Admin")
		beego.Router("/overview/user", &controllers.DashboardController{}, "*:User")
		beego.Router("/overview/data", &controllers.DashboardController{}, "*:TopologyData")
		beego.Router("/overview/access/role", &controllers.DashboardController{}, "*:AccessRole")
		beego.Router("/overview/accesskey", &controllers.DashboardAccessController{}, "GET:Status;POST:Generate")
		beego.Router("/overview/accesskey/revoke", &controllers.DashboardAccessController{}, "POST:Revoke")
		beego.AutoRouter(&controllers.IndexController{})
		beego.AutoRouter(&controllers.LoginController{})
		beego.AutoRouter(&controllers.ClientController{})
		beego.AutoRouter(&controllers.UserController{})
		beego.AutoRouter(&controllers.AuthController{})
		beego.Router("/auth/ipwhiteauth", &controllers.AuthController{}, "*:IpWhiteAuth")
		beego.Router("/global/config", &controllers.GlobalController{}, "GET:Config")
		beego.Router("/global/config/save", &controllers.GlobalController{}, "POST:SaveConfig")
		beego.Router("/global/config/restart", &controllers.GlobalController{}, "POST:Restart")
		beego.Router("/global/banlist", &controllers.GlobalController{}, "GET:Banlist;POST:Banlist")
		beego.Router("/global/unban", &controllers.GlobalController{}, "POST:Unban")
		beego.Router("/global/unbanall", &controllers.GlobalController{}, "POST:Unbanall")
		beego.Router("/global/banclean", &controllers.GlobalController{}, "POST:Banclean")
		beego.Router("/global/ipregister", &controllers.GlobalController{}, "GET:IPRegisterPage")
		beego.Router("/global/ipregister/list", &controllers.GlobalController{}, "POST:IPRegisterList")
		beego.Router("/global/ipregister/revoke", &controllers.GlobalController{}, "POST:IPRegisterRevoke")
		beego.Router("/global/ipregister/clean", &controllers.GlobalController{}, "POST:IPRegisterClean")
		beego.Router("/global/ipregister/clear", &controllers.GlobalController{}, "POST:IPRegisterClear")
		beego.Router("/global/geo", &controllers.GlobalController{}, "GET:GeoStatus")
		beego.Router("/global/updategeo", &controllers.GlobalController{}, "POST:UpdateGeoData")
		beego.AutoRouter(&controllers.GlobalController{})
		beego.AutoRouter(&controllers.AuditController{})
		beego.AddNamespace(beego.NewNamespace("/api/v1", apiV1Routers()...))
	}
}

// apiV1Routers returns the versioned public API surface. The same route set is
// mounted below web_base_url when one is configured and at the root otherwise.
// Legacy /client, /index, /user, /global and /auth routes stay untouched.
func apiV1Routers() []beego.LinkNamespace {
	return []beego.LinkNamespace{
		// Public bootstrap endpoints (no credentials required).
		beego.NSRouter("/", &controllers.ApiV1InfoController{}, "GET:Info"),
		beego.NSRouter("/time", &controllers.ApiV1InfoController{}, "GET:Time"),
		beego.NSRouter("/authkey", &controllers.ApiV1InfoController{}, "GET:AuthKey"),
		beego.NSRouter("/ipwhiteauth", &controllers.ApiV1InfoController{}, "*:IPWhiteAuth"),

		// Account-scoped dashboard snapshots.
		beego.NSRouter("/status", &controllers.ApiV1Controller{}, "GET:Status"),
		beego.NSRouter("/overview", &controllers.ApiV1Controller{}, "GET:Overview"),

		// Clients.
		beego.NSRouter("/clients", &controllers.ApiV1Controller{}, "GET:Clients;POST:ClientsCreate"),
		beego.NSRouter("/clients/:id", &controllers.ApiV1Controller{}, "GET:ClientGet;PATCH:ClientUpdate;PUT:ClientUpdate;DELETE:ClientDelete"),
		beego.NSRouter("/clients/:id/status", &controllers.ApiV1Controller{}, "POST:ClientStatus"),
		beego.NSRouter("/clients/:id/upgrade", &controllers.ApiV1Controller{}, "POST:ClientUpgrade"),
		beego.NSRouter("/clients/:id/update-check", &controllers.ApiV1Controller{}, "GET:ClientUpdateCheck"),

		// Tunnels.
		beego.NSRouter("/tunnels", &controllers.ApiV1Controller{}, "GET:Tunnels;POST:TunnelsCreate"),
		beego.NSRouter("/tunnels/:id", &controllers.ApiV1Controller{}, "GET:TunnelGet;PATCH:TunnelUpdate;PUT:TunnelUpdate;DELETE:TunnelDelete"),
		beego.NSRouter("/tunnels/:id/start", &controllers.ApiV1Controller{}, "POST:TunnelStart"),
		beego.NSRouter("/tunnels/:id/stop", &controllers.ApiV1Controller{}, "POST:TunnelStop"),
		beego.NSRouter("/tunnels/:id/copy", &controllers.ApiV1Controller{}, "POST:TunnelCopy"),

		// Domain reverse proxy rules.
		beego.NSRouter("/hosts", &controllers.ApiV1Controller{}, "GET:Hosts;POST:HostsCreate"),
		beego.NSRouter("/hosts/:id", &controllers.ApiV1Controller{}, "GET:HostGet;PATCH:HostUpdate;PUT:HostUpdate;DELETE:HostDelete"),
		beego.NSRouter("/hosts/:id/start", &controllers.ApiV1Controller{}, "POST:HostStart"),
		beego.NSRouter("/hosts/:id/stop", &controllers.ApiV1Controller{}, "POST:HostStop"),
		beego.NSRouter("/hosts/check", &controllers.ApiV1Controller{}, "POST:HostCheck"),
		beego.NSRouter("/hosts/diagnose", &controllers.ApiV1Controller{}, "POST:HostDiagnose"),

		// Dashboard users.
		beego.NSRouter("/users", &controllers.ApiV1Controller{}, "GET:Users;POST:UsersCreate"),
		beego.NSRouter("/users/:id", &controllers.ApiV1Controller{}, "GET:UserGet;PATCH:UserUpdate;PUT:UserUpdate;DELETE:UserDelete"),
		beego.NSRouter("/users/:id/status", &controllers.ApiV1Controller{}, "POST:UserStatus"),
		beego.NSRouter("/users/:id/password", &controllers.ApiV1Controller{}, "POST:UserPassword"),
		beego.NSRouter("/users/:id/token", &controllers.ApiV1Controller{}, "POST:UserToken"),
		beego.NSRouter("/users/:id/token/revoke", &controllers.ApiV1Controller{}, "POST:UserTokenRevoke"),
		beego.NSRouter("/users/:id/token/status", &controllers.ApiV1Controller{}, "GET:UserTokenStatus"),

		// Global configuration and operational endpoints.
		beego.NSRouter("/global/config", &controllers.ApiV1Controller{}, "GET:GlobalConfig;POST:GlobalConfigSave"),
		beego.NSRouter("/global/config/restart", &controllers.ApiV1Controller{}, "POST:GlobalRestart"),
		beego.NSRouter("/global/geo", &controllers.ApiV1Controller{}, "GET:GeoStatus"),
		beego.NSRouter("/global/geo/update", &controllers.ApiV1Controller{}, "POST:GeoUpdate"),
		beego.NSRouter("/global/banlist", &controllers.ApiV1Controller{}, "GET,POST:BanList"),
		beego.NSRouter("/global/unban", &controllers.ApiV1Controller{}, "POST:BanUnban"),
		beego.NSRouter("/global/unbanall", &controllers.ApiV1Controller{}, "POST:BanUnbanAll"),
		beego.NSRouter("/global/banclean", &controllers.ApiV1Controller{}, "POST:BanClean"),
		beego.NSRouter("/global/ip-register", &controllers.ApiV1Controller{}, "GET,POST:IPRegisterList"),
		beego.NSRouter("/global/ip-register/revoke", &controllers.ApiV1Controller{}, "POST:IPRegisterRevoke"),
		beego.NSRouter("/global/ip-register/clean", &controllers.ApiV1Controller{}, "POST:IPRegisterClean"),
		beego.NSRouter("/global/ip-register/clear", &controllers.ApiV1Controller{}, "POST:IPRegisterClear"),

		// Control-plane audit journal.
		beego.NSRouter("/audit", &controllers.ApiV1Controller{}, "GET,POST:Audit"),
	}
}
