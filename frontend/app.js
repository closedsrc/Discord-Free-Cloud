let ws = null;
let currentParentID = "";
let currentFolderBreadcrumbs = [];
let allLoadedFiles = [];
let currentFilter = "all";
let activeJobID = null;
let lastStatusData = null;

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

async function apiFetch(url, options = {}) {
    try {
        const res = await fetch(url, options);
        const contentType = res.headers.get("content-type") || "";
        let data = null;
        if (contentType.includes("application/json")) {
            try {
                data = await res.json();
            } catch (jsonErr) {
                data = { ok: false, error: `Invalid response from server: ${jsonErr.message}` };
            }
        } else {
            const text = await res.text().catch(() => "");
            if (res.ok) {
                data = { ok: true, text: text };
            } else {
                data = { ok: false, error: text || `HTTP ${res.status}: ${res.statusText}` };
            }
        }
        return { ok: Boolean(res.ok && (data.ok || data.ok === undefined)), status: res.status, data: data || {} };
    } catch (err) {
        return { ok: false, status: 0, data: { ok: false, error: err.message || "Network connection error" } };
    }
}

function updateCloudStatus(statusData) {
    const statusText = document.getElementById("system-status-text") || document.getElementById("conn-status-text");
    const dot = document.getElementById("system-status-dot") || document.getElementById("status-live-dot");
    const unconfiguredBanner = document.getElementById("unconfigured-warning-banner");

    if (!ws || ws.readyState !== WebSocket.OPEN) {
        if (statusText) statusText.textContent = "Offline";
        if (dot) dot.className = "status-dot offline";
        return;
    }

    if (!statusData) {
        if (statusText) statusText.textContent = "Connecting...";
        if (dot) dot.className = "status-dot warning";
        return;
    }

    if (!statusData.is_unlocked) {
        if (statusText) statusText.textContent = "Locked";
        if (dot) dot.className = "status-dot locked";
        return;
    }

    const hasBots = statusData.is_configured || (statusData.bot_nodes_count && statusData.bot_nodes_count > 0) || (statusData.channels_count && statusData.channels_count > 0);

    if (!hasBots) {
        if (statusText) statusText.textContent = "Setup Needed";
        if (dot) dot.className = "status-dot warning";
        if (unconfiguredBanner) unconfiguredBanner.classList.remove("hidden");
    } else {
        if (statusText) statusText.textContent = "Cloud Ready";
        if (dot) dot.className = "status-dot online";
        if (unconfiguredBanner) unconfiguredBanner.classList.add("hidden");
    }
}

async function loadStatus() {
    const { ok, data } = await apiFetch("/api/status");
    if (ok) {
        lastStatusData = data;
        updateCloudStatus(data);
    }
}

