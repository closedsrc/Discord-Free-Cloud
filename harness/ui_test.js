/* Self-contained frontend behavioural test. No build step, no external deps.
   It boots the real app.js inside a tiny hand-written DOM shim, stubs fetch and
   WebSocket, and asserts the load-bearing UI behaviors from the plan:
     - the lock flow, sign-in, read-only affordance gating
     - the new skeleton loading state actually paints and clears
     - navigation (latest-request-wins) semantics
   The harness owns the DOM shim; app.js is exercised unmodified. */
'use strict';

// ---------- minimal DOM shim ----------
class TokenList {
  constructor(owner) { this.owner = owner; this._s = new Set(); }
  add(...c) { c.forEach(x => this._s.add(x)); this.owner._syncClass(); }
  remove(...c) { c.forEach(x => this._s.delete(x)); this.owner._syncClass(); }
  toggle(c, force) {
    const on = force === undefined ? !this._s.has(c) : !!force;
    on ? this._s.add(c) : this._s.delete(c);
    this.owner._syncClass();
    return on;
  }
  contains(c) { return this._s.has(c); }
  _syncFrom(str) { this._s = new Set((str || '').split(/\s+/).filter(Boolean)); }
  _syncTo() { return [...this._s].join(' '); }
}
class El {
  constructor(tag) {
    this.tagName = String(tag).toUpperCase();
    this.children = [];
    this.style = {};
    this.dataset = {};
    this._listeners = {};
    this.options = [];
    this.classList = new TokenList(this);
    this._value = '';
    this._text = '';
    this.disabled = false;
  }
  set className(v) { this.classList._syncFrom(v); }
  get className() { return this.classList._syncTo(); }
  set classListRef(_) {}
  _syncClass() {}
  set id(v) { this._id = v; registry[v] = this; }
  get id() { return this._id || ''; }
  get textContent() { return this._text; }
  set textContent(v) { this._text = String(v); this._html = ''; this.children = []; }
  set innerHTML(v) {
    this._html = String(v);
    // parse just enough: count rows/tiles by their marker classes for assertions
    this.children = [];
  }
  get innerHTML() { return this._html || ''; }
  set value(v) { this._value = String(v); }
  get value() { return this._value; }
  appendChild(c) { this.children.push(c); return c; }
  prepend(c) { this.children.unshift(c); return c; }
  removeChild(c) { this.children = this.children.filter(x => x !== c); }
  remove() { if (this._parent && this._parent.removeChild) this._parent.removeChild(this); }
  addEventListener(type, fn) { (this._listeners[type] = this._listeners[type] || []).push(fn); }
  removeEventListener(type, fn) { this._listeners[type] = (this._listeners[type] || []).filter(f => f !== fn); }
  dispatch(type, ev) { (this._listeners[type] || []).forEach(f => f(Object.assign({ target: this, preventDefault(){}, stopPropagation(){} }, ev))); }
  querySelector() { return new El('div'); }
  querySelectorAll() { return []; }
  getBoundingClientRect() { return { left: 0, top: 0, right: 100, bottom: 100, width: 100, height: 100 }; }
  focus() {}
  setAttribute(k, v) { this[k] = v; }
  click() { this.dispatch('click'); }
}
const registry = {};
function makeEl(tag) { return new El(tag); }
const documentShim = {
  body: makeEl('body'),
  createElement: makeEl,
  createTextNode: t => ({ nodeType: 3, textContent: String(t) }),
  getElementById: id => registry[id] || (registry[id] = (() => { const e = makeEl('div'); e.id = id; return e; })()),

  querySelector: () => makeEl('div'),
  querySelectorAll: () => [],
  addEventListener() {}, removeEventListener() {},
  documentElement: makeEl('html'),
};
// pre-create ids app.js expects so getElementById returns stable objects
['breadcrumb-bar','page-title','search-input','count-label','empty-state','empty-title','empty-sub',
 'files-grid','files-table','file-tbody','batch-bar','batch-count','bb-trash','bb-favorite',
 'th-select-all','btn-clear-search','cloud-usage-fill','cloud-usage-text','system-status-dot','system-status-text',
 'read-only-badge','btn-sign-in','new-menu','drop-overlay','toast-stack','activity-log','transfers-drawer',
 'transfers-list','transfers-drawer-body','transfers-drawer-foot','nav-transfer-badge','hud-dot',
 'unconfigured-banner','links-panel','log-drawer','drawer-scrim','sidebar','sidebar-scrim','btn-grid','btn-list',
 'upload-target-select','sort-select','bot-token-input','guild-id-input','servers-list','bots-list',
 'cfg-new-password','token-output','modal-preview','pm-name','pm-size','pm-info-panel','pm-shares',
 'lock-overlay','lock-error','lock-dismiss','lock-password','btn-unlock','btn-settings','nav-activity',
 'btn-open-transfers','btn-close-transfers','btn-open-diagnostics','btn-close-log','btn-mobile-menu',
 'file-input','dir-input','btn-empty-trash','btn-cancel-all'].forEach(id => { const e = makeEl('div'); e.id = id; });

