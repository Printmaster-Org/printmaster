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
            showConfirm: jest.fn().mockResolvedValue(false),
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

    test('device deletion requires confirmation and cancelled deletion sends no request', async () => {
        render();
        await flush();
        fetch.mockClear();
        const button = [...document.querySelectorAll('#printer_details_actions button')].find(el => el.textContent === 'Delete Device');
        button.click();
        await flush();
        expect(window.__pm_shared.showConfirm).toHaveBeenCalledWith(expect.stringContaining(device.serial), 'Delete Device', true);
        expect(fetch).not.toHaveBeenCalled();
        expect(button.disabled).toBe(false);
        window.__pm_shared.showConfirm.mockResolvedValue(true);
        fetch.mockResolvedValue({ ok: false, status: 500 });
        button.click();
        await flush();
        expect(fetch).toHaveBeenCalledWith('/devices/delete', expect.objectContaining({ method: 'POST' }));
        expect(button.disabled).toBe(false);
        expect(button.textContent).toBe('Delete Device');
        expect(window.__pm_shared.error).toHaveBeenCalled();
    });

    test('late metrics and credentials from the previous device cannot overwrite the current device', async () => {
        const pending = [];
        fetch.mockImplementation(url => new Promise(resolve => pending.push({ url, resolve })));
        render({ ...device, serial: 'A' });
        render({ ...device, serial: 'B' });
        const respond = (serial, pageCount) => pending.filter(request => request.url.includes('serial=' + serial)).forEach(request =>
            request.resolve({ ok: true, json: async () => request.url.includes('credentials')
                ? { exists: true, username: serial }
                : [{ timestamp: '2026-10-01', page_count: 0 }, { timestamp: '2026-10-08', page_count: pageCount }] }));
        respond('B', 222);
        await flush();
        respond('A', 111);
        await flush();
        expect(document.querySelector('.device-metric-value').textContent).toBe('222');
        expect(document.getElementById('cred_username').value).toBe('B');
    });

    test('failed metrics summary is logged and offers retry rather than claiming no data', async () => {
        fetch.mockResolvedValue({ ok: false, status: 503 });
        render();
        await flush();
        expect(document.getElementById('printer_metrics_summary').textContent).toContain('Metrics unavailable');
        expect(window.__pm_shared.error).toHaveBeenCalled();
        fetch.mockResolvedValue({ ok: true, json: async () => [] });
        document.querySelector('#printer_metrics_summary button').click();
        await flush();
        expect(document.getElementById('printer_metrics_summary').textContent).toContain('No metrics data available');
    });

    test('color supplies are evidence of color capability but explicit monochrome still wins', () => {
        const toner_levels = { Black: 60, Cyan: 50, Magenta: 40, Yellow: 30 };
        expect(window.__pm_shared_cards.getDeviceTonerBarData({ toner_levels })).toHaveLength(4);
        expect(window.__pm_shared_cards.getDeviceTonerBarData({ toner_levels, is_mono: true })).toHaveLength(1);
    });

    test('deduplicates matching aliases without merging distinct or conflicting cartridges', () => {
        const levels = window.__pm_shared_cards.buildTonerLevels({ toner_levels: {
            supply_light_gray_ink_cartridge_t44h9: 27,
            'Light Gray Ink Cartridge T44H9': '27',
            photo_black: 70, matte_black: 50,
            'Violet Ink Cartridge A': 28, 'Violet Ink Cartridge B': 28,
            'Gray Ink': 20, supply_gray_ink: 30
        } });
        expect(Object.keys(levels)).toHaveLength(7);
        expect(levels['Light Gray Ink Cartridge T44H9']).toBe('27');
        expect(levels.supply_light_gray_ink_cartridge_t44h9).toBeUndefined();
        render({ ...device, toner_levels: levels });
        expect(document.querySelector('.consumables-section').textContent).not.toContain('supply_');
        expect(document.querySelector('.consumables-section').textContent).toContain('Light Gray Ink Cartridge T44H9');
    });

    test.each([[undefined, 'Not collected'], [null, 'Not collected'], ['', 'Not collected'], [0, '0'], ['12', '12']])(
        'distinguishes missing page counts (%s) from zero', (page_count, expected) => {
            const card = document.createElement('div');
            card.innerHTML = window.__pm_shared_cards.renderSavedCard({ serial: device.serial, printer_info: { ...device, page_count } });
            expect([...card.querySelectorAll('.saved-device-card-row')].find(row => row.textContent.includes('Total Pages')).textContent).toBe('Total Pages' + expected);
        });

    test.each([
        ['Black', '#1a1a1a'],
        ['Cyan', '#00bcd4'],
        ['Magenta', '#e91e63'],
        ['Yellow', '#ffc107'],
        ['Photo Black Ink', '#333'],
        ['Matte Black Ink', '#444'],
        ['Light Cyan Ink', '#4dd0e1'],
        ['Light Magenta Ink', '#f48fb1'],
        ['Gray Ink Cartridge T44H7, T44P7, T44W7', '#9e9e9e'],
        ['Green Ink Cartridge T44HB, T44PB, T44WB', '#4caf50'],
        ['Light Gray Ink Cartridge T44H9, T44P9, T44W9', '#bdbdbd'],
        ['supply_light_grey_ink_cartridge_t44h9', '#bdbdbd'],
        ['Orange Ink Cartridge T44HA, T44PA, T44WA', '#ff9800'],
        ['Violet Ink Cartridge T44HD, T44PD, T44WD', '#9c27b0'],
        ['supply_violet_ink_cartridge_t44hd', '#9c27b0'],
        ['k', '#1a1a1a'],
        ['Unknown ink', '#757575'],
        ['', '#757575']
    ])('resolves the shared palette for %s', (name, color) => {
        expect(window.__pm_shared_cards.getTonerColor(name)).toBe(color);
    });

    test('full, mini and table ink bars share colors regardless of supply level', () => {
        const toner_levels = {
            'Light Gray Ink Cartridge T44H9': 27,
            'Green Ink Cartridge T44HB': 3,
            'Orange Ink Cartridge T44HA': 28,
            'Violet Ink Cartridge T44HD': 100
        };
        const printer = { ...device, toner_levels };
        render(printer);
        const rows = [...document.querySelectorAll('.consumable-level')];
        const miniCard = document.createElement('div');
        miniCard.innerHTML = window.__pm_shared_cards.renderSavedCard({ serial: device.serial, printer_info: printer });
        const miniFills = miniCard.querySelectorAll('.mini-consumable-bar > div');
        const tableData = window.__pm_shared_cards.getDeviceTonerBarData(printer);
        Object.entries(toner_levels).forEach(([name, level], index) => {
            const color = window.__pm_shared_cards.getTonerColor(name);
            expect(rows[index].style.getPropertyValue('--consumable-color')).toBe(color);
            expect(rows[index].querySelector('[role="progressbar"]').getAttribute('aria-valuenow')).toBe(String(level));
            expect(rows[index].querySelector('.consumable-level-fill').style.width).toBe(level + '%');
            expect(rows[index].querySelector('.consumable-level-percent').textContent).toBe(level + '%');
            const probe = document.createElement('div');
            probe.style.background = color;
            expect(miniFills[index].style.background).toBe(probe.style.background);
            expect(tableData.find(entry => entry.name === name).color).toBe(color);
        });
        expect(document.querySelectorAll('#printer_consumables_card_actual .device-details-card-title')).toHaveLength(1);
    });

    test.each([
        ['waste_toner', 100, '#d32f2f'],
        ['waste_black_toner', 60, '#f57c00'],
        ['maintenance', 20, '#388e3c'],
        ['drum', 5, '#d32f2f'],
        ['drum', 30, '#f57c00'],
        ['drum', 90, '#388e3c']
    ])('retains status coloring for %s at %s%%', (name, level, color) => {
        render({ ...device, toner_levels: { [name]: level } });
        expect(document.querySelector('.consumable-level').style.getPropertyValue('--consumable-color')).toBe(color);
    });

    test('clamps levels and renders supply names and descriptions as text', () => {
        render({ ...device, toner_levels: { 'Gray <img src=x> Ink': -10, 'Orange Ink': 120, 'Part <b>number</b>': '<script>bad()</script>' } });
        const rows = document.querySelectorAll('.consumable-level');
        expect(rows[0].querySelector('[role="progressbar"]').getAttribute('aria-valuenow')).toBe('0');
        expect(rows[1].querySelector('[role="progressbar"]').getAttribute('aria-valuenow')).toBe('100');
        expect(document.querySelector('.consumable-description').textContent).toContain('<script>bad()</script>');
        expect(document.querySelector('.consumables-section img, .consumables-section script, .consumables-section b')).toBeNull();
    });
});
