package controllers

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"ehang.io/nps/lib/acl"
	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/file"
	npsversion "ehang.io/nps/lib/version"
	"ehang.io/nps/server"
	"github.com/astaxie/beego"
)

type ClientController struct {
	BaseController
}

type clientListRow struct {
	Id                  int
	UserId              int
	UserName            string
	VerifyKey           string
	Addr                string
	LocalAddr           string
	Remark              string
	Status              bool
	IsConnect           bool
	RateLimit           int
	Flow                clientListFlow
	Rate                clientListRate
	NoStore             bool
	MaxConn             int
	NowConn             int32
	ConfigConnAllow     bool
	MaxTunnelNum        int
	Version             string
	CanRemoteUpdate     bool
	WebUserName         string
	WebPasswordSet      bool
	BasicUserName       string
	BasicPasswordSet    bool
	BlackIpList         []string
	WhiteIpList         []string
	IpListPriority      int
	CreateTime          string
	LastOnlineTime      string
	ConnectionHistory   []file.ConnectionEvent
	IpWhite             bool
	IpWhiteList         []string
	RegisteredOnly      bool
	RegisterKeySet      bool
	RegisterMaxHours    int
	ExpireTime          string
	UserExpireTime      string
	EffectiveExpireTime string
}

func supportsRemoteClientUpdate(clientVersion string) bool {
	comparison, err := npsversion.Compare(clientVersion, npsversion.ClientUpdateMinVersion)
	return err == nil && comparison >= 0
}

// validateClientRegisterKey keeps registration links unambiguous.  A
// registration key must not equal any client's verify key or another
// client's registration key; an empty key simply disables client-specific
// registration while the administrator/public key can still be used.
func validateClientRegisterKey(id int, key, verifyKey string) error {
	key = strings.TrimSpace(key)
	verifyKey = strings.TrimSpace(verifyKey)
	if key == "" {
		return nil
	}
	if key == verifyKey {
		return errors.New("IP 登记密钥不能与客户端验证密钥相同")
	}
	duplicate := false
	file.GetDb().JsonDb.Clients.Range(func(_, value interface{}) bool {
		other, ok := value.(*file.Client)
		if !ok || other == nil {
			return true
		}
		other.RLock()
		if other.Id != id && (strings.TrimSpace(other.RegisterKey) == key || strings.TrimSpace(other.VerifyKey) == key) {
			duplicate = true
		}
		other.RUnlock()
		return !duplicate
	})
	if duplicate {
		return errors.New("IP 登记密钥已被其他客户端使用")
	}
	return nil
}

type clientListFlow struct {
	ExportFlow int64
	InletFlow  int64
	FlowLimit  int64
}

type clientListRate struct {
	NowRate int64
}

// clientEditSnapshot lets the controller revert an in-place edit if the
// second, atomic data-layer validation loses a race with another ownership
// change. Existing tunnels and hosts deliberately keep the same Client
// pointer, so replacing it in the map would leave running proxies on stale
// settings.
type clientEditSnapshot struct {
	verifyKey        string
	userID           int
	remark           string
	rateLimit        int
	flow             *file.Flow
	flowLimit        int64
	maxConn          int
	configConnAllow  bool
	maxTunnelNum     int
	webUserName      string
	webPassword      string
	cnf              *file.Config
	blackIPList      []string
	whiteIPList      []string
	ipListPriority   int
	ipWhite          bool
	ipWhitePass      string
	ipWhiteList      []string
	registerKey      string
	registerMaxHours int
	registeredOnly   bool
	expireTime       string
}

func snapshotClientForEdit(client *file.Client) clientEditSnapshot {
	client.RLock()
	snapshot := clientEditSnapshot{
		verifyKey:        client.VerifyKey,
		userID:           client.UserId,
		remark:           client.Remark,
		rateLimit:        client.RateLimit,
		flow:             client.Flow,
		maxConn:          client.MaxConn,
		configConnAllow:  client.ConfigConnAllow,
		maxTunnelNum:     client.MaxTunnelNum,
		webUserName:      client.WebUserName,
		webPassword:      client.WebPassword,
		blackIPList:      append([]string(nil), client.BlackIpList...),
		whiteIPList:      append([]string(nil), client.WhiteIpList...),
		ipListPriority:   client.IpListPriority,
		ipWhite:          client.IpWhite,
		ipWhitePass:      client.IpWhitePass,
		ipWhiteList:      append([]string(nil), client.IpWhiteList...),
		registerKey:      client.RegisterKey,
		registerMaxHours: client.RegisterMaxHours,
		registeredOnly:   client.RegisteredOnly,
		expireTime:       client.ExpireTime,
	}
	if client.Cnf != nil {
		config := *client.Cnf
		snapshot.cnf = &config
	}
	client.RUnlock()
	_, _, snapshot.flowLimit = snapshot.flow.Snapshot()
	return snapshot
}

