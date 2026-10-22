/** @jest-environment jsdom */
const fs = require('fs');
const path = require('path');
const yaml = require('js-yaml');

test('Agent MSI owns the exact protected service executable and adjacent bootstrap files', () => {
    const xml = fs.readFileSync(path.resolve(__dirname, '../build/windows/msi/printmaster-agent.wxs'), 'utf8');
    const document = new DOMParser().parseFromString(xml, 'text/xml');
    expect(document.getElementsByTagName('parsererror')).toHaveLength(0);
    const elements = Array.from(document.getElementsByTagName('*'));
    const byId = id => elements.find(element => element.getAttribute('Id') === id);
    const directoryNames = element => {
        const names = [];
        for (let current = element.parentNode; current?.nodeType === 1; current = current.parentNode) {
            if (current.tagName === 'Directory') names.unshift(current.getAttribute('Name') || current.getAttribute('Id'));
        }
        return names;
    };
    const binary = byId('AgentBinaryFile');
    expect(binary.getAttribute('Name')).toBe('printmaster-agent.exe');
    expect(directoryNames(binary)).toEqual(['SourceDir', 'ProgramFiles64Folder', 'PrintMaster', 'Agent']);
    expect(directoryNames(byId('BootstrapToml'))).toEqual([
        'SourceDir', 'ProgramFiles64Folder', 'PrintMaster', 'Agent', 'config'
    ]);
    expect(byId('AgentBinaryComponent').getAttribute('Guid')).not.toBe('{C1E3E0D2-3E65-4AA7-B7B4-09D0B1E0E0F1}');
    expect(byId('BootstrapFiles').getAttribute('Guid')).not.toBe('{0A98592F-A395-4C9F-BA51-AB305D50B5AA}');
    const registration = byId('AgentServiceInstall');
    expect(registration.getAttribute('Name')).toBe('PrintMasterAgent');
    expect(registration.getAttribute('Account')).toBe('LocalSystem');
    expect(registration.getAttribute('Arguments')).toBe('--service run');
    expect(registration.getAttribute('Start')).toBe('auto');
    expect(registration.parentNode).toBe(binary.parentNode);
    expect(byId('AgentServiceControl').getAttribute('Remove')).toBe('uninstall');
    expect(byId('InstallService')).toBeUndefined();
    expect(byId('UninstallService')).toBeUndefined();
    const cd = yaml.load(fs.readFileSync(path.resolve(__dirname, '../.github/workflows/cd-agent.yml'), 'utf8'));
    const build = cd.jobs['build-agent'].steps.find(step => step.name === 'Build Agent MSI');
    expect(build.run.match(/WixUtilExtension/g)).toHaveLength(2);
});

test('legacy MSI qualification is disposable manual-only, independently pinned and reboot suppressed', () => {
    const read = name => fs.readFileSync(path.resolve(__dirname, '..', name), 'utf8');
    const workflow = yaml.load(read('.github/workflows/legacy-windows-migration.yml'));
    expect(Object.keys(workflow.on)).toEqual(['workflow_dispatch']);
    expect(workflow.jobs.migrate['runs-on']).toBe('windows-latest');
    expect(workflow.jobs.migrate.strategy.matrix.version).toEqual(['0.31.0', '0.31.1']);
    const script = read('scripts/qualify-legacy-windows-migration.ps1');
    expect(script).toContain('RUNNER_ENVIRONMENT');
    expect(script).toContain('github-hosted');
    expect(script).toContain('467515862f9760761e7467e435a73862b1a549f2df18c6e998533b69ad25a6ab');
    expect(script).toContain('447fd61df0f985c0b2e8fde01f545469c30bed77f781c9fcb5bec6d42ff4cc42');
    expect(script).toContain("'InstallServices'");
    expect(script).toContain("'-test.v' '-test.timeout=12m' '-test.run=^TestLiveLegacyWindowsMSIMigration$'");
    const test = read('agent/autoupdate/migration_live_windows_test.go');
    expect(test).toContain('"/norestart", "REBOOT=ReallySuppress"');
    expect(test).toContain('runMSI("/x", "candidate", 0)');
    expect(test).toContain('install("failure", 1603)');
    expect(test).toContain('previous.verify(legacyBinary)');
    expect(test).toContain('checkMSIInstallation(binary)');
    expect(test).toContain('PRAGMA integrity_check');
    expect(test).not.toMatch(/CommandContext|Process\.Kill|Restart-Computer|shutdown\.exe/);
});
