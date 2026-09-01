package file

import (
	"sort"
	"strings"
	"sync"
)

func sortClientByKey(m *sync.Map, sortField, order string) []int {
	clients := make([]*Client, 0)
	m.Range(func(key, value interface{}) bool {
		if c, ok := value.(*Client); ok && c != nil {
			clients = append(clients, c)
		}
		return true
	})
	SortClients(clients, sortField, order)
	keys := make([]int, 0, len(clients))
	for _, c := range clients {
		keys = append(keys, snapshotClient(c).id)
	}
	return keys
}

type clientSortSnapshot struct {
	id                                          int
	inlet, export, nowRate                      int64
	remark, version, verifyKey, addr, localAddr string
	status, isConnect                           bool
}

func snapshotClient(client *Client) clientSortSnapshot {
	if client == nil {
		return clientSortSnapshot{}
	}
	client.RLock()
	flow, limiter := client.Flow, client.Rate
	snapshot := clientSortSnapshot{
		id:        client.Id,
		remark:    client.Remark,
		version:   client.Version,
		verifyKey: client.VerifyKey,
		addr:      client.Addr,
		localAddr: client.LocalAddr,
		status:    client.Status,
		isConnect: client.IsConnect,
	}
	client.RUnlock()
	if flow != nil {
		snapshot.inlet, snapshot.export, _ = flow.Snapshot()
	}
	if limiter != nil {
		snapshot.nowRate = limiter.CurrentRate()
	}
	return snapshot
}

type tunnelSortSnapshot struct {
	id, clientID, port                        int
	remark, verifyKey, mode, target, password string
	status, runStatus, clientConnect          bool
}

func snapshotTunnel(tunnel *Tunnel) tunnelSortSnapshot {
	if tunnel == nil {
		return tunnelSortSnapshot{}
	}
	tunnel.RLock()
	client, target := tunnel.Client, tunnel.Target
	snapshot := tunnelSortSnapshot{
		id:        tunnel.Id,
		port:      tunnel.Port,
		remark:    tunnel.Remark,
		mode:      tunnel.Mode,
		password:  tunnel.Password,
		status:    tunnel.Status,
		runStatus: tunnel.RunStatus,
	}
	tunnel.RUnlock()
	if client != nil {
		clientSnapshot := snapshotClient(client)
		snapshot.clientID = clientSnapshot.id
		snapshot.verifyKey = clientSnapshot.verifyKey
		snapshot.clientConnect = clientSnapshot.isConnect
	}
	if target != nil {
		target.RLock()
		snapshot.target = target.TargetStr
		target.RUnlock()
	}
	return snapshot
}

type hostSortSnapshot struct {
	id, clientID             int
	remark, verifyKey, host  string
	scheme, target, location string
	isClose, clientConnect   bool
}

func snapshotHost(host *Host) hostSortSnapshot {
	if host == nil {
		return hostSortSnapshot{}
	}
	host.RLock()
	client, target := host.Client, host.Target
	snapshot := hostSortSnapshot{
		id:       host.Id,
		remark:   host.Remark,
		host:     host.Host,
		scheme:   host.Scheme,
		location: host.Location,
		isClose:  host.IsClose,
	}
	host.RUnlock()
	if client != nil {
		clientSnapshot := snapshotClient(client)
		snapshot.clientID = clientSnapshot.id
		snapshot.verifyKey = clientSnapshot.verifyKey
		snapshot.clientConnect = clientSnapshot.isConnect
	}
	if target != nil {
		target.RLock()
		snapshot.target = target.TargetStr
		target.RUnlock()
	}
	return snapshot
}

func lessBool(a, b bool, asc bool) bool {
	if a == b {
		return false
	}
	if asc {
		return !a && b // false first
	}
	return a && !b // true first
}

func lessInt(a, b int, asc bool) bool {
	if asc {
		return a < b
	}
	return a > b
}

func lessInt64(a, b int64, asc bool) bool {
	if asc {
		return a < b
	}
	return a > b
}

func lessString(a, b string, asc bool) bool {
	if asc {
		return strings.ToLower(a) < strings.ToLower(b)
	}
	return strings.ToLower(a) > strings.ToLower(b)
}

