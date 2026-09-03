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
const fmtDate = ts => {
    try { const d = new Date(ts * 1000); return d.toDateString() + ' ' + d.toLocaleTimeString('en-GB'); }
    catch (e) { return '--'; }
};
const fmtDateShort = ts => { try { return new Date(ts * 1000).toDateString().slice(4); } catch (e) { return '--'; } };
const extOf = name => { const m = /\.([a-z0-9]+)$/i.exec(name || ''); return m ? m[1].toLowerCase() : ''; };

const IMAGE_EXT = new Set(['jpg','jpeg','png','gif','webp','bmp','ico','avif','svg','tiff']);
const VIDEO_EXT = new Set(['mp4','webm','mov','mkv','avi','m4v']);
const AUDIO_EXT = new Set(['mp3','wav','ogg','flac','aac','m4a','opus']);
const TEXT_EXT  = new Set(['txt','md','json','log','csv','yml','yaml','xml','ini','cfg','conf','env','toml','sql','sh','bash','bat','ps1','js','ts','jsx','tsx','mjs','css','scss','html','htm','go','py','rb','rs','java','kt','c','h','cpp','hpp','cs','php','lua','swift','r','pl','diff','patch','gitignore','dockerfile','makefile','readme','license','editorconfig']);
const ARCHIVE_EXT = new Set(['zip','rar','7z','tar','gz','bz2','xz','zst','tgz','iso','apk','jar']);
const CODE_FAM = new Set(['js','ts','jsx','tsx','mjs','css','scss','html','htm','go','py','rb','rs','java','kt','c','h','cpp','hpp','cs','php','lua','swift','r','pl','sql','json','xml','yml','yaml','sh','bash','ps1','dockerfile']);
const isImage = f => IMAGE_EXT.has(extOf(f.name));
const kindOf = f => f.is_dir ? 'dir'
    : IMAGE_EXT.has(extOf(f.name)) ? 'image'
    : VIDEO_EXT.has(extOf(f.name)) ? 'video'
    : AUDIO_EXT.has(extOf(f.name)) ? 'audio'
    : extOf(f.name) === 'pdf' ? 'pdf'
    : ARCHIVE_EXT.has(extOf(f.name)) ? 'archive'
    : CODE_FAM.has(extOf(f.name)) ? 'code'
    : TEXT_EXT.has(extOf(f.name)) ? 'text' : 'other';

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
    if (!d.auth_required || (d.has_session && d.is_unlocked)) { hideLock(); return true; }
    showLock();
    return false;
}
function showLock(errMsg) {
    $('lock-overlay').classList.remove('hidden');
    $('lock-error').classList.toggle('hidden', !errMsg);
    if (errMsg) $('lock-error').textContent = errMsg;
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
    if (r.ok && r.data.session) { setSession(r.data.session); $('lock-password').value = ''; hideLock(); if (!booted) boot(); else { connectWS(); loadStatus(); switchView('drive'); } }
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

async function reloadCurrent() {
    if (S.loading) return; S.loading = true;
    const params = new URLSearchParams();
    if (S.view === 'recents') params.set('view', 'recents');
    else if (S.view === 'favorites') params.set('view', 'favorites');
    else if (S.parentID) params.set('parent_id', S.parentID);
    const q = $('search-input').value.trim();
    if (q) { params.set('search', q); params.delete('parent_id'); params.delete('view'); }
    const r = await api('/api/files/view' + (params.toString() ? '?' + params : ''));
    S.files = Array.isArray(r.data) ? r.data : [];
    if (q) S.searchResults = S.files;
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
        case 'docs': return ['pdf','text'].includes(kindOf(f));
        case 'archives': return kindOf(f) === 'archive';
        case 'code': return kindOf(f) === 'code';
        default: return !f.is_dir || S.view !== 'drive' ? true : true;
    }
}
function visibleFiles() { return sortFiles(S.files.filter(passFilter)); }
function currentFolderId() { return S.parentID || (S.files.find(f => f.is_dir) || {}).parent_id || ''; }

