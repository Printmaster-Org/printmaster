const fs = require('fs');
const os = require('os');
const path = require('path');
const { spawnSync } = require('child_process');

const root = path.resolve(__dirname, '..');
const core = ['lint', 'go', 'js', 'playwright'];
const optional = ['integration', 'docker', 'native', 'docs'];
const suites = [...core, ...optional, 'privileged'];
const permissionTests = '^Test(InstallationProtection|SecureUpdaterDirectories|ValidateInstallPath|WindowsUpdater|WindowsService|InstallationDescriptor|FailedInstallRecovery|PrepareWindowsServiceExecutable|ProtectWindowsServiceFile)';

function modulesAt(directory) {
    return fs.readdirSync(directory, { withFileTypes: true })
        .filter(entry => entry.isDirectory() && fs.existsSync(path.join(directory, entry.name, 'go.mod')))
        .map(entry => entry.name).sort();
}

function parseArgs(args, availableModules = modulesAt(root)) {
    const options = {
        suites: [...core], modules: availableModules, coverage: false, dryRun: false,
        workers: '2', staticcheck: 'staticcheck', hugo: 'hugo',
        docsDir: path.resolve(root, '..', 'docs.printmaster.work')
    };
    const selected = [], skipped = [];
    const values = new Set(['suite', 'module', 'package', 'workers', 'staticcheck', 'hugo', 'docs-dir']);
    let all = false, docker = false, docs = false, privileged = false;
    for (let i = 0; i < args.length; i++) {
        const match = /^--([a-z-]+)(?:=(.*))?$/.exec(args[i]);
        if (!match) throw new Error(`Unknown argument: ${args[i]}; use --help`);
        const [, key, inline] = match;
        if (values.has(key)) {
            const value = inline ?? args[++i];
            if (!value || value.startsWith('--')) throw new Error(`--${key} requires a value`);
            if (key === 'suite') selected.push(...value.split(','));
            else if (key === 'module') (options.requestedModules ??= []).push(...value.split(','));
            else if (key === 'package') (options.packages ??= []).push(value);
            else options[({ 'docs-dir': 'docsDir' })[key] || key] = value;
        } else {
            if (inline !== undefined) throw new Error(`--${key} does not take a value`);
            if (key === 'all') all = true;
            else if (key === 'with-docker') docker = true;
            else if (key === 'with-docs') docs = true;
            else if (key === 'privileged') privileged = true;
            else if (key === 'coverage') options.coverage = true;
            else if (key === 'dry-run') options.dryRun = true;
            else if (key === 'help') options.help = true;
            else if (key.startsWith('skip-') && suites.includes(key.slice(5))) skipped.push(key.slice(5));
            else throw new Error(`Unknown option: --${key}; use --help`);
        }
    }
    if (all && selected.length) throw new Error('--all and --suite cannot be combined');
    options.suites = selected.length ? [...new Set(selected)] : all ? [...core, ...optional] : [...core];
    if (docker) options.suites.push('integration', 'docker');
    if (docs) options.suites.push('docs');
    if (privileged) options.suites.push('privileged');
    if (options.suites.some(suite => !suites.includes(suite))) throw new Error(`Suites must be: ${suites.join(', ')}`);
    if (selected.includes('privileged') && !privileged) throw new Error('Root-only fixtures require --privileged');
    if (skipped.includes('docker')) skipped.push('integration');
    options.suites = [...new Set(options.suites)].filter(suite => !skipped.includes(suite));
    if (!options.suites.length) throw new Error('No suites selected');
    if (!/^[1-9]\d*$/.test(options.workers)) throw new Error('--workers must be a positive integer');
    if (options.packages?.some(value => !value.startsWith('./') || value.includes('\\') || value.split('/').includes('..'))) {
        throw new Error('--package must be a relative Go package pattern such as ./storage or ./...');
    }
    if (options.requestedModules) {
        options.modules = [...new Set(options.requestedModules)];
        if (options.modules.some(module => !availableModules.includes(module))) {
            throw new Error(`Modules must be: ${availableModules.join(', ')}`);
        }
    }
    return options;
}

