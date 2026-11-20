(function ($, window, document) {
    'use strict';
    var form = document.getElementById('recover-form');
    if (!form) return;
    var send = document.getElementById('auth-send-email-code');
    var submit = document.getElementById('recover-submit');
    var status = document.getElementById('recover-status');
    var busy = false, remaining = 0, timer = null;
    function local(zh, en) { return window.npsIsEnglish && window.npsIsEnglish() ? en : zh; }
    function message(text, failed) {
        status.removeAttribute('data-i18n-zh'); status.removeAttribute('data-i18n-en');
        status.textContent = text; status.setAttribute('role', failed ? 'alert' : 'status');
    }
    function captcha() {
        var image = form.querySelector('img.captcha-img');
        if (image) image.src = image.getAttribute('src').split('?')[0] + '?reload=' + Date.now();
        var input = form.querySelector('[name="captcha"]');
        if (input) input.value = '';
    }
    function data() {
        var fields = {};
        $(form).serializeArray().forEach(function (field) { fields[field.name] = field.value; });
        var challenge = window.nps.loginChallenge;
        if (challenge) {
            fields.nonce = challenge.nonce;
            fields.challenge_expires = challenge.expires;
            fields.challenge_signature = challenge.signature;
        }
        return fields;
    }
    function loadChallenge() {
        $.ajax({url: window.nps.web_base_url + '/auth/challenge', dataType: 'json', cache: false, timeout: 5000}).done(function (res) {
            if (res && res.status && res.nonce && res.signature) {
                window.nps.loginChallenge = {nonce: res.nonce, expires: res.expires, signature: res.signature};
            }
        });
    }
    function countdown() {
        send.removeAttribute('data-i18n-zh'); send.removeAttribute('data-i18n-en');
        send.disabled = remaining > 0;
        send.textContent = remaining > 0 ? local(remaining + ' 秒后重试', 'Retry in ' + remaining + 's') : local('获取验证码', 'Get code');
    }
    function request(path, payload, done) {
        busy = true; send.disabled = true; submit.disabled = true; form.setAttribute('aria-busy', 'true');
        message(local('正在处理，请稍候…', 'Working, please wait…'), false);
        return $.ajax({type: 'POST', url: window.nps.web_base_url + path, data: payload, dataType: 'json', timeout: 20000})
            .done(function (response) {
                message(window.npsAuthErrorMessage(response), !response.status);
                if (response.status) done(response);
            }).fail(function (xhr) {
                message(xhr.responseJSON ? window.npsAuthErrorMessage(xhr.responseJSON) : local('请求失败，请稍后重试。', 'Request failed. Try again later.'), true);
            }).always(function () {
                busy = false; submit.disabled = false; send.disabled = remaining > 0; form.setAttribute('aria-busy', 'false'); captcha();
                loadChallenge();
            });
    }
    send.addEventListener('click', function () {
        if (busy || remaining > 0 || send.disabled) return;
        var email = document.getElementById('auth-email'), challenge = form.querySelector('[name="captcha"]');
        if (!email.reportValidity() || (challenge && !challenge.reportValidity())) return;
        var fields = data();
        if (!window.nps.loginChallenge) { message(local('安全校验未就绪，请刷新页面后重试。', 'Security challenge is not ready. Refresh the page.'), true); return; }
        delete fields.password; delete fields.email_code;
        request('/login/recover/code', fields, function (response) {
            remaining = Number(response.retry_after) || 60; countdown();
            window.clearInterval(timer);
            timer = window.setInterval(function () { remaining = Math.max(remaining - 1, 0); countdown(); if (!remaining) window.clearInterval(timer); }, 1000);
        });
    });
    form.addEventListener('submit', function (event) {
        event.preventDefault();
        if (busy || submit.disabled || !form.reportValidity()) return;
        var fields = data(), confirm = document.getElementById('auth-confirm');
        if (fields.password !== confirm.value) { message(local('两次输入的新密码不一致。', 'The new passwords do not match.'), true); confirm.focus(); return; }
        try {
            if (!window.nps.loginChallenge) throw new Error('challenge missing');
            var encryptor = new window.JSEncrypt(); encryptor.setPublicKey(window.nps.publicKey);
            fields.password = encryptor.encrypt(JSON.stringify({n: window.nps.loginChallenge.nonce, t: Date.now() + window.nps.timeOffset, p: fields.password}));
            if (!fields.password) throw new Error('encryption failed');
        } catch (error) { message(local('安全加密不可用，请刷新页面后重试。', 'Secure encryption unavailable. Refresh the page.'), true); return; }
        request('/login/recover', fields, function () {
            form.reset(); window.clearInterval(timer);
            window.setTimeout(function () { window.location.assign(window.nps.web_base_url + '/login/index'); }, 900);
        });
    });
    $.ajax({url: window.nps.web_base_url + '/auth/gettime', dataType: 'json', timeout: 5000, cache: false}).done(function (res) { if (res && res.time != null) window.nps.timeOffset = res.time * 1000 - Date.now(); });
    loadChallenge();
    function theme() {
        var dark = document.documentElement.getAttribute('data-theme') === 'dark';
        document.getElementById('theme-icon').className = dark ? 'fa fa-sun' : 'fa fa-moon';
        document.getElementById('theme-toggle').setAttribute('aria-pressed', String(dark));
        try {
            var bg = document.getElementById('auth-network-background').contentWindow.NPS_BG;
            if (bg) bg.setTheme(dark ? 'dark' : 'light');
        } catch (error) {}
    }
    document.getElementById('theme-toggle').addEventListener('click', function () {
        var next = document.documentElement.getAttribute('data-theme') === 'dark' ? 'light' : 'dark';
        document.documentElement.setAttribute('data-theme', next);
        try { window.localStorage.setItem('nps-theme', next); } catch (error) {}
        theme();
    });
    document.getElementById('auth-network-background').addEventListener('load', theme);
    $(document).on('npsLanguageChanged.recovery', function () { if (remaining > 0) countdown(); });
    window.addEventListener('pagehide', function () { window.clearInterval(timer); });
    theme();
}(jQuery, window, document));
