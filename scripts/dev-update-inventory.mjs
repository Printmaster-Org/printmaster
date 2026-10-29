import { createHash } from 'node:crypto';
import { lstat, readFile, readdir, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const REQUIRED = [
    ['linux', 'amd64', ''],
    ['linux', 'arm64', ''],
    ['windows', 'amd64', '.exe'],
    ['darwin', 'amd64', ''],
    ['darwin', 'arm64', ''],
];
const MAX_ARTIFACT_BYTES = 512 * 1024 * 1024;
const DEV_BOOTSTRAP_ROOT_SHA256 = '201bedf94312320adc7056e0cde18deb1633c3707dbd1f978a6cdff2f25e1b14';
const BASELINE_REGISTRY = path.join('common', 'cmd', 'update-publisher', 'dev-bootstrap-baselines.json');

function canonical(value) {
    if (Array.isArray(value)) return value.map(canonical);
    if (value && typeof value === 'object') {
        return Object.fromEntries(Object.keys(value).sort().map(key => [key, canonical(value[key])]));
    }
    return value;
}

function baseAgentVersion(value) {
    const baseVersion = value.trim().split('-', 1)[0];
    if (!/^\d+\.\d+\.\d+$/.test(baseVersion)) throw new Error('Agent VERSION must begin with a stable numeric version');
    return baseVersion;
}

export function resolveDevReleaseVersion(releases, sourceSha, baseVersion) {
    const releaseList = Array.isArray(releases) ? releases : releases && typeof releases === 'object' ? [releases] : null;
    if (!releaseList || !/^[a-f0-9]{40}$/.test(sourceSha || '') || !/^\d+\.\d+\.\d+$/.test(baseVersion || '')) {
        throw new Error('valid release list, source SHA, and base version are required');
    }
    const tagPattern = new RegExp(`^agent-v${baseVersion.replaceAll('.', '\\.')}\\-dev\\.([a-f0-9]{7,40})$`);
    const matches = releaseList.filter(release => {
        if (String(release.targetCommitish || '').toLowerCase() !== sourceSha) return false;
        const match = tagPattern.exec(release.tagName || '');
        return match && sourceSha.startsWith(match[1]);
    });
    if (matches.length !== 1) throw new Error(`expected one Dev release tag for source ${sourceSha}, found ${matches.length}`);
    return matches[0].tagName.slice('agent-v'.length);
}

export async function loadDevBaselines(sourceRoot, baseVersion, registryOverride = null) {
    const registry = registryOverride || JSON.parse(await readFile(path.join(sourceRoot, BASELINE_REGISTRY), 'utf8'));
    if (registry.schema !== 1 || registry.root_sha256 !== DEV_BOOTSTRAP_ROOT_SHA256 ||
        !Array.isArray(registry.releases) || registry.releases.length === 0) {
        throw new Error('Dev baseline registry schema/root is invalid; owner transition review required');
    }
    const seenVersions = new Set();
    const seenSources = new Set();
    const expected = REQUIRED.map(([platform, arch]) => `${platform}/${arch}`).sort();
    for (const release of registry.releases) {
        const marker = `${baseVersion}-dev.`;
        const suffix = release.version.startsWith(marker) ? release.version.slice(marker.length) : '';
        if (suffix.length < 7 || suffix.length > 40 || !/^[a-f0-9]{40}$/.test(release.source_sha || '') ||
            !release.source_sha.startsWith(suffix) || seenVersions.has(release.version) || seenSources.has(release.source_sha)) {
            throw new Error(`invalid/duplicate pinned Dev baseline identity: ${release.version}`);
        }
        const keys = Object.keys(release.digests || {}).sort();
        if (JSON.stringify(keys) !== JSON.stringify(expected) || keys.some(key => !/^[a-f0-9]{64}$/.test(release.digests[key]))) {
            throw new Error(`invalid pinned Dev baseline digest set: ${release.version}`);
        }
        seenVersions.add(release.version);
        seenSources.add(release.source_sha);
    }
    return registry;
}

async function buildVersionTargets({ artifactDir, version, channel = 'dev', expectedDigests = null }) {
    const expectedNames = REQUIRED.map(([platform, arch, suffix]) => `printmaster-agent-v${version}-${platform}-${arch}${suffix}`).sort();
    const files = await readdir(artifactDir, { withFileTypes: true });
    if (files.some(entry => !entry.isFile())) throw new Error(`artifact directory for ${version} must contain only flat regular files`);
    const actualNames = files.map(entry => entry.name).sort();
    if (JSON.stringify(actualNames) !== JSON.stringify(expectedNames)) {
        throw new Error(`artifact set for ${version} does not match required matrix; expected ${expectedNames.join(', ')}`);
    }

    const targets = [];
    for (const [platform, arch, suffix] of REQUIRED) {
        const filename = `printmaster-agent-v${version}-${platform}-${arch}${suffix}`;
        const file = path.resolve(artifactDir, filename);
        const info = await lstat(file);
        if (!info.isFile() || info.isSymbolicLink() || info.size <= 0 || info.size > MAX_ARTIFACT_BYTES) {
            throw new Error(`invalid Dev binary file: ${filename}`);
        }
        const digest = createHash('sha256').update(await readFile(file)).digest('hex');
        if (expectedDigests && expectedDigests[`${platform}/${arch}`] !== digest) {
            throw new Error(`pinned baseline digest mismatch: ${filename}`);
        }
        targets.push({
            target: { component: 'agent', channel, version, platform, arch, format: 'binary', sha256: digest, size_bytes: info.size },
            file,
        });
    }
    return targets;
}

export async function buildAgentReleaseInventory({ artifactDir, sourceRoot, publisherRoot, channel, version }) {
    const validVersion = channel === 'beta'
        ? /^\d+\.\d+\.\d+-beta\.[1-9]\d*$/.test(version || '')
        : channel === 'stable' && /^\d+\.\d+\.\d+$/.test(version || '');
    if (!validVersion) throw new Error('release version does not match the selected Beta or Stable channel');
    const sourceVersion = (await readFile(path.join(sourceRoot, 'agent', 'VERSION'), 'utf8')).trim();
    if (sourceVersion !== version) throw new Error('release version must match Agent VERSION at the tagged source');

    const rootBytes = await readFile(path.join(sourceRoot, 'agent', 'update_publisher_root.json'));
    const root = JSON.parse(rootBytes.toString('utf8'));
    if (createHash('sha256').update(rootBytes).digest('hex') !== DEV_BOOTSTRAP_ROOT_SHA256) {
        throw new Error('release publisher root fingerprint changed; owner transition review required');
    }
    const trustedRoot = JSON.parse(publisherRoot);
    if (JSON.stringify(canonical(root)) !== JSON.stringify(canonical(trustedRoot))) {
        throw new Error('release embedded root differs from the publisher root');
    }
    return buildVersionTargets({ artifactDir, version, channel });
}

export async function buildDevInventory({ artifactDir, sourceRoot, sourceSha, publisherRoot, version: requestedVersion = '', baselineDir = null, baselineRootPath = null, baselineRegistry = null }) {
    if (!/^[a-f0-9]{40}$/.test(sourceSha || '')) throw new Error('full source commit SHA is required');

    const baseVersion = baseAgentVersion(await readFile(path.join(sourceRoot, 'agent', 'VERSION'), 'utf8'));
    const registry = await loadDevBaselines(sourceRoot, baseVersion, baselineRegistry);
    const defaultVersion = `${baseVersion}-dev.${sourceSha.slice(0, 7)}`;
    const version = requestedVersion || defaultVersion;
    const marker = `${baseVersion}-dev.`;
    const commitSuffix = version.startsWith(marker) ? version.slice(marker.length) : '';
    if (commitSuffix.length < 7 || commitSuffix.length > sourceSha.length || !sourceSha.startsWith(commitSuffix)) {
        throw new Error('Dev version must use a unique prefix of the source commit SHA');
    }

    const rootBytes = await readFile(path.join(sourceRoot, 'agent', 'update_publisher_root.json'));
    const root = JSON.parse(rootBytes.toString('utf8'));
    const rootSHA256 = createHash('sha256').update(rootBytes).digest('hex');
    if (registry.root_sha256 !== rootSHA256) {
        throw new Error('Dev baseline registry schema/root is invalid; owner transition review required');
    }
    const trustedRoot = JSON.parse(publisherRoot);
    if (JSON.stringify(canonical(root)) !== JSON.stringify(canonical(trustedRoot))) {
        throw new Error('candidate embedded root differs from the publisher root');
    }

    const targets = [];
    if (baselineDir) {
        if (!baselineRootPath) throw new Error('pinned baseline root file is required');
        const baselineRoot = await readFile(baselineRootPath);
        if (createHash('sha256').update(baselineRoot).digest('hex') !== registry.root_sha256 ||
            JSON.stringify(canonical(JSON.parse(baselineRoot.toString('utf8')))) !== JSON.stringify(canonical(root))) {
            throw new Error('pinned baseline root differs from current publisher root');
        }
        const baselineFolders = (await readdir(baselineDir, { withFileTypes: true })).filter(entry => entry.isDirectory()).map(entry => entry.name).sort();
        const baselineVersions = registry.releases.map(release => release.version).sort();
        if (JSON.stringify(baselineFolders) !== JSON.stringify(baselineVersions)) {
            throw new Error('Dev baseline artifact directories do not match the pinned registry');
        }
        for (const baseline of registry.releases) {
            targets.push(...await buildVersionTargets({
                artifactDir: path.join(baselineDir, baseline.version), version: baseline.version,
                expectedDigests: baseline.digests,
            }));
        }
        if (registry.releases.some(release => release.version === version)) throw new Error('new Dev build is already present in the baseline registry');
    }
    targets.push(...await buildVersionTargets({ artifactDir, version }));
    return targets;
}

async function main() {
    if (process.argv[2] === '--release-inventory') {
        const [artifactDir, sourceRoot, outputPath, channel, version] = process.argv.slice(3);
        if (!artifactDir || !sourceRoot || !outputPath || !channel || !version || process.argv.length !== 8) {
            throw new Error('usage: dev-update-inventory.mjs --release-inventory ARTIFACT_DIR SOURCE_ROOT OUTPUT_JSON CHANNEL VERSION');
        }
        const publisherRoot = process.env.UPDATE_ROOT_JSON;
        if (!publisherRoot) throw new Error('publisher root is required');
        const targets = await buildAgentReleaseInventory({ artifactDir, sourceRoot, publisherRoot, channel, version });
        await writeFile(outputPath, `${JSON.stringify(targets, null, 2)}\n`, { mode: 0o600, flag: 'wx' });
        process.stdout.write(`Validated ${targets.length} ${channel} Agent binaries for ${version}.\n`);
        return;
    }
    if (process.argv[2] === '--baseline-list') {
        const sourceRoot = process.argv[3];
        if (!sourceRoot || process.argv.length !== 4) throw new Error('usage: dev-update-inventory.mjs --baseline-list SOURCE_ROOT');
        const baseVersion = baseAgentVersion(await readFile(path.join(sourceRoot, 'agent', 'VERSION'), 'utf8'));
        process.stdout.write(`${JSON.stringify((await loadDevBaselines(sourceRoot, baseVersion)).releases)}\n`);
        return;
    }
    if (process.argv[2] === '--resolve-version') {
        const [, , , releasesPath, sourceSha, baseVersion] = process.argv;
        if (!releasesPath || !sourceSha || !baseVersion || process.argv.length !== 6) {
            throw new Error('usage: dev-update-inventory.mjs --resolve-version RELEASES_JSON SOURCE_SHA BASE_VERSION');
        }
        const releases = JSON.parse(await readFile(releasesPath, 'utf8'));
        process.stdout.write(`${resolveDevReleaseVersion(releases, sourceSha, baseVersion)}\n`);
        return;
    }
    const [artifactDir, sourceRoot, sourceSha, outputPath, baselineDir, baselineRootPath, version] = process.argv.slice(2);
    if (!artifactDir || !sourceRoot || !sourceSha || !outputPath || !baselineDir || !baselineRootPath || !version || process.argv.length !== 9) {
        throw new Error('usage: dev-update-inventory.mjs ARTIFACT_DIR SOURCE_ROOT SOURCE_SHA OUTPUT_JSON BASELINE_DIR BASELINE_ROOT_FILE DEV_VERSION');
    }
    const publisherRoot = process.env.UPDATE_ROOT_JSON;
    if (!publisherRoot) throw new Error('publisher root is required');
    const targets = await buildDevInventory({ artifactDir, sourceRoot, sourceSha, publisherRoot,
        baselineDir, baselineRootPath, version });
    await writeFile(outputPath, `${JSON.stringify(targets, null, 2)}\n`, { mode: 0o600, flag: 'wx' });
    process.stdout.write(`Validated ${targets.length} Dev Agent binaries for source ${sourceSha}.\n`);
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
    main().catch(error => {
        console.error(`Dev inventory rejected: ${error.message}`);
        process.exitCode = 1;
    });
}