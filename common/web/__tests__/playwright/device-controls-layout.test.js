const fs = require('node:fs');
const path = require('node:path');
const { test, expect } = require('@playwright/test');

test('device controls receive clicks with pinning and horizontal scrolling', async ({ page }) => {
    const css = fs.readFileSync(path.resolve(__dirname, '../../../../server/web/style.css'), 'utf8');
    const columns = [200, 100, 180, 150, 130, 150, 130, 140];
    await page.setContent(`<div style="width:800px;overflow:auto" id="wrapper">
            <table id="devices_table" class="simple-table">
                <thead><tr><th class="device-controls-cell">Selection / Details</th>
                    ${columns.map(width => `<th style="width:${width}px">Device field</th>`).join('')}
                </tr></thead>
                <tbody><tr><td class="device-controls-cell"><div class="device-selection-controls">
                    <label><input type="checkbox" aria-label="Select device CV25P8"> Select</label>
                    <button type="button" id="details">Details</button>
                </div></td>${columns.map((_, index) => `<td data-column-id="${index === 0 ? 'device' : index}">Device field</td>`).join('')}</tr></tbody>
            </table></div><output id="opened"></output>`);
    await page.addStyleTag({ content: css });
    await page.locator('#details').evaluate(button => {
        button.addEventListener('click', () => { document.getElementById('opened').textContent = 'Details opened'; });
    });
    for (const pinned of [false, true]) {
        await page.locator('[data-column-id="device"]').evaluate((cell, pin) => {
            cell.classList.toggle('pinned-left', pin);
        }, pinned);
        for (const scroll of [0, 350]) {
            await page.locator('#wrapper').evaluate((wrapper, left) => { wrapper.scrollLeft = left; }, scroll);
            const checkbox = page.getByRole('checkbox', { name: 'Select device CV25P8' });
            await checkbox.check({ timeout: 3000 });
            await expect(checkbox).toBeChecked();
            await checkbox.uncheck({ timeout: 3000 });
            await expect(checkbox).not.toBeChecked();
            await page.getByRole('button', { name: 'Details', exact: true }).click({ timeout: 3000 });
            await expect(page.locator('#opened')).toHaveText('Details opened');
            await page.locator('#opened').evaluate(output => { output.textContent = ''; });
        }
    }
});
