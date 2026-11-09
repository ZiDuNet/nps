const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

const script = fs.readFileSync(path.join(__dirname, '../static/js/nps-client-filter.js'), 'utf8');
const language = fs.readFileSync(path.join(__dirname, '../static/js/language.js'), 'utf8');

// A small native-select model exercises the actual component and loader without
// requiring a third-party DOM package or talking to any production endpoint.
function fixture({ english = false, form = true } = {}) {
    let document;
    class Element {
        constructor(tag) {
            this.tagName = tag.toUpperCase();
            this.children = [];
            this.attributes = {};
            this.listeners = {};
            this._text = '';
            this._value = '';
            this._selected = false;
            this.disabled = false;
            this.required = false;
            this.offsetParent = {};
            this.style = { removeProperty() {} };
            this.className = '';
            this.classList = {
                contains: name => this.className.split(/\s+/).includes(name),
                add: name => { if (!this.classList.contains(name)) this.className = `${this.className} ${name}`.trim(); },
                toggle: (name, enabled) => {
                    if (enabled) this.classList.add(name);
                    else this.className = this.className.split(/\s+/).filter(value => value !== name).join(' ');
                },
                remove: (...names) => { names.forEach(name => this.classList.toggle(name, false)); }
            };
        }
        get textContent() { return this._text + this.children.map(child => child.textContent).join(''); }
        set textContent(value) { this._text = String(value); this.children = []; }
        setAttribute(name, value) { this.attributes[name] = String(value); }
        getAttribute(name) { return this.attributes[name] ?? null; }
        hasAttribute(name) { return name in this.attributes; }
        removeAttribute(name) { delete this.attributes[name]; }
        addEventListener(name, listener) { (this.listeners[name] ||= []).push(listener); }
        dispatchEvent(event) {
            event.currentTarget = this;
            event.target ||= this;
            for (const listener of this.listeners[event.type] || []) listener(event);
            return !event.defaultPrevented;
        }
        appendChild(child) {
            if (child.tagName === 'FRAGMENT') {
                [...child.children].forEach(item => this.appendChild(item));
                return child;
            }
            if (child.parentNode) child.parentNode.children = child.parentNode.children.filter(item => item !== child);
            this.children.push(child);
            child.parentNode = this;
            if (this.tagName === 'SELECT' && child.tagName === 'OPTION') {
                if (child.selected) this.children.forEach(item => { if (item !== child) item._selected = false; });
                if (!this.children.some(item => item.selected)) child._selected = true;
            }
            return child;
        }
        insertBefore(child, reference) {
            this.appendChild(child);
            this.children.splice(this.children.indexOf(child), 1);
            this.children.splice(reference ? this.children.indexOf(reference) : this.children.length, 0, child);
        }
        get firstChild() { return this.children[0]; }
        get options() { return this.children; }
        get selected() { return this._selected; }
        set selected(value) {
            this._selected = !!value;
            if (value && this.parentNode?.tagName === 'SELECT') {
                this.parentNode.children.forEach(child => { if (child !== this) child._selected = false; });
            }
        }
        get selectedIndex() { return this.children.findIndex(child => child.selected); }
        set selectedIndex(index) { this.children.forEach((child, position) => { child._selected = index === position; }); }
        get index() { return this.parentNode?.children.indexOf(this) ?? -1; }
        get value() {
            return this.tagName === 'SELECT' ? (this.children.find(child => child.selected)?.value || '') : this._value;
        }
        set value(value) {
            if (this.tagName === 'SELECT') this.children.forEach(child => { child._selected = child.value === String(value); });
            else this._value = String(value);
        }
        contains(element) { return this === element || this.children.some(child => child.contains(element)); }
        closest() { return null; }
        querySelector() { return null; }
        getBoundingClientRect() { return { top: 100, bottom: 140, right: 500, width: 400, height: 200 }; }
        get scrollHeight() { return this.children.length * 40; }
        focus() { document.activeElement = this; }
        scrollIntoView() {}
    }
    class DOMEvent {
        constructor(type, options = {}) { this.type = type; Object.assign(this, options); }
        preventDefault() { this.defaultPrevented = true; }
    }
    document = {
        documentElement: { lang: 'zh-CN' },
        createElement: tag => new Element(tag),
        createDocumentFragment: () => new Element('fragment'),
        getElementById: () => null,
        addEventListener() {}, activeElement: null
    };
    const observers = [];
    const requests = [];
    const window = {
        npsIsEnglish: () => english,
        innerWidth: 1280, innerHeight: 900,
        addEventListener() {}, setTimeout,
        MutationObserver: class {
            constructor(callback) { this.callback = callback; observers.push(this); }
            observe(element, options) { this.element = element; this.options = options; }
        },
        jQuery: {
            ajax(settings) {
                const callbacks = {};
                const request = {
                    settings,
                    done(callback) { callbacks.done = callback; return request; },
                    fail(callback) { callbacks.fail = callback; return request; },
                    always(callback) { callbacks.always = callback; return request; },
                    resolve(data) { callbacks.done?.(data); callbacks.always?.(); },
                    reject() { callbacks.fail?.(); callbacks.always?.(); }
                };
                requests.push(request);
                return request;
            }
        }
    };
    const scope = { window, document, Event: DOMEvent };
    vm.createContext(scope);
    vm.runInContext(script, scope);
    const parent = new Element('div');
    const select = new Element('select');
    select.name = 'client_id';
    select.required = form;
    if (form) select.setAttribute('data-nps-client-select', '');
    const label = new Element('label');
    label.textContent = '所属客户端';
    select.labels = [label];
    parent.appendChild(select);
    function option(value, text, online) {
        const option = new Element('option');
        option.value = value; option.textContent = text;
        if (online !== undefined) option.setAttribute('data-client-online', online);
        select.appendChild(option);
        return option;
    }
    return { window, document, select, option, requests, observers, scope, label, DOMEvent, setEnglish(value) { english = value; } };
}