func (snapshot clientEditSnapshot) restore(client *file.Client) {
	client.Lock()
	client.VerifyKey = snapshot.verifyKey
	client.UserId = snapshot.userID
	client.Remark = snapshot.remark
	client.RateLimit = snapshot.rateLimit
	client.Flow = snapshot.flow
	client.MaxConn = snapshot.maxConn
	client.ConfigConnAllow = snapshot.configConnAllow
	client.MaxTunnelNum = snapshot.maxTunnelNum
	client.WebUserName = snapshot.webUserName
	client.WebPassword = snapshot.webPassword
	if snapshot.cnf == nil {
		client.Cnf = nil
	} else {
		config := *snapshot.cnf
		client.Cnf = &config
	}
	client.BlackIpList = append([]string(nil), snapshot.blackIPList...)
	client.WhiteIpList = append([]string(nil), snapshot.whiteIPList...)
	client.IpListPriority = snapshot.ipListPriority
	client.IpWhite = snapshot.ipWhite
	client.IpWhitePass = snapshot.ipWhitePass
	client.IpWhiteList = append([]string(nil), snapshot.ipWhiteList...)
	client.RegisterKey = snapshot.registerKey
	client.RegisterMaxHours = snapshot.registerMaxHours
	client.RegisteredOnly = snapshot.registeredOnly
	client.ExpireTime = snapshot.expireTime
	client.Unlock()
	client.SetSourceLists(snapshot.ipListPriority, snapshot.blackIPList, snapshot.whiteIPList, acl.ScopeUser, "client")
	if snapshot.flow != nil {
		snapshot.flow.SetLimit(snapshot.flowLimit)
	}
}

func keepClientBasicPassword(current, submitted string) string {
	if strings.TrimSpace(submitted) == "" {
		return current
	}
	return submitted
}

// mergeLegacyClientLogin keeps the old per-client dashboard credentials
// available without letting an edit form accidentally clear them. The current
// User model is the primary account boundary; these fields are only a
// deliberate compatibility escape hatch for older deployments.
func mergeLegacyClientLogin(currentUsername, currentPassword, submittedUsername, submittedPassword string, clear bool) (string, string, error) {
	if clear {
		return "", "", nil
	}
	if strings.TrimSpace(submittedUsername) == "" && strings.TrimSpace(submittedPassword) == "" {
		// Do not turn an unrelated edit into a migration failure when an old
		// record already contains an incomplete legacy pair. It remains usable
		// through the current User binding and can be repaired explicitly later.
		return currentUsername, currentPassword, nil
	}
	nextUsername, nextPassword := currentUsername, currentPassword
	if strings.TrimSpace(submittedUsername) != "" {
		nextUsername = submittedUsername
	}
	if strings.TrimSpace(submittedPassword) != "" {
		nextPassword = submittedPassword
	}
	if (strings.TrimSpace(nextUsername) == "") != (strings.TrimSpace(nextPassword) == "") {
		return nextUsername, nextPassword, errors.New("旧版客户端登录用户名和密码必须同时填写")
	}
	return nextUsername, nextPassword, nil
}

func parseClientOwnerID(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	userID, err := strconv.Atoi(value)
	if err != nil || userID < 0 {
		return 0, errors.New("所属用户无效")
	}
	return userID, nil
}

func requestedClientOwnerID(s *ClientController) (int, error) {
	return parseClientOwnerID(s.GetString("user_id"))
}

func clientOwnerFieldSubmitted(s *ClientController) bool {
	if s == nil || s.Ctx == nil || s.Ctx.Request == nil {
		return false
	}
	_ = s.Ctx.Request.ParseForm()
	_, submitted := s.Ctx.Request.Form["user_id"]
	return submitted
}

