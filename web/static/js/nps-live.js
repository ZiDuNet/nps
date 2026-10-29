(function ($) {
    'use strict';
    if (!$) return;

    var timer = null;
    var inFlight = false;
    var failureCount = 0;
    var refreshInterval = 3000;
    var maxBackoff = 30000;

    function webBase() {
        return String((window.nps && window.nps.web_base_url) || '').replace(/\/+$/, '');
    }

    function formatRate(value) {
        var bytes = Number(value) || 0;
        if (typeof changeunit === 'function') return changeunit(bytes) + '/s';
        if (bytes < 1024) return bytes + ' B/s';
        if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + ' KB/s';
        if (bytes < 1024 * 1024 * 1024) return (bytes / 1024 / 1024).toFixed(1) + ' MB/s';
        return (bytes / 1024 / 1024 / 1024).toFixed(1) + ' GB/s';
    }

    function formatConnections(value, limit) {
        var used = Math.max(0, Number(value) || 0);
        var max = Math.max(0, Number(limit) || 0);
        return used + ' / ' + (max > 0 ? max : '∞');
    }

    function uniqueIDs(selector) {
        var seen = {};
        var ids = [];
        $(selector).each(function () {
            var id = Number($(this).attr('data-live-client-id') || $(this).attr('data-live-user-id'));
            if (!isFinite(id) || id <= 0 || seen[id]) return;
            seen[id] = true;
            ids.push(id);
        });
        return ids;
    }

    function hasTargets() {
        return $('#dashboard-proxy-rate, #dashboard-proxy-connections, [data-live-self-up], [data-live-client-id], [data-live-user-id]').length > 0;
    }

    function setValue(root, attribute, value) {
        var node = root ? root.querySelector('[' + attribute + ']') : document.querySelector('[' + attribute + ']');
        if (node) node.textContent = value;
    }

    function applyClient(root, row) {
        if (!root || !row) return;
        setValue(root, 'data-live-client-connections', formatConnections(row.connections, row.connectionLimit));
        setValue(root, 'data-live-client-up', formatRate(row.uploadRate));
        setValue(root, 'data-live-client-down', formatRate(row.downloadRate));
        setValue(root, 'data-live-client-rate', formatRate((Number(row.uploadRate) || 0) + (Number(row.downloadRate) || 0)));
        root.classList.remove('is-stale');
    }

    function applyUser(root, row) {
        if (!root || !row) return;
        setValue(root, 'data-live-user-connections', formatConnections(row.connections, row.connectionLimit));
        setValue(root, 'data-live-user-up', formatRate(row.uploadRate));
        setValue(root, 'data-live-user-down', formatRate(row.downloadRate));
        setValue(root, 'data-live-user-clients', String(Number(row.clients) || 0));
        root.classList.remove('is-stale');
    }

    function applySnapshot(snapshot) {
        if (!snapshot) return;
        $('#dashboard-proxy-rate').text(formatRate(snapshot.proxyInRate) + ' / ' + formatRate(snapshot.proxyOutRate));
        $('#dashboard-proxy-connections').text(String(Number(snapshot.currentProxyConnections) || 0));
        $('[data-live-self-up]').text(formatRate(snapshot.currentUser ? snapshot.currentUser.uploadRate : (snapshot.currentClient ? snapshot.currentClient.uploadRate : 0)));
        $('[data-live-self-down]').text(formatRate(snapshot.currentUser ? snapshot.currentUser.downloadRate : (snapshot.currentClient ? snapshot.currentClient.downloadRate : 0)));
        $('[data-live-self-connections]').text(snapshot.currentUser
            ? formatConnections(snapshot.currentUser.connections, snapshot.currentUser.connectionLimit)
            : formatConnections(snapshot.currentClient ? snapshot.currentClient.connections : 0, snapshot.currentClient ? snapshot.currentClient.connectionLimit : 0));
        $('.nav-account-live').removeClass('is-stale');

        var clients = {};
        (snapshot.clients || []).forEach(function (row) { clients[Number(row.clientId)] = row; });
        $('[data-live-client-id]').each(function () {
            var row = clients[Number($(this).attr('data-live-client-id'))];
            if (row) applyClient(this, row);
        });

        var users = {};
        (snapshot.users || []).forEach(function (row) { users[Number(row.userId)] = row; });
        $('[data-live-user-id]').each(function () {
            var row = users[Number($(this).attr('data-live-user-id'))];
            if (row) applyUser(this, row);
        });
    }

    function schedule(delay) {
        if (timer) window.clearTimeout(timer);
        timer = window.setTimeout(refresh, delay == null ? refreshInterval : delay);
    }

    function refresh() {
        timer = null;
        if (document.hidden || inFlight || !hasTargets()) return;
        inFlight = true;
        var clientIDs = uniqueIDs('[data-live-client-id]');
        var userIDs = uniqueIDs('[data-live-user-id]');
        var request = {};
        if (clientIDs.length) request.client_ids = clientIDs.join(',');
        if (userIDs.length) request.user_ids = userIDs.join(',');
        if (!clientIDs.length && !userIDs.length) request.summary_only = 1;
        $.ajax({
            url: webBase() + '/index/livedata',
            method: 'GET',
            data: request,
            dataType: 'json',
            cache: false,
            global: false,
            timeout: 6000
        }).done(function (response) {
            if (!response || Number(response.status) !== 1 || !response.data) return;
            failureCount = 0;
            refreshInterval = 3000;
            applySnapshot(response.data);
        }).fail(function () {
            failureCount++;
            refreshInterval = Math.min(maxBackoff, 3000 * Math.pow(2, Math.min(failureCount, 4)));
            $('.nav-account-live, [data-live-client-id], [data-live-user-id]').addClass('is-stale');
        }).always(function () {
            inFlight = false;
            schedule();
        });
    }

    function start() {
        if (!hasTargets()) return;
        schedule(0);
    }

    $(document).on('nps:content-loaded', start);
    document.addEventListener('visibilitychange', function () {
        if (!document.hidden) start();
    });
    $(start);
})(window.jQuery);
