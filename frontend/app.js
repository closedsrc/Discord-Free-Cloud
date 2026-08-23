let ws = null;
let currentParentID = "";
let currentFolderBreadcrumbs = [];
let allLoadedFiles = [];
let currentFilter = "all";
let activeJobID = null;

function escapeHtml(text) {
    if (!text) return "";
    return text
        .toString()
        .replace(/&/g, "&amp;")
        .replace(/</g, "&lt;")
        .replace(/>/g, "&gt;")
        .replace(/"/g, "&quot;")
        .replace(/'/g, "&#039;");
}

document.addEventListener("DOMContentLoaded", () => {
    initAuthLock();
    initNavigation();
    initDragAndDrop();
    initFileActions();
    initSettingsActions();
    initActivityLogs();
    initModals();
    initFilters();
    initBotCluster();
    initServerDashboard();
    initWebSocket();
    loadStatus();
    loadFiles("");
    loadBots();
    loadServers();
    loadSettings();
});

function initWebSocket() {
    const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
    const wsUrl = `${proto}//${window.location.host}/ws`;

    ws = new WebSocket(wsUrl);

    ws.onopen = () => {
        const statusText = document.getElementById("conn-status-text");
        if (statusText) statusText.textContent = "Connected";
        const dot = document.getElementById("status-live-dot");
        if (dot) dot.className = "status-dot online";
        addLogEntry("Connected in real time", "system");
    };

    ws.onmessage = (event) => {
        try {
            const msg = JSON.parse(event.data);
            handleWsMessage(msg);
        } catch (e) {
        }
    };

    ws.onclose = () => {
        const statusText = document.getElementById("conn-status-text");
        if (statusText) statusText.textContent = "Disconnected";
        const dot = document.getElementById("status-live-dot");
        if (dot) dot.className = "status-dot";
        setTimeout(initWebSocket, 3000);
    };
}

let pendingHighlightFileId = null;

const selectedFileIds = new Set();

function updateBatchActionBar() {
    const bar = document.getElementById("batch-action-bar");
    const countEl = document.getElementById("batch-selected-count");
    const thSelectAll = document.getElementById("th-select-all");

    if (countEl) {
        countEl.textContent = `${selectedFileIds.size} ${selectedFileIds.size === 1 ? 'item' : 'items'} selected`;
    }

    if (bar) {
        if (selectedFileIds.size > 0) {
            bar.classList.remove("hidden");
        } else {
            bar.classList.add("hidden");
        }
    }

    if (thSelectAll) {
        const rowCheckboxes = document.querySelectorAll(".file-row-check");
        if (rowCheckboxes.length > 0) {
            const allChecked = Array.from(rowCheckboxes).every(cb => cb.checked);
            thSelectAll.checked = allChecked;
        } else {
            thSelectAll.checked = false;
        }
    }
}

function formatETA(sec) {
    if (!sec || sec <= 0) return "";
    if (sec < 60) return `${sec}s left`;
    if (sec < 3600) {
        const m = Math.floor(sec / 60);
        const s = sec % 60;
        return `${m}m ${s}s left`;
    }
    const h = Math.floor(sec / 3600);
    const m = Math.floor((sec % 3600) / 60);
    return `${h}h ${m}m left`;
}

const allTransfersMap = new Map();

function recordTransfer(item) {
    if (!item || !item.job_id) return;
    const prev = allTransfersMap.get(item.job_id);
    allTransfersMap.set(item.job_id, {
        ...prev,
        ...item,
        createdAt: (prev && prev.createdAt) ? prev.createdAt : Date.now(),
        updatedAt: Date.now()
    });
    renderTransfersDrawer();
}

function renderTransfersDrawer() {
    const container = document.getElementById("transfers-list-container");
    if (!container) return;

    if (allTransfersMap.size === 0) {
        container.innerHTML = `
            <div class="empty-state-small" id="transfers-empty-state">
                <p>No active or recent transfers</p>
            </div>
        `;
        return;
    }

    const emptyState = document.getElementById("transfers-empty-state");
    if (emptyState) emptyState.remove();

    const sorted = Array.from(allTransfersMap.values()).sort((a, b) => (a.createdAt || 0) - (b.createdAt || 0));

    sorted.forEach(t => {
        const isDone = t.status === "COMPLETED";
        const isFailed = t.status === "FAILED";
        const pct = Math.min(100, Math.max(isDone ? 100 : 0, Math.round(t.progress_percent || 0)));
        const typeBadge = t.type === "DOWNLOAD" ? `<span class="transfer-card-badge download">Download</span>` : `<span class="transfer-card-badge upload">Upload</span>`;
        const statusBadge = isDone ? `<span class="transfer-card-badge done">Finished</span>` : (isFailed ? `<span class="transfer-card-badge err">Failed</span>` : `<span class="transfer-card-badge upload">${pct}%</span>`);
        const cardClass = isDone ? "completed" : (isFailed ? "failed" : "");
        const cardId = `transfer-card-${t.job_id}`;
        const etaText = formatETA(t.eta_seconds);
        const speedText = t.speed_mbs ? `${t.speed_mbs.toFixed(2)} MB/s${etaText ? ` • ${etaText}` : ''}` : `Part ${t.completed_chunks || 0}/${t.total_chunks || 0}`;

        let card = document.getElementById(cardId);
        if (!card) {
            card = document.createElement("div");
            card.className = `transfer-item-card ${cardClass}`;
            card.id = cardId;
            card.innerHTML = `
                <div class="transfer-card-header">
                    <div class="transfer-card-title-group">
                        <span class="type-badge-container">${typeBadge}</span>
                        <span class="transfer-card-name" title="${escapeHtml(t.file_name || 'File')}">${escapeHtml(t.file_name || 'File')}</span>
                    </div>
                    <span class="status-badge-container">${statusBadge}</span>
                </div>
                <div class="apple-progress-bar small">
                    <div class="apple-progress-fill ${isDone ? 'success' : 'glow'}" style="width: ${pct}%;"></div>
                </div>
                <div class="transfer-card-stats">
                    <span class="stats-bytes">${formatBytes(t.processed_bytes || 0)} of ${formatBytes(t.total_bytes || 0)}</span>
                    <span class="stats-speed">${speedText}</span>
                    <span class="stats-action">${!isDone && !isFailed ? `<button class="btn-apple-secondary btn-sm" onclick="cancelJob('${escapeHtml(t.job_id)}')">Cancel</button>` : (isDone ? '✓' : '✗')}</span>
                </div>
            `;
            container.appendChild(card);
        } else {
            card.className = `transfer-item-card ${cardClass}`;
            const fill = card.querySelector(".apple-progress-fill");
            if (fill) {
                fill.style.width = `${pct}%`;
                fill.className = `apple-progress-fill ${isDone ? 'success' : 'glow'}`;
            }
            const statusBadgeContainer = card.querySelector(".status-badge-container");
            if (statusBadgeContainer) statusBadgeContainer.innerHTML = statusBadge;

            const bytesSpan = card.querySelector(".stats-bytes");
            if (bytesSpan) bytesSpan.textContent = `${formatBytes(t.processed_bytes || 0)} of ${formatBytes(t.total_bytes || 0)}`;

            const speedSpan = card.querySelector(".stats-speed");
            if (speedSpan) speedSpan.textContent = speedText;

            const actionSpan = card.querySelector(".stats-action");
            if (actionSpan) {
                actionSpan.innerHTML = !isDone && !isFailed ? `<button class="btn-apple-secondary btn-sm" onclick="cancelJob('${escapeHtml(t.job_id)}')">Cancel</button>` : (isDone ? '✓' : '✗');
            }
        }
    });
}

window.cancelJob = async function(jobId) {
    if (!jobId) return;
    try {
        await fetch("/api/jobs/cancel", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ job_id: jobId })
        });
        showToast("Transfer paused", "info");
        addLogEntry(`Transfer paused`, "system");
        if (allTransfersMap.has(jobId)) {
            const item = allTransfersMap.get(jobId);
            item.status = "FAILED";
            renderTransfersDrawer();
        }
    } catch (e) {
    }
};

