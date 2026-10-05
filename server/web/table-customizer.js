/**
 * PrintMaster Table Customizer
 * 
 * Advanced table customization module supporting:
 * - Column visibility (show/hide)
 * - Column reordering (drag and drop)
 * - Column sorting (click to sort, multi-column with shift)
 * - Column resizing
 * - Column pinning (freeze left/right)
 * - Column grouping
 * - Quick filters per column
 * - Export functionality
 * - Persistence to localStorage
 */

(function() {
    'use strict';

    // Storage keys
    const STORAGE_PREFIX = 'pm_table_config_';

    /**
     * Default column definitions for the devices table
     */
    const DEVICES_COLUMN_DEFINITIONS = [
        {
            id: 'device',
            label: 'Device',
            sortKey: 'manufacturer',
            width: 200,
            minWidth: 150,
            pinnable: true,
            hideable: false, // Always show
            resizable: true,
            filterable: true,
            filterType: 'text',
            render: (device, meta) => {
                const manufacturer = escapeHtml((device.manufacturer || 'Unknown') + ' ' + (device.model || ''));
                const serial = escapeHtml(device.serial || '—');
                return `
                    <div class="table-primary">${manufacturer}</div>
                    <div class="muted-text">Serial ${serial}</div>
                `;
            }
        },
        {
            id: 'status',
            label: 'Status',
            sortKey: 'status',
            width: 100,
            minWidth: 80,
            pinnable: true,
            hideable: true,
            resizable: true,
            filterable: true,
            filterType: 'select',
            filterOptions: ['healthy', 'warning', 'error', 'jam'],
            render: (device, meta) => {
                return window.__pm_shared?.renderDeviceStatusBadge?.(meta.status) || 
                    `<span class="status-pill ${meta.status?.code || 'healthy'}">${escapeHtml(meta.status?.label || 'healthy')}</span>`;
            }
        },
        {
            id: 'consumables',
            label: 'Consumables',
            sortKey: 'consumables',
            width: 140,
            minWidth: 100,
            pinnable: false,
            hideable: true,
            resizable: true,
            filterable: true,
            filterType: 'select',
            filterOptions: ['critical', 'low', 'medium', 'high', 'unknown'],
            render: (device, meta) => {
                if (meta.metricsState && meta.metricsState !== 'ready') {
                    return window.__pm_shared?.renderTonerBars?.(meta.tonerData) || '<span class="muted-text">Loading supplies…</span>';
                }
                if (meta.tonerData && meta.tonerData.length > 0) {
                    return window.__pm_shared?.renderTonerBars?.(meta.tonerData) || '<span class="muted-text">—</span>';
                }
                return '<span class="muted-text">—</span>';
            }
        },
        {
            id: 'agent',
            label: 'Agent',
            sortKey: 'agent',
            width: 130,
            minWidth: 100,
            pinnable: true,
            hideable: true,
            resizable: true,
            filterable: true,
            filterType: 'text',
            render: (device, meta) => escapeHtml(meta.agentName || 'Unassigned')
        },
        {
            id: 'tenant',
            label: 'Tenant',
            sortKey: 'tenant',
            width: 130,
            minWidth: 100,
            pinnable: false,
            hideable: true,
            resizable: true,
            filterable: true,
            filterType: 'text',
            render: (device, meta) => {
                const tenantLabel = window.formatTenantDisplay?.(meta.tenantId || device.tenant_id || '') || 
                    (meta.tenantId || device.tenant_id || '—');
                return escapeHtml(tenantLabel);
            }
        },
        {
            id: 'network',
            label: 'Network',
            sortKey: 'ip',
            width: 150,
            minWidth: 120,
            pinnable: false,
            hideable: true,
            resizable: true,
            filterable: true,
            filterType: 'text',
            render: (device, meta) => {
                const ip = escapeHtml(device.ip || 'N/A');
                const hostname = device.hostname ? `<div class="muted-text">${escapeHtml(device.hostname)}</div>` : '';
                return `<div class="table-primary">${ip}</div>${hostname}`;
            }
        },
        {
            id: 'mac',
            label: 'MAC Address',
            sortKey: 'mac',
            width: 140,
            minWidth: 120,
            pinnable: false,
            hideable: true,
            resizable: true,
            filterable: true,
            filterType: 'text',
            defaultHidden: true,
            render: (device, meta) => escapeHtml(device.mac || '—')
        },
        {
            id: 'location',
            label: 'Location',
            sortKey: 'location',
            width: 130,
            minWidth: 100,
            pinnable: false,
            hideable: true,
            resizable: true,
            filterable: true,
            filterType: 'text',
            render: (device, meta) => escapeHtml(meta.location || '—')
        },
        {
            id: 'page_count',
            label: 'Total Pages',
            sortKey: 'page_count',
            width: 100,
            minWidth: 80,
            pinnable: false,
            hideable: true,
            resizable: true,
            filterable: false,
            defaultHidden: true,
            render: (device, meta) => {
                const pageCount = device.page_count;
                return pageCount != null ? escapeHtml(pageCount.toLocaleString()) : '<span class="muted-text">—</span>';
            }
        },
        {
            id: 'color_pages',
            label: 'Color Pages',
            sortKey: 'color_pages',
            width: 100,
            minWidth: 80,
            pinnable: false,
            hideable: true,
            resizable: true,
            filterable: false,
            defaultHidden: true,
            render: (device, meta) => {
                const count = device.raw_data?.color_pages;
                return count ? escapeHtml(count.toLocaleString()) : '<span class="muted-text">—</span>';
            }
        },
        {
            id: 'mono_pages',
            label: 'Mono Pages',
            sortKey: 'mono_pages',
            width: 100,
            minWidth: 80,
            pinnable: false,
            hideable: true,
            resizable: true,
            filterable: false,
            defaultHidden: true,
            render: (device, meta) => {
                const count = device.raw_data?.mono_pages;
                return count ? escapeHtml(count.toLocaleString()) : '<span class="muted-text">—</span>';
            }
        },
        {
            id: 'copy_pages',
            label: 'Copy Pages',
            sortKey: 'copy_pages',
            width: 100,
            minWidth: 80,
            pinnable: false,
            hideable: true,
            resizable: true,
            filterable: false,
            defaultHidden: true,
            render: (device, meta) => {
                const count = device.raw_data?.copy_pages;
                return count ? escapeHtml(count.toLocaleString()) : '<span class="muted-text">—</span>';
            }
        },
        {
            id: 'print_pages',
            label: 'Print Pages',
            sortKey: 'print_pages',
            width: 100,
            minWidth: 80,
            pinnable: false,
            hideable: true,
            resizable: true,
            filterable: false,
            defaultHidden: true,
            render: (device, meta) => {
                const count = device.raw_data?.print_pages;
                return count ? escapeHtml(count.toLocaleString()) : '<span class="muted-text">—</span>';
            }
        },
        {
            id: 'scan_count',
            label: 'Scan Count',
            sortKey: 'scan_count',
            width: 100,
            minWidth: 80,
            pinnable: false,
            hideable: true,
            resizable: true,
            filterable: false,
            defaultHidden: true,
            render: (device, meta) => {
                const count = device.raw_data?.scan_count;
                return count ? escapeHtml(count.toLocaleString()) : '<span class="muted-text">—</span>';
            }
        },
        {
            id: 'fax_pages',
            label: 'Fax Pages',
            sortKey: 'fax_pages',
            width: 100,
            minWidth: 80,
            pinnable: false,
            hideable: true,
            resizable: true,
            filterable: false,
            defaultHidden: true,
            render: (device, meta) => {
                const count = device.raw_data?.fax_pages;
                return count ? escapeHtml(count.toLocaleString()) : '<span class="muted-text">—</span>';
            }
        },
        {
            id: 'duplex_sheets',
            label: 'Duplex Sheets',
            sortKey: 'duplex_sheets',
            width: 110,
            minWidth: 90,
            pinnable: false,
            hideable: true,
            resizable: true,
            filterable: false,
            defaultHidden: true,
            render: (device, meta) => {
                const count = device.raw_data?.duplex_sheets;
                return count ? escapeHtml(count.toLocaleString()) : '<span class="muted-text">—</span>';
            }
        },
        {
            id: 'firmware',
            label: 'Firmware',
            sortKey: 'firmware',
            width: 120,
            minWidth: 80,
            pinnable: false,
            hideable: true,
            resizable: true,
            filterable: true,
            filterType: 'text',
            defaultHidden: true,
            render: (device, meta) => escapeHtml(device.firmware || device.raw_data?.firmware_version || '—')
        },
        {
            id: 'last_seen',
            label: 'Last Seen',
            sortKey: 'last_seen',
            width: 100,
            minWidth: 80,
            pinnable: false,
            hideable: true,
            resizable: true,
            filterable: false,
            render: (device, meta) => {
                const title = escapeHtml(meta.lastSeenTooltip || 'Never');
                const text = escapeHtml(meta.lastSeenRelative || 'Never');
                return `<span title="${title}">${text}</span>`;
            }
        }
        // Actions column removed - using context menu instead
    ];

    // Only indexed metadata can be sorted fleet-wide. Hydrated-only fields must
    // not silently sort a mixed loaded/unloaded subset. Snapshot fields paint
    // pending/error separately from a genuinely absent counter (including zero).
    const indexedSortKeys = new Set(['manufacturer', 'status', 'consumables', 'agent', 'tenant', 'ip', 'location', 'page_count', 'last_seen']);
    const snapshotColumns = new Set(['color_pages', 'mono_pages', 'copy_pages', 'print_pages', 'scan_count', 'fax_pages', 'duplex_sheets']);
    DEVICES_COLUMN_DEFINITIONS.forEach(column => {
        if (!indexedSortKeys.has(column.sortKey)) delete column.sortKey;
        if (snapshotColumns.has(column.id)) column.render = (device, meta) => {
            if (meta.metricsState && !['ready', 'missing'].includes(meta.metricsState)) {
                return `<span class="muted-text" role="status">${meta.metricsState === 'error' ? 'Unavailable' : 'Loading…'}</span>`;
            }
            const count = device.raw_data?.[column.id];
            return count != null ? escapeHtml(count.toLocaleString()) : '<span class="muted-text">—</span>';
        };
        if (['mac', 'firmware'].includes(column.id)) {
            const render = column.render;
            column.render = (device, meta) => meta.rowState && !['ready', 'missing'].includes(meta.rowState)
                ? `<span class="muted-text">${meta.rowState === 'error' ? 'Unavailable' : 'Loading…'}</span>` : render(device, meta);
        }
    });

    /**
     * Default column definitions for the agents table
     */
    const AGENTS_COLUMN_DEFINITIONS = [
        {
            id: 'agent',
            label: 'Agent',
            sortKey: 'name',
            width: 200,
            minWidth: 150,
            pinnable: true,
            hideable: false, // Always show
            resizable: true,
            filterable: true,
            filterType: 'text',
            render: (agent, meta) => {
                const displayName = escapeHtml(window.getAgentDisplayName?.(agent) || agent.name || agent.hostname || 'Unknown');
                const hostname = escapeHtml(agent.hostname || '');
                return `
                    <div class="table-primary">${displayName}</div>
                    <div class="muted-text">${hostname}</div>
                `;
            }
        },
        {
            id: 'tenant',
            label: 'Tenant',
            sortKey: 'tenant',
            width: 130,
            minWidth: 100,
            pinnable: false,
            hideable: true,
            resizable: true,
            filterable: true,
            filterType: 'text',
            render: (agent, meta) => {
                const tenantLabel = window.formatTenantDisplay?.(agent.tenant_id || meta.tenantId || '') || 
                    (agent.tenant_id || meta.tenantId || '—');
                return escapeHtml(tenantLabel);
            }
        },
        {
            id: 'status',
            label: 'Status',
            sortKey: 'status',
            width: 100,
            minWidth: 80,
            pinnable: true,
            hideable: true,
            resizable: true,
            filterable: true,
            filterType: 'select',
            filterOptions: ['active', 'degraded', 'offline'],
            render: (agent, meta) => {
                return window.renderAgentStatusBadge?.(meta) || 
                    `<span class="status-pill ${meta.statusKey || 'offline'}">${escapeHtml(meta.statusLabel || 'Unknown')}</span>`;
            }
        },
        {
            id: 'platform',
            label: 'Platform',
            sortKey: 'platform',
            width: 100,
            minWidth: 80,
            pinnable: false,
            hideable: true,
            resizable: true,
            filterable: true,
            filterType: 'text',
            render: (agent, meta) => escapeHtml(agent.platform || 'Unknown')
        },
        {
            id: 'version',
            label: 'Version',
            sortKey: 'version',
            width: 130,
            minWidth: 100,
            pinnable: false,
            hideable: true,
            resizable: true,
            filterable: true,
            filterType: 'text',
            render: (agent, meta) => {
                return window.renderAgentVersionCell?.(agent, true) || escapeHtml(agent.version || 'Unknown');
            }
        },
        {
            id: 'ip',
            label: 'IP Address',
            sortKey: 'ip',
            width: 130,
            minWidth: 100,
            pinnable: false,
            hideable: true,
            resizable: true,
            filterable: true,
            filterType: 'text',
            defaultHidden: true,
            render: (agent, meta) => escapeHtml(agent.ip || 'N/A')
        },
        {
            id: 'agent_id',
            label: 'Agent ID',
            sortKey: 'agent_id',
            width: 280,
            minWidth: 200,
            pinnable: false,
            hideable: true,
            resizable: true,
            filterable: true,
            filterType: 'text',
            defaultHidden: true,
            render: (agent, meta) => `<span class="muted-text copyable" data-copy="${escapeHtml(agent.agent_id || '')}" title="Click to copy">${escapeHtml(agent.agent_id || '—')}</span>`
        },
        {
            id: 'devices_count',
            label: 'Devices',
            sortKey: 'devices_count',
            width: 80,
            minWidth: 60,
            pinnable: false,
            hideable: true,
            resizable: true,
            filterable: false,
            defaultHidden: true,
            render: (agent, meta) => {
                const count = agent.devices?.length || 0;
                return count.toLocaleString();
            }
        },
        {
            id: 'registered_at',
            label: 'Registered',
            sortKey: 'registered_at',
            width: 120,
            minWidth: 90,
            pinnable: false,
            hideable: true,
            resizable: true,
            filterable: false,
            defaultHidden: true,
            render: (agent, meta) => {
                const date = agent.registered_at ? new Date(agent.registered_at) : null;
                if (!date) return '<span class="muted-text">—</span>';
                return `<span title="${date.toLocaleString()}">${date.toLocaleDateString()}</span>`;
            }
        },
        {
            id: 'last_seen',
            label: 'Last Seen',
            sortKey: 'last_seen',
            width: 100,
            minWidth: 80,
            pinnable: false,
            hideable: true,
            resizable: true,
            filterable: false,
            render: (agent, meta) => {
                const title = escapeHtml(meta.lastSeenTooltip || 'Never');
                const text = escapeHtml(meta.lastSeenRelative || 'Never');
                return `<span title="${title}">${text}</span>`;
            }
        }
        // Actions column removed - using context menu instead
    ];

    /**
     * TableCustomizer class
     */
    class TableCustomizer {
        constructor(tableId, options = {}) {
            this.tableId = tableId;
            this.storageKey = STORAGE_PREFIX + tableId;
            this.options = {
                columnDefs: options.columnDefs || [],
                onSort: options.onSort || null,
                onFilter: options.onFilter || null,
                onColumnChange: options.onColumnChange || null,
                persistConfig: options.persistConfig !== false,
                enableResize: options.enableResize !== false,
                enableReorder: options.enableReorder !== false,
                enableColumnMenu: options.enableColumnMenu !== false,
                enableExport: options.enableExport !== false,
                enableQuickFilter: options.enableQuickFilter !== false,
                ...options
            };
            
            this.columns = [];
            this.sortState = { key: null, dir: 'asc', multiSort: [] };
            this.columnFilters = {};
            this.resizeState = null;
            this.dragState = null;
            
            this._init();
        }

        _init() {
            // Load saved config or use defaults
            this._loadConfig();
            
            // Initialize event listeners
            this._bindEvents();
        }

        _loadConfig() {
            let savedConfig = null;
            if (this.options.persistConfig) {
                try {
                    const raw = localStorage.getItem(this.storageKey);
                    if (raw) {
                        savedConfig = JSON.parse(raw);
                    }
                } catch (e) {
                    console.warn('Failed to load table config:', e);
                }
            }

            // Build columns from definitions + saved config
            this.columns = this.options.columnDefs.map((def, index) => {
                const saved = savedConfig?.columns?.find(c => c.id === def.id);
                return {
                    ...def,
                    visible: saved?.visible ?? !def.defaultHidden,
                    order: saved?.order ?? index,
                    width: saved?.width ?? def.width,
                    pinned: saved?.pinned ?? (def.pinnedRight ? 'right' : (def.pinned ? 'left' : null))
                };
            });

            // Sort by order
            this.columns.sort((a, b) => a.order - b.order);

            // Restore sort state
            if (savedConfig?.sortState) {
                this.sortState = savedConfig.sortState;
            }
        }

        _saveConfig() {
            if (!this.options.persistConfig) return;
            
            try {
                const config = {
                    columns: this.columns.map((col, index) => ({
                        id: col.id,
                        visible: col.visible,
                        order: index,
                        width: col.width,
                        pinned: col.pinned
                    })),
                    sortState: this.sortState
                };
                localStorage.setItem(this.storageKey, JSON.stringify(config));
            } catch (e) {
                console.warn('Failed to save table config:', e);
            }
        }

        _bindEvents() {
            // These will be bound when renderHeader is called
        }

        /**
         * Get visible columns in display order
         */
        getVisibleColumns() {
            return this.columns.filter(c => c.visible);
        }

        /**
         * Get all columns
         */
        getAllColumns() {
            return this.columns;
        }

        /**
         * Toggle column visibility
         */
        toggleColumn(columnId, visible) {
            const col = this.columns.find(c => c.id === columnId);
            if (col && col.hideable) {
                col.visible = visible ?? !col.visible;
                this._saveConfig();
                this.options.onColumnChange?.(this.columns);
                return true;
            }
            return false;
        }

        /**
         * Reorder columns
         */
        reorderColumns(fromIndex, toIndex) {
            if (fromIndex === toIndex) return;
            
            const visibleColumns = this.getVisibleColumns();
            const [moved] = visibleColumns.splice(fromIndex, 1);
            visibleColumns.splice(toIndex, 0, moved);
            
            // Update order in main columns array
            visibleColumns.forEach((col, idx) => {
                const mainCol = this.columns.find(c => c.id === col.id);
                if (mainCol) mainCol.order = idx;
            });
            
            this._saveConfig();
            this.options.onColumnChange?.(this.columns);
        }

        /**
         * Resize column
         */
        resizeColumn(columnId, newWidth) {
            const col = this.columns.find(c => c.id === columnId);
            if (col && col.resizable) {
                col.width = Math.max(col.minWidth || 50, newWidth);
                this._saveConfig();
            }
        }

        /**
         * Pin/unpin column
         */
        pinColumn(columnId, position) {
            const col = this.columns.find(c => c.id === columnId);
            if (col && col.pinnable) {
                col.pinned = position; // 'left', 'right', or null
                this._saveConfig();
                this.options.onColumnChange?.(this.columns);
            }
        }

        /**
         * Set sort
         */
        setSort(columnId, direction, addToMultiSort = false) {
            if (addToMultiSort) {
                // Multi-column sort with shift+click
                const existingIdx = this.sortState.multiSort.findIndex(s => s.key === columnId);
                if (existingIdx >= 0) {
                    // Toggle direction or remove
                    if (this.sortState.multiSort[existingIdx].dir === direction) {
                        this.sortState.multiSort.splice(existingIdx, 1);
                    } else {
                        this.sortState.multiSort[existingIdx].dir = direction;
                    }
                } else {
                    this.sortState.multiSort.push({ key: columnId, dir: direction });
                }
            } else {
                // Single column sort
                this.sortState = { key: columnId, dir: direction, multiSort: [] };
            }
            
            this._saveConfig();
            this.options.onSort?.(this.sortState);
        }

        /**
         * Set column filter
         */
        setColumnFilter(columnId, value) {
            if (value === null || value === undefined || value === '') {
                delete this.columnFilters[columnId];
            } else {
                this.columnFilters[columnId] = value;
            }
            this.options.onFilter?.(this.columnFilters);
        }

        /**
         * Reset to default configuration
         */
        resetToDefaults() {
            localStorage.removeItem(this.storageKey);
            this._loadConfig();
            this.options.onColumnChange?.(this.columns);
        }

        /**
         * Render the customization toolbar
         */
        renderToolbar() {
            const visibleCount = this.getVisibleColumns().length;
            const totalCount = this.columns.filter(c => c.hideable).length + this.columns.filter(c => !c.hideable).length;
            
            return `
                <div class="table-customizer-toolbar">
                    <div class="table-customizer-left">
                        <button class="ghost-btn table-column-toggle-btn" title="Customize columns">
                            <svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor">
                                <path d="M1.5 2h13a.5.5 0 0 1 0 1h-13a.5.5 0 0 1 0-1zm0 4h13a.5.5 0 0 1 0 1h-13a.5.5 0 0 1 0-1zm0 4h13a.5.5 0 0 1 0 1h-13a.5.5 0 0 1 0-1zm0 4h13a.5.5 0 0 1 0 1h-13a.5.5 0 0 1 0-1z"/>
                            </svg>
                            Columns
                            <span class="column-count-badge">${visibleCount}</span>
                        </button>
                    </div>
                    <div class="table-customizer-right">
                        ${this.options.enableExport ? `
                            <button class="ghost-btn table-export-btn" title="Export table data">
                                <svg width="14" height="14" viewBox="0 0 16 16" fill="currentColor">
                                    <path d="M.5 9.9a.5.5 0 0 1 .5.5v2.5a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1v-2.5a.5.5 0 0 1 1 0v2.5a2 2 0 0 1-2 2H2a2 2 0 0 1-2-2v-2.5a.5.5 0 0 1 .5-.5z"/>
                                    <path d="M7.646 11.854a.5.5 0 0 0 .708 0l3-3a.5.5 0 0 0-.708-.708L8.5 10.293V1.5a.5.5 0 0 0-1 0v8.793L5.354 8.146a.5.5 0 1 0-.708.708l3 3z"/>
                                </svg>
                                Export
                            </button>
                        ` : ''}
                        <button class="ghost-btn table-reset-btn" title="Reset to default columns">
                            <svg width="14" height="14" viewBox="0 0 16 16" fill="currentColor">
                                <path fill-rule="evenodd" d="M8 3a5 5 0 1 0 4.546 2.914.5.5 0 0 1 .908-.417A6 6 0 1 1 8 2v1z"/>
                                <path d="M8 4.466V.534a.25.25 0 0 1 .41-.192l2.36 1.966c.12.1.12.284 0 .384L8.41 4.658A.25.25 0 0 1 8 4.466z"/>
                            </svg>
                            Reset
                        </button>
                    </div>
                </div>
            `;
        }

        /**
         * Render the column picker dropdown
         */
        renderColumnPicker() {
            const groups = {
                pinned: this.columns.filter(c => c.pinned),
                visible: this.columns.filter(c => c.visible && !c.pinned),
                hidden: this.columns.filter(c => !c.visible)
            };

            const renderGroup = (title, cols, showDrag = true) => {
                if (cols.length === 0) return '';
                return `
                    <div class="column-picker-group">
                        <div class="column-picker-group-title">${title}</div>
                        <div class="column-picker-list" data-group="${title.toLowerCase()}">
                            ${cols.map(col => `
                                <div class="column-picker-item ${col.visible ? 'column-visible' : 'column-hidden'} ${col.pinned ? 'pinned-' + col.pinned : ''}" 
                                     data-column-id="${col.id}"
                                     draggable="${showDrag && this.options.enableReorder && col.visible ? 'true' : 'false'}">
                                    ${showDrag && this.options.enableReorder && col.visible ? `
                                        <span class="column-drag-handle" title="Drag to reorder">⋮⋮</span>
                                    ` : '<span class="column-drag-placeholder"></span>'}
                                    <label class="column-picker-toggle">
                                        <span class="column-picker-label">${escapeHtml(col.label)}</span>
                                        <input type="checkbox" 
                                               ${col.visible ? 'checked' : ''} 
                                               ${!col.hideable ? 'disabled' : ''}
                                               data-column-id="${col.id}">
                                    </label>
                                    ${col.pinnable ? `
                                        <div class="column-pin-controls">
                                            <button class="pin-btn ${col.pinned === 'left' ? 'active' : ''}" 
                                                    data-pin="left" 
                                                    data-column-id="${col.id}" 
                                                    title="Pin to left">◀</button>
                                            <button class="pin-btn ${col.pinned === 'right' ? 'active' : ''}" 
                                                    data-pin="right" 
                                                    data-column-id="${col.id}" 
                                                    title="Pin to right">▶</button>
                                        </div>
                                    ` : ''}
                                </div>
                            `).join('')}
                        </div>
                    </div>
                `;
            };

            return `
                <div class="column-picker-dropdown">
                    <div class="column-picker-header">
                        <span>Customize Columns</span>
                        <button class="column-picker-close" title="Close">×</button>
                    </div>
                    <div class="column-picker-body">
                        ${renderGroup('Pinned', groups.pinned)}
                        ${renderGroup('Visible', groups.visible)}
                        ${renderGroup('Available', groups.hidden, false)}
                    </div>
                    <div class="column-picker-footer">
                        <button class="ghost-btn show-all-btn">Show All</button>
                        <button class="ghost-btn hide-optional-btn">Hide Optional</button>
                    </div>
                </div>
            `;
        }

        /**
         * Render table header with sort indicators and resize handles
         */
        renderHeader() {
            const visibleColumns = this.getVisibleColumns();
            const totalColumns = visibleColumns.length;
            
            return visibleColumns.map(col => {
                const isSorted = this.sortState.key === col.sortKey;
                const sortDir = isSorted ? this.sortState.dir : null;
                const multiSortIdx = this.sortState.multiSort.findIndex(s => s.key === col.sortKey);
                const isMultiSorted = multiSortIdx >= 0;
                
                let sortIndicator = '';
                if (col.sortKey) {
                    if (isSorted) {
                        sortIndicator = `<span class="sort-indicator ${sortDir}">${sortDir === 'asc' ? '↑' : '↓'}</span>`;
                    } else if (isMultiSorted) {
                        const msDir = this.sortState.multiSort[multiSortIdx].dir;
                        sortIndicator = `<span class="sort-indicator multi ${msDir}">${multiSortIdx + 1}${msDir === 'asc' ? '↑' : '↓'}</span>`;
                    }
                }

                // Always set a width to prevent layout recalculation with table-layout:fixed
                // Use explicit width if defined, otherwise distribute evenly
                const widthStyle = col.width 
                    ? `style="width:${col.width}px;min-width:${col.minWidth || 50}px;"`
                    : `style="width:${Math.floor(100 / totalColumns)}%;"`;
                const sortable = col.sortKey ? 'sortable' : '';
                const pinClass = col.pinned ? `pinned-${col.pinned}` : '';
                const actionsClass = col.isActions ? 'actions-col' : '';
                
                return `
                    <th data-column-id="${col.id}" 
                        data-sort-key="${col.sortKey || ''}"
                        class="${sortable} ${pinClass} ${actionsClass} ${isSorted ? 'sorted' : ''}"
                        ${widthStyle}>
                        <div class="th-content">
                            <span class="th-label">${escapeHtml(col.label)}</span>
                            ${sortIndicator}
                        </div>
                        ${col.resizable && this.options.enableResize ? `
                            <div class="column-resize-handle" data-column-id="${col.id}"></div>
                        ` : ''}
                    </th>
                `;
            }).join('');
        }

        /**
         * Render a table row
         */
        renderRow(item, meta = {}) {
            const visibleColumns = this.getVisibleColumns();
            
            return visibleColumns.map(col => {
                const pinClass = col.pinned ? `pinned-${col.pinned}` : '';
                const actionsClass = col.isActions ? 'actions-col' : '';
                // Don't set width on td - let the th control column width via table-layout
                
                let content = '';
                try {
                    content = col.render(item, meta);
                } catch (e) {
                    console.warn(`Error rendering column ${col.id}:`, e);
                    content = '<span class="muted-text">—</span>';
                }
                
                return `<td class="${pinClass} ${actionsClass}" data-column-id="${col.id}">${content}</td>`;
            }).join('');
        }

        /**
         * Export table data to CSV
         */
        exportToCSV(data, filename = 'export.csv') {
            const visibleColumns = this.getVisibleColumns().filter(c => !c.isActions);
            
            // Header row
            const headers = visibleColumns.map(c => `"${c.label.replace(/"/g, '""')}"`).join(',');
            
            // Data rows
            const rows = data.map(item => {
                const meta = item.__meta || {};
                return visibleColumns.map(col => {
                    let value = '';
                    try {
                        // Get raw value for export
                        value = this._getExportValue(item, meta, col);
                    } catch (e) {
                        value = '';
                    }
                    // Escape CSV
                    return `"${String(value).replace(/"/g, '""')}"`;
                }).join(',');
            }).join('\n');
            
            const csv = headers + '\n' + rows;
            const blob = new Blob([csv], { type: 'text/csv;charset=utf-8;' });
            const link = document.createElement('a');
            link.href = URL.createObjectURL(blob);
            link.download = filename;
            link.click();
            URL.revokeObjectURL(link.href);
        }

        _getExportValue(item, meta, col) {
            switch (col.id) {
                case 'device':
                    return (item.manufacturer || '') + ' ' + (item.model || '');
                case 'status':
                    return meta.status?.label || meta.status?.code || 'healthy';
                case 'consumables':
                    return meta.tonerData?.map(t => `${t.color}:${t.level}%`).join('; ') || '';
                case 'agent':
                    return meta.agentName || '';
                case 'tenant':
                    return window.formatTenantDisplay?.(meta.tenantId || item.tenant_id || '') || '';
                case 'network':
                    return item.ip || '';
                case 'mac':
                    return item.mac || '';
                case 'location':
                    return meta.location || '';
                case 'page_count':
                    return item.raw_data?.total_pages || item.raw_data?.page_count_total || item.raw_data?.page_count || item.page_count || '';
                case 'color_pages':
                    return item.raw_data?.color_pages || '';
                case 'mono_pages':
                    return item.raw_data?.mono_pages || '';
                case 'copy_pages':
                    return item.raw_data?.copy_pages || '';
                case 'print_pages':
                    return item.raw_data?.print_pages || '';
                case 'scan_count':
                    return item.raw_data?.scan_count || '';
                case 'fax_pages':
                    return item.raw_data?.fax_pages || '';
                case 'duplex_sheets':
                    return item.raw_data?.duplex_sheets || '';
                case 'firmware':
                    return item.firmware || item.raw_data?.firmware_version || '';
                case 'last_seen':
                    return meta.lastSeenTooltip || '';
                default:
                    return '';
            }
        }

        /**
         * Initialize event handlers for the customizer UI
         */
        bindToolbarEvents(toolbarElement) {
            if (!toolbarElement) return;

            // Column toggle button
            const toggleBtn = toolbarElement.querySelector('.table-column-toggle-btn');
            if (toggleBtn) {
                toggleBtn.addEventListener('click', (e) => {
                    e.stopPropagation();
                    this._showColumnPicker(toggleBtn);
                });
            }

            // Export button
            const exportBtn = toolbarElement.querySelector('.table-export-btn');
            if (exportBtn) {
                exportBtn.addEventListener('click', () => {
                    this.options.onExport?.();
                });
            }

            // Reset button
            const resetBtn = toolbarElement.querySelector('.table-reset-btn');
            if (resetBtn) {
                resetBtn.addEventListener('click', () => {
                    if (confirm('Reset table columns to default configuration?')) {
                        this.resetToDefaults();
                    }
                });
            }
        }

        _showColumnPicker(anchorElement) {
            // Toggle behavior - if picker already exists for this anchor, close it
            const existingPicker = document.querySelector('.column-picker-dropdown');
            if (existingPicker) {
                existingPicker.remove();
                return; // Don't open a new one
            }
            
            const picker = document.createElement('div');
            picker.innerHTML = this.renderColumnPicker();
            const dropdown = picker.firstElementChild;
            
            // Position dropdown with viewport bounds checking
            const rect = anchorElement.getBoundingClientRect();
            const viewportHeight = window.innerHeight;
            const viewportWidth = window.innerWidth;
            const preferredHeight = 480; // preferred max-height from CSS
            const dropdownWidth = 320; // approximate width
            const margin = 8; // minimum margin from viewport edge
            const buttonClearance = 4; // gap between button and dropdown
            
            // Calculate available space above and below the button
            const spaceBelow = viewportHeight - rect.bottom - margin - buttonClearance;
            const spaceAbove = rect.top - margin - buttonClearance;
            
            let top, maxHeight;
            
            if (spaceBelow >= preferredHeight) {
                // Plenty of space below - position there
                top = rect.bottom + buttonClearance;
                maxHeight = Math.min(preferredHeight, spaceBelow);
            } else if (spaceAbove >= preferredHeight) {
                // Plenty of space above - position there
                maxHeight = Math.min(preferredHeight, spaceAbove);
                top = rect.top - maxHeight - buttonClearance;
            } else if (spaceBelow >= spaceAbove) {
                // More space below, use what's available
                top = rect.bottom + buttonClearance;
                maxHeight = spaceBelow;
            } else {
                // More space above, use what's available
                maxHeight = spaceAbove;
                top = rect.top - maxHeight - buttonClearance;
            }
            
            // Ensure minimum usable height (at least show header + some items)
            maxHeight = Math.max(maxHeight, 200);
            
            // Calculate left position - keep within viewport
            let left = rect.left;
            if (left + dropdownWidth > viewportWidth - margin) {
                left = Math.max(margin, viewportWidth - dropdownWidth - margin);
            }
            
            dropdown.style.position = 'fixed';
            dropdown.style.top = `${top}px`;
            dropdown.style.left = `${left}px`;
            dropdown.style.maxHeight = `${maxHeight}px`;
            dropdown.style.zIndex = '10000';
            
            document.body.appendChild(dropdown);
            
            // Bind events
            this._bindColumnPickerEvents(dropdown);
            
            // Close on outside click
            const closeHandler = (e) => {
                if (!dropdown.contains(e.target) && e.target !== anchorElement) {
                    dropdown.remove();
                    document.removeEventListener('click', closeHandler);
                }
            };
            setTimeout(() => document.addEventListener('click', closeHandler), 0);
        }

        _bindColumnPickerEvents(dropdown) {
            // Close button
            const closeBtn = dropdown.querySelector('.column-picker-close');
            if (closeBtn) {
                closeBtn.addEventListener('click', () => dropdown.remove());
            }

            // Checkbox changes
            dropdown.querySelectorAll('input[type="checkbox"]').forEach(cb => {
                cb.addEventListener('change', (e) => {
                    const colId = e.target.dataset.columnId;
                    this.toggleColumn(colId, e.target.checked);
                    // Refresh picker body content
                    const newPicker = document.createElement('div');
                    newPicker.innerHTML = this.renderColumnPicker();
                    const newBody = newPicker.querySelector('.column-picker-body');
                    const existingBody = dropdown.querySelector('.column-picker-body');
                    if (newBody && existingBody) {
                        existingBody.innerHTML = newBody.innerHTML;
                    }
                    this._bindColumnPickerEvents(dropdown);
                });
            });

            // Pin buttons
            dropdown.querySelectorAll('.pin-btn').forEach(btn => {
                btn.addEventListener('click', (e) => {
                    const colId = btn.dataset.columnId;
                    const position = btn.dataset.pin;
                    const col = this.columns.find(c => c.id === colId);
                    // Toggle pin
                    this.pinColumn(colId, col?.pinned === position ? null : position);
                    dropdown.remove();
                });
            });

            // Drag and drop for reordering
            if (this.options.enableReorder) {
                this._setupColumnDragDrop(dropdown);
            }

            // Show all button
            const showAllBtn = dropdown.querySelector('.show-all-btn');
            if (showAllBtn) {
                showAllBtn.addEventListener('click', () => {
                    this.columns.forEach(col => {
                        if (col.hideable) col.visible = true;
                    });
                    this._saveConfig();
                    this.options.onColumnChange?.(this.columns);
                    dropdown.remove();
                });
            }

            // Hide optional button
            const hideOptionalBtn = dropdown.querySelector('.hide-optional-btn');
            if (hideOptionalBtn) {
                hideOptionalBtn.addEventListener('click', () => {
                    this.columns.forEach(col => {
                        if (col.hideable && col.defaultHidden) col.visible = false;
                    });
                    this._saveConfig();
                    this.options.onColumnChange?.(this.columns);
                    dropdown.remove();
                });
            }
        }

        _setupColumnDragDrop(dropdown) {
            const items = dropdown.querySelectorAll('.column-picker-item[draggable="true"]');
            let draggedItem = null;

            items.forEach(item => {
                item.addEventListener('dragstart', (e) => {
                    draggedItem = item;
                    item.classList.add('dragging');
                    e.dataTransfer.effectAllowed = 'move';
                });

                item.addEventListener('dragend', () => {
                    item.classList.remove('dragging');
                    draggedItem = null;
                });

                item.addEventListener('dragover', (e) => {
                    e.preventDefault();
                    if (!draggedItem || draggedItem === item) return;
                    
                    const rect = item.getBoundingClientRect();
                    const midY = rect.top + rect.height / 2;
                    
                    if (e.clientY < midY) {
                        item.parentElement.insertBefore(draggedItem, item);
                    } else {
                        item.parentElement.insertBefore(draggedItem, item.nextSibling);
                    }
                });

                item.addEventListener('drop', (e) => {
                    e.preventDefault();
                    // Update column order based on DOM order
                    const list = dropdown.querySelector('.column-picker-list[data-group="visible"]');
                    if (list) {
                        const newOrder = Array.from(list.querySelectorAll('.column-picker-item')).map(el => el.dataset.columnId);
                        newOrder.forEach((colId, idx) => {
                            const col = this.columns.find(c => c.id === colId);
                            if (col) col.order = idx;
                        });
                        this.columns.sort((a, b) => a.order - b.order);
                        this._saveConfig();
                        this.options.onColumnChange?.(this.columns);
                    }
                });
            });
        }

        /**
         * Bind header events for sorting and resizing
         */
        bindHeaderEvents(theadElement) {
            if (!theadElement) return;

            // Sort on click
            theadElement.addEventListener('click', (e) => {
                const th = e.target.closest('th[data-sort-key]');
                if (!th || !th.dataset.sortKey) return;
                
                // Don't sort if clicking resize handle
                if (e.target.closest('.column-resize-handle')) return;
                
                const key = th.dataset.sortKey;
                const currentDir = this.sortState.key === key ? this.sortState.dir : null;
                const newDir = currentDir === 'asc' ? 'desc' : 'asc';
                
                this.setSort(key, newDir, e.shiftKey);
            });

            // Column resize
            if (this.options.enableResize) {
                this._setupColumnResize(theadElement);
            }
        }

        _setupColumnResize(theadElement) {
            const handles = theadElement.querySelectorAll('.column-resize-handle');
            
            handles.forEach(handle => {
                handle.addEventListener('mousedown', (e) => {
                    e.preventDefault();
                    const th = handle.closest('th');
                    const columnId = handle.dataset.columnId;
                    const startX = e.pageX;
                    const startWidth = th.offsetWidth;
                    
                    this.resizeState = { columnId, startX, startWidth };
                    document.body.style.cursor = 'col-resize';
                    document.body.style.userSelect = 'none';
                    
                    const onMouseMove = (e) => {
                        if (!this.resizeState) return;
                        const diff = e.pageX - this.resizeState.startX;
                        const newWidth = Math.max(50, this.resizeState.startWidth + diff);
                        th.style.width = `${newWidth}px`;
                    };
                    
                    const onMouseUp = () => {
                        if (this.resizeState) {
                            const th = theadElement.querySelector(`th[data-column-id="${this.resizeState.columnId}"]`);
                            if (th) {
                                this.resizeColumn(this.resizeState.columnId, th.offsetWidth);
                            }
                        }
                        this.resizeState = null;
                        document.body.style.cursor = '';
                        document.body.style.userSelect = '';
                        document.removeEventListener('mousemove', onMouseMove);
                        document.removeEventListener('mouseup', onMouseUp);
                    };
                    
                    document.addEventListener('mousemove', onMouseMove);
                    document.addEventListener('mouseup', onMouseUp);
                });
            });
        }
    }

    // Helper function
    function escapeHtml(str) {
        if (str === null || str === undefined) return '';
        return String(str)
            .replace(/&/g, '&amp;')
            .replace(/</g, '&lt;')
            .replace(/>/g, '&gt;')
            .replace(/"/g, '&quot;')
            .replace(/'/g, '&#39;');
    }

    // Export to global scope
    window.TableCustomizer = TableCustomizer;
    window.DEVICES_COLUMN_DEFINITIONS = DEVICES_COLUMN_DEFINITIONS;
    window.AGENTS_COLUMN_DEFINITIONS = AGENTS_COLUMN_DEFINITIONS;

})();
