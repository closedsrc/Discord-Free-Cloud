/* Discord Free Cloud — drive UI. Vanilla JS, no build step.
   Backend contracts: /api/files/view, /api/files/batch, /api/files/{rename,move,favorite,trash,restore,details,raw_chunk},
   /api/upload/file, /api/folders/create, /api/files/create_text, /api/download/file?inline=1,
   /api/shares/{create,list,revoke}, /api/verify, /api/jobs, /api/jobs/cancel, /ws, /api/auth/* */
'use strict';

// ---------- session ----------
const SESSION_KEY = 'dfc_session';
const session = () => { try { return localStorage.getItem(SESSION_KEY) || ''; } catch (e) { return ''; } };
const setSession = t => { try { t ? localStorage.setItem(SESSION_KEY, t) : localStorage.removeItem(SESSION_KEY); } catch (e) {} };
const withSession = u => { const s = session(); return s ? u + (u.includes('?') ? '&' : '?') + 'session=' + encodeURIComponent(s) : u; };

async function api(url, opts = {}) {
    opts.headers = Object.assign({}, opts.headers);
    const s = session();
    if (s) opts.headers['X-DFC-Session'] = s;
    let res;
    try { res = await fetch(url, opts); }
    catch (e) { return { ok: false, status: 0, error: e.message || 'network error', data: {} }; }
    let data = {};
    const ct = res.headers.get('content-type') || '';
    if (ct.includes('json')) { try { data = await res.json(); } catch (e) { data = {}; } }
    else { data = { _text: await res.text().catch(() => '') }; }
    if (res.status === 401 && !url.startsWith('/api/auth/')) { setSession(''); showLock('Session expired — unlock again.'); }
    return { ok: res.ok && data.ok !== false, status: res.status, error: data.error || '', data };
}
const esc = v => String(v ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#039;'}[c]));
const fmtBytes = n => {
    if (!isFinite(n)) return '--';
    const u = ['B','KB','MB','GB','TB']; let i = 0; n = Number(n);
    while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
    return (i ? parseFloat(n.toFixed(2)) : Math.round(n)) + ' ' + u[i];
};
// Modified column. Date.toDateString() + toLocaleTimeString() printed a
// weekday and seconds ("Thu Sep 03 2026 08:23:06"), which is noise in a dense
// listing and did not match the short form used on grid cards. One compact
// format, minute resolution, same month/day/year order as fmtDateShort.
const fmtDate = ts => {
    try {
        const d = new Date(ts * 1000);
        if (isNaN(d)) return '--';
        const pad = n => String(n).padStart(2, '0');
        return `${d.toDateString().slice(4)}, ${pad(d.getHours())}:${pad(d.getMinutes())}`;
    } catch (e) { return '--'; }
};
const fmtDateShort = ts => { try { return new Date(ts * 1000).toDateString().slice(4); } catch (e) { return '--'; } };
const extOf = name => { const m = /\.([a-z0-9]+)$/i.exec(name || ''); return m ? m[1].toLowerCase() : ''; };

const IMAGE_EXT = new Set(['jpg','jpeg','png','gif','webp','bmp','ico','avif','svg','tiff']);
const VIDEO_EXT = new Set(['mp4','webm','mov','mkv','avi','m4v']);
const AUDIO_EXT = new Set(['mp3','wav','ogg','flac','aac','m4a','opus']);
const TEXT_EXT  = new Set(['txt','md','json','log','csv','yml','yaml','xml','ini','cfg','conf','env','toml','sql','sh','bash','bat','ps1','js','ts','jsx','tsx','mjs','css','scss','html','htm','go','py','rb','rs','java','kt','c','h','cpp','hpp','cs','php','lua','swift','r','pl','diff','patch','gitignore','dockerfile','makefile','readme','license','editorconfig']);
const ARCHIVE_EXT = new Set(['zip','rar','7z','tar','gz','bz2','xz','zst','tgz','iso','gpg','asc']);
const APP_EXT = new Set(['exe','msi','dmg','deb','rpm','appimage','jar','bin','run']);
const DOC_EXT = new Set(['doc','docx','odt','rtf','pages']);
const XLS_EXT = new Set(['xls','xlsx','ods','csv','tsv','numbers']);
const PPT_EXT = new Set(['ppt','pptx','odp','key']);
const CODE_FAM = new Set(['js','ts','jsx','tsx','mjs','css','scss','html','htm','go','py','rb','rs','java','kt','c','h','cpp','hpp','cs','php','lua','swift','r','pl','sql','json','xml','yml','yaml','sh','bash','ps1','dockerfile']);
const isImage = f => IMAGE_EXT.has(extOf(f.name));
const kindOf = f => {
    if (f.is_dir) return 'dir';
    const e = extOf(f.name);
    if (IMAGE_EXT.has(e)) return 'image';
    if (VIDEO_EXT.has(e)) return 'video';
    if (AUDIO_EXT.has(e)) return 'audio';
    if (e === 'pdf') return 'pdf';
    if (DOC_EXT.has(e)) return 'doc';
    if (XLS_EXT.has(e)) return 'sheetdoc';
    if (PPT_EXT.has(e)) return 'slides';
    if (e === 'apk') return 'apk';
    if (APP_EXT.has(e)) return 'app';
    if (ARCHIVE_EXT.has(e)) return 'archive';
    if (CODE_FAM.has(e)) return 'code';
    if (TEXT_EXT.has(e)) return 'text';
    return 'other';
};

// ---------- state ----------
const S = {
    view: 'drive',              // drive | recents | favorites | links | trash | transfers | bots | servers | settings
    parentID: '', parentName: '',
    trail: [],                  // breadcrumb path [{id,name}]
    files: [], trash: [],
    filter: 'all', sortKey: 'name', sortDir: 'asc',
    layout: localStorage.getItem('dfc_layout') || 'list',
    selected: new Set(),
    status: null, bots: [], servers: [],
    lastUploadFolder: '',
    loading: false,
    canWrite: true, publicMode: 'off',
};

// ---------- transfers ----------
const TRF_KEY = 'dfc_transfers';
const allTransfers = new Map(JSON.parse(localStorage.getItem(TRF_KEY) || '[]'));
let transfersDirty = false;
function saveTransfers() {
    const entries = [...allTransfers.entries()].slice(-80);
    try { localStorage.setItem(TRF_KEY, JSON.stringify(entries)); } catch (e) {}
}
const activeTransfers = () => [...allTransfers.values()].filter(t => t.status === 'ACTIVE' || t.status === 'STARTED');
const recentTransfers = () => [...allTransfers.entries()].map(([id, t]) => ({ id, ...t })).reverse();

function applyTelemetry(ev) {
    if (!ev || !ev.job_id) return;
    const prev = allTransfers.get(ev.job_id) || { name: '' };
    const next = {
        name: ev.file_name || prev.name || ev.job_id,
        type: ev.type || prev.type || 'UPLOAD',
        file_id: ev.file_id || prev.file_id || '',
        total_bytes: ev.total_bytes || prev.total_bytes || 0,
        processed_bytes: ev.processed_bytes ?? prev.processed_bytes ?? 0,
        chunks_done: ev.completed_chunks ?? prev.chunks_done ?? 0,
        chunks_total: ev.total_chunks ?? prev.chunks_total ?? 0,
        speed: ev.speed_mbs ?? prev.speed ?? 0,
        eta: ev.eta_seconds ?? prev.eta ?? 0,
        progress: typeof ev.progress_percent === 'number' ? ev.progress_percent : (prev.progress || 0),
        status: ev.status || prev.status || 'ACTIVE',
        error: ev.error || '',
        at: Date.now(),
        // telemetry means the server owns this transfer now: the browser's send
        // leg is finished, so the card stops saying "Sending"
        stage: prev.type === 'DOWNLOAD' || ev.type === 'DOWNLOAD' ? undefined : 'storing',
    };
    if (next.status === 'COMPLETED') next.progress = 100;
    allTransfers.set(ev.job_id, next);
    transfersDirty = true;
    renderTransfers();
    if (next.status === 'COMPLETED' && (next.type === 'UPLOAD' || next.type === 'DELETE')) {
        flashFile(next.file_id);
        if (S.view === 'drive' || S.view === 'recents') reloadCurrent();
    }
}
function reconcileJobs() {
    api('/api/jobs').then(r => {
        if (!Array.isArray(r.data)) return;
        for (const j of r.data) {
            if (j.status !== 'ACTIVE') continue;
            const existing = allTransfers.get(j.id);
            if (existing && existing.status === 'ACTIVE') continue;
            allTransfers.set(j.id, {
                name: j.file_path ? j.file_path.split(/[\/]/).pop() : j.file_id,
                type: j.type, file_id: j.file_id, total_bytes: j.total_bytes,
                processed_bytes: 0, chunks_done: j.completed_chunks, chunks_total: j.total_chunks,
                speed: 0, eta: 0, progress: j.total_chunks ? j.completed_chunks / j.total_chunks * 100 : 0,
                status: 'ACTIVE', error: '', at: Date.now(),
            });
            transfersDirty = true;
        }
        saveTransfers(); renderTransfers();
    });
}

// ---------- WebSocket ----------
let ws = null, wsRetry = null;
function connectWS() {
    if (ws && (ws.readyState === 0 || ws.readyState === 1)) return;
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    try { ws = new WebSocket(withSession(`${proto}//${location.host}/ws`)); }
    catch (e) { return; }
    ws.onopen = () => { updateStatusIndicator(); reconcileJobs(); };
    ws.onclose = () => { updateStatusIndicator(); clearTimeout(wsRetry); wsRetry = setTimeout(connectWS, 3000); };
    ws.onmessage = e => {
        let msg; try { msg = JSON.parse(e.data); } catch (err) { return; }
        if (msg.type === 'telemetry') { applyTelemetry(msg.data); if (transfersDirty) { saveTransfers(); transfersDirty = false; } }
        else if (msg.type === 'system_stats') { S.status = Object.assign(S.status || {}, msg.data); renderStorageMeter(); }
        else if (msg.type === 'files_changed') { reloadCurrent(); loadStatus(); }
        else if (msg.type === 'session_revoked') { setSession(''); showLock('Drive locked.'); }
        else if (msg.type === 'status_changed') { loadStatus(); }
        else if (msg.type === 'bots_changed') { loadBots(); loadServers(); }
    };
}

// ---------- dom helpers ----------
const $ = id => document.getElementById(id);
function toast(msg, kind = 'info') {
    const el = document.createElement('div');
    el.className = 'toast ' + kind; el.textContent = msg;
    $('toast-stack').appendChild(el);
    setTimeout(() => { el.style.opacity = '0'; el.style.transition = 'opacity .4s'; setTimeout(() => el.remove(), 450); }, kind === 'error' ? 6000 : 3400);
}
let logCount = 0;
function logLine(msg, cls = '') {
    const box = $('activity-log'); if (!box) return;
    const d = document.createElement('div'); d.className = 'log-line ' + cls;
    const t = document.createElement('span'); t.className = 'lt'; t.textContent = new Date().toLocaleTimeString();
    d.appendChild(t); d.appendChild(document.createTextNode(msg));
    box.prepend(d);
    if (++logCount > 300) { box.lastChild.remove(); logCount--; }
}

// ---------- auth ----------
async function checkAuth() {
    const r = await api('/api/auth/status');
    const d = r.data || {};
    applyPermissions(d);
    if (!d.auth_required || (d.has_session && d.is_unlocked)) { hideLock(); return true; }
    showLock();
    return false;
}
// The drive can be published read-only (PUBLIC_ACCESS=read): no lock screen, but
// no write routes either. Hide what would 403 instead of offering dead buttons.
function applyPermissions(d) {
    S.publicMode = d.public_mode || 'off';
    S.canWrite = d.can_write !== false;
    document.body.classList.toggle('read-only', !S.canWrite);
    const badge = $('read-only-badge');
    if (badge) badge.classList.toggle('hidden', S.canWrite);
    const signIn = $('btn-sign-in');
    if (signIn) signIn.classList.toggle('hidden', S.canWrite);
    // The storage plumbing is not part of what gets published: /api/bots needs
    // write scope, so a visitor would only get a 403 and an empty panel.
    document.querySelectorAll('[data-view="bots"],[data-view="servers"]')
        .forEach(b => b.classList.toggle('hidden', !S.canWrite));
    if (!S.canWrite && (S.view === 'bots' || S.view === 'servers')) switchView('drive');
}
function requireWrite() {
    if (S.canWrite) return true;
    toast('This drive is published read-only. Unlock with the master password to make changes.', 'error');
    return false;
}
function showLock(errMsg) {
    $('lock-overlay').classList.remove('hidden');
    $('lock-error').classList.toggle('hidden', !errMsg);
    if (errMsg) $('lock-error').textContent = errMsg;
    // when the drive is published there must be a way back out of the lock card
    $('lock-dismiss').classList.toggle('hidden', S.publicMode === 'off');
    setTimeout(() => $('lock-password').focus(), 60);
}
function hideLock() { $('lock-overlay').classList.add('hidden'); }
async function unlock() {
    const pwd = $('lock-password').value;
    if (!pwd) return;
    const btn = $('btn-unlock'); btn.disabled = true; btn.textContent = 'Deriving key…';
    const st = (await api('/api/auth/status')).data || {};
    let r;
    if (!st.has_password) {
        // brand-new drive: the first password derives the key
        r = await api('/api/auth/set_password', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ password: pwd }) });
    } else {
        r = await api('/api/auth/unlock', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ password: pwd }) });
    }
    btn.disabled = false; btn.textContent = 'Unlock drive';
    if (r.ok && r.data.session) {
        setSession(r.data.session); $('lock-password').value = ''; hideLock();
        // A successful unlock always mints a WRITE-scoped session (either the
        // master password loaded the key, or SIGNIN_PASSWORD matched a drive that
        // already holds it). Apply write permissions NOW so the published read-only
        // affordances (Read-only chip, Sign in button, hidden New/upload) clear
        // immediately — the old code only refreshed them on a RE-unlock, leaving the
        // first unlock stuck showing read-only chrome. checkAuth then reconciles
        // against the authoritative status.
        applyPermissions({ can_write: true, public_mode: S.publicMode, has_session: true, is_unlocked: true });
        if (!booted) boot(); else { connectWS(); loadStatus(); await checkAuth(); switchView('drive'); }
    }
    else { $('lock-error').textContent = r.error || 'Wrong password.'; $('lock-error').classList.remove('hidden'); }
}