// userClientSettings contains the controls that an ordinary dashboard user is
// allowed to set on a client they own. Administrative ownership, quota,
// verification, expiry, and legacy-login fields are intentionally absent.
type userClientSettings struct {
	Remark           string
	BasicUser        string
	BasicPass        string
	Compress         bool
	Crypt            bool
	IpWhite          bool
	IpWhitePass      string
	IpWhiteList      []string
	BlackIPList      []string
	WhiteIPList      []string
	IpListPriority   int
	RegisterKey      string
	RegisterMaxHours int
	RegisteredOnly   bool
}

func userClientSettingsFromRequest(s *ClientController) userClientSettings {
	return userClientSettings{
		Remark:           s.getEscapeString("remark"),
		BasicUser:        s.getEscapeString("u"),
		BasicPass:        s.getEscapeString("p"),
		Compress:         common.GetBoolByStr(s.getEscapeString("compress")),
		Crypt:            s.GetBoolNoErr("crypt"),
		IpWhite:          s.GetBoolNoErr("ipwhite"),
		IpWhitePass:      s.getEscapeString("ipwhitepass"),
		IpWhiteList:      RemoveRepeatedElement(strings.Split(s.getEscapeString("ipwhitelist"), "\r\n")),
		BlackIPList:      RemoveRepeatedElement(strings.Split(s.getEscapeString("blackiplist"), "\r\n")),
		WhiteIPList:      RemoveRepeatedElement(strings.Split(s.getEscapeString("whiteiplist"), "\r\n")),
		IpListPriority:   s.GetIntNoErr("iplistpriority"),
		RegisterKey:      strings.TrimSpace(s.getEscapeString("register_key")),
		RegisterMaxHours: max(s.GetIntNoErr("register_max_hours"), 0),
		RegisteredOnly:   s.GetBoolNoErr("registered_only"),
	}
}

// newUserOwnedClient applies only the ordinary user's explicitly supported
// settings. The verification key is generated by DbUtils.NewClient; resource
// limits, ownership, expiry, and legacy Web credentials retain server-side
// defaults and cannot be forged through the request body.
func newUserOwnedClient(userID int, settings userClientSettings, createdAt time.Time) *file.Client {
	client := file.NewClient("", false, false)
	client.UserId = userID
	client.Remark = settings.Remark
	client.ConfigConnAllow = true
	client.Cnf.U = settings.BasicUser
	client.Cnf.P = settings.BasicPass
	client.Cnf.Compress = settings.Compress
	client.Cnf.Crypt = settings.Crypt
	client.IpWhite = settings.IpWhite
	client.IpWhitePass = settings.IpWhitePass
	client.IpWhiteList = settings.IpWhiteList
	client.BlackIpList = settings.BlackIPList
	client.WhiteIpList = settings.WhiteIPList
	client.IpListPriority = settings.IpListPriority
	client.RegisterKey = settings.RegisterKey
	client.RegisterMaxHours = settings.RegisterMaxHours
	client.RegisteredOnly = settings.RegisteredOnly
	client.SetSourceLists(settings.IpListPriority, settings.BlackIPList, settings.WhiteIPList, acl.ScopeUser, "client")
	client.CreateTime = createdAt.Format("2006-01-02 15:04:05")
	return client
}

// currentUserClientOwner resolves the owner used when an ordinary dashboard
// User creates a client. Legacy client-login sessions are intentionally
// excluded; they can manage only their already assigned client through the
// existing ownership checks.
func (s *ClientController) currentUserClientOwner() (int, error) {
	userID, ok := s.currentUserPrincipalID()
	if !ok {
		return 0, errors.New("只有用户账号可以新增客户端")
	}
	if !file.GetDb().IsUserActive(userID) {
		return 0, errors.New("所属用户不可用")
	}
	return userID, nil
}

