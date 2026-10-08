const { test, expect } = require('@playwright/test');
const http = require('http');
const fs = require('fs');
const path = require('path');
const { inventoryResponse } = require('./inventory-fixture');
const serverWeb = path.resolve(__dirname, '../../../../server/web');
const commonWeb = path.resolve(__dirname, '../..');
let server, baseURL;
test.beforeAll(async () => {
    server = http.createServer((req, res) => {
        let pathname = decodeURIComponent(new URL(req.url, 'http://localhost').pathname).split('{{')[0];
        let file;
        if (pathname === '/app' || pathname === '/') file = path.join(serverWeb, 'index.html');
        else if (pathname.startsWith('/static/')) {
            const relative = pathname.slice(8);
            file = path.join(serverWeb, relative);
            if (!fs.existsSync(file)) file = path.join(commonWeb, relative);
        }
        if (!file || !fs.existsSync(file)) { res.writeHead(404); res.end(); return; }
        res.setHeader('Content-Type', file.endsWith('.html') ? 'text/html' : file.endsWith('.css') ? 'text/css' : 'application/javascript');
        res.end(fs.readFileSync(file));
    });
    await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
    baseURL = `http://127.0.0.1:${server.address().port}`;
});
test.afterAll(async () => { server.closeAllConnections(); await new Promise(resolve => server.close(resolve)); });
function gate() { let release; const promise = new Promise(resolve => { release = resolve; }); return { promise, release }; }
const devices = Array.from({ length: 180 }, (_, i) => ({
    serial: `SERIAL-${String(i).padStart(3, '0')}`, agent_id: 'agent-1',
    manufacturer: i === 179 ? 'Zebra' : 'HP', model: i === 179 ? 'DistantMatch' : 'LaserJet',
    ip: `10.0.0.${i + 1}`, hostname: `printer-${i}`, location: i === 179 ? 'Remote depot' : 'Office',
    page_count: i, last_seen: new Date(2026, 9, 5, 0, 0, 180 - i).toISOString(),
    toner_levels: { black: i === 179 ? 5 : 90 }, raw_data: { toner_level_black: 1, color_pages: 700 },
}));
async function open(page, options = {}) {
    const calls = [];
    await page.addInitScript(() => {
        localStorage.setItem('pm_server_active_tab', 'devices');
        localStorage.setItem('pm_server_devices_view', 'cards');
        window.EventSource = class { addEventListener() {} close() {} };
        window.WebSocket = class { addEventListener() {} close() {} };
    });
    await page.route('**/api/**', async route => {
        const pathname = new URL(route.request().url()).pathname;
        if (pathname.includes('/devices/')) calls.push({ path: pathname, keys: route.request().method() === 'POST' ? route.request().postDataJSON().serials : [] });
        if (pathname === '/api/v1/auth/me') return route.fulfill({ json: { username: 'test', role: 'admin', tenant_ids: [] } });
        if (pathname === '/api/v1/agents/list' || pathname === '/api/v1/tenants') {
            if (options.directories) await options.directories.promise;
            return route.fulfill({ json: pathname.endsWith('/list') ? [{ agent_id: 'agent-1', name: 'Delayed office', tenant_id: 'tenant-1' }] : [{ id: 'tenant-1', name: 'Authorized tenant' }] });
        }
        const stage = pathname.endsWith('/index') ? 'index' : pathname.endsWith('/rows') ? 'rows' : pathname.endsWith('/metrics/query') ? 'metrics' : '';
        if (stage && options[stage]) await options[stage].promise;
        if (stage && options.error?.(stage)) return route.fulfill({ status: 503, body: 'unavailable' });
        const response = inventoryResponse(route, options.devices || devices);
        if (response) return response;
        return route.fulfill({ json: route.request().method() === 'GET' ? [] : {} });
    });
    await page.goto(baseURL + '/app', { waitUntil: 'domcontentloaded' });
    const tab = page.locator(page.viewportSize().width < 768 ? '#mobile_bottom_tabs [data-target="devices"]' : '#desktop_tabs [data-target="devices"]');
    await expect(tab).toBeVisible();
    if (page.viewportSize().width < 768) await tab.click();
    await expect(page.locator('[data-tab="devices"]')).toBeVisible();
    return calls;
}
const cards = page => page.locator('#devices_cards .device-card-clickable');
const stageCalls = (calls, stage) => calls.filter(call => call.path.endsWith(stage));
async function openFilters(page) {
    await page.locator('#devices_filters_open').click();
    await expect(page.locator('#devices_search')).toBeVisible();
}

