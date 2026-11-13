package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	browse "go.klarlabs.de/scout"
)

// Profile holds serializable browser state that can be saved/loaded across sessions.
type Profile struct {
	ContentTrust string            `json:"content_trust,omitempty"`
	URL          string            `json:"url,omitempty"`
	Cookies      []browse.Cookie   `json:"cookies,omitempty"`
	LocalStorage map[string]string `json:"local_storage,omitempty"`
}

// ProfileResult reports which parts of a profile were restored without
// exposing cookie values or localStorage contents.
type ProfileResult struct {
	OperationOrdinal int            `json:"operation_ordinal,omitempty"`
	Action           string         `json:"action"`
	TargetID         string         `json:"target_id,omitempty"`
	URL              string         `json:"url,omitempty"`
	Outcome          string         `json:"outcome"`
	Verified         bool           `json:"verified"`
	CookiesRestored  int            `json:"cookies_restored"`
	StorageKeys      int            `json:"storage_keys_restored"`
	Evidence         string         `json:"evidence"`
	Receipt          *ActionReceipt `json:"action_receipt,omitempty"`
}

// CaptureProfile extracts the current browser state (cookies + localStorage) as a Profile.
// This is the domain operation — it does not involve filesystem I/O.
func (s *Session) CaptureProfile() (*Profile, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.CaptureProfileContext(ctx)
}

func (s *Session) CaptureProfileContext(ctx context.Context) (*Profile, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if err := s.checkExpectedTargetIDContext(ctx); err != nil {
		return nil, err
	}
	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}

	profile := &Profile{ContentTrust: ContentTrustUntrustedPage}
	pageURL, err := pageURLContext(ctx, s.page)
	if err != nil {
		return nil, fmt.Errorf("failed to read profile page URL: %w", err)
	}
	profile.URL = pageURL

	cookies, err := s.cookiesContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to read profile cookies: %w", err)
	}
	profile.Cookies = cookies

	result, err := s.page.EvaluateContext(ctx, `(function() {
		const items = {};
		for (let i = 0; i < localStorage.length; i++) {
			const key = localStorage.key(i);
			items[key] = localStorage.getItem(key);
		}
		return JSON.stringify(items);
	})()`)
	if err != nil {
		return nil, fmt.Errorf("failed to read profile localStorage: %w", err)
	}
	str, ok := result.(string)
	if !ok {
		return nil, fmt.Errorf("failed to read profile localStorage: unexpected result type %T", result)
	}
	if err := json.Unmarshal([]byte(str), &profile.LocalStorage); err != nil {
		return nil, fmt.Errorf("failed to decode profile localStorage: %w", err)
	}

	return profile, nil
}

// ApplyProfile restores cookies and localStorage from a Profile.
// This is the domain operation — it does not involve filesystem I/O.
func (s *Session) ApplyProfile(profile *Profile) error {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.ApplyProfileContext(ctx, profile)
}

func (s *Session) ApplyProfileContext(ctx context.Context, profile *Profile) error {
	_, err := s.ApplyProfileWithOutcomeContext(ctx, profile)
	return err
}

