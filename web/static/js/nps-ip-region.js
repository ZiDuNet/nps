/*
 * Client-side IP region lookup.
 *
 * Region is presentation-only metadata for the client list. The NPS server
 * deliberately does not proxy or persist it: the browser resolves public IPs
 * on demand, deduplicates identical addresses and keeps a bounded local cache.
 */
(function (window, document) {
    'use strict';

    var PROVIDERS = [
        {name: 'ip258', endpoint: 'https://ip258.cn/api/ip_api.php?ip=', suffix: ''},
        {name: 'geojs', endpoint: 'https://get.geojs.io/v1/ip/geo/', suffix: '.json'}
    ];
    // v4 also caches country codes so flags survive table redraws and reloads.
    var CACHE_KEY = 'nps-ip-region-v4';
    // ISO flags plus XK, from flag-icons 7.6.0. Only bundled assets are allowed.
    var FLAG_CODES = ('ad ae af ag ai al am ao aq ar as at au aw ax az ba bb bd be bf bg bh bi bj bl bm bn bo bq br bs bt bv bw by bz '
        + 'ca cc cd cf cg ch ci ck cl cm cn co cr cu cv cw cx cy cz de dj dk dm do dz ec ee eg eh er es et fi fj fk fm fo fr '
        + 'ga gb gd ge gf gg gh gi gl gm gn gp gq gr gs gt gu gw gy hk hm hn hr ht hu id ie il im in io iq ir is it je jm jo jp '
        + 'ke kg kh ki km kn kp kr kw ky kz la lb lc li lk lr ls lt lu lv ly ma mc md me mf mg mh mk ml mm mn mo mp mq mr ms mt mu mv mw mx my mz '
        + 'na nc ne nf ng ni nl no np nr nu nz om pa pe pf pg ph pk pl pm pn pr ps pt pw py qa re ro rs ru rw sa sb sc sd se sg sh si sj sk sl sm sn so sr ss st sv sx sy sz '
        + 'tc td tf tg th tj tk tl tm tn to tr tt tv tw tz ua ug um us uy uz va vc ve vg vi vn vu wf ws xk ye yt za zm zw').split(' ');
    // Provider abbreviations differ from Intl's official region names. These
    // also keep common flags available in browsers without Intl.DisplayNames.
    var COUNTRY_ALIASES = {
        cn: ['中国', '中国大陆', '中华人民共和国', 'China', 'Mainland China'],
        hk: ['香港', '中国香港', '香港特别行政区', 'Hong Kong', 'Hong Kong SAR China'],
        mo: ['澳门', '中国澳门', '澳门特别行政区', 'Macao', 'Macau', 'Macao SAR China'],
        tw: ['台湾', '中国台湾', '台湾省', 'Taiwan'],
        us: ['美国', '美利坚合众国', 'United States', 'United States of America', 'USA'],
        gb: ['英国', 'United Kingdom', 'Great Britain', 'UK'],
        kr: ['韩国', '大韩民国', 'South Korea', 'Korea, Republic of'],
        kp: ['朝鲜', 'North Korea'],
        ru: ['俄罗斯', '俄罗斯联邦', 'Russia', 'Russian Federation'],
        vn: ['越南', 'Vietnam', 'Viet Nam'],
        ae: ['阿联酋', '阿拉伯联合酋长国', 'United Arab Emirates', 'UAE'],
        au: ['澳大利亚', 'Australia'],
        jp: ['日本', 'Japan'],
        sg: ['新加坡', 'Singapore'],
        ca: ['加拿大', 'Canada'],
        de: ['德国', 'Germany'],
        fr: ['法国', 'France'],
        nl: ['荷兰', 'Netherlands']
    };
    var countryNameCodes;
    var CACHE_TTL = 30 * 24 * 60 * 60 * 1000;
    var FALLBACK_CACHE_TTL = 24 * 60 * 60 * 1000;
    var FAILURE_TTL = 5 * 60 * 1000;
    var REQUEST_TIMEOUT = 4000;
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
            var bracketed = ip.match(/^\[([0-9a-f:.]+)\](?::\d+)?$/i);
            if (!bracketed || bracketed[1].indexOf(':') < 0) return '';
            ip = bracketed[1];
        } else {
            var ipv4WithPort = ip.match(/^(\d{1,3}(?:\.\d{1,3}){3}):\d+$/);
            if (ipv4WithPort) ip = ipv4WithPort[1];
        }
        if (isIPv4(ip)) return ip.split('.').map(Number).join('.');
        var words = ipv6Words(ip);
        if (!words) return '';
        // IPv4-mapped IPv6 is the same address; also apply IPv4 private checks.
        if (words.slice(0, 5).every(function (word) { return word === 0; }) && words[5] === 0xffff) {
            return [words[6] >> 8, words[6] & 255, words[7] >> 8, words[7] & 255].join('.');
        }
        return ip.toLowerCase();
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

    function ipv6Words(value) {
        var ip = String(value || '').toLowerCase();
        if (ip.indexOf(':') < 0) return null;
        if (ip.indexOf('.') >= 0) {
            var separator = ip.lastIndexOf(':');
            var tail = ip.slice(separator + 1);
            if (!isIPv4(tail)) return null;
            var bytes = tail.split('.').map(Number);
            ip = ip.slice(0, separator + 1) + ((bytes[0] << 8) | bytes[1]).toString(16)
                + ':' + ((bytes[2] << 8) | bytes[3]).toString(16);
        }
        var halves = ip.split('::');
        if (halves.length > 2) return null;
        var left = halves[0] ? halves[0].split(':') : [];
        var right = halves.length === 2 && halves[1] ? halves[1].split(':') : [];
        var parts = left.concat(right);
        if (!parts.every(function (part) { return /^[0-9a-f]{1,4}$/.test(part); })) return null;
        var missing = 8 - parts.length;
        if (halves.length === 1 ? missing !== 0 : missing < 1) return null;
        var zeros = [];
        while (halves.length === 2 && missing-- > 0) zeros.push('0');
        return left.concat(zeros, right).map(function (word) { return parseInt(word, 16); });
    }

    function sameIp(first, second) {
        var left = normalizeIp(first);
        var right = normalizeIp(second);
        if (!left || !right) return false;
        if (left === right) return true;
        var leftWords = ipv6Words(left);
        var rightWords = ipv6Words(right);
        return !!(leftWords && rightWords && leftWords.join(':') === rightWords.join(':'));
    }

    function specialAddressLabel(ip) {
        if (!ip) return localText('无有效地址', 'No valid IP');
        if (ip.indexOf(':') >= 0) {
            var words = ipv6Words(ip);
            var loopback = words.slice(0, 7).every(function (word) { return word === 0; }) && words[7] <= 1;
            if (loopback || (words[0] & 0xfe00) === 0xfc00 || (words[0] & 0xffc0) === 0xfe80
                || (words[0] & 0xffc0) === 0xfec0 || (words[0] & 0xff00) === 0xff00) {
                return localText('内网地址', 'Private address');
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

    function locationPart(value) {
        if (typeof value !== 'string') return '';
        var text = value.trim();
        return /^(?:-|n\/?a|null|unknown(?: region)?|未知(?:地区|位置)?|暂无(?:数据|信息)?)$/i.test(text) ? '' : text;
    }

    function validCountryCode(value) {
        var code = typeof value === 'string' ? value.trim().toLowerCase() : '';
        if (code === 'uk') code = 'gb';
        return FLAG_CODES.indexOf(code) >= 0 ? code : '';
    }

    function flagCountryCode(value, country) {
        var code = validCountryCode(value);
        if (code || !country) return code;
        if (!countryNameCodes) {
            countryNameCodes = Object.create(null);
            ['en', 'zh-CN', 'zh-TW'].forEach(function (locale) {
                try {
                    var names = new window.Intl.DisplayNames([locale], {type: 'region'});
                    FLAG_CODES.forEach(function (flag) {
                        var name = names.of(flag.toUpperCase());
                        if (name) countryNameCodes[name.toLowerCase()] = flag;
                    });
                } catch (error) {
                    // Names still display normally when Intl is unavailable.
                }
            });
            Object.keys(COUNTRY_ALIASES).forEach(function (flag) {
                COUNTRY_ALIASES[flag].forEach(function (name) {
                    countryNameCodes[name.toLowerCase()] = flag;
                });
            });
        }
        return countryNameCodes[country.toLowerCase()] || '';
    }

    function providerRegion(provider, payload, ip) {
        if (!payload || typeof payload !== 'object') return null;
        var data = payload;
        if (provider.name === 'ip258') {
            if (Number(payload.code) !== 200 || !payload.data || typeof payload.data !== 'object') return null;
            data = payload.data;
        }
        if (data.ip && !sameIp(data.ip, ip)) return null;
        var city = locationPart(data.city);
        var region = locationPart(provider.name === 'ip258' ? data.prov : data.region);
        var area = provider.name === 'ip258' ? locationPart(data.area) : '';
        var country = locationPart(data.country);
        var countryCode = flagCountryCode(data.country_code, country);
        try {
            if (validCountryCode(data.country_code) && window.Intl && window.Intl.DisplayNames) {
                country = new window.Intl.DisplayNames([isEnglish() ? 'en' : 'zh-CN'], {type: 'region'}).of(countryCode.toUpperCase()) || country;
            }
        } catch (error) {
            // Fall back to the provider's English country name.
        }
        var parts = [];
        [country, region, city, area].forEach(function (part) {
            if (!part) return;
            var duplicate = parts.some(function (existing) {
                return existing.toLowerCase() === part.toLowerCase();
            });
            if (!duplicate) parts.push(part);
        });
        return parts.length ? {label: parts.join(' · '), countryCode: countryCode} : null;
    }

    function setOutput(node, result, state) {
        var element = node.querySelector('[data-ip-region-output]');
        if (!element) return;
        var label = typeof result === 'string' ? result : (result && result.label) || '';
        var countryCode = validCountryCode(result && result.countryCode);
        element.textContent = label || '';
        element.setAttribute('title', label || '');
        element.setAttribute('data-ip-region-state', state || 'ready');
        element.removeAttribute('aria-busy');
        var flag = node.querySelector('[data-ip-region-flag]');
        if (!flag) return;
        flag.hidden = !countryCode;
        if (!countryCode) {
            flag.removeAttribute('src');
            return;
        }
        flag.onerror = function () {
            flag.hidden = true;
            flag.removeAttribute('src');
        };
        var base = String((window.nps && window.nps.web_base_url) || '').replace(/\/+$/, '');
        flag.setAttribute('src', base + '/static/img/flags/4x3/' + countryCode + '.svg');
    }

    function publish(ip, result, state) {
        var elements = listeners[ip] || [];
        elements.forEach(function (element) {
            if (element && document.documentElement.contains(element)) setOutput(element, result, state);
        });
        delete listeners[ip];
    }

    function requestProvider(provider, ip) {
        return new Promise(function (resolve, reject) {
            var controller = typeof window.AbortController === 'function' ? new window.AbortController() : null;
            var settled = false;
            var timer = window.setTimeout(function () {
                finish(new Error('IP region request timed out'));
                if (controller) controller.abort();
            }, REQUEST_TIMEOUT);

            function finish(error, result) {
                if (settled) return;
                settled = true;
                window.clearTimeout(timer);
                if (error) reject(error);
                else resolve(result);
            }

            // A rejecting timeout still advances to the backup without AbortController.
            Promise.resolve().then(function () {
                return window.fetch(provider.endpoint + encodeURIComponent(ip) + provider.suffix, {
                    method: 'GET',
                    mode: 'cors',
                    credentials: 'omit',
                    referrerPolicy: 'no-referrer',
                    cache: 'no-store',
                    signal: controller ? controller.signal : undefined
                });
            }).then(function (response) {
                if (!response.ok) throw new Error('IP region request failed');
                return response.json();
            }).then(function (payload) {
                var result = providerRegion(provider, payload, ip);
                if (!result) throw new Error('IP region unavailable');
                finish(null, result);
            }).catch(function (error) {
                finish(error);
            });
        });
    }

    function requestGeo(ip) {
        if (typeof window.fetch !== 'function') return Promise.reject(new Error('fetch unavailable'));
        function attempt(index) {
            if (index >= PROVIDERS.length) return Promise.reject(new Error('IP region unavailable'));
            var provider = PROVIDERS[index];
            return requestProvider(provider, ip).then(function (result) {
                cache[ip] = {
                    label: result.label,
                    countryCode: result.countryCode,
                    provider: provider.name,
                    expires: Date.now() + (index === 0 ? CACHE_TTL : FALLBACK_CACHE_TTL)
                };
                writeCache();
                return result;
            }, function () {
                return attempt(index + 1);
            });
        }
        return attempt(0);
    }

    function pump() {
        while (inFlight < MAX_CONCURRENT && queue.length) {
            var ip = queue.shift();
            delete queued[ip];
            requesting[ip] = true;
            inFlight += 1;
            (function (address) {
                requestGeo(address).then(function (result) {
                    publish(address, result, 'ready');
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
        if (scope && scope.jquery) scope = scope[0];
        if (!scope || typeof scope.querySelectorAll !== 'function') return;
        nodes = scope.querySelectorAll('[data-ip-region-ip]:not([data-ip-region-bound])');
        Array.prototype.forEach.call(nodes, function (node) {
            var output = node.querySelector('[data-ip-region-output]');
            var ip = normalizeIp(node.getAttribute('data-ip-region-ip'));
            if (!output) return;
            node.setAttribute('data-ip-region-bound', '1');
            output.setAttribute('aria-busy', 'true');
            setOutput(node, localText('定位中…', 'Locating…'), 'loading');

            var special = specialAddressLabel(ip);
            if (special) {
                setOutput(node, special, 'local');
                return;
            }

            if (cache[ip] && Number(cache[ip].expires) > Date.now()) {
                setOutput(node, cache[ip], 'ready');
                return;
            }

            if (failures[ip] && Number(failures[ip]) > Date.now()) {
                setOutput(node, localText('地域未知', 'Unknown region'), 'error');
                return;
            }

            listeners[ip] = listeners[ip] || [];
            listeners[ip].push(node);
            schedule(ip);
        });
    }

    window.npsIpRegion = {
        decorate: decorate,
        normalizeIp: normalizeIp
    };
}(window, document));
