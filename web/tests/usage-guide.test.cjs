const assert = require('node:assert/strict');
const test = require('node:test');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const template = fs.readFileSync(path.join(__dirname, '../views/index/help.html'), 'utf8');
const layout = fs.readFileSync(path.join(__dirname, '../views/public/layout.html'), 'utf8');

test('help fills the available shell with flexible dimensions rather than fixed viewport deductions', () => {
    assert.match(template, /nps-help-viewport #nps-content \{[^}]*max-width: none;[^}]*min-height: 0;/);
    assert.match(template, /\.help-document-shell \{[^}]*flex: 1 1 0;[^}]*min-height: 0;/);
    assert.match(template, /\.help-document-frame \{[^}]*flex: 1 1 0;[^}]*min-height: 0;/);
    assert.match(template, /height: 100dvh/);
    assert.doesNotMatch(template, /height:\s*calc\(100vh\s*-/);
    assert.doesNotMatch(template, /max-width:\s*1440px|min-height:\s*(?:560|440)px/);
    assert.match(template, /\.help-document-header\[hidden\] \{ display: none;/);
});

test('help shell sizing is removed when content navigation returns to other pages', () => {
    const source = layout.slice(layout.indexOf('function npsSyncPageContext(root)'), layout.indexOf("npsSyncPageContext(document.getElementById('nps-content'))"));
    const classes = new Set();
    const context = vm.createContext({
        document: {
            body: {classList: {toggle(name, active) { if (active) classes.add(name); else classes.delete(name); }}},
            getElementById() { return null; }
        }
    });
    vm.runInContext(source, context);
    const help = {querySelector(selector) { return selector === '.help-document-page' ? {} : null; }};
    context.npsSyncPageContext(help);
    assert.ok(classes.has('nps-help-viewport'));
    context.npsSyncPageContext({querySelector() { return null; }});
    assert.ok(!classes.has('nps-help-viewport'));
    context.npsSyncPageContext(help);
    context.npsSyncPageContext(null);
    assert.ok(!classes.has('nps-help-viewport'));
});

test('guide retains isolated embedding and refresh without adding resize listeners', () => {
    const script = template.match(/<script>\n([\s\S]*)<\/script>/)[1];
    assert.doesNotThrow(() => new vm.Script(script));
    assert.match(script, /url\.searchParams\.set\('_nps_refresh'/);
    assert.match(template, /sandbox="{{\.help_iframe_sandbox}}"/);
    assert.match(template, /referrerpolicy="no-referrer"/);
    assert.doesNotMatch(script, /setInterval|addEventListener\(['"]resize/);
    assert.match(template, /\.help-document-meta \{ flex-basis: 100%; \}/);
});
