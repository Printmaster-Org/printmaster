// Global settings placeholder so sections can stash the latest snapshot.
const globalSettings = { discovery: {}, snmp: {}, features: {}, spooler: {}, logging: {}, web: {} };

// Cached header info for display
let cachedAgentVersion = null;
let cachedServerInfo = null;

const AGENT_UI_STATE_KEYS = {
    ACTIVE_TAB: 'pm_agent_active_tab',
};

// Toggle SNMPv3 settings visibility based on SNMP version selection
function toggleSNMPv3Settings() {
    const version = document.getElementById('dev_snmp_version')?.value || '2c';
    const v1v2cSettings = document.getElementById('snmp_v1v2c_settings');
    const v3Settings = document.getElementById('snmp_v3_settings');
    
    if (version === '3') {
        // Hide v1/v2c community string, show v3 settings
        if (v1v2cSettings) v1v2cSettings.style.display = 'none';
        if (v3Settings) v3Settings.style.display = 'block';
        // Also update auth/priv visibility based on security level
        toggleSNMPv3AuthPriv();
    } else {
        // Show v1/v2c community string, hide v3 settings
        if (v1v2cSettings) v1v2cSettings.style.display = 'block';
        if (v3Settings) v3Settings.style.display = 'none';
    }
}

// Toggle SNMPv3 auth/priv settings visibility based on security level
function toggleSNMPv3AuthPriv() {
    const secLevel = document.getElementById('dev_snmp_security_level')?.value || 'noAuthNoPriv';
    const authSettings = document.getElementById('snmp_v3_auth_settings');
    const privSettings = document.getElementById('snmp_v3_priv_settings');
    
    if (secLevel === 'noAuthNoPriv') {
        // Hide both auth and priv settings
        if (authSettings) authSettings.style.display = 'none';
        if (privSettings) privSettings.style.display = 'none';
    } else if (secLevel === 'authNoPriv') {
        // Show auth, hide priv
        if (authSettings) authSettings.style.display = 'block';
        if (privSettings) privSettings.style.display = 'none';
    } else {
        // authPriv: show both
        if (authSettings) authSettings.style.display = 'block';
        if (privSettings) privSettings.style.display = 'block';
    }
}

// Update the header info bar with version and server details
async function updateHeaderInfoBar() {
    const versionEl = document.getElementById('header_version');
    const serverEl = document.getElementById('header_server');
    const tenantEl = document.getElementById('header_tenant');
    const tenantSep = document.getElementById('header_tenant_sep');
    
    // Fetch version if not cached
    if (!cachedAgentVersion) {
        try {
            const resp = await fetch('/api/version');
            if (resp.ok) {
                cachedAgentVersion = await resp.json();
            }
        } catch (e) {
            console.warn('Failed to fetch version:', e);
        }
    }
    
    // Update version display
    if (versionEl && cachedAgentVersion) {
        const ver = cachedAgentVersion.version || 'unknown';
        const buildType = cachedAgentVersion.build_type || '';
        const showBuildType = buildType && buildType !== 'release' && !ver.toLowerCase().includes(buildType.toLowerCase());
        versionEl.textContent = 'v' + ver + (showBuildType ? ' (' + buildType + ')' : '');
        versionEl.title = 'Agent version: ' + ver + 
            (cachedAgentVersion.git_commit ? '\nCommit: ' + cachedAgentVersion.git_commit : '') +
            (cachedAgentVersion.build_time ? '\nBuilt: ' + cachedAgentVersion.build_time : '');
    }
    
    // Update server info from currentServerStatus (set by server connection code)
    if (serverEl) {
        if (currentServerStatus && currentServerStatus.enabled && currentServerStatus.url) {
            const mode = currentServerStatus.connection_mode || (currentServerStatus.connected ? 'connected' : 'disconnected');
            const serverUrl = currentServerStatus.url || '';
            // Extract hostname from URL
            let serverHost = serverUrl;
            try {
                const url = new URL(serverUrl);
                serverHost = url.hostname + (url.port && url.port !== '443' && url.port !== '80' ? ':' + url.port : '');
            } catch (e) {}
            
            serverEl.textContent = serverHost;
            serverEl.title = 'Server: ' + serverUrl + '\nConnection: ' + mode;
            serverEl.classList.add('clickable');
            serverEl.onclick = () => {
                const btn = document.getElementById('join_token_btn');
                if (btn) btn.click();
            };
        } else {
            serverEl.textContent = 'Standalone';
            serverEl.title = 'Not connected to a server';
            serverEl.classList.remove('clickable');
            serverEl.onclick = null;
        }
    }
    
    // Tenant info - check if we have it cached in the server info modal data
    if (tenantEl && tenantSep) {
        const tenantId = cachedServerInfo && cachedServerInfo.tenant_id;
        if (tenantId) {
            tenantEl.textContent = 'Tenant: ' + tenantId;
            tenantEl.title = 'Tenant ID: ' + tenantId;
            tenantEl.classList.remove('hidden');
            tenantSep.classList.remove('hidden');
        } else {
            tenantEl.classList.add('hidden');
            tenantSep.classList.add('hidden');
        }
    }
}

// Called when server status changes to update header
function onServerStatusChanged(status) {
    if (status && status.tenant_id) {
        cachedServerInfo = cachedServerInfo || {};
        cachedServerInfo.tenant_id = status.tenant_id;
    }
    updateHeaderInfoBar();
    const connected = deriveServerConnectionMode(status) !== 'disconnected';
    const reloadButton = document.getElementById('reload_server_settings_btn');
    if (reloadButton) reloadButton.style.display = connected ? 'inline-block' : 'none';
}

async function reloadServerSettings(button) {
    if (button) button.disabled = true;
    try {
        const response = await fetch('/settings/reload-server', { method: 'POST' });
        if (!response.ok) throw new Error(await response.text() || response.statusText);
        await loadSettings();
        window.__pm_shared.showToast('Settings reloaded from server', 'success', 2500);
    } catch (error) {
        window.__pm_shared.showToast('Failed to reload server settings: ' + error.message, 'error', 4000);
    } finally {
        if (button) button.disabled = false;
    }
}

function getAgentUIState(key, fallback) {
    try {
        if (typeof window === 'undefined' || !window.localStorage) {
            return fallback;
        }
        const value = window.localStorage.getItem(key);
        if (value === null || value === undefined || value === '') {
            return fallback;
        }
        return value;
    } catch (err) {
        return fallback;
    }
}

function persistAgentUIState(key, value) {
    try {
        if (typeof window === 'undefined' || !window.localStorage) {
            return;
        }
        window.localStorage.setItem(key, value);
    } catch (err) {
        // Ignore persistence errors (private browsing, etc.)
    }
}

const AUTO_UPDATE_STATUS_CLASS_MAP = {
    idle: 'status-idle',
    checking: 'status-active',
    pending: 'status-active',
    downloading: 'status-active',
    staging: 'status-active',
    applying: 'status-active',
    restarting: 'status-active',
    succeeded: 'status-success',
    failed: 'status-error',
    skipped: 'status-muted',
    rolled_back: 'status-error',
};

const AUTO_UPDATE_STATUS_COPY = {
    idle: 'Waiting for the next scheduled check.',
    checking: 'Contacting the update service...',
    pending: 'Preparing download...',
    downloading: 'Downloading the latest build...',
    staging: 'Staging files before install...',
    applying: 'Applying update...',
    restarting: 'Restarting the agent service...',
    succeeded: 'Last update completed successfully.',
    failed: 'Last update failed - check logs for details.',
    skipped: 'Update skipped per policy window.',
    rolled_back: 'Rolled back to previous version.',
};

const AUTO_UPDATE_BUSY_STATUSES = new Set(['checking', 'pending', 'downloading', 'staging', 'applying', 'restarting']);
const AUTO_UPDATE_STATUS_POLL_MS = 45000;
let autoUpdateStatusIntervalId = null;

// Real-time update progress state tracking
const updateProgressState = {
    status: 'idle',
    progress: 0,
    message: '',
    targetVersion: '',
    error: '',
    timestamp: 0
};

// Handle real-time update progress events from SSE
function handleUpdateProgress(data) {
    const status = data.status || 'unknown';
    const progress = data.progress || 0;
    const message = data.message || '';
    const targetVersion = data.target_version || '';
    const errorMsg = data.error || '';
    
    // Update state
    updateProgressState.status = status;
    updateProgressState.progress = progress;
    updateProgressState.message = message;
    updateProgressState.targetVersion = targetVersion;
    updateProgressState.error = errorMsg;
    updateProgressState.timestamp = Date.now();
    
    // Update UI elements
    updateAutoUpdateButtonsFromProgress(status, progress, message, targetVersion, errorMsg);
    
    // Show toast for key events
    switch (status) {
        case 'downloading':
            if (progress === 0) {
                window.__pm_shared.showToast('Downloading update...', 'info');
            }
            break;
        case 'ready':
            window.__pm_shared.showToast('Update downloaded, preparing to install...', 'info');
            break;
        case 'restarting':
            window.__pm_shared.showToast('Restarting agent to apply update...', 'info');
            break;
        case 'complete':
            window.__pm_shared.showToast('Update complete!', 'success');
            // Reset state after delay
            setTimeout(() => {
                updateProgressState.status = 'idle';
                updateProgressState.progress = 0;
                resetAutoUpdateButtons();
                refreshAutoUpdateStatus();
            }, 2000);
            break;
        case 'failed':
            window.__pm_shared.showToast('Update failed: ' + (errorMsg || message), 'error');
            setTimeout(() => {
                updateProgressState.status = 'idle';
                resetAutoUpdateButtons();
                refreshAutoUpdateStatus();
            }, 3000);
            break;
        case 'cancelled':
            window.__pm_shared.showToast('Update cancelled', 'warning');
            updateProgressState.status = 'idle';
            resetAutoUpdateButtons();
            break;
    }
}

// Update the auto-update buttons based on progress state
function updateAutoUpdateButtonsFromProgress(status, progress, message, targetVersion, errorMsg) {
    const checkBtn = document.getElementById('autoupdate_check_btn');
    const forceBtn = document.getElementById('autoupdate_force_btn');
    const badge = document.getElementById('autoupdate_status_badge');
    const copy = document.getElementById('autoupdate_status_copy');
    
    const isBusy = ['checking', 'downloading', 'ready', 'restarting'].includes(status);
    const canCancel = ['checking', 'downloading'].includes(status);
    
    if (checkBtn) {
        if (status === 'checking') {
            checkBtn.textContent = 'Checking...';
            checkBtn.disabled = true;
            checkBtn.classList.add('updating');
        } else if (isBusy) {
            checkBtn.textContent = 'Update in Progress';
            checkBtn.disabled = true;
            checkBtn.classList.add('updating');
        }
    }
    
    if (forceBtn) {
        if (status === 'downloading') {
            const pctText = progress > 0 ? ' ' + progress + '%' : '...';
            forceBtn.textContent = 'Downloading' + pctText;
            forceBtn.disabled = !canCancel;
            forceBtn.classList.add('updating');
            if (canCancel) {
                forceBtn.dataset.action = 'cancel';
                forceBtn.title = 'Click to cancel';
            }
        } else if (status === 'ready') {
            forceBtn.textContent = 'Installing...';
            forceBtn.disabled = true;
            forceBtn.classList.add('updating');
            delete forceBtn.dataset.action;
        } else if (status === 'restarting') {
            forceBtn.textContent = 'Restarting...';
            forceBtn.disabled = true;
            forceBtn.classList.add('updating');
            delete forceBtn.dataset.action;
        } else if (status === 'failed') {
            forceBtn.textContent = '✕ Failed';
            forceBtn.disabled = true;
            forceBtn.classList.add('error');
            forceBtn.title = errorMsg || message || 'Update failed';
            delete forceBtn.dataset.action;
        } else if (status === 'complete') {
            forceBtn.textContent = '✓ Updated';
            forceBtn.disabled = true;
            forceBtn.classList.add('success');
            delete forceBtn.dataset.action;
        }
    }
    
    // Update the status badge with real-time progress
    if (badge && isBusy) {
        badge.textContent = formatAutoUpdateStatusLabel(status);
        badge.className = 'autoupdate-status-pill ' + statusClassForAutoUpdate(status);
    }
    
    if (copy && message) {
        copy.textContent = message;
    }
}

// Reset buttons to normal state
function resetAutoUpdateButtons() {
    const checkBtn = document.getElementById('autoupdate_check_btn');
    const forceBtn = document.getElementById('autoupdate_force_btn');
    
    if (checkBtn) {
        checkBtn.textContent = 'Check for Update';
        checkBtn.disabled = false;
        checkBtn.classList.remove('updating');
        delete checkBtn.dataset.loading;
    }
    
    if (forceBtn) {
        forceBtn.textContent = 'Update Now';
        forceBtn.disabled = false;
        forceBtn.classList.remove('updating', 'error', 'success');
        forceBtn.removeAttribute('title');
        delete forceBtn.dataset.action;
        delete forceBtn.dataset.loading;
    }
}

function formatAutoUpdateStatusLabel(status) {
    if (!status || typeof status !== 'string') return 'Unknown';
    return status.split('_').map(part => part.charAt(0).toUpperCase() + part.slice(1)).join(' ');
}

function statusClassForAutoUpdate(status) {
    return AUTO_UPDATE_STATUS_CLASS_MAP[status] || 'status-muted';
}

function describeRelativeFromMs(diffMs) {
    if (!Number.isFinite(diffMs)) return '';
    const future = diffMs < 0;
    const abs = Math.abs(diffMs);
    const minute = 60 * 1000;
    const hour = 60 * minute;
    const day = 24 * hour;
    let value;
    if (abs < minute) {
        value = future ? 'in under a minute' : 'under a minute ago';
        return value;
    }
    if (abs < hour) {
        value = Math.round(abs / minute) + 'm';
    } else if (abs < day) {
        value = Math.round(abs / hour) + 'h';
    } else {
        value = Math.round(abs / day) + 'd';
    }
    return future ? 'in ' + value : value + ' ago';
}

function formatAutoUpdateTimestamp(value) {
    if (!value) return '—';
    try {
        const date = new Date(value);
        if (Number.isNaN(date.getTime())) return value;
        // Go's zero time serializes as year 0001; treat as unset.
        if (date.getFullYear && date.getFullYear() < 1971) return 'Never';
        const localized = date.toLocaleString();
        const rel = describeRelativeFromMs(Date.now() - date.getTime());
        return rel ? localized + ' (' + rel + ')' : localized;
    } catch (err) {
        return value;
    }
}

function formatPolicySourceLabel(source) {
    switch (source) {
        case 'fleet': return 'Fleet Policy';
        case 'local': return 'Local Overrides';
        case 'fallback': return 'Fallback Policy';
        case 'disabled': return 'Policy Disabled';
        case '':
        case undefined:
        case null:
            return 'Agent Defaults';
        default:
            return source.charAt(0).toUpperCase() + source.slice(1);
    }
}

function describeAutoUpdateCopy(status, payload) {
    if (payload && payload.update_available && payload.latest_version) {
        return 'Update ' + payload.latest_version + ' is ready to install.';
    }
    return AUTO_UPDATE_STATUS_COPY[status] || ('Status: ' + formatAutoUpdateStatusLabel(status));
}

function setAutoUpdateMeta(id, value) {
    const el = document.getElementById(id);
    if (!el) return;
    if (value === null || value === undefined || value === '') {
        el.textContent = '—';
    } else {
        el.textContent = value;
    }
}

function setAutoUpdateButtonsDisabled(disabled, reason) {
    ['autoupdate_check_btn', 'autoupdate_force_btn'].forEach(id => {
        const btn = document.getElementById(id);
        if (!btn) return;
        if (btn.dataset.loading === 'true') return;
        btn.disabled = !!disabled;
        if (disabled && reason) {
            btn.title = reason;
        } else {
            btn.removeAttribute('title');
        }
    });
}

async function refreshAutoUpdateStatus(triggeredByButton = false) {
    const panel = document.getElementById('autoupdate_panel');
    if (!panel) return;

    const refreshBtn = triggeredByButton ? document.getElementById('autoupdate_refresh_btn') : null;
    const badge = document.getElementById('autoupdate_status_badge');
    const copy = document.getElementById('autoupdate_status_copy');

    if (refreshBtn) {
        refreshBtn.disabled = true;
        refreshBtn.dataset.loading = 'true';
        refreshBtn.textContent = 'Refreshing...';
    }

    try {
        const resp = await fetch('/api/autoupdate/status', { headers: { 'Accept': 'application/json' } });
        if (!resp.ok) {
            throw new Error('Status ' + resp.status);
        }
        const data = await resp.json();
        applyAutoUpdateStatusToUI(data);
        return data;
    } catch (err) {
        if (badge) {
            badge.textContent = 'Unavailable';
            badge.className = 'autoupdate-status-pill status-error';
        }
        if (copy) {
            copy.textContent = 'Failed to load status: ' + (err && err.message ? err.message : err);
        }
        try {
            if (window.__pm_shared && typeof window.__pm_shared.error === 'function') {
                window.__pm_shared.error('Auto-update status fetch failed', err);
            }
        } catch (_) {}
    } finally {
        if (refreshBtn) {
            refreshBtn.disabled = false;
            refreshBtn.textContent = 'Refresh Status';
            delete refreshBtn.dataset.loading;
        }
        setTimeout(() => { try { applyMasonryLayout(); } catch (_) {} }, 60);
    }
}

function applyAutoUpdateStatusToUI(data) {
    const badge = document.getElementById('autoupdate_status_badge');
    const copy = document.getElementById('autoupdate_status_copy');
    const disabledNotice = document.getElementById('autoupdate_disabled_notice');
    const callout = document.getElementById('autoupdate_available_callout');
    const hint = document.getElementById('autoupdate_policy_hint');

    if (!badge || !copy) return;

    const isEnabled = data && data.enabled;
    const status = data && data.status ? data.status : 'unknown';
    const disabledReason = !isEnabled ? ((data && (data.disabled_reason || data.reason)) || 'Auto-update unavailable') : '';
    const isBusy = AUTO_UPDATE_BUSY_STATUSES.has(status);

    badge.textContent = isEnabled ? formatAutoUpdateStatusLabel(status) : 'Disabled';
    badge.className = 'autoupdate-status-pill ' + (isEnabled ? statusClassForAutoUpdate(status) : 'status-disabled');
    copy.textContent = isEnabled ? describeAutoUpdateCopy(status, data || {}) : disabledReason;

    if (disabledNotice) {
        if (isEnabled) {
            disabledNotice.classList.add('hidden');
            disabledNotice.textContent = '';
        } else {
            disabledNotice.textContent = disabledReason;
            disabledNotice.classList.remove('hidden');
        }
    }

    setAutoUpdateButtonsDisabled(!isEnabled || isBusy, !isEnabled ? disabledReason : (isBusy ? 'Update already running' : ''));

    setAutoUpdateMeta('autoupdate_current_version', data && data.current_version);
    setAutoUpdateMeta('autoupdate_latest_version', data && data.latest_version);

    const channelBits = [];
    if (data && data.channel) channelBits.push(String(data.channel).toUpperCase());
    const platformArch = [data && data.platform, data && data.arch].filter(Boolean).join(' / ');
    if (platformArch) channelBits.push(platformArch);
    setAutoUpdateMeta('autoupdate_channel', channelBits.join(' · ') || null);

    const policyLabel = formatPolicySourceLabel(data && data.policy_source);
    const interval = data && data.check_interval_days ? data.check_interval_days : 0;
    const intervalText = interval > 0 ? ('Every ' + interval + ' day' + (interval === 1 ? '' : 's')) : '';
    setAutoUpdateMeta('autoupdate_policy', intervalText ? (policyLabel + ' · ' + intervalText) : policyLabel);

    setAutoUpdateMeta('autoupdate_last_check', data && data.last_check_at ? formatAutoUpdateTimestamp(data.last_check_at) : null);
    setAutoUpdateMeta('autoupdate_next_check', data && data.next_check_at ? formatAutoUpdateTimestamp(data.next_check_at) : null);

    if (callout) {
        if (isEnabled && data && data.update_available && data.latest_version) {
            callout.textContent = 'Update ' + data.latest_version + ' is available. Click "Update Now" to download and install.';
            callout.classList.remove('hidden');
        } else {
            callout.textContent = '';
            callout.classList.add('hidden');
        }
    }

    if (hint) {
        const prefix = interval > 0 ? ('Automatic checks run every ' + interval + ' day' + (interval === 1 ? '' : 's') + '. ') : '';
        hint.textContent = prefix + 'Manual actions ignore the schedule but still enforce disk space and integrity checks.';
    }

    setTimeout(() => { try { applyMasonryLayout(); } catch (_) {} }, 60);
}

async function handleAutoUpdateCheck(evt) {
    const btn = evt && evt.currentTarget ? evt.currentTarget : null;
    if (!btn) return;

    const previous = btn.textContent;
    btn.disabled = true;
    btn.dataset.loading = 'true';
    btn.textContent = 'Checking...';

    try {
        const resp = await fetch('/api/autoupdate/check', { method: 'POST' });
        const payload = await resp.json().catch(() => ({}));
        if (!resp.ok) {
            // Prefer detailed message over error field, fall back to status text
            const errMsg = (payload && payload.message) || (payload && payload.error) || resp.statusText;
            throw new Error(errMsg);
        }
        if (window.__pm_shared && typeof window.__pm_shared.showToast === 'function') {
            window.__pm_shared.showToast('Update check scheduled', 'success');
        }
    } catch (err) {
        console.error('Update check failed:', err);
        if (window.__pm_shared && typeof window.__pm_shared.showToast === 'function') {
            window.__pm_shared.showToast('Update check failed: ' + (err && err.message ? err.message : err), 'error');
        }
    } finally {
        btn.disabled = false;
        btn.textContent = previous;
        delete btn.dataset.loading;
        await refreshAutoUpdateStatus();
    }
}

async function handleAutoUpdateForce(evt) {
    const btn = evt && evt.currentTarget ? evt.currentTarget : null;
    if (!btn) return;

    // Check if this is a cancel action (during download)
    if (btn.dataset.action === 'cancel') {
        try {
            const resp = await fetch('/api/autoupdate/cancel', { method: 'POST' });
            if (!resp.ok) {
                const payload = await resp.json().catch(() => ({}));
                const errMsg = (payload && payload.message) || (payload && payload.error) || resp.statusText;
                throw new Error(errMsg);
            }
            window.__pm_shared.showToast('Cancel request sent', 'info');
        } catch (err) {
            console.error('Failed to cancel update:', err);
            window.__pm_shared.showToast('Failed to cancel: ' + (err && err.message ? err.message : err), 'error');
        }
        return;
    }

    const confirmed = await window.__pm_shared.showConfirm('Download and install the latest agent update? This restarts the service and may briefly disconnect uploads.', 'Update Now', true);
    if (!confirmed) return;

    const previous = btn.textContent;
    btn.disabled = true;
    btn.dataset.loading = 'true';
    btn.textContent = 'Requesting...';

    try {
        const resp = await fetch('/api/autoupdate/force', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ reason: 'agent_ui_force_reinstall' })
        });
        const payload = await resp.json().catch(() => ({}));
        if (!resp.ok) {
            const errMsg = (payload && payload.message) || (payload && payload.error) || resp.statusText;
            throw new Error(errMsg);
        }
        // Don't reset button here - let SSE progress events update the UI
        if (window.__pm_shared && typeof window.__pm_shared.showToast === 'function') {
            window.__pm_shared.showToast('Update started – progress will be shown in real-time.', 'success');
        }
    } catch (err) {
        console.error('Force reinstall failed:', err);
        if (window.__pm_shared && typeof window.__pm_shared.showToast === 'function') {
            window.__pm_shared.showToast('Update failed: ' + (err && err.message ? err.message : err), 'error');
        }
        // Only reset on error
        btn.disabled = false;
        btn.textContent = previous;
        delete btn.dataset.loading;
    }
    // Note: Don't reset button in finally - let SSE events control the UI state
}

async function handleAutoUpdateRefresh(evt) {
    evt && evt.preventDefault && evt.preventDefault();
    await refreshAutoUpdateStatus(true);
}

function initializeAutoUpdatePanel() {
    const panel = document.getElementById('autoupdate_panel');
    if (!panel) return;

    const refreshBtn = document.getElementById('autoupdate_refresh_btn');
    if (refreshBtn) refreshBtn.addEventListener('click', handleAutoUpdateRefresh);

    const checkBtn = document.getElementById('autoupdate_check_btn');
    if (checkBtn) checkBtn.addEventListener('click', handleAutoUpdateCheck);

    const forceBtn = document.getElementById('autoupdate_force_btn');
    if (forceBtn) forceBtn.addEventListener('click', handleAutoUpdateForce);

    refreshAutoUpdateStatus();
    if (autoUpdateStatusIntervalId) clearInterval(autoUpdateStatusIntervalId);
    autoUpdateStatusIntervalId = setInterval(() => {
        refreshAutoUpdateStatus();
    }, AUTO_UPDATE_STATUS_POLL_MS);
}

// Agent-specific helper: save a discovered device (moved out of shared bundle)
async function saveDiscoveredDevice(ipOrSerial, autosave = false, updateUI = true) {
    if (!ipOrSerial) return;
    const looksLikeIP = ipOrSerial.indexOf('.') !== -1 || ipOrSerial.indexOf(':') !== -1;
    let serial = ipOrSerial;

    if (looksLikeIP) {
        try {
            let list = [];
            try {
                const resp = await fetch('/devices/list');
                if (resp.ok) list = await resp.json();
            } catch (e) {}

            if ((!list || list.length === 0) && document && typeof document.querySelector === 'function') {
                try {
                    const meta = document.querySelector('meta[http-equiv="X-PrintMaster-Agent-ID"]');
                    const agentId = meta && meta.content;
                    if (agentId) {
                        const proxyPath = '/api/v1/proxy/agent/' + encodeURIComponent(agentId) + '/devices/list';
                        const resp2 = await fetch(proxyPath);
                        if (resp2.ok) list = await resp2.json();
                    }
                } catch (e) {}
            }

            // Search saved devices list first
            for (const item of (list || [])) {
                const info = item.printer_info || item;
                const ip = (info.ip || info.IP || '').toString();
                if (ip && ip === ipOrSerial && item.serial) { serial = item.serial; break; }
            }

            // If not found, try discovered devices (they may include serials after a deep scan)
            if (!serial || serial === ipOrSerial) {
                try {
                    let discovered = [];
                    try {
                        const dresp = await fetch('/devices/discovered?include_known=true');
                        if (dresp.ok) discovered = await dresp.json();
                    } catch (e) {}

                    for (const item of (discovered || [])) {
                        const info = item.printer_info || item;
                        const ip = (info.ip || info.IP || '').toString();
                        if (ip && ip === ipOrSerial && item.serial) { serial = item.serial; break; }
                    }
                } catch (e) { window.__pm_shared && window.__pm_shared.warn && window.__pm_shared.warn('Failed to resolve IP from discovered list', e); }
            }
        } catch (e) { window.__pm_shared && window.__pm_shared.warn && window.__pm_shared.warn('Failed to resolve IP to serial for saveDiscoveredDevice', e); }
    }

    if (looksLikeIP && serial === ipOrSerial) {
        window.__pm_shared && window.__pm_shared.warn && window.__pm_shared.warn('saveDiscoveredDevice: could not resolve IP to serial, skipping save for', ipOrSerial);
        return;
    }

    if (!serial) return;

    try {
        const resp = await fetch('/devices/save', {
            method: 'POST', headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ serial: serial })
        });
        if (!resp.ok) {
            let txt = '';
            try { txt = await resp.text(); } catch (e) { txt = resp.statusText || 'unknown'; }
            throw new Error('Failed to save device: ' + txt + ' (status ' + resp.status + ')');
        }
        if (!autosave) window.__pm_shared && window.__pm_shared.showToast && window.__pm_shared.showToast('Device saved', 'success', 1500);
        if (updateUI && typeof updatePrinters === 'function') {
            try { updatePrinters(); } catch (e) { /* best-effort */ }
        }
    } catch (err) {
        window.__pm_shared && window.__pm_shared.error && window.__pm_shared.error('saveDiscoveredDevice failed', err);
        if (!autosave) window.__pm_shared && window.__pm_shared.showToast && window.__pm_shared.showToast('Failed to save device: ' + err.message, 'error');
        throw err;
    }
}

// Save all discovered devices that are not already saved.
// This implements the handler expected by the settings UI button.
async function saveAllDiscovered(evt) {
    const btn = evt && evt.currentTarget ? evt.currentTarget : null;
    const origText = btn && btn.textContent ? btn.textContent : null;
    try {
        // Prevent concurrent Save All operations
        if (window.__pm_saveAllInProgress) {
            window.__pm_shared && window.__pm_shared.showToast && window.__pm_shared.showToast('Save All already in progress', 'info');
            return;
        }
        window.__pm_saveAllInProgress = true;
        if (btn) { btn.disabled = true; btn.textContent = 'Saving...'; }

        // Mark all visible, unsaved devices as saved in a single server-side transaction.
        // (Previously this fired one concurrent /devices/save request per device, which
        // could overwhelm SQLite with parallel writers and trigger SQLITE_BUSY errors.)
        const resp = await fetch('/devices/save/all', { method: 'POST' });
        if (!resp.ok) {
            let txt = '';
            try { txt = await resp.text(); } catch (e) { txt = resp.statusText || 'unknown'; }
            throw new Error('Failed to save all devices: ' + txt + ' (status ' + resp.status + ')');
        }
        const result = await resp.json();
        const savedCount = (result && result.count) || 0;

        if (savedCount > 0) {
            window.__pm_shared && window.__pm_shared.showToast && window.__pm_shared.showToast('Saved ' + savedCount + ' devices', 'success', 2000);
        } else {
            window.__pm_shared && window.__pm_shared.showToast && window.__pm_shared.showToast('No discovered devices to save', 'info', 1500);
        }

        try { if (typeof updatePrinters === 'function') updatePrinters(); } catch (e) {}
    } catch (err) {
        window.__pm_shared && window.__pm_shared.error && window.__pm_shared.error('saveAllDiscovered failed', err);
        window.__pm_shared && window.__pm_shared.showToast && window.__pm_shared.showToast('Failed to save discovered devices: ' + (err && err.message ? err.message : err), 'error');
    } finally {
        window.__pm_saveAllInProgress = false;
        if (btn) { btn.disabled = false; if (origText) btn.textContent = origText; }
    }
}

// Expose agent implementation so shared delegate can call into it when proxied
try { window.__agent_saveDiscoveredDevice = window.__agent_saveDiscoveredDevice || saveDiscoveredDevice; } catch (e) {}

