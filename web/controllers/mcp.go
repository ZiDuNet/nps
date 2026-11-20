package controllers

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ehang.io/nps/lib/file"
	"ehang.io/nps/lib/version"
	"ehang.io/nps/web/audit"
	"github.com/astaxie/beego"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

type mcpPrincipal struct {
	Token   string
	IsAdmin bool
	UserID  int
}

type mcpPrincipalKey struct{}

// NewMCPHandler returns the Streamable HTTP MCP endpoint at /mcp. It is
// disabled unless mcp_enabled is true (the default).
func NewMCPHandler() http.Handler {
	if !beego.AppConfig.DefaultBool("mcp_enabled", true) {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "MCP is disabled", http.StatusNotFound)
		})
	}
	mcpServer := server.NewMCPServer("nps", version.VERSION, server.WithToolCapabilities(true))
	registerMCPTools(mcpServer)
	handler := server.NewStreamableHTTPServer(mcpServer,
		server.WithHTTPContextFunc(func(ctx context.Context, r *http.Request) context.Context {
			if principal, ok := resolveMCPPrincipal(r); ok {
				return context.WithValue(ctx, mcpPrincipalKey{}, principal)
			}
			return ctx
		}),
		server.WithDisableLocalhostProtection(true),
	)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := resolveMCPPrincipal(r); !ok {
			w.Header().Set("WWW-Authenticate", `Bearer realm="nps"`)
			http.Error(w, `{"status":0,"code":"unauthorized","msg":"缺少或无效的 Bearer Token"}`, http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	})
}

// resolveMCPPrincipal authenticates an MCP request from its bearer token. Both
// user tokens (npsu_) and the web-managed administrator key (npsa_) are valid.
func resolveMCPPrincipal(r *http.Request) (*mcpPrincipal, bool) {
	if r == nil {
		return nil, false
	}
	token := extractUserAPIToken(r.Header.Get("Authorization"), "")
	if token == "" {
		return nil, false
	}
	if userID, ok := lookupUserByAPIToken(token); ok {
		return &mcpPrincipal{Token: token, IsAdmin: false, UserID: userID}, true
	}
	if enabled, adminToken := adminAPIKeySnapshot(); enabled && adminToken != "" {
		if subtle.ConstantTimeCompare([]byte(adminToken), []byte(token)) == 1 {
			return &mcpPrincipal{Token: token, IsAdmin: true}, true
		}
	}
	return nil, false
}

func mcpPrincipalFrom(ctx context.Context) (*mcpPrincipal, bool) {
	if ctx == nil {
		return nil, false
	}
	principal, ok := ctx.Value(mcpPrincipalKey{}).(*mcpPrincipal)
	return principal, ok
}

func (p *mcpPrincipal) allowedClientIDs() map[int]struct{} {
	if p == nil || p.IsAdmin {
		return nil
	}
	return file.GetDb().UserClientIds(p.UserID)
}

func (p *mcpPrincipal) canTouchClient(clientID int) bool {
	if p == nil {
		return false
	}
	if p.IsAdmin {
		return true
	}
	return file.GetDb().IsClientBelongToUser(clientID, p.UserID)
}

func mcpArgString(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return strings.TrimSpace(value)
}

func mcpArgInt(args map[string]any, key string) int {
	switch value := args[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(value))
		return n
	}
	return 0
}

func mcpArgBool(args map[string]any, key string) bool {
	switch value := args[key].(type) {
	case bool:
		return value
	case string:
		b, _ := strconv.ParseBool(strings.TrimSpace(value))
		return b
	}
	return false
}

func mcpJSON(data any) (*mcp.CallToolResult, error) {
	return mcp.NewToolResultJSON(data)
}

func mcpError(text string) *mcp.CallToolResult {
	return mcp.NewToolResultError(text)
}

func registerMCPTools(s *server.MCPServer) {
	registerMCPReadTools(s)
	registerMCPClientTools(s)
	registerMCPTunnelTools(s)
	registerMCPHostTools(s)
}

