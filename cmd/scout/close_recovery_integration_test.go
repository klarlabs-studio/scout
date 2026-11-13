//go:build integration

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"go.klarlabs.de/scout/agent"
	"go.klarlabs.de/scout/internal/cdp"
	"go.klarlabs.de/scout/internal/launcher"
)

func TestDoctorRejectsLiveCDPWithUnresponsivePage(t *testing.T) {
	pageRequested := make(chan struct{}, 1)
	pageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<!doctype html><title>blocked</title><script>for (;;) {}</script>`)
		select {
		case pageRequested <- struct{}{}:
		default:
		}
	}))
	defer pageServer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	browser, err := launcher.LaunchContext(ctx, launcher.Options{Headless: true})
	if err != nil {
		t.Fatalf("LaunchContext: %v", err)
	}
	defer browser.Close()
	conn, err := cdp.DialContext(ctx, browser.WSEndpoint())
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer conn.Close()
	targets, err := conn.GetTargetsCtx(ctx)
	if err != nil {
		t.Fatalf("GetTargets: %v", err)
	}
	var targetID string
	for _, target := range targets {
		if target.Type == "page" {
			targetID = target.TargetID
			break
		}
	}
	if targetID == "" {
		t.Fatal("launched browser has no page target")
	}
	sessionID, err := conn.AttachToTargetCtx(ctx, targetID)
	if err != nil {
		t.Fatalf("AttachToTarget: %v", err)
	}
	if _, err := conn.CallSessionCtx(ctx, sessionID, "Page.enable", map[string]any{}); err != nil {
		t.Fatalf("Page.enable: %v", err)
	}
	if _, err := conn.CallSessionCtx(ctx, sessionID, "Page.navigate", map[string]any{"url": pageServer.URL}); err != nil {
		t.Fatalf("Page.navigate: %v", err)
	}
	select {
	case <-pageRequested:
	case <-ctx.Done():
		t.Fatal("renderer-blocking page was not requested")
	}
	readCtx, readCancel := context.WithTimeout(ctx, 200*time.Millisecond)
	_, rendererErr := conn.CallSessionCtx(readCtx, sessionID, "Runtime.evaluate", map[string]any{"expression": "document.readyState"})
	readCancel()
	if rendererErr == nil {
		t.Fatal("renderer unexpectedly answered Runtime.evaluate while executing an infinite loop")
	}

	setDoctorEnv := func(key, value string) func() {
		old, existed := os.LookupEnv(key)
		if err := os.Setenv(key, value); err != nil {
			t.Fatalf("Setenv %s: %v", key, err)
		}
		return func() {
			if existed {
				_ = os.Setenv(key, old)
			} else {
				_ = os.Unsetenv(key)
			}
		}
	}
	restoreRemote := setDoctorEnv("SCOUT_REMOTE_CDP", browser.WSEndpoint())
	defer restoreRemote()
	restoreOrigins := setDoctorEnv("SCOUT_MCP_ALLOWED_ORIGINS", "https://doctor-fixture.invalid")
	defer restoreOrigins()
	restorePrincipals := setDoctorEnv("SCOUT_MCP_HTTP_PRINCIPALS_JSON", "")
	defer restorePrincipals()
	restorePrivate := setDoctorEnv("SCOUT_ALLOW_PRIVATE_IPS", "")
	defer restorePrivate()

	report := runDoctorWithOptions("", "all", "", "", true, 2*time.Second)
	live := doctorCheckNamed(t, report, "browser_live")
	if live.Status != "error" || live.Code != "SCOUT_DOCTOR_BROWSER_LIVE_PAGE_READ_FAILED" {
		t.Fatalf("doctor browser_live = %+v, want responsive endpoint with failed bounded page read", live)
	}
	if cleanup := doctorCheckNamed(t, report, "browser_cleanup"); cleanup.Status != "ok" {
		t.Fatalf("doctor browser_cleanup = %+v, want ok", cleanup)
	}
	if report.Status != "error" {
		t.Fatalf("doctor status = %q, want error", report.Status)
	}
}

func TestDoctorReportsProbeTabWhenBrowserRejectsCleanup(t *testing.T) {
	pageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<!doctype html><title>doctor-cleanup</title><main>ready</main>`)
	}))
	defer pageServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	browser, err := launcher.LaunchContext(ctx, launcher.Options{Headless: true})
	if err != nil {
		t.Fatalf("LaunchContext: %v", err)
	}
	defer browser.Close()
	proxyURL, failedClose := failingTargetCloseProxy(t, browser.WSEndpoint())

	restore := setDoctorEnvironment(t, map[string]string{
		"SCOUT_REMOTE_CDP":               proxyURL,
		"SCOUT_ALLOW_PRIVATE_IPS":        "true",
		"SCOUT_MCP_ALLOWED_ORIGINS":      pageServer.URL,
		"SCOUT_MCP_HTTP_PRINCIPALS_JSON": "",
	})
	defer restore()

	originalConn, err := cdp.DialContext(ctx, browser.WSEndpoint())
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer originalConn.Close()
	before, err := originalConn.GetTargetsCtx(ctx)
	if err != nil {
		t.Fatalf("GetTargets before doctor: %v", err)
	}

	report := runDoctorWithProbeOptions("", "all", "", "", true, 10*time.Second, pageServer.URL, "")
	appProbe := doctorCheckNamed(t, report, "app_probe")
	if appProbe.Status != "error" || appProbe.Code != "SCOUT_DOCTOR_APP_PROBE_CLEANUP_FAILED" || appProbe.Remediation != "inspect_browser_tabs_and_restore_original_session" {
		t.Fatalf("app_probe = %+v, want failed temporary-tab cleanup diagnostic", appProbe)
	}
	var probeTarget string
	select {
	case probeTarget = <-failedClose:
	case <-ctx.Done():
		t.Fatal("doctor did not attempt to close the temporary probe tab")
	}
	if probeTarget == "" {
		t.Fatal("proxy observed closeTarget without a target ID")
	}

	after, err := originalConn.GetTargetsCtx(ctx)
	if err != nil {
		t.Fatalf("GetTargets after doctor: %v", err)
	}
	if !hasCDPTarget(after, probeTarget) {
		t.Fatalf("probe target %s absent after injected close failure; before=%+v after=%+v", probeTarget, before, after)
	}
	if hasCDPTarget(before, probeTarget) {
		t.Fatalf("probe target %s existed before doctor", probeTarget)
	}
	if cleanup := doctorCheckNamed(t, report, "browser_cleanup"); cleanup.Status != "ok" {
		t.Fatalf("browser_cleanup = %+v, want Scout disconnect to be reported separately", cleanup)
	}
}

