/**
 * Context Menu Tests
 * 
 * Tests the context menu system for agents and devices.
 * Uses serial execution with shared page to minimize load times.
 */
const { test, expect } = require('@playwright/test');
const http = require('http');
const fs = require('fs');
const path = require('path');

const indexHtml = path.resolve(__dirname, '../../../../server/web/index.html');
const styleCss = path.resolve(__dirname, '../../../../server/web/style.css');
const appJs = path.resolve(__dirname, '../../../../server/web/app.js');
const rbacJs = path.resolve(__dirname, '../../../../server/web/rbac.js');
const contextMenuJs = path.resolve(__dirname, '../../../../server/web/context-menu.js');
const ssoAdminJs = path.resolve(__dirname, '../../../../server/web/sso-admin.js');
const chartsJs = path.resolve(__dirname, '../../../../server/web/utils/charts.js');
const formattersJs = path.resolve(__dirname, '../../../../server/web/utils/formatters.js');
const sharedCss = path.resolve(__dirname, '../../shared.css');
const sharedJs = path.resolve(__dirname, '../../shared.js');
const cardsJs = path.resolve(__dirname, '../../cards.js');
const metricsJs = path.resolve(__dirname, '../../metrics.js');
const { inventoryResponse } = require('./inventory-fixture');

function serveFile(res, filePath, contentType) {
  const payload = fs.readFileSync(filePath);
  res.writeHead(200, { 'Content-Type': contentType });
  res.end(payload);
}

function normalizeStaticPath(pathname) {
  if (!pathname) return pathname;
  try {
    const decoded = decodeURIComponent(pathname);
    const [base] = decoded.split('{{');
    return base || decoded;
  } catch (err) {
    return pathname;
  }
}

function startAppFixtureServer() {
  return http.createServer((req, res) => {
    const url = new URL(req.url, 'http://localhost');
    try {
      if (url.pathname === '/' || url.pathname === '/app') {
        return serveFile(res, indexHtml, 'text/html');
      }
      const staticRoutes = {
        '/static/style.css': { file: styleCss, type: 'text/css' },
        '/static/shared.css': { file: sharedCss, type: 'text/css' },
        '/static/shared.js': { file: sharedJs, type: 'application/javascript' },
        '/static/cards.js': { file: cardsJs, type: 'application/javascript' },
        '/static/progressive-loader.js': { file: path.resolve(__dirname, '../../progressive-loader.js'), type: 'application/javascript' },
        '/static/metrics.js': { file: metricsJs, type: 'application/javascript' },
        '/static/utils/charts.js': { file: chartsJs, type: 'application/javascript' },
        '/static/utils/formatters.js': { file: formattersJs, type: 'application/javascript' },
        '/static/app.js': { file: appJs, type: 'application/javascript' },
        '/static/rbac.js': { file: rbacJs, type: 'application/javascript' },
        '/static/context-menu.js': { file: contextMenuJs, type: 'application/javascript' },
        '/static/sso-admin.js': { file: ssoAdminJs, type: 'application/javascript' },
      };
      const normalizedPath = normalizeStaticPath(url.pathname);
      if (staticRoutes[normalizedPath]) {
        const entry = staticRoutes[normalizedPath];
        return serveFile(res, entry.file, entry.type);
      }
      if (url.pathname === '/favicon.ico') {
        res.writeHead(204);
        return res.end();
      }
      res.writeHead(404, { 'Content-Type': 'text/plain' });
      res.end('not found');
    } catch (err) {
      console.error('fixture server error', err);
      if (!res.headersSent) {
        res.writeHead(500, { 'Content-Type': 'text/plain' });
      }
      res.end('fixture server error');
    }
  });
}

let server;
let activeConnections = new Set();