func registerMCPReadTools(s *server.MCPServer) {
	s.AddTool(mcp.NewTool("nps_dashboard",
		mcp.WithDescription("返回仪表盘概览：客户端、隧道、域名反代数量与在线客户端数。"),
	), mcpDashboard)

	s.AddTool(mcp.NewTool("nps_clients",
		mcp.WithDescription("列出客户端，可按备注/ID 搜索，或指定单个客户端 ID。"),
		mcp.WithString("search", mcp.Description("按备注、VKey 或 ID 模糊搜索")),
		mcp.WithNumber("client_id", mcp.Description("只返回指定客户端 ID")),
	), mcpClients)

	s.AddTool(mcp.NewTool("nps_client_get",
		mcp.WithDescription("返回单个客户端的详细信息。"),
		mcp.WithNumber("id", mcp.Required(), mcp.Description("客户端 ID")),
	), mcpClientGet)

	s.AddTool(mcp.NewTool("nps_tunnels",
		mcp.WithDescription("列出隧道，可按客户端 ID 或模式过滤。"),
		mcp.WithNumber("client_id", mcp.Description("按客户端 ID 过滤")),
		mcp.WithString("mode", mcp.Description("按模式过滤：tcp、udp、socks5、httpProxy、file、p2p 等")),
	), mcpTunnels)

	s.AddTool(mcp.NewTool("nps_hosts",
		mcp.WithDescription("列出域名反代规则。"),
		mcp.WithString("search", mcp.Description("按域名或备注模糊搜索")),
	), mcpHosts)

	s.AddTool(mcp.NewTool("nps_users",
		mcp.WithDescription("列出用户（仅管理员）。"),
		mcp.WithString("search", mcp.Description("按用户名或邮箱模糊搜索")),
	), mcpUsers)

	s.AddTool(mcp.NewTool("nps_audit",
		mcp.WithDescription("列出最近的操作审计事件（仅管理员）。"),
		mcp.WithNumber("limit", mcp.Description("返回条数，默认 50，最大 500")),
	), mcpAudit)

	s.AddTool(mcp.NewTool("nps_global_config",
		mcp.WithDescription("返回全局配置的脱敏概览（仅管理员）。"),
	), mcpGlobalConfig)
}

func mcpDashboard(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	principal, _ := mcpPrincipalFrom(ctx)
	db := file.GetDb()
	clients, _ := db.GetClientListForAllowedIds(0, 10000, "", "id", "asc", 0, principal.allowedClientIDs())
	tunnels, hosts, users := 0, 0, 0
	if principal.IsAdmin {
		userRows, _ := db.GetUserList(0, 10000, "")
		users = len(userRows)
	}
	allowed := principal.allowedClientIDs()
	db.JsonDb.Tasks.Range(func(_, value interface{}) bool {
		tunnel, ok := value.(*file.Tunnel)
		if !ok || tunnel == nil {
			return true
		}
		tunnel.RLock()
		clientID, status := 0, tunnel.Status
		if tunnel.Client != nil {
			clientID = tunnel.Client.Id
		}
		tunnel.RUnlock()
		if allowed != nil {
			if _, ok := allowed[clientID]; !ok {
				return true
			}
		}
		if status {
			tunnels++
		}
		return true
	})
	db.JsonDb.Hosts.Range(func(_, value interface{}) bool {
		host, ok := value.(*file.Host)
		if !ok || host == nil {
			return true
		}
		host.RLock()
		clientID, closed := 0, host.IsClose
		if host.Client != nil {
			clientID = host.Client.Id
		}
		host.RUnlock()
		if allowed != nil {
			if _, ok := allowed[clientID]; !ok {
				return true
			}
		}
		if !closed {
			hosts++
		}
		return true
	})
	return mcpJSON(map[string]any{
		"clients": len(clients), "tunnels": tunnels, "hosts": hosts, "users": users,
	})
}

func mcpClients(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	principal, _ := mcpPrincipalFrom(ctx)
	args := request.GetArguments()
	search := mcpArgString(args, "search")
	clientID := mcpArgInt(args, "client_id")
	rows, _ := file.GetDb().GetClientListForAllowedIds(0, 200, search, "id", "asc", clientID, principal.allowedClientIDs())
	dtos := make([]mcpClientDTO, 0, len(rows))
	for _, client := range rows {
		dtos = append(dtos, buildMCPClientDTO(client))
	}
	return mcpJSON(map[string]any{"total": len(dtos), "clients": dtos})
}

