// PrintMaster Shared JavaScript - Common utilities for Agent and Server UIs

// Ensure a minimal namespaced API exists immediately so other bundles can
// call into `window.__pm_shared` without guards. We provide a readiness
// promise and a small SHARED logger here so other scripts can await and
// consistently emit debug/trace messages while initialization completes.
window.__pm_shared = window.__pm_shared || {};
(function console_forwarding_shim(){
    // Keep originals so we can still call them if needed
    try {
        if (typeof window === 'undefined' || !window.console) return;
        const orig = {
            debug: console.debug && console.debug.bind(console) || console.log.bind(console),
            info: console.info && console.info.bind(console) || console.log.bind(console),
            warn: console.warn && console.warn.bind(console) || console.log.bind(console),
            error: console.error && console.error.bind(console) || console.log.bind(console),
            trace: console.trace && console.trace.bind(console) || console.log.bind(console),
            log: console.log && console.log.bind(console) || function(){}
        };

        // If __pm_shared already has logger functions, prefer them; otherwise
        // create forwarding functions that call into __pm_shared when ready
        // but always fall back to original console to avoid losing logs.
        ['debug','info','warn','error','trace','log'].forEach(fn => {
            if (!window.__pm_shared[fn]) {
                window.__pm_shared[fn] = function(...args) {
                    try {
                        // Call underlying shared logger if present
                        if (window.__pm_shared && window.__pm_shared !== undefined && typeof window.__pm_shared[fn] === 'function' && window.__pm_shared[fn] !== orig[fn]) {
                            // avoid recursion: if our shim has already been installed, call original
                            // The check above ensures we don't infinitely recurse.
                            // Use a try/catch to avoid throwing when shared logger not yet fully initialized
                            try { orig[fn](...['[SHARED-FORWARD]'].concat(args)); } catch(e){}
                        }
                    } catch (e) {}
                    // Always write to original console as well
                    try { orig[fn](...args); } catch (e) {}
                };
            }
        });
    } catch (e) {
        try { window.__pm_shared.warn('console forwarding shim failed', e); } catch (e2) {}
    }
})();

// ============================================================================
// Proxy-Aware Fetch Wrapper
// ============================================================================
// When the agent UI is accessed through the server's proxy, we need to prefix
// all root-absolute URLs (like /api/devices) with the proxy base path.
// Instead of modifying every fetch() call, we intercept fetch globally.
(function proxyAwareFetch() {
    try {
        if (typeof window === 'undefined' || typeof fetch !== 'function') return;

        // Detect proxy base from <base href="..."> element (injected by server proxy)
        function getProxyBase() {
            try {
                const baseEl = document.querySelector('base');
                if (baseEl && baseEl.href) {
                    const u = new URL(baseEl.href, window.location.href);
                    // Return pathname without trailing slash, e.g. "/api/v1/proxy/agent/abc123"
                    let path = u.pathname || '';
                    if (path.endsWith('/')) path = path.slice(0, -1);
                    // Only return if it looks like a proxy path
                    if (path.includes('/proxy/')) {
                        return path;
                    }
                }
            } catch (e) { /* ignore */ }
            return '';
        }

        const originalFetch = window.fetch;
        window.fetch = function(input, init) {
            try {
                const proxyBase = getProxyBase();
                if (proxyBase) {
                    let url = typeof input === 'string' ? input : (input && input.url ? input.url : '');
                    // Only rewrite root-absolute paths that aren't already prefixed
                    if (url && url.charAt(0) === '/' && !url.startsWith(proxyBase)) {
                        const newUrl = proxyBase + url;
                        if (typeof input === 'string') {
                            input = newUrl;
                        } else if (input && input.url) {
                            input = new Request(newUrl, input);
                        }
                    }
                }
            } catch (e) { /* fail safe - just use original */ }
            // Call explicitly with (possibly rewritten) input/init rather than
            // the original `arguments` object, which only reflects reassigned
            // parameters in non-strict mode (fragile/implicit behavior).
            return originalFetch.call(this, input, init);
        };

        // Also intercept XMLHttpRequest for older code patterns
        const originalOpen = XMLHttpRequest.prototype.open;
        XMLHttpRequest.prototype.open = function(method, url, ...rest) {
            try {
                const proxyBase = getProxyBase();
                if (proxyBase && typeof url === 'string' && url.charAt(0) === '/' && !url.startsWith(proxyBase)) {
                    url = proxyBase + url;
                }
            } catch (e) { /* fail safe */ }
            return originalOpen.call(this, method, url, ...rest);
        };
    } catch (e) {
        try { console.warn('proxy-aware fetch shim failed', e); } catch (e2) {}
    }
})();

// ============================================================================
// Shared Auth Utilities (client-side)
// ============================================================================
// Provides a unified way for server and agent UIs to fetch the current
// principal and toggle visibility of elements marked with data-role
// (e.g. data-role="admin"). This keeps role logic centralized.
try {
    window.__pm_auth = window.__pm_auth || {
        currentUser: null,
        fetching: null,
        redirectPending: false,
        _optionsPromise: null,
        ensureAuth: async function() {
            // Reuse in-flight request
            if (this.fetching) return this.fetching;
            const self = this;
            this.fetching = (async () => {
                try {
                    const resp = await fetch('/api/v1/auth/me', { credentials: 'same-origin' });
                    if (resp.ok) {
                        self.currentUser = await resp.json();
                    } else {
                        self.currentUser = null;
                        self.handleAuthFailure(resp.status);
                    }
                } catch (e) {
                    self.currentUser = null;
                } finally {
                    const cu = self.currentUser;
                    self.fetching = null;
                    self.applyRBAC();
                    return cu;
                }
            })();
            return this.fetching;
        },
        handleAuthFailure: function(status) {
            try {
                if (status !== 401) return;
                if (this.redirectPending) return;
                if (typeof window === 'undefined' || !window.location) return;
                if ((window.location.pathname || '') === '/login') return;
                if (document && document.querySelector && document.querySelector('meta[http-equiv="X-PrintMaster-Proxied"]')) {
                    // Server proxy handles login redirects
                    return;
                }
                this.redirectPending = true;
                const finalize = (opts) => {
                    const target = this.buildLoginRedirect(opts);
                    if (target) {
                        window.location.href = target;
                    }
                };
                this.fetchOptions().then(finalize).catch(() => finalize(null));
            } catch (e) { /* graceful */ }
        },
        fetchOptions: function() {
            if (!this._optionsPromise) {
                this._optionsPromise = fetch('/api/v1/auth/options', { credentials: 'same-origin' })
                    .then(resp => resp.ok ? resp.json() : null)
                    .catch(() => null);
            }
            return this._optionsPromise;
        },
        buildLoginRedirect: function(opts) {
            try {
                if (typeof window === 'undefined' || !window.location) return '/login';
                const loc = window.location;
                const path = (loc.pathname || '/');
                const search = loc.search || '';
                const safeReturn = path.startsWith('/') ? (path + search) : '/';
                if (opts && opts.mode === 'server' && opts.server_url) {
                    const trimmed = opts.server_url.replace(/\/$/, '');
                    return trimmed + '/login?return_to=' + encodeURIComponent(safeReturn || '/');
                }
                const params = new URLSearchParams();
                params.set('return_to', safeReturn || '/');
                return '/login?' + params.toString();
            } catch (e) {
                return '/login';
            }
        },
        hasRole: function(minRole) {
            if (!this.currentUser) return false;
            const order = { admin: 3, operator: 2, viewer: 1 };
            const cur = order[(this.currentUser.role||'').toLowerCase()] || 0;
            const req = order[(minRole||'').toLowerCase()] || 0;
            return cur >= req;
        },
        applyRBAC: function(){
            const cu = this.currentUser;
            const self = this;
            const nodes = document.querySelectorAll('[data-role]');
            // Use Array.prototype.forEach for broad browser compatibility
            Array.prototype.forEach.call(nodes, function(el){
                const need = (el.getAttribute('data-role')||'').toLowerCase();
                if (!need) return;
                if (need === 'admin') {
                    el.style.display = (cu && (cu.role||'').toLowerCase() === 'admin') ? '' : 'none';
                } else {
                    // Future roles: operator/viewer gating
                    el.style.display = (cu && self.hasRole(need)) ? '' : 'none';
                }
            });
        }
    };
} catch (e) { /* non-fatal */ }

