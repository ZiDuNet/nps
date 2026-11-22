// Command npsctl is the NPS Pro control-plane CLI. It talks to a running NPS
// server through the versioned /api/v1 surface using either an administrator
// auth_key signature or a user API bearer token.
package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"ehang.io/nps/lib/version"
)

const adminAPITokenPrefixForCLI = "npsa_"

type client struct {
	server     string
	token      string
	key        string
	keyID      string
	http       *http.Client
	out        io.Writer
	jsonOutput bool
	fields     string
	timeout    time.Duration
	caFile     string
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
	return runWithWriters(args, os.Stdout, os.Stderr)
}

func runWithWriters(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("npsctl", flag.ContinueOnError)
	fs.Usage = func() {}
	var (
		server      = fs.String("server", envOr("NPS_SERVER", "http://127.0.0.1:8081"), "NPS server base URL (env NPS_SERVER)")
		token       = fs.String("token", os.Getenv("NPS_TOKEN"), "user API bearer token npsu_... (env NPS_TOKEN)")
		key         = fs.String("key", os.Getenv("NPS_KEY"), "administrator auth_key or managed npsa_ key (env NPS_KEY)")
		keyID       = fs.String("key-id", "auto", "administrator key type: auto, auth_key, or managed_admin")
		limit       = fs.Int("limit", 0, "max rows for list commands")
		offset      = fs.Int("offset", 0, "number of rows to skip for list commands")
		timeout     = fs.Duration("timeout", 20*time.Second, "HTTP request timeout (for example 10s or 1m)")
		caFile      = fs.String("ca-file", "", "PEM CA bundle to trust for HTTPS connections")
		jsonOutput  = fs.Bool("json", false, "write JSON instead of human-readable tables")
		fields      = fs.String("fields", "", "write selected comma-separated fields as TSV (for example id,remark,status)")
		data        = fs.String("data", "", "JSON body for create/update commands")
		showVersion = fs.Bool("version", false, "print version and exit")
	)
	fs.SetOutput(stderr)
	globalArgs, restArgs := splitGlobalArgs(args)
	if err := fs.Parse(append(globalArgs, restArgs...)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			usage(stdout, fs)
			return nil
		}
		return err
	}
	rest := fs.Args()
	if *showVersion {
		fmt.Fprintln(stdout, version.VERSION)
		return nil
	}
	if len(rest) == 0 {
		usage(stdout, fs)
		return nil
	}
	if *limit < 0 {
		return errors.New("--limit 不能为负数")
	}
	if *offset < 0 {
		return errors.New("--offset 不能为负数")
	}
	if _, err := resolveAdminKeyID(*key, *keyID); err != nil {
		return err
	}
	if strings.TrimSpace(*token) != "" && strings.TrimSpace(*keyID) != "" && !strings.EqualFold(strings.TrimSpace(*keyID), "auto") {
		return errors.New("--key-id 仅适用于 --key，不能与 --token 一起使用")
	}
	if *jsonOutput && strings.TrimSpace(*fields) != "" {
		return errors.New("--json 与 --fields 不能同时使用")
	}
	if *fields != "" {
		if _, err := parseFields(*fields); err != nil {
			return err
		}
	}
	c := &client{
		server:     strings.TrimRight(strings.TrimSpace(*server), "/"),
		token:      strings.TrimSpace(*token),
		key:        strings.TrimSpace(*key),
		keyID:      strings.TrimSpace(*keyID),
		out:        stdout,
		jsonOutput: *jsonOutput,
		fields:     strings.TrimSpace(*fields),
		timeout:    *timeout,
		caFile:     *caFile,
	}

	cmd := rest[0]
	argv := rest[1:]
	switch cmd {
	case "version":
		if err := noPositionalArgs("version", argv); err != nil {
			return err
		}
		fmt.Fprintln(stdout, version.VERSION)
		return nil
	case "dashboard":
		if err := noPositionalArgs("dashboard", argv); err != nil {
			return err
		}
		return c.get("/api/v1/overview")
	case "clients":
		if err := noPositionalArgs("clients", argv); err != nil {
			return err
		}
		return c.list("/api/v1/clients", *limit, *offset)
	case "client":
		return c.clientCmd(argv, *data)
	case "tunnels":
		if err := noPositionalArgs("tunnels", argv); err != nil {
			return err
		}
		return c.list("/api/v1/tunnels", *limit, *offset)
	case "tunnel":
		return c.tunnelCmd(argv, *data)
	case "hosts":
		if err := noPositionalArgs("hosts", argv); err != nil {
			return err
		}
		return c.list("/api/v1/hosts", *limit, *offset)
	case "host":
		return c.hostCmd(argv, *data)
	case "users":
		if err := noPositionalArgs("users", argv); err != nil {
			return err
		}
		return c.list("/api/v1/users", *limit, *offset)
	case "audit":
		if err := noPositionalArgs("audit", argv); err != nil {
			return err
		}
		return c.list("/api/v1/audit", nonZero(*limit, 50), *offset)
	case "npc":
		if err := noPositionalArgs("npc", argv); err != nil {
			return err
		}
		printNPC()
		return nil
	case "skill":
		if err := noPositionalArgs("skill", argv); err != nil {
			return err
		}
		printSkill()
		return nil
	case "mcp":
		if err := noPositionalArgs("mcp", argv); err != nil {
			return err
		}
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
		if err := exactArgs("client get", argv, 2); err != nil {
			return err
		}
		if err := requireID("client", action, id); err != nil {
			return err
		}
		return c.get("/api/v1/clients/" + id)
	case "create":
		if err := exactArgs("client create", argv, 1); err != nil {
			return err
		}
		if err := requireData("client create", data); err != nil {
			return err
		}
		return c.post("/api/v1/clients", data)
	case "update":
		if err := exactArgs("client update", argv, 2); err != nil {
			return err
		}
		if err := requireID("client", action, id); err != nil {
			return err
		}
		if err := requireData("client update", data); err != nil {
			return err
		}
		return c.send("PUT", "/api/v1/clients/"+id, data)
	case "delete":
		if err := exactArgs("client delete", argv, 2); err != nil {
			return err
		}
		if err := requireID("client", action, id); err != nil {
			return err
		}
		return c.send("DELETE", "/api/v1/clients/"+id, "")
	case "enable", "disable":
		if err := exactArgs("client "+action, argv, 2); err != nil {
			return err
		}
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
		if err := exactArgs("tunnel get", argv, 2); err != nil {
			return err
		}
		if err := requireID("tunnel", action, id); err != nil {
			return err
		}
		return c.get("/api/v1/tunnels/" + id)
	case "create":
		if err := exactArgs("tunnel create", argv, 1); err != nil {
			return err
		}
		if err := requireData("tunnel create", data); err != nil {
			return err
		}
		return c.post("/api/v1/tunnels", data)
	case "update":
		if err := exactArgs("tunnel update", argv, 2); err != nil {
			return err
		}
		if err := requireID("tunnel", action, id); err != nil {
			return err
		}
		if err := requireData("tunnel update", data); err != nil {
			return err
		}
		return c.send("PUT", "/api/v1/tunnels/"+id, data)
	case "delete":
		if err := exactArgs("tunnel delete", argv, 2); err != nil {
			return err
		}
		if err := requireID("tunnel", action, id); err != nil {
			return err
		}
		return c.send("DELETE", "/api/v1/tunnels/"+id, "")
	case "start":
		if err := exactArgs("tunnel start", argv, 2); err != nil {
			return err
		}
		if err := requireID("tunnel", action, id); err != nil {
			return err
		}
		return c.post("/api/v1/tunnels/"+id+"/start", "")
	case "stop":
		if err := exactArgs("tunnel stop", argv, 2); err != nil {
			return err
		}
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
		if err := exactArgs("host get", argv, 2); err != nil {
			return err
		}
		if err := requireID("host", action, id); err != nil {
			return err
		}
		return c.get("/api/v1/hosts/" + id)
	case "create":
		if err := exactArgs("host create", argv, 1); err != nil {
			return err
		}
		if err := requireData("host create", data); err != nil {
			return err
		}
		return c.post("/api/v1/hosts", data)
	case "update":
		if err := exactArgs("host update", argv, 2); err != nil {
			return err
		}
		if err := requireID("host", action, id); err != nil {
			return err
		}
		if err := requireData("host update", data); err != nil {
			return err
		}
		return c.send("PUT", "/api/v1/hosts/"+id, data)
	case "delete":
		if err := exactArgs("host delete", argv, 2); err != nil {
			return err
		}
		if err := requireID("host", action, id); err != nil {
			return err
		}
		return c.send("DELETE", "/api/v1/hosts/"+id, "")
	case "start":
		if err := exactArgs("host start", argv, 2); err != nil {
			return err
		}
		if err := requireID("host", action, id); err != nil {
			return err
		}
		return c.post("/api/v1/hosts/"+id+"/start", "")
	case "stop":
		if err := exactArgs("host stop", argv, 2); err != nil {
			return err
		}
		if err := requireID("host", action, id); err != nil {
			return err
		}
		return c.post("/api/v1/hosts/"+id+"/stop", "")
	default:
		return fmt.Errorf("未知 host 动作 %q", action)
	}
}