// ApplyProfileWithOutcomeContext restores profile state and returns a
// value-free receipt describing the verified result.
func (s *Session) ApplyProfileWithOutcomeContext(ctx context.Context, profile *Profile) (*ProfileResult, error) {
	if profile == nil {
		return nil, fmt.Errorf("profile is nil")
	}
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if err := s.checkExpectedTargetIDContext(ctx); err != nil {
		return nil, err
	}
	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}
	start, before := s.traceBeforeActionContext(ctx)
	if err := s.applyProfileInternalContext(ctx, profile); err != nil {
		return nil, s.traceActionFailureContext(ctx, start, before, "apply_profile", "browser profile", "", "", err)
	}
	s.traceAfterActionContext(ctx, start, before, "apply_profile", "browser profile", "", "", nil)
	result := &ProfileResult{
		Action: "apply_profile", TargetID: s.page.TargetID(), URL: profile.URL,
		Outcome: string(OutcomePostconditionVerified), Verified: true,
		CookiesRestored: len(profile.Cookies), StorageKeys: len(profile.LocalStorage),
		Evidence: fmt.Sprintf("cookie readback matched %d profile cookies and localStorage readback matched %d saved keys", len(profile.Cookies), len(profile.LocalStorage)),
	}
	result.OperationOrdinal = s.operationOrdinalForResult("apply_profile", "browser profile")
	result.Receipt = s.newActionReceipt(result.Action, "browser profile", result.TargetID, result.Outcome, result.Evidence, "", true, result.OperationOrdinal, nil, ctx)
	actionReceiptDeadline(ctx, result.Receipt)
	s.applyTraceResult("apply_profile", "browser profile", result.TargetID, result.Outcome, result.Evidence, "", true)
	return result, nil
}

func (s *Session) applyProfileInternalContext(ctx context.Context, profile *Profile) error {
	mutated := false
	for _, c := range profile.Cookies {
		if _, err := s.page.CallContext(ctx, "Network.enable", nil); err != nil {
			if mutationOutcomeMayBeUnknown(err) {
				return s.actionOutcomeAfterDispatch("load_profile_cookie", "profile cookies", "cookie restoration setup may have been dispatched; no cookie change was confirmed", err)
			}
			if mutated {
				return s.actionOutcomeAfterDispatch("load_profile", "profile cookies", "cookie restoration was partial before the next cookie could be set", err)
			}
			return err
		}
		raw, err := s.page.CallContext(ctx, "Network.setCookie", c)
		if err != nil {
			if mutationOutcomeMayBeUnknown(err) {
				return s.actionOutcomeAfterDispatch("load_profile_cookie", c.Name, "cookie restoration may have been dispatched", err)
			}
			if mutated {
				return s.actionOutcomeAfterDispatch("load_profile", c.Name, "profile restoration was partial before the next cookie could be set", err)
			}
			return fmt.Errorf("failed to restore cookie %q: %w", c.Name, err)
		}
		var setResult struct {
			Success *bool `json:"success"`
		}
		if err := json.Unmarshal(raw, &setResult); err == nil && setResult.Success != nil && !*setResult.Success {
			if mutated {
				return s.actionOutcomeAfterDispatch("load_profile", c.Name, "profile restoration was partial because the browser rejected a later cookie", fmt.Errorf("browser rejected cookie"))
			}
			return fmt.Errorf("browser rejected cookie %q", c.Name)
		}
		mutated = true
	}
	if len(profile.Cookies) > 0 {
		stored, err := s.cookiesContext(ctx)
		if err != nil {
			return s.actionOutcomeAfterDispatch("load_profile_cookie", "profile cookies", "cookies were restored but could not be verified", err)
		}
		for _, wanted := range profile.Cookies {
			found := false
			for _, got := range stored {
				if profileCookieMatches(got, wanted) {
					found = true
					break
				}
			}
			if !found {
				return s.actionOutcomeAfterDispatch("load_profile_cookie", wanted.Name, "cookie restoration completed but the cookie was absent on readback", fmt.Errorf("cookie readback did not match"))
			}
		}
	}

	localStorage := profile.LocalStorage
	if localStorage == nil {
		localStorage = map[string]string{}
	}
	lsJSON, err := json.Marshal(localStorage)
	if err != nil {
		if mutated {
			return s.actionOutcomeAfterDispatch("load_profile", "localStorage", "cookies were restored but localStorage could not be encoded", err)
		}
		return fmt.Errorf("failed to encode profile localStorage: %w", err)
	}
	js := fmt.Sprintf(`(function() {
			const items = %s;
			localStorage.clear();
			for (const [key, value] of Object.entries(items)) {
				localStorage.setItem(key, value);
			}
			return true;
		})()`, string(lsJSON))
	// The script clears storage before restoring keys. Once dispatched, a script
	// exception may leave a partially restored profile even when no cookie was
	// part of this operation, so classify all subsequent errors as uncertain.
	mutated = true
	if _, err := s.page.EvaluateContext(ctx, js); err != nil {
		return s.actionOutcomeAfterDispatch("load_profile_storage", "localStorage", "localStorage restoration may have partially changed storage before failing", err)
	}
	readback, err := s.page.EvaluateContext(ctx, `(function(){const items={};for(let i=0;i<localStorage.length;i++){const k=localStorage.key(i);items[k]=localStorage.getItem(k)}return JSON.stringify(items)})()`)
	if err != nil {
		return s.actionOutcomeAfterDispatch("load_profile_storage", "localStorage", "localStorage was updated but could not be verified", err)
	}
	var got map[string]string
	if str, ok := readback.(string); !ok || json.Unmarshal([]byte(str), &got) != nil {
		if mutated {
			return s.actionOutcomeAfterDispatch("load_profile_storage", "localStorage", "profile state changed but localStorage verification could not be decoded", fmt.Errorf("invalid localStorage readback"))
		}
		return fmt.Errorf("failed to decode restored localStorage")
	}
	if len(got) != len(profile.LocalStorage) {
		return s.actionOutcomeAfterDispatch("load_profile_storage", "localStorage", "localStorage readback contained a different number of keys than the saved profile", fmt.Errorf("storage readback did not match"))
	}
	for k, v := range profile.LocalStorage {
		if got[k] != v {
			return s.actionOutcomeAfterDispatch("load_profile_storage", k, "localStorage key did not match after restoration", fmt.Errorf("storage readback did not match"))
		}
	}

	return nil
}

