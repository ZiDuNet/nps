(function (root, factory) {
    var api = factory();
    if (typeof module === 'object' && module.exports) module.exports = api;
    else root.NpsAudit = api;
}(typeof window !== 'undefined' ? window : this, function () {
    'use strict';
    function escape(value) {
        return String(value == null ? '' : value).replace(/[&<>"']/g, function (ch) {
            return {'&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'}[ch];
        });
    }
    function label(row, field, english) {
        return (english ? row[field + 'LabelEN'] : row[field + 'Label']) || row[field] || '-';
    }
    function pair(main, sub, mono) {
        return '<div class="audit-cell' + (mono ? ' audit-cell--mono' : '') + '"><span>' + escape(main || '-')
            + '</span>' + (sub ? '<small>' + escape(sub) + '</small>' : '') + '</div>';
    }
    function parseDetails(value) {
        if (value && typeof value === 'object') return value;
        if (!value) return {};
        try { return JSON.parse(value); } catch (error) { return value; }
    }
    var stages = {
        request: ['请求校验', 'Request validation'], pow: ['PoW 校验', 'PoW verification'],
        captcha: ['图片验证码', 'Image verification'], login_payload: ['登录请求时效', 'Request validity'],
        rate_limit: ['登录限频', 'Sign-in rate limit'], password: ['账号密码', 'Credentials'],
        totp: ['动态验证码', 'TOTP'], remember: ['免登录凭证', 'Remembered sign-in'],
        complete: ['已完成', 'Complete'], connected: ['已连接', 'Connected'], email: ['邮箱校验', 'Email verification'], validation: ['输入校验', 'Input validation']
    };
    var methods = {password: ['密码登录', 'Password'], 'password+totp': ['密码 + 动态验证码', 'Password + TOTP'],
        remember: ['免登录', 'Remembered sign-in'], session: ['会话', 'Session'],
        registration: ['注册', 'Registration'], email_code: ['邮箱验证码', 'Email verification']};
    var actors = {admin: ['管理员', 'Administrator'], user: ['用户', 'User'], client: ['客户端账号', 'Client account'],
        anonymous: ['未认证', 'Unauthenticated'], api: ['管理员 API', 'Admin API'], 'api-user': ['用户 API', 'User API']};
    function translated(values, value, english) { return values[value] ? values[value][english ? 1 : 0] : (value || '-'); }
    function summary(row, english) {
        var error = (english ? row.ErrorLabelEN : row.ErrorLabel) || row.Error;
        if (error) return pair(error, row.Stage ? translated(stages, row.Stage, english) : '');
        var details = parseDetails(row.Details);
        if (details && typeof details === 'object') {
            var values = [];
            ['remark', 'username', 'host', 'mode', 'version', 'changed', 'removed'].forEach(function (key) {
                if (details[key] != null) {
                    var names = {changed: english ? 'Changed' : '变更项', removed: english ? 'Removed' : '清理条数'};
                    values.push((names[key] ? names[key] + ': ' : '') + String(details[key]));
                }
            });
            if (values.length) return pair(values.join(' · '), '');
        }
        return pair(row.AuthMethod ? translated(methods, row.AuthMethod, english) : (english ? 'Completed' : '操作完成'), '');
    }
    function rawEvent(row, connections) {
        if (!connections) return {
            id: row.ID, time: row.Time, actor: row.Actor, actor_type: row.ActorType, actor_id: row.ActorID,
            owner_user_id: row.OwnerUserID, source_ip: row.SourceIP, remote_addr: row.RemoteAddr,
            ip_source: row.IPSource, request_id: row.RequestID, method: row.Method, path: row.Path,
            user_agent: row.UserAgent, action: row.Action, resource: row.Resource,
            resource_type: row.ResourceType, resource_id: row.ResourceID, result: row.Result,
            auth_method: row.AuthMethod, stage: row.Stage, error_code: row.ErrorCode,
            error: row.Error, details: parseDetails(row.Details)
        };
        return {
            id: row.ID, time: row.Time, result: row.Result, kind: row.Kind, action: row.Action,
            started_at: row.StartedAt, ended_at: row.EndedAt, duration: row.Duration, duration_ms: row.RawDuration,
            user_id: row.UserID, user_name: row.User, user_status: row.UserStatus, user_expire_time: row.UserExpireTime,
            client_id: row.ClientID, client_name: row.Client, client_status: row.ClientStatus,
            client_version: row.ClientVer, client_expire_time: row.ClientExpireTime,
            client_effective_expire_time: row.ClientEffectiveExpireTime, client_last_online_time: row.ClientLastOnlineTime,
            resource_type: row.ResourceType, resource_id: row.ResourceID, resource: row.Resource,
            transport: row.Transport, bridge_channel: row.BridgeChannel, handshake_stage: row.HandshakeStage,
            source_ip: row.SourceIP, remote_addr: row.RemoteAddr, server_addr: row.ServerAddr,
            client_public_ip: row.ClientPublicIP, client_local_addr: row.ClientLocalAddr,
            client_local_addr_source: row.ClientLocalAddrSource, bytes_in: row.BytesIn, bytes_out: row.BytesOut,
            target: row.Target, error: row.Error
        };
    }
    function detail(row, connections, english) {
        var caption = english ? 'Full event · JSON' : '完整事件 · JSON';
        return '<div class="audit-detail"><div class="audit-detail__bar"><span>' + caption + '</span>'
            + '<button type="button" class="btn btn-outline audit-detail__copy" data-audit-copy><i class="fa fa-copy" aria-hidden="true"></i> '
            + (english ? 'Copy JSON' : '复制 JSON') + '</button></div><pre class="audit-connection-raw" tabindex="0" aria-label="'
            + caption + '">' + escape(JSON.stringify(rawEvent(row, connections), null, 2)) + '</pre></div>';
    }
    return {escape: escape, label: label, pair: pair, parseDetails: parseDetails, summary: summary,
        rawEvent: rawEvent, detail: detail, stage: function (value, english) { return translated(stages, value, english); },
        method: function (value, english) { return translated(methods, value, english); },
        actor: function (value, english) { return translated(actors, value, english); }};
}));
