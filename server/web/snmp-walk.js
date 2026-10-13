/**
 * Admin-only SNMP walk capture tool.
 *
 * Starts full SNMP walks on an agent (proxied through the server), shows
 * progress, and lets admins download the resulting .json.gz capture so it can
 * be shared for vendor OID analysis.
 */
(function () {
    'use strict';

    const POLL_INTERVAL_MS = 2000;

    function escapeHtml(value) {
        return String(value ?? '')
            .replace(/&/g, '&amp;')
            .replace(/</g, '&lt;')
            .replace(/>/g, '&gt;')
            .replace(/"/g, '&quot;')
            .replace(/'/g, '&#39;');
    }

    function formatBytes(bytes) {
        const n = Number(bytes) || 0;
        if (n < 1024) return `${n} B`;
        if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
        return `${(n / (1024 * 1024)).toFixed(1)} MB`;
    }

    function formatDuration(ms) {
        const s = Math.round((Number(ms) || 0) / 1000);
        if (s < 60) return `${s}s`;
        return `${Math.floor(s / 60)}m ${s % 60}s`;
    }

    function formatTime(value) {
        if (!value) return '';
        const d = new Date(value);
        return Number.isNaN(d.getTime()) ? '' : d.toLocaleString();
    }

    function toast(message, type) {
        if (window.__pm_shared?.showToast) {
            window.__pm_shared.showToast(message, type || 'info');
        }
    }

    function isAdmin() {
        const shared = window.__pm_shared || {};
        return typeof shared.isGlobalAdmin === 'function' && shared.isGlobalAdmin();
    }

    function apiBase(agentId) {
        return `/api/v1/proxy/agent/${encodeURIComponent(agentId)}/api/diagnostics/snmp-walks`;
    }

    async function readError(response) {
        const text = (await response.text().catch(() => '')).trim();
        if (response.status === 404 && !text.toLowerCase().includes('walk not found')) {
            return 'This agent does not support SNMP walks yet. Update the agent and try again.';
        }
        return text || `HTTP ${response.status}`;
    }

    function statusBadge(status) {
        const colors = {
            running: 'var(--accent, #3b82f6)',
            complete: 'var(--success, #22c55e)',
            failed: 'var(--danger, #ef4444)',
            cancelled: 'var(--muted, #9ca3af)'
        };
        const color = colors[status] || 'var(--muted, #9ca3af)';
        return `<span style="color:${color};font-weight:600;">${escapeHtml(status)}</span>`;
    }

    function renderWalkRow(agentId, walk) {
        const running = walk.status === 'running';
        const progress = running
            ? `${(walk.oid_count || 0).toLocaleString()} OIDs${walk.current_root ? ` &middot; ${escapeHtml(walk.current_root)}` : ''}`
            : `${(walk.oid_count || 0).toLocaleString()} OIDs${walk.truncated ? ' (truncated at limit)' : ''} &middot; ${formatDuration(walk.duration_ms)} &middot; ${formatBytes(walk.file_size)}`;
        const errors = Array.isArray(walk.errors) && walk.errors.length
            ? `<div class="muted-text" style="color:var(--danger, #ef4444);font-size:12px;">${walk.errors.map(escapeHtml).join('<br>')}</div>`
            : '';
        const download = !running && walk.file_size > 0
            ? `<a class="modal-button modal-button-primary" style="text-decoration:none;padding:4px 10px;font-size:12px;" href="${apiBase(agentId)}/${encodeURIComponent(walk.id)}/download" download="${escapeHtml(walk.file_name || 'snmpwalk.json.gz')}">Download</a>`
            : '';
        const remove = `<button class="modal-button ${running ? 'modal-button-secondary' : 'modal-button-danger'}" style="padding:4px 10px;font-size:12px;" data-walk-action="delete" data-walk-id="${escapeHtml(walk.id)}">${running ? 'Cancel' : 'Delete'}</button>`;
        return `
            <div class="snmp-walk-row" data-walk-id="${escapeHtml(walk.id)}" style="display:flex;gap:12px;align-items:flex-start;justify-content:space-between;padding:8px 0;border-bottom:1px solid var(--border);">
                <div style="min-width:0;flex:1;">
                    <div><strong>${escapeHtml(walk.ip)}</strong>${walk.label ? ` &middot; ${escapeHtml(walk.label)}` : ''} &middot; ${statusBadge(walk.status)}</div>
                    <div class="muted-text" style="font-size:12px;">${progress}</div>
                    <div class="muted-text" style="font-size:12px;">${escapeHtml(formatTime(walk.started_at))} &middot; max ${(walk.max_oids || 0).toLocaleString()} &middot; ${escapeHtml((walk.roots || []).join(', '))}</div>
                    ${errors}
                </div>
                <div style="display:flex;gap:6px;flex-shrink:0;">${download}${remove}</div>
            </div>`;
    }

    /**
     * Open the SNMP walk tool for an agent.
     * @param {{agentId: string, ip?: string, label?: string, agentName?: string}} options
     */
    async function openSnmpWalkTool(options) {
        const opts = options || {};
        const agentId = opts.agentId;
        if (!agentId) {
            toast('SNMP walk requires an agent', 'error');
            return;
        }
        if (!isAdmin()) {
            toast('SNMP walks are restricted to administrators', 'error');
            return;
        }

        const previousFocus = document.activeElement;
        const overlay = document.createElement('div');
        overlay.className = 'modal-overlay snmp-walk-modal';
        overlay.style.display = 'flex';
        overlay.innerHTML = `
            <div class="modal-content" style="max-width:760px;" role="dialog" aria-modal="true" aria-labelledby="snmp_walk_title">
                <div class="modal-header"><h3 id="snmp_walk_title">SNMP Walk Capture</h3><button class="modal-close-x" title="Close" aria-label="Close SNMP walk dialog">&times;</button></div>
                <div class="modal-body">
                    <p class="muted-text" style="margin-top:0;">Agent: <strong>${escapeHtml(opts.agentName || agentId)}</strong>. Runs a full SNMP walk from the agent and saves a gzipped JSON capture for vendor analysis. Community strings are never written to the file.</p>
                    <div style="display:grid;grid-template-columns:1fr 1fr;gap:12px;">
                        <label class="field"><span>Device IP</span><input id="snmp_walk_ip" type="text" autocomplete="off" value="${escapeHtml(opts.ip || '')}" placeholder="192.168.1.50"></label>
                        <label class="field"><span>Label (optional)</span><input id="snmp_walk_label" type="text" maxlength="80" autocomplete="off" value="${escapeHtml(opts.label || '')}" placeholder="Model or note"></label>
                    </div>
                    <label class="field"><span>Max OIDs</span><input id="snmp_walk_max" type="number" min="1" step="1000"></label>
                    <label class="field"><span>Roots (one OID per line)</span><textarea id="snmp_walk_roots" rows="3" style="font-family:monospace;"></textarea></label>
                    <div style="display:flex;gap:8px;justify-content:flex-end;margin:8px 0 16px;">
                        <button class="modal-button modal-button-primary" data-action="start" disabled>Start Walk</button>
                    </div>
                    <h4 style="margin:0 0 4px;">Captures on this agent</h4>
                    <div id="snmp_walk_list"><p class="muted-text">Loading…</p></div>
                </div>
                <div class="modal-footer"><button class="modal-button modal-button-secondary" data-action="refresh">Refresh</button><button class="modal-button modal-button-secondary" data-action="close">Close</button></div>
            </div>`;
        document.body.appendChild(overlay);

        const ipInput = overlay.querySelector('#snmp_walk_ip');
        const labelInput = overlay.querySelector('#snmp_walk_label');
        const maxInput = overlay.querySelector('#snmp_walk_max');
        const rootsInput = overlay.querySelector('#snmp_walk_roots');
        const startBtn = overlay.querySelector('[data-action="start"]');
        const listEl = overlay.querySelector('#snmp_walk_list');
        let pollTimer = null;
        let closed = false;
        let defaultsApplied = false;

        const close = () => {
            if (closed) return;
            closed = true;
            if (pollTimer) clearTimeout(pollTimer);
            overlay.remove();
            previousFocus?.focus?.();
        };

        const schedulePoll = (walks) => {
            if (pollTimer) clearTimeout(pollTimer);
            pollTimer = null;
            if (!closed && walks.some(w => w.status === 'running')) {
                pollTimer = setTimeout(refresh, POLL_INTERVAL_MS);
            }
        };

        async function refresh() {
            if (closed) return;
            try {
                const response = await fetch(apiBase(agentId), { credentials: 'same-origin' });
                if (!response.ok) throw new Error(await readError(response));
                const data = await response.json();
                if (closed) return;
                if (!defaultsApplied) {
                    defaultsApplied = true;
                    maxInput.value = data.default_max_oids || 100000;
                    if (data.max_max_oids) maxInput.max = data.max_max_oids;
                    rootsInput.value = (data.default_roots || []).join('\n');
                    startBtn.disabled = false;
                }
                const walks = Array.isArray(data.walks) ? data.walks : [];
                listEl.innerHTML = walks.length
                    ? walks.map(w => renderWalkRow(agentId, w)).join('')
                    : '<p class="muted-text">No captures yet.</p>';
                schedulePoll(walks);
            } catch (err) {
                if (closed) return;
                listEl.innerHTML = `<p class="muted-text" style="color:var(--danger, #ef4444);">${escapeHtml(err.message || err)}</p>`;
            }
        }

        async function start() {
            const ip = ipInput.value.trim();
            if (!ip) {
                toast('Enter a device IP', 'warning');
                ipInput.focus();
                return;
            }
            const roots = rootsInput.value.split(/[\s,]+/).map(s => s.trim()).filter(Boolean);
            const maxOids = parseInt(maxInput.value, 10) || 0;
            startBtn.disabled = true;
            try {
                const response = await fetch(apiBase(agentId), {
                    method: 'POST',
                    credentials: 'same-origin',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ ip, roots, max_oids: maxOids, label: labelInput.value.trim() })
                });
                if (!response.ok) throw new Error(await readError(response));
                toast(`SNMP walk started for ${ip}`, 'success');
                await refresh();
            } catch (err) {
                toast(`Failed to start walk: ${err.message || err}`, 'error');
            } finally {
                if (!closed) startBtn.disabled = false;
            }
        }

        async function removeWalk(id, button) {
            const running = button.textContent.trim() === 'Cancel';
            if (!running && window.__pm_shared?.showConfirm) {
                const ok = await window.__pm_shared.showConfirm('Delete this SNMP walk capture from the agent?', 'Delete Capture', true);
                if (!ok) return;
            }
            button.disabled = true;
            try {
                const response = await fetch(`${apiBase(agentId)}/${encodeURIComponent(id)}`, { method: 'DELETE', credentials: 'same-origin' });
                if (!response.ok) throw new Error(await readError(response));
                toast(running ? 'Walk cancelled' : 'Capture deleted', 'success');
            } catch (err) {
                toast(`Failed: ${err.message || err}`, 'error');
            }
            await refresh();
        }

        startBtn.addEventListener('click', start);
        overlay.querySelector('[data-action="refresh"]').addEventListener('click', refresh);
        overlay.querySelector('[data-action="close"]').addEventListener('click', close);
        overlay.querySelector('.modal-close-x').addEventListener('click', close);
        overlay.addEventListener('click', event => { if (event.target === overlay) close(); });
        overlay.addEventListener('keydown', event => {
            if (event.key === 'Escape') {
                event.stopPropagation();
                close();
            }
        });
        listEl.addEventListener('click', event => {
            const button = event.target.closest('[data-walk-action="delete"]');
            if (button) removeWalk(button.getAttribute('data-walk-id'), button);
        });

        ipInput.focus();
        await refresh();
    }

    window.__pm_shared = window.__pm_shared || {};
    window.__pm_shared.openSnmpWalkTool = openSnmpWalkTool;
})();
