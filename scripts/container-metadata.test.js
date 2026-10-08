const fs = require('fs');
const path = require('path');
const os = require('os');
const { spawnSync } = require('child_process');
const yaml = require('js-yaml');

const root = path.resolve(__dirname, '..');
const bash = process.platform === 'win32' ? 'C:\\Program Files\\Git\\bin\\bash.exe' : 'bash';
const revision = 'a'.repeat(40);
const created = '2026-10-08T20:00:00Z';
function metadata(component, version, buildType, sha = revision, timestamp = created) {
    return spawnSync(bash, [path.join(root, 'scripts', 'container-metadata.sh'), component, version, buildType, sha, timestamp], { encoding: 'utf8' });
}
function labelsFrom(result) {
    if (result.status !== 0) throw new Error(`Metadata generation failed:\n${result.stdout}\n${result.stderr}`);
    return Object.fromEntries(result.stdout.trim().split('\n').map(line => {
        const index = line.indexOf('=');
        return [line.slice(0, index), line.slice(index + 1)];
    }));
}

test.each(['agent', 'server'])('%s metadata describes the product and links exact Stable, Beta and Dev release notes', component => {
    for (const [version, buildType, channel] of [
        ['0.32.0', 'release', 'stable'],
        ['0.32.0-beta.10', 'beta', 'beta'],
        ['0.32.0-dev.ab123cd', 'dev', 'dev'],
    ]) {
        const labels = labelsFrom(metadata(component, version, buildType));
        expect(labels['org.opencontainers.image.description'].length).toBeLessThanOrEqual(512);
        expect(labels['org.opencontainers.image.description']).toContain('Docs: https://docs.printmaster.work/deployment/docker/');
        const releaseURL = `https://github.com/Printmaster-Org/printmaster/releases/tag/${component}-v${version}`;
        expect(labels['org.opencontainers.image.description']).toContain(`Release notes: ${releaseURL}`);
        expect(labels['org.opencontainers.image.url']).toBe(releaseURL);
        expect(labels['org.opencontainers.image.licenses']).toBe('MIT');
        expect(labels['org.opencontainers.image.revision']).toBe(revision);
        expect(labels['org.opencontainers.image.created']).toBe(created);
        expect(labels['org.opencontainers.image.version']).toBe(version);
        expect(labels['work.printmaster.image.channel']).toBe(channel);
    }
});

test('invalid or inconsistent container metadata fails explicitly', () => {
    for (const args of [
        ['other', '0.32.0', 'release'],
        ['agent', '0.32.0-beta.1', 'release'],
        ['agent', '0.32.0', 'beta'],
        ['agent', '0.32.0', 'dev'],
        ['agent', '0.32.0-dev.abc', 'beta'],
        ['server', 'invalid\nLABEL=injected', 'release'],
        ['server', '0.32.0', 'release', 'short'],
        ['server', '0.32.0', 'release', revision, 'not-UTC'],
        ['agent', `${'9'.repeat(512)}.0.0`, 'release'],
    ]) {
        const result = metadata(...args);
        expect(result.status).not.toBe(0);
        expect(result.stderr.trim()).not.toBe('');
        expect(result.stdout).toBe('');
    }
});

