package config

import (
	"fmt"
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

func TestNewConfigParsesResourceRateLimits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "npc.conf")
	content := `[common]
server_addr=127.0.0.1:8024
vkey=replace-with-verify-key

[tcp_api]
mode=tcp
target_addr=127.0.0.1:8080
server_port=10080
upload_limit=256
download_limit=2048

[web_api]
host=api.example.com
target_addr=127.0.0.1:8081
upload_limit=64
download_limit=512
`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write test config: %v", err)
	}
	c, err := NewConfig(path)
	if err != nil {
		t.Fatalf("parse test config: %v", err)
	}
	if len(c.Tasks) != 1 || c.Tasks[0].UploadLimit != 256 || c.Tasks[0].DownloadLimit != 2048 {
		t.Fatalf("task rate limits were not parsed: %#v", c.Tasks)
	}
	if len(c.Hosts) != 1 || c.Hosts[0].UploadLimit != 64 || c.Hosts[0].DownloadLimit != 512 {
		t.Fatalf("host rate limits were not parsed: %#v", c.Hosts)
	}
}

func TestSplitKVPreservesEqualsInValue(t *testing.T) {
	key, value, ok := splitKV("  basic_password = part-one=part-two  ")
	if !ok || key != "basic_password" || value != "part-one=part-two" {
		t.Fatalf("splitKV() = (%q, %q, %v), want key and full value", key, value, ok)
	}
	for _, line := range []string{"", "  ", "# key=value", "; key=value"} {
		if _, _, ok := splitKV(line); ok {
			t.Errorf("splitKV(%q) unexpectedly accepted a comment or malformed line", line)
		}
	}
	if key, value, ok := splitKV("username"); !ok || key != "username" || value != "" {
		t.Fatalf("splitKV() lost legacy bare-key support: (%q, %q, %v)", key, value, ok)
	}
}

func TestNewConfigParsesLegacyCompatibleHostAndProxyFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "npc.conf")
	content := `[common]
server_addr=127.0.0.1:8024
vkey=secret=with=equals
basic_password=client=pass
proxy_url=http://user:pass=a@127.0.0.1:7890

[web_api]
host=api.example.com
target_addr=127.0.0.1:8080
proxy_protocol=2
path_rewrite=/api => /v1/api
redirect_url=https://example.com/api?token=a=b
auto_cors=true
compat_mode=true
header_X-Token=token=a=b
response_Cache-Control=no-cache=private

[tcp_ssh]
mode=tcp
target_addr=127.0.0.1:22
server_port=10022
proxy_protocol=1
`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write test config: %v", err)
	}
	c, err := NewConfig(path)
	if err != nil {
		t.Fatalf("parse test config: %v", err)
	}
	if c.CommonConfig.VKey != "secret=with=equals" || c.CommonConfig.ProxyUrl != "http://user:pass=a@127.0.0.1:7890" {
		t.Fatalf("common values containing '=' were truncated: %+v", c.CommonConfig)
	}
	if c.CommonConfig.Client == nil || c.CommonConfig.Client.Cnf.P != "client=pass" {
		t.Fatalf("client password containing '=' was not parsed: %#v", c.CommonConfig.Client)
	}
	if len(c.Hosts) != 1 {
		t.Fatalf("expected one Host, got %d", len(c.Hosts))
	}
	host := c.Hosts[0]
	if host.Target.ProxyProtocol != 2 || host.PathRewrite != "/api => /v1/api" || host.RedirectURL != "https://example.com/api?token=a=b" {
		t.Fatalf("Host proxy options were not parsed: %+v", host)
	}
	if !host.AutoCORS || !host.CompatMode || host.HeaderChange != "X-Token:token=a=b\n" || host.ResponseHeaderChange != "Cache-Control:no-cache=private\n" {
		t.Fatalf("Host header/compatibility options were not parsed: %+v", host)
	}
	if len(c.Tasks) != 1 || c.Tasks[0].ProtoVersion != "V1" {
		t.Fatalf("numeric TCP proxy_protocol was not mapped to legacy ProtoVersion: %#v", c.Tasks)
	}
}

func TestParseProxyProtocolInvalidValueDisablesFeature(t *testing.T) {
	for _, value := range []string{"", "0", "3", "v3", "invalid"} {
		if got := parseProxyProtocol(value); got != "" {
			t.Errorf("parseProxyProtocol(%q) = %q, want disabled", value, got)
		}
	}
}

func TestDealHostLoadsMultiAccountFile(t *testing.T) {
	dir := t.TempDir()
	accountPath := filepath.Join(dir, "host-users.conf")
	if err := os.WriteFile(accountPath, []byte("bob=second=pass\nalice=first:pass\n"), 0600); err != nil {
		t.Fatalf("write account file: %v", err)
	}
	host := dealHost(fmt.Sprintf("host=secure.example.com\nmulti_account=%s", accountPath))
	if got, want := host.Auth, "alice:first:pass\nbob:second=pass"; got != want {
		t.Fatalf("Host Auth = %q, want %q", got, want)
	}
}

func TestDealTunnelParsesMixProxyAndDestinationACL(t *testing.T) {
	tunnel := dealTunnel(`mode=mixProxy
target_addr=127.0.0.1:8080
http_proxy=true
socks5_proxy=false
dest_acl_mode=1
dest_acl_rules=full:api.example.com,10.0.0.0/8`)
	if tunnel.Mode != "mixProxy" || !tunnel.HttpProxy || tunnel.Socks5Proxy {
		t.Fatalf("mixed proxy options were not parsed: %+v", tunnel)
	}
	if tunnel.DestAclMode != 1 || tunnel.DestAclRules != "full:api.example.com\n10.0.0.0/8" {
		t.Fatalf("destination ACL was not parsed: mode=%d rules=%q", tunnel.DestAclMode, tunnel.DestAclRules)
	}
}
