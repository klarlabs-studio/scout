package agent

import (
	"encoding/json"
	"fmt"
	"os"

	browse "go.klarlabs.de/scout"
)

// Profile holds serializable browser state that can be saved/loaded across sessions.
type Profile struct {
	Cookies      []browse.Cookie   `json:"cookies,omitempty"`
	LocalStorage map[string]string `json:"local_storage,omitempty"`
}

// CaptureProfile extracts the current browser state (cookies + localStorage) as a Profile.
// This is the domain operation — it does not involve filesystem I/O.
func (s *Session) CaptureProfile() (*Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensurePage(); err != nil {
		return nil, err
	}

	profile := &Profile{}

	cookies, err := s.page.Cookies()
	if err == nil {
		profile.Cookies = cookies
	}

	result, err := s.page.Evaluate(`(function() {
		const items = {};
		for (let i = 0; i < localStorage.length; i++) {
			const key = localStorage.key(i);
			items[key] = localStorage.getItem(key);
		}
		return JSON.stringify(items);
	})()`)
	if err == nil {
		if str, ok := result.(string); ok {
			var ls map[string]string
			if json.Unmarshal([]byte(str), &ls) == nil {
				profile.LocalStorage = ls
			}
		}
	}

	return profile, nil
}

// ApplyProfile restores cookies and localStorage from a Profile.
// This is the domain operation — it does not involve filesystem I/O.
func (s *Session) ApplyProfile(profile *Profile) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensurePage(); err != nil {
		return err
	}

	for _, c := range profile.Cookies {
		_ = s.page.SetCookie(c)
	}

	if len(profile.LocalStorage) > 0 {
		lsJSON, _ := json.Marshal(profile.LocalStorage)
		js := fmt.Sprintf(`(function() {
			const items = %s;
			for (const [key, value] of Object.entries(items)) {
				localStorage.setItem(key, value);
			}
			return true;
		})()`, string(lsJSON))
		_, _ = s.page.Evaluate(js)
	}

	return nil
}

// SaveProfile captures browser state and writes it to a JSON file.
// Convenience wrapper around CaptureProfile + file write.
func (s *Session) SaveProfile(path string) error {
	profile, err := s.CaptureProfile()
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal profile: %w", err)
	}
	return os.WriteFile(path, data, 0o600)
}

// LoadProfile reads a JSON profile file and applies it to the session.
// Convenience wrapper around file read + ApplyProfile.
func (s *Session) LoadProfile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read profile: %w", err)
	}

	var profile Profile
	if err := json.Unmarshal(data, &profile); err != nil {
		return fmt.Errorf("failed to parse profile: %w", err)
	}

	return s.ApplyProfile(&profile)
}
