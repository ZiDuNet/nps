package controllers

import (
	"crypto/rand"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/file"
	"ehang.io/nps/server"
	"ehang.io/nps/server/tool"

	"github.com/astaxie/beego"
)

type IndexController struct {
	BaseController
}

// hostListRow is intentionally narrower than file.Host. The management list
// must never serialize filesystem certificate paths, private keys, or client
// credentials to the browser.
type hostListRow struct {
	Id               int
	Host             string
	PlatformDomainID string
	Remark           string
	Location         string
	Scheme           string
	IsClose          bool
	AutoHttps        bool
	PlatformManaged  bool
	Client           hostListClient
	Target           hostListTarget
}

type hostListClient struct {
	Id        int
	Remark    string
	IsConnect bool
}

type hostListTarget struct {
	TargetStr  string
	LocalProxy bool
}

type platformDomainOption struct {
	ID                    string
	Wildcard              string
	CertificateConfigured bool
}

type hostDiagnosticResult struct {
	Host    string              `json:"host"`
	Path    string              `json:"path"`
	Scheme  string              `json:"scheme"`
	Matched bool                `json:"matched"`
	Reason  string              `json:"reason"`
	Rule    *hostDiagnosticRule `json:"rule,omitempty"`
}

type hostDiagnosticRule struct {
	ID              int    `json:"id"`
	Host            string `json:"host"`
	Location        string `json:"location"`
	Scheme          string `json:"scheme"`
	Remark          string `json:"remark"`
	Client          string `json:"client"`
	Target          string `json:"target"`
	PlatformManaged bool   `json:"platformManaged"`
}

func newHostListRows(hosts []*file.Host) []*hostListRow {
	rows := make([]*hostListRow, 0, len(hosts))
	for _, host := range hosts {
		if host == nil {
			continue
		}
		host.RLock()
		client, target := host.Client, host.Target
		row := &hostListRow{
			Id:               host.Id,
			Host:             host.Host,
			PlatformDomainID: host.PlatformDomainID,
			Remark:           host.Remark,
			Location:         host.Location,
			Scheme:           host.Scheme,
			IsClose:          host.IsClose,
			AutoHttps:        host.AutoHttps,
			PlatformManaged:  host.PlatformDomainID != "",
		}
		host.RUnlock()

		if client != nil {
			client.RLock()
			row.Client = hostListClient{Id: client.Id, Remark: client.Remark, IsConnect: client.IsConnect}
			client.RUnlock()
		}
		if target != nil {
			target.RLock()
			row.Target = hostListTarget{TargetStr: target.TargetStr, LocalProxy: target.LocalProxy}
			target.RUnlock()
		}
		rows = append(rows, row)
	}
	return rows
}

func platformDomainOptions() []platformDomainOption {
	domains := file.GetDb().GetUsablePlatformDomains()
	options := make([]platformDomainOption, 0, len(domains))
	for _, domain := range domains {
		options = append(options, platformDomainOption{
			ID:                    domain.ID,
			Wildcard:              domain.Wildcard,
			CertificateConfigured: domain.CertFilePath != "" && domain.KeyFilePath != "",
		})
	}
	return options
}

func generatedPlatformPrefix() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	bytes := make([]byte, 8)
	if _, err := rand.Read(bytes); err != nil {
		return "npshost1"
	}
	for i, value := range bytes {
		bytes[i] = alphabet[int(value)%len(alphabet)]
	}
	return string(bytes)
}

func (s *IndexController) setHostFormData() {
	s.Data["platformDomains"] = platformDomainOptions()
	s.Data["platformDefaultPrefix"] = generatedPlatformPrefix()
}

func (s *IndexController) canAccessHost(host *file.Host) bool {
	if host == nil {
		return false
	}
	if s.IsAdmin() {
		return true
	}
	host.RLock()
	client := host.Client
	host.RUnlock()
	if client == nil {
		return false
	}
	client.RLock()
	clientID := client.Id
	client.RUnlock()
	return isAllowedClient(clientID, s.GetAllowedClientIds())
}

