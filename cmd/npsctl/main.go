// Command npsctl is the NPS Pro control-plane CLI. It talks to a running NPS
// server through the versioned /api/v1 surface using either an administrator
// auth_key signature or a user API bearer token.
package main

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"ehang.io/nps/lib/version"
)

type client struct {
	server string
	token  string
	key    string
	http   *http.Client
	out    io.Writer
}

type envelope struct {
	Status    int             `json:"status"`
	Code      string          `json:"code"`
	Msg       string          `json:"msg"`
	ErrorCode string          `json:"error_code"`
	Data      json.RawMessage `json:"data"`
	Rows      json.RawMessage `json:"rows"`
	Total     json.RawMessage `json:"total"`
	Meta      json.RawMessage `json:"meta"`
	RequestID string          `json:"request_id"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "npsctl:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("npsctl", flag.ContinueOnError)
	fs.Usage = func() {}
	var (
		server      = fs.String("server", envOr("NPS_SERVER", "http://127.0.0.1:8081"), "NPS server base URL (env NPS_SERVER)")
		token       = fs.String("token", os.Getenv("NPS_TOKEN"), "user API bearer token npsu_... (env NPS_TOKEN)")
		key         = fs.String("key", os.Getenv("NPS_KEY"), "administrator auth_key or managed npsa_ key (env NPS_KEY)")
		limit       = fs.Int("limit", 0, "max rows for list commands")
		data        = fs.String("data", "", "JSON body for create/update commands")
		showVersion = fs.Bool("version", false, "print version and exit")
	)
	fs.SetOutput(os.Stderr)
	globalArgs, restArgs := splitGlobalArgs(args)
	if err := fs.Parse(append(globalArgs, restArgs...)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			usage(os.Stdout, fs)
			return nil
		}
		return err
	}
	rest := fs.Args()
	if *showVersion {
		fmt.Println(version.VERSION)
		return nil
	}
	if len(rest) == 0 {
		usage(os.Stdout, fs)
		return nil
	}
	if *data != "" && !json.Valid([]byte(*data)) {
		return errors.New("--data 不是合法 JSON")
	}

	c := &client{
		server: strings.TrimRight(*server, "/"),
		token:  *token,
		key:    *key,
		http:   &http.Client{Timeout: 20 * time.Second},
		out:    os.Stdout,
	}

	cmd := rest[0]
	argv := rest[1:]
	switch cmd {
	case "version":
		fmt.Println(version.VERSION)
		return nil
	case "dashboard":
		return c.get("/api/v1/overview")
	case "clients":
		return c.list("/api/v1/clients", *limit)
	case "client":
		return c.clientCmd(argv, *data)
	case "tunnels":
		return c.list("/api/v1/tunnels", *limit)
	case "tunnel":
		return c.tunnelCmd(argv, *data)
	case "hosts":
		return c.list("/api/v1/hosts", *limit)
	case "host":
		return c.hostCmd(argv, *data)
	case "users":
		return c.list("/api/v1/users", *limit)
	case "audit":
		return c.get("/api/v1/audit?limit=" + strconv.Itoa(nonZero(*limit, 50)))
	case "npc":
		printNPC()
		return nil
	case "skill":
		printSkill()
		return nil
	case "mcp":
		return c.printMCP()
	default:
		return fmt.Errorf("未知子命令 %q", cmd)
	}
}

func (c *client) clientCmd(argv []string, data string) error {
	if len(argv) == 0 {
		return errors.New("用法: npsctl client <get|create|update|delete|enable|disable> [id] [--data JSON]")
	}
	action := argv[0]
	id := argAt(argv, 1)
	switch action {
	case "get":
		if err := requireID("client", action, id); err != nil {
			return err
		}
		return c.get("/api/v1/clients/" + id)
	case "create":
		if err := requireData("client create", data); err != nil {
			return err
		}
		return c.post("/api/v1/clients", data)
	case "update":
		if err := requireID("client", action, id); err != nil {
			return err
		}
		if err := requireData("client update", data); err != nil {
			return err
		}
		return c.send("PUT", "/api/v1/clients/"+id, data)
	case "delete":
		if err := requireID("client", action, id); err != nil {
			return err
		}
		return c.send("DELETE", "/api/v1/clients/"+id, "")
	case "enable", "disable":
		if err := requireID("client", action, id); err != nil {
			return err
		}
		status := "true"
		if action == "disable" {
			status = "false"
		}
		return c.post("/api/v1/clients/"+id+"/status", `{"status":`+status+`}`)
	default:
		return fmt.Errorf("未知 client 动作 %q", action)
	}
}

func (c *client) tunnelCmd(argv []string, data string) error {
	if len(argv) == 0 {
		return errors.New("用法: npsctl tunnel <get|create|update|delete|start|stop> [id] [--data JSON]")
	}
	action := argv[0]
	id := argAt(argv, 1)
	switch action {
	case "get":
		if err := requireID("tunnel", action, id); err != nil {
			return err
		}
		return c.get("/api/v1/tunnels/" + id)
	case "create":
		if err := requireData("tunnel create", data); err != nil {
			return err
		}
		return c.post("/api/v1/tunnels", data)
	case "update":
		if err := requireID("tunnel", action, id); err != nil {
			return err
		}
		if err := requireData("tunnel update", data); err != nil {
			return err
		}
		return c.send("PUT", "/api/v1/tunnels/"+id, data)
	case "delete":
		if err := requireID("tunnel", action, id); err != nil {
			return err
		}
		return c.send("DELETE", "/api/v1/tunnels/"+id, "")
	case "start":
		if err := requireID("tunnel", action, id); err != nil {
			return err
		}
		return c.post("/api/v1/tunnels/"+id+"/start", "")
	case "stop":
		if err := requireID("tunnel", action, id); err != nil {
			return err
		}
		return c.post("/api/v1/tunnels/"+id+"/stop", "")
	default:
		return fmt.Errorf("未知 tunnel 动作 %q", action)
	}
}

func (c *client) hostCmd(argv []string, data string) error {
	if len(argv) == 0 {
		return errors.New("用法: npsctl host <get|create|update|delete|start|stop> [id] [--data JSON]")
	}
	action := argv[0]
	id := argAt(argv, 1)
	switch action {
	case "get":
		if err := requireID("host", action, id); err != nil {
			return err
		}
		return c.get("/api/v1/hosts/" + id)
	case "create":
		if err := requireData("host create", data); err != nil {
			return err
		}
		return c.post("/api/v1/hosts", data)
	case "update":
		if err := requireID("host", action, id); err != nil {
			return err
		}
		if err := requireData("host update", data); err != nil {
			return err
		}
		return c.send("PUT", "/api/v1/hosts/"+id, data)
	case "delete":
		if err := requireID("host", action, id); err != nil {
			return err
		}
		return c.send("DELETE", "/api/v1/hosts/"+id, "")
	case "start":
		if err := requireID("host", action, id); err != nil {
			return err
		}
		return c.post("/api/v1/hosts/"+id+"/start", "")
	case "stop":
		if err := requireID("host", action, id); err != nil {
			return err
		}
		return c.post("/api/v1/hosts/"+id+"/stop", "")
	default:
		return fmt.Errorf("未知 host 动作 %q", action)
	}
}

func (c *client) list(path string, limit int) error {
	if limit > 0 {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		path += sep + "limit=" + strconv.Itoa(limit)
	}
	return c.get(path)
}

func (c *client) get(path string) error {
	return c.send("GET", path, "")
}

func (c *client) post(path, body string) error {
	return c.send("POST", path, body)
}

func (c *client) send(method, path, body string) error {
	if c.token == "" && c.key == "" {
		return errors.New("缺少凭据：请设置 --token 或 --key（或环境变量 NPS_TOKEN / NPS_KEY）")
	}
	full := c.server + path
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, full, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	} else {
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		q := req.URL.Query()
		q.Set("auth_key", md5Hex(c.key+ts))
		q.Set("timestamp", ts)
		req.URL.RawQuery = q.Encode()
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	var env envelope
	parsed := json.Unmarshal(raw, &env) == nil
	if resp.StatusCode >= http.StatusBadRequest {
		msg := strings.TrimSpace(env.Msg)
		if parsed && env.ErrorCode != "" {
			msg = strings.TrimSpace(env.ErrorCode + ": " + msg)
		}
		if msg == "" {
			msg = bodySnippet(raw)
		}
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
	}
	if parsed && env.Code != "" && env.Status != 1 {
		msg := env.Msg
		if env.ErrorCode != "" {
			msg = env.ErrorCode + ": " + msg
		}
		return errors.New(msg)
	}
	var out []byte
	switch {
	case len(env.Rows) > 0 || len(env.Total) > 0 || len(env.Meta) > 0:
		out = prettyJSON(raw)
	case len(env.Data) > 0:
		out = prettyJSON(env.Data)
	default:
		out = prettyJSON(raw)
	}
	if c.out == nil {
		c.out = os.Stdout
	}
	fmt.Fprintln(c.out, string(out))
	return nil
}

func (c *client) printMCP() error {
	u, err := url.Parse(c.server)
	if err != nil {
		return err
	}
	cred := c.token
	if cred == "" {
		cred = "YOUR_TOKEN"
	}
	cfg := map[string]any{
		"mcpServers": map[string]any{
			"nps": map[string]any{
				"type":    "http",
				"url":     u.Scheme + "://" + u.Host + "/mcp",
				"headers": map[string]string{"Authorization": "Bearer " + cred},
			},
		},
	}
	out, _ := json.MarshalIndent(cfg, "", "  ")
	if c.out == nil {
		c.out = os.Stdout
	}
	fmt.Fprintln(c.out, string(out))
	fmt.Fprintln(c.out, "\n提示: type=http 表示 Streamable HTTP，不要改成 sse；Token 可用 ${env:NPS_TOKEN} 占位。")
	return nil
}

func printNPC() {
	fmt.Print(`NPC 部署方式（任选其一）：

1. 交互式启动：   npc -server=<服务器> -vkey=<VKey>
2. 快捷启动：     在交互菜单中直接粘贴“快捷启动命令”
3. 注册系统服务： 运行 npc 后选择菜单 [1] 注册系统服务
4. 多实例：       同一 VKey 可多机运行，服务端按实例分别展示

端口以面板客户端详情或 /api/v1/clients 返回的 bridgeEndpoints 为准。
未修改默认配置时：TCP 8024，TLS 8025。

示例：
  npc -server=<bridge_host>:<bridge_port> -vkey=<vkey>
  npc -type=tls -tls_enable=true -server=<tls_host>:<tls_port> -vkey=<vkey>
`)
}

func printSkill() {
	fmt.Print(`NPS Pro Skill 安装方式：

  方式一（推荐，从你的 NPS 面板下载）：
    mkdir -p ~/.codex/skills
    curl -fsSL http://<你的面板>/skill/nps.tar.gz | tar -xz -C ~/.codex/skills

  方式二（GitHub Release）：
    curl -fsSL https://github.com/ZiDuNet/nps/releases/latest/download/nps-skill.tar.gz | tar -xz -C ~/.codex/skills

Claude 用户把目标目录换成 ~/.claude/skills；受限或沙箱环境可能要求
.user_skills 目录。安装后 Codex/Claude 会自动发现 “nps” 技能，可引导部署
NPC、查询客户端/隧道/域名反代并输出 /mcp 配置。
`)
}

func usage(w io.Writer, fs *flag.FlagSet) {
	fmt.Fprintf(w, `npsctl - NPS Pro 控制面 CLI

用法:
  npsctl [全局参数] <command> [args] [--data JSON]
  npsctl -h | --help | -version

命令:
  dashboard                         仪表盘概览
  clients [--limit N]               客户端列表
  client get <id>                   客户端详情
  client create --data '...'        创建客户端
  client update <id> --data '...'   更新客户端
  client delete <id>                删除客户端
  client enable|disable <id>        启用/停用客户端
  tunnels [--limit N]               隧道列表
  tunnel get|create|update|delete|start|stop
  hosts [--limit N]                 域名反代列表
  host get|create|update|delete|start|stop
  users                             用户列表（仅管理员）
  audit [--limit N]                 操作审计（管理员看全局，用户看本人范围）
  npc                               打印 NPC 部署方式
  skill                             打印 Skill 安装方式
  mcp                               打印 mcpServers 配置片段
  version                           版本号

列表接口返回 status/code/msg/rows/total/request_id，rows 按服务端分页；
--limit 只控制本次请求条数。环境变量：NPS_SERVER、NPS_TOKEN、NPS_KEY。

全局参数:
`)
	previous := fs.Output()
	fs.SetOutput(w)
	fs.PrintDefaults()
	fs.SetOutput(previous)
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// splitGlobalArgs moves the recognised global flags (and their values) ahead of
// positional arguments so flags can be placed before or after the subcommand.
func splitGlobalArgs(args []string) (global, rest []string) {
	known := map[string]bool{
		"--server": true, "--token": true, "--key": true, "--limit": true, "--data": true,
		"-version": true, "--version": true,
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		name := arg
		inlineValue := false
		if eq := strings.Index(arg, "="); eq >= 0 {
			name, inlineValue = arg[:eq], true
		}
		if !known[name] {
			rest = append(rest, arg)
			continue
		}
		if name == "-version" || name == "--version" {
			global = append(global, arg)
			continue
		}
		if inlineValue {
			global = append(global, arg)
			continue
		}
		global = append(global, arg)
		if i+1 < len(args) {
			global = append(global, args[i+1])
			i++
		}
	}
	return global, rest
}

func argAt(args []string, i int) string {
	if i < len(args) {
		return args[i]
	}
	return ""
}

func requireID(kind, action, id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("用法: npsctl %s %s <id>", kind, action)
	}
	n, err := strconv.Atoi(id)
	if err != nil || n <= 0 {
		return fmt.Errorf("%s id 必须是正整数，收到 %q", kind, id)
	}
	return nil
}

func requireData(action, data string) error {
	if strings.TrimSpace(data) == "" {
		return fmt.Errorf("%s 需要 --data '<JSON>'", action)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(data), &payload); err != nil || payload == nil {
		return errors.New("--data 必须是 JSON 对象")
	}
	return nil
}

func prettyJSON(raw []byte) []byte {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return raw
	}
	pretty, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return raw
	}
	return pretty
}

func bodySnippet(raw []byte) string {
	text := strings.Join(strings.Fields(string(raw)), " ")
	if text == "" {
		return "(empty body)"
	}
	if len(text) > 300 {
		return text[:300] + "…"
	}
	return text
}

func nonZero(v, fallback int) int {
	if v <= 0 {
		return fallback
	}
	return v
}

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}
