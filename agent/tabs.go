package agent

import (
	"fmt"

	browse "go.klarlabs.de/scout"
)

// tabEntry holds a named tab/page.
type tabEntry struct {
	name string
	page *browse.Page
}

// tabManager manages multiple named tabs within a session.
// Synchronization is provided by the enclosing Session.mu — tabManager
// methods are only ever called with that lock held.
type tabManager struct {
	tabs   map[string]*tabEntry
	active string
}

func newTabManager() *tabManager {
	return &tabManager{
		tabs: make(map[string]*tabEntry),
	}
}

// TabInfo describes an open tab.
type TabInfo struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	Title  string `json:"title"`
	Active bool   `json:"active"`
}

// OpenTab creates a new named tab and navigates to the URL.
// The new tab becomes the active tab.
func (s *Session) OpenTab(name, url string) (*PageResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensurePage(); err != nil {
		return nil, err
	}

	if s.tabs == nil {
		s.tabs = newTabManager()
		// Store the current page as "default" tab
		s.tabs.tabs["default"] = &tabEntry{name: "default", page: s.page}
		s.tabs.active = "default"
	}

	if _, exists := s.tabs.tabs[name]; exists {
		return nil, fmt.Errorf("tab %q already exists", name)
	}

	page, err := s.browser.NewPageAt(url)
	if err != nil {
		return nil, fmt.Errorf("failed to open tab at %s: %w", url, err)
	}
	_ = page.WaitLoad()

	s.tabs.tabs[name] = &tabEntry{name: name, page: page}
	s.tabs.active = name
	s.page = page
	s.diffInstalled = false

	return s.pageResult()
}

// SwitchTab activates a named tab.
func (s *Session) SwitchTab(name string) (*PageResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.tabs == nil {
		return nil, fmt.Errorf("no tabs open")
	}

	tab, ok := s.tabs.tabs[name]
	if !ok {
		return nil, fmt.Errorf("tab %q not found", name)
	}

	s.tabs.active = name
	s.page = tab.page
	s.diffInstalled = false

	return s.pageResult()
}

// CloseTab closes a named tab. Cannot close the active tab.
func (s *Session) CloseTab(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.tabs == nil {
		return fmt.Errorf("no tabs open")
	}

	if name == s.tabs.active {
		return fmt.Errorf("cannot close active tab %q — switch to another tab first", name)
	}

	tab, ok := s.tabs.tabs[name]
	if !ok {
		return fmt.Errorf("tab %q not found", name)
	}

	_ = tab.page.Close()
	delete(s.tabs.tabs, name)
	return nil
}

// ListTabs returns all open tabs with their URLs and titles.
func (s *Session) ListTabs() ([]TabInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.tabs == nil || len(s.tabs.tabs) == 0 {
		// Single page mode
		if s.page == nil {
			return nil, nil
		}
		url, _ := s.page.URL()
		title, _ := s.page.Evaluate(`document.title`)
		titleStr, _ := title.(string)
		return []TabInfo{{Name: "default", URL: url, Title: titleStr, Active: true}}, nil
	}

	tabs := make([]TabInfo, 0, len(s.tabs.tabs))
	for name, tab := range s.tabs.tabs {
		url, _ := tab.page.URL()
		title, _ := tab.page.Evaluate(`document.title`)
		titleStr, _ := title.(string)
		tabs = append(tabs, TabInfo{
			Name:   name,
			URL:    url,
			Title:  titleStr,
			Active: name == s.tabs.active,
		})
	}
	return tabs, nil
}