// SortClients sorts clients in-place by the given field (bootstrap-table sort name).
func SortClients(list []*Client, sortField, order string) {
	snapshots := make(map[*Client]clientSortSnapshot, len(list))
	for _, client := range list {
		snapshots[client] = snapshotClient(client)
	}
	if sortField == "" || len(list) < 2 {
		if sortField == "" && len(list) > 1 {
			sort.SliceStable(list, func(i, j int) bool { return snapshots[list[i]].id < snapshots[list[j]].id })
		}
		return
	}
	asc := order != "desc"
	sort.SliceStable(list, func(i, j int) bool {
		a, b := snapshots[list[i]], snapshots[list[j]]
		switch sortField {
		case "Id":
			return lessInt(a.id, b.id, asc)
		case "Remark":
			return lessString(a.remark, b.remark, asc)
		case "Version":
			return lessString(a.version, b.version, asc)
		case "VerifyKey":
			return lessString(a.verifyKey, b.verifyKey, asc)
		case "Addr":
			return lessString(a.addr, b.addr, asc)
		case "LocalAddr":
			return lessString(a.localAddr, b.localAddr, asc)
		case "InletFlow":
			return lessInt64(a.inlet, b.inlet, asc)
		case "ExportFlow":
			return lessInt64(a.export, b.export, asc)
		case "NowRate":
			return lessInt64(a.nowRate, b.nowRate, asc)
		case "Status":
			return lessBool(a.status, b.status, asc)
		case "IsConnect":
			return lessBool(a.isConnect, b.isConnect, asc)
		default:
			return lessInt(a.id, b.id, true)
		}
	})
}

// SortTunnels sorts tunnels in-place by the given field.
func SortTunnels(list []*Tunnel, sortField, order string) {
	snapshots := make(map[*Tunnel]tunnelSortSnapshot, len(list))
	for _, tunnel := range list {
		snapshots[tunnel] = snapshotTunnel(tunnel)
	}
	if sortField == "" || len(list) < 2 {
		if sortField == "" && len(list) > 1 {
			sort.SliceStable(list, func(i, j int) bool { return snapshots[list[i]].id < snapshots[list[j]].id })
		}
		return
	}
	asc := order != "desc"
	sort.SliceStable(list, func(i, j int) bool {
		a, b := snapshots[list[i]], snapshots[list[j]]
		switch sortField {
		case "Id":
			return lessInt(a.id, b.id, asc)
		case "ClientId":
			return lessInt(a.clientID, b.clientID, asc)
		case "Remark":
			return lessString(a.remark, b.remark, asc)
		case "Client.VerifyKey", "VerifyKey":
			return lessString(a.verifyKey, b.verifyKey, asc)
		case "Mode":
			return lessString(a.mode, b.mode, asc)
		case "Port":
			return lessInt(a.port, b.port, asc)
		case "Target":
			return lessString(a.target, b.target, asc)
		case "Password":
			return lessString(a.password, b.password, asc)
		case "Status":
			return lessBool(a.status, b.status, asc)
		case "RunStatus":
			return lessBool(a.runStatus, b.runStatus, asc)
		case "IsConnect", "Client.IsConnect":
			return lessBool(a.clientConnect, b.clientConnect, asc)
		default:
			return lessInt(a.id, b.id, true)
		}
	})
}

// SortHosts sorts hosts in-place by the given field.
func SortHosts(list []*Host, sortField, order string) {
	snapshots := make(map[*Host]hostSortSnapshot, len(list))
	for _, host := range list {
		snapshots[host] = snapshotHost(host)
	}
	if sortField == "" || len(list) < 2 {
		if sortField == "" && len(list) > 1 {
			sort.SliceStable(list, func(i, j int) bool { return snapshots[list[i]].id < snapshots[list[j]].id })
		}
		return
	}
	asc := order != "desc"
	sort.SliceStable(list, func(i, j int) bool {
		a, b := snapshots[list[i]], snapshots[list[j]]
		switch sortField {
		case "Id":
			return lessInt(a.id, b.id, asc)
		case "ClientId":
			return lessInt(a.clientID, b.clientID, asc)
		case "Remark":
			return lessString(a.remark, b.remark, asc)
		case "Client.VerifyKey", "VerifyKey":
			return lessString(a.verifyKey, b.verifyKey, asc)
		case "Host":
			return lessString(a.host, b.host, asc)
		case "Scheme":
			return lessString(a.scheme, b.scheme, asc)
		case "Target":
			return lessString(a.target, b.target, asc)
		case "Location":
			return lessString(a.location, b.location, asc)
		case "IsClose", "Status":
			// IsClose: false=open, true=closed — sort by open status for "Status" display
			return lessBool(a.isClose, b.isClose, asc)
		case "IsConnect", "Client.IsConnect":
			return lessBool(a.clientConnect, b.clientConnect, asc)
		default:
			return lessInt(a.id, b.id, true)
		}
	})
}
