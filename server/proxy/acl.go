package proxy

import (
	"net"
	"strconv"

	"ehang.io/nps/lib/acl"
	"ehang.io/nps/lib/file"
	"github.com/astaxie/beego/logs"
)

// sourcePolicies returns the global, client and resource-level source
// policies. Every configured level must allow a visitor address.
func sourcePolicies(client *file.Client, entry *file.SourceLists) (global, cli, resource *acl.Source) {
	if db := file.GetDb(); db != nil {
		if globalSettings := db.GetGlobal(); globalSettings != nil {
			global = globalSettings.SourceACL()
		}
	}
	if client != nil {
		cli = client.SourceACL()
	}
	if entry != nil {
		resource = entry.SourceACL()
	}
	return
}

func SourceDenied(addr net.Addr, client *file.Client, entry *file.SourceLists, entryName string) bool {
	global, clientPolicy, resource := sourcePolicies(client, entry)
	if !acl.Allows(addr, global, clientPolicy, resource) {
		logs.Warn("[访问控制] source %v denied at %s: %s", addr, entryName, acl.Decide(addr, levelNames(client, entryName), global, clientPolicy, resource).String())
		return true
	}
	return false
}

func SourceDeniedString(addr string, client *file.Client, entry *file.SourceLists, entryName string) bool {
	global, clientPolicy, resource := sourcePolicies(client, entry)
	if !acl.AllowsString(addr, global, clientPolicy, resource) {
		logs.Warn("[访问控制] source %s denied at %s: %s", addr, entryName, acl.DecideString(addr, levelNames(client, entryName), global, clientPolicy, resource).String())
		return true
	}
	return false
}

func datagramDenied(addr net.Addr, client *file.Client, entry *file.SourceLists) bool {
	if SourceDenied(addr, client, entry, "datagram") {
		logs.Trace("[访问控制] datagram from %v dropped", addr)
		return true
	}
	return false
}

// mixProxyDestinationAllowed applies the opt-in outbound policy only to the
// new mixed-proxy mode. The legacy HTTP proxy and SOCKS5 modes keep their
// existing unrestricted destination behavior.
func mixProxyDestinationAllowed(task *file.Tunnel, destination string) bool {
	if task == nil {
		return true
	}
	task.RLock()
	mode, aclMode, id := task.Mode, task.DestAclMode, task.Id
	task.RUnlock()
	if mode != "mixProxy" || aclMode == file.AclOff {
		return true
	}
	if task.AllowsDestination(destination) {
		return true
	}
	logs.Warn("[目标访问控制] 拒绝隧道 %d 的目标 %s", id, destination)
	return false
}

// mixProxySOCKSDestinationAllowed checks SOCKS5 CONNECT destinations. UDP
// ASSOCIATE's address is only a relay hint (commonly 0.0.0.0:0), so its actual
// datagram destinations are checked individually in the UDP relay loop.
func mixProxySOCKSDestinationAllowed(task *file.Tunnel, command uint8, destination string) bool {
	if command == associateMethod {
		return true
	}
	return mixProxyDestinationAllowed(task, destination)
}

func clientID(client *file.Client) int {
	if client == nil {
		return 0
	}
	client.RLock()
	id := client.Id
	client.RUnlock()
	return id
}

func levelNames(client *file.Client, entryName string) []string {
	name := "client"
	if client != nil {
		name = "client " + strconv.Itoa(clientID(client))
	}
	return []string{"global", name, entryName}
}

func taskName(t *file.Tunnel) string {
	if t == nil || t.Id == 0 {
		return "host"
	}
	return "tunnel " + strconv.Itoa(t.Id)
}

func taskLists(t *file.Tunnel) *file.SourceLists {
	if t == nil {
		return nil
	}
	return &t.SourceLists
}