func (s *IndexController) authorizedHost(id int) (*file.Host, error) {
	host, err := file.GetDb().GetHostById(id)
	if err != nil {
		return nil, errors.New("host ID not found")
	}
	if !s.canAccessHost(host) {
		return nil, errors.New("permission denied")
	}
	return host, nil
}

func (s *IndexController) canAccessTask(task *file.Tunnel) bool {
	if task == nil {
		return false
	}
	if s.IsAdmin() {
		return true
	}
	task.RLock()
	client := task.Client
	task.RUnlock()
	if client == nil {
		return false
	}
	client.RLock()
	clientID := client.Id
	client.RUnlock()
	return isAllowedClient(clientID, s.GetAllowedClientIds())
}

func (s *IndexController) authorizedTask(id int) (*file.Tunnel, error) {
	task, err := file.GetDb().GetTask(id)
	if err != nil {
		return nil, errors.New("tunnel ID not found")
	}
	if !s.canAccessTask(task) {
		return nil, errors.New("permission denied")
	}
	return task, nil
}

func (s *IndexController) hostDomainFromRequest(excludeHostID int) (string, string, error) {
	mode := strings.TrimSpace(s.GetString("domain_mode"))
	if mode != "platform" {
		return s.getEscapeString("host"), "", nil
	}
	platformID := strings.TrimSpace(s.GetString("platform_domain_id"))
	prefix := strings.TrimSpace(s.GetString("platform_prefix"))
	host, err := file.GetDb().ResolvePlatformHost(platformID, prefix)
	if err != nil {
		return "", "", err
	}
	available, err := file.GetDb().IsPlatformHostAvailable(platformID, prefix, excludeHostID)
	if err != nil {
		return "", "", err
	}
	if !available {
		return "", "", errors.New("平台域名前缀已被使用")
	}
	return host, platformID, nil
}

func requestedLocalProxy(requested bool) bool {
	return requested && beego.AppConfig.DefaultBool("allow_local_proxy", false)
}

func clientTunnelLimitReached(client *file.Client) (bool, int) {
	if client == nil {
		return false, 0
	}
	client.RLock()
	maxTunnelNum, userID := client.MaxTunnelNum, client.UserId
	client.RUnlock()
	if maxTunnelNum > 0 && client.GetTunnelNum() >= maxTunnelNum {
		return true, userID
	}
	return false, userID
}

func (s *IndexController) Index() {
	s.Data["web_base_url"] = beego.AppConfig.String("web_base_url")
	s.Data["data"] = s.dashboardSnapshot()
	s.SetInfo("dashboard")
	s.display("index/index")
}

// DashboardData is the lightweight JSON snapshot used by the dashboard's
// periodic refresh. Scope is derived from the authenticated principal on each
// request, so a user cannot widen the response by changing query parameters.
func (s *IndexController) DashboardData() {
	s.Data["json"] = map[string]interface{}{
		"status": 1,
		"data":   s.dashboardSnapshot(),
	}
	s.ServeJSON()
	s.StopRun()
}

func (s *IndexController) dashboardSnapshot() map[string]interface{} {
	if s.IsAdmin() {
		return server.GetDashboardData()
	}
	return server.GetDashboardDataForClients(s.GetAllowedClientIds())
}

func (s *IndexController) Help() {
	s.SetInfo("about")
	s.display("index/help")
}

func (s *IndexController) Tcp() {
	s.SetInfo("tcp")
	s.SetType("tcp")
	s.display("index/list")
}

func (s *IndexController) Udp() {
	s.SetInfo("udp")
	s.SetType("udp")
	s.display("index/list")
}

func (s *IndexController) Socks5() {
	s.SetInfo("socks5")
	s.SetType("socks5")
	s.display("index/list")
}

func (s *IndexController) Http() {
	s.SetInfo("http proxy")
	s.SetType("httpProxy")
	s.display("index/list")
}
func (s *IndexController) File() {
	s.SetInfo("file server")
	s.SetType("file")
	s.display("index/list")
}

func (s *IndexController) Secret() {
	s.SetInfo("secret")
	s.SetType("secret")
	s.display("index/list")
}
func (s *IndexController) P2p() {
	s.SetInfo("p2p")
	s.SetType("p2p")
	s.display("index/list")
}

