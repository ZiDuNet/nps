package server

import (
	"sort"
	"strconv"
	"sync/atomic"
	"time"

	"ehang.io/nps/lib/file"
)

// TopologySnapshot is the data contract for the standalone resource overview.
// It deliberately contains operational metadata only: credentials, certificate
// paths and per-resource authentication settings never leave the server.
type TopologySnapshot struct {
	Clients   []TopologyClient   `json:"clients"`
	Resources []TopologyResource `json:"resources"`
	UpdatedAt string             `json:"updatedAt"`
}

type TopologyClient struct {
	ID              int    `json:"id"`
	Remark          string `json:"remark"`
	Address         string `json:"address"`
	LocalAddress    string `json:"localAddress"`
	Version         string `json:"version"`
	Enabled         bool   `json:"enabled"`
	Online          bool   `json:"online"`
	Connections     int32  `json:"connections"`
	ConnectionLimit int    `json:"connectionLimit"`
	TunnelLimit     int    `json:"tunnelLimit"`
	RateLimit       int    `json:"rateLimit"`
	FlowIn          int64  `json:"flowIn"`
	FlowOut         int64  `json:"flowOut"`
	FlowTotal       int64  `json:"flowTotal"`
	FlowLimitBytes  int64  `json:"flowLimitBytes"`
	RateIn          int64  `json:"rateIn"`
	RateOut         int64  `json:"rateOut"`
	CreatedAt       string `json:"createdAt"`
	LastOnlineAt    string `json:"lastOnlineAt"`
	ExpiresAt       string `json:"expiresAt"`
}

type TopologyResource struct {
	ID                 int    `json:"id"`
	Kind               string `json:"kind"`
	Mode               string `json:"mode"`
	ClientID           int    `json:"clientId"`
	Remark             string `json:"remark"`
	Enabled            bool   `json:"enabled"`
	Running            bool   `json:"running"`
	State              string `json:"state"`
	RunError           string `json:"runError,omitempty"`
	Entry              string `json:"entry"`
	Target             string `json:"target"`
	Location           string `json:"location,omitempty"`
	AutoHTTPS          bool   `json:"autoHttps,omitempty"`
	FlowIn             int64  `json:"flowIn"`
	FlowOut            int64  `json:"flowOut"`
	FlowTotal          int64  `json:"flowTotal"`
	FlowLimitBytes     int64  `json:"flowLimitBytes"`
	RateIn             int64  `json:"rateIn"`
	RateOut            int64  `json:"rateOut"`
	CurrentConnections int32  `json:"currentConnections,omitempty"`
	HealthCheckURL     string `json:"healthCheckUrl,omitempty"`
}

