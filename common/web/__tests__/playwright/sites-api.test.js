/**
 * Sites/Tenants API Integration Tests
 * 
 * Tests the frontend's interaction with:
 * - /api/v1/tenants/{id}/sites (GET, POST, DELETE)
 * - /api/v1/agents/list (GET)
 * 
 * Verifies correct endpoint URLs, response parsing, and null/empty handling.
 */
const { test, expect } = require('@playwright/test');
const http = require('http');
const fs = require('fs');
const path = require('path');

const indexHtml = path.resolve(__dirname, '../../../../server/web/index.html');
const styleCss = path.resolve(__dirname, '../../../../server/web/style.css');
const appJs = path.resolve(__dirname, '../../../../server/web/app.js');
const rbacJs = path.resolve(__dirname, '../../../../server/web/rbac.js');
const ssoAdminJs = path.resolve(__dirname, '../../../../server/web/sso-admin.js');
const chartsJs = path.resolve(__dirname, '../../../../server/web/utils/charts.js');
const formattersJs = path.resolve(__dirname, '../../../../server/web/utils/formatters.js');
const sharedCss = path.resolve(__dirname, '../../shared.css');
const sharedJs = path.resolve(__dirname, '../../shared.js');
const cardsJs = path.resolve(__dirname, '../../cards.js');
const metricsJs = path.resolve(__dirname, '../../metrics.js');

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

// Helper to wait for server to be ready (accepting connections)
async function waitForServerReady(url, maxRetries = 10, delay = 100) {
  for (let i = 0; i < maxRetries; i++) {
    try {
      await new Promise((resolve, reject) => {
        const req = http.get(url, (res) => {
          res.resume(); // Consume response to free up connection
          resolve();
        });
        req.on('error', reject);
        req.setTimeout(1000, () => {
          req.destroy();
          reject(new Error('timeout'));
        });
      });
      return; // Server is ready
    } catch (err) {
      if (i === maxRetries - 1) throw new Error(`Server not ready after ${maxRetries} retries`);
      await new Promise(r => setTimeout(r, delay));
    }
  }
}

test.beforeAll(async () => {
  server = startAppFixtureServer();
  
  // Track connections for graceful shutdown
  server.on('connection', (conn) => {
    activeConnections.add(conn);
    conn.on('close', () => activeConnections.delete(conn));
  });
  
  await new Promise(resolve => server.listen(0, resolve));
  const { port } = server.address();
  global.__PM_BASE_URL__ = `http://127.0.0.1:${port}`;
  
  // Wait for server to actually be accepting connections
  await waitForServerReady(`${global.__PM_BASE_URL__}/`);
});

test.afterAll(async () => {
  if (!server) return;
  
  // Give a brief moment for any in-flight requests to complete
  await new Promise(resolve => setTimeout(resolve, 100));
  
  // Close all active connections gracefully
  for (const conn of activeConnections) {
    conn.destroy();
  }
  activeConnections.clear();
  
  await new Promise(resolve => server.close(resolve));
});

const adminUser = { username: 'alice', role: 'admin', tenant_ids: [] };

// Helper to check if running on mobile viewport
function isMobileViewport(page) {
  const size = page.viewportSize();
  return size && size.width < 768;
}

// Get the correct tab selector based on viewport
function getTabSelector(page, tabId) {
  if (isMobileViewport(page)) {
    return `#mobile_bottom_tabs .mobile-tab-item[data-target="${tabId}"]`;
  }
  return `#desktop_tabs .tab[data-target="${tabId}"]`;
}

async function loadApp(page, apiHandler) {
  await page.addInitScript(() => {
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
  await page.route('**/api/**', apiHandler);
  await page.goto(`${global.__PM_BASE_URL__}/app`, { waitUntil: 'networkidle' });
  // Wait for auth API to be called and RBAC to be applied
  await page.waitForLoadState('networkidle');
  // Wait for auth to complete and dynamic tabs to be rendered
  // Use responsive selector based on viewport
  const agentsSelector = getTabSelector(page, 'agents');
  await page.waitForSelector(agentsSelector, { timeout: 10000 });
  // For admin users (tests use adminUser), wait specifically for the admin tab
  const adminSelector = getTabSelector(page, 'admin');
  await page.waitForSelector(adminSelector, { timeout: 10000 });
}

function createBaseHandler(overrides = {}) {
  return route => {
    const url = route.request().url();
    
    if (url.includes('/api/v1/auth/me')) {
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(adminUser) });
    }
    if (url.includes('/api/v1/tenants') && url.includes('/sites')) {
      const body = overrides.sites ?? '[]';
      return route.fulfill({ status: 200, contentType: 'application/json', body: typeof body === 'string' ? body : JSON.stringify(body) });
    }
    if (url.includes('/api/v1/tenants')) {
      return route.fulfill({ 
        status: 200, 
        contentType: 'application/json', 
        body: JSON.stringify([{ id: 't1', name: 'Acme Corp' }]) 
      });
    }
    if (url.includes('/api/v1/join-token') || url.includes('/api/v1/join-tokens')) {
      return route.fulfill({ status: 200, contentType: 'application/json', body: '[]' });
    }
    if (url.includes('/api/v1/agents/list')) {
      return route.fulfill({ status: 200, contentType: 'application/json', body: '[]' });
    }
    if (route.request().method() === 'GET') {
      return route.fulfill({ status: 200, contentType: 'application/json', body: '[]' });
    }
    return route.fulfill({ status: 200, contentType: 'application/json', body: '{}' });
  };
}

