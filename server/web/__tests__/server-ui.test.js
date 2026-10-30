const fs = require('fs');
const path = require('path');
const vm = require('vm');
const { JSDOM } = require('jsdom');

const source = fs.readFileSync(path.join(__dirname, '..', 'app.js'), 'utf8');
const html = fs.readFileSync(path.join(__dirname, '..', 'index.html'), 'utf8');
const shared = fs.readFileSync(path.join(__dirname, '..', '..', '..', 'common', 'web', 'shared.js'), 'utf8');
const progressHelper = shared.slice(shared.indexOf('function updateProgressView('), shared.indexOf('window.__pm_shared.updateProgressView'));

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
        console: { error: jest.fn(), warn: jest.fn() },
        TypeError,
        Date, setTimeout, clearTimeout, AbortSignal,
        ...globals,
    });
    vm.runInContext(progressHelper + '; window.__pm_shared ||= {}; window.__pm_shared.updateProgressView = updateProgressView;', context);
    names.forEach(name => vm.runInContext(functionSource(name), context));
    return { context, document: dom.window.document };
}

describe('agent lifecycle status filters', () => {
    test('deactivation and pending deployment take precedence over connection state', () => {
        const { context } = setup(['enrichSingleAgent'], {
            AGENT_STATUS_LABELS: { active: 'Active', degraded: 'Degraded', offline: 'Offline', deactivated: 'Deactivated', pending: 'Pending setup' },
            normalizeAgentStatusFromConnection: () => 'active',
            formatRelativeTime: () => 'just now',
            tenantDisplayNameById: () => '',
            getAgentPlatformLabel: () => 'Windows',
            buildAgentSearchBlob: () => '',
        });
        const deactivated = context.enrichSingleAgent({ agent_id: 'old-agent', status: 'deactivated', connection_type: 'ws' });
        const pending = context.enrichSingleAgent({ agent_id: 'pending-deployment:id', status: 'expired', pending_deployment: true, connection_type: 'none' });
        expect(deactivated.__meta.statusKey).toBe('deactivated');
        expect(pending.__meta.statusKey).toBe('pending');
        expect(pending.__meta.statusLabel).toBe('Invite expired');
    });

    test('deactivated and pending rows can be independently filtered out', () => {
        const { context } = setup(['matchesAgentFilters']);
        const deactivated = { status: 'deactivated', __meta: { statusKey: 'deactivated', tenantId: '' } };
        const pending = { status: 'expired', pending_deployment: true, __meta: { statusKey: 'pending', tenantId: '' } };
        expect(context.matchesAgentFilters(deactivated, { statuses: new Set(['active', 'offline']) })).toBe(false);
        expect(context.matchesAgentFilters(deactivated, { statuses: new Set(['deactivated']) })).toBe(true);
        expect(context.matchesAgentFilters(pending, { statuses: new Set(['active', 'deactivated']) })).toBe(false);
        expect(context.matchesAgentFilters(pending, { statuses: new Set(['pending']) })).toBe(true);
    });
});

describe('legacy Agent bridge advisory', () => {
    function renderer() {
        return setup(['renderLegacyAgentUpdateBadge', 'renderAgentVersionCell'], {
            escapeHtml: value => String(value).replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;').replaceAll('"', '&quot;'),
            agentsVM: { latestVersion: '', updateState: {} },
        }).context;
    }

    test.each(['0.9.9', '0.30.0', '0.30.5', '0.31.1', 'v0.31.1', '0.31.2-beta.1', '0.30.5+build'])('warns for %s in table and cards', version => {
        const context = renderer();
        for (const forTable of [true, false]) {
            const markup = context.renderAgentVersionCell({ version }, forTable);
            expect(markup).toContain('Legacy updater</span>');
            expect(markup).toContain('Update this Agent to 0.32.x before upgrading the Server to 0.33.0 Stable.');
            expect(markup).toContain('Legacy updater support ends in 0.33.0 Stable.');
            expect(markup).toContain('aria-label=');
        }
    });

    test.each(['', 'N/A', 'unknown', '0.31', '0.31.1<script>', '0.32.0-beta.3', '0.32.0', '0.33.0', '1.0.0'])('does not label %s as legacy', version => {
        expect(renderer().renderLegacyAgentUpdateBadge({ version })).toBe('');
    });

    test('excludes invitations and clears after the Agent reports 0.32', () => {
        const context = renderer();
        const agent = { version: '0.31.1', pending_deployment: true };
        expect(context.renderLegacyAgentUpdateBadge(agent)).toBe('');
        agent.pending_deployment = false;
        expect(context.renderLegacyAgentUpdateBadge(agent)).toContain('Legacy updater');
        agent.version = '0.32.0';
        expect(context.renderLegacyAgentUpdateBadge(agent)).toBe('');
    });

    test('keeps warning during an update', () => {
        const context = renderer();
        context.agentsVM.updateState.legacy = { status: 'downloading', progress: 25 };
        expect(context.renderAgentVersionCell({ agent_id: 'legacy', version: '0.31.1' }, true)).toContain('Legacy updater');
    });
});

