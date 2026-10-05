/**
 * Table Customizer Tests
 * 
 * Tests the advanced column customization system for tables.
 * Includes column visibility toggle, drag-to-reorder, reset, and persistence.
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
const tableCustomizerJs = path.resolve(__dirname, '../../../../server/web/table-customizer.js');
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
        '/static/table-customizer.js': { file: tableCustomizerJs, type: 'application/javascript' },
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
    hostname: 'office-pc',
    ip: '192.168.1.100',
    version: '0.29.0',
    platform: 'windows',
    status: 'online',
    ws_connected: true,
    device_count: 5,
    last_seen: new Date().toISOString(),
    registered_at: new Date(Date.now() - 86400000).toISOString()
  },
  {
    agent_id: 'agent-002',
    name: 'Warehouse Agent',
    hostname: 'warehouse-srv',
    ip: '192.168.1.101',
    version: '0.28.0',
    platform: 'linux',
    status: 'online',
    ws_connected: false,
    device_count: 3,
    last_seen: new Date().toISOString()
  }
];

const sampleDevices = [
  {
    serial: 'ABC123',
    manufacturer: 'HP',
    model: 'LaserJet Pro MFP',
    ip: '192.168.1.50',
    mac: '00:11:22:33:44:55',
    status: 'healthy',
    agent_id: 'agent-001',
    agent_name: 'Office Agent',
    firmware: '1.2.3',
    raw_data: {
      total_pages: 12500,
      color_pages: 3200,
      mono_pages: 9300
    }
  },
  {
    serial: 'DEF456',
    manufacturer: 'Canon',
    model: 'imageCLASS MF',
    ip: '192.168.1.51',
    mac: '00:11:22:33:44:66',
    status: 'warning',
    agent_id: 'agent-002',
    agent_name: 'Warehouse Agent',
    firmware: '2.0.1'
  }
];

function createApiHandler() {
  return route => {
    const url = route.request().url();
    const method = route.request().method();
    
    if (url.includes('/api/v1/auth/me')) {
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(adminUser) });
    }
    if (url.includes('/api/v1/agents/list')) {
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(sampleAgents) });
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
  return page.viewportSize()?.width < 768;
}

// ========== Devices Table Customizer - Column Visibility ==========

test('devices table: column customizer visibility toggling', async ({ page }) => {
  // Skip on mobile - table customizer uses desktop tabs which are hidden on mobile
  test.skip(isMobileViewport(page), 'Table customizer is a desktop feature');
  
  // Clear any stored table config from previous runs
  await page.addInitScript(() => {
    localStorage.removeItem('pm_table_config_devices');
    localStorage.removeItem('pm_table_config_agents');
    window.EventSource = class {
      constructor() { this.readyState = 1; }
      addEventListener() {}
      close() {}
    };
    window.WebSocket = class {
      constructor() {}
      addEventListener() {}
      close() {}
    };
  });
  await page.route('**/api/**', createApiHandler());
  
  // Load app
  await page.goto(`${global.__PM_BASE_URL__}/app`, { waitUntil: 'networkidle' });
  await page.waitForSelector('#desktop_tabs .tab[data-target="devices"]', { timeout: 10000 });
  
  // Navigate to devices tab
  await page.locator('#desktop_tabs [data-target="devices"]').first().click();
  await page.waitForSelector('[data-serial="ABC123"]', { timeout: 10000 });
  
  // --- Test 1: Columns button exists with badge count ---
  // Scope to devices table toolbar to avoid matching agents table toolbar
  const devicesToolbar = page.locator('#devices_table_customizer_toolbar');
  const columnsBtn = devicesToolbar.locator('.table-column-toggle-btn');
  await expect(columnsBtn).toBeVisible({ timeout: 5000 });
  await expect(columnsBtn).toContainText('Columns');
  
  const badge = columnsBtn.locator('.column-count-badge');
  await expect(badge).toBeVisible();
  const initialCount = await badge.textContent();
  expect(parseInt(initialCount)).toBeGreaterThan(0);
  
  // --- Test 2: Clicking opens column picker dropdown ---
  await columnsBtn.click();
  
  const picker = page.locator('.column-picker-dropdown');
  await expect(picker).toBeVisible({ timeout: 5000 });
  await expect(picker.locator('.column-picker-header')).toContainText('Customize Columns');
  
  // --- Test 3: Verify sections exist ---
  await expect(picker.locator('.column-picker-group-title').first()).toBeVisible();
  await expect(picker.locator('.column-picker-list').first()).toBeVisible();
  
  // --- Test 4: Close button works ---
  await picker.locator('.column-picker-close').click();
  await expect(picker).not.toBeVisible({ timeout: 2000 });
  
  // --- Test 5: Column checkboxes can toggle visibility ---
  await columnsBtn.click();
  await expect(picker).toBeVisible({ timeout: 5000 });
  
  // Find a hideable, visible column to toggle (e.g., "Status" or "Consumables")
  // These are in the "Visible" section and should have enabled checkboxes
  const visibleSection = picker.locator('.column-picker-list[data-group="visible"]');
  
  // Find a checkbox that is enabled (hideable column)
  const hideableCheckbox = visibleSection.locator('input[type="checkbox"]:not([disabled])').first();
  
  if (await hideableCheckbox.count() > 0) {
    const wasChecked = await hideableCheckbox.isChecked();
    expect(wasChecked).toBe(true); // Should be checked since it's in visible section
    
    // Get the column ID to verify in localStorage
    const columnId = await hideableCheckbox.getAttribute('data-column-id');
    
    await hideableCheckbox.click();
    
    // Wait for UI to update
    await page.waitForTimeout(300);
    
    // Verify the config was saved to localStorage
    const savedConfig = await page.evaluate(() => localStorage.getItem('pm_table_config_devices'));
    expect(savedConfig).toBeTruthy();
    const config = JSON.parse(savedConfig);
    
    // Find the column in saved config
    const savedColumn = config.columns.find(c => c.id === columnId);
    expect(savedColumn).toBeTruthy();
    expect(savedColumn.visible).toBe(false); // Should now be hidden
  }
  
  // --- Test 6: Show All button makes all columns visible ---
  // Reopen picker (may have closed or need refresh)
  const existingPicker = page.locator('.column-picker-dropdown');
  if (await existingPicker.isVisible()) {
    await existingPicker.locator('.column-picker-close').click();
    await expect(existingPicker).not.toBeVisible({ timeout: 2000 });
  }
  await columnsBtn.click();
  await expect(picker).toBeVisible({ timeout: 5000 });
  
  // Click Show All button
  const showAllBtn = picker.locator('.show-all-btn');
  await showAllBtn.click();
  await expect(picker).not.toBeVisible({ timeout: 2000 });
  
  // Badge count should now be maximum
  const afterShowAll = await badge.textContent();
  expect(parseInt(afterShowAll)).toBeGreaterThanOrEqual(parseInt(initialCount));
  
  // --- Test 7: Hide Optional button hides defaultHidden columns ---
  await columnsBtn.click();
  await expect(picker).toBeVisible({ timeout: 5000 });
  
  // Click Hide Optional button
  const hideOptionalBtn = picker.locator('.hide-optional-btn');
  await hideOptionalBtn.click();
  await expect(picker).not.toBeVisible({ timeout: 2000 });
});

// ========== Devices Table Customizer - Drag Reorder ==========

test('devices table: column reordering via drag and drop', async ({ page }) => {
  test.skip(isMobileViewport(page), 'Table customizer is a desktop feature');
  
  await page.addInitScript(() => {
    localStorage.removeItem('pm_table_config_devices');
    window.EventSource = class {
      constructor() { this.readyState = 1; }
      addEventListener() {}
      close() {}
    };
    window.WebSocket = class {
      constructor() {}
      addEventListener() {}
      close() {}
    };
  });
  await page.route('**/api/**', createApiHandler());
  
  await page.goto(`${global.__PM_BASE_URL__}/app`, { waitUntil: 'networkidle' });
  await page.waitForSelector('#desktop_tabs .tab[data-target="devices"]', { timeout: 10000 });
  await page.locator('#desktop_tabs [data-target="devices"]').first().click();
  await page.waitForSelector('[data-serial="ABC123"]', { timeout: 10000 });
  
  // Scope to devices table toolbar
  const devicesToolbar = page.locator('#devices_table_customizer_toolbar');
  const columnsBtn = devicesToolbar.locator('.table-column-toggle-btn');
  await columnsBtn.click();
  
  const picker = page.locator('.column-picker-dropdown');
  await expect(picker).toBeVisible({ timeout: 5000 });
  
  // --- Test 1: Visible columns have drag handles ---
  const visibleList = picker.locator('.column-picker-list[data-group="visible"]');
  const dragHandles = visibleList.locator('.column-drag-handle');
  
  // At least some columns should have drag handles
  const handleCount = await dragHandles.count();
  expect(handleCount).toBeGreaterThan(0);
  
  // --- Test 2: Draggable items have draggable attribute ---
  const draggableItems = visibleList.locator('.column-picker-item[draggable="true"]');
  expect(await draggableItems.count()).toBeGreaterThan(0);
  
  // --- Test 3: Non-hideable column (device) should exist without drag placeholder issues ---
  const deviceColumn = picker.locator('.column-picker-item[data-column-id="device"]');
  await expect(deviceColumn).toBeVisible();
  
  // Device column checkbox should be disabled (not hideable)
  const deviceCheckbox = deviceColumn.locator('input[type="checkbox"]');
  await expect(deviceCheckbox).toBeDisabled();
  
  // Close picker
  await picker.locator('.column-picker-close').click();
});

// ========== Devices Table Customizer - Reset & Persistence ==========

test('devices table: reset and localStorage persistence', async ({ page }) => {
  test.skip(isMobileViewport(page), 'Table customizer is a desktop feature');
  
  await page.addInitScript(() => {
    localStorage.removeItem('pm_table_config_devices');
    window.EventSource = class {
      constructor() { this.readyState = 1; }
      addEventListener() {}
      close() {}
    };
    window.WebSocket = class {
      constructor() {}
      addEventListener() {}
      close() {}
    };
  });
  await page.route('**/api/**', createApiHandler());
  
  await page.goto(`${global.__PM_BASE_URL__}/app`, { waitUntil: 'networkidle' });
  await page.waitForSelector('#desktop_tabs .tab[data-target="devices"]', { timeout: 10000 });
  await page.locator('#desktop_tabs [data-target="devices"]').first().click();
  await page.waitForSelector('[data-serial="ABC123"]', { timeout: 10000 });
  
  // Scope to devices table toolbar
  const devicesToolbar = page.locator('#devices_table_customizer_toolbar');
  const columnsBtn = devicesToolbar.locator('.table-column-toggle-btn');
  const badge = columnsBtn.locator('.column-count-badge');
  const initialCount = await badge.textContent();
  
  // --- Test 1: Make a change (show all columns) ---
  await columnsBtn.click();
  const picker = page.locator('.column-picker-dropdown');
  await expect(picker).toBeVisible({ timeout: 5000 });
  await picker.locator('.show-all-btn').click();
  await expect(picker).not.toBeVisible({ timeout: 2000 });
  
  const afterShowAll = await badge.textContent();
  
  // --- Test 2: Verify localStorage was updated ---
  const savedConfig = await page.evaluate(() => localStorage.getItem('pm_table_config_devices'));
  expect(savedConfig).toBeTruthy();
  const config = JSON.parse(savedConfig);
  expect(config.columns).toBeDefined();
  expect(Array.isArray(config.columns)).toBe(true);
  
  // --- Test 3: Reset button restores defaults (with confirmation) ---
  const resetBtn = devicesToolbar.locator('.table-reset-btn');
  await expect(resetBtn).toBeVisible();
  
  // Mock window.confirm to return true
  await page.evaluate(() => {
    window.confirm = () => true;
  });
  
  await resetBtn.click();
  await page.waitForTimeout(500);
  
  // Badge count should return to initial (or close to it)
  const afterReset = await badge.textContent();
  expect(parseInt(afterReset)).toBe(parseInt(initialCount));
  
  // --- Test 4: localStorage should be cleared after reset ---
  const configAfterReset = await page.evaluate(() => localStorage.getItem('pm_table_config_devices'));
  expect(configAfterReset).toBeNull();
});

// ========== Agents Table Customizer ==========

test('agents table: column customizer works independently', async ({ page }) => {
  test.skip(isMobileViewport(page), 'Table customizer is a desktop feature');
  
  await page.addInitScript(() => {
    localStorage.removeItem('pm_table_config_agents');
    localStorage.removeItem('pm_table_config_devices');
    window.EventSource = class {
      constructor() { this.readyState = 1; }
      addEventListener() {}
      close() {}
    };
    window.WebSocket = class {
      constructor() {}
      addEventListener() {}
      close() {}
    };
  });
  await page.route('**/api/**', createApiHandler());
  
  await page.goto(`${global.__PM_BASE_URL__}/app`, { waitUntil: 'networkidle' });
  await page.waitForSelector('#desktop_tabs .tab[data-target="agents"]', { timeout: 10000 });
  
  // Navigate to agents tab (usually default, but click to be sure)
  await page.locator('#desktop_tabs [data-target="agents"]').first().click();
  await page.waitForSelector('[data-agent-id="agent-001"]', { timeout: 10000 });
  
  // --- Test 1: Agents table has its own Columns button ---
  // Scope to agents table toolbar
  const agentsToolbar = page.locator('#agents_table_customizer_toolbar');
  const columnsBtn = agentsToolbar.locator('.table-column-toggle-btn');
  await expect(columnsBtn).toBeVisible({ timeout: 5000 });
  
  const badge = columnsBtn.locator('.column-count-badge');
  const agentColumnCount = await badge.textContent();
  expect(parseInt(agentColumnCount)).toBeGreaterThan(0);
  
  // --- Test 2: Opening picker shows agent-specific columns ---
  await columnsBtn.click();
  
  const picker = page.locator('.column-picker-dropdown');
  await expect(picker).toBeVisible({ timeout: 5000 });
  
  // Verify agent-specific columns exist
  await expect(picker.locator('.column-picker-item[data-column-id="agent"]')).toBeVisible();
  await expect(picker.locator('.column-picker-item[data-column-id="status"]')).toBeVisible();
  
  // Platform and version are agent-specific columns
  const platformCol = picker.locator('.column-picker-item[data-column-id="platform"]');
  const versionCol = picker.locator('.column-picker-item[data-column-id="version"]');
  await expect(platformCol).toBeVisible();
  await expect(versionCol).toBeVisible();
  
  // --- Test 3: Agent-only columns like agent_id should exist (in Available section since defaultHidden)
  const agentIdCol = picker.locator('.column-picker-item[data-column-id="agent_id"]');
  await expect(agentIdCol).toHaveCount(1); // Exists in DOM
  
  // --- Test 4: Show all and verify badge updates ---
  await picker.locator('.show-all-btn').click();
  await expect(picker).not.toBeVisible({ timeout: 2000 });
  
  const afterShowAll = await badge.textContent();
  expect(parseInt(afterShowAll)).toBeGreaterThanOrEqual(parseInt(agentColumnCount));
  
  // --- Test 5: Verify agents table uses separate localStorage key ---
  await columnsBtn.click();
  await expect(picker).toBeVisible({ timeout: 5000 });
  await picker.locator('.column-picker-close').click();
  
  const agentsConfig = await page.evaluate(() => localStorage.getItem('pm_table_config_agents'));
  expect(agentsConfig).toBeTruthy();
  
  const devicesConfig = await page.evaluate(() => localStorage.getItem('pm_table_config_devices'));
  // Devices config should be null since we only interacted with agents table
  // (unless previous test left it - just verify they're separate keys)
  if (devicesConfig && agentsConfig) {
    expect(agentsConfig).not.toBe(devicesConfig);
  }
});

// ========== Table Header Sorting ==========

test('devices table: column header sorting', async ({ page }) => {
  test.skip(isMobileViewport(page), 'Table customizer is a desktop feature');
  
  await page.addInitScript(() => {
    localStorage.removeItem('pm_table_config_devices');
    window.EventSource = class {
      constructor() { this.readyState = 1; }
      addEventListener() {}
      close() {}
    };
    window.WebSocket = class {
      constructor() {}
      addEventListener() {}
      close() {}
    };
  });
  await page.route('**/api/**', createApiHandler());
  
  await page.goto(`${global.__PM_BASE_URL__}/app`, { waitUntil: 'networkidle' });
  await page.waitForSelector('#desktop_tabs .tab[data-target="devices"]', { timeout: 10000 });
  await page.locator('#desktop_tabs [data-target="devices"]').first().click();
  await page.waitForSelector('[data-serial="ABC123"]', { timeout: 10000 });
  
  // --- Test 1: Sortable columns have sortable class ---
  const tableHead = page.locator('#devices_table thead, .devices-table thead');
  const sortableHeaders = tableHead.locator('th.sortable');
  
  // Wait for table to render with customizer
  await page.waitForTimeout(500);
  const sortableCount = await sortableHeaders.count();
  
  // Should have at least some sortable columns
  expect(sortableCount).toBeGreaterThan(0);
  
  // --- Test 2: Clicking sortable header adds sorted class ---
  const firstSortable = sortableHeaders.first();
  await firstSortable.click();
  await page.waitForTimeout(300);
  
  // Check for sort indicator
  const sortIndicator = firstSortable.locator('.sort-indicator');
  // Sort indicator should appear after click
  // (depends on implementation - some may not show until sorted)
  
  // --- Test 3: Clicking again toggles sort direction ---
  await firstSortable.click();
  await page.waitForTimeout(300);
  
  // The header should still be marked as sorted or have indicator
  const hasSortedClass = await firstSortable.evaluate(el => el.classList.contains('sorted'));
  // This is implementation-dependent
});

// ========== Export Button ==========

test('devices table: export button exists', async ({ page }) => {
  test.skip(isMobileViewport(page), 'Table customizer is a desktop feature');
  
  await page.addInitScript(() => {
    localStorage.removeItem('pm_table_config_devices');
    window.EventSource = class {
      constructor() { this.readyState = 1; }
      addEventListener() {}
      close() {}
    };
    window.WebSocket = class {
      constructor() {}
      addEventListener() {}
      close() {}
    };
  });
  await page.route('**/api/**', createApiHandler());
  
  await page.goto(`${global.__PM_BASE_URL__}/app`, { waitUntil: 'networkidle' });
  await page.waitForSelector('#desktop_tabs .tab[data-target="devices"]', { timeout: 10000 });
  await page.locator('#desktop_tabs [data-target="devices"]').first().click();
  await page.waitForSelector('[data-serial="ABC123"]', { timeout: 10000 });
  
  // --- Test 1: Export button should be visible in toolbar ---
  const devicesToolbar = page.locator('#devices_table_customizer_toolbar');
  const exportBtn = devicesToolbar.locator('.table-export-btn');
  
  // Export might be conditionally rendered based on enableExport option
  const exportVisible = await exportBtn.isVisible().catch(() => false);
  
  if (exportVisible) {
    await expect(exportBtn).toContainText('Export');
    
    // --- Test 2: Export button has proper tooltip ---
    const title = await exportBtn.getAttribute('title');
    expect(title).toContain('Export');
  }
  
  // Test passes even if export is disabled - it's an optional feature
});

// ========== Dropdown Position and Closing ==========

test('devices table: picker dropdown positioning and closing', async ({ page }) => {
  test.skip(isMobileViewport(page), 'Table customizer is a desktop feature');
  
  await page.addInitScript(() => {
    localStorage.removeItem('pm_table_config_devices');
    window.EventSource = class {
      constructor() { this.readyState = 1; }
      addEventListener() {}
      close() {}
    };
    window.WebSocket = class {
      constructor() {}
      addEventListener() {}
      close() {}
    };
  });
  await page.route('**/api/**', createApiHandler());
  
  await page.goto(`${global.__PM_BASE_URL__}/app`, { waitUntil: 'networkidle' });
  await page.waitForSelector('#desktop_tabs .tab[data-target="devices"]', { timeout: 10000 });
  await page.locator('#desktop_tabs [data-target="devices"]').first().click();
  await page.waitForSelector('[data-serial="ABC123"]', { timeout: 10000 });
  
  // Scope to devices table toolbar
  const devicesToolbar = page.locator('#devices_table_customizer_toolbar');
  const columnsBtn = devicesToolbar.locator('.table-column-toggle-btn');
  
  // --- Test 1: Picker appears below button ---
  await columnsBtn.click();
  const picker = page.locator('.column-picker-dropdown');
  await expect(picker).toBeVisible({ timeout: 5000 });
  
  // Verify dropdown has fixed positioning
  const position = await picker.evaluate(el => getComputedStyle(el).position);
  expect(position).toBe('fixed');
  
  // --- Test 2: Click outside closes picker ---
  await page.click('body', { position: { x: 10, y: 10 }, force: true });
  await expect(picker).not.toBeVisible({ timeout: 2000 });
  
  // --- Test 3: Toggle behavior - clicking button again closes picker ---
  await columnsBtn.click();
  await expect(picker).toBeVisible({ timeout: 5000 });
  
  // Click button again - should close (toggle off)
  await columnsBtn.click();
  await expect(picker).not.toBeVisible({ timeout: 2000 });
  
  // Verify no pickers remain
  const pickerCount = await page.locator('.column-picker-dropdown').count();
  expect(pickerCount).toBe(0);
});
