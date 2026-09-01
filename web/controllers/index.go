package controllers

import (
	"ehang.io/nps/lib/file"
	"ehang.io/nps/server"
	"ehang.io/nps/server/tool"

	"github.com/astaxie/beego"
)

type IndexController struct {
	BaseController
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
	s.Data["data"] = server.GetDashboardData()
	s.SetInfo("dashboard")
	s.display("index/index")
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
	if oldTask, err := file.GetDb().GetTask(oldId); err != nil {
		s.AjaxErr("tunnel ID not found")
		return
	} else {
		if client, err := file.GetDb().GetClient(oldTask.Client.Id); err != nil {
			s.AjaxErr("modified error,the client is not exist")
			return
		} else {
			oldTask.Client = client
		}

		id := int(file.GetDb().JsonDb.GetTaskId())
		newTask := &file.Tunnel{
			Client:       oldTask.Client,
			Port:         tool.GenerateServerPort(oldTask.Mode),
			ServerIp:     oldTask.ServerIp,
			Mode:         oldTask.Mode,
			Target:       oldTask.Target,
			Id:           id,
			Status:       true,
			Remark:       oldTask.Remark,
			Password:     oldTask.Password,
			LocalPath:    oldTask.LocalPath,
			StripPre:     oldTask.StripPre,
			ProtoVersion: oldTask.ProtoVersion,
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
	if t, err := file.GetDb().GetTask(id); err != nil {
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
		if t, err := file.GetDb().GetTask(id); err != nil {
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
		if t, err := file.GetDb().GetTask(id); err != nil {
			s.AjaxErr("tunnel ID not found")
			return
		} else {
			desiredClient, clientErr := file.GetDb().GetClient(s.GetIntNoErr("client_id"))
			if clientErr != nil {
				s.AjaxErr("modified error,the client is not exist")
				return
			}
			if !s.IsAdmin() && !isAllowedClient(desiredClient.Id, s.GetAllowedClientIds()) {
				s.AjaxErr("permission denied")
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
		s.AjaxTable(list, cnt, cnt, nil)
	}
}

func (s *IndexController) GetHost() {
	if s.Ctx.Request.Method == "POST" {
		data := make(map[string]interface{})
		if h, err := file.GetDb().GetHostById(s.GetIntNoErr("id")); err != nil {
			data["code"] = 0
		} else {
			data["data"] = h
			data["code"] = 1
		}
		s.Data["json"] = data
		s.ServeJSON()
	}
}

func (s *IndexController) DelHost() {
	if !s.RequirePost() {
		return
	}
	id := s.GetIntNoErr("id")
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
	if h, err := file.GetDb().GetHostById(id); err != nil {
		s.AjaxErr("stop error")
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
	if h, err := file.GetDb().GetHostById(id); err != nil {
		s.AjaxErr("start error")
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
		s.SetInfo("add host")
		s.display("index/hadd")
	} else {
		if !s.RequirePost() {
			return
		}
		id := int(file.GetDb().JsonDb.GetHostId())
		h := &file.Host{
			Id:           id,
			Host:         s.getEscapeString("host"),
			Target:       &file.Target{TargetStr: s.getEscapeString("target"), LocalProxy: requestedLocalProxy(s.GetBoolNoErr("local_proxy"))},
			HeaderChange: s.getEscapeString("header"),
			HostChange:   s.getEscapeString("hostchange"),
			Remark:       s.getEscapeString("remark"),
			Location:     s.getEscapeString("location"),
			Flow:         &file.Flow{},
			Scheme:       s.getEscapeString("scheme"),
			KeyFilePath:  s.getEscapeString("key_file_path"),
			CertFilePath: s.getEscapeString("cert_file_path"),
			AutoHttps:    s.GetBoolNoErr("AutoHttps"),
		}

		if h.Scheme == "http" {
			h.AutoHttps = false
		}

		var err error
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
		if h, err := file.GetDb().GetHostById(id); err != nil {
			s.error()
			return
		} else {
			s.Data["h"] = h
		}
		s.SetInfo("edit")
		s.display("index/hedit")
	} else {
		if !s.RequirePost() {
			return
		}
		if _, err := file.GetDb().GetHostById(id); err != nil {
			s.AjaxErr("host ID not found")
			return
		} else {
			desiredHost := s.getEscapeString("host")
			desiredLocation := s.getEscapeString("location")
			desiredScheme := s.getEscapeString("scheme")
			desiredClient, clientErr := file.GetDb().GetClient(s.GetIntNoErr("client_id"))
			if clientErr != nil {
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
				Id:           id,
				Host:         desiredHost,
				Client:       desiredClient,
				Target:       desiredTarget,
				HeaderChange: s.getEscapeString("header"),
				HostChange:   s.getEscapeString("hostchange"),
				Remark:       s.getEscapeString("remark"),
				Location:     desiredLocation,
				Scheme:       desiredScheme,
				KeyFilePath:  s.getEscapeString("key_file_path"),
				CertFilePath: s.getEscapeString("cert_file_path"),
				AutoHttps:    autoHTTPS,
			}
			if err := file.GetDb().UpdateHost(replacement); err != nil {
				s.AjaxErr("modified error," + err.Error())
				return
			}
		}
		s.AjaxOk("modified success")
	}
}
