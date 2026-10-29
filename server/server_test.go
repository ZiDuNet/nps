package server

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"ehang.io/nps/lib/bridgeendpoint"
	"ehang.io/nps/lib/file"
	"github.com/astaxie/beego"
)

type lifecycleTestService struct {
	starts atomic.Int32
	closes atomic.Int32
	err    error
}

func (s *lifecycleTestService) Start() error {
	s.starts.Add(1)
	return s.err
}

func (s *lifecycleTestService) Close() error {
	s.closes.Add(1)
	return nil
}

type http3LifecycleTestService struct {
	lifecycleTestService
	listening bool
}

func (s *http3LifecycleTestService) HTTP3Listening() bool {
	return s != nil && s.listening
}

func TestBridgeEndpointsExposeHTTP3OnlyWhileListenerIsActive(t *testing.T) {
	host := &http3LifecycleTestService{listening: true}
	service := &managementService{host: host}
	previous, previousFound := RunList.Load(managementServiceKey)
	RunList.Store(managementServiceKey, service)
	t.Cleanup(func() {
		if previousFound {
			RunList.Store(managementServiceKey, previous)
		} else {
			RunList.Delete(managementServiceKey)
		}
	})

	keys := []string{
		"bridge_type", "bridge_addr", "bridge_port", "bridge_path",
		"tls_enable", "tls_bridge_port", "bridge_kcp_port", "bridge_ws_port",
		"bridge_wss_port", "bridge_quic_port", "http3_proxy_port",
		"bridge_http3", "bridge_host", "quic_alpn",
	}
	previousConfig := make(map[string]string, len(keys))
	for _, key := range keys {
		previousConfig[key] = beego.AppConfig.String(key)
	}
	t.Cleanup(func() {
		for key, value := range previousConfig {
			_ = beego.AppConfig.Set(key, value)
		}
	})
	for key, value := range map[string]string{
		"bridge_type": "tcp", "bridge_addr": "bridge.example.com", "bridge_port": "8024",
		"bridge_path": "/ws", "tls_enable": "false", "tls_bridge_port": "0",
		"bridge_kcp_port": "0", "bridge_ws_port": "0", "bridge_wss_port": "0",
		"bridge_quic_port": "0", "http3_proxy_port": "8443",
		"bridge_http3": "true", "bridge_host": "bridge.example.com", "quic_alpn": "nps",
	} {
		_ = beego.AppConfig.Set(key, value)
	}

	endpoints := GetBridgeEndpoints("panel.example.com", "")
	if !hasBridgeEndpoint(endpoints, "http3") {
		t.Fatalf("active HTTP/3 endpoint is missing: %#v", endpoints)
	}

	host.listening = false
	endpoints = GetBridgeEndpoints("panel.example.com", "")
	if hasBridgeEndpoint(endpoints, "http3") {
		t.Fatalf("inactive HTTP/3 endpoint must not generate commands: %#v", endpoints)
	}
	if !hasBridgeEndpoint(endpoints, "tcp") {
		t.Fatalf("TCP endpoint should remain available without HTTP/3: %#v", endpoints)
	}
}

func hasBridgeEndpoint(endpoints []bridgeendpoint.Endpoint, key string) bool {
	for _, endpoint := range endpoints {
		if endpoint.Key == key {
			return true
		}
	}
	return false
}

func TestManagementServiceOwnsHostAndWebLifecycle(t *testing.T) {
	host := &lifecycleTestService{}
	web := &lifecycleTestService{}
	service := &managementService{host: host, web: web}

	if err := service.Start(); err != nil {
		t.Fatalf("start management service: %v", err)
	}
	if host.starts.Load() != 1 || web.starts.Load() != 1 {
		t.Fatalf("expected host and web services to start once, got host=%d web=%d", host.starts.Load(), web.starts.Load())
	}
	if err := service.Close(); err != nil {
		t.Fatalf("close management service: %v", err)
	}
	if host.closes.Load() != 1 || web.closes.Load() != 1 {
		t.Fatalf("expected host and web services to close once, got host=%d web=%d", host.closes.Load(), web.closes.Load())
	}
}

func TestManagementServiceClosesHostWhenWebStartFails(t *testing.T) {
	host := &lifecycleTestService{}
	web := &lifecycleTestService{err: errors.New("web start failed")}
	service := &managementService{host: host, web: web}

	if err := service.Start(); err == nil {
		t.Fatal("expected web startup error")
	}
	if host.closes.Load() != 1 {
		t.Fatalf("host service was not rolled back after web startup failure: %d", host.closes.Load())
	}
}

func TestManagementServiceStartsWebWhenHostStartFails(t *testing.T) {
	host := &lifecycleTestService{err: errors.New("host port unavailable")}
	web := &lifecycleTestService{}
	service := &managementService{host: host, web: web}

	if err := service.Start(); err != nil {
		t.Fatalf("host startup failure must not hide the management panel: %v", err)
	}
	if web.starts.Load() != 1 {
		t.Fatalf("web service starts = %d, want 1", web.starts.Load())
	}
}

