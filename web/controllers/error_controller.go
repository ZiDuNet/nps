package controllers

import (
	"fmt"

	"ehang.io/nps/lib/version"
	"github.com/astaxie/beego"
)

// ErrorController renders HTTP error responses through the branded error page
// instead of Beego's default English fallback templates. It is registered with
// beego.ErrorController so unmatched routes, panics, and explicit exceptions
// all share one consistent presentation.
type ErrorController struct {
	beego.Controller
}

func (s *ErrorController) prepareError(code int, title, message string) {
	s.Data["code"] = code
	s.Data["title"] = title
	s.Data["message"] = message
	s.Data["web_base_url"] = beego.AppConfig.String("web_base_url")
	s.Data["version"] = version.VERSION
	s.Data["github_repository"] = version.GitHubRepository
	s.TplName = "public/error.html"
}

// Error404 handles missing routes and resources.
func (s *ErrorController) Error404() {
	s.prepareError(404, "页面未找到", "您访问的地址不存在、已被移动，或对应的资源已被删除。")
}

// Error403 handles forbidden routes.
func (s *ErrorController) Error403() {
	s.prepareError(403, "无权访问", "当前账号没有访问该页面所需的权限，请联系管理员。")
}

// Error405 handles unsupported methods.
func (s *ErrorController) Error405() {
	s.prepareError(405, "请求方法不受支持", "该地址不支持当前 HTTP 请求方法。")
}

// Error500 handles internal server errors.
func (s *ErrorController) Error500() {
	s.prepareError(500, "服务器内部错误", "服务处理请求时发生错误，请稍后重试或查看服务端日志。")
}

// Error502 handles bad gateway responses.
func (s *ErrorController) Error502() {
	s.prepareError(502, "上游服务异常", "NPS 无法从上游服务获得有效响应，请检查内网目标与客户端状态。")
}

// Error503 handles unavailable services.
func (s *ErrorController) Error503() {
	s.prepareError(503, "服务暂不可用", "服务当前不可用，可能正在维护或暂时过载，请稍后重试。")
}

// String implements fmt.Stringer for defensive logging without leaking
// request details into error contexts.
func (s *ErrorController) String() string {
	return fmt.Sprintf("ErrorController{%s}", s.TplName)
}
