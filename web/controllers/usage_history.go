package controllers

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	usagehistory "ehang.io/nps/server/usage"
)

type usageHistoryPayload struct {
	GeneratedAt     string                  `json:"generated_at"`
	Scope           string                  `json:"scope"`
	Bucket          string                  `json:"bucket"`
	IntervalSeconds int64                   `json:"interval_seconds"`
	From            string                  `json:"from"`
	To              string                  `json:"to"`
	Resources       []usagehistory.Resource `json:"resources"`
}

func parseUsageHistoryQuery(values url.Values, now time.Time) (usagehistory.Query, string, error) {
	if values == nil {
		values = make(url.Values)
	}
	scope := strings.ToLower(strings.TrimSpace(values.Get("scope")))
	switch scope {
	case "", usagehistory.ScopeGlobal:
		scope = usagehistory.ScopeGlobal
	case usagehistory.ScopeUser, usagehistory.ScopeClient, usagehistory.ScopeTunnel, usagehistory.ScopeHost:
	default:
		return usagehistory.Query{}, "", errors.New("scope 必须是 global、user、client、tunnel 或 host")
	}

	id, err := usageHistoryNonNegativeInt(values.Get("id"))
	if err != nil {
		return usagehistory.Query{}, "", errors.New("id 必须是正整数")
	}
	userID, err := usageHistoryNonNegativeInt(values.Get("user_id"))
	if err != nil {
		return usagehistory.Query{}, "", errors.New("user_id 必须是正整数")
	}
	clientID, err := usageHistoryNonNegativeInt(values.Get("client_id"))
	if err != nil {
		return usagehistory.Query{}, "", errors.New("client_id 必须是正整数")
	}
	if id > 0 {
		switch scope {
		case usagehistory.ScopeUser:
			userID = id
		case usagehistory.ScopeClient:
			clientID = id
		}
	}

	to, err := parseUsageHistoryTime(values.Get("to"), now)
	if err != nil {
		return usagehistory.Query{}, "", errors.New("to 必须是 RFC3339 时间或 Unix 秒")
	}
	if to.IsZero() {
		to = now
	}
	from, err := parseUsageHistoryTime(values.Get("from"), now)
	if err != nil {
		return usagehistory.Query{}, "", errors.New("from 必须是 RFC3339 时间或 Unix 秒")
	}
	if from.IsZero() {
		from = to.Add(-24 * time.Hour)
	}
	if !from.Before(to) {
		return usagehistory.Query{}, "", errors.New("from 必须早于 to")
	}

	bucket := strings.ToLower(strings.TrimSpace(values.Get("bucket")))
	switch bucket {
	case "":
		if to.Sub(from) > 48*time.Hour {
			bucket = "5m"
		} else {
			bucket = "1m"
		}
	case "1m", "5m":
	default:
		return usagehistory.Query{}, "", errors.New("bucket 必须是 1m 或 5m")
	}
	return usagehistory.Query{
		Scope:  scope,
		ID:     id,
		UserID: userID,
		Client: clientID,
		From:   from,
		To:     to,
	}, bucket, nil
}

func usageHistoryNonNegativeInt(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0, errors.New("invalid non-negative integer")
	}
	return value, nil
}

func parseUsageHistoryTime(raw string, now time.Time) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}
	if value, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return time.Unix(value, 0), nil
	}
	value, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, err
	}
	return value, nil
}

func scopeUsageHistoryResources(resources []usagehistory.Resource, isAdmin bool, userID, clientID int, allowed map[int]struct{}) []usagehistory.Resource {
	if isAdmin {
		return resources
	}
	result := make([]usagehistory.Resource, 0)
	for _, resource := range resources {
		switch {
		case userID > 0:
			if resource.Scope == usagehistory.ScopeUser && resource.ID == userID {
				result = append(result, resource)
				continue
			}
			if resource.Scope != usagehistory.ScopeGlobal && isAllowedClient(resource.ClientID, allowed) {
				result = append(result, resource)
			}
		case clientID > 0:
			if !isAllowedClient(clientID, allowed) {
				continue
			}
			if resource.Scope == usagehistory.ScopeClient && resource.ClientID == clientID {
				result = append(result, resource)
				continue
			}
			if resource.ClientID == clientID {
				result = append(result, resource)
			}
		}
	}
	return result
}

func (s *BaseController) usageHistorySnapshot() (usageHistoryPayload, error) {
	now := time.Now()
	query, bucket, err := parseUsageHistoryQuery(s.Ctx.Request.URL.Query(), now)
	if err != nil {
		return usageHistoryPayload{}, err
	}
	history := usagehistory.Default()
	if history == nil {
		return usageHistoryPayload{}, errors.New("历史用量采集尚未启动")
	}

	isAdmin := s.IsAdmin()
	userID, _ := s.currentUserPrincipalID()
	clientID := 0
	principal, _ := s.GetSession(sessionPrincipalKey).(string)
	if principal == sessionPrincipalClient {
		clientID, _ = sessionInt(s.GetSession("clientId"))
	}
	if !isAdmin {
		switch {
		case userID > 0 && query.Scope == usagehistory.ScopeGlobal:
			query.Scope, query.UserID, query.ID, query.Client = usagehistory.ScopeUser, userID, userID, 0
		case userID > 0 && query.Scope == usagehistory.ScopeUser:
			query.UserID, query.ID = userID, userID
		case clientID > 0 && query.Scope == usagehistory.ScopeGlobal:
			query.Scope, query.Client, query.ID = usagehistory.ScopeClient, clientID, clientID
		}
	}

	resources, interval := history.Series(query, bucket)
	allowed := map[int]struct{}(nil)
	if !isAdmin {
		allowed = s.GetAllowedClientIds()
	}
	resources = scopeUsageHistoryResources(resources, isAdmin, userID, clientID, allowed)
	return usageHistoryPayload{
		GeneratedAt:     now.Format(time.RFC3339),
		Scope:           query.Scope,
		Bucket:          bucket,
		IntervalSeconds: interval,
		From:            query.From.Format(time.RFC3339),
		To:              query.To.Format(time.RFC3339),
		Resources:       resources,
	}, nil
}

func (s *ApiV1Controller) StatusHistory() {
	payload, err := s.usageHistorySnapshot()
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "尚未启动") {
			status = http.StatusServiceUnavailable
		}
		s.apiFail(status, "usage_history_failed", err.Error())
		return
	}
	s.apiData(payload)
}

func (s *IndexController) UsageHistory() {
	payload, err := s.usageHistorySnapshot()
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "尚未启动") {
			status = http.StatusServiceUnavailable
		}
		s.Ctx.Output.SetStatus(status)
		s.Data["json"] = map[string]interface{}{"status": 0, "msg": err.Error()}
		s.ServeJSON()
		s.StopRun()
		return
	}
	s.Data["json"] = map[string]interface{}{"status": 1, "data": payload}
	s.ServeJSON()
	s.StopRun()
}