func (s *IndexController) Host() {
	s.SetInfo("host")
	s.SetType("hostServer")
	s.display("index/list")
}

func (s *IndexController) All() {
	s.Data["menu"] = "client"
	clientId := s.getEscapeString("client_id")
	s.Data["client_id"] = clientId
	s.SetInfo("client id:" + clientId)
	s.display("index/list")
}

func (s *IndexController) GetTunnel() {
	start, length := s.GetAjaxParams()
	taskType := s.getEscapeString("type")
	clientId := s.GetIntNoErr("client_id")
	var allowed map[int]struct{}
	if !s.IsAdmin() {
		allowed = s.GetAllowedClientIds()
	}
	list, cnt := server.GetTunnelByAllowedClients(start, length, taskType, clientId, s.getEscapeString("search"), s.getEscapeString("sort"), s.getEscapeString("order"), allowed)
	s.AjaxTable(list, cnt, cnt, nil)
}

func (s *IndexController) Add() {
	if s.Ctx.Request.Method == "GET" {
		s.Data["type"] = s.getEscapeString("type")
		s.Data["client_id"] = s.getEscapeString("client_id")
		s.SetInfo("add tunnel")
		s.display()
	} else {
		if !s.RequirePost() {
			return
		}
		id := int(file.GetDb().JsonDb.GetTaskId())
		t := &file.Tunnel{
			Port:         s.GetIntNoErr("port"),
			ServerIp:     s.getEscapeString("server_ip"),
			Mode:         s.getEscapeString("type"),
			Target:       &file.Target{TargetStr: s.getEscapeString("target"), LocalProxy: requestedLocalProxy(s.GetBoolNoErr("local_proxy"))},
			Id:           id,
			Status:       true,
			Remark:       s.getEscapeString("remark"),
			Password:     s.getEscapeString("password"),
			LocalPath:    s.getEscapeString("local_path"),
			StripPre:     s.getEscapeString("strip_pre"),
			ProtoVersion: s.getEscapeString("proto_version"),
			Flow:         &file.Flow{},
		}

		if t.Port <= 0 {
			t.Port = tool.GenerateServerPort(t.Mode)
		}

		if !tool.TestServerPort(t.Port, t.Mode) {
			s.AjaxErr("The port cannot be opened because it may has been occupied or is no longer allowed.")
			return
		}
		var err error
		if t.Client, err = file.GetDb().GetClient(s.GetIntNoErr("client_id")); err != nil {
			s.AjaxErr(err.Error())
			return
		}
		if !s.IsAdmin() && !isAllowedClient(t.Client.Id, s.GetAllowedClientIds()) {
			s.AjaxErr("permission denied")
			return
		}
		if reached, userID := clientTunnelLimitReached(t.Client); reached {
			s.AjaxErr("The number of tunnels exceeds the limit")
			return
		} else if userID != 0 && file.GetDb().IsUserTunnelLimitReached(userID) {
			s.AjaxErr("The number of user tunnels exceeds the limit")
			return
		}
		if err := file.GetDb().NewTask(t); err != nil {
			s.AjaxErr(err.Error())
			return
		}
		if err := server.AddTask(t); err != nil {
			s.AjaxErr(err.Error())
			return
		} else {
			s.AjaxOkWithId("add success", id)
		}
	}
}