const clients = [
    { Id: 11, Remark: '办公电脑', UserName: 'alice', UserRemark: '研发部', IsConnect: true },
    { Id: 22, Remark: '备用节点', UserName: 'bob', IsConnect: false }
];
function load(ui, settings = {}) {
    ui.window.NpsClientFilter.load(ui.select, { url: '/console/client/list', ...settings });
    return ui.requests[0];
}

test('form selector reuses the listbox with names, ID, owner, explicit status and native submission value', () => {
    const ui = fixture();
    const request = load(ui, { selectedId: 22 });
    assert.equal(request.settings.url, '/console/client/list');
    assert.deepEqual(JSON.parse(JSON.stringify(request.settings.data)), { order: 'asc', offset: 0, limit: 999 });
    assert.equal(ui.select.__npsClientFilter.trigger.getAttribute('aria-busy'), 'true');
    request.resolve({ rows: clients });
    const component = ui.select.__npsClientFilter;
    assert.equal(ui.select.name, 'client_id');
    assert.equal(ui.select.value, '22');
    assert.match(component.label.textContent, /备用节点ID 22bob/);
    assert.match(component.trigger.getAttribute('aria-label'), /所属客户端: 备用节点 · ID 22 \| bob, 离线/);
    assert.match(component.options[0].title, /办公电脑 · ID 11 \| 研发部 \(alice\)/);
    assert.equal(component.options[0].getAttribute('aria-label'), '办公电脑 · ID 11 | 研发部 (alice), 在线');
    assert.equal(component.listbox.getAttribute('aria-required'), 'true');
    assert.match(component.options[0].textContent, /在线/);
    assert.match(component.options[1].textContent, /离线/);
    assert.equal(component.trigger.getAttribute('aria-busy'), null);
    assert.ok(component.wrapper.classList.contains('nps-client-filter--form'));
    let changes = 0;
    ui.select.addEventListener('change', () => { changes++; });
    component.choose('11');
    assert.equal(ui.select.value, '11');
    assert.equal(changes, 1);
    assert.match(component.trigger.getAttribute('aria-label'), /研发部 \(alice\), 在线/);
    component.choose('22'); // Offline clients can still carry saved rules.
    assert.equal(ui.select.value, '22');
});

