// Small helper(s) used by agent save routines. Kept separate so tests can
// import focused behavior without loading the whole UI bundle.

function getMetricsRescanIntervalFromDom() {
    const el = (typeof document !== 'undefined') ? document.getElementById('metrics_rescan_interval') : null;
    let iv = el ? parseInt(el.value, 10) : NaN;
    if (isNaN(iv)) iv = 60;
    if (iv < 1) iv = 1;
    if (iv > 1440) iv = 1440;
    return iv;
}

try { if (typeof module !== 'undefined' && module.exports) module.exports = { getMetricsRescanIntervalFromDom }; } catch (e) {}
try { window.__pm_shared = window.__pm_shared || {}; window.__pm_shared.getMetricsRescanIntervalFromDom = window.__pm_shared.getMetricsRescanIntervalFromDom || getMetricsRescanIntervalFromDom; } catch (e) {}
