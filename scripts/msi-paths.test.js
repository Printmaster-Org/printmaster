/** @jest-environment jsdom */
const fs = require('fs');
const path = require('path');

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
});
