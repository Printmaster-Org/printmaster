const fs = require('fs');
const path = require('path');
const vm = require('vm');

const source = fs.readFileSync(path.join(__dirname, '..', 'shared.js'), 'utf8');
const start = source.indexOf('function updateProgressView(');
const end = source.indexOf('window.__pm_shared.updateProgressView', start);
const { updateProgressView } = vm.runInNewContext(`${source.slice(start, end)}; ({ updateProgressView })`);

describe('truthful update presentation', () => {
    const now = Date.parse('2026-10-22T12:00:00Z');
    const snapshot = (status, extra = {}) => ({
        status, run_id: 'one', progress: -1, updated_at: new Date(now).toISOString(), ...extra,
    });

    test.each([
        ['checking', 'Checking'], ['pending', 'Preparing'], ['downloading', 'Downloading'],
        ['staging', 'Staging'], ['applying', 'Installing'], ['restarting', 'Restarting'],
        ['verifying', 'Awaiting confirmation'], ['dispatched', 'Command sent'],
    ])('%s is distinct and never success', (status, label) => {
        const view = updateProgressView(snapshot(status), now, now);
        expect(view.label).toBe(label);
        expect(view.busy).toBe(true);
        expect(view.succeeded).toBe(false);
        expect(view.progress).toBeNull();
    });

    test('the exact two-minute observation boundary does not declare failure or allow cancellation', () => {
        const data = snapshot('restarting', { progress: 80 });
        expect(updateProgressView(data, now, now + 119999).overdue).toBe(false);
        const overdue = updateProgressView(data, now, now + 120000);
        expect(overdue.overdue).toBe(true);
        expect(overdue.message).toContain('outcome is not yet known');
        expect(overdue.busy).toBe(true);
        expect(overdue.canCancel).toBe(false);
        expect(overdue.failed).toBe(false);
        expect(overdue.succeeded).toBe(false);
    });

    test('old replay is immediately stale, and future Agent clocks cannot extend the local wait', () => {
        expect(updateProgressView(snapshot('applying'), now + 600000, now + 600000).overdue).toBe(true);
        const future = snapshot('verifying', { updated_at: new Date(now + 86400000).toISOString() });
        expect(updateProgressView(future, now, now + 120000).overdue).toBe(true);
    });

    test.each(['complete', 'succeeded'])('legacy %s cannot prove completion', status => {
        const view = updateProgressView({ status, progress: 100 }, now, now);
        expect(view.succeeded).toBe(false);
        expect(view.uncertain).toBe(true);
        expect(view.busy).toBe(true);
        expect(view.progress).toBe(99);
    });

    test('only reported download percentages are used; active 100 cannot imply completion', () => {
        expect(updateProgressView(snapshot('downloading', { progress: 45 }), now, now).progress).toBe(45);
        expect(updateProgressView(snapshot('downloading', { progress: 100 }), now, now).progress).toBe(99);
        expect(updateProgressView(snapshot('succeeded', { progress: 100 }), now, now).progress).toBe(100);
    });

    test.each(['failed', 'rolled_back'])('%s retains actionable owner repair and trust guidance', status => {
        const error = 'trust-state-recovery-required: preserve checkpoint; stop writers and use exact-checkpoint repair';
        const view = updateProgressView(snapshot(status, { error }), now, now + 86400000);
        expect(view.message).toBe(error);
        expect(view.failed).toBe(true);
        expect(view.busy).toBe(false);
        expect(view.overdue).toBe(false);
    });

    test.each(['idle', 'skipped', 'cancelled', 'succeeded'])('%s stays terminal even when old', status => {
        expect(updateProgressView(snapshot(status), now, now + 86400000).busy).toBe(false);
    });

    test('unknown status is explicitly unresolved rather than silently idle', () => {
        const view = updateProgressView(snapshot('future-phase'), now, now);
        expect(view.uncertain).toBe(true);
        expect(view.message).toContain('Unknown update status: future-phase');
        expect(view.busy).toBe(true);
    });
});