describe('bootstrap installation guidance', () => {
    function render(platform, clipboard) {
        const oneLiner = 'irm https://example.invalid/install/fixture | iex';
        const { context, document } = setup(['renderAddAgentStep', 'copyAddAgentInstructionsLink'], {
            navigator: { clipboard },
            escapeHtml: value => String(value).replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;').replaceAll('"', '&quot;'),
            window: { _addAgentState: { mode: 'script', platform, script: '<script>fixture</script>', oneLiner, scriptDownloadURL: 'https://example.invalid/install/fixture' } },
        });
        const selection = { removeAllRanges: jest.fn(), addRange: jest.fn(), toString: jest.fn(() => oneLiner) };
        const range = { selectNodeContents: jest.fn() };
        context.window.getSelection = jest.fn(() => selection);
        document.createRange = jest.fn(() => range);
        context.renderAddAgentStep(3);
        return { context, document, oneLiner, selection, range };
    }

    test('Windows notice, escaped disclosure and exact command copy', async () => {
        const writeText = jest.fn().mockResolvedValue(undefined);
        const { document, oneLiner } = render('windows', { writeText });
        expect(document.getElementById('add_agent_content').textContent).toContain('trust the Server');
        expect(document.getElementById('add_agent_content').textContent).toContain('Initial enrollment trusts this Server');
        expect(document.getElementById('add_agent_content').textContent).toContain('without an existing Agent');
        const details = document.querySelector('details#add_agent_full_script');
        expect(details.open).toBe(false);
        expect(details.querySelector('script')).toBeNull();
        expect(details.querySelector('pre').textContent).toBe('<script>fixture</script>');
        const instructions = document.querySelector('#add_agent_content a[href="https://example.invalid/install/fixture/instructions"]');
        expect(instructions.textContent).toBe('Open instructions');
        expect(instructions.getAttribute('rel')).toBe('noopener noreferrer');
        expect(document.getElementById('add_agent_copy').textContent).toBe('Copy install command');
        expect(document.getElementById('add_agent_copy_instructions').textContent).toBe('Copy instructions link');
        const copyInstructions = document.getElementById('add_agent_copy_instructions');
        copyInstructions.click();
        await new Promise(resolve => setTimeout(resolve, 0));
        expect(writeText).toHaveBeenCalledWith('https://example.invalid/install/fixture/instructions');
        document.getElementById('add_agent_copy').click();
        await new Promise(resolve => setTimeout(resolve, 0));
        expect(writeText).toHaveBeenCalledWith(oneLiner);
        expect(document.getElementById('add_agent_copy_status').textContent).toBe('Copied to clipboard.');
    });

    test('email result offers the same browser instructions link for copying', async () => {
        const writeText = jest.fn().mockResolvedValue(undefined);
        const instructionsURL = 'https://example.invalid/install/mail-code/instructions';
        const { context, document } = setup(['renderAddAgentStep', 'copyAddAgentInstructionsLink'], {
            navigator: { clipboard: { writeText } },
            escapeHtml: value => String(value),
            window: { _addAgentState: { mode: 'email', emailSent: true, emailTo: 'operator@example.com', instructionsURL } },
        });
        context.renderAddAgentStep(3);
        expect(document.getElementById('add_agent_primary').textContent).toBe('Done');
        expect(document.getElementById('add_agent_primary').classList.contains('modal-button-secondary')).toBe(true);
        document.getElementById('add_agent_copy_instructions').click();
        await new Promise(resolve => setTimeout(resolve, 0));
        expect(writeText).toHaveBeenCalledWith(instructionsURL);
    });

    test.each([undefined, { writeText: () => Promise.reject(new Error('denied')) }])('clipboard failure selects the command without exposing errors', async clipboard => {
        const { context, document, oneLiner } = render('linux', clipboard);
        expect(document.getElementById('add_agent_content').textContent).toContain('uses sudo');
        expect(document.getElementById('add_agent_content').textContent).not.toContain('Administrator PowerShell');
        document.getElementById('add_agent_copy').click();
        await new Promise(resolve => setTimeout(resolve, 0));
        expect(context.document.createRange).toHaveBeenCalledTimes(1);
        expect(context.window.getSelection).toHaveBeenCalledTimes(1);
        expect(document.getElementById('add_agent_copy_status').textContent).toContain('manual copying');
        expect(context.window.getSelection().toString()).toBe(oneLiner);
    });

    test('TLS bypass is an explicit opt-in carried into script generation', async () => {
        const fetch = jest.fn().mockResolvedValue({
            ok: true,
            headers: { get: () => 'application/json' },
            json: async () => ({ script: 'script', filename: 'install.ps1', download_url: 'https://example.invalid/install/code', one_liner: 'irm example | iex' }),
        });
        const state = { step: 2, mode: 'script', tenantID: 'tenant-1', platform: 'windows', ttl: 60, insecureSkipVerify: false };
        const { context, document } = setup(['renderAddAgentStep', 'handleAddAgentPrimary'], {
            fetch,
            window: { _addAgentState: state, __pm_shared: { showAlert: jest.fn() } },
            escapeHtml: value => String(value).replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;').replaceAll('"', '&quot;'),
        });
        context.renderAddAgentStep(2);
        const toggle = document.getElementById('add_agent_skip_tls');
        expect(toggle.checked).toBe(false);
        expect(document.getElementById('add_agent_content').textContent).toContain('self-signed Server certificate');
        expect(document.getElementById('add_agent_primary').textContent).toBe('Generate script');
        toggle.checked = true;
        await context.handleAddAgentPrimary();
        expect(fetch).toHaveBeenCalledTimes(1);
        expect(JSON.parse(fetch.mock.calls[0][1].body)).toMatchObject({ tenant_id: 'tenant-1', platform: 'windows', insecure_skip_verify: true });
        expect(document.getElementById('add_agent_content').textContent).toContain('TLS verification is disabled');
    });

    test('new onboarding defaults to a bootstrap script', () => {
        const { context } = setup(['openAddAgentModal', 'renderAddAgentStep'], {
            addAgentUIInitialized: false,
            populateTenantDropdown: jest.fn(),
            escapeHtml: value => String(value),
            window: { __pm_shared: {} },
        });
        context.openAddAgentModal({});
        expect(context.window._addAgentState.mode).toBe('script');
        expect(context.document.querySelector('input[name="add_agent_action"][value="script"]').checked).toBe(true);
    });

    test('Back then Continue returns to the review step instead of generating the script', async () => {
        const state = { step: 1, mode: 'script', tenantID: 'tenant-1', platform: 'windows', ttl: 60, one_time: true };
        const { context, document } = setup(['initAddAgentUI', 'openAddAgentModal', 'closeAddAgentModal', 'renderAddAgentStep', 'handleAddAgentPrimary'], {
            addAgentUIInitialized: false,
            populateTenantDropdown: (select, options) => {
                select.innerHTML = '<option value="tenant-1">Tenant one</option>';
                select.value = options.selectedId || 'tenant-1';
            },
            escapeHtml: value => String(value),
            fetch: jest.fn(),
            window: { _addAgentState: state, __pm_shared: { showAlert: jest.fn() } },
        });
        context.initAddAgentUI();
        context.openAddAgentModal({ tenantID: 'tenant-1' });
        await context.handleAddAgentPrimary();
        expect(context.window._addAgentState.step).toBe(2);
        expect(document.getElementById('add_agent_back').parentElement.id).toBe('add_agent_footer');
        document.getElementById('add_agent_back').click();
        expect(context.window._addAgentState.step).toBe(1);
        await context.handleAddAgentPrimary();
        expect(context.window._addAgentState.step).toBe(2);
        expect(document.getElementById('add_agent_step_indicator').textContent).toContain('Connection security');
        expect(context.fetch).not.toHaveBeenCalled();
    });
});