func (s *IndexController) Copy() {
	if !s.RequirePost() {
		return
	}
	oldId := s.GetIntNoErr("id")
	oldTask, err := s.authorizedTask(oldId)
	if err != nil {
		s.AjaxErr(err.Error())
		return
	}
	oldTask.RLock()
	oldClient := oldTask.Client
	oldMode := oldTask.Mode
	oldServerIP := oldTask.ServerIp
	oldRemark := oldTask.Remark
	oldPassword := oldTask.Password
	oldLocalPath := oldTask.LocalPath
	oldStripPre := oldTask.StripPre
	oldProtoVersion := oldTask.ProtoVersion
	oldTarget := oldTask.Target
	oldTask.RUnlock()
	if oldClient == nil {
		s.AjaxErr("modified error,the client is not exist")
		return
	}
	oldClient.RLock()
	oldClientID := oldClient.Id
	oldClient.RUnlock()
	if client, err := file.GetDb().GetClient(oldClientID); err != nil {
		s.AjaxErr("modified error,the client is not exist")
		return
	} else {
		var targetStr string
		var localProxy bool
		if oldTarget != nil {
			oldTarget.RLock()
			targetStr, localProxy = oldTarget.TargetStr, oldTarget.LocalProxy
			oldTarget.RUnlock()
		}

		id := int(file.GetDb().JsonDb.GetTaskId())
		newTask := &file.Tunnel{
			Client:       client,
			Port:         tool.GenerateServerPort(oldMode),
			ServerIp:     oldServerIP,
			Mode:         oldMode,
			Target:       &file.Target{TargetStr: targetStr, LocalProxy: localProxy},
			Id:           id,
			Status:       true,
			Remark:       oldRemark,
			Password:     oldPassword,
			LocalPath:    oldLocalPath,
			StripPre:     oldStripPre,
			ProtoVersion: oldProtoVersion,
			Flow:         &file.Flow{},
		}
		if !tool.TestServerPort(newTask.Port, newTask.Mode) {
			s.AjaxErr("The port cannot be opened because it may has been occupied or is no longer allowed.")
			return
		}

		if reached, userID := clientTunnelLimitReached(newTask.Client); reached {
			s.AjaxErr("The number of tunnels exceeds the limit")
			return
		} else if userID != 0 && file.GetDb().IsUserTunnelLimitReached(userID) {
			s.AjaxErr("The number of user tunnels exceeds the limit")
			return
		}
		if err := file.GetDb().NewTask(newTask); err != nil {
			s.AjaxErr(err.Error())
			return
		}
		if err := server.AddTask(newTask); err != nil {
			s.AjaxErr(err.Error())
			return
		} else {
			s.AjaxOkWithId("add success", id)
		}
	}
}

func (s *IndexController) GetOneTunnel() {
	id := s.GetIntNoErr("id")
	data := make(map[string]interface{})
	if t, err := s.authorizedTask(id); err != nil {
		data["code"] = 0
	} else {
		data["code"] = 1
		data["data"] = t
	}
	s.Data["json"] = data
	s.ServeJSON()
}
func (s *IndexController) Edit() {
	id := s.GetIntNoErr("id")
	if id <= 0 {
		if s.Ctx.Request.Method == "GET" {
			s.error()
		} else {
			s.AjaxErr("tunnel ID not found")
		}
		return
	}
	if s.Ctx.Request.Method == "GET" {
		if t, err := s.authorizedTask(id); err != nil {
			s.error()
			return
		} else {
			s.Data["t"] = t
		}
		s.SetInfo("edit tunnel")
		s.display()
	} else {
		if !s.RequirePost() {
			return
		}
		if t, err := s.authorizedTask(id); err != nil {
			s.AjaxErr(err.Error())
			return
		} else {
			t.RLock()
			desiredClient := t.Client
			t.RUnlock()
			if desiredClient == nil {
				s.AjaxErr("modified error,the client is not exist")
				return
			}
			desiredPort := s.GetIntNoErr("port")
			desiredMode := s.getEscapeString("type")
			t.RLock()
			currentPort, currentMode := t.Port, t.Mode
			t.RUnlock()
			if desiredPort <= 0 {
				desiredPort = tool.GenerateServerPort(desiredMode)
			}
			if desiredPort != currentPort || desiredMode != currentMode {
				if !tool.TestServerPort(desiredPort, desiredMode) {
					s.AjaxErr("The port cannot be opened because it may has been occupied or is no longer allowed.")
					return
				}
			}
			desiredTarget := &file.Target{TargetStr: s.getEscapeString("target")}
			desiredTarget.LocalProxy = requestedLocalProxy(s.GetBoolNoErr("local_proxy"))
			// Build the replacement atomically. Proxy workers take the same
			// task lock when selecting their client, target and mode.
			t.Lock()
			t.Client = desiredClient
			t.Port = desiredPort
			t.ServerIp = s.getEscapeString("server_ip")
			t.Mode = desiredMode
			t.Target = desiredTarget
			t.Password = s.getEscapeString("password")
			t.Id = id
			t.LocalPath = s.getEscapeString("local_path")
			t.ProtoVersion = s.getEscapeString("proto_version")
			t.StripPre = s.getEscapeString("strip_pre")
			t.Remark = s.getEscapeString("remark")
			t.Unlock()
			if err := file.GetDb().UpdateTask(t); err != nil {
				s.AjaxErr("modified error")
				return
			}
			if err := server.StopServer(t.Id); err != nil {
				// A stopped task has no runtime entry and can be started directly;
				// surface real close failures instead of claiming a successful edit.
				if _, running := server.RunList.Load(t.Id); running {
					s.AjaxErr("stop error")
					return
				}
			}
			if err := server.StartTask(t.Id); err != nil {
				s.AjaxErr("start error")
				return
			}
		}
		s.AjaxOk("modified success")
	}
}

