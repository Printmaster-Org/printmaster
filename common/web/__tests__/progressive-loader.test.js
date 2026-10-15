const { createLoader, observeViewport } = require('../progressive-loader');
const tick = async () => { for (let i = 0; i < 8; i++) await new Promise(resolve => setImmediate(resolve)); };
const deferred = () => { let resolve, reject; const promise = new Promise((yes, no) => { resolve = yes; reject = no; }); return { promise, resolve, reject }; };
const items = count => Array.from({ length: count }, (_, i) => ({ serial: String(i) }));
function fixture(overrides = {}) {
    const options = {
        fetchIndex: jest.fn(async () => items(240)),
        fetchRows: jest.fn(async keys => keys.map(serial => ({ serial, model: 'Hydrated' }))),
        fetchMetrics: jest.fn(async keys => keys.map(serial => ({ serial, toner_levels: { black: 50 } }))),
        onChange: jest.fn(), ...overrides,
    };
    return { loader: createLoader(options), options };
}

test('index does not eagerly fetch rows or metrics; explicit demand dedupes', async () => {
    const { loader, options } = fixture();
    await loader.load();
    expect(loader.getIndex()).toHaveLength(240);
    expect(options.fetchRows).not.toHaveBeenCalled();
    loader.setDemand(['1', '1', '2', 'unauthorized']);
    loader.setDemand(['1', '2']);
    await tick();
    expect(options.fetchRows).toHaveBeenCalledTimes(1);
    expect(options.fetchRows.mock.calls[0][0]).toEqual(['1', '2']);
    expect(options.fetchMetrics).not.toHaveBeenCalled();
    loader.markRendered(['1', '2']);
    await tick();
    loader.setDemand(['2', '1']); loader.markRendered(['2', '1']);
    await tick();
    expect(options.fetchMetrics).toHaveBeenCalledTimes(1);
});

test('delayed rows never start metrics until hydrated rows render', async () => {
    const gate = deferred();
    const { loader, options } = fixture({ fetchRows: jest.fn(() => gate.promise) });
    await loader.load(); loader.setDemand(['1']); loader.markRendered(['1']);
    await tick();
    expect(loader.getState('rows', '1').status).toBe('loading');
    expect(options.fetchMetrics).not.toHaveBeenCalled();
    gate.resolve([{ serial: '1' }]); await tick();
    expect(options.fetchMetrics).not.toHaveBeenCalled();
    loader.markRendered(['1']); await tick();
    expect(options.fetchMetrics).toHaveBeenCalledTimes(1);
});

test('partial results become missing, never loop, unsolicited keys ignored', async () => {
    const { loader, options } = fixture({ fetchRows: jest.fn(async () => [{ serial: '1' }, { serial: '99' }]) });
    await loader.load(); loader.setDemand(['1', '2']); await tick();
    expect(loader.getState('rows', '2').status).toBe('missing');
    expect(loader.getRow('99')).toBeUndefined();
    loader.setDemand(['1', '2']); loader.retry(['2']); await tick();
    expect(options.fetchRows).toHaveBeenCalledTimes(1);
});

test('row/metric failures retry explicitly at most twice, never automatically', async () => {
    const { loader, options } = fixture({ fetchRows: jest.fn(async () => { throw new Error('503'); }) });
    await loader.load(); loader.setDemand(['1']); await tick();
    expect(loader.getState('rows', '1').status).toBe('error');
    loader.setDemand(['1']); await tick();
    expect(options.fetchRows).toHaveBeenCalledTimes(1);
    loader.retry(['1']); await tick(); loader.retry(['1']); await tick();
    expect(options.fetchRows).toHaveBeenCalledTimes(2);
    expect(loader.getState('rows', '1').attempts).toBe(2);
});

test('metric error leaves hydrated row ready; missing snapshot is distinct', async () => {
    const { loader } = fixture({ fetchMetrics: async keys => keys.includes('1') ? Promise.reject(new Error('503')) : [] });
    await loader.load(); loader.setDemand(['1']); await tick(); loader.markRendered(['1']); await tick();
    expect(loader.getState('metrics', '1').status).toBe('error');
    expect(loader.getState('rows', '1').status).toBe('ready');
    loader.requestMetrics(['2']); await tick();
    expect(loader.getState('metrics', '2').status).toBe('missing');
});

test('reset aborts index and ignores stale adapters even when they ignore signal', async () => {
    const gate = deferred();
    const { loader, options } = fixture({ fetchIndex: jest.fn().mockImplementationOnce(() => gate.promise).mockResolvedValue([{ serial: 'fresh' }]) });
    const old = loader.load();
    const signal = options.fetchIndex.mock.calls[0][0].signal;
    await loader.load();
    expect(signal.aborted).toBe(true);
    gate.resolve([{ serial: 'stale' }]); await old;
    expect(loader.getIndex()).toEqual([{ serial: 'fresh' }]);
});