test('Agent update channel modal dispatches exact selection and cancels without command', async ({ page }) => {
    await open(page, { devices: [] });
    const commands = [];
    await page.route('**/api/v1/agents/command/**', route => {
        commands.push(route.request().postDataJSON());
        return route.fulfill({ json: { success: true } });
    });
    const channels = ['beta', 'dev', 'stable', '', 'fleet'];
    for (const channel of channels) {
        await page.evaluate(() => { window.__pm_shared.updateAgent('agent-1'); });
        await expect(page.locator('#agent_update_channel')).toBeVisible();
        await page.locator('#agent_update_channel').selectOption(channel);
        await page.locator('.modal-overlay [data-action="confirm"]').click();
        await expect.poll(() => commands.length).toBe(channels.indexOf(channel) + 1);
        expect(commands.at(-1)).toEqual(channel === 'fleet' ? { command: 'force_update', data: { reason: 'server_ui_force_fleet_channel' } } : channel ? { command: 'install_channel', data: { channel, reason: 'server_ui_channel_install' } } : { command: 'check_update' });
    }
    await page.evaluate(() => { window.__pm_shared.updateAgent('agent-1'); });
    await page.locator('.modal-overlay [data-action="cancel"]').click();
    expect(commands).toHaveLength(5);
    expect(await page.locator('#agent_update_policy_root').count()).toBe(0);
    expect(await page.evaluate(() => typeof saveAgentUpdatePolicyFromUpdatesTab)).toBe('undefined');
});

test('Fleet has one policy editor; cadence edits preserve maintenance and rollout', async ({ page }) => {
    await open(page, { devices: [] });
    const writes = [];
    await page.route('**/api/v1/update-policies/global', route => {
        writes.push(route.request().postDataJSON());
        return route.fulfill({ json: { success: true } });
    });
    await page.evaluate(() => {
        settingsUIState.scope = 'global';
        applyPolicySnapshot('global', true, {
            ...DEFAULT_UPDATE_POLICY_SPEC,
            update_check_days: 7,
            maintenance_window: { ...DEFAULT_UPDATE_POLICY_SPEC.maintenance_window, enabled: true, start_hour: 3, end_hour: 4 },
            rollout_control: { ...DEFAULT_UPDATE_POLICY_SPEC.rollout_control, batch_size: 12, jitter_seconds: 123 }
        });
        refreshPolicyPanel();
        const input = document.querySelector('#auto_update_policy_section [data-policy-path="update_check_days"]');
        input.value = '14';
        handlePolicyFieldChange({ target: input });
    });
    expect(await page.locator('.auto-update-policy').count()).toBe(1);
    await page.evaluate(() => savePolicyChanges('global'));
    expect(writes).toHaveLength(1);
    expect(writes[0].policy.update_check_days).toBe(14);
    expect(writes[0].policy.maintenance_window).toMatchObject({ enabled: true, start_hour: 3, end_hour: 4 });
    expect(writes[0].policy.rollout_control).toMatchObject({ batch_size: 12, jitter_seconds: 123 });
});

test('Fleet persistent channel selector renders and saves through existing settings draft', async ({ page }) => {
    await open(page, { devices: [] });
    const selected = await page.evaluate(() => {
        settingsUIState.scope = 'global';
        settingsUIState.globalDraft = { features: { agent_update_channel: '' } };
        const field = { path: 'features.agent_update_channel', type: 'select', title: 'Agent Update Channel', enum: ['', 'stable', 'beta', 'dev'], default: '', editable_by: ['server_admin'] };
        settingsUIState.schema = { fields: [field] };
        settingsUIState.groupedFields = { features: [field] };
        settingsUIState.managedSections = new Set(['features']);
        bindSettingsEvents();
        renderSettingsForm();
        const root = document.getElementById('settings_form_root');
        const input = root.querySelector('select[data-settings-path="features.agent_update_channel"]');
        const drafts = [];
        for (const channel of ['stable', 'beta', 'dev']) {
            input.value = channel;
            input.dispatchEvent(new Event('change', { bubbles: true }));
            drafts.push(settingsUIState.globalDraft.features.agent_update_channel);
        }
        return {
            labels: Array.from(input.options).map(option => option.textContent),
            drafts, count: root.querySelectorAll('select[data-settings-path="features.agent_update_channel"]').length,
            firstPanel: root.querySelector('.settings-section-panel').id
        };
    });
    expect(selected.labels).toEqual(['Use Agent configuration', 'Stable', 'Beta', 'Dev']);
    expect(selected.drafts).toEqual(['stable', 'beta', 'dev']);
    expect(selected.count).toBe(1);
    expect(selected.firstPanel).toBe('fleet_update_channel_section');
});

