const fs = require('fs');
const path = require('path');
const vm = require('vm');
const { JSDOM } = require('jsdom');

const source = fs.readFileSync(path.join(__dirname, '..', 'app.js'), 'utf8');
const html = fs.readFileSync(path.join(__dirname, '..', 'index.html'), 'utf8');

function functionSource(name) {
    const start = source.search(new RegExp(`^(?:async )?function ${name}\\(`, 'm'));
    const rest = source.slice(start);
    const end = rest.slice(1).search(/^(?:async )?function \w+\(/m);
    return end < 0 ? rest : rest.slice(0, end + 1);
}

function setup(names, globals = {}) {
    const dom = new JSDOM(html);
    dom.window.document.querySelectorAll('template[id^="tab-template-"]').forEach(template => {
        template.replaceWith(template.content);
    });
    const context = vm.createContext({
        document: dom.window.document,
        window: { __pm_shared: { error: jest.fn() } },
        console: { error: jest.fn() },
        TypeError,
        ...globals,
    });
    names.forEach(name => vm.runInContext(functionSource(name), context));
    return { context, document: dom.window.document };
}

describe('alert summary data states', () => {
    test('severity and scope counts are alert counts, not fleet health percentages', async () => {
        const fetch = jest.fn().mockResolvedValue({ ok: true, json: async () => ({
            active_count: 8, critical_count: 2, warning_count: 3, info_count: 3,
            offline_counts: { agents: 4 }, alerts_by_scope: { device: 7, agent: 1 },
        }) });
        const { context, document } = setup(['loadAlertSummary'], {
            fetch, alertSummaryLastUpdated: null, loadRecentAlertsPreview: jest.fn(),
        });
        await context.loadAlertSummary();
        expect(document.getElementById('summary_healthy_count').textContent).toBe('3');
        expect(document.getElementById('summary_offline_count').textContent).toBe('4');
        expect(document.getElementById('scope_count_devices').textContent).toBe('7 alerts');
        expect(document.querySelector('.scope-bar')).toBeNull();
        expect(document.getElementById('summary_no_active_alerts').hidden).toBe(true);
        expect(document.getElementById('alert_summary_status').textContent).toMatch(/Last updated/);
    });

    test('first failure stays unavailable, successful retry shows no alerts, later failure is stale', async () => {
        const fetch = jest.fn().mockRejectedValue(new Error('offline'));
        const { context, document } = setup(['loadAlertSummary'], {
            fetch, alertSummaryLastUpdated: null, loadRecentAlertsPreview: jest.fn(),
        });
        await context.loadAlertSummary();
        expect(document.getElementById('summary_critical_count').textContent).toBe('—');
        expect(document.getElementById('alert_summary_status').textContent).toMatch(/unavailable/);
        const retry = document.getElementById('alert_summary_retry');
        expect(retry.hidden).toBe(false);
        fetch.mockResolvedValueOnce({ ok: true, json: async () => ({ active_count: 0 }) });
        retry.click();
        await new Promise(resolve => setImmediate(resolve));
        expect(document.getElementById('summary_no_active_alerts').hidden).toBe(false);
        expect(retry.hidden).toBe(true);
        await context.loadAlertSummary();
        expect(document.getElementById('alert_summary_status').textContent).toMatch(/stale.*Last updated/);
        expect(retry.hidden).toBe(false);
        expect(context.console.error).toHaveBeenCalledTimes(2);
    });
});

describe('server settings recovery', () => {
    function settingsSetup(fetch) {
        const state = { loading: false, data: { server: {} }, original: {} };
        const result = setup(['loadServerSettings'], {
            fetch, serverSettingsVM: state,
            normalizeServerSettings: data => data, cloneServerSettingsData: data => data,
            renderServerSettingsForm: jest.fn(),
        });
        result.document.getElementById('server_settings_container').innerHTML =
            '<input id="draft" value="unsaved"><button id="server_settings_save_btn">Save</button>';
        return { ...result, state };
    }

    test('5xx retains disabled snapshot and retry, then recovers with fresh lock metadata', async () => {
        const fetch = jest.fn().mockResolvedValue({ ok: false, status: 503 });
        const { context, document } = settingsSetup(fetch);
        const input = document.getElementById('draft');
        await context.loadServerSettings();
        expect(document.getElementById('draft')).toBe(input);
        expect(input.closest('fieldset').disabled).toBe(true);
        expect(document.getElementById('server_settings_load_error').textContent).toMatch(/stale and read-only/);
        fetch.mockResolvedValue({ ok: true, json: async () => ({ locked_keys: ['server.port'] }) });
        document.querySelector('#server_settings_load_error button').click();
        await new Promise(resolve => setImmediate(resolve));
        expect(context.renderServerSettingsForm).toHaveBeenCalledTimes(1);
        expect(context.serverSettingsVM.lockedKeys.has('server.port')).toBe(true);
    });

    test.each([401, 403])('authorization failure %s discards prior settings', async status => {
        const { context, document, state } = settingsSetup(jest.fn().mockResolvedValue({ ok: false, status }));
        await context.loadServerSettings();
        expect(document.getElementById('draft')).toBeNull();
        expect(state.data).toBeNull();
        expect(document.getElementById('server_settings_load_error').textContent).toMatch(/unavailable/);
    });

    test('lock metadata failure does not enable the old form', async () => {
        const fetch = jest.fn().mockResolvedValueOnce({ ok: true, json: async () => ({ server: {} }) })
            .mockResolvedValueOnce({ ok: false, status: 503 });
        const { context, document } = settingsSetup(fetch);
        await context.loadServerSettings();
        expect(context.renderServerSettingsForm).not.toHaveBeenCalled();
        expect(document.getElementById('draft').closest('fieldset').disabled).toBe(true);
    });
});