function handleWsMessage(rawMsg) {
    if (!rawMsg) return;

    if (rawMsg.type === "system_stats" && rawMsg.data) {
        updateTelemetryUI(rawMsg.data);
        return;
    }

    if (rawMsg.type === "files_changed") {
        loadFiles(currentParentID, rawMsg.file_id || pendingHighlightFileId);
        loadStatus();
        return;
    }

    const msg = (rawMsg.type === "telemetry" && rawMsg.data) ? rawMsg.data : rawMsg;

    if (msg.job_id) {
        recordTransfer(msg);
    }

    if (msg.log_message) {
        let logType = "info";
        if (msg.status === "COMPLETED") logType = "success";
        if (msg.status === "FAILED") logType = "error";
        addLogEntry(msg.log_message, logType);
    }

    const panel = document.getElementById("active-transfers-panel");
    const nameEl = document.getElementById("transfer-filename");
    const bytesEl = document.getElementById("transfer-bytes");
    const fillEl = document.getElementById("transfer-progress-fill");
    const pctEl = document.getElementById("transfer-pct");
    const chunksEl = document.getElementById("transfer-chunks");
    const speedEl = document.getElementById("hud-speed");
    const etaEl = document.getElementById("hud-eta");

    let activeList = [];
    let totalActiveBytes = 0;
    let totalProcessedBytes = 0;
    let totalSpeed = 0;
    let maxEta = 0;

    for (const t of allTransfersMap.values()) {
        if (t.status === "ACTIVE" || t.status === "STARTED") {
            activeList.push(t);
            totalActiveBytes += (t.total_bytes || 0);
            totalProcessedBytes += (t.processed_bytes || 0);
            totalSpeed += (t.speed_mbs || 0);
            if ((t.eta_seconds || 0) > maxEta) maxEta = t.eta_seconds;
        }
    }

    const activeCount = activeList.length;

    if (activeCount > 0) {
        activeJobID = activeList[0].job_id;
        if (msg.file_id) pendingHighlightFileId = msg.file_id;

        if (panel) {
            panel.classList.remove("hidden");
            panel.classList.add("hud-active");
        }

        const aggPct = totalActiveBytes > 0 
            ? Math.min(100, Math.max(3, (totalProcessedBytes / totalActiveBytes) * 100))
            : (activeList[0].progress_percent || 0);

        if (nameEl) {
            if (activeCount === 1) {
                nameEl.textContent = activeList[0].file_name || "File";
            } else {
                nameEl.textContent = `${activeCount} transfers in progress (${activeList[0].file_name})`;
            }
        }
        if (bytesEl) {
            bytesEl.textContent = `${formatBytes(totalProcessedBytes)} of ${formatBytes(totalActiveBytes)}`;
        }
        if (fillEl) {
            fillEl.style.width = `${aggPct}%`;
        }
        if (pctEl) {
            pctEl.textContent = `${Math.round(aggPct)}%`;
        }
        if (chunksEl) {
            if (activeCount === 1) {
                chunksEl.textContent = `Part ${activeList[0].completed_chunks || 0} of ${activeList[0].total_chunks || 0}`;
            } else {
                let totalCh = 0, doneCh = 0;
                activeList.forEach(t => { totalCh += (t.total_chunks || 0); doneCh += (t.completed_chunks || 0); });
                chunksEl.textContent = `Part ${doneCh} of ${totalCh}`;
            }
        }
        if (speedEl) {
            speedEl.textContent = `${totalSpeed.toFixed(2)} MB/s`;
        }
        if (etaEl) {
            const formatted = formatETA(maxEta);
            etaEl.textContent = formatted || (activeCount > 0 ? "Calculating..." : "");
        }
    } else if (msg.status === "COMPLETED" || msg.status === "FAILED") {
        const completedFileId = msg.file_id || pendingHighlightFileId;
        pendingHighlightFileId = null;
        activeJobID = null;

        if (fillEl) fillEl.style.width = msg.status === "COMPLETED" ? "100%" : "0%";
        if (pctEl) pctEl.textContent = msg.status === "COMPLETED" ? "100%" : "0%";
        if (nameEl && msg.file_name) {
            nameEl.textContent = msg.status === "COMPLETED" ? `✓ ${msg.file_name}` : `✗ ${msg.file_name}`;
        }

        setTimeout(() => {
            let stillActive = false;
            for (const t of allTransfersMap.values()) {
                if (t.status === "ACTIVE" || t.status === "STARTED") stillActive = true;
            }
            if (!stillActive && panel) {
                panel.classList.remove("hud-active");
                setTimeout(() => {
                    if (!stillActive && panel) panel.classList.add("hidden");
                }, 300);
            }
        }, 2500);

        if (msg.status === "COMPLETED" && msg.file_name) {
            showToast(`"${msg.file_name}" finished!`, "success");
        }

        loadFiles(currentParentID, completedFileId);
        loadStatus();
    }
}

function addLogEntry(text, type = "info") {
    const consoleEl = document.getElementById("activity-log-console");
    if (!consoleEl) return;

    const d = new Date();
    const timeStr = d.toLocaleTimeString([], { hour12: false });

    const entry = document.createElement("div");
    entry.className = `log-entry ${type}`;

    const timeSpan = document.createElement("span");
    timeSpan.className = "log-time";
    timeSpan.textContent = `[${timeStr}]`;

    const textSpan = document.createElement("span");
    textSpan.className = "log-text";
    textSpan.textContent = text;

    entry.appendChild(timeSpan);
    entry.appendChild(textSpan);
    consoleEl.appendChild(entry);
    consoleEl.scrollTop = consoleEl.scrollHeight;
}

function initNavigation() {
    const views = {
        "nav-explorer": "view-explorer",
        "nav-bots": "view-bots",
        "nav-servers": "view-servers",
        "nav-setup": "view-setup"
    };

    Object.keys(views).forEach(navId => {
        const btn = document.getElementById(navId);
        if (btn) {
            btn.addEventListener("click", () => {
                document.querySelectorAll(".nav-button").forEach(b => b.classList.remove("active"));
                btn.classList.add("active");

                document.querySelectorAll(".view-panel").forEach(p => p.classList.add("hidden"));
                document.getElementById(views[navId]).classList.remove("hidden");

                const titleEl = document.getElementById("page-title");
                if (navId === "nav-explorer") {
                    titleEl.textContent = "Files";
                    loadFiles(currentParentID);
                } else if (navId === "nav-bots") {
                    titleEl.textContent = "My Bots";
                    loadBots();
                } else if (navId === "nav-servers") {
                    titleEl.textContent = "My Servers";
                    loadServers();
                } else if (navId === "nav-setup") {
                    titleEl.textContent = "Settings";
                }
            });
        }
    });

    const btnNavTransfers = document.getElementById("nav-transfers");
    const transfersBackdrop = document.getElementById("transfers-drawer-backdrop");
    const btnCloseTransfers = document.getElementById("btn-close-transfers");
    const btnClearTransfers = document.getElementById("btn-clear-transfers");
    const activeHud = document.getElementById("active-transfers-panel");

    if (btnNavTransfers && transfersBackdrop) {
        btnNavTransfers.addEventListener("click", () => {
            renderTransfersDrawer();
            transfersBackdrop.classList.remove("hidden");
        });
    }

    if (activeHud && transfersBackdrop) {
        activeHud.addEventListener("click", (e) => {
            if (e.target.closest("#btn-cancel-transfer")) return;
            renderTransfersDrawer();
            transfersBackdrop.classList.remove("hidden");
        });
    }

    if (btnCloseTransfers && transfersBackdrop) {
        btnCloseTransfers.addEventListener("click", () => {
            transfersBackdrop.classList.add("hidden");
        });
    }

    if (transfersBackdrop) {
        transfersBackdrop.addEventListener("click", (e) => {
            if (e.target === transfersBackdrop) transfersBackdrop.classList.add("hidden");
        });
    }

    if (btnClearTransfers) {
        btnClearTransfers.addEventListener("click", () => {
            for (const [id, t] of allTransfersMap.entries()) {
                if (t.status === "COMPLETED" || t.status === "FAILED") {
                    allTransfersMap.delete(id);
                }
            }
            renderTransfersDrawer();
        });
    }

    const cancelBtn = document.getElementById("btn-cancel-transfer");
    if (cancelBtn) {
        cancelBtn.addEventListener("click", async () => {
            if (activeJobID) {
                await fetch("/api/jobs/cancel", {
                    method: "POST",
                    headers: { "Content-Type": "application/json" },
                    body: JSON.stringify({ job_id: activeJobID })
                });
                showToast("Transfer paused", "info");
                addLogEntry("Transfer paused by user", "system");
            }
        });
    }

    const openDiag = document.getElementById("btn-open-diagnostics");
    const closeDiag = document.getElementById("btn-close-diagnostics");
    const diagBackdrop = document.getElementById("diagnostics-drawer-backdrop");

    if (openDiag && diagBackdrop) {
        openDiag.addEventListener("click", () => diagBackdrop.classList.remove("hidden"));
    }
    if (closeDiag && diagBackdrop) {
        closeDiag.addEventListener("click", () => diagBackdrop.classList.add("hidden"));
    }
    if (diagBackdrop) {
        diagBackdrop.addEventListener("click", (e) => {
            if (e.target === diagBackdrop) diagBackdrop.classList.add("hidden");
        });
    }

    const closeInspector = document.getElementById("btn-close-inspector");
    if (closeInspector) {
        closeInspector.addEventListener("click", () => {
            const insp = document.getElementById("server-channel-inspector");
            if (insp) insp.classList.add("hidden");
        });
    }
}

function initFilters() {
    const searchInput = document.getElementById("file-search-input");
    if (searchInput) {
        searchInput.addEventListener("input", () => {
            applyCurrentFilter();
        });
    }

    const pills = document.querySelectorAll(".segment-pill");
    pills.forEach(pill => {
        pill.addEventListener("click", () => {
            pills.forEach(p => p.classList.remove("active"));
            pill.classList.add("active");
            currentFilter = pill.getAttribute("data-filter") || "all";
            applyCurrentFilter();
        });
    });
}

function applyCurrentFilter() {
    const searchInput = document.getElementById("file-search-input");
    const query = searchInput ? searchInput.value.toLowerCase().trim() : "";

    let filtered = allLoadedFiles.filter(item => {
        const matchesQuery = !query || item.name.toLowerCase().includes(query);
        if (!matchesQuery) return false;

        if (currentFilter === "all") return true;
        if (currentFilter === "folders") return item.is_dir;
        if (item.is_dir) return false;

        const ext = item.name.split('.').pop().toLowerCase();
        if (currentFilter === "media") {
            return ['mp4', 'mkv', 'avi', 'mov', 'webm', 'mp3', 'wav', 'flac', 'aac', 'ogg', 'jpg', 'jpeg', 'png', 'gif', 'webp', 'svg'].includes(ext);
        }
        if (currentFilter === "docs") {
            return ['pdf', 'doc', 'docx', 'txt', 'md', 'xlsx', 'csv', 'json', 'js', 'py', 'go', 'rs', 'html', 'css', 'c', 'cpp', 'sh'].includes(ext);
        }
        if (currentFilter === "archives") {
            return ['zip', 'rar', '7z', 'tar', 'gz', 'iso'].includes(ext);
        }
        return true;
    });

    renderFileTable(filtered);
}

