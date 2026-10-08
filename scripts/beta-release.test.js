const fs = require('fs');
const path = require('path');
const os = require('os');
const { spawnSync } = require('child_process');

const root = path.resolve(__dirname, '..');
const bash = process.platform === 'win32' ? 'C:\\Program Files\\Git\\bin\\bash.exe' : 'bash';
const pwsh = 'pwsh';
const shells = ['bash', 'powershell'];
const hasPowerShell = spawnSync(pwsh, ['-NoProfile', '-Command', 'exit 0'], { timeout: 10000 }).status === 0;
const availableShells = shells.filter(shell => shell !== 'powershell' || hasPowerShell);
if (!hasPowerShell) {
    describe.skip('PowerShell Beta release lifecycle', () => {
        test('requires pwsh; Bash coverage remains active', () => {});
    });
}

function execute(command, args, cwd) {
    const result = spawnSync(command, args, { cwd, encoding: 'utf8', timeout: 30000 });
    if (result.error) throw result.error;
    return result;
}
function checked(command, args, cwd) {
    const result = execute(command, args, cwd);
    if (result.status !== 0) throw new Error(`${command} ${args.join(' ')}\n${result.stdout}\n${result.stderr}`);
    return result.stdout.trim();
}

describe.each(availableShells)('%s Beta release lifecycle', shell => {
    let directory;
    const git = (...args) => checked('git', args, directory);
    const read = component => fs.readFileSync(path.join(directory, component, 'VERSION'), 'utf8');
    function release(component, action, flags = []) {
        const args = shell === 'bash'
            ? [path.join(directory, 'release.sh'), component, action, '--skip-tests', '--skip-push', ...flags]
            : ['-NoProfile', '-File', path.join(directory, 'release.ps1'), component, action, '-SkipTests', '-SkipPush', ...flags];
        return execute(shell === 'bash' ? bash : pwsh, args, directory);
    }
    function successful(component, action, flags) {
        const result = release(component, action, flags);
        if (result.status !== 0) throw new Error(`${result.stdout}\n${result.stderr}`);
        return result.stdout;
    }
    beforeEach(() => {
        directory = fs.mkdtempSync(path.join(os.tmpdir(), 'pm-beta-release-'));
        for (const script of ['release.sh', 'release.ps1']) fs.copyFileSync(path.join(root, script), path.join(directory, script));
        fs.mkdirSync(path.join(directory, 'scripts'));
        fs.copyFileSync(path.join(root, 'scripts', 'release-version.sh'), path.join(directory, 'scripts', 'release-version.sh'));
        fs.writeFileSync(path.join(directory, 'build.sh'), '#!/usr/bin/env bash\nexit 0\n', { mode: 0o755 });
        fs.writeFileSync(path.join(directory, 'build.ps1'), 'param($Component, [switch]$Release, [switch]$VerboseBuild)\n$global:LASTEXITCODE = 0\n');
        for (const component of ['agent', 'server']) {
            fs.mkdirSync(path.join(directory, component));
            fs.writeFileSync(path.join(directory, component, 'VERSION'), '0.31.1');
        }
        git('init', '-b', 'main');
        git('config', 'user.email', 'test@example.invalid');
        git('config', 'user.name', 'Release Test');
        git('add', '.');
        git('commit', '-m', 'feat: initial release fixture');
        for (const component of ['agent', 'server']) {
            for (const tag of [`${component}-v0.31.1`, `latest-${component}`, `${component}-v0`, `${component}-v0.31`]) git('tag', tag);
        }
        fs.writeFileSync(path.join(directory, 'change.txt'), 'next release');
        git('add', '.');
        git('commit', '-m', 'feat: next release');
    });
    afterEach(() => fs.rmSync(directory, { recursive: true, force: true }));

    test.each(['agent', 'server', 'both'])('%s starts Beta, advances Beta, then promotes the exact Stable target', component => {
        const components = component === 'both' ? ['agent', 'server'] : [component];
        const stable = git('rev-parse', 'latest-agent');
        successful(component, 'minor', [shell === 'bash' ? '--beta' : '-Beta']);
        for (const item of components) {
            expect(read(item)).toBe('0.32.0-beta.1');
            expect(git('tag', '-l', `${item}-v0.32.0-beta.1`)).toBe(`${item}-v0.32.0-beta.1`);
            expect(git('rev-parse', `latest-${item}`)).toBe(stable);
            expect(git('rev-parse', `${item}-v0.31`)).toBe(stable);
        }
        expect(release(component, 'minor').status).not.toBe(0);
        for (const item of components) expect(read(item)).toBe('0.32.0-beta.1');
        successful(component, 'beta');
        for (const item of components) expect(read(item)).toBe('0.32.0-beta.2');
        successful(component, 'stable');
        for (const item of components) {
            expect(read(item)).toBe('0.32.0');
            expect(git('rev-parse', `latest-${item}^{}`)).toBe(git('rev-parse', 'HEAD'));
            expect(git('rev-parse', `${item}-v0.32^{}`)).toBe(git('rev-parse', 'HEAD'));
        }
    }, 30000);

    test('dry-run previews both versions without any mutations or build', () => {
        fs.writeFileSync(path.join(directory, 'build.sh'), '#!/usr/bin/env bash\nexit 42\n');
        fs.writeFileSync(path.join(directory, 'build.ps1'), 'throw "Dry run must not build"\n');
        git('add', '.');
        git('commit', '-m', 'test: prohibit dry run build');
        const head = git('rev-parse', 'HEAD');
        const tags = git('tag');
        const output = successful('both', 'minor', shell === 'bash' ? ['--beta', '--dry-run'] : ['-Beta', '-DryRun']);
        expect(output).toContain('0.32.0-beta.1');
        expect(read('agent')).toBe('0.31.1');
        expect(read('server')).toBe('0.31.1');
        expect(git('rev-parse', 'HEAD')).toBe(head);
        expect(git('tag')).toBe(tags);
        expect(git('status', '--porcelain')).toBe('');
    });

    test('invalid transitions and dirty files fail without losing edits', () => {
        expect(release('agent', 'beta').status).not.toBe(0);
        expect(release('agent', 'stable').status).not.toBe(0);
        fs.writeFileSync(path.join(directory, 'agent', 'VERSION'), 'personal edit');
        expect(release('agent', 'minor').status).not.toBe(0);
        expect(read('agent')).toBe('personal edit');
    });

    test('duplicate tag and second-component failure leave versions unchanged', () => {
        git('tag', 'server-v0.32.0-beta.1');
        expect(release('both', 'minor', [shell === 'bash' ? '--beta' : '-Beta']).status).not.toBe(0);
        expect(read('agent')).toBe('0.31.1');
        expect(read('server')).toBe('0.31.1');
        expect(git('status', '--porcelain')).toBe('');
    });

    test('workflow version outputs isolate Stable, Beta and Dev and reject mismatched tags', () => {
        const helper = ref => execute(bash, [path.join(directory, 'scripts', 'release-version.sh'), 'agent', ref], directory);
        expect(helper('refs/tags/agent-v0.31.1').stdout).toContain('is_release=true');
        successful('agent', 'minor', [shell === 'bash' ? '--beta' : '-Beta']);
        const beta = helper('refs/tags/agent-v0.32.0-beta.1');
        expect(beta.status).toBe(0);
        expect(beta.stdout).toContain('version=0.32.0-beta.1');
        expect(beta.stdout).toContain('build_type=beta');
        expect(beta.stdout).toContain('is_release=false');
        expect(beta.stdout).toContain('is_beta=true');
        const dev = helper('refs/heads/main');
        expect(dev.stdout).toContain('version=0.32.0-dev.');
        expect(dev.stdout).not.toContain('beta.1-dev');
        expect(helper('refs/tags/agent-v0.32.0').status).not.toBe(0);
    });
});

