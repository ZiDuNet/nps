package controllers

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"ehang.io/nps/lib/file"
	"ehang.io/nps/lib/rate"
	"github.com/astaxie/beego"
)

// UsersRegister is the administrator-only API registration endpoint. Unlike
// public self-registration, it remains available while allow_user_register is
// off and lets the administrator explicitly choose quotas and expiry. Omitted
// fields fall back to the same registration defaults used by the public form.
func (s *ApiV1Controller) UsersRegister() {
	if !s.requireAdmin() {
		return
	}
	if s.Ctx == nil || s.Ctx.Request == nil {
		s.apiFail(http.StatusBadRequest, "invalid_request", "请求无效")
		return
	}
	if err := s.Ctx.Request.ParseForm(); err != nil {
		s.apiFail(http.StatusBadRequest, "invalid_form", "请求表单无效")
		return
	}

	username := strings.TrimSpace(s.GetString("username"))
	password := s.GetString("password")
	emailAddress := strings.TrimSpace(s.GetString("email"))
	remark := strings.TrimSpace(s.GetString("remark"))
	if username == "" || len(username) > 64 {
		s.apiFail(http.StatusBadRequest, "validation_error", "用户名不能为空且不能超过 64 个字符")
		return
	}
	if len(password) < 6 {
		s.apiFail(http.StatusBadRequest, "validation_error", "密码不能少于 6 个字符")
		return
	}
	if strings.EqualFold(username, strings.TrimSpace(registrationAdminUsername())) {
		s.apiFail(http.StatusBadRequest, "validation_error", "该用户名已被管理员账号占用")
		return
	}

	maxClients, err := registrationAPIQuotaParameter(s, registrationMaxClients(), "max_client", "max_client_num")
	if err != nil {
		s.apiFail(http.StatusBadRequest, "validation_error", err.Error())
		return
	}
	maxTunnels, err := registrationAPIQuotaParameter(s, registrationMaxTunnels(), "max_tunnel", "max_tunnel_num")
	if err != nil {
		s.apiFail(http.StatusBadRequest, "validation_error", err.Error())
		return
	}
	maxHosts, err := registrationAPIHostQuotaParameter(s, registrationMaxHosts(), "max_host", "max_host_num")
	if err != nil {
		s.apiFail(http.StatusBadRequest, "validation_error", err.Error())
		return
	}
	maxConn, err := registrationAPIQuotaParameter(s, registrationMaxConn(), "max_conn", "max_connection")
	if err != nil {
		s.apiFail(http.StatusBadRequest, "validation_error", err.Error())
		return
	}
	bandwidth := registrationBandwidthBps()
	if registrationAPIHasField(s, "bandwidth_limit") {
		bandwidth, _, err = file.ParseMbpsQuotaInput(s.GetString("bandwidth_limit"))
	} else if registrationAPIHasField(s, "bandwidth") {
		bandwidth, _, err = file.ParseMbpsQuotaInput(s.GetString("bandwidth"))
	} else if registrationAPIHasField(s, "upload_limit") && registrationAPIHasField(s, "download_limit") {
		uploadBps := registrationLegacyAPIKB(s.GetString("upload_limit"))
		downloadBps := registrationLegacyAPIKB(s.GetString("download_limit"))
		if uploadBps == 0 || downloadBps == 0 {
			bandwidth = file.UnlimitedInt64()
		} else {
			bandwidth = file.LimitInt64(uploadBps + downloadBps)
		}
	} else if registrationAPIHasField(s, "upload_limit") {
		bandwidth = file.LimitInt64(registrationLegacyAPIKB(s.GetString("upload_limit")))
	} else if registrationAPIHasField(s, "download_limit") {
		bandwidth = file.LimitInt64(registrationLegacyAPIKB(s.GetString("download_limit")))
	}
	if err != nil {
		s.apiFail(http.StatusBadRequest, "validation_error", err.Error())
		return
	}

	now := time.Now()
	expireTime := registrationExpireTime(now, registrationExpireDays())
	if registrationAPIHasField(s, "expire_time") {
		expireTime, err = normalizeUserExpireTime(s.GetString("expire_time"))
		if err != nil {
			s.apiFail(http.StatusBadRequest, "validation_error", err.Error())
			return
		}
	} else if registrationAPIHasField(s, "expire_days") {
		days, parseErr := registrationAPIInt(s.GetString("expire_days"))
		if parseErr != nil {
			s.apiFail(http.StatusBadRequest, "validation_error", "expire_days 必须是非负整数")
			return
		}
		expireTime = registrationExpireTime(now, days)
	}

	user := &file.User{
		UserName:       username,
		Password:       password,
		Email:          emailAddress,
		Status:         true,
		Remark:         remark,
		MaxClientNum:   maxClients,
		MaxTunnelNum:   maxTunnels,
		MaxHostNum:     maxHosts,
		MaxConn:        maxConn,
		BandwidthLimit: bandwidth,
		ExpireTime:     expireTime,
		CreateTime:     now.Format("2006-01-02 15:04:05"),
	}
	if err := file.GetDb().NewUser(user); err != nil {
		s.apiFail(http.StatusBadRequest, "registration_failed", err.Error())
		return
	}
	rows := newUserListRowsWithResourceCounts([]*file.User{user}, file.GetDb().GetUserResourceCounts())
	if len(rows) == 1 {
		s.apiData(rows[0])
		return
	}
	s.apiData(map[string]interface{}{"Id": user.Id, "UserName": user.UserName})
}