func mcpClientGet(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	principal, _ := mcpPrincipalFrom(ctx)
	id := mcpArgInt(request.GetArguments(), "id")
	if id <= 0 {
		return mcpError("id 不能为空"), nil
	}
	if !principal.canTouchClient(id) {
		return mcpError("无权访问该客户端"), nil
	}
	client, err := file.GetDb().GetClient(id)
	if err != nil || client == nil {
		return mcpError("客户端不存在"), nil
	}
	return mcpJSON(buildMCPClientDTO(client))
}

func mcpTunnels(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	principal, _ := mcpPrincipalFrom(ctx)
	args := request.GetArguments()
	clientID := mcpArgInt(args, "client_id")
	mode := strings.ToLower(mcpArgString(args, "mode"))
	allowed := principal.allowedClientIDs()
	dtos := make([]mcpTunnelDTO, 0)
	file.GetDb().JsonDb.Tasks.Range(func(_, value interface{}) bool {
		tunnel, ok := value.(*file.Tunnel)
		if !ok || tunnel == nil {
			return true
		}
		tunnel.RLock()
		ownerID := 0
		if tunnel.Client != nil {
			ownerID = tunnel.Client.Id
		}
		tunnelMode, port, status, runStatus, remark := tunnel.Mode, tunnel.Port, tunnel.Status, tunnel.RunStatus, tunnel.Remark
		target := ""
		if tunnel.Target != nil {
			target = tunnel.Target.TargetStr
		}
		tunnelID := tunnel.Id
		tunnel.RUnlock()
		if allowed != nil {
			if _, ok := allowed[ownerID]; !ok {
				return true
			}
		}
		if clientID > 0 && ownerID != clientID {
			return true
		}
		if mode != "" && strings.ToLower(tunnelMode) != mode {
			return true
		}
		dtos = append(dtos, mcpTunnelDTO{Id: tunnelID, ClientId: ownerID, Mode: tunnelMode, Port: port, Status: status, RunStatus: runStatus, Target: target, Remark: remark})
		return true
	})
	return mcpJSON(map[string]any{"total": len(dtos), "tunnels": dtos})
}

func mcpHosts(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	principal, _ := mcpPrincipalFrom(ctx)
	search := mcpArgString(request.GetArguments(), "search")
	allowed := principal.allowedClientIDs()
	dtos := make([]mcpHostDTO, 0)
	file.GetDb().JsonDb.Hosts.Range(func(_, value interface{}) bool {
		host, ok := value.(*file.Host)
		if !ok || host == nil {
			return true
		}
		host.RLock()
		ownerID := 0
		if host.Client != nil {
			ownerID = host.Client.Id
		}
		hostName, scheme, location, remark, closed := host.Host, host.Scheme, host.Location, host.Remark, host.IsClose
		hostID := host.Id
		host.RUnlock()
		if allowed != nil {
			if _, ok := allowed[ownerID]; !ok {
				return true
			}
		}
		if search != "" && !strings.Contains(strings.ToLower(hostName), strings.ToLower(search)) && !strings.Contains(strings.ToLower(remark), strings.ToLower(search)) {
			return true
		}
		dtos = append(dtos, mcpHostDTO{Id: hostID, ClientId: ownerID, Host: hostName, Scheme: scheme, Location: location, Remark: remark, IsClose: closed})
		return true
	})
	return mcpJSON(map[string]any{"total": len(dtos), "hosts": dtos})
}

func mcpUsers(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	principal, _ := mcpPrincipalFrom(ctx)
	if principal == nil || !principal.IsAdmin {
		return mcpError("仅管理员可用"), nil
	}
	rows, _ := file.GetDb().GetUserList(0, 200, mcpArgString(request.GetArguments(), "search"))
	dtos := make([]mcpUserDTO, 0, len(rows))
	for _, user := range rows {
		user.RLock()
		dtos = append(dtos, mcpUserDTO{Id: user.Id, UserName: user.UserName, Email: user.Email, Status: user.Status, Remark: user.Remark})
		user.RUnlock()
	}
	return mcpJSON(map[string]any{"total": len(dtos), "users": dtos})
}

