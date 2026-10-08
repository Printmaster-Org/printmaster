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

    function settingsContext(managed = []) {
        document.body.innerHTML = fs.readFileSync(path.join(__dirname, '..', 'index.html'), 'utf8');
        const context = {
            document, globalSettings: {}, serverManagedSections: new Set(managed),
            settingsLoadedSuccessfully: true, settingsDirty: false,
            window: { __pm_shared: { error: jest.fn(), log: jest.fn(), showToast: jest.fn() } },
            fetch: jest.fn().mockResolvedValue({ ok: true }),
            setTimeout: jest.fn(), updateSettingsDirtyState: jest.fn(), updateSettingsLoadState: jest.fn(),
            showSettingsSaveError: jest.fn()
        };
        vm.createContext(context);
        vm.runInContext([declaration('readWebPort'), declaration('saveAllSettings'), declaration('applyServerManagedState')].join('\n'), context);
        return context;
    }

    test('Apply omits managed sections but keeps local discovery preferences, logging and web', async () => {
        const context = settingsContext(['discovery', 'snmp', 'features', 'spooler']);
        document.getElementById('show_discover_button_anyway').checked = true;
        await expect(context.saveAllSettings()).resolves.toBe(true);
        const payload = JSON.parse(context.fetch.mock.calls[0][1].body);
        expect(Object.keys(payload).sort()).toEqual(['discovery', 'logging', 'web']);
        expect(payload.discovery).toEqual({ show_discover_button_anyway: true, show_discovered_devices_anyway: false });
    });

    test('local save preserves zero retries, one-minute intervals and seconds overrides', async () => {
        const context = settingsContext();
        document.getElementById('dev_snmp_retries').value = '0';
        document.getElementById('metrics_rescan_interval').value = '1';
        document.getElementById('metrics_rescan_interval_seconds').value = '15';
        await expect(context.saveAllSettings()).resolves.toBe(true);
        const payload = JSON.parse(context.fetch.mock.calls[0][1].body);
        expect(payload.snmp.retries).toBe(0);
        expect(payload.discovery.metrics_rescan_interval_minutes).toBe(1);
        expect(payload.discovery.metrics_rescan_interval_seconds).toBe(15);
    });

    test('managed locks follow section ownership and unlock on disconnect without locking local preferences', () => {
        const context = settingsContext(['features']);
        context.applyServerManagedState();
        expect(document.getElementById('dev_epson_remote_mode').disabled).toBe(true);
        expect(document.getElementById('dev_snmp_version').disabled).toBe(false);
        context.serverManagedSections = new Set(['discovery', 'snmp', 'features', 'spooler']);
        context.applyServerManagedState();
        for (const id of ['dev_snmp_version', 'dev_asset_id_regex', 'dev_discover_concurrency', 'spooler_enabled', 'metrics_rescan_interval_seconds']) {
            expect(document.getElementById(id).disabled).toBe(true);
        }
        for (const id of ['show_discover_button_anyway', 'show_discovered_devices_anyway', 'dev_debug_logging', 'http_port']) {
            expect(document.getElementById(id).disabled).toBe(false);
        }
        context.serverManagedSections = new Set();
        context.applyServerManagedState();
        expect(document.getElementById('dev_snmp_version').disabled).toBe(false);
        expect(document.getElementById('spooler_enabled').disabled).toBe(false);
        expect(document.querySelectorAll('.server-managed-badge')).toHaveLength(0);
    });
});