func (s *IndexController) Stop() {
	if !s.RequirePost() {
		return
	}
	id := s.GetIntNoErr("id")
	if id <= 0 {
		s.AjaxErr("tunnel ID not found")
		return
	}
	if _, err := s.authorizedTask(id); err != nil {
		s.AjaxErr(err.Error())
		return
	}
	if err := server.StopServer(id); err != nil {
		s.AjaxErr("stop error")
		return
	}
	s.AjaxOk("stop success")
}

func (s *IndexController) Del() {
	if !s.RequirePost() {
		return
	}
	id := s.GetIntNoErr("id")
	if id <= 0 {
		s.AjaxErr("tunnel ID not found")
		return
	}
	if _, err := s.authorizedTask(id); err != nil {
		s.AjaxErr(err.Error())
		return
	}
	if err := server.DelTask(id); err != nil {
		s.AjaxErr("delete error")
		return
	}
	s.AjaxOk("delete success")
}

func (s *IndexController) Start() {
	if !s.RequirePost() {
		return
	}
	id := s.GetIntNoErr("id")
	if id <= 0 {
		s.AjaxErr("tunnel ID not found")
		return
	}
	if _, err := s.authorizedTask(id); err != nil {
		s.AjaxErr(err.Error())
		return
	}
	if err := server.StartTask(id); err != nil {
		s.AjaxErr("start error")
		return
	}
	s.AjaxOk("start success")
}

func (s *IndexController) HostList() {
	if s.Ctx.Request.Method == "GET" {
		s.Data["client_id"] = s.getEscapeString("client_id")
		s.Data["menu"] = "host"
		s.SetInfo("host list")
		s.display("index/hlist")
	} else {
		start, length := s.GetAjaxParams()
		clientId := s.GetIntNoErr("client_id")
		var allowed map[int]struct{}
		if !s.IsAdmin() {
			allowed = s.GetAllowedClientIds()
		}
		list, cnt := file.GetDb().GetHostByAllowedClients(start, length, clientId, s.getEscapeString("search"), allowed)
		s.AjaxTable(newHostListRows(list), cnt, cnt, nil)
	}
}

func (s *IndexController) GetHost() {
	if !s.RequirePost() {
		return
	}
	data := make(map[string]interface{})
	if host, err := s.authorizedHost(s.GetIntNoErr("id")); err != nil {
		data["code"] = 0
	} else {
		rows := newHostListRows([]*file.Host{host})
		data["data"] = rows[0]
		data["code"] = 1
	}
	s.Data["json"] = data
	s.ServeJSON()
	s.StopRun()
}

func (s *IndexController) DelHost() {
	if !s.RequirePost() {
		return
	}
	id := s.GetIntNoErr("id")
	if _, err := s.authorizedHost(id); err != nil {
		s.AjaxErr(err.Error())
		return
	}
	if err := file.GetDb().DelHost(id); err != nil {
		s.AjaxErr("delete error")
		return
	}
	s.AjaxOk("delete success")
}

