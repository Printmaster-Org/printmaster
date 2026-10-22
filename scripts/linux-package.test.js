const fs = require('fs');
const os = require('os');
const path = require('path');
const { spawnSync } = require('child_process');
const yaml = require('js-yaml');

const root = path.resolve(__dirname, '..');
const read = relative => fs.readFileSync(path.join(root, relative), 'utf8').replaceAll('\r\n', '\n');

test('Linux packages ship the fixed independent helper without direct installer sudo grants', () => {
    const deb = read('agent/build-deb.sh');
    const rpm = read('agent/fedora/printmaster-agent.spec');
    for (const source of [deb, rpm]) {
        expect(source).toContain('/usr/libexec/printmaster-agent-update');
        expect(source).toContain('/usr/libexec/printmaster-agent-update-recovery');
        expect(source).toContain('printmaster-agent-update.socket');
        expect(source).toContain('printmaster-agent-update@.service');
        expect(source).not.toContain('NOPASSWD:');
    }
    expect(read('agent/systemd/printmaster-agent-update.sudoers')).not.toMatch(/^[^#\s]/m);
    expect(rpm).not.toMatch(/%config[^\n]*sudoers/);
    expect(deb).not.toMatch(/systemctl (?:enable|start|restart) printmaster-agent-update/);
    expect(rpm).not.toMatch(/systemctl (?:enable|start|restart) printmaster-agent-update/);
});

test('systemd owns helper lifecycle independently of the Agent and restricts socket admission', () => {
    const socket = read('agent/systemd/printmaster-agent-update.socket');
    const service = read('agent/systemd/printmaster-agent-update@.service');
    for (const line of ['SocketUser=root', 'SocketGroup=printmaster', 'SocketMode=0660',
        'Accept=yes', 'MaxConnections=1', 'ListenStream=/run/printmaster-agent-update.sock']) {
        expect(socket).toContain(line);
    }
    for (const file of ['update-helper.json', 'update-publisher-root.json', 'update-policy.json']) {
        expect(socket).toContain(`ConditionPathExists=/etc/printmaster/${file}`);
    }
    for (const line of ['User=root', 'ExecStart=/usr/libexec/printmaster-agent-update\n',
        'StandardInput=socket', 'StandardOutput=socket', 'RuntimeMaxSec=16min',
        'KillMode=process', 'Restart=no']) {
        expect(service).toContain(line);
    }
    expect(service).not.toMatch(/PartOf=|BindsTo=|Requires=printmaster-agent\.service/);
});

test('release builds the architecture-matched helper before DEB and passes it into RPM packaging', () => {
    const workflow = yaml.load(read('.github/workflows/cd-agent.yml'));
    const steps = workflow.jobs['build-agent'].steps;
    const build = steps.find(step => step.name === 'Build independent package helper (Linux)');
    expect(build.if).toBe("matrix.goos == 'linux'");
    expect(build.env.GOARCH).toBe('${{ matrix.goarch }}');
    expect(build.run).toContain('./cmd/printmaster-agent-update');
    expect(build.run).toContain('./cmd/printmaster-agent-update-recovery');
    expect(steps.indexOf(build)).toBeLessThan(steps.findIndex(step => step.name === 'Build Debian Package (Linux)'));
    const upload = steps.find(step => step.name === 'Upload package helper (Linux)');
    const download = workflow.jobs['build-rpm'].steps.find(step => step.name === 'Download independent package helper');
    expect(upload.with.name).toBe('linux-package-helper-${{ matrix.goarch }}');
    expect(download.with.name).toBe('linux-package-helper-${{ matrix.arch }}');
    expect(upload.with['if-no-files-found']).toBe('error');
    expect(upload.with.path).toContain('printmaster-agent-update-recovery-linux-${{ matrix.goarch }}');
    // Public release collectors download agent-*; the internal helper is not a raw Agent asset.
    expect(upload.with.name).not.toMatch(/^agent-/);
});

const debianTest = process.platform === 'linux' ? test : test.skip;
test('destructive Linux systemd qualification is manual and disposable only', () => {
    const workflow = yaml.load(read('.github/workflows/linux-package-lifecycle.yml'));
    expect(Object.keys(workflow.on)).toEqual(['workflow_dispatch']);
    expect(workflow.jobs.qualify['runs-on']).toBe('ubuntu-26.04');
    expect(workflow.jobs.qualify.strategy.matrix.format).toEqual(['deb', 'rpm']);
    expect(workflow.jobs.qualify['timeout-minutes']).toBe(25);
    const fixture = read('scripts/qualify-linux-packages.sh');
    expect(fixture).toContain('disposable-github-runner');
    expect(fixture).toContain('RUNNER_ENVIRONMENT');
    expect(fixture).toContain('TestLiveLinuxPackageServiceLifecycle');
    expect(read('scripts/run-tests.js')).toContain('PRINTMASTER_LIVE_');
});

test('released legacy migration uses pinned assets and only disposable manual native jobs', () => {
    const workflow = yaml.load(read('.github/workflows/legacy-linux-migration.yml'));
    expect(Object.keys(workflow.on)).toEqual(['workflow_dispatch']);
    const job = workflow.jobs.migrate;
    expect(job['runs-on']).toBe('ubuntu-26.04');
    expect(job.strategy.matrix).toEqual({ version: ['0.31.0', '0.31.1'], format: ['deb', 'rpm'] });
    expect(job['timeout-minutes']).toBe(25);
    expect(job.env.PRINTMASTER_LIVE_LEGACY_MIGRATION).toBe('disposable-github-runner');
    const script = read('scripts/qualify-legacy-linux-migration.sh');
    expect(script).toContain('RUNNER_ENVIRONMENT');
    expect(script).toContain('GITHUB_ACTIONS');
    expect(script).toContain('TestLiveLegacyLinuxMigration');
    expect(script.match(/digest=[a-f0-9]{64}/g)).toHaveLength(4);
    expect(script.match(/sha256sum --check/g)).toHaveLength(2);
    expect(script).toContain('--proto-redir');
    expect(script).toContain('rpm2cpio');
    expect(script).toContain('dpkg-deb --extract');
    expect(script).toContain('candidate-agent');
    expect(script).not.toMatch(/--oldpackage|--force|--nodeps/);
    const fixture = read('agent/autoupdate/migration_live_linux_test.go');
    expect(fixture).toContain('PRAGMA integrity_check');
    expect(fixture).toContain('verifyNativeServiceExecutable');
    expect(fixture).toContain('packageHelperRootPath');
    expect(fixture).toContain('server_join_token');
});

debianTest('actual Debian build requires helper and contains protected helper/units with no privilege grant', () => {
    const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'pm-package-test-'));
    const run = (command, args) => {
        const result = spawnSync(command, args, { cwd: directory, encoding: 'utf8', timeout: 30000 });
        if (result.error) throw result.error;
        return result;
    };
    try {
        for (const name of ['build-deb.sh', 'printmaster-agent.service', 'config.example.toml']) {
            fs.copyFileSync(path.join(root, 'agent', name), path.join(directory, name));
        }
        fs.cpSync(path.join(root, 'agent/systemd'), path.join(directory, 'systemd'), { recursive: true });
        fs.writeFileSync(path.join(directory, 'printmaster-agent'), '#!/bin/sh\nexit 0\n');
        let result = run('bash', ['build-deb.sh', '1.2.3', 'amd64']);
        expect(result.status).not.toBe(0);
        expect(result.stderr).toContain('Missing Linux update helper');
        const helper = '#!/bin/sh\necho independent-helper\n';
        fs.writeFileSync(path.join(directory, 'printmaster-agent-update-linux-amd64'), helper);
        result = run('bash', ['build-deb.sh', '1.2.3', 'amd64']);
        expect(result.status).not.toBe(0);
        expect(result.stderr).toContain('recovery executable');
        fs.writeFileSync(path.join(directory, 'printmaster-agent-update-recovery-linux-amd64'), helper);
        result = run('bash', ['build-deb.sh', '1.2.3', 'amd64']);
        expect({ status: result.status, stderr: result.stderr }).toEqual({ status: 0, stderr: '' });
        const packagePath = path.join(directory, 'dist/printmaster-agent_1.2.3_amd64.deb');
        result = run('dpkg-deb', ['--extract', packagePath, path.join(directory, 'extracted')]);
        expect(result.status).toBe(0);
        const extracted = path.join(directory, 'extracted');
        expect(fs.readFileSync(path.join(extracted, 'usr/libexec/printmaster-agent-update'), 'utf8')).toBe(helper);
        expect(fs.readFileSync(path.join(extracted, 'usr/libexec/printmaster-agent-update-recovery'), 'utf8')).toBe(helper);
        expect(fs.statSync(path.join(extracted, 'usr/libexec/printmaster-agent-update')).mode & 0o777).toBe(0o755);
        expect(fs.statSync(path.join(extracted, 'etc/sudoers.d/printmaster-agent')).mode & 0o777).toBe(0o440);
        expect(fs.readFileSync(path.join(extracted, 'etc/sudoers.d/printmaster-agent'), 'utf8')).not.toMatch(/^[^#\s]/m);
        for (const unit of ['printmaster-agent-update.socket', 'printmaster-agent-update@.service']) {
            expect(fs.readFileSync(path.join(extracted, 'lib/systemd/system', unit), 'utf8').replaceAll('\r\n', '\n')).toBe(read(`agent/systemd/${unit}`));
        }
        for (const absent of ['update-helper.json', 'update-publisher-root.json', 'update-policy.json']) {
            expect(fs.existsSync(path.join(extracted, 'etc/printmaster', absent))).toBe(false);
        }
        result = run('dpkg-deb', ['--contents', packagePath]);
        expect(result.status).toBe(0);
        expect(result.stdout).toMatch(/-rwxr-xr-x root\/root[^\n]*\.\/usr\/libexec\/printmaster-agent-update/);
        expect(result.stdout).toMatch(/-r--r----- root\/root[^\n]*\.\/etc\/sudoers\.d\/printmaster-agent/);
    } finally {
        fs.rmSync(directory, { recursive: true, force: true });
    }
});
