const fs = require('fs');
const os = require('os');
const path = require('path');
const { loadNycConfig } = require('@istanbuljs/load-nyc-config');
const yaml = require('js-yaml');

test('Go modules, CI and container builds use the patched toolchain consistently', () => {
    const root = path.resolve(__dirname, '..');
    const read = name => fs.readFileSync(path.join(root, name), 'utf8');
    const version = /^go (\S+)$/m.exec(read('common/go.mod'))[1];
    expect(version).toBe('1.27.3');
    for (const module of ['agent', 'server', 'tests']) {
        expect(/^go (\S+)$/m.exec(read(`${module}/go.mod`))[1]).toBe(version);
    }
    const workflows = fs.readdirSync(path.join(root, '.github', 'workflows')).filter(name => name.endsWith('.yml'));
    for (const name of workflows) {
        const workflow = yaml.load(read(`.github/workflows/${name}`));
        if (workflow.env?.GO_VERSION) expect(workflow.env.GO_VERSION).toBe(version);
        for (const job of Object.values(workflow.jobs)) {
            for (const step of job.steps || []) {
                const selected = step.with?.['go-version'];
                if (selected && !selected.startsWith('${{')) expect(selected).toBe(version);
            }
        }
    }
    for (const component of ['agent', 'server']) {
        expect(read(`${component}/Dockerfile`)).toContain(`FROM golang:${version}-alpine AS builder`);
    }
    expect(read('tests/docker-compose.e2e.yml')).toContain(`image: golang:${version}-alpine`);
    const ci = yaml.load(read('.github/workflows/ci.yml'));
    const scan = ci.jobs.go.steps.find(step => step.name === 'Check reachable Go vulnerabilities');
    expect(scan.run).toContain('govulncheck@v1.9.0');
    expect(scan.run).toContain('govulncheck ./...');
    expect(scan.env.TEST_MODULE).toBe('${{ matrix.module }}');
    expect(scan['continue-on-error']).toBeUndefined();
});

test('coverage YAML loads aliases and extended configuration without the legacy formatter dependency', async () => {
    const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'pm-coverage-config-'));
    try {
        fs.writeFileSync(path.join(directory, 'package.json'), '{"private":true}');
        fs.writeFileSync(path.join(directory, 'base.yaml'), 'all: true\ncheck-coverage: true\n');
        fs.writeFileSync(path.join(directory, '.nycrc.yaml'),
            'extends: ./base.yaml\ninclude: &files\n  - common/web/**/*.js\nexclude: *files\n');
        const config = await loadNycConfig({ cwd: directory });
        expect(config.all).toBe(true);
        expect(config.checkCoverage).toBe(true);
        expect(config.include).toEqual(['common/web/**/*.js']);
        expect(config.exclude).toEqual(config.include);
    } finally {
        fs.rmSync(directory, { recursive: true, force: true });
    }
    const lock = JSON.parse(fs.readFileSync(path.join(__dirname, '..', 'package-lock.json'), 'utf8'));
    expect(Object.keys(lock.packages).filter(name => name.endsWith('/sprintf-js'))).toEqual([]);
});

test.each(['include: [unterminated', 'value: !!js/function "function () {}"'])(
    'coverage loader explicitly rejects invalid or executable YAML: %s', async yaml => {
        const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'pm-coverage-invalid-'));
        try {
            fs.writeFileSync(path.join(directory, 'package.json'), '{"private":true}');
            fs.writeFileSync(path.join(directory, '.nycrc.yaml'), yaml);
            await expect(loadNycConfig({ cwd: directory })).rejects.toThrow();
        } finally {
            fs.rmSync(directory, { recursive: true, force: true });
        }
    });