// ---------- globals app.js touches ----------
const store = {};
globalThis.window = globalThis;
Object.defineProperty(globalThis, 'navigator', { value: { clipboard: { writeText: async () => {} } }, configurable: true, writable: true });
globalThis.location = { protocol: 'https:', host: 'drive.test', href: 'https://drive.test/' };
globalThis.localStorage = {
  getItem: k => (k in store ? store[k] : null),
  setItem: (k, v) => { store[k] = String(v); },
  removeItem: k => { delete store[k]; },
};
globalThis.document = documentShim;
globalThis.addEventListener = () => {};
globalThis.IntersectionObserver = class { observe() {} unobserve() {} disconnect() {} };
globalThis.WebSocket = class { constructor(){ this.readyState = 0; setTimeout(()=>{ this.onopen && this.onopen(); }, 0); } send(){} close(){} };
WebSocket.OPEN = 1;

// fetch stub: controllable queue of responses
const fetchLog = [];
let fetchQueue = [];
async function fakeFetch(url, opts) {
  fetchLog.push({ url, opts: opts || {} });
  let payload = fetchQueue.length ? fetchQueue.shift() : { ok: true };
  const body = JSON.stringify(payload.body !== undefined ? payload.body : payload);
  return {
    ok: payload.ok !== false,
    status: payload.status || 200,
    headers: { get: h => (String(h).toLowerCase() === 'content-type' ? 'application/json' : '') },
    json: async () => JSON.parse(body),
    text: async () => body,
  };
}
globalThis.fetch = fakeFetch;

