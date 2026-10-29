import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { mkdtemp, mkdir, readFile, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

import { buildAgentReleaseInventory, buildDevInventory, resolveDevReleaseVersion } from './dev-update-inventory.mjs';

const sourceSha = '1234567890abcdef1234567890abcdef12345678';
const version = `0.32.0-dev.${sourceSha.slice(0, 7)}`;
const assets = [
    ['linux', 'amd64', ''],
    ['linux', 'arm64', ''],
    ['windows', 'amd64', '.exe'],
    ['darwin', 'amd64', ''],
    ['darwin', 'arm64', ''],
];

async function fixture() {
    const directory = await mkdtemp(path.join(os.tmpdir(), 'pm-dev-inventory-'));
    const sourceRoot = path.join(directory, 'source');
    const artifactDir = path.join(directory, 'artifacts');
    await mkdir(path.join(sourceRoot, 'agent'), { recursive: true });
    await mkdir(path.join(sourceRoot, 'common', 'cmd', 'update-publisher'), { recursive: true });
    await mkdir(artifactDir);
    await writeFile(path.join(sourceRoot, 'agent', 'VERSION'), '0.32.0-beta.3\n');
    const repositoryRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
    const rootBytes = await readFile(path.join(repositoryRoot, 'agent', 'update_publisher_root.json'));
    await writeFile(path.join(sourceRoot, 'agent', 'update_publisher_root.json'), rootBytes);
    const registry = await readFile(path.join(repositoryRoot, 'common', 'cmd', 'update-publisher', 'dev-bootstrap-baselines.json'));
    await writeFile(path.join(sourceRoot, 'common', 'cmd', 'update-publisher', 'dev-bootstrap-baselines.json'), registry);
    for (const [platform, arch, suffix] of assets) {
        await writeFile(path.join(artifactDir, `printmaster-agent-v${version}-${platform}-${arch}${suffix}`), `${platform}-${arch}`);
    }
    return { sourceRoot, artifactDir, publisherRoot: rootBytes.toString('utf8') };
}

test('inventory binds all five Dev binaries to one source/version/root', async () => {
    const input = await fixture();
    const targets = await buildDevInventory({ ...input, sourceSha, publisherRoot: input.publisherRoot });
    assert.equal(targets.length, 5);
    assert.deepEqual(targets.map(item => `${item.target.platform}/${item.target.arch}`).sort(),
        ['darwin/amd64', 'darwin/arm64', 'linux/amd64', 'linux/arm64', 'windows/amd64']);
    for (const item of targets) {
        assert.equal(item.target.channel, 'dev');
        assert.equal(item.target.version, version);
        assert.equal(item.target.format, 'binary');
        assert.ok(/^[a-f0-9]{64}$/.test(item.target.sha256));
        assert.ok(item.target.size_bytes > 0);
    }
});

test('inventory rejects an incomplete matrix, extra files, and wrong source SHA', async () => {
    const input = await fixture();
    await assert.rejects(buildDevInventory({ ...input, sourceSha: sourceSha.slice(0, -1), publisherRoot: input.publisherRoot }), /source commit SHA/);
    await writeFile(path.join(input.artifactDir, 'extra.bin'), 'extra');
    await assert.rejects(buildDevInventory({ ...input, sourceSha, publisherRoot: input.publisherRoot }), /artifact set/);
});

test('inventory rejects a mismatched publisher root', async () => {
    const input = await fixture();
    const wrongRoot = JSON.stringify({ _type: 'root', version: 2 });
    await assert.rejects(buildDevInventory({ ...input, sourceSha, publisherRoot: wrongRoot }), /root differs/);
});

test('target digests match the exact artifact bytes', async () => {
    const input = await fixture();
    const targets = await buildDevInventory({ ...input, sourceSha, publisherRoot: input.publisherRoot });
    const targetFile = targets.find(item => item.target.platform === 'windows').file;
    assert.equal(await readFile(targetFile, 'utf8'), 'windows-amd64');
});

test('release inventory binds five binaries to the exact Beta or Stable source, root, and channel', async () => {
    const input = await fixture();
    const betaVersion = '0.32.0-beta.3';
    const artifactDir = path.join(path.dirname(input.artifactDir), 'beta-artifacts');
    await mkdir(artifactDir);
    for (const [platform, arch, suffix] of assets) {
        await writeFile(path.join(artifactDir, `printmaster-agent-v${betaVersion}-${platform}-${arch}${suffix}`), `beta-${platform}-${arch}`);
    }

    const targets = await buildAgentReleaseInventory({ ...input, artifactDir, publisherRoot: input.publisherRoot, channel: 'beta', version: betaVersion });
    assert.equal(targets.length, 5);
    for (const item of targets) {
        assert.equal(item.target.channel, 'beta');
        assert.equal(item.target.version, betaVersion);
        assert.equal(item.target.format, 'binary');
        assert.ok(item.target.size_bytes > 0);
        assert.ok(/^[a-f0-9]{64}$/.test(item.target.sha256));
    }
    await assert.rejects(buildAgentReleaseInventory({ ...input, artifactDir, publisherRoot: input.publisherRoot, channel: 'beta', version: '0.32.0-beta.4' }), /match Agent VERSION/);
    await assert.rejects(buildAgentReleaseInventory({ ...input, artifactDir, publisherRoot: JSON.stringify({ _type: 'root', version: 2 }), channel: 'beta', version: betaVersion }), /root differs/);

    const stableVersion = '0.32.0';
    await writeFile(path.join(input.sourceRoot, 'agent', 'VERSION'), `${stableVersion}\n`);
    const stableArtifactDir = path.join(path.dirname(input.artifactDir), 'stable-artifacts');
    await mkdir(stableArtifactDir);
    for (const [platform, arch, suffix] of assets) {
        await writeFile(path.join(stableArtifactDir, `printmaster-agent-v${stableVersion}-${platform}-${arch}${suffix}`), `stable-${platform}-${arch}`);
    }
    const stableTargets = await buildAgentReleaseInventory({
        ...input, artifactDir: stableArtifactDir, publisherRoot: input.publisherRoot,
        channel: 'stable', version: stableVersion,
    });
    assert.equal(stableTargets.length, 5);
    assert.ok(stableTargets.every(item => item.target.channel === 'stable' && item.target.version === stableVersion));
    await assert.rejects(buildAgentReleaseInventory({
        ...input, artifactDir: stableArtifactDir, publisherRoot: input.publisherRoot,
        channel: 'stable', version: betaVersion,
    }), /does not match the selected/);
});

test('first-publication inventory includes the pinned root-bearing baseline and next Dev build', async () => {
    const input = await fixture();
    const baselineDir = path.join(path.dirname(input.artifactDir), 'baseline');
    const baselineVersion = '0.32.0-dev.a3706f8';
    const baselineFolder = path.join(baselineDir, baselineVersion);
    await mkdir(baselineFolder, { recursive: true });
    const baselineDigests = {};
    for (const [platform, arch, suffix] of assets) {
        const name = `printmaster-agent-v${baselineVersion}-${platform}-${arch}${suffix}`;
        const bytes = Buffer.from(`pinned-${platform}-${arch}`);
        await writeFile(path.join(baselineFolder, name), bytes);
        baselineDigests[`${platform}/${arch}`] = createHash('sha256').update(bytes).digest('hex');
    }
    const rootBytes = await readFile(path.join(input.sourceRoot, 'agent', 'update_publisher_root.json'));
    const baselineRegistry = {
        schema: 1,
        root_sha256: createHash('sha256').update(rootBytes).digest('hex'),
        releases: [{
            version: baselineVersion,
            source_sha: 'a3706f8710c81aa60e60bb84ea5e6b910a5cbb2f',
            digests: baselineDigests,
        }],
    };
    const targets = await buildDevInventory({
        ...input, sourceSha, publisherRoot: input.publisherRoot, baselineDir,
        baselineRootPath: path.join(input.sourceRoot, 'agent', 'update_publisher_root.json'), baselineRegistry,
    });
    assert.equal(targets.length, 10);
    assert.equal(targets.filter(item => item.target.version === baselineVersion).length, 5);
    assert.equal(targets.filter(item => item.target.version === version).length, 5);
    baselineRegistry.releases[0].digests[Object.keys(baselineDigests)[0]] = '0'.repeat(64);
    await assert.rejects(buildDevInventory({
        ...input, sourceSha, publisherRoot: input.publisherRoot, baselineDir,
        baselineRootPath: path.join(input.sourceRoot, 'agent', 'update_publisher_root.json'), baselineRegistry,
    }), /pinned baseline digest mismatch/);
});

test('release version resolver matches the unique tag to the exact source SHA', () => {
    const releases = [
        { tagName: `agent-v0.32.0-dev.${sourceSha.slice(0, 7)}`, targetCommitish: sourceSha },
        { tagName: 'agent-v0.32.0-dev.deadbee', targetCommitish: 'deadbeef'.repeat(5) },
    ];
    const tagVersion = `0.32.0-dev.${sourceSha.slice(0, 7)}`;
    assert.equal(resolveDevReleaseVersion(releases, sourceSha, '0.32.0'), tagVersion);
    assert.equal(resolveDevReleaseVersion(releases[0], sourceSha, '0.32.0'), tagVersion);
    assert.throws(() => resolveDevReleaseVersion(releases, '0'.repeat(40), '0.32.0'), /found 0/);
    assert.throws(() => resolveDevReleaseVersion([
        ...releases,
        { tagName: `agent-v0.32.0-dev.${sourceSha.slice(0, 8)}`, targetCommitish: sourceSha },
    ], sourceSha, '0.32.0'), /found 2/);
});