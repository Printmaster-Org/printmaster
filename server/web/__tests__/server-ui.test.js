const fs = require('fs');
const path = require('path');
const vm = require('vm');
const { JSDOM } = require('jsdom');

const source = fs.readFileSync(path.join(__dirname, '..', 'app.js'), 'utf8');
const html = fs.readFileSync(path.join(__dirname, '..', 'index.html'), 'utf8');

function functionSource(name) {
    const start = source.search(new RegExp(`^(?:async )?function ${name}\\(`, 'm'));
    const rest = source.slice(start);
    const end = rest.slice(1).search(/^(?:async )?function \w+\(/m);
    return end < 0 ? rest : rest.slice(0, end + 1);
}

function setup(names, globals = {}) {
    const dom = new JSDOM(html);
    dom.window.document.querySelectorAll('template[id^="tab-template-"]').forEach(template => {
        template.replaceWith(template.content);
    });
    const context = vm.createContext({
        document: dom.window.document,
        window: { __pm_shared: { error: jest.fn() } },
        console: { error: jest.fn(), warn: jest.fn() },
        TypeError,
        ...globals,
    });
    names.forEach(name => vm.runInContext(functionSource(name), context));
    return { context, document: dom.window.document };
}

describe('device status tag filtering', () => {
    const keys = ['healthy', 'warning', 'error', 'jam', 'offline', 'unknown'];
    const escapeHtml = value => String(value);

    function statusSetup(view = 'table') {
        const devicesVM = {
            loaded: true, uiInitialized: false, view,
            filters: {
                statuses: new Set(), consumables: new Set(['high']), query: '',
                agentId: '', tenantId: '', manufacturer: '', sortKey: 'last_seen', sortDir: 'desc',
            },
            items: keys.slice(0, -1).map(code => ({
                serial: code, agent_id: 'agent-1', manufacturer: 'Epson',
                __meta: { status: { code }, search: `${code} epson`, tenantId: 'tenant-1' },
            })),
            stats: {}, render: { displayed: 0, pageSize: 30 },
        };
        const { context, document } = setup([
            'initDevicesUI', 'applyDeviceFilters', 'matchesDeviceFilters', 'createStatusCountMap',
            'toggleStatusFilter', 'renderDevicesStats', 'renderDevicesActiveFilters',
            'buildFilterChip', 'handleFilterChipRemove', 'syncDeviceQuickFilters', 'resetDeviceFilters',
        ], {
            devicesVM, DEVICE_STATUS_KEYS: keys, DEVICE_CONSUMABLE_KEYS: ['high'],
            devicesLoader: null, formatNumber: String, escapeHtml,
            hasDeviceSupplyFilter: () => false, sortDevices: list => list,
            initDevicesTableCustomizer: jest.fn(), initFilterDrawer: jest.fn(), debounce: fn => fn,
            syncDevicesViewToggle: jest.fn(), syncDeviceSortControls: jest.fn(),
            syncDevicesAgentFilterOptions: jest.fn(), refreshDeviceFilters: jest.fn(),
            renderDevicesOverview: jest.fn(), syncTenantFilterOptions: jest.fn(),
            renderDeviceTable: jest.fn(), renderDeviceCards: jest.fn(),
            updateDeviceSelectionUI: jest.fn(), renderDeviceLoadingStatus: jest.fn(),
            syncDeviceTableSortIndicators: jest.fn(),
            getAgentInfo: () => null, formatTenantDisplay: id => id,
        });
        context.initDevicesUI();
        context.applyDeviceFilters();
        const tag = key => document.querySelector(`#devices_stats [data-status="${key}"]`);
        return { context, document, devicesVM, tag };
    }

    test.each(['table', 'cards'])('%s view selects, combines and deselects tags through actual controls', view => {
        const { context, document, devicesVM, tag } = statusSetup(view);
        const visible = () => devicesVM.filtered.map(device => device.serial);
        expect(visible()).toEqual(keys.slice(0, -1));
        expect(tag('warning').getAttribute('aria-pressed')).toBe('false');
        tag('warning').focus();
        tag('warning').click();
        expect(visible()).toEqual(['warning']);
        expect(document.activeElement).toBe(tag('warning'));
        expect(tag('warning').getAttribute('aria-pressed')).toBe('true');
        expect(document.querySelector('#devices_status_filter [data-status="warning"]').classList.contains('active')).toBe(true);
        tag('error').click();
        expect(visible()).toEqual(['warning', 'error']);
        expect(tag('healthy').textContent).toBe('healthy 1');
        tag('warning').click();
        expect(visible()).toEqual(['error']);
        tag('error').click();
        expect(visible()).toEqual(keys.slice(0, -1));
        expect(document.querySelector('[data-clear-statuses]').disabled).toBe(true);
        expect(context[view === 'table' ? 'renderDeviceTable' : 'renderDeviceCards'])
            .toHaveBeenLastCalledWith(devicesVM.filtered);
    });

    test('sidebar shares selection and clear preserves unrelated filters', () => {
        const { context, document, devicesVM, tag } = statusSetup();
        Object.assign(devicesVM.filters, {
            query: 'epson', agentId: 'agent-1', tenantId: 'tenant-1', manufacturer: 'Epson',
        });
        document.querySelector('#devices_status_filter [data-status="jam"]').click();
        expect(devicesVM.filtered.map(device => device.serial)).toEqual(['jam']);
        expect(tag('jam').getAttribute('aria-pressed')).toBe('true');
        tag('warning').click();
        document.querySelector('[data-clear-statuses]').click();
        expect(devicesVM.filters.statuses.size).toBe(0);
        expect(devicesVM.filters).toMatchObject({
            query: 'epson', agentId: 'agent-1', tenantId: 'tenant-1', manufacturer: 'Epson',
        });
        expect(devicesVM.filtered).toHaveLength(5);
        tag('error').click();
        context.handleFilterChipRemove('statuses');
        expect(devicesVM.filters.statuses.size).toBe(0);
        tag('error').click();
        context.resetDeviceFilters();
        expect(devicesVM.filters.statuses.size).toBe(0);
        expect(devicesVM.filters.query).toBe('');
    });

    test('zero-count tags, all tags and invalid statuses behave consistently', () => {
        const { context, document, devicesVM, tag } = statusSetup();
        tag('unknown').click();
        expect(devicesVM.filtered).toHaveLength(0);
        expect(tag('unknown').getAttribute('aria-pressed')).toBe('true');
        keys.slice(0, -1).forEach(key => tag(key).click());
        expect(devicesVM.filtered).toHaveLength(5);
        expect(document.querySelector('#devices_active_filters [data-filter="statuses"]')).not.toBeNull();
        context.toggleStatusFilter('invalid');
        expect(devicesVM.filters.statuses.size).toBe(keys.length);
        keys.forEach(key => tag(key).click());
        expect(devicesVM.filters.statuses.size).toBe(0);
        expect(devicesVM.filtered).toHaveLength(5);
    });
});

describe('Server device details ownership', () => {
    test('shared Details action uses scoped full rows, latest metrics, and saved state', async () => {
        const showDetails = jest.fn();
        const fetchDeviceInventory = jest.fn().mockImplementation(async kind => kind === 'rows'
            ? [{ serial: 'SAVED', agent_id: 'agent-1', is_saved: true, raw_data: { raw_data: { toner_desc_black: 'Black Ink ABC' } } }]
            : [{ serial: 'SAVED', toner_levels: { toner_black: 25 } }]);
        const deleteDevice = jest.fn();
        const { context } = setup(['showPrinterDetails'], {
            devicesVM: { items: [{ serial: 'SAVED', ip: '192.0.2.10' }] },
            devicesLoader: { getGeneration: () => 1 },
            fetchDeviceInventory, deleteDevice,
            window: { __pm_shared: { showToast: jest.fn() }, __pm_shared_cards: { showPrinterDetailsData: showDetails } }
        });
        expect(context.window.__pm_shared.showPrinterDetails).toBe(context.showPrinterDetails);
        await context.window.__pm_shared.showPrinterDetails('SAVED', 'discovered');
        expect(fetchDeviceInventory.mock.calls.map(call => call[0])).toEqual(['rows', 'metrics/query']);
        const [device, source, debug, options] = showDetails.mock.calls[0];
        expect(device.is_saved).toBe(true);
        expect(device.toner_levels).toEqual({ toner_black: 25 });
        expect(device.raw_data.raw_data.toner_desc_black).toBe('Black Ink ABC');
        expect(options).toMatchObject({ server: true, deleteDevice });
    });
});

describe('Agent details and update entry points', () => {
    const escapeHtml = value => String(value).replaceAll('&', '&amp;').replaceAll('"', '&quot;').replaceAll('<', '&lt;').replaceAll('>', '&gt;');

    test('details show reported Windows build, safe text, enrollment age, and collapsed diagnostics', () => {
        const attachName = jest.fn();
        const attachUpdate = jest.fn();
        const { context, document } = setup(['getAgentPlatformLabel', 'renderAgentDetailsModal'], {
            escapeHtml, getAgentDisplayName: agent => agent.name,
            _attachAgentDetailsNameEditor: attachName, _attachAgentUpdateHandler: attachUpdate,
            window: { __pm_shared: { renderCopyButton: () => '' } },
        });
        const agent = {
            agent_id: 'agent-1', name: '<img src=x onerror=alert(1)>', hostname: 'PC',
            platform: 'windows', os_version: 'Windows 11 (24H2) (build 26100)',
            status: 'active', connection_type: 'ws', version: '0.32.0-beta.2',
            registered_at: new Date(Date.now() - 3 * 86400000).toISOString(),
        };
        context.renderAgentDetailsModal(agent);
        const body = document.getElementById('agent_details_body');
        expect(body.textContent).toContain('Windows 11 (24H2) (build 26100)');
        expect(body.textContent).toContain(agent.name);
        expect(body.querySelector('img')).toBeNull();
        expect(body.textContent).toContain('Enrolled');
        expect(body.textContent).not.toContain('Uptime');
        expect(body.querySelector('details').open).toBe(false);
        expect(body.querySelector('.agent-details-overview #agent_check_update_btn').disabled).toBe(false);
        expect(attachName).toHaveBeenCalledWith(agent);
        expect(attachUpdate).toHaveBeenCalledWith(agent);
    });

    test('offline agents have disabled commands and missing metadata is explicit', () => {
        const { context, document } = setup(['getAgentPlatformLabel', 'renderAgentDetailsModal'], {
            escapeHtml, getAgentDisplayName: () => 'Offline PC',
            _attachAgentDetailsNameEditor: jest.fn(), _attachAgentUpdateHandler: jest.fn(),
            window: { __pm_shared: { renderCopyButton: () => '' } },
        });
        context.renderAgentDetailsModal({ agent_id: 'agent-1', platform: 'windows', status: 'offline' });
        expect(document.getElementById('agent_details_body').textContent).toContain('Not reported by agent');
        expect(document.getElementById('agent_check_update_btn').disabled).toBe(true);
        expect(document.getElementById('agent_force_update_btn').disabled).toBe(true);
    });

    test('inline update respects policy without a picker; context update asks once and sends selection', async () => {
        const showAgentUpdateChannelConfirm = jest.fn().mockResolvedValue('beta');
        const fetch = jest.fn().mockResolvedValue({ ok: true, json: async () => ({ success: true }) });
        const { context } = setup(['updateAgent'], {
            showAgentUpdateChannelConfirm, fetch, agentsVM: { updateState: {}, latestVersion: '0.32.0' },
            refreshAgentVersionCell: jest.fn(), startUpdateProgressAnimation: jest.fn(),
        });
        await context.updateAgent('agent-1');
        expect(showAgentUpdateChannelConfirm).not.toHaveBeenCalled();
        expect(JSON.parse(fetch.mock.calls[0][1].body)).toEqual({ command: 'check_update' });
        await context.updateAgent('agent-1', { chooseChannel: true });
        expect(showAgentUpdateChannelConfirm).toHaveBeenCalledTimes(1);
        expect(JSON.parse(fetch.mock.calls[1][1].body)).toEqual({
            command: 'install_channel', data: { channel: 'beta', reason: 'server_ui_channel_install' },
        });
        showAgentUpdateChannelConfirm.mockResolvedValueOnce(null);
        await context.updateAgent('agent-1', { chooseChannel: true });
        expect(fetch).toHaveBeenCalledTimes(2);
    });

    test('update picker focuses its select and Escape cancels without dispatch', async () => {
        const { context, document } = setup(['showAgentUpdateChannelConfirm']);
        const pending = context.showAgentUpdateChannelConfirm();
        expect(document.activeElement.id).toBe('agent_update_channel');
        const event = new document.defaultView.KeyboardEvent('keydown', { key: 'Escape', bubbles: true });
        document.activeElement.dispatchEvent(event);
        expect(await pending).toBeNull();
        expect(document.getElementById('agent_update_channel')).toBeNull();
    });
});

describe('alert summary data states', () => {
    test('severity and scope counts are alert counts, not fleet health percentages', async () => {
        const fetch = jest.fn().mockResolvedValue({ ok: true, json: async () => ({
            active_count: 8, critical_count: 2, warning_count: 3, info_count: 3,
            offline_counts: { agents: 4 }, alerts_by_scope: { device: 7, agent: 1 },
        }) });
        const { context, document } = setup(['loadAlertSummary'], {
            fetch, alertSummaryLastUpdated: null, loadRecentAlertsPreview: jest.fn(),
        });

        await context.loadAlertSummary();
        expect(document.getElementById('summary_healthy_count').textContent).toBe('3');
        expect(document.getElementById('summary_offline_count').textContent).toBe('4');
        expect(document.getElementById('scope_count_devices').textContent).toBe('7 alerts');
        expect(document.querySelector('.scope-bar')).toBeNull();
        expect(document.getElementById('summary_no_active_alerts').hidden).toBe(true);
        expect(document.getElementById('alert_summary_status').textContent).toMatch(/Last updated/);
    });

    test('first failure stays unavailable, successful retry shows no alerts, later failure is stale', async () => {
        const fetch = jest.fn().mockRejectedValue(new Error('offline'));
        const { context, document } = setup(['loadAlertSummary'], {
            fetch, alertSummaryLastUpdated: null, loadRecentAlertsPreview: jest.fn(),
        });
        await context.loadAlertSummary();
        expect(document.getElementById('summary_critical_count').textContent).toBe('—');
        expect(document.getElementById('alert_summary_status').textContent).toMatch(/unavailable/);
        const retry = document.getElementById('alert_summary_retry');
        expect(retry.hidden).toBe(false);
        fetch.mockResolvedValueOnce({ ok: true, json: async () => ({ active_count: 0 }) });
        retry.click();
        await new Promise(resolve => setImmediate(resolve));
        expect(document.getElementById('summary_no_active_alerts').hidden).toBe(false);
        expect(retry.hidden).toBe(true);
        await context.loadAlertSummary();
        expect(document.getElementById('alert_summary_status').textContent).toMatch(/stale.*Last updated/);
        expect(retry.hidden).toBe(false);
        expect(context.console.error).toHaveBeenCalledTimes(2);
    });
});

describe('server settings recovery', () => {
    function settingsSetup(fetch) {
        const state = { loading: false, data: { server: {} }, original: {} };
        const result = setup(['loadServerSettings'], {
            fetch, serverSettingsVM: state,
            normalizeServerSettings: data => data, cloneServerSettingsData: data => data,
            renderServerSettingsForm: jest.fn(),
        });
        result.document.getElementById('server_settings_container').innerHTML =
            '<input id="draft" value="unsaved"><button id="server_settings_save_btn">Save</button>';
        return { ...result, state };
    }

    describe('device selection and details', () => {
            function deviceSetup(extraNames = [], globals = {}) {
                return setup([
                    'updateDeviceSelectionUI', 'handleDeviceSelection', 'reconcileDeviceSelection',
                    ...extraNames,
                ], {
                    devicesVM: {
                        selection: { selectedIds: new Set(), lastSelected: null },
                        filtered: [{ serial: 'A' }, { serial: 'B' }],
                        render: { displayed: 0, pageSize: 1 },
                    },
                    escapeHtml: value => String(value).replaceAll('&', '&amp;').replaceAll('"', '&quot;').replaceAll('<', '&lt;'),
                    ...globals,
                });
            }

            test('click, Ctrl-click, and Shift-click select devices without extra controls', () => {
                const { context, document } = deviceSetup();
                context.window.__pm_shared.showPrinterDetails = jest.fn();
                const container = document.getElementById('devices_cards');
                container.innerHTML = ['A', 'B'].map(serial =>
                    `<div class="device-card-clickable" data-serial="${serial}">${serial}</div>`
                ).join('');
                context.handleDeviceSelection('A', {});
                context.handleDeviceSelection('B', { ctrlKey: true });
                expect([...context.devicesVM.selection.selectedIds]).toEqual(['A', 'B']);
                expect(container.firstElementChild.classList.contains('device-card-selected')).toBe(true);
                context.handleDeviceSelection('A', { ctrlKey: true });
                expect([...context.devicesVM.selection.selectedIds]).toEqual(['B']);
                context.handleDeviceSelection('B', {});
                context.handleDeviceSelection('A', { shiftKey: true });
                expect([...context.devicesVM.selection.selectedIds]).toEqual(['A', 'B']);
                expect(container.querySelectorAll('input, button')).toHaveLength(0);
            });

            test('refresh reset/failure preserves selection; ready authorized index prunes only absent IDs', () => {
                let onChange;
                const loader = {
                    getIndexState: () => ({ status: 'ready' }),
                    getIndex: () => [{ serial: 'A' }, { serial: 'C' }],
                };
                const { context } = deviceSetup(['initDevicesLoader'], {
                    devicesLoader: null, deviceSupplyBands: new Map(), devicesDemand: [], devicesPaintFrame: null,
                    window: { PrintMasterProgressive: { createLoader: options => { onChange = options.onChange; return loader; } } },
                    cleanupDevicesInfiniteScroll: jest.fn(), renderDevicesOverview: jest.fn(),
                    renderDeviceLoadingStatus: jest.fn(), refreshDeviceFilters: jest.fn(),
                    syncDevicesAgentFilterOptions: jest.fn(), applyDeviceFilters: jest.fn(),
                    enrichDevices: items => items, renderDevicesError: jest.fn(),
                });
                context.devicesVM.selection.selectedIds = new Set(['A', 'B']);
                context.devicesVM.selection.lastSelected = 'B';
                context.initDevicesLoader();
                onChange({ type: 'reset' });
                expect([...context.devicesVM.selection.selectedIds]).toEqual(['A', 'B']);
                loader.getIndexState = () => ({ status: 'error', error: 'offline' });
                onChange({ type: 'index' });
                expect([...context.devicesVM.selection.selectedIds]).toEqual(['A', 'B']);
                loader.getIndexState = () => ({ status: 'ready' });
                onChange({ type: 'index' });
                expect([...context.devicesVM.selection.selectedIds]).toEqual(['A']);
                expect(context.devicesVM.selection.lastSelected).toBeNull();
            });

            test('table uses only customized data columns and preserves progressive pages', () => {
                const { context, document } = deviceSetup(['renderDevicesTableHeader', 'renderDeviceTable'], {
                    initTableScrollIndicators: jest.fn(), setupDevicesInfiniteScroll: jest.fn(),
                    getProgressiveDevice: device => device, loadMoreDevices: jest.fn(),
                });
                context.devicesVM.tableCustomizer = {
                    renderHeader: () => '<th data-column-id="network">Network</th>',
                    bindHeaderEvents: jest.fn(), getVisibleColumns: () => ['network'],
                    renderRow: device => `<td data-column-id="network">${device.ip}</td>`,
                };
                context.renderDevicesTableHeader();
                expect(document.getElementById('devices_table_header').children).toHaveLength(1);
                expect(document.querySelector('#devices_table_header > th').dataset.columnId).toBe('network');
                context.renderDeviceTable([{ serial: 'A', ip: '10.0.0.1' }, { serial: 'B', ip: '10.0.0.2' }]);
                expect(document.querySelectorAll('.device-row-clickable')).toHaveLength(1);
                expect(document.querySelector('.device-row-clickable [data-device-details]')).toBeNull();
                expect(document.querySelector('.device-row-clickable > td').dataset.columnId).toBe('network');
                expect(document.querySelector('#devices_load_more_sentinel td').colSpan).toBe(1);
                expect(context.devicesVM.render.displayed).toBe(1);
            });
        });
    test('5xx retains disabled snapshot and retry, then recovers with fresh lock metadata', async () => {
        const fetch = jest.fn().mockResolvedValue({ ok: false, status: 503 });
        const { context, document } = settingsSetup(fetch);
        const input = document.getElementById('draft');
        await context.loadServerSettings();
        expect(document.getElementById('draft')).toBe(input);
        expect(input.closest('fieldset').disabled).toBe(true);
        expect(document.getElementById('server_settings_load_error').textContent).toMatch(/stale and read-only/);
        fetch.mockResolvedValue({ ok: true, json: async () => ({ locked_keys: ['server.port'] }) });
        document.querySelector('#server_settings_load_error button').click();
        await new Promise(resolve => setImmediate(resolve));
        expect(context.renderServerSettingsForm).toHaveBeenCalledTimes(1);
        expect(context.serverSettingsVM.lockedKeys.has('server.port')).toBe(true);
    });

    test.each([401, 403])('authorization failure %s discards prior settings', async status => {
        const { context, document, state } = settingsSetup(jest.fn().mockResolvedValue({ ok: false, status }));
        await context.loadServerSettings();
        expect(document.getElementById('draft')).toBeNull();
        expect(state.data).toBeNull();
        expect(document.getElementById('server_settings_load_error').textContent).toMatch(/unavailable/);
    });

    test('authorization failure takes priority over a concurrent service failure', async () => {
        const fetch = jest.fn().mockResolvedValueOnce({ ok: false, status: 503 })
            .mockResolvedValueOnce({ ok: false, status: 403 });
        const { context, document, state } = settingsSetup(fetch);
        await context.loadServerSettings();
        expect(document.getElementById('draft')).toBeNull();
        expect(state.data).toBeNull();
    });

    test('missing lock metadata never renders an editable form', async () => {
        const { context, document } = settingsSetup(jest.fn().mockResolvedValue({
            ok: true, json: async () => ({}),
        }));
        await context.loadServerSettings();
        expect(context.renderServerSettingsForm).not.toHaveBeenCalled();
        expect(document.getElementById('draft')).toBeNull();
    });

    test('lock metadata failure does not enable the old form', async () => {
        const fetch = jest.fn().mockResolvedValueOnce({ ok: true, json: async () => ({ server: {} }) })
            .mockResolvedValueOnce({ ok: false, status: 503 });
        const { context, document } = settingsSetup(fetch);
        await context.loadServerSettings();
        expect(context.renderServerSettingsForm).not.toHaveBeenCalled();
        expect(document.getElementById('draft').closest('fieldset').disabled).toBe(true);
    });
});

describe('server control accessibility', () => {
    test('theme, report range and modal close controls have accessible names and native semantics', () => {
        const { document } = setup([]);
        expect(document.getElementById('theme-toggle-checkbox').getAttribute('aria-label')).toBeTruthy();
        expect(document.querySelector('label[for="usage_report_range"]')).not.toBeNull();
        document.querySelectorAll('.modal-close').forEach(close => {
            expect(close.tagName).toBe('BUTTON');
            expect(close.type).toBe('button');
            expect(close.getAttribute('aria-label')).toBeTruthy();
        });

    });

        describe('fleet channel and SNMP controls', () => {
            test('channel options are explicit and SNMP passwords are masked', () => {
                const { context } = setup(['createInputForField'], {
                    resolveFieldValue: (field, value) => value ?? field.default
                });
                const { input } = context.createInputForField({
                    path: 'features.agent_update_channel', type: 'select',
                    enum: ['', 'stable', 'beta', 'dev'], default: ''
                }, 'dev');
                expect(Array.from(input.options).map(option => option.textContent)).toEqual([
                    'Use Agent configuration', 'Stable', 'Beta', 'Dev'
                ]);
                expect(input.value).toBe('dev');
                expect(context.createInputForField({ path: 'snmp.auth_password', type: 'password' }, 'secret').input.type).toBe('password');
            });

            test('unmanaged fleet fields are read-only at global, customer and Agent scope', () => {
                const { context } = setup(['renderSettingsFieldRow', 'createInputForField'], {
                    settingsUIState: {
                        lockedKeys: new Set(), selectedTenantId: 'tenant-1', selectedAgentId: 'agent-1'
                    },
                    resolveFieldValue: (field, value) => value,
                    escapeHtml: value => value, userCan: () => true,
                    hasOverride: () => false, pathToArray: path => path.split('.')
                });
                const field = { path: 'features.agent_update_channel', type: 'select', enum: ['', 'stable', 'beta', 'dev'] };
                for (const scope of ['global', 'tenant', 'agent']) {
                    expect(context.renderSettingsFieldRow(field, 'dev', scope, false).querySelector('select').disabled).toBe(true);
                    expect(context.renderSettingsFieldRow(field, 'dev', scope, true).querySelector('select').disabled).toBe(false);
                }
            });

            test.each(['global', 'tenant', 'agent'])('release channel is inside Agent Updates at %s scope and respects ownership', scope => {
                const field = { path: 'features.agent_update_channel', type: 'select', enum: ['', 'stable', 'beta', 'dev'] };
                const state = {
                    schema: {}, globalDraft: { features: { agent_update_channel: 'dev' } },
                    tenantDraft: { features: { agent_update_channel: 'beta' } },
                    agentDraft: { features: { agent_update_channel: 'stable' } },
                    scope, groupedFields: { features: [field] }, managedSections: new Set(['features']),
                    agentEnforcedSections: new Set(), lockedKeys: new Set(),
                    selectedTenantId: 'tenant-1', selectedAgentId: 'agent-1'
                };
                const { context, document } = setup([
                    'getSettingsDraft', 'renderSettingsForm', 'refreshPolicyPanel', 'renderUpdatePolicySection',
                    'renderSettingsFieldRow', 'createInputForField'
                ], {
                    settingsUIState: state, renderManagedSectionsPanel: () => null,
                    renderTenantEnforcementPanel: () => null, orderedSettingsSections: () => ['features'],
                    getValueByPath: (draft) => draft.features.agent_update_channel,
                    SETTINGS_SECTION_LABELS: { features: 'Features' }, escapeHtml: value => value,
                    resolveFieldValue: (field, value) => value, userCan: () => true,
                    hasOverride: () => scope !== 'global', pathToArray: path => path.split('.'),
                    getSettingsPayload: () => ({}), getPolicyState: () => ({ loaded: true, enabled: false })
                });
                context.renderSettingsForm();
                const root = document.getElementById('settings_form_root');
                const panel = root.querySelector('#auto_update_policy_section');
                expect(panel.querySelector('h4').textContent).toBe('Agent Updates');
                expect(root.querySelector('#fleet_update_channel_section')).toBeNull();
                const channel = () => panel.querySelector('select[data-settings-path="features.agent_update_channel"]');
                expect(root.querySelectorAll('select[data-settings-path="features.agent_update_channel"]')).toHaveLength(1);
                expect(channel().value).toBe({ global: 'dev', tenant: 'beta', agent: 'stable' }[scope]);
                expect(channel().disabled).toBe(false);
                if (scope !== 'global') expect(panel.querySelector('.inherit-btn').textContent).toBe('Inherit');
                context.refreshPolicyPanel();
                expect(root.querySelectorAll('select[data-settings-path="features.agent_update_channel"]')).toHaveLength(1);
                if (scope === 'agent') {
                    state.agentEnforcedSections.add('features');
                    context.renderSettingsForm();
                    expect(root.querySelector('#auto_update_policy_section select').disabled).toBe(true);
                    expect(root.querySelector('#auto_update_policy_section .inherit-btn')).toBeNull();
                }
                state.managedSections.clear();
                context.renderSettingsForm();
                expect(root.querySelector('#auto_update_policy_section select').disabled).toBe(true);
                expect(root.querySelector('#auto_update_policy_section').textContent).toContain('Section Management');
            });

            test.each(['global', 'tenant', 'agent'])('Save Changes persists the channel at %s scope', async scope => {
                const overrides = { features: { agent_update_channel: 'beta' } };
                const fetchJSON = jest.fn().mockResolvedValue({});
                const state = {
                    globalDirty: true, globalSettingsDirty: true, globalDraft: overrides,
                    managedSections: new Set(['features']),
                    tenantDirty: true, tenantSettingsDirty: true, tenantOverridesDraft: overrides,
                    tenantEnforcedSections: new Set(), selectedTenantId: 'tenant-1',
                    agentDirty: true, agentOverridesDraft: overrides,
                    agentEnforcedSections: new Set(), selectedAgentId: 'agent-1'
                };
                const { context } = setup(['saveGlobalSettings', 'saveTenantSettings', 'saveAgentSettings'], {
                    settingsUIState: state, fetchJSON, getPolicyState: () => ({ dirty: false }),
                    cloneSettings: value => JSON.parse(JSON.stringify(value)),
                    flattenOverrides: () => ['features.agent_update_channel'],
                    loadGlobalSettingsSnapshot: jest.fn(), loadTenantSnapshot: jest.fn(),
                    loadAgentSnapshot: jest.fn(), renderSettingsUI: jest.fn(),
                    window: { __pm_shared: { showToast: jest.fn() } }
                });
                await context[{ global: 'saveGlobalSettings', tenant: 'saveTenantSettings', agent: 'saveAgentSettings' }[scope]]();
                expect(fetchJSON).toHaveBeenCalledTimes(1);
                const [endpoint, request] = fetchJSON.mock.calls[0];
                expect(endpoint).toBe({
                    global: '/api/v1/settings/global',
                    tenant: '/api/v1/settings/tenants/tenant-1',
                    agent: '/api/v1/settings/agents/agent-1'
                }[scope]);
                expect(request.method).toBe('PUT');
                const payload = JSON.parse(request.body);
                expect((scope === 'tenant' ? payload.overrides : payload).features.agent_update_channel).toBe('beta');
            });
        });

    test('reopening login replaces the Enter handler and pending submissions are guarded', async () => {
        let resolveFetch;
        const fetch = jest.fn(() => new Promise(resolve => { resolveFetch = resolve; }));
        const { context, document } = setup(['showLoginModal'], { fetch });
        context.showLoginModal();
        context.showLoginModal();
        const password = document.getElementById('login_password');
        const pressEnter = () => password.dispatchEvent(
            new document.defaultView.KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true })
        );
        pressEnter();
        pressEnter();
        document.getElementById('login_submit').click();
        expect(fetch).toHaveBeenCalledTimes(1);
        expect(document.getElementById('login_submit').disabled).toBe(true);
        resolveFetch({ ok: false, text: async () => 'Invalid credentials' });
        await new Promise(resolve => setImmediate(resolve));
        expect(document.getElementById('login_submit').disabled).toBe(false);
        pressEnter();
        expect(fetch).toHaveBeenCalledTimes(2);
    });
});
