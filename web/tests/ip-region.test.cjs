const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

const script = fs.readFileSync(path.join(__dirname, '../static/js/nps-ip-region.js'), 'utf8');
const cacheKey = 'nps-ip-region-v4';
const primaryEndpoint = 'https://ip258.cn/api/ip_api.php?ip=';
const backupEndpoint = 'https://get.geojs.io/v1/ip/geo/';
const primaryPayload = data => ({ code: 200, data });
const response = payload => ({ ok: true, json: () => Promise.resolve(payload) });
const flush = () => new Promise(resolve => setImmediate(resolve));

// Exercise the real browser entrypoint without network calls or DOM dependencies.
function browser({
    payload = primaryPayload({ country: '中国', prov: '广东', city: '深圳' }),
    backupPayload = { country: 'China', country_code: 'CN', region: 'Guangdong', city: 'Shenzhen' },
    cached = {}, storageUnavailable = false, fail = false, english = false, intl = Intl,
    respond, timers, abortController = true, fetchUnavailable = false, now = () => Date.now(), basePath = '', withFlag = true
} = {}) {
    const storage = new Map(Object.entries(cached));
    const nodes = [];
    const requests = [];
    const document = {
        documentElement: { contains: element => nodes.includes(element) },
        querySelectorAll: () => nodes.filter(node => !node.attributes['data-ip-region-bound'])
    };
    const window = {
        Intl: intl,
        nps: { web_base_url: basePath },
        AbortController: abortController ? AbortController : undefined,
        npsIsEnglish: () => english,
        setTimeout: timers ? timers.setTimeout : setTimeout,
        clearTimeout: timers ? timers.clearTimeout : clearTimeout,
        localStorage: {
            getItem(key) {
                if (storageUnavailable) throw new Error('storage denied');
                return storage.get(key) || null;
            },
            setItem(key, value) {
                if (storageUnavailable) throw new Error('storage denied');
                storage.set(key, value);
            }
        },
        fetch(url, options) {
            requests.push({ url, options });
            if (respond) return respond(url, options, requests.length - 1);
            if (fail) return Promise.reject(new Error('provider unavailable'));
            if (url.startsWith(primaryEndpoint)) return Promise.resolve(response(payload));
            assert.ok(url.startsWith(backupEndpoint), `unexpected provider: ${url}`);
            return Promise.resolve(response(backupPayload));
        }
    };
    if (fetchUnavailable) delete window.fetch;
    vm.runInNewContext(script, { window, document, Date: { now } });
    return {
        storage,
        requests,
        normalizeIp: window.npsIpRegion.normalizeIp,
        add(address) {
            const flag = {
                hidden: true,
                attributes: {},
                setAttribute(key, value) { this.attributes[key] = value; },
                removeAttribute(key) { delete this.attributes[key]; }
            };
            const output = {
                flag,
                textContent: '',
                attributes: {},
                setAttribute(key, value) { this.attributes[key] = value; },
                removeAttribute(key) { delete this.attributes[key]; }
            };
            nodes.push({
                output,
                attributes: { 'data-ip-region-ip': address },
                getAttribute(key) { return this.attributes[key]; },
                setAttribute(key, value) { this.attributes[key] = value; },
                querySelector: selector => selector === '[data-ip-region-output]' ? output : (withFlag ? flag : null)
            });
            return output;
        },
        async decorate() {
            window.npsIpRegion.decorate(document);
            await flush();
        }
    };
}

function manualTimers() {
    let next = 0;
    const pending = new Map();
    return {
        setTimeout(callback, delay) {
            const id = ++next;
            pending.set(id, { callback, delay });
            return id;
        },
        clearTimeout(id) { pending.delete(id); },
        expireNext() {
            const [id, timer] = pending.entries().next().value;
            assert.equal(timer.delay, 4000);
            pending.delete(id);
            timer.callback();
        },
        pending
    };
}

test('prefers ip258 Chinese country, province, city and district and exposes the full label', async () => {
    const page = browser({ payload: primaryPayload({ country: '中国', prov: '广东', city: '深圳', area: '南山' }) });
    const output = page.add('203.0.113.10:8024');
    await page.decorate();
    assert.equal(output.textContent, '中国 · 广东 · 深圳 · 南山');
    assert.equal(output.attributes.title, output.textContent);
    assert.equal(output.attributes['data-ip-region-state'], 'ready');
    assert.equal(page.requests.length, 1);
    assert.equal(page.requests[0].url, primaryEndpoint + '203.0.113.10');
    assert.equal(page.requests[0].options.credentials, 'omit');
    assert.equal(page.requests[0].options.mode, 'cors');
    assert.equal(page.requests[0].options.referrerPolicy, 'no-referrer');
    const cached = JSON.parse(page.storage.get(cacheKey))['203.0.113.10'];
    assert.equal(cached.provider, 'ip258');
    assert.equal(cached.countryCode, 'cn');
    assert.equal(output.flag.hidden, false);
    assert.equal(output.flag.attributes.src, '/static/img/flags/4x3/cn.svg');
    assert.ok(Math.abs(cached.expires - Date.now() - 30 * 24 * 60 * 60 * 1000) < 1000);
});

