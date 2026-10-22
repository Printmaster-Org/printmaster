const { test, expect } = require('@playwright/test');
const http = require('http');
const fs = require('fs');
const path = require('path');
const { inventoryResponse } = require('./inventory-fixture');
const epsonSupplies = require('../fixtures/epson-p9500-supplies.json');
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
        window.EventSource = class {
            constructor() { this.listeners = {}; window.testEventSource = this; }
            addEventListener(name, listener) { this.listeners[name] = listener; }
            close() {}
        };
        window.WebSocket = class { addEventListener() {} close() {} };
    });
    await page.route('**/api/**', async route => {
        const pathname = new URL(route.request().url()).pathname;
        if (pathname.includes('/devices/')) calls.push({ path: pathname, keys: route.request().method() === 'POST' ? route.request().postDataJSON().serials : [] });
        if (pathname === '/api/v1/auth/me') return route.fulfill({ json: { username: 'test', role: 'admin', tenant_ids: [] } });
        if (pathname === '/api/v1/agents/list' || pathname === '/api/v1/tenants') {
            if (options.directories) await options.directories.promise;
            return route.fulfill({ json: pathname.endsWith('/list') ? (options.agents || [{ agent_id: 'agent-1', name: 'Delayed office', tenant_id: 'tenant-1' }]) : [{ id: 'tenant-1', name: 'Authorized tenant' }] });
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

async function recordSessionVisibility(page) {
    const states = [];
    await page.exposeFunction('recordSessionVisibility', state => states.push(state));
    await page.addInitScript(() => {
        const setAttribute = Element.prototype.setAttribute;
        Element.prototype.setAttribute = function(name, value) {
            setAttribute.call(this, name, value);
            if (this === document.documentElement && name === 'data-session-pending') {
                window.recordSessionVisibility(getComputedStyle(document.body).visibility);
            }
        };
    });
    return states;
}

test('invalid startup session redirects before revealing the fleet UI', async ({ page }) => {
    const states = await recordSessionVisibility(page);
    let inventoryRequests = 0;
    await page.route('**/login?**', route => route.fulfill({ contentType: 'text/html', body: '<h1>Sign in</h1>' }));
    await page.route('**/api/**', route => {
        const pathname = new URL(route.request().url()).pathname;
        if (pathname.includes('/devices/')) inventoryRequests++;
        return route.fulfill({ status: 401, body: 'invalid session' });
    });
    const navigation = page.waitForRequest('**/login?**');
    await page.goto(baseURL + '/app?tenant=office#devices', { waitUntil: 'domcontentloaded' });
    const request = await navigation;
    expect(new URL(request.url()).searchParams.get('redirect')).toBe('/app?tenant=office#devices');
    await expect.poll(() => states).toEqual(['hidden']);
    expect(inventoryRequests).toBe(0);
    await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible();
});

test('expired session hides inventory errors and redirects without waiting for auth options', async ({ page }) => {
    const states = await recordSessionVisibility(page);
    await open(page);
    await expect(cards(page)).toHaveCount(30);
    await page.route('**/login?**', route => route.fulfill({ contentType: 'text/html', body: '<h1>Sign in</h1>' }));
    await page.route('**/api/v1/devices/index', route => route.fulfill({ status: 401, body: 'invalid session' }));
    const navigation = page.waitForRequest('**/login?**');
    await page.evaluate(() => { void loadDevices(true); });
    await navigation;
    await expect.poll(() => states).toEqual(['hidden']);
    await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible();
});

test('device interactions omit extra controls and Shift-select without highlighting text', async ({ page }) => {
    await open(page, { devices: devices.slice(0, 3) });
    await expect(cards(page)).toHaveCount(3);
    await expect(page.locator('[data-device-select], [data-device-details]')).toHaveCount(0);
    // Hydration replaces card header markup; clicking mid-swap detaches the mousedown target and WebKit drops the click.
    for (let i = 0; i < 3; i++) {
        await cards(page).nth(i).scrollIntoViewIfNeeded();
        await expect(cards(page).nth(i)).toHaveAttribute('data-hydrated', 'true');
    }
    const first = cards(page).nth(0);
    await first.locator('.device-card-title').click();
    await expect.poll(() => page.evaluate(() => devicesVM.selection.lastSelected)).toBe(devices[0].serial);
    await cards(page).nth(2).locator('.device-card-title').click({ modifiers: ['Shift'] });
    expect(await page.evaluate(() => [...devicesVM.selection.selectedIds].length)).toBe(3);
    expect(await page.evaluate(() => window.getSelection().toString())).toBe('');
    // Late re-renders can replace the focused card; retry focus + Enter atomically.
    await expect(async () => {
        await first.focus();
        await page.keyboard.press('Enter');
        await expect(page.locator('#printer_details_overlay')).toBeVisible({ timeout: 1000 });
    }).toPass();
    await page.locator('#printer_details_actions').getByRole('button', { name: 'Close', exact: true }).click();
    await page.evaluate(() => { devicesVM.view = 'table'; applyDeviceFilters(); renderDevicesTableHeader(); });
    await expect(page.locator('#devices_table_header')).not.toContainText('Selection / Details');
    await expect(page.locator('#devices_table tbody tr.device-row-clickable')).toHaveCount(3);
});

test('shared copy controls copy raw device values once without changing selection', async ({ page }) => {
    await open(page, { devices: [{ ...devices[0], serial: 'COPY-ME', hostname: 'Printer host' }] });
    await page.evaluate(() => {
        window.copiedValues = [];
        Object.defineProperty(navigator, 'clipboard', { configurable: true, value: {
            writeText: async value => window.copiedValues.push(value),
        } });
    });
    const card = cards(page).first();
    const copied = () => page.evaluate(() => window.copiedValues);
    // Hydration swaps card markup; clicking a pre-hydration button detaches it and WebKit drops the click.
    await card.scrollIntoViewIfNeeded();
    await expect(card).toHaveAttribute('data-hydrated', 'true');
    await expect(card.getByRole('button', { name: 'Copy serial number', exact: true })).toBeVisible();
    await card.getByRole('button', { name: 'Copy serial number', exact: true }).click();
    await expect.poll(copied).toEqual(['COPY-ME']);
    expect(await page.evaluate(() => devicesVM.selection.selectedIds.size)).toBe(0);
    await card.getByRole('button', { name: 'Copy IP address', exact: true }).focus();
    await page.keyboard.press('Enter');
    await expect.poll(copied).toEqual(['COPY-ME', devices[0].ip]);
    const details = page.locator('#printer_details_overlay');
    await expect(async () => {
        await card.focus();
        await page.keyboard.press('Enter');
        await expect(details).toBeVisible({ timeout: 1000 });
    }).toPass();
    await expect(details.getByRole('button', { name: 'Copy Serial', exact: true })).toBeVisible();
    await details.getByRole('button', { name: 'Copy Serial', exact: true }).click();
    await expect.poll(copied).toEqual(['COPY-ME', devices[0].ip, 'COPY-ME']);
    const edit = details.getByRole('button', { name: 'Edit Location', exact: true });
    await edit.click();
    const input = page.locator('.modal-overlay input').last();
    await expect(input).toBeVisible();
    expect(await input.evaluate(element => getComputedStyle(element).userSelect)).toBe('text');
});

test('upload notifications refresh in place without repeated card loading or losing focus', async ({ page }) => {
    const calls = await open(page, { devices: devices.slice(0, 3) });
    await expect(cards(page)).toHaveCount(3);
    await expect(cards(page).first()).toHaveAttribute('data-hydrated', 'true');
    await page.waitForFunction(() => testEventSource?.listeners.device_updated);
    await cards(page).first().getByRole('button', { name: 'Copy serial number', exact: true }).focus();
    await page.evaluate(() => {
        window.retainedCard = document.querySelector('.device-card-clickable');
        window.retainedCopyButton = document.activeElement;
        for (let i = 0; i < 20; i++) testEventSource.listeners.device_updated({
            data: JSON.stringify({ serial: 'SERIAL-002', agent_id: 'agent-1' }),
        });
    });
    const before = stageCalls(calls, '/index').length;
    await expect.poll(() => stageCalls(calls, '/index').length).toBe(before + 1);
    await expect.poll(async () => page.evaluate(() => !devicesRefreshInFlight)).toBe(true);
    expect(await page.evaluate(() => window.retainedCard === document.querySelector('.device-card-clickable'))).toBe(true);
    expect(await page.evaluate(() => window.retainedCopyButton === document.activeElement)).toBe(true);
    await expect(page.locator('#devices_cards')).not.toContainText('Loading devices');
});

test('Server Details uses uploaded saved state and never invokes Agent-local Save', async ({ page }) => {
    await open(page, { devices: [
        { ...devices[0], serial: 'SAVED', is_saved: true },
        { ...devices[1], serial: 'DISCOVERED', is_saved: false },
        { ...devices[2], serial: 'LEGACY' }
    ] });
    for (const serial of ['SAVED', 'DISCOVERED', 'LEGACY']) {
        await page.evaluate(serial => window.__pm_shared.showPrinterDetails(serial, 'discovered'), serial);
        const overlay = page.locator('#printer_details_overlay');
        await expect(overlay).toBeVisible();
        await expect(page.locator('#printer_details_title')).toContainText('LaserJet');
        if (serial === 'DISCOVERED') await expect(page.locator('#printer_details_title')).toContainText('(Discovered)');
        else await expect(page.locator('#printer_details_title')).not.toContainText('(Discovered)');
        const actions = page.locator('#printer_details_actions');
        await expect(actions.getByRole('button', { name: 'Save Device', exact: true })).toHaveCount(0);
        await expect(actions.getByRole('button', { name: 'Delete from Server', exact: true })).toBeVisible();
        if (serial === 'LEGACY') await expect(actions).toContainText('not reported yet');
        if (serial === 'DISCOVERED') await expect(actions).toContainText('Agent UI');
        await actions.getByRole('button', { name: 'Close', exact: true }).click();
    }
    const deletes = [];
    await page.route('**/api/v1/devices/delete', route => {
        deletes.push(route.request().postDataJSON());
        return route.fulfill({ json: { success: true } });
    });
    await page.locator('.device-card-clickable[data-serial="SAVED"]').dblclick();
    await expect(page.locator('#printer_details_overlay')).toBeVisible();
    await page.getByRole('button', { name: 'Delete from Server', exact: true }).click();
    let confirmation = page.locator('[id^="delete_device_confirm_"].modal-overlay');
    await confirmation.getByRole('button', { name: 'Cancel', exact: true }).click();
    await expect(page.locator('#printer_details_overlay')).toBeVisible();
    expect(deletes).toHaveLength(0);
    await page.getByRole('button', { name: 'Delete from Server', exact: true }).click();
    confirmation = page.locator('[id^="delete_device_confirm_"].modal-overlay').filter({ visible: true });
    await confirmation.getByRole('button', { name: 'Delete Device', exact: true }).click();
    await expect(page.locator('#printer_details_overlay')).not.toBeVisible();
    expect(deletes).toEqual([{ serial: 'SAVED', agent_id: 'agent-1', delete_metrics: false, delete_from_agent: false }]);
    expect(await page.locator('body').evaluate(el => el.style.overflow)).toBe('');
});

test('Server metadata pins disable Agent edits and Server unpin restores reported values immediately', async ({ page }) => {
    const pinned = { ...devices[0], location: '', metadata_overrides: { location: '' } };
    await open(page, { devices: [pinned] });
    await page.evaluate(device => window.__pm_shared_cards.showPrinterDetailsData(device, 'saved'), pinned);
    await expect(page.getByRole('button', { name: 'Edit Location', exact: true })).toBeDisabled();
    await expect(page.getByRole('button', { name: 'Edit Model', exact: true })).toBeEnabled();
    await expect(page.getByRole('button', { name: 'Unpin Location', exact: true })).toHaveCount(0);
    await page.evaluate(device => window.__pm_shared_cards.showPrinterDetailsData(device, 'saved', null, { server: true }), pinned);
    await expect(page.getByRole('button', { name: 'Edit Location', exact: true })).toBeEnabled();
    const requests = [];
    await page.route('**/devices/unpin', route => {
        requests.push(route.request().postDataJSON());
        return route.fulfill({ json: { device: { ...devices[0], location: 'Reported room', metadata_overrides: {} } } });
    });
    await page.getByRole('button', { name: 'Unpin Location', exact: true }).click();
    await expect(page.locator('#field_location_display')).toHaveText('Reported room');
    await expect(page.getByRole('button', { name: 'Unpin Location', exact: true })).toHaveCount(0);
    expect(requests).toEqual([{ serial: pinned.serial, field: 'location' }]);
});

test('hydrated Epson inventory keeps inkjet capabilities and reported metadata', async ({ page }) => {
    await open(page, { devices: [{
        ...devices[0], manufacturer: 'Epson', model: 'AM-C550', asset_number: '05046',
        location: "Joe's Office", web_ui_url: 'http://192.0.2.91',
        raw_data: { is_inkjet: true, is_color: true, is_copier: true, device_type: 'Color MFP',
            toner_desc_black: 'Black Ink Cartridge T08M1', toner_level_black: 1 },
        toner_levels: { 'Black Ink Cartridge T08M1': 33 },
    }] });
    const card = cards(page).first();
    await card.scrollIntoViewIfNeeded();
    await expect(card).toHaveAttribute('data-hydrated', 'true');
    await expect(card.locator('.capability-badge.tech')).toHaveText('Inkjet');
    await expect(card.locator('.capability-badge.type')).toHaveText('Color MFP');
    const device = await page.evaluate(() => getProgressiveDevice(devicesVM.items[0]));
    expect(device.raw_data.toner_level_black).toBeUndefined();
    expect(device.raw_data.toner_desc_black).toBe('Black Ink Cartridge T08M1');
    expect(device.asset_number).toBe('05046');
    expect(device.location).toBe("Joe's Office");
    expect(device.web_ui_url).toBe('http://192.0.2.91');
});

test('saved Epson ink diagnostics show linked replacement names without toner wording', async ({ page }) => {
    await open(page, { devices: [] });
    await page.evaluate(fixture => {
        window.__pm_shared_cards.showPrinterDetailsData({ ...fixture, serial: 'EPSON-FIXTURE', ip: '192.0.2.10' }, 'saved');
    }, epsonSupplies);
    const supplies = page.locator('#printer_consumables_card_actual');
    await expect(supplies).toBeVisible();
    for (const [label, parts] of [
        ['Photo Black Ink Cartridge', 'T44H1, T44P1, T44W1'],
        ['Vivid Light Magenta Ink Cartridge', 'T44H6, T44P6, T44W6']
    ]) {
        const row = supplies.locator('.consumable-level').filter({ has: page.locator('.consumable-level-label', { hasText: new RegExp(`^${label}$`) }) });
        await expect(row.locator('.consumable-part-numbers')).toHaveText(parts);
        await expect(row.locator('[role="progressbar"]')).toHaveAttribute('aria-label', `${label} (${parts})`);
    }
    await expect(supplies).toContainText('Waste Ink');
    await expect(supplies).not.toContainText(/toner|supply_/i);
    await expect(supplies.locator('[role="progressbar"]')).toHaveCount(10);
    expect(await supplies.evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true);
});

async function openFilters(page) {
    await page.locator('#devices_filters_open').click();
    await expect(page.locator('#devices_search')).toBeVisible();
}

test('Agent update progress restores from storage and terminal events cannot regress it', async ({ page }) => {
    const snapshot = {
        run_id: 'persisted-run', run_started_at: '2026-10-16T18:00:00Z', sequence: 7,
        status: 'downloading', progress: 45, target_version: '1.1.0', message: 'Downloading update',
    };
    await open(page, { devices: [], agents: [{
        agent_id: 'agent-1', name: 'Remote site', connection_type: 'ws',
        version: '1.0.0', update_progress: snapshot,
    }] });
    await page.evaluate(() => loadAgents(true));
    expect(await page.evaluate(() => agentsVM.updateState['agent-1'].progress)).toBe(45);
    const actualProgress = await page.evaluate(() => {
        const state = agentsVM.updateState['agent-1'];
        return window.__pm_shared.updateProgressView(state.snapshot, state.timestamp).progress;
    });
    expect(actualProgress).toBe(45);
    await page.evaluate(snapshot => {
        handleAgentUpdateProgress({ ...snapshot, agent_id: 'agent-1', sequence: 10, status: 'succeeded', progress: 100 });
        handleAgentUpdateProgress({ ...snapshot, agent_id: 'agent-1', sequence: 9, status: 'restarting', progress: 80 });
        handleAgentUpdateProgress({ ...snapshot, agent_id: 'agent-1', sequence: 11, status: 'restarting', progress: 80 });
    }, snapshot);
    expect(await page.evaluate(() => agentsVM.updateState['agent-1'].status)).toBe('succeeded');
});

test('Agent update channel modal dispatches exact selection and cancels without command', async ({ page }) => {
    await open(page, { devices: [] });
    const commands = [];
    await page.route('**/api/v1/agents/command/**', route => {
        commands.push(route.request().postDataJSON());
        return route.fulfill({ json: { success: true } });
    });
    const channels = ['beta', 'dev', 'stable', '', 'fleet'];
    for (const channel of channels) {
        await page.evaluate(() => { window.__pm_shared.updateAgent('agent-1', { chooseChannel: true }); });
        await expect(page.locator('#agent_update_channel')).toBeVisible();
        await expect(page.locator('#agent_update_channel option[value="stable"]')).toHaveText('Stable');
        await expect(page.locator('#agent_update_channel option[value="beta"]')).toHaveText('Beta');
        await expect(page.locator('#agent_update_channel option[value="dev"]')).toHaveText('Dev');
        await page.locator('#agent_update_channel').selectOption(channel);
        await page.locator('.modal-overlay [data-action="confirm"]').click();
        await expect.poll(() => commands.length).toBe(channels.indexOf(channel) + 1);
        expect(commands.at(-1)).toEqual(channel === 'fleet' ? { command: 'force_update', data: { reason: 'server_ui_force_fleet_channel' } } : channel ? { command: 'install_channel', data: { channel, reason: 'server_ui_channel_install' } } : { command: 'check_update' });
        await page.evaluate(index => handleAgentUpdateProgress({
            agent_id: 'agent-1', run_id: `picker-test-${index}`,
            run_started_at: new Date(Date.now() + index * 1000).toISOString(),
            updated_at: new Date().toISOString(), sequence: 1, status: 'idle', progress: -1,
        }), channels.indexOf(channel));
    }
    await page.evaluate(() => { window.__pm_shared.updateAgent('agent-1', { chooseChannel: true }); });
    await page.locator('.modal-overlay [data-action="cancel"]').click();
    expect(commands).toHaveLength(5);
    expect(await page.locator('#agent_update_policy_root').count()).toBe(0);
    expect(await page.evaluate(() => typeof saveAgentUpdatePolicyFromUpdatesTab)).toBe('undefined');
});

test('right-click Agent update opens one picker; inline pending update sends policy check directly', async ({ page }) => {
    // Serve the WS agent from the mocked list so the async agents load cannot overwrite the fixture.
    await open(page, { devices: [], agents: [{ agent_id: 'agent-1', name: 'Test PC', connection_type: 'ws', tenant_id: 'tenant-1' }] });
    const commands = [];
    await page.route('**/api/v1/agents/command/**', route => {
        commands.push(route.request().postDataJSON());
        return route.fulfill({ json: { success: true } });
    });
    await page.evaluate(() => {
        agentsVM.items = [{ agent_id: 'agent-1', name: 'Test PC', connection_type: 'ws' }];
        const container = document.createElement('div');
        container.id = 'agent-action-fixture';
        container.style.cssText = 'position:fixed;top:120px;left:20px;z-index:100;';
        container.innerHTML = '<div data-agent-id="agent-1">Test PC <button data-agent-id="agent-1" data-action="update-agent">Pending update</button></div>';
        document.body.append(container);
        window.PMContextMenu.initAgentContextMenu(container);
    });
    await page.locator('#agent-action-fixture').getByText('Test PC', { exact: false }).click({ button: 'right' });
    await page.locator('.pm-context-menu-item[data-action="update-agent"]').click();
    await expect(page.locator('#agent_update_channel')).toHaveCount(1);
    await page.locator('#agent_update_channel').selectOption('beta');
    await page.locator('.modal-overlay [data-action="confirm"]').click();
    await expect.poll(() => commands.length).toBe(1);
    await expect(page.locator('#agent_update_channel')).toHaveCount(0);
    expect(commands[0].command).toBe('install_channel');
    await page.locator('#agent-action-fixture button').click();
    await expect.poll(() => commands.length).toBe(2);
    expect(commands[1]).toEqual({ command: 'check_update' });
    await expect(page.locator('#agent_update_channel')).toHaveCount(0);
});

test('Agent details are readable, responsive, and preserve name editing and command gating', async ({ page }, testInfo) => {
    await open(page, { devices: [] });
    await page.evaluate(() => {
        renderAgentDetailsModal({
            agent_id: '92a95307-1fc2-a6fe-f5e1-a7e2799e77bf', name: 'P3 Print Station 01',
            hostname: 'P3-ROBOTS-01', ip: '192.0.2.10', status: 'active', connection_type: 'ws',
            platform: 'Windows 11 (24H2)', os_version: 'Windows 11 (24H2) (build 26100)',
            architecture: 'amd64', version: '0.32.0-beta.2', protocol_version: '1',
            num_cpu: 8, total_memory_mb: 16384, go_version: 'go1.27.0', device_count: 12,
            build_type: 'beta', git_commit: 'abcdef123', registered_at: '2026-10-01T12:00:00Z',
            last_seen: '2026-10-12T19:00:00Z', last_heartbeat: '2026-10-12T19:00:00Z',
        });
        document.getElementById('agent_details_overlay').style.display = 'flex';
    });
    const dialog = page.locator('#agent_details_modal');
    await expect(dialog).toBeVisible();
    await expect(dialog).toContainText('build 26100');
    await expect(dialog).toContainText('16.00 GB');
    await expect(dialog).not.toContainText('Uptime');
    await expect(page.locator('#agent_check_update_btn')).toBeEnabled();
    expect(await dialog.evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true);
    expect(await page.locator('#agent_details_body').evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true);
    await page.screenshot({ path: testInfo.outputPath('agent-details.png') });
    await page.locator('#agent_details_edit_name_btn').click();
    await expect(page.locator('#agent_details_name_input')).toHaveValue('P3 Print Station 01');
    await page.locator('#agent_details_cancel_name').click();
    await expect(page.locator('#agent_details_name_display')).toHaveText('P3 Print Station 01');
    await page.getByRole('button', { name: 'Close agent details', exact: true }).click();
    await expect(dialog).not.toBeVisible();
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
            channelPanel: input.closest('.settings-section-panel').id
        };
    });
    expect(selected.labels).toEqual(['Use Agent configuration', 'Stable', 'Beta', 'Dev']);
    expect(selected.drafts).toEqual(['stable', 'beta', 'dev']);
    expect(selected.count).toBe(1);
    expect(selected.channelPanel).toBe('auto_update_policy_section');
});