// GetTopologyData returns an operational snapshot restricted to the supplied
// client IDs. A nil allowlist is the administrator view; every non-nil list is
// enforced before client, tunnel or host metadata is placed in the response.
func GetTopologyData(allowedClientIDs map[int]struct{}, isAdmin bool) TopologySnapshot {
	// Only administrators may request an unbounded view. Keeping this guard at
	// the data boundary prevents a future caller from accidentally turning an
	// empty user scope into a global response.
	if !isAdmin && allowedClientIDs == nil {
		allowedClientIDs = map[int]struct{}{}
	}
	clients := make(map[int]*file.Client)
	file.GetDb().JsonDb.Clients.Range(func(_, value interface{}) bool {
		client, ok := value.(*file.Client)
		if !ok || !dashboardClientVisible(client, allowedClientIDs) {
			return true
		}
		client.RLock()
		clientID := client.Id
		client.RUnlock()
		clients[clientID] = client
		return true
	})

	if isAdmin {
		dealClientData()
	} else {
		refreshDashboardClients(clients)
	}

	snapshot := TopologySnapshot{
		Clients:   make([]TopologyClient, 0, len(clients)),
		Resources: make([]TopologyResource, 0),
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	for _, client := range clients {
		snapshot.Clients = append(snapshot.Clients, topologyClient(client))
	}
	sort.Slice(snapshot.Clients, func(i, j int) bool { return snapshot.Clients[i].ID < snapshot.Clients[j].ID })

	file.GetDb().JsonDb.Tasks.Range(func(_, value interface{}) bool {
		task, ok := value.(*file.Tunnel)
		if !ok || task == nil {
			return true
		}
		resource, clientID, include := topologyTunnel(task, clients)
		if include {
			resource.ClientID = clientID
			snapshot.Resources = append(snapshot.Resources, resource)
		}
		return true
	})
	file.GetDb().JsonDb.Hosts.Range(func(_, value interface{}) bool {
		host, ok := value.(*file.Host)
		if !ok || host == nil {
			return true
		}
		resource, clientID, include := topologyHost(host, clients)
		if include {
			resource.ClientID = clientID
			snapshot.Resources = append(snapshot.Resources, resource)
		}
		return true
	})
	sort.Slice(snapshot.Resources, func(i, j int) bool {
		if snapshot.Resources[i].ClientID != snapshot.Resources[j].ClientID {
			return snapshot.Resources[i].ClientID < snapshot.Resources[j].ClientID
		}
		if snapshot.Resources[i].Kind != snapshot.Resources[j].Kind {
			return snapshot.Resources[i].Kind < snapshot.Resources[j].Kind
		}
		return snapshot.Resources[i].ID < snapshot.Resources[j].ID
	})
	return snapshot
}

func topologyClient(client *file.Client) TopologyClient {
	if client == nil {
		return TopologyClient{}
	}
	client.RLock()
	id := client.Id
	remark, address, localAddress, version := client.Remark, client.Addr, client.LocalAddr, client.Version
	enabled, online := client.Status, client.IsConnect
	connectionLimit, tunnelLimit, rateLimit := client.MaxConn, client.MaxTunnelNum, client.RateLimit
	flow := client.Flow
	createdAt, lastOnlineAt, expiresAt := client.CreateTime, client.LastOnlineTime, client.ExpireTime
	client.RUnlock()
	in, out, limit := flow.Snapshot()
	rateIn, rateOut := flow.RateSnapshot()
	return TopologyClient{
		ID: id, Remark: remark, Address: address, LocalAddress: localAddress, Version: version,
		Enabled: enabled, Online: online, Connections: atomic.LoadInt32(&client.NowConn),
		ConnectionLimit: connectionLimit, TunnelLimit: tunnelLimit, RateLimit: rateLimit,
		FlowIn: in, FlowOut: out, FlowTotal: flow.Total(), FlowLimitBytes: limit << 20,
		RateIn: rateIn, RateOut: rateOut, CreatedAt: createdAt, LastOnlineAt: lastOnlineAt, ExpiresAt: expiresAt,
	}
}

func topologyTunnel(task *file.Tunnel, clients map[int]*file.Client) (TopologyResource, int, bool) {
	task.RLock()
	id, mode, remark, serverIP, targetAddr := task.Id, task.Mode, task.Remark, task.ServerIp, task.TargetAddr
	enabled, runError := task.Status, task.RunError
	port, client, target, flow := task.Port, task.Client, task.Target, task.Flow
	healthURL := task.HttpHealthUrl
	task.RUnlock()
	clientID, online := topologyClientState(client, clients)
	if clientID == 0 {
		return TopologyResource{}, 0, false
	}
	entry := topologyAddress(serverIP, port)
	if target != nil {
		target.RLock()
		targetAddr = target.TargetStr
		target.RUnlock()
	}
	_, listenerOpen := RunList.Load(id)
	// RunList is the authoritative lifecycle registry. RunStatus is persisted
	// for the management table and can briefly lag a start or stop operation.
	running := enabled && listenerOpen
	state := topologyState(enabled, running, online, runError)
	in, out, limit := flow.Snapshot()
	rateIn, rateOut := flow.RateSnapshot()
	return TopologyResource{
		ID: id, Kind: "tunnel", Mode: mode, Remark: remark, Enabled: enabled, Running: running,
		State: state, RunError: runError, Entry: entry, Target: targetAddr,
		FlowIn: in, FlowOut: out, FlowTotal: flow.Total(), FlowLimitBytes: limit << 20,
		RateIn: rateIn, RateOut: rateOut, CurrentConnections: atomic.LoadInt32(&task.CurrentConnections),
		HealthCheckURL: healthURL,
	}, clientID, true
}

func topologyHost(host *file.Host, clients map[int]*file.Client) (TopologyResource, int, bool) {
	host.RLock()
	id, name, remark, scheme, location := host.Id, host.Host, host.Remark, host.Scheme, host.Location
	closed, autoHTTPS, client, target, flow := host.IsClose, host.AutoHttps, host.Client, host.Target, host.Flow
	healthURL := host.HttpHealthUrl
	host.RUnlock()
	clientID, online := topologyClientState(client, clients)
	if clientID == 0 {
		return TopologyResource{}, 0, false
	}
	targetAddr := ""
	if target != nil {
		target.RLock()
		targetAddr = target.TargetStr
		target.RUnlock()
	}
	in, out, limit := flow.Snapshot()
	rateIn, rateOut := flow.RateSnapshot()
	enabled := !closed
	running := enabled && online
	return TopologyResource{
		ID: id, Kind: "host", Mode: "host", Remark: remark, Enabled: enabled, Running: running,
		State: topologyState(enabled, running, online, ""), Entry: topologyHostEntry(name, scheme),
		Target: targetAddr, Location: location, AutoHTTPS: autoHTTPS,
		FlowIn: in, FlowOut: out, FlowTotal: flow.Total(), FlowLimitBytes: limit << 20,
		RateIn: rateIn, RateOut: rateOut, HealthCheckURL: healthURL,
	}, clientID, true
}

func topologyClientState(client *file.Client, clients map[int]*file.Client) (int, bool) {
	if client == nil {
		return 0, false
	}
	client.RLock()
	clientID, online := client.Id, client.IsConnect
	client.RUnlock()
	if _, visible := clients[clientID]; !visible {
		return 0, false
	}
	return clientID, online
}

func topologyState(enabled, running, clientOnline bool, runError string) string {
	if !enabled {
		return "stopped"
	}
	if runError != "" && !running {
		return "failed"
	}
	if !clientOnline {
		return "waiting"
	}
	if running {
		return "running"
	}
	return "stopped"
}

func topologyAddress(ip string, port int) string {
	if port <= 0 {
		return ""
	}
	if ip == "" || ip == "0.0.0.0" {
		return ":" + strconv.Itoa(port)
	}
	return ip + ":" + strconv.Itoa(port)
}

func topologyHostEntry(host, scheme string) string {
	if host == "" {
		return ""
	}
	if scheme == "http" || scheme == "https" {
		return scheme + "://" + host
	}
	return host
}
