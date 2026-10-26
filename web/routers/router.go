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

	}
}
