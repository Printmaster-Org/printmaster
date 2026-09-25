/**
 * PrintMaster Device Report UI
 * 
 * Provides a form for users to report incorrect or missing device data.
 * Reports are submitted to a proxy service which creates GitHub Gists
 * and opens pre-filled GitHub issues.
 */
(function() {
    'use strict';

    const PROXY_ENDPOINT = 'https://api.printmaster.work/diagnostic';
    const GITHUB_ISSUE_BASE = 'https://github.com/printmaster-org/printmaster/issues/new';

    // Issue type options
    const ISSUE_TYPES = [
        { value: 'wrong_manufacturer', label: 'Wrong Manufacturer' },
        { value: 'wrong_model', label: 'Wrong/Missing Model' },
        { value: 'missing_serial', label: 'Missing Serial Number' },
        { value: 'wrong_serial', label: 'Wrong Serial Number' },
        { value: 'incorrect_counters', label: 'Incorrect Page Counters' },
        { value: 'missing_toner', label: 'Missing Toner Levels' },
        { value: 'missing_supplies', label: 'Missing Supply Info' },
        { value: 'wrong_hostname', label: 'Wrong/Missing Hostname' },
        { value: 'other', label: 'Other Issue' }
    ];

    /**
     * Render the report form HTML
     * @param {Object} device - Current device data
     * @param {Object} parseDebug - Parse debug data (if available)
     * @returns {string} HTML string
     */
    function renderReportForm(device, parseDebug) {
        const issueOptions = ISSUE_TYPES.map(t => 
            `<option value="${t.value}">${t.label}</option>`
        ).join('');

        return `
            <div class="device-report-container">
                <div class="device-report-header">
                    <span class="device-report-icon">🐛</span>
                    <span class="device-report-title">Report Data Issue</span>
                </div>
                <p class="device-report-description">
                    Help improve PrintMaster by reporting incorrect or missing device data.
                    Your report will be submitted to our development team via GitHub.
                </p>
                
                <form id="device_report_form" class="device-report-form">
                    <input type="hidden" id="report_device_ip" value="${escapeHtml(device.ip || '')}">
                    <input type="hidden" id="report_device_serial" value="${escapeHtml(device.serial || '')}">
                    
                    <div class="device-report-field">
                        <label for="report_issue_type">Issue Type *</label>
                        <select id="report_issue_type" required>
                            <option value="">Select issue type...</option>
                            ${issueOptions}
                        </select>
                    </div>
                    
                    <div class="device-report-current">
                        <div class="device-report-current-title">Current Values:</div>
                        <div class="device-report-current-grid">
                            <span class="device-report-current-label">Manufacturer:</span>
                            <span class="device-report-current-value">${escapeHtml(device.manufacturer || device.make || 'Unknown')}</span>
                            <span class="device-report-current-label">Model:</span>
                            <span class="device-report-current-value">${escapeHtml(device.model || 'Unknown')}</span>
                            <span class="device-report-current-label">Serial:</span>
                            <span class="device-report-current-value">${escapeHtml(device.serial || 'Not detected')}</span>
                            <span class="device-report-current-label">Hostname:</span>
                            <span class="device-report-current-value">${escapeHtml(device.hostname || 'Not detected')}</span>
                        </div>
                    </div>
                    
                    <div class="device-report-field">
                        <label for="report_expected_value">What should it be? (optional)</label>
                        <input type="text" id="report_expected_value" 
                               placeholder="e.g., 'Canon imageCLASS MF445dw'" 
                               maxlength="200">
                    </div>
                    
                    <div class="device-report-field">
                        <label for="report_user_message">Additional notes (optional)</label>
                        <textarea id="report_user_message" 
                                  placeholder="Any additional context that might help..."
                                  rows="3"
                                  maxlength="1000"></textarea>
                    </div>
                    
                    <div class="device-report-toggle-row">
                        <input type="checkbox" id="report_full_walk" checked>
                        <div class="toggle-content">
                            <span class="toggle-label">Include full SNMP diagnostic data</span>
                            <span class="toggle-description">Performs a complete SNMP walk to capture all device data. This helps us debug vendor-specific issues but may take a few extra seconds.</span>
                        </div>
                    </div>
                    
                    <div class="device-report-privacy">
                        <span class="device-report-privacy-icon">🔒</span>
                        <span class="device-report-privacy-text">
                            Your report includes device IP (if private), model, serial, and raw SNMP data.
                            Public IPs and company hostnames are anonymized.
                        </span>
                    </div>
                    
                    <div class="device-report-actions">
                        <button type="submit" class="device-report-submit" id="report_submit_btn">
                            <span class="device-report-submit-text">Submit Report → GitHub</span>
                            <span class="device-report-submit-progress" style="display:none;">
                                <span class="device-report-progress-bar"></span>
                                <span class="device-report-progress-text">0%</span>
                            </span>
                        </button>
                    </div>
                    
                    <div id="report_status" class="device-report-status" style="display:none;"></div>
                </form>
            </div>
        `;
    }

    /**
     * Escape HTML special characters
     */
    function escapeHtml(str) {
        if (!str) return '';
        return String(str)
            .replace(/&/g, '&amp;')
            .replace(/</g, '&lt;')
            .replace(/>/g, '&gt;')
            .replace(/"/g, '&quot;')
            .replace(/'/g, '&#039;');
    }

    /**
     * Collect diagnostic data from device and parse debug
     */
    function collectDiagnosticData(device, parseDebug, formData) {
        // Collect toner levels from various possible sources
        const tonerLevels = device.toner_levels || device.tonerLevels || 
            (device.raw_data && device.raw_data.toner_levels) || {};
        
        const report = {
            report_id: generateReportId(),
            timestamp: new Date().toISOString(),
            agent_version: window.__pm_version || 'unknown',
            os: navigator.platform || 'unknown',
            arch: 'browser',
            
            // Issue classification
            issue_type: formData.issueType,
            expected_value: formData.expectedValue,
            user_message: formData.userMessage,
            
            // Device identification
            device_ip: device.ip || '',
            device_serial: device.serial || '',
            device_model: device.model || '',
            device_mac: device.mac || device.mac_address || '',
            
            // Current detected values (basic)
            current_manufacturer: device.manufacturer || device.make || '',
            current_model: device.model || '',
            current_serial: device.serial || '',
            current_hostname: device.hostname || '',
            current_page_count: device.page_count || device.pageCount || 0,
            
            // Extended device info
            firmware: device.firmware || '',
            subnet_mask: device.subnet_mask || device.subnetMask || '',
            gateway: device.gateway || '',
            consumables: device.consumables || [],
            status_messages: device.status_messages || device.statusMessages || [],
            discovery_method: device.discovery_method || device.discoveryMethod || '',
            web_ui_url: device.web_ui_url || device.webUIURL || '',
            device_type: device.device_type || device.deviceType || '',
            source_type: device.source_type || device.sourceType || '',
            is_usb: device.is_usb || device.isUSB || false,
            port_name: device.port_name || device.portName || '',
            driver_name: device.driver_name || device.driverName || '',
            is_default: device.is_default || device.isDefault || false,
            is_shared: device.is_shared || device.isShared || false,
            spooler_status: device.spooler_status || device.spoolerStatus || '',
            
            // Metrics data
            color_pages: device.color_pages || device.colorPages || 0,
            mono_pages: device.mono_pages || device.monoPages || 0,
            scan_count: device.scan_count || device.scanCount || 0,
            toner_levels: tonerLevels,
            
            // Diagnostic data
            detected_vendor: parseDebug?.final_manufacturer || device.manufacturer || '',
            vendor_confidence: parseDebug?.vendor_confidence || 0,
            detection_steps: parseDebug?.steps || [],
            
            // Raw SNMP data
            snmp_responses: (parseDebug?.raw_pdus || []).map(pdu => ({
                oid: pdu.oid || pdu.OID || '',
                type: pdu.type || pdu.Type || '',
                value: pdu.str_value || pdu.StrValue || '',
                hex_value: pdu.hex_value || pdu.HexValue || ''
            })),
            
            // Full SNMP walk option - triggers server-side diagnostic walk
            full_walk: formData.fullWalk,
            
            // Raw data catch-all (may contain additional OEM-specific fields)
            raw_data: device.raw_data || device.rawData || {},
            
            // Recent logs (will be populated server-side)
            recent_logs: []
        };
        
        return report;
    }

    /**
     * Generate a unique report ID
     */
    function generateReportId() {
        const timestamp = Date.now().toString(36);
        const random = Math.random().toString(36).substring(2, 8);
        return `RPT-${timestamp}-${random}`.toUpperCase();
    }

    /**
     * Submit report to proxy service
     */
    async function submitReport(report) {
        // First try the local agent endpoint which will handle proxy submission
        // This allows the agent to add server-side data (logs, etc.)
        try {
            const response = await fetch('/api/report', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(report)
            });
            
            if (response.ok) {
                return await response.json();
            }
            
            // If local endpoint fails, try direct proxy
            console.warn('Local report endpoint failed, trying direct proxy');
        } catch (e) {
            console.warn('Local report endpoint error:', e);
        }
        
        // Fallback: direct to proxy
        const proxyResponse = await fetch(PROXY_ENDPOINT, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ report })
        });
        
        if (!proxyResponse.ok) {
            throw new Error(`Proxy returned ${proxyResponse.status}`);
        }
        
        return await proxyResponse.json();
    }

    /**
     * Build fallback GitHub issue URL (when proxy fails)
     */
    function buildFallbackIssueUrl(report) {
        const vendor = report.detected_vendor || 'Unknown';
        const model = report.device_model || report.device_ip;
        const title = encodeURIComponent(`[Device Report] ${vendor} - ${model}`);
        
        const body = encodeURIComponent(
            `## Issue Type\n${report.issue_type}\n\n` +
            `## Expected Value\n${report.expected_value || 'Not specified'}\n\n` +
            `## Description\n${report.user_message || 'No additional notes'}\n\n` +
            `## Current Values\n` +
            `- **Manufacturer:** ${report.current_manufacturer}\n` +
            `- **Model:** ${report.current_model}\n` +
            `- **Serial:** ${report.current_serial}\n\n` +
            `## Diagnostic Data\n` +
            `**Please attach the downloaded diagnostic file below.**\n\n` +
            `---\n` +
            `*Report ID: ${report.report_id}*`
        );
        
        return `${GITHUB_ISSUE_BASE}?template=device-report.yml&title=${title}&body=${body}`;
    }

    /**
     * Download report as JSON file (fallback)
     */
    function downloadReportFile(report) {
        const json = JSON.stringify(report, null, 2);
        const blob = new Blob([json], { type: 'application/json' });
        const url = URL.createObjectURL(blob);
        
        const a = document.createElement('a');
        a.href = url;
        a.download = `printmaster-diag-${report.report_id}.json`;
        document.body.appendChild(a);
        a.click();
        document.body.removeChild(a);
        URL.revokeObjectURL(url);
    }

    /**
     * Initialize report form event handlers
     */
    function initReportForm(device, parseDebug) {
        const form = document.getElementById('device_report_form');
        if (!form) return;
        
        form.addEventListener('submit', async (e) => {
            e.preventDefault();
            
            const submitBtn = document.getElementById('report_submit_btn');
            const submitText = submitBtn.querySelector('.device-report-submit-text');
            const submitProgress = submitBtn.querySelector('.device-report-submit-progress');
            const progressBar = submitBtn.querySelector('.device-report-progress-bar');
            const progressText = submitBtn.querySelector('.device-report-progress-text');
            const statusEl = document.getElementById('report_status');
            
            // Gather form data
            const formData = {
                issueType: document.getElementById('report_issue_type').value,
                expectedValue: document.getElementById('report_expected_value').value.trim(),
                userMessage: document.getElementById('report_user_message').value.trim(),
                fullWalk: document.getElementById('report_full_walk').checked
            };
            
            if (!formData.issueType) {
                showStatus(statusEl, 'error', 'Please select an issue type');
                return;
            }
            
            // Disable button, show progress
            submitBtn.disabled = true;
            submitText.style.display = 'none';
            submitProgress.style.display = 'flex';
            progressBar.style.width = '0%';
            progressText.textContent = 'Starting...';
            statusEl.style.display = 'none';
            
            // Helper to update progress display
            const updateProgress = (percent, message) => {
                progressBar.style.width = percent + '%';
                progressText.textContent = message || (percent + '%');
            };
            
            try {
                // Collect diagnostic data for the request
                const report = collectDiagnosticData(device, parseDebug, formData);
                
                // Use streaming endpoint if full walk is requested (takes longer)
                if (formData.fullWalk) {
                    const result = await submitReportWithProgress(report, updateProgress);
                    handleSubmitResult(result, report, statusEl);
                } else {
                    // Quick submit without streaming
                    updateProgress(50, 'Submitting...');
                    const result = await submitReport(report);
                    updateProgress(100, 'Complete!');
                    handleSubmitResult(result, report, statusEl);
                }
                
            } catch (error) {
                console.error('Report submission failed:', error);
                
                // Fallback: download file and show manual instructions
                const report = collectDiagnosticData(device, parseDebug, formData);
                downloadReportFile(report);
                
                const fallbackUrl = buildFallbackIssueUrl(report);
                
                showStatus(statusEl, 'warning', 
                    'Could not submit automatically. ' +
                    'A diagnostic file has been downloaded. ' +
                    '<a href="' + fallbackUrl + '" target="_blank">Click here to open GitHub</a> ' +
                    'and attach the file to your issue.'
                );
            } finally {
                // Re-enable button
                submitBtn.disabled = false;
                submitText.style.display = 'inline';
                submitProgress.style.display = 'none';
            }
        });
    }
    
    /**
     * Handle submit result (success or partial success)
     */
    function handleSubmitResult(result, report, statusEl) {
        if (result.success && result.issue_url) {
            showStatus(statusEl, 'success', 'Report submitted! Opening GitHub...');
            setTimeout(() => {
                window.open(result.issue_url, '_blank');
            }, 500);
        } else if (result.gist_url) {
            // Partial success - gist created but issue URL missing
            const issueUrl = buildFallbackIssueUrl(report) + 
                '&gist=' + encodeURIComponent(result.gist_url);
            showStatus(statusEl, 'success', 'Report submitted! Opening GitHub...');
            setTimeout(() => {
                window.open(issueUrl, '_blank');
            }, 500);
        } else if (result.fallback) {
            // Server returned fallback mode
            downloadReportFile(result.report || report);
            const fallbackUrl = result.issue_url || buildFallbackIssueUrl(report);
            showStatus(statusEl, 'warning', 
                'Could not submit automatically. ' +
                'A diagnostic file has been downloaded. ' +
                '<a href="' + fallbackUrl + '" target="_blank">Click here to open GitHub</a> ' +
                'and attach the file to your issue.'
            );
        } else {
            throw new Error(result.error || 'Unknown error');
        }
    }
    
    /**
     * Submit report using SSE streaming for progress updates
     */
    async function submitReportWithProgress(report, onProgress) {
        return new Promise((resolve, reject) => {
            // We need to POST data and receive SSE, which is tricky.
            // Use fetch with ReadableStream to handle SSE-like response.
            fetch('/api/report/stream', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(report)
            }).then(response => {
                if (!response.ok) {
                    throw new Error(`HTTP ${response.status}`);
                }
                
                const reader = response.body.getReader();
                const decoder = new TextDecoder();
                let buffer = '';
                
                function processEvents() {
                    reader.read().then(({ done, value }) => {
                        if (done) {
                            // Stream ended without complete event - treat as error
                            reject(new Error('Stream ended unexpectedly'));
                            return;
                        }
                        
                        buffer += decoder.decode(value, { stream: true });
                        
                        // Parse SSE events from buffer
                        const lines = buffer.split('\n');
                        buffer = lines.pop() || ''; // Keep incomplete line in buffer
                        
                        let eventType = '';
                        for (const line of lines) {
                            if (line.startsWith('event: ')) {
                                eventType = line.slice(7).trim();
                            } else if (line.startsWith('data: ')) {
                                const data = line.slice(6);
                                try {
                                    const parsed = JSON.parse(data);
                                    
                                    if (eventType === 'progress') {
                                        onProgress(parsed.percent || 0, parsed.message || '');
                                    } else if (eventType === 'complete') {
                                        onProgress(100, 'Complete!');
                                        resolve(parsed);
                                        return;
                                    } else if (eventType === 'error') {
                                        if (parsed.fallback) {
                                            // Fallback mode - resolve with fallback data
                                            resolve(parsed);
                                            return;
                                        }
                                        reject(new Error(parsed.error || 'Unknown error'));
                                        return;
                                    }
                                } catch (e) {
                                    console.warn('Failed to parse SSE data:', data, e);
                                }
                            }
                        }
                        
                        // Continue reading
                        processEvents();
                    }).catch(reject);
                }
                
                processEvents();
            }).catch(reject);
        });
    }

    /**
     * Show status message
     */
    function showStatus(el, type, message) {
        if (!el) return;
        el.className = 'device-report-status device-report-status-' + type;
        el.innerHTML = message;
        el.style.display = 'block';
    }

    // Expose to global scope for use by cards.js
    window.__pm_report = {
        renderReportForm,
        initReportForm,
        ISSUE_TYPES
    };

})();
