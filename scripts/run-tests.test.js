const fs = require('fs');
const os = require('os');
const path = require('path');
const { spawnSync } = require('child_process');
const yaml = require('js-yaml');
const { parseArgs, modulesAt, Runner, core, permissionTests } = require('./run-tests');

const root = path.resolve(__dirname, '..');
let log, error, table;
beforeEach(() => {
    log = jest.spyOn(console, 'log').mockImplementation(() => {});
    error = jest.spyOn(console, 'error').mockImplementation(() => {});
    table = jest.spyOn(console, 'table').mockImplementation(() => {});
});
afterEach(() => { log.mockRestore(); error.mockRestore(); table.mockRestore(); });

test('default and opt-in selections are explicit and independently skippable', () => {
    expect(parseArgs([]).suites).toEqual(core);
    expect(parseArgs(['--skip-playwright', '--skip-lint']).suites).toEqual(['go', 'js']);
    expect(parseArgs(['--all', '--skip-docker', '--skip-docs']).suites)
        .toEqual([...core, 'native']);
    expect(parseArgs(['--with-docker', '--with-docs', '--privileged']).suites)
        .toEqual([...core, 'integration', 'docker', 'docs', 'privileged']);
    expect(parseArgs(['--all']).suites).not.toContain('privileged');
});

test.each([
    ['--typo'], ['--suite'], ['--workers=0'], ['--workers=1x'],
    ['--suite=unknown'], ['--module=missing'], ['--all', '--suite=go'],
    ['--suite=privileged'], ['--coverage=true'], ['--suite=go', '--skip-go'],
    ['--package=../agent'], ['--package=./../agent']
])('rejects invalid input without running anything: %j', (...args) => {
    expect(() => parseArgs(args)).toThrow();
});

test('module discovery picks up additions and removals without editing either shell or CI', () => {
    const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'pm-module-discovery-'));
    try {
        fs.mkdirSync(path.join(directory, 'new-module'));
        fs.writeFileSync(path.join(directory, 'new-module', 'go.mod'), 'module example.test/new');
        fs.mkdirSync(path.join(directory, 'not-a-module'));
        expect(modulesAt(directory)).toEqual(['new-module']);
        fs.unlinkSync(path.join(directory, 'new-module', 'go.mod'));
        expect(modulesAt(directory)).toEqual([]);
    } finally { fs.rmSync(directory, { recursive: true }); }
});

test('Go is uncached with an explicit timeout, module selection and separate coverage', () => {
    const execute = jest.fn(() => ({ status: 0 }));
    const runner = new Runner(parseArgs(['--suite=go', '--module=agent', '--package=./storage', '--coverage']), execute);
    expect(runner.run()).toBe(0);
    expect(execute).toHaveBeenCalledTimes(1);
    expect(execute.mock.calls[0][0]).toBe('go');
    expect(execute.mock.calls[0][1]).toEqual(['test', '-count=1', '-timeout=10m', '-v', '-coverprofile=coverage.out', './storage']);
    expect(execute.mock.calls[0][2].cwd).toBe(path.join(root, 'agent'));
    execute.mockClear();
    const integration = new Runner(parseArgs(['--suite=integration', '--coverage']), execute);
    integration.dockerReady = jest.fn();
    expect(integration.run()).toBe(0);
    expect(execute.mock.calls[0][1]).toEqual([
        'test', '-count=1', '-timeout=10m', '-v', '-coverprofile=coverage-integration.out', '-tags=integration', './storage/...'
    ]);
});

test('Playwright always builds both binaries and runs the full matrix without retries', () => {
    const execute = jest.fn(() => ({ status: 0 }));
    expect(new Runner(parseArgs(['--suite=playwright', '--workers=1']), execute).run()).toBe(0);
    expect(execute.mock.calls.slice(0, 2).map(call => call[1][0])).toEqual(['build', 'build']);
    const browser = execute.mock.calls[2];
    expect(browser[1]).toContain('--retries=0');
    expect(browser[2].env).toMatchObject({ PW_ALL_BROWSERS: '1', PW_WORKERS: '1' });
});

test('Windows PostgreSQL endpoint discovery never changes subsequent Docker CLI context', () => {
    const platform = Object.getOwnPropertyDescriptor(process, 'platform');
    Object.defineProperty(process, 'platform', { value: 'win32' });
    const endpoint = 'npipe:////./pipe/dockerDesktopLinuxEngine';
    const execute = jest.fn((command, args) => ({
        status: 0, stdout: command === 'docker' && args[0] === 'context' ? endpoint : ''
    }));
    try {
        const runner = new Runner(parseArgs(['--suite=integration']), execute);
        delete runner.env.DOCKER_HOST;
        expect(runner.run()).toBe(0);
        const postgres = execute.mock.calls.find(([command]) => command === 'go');
        expect(postgres[2].env.DOCKER_HOST).toBe(endpoint);
        expect(runner.env.DOCKER_HOST).toBeUndefined();
        runner.dockerReady();
        expect(execute.mock.calls.at(-1)[2].env.DOCKER_HOST).toBeUndefined();
    } finally { Object.defineProperty(process, 'platform', platform); }
});

test('command failures and missing prerequisites fail visibly while later suites still run', () => {
    const execute = jest.fn()
        .mockReturnValueOnce({ error: new Error('missing go') })
        .mockReturnValueOnce({ status: 0 });
    const runner = new Runner(parseArgs(['--suite=go,js', '--module=agent']), execute);
    expect(runner.run()).toBe(1);
    expect(runner.results).toEqual([{ suite: 'go', status: 'FAIL' }, { suite: 'js', status: 'PASS' }]);
    expect(error).toHaveBeenCalledWith(expect.stringContaining('missing go'));
    expect(new Runner(parseArgs(['--suite=go']), () => ({ status: null, signal: 'SIGTERM' })).run()).toBe(1);
});

