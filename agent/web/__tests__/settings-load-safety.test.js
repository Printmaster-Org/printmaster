/** @jest-environment jsdom */

const fs = require('fs');
const path = require('path');
const vm = require('vm');

const appSource = fs.readFileSync(path.join(__dirname, '..', 'app.js'), 'utf8');

function declaration(name) {
    const start = appSource.search(new RegExp('(?:async )?function ' + name + '\\s*\\('));
    if (start < 0) throw new Error('Missing function ' + name);
    const signatureOpen = appSource.indexOf('(', start);
    let signatureDepth = 0;
    let signatureQuote = null;
    let signatureClose = -1;
    for (let i = signatureOpen; i < appSource.length; i++) {
        const char = appSource[i];
        if (signatureQuote) {
            if (char === '\\') { i++; continue; }
            if (char === signatureQuote) signatureQuote = null;
            continue;
        }
        if (char === "'" || char === '"' || char === '`') { signatureQuote = char; continue; }
        if (char === '(') signatureDepth++;
        if (char === ')' && --signatureDepth === 0) { signatureClose = i; break; }
    }
    const open = appSource.indexOf('{', signatureClose + 1);
    let depth = 0;
    let quote = null;
    let lineComment = false;
    let blockComment = false;
    for (let i = open; i < appSource.length; i++) {
        const char = appSource[i];
        const next = appSource[i + 1];
        if (lineComment) { if (char === '\n') lineComment = false; continue; }
        if (blockComment) {
            if (char === '*' && next === '/') { blockComment = false; i++; }
            continue;
        }
        if (quote) {
            if (char === '\\') { i++; continue; }
            if (char === quote) quote = null;
            continue;
        }
        if (char === '/' && next === '/') { lineComment = true; i++; continue; }
        if (char === '/' && next === '*') { blockComment = true; i++; continue; }
        if (char === "'" || char === '"' || char === '`') { quote = char; continue; }
        if (char === '{') depth++;
        if (char === '}' && --depth === 0) return appSource.slice(start, i + 1);
    }
    throw new Error('Unclosed function ' + name);
}

describe('agent settings load safety', () => {
    beforeEach(() => {
        document.body.innerHTML = `
            <button id="settings_apply_btn" disabled>Apply</button>
            <button id="settings_reset_btn" disabled>Reset</button>
            <div id="settings_load_error" hidden><span id="settings_load_error_message"></span></div>
            <div id="settings_save_error" hidden></div>`;
    });

    test('a failed settings request leaves controls untouched and shows inline retry guidance', async () => {
        const context = {
            document,
            window: { __pm_shared: { error: jest.fn() } },
            fetch: jest.fn().mockResolvedValue({ ok: false, status: 503, text: async () => 'unavailable' }),
            settingsLoadedSuccessfully: false,
            updateSettingsLoadState: undefined
        };
        vm.createContext(context);
        vm.runInContext([
            declaration('updateSettingsLoadState'),
            declaration('loadSettings')
        ].join('\n'), context);

        await expect(context.loadSettings()).rejects.toThrow('Request failed (503)');
        expect(document.getElementById('settings_load_error').hidden).toBe(false);
        expect(document.getElementById('settings_load_error_message').textContent).toContain('Apply and auto-save are paused');
        expect(document.getElementById('settings_apply_btn').disabled).toBe(true);
        expect(document.getElementById('settings_reset_btn').disabled).toBe(true);
    });

    test('Apply and autosave cannot POST defaults before a successful load', async () => {
        const context = {
            document,
            window: { __pm_shared: { error: jest.fn() } },
            fetch: jest.fn(),
            settingsLoadedSuccessfully: false,
            updateSettingsLoadState: jest.fn()
        };
        vm.createContext(context);
        vm.runInContext(declaration('saveAllSettings'), context);

        await expect(context.saveAllSettings()).resolves.toBe(false);
        expect(context.fetch).not.toHaveBeenCalled();
        expect(context.updateSettingsLoadState).toHaveBeenCalled();
    });

    test('web port values must be integers within the valid port range', () => {
        document.body.innerHTML += '<input id="http_port" value="8080">';
        const context = { document };
        vm.createContext(context);
        vm.runInContext(declaration('readWebPort'), context);

        expect(context.readWebPort('http_port', 'HTTP')).toBe(8080);
        document.getElementById('http_port').value = '65536';
        expect(() => context.readWebPort('http_port', 'HTTP')).toThrow('HTTP port must be an integer');
        document.getElementById('http_port').value = '8.5';
        expect(() => context.readWebPort('http_port', 'HTTP')).toThrow('HTTP port must be an integer');
    });
});