describe('Beta package metadata', () => {
    let directory;
    beforeEach(() => {
        directory = fs.mkdtempSync(path.join(os.tmpdir(), 'pm-beta-packaging-'));
        for (const file of ['build-deb.sh', 'build-rpm.sh', 'printmaster-agent.service', 'config.example.toml']) {
            fs.copyFileSync(path.join(root, 'agent', file), path.join(directory, file));
        }
        fs.mkdirSync(path.join(directory, 'fedora'));
        fs.copyFileSync(path.join(root, 'agent', 'fedora', 'printmaster-agent.spec'), path.join(directory, 'fedora', 'printmaster-agent.spec'));
        fs.writeFileSync(path.join(directory, 'printmaster-agent'), 'test binary');
        fs.mkdirSync(path.join(directory, 'tools'));
        fs.writeFileSync(path.join(directory, 'tools', 'dpkg-deb'),
            '#!/usr/bin/env bash\nsed -n "s/^Version: //p" "$3/DEBIAN/control" > "$4"\n', { mode: 0o755 });
        fs.writeFileSync(path.join(directory, 'tools', 'rpmbuild'),
            '#!/usr/bin/env bash\nroot="${2#_topdir }"\nmkdir -p "$root/RPMS"\nprintf "%s" "$PRINTMASTER_VERSION" > "$root/RPMS/printmaster-agent-$PRINTMASTER_VERSION-1.fc44.x86_64.rpm"\n', { mode: 0o755 });
    });
    afterEach(() => fs.rmSync(directory, { recursive: true, force: true }));
    test.each(['deb', 'rpm'])('%s uses pre-Stable package ordering and retains the SemVer asset name', kind => {
        const script = path.join(directory, `build-${kind}.sh`);
        const result = execute(bash, ['-c', 'export PATH="$(pwd)/tools:$PATH"; bash "$1" 0.32.0-beta.1 amd64', 'test', script], directory);
        if (result.status !== 0) throw new Error(`${result.stdout}\n${result.stderr}`);
        const filename = fs.readdirSync(path.join(directory, 'dist')).find(file => file.endsWith(`.${kind}`));
        expect(filename).toContain('0.32.0-beta.1');
        expect(fs.readFileSync(path.join(directory, 'dist', filename), 'utf8').trim()).toBe('0.32.0~beta.1');
    });
});