async function waitForServerReady(url, maxRetries = 10, delay = 100) {
  for (let i = 0; i < maxRetries; i++) {
    try {
      await new Promise((resolve, reject) => {
        const req = http.get(url, (res) => {
          res.resume();
          resolve();
        });
        req.on('error', reject);
        req.setTimeout(1000, () => {
          req.destroy();
          reject(new Error('timeout'));
        });
      });
      return;
    } catch (err) {
      if (i === maxRetries - 1) throw new Error(`Server not ready after ${maxRetries} retries`);
      await new Promise(r => setTimeout(r, delay));
    }
  }
}

// Force serial execution - one worker only
test.describe.configure({ mode: 'serial' });

test.beforeAll(async () => {
  server = startAppFixtureServer();

  server.on('connection', (conn) => {
    activeConnections.add(conn);
    conn.on('close', () => activeConnections.delete(conn));
  });

  await new Promise(resolve => server.listen(0, resolve));
  const { port } = server.address();
  global.__PM_BASE_URL__ = `http://127.0.0.1:${port}`;

  await waitForServerReady(`${global.__PM_BASE_URL__}/`);
});

test.afterAll(async () => {
  if (!server) return;
  await new Promise(resolve => setTimeout(resolve, 100));
  for (const conn of activeConnections) {
    conn.destroy();
  }
  activeConnections.clear();
  await new Promise(resolve => server.close(resolve));
});

const adminUser = { username: 'alice', role: 'admin', tenant_ids: [] };

const sampleAgents = [
  {
    agent_id: 'agent-001',
    name: 'Office Agent',
    ip: '192.168.1.100',
    version: '0.29.0',
    status: 'online',
    ws_connected: true,
    device_count: 5,
    last_seen: new Date().toISOString()
  }
];

const sampleDevices = [
  {
    serial: 'ABC123',
    model: 'HP LaserJet Pro',
    ip: '192.168.1.50',
    mac: '00:11:22:33:44:55',
    status: 'healthy',
    agent_id: 'agent-001',
    agent_name: 'Office Agent'
  }
];

function createApiHandler(apiCalls = []) {
  return route => {
    const url = route.request().url();
    const method = route.request().method();

    // Track POST calls for verification
    if (method === 'POST') {
      const postData = route.request().postData();
      apiCalls.push({ url, method, body: postData ? JSON.parse(postData) : null });
    }

    if (url.includes('/api/v1/auth/me')) {
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(adminUser) });
    }
    if (url.includes('/api/v1/agents/list')) {
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(sampleAgents) });
    }
    if (url.includes('/api/v1/devices/delete') && method === 'POST') {
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ success: true, deleted_from_agent: true }) });
    }
    if (url.includes('/api/v1/devices')) {
      const response = inventoryResponse(route, sampleDevices);
      if (response) return response;
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(sampleDevices) });
    }
    if (url.includes('/api/v1/tenants')) {
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify([{ id: 't1', name: 'Test Tenant' }]) });
    }
    if (method === 'GET') {
      return route.fulfill({ status: 200, contentType: 'application/json', body: '[]' });
    }
    return route.fulfill({ status: 200, contentType: 'application/json', body: '{}' });
  };
}

// Helper to check if running on mobile viewport
function isMobileViewport(page) {
  const size = page.viewportSize();
  return size && size.width < 768;
}

async function openContextMenu(page, target, contextMenu, { force = false } = {}) {
  for (let attempt = 0; attempt < 3; attempt++) {
    // Give any previous menu's outside-click listener (registered via a 10ms
    // setTimeout in showContextMenu) time to fully detach before reopening,
    // otherwise the reopened menu can be torn down/rebuilt out from under us.
    await page.waitForTimeout(50);
    await target.click({ button: 'right', force });
    try {
      await expect(contextMenu).toBeVisible({ timeout: 2000 });
      await expect(contextMenu.locator('.pm-context-menu-item').first()).toBeVisible({ timeout: 2000 });
      return;
    } catch (error) {
      if (attempt === 2) throw error;
      await page.keyboard.press('Escape');
      await page.waitForTimeout(50);
    }
  }
}

