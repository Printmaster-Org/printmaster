/** @jest-environment jsdom */

require('../shared.js');

const settle = () => new Promise(resolve => setTimeout(resolve, 0));

describe('shared dialog lifecycle', () => {
    beforeEach(async () => {
        document.body.innerHTML = '<main id="background"><button id="opener">Open</button></main>';
        await settle();
        document.getElementById('opener').focus();
    });

    test('focuses, traps, cancels, and restores focus using existing handlers', async () => {
        const promise = window.__pm_shared.showPrompt('Location', '', 'Edit Location');
        await settle();
        const modal = document.getElementById('prompt_modal');
        const dialog = modal.querySelector('[role="dialog"]');
        expect(dialog.getAttribute('aria-modal')).toBe('true');
        expect(dialog.getAttribute('aria-labelledby')).toBe('prompt_modal_title');
        expect(dialog.contains(document.activeElement)).toBe(true);
        expect(document.getElementById('background').hasAttribute('inert')).toBe(true);
        const last = document.getElementById('prompt_modal_ok');
        last.focus();
        last.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true }));
        expect(document.activeElement).toBe(document.getElementById('prompt_modal_close_x'));
        document.activeElement.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));
        expect(await promise).toBe(null);
        await settle();
        expect(document.activeElement.id).toBe('opener');
        expect(document.getElementById('background').hasAttribute('inert')).toBe(false);
    });

    test('nested dialogs restore focus to the underlying dialog, then its opener', async () => {
        const first = window.__pm_shared.showPrompt('Name');
        await settle();
        const input = document.getElementById('prompt_modal_input');
        input.focus();
        const second = window.__pm_shared.showConfirm('Continue?');
        await settle();
        const dialogs = document.querySelectorAll('[role="dialog"]');
        expect(dialogs).toHaveLength(2);
        expect(document.getElementById('prompt_modal').hasAttribute('inert')).toBe(true);
        const cancel = document.querySelector('[data-action="cancel"]');
        cancel.click();
        expect(await second).toBe(false);
        await settle();
        expect(document.getElementById('prompt_modal').hasAttribute('inert')).toBe(false);
        expect(document.getElementById('prompt_modal').contains(document.activeElement)).toBe(true);
        document.getElementById('prompt_modal_cancel').click();
        expect(await first).toBe(null);
        await settle();
        expect(document.activeElement.id).toBe('opener');
    });

    test('toasts expose status semantics and literal text with a dismiss control', async () => {
        document.body.insertAdjacentHTML('beforeend', '<div id="toast_container"></div>');
        window.__pm_shared.showToast('<b>Saved</b>', 'success');
        const toast = document.querySelector('.toast');
        expect(toast.getAttribute('role')).toBe('status');
        expect(toast.querySelector('.toast-message').textContent).toBe('<b>Saved</b>');
        expect(toast.querySelector('b')).toBeNull();
        toast.querySelector('[aria-label="Dismiss notification"]').click();
        expect(toast.classList.contains('toast-hiding')).toBe(true);
    });
});
