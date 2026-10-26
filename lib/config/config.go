package config

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/file"
)

type CommonConfig struct {
	Server                string
	VKey                  string
	Tp                    string // Bridge transport: tcp, kcp, ws, or wss
	BridgePath            string
	QuicALPN              string
	AutoReconnection      bool
	TlsEnable             bool
	TLSCAFile             string
	TLSServerName         string
	TLSFingerprint        string
	TLSInsecureSkipVerify bool
	ProxyUrl              string
	Client                *file.Client
	DisconnectTime        int
	LocalIP               string
	LocalIPForward        bool
	DnsServer             string
	NtpServer             string
	NtpInterval           int
	FallbackSecret        bool
	SecretLink            bool
	DisableRemoteUpdate   bool
}

type LocalServer struct {
	Type       string
	Port       int
	Ip         string
	Password   string
	Target     string
	TargetType string
	Fallback   bool
	LocalProxy bool
}

type Config struct {
	content      string
	title        []string
	CommonConfig *CommonConfig
	Hosts        []*file.Host
	Tasks        []*file.Tunnel
	Healths      []*file.Health
	LocalServer  []*LocalServer
}

func NewConfig(path string) (c *Config, err error) {
	c = new(Config)
	var b []byte
	if b, err = common.ReadAllFromFile(path); err != nil {
		return
	} else {
		if c.content, err = common.ParseStr(string(b)); err != nil {
			return nil, err
		}
		if c.title, err = getAllTitle(c.content); err != nil {
			return
		}
		var nowIndex int
		var nextIndex int
		var nowContent string
		for i := 0; i < len(c.title); i++ {
			nowIndex = strings.Index(c.content, c.title[i]) + len(c.title[i])
			if i < len(c.title)-1 {
				nextIndex = strings.Index(c.content, c.title[i+1])
			} else {
				nextIndex = len(c.content)
			}
			nowContent = c.content[nowIndex:nextIndex]

			if strings.Index(getTitleContent(c.title[i]), "secret") == 0 && !strings.Contains(nowContent, "mode") {
				local := delLocalService(nowContent)
				local.Type = "secret"
				c.LocalServer = append(c.LocalServer, local)
				continue
			}
			//except mode
			if strings.Index(getTitleContent(c.title[i]), "p2p") == 0 && !strings.Contains(nowContent, "mode") {
				local := delLocalService(nowContent)
				local.Type = "p2p"
				c.LocalServer = append(c.LocalServer, local)
				continue
			}
			//health set
			if strings.Index(getTitleContent(c.title[i]), "health") == 0 {
				c.Healths = append(c.Healths, dealHealth(nowContent))
				continue
			}
			switch c.title[i] {
			case "[common]":
				c.CommonConfig = dealCommon(nowContent)
				if c.CommonConfig.Client == nil {
					c.CommonConfig.Client = file.NewClient("", true, true)
					c.CommonConfig.Client.Cnf = new(file.Config)
				}
			default:
				if hasConfigKey(nowContent, "host") {
					h := dealHost(nowContent)
					h.Remark = getTitleContent(c.title[i])
					c.Hosts = append(c.Hosts, h)
				} else {
					t := dealTunnel(nowContent)
					t.Remark = getTitleContent(c.title[i])
					c.Tasks = append(c.Tasks, t)
				}
			}
		}
	}
	return
}

// hasConfigKey checks actual key/value lines and ignores comments. A plain
// substring search can classify a tunnel as a Host rule when a comment
// happens to contain the word "host".
func hasConfigKey(content, key string) bool {
	for _, line := range splitStr(content) {
		if !strings.Contains(line, "=") {
			continue
		}
		parsedKey, _, ok := splitKV(line)
		if ok && parsedKey == key {
			return true
		}
	}
	return false
}

// splitKV parses one active key=value line. Values are split at the first '='
// only, so credentials, URLs, and header values may contain '='. A bare key
// keeps the legacy empty-value behavior used by multi-account files.
// Comment and blank lines are ignored consistently across all section parsers.
func splitKV(line string) (key, value string, ok bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
		return "", "", false
	}
	key, value, _ = strings.Cut(line, "=")
	key = strings.TrimSpace(key)
	if key == "" {
		return "", "", false
	}
	return key, strings.TrimSpace(value), true
}

func ensureClientConfig(c *CommonConfig) *file.Client {
	if c.Client == nil {
		c.Client = file.NewClient("", true, true)
		c.Client.Cnf = new(file.Config)
	}
	if c.Client.Cnf == nil {
		c.Client.Cnf = new(file.Config)
	}
	return c.Client
}

// parseProxyProtocol accepts both numeric values and the historical string
// form. Unknown values safely disable it.
func parseProxyProtocol(value string) string {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "1", "V1":
		return "V1"
	case "2", "V2":
		return "V2"
	default:
		return ""
	}
}

func parseProxyProtocolValue(value string) int {
	switch parseProxyProtocol(value) {
	case "V1":
		return 1
	case "V2":
		return 2
	default:
		return 0
	}
}