func newClientListRows(clients []*file.Client) []*clientListRow {
	rows := make([]*clientListRow, 0, len(clients))
	for _, client := range clients {
		if client == nil {
			continue
		}
		client.RLock()
		flow, clientRate, userID, userName := client.Flow, client.Rate, client.UserId, client.UserName
		webUserName, webPasswordSet := client.WebUserName, client.WebPassword != ""
		basicUserName, basicPasswordSet := "", false
		if client.Cnf != nil {
			basicUserName, basicPasswordSet = client.Cnf.U, client.Cnf.P != ""
		}
		row := &clientListRow{
			Id:               client.Id,
			UserId:           userID,
			UserName:         userName,
			VerifyKey:        client.VerifyKey,
			Addr:             client.Addr,
			LocalAddr:        client.LocalAddr,
			Remark:           client.Remark,
			Status:           client.Status,
			IsConnect:        client.IsConnect,
			RateLimit:        client.RateLimit,
			NoStore:          client.NoStore,
			MaxConn:          client.MaxConn,
			NowConn:          atomic.LoadInt32(&client.NowConn),
			ConfigConnAllow:  client.ConfigConnAllow,
			MaxTunnelNum:     client.MaxTunnelNum,
			Version:          client.Version,
			CanRemoteUpdate:  supportsRemoteClientUpdate(client.Version),
			WebUserName:      webUserName,
			WebPasswordSet:   webPasswordSet,
			BasicUserName:    basicUserName,
			BasicPasswordSet: basicPasswordSet,
			BlackIpList:      append([]string(nil), client.BlackIpList...),
			WhiteIpList:      append([]string(nil), client.WhiteIpList...),
			IpListPriority:   client.IpListPriority,
			CreateTime:       client.CreateTime,
			LastOnlineTime:   client.LastOnlineTime,
			IpWhite:          client.IpWhite,
			IpWhiteList:      append([]string(nil), client.IpWhiteList...),
			RegisteredOnly:   client.RegisteredOnly,
			RegisterKeySet:   strings.TrimSpace(client.RegisterKey) != "",
			RegisterMaxHours: client.RegisterMaxHours,
			ExpireTime:       client.ExpireTime,
		}
		client.RUnlock()
		row.ConnectionHistory = client.ConnectionHistorySnapshot()
		if userID > 0 {
			if user, err := file.GetDb().GetUser(userID); err == nil && user != nil {
				user.RLock()
				row.UserName, row.UserExpireTime = user.UserName, user.ExpireTime
				user.RUnlock()
			}
		}
		row.Flow.InletFlow, row.Flow.ExportFlow, row.Flow.FlowLimit = flow.Snapshot()
		if clientRate != nil {
			row.Rate.NowRate = clientRate.CurrentRate()
		}
		if effectiveExpireTime, err := file.GetDb().EffectiveClientExpireTime(client); err == nil {
			row.EffectiveExpireTime = effectiveExpireTime
		}
		rows = append(rows, row)
	}
	return rows
}

func (s *ClientController) List() {
	if s.Ctx.Request.Method == "GET" {
		s.Data["menu"] = "client"
		s.Data["client_update_min_version"] = npsversion.ClientUpdateMinVersion
		s.Data["allow_remote_update"] = beego.AppConfig.DefaultBool("allow_remote_update", true)
		s.setOwnerFilterData()
		s.SetInfo("client")
		s.display("client/list")
		return
	}
	start, length := s.GetAjaxParams()
	clientId := 0
	search, sort, order := s.getEscapeString("search"), s.getEscapeString("sort"), s.getEscapeString("order")
	var list []*file.Client
	var cnt int
	if s.IsAdmin() {
		owner, err := s.getOwnerListFilter()
		if err != nil {
			s.AjaxErr(err.Error())
			return
		}
		list, cnt = server.GetClientListByOwnerFilter(start, length, search, sort, order, clientId, owner, nil)
	} else {
		list, cnt = server.GetClientListForAllowedIds(start, length, search, sort, order, clientId, s.GetAllowedClientIds())
	}
	cmd := make(map[string]interface{})
	global := file.GetDb().GetGlobal()
	serverURL := ""
	if global != nil {
		serverURL = global.ServerUrl
	}
	cmd["ip"] = publicBridgeHost(s.Ctx.Request.Host, serverURL, beego.AppConfig.String("bridge_addr"))
	cmd["bridgeType"] = beego.AppConfig.String("bridge_type")
	cmd["bridgePort"] = server.Bridge.TunnelPort
	s.AjaxTable(newClientListRows(list), cnt, cnt, cmd)
}