function initDragAndDrop() {
    const overlay = document.getElementById("drop-overlay");
    let dragCounter = 0;

    window.addEventListener("dragenter", (e) => {
        e.preventDefault();
        dragCounter++;
        if (overlay) overlay.classList.remove("hidden");
    });

    window.addEventListener("dragleave", (e) => {
        e.preventDefault();
        dragCounter--;
        if (dragCounter <= 0) {
            dragCounter = 0;
            if (overlay) overlay.classList.add("hidden");
        }
    });

    window.addEventListener("dragover", (e) => {
        e.preventDefault();
    });

    window.addEventListener("drop", async (e) => {
        e.preventDefault();
        dragCounter = 0;
        if (overlay) overlay.classList.add("hidden");

        const items = e.dataTransfer.items;
        if (items && items.length > 0) {
            for (let i = 0; i < items.length; i++) {
                const item = items[i].webkitGetAsEntry();
                if (item) {
                    await uploadFileSystemEntry(item, currentParentID);
                }
            }
            return;
        }

        const files = e.dataTransfer.files;
        if (files && files.length > 0) {
            for (let i = 0; i < files.length; i++) {
                await uploadSingleFile(files[i], currentParentID);
            }
        }
    });
}

async function uploadFileSystemEntry(entry, parentId) {
    if (entry.isFile) {
        entry.file(async (file) => {
            await uploadSingleFile(file, parentId);
        });
    } else if (entry.isDirectory) {
        const folderName = entry.name;
        try {
            const res = await fetch("/api/folders/create", {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({ name: folderName, parent_id: parentId })
            });
            const data = await res.json();
            if (res.ok && (data.ok || data.id || data.ID)) {
                const newFolderId = (data.folder && data.folder.id) || data.id || data.ID;
                const dirReader = entry.createReader();
                dirReader.readEntries(async (entries) => {
                    for (const child of entries) {
                        await uploadFileSystemEntry(child, newFolderId);
                    }
                });
            }
        } catch (e) {
            showToast(`Could not make folder: ${e.message}`, "error");
        }
    }
}

async function uploadSingleFile(file, parentId) {
    const formData = new FormData();
    formData.append("file", file);
    formData.append("parent_id", parentId || "");

    const targetSelect = document.getElementById("upload-target-select");
    const targetGuild = targetSelect ? targetSelect.value : "all";
    if (targetGuild && targetGuild !== "all") {
        formData.append("target_guild_id", targetGuild);
    }

    showToast(`Uploading ${file.name}...`, "info");
    addLogEntry(`Started uploading ${file.name} (${formatBytes(file.size)})`, "info");

    const panel = document.getElementById("active-transfers-panel");
    const nameEl = document.getElementById("transfer-filename");
    const bytesEl = document.getElementById("transfer-bytes");
    const fillEl = document.getElementById("transfer-progress-fill");
    const pctEl = document.getElementById("transfer-pct");
    const chunksEl = document.getElementById("transfer-chunks");
    const speedEl = document.getElementById("hud-speed");
    const etaEl = document.getElementById("hud-eta");

    if (panel) {
        panel.classList.remove("hidden");
        panel.classList.add("hud-active");
    }
    if (nameEl) nameEl.textContent = file.name;
    if (bytesEl) bytesEl.textContent = `0 B of ${formatBytes(file.size)}`;
    if (fillEl) fillEl.style.width = "4%";
    if (pctEl) pctEl.textContent = "0%";
    if (chunksEl) chunksEl.textContent = "Preparing parts...";
    if (speedEl) speedEl.textContent = "-- MB/s";
    if (etaEl) etaEl.textContent = "--";

    try {
        const res = await fetch("/api/upload/file", {
            method: "POST",
            body: formData
        });
        const data = await res.json();
        if (res.ok && data.job_id) {
            activeJobID = data.job_id;
            if (data.file_id) pendingHighlightFileId = data.file_id;
        } else {
            showToast(`Upload failed: ${data.error || 'Server error'}`, "error");
            addLogEntry(`Upload stopped: ${data.error || 'Server error'}`, "error");
            if (panel) panel.classList.remove("hud-active");
        }
    } catch (e) {
        showToast(`Upload error: ${e.message}`, "error");
        addLogEntry(`Upload connection error: ${e.message}`, "error");
        if (panel) panel.classList.remove("hud-active");
    }
}

function initFileActions() {
    const btnUpload = document.getElementById("btn-upload-file");
    const fileInput = document.getElementById("hidden-file-input");

    if (btnUpload && fileInput) {
        btnUpload.addEventListener("click", () => fileInput.click());
        fileInput.addEventListener("change", async (e) => {
            if (e.target.files && e.target.files.length > 0) {
                for (let i = 0; i < e.target.files.length; i++) {
                    await uploadSingleFile(e.target.files[i], currentParentID);
                }
                fileInput.value = "";
            }
        });
    }

    const thSelectAll = document.getElementById("th-select-all");
    if (thSelectAll) {
        thSelectAll.addEventListener("change", () => {
            const rowCheckboxes = document.querySelectorAll(".file-row-check");
            rowCheckboxes.forEach(cb => {
                const id = cb.dataset.id;
                cb.checked = thSelectAll.checked;
                if (id) {
                    if (thSelectAll.checked) {
                        selectedFileIds.add(id);
                    } else {
                        selectedFileIds.delete(id);
                    }
                }
            });
            updateBatchActionBar();
        });
    }

    const btnBatchClear = document.getElementById("btn-batch-clear");
    if (btnBatchClear) {
        btnBatchClear.addEventListener("click", () => {
            selectedFileIds.clear();
            document.querySelectorAll(".file-row-check").forEach(cb => cb.checked = false);
            if (thSelectAll) thSelectAll.checked = false;
            updateBatchActionBar();
        });
    }

    const btnBatchDelete = document.getElementById("btn-batch-delete");
    if (btnBatchDelete) {
        btnBatchDelete.addEventListener("click", async () => {
            if (selectedFileIds.size === 0) return;
            const count = selectedFileIds.size;
            if (!confirm(`Delete ${count} selected item${count === 1 ? '' : 's'}?`)) return;

            try {
                const res = await fetch("/api/delete", {
                    method: "POST",
                    headers: { "Content-Type": "application/json" },
                    body: JSON.stringify({ file_ids: Array.from(selectedFileIds) })
                });
                const data = await res.json();
                if (res.ok && data.ok) {
                    showToast(`Deleted ${count} item${count === 1 ? '' : 's'}`, "success");
                    selectedFileIds.clear();
                    updateBatchActionBar();
                    await loadFiles(currentParentID);
                    loadStatus();
                } else {
                    showToast(`Could not delete: ${data.error || 'Server error'}`, "error");
                }
            } catch (err) {
                showToast(`Delete error: ${err.message}`, "error");
            }
        });
    }

    const btnCancelAll = document.getElementById("btn-cancel-all-transfers");
    if (btnCancelAll) {
        btnCancelAll.addEventListener("click", async () => {
            try {
                await fetch("/api/jobs/cancel", {
                    method: "POST",
                    headers: { "Content-Type": "application/json" },
                    body: JSON.stringify({ job_id: "all" })
                });
                showToast("All transfers stopped", "info");
                addLogEntry("All transfers stopped by user", "system");
                for (const t of allTransfersMap.values()) {
                    if (t.status === "ACTIVE" || t.status === "STARTED") {
                        t.status = "FAILED";
                    }
                }
                renderTransfersDrawer();
            } catch (err) {
            }
        });
    }

    const btnBackup = document.getElementById("btn-sync-catalog");
    if (btnBackup) {
        btnBackup.addEventListener("click", async () => {
            showToast("Saving cloud backup...", "info");
            addLogEntry("Creating backup copy on Discord...", "info");
            try {
                const res = await fetch("/api/catalog/sync", { method: "POST" });
                const data = await res.json();
                if (res.ok && data.ok) {
                    showToast("Cloud backup saved successfully", "success");
                    addLogEntry("Backup saved to Discord safely", "success");
                } else {
                    showToast(`Backup error: ${data.error || 'Check Discord setup'}`, "error");
                    addLogEntry(`Backup stopped: ${data.error || 'Check Discord setup'}`, "error");
                }
            } catch (e) {
                showToast(`Backup error: ${e.message}`, "error");
            }
        });
    }
}