// Opens the context menu and clicks a menu item, retrying the whole open+click
// sequence if the menu gets torn down/rebuilt between opening and clicking
// (observed intermittently as "element was detached from the DOM, retrying").
async function openContextMenuAndClickAction(page, target, contextMenu, action) {
  for (let attempt = 0; attempt < 3; attempt++) {
    await openContextMenu(page, target, contextMenu);
    try {
      await contextMenu.locator(`[data-action="${action}"]`).click({ timeout: 5000 });
      return;
    } catch (error) {
      if (attempt === 2) throw error;
    }
  }
}

// Opens the context menu and asserts each of `actions` is visible, retrying
// the whole open+verify sequence if the menu gets torn down between opening
// and checking a later item (the same class of race openContextMenuAndClickAction
// guards against - only the first item is guaranteed checked by openContextMenu).
async function openContextMenuAndVerifyItems(page, target, contextMenu, actions, options) {
  for (let attempt = 0; attempt < 3; attempt++) {
    await openContextMenu(page, target, contextMenu, options);
    try {
      for (const action of actions) {
        await expect(contextMenu.locator(`[data-action="${action}"]`)).toBeVisible({ timeout: 2000 });
      }
      return;
    } catch (error) {
      if (attempt === 2) throw error;
    }
  }
}

// ========== Device Context Menu - Single Page Load ==========

