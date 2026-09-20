(function () {
    "use strict";

    var $ = function (selector) { return document.querySelector(selector); };
    var baseURL = (document.body.dataset.baseUrl || "").replace(/\/$/, "");
    var refreshInterval = 5000;
    var state = {
        data: { owners: [], clients: [], resources: [], updatedAt: "" },
        loading: false,
        onlyOnline: false,
        query: "",
        selectedOwner: "",
        selectedClient: "",
        lastError: ""
    };
    var drawTimer = null;
    var MODE_META = {
        tcp: ["tcp", "m-tcp", "TCP"], host: ["域名", "m-host", "HOST"], httpProxy: ["http", "m-http", "HTTP"],
        udp: ["udp", "m-udp", "UDP"], socks5: ["socks", "m-socks", "SOCKS"], p2p: ["p2p", "m-p2p", "P2P"],
        secret: ["secret", "m-secret", "SECRET"], file: ["file", "m-secret", "FILE"]
    };
    var CLIENT_PALETTE = ["#2458e6", "#0e9384", "#d4592a", "#c0268b", "#e8890c", "#0b8fbf", "#64748b", "#7c3aed", "#087443", "#b42318"];
    var USER_PALETTE = ["#2458e6", "#0e9384", "#d4592a", "#8b5cf6", "#c0268b", "#64748b"];

    function number(value) { return Number(value) || 0; }
    function esc(value) {
        return String(value == null ? "" : value).replace(/[&<>"']/g, function (character) {
            return ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", "\"": "&quot;", "'": "&#39;" })[character];
        });
    }
    function element(tag, className, html) {
        var node = document.createElement(tag);
        if (className) node.className = className;
        if (html != null) node.innerHTML = html;
        return node;
    }
    function fmtBytes(value) {
        var bytes = Math.max(0, number(value));
        if (!bytes) return "0 B";
        var units = ["B", "KB", "MB", "GB", "TB", "PB"];
        var index = 0;
        while (bytes >= 1024 && index < units.length - 1) { bytes /= 1024; index += 1; }
        return (bytes >= 100 ? bytes.toFixed(0) : bytes >= 10 ? bytes.toFixed(1) : bytes.toFixed(2)) + " " + units[index];
    }
    function fmtRate(value) { return fmtBytes(value) + "/s"; }
    function fmtDate(value) { return value ? String(value) : "长期"; }
    function fmtEntry(resource) { return resource.entry || "—"; }
    function stateName(resource) {
        return ({ running: "运行中", waiting: "等待客户端", failed: "启动失败", stopped: resource.enabled ? "已停止" : "已停用" })[resource.state] || "未知";
    }
    function resourceName(resource) {
        if (resource.kind === "host") return resource.remark || resource.entry || ("域名规则 #" + resource.id);
        return resource.remark || ((resource.mode || "隧道").toUpperCase() + " 隧道 #" + resource.id);
    }
    function colorFor(value, palette) {
        var hash = 0;
        String(value).split("").forEach(function (character) { hash = ((hash * 31) + character.charCodeAt(0)) >>> 0; });
        return palette[hash % palette.length];
    }
    function clientColor(id) { return colorFor(id, CLIENT_PALETTE); }
    function ownerColor(id) { return colorFor(id, USER_PALETTE); }
    function ownerFor(id) {
        if (!id) return { id: 0, name: "系统·平台", remark: "", system: true, enabled: true };
        return state.data.owners.find(function (owner) { return owner.id === id; }) || { id: id, name: "已删除用户", remark: "", enabled: false };
    }
    function clientFor(id) { return state.data.clients.find(function (client) { return client.id === id; }); }
    function resourcesFor(clientID) { return state.data.resources.filter(function (resource) { return resource.clientId === clientID; }); }

    function normalize(raw) {
        var owners = (raw.owners || []).map(function (owner) {
            return {
                id: number(owner.id), name: owner.name || "未命名用户", remark: owner.remark || "", enabled: Boolean(owner.enabled),
                clientLimit: number(owner.clientLimit), tunnelLimit: number(owner.tunnelLimit), createdAt: owner.createdAt || "", expiresAt: owner.expiresAt || ""
            };
        }).sort(function (left, right) { return left.id - right.id; });
        var clients = (raw.clients || []).map(function (client) {
            return {
                id: number(client.id), ownerId: number(client.ownerId), ownerName: client.ownerName || "", ownerRemark: client.ownerRemark || "",
                remark: client.remark || "", address: client.address || "", localAddress: client.localAddress || "", version: client.version || "",
                enabled: Boolean(client.enabled), online: Boolean(client.online), connections: number(client.connections), connectionLimit: number(client.connectionLimit),
                tunnelLimit: number(client.tunnelLimit), rateLimit: number(client.rateLimit), flowIn: number(client.flowIn), flowOut: number(client.flowOut),
                flowTotal: number(client.flowTotal), flowLimitBytes: number(client.flowLimitBytes), rateIn: number(client.rateIn), rateOut: number(client.rateOut),
                createdAt: client.createdAt || "", lastOnlineAt: client.lastOnlineAt || "", expiresAt: client.expiresAt || ""
            };
        }).sort(function (left, right) { return left.id - right.id; });
        var ownersByID = {};
        owners.forEach(function (owner) { ownersByID[owner.id] = owner; });
        clients.forEach(function (client) {
            if (client.ownerId && !ownersByID[client.ownerId] && client.ownerName) {
                ownersByID[client.ownerId] = { id: client.ownerId, name: client.ownerName, remark: client.ownerRemark, enabled: false, orphan: true };
                owners.push(ownersByID[client.ownerId]);
            }
        });
        return {
            owners: owners.sort(function (left, right) { return left.id - right.id; }),
            clients: clients,
            resources: (raw.resources || []).map(function (resource) {
                return {
                    id: number(resource.id), kind: resource.kind || "tunnel", mode: resource.kind === "host" ? "host" : (resource.mode || "tcp"),
                    clientId: number(resource.clientId), remark: resource.remark || "", enabled: Boolean(resource.enabled), running: Boolean(resource.running),
                    state: resource.state || "stopped", runError: resource.runError || "", entry: resource.entry || "", target: resource.target || "",
                    location: resource.location || "", autoHttps: Boolean(resource.autoHttps), flowIn: number(resource.flowIn), flowOut: number(resource.flowOut),
                    flowTotal: number(resource.flowTotal), flowLimitBytes: number(resource.flowLimitBytes), rateIn: number(resource.rateIn), rateOut: number(resource.rateOut),
                    currentConnections: number(resource.currentConnections), healthCheckURL: resource.healthCheckUrl || ""
                };
            }).sort(function (left, right) { return left.id - right.id; }),
            updatedAt: raw.updatedAt || ""
        };
    }

    function tick() {
        var now = new Date();
        var pad = function (value) { return String(value).padStart(2, "0"); };
        $("#clock").textContent = pad(now.getHours()) + ":" + pad(now.getMinutes()) + ":" + pad(now.getSeconds());
        $("#dateStr").textContent = now.getFullYear() + "-" + pad(now.getMonth() + 1) + "-" + pad(now.getDate()) + " " + ["周日", "周一", "周二", "周三", "周四", "周五", "周六"][now.getDay()];
    }

    function renderStats() {
        var clients = state.data.clients;
        var resources = state.data.resources;
        var online = clients.filter(function (client) { return client.online; }).length;
        var running = resources.filter(function (resource) { return resource.state === "running"; }).length;
        var flow = clients.reduce(function (total, client) { return total + client.flowTotal; }, 0);
        var rate = resources.reduce(function (total, resource) { return total + resource.rateIn + resource.rateOut; }, 0);
        var types = {};
        resources.forEach(function (resource) { types[resource.mode] = (types[resource.mode] || 0) + 1; });
        var typeText = Object.keys(types).map(function (mode) { return (MODE_META[mode] || [mode, "", mode])[2] + " " + types[mode]; }).join(" · ") || "暂无规则";
        $("#gstats").innerHTML =
            '<span class="gs"><em>用户</em><b>' + state.data.owners.length + '</b><i>资源所属账号</i></span><span class="sep"></span>' +
            '<span class="gs"><em>客户端</em><b>' + clients.length + '</b><i>在线 ' + online + ' · 离线 ' + (clients.length - online) + '</i></span><span class="sep"></span>' +
            '<span class="gs"><em>资源规则</em><b class="hl">' + resources.length + '</b><i>运行 ' + running + ' · ' + esc(typeText) + '</i></span><span class="sep"></span>' +
            '<span class="gs"><em>代理速率</em><b>' + fmtRate(rate) + '</b><i>NPS 规则实时合计</i></span><span class="sep"></span>' +
            '<span class="gs"><em>累计流量</em><b>' + fmtBytes(flow) + '</b><i>客户端进出合计</i></span>';
    }

    function renderLegend() {
        var items = [["客户端在线", "dot on"], ["客户端离线", "dot off"]].concat(Object.keys(MODE_META).map(function (mode) {
            return [MODE_META[mode][2] + "规则", "sw " + MODE_META[mode][1]];
        }));
        var updated = state.data.updatedAt ? new Date(state.data.updatedAt).toLocaleTimeString("zh-CN", { hour12: false }) : "等待首个响应";
        var status = state.lastError ? "读取失败，保留上次数据" : "每 5 秒实时读取";
        $("#legend").innerHTML = items.map(function (item) {
            var parts = item[1].split(" ");
            var marker = parts[0] === "dot" ? '<span class="dot ' + parts[1] + '"></span>' : '<span class="sw ' + parts[1] + '"></span>';
            return '<span class="lg">' + marker + esc(item[0]) + '</span>';
        }).join("") + '<span class="right">' + esc(status) + ' · 最近更新 ' + esc(updated) + ' · 点击卡片查看详情</span>';
    }

    function renderFilters() {
        var ownerSelect = $("#fUser");
        var clientSelect = $("#fClient");
        var selectedOwner = state.selectedOwner;
        ownerSelect.innerHTML = '<option value="">全部用户</option>';
        if (state.data.clients.some(function (client) { return client.ownerId === 0; })) {
            ownerSelect.innerHTML += '<option value="0">系统·平台</option>';
        }
        state.data.owners.forEach(function (owner) {
            ownerSelect.innerHTML += '<option value="' + owner.id + '">' + esc(owner.name) + (owner.remark ? "（" + esc(owner.remark) + "）" : "") + '</option>';
        });
        if (Array.prototype.some.call(ownerSelect.options, function (option) { return option.value === selectedOwner; })) ownerSelect.value = selectedOwner;
        else state.selectedOwner = ownerSelect.value;

        var selectedClient = state.selectedClient;
        clientSelect.innerHTML = '<option value="">全部客户端</option>';
        state.data.clients.filter(function (client) {
            return !state.selectedOwner || String(client.ownerId) === state.selectedOwner;
        }).forEach(function (client) {
            clientSelect.innerHTML += '<option value="' + client.id + '">' + esc(client.remark || ("客户端 #" + client.id)) + (client.online ? "" : " ·离线") + '</option>';
        });
        if (Array.prototype.some.call(clientSelect.options, function (option) { return option.value === selectedClient; })) clientSelect.value = selectedClient;
        else state.selectedClient = clientSelect.value;
    }

    function resourceMatches(resource) {
        var client = clientFor(resource.clientId);
        if (!client) return false;
        if (state.selectedOwner && String(client.ownerId) !== state.selectedOwner) return false;
        if (state.selectedClient && String(client.id) !== state.selectedClient) return false;
        if (state.onlyOnline && !client.online) return false;
        if (!state.query) return true;
        var owner = ownerFor(client.ownerId);
        var text = [resourceName(resource), resource.entry, resource.target, resource.location, resource.id, client.remark, client.address, client.localAddress, owner.name, owner.remark].join(" ").toLowerCase();
        return text.indexOf(state.query) !== -1;
    }

    function connectionText(resource) {
        return resource.kind === "tunnel" ? "连接 " + resource.currentConnections : "连接 —";
    }

    function resourceCard(resource) {
        var client = clientFor(resource.clientId);
        var owner = ownerFor(client.ownerId);
        var meta = MODE_META[resource.mode] || [resource.mode, "m-secret", String(resource.mode).toUpperCase()];
        var node = element("button", "t-node" + (resource.state === "running" ? "" : " stop"));
        node.type = "button";
        node.dataset.resource = String(resource.id);
        node.dataset.owner = String(owner.id);
        node.dataset.client = String(client.id);
        node.style.borderLeftColor = clientColor(client.id);
        node.title = meta[2] + " #" + resource.id + "\n状态: " + stateName(resource) + "\n入口: " + fmtEntry(resource) + "\n目标: " + (resource.target || "—");
        node.innerHTML = (resource.state === "running" ? "" : '<span class="t-st">停</span>') +
            '<span class="t-row1"><span class="mode ' + meta[1] + '">' + esc(meta[0]) + '</span><span class="t-name">' + esc(resourceName(resource)) + '<span class="tid">#' + resource.id + '</span></span><span class="t-flow">' + fmtBytes(resource.flowTotal) + '</span></span>' +
            '<span class="t-route">' + esc(fmtEntry(resource)) + " → " + esc(resource.target || "—") + '</span>' +
            '<span class="t-live">' + (resource.state === "running" ? '<i class="pulse"></i>' : "") + esc(stateName(resource)) + " · " + esc(fmtRate(resource.rateIn + resource.rateOut)) + " · " + esc(connectionText(resource)) + '</span>' +
            '<span class="aff"><span class="udot" style="background:' + ownerColor(owner.id) + '"></span><span class="un">' + esc(owner.name) + '</span><span class="sep">·</span><span class="cdot" style="background:' + clientColor(client.id) + '"></span><span class="cn">' + esc(client.remark || ("#" + client.id)) + '</span></span>';
        node.addEventListener("click", function () { openResource(resource.id); });
        return node;
    }

    function renderMap() {
        var cols = $("#cols");
        Array.prototype.slice.call(cols.querySelectorAll(".grid, .group-frame")).forEach(function (node) { node.remove(); });
        var grid = element("div", "grid");
        var resources = state.data.resources.filter(resourceMatches);
        resources.sort(function (left, right) {
            var leftClient = clientFor(left.clientId);
            var rightClient = clientFor(right.clientId);
            return leftClient.ownerId - rightClient.ownerId || left.clientId - right.clientId || left.id - right.id;
        });
        resources.forEach(function (resource, index) {
            if (index > 0) {
                var previous = resources[index - 1];
                var previousClient = clientFor(previous.clientId);
                var currentClient = clientFor(resource.clientId);
                if (previousClient.ownerId !== currentClient.ownerId) {
                    var divider = element("div", "rowbrk");
                    divider.style.gridColumn = "1 / -1";
                    grid.appendChild(divider);
                }
            }
            grid.appendChild(resourceCard(resource));
        });
        if (!resources.length) grid.appendChild(element("div", "empty-state", "没有符合筛选条件的资源规则"));
        cols.appendChild(grid);
        window.setTimeout(drawOwnerFrames, 0);
    }

    function drawOwnerFrames() {
        Array.prototype.slice.call(document.querySelectorAll(".group-frame")).forEach(function (node) { node.remove(); });
        var grid = document.querySelector(".grid");
        if (!grid) return;
        var gridBox = grid.getBoundingClientRect();
        var groups = {};
        var order = [];
        Array.prototype.slice.call(grid.querySelectorAll(".t-node")).forEach(function (node) {
            var ownerID = node.dataset.owner;
            if (!groups[ownerID]) { groups[ownerID] = []; order.push(ownerID); }
            groups[ownerID].push(node);
        });
        order.forEach(function (ownerID) {
            var cards = groups[ownerID];
            if (!cards.length) return;
            var left = Infinity, top = Infinity, right = -Infinity, bottom = -Infinity;
            cards.forEach(function (card) {
                var box = card.getBoundingClientRect();
                left = Math.min(left, box.left); top = Math.min(top, box.top); right = Math.max(right, box.right); bottom = Math.max(bottom, box.bottom);
            });
            var owner = ownerFor(number(ownerID));
            var color = ownerColor(owner.id);
            var frame = element("div", "group-frame");
            frame.style.left = (left - gridBox.left - 8) + "px";
            frame.style.top = (top - gridBox.top - 34) + "px";
            frame.style.width = (right - left + 16) + "px";
            frame.style.height = (bottom - top + 42) + "px";
            frame.style.borderColor = color;
            var clients = Array.prototype.filter.call(state.data.clients, function (client) {
                return client.ownerId === owner.id && cards.some(function (card) { return number(card.dataset.client) === client.id; });
            });
            var labels = clients.map(function (client) {
                return '<span class="clabel' + (client.online ? "" : " offline") + '" style="border-color:' + clientColor(client.id) + ";color:" + clientColor(client.id) + '"><i style="background:' + clientColor(client.id) + '"></i><b>' + esc(client.remark || ("#" + client.id)) + '</b>' + (client.version ? "<em>v" + esc(client.version) + "</em>" : "") + "</span>";
            }).join("");
            frame.innerHTML = '<span class="topbar"><span class="glabel" style="color:' + color + ";border-color:" + color + '">' + esc(owner.name) + " · " + cards.length + " 条规则</span>" + labels + "</span>";
            grid.appendChild(frame);
        });
    }

    function row(label, value) { return '<div class="k">' + esc(label) + '</div><div class="v mono">' + esc(value || "—") + "</div>"; }
    function openResource(id) {
        var resource = state.data.resources.find(function (item) { return item.id === id; });
        if (!resource) return;
        var client = clientFor(resource.clientId);
        var owner = ownerFor(client.ownerId);
        var meta = MODE_META[resource.mode] || [resource.mode, "m-secret", String(resource.mode).toUpperCase()];
        $("#mMode").className = "mode " + meta[1];
        $("#mMode").textContent = meta[2];
        $("#mTitle").textContent = resourceName(resource) + " #" + resource.id;
        var features = [];
        if (resource.kind === "host" && resource.autoHttps) features.push("自动 HTTPS");
        if (resource.location && resource.location !== "/") features.push("路径：" + resource.location);
        if (resource.healthCheckURL) features.push("健康检查");
        $("#mBody").innerHTML =
            '<div class="live-box"><div class="live-cell"><div class="lk">实时代理速率</div><div class="lv">' + esc(fmtRate(resource.rateIn + resource.rateOut)) + '</div></div><div class="live-cell"><div class="lk">当前代理连接</div><div class="lv">' + esc(resource.kind === "tunnel" ? String(resource.currentConnections) : "—") + '</div></div></div>' +
            '<div class="section-t">所属用户</div><div class="detail-grid">' + row("用户", owner.name) + row("备注", owner.remark) + row("账号状态", owner.enabled ? "启用" : "停用") + row("账号到期", fmtDate(owner.expiresAt)) + '</div>' +
            '<div class="section-t">客户端</div><div class="detail-grid">' + row("客户端 ID", "#" + client.id) + row("客户端状态", client.online ? "在线" : (client.enabled ? "离线" : "已停用")) + row("公网来源", client.address) + row("内网地址", client.localAddress) + row("版本", client.version) + row("当前连接", client.connections + (client.connectionLimit ? " / 上限 " + client.connectionLimit : "")) + '</div>' +
            '<div class="section-t">资源规则</div><div class="detail-grid">' + row("类型", meta[2]) + row("运行状态", stateName(resource)) + row("公网入口", fmtEntry(resource)) + row("转发目标", resource.target) + row("累计入口", fmtBytes(resource.flowIn)) + row("累计出口", fmtBytes(resource.flowOut)) + row("流量上限", resource.flowLimitBytes ? fmtBytes(resource.flowLimitBytes) : "不限") + (resource.runError ? row("启动错误", resource.runError) : "") + '</div>' +
            (features.length ? '<div class="section-t">特性</div><div class="tagrow">' + features.map(function (feature) { return '<span class="tag">' + esc(feature) + "</span>"; }).join("") + "</div>" : "") +
            (resource.healthCheckURL ? '<div class="section-t">健康检查</div><div class="detail-grid">' + row("探测地址", resource.healthCheckURL) + "</div>" : "");
        $("#modalMask").classList.add("open");
    }

    function closeModal() { $("#modalMask").classList.remove("open"); }
    function bindControls() {
        $("#q").addEventListener("input", function (event) { state.query = event.target.value.trim().toLowerCase(); renderMap(); });
        $("#onlyOnline").addEventListener("change", function (event) { state.onlyOnline = event.target.checked; renderMap(); });
        $("#fUser").addEventListener("change", function (event) { state.selectedOwner = event.target.value; state.selectedClient = ""; renderFilters(); renderMap(); });
        $("#fClient").addEventListener("change", function (event) { state.selectedClient = event.target.value; renderMap(); });
        $("#mClose").addEventListener("click", closeModal);
        $("#modalMask").addEventListener("click", function (event) { if (event.target === $("#modalMask")) closeModal(); });
        document.addEventListener("keydown", function (event) { if (event.key === "Escape") closeModal(); });
        window.addEventListener("resize", function () { clearTimeout(drawTimer); drawTimer = window.setTimeout(drawOwnerFrames, 100); });
        $("#mapBody").addEventListener("scroll", function () { clearTimeout(drawTimer); drawTimer = window.setTimeout(drawOwnerFrames, 100); });
    }

    function render() {
        renderStats();
        renderFilters();
        renderMap();
        renderLegend();
    }
    function refresh() {
        if (state.loading) return;
        state.loading = true;
        fetch(baseURL + "/overview/data?_=" + Date.now(), { credentials: "same-origin", cache: "no-store", headers: { Accept: "application/json" } })
            .then(function (response) { if (!response.ok) throw new Error("HTTP " + response.status); return response.json(); })
            .then(function (payload) {
                if (!payload || payload.status !== 1 || !payload.data) throw new Error("响应格式无效");
                state.data = normalize(payload.data);
                state.lastError = "";
                render();
            })
            .catch(function (error) {
                state.lastError = error && error.message ? error.message : "请求失败";
                renderLegend();
            })
            .finally(function () { state.loading = false; });
    }

    bindControls();
    tick();
    window.setInterval(tick, 1000);
    refresh();
    window.setInterval(refresh, refreshInterval);
}());
