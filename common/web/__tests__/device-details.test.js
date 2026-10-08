/** @jest-environment jsdom */

require('../cards.js');

describe('device details', () => {
    const device = {
        serial: 'DEMO-001',
        manufacturer: 'Epson',
        model: 'WF-C5790',
        ip: '192.0.2.10',
        location: 'Reception',
        raw_data: { is_color: true, is_inkjet: true }
    };
    const render = (data = device, source = 'saved') =>
        window.__pm_shared_cards.showPrinterDetailsData({ ...data }, source);
    const flush = () => new Promise(resolve => setTimeout(resolve, 0));

    beforeEach(() => {
        document.body.innerHTML = `
            <div id="printer_details_overlay">
                <h3 id="printer_details_title"></h3>
                <div id="printer_details_capabilities"></div>
                <div id="printer_details_body"></div>
                <div id="printer_details_actions"></div>
            </div>`;
        window.__pm_shared = {
            showPrompt: jest.fn().mockResolvedValue('Finance'),
            showToast: jest.fn(),
            warn: jest.fn(),
            error: jest.fn()
        };
        global.fetch = jest.fn().mockResolvedValue({
            ok: true,
            json: async () => [],
            text: async () => ''
        });
    });

    test('groups fields and moves capabilities into the header', () => {
        render();
        expect(document.querySelector('#printer_details_capabilities').textContent).toContain('Color');
        expect(document.querySelector('.device-identity-card .device-info-fields')).not.toBeNull();
        expect(document.querySelector('[data-field-row="serial"] .edit-field-btn')).toBeNull();
        expect(document.querySelector('[aria-label="Edit Location"]').parentElement.textContent).toContain('Reception');
        expect(document.querySelector('#printer_metrics_summary')).not.toBeNull();
        expect(document.querySelector('#refresh_data_btn')).not.toBeNull();
        expect(document.querySelector('#collect_metrics_btn')).not.toBeNull();
    });

    test('retains web UI actions and renders field values as text', () => {
        render({ ...device, location: '<b>Reception</b>', web_ui_url: 'http://192.0.2.10/?a=1&b=2' });
        expect(document.querySelector('#field_location_display').textContent).toBe('<b>Reception</b>');
        expect(document.querySelector('#field_location_display b')).toBeNull();
        expect(document.querySelector('[data-action="open-direct"]').dataset.webuiUrl).toBe('http://192.0.2.10/?a=1&b=2');
        expect(document.querySelector('[data-action="open-proxy"]').dataset.serial).toBe(device.serial);
    });

    test('reopening the modal does not duplicate field updates', async () => {
        render();
        render();
        await flush();
        fetch.mockClear();
        document.querySelector('[aria-label="Edit Location"]').click();
        await flush();
        expect(window.__pm_shared.showPrompt).toHaveBeenCalledTimes(1);
        expect(fetch).toHaveBeenCalledTimes(1);
        expect(fetch).toHaveBeenCalledWith('/devices/update', expect.objectContaining({
            body: JSON.stringify({ serial: device.serial, location: 'Finance' })
        }));
        expect(document.querySelector('#field_location_display').textContent).toBe('Finance');
        expect(document.querySelector('[aria-label="Edit Location"]').dataset.current).toBe('Finance');
    });

    test('cancelled and failed edits preserve the existing value', async () => {
        render();
        await flush();
        fetch.mockClear();
        window.__pm_shared.showPrompt.mockResolvedValueOnce(null);
        document.querySelector('[aria-label="Edit Location"]').click();
        await flush();
        expect(fetch).not.toHaveBeenCalled();
        fetch.mockResolvedValueOnce({ ok: false, text: async () => 'Rejected' });
        document.querySelector('[aria-label="Edit Location"]').click();
        await flush();
        expect(document.querySelector('#field_location_display').textContent).toBe('Reception');
        expect(window.__pm_shared.showToast).toHaveBeenCalledWith('Update failed: Rejected', 'error');
    });

    test('discovered devices retain save action without saved-device metrics', () => {
        render(device, 'discovered');
        expect(document.querySelector('#printer_metrics_summary')).toBeNull();
        expect(document.querySelector('#printer_details_actions').textContent).toContain('Save Device');
        expect(document.querySelector('#printer_details_title').textContent).toContain('(Discovered)');
    });
});