function initModals() {
    const btnFolder = document.getElementById("btn-create-folder");
    const folderModal = document.getElementById("modal-folder");
    const folderInput = document.getElementById("modal-folder-input");
    const btnCancelFolder = document.getElementById("btn-cancel-folder-modal");
    const btnCloseFolder = document.getElementById("btn-close-folder-modal");
    const btnConfirmFolder = document.getElementById("btn-confirm-create-folder");

    if (btnFolder && folderModal) {
        btnFolder.addEventListener("click", () => {
            if (folderInput) folderInput.value = "";
            folderModal.classList.remove("hidden");
            if (folderInput) folderInput.focus();
        });
    }

    const hideFolderModal = () => {
        if (folderModal) folderModal.classList.add("hidden");
    };

    if (btnCancelFolder) btnCancelFolder.addEventListener("click", hideFolderModal);
    if (btnCloseFolder) btnCloseFolder.addEventListener("click", hideFolderModal);

    if (btnConfirmFolder && folderInput) {
        btnConfirmFolder.addEventListener("click", async () => {
            const name = folderInput.value.trim();
            if (!name) {
                showToast("Please enter a folder name", "error");
                return;
            }

            btnConfirmFolder.disabled = true;
            try {
                const res = await fetch("/api/folders/create", {
                    method: "POST",
                    headers: { "Content-Type": "application/json" },
                    body: JSON.stringify({ name, parent_id: currentParentID })
                });
                const data = await res.json();
                if (res.ok && (data.ok || data.id || data.ID)) {
                    showToast(`Folder "${name}" created`, "success");
                    hideFolderModal();
                    loadFiles(currentParentID);
                } else {
                    showToast(`Could not make folder: ${data.error || 'Server error'}`, "error");
                }
            } catch (e) {
                showToast(`Error: ${e.message}`, "error");
            } finally {
                btnConfirmFolder.disabled = false;
            }
        });
    }

    const btnCreateFile = document.getElementById("btn-create-file");
    const createFileModal = document.getElementById("modal-create-file");
    const fileNameInput = document.getElementById("create-file-name-input");
    const fileContentInput = document.getElementById("create-file-content-input");
    const btnCancelCreateFile = document.getElementById("btn-cancel-create-file-modal");
    const btnCloseCreateFile = document.getElementById("btn-close-create-file-modal");
    const btnConfirmCreateFile = document.getElementById("btn-confirm-create-file");

    if (btnCreateFile && createFileModal) {
        btnCreateFile.addEventListener("click", () => {
            if (fileNameInput) fileNameInput.value = "";
            if (fileContentInput) fileContentInput.value = "";
            createFileModal.classList.remove("hidden");
            if (fileNameInput) fileNameInput.focus();
        });
    }

    const hideCreateFileModal = () => {
        if (createFileModal) createFileModal.classList.add("hidden");
    };

    if (btnCancelCreateFile) btnCancelCreateFile.addEventListener("click", hideCreateFileModal);
    if (btnCloseCreateFile) btnCloseCreateFile.addEventListener("click", hideCreateFileModal);

    if (btnConfirmCreateFile && fileNameInput && fileContentInput) {
        btnConfirmCreateFile.addEventListener("click", async () => {
            const filename = fileNameInput.value.trim();
            const content = fileContentInput.value;

            if (!filename) {
                showToast("Please enter a file name", "error");
                return;
            }

            btnConfirmCreateFile.disabled = true;
            showToast(`Saving ${filename}...`, "info");

            try {
                const res = await fetch("/api/files/create_text", {
                    method: "POST",
                    headers: { "Content-Type": "application/json" },
                    body: JSON.stringify({
                        name: filename,
                        filename: filename,
                        content: content,
                        parent_id: currentParentID
                    })
                });
                const data = await res.json();
                if (res.ok && data.ok) {
                    showToast(`File ${filename} saved to cloud`, "success");
                    addLogEntry(`Created file ${filename}`, "success");
                    hideCreateFileModal();
                    loadFiles(currentParentID);
                } else {
                    showToast(`Could not create file: ${data.error || 'Server error'}`, "error");
                }
            } catch (e) {
                showToast(`Error: ${e.message}`, "error");
            } finally {
                btnConfirmCreateFile.disabled = false;
            }
        });
    }

    const previewModal = document.getElementById("modal-preview");
    const btnClosePreview = document.getElementById("btn-close-preview-modal");

    const stopAndClosePreview = () => {
        if (previewModal) previewModal.classList.add("hidden");
        const container = document.getElementById("preview-container");
        if (container) {
            const media = container.querySelector("video, audio");
            if (media) {
                try {
                    media.pause();
                    media.removeAttribute("src");
                    media.load();
                } catch (e) {}
            }
            container.innerHTML = "";
        }
    };

    if (btnClosePreview && previewModal) {
        btnClosePreview.addEventListener("click", stopAndClosePreview);
    }
    if (previewModal) {
        previewModal.addEventListener("click", (e) => {
            if (e.target === previewModal) stopAndClosePreview();
        });
    }

    document.addEventListener("keydown", (e) => {
        if (e.key === "Escape" && previewModal && !previewModal.classList.contains("hidden")) {
            stopAndClosePreview();
        }
    });
}

function initActivityLogs() {
    const clearBtn = document.getElementById("btn-clear-logs");
    if (clearBtn) {
        clearBtn.addEventListener("click", () => {
            const consoleEl = document.getElementById("activity-log-console");
            if (consoleEl) {
                consoleEl.innerHTML = "";
                addLogEntry("Logs cleared", "system");
            }
        });
    }
}

async function loadFiles(parentId = "", highlightFileId = null) {
    currentParentID = parentId;
    updateBreadcrumbUI();

    if (highlightFileId) {
        pendingHighlightFileId = highlightFileId;
    }

    try {
        const res = await fetch(`/api/files?parent_id=${encodeURIComponent(parentId)}`);
        if (res.ok) {
            allLoadedFiles = await res.json();
            applyCurrentFilter();

            if (pendingHighlightFileId) {
                const targetId = pendingHighlightFileId;
                pendingHighlightFileId = null;
                setTimeout(() => {
                    const row = document.querySelector(`tr[data-file-id="${targetId}"]`);
                    if (row) {
                        row.classList.add("row-highlight-pulse");
                        row.scrollIntoView({ behavior: "smooth", block: "nearest" });
                        setTimeout(() => row.classList.remove("row-highlight-pulse"), 3500);
                    }
                }, 100);
            }
        }
    } catch (e) {
        addLogEntry(`Failed to fetch file list: ${e.message}`, "error");
    }
}

function renderFileTable(files) {
    const tbody = document.getElementById("file-table-body");
    const emptyState = document.getElementById("empty-state");
    const countLabel = document.getElementById("filter-count-label");

    if (countLabel) {
        countLabel.textContent = `${files.length} item${files.length === 1 ? '' : 's'}`;
    }

    if (!tbody) return;
    tbody.innerHTML = "";

    if (!files || files.length === 0) {
        if (emptyState) emptyState.classList.remove("hidden");
        return;
    }

    if (emptyState) emptyState.classList.add("hidden");

    files.forEach((item, idx) => {
        const tr = document.createElement("tr");
        tr.setAttribute("data-file-id", item.id);
        tr.className = "file-row-animated";
        tr.style.animationDelay = `${Math.min(idx * 0.03, 0.3)}s`;

        const tdCheck = document.createElement("td");
        tdCheck.className = "col-select";
        tdCheck.style.textAlign = "center";

        const checkInput = document.createElement("input");
        checkInput.type = "checkbox";
        checkInput.className = "custom-checkbox file-row-check";
        checkInput.dataset.id = item.id;
        checkInput.checked = selectedFileIds.has(item.id);
        checkInput.addEventListener("click", (e) => {
            e.stopPropagation();
        });
        checkInput.addEventListener("change", () => {
            if (checkInput.checked) {
                selectedFileIds.add(item.id);
            } else {
                selectedFileIds.delete(item.id);
            }
            updateBatchActionBar();
        });
        tdCheck.appendChild(checkInput);
        tr.appendChild(tdCheck);

        const tdName = document.createElement("td");
        tdName.className = "col-name";

        const rowMain = document.createElement("div");
        rowMain.className = "file-row-main";

        const iconSpan = document.createElement("span");
        iconSpan.className = "file-type-icon";
        iconSpan.innerHTML = getFileIconSvg(item.name, item.is_dir);

        const nameSpan = document.createElement("span");
        nameSpan.textContent = item.name;

        rowMain.appendChild(iconSpan);
        rowMain.appendChild(nameSpan);
        tdName.appendChild(rowMain);

        if (item.is_dir) {
            rowMain.style.cursor = "pointer";
            rowMain.addEventListener("click", (e) => {
                e.stopPropagation();
                navigateToFolder(item.id, item.name);
            });
        }

        const tdSize = document.createElement("td");
        tdSize.className = "col-size";
        tdSize.textContent = item.is_dir ? "--" : formatBytes(item.size);

        const tdStatus = document.createElement("td");
        tdStatus.className = "col-status";
        tdStatus.innerHTML = `<span class="badge-green">Saved</span>`;

        const tdDate = document.createElement("td");
        tdDate.className = "col-date";
        const dateObj = new Date(item.mod_time * 1000);
        tdDate.textContent = dateObj.toLocaleDateString([], { month: 'short', day: 'numeric', year: 'numeric' });

        const tdActions = document.createElement("td");
        tdActions.className = "col-actions";

        const actionsGroup = document.createElement("div");
        actionsGroup.className = "row-actions-group";

        if (!item.is_dir) {
            const btnPreview = document.createElement("button");
            btnPreview.className = "btn-row-action preview";
            btnPreview.textContent = "Preview";
            btnPreview.addEventListener("click", (e) => {
                e.stopPropagation();
                openFilePreview(item);
            });
            actionsGroup.appendChild(btnPreview);

            const btnDownload = document.createElement("button");
            btnDownload.className = "btn-row-action";
            btnDownload.textContent = "Download";
            btnDownload.addEventListener("click", (e) => {
                e.stopPropagation();
                window.location.href = `/api/download?file_id=${encodeURIComponent(item.id)}`;
            });
            actionsGroup.appendChild(btnDownload);
        }

        const btnDelete = document.createElement("button");
        btnDelete.className = "btn-row-action delete";
        btnDelete.textContent = "Delete";
        btnDelete.addEventListener("click", async (e) => {
            e.stopPropagation();
            if (!confirm(`Delete ${item.name}?`)) return;

            try {
                const delRes = await fetch("/api/delete", {
                    method: "POST",
                    headers: { "Content-Type": "application/json" },
                    body: JSON.stringify({ file_id: item.id })
                });
                const data = await delRes.json();
                if (delRes.ok && data.ok) {
                    showToast(`Deleted ${item.name}`, "success");
                    selectedFileIds.delete(item.id);
                    updateBatchActionBar();
                    await loadFiles(currentParentID);
                    loadStatus();
                } else {
                    showToast(`Could not delete item: ${data.error || 'Server error'}`, "error");
                }
            } catch (err) {
                showToast(`Delete error: ${err.message}`, "error");
            }
        });
        actionsGroup.appendChild(btnDelete);

        tdActions.appendChild(actionsGroup);

        tr.appendChild(tdName);
        tr.appendChild(tdSize);
        tr.appendChild(tdStatus);
        tr.appendChild(tdDate);
        tr.appendChild(tdActions);

        tbody.appendChild(tr);
    });

    updateBatchActionBar();
}