test('last-seen aging marks temporary printers offline; filters, table and recovery agree', async ({ page }) => {
    const now = Date.now();
    await page.clock.install({ time: new Date(now) });
    const inventory = [
        { ...devices[0], serial: 'LIVE', last_seen: new Date(now - 1000).toISOString(), status_messages: ['Ready'] },
        { ...devices[1], serial: 'REMOVED', last_seen: new Date(now - 60 * 60 * 1000).toISOString(), status_messages: ['Ready'] },
        { ...devices[2], serial: 'STALE-JAM', last_seen: new Date(now - 60 * 60 * 1000).toISOString(), status_messages: ['Paper jam'] },
        { ...devices[3], serial: 'NO-SEEN', last_seen: '0001-01-01T00:00:00Z', status_messages: [] },
        { ...devices[4], serial: 'EXPIRING', last_seen: new Date(now - 14.5 * 60 * 1000).toISOString(), status_messages: [] },
    ];
    await open(page, { devices: inventory });
    const card = serial => page.locator(`#devices_cards [data-serial="${serial}"]`).first();
    await expect(card('LIVE').locator('.status-pill')).toHaveText('Ready');
    await expect(card('REMOVED').locator('.status-pill')).toHaveText('Offline');
    await expect(card('STALE-JAM').locator('.status-pill')).toHaveText('Offline');
    await expect(card('NO-SEEN').locator('.status-pill')).toHaveText('Unknown');
    await expect(card('EXPIRING').locator('.status-pill')).toHaveText('Healthy');
    await page.clock.fastForward(60 * 1000);
    await expect(card('EXPIRING').locator('.status-pill')).toHaveText('Offline');
    expect(await page.evaluate(() => devicesVM.stats.totalStatuses.offline)).toBe(3);

    await openFilters(page);
    await page.locator('#devices_status_filter [data-status="offline"]').click();
    await expect(card('REMOVED')).not.toBeVisible();
    await page.locator('#devices_status_filter [data-status="offline"]').click();
    await expect(card('REMOVED')).toBeVisible();
    await page.locator('#devices_sidebar_toggle').click();
    await page.evaluate(() => { devicesVM.view = 'table'; applyDeviceFilters(true); });
    await expect(page.locator('#devices_table tr[data-serial="REMOVED"] .status-pill')).toHaveText('Offline');

    inventory[1].last_seen = new Date(now + 60 * 1000).toISOString();
    await page.evaluate(() => loadDevices(true));
    await expect(page.locator('#devices_table tr[data-serial="REMOVED"] .status-pill')).toHaveText('Ready');
});

test('index previews precede rows, metrics and directories; viewport bounds then scroll expands', async ({ page }, testInfo) => {
    const index = gate(), rows = gate(), metrics = gate(), directories = gate();
    try {
        const calls = await open(page, { index, rows, metrics, directories });
        await expect(page.locator('#devices_cards')).toContainText('Loading devices');
        await expect(page.locator('#devices_cards')).not.toContainText('No devices');
        index.release();
        await expect(cards(page).first()).toContainText('HP');
        await expect(cards(page).first()).toContainText('Loading supplies');
        await expect.poll(() => stageCalls(calls, '/rows').length).toBeGreaterThan(0);
        expect(stageCalls(calls, '/metrics/query')).toHaveLength(0);
        expect(stageCalls(calls, '/rows').flatMap(call => call.keys).length).toBeLessThanOrEqual(30);
        await page.screenshot({ path: testInfo.outputPath('index-preview-pending.png'), fullPage: false });
        rows.release();
        await expect.poll(() => stageCalls(calls, '/metrics/query').length).toBeGreaterThan(0);
        await expect(cards(page).first()).toContainText('Loading supplies');
        metrics.release();
        await expect(cards(page).first().locator('.toner-bar')).toHaveCount(1);
        await expect(cards(page).first()).not.toContainText('1%');
        const firstKeys = new Set(stageCalls(calls, '/rows').flatMap(call => call.keys));
        await page.locator('#devices_load_more_sentinel').scrollIntoViewIfNeeded();
        await expect.poll(() => stageCalls(calls, '/rows').flatMap(call => call.keys).some(key => !firstKeys.has(key))).toBe(true);
        expect(calls.every(call => call.keys.length <= 30)).toBe(true);
        expect(calls.some(call => call.path.endsWith('/list'))).toBe(false);
        directories.release();
        await expect(cards(page).last()).toContainText('Delayed office');
        await page.screenshot({ path: testInfo.outputPath('progressive-cards-loaded.png'), fullPage: false });
    } finally { index.release(); rows.release(); metrics.release(); directories.release(); }
});

