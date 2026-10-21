const fs = require('fs');
const os = require('os');
const path = require('path');
const { spawnSync } = require('child_process');

const root = path.resolve(__dirname, '..');
const bash = process.platform === 'win32' ? 'C:\\Program Files\\Git\\bin\\bash.exe' : 'bash';
const hasPowerShell = spawnSync('pwsh', ['-NoProfile', '-Command', 'exit 0'], { timeout: 10000 }).status === 0;
const shells = hasPowerShell ? ['bash', 'powershell'] : ['bash'];

describe.each(shells)('%s build and release gates', shell => {
    let directory;
    function run(script, args = [], env = {}) {
        return spawnSync(shell === 'bash' ? bash : 'pwsh',
            shell === 'bash' ? [path.join(directory, `${script}.sh`), ...args]
                : ['-NoProfile', '-File', path.join(directory, `${script}.ps1`), ...args],
            { cwd: os.tmpdir(), env: { ...process.env, ...env }, encoding: 'utf8', timeout: 30000 });
    }
    function git(...args) {
        const result = spawnSync('git', args, { cwd: directory, encoding: 'utf8' });
        if (result.status !== 0) throw new Error(result.stderr);
        return result.stdout.trim();
    }
    beforeEach(() => {
        directory = fs.mkdtempSync(path.join(os.tmpdir(), 'pm-build-gate-'));
        for (const script of ['build.ps1', 'build.sh', 'release.ps1', 'release.sh', 'tests.ps1', 'tests.sh']) {
            fs.copyFileSync(path.join(root, script), path.join(directory, script));
        }
        fs.mkdirSync(path.join(directory, 'scripts'));
        fs.writeFileSync(path.join(directory, 'scripts', 'run-tests.js'),
            'console.log("GATE_ARGS="+JSON.stringify(process.argv.slice(2))); process.exit(Number(process.env.PM_FIXTURE_EXIT || 0));\n');
        for (const component of ['agent', 'server']) {
            fs.mkdirSync(path.join(directory, component));
            fs.writeFileSync(path.join(directory, component, 'VERSION'), '0.31.1');
        }
    });
    afterEach(() => fs.rmSync(directory, { recursive: true, force: true }));

    test('basic build validates before compiling or mutating build/version counters and propagates failure', () => {
        const result = run('build', ['agent'], { PM_FIXTURE_EXIT: '42' });
        expect(result.stdout).toContain('GATE_ARGS=["--skip-playwright"]');
        expect(result.status).toBe(42);
        expect(fs.existsSync(path.join(directory, 'agent', '.buildnumber'))).toBe(false);
        expect(fs.readFileSync(path.join(directory, 'agent', 'VERSION'), 'utf8')).toBe('0.31.1');
    });

    test('validation-only supports full, custom and explicit skipped gates', () => {
        const full = run('build', shell === 'bash' ? ['both', '--test-only', '--all-tests'] : ['both', '-TestOnly', '-AllTests']);
        expect(full.status).toBe(0);
        expect(full.stdout).toContain('GATE_ARGS=["--all"]');
        const custom = run('build', shell === 'bash'
            ? ['agent', '--test-only', '--test-arg', '--suite=go', '--test-arg', '--module=agent']
            : ['agent', '-TestOnly', '-TestArgs', '--suite=go']);
        expect(custom.status).toBe(0);
        expect(custom.stdout).toContain('GATE_ARGS=["--suite=go"');
        const skipped = run('build', shell === 'bash'
            ? ['both', '--test-only', '--skip-tests'] : ['both', '-TestOnly', '-SkipTests']);
        expect(skipped.status).toBe(0);
        expect(skipped.stdout).not.toContain('GATE_ARGS=');
        expect(skipped.stdout).toMatch(/Skipping shared test gate/);
    });

    test('release full gate chains through actual build and tests before any version, commit or tag mutation', () => {
        git('init', '-b', 'main');
        git('config', 'user.email', 'test@example.invalid');
        git('config', 'user.name', 'Gate Test');
        git('add', '.');
        git('commit', '-m', 'test: release gate fixture');
        const head = git('rev-parse', 'HEAD');
        const result = run('release', shell === 'bash'
            ? ['both', 'minor', '--skip-push'] : ['both', 'minor', '-SkipPush'], { PM_FIXTURE_EXIT: '42' });
        expect(result.status).not.toBe(0);
        expect(result.stdout + result.stderr).toContain('GATE_ARGS=["--all"]');
        expect(fs.readFileSync(path.join(directory, 'agent', 'VERSION'), 'utf8')).toBe('0.31.1');
        expect(fs.readFileSync(path.join(directory, 'server', 'VERSION'), 'utf8')).toBe('0.31.1');
        expect(git('rev-parse', 'HEAD')).toBe(head);
        expect(git('tag')).toBe('');
        expect(fs.existsSync(path.join(directory, 'agent', '.buildnumber'))).toBe(false);
    });

    test('successful release validates once before versions change and skips duplicate per-component gates', () => {
        fs.writeFileSync(path.join(directory, 'build.sh'), `#!/usr/bin/env bash
cd "$(dirname "$0")"
printf '%s|%s|%s\\n' "$(cat agent/VERSION)" "$(cat server/VERSION)" "$*" >> calls.log
exit 0
`);
        fs.writeFileSync(path.join(directory, 'build.ps1'), `param($Component, [switch]$Release, [switch]$VerboseBuild, [switch]$SkipTests, [switch]$TestOnly, [switch]$AllTests, [string[]]$TestArgs)
Add-Content calls.log "$(Get-Content agent/VERSION)|$(Get-Content server/VERSION)|$Component|TestOnly=$TestOnly|AllTests=$AllTests|SkipTests=$SkipTests"
$global:LASTEXITCODE = 0
`);
        fs.writeFileSync(path.join(directory, '.gitignore'), 'logs/\ncalls.log\n');
        git('init', '-b', 'main');
        git('config', 'user.email', 'test@example.invalid');
        git('config', 'user.name', 'Gate Test');
        git('add', '.');
        git('commit', '-m', 'feat: release fixture');
        const result = run('release', shell === 'bash'
            ? ['both', 'minor', '--skip-push'] : ['both', 'minor', '-SkipPush']);
        expect(result.status).toBe(0);
        const calls = fs.readFileSync(path.join(directory, 'calls.log'), 'utf8').trim().split(/\r?\n/);
        expect(calls).toHaveLength(3);
        expect(calls[0]).toMatch(/^0\.31\.1\|0\.31\.1\|/);
        expect(calls[0]).toMatch(shell === 'bash' ? /--test-only --all-tests/ : /TestOnly=True\|AllTests=True/);
        for (const call of calls.slice(1)) {
            expect(call).toMatch(/^0\.32\.0\|0\.32\.0\|/);
            expect(call).toMatch(shell === 'bash' ? /--release --skip-tests/ : /SkipTests=True/);
        }
    });
});