function help() {
    return `PrintMaster tests: ./tests.sh [options] or .\\tests.ps1 [options]
Default: ${core.join(', ')}. Go modules are discovered from top-level go.mod files.
  --suite NAME[,NAME]  Select only suites: ${suites.join(', ')}
  --module NAME       Limit Go/lint modules (repeatable or comma-separated)
  --package ./PATH    Limit Go tests to relative packages (repeatable)
  --skip-NAME         Omit any suite, e.g. --skip-playwright or --skip-lint
  --with-docker       Add PostgreSQL integration and isolated Docker E2E
  --with-docs         Add sibling documentation tests, Hugo build and link checks
  --all               All nonprivileged suites, including Docker/docs/native checks
  --privileged        Add root-only Unix permission/package fixtures (sudo -n)
  --coverage          Write CI-compatible Go coverage files
  --workers N         Playwright workers (default 2); all six projects, no retries
  --staticcheck PATH  Existing compatible staticcheck executable
  --hugo PATH         Existing Hugo executable for docs
  --docs-dir PATH     Documentation checkout
  --dry-run           Print selection without executing commands
  --help              Show this help
Prerequisites are never silently installed or missing suites treated as passes.
Live installer/service replacement tests are NEVER enabled by this runner.`;
}

class Runner {
    constructor(options, execute = spawnSync) {
        this.options = options;
        this.execute = execute;
        this.results = [];
        this.env = { ...process.env };
        // An inherited rehearsal opt-in must not turn a baseline into a live install.
        for (const key of Object.keys(this.env)) {
            if (key.startsWith('PRINTMASTER_LIVE_')) delete this.env[key];
        }
    }

    command(command, args, { cwd = root, env = {}, capture = false, input, timeout = 20 * 60 * 1000 } = {}) {
        console.log(`> ${command} ${args.join(' ')} [${cwd}]`);
        const result = this.execute(command, args, {
            cwd, env: { ...this.env, ...env }, encoding: 'utf8',
            stdio: capture || input !== undefined ? ['pipe', 'pipe', 'pipe'] : 'inherit',
            input, timeout, windowsHide: true
        });
        if (result.error) throw new Error(`${command}: ${result.error.message}`, { cause: result.error });
        if (result.status !== 0) {
            throw new Error(`${command} exited ${result.status ?? result.signal}\n${result.stdout || ''}${result.stderr || ''}`);
        }
        return result.stdout?.trim() || '';
    }

    goTest(module, extra = [], packages = ['./...'], coverageFile = 'coverage.out', env = {}) {
        const args = ['test', '-count=1', '-timeout=10m', '-v'];
        if (this.options.coverage) args.push(`-coverprofile=${coverageFile}`);
        this.command('go', [...args, ...extra, ...packages], { cwd: path.join(root, module), env });
    }

    dockerReady(testcontainers = false) {
        this.command('docker', ['info'], { capture: true, timeout: 30000 });
        if (testcontainers && process.platform === 'win32' && !this.env.DOCKER_HOST) {
            // testcontainers does not always discover Docker Desktop's active named pipe.
            return { DOCKER_HOST: this.command('docker', ['context', 'inspect', '--format', '{{.Endpoints.docker.Host}}'], { capture: true }) };
        }
        return {};
    }

