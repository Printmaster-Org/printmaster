/* Framework-free inventory hydration. Adapters own transport and presentation. */
(function (root, factory) {
    const api = factory();
    if (typeof module === 'object' && module.exports) module.exports = api;
    if (root) root.PrintMasterProgressive = api;
})(typeof window === 'undefined' ? null : window, function () {
    'use strict';

    function createLoader(options) {
        const keyOf = options.getKey || (item => item.serial);
        const batchSize = Math.max(1, Math.min(100, options.batchSize || 30));
        const cacheLimit = Math.max(batchSize, options.cacheLimit || 300);
        const concurrency = Math.max(1, Math.min(4, options.concurrency || 2));
        const maxAttempts = Math.max(1, Math.min(3, options.maxAttempts || 2));
        let generation = 0, index = [], indexState = 'idle', indexError = null;
        let abort = new AbortController(), active = 0;
        let demanded = new Set(), rendered = new Set();
        let globalMetrics = false;
        let states = { rows: new Map(), metrics: new Map() };
        let caches = { rows: new Map(), metrics: new Map() };
        let queues = { rows: new Set(), metrics: new Set() };
        let authorized = new Set();
        const emit = (type, keys = []) => options.onChange?.({ type, keys, generation });
        const state = (kind, key) => states[kind].get(key) || { status: 'idle', attempts: 0 };
        function read(kind, key) {
            const value = caches[kind].get(key);
            if (value) { caches[kind].delete(key); caches[kind].set(key, value); }
            return value;
        }
        function enqueue(kind, keys, retry = false) {
            for (const key of keys) {
                if (!authorized.has(key)) continue;
                const previous = state(kind, key);
                if (previous.status === 'loading' || previous.status === 'missing') continue;
                if (previous.status === 'error' && (!retry || previous.attempts >= maxAttempts)) continue;
                if (previous.status === 'ready' && caches[kind].has(key)) continue;
                queues[kind].add(key);
            }
            pump();
        }
        function pump() {
            while (active < concurrency) {
                const kind = queues.rows.size ? 'rows' : 'metrics';
                if (!queues[kind].size) return;
                const keys = [...queues[kind]].slice(0, batchSize);
                keys.forEach(key => queues[kind].delete(key));
                const epoch = generation, signal = abort.signal;
                const localStates = states[kind], localCache = caches[kind];
                keys.forEach(key => localStates.set(key, { status: 'loading', attempts: state(kind, key).attempts + 1 }));
                active++;
                emit(kind, keys);
                Promise.resolve().then(() => options[kind === 'rows' ? 'fetchRows' : 'fetchMetrics'](keys, { signal, generation: epoch }))
                    .then(result => {
                        if (epoch !== generation || signal.aborted) return;
                        if (!Array.isArray(result)) throw new Error('Expected an array');
                        const requested = new Set(keys), found = new Set();
                        result.forEach(item => {
                            const key = keyOf(item);
                            if (!requested.has(key)) return;
                            localCache.set(key, item);
                            found.add(key);
                        });
                        keys.forEach(key => localStates.set(key, { ...localStates.get(key), status: found.has(key) ? 'ready' : 'missing' }));
                        while (localCache.size > cacheLimit) localCache.delete(localCache.keys().next().value);
                        emit(kind, keys);
                    }).catch(error => {
                        if (epoch !== generation || signal.aborted) return;
                        keys.forEach(key => localStates.set(key, { ...localStates.get(key), status: 'error', error }));
                        emit(kind, keys);
                    }).finally(() => {
                        if (epoch !== generation) return;
                        active--;
                        pump();
                    });
            }
        }
        function reset() {
            abort.abort();
            generation++;
            abort = new AbortController();
            index = []; authorized = new Set(); indexState = 'idle'; indexError = null;
            active = 0; demanded = new Set(); rendered = new Set();
            globalMetrics = false;
            states = { rows: new Map(), metrics: new Map() };
            caches = { rows: new Map(), metrics: new Map() };
            queues = { rows: new Set(), metrics: new Set() };
            emit('reset');
        }
        async function load() {
            reset();
            const epoch = generation, signal = abort.signal;
            indexState = 'loading'; emit('index');
            try {
                const result = await options.fetchIndex({ signal, generation: epoch });
                if (epoch !== generation || signal.aborted) return;
                if (!Array.isArray(result)) throw new Error('Expected an array');
                index = result.filter(item => {
                    const key = keyOf(item);
                    if (!key || authorized.has(key)) return false;
                    authorized.add(key); return true;
                });
                indexState = 'ready'; emit('index');
            } catch (error) {
                if (epoch !== generation || signal.aborted) return;
                indexState = 'error'; indexError = error; emit('index');
            }
        }
        return {
            load, reset,
            getIndex: () => index,
            getIndexState: () => ({ status: indexState, error: indexError }),
            getState: state,
            getRow: key => read('rows', key),
            getMetrics: key => read('metrics', key),
            getGeneration: () => generation,
            getCacheSizes: () => ({ rows: caches.rows.size, metrics: caches.metrics.size }),
            setDemand(keys) {
                demanded = new Set(keys);
                queues.rows = new Set([...queues.rows].filter(key => demanded.has(key)));
                if (!globalMetrics) queues.metrics = new Set([...queues.metrics].filter(key => demanded.has(key)));
                enqueue('rows', demanded);
                enqueue('metrics', [...demanded].filter(key => rendered.has(key) && caches.rows.has(key)));
            },
            // Call only after hydrated rows have been inserted into the DOM.
            markRendered(keys) {
                keys.forEach(key => { if (caches.rows.has(key)) rendered.add(key); });
                enqueue('metrics', keys.filter(key => rendered.has(key) && demanded.has(key)));
            },
            // Explicit global supply filtering is the only non-viewport metrics demand.
            requestMetrics: keys => { globalMetrics = true; enqueue('metrics', keys); },
            clearMetricQueue: () => { globalMetrics = false; queues.metrics = new Set([...queues.metrics].filter(key => demanded.has(key))); },
            retry(keys) {
                enqueue('rows', keys, true);
                enqueue('metrics', keys.filter(key => rendered.has(key)), true);
            }
        };
    }

    // Own both demand observation and infinite-page observation in shared code.
    // Native button remains a keyboard/unsupported-IntersectionObserver fallback.
    function observeViewport({ elements, sentinel, onDemand, onMore, getKey, root = null, overscan = 200 }) {
        const visible = new Set();
        if (typeof IntersectionObserver === 'undefined') {
            onDemand(elements.slice(0, 30).map(getKey));
            return { disconnect() {} };
        }
        const observer = new IntersectionObserver(entries => {
            let more = false;
            entries.forEach(entry => {
                if (entry.target === sentinel) { more = entry.isIntersecting; return; }
                const key = getKey(entry.target);
                if (entry.isIntersecting) visible.add(key); else visible.delete(key);
            });
            onDemand([...visible]);
            if (more) onMore();
        }, { root, rootMargin: `${overscan}px`, threshold: 0 });
        elements.forEach(element => observer.observe(element));
        if (sentinel) observer.observe(sentinel);
        return observer;
    }
    return { createLoader, observeViewport };
});