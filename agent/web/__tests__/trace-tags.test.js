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
        if (lineComment) {
            if (char === '\n') lineComment = false;
            continue;
        }
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

describe('agent trace tag controls', () => {
    let context;

    beforeEach(() => {
        document.body.innerHTML = `
            <button id="trace_tags_refresh_btn">Refresh</button>
            <button id="trace_tags_save_btn" disabled>Save Trace Tags</button>
            <div id="trace_tags_container"><span>Loading...</span></div>`;
        context = {
            document,
            window: { __pm_shared: {
                error: jest.fn(), log: jest.fn(), showToast: jest.fn(),
                showConfirm: jest.fn().mockResolvedValue(true)
            } },
            fetch: jest.fn(),
            traceTagsDirty: false,
            traceTagsLoading: false,
            traceTagsSaving: false,
            TRACE_TAG_CATEGORIES: { Proxy: [{ id: 'proxy_request', label: 'Requests' }] },
            console,
            setTimeout
        };
        vm.createContext(context);
        vm.runInContext([
            declaration('updateTraceTagsSaveButton'),
            declaration('loadTraceTags'),
            declaration('toggleTraceSection'),
            declaration('updateSectionToggle'),
            declaration('saveTraceTags')
        ].join('\n'), context);
    });

    test('renders associated labels and tracks dirty state for tag and section changes', async () => {
        context.fetch.mockResolvedValue({ ok: true, json: async () => ({ tags: { proxy_request: false } }) });
        await context.loadTraceTags();

        expect(document.getElementById('trace_tags_container').textContent).not.toContain('Loading...');
        const tag = document.getElementById('trace_tag_proxy_request');
        expect(document.querySelector('label[for="trace_tag_proxy_request"]').contains(tag)).toBe(true);
        const save = document.getElementById('trace_tags_save_btn');
        expect(save.disabled).toBe(true);

        tag.checked = true;
        tag.dispatchEvent(new Event('change'));
        expect(save.disabled).toBe(false);
        expect(save.textContent).toContain('*');

        document.querySelector('.section-toggle').checked = false;
        document.querySelector('.section-toggle').dispatchEvent(new Event('change'));
        expect(save.disabled).toBe(false);
    });

    test('Refresh and Save issue requests and clear dirty state only after successful save', async () => {
        context.fetch
            .mockResolvedValueOnce({ ok: true, json: async () => ({ tags: { proxy_request: false } }) })
            .mockResolvedValueOnce({ ok: true });
        await context.loadTraceTags();
        const tag = document.getElementById('trace_tag_proxy_request');
        tag.checked = true;
        tag.dispatchEvent(new Event('change'));

        expect(await context.saveTraceTags()).toBe(true);
        expect(context.fetch).toHaveBeenLastCalledWith('/settings/trace_tags', expect.objectContaining({
            method: 'POST',
            body: JSON.stringify({ tags: { proxy_request: true } })
        }));
        expect(document.getElementById('trace_tags_save_btn').disabled).toBe(true);
    });
});