func getTitleContent(s string) string {
	re, _ := regexp.Compile(`[\[\]]`)
	return re.ReplaceAllString(s, "")
}

func dealCommon(s string) *CommonConfig {
	c := &CommonConfig{}
	for _, v := range splitStr(s) {
		key, value, ok := splitKV(v)
		if !ok {
			continue
		}
		switch key {
		case "server_addr", "server":
			c.Server = value
		case "vkey":
			c.VKey = value
		case "conn_type", "tp":
			c.Tp = value
		case "bridge_path":
			c.BridgePath = value
		case "quic_alpn":
			c.QuicALPN = value
		case "auto_reconnection":
			c.AutoReconnection = common.GetBoolByStr(value)
		case "basic_username":
			ensureClientConfig(c).Cnf.U = value
		case "basic_password":
			ensureClientConfig(c).Cnf.P = value
		case "web_password":
			ensureClientConfig(c).WebPassword = value
		case "web_username":
			ensureClientConfig(c).WebUserName = value
		case "compress":
			ensureClientConfig(c).Cnf.Compress = common.GetBoolByStr(value)
		case "crypt":
			ensureClientConfig(c).Cnf.Crypt = common.GetBoolByStr(value)
		case "proxy_url":
			c.ProxyUrl = value
		case "local_ip":
			c.LocalIP = value
		case "local_ip_forward":
			c.LocalIPForward = common.GetBoolByStr(value)
		case "dns_server":
			c.DnsServer = value
		case "ntp_server":
			c.NtpServer = value
		case "ntp_interval":
			c.NtpInterval = common.GetIntNoErrByStr(value)
		case "fallback_secret":
			c.FallbackSecret = common.GetBoolByStr(value)
		case "secret_link":
			c.SecretLink = common.GetBoolByStr(value)
		case "disable_remote_update":
			c.DisableRemoteUpdate = common.GetBoolByStr(value)
		case "rate_limit":
			ensureClientConfig(c).RateLimit = common.GetIntNoErrByStr(value)
		case "flow_limit":
			client := ensureClientConfig(c)
			if client.Flow == nil {
				client.Flow = file.NewFlow()
			}
			client.Flow.SetLimit(int64(common.GetIntNoErrByStr(value)))
		case "max_conn":
			ensureClientConfig(c).MaxConn = common.GetIntNoErrByStr(value)
		case "remark":
			ensureClientConfig(c).Remark = value
		case "pprof_addr":
			common.InitPProfFromArg(value)
		case "disconnect_timeout":
			c.DisconnectTime = common.GetIntNoErrByStr(value)
		case "tls_enable":
			c.TlsEnable = common.GetBoolByStr(value)
		case "tls_ca_file":
			c.TLSCAFile = value
		case "tls_server_name":
			c.TLSServerName = value
		case "tls_fingerprint":
			c.TLSFingerprint = value
		case "tls_insecure_skip_verify":
			c.TLSInsecureSkipVerify = common.GetBoolByStr(value)
		}
	}
	return c
}

func dealHost(s string) *file.Host {
	h := &file.Host{}
	h.Target = new(file.Target)
	h.Scheme = "all"
	var headerChange, responseHeaderChange string
	for _, v := range splitStr(s) {
		key, value, ok := splitKV(v)
		if !ok {
			continue
		}
		switch key {
		case "host":
			h.Host = value
		case "upload_limit":
			h.UploadLimit = max(common.GetIntNoErrByStr(value), 0)
		case "download_limit":
			h.DownloadLimit = max(common.GetIntNoErrByStr(value), 0)
		case "target_addr":
			h.Target.TargetStr = strings.ReplaceAll(value, ",", "\n")
		case "proxy_protocol":
			h.Target.ProxyProtocol = parseProxyProtocolValue(value)
		case "host_change":
			h.HostChange = value
		case "scheme":
			h.Scheme = value
		case "location":
			h.Location = value
		case "path_rewrite":
			h.PathRewrite = value
		case "redirect_url":
			h.RedirectURL = value
		case "auto_https":
			h.AutoHttps = common.GetBoolByStr(value)
		case "auto_cors":
			h.AutoCORS = common.GetBoolByStr(value)
		case "compat_mode":
			h.CompatMode = common.GetBoolByStr(value)
		case "target_is_https":
			h.TargetIsHTTPS = common.GetBoolByStr(value)
		case "https_just_proxy":
			h.HTTPSJustProxy = common.GetBoolByStr(value)
		case "auto_ssl":
			h.AutoSSL = common.GetBoolByStr(value)
		case "tls_offload":
			h.TLSOffload = common.GetBoolByStr(value)
		case "auth":
			h.Auth = value
		case "multi_account":
			if common.FileExists(value) {
				if b, err := common.ReadAllFromFile(value); err != nil {
					panic(err)
				} else if content, err := common.ParseStr(string(b)); err != nil {
					panic(err)
				} else {
					accounts := dealMultiUser(content)
					usernames := make([]string, 0, len(accounts))
					for username := range accounts {
						usernames = append(usernames, username)
					}
					sort.Strings(usernames)
					lines := make([]string, 0, len(usernames))
					for _, username := range usernames {
						lines = append(lines, username+":"+accounts[username])
					}
					h.Auth = strings.Join(lines, "\n")
				}
			}
		default:
			if name, ok := strings.CutPrefix(key, "header_"); ok && name != "" {
				headerChange += name + ":" + value + "\n"
			}
			if name, ok := strings.CutPrefix(key, "response_"); ok && name != "" {
				responseHeaderChange += name + ":" + value + "\n"
			}
			h.HeaderChange = headerChange
			h.ResponseHeaderChange = responseHeaderChange
		}
	}
	return h
}