// Upgrade tells a connected, update-capable NPC to run its built-in updater.
// Updating is asynchronous; success here means only that the signal was sent.
func (s *ClientController) Upgrade() {
	if !s.RequirePost() || !s.RequireAdmin() {
		return
	}
	if !beego.AppConfig.DefaultBool("allow_remote_update", true) {
		s.AjaxErr("服务端已全局禁止远程升级")
		return
	}
	id := s.GetIntNoErr("id")
	if id <= 0 {
		s.AjaxErr("客户端编号无效")
		return
	}
	client, err := file.GetDb().GetClient(id)
	if err != nil || client == nil {
		s.AjaxErr("客户端不存在")
		return
	}
	client.RLock()
	ownerID, clientVersion := client.UserId, client.Version
	enabled, noStore := client.Status, client.NoStore
	client.RUnlock()
	if !enabled || noStore {
		err = errors.New("客户端已停用或不支持远程升级")
		s.auditMutation("client.remote_update", "client", id, ownerID, "", err, nil)
		s.AjaxErr(err.Error())
		return
	}
	if !supportsRemoteClientUpdate(clientVersion) {
		err = fmt.Errorf("当前客户端版本 %q 不支持远程升级，请先手动升级到 v%s", clientVersion, npsversion.ClientUpdateMinVersion)
		s.auditMutation("client.remote_update", "client", id, ownerID, "", err, map[string]string{"version": clientVersion})
		s.AjaxErr(err.Error())
		return
	}
	if err = server.RequestClientUpdate(id); err != nil {
		s.auditMutation("client.remote_update", "client", id, ownerID, "", err, map[string]string{"version": clientVersion})
		s.AjaxErr(err.Error())
		return
	}
	s.auditMutation("client.remote_update", "client", id, ownerID, "", nil, map[string]string{"version": clientVersion})
	s.AjaxOk("升级指令已下发；客户端下载并校验新版本后将自动重启")
}

// 添加客户端
func (s *ClientController) Add() {
	if s.Ctx.Request.Method == "GET" {
		s.Data["menu"] = "client"
		s.SetInfo("add client")
		// Only administrators need the owner selector. Ordinary Users are
		// always bound to their own account by the POST handler.
		if s.IsAdmin() {
			s.Data["users"], _ = file.GetDb().GetUserList(0, 10000, "")
		}
		s.display()
	} else {
		if !s.RequirePost() {
			return
		}
		isAdmin := s.IsAdmin()
		var t *file.Client
		if isAdmin {
			userID, err := requestedClientOwnerID(s)
			if err != nil {
				s.AjaxErr(err.Error())
				return
			}
			if err := file.GetDb().ValidateClientOwner(userID); err != nil {
				s.AjaxErr(err.Error())
				return
			}
			webUsername := s.getEscapeString("web_username")
			webPassword := s.getEscapeString("web_password")
			if _, _, err := mergeLegacyClientLogin("", "", webUsername, webPassword, false); err != nil {
				s.AjaxErr(err.Error())
				return
			}
			expireTime, expireErr := normalizeUserExpireTime(s.GetString("expire_time"))
			if expireErr != nil {
				s.AjaxErr("到期时间格式无效")
				return
			}
			id := int(file.GetDb().JsonDb.GetClientId())
			t = &file.Client{
				VerifyKey: s.getEscapeString("vkey"),
				Id:        id,
				UserId:    userID,
				Status:    true,
				Remark:    s.getEscapeString("remark"),
				Cnf: &file.Config{
					U:        s.getEscapeString("u"),
					P:        s.getEscapeString("p"),
					Compress: common.GetBoolByStr(s.getEscapeString("compress")),
					Crypt:    s.GetBoolNoErr("crypt"),
				},
				ConfigConnAllow: s.GetBoolNoErr("config_conn_allow"),
				RateLimit:       s.GetIntNoErr("rate_limit"),
				MaxConn:         s.GetIntNoErr("max_conn"),
				WebUserName:     webUsername,
				WebPassword:     webPassword,
				MaxTunnelNum:    s.GetIntNoErr("max_tunnel"),
				Flow: &file.Flow{
					ExportFlow: 0,
					InletFlow:  0,
					FlowLimit:  int64(s.GetIntNoErr("flow_limit")),
				},
				BlackIpList:      RemoveRepeatedElement(strings.Split(s.getEscapeString("blackiplist"), "\r\n")),
				WhiteIpList:      RemoveRepeatedElement(strings.Split(s.getEscapeString("whiteiplist"), "\r\n")),
				IpListPriority:   s.GetIntNoErr("iplistpriority"),
				IpWhite:          s.GetBoolNoErr("ipwhite"),
				IpWhitePass:      s.getEscapeString("ipwhitepass"),
				IpWhiteList:      RemoveRepeatedElement(strings.Split(s.getEscapeString("ipwhitelist"), "\r\n")),
				RegisterKey:      strings.TrimSpace(s.getEscapeString("register_key")),
				RegisterMaxHours: max(s.GetIntNoErr("register_max_hours"), 0),
				RegisteredOnly:   s.GetBoolNoErr("registered_only"),
				ExpireTime:       expireTime,
				CreateTime:       time.Now().Format("2006-01-02 15:04:05"),
			}
			t.SetSourceLists(t.IpListPriority, t.BlackIpList, t.WhiteIpList, acl.ScopeUser, "client")
		} else {
			userID, err := s.currentUserClientOwner()
			if err != nil {
				s.AjaxErr(err.Error())
				return
			}
			t = newUserOwnedClient(userID, userClientSettingsFromRequest(s), time.Now())
		}
		if err := validateSourceAccessLists(acl.ScopeUser, t.IpListPriority, t.BlackIpList, t.WhiteIpList); err != nil {
			s.AjaxErr("客户端 IP 名单无效: " + err.Error())
			return
		}
		if err := validateClientRegisterKey(t.Id, t.RegisterKey, t.VerifyKey); err != nil {
			s.AjaxErr(err.Error())
			return
		}
		if err := file.GetDb().NewClient(t); err != nil {
			s.AjaxErr(err.Error())
			return
		}
		s.auditMutation("client.create", "client", t.Id, t.UserId, "", nil, map[string]string{"remark": t.Remark})
		s.AjaxOkWithId("add success", t.Id)
	}
}
func (s *ClientController) GetClient() {
	if !s.RequirePost() {
		return
	}
	id := s.GetIntNoErr("id")
	data := make(map[string]interface{})
	if c, err := file.GetDb().GetClient(id); err != nil {
		data["code"] = 0
	} else {
		data["code"] = 1
		// Never serialize the mutable Client model directly here. It contains
		// WebPassword, IpWhitePass and basic-auth credentials. The list DTO is
		// deliberately limited to fields that are safe for an authenticated
		// management response and is also consistent with /client/list.
		rows := newClientListRows([]*file.Client{c})
		if len(rows) == 1 {
			data["data"] = rows[0]
		} else {
			data["code"] = 0
		}
	}
	s.Data["json"] = data
	s.ServeJSON()
}

