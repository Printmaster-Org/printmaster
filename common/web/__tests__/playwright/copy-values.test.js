const { test, expect } = require('@playwright/test');
const path = require('path');

test('table values truncate without covering copy controls and copy the full value', async ({ page }) => {
    await page.setContent('<table class="simple-table"><tbody><tr></tr></tbody></table>');
    await page.addStyleTag({ path: path.resolve(__dirname, '../../shared.css') });
    await page.addStyleTag({ path: path.resolve(__dirname, '../../../../server/web/style.css') });
    await page.addScriptTag({ path: path.resolve(__dirname, '../../shared.js') });
    await page.evaluate(() => {
        window.copiedValues = [];
        Object.defineProperty(navigator, 'clipboard', {
            configurable: true,
            value: { writeText: async value => window.copiedValues.push(value) }
        });
        const copy = window.__pm_shared.renderCopyableValue;
        document.querySelector('tr').innerHTML = [
            copy('P1 - Ubuntu VM Agent with a long name', 'agent name'),
            copy('Ross Design & Engineering "Tenant"', 'tenant'),
            `<div class="table-primary">${copy('192.168.0.152', 'IP address')}</div>
             <div class="muted-text">${copy('P2-P-OFFICE.RossDesign.example', 'hostname')}</div>`,
            `<div class="muted-text">Serial ${copy('LONG-SERIAL-12345678901234567890', 'serial number')}</div>`
        ].map(value => `<td>${value}</td>`).join('');
    });

    // Font metrics differ between OSes (CI runs Linux); the wide font pass approximates DejaVu Sans.
    const fonts = ['', '"DejaVu Sans", Verdana, sans-serif'];
    const widths = [140, 180, 240];
    for (const [font, width] of fonts.flatMap(font => widths.map(width => [font, width]))) {
        await page.locator('table').evaluate((table, { width, font }) => {
            table.style.minWidth = '0';
            table.style.width = `${width * 4}px`;
            table.style.fontFamily = font;
        }, { width, font });
        const values = page.locator('.pm-copy-value');
        for (const value of await values.all()) {
            const text = value.locator(':scope > span');
            const button = value.locator('button');
            const fullValue = await button.getAttribute('data-copy');
            await expect(text).toHaveAttribute('title', fullValue);
            expect((await text.boundingBox()).width).toBeGreaterThan(10);
            await text.hover();
            await expect(button).toBeVisible();
            const geometry = await value.evaluate(element => {
                const text = element.querySelector('span');
                const button = element.querySelector('button');
                const cell = element.closest('td');
                const style = getComputedStyle(cell);
                return {
                    textRight: text.getBoundingClientRect().right,
                    buttonLeft: button.getBoundingClientRect().left,
                    buttonRight: button.getBoundingClientRect().right,
                    cellRight: cell.getBoundingClientRect().right - parseFloat(style.paddingRight),
                    truncated: text.scrollWidth > text.clientWidth,
                    overflow: getComputedStyle(text).textOverflow,
                    buttonWidth: button.getBoundingClientRect().width
                };
            });
            expect(geometry.textRight).toBeLessThanOrEqual(geometry.buttonLeft);
            expect(geometry.buttonRight).toBeLessThanOrEqual(geometry.cellRight + 1);
            expect(geometry.buttonWidth).toBeCloseTo(24, 2);
            expect(geometry.overflow).toBe('ellipsis');
            if (width === widths[0]) expect(geometry.truncated).toBe(true);
            await button.click();
            await expect.poll(() => page.evaluate(() => window.copiedValues.at(-1))).toBe(fullValue);
        }
    }
});
