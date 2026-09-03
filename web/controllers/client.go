package controllers

import (
	"errors"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/file"
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
	BlackIpList         []string
	CreateTime          string
	LastOnlineTime      string
	IpWhite             bool
	IpWhiteList         []string
	ExpireTime          string
	UserExpireTime      string
	EffectiveExpireTime string
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
	verifyKey       string
	userID          int
	remark          string
	rateLimit       int
	flow            *file.Flow
	flowLimit       int64
	maxConn         int
	configConnAllow bool
	maxTunnelNum    int
	webUserName     string
	webPassword     string
	cnf             *file.Config
	blackIPList     []string
	ipWhite         bool
	ipWhitePass     string
	ipWhiteList     []string
	expireTime      string
}

func snapshotClientForEdit(client *file.Client) clientEditSnapshot {
	client.RLock()
	snapshot := clientEditSnapshot{
		verifyKey:       client.VerifyKey,
		userID:          client.UserId,
		remark:          client.Remark,
		rateLimit:       client.RateLimit,
		flow:            client.Flow,
		maxConn:         client.MaxConn,
		configConnAllow: client.ConfigConnAllow,
		maxTunnelNum:    client.MaxTunnelNum,
		webUserName:     client.WebUserName,
		webPassword:     client.WebPassword,
		blackIPList:     append([]string(nil), client.BlackIpList...),
		ipWhite:         client.IpWhite,
		ipWhitePass:     client.IpWhitePass,
		ipWhiteList:     append([]string(nil), client.IpWhiteList...),
		expireTime:      client.ExpireTime,
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
	client.IpWhite = snapshot.ipWhite
	client.IpWhitePass = snapshot.ipWhitePass
	client.IpWhiteList = append([]string(nil), snapshot.ipWhiteList...)
	client.ExpireTime = snapshot.expireTime
	client.Unlock()
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
	Remark      string
	BasicUser   string
	BasicPass   string
	Compress    bool
	Crypt       bool
	IpWhite     bool
	IpWhitePass string
	IpWhiteList []string
	BlackIPList []string
}

func userClientSettingsFromRequest(s *ClientController) userClientSettings {
	return userClientSettings{
		Remark:      s.getEscapeString("remark"),
		BasicUser:   s.getEscapeString("u"),
		BasicPass:   s.getEscapeString("p"),
		Compress:    common.GetBoolByStr(s.getEscapeString("compress")),
		Crypt:       s.GetBoolNoErr("crypt"),
		IpWhite:     s.GetBoolNoErr("ipwhite"),
		IpWhitePass: s.getEscapeString("ipwhitepass"),
		IpWhiteList: RemoveRepeatedElement(strings.Split(s.getEscapeString("ipwhitelist"), "\r\n")),
		BlackIPList: RemoveRepeatedElement(strings.Split(s.getEscapeString("blackiplist"), "\r\n")),
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
		row := &clientListRow{
			Id:              client.Id,
			UserId:          userID,
			UserName:        userName,
			VerifyKey:       client.VerifyKey,
			Addr:            client.Addr,
			LocalAddr:       client.LocalAddr,
			Remark:          client.Remark,
			Status:          client.Status,
			IsConnect:       client.IsConnect,
			RateLimit:       client.RateLimit,
			NoStore:         client.NoStore,
			MaxConn:         client.MaxConn,
			NowConn:         atomic.LoadInt32(&client.NowConn),
			ConfigConnAllow: client.ConfigConnAllow,
			MaxTunnelNum:    client.MaxTunnelNum,
			Version:         client.Version,
			BlackIpList:     append([]string(nil), client.BlackIpList...),
			CreateTime:      client.CreateTime,
			LastOnlineTime:  client.LastOnlineTime,
			IpWhite:         client.IpWhite,
			IpWhiteList:     append([]string(nil), client.IpWhiteList...),
			ExpireTime:      client.ExpireTime,
		}
		client.RUnlock()
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
	ip := s.Ctx.Request.Host
	cmd["ip"] = common.GetIpByAddr(ip)
	cmd["bridgeType"] = beego.AppConfig.String("bridge_type")
	cmd["bridgePort"] = server.Bridge.TunnelPort
	s.AjaxTable(newClientListRows(list), cnt, cnt, cmd)
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
				BlackIpList: RemoveRepeatedElement(strings.Split(s.getEscapeString("blackiplist"), "\r\n")),
				IpWhite:     s.GetBoolNoErr("ipwhite"),
				IpWhitePass: s.getEscapeString("ipwhitepass"),
				IpWhiteList: RemoveRepeatedElement(strings.Split(s.getEscapeString("ipwhitelist"), "\r\n")),
				ExpireTime:  expireTime,
				CreateTime:  time.Now().Format("2006-01-02 15:04:05"),
			}
		} else {
			userID, err := s.currentUserClientOwner()
			if err != nil {
				s.AjaxErr(err.Error())
				return
			}
			t = newUserOwnedClient(userID, userClientSettingsFromRequest(s), time.Now())
		}
		if err := file.GetDb().NewClient(t); err != nil {
			s.AjaxErr(err.Error())
			return
		}
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
		c.Unlock()
		if err := file.GetDb().UpdateClient(c); err != nil {
			before.restore(c)
			s.AjaxErr(err.Error())
			return
		}
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
		client.Unlock()
		file.GetDb().JsonDb.StoreClientsToJsonFile()
		if !status {
			server.DelClientConnect(client.Id)
		}
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
	if !s.RequireAdmin() {
		return
	}
	id := s.GetIntNoErr("id")
	if err := file.GetDb().DelClient(id); err != nil {
		s.AjaxErr("delete error")
	}
	server.DelTunnelAndHostByClientId(id, false)
	server.DelClientConnect(id)
	s.AjaxOk("delete success")
}