// 修改客户端
func (s *ClientController) Edit() {
	id := s.GetIntNoErr("id")
	if s.Ctx.Request.Method == "GET" {
		s.Data["menu"] = "client"
		if c, err := file.GetDb().GetClient(id); err != nil {
			s.error()
			return
		} else {
			s.Data["c"] = c
			if s.IsAdmin() {
				s.Data["users"], _ = file.GetDb().GetUserList(0, 10000, "")
			}
			s.Data["BlackIpList"] = strings.Join(c.BlackIpList, "\r\n")
			s.Data["WhiteIpList"] = strings.Join(c.WhiteIpList, "\r\n")
			s.Data["IpWhiteList"] = strings.Join(c.IpWhiteList, "\r\n")
		}
		s.SetInfo("edit client")
		s.display()
	} else {
		if !s.RequirePost() {
			return
		}
		c, err := file.GetDb().GetClient(id)
		if err != nil || c == nil {
			s.AjaxErr("client ID not found")
			return
		}

		isAdmin := s.IsAdmin()
		if !isAdmin && !isAllowedClient(c.Id, s.GetAllowedClientIds()) {
			// Prepare performs the same check for normal browser requests. Keep
			// it here as a defense in depth for direct controller/API invocation.
			s.AjaxErr("permission denied")
			return
		}
		if isAdmin && !file.GetDb().VerifyVkey(s.getEscapeString("vkey"), c.Id) {
			s.AjaxErr("Vkey duplicate, please reset")
			return
		}

		before := snapshotClientForEdit(c)
		currentBasicPassword := ""
		if before.cnf != nil {
			currentBasicPassword = before.cnf.P
		}
		remark := s.getEscapeString("remark")
		cnfUser := s.getEscapeString("u")
		cnfPassword := keepClientBasicPassword(currentBasicPassword, s.getEscapeString("p"))
		compress := common.GetBoolByStr(s.getEscapeString("compress"))
		cryptEnabled := s.GetBoolNoErr("crypt")
		ipWhite := s.GetBoolNoErr("ipwhite")
		ipWhitePass := s.getEscapeString("ipwhitepass")
		ipWhiteList := RemoveRepeatedElement(strings.Split(s.getEscapeString("ipwhitelist"), "\r\n"))
		blackIPList := RemoveRepeatedElement(strings.Split(s.getEscapeString("blackiplist"), "\r\n"))
		whiteIPList := RemoveRepeatedElement(strings.Split(s.getEscapeString("whiteiplist"), "\r\n"))
		ipListPriority := s.GetIntNoErr("iplistpriority")
		registerKey := strings.TrimSpace(s.getEscapeString("register_key"))
		registerMaxHours := max(s.GetIntNoErr("register_max_hours"), 0)
		registeredOnly := s.GetBoolNoErr("registered_only")
		_ = s.Ctx.Request.ParseForm()
		if _, submitted := s.Ctx.Request.Form["blackiplist"]; !submitted {
			blackIPList = append([]string(nil), before.blackIPList...)
		}
		if _, submitted := s.Ctx.Request.Form["whiteiplist"]; !submitted {
			whiteIPList = append([]string(nil), before.whiteIPList...)
		}
		if _, submitted := s.Ctx.Request.Form["iplistpriority"]; !submitted {
			ipListPriority = before.ipListPriority
		}
		verifyKey := before.verifyKey
		if isAdmin {
			verifyKey = s.getEscapeString("vkey")
		} else {
			// Older user-facing forms did not submit registration controls. Keep
			// their existing values when an old client calls this endpoint.
			_ = s.Ctx.Request.ParseForm()
			if _, submitted := s.Ctx.Request.Form["register_key"]; !submitted {
				registerKey = before.registerKey
			}
			if _, submitted := s.Ctx.Request.Form["register_max_hours"]; !submitted {
				registerMaxHours = before.registerMaxHours
			}
			if _, submitted := s.Ctx.Request.Form["registered_only"]; !submitted {
				registeredOnly = before.registeredOnly
			}
		}

		selectedUserID := before.userID
		legacyUsername, legacyPassword, expireTime := "", "", before.expireTime
		if isAdmin {
			expireTime, err = normalizeUserExpireTime(s.GetString("expire_time"))
			if err != nil {
				s.AjaxErr("到期时间格式无效")
				return
			}
			legacyUsername, legacyPassword, err = mergeLegacyClientLogin(
				before.webUserName,
				before.webPassword,
				s.getEscapeString("web_username"),
				s.getEscapeString("web_password"),
				s.GetBoolNoErr("clear_legacy_web_login"),
			)
			if err != nil {
				s.AjaxErr(err.Error())
				return
			}
			if clientOwnerFieldSubmitted(s) {
				selectedUserID, err = requestedClientOwnerID(s)
				if err != nil {
					s.AjaxErr(err.Error())
					return
				}
				if err := file.GetDb().ValidateClientOwner(selectedUserID); err != nil {
					s.AjaxErr(err.Error())
					return
				}
			}
		}
		if err := file.GetDb().ValidateClientAssignment(&file.Client{
			Id:         c.Id,
			UserId:     selectedUserID,
			ExpireTime: expireTime,
		}, c.Id); err != nil {
			s.AjaxErr(err.Error())
			return
		}
		if err := validateSourceAccessLists(acl.ScopeUser, ipListPriority, blackIPList, whiteIPList); err != nil {
			s.AjaxErr("客户端 IP 名单无效: " + err.Error())
			return
		}
		if err := validateClientRegisterKey(c.Id, registerKey, verifyKey); err != nil {
			s.AjaxErr(err.Error())
			return
		}

		c.Lock()
		if c.Cnf == nil {
			c.Cnf = &file.Config{}
		}
		if c.Flow == nil {
			c.Flow = &file.Flow{}
		}
		if isAdmin {
			c.VerifyKey = s.getEscapeString("vkey")
			c.UserId = selectedUserID
			c.Flow.SetLimit(int64(s.GetIntNoErr("flow_limit")))
			c.RateLimit = s.GetIntNoErr("rate_limit")
			c.MaxConn = s.GetIntNoErr("max_conn")
			c.MaxTunnelNum = s.GetIntNoErr("max_tunnel")
			c.WebUserName = legacyUsername
			c.WebPassword = legacyPassword
			c.ExpireTime = expireTime
			c.ConfigConnAllow = s.GetBoolNoErr("config_conn_allow")
		}
		c.Remark = remark
		c.Cnf.U = cnfUser
		c.Cnf.P = cnfPassword
		c.Cnf.Compress = compress
		c.Cnf.Crypt = cryptEnabled
		c.IpWhite = ipWhite
		c.IpWhitePass = ipWhitePass
		c.IpWhiteList = ipWhiteList
		c.BlackIpList = blackIPList
		c.WhiteIpList = whiteIPList
		c.IpListPriority = ipListPriority
		c.RegisterKey = registerKey
		c.RegisterMaxHours = registerMaxHours
		c.RegisteredOnly = registeredOnly
		c.Unlock()
		c.SetSourceLists(ipListPriority, blackIPList, whiteIPList, acl.ScopeUser, "client")
		if err := file.GetDb().UpdateClient(c); err != nil {
			before.restore(c)
			s.AjaxErr(err.Error())
			return
		}
		c.RLock()
		ownerID, remark := c.UserId, c.Remark
		c.RUnlock()
		s.auditMutation("client.update", "client", id, ownerID, "", nil, map[string]string{"remark": remark})
		s.AjaxOk("save success")
	}
}

