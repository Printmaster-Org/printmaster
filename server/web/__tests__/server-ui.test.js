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
        console: { error: jest.fn() },
        TypeError,
        ...globals,
    });
    names.forEach(name => vm.runInContext(functionSource(name), context));
    return { context, document: dom.window.document };
}

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
                    'renderDeviceControls', 'bindDeviceControls', ...extraNames,
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

            test('explicit checkbox toggles multi-selection without modifiers; Details is a native button', () => {
                const { context, document } = deviceSetup();
                context.window.__pm_shared.showPrinterDetails = jest.fn();
                const container = document.getElementById('devices_cards');
                container.innerHTML = ['A', 'B'].map(serial =>
                    `<div class="device-card-clickable" data-serial="${serial}">${context.renderDeviceControls({ serial })}</div>`
                ).join('');
                context.bindDeviceControls(container);
                context.bindDeviceControls(container);
                const checkboxes = container.querySelectorAll('input');
                checkboxes[0].click();
                checkboxes[1].click();
                expect([...context.devicesVM.selection.selectedIds]).toEqual(['A', 'B']);
                expect(checkboxes[0].checked).toBe(true);
                checkboxes[0].click();
                expect([...context.devicesVM.selection.selectedIds]).toEqual(['B']);
                container.querySelector('button').click();
                expect(context.window.__pm_shared.showPrinterDetails).toHaveBeenCalledTimes(1);
                expect(context.window.__pm_shared.showPrinterDetails).toHaveBeenCalledWith('A', 'saved');
                expect(checkboxes[1].getAttribute('aria-label')).toBe('Select device B');
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

            test('table controls remain available with customized columns and progressive pages', () => {
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
                expect(document.getElementById('devices_table_header').children).toHaveLength(2);
                expect(document.querySelector('#devices_table_header > th').classList.contains('device-controls-cell')).toBe(true);
                context.renderDeviceTable([{ serial: 'A', ip: '10.0.0.1' }, { serial: 'B', ip: '10.0.0.2' }]);
                expect(document.querySelectorAll('.device-row-clickable')).toHaveLength(1);
                expect(document.querySelector('.device-row-clickable [data-device-details]')).not.toBeNull();
                expect(document.querySelector('.device-row-clickable > td').classList.contains('device-controls-cell')).toBe(true);
                expect(document.querySelector('#devices_load_more_sentinel td').colSpan).toBe(2);
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

            test('release channel is prominent, unique, and respects Features ownership', () => {
                const field = { path: 'features.agent_update_channel', type: 'select' };
                const state = {
                    schema: {}, globalDraft: { features: { agent_update_channel: 'dev' } },
                    scope: 'global', groupedFields: { features: [field] }, managedSections: new Set(['features'])
                };
                const renderRow = jest.fn((meta, value) => {
                    const row = document.createElement('div');
                    row.dataset.settingsPath = meta.path;
                    row.textContent = value;
                    return row;
                });
                const { context, document } = setup(['renderSettingsForm'], {
                    settingsUIState: state, renderManagedSectionsPanel: () => null,
                    renderSettingsFieldRow: renderRow, orderedSettingsSections: () => ['features'],
                    getValueByPath: (draft) => draft.features.agent_update_channel,
                    SETTINGS_SECTION_LABELS: { features: 'Features' }, escapeHtml: value => value,
                    refreshPolicyPanel: jest.fn()
                });
                context.renderSettingsForm();
                const root = document.getElementById('settings_form_root');
                expect(root.firstElementChild.id).toBe('fleet_update_channel_section');
                expect(root.querySelectorAll('[data-settings-path="features.agent_update_channel"]')).toHaveLength(1);
                expect(renderRow).toHaveBeenLastCalledWith(field, 'dev', 'global', true, false);
                state.managedSections.clear();
                context.renderSettingsForm();
                expect(renderRow).toHaveBeenLastCalledWith(field, 'dev', 'global', false, false);
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