function navigateToFolder(folderId, folderName) {
    if (!folderId) {
        currentFolderBreadcrumbs = [];
        loadFiles("");
        return;
    }
    if (folderId === currentParentID) return;
    const existingIdx = currentFolderBreadcrumbs.findIndex(c => c.id === folderId);
    if (existingIdx >= 0) {
        currentFolderBreadcrumbs = currentFolderBreadcrumbs.slice(0, existingIdx + 1);
    } else {
        currentFolderBreadcrumbs.push({ id: folderId, name: folderName || "Folder" });
    }
    loadFiles(folderId);
}

function updateBreadcrumbUI() {
    const bar = document.getElementById("breadcrumb-bar");
    if (!bar) return;
    bar.innerHTML = "";

    const rootSpan = document.createElement("span");
    rootSpan.className = "breadcrumb-link";
    rootSpan.style.cursor = "pointer";
    rootSpan.textContent = "My Drive";
    rootSpan.addEventListener("click", () => {
        navigateToFolder("", "My Drive");
    });
    bar.appendChild(rootSpan);

    currentFolderBreadcrumbs.forEach((crumb, idx) => {
        const sep = document.createElement("span");
        sep.textContent = " / ";
        sep.style.margin = "0 4px";
        sep.style.color = "var(--text-tertiary)";
        bar.appendChild(sep);

        const crumbSpan = document.createElement("span");
        crumbSpan.className = "breadcrumb-link";
        crumbSpan.textContent = crumb.name;
        crumbSpan.style.cursor = "pointer";
        crumbSpan.addEventListener("click", () => {
            navigateToFolder(crumb.id, crumb.name);
        });
        bar.appendChild(crumbSpan);
    });
}

async function openFilePreview(file) {
    const modal = document.getElementById("modal-preview");
    const title = document.getElementById("preview-filename");
    const icon = document.getElementById("preview-icon");
    const container = document.getElementById("preview-container");
    const dlBtn = document.getElementById("btn-preview-download");

    if (!modal || !container) return;

    if (title) title.textContent = file.name;
    if (icon) icon.innerHTML = getFileIconSvg(file.name, false);
    if (dlBtn) {
        dlBtn.onclick = () => {
            window.location.href = `/api/download?file_id=${encodeURIComponent(file.id)}`;
        };
    }

    container.innerHTML = `<div class="preview-loading">Loading preview...</div>`;
    modal.classList.remove("hidden");

    const streamUrl = `/api/download/file?file_id=${encodeURIComponent(file.id)}`;
    const ext = file.name.split('.').pop().toLowerCase();

    if (['mp4', 'webm', 'mov', 'mkv'].includes(ext)) {
        container.innerHTML = `<video class="preview-media-player" controls autoplay preload="auto" playsinline src="${streamUrl}"></video>`;
    } else if (['mp3', 'wav', 'ogg', 'aac', 'flac'].includes(ext)) {
        container.innerHTML = `<audio class="preview-media-player" controls autoplay preload="auto" src="${streamUrl}"></audio>`;
    } else if (['jpg', 'jpeg', 'png', 'gif', 'webp', 'svg'].includes(ext)) {
        container.innerHTML = `<img class="preview-image-view" src="${streamUrl}" alt="${escapeHtml(file.name)}">`;
    } else if (ext === 'pdf') {
        container.innerHTML = `<iframe class="preview-frame" src="${streamUrl}"></iframe>`;
    } else if (['txt', 'md', 'json', 'js', 'py', 'go', 'html', 'css', 'c', 'cpp', 'sh', 'rs', 'yaml', 'yml', 'xml', 'log', 'csv'].includes(ext) || file.size < 500000) {
        try {
            const res = await fetch(streamUrl);
            const text = await res.text();
            container.innerHTML = `<div class="preview-code-box"><pre>${escapeHtml(text)}</pre></div>`;
        } catch (e) {
            container.innerHTML = `<div class="preview-loading">Could not load text preview</div>`;
        }
    } else {
        container.innerHTML = `
            <div style="text-align: center; color: var(--text-secondary);">
                <div>Preview not available for this file type</div>
                <div style="margin-top: 6px; font-size: 11px;">Click Download to save the file to your computer</div>
            </div>
        `;
    }
}

function updateTelemetryUI(data) {
    if (!data) return;

    const totalSizeEl = document.getElementById("total-storage-size");
    const totalFilesEl = document.getElementById("total-files-count");
    if (totalSizeEl && data.total_storage_bytes !== undefined) {
        totalSizeEl.textContent = formatBytes(data.total_storage_bytes);
    }
    if (totalFilesEl) {
        const count = data.total_files !== undefined ? data.total_files : data.files_count;
        if (count !== undefined) {
            totalFilesEl.textContent = `${count} ${count === 1 ? 'file' : 'files'}`;
        }
    }

    const disk = data.disk_space;
    if (disk) {
        const driveNameEl = document.getElementById("disk-drive-name");
        const freeValEl = document.getElementById("disk-free-val");
        const usedValEl = document.getElementById("disk-used-val");
        const totalValEl = document.getElementById("disk-total-val");
        const fillEl = document.getElementById("disk-usage-fill");

        if (driveNameEl && disk.drive) {
            driveNameEl.textContent = `PC Storage (${disk.drive})`;
        }
        if (freeValEl && disk.free_bytes !== undefined) {
            freeValEl.textContent = `${formatBytes(disk.free_bytes)} free`;
        }
        if (usedValEl && disk.used_bytes !== undefined) {
            usedValEl.textContent = `${formatBytes(disk.used_bytes)} used`;
        }
        if (totalValEl && disk.total_bytes !== undefined) {
            totalValEl.textContent = `${formatBytes(disk.total_bytes)} total`;
        }
        if (fillEl && disk.used_percent !== undefined) {
            const pct = Math.min(100, Math.max(0, disk.used_percent));
            fillEl.style.width = `${pct.toFixed(1)}%`;
            if (pct >= 90) {
                fillEl.style.background = "var(--accent-red, #ff453a)";
            } else if (pct >= 80) {
                fillEl.style.background = "var(--accent-amber, #ff9f0a)";
            } else {
                fillEl.style.background = "var(--accent-blue, #007aff)";
            }
        }
    }
}

async function loadStatus() {
    try {
        const res = await fetch("/api/status");
        if (res.ok) {
            const data = await res.json();
            updateTelemetryUI(data);
        }
    } catch (e) {
    }
}

function initBotCluster() {
    const btnAdd = document.getElementById("btn-add-cluster-bot");
    if (btnAdd) {
        btnAdd.addEventListener("click", async () => {
            const tokenInput = document.getElementById("new-bot-token");
            const guildInput = document.getElementById("new-bot-guild");
            if (!tokenInput || !guildInput) return;

            const token = tokenInput.value.trim();
            const guildID = guildInput.value.trim();

            if (!token || !guildID) {
                showToast("Please enter bot token and server ID", "error");
                return;
            }

            btnAdd.disabled = true;
            showToast("Connecting bot...", "info");
            addLogEntry(`Checking bot connection on server ${guildID}...`, "info");

            try {
                const res = await fetch("/api/bots/add", {
                    method: "POST",
                    headers: { "Content-Type": "application/json" },
                    body: JSON.stringify({ bot_token: token, guild_id: guildID })
                });
                const data = await res.json();
                if (res.ok && data.ok) {
                    showToast(`Connected bot ${data.node.bot_name || 'Bot'} successfully`, "success");
                    addLogEntry(`Bot ${data.node.bot_name || 'Bot'} linked to server ${data.node.guild_name || guildID}`, "success");
                    tokenInput.value = "";
                    guildInput.value = "";
                    loadBots();
                    loadServers();
                } else {
                    const errMsg = data.error || "Could not connect bot";
                    showToast(errMsg, "error");
                    addLogEntry(`Bot connection stopped: ${errMsg}`, "error");
                }
            } catch (e) {
                showToast(`Connection error: ${e.message}`, "error");
            } finally {
                btnAdd.disabled = false;
            }
        });
    }
}

