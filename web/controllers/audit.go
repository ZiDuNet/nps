package controllers

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"ehang.io/nps/lib/connectionlog"
	"ehang.io/nps/web/audit"
)

// AuditController exposes the control-plane journal and persistent connection
// history. Administrators see all records; ordinary users only see resources
// owned by their account.
type AuditController struct {
	BaseController
}

type auditListRow struct {
	ID       string
	Time     string
	Actor    string
	SourceIP string
	Action   string
	Resource string
	Result   string
	Error    string
	Details  string
}

type connectionListRow struct {
	ID          string
	Time        string
	Result      string
	Kind        string
	User        string
	Client      string
	Resource    string
	Transport   string
	SourceIP    string
	Target      string
	Duration    string
	Traffic     string
	ClientVer   string
	Error       string
	StartedAt   string
	EndedAt     string
	Action      string
	RawDuration int64
	BytesIn     int64
	BytesOut    int64
}

func (s *AuditController) List() {
	if s.Ctx.Request.Method == "GET" {
		s.Data["menu"] = "audit"
		s.Data["audit_initial_tab"] = "operations"
		s.SetInfo("audit")
		s.display("audit/list")
		return
	}
	start, length := s.GetAjaxParams()
	filter := audit.Filter{
		Offset: start, Limit: length,
		Search:       s.getEscapeString("search"),
		Action:       s.getEscapeString("action"),
		ResourceType: s.getEscapeString("resource_type"),
		Result:       s.getEscapeString("result"),
	}
	if !s.IsAdmin() {
		ownerID, ok := s.currentUserPrincipalID()
		if !ok {
			ownerID = -1
		}
		filter.OwnerUserID = &ownerID
	}
	page, err := audit.List(filter)
	if err != nil {
		s.AjaxErr("读取审计日志失败")
		return
	}
	rows := make([]*auditListRow, 0, len(page.Events))
	for _, event := range page.Events {
		if event == nil {
			continue
		}
		details := ""
		if len(event.Details) > 0 {
			if data, marshalErr := json.Marshal(event.Details); marshalErr == nil {
				details = string(data)
			}
		}
		actor := event.ActorName
		if actor == "" {
			actor = event.ActorType
		}
		resource := event.ResourceType
		if event.ResourceID > 0 {
			resource += " #" + strconv.Itoa(event.ResourceID)
		}
		rows = append(rows, &auditListRow{
			ID:       event.ID,
			Time:     event.Time.Local().Format("2006-01-02 15:04:05"),
			Actor:    actor,
			SourceIP: event.SourceIP,
			Action:   event.Action,
			Resource: resource,
			Result:   event.Result,
			Error:    event.Error,
			Details:  details,
		})
	}
	s.AjaxTable(rows, len(rows), page.Total, nil)
}

func (s *AuditController) Connections() {
	if s.Ctx.Request.Method == "GET" {
		s.Data["menu"] = "audit"
		s.Data["audit_initial_tab"] = "connections"
		s.SetInfo("connection log")
		s.display("audit/list")
		return
	}
	start, length := s.GetAjaxParams()
	filter := connectionlog.Filter{
		Offset:          start,
		Limit:           length,
		Search:          s.getEscapeString("search"),
		Kind:            s.getEscapeString("kind"),
		Action:          s.getEscapeString("action"),
		Result:          s.getEscapeString("result"),
		BridgeTransport: s.getEscapeString("bridge_transport"),
		ProxyProtocol:   s.getEscapeString("proxy_protocol"),
	}
	if !s.IsAdmin() {
		filter.AllowedClientIDs = s.GetAllowedClientIds()
	}
	page, err := connectionlog.List(filter)
	if err != nil {
		s.AjaxErr("读取客户端连接日志失败")
		return
	}
	rows := make([]*connectionListRow, 0, len(page.Events))
	for _, event := range page.Events {
		if event == nil {
			continue
		}
		user := strings.TrimSpace(event.UserName)
		if user == "" && event.UserID > 0 {
			user = "#" + strconv.Itoa(event.UserID)
		}
		client := strings.TrimSpace(event.ClientRemark)
		if client == "" && event.ClientID > 0 {
			client = "#" + strconv.Itoa(event.ClientID)
		} else if event.ClientID > 0 {
			client += " #" + strconv.Itoa(event.ClientID)
		}
		resource := event.ResourceType
		if event.ResourceID > 0 {
			resource += " #" + strconv.Itoa(event.ResourceID)
		}
		if event.ResourceRemark != "" {
			resource += " · " + event.ResourceRemark
		}
		rows = append(rows, &connectionListRow{
			ID:          event.ID,
			Time:        event.EndedAt.Local().Format("2006-01-02 15:04:05"),
			Result:      event.Result,
			Kind:        event.Kind,
			User:        user,
			Client:      client,
			Resource:    resource,
			Transport:   strings.TrimSpace(strings.Join(nonEmpty(event.BridgeTransport, event.ProxyProtocol), " / ")),
			SourceIP:    event.SourceIP,
			Target:      event.Target,
			Duration:    formatDuration(event.DurationMS),
			Traffic:     formatTraffic(event.BytesIn, event.BytesOut),
			ClientVer:   event.ClientVersion,
			Error:       event.Error,
			StartedAt:   event.StartedAt.Local().Format("2006-01-02 15:04:05"),
			EndedAt:     event.EndedAt.Local().Format("2006-01-02 15:04:05"),
			Action:      event.Action,
			RawDuration: event.DurationMS,
			BytesIn:     event.BytesIn,
			BytesOut:    event.BytesOut,
		})
	}
	s.AjaxTable(rows, len(rows), page.Total, nil)
}

func nonEmpty(values ...string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func formatDuration(milliseconds int64) string {
	if milliseconds <= 0 {
		return "-"
	}
	duration := time.Duration(milliseconds) * time.Millisecond
	if duration < time.Second {
		return duration.String()
	}
	return duration.Round(time.Millisecond).String()
}

func formatTraffic(in, out int64) string {
	if in <= 0 && out <= 0 {
		return "-"
	}
	return "↑ " + humanBytes(in) + " / ↓ " + humanBytes(out)
}

func humanBytes(value int64) string {
	if value < 1024 {
		return strconv.FormatInt(value, 10) + " B"
	}
	units := []string{"KB", "MB", "GB", "TB"}
	size := float64(value)
	for _, unit := range units {
		size /= 1024
		if size < 1024 || unit == units[len(units)-1] {
			return strconv.FormatFloat(size, 'f', 1, 64) + " " + unit
		}
	}
	return strconv.FormatInt(value, 10) + " B"
}
