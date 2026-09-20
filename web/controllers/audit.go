package controllers

import (
	"encoding/json"
	"strconv"

	"ehang.io/nps/web/audit"
)

// AuditController exposes the administrator's control-plane journal. Normal
// users are deliberately denied: their own activity is still recorded for
// administrators, but the journal is an operational security surface.
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

func (s *AuditController) List() {
	if !s.RequireAdmin() {
		return
	}
	if s.Ctx.Request.Method == "GET" {
		s.Data["menu"] = "audit"
		s.SetInfo("audit")
		s.display("audit/list")
		return
	}
	start, length := s.GetAjaxParams()
	page, err := audit.List(audit.Filter{
		Offset: start, Limit: length,
		Search:       s.getEscapeString("search"),
		Action:       s.getEscapeString("action"),
		ResourceType: s.getEscapeString("resource_type"),
		Result:       s.getEscapeString("result"),
	})
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