describe('ordered agent update lifecycle', () => {
    let context, agentsVM, toast;
    const event = (sequence, status, progress, run = 'one', started = '2026-10-16T18:00:00.000Z') => ({
        agent_id: 'agent-1', run_id: run, run_started_at: started, sequence, status, progress,
        target_version: '1.1.0', message: status, updated_at: new Date().toISOString(),
    });
    beforeEach(() => {
        jest.useFakeTimers();
        agentsVM = { items: [{ agent_id: 'agent-1', name: 'Remote office', version: '1.0.0' }], updateState: {} };
        toast = jest.fn();
        ({ context } = setup(['handleAgentUpdateProgress', 'scheduleAgentUpdateObservation', 'handleAgentReconnectAfterUpdate'], {
            agentsVM, setTimeout, window: { __pm_shared: { showToast: toast } },
            refreshAgentVersionCell: jest.fn(),
            loadAgents: jest.fn(), fetchSingleAgent: jest.fn().mockResolvedValue({ version: '1.1.0' }),
        }));
    });
    afterEach(() => jest.useRealTimers());

    test('completion cannot regress, replay cannot notify twice, and old runs cannot overwrite new ones', () => {
        context.handleAgentUpdateProgress(event(2, 'restarting', 80));
        context.handleAgentUpdateProgress(event(4, 'succeeded', 100));
        context.handleAgentUpdateProgress(event(3, 'restarting', 80));
        context.handleAgentUpdateProgress(event(5, 'restarting', 80));
        context.handleAgentUpdateProgress(event(4, 'succeeded', 100));
        expect(agentsVM.updateState['agent-1'].status).toBe('succeeded');
        expect(toast.mock.calls.filter(([message]) => message.includes('complete'))).toHaveLength(1);
        context.handleAgentUpdateProgress(event(1, 'downloading', 10, 'two', '2026-10-16T18:01:00.000Z'));
        context.handleAgentUpdateProgress(event(100, 'succeeded', 100));
        jest.advanceTimersByTime(5000);
        expect(agentsVM.updateState['agent-1'].runId).toBe('two');
        expect(agentsVM.updateState['agent-1'].status).toBe('downloading');
    });

    test('a persisted snapshot restores silently and uses the agent percentage without rescaling', () => {
        context.handleAgentUpdateProgress(event(8, 'downloading', 45), { silent: true });
        expect(toast).not.toHaveBeenCalled();
        const state = agentsVM.updateState['agent-1'];
        expect(context.window.__pm_shared.updateProgressView(state.snapshot, state.timestamp).progress).toBe(45);
        context.handleAgentUpdateProgress(event(7, 'downloading', 30));
        expect(agentsVM.updateState['agent-1'].progress).toBe(45);
        context.handleAgentUpdateProgress({ agent_id: 'agent-1', status: 'failed', error: 'legacy error' });
        expect(agentsVM.updateState['agent-1'].status).toBe('downloading');
    });

    test('a reconnect and a UI timeout never manufacture completion or clear an unresolved run', async () => {
        context.handleAgentUpdateProgress(event(2, 'restarting', 80));
        await context.handleAgentReconnectAfterUpdate('agent-1', agentsVM.updateState['agent-1']);
        expect(agentsVM.updateState['agent-1'].status).toBe('restarting');
        jest.advanceTimersByTime(120000);
        const state = agentsVM.updateState['agent-1'];
        expect(context.window.__pm_shared.updateProgressView(state.snapshot, state.timestamp).message).toContain('outcome is not yet known');
        expect(toast.mock.calls.some(([message]) => message.includes('complete'))).toBe(false);
    });

    test('replayed progress does not extend the observation deadline and terminal errors remain visible', () => {
        const data = event(1, 'applying', 68);
        context.handleAgentUpdateProgress(data, { silent: true });
        jest.advanceTimersByTime(119999);
        context.handleAgentUpdateProgress(data, { silent: true });
        expect(context.fetchSingleAgent).not.toHaveBeenCalled();
        jest.advanceTimersByTime(1);
        expect(context.fetchSingleAgent).toHaveBeenCalledTimes(1);
        const failure = { ...event(2, 'failed', 68), error: 'Owner repair required; preserve protected journal' };
        context.handleAgentUpdateProgress(failure);
        jest.advanceTimersByTime(600000);
        expect(agentsVM.updateState['agent-1'].error).toBe(failure.error);
    });

    test.each([true, false])('late command response (%s) cannot overwrite real progress', async success => {
        let resolve;
        const fetch = jest.fn(() => new Promise(done => { resolve = done; }));
        const { context: command } = setup(['updateAgent', 'handleAgentUpdateProgress', 'scheduleAgentUpdateObservation'], {
            agentsVM, fetch, refreshAgentVersionCell: jest.fn(), fetchSingleAgent: jest.fn(),
            window: { __pm_shared: { showToast: toast, error: jest.fn() } },
        });
        const pending = command.updateAgent('agent-1');
        expect(agentsVM.updateState['agent-1'].status).toBe('dispatched');
        command.handleAgentUpdateProgress(event(1, 'downloading', 45));
        resolve({ ok: true, json: async () => ({ success, message: 'command response' }) });
        await pending;
        expect(agentsVM.updateState['agent-1'].status).toBe('downloading');
        expect(agentsVM.updateState['agent-1'].progress).toBe(45);
    });

    test('network loss is ambiguous, blocks duplicate dispatch, and never pretends installation failed', async () => {
        const fetch = jest.fn().mockRejectedValue(new Error('connection lost'));
        const { context: command } = setup(['updateAgent', 'scheduleAgentUpdateObservation'], {
            agentsVM, fetch, refreshAgentVersionCell: jest.fn(), fetchSingleAgent: jest.fn(),
            window: { __pm_shared: { showToast: toast, error: jest.fn() } },
        });
        await command.updateAgent('agent-1');
        await command.updateAgent('agent-1');
        expect(fetch).toHaveBeenCalledTimes(1);
        jest.advanceTimersByTime(120000);
        const state = agentsVM.updateState['agent-1'];
        const view = command.window.__pm_shared.updateProgressView(state, state.timestamp);
        expect(view.overdue).toBe(true);
        expect(view.failed).toBe(false);
        expect(view.message).toContain('connection lost');
    });

    test('version cells keep final check/errors visible without hiding later available updates', () => {
        const { context: render, document } = setup(['renderLegacyAgentUpdateBadge', 'renderAgentVersionCell'], {
            agentsVM, compareVersions: () => 1,
            escapeHtml: value => String(value).replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('"', '&quot;'),
        });
        const cell = document.createElement('div');
        agentsVM.latestVersion = '1.2.0';
        context.handleAgentUpdateProgress({ ...event(1, 'idle', -1), message: 'No applicable update in the selected channel' }, { silent: true });
        cell.innerHTML = render.renderAgentVersionCell(agentsVM.items[0], true);
        expect(cell.textContent).toContain('No applicable update');
        expect(cell.querySelector('[data-action="update-agent"]')).not.toBeNull();
        context.handleAgentUpdateProgress({
            ...event(1, 'failed', -1, 'two', '2026-10-16T18:01:00Z'),
            error: 'Owner repair required <preserve journal>',
        }, { silent: true });
        cell.innerHTML = render.renderAgentVersionCell(agentsVM.items[0], true);
        expect(cell.textContent).toContain('Owner repair required <preserve journal>');
        expect(cell.querySelector('.update-outcome').getAttribute('role')).toBe('status');
        expect(cell.querySelector('preserve')).toBeNull();
        expect(cell.querySelector('[data-action="update-agent"]')).not.toBeNull();
    });
});