func TestStartNewServerRejectsMissingManagementConfiguration(t *testing.T) {
	if err := StartNewServer(0, nil, "tcp", 0); err == nil {
		t.Fatal("missing management configuration must fail startup")
	}
}

func TestManagementServiceKeyIsOutsideTaskNamespace(t *testing.T) {
	var services sync.Map
	services.Store(managementServiceKey, &lifecycleTestService{})
	if _, found := services.Load(0); found {
		t.Fatal("management service must not be reachable through task ID 0")
	}
}

func TestTaskLifecycleRejectsNonPositiveIDs(t *testing.T) {
	for _, id := range []int{-1, 0} {
		t.Run("id", func(t *testing.T) {
			if err := StopServer(id); err == nil {
				t.Fatalf("StopServer(%d) unexpectedly succeeded", id)
			}
			if err := StartTask(id); err == nil {
				t.Fatalf("StartTask(%d) unexpectedly succeeded", id)
			}
			if err := DelTask(id); err == nil {
				t.Fatalf("DelTask(%d) unexpectedly succeeded", id)
			}
			if err := AddTask(&file.Tunnel{Id: id}); err == nil {
				t.Fatalf("AddTask(%d) unexpectedly succeeded", id)
			}
		})
	}
}

func TestRevokeUserClientsWithOnlyTouchesOwnedClients(t *testing.T) {
	var clients sync.Map
	clients.Store(1, &file.Client{Id: 1, UserId: 7})
	clients.Store(2, &file.Client{Id: 2, UserId: 8})
	var disconnected, removed []int
	revokeUserClientsWith(7, &clients, func(id int) { disconnected = append(disconnected, id) }, func(id int, _ bool) { removed = append(removed, id) })
	if len(disconnected) != 1 || disconnected[0] != 1 || len(removed) != 1 || removed[0] != 1 {
		t.Fatalf("revocation touched unexpected clients: disconnected=%v removed=%v", disconnected, removed)
	}
}