document.addEventListener("DOMContentLoaded", () => {
    initAuthLock();
    initNavigation();
    initQuickSetupWizard();
    initDragAndDrop();
    initFileActions();
    initSettingsActions();
    initActivityLogs();
    initModals();
    initFilters();
    initBotActions();
    initServerActions();
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
        addLogEntry("Connected to local engine", "system");
        loadStatus();
    };

    ws.onmessage = (event) => {
        try {
            const msg = JSON.parse(event.data);
            handleWsMessage(msg);
        } catch (e) {
        }
    };

    ws.onclose = () => {
        updateCloudStatus(null);
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
        if (rowCheckboxes.length) {
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
        const isDelete = t.type === "DELETE";
        const pct = Math.min(100, Math.max(isDone ? 100 : 0, Math.round(t.progress_percent || 0)));
        let typeBadge = `<span class="transfer-card-badge upload">Upload</span>`;
        if (t.type === "DOWNLOAD") {
            typeBadge = `<span class="transfer-card-badge download">Download</span>`;
        } else if (isDelete) {
            typeBadge = `<span class="transfer-card-badge err">Delete</span>`;
        }
        const statusBadge = isDone ? `<span class="transfer-card-badge done">Finished</span>` : (isFailed ? `<span class="transfer-card-badge err">Failed</span>` : `<span class="transfer-card-badge ${isDelete ? 'err' : 'upload'}">${pct}%</span>`);
        const cardClass = isDone ? "completed" : (isFailed ? "failed" : "");
        const cardId = `transfer-card-${t.job_id}`;
        const etaText = formatETA(t.eta_seconds);
        let speedText = t.speed_mbs ? `${t.speed_mbs.toFixed(2)} MB/s${etaText ? ` • ${etaText}` : ''}` : `Part ${t.completed_chunks || 0}/${t.total_chunks || 0}`;
        if (isDelete) {
            speedText = `${t.completed_chunks || 0} of ${t.total_chunks || 0} parts removed`;
        }

        const statsBytesText = isDelete 
            ? `${t.completed_chunks || 0} / ${t.total_chunks || 0} parts`
            : `${formatBytes(t.processed_bytes || 0)} of ${formatBytes(t.total_bytes || 0)}`;

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
                    <div class="apple-progress-fill ${isDone ? 'success' : (isDelete ? 'danger glow' : 'glow')}" style="width: ${pct}%;"></div>
                </div>
                <div class="transfer-card-stats">
                    <span class="stats-bytes">${statsBytesText}</span>
                    <span class="stats-speed">${speedText}</span>
                    <span class="stats-action">${!isDone && !isFailed ? (isDelete ? '<span class="text-xs opacity-75">Cleaning...</span>' : `<button class="btn-apple-secondary btn-sm" onclick="cancelJob('${escapeHtml(t.job_id)}')">Cancel</button>`) : (isDone ? '✓' : '✗')}</span>
                </div>
            `;
            container.appendChild(card);
        } else {
            card.className = `transfer-item-card ${cardClass}`;
            const fill = card.querySelector(".apple-progress-fill");
            if (fill) {
                fill.style.width = `${pct}%`;
                fill.className = `apple-progress-fill ${isDone ? 'success' : (isDelete ? 'danger glow' : 'glow')}`;
            }
            const statusBadgeContainer = card.querySelector(".status-badge-container");
            if (statusBadgeContainer) statusBadgeContainer.innerHTML = statusBadge;

            const bytesSpan = card.querySelector(".stats-bytes");
            if (bytesSpan) bytesSpan.textContent = statsBytesText;

            const speedSpan = card.querySelector(".stats-speed");
            if (speedSpan) speedSpan.textContent = speedText;

            const actionSpan = card.querySelector(".stats-action");
            if (actionSpan) {
                actionSpan.innerHTML = !isDone && !isFailed ? (isDelete ? '<span class="text-xs opacity-75">Cleaning...</span>' : `<button class="btn-apple-secondary btn-sm" onclick="cancelJob('${escapeHtml(t.job_id)}')">Cancel</button>`) : (isDone ? '✓' : '✗');
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

function checkAndHideHUD(panel) {
    const active = Array.from(allTransfersMap.values()).some(t => t.status === "ACTIVE" || t.status === "STARTED");
    if (active || !panel) return;
    panel.classList.remove("hud-active");
    setTimeout(() => {
        const stillActive = Array.from(allTransfersMap.values()).some(t => t.status === "ACTIVE" || t.status === "STARTED");
        if (!stillActive && panel) panel.classList.add("hidden");
    }, 300);
}

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

    if (rawMsg.type === "servers_changed" || rawMsg.type === "bots_changed" || rawMsg.type === "status_changed") {
        loadServers();
        loadBots();
        loadStatus();
        if (rawMsg.new_count && rawMsg.new_count > 0) {
            showToast(`Connected ${rawMsg.new_count} new Discord server(s)!`, "success");
            addLogEntry(`Realtime sync: ${rawMsg.new_count} new server(s) detected and configured`, "success");
        }
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

        const isDeleteOnly = activeList.every(t => t.type === "DELETE");
        const hasDelete = activeList.some(t => t.type === "DELETE");

        if (nameEl) {
            if (activeCount === 1) {
                if (activeList[0].type === "DELETE") {
                    nameEl.textContent = `Deleting ${activeList[0].file_name || "files"}...`;
                } else {
                    nameEl.textContent = activeList[0].file_name || "File";
                }
            } else {
                if (isDeleteOnly) {
                    nameEl.textContent = `${activeCount} delete operations in progress`;
                } else {
                    nameEl.textContent = `${activeCount} transfers in progress (${activeList[0].file_name})`;
                }
            }
        }
        if (bytesEl) {
            if (isDeleteOnly) {
                bytesEl.textContent = `${totalProcessedBytes} of ${totalActiveBytes} parts deleted`;
            } else {
                bytesEl.textContent = `${formatBytes(totalProcessedBytes)} of ${formatBytes(totalActiveBytes)}`;
            }
        }
        if (fillEl) {
            fillEl.style.width = `${aggPct}%`;
            fillEl.className = isDeleteOnly ? "apple-progress-fill danger glow" : "apple-progress-fill glow";
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
            if (isDeleteOnly) {
                speedEl.textContent = "Discord Cleanup";
            } else {
                speedEl.textContent = `${totalSpeed.toFixed(2)} MB/s`;
            }
        }
        if (etaEl) {
            const formatted = formatETA(maxEta);
            etaEl.textContent = formatted || (activeCount > 0 ? (isDeleteOnly ? "In progress" : "Calculating...") : "");
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

        setTimeout(() => checkAndHideHUD(panel), 2500);

        if (msg.status === "COMPLETED" && msg.file_name) {
            if (msg.type === "DELETE") {
                showToast(`Deleted ${msg.file_name} from Discord!`, "success");
            } else {
                showToast(`"${msg.file_name}" finished!`, "success");
            }
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

function switchToTab(navId) {
    const views = {
        "nav-explorer": "view-explorer",
        "nav-bots": "view-bots",
        "nav-servers": "view-servers",
        "nav-settings": "view-settings",
        "nav-setup": "view-settings"
    };

    const targetViewId = views[navId];
    if (!targetViewId) return;

    document.querySelectorAll(".nav-button").forEach(b => b.classList.remove("active"));
    const activeBtn = document.getElementById(navId) || document.getElementById(navId === "nav-setup" ? "nav-settings" : navId);
    if (activeBtn) activeBtn.classList.add("active");

    document.querySelectorAll(".view-panel").forEach(p => p.classList.add("hidden"));
    const targetView = document.getElementById(targetViewId) || document.getElementById("view-setup");
    if (targetView) targetView.classList.remove("hidden");

    const titleEl = document.getElementById("page-title");
    const breadcrumbEl = document.getElementById("breadcrumb-bar");
    const searchEl = document.getElementById("top-center-search");
    const fileActionsEl = document.getElementById("top-file-actions");

    if (navId === "nav-explorer") {
        if (titleEl) titleEl.textContent = "Files";
        if (breadcrumbEl) breadcrumbEl.classList.remove("hidden");
        if (searchEl) searchEl.classList.remove("hidden");
        if (fileActionsEl) fileActionsEl.classList.remove("hidden");
        loadFiles(currentParentID);
    } else {
        if (breadcrumbEl) breadcrumbEl.classList.add("hidden");
        if (searchEl) searchEl.classList.add("hidden");
        if (fileActionsEl) fileActionsEl.classList.add("hidden");

        if (navId === "nav-bots") {
            if (titleEl) titleEl.textContent = "My Bots";
            loadBots();
        } else if (navId === "nav-servers") {
            if (titleEl) titleEl.textContent = "My Servers";
            loadServers();
        } else if (navId === "nav-settings" || navId === "nav-setup") {
            if (titleEl) titleEl.textContent = "Settings";
            loadSettings();
        }
    }
}

function initNavigation() {
    const views = ["nav-explorer", "nav-bots", "nav-servers", "nav-settings", "nav-setup"];

    views.forEach(navId => {
        const btn = document.getElementById(navId);
        if (btn) {
            btn.addEventListener("click", () => switchToTab(navId));
        }
    });

    const bannerBtn = document.getElementById("btn-goto-settings-banner");
    if (bannerBtn) {
        bannerBtn.addEventListener("click", () => openQuickSetupWizard());
    }

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

let wizardState = {
    step: 1,
    botToken: "",
    botName: "",
    inviteUrl: "",
    detectedServers: []
};

function setWizardStep(step) {
    wizardState.step = step;

    for (let i = 1; i <= 3; i++) {
        const node = document.getElementById(`wizard-node-${i}`);
        const panel = document.getElementById(`wizard-panel-${i}`);
        if (node) {
            node.classList.remove("active", "done");
            if (i === step) {
                node.classList.add("active");
            } else if (i < step) {
                node.classList.add("done");
            }
        }
        if (panel) {
            if (i === step) {
                panel.classList.remove("hidden");
            } else {
                panel.classList.add("hidden");
            }
        }
    }
}

async function openQuickSetupWizard() {
    const modal = document.getElementById("modal-quick-setup");
    if (!modal) {
        switchToTab("nav-bots");
        return;
    }

    const { ok, data } = await apiFetch("/api/bots");
    const bots = (ok && data.nodes) ? data.nodes : [];

    if (bots.length) {
        const firstBot = bots[0];
        wizardState.botName = firstBot.bot_name || "Discord Bot";
        wizardState.inviteUrl = firstBot.invite_url || "";
        const nameEl = document.getElementById("wizard-bot-added-name");
        if (nameEl) nameEl.textContent = `Connected to ${wizardState.botName}`;
        const linkEl = document.getElementById("wizard-invite-link");
        if (linkEl && wizardState.inviteUrl) {
            linkEl.href = wizardState.inviteUrl;
        }

        const srvRes = await apiFetch("/api/servers");
        const servers = (srvRes.ok && srvRes.data.servers) ? srvRes.data.servers : [];
        if (servers.length) {
            setWizardStep(3);
            loadWizardServers(servers);
        } else {
            setWizardStep(2);
        }
    } else {
        setWizardStep(1);
    }

    modal.classList.remove("hidden");
    const input = document.getElementById("wizard-bot-token");
    if (input && wizardState.step === 1) {
        input.value = "";
        input.focus();
    }
}

function closeQuickSetupWizard() {
    const modal = document.getElementById("modal-quick-setup");
    if (modal) modal.classList.add("hidden");
}

function loadWizardServers(servers) {
    const container = document.getElementById("wizard-servers-checklist");
    if (!container) return;

    if (!servers || servers.length === 0) {
        container.innerHTML = `
            <div style="font-size: 12px; color: var(--text-secondary); padding: 8px;">
                No joined Discord servers detected yet. Make sure you invited the bot to your server in Step 2!
            </div>
        `;
        return;
    }

    let html = "";
    servers.forEach(s => {
        const isReady = (s.channel_count && s.channel_count >= 3);
        html += `
            <div style="display: flex; align-items: center; justify-content: space-between; padding: 6px 4px; border-bottom: 1px solid var(--border-subtle);">
                <div style="display: flex; align-items: center; gap: 8px;">
                    <div style="width: 24px; height: 24px; border-radius: 4px; background: var(--accent-discord); color: #fff; display: flex; align-items: center; justify-content: center; font-size: 10px; font-weight: 700;">
                        ${escapeHtml((s.guild_name || 'S')[0].toUpperCase())}
                    </div>
                    <div>
                        <div style="font-size: 12px; font-weight: 600; color: #fff;">${escapeHtml(s.guild_name || s.guild_id)}</div>
                        <div style="font-size: 10.5px; color: var(--text-muted);">ID: ${escapeHtml(s.guild_id)}</div>
                    </div>
                </div>
                <span class="${isReady ? 'badge-green' : 'cloud-badge-unlimited'}" style="font-size: 10px;">
                    ${isReady ? 'Channels Ready' : 'Needs Setup'}
                </span>
            </div>
        `;
    });
    container.innerHTML = html;
}

function initQuickSetupWizard() {
    const modal = document.getElementById("modal-quick-setup");
    const btnClose = document.getElementById("btn-close-quick-setup");
    const btnAddBot = document.getElementById("btn-wizard-add-bot");
    const btnJoinedServer = document.getElementById("btn-wizard-joined-server");
    const btnFinish = document.getElementById("btn-wizard-finish-setup");
    const btnBack1 = document.getElementById("btn-wizard-back-1");
    const btnBack2 = document.getElementById("btn-wizard-back-2");
    const inputToken = document.getElementById("wizard-bot-token");

    if (btnClose) btnClose.addEventListener("click", closeQuickSetupWizard);
    if (modal) {
        modal.addEventListener("click", (e) => {
            if (e.target === modal) closeQuickSetupWizard();
        });
    }

    if (btnBack1) btnBack1.addEventListener("click", () => setWizardStep(1));
    if (btnBack2) btnBack2.addEventListener("click", () => setWizardStep(2));

    if (btnAddBot && inputToken) {
        const handleAdd = async () => {
            const token = inputToken.value.trim();
            if (!token) {
                showToast("Please paste your bot token", "error");
                return;
            }

            btnAddBot.disabled = true;
            btnAddBot.innerHTML = `<span>Connecting...</span>`;
            showToast("Verifying bot token with Discord...", "info");

            const { ok, data } = await apiFetch("/api/bots/add", {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({ token: token })
            });

            btnAddBot.disabled = false;
            btnAddBot.innerHTML = `<span>Verify &amp; Add Bot</span> <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M5 12h14M12 5l7 7-7 7"/></svg>`;

            if (ok && data.ok) {
                showToast(`Connected to ${data.node?.bot_name || 'Bot'}!`, "success");
                addLogEntry(`Connected Discord bot ${data.node?.bot_name || ''}`, "success");
                wizardState.botName = data.node?.bot_name || "Discord Bot";
                wizardState.inviteUrl = data.node?.invite_url || "";

                const nameEl = document.getElementById("wizard-bot-added-name");
                if (nameEl) nameEl.textContent = `Connected to ${wizardState.botName}`;

                const linkEl = document.getElementById("wizard-invite-link");
                if (linkEl && wizardState.inviteUrl) {
                    linkEl.href = wizardState.inviteUrl;
                }

                loadBots();
                loadStatus();
                setWizardStep(2);
            } else {
                showToast(`Could not connect bot: ${data.error || 'Check token'}`, "error");
            }
        };

        btnAddBot.addEventListener("click", handleAdd);
        inputToken.addEventListener("keydown", (e) => {
            if (e.key === "Enter") handleAdd();
        });
    }

    if (btnJoinedServer) {
        btnJoinedServer.addEventListener("click", async () => {
            btnJoinedServer.disabled = true;
            btnJoinedServer.innerHTML = `<span>Detecting servers...</span>`;

            await apiFetch("/api/servers/sync", { method: "POST" });
            const srvRes = await apiFetch("/api/servers");
            const servers = (srvRes.ok && srvRes.data.servers) ? srvRes.data.servers : [];
            wizardState.detectedServers = servers;

            btnJoinedServer.disabled = false;
            btnJoinedServer.innerHTML = `<span>I Added Bot to Server</span> <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M5 12h14M12 5l7 7-7 7"/></svg>`;

            setWizardStep(3);
            loadWizardServers(servers);
            loadServers();
            loadStatus();
        });
    }

    if (btnFinish) {
        btnFinish.addEventListener("click", async () => {
            btnFinish.disabled = true;
            btnFinish.innerHTML = `<span>Initializing storage...</span>`;
            showToast("Initializing storage channels on Discord server...", "info");

            const srvRes = await apiFetch("/api/servers");
            const servers = (srvRes.ok && srvRes.data.servers) ? srvRes.data.servers : [];

            if (servers.length === 0) {
                showToast("No server detected yet. Please ensure the bot is added to your server.", "error");
                btnFinish.disabled = false;
                btnFinish.innerHTML = `<span>Initialize Storage &amp; Finish</span>`;
                return;
            }

            for (const s of servers) {
                await apiFetch("/api/servers/setup_channels", {
                    method: "POST",
                    headers: { "Content-Type": "application/json" },
                    body: JSON.stringify({ guild_id: s.guild_id })
                });
            }

            btnFinish.disabled = false;
            btnFinish.innerHTML = `<span>Initialize Storage &amp; Finish</span>`;

            showToast("Setup complete! Your private cloud storage is ready.", "success");
            addLogEntry("Discord storage channels created and verified", "success");

            closeQuickSetupWizard();
            loadStatus();
            loadServers();
            loadBots();
            switchToTab("nav-explorer");
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
        if (items && items.length) {
            for (let i = 0; i < items.length; i++) {
                const item = items[i].webkitGetAsEntry();
                if (item) {
                    await uploadFileSystemEntry(item, currentParentID);
                }
            }
            return;
        }

        const files = e.dataTransfer.files;
        if (files && files.length) {
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
        const { ok, data } = await apiFetch("/api/folders/create", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ name: folderName, parent_id: parentId })
        });
        if (ok && (data.ok || data.id || data.ID)) {
            const newFolderId = (data.folder && data.folder.id) || data.id || data.ID;
            const dirReader = entry.createReader();
            dirReader.readEntries(async (entries) => {
                for (const child of entries) {
                    await uploadFileSystemEntry(child, newFolderId);
                }
            });
        } else {
            showToast(`Could not create folder "${folderName}": ${data.error || "Server error"}`, "error");
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

    const { ok, data } = await apiFetch("/api/upload/file", {
        method: "POST",
        body: formData
    });

    if (ok && data.job_id) {
        activeJobID = data.job_id;
        if (data.file_id) pendingHighlightFileId = data.file_id;
    } else {
        const errMsg = data.error || "Upload failed";
        showToast(`Upload failed: ${errMsg}`, "error");
        addLogEntry(`Upload stopped: ${errMsg}`, "error");
        if (panel) {
            panel.classList.remove("hud-active");
            setTimeout(() => {
                const anyActive = Array.from(allTransfersMap.values()).some(t => t.status === "ACTIVE" || t.status === "STARTED");
                if (!anyActive && panel) panel.classList.add("hidden");
            }, 500);
        }
    }
}

function initFileActions() {
    const btnUpload = document.getElementById("btn-upload-file");
    const fileInput = document.getElementById("hidden-file-input");

    if (btnUpload && fileInput) {
        btnUpload.addEventListener("click", () => fileInput.click());
        fileInput.addEventListener("change", async (e) => {
            if (e.target.files && e.target.files.length) {
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
            const allChecked = thSelectAll.checked;
            document.querySelectorAll(".file-row-check").forEach(cb => {
                const id = cb.dataset.id;
                cb.checked = allChecked;
                if (!id) return;
                if (allChecked) selectedFileIds.add(id);
                else selectedFileIds.delete(id);
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
            const confirmed = await showConfirmModal(
                `Delete ${count} selected item${count === 1 ? '' : 's'}?`,
                `This will permanently delete the selected ${count} item${count === 1 ? '' : 's'} and clean up associated parts.`,
                "Delete Selected",
                true
            );
            if (!confirmed) return;

            showToast(`Deleting ${count} selected item${count === 1 ? '' : 's'}...`, "info");
            const { ok, data } = await apiFetch("/api/delete", {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({ file_ids: Array.from(selectedFileIds) })
            });

            if (ok && data.ok) {
                selectedFileIds.clear();
                updateBatchActionBar();
                await loadFiles(currentParentID);
                loadStatus();
                if (data.discord_messages && data.discord_messages > 0) {
                    showToast(`Cleaning ${data.discord_messages} parts from Discord...`, "info");
                } else {
                    showToast(`Deleted ${count} item${count === 1 ? '' : 's'}`, "success");
                }
            } else {
                showToast(`Could not delete: ${data.error || 'Server error'}`, "error");
            }
        });
    }

    const btnCancelAll = document.getElementById("btn-cancel-all-transfers");
    if (btnCancelAll) {
        btnCancelAll.addEventListener("click", async () => {
            await apiFetch("/api/jobs/cancel", {
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
        });
    }

    const btnBackup = document.getElementById("btn-sync-catalog");
    if (btnBackup) {
        btnBackup.addEventListener("click", async () => {
            showToast("Saving backup...", "info");
            addLogEntry("Syncing catalog backup...", "info");
            const { ok, data } = await apiFetch("/api/catalog/sync", { method: "POST" });
            if (ok && data.ok) {
                showToast("Backup saved", "success");
                addLogEntry("Catalog backup saved to Discord", "success");
            } else {
                showToast(`Backup error: ${data.error || 'Check Discord setup'}`, "error");
                addLogEntry(`Backup stopped: ${data.error || 'Check Discord setup'}`, "error");
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
            const { ok, data } = await apiFetch("/api/folders/create", {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({ name, parent_id: currentParentID })
            });

            btnConfirmFolder.disabled = false;
            if (ok && (data.ok || data.id || data.ID)) {
                showToast(`Folder "${name}" created`, "success");
                hideFolderModal();
                loadFiles(currentParentID);
            } else {
                showToast(`Could not make folder: ${data.error || 'Server error'}`, "error");
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

            const { ok, data } = await apiFetch("/api/files/create_text", {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({
                    name: filename,
                    filename: filename,
                    content: content,
                    parent_id: currentParentID
                })
            });

            btnConfirmCreateFile.disabled = false;
            if (ok && data.ok) {
                showToast(`Created ${filename}`, "success");
                addLogEntry(`Created ${filename}`, "success");
                hideCreateFileModal();
                loadFiles(currentParentID);
            } else {
                showToast(`Could not create file: ${data.error || 'Server error'}`, "error");
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

function showConfirmModal(title, message, confirmBtnText = "Delete", isDanger = true) {
    return new Promise((resolve) => {
        const modal = document.getElementById("modal-confirm");
        const titleEl = document.getElementById("modal-confirm-title");
        const msgEl = document.getElementById("modal-confirm-message");
        const btnConfirm = document.getElementById("btn-confirm-action");
        const btnCancel = document.getElementById("btn-confirm-cancel");
        const btnClose = document.getElementById("btn-close-confirm-modal");

        if (!modal) {
            resolve(true);
            return;
        }

        if (titleEl) titleEl.textContent = title;
        if (msgEl) msgEl.textContent = message;
        if (btnConfirm) {
            btnConfirm.textContent = confirmBtnText;
            btnConfirm.className = isDanger ? "btn-danger" : "btn-primary";
        }

        let handled = false;
        const cleanup = () => {
            if (handled) return;
            handled = true;
            modal.classList.add("hidden");
            btnConfirm.onclick = null;
            btnCancel.onclick = null;
            if (btnClose) btnClose.onclick = null;
            modal.onclick = null;
            document.removeEventListener("keydown", handleKey);
        };

        const handleKey = (e) => {
            if (e.key === "Escape") {
                cleanup();
                resolve(false);
            }
        };

        btnConfirm.onclick = () => {
            cleanup();
            resolve(true);
        };

        btnCancel.onclick = () => {
            cleanup();
            resolve(false);
        };

        if (btnClose) {
            btnClose.onclick = () => {
                cleanup();
                resolve(false);
            };
        }

        modal.onclick = (e) => {
            if (e.target === modal) {
                cleanup();
                resolve(false);
            }
        };

        document.addEventListener("keydown", handleKey);
        modal.classList.remove("hidden");
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

    const { ok, data } = await apiFetch(`/api/files?parent_id=${encodeURIComponent(parentId)}`);
    if (ok && Array.isArray(data)) {
        allLoadedFiles = data;
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
    } else {
        addLogEntry(`Failed to fetch file list: ${data.error || 'Server error'}`, "error");
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
        if (item.is_dir) {
            tdStatus.innerHTML = `<span style="background: rgba(251, 191, 36, 0.12); color: #fbbf24; border: 1px solid rgba(251, 191, 36, 0.25); border-radius: 12px; padding: 3px 8px; font-size: 11px; font-weight: 600; display: inline-flex; align-items: center; gap: 4px;"><span style="width: 5px; height: 5px; border-radius: 50%; background: #fbbf24; display: inline-block;"></span>Folder</span>`;
        } else {
            tdStatus.innerHTML = `<span class="badge-green">Saved</span>`;
        }

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
            const confirmed = await showConfirmModal(
                `Delete "${item.name}"?`,
                `This will permanently remove this ${item.is_dir ? "folder and its contents" : "file"} from your storage.`,
                "Delete",
                true
            );
            if (!confirmed) return;

            showToast(`Deleting ${item.name}...`, "info");
            const { ok, data } = await apiFetch("/api/delete", {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({ file_id: item.id })
            });

            if (ok && data.ok) {
                selectedFileIds.delete(item.id);
                updateBatchActionBar();
                await loadFiles(currentParentID);
                loadStatus();
                if (data.discord_messages && data.discord_messages > 0) {
                    showToast(`Cleaning ${data.discord_messages} parts from Discord...`, "info");
                } else {
                    showToast(`Deleted ${item.name}`, "success");
                }
            } else {
                showToast(`Could not delete item: ${data.error || 'Server error'}`, "error");
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

    const streamUrl = `/api/download/file?file_id=${encodeURIComponent(file.id)}&inline=1`;
    const ext = (file.name || "").split('.').pop().toLowerCase();

    if (['mp4', 'webm', 'mov', 'mkv'].includes(ext)) {
        container.innerHTML = `<video class="preview-media-player" controls autoplay preload="auto" playsinline src="${streamUrl}"></video>`;
    } else if (['mp3', 'wav', 'ogg', 'aac', 'flac'].includes(ext)) {
        container.innerHTML = `<audio class="preview-media-player" controls autoplay preload="auto" src="${streamUrl}"></audio>`;
    } else if (['jpg', 'jpeg', 'png', 'gif', 'webp', 'svg', 'bmp', 'ico'].includes(ext)) {
        container.innerHTML = `<img class="preview-image-view" src="${streamUrl}" alt="${escapeHtml(file.name)}">`;
    } else if (ext === 'pdf') {
        container.innerHTML = `<iframe class="preview-frame" src="${streamUrl}"></iframe>`;
    } else if (['txt', 'md', 'json', 'js', 'ts', 'tsx', 'jsx', 'py', 'go', 'html', 'css', 'c', 'cpp', 'h', 'hpp', 'sh', 'rs', 'yaml', 'yml', 'xml', 'log', 'csv', 'ini', 'toml', 'env', 'bat', 'cmd', 'ps1', 'sql', 'java', 'kt', 'rb', 'php', 'lua', 'r', 'svelte', 'vue', 'conf', 'cfg', 'properties'].includes(ext)) {
        try {
            const res = await fetch(streamUrl);
            if (!res.ok) throw new Error("Fetch error");
            const text = await res.text();
            container.innerHTML = `<div class="preview-code-box"><pre>${escapeHtml(text)}</pre></div>`;
        } catch (e) {
            container.innerHTML = `<div class="preview-loading">Could not load text preview</div>`;
        }
    } else {
        container.innerHTML = `
            <div style="text-align: center; color: var(--text-secondary); padding: 32px 16px;">
                <div style="font-size: 14px; font-weight: 600; color: var(--text-primary);">Preview not available for this file type</div>
                <div style="margin-top: 8px; font-size: 12px; color: var(--text-muted);">Click Download to save "${escapeHtml(file.name)}" to your computer</div>
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
    const { ok, data } = await apiFetch("/api/status");
    if (ok) {
        lastStatusData = data;
        updateTelemetryUI(data);
        updateCloudStatus(data);
    }
}
function initBotActions() {
    const btnAdd = document.getElementById("btn-add-cluster-bot");
    if (btnAdd) {
        btnAdd.addEventListener("click", async () => {
            const tokenInput = document.getElementById("new-bot-token");
            if (!tokenInput) return;

            const token = tokenInput.value.trim();

            if (!token) {
                showToast("Please enter a bot token", "error");
                return;
            }

            btnAdd.disabled = true;
            showToast("Connecting bot...", "info");
            addLogEntry("Verifying bot token with Discord...", "info");

            const { ok, data } = await apiFetch("/api/bots/add", {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({ bot_token: token })
            });

            btnAdd.disabled = false;
            if (ok && data.ok) {
                showToast("Bot connected", "success");
                addLogEntry("Bot connected. Invite link ready.", "success");
                tokenInput.value = "";
                loadBots();
                loadServers();
                loadStatus();
            } else {
                const errMsg = data.error || "Could not connect bot";
                showToast(errMsg, "error");
                addLogEntry(`Bot connection error: ${errMsg}`, "error");
            }
        });
    }

    const btnSyncBots = document.getElementById("btn-sync-bots-now");
    if (btnSyncBots) {
        btnSyncBots.addEventListener("click", async () => {
            btnSyncBots.disabled = true;
            showToast("Checking Discord for servers...", "info");
            const { ok, data } = await apiFetch("/api/servers/sync", { method: "POST" });
            btnSyncBots.disabled = false;
            if (ok) {
                if (data.new_count > 0) {
                    showToast(`Detected ${data.new_count} server(s)`, "success");
                } else {
                    showToast("Servers up to date", "info");
                }
                loadBots();
                loadServers();
                loadStatus();
            }
        });
    }
}

async function removeBotNode(id, name) {
    const confirmed = await showConfirmModal(
        `Remove "${name}"?`,
        `This will disconnect bot node ${name} from your storage cluster.`,
        "Remove Bot",
        true
    );
    if (!confirmed) return;
    const { ok, data } = await apiFetch("/api/bots/delete", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ id })
    });
    if (!ok) {
        showToast(`Could not remove bot: ${data.error || 'Server error'}`, "error");
        return;
    }
    showToast("Bot removed", "success");
    loadBots();
    loadServers();
    loadStatus();
}

async function loadBots() {
    const { ok, data } = await apiFetch("/api/bots");
    if (ok) {
        const grid = document.getElementById("bots-cluster-grid");
        if (!grid) return;
        grid.innerHTML = "";

        if (data.nodes && data.nodes.length) {
            data.nodes.forEach(node => {
                const card = document.createElement("div");
                card.className = "shard-pill-card";
                const guildCount = node.guild_count || (node.guild_id ? 1 : 0);
                const isPending = guildCount === 0 || node.status === "Pending Server";
                const statusBadge = isPending 
                    ? `<span style="background: rgba(251, 191, 36, 0.12); color: #fbbf24; border: 1px solid rgba(251, 191, 36, 0.25); border-radius: 12px; padding: 3px 8px; font-size: 11px; font-weight: 600; display: inline-flex; align-items: center; gap: 4px;"><span style="width: 5px; height: 5px; border-radius: 50%; background: #fbbf24; display: inline-block;"></span>Pending Server</span>`
                    : `<span class="badge-green">${escapeHtml(node.status || 'Active')}</span>`;

                const serverDesc = isPending 
                    ? `<div style="font-size: 12px; color: #fbbf24; margin: 4px 0 8px 0;">Bot not in any server yet. Click <strong>Invite to Server</strong> to link it.</div>`
                    : `<div class="shard-detail" style="margin-bottom: 5px;">Connected Servers (${guildCount}): <strong>${escapeHtml(node.guild_name || node.guild_id)}</strong></div>
                       <div class="shard-detail" style="margin-bottom: 10px;">Ping: ${node.ping_ms || 35}ms | Storage Channels: ${node.channel_count || 4}</div>`;

                const inviteBtnHtml = node.invite_url ? `
                    <a href="${escapeHtml(node.invite_url)}" target="_blank" class="btn-primary btn-sm btn-invite-bot" style="text-decoration: none; display: inline-flex; align-items: center; gap: 6px;">
                        <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"></path><polyline points="15 3 21 3 21 9"></polyline><line x1="10" y1="14" x2="21" y2="3"></line></svg>
                        <span>Invite to Server</span>
                    </a>
                ` : '';

                card.innerHTML = `
                    <div class="shard-card-top">
                        <span class="shard-title" style="display: flex; align-items: center; gap: 8px;">
                            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="#818cf8" stroke-width="2"><rect x="4" y="8" width="16" height="12" rx="2"></rect><path d="M12 2v6"></path><circle cx="9" cy="14" r="1"></circle><circle cx="15" cy="14" r="1"></circle></svg>
                            ${escapeHtml(node.bot_name || 'Discord Bot')}
                        </span>
                        ${statusBadge}
                    </div>
                    ${serverDesc}
                `;

                const actionsRow = document.createElement("div");
                actionsRow.className = "row-actions-group";
                actionsRow.style.marginTop = "8px";
                actionsRow.style.gap = "8px";

                if (inviteBtnHtml) {
                    const wrap = document.createElement("div");
                    wrap.innerHTML = inviteBtnHtml;
                    actionsRow.appendChild(wrap.firstElementChild);
                }

                const btnDelete = document.createElement("button");
                btnDelete.className = "btn-row-action delete";
                btnDelete.textContent = "Remove";
                btnDelete.addEventListener("click", () => removeBotNode(node.id, node.bot_name || 'this bot'));

                actionsRow.appendChild(btnDelete);
                card.appendChild(actionsRow);
                grid.appendChild(card);
            });
        } else {
            const emptyCard = document.createElement("div");
            emptyCard.className = "shard-pill-card";
            emptyCard.style.gridColumn = "1 / -1";
            emptyCard.innerHTML = `
                <div class="shard-card-top">
                    <span class="shard-title">No Bots Connected</span>
                    <button class="btn-primary btn-xs" id="btn-focus-bot-input">Connect Bot</button>
                </div>
                <div class="shard-detail">Paste your Bot Token in the box above and click Add Bot. You will get an instant Invite link to add the bot to any Discord server.</div>
            `;
            grid.appendChild(emptyCard);
            const btnFocus = emptyCard.querySelector("#btn-focus-bot-input");
            btnFocus?.addEventListener("click", () => {
                document.getElementById("new-bot-token")?.focus();
            });
        }
        populateCleanServersChecklist();
    }
}

let allConnectedServers = [];

async function loadServers() {
    const { ok, data } = await apiFetch("/api/servers");
    if (ok) {
        allConnectedServers = data.servers || [];
        renderServers(allConnectedServers);
        updateTargetServerDropdown(allConnectedServers);
    }
}

function updateTargetServerDropdown(servers) {
    const targetSelect = document.getElementById("upload-target-select");
    if (!targetSelect) return;

    const currentVal = targetSelect.value;
    targetSelect.innerHTML = `<option value="all">Every Server (All Bots)</option>`;

    if (servers && servers.length) {
        servers.forEach(srv => {
            const opt = document.createElement("option");
            opt.value = srv.guild_id;
            opt.textContent = `${srv.guild_name || 'Server'} (${srv.bot_name || 'Bot'})`;
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
        emptyCard.style.gridColumn = "1 / -1";
        emptyCard.innerHTML = `
            <div class="shard-card-top">
                <span class="shard-title">No Storage Servers Connected Yet</span>
                <button class="btn-primary btn-xs" id="btn-open-setup-from-servers">Setup Bot &amp; Server</button>
            </div>
            <div class="shard-detail">Connect a Discord bot under My Bots and invite it to your Discord storage server. It will appear here with automatic storage channels.</div>
        `;
        grid.appendChild(emptyCard);
        const btnSetupFromSrv = emptyCard.querySelector("#btn-open-setup-from-servers");
        if (btnSetupFromSrv) {
            btnSetupFromSrv.addEventListener("click", () => openQuickSetupWizard());
        }
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
                <div class="server-action-btns">
                    <button class="btn-server-act btn-ping-single" title="Test server response time">
                        <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><polygon points="13 2 3 14 12 14 11 22 21 10 12 10 13 2"></polygon></svg>
                        <span>Ping</span>
                    </button>
                    <button class="btn-server-act btn-setup-single" title="Verify and initialize storage channels">
                        <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="3"></circle><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 0 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 0 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 0 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 0 1 2.83-2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"></path></svg>
                        <span>Setup</span>
                    </button>
                    <button class="btn-server-act btn-view-channels" title="View storage channels">
                        <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><line x1="8" y1="6" x2="21" y2="6"></line><line x1="8" y1="12" x2="21" y2="12"></line><line x1="8" y1="18" x2="21" y2="18"></line><line x1="3" y1="6" x2="3.01" y2="6"></line><line x1="3" y1="12" x2="3.01" y2="12"></line><line x1="3" y1="18" x2="3.01" y2="18"></line></svg>
                        <span>Channels</span>
                    </button>
                    <button class="btn-server-act danger btn-delete-server" title="Remove server">
                        <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><polyline points="3 6 5 6 21 6"></polyline><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"></path></svg>
                    </button>
                </div>
            </div>
        `;

        const btnPing = card.querySelector(".btn-ping-single");
        if (btnPing) {
            btnPing.addEventListener("click", async (e) => {
                e.stopPropagation();
                btnPing.disabled = true;
                const { ok, data } = await apiFetch("/api/servers/health");
                btnPing.disabled = false;
                if (ok && data.servers) {
                    renderServers(data.servers);
                    showToast(`Ping refreshed for ${srv.guild_name}`, "success");
                } else {
                    showToast(`Ping failed: ${data.error || 'Server error'}`, "error");
                }
            });
        }

        const btnSetup = card.querySelector(".btn-setup-single");
        if (btnSetup) {
            btnSetup.addEventListener("click", async (e) => {
                e.stopPropagation();
                btnSetup.disabled = true;
                showToast(`Setting up storage channels on ${srv.guild_name}...`, "info");
                const { ok, data } = await apiFetch("/api/servers/setup_channels", {
                    method: "POST",
                    headers: { "Content-Type": "application/json" },
                    body: JSON.stringify({ guild_id: srv.guild_id })
                });
                btnSetup.disabled = false;
                if (ok && data.ok) {
                    showToast(`Created channels on ${srv.guild_name}`, "success");
                    addLogEntry(`Storage channels created on ${srv.guild_name}`, "success");
                    loadServers();
                    loadStatus();
                } else {
                    showToast(data.error || "Setup failed", "error");
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
                const confirmed = await showConfirmModal(
                    `Remove Server "${srv.guild_name}"?`,
                    `This will remove the server from active replication.`,
                    "Remove Server",
                    true
                );
                if (!confirmed) return;
                const { ok, data } = await apiFetch("/api/bots/delete", {
                    method: "POST",
                    headers: { "Content-Type": "application/json" },
                    body: JSON.stringify({ id: srv.guild_id })
                });
                if (ok) {
                    showToast("Server removed", "success");
                    loadServers();
                    loadBots();
                    loadStatus();
                } else {
                    showToast(`Could not remove server: ${data.error || 'Server error'}`, "error");
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

    const { ok, data } = await apiFetch("/api/servers/channels", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ guild_id: guildID })
    });

    if (ok && data.ok && data.channels) {
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
}

function initServerActions() {
    const btnSyncServers = document.getElementById("btn-sync-servers-now");
    if (btnSyncServers) {
        btnSyncServers.addEventListener("click", async () => {
            btnSyncServers.disabled = true;
            showToast("Scanning for Discord servers...", "info");
            const { ok, data } = await apiFetch("/api/servers/sync", { method: "POST" });
            btnSyncServers.disabled = false;
            if (ok) {
                if (data.new_count > 0) {
                    showToast(`Found ${data.new_count} new server(s)`, "success");
                } else {
                    showToast("Servers up to date", "info");
                }
                loadServers();
                loadBots();
                loadStatus();
            }
        });
    }

    const btnPingAll = document.getElementById("btn-ping-all-servers");
    if (btnPingAll) {
        btnPingAll.addEventListener("click", async () => {
            btnPingAll.disabled = true;
            btnPingAll.innerHTML = `<span class="loading-spin"></span> <span>Pinging...</span>`;
            showToast("Testing connection to Discord servers...", "info");
            addLogEntry("Pinging servers...", "info");

            const { ok, data } = await apiFetch("/api/servers/health");
            btnPingAll.disabled = false;
            btnPingAll.innerHTML = `<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M13 2L3 14h9l-1 8 10-12h-9l1-8z"></path></svg> <span>Ping All</span>`;

            if (ok && data.servers) {
                renderServers(data.servers);
                showToast("Ping completed", "success");
                addLogEntry(`Ping completed: ${data.count} servers checked`, "success");
            } else {
                showToast(`Ping error: ${data.error || 'Server error'}`, "error");
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

            const { ok, data } = await apiFetch("/api/bots/add", {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({ bot_token: token, guild_id: guild })
            });

            btnConfirmAdd.disabled = false;
            btnConfirmAdd.textContent = "Link Server";

            if (ok && data.ok) {
                showToast(`Server "${data.node.guild_name}" linked successfully!`, "success");
                addLogEntry(`Added storage server "${data.node.guild_name}" with bot "${data.node.bot_name}"`, "success");
                closeAddModal();
                loadServers();
                loadBots();
                loadStatus();
            } else {
                showToast(`Could not link server: ${data.error || 'Check permissions'}`, "error");
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

            const { ok, data } = await apiFetch("/api/auth/set_password", {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({ password: pass })
            });

            if (ok && data.ok) {
                showToast("Password updated", "success");
                addLogEntry("Encryption password updated", "system");
                if (passInput) passInput.value = "";
                loadStatus();
            } else {
                showToast(`Could not update password: ${data.error || 'Server error'}`, "error");
            }
        });
    }

    if (btnAutoSetup) {
        btnAutoSetup.addEventListener("click", async () => {
            const token = document.getElementById("cfg-bot-token").value.trim();
            const guild = document.getElementById("cfg-guild-id").value.trim();

            btnAutoSetup.disabled = true;
            showToast("Setting up storage channels...", "info");
            addLogEntry("Running storage channel setup...", "info");

            const { ok, data } = await apiFetch("/api/auto-setup", {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({ bot_token: token, guild_id: guild })
            });

            btnAutoSetup.disabled = false;
            if (ok && data.ok) {
                showToast("Setup completed", "success");
                addLogEntry("Storage channels initialized", "success");
                loadBots();
                loadServers();
                loadStatus();
            } else {
                const errMsg = data.error || "Setup stopped";
                showToast(errMsg, "error");
                addLogEntry(`Setup stopped: ${errMsg}`, "error");
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

            const isAll = (checkAll && checkAll.checked) || (itemChecks.length && selectedGuilds.length === itemChecks.length) || selectedGuilds.length === 0;
            const targetMsg = isAll ? "all connected bots and servers" : `${selectedGuilds.length} selected server(s)`;

            const confirmed = await showConfirmModal(
                "Reset & Clean Storage Channels?",
                `Delete old storage channels across ${targetMsg} and reset setup?`,
                "Reset Channels",
                true
            );
            if (!confirmed) return;

            btnClean.disabled = true;
            showToast("Cleaning old channels from server(s)...", "info");
            addLogEntry(`Cleaning old storage channels from ${targetMsg}...`, "info");

            const { ok, data } = await apiFetch("/api/channels/clean", {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({
                    all_servers: isAll,
                    target_guild_ids: selectedGuilds
                })
            });

            btnClean.disabled = false;
            if (ok && data.ok) {
                showToast(`Cleaned ${data.deleted_count || 0} old channels across ${data.servers_count || 1} server(s)`, "success");
                addLogEntry(`Removed ${data.deleted_count || 0} old channels across ${data.servers_count || 1} server(s)`, "success");
                loadServers();
                loadBots();
                loadStatus();
            } else {
                showToast(`Could not clean channels: ${data.error || 'Server error'}`, "error");
            }
        });
    }

    populateCleanServersChecklist();

    if (btnRestore) {
        btnRestore.addEventListener("click", async () => {
            const metaId = prompt("Enter your metadata channel ID (leave blank to search automatically):", "");
            showToast("Restoring files from cloud backup...", "info");
            addLogEntry("Reading backup snapshot from Discord...", "info");

            const { ok, data } = await apiFetch("/api/catalog/restore", {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({ metadata_channel_id: metaId })
            });

            if (ok && data.ok) {
                showToast(`Restored ${data.files_imported || 0} files successfully`, "success");
                addLogEntry(`Restored ${data.files_imported || 0} files into your local index`, "success");
                loadFiles("");
                loadStatus();
            } else {
                const errMsg = data.error || "Restore stopped";
                showToast(errMsg, "error");
                addLogEntry(`Restore stopped: ${errMsg}`, "error");
            }
        });
    }
}

async function populateCleanServersChecklist() {
    const list = document.getElementById("clean-specific-servers-list");
    if (!list) return;
    const { ok, data } = await apiFetch("/api/servers");
    if (!ok || !data.servers || data.servers.length === 0) {
        list.innerHTML = `<span style="font-size: 11.5px; color: var(--text-muted);">No secondary servers connected</span>`;
        return;
    }
    list.innerHTML = "";
    data.servers.forEach(srv => {
        const item = document.createElement("label");
        item.className = "clean-server-checkbox-label";
        item.innerHTML = `
            <input type="checkbox" class="custom-checkbox clean-server-item-check" data-guild-id="${escapeHtml(srv.guild_id)}" checked>
            <span>${escapeHtml(srv.guild_name || srv.guild_id)} <small style="color: var(--text-muted);">(${escapeHtml(srv.bot_name || 'Bot')})</small></span>
        `;
        list.appendChild(item);
    });

    const checkAll = document.getElementById("clean-check-all-servers");
    const itemChecks = list.querySelectorAll(".clean-server-item-check");
    itemChecks.forEach(cb => {
        cb.addEventListener("change", () => {
            const allChecked = Array.from(itemChecks).every(c => c.checked);
            if (checkAll) checkAll.checked = allChecked;
        });
    });
}

async function loadSettings() {
    const { ok, data } = await apiFetch("/api/settings");
    if (ok && data) {
        const guildEl = document.getElementById("cfg-guild-id");
        if (guildEl && data.guild_id) guildEl.value = data.guild_id;
        const botEl = document.getElementById("cfg-bot-token");
        if (botEl && data.bot_token_masked) botEl.placeholder = data.bot_token_masked;
        if (data.chunk_size_bytes) {
            const csEl = document.getElementById("cfg-chunk-size");
            if (csEl) csEl.value = data.chunk_size_bytes;
        }
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
        return `<svg width="20" height="20" viewBox="0 0 24 24" fill="#fbbf24" stroke="#f59e0b" stroke-width="1.2"><path d="M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z"></path></svg>`;
    }
    const ext = (filename || "").split('.').pop().toLowerCase();
    if (['mp4', 'mkv', 'avi', 'mov', 'webm'].includes(ext)) {
        return `<svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="#c084fc" stroke-width="2"><rect x="2" y="2" width="20" height="20" rx="2.18" ry="2.18"></rect><line x1="7" y1="2" x2="7" y2="22"></line><line x1="17" y1="2" x2="17" y2="22"></line><line x1="2" y1="12" x2="22" y2="12"></line><line x1="2" y1="7" x2="7" y2="7"></line><line x1="2" y1="17" x2="7" y2="17"></line><line x1="17" y1="17" x2="22" y2="17"></line><line x1="17" y1="7" x2="22" y2="7"></line></svg>`;
    }
    if (['mp3', 'wav', 'flac', 'aac', 'ogg'].includes(ext)) {
        return `<svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="#f472b6" stroke-width="2"><path d="M9 18V5l12-2v13"></path><circle cx="6" cy="18" r="3"></circle><circle cx="18" cy="16" r="3"></circle></svg>`;
    }
    if (['jpg', 'jpeg', 'png', 'gif', 'webp', 'svg'].includes(ext)) {
        return `<svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="#38bdf8" stroke-width="2"><rect x="3" y="3" width="18" height="18" rx="2" ry="2"></rect><circle cx="8.5" cy="8.5" r="1.5"></circle><polyline points="21 15 16 10 5 21"></polyline></svg>`;
    }
    if (['zip', 'rar', '7z', 'tar', 'gz', 'iso'].includes(ext)) {
        return `<svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="#fb923c" stroke-width="2"><path d="M21 8v13H3V8"></path><path d="M1 3h22v5H1z"></path><path d="M10 12h4"></path></svg>`;
    }
    if (['json', 'js', 'ts', 'jsx', 'tsx', 'py', 'go', 'rs', 'html', 'css', 'c', 'cpp', 'sh', 'yaml', 'yml'].includes(ext)) {
        return `<svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="#818cf8" stroke-width="2"><polyline points="16 18 22 12 16 6"></polyline><polyline points="8 6 2 12 8 18"></polyline></svg>`;
    }
    if (['pdf', 'doc', 'docx', 'txt', 'md', 'rtf'].includes(ext)) {
        return `<svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="#34d399" stroke-width="2"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"></path><polyline points="14 2 14 8 20 8"></polyline><line x1="16" y1="13" x2="8" y2="13"></line><line x1="16" y1="17" x2="8" y2="17"></line><polyline points="10 9 9 9 8 9"></polyline></svg>`;
    }
    return `<svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="#94a3b8" stroke-width="2"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"></path><polyline points="14 2 14 8 20 8"></polyline></svg>`;
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

    const { ok, data } = await apiFetch("/api/auth/status");
    if (ok) {
        authState = data;
    }

    if (authState.is_unlocked) {
        overlay.classList.add("hidden");
        return;
    }

    overlay.classList.remove("hidden");

    if (!authState.has_password) {
        if (titleEl) titleEl.textContent = "Set Encryption Password";
        if (subEl) subEl.textContent = "Set a password to encrypt your files.";
        if (warnBox) warnBox.classList.remove("hidden");
        if (btnText) btnText.textContent = "Set Password & Unlock";
    } else {
        if (titleEl) titleEl.textContent = "Unlock Drive";
        if (subEl) subEl.textContent = "Enter your encryption password to unlock files.";
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

        const { ok: submitOk, data: submitData } = await apiFetch(endpoint, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ password: pass })
        });

        btnSubmit.disabled = false;
        if (submitOk && submitData.ok) {
            showToast("Drive unlocked", "success");
            addLogEntry("Drive unlocked", "success");
            overlay.classList.add("hidden");
            inputEl.value = "";
            loadFiles("");
            loadBots();
            loadServers();
            loadStatus();
        } else {
            const errMsg = submitData.error || "Incorrect password";
            showToast(errMsg, "error");
            addLogEntry(`Unlock failed: ${errMsg}`, "error");
        }
    };

    btnSubmit.onclick = doSubmit;
    inputEl.onkeydown = (e) => {
        if (e.key === "Enter") doSubmit();
    };
}