async function loadBots() {
    try {
        const res = await fetch("/api/bots");
        if (res.ok) {
            const data = await res.json();
            const grid = document.getElementById("bots-cluster-grid");
            if (!grid) return;
            grid.innerHTML = "";

            if (data.nodes && data.nodes.length > 0) {
                data.nodes.forEach(node => {
                    const card = document.createElement("div");
                    card.className = "shard-pill-card";
                    card.innerHTML = `
                        <div class="shard-card-top">
                            <span class="shard-title">${escapeHtml(node.bot_name || 'Discord Bot')}</span>
                            <span class="badge-green">${escapeHtml(node.status || 'Active')}</span>
                        </div>
                        <div class="shard-detail" style="margin-bottom: 5px;">Server: ${escapeHtml(node.guild_name || node.guild_id)}</div>
                        <div class="shard-detail" style="margin-bottom: 10px;">Ping: ${node.ping_ms || 50}ms | Channels: ${node.channel_count || 4}</div>
                    `;

                    const actionsRow = document.createElement("div");
                    actionsRow.className = "row-actions-group";

                    const btnDelete = document.createElement("button");
                    btnDelete.className = "btn-row-action delete";
                    btnDelete.textContent = "Remove";
                    btnDelete.addEventListener("click", async () => {
                        if (!confirm(`Remove bot ${node.bot_name || 'this bot'}?`)) return;
                        try {
                            const delRes = await fetch("/api/bots/delete", {
                                method: "POST",
                                headers: { "Content-Type": "application/json" },
                                body: JSON.stringify({ id: node.id })
                            });
                            if (delRes.ok) {
                                showToast("Bot removed successfully", "success");
                                loadBots();
                                loadServers();
                            }
                        } catch (e) {
                            showToast(`Error: ${e.message}`, "error");
                        }
                    });

                    actionsRow.appendChild(btnDelete);
                    card.appendChild(actionsRow);
                    grid.appendChild(card);
                });
            } else {
                const emptyCard = document.createElement("div");
                emptyCard.className = "shard-pill-card";
                emptyCard.innerHTML = `
                    <div class="shard-card-top">
                        <span class="shard-title">No Bots Connected</span>
                        <span class="badge-green">Add First Bot</span>
                    </div>
                    <div class="shard-detail">Paste a Bot Token and Server ID above to connect your first bot</div>
                `;
                grid.appendChild(emptyCard);
            }
            populateCleanServersChecklist();
        }
    } catch (e) {
    }
}

let allConnectedServers = [];

async function loadServers() {
    try {
        const res = await fetch("/api/servers");
        if (res.ok) {
            const data = await res.json();
            allConnectedServers = data.servers || [];
            renderServers(allConnectedServers);
            updateTargetServerDropdown(allConnectedServers);
        }
    } catch (e) {
        console.error("loadServers error:", e);
    }
}

function updateTargetServerDropdown(servers) {
    const targetSelect = document.getElementById("upload-target-select");
    if (!targetSelect) return;

    const currentVal = targetSelect.value;
    targetSelect.innerHTML = `<option value="all">🌐 Every Server (All Bots)</option>`;

    if (servers && servers.length > 0) {
        servers.forEach(srv => {
            const opt = document.createElement("option");
            opt.value = srv.guild_id;
            opt.textContent = `🖥️ ${srv.guild_name || 'Server'} (${srv.bot_name || 'Bot'})`;
            targetSelect.appendChild(opt);
        });
    }

    if (currentVal && Array.from(targetSelect.options).some(o => o.value === currentVal)) {
        targetSelect.value = currentVal;
    }
}

function renderServers(servers) {
    const grid = document.getElementById("servers-grid");
    if (!grid) return;
    grid.innerHTML = "";

    const statOnline = document.getElementById("stat-servers-online");
    const statAvgPing = document.getElementById("stat-servers-avg-ping");
    const statCount = document.getElementById("stat-servers-count");
    const statShards = document.getElementById("stat-servers-shards");

    const onlineCount = servers.filter(s => s.status === "Active" || s.status === "Online").length;
    if (statOnline) statOnline.textContent = `${onlineCount} / ${servers.length} Online`;
    if (statCount) statCount.textContent = `${servers.length} Connected`;

    let totalPing = 0;
    let pingCount = 0;
    let totalChannels = 0;

    servers.forEach(s => {
        if (s.ping_ms > 0 && s.ping_ms < 999) {
            totalPing += s.ping_ms;
            pingCount++;
        }
        totalChannels += (s.channel_count || 4);
    });

    const avgPing = pingCount > 0 ? Math.round(totalPing / pingCount) : 0;
    if (statAvgPing) statAvgPing.textContent = avgPing > 0 ? `${avgPing} ms` : `-- ms`;
    if (statShards) statShards.textContent = `${totalChannels} Active`;

    if (servers.length === 0) {
        const emptyCard = document.createElement("div");
        emptyCard.className = "shard-pill-card";
        emptyCard.innerHTML = `
            <div class="shard-card-top">
                <span class="shard-title">No Servers Connected Yet</span>
                <span class="badge-green">Add Server</span>
            </div>
            <div class="shard-detail">Click "+ Add Server" or go to Settings to add your first Discord server.</div>
        `;
        grid.appendChild(emptyCard);
        return;
    }

    servers.forEach(srv => {
        const card = document.createElement("div");
        card.className = "server-status-card";

        const isOnline = srv.status === "Active" || srv.status === "Online";
        let pingClass = "ping-offline";
        let pingText = "Offline";

        if (isOnline) {
            const p = srv.ping_ms || 45;
            if (p < 100) pingClass = "ping-fast";
            else if (p < 250) pingClass = "ping-med";
            else pingClass = "ping-slow";
            pingText = `${p} ms`;
        }

        const initials = (srv.guild_name || "S").split(" ").map(w => w[0]).slice(0, 2).join("").toUpperCase();

        card.innerHTML = `
            <div class="server-card-header">
                <div class="server-identity">
                    <div class="server-avatar-badge">${escapeHtml(initials)}</div>
                    <div>
                        <div class="server-name-title">${escapeHtml(srv.guild_name || 'Discord Server')}</div>
                        <div class="server-guild-id">ID: ${escapeHtml(srv.guild_id)}</div>
                    </div>
                </div>
                <div class="server-ping-badge ${pingClass}">
                    ● ${pingText}
                </div>
            </div>

            <div class="server-metrics-row">
                <div class="server-metric-item">
                    <span class="metric-k">Bot</span>
                    <span class="metric-v">${escapeHtml(srv.bot_name || 'Main Bot')}</span>
                </div>
                <div class="server-metric-item">
                    <span class="metric-k">Channels</span>
                    <span class="metric-v">${srv.channel_count || 4} Channels</span>
                </div>
                <div class="server-metric-item">
                    <span class="metric-k">Used Storage</span>
                    <span class="metric-v">${escapeHtml(srv.storage_formatted || '0 B')}</span>
                </div>
                <div class="server-metric-item">
                    <span class="metric-k">Status</span>
                    <span class="metric-v" style="color: ${isOnline ? 'var(--accent-green)' : 'var(--accent-red)'};">${isOnline ? 'Active' : 'Offline'}</span>
                </div>
            </div>

            <div class="server-card-footer">
                <span class="shard-detail">All Good</span>
                <div class="server-action-btns">
                    <button class="btn-server-act btn-ping-single" title="Check server speed">
                        ⚡ Ping
                    </button>
                    <button class="btn-server-act btn-setup-single" title="Make sure storage channels exist on this server">
                        ⚙️ Setup Channels
                    </button>
                    <button class="btn-server-act btn-view-channels" title="View channels on this server">
                        📋 Channels
                    </button>
                    <button class="btn-server-act danger btn-delete-server" title="Remove this server">
                        🗑️
                    </button>
                </div>
            </div>
        `;

        const btnPing = card.querySelector(".btn-ping-single");
        if (btnPing) {
            btnPing.addEventListener("click", async (e) => {
                e.stopPropagation();
                btnPing.disabled = true;
                btnPing.textContent = "⚡ Testing...";
                try {
                    const res = await fetch("/api/servers/health");
                    const data = await res.json();
                    if (res.ok && data.servers) {
                        renderServers(data.servers);
                        showToast(`Ping refreshed for ${srv.guild_name}`, "success");
                    }
                } catch (err) {
                    showToast(`Ping failed: ${err.message}`, "error");
                } finally {
                    btnPing.disabled = false;
                    btnPing.textContent = "⚡ Ping";
                }
            });
        }

        const btnSetup = card.querySelector(".btn-setup-single");
        if (btnSetup) {
            btnSetup.addEventListener("click", async (e) => {
                e.stopPropagation();
                btnSetup.disabled = true;
                btnSetup.textContent = "⚙️ Setting up...";
                showToast(`Configuring storage channels on ${srv.guild_name}...`, "info");
                try {
                    const res = await fetch("/api/servers/setup_channels", {
                        method: "POST",
                        headers: { "Content-Type": "application/json" },
                        body: JSON.stringify({ guild_id: srv.guild_id })
                    });
                    const data = await res.json();
                    if (res.ok && data.ok) {
                        showToast(`Configured ${data.storage_channels} storage channels on ${srv.guild_name}!`, "success");
                        addLogEntry(`Storage channels provisioned on ${srv.guild_name}`, "success");
                        loadServers();
                    } else {
                        showToast(data.error || "Setup failed", "error");
                    }
                } catch (err) {
                    showToast(`Setup error: ${err.message}`, "error");
                } finally {
                    btnSetup.disabled = false;
                    btnSetup.textContent = "⚙️ Setup Shards";
                }
            });
        }

        const btnView = card.querySelector(".btn-view-channels");
        if (btnView) {
            btnView.addEventListener("click", (e) => {
                e.stopPropagation();
                inspectServerChannels(srv.guild_id, srv.guild_name);
            });
        }

        const btnDel = card.querySelector(".btn-delete-server");
        if (btnDel) {
            btnDel.addEventListener("click", async (e) => {
                e.stopPropagation();
                if (!confirm(`Remove server "${srv.guild_name}" from your storage cluster?`)) return;
                try {
                    const res = await fetch("/api/bots/delete", {
                        method: "POST",
                        headers: { "Content-Type": "application/json" },
                        body: JSON.stringify({ id: srv.guild_id })
                    });
                    if (res.ok) {
                        showToast("Server node removed", "success");
                        loadServers();
                        loadBots();
                    }
                } catch (err) {
                    showToast(`Error: ${err.message}`, "error");
                }
            });
        }

        grid.appendChild(card);
    });
}