test('reset ignores stale rows/metrics completion and owns new concurrency slots', async () => {
    const gate = deferred();
    const { loader, options } = fixture({ concurrency: 1, fetchRows: jest.fn().mockImplementationOnce(() => gate.promise).mockImplementation(async keys => keys.map(serial => ({ serial, fresh: true }))) });
    await loader.load(); loader.setDemand(['1']); await tick();
    const oldSignal = options.fetchRows.mock.calls[0][1].signal;
    await loader.load(); loader.setDemand(['2']); await tick();
    gate.resolve([{ serial: '1' }]); await tick();
    expect(oldSignal.aborted).toBe(true);
    expect(loader.getRow('1')).toBeUndefined();
    expect(loader.getRow('2').fresh).toBe(true);
});

test.each([30, 500])('bounded batches/concurrency with configured size %s', async batchSize => {
    let active = 0, peak = 0;
    const { loader, options } = fixture({ batchSize, fetchRows: jest.fn(async keys => {
        peak = Math.max(peak, ++active); await tick(); active--; return keys.map(serial => ({ serial }));
    }) });
    await loader.load(); loader.setDemand(items(240).map(item => item.serial)); await tick(); await tick(); await tick();
    expect(peak).toBeLessThanOrEqual(2);
    expect(Math.max(...options.fetchRows.mock.calls.map(call => call[0].length))).toBeLessThanOrEqual(Math.min(100, batchSize));
});

test('loaded rows and supplies survive scrolling beyond 300 devices without refetching', async () => {
    const { loader, options } = fixture({ fetchIndex: jest.fn(async () => items(360)) });
    await loader.load();
    for (let offset = 0; offset < 360; offset += 30) {
        const keys = items(360).slice(offset, offset + 30).map(item => item.serial);
        loader.setDemand(keys); await tick();
        loader.markRendered(keys); await tick();
    }
    expect(loader.getCacheSizes()).toEqual({ rows: 360, metrics: 360 });
    expect(loader.getIndex()[0].model).toBeUndefined();
    const row = loader.getRow('0'), metrics = loader.getMetrics('0');
    options.fetchRows.mockClear(); options.fetchMetrics.mockClear();
    loader.setDemand([]); loader.setDemand(['0']); loader.markRendered(['0']); await tick();
    expect(loader.getRow('0').model).toBe('Hydrated');
    expect(loader.getRow('0')).toBe(row);
    expect(loader.getMetrics('0')).toBe(metrics);
    expect(loader.getState('metrics', '0').status).toBe('ready');
    expect(options.fetchRows).not.toHaveBeenCalled();
    expect(options.fetchMetrics).not.toHaveBeenCalled();
    await loader.load();
    expect(loader.getCacheSizes()).toEqual({ rows: 0, metrics: 0 });
});

test('replaced demand drops queued offscreen rows; explicit metrics scan remains bounded', async () => {
    const gate = deferred();
    const { loader, options } = fixture({ concurrency: 1, fetchRows: jest.fn().mockImplementationOnce(() => gate.promise).mockImplementation(async keys => keys.map(serial => ({ serial }))) });
    await loader.load(); loader.setDemand(items(200).map(item => item.serial)); await tick();
    loader.setDemand(['230']); gate.resolve(items(30)); await tick();
    expect(options.fetchRows.mock.calls.map(call => call[0])).toEqual([items(30).map(item => item.serial), ['230']]);
    loader.requestMetrics(items(240).map(item => item.serial)); await tick();
    expect(options.fetchMetrics.mock.calls.every(call => call[0].length <= 30)).toBe(true);
});

test('malformed index errors; empty index is ready, not error', async () => {
    const { loader } = fixture({ fetchIndex: jest.fn().mockResolvedValueOnce({}).mockResolvedValueOnce([]) });
    await loader.load(); expect(loader.getIndexState().status).toBe('error');
    await loader.load(); expect(loader.getIndexState().status).toBe('ready');
});