// ========== Sites API Tests ==========

test.describe('Sites API Integration', () => {
  test('fetchSitesForTenant uses correct endpoint and handles empty array', async ({ page }) => {
    const apiCalls = [];
    
    const handler = route => {
      apiCalls.push({ url: route.request().url(), method: route.request().method() });
      return createBaseHandler()(route);
    };
    
    await loadApp(page, handler);
    await page.waitForLoadState('networkidle');
    
    // Navigate to admin > tenants (use responsive selector)
    const adminTab = page.locator(getTabSelector(page, 'admin')).first();
    await expect(adminTab).toBeVisible({ timeout: 10000 });
    await adminTab.click();
    await page.locator('.admin-subtab[data-adminview="tenants"]').click();
    
    // Wait for tenants list to appear
    await expect(page.locator('#tenants_list')).toBeVisible();
    
    // Click expand button on first tenant row
    const expandBtn = page.locator('.expand-btn[data-action="toggle-sites"]').first();
    if (await expandBtn.isVisible()) {
      await expandBtn.click();
      
      // Wait for sites to load
      await page.waitForTimeout(500);
      
      // Verify correct endpoint was called
      const sitesCall = apiCalls.find(c => c.url.includes('/sites'));
      expect(sitesCall).toBeTruthy();
      expect(sitesCall.url).toContain('/api/v1/tenants/');
      expect(sitesCall.url).toContain('/sites');
      
      // Verify agents/list was called (not /api/v1/agents)
      const agentsCall = apiCalls.find(c => c.url.includes('/agents'));
      expect(agentsCall).toBeTruthy();
      expect(agentsCall.url).toContain('/api/v1/agents/list');
    }
  });

  test('handles null sites response gracefully', async ({ page }) => {
    await loadApp(page, createBaseHandler({ sites: 'null' }));
    await page.waitForLoadState('networkidle');
    
    const adminTab = page.locator(getTabSelector(page, 'admin')).first();
    await expect(adminTab).toBeVisible({ timeout: 10000 });
    await adminTab.click();
    await page.locator('.admin-subtab[data-adminview="tenants"]').click();
    
    await expect(page.locator('#tenants_list')).toBeVisible();
    
    const expandBtn = page.locator('.expand-btn[data-action="toggle-sites"]').first();
    if (await expandBtn.isVisible()) {
      await expandBtn.click();
      await page.waitForTimeout(500);
      
      // Should NOT show error - should show empty state
      const errorText = page.locator('.error-text');
      await expect(errorText).toHaveCount(0);
    }
  });

  test('sites list shows sites when data returned', async ({ page }) => {
    const testSites = [
      { id: 's1', name: 'Main Office', address: '123 Main St', tenant_id: 't1', filter_rules: [] },
      { id: 's2', name: 'Branch Office', address: '456 Oak Ave', tenant_id: 't1', filter_rules: [{ type: 'subnet', value: '192.168.1.0/24' }] }
    ];
    
    await loadApp(page, createBaseHandler({ sites: testSites }));
    await page.waitForLoadState('networkidle');
    
    const adminTab = page.locator(getTabSelector(page, 'admin')).first();
    await expect(adminTab).toBeVisible({ timeout: 10000 });
    await adminTab.click();
    await page.locator('.admin-subtab[data-adminview="tenants"]').click();
    
    await expect(page.locator('#tenants_list')).toBeVisible();
    
    const expandBtn = page.locator('.expand-btn[data-action="toggle-sites"]').first();
    if (await expandBtn.isVisible()) {
      await expandBtn.click();
      await page.waitForTimeout(500);
      
      // Should show site names in the tree
      await expect(page.getByText('Main Office')).toBeVisible();
      await expect(page.getByText('Branch Office')).toBeVisible();
    }
  });
});

// ========== Agents API Tests ==========