// ---------- views / navigation ----------
const VIEW_TITLES = { drive: 'Cloud Drive', recents: 'Recents', favorites: 'Favorites', links: 'Shared Links', trash: 'Trash', transfers: 'Transfers', bots: 'My Bots', servers: 'My Servers', settings: 'Settings' };
const FILE_VIEWS = new Set(['drive','recents','favorites']);
// recents/favorites share the drive panel — they are different catalog queries
// rendered into the same explorer area.
const PANEL_OF = { drive: 'view-drive', recents: 'view-drive', favorites: 'view-drive', links: 'view-links', trash: 'view-trash', transfers: 'view-transfers', bots: 'view-bots', servers: 'view-servers', settings: 'view-settings' };
function switchView(v) {
    S.view = v; S.selected.clear();
    document.querySelectorAll('.nav-button').forEach(b => b.classList.toggle('active', b.dataset.view === v));
    document.querySelectorAll('.view-panel').forEach(p => p.classList.add('hidden'));
    const panel = $(PANEL_OF[v]);
    if (panel) panel.classList.remove('hidden');
    $('page-title').textContent = VIEW_TITLES[v] || 'Files';
    if (v === 'drive') { if (S.parentID) { /* keep folder */ } else { S.parentID = ''; S.parentName = ''; } }
    $('search-input').value = ''; $('btn-clear-search').classList.add('hidden');
    applyToolbarFor(v);
    renderBreadcrumb();
    updateBatchBar();
    if (FILE_VIEWS.has(v)) reloadCurrent();
    else if (v === 'trash') loadTrash();
    else if (v === 'links') loadLinks();
    else if (v === 'transfers') renderTransfersPage();
    else if (v === 'bots') loadBots();
    else if (v === 'servers') loadServers();
    document.body.classList.remove('nav-open');
    $('new-menu').classList.add('hidden');
}
// The drive toolbar (search scope, New, layout toggle) only makes sense over a
// file listing; the infrastructure views get a clean header instead.
function applyToolbarFor(v) {
    const fileish = FILE_VIEWS.has(v) || v === 'trash';
    $('search-input').placeholder = v === 'drive' && S.parentID ? 'Search in this folder…'
        : fileish ? 'Search your drive…' : 'Search your drive…';
    document.querySelector('.searchbox').classList.toggle('hidden', !fileish);
    $('btn-new-menu').classList.toggle('hidden', !fileish);
    document.querySelector('.vtoggle').classList.toggle('hidden', !FILE_VIEWS.has(v));
}
function gotoDriveFrom(otherView) {
    if (S.view === otherView && S.parentID) return; // already inside a drive folder
    S.parentID = ''; S.parentName = ''; S.trail = [];
    switchView('drive');
}
function navigateInto(folder) {
    // coming from recents/favorites/links etc — return to the drive at the
    // folder that was clicked (drive breadcrumb restarts at its parent)
    if (S.view !== 'drive') {
        S.trail = []; S.parentID = folder.id; S.parentName = folder.name;
        switchView('drive');
        return;
    }
    if (folder.id === S.parentID) return;
    if (S.parentID) S.trail.push({ id: S.parentID, name: S.parentName });
    S.parentID = folder.id; S.parentName = folder.name;
    reloadCurrent(); renderBreadcrumb();
}
function openFolderDirect(folder) {
    if (S.view !== 'drive') { S.trail = []; S.parentID = folder.id; S.parentName = folder.name; switchView('drive'); }
    else { navigateInto(folder); }
}
function navigateUp() { const p = S.trail.pop(); S.parentID = p ? p.id : ''; S.parentName = p ? p.name : ''; reloadCurrent(); renderBreadcrumb(); }
function renderBreadcrumb() {
    const bar = $('breadcrumb-bar'); bar.innerHTML = '';
    if (S.view === 'drive') $('page-title').textContent = S.parentID ? S.parentName : 'Cloud Drive';
    if (S.view === 'drive') $('search-input').placeholder = S.parentID ? 'Search in this folder…' : 'Search your drive…';
    if (S.view !== 'drive' || !S.parentID) return;
    const crumb = (label, fn, isLast) => {
        const b = document.createElement(isLast ? 'span' : 'button');
        b.className = 'crumb' + (isLast ? ' cur' : ''); if (!isLast) b.type = 'button';
        b.textContent = label; if (fn) b.onclick = fn; bar.appendChild(b);
    };
    const mkSep = () => { const sp = document.createElement('span'); sp.className = 'sep'; sp.textContent = '/'; bar.appendChild(sp); };
    crumb('Cloud Drive', () => { S.parentID = ''; S.parentName = ''; S.trail = []; reloadCurrent(); renderBreadcrumb(); });
    S.trail.forEach((t, i) => {
        mkSep();
        crumb(t.name, () => { S.trail.length = i; S.parentID = t.id; S.parentName = t.name; reloadCurrent(); renderBreadcrumb(); });
    });
    if (S.parentID) {
        mkSep();
        crumb(S.parentName || '…', null, true);
    }
}

let loadSeq = 0;
let loadedKey = null;   // which listing the rows on screen belong to
async function reloadCurrent() {
    // Navigations must not be dropped while an earlier list request is still in
    // flight — on a slow host that left the old contents under the new title.
    // Latest request wins instead.
    const seq = ++loadSeq;
    const params = new URLSearchParams();
    if (S.view === 'recents') params.set('view', 'recents');
    else if (S.view === 'favorites') params.set('view', 'favorites');
    else if (S.parentID) params.set('parent_id', S.parentID);
    const q = $('search-input').value.trim();
    if (q) { params.set('search', q); params.delete('parent_id'); params.delete('view'); }
    S.searchMode = !!q;
    // A navigation and a background refresh want opposite things. Going somewhere
    // new should show skeletons, because the old folder's rows under a new title
    // are a lie. A refresh (telemetry, files_changed, an upload finishing) must
    // NOT blank the listing the user is reading — that flicker is worse than a
    // slightly stale row. The request key tells the two apart.
    const key = params.toString();
    if (key !== loadedKey) S.files = [];
    S.loading = true;
    render();
    const r = await api('/api/files/view' + (key ? '?' + key : ''));
    if (seq !== loadSeq) return;
    S.files = Array.isArray(r.data) ? r.data : [];
    loadedKey = key;
    S.loading = false;
    render();
}
function sortFiles(list) {
    const dir = S.sortDir === 'desc' ? -1 : 1;
    return [...list].sort((a, b) => {
        if (S.view === 'drive' && !S.searchMode && a.is_dir !== b.is_dir) return a.is_dir ? -1 : 1;
        let r = 0;
        if (S.sortKey === 'size') r = (a.size || 0) - (b.size || 0);
        else if (S.sortKey === 'date') r = (a.mod_time || 0) - (b.mod_time || 0);
        else r = a.name.localeCompare(b.name, undefined, { numeric: true });
        return r * dir;
    });
}
function passFilter(f) {
    switch (S.filter) {
        case 'folders': return f.is_dir;
        case 'media': return ['image','video','audio'].includes(kindOf(f));
        case 'docs': return ['pdf','text','doc','sheetdoc','slides'].includes(kindOf(f));
        case 'archives': return ['archive','app','apk'].includes(kindOf(f));
        case 'code': return kindOf(f) === 'code';
        default: return true;
    }
}
function visibleFiles() { return sortFiles(S.files.filter(passFilter)); }
function currentFolderId() { return S.parentID || (S.files.find(f => f.is_dir) || {}).parent_id || ''; }

// ---------- icons ----------
// A grey document sheet carries one saturated glyph or badge, the way Filen and
// Drive do it: the colour names the type, the sheet keeps a dense listing calm.
// #bababa is the sheet body measured off the reference product.
//
// Every glyph must survive being drawn at 24px in a list row, not just at 64px
// in a grid tile. Hairline strokes disappear at row size and leave an anonymous
// grey page, so each type carries a block of saturated colour (a badge, a filled
// shape, or a heavy stroke) rather than fine linework.
const SHEET = '<path d="M6 2.2h7.1l5.5 5.5v11.7a2.4 2.4 0 0 1-2.4 2.4H6a2.4 2.4 0 0 1-2.4-2.4V4.6A2.4 2.4 0 0 1 6 2.2z" fill="#bababa"/><path d="M13.1 2.2l5.5 5.5h-5.5z" fill="#8e8e8e"/>';
const sheetIcon = (glyph, base) => `<svg viewBox="0 0 24 24" fill="none">${base || SHEET}${glyph}</svg>`;
const label = (text, fill) =>
    `<rect x="4.4" y="12.1" width="13.2" height="6.6" rx="1.5" fill="${fill}"/>` +
    `<text x="11" y="16.95" text-anchor="middle" font-family="Inter,Arial,sans-serif" font-size="4.5" font-weight="700" fill="#fff">${text}</text>`;

