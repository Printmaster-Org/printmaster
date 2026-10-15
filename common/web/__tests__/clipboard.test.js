/** @jest-environment jsdom */

require('../shared.js');

describe('clipboard helpers', () => {
    beforeEach(() => {
        document.body.innerHTML = '';
        window.navigator.clipboard = { writeText: jest.fn().mockResolvedValue(true) };
    });

    test('copyToClipboard writes text and returns true', async () => {
        const res = await window.__pm_shared.copyToClipboard('abc');
        expect(window.navigator.clipboard.writeText).toHaveBeenCalledWith('abc');
        expect(res).toBe(true);
    });

    test('copyToClipboard handles rejection and returns false', async () => {
        // Suppress expected error logging during this test
        const origError = window.__pm_shared.error;
        window.__pm_shared.error = jest.fn();
        
        window.navigator.clipboard.writeText.mockRejectedValueOnce(new Error('fail'));
        const res = await window.__pm_shared.copyToClipboard('abc');
        expect(res).toBe(false);
        
        window.__pm_shared.error = origError;
    });

    test('copy buttons safely encode raw values and copy once without bubbling to selection', async () => {
        const value = '<device "serial">&123';
        document.body.innerHTML = `<div id="row">${window.__pm_shared.renderCopyableValue(value, 'serial number')}</div>`;
        const rowClick = jest.fn();
        document.getElementById('row').addEventListener('click', event => {
            if (!event.target.closest('button')) rowClick();
        });
        const button = document.querySelector('.pm-copy-button');
        expect(button.dataset.copy).toBe(value);
        expect(button.getAttribute('aria-label')).toBe('Copy serial number');
        expect(document.querySelector('.pm-copy-value > span').title).toBe(value);
        expect(document.querySelector('device')).toBeNull();
        button.click();
        await Promise.resolve(); await Promise.resolve();
        expect(navigator.clipboard.writeText).toHaveBeenCalledTimes(1);
        expect(navigator.clipboard.writeText).toHaveBeenCalledWith(value);
        expect(rowClick).not.toHaveBeenCalled();
    });

    test('no copy button for absent values; zero is copyable', () => {
        expect(window.__pm_shared.renderCopyButton('')).toBe('');
        expect(window.__pm_shared.renderCopyButton(null)).toBe('');
        expect(window.__pm_shared.renderCopyButton(0)).toContain('data-copy="0"');
    });

    test('missing clipboard API reports failure instead of throwing synchronously', async () => {
        navigator.clipboard = undefined;
        const original = window.__pm_shared.error;
        window.__pm_shared.error = jest.fn();
        expect(await window.__pm_shared.copyToClipboard('abc')).toBe(false);
        expect(window.__pm_shared.error).toHaveBeenCalled();
        window.__pm_shared.error = original;
    });
});