func hasCDPTarget(targets []cdp.TargetInfo, targetID string) bool {
	for _, target := range targets {
		if target.TargetID == targetID {
			return true
		}
	}
	return false
}

func setDoctorEnvironment(t *testing.T, values map[string]string) func() {
	t.Helper()
	type previousValue struct {
		value   string
		existed bool
	}
	previous := make(map[string]previousValue, len(values))
	for key, value := range values {
		old, existed := os.LookupEnv(key)
		previous[key] = previousValue{value: old, existed: existed}
		if err := os.Setenv(key, value); err != nil {
			t.Fatalf("Setenv %s: %v", key, err)
		}
	}
	return func() {
		for key, old := range previous {
			if old.existed {
				_ = os.Setenv(key, old.value)
			} else {
				_ = os.Unsetenv(key)
			}
		}
	}
}

func failingTargetCloseProxy(t *testing.T, upstreamURL string) (string, <-chan string) {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	var target atomic.Value
	var shouldFail atomic.Bool
	failed := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		client, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		upstream, _, err := websocket.DefaultDialer.Dial(upstreamURL, nil)
		if err != nil {
			_ = client.Close()
			return
		}
		var clientWriteMu sync.Mutex
		var relay sync.WaitGroup
		relay.Add(2)
		go func() {
			defer relay.Done()
			defer upstream.Close()
			for {
				messageType, data, readErr := client.ReadMessage()
				if readErr != nil {
					return
				}
				var command struct {
					ID     json.RawMessage `json:"id"`
					Method string          `json:"method"`
					Params struct {
						TargetID string `json:"targetId"`
					} `json:"params"`
				}
				if json.Unmarshal(data, &command) == nil {
					switch command.Method {
					case "Target.createTarget":
						// The doctor's probe target is captured from the response below.
						shouldFail.Store(true)
					case "Target.closeTarget":
						if expected, ok := target.Load().(string); ok && expected != "" && expected == command.Params.TargetID && shouldFail.CompareAndSwap(true, false) {
							response, _ := json.Marshal(map[string]any{
								"id":    command.ID,
								"error": map[string]any{"code": -32000, "message": "injected Target.closeTarget failure"},
							})
							clientWriteMu.Lock()
							writeErr := client.WriteMessage(websocket.TextMessage, response)
							clientWriteMu.Unlock()
							select {
							case failed <- command.Params.TargetID:
							default:
							}
							if writeErr != nil {
								return
							}
							continue
						}
					}
				}
				if writeErr := upstream.WriteMessage(messageType, data); writeErr != nil {
					return
				}
			}
		}()
		go func() {
			defer relay.Done()
			defer client.Close()
			for {
				messageType, data, readErr := upstream.ReadMessage()
				if readErr != nil {
					return
				}
				var message struct {
					ID     json.RawMessage `json:"id"`
					Result struct {
						TargetID string `json:"targetId"`
					} `json:"result"`
				}
				if json.Unmarshal(data, &message) == nil && message.Result.TargetID != "" && shouldFail.Load() {
					target.Store(message.Result.TargetID)
				}
				clientWriteMu.Lock()
				writeErr := client.WriteMessage(messageType, data)
				clientWriteMu.Unlock()
				if writeErr != nil {
					return
				}
			}
		}()
		relay.Wait()
	}))
	t.Cleanup(server.Close)
	return "ws" + strings.TrimPrefix(server.URL, "http"), failed
}

