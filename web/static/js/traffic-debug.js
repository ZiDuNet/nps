(function (window, document) {
    'use strict';

    var active = null;

    function text(value) {
        var element = document.createElement('span');
        element.textContent = value == null ? '' : String(value);
        return element.innerHTML;
    }

    function localText(zh, en) {
        return typeof window.npsIsEnglish === 'function' && window.npsIsEnglish() ? en : zh;
    }

    function prettyBody(value) {
        if (!value) return '';
        try {
            return JSON.stringify(JSON.parse(value), null, 2);
        } catch (error) {
            return value;
        }
    }

    function formatHeaders(headers) {
        if (!headers) return '-';
        return Object.keys(headers).sort().map(function (key) {
            return key + ': ' + headers[key];
        }).join('\n') || '-';
    }

    function formatBytes(value) {
        var number = Number(value || 0);
        if (!number) return '0 B';
        var units = ['B', 'KB', 'MB', 'GB'];
        var index = 0;
        while (number >= 1024 && index < units.length - 1) { number /= 1024; index++; }
        return number.toFixed(index ? 1 : 0) + ' ' + units[index];
    }

    function createElement(tag, className, content) {
        var element = document.createElement(tag);
        if (className) element.className = className;
        if (content != null) element.textContent = content;
        return element;
    }

    function npsOpenTrafficDebug(kind, id) {
        if (active) active.close();
        var state = {
            kind: kind === 'host' ? 'host' : 'tunnel',
            id: Number(id),
            events: {},
            order: [],
            selected: null,
            source: null,
            paused: false,
            autoScroll: true,
            closed: false,
            root: null,
            list: null,
            detail: null,
            status: null,
            pauseButton: null,
            renderQueued: false
        };
        active = state;

        var root = createElement('div', 'traffic-debug-modal');
        root.innerHTML = '<div class="traffic-debug-dialog" role="dialog" aria-modal="true" aria-labelledby="traffic-debug-title">'
            + '<div class="traffic-debug-header">'
            + '<div><div class="traffic-debug-kicker">' + text(localText('实时请求调试', 'Live request inspector')) + '</div><h2 id="traffic-debug-title">' + text((state.kind === 'host' ? 'Host' : 'Tunnel') + ' #' + state.id) + '</h2></div>'
            + '<div class="traffic-debug-actions"><span class="traffic-debug-status is-connecting"></span><button type="button" class="btn btn-outline btn-primary traffic-debug-pause"></button><button type="button" class="btn btn-outline btn-info traffic-debug-clear"></button><button type="button" class="btn btn-outline traffic-debug-close" aria-label="Close"><i class="fa fa-times" aria-hidden="true"></i></button></div>'
            + '</div>'
            + '<div class="traffic-debug-toolbar"><span class="traffic-debug-hint"></span><button type="button" class="btn btn-xs btn-outline btn-primary traffic-debug-autoscroll"></button><span class="traffic-debug-count"></span></div>'
            + '<div class="traffic-debug-content"><aside class="traffic-debug-list"></aside><main class="traffic-debug-detail"><div class="traffic-debug-empty"></div></main></div>'
            + '</div>';
        document.body.appendChild(root);
        state.root = root;
        state.list = root.querySelector('.traffic-debug-list');
        state.detail = root.querySelector('.traffic-debug-detail');
        state.status = root.querySelector('.traffic-debug-status');
        state.pauseButton = root.querySelector('.traffic-debug-pause');
        root.querySelector('.traffic-debug-hint').textContent = localText('仅显示打开窗口后的文本请求正文，文件和二进制内容会被忽略。', 'Only text bodies after opening are shown. Files and binary content are ignored.');
        root.querySelector('.traffic-debug-empty').textContent = localText('等待请求进入...', 'Waiting for requests...');
        root.querySelector('.traffic-debug-clear').textContent = localText('清空', 'Clear');
        root.querySelector('.traffic-debug-autoscroll').textContent = localText('自动滚动：开', 'Auto-scroll: On');
        state.pauseButton.innerHTML = '<i class="fa fa-pause" aria-hidden="true"></i> ' + text(localText('暂停', 'Pause'));

        function setStatus(label, className) {
            state.status.className = 'traffic-debug-status ' + className;
            state.status.textContent = label;
        }

        function selectedEvent() {
            return state.selected ? state.events[state.selected] : null;
        }

        function renderList() {
            state.list.innerHTML = '';
            state.order.slice().reverse().forEach(function (requestId) {
                var item = state.events[requestId];
                if (!item) return;
                var row = createElement('button', 'traffic-debug-request' + (state.selected === requestId ? ' is-selected' : ''));
                row.type = 'button';
                var status = item.status ? String(item.status) : (item.complete ? 'done' : 'live');
                var label = item.method || (String(item.type || '').indexOf('connection') === 0 ? 'TCP' : item.type);
                row.innerHTML = '<span class="traffic-debug-request-method">' + text(label) + '</span>'
                    + '<span class="traffic-debug-request-path">' + text(item.path || item.target_addr || '-') + '</span>'
                    + '<span class="traffic-debug-request-meta">' + text(status + ' · ' + formatBytes((item.bytes_in || 0) + (item.bytes_out || 0))) + '</span>';
                row.addEventListener('click', function () { state.selected = requestId; renderList(); renderDetail(); });
                state.list.appendChild(row);
            });
            root.querySelector('.traffic-debug-count').textContent = localText(state.order.length + ' 条请求', state.order.length + ' requests');
        }

        function renderDetail() {
            var item = selectedEvent();
            if (!item) {
                state.detail.innerHTML = '<div class="traffic-debug-empty">' + text(localText('选择左侧请求查看详细信息', 'Select a request to inspect details')) + '</div>';
                return;
            }
            var requestBody = prettyBody(item.requestBody || '');
            var responseBody = prettyBody(item.responseBody || '');
            if (item.requestTruncated) requestBody += (requestBody ? '\n\n' : '') + localText('[正文已截断：仅显示前 256KB]', '[Body truncated: first 256KB shown]');
            if (item.responseTruncated) responseBody += (responseBody ? '\n\n' : '') + localText('[正文已截断：仅显示前 256KB]', '[Body truncated: first 256KB shown]');
            state.detail.innerHTML = '<div class="traffic-debug-summary">'
                + '<div><span>' + text(localText('来源地址', 'Source')) + '</span><strong>' + text(item.remote_addr || '-') + '</strong></div>'
                + '<div><span>' + text(localText('后端地址', 'Backend')) + '</span><strong>' + text(item.target_addr || '-') + '</strong></div>'
                + '<div><span>' + text(localText('状态', 'Status')) + '</span><strong>' + text(item.status || (item.complete ? '完成' : '传输中')) + '</strong></div>'
                + '<div><span>' + text(localText('耗时', 'Duration')) + '</span><strong>' + text((item.duration_ms || '-') + (item.duration_ms ? ' ms' : '')) + '</strong></div>'
                + '<div><span>' + text(localText('请求大小', 'Request size')) + '</span><strong>' + text(formatBytes(item.requestBytes)) + '</strong></div>'
                + '<div><span>' + text(localText('响应大小', 'Response size')) + '</span><strong>' + text(formatBytes(item.responseBytes)) + '</strong></div>'
                + '</div>'
                + '<div class="traffic-debug-tabs">'
                + '<section><h3>' + text(localText('请求头', 'Request headers')) + '</h3><pre>' + text(formatHeaders(item.requestHeaders)) + '</pre></section>'
                + '<section><h3>' + text(localText('请求体', 'Request body')) + '</h3><pre>' + text(requestBody || (item.requestSkipped ? localText('[文件或二进制内容已忽略]', '[File or binary body omitted]') : '-')) + '</pre></section>'
                + '<section><h3>' + text(localText('响应头', 'Response headers')) + '</h3><pre>' + text(formatHeaders(item.responseHeaders)) + '</pre></section>'
                + '<section><h3>' + text(localText('响应体', 'Response body')) + '</h3><pre>' + text(responseBody || (item.responseSkipped ? localText('[文件或二进制内容已忽略]', '[File or binary body omitted]') : '-')) + '</pre></section>'
                + '</div>';
        }

        function scheduleRender() {
            if (state.renderQueued) return;
            state.renderQueued = true;
            window.requestAnimationFrame(function () {
                state.renderQueued = false;
                renderList();
                renderDetail();
                if (state.autoScroll && state.detail) state.detail.scrollTop = state.detail.scrollHeight;
            });
        }

        function mergeEvent(event) {
            if (!event) return;
            if (event.type === 'ready') { setStatus(localText('已连接', 'Connected'), 'is-live'); return; }
            if (event.type === 'dropped') { setStatus(localText('已丢弃 ' + event.dropped + ' 条调试事件', event.dropped + ' events dropped'), 'is-warning'); return; }
            var idValue = event.request_id || ('connection-' + (event.time || Date.now()));
            var item = state.events[idValue];
            if (!item) {
                item = {request_id: idValue, requestBody: '', responseBody: '', requestBytes: 0, responseBytes: 0};
                state.events[idValue] = item;
                state.order.push(idValue);
                if (state.order.length > 100) delete state.events[state.order.shift()];
            }
            Object.keys(event).forEach(function (key) { if (key !== 'body') item[key] = event[key]; });
            if (event.type === 'request_start') {
                item.requestHeaders = event.headers;
                item.requestSkipped = event.body_skipped;
            } else if (event.type === 'request_sent') {
                item.requestBytes = event.bytes_in || item.requestBytes;
            } else if (event.type === 'response_headers') {
                item.status = event.status;
                item.responseHeaders = event.headers;
                item.responseSkipped = event.body_skipped;
            } else if (event.type === 'body_chunk') {
                if (event.part === 'request') item.requestBody += event.body || '';
                else item.responseBody += event.body || '';
                if (event.part === 'request') item.requestBytes += (event.body || '').length;
                else item.responseBytes += (event.body || '').length;
            } else if (event.type === 'body_end') {
                if (event.part === 'request') item.requestTruncated = !!event.body_truncated;
                else item.responseTruncated = !!event.body_truncated;
            } else if (event.type === 'response_end' || event.type === 'connection_end') {
                item.complete = event.complete;
                item.duration_ms = event.duration_ms;
                item.error = event.error;
                if (event.bytes_out != null) item.responseBytes = event.bytes_out;
            }
            if (!state.selected) state.selected = idValue;
            scheduleRender();
        }

        function connect() {
            if (state.closed || state.paused) return;
            var base = (window.nps && window.nps.web_base_url) || '';
            var endpoint = base + '/index/' + (state.kind === 'host' ? 'hosttrafficdebug' : 'trafficdebug') + '?id=' + encodeURIComponent(state.id);
            state.source = new EventSource(endpoint);
            state.source.onopen = function () { setStatus(localText('已连接', 'Connected'), 'is-live'); };
            state.source.onmessage = function (message) {
                try { mergeEvent(JSON.parse(message.data)); } catch (error) { setStatus(localText('事件解析失败', 'Invalid event'), 'is-warning'); }
            };
            state.source.onerror = function () {
                if (!state.closed && !state.paused) setStatus(localText('连接中断，正在重连', 'Disconnected, reconnecting'), 'is-warning');
            };
        }

        state.pauseButton.addEventListener('click', function () {
            state.paused = !state.paused;
            if (state.paused) {
                if (state.source) state.source.close();
                state.pauseButton.innerHTML = '<i class="fa fa-play" aria-hidden="true"></i> ' + text(localText('继续', 'Resume'));
                setStatus(localText('已暂停', 'Paused'), 'is-paused');
            } else {
                state.pauseButton.innerHTML = '<i class="fa fa-pause" aria-hidden="true"></i> ' + text(localText('暂停', 'Pause'));
                connect();
            }
        });
        root.querySelector('.traffic-debug-clear').addEventListener('click', function () {
            state.events = {}; state.order = []; state.selected = null; renderList(); renderDetail();
        });
        root.querySelector('.traffic-debug-autoscroll').addEventListener('click', function () {
            state.autoScroll = !state.autoScroll;
            root.querySelector('.traffic-debug-autoscroll').textContent = localText('自动滚动：' + (state.autoScroll ? '开' : '关'), 'Auto-scroll: ' + (state.autoScroll ? 'On' : 'Off'));
        });
        root.querySelector('.traffic-debug-close').addEventListener('click', function () { state.close(); });
        root.addEventListener('click', function (event) { if (event.target === root) state.close(); });
        state.close = function () {
            if (state.closed) return;
            state.closed = true;
            if (state.source) state.source.close();
            if (root.parentNode) root.parentNode.removeChild(root);
            if (active === state) active = null;
        };
        renderList(); renderDetail(); connect();
    }

    window.npsOpenTrafficDebug = npsOpenTrafficDebug;
})(window, document);
