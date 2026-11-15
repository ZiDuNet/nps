const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const layout = fs.readFileSync(path.join(__dirname, '../views/public/layout.html'), 'utf8');
const source = layout.slice(layout.indexOf('function executeContentScripts(root)'), layout.indexOf('function focusNewPage()'));

function fixture() {
    const events = [], external = [];
    const scripts = [
        {src: '/static/js/nps-audit.js', attributes: [{name: 'src', value: '/static/js/nps-audit.js'}]},
        {src: '', attributes: [], textContent: 'initializeAudit();'}
    ];
    const root = {querySelectorAll: () => scripts, contains: node => scripts.includes(node)};
    const context = vm.createContext({Promise, window: {setTimeout, clearTimeout}, navigationSerial: 1, initializeAudit() { events.push('initialized'); }});
    context.document = {createElement() { return {setAttribute(name, value) { this[name] = value; }}; }};
    for (const original of scripts) {
        original.parentNode = {replaceChild(script) {
            if (script.src) { external.push(script); events.push('requested'); }
            else vm.runInContext(script.text, context);
        }};
    }
    vm.runInContext(source, context);
    return {context, events, external, root};
}

test('content navigation waits for the audit dependency before running its initializer', async () => {
    const f = fixture();
    const pending = f.context.executeContentScripts(f.root);
    await Promise.resolve(); await Promise.resolve();
    assert.deepEqual(f.events, ['requested']);
    assert.equal(f.external[0].async, false);
    f.external[0].onload();
    await pending;
    assert.deepEqual(f.events, ['requested', 'initialized']);
    assert.match(layout, /return executeContentScripts\(content\)\.then/);
});

test('failed dependency rejects navigation without initializing a broken audit table', async () => {
    const f = fixture();
    const pending = f.context.executeContentScripts(f.root);
    await Promise.resolve(); await Promise.resolve();
    f.external[0].onerror();
    await assert.rejects(pending, /page script load failed/);
    assert.deepEqual(f.events, ['requested']);
});

test('leaving a page during dependency loading cancels its remaining initializers', async () => {
    const f = fixture();
    const pending = f.context.executeContentScripts(f.root);
    await Promise.resolve(); await Promise.resolve();
    f.context.navigationSerial++;
    f.external[0].onload();
    await pending;
    assert.deepEqual(f.events, ['requested']);
});

test('recovery browser code parses and never sends plaintext passwords in email-code requests', () => {
    const js = fs.readFileSync(path.join(__dirname, '../static/js/nps-password-reset.js'), 'utf8');
    assert.doesNotThrow(() => new vm.Script(js));
    assert.match(js, /delete fields\.password/);
    assert.match(js, /encryptor\.encrypt\(JSON\.stringify/);
    assert.match(js, /timeout: 20000/);
    assert.match(js, /form\.reportValidity\(\)/);
    assert.match(js, /status\.textContent = text/);
    assert.match(js, /window\.clearInterval\(timer\)/);
});
