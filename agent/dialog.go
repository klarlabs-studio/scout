package agent

import (
	"context"
	"encoding/json"
	"fmt"
)

// DialogInfo describes a visible modal/dialog on the page.
type DialogInfo struct {
	ContentTrust string      `json:"content_trust,omitempty"`
	Found        bool        `json:"found"`
	Type         string      `json:"type,omitempty"`     // dialog, modal, overlay, alert, confirm, prompt
	Title        string      `json:"title,omitempty"`    // dialog title/heading
	Text         string      `json:"text,omitempty"`     // dialog body text
	Buttons      []string    `json:"buttons,omitempty"`  // available action buttons
	Inputs       []InputInfo `json:"inputs,omitempty"`   // input fields in the dialog
	Selector     string      `json:"selector,omitempty"` // CSS selector for the dialog element
}

// DetectDialog checks if a modal, dialog, or overlay is currently visible on the page.
func (s *Session) DetectDialog() (*DialogInfo, error) {
	return s.DetectDialogContext(context.Background())
}

// DetectDialogContext searches for visible dialogs while honoring ctx.
func (s *Session) DetectDialogContext(ctx context.Context) (*DialogInfo, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()

	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}

	js := `(function() {
		const result = {found: false};

		// Check for native <dialog> elements
		for (const dialog of document.querySelectorAll('dialog[open]')) {
			result.found = true;
			result.type = 'dialog';
			result.selector = dialog.id ? '#' + dialog.id : 'dialog[open]';
			result.title = (dialog.querySelector('h1,h2,h3,h4,.title,[class*="title"]') || {}).textContent?.trim() || '';
			result.text = dialog.textContent.trim().slice(0, 500);
			result.buttons = Array.from(dialog.querySelectorAll('button')).map(b => b.textContent.trim()).filter(Boolean).slice(0, 5);
			result.inputs = Array.from(dialog.querySelectorAll('input,textarea,select')).map(i => ({
				id: i.id || '', name: i.name || '', type: i.type || i.tagName.toLowerCase(),
				placeholder: i.placeholder || ''
			})).slice(0, 5);
			return JSON.stringify(result);
		}

		// Check for aria-modal elements
		for (const modal of document.querySelectorAll('[aria-modal="true"], [role="dialog"], [role="alertdialog"]')) {
			const style = window.getComputedStyle(modal);
			if (style.display === 'none' || style.visibility === 'hidden') continue;
			result.found = true;
			result.type = modal.getAttribute('role') === 'alertdialog' ? 'alert' : 'modal';
			result.selector = modal.id ? '#' + modal.id : '[aria-modal="true"]';
			result.title = (modal.querySelector('h1,h2,h3,h4,.title,[class*="title"],[class*="header"]') || {}).textContent?.trim() || '';
			result.text = modal.textContent.trim().slice(0, 500);
			result.buttons = Array.from(modal.querySelectorAll('button,a[role="button"]')).map(b => b.textContent.trim()).filter(Boolean).slice(0, 5);
			result.inputs = Array.from(modal.querySelectorAll('input,textarea,select')).map(i => ({
				id: i.id || '', name: i.name || '', type: i.type || i.tagName.toLowerCase(),
				placeholder: i.placeholder || ''
			})).slice(0, 5);
			return JSON.stringify(result);
		}

		// Check for overlay-style modals (fixed/absolute with high z-index)
		const overlaySelectors = [
			'[class*="modal"]:not([style*="display: none"])',
			'[class*="dialog"]:not([style*="display: none"])',
			'[class*="overlay"]:not([style*="display: none"])',
			'[class*="popup"]:not([style*="display: none"])',
			'[class*="lightbox"]:not([style*="display: none"])',
		];

		for (const sel of overlaySelectors) {
			for (const el of document.querySelectorAll(sel)) {
				const style = window.getComputedStyle(el);
				if (style.display === 'none' || style.visibility === 'hidden' || style.opacity === '0') continue;
				const zIndex = parseInt(style.zIndex) || 0;
				const pos = style.position;
				if ((pos === 'fixed' || pos === 'absolute') && zIndex >= 100) {
					result.found = true;
					result.type = 'overlay';
					result.selector = el.id ? '#' + el.id : sel;
					result.title = (el.querySelector('h1,h2,h3,h4,.title,[class*="title"]') || {}).textContent?.trim() || '';
					result.text = el.textContent.trim().slice(0, 500);
					result.buttons = Array.from(el.querySelectorAll('button,a[role="button"]')).map(b => b.textContent.trim()).filter(Boolean).slice(0, 5);
					result.inputs = Array.from(el.querySelectorAll('input,textarea,select')).map(i => ({
						id: i.id || '', name: i.name || '', type: i.type || i.tagName.toLowerCase(),
						placeholder: i.placeholder || ''
					})).slice(0, 5);
					return JSON.stringify(result);
				}
			}
		}

		return JSON.stringify(result);
	})()`

	resultVal, err := s.page.EvaluateContext(ctx, js)
	if err != nil {
		return nil, fmt.Errorf("dialog detection failed: %w", err)
	}

	str, _ := resultVal.(string)
	info := DialogInfo{ContentTrust: ContentTrustUntrustedPage}
	_ = json.Unmarshal([]byte(str), &info)
	for i := range info.Inputs {
		info.Inputs[i].ContentTrust = ContentTrustUntrustedPage
	}
	return &info, nil
}
