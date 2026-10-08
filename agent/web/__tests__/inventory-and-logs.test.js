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

function response(data, ok = true, status = 200) {
    return { ok, status, json: async () => data };
}

describe('agent inventory and log loading', () => {
    beforeEach(() => {
        document.body.innerHTML = `
            <input id="show_saved_in_discovered" type="checkbox">
            <input id="autosave_checkbox" type="checkbox">
            <input id="show_discovered_devices_anyway" type="checkbox">
            <section id="discovered_section"></section>
            <div id="discovered_devices_cards"><span>previous discovered devices</span></div>
            <div id="saved_devices_cards"><span>previous saved devices</span></div>
            <div id="discovered_stats"></div><div id="saved_stats"></div>
            <div id="logs_load_error" hidden><span id="logs_load_error_message"></span></div>
            <pre id="log"></pre>`;
    });

    function setup(overrides = {}) {
        const context = {
            document,
            window: {
                discoveredPrinters: ['keep'],
                __pm_shared: {
                    error: jest.fn(), warn: jest.fn(), log: jest.fn(),
                    saveDiscoveredDevice: jest.fn().mockResolvedValue(true)
                },
                __pm_shared_cards: {
                    getDeviceTonerBarData: jest.fn(printer => Object.entries(printer.toner_levels || {}).map(([name, level]) => ({ name, level }))),
                    renderDiscoveredCard: jest.fn(printer => '<div>' + printer.name + '</div>'),
                    renderSavedCard: jest.fn(item => '<div class="saved-device-card" data-device-key="' + item.serial + '"></div>')
                },
                __pm_shared_metrics: { loadUsageGraph: jest.fn() }
            },
            fetch: jest.fn(),
            updateSavedDevicesTable: jest.fn(),
            updateManufacturerFilter: jest.fn(),
            allLogEntries: ['old log line'],
            filterAndDisplayLogs: jest.fn(),
            ...overrides
        };
        vm.createContext(context);
        vm.runInContext([
            declaration('fetchInventoryList'),
            declaration('showInventoryLoadError'),
            declaration('hasLowToner'),
            declaration('updatePrinters'),
            declaration('setLogsLoadError'),
            declaration('updateLog')
        ].join('\n'), context);
        return context;
    }

    test('HTTP inventory failures retain existing data and show a retry action', async () => {
        const context = setup();
        context.fetch.mockResolvedValue(response(null, false, 503));

        context.updatePrinters();
        await new Promise(resolve => setTimeout(resolve, 0));

        expect(document.getElementById('discovered_devices_cards').textContent).toContain('previous discovered devices');
        expect(document.getElementById('saved_devices_cards').textContent).toContain('previous saved devices');
        expect(document.querySelectorAll('[data-inventory-load-error] button')).toHaveLength(2);
        expect(document.querySelector('[data-inventory-load-error]').textContent).toContain('503');
    });

    test('discovered and saved summaries use shared finite toner levels with the low-below-20 threshold', async () => {
        const context = setup();
        const discovered = [
            { name: 'low', toner_levels: { Black: 19 } },
            { name: 'boundary', toner_levels: { Black: 20 } },
            { name: 'not numeric', toner_levels: { Black: NaN } }
        ];
        const saved = [{ serial: 'saved-1', printer_info: { toner_levels: { Cyan: 15 } } }];
        context.fetch
            .mockResolvedValueOnce(response(discovered))
            .mockResolvedValueOnce(response(saved))
            .mockResolvedValueOnce(response(saved));

        context.updatePrinters();
        await new Promise(resolve => setTimeout(resolve, 0));

        expect(document.getElementById('discovered_stats').textContent).toContain('Low Toner: 1');
        expect(document.getElementById('saved_stats').textContent).toContain('Low Toner: 1');
        expect(context.window.__pm_shared_cards.getDeviceTonerBarData).toHaveBeenCalled();
    });

    test('failed initial log load shows retry state without replacing cached entries', async () => {
        const context = setup({
            fetch: jest.fn().mockResolvedValue({ ok: false, status: 500, text: async () => 'unavailable' })
        });

        expect(await context.updateLog()).toBe(false);
        expect(context.allLogEntries).toEqual(['old log line']);
        expect(context.filterAndDisplayLogs).not.toHaveBeenCalled();
        expect(document.getElementById('logs_load_error').hidden).toBe(false);
        expect(document.getElementById('logs_load_error_message').textContent).toContain('500');
    });
});
