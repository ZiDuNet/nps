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
)

const version = "1.1.47"

type client struct {
	server string
	token  string
	key    string
	http   *http.Client
}

type envelope struct {
	Status    int             `json:"status"`
	Code      string          `json:"code"`
	Msg       string          `json:"msg"`
	ErrorCode string          `json:"error_code"`
	Data      json.RawMessage `json:"data"`
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
	var (
		server = fs.String("server", envOr("NPS_SERVER", "http://127.0.0.1:8081"), "NPS server base URL")
		token  = fs.String("token", os.Getenv("NPS_TOKEN"), "user API bearer token (npsu_...)")
		key    = fs.String("key", os.Getenv("NPS_KEY"), "administrator auth_key for signature auth")
		limit  = fs.Int("limit", 0, "max rows for list commands")
		data   = fs.String("data", "", "JSON body for create/update commands")
	)
	fs.SetOutput(os.Stderr)
	globalArgs, restArgs := splitGlobalArgs(args)
	if err := fs.Parse(append(globalArgs, restArgs...)); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		usage(fs)
		return nil
	}

	c := &client{
		server: strings.TrimRight(*server, "/"),
		token:  *token,
		key:    *key,
		http:   &http.Client{Timeout: 20 * time.Second},
	}

	cmd := rest[0]
	argv := rest[1:]
	switch cmd {
	case "version":
		fmt.Println(version)
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
		return c.get("/api/v1/clients/" + id)
	case "create":
		return c.post("/api/v1/clients", data)
	case "update":
		return c.send("PUT", "/api/v1/clients/"+id, data)
	case "delete":
		return c.send("DELETE", "/api/v1/clients/"+id, "")
	case "enable", "disable":
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
		return errors.New("用法: npsctl tunnel <get|create|delete|start|stop> [id] [--data JSON]")
	}
	action := argv[0]
	id := argAt(argv, 1)
	switch action {
	case "get":
		return c.get("/api/v1/tunnels/" + id)
	case "create":
		return c.post("/api/v1/tunnels", data)
	case "delete":
		return c.send("DELETE", "/api/v1/tunnels/"+id, "")
	case "start":
		return c.post("/api/v1/tunnels/"+id+"/start", "")
	case "stop":
		return c.post("/api/v1/tunnels/"+id+"/stop", "")
	default:
		return fmt.Errorf("未知 tunnel 动作 %q", action)
	}
}

func (c *client) hostCmd(argv []string, data string) error {
	if len(argv) == 0 {
		return errors.New("用法: npsctl host <get|create|delete|start|stop> [id] [--data JSON]")
	}
	action := argv[0]
	id := argAt(argv, 1)
	switch action {
	case "get":
		return c.get("/api/v1/hosts/" + id)
	case "create":
		return c.post("/api/v1/hosts", data)
	case "delete":
		return c.send("DELETE", "/api/v1/hosts/"+id, "")
	case "start":
		return c.post("/api/v1/hosts/"+id+"/start", "")
	case "stop":
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
	if err := json.Unmarshal(raw, &env); err == nil && env.Code != "" {
		if env.Status != 1 {
			msg := env.Msg
			if env.ErrorCode != "" {
				msg = env.ErrorCode + ": " + msg
			}
			return errors.New(msg)
		}
	}
	out := raw
	if len(env.Data) > 0 {
		var pretty json.RawMessage
		if err := json.Unmarshal(env.Data, &pretty); err == nil {
			out, _ = json.MarshalIndent(pretty, "", "  ")
		}
	} else if env.Code != "" {
		out, _ = json.MarshalIndent(env, "", "  ")
	}
	fmt.Println(string(out))
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
				"url":     u.Scheme + "://" + u.Host + "/mcp",
				"headers": map[string]string{"Authorization": "Bearer " + cred},
			},
		},
	}
	out, _ := json.MarshalIndent(cfg, "", "  ")
	fmt.Println(string(out))
	return nil
}

func printNPC() {
	fmt.Print(`NPC 部署方式（任选其一）：

1. 交互式启动：   npc -server=<服务器> -vkey=<VKey>
2. 快捷启动：     在交互菜单中直接粘贴“快捷启动命令”
3. 注册系统服务： 运行 npc 后选择菜单 [1] 注册系统服务
4. 多实例：       同一 VKey 可多机运行，服务端按实例分别展示

示例：
  npc -server=nps.example.com:8024 -vkey=a22c306c64
  npc -type=tls -tls_enable=true -server=nps.example.com:8025 -vkey=a22c306c64
`)
}

func printSkill() {
	fmt.Print(`NPS Pro Skill 安装方式：

  方式一（推荐，从你的 NPS 面板下载）：
    mkdir -p ~/.codex/skills
    curl -fsSL http://<你的面板>/skill/nps.tar.gz | tar -xz -C ~/.codex/skills

  方式二（GitHub Release）：
    curl -fsSL https://github.com/ZiDuNet/nps/releases/latest/download/nps-skill.tar.gz | tar -xz -C ~/.codex/skills

Claude 用户把目标目录换成 ~/.claude/skills。安装后 Codex/Claude 会自动发现
“nps” 技能，可引导部署 NPC、查询客户端/隧道/域名反代并输出 /mcp 配置。
`)
}

func usage(fs *flag.FlagSet) {
	fmt.Fprintf(os.Stderr, `npsctl - NPS Pro 控制面 CLI

用法:
  npsctl [全局参数] <command> [args] [--data JSON]

命令:
  dashboard                         仪表盘概览
  clients [--limit N]               客户端列表
  client get <id>                   客户端详情
  client create --data '...'        创建客户端
  client update <id> --data '...'   更新客户端
  client delete <id>                删除客户端
  client enable|disable <id>        启用/停用客户端
  tunnels [--limit N]               隧道列表
  tunnel get|create|delete|start|stop
  hosts [--limit N]                 域名反代列表
  host get|create|delete|start|stop
  users                             用户列表（仅管理员）
  audit [--limit N]                 操作审计
  npc                               打印 NPC 部署方式
  skill                             打印 Skill 安装方式
  mcp                               打印 mcpServers 配置片段
  version                           版本号

全局参数:
`)
	fs.PrintDefaults()
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
