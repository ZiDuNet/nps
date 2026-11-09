const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

const template = fs.readFileSync(path.join(__dirname, '../views/client/list.html'), 'utf8');
const helpers = template.slice(template.indexOf('    function clientAddressMarkup'), template.indexOf("    $('#table').npsTable({"));
function clientUI(english = false) {
    const scope = {
        npsIsEnglish: () => english,
        changeunit: value => `${value} B`,
        npsEscapeHtml: value => String(value).replace(/[&<>"']/g, character => ({
            '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'
        })[character])
    };
    vm.runInNewContext(helpers, scope);
    return scope;
}

test('client traffic column combines both byte totals without live-rate bindings', () => {
    const ui = clientUI();
    const result = ui.clientMetricMarkup({ Id: 12, Flow: { InletFlow: 1024, ExportFlow: 2048 } }, false);
    assert.match(result, /class="client-metric-pair client-traffic"/);
    assert.match(result, /↑.*1024 B.*↓.*2048 B/);
    assert.match(result, /入口：公网→内网/);
    assert.match(result, /出口：内网→公网/);
    assert.doesNotMatch(result, /data-live-client|B\/s/);
    assert.equal((template.match(/field: 'InletFlow'/g) || []).length, 1);
    assert.doesNotMatch(template, /field: 'ExportFlow'/);
});

test('live bandwidth keeps separate directions, per-second units and number-only update targets', () => {
    const ui = clientUI(true);
    const result = ui.clientMetricMarkup({ Id: 12, Rate: { UploadRate: 50, DownloadRate: 75 } }, true);
    assert.match(result, /data-live-client-id="12"/);
    assert.match(result, /<span data-live-client-up>50 B\/s<\/span>/);
    assert.match(result, /<span data-live-client-down>75 B\/s<\/span>/);
    // The poller replaces the number's textContent, not its accessible label.
    assert.match(result, /<span class="sr-only">Inlet: public → private <\/span><span data-live-client-up>/);
    assert.match(result, /Outlet: private → public/);
});

test('absent totals and rates remain zero instead of undefined or NaN', () => {
    const ui = clientUI();
    for (const live of [false, true]) {
        const result = ui.clientMetricMarkup({ Id: 12 }, live);
        assert.equal((result.match(/>0 B/g) || []).length, 2);
        assert.doesNotMatch(result, /NaN|undefined|null/);
    }
});

test('metric headings expose native tooltips, keyboard descriptions, units and sort semantics', () => {
    const ui = clientUI();
    const total = ui.clientMetricHeading(false);
    const live = ui.clientMetricHeading(true);
    for (const heading of [total, live]) {
        assert.match(heading, /tabindex="0"/);
        assert.match(heading, /title="[^"]*公网→内网；↓ 出口：内网→公网/);
        assert.match(heading, /aria-label="/);
        assert.match(heading, /data-title-en="/);
        assert.match(heading, /排序/);
    }
    assert.match(total, /累计字节数，非实时速度/);
    assert.match(live, /B\/s，每秒字节数/);
    assert.match(ui.clientMetricHeading(false), />累计流量<\/span>/);
    assert.match(clientUI(true).clientMetricHeading(true), />Live rate<\/span>/);
});

test('client address reserves a decorative flag beside text, escapes markup and remains visible on desktop', () => {
    const ui = clientUI();
    const address = ui.clientAddressMarkup('1.1.1.1');
    assert.match(address, /data-ip-region-flag width="16" height="12" alt="" aria-hidden="true" hidden/);
    assert.match(address, /data-ip-region-output/);
    assert.match(ui.clientAddressMarkup('\"><img src=x>'), /&quot;&gt;&lt;img src=x&gt;/);
    assert.doesNotMatch(ui.clientAddressMarkup('-'), /data-ip-region-flag/);
    assert.match(template, /class: 'client-list__address',\s*visible: true/);
    assert.match(template, /storageKey: 'client-v5'/);
});