func (s *IndexController) HostStop() {
	if !s.RequirePost() {
		return
	}
	id := s.GetIntNoErr("id")
	if h, err := s.authorizedHost(id); err != nil {
		s.AjaxErr(err.Error())
		return
	} else {
		h.Lock()
		h.IsClose = true
		h.Unlock()
		file.GetDb().JsonDb.StoreHostToJsonFile()
	}
	s.AjaxOk("stop success")
}

func (s *IndexController) HostStart() {
	if !s.RequirePost() {
		return
	}
	id := s.GetIntNoErr("id")
	if h, err := s.authorizedHost(id); err != nil {
		s.AjaxErr(err.Error())
		return
	} else {
		h.Lock()
		h.IsClose = false
		h.Unlock()
		file.GetDb().JsonDb.StoreHostToJsonFile()
	}
	s.AjaxOk("start success")
}

func (s *IndexController) AddHost() {
	if s.Ctx.Request.Method == "GET" {
		s.Data["client_id"] = s.getEscapeString("client_id")
		s.Data["menu"] = "host"
		s.setHostFormData()
		s.SetInfo("add host")
		s.display("index/hadd")
	} else {
		if !s.RequirePost() {
			return
		}
		hostName, platformDomainID, err := s.hostDomainFromRequest(0)
		if err != nil {
			s.AjaxErr("add fail, " + err.Error())
			return
		}
		id := int(file.GetDb().JsonDb.GetHostId())
		h := &file.Host{
			Id:               id,
			Host:             hostName,
			PlatformDomainID: platformDomainID,
			Target:           &file.Target{TargetStr: s.getEscapeString("target"), LocalProxy: requestedLocalProxy(s.GetBoolNoErr("local_proxy"))},
			HeaderChange:     s.getEscapeString("header"),
			HostChange:       s.getEscapeString("hostchange"),
			Remark:           s.getEscapeString("remark"),
			Location:         s.getEscapeString("location"),
			Flow:             &file.Flow{},
			Scheme:           s.getEscapeString("scheme"),
			KeyFilePath:      s.getEscapeString("key_file_path"),
			CertFilePath:     s.getEscapeString("cert_file_path"),
			AutoHttps:        s.GetBoolNoErr("AutoHttps"),
		}

		if h.Scheme == "http" {
			h.AutoHttps = false
		}

		if h.Client, err = file.GetDb().GetClient(s.GetIntNoErr("client_id")); err != nil {
			s.AjaxErr("add error the client can not be found")
			return
		}
		if !s.IsAdmin() && !isAllowedClient(h.Client.Id, s.GetAllowedClientIds()) {
			s.AjaxErr("permission denied")
			return
		}
		if reached, userID := clientTunnelLimitReached(h.Client); reached {
			s.AjaxErr("The number of tunnels exceeds the limit")
			return
		} else if userID != 0 && file.GetDb().IsUserTunnelLimitReached(userID) {
			s.AjaxErr("The number of user tunnels exceeds the limit")
			return
		}

		if err := file.GetDb().NewHost(h); err != nil {
			s.AjaxErr("add fail" + err.Error())
			return
		}
		s.AjaxOkWithId("add success", id)
	}
}

