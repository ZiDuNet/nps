package controllers

import (
	"html"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"ehang.io/nps/lib/file"
	"ehang.io/nps/server"
)

type liveClientSnapshot struct {
	ClientID        int   `json:"clientId"`
	UserID          int   `json:"userId"`
	Connections     int32 `json:"connections"`
	ConnectionLimit int   `json:"connectionLimit"`
	UploadRate      int64 `json:"uploadRate"`
	DownloadRate    int64 `json:"downloadRate"`
	Online          bool  `json:"online"`
}

type liveUserSnapshot struct {
	UserID          int    `json:"userId"`
	UserName        string `json:"userName"`
	Clients         int    `json:"clients"`
	Connections     int64  `json:"connections"`
	ConnectionLimit int    `json:"connectionLimit"`
	UploadLimit     int    `json:"uploadLimit"`
	DownloadLimit   int    `json:"downloadLimit"`
	UploadRate      int64  `json:"uploadRate"`
	DownloadRate    int64  `json:"downloadRate"`
}

type liveDataSnapshot struct {
	GeneratedAt             string               `json:"generatedAt"`
	ProxyInRate             int64                `json:"proxyInRate"`
	ProxyOutRate            int64                `json:"proxyOutRate"`
	CurrentProxyConnections int64                `json:"currentProxyConnections"`
	Clients                 []liveClientSnapshot `json:"clients"`
	Users                   []liveUserSnapshot   `json:"users"`
	CurrentUser             *liveUserSnapshot    `json:"currentUser,omitempty"`
	CurrentClient           *liveClientSnapshot  `json:"currentClient,omitempty"`
}

// LiveData serves the frequent, intentionally small dashboard refresh used by
// visible counters. It never samples host information or rebuilds resource
// tables, and every row remains constrained by the current principal scope.
func (s *IndexController) LiveData() {
	query := s.Ctx.Request.URL.Query()
	clientIDs := parseLiveIDSet(query["client_ids"])
	userIDs := parseLiveIDSet(query["user_ids"])
	summaryOnly := strings.EqualFold(strings.TrimSpace(query.Get("summary_only")), "1") ||
		strings.EqualFold(strings.TrimSpace(query.Get("summary_only")), "true")
	includeAll := !summaryOnly && len(clientIDs) == 0 && len(userIDs) == 0
	s.Data["json"] = map[string]interface{}{
		"status": 1,
		"data":   s.liveDataSnapshot(clientIDs, userIDs, includeAll),
	}
	s.ServeJSON()
	s.StopRun()
}

func parseLiveIDSet(values []string) map[int]struct{} {
	ids := make(map[int]struct{})
	for _, value := range values {
		for _, raw := range strings.Split(value, ",") {
			id, err := strconv.Atoi(strings.TrimSpace(raw))
			if err != nil || id <= 0 {
				continue
			}
			ids[id] = struct{}{}
			if len(ids) >= 5000 {
				return ids
			}
		}
	}
	return ids
}

