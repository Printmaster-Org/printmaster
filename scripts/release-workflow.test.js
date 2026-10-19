const fs = require('fs');
const path = require('path');
const os = require('os');
const { spawnSync } = require('child_process');
const yaml = require('js-yaml');
const picomatch = require('picomatch');

const root = path.resolve(__dirname, '..');
const workflow = fs.readFileSync(path.join(root, '.github/workflows/cd-agent.yml'), 'utf8');

test('B2 preflight is manual, opt-in, environment-scoped and cannot publish', () => {
    const preflight = yaml.load(fs.readFileSync(path.join(root, '.github', 'workflows', 'update-storage-preflight.yml'), 'utf8'));
    expect(preflight.on).toEqual({ workflow_dispatch: {} });
    expect(preflight.permissions).toEqual({ contents: 'read' });
    expect(preflight.concurrency['cancel-in-progress']).toBe(false);
    const inspect = preflight.jobs.inspect;
    expect(inspect.if).toBe("github.ref == 'refs/heads/main' && vars.ENABLE_UPDATE_STORAGE_PREFLIGHT == 'true'");
    expect(inspect.environment).toBe('update-storage-preflight');
    expect(inspect['timeout-minutes']).toBe(5);
    expect(inspect.steps[0].with['persist-credentials']).toBe(false);
    for (const step of inspect.steps.filter(step => step.uses)) {
        expect(step.uses).toMatch(/@[0-9a-f]{40}$/);
    }
    const build = inspect.steps.find(step => step.name === 'Build checker without credentials');
    expect(build.env).toBeUndefined();
    expect(build.run).toContain('go build');
    const check = inspect.steps.find(step => step.name === 'Inspect bucket configuration without mutation');
    expect(check['working-directory']).toBe('common');
    expect(check.run).toContain('"$RUNNER_TEMP/update-storage-check"');
    expect(inspect.steps.indexOf(build)).toBeLessThan(inspect.steps.indexOf(check));
    expect(check.env.B2_RECOVERY_CHECK_APPLICATION_KEY).toBe('${{ secrets.B2_RECOVERY_CHECK_APPLICATION_KEY }}');
    expect(check.env.B2_METADATA_BUCKET).toBe('${{ vars.B2_METADATA_BUCKET }}');
    expect(inspect.steps.filter(step => step.env)).toHaveLength(1);
    expect(JSON.stringify(preflight)).not.toMatch(/root\.pem|targets\.pem|update-metadata|upload-artifact|continue-on-error/);
});

function job(name) {
    const match = workflow.match(new RegExp(`^  ${name}:\\n([\\s\\S]*?)(?=^  [a-z][a-z-]*:|$(?![\\s\\S]))`, 'm'));
    if (!match) throw new Error(`Missing job ${name}`);
    return match[1];
}
function needs(name) {
    return job(name).match(/^    needs: \[([^\]]+)\]/m)[1].split(',').map(value => value.trim());
}

