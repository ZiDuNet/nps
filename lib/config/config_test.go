package config

import (
	"log"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestReg(t *testing.T) {
	content := `
[common]
server=127.0.0.1:8284
tp=tcp
vkey=123
[web2]
host=www.baidu.com
host_change=www.sina.com
target=127.0.0.1:8080,127.0.0.1:8082
header_cookkile=122123
header_user-Agent=122123
[web2]
host=www.baidu.com
host_change=www.sina.com
target=127.0.0.1:8080,127.0.0.1:8082
header_cookkile="122123"
header_user-Agent=122123
[tunnel1]
type=udp
target=127.0.0.1:8080
port=9001
compress=snappy
crypt=true
u=1
p=2
[tunnel2]
type=tcp
target=127.0.0.1:8080
port=9001
compress=snappy
crypt=true
u=1
p=2
`
	re, err := regexp.Compile(`\[.+?\]`)
	if err != nil {
		t.Fail()
	}
	log.Println(re.FindAllString(content, -1))
}

func TestDealCommon(t *testing.T) {
	s := `server=127.0.0.1:8284
tp=tcp
vkey=123`
	f := new(CommonConfig)
	f.Server = "127.0.0.1:8284"
	f.Tp = "tcp"
	f.VKey = "123"
	if c := dealCommon(s); *c != *f {
		t.Fail()
	}
}

func TestGetTitleContent(t *testing.T) {
	s := "[common]"
	if getTitleContent(s) != "common" {
		t.Fail()
	}
}

func TestDealCommonParsesTLSVerificationOptions(t *testing.T) {
	c := dealCommon(`server=127.0.0.1:8025
tls_enable=true
tls_ca_file=/etc/nps/ca.pem
tls_server_name=bridge.example.com
tls_fingerprint=sha256:AA
tls_insecure_skip_verify=true`)
	if c == nil || !c.TlsEnable || c.TLSCAFile != "/etc/nps/ca.pem" || c.TLSServerName != "bridge.example.com" || c.TLSFingerprint != "sha256:AA" || !c.TLSInsecureSkipVerify {
		t.Fatalf("TLS options were not parsed: %#v", c)
	}
}

func TestHasConfigKeyIgnoresComments(t *testing.T) {
	content := `# host=comment.example
; host=another-comment.example
mode=tcp
target_addr=127.0.0.1:22`
	if hasConfigKey(content, "host") {
		t.Fatal("commented host key should not classify a tunnel as a Host rule")
	}
	if !hasConfigKey("host = app.example.com\n", "host") {
		t.Fatal("active host key was not detected")
	}
}

func TestNewConfigKeepsCommentedHostInTunnel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "npc.conf")
	content := `[common]
server_addr=127.0.0.1:8024
vkey=replace-with-verify-key

[tcp_ssh]
# host=comment.example.com
mode=tcp
target_addr=127.0.0.1:22
server_port=10022
`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write test config: %v", err)
	}
	c, err := NewConfig(path)
	if err != nil {
		t.Fatalf("parse test config: %v", err)
	}
	if len(c.Hosts) != 0 || len(c.Tasks) != 1 {
		t.Fatalf("commented host should remain a tunnel: hosts=%d tasks=%d", len(c.Hosts), len(c.Tasks))
	}
}