func mcpAudit(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	principal, _ := mcpPrincipalFrom(ctx)
	if principal == nil || !principal.IsAdmin {
		return mcpError("仅管理员可用"), nil
	}
	limit := mcpArgInt(request.GetArguments(), "limit")
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	audit.Configure(beego.AppConfig.String("audit_log_path"))
	page, err := audit.List(audit.Filter{Limit: limit})
	if err != nil {
		return mcpError("读取审计失败: " + err.Error()), nil
	}
	return mcpJSON(map[string]any{"total": page.Total, "events": page.Events})
}

func mcpGlobalConfig(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	principal, _ := mcpPrincipalFrom(ctx)
	if principal == nil || !principal.IsAdmin {
		return mcpError("仅管理员可用"), nil
	}
	keys := []string{
		"web_port", "web_host", "web_base_url", "web_open_ssl",
		"bridge_port", "tls_bridge_port", "bridge_type", "allow_user_login",
		"allow_user_register", "open_captcha", "force_pow", "mcp_enabled",
		"flow_limit_type", "client_history_limit", "timezone",
	}
	values := make(map[string]any, len(keys))
	for _, key := range keys {
		values[key] = beego.AppConfig.String(key)
	}
	return mcpJSON(map[string]any{"config": values})
}

type mcpClientDTO struct {
	Id         int    `json:"id"`
	UserId     int    `json:"user_id"`
	Remark     string `json:"remark"`
	Status     bool   `json:"status"`
	IsConnect  bool   `json:"is_connect"`
	Version    string `json:"version"`
	Addr       string `json:"addr"`
	LocalAddr  string `json:"local_addr"`
	RateLimit  int    `json:"rate_limit_kb"`
	MaxConn    int    `json:"max_conn"`
	NowConn    int32  `json:"now_conn"`
	MaxTunnel  int    `json:"max_tunnel"`
	FlowLimit  int64  `json:"flow_limit_kb"`
	ExpireTime string `json:"expire_time"`
	CreateTime string `json:"create_time"`
}

func buildMCPClientDTO(client *file.Client) mcpClientDTO {
	client.RLock()
	dto := mcpClientDTO{
		Id:         client.Id,
		UserId:     client.UserId,
		Remark:     client.Remark,
		Status:     client.Status,
		IsConnect:  client.IsConnect,
		Version:    client.Version,
		Addr:       client.Addr,
		LocalAddr:  client.LocalAddr,
		RateLimit:  client.RateLimit,
		MaxConn:    client.MaxConn,
		NowConn:    client.NowConn,
		MaxTunnel:  client.MaxTunnelNum,
		ExpireTime: client.ExpireTime,
		CreateTime: client.CreateTime,
	}
	if client.Flow != nil {
		dto.FlowLimit = client.Flow.FlowLimit
	}
	client.RUnlock()
	return dto
}

type mcpTunnelDTO struct {
	Id        int    `json:"id"`
	ClientId  int    `json:"client_id"`
	Mode      string `json:"mode"`
	Port      int    `json:"port"`
	Status    bool   `json:"status"`
	RunStatus bool   `json:"run_status"`
	Target    string `json:"target"`
	Remark    string `json:"remark"`
}

type mcpHostDTO struct {
	Id       int    `json:"id"`
	ClientId int    `json:"client_id"`
	Host     string `json:"host"`
	Scheme   string `json:"scheme"`
	Location string `json:"location"`
	Remark   string `json:"remark"`
	IsClose  bool   `json:"is_close"`
}

type mcpUserDTO struct {
	Id       int    `json:"id"`
	UserName string `json:"username"`
	Email    string `json:"email"`
	Status   bool   `json:"status"`
	Remark   string `json:"remark"`
}