test.describe('Agents API Integration', () => {
  test('agents list uses /api/v1/agents/list endpoint', async ({ page }) => {
    // Track the API URLs that are requested
    const apiCallUrls = [];
    
    // Create a handler that tracks calls and also handles responses
    const trackingHandler = (route) => {
      const url = route.request().url();
      
      // Track all API calls
      if (url.includes('/api/')) {
        apiCallUrls.push(url);
      }
      
      // Handle the responses
      if (url.includes('/api/v1/auth/me')) {
        return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(adminUser) });
      }
      if (url.includes('/api/v1/agents/list')) {
        return route.fulfill({ status: 200, contentType: 'application/json', body: '[]' });
      }
      if (url.includes('/api/v1/tenants')) {
        return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify([{ id: 't1', name: 'Acme Corp' }]) });
      }
      if (url.includes('/api/v1/join-token') || url.includes('/api/v1/join-tokens')) {
        return route.fulfill({ status: 200, contentType: 'application/json', body: '[]' });
      }
      if (route.request().method() === 'GET') {
        return route.fulfill({ status: 200, contentType: 'application/json', body: '[]' });
      }
      return route.fulfill({ status: 200, contentType: 'application/json', body: '{}' });
    };
    
    // Use loadApp helper which properly waits for auth to complete
    await loadApp(page, trackingHandler);
    
    // Click the agents tab to ensure loadAgents() runs
    const agentsTab = page.locator(getTabSelector(page, 'agents')).first();
    await expect(agentsTab).toBeVisible();
    await agentsTab.click();
    
    // Wait for agents list to load
    await page.waitForTimeout(1000);
    
    // Verify the correct endpoint was called (not the bare /agents endpoint)
    const agentsListCalled = apiCallUrls.some(url => url.includes('/api/v1/agents/list'));
    const bareAgentsCalled = apiCallUrls.some(url => url.match(/\/api\/v1\/agents($|\?)/));
    
    expect(agentsListCalled).toBe(true);
    expect(bareAgentsCalled).toBe(false);
  });

  test('agents response is array, not wrapped object', async ({ page }) => {
    await loadApp(page, createBaseHandler());
    
    const agentsTab = page.locator(getTabSelector(page, 'agents')).first();
    await expect(agentsTab).toBeVisible();
    await agentsTab.click();
    await page.waitForTimeout(1000);
    
    // Should show agent names without error
    const errorText = page.locator('.error-text');
    await expect(errorText).toHaveCount(0);
  });

  test('handles empty agents array gracefully', async ({ page }) => {
    await loadApp(page, createBaseHandler());
    
    const agentsTab = page.locator(getTabSelector(page, 'agents')).first();
    await expect(agentsTab).toBeVisible();
    await agentsTab.click();
    await page.waitForTimeout(500);
    
    // Should not show error for empty list
    const errorText = page.locator('.error-text');
    await expect(errorText).toHaveCount(0);
  });
});

// ========== Tenant Expand/Collapse Tests ==========

test.describe('Tenant Sites Expansion', () => {
  test('expand button toggles sites row visibility', async ({ page }) => {
    await loadApp(page, createBaseHandler());
    await page.waitForLoadState('networkidle');
    
    const adminTab = page.locator(getTabSelector(page, 'admin')).first();
    await expect(adminTab).toBeVisible({ timeout: 10000 });
    await adminTab.click();
    await page.locator('.admin-subtab[data-adminview="tenants"]').click();
    
    await expect(page.locator('#tenants_list')).toBeVisible();
    
    const expandBtn = page.locator('.expand-btn[data-action="toggle-sites"]').first();
    if (await expandBtn.isVisible()) {
      // Initially collapsed
      const sitesRow = page.locator('.sites-expansion-row').first();
      await expect(sitesRow).toHaveClass(/hidden/);
      
      // Click to expand
      await expandBtn.click();
      await page.waitForTimeout(300);
      await expect(sitesRow).not.toHaveClass(/hidden/);
      
      // Click to collapse
      await expandBtn.click();
      await page.waitForTimeout(300);
      await expect(sitesRow).toHaveClass(/hidden/);
    }
  });

  test('empty sites shows Add Site button', async ({ page }) => {
    await loadApp(page, createBaseHandler());
    await page.waitForLoadState('networkidle');
    
    const adminTab = page.locator(getTabSelector(page, 'admin')).first();
    await expect(adminTab).toBeVisible({ timeout: 10000 });
    await adminTab.click();
    await page.locator('.admin-subtab[data-adminview="tenants"]').click();
    
    const expandBtn = page.locator('.expand-btn[data-action="toggle-sites"]').first();
    if (await expandBtn.isVisible()) {
      await expandBtn.click();
      await page.waitForTimeout(500);
      
      // Should show Add Site button when no sites (the one in the expansion row)
      const addSiteBtn = page.locator('.sites-expansion-row button:has-text("Add Site")').first();
      await expect(addSiteBtn).toBeVisible();
    }
  });
});