// Clear (delete) all discovered devices from the local DB
async function clearDiscovered(evt) {
    const btn = evt && evt.currentTarget ? evt.currentTarget : null;
    const origText = btn && btn.textContent ? btn.textContent : null;

    try {
        // Confirm destructive action using shared confirm modal (avoid native confirm)
        const confirmed = await window.__pm_shared.showConfirm('Delete all discovered devices from the database? This cannot be undone.', 'Delete discovered devices', true);
        if (!confirmed) return;

        if (btn) { btn.disabled = true; btn.textContent = 'Clearing...'; }

        const resp = await fetch('/devices/clear_discovered', { method: 'POST' });
        if (!resp.ok) {
            let txt = '';
            try { txt = await resp.text(); } catch (e) { txt = resp.statusText || 'unknown'; }
            throw new Error('Failed to clear discovered devices: ' + txt + ' (status ' + resp.status + ')');
        }

        // Show feedback and refresh UI
        window.__pm_shared && window.__pm_shared.showToast && window.__pm_shared.showToast('Cleared discovered devices', 'success', 1500);
        try { if (typeof updatePrinters === 'function') updatePrinters(); } catch (e) {}
    } catch (err) {
        window.__pm_shared && window.__pm_shared.error && window.__pm_shared.error('clearDiscovered failed', err);
        window.__pm_shared && window.__pm_shared.showToast && window.__pm_shared.showToast('Failed to clear discovered devices: ' + (err && err.message ? err.message : err), 'error');
    } finally {
        if (btn) { btn.disabled = false; if (origText) btn.textContent = origText; }
    }
}

// NOTE: The canonical printer-details renderer lives in
// `common/web/cards.js` as `window.__pm_shared_cards.showPrinterDetailsData`.
// We no longer provide a local wrapper here to avoid duplication and
// indirection — callers should call the shared renderer directly.

// Update the discovered and saved printers UI by querying the backend and
// rendering cards using the shared card renderers in `common/web/cards.js`.
async function fetchInventoryList(url, section) {
    try {
        const response = await fetch(url);
        if (!response.ok) throw new Error('Request failed (' + response.status + ')');
        const data = await response.json();
        if (!Array.isArray(data)) throw new Error('Invalid inventory response');
        return data;
    } catch (error) {
        error.inventorySection = section;
        throw error;
    }
}

function showInventoryLoadError(container, error) {
    if (!container) return;
    let panel = container.querySelector('[data-inventory-load-error]');
    if (!panel) {
        panel = document.createElement('div');
        panel.dataset.inventoryLoadError = '';
        panel.style.cssText = 'color:var(--error,#b42318);padding:12px;';
        const message = document.createElement('span');
        message.dataset.inventoryErrorMessage = '';
        panel.appendChild(message);
        const retry = document.createElement('button');
        retry.type = 'button';
        retry.textContent = 'Retry';
        retry.addEventListener('click', updatePrinters);
        panel.appendChild(retry);
        container.appendChild(panel);
    }
    panel.querySelector('[data-inventory-error-message]').textContent =
        'Failed to load inventory: ' + error.message + '. ';
}

function hasLowToner(printer) {
    const getTonerData = window.__pm_shared_cards && window.__pm_shared_cards.getDeviceTonerBarData;
    if (typeof getTonerData !== 'function') return false;
    return getTonerData(printer).some(supply => Number.isFinite(supply.level) && supply.level < 20);
}

function updatePrinters() {
    try {
        const showKnownDevices = document.getElementById('show_saved_in_discovered')?.checked || false;

        // Always fetch all discovered devices (no time filter)
        let discoveredEndpoint = '/devices/discovered?include_known=' + showKnownDevices;

        Promise.all([
            fetchInventoryList(discoveredEndpoint, 'discovered'),
            fetchInventoryList('/devices/list', 'saved')
        ]).then(([discovered, saved]) => {
            window.discoveredPrinters = discovered || [];

            const discoveredContainer = document.getElementById('discovered_devices_cards');
            if (!discoveredContainer) return;
            discoveredContainer.querySelector('[data-inventory-load-error]')?.remove();

                // If any card is currently animating (saving/removing), defer
                // this re-render so we don't interrupt exit animations. SSE or
                // other async updates may call updatePrinters frequently; it's
                // better to wait a short time and retry than to forcibly
                // replace innerHTML while a visual transition is in progress.
                try {
                    const animating = discoveredContainer.querySelector('.saving, .removing');
                    if (animating) {
                        // schedule a retry shortly after expected animation end
                        setTimeout(() => { try { updatePrinters(); } catch (e) {} }, 500);
                        return;
                    }
                } catch (e) {}

            const savedSerials = new Set();
            const savedIPs = new Set();
            if (Array.isArray(saved)) {
                saved.forEach(item => {
                    const p = item.printer_info || {};
                    const serial = item.serial || '';
                    const ip = p.ip || '';
                    if (serial) savedSerials.add(serial);
                    if (ip) savedIPs.add(ip);
                });
            }

            // Autosave/display controls
            const autosaveEnabled = document.getElementById('autosave_checkbox')?.checked || false;
            const showDiscoveredAnyway = document.getElementById('show_discovered_devices_anyway')?.checked || false;
            const discoveredSection = document.getElementById('discovered_section');
            if (discoveredSection) {
                const shouldShowDiscovered = !autosaveEnabled || (autosaveEnabled && showDiscoveredAnyway);
                discoveredSection.style.display = shouldShowDiscovered ? 'block' : 'none';
            }

            // Render discovered
            if (!Array.isArray(discovered) || discovered.length === 0) {
                discoveredContainer.innerHTML = '<div style="color:var(--muted);padding:12px">No discovered printers</div>';
                const statsEl = document.getElementById('discovered_stats'); if (statsEl) statsEl.innerHTML = '';
            } else {
                let lowTonerCount = 0;
                discovered.forEach(p => { if (hasLowToner(p)) lowTonerCount++; });
                const statsHtml = '<span style="color:var(--text)"><strong>Total:</strong> ' + discovered.length + '</span>' +
                    '<span style="color:#b58900"><strong>Low Toner:</strong> ' + lowTonerCount + '</span>';
                const statsEl = document.getElementById('discovered_stats'); if (statsEl) statsEl.innerHTML = statsHtml;

                let cardsHTML = '';
                discovered.forEach(p => {
                    const isSaved = (p.serial && savedSerials.has(p.serial)) || (p.ip && savedIPs.has(p.ip));
                    cardsHTML += window.__pm_shared_cards.renderDiscoveredCard(p, isSaved);
                });
                discoveredContainer.innerHTML = cardsHTML;
                // Normalize any inline onclick handlers produced by renderers into
                // programmatic event listeners so behavior matches the saved-card
                // buttons (which use direct .onclick handlers). This helps keep
                // a single wiring strategy across the UI and avoids relying on
                // inline JS in generated HTML.
                // Buttons in discovered cards are wired via delegated handlers in shared cards
                // (`cards.js`) so no runtime normalization is required here.
            }

            // Autosave new discovered devices if enabled
            // Works for both network devices (by IP) and USB/spooler devices (by serial)
            if (autosaveEnabled && Array.isArray(discovered) && discovered.length > 0) {
                if (!window.autosavedSerials) window.autosavedSerials = new Set();
                discovered.forEach(p => {
                    const isSaved = (p.serial && savedSerials.has(p.serial)) || (p.ip && savedIPs.has(p.ip));
                    // Use serial as the identifier (works for USB devices without IP)
                    // Falls back to IP for legacy network devices without serial
                    const identifier = p.serial || p.ip;
                    if (identifier && !isSaved && !window.autosavedSerials.has(identifier)) {
                        window.autosavedSerials.add(identifier);
                        window.__pm_shared.saveDiscoveredDevice(identifier, true).catch(() => { window.autosavedSerials.delete(identifier); });
                    }
                });
            }

        }).catch(e => {
            window.__pm_shared.error('updatePrinters discovered error', e);
            const targetId = e.inventorySection === 'saved' ? 'saved_devices_cards' : 'discovered_devices_cards';
            showInventoryLoadError(document.getElementById(targetId), e);
        });

        // Render saved devices
        fetchInventoryList('/devices/list', 'saved').then(saved => {
            const savedContainer = document.getElementById('saved_devices_cards');
            if (!savedContainer) return;
            savedContainer.querySelector('[data-inventory-load-error]')?.remove();

            if (!Array.isArray(saved) || saved.length === 0) {
                savedContainer.innerHTML = '<div style="color:var(--muted);padding:12px">No saved devices</div>';
                const statsEl = document.getElementById('saved_stats'); if (statsEl) statsEl.innerHTML = '';
            } else {
                let lowTonerCount = 0;
                saved.forEach(item => { if (hasLowToner(item.printer_info || {})) lowTonerCount++; });
                const statsHtml = '<span style="color:var(--text)"><strong>Total:</strong> ' + saved.length + '</span>' +
                    '<span style="color:#b58900"><strong>Low Toner:</strong> ' + lowTonerCount + '</span>';
                const statsEl = document.getElementById('saved_stats'); if (statsEl) statsEl.innerHTML = statsHtml;

                const existingCards = Array.from(savedContainer.querySelectorAll('.saved-device-card'));
                const existingKeys = new Set(existingCards.map(c => c.dataset.deviceKey));
                const isInitialLoad = existingCards.length === 0;

                if (isInitialLoad) {
                    let cardsHTML = '';
                    saved.forEach(item => { cardsHTML += window.__pm_shared_cards.renderSavedCard(item); });
                    // If any saved card is animating, defer replacing the
                    // entire saved container to avoid cutting animations short.
                    try {
                        const animatingSaved = savedContainer.querySelector('.saving, .removing');
                        if (animatingSaved) {
                            setTimeout(() => { try { savedContainer.innerHTML = cardsHTML; } catch (e) {} }, 500);
                        } else {
                            savedContainer.innerHTML = cardsHTML;
                        }
                    } catch (e) { savedContainer.innerHTML = cardsHTML; }
                    // Ensure saved card inline handlers are normalized to programmatic
                    // listeners as well so Delete/Details/other actions behave the
                    // same across discovered and saved lists.
                    // Saved cards use data-action attributes and delegated handlers
                    // implemented in `common/web/cards.js` — no runtime rewrite needed.
                    saved.forEach(item => { if (item.serial) window.__pm_shared_metrics.loadUsageGraph(item.serial); });
                } else {
                    saved.forEach(item => {
                        const deviceKey = item.serial || (item.printer_info && item.printer_info.ip) || '';
                        if (!deviceKey) return;
                        if (!existingKeys.has(deviceKey)) {
                            const tempDiv = document.createElement('div');
                            tempDiv.innerHTML = window.__pm_shared_cards.renderSavedCard(item);
                            const newCard = tempDiv.firstElementChild;
                            if (newCard) savedContainer.appendChild(newCard);
                            requestAnimationFrame(() => { if (newCard) newCard.classList.add('card-entering'); });
                            if (item.serial) window.__pm_shared_metrics.loadUsageGraph(item.serial);
                        }
                    });

                    const savedKeys = new Set(saved.map(item => item.serial || (item.printer_info && item.printer_info.ip) || ''));
                    existingCards.forEach(card => {
                        const key = card.dataset.deviceKey;
                        if (key && !savedKeys.has(key)) { card.classList.add('removing'); setTimeout(() => card.remove(), 400); }
                    });
                }
            }
            
            // Also update table view if visible
            updateSavedDevicesTable(saved);
            
            // Update manufacturer filter options
            updateManufacturerFilter(saved);
        }).catch(e => {
            window.__pm_shared.error('updatePrinters saved error', e);
            showInventoryLoadError(document.getElementById('saved_devices_cards'), e);
        });

    } catch (e) {
        window.__pm_shared.error('updatePrinters failed', e);
    }
}

// ============================================
// DEVICES TAB - View Model and Filtering
// ============================================

const devicesVM = {
    view: 'cards', // 'cards' or 'table'
    filters: {
        search: '',
        manufacturer: '',
        sortKey: 'last_seen',
        sortDir: 'desc',
        consumableBands: ['critical', 'low', 'medium', 'high', 'unknown']
    },
    allDevices: []
};

// Store devices for filtering
function updateManufacturerFilter(devices) {
    devicesVM.allDevices = devices || [];
    const select = document.getElementById('devices_manufacturer_filter');
    if (!select) return;
    
    const manufacturers = new Set();
    devices.forEach(item => {
        const p = item.printer_info || {};
        if (p.manufacturer) manufacturers.add(p.manufacturer);
    });
    
    const current = select.value;
    select.innerHTML = '<option value="">All Manufacturers</option>';
    Array.from(manufacturers).sort().forEach(m => {
        const opt = document.createElement('option');
        opt.value = m;
        opt.textContent = m;
        select.appendChild(opt);
    });
    select.value = current;
}

// Set devices view (cards or table)
function setDevicesView(view) {
    devicesVM.view = view;
    
    const cardsContainer = document.getElementById('saved_devices_cards');
    const tableWrapper = document.getElementById('saved_devices_table_wrapper');
    const toggle = document.getElementById('devices_view_toggle');
    
    if (view === 'table') {
        if (cardsContainer) cardsContainer.classList.add('hidden');
        if (tableWrapper) tableWrapper.classList.remove('hidden');
    } else {
        if (cardsContainer) cardsContainer.classList.remove('hidden');
        if (tableWrapper) tableWrapper.classList.add('hidden');
    }
    
    if (toggle) {
        toggle.querySelectorAll('.ghost-btn').forEach(btn => {
            btn.classList.toggle('active', btn.getAttribute('data-view') === view);
        });
    }
    
    // Refresh the table if switching to table view
    if (view === 'table') {
        updateSavedDevicesTable(devicesVM.allDevices);
    }
    
    localStorage.setItem('pm_agent_devices_view', view);
}

// Render saved devices table
function updateSavedDevicesTable(devices) {
    const tbody = document.querySelector('#saved_devices_table tbody');
    if (!tbody) return;
    
    if (!Array.isArray(devices) || devices.length === 0) {
        tbody.innerHTML = '<tr><td colspan="6" class="muted-text">No saved devices</td></tr>';
        return;
    }
    
    // Apply filters
    let filtered = filterDevices(devices);
    
    // Sort
    filtered = sortDevices(filtered);
    
    let html = '';
    filtered.forEach(item => {
        const p = item.printer_info || {};
        const serial = item.serial || '';
        const lifeCount = p.page_count || p.total_mono_impressions || 0;
        const webUIUrl = item.web_ui_url || (item.is_usb && serial ? '/proxy/' + encodeURIComponent(serial) + '/' : '');
        
        // Build consumables bars (vertical progress indicators) - shared with server UI
        const consumablesHtml = window.__pm_shared_cards.renderTonerBars(window.__pm_shared_cards.getDeviceTonerBarData(p));
        
        html += '<tr class="device-row-clickable" data-serial="' + serial + '" data-ip="' + (p.ip || '') + '">';
        html += '<td><div class="table-device-info"><span class="table-device-name">' + escapeHtml(p.manufacturer || 'Unknown') + ' ' + escapeHtml(p.model || '') + '</span>';
        html += '<span class="table-device-serial">' + escapeHtml(serial) + '</span></div></td>';
        html += '<td>' + consumablesHtml + '</td>';
        html += '<td><span style="font-family:monospace;font-size:12px">' + escapeHtml(p.ip || 'N/A') + '</span></td>';
        html += '<td>' + escapeHtml(item.location || '—') + '</td>';
        html += '<td style="text-align:right;font-family:monospace">' + (lifeCount || 0).toLocaleString() + '</td>';
        html += '<td class="actions-col"><div class="table-actions">';
        if (webUIUrl) {
            html += '<button data-action="webui" data-webui-url="' + webUIUrl + '" data-serial="' + serial + '">WebUI</button>';
        }
        html += '<button data-action="details" data-serial="' + serial + '" data-source="saved">Details</button>';
        html += '<button class="delete" data-action="delete" data-serial="' + serial + '">Delete</button>';
        html += '</div></td>';
        html += '</tr>';
    });
    
    tbody.innerHTML = html;
}

// Filter devices based on current filter state
function filterDevices(devices) {
    return devices.filter(item => {
        const p = item.printer_info || {};
        
        // Search filter
        if (devicesVM.filters.search) {
            const search = devicesVM.filters.search.toLowerCase();
            const searchFields = [
                item.serial,
                p.ip,
                p.manufacturer,
                p.model,
                item.location,
                item.asset_number
            ].filter(Boolean).map(s => s.toLowerCase());
            if (!searchFields.some(f => f.includes(search))) return false;
        }
        
        // Manufacturer filter
        if (devicesVM.filters.manufacturer && p.manufacturer !== devicesVM.filters.manufacturer) {
            return false;
        }
        
        // Consumable band filter
        const toners = window.__pm_shared_cards.buildTonerLevels(p) || {};
        const levels = Object.values(toners).filter(v => typeof v === 'number');
        let band = 'unknown';
        if (levels.length > 0) {
            const minLevel = Math.min(...levels);
            if (minLevel <= 10) band = 'critical';
            else if (minLevel <= 25) band = 'low';
            else if (minLevel <= 50) band = 'medium';
            else band = 'high';
        }
        if (!devicesVM.filters.consumableBands.includes(band)) {
            return false;
        }
        
        return true;
    });
}

// Sort devices
function sortDevices(devices) {
    const key = devicesVM.filters.sortKey;
    const dir = devicesVM.filters.sortDir === 'asc' ? 1 : -1;
    
    return [...devices].sort((a, b) => {
        const pa = a.printer_info || {};
        const pb = b.printer_info || {};
        
        let va, vb;
        switch (key) {
            case 'manufacturer':
                va = (pa.manufacturer || '').toLowerCase();
                vb = (pb.manufacturer || '').toLowerCase();
                break;
            case 'model':
                va = (pa.model || '').toLowerCase();
                vb = (pb.model || '').toLowerCase();
                break;
            case 'ip':
                va = pa.ip || '';
                vb = pb.ip || '';
                break;
            case 'location':
                va = (a.location || '').toLowerCase();
                vb = (b.location || '').toLowerCase();
                break;
            case 'pages':
                va = pa.page_count || 0;
                vb = pb.page_count || 0;
                break;
            case 'last_seen':
            default:
                va = a.last_seen || a.updated_at || '';
                vb = b.last_seen || b.updated_at || '';
                break;
        }
        
        if (va < vb) return -1 * dir;
        if (va > vb) return 1 * dir;
        return 0;
    });
}

// Set sort
function setDeviceSort(key, dir) {
    devicesVM.filters.sortKey = key;
    devicesVM.filters.sortDir = dir;
    
    const select = document.getElementById('devices_sort_select');
    const dirIcon = document.getElementById('devices_sort_dir_icon');
    
    if (select) select.value = key;
    if (dirIcon) dirIcon.textContent = dir === 'asc' ? '↑' : '↓';
    
    // Refresh views
    applyDevicesFilters();
}

// Toggle consumable band filter
function toggleConsumableFilter(band) {
    const idx = devicesVM.filters.consumableBands.indexOf(band);
    if (idx >= 0) {
        devicesVM.filters.consumableBands.splice(idx, 1);
    } else {
        devicesVM.filters.consumableBands.push(band);
    }
    
    // Update pill UI
    const container = document.getElementById('devices_consumable_filter');
    if (container) {
        container.querySelectorAll('.pill').forEach(pill => {
            const pillBand = pill.getAttribute('data-band');
            pill.classList.toggle('active', devicesVM.filters.consumableBands.includes(pillBand));
        });
    }
    
    updateActiveFiltersChips();
    applyDevicesFilters();
}

// Reset all filters
function resetDeviceFilters() {
    devicesVM.filters.search = '';
    devicesVM.filters.manufacturer = '';
    devicesVM.filters.consumableBands = ['critical', 'low', 'medium', 'high', 'unknown'];
    
    const searchInput = document.getElementById('devices_search');
    const mfgSelect = document.getElementById('devices_manufacturer_filter');
    const consumableFilter = document.getElementById('devices_consumable_filter');
    
    if (searchInput) searchInput.value = '';
    if (mfgSelect) mfgSelect.value = '';
    if (consumableFilter) {
        consumableFilter.querySelectorAll('.pill').forEach(p => p.classList.add('active'));
    }
    
    updateActiveFiltersChips();
    applyDevicesFilters();
}

// Update active filters chips display
function updateActiveFiltersChips() {
    const container = document.getElementById('devices_active_filters');
    if (!container) return;
    
    let html = '';
    
    if (devicesVM.filters.search) {
        html += '<button class="filter-chip" data-filter="search">Search: ' + escapeHtml(devicesVM.filters.search) + '</button>';
    }
    if (devicesVM.filters.manufacturer) {
        html += '<button class="filter-chip" data-filter="manufacturer">' + escapeHtml(devicesVM.filters.manufacturer) + '</button>';
    }
    
    const allBands = ['critical', 'low', 'medium', 'high', 'unknown'];
    const excludedBands = allBands.filter(b => !devicesVM.filters.consumableBands.includes(b));
    excludedBands.forEach(band => {
        html += '<button class="filter-chip" data-filter="band-' + band + '">Not ' + band + '</button>';
    });
    
    container.innerHTML = html;
}

// Handle filter chip removal
function handleFilterChipRemove(filter) {
    if (filter === 'search') {
        devicesVM.filters.search = '';
        const input = document.getElementById('devices_search');
        if (input) input.value = '';
    } else if (filter === 'manufacturer') {
        devicesVM.filters.manufacturer = '';
        const select = document.getElementById('devices_manufacturer_filter');
        if (select) select.value = '';
    } else if (filter.startsWith('band-')) {
        const band = filter.replace('band-', '');
        if (!devicesVM.filters.consumableBands.includes(band)) {
            devicesVM.filters.consumableBands.push(band);
            const container = document.getElementById('devices_consumable_filter');
            if (container) {
                const pill = container.querySelector('[data-band="' + band + '"]');
                if (pill) pill.classList.add('active');
            }
        }
    }
    
    updateActiveFiltersChips();
    applyDevicesFilters();
}

// Apply filters to both card and table views
function applyDevicesFilters() {
    // Filter cards
    const cardsContainer = document.getElementById('saved_devices_cards');
    if (cardsContainer) {
        const cards = cardsContainer.querySelectorAll('.saved-device-card');
        cards.forEach(card => {
            const item = {
                serial: card.dataset.serial || '',
                location: '', // Would need to store this in data attribute
                printer_info: {
                    manufacturer: card.dataset.make || '',
                    model: card.dataset.model || '',
                    ip: card.dataset.ip || ''
                }
            };
            
            // Check search filter
            let visible = true;
            if (devicesVM.filters.search) {
                const search = devicesVM.filters.search.toLowerCase();
                const searchFields = [item.serial, item.printer_info.ip, item.printer_info.manufacturer, item.printer_info.model].filter(Boolean).map(s => s.toLowerCase());
                if (!searchFields.some(f => f.includes(search))) visible = false;
            }
            
            // Check manufacturer filter
            if (visible && devicesVM.filters.manufacturer && item.printer_info.manufacturer !== devicesVM.filters.manufacturer) {
                visible = false;
            }
            
            card.style.display = visible ? '' : 'none';
        });
    }
    
    // Update table
    updateSavedDevicesTable(devicesVM.allDevices);
}

// Initialize devices tab controls
function initDevicesTabControls() {
    // Sidebar toggle
    const sidebarToggle = document.getElementById('devices_sidebar_toggle');
    const sidebar = document.getElementById('devices_sidebar');
    if (sidebarToggle && sidebar) {
        sidebarToggle.addEventListener('click', () => {
            sidebar.classList.toggle('collapsed');
            localStorage.setItem('pm_agent_devices_sidebar_collapsed', sidebar.classList.contains('collapsed'));
        });
        // Restore collapsed state
        if (localStorage.getItem('pm_agent_devices_sidebar_collapsed') === 'true') {
            sidebar.classList.add('collapsed');
        }
    }
    
    // Search input
    const searchInput = document.getElementById('devices_search');
    if (searchInput) {
        searchInput.addEventListener('input', (e) => {
            devicesVM.filters.search = e.target.value;
            updateActiveFiltersChips();
            applyDevicesFilters();
        });
    }
    
    // Manufacturer filter
    const mfgFilter = document.getElementById('devices_manufacturer_filter');
    if (mfgFilter) {
        mfgFilter.addEventListener('change', (e) => {
            devicesVM.filters.manufacturer = e.target.value;
            updateActiveFiltersChips();
            applyDevicesFilters();
        });
    }
    
    // Sort select
    const sortSelect = document.getElementById('devices_sort_select');
    if (sortSelect) {
        sortSelect.addEventListener('change', (e) => {
            setDeviceSort(e.target.value, devicesVM.filters.sortDir);
        });
    }
    
    // Sort direction button
    const sortDirBtn = document.getElementById('devices_sort_dir_btn');
    if (sortDirBtn) {
        sortDirBtn.addEventListener('click', () => {
            const nextDir = devicesVM.filters.sortDir === 'asc' ? 'desc' : 'asc';
            setDeviceSort(devicesVM.filters.sortKey, nextDir);
        });
    }
    
    // View toggle
    const viewToggle = document.getElementById('devices_view_toggle');
    if (viewToggle) {
        viewToggle.addEventListener('click', (e) => {
            const btn = e.target.closest('[data-view]');
            if (btn) setDevicesView(btn.getAttribute('data-view'));
        });
    }
    
    // Consumable filter pills
    const consumableFilter = document.getElementById('devices_consumable_filter');
    if (consumableFilter) {
        consumableFilter.addEventListener('click', (e) => {
            const pill = e.target.closest('[data-band]');
            if (pill) toggleConsumableFilter(pill.getAttribute('data-band'));
        });
    }
    
    // Reset filters button
    const resetBtn = document.getElementById('devices_reset_filters');
    if (resetBtn) {
        resetBtn.addEventListener('click', resetDeviceFilters);
    }
    
    // Filter chips (click to remove)
    const chipsContainer = document.getElementById('devices_active_filters');
    if (chipsContainer) {
        chipsContainer.addEventListener('click', (e) => {
            const chip = e.target.closest('[data-filter]');
            if (chip) handleFilterChipRemove(chip.getAttribute('data-filter'));
        });
    }
    
    // Table header sort
    const tableHead = document.querySelector('#saved_devices_table thead');
    if (tableHead) {
        tableHead.addEventListener('click', (e) => {
            const th = e.target.closest('[data-sort-key]');
            if (!th) return;
            const key = th.getAttribute('data-sort-key');
            const newDir = devicesVM.filters.sortKey === key && devicesVM.filters.sortDir === 'desc' ? 'asc' : 'desc';
            setDeviceSort(key, newDir);
        });
    }
    
    // Table row click for details
    const tableBody = document.querySelector('#saved_devices_table tbody');
    if (tableBody) {
        tableBody.addEventListener('click', (e) => {
            // Don't trigger on button clicks
            if (e.target.closest('button') || e.target.closest('.table-actions')) return;
            const row = e.target.closest('tr[data-serial]');
            if (row) {
                const serial = row.getAttribute('data-serial');
                if (serial) window.__pm_shared.showPrinterDetails(serial, 'saved');
            }
        });
    }
    
    // Restore view preference
    const savedView = localStorage.getItem('pm_agent_devices_view');
    if (savedView === 'table') {
        setDevicesView('table');
    }
}

// Simple HTML escape
function escapeHtml(str) {
    if (!str) return '';
    return String(str).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}

// Toner color mapping and toner-bar rendering now live in common/web/cards.js
// (window.__pm_shared_cards.getTonerColor / renderTonerBars) so agent and
// server share identical coloring and mono/color filtering behavior.

// Database backend field toggles are provided by the shared bundle
// (common/web/shared.js) and exported as window.__pm_shared.toggleDatabaseFields.
// We intentionally do not keep a local copy here to avoid duplication.

// Clear entire database (backup and reset)
async function clearDatabase() {
    const confirmed = await window.__pm_shared.showConfirm(
        'This will backup the current database and start fresh. All saved devices, metrics, and history will be moved to a backup file. Continue?',
        'Clear Database',
        true
    );

    if (!confirmed) return;
    
    try {
        const r = await fetch('/database/clear', { method: 'POST' });
        if (!r.ok) throw new Error('Database clear failed');
        const result = await r.json();
        if (result.reload) {
            window.__pm_shared.showToast('Database backed up and reset successfully. Reloading...', 'success');
            setTimeout(() => window.location.reload(), 1500);
        }
    } catch (e) {
        window.__pm_shared.error('Database clear failed:', e);
        window.__pm_shared.showToast('Database clear failed: ' + e.message, 'error');
    }
}

// Show saved device details in modal
async function showSavedDeviceDetails(serial) {
    if (!serial) return;
    try {
        // Delegate to the shared serial-resolving wrapper. That wrapper will
        // prefer server-side data when available and fall back to the agent
        // local DB otherwise. Using the shared function keeps behavior
        // consistent across Agent and Server UIs.
        if (window.__pm_shared && typeof window.__pm_shared.showPrinterDetails === 'function') {
            window.__pm_shared.showPrinterDetails(serial, 'saved');
        } else {
            // Fallback: try agent-local canonical profile endpoint directly
            const r = await fetch('/api/devices/profile?serial=' + encodeURIComponent(serial));
            if (!r.ok) throw new Error('Device not found');
            const profile = await r.json();
            const device = (profile && profile.device) ? profile.device : profile;
            window.__pm_shared_cards.showPrinterDetailsData(device, 'saved');
        }
    } catch (e) {
        window.__pm_shared.showToast('Failed to load device: ' + e.message, 'error');
    }
}

// Delete saved device
async function deleteSavedDevice(serial) {
    if (!serial) return;
    const confirmed = await window.__pm_shared.showConfirm(
        'Delete this device? This will remove it from the database but it may be re-discovered if still on the network.',
        'Delete Device',
        true
    );
    if (!confirmed) return;
    
    try {
        const r = await fetch('/devices/delete', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ serial: serial }) });
        if (!r.ok) throw new Error('Delete failed');
        window.__pm_shared.showToast('Device deleted successfully', 'success');
        // Animate removal of the saved-device card to avoid an abrupt snap.
        try {
            const card = document.querySelector('.saved-device-card[data-serial="' + serial + '"]') || document.querySelector('.saved-device-card[data-device-key="' + serial + '"]');
            if (card) {
                card.classList.add('removing');
                let handled = false;
                const onEnd = () => {
                    if (handled) return; handled = true;
                    try { card.remove(); } catch (e) {}
                    try { if (typeof updatePrinters === 'function') updatePrinters(); } catch (e) {}
                    card.removeEventListener('animationend', onEnd);
                    card.removeEventListener('transitionend', onEnd);
                };
                card.addEventListener('animationend', onEnd);
                card.addEventListener('transitionend', onEnd);
                // safety fallback
                setTimeout(onEnd, 800);
            } else {
                try { if (typeof updatePrinters === 'function') updatePrinters(); } catch (e) {}
            }
        } catch (e) { try { if (typeof updatePrinters === 'function') updatePrinters(); } catch (er) {} }
    } catch (e) {
    window.__pm_shared.error('Delete failed:', e);
    window.__pm_shared.showToast('Delete failed: ' + e.message, 'error');
    }
}