    runSuite(suite) {
        const o = this.options;
        if (suite === 'go' || suite === 'lint') {
            const failures = [];
            for (const module of o.modules) {
                const checks = suite === 'go'
                    ? [() => this.goTest(module, [], o.packages || ['./...'])]
                    : [
                        () => this.command('go', ['vet', './...'], { cwd: path.join(root, module) }),
                        () => this.command(o.staticcheck, ['./...'], { cwd: path.join(root, module) })
                    ];
                for (const check of checks) {
                    try { check(); } catch (error) {
                        console.error(`${module}: ${error.message}`);
                        failures.push(error);
                    }
                }
            }
            if (failures.length) throw new AggregateError(failures, failures.map(error => error.message).join('\n'));
        } else if (suite === 'js') {
            this.command(process.execPath, [require.resolve('jest/bin/jest'), '--runInBand']);
        } else if (suite === 'playwright') {
            for (const component of ['agent', 'server']) {
                const binary = `printmaster-${component}${process.platform === 'win32' ? '.exe' : ''}`;
                this.command('go', ['build', '-o', binary, '.'], { cwd: path.join(root, component) });
            }
            this.command(process.execPath, [require.resolve('@playwright/test/cli'),
                'test', '--config=playwright.config.js', '--retries=0'], {
                env: { PW_ALL_BROWSERS: '1', PW_WORKERS: o.workers }
            });
        } else if (suite === 'integration') {
            const env = this.dockerReady(true);
            this.goTest('server', ['-tags=integration'], ['./storage/...'], 'coverage-integration.out', env);
        } else if (suite === 'docker') {
            this.dockerE2E();
        } else if (suite === 'native') {
            this.command('go', ['test', '-count=1', '-v', '-timeout=5m', '.', './autoupdate', '-run', permissionTests], {
                cwd: path.join(root, 'agent'), env: process.platform === 'darwin' ? { TMPDIR: '/private/tmp' } : {}
            });
        } else if (suite === 'privileged') {
            this.privileged();
        } else if (suite === 'docs') {
            const directory = path.resolve(o.docsDir);
            const tests = fs.readdirSync(path.join(directory, 'scripts')).filter(name => name.endsWith('.test.mjs'));
            if (!tests.length) throw new Error('No documentation tests found');
            this.command(process.execPath, ['--test', ...tests.map(name => path.join('scripts', name))], { cwd: directory });
            this.command(o.hugo, ['--gc', '--minify', '--panicOnWarning'], { cwd: directory });
            this.command(process.execPath, [path.join('scripts', 'check-site.mjs'), 'public'], { cwd: directory });
        }
    }

    privileged() {
        if (process.platform === 'win32') throw new Error('--privileged requires a native Unix host; no automatic Windows elevation or WSL substitution');
        const temp = fs.mkdtempSync(path.join(fs.realpathSync(os.tmpdir()), 'pm-root-tests-'));
        const binary = path.join(temp, 'permissions.test');
        try {
            this.command('go', ['test', '-c', '-o', binary, './autoupdate'], { cwd: path.join(root, 'agent') });
            const run = (env, pattern) => this.command('sudo', ['-n', 'env', ...env, binary,
                '-test.v', '-test.timeout=5m', `-test.run=${pattern}`]);
            const environment = process.platform === 'darwin' ? ['TMPDIR=/private/tmp'] : [];
            run(environment, '^(TestInstallationProtection|TestValidateInstallPath|TestRootPackageTree|TestPrivilegedRepositoryPackage)');
            if (process.platform === 'linux') {
                run(['PRINTMASTER_TEST_ISOLATED_DEB=1'], '^TestPrivilegedRepositoryPackageIsolatedDebian$');
            }
        } finally {
            fs.rmSync(temp, { recursive: true });
        }
    }

