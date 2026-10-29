// Package quickcommand parses and generates NPC quick-start commands.
//
// The historical text format remains supported:
//
//	Base64("nps:name|host:port|vkey|tls[|sha256-fingerprint]")
//
// Transports that cannot be represented by the historical format use the
// versioned JSON format:
//
//	Base64("nps2:" + JSON)
package quickcommand

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

const (
	LegacyPrefix = "nps:"
	JSONPrefix   = "nps2:"
	Version      = 2
)

// Command is the complete connection intent carried by a v2 quick command.
// V1 commands are normalized into the same structure.
type Command struct {
	V                     int    `json:"v,omitempty"`
	Name                  string `json:"name,omitempty"`
	Server                string `json:"server"`
	VKey                  string `json:"vkey"`
	Endpoint              string `json:"endpoint,omitempty"`
	Transport             string `json:"transport,omitempty"`
	TLS                   bool   `json:"tls,omitempty"`
	BridgePath            string `json:"bridge_path,omitempty"`
	QuicALPN              string `json:"quic_alpn,omitempty"`
	TLSServerName         string `json:"tls_server_name,omitempty"`
	TLSFingerprint        string `json:"tls_fingerprint,omitempty"`
	TLSCAFile             string `json:"tls_ca_file,omitempty"`
	TLSInsecureSkipVerify bool   `json:"tls_insecure_skip_verify,omitempty"`
}

// Normalize validates a command and expands compact transport aliases.
func Normalize(command Command) (Command, error) {
	command.Name = strings.TrimSpace(command.Name)
	command.Server = strings.TrimSpace(command.Server)
	command.VKey = strings.TrimSpace(command.VKey)
	command.Endpoint = strings.ToLower(strings.TrimSpace(command.Endpoint))
	command.Transport = strings.ToLower(strings.TrimSpace(command.Transport))
	command.BridgePath = strings.TrimSpace(command.BridgePath)
	command.QuicALPN = strings.TrimSpace(command.QuicALPN)
	command.TLSServerName = strings.TrimSpace(command.TLSServerName)
	command.TLSFingerprint = strings.TrimSpace(command.TLSFingerprint)
	command.TLSCAFile = strings.TrimSpace(command.TLSCAFile)
	if command.Server == "" {
		return Command{}, errors.New("服务端地址不能为空")
	}
	if command.VKey == "" {
		return Command{}, errors.New("客户端 VKey 不能为空")
	}
	if command.Transport == "" {
		command.Transport = "tcp"
	}
	switch command.Transport {
	case "tls", "tcp+tls", "tcp-tls":
		command.Transport = "tcp"
		command.TLS = true
	case "tcp", "kcp", "ws", "wss", "quic":
	default:
		return Command{}, fmt.Errorf("不支持的 Bridge 协议 %q", command.Transport)
	}
	if command.Transport == "wss" {
		command.TLS = true
	}
	if command.Transport == "ws" || command.Transport == "wss" {
		if command.BridgePath == "" {
			command.BridgePath = "/ws"
		}
		parsed, err := url.ParseRequestURI(command.BridgePath)
		if err != nil || !strings.HasPrefix(command.BridgePath, "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
			return Command{}, fmt.Errorf("Bridge 路径 %q 无效，需要以 / 开头且不能包含查询参数", command.BridgePath)
		}
	}
	if command.Transport == "quic" && command.QuicALPN == "" {
		command.QuicALPN = "nps"
	}
	switch command.Endpoint {
	case "", "tcp", "tls", "kcp", "ws", "wss", "quic", "http3":
	default:
		return Command{}, fmt.Errorf("不支持的 Bridge 入口 %q", command.Endpoint)
	}
	command.V = Version
	return command, nil
}

// Encode returns a v1 command when every option can be represented by the
// historical format, otherwise it returns a v2 JSON command.
func Encode(command Command) (string, error) {
	command, err := Normalize(command)
	if err != nil {
		return "", err
	}
	if command.Transport == "tcp" &&
		command.BridgePath == "" &&
		command.QuicALPN == "" &&
		command.TLSServerName == "" &&
		command.TLSCAFile == "" &&
		!command.TLSInsecureSkipVerify {
		return EncodeLegacy(command.Name, command.Server, command.VKey, command.TLS, command.TLSFingerprint), nil
	}
	return EncodeV2(command)
}

// EncodeLegacy generates the permanent v1 format used by existing NPCs.
func EncodeLegacy(name, server, vkey string, tlsEnabled bool, fingerprint ...string) string {
	parts := []string{
		strings.TrimSpace(name),
		strings.TrimSpace(server),
		strings.TrimSpace(vkey),
		fmt.Sprintf("%t", tlsEnabled),
	}
	if len(fingerprint) > 0 && strings.TrimSpace(fingerprint[0]) != "" {
		parts = append(parts, strings.TrimSpace(fingerprint[0]))
	}
	payload := LegacyPrefix + strings.Join(parts, "|")
	return base64.StdEncoding.EncodeToString([]byte(payload))
}