// Edit a device field inline (Asset Number, Location)
async function editField(serial, fieldName, currentValue, element) {
    if (!serial || !fieldName) return;

    const displayName = fieldName === 'asset_number' ? 'Asset Number' : 'Location';
    const newValue = await window.__pm_shared.showPrompt('Enter new ' + displayName + ':', currentValue || '');

    if (newValue === null) return; // User cancelled

    try {
        const body = { serial: serial };
        body[fieldName] = newValue;

        const r = await fetch('/devices/update', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(body)
        });

        if (!r.ok) throw new Error('Update failed');

        // Update the display
        element.textContent = newValue;
        element.onclick = function () { editField(serial, fieldName, newValue, element); };
        window.__pm_shared.showToast(`${displayName} updated successfully`, 'success');

    } catch (e) {
        window.__pm_shared.error('Update failed:', e);
        window.__pm_shared.showToast('Update failed: ' + e.message, 'error');
    }
}

// Delete all saved devices
async function deleteAllSavedDevices() {
    try {
        // Get list of saved devices
        const r = await fetch('/devices/list');
        if (!r.ok) throw new Error('Failed to fetch device list');
        const saved = await r.json();

        if (!Array.isArray(saved) || saved.length === 0) {
            window.__pm_shared.showToast('No saved devices to delete', 'info');
            return;
        }

        // Confirm deletion
        const confirmed = await window.__pm_shared.showConfirm(
            `Delete all ${saved.length} saved device(s)? This cannot be undone.`,
            'Delete All Devices',
            true
        );
        if (!confirmed) return;

        // Delete each device
        let deleted = 0;
        let failed = 0;
        for (const item of saved) {
            const serial = item.serial || (item.info && item.info.serial) || (item.printer_info && item.printer_info.serial);
            if (!serial) continue;

            try {
                const delResp = await fetch('/devices/delete', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ serial: serial }) });
                if (delResp.ok) {
                    deleted++;
                } else {
                    failed++;
                }
            } catch (e) {
                failed++;
                window.__pm_shared.error('Failed to delete device ' + serial + ':', e);
            }
        }

        updatePrinters();
        window.__pm_shared.log('Delete complete: ' + deleted + ' deleted, ' + failed + ' failed');
        if (deleted > 0) {
            window.__pm_shared.showToast(`Deleted ${deleted} device${deleted !== 1 ? 's' : ''} successfully`, 'success');
        }
        if (failed > 0) {
            window.__pm_shared.showToast(`Failed to delete ${failed} device${failed !== 1 ? 's' : ''}`, 'error');
        }
    } catch (e) {
    window.__pm_shared.error('Delete all failed:', e);
    window.__pm_shared.showToast('Delete all failed: ' + e.message, 'error');
    }
}

// Store all log entries for client-side filtering
let allLogEntries = [];
let parsedLogEntries = []; // Parsed log entries for table view
let activeLogViewMode = localStorage.getItem('pm_agent_log_view_mode') || 'table';

// Parse and colorize a log line
function colorizeLogLine(line) {
    const levelColors = {
        'ERROR': '#ef4444',
        'WARN': '#f59e0b',
        'INFO': '#93a1a1',
        'DEBUG': '#6b7280',
        'TRACE': '#4b5563'
    };
    
    // Match log format: timestamp [LEVEL] message
    const match = line.match(/^(\S+)\s+\[(\w+)\]\s+(.*)$/);
    if (match) {
        const [, timestamp, level, message] = match;
        const color = levelColors[level] || '#93a1a1';
        return `<span style="color:#586e75">${timestamp}</span> <span style="color:${color};font-weight:bold">[${level}]</span> <span style="color:${color}">${message}</span>`;
    }
    return line;
}

/**
 * Parse a single log line into structured components
 * Format: 2006-01-02T15:04:05-07:00 [LEVEL] message key=value key=value
 */
function parseLogLine(line) {
    if (!line || typeof line !== 'string') {
        return { raw: String(line || ''), timestamp: null, level: '', message: line || '', context: {} };
    }

    const entry = { raw: line, timestamp: null, level: '', message: '', context: {} };

    // Match timestamp at start: ISO 8601 format
    let remaining = line;
    const timestampMatch = line.match(/^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:[+-]\d{2}:\d{2}|Z)?)\s*/);
    if (timestampMatch) {
        try {
            entry.timestamp = new Date(timestampMatch[1]);
        } catch (e) {
            entry.timestamp = null;
        }
        remaining = line.slice(timestampMatch[0].length);
    }

    // Match level in brackets: [ERROR], [WARN], [INFO], [DEBUG], [TRACE]
    const levelMatch = remaining.match(/^\[(\w+)\]\s*/);
    if (levelMatch) {
        entry.level = levelMatch[1].toUpperCase();
        remaining = remaining.slice(levelMatch[0].length);
    }

    // Extract key=value context pairs from the end
    const contextPairs = [];
    const kvPattern = /\s+(\w+)=("[^"]*"|\S+)$/;
    let match;
    while ((match = remaining.match(kvPattern)) !== null) {
        let value = match[2];
        if (value.startsWith('"') && value.endsWith('"')) {
            value = value.slice(1, -1);
        }
        contextPairs.unshift({ key: match[1], value: value });
        remaining = remaining.slice(0, match.index);
    }

    entry.message = remaining.trim();
    contextPairs.forEach(pair => {
        entry.context[pair.key] = pair.value;
    });

    return entry;
}

function initLogViewModeToggle() {
    const viewButtons = document.querySelectorAll('.log-view-btn[data-log-view-mode]');
    viewButtons.forEach(btn => {
        btn.addEventListener('click', () => {
            const mode = btn.dataset.logViewMode || 'table';
            switchLogViewMode(mode);
        });
    });
    syncLogViewModeUI();
}

function switchLogViewMode(mode) {
    if (mode !== 'table' && mode !== 'raw') {
        mode = 'table';
    }
    activeLogViewMode = mode;
    try {
        localStorage.setItem('pm_agent_log_view_mode', mode);
    } catch (e) {}
    syncLogViewModeUI();
    filterAndDisplayLogs();
}

function syncLogViewModeUI() {
    document.querySelectorAll('.log-view-btn[data-log-view-mode]').forEach(btn => {
        const btnMode = btn.dataset.logViewMode || 'table';
        btn.classList.toggle('active', btnMode === activeLogViewMode);
    });

    const tableContainer = document.getElementById('log_table_container');
    const rawContainer = document.getElementById('log');

    if (activeLogViewMode === 'table') {
        if (tableContainer) tableContainer.classList.remove('hidden');
        if (rawContainer) rawContainer.classList.add('hidden');
    } else {
        if (tableContainer) tableContainer.classList.add('hidden');
        if (rawContainer) rawContainer.classList.remove('hidden');
    }
}

function escapeHtml(text) {
    if (!text) return '';
    const div = document.createElement('div');
    div.textContent = text;
    return div.innerHTML;
}

function formatDateShort(date) {
    if (!date || !(date instanceof Date) || isNaN(date.getTime())) return '';
    const month = String(date.getMonth() + 1).padStart(2, '0');
    const day = String(date.getDate()).padStart(2, '0');
    return `${month}/${day} `;
}

function formatTimeShort(date) {
    if (!date || !(date instanceof Date) || isNaN(date.getTime())) return '';
    const hours = String(date.getHours()).padStart(2, '0');
    const mins = String(date.getMinutes()).padStart(2, '0');
    const secs = String(date.getSeconds()).padStart(2, '0');
    return `${hours}:${mins}:${secs}`;
}

// Filter and display logs based on current filters
function filterAndDisplayLogs() {
    // Parse all entries
    parsedLogEntries = allLogEntries.map(parseLogLine);
    
    const levelFilter = document.getElementById('log_level_filter')?.value || '';
    const searchFilter = document.getElementById('log_search_filter')?.value.toLowerCase() || '';
    
    let filtered = parsedLogEntries;
    
    // Filter by level
    if (levelFilter) {
        filtered = filtered.filter(entry => entry.level === levelFilter);
    }
    
    // Filter by search text
    if (searchFilter) {
        filtered = filtered.filter(entry => entry.raw.toLowerCase().includes(searchFilter));
    }
    
    if (activeLogViewMode === 'table') {
        renderLogsTable(filtered);
    } else {
        renderLogsRaw(filtered);
    }
}

function renderLogsTable(entries) {
    const tbody = document.getElementById('log_table_body');
    if (!tbody) return;

    if (!entries || entries.length === 0) {
        tbody.innerHTML = '<tr><td colspan="4" class="log-table-empty">No matching log entries</td></tr>';
        return;
    }

    const rows = entries.map(entry => {
        const timeHtml = entry.timestamp
            ? `<span class="log-time-date">${formatDateShort(entry.timestamp)}</span>${formatTimeShort(entry.timestamp)}`
            : '<span class="log-time">—</span>';

        const levelClass = entry.level ? `log-level-${entry.level.toLowerCase()}` : '';
        const levelHtml = entry.level
            ? `<span class="log-level ${levelClass}">${escapeHtml(entry.level)}</span>`
            : '';

        const contextHtml = Object.keys(entry.context).length > 0
            ? Object.entries(entry.context).map(([k, v]) => 
                `<span class="log-context-tag"><span class="tag-key">${escapeHtml(k)}</span>=<span class="tag-value">${escapeHtml(String(v))}</span></span>`
              ).join('')
            : '';

        return `<tr>
            <td class="log-time">${timeHtml}</td>
            <td>${levelHtml}</td>
            <td class="log-message">${escapeHtml(entry.message)}</td>
            <td class="log-context">${contextHtml}</td>
        </tr>`;
    });

    tbody.innerHTML = rows.join('');

    // Auto-scroll to bottom if not paused
    const pauseCheckbox = document.getElementById('pause_autoscroll');
    if (!pauseCheckbox || !pauseCheckbox.checked) {
        const container = document.getElementById('log_table_container');
        if (container) {
            container.scrollTop = container.scrollHeight;
        }
    }
}

function renderLogsRaw(entries) {
    const logEl = document.getElementById('log');
    if (!logEl) return;

    if (!entries || entries.length === 0) {
        logEl.innerHTML = '<span style="color:#586e75">(no matching entries)</span>';
        return;
    }

    // Colorize and display
    const html = entries.map(entry => colorizeLogLine(entry.raw)).join('\n');
    logEl.innerHTML = html;
    
    // Auto-scroll to bottom if not paused
    const pauseCheckbox = document.getElementById('pause_autoscroll');
    if (!pauseCheckbox || !pauseCheckbox.checked) {
        logEl.scrollTop = logEl.scrollHeight;
    }
}

function setLogsLoadError(error) {
    const panel = document.getElementById('logs_load_error');
    const message = document.getElementById('logs_load_error_message');
    if (panel) panel.hidden = !error;
    if (message) message.textContent = error ? 'Failed to load logs: ' + error.message : '';
}

// Load initial log contents once (SSE will stream new entries in real-time)
async function updateLog() {
    try {
        const response = await fetch('/logs');
        if (!response.ok) throw new Error('Request failed (' + response.status + ')');
        const t = await response.text();
        const lines = t.split('\n').filter(line => line.trim());
        allLogEntries = lines;
        filterAndDisplayLogs();
        setLogsLoadError(null);
        return true;
    } catch (error) {
        window.__pm_shared.error('updateLog failed', error);
        setLogsLoadError(error);
        return false;
    }
}

async function copyLogs() {
    try {
        const text = await fetch('/logs').then(r => r.text());
        await copyToClipboard(text, null, 'All logs copied to clipboard');
    } catch (e) {
        window.__pm_shared.error('Copy logs failed:', e);
        window.__pm_shared.showToast('Failed to copy logs: ' + e.message, 'error');
    }
}

async function clearLogs() {
    try {
        const confirmed = await window.__pm_shared.showConfirm(
            'Clear logs? This will rotate the current log file and start fresh.',
            'Clear Logs'
        );
        if (!confirmed) return;
        
        const resp = await fetch('/logs/clear', { method: 'POST' });
        if (!resp.ok) {
            const text = await resp.text();
            window.__pm_shared.showToast('Clear logs failed: ' + text, 'error');
            return;
        }
        
        // Clear the display and array
        allLogEntries = [];
        const logEl = document.getElementById('log');
        if (logEl) {
            logEl.innerHTML = '<span style="color:#586e75">(logs cleared - waiting for new entries)</span>';
        }
        
    window.__pm_shared.showToast('Logs cleared and rotated', 'success');
    } catch (e) {
        window.__pm_shared.error('Clear logs failed:', e);
        window.__pm_shared.showToast('Failed to clear logs: ' + e.message, 'error');
    }
}

async function downloadLogs() {
    try {
        const resp = await fetch('/logs/archive');
        if (!resp.ok) {
            const t = await resp.text();
            window.__pm_shared.showToast('Download failed: ' + t, 'error');
            return;
        }
        const blob = await resp.blob();
        let filename = 'logs.zip';
        const cd = resp.headers.get('Content-Disposition') || '';
        const m = /filename="?([^";]+)"?/i.exec(cd);
        if (m && m[1]) filename = m[1];
        const url = URL.createObjectURL(blob);
        const a = document.createElement('a');
        a.href = url; a.download = filename; document.body.appendChild(a); a.click(); a.remove();
        URL.revokeObjectURL(url);
    window.__pm_shared.showToast('Log archive downloaded', 'success');
    } catch (e) {
        window.__pm_shared.error('Download failed:', e);
        window.__pm_shared.showToast('Failed to download logs: ' + e.message, 'error');
    }
}

function updateMetrics() {
    fetch('/scan_metrics').then(r => r.ok ? r.json() : Promise.resolve(null)).then(m => {
        const el = document.getElementById('metrics_text');
        if (!el) return;
        let txt = '';
        if (m) txt += 'hosts=' + (m.total_hosts_scanned || 0) + ' printers=' + (m.detected_printers || 0) + ' missing_mfg=' + (m.missing_manufacturer || 0);
        el.textContent = txt;
    }).catch(() => { })
}

// Shared UI log appender. If elId omitted, appends to global '#log'.
function appendUiLog(msg, elId) {
    try {
        const id = elId || 'log';
        const el = document.getElementById(id);
        if (!el) return;
        // determine whether user is near bottom
        const atBottom = (el.scrollHeight - el.scrollTop - el.clientHeight) < 50;
        el.textContent += new Date().toISOString() + ' ' + msg + '\n';
        // if pause checkbox checked, do not auto-scroll
        const paused = document.getElementById('pause_autoscroll');
        if (paused && paused.checked) return;
        if (atBottom) {
            el.scrollTop = el.scrollHeight;
        }
    } catch (e) { /* ignore UI errors */ }
}
function loadSavedRanges() {
    fetch('/settings').then(r => r.ok ? r.json() : Promise.resolve(null)).then(d => {
        if (!d) return;
        const txt = (d.discovery && d.discovery.ranges_text) ? d.discovery.ranges_text : '';
        document.getElementById('ranges_text').value = txt;
        // On load, estimate expansion and warn if too large
        try {
            const cnt = estimateRangeCount(txt);
            const MAX_ADDRS = 10000;
            if (cnt > MAX_ADDRS) {
                window.__pm_shared.showToast(`Saved ranges expand to ${cnt} addresses which exceeds the allowed maximum of ${MAX_ADDRS}. Manual IP scanning may be disabled. Reduce ranges or enable passive discovery.`, 'error', 8000);
            }
        } catch (e) {
            // Non-fatal - ignore parse failures here
        }
    })
}

function saveRanges() {
    let txt = document.getElementById('ranges_text').value;
    // Client-side guard: prevent saving ranges that expand beyond safe threshold
    try {
        const cnt = estimateRangeCount(txt);
        const MAX_ADDRS = 10000; // keep consistent with server-side policy
        if (cnt > MAX_ADDRS) {
            window.__pm_shared.showToast(`Cannot save ranges: expansion would produce ${cnt} addresses (over max ${MAX_ADDRS})`, 'error', 6000);
            return;
        }
    } catch (e) {
        // If estimation fails, proceed and let server validate
    }
    fetch('/settings', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ discovery: { ranges_text: txt } }) })
        .then(async r => {
                if (!r.ok) { 
                let t = await r.text(); 
                window.__pm_shared.showToast('Save failed: ' + t, 'error');
                return; 
            }
            window.__pm_shared.showToast('Ranges saved', 'success');
        })
        .catch(e => {
            window.__pm_shared.showToast('Save failed: ' + e.message, 'error');
        })
}

// Estimate number of IP addresses represented by the ranges text.
// Conservative: returns a large number (>MAX) if parsing uncertain.
function estimateRangeCount(text) {
    if (!text || !text.trim()) return 0;
    const lines = text.split('\n');
    let total = 0;
    for (let raw of lines) {
        const s = raw.trim();
        if (!s || s.startsWith('#')) continue;

        // CIDR
        const cidrMatch = s.match(/^(\d{1,3}(?:\.\d{1,3}){3})\/(\d{1,2})$/);
        if (cidrMatch) {
            const prefix = parseInt(cidrMatch[2], 10);
            if (isNaN(prefix) || prefix < 0 || prefix > 32) throw new Error('invalid CIDR');
            const hostBits = 32 - prefix;
            if (hostBits >= 31) return Number.MAX_SAFE_INTEGER;
            const cnt = Math.pow(2, hostBits);
            total += cnt;
            continue;
        }

        // Wildcard like 192.168.1.x or 192.168.1.*
        if (s.endsWith('.x') || s.endsWith('.*')) {
            total += 256;
            continue;
        }

        // Dash range
        if (s.includes('-')) {
            const parts = s.split('-').map(p => p.trim());
            const left = parts[0];
            const right = parts[1];
            // If right is full IP
            if (/^\d{1,3}(?:\.\d{1,3}){3}$/.test(right)) {
                const start = ipToUint32_js(left);
                const end = ipToUint32_js(right);
                if (end < start) throw new Error('end before start');
                total += (end - start + 1);
                continue;
            }
            // shorthand: right supplies last octet(s) or single number
            const lparts = left.split('.');
            const rparts = right.split('.');
            if (lparts.length !== 4) throw new Error('invalid left ip');
            // build end IP
            let endParts = [];
            if (rparts.length >= 1 && rparts.length <= 3) {
                const copy = 4 - rparts.length;
                for (let i = 0; i < copy; i++) endParts.push(lparts[i]);
                for (let rp of rparts) endParts.push(rp);
                const start = ipToUint32_js(left);
                const end = ipToUint32_js(endParts.join('.'));
                if (end < start) throw new Error('end before start');
                total += (end - start + 1);
                continue;
            }
            // unknown format -> conservative
            return Number.MAX_SAFE_INTEGER;
        }

        // Single IP
        if (/^\d{1,3}(?:\.\d{1,3}){3}$/.test(s)) {
            total += 1;
            continue;
        }

        // Unrecognized -> conservative large
        return Number.MAX_SAFE_INTEGER;
    }
    return total;
}

// helper: parse IPv4 dotted quad to uint32 (JS)
function ipToUint32_js(ip) {
    const parts = ip.split('.').map(n => parseInt(n, 10));
    if (parts.length !== 4) throw new Error('invalid ip');
    let n = 0;
    for (let i = 0; i < 4; i++) {
        if (isNaN(parts[i]) || parts[i] < 0 || parts[i] > 255) throw new Error('invalid ip octet');
        n = (n << 8) + (parts[i] & 0xFF);
    }
    return n >>> 0;
}

function clearRanges() {
    fetch('/settings', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ discovery: { ranges_text: '' } }) })
        .then(() => loadSavedRanges())
}

// Update the detected subnet display in settings
function updateSubnetDisplay() {
    fetch('/settings').then(r => r.ok ? r.json() : Promise.resolve(null)).then(d => {
        const subnet = (d && d.discovery && d.discovery.detected_subnet) ? d.discovery.detected_subnet : '(unable to detect)';
        const subnetEl = document.getElementById('detected_subnet_display');
        if (subnetEl) subnetEl.textContent = subnet;
        // If detected subnet is a CIDR and expands beyond MAX_ADDRS, disable the subnet scan toggle
        try {
            const MAX_ADDRS = 10000;
            if (subnet && subnet.includes('/') ) {
                const cnt = estimateRangeCount(subnet);
                const scanEl = document.getElementById('scan_local_subnet_enabled');
                if (cnt > MAX_ADDRS) {
                    if (scanEl) {
                        scanEl.checked = false;
                        scanEl.disabled = true;
                    }
                    // Persistently turn off subnet scanning so server/agent also respects it
                    try {
                        fetch('/settings', {
                            method: 'POST',
                            headers: { 'Content-Type': 'application/json' },
                            body: JSON.stringify({ discovery: { subnet_scan: false } })
                        }).then(r => {
                            if (!r.ok) {
                                window.__pm_shared.warn('Failed to persist subnet_scan=false');
                            }
                        }).catch(e => window.__pm_shared.warn('Persist subnet_scan failed', e));
                    } catch (e) {
                        window.__pm_shared.warn('Persist subnet_scan failed', e);
                    }

                    // Update inline reason text so users see why the control is disabled
                    try {
                        const reasonEl = document.getElementById('detected_subnet_reason');
                        if (reasonEl) {
                            reasonEl.textContent = `Disabled: ${subnet} expands to ${cnt} addresses (> ${MAX_ADDRS}). Subnet scanning has been turned off to avoid large scans. Use manual ranges or enable passive discovery.`;
                            reasonEl.style.display = 'block';
                        }
                    } catch (e) {
                        // ignore
                    }

                    try {
                        // If user previously chose "Don't show this again" skip alert
                        if (!localStorage.getItem('hideConfigWarning')) {
                            // showAlert(message, title, isDangerous, showDontRemindCheckbox)
                            window.__pm_shared.showAlert(
                                `Detected subnet ${subnet} expands to ${cnt} addresses (over ${MAX_ADDRS}). Subnet scanning has been disabled to avoid excessive scans. It has been turned off and cannot be re-enabled until you update ranges or enable manually. Use manual ranges or passive discovery.`,
                                'Subnet scanning disabled',
                                true,
                                true
                            );
                        }
                    } catch (e) { try { window.__pm_shared.warn('alert failed', e); } catch(_){} }
                } else {
                    if (scanEl) {
                        // ensure it's enabled (but don't override user's saved value)
                        scanEl.disabled = false;
                    }
                    try {
                        const reasonEl = document.getElementById('detected_subnet_reason');
                        if (reasonEl) { reasonEl.style.display = 'none'; reasonEl.textContent = ''; }
                    } catch (e) {}
                }
            }
        } catch (e) {
            // ignore parsing errors
        }
    })
}

// UI effects when Auto Discover is toggled (called after settings are saved)
function toggleAutoDiscoverUI() {
    // Hide/show Discover Now button based on Auto Discover state with animation
    const autoDiscoverEnabled = document.getElementById('auto_discover_checkbox')?.checked ?? false;
    const discoverBtn = document.getElementById('discover_now_btn');
    const showAnywayContainer = document.getElementById('show_discover_button_anyway_container');
    const showAnywayToggle = document.getElementById('show_discover_button_anyway');
    const showAnyway = showAnywayToggle?.checked ?? false;

    if (discoverBtn) {
        // Logic: Show button when auto discover is OFF, OR when "Show Anyway" is enabled
        const shouldShowButton = !autoDiscoverEnabled || (autoDiscoverEnabled && showAnyway);
        
        if (shouldShowButton) {
            // Show and fade in
            discoverBtn.style.display = 'inline-block';
            discoverBtn.style.opacity = '0';
            discoverBtn.style.transform = 'scale(0.95)';
            setTimeout(() => {
                discoverBtn.style.transition = 'opacity 0.3s ease, transform 0.3s ease';
                discoverBtn.style.opacity = '1';
                discoverBtn.style.transform = 'scale(1)';
            }, 10);
        } else {
            // Fade out and hide (only when auto discover is ON and "Show Anyway" is OFF)
            discoverBtn.style.transition = 'opacity 0.3s ease, transform 0.3s ease';
            discoverBtn.style.opacity = '0';
            discoverBtn.style.transform = 'scale(0.95)';
            setTimeout(() => {
                discoverBtn.style.display = 'none';
            }, 300);
        }
    }

    // Show/hide the "Show Discover Button Anyway" toggle container
    if (showAnywayContainer) {
        showAnywayContainer.style.display = autoDiscoverEnabled ? 'flex' : 'none';
    }
}



// Show/hide the ranges configuration dropdown based on manual_ranges setting
function toggleRangesDropdown() {
    const enabled = document.getElementById('manual_ranges_enabled')?.checked ?? true;
    const rangesEl = document.getElementById('ranges_text');
    if (!rangesEl) return;
    // Find the nearest details container (our Advanced Active Probes details) or parent
    const container = rangesEl.closest('details') || rangesEl.parentElement;
    if (!container) return;

    if (enabled) {
        container.style.display = '';
        try { container.open = true; } catch (e) { /* ignore */ }
    } else {
        try { container.open = false; } catch (e) { /* ignore */ }
        container.style.display = 'none';
    }
}

// Toggle top-level IP scanning UI. Hides/shows elements that should be disabled when IP scanning is off.
function toggleIPScanningUI() {
    const enabled = document.getElementById('ip_scanning_enabled')?.checked ?? true;
    document.querySelectorAll('.ipscan-subtoggle').forEach(el => {
        el.style.display = enabled ? '' : 'none';
    });
    // Ensure ranges visibility respects both parent and manual_ranges checkbox
    toggleRangesDropdown();
}

// Ranges are now displayed in ranges_text textarea via loadSavedRanges() function

// if advanced manufacturer selector exists, wire its change handler (guarded)
(function () { const m = document.getElementById('mib_mfg'); if (!m) return; m.addEventListener('change', function (e) { const v = e.target.value; const custom = document.getElementById('mib_custom'); if (custom) { custom.style.display = (v === 'custom') ? 'inline-block' : 'none'; } }); })();