    dockerE2E() {
        this.dockerReady();
        this.command('sqlite3', ['--version'], { capture: true });
        const temp = fs.mkdtempSync(path.join(os.tmpdir(), 'pm-e2e-'));
        const project = `pm-tests-${process.pid}-${Date.now()}`;
        const env = {
            PM_E2E_DATA_ROOT: temp.replaceAll('\\', '/'),
            PM_E2E_SERVER_PORT: '0', PM_E2E_AGENT_PORT: '0'
        };
        for (const component of ['agent', 'server']) fs.mkdirSync(path.join(temp, component));
        const compose = (args, options) => this.command('docker', [
            'compose', '-p', project, '-f', path.join('tests', 'docker-compose.e2e.yml'), ...args
        ], { env, ...options });
        const errors = [];
        try {
            compose(['up', '--build', '-d', '--wait', '--wait-timeout', '120', 'server', 'agent']);
            compose(['stop', 'agent', 'server']);
            for (const component of ['server', 'agent']) {
                const database = path.join(temp, component, `${component}.db`);
                const sql = fs.readFileSync(path.join(root, 'tests', 'testdata', 'seed', `${component}_data.sql`), 'utf8');
                const args = [database, `PRAGMA wal_checkpoint(FULL);\n${sql}`];
                let writable = true;
                try { fs.accessSync(database, fs.constants.W_OK); } catch (error) {
                    if (error.code !== 'EACCES') throw error;
                    writable = false;
                }
                this.command(writable ? 'sqlite3' : 'sudo', writable ? args : ['-n', 'sqlite3', ...args]);
            }
            compose(['up', '-d', '--wait', '--wait-timeout', '120', 'server', 'agent']);
            const endpoint = (component, port) => {
                const mapped = compose(['port', component, String(port)], { capture: true });
                const match = /:(\d+)$/.exec(mapped);
                if (!match) throw new Error(`Cannot determine ${component} port: ${mapped}`);
                return `http://127.0.0.1:${match[1]}`;
            };
            this.command('go', ['test', '-count=1', '-timeout=10m', '-v', '-tags=e2e', './...'], {
                cwd: path.join(root, 'tests'),
                env: { E2E_SERVER_URL: endpoint('server', 9090), E2E_AGENT_URL: endpoint('agent', 8080),
                    E2E_ADMIN_PASSWORD: 'e2e-test-password' }
            });
        } catch (error) { errors.push(error); }
        try {
            const logs = compose(['logs', '--no-color', 'server', 'agent'], { capture: true });
            const output = path.join(root, 'test-results', project);
            fs.mkdirSync(output, { recursive: true });
            fs.writeFileSync(path.join(output, 'containers.log'), logs);
        } catch (error) { errors.push(error); }
        let cleaned = false;
        try {
            compose(['down', '--volumes', '--rmi', 'local', '--remove-orphans']);
            cleaned = true;
        } catch (error) { errors.push(error); }
        if (cleaned) {
            // Root-owned Linux database files can only be removed by their owner.
            try { fs.rmSync(temp, { recursive: true }); } catch (error) {
                if (error.code !== 'EACCES' && error.code !== 'EPERM') throw error;
                this.command('sudo', ['-n', 'rm', '-r', '--', temp]);
            }
        }
        if (errors.length) throw new AggregateError(errors, errors.map(error => error.message).join('\n'));
    }

    run() {
        console.log(`Suites: ${this.options.suites.join(', ')}; Go modules: ${this.options.modules.join(', ')}`);
        console.log(`Omitted: ${suites.filter(suite => !this.options.suites.includes(suite)).join(', ') || 'none'}`);
        if (this.options.dryRun) return 0;
        for (const suite of this.options.suites) {
            try {
                this.runSuite(suite);
                this.results.push({ suite, status: 'PASS' });
            } catch (error) {
                console.error(`FAIL ${suite}: ${error.message}`);
                this.results.push({ suite, status: 'FAIL' });
            }
        }
        console.table(this.results);
        console.log('Suite PASS does not convert platform/opt-in skips inside a test suite into qualification evidence.');
        return this.results.some(result => result.status === 'FAIL') ? 1 : 0;
    }
}

function main(args) {
    try {
        const options = parseArgs(args);
        if (options.help) { console.log(help()); return 0; }
        return new Runner(options).run();
    } catch (error) { console.error(error.message); return 1; }
}

if (require.main === module) process.exitCode = main(process.argv.slice(2));
module.exports = { parseArgs, modulesAt, Runner, core, suites, permissionTests };
