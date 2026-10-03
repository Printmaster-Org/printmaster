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

async function mockApi(page, user) {
  await page.route('**/api/**', route => {
    const url = route.request().url();
    if (url.includes('/api/v1/auth/me')) {
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(user) });
    }
    if (url.includes('/api/v1/tenants')) {
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify([{ id: 't1', name: 'Acme Corp' }]) });
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
  });
}

async function loadApp(page, user) {
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
  await mockApi(page, user);
  await page.goto(`${global.__PM_BASE_URL__}/app`, { waitUntil: 'networkidle' });
  // Wait for auth API to be called and RBAC to be applied
  await page.waitForLoadState('networkidle');
  // Wait for auth to complete and dynamic tabs to be rendered
  // Use responsive selector based on viewport
  const agentsSelector = getTabSelector(page, 'agents');
  await page.waitForSelector(agentsSelector, { timeout: 10000 });
  // For admin and operator users, wait specifically for the admin tab to appear
  if (user && (user.role === 'admin' || user.role === 'operator')) {
    const adminSelector = getTabSelector(page, 'admin');
    await page.waitForSelector(adminSelector, { timeout: 10000 });
  }
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
const operatorUser = { username: 'oliver', role: 'operator', tenant_ids: ['t1'] };
const viewerUser = { username: 'victor', role: 'viewer', tenant_ids: ['t1'] };

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

test('admin sees tenants in admin tab', async ({ page }) => {
  await loadApp(page, adminUser);
  // Wait for dynamic tabs to be created after auth
  await page.waitForLoadState('networkidle');
  // Navigate to Admin tab (use responsive selector)
  const adminTab = page.locator(getTabSelector(page, 'admin')).first();
  await expect(adminTab).toBeVisible({ timeout: 10000 });
  await adminTab.click();
  // Switch to Tenants sub-view
  const tenantsSubtab = page.locator('.admin-subtab[data-adminview="tenants"]');
  await expect(tenantsSubtab).toBeVisible();
  await tenantsSubtab.click();
  // New tenant button should be visible
  await expect(page.locator('#new_tenant_btn')).toBeVisible();
});

test('mobile admin navigation fits without horizontal scrolling', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await loadApp(page, adminUser);

  const mobileTabs = page.locator('#mobile_bottom_tabs .mobile-tab-item');
  await expect(mobileTabs).toHaveCount(7);

  for (const width of [320, 390]) {
    await page.setViewportSize({ width, height: 844 });
    await page.waitForFunction(() => {
      const nav = document.getElementById('mobile_bottom_tabs');
      return nav && Math.abs(nav.getBoundingClientRect().width - document.documentElement.clientWidth) < 1;
    });

    const layout = await page.locator('.mobile-bottom-tabs-inner').evaluate(element => ({
      clientWidth: element.clientWidth,
      scrollWidth: element.scrollWidth,
      viewportWidth: document.documentElement.clientWidth,
      tabs: Array.from(element.querySelectorAll('.mobile-tab-item')).map(tab => {
        const rect = tab.getBoundingClientRect();
        return { target: tab.dataset.target, left: rect.left, right: rect.right };
      }),
    }));

    expect(layout.scrollWidth).toBeLessThanOrEqual(layout.clientWidth);
    expect(layout.tabs.map(tab => tab.target)).toContain('admin');
    expect(layout.tabs.every(tab => tab.left >= 0 && tab.right <= layout.viewportWidth + 1)).toBe(true);
    await expect(page.locator('#mobile_bottom_tabs [data-target="admin"]')).toBeVisible();
  }
});

test('operator can see admin tab but only fleet and alerts subtabs', async ({ page }) => {
  await loadApp(page, operatorUser);
  await page.waitForLoadState('networkidle');
  
  // Operators CAN see the admin tab (with granular settings permissions)
  const adminTab = page.locator(getTabSelector(page, 'admin')).first();
  await expect(adminTab).toBeVisible({ timeout: 10000 });
  await adminTab.click();
  
  // Operators should see Fleet and Alerts Config subtabs
  await expect(page.locator('.admin-subtab[data-adminview="fleet"]')).toBeVisible();
  await expect(page.locator('.admin-subtab[data-adminview="alertsconfig"]')).toBeVisible();
  
  // Operators should NOT see admin-only subtabs (users, access, tenants, server, audit)
  await expect(page.locator('.admin-subtab[data-adminview="users"]')).toBeHidden();
  await expect(page.locator('.admin-subtab[data-adminview="access"]')).toBeHidden();
  await expect(page.locator('.admin-subtab[data-adminview="tenants"]')).toBeHidden();
  await expect(page.locator('.admin-subtab[data-adminview="server"]')).toBeHidden();
  await expect(page.locator('.admin-subtab[data-adminview="audit"]')).toBeHidden();
});

test('viewer cannot see add agent button', async ({ page }) => {
  await loadApp(page, viewerUser);
  await expect(page.locator('#join_token_btn')).toBeHidden();
});