// DEPRECATED: Manual MIB walk functionality is superseded by automated discovery pipeline
// See: docs/DEPRECATED_PENDING_REMOVAL.md
// TODO: Remove this function and associated UI elements after verification
function runMibWalk() {
    // Simplified single-IP manual walk: only IP, community and version are required.
    const ipEl = document.getElementById('mib_ip');
    if (!ipEl) { window.__pm_shared.showToast('Manual walk UI missing', 'error'); return; }
    const ip = ipEl.value.trim();
    if (!ip) { window.__pm_shared.showToast('Enter target IP', 'error'); return; }
    const community = (document.getElementById('mib_community') || {}).value ? document.getElementById('mib_community').value.trim() : '';
    const version = (document.getElementById('mib_version') || {}).value || 'v2c';
    const body = { ip: ip, community: community, version: version, max_entries: 2000 };
    const meta = document.getElementById('mib_results_meta');
    const tbody = document.getElementById('mib_results_tbody');
    if (meta) { meta.style.display = 'block'; meta.textContent = 'Running MIB walk...'; }
    if (tbody) tbody.innerHTML = '';
    fetch('/mib_walk', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
        .then(async r => {
            if (!r.ok) { const t = await r.text(); if (meta) meta.textContent = 'Error: ' + t; return; }
            return r.json();
        })
        .then(j => { if (!j) return; renderMibResults(j); }).catch(e => { if (meta) meta.textContent = 'Request failed: ' + e; });
}

// DEPRECATED: Manual MIB walk functionality is superseded by automated discovery pipeline
// See: docs/DEPRECATED_PENDING_REMOVAL.md
// TODO: Remove this function and associated UI elements after verification
// Run a MIB walk for a specific discovered printer IP and show results in the MIB Walk panel
function runMibWalkFor(ip) {
    if (!ip) return;
    // switch to Devices tab so results are visible
    showTab('devices');
    const meta = document.getElementById('mib_results_meta');
    const tbody = document.getElementById('mib_results_tbody');
    if (meta) { meta.style.display = 'block'; meta.textContent = 'Running MIB walk for ' + ip + '...'; }
    if (tbody) tbody.innerHTML = '';
    // Use simplified single-IP walk parameters (default max_entries)
    const community = (document.getElementById('mib_community') || {}).value ? document.getElementById('mib_community').value.trim() : '';
    const version = (document.getElementById('mib_version') || {}).value || 'v2c';
    const body = { ip: ip, community: community, version: version, max_entries: 2000 };
    fetch('/mib_walk', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
        .then(async r => {
            if (!r.ok) { const t = await r.text(); if (meta) meta.textContent = 'Error: ' + t; return; }
            return r.json();
        })
        .then(j => { if (!j) return; renderMibResults(j); }).catch(e => { if (meta) meta.textContent = 'Request failed: ' + e; });
}

function viewDiag(ip) {
    if (!ip) return;
    // ensure the Devices tab is visible so diagnostics are visible
    showTab('devices');
    const pre = document.getElementById('mib_results');
    if (pre) pre.textContent = 'Device diagnostics removed (legacy feature).';
    try { window.__pm_shared && window.__pm_shared.showToast && window.__pm_shared.showToast('Device diagnostics view removed (legacy)', 'info'); } catch (e) { /* ignore */ }
}


// DEPRECATED: Show printer details modal by fetching device info by IP.
// Use showSavedDeviceDetails(serial) or window.__pm_shared.showPrinterDetails(serial, source) instead.
// This function is kept for backwards compatibility but may be removed in a future release.
// The serial-based approach is preferred because:
// 1. Serial is a stable identifier (IPs can change via DHCP)
// 2. Avoids the two-stage IP→device lookup which can be slow and error-prone
// 3. Matches the canonical identifier used throughout the API
function showPrinterDetails(ip, source) {
    console.warn('showPrinterDetails(ip) is deprecated. Use serial-based lookup via showSavedDeviceDetails(serial) or window.__pm_shared.showPrinterDetails(serial, source).');
    if (!ip) return;
    source = source || 'discovered'; // default to discovered
    const bodyEl = document.getElementById('printer_details_body');
    const modal = document.getElementById('printer_details_modal');

    bodyEl.textContent = 'Loading...';
    modal.classList.remove('hidden');

    // Try to find device by IP and redirect to serial-based lookup once resolved.
    // This maintains backwards compatibility while steering towards the preferred approach.
    fetch('/devices/discovered').then(r => r.json()).then(discovered => {
        // Check discovered printers first
        let p = discovered.find(d => d.ip === ip);
        if (p && p.serial) {
            // Redirect to serial-based lookup
            if (window.__pm_shared && typeof window.__pm_shared.showPrinterDetails === 'function') {
                window.__pm_shared.showPrinterDetails(p.serial, source);
                return;
            }
            // Fallback to direct render
            try { window.__pm_shared_cards.showPrinterDetailsData(p, source); } catch (e) { bodyEl.textContent = 'Error rendering device: ' + e; }
            return;
        }

        // Not in discovered, try database - saved list returns wrapped printer_info
        fetch('/devices/list').then(r => r.json()).then(saved => {
            const item = saved.find(d => (d.printer_info && d.printer_info.ip === ip));
            if (item && item.serial) {
                // Redirect to serial-based lookup
                if (window.__pm_shared && typeof window.__pm_shared.showPrinterDetails === 'function') {
                    window.__pm_shared.showPrinterDetails(item.serial, 'saved');
                    return;
                }
                // Fallback to direct render
                const deviceObj = item.printer_info || item;
                if (!deviceObj.serial && item.serial) deviceObj.serial = item.serial;
                try { window.__pm_shared_cards.showPrinterDetailsData(deviceObj, 'saved'); } catch (e) { bodyEl.textContent = 'Error rendering saved device: ' + e; }
            } else {
                bodyEl.textContent = 'Device not found';
            }
        }).catch(e => { bodyEl.textContent = 'Error loading device: ' + e; });
    }).catch(e => { bodyEl.textContent = 'Error loading devices: ' + e; });
}

// Load device metrics history and display in UI with interactive timeframe selector
// If targetId is provided, render UI into that element. Otherwise render into default '#metrics_content'.
async function loadDeviceMetrics(serial, targetId) {
    const sharedLoad = window.__pm_shared_metrics && window.__pm_shared_metrics.loadDeviceMetrics;
    if (typeof sharedLoad === 'function' && sharedLoad !== loadDeviceMetrics) {
        return sharedLoad(serial, targetId);
    }
    window.__pm_shared.log('[Metrics] loadDeviceMetrics called for serial:', serial);
    let contentEl = null;
    if (targetId) contentEl = document.getElementById(targetId);
    if (!contentEl) contentEl = document.getElementById('metrics_content');
    if (!contentEl) {
        window.__pm_shared.error('[Metrics] metrics_content element not found and no target available');
        return;
    }
    window.__pm_shared.log('[Metrics] Rendering metrics into element:', contentEl.id || contentEl.tagName);

    // Create interactive metrics UI
    let html = '';

    // Toggle to show/hide the datetime selector (hidden by default)
    const toggleTarget = targetId || '';
    html += '<div style="display:flex;justify-content:flex-end;margin-bottom:8px">';
    html += '<button id="metrics_toggle_time_btn" data-action="toggle-time" data-target="' + toggleTarget + '" aria-expanded="false" style="padding:6px 10px;font-size:13px">Show time selector</button>';
    html += '</div>';

    // Datetime range picker - hidden by default
    html += '<div id="metrics_time_selector" class="hidden">';
    html += '<div id="metrics_custom_range" style="margin-bottom:16px;padding:16px;background:rgba(0,0,0,0.2);border:1px solid rgba(255,255,255,0.05);border-radius:6px">';

    // Quick preset buttons (will be enabled/disabled based on available data)
    html += '<div style="margin-bottom:16px">';
    html += '<div style="font-size:12px;color:var(--muted);margin-bottom:8px">Quick Select:</div>';
    html += '<div style="display:flex;gap:6px;flex-wrap:wrap">';
    html += '<button id="preset_day" data-action="preset" data-preset="day" data-serial="' + serial + '" style="padding:6px 12px;font-size:13px;min-height:32px">Last 24 Hours</button>';
    html += '<button id="preset_week" data-action="preset" data-preset="week" data-serial="' + serial + '" style="padding:6px 12px;font-size:13px;min-height:32px">Last 7 Days</button>';
    html += '<button id="preset_month" data-action="preset" data-preset="month" data-serial="' + serial + '" style="padding:6px 12px;font-size:13px;min-height:32px">Last 30 Days</button>';
    html += '<button id="preset_year" data-action="preset" data-preset="year" data-serial="' + serial + '" style="padding:6px 12px;font-size:13px;min-height:32px">Last Year</button>';
    html += '<button id="preset_all" data-action="preset" data-preset="all" data-serial="' + serial + '" style="padding:6px 12px;font-size:13px;min-height:32px">All Time</button>';
    html += '</div>'; // close metrics_custom_range
    html += '</div>'; // close metrics_time_selector
    html += '</div>';

    // Custom datetime range picker with flatpickr
    html += '<div style="margin-bottom:12px">';
    html += '<div style="font-size:12px;color:var(--muted);margin-bottom:8px">Custom Range:</div>';
    html += '<input type="text" id="metrics_datetime_range" placeholder="Select date range..." style="width:100%;background:#073642;border:1px solid #004b56;color:var(--text);padding:10px;border-radius:4px;font-size:14px;min-height:38px;cursor:pointer" readonly />';
    html += '</div>';

    // Data range info
    html += '<div style="font-size:11px;color:var(--muted);margin-bottom:8px;padding:6px;background:rgba(0,0,0,0.1);border-radius:4px">';
    html += 'Available: <span id="metrics_data_range_start">Loading...</span> to <span id="metrics_data_range_end">Loading...</span>';
    html += '</div>';

    // Apply button
    html += '<button data-action="refresh" data-serial="' + serial + '" style="width:100%;padding:10px;font-size:14px;min-height:40px;font-weight:600;background:#268bd2;color:#fff">Update Chart</button>';
    html += '</div>';

    // Stats summary
    html += '<div id="metrics_stats" style="margin-bottom:12px;font-size:12px"></div>';

    // Chart canvas
    html += '<canvas id="metrics_chart" width="500" height="200" style="width:100%;max-height:200px;background:#001f22;border:1px solid rgba(255,255,255,0.06);border-radius:4px"></canvas>';

    contentEl.innerHTML = html;

    // Initialize metrics data range (will be set after first data load)
    window.metricsDataRange = { min: null, max: null, serial: serial, flatpickr: null };

    // Initialize flatpickr and load data
    await initializeCustomDatetimePicker(serial, contentEl);
    // Ensure chart/table refresh runs after picker init so UI shows data without requiring the user to click
    try {
        // Small defer to allow DOM/layout to settle
        setTimeout(() => {
            try { refreshMetricsChart(serial); } catch (e) { window.__pm_shared.warn('[Metrics] auto refresh failed:', e); }
        }, 50);
    } catch (e) {
        window.__pm_shared.warn('[Metrics] Failed to auto-refresh after init:', e);
    }
}

// Initialize custom datetime picker with actual data bounds
async function initializeCustomDatetimePicker(serial, contentElOverride) {
    const sharedInit = window.__pm_shared_metrics && window.__pm_shared_metrics.initializeCustomDatetimePicker;
    if (typeof sharedInit === 'function' && sharedInit !== initializeCustomDatetimePicker) {
        return sharedInit(serial, contentElOverride);
    }
    window.__pm_shared.log('[Metrics] initializeCustomDatetimePicker called');
    try {
        // Fetch only bounds to determine data range (fast, small payload)
        const url = '/api/devices/metrics/bounds?serial=' + encodeURIComponent(serial);
        window.__pm_shared.log('[Metrics] Fetching bounds:', url);
        const res = await fetch(url);
        if (!res.ok) {
            window.__pm_shared.error('[Metrics] API returned status:', res.status);
            return;
        }

        const bounds = await res.json();
        window.__pm_shared.log('[Metrics] Bounds:', bounds);
        // Use provided content element or fall back to global
        const contentEl = contentElOverride || document.getElementById('metrics_content');

        const minTime = bounds && bounds.min_timestamp ? new Date(bounds.min_timestamp) : null;
        const maxTime = bounds && bounds.max_timestamp ? new Date(bounds.max_timestamp) : null;
        if (!minTime || !maxTime || isNaN(minTime.getTime()) || isNaN(maxTime.getTime())) {
            window.__pm_shared.warn('[Metrics] No history data available');
            if (contentEl) {
                const startEl = contentEl.querySelector('#metrics_data_range_start');
                const endEl = contentEl.querySelector('#metrics_data_range_end');
                if (startEl) startEl.textContent = 'No data';
                if (endEl) endEl.textContent = 'No data';
                contentEl.innerHTML = '<div style="color:var(--muted);padding:12px">No metrics data available yet. Collect metrics first.</div>';
            } else {
                const globalStart = document.getElementById('metrics_data_range_start');
                const globalEnd = document.getElementById('metrics_data_range_end');
                if (globalStart) globalStart.textContent = 'No data';
                if (globalEnd) globalEnd.textContent = 'No data';
                const globalContent = document.getElementById('metrics_content');
                if (globalContent) globalContent.innerHTML = '<div style="color:var(--muted);padding:12px">No metrics data available yet. Collect metrics first.</div>';
            }
            return;
        }

        // Store data range globally
        window.metricsDataRange.min = minTime;
        window.metricsDataRange.max = maxTime;

        // Update labels inside provided content element if present
        if (contentEl) {
            const startEl = contentEl.querySelector('#metrics_data_range_start');
            const endEl = contentEl.querySelector('#metrics_data_range_end');
            if (startEl) startEl.textContent = minTime.toLocaleString();
            if (endEl) endEl.textContent = maxTime.toLocaleString();
        } else {
            const globalStart = document.getElementById('metrics_data_range_start');
            const globalEnd = document.getElementById('metrics_data_range_end');
            if (globalStart) globalStart.textContent = minTime.toLocaleString();
            if (globalEnd) globalEnd.textContent = maxTime.toLocaleString();
        }

        // Default to full available range (All Time)
        const now = maxTime;

        // Check if flatpickr is available
        if (typeof flatpickr === 'undefined') {
            window.__pm_shared.error('[Metrics] flatpickr library not loaded');
            if (contentEl) contentEl.innerHTML = '<div style="color:#d33;padding:12px">Error: Date picker library not loaded. Please refresh the page.</div>';
            return;
        }
        window.__pm_shared.log('[Metrics] Initializing flatpickr with full range:', minTime, 'to', maxTime);
        // Initialize flatpickr with range mode
        const selector = (contentElOverride ? ('#' + (contentElOverride.id || 'metrics_datetime_range')) : '#metrics_datetime_range');
        // If contentElOverride is provided, use the input inside it
        const targetSelector = contentElOverride ? (contentElOverride.querySelector('#metrics_datetime_range')) : document.querySelector('#metrics_datetime_range');
        const fpInstance = flatpickr(targetSelector, {
            mode: 'range',
            enableTime: true,
            dateFormat: 'Y-m-d H:i',
            minDate: minTime,
            maxDate: maxTime,
            // Default to all available data on initial load
            defaultDate: [minTime, maxTime],
            time_24hr: true,
            onChange: function (selectedDates, dateStr, instance) {
                window.__pm_shared.log('[Metrics] Range changed:', selectedDates);
                // Auto-refresh chart when range changes
                if (selectedDates.length === 2) {
                    refreshMetricsChart(serial);
                }
            },
            onReady: function (selectedDates, dateStr, instance) {
                window.__pm_shared.log('[Metrics] flatpickr ready, refreshing chart');
                // Refresh chart with initial range
                refreshMetricsChart(serial);
            }
        });

        // Store flatpickr instance for programmatic updates
        window.metricsDataRange.flatpickr = fpInstance;

        // Enable/disable preset buttons based on available data range
        updatePresetButtonStates(minTime, maxTime);

    } catch (e) {
        window.__pm_shared.error('[Metrics] Failed to initialize datetime picker:', e);
        const contentEl = document.getElementById('metrics_content');
        if (contentEl) {
            contentEl.innerHTML = '<div style="color:#d33;padding:12px">Error loading metrics: ' + e.message + '</div>';
        }
    }
}

// Update preset button states based on available data range
function updatePresetButtonStates(minTime, maxTime) {
    const dataRangeMs = maxTime.getTime() - minTime.getTime();
    const dayMs = 24 * 60 * 60 * 1000;

    // Calculate how much data we have
    const hasDayData = dataRangeMs >= dayMs;
    const hasWeekData = dataRangeMs >= 7 * dayMs;
    const hasMonthData = dataRangeMs >= 30 * dayMs;
    const hasYearData = dataRangeMs >= 365 * dayMs;

    // Enable/disable buttons
    const dayBtn = document.getElementById('preset_day');
    const weekBtn = document.getElementById('preset_week');
    const monthBtn = document.getElementById('preset_month');
    const yearBtn = document.getElementById('preset_year');

    if (dayBtn) dayBtn.disabled = !hasDayData;
    if (weekBtn) weekBtn.disabled = !hasWeekData;
    if (monthBtn) monthBtn.disabled = !hasMonthData;
    if (yearBtn) yearBtn.disabled = !hasYearData;

    // All Time button is always enabled (just shows all available data)
}

// Quick range preset button handler
window.setMetricsQuickRange = function (preset, serial) {
    const sharedRange = window.__pm_shared_metrics && window.__pm_shared_metrics.setMetricsQuickRange;
    if (typeof sharedRange === 'function' && sharedRange !== window.setMetricsQuickRange) {
        return sharedRange(preset, serial);
    }
    if (!window.metricsDataRange || !window.metricsDataRange.min || !window.metricsDataRange.flatpickr) return;

    const minTime = window.metricsDataRange.min;
    const maxTime = window.metricsDataRange.max;
    const fp = window.metricsDataRange.flatpickr;

    let startTime;
    switch (preset) {
        case 'all':
            startTime = minTime;
            break;
        case 'year':
            startTime = new Date(Math.max(minTime.getTime(), maxTime.getTime() - 365 * 24 * 60 * 60 * 1000));
            break;
        case 'month':
            startTime = new Date(Math.max(minTime.getTime(), maxTime.getTime() - 30 * 24 * 60 * 60 * 1000));
            break;
        case 'week':
            startTime = new Date(Math.max(minTime.getTime(), maxTime.getTime() - 7 * 24 * 60 * 60 * 1000));
            break;
        case 'day':
            startTime = new Date(Math.max(minTime.getTime(), maxTime.getTime() - 24 * 60 * 60 * 1000));
            break;
    }

    // Update flatpickr selection
    fp.setDate([startTime, maxTime], true);
}

// Refresh metrics chart based on selected timeframe
window.refreshMetricsChart = async function (serial) {
    const sharedRefresh = window.__pm_shared_metrics && window.__pm_shared_metrics.refreshMetricsChart;
    if (typeof sharedRefresh === 'function' && sharedRefresh !== window.refreshMetricsChart) {
        return sharedRefresh(serial);
    }
    window.__pm_shared.log('[Metrics] refreshMetricsChart called for serial:', serial);
    // Prefer modal body if present, otherwise default metrics_content
    const container = document.getElementById('metrics_modal_body') || document.getElementById('metrics_content');
    if (!container) {
        window.__pm_shared.error('[Metrics] No metrics container found');
        return;
    }
    const statsEl = container.querySelector('#metrics_stats') || document.getElementById('metrics_stats');
    const canvas = container.querySelector('#metrics_chart') || document.getElementById('metrics_chart');
    if (!canvas || !statsEl) {
        window.__pm_shared.error('[Metrics] Missing chart elements within container - canvas:', !!canvas, 'stats:', !!statsEl);
        return;
    }

    try {
        // Get selected dates from flatpickr
        const fp = window.metricsDataRange?.flatpickr;
        if (!fp || !fp.selectedDates || fp.selectedDates.length !== 2) {
            window.__pm_shared.warn('[Metrics] No valid date range selected');
            statsEl.textContent = 'Please select a date range';
            return;
        }
        window.__pm_shared.log('[Metrics] Using date range:', fp.selectedDates);

        const startTime = fp.selectedDates[0];
        const endTime = fp.selectedDates[1];

        // Validate range
        if (endTime <= startTime) {
            statsEl.textContent = 'End time must be after start time';
            return;
        }

        // Fetch data
        const startISO = startTime.toISOString();
        const endISO = endTime.toISOString();
        const url = '/api/devices/metrics/history?serial=' + encodeURIComponent(serial) + '&since=' + encodeURIComponent(startISO) + '&until=' + encodeURIComponent(endISO);
        window.__pm_shared.log('[Metrics] Fetching chart data:', url);
        const res = await fetch(url);

        if (!res.ok) {
            window.__pm_shared.error('[Metrics] Chart data API returned status:', res.status);
            statsEl.textContent = 'No metrics data available yet.';
            return;
        }

        const history = await res.json();
        window.__pm_shared.log('[Metrics] Received', history?.length || 0, 'chart data points');
        if (!history || history.length === 0) {
            window.__pm_shared.warn('[Metrics] No data in selected timeframe');
            statsEl.textContent = 'No metrics data in selected timeframe.';
            drawEmptyChart(canvas);
            return;
        }

        // Calculate stats - comprehensive metrics table
        const latest = history[history.length - 1];
        const oldest = history[0];
        const durationMs = new Date(latest.timestamp).getTime() - new Date(oldest.timestamp).getTime();
        const durationDays = Math.max(1, durationMs / (24 * 60 * 60 * 1000));

        // Build comprehensive metrics table
        let statsHtml = '<table style="width:100%;border-collapse:collapse;margin-bottom:12px;font-size:12px">';
        statsHtml += '<thead><tr style="border-bottom:2px solid rgba(255,255,255,0.1)">';
        statsHtml += '<th style="text-align:left;padding:6px 8px;color:var(--highlight);font-weight:600">Metric</th>';
        statsHtml += '<th style="text-align:right;padding:6px 8px;color:var(--highlight);font-weight:600">Lifetime Total</th>';
        statsHtml += '<th style="text-align:right;padding:6px 8px;color:var(--highlight);font-weight:600">Period Diff</th>';
        statsHtml += '<th style="text-align:right;padding:6px 8px;color:var(--highlight);font-weight:600">Avg/Day</th>';
        statsHtml += '</tr></thead><tbody>';

        // Total Pages
        const lifetimePages = latest.page_count || 0;
        const periodPages = lifetimePages - (oldest.page_count || 0);
        const avgPagesPerDay = (periodPages / durationDays).toFixed(1);
        statsHtml += '<tr style="border-bottom:1px solid rgba(255,255,255,0.05)">';
        statsHtml += '<td style="padding:6px 8px;color:var(--text)">Total Pages</td>';
        statsHtml += '<td style="padding:6px 8px;text-align:right;color:var(--text);font-weight:600">' + lifetimePages.toLocaleString() + '</td>';
        statsHtml += '<td style="padding:6px 8px;text-align:right;color:#268bd2;font-weight:600">' + periodPages.toLocaleString() + '</td>';
        statsHtml += '<td style="padding:6px 8px;text-align:right;color:var(--muted)">' + avgPagesPerDay + '</td>';
        statsHtml += '</tr>';

        // Color Pages (if available)
        if (latest.color_pages !== undefined && latest.color_pages > 0) {
            const lifetimeColor = latest.color_pages || 0;
            const periodColor = lifetimeColor - (oldest.color_pages || 0);
            const avgColorPerDay = (periodColor / durationDays).toFixed(1);
            statsHtml += '<tr style="border-bottom:1px solid rgba(255,255,255,0.05)">';
            statsHtml += '<td style="padding:6px 8px;color:var(--text)">Color Pages</td>';
            statsHtml += '<td style="padding:6px 8px;text-align:right;color:var(--text);font-weight:600">' + lifetimeColor.toLocaleString() + '</td>';
            statsHtml += '<td style="padding:6px 8px;text-align:right;color:#268bd2;font-weight:600">' + periodColor.toLocaleString() + '</td>';
            statsHtml += '<td style="padding:6px 8px;text-align:right;color:var(--muted)">' + avgColorPerDay + '</td>';
            statsHtml += '</tr>';
        }

        // Mono Pages (if available)
        if (latest.mono_pages !== undefined && latest.mono_pages > 0) {
            const lifetimeMono = latest.mono_pages || 0;
            const periodMono = lifetimeMono - (oldest.mono_pages || 0);
            const avgMonoPerDay = (periodMono / durationDays).toFixed(1);
            statsHtml += '<tr style="border-bottom:1px solid rgba(255,255,255,0.05)">';
            statsHtml += '<td style="padding:6px 8px;color:var(--text)">Mono Pages</td>';
            statsHtml += '<td style="padding:6px 8px;text-align:right;color:var(--text);font-weight:600">' + lifetimeMono.toLocaleString() + '</td>';
            statsHtml += '<td style="padding:6px 8px;text-align:right;color:#268bd2;font-weight:600">' + periodMono.toLocaleString() + '</td>';
            statsHtml += '<td style="padding:6px 8px;text-align:right;color:var(--muted)">' + avgMonoPerDay + '</td>';
            statsHtml += '</tr>';
        }

        // Scans (if available)
        if (latest.scan_count !== undefined && latest.scan_count > 0) {
            const lifetimeScans = latest.scan_count || 0;
            const periodScans = lifetimeScans - (oldest.scan_count || 0);
            const avgScansPerDay = (periodScans / durationDays).toFixed(1);
            statsHtml += '<tr style="border-bottom:1px solid rgba(255,255,255,0.05)">';
            statsHtml += '<td style="padding:6px 8px;color:var(--text)">Scans</td>';
            statsHtml += '<td style="padding:6px 8px;text-align:right;color:var(--text);font-weight:600">' + lifetimeScans.toLocaleString() + '</td>';
            statsHtml += '<td style="padding:6px 8px;text-align:right;color:#268bd2;font-weight:600">' + periodScans.toLocaleString() + '</td>';
            statsHtml += '<td style="padding:6px 8px;text-align:right;color:var(--muted)">' + avgScansPerDay + '</td>';
            statsHtml += '</tr>';
        }

        // Toner levels (show current levels from latest snapshot)
        // Prefer the normalized buildTonerLevels helper when available, otherwise
        // fall back to structured latest.toners / latest.toner. Avoid using the
        // legacy top-level `toner_levels` field.
        const latestToners = (typeof buildTonerLevels === 'function') ? buildTonerLevels({}, latest)
            : (window.__pm_shared_cards && typeof window.__pm_shared_cards.buildTonerLevels === 'function') ? window.__pm_shared_cards.buildTonerLevels({}, latest)
            : (latest.toners || latest.toner || {});
        if (latestToners && Object.keys(latestToners).length > 0) {
            for (const [color, level] of Object.entries(latestToners)) {
                const levelNum = typeof level === 'number' ? level : parseInt(level) || 0;
                const levelColor = levelNum < 20 ? '#d32f2f' : (levelNum < 50 ? '#f57c00' : '#388e3c');
                statsHtml += '<tr style="border-bottom:1px solid rgba(255,255,255,0.05)">';
                statsHtml += '<td style="padding:6px 8px;color:var(--text)">' + color + '</td>';
                statsHtml += '<td style="padding:6px 8px;text-align:right;color:' + levelColor + ';font-weight:600">' + levelNum + '%</td>';
                statsHtml += '<td style="padding:6px 8px;text-align:right;color:var(--muted)" colspan="2">Current Level</td>';
                statsHtml += '</tr>';
            }
        }

        statsHtml += '</tbody></table>';
        statsEl.innerHTML = statsHtml;
        window.__pm_shared.log('[Metrics] Stats updated, drawing chart');

        // Draw chart
        drawMetricsChart(canvas, history, startTime, endTime);

        // Render a horizontally-scrollable metrics table showing each snapshot and a delete button
        try {
            const tableContainerId = 'metrics_table_container';
            let tableHtml = '<div id="' + tableContainerId + '" class="metrics-table-container" style="margin-top:12px;padding:8px;background:rgba(0,0,0,0.05);border-radius:6px;overflow-x:auto">';
            tableHtml += '<table class="metrics-table" style="border-collapse:collapse;min-width:800px;font-size:13px">';
            tableHtml += '<thead><tr style="border-bottom:2px solid rgba(255,255,255,0.06)">';
            // Timestamp header includes inline delete button column
            tableHtml += '<th style="padding:8px 12px;text-align:left">Timestamp</th>';
            tableHtml += '<th style="padding:8px 12px;text-align:right">Total</th>';
            tableHtml += '<th style="padding:8px 12px;text-align:right">Color</th>';
            tableHtml += '<th style="padding:8px 12px;text-align:right">Mono</th>';
            tableHtml += '<th style="padding:8px 12px;text-align:right">Scans</th>';
            tableHtml += '<th style="padding:8px 12px;text-align:right">Fax</th>';
            tableHtml += '</tr></thead><tbody>';

            history.forEach(item => {
                const ts = new Date(item.timestamp).toLocaleString();
                tableHtml += '<tr style="border-bottom:1px solid rgba(255,255,255,0.03)">';
                // Place delete button inline with timestamp (right-aligned within the same cell)
                tableHtml += '<td style="padding:8px 12px;display:flex;align-items:center;justify-content:space-between">';
                tableHtml += '<span>' + ts + ' <span style="color:var(--muted);font-size:11px;margin-left:6px">(' + (item.tier || 'raw') + ')</span></span>';
                tableHtml += '<span style="margin-left:12px">';
                tableHtml += '<button class="trash-btn" data-id="' + (item.id || '') + '" data-tier="' + (item.tier || '') + '" title="Delete this metrics row"></button>';
                tableHtml += '</span>';
                tableHtml += '</td>';
                tableHtml += '<td style="padding:8px 12px;text-align:right">' + ((item.page_count||0).toLocaleString()) + '</td>';
                tableHtml += '<td style="padding:8px 12px;text-align:right">' + ((item.color_pages||0).toLocaleString()) + '</td>';
                tableHtml += '<td style="padding:8px 12px;text-align:right">' + ((item.mono_pages||0).toLocaleString()) + '</td>';
                tableHtml += '<td style="padding:8px 12px;text-align:right">' + ((item.scan_count||0).toLocaleString()) + '</td>';
                tableHtml += '<td style="padding:8px 12px;text-align:right">' + ((item.fax_pages||0).toLocaleString()) + '</td>';
                tableHtml += '</tr>';
            });

            tableHtml += '</tbody></table></div>';
            // Create a stable panel wrapper with id so we can reliably find/replace it later
            const panelId = 'metrics_rows_panel';
            const panelHtml = '<div class="panel" id="' + panelId + '"><h4 style="margin-top:0;color:var(--highlight)">Metrics Rows</h4>' + tableHtml + '</div>';

            // Remove existing panel if present
            const old = document.getElementById(panelId);
            if (old && old.parentElement) old.parentElement.removeChild(old);

            // Insert panel into the current container (modal or page)
            if (container) {
                const metricsPanel = container.closest('.panel');
                if (metricsPanel && metricsPanel.parentElement) {
                    metricsPanel.insertAdjacentHTML('afterend', panelHtml);
                } else {
                    container.insertAdjacentHTML('beforeend', panelHtml);
                }
            } else {
                canvas.insertAdjacentHTML('afterend', panelHtml);
            }

            // Delegate click handler for delete buttons within the current container
            const metricsContainerForEvents = document.getElementById('metrics_modal_body') || document.getElementById('metrics_content');
            if (metricsContainerForEvents) {
                // Remove previous handler if present to avoid duplicate listeners
                if (metricsContainerForEvents._metricsDeleteHandler) {
                    metricsContainerForEvents.removeEventListener('click', metricsContainerForEvents._metricsDeleteHandler);
                    metricsContainerForEvents._metricsDeleteHandler = null;
                }

                const handler = async (e) => {
                    const btn = e.target.closest('.trash-btn');
                    if (!btn) return;
                    const id = btn.getAttribute('data-id');
                    const tier = btn.getAttribute('data-tier') || '';
                    if (!id) return;

                    // Use project's nicer modal confirm if available
                    const confirmed = await window.__pm_shared.showConfirm('Delete this metrics row? This action cannot be undone.', 'Confirm Delete', true);
                    if (!confirmed) return;

                    try {
                        btn.disabled = true;
                        const resp = await fetch('/api/devices/metrics/delete', {
                            method: 'POST', headers: { 'Content-Type': 'application/json' },
                            body: JSON.stringify({ id: Number(id), tier: tier })
                        });
                            if (resp.status === 204) {
                            // Refresh chart after deletion
                            refreshMetricsChart(serial);
                        } else {
                            const txt = await resp.text();
                                await window.__pm_shared.showConfirm('Failed to delete metric: ' + txt, 'Delete Failed', false);
                        }
                    } catch (err) {
                        await window.__pm_shared.showConfirm('Error deleting metric: ' + err, 'Delete Failed', false);
                    } finally {
                        btn.disabled = false;
                    }
                };

                metricsContainerForEvents.addEventListener('click', handler);
                metricsContainerForEvents._metricsDeleteHandler = handler;
            }
        } catch (e) {
            window.__pm_shared.warn('[Metrics] Failed to render metrics table:', e);
        }

    } catch (e) {
        window.__pm_shared.error('[Metrics] Error refreshing chart:', e);
        statsEl.textContent = 'Failed to load metrics: ' + e.message;
    }
}

// Draw smooth line graph showing cumulative page count over time
function drawMetricsChart(canvas, history, startTime, endTime) {
    const sharedDraw = window.__pm_shared_metrics && window.__pm_shared_metrics.drawMetricsChart;
    if (typeof sharedDraw === 'function' && sharedDraw !== drawMetricsChart) {
        return sharedDraw(canvas, history, startTime, endTime);
    }
    window.__pm_shared.log('[Metrics] drawMetricsChart called with', history.length, 'points');
    const ctx = canvas.getContext('2d');
    if (!ctx) {
        window.__pm_shared.error('[Metrics] Failed to get canvas 2d context');
        return;
    }

    // Handle high DPI displays (Retina, etc.)
    const dpr = window.devicePixelRatio || 1;
    const rect = canvas.getBoundingClientRect();
    window.__pm_shared.log('[Metrics] Canvas dimensions:', rect.width, 'x', rect.height, 'DPR:', dpr);
    canvas.width = rect.width * dpr;
    canvas.height = rect.height * dpr;
    ctx.scale(dpr, dpr);

    const width = rect.width;
    const height = rect.height;

    // Clear canvas
    ctx.clearRect(0, 0, canvas.width, canvas.height);

    if (!history || history.length < 1) {
        drawEmptyChart(canvas);
        return;
    }

    // Chart dimensions with padding
    const padding = { top: 30, right: 40, bottom: 50, left: 60 };
    const chartWidth = width - padding.left - padding.right;
    const chartHeight = height - padding.top - padding.bottom;

    // Convert history to plottable data points
    const dataPoints = history.map(snapshot => ({
        time: new Date(snapshot.timestamp).getTime(),
        value: snapshot.page_count || 0
    })).filter(p => p.time >= startTime.getTime() && p.time <= endTime.getTime());

    if (dataPoints.length === 0) {
        drawEmptyChart(canvas);
        return;
    }

    // Find min/max values for scaling
    const minValue = Math.min(...dataPoints.map(p => p.value));
    const maxValue = Math.max(...dataPoints.map(p => p.value));
    const valueRange = maxValue - minValue || 1;
    const timeRange = endTime.getTime() - startTime.getTime();

    // Helper function to map data to canvas coordinates
    const mapX = (time) => padding.left + ((time - startTime.getTime()) / timeRange) * chartWidth;
    const mapY = (value) => padding.top + chartHeight - ((value - minValue) / valueRange) * chartHeight;

    // Draw background grid
    ctx.strokeStyle = 'rgba(255,255,255,0.05)';
    ctx.lineWidth = 1;

    // Horizontal grid lines
    const ySteps = 5;
    for (let i = 0; i <= ySteps; i++) {
        const value = minValue + (valueRange / ySteps) * i;
        const y = mapY(value);

        ctx.beginPath();
        ctx.moveTo(padding.left, y);
        ctx.lineTo(padding.left + chartWidth, y);
        ctx.stroke();
    }

    // Vertical grid lines (time-based)
    const xSteps = 6;
    for (let i = 0; i <= xSteps; i++) {
        const time = startTime.getTime() + (timeRange / xSteps) * i;
        const x = mapX(time);

        ctx.strokeStyle = 'rgba(255,255,255,0.05)';
        ctx.beginPath();
        ctx.moveTo(x, padding.top);
        ctx.lineTo(x, padding.top + chartHeight);
        ctx.stroke();
    }

    // Draw axes
    ctx.strokeStyle = 'rgba(255,255,255,0.2)';
    ctx.lineWidth = 2;
    ctx.beginPath();
    ctx.moveTo(padding.left, padding.top);
    ctx.lineTo(padding.left, padding.top + chartHeight);
    ctx.lineTo(padding.left + chartWidth, padding.top + chartHeight);
    ctx.stroke();

    // Draw Y-axis labels (page counts)
    ctx.fillStyle = 'rgba(255,255,255,0.6)';
    ctx.font = '11px monospace';
    ctx.textAlign = 'right';
    for (let i = 0; i <= ySteps; i++) {
        const value = Math.round(minValue + (valueRange / ySteps) * i);
        const y = mapY(value);
        ctx.fillText(value.toLocaleString(), padding.left - 8, y + 4);
    }

    // Draw X-axis labels (time)
    ctx.textAlign = 'center';
    ctx.fillStyle = 'rgba(255,255,255,0.6)';
    ctx.font = '10px sans-serif';

    const durationHours = timeRange / (60 * 60 * 1000);
    const formatTime = (time) => {
        const d = new Date(time);
        if (durationHours <= 6) {
            // Show hours and minutes
            return d.getHours().toString().padStart(2, '0') + ':' + d.getMinutes().toString().padStart(2, '0');
        } else if (durationHours <= 48) {
            // Show day and hour
            return (d.getMonth() + 1) + '/' + d.getDate() + ' ' + d.getHours() + 'h';
        } else {
            // Show date only
            return (d.getMonth() + 1) + '/' + d.getDate();
        }
    };

    for (let i = 0; i <= xSteps; i++) {
        const time = startTime.getTime() + (timeRange / xSteps) * i;
        const x = mapX(time);
        const label = formatTime(time);
        ctx.save();
        ctx.translate(x, padding.top + chartHeight + 12);
        ctx.rotate(-Math.PI / 6);
        ctx.fillText(label, 0, 0);
        ctx.restore();
    }

    // Draw smooth gradient area under the line
    if (dataPoints.length > 0) {
        ctx.beginPath();
        ctx.moveTo(mapX(dataPoints[0].time), padding.top + chartHeight);
        ctx.lineTo(mapX(dataPoints[0].time), mapY(dataPoints[0].value));

        // Draw smooth curve through points using quadratic curves
        for (let i = 0; i < dataPoints.length - 1; i++) {
            const curr = dataPoints[i];
            const next = dataPoints[i + 1];
            const currX = mapX(curr.time);
            const currY = mapY(curr.value);
            const nextX = mapX(next.time);
            const nextY = mapY(next.value);

            // Control point for smooth curve (midpoint)
            const cpX = (currX + nextX) / 2;
            const cpY = (currY + nextY) / 2;

            ctx.quadraticCurveTo(currX, currY, cpX, cpY);
        }

        // Final point
        const last = dataPoints[dataPoints.length - 1];
        ctx.lineTo(mapX(last.time), mapY(last.value));
        ctx.lineTo(mapX(last.time), padding.top + chartHeight);
        ctx.closePath();

        // Gradient fill
        const gradient = ctx.createLinearGradient(0, padding.top, 0, padding.top + chartHeight);
        gradient.addColorStop(0, 'rgba(38, 139, 210, 0.3)'); // #268bd2 with alpha
        gradient.addColorStop(1, 'rgba(38, 139, 210, 0.05)');
        ctx.fillStyle = gradient;
        ctx.fill();
    }

    // Draw the main line
    if (dataPoints.length > 0) {
        ctx.beginPath();
        ctx.moveTo(mapX(dataPoints[0].time), mapY(dataPoints[0].value));

        for (let i = 0; i < dataPoints.length - 1; i++) {
            const curr = dataPoints[i];
            const next = dataPoints[i + 1];
            const currX = mapX(curr.time);
            const currY = mapY(curr.value);
            const nextX = mapX(next.time);
            const nextY = mapY(next.value);

            // Control point for smooth curve
            const cpX = (currX + nextX) / 2;
            const cpY = (currY + nextY) / 2;

            ctx.quadraticCurveTo(currX, currY, cpX, cpY);
        }

        // Final segment
        const last = dataPoints[dataPoints.length - 1];
        ctx.lineTo(mapX(last.time), mapY(last.value));

        ctx.strokeStyle = '#268bd2'; // Solarized blue
        ctx.lineWidth = 2.5;
        ctx.lineJoin = 'round';
        ctx.lineCap = 'round';
        ctx.stroke();
    }

    // Draw data points as circles (visible when zoomed in)
    if (dataPoints.length <= 50) {
        ctx.fillStyle = '#268bd2';
        ctx.strokeStyle = '#073642'; // Dark background color
        ctx.lineWidth = 2;

        dataPoints.forEach(point => {
            const x = mapX(point.time);
            const y = mapY(point.value);

            ctx.beginPath();
            ctx.arc(x, y, 4, 0, Math.PI * 2);
            ctx.fill();
            ctx.stroke();
        });
    }

    // Chart title
    ctx.fillStyle = 'rgba(255,255,255,0.8)';
    ctx.font = 'bold 12px sans-serif';
    ctx.textAlign = 'left';
    ctx.fillText('Page Count Over Time', padding.left, 18);

    // Data point count indicator (top right)
    ctx.font = '10px monospace';
    ctx.textAlign = 'right';
    ctx.fillStyle = 'rgba(255,255,255,0.5)';
    ctx.fillText(dataPoints.length + ' points', width - padding.right, 18);
}

// Draw empty chart placeholder
function drawEmptyChart(canvas) {
    const sharedEmpty = window.__pm_shared_metrics && window.__pm_shared_metrics.drawEmptyChart;
    if (typeof sharedEmpty === 'function' && sharedEmpty !== drawEmptyChart) {
        return sharedEmpty(canvas);
    }
    const ctx = canvas.getContext('2d');

    // Handle high DPI displays
    const dpr = window.devicePixelRatio || 1;
    const rect = canvas.getBoundingClientRect();
    canvas.width = rect.width * dpr;
    canvas.height = rect.height * dpr;
    ctx.scale(dpr, dpr);

    ctx.clearRect(0, 0, canvas.width, canvas.height);
    ctx.fillStyle = 'rgba(255,255,255,0.3)';
    ctx.font = '12px sans-serif';
    ctx.textAlign = 'center';
    ctx.fillText('No data available', rect.width / 2, rect.height / 2);
}

// Use /devices/list to query database and refresh individual devices as needed

function loadUnknowns() {
    fetch('/unknown_manufacturers').then(r => r.json()).then(lines => {
        const pre = document.getElementById('unknowns');
        pre.textContent = (lines && lines.length) ? lines.join('\n') : '(no entries)';
    }).catch(e => { document.getElementById('unknowns').textContent = 'Failed to load: ' + e });
}

// Inline onclick normalization removed. Buttons should be emitted with
// data-action attributes or wired directly when created. See `common/web/cards.js`
// for delegated action handling.

setInterval(updateMetrics, 2000);

// Connect to SSE for real-time updates (replaces polling)
// If this page is being served through the central server proxy, the server
// injects a meta tag `X-PrintMaster-Proxied`. In that case we must connect
// to the server-side SSE endpoint under `/api/events`. Otherwise use the
// agent-local `/events` endpoint.
const __isProxied = !!document.querySelector('meta[http-equiv="X-PrintMaster-Proxied"]');
const __ssePath = __isProxied ? '/api/events' : '/events';
window.__pm_shared.log('[SSE] connecting to', __ssePath, 'proxied=', __isProxied);
const eventSource = new EventSource(__ssePath);

eventSource.addEventListener('connected', (e) => {
    window.__pm_shared.log('SSE connected:', e.data);
    // Load initial logs when connected
    updateLog();
    refreshServerConnectionUI({ silent: true });
});

eventSource.addEventListener('server_status', (e) => {
    try {
        const payload = JSON.parse(e.data || '{}');
        const status = payload && (payload.status || payload);
        applyServerConnectionStatus(status, { silent: true });
    } catch (err) {
        try { window.__pm_shared.warn('Failed to parse server_status event', err); } catch (_) {}
    }
});

eventSource.addEventListener('log_entry', (e) => {
    const data = JSON.parse(e.data);
    // Format log entry similar to the /logs endpoint format
    let line = data.timestamp + ' [' + data.level + '] ' + data.message;
    if (data.context && Object.keys(data.context).length > 0) {
        for (const [k, v] of Object.entries(data.context)) {
            // Properly stringify the value - handle objects, arrays, and primitives
            let valueStr;
            if (typeof v === 'object' && v !== null) {
                valueStr = JSON.stringify(v);
            } else {
                valueStr = String(v);
            }
            line += ' ' + k + '=' + valueStr;
        }
    }
    
    // Add to our log entries array
    allLogEntries.push(line);
    
    // Limit array size to prevent memory issues (keep last 5000 entries)
    if (allLogEntries.length > 5000) {
        allLogEntries = allLogEntries.slice(-5000);
    }
    
    // Re-filter and display
    filterAndDisplayLogs();
});

eventSource.addEventListener('device_discovered', (e) => {
    window.__pm_shared.log('New device discovered:', e.data);
    
    // Check if modal is open - pause updates to prevent glitchiness
    const overlay = document.getElementById('printer_details_overlay');
    if (overlay && overlay.style.display === 'flex' && overlay.dataset.currentPrinterIp) {
        window.__pm_shared.log('Skipping updatePrinters() - modal is open');
        return;
    }
    
    updatePrinters(); // Refresh device list
});

eventSource.addEventListener('device_updated', (e) => {
    window.__pm_shared.log('Device updated:', e.data);
    
    // Check if modal is open showing this device - don't update to prevent glitchiness
    const overlay = document.getElementById('printer_details_overlay');
    if (overlay && overlay.style.display === 'flex' && overlay.dataset.currentPrinterIp) {
        try {
            const updatedDevice = JSON.parse(e.data);
            const modalIP = overlay.dataset.currentPrinterIp;
            const updatedIP = updatedDevice.ip || updatedDevice.IP || '';
            
            // Skip update if modal is showing this device
            if (modalIP === updatedIP) {
                window.__pm_shared.log('Skipping updatePrinters() - modal open for this device');
                return;
            }
        } catch (err) {
            window.__pm_shared.warn('Failed to parse device_updated event:', err);
        }
    }
    
    updatePrinters(); // Refresh device list
});

eventSource.addEventListener('device_discovering', (e) => {
    const data = JSON.parse(e.data);
    window.__pm_shared.log('Device discovering:', data);
    // Show progressive discovery card (fail-fast if shared renderer unavailable)
    window.__pm_shared_cards.showDiscoveringCard(data);
});

eventSource.addEventListener('metrics_update', (e) => {
    const data = JSON.parse(e.data);
    updateMetricsChart(data);
});

eventSource.addEventListener('update_progress', (e) => {
    try {
        const data = JSON.parse(e.data);
        window.__pm_shared.log('Update progress:', data);
        handleUpdateProgress(data);
    } catch (err) {
        window.__pm_shared.warn('Failed to parse update_progress event:', err);
    }
});

eventSource.onerror = (e) => {
    try {
        window.__pm_shared.error('[SSE] connection error, will auto-reconnect', { error: e, readyState: eventSource.readyState, path: __ssePath, timestamp: new Date().toISOString() });
    } catch (err) {
        try { window.__pm_shared.error('SSE connection error (failed to log details)'); } catch(_){}
    }
};

// Load auto-discover checkbox state on page load (from unified settings)
fetch('/settings').then(r => r.json()).then(all => {
    const disc = all.discovery || {};
    // Apply discovery-related toggles immediately so UI reflects server state on first paint
    const autoDiscoverEl = document.getElementById('auto_discover_checkbox');
    if (autoDiscoverEl) autoDiscoverEl.checked = disc.auto_discover_enabled === true;
    const autosaveEl = document.getElementById('autosave_checkbox');
    if (autosaveEl) autosaveEl.checked = disc.autosave_discovered_devices === true;
    // Ensure 'Show Discovered Devices Anyway' container visibility follows autosave setting
    const showDiscoveredAnywayContainer = document.getElementById('show_discovered_devices_anyway_container');
    if (showDiscoveredAnywayContainer) showDiscoveredAnywayContainer.style.display = (disc.autosave_discovered_devices === true) ? 'flex' : 'none';
}).catch(e => {
    window.__pm_shared.error('Failed to load auto discover state:', e);
});

// UI helpers
function toggleMobileNav() {
    const nav = document.getElementById('mobile_nav');
    if (nav) {
        nav.classList.toggle('open');
    }
}

// Initialize mobile bottom tab bar navigation
function initMobileBottomTabs() {
    const bottomTabs = document.getElementById('mobile_bottom_tabs');
    if (!bottomTabs) return;
    
    const tabItems = bottomTabs.querySelectorAll('.mobile-tab-item');
    tabItems.forEach(item => {
        item.addEventListener('click', () => {
            const target = item.dataset.target;
            if (target) {
                showTab(target);
            }
        });
    });
}

// Update mobile bottom tabs active state when switching tabs
function updateMobileBottomTabsActiveState(targetTab) {
    const bottomTabs = document.getElementById('mobile_bottom_tabs');
    if (!bottomTabs) return;
    
    const tabItems = bottomTabs.querySelectorAll('.mobile-tab-item');
    tabItems.forEach(item => {
        if (item.dataset.target === targetTab) {
            item.classList.add('active');
        } else {
            item.classList.remove('active');
        }
    });
}

// Show/hide RBAC-gated tabs in mobile bottom tab bar
function updateMobileBottomTabsRBAC(role) {
    const bottomTabs = document.getElementById('mobile_bottom_tabs');
    if (!bottomTabs) return;
    
    const adminTabs = bottomTabs.querySelectorAll('.mobile-tab-item[data-role="admin"]');
    adminTabs.forEach(tab => {
        if (role === 'admin') {
            tab.style.display = 'flex';
        } else {
            tab.style.display = 'none';
        }
    });
}

// Theme toggle functionality
function toggleTheme() {
    const checkbox = document.getElementById('theme-toggle-checkbox');
    const body = document.body;

    // Check if View Transitions API is supported
    if (!document.startViewTransition) {
        // Fallback for browsers that don't support View Transitions (Safari, Firefox)
        if (checkbox.checked) {
            body.classList.add('light-mode');
            localStorage.setItem('theme', 'light');
        } else {
            body.classList.remove('light-mode');
            localStorage.setItem('theme', 'dark');
        }
        return;
    }

    // Get the theme toggle button position to center the expanding circle
    const btn = document.getElementById('theme_toggle_btn');
    if (btn) {
        const rect = btn.getBoundingClientRect();
        const x = rect.left + rect.width / 2;
        const y = rect.top + rect.height / 2;

        // Calculate the maximum distance to cover the entire viewport
        const maxRadius = Math.hypot(
            Math.max(x, window.innerWidth - x),
            Math.max(y, window.innerHeight - y)
        );

        // Set CSS custom properties for the circle animation
        document.documentElement.style.setProperty('--circle-x', x + 'px');
        document.documentElement.style.setProperty('--circle-y', y + 'px');
        document.documentElement.style.setProperty('--circle-radius', maxRadius + 'px');
    }

    // Start view transition with expanding circle animation
    const transition = document.startViewTransition(() => {
        if (checkbox.checked) {
            body.classList.add('light-mode');
            localStorage.setItem('theme', 'light');
        } else {
            body.classList.remove('light-mode');
            localStorage.setItem('theme', 'dark');
        }
    });
}

// Load saved theme preference on page load
function loadThemePreference() {
    const savedTheme = localStorage.getItem('theme');
    const checkbox = document.getElementById('theme-toggle-checkbox');

    if (savedTheme === 'light') {
        document.body.classList.add('light-mode');
        if (checkbox) {
            checkbox.checked = true;
        }
    }
}

// Apply masonry layout to settings grid (or any .settings-grid) - GLOBAL FUNCTION
function applyMasonryLayout(targetGrid) {
    const grid = targetGrid || document.querySelector('.settings-grid');
    if (!grid) return;

    if ((grid.dataset && grid.dataset.layout === 'single') || grid.classList.contains('agent-settings-stack')) {
        grid.style.display = '';
        grid.style.flexWrap = '';
        grid.style.gap = '';
        grid.style.alignItems = '';
        return;
    }

    const panels = Array.from(grid.querySelectorAll('.panel'));
    if (panels.length === 0) return;

    // Determine number of columns based on viewport width
    const width = window.innerWidth;
    let numColumns = 1;
    if (width >= 1600) numColumns = 3;
    else if (width >= 1200) numColumns = 2;

    // Reset grid to column layout
    grid.style.display = 'flex';
    grid.style.flexWrap = 'wrap';
    grid.style.gap = '0';
    grid.style.alignItems = 'flex-start';

    // Create column containers
    const columns = [];
    for (let i = 0; i < numColumns; i++) {
        const col = document.createElement('div');
        col.style.flex = '1';
        col.style.minWidth = '0';
        col.style.display = 'flex';
        col.style.flexDirection = 'column';
        col.style.gap = '16px';
        col.style.padding = '0 8px';
        columns.push(col);
    }

    // Track column heights
    const columnHeights = new Array(numColumns).fill(0);

    // Distribute panels to shortest column
    panels.forEach(panel => {
        // Find shortest column
        const shortestIndex = columnHeights.indexOf(Math.min(...columnHeights));
        
        // Add panel to that column
        columns[shortestIndex].appendChild(panel);
        
        // Update column height (approximate based on offsetHeight)
        columnHeights[shortestIndex] += panel.offsetHeight + 16; // +16 for gap
    });

    // Clear grid and append columns
    grid.innerHTML = '';
    columns.forEach(col => grid.appendChild(col));
}

// Load theme preference when page loads
// Global state: check if UI is being accessed through server proxy
let isProxiedFromServer = false;
// Shared auth utilities now manage currentUser and role-based visibility.

document.addEventListener('DOMContentLoaded', async function () {
    await window.__pm_auth.ensureAuth();
    loadThemePreference();
    window.__pm_shared.toggleDatabaseFields();

    initializeAutoUpdatePanel();
    
    // Initialize header info bar
    updateHeaderInfoBar();

    // Check if we're being accessed through the server's proxy
    // The server adds a special meta tag when proxying
    const proxiedMeta = document.querySelector('meta[http-equiv="X-PrintMaster-Proxied"]');
    if (proxiedMeta) {
        isProxiedFromServer = true;
        window.__pm_shared.log('Agent UI is being accessed through server proxy - disabling nested proxy features');
    }

    // Check for database rotation warning on first page load (provided by shared cards)
    window.__pm_shared_cards.checkDatabaseRotationWarning();

    // Initialize advanced settings visibility
    const advancedVisible = localStorage.getItem('settingsAdvancedVisible') === 'true';
    const advancedCheckbox = document.getElementById('settings_advanced_toggle');
    if (advancedCheckbox) {
        advancedCheckbox.checked = advancedVisible;
        // Apply initial state without animation
        const advancedElements = document.querySelectorAll('.advanced-setting');
        advancedElements.forEach(el => {
            if (advancedVisible) {
                // Use flex for mini-toggle-container, default for others
                el.style.display = el.classList.contains('mini-toggle-container') ? 'flex' : '';
            } else {
                el.style.display = 'none';
            }
        });
    }

    // Attach event listeners for elements that had inline handlers
    const themeToggle = document.getElementById('theme-toggle-checkbox');
    if (themeToggle) {
        themeToggle.addEventListener('change', toggleTheme);
    }

    // Initialize mobile bottom tab bar
    initMobileBottomTabs();
    
    // Initialize devices tab controls (sidebar, filters, view toggle)
    initDevicesTabControls();

    // Old hamburger menu (deprecated - kept for backwards compatibility)
    const hamburger = document.querySelector('.hamburger-menu');
    if (hamburger) {
        hamburger.addEventListener('click', toggleMobileNav);
    }

    const runDiscoverNow = function (btn) {
        const originalText = btn ? btn.textContent : null;
        if (btn) { btn.disabled = true; btn.textContent = 'Discovering...'; }
        window.__pm_shared.showToast('Discovery scan started...', 'info');
        fetch("/discover", { method: "POST" }).then(async (r) => {
            if (!r.ok) {
                const t = await r.text().catch(() => r.statusText);
                window.__pm_shared.showToast('Discovery failed: ' + t, 'error');
                return;
            }
            window.__pm_shared.showToast('Discovery scan complete', 'success');
            updatePrinters();
        }).catch(e => {
            window.__pm_shared.error('Discover Now failed', e);
            window.__pm_shared.showToast('Discovery failed: ' + e.message, 'error');
        }).finally(() => {
            if (btn) { btn.disabled = false; btn.textContent = originalText; }
        });
    };

    const discoverBtn = document.getElementById('discover_now_btn');
    if (discoverBtn) {
        discoverBtn.addEventListener('click', function () { runDiscoverNow(discoverBtn); });
    }

    const discoverSettingsBtn = document.getElementById('discover_now_settings_btn');
    if (discoverSettingsBtn) {
        discoverSettingsBtn.addEventListener('click', function () { runDiscoverNow(discoverSettingsBtn); });
    }

    // Immediate visibility handlers for "Show Anyway" containers (no save, just show/hide)
    const autoDiscoverCheckbox = document.getElementById('auto_discover_checkbox');
    if (autoDiscoverCheckbox) {
        autoDiscoverCheckbox.addEventListener('change', function () {
            const showAnywayContainer = document.getElementById('show_discover_button_anyway_container');
            const showAnywayToggle = document.getElementById('show_discover_button_anyway');
            if (showAnywayContainer) {
                showAnywayContainer.style.display = this.checked ? 'flex' : 'none';
                // When hiding, uncheck the toggle to prevent unexpected UI state
                if (!this.checked && showAnywayToggle) {
                    showAnywayToggle.checked = false;
                }
            }
        });
    }

    // Note: Autosave checkbox immediate visibility handlers moved to toggleAutosaveUI()
    // to consolidate all autosave logic in one place

    // Passive Discovery master toggle - shows/hides methods and preserves state
    const passiveDiscoveryCheckbox = document.getElementById('passive_discovery_enabled');
    if (passiveDiscoveryCheckbox) {
        // Store previous state of passive discovery methods
        if (!window.__passiveDiscoveryState) {
            window.__passiveDiscoveryState = {};
        }

        passiveDiscoveryCheckbox.addEventListener('change', function () {
            const container = document.getElementById('passive_discovery_methods_container');
            const methodIds = ['discovery_live_wsd_enabled', 'discovery_live_mdns_enabled',
                'discovery_live_ssdp_enabled', 'discovery_live_snmptrap_enabled',
                'discovery_live_llmnr_enabled'];

            if (container) {
                if (this.checked) {
                    // Show container
                    container.style.display = 'block';
                    // Restore previous states
                    methodIds.forEach(id => {
                        const el = document.getElementById(id);
                        if (el && window.__passiveDiscoveryState[id] !== undefined) {
                            el.checked = window.__passiveDiscoveryState[id];
                        }
                    });
                } else {
                    // Save current states before hiding
                    methodIds.forEach(id => {
                        const el = document.getElementById(id);
                        if (el) {
                            window.__passiveDiscoveryState[id] = el.checked;
                            el.checked = false;
                        }
                    });
                    // Hide container
                    container.style.display = 'none';
                }
            }
        });
    }

    // Manual IP ranges toggle - show/hide the ranges textarea
    const manualRangesCheckbox = document.getElementById('manual_ranges_enabled');
    if (manualRangesCheckbox) {
        manualRangesCheckbox.addEventListener('change', function () {
            const container = document.getElementById('manual_ranges_container');
            if (container) {
                container.style.display = this.checked ? 'block' : 'none';
            }
        });
    }

    const showDiscoverAnywayToggle = document.getElementById('show_discover_button_anyway');
    if (showDiscoverAnywayToggle) {
        showDiscoverAnywayToggle.addEventListener('change', function () {
            const discoverBtn = document.getElementById('discover_now_btn');
            if (discoverBtn) {
                if (this.checked) {
                    discoverBtn.style.display = 'inline-block';
                    discoverBtn.style.opacity = '1';
                    discoverBtn.style.transform = 'scale(1)';
                } else {
                    discoverBtn.style.display = 'none';
                }
            }
        });
    }

    const showDiscoveredAnywayToggle = document.getElementById('show_discovered_devices_anyway');
    if (showDiscoveredAnywayToggle) {
        showDiscoveredAnywayToggle.addEventListener('change', function () {
            // Only affect "Show Known Devices" if this toggle is visible (i.e., autosave is enabled)
            const container = document.getElementById('show_discovered_devices_anyway_container');
            const isVisible = container && container.style.display !== 'none';
            
            if (isVisible) {
                // Force-enable the "Show Known Devices" checkbox when this is toggled on
                const showSavedCheckbox = document.getElementById('show_saved_in_discovered');
                if (showSavedCheckbox && this.checked) {
                    showSavedCheckbox.checked = true;
                }
            }
            updatePrinters();
        });
    }

    const showSavedCheckbox = document.getElementById('show_saved_in_discovered');
    if (showSavedCheckbox) {
        showSavedCheckbox.addEventListener('change', updatePrinters);
    }

    const settingsAutosave = document.getElementById('settings_autosave');
    if (settingsAutosave) {
        settingsAutosave.addEventListener('change', toggleAutoSave);
    }

    const settingsAdvanced = document.getElementById('settings_advanced_toggle');
    if (settingsAdvanced) {
        settingsAdvanced.addEventListener('change', toggleAdvancedSettings);
    }

    const scanLocalSubnet = document.getElementById('scan_local_subnet_enabled');
    if (scanLocalSubnet) {
        scanLocalSubnet.addEventListener('change', toggleRangesDropdown);
    }

    const manualRanges = document.getElementById('manual_ranges_enabled');
    if (manualRanges) {
        manualRanges.addEventListener('change', toggleRangesDropdown);
    }

    const dbBackendType = document.getElementById('db_backend_type');
    if (dbBackendType) {
    dbBackendType.addEventListener('change', function () { window.__pm_shared.toggleDatabaseFields(); });
    }

    // Attach tab button listeners
    document.querySelectorAll('.tab').forEach(btn => {
        btn.addEventListener('click', function () {
            const target = this.getAttribute('data-target');
            if (target) showTab(target);
        });
    });

    // Attach settings link listener
    const settingsLinks = document.querySelectorAll('.settings-link');
    settingsLinks.forEach(link => {
        link.addEventListener('click', function (e) {
            e.preventDefault();
            showTab('settings');
            return false;
        });
    });

    // Attach settings button listeners
    const applyBtn = document.getElementById('settings_apply_btn');
    if (applyBtn) {
        applyBtn.addEventListener('click', function () {
            saveAllSettings(this);
        });
    }
    const retrySettingsBtn = document.getElementById('settings_load_retry_btn');
    if (retrySettingsBtn) retrySettingsBtn.addEventListener('click', () => loadSettings().catch(() => {}));
    document.querySelectorAll('[data-tab="settings"] input, [data-tab="settings"] select, [data-tab="settings"] textarea')
        .forEach(input => {
            if (input.id === 'settings_autosave' || input.id === 'settings_advanced_toggle') return;
            input.addEventListener('input', markSettingsDirty);
            input.addEventListener('change', markSettingsDirty);
        });

    const revertBtn = document.getElementById('settings_revert_btn');
    if (revertBtn) {
        revertBtn.addEventListener('click', () => loadSettings().catch(() => {}));
    }

    const resetBtn = document.getElementById('settings_reset_btn');
    if (resetBtn) {
        resetBtn.addEventListener('click', resetSettings);
    }

    // Discovered search filter
    const discoveredSearch = document.getElementById('discovered_search');
    if (discoveredSearch) {
        discoveredSearch.addEventListener('input', function () {
            filterDiscoveredCards(this.value);
        });
    }

    // Discovered section buttons (use data-action attributes)
    const saveAllBtn = document.querySelector('#discovered_section button[data-action="save-all-discovered"]');
    if (saveAllBtn) {
        saveAllBtn.addEventListener('click', saveAllDiscovered);
    }

    const clearDiscBtn = document.querySelector('#discovered_section button[data-action="clear-discovered"]');
    if (clearDiscBtn) {
        clearDiscBtn.addEventListener('click', clearDiscovered);
    }

    // Delete all saved devices button (data-action="delete-all-saved")
    const deleteAllBtn = document.querySelector('button[data-action="delete-all-saved"]');
    if (deleteAllBtn) {
        deleteAllBtn.addEventListener('click', deleteAllSavedDevices);
    }

    // Clear ranges button
    const clearRangesBtn = document.querySelector('button[onclick*="clearRanges"]');
    if (clearRangesBtn) {
        clearRangesBtn.removeAttribute('onclick');
        clearRangesBtn.addEventListener('click', clearRanges);
    }

    // Regenerate certificates button
    const regenerateCertsBtn = document.getElementById('regenerate_certs_btn');
    if (regenerateCertsBtn) {
        regenerateCertsBtn.addEventListener('click', async function () {
            const _confirmed = await window.__pm_shared.showConfirm('Regenerate TLS certificates? This will create new self-signed certificates. You will need to restart the agent for changes to take effect.', 'Regenerate Certificates', true);
            if (!_confirmed) return;
            try {
                const response = await fetch('/api/regenerate-certs', { method: 'POST' });
                const result = await response.json();
                    if (response.ok) {
                    window.__pm_shared.showAlert(result.message + '\n\nCert: ' + result.cert + '\nKey: ' + result.key, 'Certificates generated', false, false);
                } else {
                    window.__pm_shared.showAlert('Failed to regenerate certificates: ' + (result.error || response.statusText), 'Regenerate Certificates', true, false);
                }
            } catch (err) {
                window.__pm_shared.showAlert('Error regenerating certificates: ' + err.message, 'Regenerate Certificates', true, false);
            }
        });
    }

    // Trace tag controls use stable IDs because they have no inline handlers.
    const loadTraceBtn = document.getElementById('trace_tags_refresh_btn');
    if (loadTraceBtn) loadTraceBtn.addEventListener('click', () => loadTraceTags({ confirmDiscard: true }));
    const saveTraceBtn = document.getElementById('trace_tags_save_btn');
    if (saveTraceBtn) saveTraceBtn.addEventListener('click', saveTraceTags);

    // Clear database button
    const clearDbBtn = document.getElementById('clear_database_btn');
    if (clearDbBtn) {
        clearDbBtn.addEventListener('click', clearDatabase);
    }

    // Log buttons
    const copyLogsBtn = document.getElementById('copy_logs_btn');
    if (copyLogsBtn) {
        copyLogsBtn.addEventListener('click', copyLogs);
    }

    const downloadLogsBtn = document.getElementById('download_logs_btn');
    if (downloadLogsBtn) {
        downloadLogsBtn.addEventListener('click', downloadLogs);
    }

    const clearLogBtn = document.getElementById('clear_log_btn');
    if (clearLogBtn) {
        clearLogBtn.addEventListener('click', clearLogs);
    }
    const retryLogsBtn = document.getElementById('logs_load_retry_btn');
    if (retryLogsBtn) retryLogsBtn.addEventListener('click', updateLog);
    
    // Log filters
    const logLevelFilter = document.getElementById('log_level_filter');
    if (logLevelFilter) {
        logLevelFilter.addEventListener('change', filterAndDisplayLogs);
    }
    
    const logSearchFilter = document.getElementById('log_search_filter');
    if (logSearchFilter) {
        logSearchFilter.addEventListener('input', filterAndDisplayLogs);
    }

    // Initialize log view mode toggle
    initLogViewModeToggle();

    // Printer details modal close button, X button, and overlay backdrop click
    const detailsOverlay = document.getElementById('printer_details_overlay');
    const modalCloseBtn = document.querySelector('#printer_details_actions button');
    const printerDetailsCloseX = document.getElementById('printer_details_close_x');
    
    const closePrinterDetailsModal = function() {
        if (detailsOverlay) {
            detailsOverlay.style.display = 'none';
            document.body.style.overflow = '';
            delete detailsOverlay.dataset.currentPrinterIp;
        }
    };
    
    if (modalCloseBtn) {
        modalCloseBtn.addEventListener('click', closePrinterDetailsModal);
    }
    
    if (printerDetailsCloseX) {
        printerDetailsCloseX.addEventListener('click', closePrinterDetailsModal);
    }
    
    // Close modal when clicking backdrop
    if (detailsOverlay) {
        detailsOverlay.addEventListener('click', function(e) {
            if (e.target === detailsOverlay) {
                closePrinterDetailsModal();
            }
        });
    }

    // WebUI modal buttons
    const webuiProxyBtn = document.getElementById('webui_proxy_btn');
    const webuiDirectBtn = document.getElementById('webui_direct_btn');
    const webuiCloseBtn = document.querySelector('.webui-modal-close');
    const webuiCloseX = document.getElementById('webui_modal_close_x');
    const webuiModal = document.getElementById('webui_modal');
    
    if (webuiProxyBtn) {
        // Disable proxy button if we're already being proxied from server
        if (isProxiedFromServer) {
            webuiProxyBtn.disabled = true;
            webuiProxyBtn.style.opacity = '0.5';
            webuiProxyBtn.style.cursor = 'not-allowed';
            
            // Update button text to explain why it's disabled
            const proxyTitle = webuiProxyBtn.querySelector('.webui-modal-button-title');
            const proxyDesc = webuiProxyBtn.querySelector('.webui-modal-button-desc');
            if (proxyTitle) proxyTitle.textContent = '🔐 Proxy UI (Already Proxied)';
            if (proxyDesc) proxyDesc.textContent = 'You are already accessing this through the server proxy';
        } else {
            webuiProxyBtn.addEventListener('click', openProxyUI);
        }
    }
    
    if (webuiDirectBtn) {
        webuiDirectBtn.addEventListener('click', openDirectUI);
    }
    
    if (webuiCloseBtn) {
        webuiCloseBtn.addEventListener('click', closeWebUIModal);
    }
    
    if (webuiCloseX) {
        webuiCloseX.addEventListener('click', closeWebUIModal);
    }
    
    // Close webui modal when clicking backdrop
    if (webuiModal) {
        webuiModal.addEventListener('click', function(e) {
            if (e.target === webuiModal) {
                closeWebUIModal();
            }
        });
    }

    // Apply masonry on load
    applyMasonryLayout();

    // Reapply on window resize (debounced)
    let resizeTimeout;
    window.addEventListener('resize', function() {
        clearTimeout(resizeTimeout);
        resizeTimeout = setTimeout(() => {
            // Get all panels from all columns
            const grid = document.querySelector('.settings-grid');
            if (!grid || (grid.dataset && grid.dataset.layout === 'single') || grid.classList.contains('agent-settings-stack')) return;
            const allPanels = Array.from(grid.querySelectorAll('.panel'));
            
            // Store panels temporarily
            const tempContainer = document.createElement('div');
            allPanels.forEach(panel => tempContainer.appendChild(panel));
            
            // Clear grid
            grid.innerHTML = '';
            
            // Re-add panels to grid
            allPanels.forEach(panel => grid.appendChild(panel));
            
            // Reapply masonry
            applyMasonryLayout();
        }, 250);
    });

    // Reapply when advanced settings toggle (affects panel visibility/height)
    const advancedToggle = document.getElementById('show_advanced');
    if (advancedToggle) {
        advancedToggle.addEventListener('change', function() {
            setTimeout(() => {
                const grid = document.querySelector('.settings-grid');
                if (!grid || (grid.dataset && grid.dataset.layout === 'single') || grid.classList.contains('agent-settings-stack')) return;
                applyMasonryLayout();
            }, 100);
        });
    }

    restoreAgentActiveTab();
});

function showTab(name) {
    const all = document.querySelectorAll('[data-tab]');
    all.forEach(el => { el.classList.add('hidden'); });
    document.querySelectorAll('[data-tab="' + name + '"]').forEach(el => { el.classList.remove('hidden'); });

    // toggle active class on tab buttons (both desktop and mobile)
    const tabs = document.querySelectorAll('.tab');
    tabs.forEach(t => { t.classList.remove('active'); });
    const activeBtns = document.querySelectorAll('.tab[data-target="' + name + '"]');
    activeBtns.forEach(btn => { btn.classList.add('active'); });

    // Update mobile bottom tabs active state
    updateMobileBottomTabsActiveState(name);

    // Update mobile menu label (legacy)
    const label = document.getElementById('current_tab_label');
    if (label) {
        const tabNames = {
            'devices': 'Devices',
            'settings': 'Settings',
            'logs': 'Logs'
        };
        label.textContent = 'Menu - ' + (tabNames[name] || name);
    }

    // Close mobile nav after selection (legacy)
    const nav = document.getElementById('mobile_nav');
    if (nav) {
        nav.classList.remove('open');
    }


    // if devices tab, refresh both discovered and saved devices tables
    if (name === 'devices') {
        updatePrinters();
    }

    // if settings tab, load settings
    if (name === 'settings') {
        loadSettings().catch(() => {});
    }

    if (isAgentTabSelectable(name)) {
        persistAgentUIState(AGENT_UI_STATE_KEYS.ACTIVE_TAB, name);
    }
}

function isAgentTabSelectable(name) {
    if (!name) return false;
    const panel = document.querySelector(`[data-tab="${name}"]`);
    if (!panel) return false;
    const buttons = Array.from(document.querySelectorAll(`.tab[data-target="${name}"]`));
    if (buttons.length === 0) {
        return true;
    }
    return buttons.some(btn => btn.offsetParent !== null);
}

function restoreAgentActiveTab() {
    const saved = getAgentUIState(AGENT_UI_STATE_KEYS.ACTIVE_TAB, null);
    if (saved && isAgentTabSelectable(saved)) {
        showTab(saved);
        return;
    }
    showTab('devices');
}



// Modal handlers


// Track server-managed state globally
let serverManagedSections = new Set();
let settingsLoadedSuccessfully = false;
let settingsDirty = false;

function updateSettingsDirtyState() {
    const applyButton = document.getElementById('settings_apply_btn');
    if (applyButton) {
        applyButton.disabled = !settingsLoadedSuccessfully;
        applyButton.textContent = settingsDirty ? 'Apply *' : 'Apply';
    }
}

function markSettingsDirty() {
    if (!settingsLoadedSuccessfully) return;
    settingsDirty = true;
    updateSettingsDirtyState();
}

function updateSettingsLoadState(error) {
    const errorPanel = document.getElementById('settings_load_error');
    const message = document.getElementById('settings_load_error_message');
    if (errorPanel) errorPanel.hidden = !error;
    if (message) {
        if (!error) {
            message.textContent = '';
        } else if (settingsLoadedSuccessfully) {
            message.textContent = 'Settings could not be refreshed: ' + error.message + '. Previously loaded settings remain available.';
        } else {
            message.textContent = 'Settings could not be loaded: ' + error.message + '. Apply and auto-save are paused until settings load successfully.';
        }
    }
    const applyButton = document.getElementById('settings_apply_btn');
    const resetButton = document.getElementById('settings_reset_btn');
    if (applyButton) applyButton.disabled = !settingsLoadedSuccessfully;
    if (resetButton) resetButton.disabled = !settingsLoadedSuccessfully;
}

function showSettingsSaveError(error) {
    const errorPanel = document.getElementById('settings_save_error');
    if (!errorPanel) return;
    errorPanel.textContent = 'Settings were not saved: ' + error.message;
    errorPanel.hidden = false;
}

// Apply server-managed state to UI elements - disables fields and shows badge
function applyServerManagedState() {
    // Map section names to their container panel selectors
    const sectionSelectors = {
        'discovery': [
            '#settings [data-section="discovery"]',
            '.settings-grid .panel:has(#scan_local_subnet_enabled)',
            '.settings-grid .panel:has(#discovery_arp_enabled)',
            '.settings-grid .panel:has(#auto_discover_checkbox)',
            '.settings-grid .panel:has(#passive_discovery_enabled)',
            '.settings-grid .panel:has(#metrics_rescan_enabled)'
        ]
    };

    // Remove any existing server-managed badges
    document.querySelectorAll('.server-managed-badge').forEach(el => el.remove());

    // For each managed section, disable inputs and add badge
    for (const section of serverManagedSections) {
        const selectors = sectionSelectors[section] || [];
        for (const sel of selectors) {
            document.querySelectorAll(sel).forEach(panel => {
                // Add badge to panel header if not already present
                const header = panel.querySelector('h4');
                if (header && !header.querySelector('.server-managed-badge')) {
                    const badge = document.createElement('span');
                    badge.className = 'server-managed-badge';
                    badge.style.cssText = 'font-size:11px;color:var(--primary);background:rgba(0,122,255,0.15);padding:2px 6px;border-radius:4px;margin-left:8px;font-weight:normal;';
                    badge.textContent = 'Server Override';
                    header.appendChild(badge);
                }
                // Disable all inputs within the panel
                panel.querySelectorAll('input, select, textarea').forEach(input => {
                    input.disabled = true;
                });
            });
        }
    }

    // Also handle discovery-specific elements by ID
    if (serverManagedSections.has('discovery')) {
        const discoveryInputIds = [
            'scan_local_subnet_enabled', 'manual_ranges_enabled', 'ip_scanning_enabled',
            'discovery_arp_enabled', 'discovery_icmp_enabled', 'discovery_tcp_enabled',
            'discovery_snmp_enabled', 'discovery_mdns_enabled',
            'discovery_live_mdns_enabled', 'discovery_live_wsd_enabled',
            'discovery_live_ssdp_enabled', 'discovery_live_snmptrap_enabled',
            'discovery_live_llmnr_enabled', 'passive_discovery_enabled',
            'metrics_rescan_enabled', 'metrics_rescan_interval',
            'auto_discover_checkbox', 'autosave_checkbox',
            'show_discover_button_anyway', 'show_discovered_devices_anyway',
            'ranges_input'
        ];
        for (const id of discoveryInputIds) {
            const el = document.getElementById(id);
            if (el) el.disabled = true;
        }
    }
}

// Load settings from /settings endpoint and populate ALL UI elements
function loadSettings() {
    return fetch('/settings').then(async r => {
        if (!r.ok) {
            let detail = '';
            try { detail = await r.text(); } catch (e) {}
            throw new Error('Request failed (' + r.status + ')' + (detail ? ': ' + detail : ''));
        }
        const s = await r.json();
        const snmp = s.snmp || {};
        const feat = s.features || {};
        const log = s.logging || {};
        const disc = s.discovery || {};
        const web = s.web || {};
        const spooler = s.spooler || {};

        // Track which sections are server-managed
        serverManagedSections = new Set(s.managed_sections || []);
        applyServerManagedState();

        // Store web settings globally for use in device modal rendering
        globalSettings.web = web;
        globalSettings.features = feat;
        globalSettings.spooler = spooler;

        // Populate all form fields from settings
        // Logging settings
        document.getElementById('dev_debug_logging').value = log.level || 'info';
        document.getElementById('dev_dump_parse_debug').checked = !!log.dump_parse_debug;
        
        // SNMP settings - basic
        document.getElementById('dev_snmp_version').value = snmp.version || '2c';
        document.getElementById('dev_snmp_community').value = snmp.community || '';
        document.getElementById('dev_snmp_timeout').value = snmp.timeout_ms || 2000;
        document.getElementById('dev_snmp_retries').value = snmp.retries || 1;
        
        // SNMP settings - v3 security
        document.getElementById('dev_snmp_security_level').value = snmp.security_level || 'authPriv';
        document.getElementById('dev_snmp_username').value = snmp.username || '';
        document.getElementById('dev_snmp_auth_protocol').value = snmp.auth_protocol || 'SHA256';
        document.getElementById('dev_snmp_auth_password').value = snmp.auth_password || '';
        document.getElementById('dev_snmp_priv_protocol').value = snmp.priv_protocol || 'AES';
        document.getElementById('dev_snmp_priv_password').value = snmp.priv_password || '';
        document.getElementById('dev_snmp_context_name').value = snmp.context_name || '';
        
        // Update SNMPv3 UI visibility based on version
        toggleSNMPv3Settings();
        toggleSNMPv3AuthPriv();
        
        // Features settings
        document.getElementById('dev_asset_id_regex').value = feat.asset_id_regex || '';
        const epsonRemoteToggle = document.getElementById('dev_epson_remote_mode');
        if (epsonRemoteToggle) {
            epsonRemoteToggle.checked = feat.epson_remote_mode_enabled === true;
        }
        
        // Discovery settings
        document.getElementById('dev_discover_concurrency').value = disc.concurrency || 50;
        document.getElementById('scan_local_subnet_enabled').checked = disc.subnet_scan !== false;
        document.getElementById('manual_ranges_enabled').checked = disc.manual_ranges !== false;
    // Master IP scanning toggle (default enabled)
    const ipScanEl = document.getElementById('ip_scanning_enabled');
    if (ipScanEl) { ipScanEl.checked = disc.ip_scanning_enabled !== false; }
        document.getElementById('discovery_arp_enabled').checked = disc.arp_enabled !== false;
        document.getElementById('discovery_icmp_enabled').checked = disc.icmp_enabled !== false;
        document.getElementById('discovery_tcp_enabled').checked = disc.tcp_enabled !== false;
        document.getElementById('discovery_snmp_enabled').checked = disc.snmp_enabled !== false;
        document.getElementById('discovery_mdns_enabled').checked = disc.mdns_enabled === true;
        document.getElementById('discovery_live_mdns_enabled').checked = disc.auto_discover_live_mdns === true;
        document.getElementById('discovery_live_wsd_enabled').checked = disc.auto_discover_live_wsd === true;
        document.getElementById('discovery_live_ssdp_enabled').checked = disc.auto_discover_live_ssdp === true;
        document.getElementById('discovery_live_snmptrap_enabled').checked = disc.auto_discover_live_snmptrap === true;
        document.getElementById('discovery_live_llmnr_enabled').checked = disc.auto_discover_live_llmnr === true;
        document.getElementById('metrics_rescan_enabled').checked = disc.metrics_rescan_enabled === true;
        document.getElementById('metrics_rescan_interval').value = disc.metrics_rescan_interval_minutes ?? 60;
        document.getElementById('auto_discover_checkbox').checked = disc.auto_discover_enabled === true;
        document.getElementById('autosave_checkbox').checked = disc.autosave_discovered_devices === true;

        // Load the "Show Anyway" toggle states
        document.getElementById('show_discover_button_anyway').checked = disc.show_discover_button_anyway === true;
        document.getElementById('show_discovered_devices_anyway').checked = disc.show_discovered_devices_anyway === true;

        // Load passive discovery master toggle and determine if any methods are enabled
        const anyPassiveMethodEnabled = disc.auto_discover_live_mdns === true ||
            disc.auto_discover_live_wsd === true ||
            disc.auto_discover_live_ssdp === true ||
            disc.auto_discover_live_snmptrap === true ||
            disc.auto_discover_live_llmnr === true;
        const passiveEnabled = disc.passive_discovery_enabled !== false && anyPassiveMethodEnabled;
        document.getElementById('passive_discovery_enabled').checked = passiveEnabled;

        // Update UI state based on loaded settings
    // Ensure ranges toggle and IP scanning UI reflect server settings. Some browsers
    // or racey initialization can cause the checkbox state to be overwritten by
    // other startup handlers; apply the desired state and re-run the UI toggles
    // immediately and once again after a short delay to be robust.
    const desiredManualRanges = disc.manual_ranges !== false;
    const manualRangesEl = document.getElementById('manual_ranges_enabled');
    if (manualRangesEl) {
        manualRangesEl.checked = desiredManualRanges;
    }
    toggleRangesDropdown();
    toggleIPScanningUI();
    // Re-apply after a tick in case other init code overwrote the checkbox
    setTimeout(() => {
        const el = document.getElementById('manual_ranges_enabled');
        if (el) {
            el.checked = desiredManualRanges;
        }
        toggleRangesDropdown();
        toggleIPScanningUI();
    }, 60);
        updateSubnetDisplay();
        document.getElementById('discover_now_btn').style.display = (disc.auto_discover_enabled === true) ? 'none' : 'inline-block';

        // Update the visibility of the "Show Anyway" toggle containers based on loaded settings
        const showDiscoverAnywayContainer = document.getElementById('show_discover_button_anyway_container');
        if (showDiscoverAnywayContainer) {
            showDiscoverAnywayContainer.style.display = (disc.auto_discover_enabled === true) ? 'flex' : 'none';
        }
        const showDiscoveredAnywayContainer = document.getElementById('show_discovered_devices_anyway_container');
        if (showDiscoveredAnywayContainer) {
            showDiscoveredAnywayContainer.style.display = (disc.autosave_discovered_devices === true) ? 'flex' : 'none';
        }

        // Update passive discovery methods container visibility
        const passiveMethodsContainer = document.getElementById('passive_discovery_methods_container');
        if (passiveMethodsContainer) {
            passiveMethodsContainer.style.display = passiveEnabled ? 'block' : 'none';
        }

        // Update manual ranges container visibility
        const manualRangesContainer = document.getElementById('manual_ranges_container');
        const manualRangesEnabled = document.getElementById('manual_ranges_enabled');
        if (manualRangesContainer && manualRangesEnabled) {
            manualRangesContainer.style.display = manualRangesEnabled.checked ? 'block' : 'none';
        }

        // Populate credentials setting from features
        const credentialsCheckbox = document.getElementById('enable_saved_credentials');
        if (credentialsCheckbox) {
            credentialsCheckbox.checked = feat.credentials_enabled !== false;
        }

        // Populate network settings (HTTP/HTTPS) from web settings
        const enableHttpCheckbox = document.getElementById('enable_http');
        if (enableHttpCheckbox) {
            enableHttpCheckbox.checked = web.enable_http !== false;
        }
        const httpPortInput = document.getElementById('http_port');
        if (httpPortInput) {
            httpPortInput.value = web.http_port || '8080';
        }
        const enableHttpsCheckbox = document.getElementById('enable_https');
        if (enableHttpsCheckbox) {
            enableHttpsCheckbox.checked = web.enable_https !== false;
        }
        const httpsPortInput = document.getElementById('https_port');
        if (httpsPortInput) {
            httpsPortInput.value = web.https_port || '8443';
        }
        const redirectCheckbox = document.getElementById('redirect_http_to_https');
        if (redirectCheckbox) {
            redirectCheckbox.checked = web.redirect_http_to_https === true;
        }
        const customCertPathInput = document.getElementById('custom_cert_path');
        if (customCertPathInput) {
            customCertPathInput.value = web.custom_cert_path || '';
        }
        const customKeyPathInput = document.getElementById('custom_key_path');
        if (customKeyPathInput) {
            customKeyPathInput.value = web.custom_key_path || '';
        }

        // Apply the "Show Discover Button Anyway" toggle state
        if (disc.show_discover_button_anyway === true && disc.auto_discover_enabled === true) {
            document.getElementById('discover_now_btn').style.display = 'inline-block';
        }

        // Apply the "Show Discovered Devices Anyway" toggle state (only if autosave is enabled)
        if (disc.show_discovered_devices_anyway === true && disc.autosave_discovered_devices === true) {
            const showSavedCheckbox = document.getElementById('show_saved_in_discovered');
            if (showSavedCheckbox) {
                showSavedCheckbox.checked = true;
            }
        }

        // Populate spooler/local printer tracking settings
        const spoolerEnabledEl = document.getElementById('spooler_enabled');
        if (spoolerEnabledEl) {
            spoolerEnabledEl.checked = spooler.enabled !== false;
        }
        const spoolerPollIntervalEl = document.getElementById('spooler_poll_interval');
        if (spoolerPollIntervalEl) {
            spoolerPollIntervalEl.value = spooler.poll_interval_seconds || 30;
        }
        const spoolerIncludeNetworkEl = document.getElementById('spooler_include_network');
        if (spoolerIncludeNetworkEl) {
            spoolerIncludeNetworkEl.checked = spooler.include_network_printers === true;
        }
        const spoolerIncludeVirtualEl = document.getElementById('spooler_include_virtual');
        if (spoolerIncludeVirtualEl) {
            spoolerIncludeVirtualEl.checked = spooler.include_virtual_printers === true;
        }
        // Show/hide spooler options based on enabled state
        const spoolerOptionsContainer = document.getElementById('spooler_options_container');
        if (spoolerOptionsContainer) {
            spoolerOptionsContainer.style.display = spooler.enabled !== false ? 'block' : 'none';
        }
        
        // Update Local Printers tab visibility based on spooler enabled state
        if (typeof updateLocalPrintersTabVisibility === 'function') {
            updateLocalPrintersTabVisibility(spooler.enabled !== false);
        }

        settingsLoadedSuccessfully = true;
        settingsDirty = false;
        updateSettingsLoadState();
        updateSettingsDirtyState();
        const saveError = document.getElementById('settings_save_error');
        if (saveError) { saveError.hidden = true; saveError.textContent = ''; }
        loadTraceTags();
    }).catch(e => {
        updateSettingsLoadState(e);
        window.__pm_shared.error('loadSettings failed', e);
        throw e;
    });
}

let traceTagsDirty = false;
let traceTagsLoading = false;
let traceTagsSaving = false;

function updateTraceTagsSaveButton() {
    const button = document.getElementById('trace_tags_save_btn');
    if (!button) return;
    button.disabled = !traceTagsDirty || traceTagsLoading || traceTagsSaving;
    button.textContent = traceTagsDirty ? 'Save Trace Tags *' : 'Save Trace Tags';
}

// Available trace tag categories for granular logging, organized by section
const TRACE_TAG_CATEGORIES = {
    'Proxy': [
        { id: 'proxy_request', label: 'Requests' },
        { id: 'proxy_response', label: 'Responses' },
        { id: 'proxy_director', label: 'Director/Header Rewrites' },
        { id: 'proxy_body_rewrite', label: 'Body Rewrites' }
    ],
    'SNMP': [
        { id: 'snmp_walk', label: 'SNMP Walks' },
        { id: 'snmp_trap', label: 'SNMP Traps' }
    ],
    'Metrics': [
        { id: 'metrics_collection', label: 'Metrics Collection' },
        { id: 'metrics_rescan', label: 'Metrics Rescan' }
    ],
    'Discovery': [
        { id: 'discovery_scan', label: 'Discovery Scan' },
        { id: 'mdns', label: 'mDNS' },
        { id: 'wsd', label: 'WS-Discovery' },
        { id: 'ssdp', label: 'SSDP' },
        { id: 'llmnr', label: 'LLMNR' },
        { id: 'arp', label: 'ARP' }
    ],
    'Device Operations': [
        { id: 'device_save', label: 'Device Save' },
        { id: 'device_delete', label: 'Device Delete' }
    ]
};

async function loadTraceTags(options = {}) {
    const container = document.getElementById('trace_tags_container');
    if (!container) {
        window.__pm_shared.error('trace_tags_container element not found');
        return;
    }

    if (traceTagsDirty && options.confirmDiscard) {
        const discard = await window.__pm_shared.showConfirm('Discard unsaved trace tag changes?', 'Refresh Trace Tags');
        if (!discard) return false;
    }

    const refreshButton = document.getElementById('trace_tags_refresh_btn');
    const originalRefreshText = refreshButton && refreshButton.textContent;
    traceTagsLoading = true;
    if (refreshButton) {
        refreshButton.disabled = true;
        refreshButton.textContent = 'Refreshing...';
    }
    updateTraceTagsSaveButton();

    try {
        const r = await fetch('/settings/trace_tags');
        if (!r.ok) {
            throw new Error('Failed to load trace tags (status ' + r.status + ')');
        }
        const data = await r.json();
        window.__pm_shared.log('Loaded trace tags:', data);
        // API returns tags as a map/object {tag: bool}, not an array
        const enabledTagsMap = data.tags || {};

        container.querySelector('[data-trace-tags-error]')?.remove();
        Array.from(container.children).filter(el => el.textContent.trim() === 'Loading...').forEach(el => el.remove());
        container.querySelectorAll('.trace-tag-content').forEach(el => el.remove());
        const content = document.createElement('div');
        content.className = 'trace-tag-content';
        container.appendChild(content);

        if (Object.keys(TRACE_TAG_CATEGORIES).length === 0) {
            content.innerHTML = '<span style="color:var(--muted);font-size:12px">No trace tag categories defined</span>';
            traceTagsDirty = false;
            updateTraceTagsSaveButton();
            return true;
        }

        let totalTags = 0;

        // Render each section
        Object.keys(TRACE_TAG_CATEGORIES).forEach(sectionName => {
            const tags = TRACE_TAG_CATEGORIES[sectionName];

            // Section header with toggle all checkbox
            const sectionDiv = document.createElement('div');
            sectionDiv.style.gridColumn = '1 / -1';
            sectionDiv.style.marginTop = totalTags > 0 ? '12px' : '0';
            sectionDiv.style.paddingBottom = '6px';
            sectionDiv.style.borderBottom = '1px solid var(--border)';
            sectionDiv.style.display = 'flex';
            sectionDiv.style.alignItems = 'center';
            sectionDiv.style.gap = '8px';

            const sectionToggle = document.createElement('input');
            sectionToggle.type = 'checkbox';
            sectionToggle.className = 'section-toggle';
            sectionToggle.id = 'trace_section_' + sectionName.toLowerCase().replace(/[^a-z0-9]+/g, '_');
            sectionToggle.dataset.section = sectionName;
            sectionToggle.onchange = () => toggleTraceSection(sectionName, sectionToggle.checked);

            const sectionLabel = document.createElement('label');
            sectionLabel.htmlFor = sectionToggle.id;
            sectionLabel.style.fontWeight = '500';
            sectionLabel.style.color = 'var(--highlight)';
            sectionLabel.style.fontSize = '14px';
            sectionLabel.textContent = sectionName;

            sectionDiv.appendChild(sectionToggle);
            sectionDiv.appendChild(sectionLabel);
            content.appendChild(sectionDiv);

            // Render tags in this section
            tags.forEach(tag => {
                const label = document.createElement('label');
                label.style.display = 'flex';
                label.style.alignItems = 'center';
                label.style.gap = '6px';
                label.style.fontSize = '13px';
                label.style.cursor = 'pointer';
                label.style.paddingLeft = '20px';

                const checkbox = document.createElement('input');
                checkbox.type = 'checkbox';
                checkbox.id = 'trace_tag_' + tag.id;
                checkbox.className = 'trace-tag-checkbox';
                checkbox.dataset.section = sectionName;
                checkbox.checked = enabledTagsMap[tag.id] === true;
                checkbox.onchange = () => {
                    updateSectionToggle(sectionName);
                    traceTagsDirty = true;
                    updateTraceTagsSaveButton();
                };

                label.appendChild(checkbox);
                label.htmlFor = checkbox.id;
                label.appendChild(document.createTextNode(tag.label));
                content.appendChild(label);
                totalTags++;
            });

            // Update section toggle state
            updateSectionToggle(sectionName);
        });

        window.__pm_shared.log('Rendered', totalTags, 'trace tag checkboxes in', Object.keys(TRACE_TAG_CATEGORIES).length, 'sections');
        traceTagsDirty = false;
        updateTraceTagsSaveButton();
        return true;
    } catch (e) {
        window.__pm_shared.error('loadTraceTags failed', e);
        let error = container.querySelector('[data-trace-tags-error]');
        if (!error) {
            error = document.createElement('div');
            error.dataset.traceTagsError = '';
            error.style.cssText = 'color:var(--error,#d33);font-size:12px;margin-bottom:8px;';
            container.prepend(error);
        }
        error.textContent = 'Failed to load trace tags: ' + e.message;
        return false;
    } finally {
        traceTagsLoading = false;
        if (refreshButton) {
            refreshButton.disabled = false;
            refreshButton.textContent = originalRefreshText;
        }
        updateTraceTagsSaveButton();
    }
}

function toggleTraceSection(sectionName, checked) {
    const checkboxes = document.querySelectorAll('.trace-tag-checkbox[data-section="' + sectionName + '"]');
    checkboxes.forEach(cb => cb.checked = checked);
    traceTagsDirty = true;
    updateTraceTagsSaveButton();
}

function updateSectionToggle(sectionName) {
    const checkboxes = document.querySelectorAll('.trace-tag-checkbox[data-section="' + sectionName + '"]');
    const sectionToggle = document.querySelector('.section-toggle[data-section="' + sectionName + '"]');
    if (!sectionToggle) return;

    let allChecked = true;
    let anyChecked = false;
    checkboxes.forEach(cb => {
        if (cb.checked) anyChecked = true;
        else allChecked = false;
    });

    sectionToggle.checked = allChecked;
    sectionToggle.indeterminate = anyChecked && !allChecked;
}

async function saveTraceTags() {
    if (!traceTagsDirty || traceTagsLoading || traceTagsSaving) return false;
    // Build tags object as {tag_id: true/false}
    const tagsMap = {};
    Object.keys(TRACE_TAG_CATEGORIES).forEach(sectionName => {
        TRACE_TAG_CATEGORIES[sectionName].forEach(tag => {
            const checkbox = document.getElementById('trace_tag_' + tag.id);
            if (checkbox) {
                tagsMap[tag.id] = checkbox.checked;
            }
        });
    });

    window.__pm_shared.log('Saving trace tags:', tagsMap);

    const button = document.getElementById('trace_tags_save_btn');
    traceTagsSaving = true;
    if (button) button.textContent = 'Saving...';
    updateTraceTagsSaveButton();
    try {
        const r = await fetch('/settings/trace_tags', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ tags: tagsMap })
        });
        if (!r.ok) {
            throw new Error('Failed to save trace tags (status ' + r.status + ')');
        }
        traceTagsDirty = false;
        window.__pm_shared.showToast('Trace tags saved successfully', 'success');
        return true;
    } catch (e) {
        window.__pm_shared.error('saveTraceTags failed', e);
        window.__pm_shared.showToast('Failed to save trace tags: ' + e.message, 'error');
        return false;
    } finally {
        traceTagsSaving = false;
        updateTraceTagsSaveButton();
    }
}