test('client and owner metadata are text nodes, invalid IDs are dropped and missing names have localized fallbacks', () => {
    const ui = fixture({ english: true });
    load(ui).resolve({ rows: [
        null, { Id: -1 }, { Id: 1.5 }, { Id: 'bad' },
        { Id: 4, Remark: '<img onerror=alert(1)>', UserName: '<script>user</script>', IsConnect: true },
        { Id: 5 }
    ] });
    const component = ui.select.__npsClientFilter;
    assert.equal(ui.select.options.length, 2);
    assert.match(component.options[0].textContent, /<img onerror=alert\(1\)>/);
    assert.match(component.options[0].textContent, /<script>user<\/script>/);
    assert.equal(component.options[0].children[0].children[0].children[0].tagName, 'SPAN');
    assert.match(component.options[1].title, /Unnamed client · ID 5 \| Unassigned/);
    assert.match(component.options[1].textContent, /Offline/);
    assert.equal(ui.select.value, '4');
});

test('requested preselection is kept; an unavailable requested client requires an explicit new selection', () => {
    for (const selectedId of [11, '22']) {
        const ui = fixture();
        load(ui, { selectedId }).resolve({ rows: clients });
        assert.equal(ui.select.value, String(selectedId));
    }
    const ui = fixture();
    load(ui, { selectedId: 99 }).resolve({ rows: clients });
    assert.equal(ui.select.value, '');
    assert.match(ui.select.__npsClientFilter.label.textContent, /请选择可用的客户端/);
    ui.select.__npsClientFilter.choose('22');
    assert.equal(ui.select.value, '22');
});

test('edit responses cannot silently move a rule when its original client is missing or the request fails', () => {
    for (const response of [{ rows: clients }, { rows: [] }, { unexpected: true }, null]) {
        const ui = fixture();
        ui.option('99', '原客户端');
        const request = load(ui, { selectedId: 99, preserveSelection: true });
        if (response === null) request.reject();
        else request.resolve(response);
        assert.equal(ui.select.value, '99');
        assert.equal(ui.select.options.length, 1);
        assert.equal(ui.select.__npsClientFilter.label.textContent, '原客户端');
        assert.equal(ui.select.__npsClientFilter.trigger.getAttribute('aria-busy'), null);
    }
});

test('empty, malformed and failed create responses remain invalid and show a useful message', () => {
    for (const response of [{ rows: [] }, { unexpected: true }, null]) {
        const ui = fixture();
        const request = load(ui);
        if (response === null) request.reject();
        else request.resolve(response);
        assert.equal(ui.select.value, '');
        assert.match(ui.select.__npsClientFilter.label.textContent, /暂无可选客户端|加载客户端失败/);
        assert.doesNotMatch(ui.select.__npsClientFilter.statusSlot.textContent, /在线|离线/);
    }
});

test('plain existing filters retain their original labels and online badges', () => {
    const ui = fixture({ form: false });
    ui.option('0', '全部客户端');
    ui.option('11', '办公电脑（ID 11）', true);
    ui.option('22', '备用节点（ID 22）', false);
    const component = ui.window.NpsClientFilter.enhance(ui.select);
    assert.equal(component.label.textContent, '全部客户端');
    assert.equal(component.statusSlot.textContent, '');
    assert.ok(!component.wrapper.classList.contains('nps-client-filter--form'));
    component.choose('11');
    assert.equal(component.label.textContent, '办公电脑（ID 11）');
    assert.equal(component.statusSlot.textContent, '在线');
    component.choose('22');
    assert.equal(component.statusSlot.textContent, '离线');
});

test('keyboard selection, escape, label focus and disabled state stay synchronized', () => {
    const ui = fixture();
    load(ui).resolve({ rows: clients });
    const component = ui.select.__npsClientFilter;
    ui.label.dispatchEvent(new ui.DOMEvent('click'));
    assert.equal(ui.document.activeElement, component.trigger);
    component.trigger.dispatchEvent(new ui.DOMEvent('keydown', { key: 'ArrowDown' }));
    assert.equal(component.trigger.getAttribute('aria-expanded'), 'true');
    assert.equal(ui.document.activeElement, component.options[1]);
    component.options[1].dispatchEvent(new ui.DOMEvent('keydown', { key: 'Enter' }));
    assert.equal(ui.select.value, '22');
    assert.equal(component.trigger.getAttribute('aria-expanded'), 'false');
    assert.equal(ui.document.activeElement, component.trigger);
    component.openMenu('Home');
    ui.document.activeElement.dispatchEvent(new ui.DOMEvent('keydown', { key: 'Escape' }));
    assert.equal(ui.document.activeElement, component.trigger);
    ui.select.options[0].disabled = true;
    component.choose('11');
    assert.equal(ui.select.value, '22');
    component.openMenu();
    ui.select.disabled = true;
    ui.observers[0].callback();
    assert.equal(component.trigger.disabled, true);
    assert.equal(component.open, false);
    component.choose('11');
    assert.equal(ui.select.value, '22');
});