test('global distant search/sort; card/table switch uses caches; keyboard rows accessible', async ({ page }) => {
    const calls = await open(page);
    await expect(cards(page).first().locator('.toner-bar')).toHaveCount(1);
    await openFilters(page);
    await page.locator('#devices_search').fill('DistantMatch');
    await expect(cards(page)).toHaveCount(1);
    await expect(cards(page).first()).toHaveAttribute('data-serial', 'SERIAL-179');
    await expect(cards(page).first().locator('.toner-bar')).toHaveCount(1);
    await page.keyboard.press('Escape');
    const beforeRows = stageCalls(calls, '/rows').length, beforeMetrics = stageCalls(calls, '/metrics/query').length;
    await page.locator('#devices_view_toggle [data-view="table"]').click();
    const row = page.locator('#devices_table tbody tr[data-serial="SERIAL-179"]');
    await expect(row).toBeVisible(); await expect(row).toHaveAttribute('tabindex', '0');
    await row.focus(); await page.keyboard.press('Space');
    await expect(row).toHaveClass(/selected/);
    await page.locator('#devices_view_toggle [data-view="cards"]').click();
    await expect(cards(page)).toHaveCount(1);
    expect(stageCalls(calls, '/rows').length).toBe(beforeRows);
    expect(stageCalls(calls, '/metrics/query').length).toBe(beforeMetrics);
    await openFilters(page);
    await page.locator('#devices_search').fill('');
    await expect(cards(page)).toHaveCount(30);
    await page.locator('#devices_sort_select').selectOption('manufacturer');
    if (await page.locator('#devices_sort_dir_btn').getAttribute('data-dir') !== 'desc') await page.locator('#devices_sort_dir_btn').click();
    await expect(cards(page).first()).toHaveAttribute('data-serial', 'SERIAL-179');
});

test('all overview cards are compact keyboard actions for agent selection, reset and page sorting', async ({ page }) => {
    await open(page);
    const overviewCards = page.locator('#devices_overview_metrics button.devices-summary-card');
    await expect(overviewCards).toHaveCount(3);
    const minHeight = await overviewCards.first().evaluate(element => element.getBoundingClientRect().height);
    expect(minHeight).toBeLessThan(140);
    await expect(overviewCards.nth(0)).toHaveAttribute('aria-label', 'Filter devices by agent');

    await overviewCards.nth(0).click();
    await expect(page.locator('#devices_agent_filter')).toBeFocused();
    await expect(page.locator('#devices_sidebar')).not.toHaveClass(/collapsed/);
    await page.locator('#devices_search').fill('DistantMatch');
    await expect(cards(page)).toHaveCount(1);
    await page.keyboard.press('Escape');
    await overviewCards.nth(1).click();
    await expect(cards(page)).toHaveCount(30);

    await overviewCards.nth(2).focus();
    await page.keyboard.press('Enter');
    await expect(page.locator('#devices_sort_select')).toHaveValue('page_count');
    await expect(cards(page).first()).toHaveAttribute('data-serial', 'SERIAL-179');
});

test('global supply filter batches metrics without whole fleet rows; pending never false empty/unknown', async ({ page }) => {
    const metrics = gate();
    try {
        const calls = await open(page, { metrics });
        await expect(cards(page).first()).toBeVisible();
        await openFilters(page);
        // Keep Critical only using the existing pill controls.
        for (const band of ['low', 'medium', 'high', 'unknown']) await page.locator(`#devices_consumable_filter [data-band="${band}"]`).click();
        await expect(page.locator('#devices_cards')).toContainText('Checking supplies');
        await expect(page.locator('#devices_cards')).not.toContainText('No devices match');
        metrics.release();
        await expect(cards(page)).toHaveCount(1);
        await expect(cards(page).first()).toHaveAttribute('data-serial', 'SERIAL-179');
        await expect(page.locator('#devices_loading_status')).toHaveText('');
        const metricKeys = new Set(stageCalls(calls, '/metrics/query').flatMap(call => call.keys));
        expect(metricKeys.size).toBe(180);
        expect(new Set(stageCalls(calls, '/rows').flatMap(call => call.keys)).size).toBeLessThan(60);
        expect(calls.every(call => call.keys.length <= 30)).toBe(true);
    } finally { metrics.release(); }
});