test('Agent Updates scopes support channel overrides, inheritance, and readable responsive layout', async ({ page }) => {
    await open(page, { devices: [] });
    await page.evaluate(() => {
        const root = document.getElementById('settings_form_root');
        document.body.appendChild(root);
        [...document.body.children].forEach(child => { if (child !== root) child.style.display = 'none'; });
        root.style.width = 'calc(100% - 32px)';
        root.style.maxWidth = '1100px';
        root.style.margin = '16px auto';
        settingsUIState.globalSnapshot = { settings: { features: { agent_update_channel: 'stable' } } };
        settingsUIState.globalDraft = cloneSettings(settingsUIState.globalSnapshot.settings);
        settingsUIState.tenantSnapshot = { overrides: { features: { agent_update_channel: 'dev' } } };
        settingsUIState.agentSnapshot = { overrides: { features: { agent_update_channel: 'dev' } } };
        settingsUIState.tenantDraft = { features: { agent_update_channel: 'dev' } };
        settingsUIState.agentDraft = { features: { agent_update_channel: 'dev' } };
        settingsUIState.agentBaseSnapshot = { settings: { features: { agent_update_channel: 'beta' } } };
        settingsUIState.tenantOverridesDraft = { features: { agent_update_channel: 'dev' } };
        settingsUIState.agentOverridesDraft = { features: { agent_update_channel: 'dev' } };
        settingsUIState.selectedTenantId = 'tenant-1';
        settingsUIState.selectedAgentId = 'agent-1';
        const field = { path: 'features.agent_update_channel', type: 'select', title: 'Agent Update Channel',
            description: 'Choose the release channel for this scope. Global defaults flow through customer overrides to permitted Agent overrides.',
            enum: ['', 'stable', 'beta', 'dev'], default: '' };
        settingsUIState.schema = { fields: [field] };
        settingsUIState.groupedFields = { features: [field] };
        settingsUIState.managedSections = new Set(['features']);
        settingsUIState.agentEnforcedSections = new Set();
        applyPolicySnapshot('global', false, DEFAULT_UPDATE_POLICY_SPEC);
        applyPolicySnapshot('tenant', false, DEFAULT_UPDATE_POLICY_SPEC);
        bindSettingsEvents();
    });
    for (const scope of ['global', 'tenant', 'agent']) {
        await page.evaluate(scope => {
            settingsUIState.scope = scope;
            renderSettingsForm();
        }, scope);
        const panel = page.locator('#auto_update_policy_section');
        const channel = panel.locator('select[data-settings-path="features.agent_update_channel"]');
        await expect(panel.locator('h4')).toHaveText('Agent Updates');
        await expect(channel).toBeEnabled();
        await channel.selectOption('dev');
        expect(await page.evaluate(scope => settingsUIState[`${scope}Draft`].features.agent_update_channel, scope)).toBe('dev');
        await page.evaluate(() => renderSettingsForm());
        if (scope !== 'global') {
            await expect(panel.locator('.settings-badge.override')).toBeVisible();
            await panel.getByRole('button', { name: 'Inherit', exact: true }).click();
            await expect(channel).toHaveValue(scope === 'tenant' ? 'stable' : 'beta');
            await expect(panel.locator('.settings-badge.inherited')).toBeVisible();
            expect(await page.evaluate(scope => settingsUIState[`${scope}Dirty`], scope)).toBe(true);
            expect(await page.evaluate(scope => flattenOverrides(settingsUIState[`${scope}OverridesDraft`]), scope)).toEqual([]);
        }
        const layout = await panel.evaluate(panel => {
            const header = panel.querySelector('.settings-section-header');
            const title = header.querySelector('h4').getBoundingClientRect();
            const description = header.querySelector('p').getBoundingClientRect();
            const fieldTitle = panel.querySelector('.field-title').getBoundingClientRect();
            const select = panel.querySelector('select').getBoundingClientRect();
            return { titleHeight: title.height, stacked: description.top >= title.bottom,
                fieldTitleHeight: fieldTitle.height, selectWidth: select.width,
                overflow: panel.scrollWidth > panel.clientWidth };
        });
        expect(layout.titleHeight).toBeLessThanOrEqual(30);
        expect(layout.fieldTitleHeight).toBeLessThanOrEqual(30);
        expect(layout.stacked).toBe(true);
        expect(layout.selectWidth).toBeGreaterThanOrEqual(180);
        expect(layout.overflow).toBe(false);
    }
    await page.evaluate(() => {
        settingsUIState.agentEnforcedSections.add('features');
        renderSettingsForm();
    });
    await expect(page.locator('#auto_update_policy_section select')).toBeDisabled();
    await expect(page.locator('#auto_update_policy_section .inherit-btn')).toHaveCount(0);
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
    // Status tags are an inclusive selection: choosing Offline shows only offline devices.
    await page.locator('#devices_status_filter [data-status="offline"]').click();
    await expect(page.locator('#devices_status_filter [data-status="offline"]')).toHaveAttribute('aria-pressed', 'true');
    await expect(card('REMOVED')).toBeVisible();
    await expect(card('LIVE')).not.toBeVisible();
    await page.locator('#devices_status_filter [data-status="offline"]').click();
    await expect(card('LIVE')).toBeVisible();
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

test('40-device inventory retains displayed supplies during upload refresh and scrolling', async ({ page }) => {
    const refreshMetrics = gate();
    const options = { devices: devices.slice(0, 40) };
    try {
        const calls = await open(page, options);
        await expect(cards(page).first().locator('.toner-bar')).toHaveCount(1);
        const serial = await cards(page).first().getAttribute('data-serial');
        const metricsBefore = stageCalls(calls, '/metrics/query').length;
        options.metrics = refreshMetrics;
        await page.evaluate(serial => window.testEventSource.listeners.device_updated({
            data: JSON.stringify({ serial, agent_id: 'agent-1' })
        }), serial);
        await expect.poll(() => stageCalls(calls, '/metrics/query').length).toBeGreaterThan(metricsBefore);
        const first = page.locator(`#devices_cards [data-serial="${serial}"]`);
        await expect(first.locator('.toner-bar')).toHaveCount(1);
        await expect(first).not.toContainText('Loading supplies');
        await page.locator('#devices_load_more_sentinel').scrollIntoViewIfNeeded();
        await expect(cards(page)).toHaveCount(40);
        await first.scrollIntoViewIfNeeded();
        await expect(first.locator('.toner-bar')).toHaveCount(1);
        await expect(first).not.toContainText('Loading supplies');
        options.devices = options.devices.map(device => ({ ...device, toner_levels: { black: 55 } }));
        refreshMetrics.release();
        await expect(first.locator('.toner-bar')).toHaveAttribute('aria-label', /55%/);
    } finally { refreshMetrics.release(); }
});

test('loaded supplies survive a large inventory scan, scrolling and view changes', async ({ page }) => {
    const inventory = Array.from({ length: 360 }, (_, i) => ({
        ...devices[0], serial: `CACHE-${String(i).padStart(3, '0')}`,
        hostname: `cached-printer-${i}`, toner_levels: { black: 90 }
    }));
    const calls = await open(page, { devices: inventory });
    const first = cards(page).first();
    await expect(first.locator('.toner-bar')).toHaveCount(1);
    const firstSerial = await first.getAttribute('data-serial');
    await page.evaluate(() => {
        devicesVM.filters.sortKey = 'consumables';
        applyDeviceFilters(true);
    });
    await expect.poll(() => page.evaluate(() => deviceSupplyBands.size)).toBe(360);
    await expect.poll(() => page.evaluate(() => devicesLoader.getCacheSizes().metrics)).toBe(360);
    await page.evaluate(() => {
        devicesVM.filters.sortKey = 'manufacturer';
        applyDeviceFilters(true);
    });
    await page.locator('#devices_load_more_sentinel').scrollIntoViewIfNeeded();
    await expect.poll(() => cards(page).count()).toBeGreaterThan(30);
    await page.locator(`#devices_cards [data-serial="${firstSerial}"]`).scrollIntoViewIfNeeded();
    await expect(page.locator(`#devices_cards [data-serial="${firstSerial}"] .toner-bar`)).toHaveCount(1);
    await page.evaluate(() => {
        devicesVM.view = 'table';
        applyDeviceFilters();
        renderDevicesTableHeader();
    });
    const row = page.locator(`#devices_table tr[data-serial="${firstSerial}"]`);
    await row.scrollIntoViewIfNeeded();
    await expect(row.locator('.toner-bar')).toHaveCount(1);
    await expect(row).not.toContainText('Loading supplies');
    expect(stageCalls(calls, '/metrics/query').flatMap(call => call.keys).filter(key => key === firstSerial)).toHaveLength(1);
    expect(calls.every(call => call.keys.length <= 30)).toBe(true);
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
    const failedKeys = stageCalls(calls, '/rows')[0].keys;
    failing = false;
    await page.locator('#devices_retry_loading').click();
    await expect(cards(page).first().locator('.toner-bar')).toHaveCount(1);
    await expect(page.locator('#devices_retry_loading')).toBeHidden();
    // Rows load per viewport, so cards below the fold may hydrate in later
    // requests; retry must re-request only failed keys, each exactly once.
    const rowCalls = stageCalls(calls, '/rows');
    expect(rowCalls[1].keys).toEqual(failedKeys);
    const requested = rowCalls.flatMap(call => call.keys);
    for (const key of new Set(requested)) {
        expect(requested.filter(k => k === key)).toHaveLength(failedKeys.includes(key) ? 2 : 1);
    }
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