// Saved walks helpers
function refreshMibWalks() {
    // Now list merged devices rather than raw saved walks
    fetch('/devices/list').then(r => r.json()).then(arr => {
        const tbody = document.getElementById('mib_walks_tbody'); tbody.innerHTML = '';
        // arr elements: { serial, path, info }
        arr.forEach(item => {
            const tr = document.createElement('tr'); tr.style.borderTop = '1px solid rgba(255,255,255,0.03)';
            const nameTd = document.createElement('td'); nameTd.textContent = item.path || ''; nameTd.style.fontFamily = 'monospace';
            const ipTd = document.createElement('td'); ipTd.textContent = (item.info && item.info.ip) ? item.info.ip : '';
            const dtTd = document.createElement('td'); dtTd.textContent = '';
            const cntTd = document.createElement('td'); cntTd.style.textAlign = 'right'; cntTd.textContent = '';
            const actionsTd = document.createElement('td'); actionsTd.style.display = 'flex'; actionsTd.style.gap = '6px';
            const viewBtn = document.createElement('button'); viewBtn.textContent = 'View'; viewBtn.onclick = () => { viewDevice(item.serial); };
            const delBtn = document.createElement('button'); delBtn.textContent = 'Delete'; delBtn.onclick = async () => {
                delBtn.disabled = true;
                delBtn.textContent = 'Deleting...';
                const r = await fetch('/devices/delete', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ serial: item.serial }) });
                if (!r.ok) { delBtn.disabled = false; delBtn.textContent = 'Delete'; window.__pm_shared.showToast('Delete failed', 'error'); return; }
                window.__pm_shared.showToast('Device deleted successfully', 'success');
                refreshMibWalks();
            };
            actionsTd.appendChild(viewBtn); actionsTd.appendChild(delBtn);
            tr.appendChild(nameTd); tr.appendChild(ipTd); tr.appendChild(dtTd); tr.appendChild(cntTd); tr.appendChild(actionsTd);
            tbody.appendChild(tr);
        });
    }).catch(e => { window.__pm_shared.error('failed to load devices', e); });
}

