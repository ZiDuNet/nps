package proxy

import (
	"html"

	"ehang.io/nps/lib/common"
)

// ipWhiteChallengeIP returns the peer IP in a form safe for interpolation into
// the HTML allowlist challenge page. Remote addresses are network input and
// must not be inserted into markup as-is.
func ipWhiteChallengeIP(remote string) string {
	return html.EscapeString(common.GetIpByAddr(remote))
}