test('deduplicates names and skips missing location fields', async t => {
    const cases = [
        [{ country: '中国', prov: '北京', city: '北京', area: '海淀' }, '中国 · 北京 · 海淀'],
        [{ country: 'Singapore', prov: 'singapore', city: 'SINGAPORE' }, 'Singapore'],
        [{ country: 'China', city: 'Shanghai' }, 'China · Shanghai'],
        [{ country: '中国', prov: '广东' }, '中国 · 广东'],
        [{ city: ' 深圳 ' }, '深圳'],
        [{ country: '中国', prov: '未知', city: '-', area: 'N/A' }, '中国']
    ];
    for (const [payload, expected] of cases) {
        await t.test(expected, async () => {
            const page = browser({ payload: primaryPayload(payload) });
            const output = page.add('203.0.113.10');
            await page.decorate();
            assert.equal(output.textContent, expected);
        });
    }
});

test('keeps English and unsupported Intl country-name fallbacks', async () => {
    for (const intl of [Intl, {}]) {
        const page = browser({ english: true, intl, payload: { code: 403 } });
        const output = page.add('203.0.113.10');
        await page.decorate();
        assert.equal(output.textContent, 'China · Guangdong · Shenzhen');
        assert.equal(page.requests.length, 2);
    }
});

test('does not query private addresses and keeps invalid addresses local', async () => {
    const page = browser();
    const privateIPs = [
        '10.0.0.1', '192.168.1.2:8024', '172.16.1.2', '127.0.0.1', '100.64.0.1',
        '[::1]:8024', '0:0:0:0:0:0:0:1', '::', '0:0:0:0:0:0:0:0',
        'fc00::1', 'fd12::1', 'fe80::1', 'fec0::1', 'ff02::1',
        '::ffff:192.168.1.2', '::ffff:c0a8:102', '[::ffff:7f00:1]:8024'
    ];
    const outputs = privateIPs.map(address => page.add(address));
    const invalidIPs = ['-', 'example.com', '999.1.2.3', '1.2.3.4/24', '[1.1.1.1]', '2001:::1', '2001:db8:', '[::1]extra', '1:2:3:4:5:6:7:8:9', 'fe80::1%eth0'];
    const invalid = invalidIPs.map(address => page.add(address));
    await page.decorate();
    for (const output of outputs) {
        assert.equal(output.textContent, '内网地址');
        assert.equal(output.attributes.title, '内网地址');
        assert.equal(output.flag.hidden, true);
        assert.equal(output.flag.attributes.src, undefined);
    }
    for (const output of invalid) assert.equal(output.textContent, '无有效地址');
    assert.equal(page.requests.length, 0);
});

test('supports public IPv6 and encodes the address for both providers', async t => {
    for (const primaryFails of [false, true]) {
        await t.test(primaryFails ? 'GeoJS backup' : 'ip258 primary', async () => {
            const page = browser({
                payload: primaryFails ? { code: 403 } : primaryPayload({ ip: '2606:4700:4700:0:0:0:0:1111', country: '美国' }),
                backupPayload: { ip: '2606:4700:4700::1111', country: 'United States', country_code: 'US' }
            });
            const output = page.add('[2606:4700:4700::1111]:8024');
            await page.decorate();
            assert.equal(output.textContent, '美国');
            assert.equal(page.requests[0].url, primaryEndpoint + '2606%3A4700%3A4700%3A%3A1111');
            if (primaryFails) assert.equal(page.requests[1].url, backupEndpoint + '2606%3A4700%3A4700%3A%3A1111.json');
            else assert.equal(page.requests.length, 1);
        });
    }
    assert.equal(browser().normalizeIp('::ffff:1.1.1.1'), '1.1.1.1');
});