// ============================================================================
// Debug controls and unload guard
// ============================================================================
// Provide a simple debugMode flag and an isUnloading guard so verbose
// trace output (which used console.trace) can be suppressed in normal
// runs and during page navigations. Calls to `window.__pm_shared.trace`
// will be no-ops unless debugMode is true.
try {
    window.__pm_shared.debugMode = window.__pm_shared.debugMode || false;
    window.__pm_shared.isUnloading = false;

    // Mark unloading on various lifecycle events so we can suppress
    // noisy logs when the browser intentionally tears down connections.
    window.addEventListener && window.addEventListener('visibilitychange', () => {
        if (document.visibilityState === 'hidden') window.__pm_shared.isUnloading = true;
    });
    window.addEventListener && window.addEventListener('pagehide', () => { window.__pm_shared.isUnloading = true; });
    window.addEventListener && window.addEventListener('beforeunload', () => { window.__pm_shared.isUnloading = true; });

    // Wrap the existing trace function so we only emit a full stack trace
    // when debugMode is enabled. In normal mode, downgrade to console.debug
    // to avoid noisy stack traces in the browser console.
    (function installTraceWrapper() {
        try {
            const s = window.__pm_shared || {};
            // Keep a reference to any existing trace implementation
            s.__original_trace = s.__original_trace || s.trace || function(){};
            s.trace = function(...args) {
                try {
                    if (s.isUnloading) return; // suppress during unload/navigation
                    if (s.debugMode) {
                        // Full trace for debugging
                        if (typeof console.trace === 'function') {
                            console.trace('[SHARED-TRACE]', ...args);
                            return;
                        }
                    }
                    // Normal run: lighter debug output
                    try { console.debug && console.debug('[SHARED]', ...args); } catch (e) { console.log('[SHARED]', ...args); }
                } catch (e) {}
            };
            window.__pm_shared = s;
        } catch (e) { /* best-effort: don't break the page */ }
    })();
} catch (e) { /* defensive */ }
function showPrinterDetails(identifier, source) {
    // Always resolve devices by serial/device key only. IP-based lookup is
    // intentionally disabled to avoid mismatches and inconsistent UI data.
    try {
        if (window.__pm_shared_cards && typeof window.__pm_shared_cards.showPrinterDetailsData === 'function') {
            if (!identifier) {
                window.__pm_shared.showToast('Printer identifier not provided', 'error');
                return;
            }

            // Treat the identifier strictly as a serial/device-key and search
            // the saved devices list. Prefer the central server API when
            // available (server is canonical). Fall back to the agent-local
            // endpoint if the server endpoint is unreachable or returns an
            // unexpected response.
            // Try to resolve a canonical server base in this order:
            // 1. <base href="..."> element (proxied pages inject an absolute base)
            // 2. meta[name="printmaster-server-base"] or meta[http-equiv="X-PrintMaster-Server-Base"]
            // 3. window.__pm_shared.serverBase (JS-injected global)
            // If none found, fall back to a relative fetch which targets the current origin.
            function resolveServerBase() {
                try {
                    // 1) base element
                    const baseEl = document.querySelector('base');
                    if (baseEl && baseEl.href) {
                        try {
                            const u = new URL(baseEl.href, window.location.href);
                            // Keep origin+pathname (proxyBase may include path)
                            return (u.origin || '') + (u.pathname || '') .replace(/\/$/, '') ;
                        } catch (e) { /* ignore */ }
                    }

                    // 2) meta tag (explicit server base)
                    const meta = document.querySelector('meta[name="printmaster-server-base"]') || document.querySelector('meta[http-equiv="X-PrintMaster-Server-Base"]');
                    if (meta && (meta.content || meta.getAttribute('content'))) {
                        try { return (new URL(meta.content, window.location.href)).origin; } catch (e) { /* ignore */ }
                    }

                    // 3) shared global
                    if (window.__pm_shared && window.__pm_shared.serverBase) {
                        try { return (new URL(window.__pm_shared.serverBase, window.location.href)).origin; } catch (e) { /* ignore */ }
                    }
                } catch (e) { /* defensive */ }
                return null;
            }

            const _serverBase = resolveServerBase();
            const tryServer = (_serverBase ? fetch((_serverBase.replace(/\/$/, '')) + '/api/v1/devices/list') : fetch('/api/v1/devices/list')).then(r => r.ok ? r.json() : null).catch(() => null);
            const tryAgent = fetch('/devices/list').then(r => r.ok ? r.json() : []).catch(() => []);

            // Race: prefer server result when it yields a list, otherwise use agent
            Promise.all([tryServer, tryAgent]).then(([serverList, agentList]) => {
                const list = (Array.isArray(serverList) && serverList.length > 0) ? serverList : (agentList || []);
                const arr = list || [];
                const match = arr.find(it => {
                    // Top-level saved item may have `serial` property
                    if (it && it.serial && String(it.serial) === String(identifier)) return true;
                    const p = it.printer_info || it;
                    if (p && (p.serial && String(p.serial) === String(identifier))) return true;
                    // Some items may include serial under different casing
                    if (p && (p.Serial && String(p.Serial) === String(identifier))) return true;
                    return false;
                });
                if (match) {
                    try {
                        // Prefer embedded printer_info but merge top-level saved fields
                        // into the object so UI helpers (which expect p.asset_number
                        // and p.location) can pick them up regardless of where they
                        // were stored by the server/agent.
                        const deviceObj = match.printer_info ? Object.assign({}, match.printer_info) : Object.assign({}, match);
                        // If server saved asset_number/location at top-level, copy them
                        if ((!deviceObj.asset_number || deviceObj.asset_number === '') && match.asset_number) deviceObj.asset_number = match.asset_number;
                        if ((!deviceObj.location || deviceObj.location === '') && match.location) deviceObj.location = match.location;
                        if ((!deviceObj.serial || deviceObj.serial === '') && match.serial) deviceObj.serial = match.serial;
                        // Also accept alternate casing used in some records
                        if ((!deviceObj.asset_number || deviceObj.asset_number === '') && match.AssetNumber) deviceObj.asset_number = match.AssetNumber;
                        if ((!deviceObj.location || deviceObj.location === '') && match.Location) deviceObj.location = match.Location;
                        if ((!deviceObj.serial || deviceObj.serial === '') && match.Serial) deviceObj.serial = match.Serial;
                        if (typeof match.is_saved === 'boolean') deviceObj.is_saved = match.is_saved;

                        window.__pm_shared_cards.showPrinterDetailsData(deviceObj, source || 'saved', null);
                    } catch (e) { window.__pm_shared.warn('showPrinterDetails render failed', e); }
                } else {
                    window.__pm_shared.showToast('Printer details not found for serial: ' + identifier, 'error');
                }
            }).catch(() => { window.__pm_shared.showToast('Failed to fetch device list', 'error'); });
        }
    } catch (e) {
        window.__pm_shared.error('showPrinterDetails wrapper failed', e);
        throw e;
    }
}
    function showToast(message, type = 'info', duration = type === 'error' ? 8000 : 3000) {
        // Log error toasts to console for debugging
        if (type === 'error') {
            try { window.__pm_shared.error('[TOAST ERROR]', message); } catch (e) { console.error('[TOAST ERROR]', message); }
        } else if (type === 'warning') {
            try { window.__pm_shared.warn('[TOAST WARNING]', message); } catch (e) { console.warn('[TOAST WARNING]', message); }
        }

        const container = document.getElementById('toast_container');
        if (!container) return;

        const toast = document.createElement('div');
        toast.className = `toast toast-${type}`;
        toast.setAttribute('role', type === 'error' ? 'alert' : 'status');
        toast.setAttribute('aria-atomic', 'true');

        const icons = {
            success: '✓',
            error: '✗',
            info: 'ℹ'
        };

        const icon = document.createElement('span');
        icon.className = 'toast-icon';
        icon.setAttribute('aria-hidden', 'true');
        icon.textContent = icons[type] || icons.info;
        const text = document.createElement('span');
        text.className = 'toast-message';
        text.textContent = message;
        const dismiss = document.createElement('button');
        dismiss.type = 'button';
        dismiss.className = 'toast-dismiss';
        dismiss.setAttribute('aria-label', 'Dismiss notification');
        dismiss.textContent = '×';
        toast.append(icon, text, dismiss);

        container.appendChild(toast);

        let timer;
        const remove = () => {
            clearTimeout(timer);
            toast.classList.add('toast-hiding');
            setTimeout(() => {
                if (toast.parentNode) {
                    toast.parentNode.removeChild(toast);
                }
            }, 300); // Match CSS animation duration
        };
        const resume = () => {
            clearTimeout(timer);
            if (!toast.contains(document.activeElement) && !toast.matches(':hover')) timer = setTimeout(remove, duration);
        };
        dismiss.onclick = remove;
        toast.addEventListener('mouseenter', () => clearTimeout(timer));
        toast.addEventListener('mouseleave', resume);
        toast.addEventListener('focusin', () => clearTimeout(timer));
        toast.addEventListener('focusout', resume);
        resume();
    }

    // Ensure the shared namespace exposes showToast
    window.__pm_shared.showToast = window.__pm_shared.showToast || showToast;

    // Ensure flatpickr is available on pages that need it (metrics modal, etc.).
    // This will inject the CSS and JS from the server's static path when the
    // document is ready. It is idempotent and exposes a Promise
    // `window.__pm_shared.flatpickrReady` that resolves to the `flatpickr`
    // global (or `null` if loading failed).
    (function ensureFlatpickrLoaded() {
        try {
            if (typeof window === 'undefined' || typeof document === 'undefined') return;

            // If flatpickr is already present, expose a resolved promise.
            if (window.flatpickr) {
                window.__pm_shared.flatpickrReady = window.__pm_shared.flatpickrReady || Promise.resolve(window.flatpickr);
                return;
            }

            // Avoid creating multiple loaders
            if (window.__pm_shared && window.__pm_shared.flatpickrReady) return;

            window.__pm_shared.flatpickrReady = new Promise((resolve, reject) => {
                const cssId = 'pm-flatpickr-css';
                const jsId = 'pm-flatpickr-js';
                const cssHref = '/static/flatpickr/flatpickr.min.css';
                const jsSrc = '/static/flatpickr/flatpickr.min.js';

                function createLink() {
                    if (!document.getElementById(cssId)) {
                        try {
                            const link = document.createElement('link');
                            link.id = cssId;
                            link.rel = 'stylesheet';
                            link.href = cssHref;
                            document.head.appendChild(link);
                        } catch (e) { /* non-fatal */ }
                    }
                }

                function createScript() {
                    if (document.getElementById(jsId)) {
                        // If script exists but flatpickr still undefined, wait a bit
                        const existing = document.getElementById(jsId);
                        existing.addEventListener && existing.addEventListener('load', () => resolve(window.flatpickr || null));
                        existing.addEventListener && existing.addEventListener('error', () => resolve(null));
                        return;
                    }
                    try {
                        const script = document.createElement('script');
                        script.id = jsId;
                        script.src = jsSrc;
                        script.async = false; // preserve execution order
                        script.onload = function() { resolve(window.flatpickr || null); };
                        script.onerror = function() { window.__pm_shared && window.__pm_shared.warn && window.__pm_shared.warn('[SHARED] flatpickr load failed'); resolve(null); };
                        document.head.appendChild(script);
                    } catch (e) { window.__pm_shared && window.__pm_shared.warn && window.__pm_shared.warn('[SHARED] flatpickr injection failed', e); resolve(null); }
                }

                // Create CSS immediately and the script once DOM ready; if DOMContentLoaded
                // already fired, inject immediately.
                try { createLink(); } catch (e) {}
                if (document.readyState === 'loading') {
                    document.addEventListener('DOMContentLoaded', createScript);
                } else {
                    // Small defer to ensure head exists in edge cases
                    setTimeout(createScript, 0);
                }

                // Safety timeout: if nothing resolves within 5s, resolve with null
                setTimeout(() => {
                    if (window.flatpickr) return; // already there
                    // If promise still pending, resolve with null to avoid hung awaits
                    resolve(window.flatpickr || null);
                }, 5000);
            });
        } catch (e) {
            try { window.__pm_shared && window.__pm_shared.warn && window.__pm_shared.warn('ensureFlatpickrLoaded failed', e); } catch (ex) {}
        }
    })();