test.each(['agent', 'server'])('%s workflow labels platform images and annotates the multi-arch index without splitting descriptions', component => {
    const workflow = yaml.load(fs.readFileSync(path.join(root, '.github', 'workflows', `cd-${component}.yml`), 'utf8'));
    for (const jobName of ['build-docker', 'merge-docker']) {
        const steps = workflow.jobs[jobName].steps;
        const generator = steps.find(step => step.id === 'container_metadata');
        expect(generator.env.REVISION).toBe('${{ github.sha }}');
        expect(generator.env.VERSION).toBe('${{ steps.version.outputs.version }}');
        expect(generator.env.BUILD_TYPE).toBe('${{ steps.version.outputs.build_type }}');
        expect(generator.env.CREATED).toBe('${{ steps.version.outputs.build_time }}');
        expect(generator.run).toContain(`bash scripts/container-metadata.sh ${component}`);
        const consumer = steps.find(step => step.id === (jobName === 'build-docker' ? 'build' : 'meta'));
        expect(steps.indexOf(generator)).toBeLessThan(steps.indexOf(consumer));
        expect(consumer.with.labels).toBe('${{ steps.container_metadata.outputs.labels }}');
        if (jobName === 'build-docker') expect(consumer.with.outputs).toContain('oci-mediatypes=true');
    }
    const dockerfile = fs.readFileSync(path.join(root, component, 'Dockerfile'), 'utf8');
    expect(dockerfile.indexOf('LABEL org.opencontainers.image.title')).toBeGreaterThan(dockerfile.indexOf('FROM alpine:'));
    expect(dockerfile).toContain('org.opencontainers.image.documentation="https://docs.printmaster.work/deployment/docker/"');

    const labels = labelsFrom(metadata(component, '0.32.0-beta.1', 'beta'));
    const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'pm-container-metadata-'));
    try {
        const digests = path.join(directory, 'digests');
        fs.mkdirSync(digests);
        for (const digest of ['ab123', 'cd456']) fs.writeFileSync(path.join(digests, digest), '');
        const tools = path.join(directory, 'tools');
        fs.mkdirSync(tools);
        fs.writeFileSync(path.join(tools, 'jq'), `#!/usr/bin/env node
const fs = require('fs');
const data = JSON.parse(fs.readFileSync(0, 'utf8'));
if (process.argv.at(-1).startsWith('.labels')) {
    console.log(Object.entries(data.labels).map(([key, value]) => key + '=' + value).join('\\n'));
} else {
    console.log(data.tags.map(tag => '-t ' + tag).join(' '));
}
`, { mode: 0o755 });
        const step = workflow.jobs['merge-docker'].steps.find(step => step.name === 'Create manifest list and push');
        const command = step.run.replaceAll('${{ env.REGISTRY }}', 'ghcr.io').replaceAll('${{ env.IMAGE_NAME }}', 'printmaster-org/printmaster');
        const capture = path.join(directory, 'args');
        const fixtureCommands = 'jq() { node "$JQ_FIXTURE" "$@"; }; docker() { printf "%s\\0" "$@" > "$CAPTURE_ARGS"; }; ';
        const result = spawnSync(bash, ['-e', '-c', fixtureCommands + command], {
            cwd: digests, encoding: 'utf8',
            env: {
                ...process.env, JQ_FIXTURE: path.join(tools, 'jq').replaceAll('\\', '/'), CAPTURE_ARGS: capture.replaceAll('\\', '/'),
                DOCKER_METADATA_OUTPUT_JSON: JSON.stringify({ labels, tags: [`ghcr.io/printmaster-org/printmaster-${component}:beta`] }),
            },
        });
        if (result.status !== 0) throw new Error(`Manifest command failed:\n${result.stdout}\n${result.stderr}`);
        const args = fs.readFileSync(capture, 'utf8').split('\0');
        expect(args.slice(0, 3)).toEqual(['buildx', 'imagetools', 'create']);
        for (const [key, value] of Object.entries(labels)) {
            expect(args).toContain(`index:${key}=${value}`);
        }
        expect(args).toContain(`ghcr.io/printmaster-org/printmaster-${component}@sha256:ab123`);
        expect(args).toContain(`ghcr.io/printmaster-org/printmaster-${component}@sha256:cd456`);
        expect(args).not.toContain(`ghcr.io/printmaster-org/printmaster-${component}:latest`);
        fs.unlinkSync(capture);
        const invalid = spawnSync(bash, ['-e', '-c', fixtureCommands + command], {
            cwd: digests, encoding: 'utf8',
            env: {
                ...process.env, JQ_FIXTURE: path.join(tools, 'jq').replaceAll('\\', '/'), CAPTURE_ARGS: capture.replaceAll('\\', '/'),
                DOCKER_METADATA_OUTPUT_JSON: 'invalid JSON',
            },
        });
        expect(invalid.status).not.toBe(0);
        expect(invalid.stderr).not.toBe('');
        expect(fs.existsSync(capture)).toBe(false);
    } finally {
        fs.rmSync(directory, { recursive: true, force: true });
    }
});