func registerMCPClientTools(s *server.MCPServer) {
	s.AddTool(mcp.NewTool("nps_client_create",
		mcp.WithDescription("创建客户端。管理员可指定 user_id，普通用户固定归属自己。"),
		mcp.WithString("remark", mcp.Description("备注")),
		mcp.WithNumber("user_id", mcp.Description("归属用户 ID（仅管理员）")),
		mcp.WithNumber("max_conn", mcp.Description("最大连接数，0 表示不限")),
		mcp.WithNumber("max_tunnel", mcp.Description("最大隧道数，0 表示不限")),
		mcp.WithNumber("rate_limit", mcp.Description("限速 KB/s，0 表示不限")),
	), mcpClientCreate)

	s.AddTool(mcp.NewTool("nps_client_update",
		mcp.WithDescription("更新客户端备注或限额字段。"),
		mcp.WithNumber("id", mcp.Required(), mcp.Description("客户端 ID")),
		mcp.WithString("remark", mcp.Description("备注")),
		mcp.WithNumber("max_conn", mcp.Description("最大连接数")),
		mcp.WithNumber("max_tunnel", mcp.Description("最大隧道数")),
		mcp.WithNumber("rate_limit", mcp.Description("限速 KB/s")),
	), mcpClientUpdate)

	s.AddTool(mcp.NewTool("nps_client_delete",
		mcp.WithDescription("删除客户端及其隧道与域名反代。"),
		mcp.WithNumber("id", mcp.Required(), mcp.Description("客户端 ID")),
	), mcpClientDelete)

	s.AddTool(mcp.NewTool("nps_client_set_status",
		mcp.WithDescription("启用或停用客户端（仅管理员）。"),
		mcp.WithNumber("id", mcp.Required(), mcp.Description("客户端 ID")),
		mcp.WithBoolean("status", mcp.Required(), mcp.Description("true 启用，false 停用")),
	), mcpClientSetStatus)
}

func mcpClientCreate(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	principal, _ := mcpPrincipalFrom(ctx)
	args := request.GetArguments()
	client := &file.Client{
		Remark:       mcpArgString(args, "remark"),
		Status:       true,
		MaxConn:      mcpArgInt(args, "max_conn"),
		MaxTunnelNum: mcpArgInt(args, "max_tunnel"),
		RateLimit:    mcpArgInt(args, "rate_limit"),
		Flow:         &file.Flow{FlowLimit: 0},
		CreateTime:   time.Now().Format("2006-01-02 15:04:05"),
	}
	if principal.IsAdmin {
		client.UserId = mcpArgInt(args, "user_id")
	} else {
		client.UserId = principal.UserID
	}
	if err := file.GetDb().NewClient(client); err != nil {
		return mcpError("创建客户端失败: " + err.Error()), nil
	}
	return mcpJSON(map[string]any{"id": client.Id, "vkey": client.VerifyKey})
}

func mcpClientUpdate(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	principal, _ := mcpPrincipalFrom(ctx)
	args := request.GetArguments()
	id := mcpArgInt(args, "id")
	if !principal.canTouchClient(id) {
		return mcpError("无权操作该客户端"), nil
	}
	client, err := file.GetDb().GetClient(id)
	if err != nil || client == nil {
		return mcpError("客户端不存在"), nil
	}
	client.Lock()
	if remark, ok := args["remark"].(string); ok {
		client.Remark = strings.TrimSpace(remark)
	}
	if v, ok := args["max_conn"]; ok {
		client.MaxConn = mcpArgInt(args, "max_conn")
		_ = v
	}
	if _, ok := args["max_tunnel"]; ok {
		client.MaxTunnelNum = mcpArgInt(args, "max_tunnel")
	}
	if _, ok := args["rate_limit"]; ok {
		client.RateLimit = mcpArgInt(args, "rate_limit")
	}
	client.Unlock()
	if err := file.GetDb().UpdateClient(client); err != nil {
		return mcpError("更新客户端失败: " + err.Error()), nil
	}
	return mcpJSON(map[string]any{"id": id})
}

func mcpClientDelete(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	principal, _ := mcpPrincipalFrom(ctx)
	id := mcpArgInt(request.GetArguments(), "id")
	if !principal.canTouchClient(id) {
		return mcpError("无权操作该客户端"), nil
	}
	if principal.IsAdmin {
		if err := file.GetDb().DelClient(id); err != nil {
			return mcpError("删除失败: " + err.Error()), nil
		}
	} else {
		if err := file.GetDb().DelClientForUser(id, principal.UserID); err != nil {
			return mcpError("删除失败: " + err.Error()), nil
		}
	}
	return mcpJSON(map[string]any{"deleted": id})
}