const ICON = {
    folder: '<svg viewBox="0 0 24 24" fill="none"><path d="M3 6.4A2.4 2.4 0 0 1 5.4 4h3.8l2.1 2.5h7.3A2.4 2.4 0 0 1 21 8.9v9.1a2.4 2.4 0 0 1-2.4 2.4H5.4A2.4 2.4 0 0 1 3 18z" fill="#3b82f6"/><path d="M3 6.4A2.4 2.4 0 0 1 5.4 4h3.8l2.1 2.5H3z" fill="#60a5fa"/></svg>',
    image: sheetIcon('<path d="M4.6 18.7l4-4.3 2.6 2.8 2.8-3.3 3.4 4.8z" fill="#38bdf8"/><circle cx="7.9" cy="11.4" r="1.7" fill="#0ea5e9"/>'),
    video: sheetIcon('<rect x="4.6" y="12.2" width="12.8" height="6.4" rx="1.5" fill="#a855f7"/><path d="M9.8 14.1l3.6 1.9-3.6 1.9z" fill="#fff"/>'),
    audio: sheetIcon('<path d="M8.6 18.6v-6.3l6.4-1.3v6.3" stroke="#22c55e" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"/><circle cx="7.6" cy="18.4" r="2.1" fill="#22c55e"/><circle cx="15" cy="17.1" r="2.1" fill="#22c55e"/>'),
    pdf: sheetIcon(label('PDF', '#e5484d')),
    doc: sheetIcon(label('DOC', '#2f7ff5')),
    sheetdoc: sheetIcon(label('XLS', '#15a35b')),
    slides: sheetIcon(label('PPT', '#e2711d')),
    // a solid amber zipper reads as "archive" at 24px; the old dashed hairline
    // and dot vanished into the grey sheet at row size
    archive: sheetIcon('<rect x="8.9" y="2.4" width="4.2" height="12.4" rx="1" fill="#f5a524"/><path d="M8.9 5.6h4.2M8.9 8.1h4.2M8.9 10.6h4.2M8.9 13.1h4.2" stroke="#bababa" stroke-width="1.1"/><rect x="7.9" y="14.4" width="6.2" height="5.4" rx="1.6" fill="#f5a524"/><rect x="10.2" y="16" width="1.6" height="2.4" rx=".8" fill="#8a5b12"/>'),
    app: sheetIcon('<circle cx="11" cy="15.6" r="3.1" fill="#64748b"/><circle cx="11" cy="15.6" r="1.2" fill="#bababa"/><path d="M11 10.6v1.9M11 18.7v1.9M6.6 13.1l1.7 1M13.7 17.1l1.7 1M6.6 18.1l1.7-1M13.7 14.1l1.7-1" stroke="#64748b" stroke-width="2.1" stroke-linecap="round"/>'),
    apk: sheetIcon('<path d="M6.2 18.2a4.8 4.8 0 0 1 9.6 0z" fill="#3ddc84"/><path d="M7.5 12.1l1.2 2M14.5 12.1l-1.2 2" stroke="#3ddc84" stroke-width="2.1" stroke-linecap="round"/><rect x="5.6" y="19" width="10.8" height="2.1" rx="1.05" fill="#3ddc84"/>'),
    code: sheetIcon('<path d="M9.1 12.6l-2.6 2.9 2.6 2.9M12.9 12.6l2.6 2.9-2.6 2.9" stroke="#4f6ef7" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"/>'),
    text: sheetIcon('<path d="M6.6 12.6h8.8M6.6 15.5h8.8M6.6 18.4h5.6" stroke="#71717a" stroke-width="2.2" stroke-linecap="round"/>'),
    other: sheetIcon('<rect x="5.6" y="13.4" width="10.8" height="2.2" rx="1.1" fill="#8e8e8e"/><rect x="5.6" y="17" width="7.2" height="2.2" rx="1.1" fill="#8e8e8e"/>'),
};
ICON.dir = ICON.folder;
const iconFor = f => ICON[kindOf(f)] || ICON.other;

// Small monochrome glyphs for menus and buttons — never mixed with file icons.
const ACT = {
    preview: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M2.6 12S6 5.8 12 5.8 21.4 12 21.4 12 18 18.2 12 18.2 2.6 12 2.6 12z"/><circle cx="12" cy="12" r="2.9"/></svg>',
    download: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M21 15v3.5a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V15"/><path d="M7.5 10.5 12 15l4.5-4.5M12 15V3.5"/></svg>',
    link: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round"><path d="M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71"/><path d="M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71"/></svg>',
    rename: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M12 20h9"/><path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4z"/></svg>',
    move: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z"/><path d="M9.5 13.5h6M13 11l2.5 2.5L13 16"/></svg>',
    heart: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round"><path d="M20.8 4.6a5.5 5.5 0 0 0-7.78 0L12 5.67l-1.06-1.06a5.5 5.5 0 0 0-7.78 7.78l1.06 1.06L12 21.2l7.78-7.78 1.06-1.06a5.5 5.5 0 0 0 0-7.78z"/></svg>',
    info: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round"><circle cx="12" cy="12" r="9"/><path d="M12 16.5V11M12 8h.01"/></svg>',
    trash: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M3.5 6.5h17M9 6.5V4.8A1.3 1.3 0 0 1 10.3 3.5h3.4A1.3 1.3 0 0 1 15 4.8v1.7M6 6.5l.8 12.2A1.8 1.8 0 0 0 8.6 20.5h6.8a1.8 1.8 0 0 0 1.8-1.8l.8-12.2"/></svg>',
    restore: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M3.5 12a8.5 8.5 0 1 0 2.6-6.1"/><path d="M3.2 4.5v4.2h4.2"/></svg>',
    x: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round"><path d="M6 6l12 12M18 6L6 18"/></svg>',
    upload: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M21 15v3.5a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V15"/><path d="M7.5 8 12 3.5 16.5 8M12 3.5V15"/></svg>',
    open: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z"/></svg>',
};

// ---------- rendering ----------
function renderSkeletons() {
    const rows = Array.from({ length: 7 }, () => `<tr class="skeleton-row"><td class="tc"></td><td><div class="skel-row"><span class="skel skel-icon"></span><span class="skel skel-name"></span></div></td><td></td><td><span class="skel skel-size"></span></td><td><span class="skel skel-date"></span></td><td></td></tr>`).join('');
    $('file-tbody').innerHTML = rows;
    $('files-grid').innerHTML = Array.from({ length: 8 }, () => '<div class="skel skel-tile"></div>').join('');
}
function render() {
    const files = visibleFiles();
    $('count-label').textContent = S.loading ? 'Loading…' : files.length + (files.length === 1 ? ' item' : ' items');
    // Skeletons only stand in for content we do not have. A refresh of a listing
    // already on screen keeps its rows (reloadCurrent clears S.files on a real
    // navigation), otherwise every telemetry tick blanks the folder mid-read.
    const isLoading = S.loading && files.length === 0 && (S.view === 'drive' || S.view === 'recents' || S.view === 'favorites');
    $('empty-state').classList.toggle('hidden', isLoading || files.length !== 0);
    if (!isLoading && files.length === 0) {
        const searching = $('search-input').value.trim();
        $('empty-title').textContent = searching ? 'Nothing matches your search' : S.view === 'recents' ? 'No recent files' : S.view === 'favorites' ? 'No favorites yet' : 'This folder is empty';
        $('empty-sub').textContent = searching ? 'Try a different term.' : 'Drop files anywhere, or press New → Upload files.';
    }
    if (S.layout === 'grid') {
        $('files-grid').classList.remove('hidden'); $('files-table').classList.add('hidden');
        if (isLoading) { $('files-grid').innerHTML = Array.from({ length: 8 }, () => '<div class="skel skel-tile"></div>').join(''); }
        else renderGrid(files);
    } else {
        $('files-grid').classList.add('hidden'); $('files-table').classList.remove('hidden');
        if (isLoading) { renderSkeletons(); }
        else renderTable(files);
    }
    updateBatchBar();
}
const thumbObserver = new IntersectionObserver(entries => {
    for (const en of entries) {
        if (!en.isIntersecting) continue;
        const img = en.target.querySelector('img');
        if (img && img.dataset.src) { revealOnLoad(img); img.src = img.dataset.src; delete img.dataset.src; }
        thumbObserver.unobserve(en.target);
    }
}, { rootMargin: '200px' });
// list rows put the <img> at the observed node itself
const rowThumbObserver = new IntersectionObserver(entries => {
    for (const en of entries) {
        if (!en.isIntersecting) continue;
        const img = en.target;
        if (img.dataset.src) { revealOnLoad(img); img.src = img.dataset.src; delete img.dataset.src; }
        rowThumbObserver.unobserve(img);
    }
}, { rootMargin: '200px' });
// fade a thumbnail in only once it decodes; a failed shard leaves the type icon
function revealOnLoad(img) {
    img.addEventListener('load', () => img.classList.add('on'), { once: true });
    img.addEventListener('error', () => img.remove(), { once: true });
}
// Every thumbnail is the whole file decrypted through Discord — there is no
// server-side thumbnailer. Cap it at one chunk so a listing cannot pull
// hundreds of megabytes to paint 28px cells; larger images keep the type icon.
const THUMB_MAX_BYTES = 8 * 1024 * 1024;
const thumbable = f => isImage(f) && (f.size || 0) <= THUMB_MAX_BYTES;
function thumbHTML(f) {
    // The type icon always renders underneath; the decrypted thumbnail fades in
    // over it once Discord returns the bytes, so a cell is never a blank box.
    const icon = `<span class="fc-type-icon">${f.is_dir ? ICON.folder : iconFor(f)}</span>`;
    if (thumbable(f)) return `<div class="fc-thumb">${icon}<img loading="lazy" data-src="${withSession('/api/download/file?file_id=' + encodeURIComponent(f.id) + '&inline=1')}" alt=""></div>`;
    return `<div class="fc-thumb">${icon}</div>`;
}
function statusOf(f) {
    if (f.is_dir) return 'folder';
    if (f.health === 'empty') return 'error';
    if (f.health === 'partial') return 'processing';
    return 'ok';
}
function renderGrid(files) {
    const grid = $('files-grid'); grid.innerHTML = '';
    for (const f of files) {
        const card = document.createElement('article');
        const bleed = thumbable(f);
        card.className = 'file-card' + (bleed ? ' bleed' : '') + (S.selected.has(f.id) ? ' selected' : '');
        card.dataset.id = f.id;
        const st = statusOf(f);
        card.innerHTML = `
            ${thumbHTML(f)}
            <span class="fc-check${S.selected.has(f.id) ? ' on' : ''}">✓</span>
            <button class="fc-menu" aria-label="More">⋮</button>
            ${st !== 'ok' && st !== 'folder' ? `<span class="fc-badge badge-${st === 'processing' ? 'processing' : 'error'}">${st === 'processing' ? 'PARTIAL' : 'INCOMPLETE'}</span>` : ''}
            <div class="fc-pill">
                <span class="fc-name" title="${esc(f.name)}">${esc(f.name)}${f.favorite ? ' <span class="fav-star">♥</span>' : ''}</span>
                <span class="fc-meta">${f.is_dir ? 'Folder' : fmtBytes(f.size) + ' · ' + fmtDateShort(f.mod_time)}</span>
            </div>`;
        grid.appendChild(card);
        const thumb = card.querySelector('.fc-thumb');
        if (thumb) thumbObserver.observe(thumb);
        card.addEventListener('click', e => {
            if (e.target.closest('.fc-menu')) { e.stopPropagation(); openFileMenu(f, e.target.closest('.fc-menu').getBoundingClientRect()); return; }
            if (e.target.closest('.fc-check') || e.metaKey || e.ctrlKey) { e.preventDefault(); toggleSelect(f.id); return; }
            if (f.is_dir) return openFolderDirect(f);
            openPreview(f.id);
        });
        card.addEventListener('contextmenu', e => { e.preventDefault(); openFileMenu(f, { left: e.clientX, top: e.clientY }); });
    }
}
function renderTable(files) {
    const tbody = $('file-tbody'); tbody.innerHTML = '';
    for (const f of files) {
        const tr = document.createElement('tr');
        tr.className = 'file-row' + (S.selected.has(f.id) ? ' selected' : '');
        const st = statusOf(f);
        const icon = f.is_dir ? ICON.folder : iconFor(f);
        const thumb = thumbable(f) ? icon + '<img loading="lazy" data-src="' + withSession('/api/download/file?file_id=' + encodeURIComponent(f.id) + '&inline=1') + '" alt="">' : icon;
        tr.innerHTML = `
            <td class="tc"><input type="checkbox" class="checkbox row-check" ${S.selected.has(f.id) ? 'checked' : ''}></td>
            <td><div class="fr-name"><span class="fr-icon k-${kindOf(f)}">${thumb}</span>
                <span class="fr-label">${esc(f.name)}${f.favorite ? ' <span class="fav-star">♥</span>' : ''}</span></div></td>
            <td class="fr-status">${st === 'processing' ? '<span class="st processing">Partial</span>' : st === 'error' ? '<span class="st error">Incomplete</span>' : ''}</td>
            <td class="fr-size">${f.is_dir ? '' : fmtBytes(f.size)}</td>
            <td class="fr-date">${fmtDate(f.mod_time)}</td>
            <td><button class="fr-menu" aria-label="More">⋮</button></td>`;
        tbody.appendChild(tr);
        tr.querySelector('.row-check').addEventListener('click', e => { e.stopPropagation(); toggleSelect(f.id); });
        tr.addEventListener('click', e => { if (e.target.closest('button') || e.target.closest('input')) return; if (f.is_dir) openFolderDirect(f); else openPreview(f.id); });
        tr.addEventListener('contextmenu', e => { e.preventDefault(); openFileMenu(f, { left: e.clientX, top: e.clientY }); });
        tr.querySelector('.fr-menu').addEventListener('click', e => { e.stopPropagation(); openFileMenu(f, e.target.getBoundingClientRect()); });
    }
    const imgs = tbody.querySelectorAll('img[data-src]');
    imgs.forEach(img => rowThumbObserver.observe(img));
}
function toggleSelect(id) {
    S.selected.has(id) ? S.selected.delete(id) : S.selected.add(id);
    render();
}
function updateBatchBar() {
    const bar = $('batch-bar'); const n = S.selected.size;
    bar.classList.toggle('hidden', n === 0);
    $('batch-count').textContent = n + ' selected';
    const anyTrash = S.view === 'trash';
    $('bb-trash').classList.toggle('hidden', anyTrash);
    $('bb-favorite').classList.toggle('hidden', anyTrash);
}
function flashFile(id) {
    const el = document.querySelector(`[data-id="${CSS.escape(id)}"]`);
    if (el) { el.classList.add('row-pulse'); setTimeout(() => el.classList.remove('row-pulse'), 1600); }
}