test('main dev publication has no tag-only RPM dependency; stable packaging stays required', () => {
    expect(needs('release-agent-dev')).toEqual(['build-agent', 'merge-docker']);
    expect(job('release-agent-dev')).toContain("if: github.ref == 'refs/heads/main'");
    expect(needs('release-agent')).toContain('build-rpm');
    expect(job('build-rpm')).toContain("if: startsWith(github.ref, 'refs/tags/agent-v')");
    // Actions implicitly requires successful dependencies. This catches the
    // observed regression: main builds succeed, RPM legitimately skips.
    const results = { 'build-agent': 'success', 'merge-docker': 'success', 'build-rpm': 'skipped' };
    expect(needs('release-agent-dev').every(name => results[name] === 'success')).toBe(true);
    for (const failed of ['build-agent', 'merge-docker']) {
        expect(needs('release-agent-dev').every(name => ({ ...results, [failed]: 'failure' })[name] === 'success')).toBe(false);
    }
    expect(job('release-agent-dev')).not.toMatch(/if:.*always\(/);
});

test('dev publication pins built SHA and rejects missing uploads', () => {
    const dev = job('release-agent-dev');
    expect(dev).toContain('target_commitish: ${{ github.sha }}');
    expect(dev).toContain('fail_on_unmatched_files: true');
    expect(dev).toContain('prerelease: true');
    expect(dev).toContain('bash scripts/prepare-agent-dev-release.sh artifacts "$DEV_VERSION"');
});

test('tagged Agent builds require a valid provisioned public root before packaging', () => {
    const steps = yaml.load(workflow).jobs['build-agent'].steps;
    const guard = steps.findIndex(step => step.name === 'Require provisioned publisher root for tagged releases');
    expect(guard).toBeGreaterThan(-1);
    expect(steps[guard].if).toBe("startsWith(github.ref, 'refs/tags/agent-v')");
    expect(steps[guard]['working-directory']).toBe('./common');
    expect(steps[guard].run).toBe('go run ./cmd/update-keys check-root -root ../agent/update_publisher_root.json');
    expect(steps[guard]['continue-on-error']).toBeUndefined();
    expect(guard).toBeLessThan(steps.findIndex(step => step.name === 'Build Agent'));
});

test('local release and nondev container builds enforce the same public-root guard', () => {
    const powershell = fs.readFileSync(path.join(root, 'build.ps1'), 'utf8');
    const bash = fs.readFileSync(path.join(root, 'build.sh'), 'utf8');
    const docker = fs.readFileSync(path.join(root, 'agent', 'Dockerfile'), 'utf8');
    expect(powershell).toContain("if ($Component -eq 'agent' -and $IsRelease)");
    expect(powershell).toContain('go run .\\cmd\\update-keys check-root -root ..\\agent\\update_publisher_root.json');
    expect(powershell).toContain("throw 'Agent release requires an owner-provisioned publisher root'");
    expect(powershell.indexOf('go run .\\cmd\\update-keys')).toBeLessThan(powershell.indexOf('if ($IsRelease -and $IncrementVersion)'));
    expect(bash).toContain('if [[ "$component" == "agent" && "$RELEASE" == "1" ]]');
    expect(bash).toContain('go run ./cmd/update-keys check-root -root ../agent/update_publisher_root.json');
    expect(bash.indexOf('go run ./cmd/update-keys')).toBeLessThan(bash.indexOf('if [[ "$RELEASE" == "1" && "$INCREMENT_VERSION" == "1" ]]'));
    expect(docker).toContain('set -eu;');
    expect(docker).toContain('if [ "$BUILD_TYPE" != "dev" ]; then');
    expect(docker).toContain('go run ./cmd/update-keys check-root -root ../agent/update_publisher_root.json');
    expect(docker.indexOf('go run ./cmd/update-keys')).toBeLessThan(docker.indexOf('GOOS=linux'));
});

test('Fedora RPM job trusts only the checkout before reading Git version metadata', () => {
    const rpm = job('build-rpm');
    expect(rpm).toContain('container:\n      image: fedora:latest');
    const trust = rpm.indexOf('git config --global --add safe.directory "$GITHUB_WORKSPACE"');
    const version = rpm.indexOf('bash scripts/release-version.sh');
    expect(trust).toBeGreaterThan(-1);
    expect(version).toBeGreaterThan(trust);
    expect(rpm).not.toMatch(/safe\.directory\s+['"]?\*/);
});

test('CI tests main and pull requests without rerunning for release tags', () => {
    const ci = fs.readFileSync(path.join(root, '.github/workflows/ci.yml'), 'utf8');
    const triggers = ci.slice(ci.indexOf('\non:'), ci.indexOf('\nconcurrency:'));
    expect(triggers).toMatch(/push:\n\s+branches: \[main\]/);
    expect(triggers).toMatch(/pull_request:\n\s+branches: \[main\]/);
    expect(triggers).toContain('workflow_dispatch:');
    expect(triggers).not.toMatch(/^\s+tags:/m);
    expect(ci).toContain('name: CI Passed');
});

describe('documentation-only workflow filtering', () => {
    const workflows = Object.fromEntries(['ci', 'cd-agent', 'cd-server', 'codeql'].map(name => [
        name, yaml.load(fs.readFileSync(path.join(root, '.github', 'workflows', `${name}.yml`), 'utf8')),
    ]));
    const events = [
        ['ci', 'push'], ['ci', 'pull_request'],
        ['cd-agent', 'push'], ['cd-server', 'push'], ['codeql', 'push'],
    ];

    test.each(events)('%s %s ignores only documentation paths', (name, event) => {
        const trigger = workflows[name].on[event];
        expect(trigger.branches).toEqual(['main']);
        expect(trigger['paths-ignore']).toEqual(['**/*.md', 'docs/**']);
        expect(trigger.paths).toBeUndefined();
        const ignored = picomatch(trigger['paths-ignore'], { dot: true });
        const runs = files => files.some(file => !ignored(file));

        expect(runs(['README.md'])).toBe(false);
        expect(runs(['AGENTS.md', '.github/copilot-instructions.md', 'agent/README.md', 'CONTRIBUTING.md'])).toBe(false);
        expect(runs(['docs/guide.md', 'docs/images/example.png'])).toBe(false);
        for (const file of [
            'agent/main.go', 'server/main.go', 'common/web/shared.js',
            'agent/go.mod', 'server/go.sum', 'go.work.sum', 'package-lock.json',
            'agent/web/index.html', 'common/web/style.css', 'static/logo.png',
            'agent/config.example.toml', 'agent/Dockerfile', 'agent/VERSION',
            'tests/example_test.go', 'scripts/release-workflow.test.js',
            'build.ps1', 'build.sh', 'docker-compose.yml', 'packaging/site/index.html',
            '.github/workflows/ci.yml', '.github/dependabot.yml',
        ]) {
            expect(runs([file])).toBe(true);
            expect(runs(['README.md', file])).toBe(true);
        }
    });

    test('manual runs, release tag publication, and scheduled security scans remain enabled', () => {
        for (const workflow of Object.values(workflows)) {
            expect(workflow.on.workflow_dispatch).toEqual({});
        }
        for (const component of ['agent', 'server']) {
            expect(workflows[`cd-${component}`].on.push.tags).toEqual([
                `${component}-v[0-9]+.[0-9]+.[0-9]+`,
                `${component}-v[0-9]+.[0-9]+.[0-9]+-beta.[0-9]+`,
            ]);
        }
        expect(workflows.codeql.on.schedule).toEqual([{ cron: '0 0 * * 0' }]);
    });
});

test('Playwright caches its default browser directory and installs system dependencies on every runner', () => {
    const ci = fs.readFileSync(path.join(root, '.github/workflows/ci.yml'), 'utf8');
    const javascript = ci.slice(ci.indexOf('\n  javascript:'), ci.indexOf('\n  e2e:'));
    expect(javascript).not.toContain('PLAYWRIGHT_BROWSERS_PATH:');
    expect(javascript).toContain('path: ~/.cache/ms-playwright');
    expect(javascript).toContain("key: ${{ runner.os }}-playwright-${{ hashFiles('package-lock.json') }}");
    expect(javascript).toMatch(/- name: Ensure Playwright browsers and system dependencies\n\s+run: npx playwright install --with-deps chromium firefox webkit/);
});

test.each(['agent', 'server'])('%s CD reuses successful CI for the exact SHA and rejects untested commits', component => {
    const cd = fs.readFileSync(path.join(root, `.github/workflows/cd-${component}.yml`), 'utf8');
    const gate = cd.slice(cd.indexOf('\n  wait-for-ci:'), cd.indexOf(`\n  build-${component}:`));
    expect(gate).toContain('check-name: CI Passed');
    expect(gate).toContain('ref: ${{ github.sha }}');
    expect(gate).toContain('allowed-conclusions: success');
    expect(gate).toContain("fail-on-no-checks: 'true'");
    expect(gate).toContain('timeout-minutes: 15');
    expect(gate).toContain('checks-discovery-timeout: 900');
    expect(cd).toMatch(/tags:\n\s+- '.*-v/);
    for (const build of [`build-${component}`, 'build-docker']) {
        const match = cd.match(new RegExp(`^  ${build}:\\n([\\s\\S]*?)(?=^  [a-z][a-z-]*:|$(?![\\s\\S]))`, 'm'));
        expect(match).not.toBeNull();
        expect(match[1]).toContain('needs: [wait-for-ci]');
    }
});

describe('Agent dev asset staging', () => {
    const version = '0.31.1-dev.test123';
    let directory;
    beforeEach(() => { directory = fs.mkdtempSync(path.join(os.tmpdir(), 'pm-dev-release-')); });
    afterEach(() => { fs.rmSync(directory, { recursive: true, force: true }); });
    function seed(omit) {
        for (const platform of ['linux-amd64', 'linux-arm64', 'windows-amd64', 'darwin-amd64', 'darwin-arm64']) {
            if (platform === omit) continue;
            const folder = path.join(directory, `agent-binary-${platform}`);
            fs.mkdirSync(folder);
            fs.writeFileSync(path.join(folder, `printmaster-agent-v${version}-${platform}${platform.startsWith('windows') ? '.exe' : ''}`), 'binary');
        }
    }
    // On Windows, `bash` on PATH is often the WSL launcher stub (no distro),
    // so prefer the bash that ships with Git for Windows.
    function findBash() {
        if (process.platform !== 'win32') return { command: 'bash', posix: value => value };
        const candidates = [];
        for (const base of [process.env.ProgramFiles, process.env['ProgramFiles(x86)'], process.env.LOCALAPPDATA && path.join(process.env.LOCALAPPDATA, 'Programs')]) {
            if (base) candidates.push(path.join(base, 'Git', 'bin', 'bash.exe'));
        }
        const git = spawnSync('where', ['git'], { encoding: 'utf8' });
        for (const line of (git.stdout || '').split(/\r?\n/).filter(Boolean)) {
            candidates.push(path.join(path.dirname(line), '..', 'bin', 'bash.exe'));
        }
        const command = candidates.find(candidate => fs.existsSync(candidate)) || 'bash';
        return { command, posix: value => value.replace(/\\/g, '/') };
    }
    const bash = findBash();
    function prepare() {
        const args = [path.join(root, 'scripts/prepare-agent-dev-release.sh'), directory, version].map(bash.posix);
        return spawnSync(bash.command, args, { encoding: 'utf8' });
    }
    test('stages all binaries and multiple package assets, ignoring digests', () => {
        seed();
        const folder = path.join(directory, 'agent-deb-amd64');
        fs.mkdirSync(folder);
        for (const file of ['printmaster-agent_amd64.deb', 'printmaster-agent_extra.deb']) fs.writeFileSync(path.join(folder, file), 'package');
        fs.mkdirSync(path.join(directory, 'agent-digests-amd64'));
        fs.writeFileSync(path.join(directory, 'agent-digests-amd64', 'deadbeef'), 'digest');
        expect(prepare().status).toBe(0);
        expect(fs.readdirSync(directory).filter(name => name.startsWith('printmaster-'))).toHaveLength(7);
        expect(fs.existsSync(path.join(directory, 'deadbeef'))).toBe(false);
    });
    test('missing matrix binary blocks publication', () => {
        seed('windows-amd64');
        const result = prepare();
        expect(result.status).not.toBe(0);
        expect(result.stderr).toContain('Missing or empty Agent dev binary');
    });
    test('empty matrix binary blocks publication', () => {
        seed();
        fs.writeFileSync(path.join(directory, 'agent-binary-linux-amd64', `printmaster-agent-v${version}-linux-amd64`), '');
        const result = prepare();
        expect(result.status).not.toBe(0);
        expect(result.stderr).toContain('Missing or empty Agent dev binary');
    });
});