describe('replayable upstream release sync', () => {
    const event = (sequence, phase, extra = {}) => ({
        run_id: 'sync-one', run_started_at: new Date().toISOString(),
        updated_at: new Date().toISOString(), deadline: new Date(Date.now() + 3600000).toISOString(),
        sequence, phase, message: phase, ...extra,
    });
    let context, document, toast;
    beforeEach(() => {
        jest.useFakeTimers();
        toast = jest.fn();
        ({ context, document } = setup([
            'handleReleaseSyncProgress', 'renderReleaseSyncProgress', 'scheduleReleaseSyncRefresh',
            'refreshReleaseSyncStatus', 'triggerReleasesSync', 'formatBytes',
        ], {
            releaseSyncProgress: null, releaseSyncSnapshot: null, releaseSyncTimer: null,
            releaseSyncReceivedAt: 0, releasesSyncPending: false,
            showToast: toast, escapeHtml: value => String(value),
            loadReleaseArtifacts: jest.fn(), loadSelfUpdateRuns: jest.fn(),
            window: { __pm_shared: { warn: jest.fn() } },
            fetch: jest.fn().mockResolvedValue({ ok: true, json: async () => ({ progress: null }) }),
        }));
    });
    afterEach(() => jest.useRealTimers());

    test('reload restores errors silently and terminal replay cannot regress or toast twice', async () => {
        const failure = event(4, 'error', { error: 'Upstream metadata timed out; cache is incomplete' });
        context.fetch.mockResolvedValue({ ok: true, json: async () => ({ progress: failure }) });
        await context.refreshReleaseSyncStatus();
        context.handleReleaseSyncProgress(event(3, 'downloading'));
        context.handleReleaseSyncProgress(event(5, 'complete'));
        expect(document.getElementById('releases_sync_progress').textContent).toContain(failure.error);
        expect(context.releasesSyncPending).toBe(false);
        expect(toast).not.toHaveBeenCalled();
    });

    test('stale sync is unknown, not complete; polling stops after the advertised deadline allowance', () => {
        const baselineTimers = jest.getTimerCount();
        context.handleReleaseSyncProgress(event(1, 'fetching'), { silent: true });
        jest.setSystemTime(Date.now() + 120000);
        context.renderReleaseSyncProgress();
        expect(document.getElementById('releases_sync_progress').textContent).toContain('outcome is not yet known');
        expect(document.getElementById('releases_sync_btn').disabled).toBe(true);
        expect(context.releasesSyncPending).toBe(true);
        jest.setSystemTime(Date.now() + 3600001);
        context.scheduleReleaseSyncRefresh();
        expect(jest.getTimerCount()).toBe(baselineTimers);
    });

    test('late successful dispatch cannot overwrite a final error', async () => {
        let resolve;
        context.fetch.mockImplementation(() => new Promise(done => { resolve = done; }));
        const pending = context.triggerReleasesSync();
        const initial = event(1, 'fetching');
        context.handleReleaseSyncProgress({ ...initial, sequence: 2, phase: 'error', error: 'Download verification failed' });
        resolve({ ok: true, json: async () => ({ success: true, progress: initial }) });
        await pending;
        expect(document.getElementById('releases_sync_progress').textContent).toContain('Download verification failed');
        expect(context.releaseSyncProgress.phase).toBe('error');
        expect(toast).toHaveBeenCalledTimes(1);
    });

    test('lost command response is unresolved and cannot immediately dispatch a duplicate', async () => {
        context.fetch.mockRejectedValue(new Error('network lost'));
        await context.triggerReleasesSync();
        await context.triggerReleasesSync();
        expect(context.fetch).toHaveBeenCalledTimes(1);
        expect(context.releaseSyncProgress.phase).toBe('fetching');
        expect(context.releasesSyncPending).toBe(true);
        expect(document.getElementById('releases_sync_progress').textContent).toContain('could not be confirmed');
    });
});

describe('Server session monitoring', () => {
    function sessionSetup(status) {
        const replace = jest.fn();
        const fetch = jest.fn().mockResolvedValue({ status, ok: status === 200 });
        const auth = { currentUser: { username: 'admin' }, redirectPending: false };
        const { context, document } = setup(['monitorServerSession'], {
            Request, URL,
            window: {
                fetch, __pm_auth: auth,
                location: {
                    href: 'https://printmaster.test/?tenant=office#devices',
                    origin: 'https://printmaster.test', pathname: '/',
                    search: '?tenant=office', hash: '#devices', replace,
                },
            },
        });
        document.documentElement.removeAttribute('data-session-pending');
        context.monitorServerSession();
        return { context, document, fetch, auth, replace };
    }

    test.each([
        '/api/v1/auth/me', '/api/v1/devices/index', '/api/v1/devices/rows',
        '/api/v1/devices/metrics/query', '/api/v1/agents/list', '/api/v1/metrics/summary',
    ])('%s redirects before the caller handles a 401', async path => {
        const { context, document, auth, replace } = sessionSetup(401);
        await context.window.fetch(path);
        expect(document.documentElement.hasAttribute('data-session-pending')).toBe(true);
        expect(auth.currentUser).toBeNull();
        expect(replace).toHaveBeenCalledWith('/login?redirect=%2F%3Ftenant%3Doffice%23devices');
        await context.window.fetch(path);
        expect(replace).toHaveBeenCalledTimes(1);
    });

    test.each([200, 403, 500, 503])('HTTP %s is not a session failure', async status => {
        const { context, document, replace } = sessionSetup(status);
        const response = await context.window.fetch('/api/v1/devices/index');
        expect(response.status).toBe(status);
        expect(document.documentElement.hasAttribute('data-session-pending')).toBe(false);
        expect(replace).not.toHaveBeenCalled();
    });

    test.each([
        '/api/v1/proxy/agent/site-1/devices', '/api/v1/proxy/device/printer-1',
        '/api/v1/auth/login', '/api/v1/auth/logout', '/api/v1/auth/options',
        'https://other.test/api/v1/devices/index',
    ])('%s does not invalidate the Server session on 401', async path => {
        const { context, replace } = sessionSetup(401);
        await context.window.fetch(path);
        expect(replace).not.toHaveBeenCalled();
    });

    test('preserves Request and fetch options, and propagates network errors', async () => {
        const { context, fetch, replace } = sessionSetup(200);
        const request = new Request('https://printmaster.test/api/v1/devices/rows');
        const options = { method: 'POST', body: '{"serials":["one"]}' };
        await context.window.fetch(request, options);
        expect(fetch).toHaveBeenCalledWith(request, options);
        fetch.mockRejectedValueOnce(new TypeError('Network unavailable'));
        await expect(context.window.fetch('/api/v1/devices/index')).rejects.toThrow('Network unavailable');
        expect(replace).not.toHaveBeenCalled();
    });
});