// ---------- status ----------
async function loadStatus() { const r = await api('/api/status'); if (r.ok) { S.status = r.data; renderStatus(); } }
function renderStatus() {
    const d = S.status || {};
    const ready = d.cloud_ready && d.is_unlocked;
    updateStatusIndicator(ready);
    renderStorageMeter(d);
    const unconf = !d.is_configured && !(d.bot_nodes_count > 0) && !(d.channels_count > 0);
    $('unconfigured-banner').classList.toggle('hidden', !unconf);
}
function updateStatusIndicator(ready) {
    const dot = $('system-status-dot'), txt = $('system-status-text');
    if (!ws || ws.readyState !== 1) {
        dot.className = 'status-dot offline'; txt.textContent = 'Offline';
        if (txt.parentElement) txt.parentElement.classList.add('degraded');
        return;
    }
    if (ready === undefined) ready = S.status && S.status.cloud_ready;
    dot.className = 'status-dot ' + (ready ? 'online' : 'warning');
    txt.textContent = ready ? 'Cloud ready' : (S.status && S.status.is_unlocked === false ? 'Locked' : 'Setup needed');
    if (txt.parentElement) txt.parentElement.classList.toggle('degraded', !ready);
}
function renderStorageMeter(d) {
    d = d || S.status || {};
    $('cloud-usage-text').textContent = fmtBytes(d.total_storage_bytes || 0) + ' · ' + (d.total_files || 0) + ' files';
    const cap = d.disk_free_bytes || 1;
    const pct = Math.min(100, (d.total_storage_bytes || 0) / (cap + (d.total_storage_bytes || 0)) * 100 || 0);
    const fill = $('cloud-usage-fill'); if (fill) fill.style.width = pct + '%';
}

// ---------- file actions ----------
function dl(f) { location.href = withSession('/api/download?file_id=' + encodeURIComponent(f.id)); }
async function ctxDownload(f) { dl(f); }
async function copyLink(f) {
    const r = await api('/api/shares/create', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ file_id: f.id }) });
    if (!r.ok) return toast('Could not create link: ' + r.error, 'error');
    const url = r.data.url;
    try { await navigator.clipboard.writeText(url); toast('Share link copied (7 days)', 'success'); }
    catch (e) { showInput('Share link', url, false).then(() => {}); }
}
function openFileMenu(f, rect) {
    closeMenu();
    const menu = document.createElement('div'); menu.className = 'ctx-menu';
    const inTrash = S.view === 'trash';
    const items = [];
    if (f.is_dir) items.push(['Open', ACT.open, () => openFolderDirect(f)]);
    if (!f.is_dir) {
        items.push(['Preview', ACT.preview, () => openPreview(f.id)]);
        items.push(['Download', ACT.download, () => dl(f)]);
        if (!inTrash && S.canWrite) items.push(['Copy share link', ACT.link, () => copyLink(f)]);
        if (!f.is_dir) items.push(['File details', ACT.info, () => openPreview(f.id, true)]);
    }
    if (!inTrash && S.canWrite) {
        items.push(['Rename', ACT.rename, () => renameItem(f)]);
        items.push(['Move to…', ACT.move, () => moveItem(f)]);
        items.push([f.favorite ? 'Remove from favorites' : 'Add to favorites', ACT.heart, () => favoriteItem(f, !f.favorite)]);
    }
    if (S.canWrite) {
        items.push(['sep']);
        if (inTrash) items.push(['Restore', ACT.restore, () => trashRestore([f.id]), true]);
        else items.push(['Move to trash', ACT.trash, () => trashMove([f.id]), true]);
        items.push(['Delete forever', ACT.x, () => deleteForever([f.id]), true]);
    }
    for (const it of items) {
        if (it[0] === 'sep') { const s = document.createElement('div'); s.className = 'ctx-sep'; menu.appendChild(s); continue; }
        const b = document.createElement('button'); b.className = 'ctx-item' + (it[3] ? ' danger' : '');
        b.innerHTML = `<span class="ctx-icon">${it[1]}</span>${esc(it[0])}`;
        b.onclick = () => { closeMenu(); it[2](); };
        menu.appendChild(b);
    }
    document.body.appendChild(menu);
    const mw = 220, mh = menu.offsetHeight;
    menu.style.left = Math.min(rect.left, innerWidth - mw - 8) + 'px';
    menu.style.top = Math.min(rect.top, innerHeight - mh - 8) + 'px';
    activeMenu = menu;
}
let activeMenu = null;
const closeMenu = () => { if (activeMenu) { activeMenu.remove(); activeMenu = null; } };
document.addEventListener('click', e => { if (activeMenu && !e.target.closest('.ctx-menu')) closeMenu(); });
document.addEventListener('keydown', e => { if (e.key === 'Escape') { closeMenu(); $('new-menu').classList.add('hidden'); } });

async function doBatch(ids, action, value) {
    const r = await api('/api/files/batch', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ ids, action, value }) });
    if (!r.ok) return toast(r.error || 'Failed', 'error');
    S.selected.clear(); reloadCurrent(); renderBreadcrumb();
    return r;
}
async function renameItem(f) {
    const name = await showInput('Rename', f.name);
    if (!name || name === f.name) return;
    const r = await api('/api/files/rename', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ id: f.id, name }) });
    if (r.ok) toast('Renamed', 'success'); else toast(r.error, 'error');
    reloadCurrent();
}
async function moveItem(f) {
    const folder = await pickFolder('Move "' + f.name + '" to…');
    if (folder === undefined) return;
    const r = await api('/api/files/move', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ id: f.id, parent_id: folder.id }) });
    if (r.ok) toast('Moved to ' + (folder.name || 'root'), 'success'); else toast(r.error, 'error');
    reloadCurrent();
}
async function favoriteItem(f, on) {
    const r = await api('/api/files/favorite', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ id: f.id, value: on }) });
    if (r.ok) { f.favorite = on; render(); }
}
async function trashMove(ids) {
    if (!await confirmBox('Move to trash?', 'You can restore later from Trash.')) return;
    await doBatch(ids, 'trash');
}
async function trashRestore(ids) { await doBatch(ids, 'restore'); }
async function deleteForever(ids) {
    const n = ids.length;
    if (!await confirmBox('Delete ' + n + ' item' + (n === 1 ? '' : 's') + ' forever?', 'Encrypted parts will be removed from Discord. This cannot be undone.', true)) return;
    const r = await api('/api/delete', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ file_ids: ids }) });
    if (r.ok) { toast('Deleting… parts removed from Discord', 'success'); S.selected.clear(); reloadCurrent(); loadStatus(); }
    else toast(r.error, 'error');
}
async function loadTrash() {
    const r = await api('/api/files/view?view=trash');
    S.trash = Array.isArray(r.data) ? r.data : [];
    $('trash-count').textContent = S.trash.length + ' items in trash';
    const grid = $('trash-grid'); grid.innerHTML = '';
    if (!S.trash.length) { grid.innerHTML = '<div class="xfer-empty" style="grid-column:1/-1">Trash is empty.</div>'; return; }
    for (const f of S.trash) {
        const card = document.createElement('article');
        card.className = 'file-card' + (thumbable(f) ? ' bleed' : '');
        card.innerHTML = `${thumbHTML(f)}
            <button class="fc-menu" aria-label="More">⋮</button>
            <div class="fc-pill"><span class="fc-name" title="${esc(f.name)}">${esc(f.name)}</span>
            <span class="fc-meta">${f.is_dir ? 'Folder' : fmtBytes(f.size)}</span></div>`;
        const t = card.querySelector('.fc-thumb'); if (t) thumbObserver.observe(t);
        const restore = () => trashRestore([f.id]).then(loadTrash);
        card.addEventListener('click', () => {
            const menu = document.createElement('div'); menu.className = 'ctx-menu';
            const mk = (label, fn, danger) => { const b = document.createElement('button'); b.className = 'ctx-item' + (danger ? ' danger' : ''); b.textContent = label; b.onclick = () => { menu.remove(); fn(); }; menu.appendChild(b); };
            if (S.canWrite) {
                mk('Restore', restore);
                mk('Delete forever', () => deleteForever([f.id]).then(loadTrash), true);
            }
            document.body.appendChild(menu);
            const r = card.getBoundingClientRect();
            menu.style.left = r.left + 'px'; menu.style.top = (r.bottom + 4) + 'px';
        });
        grid.appendChild(card);
    }
}
document.addEventListener('click', async e => {
    if (e.target.id === 'btn-empty-trash') {
        if (!S.trash.length) return toast('Trash already empty', 'info');
        if (await confirmBox('Empty trash?', 'Deletes ' + S.trash.length + ' item(s) and their Discord parts forever.', true)) {
            await api('/api/delete', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ file_ids: S.trash.map(f => f.id) }) });
            toast('Trash emptied', 'success'); loadTrash(); loadStatus();
        }
    }
});