test('ignores old GeoJS-only caches and reuses Chinese results across renders', async () => {
    const ip = '203.0.113.10';
    const expires = Date.now() + 30 * 24 * 60 * 60 * 1000;
    const page = browser({
        cached: {
            'nps-ip-region-v1': JSON.stringify({ [ip]: { label: 'Guangdong · 中国', expires } }),
            'nps-ip-region-v2': JSON.stringify({ [ip]: { label: '中国 · Guangdong · Shenzhen', expires } }),
            'nps-ip-region-v3': JSON.stringify({ [ip]: { label: '中国 · 广东 · 深圳', expires } })
        }
    });
    const first = page.add(ip);
    const duplicate = page.add(ip);
    await page.decorate();
    const rerendered = page.add(ip);
    await page.decorate();
    assert.equal(page.requests.length, 1);
    for (const output of [first, duplicate, rerendered]) {
        assert.equal(output.textContent, '中国 · 广东 · 深圳');
        assert.equal(output.attributes.title, output.textContent);
        assert.equal(output.flag.attributes.src, '/static/img/flags/4x3/cn.svg');
    }
    const cached = JSON.parse(page.storage.get(cacheKey));
    assert.equal(cached[ip].label, first.textContent);
    const reloaded = browser({ cached: Object.fromEntries(page.storage), fail: true });
    const restored = reloaded.add(ip);
    await reloaded.decorate();
    assert.equal(restored.textContent, first.textContent);
    assert.equal(restored.attributes.title, first.textContent);
    assert.equal(restored.flag.hidden, false);
    assert.equal(restored.flag.attributes.src, '/static/img/flags/4x3/cn.svg');
    assert.equal(reloaded.requests.length, 0);
});

test('works without browser storage and degrades gracefully on lookup failure', async () => {
    const page = browser({ storageUnavailable: true, payload: primaryPayload({ city: '深圳' }) });
    const output = page.add('203.0.113.10');
    await page.decorate();
    assert.equal(output.textContent, '深圳');

    for (const options of [{ fail: true }, { payload: {}, backupPayload: {} }, { fetchUnavailable: true }]) {
        const unavailable = browser(options);
        const unknown = unavailable.add('203.0.113.10');
        await unavailable.decorate();
        assert.equal(unknown.textContent, '地域未知');
        assert.equal(unknown.attributes['data-ip-region-state'], 'error');
    }
});

test('falls back on network, CORS, HTTP, JSON, application and location errors', async t => {
    const failures = [
        ['network/CORS error', () => Promise.reject(new TypeError('Failed to fetch'))],
        ['HTTP 403', () => Promise.resolve({ ok: false })],
        ['invalid JSON', () => Promise.resolve({ ok: true, json: () => Promise.reject(new SyntaxError('invalid JSON')) })],
        ['application error', () => Promise.resolve(response({ code: 403, data: { country: '错误内容' } }))],
        ['missing fields', () => Promise.resolve(response(primaryPayload({})))],
        ['unknown location', () => Promise.resolve(response(primaryPayload({ country: '未知地区', city: '-', prov: 'unknown' })))],
        ['unexpected IP', () => Promise.resolve(response(primaryPayload({ ip: '8.8.8.8', country: '错误位置' })))],
        ['malformed field types', () => Promise.resolve(response(primaryPayload({ country: {}, city: ['深圳'], prov: 123 })))],
        ['synchronous exception', () => { throw new TypeError('fetch unavailable'); }]
    ];
    for (const [name, primaryResponse] of failures) {
        await t.test(name, async () => {
            const page = browser({ respond: url => url.startsWith(primaryEndpoint)
                ? primaryResponse() : Promise.resolve(response({ country: 'China', country_code: 'CN', region: 'Guangdong', city: 'Shenzhen' })) });
            const output = page.add('203.0.113.10');
            await page.decorate();
            assert.equal(output.textContent, '中国 · Guangdong · Shenzhen');
            assert.equal(page.requests.length, 2);
            assert.equal(page.requests[1].url, backupEndpoint + '203.0.113.10.json');
            for (const request of page.requests) {
                assert.equal(request.options.credentials, 'omit');
                assert.equal(request.options.mode, 'cors');
                assert.equal(request.options.referrerPolicy, 'no-referrer');
            }
            const cached = JSON.parse(page.storage.get(cacheKey))['203.0.113.10'];
            assert.equal(cached.provider, 'geojs');
            assert.ok(Math.abs(cached.expires - Date.now() - 24 * 60 * 60 * 1000) < 1000);
        });
    }
});