func mcpClientSetStatus(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	principal, _ := mcpPrincipalFrom(ctx)
	if principal == nil || !principal.IsAdmin {
		return mcpError("仅管理员可用"), nil
	}
	args := request.GetArguments()
	id := mcpArgInt(args, "id")
	client, err := file.GetDb().GetClient(id)
	if err != nil || client == nil {
		return mcpError("客户端不存在"), nil
	}
	client.Lock()
	client.Status = mcpArgBool(args, "status")
	client.Unlock()
	if err := file.GetDb().UpdateClient(client); err != nil {
		return mcpError("更新状态失败: " + err.Error()), nil
	}
	return mcpJSON(map[string]any{"id": id, "status": client.Status})
}

func registerMCPTunnelTools(s *server.MCPServer) {
	s.AddTool(mcp.NewTool("nps_tunnel_create",
		mcp.WithDescription("创建隧道。"),
		mcp.WithString("mode", mcp.Required(), mcp.Description("模式：tcp、udp、socks5、httpProxy、file、p2p 等")),
		mcp.WithNumber("port", mcp.Required(), mcp.Description("监听端口")),
		mcp.WithNumber("client_id", mcp.Required(), mcp.Description("客户端 ID")),
		mcp.WithString("target", mcp.Description("目标地址，如 127.0.0.1:8080；多个换行分隔")),
		mcp.WithString("remark", mcp.Description("备注")),
	), mcpTunnelCreate)

	s.AddTool(mcp.NewTool("nps_tunnel_delete",
		mcp.WithDescription("删除隧道。"),
		mcp.WithNumber("id", mcp.Required(), mcp.Description("隧道 ID")),
	), mcpTunnelDelete)

	s.AddTool(mcp.NewTool("nps_tunnel_set_status",
		mcp.WithDescription("启用或停用隧道。"),
		mcp.WithNumber("id", mcp.Required(), mcp.Description("隧道 ID")),
		mcp.WithBoolean("status", mcp.Required(), mcp.Description("true 启用，false 停用")),
	), mcpTunnelSetStatus)
}

func mcpTunnelCreate(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	principal, _ := mcpPrincipalFrom(ctx)
	args := request.GetArguments()
	clientID := mcpArgInt(args, "client_id")
	if !principal.canTouchClient(clientID) {
		return mcpError("无权操作该客户端"), nil
	}
	client, err := file.GetDb().GetClient(clientID)
	if err != nil || client == nil {
		return mcpError("客户端不存在"), nil
	}
	tunnel := &file.Tunnel{
		Mode:   strings.ToLower(mcpArgString(args, "mode")),
		Port:   mcpArgInt(args, "port"),
		Status: true,
		Client: client,
		Remark: mcpArgString(args, "remark"),
		Target: &file.Target{TargetStr: mcpArgString(args, "target")},
		Flow:   &file.Flow{FlowLimit: 0},
	}
	if err := file.GetDb().NewTask(tunnel); err != nil {
		return mcpError("创建隧道失败: " + err.Error()), nil
	}
	return mcpJSON(map[string]any{"id": tunnel.Id})
}

func mcpTunnelDelete(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	principal, _ := mcpPrincipalFrom(ctx)
	id := mcpArgInt(request.GetArguments(), "id")
	tunnel, err := file.GetDb().GetTask(id)
	if err != nil || tunnel == nil {
		return mcpError("隧道不存在"), nil
	}
	tunnel.RLock()
	clientID := 0
	if tunnel.Client != nil {
		clientID = tunnel.Client.Id
	}
	tunnel.RUnlock()
	if !principal.canTouchClient(clientID) {
		return mcpError("无权操作该隧道"), nil
	}
	if err := file.GetDb().DelTask(id); err != nil {
		return mcpError("删除失败: " + err.Error()), nil
	}
	return mcpJSON(map[string]any{"deleted": id})
}

func mcpTunnelSetStatus(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	principal, _ := mcpPrincipalFrom(ctx)
	args := request.GetArguments()
	id := mcpArgInt(args, "id")
	tunnel, err := file.GetDb().GetTask(id)
	if err != nil || tunnel == nil {
		return mcpError("隧道不存在"), nil
	}
	tunnel.RLock()
	clientID := 0
	if tunnel.Client != nil {
		clientID = tunnel.Client.Id
	}
	tunnel.RUnlock()
	if !principal.canTouchClient(clientID) {
		return mcpError("无权操作该隧道"), nil
	}
	tunnel.Lock()
	tunnel.Status = mcpArgBool(args, "status")
	tunnel.Unlock()
	if err := file.GetDb().UpdateTask(tunnel); err != nil {
		return mcpError("更新状态失败: " + err.Error()), nil
	}
	return mcpJSON(map[string]any{"id": id, "status": tunnel.Status})
}

