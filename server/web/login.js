(function(){
    if (typeof window === 'undefined' || typeof document === 'undefined') {
        return;
    }

    const params = new URLSearchParams(window.location.search);
    const tenantParam = (params.get('tenant') || '').trim();
    const redirectTarget = params.get('redirect') || '/';
    const skipAuto = ['1','true','yes','on'].includes((params.get('no_auto') || '').toLowerCase());
    const errorCode = params.get('error') || '';

    const errorMessages = {
        oidc_invalid: 'The sign-in response was incomplete. Please try again.',
        oidc_state: 'The sign-in request expired. Start again from this page.',
        oidc_provider: 'The selected identity provider is no longer available.',
        oidc_discovery: 'Unable to reach the identity provider. Please try again.',
        oidc_exchange: 'Failed to exchange the sign-in code. Try again or use another method.',
        oidc_token: 'The identity provider did not return a valid token.',
        oidc_verify: 'The identity token could not be verified.',
        oidc_nonce: 'The sign-in session is no longer valid. Try again.',
        oidc_claims: 'Could not read your profile information from the identity provider.',
        oidc_user: 'We could not create or find a user for this identity.',
        oidc_session: 'We could not establish a session. Please try again.',
        oidc_agent: 'Access to the requested agent was denied. Start again from the agent.',
    };

    const elementIds = {
        error: 'login_error',
        loading: 'auth_loading',
        localSection: 'local_login_section',
        ssoSection: 'sso_section',
        ssoButtons: 'sso_buttons',
        noOptions: 'no_options_message',
        autoHint: 'auto_login_hint',
        skipAutoLink: 'skip_auto_link',
        tenantBanner: 'tenant_detected_banner',
        lookupSection: 'tenant_lookup_section',
        lookupInput: 'tenant_lookup_input',
        lookupButton: 'tenant_lookup_button',
        lookupStatus: 'tenant_lookup_status',
    };

    const elements = new Proxy({}, {
        get(_, prop) {
            const id = elementIds[prop];
            if (!id || typeof document === 'undefined') {
                return null;
            }
            return document.getElementById(id);
        }
    });

    let tenantLookupInitialized = false;

    // Only allow navigation to same-page-relative paths or http(s) URLs (e.g. an
    // agent's own web UI, which legitimately lives on a different origin). This
    // blocks javascript:/data:/vbscript: URIs, which would otherwise execute
    // arbitrary script when assigned to window.location (DOM XSS), and rejects
    // control characters some browsers normalize into protocol-relative URLs
    // (open redirect), e.g. "/\t/evil.com" -> "//evil.com".
    function isSafeNavigationTarget(target){
        if(typeof target !== 'string' || target === ''){
            return false;
        }
        for(let i = 0; i < target.length; i++){
            const code = target.charCodeAt(i);
            if(code <= 0x1f || code === 0x7f){
                return false;
            }
        }
        if(target.startsWith('/') && !target.startsWith('//') && !target.startsWith('/\\')){
            return true;
        }
        try{
            const parsed = new URL(target, window.location.origin);
            return parsed.protocol === 'http:' || parsed.protocol === 'https:';
        }catch(e){
            return false;
        }
    }

    function navigateTo(target){
        if(!isSafeNavigationTarget(target)){
            target = '/';
        }
        if(typeof window.__pmLoginNavigate === 'function'){
            window.__pmLoginNavigate(target);
            return;
        }
        window.location = target;
    }

    function showError(message){
        if(!elements.error) return;
        elements.error.textContent = message;
        elements.error.style.display = 'block';
    }

    async function loadAuthOptions(){
        const query = new URLSearchParams();
        if(tenantParam){
            query.set('tenant', tenantParam);
        }
        try{
            const res = await fetch('/api/v1/auth/options' + (query.toString() ? ('?' + query.toString()) : ''));
            if(!res.ok){
                throw new Error('Unable to load sign-in options.');
            }
            const data = await res.json();
            renderOptions(data || {});
        }catch(err){
            showError(err && err.message ? err.message : 'Unable to load sign-in options.');
            if(elements.noOptions){
                elements.noOptions.style.display = 'block';
            }
        }finally{
            if(elements.loading){
                elements.loading.style.display = 'none';
            }
        }
    }

    function renderOptions(opts){
        const providers = Array.isArray(opts.providers) ? opts.providers : [];
        const localEnabled = !!opts.local_login;

        if(localEnabled && elements.localSection){
            elements.localSection.style.display = 'block';
        }

        if(providers.length > 0 && elements.ssoSection && elements.ssoButtons){
            elements.ssoSection.style.display = 'block';
            elements.ssoButtons.innerHTML = '';
            providers.forEach(p => elements.ssoButtons.appendChild(createProviderButton(p)));
        }

        if(!localEnabled && providers.length === 0 && elements.noOptions){
            elements.noOptions.style.display = 'block';
        }

        updateTenantBanner(opts);
        maybeShowTenantLookup(providers, opts);
        maybeAutoLogin(providers, opts);
    }

    function createProviderButton(provider){
        const btn = document.createElement('button');
        btn.type = 'button';
        btn.className = 'modal-button modal-button-primary';
        btn.style.display = 'flex';
        btn.style.alignItems = 'center';
        btn.style.justifyContent = 'center';
        btn.style.gap = '6px';
        btn.style.fontWeight = '600';
        btn.dataset.slug = provider.slug;
        btn.textContent = provider.button_text || provider.display_name || 'Continue';

        if(provider.button_style){
            btn.className += ' ' + provider.button_style;
        }

        if(provider.icon){
            const icon = document.createElement('img');
            icon.src = provider.icon;
            icon.alt = '';
            icon.style.width = '18px';
            icon.style.height = '18px';
            icon.style.objectFit = 'contain';
            btn.prepend(icon);
        }

        btn.addEventListener('click', () => startOIDC(provider.slug));
        return btn;
    }

    function maybeAutoLogin(providers, opts){
        if(!providers || providers.length === 0){
            return;
        }
        const autoProvider = providers.find(p => p && p.auto_login);
        if(!autoProvider || skipAuto || errorCode){
            return;
        }
        if(elements.autoHint){
            elements.autoHint.style.display = 'block';
            if(elements.skipAutoLink){
                const qp = new URLSearchParams(window.location.search);
                qp.set('no_auto', '1');
                elements.skipAutoLink.href = window.location.pathname + '?' + qp.toString();
            }
        }
        startOIDC(autoProvider.slug);
    }

    function updateTenantBanner(opts){
        if(!elements.tenantBanner){
            return;
        }
        const tenantId = opts && (opts.tenant_id || opts.tenantId);
        if(tenantId){
            const name = (opts && (opts.tenant_name || opts.tenantName)) || tenantId;
            const source = describeTenantSource(opts && opts.tenant_source);
            let message = `Showing sign-in options for ${name}.`;
            if(source){
                message += ` (Detected via ${source}.)`;
            }
            elements.tenantBanner.textContent = message;
            elements.tenantBanner.style.display = 'block';
        }else{
            elements.tenantBanner.style.display = 'none';
        }
    }

    function describeTenantSource(source){
        switch((source || '').toLowerCase()){
            case 'query':
                return 'direct link';
            case 'domain_hint':
                return 'domain hint';
            case 'login_hint':
                return 'login hint';
            case 'host':
                return 'site host';
            case 'forwarded_host':
                return 'proxy host';
            case 'cookie':
                return 'previous login';
            case 'lookup':
                return 'email lookup';
            default:
                return '';
        }
    }

    function maybeShowTenantLookup(providers, opts){
        if(!elements.lookupSection){
            return;
        }
        const providerCount = Array.isArray(providers) ? providers.length : 0;
        const resolvedTenant = opts && (opts.tenant_id || opts.tenantId);
        const shouldShow = providerCount > 1 && !tenantParam && !resolvedTenant;
        if(shouldShow){
            elements.lookupSection.style.display = 'block';
            initTenantLookup();
        }else{
            elements.lookupSection.style.display = 'none';
            setLookupStatus('', false);
        }
    }

    function initTenantLookup(){
        if(tenantLookupInitialized){
            return;
        }
        tenantLookupInitialized = true;
        if(elements.lookupButton){
            elements.lookupButton.addEventListener('click', submitTenantLookup);
        }
        if(elements.lookupInput){
            elements.lookupInput.addEventListener('keypress', function(ev){
                if(ev.key === 'Enter'){
                    submitTenantLookup();
                }
            });
        }
    }

    async function submitTenantLookup(){
        if(!elements.lookupInput){
            return;
        }
        const hint = (elements.lookupInput.value || '').trim();
        if(!hint){
            setLookupStatus('Enter your work email first.', true);
            return;
        }
        setLookupStatus('Looking up your organization…', false);
        setLookupPending(true);
        try{
            const res = await fetch('/api/v1/auth/tenant-lookup', {
                method: 'POST',
                headers: {'content-type': 'application/json'},
                body: JSON.stringify({hint})
            });
            if(!res.ok){
                throw new Error('Lookup failed. Try again in a moment.');
            }
            const data = await res.json();
            if(data && data.match && data.tenant_id){
                const qp = new URLSearchParams(window.location.search);
                qp.set('tenant', data.tenant_id);
                if(redirectTarget){
                    qp.set('redirect', redirectTarget);
                }
                navigateTo(window.location.pathname + '?' + qp.toString());
                return;
            }
            setLookupStatus('We could not find an organization for that email domain. Double-check the spelling or contact your administrator.', true);
        }catch(err){
            const message = (err && err.message) ? err.message : 'Lookup failed. Try again in a moment.';
            setLookupStatus(message, true);
        }finally{
            setLookupPending(false);
        }
    }

    function setLookupStatus(message, isError){
        if(!elements.lookupStatus){
            return;
        }
        if(!message){
            elements.lookupStatus.style.display = 'none';
            return;
        }
        elements.lookupStatus.textContent = message;
        elements.lookupStatus.style.display = 'block';
        elements.lookupStatus.style.color = isError ? 'var(--danger)' : 'var(--muted)';
    }

    function setLookupPending(isPending){
        if(elements.lookupButton){
            elements.lookupButton.disabled = !!isPending;
        }
        if(elements.lookupInput){
            elements.lookupInput.disabled = !!isPending;
        }
    }

    function startOIDC(slug){
        if(!slug) return;
        disableInputs();
        const qs = new URLSearchParams();
        if(tenantParam){
            qs.set('tenant', tenantParam);
        }
        if(redirectTarget){
            qs.set('redirect', redirectTarget);
        }
        if(isAgentCallbackRedirect(redirectTarget)){
            try {
                qs.set('agent_id', agentCallbackTarget(redirectTarget));
            } catch (err) {
                showError(err.message);
                return;
            }
        }
        navigateTo('/auth/oidc/start/' + encodeURIComponent(slug) + (qs.toString() ? ('?' + qs.toString()) : ''));
    }

    function disableInputs(){
        ['login_submit','login_cancel'].forEach(id => {
            const el = document.getElementById(id);
            if(el){
                el.disabled = true;
            }
        });
        if(elements.ssoButtons){
            Array.from(elements.ssoButtons.querySelectorAll('button')).forEach(btn => {
                btn.disabled = true;
            });
        }
    }

    // Check if redirect target is an agent callback URL
    function isAgentCallbackRedirect(url) {
        try {
            const parsed = new URL(url, window.location.origin);
            return parsed.pathname === '/api/v1/auth/callback';
        } catch (e) {
            return false;
        }
    }

    // Get a callback token for agent redirect
    function agentCallbackTarget(callbackUrl) {
        const url = new URL(callbackUrl, window.location.origin);
        const ids = url.searchParams.getAll('agent_id');
        const carried = (ids[0] || '').trim();
        const explicit = (params.get('agent_id') || '').trim();
        if(ids.length > 1 || (explicit && carried && explicit !== carried)) {
            throw new Error('Agent callback target mismatch. Start again from the agent.');
        }
        const target = explicit || carried;
        if(!target) throw new Error('Agent ID missing. Start again from the agent.');
        return target;
    }

    async function getAgentCallbackToken(callbackUrl) {
        const resp = await fetch('/api/v1/auth/agent-callback', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            credentials: 'same-origin',
            body: JSON.stringify({ callback_url: callbackUrl, agent_id: agentCallbackTarget(callbackUrl) })
        });
        if (!resp.ok) {
            throw new Error('Failed to create agent callback token');
        }
        const data = await resp.json();
        return data.token;
    }

    // Complete redirect, handling agent callbacks specially
    async function completeRedirect() {
        const target = redirectTarget || '/';
        
        // Check if this is an agent callback redirect
        if (isAgentCallbackRedirect(target)) {
            try {
                // Get a callback token for the agent
                const token = await getAgentCallbackToken(target);
                
                // Parse the callback URL and add the token
                const callbackUrl = new URL(target);
                callbackUrl.searchParams.set('token', token);
                
                navigateTo(callbackUrl.toString());
                return;
            } catch (e) {
                console.error('Agent callback token failed:', e);
                showError('Failed to authenticate with agent. Please try again.');
                return;
            }
        }
        
        // Normal redirect for server pages
        navigateTo(target);
    }

    async function doLogin(){
        const errEl = elements.error;
        if(errEl){
            errEl.style.display = 'none';
        }
        const uEl = document.getElementById('login_username');
        const pEl = document.getElementById('login_password');
        const username = uEl ? (uEl.value || '') : '';
        const password = pEl ? (pEl.value || '') : '';
        try{
            const r = await fetch('/api/v1/auth/login', {method:'POST', headers:{'content-type':'application/json'}, body: JSON.stringify({username, password})});
            if(!r.ok){
                const text = await r.text();
                showError(text || 'Invalid credentials');
                return;
            }
            await completeRedirect();
        }catch(e){
            showError(e && e.message ? e.message : 'Login failed');
        }
    }

    function initializeLoginPage(){
        if(initializeLoginPage._ran){
            return;
        }
        initializeLoginPage._ran = true;
        if(errorCode){
            showError(errorMessages[errorCode] || 'Sign-in failed. Please try again.');
        }
        loadAuthOptions();

        const submit = document.getElementById('login_submit');
        if(submit){
            submit.onclick = doLogin;
        }
        const cancel = document.getElementById('login_cancel');
        if(cancel){
            cancel.onclick = function(){ navigateTo(redirectTarget || '/'); };
        }
        const pwd = document.getElementById('login_password');
        if(pwd){
            pwd.addEventListener('keypress', function(e){ if(e.key==='Enter'){ doLogin(); } });
        }
    }

    document.addEventListener('DOMContentLoaded', initializeLoginPage);
    if(document.readyState === 'interactive' || document.readyState === 'complete'){
        initializeLoginPage();
    }

    window.__pmLogin = window.__pmLogin || {};
    window.__pmLogin.init = initializeLoginPage;
})();