async function inspectServerChannels(guildID, guildName) {
    const inspector = document.getElementById("server-channel-inspector");
    const nameEl = document.getElementById("inspector-server-name");
    const subEl = document.getElementById("inspector-server-sub");
    const listEl = document.getElementById("inspector-channels-list");

    if (!inspector || !listEl) return;

    if (nameEl) nameEl.textContent = `${guildName} Channels`;
    if (subEl) subEl.textContent = `Server ID: ${guildID}`;
    listEl.innerHTML = `<div class="preview-loading">Loading channels from Discord...</div>`;
    inspector.classList.remove("hidden");
    inspector.scrollIntoView({ behavior: 'smooth' });

    try {
        const res = await fetch("/api/servers/channels", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ guild_id: guildID })
        });
        const data = await res.json();
        if (res.ok && data.ok && data.channels) {
            listEl.innerHTML = "";
            data.channels.forEach(ch => {
                const item = document.createElement("div");
                item.className = "channel-list-item";
                item.innerHTML = `
                    <div class="channel-info-left">
                        <span class="channel-name"># ${escapeHtml(ch.name)}</span>
                        <span class="channel-type-badge">${escapeHtml(ch.type)}</span>
                    </div>
                    <div style="display: flex; align-items: center; gap: 8px;">
                        ${ch.is_storage ? '<span class="badge-green">Storage Channel</span>' : ''}
                        <span class="shard-detail">${escapeHtml(ch.id)}</span>
                    </div>
                `;
                listEl.appendChild(item);
            });
        } else {
            listEl.innerHTML = `<div class="preview-loading">Could not load channels: ${data.error || 'Server error'}</div>`;
        }
    } catch (e) {
        listEl.innerHTML = `<div class="preview-loading">Error: ${e.message}</div>`;
    }
}

function initServerDashboard() {
    const btnPingAll = document.getElementById("btn-ping-all-servers");
    if (btnPingAll) {
        btnPingAll.addEventListener("click", async () => {
            btnPingAll.disabled = true;
            btnPingAll.innerHTML = `<span class="loading-spin"></span> <span>Pinging All...</span>`;
            showToast("Testing connection to all Discord servers...", "info");
            addLogEntry("Pinging all server nodes...", "info");
            try {
                const res = await fetch("/api/servers/health");
                const data = await res.json();
                if (res.ok && data.servers) {
                    renderServers(data.servers);
                    showToast(`Scanned ${data.count} server nodes successfully!`, "success");
                    addLogEntry(`Cluster scan complete: ${data.count} nodes checked`, "success");
                }
            } catch (err) {
                showToast(`Ping error: ${err.message}`, "error");
            } finally {
                btnPingAll.disabled = false;
                btnPingAll.innerHTML = `<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M13 2L3 14h9l-1 8 10-12h-9l1-8z"></path></svg> <span>Ping All Servers</span>`;
            }
        });
    }

    const btnOpenAdd = document.getElementById("btn-open-add-server-modal");
    const modalAdd = document.getElementById("modal-add-server");
    const btnCloseAdd = document.getElementById("btn-close-add-server-modal");
    const btnCancelAdd = document.getElementById("btn-cancel-add-server-modal");
    const btnConfirmAdd = document.getElementById("btn-confirm-add-server");

    if (btnOpenAdd && modalAdd) {
        btnOpenAdd.addEventListener("click", () => {
            modalAdd.classList.remove("hidden");
            const tokenInp = document.getElementById("modal-server-token-input");
            if (tokenInp) tokenInp.focus();
        });
    }

    const closeAddModal = () => {
        if (modalAdd) modalAdd.classList.add("hidden");
        const tokenInp = document.getElementById("modal-server-token-input");
        const guildInp = document.getElementById("modal-server-guild-input");
        if (tokenInp) tokenInp.value = "";
        if (guildInp) guildInp.value = "";
    };

    if (btnCloseAdd) btnCloseAdd.addEventListener("click", closeAddModal);
    if (btnCancelAdd) btnCancelAdd.addEventListener("click", closeAddModal);

    if (btnConfirmAdd) {
        btnConfirmAdd.addEventListener("click", async () => {
            const tokenInp = document.getElementById("modal-server-token-input");
            const guildInp = document.getElementById("modal-server-guild-input");
            const token = tokenInp ? tokenInp.value.trim() : "";
            const guild = guildInp ? guildInp.value.trim() : "";

            if (!token || !guild) {
                showToast("Please provide both bot token and server ID", "error");
                return;
            }

            btnConfirmAdd.disabled = true;
            btnConfirmAdd.textContent = "Connecting...";
            showToast("Connecting bot to server...", "info");

            try {
                const res = await fetch("/api/bots/add", {
                    method: "POST",
                    headers: { "Content-Type": "application/json" },
                    body: JSON.stringify({ bot_token: token, guild_id: guild })
                });
                const data = await res.json();
                if (res.ok && data.ok) {
                    showToast(`Server "${data.node.guild_name}" linked successfully!`, "success");
                    addLogEntry(`Added storage server "${data.node.guild_name}" with bot "${data.node.bot_name}"`, "success");
                    closeAddModal();
                    loadServers();
                    loadBots();
                } else {
                    showToast(`Could not link server: ${data.error || 'Check permissions'}`, "error");
                }
            } catch (err) {
                showToast(`Error: ${err.message}`, "error");
            } finally {
                btnConfirmAdd.disabled = false;
                btnConfirmAdd.textContent = "Link Server";
            }
        });
    }
}

function initSettingsActions() {
    const btnSave = document.getElementById("btn-save-settings");
    const btnAutoSetup = document.getElementById("btn-run-auto-setup");
    const btnClean = document.getElementById("btn-clean-channels");
    const btnRestore = document.getElementById("btn-restore-drive");

    if (btnSave) {
        btnSave.addEventListener("click", async () => {
            const passInput = document.getElementById("cfg-password");
            const pass = passInput ? passInput.value.trim() : "";

            if (!pass) {
                showToast("Please enter a new password", "error");
                return;
            }

            try {
                const res = await fetch("/api/auth/set_password", {
                    method: "POST",
                    headers: { "Content-Type": "application/json" },
                    body: JSON.stringify({ password: pass })
                });
                if (res.ok) {
                    showToast("Password updated and saved safely", "success");
                    addLogEntry("Master encryption password updated", "system");
                    if (passInput) passInput.value = "";
                } else {
                    showToast("Could not update password", "error");
                }
            } catch (e) {
                showToast(`Error: ${e.message}`, "error");
            }
        });
    }

    if (btnAutoSetup) {
        btnAutoSetup.addEventListener("click", async () => {
            const token = document.getElementById("cfg-bot-token").value.trim();
            const guild = document.getElementById("cfg-guild-id").value.trim();

            btnAutoSetup.disabled = true;
            showToast("Setting up your Discord storage channels...", "info");
            addLogEntry("Running 1 click setup for private cloud...", "info");

            try {
                const res = await fetch("/api/auto-setup", {
                    method: "POST",
                    headers: { "Content-Type": "application/json" },
                    body: JSON.stringify({ bot_token: token, guild_id: guild })
                });
                const data = await res.json();
                if (res.ok) {
                    showToast("Setup completed successfully", "success");
                    addLogEntry("Discord storage channels and backup metadata are ready", "success");
                    loadBots();
                    loadServers();
                } else {
                    const errMsg = data.error || "Setup stopped";
                    showToast(errMsg, "error");
                    addLogEntry(`Setup stopped: ${errMsg}`, "error");
                }
            } catch (e) {
                showToast(`Setup error: ${e.message}`, "error");
            } finally {
                btnAutoSetup.disabled = false;
            }
        });
    }

    const checkAllServers = document.getElementById("clean-check-all-servers");
    if (checkAllServers) {
        checkAllServers.addEventListener("change", () => {
            const itemChecks = document.querySelectorAll(".clean-server-item-check");
            itemChecks.forEach(cb => cb.checked = checkAllServers.checked);
        });
    }

    if (btnClean) {
        btnClean.addEventListener("click", async () => {
            const checkAll = document.getElementById("clean-check-all-servers");
            const itemChecks = document.querySelectorAll(".clean-server-item-check");
            const selectedGuilds = [];

            itemChecks.forEach(cb => {
                if (cb.checked && cb.dataset.guildId) {
                    selectedGuilds.push(cb.dataset.guildId);
                }
            });

            const isAll = (checkAll && checkAll.checked) || (itemChecks.length > 0 && selectedGuilds.length === itemChecks.length) || selectedGuilds.length === 0;
            const targetMsg = isAll ? "all connected bots and servers" : `${selectedGuilds.length} selected server(s)`;

            if (!confirm(`Delete old storage channels across ${targetMsg} and reset setup?`)) return;

            btnClean.disabled = true;
            showToast("Cleaning old channels from server(s)...", "info");
            addLogEntry(`Cleaning old storage channels from ${targetMsg}...`, "info");

            try {
                const res = await fetch("/api/channels/clean", {
                    method: "POST",
                    headers: { "Content-Type": "application/json" },
                    body: JSON.stringify({
                        all_servers: isAll,
                        target_guild_ids: selectedGuilds
                    })
                });
                const data = await res.json();
                if (res.ok && data.ok) {
                    showToast(`Cleaned ${data.deleted_count || 0} old channels across ${data.servers_count || 1} server(s)`, "success");
                    addLogEntry(`Removed ${data.deleted_count || 0} old channels across ${data.servers_count || 1} server(s)`, "success");
                    loadServers();
                    loadBots();
                } else {
                    showToast(`Could not clean channels: ${data.error || 'Server error'}`, "error");
                }
            } catch (e) {
                showToast(`Clean error: ${e.message}`, "error");
            } finally {
                btnClean.disabled = false;
            }
        });
    }

    populateCleanServersChecklist();

    if (btnRestore) {
        btnRestore.addEventListener("click", async () => {
            const metaId = prompt("Enter your metadata channel ID (leave blank to search automatically):", "");
            showToast("Restoring files from cloud backup...", "info");
            addLogEntry("Reading backup snapshot from Discord...", "info");

            try {
                const res = await fetch("/api/catalog/restore", {
                    method: "POST",
                    headers: { "Content-Type": "application/json" },
                    body: JSON.stringify({ metadata_channel_id: metaId })
                });
                const data = await res.json();
                if (res.ok && data.ok) {
                    showToast(`Restored ${data.files_count || 0} files successfully`, "success");
                    addLogEntry(`Restored ${data.files_count || 0} files into your local index`, "success");
                    loadFiles("");
                    loadStatus();
                } else {
                    const errMsg = data.error || "Restore stopped";
                    showToast(errMsg, "error");
                    addLogEntry(`Restore stopped: ${errMsg}`, "error");
                }
            } catch (e) {
                showToast(`Restore error: ${e.message}`, "error");
            }
        });
    }
}

