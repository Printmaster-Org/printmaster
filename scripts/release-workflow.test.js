const fs = require('fs');
const path = require('path');
const os = require('os');
const { spawnSync } = require('child_process');

const root = path.resolve(__dirname, '..');
const workflow = fs.readFileSync(path.join(root, '.github/workflows/cd-agent.yml'), 'utf8');
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
    function prepare() {
        return spawnSync('bash', [path.join(root, 'scripts/prepare-agent-dev-release.sh'), directory, version], { encoding: 'utf8' });
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
        expect(prepare().status).not.toBe(0);
    });
});