func RemoveRepeatedElement(arr []string) (newArr []string) {
	newArr = make([]string, 0)
	for i := 0; i < len(arr); i++ {
		// 过滤空IP
		if strings.TrimSpace(arr[i]) == "" {
			continue
		}
		repeat := false
		for j := i + 1; j < len(arr); j++ {
			if arr[i] == arr[j] {
				repeat = true
				break
			}
		}
		if !repeat {
			newArr = append(newArr, arr[i])
		}
	}
	return
}

// 支持的过期时间日期格式
var expireTimeFormats = []string{
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
	"2006-01-02",
	"2006/01/02 15:04:05",
	"2006/01/02 15:04",
	"2006/01/02",
	time.RFC3339,
}

// ParseExpireTime 尝试用多种格式解析过期时间字符串
func ParseExpireTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range expireTimeFormats {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// normalizeExpireTime 将用户输入的过期时间统一为 "2006-01-02 15:04:05" 格式
func normalizeExpireTime(s string) string {
	if t, ok := ParseExpireTime(s); ok {
		return t.Format("2006-01-02 15:04:05")
	}
	return ""
}

// 更改状态
func (s *ClientController) ChangeStatus() {
	if !s.RequirePost() {
		return
	}
	if !s.RequireAdmin() {
		return
	}
	id := s.GetIntNoErr("id")
	if client, err := file.GetDb().GetClient(id); err == nil {
		status := s.GetBoolNoErr("status")
		client.Lock()
		client.Status = status
		ownerID := client.UserId
		client.Unlock()
		file.GetDb().JsonDb.StoreClientsToJsonFile()
		if !status {
			server.DelClientConnect(client.Id)
		}
		s.auditMutation("client.status_change", "client", id, ownerID, "", nil, map[string]string{"status": strconv.FormatBool(status)})
		s.AjaxOk("modified success")
		return
	}
	s.AjaxErr("modified fail")
}

// 删除客户端
func (s *ClientController) Del() {
	if !s.RequirePost() {
		return
	}
	id := s.GetIntNoErr("id")
	if !s.RequireAdminOrClientOwner(id) {
		return
	}
	client, err := file.GetDb().GetClient(id)
	if err != nil || client == nil {
		s.AjaxErr("delete error")
		return
	}
	client.RLock()
	ownerID := client.UserId
	client.RUnlock()
	if s.IsAdmin() {
		err = file.GetDb().DelClient(id)
	} else {
		userID, ok := s.currentUserPrincipalID()
		if !ok {
			s.rejectRequest(http.StatusForbidden, "permission denied")
			return
		}
		err = file.GetDb().DelClientForUser(id, userID)
	}
	if err != nil {
		s.AjaxErr("delete error")
		return
	}
	server.DelTunnelAndHostByClientId(id, false)
	server.DelClientConnect(id)
	s.auditMutation("client.delete", "client", id, ownerID, "", nil, nil)
	s.AjaxOk("delete success")
}
