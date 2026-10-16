const fs = require('fs');
const path = require('path');
const vm = require('vm');

const source = fs.readFileSync(path.join(__dirname, '..', 'app.js'), 'utf8');
const start = source.indexOf('function handleUpdateProgress(');
const end = source.indexOf('// Update the auto-update buttons', start);

describe('Agent ordered update progress', () => {
    test('terminal replay and cleanup cannot overwrite a newer run', () => {
        jest.useFakeTimers();
        try {
            const state = {};
            const toast = jest.fn();
            const context = vm.createContext({
                updateProgressState: state, updateProgressSnapshot: null, setTimeout,
                window: { __pm_shared: { showToast: toast } },
                updateAutoUpdateButtonsFromProgress: jest.fn(),
                resetAutoUpdateButtons: jest.fn(), refreshAutoUpdateStatus: jest.fn(),
            });
            vm.runInContext(source.slice(start, end), context);
            const event = (sequence, status, run = 'one', started = '2026-10-16T18:00:00Z') => ({
                run_id: run, run_started_at: started, sequence, status, progress: status === 'succeeded' ? 100 : 80,
            });
            context.handleUpdateProgress(event(4, 'succeeded'));
            context.handleUpdateProgress(event(3, 'restarting'));
            context.handleUpdateProgress(event(4, 'succeeded'));
            expect(state.status).toBe('complete');
            expect(toast).toHaveBeenCalledTimes(1);
            context.handleUpdateProgress(event(1, 'downloading', 'two', '2026-10-16T18:01:00Z'), { silent: true });
            jest.advanceTimersByTime(5000);
            expect(state.status).toBe('downloading');
            expect(context.resetAutoUpdateButtons).not.toHaveBeenCalled();
        } finally {
            jest.useRealTimers();
        }
    });
});