// ---------- shared links view ----------
async function allFilesRecursive() {
    const out = [];
    const walk = async pid => {
        const r = await api('/api/files' + (pid ? '?parent_id=' + encodeURIComponent(pid) : ''));
        for (const f of (r.data || [])) { out.push(f); if (f.is_dir) await walk(f.id); }
    };
    await walk('');
    return out;
}
async function loadLinks() {
    const panel = $('links-panel'); panel.innerHTML = '<div class="xfer-empty">Loading links…</div>';
    const files = await allFilesRecursive();
    const rows = [];
    for (const f of files.filter(x => !x.is_dir)) {
        const r = await api('/api/shares/list?file_id=' + encodeURIComponent(f.id));
        if (!r.ok || !r.data.shares) continue;
        for (const sh of r.data.shares) rows.push({ file: f, share: sh });
    }
    if (!rows.length) { panel.innerHTML = '<div class="xfer-empty">No share links yet.<br>Right-click a file → Copy share link.</div>'; return; }
    panel.innerHTML = '';
    for (const { file: f, share: sh } of rows) {
        const div = document.createElement('div'); div.className = 'link-row';
        const exp = sh.expires_at ? new Date(sh.expires_at * 1000).toLocaleString() : 'never';
        div.innerHTML = `<span class="fr-icon">${f.is_dir ? ICON.folder : iconFor(f)}</span><span class="link-name">${esc(f.name)}</span>
            <span class="link-exp">${sh.downloads || 0} dl · ${esc(exp)}${sh.expired ? ' (expired)' : ''}</span>
            <button class="link-copy">Open file</button>
            ${S.canWrite ? `<button class="link-revoke" data-i="${esc(sh.id)}">Revoke</button>` : ''}`;
        div.querySelector('.link-copy').onclick = () => openPreview(f.id);
        const rev = div.querySelector('.link-revoke');
        if (rev) rev.onclick = async e => {
            await api('/api/shares/revoke', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ id: e.target.dataset.i }) });
            toast('Link revoked', 'success'); loadLinks();
        };
        panel.appendChild(div);
    }
}

// ---------- preview ----------
let previewIndex = -1, previewFiles = [], previewFor = '', previewZoom = 1, previewCurrent = null;
let previewPan = { x: 0, y: 0 };
function applyPreviewTransform() {
    const img = $('pm-image');
    if (!img) return;
    img.style.transform = `translate(${previewPan.x}px, ${previewPan.y}px) scale(${previewZoom})`;
    img.style.cursor = previewZoom > 1 ? 'grab' : 'zoom-in';
    const value = $('pm-zoom-value');
    if (value) value.textContent = Math.round(previewZoom * 100) + '%';
}
function setPreviewZoom(next) {
    previewZoom = Math.max(0.25, Math.min(6, Number(next) || 1));
    if (previewZoom <= 1) previewPan = { x: 0, y: 0 };
    applyPreviewTransform();
}
function fitPreview() {
    previewPan = { x: 0, y: 0 };
    setPreviewZoom(1);
}
async function openPreview(id, openInfo) {
    // open first, fill second: fetching chunk health can take a second on a
    // busy host and a dead click feels broken.
    const modal = $('modal-preview');
    previewFor = id;
    previewFiles = visibleFiles().filter(x => !x.is_dir);
    previewIndex = previewFiles.findIndex(x => x.id === id);
    const known = previewFiles[previewIndex];
    previewCurrent = known || null;
    document.querySelectorAll('.pm-edge').forEach(el => el.classList.toggle('hidden', previewFiles.length < 2));
    $('pm-name').textContent = known ? known.name : 'Loading…';
    $('pm-size').textContent = known && !known.is_dir ? fmtBytes(known.size) : '';
    $('pm-stage').innerHTML = '<div class="pm-loading"><span class="spinner"></span>Opening…</div>';
    $('pm-details-list').innerHTML = ''; $('pm-health-list').innerHTML = '';
    // never leave the SHARES heading standing over nothing while the list loads
    $('pm-shares').innerHTML = '<div class="pm-empty">Loading…</div>';
    document.querySelectorAll('.pm-zoom-control,.pm-zoom-value,.pm-fit').forEach(el => el.classList.add('hidden'));
    previewPan = { x: 0, y: 0 }; previewZoom = 1;
    modal.classList.remove('hidden');
    if (openInfo) toggleInfo(true);
    loadSharesInto(id); // in flight alongside the details request, not after it
    const r = await api('/api/files/details?file_id=' + encodeURIComponent(id));
    if (previewFor !== id || modal.classList.contains('hidden')) return; // moved on already
    if (!r.ok) { closePreview(); return toast(r.error || 'Not found', 'error'); }
    previewCurrent = r.data;
    renderPreview(r.data);
    loadSharesInto(id);
}
function previewSubtitle(f) {
    const parts = f.chunk_count || 0, att = f.attachment_count || 0, srv = f.replica_servers || 0;
    const bits = [fmtBytes(f.size), parts + (parts === 1 ? ' part' : ' parts')];
    if (att > parts) bits.push(att + ' attachments');
    bits.push(srv + (srv === 1 ? ' server' : ' servers'));
    return bits.join(' · ');
}
function renderPreview(f) {
    $('pm-name').textContent = f.name;
    $('pm-size').textContent = previewSubtitle(f);
    $('pm-favorite').classList.toggle('faved', !!f.favorite);
    const stage = $('pm-stage'); stage.innerHTML = '';
    const k = kindOf(f);
    const media = withSession('/api/download/file?file_id=' + encodeURIComponent(f.id) + '&inline=1');
    document.querySelectorAll('.pm-zoom-control,.pm-zoom-value,.pm-fit').forEach(el => el.classList.toggle('hidden', k !== 'image'));
    if (k === 'image') {
        stage.innerHTML = '<div class="pm-loading"><span class="spinner"></span>Decrypting…</div>';
        const img = new Image();
        img.id = 'pm-image'; img.alt = f.name; img.draggable = false;
        const fallback = msg => {
            stage.innerHTML = `<div class="no-preview"><div class="np-icon">${ICON.image}</div>${esc(msg)}<button class="btn-primary btn-sm" id="pm-dl2">Download file</button></div>`;
            const b = stage.querySelector('#pm-dl2'); if (b) b.onclick = () => dl(f);
        };
        // a stalled shard fetch must not leave the viewer spinning forever
        const slow = setTimeout(() => {
            const note = stage.querySelector('.pm-loading');
            if (note) note.innerHTML = '<span class="spinner"></span>Still fetching encrypted parts…<button class="btn-secondary btn-sm" id="pm-dl3">Download instead</button>';
            const b = stage.querySelector('#pm-dl3'); if (b) b.onclick = () => dl(f);
        }, 20000);
        img.onload = () => { clearTimeout(slow); stage.innerHTML = ''; stage.appendChild(img); fitPreview(); };
        img.onerror = () => { clearTimeout(slow); fallback('This image could not be decoded in the browser.'); };
        img.src = media;
    }
    else if (k === 'video') stage.innerHTML = `<video controls autoplay playsinline src="${media}"></video>`;
    else if (k === 'audio') stage.innerHTML = `<audio controls autoplay src="${media}"></audio>`;
    else if (k === 'pdf') stage.innerHTML = `<iframe src="${media}#toolbar=1"></iframe>`;
    else if (k === 'text' || k === 'code') {
        stage.innerHTML = '<pre>Loading…</pre>';
        fetch(media).then(r => r.text()).then(t => { stage.querySelector('pre').textContent = t.length > 500000 ? t.slice(0, 500000) + '\n\n… truncated — download for full file' : t; })
            .catch(() => stage.innerHTML = '<div class="no-preview"><div class="np-icon">⚠</div>Could not load text preview</div>');
    } else {
        stage.innerHTML = `<div class="no-preview"><div class="np-icon">${ICON[k] || ICON.other}</div>${k === 'dir' ? 'Folder — open from the drive.' : 'No inline preview for this type'}<button class="btn-primary btn-sm" id="pm-dl2">Download file</button></div>`;
        const b = stage.querySelector('#pm-dl2'); if (b) b.onclick = () => dl(f);
    }
    renderDetails(f);
}
function renderDetails(f) {
    const list = $('pm-details-list');
    list.innerHTML = '';
    const add = (dt, dd) => { list.innerHTML += `<dt>${esc(dt)}</dt><dd>${esc(dd)}</dd>`; };
    add('Name', f.name); add('Path', f.path || '/');
    add('Size', fmtBytes(f.size)); add('Modified', fmtDate(f.mod_time));
    if (f.sha256) add('SHA-256', f.sha256);
    const h = $('pm-health-list'); h.innerHTML = '';
    const parts = Array.isArray(f.parts) ? f.parts : [];
    if (parts.length) {
        for (const p of parts) {
            const stored = p.status === 'COMPLETED';
            const row = document.createElement('div'); row.className = 'health-row';
            const where = stored
                ? p.copies + (p.copies === 1 ? ' copy' : ' copies') + (p.servers ? ' · ' + p.servers + (p.servers === 1 ? ' server' : ' servers') : '')
                : 'not stored yet';
            row.innerHTML = `<span class="health-dot${stored ? '' : ' missing'}"></span>
                <span class="health-part">part ${p.index + 1}</span>
                <span class="health-size">${fmtBytes(p.size || 0)} · ${esc(where)}</span>`;
            if (stored) row.onclick = () => {
                location.href = withSession('/api/files/raw_chunk?file_id=' + encodeURIComponent(f.id) + '&chunk_index=' + p.index);
                toast('Downloading raw encrypted part ' + (p.index + 1), 'info');
            };
            h.appendChild(row);
        }
    } else if (!f.is_dir) {
        const row = document.createElement('div'); row.className = 'health-row';
        row.innerHTML = '<span class="health-dot missing"></span><span class="health-part">no parts recorded</span>';
        h.appendChild(row);
    }
    const vr = document.createElement('button'); vr.className = 'btn-secondary btn-sm'; vr.style.width = '100%';
    vr.textContent = 'Verify integrity (re-download + hash)';
    vr.onclick = async () => {
        vr.disabled = true; vr.textContent = 'Verifying…';
        const r = await api('/api/verify', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ file_id: f.id }) });
        vr.disabled = false; vr.textContent = 'Verify integrity (re-download + hash)';
        let box = $('pm-verify'); if (!box) { box = document.createElement('div'); box.id = 'pm-verify'; h.after(box); }
        if (r.ok && r.data.result) {
            const res = r.data.result;
            box.innerHTML = `<div class="verify-result ${res.ok ? 'ok' : 'fail'}">${res.ok ? '✓ Integrity confirmed — ' + (res.chunks || []).filter(c => c.ok).length + '/' + (res.chunks || []).length + ' parts + whole-file hash match' : '✗ Verification failed: ' + esc(res.error || 'hash mismatch')}</div>`;
            logLine('verify ' + (res.ok ? 'ok' : 'FAIL') + ': ' + f.name, res.ok ? 'ok' : 'err');
        } else box.innerHTML = `<div class="verify-result fail">${esc(r.error || 'verify failed')}</div>`;
    };
    h.appendChild(vr);
}
async function loadSharesInto(id) {
    const r = await api('/api/shares/list?file_id=' + encodeURIComponent(id));
    const box = $('pm-shares');
    if (!box || previewFor !== id) return; // a later file already owns the panel
    box.innerHTML = '';
    if (!r.ok) { box.innerHTML = '<div class="pm-empty">Links unavailable.</div>'; return; }
    const shares = (r.data && r.data.shares) || [];
    if (!shares.length) { box.innerHTML = '<div class="pm-empty">No active links.</div>'; return; }
    for (const sh of shares) {
        const div = document.createElement('div'); div.className = 'share-row';
        div.innerHTML = `<span>${sh.downloads || 0} dl</span><span class="share-exp">${esc(sh.expires_at ? new Date(sh.expires_at * 1000).toLocaleDateString() : '—')}</span>${S.canWrite ? '<button class="share-rev" title="Revoke">&times;</button>' : ''}`;
        const rev = div.querySelector('.share-rev');
        if (rev) rev.onclick = async () => { await api('/api/shares/revoke', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ id: sh.id }) }); loadSharesInto(id); };
        box.appendChild(div);
    }
}
function toggleInfo(force) {
    const p = $('pm-info-panel');
    p.classList.toggle('hidden', force === true ? false : force === false ? true : !p.classList.contains('hidden'));
}
function closePreview() {
    const modal = $('modal-preview');
    modal.classList.add('hidden');
    previewFor = ''; previewCurrent = null;
    // clearing the stage stops <video>/<audio> — merely hiding the modal keeps it playing
    $('pm-stage').innerHTML = '';
}
const previewTarget = () => previewCurrent || previewFiles[previewIndex] || null;
document.addEventListener('click', e => {
    if (e.target.closest('#pm-close') || (e.target.id === 'modal-preview' && !e.target.closest('.preview-modal'))) closePreview();
    if (e.target.closest('#pm-info')) toggleInfo();
    if (e.target.closest('#pm-download')) { const f = previewTarget(); if (f) dl(f); }
    if (e.target.closest('#pm-share')) { const f = previewTarget(); if (f) copyLink(f); }
    if (e.target.closest('#pm-favorite')) { const f = previewTarget(); if (f) favoriteItem(f, !f.favorite).then(() => $('pm-favorite').classList.toggle('faved', !!f.favorite)); }
    if (e.target.closest('#pm-prev')) navPreview(-1);
    if (e.target.closest('#pm-next')) navPreview(1);
    if (e.target.closest('#pm-zoom-in')) setPreviewZoom(previewZoom * 1.25);
    if (e.target.closest('#pm-zoom-out')) setPreviewZoom(previewZoom / 1.25);
    if (e.target.closest('#pm-fit')) fitPreview();
    // releasing a pan fires a click on the image; that must not snap zoom back
    if (e.target.id === 'pm-image' && !panJustEnded) setPreviewZoom(previewZoom > 1 ? 1 : 2);
});
// ctrl/⌘ + wheel zooms the image like a desktop viewer; plain wheel still scrolls
document.addEventListener('wheel', e => {
    if (!$('pm-image') || !e.target.closest('#pm-stage')) return;
    if (!e.ctrlKey && !e.metaKey) return;
    e.preventDefault();
    setPreviewZoom(previewZoom * (e.deltaY < 0 ? 1.12 : 1 / 1.12));
}, { passive: false });
// drag to pan once zoomed past fit
let panJustEnded = false;
(() => {
    let panning = false, moved = false, sx = 0, sy = 0, ox = 0, oy = 0;
    document.addEventListener('mousedown', e => {
        if (e.target.id !== 'pm-image' || previewZoom <= 1) return;
        panning = true; moved = false; sx = e.clientX; sy = e.clientY; ox = previewPan.x; oy = previewPan.y;
        e.target.classList.add('panning');
        e.target.style.cursor = 'grabbing'; e.preventDefault();
    });
    document.addEventListener('mousemove', e => {
        if (!panning) return;
        if (Math.abs(e.clientX - sx) > 3 || Math.abs(e.clientY - sy) > 3) moved = true;
        previewPan = { x: ox + (e.clientX - sx), y: oy + (e.clientY - sy) };
        applyPreviewTransform();
    });
    document.addEventListener('mouseup', () => {
        if (!panning) return;
        panning = false;
        panJustEnded = moved;
        const img = $('pm-image'); if (img) img.classList.remove('panning');
        applyPreviewTransform();
        setTimeout(() => { panJustEnded = false; }, 0);
    });
})();
function navPreview(dir) {
    if (previewFiles.length < 2) return;
    previewIndex = (previewIndex + dir + previewFiles.length) % previewFiles.length;
    openPreview(previewFiles[previewIndex].id);
}
document.addEventListener('keydown', e => {
    if ($('modal-preview').classList.contains('hidden')) return;
    if (e.target instanceof HTMLInputElement || e.target instanceof HTMLTextAreaElement) return;
    if (e.key === 'ArrowLeft') navPreview(-1);
    if (e.key === 'ArrowRight') navPreview(1);
    if (e.key === 'Escape') closePreview();
    if (!$('pm-image')) return;
    if (e.key === '+' || e.key === '=') { e.preventDefault(); setPreviewZoom(previewZoom * 1.25); }
    if (e.key === '-' || e.key === '_') { e.preventDefault(); setPreviewZoom(previewZoom / 1.25); }
    if (e.key === '0') { e.preventDefault(); fitPreview(); }
});

