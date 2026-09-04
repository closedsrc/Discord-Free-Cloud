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

// fetch stub. Entries may carry a `url` substring so a queued answer is bound to
// the call it was written for; without that, a background poll (watchJob hitting
// /api/jobs) steals the response the test queued for a listing.
const fetchLog = [];
let fetchQueue = [];
const DEFAULT_BODY = url => (/\/api\/jobs/.test(url) ? [] : /\/api\/files\/view|\/api\/files\b/.test(url) ? [] : { ok: true });
async function fakeFetch(url, opts) {
  fetchLog.push({ url, opts: opts || {} });
  let idx = fetchQueue.findIndex(q => q.url && url.includes(q.url));
  if (idx < 0) idx = fetchQueue.findIndex(q => !q.url && !/\/api\/jobs/.test(url));
  const payload = idx >= 0 ? fetchQueue.splice(idx, 1)[0] : { ok: true, body: DEFAULT_BODY(url) };
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

// FormData + XMLHttpRequest stubs. uploadOne uses XHR (not fetch) because it is
// the only way to observe upload progress, so the harness has to drive the
// progress/onload callbacks by hand. `xhrControl.pending` holds the live request
// so a test can emit progress mid-flight and assert what the UI showed.
globalThis.FormData = class { constructor(){ this.parts = []; } append(k, v){ this.parts.push([k, v]); } };
globalThis.File = class { constructor(bits, name, opts){ this.name = name; this.size = (opts && opts.size) || 1024; this.type = (opts && opts.type) || ''; } };
const xhrControl = { pending: null, log: [] };
globalThis.XMLHttpRequest = class {
  constructor() { this.upload = {}; this.status = 0; this.responseText = ''; this._headers = {}; }
  open(method, url) { this.method = method; this.url = url; }
  setRequestHeader(k, v) { this._headers[k] = v; }
  send(body) { this.body = body; xhrControl.pending = this; xhrControl.log.push(this.method + ' ' + this.url); }
  // helpers the test calls
  emitProgress(loaded, total) { this.upload.onprogress && this.upload.onprogress({ lengthComputable: true, loaded, total }); }
  finish(status, obj) { this.status = status; this.responseText = JSON.stringify(obj); this.onload && this.onload(); }
  fail() { this.onerror && this.onerror(); }
};

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
  FormData: globalThis.FormData, XMLHttpRequest: globalThis.XMLHttpRequest, File: globalThis.File,
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
__exports.uploadOne = uploadOne;
__exports.uploadFiles = uploadFiles;
__exports.allTransfers = allTransfers;
__exports.applyTelemetry = applyTelemetry;
__exports.openPreview = openPreview;
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

  console.log('\n== upload feedback is immediate (regression: silence for the whole send leg) ==');
  // The reported bug: pick a file, nothing happens. The transfer used to be
  // registered only AFTER the multipart body finished uploading, so a large file
  // meant minutes with no toast, no badge, no card and no row.
  S.canWrite = true; S.view = 'drive'; S.parentID = '';
  app.allTransfers.clear();
  $('toast-stack').children = [];
  xhrControl.pending = null;
  const file = new File(['x'], 'holiday.zip', { size: 40 * 1024 * 1024 });
  const upload = app.uploadFiles([file]);       // deliberately not awaited yet
  await new Promise(r => setTimeout(r, 5));
  check('a transfer exists before the request completes', app.allTransfers.size === 1, 'size=' + app.allTransfers.size);
  check('transfer badge is visible immediately', !$('nav-transfer-badge').classList.contains('hidden'), $('nav-transfer-badge').className);
  check('badge counts the in-flight upload', $('nav-transfer-badge').textContent === '1', $('nav-transfer-badge').textContent);
  check('drawer opened so the card is on screen', !$('transfers-drawer').classList.contains('closed'), $('transfers-drawer').className);
  check('a toast announced the upload', $('toast-stack').children.length > 0, 'toasts=' + $('toast-stack').children.length);
  check('the card is drawn (not the empty state)', !/No transfers yet/.test($('transfers-drawer-body').innerHTML), $('transfers-drawer-body').innerHTML.slice(0, 60));
  check('the card names the send leg', /Sending/.test($('transfers-drawer-body').innerHTML), $('transfers-drawer-body').innerHTML.slice(0, 200));
  check('XHR was used for the upload', xhrControl.log.some(l => l.includes('/api/upload/file')), JSON.stringify(xhrControl.log));

  // mid-flight progress must move the bar
  const firstEntry = () => [...app.allTransfers.values()][0];
  xhrControl.pending.emitProgress(10 * 1024 * 1024, 40 * 1024 * 1024);
  check('upload progress updates the transfer', Math.round(firstEntry().progress) === 25, 'progress=' + firstEntry().progress);
  check('upload progress reports bytes sent', firstEntry().processed_bytes === 10 * 1024 * 1024, 'bytes=' + firstEntry().processed_bytes);

  // server accepts -> the card must move to the job id, not duplicate
  fetchQueue.push({ ok: true, body: [] });      // watchJob's first /api/jobs poll
  xhrControl.pending.finish(200, { ok: true, job_id: 'JOB-1', file_name: 'holiday.zip', status: 'started' });
  await upload;
  check('still exactly one transfer after the response', app.allTransfers.size === 1, 'size=' + app.allTransfers.size);
  check('the transfer is keyed by the server job id', app.allTransfers.has('JOB-1'), JSON.stringify([...app.allTransfers.keys()]));
  check('the card switches to the storing leg', app.allTransfers.get('JOB-1').stage === 'storing', app.allTransfers.get('JOB-1').stage);
  // and server telemetry keeps updating that same card
  app.applyTelemetry({ job_id: 'JOB-1', file_name: 'holiday.zip', type: 'UPLOAD', progress_percent: 60, status: 'ACTIVE', total_chunks: 6, completed_chunks: 3 });
  check('telemetry updates the same card', app.allTransfers.size === 1 && app.allTransfers.get('JOB-1').progress === 60, JSON.stringify([...app.allTransfers.keys()]));
  check('telemetry does not relabel it as Sending', /Encrypting/.test($('transfers-drawer-body').innerHTML), $('transfers-drawer-body').innerHTML.slice(0, 200));

  console.log('\n== a rejected upload reports the reason instead of failing silently ==');
  app.allTransfers.clear();
  $('toast-stack').children = [];
  const bad = app.uploadOne(new File(['x'], 'nope.bin', { size: 10 }), '');
  await new Promise(r => setTimeout(r, 5));
  xhrControl.pending.finish(507, { ok: false, error: 'no storage server connected' });
  await bad;
  const failedXfer = [...app.allTransfers.values()][0];
  check('the failed upload is marked FAILED', failedXfer && failedXfer.status === 'FAILED', JSON.stringify(failedXfer && failedXfer.status));
  check('the failure carries the server reason', failedXfer && /no storage server/.test(failedXfer.error), JSON.stringify(failedXfer && failedXfer.error));
  check('the user is toasted about the failure', $('toast-stack').children.length > 0, 'toasts=' + $('toast-stack').children.length);

  console.log('\n== text preview passes the session with the URL, like <img> does ==');
  // A media fetch cannot set a header, so the session travels in the URL
  // (withSession). The text preview fetched the bare media URL without it and
  // rendered an empty <pre> for every logged-in user — invisible in every test
  // that only checks the listing.
  const previewCalls = [];
  const realLocalStorageGet = localStorage.getItem.bind(localStorage);
  localStorage.getItem = k => (k === 'dfc_session' ? 'PREVIEWSESSION' : realLocalStorageGet(k));
  // Rebind inside the vm context too: app.js closes over the `fetch` the
  // context was created with, so swapping the host global changes nothing.
  const ctxFetch = async (url, opts) => {
    if (typeof url === 'string' && /\/api\/download\/file/.test(url)) previewCalls.push(url);
    return fakeFetch(url, opts);
  };
  ctx.fetch = ctxFetch;
  globalThis.fetch = ctxFetch;
  fetchQueue.length = 0;
  // openPreview reads the file off the current listing first (visibleFiles) and
  // only falls back to the details response, so the row has to exist for the
  // text branch to fire.
  S.files = [{ id: 'txt-1', name: 'note.txt', size: 5, mod_time: 1, is_dir: false, health: 'ok' }];
  S.view = 'drive'; S.parentID = ''; S.searchMode = false; S.filter = 'all';
  // openPreview needs the details call + shares call before it fetches the text.
  // Queue with the query delimiter so the URL-substring matcher cannot grab the
  // details answer for the shares call (both start with /api/shares- or
  // /api/files- prefixes that collide halfway).
  fetchQueue.push({ url: '/api/files/details?file_id=', ok: true, body: { id: 'txt-1', name: 'note.txt', size: 5, mod_time: 1, is_dir: false, chunk_count: 1, attachment_count: 1, replica_servers: 1, parts: [] } });
  fetchQueue.push({ url: '/api/shares/list?file_id=', ok: true, body: { shares: [] } });
  await app.openPreview('txt-1');
  await new Promise(r => setTimeout(r, 10));
  ctx.fetch = fakeFetch;
  globalThis.fetch = fakeFetch;
  localStorage.getItem = realLocalStorageGet;
  const mediaCall = previewCalls.find(u => u.includes('inline=1'));
  check('text body is requested with the session in the URL', !!mediaCall && /session=PREVIEWSESSION/.test(mediaCall), JSON.stringify(previewCalls));

  console.log('\n== a background refresh must not blank the listing (skeleton flicker) ==');
  // Navigating somewhere new should show skeletons; a refresh of the SAME listing
  // (telemetry, files_changed, an upload finishing) must keep the rows on screen.
  S.layout = 'list'; S.view = 'drive'; S.parentID = ''; $('search-input').value = '';
  fetchQueue.push({ url: '/api/files/view', ok: true, body: [{ id: 'r1', name: 'kept.png', is_dir: false, size: 4, mod_time: 4, health: 'ok' }] });
  await reload();
  check('rows present after the first load', S.files.length === 1, 'files=' + S.files.length);
  fetchQueue.push({ url: '/api/files/view', ok: true, body: [{ id: 'r1', name: 'kept.png', is_dir: false, size: 4, mod_time: 4, health: 'ok' }] });
  const refresh = reload();                       // same folder -> quiet refresh
  check('refresh keeps the existing rows visible', !$('file-tbody').innerHTML.includes('skeleton-row'), $('file-tbody').innerHTML.slice(0, 60));
  await refresh;
  S.parentID = 'folder-9';                        // now actually navigate
  fetchQueue.push({ ok: true, body: [] });
  const nav = reload();
  check('navigation does show skeletons', $('file-tbody').innerHTML.includes('skeleton-row'), $('file-tbody').innerHTML.slice(0, 60));
  await nav;
  S.parentID = '';

  console.log('\n----------------------------------------');
  console.log(`RESULT: ${passed} passed, ${failed} failed`);
  console.log('----------------------------------------');
  process.exit(failed ? 1 : 0);
}
run().catch(e => { console.error('HARNESS CRASH:', e); process.exit(2); });