test('native invalid events focus the visible selector and clear on a valid selection', () => {
    const ui = fixture();
    load(ui).resolve({ rows: [] });
    const component = ui.select.__npsClientFilter;
    const invalid = new ui.DOMEvent('invalid');
    ui.select.dispatchEvent(invalid);
    assert.equal(invalid.defaultPrevented, true);
    assert.equal(ui.document.activeElement, component.trigger);
    assert.equal(component.trigger.getAttribute('aria-invalid'), 'true');
    ui.window.npsSetFieldValidity = (field, valid) => {
        field.classList.toggle('is-invalid', !valid);
        field.__npsClientFilter.syncValidity();
    };
    const option = ui.option('11', '办公电脑');
    option.selected = true;
    ui.select.dispatchEvent(new ui.DOMEvent('change'));
    assert.equal(component.trigger.getAttribute('aria-invalid'), 'false');
});

test('shared form validation checks the visible selector, but skips disabled or genuinely hidden fields', () => {
    const ui = fixture();
    load(ui).resolve({ rows: [] });
    const component = ui.select.__npsClientFilter;
    ui.select.offsetParent = null; // Native source select is deliberately hidden.
    ui.scope.npsSetFieldValidity = () => {};
    ui.scope.npsFieldLabel = () => '所属客户端';
    ui.scope.npsNotify = () => {};
    ui.scope.npsIsEnglish = () => false;
    ui.scope.npsFormMessage = () => '请填写：';
    vm.runInContext(language.slice(language.indexOf('function validateNpsForm('), language.indexOf('\n$(function () {', language.indexOf('function validateNpsForm('))), ui.scope);
    const form = { querySelectorAll: () => [ui.select] };
    assert.equal(ui.scope.validateNpsForm(form, true), false);
    assert.equal(ui.document.activeElement, component.trigger);
    ui.select.disabled = true;
    assert.equal(ui.scope.validateNpsForm(form, true), true);
    ui.select.disabled = false;
    component.trigger.offsetParent = null;
    assert.equal(ui.scope.validateNpsForm(form, true), true);
    component.trigger.offsetParent = {};
    ui.option('11', '办公电脑').selected = true;
    assert.equal(ui.scope.validateNpsForm(form, true), true);
});

test('language refresh updates fallbacks and badges without losing selection or keyboard focus', () => {
    const ui = fixture();
    load(ui, { selectedId: 22 }).resolve({ rows: clients });
    const component = ui.select.__npsClientFilter;
    component.openMenu('Home');
    ui.setEnglish(true);
    component.refresh();
    assert.equal(ui.select.value, '22');
    assert.equal(component.statusSlot.textContent, 'Offline');
    assert.equal(ui.document.activeElement.getAttribute('data-value'), '11');
    assert.match(language, /find\('select\[data-nps-client-select\]'\)/);
});

test('menu width and position follow the viewport rather than the form card', () => {
    const ui = fixture();
    ui.window.innerWidth = 375;
    ui.window.innerHeight = 720;
    load(ui).resolve({ rows: clients });
    const component = ui.select.__npsClientFilter;
    component.trigger.getBoundingClientRect = () => ({ top: 500, bottom: 544, left: 35, right: 332, width: 297 });
    component.listbox.getBoundingClientRect = () => ({ width: 351, height: 80 });
    component.openMenu();
    assert.equal(component.listbox.style.maxWidth, '351px');
    assert.ok(parseInt(component.listbox.style.left, 10) >= 12);
    assert.ok(parseInt(component.listbox.style.left, 10) + 351 <= 363);
    assert.ok(parseInt(component.listbox.style.top, 10) + 80 <= 708);
});
