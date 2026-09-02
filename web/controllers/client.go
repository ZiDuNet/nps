package controllers

import (
	"errors"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/file"
	"ehang.io/nps/lib/rate"
	"ehang.io/nps/server"
	"github.com/astaxie/beego"
)

type ClientController struct {
	BaseController
}

type clientListRow struct {
	Id              int
	UserName        string
	VerifyKey       string
	Addr            string
	LocalAddr       string
	Remark          string
	Status          bool
	IsConnect       bool
	RateLimit       int
	Flow            clientListFlow
	Rate            clientListRate
	NoStore         bool
	MaxConn         int
	NowConn         int32
	ConfigConnAllow bool
	MaxTunnelNum    int
	Version         string
	BlackIpList     []string
	CreateTime      string
	LastOnlineTime  string
	IpWhite         bool
	IpWhiteList     []string
	ExpireTime      string
}

type clientListFlow struct {
	ExportFlow int64
	InletFlow  int64
	FlowLimit  int64
}

type clientListRate struct {
	NowRate int64
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
				row.UserName = user.UserName
				user.RUnlock()
			}
		}
		row.Flow.InletFlow, row.Flow.ExportFlow, row.Flow.FlowLimit = flow.Snapshot()
		row.Rate.NowRate = clientRate.CurrentRate()
		rows = append(rows, row)
	}
	return rows
}

func (s *ClientController) List() {
	if s.Ctx.Request.Method == "GET" {
		s.Data["menu"] = "client"
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
		list, cnt = server.GetClientList(start, length, search, sort, order, clientId)
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
		s.Data["users"], _ = file.GetDb().GetUserList(0, 10000, "")
		s.display()
	} else {
		if !s.RequirePost() {
			return
		}
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
		id := int(file.GetDb().JsonDb.GetClientId())
		t := &file.Client{
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
			ExpireTime:  normalizeExpireTime(s.getEscapeString("expire_time")),
			CreateTime:  time.Now().Format("2006-01-02 15:04:05"),
		}
		if err := file.GetDb().NewClient(t); err != nil {
			s.AjaxErr(err.Error())
			return
		}
		s.AjaxOkWithId("add success", id)
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
			s.Data["users"], _ = file.GetDb().GetUserList(0, 10000, "")
			s.Data["BlackIpList"] = strings.Join(c.BlackIpList, "\r\n")
			s.Data["IpWhiteList"] = strings.Join(c.IpWhiteList, "\r\n")
		}
		s.SetInfo("edit client")
		s.display()
	} else {
		if !s.RequirePost() {
			return
		}
		if c, err := file.GetDb().GetClient(id); err != nil {
			s.error()
			s.AjaxErr("client ID not found")
			return
		} else {
			if s.IsAdmin() {
				if !file.GetDb().VerifyVkey(s.getEscapeString("vkey"), c.Id) {
					s.AjaxErr("Vkey duplicate, please reset")
					return
				}
			}
			isAdmin := s.IsAdmin()
			remark := s.getEscapeString("remark")
			submittedUsername := s.getEscapeString("web_username")
			submittedPassword := s.getEscapeString("web_password")
			cnfUser := s.getEscapeString("u")
			cnfPassword := s.getEscapeString("p")
			compress := common.GetBoolByStr(s.getEscapeString("compress"))
			cryptEnabled := s.GetBoolNoErr("crypt")
			configConnAllow := s.GetBoolNoErr("config_conn_allow")
			ipWhite := s.GetBoolNoErr("ipwhite")
			ipWhitePass := s.getEscapeString("ipwhitepass")
			ipWhiteList := RemoveRepeatedElement(strings.Split(s.getEscapeString("ipwhitelist"), "\r\n"))
			blackIPList := RemoveRepeatedElement(strings.Split(s.getEscapeString("blackiplist"), "\r\n"))
			expireTime := normalizeExpireTime(s.getEscapeString("expire_time"))
			b, err := beego.AppConfig.Bool("allow_user_change_username")
			canChangeLegacyUsername := isAdmin || (err == nil && b)
			c.RLock()
			currentWebUsername, currentWebPassword := c.WebUserName, c.WebPassword
			c.RUnlock()
			if !canChangeLegacyUsername {
				submittedUsername = ""
			}
			legacyUsername, legacyPassword, legacyErr := mergeLegacyClientLogin(
				currentWebUsername,
				currentWebPassword,
				submittedUsername,
				submittedPassword,
				s.GetBoolNoErr("clear_legacy_web_login") && canChangeLegacyUsername,
			)
			if legacyErr != nil {
				s.AjaxErr(legacyErr.Error())
				return
			}
			selectedUserID := 0
			if isAdmin {
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
				} else {
					c.RLock()
					selectedUserID = c.UserId
					c.RUnlock()
				}
			}
			oldRate := (*rate.Rate)(nil)
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
			}
			c.Remark = remark
			c.Cnf.U = cnfUser
			c.Cnf.P = cnfPassword
			c.Cnf.Compress = compress
			c.Cnf.Crypt = cryptEnabled
			c.WebUserName = legacyUsername
			c.WebPassword = legacyPassword
			c.ConfigConnAllow = configConnAllow
			c.IpWhite = ipWhite
			c.IpWhitePass = ipWhitePass
			c.IpWhiteList = ipWhiteList
			c.BlackIpList = blackIPList
			c.ExpireTime = expireTime
			oldRate = c.Rate
			if c.RateLimit > 0 {
				c.Rate = rate.NewRate(int64(c.RateLimit) * 1024)
			} else {
				c.Rate = rate.NewRate(int64(2<<23) * 1024)
			}
			newRate := c.Rate
			c.Unlock()
			if oldRate != nil {
				oldRate.Stop()
			}
			newRate.Start()
			file.GetDb().JsonDb.StoreClientsToJsonFile()
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