// EncodeV2 generates the JSON-based format understood by current NPC and GUI
// releases.
func EncodeV2(command Command) (string, error) {
	command, err := Normalize(command)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(command)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(append([]byte(JSONPrefix), payload...)), nil
}

// DecodeBase64 decodes both supported quick-command formats.
func DecodeBase64(encoded string) (Command, error) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return Command{}, errors.New("快捷启动命令不能为空")
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(encoded)
	}
	if err != nil {
		return Command{}, fmt.Errorf("快捷启动命令 Base64 解码失败: %w", err)
	}
	return DecodePayload(string(decoded))
}

// DecodePayload decodes a command payload that has already been Base64
// decoded. Plain payloads are accepted to keep diagnostics and tests simple.
func DecodePayload(payload string) (Command, error) {
	payload = strings.TrimSpace(payload)
	switch {
	case strings.HasPrefix(payload, JSONPrefix):
		var command Command
		if err := json.Unmarshal([]byte(strings.TrimPrefix(payload, JSONPrefix)), &command); err != nil {
			return Command{}, fmt.Errorf("快捷启动命令 JSON 无效: %w", err)
		}
		if command.V != 0 && command.V != Version {
			return Command{}, fmt.Errorf("不支持的快捷启动命令版本 %d", command.V)
		}
		return Normalize(command)
	case strings.HasPrefix(payload, LegacyPrefix):
		parts := strings.Split(strings.TrimPrefix(payload, LegacyPrefix), "|")
		if len(parts) < 4 || len(parts) > 5 {
			return Command{}, errors.New("快捷启动命令格式错误，请检查")
		}
		command := Command{
			Name:      parts[0],
			Server:    parts[1],
			VKey:      parts[2],
			Transport: "tcp",
			TLS:       strings.EqualFold(strings.TrimSpace(parts[3]), "true"),
		}
		if len(parts) == 5 {
			command.TLSFingerprint = parts[4]
		}
		return Normalize(command)
	case strings.HasPrefix(payload, "nps "):
		return DecodePayload(LegacyPrefix + strings.TrimPrefix(payload, "nps "))
	default:
		return Command{}, errors.New("快捷启动命令格式错误，请检查")
	}
}

// Args returns the NPC flags represented by command. The executable name is
// intentionally excluded so CLI, GUI and tests can share this method.
func (command Command) Args() []string {
	command, err := Normalize(command)
	if err != nil {
		return nil
	}
	args := []string{
		"-server=" + command.Server,
		"-vkey=" + command.VKey,
		"-type=" + command.Transport,
	}
	if command.TLS {
		args = append(args, "-tls_enable=true")
	}
	if command.BridgePath != "" {
		args = append(args, "-bridge_path="+command.BridgePath)
	}
	if command.QuicALPN != "" {
		args = append(args, "-quic_alpn="+command.QuicALPN)
	}
	if command.TLSServerName != "" {
		args = append(args, "-tls_server_name="+command.TLSServerName)
	}
	if command.TLSFingerprint != "" {
		args = append(args, "-tls_fingerprint="+command.TLSFingerprint)
	}
	if command.TLSCAFile != "" {
		args = append(args, "-tls_ca_file="+command.TLSCAFile)
	}
	if command.TLSInsecureSkipVerify {
		args = append(args, "-tls_insecure_skip_verify=true")
	}
	return args
}

// CommandLine renders a copyable NPC invocation.
func (command Command) CommandLine(executable string) string {
	executable = strings.TrimSpace(executable)
	if executable == "" {
		executable = "npc"
	}
	return strings.Join(append([]string{executable}, command.Args()...), " ")
}

// ProtocolName returns the human-readable protocol used by management UI.
func (command Command) ProtocolName() string {
	command, err := Normalize(command)
	if err != nil {
		return ""
	}
	switch command.Transport {
	case "tcp":
		if command.TLS {
			return "TCP + TLS"
		}
		return "TCP"
	case "kcp":
		return "KCP"
	case "ws":
		return "WS"
	case "wss":
		return "WSS"
	case "quic":
		return "QUIC"
	default:
		return strings.ToUpper(command.Transport)
	}
}

// ServiceSuffix returns the stable service-name suffix for a transport. Plain
// TCP keeps the historical name; all other transports receive a suffix.
func (command Command) ServiceSuffix() string {
	command, err := Normalize(command)
	if err != nil {
		return "invalid"
	}
	if command.Endpoint == "http3" {
		return "http3"
	}
	if command.Transport == "tcp" && !command.TLS {
		return ""
	}
	if command.Transport == "tcp" && command.TLS {
		return "tls"
	}
	return command.Transport
}