test('admin can view audit subtab in admin tab', async ({ page }) => {
  await loadApp(page, adminUser);
  // Wait for dynamic tabs to be created after auth
  await page.waitForLoadState('networkidle');
  await page.locator(getTabSelector(page, 'admin')).click();
  const auditSubtab = page.locator('.admin-subtab[data-adminview="audit"]');
  await expect(auditSubtab).toBeVisible();
});

test('viewer does not see admin tab', async ({ page }) => {
  await loadApp(page, viewerUser);
  const adminTab = page.locator(getTabSelector(page, 'admin'));
  await expect(adminTab).toBeHidden();
});

test('server filter drawers float over results and start closed', async ({ page }) => {
  await loadApp(page, adminUser);
  const drawers = [
    { tab: 'dashboard', sidebar: 'dashboard_sidebar', trigger: 'dashboard_filters_open', close: 'dashboard_sidebar_toggle', main: '.dashboard-main' },
    { tab: 'agents', sidebar: 'agents_sidebar', trigger: 'agents_filters_open', close: 'agents_sidebar_toggle', main: '.agents-main' },
    { tab: 'devices', sidebar: 'devices_sidebar', trigger: 'devices_filters_open', close: 'devices_sidebar_toggle', main: '.devices-main' },
    { tab: 'logs', sidebar: 'logs_sidebar', trigger: 'logs_filters_open', close: 'logs_sidebar_toggle', main: '.logs-main' },
  ];

  for (const drawerConfig of drawers) {
    await page.locator(getTabSelector(page, drawerConfig.tab)).click();
    const sidebar = page.locator(`#${drawerConfig.sidebar}`);
    const trigger = page.locator(`#${drawerConfig.trigger}`);
    const main = page.locator(drawerConfig.main);
    await expect(sidebar).toBeHidden();
    await expect(trigger).toHaveAttribute('aria-expanded', 'false');

    const before = await main.boundingBox();
    await trigger.click();
    await expect(sidebar).toBeVisible();
    await expect(trigger).toHaveAttribute('aria-expanded', 'true');
    await expect(sidebar).toHaveCSS('position', 'absolute');

    const after = await main.boundingBox();
    expect(after.x).toBeCloseTo(before.x, 0);
    expect(after.width).toBeCloseTo(before.width, 0);

    await page.locator(`#${drawerConfig.close}`).click();
    await expect(sidebar).toBeHidden();
    await expect(trigger).toHaveAttribute('aria-expanded', 'false');
  }

  await page.locator(getTabSelector(page, 'admin')).click();
  await page.locator('.admin-subtab[data-adminview="tenants"]').click();
  const tenantSidebar = page.locator('#tenants_sidebar');
  const tenantTrigger = page.locator('#tenants_filters_open');
  const tenantMain = page.locator('.tenants-main');
  await expect(tenantSidebar).toBeHidden();
  const tenantBefore = await tenantMain.boundingBox();
  await tenantTrigger.click();
  await expect(tenantSidebar).toBeVisible();
  await expect(tenantSidebar).toHaveCSS('position', 'absolute');
  const tenantAfter = await tenantMain.boundingBox();
  expect(tenantAfter.x).toBeCloseTo(tenantBefore.x, 0);
  expect(tenantAfter.width).toBeCloseTo(tenantBefore.width, 0);
  await page.locator('#tenants_sidebar_toggle').click();
  await expect(tenantSidebar).toBeHidden();
});

test('muted text meets contrast on server surfaces in both themes', async ({ page }) => {
  await loadApp(page, viewerUser);

  const contrast = await page.evaluate(() => {
    const luminance = color => {
      const channels = color.match(/[0-9a-f]{2}/gi).map(value => parseInt(value, 16) / 255);
      const linear = channels.map(value => value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4);
      return 0.2126 * linear[0] + 0.7152 * linear[1] + 0.0722 * linear[2];
    };
    const ratio = (foreground, background) => {
      const a = luminance(foreground);
      const b = luminance(background);
      return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
    };
    const result = {};
    for (const theme of ['dark', 'light']) {
      document.body.classList.toggle('light-mode', theme === 'light');
      const styles = getComputedStyle(document.body);
      const muted = styles.getPropertyValue('--muted').trim();
      const surfaces = [styles.getPropertyValue('--panel').trim(), styles.getPropertyValue('--bg').trim()];
      result[theme] = Math.min(...surfaces.map(surface => ratio(muted, surface)));
    }
    return result;
  });

  expect(contrast.dark).toBeGreaterThanOrEqual(4.5);
  expect(contrast.light).toBeGreaterThanOrEqual(4.5);
});

test('main workspace shell remains stationary on hover', async ({ page }) => {
  await loadApp(page, viewerUser);
  const shell = page.locator('.content-container');
  const before = await shell.boundingBox();
  await page.mouse.move(before.x + before.width / 2, before.y + before.height / 2);
  await expect(shell).toHaveCSS('transform', 'none');
  const after = await shell.boundingBox();

  expect(after.x).toBe(before.x);
  expect(after.y).toBe(before.y);
  expect(after.width).toBe(before.width);
  expect(after.height).toBe(before.height);
});