describe('device status tag filtering', () => {
    const keys = ['healthy', 'warning', 'error', 'jam', 'offline', 'unknown'];
    const escapeHtml = value => String(value);

    function statusSetup(view = 'table') {
        const devicesVM = {
            loaded: true, uiInitialized: false, view,
            filters: {
                statuses: new Set(), consumables: new Set(['high']), query: '',
                agentId: '', tenantId: '', manufacturer: '', sortKey: 'last_seen', sortDir: 'desc',
            },
            items: keys.slice(0, -1).map(code => ({
                serial: code, agent_id: 'agent-1', manufacturer: 'Epson',
                __meta: { status: { code }, search: `${code} epson`, tenantId: 'tenant-1' },
            })),
            stats: {}, render: { displayed: 0, pageSize: 30 },
        };
        const { context, document } = setup([
            'initDevicesUI', 'applyDeviceFilters', 'matchesDeviceFilters', 'createStatusCountMap',
            'toggleStatusFilter', 'renderDevicesStats', 'renderDevicesActiveFilters',
            'buildFilterChip', 'handleFilterChipRemove', 'syncDeviceQuickFilters', 'resetDeviceFilters',
        ], {
            devicesVM, DEVICE_STATUS_KEYS: keys, DEVICE_CONSUMABLE_KEYS: ['high'],
            devicesLoader: null, formatNumber: String, escapeHtml,
            hasDeviceSupplyFilter: () => false, sortDevices: list => list,
            initDevicesTableCustomizer: jest.fn(), initFilterDrawer: jest.fn(), debounce: fn => fn,
            syncDevicesViewToggle: jest.fn(), syncDeviceSortControls: jest.fn(),
            syncDevicesAgentFilterOptions: jest.fn(), refreshDeviceFilters: jest.fn(),
            renderDevicesOverview: jest.fn(), syncTenantFilterOptions: jest.fn(),
            renderDeviceTable: jest.fn(), renderDeviceCards: jest.fn(),
            updateDeviceSelectionUI: jest.fn(), renderDeviceLoadingStatus: jest.fn(),
            syncDeviceTableSortIndicators: jest.fn(),
            getAgentInfo: () => null, formatTenantDisplay: id => id,
        });
        context.initDevicesUI();
        context.applyDeviceFilters();
        const tag = key => document.querySelector(`#devices_stats [data-status="${key}"]`);
        return { context, document, devicesVM, tag };
    }

    test.each(['table', 'cards'])('%s view selects, combines and deselects tags through actual controls', view => {
        const { context, document, devicesVM, tag } = statusSetup(view);
        const visible = () => devicesVM.filtered.map(device => device.serial);
        expect(visible()).toEqual(keys.slice(0, -1));
        expect(tag('warning').getAttribute('aria-pressed')).toBe('false');
        tag('warning').focus();
        tag('warning').click();
        expect(visible()).toEqual(['warning']);
        expect(document.activeElement).toBe(tag('warning'));
        expect(tag('warning').getAttribute('aria-pressed')).toBe('true');
        expect(document.querySelector('#devices_status_filter [data-status="warning"]').classList.contains('active')).toBe(true);
        tag('error').click();
        expect(visible()).toEqual(['warning', 'error']);
        expect(tag('healthy').textContent).toBe('healthy 1');
        tag('warning').click();
        expect(visible()).toEqual(['error']);
        tag('error').click();
        expect(visible()).toEqual(keys.slice(0, -1));
        expect(document.querySelector('[data-clear-statuses]').disabled).toBe(true);
        expect(context[view === 'table' ? 'renderDeviceTable' : 'renderDeviceCards'])
            .toHaveBeenLastCalledWith(devicesVM.filtered);
    });

    test('sidebar shares selection and clear preserves unrelated filters', () => {
        const { context, document, devicesVM, tag } = statusSetup();
        Object.assign(devicesVM.filters, {
            query: 'epson', agentId: 'agent-1', tenantId: 'tenant-1', manufacturer: 'Epson',
        });
        document.querySelector('#devices_status_filter [data-status="jam"]').click();
        expect(devicesVM.filtered.map(device => device.serial)).toEqual(['jam']);
        expect(tag('jam').getAttribute('aria-pressed')).toBe('true');
        tag('warning').click();
        document.querySelector('[data-clear-statuses]').click();
        expect(devicesVM.filters.statuses.size).toBe(0);
        expect(devicesVM.filters).toMatchObject({
            query: 'epson', agentId: 'agent-1', tenantId: 'tenant-1', manufacturer: 'Epson',
        });
        expect(devicesVM.filtered).toHaveLength(5);
        tag('error').click();
        context.handleFilterChipRemove('statuses');
        expect(devicesVM.filters.statuses.size).toBe(0);
        tag('error').click();
        context.resetDeviceFilters();
        expect(devicesVM.filters.statuses.size).toBe(0);
        expect(devicesVM.filters.query).toBe('');
    });

    test('zero-count tags, all tags and invalid statuses behave consistently', () => {
        const { context, document, devicesVM, tag } = statusSetup();
        tag('unknown').click();
        expect(devicesVM.filtered).toHaveLength(0);
        expect(tag('unknown').getAttribute('aria-pressed')).toBe('true');
        keys.slice(0, -1).forEach(key => tag(key).click());
        expect(devicesVM.filtered).toHaveLength(5);
        expect(document.querySelector('#devices_active_filters [data-filter="statuses"]')).not.toBeNull();
        context.toggleStatusFilter('invalid');
        expect(devicesVM.filters.statuses.size).toBe(keys.length);
        keys.forEach(key => tag(key).click());
        expect(devicesVM.filters.statuses.size).toBe(0);
        expect(devicesVM.filtered).toHaveLength(5);
    });
});