func (c *client) list(path string, limit, offset int) error {
	if limit < 0 || offset < 0 {
		return errors.New("列表分页参数不能为负数")
	}
	u, err := url.Parse(path)
	if err != nil {
		return err
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return fmt.Errorf("列表查询串格式无效: %w", err)
	}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	if offset > 0 {
		query.Set("offset", strconv.Itoa(offset))
	}
	u.RawQuery = query.Encode()
	return c.get(u.String())
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
	if c.token != "" && c.key != "" {
		return errors.New("凭据冲突：--token 与 --key 只能选择一个")
	}
	if err := validateServerURL(c.server); err != nil {
		return err
	}
	if c.http == nil {
		httpClient, err := newHTTPClient(c.timeout, c.caFile)
		if err != nil {
			return err
		}
		c.http = httpClient
	}
	full := strings.TrimRight(c.server, "/") + path
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
		if err := signAdminRequest(req, c.key, c.keyID, []byte(body)); err != nil {
			return err
		}
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
	if parsed && env.Status != 1 {
		msg := env.Msg
		if env.ErrorCode != "" {
			msg = env.ErrorCode + ": " + msg
		}
		if msg == "" {
			msg = "服务端返回失败状态"
		}
		return errors.New(msg)
	}
	if c.out == nil {
		c.out = os.Stdout
	}
	if c.fields != "" {
		return c.writeFields(raw, env)
	}
	if c.jsonOutput {
		fmt.Fprintln(c.out, string(prettyJSON(raw)))
		return nil
	}
	if len(env.Rows) > 0 {
		if err := c.writeTable(env.Rows, env.Total, path); err == nil {
			return nil
		}
		// Fall back to the complete JSON response if an endpoint unexpectedly
		// returns a non-tabular rows value.
		fmt.Fprintln(c.out, string(prettyJSON(raw)))
		return nil
	}
	if len(env.Data) > 0 {
		fmt.Fprintln(c.out, string(prettyJSON(env.Data)))
		return nil
	}
	fmt.Fprintln(c.out, string(prettyJSON(raw)))
	return nil
}