test('row/metric errors preserve identities, explicit retry bounded; index retry and empty distinct', async ({ page }) => {
    let failing = true;
    const calls = await open(page, { devices: devices.slice(0, 2), error: stage => failing && stage === 'rows' });
    await expect(cards(page)).toHaveCount(2);
    await expect(page.locator('#devices_retry_loading')).toBeVisible();
    expect(stageCalls(calls, '/rows')).toHaveLength(1);
    failing = false;
    await page.locator('#devices_retry_loading').click();
    await expect(cards(page).first().locator('.toner-bar')).toHaveCount(1);
    await expect(page.locator('#devices_retry_loading')).toBeHidden();
    expect(stageCalls(calls, '/rows')).toHaveLength(2);
    expect(calls.some(call => call.path.endsWith('/list'))).toBe(false);
});

test('index failure offers keyboard retry; truly empty ready index shows empty state', async ({ page }) => {
    let failing = true;
    await open(page, { devices: [], error: stage => failing && stage === 'index' });
    await expect(page.locator('#devices_cards')).toContainText('Failed to load devices');
    failing = false;
    await page.locator('#devices_retry_loading').focus(); await page.keyboard.press('Enter');
    await expect(page.locator('#devices_cards')).toContainText('No devices match');
    await expect(page.locator('#devices_retry_loading')).toBeHidden();
});

test('refresh abort/reset isolates delayed previous-generation rows and metrics', async ({ page }) => {
    const oldRows = gate();
    let rowRequests = 0;
    const options = { devices: devices.slice(0, 2), rows: { get promise() { return ++rowRequests === 1 ? oldRows.promise : Promise.resolve(); } } };
    try {
        const calls = await open(page, options);
        await expect.poll(() => stageCalls(calls, '/rows').length).toBe(1);
        options.devices = [devices[179]];
        await page.evaluate(() => loadDevices(true));
        await expect(cards(page)).toHaveCount(1);
        await expect(cards(page).first()).toHaveAttribute('data-serial', 'SERIAL-179');
        await expect(cards(page).first().locator('.toner-bar')).toHaveCount(1);
        oldRows.release();
        await expect(page.locator('#devices_loading_status')).toHaveText('');
        await expect(cards(page)).toHaveCount(1);
        expect(stageCalls(calls, '/metrics/query').flatMap(call => call.keys)).not.toContain('SERIAL-000');
    } finally { oldRows.release(); }
});

test('table preview/zero counters; metric errors retry without refetching rows', async ({ page }, testInfo) => {
    let failing = true;
    const calls = await open(page, { devices: [{ ...devices[0], page_count: 0, raw_data: { color_pages: 0 }, toner_levels: {} }], error: stage => failing && stage === 'metrics' });
    await page.locator('#devices_view_toggle [data-view="table"]').click();
    const row = page.locator('#devices_table tbody tr[data-serial]');
    await expect(row).toContainText('Supplies unavailable');
    await page.evaluate(() => {
        devicesVM.tableCustomizer.columns.forEach(column => { if (['page_count', 'color_pages'].includes(column.id)) column.visible = true; });
        renderDevicesTableHeader(); applyDeviceFilters();
    });
    await expect(row.locator('[data-column-id="page_count"]')).toHaveText('0');
    await expect(row.locator('[data-column-id="color_pages"]')).toHaveText('Unavailable');
    const before = stageCalls(calls, '/rows').length;
    failing = false;
    await page.locator('#devices_retry_loading').click();
    await expect(row.locator('[data-column-id="color_pages"]')).toHaveText('0');
    await expect(row.locator('[data-column-id="consumables"]')).not.toContainText('Loading');
    expect(stageCalls(calls, '/rows').length).toBe(before);
    await page.screenshot({ path: testInfo.outputPath('progressive-table-zero-counts.png'), fullPage: false });
});