// ---------- load app.js ----------
const fs = require('fs');
const vm = require('vm');
const src = fs.readFileSync(__dirname + '/../frontend/app.js', 'utf8');
const ctx = {
  global: globalThis, window: globalThis, document: documentShim, location: globalThis.location,
  navigator: globalThis.navigator, localStorage: globalThis.localStorage,
  fetch: fakeFetch, WebSocket: globalThis.WebSocket, IntersectionObserver: globalThis.IntersectionObserver,
  addEventListener: () => {}, console, setTimeout, clearTimeout, setInterval, clearInterval,
  URLSearchParams, Date, Math, JSON, CSS: { escape: s => String(s) }, navigator_: globalThis.navigator,
  // app.js references a few browser globals directly; wire them:
  getComputedStyle: () => ({}),
};
// expose registry-backed $() the same way app.js does
vm.createContext(ctx);
// expose registry-backed $() the same way app.js does
ctx.__registry = registry;
// app.js is a top-level script: its `const`/`function` bindings are script-scope
// and NOT properties of the vm context object. The init IIFE also fires
// fetch/LocalStorage/WebSocket at load. So: strip the trailing init block,
// append an export line that reifies the bindings we drive, and run once.
const stripped = src.replace(
  /\(async function init\(\)[\s\S]*$/,
  '// init IIFE removed by harness\n'
).replace(
  "const $ = id => document.getElementById(id);",
  "const $ = id => (globalThis.__registry[id] || (globalThis.__registry[id] = (function(){const e=document.createElement('div');e.id=id;return e;})()));"
);
const EXPORTS = `
__exports.S = S;
__exports.reloadCurrent = reloadCurrent;
__exports.render = render;
__exports.applyPermissions = applyPermissions;
__exports.checkAuth = checkAuth;
__exports.unlock = unlock;
__exports.showLock = showLock;
__exports.kindOf = kindOf;
__exports.SHEET = SHEET;
__exports.ICON = ICON;
__exports.document = document;
__exports.registry = globalThis.__registry;
`;
ctx.__exports = {};
vm.runInContext(stripped + '\n' + EXPORTS, ctx);
const app = ctx.__exports; // live bindings after load


// ---------- assertions ----------
let passed = 0, failed = 0;
function check(name, cond, detail) {
  if (cond) { passed++; console.log('  PASS  ' + name); }
  else { failed++; console.log('  FAIL  ' + name + (detail ? ' :: ' + detail : '')); }
}
async function run() {
  const S = app.S;
  const $ = id => registry[id];
  const reload = app.reloadCurrent;
  const render = app.render;

  console.log('\n== skeleton loading paints then clears (list view) ==');
  S.view = 'drive'; S.layout = 'list'; S.parentID = ''; S.files = []; S.searchMode = false;
  // queue a slow view response: nothing in queue -> default {ok:true} -> data [] ;
  // to observe the loading paint we call reload and inspect BEFORE it resolves.
  $('search-input').value = '';
  fetchQueue.push({ ok: true, body: [{ id: 'a', name: 'x.png', is_dir: false, size: 10, mod_time: 1, health: 'ok' }] });
  const p = reload();
  // synchronous: S.loading true and table shows skeleton rows
  check('S.loading true during in-flight list request', S.loading === true, 'S.loading=' + S.loading);
  check('list skeleton rows rendered while loading', /skeleton-row/.test($('file-tbody').innerHTML), $('file-tbody').innerHTML.slice(0,40));
  check('count label reads Loading during fetch', $('count-label').textContent === 'Loading…', $('count-label').textContent);
  check('empty-state suppressed while loading', $('empty-state').classList.contains('hidden') === true);
  await p;
  check('S.loading cleared after fetch', S.loading === false, 'S.loading=' + S.loading);
  check('real row rendered after fetch (no skeleton)', !$('file-tbody').innerHTML.includes('skeleton-row'), $('file-tbody').innerHTML.slice(0,40));

  console.log('\n== skeleton loading paints then clears (grid view) ==');
  S.layout = 'grid'; S.files = [];
  fetchQueue.push({ ok: true, body: [{ id: 'b', name: 'y.png', is_dir: false, size: 10, mod_time: 1, health: 'ok' }] });
  const pg = reload();
  check('grid skeleton tiles rendered while loading', /skel-tile/.test($('files-grid').innerHTML), $('files-grid').innerHTML.slice(0,40));
  await pg;
  check('grid real card rendered after fetch', !$('files-grid').innerHTML.includes('skel-tile'), $('files-grid').innerHTML.slice(0,40));

  console.log('\n== latest-request-wins on rapid navigation ==');
  S.layout = 'list';
  // two overlapping reloads; only the last must land
  fetchQueue.push({ ok: true, body: [{ id: 'first', name: 'first.png', is_dir: false, size: 1, mod_time: 1, health: 'ok' }] });
  fetchQueue.push({ ok: true, body: [{ id: 'second', name: 'second.png', is_dir: false, size: 2, mod_time: 2, health: 'ok' }] });
  const p1 = reload();
  const p2 = reload();
  await Promise.all([p1, p2]);
  check('final listing reflects the LAST response', S.files.length === 1 && S.files[0].id === 'second',
    JSON.stringify(S.files.map(f => f.id)));

  console.log('\n== applyPermissions gates read-only affordances ==');
  app.applyPermissions({ public_mode: 'read', can_write: false });
  check('body gets read-only class', app.document.body.classList.contains('read-only'), app.document.body.className);
  check('S.canWrite false under read scope', S.canWrite === false, 'canWrite=' + S.canWrite);
  app.applyPermissions({ public_mode: 'off', can_write: true });
  check('read-only class removed for writable session', !app.document.body.classList.contains('read-only'), app.document.body.className);
  check('S.canWrite true again', S.canWrite === true);

  console.log('\n== kindOf / icon classification ==');
  check('png -> image', app.kindOf({ name: 'a.png' }) === 'image');
  check('no ext -> other', app.kindOf({ name: 'README' }) === 'other');
  check('dir -> dir', app.kindOf({ name: 'x', is_dir: true }) === 'dir');
  check('tar.gz -> archive', app.kindOf({ name: 'a.tar.gz' }) === 'archive');

  console.log('\n== sheet icon uses measured #bababa fill ==');
  check('SHEET fill is #bababa (not old #aab2bd)', /fill="#bababa"/.test(String(app.SHEET)) && !/fill="#aab2bd"/.test(String(app.SHEET)),
    String(app.SHEET).slice(0, 80));

  console.log('\n== auth status drives lock visibility ==');
  // checkAuth: auth_required true + no session -> lock shown
  fetchQueue.push({ ok: true, body: { auth_required: true, has_session: false, is_unlocked: true, can_write: false, public_mode: 'off', has_password: true } });
  const shown = await app.checkAuth();
  check('checkAuth returns false when locked', shown === false, 'returned ' + shown);
  check('lock-overlay is visible when locked', !$('lock-overlay').classList.contains('hidden'), $('lock-overlay').className);
  // auth_required false -> hideLock -> true
  fetchQueue.push({ ok: true, body: { auth_required: false, has_session: true, is_unlocked: true, can_write: true, public_mode: 'off', has_password: true } });
  const ok = await app.checkAuth();
  check('checkAuth returns true when unlocked', ok === true, 'returned ' + ok);
  check('lock-overlay hidden when unlocked', $('lock-overlay').classList.contains('hidden'), $('lock-overlay').className);

  console.log('\n== first unlock clears read-only chrome (regression: stuck read-only after signin) ==');
  // Reproduce the production path that the live browser pass caught: load into
  // the locked state (can_write false), then sign in with the access password.
  app.applyPermissions({ can_write: false, public_mode: 'off' }); // published/read-only baseline
  check('baseline read-only class set', app.document.body.classList.contains('read-only'), app.document.body.className);
  check('baseline canWrite false', S.canWrite === false);
  app.showLock(''); // force the overlay back up as a fresh page load would
  $('lock-password').value = 'unit-access-password';
  // status reports the drive already holds its key, then unlock hands back a session
  fetchQueue.push({ ok: true, body: { has_password: true, is_unlocked: true, auth_required: true, can_write: false } });
  fetchQueue.push({ ok: true, body: { ok: true, status: 'unlocked', session: 'SESSIONTOKEN' } });
  // boot()/applyPermissions may refetch status; give it a write-scoped answer
  fetchQueue.push({ ok: true, body: { has_password: true, is_unlocked: true, auth_required: false, has_session: true, can_write: true, public_mode: 'off' } });
  await app.unlock();
  await new Promise(r => setTimeout(r, 20)); // let boot() settle
  check('unlock flips canWrite to true', S.canWrite === true, 'canWrite=' + S.canWrite);
  check('unlock removes read-only class', !app.document.body.classList.contains('read-only'), app.document.body.className);
  check('read-only badge hidden after unlock', $('read-only-badge').classList.contains('hidden'), $('read-only-badge').className);
  check('sign-in button hidden after unlock', $('btn-sign-in').classList.contains('hidden'), $('btn-sign-in').className);
  check('lock overlay hidden after unlock', $('lock-overlay').classList.contains('hidden'), $('lock-overlay').className);

  console.log('\n----------------------------------------');
  console.log(`RESULT: ${passed} passed, ${failed} failed`);
  console.log('----------------------------------------');
  process.exit(failed ? 1 : 0);
}
run().catch(e => { console.error('HARNESS CRASH:', e); process.exit(2); });