func (c *client) writeFields(raw []byte, env envelope) error {
	requested, err := parseFields(c.fields)
	if err != nil {
		return err
	}
	rows := env.Rows
	if len(rows) == 0 {
		rows = env.Data
	}
	if len(rows) == 0 {
		return errors.New("响应中没有 rows 或 data，无法输出 --fields")
	}
	return writeTSV(c.out, rows, requested)
}

func (c *client) writeTable(rowsRaw, totalRaw json.RawMessage, path string) error {
	var rows []any
	if err := json.Unmarshal(rowsRaw, &rows); err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Fprintln(c.out, "（没有记录）")
		if total, ok := jsonInteger(totalRaw); ok {
			fmt.Fprintf(c.out, "共 %d 条\n", total)
		}
		return nil
	}

	fields := defaultFields(path)
	selected := make([]string, 0, len(fields))
	for _, field := range fields {
		if _, _, ok := lookupField(rows[0], field); ok {
			selected = append(selected, field)
		}
	}
	if len(selected) == 0 {
		return errors.New("无法识别列表字段")
	}
	writer := tabwriter.NewWriter(c.out, 0, 4, 2, ' ', 0)
	if err := writeTableRow(writer, selected, selected); err != nil {
		return err
	}
	for _, row := range rows {
		values := make([]string, len(selected))
		for i, field := range selected {
			value, _, ok := lookupField(row, field)
			if ok {
				values[i] = displayValue(value)
			}
		}
		if err := writeTableRow(writer, values, nil); err != nil {
			return err
		}
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	if total, ok := jsonInteger(totalRaw); ok {
		fmt.Fprintf(c.out, "显示 %d 条（总计 %d 条）\n", len(rows), total)
	} else {
		fmt.Fprintf(c.out, "显示 %d 条\n", len(rows))
	}
	return nil
}