// ---------- dialogs ----------
function showInput(title, value, isSelect) {
    return new Promise(resolve => {
        const ov = $('modal-input');
        $('mi-title').textContent = title;
        const field = $('mi-field'), sel = $('mi-select');
        const useSel = !!isSelect;
        field.classList.toggle('hidden', useSel); sel.classList.toggle('hidden', !useSel);
        if (useSel) { sel.innerHTML = ''; const o = document.createElement('option'); o.value = ''; o.textContent = '— Cloud Drive (root) —'; sel.appendChild(o);
            for (const f of value) { const oo = document.createElement('option'); oo.value = f.id; oo.textContent = '　'.repeat(f.depth || 0) + '📁 ' + f.name; sel.appendChild(oo); } }
        else { field.value = value || ''; field.type = (title.toLowerCase().includes('password')) ? 'password' : 'text'; }
        ov.classList.remove('hidden'); (useSel ? sel : field).focus();
        const done = v => { ov.classList.add('hidden'); $('mi-ok').onclick = $('mi-cancel').onclick = null; resolve(v); };
        $('mi-ok').onclick = () => done(useSel ? sel.value : field.value.trim());
        $('mi-cancel').onclick = () => done(null);
        if (!useSel) field.onkeydown = ev => { if (ev.key === 'Enter') done(field.value.trim()); };
    });
}
async function pickFolder(title) {
    const dirs = [];
    const walk = async (pid, depth) => {
        const r = await api('/api/files' + (pid ? '?parent_id=' + encodeURIComponent(pid) : ''));
        for (const f of (r.data || [])) if (f.is_dir) { dirs.push({ id: f.id, name: f.name, path: f.path, depth }); await walk(f.id, depth + 1); }
    };
    await walk('', 0);
    if (!dirs.length) return { id: '' };
    const chosen = await showInput(title, dirs, true);
    if (chosen === null) return undefined;
    return { id: chosen, name: chosen ? dirs.find(d => d.id === chosen).name : 'Cloud Drive' };
}
function confirmBox(title, msg, danger) {
    return new Promise(resolve => {
        const ov = $('modal-confirm');
        $('mc-title').textContent = title; $('mc-msg').textContent = msg || '';
        const okb = $('mc-ok'); okb.className = danger ? 'btn-danger' : 'btn-primary'; okb.style.height = '34px';
        ov.classList.remove('hidden');
        const done = v => { ov.classList.add('hidden'); okb.onclick = $('mc-cancel').onclick = null; resolve(v); };
        okb.onclick = () => done(true); $('mc-cancel').onclick = () => done(false);
    });
}

// ---------- creation / upload ----------
async function createFolder() {
    if (!requireWrite()) return;
    const name = await showInput('New folder', '');
    if (!name) return;
    const r = await api('/api/folders/create', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name, parent_id: S.parentID }) });
    if (r.ok) { toast('Folder created', 'success'); flashFile((r.data.folder || {}).id); reloadCurrent(); } else toast(r.error, 'error');
}
async function createTextFile() {
    if (!requireWrite()) return;
    const name = await showInput('New text file', 'untitled.txt');
    if (!name) return;
    const content = await showInput('Content of ' + name, '');
    if (content === null) return;
    const r = await api('/api/files/create_text', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name, content, parent_id: S.parentID }) });
    if (r.ok) { toast('File created — uploading…', 'success'); } else toast(r.error, 'error');
}
// An upload has two legs and the browser only sees the first one: the multipart
// body travels to the server, and only THEN does the server start encrypting and
// posting shards to Discord (which is what /ws telemetry reports). The old code
// awaited fetch() before registering the transfer, so for the whole send leg —
// minutes on a large file — there was no toast, no badge, no card and no row:
// indistinguishable from "upload is broken". XHR is used instead of fetch purely
// because it is the only way to observe upload progress in a browser.
const XFER_STAGE = { sending: 'Sending', queued: 'Processing', storing: 'Encrypting' };
async function uploadOne(file, parentId) {
    const localID = 'send-' + Date.now().toString(36) + '-' + Math.random().toString(36).slice(2, 7);
    const parts = Math.max(1, Math.ceil(file.size / 7.5e6));
    const entry = {
        name: file.name, type: 'UPLOAD', file_id: '', total_bytes: file.size,
        processed_bytes: 0, chunks_done: 0, chunks_total: parts, speed: 0, eta: 0,
        progress: 0, status: 'ACTIVE', error: '', at: Date.now(), stage: 'sending',
    };
    allTransfers.set(localID, entry);
    transfersDirty = true; renderTransfers();

    const fd = new FormData();
    fd.append('file', file); fd.append('parent_id', parentId || S.parentID);
    const tgt = $('upload-target-select').value;
    if (tgt && tgt !== 'all') fd.append('target_guild_id', tgt);

    const res = await new Promise(resolve => {
        const xhr = new XMLHttpRequest();
        xhr.open('POST', '/api/upload/file');
        const s = session();
        if (s) xhr.setRequestHeader('X-DFC-Session', s);
        const startedAt = Date.now();
        xhr.upload.onprogress = ev => {
            if (!ev.lengthComputable) return;
            const secs = Math.max(0.25, (Date.now() - startedAt) / 1000);
            const rate = ev.loaded / secs;
            entry.processed_bytes = ev.loaded;
            entry.progress = ev.total ? (ev.loaded / ev.total) * 100 : 0;
            entry.speed = rate / 1e6;
            entry.eta = rate > 0 ? Math.max(0, (ev.total - ev.loaded) / rate) : 0;
            // The body is sent but the server has not answered yet (it stages the
            // file and starts the job first). Saying "Sending · 0s left" there
            // looks stuck; name the wait instead.
            if (ev.total && ev.loaded >= ev.total) { entry.stage = 'queued'; entry.speed = 0; entry.eta = 0; }
            renderTransfers();
        };
        xhr.onload = () => {
            let data = {};
            try { data = JSON.parse(xhr.responseText || '{}'); } catch (e) { data = {}; }
            resolve({ ok: xhr.status >= 200 && xhr.status < 300 && data.ok !== false, status: xhr.status, data, error: data.error || ('HTTP ' + xhr.status) });
        };
        xhr.onerror = () => resolve({ ok: false, status: 0, data: {}, error: 'network error' });
        xhr.onabort = () => resolve({ ok: false, status: 0, data: {}, error: 'cancelled' });
        xhr.send(fd);
    });

    if (res.status === 401) { setSession(''); showLock('Session expired — unlock again.'); }
    if (!res.ok || !res.data.job_id) {
        entry.status = 'FAILED';
        entry.error = res.error || 'upload rejected';
        transfersDirty = true; saveTransfers(); renderTransfers();
        toast('Upload failed: ' + entry.error, 'error');
        logLine('upload FAILED ' + file.name + ': ' + entry.error, 'err');
        return null;
    }
    // Hand the card over to the job id so /ws telemetry keeps updating the same
    // entry instead of opening a second one next to it.
    allTransfers.delete(localID);
    allTransfers.set(res.data.job_id, Object.assign({}, entry, {
        stage: 'storing', progress: 0, processed_bytes: 0, speed: 0, eta: 0,
    }));
    transfersDirty = true; saveTransfers(); renderTransfers();
    watchJob(res.data.job_id);
    return res.data.job_id;
}