// ============================================================================
// MODAL SYSTEM
// ============================================================================

// Observe legacy and dynamically created dialogs so both applications share one
// focus lifecycle without replacing their existing cancel/save handlers.
(function installDialogAccessibility() {
    const selector = '.modal, .modal-overlay, #printer_details_overlay';
    const focusable = 'button, input:not([type="hidden"]), select, textarea, a[href], [tabindex]';
    const stack = [];
    const inertState = new Map();
    let titleSequence = 0;
    function visible(element) {
        for (let node = element; node && node !== document; node = node.parentElement) {
            if (node.hidden || getComputedStyle(node).display === 'none' || getComputedStyle(node).visibility === 'hidden') return false;
        }
        return element.isConnected;
    }
    function controls(dialog) {
        return [...dialog.querySelectorAll(focusable)].filter(el => !el.disabled && el.tabIndex >= 0 && visible(el));
    }
    function focusDialog(entry) {
        const target = entry.dialog.querySelector('[autofocus], [id$="_cancel"]');
        (target && !target.disabled && visible(target) ? target : controls(entry.dialog)[0] || entry.dialog).focus();
    }
    function synchronize() {
        if (typeof document === 'undefined' || !document.body) return;
        const open = [...document.querySelectorAll(selector)].filter(visible);
        const removed = stack.filter(entry => !open.includes(entry.overlay));
        for (let index = stack.length - 1; index >= 0; index--) {
            if (!open.includes(stack[index].overlay)) stack.splice(index, 1);
        }
        let newest;
        for (const overlay of open) {
            if (stack.some(entry => entry.overlay === overlay)) continue;
            const dialog = overlay.querySelector('[role="dialog"], .modal-content, #printer_details_modal') || overlay;
            dialog.setAttribute('role', 'dialog');
            dialog.setAttribute('aria-modal', 'true');
            dialog.tabIndex = -1;
            const title = dialog.querySelector('.modal-title, h2, h3, h4, [id$="_title"]');
            if (title && !dialog.hasAttribute('aria-labelledby')) {
                if (!title.id) title.id = 'pm_dialog_title_' + ++titleSequence;
                dialog.setAttribute('aria-labelledby', title.id);
            }
            newest = { overlay, dialog, opener: document.activeElement };
            stack.push(newest);
        }
        for (const [node, inert] of inertState) {
            if (inert) node.setAttribute('inert', '');
            else node.removeAttribute('inert');
        }
        inertState.clear();
        const top = stack[stack.length - 1];
        if (top) {
            // Nested modals may live inside a panel: inert its siblings at every
            // ancestor, never the ancestor that contains the active dialog.
            for (let node = top.overlay; node && node !== document.body; node = node.parentElement) {
                for (const sibling of node.parentElement?.children || []) {
                    if (sibling === node || /SCRIPT|STYLE|LINK/.test(sibling.tagName) || sibling.id === 'toast_container') continue;
                    inertState.set(sibling, sibling.hasAttribute('inert'));
                    sibling.setAttribute('inert', '');
                }
            }
            if (newest || !top.dialog.contains(document.activeElement)) {
                const opener = removed[removed.length - 1]?.opener;
                if (!newest && opener?.isConnected && top.dialog.contains(opener) && visible(opener) && !opener.disabled) opener.focus();
                else focusDialog(top);
            }
        } else if (removed.length) {
            const opener = removed[0].opener;
            if (opener?.isConnected && visible(opener)) opener.focus();
        }
    }
    function initialize() {
        new MutationObserver(synchronize).observe(document.body, {
            subtree: true, childList: true, attributes: true, attributeFilter: ['style', 'class', 'hidden']
        });
        synchronize();
        document.addEventListener('keydown', event => {
            const top = stack[stack.length - 1];
            if (!top) return;
            if (event.key === 'Escape') {
                const picker = window.metricsDataRange?.flatpickr;
                if (picker?.isOpen && top.dialog.contains(picker.calendarContainer)) {
                    event.preventDefault();
                    event.stopImmediatePropagation();
                    picker.input?.focus();
                    picker.close();
                    return;
                }
                const dismiss = top.dialog.querySelector('[id$="_cancel"], .modal-close-x, .modal-close, [id$="_close_x"], [aria-label^="Close"]')
                    || controls(top.dialog).find(el => /^(close|cancel|ok)$/i.test(el.textContent.trim()));
                if (dismiss && !dismiss.disabled) {
                    event.preventDefault();
                    event.stopImmediatePropagation();
                    dismiss.click();
                }
            } else if (event.key === 'Tab') {
                const items = controls(top.dialog);
                const index = items.indexOf(document.activeElement);
                if (!items.length || index < 0 || (!event.shiftKey && index === items.length - 1) || (event.shiftKey && index === 0)) {
                    event.preventDefault();
                    (items.length ? items[event.shiftKey ? items.length - 1 : 0] : top.dialog).focus();
                }
            }
        }, true);
        document.addEventListener('focusin', () => {
            const top = stack[stack.length - 1];
            if (top && !top.dialog.contains(document.activeElement) && visible(top.overlay)) focusDialog(top);
        });
    }
    if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', initialize, { once: true });
    else initialize();
})();

