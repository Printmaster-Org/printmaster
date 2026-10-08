// Shared metrics UI and charting code extracted from agent/web/app.js
// Exposes the same global symbols used by the app: loadDeviceMetrics, initializeCustomDatetimePicker,
// setMetricsQuickRange, refreshMetricsChart and lower-level helpers.

const TONER_COLOR_ORDER = ['black', 'cyan', 'magenta', 'yellow', 'maintenance', 'waste'];
const TONER_COLOR_MAP = {
    black: '#4a4a4a',
    cyan: '#00bcd4',
    magenta: '#d81b60',
    yellow: '#fbc02d',
    maintenance: '#ffb300',
    waste: '#ef6c00',
    default: '#8bc34a',
};

function normalizeTonerValue(value) {
    if (value === null || value === undefined || value === '') return null;
    if (typeof value === 'number' && !Number.isNaN(value)) {
        return Math.min(100, Math.max(0, value));
    }
    const numeric = parseFloat(String(value).replace(/[^0-9.-]/g, ''));
    if (Number.isNaN(numeric)) return null;
    return Math.min(100, Math.max(0, numeric));
}

function canonicalizeTonerName(name) {
    const raw = (name || '').trim();
    const lower = raw.toLowerCase();
    if (!raw) return { label: 'Supply', colorKey: 'default' };
    const aliases = { k: 'Black', c: 'Cyan', m: 'Magenta', y: 'Yellow' };
    if (aliases[lower]) return { label: aliases[lower], colorKey: aliases[lower].toLowerCase() };
    if (lower.includes('waste')) return { label: raw, colorKey: 'waste' };
    if (lower.includes('maint') || lower.includes('service')) {
        return { label: raw, colorKey: 'maintenance' };
    }
    const cleaned = window.__pm_shared_cards?.getSupplyDisplayName
        ? window.__pm_shared_cards.getSupplyDisplayName(raw) : raw.replace(/[_]+/g, ' ').replace(/\s+/g, ' ').trim();
    const titled = cleaned.replace(/\b\w/g, (c) => c.toUpperCase());
    return { label: titled || 'Supply', colorKey: 'default' };
}

function extractSnapshotTonerEntries(snapshot) {
    let rawLevels = {};
    try {
        if (window.__pm_shared_cards && typeof window.__pm_shared_cards.buildTonerLevels === 'function') {
            rawLevels = window.__pm_shared_cards.buildTonerLevels(null, snapshot) || {};
        } else if (snapshot) {
            rawLevels = snapshot.toners || snapshot.toner || snapshot.toner_levels || {};
        }
    } catch (e) {
        rawLevels = snapshot && (snapshot.toners || snapshot.toner || snapshot.toner_levels) || {};
    }

    const entries = [];
    Object.entries(rawLevels || {}).forEach(([name, value]) => {
        const normalized = normalizeTonerValue(value);
        if (normalized === null) return;
        const meta = canonicalizeTonerName(name);
        entries.push({ label: meta.label, colorKey: meta.colorKey, value: normalized });
    });
    return entries;
}

function buildTonerSeries(history) {
    const series = {};
    if (!Array.isArray(history)) return series;
    history.forEach(snapshot => {
        const timestamp = new Date(snapshot.timestamp).getTime();
        if (Number.isNaN(timestamp)) return;
        extractSnapshotTonerEntries(snapshot).forEach(entry => {
            // Cartridge identity, not its base ink color, stays stable between snapshots.
            const key = entry.label;
            if (!series[key]) {
                series[key] = { colorKey: entry.colorKey, points: [] };
            }
            series[key].points.push({ time: timestamp, value: entry.value });
        });
    });
    return series;
}

function filterTonerSeriesForRange(series, startTime, endTime) {
    if (!series) return {};
    const result = {};
    const startMs = startTime.getTime();
    const endMs = endTime.getTime();
    Object.entries(series).forEach(([name, entry]) => {
        const points = (entry.points || []).filter(pt => pt.time >= startMs && pt.time <= endMs);
        if (points.length > 0) {
            result[name] = { colorKey: entry.colorKey, points };
        }
    });
    return result;
}

function resolveTonerColor(label, colorKey) {
    if (!/waste|maint|service/i.test(label) && window.__pm_shared_cards?.getTonerColor) {
        return window.__pm_shared_cards.getTonerColor(label);
    }
    const loweredKey = (colorKey || '').toLowerCase();
    if (TONER_COLOR_MAP[loweredKey]) return TONER_COLOR_MAP[loweredKey];
    const lower = (label || '').toLowerCase();
    if (lower.includes('black')) return TONER_COLOR_MAP.black;
    if (lower.includes('cyan')) return TONER_COLOR_MAP.cyan;
    if (lower.includes('magenta')) return TONER_COLOR_MAP.magenta;
    if (lower.includes('yellow')) return TONER_COLOR_MAP.yellow;
    if (lower.includes('waste')) return TONER_COLOR_MAP.waste;
    if (lower.includes('maint')) return TONER_COLOR_MAP.maintenance;
    return TONER_COLOR_MAP.default;
}

function legendSortKey(name, colorKey) {
    const loweredKey = (colorKey || '').toLowerCase();
    const idx = TONER_COLOR_ORDER.indexOf(loweredKey);
    if (idx !== -1) return idx;
    return TONER_COLOR_ORDER.length + name.toLowerCase().charCodeAt(0);
}