// Removed legacy alias: refreshDevices()

function viewDevice(serial) {
    const meta = document.getElementById('mib_results_meta'); const tbody = document.getElementById('mib_results_tbody');
    meta.style.display = 'block'; meta.textContent = 'Loading device ' + serial + '...'; tbody.innerHTML = '';
    fetch('/api/devices/profile?serial=' + encodeURIComponent(serial)).then(async r => {
        if (!r.ok) { meta.textContent = 'Not found'; return; }
        const j = await r.json();
        // Canonical response: { device, latest_metrics }
        let deviceData = (j && j.device) ? j.device : j;
        if (deviceData) {
            renderPrinterInfo(deviceData);
            // add a refresh button to meta so user can refresh this device
            const btn = document.createElement('button'); btn.textContent = 'Refresh Device';
            btn.onclick = async () => {
                btn.disabled = true; const old = btn.textContent; btn.textContent = 'Refreshing...';
                try {
                    const resp = await fetch('/devices/refresh', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ serial }) });
                    if (!resp.ok) {
                        const txt = await resp.text();
                        window.__pm_shared.showToast('Refresh failed: ' + txt, 'error');
                        return;
                    }
                    window.__pm_shared.showToast('Refresh queued for ' + serial, 'success');
                } catch (e) { window.__pm_shared.showToast('Refresh failed: ' + e.message, 'error'); }
                finally { btn.disabled = false; btn.textContent = old; }
            };
            meta.appendChild(document.createTextNode(' ')); meta.appendChild(btn);
            meta.dataset.deviceSerial = serial;
            meta.textContent = 'Device: ' + serial;
        } else {
            meta.textContent = 'No device data available';
        }
    }).catch(e => { meta.textContent = 'Load failed: ' + e; });
}