// Create a simple temporary confirm modal when the embedded `confirm_modal`
// DOM is not available or when we want to avoid the native blocking
// `window.confirm`. Returns a Promise<boolean> that resolves to true when
// confirmed and false when cancelled.
function createTemporaryConfirmModal(message, title = 'Confirm', isDangerous = false) {
    return new Promise((resolve) => {
        try {
            const wrapper = document.createElement('div');
            wrapper.className = 'modal-overlay';
            const uid = 'tmp_confirm_' + Date.now();
            wrapper.id = uid;
            wrapper.style.display = 'flex';
            wrapper.innerHTML = `
                <div class="modal-content" style="max-width:480px;">
                    <div class="modal-header">
                        <h3 class="modal-title"></h3>
                        <button class="modal-close-x" title="Close" aria-label="Close">&times;</button>
                    </div>
                    <div class="modal-body">
                        <p style="white-space:pre-wrap;"></p>
                    </div>
                    <div class="modal-footer">
                        <button class="modal-button modal-button-secondary" data-action="cancel">Cancel</button>
                        <button class="modal-button modal-button-primary" data-action="confirm">OK</button>
                    </div>
                </div>
            `;
            wrapper.querySelector('.modal-title').textContent = title;
            wrapper.querySelector('.modal-body p').textContent = message;
            document.body.appendChild(wrapper);

            const onConfirm = () => { cleanup(); resolve(true); };
            const onCancel = () => { cleanup(); resolve(false); };
            const onBackdrop = (e) => { if (e.target === wrapper) onCancel(); };

            const btnConfirm = wrapper.querySelector('[data-action="confirm"]');
            const btnCancel = wrapper.querySelector('[data-action="cancel"]');
            const closeX = wrapper.querySelector('.modal-close-x');

            function cleanup() {
                try { btnConfirm && btnConfirm.removeEventListener('click', onConfirm); } catch (e) {}
                try { btnCancel && btnCancel.removeEventListener('click', onCancel); } catch (e) {}
                try { closeX && closeX.removeEventListener('click', onCancel); } catch (e) {}
                try { wrapper.removeEventListener('click', onBackdrop); } catch (e) {}
                try { wrapper.parentNode && wrapper.parentNode.removeChild(wrapper); } catch (e) {}
            }

            btnConfirm && btnConfirm.addEventListener('click', onConfirm);
            btnCancel && btnCancel.addEventListener('click', onCancel);
            closeX && closeX.addEventListener('click', onCancel);
            wrapper.addEventListener('click', onBackdrop);
        } catch (e) {
            window.__pm_shared.error('Temporary confirmation dialog failed', e);
            try { resolve(false); } catch (ex) {}
        }
    });
}

