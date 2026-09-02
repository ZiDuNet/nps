package web

import (
	"strings"
	"testing"
)

func TestEmbeddedProxyErrorPageIncludesBrandLinks(t *testing.T) {
	content, err := ReadStaticFile("page/error.html")
	if err != nil {
		t.Fatalf("read embedded proxy error page: %v", err)
	}
	page := string(content)
	for _, marker := range []string{
		"<meta name=\"viewport\"",
		"HTTP 404",
		"By：<a",
		"WuShuo",
		"https://github.com/ZiDuNet/nps",
		"prefers-reduced-motion",
	} {
		if !strings.Contains(page, marker) {
			t.Fatalf("proxy error page is missing %q", marker)
		}
	}
}