func dealHealth(s string) *file.Health {
	h := &file.Health{}
	for _, v := range splitStr(s) {
		key, value, ok := splitKV(v)
		if !ok {
			continue
		}
		switch key {
		case "health_check_timeout":
			h.HealthCheckTimeout = common.GetIntNoErrByStr(value)
		case "health_check_max_failed":
			h.HealthMaxFail = common.GetIntNoErrByStr(value)
		case "health_check_interval":
			h.HealthCheckInterval = common.GetIntNoErrByStr(value)
		case "health_http_url":
			h.HttpHealthUrl = value
		case "health_check_type":
			h.HealthCheckType = value
		case "health_check_target":
			h.HealthCheckTarget = strings.ReplaceAll(value, ",", "\n")
		}
	}
	return h
}

func dealTunnel(s string) *file.Tunnel {
	t := &file.Tunnel{}
	t.Target = new(file.Target)
	for _, v := range splitStr(s) {
		key, value, ok := splitKV(v)
		if !ok {
			continue
		}
		switch key {
		case "upload_limit":
			t.UploadLimit = max(common.GetIntNoErrByStr(value), 0)
		case "download_limit":
			t.DownloadLimit = max(common.GetIntNoErrByStr(value), 0)
		case "server_port":
			t.Ports = value
		case "server_ip":
			t.ServerIp = value
		case "mode":
			t.Mode = value
		case "target_addr":
			t.Target.TargetStr = strings.ReplaceAll(value, ",", "\n")
		case "target_port":
			t.Target.TargetStr = value
		case "target_ip":
			t.TargetAddr = value
		case "proxy_protocol":
			t.ProtoVersion = parseProxyProtocol(value)
		case "proto_version":
			t.ProtoVersion = parseProxyProtocol(value)
		case "http_proxy":
			t.HttpProxy = common.GetBoolByStr(value)
		case "socks5_proxy":
			t.Socks5Proxy = common.GetBoolByStr(value)
		case "target_type":
			t.TargetType = strings.ToLower(strings.TrimSpace(value))
		case "dest_acl_mode":
			t.DestAclMode = common.GetIntNoErrByStr(value)
		case "dest_acl_rules":
			t.DestAclRules = strings.ReplaceAll(value, ",", "\n")
		case "password":
			t.Password = value
		case "local_path":
			t.LocalPath = value
		case "strip_pre":
			t.StripPre = value
		case "multi_account":
			t.MultiAccount = &file.MultiAccount{}
			if common.FileExists(value) {
				if b, err := common.ReadAllFromFile(value); err != nil {
					panic(err)
				} else {
					if content, err := common.ParseStr(string(b)); err != nil {
						panic(err)
					} else {
						t.MultiAccount.AccountMap = dealMultiUser(content)
					}
				}
			}
		}
	}
	return t

}

func dealMultiUser(s string) map[string]string {
	multiUserMap := make(map[string]string)
	for _, v := range splitStr(s) {
		key, value, ok := splitKV(v)
		if !ok {
			continue
		}
		multiUserMap[key] = value
	}
	return multiUserMap
}

func delLocalService(s string) *LocalServer {
	l := new(LocalServer)
	for _, v := range splitStr(s) {
		key, value, ok := splitKV(v)
		if !ok {
			continue
		}
		switch key {
		case "local_port":
			l.Port = common.GetIntNoErrByStr(value)
		case "local_ip":
			l.Ip = value
		case "password":
			l.Password = value
		case "target_addr":
			l.Target = value
		case "target_type":
			l.TargetType = value
		case "fallback_secret":
			l.Fallback = common.GetBoolByStr(value)
		case "local_proxy":
			l.LocalProxy = common.GetBoolByStr(value)
		}
	}
	return l
}

func getAllTitle(content string) (arr []string, err error) {
	var re *regexp.Regexp
	re, err = regexp.Compile(`(?m)^\[[^\[\]\r\n]+\]`)
	if err != nil {
		return
	}
	arr = re.FindAllString(content, -1)
	m := make(map[string]bool)
	for _, v := range arr {
		if _, ok := m[v]; ok {
			err = errors.New(fmt.Sprintf("Item names %s are not allowed to be duplicated", v))
			return
		}
		m[v] = true
	}
	return
}

func splitStr(s string) (configDataArr []string) {
	if common.IsWindows() {
		configDataArr = strings.Split(s, "\r\n")
	}
	if len(configDataArr) < 3 {
		configDataArr = strings.Split(s, "\n")
	}
	return
}