func TestSwitchTabClosedTargetFailsFastAndKeepsQueueUsable(t *testing.T) {
	pageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<!doctype html><title>discardable</title><body>ready</body>`)
	}))
	defer pageServer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	browser, err := launcher.LaunchContext(ctx, launcher.Options{Headless: true})
	if err != nil {
		t.Fatalf("LaunchContext: %v", err)
	}
	defer browser.Close()
	session, err := agent.NewSessionContext(ctx, agent.SessionConfig{Headless: true, AllowPrivateIPs: true, Timeout: 8 * time.Second, RemoteCDP: browser.WSEndpoint()})
	if err != nil {
		t.Fatalf("NewSessionContext: %v", err)
	}
	defer session.Close()
	if _, err := session.Navigate(pageServer.URL); err != nil {
		t.Fatalf("Navigate default: %v", err)
	}
	if _, err := session.OpenTab("sleeping", pageServer.URL); err != nil {
		t.Fatalf("OpenTab sleeping: %v", err)
	}
	if _, err := session.SwitchTab("default"); err != nil {
		t.Fatalf("SwitchTab default: %v", err)
	}
	tabs, err := session.ListTabsContext(ctx)
	if err != nil {
		t.Fatalf("ListTabs: %v", err)
	}
	var sleepingTarget string
	for _, tab := range tabs {
		if tab.Name == "sleeping" {
			sleepingTarget = tab.TargetID
		}
	}
	conn, err := cdp.DialContext(ctx, browser.WSEndpoint())
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer conn.Close()
	if err := conn.CloseTargetCtx(ctx, sleepingTarget); err != nil {
		t.Fatalf("CloseTarget: %v", err)
	}
	switchCtx, switchCancel := context.WithTimeout(ctx, 6*time.Second)
	started := time.Now()
	result, err := session.SwitchTabContext(switchCtx, "sleeping")
	switchCancel()
	if time.Since(started) >= 6*time.Second {
		t.Fatalf("switch did not complete within its deadline: %s", time.Since(started))
	}
	if result != nil || err == nil {
		t.Fatalf("switch result = %+v, error = %v; closed targets must fail without hanging", result, err)
	}
	readCtx, readCancel := context.WithTimeout(ctx, 2*time.Second)
	defer readCancel()
	if _, err := session.SwitchTabContext(readCtx, "default"); err != nil {
		t.Fatalf("follow-up switch after failed target: %v", err)
	}
}

func TestSwitchTabActivationFailurePreservesSelectedPageAndFrame(t *testing.T) {
	frameServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<!doctype html><title>frame-still-selected</title><body>frame-ok</body>`)
	}))
	defer frameServer.Close()
	pageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprintf(w, `<!doctype html><title>default-page</title><iframe src=%q></iframe>`, frameServer.URL)
	}))
	defer pageServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	browser, err := launcher.LaunchContext(ctx, launcher.Options{Headless: true})
	if err != nil {
		t.Fatalf("LaunchContext: %v", err)
	}
	defer browser.Close()
	proxyURL, failActivation, activationFailed := failingTargetActivationProxy(t, browser.WSEndpoint())
	session, err := agent.NewSessionContext(ctx, agent.SessionConfig{
		Headless: true, AllowPrivateIPs: true, Timeout: 10 * time.Second, RemoteCDP: proxyURL,
	})
	if err != nil {
		t.Fatalf("NewSessionContext: %v", err)
	}
	defer session.Close()
	if _, err := session.Navigate(pageServer.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if _, err := session.OpenTab("blocked", frameServer.URL); err != nil {
		t.Fatalf("OpenTab blocked: %v", err)
	}
	if _, err := session.SwitchTabContext(ctx, "default"); err != nil {
		t.Fatalf("SwitchTab default: %v", err)
	}
	if _, err := session.SwitchToFrameContext(ctx, "iframe"); err != nil {
		t.Fatalf("SwitchToFrame iframe: %v", err)
	}
	before, err := session.EvalContext(ctx, "location.href")
	if err != nil || strings.TrimRight(fmt.Sprint(before), "/") != strings.TrimRight(frameServer.URL, "/") {
		t.Fatalf("selected frame URL=%v err=%v, want %q", before, err, frameServer.URL)
	}
	tabsBefore, err := session.ListTabsContext(ctx)
	if err != nil {
		t.Fatalf("ListTabs before injected failure: %v", err)
	}
	var defaultTarget, blockedTarget string
	for _, tab := range tabsBefore {
		switch tab.Name {
		case "default":
			defaultTarget = tab.TargetID
		case "blocked":
			blockedTarget = tab.TargetID
		}
	}
	if defaultTarget == "" {
		t.Fatalf("default target missing: %+v", tabsBefore)
	}

	debugConn, err := cdp.DialContext(ctx, browser.WSEndpoint())
	if err != nil {
		t.Fatalf("DialContext for blocked renderer: %v", err)
	}
	defer debugConn.Close()
	if blockedTarget == "" {
		t.Fatalf("blocked tab target missing: %+v", tabsBefore)
	}
	debugSession, err := debugConn.AttachToTargetCtx(ctx, blockedTarget)
	if err != nil {
		t.Fatalf("AttachToTarget blocked tab: %v", err)
	}
	if _, err := debugConn.CallSessionCtx(ctx, debugSession, "Page.enable", map[string]any{}); err != nil {
		t.Fatalf("Page.enable blocked tab: %v", err)
	}
	blockDone := make(chan error, 1)
	go func() {
		_, evalErr := debugConn.CallSessionCtx(ctx, debugSession, "Runtime.evaluate", map[string]any{
			"expression": "for (;;) {}", "awaitPromise": false,
		})
		blockDone <- evalErr
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		probeCtx, probeCancel := context.WithTimeout(ctx, 80*time.Millisecond)
		_, probeErr := debugConn.CallSessionCtx(probeCtx, debugSession, "Runtime.evaluate", map[string]any{
			"expression": "1", "returnByValue": true,
		})
		probeCancel()
		if probeErr != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("renderer did not become unresponsive")
		}
		time.Sleep(20 * time.Millisecond)
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
		defer closeCancel()
		_, _ = debugConn.CallSessionCtx(closeCtx, "", "Target.closeTarget", map[string]string{"targetId": blockedTarget})
		select {
		case <-blockDone:
		case <-time.After(time.Second):
		}
	}()

	failActivation(blockedTarget)
	_, switchErr := session.SwitchTabContext(ctx, "blocked")
	if switchErr == nil || !strings.Contains(switchErr.Error(), "injected Target.activateTarget failure") {
		t.Fatalf("SwitchTab error = %v, want injected target activation error", switchErr)
	}
	select {
	case target := <-activationFailed:
		if target != blockedTarget {
			t.Fatalf("activation failure target=%q want %q", target, blockedTarget)
		}
	case <-ctx.Done():
		t.Fatal("activation fault was not exercised")
	}

	tabsAfter, err := session.ListTabsContext(ctx)
	if err != nil {
		t.Fatalf("ListTabs after activation failure: %v", err)
	}
	var defaultAfter, blockedAfter *agent.TabInfo
	for i := range tabsAfter {
		switch tabsAfter[i].Name {
		case "default":
			defaultAfter = &tabsAfter[i]
		case "blocked":
			blockedAfter = &tabsAfter[i]
		}
	}
	if defaultAfter == nil || !defaultAfter.Active || defaultAfter.TargetID != defaultTarget || defaultAfter.MetadataStale {
		t.Fatalf("default tab state after activation failure = %+v, tabs=%+v", defaultAfter, tabsAfter)
	}
	if blockedAfter == nil || !blockedAfter.MetadataStale || strings.TrimRight(blockedAfter.URL, "/") != strings.TrimRight(frameServer.URL, "/") {
		t.Fatalf("blocked tab metadata after activation failure = %+v, tabs=%+v", blockedAfter, tabsAfter)
	}

	after, err := session.EvalContext(ctx, "location.href")
	if err != nil || strings.TrimRight(fmt.Sprint(after), "/") != strings.TrimRight(frameServer.URL, "/") {
		t.Fatalf("selected frame after activation failure URL=%v err=%v, want %q", after, err, frameServer.URL)
	}
	selectedTitle, err := session.EvalContext(ctx, "document.title")
	if err != nil || selectedTitle != "frame-still-selected" {
		t.Fatalf("selected frame after activation failure title=%v err=%v, want frame-still-selected", selectedTitle, err)
	}
}

