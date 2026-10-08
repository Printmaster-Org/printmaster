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
});