async function loadSettings() {
    try {
        const res = await fetch("/api/settings");
        if (res.ok) {
            const data = await res.json();
            if (data.guild_id) document.getElementById("cfg-guild-id").value = data.guild_id;
            if (data.bot_token_masked) document.getElementById("cfg-bot-token").placeholder = data.bot_token_masked;
            if (data.chunk_size_bytes) {
                const csEl = document.getElementById("cfg-chunk-size");
                if (csEl) csEl.value = data.chunk_size_bytes;
            }
        }
    } catch (e) {
    }
}

function formatBytes(bytes, decimals = 1) {
    if (!bytes || bytes === 0) return '0 B';
    const k = 1024;
    const dm = decimals < 0 ? 0 : decimals;
    const sizes = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return parseFloat((bytes / Math.pow(k, i)).toFixed(dm)) + ' ' + sizes[i];
}

function getFileIconSvg(filename, isDir) {
    if (isDir) {
        return `<svg width="17" height="17" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"><path d="M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z"></path></svg>`;
    }
    const ext = filename.split('.').pop().toLowerCase();
    if (['mp4', 'mkv', 'avi', 'mov', 'webm'].includes(ext)) {
        return `<svg width="17" height="17" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"><polygon points="5 3 19 12 5 21 5 3"></polygon></svg>`;
    }
    if (['mp3', 'wav', 'flac', 'aac', 'ogg'].includes(ext)) {
        return `<svg width="17" height="17" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"><path d="M9 18V5l12-2v13"></path><circle cx="6" cy="18" r="3"></circle><circle cx="18" cy="16" r="3"></circle></svg>`;
    }
    if (['jpg', 'jpeg', 'png', 'gif', 'webp', 'svg'].includes(ext)) {
        return `<svg width="17" height="17" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"><rect x="3" y="3" width="18" height="18" rx="2" ry="2"></rect><circle cx="8.5" cy="8.5" r="1.5"></circle><polyline points="21 15 16 10 5 21"></polyline></svg>`;
    }
    if (['zip', 'rar', '7z', 'tar', 'gz', 'iso'].includes(ext)) {
        return `<svg width="17" height="17" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"><path d="M21 8v13H3V8"></path><path d="M1 3h22v5H1z"></path><path d="M10 12h4"></path></svg>`;
    }
    if (['json', 'js', 'py', 'go', 'rs', 'html', 'css', 'c', 'cpp', 'sh'].includes(ext)) {
        return `<svg width="17" height="17" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"><polyline points="16 18 22 12 16 6"></polyline><polyline points="8 6 2 12 8 18"></polyline></svg>`;
    }
    return `<svg width="17" height="17" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"></path><polyline points="14 2 14 8 20 8"></polyline></svg>`;
}

function showToast(message, type = "info") {
    const container = document.getElementById("toast-container");
    if (!container) return;

    const toast = document.createElement("div");
    toast.className = `toast toast-${type}`;
    toast.textContent = message;

    container.appendChild(toast);
    setTimeout(() => {
        toast.style.opacity = "0";
        setTimeout(() => toast.remove(), 300);
    }, 3500);
}

let authState = { has_password: false, is_unlocked: false };

async function initAuthLock() {
    const overlay = document.getElementById("lock-screen-overlay");
    const titleEl = document.getElementById("lock-screen-title");
    const subEl = document.getElementById("lock-screen-sub");
    const warnBox = document.getElementById("lock-warning-box");
    const inputEl = document.getElementById("lock-password-input");
    const btnSubmit = document.getElementById("btn-submit-lock");
    const btnText = document.getElementById("btn-submit-lock-text");

    if (!overlay || !inputEl || !btnSubmit) return;

    try {
        const res = await fetch("/api/auth/status");
        if (res.ok) {
            authState = await res.json();
        }
    } catch (e) {
    }

    if (authState.is_unlocked) {
        overlay.classList.add("hidden");
        return;
    }

    overlay.classList.remove("hidden");

    if (!authState.has_password) {
        if (titleEl) titleEl.textContent = "Set Master Password";
        if (subEl) subEl.textContent = "Choose a password to secure your personal cloud storage.";
        if (warnBox) warnBox.classList.remove("hidden");
        if (btnText) btnText.textContent = "Set Password and Unlock";
    } else {
        if (titleEl) titleEl.textContent = "Unlock Discord Free Cloud";
        if (subEl) subEl.textContent = "Enter your master password to access your files.";
        if (warnBox) warnBox.classList.add("hidden");
        if (btnText) btnText.textContent = "Unlock Drive";
    }

    const doSubmit = async () => {
        const pass = inputEl.value.trim();
        if (!pass) {
            showToast("Please enter a password", "error");
            return;
        }

        btnSubmit.disabled = true;
        const endpoint = authState.has_password ? "/api/auth/unlock" : "/api/auth/set_password";

        try {
            const res = await fetch(endpoint, {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({ password: pass })
            });
            const data = await res.json();
            if (res.ok && data.ok) {
                showToast("Drive unlocked successfully", "success");
                addLogEntry("Drive encryption key initialized and unlocked", "success");
                overlay.classList.add("hidden");
                inputEl.value = "";
                loadFiles("");
                loadBots();
                loadServers();
                loadStatus();
            } else {
                const errMsg = data.error || "Incorrect password";
                showToast(errMsg, "error");
                addLogEntry(`Unlock failed: ${errMsg}`, "error");
            }
        } catch (e) {
            showToast(`Error: ${e.message}`, "error");
        } finally {
            btnSubmit.disabled = false;
        }
    };

    btnSubmit.onclick = doSubmit;
    inputEl.onkeydown = (e) => {
        if (e.key === "Enter") doSubmit();
    };
}

async function populateCleanServersChecklist() {
    const container = document.getElementById("clean-specific-servers-list");
    if (!container) return;

    try {
        const res = await fetch("/api/bots");
        if (!res.ok) return;
        const data = await res.json();
        const nodes = data.nodes || [];

        if (nodes.length === 0) {
            container.innerHTML = `<span style="font-size: 12px; color: var(--text-tertiary);">Primary server configured</span>`;
            return;
        }

        let html = "";
        nodes.forEach(n => {
            html += `
                <label class="clean-server-checkbox-label" style="display: flex; align-items: center; gap: 8px; font-size: 12px; cursor: pointer; color: var(--text-secondary);">
                    <input type="checkbox" class="custom-checkbox clean-server-item-check" data-guild-id="${escapeHtml(n.guild_id)}" checked>
                    <span>${escapeHtml(n.guild_name || n.guild_id)} <span style="color: var(--text-tertiary);">(${escapeHtml(n.bot_name || 'Bot')})</span></span>
                </label>
            `;
        });
        container.innerHTML = html;

        const checkAll = document.getElementById("clean-check-all-servers");
        const itemChecks = container.querySelectorAll(".clean-server-item-check");

        itemChecks.forEach(cb => {
            cb.addEventListener("change", () => {
                const allChecked = Array.from(itemChecks).every(c => c.checked);
                if (checkAll) checkAll.checked = allChecked;
            });
        });
    } catch (e) {
    }
}