func writeTableRow(w io.Writer, values, _ []string) error {
	for i, value := range values {
		value = strings.NewReplacer("\r", " ", "\n", " ", "\t", " ").Replace(value)
		if i > 0 {
			if _, err := io.WriteString(w, "\t"); err != nil {
				return err
			}
		}
		if _, err := io.WriteString(w, value); err != nil {
			return err
		}
	}
	_, err := io.WriteString(w, "\n")
	return err
}

func writeTSV(out io.Writer, raw json.RawMessage, fields []string) error {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("解析 --fields 数据失败: %w", err)
	}
	rows, ok := value.([]any)
	if !ok {
		rows = []any{value}
	}
	writer := csv.NewWriter(out)
	writer.Comma = '\t'
	for rowIndex, row := range rows {
		values := make([]string, len(fields))
		for i, field := range fields {
			value, _, found := lookupField(row, field)
			if !found {
				if rowIndex == 0 {
					return fmt.Errorf("--fields 指定的字段 %q 不存在", field)
				}
				continue
			}
			values[i] = displayValue(value)
		}
		if rowIndex == 0 {
			if err := writer.Write(fields); err != nil {
				return err
			}
		}
		if err := writer.Write(values); err != nil {
			return err
		}
	}
	if len(rows) == 0 {
		if err := writer.Write(fields); err != nil {
			return err
		}
	}
	writer.Flush()
	return writer.Error()
}

func parseFields(value string) ([]string, error) {
	parts := strings.Split(value, ",")
	fields := make([]string, 0, len(parts))
	seen := make(map[string]bool, len(parts))
	for _, part := range parts {
		field := strings.TrimSpace(part)
		if field == "" {
			return nil, errors.New("--fields 需要逗号分隔的非空字段名")
		}
		key := strings.ToLower(field)
		if seen[key] {
			return nil, fmt.Errorf("--fields 字段重复: %q", field)
		}
		seen[key] = true
		fields = append(fields, field)
	}
	return fields, nil
}

func defaultFields(path string) []string {
	cleanPath := strings.ToLower(strings.Trim(strings.SplitN(path, "?", 2)[0], "/"))
	switch {
	case strings.HasSuffix(cleanPath, "/clients"):
		return []string{"Id", "UserName", "Remark", "Addr", "Status", "IsConnect", "NowConn"}
	case strings.HasSuffix(cleanPath, "/tunnels"):
		return []string{"Id", "Remark", "Mode", "Port", "Status", "RunStatus", "RunError"}
	case strings.HasSuffix(cleanPath, "/hosts"):
		return []string{"Id", "Host", "Remark", "Scheme", "IsClose", "AutoHttps"}
	case strings.HasSuffix(cleanPath, "/users"):
		return []string{"Id", "UserName", "Status", "Remark"}
	case strings.HasSuffix(cleanPath, "/audit"):
		return []string{"ID", "Time", "Actor", "Action", "Resource", "Result", "SourceIP", "Error"}
	default:
		return nil
	}
}

