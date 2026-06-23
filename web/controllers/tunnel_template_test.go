package controllers

import (
	"os"
	"strings"
	"testing"
)

func TestTunnelFormTemplatesExposeTypeSpecificFields(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{name: "add", path: "../views/index/add.html"},
		{name: "edit", path: "../views/index/edit.html"},
	}

	expected := map[string]string{
		"tcp":       `tcp: ["port", "target", "local_proxy", "client_id", "server_ip", "proto_version"]`,
		"udp":       `udp: ["port", "target", "local_proxy", "client_id", "server_ip"]`,
		"socks5":    `socks5: ["port", "client_id", "server_ip"]`,
		"httpProxy": `httpProxy: ["port", "client_id", "server_ip"]`,
		"secret":    `secret: ["target", "password", "client_id", "server_ip"]`,
		"p2p":       `p2p: ["target", "password", "client_id", "server_ip"]`,
		"file":      `file: ["port", "local_path", "strip_pre", "client_id", "server_ip"]`,
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := readTemplateForTest(t, tt.path)
			if !strings.Contains(content, `<option value="file" langtag="scheme-file"></option>`) {
				t.Fatalf("%s template does not expose file tunnel option", tt.name)
			}
			for tunnelType, fields := range expected {
				if !strings.Contains(content, fields) {
					t.Fatalf("%s template misses %s field map: %s", tt.name, tunnelType, fields)
				}
			}
		})
	}
}

func readTemplateForTest(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read template %s: %v", path, err)
	}
	return string(b)
}
