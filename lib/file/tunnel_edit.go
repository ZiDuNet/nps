package file

import "ehang.io/nps/lib/acl"

// tunnelEditFields is the persisted, operator-controlled portion of a tunnel.
// Runtime state (traffic counters, rate buckets, connection counts and health
// probe state) deliberately stays on the stored object.
type tunnelEditFields struct {
	port           int
	serverIP       string
	mode           string
	status         bool
	runError       string
	target         *Target
	password       string
	proxyAuth      string
	remark         string
	targetType     string
	localPath      string
	stripPre       string
	protoVersion   string
	maxConn        int
	httpProxy      bool
	socks5Proxy    bool
	destACLMode    int
	destACLRules   string
	registeredOnly bool
	client         *Client
	uploadLimit    int64
	downloadLimit  int64
	flowLimit      int64
	aclPriority    int
	aclBlack       []string
	aclWhite       []string
	health         Health
}

// snapshotTunnelReplacement clones an edit payload while keeping references
// to the stored runtime client object. The copy is never installed in Tasks;
// it exists only so validation and ACL compilation can run on the submitted
// values without touching the live tunnel before persistence succeeds.
func snapshotTunnelReplacement(edit, stored *Tunnel) *Tunnel {
	if edit == nil {
		return nil
	}
	priority, black, white := sourceListSnapshot(&edit.SourceLists)
	target := &Target{}
	if edit.Target != nil {
		edit.Target.RLock()
		target.TargetStr = edit.Target.TargetStr
		target.LocalProxy = edit.Target.LocalProxy
		target.ProxyProtocol = edit.Target.ProxyProtocol
		target.TargetArr = append([]string(nil), edit.Target.TargetArr...)
		edit.Target.RUnlock()
	}
	replacement := &Tunnel{
		Id:                 stored.Id,
		Port:               edit.Port,
		ServerIp:           edit.ServerIp,
		Mode:               edit.Mode,
		Status:             edit.Status,
		RunError:           edit.RunError,
		Client:             stored.Client,
		Target:             target,
		Password:           edit.Password,
		ProxyAuth:          edit.ProxyAuth,
		Remark:             edit.Remark,
		TargetType:         edit.TargetType,
		LocalPath:          edit.LocalPath,
		StripPre:           edit.StripPre,
		ProtoVersion:       edit.ProtoVersion,
		MaxConn:            edit.MaxConn,
		HttpProxy:          edit.HttpProxy,
		Socks5Proxy:        edit.Socks5Proxy,
		DestAclMode:        edit.DestAclMode,
		DestAclRules:       edit.DestAclRules,
		RegisteredOnly:     edit.RegisteredOnly,
		Flow:               snapshotFlowConfig(edit.Flow),
		RateLimits:         RateLimits{UploadLimit: edit.UploadLimit, DownloadLimit: edit.DownloadLimit},
		CurrentConnections: edit.CurrentConnections,
	}
	replacement.Client = edit.Client
	replacement.SetSourceLists(priority, black, white, acl.ScopeUser, "tunnel")
	setHealthConfig(&replacement.Health, HealthConfigSnapshot(&edit.Health))
	return replacement
}

// snapshotFlowConfig copies only the configured limit. Traffic counters stay
// on the stored flow object and are never replaced by an edit payload.
func snapshotFlowConfig(flow *Flow) *Flow {
	replacement := NewFlow()
	if flow != nil {
		flow.RLock()
		replacement.FlowLimit = flow.FlowLimit
		flow.RUnlock()
	}
	return replacement
}