func lookupField(row any, requested string) (any, string, bool) {
	object, ok := row.(map[string]any)
	if !ok {
		return nil, "", false
	}
	if value, exists := object[requested]; exists {
		return value, requested, true
	}
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if strings.EqualFold(key, requested) {
			return object[key], key, true
		}
	}
	return nil, "", false
}

func displayValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return fmt.Sprint(typed)
		}
		return string(encoded)
	}
}

func jsonInteger(raw json.RawMessage) (int64, bool) {
	integer, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	return integer, err == nil
}

func (c *client) printMCP() error {
	server := strings.TrimRight(strings.TrimSpace(c.server), "/")
	if err := validateServerURL(server); err != nil {
		return err
	}
	if c.token != "" && c.key != "" {
		return errors.New("凭据冲突：--token 与 --key 只能选择一个")
	}
	u, _ := url.Parse(server)
	cred := c.token
	if cred == "" {
		keyID, err := resolveAdminKeyID(c.key, c.keyID)
		if err != nil {
			return err
		}
		if keyID == "managed_admin" {
			cred = c.key
		} else {
			cred = "YOUR_TOKEN"
		}
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
	fmt.Fprint(c.out, "\n提示: type=http 表示 Streamable HTTP，不要改成 sse。")
	if cred == "YOUR_TOKEN" {
		fmt.Fprint(c.out, " 当前没有可直接写入的凭据；设置 NPS_TOKEN 后重跑 npsctl mcp，或将 YOUR_TOKEN 替换为 npsu_ 用户 Token / npsa_ 管理员 API 密钥。")
	}
	fmt.Fprintln(c.out, " npsctl 不会展开 MCP 客户端专属的环境变量插值语法；若手动改用环境变量，请按目标 MCP 客户端文档配置。")
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

全局参数:
  --server URL       NPS 服务端地址（默认 NPS_SERVER 或 http://127.0.0.1:8081）
  --token TOKEN      用户 API Token（默认 NPS_TOKEN）
  --key KEY          管理员密钥（默认 NPS_KEY）
  --key-id TYPE      管理员密钥类型：auto、auth_key 或 managed_admin（默认 auto）
  --limit N          列表最大条数
  --offset N         跳过前 N 条列表记录
  --timeout DURATION HTTP 请求超时（默认 20s，例如 5s、1m）
  --ca-file PATH     HTTPS 使用的 PEM CA 证书包
  --json             列表输出完整 JSON 信封
  --fields A,B       输出所选字段的 TSV（适合脚本管道）
  --data JSON        创建/更新请求体

命令:
  dashboard                         仪表盘概览
  clients [--limit N] [--offset N]  客户端列表
  client get <id>                   客户端详情
  client create --data '...'        创建客户端
  client update <id> --data '...'   更新客户端
  client delete <id>                删除客户端
  client enable|disable <id>        启用/停用客户端
  tunnels [--limit N] [--offset N]  隧道列表
  tunnel get|create|update|delete|start|stop
  hosts [--limit N] [--offset N]   域名反代列表
  host get|create|update|delete|start|stop
  users [--limit N] [--offset N]    用户列表（仅管理员）
  audit [--limit N] [--offset N]    操作审计（管理员看全局，用户看本人范围）
  npc                               打印 NPC 部署方式
  skill                             打印 Skill 安装方式
  mcp                               打印 mcpServers 配置片段
  version                           版本号

列表默认显示易读表格；加 --json 可获取完整分页信封，或用 --fields 指定 TSV 列。
环境变量：NPS_SERVER、NPS_TOKEN、NPS_KEY。
`)
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func newHTTPClient(timeout time.Duration, caFile string) (*http.Client, error) {
	if timeout <= 0 {
		return nil, errors.New("--timeout 必须大于 0")
	}
	client := &http.Client{Timeout: timeout}
	if strings.TrimSpace(caFile) == "" {
		return client, nil
	}
	caPEM, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("读取 --ca-file 失败: %w", err)
	}
	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		roots = x509.NewCertPool()
	}
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("--ca-file 不包含有效的 PEM CA 证书")
	}
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("无法复制默认 HTTP transport")
	}
	transport = transport.Clone()
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{}
	} else {
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	}
	transport.TLSClientConfig.RootCAs = roots
	client.Transport = transport
	return client, nil
}

// splitGlobalArgs moves the recognised global flags (and their values) ahead of
// positional arguments so flags can be placed before or after the subcommand.
func splitGlobalArgs(args []string) (global, rest []string) {
	known := map[string]bool{
		"--server": true, "--token": true, "--key": true, "--key-id": true, "--limit": true, "--offset": true,
		"--timeout": true, "--ca-file": true, "--json": true, "--fields": true, "--data": true,
		"-version": true, "--version": true, "-h": true, "--help": true,
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
		if name == "-version" || name == "--version" || name == "-h" || name == "--help" {
			global = append(global, arg)
			continue
		}
		if name == "--json" {
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

func exactArgs(command string, args []string, want int) error {
	if len(args) == want {
		return nil
	}
	return fmt.Errorf("用法: npsctl %s 参数数量不正确：需要 %d 个，实际 %d 个", command, want, len(args))
}

func noPositionalArgs(command string, args []string) error {
	if len(args) == 0 {
		return nil
	}
	return fmt.Errorf("用法: npsctl %s 不接受额外参数，收到 %q", command, strings.Join(args, " "))
}

func validateServerURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return errors.New("--server 必须是带主机名的绝对 http:// 或 https:// URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("--server 仅支持 http:// 或 https://")
	}
	if u.User != nil {
		return errors.New("--server 不能包含用户名或密码")
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

func signAdminRequest(req *http.Request, secret, requestedKeyID string, body []byte) error {
	if req == nil {
		return errors.New("无法为 nil 请求签名")
	}
	if strings.TrimSpace(secret) == "" {
		return errors.New("管理员密钥不能为空")
	}
	nonceBytes := make([]byte, 24)
	if _, err := rand.Read(nonceBytes); err != nil {
		return fmt.Errorf("生成 API nonce 失败: %w", err)
	}
	nonce := base64.RawURLEncoding.EncodeToString(nonceBytes)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	bodySum := sha256.Sum256(body)
	bodyHash := hex.EncodeToString(bodySum[:])
	keyID, err := resolveAdminKeyID(secret, requestedKeyID)
	if err != nil {
		return err
	}
	query, err := url.ParseQuery(req.URL.RawQuery)
	if err != nil {
		return fmt.Errorf("管理员请求查询串格式无效: %w", err)
	}
	path := req.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	canonical := strings.Join([]string{
		strings.ToUpper(req.Method),
		path,
		query.Encode(),
		bodyHash,
		timestamp,
		nonce,
		keyID,
	}, "\n")
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(canonical))
	req.Header.Set("Authorization", "NPS-HMAC "+hex.EncodeToString(mac.Sum(nil)))
	req.Header.Set("X-NPS-Timestamp", timestamp)
	req.Header.Set("X-NPS-Nonce", nonce)
	req.Header.Set("X-NPS-Key-Id", keyID)
	return nil
}

func resolveAdminKeyID(secret, requested string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(requested)) {
	case "", "auto":
		if strings.HasPrefix(strings.TrimSpace(secret), adminAPITokenPrefixForCLI) {
			return "managed_admin", nil
		}
		return "auth_key", nil
	case "auth_key", "managed_admin":
		return strings.ToLower(strings.TrimSpace(requested)), nil
	default:
		return "", errors.New("--key-id 必须为 auto、auth_key 或 managed_admin")
	}
}