function renderTonerLegend(container, tonerSeries) {
    const legendEl = (container && container.querySelector) ? container.querySelector('#toner_legend') : document.getElementById('toner_legend');
    if (!legendEl) return;
    const keys = Object.keys(tonerSeries || {});
    if (keys.length === 0) {
        legendEl.style.display = 'none';
        legendEl.innerHTML = '';
        return;
    }
    const sorted = keys.sort((a, b) => {
        const aKey = legendSortKey(a, tonerSeries[a].colorKey);
        const bKey = legendSortKey(b, tonerSeries[b].colorKey);
        if (aKey === bKey) {
            return a.localeCompare(b);
        }
        return aKey - bKey;
    });

    legendEl.style.display = 'flex';
    legendEl.style.flexWrap = 'wrap';
    legendEl.replaceChildren(...sorted.map(name => {
        const entry = tonerSeries[name];
        const color = resolveTonerColor(name, entry.colorKey);
        const lastPoint = entry.points && entry.points[entry.points.length - 1];
        const latestText = lastPoint ? Math.round(lastPoint.value) + '%' : '–';
        const item = document.createElement('div');
        item.className = 'toner-legend-item';
        item.style.cssText = 'display:flex;align-items:center;gap:6px;padding:4px 10px;background:rgba(0,0,0,0.15);border-radius:999px;margin:4px 6px;color:var(--text);font-size:12px';
        const swatch = document.createElement('span');
        swatch.style.cssText = 'width:10px;height:10px;border-radius:50%;background:' + color;
        const label = document.createElement('span');
        label.textContent = name + ': ';
        const level = document.createElement('strong');
        level.textContent = latestText;
        label.appendChild(level);
        item.append(swatch, label);
        return item;
    }));
}

// Load device metrics history and display in UI with interactive timeframe selector
// If targetId is provided, render UI into that element. Otherwise render into default '#metrics_content'.
async function loadDeviceMetrics(serial, targetId, options = {}) {
    try { window.__pm_shared && window.__pm_shared.debug && window.__pm_shared.debug('[Metrics] (shared) loadDeviceMetrics called for serial:', serial); } catch (e) {}
    let contentEl = null;
    if (targetId) contentEl = document.getElementById(targetId);
    if (!contentEl) contentEl = document.getElementById('metrics_content');
    if (!contentEl) {
        window.__pm_shared.error('[Metrics] metrics_content element not found and no target available');
        return;
    }
    window.metricsDataRange?.flatpickr?.destroy?.();
    contentEl.dataset.metricsSerial = serial;
    const generation = (contentEl._metricsGeneration || 0) + 1;
    contentEl._metricsGeneration = generation;
    contentEl._metricsOptions = options;
    const previousRows = document.getElementById('metrics_rows_panel');
    if (previousRows) previousRows.remove();
    try { window.__pm_shared && window.__pm_shared.debug && window.__pm_shared.debug('[Metrics] Rendering metrics into element:', contentEl.id || contentEl.tagName); } catch (e) {}

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
    html += "<button id=\"preset_day\" data-action=\"preset\" data-preset=\"day\" data-serial=\"" + serial + "\" style=\"padding:6px 12px;font-size:13px;min-height:32px\">Last 24 Hours</button>";
    html += "<button id=\"preset_week\" data-action=\"preset\" data-preset=\"week\" data-serial=\"" + serial + "\" style=\"padding:6px 12px;font-size:13px;min-height:32px\">Last 7 Days</button>";
    html += "<button id=\"preset_month\" data-action=\"preset\" data-preset=\"month\" data-serial=\"" + serial + "\" style=\"padding:6px 12px;font-size:13px;min-height:32px\">Last 30 Days</button>";
    html += "<button id=\"preset_year\" data-action=\"preset\" data-preset=\"year\" data-serial=\"" + serial + "\" style=\"padding:6px 12px;font-size:13px;min-height:32px\">Last Year</button>";
    html += "<button id=\"preset_all\" data-action=\"preset\" data-preset=\"all\" data-serial=\"" + serial + "\" style=\"padding:6px 12px;font-size:13px;min-height:32px\">All Time</button>";
    html += '</div>'; // close metrics_custom_range
    html += '</div>'; // close metrics_time_selector
    html += '</div>';

    // Custom datetime range picker with flatpickr
    html += '<div style="margin-bottom:12px">';
    html += '<div style="font-size:12px;color:var(--muted);margin-bottom:8px">Custom Range:</div>';
    html += '<input type="text" id="metrics_datetime_range" aria-label="Metrics date range" placeholder="Select date range..." style="width:100%;background:var(--panel);border:1px solid var(--border);color:var(--text);padding:10px;border-radius:4px;font-size:14px;min-height:38px;cursor:pointer" readonly />';
    html += '</div>';

    // Data range info
    html += '<div style="font-size:11px;color:var(--muted);margin-bottom:8px;padding:6px;background:rgba(0,0,0,0.1);border-radius:4px">';
    html += 'Available: <span id="metrics_data_range_start">Loading...</span> to <span id="metrics_data_range_end">Loading...</span>';
    html += '</div>';

    // Raw data toggle and Apply button
    html += '<div style="display:flex;align-items:center;gap:12px">';
    html += '<label style="display:flex;align-items:center;gap:6px;font-size:12px;color:var(--muted);cursor:pointer">';
    html += '<input type="checkbox" id="metrics_raw_mode" style="width:16px;height:16px;accent-color:#268bd2" />';
    html += '<span>Raw data mode</span>';
    html += '</label>';
    html += '<button data-action="refresh" data-serial="' + serial + '" style="flex:1;padding:10px;font-size:14px;min-height:40px;font-weight:600;background:#268bd2;color:#fff">Update Chart</button>';
    html += '</div>';
    html += '</div>';

    // Stats summary
    html += '<div id="metrics_stats" style="margin-bottom:12px;font-size:12px"></div>';

    // Chart canvas
    html += '<canvas id="metrics_chart" role="img" aria-label="Page counts and supply levels over the selected date range. Values are available in the metrics tables and supply legend." width="500" height="200" style="width:100%;max-height:200px;background:var(--bg);border:1px solid var(--border);border-radius:4px"></canvas>';
    html += '<div id="toner_legend" style="margin-top:8px;display:none;gap:8px;flex-wrap:wrap"></div>';

    contentEl.innerHTML = html;

    // Initialize metrics data range (will be set after first data load)
    window.metricsDataRange = { min: null, max: null, serial: serial, flatpickr: null };

    // Initialize flatpickr and load data
    await initializeCustomDatetimePicker(serial, contentEl, options.preset, generation);
    // Ensure chart/table refresh runs after picker init so UI shows data without requiring the user to click
    try {
        if (contentEl._metricsGeneration === generation && window.metricsDataRange?.flatpickr) await refreshMetricsChart(serial);
    } catch (e) {
        try { window.__pm_shared.warn && window.__pm_shared.warn('[Metrics] Failed to auto-refresh after init:', e); } catch(_) {}
    }
}