test('times out and advances to GeoJS with or without AbortController', async t => {
    for (const abortController of [true, false]) {
        await t.test(abortController ? 'aborts request' : 'rejects without AbortController', async () => {
            const timers = manualTimers();
            let resolvePrimary;
            const page = browser({ abortController, timers, respond: url => url.startsWith(primaryEndpoint)
                ? new Promise(resolve => { resolvePrimary = resolve; })
                : Promise.resolve(response({ country: 'China', country_code: 'CN', city: 'Shenzhen' })) });
            const output = page.add('203.0.113.10');
            await page.decorate();
            assert.equal(output.attributes['data-ip-region-state'], 'loading');
            timers.expireNext();
            await flush();
            assert.equal(output.textContent, '中国 · Shenzhen');
            assert.equal(page.requests.length, 2);
            if (abortController) assert.equal(page.requests[0].options.signal.aborted, true);
            assert.equal(timers.pending.size, 0);
            resolvePrimary(response(primaryPayload({ country: '不应覆盖备用结果' })));
            await flush();
            assert.equal(output.textContent, '中国 · Shenzhen');
            assert.equal(JSON.parse(page.storage.get(cacheKey))['203.0.113.10'].provider, 'geojs');
        });
    }
});

test('times out while reading JSON rather than hanging the lookup queue', async () => {
    const timers = manualTimers();
    const page = browser({ timers, respond: url => url.startsWith(primaryEndpoint)
        ? Promise.resolve({ ok: true, json: () => new Promise(() => {}) })
        : Promise.resolve(response({ country: '中国', city: '深圳' })) });
    const output = page.add('203.0.113.10');
    await page.decorate();
    timers.expireNext();
    await flush();
    assert.equal(output.textContent, '中国 · 深圳');
    assert.equal(timers.pending.size, 0);
});

test('retries the primary after the short-lived backup cache expires', async () => {
    let now = Date.now();
    const ip = '203.0.113.10';
    const page = browser({ now: () => now, payload: { code: 403 } });
    const initial = page.add(ip);
    await page.decorate();
    assert.equal(initial.textContent, '中国 · Guangdong · Shenzhen');
    const cached = page.add(ip);
    await page.decorate();
    assert.equal(cached.textContent, initial.textContent);
    assert.equal(page.requests.length, 2);
    now += 24 * 60 * 60 * 1000 + 1;
    page.add(ip);
    await page.decorate();
    assert.equal(page.requests.length, 4);
    assert.equal(page.requests[2].url, primaryEndpoint + ip);
});

test('all-provider failures are not cached as locations and cool down for five minutes', async () => {
    let now = Date.now();
    const page = browser({ fail: true, now: () => now });
    const output = page.add('203.0.113.10');
    await page.decorate();
    assert.equal(output.textContent, '地域未知');
    assert.equal(output.flag.hidden, true);
    assert.equal(page.requests.length, 2);
    assert.equal(page.storage.has(cacheKey), false);
    page.add('203.0.113.10');
    await page.decorate();
    assert.equal(page.requests.length, 2);
    now += 5 * 60 * 1000 + 1;
    page.add('203.0.113.10');
    await page.decorate();
    assert.equal(page.requests.length, 4);
});

test('GeoJS ownership-only records do not invent a geographic location', async () => {
    const page = browser({ payload: { code: 403 }, backupPayload: {
        ip: '1.1.1.1', asn: 13335, latitude: 'nil', longitude: 'nil',
        organization: 'AS13335 Cloudflare, Inc.', organization_name: 'Cloudflare, Inc.'
    } });
    const output = page.add('1.1.1.1');
    await page.decorate();
    assert.equal(page.requests.length, 2);
    assert.equal(output.textContent, '地域未知');
    assert.equal(page.storage.has(cacheKey), false);
});

test('keeps at most four lookups in flight and deduplicates repeated addresses', async () => {
    const pending = [];
    const page = browser({ respond: () => new Promise(resolve => pending.push(resolve)) });
    const outputs = Array.from({ length: 10 }, (_, index) => page.add(`203.0.113.${index + 1}`));
    outputs.push(page.add('203.0.113.1'));
    await page.decorate();
    assert.equal(page.requests.length, 4);
    while (pending.length) {
        pending.splice(0).forEach(resolve => resolve(response(primaryPayload({ country: '中国' }))));
        await flush();
        assert.ok(pending.length <= 4);
    }
    assert.equal(page.requests.length, 10);
    for (const output of outputs) assert.equal(output.textContent, '中国');
});