test('dry run never executes commands and inherited live-installer opt-ins are removed', () => {
    const previous = process.env.PRINTMASTER_LIVE_INSTALLER_PERMISSIONS;
    process.env.PRINTMASTER_LIVE_INSTALLER_PERMISSIONS = 'qualify-test-agent-msi';
    try {
        const execute = jest.fn(() => ({ status: 0 }));
        const runner = new Runner(parseArgs(['--all', '--dry-run']), execute);
        expect(runner.env.PRINTMASTER_LIVE_INSTALLER_PERMISSIONS).toBeUndefined();
        expect(runner.run()).toBe(0);
        expect(execute).not.toHaveBeenCalled();
    } finally {
        if (previous === undefined) delete process.env.PRINTMASTER_LIVE_INSTALLER_PERMISSIONS;
        else process.env.PRINTMASTER_LIVE_INSTALLER_PERMISSIONS = previous;
    }
});

test('native selection retains descriptor, identity and recovery checks', () => {
    for (const name of ['TestInstallationProtectionSeparateUnixIdentities',
        'TestInstallationDescriptorRejectsSubstitutionBeforeApply',
        'TestFailedInstallRecoveryOnlyRestoresRecordedPreviousArtifact']) {
        expect(name).toMatch(new RegExp(permissionTests));
    }
});

test('Docker fixture seeds only after stopping and always cleans its uniquely scoped project after failure', () => {
    const runner = new Runner(parseArgs(['--suite=docker']));
    runner.dockerReady = jest.fn();
    let temp;
    const calls = [];
    runner.command = jest.fn((command, args, options = {}) => {
        calls.push([command, args, options]);
        if (options.env?.PM_E2E_DATA_ROOT) {
            temp = options.env.PM_E2E_DATA_ROOT;
            for (const component of ['agent', 'server']) {
                fs.writeFileSync(path.join(temp, component, `${component}.db`), '');
            }
        }
        if (command === 'docker' && args.includes('port')) return '127.0.0.1:12345';
        if (command === 'go') throw new Error('intentional E2E failure');
        return '';
    });
    try {
        expect(() => runner.dockerE2E()).toThrow('intentional E2E failure');
        expect(fs.existsSync(temp)).toBe(false);
        const stop = calls.findIndex(([, args]) => args.includes('stop'));
        const seed = calls.findIndex(([command, args]) => command === 'sqlite3' && args[0] !== '--version');
        expect(stop).toBeGreaterThan(-1);
        expect(seed).toBeGreaterThan(stop);
        const cleanup = calls.find(([, args]) => args.includes('down'));
        expect(cleanup[1]).toEqual(expect.arrayContaining(['--rmi', 'local', '--volumes']));
        expect(cleanup[1][2]).toMatch(/^pm-tests-/);
        expect(calls.find(([, args]) => args.includes('up'))[2].env).toMatchObject({
            PM_E2E_AGENT_PORT: '0', PM_E2E_SERVER_PORT: '0'
        });
    } finally {
        if (temp && fs.existsSync(temp)) fs.rmSync(temp, { recursive: true });
        const cleanup = calls.find(([, args]) => args.includes('down'));
        if (cleanup) fs.rmSync(path.join(root, 'test-results', cleanup[1][2]), { recursive: true, force: true });
    }
});

test('CI uses the same runner for every test suite and preserves the complete gate', () => {
    const ci = yaml.load(fs.readFileSync(path.join(root, '.github', 'workflows', 'ci.yml'), 'utf8'));
    const native = yaml.load(fs.readFileSync(path.join(root, '.github', 'workflows', 'installer-permissions.yml'), 'utf8'));
    const commands = Object.values(ci.jobs).flatMap(job => (job.steps || []).map(step => step.run || ''));
    expect(commands.join('\n')).not.toMatch(/\bgo test\b|npm run test:|go build\b/);
    for (const selection of ['lint,go', 'js,playwright', 'integration', 'docker']) {
        expect(commands.some(command => command.includes(`bash tests.sh --suite ${selection}`))).toBe(true);
    }
    expect(ci.jobs['ci-passed'].needs).toEqual(['go', 'installer-permissions', 'javascript', 'integration', 'e2e']);
    expect(native.jobs.native.steps.map(step => step.run || '').join('\n')).toContain('--suite privileged --privileged');
});

test('shell wrappers work outside the repository and propagate invalid-argument failure', () => {
    const shells = process.platform === 'win32'
        ? [['pwsh', ['-NoProfile', '-File', path.join(root, 'tests.ps1')]],
            [path.join(process.env.ProgramFiles, 'Git', 'bin', 'bash.exe'), [path.join(root, 'tests.sh')]]]
        : [['bash', [path.join(root, 'tests.sh')]]];
    for (const [command, args] of shells) {
        const good = spawnSync(command, [...args, '--suite', 'go', '--dry-run'], { cwd: os.tmpdir(), encoding: 'utf8', timeout: 30000 });
        expect(good.error).toBeUndefined();
        expect(good.status).toBe(0);
        expect(good.stdout).toContain('Suites: go');
        const bad = spawnSync(command, [...args, '--invalid-option'], { cwd: os.tmpdir(), encoding: 'utf8', timeout: 30000 });
        expect(bad.status).toBe(1);
        expect(bad.stderr).toContain('Unknown option');
    }
});
