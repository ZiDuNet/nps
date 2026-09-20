(function () {
    "use strict";

    var body = document.body;
    var baseURL = (body.dataset.baseUrl || "").replace(/\/$/, "");
    var accountName = body.dataset.accountName || "当前用户";
    var refreshInterval = 5000;
    var state = {
        data: null,
        loading: false,
        paused: false,
        query: "",
        onlineOnly: false,
        collapsed: new Set()
    };

    var elements = {
        updated: document.getElementById("overview-updated"),
        clock: document.getElementById("overview-clock"),
        refresh: document.getElementById("overview-refresh"),
        pause: document.getElementById("overview-pause"),
        summary: document.getElementById("overview-summary"),
        search: document.getElementById("overview-search"),
        onlineOnly: document.getElementById("overview-online-only"),
        collapse: document.getElementById("overview-collapse"),
        notice: document.getElementById("overview-notice"),
        topology: document.getElementById("overview-topology"),
        detail: document.getElementById("overview-detail"),
        detailTitle: document.getElementById("overview-detail-title"),
        detailKind: document.getElementById("overview-detail-kind"),
        detailList: document.getElementById("overview-detail-list"),
        detailClose: document.getElementById("overview-detail-close"),
        liveStatus: document.getElementById("overview-live-status")
    };

    function createElement(tag, className, text) {
        var node = document.createElement(tag);
        if (className) {
            node.className = className;
        }
        if (text !== undefined && text !== null) {
            node.textContent = String(text);
        }
        return node;
    }

    function icon(name) {
        var node = createElement("i", "fa " + name);
        node.setAttribute("aria-hidden", "true");
        return node;
    }

    function clear(node) {
        while (node.firstChild) {
            node.removeChild(node.firstChild);
        }
    }

    function asNumber(value) {
        var number = Number(value);
        return Number.isFinite(number) ? number : 0;
    }

    function formatBytes(value) {
        var bytes = Math.max(0, asNumber(value));
        if (bytes < 1024) {
            return Math.round(bytes) + " B";
        }
        var units = ["KB", "MB", "GB", "TB", "PB"];
        var index = -1;
        do {
            bytes /= 1024;
            index += 1;
        } while (bytes >= 1024 && index < units.length - 1);
        return bytes.toFixed(bytes >= 100 || index === 0 ? 0 : 1) + " " + units[index];
    }

    function formatRate(value) {
        return formatBytes(value) + "/s";
    }

    function formatDate(value) {
        if (!value) {
            return "未记录";
        }
        var date = new Date(value);
        if (Number.isNaN(date.getTime())) {
            return String(value);
        }
        return date.toLocaleString("zh-CN", { hour12: false });
    }

    function formatLimit(value, suffix) {
        var limit = asNumber(value);
        return limit > 0 ? formatBytes(limit) + (suffix || "") : "不限";
    }

    function displayName(client) {
        return client.remark || ("客户端 #" + client.id);
    }

    function stateLabel(resource) {
        var labels = {
            running: "运行中",
            waiting: "等待客户端",
            stopped: resource.enabled ? "已停止" : "已停用",
            failed: "启动失败"
        };
        return labels[resource.state] || "未知";
    }

    function stateClass(resource) {
        return "overview-pill-" + (resource.state || "stopped");
    }

    function resourceName(resource) {
        if (resource.kind === "host") {
            return resource.entry || resource.remark || ("域名规则 #" + resource.id);
        }
        return resource.remark || ((resource.mode || "隧道").toUpperCase() + " 隧道 #" + resource.id);
    }

    function resourceType(resource) {
        if (resource.kind === "host") {
            return "域名规则";
        }
        var modes = {
            tcp: "TCP 隧道",
            udp: "UDP 隧道",
            socks5: "SOCKS5 代理",
            httpProxy: "HTTP 代理",
            secret: "Secret 隧道",
            p2p: "P2P 隧道",
            file: "文件访问"
        };
        return modes[resource.mode] || ((resource.mode || "未知").toUpperCase() + " 隧道");
    }

    function setNotice(message) {
        if (!message) {
            elements.notice.hidden = true;
            elements.notice.textContent = "";
            return;
        }
        elements.notice.hidden = false;
        elements.notice.textContent = message;
    }

    function updateClock() {
        elements.clock.dateTime = new Date().toISOString();
        elements.clock.textContent = new Date().toLocaleString("zh-CN", {
            hour12: false,
            month: "2-digit",
            day: "2-digit",
            hour: "2-digit",
            minute: "2-digit",
            second: "2-digit"
        });
    }

    function makePill(label, className) {
        return createElement("span", "overview-pill " + className, label);
    }

    function makeSummaryItem(label, value) {
        var item = createElement("div", "overview-summary-item");
        item.appendChild(createElement("span", "overview-summary-label", label));
        item.appendChild(createElement("strong", "overview-summary-value", value));
        return item;
    }

    function renderSummary(data) {
        var clients = Array.isArray(data.clients) ? data.clients : [];
        var resources = Array.isArray(data.resources) ? data.resources : [];
        var online = clients.filter(function (client) { return client.online; }).length;
        var running = resources.filter(function (resource) { return resource.state === "running"; }).length;
        var totalRate = clients.reduce(function (total, client) {
            return total + asNumber(client.rateIn) + asNumber(client.rateOut);
        }, 0);
        clear(elements.summary);
        elements.summary.appendChild(makeSummaryItem("客户端", clients.length));
        elements.summary.appendChild(makeSummaryItem("在线", online));
        elements.summary.appendChild(makeSummaryItem("运行规则", running));
        elements.summary.appendChild(makeSummaryItem("当前速率", formatRate(totalRate)));
    }

    function textForClient(client, resources) {
        var values = [client.id, client.remark, client.address, client.localAddress, client.version];
        resources.forEach(function (resource) {
            values.push(resourceName(resource), resource.entry, resource.target, resource.location, resource.mode, resource.remark);
        });
        return values.filter(Boolean).join(" ").toLocaleLowerCase();
    }

    function visibleClients(data) {
        var resourcesByClient = new Map();
        (data.resources || []).forEach(function (resource) {
            var id = asNumber(resource.clientId);
            if (!resourcesByClient.has(id)) {
                resourcesByClient.set(id, []);
            }
            resourcesByClient.get(id).push(resource);
        });
        var query = state.query.trim().toLocaleLowerCase();
        return (data.clients || []).map(function (client) {
            return { client: client, resources: resourcesByClient.get(asNumber(client.id)) || [] };
        }).filter(function (group) {
            if (state.onlineOnly && !group.client.online) {
                return false;
            }
            return !query || textForClient(group.client, group.resources).indexOf(query) !== -1;
        });
    }

    function appendDetailLine(container, label, value) {
        container.appendChild(createElement("dt", "", label));
        container.appendChild(createElement("dd", "", value || "--"));
    }

    function openDetail(resource, client) {
        clear(elements.detailList);
        elements.detailKind.textContent = resourceType(resource) + " · " + stateLabel(resource);
        elements.detailTitle.textContent = resourceName(resource);
        appendDetailLine(elements.detailList, "状态", stateLabel(resource));
        appendDetailLine(elements.detailList, "客户端", displayName(client) + " (#" + client.id + ")");
        appendDetailLine(elements.detailList, "入口", resource.entry || "--");
        appendDetailLine(elements.detailList, "后端目标", resource.target || "--");
        if (resource.location) {
            appendDetailLine(elements.detailList, "匹配路径", resource.location);
        }
        if (resource.kind === "host") {
            appendDetailLine(elements.detailList, "自动 HTTPS", resource.autoHttps ? "已启用" : "未启用");
        }
        if (asNumber(resource.currentConnections) > 0 || resource.kind === "tunnel") {
            appendDetailLine(elements.detailList, "当前连接", asNumber(resource.currentConnections));
        }
        appendDetailLine(elements.detailList, "入口流量", formatBytes(resource.flowIn));
        appendDetailLine(elements.detailList, "出口流量", formatBytes(resource.flowOut));
        appendDetailLine(elements.detailList, "当前速率", "入 " + formatRate(resource.rateIn) + " · 出 " + formatRate(resource.rateOut));
        appendDetailLine(elements.detailList, "流量限制", formatLimit(resource.flowLimitBytes));
        if (resource.healthCheckUrl) {
            appendDetailLine(elements.detailList, "健康检查", resource.healthCheckUrl);
        }
        if (resource.runError) {
            appendDetailLine(elements.detailList, "运行错误", resource.runError);
        }
        if (typeof elements.detail.showModal === "function") {
            if (!elements.detail.open) {
                elements.detail.showModal();
            }
        } else {
            elements.detail.setAttribute("open", "open");
        }
        elements.detailClose.focus();
    }

    function closeDetail() {
        if (typeof elements.detail.close === "function") {
            elements.detail.close();
        } else {
            elements.detail.removeAttribute("open");
        }
    }

    function makeResourceCard(resource, client) {
        var card = createElement("button", "overview-resource-card");
        card.type = "button";
        card.title = "查看" + resourceName(resource) + "详情";
        card.addEventListener("click", function () { openDetail(resource, client); });

        var top = createElement("div", "overview-resource-top");
        var resourceIcon = createElement("span", "overview-resource-icon" + (resource.kind === "host" ? " host" : ""));
        resourceIcon.appendChild(icon(resource.kind === "host" ? "fa-globe" : "fa-random"));
        top.appendChild(resourceIcon);
        var name = createElement("div", "overview-resource-name");
        name.appendChild(createElement("strong", "", resourceName(resource)));
        name.appendChild(createElement("span", "", resourceType(resource)));
        top.appendChild(name);
        top.appendChild(makePill(stateLabel(resource), stateClass(resource)));
        card.appendChild(top);

        var details = createElement("div", "overview-resource-details");
        var entry = createElement("div");
        entry.appendChild(createElement("dt", "", "入口"));
        entry.appendChild(createElement("dd", "", resource.entry || "--"));
        details.appendChild(entry);
        var target = createElement("div");
        target.appendChild(createElement("dt", "", "后端"));
        target.appendChild(createElement("dd", "", resource.target || "--"));
        details.appendChild(target);
        card.appendChild(details);

        var metrics = createElement("div", "overview-resource-metrics");
        metrics.appendChild(createElement("span", "", "速率 " + formatRate(asNumber(resource.rateIn) + asNumber(resource.rateOut))));
        if (resource.kind === "tunnel") {
            metrics.appendChild(createElement("span", "", "连接 " + asNumber(resource.currentConnections)));
        }
        metrics.appendChild(createElement("span", "", "流量 " + formatBytes(resource.flowTotal)));
        card.appendChild(metrics);
        return card;
    }

    function makeClientGroup(group) {
        var client = group.client;
        var isCollapsed = state.collapsed.has(String(client.id));
        var section = createElement("section", "overview-client" + (isCollapsed ? " is-collapsed" : ""));
        section.setAttribute("aria-labelledby", "overview-client-" + client.id);
        var header = createElement("header", "overview-client-header");
        var title = createElement("div", "overview-client-title");
        var titleRow = createElement("div", "overview-client-title-row");
        titleRow.appendChild(createElement("h3", "", displayName(client)));
        titleRow.lastChild.id = "overview-client-" + client.id;
        titleRow.appendChild(makePill(client.online ? "在线" : "离线", client.online ? "overview-pill-online" : "overview-pill-offline"));
        title.appendChild(titleRow);
        title.appendChild(createElement("span", "overview-client-address", client.localAddress || client.address || "未上报连接地址"));
        header.appendChild(title);

        var stats = createElement("div", "overview-client-stats");
        stats.appendChild(createElement("span", "", "规则 " + group.resources.length));
        stats.appendChild(createElement("span", "", "连接 " + asNumber(client.connections) + "/" + (asNumber(client.connectionLimit) || "不限")));
        stats.appendChild(createElement("span", "", "速率 " + formatRate(asNumber(client.rateIn) + asNumber(client.rateOut))));
        stats.appendChild(createElement("span", "", "总流量 " + formatBytes(client.flowTotal)));
        header.appendChild(stats);

        var toggle = createElement("button", "overview-client-toggle");
        toggle.type = "button";
        toggle.setAttribute("aria-expanded", String(!isCollapsed));
        toggle.setAttribute("aria-label", (isCollapsed ? "展开" : "收起") + displayName(client) + "的规则");
        toggle.appendChild(icon("fa-chevron-down"));
        toggle.addEventListener("click", function () {
            var id = String(client.id);
            if (state.collapsed.has(id)) {
                state.collapsed.delete(id);
            } else {
                state.collapsed.add(id);
            }
            renderTopology();
        });
        header.appendChild(toggle);
        section.appendChild(header);

        var grid = createElement("div", "overview-resource-grid");
        if (group.resources.length === 0) {
            grid.appendChild(createElement("div", "overview-empty", "该客户端没有可展示的转发规则"));
        } else {
            group.resources.forEach(function (resource) {
                grid.appendChild(makeResourceCard(resource, client));
            });
        }
        section.appendChild(grid);
        return section;
    }

    function renderTopology() {
        var data = state.data;
        if (!data) {
            return;
        }
        var groups = visibleClients(data);
        clear(elements.topology);
        elements.topology.setAttribute("aria-busy", "false");
        if (!groups.length) {
            var empty = createElement("div", "overview-empty");
            empty.appendChild(createElement("strong", "", "没有匹配的资源"));
            empty.appendChild(createElement("span", "", "调整筛选条件后再试。"));
            elements.topology.appendChild(empty);
        } else {
            var list = createElement("div", "overview-client-list");
            groups.forEach(function (group) { list.appendChild(makeClientGroup(group)); });
            elements.topology.appendChild(list);
        }
        var allCollapsed = groups.length > 0 && groups.every(function (group) {
            return state.collapsed.has(String(group.client.id));
        });
        elements.collapse.textContent = allCollapsed ? "展开全部" : "收起全部";
        elements.collapse.setAttribute("aria-pressed", String(allCollapsed));
    }

    function render(data) {
        renderSummary(data);
        renderTopology();
        var updated = formatDate(data.updatedAt);
        elements.updated.textContent = state.paused ? "自动刷新已暂停" : "更新于 " + updated;
        elements.liveStatus.textContent = "已更新" + accountName + "的资源状态。";
    }

    function redirectToLogin() {
        window.location.assign(baseURL + "/login/index");
    }

    function loadData() {
        if (state.loading) {
            return;
        }
        state.loading = true;
        elements.refresh.disabled = true;
        elements.topology.setAttribute("aria-busy", "true");
        fetch(baseURL + "/overview/data?_=" + Date.now(), {
            credentials: "same-origin",
            cache: "no-store",
            headers: { "Accept": "application/json" }
        }).then(function (response) {
            var contentType = response.headers.get("content-type") || "";
            if (response.redirected || response.status === 401 || response.status === 403 || contentType.indexOf("application/json") === -1) {
                redirectToLogin();
                throw new Error("登录状态已失效");
            }
            if (!response.ok) {
                throw new Error("服务器返回 " + response.status);
            }
            return response.json();
        }).then(function (payload) {
            if (!payload || payload.status !== 1 || !payload.data) {
                throw new Error("实时数据格式无效");
            }
            state.data = payload.data;
            setNotice("");
            render(state.data);
        }).catch(function (error) {
            if (error && error.message === "登录状态已失效") {
                return;
            }
            elements.topology.setAttribute("aria-busy", "false");
            setNotice("暂时无法读取实时资源数据：" + (error && error.message ? error.message : "网络错误"));
            elements.liveStatus.textContent = "实时资源数据读取失败。";
        }).finally(function () {
            state.loading = false;
            elements.refresh.disabled = false;
        });
    }

    function togglePause() {
        state.paused = !state.paused;
        elements.pause.setAttribute("aria-pressed", String(state.paused));
        elements.pause.title = state.paused ? "恢复自动刷新" : "暂停自动刷新";
        elements.pause.setAttribute("aria-label", elements.pause.title);
        clear(elements.pause);
        elements.pause.appendChild(icon(state.paused ? "fa-play" : "fa-pause"));
        if (state.paused) {
            elements.updated.textContent = "自动刷新已暂停";
        } else {
            loadData();
        }
    }

    elements.search.addEventListener("input", function () {
        state.query = elements.search.value || "";
        renderTopology();
    });
    elements.onlineOnly.addEventListener("change", function () {
        state.onlineOnly = elements.onlineOnly.checked;
        renderTopology();
    });
    elements.collapse.addEventListener("click", function () {
        if (!state.data) {
            return;
        }
        var groups = visibleClients(state.data);
        var allCollapsed = groups.length > 0 && groups.every(function (group) {
            return state.collapsed.has(String(group.client.id));
        });
        groups.forEach(function (group) {
            var id = String(group.client.id);
            if (allCollapsed) {
                state.collapsed.delete(id);
            } else {
                state.collapsed.add(id);
            }
        });
        renderTopology();
    });
    elements.refresh.addEventListener("click", loadData);
    elements.pause.addEventListener("click", togglePause);
    elements.detailClose.addEventListener("click", closeDetail);
    elements.detail.addEventListener("click", function (event) {
        if (event.target === elements.detail) {
            closeDetail();
        }
    });
    document.addEventListener("visibilitychange", function () {
        if (!document.hidden && !state.paused) {
            loadData();
        }
    });

    updateClock();
    window.setInterval(updateClock, 1000);
    window.setInterval(function () {
        if (!state.paused && !document.hidden) {
            loadData();
        }
    }, refreshInterval);
    loadData();
}());