test('device context menu: appearance, actions, delete flow', async ({ page, browserName }) => {
  test.skip(isMobileViewport(page), 'Context menus are desktop-only (mobile uses different navigation)');
  // Grant clipboard permissions upfront (Chromium only - Firefox/WebKit don't support these permissions)
  if (browserName === 'chromium') {
    await page.context().grantPermissions(['clipboard-read', 'clipboard-write']);
  }

  const apiCalls = [];

  // Setup mocks
  await page.addInitScript(() => {
    window.EventSource = class {
      constructor() { this.readyState = 1; }
      addEventListener() { }
      close() { }
    };
    window.WebSocket = class {
      constructor() { }
      addEventListener() { }
      close() { }
    };
  });
  // Disable CSS animations/transitions so modal open/slide-in animations don't
  // leave elements in a transient "unstable" state for Playwright's actionability
  // checks (this caused intermittent click timeouts on WebKit).
  await page.addInitScript(() => {
    const style = document.createElement('style');
    style.textContent = '*, *::before, *::after { animation-duration: 0s !important; animation-delay: 0s !important; transition-duration: 0s !important; transition-delay: 0s !important; }';
    document.documentElement.appendChild(style);
  });
  await page.route('**/api/**', createApiHandler(apiCalls));

  // Load app once. Avoid 'networkidle' - it's unreliable under CI resource
  // contention (background polling can prevent it from ever settling); the
  // explicit selector wait below is the real readiness signal.
  await page.goto(`${global.__PM_BASE_URL__}/app`, { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#desktop_tabs .tab[data-target="devices"]', { timeout: 10000 });

  // Navigate to devices
  await page.locator('#desktop_tabs [data-target="devices"]').first().click();
  await page.waitForSelector('[data-serial="ABC123"]', { timeout: 10000 });

  // Wait for context menu handlers to be bound (race condition fix)
  // The DOM element appears before initDeviceContextMenu() runs
  await page.waitForFunction(() => {
    const table = document.getElementById('devices_table');
    const cards = document.getElementById('devices_cards');
    return (table && table.dataset.contextMenuBound === 'true') ||
      (cards && cards.dataset.contextMenuBound === 'true');
  }, { timeout: 5000 });

  // Small stabilization wait to ensure event handlers are fully attached
  await page.waitForTimeout(100);

  // Clear any lingering selection state so this test always exercises the
  // single-device menu path, even if a prior interaction left multiple items
  // selected in the same browser context.
  await page.evaluate(() => {
    const selection = window.devicesVM?.selection;
    if (selection) {
      selection.selectedIds.clear();
      selection.lastSelected = null;
    }
  });

  // --- Test 1: Context menu appears on right-click ---
  const deviceElement = page.locator('.saved-device-card[data-serial="ABC123"], .device-card[data-serial="ABC123"], tr[data-serial="ABC123"]').first();

  const contextMenu = page.locator('.pm-context-menu');
  await openContextMenuAndVerifyItems(page, deviceElement, contextMenu, [
    'show-printer-details',
    'copy-serial',
    'delete-device',
  ]);

  // Verify delete has danger styling
  const deleteItem = contextMenu.locator('[data-action="delete-device"]');
  await expect(deleteItem).toHaveClass(/danger/);

  // Verify dividers exist
  await expect(contextMenu.locator('.pm-context-menu-divider').first()).toBeVisible();

  // --- Test 2: Menu closes on escape ---
  await page.keyboard.press('Escape');
  await expect(contextMenu).not.toBeVisible({ timeout: 2000 });

  // --- Test 3: Menu closes on outside click ---
  await openContextMenu(page, deviceElement, contextMenu);
  await page.click('body', { position: { x: 10, y: 10 } });
  await expect(contextMenu).not.toBeVisible({ timeout: 2000 });

  // --- Test 4: Copy serial shows toast ---
  await openContextMenu(page, deviceElement, contextMenu);
  await contextMenu.locator('[data-action="copy-serial"]').click();

  await expect(page.locator('.toast:visible').first()).toBeVisible({ timeout: 3000 });
  await expect(page.locator('.toast:visible').first()).toContainText('copied');

  // Wait for all toasts to clear from DOM (3s display + 300ms exit animation)
  await page.waitForFunction(() => document.querySelectorAll('.toast').length === 0, { timeout: 8000 });

  // --- Test 5: Copy IP shows toast ---
  await openContextMenu(page, deviceElement, contextMenu);
  await contextMenu.locator('[data-action="copy-device-ip"]').click();

  await expect(page.locator('.toast:visible').first()).toBeVisible({ timeout: 3000 });
  await page.waitForFunction(() => document.querySelectorAll('.toast').length === 0, { timeout: 8000 });

  // --- Test 6: Delete device shows confirmation modal ---
  await openContextMenuAndClickAction(page, deviceElement, contextMenu, 'delete-device');

  const modal = page.locator('.modal-overlay:visible');
  await expect(modal).toBeVisible({ timeout: 5000 });
  await expect(modal.locator('text=ABC123')).toBeVisible();
  await expect(modal.locator('text=Also delete metrics history')).toBeVisible();
  await expect(modal.locator('text=Also delete from agent')).toBeVisible();

  const deleteMetricsCheckbox = modal.locator('input[id$="_delete_metrics"]');
  const deleteFromAgentCheckbox = modal.locator('input[id$="_delete_from_agent"]');

  // --- Test 7: Modal checkboxes work ---
  await expect(deleteMetricsCheckbox).not.toBeChecked();
  await deleteMetricsCheckbox.check({ force: true });
  await expect(deleteMetricsCheckbox).toBeChecked();
  await deleteFromAgentCheckbox.check({ force: true });
  await expect(deleteFromAgentCheckbox).toBeChecked();

  // --- Test 8: Cancel closes modal without API call ---
  apiCalls.length = 0; // Clear tracked calls
  await modal.locator('button:has-text("Cancel")').click({ force: true });
  await expect(modal).not.toBeVisible({ timeout: 2000 });
  expect(apiCalls.filter(c => c.url.includes('/delete'))).toHaveLength(0);

  // --- Test 9: Delete sends correct API request ---
  await openContextMenuAndClickAction(page, deviceElement, contextMenu, 'delete-device');
  await expect(modal).toBeVisible({ timeout: 5000 });

  // Check both options
  await deleteMetricsCheckbox.check({ force: true });
  await deleteFromAgentCheckbox.check({ force: true });

  // Click delete
  await modal.locator('button:has-text("Delete")').click({ force: true });

  // Wait for API call
  await page.waitForTimeout(500);

  const deleteCall = apiCalls.find(c => c.url.includes('/devices/delete'));
  expect(deleteCall).toBeTruthy();
  expect(deleteCall.body.serial).toBe('ABC123');
  expect(deleteCall.body.delete_metrics).toBe(true);
  expect(deleteCall.body.delete_from_agent).toBe(true);
  expect(deleteCall.body.agent_id).toBe('agent-001');
});

// ========== Agent Context Menu - Single Page Load ==========

test('agent context menu: appearance and actions', async ({ page, browserName }) => {
  test.skip(isMobileViewport(page), 'Context menus are desktop-only (mobile uses different navigation)');

  // Grant clipboard permissions (Chromium only - Firefox/WebKit don't support these permissions)
  if (browserName === 'chromium') {
    await page.context().grantPermissions(['clipboard-read', 'clipboard-write']);
  }

  await page.addInitScript(() => {
    window.EventSource = class {
      constructor() { this.readyState = 1; }
      addEventListener() { }
      close() { }
    };
    window.WebSocket = class {
      constructor() { }
      addEventListener() { }
      close() { }
    };
  });
  await page.route('**/api/**', createApiHandler());

  await page.goto(`${global.__PM_BASE_URL__}/app`, { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#desktop_tabs .tab[data-target="agents"]', { timeout: 10000 });

  // Navigate to agents (should already be there by default)
  await page.locator('#desktop_tabs [data-target="agents"]').first().click();
  await page.waitForSelector('[data-agent-id="agent-001"]', { timeout: 10000 });

  // Wait for context menu handlers to be bound (race condition fix)
  // The DOM element appears before initAgentContextMenu() runs
  await page.waitForFunction(() => {
    const table = document.getElementById('agents_table');
    const cards = document.getElementById('agents_cards');
    return (table && table.dataset.contextMenuBound === 'true') ||
      (cards && cards.dataset.contextMenuBound === 'true');
  }, { timeout: 5000 });

  // Small stabilization wait to ensure event handlers are fully attached
  await page.waitForTimeout(100);

  const agentElement = page.locator('[data-agent-id="agent-001"]').first();
  await expect(agentElement).toBeVisible();

  // --- Test 1: Context menu appears ---
  const contextMenu = page.locator('.pm-context-menu');
  await openContextMenuAndVerifyItems(page, agentElement, contextMenu, [
    'view-agent',
    'copy-agent-id',
    'delete-agent',
  ], { force: true });

  // Verify delete has danger styling
  await expect(contextMenu.locator('[data-action="delete-agent"]')).toHaveClass(/danger/);

  // --- Test 2: Copy agent ID shows toast ---
  await contextMenu.locator('[data-action="copy-agent-id"]').click();

  await expect(page.locator('.toast:visible').first()).toBeVisible({ timeout: 3000 });
  await expect(page.locator('.toast:visible').first()).toContainText('Agent ID');

  await page.waitForFunction(() => document.querySelectorAll('.toast').length === 0, { timeout: 5000 });

  // --- Test 3: Copy IP shows toast ---
  await agentElement.click({ button: 'right' });
  await expect(contextMenu).toBeVisible({ timeout: 5000 });
  await contextMenu.locator('[data-action="copy-agent-ip"]').click();

  await expect(page.locator('.toast:visible').first()).toBeVisible({ timeout: 3000 });
  await page.waitForFunction(() => document.querySelectorAll('.toast').length === 0, { timeout: 5000 });

  // --- Test 4: Menu closes on escape ---
  await agentElement.click({ button: 'right' });
  await expect(contextMenu).toBeVisible({ timeout: 5000 });
  await page.keyboard.press('Escape');
  await expect(contextMenu).not.toBeVisible({ timeout: 2000 });
});