func (s *BaseController) liveDataSnapshot(clientIDs, userIDs map[int]struct{}, includeAll bool) liveDataSnapshot {
	if clientIDs == nil {
		clientIDs = make(map[int]struct{})
	}
	if userIDs == nil {
		userIDs = make(map[int]struct{})
	}
	snapshot := liveDataSnapshot{
		GeneratedAt: time.Now().Format(time.RFC3339),
		Clients:     make([]liveClientSnapshot, 0),
		Users:       make([]liveUserSnapshot, 0),
	}
	var allowed map[int]struct{}
	if !s.IsAdmin() {
		allowed = s.GetAllowedClientIds()
	}
	allClients := liveClientSnapshots(allowed)
	clientByID := make(map[int]liveClientSnapshot, len(allClients))
	clientsPerUser := make(map[int]int)
	for _, client := range allClients {
		clientByID[client.ClientID] = client
		snapshot.ProxyInRate += client.UploadRate
		snapshot.ProxyOutRate += client.DownloadRate
		snapshot.CurrentProxyConnections += int64(client.Connections)
		if client.UserID > 0 {
			clientsPerUser[client.UserID]++
		}
		if includeAll || liveIDRequested(clientIDs, client.ClientID) {
			snapshot.Clients = append(snapshot.Clients, client)
		}
	}

	currentUserID, currentClientID := s.currentLivePrincipalIDs()
	if includeAll && s.IsAdmin() {
		file.GetDb().JsonDb.Users.Range(func(key, value interface{}) bool {
			if userID, ok := key.(int); ok && userID > 0 {
				userIDs[userID] = struct{}{}
			}
			return true
		})
	}
	if currentUserID > 0 {
		userIDs[currentUserID] = struct{}{}
	}
	for _, client := range snapshot.Clients {
		if client.UserID > 0 && (includeAll || liveIDRequested(userIDs, client.UserID)) {
			userIDs[client.UserID] = struct{}{}
		}
	}
	userIDList := make([]int, 0, len(userIDs))
	for userID := range userIDs {
		if userID > 0 {
			userIDList = append(userIDList, userID)
		}
	}
	sort.Ints(userIDList)
	for _, userID := range userIDList {
		user, err := file.GetDb().GetUser(userID)
		if err != nil || user == nil {
			continue
		}
		user.RLock()
		row := liveUserSnapshot{
			UserID:          userID,
			UserName:        html.UnescapeString(user.UserName),
			ConnectionLimit: user.MaxConn,
			UploadLimit:     user.UploadLimit,
			DownloadLimit:   user.DownloadLimit,
			Clients:         clientsPerUser[userID],
		}
		user.RUnlock()
		var runtimeLimit int64
		row.Connections, runtimeLimit, row.UploadRate, row.DownloadRate = file.UserRuntimeSnapshot(userID)
		if runtimeLimit > 0 {
			row.ConnectionLimit = int(runtimeLimit)
		}
		snapshot.Users = append(snapshot.Users, row)
	}

	if currentUserID > 0 {
		for index := range snapshot.Users {
			if snapshot.Users[index].UserID == currentUserID {
				current := snapshot.Users[index]
				snapshot.CurrentUser = &current
				break
			}
		}
	}
	if currentClientID > 0 {
		if current, ok := clientByID[currentClientID]; ok {
			snapshot.CurrentClient = &current
		}
	}
	return snapshot
}

func liveIDRequested(ids map[int]struct{}, id int) bool {
	_, ok := ids[id]
	return ok
}

func liveClientSnapshots(allowed map[int]struct{}) []liveClientSnapshot {
	keys := file.GetMapKeys(&file.GetDb().JsonDb.Clients, true, "", "")
	rows := make([]liveClientSnapshot, 0, len(keys))
	for _, clientID := range keys {
		value, ok := file.GetDb().JsonDb.Clients.Load(clientID)
		if !ok {
			continue
		}
		client, ok := value.(*file.Client)
		if !ok || client == nil {
			continue
		}
		client.RLock()
		if client.NoDisplay {
			client.RUnlock()
			continue
		}
		id, userID := client.Id, client.UserId
		if allowed != nil && !liveIDRequested(allowed, id) {
			client.RUnlock()
			continue
		}
		row := liveClientSnapshot{
			ClientID:        id,
			UserID:          userID,
			Connections:     atomic.LoadInt32(&client.NowConn),
			ConnectionLimit: client.MaxConn,
			Online:          client.IsConnect,
		}
		flow := client.Flow
		client.RUnlock()
		if row.Connections < 0 {
			row.Connections = 0
		}
		if flow != nil {
			row.UploadRate, row.DownloadRate = flow.RateSnapshot()
		}
		if server.Bridge != nil {
			row.Online = len(server.Bridge.ActiveClientTransports(id)) > 0
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].ClientID < rows[j].ClientID })
	return rows
}

func (s *BaseController) currentLivePrincipalIDs() (userID, clientID int) {
	if id, ok := s.currentUserPrincipalID(); ok {
		userID = id
	}
	principal, _ := s.GetSession(sessionPrincipalKey).(string)
	if principal == sessionPrincipalClient {
		clientID, _ = sessionInt(s.GetSession("clientId"))
	}
	return userID, clientID
}