function renderPrinterInfo(pi) {
    const tbody = document.getElementById('mib_results_tbody'); tbody.innerHTML = '';
    // show a handful of key/value rows
    const addRow = (k, v) => {
        const tr = document.createElement('tr');
        const act = document.createElement('td'); act.textContent = '';
        const sym = document.createElement('td'); sym.textContent = k;
        const typ = document.createElement('td'); typ.textContent = typeof v;
        const val = document.createElement('td'); val.textContent = (v === null ? '' : (typeof v === 'object' ? JSON.stringify(v) : String(v)));
        const oid = document.createElement('td'); oid.textContent = '';
        tr.appendChild(act); tr.appendChild(sym); tr.appendChild(typ); tr.appendChild(val); tr.appendChild(oid);
        tbody.appendChild(tr);
    };
    addRow('IP', pi.ip || '');
    addRow('Manufacturer', pi.manufacturer || '');
    addRow('Model', pi.model || '');
    addRow('Serial', pi.serial || '');
    addRow('Hostname', pi.hostname || '');
    addRow('MAC', pi.mac_address || '');
    addRow('Firmware', pi.firmware || '');
    addRow('PageCount', pi.page_count || '');
    if (pi.toner_levels) addRow('TonerLevels', pi.toner_levels);
    if (pi.consumables) addRow('Consumables', pi.consumables);
    if (pi.status_messages && pi.status_messages.length > 0) addRow('StatusMessages', pi.status_messages);
    if (pi.last_seen) addRow('LastSeen', new Date(pi.last_seen).toLocaleString());
}



function saveDevSettings() {
    if (!settingsLoadedSuccessfully) {
        updateSettingsLoadState(new Error('waiting for a successful settings load'));
        return;
    }
    const logLevel = document.getElementById('dev_debug_logging').value;
    // Compose the new settings structure
    const snmpVersion = document.getElementById('dev_snmp_version').value || '2c';
    const snmpSettings = {
        version: snmpVersion,
        community: document.getElementById('dev_snmp_community').value,
        timeout_ms: parseInt(document.getElementById('dev_snmp_timeout').value) || 2000,
        retries: parseInt(document.getElementById('dev_snmp_retries').value) || 1
    };
    // Add SNMPv3 settings if version 3 is selected
    if (snmpVersion === '3') {
        snmpSettings.security_level = document.getElementById('dev_snmp_security_level').value || 'noAuthNoPriv';
        snmpSettings.username = document.getElementById('dev_snmp_username').value || '';
        snmpSettings.auth_protocol = document.getElementById('dev_snmp_auth_protocol').value || '';
        snmpSettings.auth_password = document.getElementById('dev_snmp_auth_password').value || '';
        snmpSettings.priv_protocol = document.getElementById('dev_snmp_priv_protocol').value || '';
        snmpSettings.priv_password = document.getElementById('dev_snmp_priv_password').value || '';
        snmpSettings.context_name = document.getElementById('dev_snmp_context_name').value || '';
    }
    const featuresSettings = {
        asset_id_regex: document.getElementById('dev_asset_id_regex').value || '',
        epson_remote_mode_enabled: document.getElementById('dev_epson_remote_mode')?.checked ?? false
    };
    const loggingSettings = {
        level: logLevel,
        dump_parse_debug: document.getElementById('dev_dump_parse_debug').checked
    };
    // POST settings as part of the unified /settings endpoint
    fetch('/settings', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ snmp: snmpSettings, features: featuresSettings, logging: loggingSettings }) })
        .then(async r => { 
            if (!r.ok) { 
                const t = await r.text(); 
                window.__pm_shared.error('Save failed:', t); 
                window.__pm_shared.showToast('Save failed: ' + t, 'error');
                return; 
            } 
            window.__pm_shared.showToast('Settings saved successfully', 'success');
        })
        .catch(e => { window.__pm_shared.error('Save failed:', e); window.__pm_shared.showToast('Save failed: ' + e.message, 'error'); });
}

// Toggle auto-save mode
function toggleAutoSave() {
    const enabled = document.getElementById('settings_autosave').checked;
    const buttonsDiv = document.getElementById('settings_buttons');
    const advancedLabel = document.getElementById('advanced_toggle_label');

    if (enabled) {
        // Hide buttons and move advanced toggle fully right
        buttonsDiv.style.display = 'none';
        if (advancedLabel) {
            advancedLabel.style.marginLeft = 'auto';
        }
        // Save the preference
        localStorage.setItem('settings_autosave', 'true');
        // Add onchange handlers to all settings inputs
        addAutoSaveHandlers();
    } else {
        // Show buttons and adjust spacing
        buttonsDiv.style.display = 'flex';
        if (advancedLabel) {
            advancedLabel.style.marginLeft = '12px';
        }
        localStorage.setItem('settings_autosave', 'false');
        // Remove onchange handlers
        removeAutoSaveHandlers();
    }
}

// Toggle visibility of advanced settings controls
function toggleAdvancedSettings() {
    try {
        const enabled = document.getElementById('settings_advanced_toggle')?.checked || false;
        // Elements marked as advanced-setting should be shown/hidden
        document.querySelectorAll('.advanced-setting').forEach(el => {
            if (enabled) {
                el.style.display = '';
            } else {
                el.style.display = 'none';
            }
        });

        // Textareas or other advanced inputs may use a dedicated class
        document.querySelectorAll('.advanced-setting-textarea').forEach(el => {
            if (enabled) el.style.display = '';
            else el.style.display = 'none';
        });

        // Persist preference
        try { localStorage.setItem('settings_advanced', enabled ? 'true' : 'false'); } catch (e) {}
    } catch (e) {
        window.__pm_shared.error('toggleAdvancedSettings failed', e);
        throw e;
    }
}

function showAutosaveFeedback() {
    const feedback = document.getElementById('autosave_feedback');
    if (!feedback) return;

    feedback.classList.add('show');
    setTimeout(() => {
        feedback.classList.remove('show');
    }, 2000);
}

// Add auto-save handlers to all settings inputs
function addAutoSaveHandlers() {
    // keep references so we can remove listeners later
    window.__settingsChangeHandler = () => { saveAllSettings().then(saved => { if (saved) showAutosaveFeedback(); }); };
    window.__manualRangesHandler = () => { toggleRangesDropdown(); saveAllSettings().then(saved => { if (saved) showAutosaveFeedback(); }); };
    window.__ipScanningHandler = () => { toggleIPScanningUI(); saveAllSettings().then(saved => { if (saved) showAutosaveFeedback(); }); };

    // Discovery settings
    document.getElementById('scan_local_subnet_enabled')?.addEventListener('change', window.__settingsChangeHandler);
    const manualRangesEl = document.getElementById('manual_ranges_enabled');
    if (manualRangesEl) { manualRangesEl.addEventListener('change', window.__manualRangesHandler); }
    document.getElementById('discovery_arp_enabled')?.addEventListener('change', window.__settingsChangeHandler);
    document.getElementById('discovery_icmp_enabled')?.addEventListener('change', window.__settingsChangeHandler);
    document.getElementById('discovery_tcp_enabled')?.addEventListener('change', window.__settingsChangeHandler);
    document.getElementById('discovery_snmp_enabled')?.addEventListener('change', window.__settingsChangeHandler);
    document.getElementById('discovery_mdns_enabled')?.addEventListener('change', window.__settingsChangeHandler);
    document.getElementById('ip_scanning_enabled')?.addEventListener('change', window.__ipScanningHandler);
    // Auto-save ranges when the textarea loses focus
    const rangesEl = document.getElementById('ranges_text');
    if (rangesEl) { rangesEl.addEventListener('blur', window.__settingsChangeHandler); }
    // Auto Discover and Autosave toggles (with UI effects)
    window.__autoDiscoverHandler = () => { toggleAutoDiscoverUI(); saveAllSettings().then(saved => { if (saved) showAutosaveFeedback(); }); };
    window.__autosaveHandler = () => { toggleAutosaveUI(); saveAllSettings().then(saved => { if (saved) showAutosaveFeedback(); }); };
    const autoDiscoverEl = document.getElementById('auto_discover_checkbox');
    if (autoDiscoverEl) { autoDiscoverEl.addEventListener('change', window.__autoDiscoverHandler); }
    const autosaveEl = document.getElementById('autosave_checkbox');
    if (autosaveEl) { autosaveEl.addEventListener('change', window.__autosaveHandler); }

    // "Show Anyway" toggles (trigger save and update UI)
    window.__showDiscoverAnywayHandler = () => {
        const checked = document.getElementById('show_discover_button_anyway')?.checked;
        const container = document.getElementById('show_discover_button_anyway_container');
        const discoverBtn = document.getElementById('discover_now_btn');
        
        // Only affect discover button if this toggle is visible (auto discover must be enabled)
        const isVisible = container && container.style.display !== 'none';
        
        if (isVisible && discoverBtn) {
            if (checked) {
                discoverBtn.style.display = 'inline-block';
            } else {
                discoverBtn.style.display = 'none';
            }
        }
        
        saveAllSettings().then(saved => { if (saved) showAutosaveFeedback(); });
    };
    window.__showDiscoveredAnywayHandler = () => {
        const checked = document.getElementById('show_discovered_devices_anyway')?.checked;
        const showSavedCheckbox = document.getElementById('show_saved_in_discovered');
        const container = document.getElementById('show_discovered_devices_anyway_container');
        
        // Only affect "Show Known Devices" if this toggle is visible (autosave must be enabled)
        const isVisible = container && container.style.display !== 'none';
        
        if (isVisible && checked && showSavedCheckbox) {
            // When "Show Anyway" is enabled, force-enable "Show Known Devices"
            // (discovered section stays visible and shows all devices including saved ones)
            showSavedCheckbox.checked = true;
        }
        
        // Always update printers to show/hide discovered section
        updatePrinters();
        
        // Save the preference
        saveAllSettings().then(saved => { if (saved) showAutosaveFeedback(); });
    };
    const showDiscoverAnywayEl = document.getElementById('show_discover_button_anyway');
    if (showDiscoverAnywayEl) { showDiscoverAnywayEl.addEventListener('change', window.__showDiscoverAnywayHandler); }
    const showDiscoveredAnywayEl = document.getElementById('show_discovered_devices_anyway');
    if (showDiscoveredAnywayEl) { showDiscoveredAnywayEl.addEventListener('change', window.__showDiscoveredAnywayHandler); }

    // Passive discovery master toggle gets standard change handler
    document.getElementById('passive_discovery_enabled')?.addEventListener('change', window.__settingsChangeHandler);
    document.getElementById('discovery_live_mdns_enabled')?.addEventListener('change', window.__settingsChangeHandler);
    document.getElementById('discovery_live_wsd_enabled')?.addEventListener('change', window.__settingsChangeHandler);
    document.getElementById('discovery_live_ssdp_enabled')?.addEventListener('change', window.__settingsChangeHandler);
    document.getElementById('discovery_live_snmptrap_enabled')?.addEventListener('change', window.__settingsChangeHandler);
    document.getElementById('discovery_live_llmnr_enabled')?.addEventListener('change', window.__settingsChangeHandler);
    document.getElementById('metrics_rescan_enabled')?.addEventListener('change', window.__settingsChangeHandler);
    // Remove IP scanning handlers when autosave disabled
    document.getElementById('metrics_rescan_interval')?.addEventListener('change', window.__settingsChangeHandler);

    // Spooler/local printer tracking settings
    window.__spoolerEnabledHandler = function() {
        const spoolerOptionsContainer = document.getElementById('spooler_options_container');
        if (spoolerOptionsContainer) {
            spoolerOptionsContainer.style.display = this.checked ? 'block' : 'none';
        }
        saveAllSettings().then(saved => { if (saved) showAutosaveFeedback(); });
    };
    document.getElementById('spooler_enabled')?.addEventListener('change', window.__spoolerEnabledHandler);
    document.getElementById('spooler_poll_interval')?.addEventListener('change', window.__settingsChangeHandler);
    document.getElementById('spooler_include_network')?.addEventListener('change', window.__settingsChangeHandler);
    document.getElementById('spooler_include_virtual')?.addEventListener('change', window.__settingsChangeHandler);

    // Developer settings
    document.getElementById('dev_debug_logging')?.addEventListener('change', window.__settingsChangeHandler);
    document.getElementById('dev_dump_parse_debug')?.addEventListener('change', window.__settingsChangeHandler);
    document.getElementById('dev_snmp_community')?.addEventListener('change', window.__settingsChangeHandler);
    document.getElementById('dev_snmp_timeout')?.addEventListener('change', window.__settingsChangeHandler);
    document.getElementById('dev_snmp_retries')?.addEventListener('change', window.__settingsChangeHandler);
    document.getElementById('dev_epson_remote_mode')?.addEventListener('change', window.__settingsChangeHandler);
    document.getElementById('dev_discover_concurrency')?.addEventListener('change', window.__settingsChangeHandler);
    document.getElementById('dev_asset_id_regex')?.addEventListener('change', window.__settingsChangeHandler);
}

// Remove auto-save handlers
function removeAutoSaveHandlers() {
    if (!window.__settingsChangeHandler) return;
    document.getElementById('scan_local_subnet_enabled')?.removeEventListener('change', window.__settingsChangeHandler);
    const manualRangesEl = document.getElementById('manual_ranges_enabled');
    if (manualRangesEl && window.__manualRangesHandler) { manualRangesEl.removeEventListener('change', window.__manualRangesHandler); }
    document.getElementById('discovery_arp_enabled')?.removeEventListener('change', window.__settingsChangeHandler);
    document.getElementById('discovery_icmp_enabled')?.removeEventListener('change', window.__settingsChangeHandler);
    document.getElementById('discovery_tcp_enabled')?.removeEventListener('change', window.__settingsChangeHandler);
    document.getElementById('discovery_snmp_enabled')?.removeEventListener('change', window.__settingsChangeHandler);
    document.getElementById('discovery_mdns_enabled')?.removeEventListener('change', window.__settingsChangeHandler);
    const rangesEl = document.getElementById('ranges_text');
    if (rangesEl) { rangesEl.removeEventListener('blur', window.__settingsChangeHandler); }
    // Note: auto_discover_checkbox and autosave_checkbox use separate handlers stored in window
    const autoDiscoverEl = document.getElementById('auto_discover_checkbox');
    if (autoDiscoverEl && window.__autoDiscoverHandler) { autoDiscoverEl.removeEventListener('change', window.__autoDiscoverHandler); }
    const autosaveEl = document.getElementById('autosave_checkbox');
    if (autosaveEl && window.__autosaveHandler) { autosaveEl.removeEventListener('change', window.__autosaveHandler); }

    // Remove "Show Anyway" toggle handlers
    const showDiscoverAnywayEl = document.getElementById('show_discover_button_anyway');
    if (showDiscoverAnywayEl && window.__showDiscoverAnywayHandler) { showDiscoverAnywayEl.removeEventListener('change', window.__showDiscoverAnywayHandler); }
    const showDiscoveredAnywayEl = document.getElementById('show_discovered_devices_anyway');
    if (showDiscoveredAnywayEl && window.__showDiscoveredAnywayHandler) { showDiscoveredAnywayEl.removeEventListener('change', window.__showDiscoveredAnywayHandler); }

    document.getElementById('passive_discovery_enabled')?.removeEventListener('change', window.__settingsChangeHandler);
    document.getElementById('discovery_live_mdns_enabled')?.removeEventListener('change', window.__settingsChangeHandler);
    document.getElementById('discovery_live_wsd_enabled')?.removeEventListener('change', window.__settingsChangeHandler);
    document.getElementById('discovery_live_ssdp_enabled')?.removeEventListener('change', window.__settingsChangeHandler);
    document.getElementById('discovery_live_snmptrap_enabled')?.removeEventListener('change', window.__settingsChangeHandler);
    document.getElementById('discovery_live_llmnr_enabled')?.removeEventListener('change', window.__settingsChangeHandler);
    document.getElementById('metrics_rescan_enabled')?.removeEventListener('change', window.__settingsChangeHandler);
    document.getElementById('ip_scanning_enabled')?.removeEventListener('change', window.__ipScanningHandler);
    document.getElementById('metrics_rescan_interval')?.removeEventListener('change', window.__settingsChangeHandler);

    // Spooler settings
    const spoolerEnabledEl = document.getElementById('spooler_enabled');
    if (spoolerEnabledEl && window.__spoolerEnabledHandler) { spoolerEnabledEl.removeEventListener('change', window.__spoolerEnabledHandler); }
    document.getElementById('spooler_poll_interval')?.removeEventListener('change', window.__settingsChangeHandler);
    document.getElementById('spooler_include_network')?.removeEventListener('change', window.__settingsChangeHandler);
    document.getElementById('spooler_include_virtual')?.removeEventListener('change', window.__settingsChangeHandler);

    document.getElementById('dev_debug_logging')?.removeEventListener('change', window.__settingsChangeHandler);
    document.getElementById('dev_dump_parse_debug')?.removeEventListener('change', window.__settingsChangeHandler);
    document.getElementById('dev_snmp_community')?.removeEventListener('change', window.__settingsChangeHandler);
    document.getElementById('dev_snmp_timeout')?.removeEventListener('change', window.__settingsChangeHandler);
    document.getElementById('dev_snmp_retries')?.removeEventListener('change', window.__settingsChangeHandler);
    document.getElementById('dev_epson_remote_mode')?.removeEventListener('change', window.__settingsChangeHandler);
    document.getElementById('dev_discover_concurrency')?.removeEventListener('change', window.__settingsChangeHandler);
    document.getElementById('dev_asset_id_regex')?.removeEventListener('change', window.__settingsChangeHandler);
}

// Unified save for both discovery and developer settings
function readWebPort(id, label) {
    const raw = document.getElementById(id)?.value.trim() || '';
    const port = Number(raw);
    if (!/^\d+$/.test(raw) || !Number.isInteger(port) || port < 1 || port > 65535) {
        throw new Error(label + ' port must be an integer from 1 to 65535');
    }
    return port;
}

async function saveAllSettings(btn) {
    try {
        if (!settingsLoadedSuccessfully) {
            updateSettingsLoadState(new Error('waiting for a successful settings load'));
            return false;
        }
        if (btn) { btn.disabled = true; btn.textContent = 'Applying...'; }
        // Compose discovery settings
        const discoverySettings = {
            // Master IP scanning toggle
            ip_scanning_enabled: document.getElementById('ip_scanning_enabled')?.checked ?? true,
            // IP Sources
            subnet_scan: document.getElementById('scan_local_subnet_enabled')?.checked ?? true,
            manual_ranges: document.getElementById('manual_ranges_enabled')?.checked ?? false,

            // Active Probes
            arp_enabled: document.getElementById('discovery_arp_enabled')?.checked ?? true,
            icmp_enabled: document.getElementById('discovery_icmp_enabled')?.checked ?? true,
            tcp_enabled: document.getElementById('discovery_tcp_enabled')?.checked ?? true,
            mdns_enabled: document.getElementById('discovery_mdns_enabled')?.checked ?? false,

            // Device Identification
            snmp_enabled: document.getElementById('discovery_snmp_enabled')?.checked ?? true,

            // Passive Discovery
            auto_discover_enabled: document.getElementById('auto_discover_checkbox')?.checked ?? false,
            autosave_discovered_devices: document.getElementById('autosave_checkbox')?.checked ?? false,
            show_discover_button_anyway: document.getElementById('show_discover_button_anyway')?.checked ?? false,
            show_discovered_devices_anyway: document.getElementById('show_discovered_devices_anyway')?.checked ?? false,
            passive_discovery_enabled: document.getElementById('passive_discovery_enabled')?.checked ?? false,
            auto_discover_live_mdns: document.getElementById('discovery_live_mdns_enabled')?.checked ?? true,
            auto_discover_live_wsd: document.getElementById('discovery_live_wsd_enabled')?.checked ?? true,
            auto_discover_live_ssdp: document.getElementById('discovery_live_ssdp_enabled')?.checked ?? false,
            auto_discover_live_snmptrap: document.getElementById('discovery_live_snmptrap_enabled')?.checked ?? false,
            auto_discover_live_llmnr: document.getElementById('discovery_live_llmnr_enabled')?.checked ?? false,

            // Metrics Monitoring
                metrics_rescan_enabled: document.getElementById('metrics_rescan_enabled')?.checked ?? false,
                // Validate and clamp interval to avoid sending NaN/null which the
                // backend treats as missing and falls back to default (60). Ensure
                // interval is an integer between 5 and 1440.
                metrics_rescan_interval_minutes: (function(){
                    const el = document.getElementById('metrics_rescan_interval');
                    let iv = el ? parseInt(el.value, 10) : NaN;
                    if (isNaN(iv)) iv = 60; // default
                    if (iv < 5) iv = 5;
                    if (iv > 1440) iv = 1440;
                    return iv;
                })(),

            // Performance
            concurrency: parseInt(document.getElementById('dev_discover_concurrency').value) || 50
        };

        // Include ranges text directly in unified save payload
        const rangesTextEl = document.getElementById('ranges_text');
        if (rangesTextEl) {
            discoverySettings.ranges_text = rangesTextEl.value || '';
        }

        // Read log level for logging settings
        const logLevel = document.getElementById('dev_debug_logging').value;

        // Compose SNMP settings (moved from developer)
        const snmpVersion = document.getElementById('dev_snmp_version').value || '2c';
        const snmpSettings = {
            version: snmpVersion,
            community: document.getElementById('dev_snmp_community').value,
            timeout_ms: parseInt(document.getElementById('dev_snmp_timeout').value) || 2000,
            retries: parseInt(document.getElementById('dev_snmp_retries').value) || 1
        };
        // Add SNMPv3 settings if version 3 is selected
        if (snmpVersion === '3') {
            snmpSettings.security_level = document.getElementById('dev_snmp_security_level').value || 'noAuthNoPriv';
            snmpSettings.username = document.getElementById('dev_snmp_username').value || '';
            snmpSettings.auth_protocol = document.getElementById('dev_snmp_auth_protocol').value || '';
            snmpSettings.auth_password = document.getElementById('dev_snmp_auth_password').value || '';
            snmpSettings.priv_protocol = document.getElementById('dev_snmp_priv_protocol').value || '';
            snmpSettings.priv_password = document.getElementById('dev_snmp_priv_password').value || '';
            snmpSettings.context_name = document.getElementById('dev_snmp_context_name').value || '';
        }

        // Compose features settings
        const featuresSettings = {
            credentials_enabled: document.getElementById('enable_saved_credentials')?.checked ?? true,
            asset_id_regex: document.getElementById('dev_asset_id_regex').value || '',
            epson_remote_mode_enabled: document.getElementById('dev_epson_remote_mode')?.checked ?? false
        };

        // Compose logging settings (agent-local)
        const loggingSettings = {
            level: logLevel,
            dump_parse_debug: document.getElementById('dev_dump_parse_debug').checked
        };

        // Compose web settings (agent-local)
        const webSettings = {
            enable_http: document.getElementById('enable_http')?.checked ?? true,
            http_port: readWebPort('http_port', 'HTTP'),
            enable_https: document.getElementById('enable_https')?.checked ?? true,
            https_port: readWebPort('https_port', 'HTTPS'),
            redirect_http_to_https: document.getElementById('redirect_http_to_https')?.checked ?? false,
            custom_cert_path: document.getElementById('custom_cert_path')?.value || '',
            custom_key_path: document.getElementById('custom_key_path')?.value || ''
        };

        // Compose spooler/local printer tracking settings (fleet-managed)
        const spoolerSettings = {
            enabled: document.getElementById('spooler_enabled')?.checked ?? true,
            poll_interval_seconds: (function(){
                const el = document.getElementById('spooler_poll_interval');
                let iv = el ? parseInt(el.value, 10) : NaN;
                if (isNaN(iv)) iv = 30; // default
                if (iv < 5) iv = 5;
                if (iv > 300) iv = 300;
                return iv;
            })(),
            include_network_printers: document.getElementById('spooler_include_network')?.checked ?? false,
            include_virtual_printers: document.getElementById('spooler_include_virtual')?.checked ?? false
        };

        const rUnified = await fetch('/settings', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ discovery: discoverySettings, snmp: snmpSettings, features: featuresSettings, spooler: spoolerSettings, logging: loggingSettings, web: webSettings })
        });
        if (!rUnified.ok) {
            const t = await rUnified.text();
            throw new Error('Failed to save settings: ' + t);
        }

        settingsDirty = false;
        updateSettingsDirtyState();
        const saveError = document.getElementById('settings_save_error');
        if (saveError) { saveError.hidden = true; saveError.textContent = ''; }
    window.__pm_shared.log('All settings saved successfully');
        if (btn) {
            btn.textContent = '✓ Applied';
            setTimeout(() => { updateSettingsDirtyState(); }, 1500);
        }
    window.__pm_shared.showToast('Settings saved successfully', 'success');
        return true;
    } catch (e) {
        window.__pm_shared.error('Save failed:', e);
        showSettingsSaveError(e);
        if (!btn) {
            // Autosave failed silently in background, just log it
            window.__pm_shared.warn('Autosave failed:', e.message);
            } else {
            window.__pm_shared.showToast('Save failed: ' + e.message, 'error');
            updateSettingsDirtyState();
        }
        return Promise.reject(e);
    }
}