test.each(['agent', 'server'])('%s workflow publishes Beta without moving Stable aliases', component => {
    const workflow = fs.readFileSync(path.join(root, '.github', 'workflows', `cd-${component}.yml`), 'utf8');
    const ci = fs.readFileSync(path.join(root, '.github', 'workflows', 'ci.yml'), 'utf8');
    const pattern = `${component}-v[0-9]+.[0-9]+.[0-9]+-beta.[0-9]+`;
    expect(workflow).toContain(pattern);
    expect(ci).toContain(pattern);
    expect(workflow.match(/bash scripts\/release-version.sh/g)).toHaveLength(component === 'agent' ? 5 : 4);
    expect(workflow).toContain('flavor: latest=false');
    expect(workflow).toContain('type=raw,value=latest,enable=${{ steps.version.outputs.is_release }}');
    expect(workflow).toContain('type=raw,value=beta,enable=${{ steps.version.outputs.is_beta }}');
    expect(workflow).toContain("prerelease: ${{ steps.version.outputs.is_beta == 'true' }}");
    expect(workflow).toContain("make_latest: ${{ steps.version.outputs.is_beta != 'true' }}");
    if (component === 'agent') {
        expect(workflow).toContain("if: startsWith(github.ref, 'refs/tags/agent-v') && !contains(github.ref, '-beta.')");
        expect(workflow).toContain("steps.version.outputs.build_type == 'release'");
    }
});
