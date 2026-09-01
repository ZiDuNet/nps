package controllers

import (
	"os"
	"strings"
	"testing"
)

func TestLanguageSubmitUsesSameOriginReturnUrl(t *testing.T) {
	content, err := os.ReadFile("../static/js/language.js")
	if err != nil {
		t.Fatalf("read language script: %v", err)
	}
	script := string(content)
	if !strings.Contains(script, "function npsSafeReturnUrl(referrer)") {
		t.Fatal("language script must validate referrer URLs")
	}
	if strings.Contains(script, "window.location.href = document.referrer") {
		t.Fatal("language script must not redirect directly to an arbitrary referrer")
	}
}
