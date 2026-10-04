const { test, expect } = require('@playwright/test');
const http = require('http');
const fs = require('fs');
const path = require('path');

const loginHtmlPath = path.resolve(__dirname, '../../../../server/web/login.html');
const acceptInviteHtmlPath = path.resolve(__dirname, '../../../../server/web/accept-invite.html');
const loginJsPath = path.resolve(__dirname, '../../../../server/web/login.js');
const loginCssPath = path.resolve(__dirname, '../../../../server/web/style.css');
const sharedCssPath = path.resolve(__dirname, '../../shared.css');
const sharedJsPath = path.resolve(__dirname, '../../shared.js');

let server;
let baseURL;

function serveFile(res, filePath, contentType) {
  const payload = fs.readFileSync(filePath);
  res.writeHead(200, { 'Content-Type': contentType });
  res.end(payload);
}

async function waitForLocalLoginSection(page, timeout = 5000) {
  const section = page.locator('#local_login_section');
  await expect(section).toBeVisible({ timeout });
  return section;
}

function waitForAuthOptions(page) {
  return page.waitForResponse(resp => resp.url().includes('/api/v1/auth/options') && resp.status() === 200);
}

function startFixtureServer() {
  return http.createServer((req, res) => {
    try {
      const url = new URL(req.url, 'http://localhost');
      if (url.pathname === '/login') {
        return serveFile(res, loginHtmlPath, 'text/html');
      }
      if (url.pathname === '/accept-invite') {
        return serveFile(res, acceptInviteHtmlPath, 'text/html');
      }
      if (url.pathname === '/' || url.pathname === '/app') {
        res.writeHead(200, { 'Content-Type': 'text/html' });
        res.end('<html><body>OK</body></html>');
        return;
      }
      if (url.pathname === '/static/shared.css') {
        return serveFile(res, sharedCssPath, 'text/css');
      }
      if (url.pathname === '/static/shared.js') {
        return serveFile(res, sharedJsPath, 'application/javascript');
      }
      if (url.pathname === '/static/style.css') {
        return serveFile(res, loginCssPath, 'text/css');
      }
      if (url.pathname === '/static/login.js') {
        return serveFile(res, loginJsPath, 'application/javascript');
      }

      res.writeHead(404, { 'Content-Type': 'text/plain' });
      res.end('not found');
    } catch (err) {
      console.error('fixture server error', err);
      if (!res.headersSent) {
        res.writeHead(500, { 'Content-Type': 'text/plain' });
      }
      if (!res.writableEnded) {
        res.end('fixture server error: ' + err.message);
      }
    }
  });
}

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
  server = startFixtureServer();
  
  // Track connections for graceful shutdown
  server.on('connection', (conn) => {
    activeConnections.add(conn);
    conn.on('close', () => activeConnections.delete(conn));
  });
  
  await new Promise(resolve => server.listen(0, resolve));
  const address = server.address();
  baseURL = `http://127.0.0.1:${address.port}`;
  
  // Wait for server to actually be accepting connections
  await waitForServerReady(`${baseURL}/`);
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

test('login and invitation links retain theme contrast and keyboard focus cues', async ({ page }) => {
  await page.route('**/api/v1/auth/options**', route => route.fulfill({
    status: 200,
    contentType: 'application/json',
    body: JSON.stringify({ local_login: true, providers: [] }),
  }));
  await page.route('**/api/v1/users/invite/validate**', route => route.fulfill({
    status: 400,
    contentType: 'text/plain',
    body: 'Invitation expired',
  }));

  const authPages = [
    { url: '/login', linkName: 'Back to home' },
    { url: '/accept-invite?token=expired', linkName: 'Go to login' },
  ];

  for (const authPage of authPages) {
    await page.goto(`${baseURL}${authPage.url}`);
    const link = page.getByRole('link', { name: authPage.linkName });
    await expect(link).toBeVisible();

    for (const lightMode of [false, true]) {
      await page.locator('body').evaluate((body, isLightMode) => {
        body.classList.toggle('light-mode', isLightMode);
      }, lightMode);
      const linkStyles = await link.evaluate(element => {
        const themeColorProbe = document.createElement('span');
        themeColorProbe.style.color = getComputedStyle(document.body).getPropertyValue('--muted').trim();
        document.body.appendChild(themeColorProbe);
        const themeColor = getComputedStyle(themeColorProbe).color;
        themeColorProbe.remove();
        return {
          color: getComputedStyle(element).color,
          themeColor,
          decoration: getComputedStyle(element).textDecorationLine,
        };
      });
      expect(linkStyles.color).toBe(linkStyles.themeColor);
      expect(linkStyles.decoration).toContain('underline');
    }

    await link.focus();
    await expect(link).toBeFocused();
    expect(await link.evaluate(element => getComputedStyle(element).outlineStyle)).toBe('solid');
    expect(await link.evaluate(element => getComputedStyle(element).outlineWidth)).toBe('2px');
  }
});

test('renders SSO provider button and starts OIDC flow', async ({ page }) => {
  await page.route('**/api/v1/auth/options**', route => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        local_login: false,
        providers: [
          { slug: 'entra', display_name: 'Entra', button_text: 'Sign in with Entra', auto_login: false }
        ],
      }),
    });
  });
  await page.route('**/auth/oidc/start/**', route => {
    route.fulfill({ status: 200, body: 'redirected' });
  });

  const authOptionsLoaded = waitForAuthOptions(page);
  await page.goto(`${baseURL}/login?tenant=acme`);
  await authOptionsLoaded;
  const ssoButton = page.getByRole('button', { name: 'Sign in with Entra' });
  await expect(ssoButton).toBeVisible();

  const startRequest = page.waitForRequest('**/auth/oidc/start/**');
  await ssoButton.click();
  const request = await startRequest;
  expect(request.url()).toContain('/auth/oidc/start/entra');
  expect(request.url()).toContain('tenant=acme');
});

test('auto-login provider redirects without interaction', async ({ page }) => {
  await page.route('**/api/v1/auth/options**', route => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        local_login: false,
        providers: [
          { slug: 'auto', display_name: 'Auto', auto_login: true }
        ],
      }),
    });
  });
  await page.route('**/auth/oidc/start/**', route => route.fulfill({ status: 200, body: 'auto' }));

  const startRequest = page.waitForRequest('**/auth/oidc/start/**');
  await page.goto(`${baseURL}/login`);
  const request = await startRequest;
  expect(request.url()).toContain('/auth/oidc/start/auto');
});

test('local login form submits and navigates to redirect target', async ({ page }) => {
  await page.route('**/api/v1/auth/options**', route => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ local_login: true, providers: [] }),
    });
  });
  await page.route('**/api/v1/auth/login', route => {
    route.fulfill({ status: 200, contentType: 'application/json', body: '{}' });
  });

  const authOptionsLoaded = waitForAuthOptions(page);
  await page.goto(`${baseURL}/login?redirect=%2Fapp`);
  await authOptionsLoaded;
  await waitForLocalLoginSection(page);
  const usernameInput = page.locator('#login_username');
  const passwordInput = page.locator('#login_password');
  await usernameInput.fill('admin', { timeout: 5000 });
  await passwordInput.fill('secret', { timeout: 5000 });

  const navigation = page.waitForURL('**/app');
  await page.click('#login_submit');
  await navigation;
  await expect(page).toHaveURL(/\/app$/);
});
