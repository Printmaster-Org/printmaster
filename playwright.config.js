// Playwright config with adaptive defaults for local vs CI environments.
const os = require('os');

const cpuCount = Math.max(1, os.cpus()?.length || 1);
const defaultWorkers = process.env.CI
  ? 4
  : Math.min(6, Math.max(1, Math.floor(cpuCount * 0.5)));

const parsedWorkers = Number.parseInt(process.env.PW_WORKERS || '', 10);
const workers = Number.isFinite(parsedWorkers) && parsedWorkers > 0
  ? parsedWorkers
  : defaultWorkers;

const chromiumProjects = [
  { 
    name: 'chromium-desktop', 
    use: { 
      browserName: 'chromium',
      viewport: { width: 1920, height: 1080 }
    } 
  },
  { 
    name: 'chromium-desktop-small', 
    use: { 
      browserName: 'chromium',
      viewport: { width: 1280, height: 720 }
    } 
  },
  { 
    name: 'chromium-mobile', 
    use: { 
      browserName: 'chromium',
      viewport: { width: 390, height: 844 },
      isMobile: true,
      hasTouch: true
    } 
  }
];

const nonChromiumProjects = [
  // Note: Firefox does NOT support isMobile option, so no firefox-mobile project
  {
    name: 'firefox-desktop',
    use: {
      browserName: 'firefox',
      viewport: { width: 1920, height: 1080 }
    }
  },
  {
    name: 'webkit-desktop',
    use: {
      browserName: 'webkit',
      viewport: { width: 1920, height: 1080 }
    }
  },
  {
    name: 'webkit-mobile',
    use: {
      browserName: 'webkit',
      viewport: { width: 390, height: 844 },
      isMobile: true,
      hasTouch: true
    }
  }
];

const runAllBrowsers = process.env.CI === 'true' || process.env.PW_ALL_BROWSERS === '1';

module.exports = {
  testDir: './common/web/__tests__/playwright',
  testMatch: '*.test.js',
  timeout: 30000,
  workers,
  use: {
    headless: true,
    screenshot: 'only-on-failure',
  },
  projects: runAllBrowsers
    ? [...chromiumProjects, ...nonChromiumProjects]
    : chromiumProjects
};