describe('Server device details ownership', () => {
    test('shared Details action uses scoped full rows, latest metrics, and saved state', async () => {
        const showDetails = jest.fn();
        const fetchDeviceInventory = jest.fn().mockImplementation(async kind => kind === 'rows'
            ? [{ serial: 'SAVED', agent_id: 'agent-1', is_saved: true, raw_data: { raw_data: { toner_desc_black: 'Black Ink ABC' } } }]
            : [{ serial: 'SAVED', toner_levels: { toner_black: 25 } }]);
        const deleteDevice = jest.fn();
        const { context } = setup(['showPrinterDetails'], {
            devicesVM: { items: [{ serial: 'SAVED', ip: '192.0.2.10' }] },
            devicesLoader: { getGeneration: () => 1 },
            fetchDeviceInventory, deleteDevice,
            window: { __pm_shared: { showToast: jest.fn() }, __pm_shared_cards: { showPrinterDetailsData: showDetails } }
        });
        expect(context.window.__pm_shared.showPrinterDetails).toBe(context.showPrinterDetails);
        await context.window.__pm_shared.showPrinterDetails('SAVED', 'discovered');
        expect(fetchDeviceInventory.mock.calls.map(call => call[0])).toEqual(['rows', 'metrics/query']);
        const [device, source, debug, options] = showDetails.mock.calls[0];
        expect(device.is_saved).toBe(true);
        expect(device.toner_levels).toEqual({ toner_black: 25 });
        expect(device.raw_data.raw_data.toner_desc_black).toBe('Black Ink ABC');
        expect(options).toMatchObject({ server: true, deleteDevice });
    });
});

