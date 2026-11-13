package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	cdpclient "go.klarlabs.de/scout/internal/cdp"
	"go.klarlabs.de/scout/internal/launcher"
)

func TestRemoteSessionRecoveryRejectsBrowserGenerationChangeBehindDiscoveryURL(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	firstBrowser, err := launcher.LaunchContext(ctx, launcher.Options{Headless: true})
	if err != nil {
		t.Skipf("local Chrome unavailable: %v", err)
	}
	defer firstBrowser.Close()
	secondBrowser, err := launcher.LaunchContext(ctx, launcher.Options{Headless: true})
	if err != nil {
		t.Skipf("second local Chrome unavailable: %v", err)
	}
	defer secondBrowser.Close()

	var currentWebSocket atomic.Value
	currentWebSocket.Store(firstBrowser.WSEndpoint())
	discovery := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"webSocketDebuggerUrl": currentWebSocket.Load().(string)})
	}))
	defer discovery.Close()
	pageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><body><div id="state">original generation</div></body></html>`))
	}))
	defer pageServer.Close()
	store, err := OpenBoltTaskRunStore(filepath.Join(t.TempDir(), "generation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg := SessionConfig{RemoteCDP: discovery.URL, AllowedOrigins: []string{pageServer.URL}, AllowPrivateIPs: true}
	digest, err := SessionRecoveryPolicyDigest(cfg)
	if err != nil {
		t.Fatal(err)
	}
	session, err := NewSessionContext(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	page, err := session.NavigateWithPostconditionContext(ctx, pageServer.URL, Postcondition{Text: "original generation"})
	if err != nil {
		_ = session.Close()
		t.Fatal(err)
	}
	handle, err := NewSessionRecoveryHandle()
	if err != nil {
		_ = session.Close()
		t.Fatal(err)
	}
	record, err := session.PersistRecoveryRecord(ctx, store, handle, "principal-generation", "default", digest, time.Now().Add(time.Hour))
	if err != nil {
		_ = session.Close()
		t.Fatal(err)
	}
	if record.TargetID != page.TargetID {
		_ = session.Close()
		t.Fatalf("persisted target = %q, want %q", record.TargetID, page.TargetID)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	currentWebSocket.Store(secondBrowser.WSEndpoint())
	if _, _, err := ResumeSessionContext(ctx, cfg, store, handle, "principal-generation", digest); !errors.Is(err, ErrSessionRecoveryUnavailable) {
		t.Fatalf("resume after discovered browser generation changed = %v, want unavailable", err)
	}
}

func TestRemoteSessionRecoveryReattachesExactTarget(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	browser, err := launcher.LaunchContext(ctx, launcher.Options{Headless: true})
	if err != nil {
		t.Skipf("local Chrome unavailable: %v", err)
	}
	defer browser.Close()
	pageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><body><div id="state">recovered</div></body></html>`))
	}))
	defer pageServer.Close()
	storePath := filepath.Join(t.TempDir(), "recovery.db")
	store, err := OpenBoltTaskRunStore(storePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	cfg := SessionConfig{RemoteCDP: browser.WSEndpoint(), AllowedOrigins: []string{pageServer.URL}, AllowPrivateIPs: true}
	digest, err := SessionRecoveryPolicyDigest(cfg)
	if err != nil {
		t.Fatal(err)
	}
	session, err := NewSessionContext(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	page, err := session.NavigateWithPostconditionContext(ctx, pageServer.URL, Postcondition{Text: "recovered"})
	if err != nil {
		_ = session.Close()
		t.Fatal(err)
	}
	handle, err := NewSessionRecoveryHandle()
	if err != nil {
		_ = session.Close()
		t.Fatal(err)
	}
	record, err := session.PersistRecoveryRecord(ctx, store, handle, "principal-hash-a", "default", digest, time.Now().Add(time.Hour))
	if err != nil {
		_ = session.Close()
		t.Fatal(err)
	}
	if record.TargetID != page.TargetID || record.BrowserInstanceID == "" {
		_ = session.Close()
		t.Fatalf("persisted identity = %+v, want target %q and browser fingerprint", record, page.TargetID)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenBoltTaskRunStore(storePath)
	if err != nil {
		t.Fatal(err)
	}

	resumed, got, err := ResumeSessionContext(ctx, cfg, store, handle, "principal-hash-a", digest)
	if err != nil {
		t.Fatalf("ResumeSessionContext() error = %v", err)
	}
	defer resumed.Close()
	if got.TargetID != page.TargetID {
		t.Fatalf("resumed target = %q, want original %q", got.TargetID, page.TargetID)
	}
	state, err := resumed.ExtractContext(ctx, "#state")
	if err != nil || state.Text != "recovered" {
		t.Fatalf("resumed page state = (%q, %v), want recovered", state.Text, err)
	}
	if _, _, err := ResumeSessionContext(ctx, cfg, store, handle, "principal-hash-b", digest); !errors.Is(err, ErrSessionRecoveryUnavailable) {
		t.Fatalf("foreign-owner resume error = %v, want generic unavailable", err)
	}
	changedPolicy := cfg
	changedPolicy.AllowedOrigins = []string{"https://changed.example"}
	if _, _, err := ResumeSessionContext(ctx, changedPolicy, store, handle, "principal-hash-a", digest); !errors.Is(err, ErrSessionRecoveryUnavailable) {
		t.Fatalf("changed-policy resume error = %v, want generic unavailable", err)
	}
	if err := resumed.Close(); err != nil {
		t.Fatal(err)
	}
	connection, err := cdpclient.DialContext(ctx, browser.WSEndpoint())
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.CloseTargetCtx(ctx, record.TargetID); err != nil {
		_ = connection.Close()
		t.Fatal(err)
	}
	_ = connection.Close()
	if _, _, err := ResumeSessionContext(ctx, cfg, store, handle, "principal-hash-a", digest); !errors.Is(err, ErrSessionRecoveryUnavailable) {
		t.Fatalf("closed-target resume error = %v, want generic unavailable", err)
	}
}

func TestIsolatedRemoteSessionRecoveryReattachesExactContextAndTarget(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	browser, err := launcher.LaunchContext(ctx, launcher.Options{Headless: true})
	if err != nil {
		t.Skipf("local Chrome unavailable: %v", err)
	}
	defer browser.Close()
	pageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><body><div id="state">isolated-recovered</div></body></html>`))
	}))
	defer pageServer.Close()
	store, err := OpenBoltTaskRunStore(filepath.Join(t.TempDir(), "isolated-recovery.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg := SessionConfig{RemoteCDP: browser.WSEndpoint(), AllowedOrigins: []string{pageServer.URL}, AllowPrivateIPs: true, IsolateRemoteBrowserContext: true}
	digest, err := SessionRecoveryPolicyDigest(cfg)
	if err != nil {
		t.Fatal(err)
	}
	session, err := NewSessionContext(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	page, err := session.NavigateWithPostconditionContext(ctx, pageServer.URL, Postcondition{Text: "isolated-recovered"})
	if err != nil {
		_ = session.Close()
		t.Fatal(err)
	}
	contextProvider, ok := session.browser.(interface{ RemoteBrowserContextID() string })
	if !ok || contextProvider.RemoteBrowserContextID() == "" {
		_ = session.Close()
		t.Fatal("isolated session did not expose its remote context identity internally")
	}
	handle, err := NewSessionRecoveryHandle()
	if err != nil {
		_ = session.Close()
		t.Fatal(err)
	}
	record, err := session.PersistRecoveryRecord(ctx, store, handle, "principal-hash-isolated", "default", digest, time.Now().Add(time.Hour))
	if err != nil {
		_ = session.Close()
		t.Fatal(err)
	}
	if record.TargetID != page.TargetID || record.BrowserContextID != contextProvider.RemoteBrowserContextID() {
		_ = session.Close()
		t.Fatalf("persisted context/target = (%q,%q), want (%q,%q)", record.BrowserContextID, record.TargetID, contextProvider.RemoteBrowserContextID(), page.TargetID)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	resumed, got, err := ResumeSessionContext(ctx, cfg, store, handle, "principal-hash-isolated", digest)
	if err != nil {
		t.Fatalf("ResumeSessionContext(isolated) error = %v", err)
	}
	defer resumed.Close()
	if got.TargetID != page.TargetID || resumed.browserInstanceID() != record.BrowserInstanceID {
		t.Fatalf("resumed browser identity = (%q,%q), want (%q,%q)", resumed.browserInstanceID(), got.TargetID, record.BrowserInstanceID, page.TargetID)
	}
	state, err := resumed.ExtractContext(ctx, "#state")
	if err != nil || state.Text != "isolated-recovered" {
		t.Fatalf("resumed page state = (%q, %v), want isolated-recovered", state.Text, err)
	}
	if err := resumed.Close(); err != nil {
		t.Fatal(err)
	}
	connection, err := cdpclient.DialContext(ctx, browser.WSEndpoint())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := connection.DisposeBrowserContext(ctx, record.BrowserContextID); err != nil {
		t.Fatalf("dispose recovered context: %v", err)
	}
	if _, _, err := ResumeSessionContext(ctx, cfg, store, handle, "principal-hash-isolated", digest); !errors.Is(err, ErrSessionRecoveryUnavailable) {
		t.Fatalf("resume after context disposal error = %v, want unavailable", err)
	}
}

func TestSessionRecoveryPolicyDigestDoesNotExposeEndpoint(t *testing.T) {
	cfg := SessionConfig{RemoteCDP: "https://user:secret@example.test/devtools?token=secret", AllowedOrigins: []string{"https://app.example"}}
	digest, err := SessionRecoveryPolicyDigest(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if digest == "" || digest == cfg.RemoteCDP || containsAny(digest, "secret", "example.test", "app.example") {
		t.Fatalf("policy digest exposes source configuration: %q", digest)
	}
}