test('background refresh retains unchanged hydration, invalidates changed keys, and removes unauthorized devices', async () => {
    const { loader, options } = fixture({
        fetchIndex: jest.fn().mockResolvedValueOnce(items(3)).mockResolvedValueOnce(items(2)),
    });

    await loader.load();
    loader.setDemand(['0', '1', '2']); await tick();
    loader.markRendered(['0', '1', '2']); await tick();
    const retained = loader.getRow('0');
    const metrics = loader.getMetrics('0');
    options.onChange.mockClear(); options.fetchRows.mockClear(); options.fetchMetrics.mockClear();
    await loader.refresh(['1']); await tick();
    expect(loader.getRow('0')).toBe(retained);
    expect(loader.getMetrics('0')).toBe(metrics);
    expect(loader.getIndex()).toEqual(items(2));
    expect(loader.getRow('2')).toBeUndefined();
    expect(options.onChange.mock.calls.some(([event]) => event.type === 'reset')).toBe(false);
    expect(options.fetchRows.mock.calls[0][0]).toEqual(['1']);
    loader.markRendered(['1']); await tick();
    expect(options.fetchMetrics.mock.calls[0][0]).toEqual(['1']);
});

test('background refresh keeps slow unchanged requests alive without duplicate hydration', async () => {
    const gate = deferred();
    const { loader, options } = fixture({ fetchRows: jest.fn(() => gate.promise) });
    await loader.load(); loader.setDemand(['1']); await tick();
    const signal = options.fetchRows.mock.calls[0][1].signal;
    await loader.refresh(); await loader.refresh();
    expect(signal.aborted).toBe(false);
    expect(options.fetchRows).toHaveBeenCalledTimes(1);
    gate.resolve([{ serial: '1', model: 'Slow response' }]); await tick();
    expect(loader.getRow('1').model).toBe('Slow response');
});

test('background refresh rejects changed and revoked rows from obsolete requests', async () => {
    const gate = deferred();
    const { loader } = fixture({
        concurrency: 1,
        fetchIndex: jest.fn().mockResolvedValueOnce(items(3)).mockResolvedValueOnce(items(2)),
        fetchRows: jest.fn().mockImplementationOnce(() => gate.promise)
            .mockImplementation(async keys => keys.map(serial => ({ serial, model: 'Fresh' }))),
    });
    await loader.load(); loader.setDemand(['0', '1', '2']); await tick();
    await loader.refresh(['1']);
    gate.resolve(items(3).map(item => ({ ...item, model: 'Obsolete' }))); await tick();
    expect(loader.getRow('0').model).toBe('Obsolete');
    expect(loader.getRow('1').model).toBe('Fresh');
    expect(loader.getRow('2')).toBeUndefined();
});

test('refresh failure retains last inventory and reports explicit stale error', async () => {
    const { loader, options } = fixture({
        fetchIndex: jest.fn().mockResolvedValueOnce(items(2)).mockRejectedValueOnce(new Error('offline')),
    });
    await loader.load(); loader.setDemand(['0']); await tick();
    const row = loader.getRow('0');
    await loader.refresh();
    expect(loader.getIndex()).toEqual(items(2));
    expect(loader.getRow('0')).toBe(row);
    expect(loader.getIndexState().error.message).toBe('offline');
    expect(options.onChange).toHaveBeenLastCalledWith(expect.objectContaining({ type: 'refresh-error' }));
});

test('refresh cannot revive inventory after reset and ignores in-flight obsolete hydration', async () => {
    const index = deferred(), rows = deferred();
    const { loader, options } = fixture({
        fetchIndex: jest.fn().mockResolvedValueOnce(items(2)).mockImplementationOnce(() => index.promise).mockResolvedValueOnce([]),
        fetchRows: jest.fn(() => rows.promise),
    });
    await loader.load(); loader.setDemand(['0']); await tick();
    const refresh = loader.refresh();
    await tick();
    await loader.load();
    index.resolve(items(2)); rows.resolve([{ serial: '0' }]); await refresh; await tick();
    expect(loader.getIndex()).toEqual([]);
    expect(loader.getRow('0')).toBeUndefined();
});

test('viewport helper owns overscan, demand, more and disconnect', () => {
    let callback, config;
    const observe = jest.fn(), disconnect = jest.fn();
    global.IntersectionObserver = class { constructor(cb, options) { callback = cb; config = options; } observe = observe; disconnect = disconnect; };
    const row = { key: '1' }, sentinel = {};
    const onDemand = jest.fn(), onMore = jest.fn();
    const observer = observeViewport({ elements: [row], sentinel, getKey: element => element.key, onDemand, onMore });
    callback([{ target: row, isIntersecting: true }, { target: sentinel, isIntersecting: true }]);
    expect(config.rootMargin).toBe('200px'); expect(onDemand).toHaveBeenLastCalledWith(['1']); expect(onMore).toHaveBeenCalledTimes(1);
    callback([{ target: row, isIntersecting: false }]); expect(onDemand).toHaveBeenLastCalledWith([]);
    observer.disconnect(); expect(disconnect).toHaveBeenCalled(); delete global.IntersectionObserver;
});