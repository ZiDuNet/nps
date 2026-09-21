(function () {
    "use strict";

    var $ = function (selector) { return document.querySelector(selector); };
    var body = document.body;
    var baseURL = (body.dataset.baseUrl || "").replace(/\/$/, "");
    var accountName = body.dataset.accountName || "当前用户";
    var DEFAULT_REFRESH_SECONDS = 60;
    var MIN_REFRESH_SECONDS = 5;
    var MAX_REFRESH_SECONDS = 3600;
    var REFRESH_STORAGE_KEY = "nps-overview-refresh-seconds";
    var refreshSeconds = loadRefreshSeconds();
    var refreshTimer = null;
    var state = { data: null, loading: false, onlyOnline: false, collapsedClients: new Set() };
    var linkPaths = [];
    var drawTimer = null;
    var MODE_META = {
        tcp: ["tcp", "m-tcp", "TCP"], host: ["域名", "m-host", "HOST"], httpProxy: ["http", "m-http", "HTTP"],
        udp: ["udp", "m-udp", "UDP"], socks5: ["socks", "m-socks", "SOCKS"], p2p: ["p2p", "m-p2p", "P2P"],
        secret: ["secret", "m-secret", "SECRET"], file: ["file", "m-file", "FILE"]
    };

    function clampRefreshSeconds(value) {
        var seconds = Number.parseInt(value, 10);
        if (!Number.isFinite(seconds)) return DEFAULT_REFRESH_SECONDS;
        return Math.min(MAX_REFRESH_SECONDS, Math.max(MIN_REFRESH_SECONDS, seconds));
    }
    function loadRefreshSeconds() {
        try { return clampRefreshSeconds(window.localStorage.getItem(REFRESH_STORAGE_KEY)); } catch (error) { return DEFAULT_REFRESH_SECONDS; }
    }
    function saveRefreshSeconds() {
        try { window.localStorage.setItem(REFRESH_STORAGE_KEY, String(refreshSeconds)); } catch (error) { /* 本地存储不可用时仍保持当前页面设置 */ }
    }
    function refreshLabel() { return "每 " + refreshSeconds + " 秒"; }
    function scheduleRefresh() {
        if (refreshTimer !== null) window.clearTimeout(refreshTimer);
        refreshTimer = window.setTimeout(function () {
            loadData();
            scheduleRefresh();
        }, refreshSeconds * 1000);
    }
    function setRefreshSeconds(value) {
        refreshSeconds = clampRefreshSeconds(value);
        var input = $("#refreshSeconds");
        if (input) input.value = String(refreshSeconds);
        saveRefreshSeconds();
        scheduleRefresh();
        renderLegend();
        if (state.data) {
            var updated = state.data.updatedAt ? new Date(state.data.updatedAt).toLocaleTimeString("zh-CN", { hour12: false }) : "刚刚";
            $("#topStatus").textContent = "更新于 " + updated + " · " + refreshLabel() + "刷新";
        }
    }
    function bindRefreshControl() {
        var input = $("#refreshSeconds");
        if (!input) return;
        input.value = String(refreshSeconds);
        input.addEventListener("change", function () { setRefreshSeconds(input.value); });
        input.addEventListener("keydown", function (event) {
            if (event.key === "Enter") { event.preventDefault(); input.blur(); }
        });
    }

    function esc(value) {
        return String(value == null ? "" : value).replace(/[&<>"']/g, function (character) {
            return ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", "\"": "&quot;", "'": "&#39;" })[character];
        });
    }

    function fmtBytes(value) {
        var bytes = Math.max(0, Number(value) || 0);
        if (!bytes) return "0 B";
        var units = ["B", "KB", "MB", "GB", "TB", "PB"];
        var index = 0;
        while (bytes >= 1024 && index < units.length - 1) { bytes /= 1024; index += 1; }
        return (bytes >= 100 ? bytes.toFixed(0) : bytes >= 10 ? bytes.toFixed(1) : bytes.toFixed(2)) + " " + units[index];
    }

    function fmtRate(value) { return fmtBytes(value) + "/s"; }
    function fmtShort(value) { return value ? String(value).slice(5, 16) : "--"; }
    function fmtDate(value) { return value ? String(value).slice(0, 10) : "长期"; }
    function fmtEntry(resource) { return resource.entry || "—"; }
    function number(value) { return Number(value) || 0; }

    function colorFor(value) {
        var palette = ["#2458e6", "#0e9384", "#d4592a", "#8b5cf6", "#c0268b", "#64748b"];
        var hash = 0;
        String(value).split("").forEach(function (character) { hash = ((hash * 31) + character.charCodeAt(0)) >>> 0; });
        return palette[hash % palette.length];
    }

    function initials(value) {
        var text = String(value || "NPS").trim();
        return text.slice(0, 2).toUpperCase() || "NPS";
    }

    function stateName(resource) {
        return ({ running: "运行中", waiting: "等待客户端", failed: "启动失败", stopped: resource.enabled ? "已停止" : "已停用" })[resource.state] || "未知";
    }

    function resourceName(resource) {
        if (resource.kind === "host") return resource.remark || resource.entry || ("域名规则 #" + resource.id);
        return resource.remark || ((resource.mode || "隧道").toUpperCase() + " 隧道 #" + resource.id);
    }

    function normalize(raw) {
        var clients = (raw.clients || []).map(function (client) {
            return {
                id: number(client.id), remark: client.remark || "", addr: client.address || "", localAddr: client.localAddress || "",
                version: client.version || "", online: Boolean(client.online), enabled: Boolean(client.enabled),
                nowConn: number(client.connections), maxConn: number(client.connectionLimit), maxTunnelNum: number(client.tunnelLimit),
                flow: number(client.flowTotal), flowInlet: number(client.flowIn), flowOut: number(client.flowOut),
                flowLimit: number(client.flowLimitBytes), rateIn: number(client.rateIn), rateOut: number(client.rateOut),
                createTime: client.createdAt || "", lastOnline: client.lastOnlineAt || "", expire: client.expiresAt || ""
            };
        });
        var tunnels = (raw.resources || []).map(function (resource) {
            return {
                id: number(resource.id), clientId: number(resource.clientId), kind: resource.kind || "tunnel",
                mode: resource.kind === "host" ? "host" : (resource.mode || "tcp"), remark: resource.remark || "",
                enabled: Boolean(resource.enabled), running: resource.state === "running", state: resource.state || "stopped",
                runError: resource.runError || "", entry: resource.entry || "", target: resource.target || "", location: resource.location || "",
                autoHttps: Boolean(resource.autoHttps), flow: number(resource.flowTotal), flowInlet: number(resource.flowIn),
                flowOut: number(resource.flowOut), flowLimit: number(resource.flowLimitBytes), rateIn: number(resource.rateIn),
                rateOut: number(resource.rateOut), currentConnections: number(resource.currentConnections), healthUrl: resource.healthCheckUrl || ""
            };
        });
        return { clients: clients, tunnels: tunnels, updatedAt: raw.updatedAt || "" };
    }

    function tunnelsOf(clientID) {
        return state.data.tunnels.filter(function (tunnel) { return tunnel.clientId === clientID; });
    }

    function tick() {
        var now = new Date();
        var pad = function (value) { return String(value).padStart(2, "0"); };
        $("#clock").textContent = pad(now.getHours()) + ":" + pad(now.getMinutes()) + ":" + pad(now.getSeconds());
        $("#dateStr").textContent = now.getFullYear() + "-" + pad(now.getMonth() + 1) + "-" + pad(now.getDate()) + " " + ["周日", "周一", "周二", "周三", "周四", "周五", "周六"][now.getDay()];
    }

    function renderAcct() {
        var clients = state.data.clients;
        var tunnels = state.data.tunnels;
        var online = clients.filter(function (client) { return client.online; }).length;
        var running = tunnels.filter(function (tunnel) { return tunnel.running; }).length;
        var flow = clients.reduce(function (total, client) { return total + client.flow; }, 0);
        var rate = clients.reduce(function (total, client) { return total + client.rateIn + client.rateOut; }, 0);
        $("#acct").innerHTML =
            '<span class="av" style="background:' + colorFor(accountName) + '">' + esc(initials(accountName)) + '</span>' +
            '<span class="a"><b>' + esc(accountName) + '</b><span class="k">当前登录账号</span></span>' +
            '<span class="a"><span class="k">客户端</span><b>' + clients.length + '</b><span class="k">在线 ' + online + '</span></span>' +
            '<span class="a"><span class="k">资源规则</span><b>' + tunnels.length + '</b><span class="k">运行 ' + running + '</span></span>' +
            '<span class="a"><span class="k">累计流量</span><b>' + fmtBytes(flow) + '</b></span>' +
            '<span class="a"><span class="k">当前速率</span><b>' + fmtRate(rate) + '</b></span>';
    }

    function renderLegend() {
        var items = [["客户端在线", "dot on"], ["客户端离线", "dot off"]].concat(Object.keys(MODE_META).map(function (mode) {
            return [MODE_META[mode][2] + "隧道", "sw " + MODE_META[mode][1]];
        }));
        $("#legend").innerHTML = items.map(function (item) {
            var marker = item[1].indexOf("dot") === 0 ? '<span class="dot ' + item[1].split(" ")[1] + '"></span>' : '<span class="sw ' + item[1].split(" ")[1] + '"></span>';
            return '<span class="lg">' + marker + esc(item[0]) + '</span>';
        }).join("") + '<span class="right">' + esc(refreshLabel()) + '刷新 · 点击客户端收起或展开规则 · 点击规则查看详情</span>';
    }

    function renderMap() {
        var cols = $("#cols");
        Array.prototype.slice.call(cols.querySelectorAll(".band")).forEach(function (node) { node.remove(); });
        var band = document.createElement("div");
        band.className = "band";
        var clients = state.data.clients.filter(function (client) { return !state.onlyOnline || client.online; }).sort(function (left, right) {
            return Number(right.online) - Number(left.online) || right.flow - left.flow;
        });
        if (!clients.length) {
            band.innerHTML = '<div class="empty-note">当前账号没有符合条件的客户端</div>';
            cols.appendChild(band);
            drawLinks();
            return;
        }
        clients.forEach(function (client) {
            var resources = tunnelsOf(client.id);
            var block = document.createElement("div");
            block.className = "client-block";
            var clientNode = document.createElement("div");
            clientNode.className = "c-node" + (client.online ? "" : " off");
            clientNode.dataset.client = client.id;
            clientNode.title = "客户端 #" + client.id + "\n来源 " + (client.addr || "—") + " · 内网 " + (client.localAddr || "—") + "\n版本 " + (client.version || "—") + "\n点击查看详情";
            var version = client.version ? '<span class="c-ver">v' + esc(client.version) + '</span>' : "";
            var connections = client.nowConn ? '<span class="c-conn">' + client.nowConn + ' 连接</span>' : "";
            clientNode.innerHTML =
                '<div class="c-row"><span class="dot ' + (client.online ? "on" : "off") + '"></span>' +
                '<span class="c-name">' + esc(client.remark || "(未命名)") + '</span>' + version + connections + '<span class="c-badge">' + resources.length + ' 规则</span></div>' +
                '<div class="c-sub"><span class="lab">来源</span>' + esc(client.addr || "—") + (client.localAddr ? '<span class="sep">·</span><span class="lab">内网</span>' + esc(client.localAddr) : "") + '</div>' +
                '<div class="c-meta">#' + client.id + ' · 流量 <span class="fl">' + fmtBytes(client.flow) + '</span> · 最近在线 ' + esc(fmtShort(client.lastOnline)) + ' · 创建 ' + esc(fmtShort(client.createTime)) + '</div>';
            clientNode.addEventListener("click", function () { openClient(client.id); });
            block.appendChild(clientNode);

            if (resources.length && !state.collapsedClients.has(client.id)) {
                var group = document.createElement("div");
                group.className = "t-group";
                resources.forEach(function (resource) {
                    var node = document.createElement("div");
                    node.className = "t-node" + (resource.running ? "" : " stop");
                    node.dataset.tunnel = resource.id;
                    node.dataset.owner = "c" + client.id;
                    var meta = MODE_META[resource.mode] || [resource.mode, "m-secret", String(resource.mode).toUpperCase()];
                    node.title = meta[2] + " #" + resource.id + "\n入口: " + fmtEntry(resource) + "\n目标: " + (resource.target || "—") + "\n" + stateName(resource);
                    node.style.borderLeft = "3px solid " + colorFor(client.id);
                    node.innerHTML = (resource.running ? "" : '<span class="t-st">停</span>') +
                        '<span class="mode ' + meta[1] + '">' + esc(meta[0]) + '</span>' +
                        '<span class="t-main"><span class="t-name">' + esc(resourceName(resource)) + '<span class="tid">#' + resource.id + '</span></span>' +
                        '<span class="t-route">' + esc(fmtEntry(resource)) + ' → ' + esc(resource.target || "—") + '</span>' +
                        '<span class="t-live">' + (resource.running ? '<i class="pulse"></i>' : "") + esc(fmtRate(resource.rateIn + resource.rateOut)) + ' · 连接 ' + resource.currentConnections + '</span></span>' +
                        '<span class="t-flow">' + fmtBytes(resource.flow) + '</span>';
                    node.addEventListener("click", function (event) { event.stopPropagation(); openResource(resource.id); });
                    group.appendChild(node);
                });
                block.appendChild(group);
            } else if (resources.length) {
                var collapsedNote = document.createElement("div");
                collapsedNote.className = "empty-note";
                collapsedNote.textContent = "点击客户端卡片展开 " + resources.length + " 条规则";
                block.appendChild(collapsedNote);
            } else {
                var emptyNote = document.createElement("div");
                emptyNote.className = "empty-note";
                emptyNote.textContent = "该客户端暂无转发规则";
                block.appendChild(emptyNote);
            }
            band.appendChild(block);
        });
        cols.appendChild(band);
        drawLinks();
    }

    function drawLinks() {
        var svg = $("#links");
        var cols = $("#cols");
        linkPaths.forEach(function (path) { path.remove(); });
        linkPaths = [];
        var clientNodes = Array.prototype.slice.call(cols.querySelectorAll(".c-node"));
        var tunnelNodes = Array.prototype.slice.call(cols.querySelectorAll(".t-node"));
        function rect(node) {
            var item = node.getBoundingClientRect();
            var root = cols.getBoundingClientRect();
            return { x: item.left - root.left, y: item.top - root.top, w: item.width, h: item.height };
        }
        clientNodes.forEach(function (clientNode) {
            var clientID = Number(clientNode.dataset.client);
            var source = rect(clientNode);
            tunnelNodes.filter(function (node) { return node.dataset.owner === "c" + clientID; }).forEach(function (tunnelNode) {
                var target = rect(tunnelNode);
                var middle = (source.x + source.w / 2 + target.x + target.w / 2) / 2;
                var path = document.createElementNS("http://www.w3.org/2000/svg", "path");
                path.setAttribute("d", "M " + (source.x + source.w / 2) + " " + (source.y + source.h) + " C " + middle + " " + (source.y + source.h) + ", " + middle + " " + target.y + ", " + (target.x + target.w / 2) + " " + target.y);
                path.setAttribute("class", "link");
                svg.appendChild(path);
                linkPaths.push(path);
            });
        });
    }

    function scheduleDraw() { clearTimeout(drawTimer); drawTimer = setTimeout(drawLinks, 120); }

    function applyFilters() {
        var query = $("#q").value.trim().toLowerCase();
        Array.prototype.slice.call(document.querySelectorAll(".c-node")).forEach(function (node) {
            var client = state.data.clients.find(function (item) { return item.id === Number(node.dataset.client); });
            var text = [client.remark, client.addr, client.localAddr, client.id].join(" ").toLowerCase();
            node.classList.toggle("dimfade", Boolean(query) && text.indexOf(query) === -1);
        });
        Array.prototype.slice.call(document.querySelectorAll(".t-node")).forEach(function (node) {
            var resource = state.data.tunnels.find(function (item) { return item.id === Number(node.dataset.tunnel); });
            var text = [resourceName(resource), resource.entry, resource.target, resource.location, resource.id].join(" ").toLowerCase();
            node.classList.toggle("dimfade", Boolean(query) && text.indexOf(query) === -1);
        });
        scheduleDraw();
    }

    function row(label, value) { return '<div class="k">' + esc(label) + '</div><div class="v mono">' + esc(value || "—") + '</div>'; }

    function openResource(id) {
        var resource = state.data.tunnels.find(function (item) { return item.id === id; });
        if (!resource) return;
        var client = state.data.clients.find(function (item) { return item.id === resource.clientId; });
        var meta = MODE_META[resource.mode] || [resource.mode, "m-secret", String(resource.mode).toUpperCase()];
        $("#mMode").className = "mode " + meta[1];
        $("#mMode").textContent = meta[0];
        $("#mTitle").textContent = resourceName(resource) + " #" + resource.id;
        var features = [];
        if (resource.kind === "host") {
            if (resource.autoHttps) features.push("自动 HTTPS");
            if (resource.location) features.push("路径：" + resource.location);
        }
        if (resource.healthUrl) features.push("健康检查");
        $("#mBody").innerHTML =
            '<div class="live-box"><div class="live-cell"><div class="lk">实时网速</div><div class="lv">' + fmtRate(resource.rateIn + resource.rateOut) + '</div></div><div class="live-cell"><div class="lk">当前连接数</div><div class="lv">' + resource.currentConnections + '</div></div></div>' +
            '<div class="section-t">基本信息</div><div class="detail-grid">' +
            row("资源 ID", "#" + resource.id) + row("类型", meta[2]) + row("状态", stateName(resource)) + row("公网入口", fmtEntry(resource)) + row("内网目标", resource.target) +
            (resource.location ? row("匹配路径", resource.location) : "") + '</div>' +
            '<div class="section-t">归属与流量</div><div class="detail-grid">' +
            row("所属客户端", client ? ((client.remark || "(未命名)") + " #" + client.id + (client.online ? " · 在线" : " · 离线")) : "—") + row("所属账号", accountName) +
            row("累计出口流量", fmtBytes(resource.flowOut)) + row("累计入口流量", fmtBytes(resource.flowInlet)) + row("流量上限", resource.flowLimit ? fmtBytes(resource.flowLimit) : "不限") + '</div>' +
            (features.length ? '<div class="section-t">特性</div><div class="tagrow">' + features.map(function (feature) { return '<span class="tag">' + esc(feature) + '</span>'; }).join("") + '</div>' : "") +
            (resource.healthUrl ? '<div class="section-t">健康检查</div><div class="detail-grid">' + row("探测地址", resource.healthUrl) + '</div>' : "") +
            (resource.runError ? '<div class="section-t">运行错误</div><div class="detail-grid">' + row("错误", resource.runError) + '</div>' : "");
        openModal();
    }

    function openClient(id) {
        var client = state.data.clients.find(function (item) { return item.id === id; });
        if (!client) return;
        var resources = tunnelsOf(id);
        $("#mMode").className = "mode m-secret";
        $("#mMode").textContent = "CLIENT";
        $("#mTitle").textContent = (client.remark || "(未命名客户端)") + " #" + client.id;
        $("#mBody").innerHTML =
            '<div class="section-t">账号信息</div><div class="detail-grid">' + row("账号", accountName) + row("资源规则", resources.length) + '</div>' +
            '<div class="section-t">客户端信息</div><div class="detail-grid">' +
            row("客户端 ID", "#" + client.id) + row("状态", client.online ? "在线" : "离线") + row("公网来源 IP", client.addr) + row("内网地址", client.localAddr) +
            row("NPS 版本", client.version) + row("当前连接", client.nowConn + (client.maxConn ? " / 上限 " + client.maxConn : "")) + row("累计流量", fmtBytes(client.flow)) +
            row("创建时间", client.createTime) + row("最近在线", client.lastOnline) + row("到期", fmtDate(client.expire)) + '</div>' +
            '<div class="section-t">该客户端的 ' + resources.length + ' 条规则</div><div class="tagrow">' +
            (resources.map(function (resource) { var meta = MODE_META[resource.mode] || [resource.mode, "m-secret", resource.mode]; return '<span class="tag">' + esc(meta[2] + " · " + resourceName(resource)) + '</span>'; }).join("") || '<span class="tag gray">无规则</span>') + '</div>';
        openModal();
    }

    function openModal() { $("#modalMask").hidden = false; $("#modalMask").classList.add("open"); $("#mClose").focus(); }
    function closeModal() { $("#modalMask").classList.remove("open"); $("#modalMask").hidden = true; }

    function renderAll() {
        $("#who").textContent = "当前账号：" + accountName;
        renderAcct();
        renderMap();
        applyFilters();
    }

    function redirectToLogin() { window.location.assign(baseURL + "/login/index"); }

    function loadData() {
        if (state.loading || document.hidden) return;
        state.loading = true;
        fetch(baseURL + "/overview/data?_=" + Date.now(), { credentials: "same-origin", cache: "no-store", headers: { Accept: "application/json" } })
            .then(function (response) {
                var type = response.headers.get("content-type") || "";
                if (response.redirected || response.status === 401 || response.status === 403 || type.indexOf("application/json") === -1) {
                    redirectToLogin();
                    throw new Error("登录状态已失效");
                }
                if (!response.ok) throw new Error("服务器返回 " + response.status);
                return response.json();
            })
            .then(function (payload) {
                if (!payload || payload.status !== 1 || !payload.data) throw new Error("实时数据格式无效");
                state.data = normalize(payload.data);
                renderAll();
                $("#topStatus").textContent = "更新于 " + (state.data.updatedAt ? new Date(state.data.updatedAt).toLocaleTimeString("zh-CN", { hour12: false }) : "刚刚") + " · " + refreshLabel() + "刷新";
                $("#liveStatus").textContent = "已更新当前账号的资源拓扑。";
            })
            .catch(function (error) {
                if (error && error.message === "登录状态已失效") return;
                $("#topStatus").textContent = "实时数据读取失败";
                $("#liveStatus").textContent = "实时数据读取失败。";
                if (!state.data) $("#cols").innerHTML = '<div class="empty-note">暂时无法读取资源数据，请稍后重试。</div><svg class="links" id="links" aria-hidden="true"></svg>';
            })
            .finally(function () { state.loading = false; });
    }

    $("#q").addEventListener("input", applyFilters);
    $("#onlyOnline").addEventListener("change", function (event) { state.onlyOnline = event.target.checked; renderMap(); applyFilters(); });
    $("#segTunnels").addEventListener("click", function (event) {
        var button = event.target.closest("button");
        if (!button || !state.data) return;
        Array.prototype.slice.call($("#segTunnels").querySelectorAll("button")).forEach(function (item) { item.classList.remove("on"); });
        button.classList.add("on");
        if (button.dataset.mode === "off") state.data.clients.forEach(function (client) { if (tunnelsOf(client.id).length) state.collapsedClients.add(client.id); });
        else state.collapsedClients.clear();
        renderMap();
        applyFilters();
    });
    $("#mClose").addEventListener("click", closeModal);
    $("#modalMask").addEventListener("click", function (event) { if (event.target === $("#modalMask")) closeModal(); });
    document.addEventListener("keydown", function (event) { if (event.key === "Escape") closeModal(); });
    window.addEventListener("resize", scheduleDraw);
    $("#mapBody").addEventListener("scroll", scheduleDraw);
    document.addEventListener("visibilitychange", function () { if (!document.hidden) loadData(); });
    bindRefreshControl();

    renderLegend();
    tick();
    window.setInterval(tick, 1000);
    loadData();
    scheduleRefresh();
}());
