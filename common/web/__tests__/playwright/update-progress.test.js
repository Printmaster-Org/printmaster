const { test, expect } = require('@playwright/test');
const http = require('http');
const fs = require('fs');
const path = require('path');

const root = path.resolve(__dirname, '../../../..');
let server, baseURL;
test.beforeAll(async () => {
    server = http.createServer((req, res) => {
        const pathname = decodeURIComponent(new URL(req.url, 'http://localhost').pathname).split('{{')[0];
        const agent = pathname.startsWith('/agent/');
        const relative = pathname.replace(/^\/agent/, '');
        let file = path.join(root, agent ? 'agent/web' : 'server/web', relative === '/' ? 'index.html' : relative.replace(/^\/static\//, ''));
        if (!fs.existsSync(file) && relative.startsWith('/static/')) file = path.join(root, 'common/web', relative.slice(8));
        if (!fs.existsSync(file) || !fs.statSync(file).isFile()) { res.writeHead(404); res.end(); return; }
        res.setHeader('Content-Type', file.endsWith('.html') ? 'text/html' : file.endsWith('.css') ? 'text/css' : 'application/javascript');
        res.end(fs.readFileSync(file));
    });
    await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
    baseURL = `http://127.0.0.1:${server.address().port}`;
});
test.afterAll(async () => {
    server.closeAllConnections();
    await new Promise(resolve => server.close(resolve));
});

function snapshot(sequence, status, extra = {}) {
    return {
        run_id: 'one', run_started_at: '2026-10-22T12:00:00Z',
        updated_at: new Date().toISOString(), sequence, status, progress: -1,
        target_version: '0.32.0', ...extra,
    };
}

async function open(page, kind, initial) {
    const agent = { agent_id: 'agent-1', name: 'Test office', version: '0.31.0', connection_type: 'ws', update_progress: initial };
    let current = initial;
    await page.addInitScript(() => {
        localStorage.setItem('pm_agent_active_tab', 'settings');
        localStorage.setItem('pm_server_active_tab', 'agents');
        window.EventSource = class {
            constructor() { this.listeners = {}; window.testEvents = this; }
            addEventListener(name, listener) { this.listeners[name] = listener; }
            close() {}
        };
        window.WebSocket = class { addEventListener() {} close() {} };
    });
    await page.route('**/api/**', route => {
        const pathname = new URL(route.request().url()).pathname;
        if (pathname === '/api/v1/auth/me') return route.fulfill({ json: { username: 'admin', role: 'admin', tenant_ids: [] } });
        if (pathname === '/api/autoupdate/status') return route.fulfill({
            json: { enabled: true, status: current.status, current_version: '0.31.0', update_progress: current },
        });
        if (pathname === '/api/v1/agents/list') return route.fulfill({ json: [{ ...agent, update_progress: current }] });
        if (pathname === '/api/v1/agents/agent-1') return route.fulfill({ json: { ...agent, update_progress: current } });
        return route.fulfill({ json: route.request().method() === 'GET' ? [] : { success: true } });
    });
    await page.goto(baseURL + (kind === 'agent' ? '/agent/' : '/'), { waitUntil: 'domcontentloaded' });
    await page.waitForFunction(() => typeof window.__pm_shared?.updateProgressView === 'function');
    if (kind === 'agent') {
        if (page.viewportSize().width < 768) await page.locator('#mobile_bottom_tabs [data-target="settings"]').click();
        await expect(page.locator('#autoupdate_panel')).toBeVisible();
        await page.evaluate(() => refreshAutoUpdateStatus());
    } else {
        const tab = page.locator(page.viewportSize().width < 768 ? '#mobile_bottom_tabs [data-target="agents"]' : '#desktop_tabs [data-target="agents"]');
        await tab.click();
        await page.waitForFunction(() => typeof agentsVM !== 'undefined' && agentsVM.items.length === 1);
        await page.evaluate(() => renderAgentDetailsModal(agentsVM.items[0]));
        await page.evaluate(() => { document.getElementById('agent_details_overlay').style.display = 'flex'; });
        await expect(page.locator('#agent_update_status')).toBeVisible();
    }
    return async data => {
        if (data.run_id !== current.run_id || data.sequence > current.sequence) current = data;
        await page.evaluate(({ kind, data }) => {
            if (kind === 'agent') handleUpdateProgress(data);
            else handleAgentUpdateProgress({ ...data, agent_id: 'agent-1' });
        }, { kind, data });
    };
}

for (const kind of ['agent', 'server']) {
    test(`${kind}: distinct phases, stale confirmation, recovery guidance and terminal replay`, async ({ page }) => {
        const emit = await open(page, kind, snapshot(1, 'downloading', { progress: 45 }));
        const status = page.locator(kind === 'agent' ? '#autoupdate_status_badge' : '#agent_update_status');
        const copy = page.locator(kind === 'agent' ? '#autoupdate_status_copy' : '#agent_update_status');
        const force = page.locator(kind === 'agent' ? '#autoupdate_force_btn' : '#agent_force_update_btn');
        const check = page.locator(kind === 'agent' ? '#autoupdate_check_btn' : '#agent_check_update_btn');
        await expect(status).toContainText('Downloading');
        if (kind === 'agent') {
            await expect(copy).toContainText('45%');
            await expect(force).toHaveText('Cancel Update');
        }

        await emit(snapshot(2, 'staging'));
        await expect(status).toContainText('Staging');
        await emit(snapshot(3, 'applying'));
        await expect(status).toContainText('Installing');
        await expect(force).toBeDisabled();
        await emit(snapshot(4, 'restarting'));
        await expect(status).toContainText('Restarting');
        await emit(snapshot(5, 'verifying', { updated_at: new Date(Date.now() - 120001).toISOString() }));
        await expect(status).toContainText('Awaiting confirmation');
        await expect(copy).toContainText('outcome is not yet known');
        await expect(check).toBeDisabled();
        await expect(force).toBeDisabled();
        const error = 'trust-state-recovery-required: Owner repair required; preserve the checkpoint and stop metadata writers before exact-checkpoint repair.';
        await emit(snapshot(6, 'failed', { error }));
        await expect(copy).toContainText(error);
        await page.clock.install();
        await page.clock.fastForward(10000);
        await expect(copy).toContainText(error);
        await emit(snapshot(5, 'restarting'));
        await expect(copy).toContainText(error);
        await page.reload();
        if (kind === 'agent') {
            if (page.viewportSize().width < 768) await page.locator('#mobile_bottom_tabs [data-target="settings"]').click();
            await page.evaluate(() => refreshAutoUpdateStatus());
        } else {
            await page.locator(page.viewportSize().width < 768 ? '#mobile_bottom_tabs [data-target="agents"]' : '#desktop_tabs [data-target="agents"]').click();
            await page.waitForFunction(() => agentsVM.items.length === 1);
            await page.evaluate(() => {
                renderAgentDetailsModal(agentsVM.items[0]);
                document.getElementById('agent_details_overlay').style.display = 'flex';
            });
        }
        await expect(copy).toContainText(error);
        const success = snapshot(1, 'succeeded', {
            run_id: 'two', run_started_at: '2026-10-22T12:01:00Z', progress: 100,
        });
        await emit(success);
        await expect(status).toContainText('Updated');
    });
}

test('upstream sync restores a missed final error after reload without treating dispatch as success', async ({ page }) => {
    await open(page, 'server', snapshot(1, 'idle'));
    let progress = {
        run_id: 'sync-one', run_started_at: new Date().toISOString(), sequence: 1,
        updated_at: new Date().toISOString(), deadline: new Date(Date.now() + 3600000).toISOString(),
        phase: 'fetching', message: 'Fetching upstream metadata',
    };
    await page.route('**/api/v1/releases/sync', route => route.fulfill({ json: {
        success: true, in_progress: !['error', 'complete'].includes(progress.phase), progress,
    } }));
    const showFleet = async () => {
        await page.evaluate(() => {
            document.getElementById('agent_details_overlay').style.display = 'none';
            switchTab('admin');
            switchAdminView('fleet');
        });
        await page.evaluate(() => refreshReleaseSyncStatus());
    };
    await showFleet();
    await expect(page.locator('#releases_sync_progress')).toContainText('Fetching upstream metadata');
    await expect(page.locator('#releases_sync_btn')).toBeDisabled();
    progress = { ...progress, sequence: 2, phase: 'error', error: 'Upstream download timed out; cached release is incomplete' };
    await page.reload();
    await page.waitForFunction(() => typeof switchTab === 'function');
    await showFleet();
    await expect(page.locator('#releases_sync_progress')).toContainText(progress.error);
    await expect(page.locator('#releases_sync_btn')).toBeEnabled();
    await expect(page.locator('#releases_sync_progress')).toBeVisible();
});