test('treats provider markup as plain text rather than executing it', async () => {
    const page = browser({ payload: primaryPayload({ country: '<img src=x onerror=alert(1)>', city: '深圳' }) });
    const output = page.add('203.0.113.10');
    await page.decorate();
    assert.equal(output.textContent, '<img src=x onerror=alert(1)> · 深圳');
    assert.equal(output.attributes.title, output.textContent);
    assert.equal(output.flag.hidden, true);
});

test('maps provider country names and common aliases to bundled flags without an extra request', async t => {
    for (const [country, expected] of [
        ['中国大陆', 'cn'], ['香港', 'hk'], ['中国澳门', 'mo'], ['台湾', 'tw'],
        ['美国', 'us'], ['UK', 'gb'], ['韩国', 'kr'], ['俄罗斯联邦', 'ru'],
        ['巴西', 'br'], ['印度尼西亚', 'id'], ['德国', 'de'], ['南非', 'za'],
        ['Australia', 'au'], ['Japan', 'jp'], ['Canada', 'ca']
    ]) {
        await t.test(country, async () => {
            const page = browser({ payload: primaryPayload({ country }) });
            const output = page.add('203.0.113.10');
            await page.decorate();
            assert.equal(output.textContent, country);
            assert.equal(output.flag.hidden, false);
            assert.equal(output.flag.attributes.src, `/static/img/flags/4x3/${expected}.svg`);
            assert.equal(page.requests.length, 1);
        });
    }
});

test('maps common country names without Intl and keeps flag images under the web path prefix', async () => {
    const page = browser({ intl: {}, basePath: '/nps/', payload: primaryPayload({ country: '中国' }) });
    const output = page.add('203.0.113.10');
    await page.decorate();
    assert.equal(output.flag.attributes.src, '/nps/static/img/flags/4x3/cn.svg');
    assert.equal(output.textContent, '中国');
});

test('uses validated GeoJS country codes for IPv6 flags and persists them with the backup result', async () => {
    const ip = '2606:4700:4700::1111';
    const page = browser({ payload: { code: 403 }, backupPayload: { ip, country: 'United States', country_code: 'us' } });
    const output = page.add(`[${ip}]:8024`);
    await page.decorate();
    assert.equal(output.textContent, '美国');
    assert.equal(output.flag.attributes.src, '/static/img/flags/4x3/us.svg');
    assert.equal(JSON.parse(page.storage.get(cacheKey))[ip].countryCode, 'us');
    assert.equal(page.requests.length, 2);
});

test('does not invent a flag when only city, unknown country or invalid country code is available', async () => {
    for (const data of [{ city: '深圳' }, { country: '未知国家' }, { country: 'Unknown country', country_code: '../../x' }, { country: 'Unknown country', country_code: 'ZZ' }]) {
        const page = browser({ payload: primaryPayload(data) });
        const output = page.add('203.0.113.10');
        await page.decorate();
        assert.equal(output.flag.hidden, true);
        assert.equal(output.flag.attributes.src, undefined);
        assert.equal(page.requests.length, 1);
    }
});

test('leaves geolocation text intact if flag images are unavailable or an older template has no flag', async () => {
    const page = browser();
    const output = page.add('203.0.113.10');
    await page.decorate();
    output.flag.onerror();
    assert.equal(output.flag.hidden, true);
    assert.equal(output.flag.attributes.src, undefined);
    assert.equal(output.textContent, '中国 · 广东 · 深圳');
    const old = browser({ withFlag: false });
    const oldOutput = old.add('203.0.113.10');
    await old.decorate();
    assert.equal(oldOutput.textContent, output.textContent);
});

test('all flag codes have local SVG assets and the upstream MIT attribution is included', async () => {
    const directory = path.join(__dirname, '../static/img/flags/4x3');
    const files = fs.readdirSync(directory).filter(name => /^[a-z]{2}\.svg$/.test(name));
    assert.equal(files.length, 250);
    for (const file of files) {
        const svg = fs.readFileSync(path.join(directory, file), 'utf8');
        assert.match(svg, /<svg\b/);
        assert.doesNotMatch(svg, /<script\b|\bon\w+\s*=|(?:href|src)=["'](?:https?:|data:|javascript:)/i);
        const code = file.slice(0, 2);
        const page = browser({ payload: primaryPayload({ country_code: code.toUpperCase(), country: code }) });
        const output = page.add('203.0.113.10');
        await page.decorate();
        assert.equal(output.flag.attributes.src, `/static/img/flags/4x3/${file}`);
    }
    const license = fs.readFileSync(path.join(__dirname, '../static/licenses/flag-icons-7.6.0-MIT.txt'), 'utf8');
    assert.match(license, /Copyright \(c\) 2013 Panayiotis Lipiridis/);
});
