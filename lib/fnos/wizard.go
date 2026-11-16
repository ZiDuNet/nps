package fnos

import (
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"html"
	"net"
	"strconv"
)

//go:embed logo.png
var logoPNG []byte

func logoDataURI() string {
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(logoPNG)
}

type option struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

type item struct {
	Type      string   `json:"type"`
	Field     string   `json:"field,omitempty"`
	Label     string   `json:"label,omitempty"`
	InitValue string   `json:"initValue,omitempty"`
	HelpText  string   `json:"helpText,omitempty"`
	Options   []option `json:"options,omitempty"`
}

type step struct {
	Title string `json:"stepTitle"`
	Items []item `json:"items"`
}

// InstallWizard renders the install-time introduction only. Installation
// never collects connection details; configuration happens afterwards in the
// embedded Web UI launched from the fnOS desktop.
func InstallWizard() []byte {
	steps := []step{{
		Title: "NPC-Pro",
		Items: []item{
			{Type: "tips", HelpText: `<img src="` + logoDataURI() + `" alt="NPC-Pro" style="display:block;width:40px;height:40px;border-radius:10px;margin:4px auto 10px;">`},
			{Type: "tips", HelpText: "NPC-Pro 是 NPS-Pro 的客户端，用于将本机内网服务安全穿透到公网。"},
			{Type: "tips", HelpText: "在飞牛上支持同时管理多条连接与多个 VKey，每条连接独立运行、独立启停。"},
			{Type: "tips", HelpText: "支持 TCP、TCP+TLS、KCP、WS、WSS 和 QUIC 六种连接方式。"},
			{Type: "tips", HelpText: "安装完成后，从飞牛桌面打开「NPC-Pro」即可进入图形化配置界面；本应用不注册端口，也不开放额外的 Web 管理端口。"},
			{Type: "tips", HelpText: `项目地址：<a href="https://github.com/ZiDuNet/nps" target="_blank" rel="noopener">https://github.com/ZiDuNet/nps</a>`},
		},
	}}
	data, _ := json.MarshalIndent(steps, "", "  ")
	return data
}

// Wizard only contains public connection details. VKeys and quick codes are
// never embedded in appcenter metadata; empty password inputs mean retain.
// Existing connections each get a native step, followed by one Add step.
func Wizard(config Config, install bool, logTail ...string) ([]byte, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	steps := []step{{Title: "连接设置", Items: []item{{Type: "tips", HelpText: "在飞牛原生设置中管理全部 NPC 连接：无需打开网页，也不开放额外端口。每条连接独立运行，保存后自动生效；也可以直接编辑配置文件，向导保存不会覆盖手工修改的内容。"}}}}
	for index, entry := range config.Connections {
		name := entry.Command.Name
		if name == "" {
			name = entry.Command.Server
		}
		prefix := FieldPrefix(entry.ID, config.Revision)
		items := []item{
			{Type: "tips", HelpText: "连接 " + entry.ID + "：" + html.EscapeString(entry.Command.Server) + "（" + entry.Command.ProtocolName() + "）。在线状态请查看 NPS 服务端客户端列表或应用运行日志。"},
			{Type: "tips", HelpText: "配置文件：" + html.EscapeString(RelativePath(entry.ID)) + "（应用 etc 目录），支持手动编辑隧道、域名等规则；fnos_enabled=false 可保留文件并停用本条连接。"},
			{Type: "select", Field: prefix + "action", Label: "本条操作", InitValue: "keep", Options: []option{{"保持不变", "keep"}, {"保存修改（含启用/停用）", "update"}, {"删除此连接", "delete"}}},
		}
		base, quick, core, advanced := connectionFields(prefix, &entry)
		items = append(items, base...)
		items = append(items, quick...)
		items = append(items, core...)
		items = append(items, advanced...)
		items = append(items, item{Type: "radio", Field: prefix + "confirm_delete", Label: "删除确认", InitValue: "no", Options: []option{{"不删除", "no"}, {"确认删除此连接", "yes"}}})
		steps = append(steps, step{Title: strconv.Itoa(index+1) + ". " + html.EscapeString(name), Items: items})
	}
	prefix := FieldPrefix("new", config.Revision)
	initial := "skip"
	if install {
		initial = "add"
	}
	base, quick, core, advanced := connectionFields(prefix, nil)
	base = append([]item{{Type: "radio", Field: prefix + "action", Label: "新增连接", InitValue: initial, Options: []option{{"暂不新增", "skip"}, {"新增一条连接", "add"}}}}, base...)
	base = append(base, item{Type: "tips", HelpText: "保存后可重新打开应用设置继续添加。更新应用会保留全部连接配置；飞牛包与 Docker 不支持服务端远程自更新。"})
	quick = append([]item{{Type: "tips", HelpText: "仅在上方选择“快捷指令”时填写本页。粘贴 NPS 面板生成的完整快捷码即可，无需其他输入。"}}, quick...)
	core = append([]item{{Type: "tips", HelpText: "仅在上方选择“手动填写”时填写本页。"}}, core...)
	advanced = append([]item{{Type: "tips", HelpText: "按所选协议填写：WS/WSS 填路径，QUIC 填 ALPN，TLS/WSS/QUIC 可按需填 SNI、证书指纹或 CA；TCP 和 KCP 全部留空即可。"}}, advanced...)
	steps = append(steps,
		step{Title: "新增连接", Items: base},
		step{Title: "新增连接 · 快捷指令", Items: quick},
		step{Title: "新增连接 · 手动填写", Items: core},
		step{Title: "新增连接 · 协议参数", Items: advanced},
	)
	if !install {
		preview := "尚无运行日志。"
		if len(logTail) > 0 && logTail[0] != "" {
			preview = logTail[0]
		}
		steps = append(steps, step{Title: "运行日志", Items: []item{
			{Type: "tips", HelpText: "以下为最近运行日志（最多 500 条），已自动遮盖 VKey 等敏感信息；重新打开设置即可刷新。"},
			{Type: "tips", HelpText: "<pre>" + html.EscapeString(preview) + "</pre>"},
			{Type: "radio", Field: FieldPrefix("logs", config.Revision) + "action", Label: "日志操作", InitValue: "keep", Options: []option{{"保留日志", "keep"}, {"确认清空运行日志及三个轮转文件（不可恢复）", "clear"}}},
		}})
	}
	return json.MarshalIndent(steps, "", "  ")
}