// SaveProfile captures browser state and writes it to a JSON file.
// Convenience wrapper around CaptureProfile + file write.
func (s *Session) SaveProfile(path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.SaveProfileContext(ctx, path)
}

func (s *Session) SaveProfileContext(ctx context.Context, path string) error {
	profile, err := s.CaptureProfileContext(ctx)
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal profile: %w", err)
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("failed to create temporary profile file: %w", err)
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("failed to secure profile file permissions: %w", err)
	}
	n, err := tmp.Write(data)
	if err != nil {
		_ = tmp.Close()
		return fmt.Errorf("failed to write profile file: %w", err)
	}
	if n != len(data) {
		_ = tmp.Close()
		return fmt.Errorf("failed to write profile file: %w", io.ErrShortWrite)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("failed to sync profile file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to close profile file: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("failed to replace profile file: %w", err)
	}
	committed = true
	return nil
}

// LoadProfile reads a JSON profile file and applies it to the session.
// Convenience wrapper around file read + ApplyProfile.
func (s *Session) LoadProfile(path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.LoadProfileContext(ctx, path)
}

func (s *Session) LoadProfileContext(ctx context.Context, path string) error {
	_, err := s.LoadProfileWithOutcomeContext(ctx, path)
	return err
}

// LoadProfileWithOutcomeContext loads and restores a profile, returning
// metadata-only evidence after the origin reload completes.
func (s *Session) LoadProfileWithOutcomeContext(ctx context.Context, path string) (*ProfileResult, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if err := s.checkExpectedTargetIDContext(ctx); err != nil {
		return nil, err
	}
	start, before := s.traceBeforeActionContext(ctx)
	data, err := os.ReadFile(path) //nolint:gosec // G304: caller-controlled profile path is intentional
	if err != nil {
		return nil, s.traceActionFailureContext(ctx, start, before, "load_profile", "browser profile", "", "", fmt.Errorf("failed to read profile: %w", err))
	}

	var profile Profile
	if err := json.Unmarshal(data, &profile); err != nil {
		return nil, s.traceActionFailureContext(ctx, start, before, "load_profile", "browser profile", "", "", fmt.Errorf("failed to parse profile: %w", err))
	}
	// Load the saved origin first so localStorage is restored into the correct
	// origin. Reload after applying state so the page observes restored cookies
	// and storage during its normal startup.
	if profile.URL != "" {
		if err := s.ensurePageContext(ctx); err != nil {
			return nil, s.traceActionFailureContext(ctx, start, before, "load_profile", "browser profile", "", "", err)
		}
		if err := s.page.NavigateContext(ctx, profile.URL); err != nil {
			var outcomeUnknown *browse.NavigationOutcomeUnknownError
			if errors.As(err, &outcomeUnknown) {
				outcomeErr := s.actionOutcomeAfterDispatch("load_profile", profile.URL, "profile navigation may have completed but state restoration did not continue", err)
				return nil, s.traceActionFailureContext(ctx, start, before, "load_profile", "browser profile", "", "", outcomeErr)
			}
			return nil, s.traceActionFailureContext(ctx, start, before, "load_profile", "browser profile", "", "", fmt.Errorf("failed to open profile origin: %w", err))
		}
	}

	if err := s.ensurePageContext(ctx); err != nil {
		return nil, s.traceActionFailureContext(ctx, start, before, "load_profile", "browser profile", "", "", err)
	}
	if err := s.applyProfileInternalContext(ctx, &profile); err != nil {
		return nil, s.traceActionFailureContext(ctx, start, before, "load_profile", "browser profile", "", "", err)
	}
	if profile.URL != "" {
		if err := s.page.ReloadContext(ctx, false); err != nil {
			var outcomeUnknown *browse.NavigationOutcomeUnknownError
			if errors.As(err, &outcomeUnknown) {
				outcomeErr := s.actionOutcomeAfterDispatch("load_profile", profile.URL, "profile state was restored but reload completion could not be confirmed", err)
				return nil, s.traceActionFailureContext(ctx, start, before, "load_profile", "browser profile", "", "", outcomeErr)
			}
			return nil, s.traceActionFailureContext(ctx, start, before, "load_profile", "browser profile", "", "", fmt.Errorf("failed to reload profile origin: %w", err))
		}
	}
	s.traceAfterActionContext(ctx, start, before, "load_profile", "browser profile", "", "", nil)
	result := &ProfileResult{
		Action: "load_profile", TargetID: s.page.TargetID(), URL: profile.URL, Outcome: string(OutcomePostconditionVerified),
		Verified: true, CookiesRestored: len(profile.Cookies), StorageKeys: len(profile.LocalStorage),
		Evidence: fmt.Sprintf("loaded profile origin; cookie readback matched %d profile cookies and localStorage readback matched %d saved keys", len(profile.Cookies), len(profile.LocalStorage)),
	}
	result.OperationOrdinal = s.operationOrdinalForResult("load_profile", "browser profile")
	result.Receipt = s.newActionReceipt(result.Action, "browser profile", result.TargetID, result.Outcome, result.Evidence, "", true, result.OperationOrdinal, nil, ctx)
	actionReceiptDeadline(ctx, result.Receipt)
	s.applyTraceResult("load_profile", "browser profile", result.TargetID, result.Outcome, result.Evidence, "", true)
	return result, nil
}

// profileCookieMatches compares every cookie attribute persisted in a profile.
// A saved zero expiry denotes a session cookie; other expiry values must match.
func profileCookieMatches(actual, wanted browse.Cookie) bool {
	if actual.Name != wanted.Name || !cookieDomainMatches(actual.Domain, wanted.Domain) || actual.Path != wanted.Path ||
		actual.Value != wanted.Value || actual.Secure != wanted.Secure || actual.HTTPOnly != wanted.HTTPOnly ||
		actual.SameSite != wanted.SameSite {
		return false
	}
	if wanted.Expires == 0 {
		return actual.Expires == 0
	}
	return actual.Expires == wanted.Expires
}
