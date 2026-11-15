const assert = require('node:assert/strict');
const test = require('node:test');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const ui = require('../static/js/nps-audit.js');

test('audit action uses localized names without changing stable machine codes', () => {
    const row = {Action: 'auth.login', ActionLabel: '登录', ActionLabelEN: 'Sign in'};
    assert.equal(ui.label(row, 'Action', false), '登录');
    assert.equal(ui.label(row, 'Action', true), 'Sign in');
    assert.equal(ui.label({Action: 'future.action'}, 'Action', false), 'future.action');
    assert.equal(ui.rawEvent(row, false).action, 'auth.login');
});
test('audit formatting escapes all server supplied text including raw JSON', () => {
    const attack = '<img src=x onerror="alert(1)">';
    for (const html of [ui.pair(attack, attack), ui.summary({Error: attack}, false), ui.detail({Details: JSON.stringify({remark: attack}), Action: attack}, false, false)]) {
        assert.doesNotMatch(html, /<img/);
        assert.match(html, /&lt;img/);
    }
});
test('audit details retain request and connection metadata and can be copied', () => {
    const auth = {Method:'POST',Path:'/login/verify',RequestID:'req-1',RemoteAddr:'192.0.2.10:77',IPSource:'peer',AuthMethod:'password',Stage:'totp',ErrorCode:'totp_invalid',UserAgent:'TestBrowser'};
    const raw = ui.rawEvent(auth, false);
    assert.equal(raw.request_id,'req-1'); assert.equal(raw.remote_addr,'192.0.2.10:77');
    assert.equal(raw.error_code,'totp_invalid'); assert.equal(raw.user_agent,'TestBrowser');
    const connection = ui.rawEvent({ClientLocalAddr:'2001:db8::10',RemoteAddr:'[2001:db8::11]:123',ServerAddr:':8025',RawDuration:12,BytesIn:123,BytesOut:456}, true);
    assert.equal(connection.client_local_addr,'2001:db8::10'); assert.equal(connection.duration_ms,12);
    assert.equal(connection.bytes_out,456);
    assert.match(ui.detail(auth,false,false), /data-audit-copy/);
    assert.match(ui.detail(auth,false,true), /Copy JSON/);
});
test('audit result summary is human readable with safe historical fallbacks', () => {
    assert.match(ui.summary({ErrorLabel:'图片验证码错误',Stage:'captcha'},false), /图片验证码错误/);
    assert.match(ui.summary({Details:'{"changed":"3","remark":"示例"}'},false), /示例 · 变更项: 3/);
    assert.match(ui.summary({AuthMethod:'remember'},true), /Remembered sign-in/);
    assert.doesNotThrow(()=>ui.summary({Details:'{invalid json'},false));
    assert.equal(ui.actor('anonymous',false),'未认证');
});
test('audit page script parses and desktop fills content without widening the document', () => {
    const template = fs.readFileSync(path.join(__dirname,'../views/audit/list.html'),'utf8');
    const inline = template.match(/<script>\n([\s\S]*)<\/script>/)[1];
    assert.doesNotThrow(()=>new vm.Script(inline));
    assert.match(template,/pageSize: 50/);
    assert.match(template,/storageKey: key \+ '\.v2'/);
    assert.match(template,/ArrowLeft/);
    assert.match(template,/npsLanguageChanged\.audit/);
    assert.match(template,/instance\.refreshFilterTitles\(\)/);
    assert.match(template,/removeAttr\('data-i18n-zh data-i18n-en'\)/);
    const css = fs.readFileSync(path.join(__dirname,'../static/css/nps-audit.css'),'utf8');
    assert.match(css,/max-width: none/);
    assert.match(css,/audit-hub__view.is-active > \.management-table-card \{ flex: 1 1 auto; width: 100%/);
    assert.match(css,/white-space: pre-wrap/);
    assert.match(css,/@media \(max-width: 600px\)/);
});

test('log cleanup validates whole retention days and freezes the selected journal scope', () => {
    const now = Date.parse('2026-11-15T12:00:00Z');
    assert.deepEqual(ui.cleanupPlan('30', 'operations', now), {days: 30, kind: 'operations', before: '2026-10-16T12:00:00.000Z'});
    assert.equal(ui.cleanupPlan(1, 'connections', now).before, '2026-11-14T12:00:00.000Z');
    assert.ok(ui.cleanupPlan(36500, 'operations', now));
    for (const value of ['', ' ', null, 0, -1, 1.5, '1e3', '36501', Infinity, '<script>']) {
        assert.equal(ui.cleanupPlan(value, 'operations', now), null, 'accepted invalid retention: ' + value);
    }
    assert.equal(ui.cleanupPlan(30, '../../nps.log', now), null);
    assert.equal(ui.cleanupPlan(30, 'operations', NaN), null);
});

test('audit cleanup is administrator-only UI with explicit confirmation and safe request feedback', () => {
    const template = fs.readFileSync(path.join(__dirname, '../views/audit/list.html'), 'utf8');
    assert.match(template, /\{\{if \.isAdmin\}\}<button[^>]+id="audit-cleanup-open"/);
    assert.match(template, /\{\{if \.isAdmin\}\}\s*<div id="audit-cleanup-modal"/);
    assert.match(template, /id="audit-cleanup-confirm" type="checkbox"/);
    assert.match(template, /id="audit-cleanup-submit"[^>]+disabled/);
    assert.match(template, /aria-describedby="audit-cleanup-cutoff audit-cleanup-warning"/);
    assert.match(template, /无法撤销/);
    assert.match(template, /最近 2000 条/);
    assert.match(template, /data\.recent_limit/);
    assert.match(template, /headers: \{'X-NPS-Log-Cleanup': '1'\}/);
    assert.match(template, /confirm: '1'/);
    assert.match(template, /cleanupKind = activeView/);
    assert.match(template, /cleanupBusy \|\| !plan \|\| !\$\('#audit-cleanup-confirm'\)\.prop\('checked'\)/);
    assert.match(template, /before retrying/);
    assert.match(template, /setLang\('\.audit-hub'\)/);
    assert.match(template, /setLang\('#audit-cleanup-modal'\)/);
    assert.doesNotMatch(template, /setLang\('\.audit-hub, /);
});
