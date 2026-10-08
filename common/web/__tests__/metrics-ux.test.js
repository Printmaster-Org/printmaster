/** @jest-environment jsdom */

require('../cards.js');
require('../metrics.js');

const metrics = window.__pm_shared_metrics;
const flush = () => new Promise(resolve => setTimeout(resolve, 0));

describe('metrics identity and loading', () => {
    beforeEach(() => {
        document.body.innerHTML = '<div id="metrics_content"></div>';
        window.__pm_shared = Object.fromEntries(['log', 'warn', 'error', 'debug', 'trace'].map(key => [key, jest.fn()]));
        window.metricsDataRange = null;
        window.__pm_shared.flatpickrReady = Promise.resolve(jest.fn((input, options) => ({
            selectedDates: options.defaultDate, destroy: jest.fn()
        })));
        global.fetch = jest.fn();
    });

    test('keeps photo and matte black as two continuous series', () => {
        const series = metrics.buildTonerSeries([1, 2, 3].map(day => ({
            timestamp: `2026-10-0${day}T00:00:00Z`,
            toner_levels: { photo_black: 70 - day, matte_black: 50 - day }
        })));
        expect(Object.keys(series)).toEqual(['Photo Black', 'Matte Black']);
        expect(series['Photo Black'].points).toHaveLength(3);
        expect(series['Matte Black'].points).toHaveLength(3);
    });

    test.each(['Gray', 'Light Gray', 'Light Cyan', 'Photo Black', 'Orange', 'Violet'])(
        'shares the card palette for %s', name => {
            expect(metrics.resolveTonerColor(name, 'default')).toBe(window.__pm_shared_cards.getTonerColor(name));
        });

    test('seven-day range is exact and clamped to available history', () => {
        const end = new Date('2026-10-08T00:00:00Z');
        const start = new Date('2026-07-01T00:00:00Z');
        expect((end - metrics.getMetricsInitialRange(start, end, '7day')[0]) / 86400000).toBe(7);
        const recent = new Date('2026-10-06T00:00:00Z');
        expect(metrics.getMetricsInitialRange(recent, end, '7day')[0]).toEqual(recent);
        expect(metrics.getMetricsInitialRange(start, end)[0]).toEqual(start);
    });

    test('bounds failure displays unavailable state, logs, and retry retains requested preset', async () => {
        fetch.mockResolvedValue({ ok: false, status: 503 });
        await metrics.loadDeviceMetrics('A', 'metrics_content', { preset: '7day' });
        expect(document.getElementById('metrics_stats').textContent).toContain('Metrics unavailable');
        expect(document.getElementById('metrics_chart').hidden).toBe(true);
        expect(window.__pm_shared.error).toHaveBeenCalled();
        document.querySelector('#metrics_stats button').click();
        await flush();
        expect(fetch).toHaveBeenCalledTimes(2);
        expect(document.getElementById('metrics_content')._metricsOptions).toEqual({ preset: '7day' });
    });

    test('history failure hides stale chart and rows instead of reporting an empty result', async () => {
        document.getElementById('metrics_content').innerHTML = '<div id="metrics_stats">Old totals</div><canvas id="metrics_chart"></canvas><div id="toner_legend"></div>';
        document.body.insertAdjacentHTML('beforeend', '<div id="metrics_rows_panel">Old rows</div>');
        window.metricsDataRange = { flatpickr: { selectedDates: [new Date('2026-10-01'), new Date('2026-10-08')] } };
        fetch.mockResolvedValue({ ok: false, status: 500 });
        await metrics.refreshMetricsChart('A');
        expect(document.getElementById('metrics_stats').textContent).toContain('Previous results are not current');
        expect(document.getElementById('metrics_rows_panel').hidden).toBe(true);
        expect(document.getElementById('metrics_chart').hidden).toBe(true);
    });

    test('reopening the same device ignores an old history request even when request counters match', async () => {
        const content = document.getElementById('metrics_content');
        content.dataset.metricsSerial = 'A';
        content.innerHTML = '<div id="metrics_stats">Loading</div><canvas id="metrics_chart"></canvas><div id="toner_legend"></div>';
        content._metricsGeneration = 1;
        window.metricsDataRange = { flatpickr: { selectedDates: [new Date('2026-10-01'), new Date('2026-10-08')] } };
        let complete;
        fetch.mockReturnValue(new Promise(resolve => { complete = resolve; }));
        const pending = metrics.refreshMetricsChart('A');
        content._metricsGeneration = 2;
        content.querySelector('#metrics_stats').textContent = 'New view';
        complete({ ok: false, status: 503 });
        await pending;
        expect(content.querySelector('#metrics_stats').textContent).toBe('New view');
        expect(window.__pm_shared.error).not.toHaveBeenCalled();
    });

    test('date picker remains within its modal and new empty history removes previous rows', async () => {
        document.body.insertAdjacentHTML('beforeend', '<div id="metrics_rows_panel">Old rows</div>');
        fetch.mockResolvedValue({ ok: true, json: async () => ({}) });
        await metrics.loadDeviceMetrics('A', 'metrics_content');
        expect(document.getElementById('metrics_rows_panel')).toBeNull();
        const picker = jest.fn(() => ({ selectedDates: [], destroy: jest.fn() }));
        window.__pm_shared.flatpickrReady = Promise.resolve(picker);
        fetch.mockResolvedValue({ ok: true, json: async () => ({
            min_timestamp: '2026-10-01T00:00:00Z', max_timestamp: '2026-10-08T00:00:00Z'
        }) });
        await metrics.loadDeviceMetrics('B', 'metrics_content');
        expect(picker.mock.calls[0][1].appendTo).toBe(document.getElementById('metrics_content'));
        expect(document.getElementById('metrics_chart').hidden).toBe(true);
    });

    test('cartridge legend displays arbitrary supply names as literal text', () => {
        const content = document.getElementById('metrics_content');
        content.innerHTML = '<div id="toner_legend"></div>';
        const name = '<img src=x onerror=alert(1)> Black cartridge';
        metrics.renderTonerLegend(content, { [name]: { colorKey: 'black', points: [{ time: 1, value: 50 }] } });
        expect(content.querySelector('img')).toBeNull();
        expect(content.textContent).toBe(name + ': 50%');
    });

    test('usage history failures provide logged Retry rather than claiming no data', async () => {
        document.body.insertAdjacentHTML('beforeend', '<div id="usage-graph-A"></div>');
        fetch.mockResolvedValue({ ok: false, status: 503 });
        await window.loadUsageGraph('A');
        expect(document.getElementById('usage-graph-A').textContent).toBe('Usage unavailableRetry');
        expect(window.__pm_shared.warn).toHaveBeenCalled();
        fetch.mockResolvedValue({ ok: true, json: async () => [{ timestamp: '2026-10-01T00:00:00Z' }] });
        document.querySelector('#usage-graph-A button').click();
        await flush();
        expect(document.getElementById('usage-graph-A').textContent).toBe('Not collected');
        expect(fetch).toHaveBeenCalledTimes(2);
    });

    test('missing page counters do not fabricate zero-valued points while supplies remain charted', () => {
        const canvas = document.createElement('canvas');
        const ctx = Object.fromEntries(['scale', 'clearRect', 'beginPath', 'moveTo', 'lineTo', 'stroke',
            'save', 'restore', 'fillText', 'translate', 'rotate', 'arc', 'fill'].map(name => [name, jest.fn()]));
        canvas.getContext = jest.fn(() => ctx);
        canvas.getBoundingClientRect = () => ({ width: 600, height: 300 });
        const start = new Date('2026-10-01T00:00:00Z');
        const end = new Date('2026-10-08T00:00:00Z');
        metrics.drawMetricsChart(canvas, [{ timestamp: start.toISOString() }, { timestamp: end.toISOString() }],
            start, end, { Cyan: { colorKey: 'cyan', points: [{ time: start.getTime(), value: 50 }] } });
        expect(ctx.fillText.mock.calls.some(([text]) => text === '0')).toBe(false);
        expect(ctx.fillText.mock.calls.some(([text]) => text === '50%')).toBe(true);
    });
});
