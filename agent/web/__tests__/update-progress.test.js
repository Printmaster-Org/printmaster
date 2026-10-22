const fs = require('fs');
const path = require('path');
const vm = require('vm');

const source = fs.readFileSync(path.join(__dirname, '..', 'app.js'), 'utf8');
const start = source.indexOf('function handleUpdateProgress(');
const end = source.indexOf('// Update the auto-update buttons', start);
const shared = fs.readFileSync(path.join(__dirname, '..', '..', '..', 'common', 'web', 'shared.js'), 'utf8');
const helper = shared.slice(shared.indexOf('function updateProgressView('), shared.indexOf('window.__pm_shared.updateProgressView'));

describe('Agent ordered update progress', () => {
    test('terminal replay and cleanup cannot overwrite a newer run', () => {
        jest.useFakeTimers();
        try {
            const state = {};
            const toast = jest.fn();
            const context = vm.createContext({
                updateProgressState: state, updateProgressSnapshot: null, updateProgressTimer: null, setTimeout, clearTimeout, Date,
                window: { __pm_shared: { showToast: toast } },
                updateAutoUpdateButtonsFromProgress: jest.fn(),
                resetAutoUpdateButtons: jest.fn(), refreshAutoUpdateStatus: jest.fn(),
            });
            vm.runInContext(helper + '; window.__pm_shared.updateProgressView = updateProgressView;', context);
            vm.runInContext(source.slice(start, end), context);
            const event = (sequence, status, run = 'one', started = '2026-10-16T18:00:00Z') => ({
                run_id: run, run_started_at: started, sequence, status, progress: status === 'succeeded' ? 100 : 80,
            });
            context.handleUpdateProgress(event(4, 'succeeded'));
            context.handleUpdateProgress(event(3, 'restarting'));
            context.handleUpdateProgress(event(4, 'succeeded'));
            expect(state.status).toBe('succeeded');
            expect(toast).toHaveBeenCalledTimes(1);
            context.handleUpdateProgress(event(1, 'downloading', 'two', '2026-10-16T18:01:00Z'), { silent: true });
            jest.advanceTimersByTime(5000);
            expect(state.status).toBe('downloading');
            expect(context.resetAutoUpdateButtons).not.toHaveBeenCalled();
        } finally {
            jest.useRealTimers();
        }
    });

    test('stale timers are identity-bound, replay does not renew them, and errors persist', () => {
        jest.useFakeTimers();
        try {
            const state = {};
            const render = jest.fn();
            const context = vm.createContext({
                updateProgressState: state, updateProgressSnapshot: null, updateProgressTimer: null,
                setTimeout, clearTimeout, Date,
                window: { __pm_shared: { showToast: jest.fn() } },
                updateAutoUpdateButtonsFromProgress: render,
            });
            vm.runInContext(helper + '; window.__pm_shared.updateProgressView = updateProgressView;', context);
            vm.runInContext(source.slice(start, end), context);
            const data = { run_id: 'one', run_started_at: new Date().toISOString(), updated_at: new Date().toISOString(), sequence: 1, status: 'verifying' };
            context.handleUpdateProgress(data, { silent: true });
            jest.advanceTimersByTime(119999);
            context.handleUpdateProgress(data, { silent: true });
            expect(render).toHaveBeenCalledTimes(1);
            jest.advanceTimersByTime(1);
            expect(render).toHaveBeenCalledTimes(2);
            expect(state.status).toBe('verifying');
            context.handleUpdateProgress({ ...data, sequence: 2, status: 'failed', error: 'Owner repair required; preserve protected state' });
            jest.advanceTimersByTime(600000);
            expect(state.status).toBe('failed');
            expect(state.error).toContain('Owner repair required');
        } finally {
            jest.useRealTimers();
        }
    });

    test('an ambiguous request blocks duplicates, and a late response cannot overwrite Agent progress', async () => {
        jest.useFakeTimers();
        try {
            const state = {};
            const toast = jest.fn();
            let resolve;
            const fetch = jest.fn(() => new Promise(done => { resolve = done; }));
            const context = vm.createContext({
                updateProgressState: state, updateProgressSnapshot: null, updateProgressTimer: null,
                setTimeout, clearTimeout, Date, AbortSignal, fetch,
                window: { __pm_shared: { showToast: toast, error: jest.fn() } },
                updateAutoUpdateButtonsFromProgress: jest.fn(), refreshAutoUpdateStatus: jest.fn(),
            });
            vm.runInContext(helper + '; window.__pm_shared.updateProgressView = updateProgressView;', context);
            vm.runInContext(source.slice(start, end), context);
            vm.runInContext(source.slice(source.indexOf('async function requestAutoUpdate('), source.indexOf('async function handleAutoUpdateRefresh(')), context);
            const pending = context.requestAutoUpdate('/api/autoupdate/force');
            await context.requestAutoUpdate('/api/autoupdate/force');
            expect(fetch).toHaveBeenCalledTimes(1);
            expect(state.status).toBe('dispatched');
            context.handleUpdateProgress({
                run_id: 'one', run_started_at: new Date().toISOString(), updated_at: new Date().toISOString(),
                sequence: 1, status: 'failed', error: 'trust-clock-rollback: correct local UTC; do not reset checkpoint',
            });
            resolve({ ok: true, json: async () => ({ status: 'force_triggered' }) });
            await pending;
            expect(state.status).toBe('failed');
            expect(state.error).toContain('trust-clock-rollback');
            expect(toast.mock.calls.some(([, level]) => level === 'success')).toBe(false);
        } finally {
            jest.useRealTimers();
        }
    });
});