func TestDashboardDataForClientsIsolatedAndClassified(t *testing.T) {
	db := file.NewJsonDb(t.TempDir())
	owned := file.NewClient("owned", false, false)
	owned.Id = 1
	owned.Remark = "owned client"
	owned.Status = true
	owned.MaxConn = 4
	owned.MaxTunnelNum = 4
	owned.Flow = &file.Flow{InletFlow: 120, ExportFlow: 80, FlowLimit: 1}
	other := file.NewClient("other", false, false)
	other.Id = 2
	other.Remark = "other client"
	other.Flow = &file.Flow{InletFlow: 900, ExportFlow: 700}
	db.Clients.Store(owned.Id, owned)
	db.Clients.Store(other.Id, other)
	db.Tasks.Store(10, &file.Tunnel{Id: 10, Mode: "tcp", Status: true, Client: owned, Flow: &file.Flow{}})
	db.Tasks.Store(20, &file.Tunnel{Id: 20, Mode: "udp", Status: true, Client: other, Flow: &file.Flow{}})
	db.Hosts.Store(30, &file.Host{Id: 30, Host: "owned.example", Client: owned, Flow: &file.Flow{}})
	db.Hosts.Store(40, &file.Host{Id: 40, Host: "other.example", Client: other, Flow: &file.Flow{}})

	dbUtils := file.GetDb()
	oldDB := dbUtils.JsonDb
	oldBridge := Bridge
	dbUtils.JsonDb = db
	Bridge = nil
	bridgeConfig := map[string]string{
		"bridge_type": "tcp", "bridge_addr": "bridge.example.com", "bridge_port": "8024",
		"bridge_path": "/ws", "bridge_show_path": "", "bridge_host": "",
		"bridge_tcp_show": "true", "bridge_tcp_show_ip": "", "bridge_tcp_show_port": "",
		"tls_enable": "true", "tls_bridge_port": "8025",
		"bridge_tls_show": "true", "bridge_tls_show_ip": "", "bridge_tls_show_port": "",
		"bridge_kcp_port": "0", "bridge_kcp_show": "true",
		"bridge_ws_port": "0", "bridge_ws_show": "true",
		"bridge_wss_port": "0", "bridge_wss_show": "true",
		"bridge_quic_port": "0", "bridge_quic_show": "true",
		"http3_proxy_port": "0", "bridge_http3": "false",
	}
	oldBridgeConfig := make(map[string]string, len(bridgeConfig))
	for key := range bridgeConfig {
		oldBridgeConfig[key] = beego.AppConfig.String(key)
	}
	for key, value := range bridgeConfig {
		_ = beego.AppConfig.Set(key, value)
	}
	t.Cleanup(func() {
		dbUtils.JsonDb = oldDB
		Bridge = oldBridge
		for key, value := range oldBridgeConfig {
			_ = beego.AppConfig.Set(key, value)
		}
	})

	data := GetDashboardDataForClients(map[int]struct{}{owned.Id: {}})
	if got := data["clientCount"]; got != 1 {
		t.Fatalf("scoped client count = %v, want 1", got)
	}
	if got := data["tunnelCount"]; got != 1 {
		t.Fatalf("scoped tunnel count = %v, want 1", got)
	}
	if got := data["hostCount"]; got != 1 {
		t.Fatalf("scoped host count = %v, want 1", got)
	}
	if got := data["inletFlowCount"]; got != int(owned.Flow.InletFlow) {
		t.Fatalf("scoped inlet flow = %v, want %d", got, owned.Flow.InletFlow)
	}
	if got := data["exportFlowCount"]; got != int(owned.Flow.ExportFlow) {
		t.Fatalf("scoped export flow = %v, want %d", got, owned.Flow.ExportFlow)
	}
	if _, exists := data["cpu"]; exists {
		t.Fatal("ordinary dashboard must not include host CPU data")
	}
	if _, exists := data["httpProxyPort"]; exists {
		t.Fatal("ordinary dashboard must not include listener configuration")
	}
	endpoints, ok := data["bridgeEndpoints"].([]bridgeendpoint.Endpoint)
	if !ok {
		t.Fatalf("ordinary dashboard bridgeEndpoints type = %T, want []bridgeendpoint.Endpoint", data["bridgeEndpoints"])
	}
	if len(endpoints) != 2 || !hasBridgeEndpoint(endpoints, "tcp") || !hasBridgeEndpoint(endpoints, "tls") {
		t.Fatalf("ordinary dashboard Bridge endpoints = %#v, want enabled TCP and TLS", endpoints)
	}
	if hasBridgeEndpoint(endpoints, "ws") {
		t.Fatalf("disabled WS endpoint must not be shown to ordinary users: %#v", endpoints)
	}
	if got := data["systemInfoDisplay"]; got != false {
		t.Fatalf("ordinary systemInfoDisplay = %v, want false", got)
	}

	summary, ok := data["runtimeStatus"].(map[string]interface{})
	if !ok {
		t.Fatalf("runtimeStatus type = %T, want map[string]interface{}", data["runtimeStatus"])
	}
	if got := summary["clientOffline"]; got != 1 {
		t.Fatalf("offline clients = %v, want 1", got)
	}
	if got := summary["tunnelWaiting"]; got != 1 {
		t.Fatalf("waiting tunnels = %v, want 1", got)
	}
	resource, ok := data["resourceStatus"].(map[string]int)
	if !ok {
		t.Fatalf("resourceStatus type = %T, want map[string]int", data["resourceStatus"])
	}
	if resource["running"] != 0 || resource["stopped"] != 0 || resource["waiting"] != 1 {
		t.Fatalf("scoped resource status = %#v, want running=0 stopped=0 waiting=1", resource)
	}
	rows, ok := data["runtimeRows"].([]dashboardRuntimeRow)
	if !ok || len(rows) != 2 {
		t.Fatalf("scoped runtime rows = %#v, want one client and one tunnel", data["runtimeRows"])
	}
	quotas, ok := data["quotas"].([]dashboardQuotaRow)
	if !ok || len(quotas) != 1 || quotas[0].ClientID != owned.Id {
		t.Fatalf("scoped quotas = %#v, want only client %d", data["quotas"], owned.Id)
	}
}

func TestDashboardPendingItemsIncludeQuotaAndHealthWarnings(t *testing.T) {
	db := file.NewJsonDb(t.TempDir())
	client := file.NewClient("pending", false, false)
	client.Id = 1
	client.Status = true
	client.MaxConn = 1
	client.NowConn = 1
	client.MaxTunnelNum = 1
	client.Flow = &file.Flow{FlowLimit: 1, InletFlow: 1 << 20}
	task := &file.Tunnel{Id: 1, Status: true, Client: client}
	task.Health.HealthMaxFail = 2
	task.Health.HealthMap = map[string]int{"backend:8080": 2}
	db.Clients.Store(client.Id, client)
	db.Tasks.Store(task.Id, task)
	dbUtils := file.GetDb()
	oldDB := dbUtils.JsonDb
	dbUtils.JsonDb = db
	t.Cleanup(func() { dbUtils.JsonDb = oldDB })

	pending := dashboardPendingItems(map[int]*file.Client{client.Id: client}, nil)
	for _, want := range []string{"客户端连接数接近上限", "隧道数量接近上限", "流量配额接近上限", "后端健康检查异常"} {
		found := false
		for _, value := range pending {
			if value == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("pending items %v do not include %q", pending, want)
		}
	}
}