// Create a simple temporary alert modal when embedded modal is not present.
// This is non-blocking and will dismiss on OK.
function createTemporaryAlertModal(message, title = 'Notice', showDontRemindCheckbox = false, isHtml = false) {
    try {
        const wrapper = document.createElement('div');
        wrapper.className = 'modal-overlay';
        wrapper.style.display = 'flex';
        
        // Helper to escape HTML if global function is missing
        const safeEscape = (s) => (typeof escapeHtml === 'function' ? escapeHtml(s) : String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;").replace(/'/g, "&#039;"));

        wrapper.innerHTML = `
            <div class="modal-content" style="max-width:480px;">
                <div class="modal-header">
                    <h3 class="modal-title">${safeEscape(title)}</h3>
                    <button class="modal-close-x" title="Close">&times;</button>
                </div>
                <div class="modal-body">
                    <div style="white-space:pre-wrap;">${isHtml ? message : safeEscape(message)}</div>
                </div>
                <div class="modal-footer" style="display:flex;align-items:center;gap:12px;">
                    ${showDontRemindCheckbox ? `<label style="display:flex;align-items:center;gap:8px;cursor:pointer;margin-right:auto;"><input type="checkbox" id="_tmp_alert_dont_remind" style="cursor:pointer;"><span style="font-size:13px;color:var(--muted);">Don't show this again</span></label>` : ''}
                    <button class="modal-button modal-button-primary" data-action="ok">OK</button>
                </div>
            </div>
        `;
        document.body.appendChild(wrapper);

        const btnOk = wrapper.querySelector('[data-action="ok"]');
        const closeX = wrapper.querySelector('.modal-close-x');

        const cleanup = () => { try { wrapper.parentNode && wrapper.parentNode.removeChild(wrapper); } catch (e) {} };
        btnOk && btnOk.addEventListener('click', () => {
            const cb = wrapper.querySelector('#_tmp_alert_dont_remind');
            if (cb && cb.checked) {
                try { localStorage.setItem('hideConfigWarning', 'true'); } catch (e) {}
            }
            cleanup();
        });
        closeX && closeX.addEventListener('click', cleanup);
        wrapper.addEventListener('click', (e) => { if (e.target === wrapper) cleanup(); });
    } catch (e) {
        // Last resort fallback to console if DOM operations fail
        try { console.warn('Alert fallback failed:', e); } catch (ex) {}
    }
}


/**
 * Show a confirmation modal dialog
 * @param {string} message - Message to display
 * @param {string} title - Modal title (default 'Confirm Action')
 * @param {boolean} isDangerous - Whether this is a destructive action (affects button color)
 * @returns {Promise<boolean>} - Resolves to true if confirmed, false if cancelled
 */
function showConfirm(message, title = 'Confirm Action', isDangerous = false) {
    return new Promise((resolve) => {
        // Prevent re-entrancy / recursion: if a confirm modal is already open,
        // avoid opening another one. This guards against accidental re-binding
        // or nested calls that caused "too much recursion" in some pages.
        if (window.__pm_confirm_open) {
            // If a confirm is already open, create a small temporary async
            // confirm modal instead of using the native `window.confirm`.
            // This keeps behavior consistent (returns a Promise<boolean>). 
            createTemporaryConfirmModal(message, title, isDangerous).then(resolve).catch(() => resolve(false));
            return;
        }

        window.__pm_confirm_open = true;

        const modal = document.getElementById('confirm_modal');
        const titleEl = document.getElementById('confirm_modal_title');
        const messageEl = document.getElementById('confirm_modal_message');
        const confirmBtn = document.getElementById('confirm_modal_confirm');
        const cancelBtn = document.getElementById('confirm_modal_cancel');
        const closeX = document.getElementById('confirm_modal_close_x');
        
        if (!modal || !titleEl || !messageEl || !confirmBtn || !cancelBtn) {
            // Fallback: create a temporary confirm modal if the expected
            // DOM elements are not present. This avoids calling the native
            // window.confirm which produces a blocking native dialog.
            createTemporaryConfirmModal(message, title, isDangerous).then(resolve).catch(() => resolve(false));
            return;
        }
        
        // Set content
        titleEl.textContent = title;
        messageEl.textContent = message;
        
        // Style confirm button based on danger level
        confirmBtn.className = isDangerous ? 
            'modal-button modal-button-danger' : 
            'modal-button modal-button-primary';
        
        // Show modal
        modal.style.display = 'flex';
        
        // Handle confirm
        const onConfirm = () => {
            cleanup();
            resolve(true);
        };
        
        // Handle cancel
        const onCancel = () => {
            cleanup();
            resolve(false);
        };
        
        // Cleanup function
        const cleanup = () => {
            modal.style.display = 'none';
            confirmBtn.removeEventListener('click', onConfirm);
            cancelBtn.removeEventListener('click', onCancel);
            if (closeX) closeX.removeEventListener('click', onCancel);
            modal.removeEventListener('click', onBackdropClick);
            // Clear guard
            window.__pm_confirm_open = false;
        };
        
        // Backdrop click closes modal
        const onBackdropClick = (e) => {
            if (e.target === modal) {
                onCancel();
            }
        };
        
        // Attach event listeners
        confirmBtn.addEventListener('click', onConfirm);
        cancelBtn.addEventListener('click', onCancel);
        if (closeX) closeX.addEventListener('click', onCancel);
        modal.addEventListener('click', onBackdropClick);
    });
}

/**
 * Show an alert-style modal (no cancel button, just OK to dismiss)
 * @param {string} message - Message to display
 * @param {string} title - Modal title (default 'Notice')
 * @param {boolean} isDangerous - Whether to style as danger/warning
 * @param {boolean} showDontRemindCheckbox - Show "don't remind me" checkbox
 * @param {boolean} isHtml - Whether message contains HTML (default false)
 */
function showAlert(message, title = 'Notice', isDangerous = false, showDontRemindCheckbox = false, isHtml = false) {
    const modal = document.getElementById('confirm_modal');
    const titleEl = document.getElementById('confirm_modal_title');
    const messageEl = document.getElementById('confirm_modal_message');
    const confirmBtn = document.getElementById('confirm_modal_confirm');
    const cancelBtn = document.getElementById('confirm_modal_cancel');
    const closeX = document.getElementById('confirm_modal_close_x');
    
    if (!modal || !titleEl || !messageEl || !confirmBtn) {
        // Fallback to a temporary alert modal instead of native alert()
        createTemporaryAlertModal(message, title, showDontRemindCheckbox, isHtml);
        return;
    }
    
    // Set content
    titleEl.textContent = title;
    messageEl.style.whiteSpace = 'pre-wrap'; // Preserve line breaks
    
    if (isHtml) {
        messageEl.innerHTML = message;
    } else {
        messageEl.textContent = message;
    }
    
    // Add "Don't remind me" toggle if requested - insert into footer area
    let dontRemindCheckbox = null;
    const buttonsContainer = confirmBtn.parentElement;
    if (showDontRemindCheckbox && buttonsContainer) {
        // Create toggle element and insert before buttons
        const toggleWrapper = document.createElement('label');
        toggleWrapper.id = 'dont_remind_wrapper';
        toggleWrapper.style.cssText = 'display:flex;align-items:center;gap:8px;cursor:pointer;margin-right:auto;';
        toggleWrapper.innerHTML = `
            <input type="checkbox" id="dont_remind_checkbox" style="cursor:pointer;">
            <span style="font-size:13px;color:var(--muted);">Don't show this again</span>
        `;
        buttonsContainer.insertBefore(toggleWrapper, buttonsContainer.firstChild);
        dontRemindCheckbox = modal.querySelector('#dont_remind_checkbox');
    }
    
    // Style confirm button
    confirmBtn.textContent = 'OK';
    confirmBtn.className = isDangerous ? 
        'modal-button modal-button-danger' : 
        'modal-button modal-button-primary';
    
    // Hide cancel button for alert style
    if (cancelBtn) cancelBtn.style.display = 'none';
    
    // Show modal
    modal.style.display = 'flex';
    
    // Handle dismiss
    const onDismiss = () => {
        // Save preference if checkbox is checked
        if (dontRemindCheckbox && dontRemindCheckbox.checked) {
            localStorage.setItem('hideConfigWarning', 'true');
        }
        cleanup();
    };
    
    // Cleanup function
    const cleanup = () => {
        modal.style.display = 'none';
        if (cancelBtn) cancelBtn.style.display = ''; // Restore for future confirms
        messageEl.style.whiteSpace = ''; // Reset style
        messageEl.innerHTML = ''; // Clear HTML
        confirmBtn.textContent = 'Confirm'; // Reset text
        // Remove toggle wrapper if it was added
        const toggleWrapper = modal.querySelector('#dont_remind_wrapper');
        if (toggleWrapper) toggleWrapper.remove();
        confirmBtn.removeEventListener('click', onDismiss);
        if (closeX) closeX.removeEventListener('click', onDismiss);
        modal.removeEventListener('click', onBackdropClick);
    };
    
    // Backdrop click closes modal
    const onBackdropClick = (e) => {
        if (e.target === modal) {
            onDismiss();
        }
    };
    
    // Attach event listeners
    confirmBtn.addEventListener('click', onDismiss);
    if (closeX) closeX.addEventListener('click', onDismiss);
    modal.addEventListener('click', onBackdropClick);
}

// ============================================================================
// CLIPBOARD UTILITIES
// ============================================================================

/**
 * Copy text to clipboard with user feedback
 * @param {string} text - Text to copy
 */
function copyToClipboard(text, callback, successMessage) {
    // callback (optional) will be called with a boolean success flag
    if (!text) {
        if (typeof callback === 'function') callback(false);
        return Promise.resolve(false);
    }
    return navigator.clipboard.writeText(text).then(() => {
        try { showToast(successMessage || 'Copied to clipboard', 'success', 1500); } catch (e) {}
        if (typeof callback === 'function') callback(true);
        return true;
    }).catch(err => {
        try { window.__pm_shared.error('Failed to copy:', err); } catch (e) {}
        try { showToast('Failed to copy to clipboard', 'error'); } catch (e) {}
        if (typeof callback === 'function') callback(false);
        return false;
    });
}

// ============================================================================
// DATE/TIME FORMATTING
// ============================================================================

/**
 * Format a date string to local time display
 * @param {string} dateString - ISO date string
 * @returns {string} - Formatted date/time
 */
function formatDateTime(dateString) {
    if (!dateString) return 'Never';
    
    try {
        const date = new Date(dateString);
        if (isNaN(date.getTime())) return 'Invalid date';
        
        // Format as: "Nov 8, 2025 3:45 PM"
        return date.toLocaleString('en-US', {
            month: 'short',
            day: 'numeric',
            year: 'numeric',
            hour: 'numeric',
            minute: '2-digit',
            hour12: true
        });
    } catch (err) {
        window.__pm_shared.error('Date formatting error:', err);
        return dateString;
    }
}

/**
 * Show a prompt modal dialog and return the entered value or null if cancelled.
 * @param {string} message - Message to display
 * @param {string} defaultValue - Default input value
 * @param {string} title - Modal title
 * @returns {Promise<string|null>}
 */
function showPrompt(message, defaultValue = '', title = 'Input') {
    return new Promise((resolve) => {
        // Create modal dynamically if missing
        let modal = document.getElementById('prompt_modal');
        if (!modal) {
            modal = document.createElement('div');
            modal.id = 'prompt_modal';
            modal.className = 'modal-overlay';
            modal.style.display = 'none';
            modal.innerHTML = `
                <div class="modal-content" style="max-width:480px;">
                    <div class="modal-header">
                        <h3 class="modal-title" id="prompt_modal_title">${title}</h3>
                        <button class="modal-close-x" id="prompt_modal_close_x" title="Close">&times;</button>
                    </div>
                    <div class="modal-body">
                        <p id="prompt_modal_message">${message}</p>
                        <input id="prompt_modal_input" aria-labelledby="prompt_modal_message" class="modal-input" style="width:100%;padding:8px;margin-top:8px;" />
                    </div>
                    <div class="modal-footer">
                        <button class="modal-button modal-button-secondary" id="prompt_modal_cancel">Cancel</button>
                        <button class="modal-button modal-button-primary" id="prompt_modal_ok">OK</button>
                    </div>
                </div>
            `;
            document.body.appendChild(modal);

        }

        const titleEl = document.getElementById('prompt_modal_title');
        const messageEl = document.getElementById('prompt_modal_message');
        const inputEl = document.getElementById('prompt_modal_input');
        const okBtn = document.getElementById('prompt_modal_ok');
        const closeBtn = document.getElementById('prompt_modal_close_x');
        const cancelBtn = document.getElementById('prompt_modal_cancel');

        titleEl.textContent = title;
        messageEl.textContent = message;
        inputEl.value = defaultValue || '';
        inputEl.setAttribute('aria-labelledby', 'prompt_modal_message');
        if (closeBtn) closeBtn.setAttribute('aria-label', 'Close input dialog');

        let closed = false;
        const finalize = (result) => {
            if (closed) return;
            closed = true;
            modal.style.display = 'none';
            cleanup();
            resolve(result);
        };
        const onBackdropClick = (event) => {
            if (event.target === modal) {
                finalize(null);
            }
        };
        const onOk = () => finalize(inputEl.value);
        const onKeyDown = (event) => {
            if (event.key === 'Enter') {
                event.preventDefault();
                onOk();
            }
        };
        const onDismiss = () => finalize(null);

        function cleanup() {
            try { okBtn.removeEventListener('click', onOk); } catch (e) {}
            try { inputEl.removeEventListener('keydown', onKeyDown); } catch (e) {}
            try { modal.removeEventListener('click', onBackdropClick); } catch (e) {}
            if (closeBtn) { try { closeBtn.removeEventListener('click', onDismiss); } catch (e) {} }
            if (cancelBtn) { try { cancelBtn.removeEventListener('click', onDismiss); } catch (e) {} }
        }

        okBtn.addEventListener('click', onOk);
        inputEl.addEventListener('keydown', onKeyDown);
        modal.addEventListener('click', onBackdropClick);
        if (closeBtn) closeBtn.addEventListener('click', onDismiss);
        if (cancelBtn) cancelBtn.addEventListener('click', onDismiss);

        // Show modal and focus input
        modal.style.display = 'flex';
        setTimeout(() => { if (!closed) { inputEl.focus(); inputEl.select(); } }, 10);
    });
}

/**
 * Compatibility wrapper used by older code paths that call showInputModal(title, label, default)
 * Maps the parameters to showPrompt(label, defaultValue, title) so existing callers work.
 * @returns {Promise<string|null>}
 */
function showInputModal(title, label, defaultValue = '') {
    // Note: showPrompt signature is (message, defaultValue, title)
    return showPrompt(label, defaultValue, title);
}

/**
 * Agent-only saveDiscoveredDevice delegate.
 * The full implementation now lives in the agent bundle. This thin wrapper
 * delegates to the agent-provided implementation when available. This keeps
 * `common/web/shared.js` lightweight while preserving backwards compatibility
 * for callers that still invoke `window.__pm_shared.saveDiscoveredDevice`.
 */
async function saveDiscoveredDevice(ipOrSerial, autosave = false, updateUI = true) {
    try {
        if (typeof window.__agent_saveDiscoveredDevice === 'function') {
            return await window.__agent_saveDiscoveredDevice(ipOrSerial, autosave, updateUI);
        }
        window.__pm_shared.warn('saveDiscoveredDevice called but agent implementation missing');
        return Promise.reject(new Error('agent saveDiscoveredDevice not available'));
    } catch (e) {
        window.__pm_shared.error('saveDiscoveredDevice wrapper failed', e);
        throw e;
    }
}

// Expose a delegating shim so legacy callers continue to work.
window.saveDiscoveredDevice = window.saveDiscoveredDevice || saveDiscoveredDevice;
window.__pm_shared.saveDiscoveredDevice = window.__pm_shared.saveDiscoveredDevice || saveDiscoveredDevice;

// Lightweight helper shims to ensure shared code has minimal implementations
function makeClipboardIcon() {
    return '<svg class="clipboard-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">' +
        '<rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect>' +
        '<path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path>' +
        '</svg>';
}

function showWebUIModal(webUIURL, serial) {
    // Simple default: open in new tab if URL present
    if (!webUIURL) return;
    try { window.open(webUIURL, '_blank'); } catch (e) { window.__pm_shared.warn('showWebUIModal fallback failed', e); }
}



async function deleteSavedDevice(serial) {
    if (!serial) return;
    const confirmed = await window.__pm_shared.showConfirm('Delete this device? This will remove it from the database.', 'Delete Device', true);
    if (!confirmed) return;
    try {
        const r = await fetch('/devices/delete', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ serial }) });
        if (!r.ok) throw new Error(await r.text());
        window.__pm_shared.showToast('Device deleted successfully', 'success');
        // Animate removal of the corresponding card (if present) to avoid
        // an instant snap. The CSS class 'removing' triggers the exit
        // animation; wait for animationend before updating the UI.
        try {
            // Try to find by explicit data-serial or fallback to data-device-key
            // Escape backslashes before quotes, otherwise the backslash inserted
            // to escape a quote would itself get re-escaped by the next replace.
            const esc = (s) => (s || '').replace(/\\/g, '\\\\').replace(/"/g, '\\"');
            let card = document.querySelector('.saved-device-card[data-serial="' + esc(serial) + '"]');
            if (!card) card = document.querySelector('.saved-device-card[data-device-key="' + esc(serial) + '"]');
            if (card) {
                card.classList.add('removing');
                // Use animationend to detect completion; fall back to timeout
                let handled = false;
                const onEnd = (ev) => {
                    if (handled) return;
                    handled = true;
                    try { card.remove(); } catch (e) {}
                    try { if (typeof updatePrinters === 'function') updatePrinters(); } catch (e) {}
                    card.removeEventListener('animationend', onEnd);
                    card.removeEventListener('transitionend', onEnd);
                };
                card.addEventListener('animationend', onEnd);
                card.addEventListener('transitionend', onEnd);
                // safety fallback in case events don't fire
                setTimeout(onEnd, 600);
            } else {
                if (typeof updatePrinters === 'function') try { updatePrinters(); } catch (e) {}
            }
        } catch (e) {
            try { if (typeof updatePrinters === 'function') updatePrinters(); } catch (er) {}
        }
    } catch (e) {
        window.__pm_shared.error('deleteSavedDevice failed', e);
        window.__pm_shared.showToast('Failed to delete device', 'error');
    }
}

async function editField(serial, fieldName, currentValue, element) {
    if (!serial || !fieldName) return;
    try {
        const newValue = await window.__pm_shared.showPrompt('Enter new value for ' + fieldName + ':', currentValue || '');
        if (newValue === null) return;
        const r = await fetch('/devices/update', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ serial, [fieldName]: newValue }) });
        if (!r.ok) throw new Error(await r.text());
        window.__pm_shared.showToast('Field updated', 'success');
        // Refresh UI element if provided
        if (element && element.nodeType === 1) element.textContent = newValue;
    } catch (e) {
        window.__pm_shared.error('editField failed', e);
        window.__pm_shared.showToast('Failed to update field', 'error');
    }
}

function applyMasonryLayout(targetGrid) {
    // No-op safe fallback for layouts; consumers may implement real layout
    return;
}

function showDeviceMetricsModal(serial, preset) {
    if (!serial) return;
    try {
        // If the shared metrics modal exists, reuse it
    if (typeof window.__pm_shared_metrics?.loadDeviceMetrics === 'function') {
            // If a dedicated modal creator is available, prefer it — it will
            // create the modal DOM if needed and handle rendering. This keeps
            // the shared logic robust across different UI entrypoints.
            if (typeof window.showMetricsModal === 'function') {
                try { window.showMetricsModal({ serial, preset }); return; } catch (e) { /* fallthrough */ }
            }

            // If modal elements exist in DOM, show modal and render
            const modalBody = document.getElementById('metrics_modal_body') || document.getElementById('metrics_content');
            if (modalBody) {
                // ensure the modal is visible in page (agent/server will style appropriately)
                const modal = document.getElementById('printer_details_overlay') || document.getElementById('metrics_modal');
                if (modal) try { modal.style.display = 'flex'; } catch (e) {}
                window.__pm_shared_metrics.loadDeviceMetrics(serial, modalBody.id || 'metrics_content');
            } else {
                // Fallback: open metrics page/tab
                window.location.hash = '#metrics';
                setTimeout(() => { try { window.__pm_shared_metrics.loadDeviceMetrics(serial); } catch (e) {} }, 200);
            }
        } else {
            window.__pm_shared.showToast('Metrics UI not available', 'error');
        }
    } catch (e) { window.__pm_shared.warn('showDeviceMetricsModal failed', e); }
}

// Export shims to global namespace
window.makeClipboardIcon = window.makeClipboardIcon || makeClipboardIcon;
window.showWebUIModal = window.showWebUIModal || showWebUIModal;
window.showPrinterDetails = window.showPrinterDetails || showPrinterDetails;
window.deleteSavedDevice = window.deleteSavedDevice || deleteSavedDevice;
window.editField = window.editField || editField;
window.applyMasonryLayout = window.applyMasonryLayout || applyMasonryLayout;
window.showDeviceMetricsModal = window.showDeviceMetricsModal || showDeviceMetricsModal;

window.__pm_shared.makeClipboardIcon = window.__pm_shared.makeClipboardIcon || makeClipboardIcon;
window.__pm_shared.showWebUIModal = window.__pm_shared.showWebUIModal || showWebUIModal;
window.__pm_shared.showPrinterDetails = window.__pm_shared.showPrinterDetails || showPrinterDetails;
window.__pm_shared.deleteSavedDevice = window.__pm_shared.deleteSavedDevice || deleteSavedDevice;
window.__pm_shared.editField = window.__pm_shared.editField || editField;
window.__pm_shared.applyMasonryLayout = window.__pm_shared.applyMasonryLayout || applyMasonryLayout;
window.__pm_shared.showDeviceMetricsModal = window.__pm_shared.showDeviceMetricsModal || showDeviceMetricsModal;

/**
 * Format a date string as relative time (e.g., "2 minutes ago")
 * @param {string} dateString - ISO date string
 * @returns {string} - Relative time string
 */
function formatRelativeTime(dateString) {
    if (!dateString) return 'Never';
    
    try {
        const date = new Date(dateString);
        if (isNaN(date.getTime())) return 'Invalid date';
        
        const now = new Date();
        const seconds = Math.floor((now - date) / 1000);
        
        if (seconds < 60) return 'Just now';
        const minutes = Math.floor(seconds / 60);
        if (minutes < 60) return `${minutes} minute${minutes !== 1 ? 's' : ''} ago`;
        const hours = Math.floor(minutes / 60);
        if (hours < 24) return `${hours} hour${hours !== 1 ? 's' : ''} ago`;
        const days = Math.floor(hours / 24);
        if (days < 30) return `${days} day${days !== 1 ? 's' : ''} ago`;
        const months = Math.floor(days / 30);
        if (months < 12) return `${months} month${months !== 1 ? 's' : ''} ago`;
        const years = Math.floor(months / 12);
        return `${years} year${years !== 1 ? 's' : ''} ago`;
    } catch (err) {
        window.__pm_shared.error('Relative time formatting error:', err);
        return dateString;
    }
}

// ============================================================================
// Database backend field toggles (shared)
// Moved here so Agent and Server UIs reuse the same behavior without
// duplicating the logic in each bundle.
function toggleDatabaseFields() {
    const selector = document.getElementById('db_backend_type');
    if (!selector) return;

    const selectedBackend = selector.value;

    // Hide all backend field groups
    const allFieldGroups = [
        'db_sqlite_fields',
        'db_postgresql_fields',
        'db_mysql_fields',
        'db_mssql_fields',
        'db_mongodb_fields'
    ];
    allFieldGroups.forEach(id => {
        const el = document.getElementById(id);
        if (el) el.style.display = 'none';
    });

    // Show the selected backend's fields
    const targetId = 'db_' + selectedBackend + '_fields';
    const targetEl = document.getElementById(targetId);
    if (targetEl) targetEl.style.display = 'flex';

    // Show/hide clear database section (SQLite only)
    const clearSection = document.getElementById('clear_db_section');
    if (clearSection) {
        clearSection.style.display = (selectedBackend === 'sqlite') ? 'block' : 'none';
    }
}

// Export to namespaced shared API
window.__pm_shared = window.__pm_shared || {};
window.__pm_shared.toggleDatabaseFields = toggleDatabaseFields;

// Ensure a safe delegator for autosave UI toggle exists. Some bundles
// (older or compiled server bundles) call `toggleAutosaveUI()` while the
// authoritative implementation lives in the agent bundle as `toggleAutoSave()`.
// Provide a delegator that prefers the agent implementation and falls back
// to a minimal, safe UI update to avoid ReferenceError exceptions.
function toggleAutosaveUI() {
    try {
        // Prefer the (newer) agent implementation if available
        if (typeof toggleAutoSave === 'function') {
            return toggleAutoSave();
        }
        if (window.__pm_shared && typeof window.__pm_shared.toggleAutoSave === 'function') {
            return window.__pm_shared.toggleAutoSave();
        }

        // Minimal safe fallback: update visible controls and localStorage
        const settingsCheckbox = document.getElementById('settings_autosave');
        const buttonsDiv = document.getElementById('settings_buttons');
        const advancedLabel = document.getElementById('advanced_toggle_label');
        if (!settingsCheckbox) return;

        const enabled = settingsCheckbox.checked;
        if (enabled) {
            if (buttonsDiv) buttonsDiv.style.display = 'none';
            if (advancedLabel) advancedLabel.style.marginLeft = 'auto';
            localStorage.setItem('settings_autosave', 'true');
            // No-op for handlers: agent bundle will attach real handlers when loaded
        } else {
            if (buttonsDiv) buttonsDiv.style.display = 'flex';
            if (advancedLabel) advancedLabel.style.marginLeft = '12px';
            localStorage.setItem('settings_autosave', 'false');
        }
    } catch (e) {
        try { window.__pm_shared.warn('toggleAutosaveUI fallback failed', e); } catch (e2) {}
    }
}

// Export aliases so callers don't get ReferenceError
window.toggleAutosaveUI = window.toggleAutosaveUI || toggleAutosaveUI;
window.__pm_shared.toggleAutosaveUI = window.__pm_shared.toggleAutosaveUI || toggleAutosaveUI;

// Convenience wrappers for agent/device UI actions so callers can call the
// namespaced API directly without fallbacks. These implement minimal
// behaviors used by the UI (open remote UIs, show metrics modal, fetch
// agent details, or delete an agent via API).
window.__pm_shared.openAgentUI = function (agentId) {
    if (!agentId) return;
    try { window.open('/api/v1/proxy/agent/' + encodeURIComponent(agentId) + '/', '_blank'); } catch (e) { /* best-effort */ }
};

window.__pm_shared.openDeviceUI = function (serial) {
    if (!serial) return;
    try { window.open('/proxy/' + encodeURIComponent(serial) + '/', '_blank'); } catch (e) { /* best-effort */ }
};

window.__pm_shared.openDeviceMetrics = function (serial) {
    if (!serial) return;
    // Delegate to the shared metrics modal loader
    try { window.__pm_shared.showDeviceMetricsModal(serial); } catch (e) { /* best-effort */ }
};

window.__pm_shared.viewAgentDetails = async function (agentId) {
    if (!agentId) return;
    // If the page has an agent modal, populate it, otherwise open agents tab
    try {
        const modal = document.getElementById('agent_details_modal');
        const body = document.getElementById('agent_details_body');
        const title = document.getElementById('agent_details_title');
        if (modal && body && title) {
            modal.style.display = 'flex';
            title.textContent = 'Agent Details';
            body.innerHTML = '<div style="color:var(--muted);text-align:center;padding:20px;">Loading agent details...</div>';
            const res = await fetch('/api/v1/agents/' + encodeURIComponent(agentId));
            if (!res.ok) { body.innerHTML = '<div style="color:var(--muted);padding:12px">Failed to load agent details</div>'; return; }
            const agent = await res.json();
            body.innerHTML = '<pre style="white-space:pre-wrap;word-break:break-word">' + JSON.stringify(agent, null, 2) + '</pre>';
            return;
        }
        // Fallback: open agents tab
        window.location.hash = '#agents';
    } catch (e) {
        try { window.__pm_shared.error('viewAgentDetails failed', e); } catch (e2) {}
    }
};

window.__pm_shared.deleteAgent = async function (agentId, agentName) {
    if (!agentId) return;
    // Confirm then delete via API
    const ok = await window.__pm_shared.showConfirm('Delete agent ' + (agentName || agentId) + '?', 'Delete Agent', true);
    if (!ok) return;
    try {
        const res = await fetch('/api/v1/agents/' + encodeURIComponent(agentId), { method: 'DELETE' });
        if (!res.ok) {
            const txt = await res.text().catch(() => res.statusText || 'error');
            window.__pm_shared.showToast('Failed to delete agent: ' + txt, 'error');
            return;
        }
        window.__pm_shared.showToast('Agent deleted', 'success');
        // Trigger a refresh if function exists
        if (typeof loadAgents === 'function') try { loadAgents(); } catch (e) {}
    } catch (e) {
        window.__pm_shared.showToast('Failed to delete agent', 'error');
    }
};

// ============================================================================
// NUMBER FORMATTING
// ============================================================================

/**
 * Format a number with thousand separators
 * @param {number} num - Number to format
 * @returns {string} - Formatted number (e.g., "1,234,567")
 */
function formatNumber(num) {
    if (num === null || num === undefined) return '0';
    return num.toLocaleString('en-US');
}

/**
 * Format bytes as human-readable size
 * @param {number} bytes - Number of bytes
 * @param {number} decimals - Number of decimal places (default 2)
 * @returns {string} - Formatted size (e.g., "1.5 MB")
 */
function formatBytes(bytes, decimals = 2) {
    if (bytes === 0) return '0 Bytes';
    
    const k = 1024;
    const dm = decimals < 0 ? 0 : decimals;
    const sizes = ['Bytes', 'KB', 'MB', 'GB', 'TB'];
    
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    
    return parseFloat((bytes / Math.pow(k, i)).toFixed(dm)) + ' ' + sizes[i];
}

// ============================================================================
// DEBOUNCE UTILITY
// ============================================================================

/**
 * Debounce a function call
 * @param {Function} func - Function to debounce
 * @param {number} wait - Delay in milliseconds
 * @returns {Function} - Debounced function
 */
function debounce(func, wait) {
    let timeout;
    return function executedFunction(...args) {
        const later = () => {
            clearTimeout(timeout);
            func(...args);
        };
        clearTimeout(timeout);
        timeout = setTimeout(later, wait);
    };
}

// If running under CommonJS (tests / Node), prefer the testable helpers
// from `shared_helpers.js` to avoid duplicating pure logic and to ensure
// unit-tested implementations are used during tests. In the browser this
// `require` will be unavailable and we fall back to the in-file functions.
try {
    if (typeof module !== 'undefined' && module.exports && typeof require === 'function') {
        const h = require('./shared_helpers');
        if (h) {
            // Override the local helper implementations with the tested ones
            try { formatDateTime = h.formatDateTime || formatDateTime; } catch (e) {}
            try { formatRelativeTime = h.formatRelativeTime || formatRelativeTime; } catch (e) {}
            try { formatNumber = h.formatNumber || formatNumber; } catch (e) {}
            try { formatBytes = h.formatBytes || formatBytes; } catch (e) {}
            try { debounce = h.debounce || debounce; } catch (e) {}
        }
    }
} catch (e) {
    try { console.warn('Failed to load shared_helpers for test environment', e); } catch (e2) {}
}

// Export shared helpers under a single namespaced object to avoid global
// symbol collisions when agent and server bundles are loaded together (proxied).
// Consumers should prefer `window.__pm_shared.<fn>` instead of global functions.
(function exportSharedNamespace() {
    try {
        if (typeof window === 'undefined') return;
        window.__pm_shared = window.__pm_shared || {};
        // Export commonly used helpers
        window.__pm_shared.showToast = window.__pm_shared.showToast || showToast;
        window.__pm_shared.showConfirm = window.__pm_shared.showConfirm || showConfirm;
        window.__pm_shared.showAlert = window.__pm_shared.showAlert || showAlert;
        window.__pm_shared.copyToClipboard = window.__pm_shared.copyToClipboard || copyToClipboard;
        window.__pm_shared.formatDateTime = window.__pm_shared.formatDateTime || formatDateTime;
        window.__pm_shared.formatRelativeTime = window.__pm_shared.formatRelativeTime || formatRelativeTime;
        window.__pm_shared.formatNumber = window.__pm_shared.formatNumber || formatNumber;
        window.__pm_shared.formatBytes = window.__pm_shared.formatBytes || formatBytes;
        window.__pm_shared.debounce = window.__pm_shared.debounce || debounce;
    window.__pm_shared.showPrompt = window.__pm_shared.showPrompt || showPrompt;
        // Note: we intentionally do NOT overwrite global `window.showToast` etc.
        // This avoids clobbering other bundles and makes callers opt-in to the
        // shared namespace for safer cross-bundle interaction.
    } catch (e) {
        // Non-fatal: best-effort namespacing
        try { window.__pm_shared.warn('Failed to export __pm_shared namespace', e); } catch (e2) {}
    }
})();

// Backwards-compatible global exports removed. Consumers should use the
// namespaced `window.__pm_shared` API (e.g. window.__pm_shared.showToast)
// to avoid global symbol collisions when agent and server bundles are
// loaded together via proxy.

// ============================================================================
// SHARED METRICS MODAL
// ============================================================================
/**
 * Show a shared metrics modal.
 * Options:
 *  - serial: device serial (optional)
 *  - onFetch: async function({serial, from, to}) -> { data: [...] }
 */
window.showMetricsModal = async function (opts = {}) {
    opts = opts || {};
    const serial = opts.serial || null;
    const onFetch = opts.onFetch || (async ({serial, from, to}) => {
        // default fetch uses server API if available
        const qs = [];
        if (serial) qs.push('serial=' + encodeURIComponent(serial));
        if (from) qs.push('from=' + encodeURIComponent(new Date(from).toISOString()));
        if (to) qs.push('to=' + encodeURIComponent(new Date(to).toISOString()));
        const url = '/api/devices/metrics/history' + (qs.length ? ('?' + qs.join('&')) : '');
        const res = await fetch(url);
        if (!res.ok) throw new Error('Failed to fetch metrics');
        return res.json();
    });

    // Use the shared metrics bundle loader directly (fail-fast if not present)
    if (typeof window.__pm_shared_metrics?.loadDeviceMetrics === 'function') {
        // Create a modal container that the shared metrics loader understands
        let modal = document.getElementById('metrics_modal');
        if (!modal) {
            modal = document.createElement('div');
            modal.id = 'metrics_modal';
            modal.className = 'modal';
            modal.style.display = 'none';
            modal.innerHTML = `
                <div class="modal-content" style="max-width:900px;">
                    <div class="modal-header">
                        <span class="modal-title" id="metrics_modal_title">Metrics</span>
                        <button type="button" class="modal-close" id="metrics_modal_close_x" aria-label="Close metrics">&times;</button>
                    </div>
                    <div class="modal-body" id="metrics_modal_body" style="max-height:60vh;overflow:auto;padding:16px;">
                        <div id="metrics_modal_content" style="font-size:13px;color:var(--muted)">Loading...</div>
                    </div>
                    <div class="modal-footer">
                        <button class="modal-button modal-button-secondary" id="metrics_modal_close">Close</button>
                    </div>
                </div>
            `;
            document.body.appendChild(modal);

            // close handlers
            modal.querySelector('#metrics_modal_close_x').addEventListener('click', () => modal.style.display = 'none');
            modal.querySelector('#metrics_modal_close').addEventListener('click', () => modal.style.display = 'none');
            modal.addEventListener('click', (e) => { if (e.target === modal) modal.style.display = 'none'; });
        }

        const titleEl = document.getElementById('metrics_modal_title');
        const contentEl = document.getElementById('metrics_modal_content');
        titleEl.textContent = serial ? `Metrics: ${serial}` : 'Metrics';
        contentEl.innerHTML = '<div style="color:var(--muted);">Loading metrics...</div>';

        // Show modal then delegate rendering to the shared metrics loader
        modal.style.display = 'flex';
        try {
            // Prefer the explicit exported shared implementation if present
            const loader = (window.__pm_shared_metrics && window.__pm_shared_metrics.loadDeviceMetrics) ? window.__pm_shared_metrics.loadDeviceMetrics : window.loadDeviceMetrics;
            // Call loader to render the full metrics UI into the modal content
            // Use a short timeout so the modal becomes visible before heavy work
            setTimeout(() => {
                Promise.resolve(loader(serial, 'metrics_modal_content', { preset: opts.preset }))
                    .catch(e => window.__pm_shared.error('Metrics modal loading failed', { serial, error: e }));
            }, 60);
        } catch (e) {
            window.__pm_shared.warn('Failed to invoke shared metrics loader', e);
        }

        return; // done
    }

    // Initialize flatpickr on range input if available
    try {
        if (typeof flatpickr === 'function') {
            // If already initialized, destroy first (safety)
            if (rangeInput._flatpickr) rangeInput._flatpickr.destroy();
            const fpInstance = flatpickr(rangeInput, {
                mode: 'range',
                enableTime: true,
                dateFormat: 'Y-m-d H:i',
                defaultDate: [new Date(Date.now() - 7 * 24 * 3600 * 1000), new Date()],
            });
            // Expose a lightweight handle for callers that expect a global reference
            window.metricsDataRange = window.metricsDataRange || { min: null, max: null, serial: serial, flatpickr: null };
            try { window.metricsDataRange.flatpickr = fpInstance; } catch (e) { /* ignore */ }

            // Honor preset option (e.g., '7day') by setting a sensible selection
            if (opts && opts.preset === '7day') {
                try {
                    const maxTime = new Date();
                    const start = new Date(maxTime.getTime() - 7 * 24 * 60 * 60 * 1000);
                    if (typeof fpInstance.setDate === 'function') fpInstance.setDate([start, maxTime], true);
                } catch (e) { /* ignore preset failure */ }
            }
        }
    } catch (e) {
        window.__pm_shared.warn('flatpickr init failed in shared metrics modal', e);
    }

    async function doLoad() {
        contentEl.innerHTML = '<div style="color:var(--muted);">Loading metrics...</div>';
        let from = null, to = null;
        try {
            const val = rangeInput.value || '';
            if (val.includes(' to ')) {
                const parts = val.split(' to ');
                from = new Date(parts[0]);
                to = new Date(parts[1]);
            } else if (val) {
                from = new Date(val);
                to = new Date();
            }
        } catch (e) { /* ignore parse errors */ }

        try {
            const data = await onFetch({ serial, from, to });
            // If API returned array directly, use it
            let items = data;
            if (data && data.history) items = data.history;
            if (!items || items.length === 0) {
                contentEl.innerHTML = '<div style="color:var(--muted);">No metrics available for selected range.</div>';
                return;
            }

            // Render simple list
            let html = '<div style="display:flex;flex-direction:column;gap:8px;">';
            items.forEach(it => {
                const t = it.timestamp ? new Date(it.timestamp).toLocaleString() : 'N/A';
                html += `<div style="padding:8px;background:rgba(0,0,0,0.03);border-radius:6px;"><strong>${it.serial || it.Serial || ''}</strong> — ${t} — pages: ${it.page_count || it.pageCount || 'n/a'}</div>`;
            });
            html += '</div>';
            contentEl.innerHTML = html;
        } catch (err) {
            window.__pm_shared.error('Metrics fetch failed', err);
            contentEl.innerHTML = `<div style="color:var(--error);">Failed to load metrics: ${typeof escapeHtml === 'function' ? escapeHtml(err.message || err) : String(err.message || err)}</div>`;
        }
    }

    refreshBtn.addEventListener('click', doLoad);

    // show modal and trigger initial load
    modal.style.display = 'flex';
    setTimeout(doLoad, 50);
};