func (s *IndexController) EditHost() {
	id := s.GetIntNoErr("id")
	if s.Ctx.Request.Method == "GET" {
		s.Data["menu"] = "host"
		if h, err := s.authorizedHost(id); err != nil {
			s.error()
			return
		} else {
			s.Data["h"] = h
			h.RLock()
			platformDomainID, hostName := h.PlatformDomainID, h.Host
			h.RUnlock()
			s.Data["hostIsPlatform"] = platformDomainID != ""
			if domain, domainErr := file.GetDb().GetPlatformDomain(platformDomainID); domainErr == nil {
				s.Data["platformPrefix"] = strings.TrimSuffix(hostName, "."+strings.TrimPrefix(domain.Wildcard, "*."))
			}
		}
		s.setHostFormData()
		s.SetInfo("edit")
		s.display("index/hedit")
	} else {
		if !s.RequirePost() {
			return
		}
		storedHost, err := s.authorizedHost(id)
		if err != nil {
			s.AjaxErr(err.Error())
			return
		} else {
			desiredHost, platformDomainID, domainErr := s.hostDomainFromRequest(id)
			if domainErr != nil {
				s.AjaxErr("modified error," + domainErr.Error())
				return
			}
			desiredLocation := s.getEscapeString("location")
			desiredScheme := s.getEscapeString("scheme")
			storedHost.RLock()
			desiredClient := storedHost.Client
			storedHost.RUnlock()
			if desiredClient == nil {
				s.AjaxErr("modified error,the client is not exist")
				return
			}
			if !s.IsAdmin() && !isAllowedClient(desiredClient.Id, s.GetAllowedClientIds()) {
				s.AjaxErr("permission denied")
				return
			}
			desiredTarget := &file.Target{TargetStr: s.getEscapeString("target"), LocalProxy: requestedLocalProxy(s.GetBoolNoErr("local_proxy"))}
			autoHTTPS := s.GetBoolNoErr("AutoHttps")
			if desiredScheme == "http" {
				autoHTTPS = false
			}
			replacement := &file.Host{
				Id:               id,
				Host:             desiredHost,
				PlatformDomainID: platformDomainID,
				Client:           desiredClient,
				Target:           desiredTarget,
				HeaderChange:     s.getEscapeString("header"),
				HostChange:       s.getEscapeString("hostchange"),
				Remark:           s.getEscapeString("remark"),
				Location:         desiredLocation,
				Scheme:           desiredScheme,
				KeyFilePath:      s.getEscapeString("key_file_path"),
				CertFilePath:     s.getEscapeString("cert_file_path"),
				AutoHttps:        autoHTTPS,
			}
			if err := file.GetDb().UpdateHost(replacement); err != nil {
				s.AjaxErr("modified error," + err.Error())
				return
			}
		}
		s.AjaxOk("modified success")
	}
}

// PlatformHostAvailable provides immediate feedback while a user edits a
// managed wildcard prefix. NewHost and UpdateHost repeat the validation while
// holding the data-layer mutation lock, so this endpoint is only a UX aid.
func (s *IndexController) PlatformHostAvailable() {
	if !s.RequirePost() {
		return
	}
	platformID := strings.TrimSpace(s.GetString("platform_domain_id"))
	prefix := strings.TrimSpace(s.GetString("platform_prefix"))
	host, err := file.GetDb().ResolvePlatformHost(platformID, prefix)
	if err != nil {
		s.AjaxErr(err.Error())
		return
	}
	available, err := file.GetDb().IsPlatformHostAvailable(platformID, prefix, s.GetIntNoErr("id"))
	if err != nil {
		s.AjaxErr(err.Error())
		return
	}
	s.Data["json"] = map[string]interface{}{
		"status":    1,
		"available": available,
		"host":      host,
	}
	s.ServeJSON()
	s.StopRun()
}

func diagnosticHostRuleMatches(host, rule string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	rule = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(rule)), ".")
	if host == "" || rule == "" {
		return false
	}
	if !strings.Contains(rule, "*") {
		return host == rule
	}
	if !strings.HasPrefix(rule, "*.") || strings.Count(rule, "*") != 1 {
		return false
	}
	suffix := strings.TrimPrefix(rule, "*.")
	return len(host) > len(suffix)+1 && strings.HasSuffix(host, "."+suffix)
}

func diagnosticPathMatches(path, location string) bool {
	if location == "" || location == "/" {
		return strings.HasPrefix(path, "/")
	}
	if !strings.HasPrefix(path, location) {
		return false
	}
	if len(path) == len(location) {
		return true
	}
	next := path[len(location)]
	return next == '/' || next == '?'
}

