/*
 * Client-side IP region lookup.
 *
 * Region is presentation-only metadata for the client list. The NPS server
 * deliberately does not proxy or persist it: the browser resolves public IPs
 * on demand, deduplicates identical addresses and keeps a bounded local cache.
 */
(function (window, document) {
    'use strict';

    var ENDPOINT = 'https://get.geojs.io/v1/ip/geo/';
    var CACHE_KEY = 'nps-ip-region-v1';
    var CACHE_TTL = 30 * 24 * 60 * 60 * 1000;
    var FAILURE_TTL = 5 * 60 * 1000;
    var REQUEST_TIMEOUT = 6000;
    var MAX_CONCURRENT = 4;
    var cache = readCache();
    var failures = {};
    var queue = [];
    var queued = {};
    var requesting = {};
    var listeners = {};
    var inFlight = 0;

    function isEnglish() {
        return typeof window.npsIsEnglish === 'function' && window.npsIsEnglish();
    }

    function localText(zh, en) {
        return isEnglish() ? en : zh;
    }

    function readCache() {
        try {
            var parsed = JSON.parse(window.localStorage.getItem(CACHE_KEY) || '{}');
            return parsed && typeof parsed === 'object' ? parsed : {};
        } catch (error) {
            return {};
        }
    }

    function writeCache() {
        var now = Date.now();
        var compact = {};
        Object.keys(cache).forEach(function (ip) {
            var entry = cache[ip];
            if (entry && entry.label && Number(entry.expires) > now) compact[ip] = entry;
        });
        cache = compact;
        try {
            window.localStorage.setItem(CACHE_KEY, JSON.stringify(cache));
        } catch (error) {
            // Storage can be unavailable in hardened or private browser modes.
        }
    }

    function normalizeIp(value) {
        var ip = String(value || '').trim();
        if (!ip || ip === '-') return '';
        if (ip.charAt(0) === '[') {
            var bracket = ip.indexOf(']');
            if (bracket > 1) ip = ip.slice(1, bracket);
        } else {
            var ipv4WithPort = ip.match(/^(\d{1,3}(?:\.\d{1,3}){3}):\d+$/);
            if (ipv4WithPort) ip = ipv4WithPort[1];
        }
        if (isIPv4(ip)) return ip;
        if (ip.indexOf(':') >= 0 && /^[0-9a-f:.]+$/i.test(ip)) return ip.toLowerCase();
        return '';
    }

    function isIPv4(value) {
        var parts = String(value || '').split('.');
        if (parts.length !== 4) return false;
        return parts.every(function (part) {
            if (!/^\d{1,3}$/.test(part)) return false;
            var number = Number(part);
            return number >= 0 && number <= 255;
        });
    }

    function specialAddressLabel(ip) {
        if (!ip) return localText('无有效地址', 'No valid IP');
        if (ip.indexOf(':') >= 0) {
            var lower = ip.toLowerCase();
            if (lower === '::' || lower === '::1') return localText('内网地址', 'Private address');
            if (/^f[cd]/.test(lower)) return localText('内网地址', 'Private address');
            if (/^fe[89ab]/.test(lower)) return localText('内网地址', 'Private address');
            if (lower.indexOf('::ffff:') === 0 && isIPv4(lower.slice(7))) {
                return specialAddressLabel(lower.slice(7));
            }
            return '';
        }
        var parts = ip.split('.').map(Number);
        var first = parts[0];
        var second = parts[1];
        if (first === 0 || first === 10 || first === 127) return localText('内网地址', 'Private address');
        if (first === 169 && second === 254) return localText('内网地址', 'Private address');
        if (first === 172 && second >= 16 && second <= 31) return localText('内网地址', 'Private address');
        if (first === 192 && second === 168) return localText('内网地址', 'Private address');
        if (first === 100 && second >= 64 && second <= 127) return localText('内网地址', 'Private address');
        if (first === 198 && (second === 18 || second === 19)) return localText('内网地址', 'Private address');
        if (first >= 224) return localText('内网地址', 'Private address');
        return '';
    }

    function providerLabel(payload) {
        var city = String(payload && payload.city || '').trim();
        var region = String(payload && payload.region || '').trim();
        var countryCode = String(payload && payload.country_code || '').trim().toUpperCase();
        var country = String(payload && payload.country || '').trim();
        try {
            if (countryCode && window.Intl && window.Intl.DisplayNames) {
                country = new window.Intl.DisplayNames([isEnglish() ? 'en' : 'zh-CN'], {type: 'region'}).of(countryCode) || country;
            }
        } catch (error) {
            // Fall back to the provider's English country name.
        }
        var area = region || city;
        var parts = [];
        [area, country].forEach(function (part) {
            if (!part) return;
            var duplicate = parts.some(function (existing) {
                return existing.toLowerCase() === part.toLowerCase();
            });
            if (!duplicate) parts.push(part);
        });
        return parts.join(' · ');
    }

    function setOutput(element, label, state) {
        if (!element) return;
        element.textContent = label || '';
        element.setAttribute('data-ip-region-state', state || 'ready');
        element.removeAttribute('aria-busy');
    }

    function publish(ip, label, state) {
        var elements = listeners[ip] || [];
        elements.forEach(function (element) {
            if (element && document.documentElement.contains(element)) setOutput(element, label, state);
        });
        delete listeners[ip];
    }

    function requestGeo(ip) {
        if (typeof window.fetch !== 'function') return Promise.reject(new Error('fetch unavailable'));
        var controller = typeof window.AbortController === 'function' ? new window.AbortController() : null;
        var timer = window.setTimeout(function () {
            if (controller) controller.abort();
        }, REQUEST_TIMEOUT);
        return window.fetch(ENDPOINT + encodeURIComponent(ip) + '.json', {
            method: 'GET',
            mode: 'cors',
            credentials: 'omit',
            cache: 'force-cache',
            signal: controller ? controller.signal : undefined
        }).then(function (response) {
            if (!response.ok) throw new Error('IP region request failed');
            return response.json();
        }).then(function (payload) {
            var label = providerLabel(payload);
            if (!label) throw new Error('IP region unavailable');
            cache[ip] = {label: label, expires: Date.now() + CACHE_TTL};
            writeCache();
            return label;
        }).finally(function () {
            window.clearTimeout(timer);
        });
    }

    function pump() {
        while (inFlight < MAX_CONCURRENT && queue.length) {
            var ip = queue.shift();
            delete queued[ip];
            requesting[ip] = true;
            inFlight += 1;
            (function (address) {
                requestGeo(address).then(function (label) {
                    publish(address, label, 'ready');
                }).catch(function () {
                    failures[address] = Date.now() + FAILURE_TTL;
                    publish(address, localText('地域未知', 'Unknown region'), 'error');
                }).finally(function () {
                    delete requesting[address];
                    inFlight -= 1;
                    pump();
                });
            }(ip));
        }
    }

    function schedule(ip) {
        if (queued[ip] || requesting[ip]) return;
        queued[ip] = true;
        queue.push(ip);
        pump();
    }

    function decorate(root) {
        var scope = root || document;
        var nodes;
        if (typeof scope === 'string') scope = document.querySelector(scope);
        if (scope.jquery) scope = scope[0];
        if (!scope || typeof scope.querySelectorAll !== 'function') return;
        nodes = scope.querySelectorAll('[data-ip-region-ip]:not([data-ip-region-bound])');
        Array.prototype.forEach.call(nodes, function (node) {
            var output = node.querySelector('[data-ip-region-output]');
            var ip = normalizeIp(node.getAttribute('data-ip-region-ip'));
            if (!output) return;
            node.setAttribute('data-ip-region-bound', '1');
            output.setAttribute('aria-busy', 'true');
            setOutput(output, localText('定位中…', 'Locating…'), 'loading');

            var special = specialAddressLabel(ip);
            if (special) {
                setOutput(output, special, 'local');
                return;
            }

            if (cache[ip] && Number(cache[ip].expires) > Date.now()) {
                setOutput(output, cache[ip].label, 'ready');
                return;
            }

            if (failures[ip] && Number(failures[ip]) > Date.now()) {
                setOutput(output, localText('地域未知', 'Unknown region'), 'error');
                return;
            }

            listeners[ip] = listeners[ip] || [];
            listeners[ip].push(output);
            schedule(ip);
        });
    }

    window.npsIpRegion = {
        decorate: decorate,
        normalizeIp: normalizeIp
    };
}(window, document));