func TestSwitchTabDialogPausedTargetReportsUnknownAndQueueRecovers(t *testing.T) {
	pageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<!doctype html><title>dialog fixture</title><body>ready</body>`)
	}))
	defer pageServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	browser, err := launcher.LaunchContext(ctx, launcher.Options{Headless: true})
	if err != nil {
		t.Fatalf("LaunchContext: %v", err)
	}
	defer browser.Close()
	session, err := agent.NewSessionContext(ctx, agent.SessionConfig{Headless: true, AllowPrivateIPs: true, Timeout: 8 * time.Second, RemoteCDP: browser.WSEndpoint()})
	if err != nil {
		t.Fatalf("NewSessionContext: %v", err)
	}
	defer session.Close()
	if _, err := session.Navigate(pageServer.URL); err != nil {
		t.Fatalf("Navigate default: %v", err)
	}
	if _, err := session.OpenTab("paused", pageServer.URL); err != nil {
		t.Fatalf("OpenTab paused: %v", err)
	}
	if _, err := session.SwitchTab("default"); err != nil {
		t.Fatalf("SwitchTab default: %v", err)
	}
	tabs, err := session.ListTabsContext(ctx)
	if err != nil {
		t.Fatalf("ListTabs: %v", err)
	}
	var pausedTarget string
	for _, tab := range tabs {
		if tab.Name == "paused" {
			pausedTarget = tab.TargetID
		}
	}
	if pausedTarget == "" {
		t.Fatalf("paused target missing: %+v", tabs)
	}

	conn, err := cdp.DialContext(ctx, browser.WSEndpoint())
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer conn.Close()
	sessionID, err := conn.AttachToTargetCtx(ctx, pausedTarget)
	if err != nil {
		t.Fatalf("AttachToTarget: %v", err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Second)
		defer cleanupCancel()
		_ = conn.DetachFromTargetCtx(cleanupCtx, sessionID)
	}()
	if _, err := conn.CallSessionCtx(ctx, sessionID, "Page.enable", map[string]any{}); err != nil {
		t.Fatalf("Page.enable: %v", err)
	}
	dialogOpened := make(chan struct{}, 1)
	removeDialogHandler := conn.OnSession(sessionID, "Page.javascriptDialogOpening", func(json.RawMessage) {
		select {
		case dialogOpened <- struct{}{}:
		default:
		}
	})
	defer removeDialogHandler()
	evalDone := make(chan error, 1)
	go func() {
		_, err := conn.CallSessionCtx(ctx, sessionID, "Runtime.evaluate", map[string]any{"expression": "alert('paused')"})
		evalDone <- err
	}()
	select {
	case <-dialogOpened:
	case <-ctx.Done():
		t.Fatal("JavaScript dialog did not open")
	}

	switchCtx, switchCancel := context.WithTimeout(ctx, 5*time.Second)
	result, switchErr := session.SwitchTabContext(switchCtx, "paused")
	switchCancel()
	var outcome *agent.ActionOutcomeError
	if result != nil || !errors.As(switchErr, &outcome) || outcome.Outcome != agent.OutcomeUnknown {
		t.Fatalf("dialog-paused switch result=%+v err=%v; want unknown action outcome", result, switchErr)
	}
	if outcome.TargetID != pausedTarget {
		t.Fatalf("unknown switch target=%q, want %q", outcome.TargetID, pausedTarget)
	}

	if _, err := conn.CallSessionCtx(ctx, sessionID, "Page.handleJavaScriptDialog", map[string]any{"accept": true}); err != nil {
		t.Fatalf("dismiss JavaScript dialog: %v", err)
	}
	select {
	case evalErr := <-evalDone:
		if evalErr != nil {
			t.Fatalf("dialog evaluation after dismissal: %v", evalErr)
		}
	case <-ctx.Done():
		t.Fatal("dialog evaluation did not finish after dismissal")
	}
	activeTabs, err := session.ListTabsContext(ctx)
	if err != nil {
		t.Fatalf("ListTabs after dialog dismissal: %v", err)
	}
	pausedActive := false
	for _, tab := range activeTabs {
		if tab.Name == "paused" && !tab.Active {
			t.Fatalf("Scout did not retain the requested tab after uncertain activation: %+v", activeTabs)
		}
		if tab.Name == "paused" && tab.Active {
			pausedActive = true
		}
	}
	if !pausedActive {
		t.Fatalf("paused tab missing after dialog dismissal: %+v", activeTabs)
	}
	if _, err := session.SwitchTabContext(ctx, "default"); err != nil {
		t.Fatalf("follow-up switch after dialog dismissal: %v", err)
	}
	value, err := session.Eval("document.title")
	if err != nil || value != "dialog fixture" {
		t.Fatalf("follow-up page evaluation value=%v err=%v", value, err)
	}
	if _, err := session.OpenTab("close-paused", pageServer.URL); err != nil {
		t.Fatalf("OpenTab close-paused: %v", err)
	}
	if _, err := session.SwitchTab("default"); err != nil {
		t.Fatalf("SwitchTab default before close: %v", err)
	}
	tabs, err = session.ListTabsContext(ctx)
	if err != nil {
		t.Fatalf("ListTabs before close: %v", err)
	}
	var closeTarget string
	for _, tab := range tabs {
		if tab.Name == "close-paused" {
			closeTarget = tab.TargetID
		}
	}
	if closeTarget == "" {
		t.Fatalf("close-paused target missing: %+v", tabs)
	}
	closeSessionID, err := conn.AttachToTargetCtx(ctx, closeTarget)
	if err != nil {
		t.Fatalf("AttachToTarget close-paused: %v", err)
	}
	if _, err := conn.CallSessionCtx(ctx, closeSessionID, "Page.enable", map[string]any{}); err != nil {
		t.Fatalf("Page.enable close-paused: %v", err)
	}
	closeDialogOpened := make(chan struct{}, 1)
	removeCloseDialogHandler := conn.OnSession(closeSessionID, "Page.javascriptDialogOpening", func(json.RawMessage) {
		select {
		case closeDialogOpened <- struct{}{}:
		default:
		}
	})
	defer removeCloseDialogHandler()
	closeEvalDone := make(chan error, 1)
	go func() {
		_, err := conn.CallSessionCtx(ctx, closeSessionID, "Runtime.evaluate", map[string]any{"expression": "alert('close paused')"})
		closeEvalDone <- err
	}()
	select {
	case <-closeDialogOpened:
	case <-ctx.Done():
		t.Fatal("close-tab JavaScript dialog did not open")
	}
	closeResult, err := session.CloseTabResultContext(ctx, "close-paused")
	if err != nil || closeResult == nil || !closeResult.Verified || closeResult.TargetID != closeTarget {
		t.Fatalf("close paused tab result=%+v err=%v", closeResult, err)
	}
	targets, err := conn.GetTargetsCtx(ctx)
	if err != nil {
		t.Fatalf("GetTargets after close: %v", err)
	}
	for _, target := range targets {
		if target.TargetID == closeTarget {
			t.Fatalf("dialog-paused target %s remains open after verified close", closeTarget)
		}
	}
	if _, err := session.SwitchTabContext(ctx, "default"); err != nil {
		t.Fatalf("follow-up switch after dialog-paused close: %v", err)
	}
	value, err = session.Eval("document.title")
	if err != nil || value != "dialog fixture" {
		t.Fatalf("post-close page evaluation value=%v err=%v", value, err)
	}
	select {
	case <-closeEvalDone:
	case <-time.After(time.Second):
		// Closing a target with an open JavaScript dialog disconnects its
		// renderer call; the important postcondition is target absence.
	}
}

func TestSwitchTabRebindsUniquelyMatchedUrgentlyDiscardedTarget(t *testing.T) {
	pageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		title := "default recovery fixture"
		if r.URL.Path == "/candidate" {
			title = "discard recovery fixture"
		}
		_, _ = fmt.Fprintf(w, `<!doctype html><title>%s</title><body>ready</body>`, title)
	}))
	defer pageServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	browser, err := launcher.LaunchContext(ctx, launcher.Options{Headless: true})
	if err != nil {
		t.Fatalf("LaunchContext: %v", err)
	}
	defer browser.Close()
	session, err := agent.NewSessionContext(ctx, agent.SessionConfig{Headless: true, AllowPrivateIPs: true, Timeout: 8 * time.Second, RemoteCDP: browser.WSEndpoint()})
	if err != nil {
		t.Fatalf("NewSessionContext: %v", err)
	}
	defer session.Close()
	if _, err := session.Navigate(pageServer.URL + "/default"); err != nil {
		t.Fatalf("Navigate default: %v", err)
	}
	candidateURL := pageServer.URL + "/candidate"
	if _, err := session.OpenTab("discarded", candidateURL); err != nil {
		t.Fatalf("OpenTab discarded: %v", err)
	}
	if _, err := session.SwitchTabContext(ctx, "default"); err != nil {
		t.Fatalf("SwitchTab default: %v", err)
	}
	tabs, err := session.ListTabsContext(ctx)
	if err != nil {
		t.Fatalf("ListTabs: %v", err)
	}
	var oldTarget, defaultTarget, discardedURL string
	for _, tab := range tabs {
		switch tab.Name {
		case "discarded":
			oldTarget = tab.TargetID
			discardedURL = tab.URL
		case "default":
			defaultTarget = tab.TargetID
		}
	}
	if oldTarget == "" || defaultTarget == "" {
		t.Fatalf("discarded target missing: %+v", tabs)
	}
	if _, err := session.SwitchTabContext(ctx, "discarded"); err != nil {
		t.Fatalf("SwitchTab discarded before detach: %v", err)
	}
	if err := session.Page().DetachContext(ctx); err != nil {
		t.Fatalf("detach Scout from discarded candidate: %v", err)
	}
	if _, err := session.SwitchTabContext(ctx, "default"); err != nil {
		t.Fatalf("SwitchTab default after detach: %v", err)
	}

	conn, err := cdp.DialContext(ctx, browser.WSEndpoint())
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer conn.Close()
	debugTarget, err := conn.CreateTargetCtx(ctx, "chrome://chrome-urls")
	if err != nil {
		t.Fatalf("create Chrome internal debug page: %v", err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Second)
		defer cleanupCancel()
		_ = conn.CloseTargetCtx(cleanupCtx, debugTarget)
	}()
	debugSession, err := conn.AttachToTargetCtx(ctx, debugTarget)
	if err != nil {
		t.Fatalf("attach Chrome internal debug page: %v", err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Second)
		defer cleanupCancel()
		_ = conn.DetachFromTargetCtx(cleanupCtx, debugSession)
	}()
	if _, err := conn.CallSessionCtx(ctx, debugSession, "Runtime.enable", map[string]any{}); err != nil {
		t.Fatalf("Runtime.enable Chrome internal debug page: %v", err)
	}
	// Older builds require enabling the page through a button on
	// chrome://chrome-urls. Wait for its WebUI component before clicking.
	if err := evalCDPBoolean(ctx, conn, debugSession, `(async () => { for (let i = 0; i < 50; i++) { const app = document.querySelector('chrome-urls-app'); const button = app && app.shadowRoot && app.shadowRoot.querySelector('cr-button'); if (button) { button.click(); return true; } await new Promise(resolve => setTimeout(resolve, 100)); } return true; })()`); err != nil {
		t.Skipf("Chrome internal URL page is unavailable: %v", err)
	}
	if _, err := conn.CallSessionCtx(ctx, debugSession, "Page.navigate", map[string]any{"url": "chrome://discards"}); err != nil {
		t.Fatalf("navigate to chrome://discards: %v", err)
	}
	if _, err := conn.CallSessionCtx(ctx, debugSession, "Page.enable", map[string]any{}); err != nil {
		t.Fatalf("Page.enable Chrome discard page: %v", err)
	}
	if err := evalCDPBoolean(ctx, conn, debugSession, fmt.Sprintf(`(async () => { for (let i = 0; i < 80; i++) { const host = document.querySelector('discards-main'); const tab = host && host.shadowRoot && host.shadowRoot.querySelector('discards-tab'); const rows = tab && tab.shadowRoot && tab.shadowRoot.querySelectorAll('tbody tr'); if (rows && [...rows].some(r => r.innerText.includes(%q))) return true; await new Promise(resolve => setTimeout(resolve, 100)); } return false; })()`, "discard recovery fixture")); err != nil {
		t.Skipf("Chrome discard page did not expose a usable tab inventory: %v", err)
	}
	clickDiscard := fmt.Sprintf(`(() => { const host = document.querySelector('discards-main'); const tab = host && host.shadowRoot && host.shadowRoot.querySelector('discards-tab'); const rows = tab && tab.shadowRoot && [...tab.shadowRoot.querySelectorAll('tbody tr')]; const row = rows && rows.find(r => r.innerText.includes(%q) && r.innerText.includes(%q)); if (!row) return false; const action = [...row.querySelectorAll('[is="action-link"]')].find(e => e.innerText.includes('Urgent Discard')); if (!action) return false; action.click(); return true; })()`, "discard recovery fixture", candidateURL)
	if err := evalCDPBoolean(ctx, conn, debugSession, clickDiscard); err != nil {
		t.Skipf("Chrome discard control is unavailable for this target: %v", err)
	}
	var replacement string
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		current, targetErr := conn.GetTargetsCtx(ctx)
		if targetErr != nil {
			t.Fatalf("GetTargets after urgent discard: %v", targetErr)
		}
		for _, target := range current {
			if target.Type == "page" && target.TargetID != oldTarget && target.TargetID != defaultTarget && target.URL == discardedURL && target.Title == "discard recovery fixture" {
				replacement = target.TargetID
				break
			}
		}
		if replacement != "" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if replacement == "" {
		t.Fatalf("urgent discard did not replace target %s; Chrome targets=%+v", oldTarget, mustTargets(t, conn, ctx))
	}
	if _, err := session.OpenTab("registered-twin", candidateURL); err != nil {
		t.Fatalf("OpenTab registered twin: %v", err)
	}
	if _, err := session.SwitchTabContext(ctx, "default"); err != nil {
		t.Fatalf("SwitchTab default after opening registered twin: %v", err)
	}
	duplicateTarget, err := conn.CreateTargetCtx(ctx, candidateURL)
	if err != nil {
		t.Fatalf("create unregistered duplicate target: %v", err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Second)
		defer cleanupCancel()
		_ = conn.CloseTargetCtx(cleanupCtx, duplicateTarget)
	}()
	duplicateReady := false
	for until := time.Now().Add(5 * time.Second); time.Now().Before(until); {
		current, targetErr := conn.GetTargetsCtx(ctx)
		if targetErr != nil {
			t.Fatalf("GetTargets for duplicate target: %v", targetErr)
		}
		for _, target := range current {
			if target.TargetID == duplicateTarget && target.URL == discardedURL && target.Title == "discard recovery fixture" {
				duplicateReady = true
				break
			}
		}
		if duplicateReady {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !duplicateReady {
		t.Fatalf("duplicate target did not finish loading: %+v", mustTargets(t, conn, ctx))
	}
	if ambiguous, ambiguousErr := session.SwitchTabContext(ctx, "discarded"); ambiguous != nil || ambiguousErr == nil {
		t.Fatalf("ambiguous replacement switch result=%+v err=%v; want a fail-closed error", ambiguous, ambiguousErr)
	}
	activeTitle, activeErr := session.Eval("document.title")
	if activeErr != nil || activeTitle != "default recovery fixture" {
		t.Fatalf("ambiguous replacement changed active page title=%v err=%v", activeTitle, activeErr)
	}
	if err := conn.CloseTargetCtx(ctx, duplicateTarget); err != nil {
		t.Fatalf("close duplicate target: %v", err)
	}
	result, err := session.SwitchTabContext(ctx, "discarded")
	if err != nil {
		t.Fatalf("SwitchTab should rebind uniquely matched discarded target: %v", err)
	}
	if result == nil || result.TargetID != replacement || !strings.Contains(result.Evidence, "unique browser replacement") {
		t.Fatalf("rebound result = %+v; want replacement target %s and recovery evidence", result, replacement)
	}
	value, err := session.Eval("document.title")
	if err != nil || value != "discard recovery fixture" {
		t.Fatalf("evaluation after rebind value=%v err=%v", value, err)
	}
}

func evalCDPBoolean(ctx context.Context, conn *cdp.Conn, sessionID, expression string) error {
	raw, err := conn.CallSessionCtx(ctx, sessionID, "Runtime.evaluate", map[string]any{"expression": expression, "awaitPromise": true, "returnByValue": true})
	if err != nil {
		return err
	}
	var response struct {
		Result struct {
			Value bool `json:"value"`
		} `json:"result"`
		ExceptionDetails *json.RawMessage `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return err
	}
	if response.ExceptionDetails != nil {
		return fmt.Errorf("evaluation raised an exception: %s", string(*response.ExceptionDetails))
	}
	if !response.Result.Value {
		return errors.New("evaluation returned false")
	}
	return nil
}

