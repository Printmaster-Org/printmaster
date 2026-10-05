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