func connectionFields(prefix string, entry *Entry) (base, quick, core, advanced []item) {
	mode, enabled, name, host, port, protocol, path, alpn, sni, fingerprint, ca := "quick", "true", "", "", "8024", "tcp", "/ws", "nps", "", "", ""
	if entry != nil {
		mode, enabled, name = entry.Mode, strconv.FormatBool(entry.Enabled), entry.Command.Name
		host, port, _ = net.SplitHostPort(entry.Command.Server)
		protocol = entry.Command.Transport
		if protocol == "tcp" && entry.Command.TLS {
			protocol = "tls"
		}
		if entry.Command.BridgePath != "" {
			path = entry.Command.BridgePath
		}
		if entry.Command.QuicALPN != "" {
			alpn = entry.Command.QuicALPN
		}
		sni, fingerprint, ca = entry.Command.TLSServerName, entry.Command.TLSFingerprint, entry.Command.TLSCAFile
	}
	base = []item{
		{Type: "text", Field: prefix + "name", Label: "连接备注", InitValue: name},
		{Type: "radio", Field: prefix + "enabled", Label: "是否启用", InitValue: enabled, Options: []option{{"启用，随应用启动", "true"}, {"停用，保留配置", "false"}}},
		{Type: "radio", Field: prefix + "mode", Label: "配置方式（二选一）", InitValue: mode, Options: []option{{"快捷指令", "quick"}, {"手动填写", "manual"}}},
	}
	quick = []item{
		{Type: "password", Field: prefix + "quick", Label: "快捷启动码", HelpText: "粘贴 NPS 面板生成的完整快捷码，不是 shell 命令。编辑已有快捷连接时留空保留原参数。"},
	}
	core = []item{
		{Type: "text", Field: prefix + "host", Label: "手动：服务端域名 / IPv4 / IPv6", InitValue: host},
		{Type: "text", Field: prefix + "port", Label: "手动：服务端端口", InitValue: port},
		{Type: "password", Field: prefix + "vkey", Label: "手动：VKey", HelpText: "密钥不回显；编辑已有连接时留空保留原 VKey。"},
		{Type: "select", Field: prefix + "protocol", Label: "手动：连接协议", InitValue: protocol, Options: []option{{"TCP（默认）", "tcp"}, {"TCP + TLS", "tls"}, {"KCP（UDP）", "kcp"}, {"WS", "ws"}, {"WSS", "wss"}, {"QUIC（UDP + TLS）", "quic"}}},
	}
	// fnOS wizards are static, so irrelevant fields are hidden per current
	// protocol instead of relying on dynamic form linkage. Blank values for
	// hidden fields are safe: Apply clears transport-specific options that do
	// not match the chosen protocol.
	if protocol == "ws" || protocol == "wss" {
		advanced = append(advanced, item{Type: "text", Field: prefix + "path", Label: "手动：WS / WSS 路径", InitValue: path})
	}
	if protocol == "quic" {
		advanced = append(advanced, item{Type: "text", Field: prefix + "alpn", Label: "手动：QUIC ALPN", InitValue: alpn})
	}
	if protocol == "tls" || protocol == "wss" || protocol == "quic" {
		advanced = append(advanced,
			item{Type: "text", Field: prefix + "sni", Label: "手动：TLS ServerName（可选）", InitValue: sni},
			item{Type: "text", Field: prefix + "fingerprint", Label: "手动：TLS 证书指纹（可选）", InitValue: fingerprint},
			item{Type: "text", Field: prefix + "ca", Label: "手动：自有 CA 文件路径（可选）", InitValue: ca},
		)
	}
	return base, quick, core, advanced
}
