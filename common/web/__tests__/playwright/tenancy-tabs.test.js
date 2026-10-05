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

async function mockApi(page, user) {
  await page.route('**/api/**', route => {
    const url = route.request().url();
    if (url.includes('/api/v1/devices/rows') || url.includes('/api/v1/devices/metrics/query')) {
      return route.fulfill({ status: 200, contentType: 'application/json', body: '[]' });
    }
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

test('Admin alert setup label differs from operator Alerts destination', async ({ page }) => {
  await loadApp(page, adminUser);
  await expect(page.locator(getTabSelector(page, 'alerts'))).toHaveText('Alerts');
  await page.locator(getTabSelector(page, 'admin')).click();
  await expect(page.locator('.admin-subtab[data-adminview="alertsconfig"]')).toHaveText('Alert Setup');
});

test('Audit keeps common filters visible and groups advanced filters', async ({ page }) => {
  await loadApp(page, adminUser);
  await page.locator(getTabSelector(page, 'admin')).click();
  await page.locator('.admin-subtab[data-adminview="audit"]').click();

  const advanced = page.locator('.audit-advanced-filters');
  await expect(advanced).toBeVisible();
  await expect(advanced).not.toHaveAttribute('open', '');
  await expect(page.locator('#audit_time_filter')).toBeVisible();
  await expect(page.locator('#audit_search_filter')).toBeVisible();
  await expect(page.locator('#audit_actor_filter')).toBeHidden();

  await advanced.locator('summary').click();
  await expect(page.locator('#audit_actor_filter')).toBeVisible();
  await expect(page.locator('#audit_tenant_filter')).toBeVisible();
  await expect(page.locator('#audit_action_filter')).toBeVisible();
});

test('Access keeps SSO primary and collapses reference sections', async ({ page }) => {
  await loadApp(page, adminUser);
  await page.locator(getTabSelector(page, 'admin')).click();
  await page.locator('.admin-subtab[data-adminview="access"]').click();

  await expect(page.locator('#sso_add_provider_btn')).toBeVisible();
  const disclosures = page.locator('.access-disclosure');
  await expect(disclosures).toHaveCount(2);
  await expect(disclosures.nth(0)).not.toHaveAttribute('open', '');
  await expect(disclosures.nth(1)).not.toHaveAttribute('open', '');
  await expect(page.locator('#roles_matrix')).toBeHidden();
  await expect(page.locator('#sessions_refresh_btn')).toBeHidden();

  await disclosures.nth(1).locator('summary').click();
  await expect(page.locator('#sessions_refresh_btn')).toBeVisible();
  await disclosures.nth(0).locator('summary').focus();
  await page.keyboard.press('Enter');
  await expect(page.locator('#roles_matrix')).toBeVisible();
});

test('viewer does not see admin tab', async ({ page }) => {
  await loadApp(page, viewerUser);
  const adminTab = page.locator(getTabSelector(page, 'admin'));
  await expect(adminTab).toBeHidden();
});

test('server filter drawers float over results and start closed', async ({ page }) => {
  await loadApp(page, adminUser);
  let finishVersionCheck;
  const versionCheckGate = new Promise(resolve => { finishVersionCheck = resolve; });
  await page.route('**/api/v1/releases/latest-agent-version', async route => {
    await versionCheckGate;
    await route.fulfill({ status: 200, contentType: 'application/json', body: '{}' });
  });
  const expectStationaryControl = async control => {
    if (isMobileViewport(page)) return;
    const beforeHover = await control.boundingBox();
    await control.hover();
    await expect(control).toHaveCSS('transform', 'none');
    await expect(control).toHaveCSS('filter', 'none');
    const afterHover = await control.boundingBox();
    expect(afterHover).toEqual(beforeHover);
    // Exercise the bottom edge too: hover must not move the hit target away.
    await control.click({ position: { x: beforeHover.width / 2, y: beforeHover.height - 2 } });
  };
  const drawers = [
    { tab: 'dashboard', sidebar: 'dashboard_sidebar', trigger: 'dashboard_filters_open', close: 'dashboard_sidebar_toggle', main: '.dashboard-main' },
    { tab: 'agents', sidebar: 'agents_sidebar', trigger: 'agents_filters_open', close: 'agents_sidebar_toggle', main: '.agents-main' },
    { tab: 'devices', sidebar: 'devices_sidebar', trigger: 'devices_filters_open', close: 'devices_sidebar_toggle', main: '.devices-main' },
    { tab: 'logs', sidebar: 'logs_sidebar', trigger: 'logs_filters_open', close: 'logs_sidebar_toggle', main: '.logs-main' },
  ];

  for (const drawerConfig of drawers) {
    const tab = page.locator(getTabSelector(page, drawerConfig.tab));
    await tab.click();
    await expect(tab).toHaveCSS('filter', 'none');
    if (drawerConfig.tab === 'agents') {
      // loadAgents schedules an async version check after rendering. Its
      // Checking… -> Check for Updates label changes header width, independently
      // of hover. Measure hover geometry only after that operation completes.
      const checkUpdates = page.locator('#agents_check_updates_btn');
      await expect(checkUpdates).toHaveText('Checking…');
      finishVersionCheck();
      await expect(checkUpdates).toHaveText('Check for Updates');
      await expect(checkUpdates).toBeEnabled();
    }
    const sidebar = page.locator(`#${drawerConfig.sidebar}`);
    const trigger = page.locator(`#${drawerConfig.trigger}`);
    const main = page.locator(drawerConfig.main);
    await expect(sidebar).toBeHidden();
    await expect(trigger).toHaveAttribute('aria-expanded', 'false');

    const before = await main.boundingBox();
    if (isMobileViewport(page)) await trigger.click();
    else await expectStationaryControl(trigger);
    await expect(sidebar).toBeVisible();
    await expect(trigger).toHaveAttribute('aria-expanded', 'true');
    await expect(sidebar).toHaveCSS('position', 'absolute');

    const after = await main.boundingBox();
    expect(after.x).toBeCloseTo(before.x, 0);
    expect(after.width).toBeCloseTo(before.width, 0);

    const close = page.locator(`#${drawerConfig.close}`);
    if (isMobileViewport(page)) await close.click();
    else await expectStationaryControl(close);
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
  if (isMobileViewport(page)) await tenantTrigger.click();
  else await expectStationaryControl(tenantTrigger);
  await expect(tenantSidebar).toBeVisible();
  await expect(tenantSidebar).toHaveCSS('position', 'absolute');
  const tenantAfter = await tenantMain.boundingBox();
  expect(tenantAfter.x).toBeCloseTo(tenantBefore.x, 0);
  expect(tenantAfter.width).toBeCloseTo(tenantBefore.width, 0);
  const tenantClose = page.locator('#tenants_sidebar_toggle');
  if (isMobileViewport(page)) await tenantClose.click();
  else await expectStationaryControl(tenantClose);
  await expect(tenantSidebar).toBeHidden();
});

test('server settings navigate by category without discarding edits', async ({ page }) => {
  await loadApp(page, adminUser);
  await page.route('**/api/v1/server/settings', route => route.fulfill({
    status: 200,
    contentType: 'application/json',
    body: JSON.stringify({
      version: '1.2.3',
      config_source: 'config.toml',
      tenancy_enabled: true,
      database: { path: '/var/lib/printmaster/server.db' },
      server: { http_port: 9090, https_port: 9443, bind_address: '0.0.0.0', agent_timeout_minutes: 5 },
      security: { rate_limit_enabled: true, rate_limit_max_attempts: 5, rate_limit_block_minutes: 15, rate_limit_window_minutes: 5 },
      tls: { mode: 'self-signed' },
      logging: { level: 'INFO' },
      releases: { max_releases: 10, poll_interval_minutes: 60, retention_versions: 5 },
      self_update: { enabled: false, channel: 'stable', max_artifacts: 5, check_interval_minutes: 60 },
      smtp: { enabled: false, host: '', port: 587, user: '', from: '', email_theme: 'auto' },
      notifications: { enabled: false, admin_emails: '', notify_on_critical: true, notify_on_warning: true, daily_summary_enabled: false },
    }),
  }));
  await page.route('**/api/v1/server/settings/sources', route => route.fulfill({
    status: 200,
    contentType: 'application/json',
    body: JSON.stringify({ locked_keys: [] }),
  }));

  await page.locator(getTabSelector(page, 'admin')).click();
  await page.locator('.admin-subtab[data-adminview="server"]').click();
  const navigation = page.locator('.server-settings-nav');
  const mobileNavigation = page.locator('#server_settings_section_select');
  const isMobile = page.viewportSize().width <= 900;
  if (isMobile) {
    await expect(mobileNavigation).toBeVisible();
    await expect(mobileNavigation.locator('option')).toHaveCount(8);
  } else {
    await expect(navigation).toBeVisible();
    await expect(navigation.locator('.server-settings-nav-item')).toHaveCount(8);
  }
  await expect(page.locator('#server_settings_section_server')).toBeVisible();

  await page.locator('#server_setting_server_bind_address').fill('127.0.0.1');
  if (isMobile) {
    await mobileNavigation.selectOption('tls');
  } else {
    await navigation.locator('[data-server-settings-target="tls"]').click();
  }
  await expect(page.locator('#server_settings_section_tls')).toBeVisible();
  await expect(page.locator('#server_settings_section_tls label', { hasText: "Let's Encrypt Domain" })).toHaveCount(1);
  await expect(page.locator('#server_settings_section_server')).toBeHidden();
  if (isMobile) {
    await mobileNavigation.selectOption('server');
    const actions = await page.locator('.server-settings-actions').boundingBox();
    const bottomTabs = await page.locator('#mobile_bottom_tabs').boundingBox();
    expect(actions.height).toBeLessThanOrEqual(80);
    expect(actions.y + actions.height).toBeLessThanOrEqual(bottomTabs.y + 1);
  } else {
    await navigation.locator('[data-server-settings-target="server"]').click();
  }
  await expect(page.locator('#server_setting_server_bind_address')).toHaveValue('127.0.0.1');
  await expect(page.locator('#server_settings_save_btn')).toBeEnabled();
  await page.locator('#server_settings_discard_btn').click();
  await expect(page.locator('#server_setting_server_bind_address')).toHaveValue('0.0.0.0');
});

test('alert configuration keeps advanced sections collapsed by default', async ({ page }) => {
  await page.addInitScript(() => {
    if (!sessionStorage.getItem('alerts-collapse-test-initialized')) {
      localStorage.removeItem('alertsSectionsCollapsed');
      sessionStorage.setItem('alerts-collapse-test-initialized', 'true');
    }
  });
  await loadApp(page, adminUser);
  await page.locator(getTabSelector(page, 'admin')).click();
  await page.locator('.admin-subtab[data-adminview="alertsconfig"]').click();

  await expect(page.locator('#alert_rules_section')).not.toHaveClass(/collapsed/);
  const collapsedByDefault = [
    'notification_channels_section',
    'escalation_policies_section',
    'maintenance_windows_section',
    'quiet_hours_section',
    'flapping_section',
    'grouping_section',
    'dependencies_section',
    'report_scheduling_section',
  ];
  for (const sectionId of collapsedByDefault) {
    await expect(page.locator(`#${sectionId}`)).toHaveClass(/collapsed/);
  }
  await expect(page.locator('#new_notification_channel_btn')).toBeVisible();
  await expect(page.locator('#new_escalation_policy_btn')).toBeVisible();
  await expect(page.locator('#new_maintenance_window_btn')).toBeVisible();

  await page.locator('#notification_channels_section .alerts-section-header').click();
  await expect(page.locator('#notification_channels_section')).not.toHaveClass(/collapsed/);
  await page.reload({ waitUntil: 'networkidle' });
  await page.locator(getTabSelector(page, 'admin')).click();
  await page.locator('.admin-subtab[data-adminview="alertsconfig"]').click();
  await expect(page.locator('#notification_channels_section')).not.toHaveClass(/collapsed/);
});

test('metrics charts use a responsive grid', async ({ page }) => {
  await loadApp(page, viewerUser);
  await page.locator(getTabSelector(page, 'metrics')).click();

  const grid = page.locator('#metrics_chart_grid');
  await expect(grid).toHaveCSS('display', 'grid');
  if (page.viewportSize().width > 900) {
    const desktopColumns = await grid.evaluate(element => getComputedStyle(element).gridTemplateColumns.split(' ').length);
    expect(desktopColumns).toBeGreaterThanOrEqual(2);
  }
  const emptyCardHeights = await grid.locator('.metric-chart-card:has(.no-data-placeholder)').evaluateAll(cards =>
    cards.map(card => card.getBoundingClientRect().height)
  );
  expect(emptyCardHeights.length).toBeGreaterThan(0);
  expect(Math.max(...emptyCardHeights)).toBeLessThanOrEqual(220);

  await page.setViewportSize({ width: 390, height: 844 });
  await page.reload({ waitUntil: 'networkidle' });
  await page.locator('#mobile_bottom_tabs [data-target="metrics"]').click();
  await expect(page.locator('#metrics_chart_grid')).toHaveCSS('display', 'grid');
  const mobileColumns = await page.locator('#metrics_chart_grid').evaluate(element => getComputedStyle(element).gridTemplateColumns.split(' ').length);
  expect(mobileColumns).toBe(1);
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