func registrationAdminUsername() string {
	return strings.TrimSpace(beego.AppConfig.String("web_username"))
}

func registrationAPIHasField(s *ApiV1Controller, key string) bool {
	if s == nil || s.Ctx == nil || s.Ctx.Request == nil {
		return false
	}
	if s.Ctx.Request.Form == nil {
		_ = s.Ctx.Request.ParseForm()
	}
	_, exists := s.Ctx.Request.Form[key]
	return exists
}

func registrationAPIIntParameter(s *ApiV1Controller, fallback int, keys ...string) (int, error) {
	for _, key := range keys {
		if !registrationAPIHasField(s, key) {
			continue
		}
		return registrationAPIInt(s.GetString(key))
	}
	return fallback, nil
}

func registrationAPIInt(raw string) (int, error) {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < 0 {
		return 0, &registrationAPIParameterError{}
	}
	return value, nil
}

// registrationAPIQuotaParameter uses the same three-state contract as the
// dashboard: an omitted field falls back to the configured default, blank or
// null means unlimited, zero means the resource is not included, and a
// positive value is the cap. Keeping one contract avoids an admin API call
// silently turning a zero-quota plan into an unlimited account.
func registrationAPIQuotaParameter(s *ApiV1Controller, fallback *int, keys ...string) (*int, error) {
	for _, key := range keys {
		if !registrationAPIHasField(s, key) {
			continue
		}
		value, _, err := file.ParseIntQuotaInput(s.GetString(key))
		return value, err
	}
	return file.IntQuotaSnapshot(fallback), nil
}

// registrationAPIHostQuotaParameter applies the same compatibility rules as
// the other registration limits while keeping an omitted Host quota unlimited.
// register_max_hosts can be absent from pre-upgrade profiles, where the field
// did not exist and Host rules were not independently capped.
func registrationAPIHostQuotaParameter(s *ApiV1Controller, fallback *int, keys ...string) (*int, error) {
	for _, key := range keys {
		if !registrationAPIHasField(s, key) {
			continue
		}
		value, _, err := file.ParseIntQuotaInput(s.GetString(key))
		return value, err
	}
	return file.IntQuotaSnapshot(fallback), nil
}

// registrationLegacyAPIKB accepts the historical KB/s API aliases only during
// the compatibility window and converts them to bps for the new shared budget.
func registrationLegacyAPIKB(raw string) int64 {
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value < 0 {
		return 0
	}
	return rate.BpsFromKBPerSecond(value)
}

type registrationAPIParameterError struct{}

func (*registrationAPIParameterError) Error() string {
	return "配额和有效天数必须是非负整数"
}