async function resetSettings() {
    if (!settingsLoadedSuccessfully) return;
    const confirmed = await window.__pm_shared.showConfirm(
        'Reset all settings to defaults?\n\nThis will restore default settings unless they are manually configured in config.json.',
        'Reset Settings',
        true
    );
    if (!confirmed) return;
    
    fetch('/settings', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ reset: true }) })
        .then(async r => {
            if (!r.ok) {
                const t = await r.text();
                window.__pm_shared.error('Reset failed:', t);
                window.__pm_shared.showToast('Reset failed: ' + t, 'error');
                return;
            }
            loadSettings().then(() => updatePrinters());
            window.__pm_shared.showToast('Settings reset successfully', 'success');
        })
        .catch(e => { window.__pm_shared.error('Reset failed:', e); window.__pm_shared.showToast('Reset failed: ' + e.message, 'error'); });
}

// Clipboard icon is provided by shared helpers (window.__pm_shared.makeClipboardIcon)

// WebUI Modal functions
let currentWebUIURL = '';
let currentSerial = '';

function showWebUIModal(webUIURL, serial) {
    currentWebUIURL = webUIURL;
    currentSerial = serial;
    document.getElementById('webui_modal').classList.add('active');
}

function closeWebUIModal() {
    document.getElementById('webui_modal').classList.remove('active');
    currentWebUIURL = '';
    currentSerial = '';
}

function openProxyUI() {
    if (currentSerial) {
        window.open('/proxy/' + currentSerial, '_blank');
        closeWebUIModal();
    }
}

function openDirectUI() {
    if (currentWebUIURL) {
        window.open(currentWebUIURL, '_blank');
        closeWebUIModal();
    }
}

// Metrics modal (delegate to shared implementation)
function showDeviceMetricsModal(serial, preset) {
    if (!serial) return;
    if (typeof window !== 'undefined' && typeof window.showMetricsModal === 'function') {
        try {
            window.showMetricsModal({ serial, preset });
            return;
        } catch (e) {
            window.__pm_shared.warn('shared.showMetricsModal failed', e);
        }
    }
    // Fallback: minimal alert
    window.__pm_shared.showAlert('Metrics UI not available for ' + serial, 'Metrics', false, false);
}

function closeDeviceMetricsModal() {
    const modal = document.getElementById('metrics_modal');
    if (!modal) return;
    modal.style.display = 'none';
}

// Expose saveAllSettings for testability and external callers
try { window.saveAllSettings = window.saveAllSettings || saveAllSettings; } catch (e) {}

// Toggle the visibility of the metrics time selector for a given container.
// targetId: optional id of the container that holds the metrics UI (e.g. 'metrics_modal_body' or 'metrics_content').
function toggleMetricsTimeSelector(targetId) {
    try {
        let container = null;
        if (targetId) container = document.getElementById(targetId);
        // Fallbacks: modal body or generic metrics content
        if (!container) container = document.getElementById('metrics_modal_body') || document.getElementById('metrics_content') || document.body;

        const selector = container.querySelector('#metrics_time_selector');
        const btn = container.querySelector('#metrics_toggle_time_btn');
        if (!selector || !btn) return;

        const nowHidden = selector.classList.toggle('hidden');
        btn.textContent = nowHidden ? 'Show time selector' : 'Hide time selector';
        btn.setAttribute('aria-expanded', (!nowHidden).toString());
    } catch (e) {
        window.__pm_shared.warn('toggleMetricsTimeSelector failed', e);
    }
}

// Initialize UI after all functions are defined
// Load settings first to ensure visibility states are correct before rendering
// loadSettings() is async, so we chain the dependent calls
loadSettings().then(() => {
    updatePrinters();
    loadSavedRanges();
}).catch(() => {
    // Fallback: still try to render even if settings fail
    updatePrinters();
    loadSavedRanges();
});

// Clear search filter boxes on page load
const discoveredSearchInput = document.getElementById('discovered_search');
if (discoveredSearchInput) {
    discoveredSearchInput.value = '';
}
const savedSearchInput = document.getElementById('saved_search');
if (savedSearchInput) {
    savedSearchInput.value = '';
}

// Check auto-save preference
const autoSave = localStorage.getItem('settings_autosave') === 'true';
if (autoSave) {
    document.getElementById('settings_autosave').checked = true;
    toggleAutoSave();
}

let currentServerStatus = null;
const LAST_SERVER_URL_KEY = 'pm:last-server-url';

const deviceAuthState = {
    pollTimer: null,
    pollToken: '',
    serverURL: '',
    caPath: '',
    insecure: false,
    agentName: '',
    authorizeURL: ''
};

const DEVICE_AUTH_POLL_INTERVAL = 3500;

function rememberServerURL(url) {
    try {
        if (url) {
            localStorage.setItem(LAST_SERVER_URL_KEY, url);
        }
    } catch (_) {}
}

function getRememberedServerURL() {
    try {
        return localStorage.getItem(LAST_SERVER_URL_KEY);
    } catch (_) {
        return null;
    }
}

function defaultServerURL() {
    if (currentServerStatus && currentServerStatus.url) {
        return currentServerStatus.url;
    }
    return getRememberedServerURL() || 'https://';
}
let serverStatusFetchPromise = null;
const SERVER_STATUS_BADGE_META = {
    live: {
        label: 'Live',
        title: 'Real-time WebSocket connection active'
    },
    connected: {
        label: 'Connected',
        title: 'HTTP fallback active'
    },
    disconnected: {
        label: 'Disconnected',
        title: 'No server connection'
    }
};

function deriveServerConnectionMode(status) {
    if (!status || !status.enabled || !status.url) {
        return 'disconnected';
    }
    const mode = (status.connection_mode || '').trim().toLowerCase();
    if (mode === 'live' || mode === 'connected' || mode === 'disconnected') {
        return mode;
    }
    return status.connected ? 'connected' : 'disconnected';
}

function updateServerStatusBadge(status) {
    const badge = document.getElementById('server_status_badge');
    if (!badge) return;
    const mode = deriveServerConnectionMode(status);
    const meta = SERVER_STATUS_BADGE_META[mode] || SERVER_STATUS_BADGE_META.disconnected;
    badge.textContent = meta.label;
    badge.title = meta.title;
    badge.setAttribute('data-mode', mode);
    ['live', 'connected', 'disconnected'].forEach(state => badge.classList.remove('server-status-' + state));
    badge.classList.add('server-status-' + mode);
    badge.style.display = 'inline-flex';
}

function applyServerConnectionStatus(status, options = {}) {
    const btn = document.getElementById('join_token_btn');
    currentServerStatus = status || null;
    const connected = !!(currentServerStatus && currentServerStatus.enabled && currentServerStatus.url);
    if (btn) {
        btn.dataset.mode = connected ? 'connected' : 'standalone';
        btn.textContent = connected ? 'Server Info' : 'Join Server';
        btn.title = connected ? 'View current server connection' : 'Join a server with login approval';
    }
    populateServerInfoModal(currentServerStatus);
    updateServerStatusBadge(currentServerStatus);
    
    // Update header info bar with server details
    onServerStatusChanged(currentServerStatus);
}

async function probeServerConnection(serverUrl) {
    const resp = await fetch('/settings/probe-server', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ server_url: serverUrl })
    });
    if (!resp.ok) {
        const txt = await resp.text();
        throw new Error(txt || resp.statusText || 'probe failed');
    }
    return resp.json();
}

function summarizeCertificate(cert) {
    if (!cert) return 'Unknown certificate';
    const parts = [];
    parts.push(`Subject: ${cert.subject}`);
    parts.push(`Issuer: ${cert.issuer}`);
    if (cert.not_before && cert.not_after) {
        parts.push(`Valid: ${new Date(cert.not_before).toLocaleString()} - ${new Date(cert.not_after).toLocaleString()}`);
    }
    if (cert.dns_names && cert.dns_names.length) {
        parts.push(`DNS Names: ${cert.dns_names.join(', ')}`);
    }
    return parts.join('\n');
}

async function evaluateProbeResult(probe) {
    const summary = { ok: true, insecure: false };
    if (!probe) {
        return summary;
    }
    const tls = probe.tls || {};
    if (tls.enabled) {
        if (tls.valid) {
            return summary;
        }
        if (tls.error_code === 'unknown_authority') {
            const proceed = await window.__pm_shared.showConfirm(
                'The server presented a certificate that is not trusted by this system.\n' +
                summarizeCertificate(tls.certificate) +
                '\n\nClick OK to continue with TLS verification disabled (not recommended).',
                'Untrusted certificate',
                false
            );
            if (!proceed) {
                return { ok: false, insecure: false };
            }
            summary.insecure = true;
            return summary;
        }
        return { ok: false, insecure: false, message: 'TLS verification failed: ' + (tls.error || tls.error_code || 'unknown error') };
    }

    if (!probe.reachable) {
        return { ok: false, insecure: false, message: 'Unable to reach the server.' };
    }

    if (probe.scheme === 'http') {
        const proceed = await window.__pm_shared.showConfirm(
            'The server appears to be using HTTP without TLS. Continue anyway?',
            'Insecure connection',
            false
        );
        if (!proceed) {
            return { ok: false, insecure: false };
        }
    }

    return summary;
}

async function submitJoinRequest(serverURL, joinToken, options = {}) {
    const payload = {
        server_url: serverURL,
        token: joinToken,
        insecure: !!options.insecure
    };
    if (options.caPath) {
        payload.ca_path = options.caPath;
    }
    if (options.agentName) {
        payload.agent_name = options.agentName;
    }

    const resp = await fetch('/settings/join', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload)
    });
    if (!resp.ok) {
        const txt = await resp.text();
        throw new Error(txt || resp.statusText || 'join failed');
    }
    const body = await resp.json();
    if (!body || body.success !== true) {
        const errMsg = body && body.error ? body.error : 'join failed';
        throw new Error(errMsg);
    }
    rememberServerURL(serverURL);
    await refreshServerConnectionUI({ silent: true });
    return body;
}

async function runJoinWorkflow(defaultURL) {
    try {
        const remembered = defaultURL || getRememberedServerURL() || 'https://';
        const server = await window.__pm_shared.showPrompt('Server base URL (e.g. https://printmaster.example:9443):', remembered);
        if (!server) return false;

        window.__pm_shared.showToast('Validating server...', 'info', 2000);
        let normalizedServer = server;
        let insecure = false;
        const probe = await probeServerConnection(server);
        if (probe && probe.server_url) {
            normalizedServer = probe.server_url;
        }
        const probeResult = await evaluateProbeResult(probe);
        if (!probeResult.ok) {
            if (probeResult.message) {
                window.__pm_shared.showToast(probeResult.message, 'error', 4000);
            }
            return false;
        }
        insecure = !!probeResult.insecure;

        const token = await window.__pm_shared.showPrompt('Join token (copy on create):', '');
        if (!token) return false;

        window.__pm_shared.showToast('Joining server...', 'info', 3000);

        const body = await submitJoinRequest(normalizedServer, token, { insecure });
        if (body && body.success) {
            // Cache tenant info for header display
            if (body.tenant_id) {
                cachedServerInfo = cachedServerInfo || {};
                cachedServerInfo.tenant_id = body.tenant_id;
                cachedServerInfo.tenant_name = body.tenant_name || body.tenant_id;
                updateHeaderInfoBar();
            }
            const tenantDisplay = body.tenant_name || body.tenant_id || 'unknown';
            window.__pm_shared.showToast('Joined server. Tenant: ' + tenantDisplay, 'success', 4000);
            return true;
        }
    } catch (err) {
        window.__pm_shared.showToast('Failed to join server: ' + (err && err.message ? err.message : err), 'error', 4000);
        return false;
    }
    return false;
}

function openDeviceAuthModal(defaultURL) {
    const modal = document.getElementById('device_auth_modal');
    if (!modal) return;
    resetDeviceAuthModal({ preserveFields: true });
    const serverInput = document.getElementById('device_auth_server_url');
    const preferred = defaultURL || serverInput?.value || defaultServerURL();
    if (serverInput && preferred) {
        serverInput.value = preferred;
    }
    modal.style.display = 'flex';
    modal.classList.add('active');
    if (serverInput) {
        setTimeout(() => serverInput.focus(), 30);
    }
}

function closeDeviceAuthModal() {
    const modal = document.getElementById('device_auth_modal');
    if (!modal) return;
    resetDeviceAuthModal({ preserveFields: true });
    modal.classList.remove('active');
    modal.style.display = 'none';
}

function openJoinCodeFlowFromDeviceAuth() {
    const serverInput = document.getElementById('device_auth_server_url');
    const url = (serverInput && serverInput.value) ? serverInput.value : defaultServerURL();
    if (url) {
        rememberServerURL(url);
    }
    closeDeviceAuthModal();
    setTimeout(() => {
        runJoinWorkflow(url && url.trim() ? url : defaultServerURL());
    }, 150);
}

function resetDeviceAuthModal(options = {}) {
    const { preserveFields = true } = options;
    stopDeviceAuthPolling();
    deviceAuthState.pollToken = '';
    deviceAuthState.serverURL = '';
    deviceAuthState.caPath = '';
    deviceAuthState.insecure = false;
    deviceAuthState.agentName = '';
    deviceAuthState.authorizeURL = '';
    const pending = document.getElementById('device_auth_pending');
    if (pending) pending.classList.add('hidden');
    updateDeviceAuthStatusBadge('pending', 'Waiting for approval…');
    const codeEl = document.getElementById('device_auth_code');
    if (codeEl) codeEl.textContent = '••••••';
    const linkEl = document.getElementById('device_auth_authorize_link');
    if (linkEl) {
        linkEl.href = '#';
        linkEl.classList.add('disabled');
    }
    const expiresEl = document.getElementById('device_auth_expires');
    if (expiresEl) expiresEl.textContent = '—';
    const startBtn = document.getElementById('device_auth_start_btn');
    if (startBtn) {
        startBtn.disabled = false;
        startBtn.textContent = 'Start approval';
    }
    const restartBtn = document.getElementById('device_auth_restart_btn');
    if (restartBtn) restartBtn.disabled = true;
    if (!preserveFields) {
        const serverInput = document.getElementById('device_auth_server_url');
        const agentNameInput = document.getElementById('device_auth_agent_name');
        const caPathInput = document.getElementById('device_auth_ca_path');
        const insecureInput = document.getElementById('device_auth_insecure');
        if (serverInput) serverInput.value = '';
        if (agentNameInput) agentNameInput.value = '';
        if (caPathInput) caPathInput.value = '';
        if (insecureInput) insecureInput.checked = false;
    }
    setDeviceAuthMessage('', 'info');
}

function setDeviceAuthMessage(text, kind = 'info') {
    const messageEl = document.getElementById('device_auth_message');
    if (!messageEl) return;
    if (!text) {
        messageEl.textContent = '';
        messageEl.className = 'device-auth-message';
        return;
    }
    messageEl.textContent = text;
    messageEl.className = 'device-auth-message ' + kind;
}

function updateDeviceAuthStatusBadge(status, text) {
    const badge = document.getElementById('device_auth_status_badge');
    const label = document.getElementById('device_auth_status_text');
    if (!badge || !label) return;
    const map = {
        pending: { label: 'Pending', className: 'device-auth-status-pending', hint: 'Waiting for approval…' },
        approved: { label: 'Approved', className: 'device-auth-status-approved', hint: 'Approved' },
        rejected: { label: 'Rejected', className: 'device-auth-status-rejected', hint: 'Rejected' },
        expired: { label: 'Expired', className: 'device-auth-status-expired', hint: 'Expired' }
    };
    const meta = map[status] || map.pending;
    badge.textContent = meta.label;
    badge.className = 'device-auth-badge ' + meta.className;
    label.textContent = text || meta.hint;
}

async function startDeviceAuthFlow() {
    const serverInput = document.getElementById('device_auth_server_url');
    const agentNameInput = document.getElementById('device_auth_agent_name');
    const caPathInput = document.getElementById('device_auth_ca_path');
    const insecureInput = document.getElementById('device_auth_insecure');
    const startBtn = document.getElementById('device_auth_start_btn');
    if (!serverInput || !startBtn) return;
    const serverURL = (serverInput.value || '').trim();
    if (!serverURL) {
        setDeviceAuthMessage('Enter the server URL before starting approval.', 'error');
        return;
    }
    rememberServerURL(serverURL);
    const caPath = (caPathInput?.value || '').trim();
    const agentName = (agentNameInput?.value || '').trim();
    const insecure = !!(insecureInput?.checked);
    setDeviceAuthMessage('Requesting approval from server…', 'info');
    startBtn.disabled = true;
    startBtn.textContent = 'Starting…';
    try {
        const resp = await fetch('/settings/device-auth/start', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                server_url: serverURL,
                agent_name: agentName || undefined,
                ca_path: caPath || undefined,
                insecure: insecure
            })
        });
        if (!resp.ok) {
            const txt = await resp.text();
            throw new Error(txt || resp.statusText || 'device auth start failed');
        }
        const data = await resp.json();
        deviceAuthState.pollToken = data.poll_token;
        deviceAuthState.serverURL = serverURL;
        deviceAuthState.caPath = caPath;
        deviceAuthState.insecure = insecure;
        deviceAuthState.agentName = agentName;
        deviceAuthState.authorizeURL = data.authorize_url || '';
        const pending = document.getElementById('device_auth_pending');
        if (pending) pending.classList.remove('hidden');
        const codeEl = document.getElementById('device_auth_code');
        if (codeEl) codeEl.textContent = data.code || '———';
        const linkEl = document.getElementById('device_auth_authorize_link');
        if (linkEl) {
            if (deviceAuthState.authorizeURL) {
                linkEl.href = deviceAuthState.authorizeURL;
                linkEl.classList.remove('disabled');
            } else {
                linkEl.href = '#';
                linkEl.classList.add('disabled');
            }
        }
        const expiresEl = document.getElementById('device_auth_expires');
        if (expiresEl) {
            expiresEl.textContent = data.expires_at ? new Date(data.expires_at).toLocaleString() : '—';
        }
        updateDeviceAuthStatusBadge('pending', data.message || 'Waiting for approval…');
        setDeviceAuthMessage('Waiting for approval…', 'info');
        const restartBtn = document.getElementById('device_auth_restart_btn');
        if (restartBtn) restartBtn.disabled = false;
        startBtn.textContent = 'Awaiting approval';
        startDeviceAuthPolling();
    } catch (err) {
        setDeviceAuthMessage(err && err.message ? err.message : 'Failed to start approval', 'error');
        startBtn.disabled = false;
        startBtn.textContent = 'Start approval';
    }
}

function startDeviceAuthPolling() {
    stopDeviceAuthPolling();
    deviceAuthState.pollTimer = window.setInterval(pollDeviceAuthStatus, DEVICE_AUTH_POLL_INTERVAL);
    pollDeviceAuthStatus();
}

function stopDeviceAuthPolling() {
    if (deviceAuthState.pollTimer) {
        clearInterval(deviceAuthState.pollTimer);
        deviceAuthState.pollTimer = null;
    }
}

async function pollDeviceAuthStatus() {
    if (!deviceAuthState.pollToken || !deviceAuthState.serverURL) {
        stopDeviceAuthPolling();
        return;
    }
    try {
        const payload = {
            server_url: deviceAuthState.serverURL,
            poll_token: deviceAuthState.pollToken,
            insecure: !!deviceAuthState.insecure
        };
        if (deviceAuthState.caPath) {
            payload.ca_path = deviceAuthState.caPath;
        }
        const resp = await fetch('/settings/device-auth/poll', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(payload)
        });
        if (!resp.ok) {
            const txt = await resp.text();
            throw new Error(txt || resp.statusText || 'device auth poll failed');
        }
        const data = await resp.json();
        const status = (data.status || 'pending').toLowerCase();
        updateDeviceAuthStatusBadge(status, data.message || '');
        if (status === 'pending') {
            setDeviceAuthMessage('Still waiting for approval…', 'info');
            return;
        }
        if (status === 'approved') {
            if (data.join_token) {
                stopDeviceAuthPolling();
                setDeviceAuthMessage('Approval granted. Completing join…', 'success');
                await finalizeDeviceAuthJoin(data.join_token, data.agent_name);
                return;
            }
            stopDeviceAuthPolling();
            setDeviceAuthMessage(data.message || 'Approval succeeded but server did not return a join token.', 'error');
            const startBtn = document.getElementById('device_auth_start_btn');
            if (startBtn) {
                startBtn.disabled = false;
                startBtn.textContent = 'Start approval';
            }
            return;
        }
        if (status === 'rejected' || status === 'expired') {
            stopDeviceAuthPolling();
            const msg = data.message || (status === 'rejected' ? 'Request rejected' : 'Request expired');
            setDeviceAuthMessage(msg, 'error');
            const startBtn = document.getElementById('device_auth_start_btn');
            if (startBtn) {
                startBtn.disabled = false;
                startBtn.textContent = 'Start approval';
            }
            return;
        }
        stopDeviceAuthPolling();
        const fallback = data.message || ('Unexpected status: ' + status);
        setDeviceAuthMessage(fallback, 'error');
        const startBtn = document.getElementById('device_auth_start_btn');
        if (startBtn) {
            startBtn.disabled = false;
            startBtn.textContent = 'Start approval';
        }
    } catch (err) {
        stopDeviceAuthPolling();
        setDeviceAuthMessage(err && err.message ? err.message : 'Failed to poll approval status', 'error');
        const startBtn = document.getElementById('device_auth_start_btn');
        if (startBtn) {
            startBtn.disabled = false;
            startBtn.textContent = 'Start approval';
        }
    }
}

async function finalizeDeviceAuthJoin(joinToken, approvedName) {
    const startBtn = document.getElementById('device_auth_start_btn');
    if (startBtn) {
        startBtn.disabled = true;
        startBtn.textContent = 'Completing…';
    }
    try {
        const result = await submitJoinRequest(deviceAuthState.serverURL, joinToken, {
            insecure: deviceAuthState.insecure,
            caPath: deviceAuthState.caPath,
            agentName: approvedName || deviceAuthState.agentName
        });
        // Cache tenant info for header display
        if (result && result.tenant_id) {
            cachedServerInfo = cachedServerInfo || {};
            cachedServerInfo.tenant_id = result.tenant_id;
            cachedServerInfo.tenant_name = result.tenant_name || result.tenant_id;
            updateHeaderInfoBar();
        }
        const tenantDisplay = (result && result.tenant_name) || (result && result.tenant_id) || 'unknown';
        window.__pm_shared && window.__pm_shared.showToast && window.__pm_shared.showToast('Joined server. Tenant: ' + tenantDisplay, 'success', 4000);
        updateDeviceAuthStatusBadge('approved', 'Join complete');
        setDeviceAuthMessage('Joined server successfully.', 'success');
        try {
            await refreshServerConnectionUI({ silent: true });
        } catch (_) {}
        setTimeout(() => {
            closeDeviceAuthModal();
        }, 1500);
    } catch (err) {
        setDeviceAuthMessage('Approval succeeded but join failed: ' + (err && err.message ? err.message : err), 'error');
        if (startBtn) {
            startBtn.disabled = false;
            startBtn.textContent = 'Try again';
        }
    }
}

function initDeviceAuthModal() {
    const modal = document.getElementById('device_auth_modal');
    if (!modal) return;
    const closeButtons = ['device_auth_close_x', 'device_auth_cancel_btn'];
    closeButtons.forEach(id => {
        const el = document.getElementById(id);
        if (el) {
            el.addEventListener('click', closeDeviceAuthModal);
        }
    });
    modal.addEventListener('click', evt => {
        if (evt.target === modal) {
            closeDeviceAuthModal();
        }
    });
    const startBtn = document.getElementById('device_auth_start_btn');
    if (startBtn) {
        startBtn.addEventListener('click', startDeviceAuthFlow);
    }
    const restartBtn = document.getElementById('device_auth_restart_btn');
    if (restartBtn) {
        restartBtn.addEventListener('click', () => resetDeviceAuthModal({ preserveFields: true }));
    }
    const codeBtn = document.getElementById('device_auth_code_btn');
    if (codeBtn) {
        codeBtn.addEventListener('click', openJoinCodeFlowFromDeviceAuth);
    }
    resetDeviceAuthModal({ preserveFields: true });
}

async function refreshServerConnectionUI(options = {}) {
    if (serverStatusFetchPromise) {
        return serverStatusFetchPromise;
    }
    const { silent } = options;
    serverStatusFetchPromise = (async () => {
        try {
            const resp = await fetch('/settings/server');
            if (!resp.ok) {
                throw new Error('status ' + resp.status);
            }
            const data = await resp.json();
            applyServerConnectionStatus(data, { silent });
        } catch (err) {
            if (!silent) {
                try { window.__pm_shared.warn('Failed to load server status', err); } catch (e) {}
            }
            applyServerConnectionStatus(null, { silent: true });
        } finally {
            serverStatusFetchPromise = null;
        }
    })();
    return serverStatusFetchPromise;
}

function populateServerInfoModal(status) {
    const info = status || {};
    const connected = !!info.connected;
    const connectionMode = deriveServerConnectionMode(info);
    const setField = (id, value) => {
        const el = document.getElementById(id);
        if (el) el.textContent = value;
    };
    const statusEl = document.getElementById('server_info_status');
    if (statusEl) {
        let statusLabel = 'Disconnected';
        if (connectionMode === 'live') {
            statusLabel = 'Live (WebSocket)';
        } else if (connectionMode === 'connected') {
            statusLabel = 'Connected';
        }
        statusEl.textContent = statusLabel;
        statusEl.classList.toggle('connected', connectionMode !== 'disconnected');
        statusEl.setAttribute('data-mode', connectionMode);
    }
    setField('server_info_url', info.url || 'Not configured');
    setField('server_info_agent_id', info.agent_id || 'Not generated yet');
    setField('server_info_name', info.name || 'Using system hostname');
    setField('server_info_insecure', info.insecure_skip_verify ? 'TLS verification is skipped' : 'TLS verification enforced');
    setField('server_info_ca_path', info.ca_path || 'Not configured');
    const heartbeat = info.heartbeat_interval > 0 ? info.heartbeat_interval + 's' : 'default';
    const upload = info.upload_interval > 0 ? info.upload_interval + 's' : 'default';
    setField('server_info_intervals', 'Heartbeat: ' + heartbeat + ' • Upload: ' + upload);
    setField('server_info_last_heartbeat', describeServerTimestamp(info.last_heartbeat));
    setField('server_info_last_upload', describeServerTimestamp(info.last_device_upload));
    setField('server_info_last_metrics', describeServerTimestamp(info.last_metrics_upload));
    setField('server_info_agent_token', info.has_agent_token ? 'Yes' : 'No');
    setField('server_info_join_token', info.has_join_token ? 'Yes' : 'No');
}

function describeServerTimestamp(value) {
    if (!value) return 'Never';
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return 'Never';
    const formatter = window.__pm_shared && typeof window.__pm_shared.formatRelativeTime === 'function'
        ? window.__pm_shared.formatRelativeTime
        : null;
    const relative = formatter ? formatter(date.toISOString()) : '';
    const absolute = date.toLocaleString();
    return relative ? relative + ' — ' + absolute : absolute;
}

function openServerInfoModal() {
    populateServerInfoModal(currentServerStatus);
    const modal = document.getElementById('server_info_modal');
    if (!modal) return;
    modal.style.display = 'flex';
    modal.classList.add('active');
}

function closeServerInfoModal() {
    const modal = document.getElementById('server_info_modal');
    if (!modal) return;
    modal.classList.remove('active');
    modal.style.display = 'none';
}

async function handleServerRejoin() {
    const defaultURL = currentServerStatus && currentServerStatus.url
        ? currentServerStatus.url
        : (getRememberedServerURL() || 'https://');
    const joined = await runJoinWorkflow(defaultURL);
    if (joined) {
        closeServerInfoModal();
    }
}

async function handleServerUnjoin() {
    const confirmed = await window.__pm_shared.showConfirm(
		'Disconnecting will stop uploads and remove stored server tokens. You can reconnect later with a new join token.',
		'Disconnect from server',
        true
    );
    if (!confirmed) return;
    try {
        const resp = await fetch('/settings/server', { method: 'DELETE' });
        if (!resp.ok) {
            const txt = await resp.text();
			throw new Error(txt || resp.statusText || 'disconnect failed');
        }
        window.__pm_shared.showToast('Server connection removed', 'success', 3000);
        closeServerInfoModal();
        await refreshServerConnectionUI();
    } catch (err) {
		window.__pm_shared.showToast('Failed to disconnect from server: ' + (err && err.message ? err.message : err), 'error', 4000);
    }
}

function initServerConnectionControls() {
    try {
        const btn = document.getElementById('join_token_btn');
        if (!btn) return;
        btn.classList.add('modal-button', 'modal-button-primary');
        btn.dataset.mode = 'standalone';
        btn.addEventListener('click', () => {
            if (btn.dataset.mode === 'connected') {
                openServerInfoModal();
            } else {
                openDeviceAuthModal(defaultServerURL());
            }
        });

        initDeviceAuthModal();

        const overlay = document.getElementById('server_info_modal');
        if (overlay) {
            overlay.addEventListener('click', evt => {
                if (evt.target === overlay) closeServerInfoModal();
            });
        }
        ['server_info_close_btn', 'server_info_close_x'].forEach(id => {
            const el = document.getElementById(id);
            if (el) el.addEventListener('click', closeServerInfoModal);
        });
        const rejoinBtn = document.getElementById('server_info_rejoin_btn');
        if (rejoinBtn) {
            rejoinBtn.addEventListener('click', handleServerRejoin);
        }
        const unjoinBtn = document.getElementById('server_info_unjoin_btn');
        if (unjoinBtn) {
            unjoinBtn.addEventListener('click', handleServerUnjoin);
        }
        const reloadSettingsBtn = document.getElementById('server_info_reload_settings_btn');
        if (reloadSettingsBtn) reloadSettingsBtn.addEventListener('click', () => reloadServerSettings(reloadSettingsBtn));
        const settingsReloadBtn = document.getElementById('reload_server_settings_btn');
        if (settingsReloadBtn) settingsReloadBtn.addEventListener('click', () => reloadServerSettings(settingsReloadBtn));

        refreshServerConnectionUI();
    } catch (e) {
        try { window.__pm_shared.warn('initServerConnectionControls failed', e); } catch (err) {}
    }
}

try { document.addEventListener('DOMContentLoaded', initServerConnectionControls); } catch (e) {}