// Safety net for the listing. A completed upload is normally revealed by the
// telemetry COMPLETED event or files_changed, but a throttled tab or a WebSocket
// reconnect can drop both, and then the file exists on Discord while the drive
// still shows the old contents. Poll until the job leaves the active list, then
// refresh once regardless of what the socket did or did not deliver.
const watchedJobs = new Set();
function watchJob(jobID) {
    if (!jobID || watchedJobs.has(jobID)) return;
    watchedJobs.add(jobID);
    let ticks = 0;
    const tick = async () => {
        ticks++;
        const r = await api('/api/jobs');
        const rows = Array.isArray(r.data) ? r.data : [];
        const live = rows.find(j => j.id === jobID);
        if (live && live.status === 'ACTIVE' && ticks < 400) { setTimeout(tick, 3000); return; }
        watchedJobs.delete(jobID);
        const t = allTransfers.get(jobID);
        if (t && t.status !== 'FAILED') {
            if (live && live.status === 'FAILED') { t.status = 'FAILED'; t.error = live.error || 'upload failed'; }
            else { t.status = 'COMPLETED'; t.progress = 100; t.processed_bytes = t.total_bytes; }
            saveTransfers(); renderTransfers();
        }
        reloadCurrent(); loadStatus();
    };
    setTimeout(tick, 3000);
}
async function uploadFiles(files) {
    if (!files.length || !requireWrite()) return;
    await ensureParentForUpload();
    toast(files.length === 1 ? 'Uploading ' + files[0].name + '…' : 'Uploading ' + files.length + ' files…', 'info');
    toggleDrawer(true);
    for (const f of files) await uploadOne(f, S.parentID);
}
async function uploadDirectory(fileList) {
    await ensureParentForUpload();
    const files = [...fileList];
    if (!files.length) return;
    const roots = new Set(files.map(f => (f.webkitRelativePath || '').split('/')[0]).filter(Boolean));
    const cache = new Map([['', S.parentID]]);
    const folderFor = async rel => {
        if (!rel) return S.parentID;
        if (cache.has(rel)) return cache.get(rel);
        const parts = rel.split('/').filter(Boolean);
        const parent = await folderFor(parts.slice(0, -1).join('/'));
        const name = parts[parts.length - 1];
        const r = await api('/api/folders/create', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name, parent_id: parent }) });
        const id = r.ok ? (r.data.folder || {}).id : parent;
        cache.set(rel, id);
        return id;
    };
    const top = [...roots][0] || 'upload';
    toast('Uploading folder "' + top + '" (' + files.length + ' files)…', 'info');
    toggleDrawer(true);
    for (const f of files) {
        const rel = (f.webkitRelativePath || f.name).split('/').slice(0, -1).join('/');
        await uploadOne(f, await folderFor(rel));
    }
    reloadCurrent();
}
async function ensureParentForUpload() { if (!S.parentID && (S.view !== 'drive' || $('search-input').value.trim())) S.parentID = ''; }
async function uploadDataTransfer(dt) {
    await ensureParentForUpload();
    const target = S.parentID;
    const items = dt.items && dt.items.length ? [...dt.items] : [];
    const entries = items.map(it => it.webkitGetAsEntry && it.webkitGetAsEntry()).filter(Boolean);
    const files = [];
    const walkEntry = async (entry, rel) => {
        if (entry.isFile) { const f = await new Promise(res => entry.file(res)); files.push({ file: f, rel }); }
        else if (entry.isDirectory) {
            const reader = entry.createReader();
            let batch; do { batch = await new Promise(res => reader.readEntries(res, () => res([]))); for (const b of batch) await walkEntry(b, rel + entry.name + '/'); } while (batch.length);
        }
    };
    if (entries.length) { for (const e of entries) await walkEntry(e, ''); }
    else if (dt.files) for (const f of dt.files) files.push({ file: f, rel: '' });
    if (!files.length) { toast('Nothing to upload — the drop contained no files', 'error'); return; }
    // announce BEFORE the loop: the old toast fired after every file had finished,
    // i.e. exactly when the user no longer needed telling
    toast('Uploading ' + files.length + ' file' + (files.length === 1 ? '' : 's') + '…', 'info');
    toggleDrawer(true);
    const folderCache = new Map([['', target]]);
    const folderFor = async rel => {
        if (!rel) return target;
        if (folderCache.has(rel)) return folderCache.get(rel);
        const parts = rel.split('/').filter(Boolean);
        const parentRel = parts.slice(0, -1).join('/');
        const parent = await folderFor(parentRel);
        const name = parts[parts.length - 1];
        const r = await api('/api/folders/create', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name, parent_id: parent }) });
        const id = r.ok ? (r.data.folder || {}).id : parent;
        folderCache.set(rel, id);
        return id;
    };
    for (const { file, rel } of files) await uploadOne(file, await folderFor(rel));
}
document.addEventListener('change', e => {
    if (e.target.id === 'file-input') { uploadFiles([...e.target.files]); e.target.value = ''; }
    if (e.target.id === 'dir-input') { uploadDirectory(e.target.files); e.target.value = ''; }
    if (e.target.id === 'sort-select') { const [k, d] = e.target.value.split('-'); S.sortKey = k; S.sortDir = d; render(); }
});
let dragDepth = 0;
document.addEventListener('dragenter', e => { if (S.canWrite && e.dataTransfer && [...(e.dataTransfer.types || [])].includes('Files')) { dragDepth++; $('drop-overlay').classList.remove('hidden'); } });
document.addEventListener('dragleave', () => { if (--dragDepth <= 0) { dragDepth = 0; $('drop-overlay').classList.add('hidden'); } });
document.addEventListener('dragover', e => e.preventDefault());
document.addEventListener('drop', e => {
    if (!e.dataTransfer || ![...(e.dataTransfer.types || [])].includes('Files')) return;
    e.preventDefault(); dragDepth = 0; $('drop-overlay').classList.add('hidden');
    if (!requireWrite()) return;
    uploadDataTransfer(e.dataTransfer);
});

// ---------- transfers rendering ----------
function xferCard(t, id) {
    const st = t.status === 'COMPLETED' ? 'done' : t.status === 'FAILED' ? 'failed' : t.status === 'PAUSED' ? 'paused' : 'active';
    // An upload is two legs with two separate progress sources, so name the leg
    // instead of pretending one bar: "Sending" is the browser→server body (XHR
    // progress), "Encrypting" is the server→Discord shard publish (/ws telemetry).
    const active = t.type === 'DOWNLOAD' ? 'Downloading' : t.type === 'DELETE' ? 'Cleaning'
        : (t.stage && XFER_STAGE[t.stage]) || 'Uploading';
    const label = { done: 'Done', failed: 'Failed', paused: 'Paused', active }[st];
    const icon = t.type === 'DOWNLOAD' ? ACT.download : t.type === 'DELETE' ? ACT.trash : ACT.upload;
    const meta = st === 'active' && t.speed > 0 ? ' · ' + t.speed.toFixed(1) + ' MB/s · ' + Math.round(t.eta || 0) + 's left' : '';
    // On the storing leg the server counts the bytes it has to publish, which
    // includes every replica — a 20 MB file reports a 40 MB total across two
    // guilds. Rendering that as "4 MB / 40 MB" reads as if the file doubled, so
    // the shard counter is the honest unit there; bytes belong to the send leg.
    const sub = t.stage === 'storing' && t.chunks_total
        ? `part ${Math.min(t.chunks_done || 0, t.chunks_total)} of ${t.chunks_total} encrypted & published`
        : `${fmtBytes(t.processed_bytes || 0)} / ${fmtBytes(t.total_bytes || 0)}`;
    return `<div class="xfer-card" data-id="${esc(id)}">
        <div class="xfer-top"><span class="xfer-ic">${icon}</span>
            <div style="min-width:0;flex:1"><div class="xfer-name">${esc(t.name)}</div>
            <div class="xfer-sub">${sub}${meta}${st === 'failed' ? ' · ' + esc(t.error || 'failed') : ''}</div></div>
            <span class="xfer-state ${st}">${label}</span></div>
        <div class="xfer-bar ${st}"><div style="width:${Math.max(2, t.progress || 0)}%"></div></div>
        ${st === 'active' ? `<div class="xfer-actions"><button class="xfer-x danger" data-x="cancel">Cancel</button></div>` : ''}
        ${st === 'failed' ? `<div class="xfer-actions"><button class="xfer-x" data-x="dismiss">Dismiss</button></div>` : ''}
    </div>`;
}
function renderTransfers() {
    const act = activeTransfers();
    const badge = $('nav-transfer-badge'); badge.classList.toggle('hidden', act.length === 0); badge.textContent = act.length;
    $('hud-dot').classList.toggle('hidden', act.length === 0);
    renderTransfersDrawer();
    if (S.view === 'transfers') renderTransfersPage();
}
function renderTransfersDrawer() {
    const body = $('transfers-drawer-body'); const rec = recentTransfers();
    if (!rec.length) { body.innerHTML = '<div class="xfer-empty">No transfers yet.<br>Files you upload appear here.</div>'; $('transfers-drawer-foot').innerHTML = ''; return; }
    body.innerHTML = rec.slice(0, 20).map(t => xferCard(t, t.id)).join('');
    const act = activeTransfers();
    let sumSpeed = 0, sumEta = 0;
    for (const t of act) { sumSpeed += t.speed || 0; sumEta = Math.max(sumEta, t.eta || 0); }
    $('transfers-drawer-foot').innerHTML = act.length ? `<span>~${Math.round(sumEta)}s remaining</span><span>${sumSpeed.toFixed(1)} MB/s</span><button class="xfer-x danger" style="margin-left:auto" data-x="cancel-all">Cancel all</button>` : '';
}
function renderTransfersPage() {
    const list = $('transfers-list');
    const rec = recentTransfers();
    $('btn-cancel-all').classList.toggle('hidden', activeTransfers().length === 0);
    list.innerHTML = rec.length ? rec.map(t => xferCard(t, t.id)).join('') : '<div class="xfer-empty">No transfers yet.</div>';
}
document.addEventListener('click', async e => {
    const card = e.target.closest('.xfer-card');
    if (e.target.dataset.x === 'cancel' && card) { await api('/api/jobs/cancel', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ job_id: card.dataset.id }) }); }
    if (e.target.dataset.x === 'dismiss' && card) { allTransfers.delete(card.dataset.id); saveTransfers(); renderTransfers(); }
    if (e.target.dataset.x === 'cancel-all' || e.target.id === 'btn-cancel-all') { await api('/api/jobs/cancel', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ job_id: 'all' }) }); }
    if (e.target.closest('#btn-close-transfers')) toggleDrawer(false);
    if (e.target.closest('#btn-open-transfers')) toggleDrawer(true);
});
function toggleDrawer(open) {
    $('transfers-drawer').classList.toggle('closed', !open);
    document.body.classList.toggle('drawer-open', !!open);
}

