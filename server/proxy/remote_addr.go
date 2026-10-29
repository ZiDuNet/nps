package proxy

import (
	"crypto/subtle"
	"html"
	"net/http"
	"strings"

	"ehang.io/nps/lib/common"
	"github.com/astaxie/beego"
)

// effectiveHTTPRemoteAddr returns the source address used by HTTP access
// control.  It intentionally keeps the original TCP peer's port when no
// forwarded address is enabled; once an HTTP reverse proxy supplies the
// visitor address, the IP-only form is safer and more useful for ACLs and IP
// registrations than the proxy's connection port.
func effectiveHTTPRemoteAddr(r *http.Request) string {
	if r == nil {
		return ""
	}
	allowForwarded := beego.AppConfig.DefaultBool("allow_x_real_ip", true)
	if !allowForwarded {
		secret := strings.TrimSpace(beego.AppConfig.String("x_nps_http_only"))
		provided := strings.TrimSpace(r.Header.Get("X-NPS-Http-Only"))
		allowForwarded = secret != "" && provided != "" &&
			subtle.ConstantTimeCompare([]byte(html.UnescapeString(secret)), []byte(provided)) == 1
	}
	return common.EffectiveHTTPClientIP(
		r.RemoteAddr,
		r.Header.Get("X-Real-IP"),
		r.Header.Get("X-Forwarded-For"),
		allowForwarded,
	)
}