func registerMCPHostTools(s *server.MCPServer) {
	s.AddTool(mcp.NewTool("nps_host_create",
		mcp.WithDescription("创建域名反代规则。"),
		mcp.WithString("host", mcp.Required(), mcp.Description("域名")),
		mcp.WithNumber("client_id", mcp.Required(), mcp.Description("客户端 ID")),
		mcp.WithString("scheme", mcp.Description("http、https 或 all，默认 all")),
		mcp.WithString("location", mcp.Description("路径前缀，默认 /")),
		mcp.WithString("target", mcp.Description("目标地址，如 127.0.0.1:8080")),
		mcp.WithString("remark", mcp.Description("备注")),
	), mcpHostCreate)

	s.AddTool(mcp.NewTool("nps_host_delete",
		mcp.WithDescription("删除域名反代规则。"),
		mcp.WithNumber("id", mcp.Required(), mcp.Description("规则 ID")),
	), mcpHostDelete)

	s.AddTool(mcp.NewTool("nps_host_set_status",
		mcp.WithDescription("启用或停用域名反代规则。"),
		mcp.WithNumber("id", mcp.Required(), mcp.Description("规则 ID")),
		mcp.WithBoolean("status", mcp.Required(), mcp.Description("true 启用，false 停用")),
	), mcpHostSetStatus)
}

func mcpHostCreate(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	principal, _ := mcpPrincipalFrom(ctx)
	args := request.GetArguments()
	clientID := mcpArgInt(args, "client_id")
	if !principal.canTouchClient(clientID) {
		return mcpError("无权操作该客户端"), nil
	}
	client, err := file.GetDb().GetClient(clientID)
	if err != nil || client == nil {
		return mcpError("客户端不存在"), nil
	}
	scheme := strings.ToLower(mcpArgString(args, "scheme"))
	if scheme == "" {
		scheme = "all"
	}
	location := mcpArgString(args, "location")
	if location == "" {
		location = "/"
	}
	host := &file.Host{
		Host:     strings.ToLower(mcpArgString(args, "host")),
		Scheme:   scheme,
		Location: location,
		Client:   client,
		Target:   &file.Target{TargetStr: mcpArgString(args, "target")},
		Remark:   mcpArgString(args, "remark"),
	}
	if err := file.GetDb().NewHost(host); err != nil {
		return mcpError("创建域名反代失败: " + err.Error()), nil
	}
	return mcpJSON(map[string]any{"id": host.Id})
}

func mcpHostDelete(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	principal, _ := mcpPrincipalFrom(ctx)
	id := mcpArgInt(request.GetArguments(), "id")
	host, err := file.GetDb().GetHostById(id)
	if err != nil || host == nil {
		return mcpError("域名反代不存在"), nil
	}
	host.RLock()
	clientID := 0
	if host.Client != nil {
		clientID = host.Client.Id
	}
	host.RUnlock()
	if !principal.canTouchClient(clientID) {
		return mcpError("无权操作该域名反代"), nil
	}
	if err := file.GetDb().DelHost(id); err != nil {
		return mcpError("删除失败: " + err.Error()), nil
	}
	return mcpJSON(map[string]any{"deleted": id})
}

func mcpHostSetStatus(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	principal, _ := mcpPrincipalFrom(ctx)
	args := request.GetArguments()
	id := mcpArgInt(args, "id")
	host, err := file.GetDb().GetHostById(id)
	if err != nil || host == nil {
		return mcpError("域名反代不存在"), nil
	}
	host.RLock()
	clientID := 0
	if host.Client != nil {
		clientID = host.Client.Id
	}
	host.RUnlock()
	if !principal.canTouchClient(clientID) {
		return mcpError("无权操作该域名反代"), nil
	}
	host.Lock()
	host.IsClose = !mcpArgBool(args, "status")
	host.Unlock()
	if err := file.GetDb().UpdateHost(host); err != nil {
		return mcpError("更新状态失败: " + err.Error()), nil
	}
	return mcpJSON(map[string]any{"id": id, "is_close": host.IsClose})
}