// ---------- infrastructure ----------
async function loadBots() {
    const r = await api('/api/bots');
    S.bots = (r.data && r.data.nodes) || [];
    const box = $('bots-list');
    if (!S.bots.length) { box.innerHTML = '<div class="infra-card">No bots yet. Paste a bot token above to provision encrypted storage in your server.</div>'; }
    else box.innerHTML = S.bots.map(b => `<div class="bot-card">
        <span class="bc-avatar">🤖</span>
        <div class="bc-main"><div class="bc-name">${esc(b.bot_name || 'Bot')} ${b.status === 'Active' ? '<span class="st ok" style="font-size:10px;vertical-align:1px">ACTIVE</span>' : ''}</div>
        <div class="bc-sub">token: ${esc(b.bot_token ? '••••' + b.bot_token.slice(-4) : 'stored')} · ${b.guilds ? esc(b.guilds.join(', ')) : 'no server'}</div></div>
        <div class="bc-stats"><span>${b.channel_count || 0} ch</span><span>${fmtBytes(b.storage_bytes || 0)}</span></div>
        <button class="bc-del" data-id="${esc(b.id)}" data-token="${b.bot_token ? '1' : ''}" title="Remove">✕</button></div>`).join('');
    populateTargets();
}
document.addEventListener('click', async e => {
    const del = e.target.closest('.bc-del');
    if (del) {
        if (!await confirmBox('Remove this bot?', 'Its channels are cleared from the catalog. Files already on Discord stay until you clean the channel.')) return;
        const r = await api('/api/bots/delete', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ id: del.dataset.id }) });
        if (r.ok) { toast('Bot removed', 'success'); loadBots(); } else toast(r.error, 'error');
    }
    if (e.target.id === 'btn-add-bot') {
        const token = $('bot-token-input').value.trim(), guild = $('guild-id-input').value.trim();
        if (!token) return toast('Bot token required', 'error');
        toast('Registering bot…', 'info');
        const r = await api('/api/bots/add', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ token, guild_id: guild || undefined }) });
        if (r.ok) { toast('Bot registered — storage channels will be provisioned on first upload', 'success'); $('bot-token-input').value = ''; $('guild-id-input').value = ''; loadBots(); loadStatus(); }
        else toast(r.error, 'error');
    }
    if (e.target.id === 'btn-banner-bots') switchView('bots');
});
// Discord-style initial tile: letter and hue both derive from the name, so two
// servers never render as the same badge.
const AVATAR_HUES = ['#2f7ff5', '#5865f2', '#a855f7', '#31c48d', '#f5a524', '#e5484d', '#0ea5e9'];
const avatarFor = name => {
    const label = String(name || '?').trim();
    let h = 0;
    for (let i = 0; i < label.length; i++) h = (h * 31 + label.charCodeAt(i)) >>> 0;
    return { letter: (label[0] || '?').toUpperCase(), hue: AVATAR_HUES[h % AVATAR_HUES.length] };
};
async function loadServers() {
    const r = await api('/api/servers');
    S.servers = (r.data && r.data.servers) || [];
    const box = $('servers-list');
    if (!S.servers.length) { box.innerHTML = '<div class="infra-card">No servers connected. Add a bot to replicate encrypted shards across servers.</div>'; return; }
    box.innerHTML = S.servers.map(s => {
        const name = s.guild_name || s.guild_id || 'Server';
        const av = avatarFor(name);
        // plain words, not an .st chip: `.st.ok` is display:none by design so a
        // healthy file shows no badge, which would silently blank this line
        const state = String(s.status || 'unknown').toLowerCase();
        return `<div class="server-card">
        <span class="bc-avatar" style="background:${av.hue}">${esc(av.letter)}</span>
        <div class="bc-main"><div class="bc-name">${esc(name)}</div>
        <div class="bc-sub">${s.channel_count || 0} storage channels · shards ${esc(state)}</div></div>
        <div class="bc-stats"><span>${fmtBytes(s.storage_bytes || 0)}</span></div></div>`;
    }).join('');
    populateTargets();
}
function populateTargets() {
    const sel = $('upload-target-select');
    const cur = sel.value;
    sel.innerHTML = '<option value="all">Every server</option>';
    for (const s of S.servers) { const o = document.createElement('option'); o.value = s.guild_id; o.textContent = s.guild_name || s.guild_id; sel.appendChild(o); }
    sel.value = [...sel.options].some(o => o.value === cur) ? cur : 'all';
}

// ---------- settings ----------
async function setPassword() {
    const pwd = $('cfg-new-password').value;
    if (pwd.length < 8) return toast('Password must be at least 8 characters', 'error');
    if (!await confirmBox('Change master password?', 'This re-derives the encryption key. Files uploaded with the old password become unreadable — this is a fresh start, not a rotation.', true)) return;
    const r = await api('/api/auth/set_password', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ password: pwd }) });
    if (r.ok && r.data.session) { setSession(r.data.session); $('cfg-new-password').value = ''; toast('Password updated — new key derived', 'success'); }
    else toast(r.error || 'Failed', 'error');
}
async function rotateToken(scope) {
    const r = await api('/api/create-token', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ scope }) });
    if (!r.ok) return toast(r.error, 'error');
    const out = $('token-output'); out.textContent = r.data.token; out.dataset.full = r.data.token; out.classList.remove('hidden');
    try { await navigator.clipboard.writeText(r.data.token); toast(scope + ' token rotated & copied — update your scripts', 'success'); }
    catch (e) { toast('New ' + scope + ' token shown in Settings; click it to copy', 'info'); }
    logLine('rotated ' + scope + ' api token');
}
document.addEventListener('click', async e => {
    if (e.target.id === 'btn-set-password') setPassword();
    if (e.target.id === 'btn-rotate-write') rotateToken('write');
    if (e.target.id === 'btn-rotate-read') rotateToken('read');
    if (e.target.id === 'token-output') { try { await navigator.clipboard.writeText(e.target.dataset.full); toast('Copied', 'success'); } catch (err) {} }
    if (e.target.id === 'btn-lock-now') { await api('/api/auth/lock', { method: 'POST' }); setSession(''); showLock('Locked — unlock to continue.'); }
    if (e.target.id === 'btn-catalog-sync') { const r = await api('/api/catalog/sync', { method: 'POST' }); toast(r.ok ? 'Catalog checkpointed' : 'Failed: ' + r.error, r.ok ? 'success' : 'error'); }
    if (e.target.id === 'btn-catalog-restore') { if (await confirmBox('Restore catalog?', 'Rebuilds the file listing from the last Discord checkpoint.')) { const r = await api('/api/catalog/restore', { method: 'POST' }); toast(r.ok ? 'Catalog restored: ' + (r.data.files_imported || 0) + ' files' : 'Failed: ' + r.error, r.ok ? 'success' : 'error'); reloadCurrent(); } }
});

// ---------- header / menus ----------
function wireUI() {
    document.querySelectorAll('.nav-button[data-view]').forEach(b => b.addEventListener('click', () => switchView(b.dataset.view)));
    $('nav-activity').addEventListener('click', () => {
        document.body.classList.remove('nav-open');
        $('log-drawer').classList.add('open'); $('drawer-scrim').classList.remove('hidden');
    });
    $('btn-settings').addEventListener('click', () => switchView('settings'));
    $('btn-sign-in').addEventListener('click', () => showLock());
    $('lock-dismiss').addEventListener('click', () => { $('lock-password').value = ''; hideLock(); });
    $('btn-mobile-menu').addEventListener('click', () => document.body.classList.toggle('nav-open'));
    $('sidebar-scrim').addEventListener('click', () => document.body.classList.remove('nav-open'));
    $('btn-unlock').addEventListener('click', unlock);
    $('lock-password').addEventListener('keydown', e => { if (e.key === 'Enter') unlock(); });
    $('btn-new-menu').addEventListener('click', e => {
        e.stopPropagation();
        const menu = $('new-menu');
        menu.classList.toggle('hidden');
        if (menu.classList.contains('hidden')) return;
        // anchor the panel to the button that opened it
        const b = $('btn-new-menu').getBoundingClientRect();
        const main = document.querySelector('.main').getBoundingClientRect();
        menu.style.top = Math.round(b.bottom - main.top + 6) + 'px';
        menu.style.right = Math.max(8, Math.round(main.right - b.right)) + 'px';
    });
    document.addEventListener('click', e => { if (!e.target.closest('#new-menu') && !e.target.closest('#btn-new-menu')) $('new-menu').classList.add('hidden'); });
    $('menu-upload-files').addEventListener('click', () => { $('file-input').click(); });
    $('menu-upload-folder').addEventListener('click', () => { $('dir-input').click(); });
    $('menu-new-folder').addEventListener('click', createFolder);
    $('menu-new-file').addEventListener('click', createTextFile);
    $('btn-grid').addEventListener('click', () => setLayout('grid'));
    $('btn-list').addEventListener('click', () => setLayout('list'));
    document.querySelectorAll('.th-sort').forEach(th => th.addEventListener('click', () => {
        const k = th.dataset.sort;
        if (S.sortKey === k) S.sortDir = S.sortDir === 'asc' ? 'desc' : 'asc'; else { S.sortKey = k; S.sortDir = k === 'name' ? 'asc' : 'desc'; }
        $('sort-select').value = S.sortKey + '-' + S.sortDir; render(); updateSortHeads();
    }));
    document.querySelectorAll('.pill').forEach(p => p.addEventListener('click', () => {
        S.filter = p.dataset.filter;
        document.querySelectorAll('.pill').forEach(x => x.classList.toggle('active', x === p));
        render();
    }));
    let searchT;
    $('search-input').addEventListener('input', () => {
        $('btn-clear-search').classList.toggle('hidden', !$('search-input').value);
        clearTimeout(searchT); searchT = setTimeout(() => { S.searchMode = !!$('search-input').value.trim(); reloadCurrent(); }, 250);
    });
    $('btn-clear-search').addEventListener('click', () => { $('search-input').value = ''; S.searchMode = false; $('btn-clear-search').classList.add('hidden'); reloadCurrent(); });
    $('th-select-all').addEventListener('change', e => {
        if (e.target.checked) visibleFiles().forEach(f => S.selected.add(f.id));
        else S.selected.clear();
        render();
    });
    $('bb-clear').addEventListener('click', () => { S.selected.clear(); render(); });
    $('bb-favorite').addEventListener('click', () => doBatch([...S.selected], 'favorite'));
    $('bb-trash').addEventListener('click', () => trashMove([...S.selected]));
    $('bb-delete').addEventListener('click', () => deleteForever([...S.selected]));
    $('btn-open-diagnostics').addEventListener('click', () => { $('log-drawer').classList.add('open'); $('drawer-scrim').classList.remove('hidden'); });
    $('btn-close-log').addEventListener('click', () => { $('log-drawer').classList.remove('open'); $('drawer-scrim').classList.add('hidden'); });
    $('drawer-scrim').addEventListener('click', () => { $('log-drawer').classList.remove('open'); $('drawer-scrim').classList.add('hidden'); });
    // show only the control that switches to the other mode, from the first paint
    $('btn-grid').classList.toggle('hidden', S.layout === 'grid');
    $('btn-list').classList.toggle('hidden', S.layout !== 'grid');
    updateSortHeads();
    toggleDrawer(false);
}
function setLayout(l) {
    S.layout = l; localStorage.setItem('dfc_layout', l);
    // one control, like Filen: show the button for the mode you are NOT in
    $('btn-grid').classList.toggle('hidden', l === 'grid');
    $('btn-list').classList.toggle('hidden', l !== 'grid');
    render();
}
function updateSortHeads() {
    document.querySelectorAll('.th-sort').forEach(th => { th.classList.remove('asc', 'desc'); if (th.dataset.sort === S.sortKey) th.classList.add(S.sortDir); });
}

// ---------- boot ----------
let booted = false;
async function boot() {
    if (booted) return; booted = true;
    connectWS();
    await loadStatus();
    switchView('drive');
    // /api/bots needs write scope; a published visitor would only collect a 403
    if (S.canWrite) { loadBots(); loadServers(); }
    loadStatus();
    reconcileJobs();
    setInterval(loadStatus, 60000);
    logLine('drive unlocked — engine ready', 'ok');
}
(async function init() {
    wireUI();               // binds unlock/new-menu/etc once
    const ok = await checkAuth();
    if (ok) boot();         // boot() must NOT call wireUI again
})();
