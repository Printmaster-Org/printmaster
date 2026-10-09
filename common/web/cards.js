// Shared card view utilities for PrintMaster UIs
// Exports renderSavedCard(item) and checkDatabaseRotationWarning()
(function(){
    function makeClipboardIconFallback() {
        return '<svg class="clipboard-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">' +
            '<rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect>' +
            '<path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path>' +
            '</svg>';
    }

    function renderCapabilitiesFallback(device) {
        if (!device) return '';
        const caps = [];
        try {
            // Normalize source of capability and metric fields
            const raw = device.raw_data || device.metrics || device.latest || device.latest_metrics || device.metrics_latest || device;
            
            // Check for explicit capability flags first (from SNMP capability detection)
            const hasExplicitCaps = raw.is_color !== undefined || raw.is_mono !== undefined || 
                                    raw.is_copier !== undefined || raw.is_scanner !== undefined || 
                                    raw.is_fax !== undefined || raw.device_type !== undefined;
            
            if (hasExplicitCaps) {
                // Use device_type if available (most descriptive)
                if (raw.device_type) {
                    caps.push('<span class="capability-badge type">' + escapeHtmlCards(raw.device_type) + '</span>');
                } else {
                    // Fallback to individual color/mono
                    if (raw.is_color) caps.push('<span class="capability-badge color">Color</span>');
                    else if (raw.is_mono) caps.push('<span class="capability-badge mono">Mono</span>');
                }
                
                // Function badges (only if no device_type to avoid redundancy)
                if (!raw.device_type) {
                    if (raw.is_copier) caps.push('<span class="capability-badge function">Copier</span>');
                    if (raw.is_scanner) caps.push('<span class="capability-badge function">Scanner</span>');
                    if (raw.is_fax) caps.push('<span class="capability-badge function">Fax</span>');
                }
                
                // Technology badge
                if (raw.is_laser) caps.push('<span class="capability-badge tech">Laser</span>');
                else if (raw.is_inkjet) caps.push('<span class="capability-badge tech">Inkjet</span>');
                
                // Feature badge
                if (raw.has_duplex) caps.push('<span class="capability-badge feature">Duplex</span>');
            } else {
                // Legacy fallback: infer capabilities from metrics
                const hasColor = raw.color_impressions || raw.color_pages || device.color_impressions || device.color_pages || false;
                const hasBlack = raw.page_count || raw.mono_impressions || device.page_count || device.mono_impressions || false;
                if (hasColor) caps.push('<span class="capability-badge color">Color</span>');
                if (hasBlack && !hasColor) caps.push('<span class="capability-badge mono">Mono</span>');
                if (device.fax) caps.push('<span class="capability-badge function">Fax</span>');
                if (device.scan) caps.push('<span class="capability-badge function">Scan</span>');
                if (device.duplex) caps.push('<span class="capability-badge feature">Duplex</span>');
            }
        } catch (e) {
            return '';
        }
        return caps.length ? '<div class="capabilities-container">' + caps.join('') + '</div>' : '';
    }
    
    // Render device source badges based on unified device type fields
    function renderDeviceSourceBadges(item) {
        if (!item) return '';
        const badges = [];
        
        // Device type badge with icon
        const deviceType = item.device_type || '';
        const sourceType = item.source_type || '';
        const isUSB = item.is_usb || false;
        
        // Device type icons
        const typeIcons = {
            'network': '🌐',
            'usb': '🔌',
            'local': '🖥️',
            'shared': '📁',
            'virtual': '📄'
        };
        
        const icon = typeIcons[deviceType] || '🖨️';
        
        if (deviceType) {
            let badgeClass = 'source-badge';
            if (deviceType === 'usb') badgeClass += ' usb';
            else if (deviceType === 'network') badgeClass += ' network';
            else if (deviceType === 'shared') badgeClass += ' shared';
            else if (deviceType === 'virtual') badgeClass += ' virtual';
            else if (deviceType === 'local') badgeClass += ' local';
            
            badges.push('<span class="' + badgeClass + '">' + icon + ' ' + escapeHtmlCards(deviceType) + '</span>');
        } else if (isUSB) {
            badges.push('<span class="source-badge usb">🔌 USB</span>');
        }
        
        // Source type badge (how it was discovered)
        if (sourceType === 'spooler') {
            badges.push('<span class="source-badge spooler">📋 Spooler</span>');
        } else if (sourceType === 'snmp') {
            badges.push('<span class="source-badge snmp">📡 SNMP</span>');
        } else if (sourceType === 'manual') {
            badges.push('<span class="source-badge manual">✏️ Manual</span>');
        }
        
        // Additional flags
        if (item.is_default) {
            badges.push('<span class="source-badge default">⭐ Default</span>');
        }
        if (item.is_shared) {
            badges.push('<span class="source-badge shared">🔗 Shared</span>');
        }
        
        return badges.length ? '<div class="source-badges">' + badges.join('') + '</div>' : '';
    }
    
    // Simple HTML escaping for card content
    function escapeHtmlCards(str) {
        if (!str) return '';
        return String(str).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
    }

    function renderSavedCard(item) {
        const device = (item && item.printer_info) || {};
        const serial = item && item.serial ? item.serial : '';
    const toners = buildTonerLevels(device);
        const lifeCount = getPageCount(device);

        const graphId = 'usage-graph-' + (serial || (device.ip||'')).toString().replace(/[^a-zA-Z0-9]/g,'_');
        const usageGraphHTML = '<div id="' + graphId + '" class="usage-graph-container">' +
            '<div class="usage-graph-no-data">Loading usage data...</div>' +
            '</div>';

        // Consumables - render a compact (mini) section on the saved device card
        // so the card remains compact while the details modal shows the full UI.
        const consumablesSection = (toners && Object.keys(toners).length > 0) ?
            ('<div class="saved-device-card-section">' +
                '<div class="saved-device-card-section-title">Consumables</div>' +
                '<div class="consumable-container">' + renderMiniConsumablesSection(toners) + '</div></div>') : '';

        const capabilitiesHTML = (typeof renderCapabilities === 'function') ? renderCapabilities(device) : renderCapabilitiesFallback(device);
        const deviceSourceBadgesHTML = renderDeviceSourceBadges(item);
    const clipIcon = (window.__pm_shared && typeof window.__pm_shared.makeClipboardIcon === 'function') ? window.__pm_shared.makeClipboardIcon() : makeClipboardIconFallback();

        const ipVal = device.ip || 'N/A';
        const macVal = device.mac || '';
        const deviceKey = serial || ipVal || '';
        
        // Determine WebUI URL - use IPP-USB proxy for USB devices
        let webUIUrl = item && item.web_ui_url ? item.web_ui_url : '';
        const isUSB = item && (item.is_usb || item.device_type === 'usb');
        if (isUSB && serial && !webUIUrl) {
            // USB devices use the unified /proxy/ endpoint (same as network devices)
            // The agent automatically routes USB serials to IPP-USB proxy
            webUIUrl = '/proxy/' + encodeURIComponent(serial) + '/';
        }

        return `<div class="saved-device-card card-entering" data-device-key="${deviceKey}" data-make="${device.manufacturer||''}" data-model="${device.model||''}" data-ip="${device.ip||''}" data-serial="${serial}" data-device-type="${item&&item.device_type||''}" data-source-type="${item&&item.source_type||''}" data-is-usb="${isUSB}">` +
            `<div class="saved-device-card-header"><div class="saved-device-card-main">` +
            `<h5 class="saved-device-card-title">${device.manufacturer||'Unknown'} ${device.model||''}</h5>` +
            `${capabilitiesHTML}` +
            `${deviceSourceBadgesHTML}` +
            `<p class="saved-device-card-subtitle copyable" role="button" tabindex="0" aria-label="Copy serial number" data-copy="${serial}">Serial: ${serial}${clipIcon}</p>` +
            `<p class="saved-device-card-subtitle"><span class="copyable" role="button" tabindex="0" aria-label="Copy IP address" data-copy="${ipVal}" style="display:inline-flex;align-items:center;gap:4px;">IP: ${ipVal}${clipIcon}</span>` + (macVal?`<span class="copyable" role="button" tabindex="0" aria-label="Copy MAC address" data-copy="${macVal}" style="display:inline-flex;align-items:center;gap:4px;margin-left:8px;"> • MAC: ${macVal}${clipIcon}</span>`:'') + `</p>` +
            `</div><div style="display:flex;gap:8px;flex-wrap:wrap;">` +
            (webUIUrl ? `<button class="primary" style="font-size:12px" data-action="webui" data-webui-url="${webUIUrl}" data-serial="${serial}">WebUI</button>` : '') +
            `<button data-action="details" data-ip="${device.ip||''}" data-serial="${serial}" data-source="saved">Details</button>` +
            `<button class="delete" data-action="delete" data-serial="${serial}">Delete</button>` +
            `</div></div>` +
            `<div class="saved-device-card-grid"><div class="saved-device-card-inner-panel">` +
            `<div class="saved-device-card-section"><div class="saved-device-card-section-title">Device Info</div>` +
            `<div class="saved-device-card-row"><span class="saved-device-card-label">Asset #</span><span class="saved-device-card-value editable-field" role="button" tabindex="0" aria-label="Edit asset number" data-action="edit" data-serial="${serial}" data-field="asset_number" data-current="${item&&item.asset_number?item.asset_number:''}">${item&&item.asset_number?item.asset_number:'(click to add)'}</span></div>` +
            `<div class="saved-device-card-row"><span class="saved-device-card-label">Location</span><span class="saved-device-card-value editable-field" role="button" tabindex="0" aria-label="Edit location" data-action="edit" data-serial="${serial}" data-field="location" data-current="${item&&item.location?item.location:''}">${item&&item.location?item.location:'(click to add)'}</span></div>` +
            `<div class="saved-device-card-row"><span class="saved-device-card-label">Total Pages</span><span class="saved-device-card-value" title="${lifeCount === null ? 'Page count not collected' : 'Last reported page count'}">${lifeCount === null ? 'Not collected' : lifeCount.toLocaleString()}</span></div>` +
            `</div>${consumablesSection}</div>${usageGraphHTML}</div></div>`;
    }

    // Preserve any existing global renderCapabilities if present, otherwise
    // provide a safe implementation that delegates to the fallback renderer.
    function renderCapabilities(device) {
        return renderCapabilitiesFallback(device);
    }

    // Render a consumable with smart coloring/icons and progress bar.
    // This mirrors the richer behavior from the agent bundle so both
    // saved cards and the details modal show consistent supply UI.
    function renderConsumable(name, value, isLevel) {
        // If it's a numeric level (0-100), show as progress bar
        if (isLevel) {
            const v = Number(value);
            const pct = Number.isFinite(v) ? Math.max(0, Math.min(100, v)) : null;
            const color = getConsumableColor(name, pct);
            const label = escapeHtmlCards(getSupplyDisplayName(name));
            return '<div class="consumable-level" style="--consumable-color:' + color + '">'
                + '<div class="consumable-level-header"><span class="consumable-level-name">'
                + '<span class="consumable-color-swatch" aria-hidden="true"></span>' + label + '</span>'
                + '<span class="consumable-level-percent">' + (pct === null ? 'n/a' : pct + '%') + '</span></div>'
                + '<div class="consumable-level-track" role="progressbar" aria-label="' + label
                + '" aria-valuemin="0" aria-valuemax="100"'
                + (pct === null ? '' : ' aria-valuenow="' + pct + '"') + '>'
                + '<div class="consumable-level-fill" style="width:' + (pct === null ? 0 : pct) + '%"></div>'
                + '</div></div>';
        } else {
            // Text description (e.g., part numbers, status messages)
            return '<div class="consumable-description"><span>' + escapeHtmlCards(getSupplyDisplayName(name)) + ':</span><span>' + escapeHtmlCards(value) + '</span></div>';
        }
    }

    // Map of toner/ink color names to CSS colors, used by the compact
    // per-color "toner bar" indicators shown in device lists/tables.
    const TONER_COLORS = {
        'black': '#1a1a1a',
        'cyan': '#00bcd4',
        'magenta': '#e91e63',
        'yellow': '#ffc107',
        'photo_black': '#333',
        'matte_black': '#444',
        'light_cyan': '#4dd0e1',
        'light_magenta': '#f48fb1',
        'gray': '#9e9e9e',
        'light_gray': '#bdbdbd',
        'orange': '#ff9800',
        'green': '#4caf50',
        'red': '#f44336',
        'blue': '#2196f3',
        'violet': '#9c27b0',
    };

    function getTonerColor(name) {
        const key = String(name || '').toLowerCase().replace(/grey/g, 'gray')
            .replace(/[^a-z]+/g, '_').replace(/^_|_$/g, '');
        const aliases = { k: 'black', c: 'cyan', m: 'magenta', y: 'yellow' };
        if (aliases[key]) return TONER_COLORS[aliases[key]];
        if (TONER_COLORS[key]) return TONER_COLORS[key];
        // Match specific shades before their base colors, including supply keys.
        for (const colorName of Object.keys(TONER_COLORS).sort((a, b) => b.length - a.length)) {
            if (('_' + key + '_').includes('_' + colorName + '_')) return TONER_COLORS[colorName];
        }
        // Default gray for unknown
        return '#757575';
    }

    // Ink/toner color keywords - only these appear in the compact toner bars
    const INK_COLOR_KEYWORDS = ['black', 'cyan', 'magenta', 'yellow', 'photo', 'matte', 'light', 'gray', 'grey', 'red', 'blue', 'green', 'orange', 'violet'];
    // Maintenance/non-ink keywords to exclude from toner bars
    const NON_INK_KEYWORDS = ['drum', 'belt', 'waste', 'fuser', 'maintenance', 'kit', 'transfer', 'opc', 'developer', 'roller', 'unit'];

    function isInkOrToner(name) {
        const lower = (name || '').toLowerCase();
        for (const kw of NON_INK_KEYWORDS) {
            if (lower.includes(kw)) return false;
        }
        for (const kw of INK_COLOR_KEYWORDS) {
            if (lower.includes(kw)) return true;
        }
        if (lower.includes('toner') || lower.includes('ink')) return true;
        return false;
    }

    function getConsumableColor(name, level) {
        const lower = String(name || '').toLowerCase();
        if (lower.includes('waste') || lower.includes('maintenance')) {
            if (level === null) return '#888';
            return level > 80 ? '#d32f2f' : level > 50 ? '#f57c00' : '#388e3c';
        }
        if (isInkOrToner(name) || /^[kcmy]$/i.test(name)) return getTonerColor(name);
        if (level === null) return '#888';
        return level < 20 ? '#d32f2f' : level < 50 ? '#f57c00' : '#388e3c';
    }

    function normalizeTonerLevel(value) {
        if (value === null || value === undefined || value === '') return null;
        const num = Number(value);
        if (!Number.isFinite(num)) return null;
        return Math.max(0, Math.min(100, Math.round(num)));
    }

    function getPageCount(device) {
        for (const value of [device?.page_count, device?.total_mono_impressions]) {
            if (value === null || value === undefined || String(value).trim() === '') continue;
            const count = Number(value);
            if (Number.isFinite(count) && count >= 0) return count;
        }
        return null;
    }

    function getSupplyDisplayName(name) {
        return String(name || '').replace(/^supply[_\s]+/i, '').replace(/_/g, ' ')
            .replace(/\s+/g, ' ').trim();
    }

    function normalizeSupplyLevels(levels) {
        const result = {};
        const identities = new Map();
        for (const [name, value] of Object.entries(levels || {})) {
            const identity = getSupplyIdentity(name);
            const prior = identities.get(identity);
            // Only collapse proven aliases, never distinct cartridges or conflicting readings.
            const equal = prior && (String(result[prior]) === String(value) ||
                (normalizeTonerLevel(result[prior]) !== null && normalizeTonerLevel(result[prior]) === normalizeTonerLevel(value)));
            if (equal) {
                if (/^supply[_\s]/i.test(prior) && !/^supply[_\s]/i.test(name)) {
                    delete result[prior];
                    result[name] = value;
                    identities.set(identity, name);
                }
                continue;
            }
            result[name] = value;
            identities.set(identity, name);
        }
        return result;
    }

    function getSupplyIdentity(name) {
        return getSupplyDisplayName(name).toLowerCase().replace(/\bgrey\b/g, 'gray')
            .replace(/[^a-z0-9]+/g, ' ').trim();
    }

    function resolveSupplyNames(levels, device, latest) {
        const sources = [device, latest];
        for (const source of [...sources]) {
            if (source) sources.push(source.raw_data, source.raw, source.metrics);
        }
        for (const source of [...sources]) {
            if (source?.raw_data) sources.push(source.raw_data);
        }
        const description = color => {
            for (const source of sources) {
                const value = source?.[`toner_desc_${color}`];
                if (typeof value === 'string' && value.trim()) return value.trim();
            }
            return '';
        };
        const isInk = sources.some(source => source?.is_inkjet === true || /inkjet/i.test(source?.device_type || '')) ||
            ['black', 'cyan', 'magenta', 'yellow'].some(color => /\bink\b/i.test(description(color))) ||
            [...Object.keys(levels), ...sources.flatMap(source => Array.isArray(source?.consumables) ? source.consumables : [])]
                .some(name => /\bink\b/i.test(getSupplyDisplayName(name)));
        const result = {};
        const originalNames = new Map();
        for (const [name, value] of Object.entries(levels)) {
            const generic = /^(?:(?:toner|ink)[_ ](?:level[_ ])?)?(black|cyan|magenta|yellow|k|c|m|y)$/i.exec(name);
            let label = name;
            let fallbackLabel = name;
            if (generic) {
                const color = { k: 'black', c: 'cyan', m: 'magenta', y: 'yellow' }[generic[1].toLowerCase()] || generic[1].toLowerCase();
                fallbackLabel = `${color[0].toUpperCase()}${color.slice(1)} ${isInk ? 'Ink' : 'Toner'}`;
                label = description(color) || fallbackLabel;
            } else if (isInk && /^waste[_ ]toner$/i.test(name)) {
                // A shared waste metric cannot identify which of multiple maintenance boxes it measures.
                label = 'Waste Ink';
            }
            if (Object.prototype.hasOwnProperty.call(result, label) && String(result[label]) !== String(value)) {
                const priorName = originalNames.get(label);
                if (name === label && priorName !== label) {
                    result[priorName] = result[label];
                    result[label] = value;
                    originalNames.set(label, name);
                } else {
                    result[fallbackLabel] = value;
                }
            } else {
                result[label] = value;
                originalNames.set(label, fallbackLabel);
            }
        }
        return result;
    }

    // Build the compact per-color toner bar dataset for a device/printer_info
    // object (and optional latest metrics snapshot). Reuses buildTonerLevels()
    // for source resolution and mono/color filtering, then narrows to actual
    // ink/toner entries with numeric levels, sorted K/C/M/Y first.
    function getDeviceTonerBarData(p, latest) {
        const levels = buildTonerLevels(p, latest);
        const order = ['black', 'cyan', 'magenta', 'yellow'];
        const entries = [];
        for (const [name, value] of Object.entries(levels)) {
            if (!isInkOrToner(name)) continue;
            const level = normalizeTonerLevel(value);
            if (level === null) continue;
            entries.push({ name, level, color: getTonerColor(name) });
        }
        entries.sort((a, b) => {
            const aKey = a.name.toLowerCase();
            const bKey = b.name.toLowerCase();
            const aIdx = order.findIndex(c => aKey.includes(c));
            const bIdx = order.findIndex(c => bKey.includes(c));
            if (aIdx !== -1 && bIdx !== -1) return aIdx - bIdx;
            if (aIdx !== -1) return -1;
            if (bIdx !== -1) return 1;
            return aKey.localeCompare(bKey);
        });
        return entries;
    }

    // Render the compact per-color toner bars (used in device list/table rows).
    // Accepts the array produced by getDeviceTonerBarData().
    function renderTonerBars(tonerData) {
        if (!tonerData || tonerData.length === 0) return '<span class="muted-text">—</span>';
        const bars = tonerData.map(t => {
            const levelClass = t.level <= 10 ? 'critical' : t.level <= 25 ? 'low' : '';
            const label = escapeHtmlCards(getSupplyDisplayName(t.name)) + ': ' + t.level + '%';
            return '<div class="toner-bar ' + levelClass + '" role="img" tabindex="0" aria-label="' + label + '" title="' + label + '" style="--toner-color: ' + t.color + '; --toner-level: ' + t.level + '%"></div>';
        }).join('');
        return '<div class="toner-bars">' + bars + '</div>';
    }

    // Render a full consumables section given a map of toner/supply levels
    // Reused by saved cards and the details modal to avoid duplicated markup.
    function renderConsumablesSection(tonerLevels) {
        if (!tonerLevels || Object.keys(tonerLevels).length === 0) return '';
        let out = '<div class="consumables-section">';
        for (const [k, v] of Object.entries(tonerLevels)) {
            // If value looks numeric, treat as a percentage level; otherwise render as text
            const isNumeric = (v !== null && v !== undefined) && (typeof v === 'number' || (!isNaN(Number(v)) && String(v).trim() !== ''));
            out += renderConsumable(k, v, !!isNumeric);
        }
        out += '</div>';
        return out;
    }

    // Build a normalized toner-levels map from available sources.
    // Sources checked (in order): explicit toner_levels/toners on object,
    // provided metrics snapshot (latest), and legacy raw_data keys
    // like toner_level_black / toner_desc_black. Returns an object with
    // friendly keys (e.g. 'Black') mapped to either numeric percentage or
    // descriptive text when a numeric level isn't available.
    function buildTonerLevels(p, latest) {
        const out = {};
        try {
            // If object already has explicit structured toners, use it
            // Check toner_levels (server enriched), toners, and toner
            const explicit = p && (p.toner_levels || p.toners || p.toner);
            // If latest metrics snapshot contains structured toners, prefer that
            const latestStructured = latest && (latest.toner_levels || latest.toners || latest.toner) ? (latest.toner_levels || latest.toners || latest.toner) : null;

            if (explicit && Object.keys(explicit).length > 0) {
                // Copy through
                Object.entries(explicit).forEach(([k, v]) => { out[k] = v; });
            } else if (latestStructured && Object.keys(latestStructured).length > 0) {
                Object.entries(latestStructured).forEach(([k, v]) => { out[k] = v; });
            } else {
                // Fall back to legacy raw_data keys on p.raw_data (toner_level_black etc.)
                const raw = (p && (p.raw_data || p.raw || p.metrics)) || {};
                // Helper to pick numeric value if present
                const pick = (keys) => {
                    for (const k of keys) {
                        if (raw[k] !== undefined && raw[k] !== null) return raw[k];
                        if (latest && latest[k] !== undefined && latest[k] !== null) return latest[k];
                        if (p && p[k] !== undefined && p[k] !== null) return p[k];
                    }
                    return undefined;
                };

                const mappings = [
                    { name: 'Black', keys: ['toner_level_black', 'toner_level_k', 'toner_level_1', 'black_level', 'black_toner'] , descKeys: ['toner_desc_black', 'toner_desc_k', 'toner_desc_1', 'toner_desc'] },
                    { name: 'Cyan', keys: ['toner_level_cyan', 'toner_level_c', 'toner_level_2', 'cyan_level'], descKeys: ['toner_desc_cyan'] },
                    { name: 'Magenta', keys: ['toner_level_magenta', 'toner_level_m', 'toner_level_3', 'magenta_level'], descKeys: ['toner_desc_magenta'] },
                    { name: 'Yellow', keys: ['toner_level_yellow', 'toner_level_y', 'toner_level_4', 'yellow_level'], descKeys: ['toner_desc_yellow'] },
                ];

                for (const m of mappings) {
                    const val = pick(m.keys);
                    if (val !== undefined) {
                        out[pick(m.descKeys) || m.name] = val;
                        continue;
                    }
                    // If no numeric level, try to provide a descriptive name from descKeys
                    const desc = pick(m.descKeys);
                    if (desc) out[m.name] = desc;
                }

                // As a last resort, scan raw for any keys that look like 'toner' or 'cartridge'
                if (Object.keys(out).length === 0) {
                    for (const [k, v] of Object.entries(raw)) {
                        if (!k) continue;
                        const lk = k.toLowerCase();
                        if (lk.includes('toner') || lk.includes('cartridge') || lk.includes('supply')) {
                            // Use a cleaned-up key name
                            const pretty = k.replace(/_/g, ' ').replace(/toner level/i, '').trim() || k;
                            out[pretty] = v;
                        }
                    }
                }
            }

            // Post-process: only include consumables the device actually has.
            // Applies regardless of which source populated `out` above, so
            // mono devices never show phantom CMY entries even when an
            // explicit toner_levels/toners map (still) contains them.
            // Priority: explicit `p.consumables` list if present; otherwise infer
            // from device metrics (color pages) and raw values.
            if (p) {
                // Normalize keys for comparison
                const normalize = getSupplyIdentity;

                // If the device provided a consumables array, use that to limit keys
                if (Array.isArray(p.consumables) && p.consumables.length > 0) {
                    const present = new Set();
                    for (const desc of p.consumables) {
                        const d = normalize(desc);
                        if (d.includes('black') || d.includes(' k') || d === 'k') present.add('black');
                        else if (d.includes('cyan') || d === 'c') present.add('cyan');
                        else if (d.includes('magenta') || d === 'm') present.add('magenta');
                        else if (d.includes('yellow') || d === 'y') present.add('yellow');
                        else {
                            // If description doesn't match a color, include it by its exact text
                            present.add(normalize(desc));
                        }
                    }
                    // Filter out color keys not present
                    for (const k of Object.keys(out)) {
                        const lk = normalize(k);
                        if (lk.includes('black')) {
                            if (!present.has('black')) delete out[k];
                        } else if (lk.includes('cyan')) {
                            if (!present.has('cyan')) delete out[k];
                        } else if (lk.includes('magenta')) {
                            if (!present.has('magenta')) delete out[k];
                        } else if (lk.includes('yellow')) {
                            if (!present.has('yellow')) delete out[k];
                        } else {
                            // keep non-color keys only if explicitly present
                            if (!present.has(k) && !present.has(normalize(k))) delete out[k];
                        }
                    }
                } else {
                    // No explicit consumables list. Detect if device is mono-only.
                    // Check multiple sources for mono/color indicators:
                    const raw = p.raw_data || p.metrics || p.latest || p.latest_metrics || {};
                    const isMono = p.is_mono || raw.is_mono || 
                        (p.device_type && /mono/i.test(p.device_type)) ||
                        (raw.device_type && /mono/i.test(raw.device_type));
                    const isColor = p.is_color || raw.is_color ||
                        (p.device_type && /color/i.test(p.device_type)) ||
                        (raw.device_type && /color/i.test(raw.device_type));
                    const hasColorUsage = !!(p.color_impressions || p.color_pages || 
                        (p.meters && (p.meters.color_pages || p.meters.cyan)) ||
                        raw.color_impressions || raw.color_pages);
                    
                    // Device is mono if explicitly flagged as mono, or if not flagged as color
                    // and has no color usage history
                    const hasColorSupplies = Object.keys(out).some(name => /cyan|magenta|yellow/i.test(name));
                    const deviceIsMono = isMono || (!isColor && !hasColorUsage && !hasColorSupplies);
                    
                    if (deviceIsMono) {
                        // Drop CMY entries for mono devices - they don't have color supplies
                        for (const k of Object.keys(out)) {
                            const lk = normalize(k);
                            if (lk.includes('cyan') || lk.includes('magenta') || lk.includes('yellow')) {
                                delete out[k];
                            }
                        }
                    }
                }
            }
        } catch (e) {
            // ignore
        }
        return normalizeSupplyLevels(resolveSupplyNames(out, p, latest));
    }

        // Render a compact consumable representation suitable for display on a saved-device card
        // Uses smaller bars and abbreviated labels to keep cards dense.
        function renderMiniConsumable(name, value, isLevel) {
            const nameShort = escapeHtmlCards(getSupplyDisplayName(name));
            if (isLevel) {
                const v = Number(value);
                const pct = isNaN(v) ? 0 : Math.max(0, Math.min(100, v));
                const color = getConsumableColor(name, pct);

                return '<div class="mini-consumable">'
                    + '<div class="mini-consumable-label">' + nameShort + '</div>'
                    + '<div class="mini-consumable-bar"><div style="width:' + pct + '%;background:' + color + ';height:100%"></div></div>'
                    + '<div class="mini-consumable-pct">' + pct + '%</div>'
                    + '</div>';
            }
            // fallback textual mini entry
            return '<div class="mini-consumable">'
                + '<div class="mini-consumable-label">' + nameShort + '</div>'
                + '<div class="mini-consumable-pct">' + escapeHtmlCards(value) + '</div>'
                + '</div>';
        }

        function renderMiniConsumablesSection(tonerLevels) {
            if (!tonerLevels || Object.keys(tonerLevels).length === 0) return '';
            // Render mini consumables stacked vertically to prevent overflow
            let out = '<div class="mini-consumables">';
            for (const [k, v] of Object.entries(tonerLevels)) {
                const isNumeric = (v !== null && v !== undefined) && (typeof v === 'number' || (!isNaN(Number(v)) && String(v).trim() !== ''));
                out += renderMiniConsumable(k, v, !!isNumeric);
            }
            out += '</div>';
            return out;
        }

    async function checkDatabaseRotationWarning() {
        try {
            const res = await fetch('/database/rotation_warning');
            if (!res.ok) return;
            const data = await res.json();
            if (data && data.rotated) {
                const message = `The database was rotated due to a migration failure on ${data.rotated_at || 'recently'}.\n\nA fresh database has been created and the old database has been backed up to:\n${data.backup_path || 'unknown location'}\n\nAll discovered devices and historical metrics data from the previous database are not available in the current session. If you need to recover data, you can manually restore the backup file.\n\nClick OK to acknowledge this warning.`;
                const confirmed = await window.__pm_shared.showConfirm(message, 'Database Rotation Notice');
                if (confirmed) {
                    await fetch('/database/rotation_warning', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ ack: true }) });
                }
            }
        } catch (err) {
            window.__pm_shared.error('Failed to check rotation warning:', err);
        }
    }

    // Export to shared namespace
    window.__pm_shared_cards = window.__pm_shared_cards || {};
    window.__pm_shared_cards.renderSavedCard = renderSavedCard;
    window.__pm_shared_cards.buildTonerLevels = buildTonerLevels;
    window.__pm_shared_cards.getSupplyDisplayName = getSupplyDisplayName;
    window.__pm_shared_cards.getSupplyIdentity = getSupplyIdentity;
    window.__pm_shared_cards.normalizeSupplyLevels = normalizeSupplyLevels;
    window.__pm_shared_cards.getPageCount = getPageCount;
    window.__pm_shared_cards.checkDatabaseRotationWarning = checkDatabaseRotationWarning;
    window.__pm_shared_cards.renderCapabilities = renderCapabilities;
    window.__pm_shared_cards.getTonerColor = getTonerColor;
    window.__pm_shared_cards.isInkOrToner = isInkOrToner;
    window.__pm_shared_cards.getDeviceTonerBarData = getDeviceTonerBarData;
    window.__pm_shared_cards.renderTonerBars = renderTonerBars;

    document.addEventListener('keydown', event => {
        if ((event.key === 'Enter' || event.key === ' ') && event.target.matches('.copyable[role="button"], .editable-field[role="button"]')) {
            event.preventDefault();
            event.target.click();
        }
    });

    // Attach delegated click handler for copyable elements that use data-copy.
    // Await shared.ready so shared utilities (copyToClipboard, showToast) are
    // available and logging is consistent.
    document.addEventListener('click', async (ev) => {
        try {
            await (window.__pm_shared && window.__pm_shared.ready);
        } catch (e) {
            // If ready promise rejected or missing, continue to avoid blocking UI
        }
        try {
            const el = ev.target.closest && ev.target.closest('.copyable');
            if (!el) return;
            const val = el.getAttribute('data-copy');
            if (!val) return;
            try {
                window.__pm_shared.copyToClipboard(val, null, 'Copied to clipboard');
            } catch (err) {
                // fallback
                try { navigator.clipboard.writeText(val); window.__pm_shared.showToast && window.__pm_shared.showToast('Copied to clipboard', 'success'); } catch (e) {}
            }
        } catch (e) {
            try { window.__pm_shared && window.__pm_shared.debug && window.__pm_shared.debug('copyable handler error', e); } catch (er) {}
        }
    });

    // Delegated handler for action buttons in rendered cards. We await
    // the shared.ready promise to guarantee the shared API and logger are
    // available before calling into window.__pm_shared.
    document.addEventListener('click', async (ev) => {
        try {
            await (window.__pm_shared && window.__pm_shared.ready);
        } catch (e) {}
        try {
                // Support both data-action attributes and legacy .save-device-btn class
                let btn = ev.target.closest && (ev.target.closest('[data-action]') || ev.target.closest('.save-device-btn'));
                if (!btn) return;
                // Determine action: prefer explicit data-action, fall back to class-based legacy actions
                const action = btn.getAttribute('data-action') || (btn.classList && btn.classList.contains('save-device-btn') ? 'save' : null);
            try { window.__pm_shared && window.__pm_shared.trace && window.__pm_shared.trace('card action', action, btn.dataset); } catch (er) {}

            if (action === 'save' || btn.classList.contains('save-device-btn')) {
                // Old markup migration: support both data-action="save" and .save-device-btn
                const ip = btn.getAttribute('data-ip') || btn.getAttribute('data-device-ip');
                if (!ip) return;
                // Find the card element and provide a saving visual state
                const card = btn.closest && btn.closest('.device-card');
                try { btn.disabled = true; } catch (e) {}
                try {
                    if (card) {
                        // Indicate that the card is being saved (green flash)
                        card.classList.add('saving');
                        // ensure the element participates in smooth reflow
                        card.style.willChange = 'transform, max-height, margin';
                    }
                } catch (e) {}

                try {
                    // Prevent the agent-side save helper from immediately calling
                    // updatePrinters (which would re-render and remove the card
                    // before our exit animation completes). Request the save but
                    // ask it NOT to update the UI; we'll call updatePrinters after
                    // the animation finishes.
                    await window.__pm_shared.saveDiscoveredDevice(ip, false, false);

                    // On success, show saved state then animate removal so other
                    // cards slide into place smoothly (removing uses slideOut keyframes)
                    try {
                        if (card) {
                            card.classList.remove('saving');
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
                            setTimeout(onEnd, 700);
                        } else {
                            try { if (typeof updatePrinters === 'function') updatePrinters(); } catch (e) {}
                        }
                    } catch (e) {
                        try { if (typeof updatePrinters === 'function') updatePrinters(); } catch (er) {}
                    }

                    btn.textContent = 'Saved ✓';
                } catch (e) {
                    window.__pm_shared.error('Save failed:', e);
                    try { btn.disabled = false; } catch (er) {}
                    try { btn.textContent = 'Save'; } catch (er) {}
                    window.__pm_shared.showToast('Save failed: ' + (e && e.message ? e.message : e), 'error');
                }
                return;
            }

            if (action === 'webui') {
                const url = btn.getAttribute('data-webui-url');
                const serial = btn.getAttribute('data-serial');
                window.__pm_shared.showWebUIModal(url, serial);
                return;
            }

            if (action === 'open-direct') {
                const url = btn.getAttribute('data-webui-url');
                window.open(url, '_blank');
                return;
            }

            if (action === 'open-proxy') {
                const serial = btn.getAttribute('data-serial');
                window.__pm_shared.openDeviceUI(serial);
                return;
            }

            if (action === 'metrics') {
                const serial = btn.getAttribute('data-serial');
                const preset = btn.getAttribute('data-preset');
                window.__pm_shared.showDeviceMetricsModal(serial, preset);
                return;
            }

            // Server-specific actions (agents/devices list)
            if (action === 'view-agent') {
                const agentId = btn.getAttribute('data-agent-id');
                window.__pm_shared.viewAgentDetails(agentId);
                return;
            }

            if (action === 'agent-settings') {
                const agentId = btn.getAttribute('data-agent-id');
                if (window.__pm_shared.openFleetSettingsForAgent) {
                    window.__pm_shared.openFleetSettingsForAgent(agentId);
                }
                return;
            }

            if (action === 'open-agent') {
                const agentId = btn.getAttribute('data-agent-id');
                window.__pm_shared.openAgentUI(agentId);
                return;
            }

            if (action === 'delete-agent') {
                const agentId = btn.getAttribute('data-agent-id');
                const agentName = btn.getAttribute('data-agent-name');
                window.__pm_shared.deleteAgent(agentId, agentName);
                return;
            }

            if (action === 'update-agent') {
                const agentId = btn.getAttribute('data-agent-id');
                window.__pm_shared.updateAgent(agentId);
                return;
            }

            if (action === 'cancel-update') {
                const agentId = btn.getAttribute('data-agent-id');
                if (window.__pm_shared.cancelAgentUpdate) {
                    window.__pm_shared.cancelAgentUpdate(agentId);
                }
                return;
            }

            if (action === 'open-device') {
                const serial = btn.getAttribute('data-serial');
                window.__pm_shared.openDeviceUI(serial);
                return;
            }

            if (action === 'view-metrics') {
                const serial = btn.getAttribute('data-serial');
                window.__pm_shared.openDeviceMetrics(serial);
                return;
            }

            if (action === 'show-printer-details') {
                // Prefer serial lookup when available, otherwise fall back to IP.
                const serial = btn.getAttribute('data-serial');
                const ip = btn.getAttribute('data-ip');
                const lookup = serial || ip;
                const source = btn.getAttribute('data-source') || 'discovered';
                window.__pm_shared.showPrinterDetails(lookup, source);
                return;
            }

            if (action === 'details') {
                // Prefer serial if provided on the button; many saved cards include data-serial.
                const serial = btn.getAttribute('data-serial');
                const ip = btn.getAttribute('data-ip');
                const lookup = serial || ip;
                const source = btn.getAttribute('data-source') || 'discovered';
                window.__pm_shared.showPrinterDetails(lookup, source);
                return;
            }

            if (action === 'delete') {
                const serial = btn.getAttribute('data-serial');
                window.__pm_shared.deleteSavedDevice(serial);
                return;
            }

            if (action === 'edit') {
                const serial = btn.getAttribute('data-serial');
                const field = btn.getAttribute('data-field');
                const current = btn.getAttribute('data-current');
                window.__pm_shared.editField(serial, field, current, btn);
                return;
            }
        } catch (e) {
            window.__pm_shared.error('card action handler error', e);
        }
    });

    // Shared modal renderer for printer details (moved from agent/web/app.js)
    // This builds the saved/discovered device modal and also provides a
    // small metrics-summary loader used by the modal. Consumers should
    // call `window.__pm_shared_cards.showPrinterDetailsData(p, source, parseDebug)`.
    let detailsGeneration = 0;
    function showPrinterDetailsData(p, source, parseDebug) {
        if (!p) return;
        source = source || 'discovered';
        const bodyEl = document.getElementById('printer_details_body');
        const overlay = document.getElementById('printer_details_overlay');
        const titleEl = document.getElementById('printer_details_title');
        const actionsEl = document.getElementById('printer_details_actions');
        const generation = ++detailsGeneration;
        // Each opening owns its asynchronous rendering, including reopening the same serial.
        const isCurrent = () => generation === detailsGeneration && overlay.style.display !== 'none';

        // Build a descriptive title with device name
        try {
            const mfg = p.manufacturer || p.make || '';
            const model = p.model || '';
            const deviceName = (mfg + ' ' + model).trim() || 'Unknown Device';
            const sourceLabel = (source === 'saved') ? '' : ' (Discovered)';
            titleEl.textContent = deviceName + sourceLabel;
        } catch (e) {
            titleEl.textContent = (source === 'saved') ? 'Device Details' : 'Discovered Device';
        }

        // Store the current printer IP to prevent glitchy updates from live data
        try { overlay.dataset.currentPrinterIp = p.ip || p.IP || ''; } catch (e) {}

        // Show modal overlay and prevent body scroll
        try { overlay.style.display = 'flex'; document.body.style.overflow = 'hidden'; } catch (e) {}

        // Helper renderers
        function renderInfoCard(title, content, opts = {}) {
            if (!content) return '';
            const extraClass = opts.className ? ' ' + opts.className : '';
            return '<section class="device-details-card' + extraClass + '">' +
                '<div class="device-details-card-title">' + title + '</div>' +
                '<div class="device-details-card-content">' + content + '</div>' +
                '</section>';
        }

        function renderMetricStat(label, primary, secondary) {
            return '<div class="device-metric-pill">' +
                '<div class="device-metric-label">' + label + '</div>' +
                '<div class="device-metric-value">' + primary + '</div>' +
                (secondary ? '<div class="device-metric-subtext">' + secondary + '</div>' : '') +
                '</div>';
        }

        function renderRow(label, value) {
            if (!value && value !== 0) return '';
            return '<div class="device-detail-field">' +
                '<div class="device-detail-label">' + label + '</div>' +
                '<div class="device-detail-value">' + value + '</div>' +
                '</div>';
        }

        // Editable row helper - shows value as text with an edit button
        function renderEditableRow(label, field, value, opts = { type: 'text', readonly: false, placeholder: '' }) {
            const safeVal = (value === undefined || value === null) ? '' : String(value);
            const displayVal = safeVal ? escapeHtmlCards(safeVal) : '<span class="device-detail-empty">Not set</span>';
            const serial = p.serial || p.Serial || '';

            let row = '<div class="device-detail-field" data-field-row="' + field + '">';
            row += '<div class="device-detail-label">' + label + '</div>';
            row += '<div class="device-detail-value-group"><div class="device-detail-value" id="field_' + field + '_display">' + displayVal + '</div>';
            if (!opts.readonly) {
                row += '<button class="edit-field-btn" data-field="' + field + '" data-serial="' + escapeHtmlCards(serial) + '" data-current="' + escapeHtmlCards(safeVal) + '" data-label="' + label + '" title="Edit ' + label + '" aria-label="Edit ' + label + '"><svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="M16 3l5 5L8 21H3v-5L16 3zM14 5l5 5"/></svg></button>';
            }
            row += '</div>' + (opts.actions || '') + '</div>';
            return row;
        }

        // Build HTML (kept intentionally similar to agent implementation)
        let html = '<div class="device-details-stack">';

        if (!p || Object.keys(p).length === 0) {
            html += '<div style="color:var(--muted);padding:12px">No printer data available</div>';
            bodyEl.innerHTML = html + '</div>';
            return;
        }

        // Capabilities (use shared renderer)
        const capabilitiesHTML = window.__pm_shared_cards.renderCapabilities(p);
        const capabilitiesEl = document.getElementById('printer_details_capabilities');
        if (capabilitiesEl) capabilitiesEl.innerHTML = capabilitiesHTML;
        else if (capabilitiesHTML) html += renderInfoCard('Capabilities', capabilitiesHTML);

        // Device Info (editable rows with lock buttons)
        let deviceInfo = '<div class="device-info-fields">';
        deviceInfo += renderEditableRow('Manufacturer', 'manufacturer', p.manufacturer);
        deviceInfo += renderEditableRow('Model', 'model', p.model);
        deviceInfo += renderEditableRow('Serial', 'serial', p.serial, { type: 'text', readonly: true });
        deviceInfo += renderEditableRow('Firmware', 'firmware', p.firmware);
        deviceInfo += renderEditableRow('Asset Number', 'asset_number', p.asset_number);
        deviceInfo += renderEditableRow('Location', 'location', p.location);
        deviceInfo += renderEditableRow('Description', 'description', p.description, { type: 'textarea' });
        // Web UI with proxy buttons and edit
        const webUIVal = p.web_ui_url || p.webui || '';
        const serial = p.serial || p.Serial || '';
        const webUiActions = webUIVal ? '<div class="device-webui-actions">' +
                '<button data-action="open-direct" data-webui-url="' + escapeHtmlCards(webUIVal) + '">Direct</button>' +
                '<button class="primary" data-action="open-proxy" data-serial="' + escapeHtmlCards(serial) + '">Proxy</button>' +
                '</div>' : '';
        deviceInfo += renderEditableRow('Web UI', 'web_ui_url', webUIVal, { actions: webUiActions });
        if (p.last_seen) deviceInfo += renderRow('Last Seen', new Date(p.last_seen).toLocaleString());
        if (p.first_seen) deviceInfo += renderRow('First Seen', new Date(p.first_seen).toLocaleString());
        deviceInfo += '</div>';
        html += renderInfoCard('Device Info', deviceInfo, { className: 'device-identity-card' });

        // Metrics card for saved devices (compact summary + quick-open buttons)
        if (source === 'saved' && p.serial) {
            const metricsHtml = '<div class="device-metrics-card-content">' +
                '<div id="printer_metrics_summary" class="device-metrics-summary-placeholder">Loading metrics summary...</div>' +
                '<div class="device-metrics-actions">' +
                '<button class="primary" data-action="metrics" data-serial="' + p.serial + '">Open Metrics</button>' +
                '<button style="font-size:12px;padding:6px 10px" data-action="metrics" data-serial="' + p.serial + '" data-preset="7day">Last 7 Days</button>' +
                '</div>' +
                '<div class="device-metrics-note">Full charts live in the Metrics tab.</div>' +
                '</div>';
            html += renderInfoCard('Metrics Overview', metricsHtml, { className: 'device-metrics-card' });
        }

    // Network Info (editable)
    let networkInfo = '<div class="device-info-fields">';
    networkInfo += renderEditableRow('IP Address', 'ip', p.ip || p.IP || '');
    networkInfo += renderEditableRow('MAC Address', 'mac_address', p.mac || p.mac_address || '');
    networkInfo += renderEditableRow('Hostname', 'hostname', p.hostname);
    networkInfo += renderEditableRow('Subnet Mask', 'subnet_mask', p.subnet_mask);
    networkInfo += '</div>';
    html += renderInfoCard('Network', networkInfo, { className: 'device-network-card' });

        // Web UI Credentials Card (for proxy auto-login)
        // Determine default username based on manufacturer
        const mfg = (p.manufacturer || '').toLowerCase();
        let defaultUser = 'admin';
        let passwordHint = 'Serial Number';
        if (mfg.includes('kyocera')) {
            defaultUser = 'Admin'; // Capital A for Kyocera
        } else if (mfg.includes('epson')) {
            defaultUser = 'EPSON';
            passwordHint = 'Serial Number (default)';
        } else if (mfg.includes('hp')) {
            passwordHint = 'Blank (default) or set password';
        }

        // Render credentials card (server will proxy to agent)
        let credsInfo = '<div style="color:var(--muted);font-size:12px;margin-bottom:8px">For automatic proxy login. Password is encrypted at rest.</div>';
        credsInfo += '<div style="display:grid;gap:6px">';
        credsInfo += '<div style="display:grid;grid-template-columns:auto 1fr;gap:4px 8px;align-items:center">';
        credsInfo += '<div style="color:var(--muted)">Username:</div>';
        credsInfo += '<input id="cred_username" type="text" placeholder="' + defaultUser + '" style="width:100%">';
        credsInfo += '</div>';
        credsInfo += '<div style="display:grid;grid-template-columns:auto 1fr;gap:4px 8px;align-items:center">';
        credsInfo += '<div style="color:var(--muted)">Password:</div>';
        credsInfo += '<input id="cred_password" type="password" placeholder="' + passwordHint + '" style="width:100%">';
        credsInfo += '</div>';
        credsInfo += '<div style="display:grid;grid-template-columns:auto 1fr;gap:4px 8px;align-items:center">';
        credsInfo += '<div style="color:var(--muted)">Auth Type:</div>';
        credsInfo += '<select id="cred_auth_type" style="width:100%"><option value="basic">HTTP Basic</option><option value="form">Form Login</option></select>';
        credsInfo += '</div>';
        credsInfo += '<div style="display:flex;align-items:center;gap:8px;margin-top:4px">';
        credsInfo += '<input type="checkbox" id="cred_auto_login">';
        credsInfo += '<label for="cred_auto_login" style="color:var(--text);cursor:pointer">Enable auto-login when using proxy</label>';
        credsInfo += '</div>';
        credsInfo += '<div style="display:flex;gap:8px;margin-top:4px">';
        credsInfo += '<button id="save_creds_btn" style="flex:1">Save Credentials</button>';
        credsInfo += '<span id="creds_status" style="color:var(--muted);align-self:center;font-size:12px"></span>';
        credsInfo += '</div>';
        credsInfo += '</div>';

        // Consumables (render as its own card). If we don't have explicit
        // consumable information yet, include a placeholder `printer_consumables_card`
        // which can later be populated from the latest metrics snapshot.
        const tonerLevels = buildTonerLevels(p);
        if (Object.keys(tonerLevels).length > 0) {
            // Render the consumables card with an explicit id so we can
            // detect it later and avoid duplicate insertions.
            html += '<div id="printer_consumables_card_actual">' + renderInfoCard('Consumables', renderConsumablesSection(tonerLevels)) + '</div>';
        } else {
            html += '<div id="printer_consumables_card"></div>';
        }
        html += renderInfoCard('Web UI Credentials (optional)', credsInfo);

        // Interfaces: attempt to extract from parseDebug.raw_pdus (IF-MIB columns)
        let interfacesSection = '';
        try {
            if (parseDebug && Array.isArray(parseDebug.raw_pdus)) {
                const ifs = {};
                parseDebug.raw_pdus.forEach(rp => {
                    const oid = rp.oid || rp.OID || '';
                    if (oid.startsWith('1.3.6.1.2.1.2.2.1.')) {
                        const parts = oid.split('.');
                        const col = parts[parts.length - 2];
                        const idx = parts[parts.length - 1];
                        if (!ifs[idx]) ifs[idx] = { index: idx };
                        const v = rp.str_value || rp.StrValue || '';
                        switch (col) {
                            case '2': ifs[idx].descr = v; break;
                            case '3': ifs[idx].type = v; break;
                            case '5': ifs[idx].speed = v; break;
                            case '6': ifs[idx].mac = v; break;
                        }
                    }
                });
                const keys = Object.keys(ifs);
                if (keys.length > 0) {
                    interfacesSection += '<details class="device-details-expando">';
                    interfacesSection += '<summary>Interface Details (' + keys.length + ')</summary>';
                    keys.forEach(k => {
                        const it = ifs[k];
                        interfacesSection += '<div class="device-details-expando-row">';
                        interfacesSection += '<div class="device-details-grid">';
                        if (it.descr) interfacesSection += '<div>Interface</div><div>' + it.descr + '</div>';
                        if (it.mac) interfacesSection += '<div>MAC</div><div style="font-family:monospace;font-size:12px">' + it.mac + '</div>';
                        if (it.speed) interfacesSection += '<div>Speed</div><div>' + it.speed + '</div>';
                        interfacesSection += '</div></div>';
                    });
                    interfacesSection += '</details>';
                }
            }
        } catch (e) { /* non-critical */ }
        if (interfacesSection) html += renderInfoCard('Network Interfaces', interfacesSection);

        // Paper trays - collapsible
        let paperSection = '';
        try {
            if (p.paper_tray_status && p.paper_tray_status.length) {
                paperSection += '<details class="device-details-expando" open>';
                paperSection += '<summary>Loaded Trays</summary>';
                paperSection += '<div class="device-details-list">';
                p.paper_tray_status.forEach(t => paperSection += '<div class="device-details-list-item">' + t + '</div>');
                paperSection += '</div></details>';
            }
        } catch (e) { /* ignore */ }
        if (paperSection) html += renderInfoCard('Paper Trays', paperSection);

        // Status messages and alerts - collapsible
        let statusSection = '';
        try {
            if (p.status_messages && p.status_messages.length) {
                statusSection += '<details class="device-details-expando" open>';
                statusSection += '<summary>Active Alerts</summary>';
                statusSection += '<div class="device-details-list">';
                p.status_messages.forEach(s => statusSection += '<div class="device-details-list-item">' + s + '</div>');
                statusSection += '</div></details>';
            }
        } catch (e) { /* ignore */ }
        if (statusSection) html += renderInfoCard('Status Messages', statusSection);

        // Device Data Report section (replaces old parse debug link)
        let reportSection = '';
        try {
            if (window.__pm_report && typeof window.__pm_report.renderReportForm === 'function') {
                reportSection = window.__pm_report.renderReportForm(p, parseDebug);
            } else {
                // Fallback: simple button that triggers report UI when JS loads
                reportSection = '<div id="device_report_placeholder" class="device-report-container">' +
                    '<p style="color:var(--muted);font-size:13px;">Loading report form...</p>' +
                    '</div>';
            }
        } catch (e) {
            console.warn('Report form render error:', e);
        }
        if (reportSection) html += renderInfoCard('Report Data Issue', reportSection, { className: 'device-report-card' });

        const liveTools = '<div id="action_buttons_area" class="device-live-tools">' +
            '<div class="device-live-actions-row">' +
            '<button id="refresh_data_btn">Scan for Updates</button>' +
            (source === 'saved' ? '<button id="collect_metrics_btn">Collect Metrics</button>' : '') +
            '<span id="refresh_status" class="device-live-status"></span>' +
            '</div>' +
            '<div style="color:var(--muted);font-size:11px;margin-top:4px">Scans the device via SNMP to check for updated values. Use edit buttons (✏️) to manually correct any field.</div>' +
            (source === 'saved' && (p.type === 'usb' || p.connection_type === 'usb') ? '<div style="color:var(--warn);font-size:11px;margin-top:4px">⚠️ USB metrics collection can take 30+ seconds</div>' : '') +
            '<div id="diff_container"></div>' +
            '</div>';
        html += renderInfoCard('Data Tools', liveTools, { className: 'device-live-card' });

        html += '</div>';

    bodyEl.innerHTML = html;

        // Populate compact metrics (if any)
        (async function populatePrinterMetricsSummaryLocal() {
            try {
                if (source !== 'saved' || !p || !p.serial) return;
                const summaryEl = document.getElementById('printer_metrics_summary');
                if (!summaryEl) return;
                summaryEl.innerHTML = '<div class="device-metrics-placeholder">Loading metrics summary...</div>';

                const url = '/api/devices/metrics/history?serial=' + encodeURIComponent(p.serial) + '&period=7day';
                const res = await fetch(url);
                if (!isCurrent()) return;
                if (!res.ok) throw new Error('Metrics request failed (HTTP ' + res.status + ')');
                const history = await res.json();
                if (!isCurrent()) return;
                if (!history || history.length === 0) { summaryEl.innerHTML = '<div class="device-metrics-placeholder">No metrics data available</div>'; return; }

                const latest = history[history.length - 1];
                const oldest = history[0];
                const durationMs = new Date(latest.timestamp).getTime() - new Date(oldest.timestamp).getTime();
                const durationDays = Math.max(1, durationMs / (24 * 60 * 60 * 1000));

                const lifetimePages = getPageCount(latest);
                const oldestPages = getPageCount(oldest);
                const periodPages = lifetimePages === null || oldestPages === null ? null : lifetimePages - oldestPages;
                const avgPages = periodPages === null ? 'Not collected' : (periodPages / durationDays).toFixed(1);
                let statsHtml = '<div class="device-metrics-summary-grid">';
                statsHtml += renderMetricStat('Lifetime Pages', lifetimePages === null ? 'Not collected' : lifetimePages.toLocaleString(), 'All-time total');
                statsHtml += renderMetricStat('7-day Delta', periodPages === null ? 'Not collected' : periodPages.toLocaleString(), 'vs earliest sample');
                statsHtml += renderMetricStat('Avg / Day', avgPages, Math.max(1, Math.round(durationDays)) + ' day window');
                if (latest.mono_pages !== undefined || latest.mono_impressions !== undefined) {
                    const lifetimeMono = latest.mono_pages || latest.mono_impressions || 0;
                    const periodMono = lifetimeMono - (oldest.mono_pages || oldest.mono_impressions || 0);
                    const monoSummary = (periodMono.toLocaleString ? periodMono.toLocaleString() : periodMono) + ' this period';
                    statsHtml += renderMetricStat('Mono Pages', (lifetimeMono.toLocaleString ? lifetimeMono.toLocaleString() : lifetimeMono), monoSummary);
                }
                statsHtml += '</div>';
                statsHtml += '<div class="device-metrics-footnote">Last sample ' + new Date(latest.timestamp).toLocaleString() + '</div>';

                const summaryElFinal = document.getElementById('printer_metrics_summary');
                if (summaryElFinal) summaryElFinal.innerHTML = statsHtml;
                // If the device object didn't include toner_levels (schema v7+),
                // populate consumables UI from the latest metrics snapshot so
                // the modal still shows toner bars for saved devices.
                    try {
                        // If the main render didn't include consumables, populate the
                        // dedicated consumables card placeholder with data from the
                        // latest metrics snapshot so consumables and metrics are
                        // separate cards and don't duplicate bars.
                        const injected = buildTonerLevels(p, latest);
                        if (injected && Object.keys(injected).length > 0) {
                            const sectionHtml = renderConsumablesSection(injected);
                            // If a real consumables card was already rendered, don't add another
                            const existingActual = document.getElementById('printer_consumables_card_actual');
                            if (existingActual) {
                                // Optionally update existing card content
                                try { existingActual.innerHTML = renderInfoCard('Consumables', sectionHtml); } catch (e) {}
                            } else {
                                const consumablesPlaceholder = document.getElementById('printer_consumables_card');
                                if (consumablesPlaceholder) {
                                    consumablesPlaceholder.innerHTML = renderInfoCard('Consumables', sectionHtml);
                                    // mark as actual so future attempts don't duplicate
                                    consumablesPlaceholder.id = 'printer_consumables_card_actual';
                                } else {
                                    // Fallback (old behavior) if placeholder missing
                                    const metricsSummaryEl = document.getElementById('printer_metrics_summary');
                                    if (metricsSummaryEl && metricsSummaryEl.parentElement) {
                                        // Avoid duplicating: ensure there's no existing actual card first
                                        const already = document.getElementById('printer_consumables_card_actual');
                                        if (!already) metricsSummaryEl.insertAdjacentHTML('afterend', renderInfoCard('Consumables', sectionHtml));
                                    } else {
                                        const be = document.getElementById('printer_details_body');
                                        if (be) {
                                            const already = document.getElementById('printer_consumables_card_actual');
                                            if (!already) be.insertAdjacentHTML('afterbegin', renderInfoCard('Consumables', sectionHtml));
                                        }
                                    }
                                }
                            }
                        }
                    } catch (e) { window.__pm_shared.warn('Consumables summary rendering failed', e); }
            } catch (err) {
                window.__pm_shared.error('Device metrics summary failed', { serial: p.serial, error: err });
                if (!isCurrent()) return;
                const summaryEl = document.getElementById('printer_metrics_summary');
                if (summaryEl) {
                    summaryEl.innerHTML = '<div class="device-metrics-placeholder" role="status">Metrics unavailable. <button type="button">Retry</button></div>';
                    summaryEl.querySelector('button').onclick = populatePrinterMetricsSummaryLocal;
                }
            }
        })();

        // Small post-render layout hook (if consumer provides applyMasonryLayout)
        setTimeout(() => {
            const modalGrid = bodyEl && bodyEl.querySelector ? bodyEl.querySelector('.device-details-stack') : null;
            if (modalGrid && typeof applyMasonryLayout === 'function') {
                try { applyMasonryLayout(modalGrid); } catch (e) {}
            }
        }, 50);

        // Wire up edit field buttons
        try {
            bodyEl.onclick = async (e) => {
                const btn = e.target.closest && e.target.closest('.edit-field-btn');
                if (!btn) return;
                const field = btn.getAttribute('data-field');
                const serial = btn.getAttribute('data-serial');
                const current = btn.getAttribute('data-current') || '';
                const label = btn.getAttribute('data-label') || field.replace(/_/g, ' ');

                if (!serial || !field) return;

                try {
                    const newValue = await window.__pm_shared.showPrompt('Edit ' + label + ':', current, 'Edit Field');
                    if (newValue === null) return; // User cancelled
                    if (!isCurrent()) return;

                    const r = await fetch('/devices/update', {
                        method: 'POST',
                        headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify({ serial, [field]: newValue })
                    });
                    if (!r.ok) {
                        const t = await r.text();
                        window.__pm_shared.showToast('Update failed: ' + t, 'error');
                        return;
                    }
                    window.__pm_shared.showToast('Field updated', 'success');
                    if (!isCurrent()) return;

                    // Update the display value in the UI
                    const displayEl = document.getElementById('field_' + field + '_display');
                    if (displayEl) {
                        displayEl.innerHTML = newValue ? escapeHtmlCards(newValue) : '<span class="device-detail-empty">Not set</span>';
                    }
                    // Update the data-current attribute for future edits
                    btn.setAttribute('data-current', newValue);
                    // Also update the device object in memory
                    p[field] = newValue;
                } catch (err) {
                    window.__pm_shared.showToast('Failed to update field', 'error');
                    window.__pm_shared.error('Edit field error', err);
                }
            };
        } catch (e) { window.__pm_shared.warn('edit field wiring failed', e); }

        // Wire up scan for updates button
        try {
            document.getElementById('refresh_data_btn')?.addEventListener('click', async function () {
                const statusEl = document.getElementById('refresh_status');
                const btn = document.getElementById('refresh_data_btn');
                if (!btn) return;
                btn.disabled = true;
                if (statusEl) statusEl.textContent = ' Scanning device...';
                try {
                    const body = { serial: p.serial || '', ip: p.ip };
                    const r = await fetch('/devices/preview', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
                    if (!r.ok) throw new Error('Device scan failed (HTTP ' + r.status + ')');
                    const { proposed } = await r.json();
                    if (!isCurrent()) return;
                    if (statusEl) statusEl.textContent = '';
                    const fields = ['manufacturer', 'model', 'hostname', 'firmware', 'ip', 'subnet_mask', 'gateway', 'dns_servers', 'dhcp_server', 'asset_number', 'location', 'description', 'web_ui_url'];
                    
                    // Clear any existing diff UI
                    const diffContainer = document.getElementById('diff_container');
                    if (diffContainer) diffContainer.innerHTML = '';
                    
                    // Build list of differences
                    let diffs = [];
                    fields.forEach(f => {
                        let currentVal = (f === 'dns_servers') ? (p.dns_servers || []).join(', ') : (p[f] || '');
                        let proposedVal = (f === 'dns_servers') ? (proposed[f] || []).join(', ') : (proposed[f] || '');
                        if (String(currentVal) !== String(proposedVal) && proposedVal !== '') {
                            diffs.push({ field: f, current: currentVal, proposed: proposedVal });
                        }
                    });

                    if (diffs.length === 0) {
                        if (diffContainer) {
                            diffContainer.innerHTML = '<div style="color:var(--muted);padding:8px;font-size:13px;text-align:center">✓ Device data is up to date</div>';
                        }
                        return;
                    }
                    
                    let diffHtml = '<div id="diff_card" style="background:rgba(255,255,255,0.03);border:1px solid rgba(255,255,255,0.08);border-radius:6px;padding:10px;margin-top:8px">';
                    diffHtml += '<div style="font-weight:600;color:var(--highlight);margin-bottom:6px;font-size:14px">Scanned Updates (' + diffs.length + ')</div>';
                    diffHtml += '<div style="display:flex;flex-direction:column;gap:8px">';
                    diffs.forEach(d => {
                        const label = d.field.replace(/_/g, ' ').replace(/\b\w/g, l => l.toUpperCase());
                        diffHtml += '<div style="display:grid;grid-template-columns:1fr auto;gap:8px;align-items:center;padding:6px;background:rgba(0,0,0,0.1);border-radius:4px">';
                        diffHtml += '<div><div style="color:var(--muted);font-size:11px;text-transform:uppercase">' + label + '</div>';
                        diffHtml += '<div style="font-size:12px"><span style="color:var(--muted);text-decoration:line-through">' + (d.current ? escapeHtmlCards(d.current) : '<i>empty</i>') + '</span> → <span style="color:var(--highlight)">' + escapeHtmlCards(d.proposed) + '</span></div></div>';
                        diffHtml += '<button class="apply-single-btn" data-field="' + d.field + '" data-value="' + String(d.proposed).replace(/"/g, '&quot;') + '" style="font-size:11px;padding:4px 8px">Apply</button>';
                        diffHtml += '</div>';
                    });
                    diffHtml += '</div>';
                    if (diffs.length > 1) {
                        diffHtml += '<div style="margin-top:8px;text-align:right"><button id="apply_all_btn" class="primary">Apply All (' + diffs.length + ')</button></div>';
                    }
                    diffHtml += '</div>';
                    
                    // Insert into the diff_container
                    if (diffContainer) {
                        diffContainer.innerHTML = diffHtml;
                        
                        // Wire up individual apply buttons
                        diffContainer.querySelectorAll('.apply-single-btn').forEach(applyBtn => {
                            applyBtn.addEventListener('click', async () => {
                                const field = applyBtn.getAttribute('data-field');
                                const value = applyBtn.getAttribute('data-value');
                                applyBtn.disabled = true;
                                applyBtn.textContent = '...';
                                try {
                                    const ur = await fetch('/devices/update', {
                                        method: 'POST',
                                        headers: { 'Content-Type': 'application/json' },
                                        body: JSON.stringify({ serial: p.serial, [field]: value })
                                    });
                                    if (!isCurrent()) return;
                                    if (!ur.ok) {
                                        const t = await ur.text();
                                        window.__pm_shared.showToast('Update failed: ' + t, 'error');
                                        applyBtn.disabled = false;
                                        applyBtn.textContent = 'Apply';
                                        return;
                                    }
                                    applyBtn.textContent = '✓';
                                    applyBtn.style.background = 'var(--success)';
                                    applyBtn.style.color = '#fff';
                                    // Update display
                                    const displayEl = document.getElementById('field_' + field + '_display');
                                    if (displayEl) {
                                        displayEl.innerHTML = value ? escapeHtmlCards(value) : '<span style="color:var(--muted);font-style:italic">Not set</span>';
                                    }
                                    p[field] = value;
                                    window.__pm_shared.showToast(field.replace(/_/g, ' ') + ' updated', 'success');
                                } catch (err) {
                                    window.__pm_shared.error('Scanned field update failed', { serial: p.serial, field, error: err });
                                    window.__pm_shared.showToast('Update failed', 'error');
                                    applyBtn.disabled = false;
                                    applyBtn.textContent = 'Apply';
                                }
                            });
                        });

                        // Wire up apply all button
                        document.getElementById('apply_all_btn')?.addEventListener('click', async () => {
                            const payload = { serial: p.serial };
                            diffs.forEach(d => { payload[d.field] = d.proposed; });
                            try {
                                const ur = await fetch('/devices/update', {
                                    method: 'POST',
                                    headers: { 'Content-Type': 'application/json' },
                                    body: JSON.stringify(payload)
                                });
                                if (!ur.ok) {
                                    const t = await ur.text();
                                    window.__pm_shared.showToast('Update failed: ' + t, 'error');
                                    return;
                                }
                                window.__pm_shared.showToast('All updates applied', 'success');
                                // Refresh the modal to show updated values
                                await new Promise(res => setTimeout(res, 400));
                                if (!isCurrent()) return;
                                const serialId = p.serial || p.Serial;
                                if (serialId && window.__pm_shared && typeof window.__pm_shared.showPrinterDetails === 'function') {
                                    window.__pm_shared.showPrinterDetails(serialId, source);
                                }
                            } catch (err) {
                                window.__pm_shared.error('Scanned updates failed', { serial: p.serial, error: err });
                                window.__pm_shared.showToast('Update failed', 'error');
                            }
                        });
                    }
                } catch (err) {
                    window.__pm_shared.error('Device scan failed', { serial: p.serial, error: err });
                    if (statusEl) statusEl.textContent = ' Failed: ' + err;
                } finally {
                    btn.disabled = false;
                }
            });
            // DON'T auto-run the scan on modal open - it can timeout and confuse users
            // Let them click "Scan for Updates" if they want to check for changes
        } catch (e) { window.__pm_shared.warn('refresh wiring failed', e); }

        // Wire up metrics collection button (saved devices only)
        try {
            if (source === 'saved') {
                document.getElementById('collect_metrics_btn')?.addEventListener('click', async function () {
                    const statusEl = document.getElementById('refresh_status');
                    const btn = document.getElementById('collect_metrics_btn');
                    if (!btn) return;
                    btn.disabled = true;
                    
                    // Check if this is a USB device
                    const isUSB = (p.type === 'usb' || p.connection_type === 'usb');
                    
                    if (isUSB) {
                        // USB device - use USB metrics endpoint (can take 30+ seconds)
                        if (statusEl) statusEl.innerHTML = ' <span class="spinner"></span> Collecting USB metrics (this may take 30+ seconds)...';
                        try {
                            const r = await fetch('/api/usb-printers/metrics/' + encodeURIComponent(p.serial));
                            if (!r.ok) { const t = await r.text(); if (statusEl) statusEl.textContent = ' Error: ' + t; btn.disabled = false; return; }
                            const usbMetrics = await r.json();
                            
                            // If we got valid metrics, save them to the device store
                            if (usbMetrics && usbMetrics.total_pages > 0) {
                                // Convert USB metrics to storage format and save
                                const saveBody = {
                                    serial: p.serial,
                                    page_count: usbMetrics.total_pages,
                                    color_pages: usbMetrics.color_pages || 0,
                                    mono_pages: usbMetrics.mono_pages || 0,
                                    copy_pages: usbMetrics.copy_pages || 0,
                                    scan_count: usbMetrics.scan_pages || 0,
                                    toner_levels: {}
                                };
                                if (usbMetrics.toner_black) saveBody.toner_levels.black = usbMetrics.toner_black;
                                if (usbMetrics.toner_cyan) saveBody.toner_levels.cyan = usbMetrics.toner_cyan;
                                if (usbMetrics.toner_magenta) saveBody.toner_levels.magenta = usbMetrics.toner_magenta;
                                if (usbMetrics.toner_yellow) saveBody.toner_levels.yellow = usbMetrics.toner_yellow;
                                
                                // Try to save to metrics store
                                try {
                                    await fetch('/api/devices/metrics/save', { 
                                        method: 'POST', 
                                        headers: { 'Content-Type': 'application/json' }, 
                                        body: JSON.stringify(saveBody) 
                                    });
                                } catch (saveErr) {
                                    console.warn('Failed to save USB metrics to store:', saveErr);
                                }
                                
                                if (statusEl) statusEl.textContent = ' USB metrics collected: ' + usbMetrics.total_pages.toLocaleString() + ' pages ✓';
                            } else {
                                if (statusEl) statusEl.textContent = ' No metrics data found';
                            }
                            setTimeout(() => { if (statusEl) statusEl.textContent = ''; if (btn) btn.disabled = false; }, 3000);
                        } catch (err) {
                            if (statusEl) statusEl.textContent = ' Failed: ' + err;
                            if (btn) btn.disabled = false;
                        }
                    } else {
                        // Network device - use SNMP metrics collection
                        if (statusEl) statusEl.textContent = ' Collecting metrics...';
                        try {
                            const body = { serial: p.serial, ip: p.ip };
                            const r = await fetch('/devices/metrics/collect', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
                            if (!r.ok) { const t = await r.text(); if (statusEl) statusEl.textContent = ' Error: ' + t; btn.disabled = false; return; }
                            const result = await r.json();
                            if (statusEl) statusEl.textContent = ' Metrics saved ✓';
                            setTimeout(() => { if (statusEl) statusEl.textContent = ''; if (btn) btn.disabled = false; }, 2000);
                        } catch (err) {
                            if (statusEl) statusEl.textContent = ' Failed: ' + err;
                            if (btn) btn.disabled = false;
                        }
                    }
                });
            }
        } catch (e) { window.__pm_shared.warn('collect metrics wiring failed', e); }

        // Action buttons (delete/save/close)
        try {
            actionsEl.innerHTML = '';
            if (source === 'saved') {
                const deleteBtn = document.createElement('button');
                deleteBtn.textContent = 'Delete Device';
                deleteBtn.className = 'delete';
                deleteBtn.onclick = async function () {
                    if (deleteBtn.disabled) return;
                    deleteBtn.disabled = true;
                    try {
                        const confirmed = await window.__pm_shared.showConfirm(
                            'Delete ' + (p.serial || p.Serial || 'this device') + '? This will remove it from the database.',
                            'Delete Device', true);
                        if (!confirmed || !isCurrent()) return;
                        deleteBtn.textContent = 'Deleting...';
                        const r = await fetch('/devices/delete', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ serial: p.serial || p.Serial }) });
                        if (!r.ok) throw new Error('Delete failed (HTTP ' + r.status + ')');
                        if (!isCurrent()) return;
                        deleteBtn.textContent = 'Deleted ✓';
                        window.__pm_shared.showToast('Device deleted successfully', 'success');
                        const cardToRemove = document.querySelector('.saved-device-card[data-serial="' + (p.serial || p.Serial) + '"]');
                        if (cardToRemove) {
                                    try {
                                        cardToRemove.classList.add('removing');
                                        let handledRem = false;
                                        const onEndRem = () => {
                                            if (handledRem) return; handledRem = true;
                                            try { cardToRemove.remove(); } catch (e) {}
                                            try { overlay.style.display = 'none'; document.body.style.overflow = ''; delete overlay.dataset.currentPrinterIp; } catch (e) {}
                                            try { if (typeof updatePrinters === 'function') updatePrinters(); } catch (e) {}
                                            cardToRemove.removeEventListener('animationend', onEndRem);
                                            cardToRemove.removeEventListener('transitionend', onEndRem);
                                        };
                                        cardToRemove.addEventListener('animationend', onEndRem);
                                        cardToRemove.addEventListener('transitionend', onEndRem);
                                        // safety fallback
                                        setTimeout(onEndRem, 800);
                                    } catch (e) {
                                        overlay.style.display = 'none'; document.body.style.overflow = ''; delete overlay.dataset.currentPrinterIp; try { if (typeof updatePrinters === 'function') updatePrinters(); } catch (er) {}
                                    }
                                } else {
                                    overlay.style.display = 'none'; document.body.style.overflow = ''; delete overlay.dataset.currentPrinterIp; updatePrinters();
                                }
                    } catch (e) {
                        window.__pm_shared.error('Delete failed:', e);
                        window.__pm_shared.showToast('Delete failed: ' + e.message, 'error');
                    } finally {
                        deleteBtn.disabled = false;
                        if (deleteBtn.textContent === 'Deleting...' || deleteBtn.textContent === 'Delete Device') deleteBtn.textContent = 'Delete Device';
                    }
                };
                actionsEl.appendChild(deleteBtn);
            } else {
                const saveBtn = document.createElement('button');
                saveBtn.textContent = 'Save Device';
                saveBtn.classList.add('primary');
                saveBtn.onclick = async function () {
                    saveBtn.disabled = true; saveBtn.textContent = 'Saving...';
                    const statusLine = document.createElement('div'); statusLine.style.cssText = 'margin-top: 12px; font-size: 0.9em; color: #93a1a1; text-align: center;';
                    try {
                        await window.__pm_shared.saveDiscoveredDevice(p.IP || p.ip, true, false);
                        saveBtn.textContent = 'Saved ✓'; actionsEl.appendChild(statusLine);
                        const cardToRemove = document.querySelector('.device-card[data-ip="' + (p.IP || p.ip) + '"]'); if (cardToRemove) cardToRemove.classList.add('removing');
                        setTimeout(async () => {
                            try {
                                let dots = 0; statusLine.textContent = 'Gathering additional details';
                                const dotInterval = setInterval(() => { dots = (dots + 1) % 4; statusLine.textContent = 'Gathering additional details' + '.'.repeat(dots); }, 400);
                                const body = { ip: p.IP || p.ip, max_entries: 5000 };
                                const r = await fetch('/mib_walk', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
                                clearInterval(dotInterval);
                                if (!r.ok) throw new Error('Additional device details failed (HTTP ' + r.status + ')');
                                await r.json();
                                if (!isCurrent()) return;
                                updatePrinters();
                                statusLine.textContent = '✓ Details updated'; statusLine.style.color = '#859900';
                                setTimeout(() => { overlay.style.display = 'none'; document.body.style.overflow = ''; delete overlay.dataset.currentPrinterIp; }, 1200);
                            } catch (e) { window.__pm_shared.warn('Background refresh failed:', e); statusLine.textContent = '⚠ Refresh incomplete (device saved)'; statusLine.style.color = '#b58900'; setTimeout(() => { overlay.style.display = 'none'; document.body.style.overflow = ''; delete overlay.dataset.currentPrinterIp; }, 1500); }
                        }, 100);
                    } catch (e) { window.__pm_shared.error('Save failed:', e); window.__pm_shared.showToast('Save failed: ' + e.message, 'error'); saveBtn.disabled = false; saveBtn.textContent = 'Save Device'; if (statusLine.parentNode) statusLine.remove(); }
                };
                actionsEl.appendChild(saveBtn);
            }
            const closeBtn = document.createElement('button'); closeBtn.textContent = 'Close'; closeBtn.onclick = () => { overlay.style.display = 'none'; document.body.style.overflow = ''; delete overlay.dataset.currentPrinterIp; }; actionsEl.appendChild(closeBtn);
        } catch (e) { window.__pm_shared.warn('actions wiring failed', e); }

        // Load existing Web UI credentials for this device and wire save button
        try {
            if (p && p.serial) {
                (async function () {
                    try {
                        const r = await fetch('/device/webui-credentials?serial=' + encodeURIComponent(p.serial));
                        if (!isCurrent()) return;
                        if (!r.ok) throw new Error('Credentials request failed (HTTP ' + r.status + ')');
                        if (r.ok) {
                            const creds = await r.json();
                            if (!isCurrent()) return;
                            if (creds.exists) {
                                document.getElementById('cred_username').value = creds.username || '';
                                document.getElementById('cred_auth_type').value = creds.auth_type || 'basic';
                                document.getElementById('cred_auto_login').checked = creds.auto_login || false;
                                document.getElementById('creds_status').textContent = '✓ Saved';
                                document.getElementById('creds_status').style.color = '#859900';
                            }
                        }
                    } catch (error) {
                        window.__pm_shared.warn('Device credential loading failed', { serial: p.serial, error });
                        if (isCurrent()) document.getElementById('creds_status').textContent = 'Credentials unavailable';
                    }
                })();

                document.getElementById('save_creds_btn')?.addEventListener('click', async function () {
                    const statusEl = document.getElementById('creds_status');
                    const btn = document.getElementById('save_creds_btn');
                    if (!p || !p.serial) { if (statusEl) { statusEl.textContent = 'Error: no serial'; statusEl.style.color = '#dc322f'; } return; }
                    if (btn) btn.disabled = true;
                    if (statusEl) { statusEl.textContent = 'Saving...'; statusEl.style.color = 'var(--muted)'; }
                    try {
                        const payload = {
                            serial: p.serial,
                            username: document.getElementById('cred_username').value.trim(),
                            password: document.getElementById('cred_password').value,
                            auth_type: document.getElementById('cred_auth_type').value,
                            auto_login: document.getElementById('cred_auto_login').checked
                        };
                        const r = await fetch('/device/webui-credentials', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload) });
                        if (!r.ok) throw new Error('Save failed');
                        if (!isCurrent()) return;
                        if (statusEl) { statusEl.textContent = '✓ Saved'; statusEl.style.color = '#859900'; }
                        const pwdEl = document.getElementById('cred_password'); if (pwdEl) pwdEl.value = '';
                    } catch (e) {
                        window.__pm_shared.error('Device credential saving failed', { serial: p.serial, error: e });
                        if (!isCurrent()) return;
                        const statusEl2 = document.getElementById('creds_status'); if (statusEl2) { statusEl2.textContent = 'Error: ' + e; statusEl2.style.color = '#dc322f'; }
                    } finally { if (isCurrent()) { const btn2 = document.getElementById('save_creds_btn'); if (btn2) btn2.disabled = false; } }
                });
            }
        } catch (e) { window.__pm_shared.warn('cred wiring failed', e); }

        // Initialize device report form
        try {
            if (window.__pm_report && typeof window.__pm_report.initReportForm === 'function') {
                window.__pm_report.initReportForm(p, parseDebug);
            }
        } catch (e) { window.__pm_shared.warn('report form wiring failed', e); }
    }

    // Export shared modal renderer
    window.__pm_shared_cards.showPrinterDetailsData = showPrinterDetailsData;

    // Render a discovered device card (shared implementation)
    function renderDiscoveredCard(p, isSaved) {
        const ip = p.ip || p.address || '';
        const serial = p.serial || '';
        const make = (p.make || p.manufacturer || '').toString();
        const model = (p.model || '').toString();
        const deviceKey = serial || ip || '';

        const savedClass = isSaved ? ' saved' : '';

    const saveBtn = isSaved ? '<button class="btn small" disabled>Saved</button>' : '<button class="btn primary small save-device-btn" data-ip="' + ip + '">Save</button>';
    const proxyBtn = serial ? '<button class="btn small" data-action="open-proxy" data-serial="' + encodeURIComponent(serial) + '">Proxy</button>' : '';

        let html = '';
        html += '<div class="device-card' + savedClass + '" data-device-key="' + deviceKey + '" data-ip="' + ip + '" data-serial="' + serial + '" data-make="' + (make || '') + '" data-model="' + (model || '') + '">';
        html += '<div class="printer-card-header">';
        html += '<div style="display:flex;justify-content:space-between;align-items:center">';
        html += '<div style="display:flex;flex-direction:column">';
        html += '<strong>' + (make ? (make + ' ') : '') + (model || '') + '</strong>';
        html += '<span style="color:var(--muted);font-size:12px">' + ip + '</span>';
        html += '</div>'; // left
        html += '<div style="display:flex;gap:8px">' + saveBtn + proxyBtn + '</div>';
        html += '</div></div>';
        html += '<div class="printer-card-body">';
        if (serial) html += '<div><strong>Serial:</strong> <code>' + serial + '</code></div>';
        html += '</div></div>';

        return html;
    }

    // Show a progressive discovery card while information is being gathered
    function showDiscoveringCard(data) {
        try {
            const discoveredContainer = document.getElementById('discovered_devices_cards');
            if (!discoveredContainer) return;

            const ip = data.ip || 'Unknown IP';
            const serial = data.serial || '';
            const method = data.method || '';
            const status = data.status || 'discovering';

            const cardId = 'discovering-' + ip.replace(/\./g, '-');
            let card = document.getElementById(cardId);

            if (!card) {
                card = document.createElement('div');
                card.id = cardId;
                card.className = 'printer-card discovering';
                card.style.opacity = '0.7';
                card.style.border = '2px dashed var(--accent)';
                discoveredContainer.insertBefore(card, discoveredContainer.firstChild);
            }

            let cardHtml = '<div class="printer-card-header" style="border-bottom:1px solid var(--border)">';
            cardHtml += '<div style="display:flex;justify-content:space-between;align-items:center">';
            cardHtml += '<div style="display:flex;align-items:center;gap:8px">';
            cardHtml += '<span class="spinner" style="display:inline-block"></span>';
            cardHtml += '<span style="color:var(--accent);font-weight:500">Discovering...</span>';
            cardHtml += '</div>';
            cardHtml += '<span style="color:var(--muted);font-size:12px">' + method + '</span>';
            cardHtml += '</div></div>';

            cardHtml += '<div class="printer-card-body">';
            cardHtml += '<div style="margin-bottom:8px"><strong>IP:</strong> <code>' + ip + '</code></div>';

            if (serial) {
                cardHtml += '<div style="margin-bottom:8px"><strong>Serial:</strong> <code>' + serial + '</code></div>';
                if (status === 'gathering_details') {
                    cardHtml += '<div style="color:var(--muted);font-size:12px">🔍 Gathering manufacturer, model, and details...</div>';
                }
            } else {
                cardHtml += '<div style="color:var(--muted);font-size:12px">🔍 Getting serial number...</div>';
            }

            cardHtml += '</div>';
            card.innerHTML = cardHtml;

            setTimeout(() => {
                const existingCard = document.getElementById(cardId);
                if (existingCard) existingCard.remove();
            }, 30000);
        } catch (e) {
            window.__pm_shared.warn('shared.showDiscoveringCard failed', e);
        }
    }

    // Export shared discovered-card functions
    window.__pm_shared_cards.renderDiscoveredCard = renderDiscoveredCard;
    window.__pm_shared_cards.showDiscoveringCard = showDiscoveringCard;

    // Note: backwards-compatible global exports removed. Consumers should use
    // the namespaced `window.__pm_shared_cards` API (renderSavedCard,
    // renderDiscoveredCard, showDiscoveringCard, checkDatabaseRotationWarning,
    // renderCapabilities).

})();