func mustTargets(t *testing.T, conn *cdp.Conn, ctx context.Context) []cdp.TargetInfo {
	t.Helper()
	targets, err := conn.GetTargetsCtx(ctx)
	if err != nil {
		t.Fatalf("GetTargets: %v", err)
	}
	return targets
}

func TestCloseTabTimeoutReconcilesLateCloseAndRepairsRegistry(t *testing.T) {
	pageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<!doctype html><html><body><h1>Ready</h1></body></html>`)
	}))
	defer pageServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	browser, err := launcher.LaunchContext(ctx, launcher.Options{Headless: true})
	if err != nil {
		t.Fatalf("LaunchContext: %v", err)
	}
	defer browser.Close()
	proxyURL, closeReplyForwarded := delayedCloseResponseProxy(t, browser.WSEndpoint(), 500*time.Millisecond)
	session, err := agent.NewSessionContext(ctx, agent.SessionConfig{Headless: true, AllowPrivateIPs: true, Timeout: 10 * time.Second, RemoteCDP: proxyURL})
	if err != nil {
		t.Fatalf("NewSessionContext: %v", err)
	}
	defer session.Close()
	if _, err := session.Navigate(pageServer.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if _, err := session.OpenTab("slow-close", pageServer.URL); err != nil {
		t.Fatalf("OpenTab: %v", err)
	}
	openedTabs, err := session.ListTabsContext(ctx)
	if err != nil {
		t.Fatalf("ListTabs: %v", err)
	}
	var targetID string
	for _, tab := range openedTabs {
		if tab.Name == "slow-close" {
			targetID = tab.TargetID
		}
	}
	if targetID == "" {
		t.Fatalf("slow-close target missing from tab inventory: %+v", openedTabs)
	}
	if _, err := session.SwitchTab("default"); err != nil {
		t.Fatalf("SwitchTab: %v", err)
	}
	closeRequest, err := json.Marshal(sessionRequest{Command: "close_tab", Tab: "slow-close", TabTargetID: targetID, TimeoutMS: 150})
	if err != nil {
		t.Fatalf("encode close request: %v", err)
	}
	var closeOutput strings.Builder
	if err := runSessionJSONL(session, strings.NewReader(string(closeRequest)+"\n"), &closeOutput, nil); err != nil {
		t.Fatalf("run close request through session CLI: %v", err)
	}
	var closeResponse sessionResponse
	if err := json.Unmarshal([]byte(closeOutput.String()), &closeResponse); err != nil {
		t.Fatalf("decode close response: %v\n%s", err, closeOutput.String())
	}
	if closeResponse.OK || closeResponse.ErrorDetails == nil || closeResponse.ErrorDetails.Outcome != string(agent.OutcomeUnknown) {
		t.Fatalf("close response = %+v, want unknown outcome", closeResponse)
	}
	if closeResponse.ErrorDetails.TargetID != targetID || closeResponse.ErrorDetails.ExpectedPostcondition == nil || closeResponse.ErrorDetails.ExpectedPostcondition.TargetAbsent != targetID {
		t.Fatalf("CLI close error lost target identity or absence predicate: %+v", closeResponse.ErrorDetails)
	}
	if closeResponse.RecoveryRecord == nil {
		t.Fatalf("CLI close response omitted portable recovery record: %+v", closeResponse)
	}

	select {
	case <-closeReplyForwarded:
	case <-ctx.Done():
		t.Fatal("timed out waiting for delayed close response to be forwarded")
	}
	closer, err := cdp.DialContext(ctx, browser.WSEndpoint())
	if err != nil {
		t.Fatalf("connect target inventory client: %v", err)
	}
	defer closer.Close()
	targets, err := closer.GetTargetsCtx(ctx)
	if err != nil {
		t.Fatalf("GetTargets after late close: %v", err)
	}
	for _, target := range targets {
		if target.TargetID == targetID {
			t.Fatalf("late close response was returned but target %s remains present", targetID)
		}
	}

	data, err := json.Marshal(closeResponse.RecoveryRecord)
	if err != nil {
		t.Fatalf("Marshal recovery record: %v", err)
	}
	reconcileRequest, err := json.Marshal(sessionRequest{Command: "reconcile", Options: map[string]string{"recovery_record": string(data)}})
	if err != nil {
		t.Fatalf("encode reconcile request: %v", err)
	}
	var reconcileOutput strings.Builder
	if err := runSessionJSONL(session, strings.NewReader(string(reconcileRequest)+"\n"), &reconcileOutput, nil); err != nil {
		t.Fatalf("run reconcile request through session CLI: %v", err)
	}
	var reconcileResponse sessionResponse
	if err := json.Unmarshal([]byte(reconcileOutput.String()), &reconcileResponse); err != nil {
		t.Fatalf("decode reconcile response: %v\n%s", err, reconcileOutput.String())
	}
	if !reconcileResponse.OK {
		t.Fatalf("reconcile response = %+v", reconcileResponse)
	}
	reconciled, ok := reconcileResponse.Result.(map[string]any)
	if !ok || reconciled["verified"] != true || reconciled["outcome"] != string(agent.OutcomePostconditionVerified) {
		t.Fatalf("reconcile result = %#v, want verified late close", reconcileResponse.Result)
	}
	tabs, err := session.ListTabsContext(ctx)
	if err != nil {
		t.Fatalf("ListTabs after reconciliation: %v", err)
	}
	for _, tab := range tabs {
		if tab.Name == "slow-close" {
			t.Fatalf("reconciled late-close tab remained in registry: %+v", tabs)
		}
	}
}

func TestPortableRecoveryRejectsDifferentContextOnSameBrowser(t *testing.T) {
	pageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<!doctype html><html><body><h1>Context</h1></body></html>`)
	}))
	defer pageServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	browser, err := launcher.LaunchContext(ctx, launcher.Options{Headless: true})
	if err != nil {
		t.Fatalf("LaunchContext: %v", err)
	}
	defer browser.Close()

	first, err := agent.NewSessionContext(ctx, agent.SessionConfig{
		Headless: true, AllowPrivateIPs: true, Timeout: 10 * time.Second,
		RemoteCDP: browser.WSEndpoint(), IsolateRemoteBrowserContext: true,
	})
	if err != nil {
		t.Fatalf("start first isolated session: %v", err)
	}
	defer first.Close()
	if _, err := first.Navigate(pageServer.URL); err != nil {
		t.Fatalf("navigate first session: %v", err)
	}
	opened, err := first.OpenTab("context-owned", pageServer.URL)
	if err != nil {
		t.Fatalf("open context-owned tab: %v", err)
	}
	if _, err := first.SwitchTab("default"); err != nil {
		t.Fatalf("switch first session to default: %v", err)
	}
	recovery, err := agent.NewActionRecoveryRecord(&agent.ActionOutcomeError{
		RunID: "context-a-run", OperationOrdinal: opened.OperationOrdinal,
		Action: "close_tab", Target: "context-owned", TargetID: opened.TargetID,
		BrowserInstanceID:     opened.Receipt.BrowserInstanceID,
		Outcome:               agent.OutcomeUnknown,
		ExpectedPostcondition: &agent.Postcondition{TargetAbsent: opened.TargetID},
	})
	if err != nil {
		t.Fatalf("create context A recovery record: %v", err)
	}

	second, err := agent.NewSessionContext(ctx, agent.SessionConfig{
		Headless: true, AllowPrivateIPs: true, Timeout: 10 * time.Second,
		RemoteCDP: browser.WSEndpoint(), IsolateRemoteBrowserContext: true,
	})
	if err != nil {
		t.Fatalf("start second isolated session: %v", err)
	}
	defer second.Close()
	if _, err := second.Navigate(pageServer.URL); err != nil {
		t.Fatalf("navigate second session: %v", err)
	}
	closer, err := cdp.DialContext(ctx, browser.WSEndpoint())
	if err != nil {
		t.Fatalf("connect target inventory client: %v", err)
	}
	defer closer.Close()
	if err := closer.CloseTargetCtx(ctx, opened.TargetID); err != nil {
		t.Fatalf("close context A target: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		targets, err := closer.GetTargetsCtx(ctx)
		if err != nil {
			t.Fatalf("read target inventory after close: %v", err)
		}
		present := false
		for _, target := range targets {
			if target.TargetID == opened.TargetID {
				present = true
				break
			}
		}
		if !present {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("context A target %s remained present after close", opened.TargetID)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := second.ReconcileActionRecoveryRecordContext(ctx, recovery); err == nil {
		t.Fatal("recovery from another BrowserContext succeeded although its target is absent here")
	} else if operation, ok := err.(*agent.OperationError); !ok || operation.Cause != "browser_instance_mismatch" {
		t.Fatalf("cross-context recovery error = %#v, want browser_instance_mismatch", err)
	}
}

func delayedCloseResponseProxy(t *testing.T, upstreamURL string, delay time.Duration) (string, <-chan struct{}) {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	forwarded := make(chan struct{})
	var forwardedOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		client, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		upstream, _, err := websocket.DefaultDialer.Dial(upstreamURL, nil)
		if err != nil {
			_ = client.Close()
			return
		}
		var mu sync.Mutex
		closeCommandID := ""
		var relay sync.WaitGroup
		relay.Add(2)
		go func() {
			defer relay.Done()
			defer upstream.Close()
			for {
				messageType, data, readErr := client.ReadMessage()
				if readErr != nil {
					return
				}
				var message struct {
					ID     json.Number `json:"id"`
					Method string      `json:"method"`
				}
				decoder := json.NewDecoder(strings.NewReader(string(data)))
				decoder.UseNumber()
				if decoder.Decode(&message) == nil && message.Method == "Target.closeTarget" {
					mu.Lock()
					closeCommandID = message.ID.String()
					mu.Unlock()
				}
				if writeErr := upstream.WriteMessage(messageType, data); writeErr != nil {
					return
				}
			}
		}()
		go func() {
			defer relay.Done()
			defer client.Close()
			for {
				messageType, data, readErr := upstream.ReadMessage()
				if readErr != nil {
					return
				}
				var message struct {
					ID json.Number `json:"id"`
				}
				decoder := json.NewDecoder(strings.NewReader(string(data)))
				decoder.UseNumber()
				if decoder.Decode(&message) == nil && message.ID.String() != "" {
					mu.Lock()
					isCloseResponse := message.ID.String() == closeCommandID
					mu.Unlock()
					if isCloseResponse {
						time.Sleep(delay)
						forwardedOnce.Do(func() { close(forwarded) })
					}
				}
				if writeErr := client.WriteMessage(messageType, data); writeErr != nil {
					return
				}
			}
		}()
		relay.Wait()
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() {
		select {
		case <-forwarded:
		default:
		}
	})
	return "ws" + strings.TrimPrefix(server.URL, "http"), forwarded
}

func failingTargetActivationProxy(t *testing.T, upstreamURL string) (string, func(string), <-chan string) {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	var target atomic.Value
	failed := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		client, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		upstream, _, err := websocket.DefaultDialer.Dial(upstreamURL, nil)
		if err != nil {
			_ = client.Close()
			return
		}
		var clientWriteMu sync.Mutex
		var relay sync.WaitGroup
		relay.Add(2)
		go func() {
			defer relay.Done()
			defer upstream.Close()
			for {
				messageType, data, readErr := client.ReadMessage()
				if readErr != nil {
					return
				}
				var command struct {
					ID     json.RawMessage `json:"id"`
					Method string          `json:"method"`
					Params struct {
						TargetID string `json:"targetId"`
					} `json:"params"`
				}
				if json.Unmarshal(data, &command) == nil && command.Method == "Target.activateTarget" && command.Params.TargetID != "" {
					if expected, ok := target.Load().(string); ok && expected == command.Params.TargetID {
						response, _ := json.Marshal(map[string]any{
							"id":    command.ID,
							"error": map[string]any{"code": -32000, "message": "injected Target.activateTarget failure"},
						})
						clientWriteMu.Lock()
						writeErr := client.WriteMessage(websocket.TextMessage, response)
						clientWriteMu.Unlock()
						select {
						case failed <- command.Params.TargetID:
						default:
						}
						if writeErr != nil {
							return
						}
						continue
					}
				}
				if writeErr := upstream.WriteMessage(messageType, data); writeErr != nil {
					return
				}
			}
		}()
		go func() {
			defer relay.Done()
			defer client.Close()
			for {
				messageType, data, readErr := upstream.ReadMessage()
				if readErr != nil {
					return
				}
				clientWriteMu.Lock()
				writeErr := client.WriteMessage(messageType, data)
				clientWriteMu.Unlock()
				if writeErr != nil {
					return
				}
			}
		}()
		relay.Wait()
	}))
	t.Cleanup(server.Close)
	return "ws" + strings.TrimPrefix(server.URL, "http"), func(targetID string) {
		target.Store(targetID)
	}, failed
}