// ---------- icons ----------
const ICON = {
    folder: '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z"/></svg>',
    image: '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="3" y="3" width="18" height="18" rx="2"/><circle cx="8.5" cy="8.5" r="1.5"/><path d="M21 15l-5-5L5 21"/></svg>',
    video: '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polygon points="23 7 16 12 23 17 23 7"/><rect x="1" y="5" width="15" height="14" rx="2"/></svg>',
    audio: '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M9 18V5l12-2v13"/><circle cx="6" cy="18" r="3"/><circle cx="18" cy="16" r="3"/></svg>',
    pdf: '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><polyline points="14 2 14 8 20 8"/></svg>',
    archive: '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 8v13H3V8"/><path d="M1 3h22v5H1z"/><path d="M10 12h4"/></svg>',
    code: '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><polyline points="16 18 22 12 16 6"/><polyline points="8 6 2 12 8 18"/></svg>',
    text: '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><polyline points="14 2 14 8 20 8"/><line x1="8" y1="13" x2="16" y2="13"/><line x1="8" y1="17" x2="13" y2="17"/></svg>',
    other: '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M13 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V9z"/><polyline points="13 2 13 9 20 9"/></svg>',
};
const iconFor = f => ICON[kindOf(f)] || ICON.other;

// ---------- rendering ----------
function render() {
    const files = visibleFiles();
    $('count-label').textContent = files.length + (files.length === 1 ? ' item' : ' items');
    const isEmpty = files.length === 0;
    $('empty-state').classList.toggle('hidden', !isEmpty);
    if (isEmpty) {
        const searching = $('search-input').value.trim();
        $('empty-title').textContent = searching ? 'Nothing matches your search' : S.view === 'recents' ? 'No recent files' : S.view === 'favorites' ? 'No favorites yet' : 'This folder is empty';
        $('empty-sub').textContent = searching ? 'Try a different term.' : 'Drop files anywhere, or press New → Upload files.';
    }
    if (S.layout === 'grid') { $('files-grid').classList.remove('hidden'); $('files-table').classList.add('hidden'); renderGrid(files); }
    else { $('files-grid').classList.add('hidden'); $('files-table').classList.remove('hidden'); renderTable(files); }
    updateBatchBar();
}
const thumbObserver = new IntersectionObserver(entries => {
    for (const en of entries) {
        if (!en.isIntersecting) continue;
        const img = en.target.querySelector('img');
        if (img && img.dataset.src) { img.src = img.dataset.src; delete img.dataset.src; }
        thumbObserver.unobserve(en.target);
    }
}, { rootMargin: '200px' });
function thumbHTML(f) {
    if (f.is_dir) return `<div class="fc-thumb"><span class="fc-type-icon k-dir">${ICON.folder.replace('width="15" height="15"','width="40" height="40"')}</span></div>`;
    if (isImage(f)) return `<div class="fc-thumb"><img loading="lazy" data-src="${withSession('/api/download/file?file_id=' + encodeURIComponent(f.id) + '&inline=1')}" alt=""></div>`;
    return `<div class="fc-thumb"><span class="fc-type-icon k-${kindOf(f)}">${iconFor(f).replace('width="15" height="15"','width="40" height="40"')}</span></div>`;
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
        card.className = 'file-card' + (S.selected.has(f.id) ? ' selected' : '');
        card.dataset.id = f.id;
        const st = statusOf(f);
        card.innerHTML = `
            ${thumbHTML(f)}
            <span class="fc-check${S.selected.has(f.id) ? ' on' : ''}">${S.selected.has(f.id) ? '✓' : ''}</span>
            <button class="fc-menu" aria-label="More">⋮</button>
            ${f.favorite ? '<span class="fc-fav">' + '<svg width="12" height="12" viewBox="0 0 24 24" fill="currentColor"><path d="M20.84 4.61a5.5 5.5 0 0 0-7.78 0L12 5.67l-1.06-1.06a5.5 5.5 0 0 0-7.78 7.78l1.06 1.06L12 21.23l7.78-7.78 1.06-1.06a5.5 5.5 0 0 0 0-7.78z"/></svg>' + '</span>' : ''}
            ${st !== 'ok' && st !== 'folder' ? `<span class="fc-badge badge-${st === 'processing' ? 'processing' : 'error'}">${st === 'processing' ? 'PARTIAL' : 'INCOMPLETE'}</span>` : ''}
            <div class="fc-body">
                <span class="fc-name" title="${esc(f.name)}">${esc(f.name)}</span>
                <span class="fc-meta">${f.is_dir ? fmtDateShort(f.mod_time) : fmtBytes(f.size) + ' · ' + fmtDateShort(f.mod_time)}</span>
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
        const thumb = isImage(f) ? '<img loading="lazy" data-src="' + withSession('/api/download/file?file_id=' + encodeURIComponent(f.id) + '&inline=1') + '" alt="">' : (f.is_dir ? ICON.folder : iconFor(f));
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
    imgs.forEach(img => { const o = new IntersectionObserver((en, ob) => { if (en[0].isIntersecting) { img.src = img.dataset.src; ob.disconnect(); } }); o.observe(img); });
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
    if (!f.is_dir) {
        items.push(['Preview', ICON.image, () => openPreview(f.id)]);
        items.push(['Download', ICON.folder, () => dl(f)]);
        if (!inTrash) items.push(['Copy share link', ICON.code, () => copyLink(f)]);
    }
    if (!inTrash) {
        items.push(['Rename', ICON.text, () => renameItem(f)]);
        if (!f.is_dir) items.push(['Move to…', ICON.folder, () => moveItem(f)]);
        items.push([f.favorite ? 'Unfavorite' : 'Add to favorites', ICON.image, () => favoriteItem(f, !f.favorite)]);
        items.push(['Details', ICON.text, () => openPreview(f.id, true)]);
    }
    items.push(['sep']);
    if (inTrash) items.push(['Restore', ICON.folder, () => trashRestore([f.id]), true]);
    else items.push(['Move to trash', ICON.text, () => trashMove([f.id]), true]);
    items.push(['Delete forever', ICON.other, () => deleteForever([f.id]), true]);
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
        const card = document.createElement('article'); card.className = 'file-card';
        card.innerHTML = `${thumbHTML(f)}
            <button class="fc-menu" aria-label="More">⋮</button>
            <div class="fc-body"><span class="fc-name" title="${esc(f.name)}">${esc(f.name)}</span>
            <span class="fc-meta">${f.is_dir ? 'Folder' : fmtBytes(f.size)}</span></div>`;
        const t = card.querySelector('.fc-thumb'); if (t) thumbObserver.observe(t);
        const restore = () => trashRestore([f.id]).then(loadTrash);
        card.addEventListener('click', () => {
            const menu = document.createElement('div'); menu.className = 'ctx-menu';
            const mk = (label, fn, danger) => { const b = document.createElement('button'); b.className = 'ctx-item' + (danger ? ' danger' : ''); b.textContent = label; b.onclick = () => { menu.remove(); fn(); }; menu.appendChild(b); };
            mk('Restore', restore);
            mk('Delete forever', () => deleteForever([f.id]).then(loadTrash), true);
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
        div.innerHTML = `<span class="fr-icon">${iconFor(f)}</span><span class="link-name">${esc(f.name)}</span>
            <span class="link-exp">${sh.downloads || 0} dl · ${esc(exp)}${sh.expired ? ' (expired)' : ''}</span>
            <button class="link-copy">Open file</button>
            <button class="link-revoke" data-i="${esc(sh.id)}">Revoke</button>`;
        div.querySelector('.link-copy').onclick = () => openPreview(f.id);
        div.querySelector('.link-revoke').onclick = async e => {
            await api('/api/shares/revoke', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ id: e.target.dataset.i }) });
            toast('Link revoked', 'success'); loadLinks();
        };
        panel.appendChild(div);
    }
}

// ---------- preview ----------
let previewIndex = -1, previewFiles = [], previewFor = '';
async function openPreview(id, openInfo) {
    // open first, fill second: fetching chunk health can take a second on a
    // busy host and a dead click feels broken.
    const modal = $('modal-preview');
    previewFor = id;
    previewFiles = visibleFiles().filter(x => !x.is_dir);
    previewIndex = previewFiles.findIndex(x => x.id === id);
    const known = previewFiles[previewIndex];
    $('pm-name').textContent = known ? known.name : 'Loading…';
    $('pm-size').textContent = known && !known.is_dir ? fmtBytes(known.size) : '';
    $('pm-stage').innerHTML = '<div class="pm-loading"><span class="spinner"></span>Opening…</div>';
    $('pm-details-list').innerHTML = ''; $('pm-health-list').innerHTML = ''; $('pm-shares').innerHTML = '';
    modal.classList.remove('hidden');
    if (openInfo) toggleInfo(true);
    const r = await api('/api/files/details?file_id=' + encodeURIComponent(id));
    if (previewFor !== id || modal.classList.contains('hidden')) return; // moved on already
    if (!r.ok) { modal.classList.add('hidden'); return toast(r.error || 'Not found', 'error'); }
    renderPreview(r.data);
    loadSharesInto(id);
}
function renderPreview(f) {
    $('pm-name').textContent = f.name;
    $('pm-size').textContent = fmtBytes(f.size) + ' · ' + (f.chunk_count || 0) + ' parts · ' + (f.replica_servers || 0) + ' server' + ((f.replica_servers || 0) === 1 ? '' : 's');
    $('pm-favorite').classList.toggle('faved', !!f.favorite);
    const stage = $('pm-stage'); stage.innerHTML = '';
    const k = kindOf(f);
    const media = withSession('/api/download/file?file_id=' + encodeURIComponent(f.id) + '&inline=1');
    if (k === 'image') stage.innerHTML = `<img src="${media}" alt="">`;
    else if (k === 'video') stage.innerHTML = `<video controls autoplay playsinline src="${media}"></video>`;
    else if (k === 'audio') stage.innerHTML = `<audio controls autoplay src="${media}"></audio>`;
    else if (k === 'pdf') stage.innerHTML = `<iframe src="${media}#toolbar=1"></iframe>`;
    else if (k === 'text' || k === 'code') {
        stage.innerHTML = '<pre>Loading…</pre>';
        fetch(media).then(r => r.text()).then(t => { stage.querySelector('pre').textContent = t.length > 500000 ? t.slice(0, 500000) + '\n\n… truncated — download for full file' : t; })
            .catch(() => stage.innerHTML = '<div class="no-preview"><div class="np-icon">⚠</div>Could not load text preview</div>');
    } else {
        stage.innerHTML = `<div class="no-preview"><div class="np-icon">${(ICON[k] || ICON.other).replace('width="15" height="15"','width="46" height="46"')}</div>${k === 'dir' ? 'Folder — open from the drive.' : 'No inline preview for this type'}<br><button class="btn-primary btn-sm" style="margin-top:12px" id="pm-dl2">Download file</button></div>`;
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
    if (f.chunks && f.chunks.length) {
        f.chunks.forEach(c => {
            const row = document.createElement('div'); row.className = 'health-row';
            row.innerHTML = `<span class="health-dot${c.status === 'COMPLETED' ? '' : ' missing'}"></span>
                <span class="health-part">part ${c.index + 1}</span><span class="health-size">${fmtBytes(c.size || 0)}</span>`;
            if (c.status === 'COMPLETED') row.onclick = () => {
                location.href = withSession('/api/files/raw_chunk?file_id=' + encodeURIComponent(f.id) + '&chunk_index=' + c.index);
                toast('Downloading raw encrypted part ' + (c.index + 1), 'info');
            };
            h.appendChild(row);
        });
    } else if (!f.is_dir) {
        for (let i = 0; i < (f.chunk_count || 0); i++) {
            const row = document.createElement('div'); row.className = 'health-row';
            const ok = i < (f.attachment_count || 0);
            row.innerHTML = `<span class="health-dot${ok ? '' : ' missing'}"></span><span class="health-part">part ${i + 1}</span><span class="health-size">${ok ? 'encrypted PNG stored on Discord' : 'missing'}</span>`;
            if (ok) row.onclick = () => { location.href = withSession('/api/files/raw_chunk?file_id=' + encodeURIComponent(f.id) + '&chunk_index=' + i); };
            h.appendChild(row);
        }
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
    $('pm-shares').innerHTML = '';
}
async function loadSharesInto(id) {
    const r = await api('/api/shares/list?file_id=' + encodeURIComponent(id));
    const box = $('pm-shares'); if (!box) return; box.innerHTML = '';
    const shares = (r.data && r.data.shares) || [];
    if (!shares.length) { box.innerHTML = '<div style="font-size:11.5px;color:var(--text-faint)">No active links.</div>'; return; }
    for (const sh of shares) {
        const div = document.createElement('div'); div.className = 'share-row';
        div.innerHTML = `<span>${sh.downloads || 0} dl</span><span class="share-exp">${esc(sh.expires_at ? new Date(sh.expires_at * 1000).toLocaleDateString() : '—')}</span><button class="share-rev" title="Revoke">&times;</button>`;
        div.querySelector('.share-rev').onclick = async () => { await api('/api/shares/revoke', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ id: sh.id }) }); loadSharesInto(id); };
        box.appendChild(div);
    }
}
function toggleInfo(force) {
    const p = $('pm-info-panel');
    p.classList.toggle('hidden', force === true ? false : force === false ? true : !p.classList.contains('hidden'));
}
document.addEventListener('click', e => {
    if (e.target.closest('#pm-close') || (e.target.id === 'modal-preview' && !e.target.closest('.preview-modal'))) $('modal-preview').classList.add('hidden');
    if (e.target.closest('#pm-info')) toggleInfo();
    if (e.target.closest('#pm-download')) { const f = previewFiles[previewIndex]; if (f) dl(f); }
    if (e.target.closest('#pm-share')) { const f = previewFiles[previewIndex]; if (f) copyLink(f); }
    if (e.target.closest('#pm-favorite')) { const f = previewFiles[previewIndex]; if (f) favoriteItem(f, !f.favorite).then(() => { $('pm-favorite').classList.toggle('faved', !!f.favorite); }); }
    if (e.target.closest('#pm-prev')) navPreview(-1);
    if (e.target.closest('#pm-next')) navPreview(1);
});
function navPreview(dir) {
    if (!previewFiles.length) return;
    previewIndex = (previewIndex + dir + previewFiles.length) % previewFiles.length;
    openPreview(previewFiles[previewIndex].id);
}
document.addEventListener('keydown', e => {
    if ($('modal-preview').classList.contains('hidden')) return;
    if (e.key === 'ArrowLeft') navPreview(-1);
    if (e.key === 'ArrowRight') navPreview(1);
    if (e.key === 'Escape') $('modal-preview').classList.add('hidden');
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
    const name = await showInput('New folder', '');
    if (!name) return;
    const r = await api('/api/folders/create', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name, parent_id: S.parentID }) });
    if (r.ok) { toast('Folder created', 'success'); flashFile((r.data.folder || {}).id); reloadCurrent(); } else toast(r.error, 'error');
}
async function createTextFile() {
    const name = await showInput('New text file', 'untitled.txt');
    if (!name) return;
    const content = await showInput('Content of ' + name, '');
    if (content === null) return;
    const r = await api('/api/files/create_text', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name, content, parent_id: S.parentID }) });
    if (r.ok) { toast('File created — uploading…', 'success'); } else toast(r.error, 'error');
}
async function uploadOne(file, parentId) {
    const fd = new FormData();
    fd.append('file', file); fd.append('parent_id', parentId || S.parentID);
    const tgt = $('upload-target-select').value;
    if (tgt && tgt !== 'all') fd.append('target_guild_id', tgt);
    const r = await api('/api/upload/file', { method: 'POST', body: fd });
    if (r.ok && r.data.job_id) {
        allTransfers.set(r.data.job_id, { name: file.name, type: 'UPLOAD', file_id: '', total_bytes: file.size, processed_bytes: 0, chunks_done: 0, chunks_total: Math.ceil(file.size / 7.5e6) || 1, speed: 0, eta: 0, progress: 2, status: 'ACTIVE', error: '', at: Date.now() });
        saveTransfers(); renderTransfers();
    } else toast('Upload failed: ' + r.error, 'error');
}
async function uploadFiles(files) { if (!files.length) return; await ensureParentForUpload(); for (const f of files) await uploadOne(f, S.parentID); }
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
    toast('Uploading ' + files.length + ' file' + (files.length === 1 ? '' : 's') + '…', 'info');
}
document.addEventListener('change', e => {
    if (e.target.id === 'file-input') { uploadFiles([...e.target.files]); e.target.value = ''; }
    if (e.target.id === 'dir-input') { uploadDirectory(e.target.files); e.target.value = ''; }
    if (e.target.id === 'sort-select') { const [k, d] = e.target.value.split('-'); S.sortKey = k; S.sortDir = d; render(); }
});
let dragDepth = 0;
document.addEventListener('dragenter', e => { if (e.dataTransfer && [...(e.dataTransfer.types || [])].includes('Files')) { dragDepth++; $('drop-overlay').classList.remove('hidden'); } });
document.addEventListener('dragleave', () => { if (--dragDepth <= 0) { dragDepth = 0; $('drop-overlay').classList.add('hidden'); } });
document.addEventListener('dragover', e => e.preventDefault());
document.addEventListener('drop', e => {
    if (!e.dataTransfer || ![...(e.dataTransfer.types || [])].includes('Files')) return;
    e.preventDefault(); dragDepth = 0; $('drop-overlay').classList.add('hidden');
    uploadDataTransfer(e.dataTransfer);
});

// ---------- transfers rendering ----------
function xferCard(t, id) {
    const st = t.status === 'COMPLETED' ? 'done' : t.status === 'FAILED' ? 'failed' : t.status === 'PAUSED' ? 'paused' : 'active';
    const label = { done: 'Done', failed: 'Failed', paused: 'Paused', active: (t.type === 'DOWNLOAD' ? 'Downloading' : t.type === 'DELETE' ? 'Cleaning' : 'Uploading') }[st];
    const icon = ICON.archive;
    return `<div class="xfer-card" data-id="${esc(id)}">
        <div class="xfer-top"><span class="xfer-ic">${icon}</span>
            <div style="min-width:0;flex:1"><div class="xfer-name">${esc(t.name)}</div>
            <div class="xfer-sub">${fmtBytes(t.processed_bytes || 0)} / ${fmtBytes(t.total_bytes || 0)}${st === 'active' ? ' · ' + (t.speed > 0 ? t.speed.toFixed(1) + ' MB/s · ' + Math.round(t.eta || 0) + 's left' : '') : ''}${st === 'failed' ? ' · ' + esc(t.error || 'failed') : ''}</div></div>
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
function toggleDrawer(open) { $('transfers-drawer').classList.toggle('closed', !open); }

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
async function loadServers() {
    const r = await api('/api/servers');
    S.servers = (r.data && r.data.servers) || [];
    const box = $('servers-list');
    if (!S.servers.length) { box.innerHTML = '<div class="infra-card">No servers connected. Add a bot to replicate encrypted shards across servers.</div>'; return; }
    box.innerHTML = S.servers.map(s => `<div class="server-card">
        <span class="bc-avatar" style="background:var(--green)">S</span>
        <div class="bc-main"><div class="bc-name">${esc(s.guild_name || s.guild_id)}</div><div class="bc-sub">${s.channel_count || 0} channels · shard ${s.status || 'unknown'}</div></div>
        <div class="bc-stats"><span>${fmtBytes(s.storage_bytes || 0)}</span></div></div>`).join('');
    populateTargets();
}
function populateTargets() {
    const sel = $('upload-target-select');
    const cur = sel.value;
    sel.innerHTML = '<option value="all">Every server (replicate)</option>';
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
    document.querySelectorAll('.nav-button').forEach(b => b.addEventListener('click', () => switchView(b.dataset.view)));
    $('btn-settings').addEventListener('click', () => switchView('settings'));
    $('btn-mobile-menu').addEventListener('click', () => document.body.classList.toggle('nav-open'));
    $('sidebar-scrim').addEventListener('click', () => document.body.classList.remove('nav-open'));
    $('btn-unlock').addEventListener('click', unlock);
    $('lock-password').addEventListener('keydown', e => { if (e.key === 'Enter') unlock(); });
    $('btn-new-menu').addEventListener('click', e => { e.stopPropagation(); $('new-menu').classList.toggle('hidden'); });
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
    if (S.layout === 'list') { $('btn-list').classList.add('active'); $('btn-grid').classList.remove('active'); }
    updateSortHeads();
    toggleDrawer(false);
}
function setLayout(l) {
    S.layout = l; localStorage.setItem('dfc_layout', l);
    $('btn-grid').classList.toggle('active', l === 'grid'); $('btn-list').classList.toggle('active', l === 'list');
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
    loadBots(); loadServers(); loadStatus();
    reconcileJobs();
    setInterval(loadStatus, 60000);
    logLine('drive unlocked — engine ready', 'ok');
}
(async function init() {
    wireUI();               // binds unlock/new-menu/etc once
    const ok = await checkAuth();
    if (ok) boot();         // boot() must NOT call wireUI again
})();
