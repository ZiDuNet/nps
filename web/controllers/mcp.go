package controllers

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"ehang.io/nps/lib/file"
	"ehang.io/nps/lib/rate"
	"ehang.io/nps/lib/version"
	npsserver "ehang.io/nps/server"
	"ehang.io/nps/server/tool"
	"ehang.io/nps/web/audit"
	"github.com/astaxie/beego"
	"github.com/astaxie/beego/logs"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

type mcpPrincipal struct {
	Token   string
	IsAdmin bool
	UserID  int
}

type mcpPrincipalKey struct{}

// NewMCPHandler returns the Streamable HTTP MCP endpoint at /mcp. The opt-in
// switch is checked per request so an administrator can enable it from the
// settings panel without restarting NPS. When disabled, callers still receive
// an unauthenticated 404 so the endpoint's state is not exposed. When enabled,
// authentication runs before MCP method handling, so unauthenticated requests
// receive 401 even for methods that the stateless endpoint does not support.
func NewMCPHandler() http.Handler {
	mcpServer := server.NewMCPServer("nps", version.VERSION, server.WithToolCapabilities(true))
	registerMCPTools(mcpServer)
	handler := server.NewStreamableHTTPServer(mcpServer,
		server.WithHTTPContextFunc(func(ctx context.Context, r *http.Request) context.Context {
			if principal, ok := resolveMCPPrincipal(r); ok {
				return context.WithValue(ctx, mcpPrincipalKey{}, principal)
			}
			return ctx
		}),
		// Stateless MCP does not mint a session ID and ignores one if a client
		// sends a stale or unknown Mcp-Session-Id header.
		server.WithStateLess(true),
		server.WithDisableStreaming(true),
		server.WithDisableLocalhostProtection(true),
	)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !beego.AppConfig.DefaultBool("mcp_enabled", true) {
			http.NotFound(w, r)
			return
		}
		if _, ok := resolveMCPPrincipal(r); !ok {
			w.Header().Set("WWW-Authenticate", `Bearer realm="nps"`)
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"status":0,"code":"unauthorized","msg":"缺少或无效的 Bearer Token","msg_en":"Missing or invalid Bearer token"}` + "\n"))
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

// mcpSubmitsClientLimits reports whether the request attempted to change a
// client-wide quota. Ordinary users manage their own devices but cannot widen
// the resource limits an administrator assigned.
func mcpSubmitsClientLimits(args map[string]any) bool {
	for _, key := range []string{"max_conn", "max_tunnel", "rate_limit"} {
		if _, ok := args[key]; ok {
			return true
		}
	}
	return false
}

// mcpClientLimits parses the optional client quotas. Client limits use the
// historical zero-means-unlimited encoding, and rate_limit is expressed in
// Mbps while Client.RateLimit stores bps.
func mcpClientLimits(args map[string]any) (maxConn, maxTunnel int, rateLimitBps int64, err error) {
	parseInt := func(key string) (int, error) {
		value, ok := args[key]
		if !ok || value == nil {
			return 0, nil
		}
		parsed := mcpArgInt(args, key)
		if parsed < 0 {
			return 0, errors.New("客户端配额、连接数和带宽限制不能小于 0")
		}
		return parsed, nil
	}
	if maxConn, err = parseInt("max_conn"); err != nil {
		return 0, 0, 0, err
	}
	if maxTunnel, err = parseInt("max_tunnel"); err != nil {
		return 0, 0, 0, err
	}
	var rateLimit *int64
	rateLimit, err = file.ParseLegacyMbpsAlias(mcpArgRawString(args, "rate_limit"))
	if err != nil {
		return 0, 0, 0, err
	}
	if rateLimit == nil {
		rateLimit = file.LimitInt64(0)
	}
	if *rateLimit < 0 {
		return 0, 0, 0, errors.New("带宽限制不能小于 0")
	}
	return maxConn, maxTunnel, *rateLimit, nil
}

func mcpArgRawString(args map[string]any, key string) string {
	switch value := args[key].(type) {
	case string:
		return strings.TrimSpace(value)
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64)
	case int:
		return strconv.Itoa(value)
	case int64:
		return strconv.FormatInt(value, 10)
	}
	return ""
}

func mcpJSON(data any) (*mcp.CallToolResult, error) {
	return mcp.NewToolResultJSON(data)
}

func mcpError(text string) *mcp.CallToolResult {
	return mcp.NewToolResultError(text)
}

func mcpRequestPrincipal(ctx context.Context) (*mcpPrincipal, *mcp.CallToolResult) {
	principal, ok := mcpPrincipalFrom(ctx)
	if !ok || principal == nil {
		return nil, mcpError("认证主体缺失，请重新连接")
	}
	return principal, nil
}

func newMCPTool(name, title string, readOnly, destructive, idempotent bool, options ...mcp.ToolOption) mcp.Tool {
	all := []mcp.ToolOption{
		mcp.WithoutDefaultAnnotations(),
		mcp.WithTitleAnnotation(title),
		mcp.WithReadOnlyHintAnnotation(readOnly),
		mcp.WithDestructiveHintAnnotation(destructive),
		mcp.WithIdempotentHintAnnotation(idempotent),
		mcp.WithOpenWorldHintAnnotation(false),
	}
	all = append(all, options...)
	return mcp.NewTool(name, all...)
}

func mcpPageBounds(args map[string]any, defaultLimit int) (offset, limit int) {
	offset = mcpArgInt(args, "offset")
	if offset < 0 {
		offset = 0
	}
	limit = mcpArgInt(args, "limit")
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > 500 {
		limit = 500
	}
	return offset, limit
}

func mcpPage[T any](rows []T, offset, limit int) []T {
	if offset >= len(rows) {
		return []T{}
	}
	end := offset + limit
	if end > len(rows) {
		end = len(rows)
	}
	return rows[offset:end]
}

func registerMCPTools(s *server.MCPServer) {
	registerMCPReadTools(s)
	registerMCPClientTools(s)
	registerMCPTunnelTools(s)
	registerMCPHostTools(s)
}

func registerMCPReadTools(s *server.MCPServer) {
	s.AddTool(newMCPTool("nps_dashboard", "查看仪表盘", true, false, true,
		mcp.WithDescription("返回仪表盘概览：客户端、隧道、域名反代数量与在线客户端数。"),
	), mcpDashboard)

	s.AddTool(newMCPTool("nps_clients", "列出客户端", true, false, true,
		mcp.WithDescription("列出客户端，可按备注/ID 搜索，或指定单个客户端 ID。"),
		mcp.WithString("search", mcp.Description("按备注、VKey 或 ID 模糊搜索")),
		mcp.WithNumber("client_id", mcp.Description("只返回指定客户端 ID")),
		mcp.WithNumber("offset", mcp.Description("分页偏移，默认 0")),
		mcp.WithNumber("limit", mcp.Description("返回条数，默认 100，最大 500")),
	), mcpClients)

	s.AddTool(newMCPTool("nps_client_get", "查看客户端", true, false, true,
		mcp.WithDescription("返回单个客户端的详细信息。"),
		mcp.WithNumber("id", mcp.Required(), mcp.Description("客户端 ID")),
	), mcpClientGet)

	s.AddTool(newMCPTool("nps_tunnels", "列出隧道", true, false, true,
		mcp.WithDescription("列出隧道，可按客户端 ID 或模式过滤。"),
		mcp.WithNumber("client_id", mcp.Description("按客户端 ID 过滤")),
		mcp.WithString("mode", mcp.Description("按模式过滤：tcp、udp、socks5、httpProxy、file、p2p 等")),
		mcp.WithNumber("offset", mcp.Description("分页偏移，默认 0")),
		mcp.WithNumber("limit", mcp.Description("返回条数，默认 100，最大 500")),
	), mcpTunnels)

	s.AddTool(newMCPTool("nps_hosts", "列出域名反代", true, false, true,
		mcp.WithDescription("列出域名反代规则。"),
		mcp.WithString("search", mcp.Description("按域名或备注模糊搜索")),
		mcp.WithNumber("offset", mcp.Description("分页偏移，默认 0")),
		mcp.WithNumber("limit", mcp.Description("返回条数，默认 100，最大 500")),
	), mcpHosts)

	s.AddTool(newMCPTool("nps_users", "列出用户", true, false, true,
		mcp.WithDescription("列出用户（仅管理员）。"),
		mcp.WithString("search", mcp.Description("按用户名或邮箱模糊搜索")),
		mcp.WithNumber("offset", mcp.Description("分页偏移，默认 0")),
		mcp.WithNumber("limit", mcp.Description("返回条数，默认 100，最大 500")),
	), mcpUsers)

	s.AddTool(newMCPTool("nps_audit", "查看操作审计", true, false, true,
		mcp.WithDescription("列出当前账号可见的操作审计事件；管理员查看全局，普通用户仅查看本人范围。"),
		mcp.WithNumber("limit", mcp.Description("返回条数，默认 50，最大 500")),
		mcp.WithNumber("offset", mcp.Description("分页偏移，默认 0")),
	), mcpAudit)

	s.AddTool(newMCPTool("nps_global_config", "查看全局配置", true, false, true,
		mcp.WithDescription("返回全局配置的脱敏概览（仅管理员）。"),
	), mcpGlobalConfig)
}

func mcpDashboard(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	principal, failure := mcpRequestPrincipal(ctx)
	if failure != nil {
		return failure, nil
	}
	db := file.GetDb()
	allowed := principal.allowedClientIDs()
	clients, onlineClients, tunnels, hosts, users := 0, 0, 0, 0, 0
	db.JsonDb.Clients.Range(func(_, value interface{}) bool {
		client, ok := value.(*file.Client)
		if !ok || client == nil {
			return true
		}
		client.RLock()
		clientID, connected, noDisplay := client.Id, client.IsConnect, client.NoDisplay
		client.RUnlock()
		if noDisplay {
			return true
		}
		if allowed != nil {
			if _, ok := allowed[clientID]; !ok {
				return true
			}
		}
		clients++
		if connected {
			onlineClients++
		}
		return true
	})
	if principal.IsAdmin {
		_, users = db.GetUserList(0, 0, "")
	}
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
		"clients": clients, "online_clients": onlineClients, "tunnels": tunnels, "hosts": hosts, "users": users,
	})
}

func mcpClients(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	principal, failure := mcpRequestPrincipal(ctx)
	if failure != nil {
		return failure, nil
	}
	args := request.GetArguments()
	search := mcpArgString(args, "search")
	clientID := mcpArgInt(args, "client_id")
	offset, limit := mcpPageBounds(args, 100)
	rows, total := file.GetDb().GetClientListForAllowedIds(offset, limit, search, "id", "asc", clientID, principal.allowedClientIDs())
	dtos := make([]mcpClientDTO, 0, len(rows))
	for _, client := range rows {
		dtos = append(dtos, buildMCPClientDTO(client))
	}
	return mcpJSON(map[string]any{"total": total, "clients": dtos, "offset": offset, "limit": limit})
}

func mcpClientGet(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	principal, failure := mcpRequestPrincipal(ctx)
	if failure != nil {
		return failure, nil
	}
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
	principal, failure := mcpRequestPrincipal(ctx)
	if failure != nil {
		return failure, nil
	}
	args := request.GetArguments()
	clientID := mcpArgInt(args, "client_id")
	mode := strings.ToLower(mcpArgString(args, "mode"))
	offset, limit := mcpPageBounds(args, 100)
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
	sort.Slice(dtos, func(i, j int) bool { return dtos[i].Id < dtos[j].Id })
	total := len(dtos)
	return mcpJSON(map[string]any{"total": total, "tunnels": mcpPage(dtos, offset, limit), "offset": offset, "limit": limit})
}

func mcpHosts(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	principal, failure := mcpRequestPrincipal(ctx)
	if failure != nil {
		return failure, nil
	}
	args := request.GetArguments()
	search := mcpArgString(args, "search")
	offset, limit := mcpPageBounds(args, 100)
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
	sort.Slice(dtos, func(i, j int) bool { return dtos[i].Id < dtos[j].Id })
	total := len(dtos)
	return mcpJSON(map[string]any{"total": total, "hosts": mcpPage(dtos, offset, limit), "offset": offset, "limit": limit})
}

func mcpUsers(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	principal, failure := mcpRequestPrincipal(ctx)
	if failure != nil {
		return failure, nil
	}
	if !principal.IsAdmin {
		return mcpError("仅管理员可用"), nil
	}
	args := request.GetArguments()
	offset, limit := mcpPageBounds(args, 100)
	rows, total := file.GetDb().GetUserList(offset, limit, mcpArgString(args, "search"))
	dtos := make([]mcpUserDTO, 0, len(rows))
	for _, user := range rows {
		user.RLock()
		dtos = append(dtos, mcpUserDTO{Id: user.Id, UserName: user.UserName, Email: user.Email, Status: user.Status, Remark: user.Remark})
		user.RUnlock()
	}
	return mcpJSON(map[string]any{"total": total, "users": dtos, "offset": offset, "limit": limit})
}

func mcpAudit(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	principal, failure := mcpRequestPrincipal(ctx)
	if failure != nil {
		return failure, nil
	}
	args := request.GetArguments()
	offset := mcpArgInt(args, "offset")
	if offset < 0 {
		offset = 0
	}
	limit := mcpArgInt(args, "limit")
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	audit.Configure(beego.AppConfig.String("audit_log_path"))
	filter := audit.Filter{Offset: offset, Limit: limit}
	if !principal.IsAdmin {
		ownerID := principal.UserID
		filter.OwnerUserID = &ownerID
	}
	page, err := audit.List(filter)
	if err != nil {
		return mcpError("读取审计失败: " + err.Error()), nil
	}
	return mcpJSON(map[string]any{"total": page.Total, "events": page.Events, "offset": offset, "limit": limit})
}

func mcpGlobalConfig(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	principal, failure := mcpRequestPrincipal(ctx)
	if failure != nil {
		return failure, nil
	}
	if !principal.IsAdmin {
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
	Id            int    `json:"id"`
	UserId        int    `json:"user_id"`
	Remark        string `json:"remark"`
	Status        bool   `json:"status"`
	IsConnect     bool   `json:"is_connect"`
	Version       string `json:"version"`
	Addr          string `json:"addr"`
	LocalAddr     string `json:"local_addr"`
	RateLimit     int64  `json:"rate_limit_bps"`
	RateLimitMbps string `json:"rate_limit_mbps"`
	MaxConn       int    `json:"max_conn"`
	NowConn       int32  `json:"now_conn"`
	MaxTunnel     int    `json:"max_tunnel"`
	FlowLimitMB   int64  `json:"flow_limit_mb"`
	ExpireTime    string `json:"expire_time"`
	CreateTime    string `json:"create_time"`
}

func buildMCPClientDTO(client *file.Client) mcpClientDTO {
	client.RLock()
	dto := mcpClientDTO{
		Id:            client.Id,
		UserId:        client.UserId,
		Remark:        client.Remark,
		Status:        client.Status,
		IsConnect:     client.IsConnect,
		Version:       client.Version,
		Addr:          client.Addr,
		LocalAddr:     client.LocalAddr,
		RateLimit:     client.RateLimit,
		RateLimitMbps: rate.FormatMbps(client.RateLimit),
		MaxConn:       client.MaxConn,
		NowConn:       client.NowConn,
		MaxTunnel:     client.MaxTunnelNum,
		ExpireTime:    client.ExpireTime,
		CreateTime:    client.CreateTime,
	}
	if client.Flow != nil {
		dto.FlowLimitMB = client.Flow.FlowLimit
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
	s.AddTool(newMCPTool("nps_client_create", "创建客户端", false, false, false,
		mcp.WithDescription("创建客户端。管理员可指定 user_id，普通用户固定归属自己。"),
		mcp.WithString("remark", mcp.Description("备注")),
		mcp.WithNumber("user_id", mcp.Description("归属用户 ID（仅管理员）")),
		mcp.WithNumber("max_conn", mcp.Description("最大连接数，0 表示不限")),
		mcp.WithNumber("max_tunnel", mcp.Description("最大隧道数，0 表示不限")),
		mcp.WithNumber("rate_limit", mcp.Description("客户端共享双向合计带宽，单位 Mbps；0 表示不限")),
	), mcpClientCreate)

	s.AddTool(newMCPTool("nps_client_update", "更新客户端", false, false, true,
		mcp.WithDescription("更新客户端备注或限额字段。"),
		mcp.WithNumber("id", mcp.Required(), mcp.Description("客户端 ID")),
		mcp.WithString("remark", mcp.Description("备注")),
		mcp.WithNumber("max_conn", mcp.Description("最大连接数")),
		mcp.WithNumber("max_tunnel", mcp.Description("最大隧道数")),
		mcp.WithNumber("rate_limit", mcp.Description("客户端共享双向合计带宽，单位 Mbps；0 表示不限")),
	), mcpClientUpdate)

	s.AddTool(newMCPTool("nps_client_delete", "删除客户端", false, true, true,
		mcp.WithDescription("删除客户端及其隧道与域名反代。"),
		mcp.WithNumber("id", mcp.Required(), mcp.Description("客户端 ID")),
	), mcpClientDelete)

	s.AddTool(newMCPTool("nps_client_set_status", "启停客户端", false, false, true,
		mcp.WithDescription("启用或停用客户端（仅管理员）。"),
		mcp.WithNumber("id", mcp.Required(), mcp.Description("客户端 ID")),
		mcp.WithBoolean("status", mcp.Required(), mcp.Description("true 启用，false 停用")),
	), mcpClientSetStatus)
}

func mcpClientCreate(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	principal, failure := mcpRequestPrincipal(ctx)
	if failure != nil {
		return failure, nil
	}
	args := request.GetArguments()
	if !principal.IsAdmin && mcpSubmitsClientLimits(args) {
		return mcpError("普通用户不能设置客户端资源限额"), nil
	}
	maxConn, maxTunnel, rateLimitBps, err := mcpClientLimits(args)
	if err != nil {
		return mcpError(err.Error()), nil
	}
	client := &file.Client{
		Remark:       mcpArgString(args, "remark"),
		Status:       true,
		MaxConn:      maxConn,
		MaxTunnelNum: maxTunnel,
		RateLimit:    rateLimitBps,
		Flow:         &file.Flow{FlowLimit: 0},
		CreateTime:   time.Now().Format("2006-01-02 15:04:05"),
	}
	if principal.IsAdmin {
		client.UserId = mcpArgInt(args, "user_id")
	} else {
		client.UserId = principal.UserID
	}
	create := file.GetDb().NewClient
	if principal.IsAdmin {
		create = file.GetDb().NewClientAsAdmin
	}
	if err := create(client); err != nil {
		return mcpError("创建客户端失败: " + err.Error()), nil
	}
	return mcpJSON(map[string]any{"id": client.Id, "vkey": client.VerifyKey})
}

func mcpClientUpdate(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	principal, failure := mcpRequestPrincipal(ctx)
	if failure != nil {
		return failure, nil
	}
	args := request.GetArguments()
	id := mcpArgInt(args, "id")
	if !principal.canTouchClient(id) {
		return mcpError("无权操作该客户端"), nil
	}
	if !principal.IsAdmin && mcpSubmitsClientLimits(args) {
		return mcpError("普通用户不能设置客户端资源限额"), nil
	}
	maxConn, maxTunnel, rateLimitBps, err := mcpClientLimits(args)
	if err != nil {
		return mcpError(err.Error()), nil
	}
	client, err := file.GetDb().GetClient(id)
	if err != nil || client == nil {
		return mcpError("客户端不存在"), nil
	}
	client.Lock()
	if remark, ok := args["remark"].(string); ok {
		client.Remark = strings.TrimSpace(remark)
	}
	if principal.IsAdmin {
		if _, ok := args["max_conn"]; ok {
			client.MaxConn = maxConn
		}
		if _, ok := args["max_tunnel"]; ok {
			client.MaxTunnelNum = maxTunnel
		}
		if _, ok := args["rate_limit"]; ok {
			client.RateLimit = rateLimitBps
		}
	}
	client.Unlock()
	update := file.GetDb().UpdateClient
	if principal.IsAdmin {
		update = file.GetDb().UpdateClientAsAdmin
	}
	if err := update(client); err != nil {
		return mcpError("更新客户端失败: " + err.Error()), nil
	}
	return mcpJSON(map[string]any{"id": id})
}

func mcpClientDelete(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	principal, failure := mcpRequestPrincipal(ctx)
	if failure != nil {
		return failure, nil
	}
	id := mcpArgInt(request.GetArguments(), "id")
	if !principal.canTouchClient(id) {
		return mcpError("无权操作该客户端"), nil
	}
	var committedClients []*file.Client
	var committedTasks []*file.Tunnel
	afterCommit := func(clients []*file.Client, tasks []*file.Tunnel, _ []*file.Host) {
		committedClients = clients
		committedTasks = tasks
	}
	if principal.IsAdmin {
		if err := file.GetDb().DelClientCascadeWithHook(id, afterCommit); err != nil {
			return mcpError("删除失败: " + err.Error()), nil
		}
	} else {
		if err := file.GetDb().DelClientCascadeForUserWithHook(id, principal.UserID, afterCommit); err != nil {
			return mcpError("删除失败: " + err.Error()), nil
		}
	}
	clientID := id
	if len(committedClients) > 0 && committedClients[0] != nil {
		clientID = committedClients[0].Id
	}
	if cleanupErr := npsserver.FinalizeCommittedClientDeletion(clientID, committedTasks); cleanupErr != nil {
		logs.Warn("client %d was deleted but runtime cleanup failed: %v", clientID, cleanupErr)
	}
	return mcpJSON(map[string]any{"deleted": id})
}

func mcpClientSetStatus(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	principal, failure := mcpRequestPrincipal(ctx)
	if failure != nil {
		return failure, nil
	}
	if !principal.IsAdmin {
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
	if err := file.GetDb().UpdateClientAsAdmin(client); err != nil {
		return mcpError("更新状态失败: " + err.Error()), nil
	}
	if !mcpArgBool(args, "status") {
		npsserver.DelClientConnect(id)
	}
	return mcpJSON(map[string]any{"id": id, "status": client.Status})
}

func registerMCPTunnelTools(s *server.MCPServer) {
	s.AddTool(newMCPTool("nps_tunnel_create", "创建隧道", false, false, false,
		mcp.WithDescription("创建隧道。"),
		mcp.WithString("mode", mcp.Required(), mcp.Description("模式：tcp、udp、socks5、httpProxy、file、p2p 等")),
		mcp.WithNumber("port", mcp.Required(), mcp.Description("监听端口")),
		mcp.WithNumber("client_id", mcp.Required(), mcp.Description("客户端 ID")),
		mcp.WithString("target", mcp.Description("目标地址，如 127.0.0.1:8080；多个换行分隔")),
		mcp.WithString("remark", mcp.Description("备注")),
	), mcpTunnelCreate)

	s.AddTool(newMCPTool("nps_tunnel_delete", "删除隧道", false, true, true,
		mcp.WithDescription("删除隧道。"),
		mcp.WithNumber("id", mcp.Required(), mcp.Description("隧道 ID")),
	), mcpTunnelDelete)

	s.AddTool(newMCPTool("nps_tunnel_set_status", "启停隧道", false, false, true,
		mcp.WithDescription("启用或停用隧道。"),
		mcp.WithNumber("id", mcp.Required(), mcp.Description("隧道 ID")),
		mcp.WithBoolean("status", mcp.Required(), mcp.Description("true 启用，false 停用")),
	), mcpTunnelSetStatus)
}

func mcpTunnelCreate(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	principal, failure := mcpRequestPrincipal(ctx)
	if failure != nil {
		return failure, nil
	}
	args := request.GetArguments()
	clientID := mcpArgInt(args, "client_id")
	if !principal.canTouchClient(clientID) {
		return mcpError("无权操作该客户端"), nil
	}
	client, err := file.GetDb().GetClient(clientID)
	if err != nil || client == nil {
		return mcpError("客户端不存在"), nil
	}
	mode := mcpArgString(args, "mode")
	if !supportedTunnelMode(mode) {
		return mcpError("不支持的隧道模式"), nil
	}
	target := mcpArgString(args, "target")
	if err := validateBridgeTargetPermission(principal.IsAdmin, target); err != nil {
		return mcpError(err.Error()), nil
	}
	port := mcpArgInt(args, "port")
	if port < 0 || port > 65535 {
		return mcpError("端口必须在 0..65535 范围内"), nil
	}
	tunnel := &file.Tunnel{
		Mode:   mode,
		Port:   port,
		Status: true,
		Client: client,
		Remark: mcpArgString(args, "remark"),
		Target: &file.Target{TargetStr: target},
		Flow:   &file.Flow{FlowLimit: 0},
	}
	if tunnel.Port == 0 {
		tunnel.Port = tool.GenerateServerPort(tunnel.Mode)
	}
	create := file.GetDb().NewTask
	if principal.IsAdmin {
		create = file.GetDb().NewTaskAsAdmin
	}
	if err := create(tunnel); err != nil {
		return mcpError("创建隧道失败: " + err.Error()), nil
	}
	if err := npsserver.AddTask(tunnel); err != nil {
		if delErr := file.GetDb().DelTask(tunnel.Id); delErr != nil {
			return mcpError("启动隧道失败: " + err.Error() + "; 回滚记录失败: " + delErr.Error()), nil
		}
		return mcpError("启动隧道失败: " + err.Error()), nil
	}
	return mcpJSON(map[string]any{"id": tunnel.Id})
}

func mcpTunnelDelete(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	principal, failure := mcpRequestPrincipal(ctx)
	if failure != nil {
		return failure, nil
	}
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
	if err := npsserver.DelTask(id); err != nil {
		return mcpError("删除失败: " + err.Error()), nil
	}
	return mcpJSON(map[string]any{"deleted": id})
}

func mcpTunnelSetStatus(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	principal, failure := mcpRequestPrincipal(ctx)
	if failure != nil {
		return failure, nil
	}
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
	status := mcpArgBool(args, "status")
	tunnel.RLock()
	current := tunnel.Status
	tunnel.RUnlock()
	if status == current {
		return mcpJSON(map[string]any{"id": id, "status": status})
	}
	var lifecycleErr error
	if status {
		lifecycleErr = npsserver.StartTask(id)
	} else {
		lifecycleErr = npsserver.StopServer(id)
		if _, running := npsserver.RunList.Load(id); !running {
			// A persisted task can be stopped already; reflect the requested
			// state without failing the idempotent status call.
			tunnel.Lock()
			tunnel.Status = false
			tunnel.Unlock()
			lifecycleErr = file.GetDb().UpdateTask(tunnel)
		}
	}
	if lifecycleErr != nil {
		return mcpError("更新状态失败: " + lifecycleErr.Error()), nil
	}
	return mcpJSON(map[string]any{"id": id, "status": tunnel.Status})
}

func registerMCPHostTools(s *server.MCPServer) {
	s.AddTool(newMCPTool("nps_platform_domains", "列出平台域名", true, false, true,
		mcp.WithDescription("列出可用于创建域名反代的平台泛域名：ID、泛域名以及是否已配置证书。证书路径不会返回。"),
	), mcpPlatformDomains)

	s.AddTool(newMCPTool("nps_host_create", "创建域名反代", false, false, false,
		mcp.WithDescription("创建域名反代规则。固定使用服务端托管的平台域名：域名由平台泛域名和单层前缀组成，不支持自定义域名。"),
		mcp.WithNumber("client_id", mcp.Required(), mcp.Description("客户端 ID")),
		mcp.WithString("prefix", mcp.Description("平台子域名前缀（单层，例如 demo）；留空则随机生成")),
		mcp.WithString("platform_domain_id", mcp.Description("平台泛域名 ID 或 *.example.com；留空自动选择可用泛域名")),
		mcp.WithString("scheme", mcp.Description("http、https 或 all，默认 all")),
		mcp.WithString("location", mcp.Description("路径前缀，默认 /")),
		mcp.WithString("target", mcp.Description("目标地址，如 127.0.0.1:8080")),
		mcp.WithString("remark", mcp.Description("备注")),
	), mcpHostCreate)

	s.AddTool(newMCPTool("nps_host_delete", "删除域名反代", false, true, true,
		mcp.WithDescription("删除域名反代规则。"),
		mcp.WithNumber("id", mcp.Required(), mcp.Description("规则 ID")),
	), mcpHostDelete)

	s.AddTool(newMCPTool("nps_host_set_status", "启停域名反代", false, false, true,
		mcp.WithDescription("启用或停用域名反代规则。"),
		mcp.WithNumber("id", mcp.Required(), mcp.Description("规则 ID")),
		mcp.WithBoolean("status", mcp.Required(), mcp.Description("true 启用，false 停用")),
	), mcpHostSetStatus)
}

func mcpPlatformDomains(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if _, failure := mcpRequestPrincipal(ctx); failure != nil {
		return failure, nil
	}
	domains := file.GetDb().GetUsablePlatformDomains()
	rows := make([]map[string]any, 0, len(domains))
	for _, domain := range domains {
		rows = append(rows, map[string]any{
			"id":                     domain.ID,
			"wildcard":               domain.Wildcard,
			"certificate_configured": domain.CertFilePath != "" && domain.KeyFilePath != "",
		})
	}
	return mcpJSON(map[string]any{"domains": rows})
}

func mcpHostCreate(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	principal, failure := mcpRequestPrincipal(ctx)
	if failure != nil {
		return failure, nil
	}
	args := request.GetArguments()
	if strings.TrimSpace(mcpArgString(args, "host")) != "" || strings.EqualFold(strings.TrimSpace(mcpArgString(args, "domain_mode")), "custom") {
		return mcpError("不支持自定义域名：请用 prefix 指定平台子域名前缀；自定义域名请在 Web 面板创建"), nil
	}
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
	target := mcpArgString(args, "target")
	if err := validateBridgeTargetPermission(principal.IsAdmin, target); err != nil {
		return mcpError(err.Error()), nil
	}
	hostName, platformDomainID, err := file.GetDb().AllocatePlatformHost(
		mcpArgString(args, "platform_domain_id"),
		mcpArgString(args, "prefix"),
		location,
		scheme,
		0,
	)
	if err != nil {
		return mcpError("创建域名反代失败: " + err.Error()), nil
	}
	host := &file.Host{
		Host:             hostName,
		PlatformDomainID: platformDomainID,
		Scheme:           scheme,
		Location:         location,
		Client:           client,
		Target:           &file.Target{TargetStr: target},
		Remark:           mcpArgString(args, "remark"),
	}
	create := file.GetDb().NewHost
	if principal.IsAdmin {
		create = file.GetDb().NewHostAsAdmin
	}
	if err := create(host); err != nil {
		return mcpError("创建域名反代失败: " + err.Error()), nil
	}
	return mcpJSON(map[string]any{"id": host.Id, "host": hostName, "platform_domain_id": platformDomainID})
}

func mcpHostDelete(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	principal, failure := mcpRequestPrincipal(ctx)
	if failure != nil {
		return failure, nil
	}
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
	principal, failure := mcpRequestPrincipal(ctx)
	if failure != nil {
		return failure, nil
	}
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