function showMetricsLoadError(contentEl, serial, error) {
    window.__pm_shared.error('[Metrics] Loading failed', { serial, error });
    if (!contentEl) return;
    const canvas = contentEl.querySelector('#metrics_chart');
    if (canvas) canvas.hidden = true;
    renderTonerLegend(contentEl, {});
    const rows = document.getElementById('metrics_rows_panel');
    if (rows) rows.hidden = true;
    let status = contentEl.querySelector('#metrics_stats');
    if (!status) {
        status = document.createElement('div');
        contentEl.appendChild(status);
    }
    status.setAttribute('role', 'status');
    status.replaceChildren(document.createTextNode('Metrics unavailable. Previous results are not current. '));
    const retry = document.createElement('button');
    retry.type = 'button';
    retry.textContent = 'Retry';
    retry.onclick = () => loadDeviceMetrics(serial, contentEl.id, contentEl._metricsOptions || {});
    status.appendChild(retry);
}

function getMetricsInitialRange(minTime, maxTime, preset) {
    const days = { day: 1, week: 7, '7day': 7, month: 30, year: 365 }[preset];
    return [days ? new Date(Math.max(minTime.getTime(), maxTime.getTime() - days * 86400000)) : minTime, maxTime];
}

// Initialize custom datetime picker with actual data bounds.
async function initializeCustomDatetimePicker(serial, contentElOverride, preset, generation) {
    const contentEl = contentElOverride || document.getElementById('metrics_content');
    const current = () => !contentElOverride || generation === undefined ||
        (contentEl._metricsGeneration === generation && contentEl.dataset.metricsSerial === serial);
    try { window.__pm_shared && window.__pm_shared.debug && window.__pm_shared.debug('[Metrics] (shared) initializeCustomDatetimePicker called'); } catch (e) {}
    try {
        // Fetch only bounds to determine available data range (fast, small payload)
        const url = '/api/devices/metrics/bounds?serial=' + encodeURIComponent(serial);
        try { window.__pm_shared && window.__pm_shared.debug && window.__pm_shared.debug('[Metrics] Fetching bounds:', url); } catch (e) {}
        const res = await fetch(url);
        if (!current()) return;
        if (!res.ok) throw new Error('Metrics bounds request failed (HTTP ' + res.status + ')');

        const bounds = await res.json();
        if (!current()) return;
        try { window.__pm_shared && window.__pm_shared.debug && window.__pm_shared.debug('[Metrics] Bounds:', bounds); } catch (e) {}
        // Use provided content element or fall back to global

        const minTime = bounds && bounds.min_timestamp ? new Date(bounds.min_timestamp) : null;
        const maxTime = bounds && bounds.max_timestamp ? new Date(bounds.max_timestamp) : null;
        if (!minTime || !maxTime || isNaN(minTime.getTime()) || isNaN(maxTime.getTime())) {
            try { window.__pm_shared && window.__pm_shared.warn && window.__pm_shared.warn('[Metrics] No history data available'); } catch (e) {}
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

        // Await the shared flatpickr loader so we avoid race conditions where
        // metrics initialize before the library script has executed.
        const fpLib = await (window.__pm_shared && window.__pm_shared.flatpickrReady ? window.__pm_shared.flatpickrReady : Promise.resolve(window.flatpickr || null));
        if (!current()) return;
        if (!fpLib) throw new Error('Metrics date picker is unavailable');
        try { window.__pm_shared && window.__pm_shared.debug && window.__pm_shared.debug('[Metrics] Initializing flatpickr with full range:', minTime, 'to', maxTime); } catch (e) {}
        // Initialize flatpickr with range mode
        const targetSelector = contentElOverride ? (contentElOverride.querySelector('#metrics_datetime_range')) : document.querySelector('#metrics_datetime_range');
        const fpInstance = fpLib(targetSelector, {
            // Keep the calendar inside the dialog's active focus/inert boundary.
            appendTo: contentEl || undefined,
            mode: 'range',
            enableTime: true,
            dateFormat: 'Y-m-d H:i',
            minDate: minTime,
            maxDate: maxTime,
            // Default to all available data on initial load
            defaultDate: getMetricsInitialRange(minTime, maxTime, preset),
            time_24hr: true,
            onChange: function (selectedDates, dateStr, instance) {
                try { window.__pm_shared && window.__pm_shared.trace && window.__pm_shared.trace('[Metrics] Range changed:', selectedDates); } catch (e) {}
                // Auto-refresh chart when range changes
                if (selectedDates.length === 2) {
                    if (current()) refreshMetricsChart(serial);
                }
            },
            onReady: function (selectedDates, dateStr, instance) {
                try { window.__pm_shared && window.__pm_shared.trace && window.__pm_shared.trace('[Metrics] flatpickr ready, refreshing chart'); } catch (e) {}
            }
        });

        // Store flatpickr instance for programmatic updates
        window.metricsDataRange.flatpickr = fpInstance;

        // Enable/disable preset buttons based on available data range
        updatePresetButtonStates(minTime, maxTime);

    } catch (e) {
        if (current()) showMetricsLoadError(contentEl, serial, e);
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

// Delegated handlers for metrics UI quick interactions (toggle, preset, refresh)
document.addEventListener('click', async function (ev) {
    try {
        await (window.__pm_shared && window.__pm_shared.ready);
        const btn = ev.target.closest && ev.target.closest('[data-action]');
        if (!btn) return;
        const action = btn.getAttribute('data-action');
        if (action === 'toggle-time') {
            const target = btn.getAttribute('data-target') || '';
            toggleMetricsTimeSelector && toggleMetricsTimeSelector(target);
            return;
        }
        if (action === 'preset') {
            const preset = btn.getAttribute('data-preset');
            const serial = btn.getAttribute('data-serial');
            setMetricsQuickRange && setMetricsQuickRange(preset, serial);
            return;
        }
        if (action === 'refresh') {
            const serial = btn.getAttribute('data-serial');
            refreshMetricsChart && refreshMetricsChart(serial);
            return;
        }
    } catch (e) { window.__pm_shared.warn('[Metrics] delegated handler error', e); }
});

// Quick range preset button handler
function setMetricsQuickRange(preset, serial) {
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
async function refreshMetricsChart(serial) {
    window.__pm_shared.log('[Metrics] (shared) refreshMetricsChart called for serial:', serial);
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
    const contentEl = canvas.closest('[data-metrics-serial]');
    if (contentEl && contentEl.dataset.metricsSerial !== serial) return;
    const request = contentEl ? (contentEl._metricsRequest || 0) + 1 : 0;
    const generation = contentEl?._metricsGeneration;
    if (contentEl) contentEl._metricsRequest = request;
    const current = () => !contentEl || (contentEl.isConnected && contentEl.dataset.metricsSerial === serial &&
        contentEl._metricsRequest === request && contentEl._metricsGeneration === generation);

    try {
        // Get selected dates from flatpickr
        const fp = window.metricsDataRange?.flatpickr;
        if (!fp || !fp.selectedDates || fp.selectedDates.length !== 2) {
            window.__pm_shared.warn('[Metrics] No valid date range selected');
            statsEl.textContent = 'Please select a date range';
            canvas.hidden = true;
            renderTonerLegend(container, {});
            return;
        }
        window.__pm_shared.log('[Metrics] Using date range:', fp.selectedDates);

        const startTime = fp.selectedDates[0];
        const endTime = fp.selectedDates[1];

        // Validate range
        if (endTime <= startTime) {
            statsEl.textContent = 'End time must be after start time';
            canvas.hidden = true;
            renderTonerLegend(container, {});
            return;
        }

        // Check for raw mode toggle
        const rawModeCheckbox = container.querySelector('#metrics_raw_mode') || document.getElementById('metrics_raw_mode');
        const rawMode = rawModeCheckbox && rawModeCheckbox.checked;

        // Fetch data with optional raw mode and maxPoints
        const startISO = startTime.toISOString();
        const endISO = endTime.toISOString();
        let url = '/api/devices/metrics/history?serial=' + encodeURIComponent(serial) + '&since=' + encodeURIComponent(startISO) + '&until=' + encodeURIComponent(endISO);
        if (rawMode) {
            url += '&raw=true';
        } else {
            // Default to 200 points for good chart display
            url += '&maxPoints=200';
        }
        window.__pm_shared.log('[Metrics] Fetching chart data:', url, rawMode ? '(raw mode)' : '(downsampled)');
        canvas.hidden = true;
        statsEl.textContent = 'Loading metrics...';
        const previousRows = document.getElementById('metrics_rows_panel');
        if (previousRows) previousRows.hidden = true;
        const res = await fetch(url);
        if (!current()) return;

        if (!res.ok) {
            throw new Error('Metrics history request failed (HTTP ' + res.status + ')');
        }

        const history = await res.json();
        if (!current()) return;
        window.__pm_shared.log('[Metrics] Received', history?.length || 0, 'chart data points' + (rawMode ? ' (raw)' : ' (downsampled)'));
        if (!history || history.length === 0) {
            window.__pm_shared.warn('[Metrics] No data in selected timeframe');
            statsEl.textContent = 'No metrics data in selected timeframe.';
            canvas.hidden = false;
            drawEmptyChart(canvas);
            renderTonerLegend(container, {});
            return;
        }

        const tonerSeries = buildTonerSeries(history);
        const filteredTonerSeries = filterTonerSeriesForRange(tonerSeries, startTime, endTime);

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
        const lifetimePages = window.__pm_shared_cards.getPageCount(latest);
        const oldestPages = window.__pm_shared_cards.getPageCount(oldest);
        const periodPages = lifetimePages === null || oldestPages === null ? null : lifetimePages - oldestPages;
        const avgPagesPerDay = periodPages === null ? 'Not collected' : (periodPages / durationDays).toFixed(1);
        statsHtml += '<tr style="border-bottom:1px solid rgba(255,255,255,0.05)">';
        statsHtml += '<td style="padding:6px 8px;color:var(--text)">Total Pages</td>';
        statsHtml += '<td style="padding:6px 8px;text-align:right;color:var(--text);font-weight:600">' + (lifetimePages === null ? 'Not collected' : lifetimePages.toLocaleString()) + '</td>';
        statsHtml += '<td style="padding:6px 8px;text-align:right;color:#268bd2;font-weight:600">' + (periodPages === null ? 'Not collected' : periodPages.toLocaleString()) + '</td>';
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

        // Note: Consumables are rendered as their own card in the details modal.
        // Do not inject toner/consumable rows into the metrics stats to avoid
        // duplicate UI. The dedicated consumables card is populated elsewhere.

        statsHtml += '</tbody></table>';
        statsEl.innerHTML = statsHtml;
        canvas.hidden = false;
        window.__pm_shared.log('[Metrics] Stats updated, drawing chart');

        // Draw chart
        drawMetricsChart(canvas, history, startTime, endTime, filteredTonerSeries);
        renderTonerLegend(container, filteredTonerSeries);

        // Render a horizontally-scrollable metrics table showing each snapshot and a delete button
        try {
            const pageSize = 200;
            const totalRows = history.length;
            const totalPages = Math.max(1, Math.ceil(totalRows / pageSize));
            const buildRowsHtmlForPage = (pageIndex) => {
                const start = pageIndex * pageSize;
                const end = Math.min(totalRows, start + pageSize);
                let rowsHtml = '';
                for (let i = start; i < end; i++) {
                    const item = history[i];
                    const ts = new Date(item.timestamp).toLocaleString();
                    rowsHtml += '<tr style="border-bottom:1px solid rgba(255,255,255,0.03)">';
                    rowsHtml += '<td style="padding:8px 12px;display:flex;align-items:center;justify-content:space-between">';
                    rowsHtml += '<span>' + ts + ' <span style="color:var(--muted);font-size:11px;margin-left:6px">(' + (item.tier || 'raw') + ')</span></span>';
                    rowsHtml += '<span style="margin-left:12px">';
                    rowsHtml += '<button class="trash-btn" data-id="' + (item.id || '') + '" data-tier="' + (item.tier || '') + '" title="Delete this metrics row"></button>';
                    rowsHtml += '</span>';
                    rowsHtml += '</td>';
                    [window.__pm_shared_cards.getPageCount(item), ...['color_pages', 'mono_pages', 'scan_count', 'fax_pages'].map(
                        key => window.__pm_shared_cards.getPageCount({ page_count: item[key] })
                    )].forEach(value => {
                        rowsHtml += '<td style="padding:8px 12px;text-align:right">' + (value === null ? 'Not collected' : value.toLocaleString()) + '</td>';
                    });
                    rowsHtml += '</tr>';
                }
                return rowsHtml;
            };

            const tableContainerId = 'metrics_table_container';
            let tableHtml = '<div id="' + tableContainerId + '" class="metrics-table-container" style="margin-top:12px;padding:8px;background:rgba(0,0,0,0.05);border-radius:6px;overflow-x:auto">';

            if (totalPages > 1) {
                tableHtml += '<div style="display:flex;align-items:center;justify-content:space-between;gap:8px;margin-bottom:8px">';
                tableHtml += '<div style="color:var(--muted);font-size:12px">';
                tableHtml += '<span id="metrics_rows_pager_info"></span>';
                tableHtml += '</div>';
                tableHtml += '<div style="display:flex;gap:6px">';
                tableHtml += '<button class="btn small metrics-page-btn" data-action="metrics-page" data-dir="prev">Prev</button>';
                tableHtml += '<button class="btn small metrics-page-btn" data-action="metrics-page" data-dir="next">Next</button>';
                tableHtml += '</div>';
                tableHtml += '</div>';
            }

            tableHtml += '<table class="metrics-table" style="border-collapse:collapse;min-width:800px;font-size:13px">';
            tableHtml += '<thead><tr style="border-bottom:2px solid rgba(255,255,255,0.06)">';
            tableHtml += '<th style="padding:8px 12px;text-align:left">Timestamp</th>';
            tableHtml += '<th style="padding:8px 12px;text-align:right">Total</th>';
            tableHtml += '<th style="padding:8px 12px;text-align:right">Color</th>';
            tableHtml += '<th style="padding:8px 12px;text-align:right">Mono</th>';
            tableHtml += '<th style="padding:8px 12px;text-align:right">Scans</th>';
            tableHtml += '<th style="padding:8px 12px;text-align:right">Fax</th>';
            tableHtml += '</tr></thead><tbody id="metrics_rows_tbody"></tbody>';
            tableHtml += '</table></div>';
            const panelId = 'metrics_rows_panel';
            const panelHtml = '<div class="panel" id="' + panelId + '"><h4 style="margin-top:0;color:var(--highlight)">Metrics Rows</h4>' + tableHtml + '</div>';

            const old = document.getElementById(panelId);
            if (old && old.parentElement) old.parentElement.removeChild(old);

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

            // Initialize paging (or single-page render)
            const panelEl = document.getElementById(panelId);
            if (panelEl) {
                const tbody = panelEl.querySelector('#metrics_rows_tbody');
                if (tbody) tbody.innerHTML = buildRowsHtmlForPage(0);
                panelEl._pmMetricsRowsState = {
                    currentPage: 0,
                    totalPages,
                    totalRows,
                    pageSize
                };
                const infoEl = panelEl.querySelector('#metrics_rows_pager_info');
                const prevBtn = panelEl.querySelector('.metrics-page-btn[data-dir="prev"]');
                const nextBtn = panelEl.querySelector('.metrics-page-btn[data-dir="next"]');
                const updatePager = () => {
                    const state = panelEl._pmMetricsRowsState;
                    if (!state) return;
                    const start = state.currentPage * pageSize + 1;
                    const end = Math.min(totalRows, (state.currentPage + 1) * pageSize);
                    if (infoEl) infoEl.textContent = 'Rows ' + start.toLocaleString() + '–' + end.toLocaleString() + ' of ' + totalRows.toLocaleString() + ' (Page ' + (state.currentPage + 1) + '/' + totalPages + ')';
                    if (prevBtn) prevBtn.disabled = state.currentPage <= 0;
                    if (nextBtn) nextBtn.disabled = state.currentPage >= totalPages - 1;
                };
                panelEl._pmMetricsRowsUpdatePager = updatePager;
                updatePager();
            }

            const metricsContainerForEvents = document.getElementById('metrics_modal_body') || document.getElementById('metrics_content');
            if (metricsContainerForEvents) {
                if (metricsContainerForEvents._metricsDeleteHandler) {
                    metricsContainerForEvents.removeEventListener('click', metricsContainerForEvents._metricsDeleteHandler);
                    metricsContainerForEvents._metricsDeleteHandler = null;
                }

                const handler = async (e) => {
                    const pageBtn = e.target.closest('[data-action="metrics-page"]');
                    if (pageBtn) {
                        const dir = pageBtn.getAttribute('data-dir') || '';
                        const panelEl = document.getElementById(panelId);
                        const state = panelEl && panelEl._pmMetricsRowsState;
                        if (!panelEl || !state) return;
                        if (dir === 'prev' && state.currentPage > 0) state.currentPage--;
                        if (dir === 'next' && state.currentPage < state.totalPages - 1) state.currentPage++;
                        const tbody = panelEl.querySelector('#metrics_rows_tbody');
                        if (tbody) tbody.innerHTML = buildRowsHtmlForPage(state.currentPage);
                        if (panelEl._pmMetricsRowsUpdatePager) panelEl._pmMetricsRowsUpdatePager();
                        return;
                    }

                    const btn = e.target.closest('.trash-btn');
                    if (!btn) return;
                    const id = btn.getAttribute('data-id');
                    const tier = btn.getAttribute('data-tier') || '';
                    if (!id) return;

                    const confirmed = await window.__pm_shared.showConfirm('Delete this metrics row? This action cannot be undone.', 'Confirm Delete', true);
                    if (!confirmed) return;

                    try {
                        btn.disabled = true;
                        const resp = await fetch('/api/devices/metrics/delete', {
                            method: 'POST', headers: { 'Content-Type': 'application/json' },
                            body: JSON.stringify({ id: Number(id), tier: tier })
                        });
                        if (resp.status === 204) {
                            refreshMetricsChart(serial);
                        } else {
                            const txt = await resp.text();
                            window.__pm_shared.showAlert('Failed to delete metric: ' + txt, 'Delete Failed', true, false);
                        }
                    } catch (err) {
                        window.__pm_shared.showAlert('Error deleting metric: ' + err, 'Delete Failed', true, false);
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
        if (current()) showMetricsLoadError(contentEl || container, serial, e);
    }
}

// Draw smooth line graph showing cumulative page count over time
function drawMetricsChart(canvas, history, startTime, endTime, tonerSeries) {
    window.__pm_shared.log('[Metrics] (shared) drawMetricsChart called with', history.length, 'points');
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
        value: window.__pm_shared_cards.getPageCount(snapshot)
    })).filter(p => p.value !== null && p.time >= startTime.getTime() && p.time <= endTime.getTime());
    const seriesToPlot = tonerSeries || {};
    const hasTonerSeries = Object.keys(seriesToPlot).length > 0;
    if (dataPoints.length === 0 && !hasTonerSeries) {
        drawEmptyChart(canvas);
        return;
    }

    // Find min/max values for scaling
    const minValue = dataPoints.length ? Math.min(...dataPoints.map(p => p.value)) : 0;
    const maxValue = dataPoints.length ? Math.max(...dataPoints.map(p => p.value)) : 1;
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
    for (let i = 0; dataPoints.length && i <= ySteps; i++) {
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

    const tonerAxis = padding.left + chartWidth;
    const tonerMin = 0;
    const tonerMax = 100;
    const mapTonerY = (value) => padding.top + chartHeight - ((value - tonerMin) / (tonerMax - tonerMin)) * chartHeight;

    if (hasTonerSeries) {
        ctx.save();
        ctx.strokeStyle = 'rgba(255,255,255,0.25)';
        ctx.beginPath();
        ctx.moveTo(tonerAxis, padding.top);
        ctx.lineTo(tonerAxis, padding.top + chartHeight);
        ctx.stroke();

        const tonerTicks = [0, 25, 50, 75, 100];
        ctx.fillStyle = 'rgba(255,255,255,0.6)';
        ctx.font = '10px monospace';
        ctx.textAlign = 'left';
        tonerTicks.forEach(level => {
            const y = mapTonerY(level);
            ctx.beginPath();
            ctx.moveTo(tonerAxis, y);
            ctx.lineTo(tonerAxis + 6, y);
            ctx.stroke();
            ctx.fillText(level + '%', tonerAxis + 8, y + 4);
        });
        ctx.restore();
    }

    // Draw Y-axis labels (page counts)
    ctx.fillStyle = 'rgba(255,255,255,0.6)';
    ctx.font = '11px monospace';
    ctx.textAlign = 'right';
    for (let i = 0; dataPoints.length && i <= ySteps; i++) {
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
            return d.getHours().toString().padStart(2, '0') + ':' + d.getMinutes().toString().padStart(2, '0');
        } else if (durationHours <= 48) {
            return (d.getMonth() + 1) + '/' + d.getDate() + ' ' + d.getHours() + 'h';
        } else {
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

        for (let i = 0; i < dataPoints.length - 1; i++) {
            const curr = dataPoints[i];
            const next = dataPoints[i + 1];
            const currX = mapX(curr.time);
            const currY = mapY(curr.value);
            const nextX = mapX(next.time);
            const nextY = mapY(next.value);

            const cpX = (currX + nextX) / 2;
            const cpY = (currY + nextY) / 2;

            ctx.quadraticCurveTo(currX, currY, cpX, cpY);
        }

        const last = dataPoints[dataPoints.length - 1];
        ctx.lineTo(mapX(last.time), mapY(last.value));
        ctx.lineTo(mapX(last.time), padding.top + chartHeight);
        ctx.closePath();

        const gradient = ctx.createLinearGradient(0, padding.top, 0, padding.top + chartHeight);
        gradient.addColorStop(0, 'rgba(38, 139, 210, 0.3)');
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

            const cpX = (currX + nextX) / 2;
            const cpY = (currY + nextY) / 2;

            ctx.quadraticCurveTo(currX, currY, cpX, cpY);
        }

        const last = dataPoints[dataPoints.length - 1];
        ctx.lineTo(mapX(last.time), mapY(last.value));

        ctx.strokeStyle = '#268bd2';
        ctx.lineWidth = 2.5;
        ctx.lineJoin = 'round';
        ctx.lineCap = 'round';
        ctx.stroke();
    }

    // Draw data points as circles (visible when zoomed in)
    if (dataPoints.length <= 50) {
        ctx.fillStyle = '#268bd2';
        ctx.strokeStyle = '#073642';
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

    if (hasTonerSeries) {
        Object.entries(seriesToPlot).forEach(([name, entry]) => {
            const color = resolveTonerColor(name, entry.colorKey);
            ctx.beginPath();
            ctx.strokeStyle = color;
            ctx.lineWidth = 1.5;
            ctx.moveTo(mapX(entry.points[0].time), mapTonerY(entry.points[0].value));
            for (let i = 1; i < entry.points.length; i++) {
                ctx.lineTo(mapX(entry.points[i].time), mapTonerY(entry.points[i].value));
            }
            ctx.stroke();

            if (entry.points.length <= 60) {
                entry.points.forEach(pt => {
                    ctx.beginPath();
                    ctx.fillStyle = color;
                    ctx.strokeStyle = '#002b36';
                    ctx.lineWidth = 1;
                    ctx.arc(mapX(pt.time), mapTonerY(pt.value), 3, 0, Math.PI * 2);
                    ctx.fill();
                    ctx.stroke();
                });
            }
        });
    }

    // Chart title
    ctx.fillStyle = 'rgba(255,255,255,0.8)';
    ctx.font = 'bold 12px sans-serif';
    ctx.textAlign = 'left';
    ctx.fillText('Page Count Over Time', padding.left, 18);
    if (hasTonerSeries) {
        ctx.font = '10px sans-serif';
        ctx.textAlign = 'right';
        ctx.fillText('Consumable %', tonerAxis, 18);
    }

    // Data point count indicator (top right)
    ctx.font = '10px monospace';
    ctx.textAlign = 'right';
    ctx.fillStyle = 'rgba(255,255,255,0.5)';
    ctx.fillText(dataPoints.length + ' points', width - padding.right, 18);
}

// Draw empty chart placeholder
function drawEmptyChart(canvas) {
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


// Expose as globals for backward compatibility (most existing code calls these directly)
// Also register under a private shared namespace so consumers can reliably call shared implementations
window.__pm_shared_metrics = window.__pm_shared_metrics || {};
window.__pm_shared_metrics.loadDeviceMetrics = loadDeviceMetrics;
window.__pm_shared_metrics.initializeCustomDatetimePicker = initializeCustomDatetimePicker;
window.__pm_shared_metrics.setMetricsQuickRange = setMetricsQuickRange;
window.__pm_shared_metrics.refreshMetricsChart = refreshMetricsChart;
window.__pm_shared_metrics.drawMetricsChart = drawMetricsChart;
window.__pm_shared_metrics.drawEmptyChart = drawEmptyChart;
window.__pm_shared_metrics.buildTonerSeries = buildTonerSeries;
window.__pm_shared_metrics.resolveTonerColor = resolveTonerColor;
window.__pm_shared_metrics.getMetricsInitialRange = getMetricsInitialRange;
window.__pm_shared_metrics.renderTonerLegend = renderTonerLegend;

// Lightweight usage-sparkline loader used in saved-device cards
async function loadUsageGraph(serial) {
    if (!serial) return;
    const graphId = 'usage-graph-' + serial.toString().replace(/[^a-zA-Z0-9]/g,'_');
    const container = document.getElementById(graphId);
    if (!container) return;

    // Show loading state
    container.innerHTML = '<canvas class="usage-graph-canvas" style="width:100%;height:80px;display:block"></canvas>';
    const canvas = container.querySelector('canvas');
    if (!canvas) return;

    try {
        const url = '/api/devices/metrics/history?serial=' + encodeURIComponent(serial) + '&period=month';
        const res = await fetch(url);
        if (!res.ok) throw new Error('Usage history request failed (HTTP ' + res.status + ')');
        const history = await res.json();
        if (!history || history.length === 0) {
            container.innerHTML = '<div class="usage-graph-no-data">No data</div>';
            return;
        }

        const points = history.map(h => ({ t: new Date(h.timestamp).getTime(), v: window.__pm_shared_cards.getPageCount(h) }))
            .filter(point => Number.isFinite(point.t) && point.v !== null);
        if (!points.length) {
            container.innerHTML = '<div class="usage-graph-no-data">Not collected</div>';
            return;
        }
        drawUsageSparkline(canvas, points);
    } catch (e) {
        window.__pm_shared.warn('[Metrics] loadUsageGraph failed for', serial, e);
        container.innerHTML = '<div class="usage-graph-no-data" role="status">Usage unavailable</div>';
        const retry = document.createElement('button');
        retry.type = 'button';
        retry.textContent = 'Retry';
        retry.addEventListener('click', () => loadUsageGraph(serial));
        container.appendChild(retry);
    }
}

function drawUsageSparkline(canvas, points) {
    const ctx = canvas.getContext('2d');
    if (!ctx) return;
    // DPI handling
    const dpr = window.devicePixelRatio || 1;
    const rect = canvas.getBoundingClientRect();
    canvas.width = rect.width * dpr;
    canvas.height = rect.height * dpr;
    ctx.scale(dpr, dpr);

    ctx.clearRect(0, 0, rect.width, rect.height);
    if (!points || points.length === 0) {
        ctx.fillStyle = 'rgba(255,255,255,0.3)';
        ctx.font = '12px sans-serif';
        ctx.textAlign = 'center';
        ctx.fillText('No data', rect.width / 2, rect.height / 2);
        return;
    }

    // Normalize
    const times = points.map(p => p.t);
    const vals = points.map(p => p.v);
    const minT = Math.min(...times);
    const maxT = Math.max(...times);
    const minV = Math.min(...vals);
    const maxV = Math.max(...vals);
    const tRange = Math.max(1, maxT - minT);
    const vRange = Math.max(1, maxV - minV);

    const padding = { left: 4, right: 4, top: 8, bottom: 12 };
    const w = rect.width - padding.left - padding.right;
    const h = rect.height - padding.top - padding.bottom;

    const mapX = (t) => padding.left + ((t - minT) / tRange) * w;
    const mapY = (v) => padding.top + h - ((v - minV) / vRange) * h;

    // Area gradient
    ctx.beginPath();
    ctx.moveTo(mapX(times[0]), mapY(vals[0]));
    for (let i = 1; i < points.length; i++) {
        ctx.lineTo(mapX(times[i]), mapY(vals[i]));
    }
    ctx.lineTo(padding.left + w, padding.top + h);
    ctx.lineTo(padding.left, padding.top + h);
    ctx.closePath();
    const grad = ctx.createLinearGradient(0, padding.top, 0, padding.top + h);
    grad.addColorStop(0, 'rgba(38,139,210,0.25)');
    grad.addColorStop(1, 'rgba(38,139,210,0.03)');
    ctx.fillStyle = grad;
    ctx.fill();

    // Line
    ctx.beginPath();
    ctx.moveTo(mapX(times[0]), mapY(vals[0]));
    for (let i = 1; i < points.length; i++) {
        ctx.lineTo(mapX(times[i]), mapY(vals[i]));
    }
    ctx.strokeStyle = '#268bd2';
    ctx.lineWidth = 1.5;
    ctx.stroke();

    // Latest value label
    const latest = vals[vals.length - 1] || 0;
    ctx.fillStyle = 'rgba(255,255,255,0.9)';
    ctx.font = '11px monospace';
    ctx.textAlign = 'right';
    ctx.fillText(latest.toLocaleString(), rect.width - 6, 12);
}

// Export usage loader
window.__pm_shared_metrics.loadUsageGraph = loadUsageGraph;

// Prefer exporting top-level names only if nothing else is defined (backwards compatibility)
window.loadDeviceMetrics = window.loadDeviceMetrics || loadDeviceMetrics;
window.initializeCustomDatetimePicker = window.initializeCustomDatetimePicker || initializeCustomDatetimePicker;
window.setMetricsQuickRange = window.setMetricsQuickRange || setMetricsQuickRange;
window.refreshMetricsChart = window.refreshMetricsChart || refreshMetricsChart;
window.loadUsageGraph = window.loadUsageGraph || loadUsageGraph;
