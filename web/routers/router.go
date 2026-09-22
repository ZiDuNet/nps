package routers

import (
	"ehang.io/nps/web/controllers"
	"github.com/astaxie/beego"
)

func Init() {
	web_base_url := beego.AppConfig.String("web_base_url")
	if len(web_base_url) > 0 {
		ns := beego.NewNamespace(web_base_url,
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
			beego.NSAutoRouter(&controllers.GlobalController{}),
			beego.NSAutoRouter(&controllers.AuditController{}),
		)
		beego.AddNamespace(ns)
	} else {
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
		beego.AutoRouter(&controllers.GlobalController{})
		beego.AutoRouter(&controllers.AuditController{})

	}
}