func (s *IndexController) explainHostDiagnosticFailure(host, path, scheme string) string {
	var allowed map[int]struct{}
	if !s.IsAdmin() {
		allowed = s.GetAllowedClientIds()
	}
	hosts, _ := file.GetDb().GetHostByAllowedClients(0, 100000, 0, "", allowed)
	matchedHost, matchedEnabled, matchedScheme, matchedPath := false, false, false, false
	for _, candidate := range hosts {
		if candidate == nil || !s.canAccessHost(candidate) {
			continue
		}
		candidate.RLock()
		rule, location, ruleScheme, isClose, target := candidate.Host, candidate.Location, candidate.Scheme, candidate.IsClose, candidate.Target
		candidate.RUnlock()
		if !diagnosticHostRuleMatches(host, rule) {
			continue
		}
		matchedHost = true
		if isClose {
			continue
		}
		matchedEnabled = true
		if ruleScheme != "" && ruleScheme != "all" && ruleScheme != scheme {
			continue
		}
		matchedScheme = true
		if !diagnosticPathMatches(path, location) {
			continue
		}
		matchedPath = true
		if target == nil {
			return "匹配规则尚未配置内网目标。"
		}
		target.RLock()
		targetStr := strings.TrimSpace(target.TargetStr)
		target.RUnlock()
		if targetStr == "" {
			return "匹配规则尚未配置内网目标。"
		}
	}
	switch {
	case !matchedHost:
		return "没有匹配的域名规则。"
	case !matchedEnabled:
		return "匹配到的域名规则均已停用。"
	case !matchedScheme:
		return "请求协议与匹配规则的协议不一致。"
	case !matchedPath:
		return "请求路径没有匹配到已启用规则的路由。"
	default:
		return "规则当前没有可用的内网目标。"
	}
}

func (s *IndexController) HostDiagnose() {
	if s.Ctx.Request.Method == http.MethodGet {
		s.Data["menu"] = "host"
		s.SetInfo("host diagnose")
		s.display("index/hdiagnose")
		return
	}
	if !s.RequirePost() {
		return
	}

	host := common.GetIpByAddr(strings.TrimSpace(s.GetString("host")))
	path := strings.TrimSpace(s.GetString("path"))
	scheme := strings.ToLower(strings.TrimSpace(s.GetString("scheme")))
	if host == "" {
		s.AjaxErr("Host 不能为空")
		return
	}
	if scheme != "http" && scheme != "https" {
		s.AjaxErr("协议必须是 HTTP 或 HTTPS")
		return
	}
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		s.AjaxErr("路径必须以 / 开头")
		return
	}
	parsedPath, err := url.ParseRequestURI(path)
	if err != nil {
		s.AjaxErr("路径格式无效")
		return
	}
	request := &http.Request{Host: host, URL: &url.URL{Scheme: scheme, Path: parsedPath.Path, RawQuery: parsedPath.RawQuery}, RequestURI: path}
	result := hostDiagnosticResult{Host: host, Path: path, Scheme: scheme}
	matched, matchErr := file.GetDb().GetInfoByHost(host, request)
	if matchErr != nil || !s.canAccessHost(matched) {
		result.Reason = s.explainHostDiagnosticFailure(host, path, scheme)
		s.Data["json"] = map[string]interface{}{"status": 1, "data": result}
		s.ServeJSON()
		s.StopRun()
		return
	}

	matched.RLock()
	client, target := matched.Client, matched.Target
	rule := &hostDiagnosticRule{
		ID:              matched.Id,
		Host:            matched.Host,
		Location:        matched.Location,
		Scheme:          matched.Scheme,
		Remark:          matched.Remark,
		PlatformManaged: matched.PlatformDomainID != "",
	}
	matched.RUnlock()
	if client != nil {
		client.RLock()
		clientID, clientRemark := client.Id, strings.TrimSpace(client.Remark)
		client.RUnlock()
		rule.Client = "客户端 " + strconv.Itoa(clientID)
		if clientRemark != "" {
			rule.Client += " · " + clientRemark
		}
	}
	if target != nil {
		if selected, targetErr := target.PreviewTarget(); targetErr == nil {
			rule.Target = selected
		} else {
			result.Reason = "规则已命中，但没有可用的内网目标。"
		}
	}
	if rule.Target == "" && result.Reason == "" {
		result.Reason = "规则已命中，但没有可用的内网目标。"
	}
	result.Matched = true
	result.Rule = rule
	s.Data["json"] = map[string]interface{}{"status": 1, "data": result}
	s.ServeJSON()
	s.StopRun()
}
