const fs = require('fs');
const path = require('path');
const os = require('os');
const { spawnSync } = require('child_process');

const root = path.resolve(__dirname, '..');
const bash = process.platform === 'win32' ? 'C:\\Program Files\\Git\\bin\\bash.exe' : 'bash';

function run(command, args, cwd, env = {}) {
    const result = spawnSync(command, args, { cwd, encoding: 'utf8', timeout: 30000, env: { ...process.env, ...env } });
    if (result.error) throw result.error;
    return result;
}

// Parses GITHUB_OUTPUT text, including heredoc-style multi-line values.
function parseOutputs(text) {
    const outputs = {};
    const lines = text.replace(/\r/g, '').split('\n');
    for (let i = 0; i < lines.length; i++) {
        const heredoc = lines[i].match(/^([a-z_]+)<<(\S+)$/);
        if (heredoc) {
            const body = [];
            for (i++; lines[i] !== heredoc[2]; i++) body.push(lines[i]);
            outputs[heredoc[1]] = body.join('\n');
            continue;
        }
        const pair = lines[i].match(/^([a-z_]+)=(.*)$/);
        if (pair) outputs[pair[1]] = pair[2];
    }
    return outputs;
}

describe('release companion resolution', () => {
    let directory;
    const git = (...args) => {
        const result = run('git', args, directory);
        if (result.status !== 0) throw new Error(result.stderr);
    };
    // Skips the GitHub CLI RPM lookup so tests never reach the network.
    const offline = { RELEASE_COMPANION_OFFLINE: '1' };
    function companion(component, version) {
        const result = run(bash, [path.join(root, 'scripts', 'release-companion.sh'), component, version], directory, offline);
        if (result.status !== 0) throw new Error(`${result.stdout}\n${result.stderr}`);
        return parseOutputs(result.stdout);
    }

    beforeEach(() => {
        directory = fs.mkdtempSync(path.join(os.tmpdir(), 'pm-release-companion-'));
        git('init', '-b', 'main');
        git('config', 'user.email', 'test@example.invalid');
        git('config', 'user.name', 'Release Test');
        git('commit', '--allow-empty', '-m', 'fixture');
        for (const tag of ['agent-v0.31.0', 'agent-v0.31.1', 'agent-v0.31.2-dev.abc1234', 'server-v0.31.1']) git('tag', tag);
    });
    afterEach(() => fs.rmSync(directory, { recursive: true, force: true }));

    test('Stable release links the newest Stable companion with repository installs and MSI', () => {
        const out = companion('agent', '0.31.1');
        expect(out.agent_tag).toBe('agent-v0.31.1');
        expect(out.agent_is_beta).toBe('false');
        expect(out.agent_section).toContain('packages.printmaster.work/install.sh');
        expect(out.agent_section).toContain('printmaster-agent-v0.31.1-windows-amd64.msi');
        expect(out.agent_section).not.toContain('dev.abc1234');
    });

    test('same-version Beta companion wins and omits Stable-only installers', () => {
        git('tag', 'agent-v0.32.0-beta.1');
        const out = companion('agent', '0.32.0-beta.1');
        expect(out.agent_tag).toBe('agent-v0.32.0-beta.1');
        expect(out.agent_is_beta).toBe('true');
        expect(out.agent_section).toContain('**Beta Agent:**');
        expect(out.agent_section).toContain('printmaster-agent-v0.32.0-beta.1-windows-amd64.exe');
        expect(out.agent_section).not.toContain('.msi');
        expect(out.agent_section).not.toContain('install.sh');
    });

    test('Beta without a companion never falls back to another Beta or Dev tag', () => {
        git('tag', 'agent-v0.32.0-beta.0');
        const out = companion('agent', '0.32.0-beta.1');
        expect(out.agent_tag).toBe('');
        expect(out.agent_section).toContain('No PrintMaster Agent release matches v0.32 yet');
    });

    test('Server companion pins the exact image version instead of latest', () => {
        git('tag', 'server-v0.32.0-beta.1');
        const out = companion('server', '0.32.0-beta.1');
        expect(out.server_tag).toBe('server-v0.32.0-beta.1');
        expect(out.server_section).toContain('docker pull ghcr.io/printmaster-org/printmaster-server:0.32.0-beta.1');
        expect(out.server_section).not.toContain(':latest');
    });

    test('rejects unknown companion components', () => {
        const result = run(bash, [path.join(root, 'scripts', 'release-companion.sh'), 'website', '0.31.1'], directory, offline);
        expect(result.status).not.toBe(0);
    });
});

describe.each(['agent', 'server'])('%s release body', component => {
    const workflow = fs.readFileSync(path.join(root, '.github', 'workflows', `cd-${component}.yml`), 'utf8');
    const companion = component === 'agent' ? 'server' : 'agent';
    const body = workflow.slice(workflow.indexOf('body: |'), workflow.indexOf('files: artifacts/printmaster-*'));

    test('uses the shared companion resolver', () => {
        expect(workflow).toContain(`bash scripts/release-companion.sh ${companion} `);
        expect(body).toContain(`\${{ steps.companion.outputs.${companion}_section }}`);
    });

    test('never advertises :latest for a versioned release or unbuilt architectures', () => {
        expect(body).not.toMatch(/printmaster-(server|agent):latest/);
        expect(body).not.toContain('arm/v7');
    });
});