describe('Agent details and update entry points', () => {
    const escapeHtml = value => String(value).replaceAll('&', '&amp;').replaceAll('"', '&quot;').replaceAll('<', '&lt;').replaceAll('>', '&gt;');

    test('details show reported Windows build, safe text, enrollment age, and collapsed diagnostics', () => {
        const attachName = jest.fn();
        const attachUpdate = jest.fn();
        const { context, document } = setup(['getAgentPlatformLabel', 'renderAgentDetailsModal'], {
            escapeHtml, getAgentDisplayName: agent => agent.name,
            _attachAgentDetailsNameEditor: attachName, _attachAgentUpdateHandler: attachUpdate,
            window: { __pm_shared: { renderCopyButton: () => '' } },
        });
        const agent = {
            agent_id: 'agent-1', name: '<img src=x onerror=alert(1)>', hostname: 'PC',
            platform: 'windows', os_version: 'Windows 11 (24H2) (build 26100)',
            status: 'active', connection_type: 'ws', version: '0.32.0-beta.2',
            registered_at: new Date(Date.now() - 3 * 86400000).toISOString(),
        };
        context.renderAgentDetailsModal(agent);
        const body = document.getElementById('agent_details_body');
        expect(body.textContent).toContain('Windows 11 (24H2) (build 26100)');
        expect(body.textContent).toContain(agent.name);
        expect(body.querySelector('img')).toBeNull();
        expect(body.textContent).toContain('Enrolled');
        expect(body.textContent).not.toContain('Uptime');
        expect(body.querySelector('details').open).toBe(false);
        expect(body.querySelector('.agent-details-overview #agent_check_update_btn').disabled).toBe(false);
        expect(attachName).toHaveBeenCalledWith(agent);
        expect(attachUpdate).toHaveBeenCalledWith(agent);
    });

    test('offline agents have disabled commands and missing metadata is explicit', () => {
        const { context, document } = setup(['getAgentPlatformLabel', 'renderAgentDetailsModal'], {
            escapeHtml, getAgentDisplayName: () => 'Offline PC',
            _attachAgentDetailsNameEditor: jest.fn(), _attachAgentUpdateHandler: jest.fn(),
            window: { __pm_shared: { renderCopyButton: () => '' } },
        });
        context.renderAgentDetailsModal({ agent_id: 'agent-1', platform: 'windows', status: 'offline' });
        expect(document.getElementById('agent_details_body').textContent).toContain('Not reported by agent');
        expect(document.getElementById('agent_check_update_btn').disabled).toBe(true);
        expect(document.getElementById('agent_force_update_btn').disabled).toBe(true);
    });

    test('inline update respects policy without a picker; context update asks once and sends selection', async () => {
        const showAgentUpdateChannelConfirm = jest.fn().mockResolvedValue('beta');
        const fetch = jest.fn().mockResolvedValue({ ok: true, json: async () => ({ success: true }) });
        const { context } = setup(['updateAgent'], {
            showAgentUpdateChannelConfirm, fetch, agentsVM: { updateState: {}, latestVersion: '0.32.0' },
            refreshAgentVersionCell: jest.fn(),
            scheduleAgentUpdateObservation: jest.fn(),
        });
        await context.updateAgent('agent-1');
        expect(showAgentUpdateChannelConfirm).not.toHaveBeenCalled();
        expect(JSON.parse(fetch.mock.calls[0][1].body)).toEqual({ command: 'check_update' });
        context.agentsVM.updateState = {};
        await context.updateAgent('agent-1', { chooseChannel: true });
        expect(showAgentUpdateChannelConfirm).toHaveBeenCalledTimes(1);
        expect(JSON.parse(fetch.mock.calls[1][1].body)).toEqual({
            command: 'install_channel', data: { channel: 'beta', reason: 'server_ui_channel_install' },
        });
        context.agentsVM.updateState = {};
        showAgentUpdateChannelConfirm.mockResolvedValueOnce(null);
        await context.updateAgent('agent-1', { chooseChannel: true });
        expect(fetch).toHaveBeenCalledTimes(2);
    });

    test('update picker focuses its select and Escape cancels without dispatch', async () => {
        const { context, document } = setup(['showAgentUpdateChannelConfirm']);
        const pending = context.showAgentUpdateChannelConfirm();
        expect(document.activeElement.id).toBe('agent_update_channel');
        const event = new document.defaultView.KeyboardEvent('keydown', { key: 'Escape', bubbles: true });
        document.activeElement.dispatchEvent(event);
        expect(await pending).toBeNull();
        expect(document.getElementById('agent_update_channel')).toBeNull();
    });
});

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

    describe('device selection and details', () => {
            function deviceSetup(extraNames = [], globals = {}) {
                return setup([
                    'updateDeviceSelectionUI', 'handleDeviceSelection', 'reconcileDeviceSelection',
                    ...extraNames,
                ], {
                    devicesVM: {
                        selection: { selectedIds: new Set(), lastSelected: null },
                        filtered: [{ serial: 'A' }, { serial: 'B' }],
                        render: { displayed: 0, pageSize: 1 },
                    },
                    escapeHtml: value => String(value).replaceAll('&', '&amp;').replaceAll('"', '&quot;').replaceAll('<', '&lt;'),
                    ...globals,
                });
            }

            test('click, Ctrl-click, and Shift-click select devices without extra controls', () => {
                const { context, document } = deviceSetup();
                context.window.__pm_shared.showPrinterDetails = jest.fn();
                const container = document.getElementById('devices_cards');
                container.innerHTML = ['A', 'B'].map(serial =>
                    `<div class="device-card-clickable" data-serial="${serial}">${serial}</div>`
                ).join('');
                context.handleDeviceSelection('A', {});
                context.handleDeviceSelection('B', { ctrlKey: true });
                expect([...context.devicesVM.selection.selectedIds]).toEqual(['A', 'B']);
                expect(container.firstElementChild.classList.contains('device-card-selected')).toBe(true);
                context.handleDeviceSelection('A', { ctrlKey: true });
                expect([...context.devicesVM.selection.selectedIds]).toEqual(['B']);
                context.handleDeviceSelection('B', {});
                context.handleDeviceSelection('A', { shiftKey: true });
                expect([...context.devicesVM.selection.selectedIds]).toEqual(['A', 'B']);
                expect(container.querySelectorAll('input, button')).toHaveLength(0);
            });

            test('refresh reset/failure preserves selection; ready authorized index prunes only absent IDs', () => {
                let onChange;
                const loader = {
                    getIndexState: () => ({ status: 'ready' }),
                    getIndex: () => [{ serial: 'A' }, { serial: 'C' }],
                };
                const { context } = deviceSetup(['initDevicesLoader'], {
                    devicesLoader: null, deviceSupplyBands: new Map(), devicesDemand: [], devicesPaintFrame: null,
                    window: { PrintMasterProgressive: { createLoader: options => { onChange = options.onChange; return loader; } } },
                    cleanupDevicesInfiniteScroll: jest.fn(), renderDevicesOverview: jest.fn(),
                    renderDeviceLoadingStatus: jest.fn(), refreshDeviceFilters: jest.fn(),
                    syncDevicesAgentFilterOptions: jest.fn(), applyDeviceFilters: jest.fn(),
                    enrichDevices: items => items, renderDevicesError: jest.fn(),
                });
                context.devicesVM.selection.selectedIds = new Set(['A', 'B']);
                context.devicesVM.selection.lastSelected = 'B';
                context.initDevicesLoader();
                onChange({ type: 'reset' });
                expect([...context.devicesVM.selection.selectedIds]).toEqual(['A', 'B']);
                loader.getIndexState = () => ({ status: 'error', error: 'offline' });
                onChange({ type: 'index' });
                expect([...context.devicesVM.selection.selectedIds]).toEqual(['A', 'B']);
                loader.getIndexState = () => ({ status: 'ready' });
                onChange({ type: 'index' });
                expect([...context.devicesVM.selection.selectedIds]).toEqual(['A']);
                expect(context.devicesVM.selection.lastSelected).toBeNull();
            });

            test('table uses only customized data columns and preserves progressive pages', () => {
                const { context, document } = deviceSetup(['renderDevicesTableHeader', 'renderDeviceTable'], {
                    initTableScrollIndicators: jest.fn(), setupDevicesInfiniteScroll: jest.fn(),
                    getProgressiveDevice: device => device, loadMoreDevices: jest.fn(),
                });
                context.devicesVM.tableCustomizer = {
                    renderHeader: () => '<th data-column-id="network">Network</th>',
                    bindHeaderEvents: jest.fn(), getVisibleColumns: () => ['network'],
                    renderRow: device => `<td data-column-id="network">${device.ip}</td>`,
                };
                context.renderDevicesTableHeader();
                expect(document.getElementById('devices_table_header').children).toHaveLength(1);
                expect(document.querySelector('#devices_table_header > th').dataset.columnId).toBe('network');
                context.renderDeviceTable([{ serial: 'A', ip: '10.0.0.1' }, { serial: 'B', ip: '10.0.0.2' }]);
                expect(document.querySelectorAll('.device-row-clickable')).toHaveLength(1);
                expect(document.querySelector('.device-row-clickable [data-device-details]')).toBeNull();
                expect(document.querySelector('.device-row-clickable > td').dataset.columnId).toBe('network');
                expect(document.querySelector('#devices_load_more_sentinel td').colSpan).toBe(1);
                expect(context.devicesVM.render.displayed).toBe(1);
            });
        });
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

    test('authorization failure takes priority over a concurrent service failure', async () => {
        const fetch = jest.fn().mockResolvedValueOnce({ ok: false, status: 503 })
            .mockResolvedValueOnce({ ok: false, status: 403 });
        const { context, document, state } = settingsSetup(fetch);
        await context.loadServerSettings();
        expect(document.getElementById('draft')).toBeNull();
        expect(state.data).toBeNull();
    });

    test('missing lock metadata never renders an editable form', async () => {
        const { context, document } = settingsSetup(jest.fn().mockResolvedValue({
            ok: true, json: async () => ({}),
        }));
        await context.loadServerSettings();
        expect(context.renderServerSettingsForm).not.toHaveBeenCalled();
        expect(document.getElementById('draft')).toBeNull();
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

describe('server control accessibility', () => {
    test('theme, report range and modal close controls have accessible names and native semantics', () => {
        const { document } = setup([]);
        expect(document.getElementById('theme-toggle-checkbox').getAttribute('aria-label')).toBeTruthy();
        expect(document.querySelector('label[for="usage_report_range"]')).not.toBeNull();
        document.querySelectorAll('.modal-close').forEach(close => {
            expect(close.tagName).toBe('BUTTON');
            expect(close.type).toBe('button');
            expect(close.getAttribute('aria-label')).toBeTruthy();
        });

    });

        describe('fleet channel and SNMP controls', () => {
            test('channel options are explicit and SNMP passwords are masked', () => {
                const { context } = setup(['createInputForField'], {
                    resolveFieldValue: (field, value) => value ?? field.default
                });
                const { input } = context.createInputForField({
                    path: 'features.agent_update_channel', type: 'select',
                    enum: ['', 'stable', 'beta', 'dev'], default: ''
                }, 'dev');
                expect(Array.from(input.options).map(option => option.textContent)).toEqual([
                    'Use Agent configuration', 'Stable', 'Beta', 'Dev'
                ]);
                expect(input.value).toBe('dev');
                expect(context.createInputForField({ path: 'snmp.auth_password', type: 'password' }, 'secret').input.type).toBe('password');
            });

            test('unmanaged fleet fields are read-only at global, customer and Agent scope', () => {
                const { context } = setup(['renderSettingsFieldRow', 'createInputForField'], {
                    settingsUIState: {
                        lockedKeys: new Set(), selectedTenantId: 'tenant-1', selectedAgentId: 'agent-1'
                    },
                    resolveFieldValue: (field, value) => value,
                    escapeHtml: value => value, userCan: () => true,
                    hasOverride: () => false, pathToArray: path => path.split('.')
                });
                const field = { path: 'features.agent_update_channel', type: 'select', enum: ['', 'stable', 'beta', 'dev'] };
                for (const scope of ['global', 'tenant', 'agent']) {
                    expect(context.renderSettingsFieldRow(field, 'dev', scope, false).querySelector('select').disabled).toBe(true);
                    expect(context.renderSettingsFieldRow(field, 'dev', scope, true).querySelector('select').disabled).toBe(false);
                }
            });

            test.each(['global', 'tenant', 'agent'])('release channel is inside Agent Updates at %s scope and respects ownership', scope => {
                const field = { path: 'features.agent_update_channel', type: 'select', enum: ['', 'stable', 'beta', 'dev'] };
                const state = {
                    schema: {}, globalDraft: { features: { agent_update_channel: 'dev' } },
                    tenantDraft: { features: { agent_update_channel: 'beta' } },
                    agentDraft: { features: { agent_update_channel: 'stable' } },
                    scope, groupedFields: { features: [field] }, managedSections: new Set(['features']),
                    agentEnforcedSections: new Set(), lockedKeys: new Set(),
                    selectedTenantId: 'tenant-1', selectedAgentId: 'agent-1'
                };
                const { context, document } = setup([
                    'getSettingsDraft', 'renderSettingsForm', 'refreshPolicyPanel', 'renderUpdatePolicySection',
                    'renderSettingsFieldRow', 'createInputForField'
                ], {
                    settingsUIState: state, renderManagedSectionsPanel: () => null,
                    renderTenantEnforcementPanel: () => null, orderedSettingsSections: () => ['features'],
                    getValueByPath: (draft) => draft.features.agent_update_channel,
                    SETTINGS_SECTION_LABELS: { features: 'Features' }, escapeHtml: value => value,
                    resolveFieldValue: (field, value) => value, userCan: () => true,
                    hasOverride: () => scope !== 'global', pathToArray: path => path.split('.'),
                    getSettingsPayload: () => ({}), getPolicyState: () => ({ loaded: true, enabled: false })
                });
                context.renderSettingsForm();
                const root = document.getElementById('settings_form_root');
                const panel = root.querySelector('#auto_update_policy_section');
                expect(panel.querySelector('h4').textContent).toBe('Agent Updates');
                expect(root.querySelector('#fleet_update_channel_section')).toBeNull();
                const channel = () => panel.querySelector('select[data-settings-path="features.agent_update_channel"]');
                expect(root.querySelectorAll('select[data-settings-path="features.agent_update_channel"]')).toHaveLength(1);
                expect(channel().value).toBe({ global: 'dev', tenant: 'beta', agent: 'stable' }[scope]);
                expect(channel().disabled).toBe(false);
                if (scope !== 'global') expect(panel.querySelector('.inherit-btn').textContent).toBe('Inherit');
                context.refreshPolicyPanel();
                expect(root.querySelectorAll('select[data-settings-path="features.agent_update_channel"]')).toHaveLength(1);
                if (scope === 'agent') {
                    state.agentEnforcedSections.add('features');
                    context.renderSettingsForm();
                    expect(root.querySelector('#auto_update_policy_section select').disabled).toBe(true);
                    expect(root.querySelector('#auto_update_policy_section .inherit-btn')).toBeNull();
                }
                state.managedSections.clear();
                context.renderSettingsForm();
                expect(root.querySelector('#auto_update_policy_section select').disabled).toBe(true);
                expect(root.querySelector('#auto_update_policy_section').textContent).toContain('Section Management');
            });

            test.each(['global', 'tenant', 'agent'])('Save Changes persists the channel at %s scope', async scope => {
                const overrides = { features: { agent_update_channel: 'beta' } };
                const fetchJSON = jest.fn().mockResolvedValue({});
                const state = {
                    globalDirty: true, globalSettingsDirty: true, globalDraft: overrides,
                    managedSections: new Set(['features']),
                    tenantDirty: true, tenantSettingsDirty: true, tenantOverridesDraft: overrides,
                    tenantEnforcedSections: new Set(), selectedTenantId: 'tenant-1',
                    agentDirty: true, agentOverridesDraft: overrides,
                    agentEnforcedSections: new Set(), selectedAgentId: 'agent-1'
                };
                const { context } = setup(['saveGlobalSettings', 'saveTenantSettings', 'saveAgentSettings'], {
                    settingsUIState: state, fetchJSON, getPolicyState: () => ({ dirty: false }),
                    cloneSettings: value => JSON.parse(JSON.stringify(value)),
                    flattenOverrides: () => ['features.agent_update_channel'],
                    loadGlobalSettingsSnapshot: jest.fn(), loadTenantSnapshot: jest.fn(),
                    loadAgentSnapshot: jest.fn(), renderSettingsUI: jest.fn(),
                    window: { __pm_shared: { showToast: jest.fn() } }
                });
                await context[{ global: 'saveGlobalSettings', tenant: 'saveTenantSettings', agent: 'saveAgentSettings' }[scope]]();
                expect(fetchJSON).toHaveBeenCalledTimes(1);
                const [endpoint, request] = fetchJSON.mock.calls[0];
                expect(endpoint).toBe({
                    global: '/api/v1/settings/global',
                    tenant: '/api/v1/settings/tenants/tenant-1',
                    agent: '/api/v1/settings/agents/agent-1'
                }[scope]);
                expect(request.method).toBe('PUT');
                const payload = JSON.parse(request.body);
                expect((scope === 'tenant' ? payload.overrides : payload).features.agent_update_channel).toBe('beta');
            });
        });

    test('reopening login replaces the Enter handler and pending submissions are guarded', async () => {
        let resolveFetch;
        const fetch = jest.fn(() => new Promise(resolve => { resolveFetch = resolve; }));
        const { context, document } = setup(['showLoginModal'], { fetch });
        context.showLoginModal();
        context.showLoginModal();
        const password = document.getElementById('login_password');
        const pressEnter = () => password.dispatchEvent(
            new document.defaultView.KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true })
        );
        pressEnter();
        pressEnter();
        document.getElementById('login_submit').click();
        expect(fetch).toHaveBeenCalledTimes(1);
        expect(document.getElementById('login_submit').disabled).toBe(true);
        resolveFetch({ ok: false, text: async () => 'Invalid credentials' });
        await new Promise(resolve => setImmediate(resolve));
        expect(document.getElementById('login_submit').disabled).toBe(false);
        pressEnter();
        expect(fetch).toHaveBeenCalledTimes(2);
    });
});