func snapshotTunnelEdit(t *Tunnel) tunnelEditFields {
	if t == nil {
		return tunnelEditFields{}
	}
	priority, black, white := sourceListSnapshot(&t.SourceLists)
	t.RLock()
	fields := tunnelEditFields{
		port:           t.Port,
		serverIP:       t.ServerIp,
		mode:           t.Mode,
		status:         t.Status,
		runError:       t.RunError,
		target:         t.Target,
		password:       t.Password,
		proxyAuth:      t.ProxyAuth,
		remark:         t.Remark,
		targetType:     t.TargetType,
		localPath:      t.LocalPath,
		stripPre:       t.StripPre,
		protoVersion:   t.ProtoVersion,
		maxConn:        t.MaxConn,
		httpProxy:      t.HttpProxy,
		socks5Proxy:    t.Socks5Proxy,
		destACLMode:    t.DestAclMode,
		destACLRules:   t.DestAclRules,
		registeredOnly: t.RegisteredOnly,
		client:         t.Client,
		uploadLimit:    t.UploadLimit,
		downloadLimit:  t.DownloadLimit,
		aclPriority:    priority,
		aclBlack:       black,
		aclWhite:       white,
	}
	if t.Flow != nil {
		t.Flow.RLock()
		fields.flowLimit = t.Flow.FlowLimit
		t.Flow.RUnlock()
	}
	t.RUnlock()
	fields.health = HealthConfigSnapshot(&t.Health)
	return fields
}

func restoreTunnelEdit(t *Tunnel, fields tunnelEditFields) {
	if t == nil {
		return
	}
	t.Lock()
	t.Port = fields.port
	t.ServerIp = fields.serverIP
	t.Mode = fields.mode
	t.Status = fields.status
	t.RunError = fields.runError
	t.Target = fields.target
	t.Password = fields.password
	t.ProxyAuth = fields.proxyAuth
	t.Remark = fields.remark
	t.TargetType = fields.targetType
	t.LocalPath = fields.localPath
	t.StripPre = fields.stripPre
	t.ProtoVersion = fields.protoVersion
	t.MaxConn = fields.maxConn
	t.HttpProxy = fields.httpProxy
	t.Socks5Proxy = fields.socks5Proxy
	t.DestAclMode = fields.destACLMode
	t.DestAclRules = fields.destACLRules
	t.RegisteredOnly = fields.registeredOnly
	t.Client = fields.client
	if t.Flow != nil {
		t.Flow.SetLimit(fields.flowLimit)
	}
	t.Unlock()
	t.SetRateLimits(fields.uploadLimit, fields.downloadLimit)
	t.SetSourceLists(fields.aclPriority, fields.aclBlack, fields.aclWhite, acl.ScopeUser, "tunnel")
	setHealthConfig(&t.Health, fields.health)
	// Apply the persisted ACL alongside its rule text so a failed store cannot
	// leave the old text paired with the new compiled policy.
	t.CompileDestinationACL()
}

// applyTunnelEdit copies operator-editable fields into the live stored object
// and returns the previous values. Keeping the object identity is important:
// active proxy workers and lifecycle goroutines hold the pointer, while the
// returned snapshot lets callers restore memory if persistence fails.
func applyTunnelEdit(stored, edit *Tunnel) tunnelEditFields {
	previous := snapshotTunnelEdit(stored)
	if stored == nil || edit == nil {
		return previous
	}
	next := snapshotTunnelEdit(edit)
	stored.Lock()
	stored.Port = next.port
	stored.ServerIp = next.serverIP
	stored.Mode = next.mode
	stored.Status = next.status
	stored.RunError = next.runError
	stored.Target = next.target
	stored.Password = next.password
	stored.ProxyAuth = next.proxyAuth
	stored.Remark = next.remark
	stored.TargetType = next.targetType
	stored.LocalPath = next.localPath
	stored.StripPre = next.stripPre
	stored.ProtoVersion = next.protoVersion
	stored.MaxConn = next.maxConn
	stored.HttpProxy = next.httpProxy
	stored.Socks5Proxy = next.socks5Proxy
	stored.DestAclMode = next.destACLMode
	stored.DestAclRules = next.destACLRules
	stored.RegisteredOnly = next.registeredOnly
	stored.Client = next.client
	if stored.Flow != nil {
		stored.Flow.SetLimit(next.flowLimit)
	}
	stored.Unlock()
	stored.SetRateLimits(next.uploadLimit, next.downloadLimit)
	stored.SetSourceLists(next.aclPriority, next.aclBlack, next.aclWhite, acl.ScopeUser, "tunnel")
	setHealthConfig(&stored.Health, next.health)
	stored.CompileDestinationACL()
	return previous
}
