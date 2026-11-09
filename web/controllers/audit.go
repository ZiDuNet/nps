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
	ID              string
	Time            string
	Actor           string
	ActorType       string
	ActorID         int
	OwnerUserID     int
	SourceIP        string
	Action          string
	Resource        string
	ResourceType    string
	ResourceID      int
	Result          string
	Error           string
	Details         string
	Method          string
	Path            string
	RemoteAddr      string
	IPSource        string
	RequestID       string
	UserAgent       string
	AuthMethod      string
	Stage           string
	ErrorCode       string
	ActionLabel     string
	ActionLabelEN   string
	ResourceLabel   string
	ResourceLabelEN string
	ErrorLabel      string
	ErrorLabelEN    string
}

type connectionListRow struct {
	ID                        string
	Time                      string
	Result                    string
	Kind                      string
	User                      string
	UserID                    int
	Client                    string
	ClientID                  int
	Resource                  string
	ResourceType              string
	ResourceID                int
	Transport                 string
	BridgeChannel             string
	HandshakeStage            string
	SourceIP                  string
	RemoteAddr                string
	ServerAddr                string
	ClientPublicIP            string
	ClientLocalAddr           string
	ClientLocalAddrSource     string
	Target                    string
	Duration                  string
	Traffic                   string
	ClientVer                 string
	ClientStatus              string
	ClientExpireTime          string
	ClientEffectiveExpireTime string
	ClientLastOnlineTime      string
	UserStatus                string
	UserExpireTime            string
	Error                     string
	StartedAt                 string
	EndedAt                   string
	Action                    string
	RawDuration               int64
	BytesIn                   int64
	BytesOut                  int64
	ActionLabel               string
	ActionLabelEN             string
	ResourceLabel             string
	ResourceLabelEN           string
}

func (s *AuditController) List() {
	if s.Ctx.Request.Method == "GET" {
		s.Data["menu"] = "audit"
		s.Data["audit_initial_tab"] = "operations"
		s.auditPageOptions()
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
			ID:           event.ID,
			Time:         event.Time.Local().Format("2006-01-02 15:04:05"),
			Actor:        actor,
			ActorType:    event.ActorType,
			ActorID:      event.ActorID,
			OwnerUserID:  event.OwnerUserID,
			SourceIP:     event.SourceIP,
			Action:       event.Action,
			Resource:     resource,
			ResourceType: event.ResourceType,
			ResourceID:   event.ResourceID,
			Result:       event.Result,
			Error:        event.Error,
			Details:      details,
			Method:       event.Method, Path: event.Path, RemoteAddr: event.RemoteAddr, IPSource: event.IPSource,
			RequestID: event.RequestID, UserAgent: event.UserAgent, AuthMethod: event.AuthMethod,
			Stage: event.Stage, ErrorCode: event.ErrorCode,
			ActionLabel: audit.ActionName(event.Action, false), ActionLabelEN: audit.ActionName(event.Action, true),
			ResourceLabel:   strings.Replace(resource, event.ResourceType, audit.ResourceName(event.ResourceType, false), 1),
			ResourceLabelEN: strings.Replace(resource, event.ResourceType, audit.ResourceName(event.ResourceType, true), 1),
			ErrorLabel:      auditErrorLabel(event, false), ErrorLabelEN: auditErrorLabel(event, true),
		})
	}
	s.AjaxTable(rows, len(rows), page.Total, s.auditLogStatus(audit.Path()))
}

func (s *AuditController) Connections() {
	if s.Ctx.Request.Method == "GET" {
		s.Data["menu"] = "audit"
		s.Data["audit_initial_tab"] = "connections"
		s.auditPageOptions()
		s.SetInfo("connection log")
		s.display("audit/list")
		return
	}
	start, length := s.GetAjaxParams()
	filter := connectionlog.Filter{
		Offset:          start,
		Limit:           length,
		Search:          connectionAuditSearch(s.getEscapeString("search")),
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
			ID:                        event.ID,
			Time:                      event.EndedAt.Local().Format("2006-01-02 15:04:05"),
			Result:                    event.Result,
			Kind:                      event.Kind,
			User:                      user,
			UserID:                    event.UserID,
			Client:                    client,
			ClientID:                  event.ClientID,
			Resource:                  resource,
			ResourceType:              event.ResourceType,
			ResourceID:                event.ResourceID,
			Transport:                 strings.TrimSpace(strings.Join(nonEmpty(event.BridgeTransport, event.ProxyProtocol), " / ")),
			BridgeChannel:             event.BridgeChannel,
			HandshakeStage:            event.HandshakeStage,
			SourceIP:                  event.SourceIP,
			RemoteAddr:                event.RemoteAddr,
			ServerAddr:                event.ServerAddr,
			ClientPublicIP:            event.ClientPublicIP,
			ClientLocalAddr:           event.ClientLocalAddr,
			ClientLocalAddrSource:     event.ClientLocalAddrSource,
			Target:                    event.Target,
			Duration:                  formatDuration(event.DurationMS),
			Traffic:                   formatTraffic(event.BytesIn, event.BytesOut),
			ClientVer:                 event.ClientVersion,
			ClientStatus:              event.ClientStatus,
			ClientExpireTime:          event.ClientExpireTime,
			ClientEffectiveExpireTime: event.ClientEffectiveExpireTime,
			ClientLastOnlineTime:      event.ClientLastOnlineTime,
			UserStatus:                event.UserStatus,
			UserExpireTime:            event.UserExpireTime,
			Error:                     event.Error,
			StartedAt:                 event.StartedAt.Local().Format("2006-01-02 15:04:05"),
			EndedAt:                   event.EndedAt.Local().Format("2006-01-02 15:04:05"),
			Action:                    event.Action,
			RawDuration:               event.DurationMS,
			BytesIn:                   event.BytesIn,
			BytesOut:                  event.BytesOut,
			ActionLabel:               audit.ActionName(event.Action, false), ActionLabelEN: audit.ActionName(event.Action, true),
			ResourceLabel:   strings.Replace(resource, event.ResourceType, audit.ResourceName(event.ResourceType, false), 1),
			ResourceLabelEN: strings.Replace(resource, event.ResourceType, audit.ResourceName(event.ResourceType, true), 1),
		})
	}
	status := s.auditLogStatus(connectionlog.Path())
	if s.IsAdmin() {
		status["dropped"] = connectionlog.Dropped()
	}
	s.AjaxTable(rows, len(rows), page.Total, status)
}

func (s *AuditController) auditPageOptions() {
	s.Data["audit_action_groups"] = audit.ActionGroups(false)
	s.Data["connection_action_groups"] = audit.ActionGroups(true)
}

func (s *AuditController) auditLogStatus(path string) map[string]interface{} {
	status := map[string]interface{}{"log_enabled": path != ""}
	// Filesystem layout is diagnostic information for administrators only.
	if s.IsAdmin() {
		status["log_path"] = path
	}
	return status
}

func auditErrorLabel(event *audit.Event, english bool) string {
	if event.Error == "username or password incorrect" {
		return audit.FailureName("invalid_credentials", english)
	}
	if event.ErrorCode != "" {
		if label := audit.FailureName(event.ErrorCode, english); label != event.ErrorCode {
			return label
		}
	}
	return event.Error
}

func connectionAuditSearch(search string) string {
	for _, group := range audit.ActionGroups(true) {
		for _, action := range group.Actions {
			if search == action.Name || strings.EqualFold(search, action.NameEN) {
				return action.Code
			}
		}
	}
	return search
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
	return humanBytes(in) + " / " + humanBytes(out)
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
