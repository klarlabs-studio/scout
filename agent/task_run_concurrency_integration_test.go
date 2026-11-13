//go:build integration

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	cdpclient "go.klarlabs.de/scout/internal/cdp"
	"go.klarlabs.de/scout/internal/launcher"
)

type finalVerificationBarrierStore struct {
	*BoltTaskRunStore
	reached chan struct{}
	release chan struct{}
}

type taskHandoffBarrierStore struct {
	*BoltTaskRunStore
	reached chan struct{}
	release chan struct{}
}

type taskActionStartBarrierStore struct {
	*BoltTaskRunStore
	reached chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *taskHandoffBarrierStore) BindTaskRunHandoff(ctx context.Context, id, fingerprint, targetID, browserInstanceID string, plan TaskRunPlan, recovery TaskRunRecovery) error {
	select {
	case s.reached <- struct{}{}:
	default:
	}
	select {
	case <-s.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.BoltTaskRunStore.BindTaskRunHandoff(ctx, id, fingerprint, targetID, browserInstanceID, plan, recovery)
}

func (s *taskActionStartBarrierStore) UpdateTaskRunRecovery(ctx context.Context, id, fingerprint string, recovery TaskRunRecovery) error {
	blocked := false
	if recovery.Current != nil && recovery.Current.Status == "started" {
		s.once.Do(func() { blocked = true })
	}
	if blocked {
		select {
		case s.reached <- struct{}{}:
		default:
		}
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.BoltTaskRunStore.UpdateTaskRunRecovery(ctx, id, fingerprint, recovery)
}

func (s *finalVerificationBarrierStore) UpdateTaskRunRecovery(ctx context.Context, id, fingerprint string, recovery TaskRunRecovery) error {
	if recovery.Phase == "final_verification" {
		select {
		case s.reached <- struct{}{}:
		default:
		}
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.BoltTaskRunStore.UpdateTaskRunRecovery(ctx, id, fingerprint, recovery)
}

func TestRunTaskFinalVerificationIsSerializedWithOtherSessionCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/interference" {
			_, _ = fmt.Fprint(w, `<!doctype html><html><body>interference</body></html>`)
			return
		}
		_, _ = fmt.Fprint(w, `<!doctype html><html><body><button id="complete" onclick="document.body.innerText='complete'">Complete</button></body></html>`)
	}))
	defer server.Close()

	storeDB, err := OpenBoltTaskRunStore(t.TempDir() + "/tasks.db")
	if err != nil {
		t.Fatal(err)
	}
	store := &finalVerificationBarrierStore{BoltTaskRunStore: storeDB, reached: make(chan struct{}, 1), release: make(chan struct{})}
	session, err := NewSession(SessionConfig{Headless: true, AllowPrivateIPs: true, TaskRunStore: store})
	if err != nil {
		_ = storeDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = session.Close()
		_ = storeDB.Close()
	})
	if _, err := session.Navigate(server.URL); err != nil {
		t.Fatal(err)
	}

	taskDone := make(chan struct {
		result *TaskRunResult
		err    error
	}, 1)
	go func() {
		result, err := session.RunTaskContext(context.Background(), TaskRunSpec{
			ID:                 "atomic-final-verification",
			Actions:            []BatchAction{{Action: "click", Selector: "#complete", ExpectedText: "complete"}},
			FinalPostcondition: &Postcondition{Text: "complete"},
		})
		taskDone <- struct {
			result *TaskRunResult
			err    error
		}{result, err}
	}()

	select {
	case <-store.reached:
	case <-time.After(10 * time.Second):
		t.Fatal("task did not checkpoint final verification")
	}

	navigationStarted := make(chan struct{})
	navigationDone := make(chan error, 1)
	go func() {
		close(navigationStarted)
		_, err := session.Navigate(server.URL + "/interference")
		navigationDone <- err
	}()
	<-navigationStarted
	select {
	case err := <-navigationDone:
		t.Fatalf("concurrent navigation crossed the final verification boundary: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(store.release)
	select {
	case outcome := <-taskDone:
		if outcome.err != nil {
			t.Fatalf("RunTaskContext: %v", outcome.err)
		}
		if outcome.result == nil || outcome.result.Status != "completed" || outcome.result.FinalVerification == nil || !outcome.result.FinalVerification.Verified {
			t.Fatalf("task result = %+v, want completed with verified final postcondition", outcome.result)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("task did not finish after releasing final verification")
	}
	select {
	case err := <-navigationDone:
		if err != nil {
			t.Fatalf("queued navigation: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("queued navigation did not run after task verification")
	}
}

func TestSelectedOOPIFReattachesAfterOwnerReplacement(t *testing.T) {
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, "<!doctype html><html><head><title>%s</title></head><body><p id=state>%s</p></body></html>", r.URL.Path, r.URL.Path)
	}))
	defer child.Close()
	childURL := strings.Replace(child.URL, "127.0.0.1", "localhost", 1)
	parent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprintf(w, `<!doctype html><html><body><iframe id="isolated" src=%q></iframe></body></html>`, childURL+"/before")
	}))
	defer parent.Close()

	session, err := NewSession(SessionConfig{Headless: true, AllowPrivateIPs: true, AllowedOrigins: []string{parent.URL, childURL}})
	if err != nil {
		t.Skipf("local Chrome unavailable: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if _, err := session.Navigate(parent.URL); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	selected, err := session.SwitchToFrame("#isolated")
	if err != nil {
		t.Fatalf("select OOPIF: %v", err)
	}
	originalFrameID := selected.FrameID
	if value, err := session.Eval(`document.querySelector('#state').textContent`); err != nil || value != "/before" {
		t.Fatalf("initial OOPIF read = (%v, %v)", value, err)
	}
	replacementURL, _ := json.Marshal(childURL + "/after")
	if _, err := session.mainPage.EvaluateContext(context.Background(), fmt.Sprintf(`(function(){const old=document.querySelector('#isolated');old.remove();const frame=document.createElement('iframe');frame.id='isolated';frame.src=%s;document.body.appendChild(frame)})()`, replacementURL)); err != nil {
		t.Fatalf("replace OOPIF owner element: %v", err)
	}
	detachDeadline := time.Now().Add(2 * time.Second)
	for (session.frameDetached == nil || !session.frameDetached.Load()) && time.Now().Before(detachDeadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if session.frameDetached == nil || !session.frameDetached.Load() {
		t.Fatal("browser-level Target.detachedFromTarget event did not invalidate the selected OOPIF route")
	}
	time.Sleep(500 * time.Millisecond)
	value, err := session.Eval(`document.querySelector('#state').textContent`)
	if err != nil || value != "/after" {
		t.Fatalf("rebound OOPIF read = (%v, %v), want /after", value, err)
	}
	if session.frameRoute == nil || session.frameID == "" || session.frameID == originalFrameID || session.frameRoute.TargetID() == "" {
		t.Fatalf("OOPIF route was not rebound: route=%v frame=%q", session.frameRoute, session.frameID)
	}
}

func TestNestedSiteIsolatedFrameRoutesAndUnwinds(t *testing.T) {
	grandchild := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<!doctype html><title>Nested OOPIF</title><p id="deep">deep content</p>`)
	}))
	defer grandchild.Close()
	grandchildURL := grandchild.URL
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprintf(w, `<!doctype html><title>Middle OOPIF</title><iframe id="inner" src=%q></iframe>`, grandchildURL)
	}))
	defer child.Close()
	childURL := strings.Replace(child.URL, "127.0.0.1", "localhost", 1)
	parent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprintf(w, `<!doctype html><title>Top page</title><iframe id="outer" src=%q></iframe>`, childURL)
	}))
	defer parent.Close()

	session, err := NewSession(SessionConfig{Headless: true, AllowPrivateIPs: true, AllowedOrigins: []string{parent.URL, childURL, grandchildURL}})
	if err != nil {
		t.Skipf("local Chrome unavailable: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if _, err := session.Navigate(parent.URL); err != nil {
		t.Fatal(err)
	}
	time.Sleep(700 * time.Millisecond)
	if _, err := session.SwitchToFrame("#outer"); err != nil {
		t.Fatalf("select outer OOPIF: %v", err)
	}
	time.Sleep(400 * time.Millisecond)
	if _, err := session.SwitchToFrame("#inner"); err != nil {
		t.Fatalf("select nested OOPIF: %v", err)
	}
	if len(session.frameRouteStack) != 2 {
		t.Fatalf("route stack depth = %d, want 2", len(session.frameRouteStack))
	}
	if value, err := session.Eval(`document.querySelector('#deep').textContent`); err != nil || value != "deep content" {
		t.Fatalf("nested document read = (%v, %v)", value, err)
	}
	if _, err := session.SwitchToMainFrame(); err != nil {
		t.Fatalf("unwind nested routes: %v", err)
	}
	if len(session.frameRouteStack) != 0 || session.InFrame() {
		t.Fatalf("route stack remains after unwind: depth=%d InFrame=%v", len(session.frameRouteStack), session.InFrame())
	}
	if value, err := session.Eval(`document.title`); err != nil || value != "Top page" {
		t.Fatalf("top page after unwind = (%v, %v)", value, err)
	}
}

func TestNestedOOPIFReattachesAfterAncestorReplacement(t *testing.T) {
	grandchild := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprintf(w, `<!doctype html><title>Deep %s</title><p id="deep">%s</p>`, r.URL.Path, r.URL.Path)
	}))
	defer grandchild.Close()
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		deepPath := "/before"
		if strings.Contains(r.URL.Path, "after") || r.URL.Query().Get("after") == "1" {
			deepPath = "/after"
		}
		_, _ = fmt.Fprintf(w, `<!doctype html><title>Middle</title><iframe id="inner" src=%q></iframe>`, grandchild.URL+deepPath)
	}))
	defer child.Close()
	childURL := strings.Replace(child.URL, "127.0.0.1", "localhost", 1)
	parent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		path := "/before"
		if r.URL.Query().Get("after") == "1" {
			path = "/after"
		}
		_, _ = fmt.Fprintf(w, `<!doctype html><title>Top</title><iframe id="outer" src=%q></iframe>`, childURL+path)
	}))
	defer parent.Close()

	session, err := NewSession(SessionConfig{Headless: true, AllowPrivateIPs: true, AllowedOrigins: []string{parent.URL, childURL, grandchild.URL}})
	if err != nil {
		t.Skipf("local Chrome unavailable: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if _, err := session.Navigate(parent.URL); err != nil {
		t.Fatal(err)
	}
	time.Sleep(700 * time.Millisecond)
	if _, err := session.SwitchToFrame("#outer"); err != nil {
		t.Fatalf("select outer OOPIF: %v", err)
	}
	time.Sleep(350 * time.Millisecond)
	if _, err := session.SwitchToFrame("#inner"); err != nil {
		t.Fatalf("select nested OOPIF: %v", err)
	}
	oldIDs := []string{session.frameRouteStack[0].frameID, session.frameRouteStack[1].frameID}
	oldParent := session.mainPage
	replacementURL, _ := json.Marshal(childURL + "/before?after=1")
	if _, err := oldParent.EvaluateContext(context.Background(), fmt.Sprintf(`(function(){const old=document.querySelector('#outer');old.remove();const frame=document.createElement('iframe');frame.id='outer';frame.src=%s;document.body.appendChild(frame)})()`, replacementURL)); err != nil {
		t.Fatalf("replace outer OOPIF owner: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for (session.frameRouteStack[0].detached == nil || !session.frameRouteStack[0].detached.Load()) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if session.frameRouteStack[0].detached == nil || !session.frameRouteStack[0].detached.Load() {
		t.Fatal("outer OOPIF detach was not observed")
	}
	time.Sleep(500 * time.Millisecond)
	value, err := session.Eval(`document.querySelector('#deep').textContent`)
	if err != nil || value != "/after" {
		t.Fatalf("nested read after ancestor replacement = (%v, %v), want /after", value, err)
	}
	if session.frameRouteStack[0].frameID == oldIDs[0] || session.frameRouteStack[1].frameID == oldIDs[1] {
		t.Fatalf("nested routes were not both rebound: old=%v new=[%s %s]", oldIDs, session.frameRouteStack[0].frameID, session.frameRouteStack[1].frameID)
	}
}

func TestRunTaskRejectsFrameChangeBetweenBindingAndDispatch(t *testing.T) {
	clicked := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/task-click" {
			select {
			case clicked <- struct{}{}:
			default:
			}
			_, _ = fmt.Fprint(w, "ok")
			return
		}
		if r.URL.Path == "/frame" {
			_, _ = fmt.Fprint(w, `<!doctype html><html><body><button id="complete" onclick="fetch('/task-click');document.body.innerText='task complete'">Complete</button></body></html>`)
			return
		}
		_, _ = fmt.Fprintf(w, `<!doctype html><html><body><iframe id="task-frame" src=%q></iframe></body></html>`, serverURLForRequest(r, "/frame"))
	}))
	defer server.Close()

	storeDB, err := OpenBoltTaskRunStore(t.TempDir() + "/tasks.db")
	if err != nil {
		t.Fatal(err)
	}
	store := &taskHandoffBarrierStore{BoltTaskRunStore: storeDB, reached: make(chan struct{}, 1), release: make(chan struct{})}
	session, err := NewSession(SessionConfig{Headless: true, AllowPrivateIPs: true, TaskRunStore: store})
	if err != nil {
		_ = storeDB.Close()
		t.Skipf("local Chrome unavailable: %v", err)
	}
	t.Cleanup(func() { _ = session.Close(); _ = storeDB.Close() })
	if _, err := session.Navigate(server.URL); err != nil {
		t.Fatal(err)
	}
	if _, err := session.SwitchToFrame("#task-frame"); err != nil {
		t.Fatal(err)
	}

	taskDone := make(chan error, 1)
	go func() {
		_, taskErr := session.RunTaskContext(context.Background(), TaskRunSpec{ID: "frame-changed-before-dispatch", Timeout: 10 * time.Second, Actions: []BatchAction{{Action: "click", Selector: "#complete", ExpectedText: "task complete"}}})
		taskDone <- taskErr
	}()
	select {
	case <-store.reached:
	case <-time.After(10 * time.Second):
		t.Fatal("task did not reach durable handoff")
	}
	if _, err := session.SwitchToMainFrame(); err != nil {
		close(store.release)
		t.Fatalf("switch to main frame: %v", err)
	}
	close(store.release)
	select {
	case err := <-taskDone:
		var opErr *OperationError
		if !errors.As(err, &opErr) || opErr.Cause != "frame_changed" {
			t.Fatalf("task error = %v, want frame_changed", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("task did not finish after releasing handoff")
	}
	select {
	case <-clicked:
		t.Fatal("task dispatched into the wrong frame")
	case <-time.After(250 * time.Millisecond):
	}
}

func TestRunTaskTypeUsesSelectedFrame(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/frame" {
			_, _ = fmt.Fprint(w, `<!doctype html><html><body><input id="name"><label for="email">Email</label><input id="email"><x-field></x-field><script>customElements.define('x-field',class extends HTMLElement{connectedCallback(){const root=this.attachShadow({mode:'open'});root.innerHTML='<input aria-label="Workspace">'}})</script></body></html>`)
			return
		}
		_, _ = fmt.Fprintf(w, `<!doctype html><html><body><iframe id="task-frame" src=%q></iframe></body></html>`, "http://"+r.Host+"/frame")
	}))
	defer server.Close()
	session, err := NewSession(SessionConfig{Headless: true, AllowPrivateIPs: true})
	if err != nil {
		t.Skipf("local Chrome unavailable: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if _, err := session.Navigate(server.URL); err != nil {
		t.Fatal(err)
	}
	if _, err := session.SwitchToFrame("#task-frame"); err != nil {
		t.Fatal(err)
	}
	result, err := session.RunTaskContext(context.Background(), TaskRunSpec{
		ID: "type-in-selected-frame",
		Actions: []BatchAction{
			{Action: "type", Selector: "#name", Value: "Grace", ExpectedValueSelector: "#name", ExpectedValue: "Grace"},
			{Action: "fill_form_semantic", Fields: map[string]string{"Email": "ada@example.test", "Workspace": "scout"}},
		},
	})
	if err != nil {
		t.Fatalf("task run: %v", err)
	}
	if result == nil || result.Status != "completed" {
		t.Fatalf("task result = %+v, want completed", result)
	}
	value, err := session.evaluateInFrameContext(context.Background(), session.frameContextID, `document.querySelector('#name').value`)
	if err != nil {
		t.Fatalf("read selected frame input: %v", err)
	}
	if value != "Grace" {
		t.Fatalf("selected frame input = %v, want Grace", value)
	}
	value, err = session.evaluateInFrameContext(context.Background(), session.frameContextID, `document.querySelector('#email').value`)
	if err != nil {
		t.Fatalf("read semantic field in selected frame: %v", err)
	}
	if value != "ada@example.test" {
		t.Fatalf("selected frame semantic field = %v, want ada@example.test", value)
	}
	value, err = session.evaluateInFrameContext(context.Background(), session.frameContextID, `document.querySelector('x-field').shadowRoot.querySelector('input').value`)
	if err != nil {
		t.Fatalf("read open-shadow field in selected frame: %v", err)
	}
	if value != "scout" {
		t.Fatalf("selected frame shadow field = %v, want scout", value)
	}
}

func serverURLForRequest(r *http.Request, path string) string {
	return "http://" + r.Host + path
}

func TestRunTaskRejectsSameTargetNavigationBetweenBindingAndDispatch(t *testing.T) {
	wrongTargetClicked := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/task-click":
			_, _ = fmt.Fprint(w, "ok")
		case "/wrong-click":
			select {
			case wrongTargetClicked <- struct{}{}:
			default:
			}
			_, _ = fmt.Fprint(w, "ok")
		case "/interference":
			_, _ = fmt.Fprint(w, `<!doctype html><html><body><button id="complete" onclick="fetch('/wrong-click'); document.body.innerText='wrong complete'">Complete</button></body></html>`)
		default:
			_, _ = fmt.Fprint(w, `<!doctype html><html><body><button id="complete" onclick="fetch('/task-click'); document.body.innerText='task complete'">Complete</button></body></html>`)
		}
	}))
	defer server.Close()

	storeDB, err := OpenBoltTaskRunStore(t.TempDir() + "/tasks.db")
	if err != nil {
		t.Fatal(err)
	}
	store := &taskHandoffBarrierStore{BoltTaskRunStore: storeDB, reached: make(chan struct{}, 1), release: make(chan struct{})}
	session, err := NewSession(SessionConfig{Headless: true, AllowPrivateIPs: true, TaskRunStore: store})
	if err != nil {
		_ = storeDB.Close()
		t.Skipf("local Chrome unavailable: %v", err)
	}
	t.Cleanup(func() {
		_ = session.Close()
		_ = storeDB.Close()
	})
	if _, err := session.Navigate(server.URL + "/original"); err != nil {
		t.Fatal(err)
	}

	taskDone := make(chan struct {
		result *TaskRunResult
		err    error
	}, 1)
	go func() {
		result, err := session.RunTaskContext(context.Background(), TaskRunSpec{
			ID:      "page-changed-before-dispatch",
			Timeout: 10 * time.Second,
			Actions: []BatchAction{{Action: "click", Selector: "#complete", ExpectedText: "task complete"}},
		})
		taskDone <- struct {
			result *TaskRunResult
			err    error
		}{result, err}
	}()

	select {
	case <-store.reached:
	case <-time.After(10 * time.Second):
		t.Fatal("task did not reach the durable handoff checkpoint")
	}
	if _, err := session.Navigate(server.URL + "/interference"); err != nil {
		close(store.release)
		t.Fatalf("concurrent same-target navigation: %v", err)
	}
	close(store.release)

	select {
	case outcome := <-taskDone:
		if outcome.err == nil {
			t.Fatalf("RunTaskContext succeeded after the page changed: %+v", outcome.result)
		}
		if outcome.result == nil || outcome.result.Batch != nil {
			t.Fatalf("task result = %+v, want a pre-dispatch failure without a batch", outcome.result)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("task did not finish after releasing the durable handoff")
	}
	select {
	case <-wrongTargetClicked:
		t.Fatal("task dispatched a click to the replacement page on the same target")
	case <-time.After(250 * time.Millisecond):
	}
}

func TestRunTaskRejectsClosedTargetBetweenBindingAndDispatch(t *testing.T) {
	clicked := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/task-click" {
			select {
			case clicked <- struct{}{}:
			default:
			}
			_, _ = fmt.Fprint(w, "ok")
			return
		}
		_, _ = fmt.Fprint(w, `<!doctype html><html><body><button id="complete" onclick="fetch('/task-click'); document.body.innerText='task complete'">Complete</button></body></html>`)
	}))
	defer server.Close()

	storeDB, err := OpenBoltTaskRunStore(t.TempDir() + "/tasks.db")
	if err != nil {
		t.Fatal(err)
	}
	store := &taskHandoffBarrierStore{BoltTaskRunStore: storeDB, reached: make(chan struct{}, 1), release: make(chan struct{})}
	session, err := NewSession(SessionConfig{Headless: true, AllowPrivateIPs: true, TaskRunStore: store})
	if err != nil {
		_ = storeDB.Close()
		t.Skipf("local Chrome unavailable: %v", err)
	}
	t.Cleanup(func() {
		_ = session.Close()
		_ = storeDB.Close()
	})
	if _, err := session.Navigate(server.URL + "/original"); err != nil {
		t.Fatal(err)
	}

	taskDone := make(chan struct {
		result *TaskRunResult
		err    error
	}, 1)
	go func() {
		result, err := session.RunTaskContext(context.Background(), TaskRunSpec{
			ID:      "target-closed-before-dispatch",
			Timeout: 10 * time.Second,
			Actions: []BatchAction{{Action: "click", Selector: "#complete", ExpectedText: "task complete"}},
		})
		taskDone <- struct {
			result *TaskRunResult
			err    error
		}{result, err}
	}()

	select {
	case <-store.reached:
	case <-time.After(10 * time.Second):
		t.Fatal("task did not reach the durable handoff checkpoint")
	}
	if err := session.page.CloseContext(context.Background()); err != nil {
		close(store.release)
		t.Fatalf("close captured browser target: %v", err)
	}
	close(store.release)

	select {
	case outcome := <-taskDone:
		if outcome.err == nil {
			t.Fatalf("RunTaskContext succeeded after its captured target was closed: %+v", outcome.result)
		}
		if outcome.result == nil || outcome.result.Batch != nil {
			t.Fatalf("task result = %+v, want a pre-dispatch failure without a batch", outcome.result)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("task did not finish after closing the captured target")
	}
	select {
	case <-clicked:
		t.Fatal("task dispatched a click after its captured target was closed")
	case <-time.After(250 * time.Millisecond):
	}
}

func TestRunTaskRejectsTargetSwitchBetweenBindingAndDispatch(t *testing.T) {
	wrongTargetClicked := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/wrong-click":
			select {
			case wrongTargetClicked <- struct{}{}:
			default:
			}
			_, _ = fmt.Fprint(w, "ok")
		case "/other":
			_, _ = fmt.Fprint(w, `<!doctype html><html><body><button id="complete" onclick="fetch('/wrong-click'); document.body.innerText='wrong complete'">Complete</button></body></html>`)
		default:
			_, _ = fmt.Fprint(w, `<!doctype html><html><body><button id="complete" onclick="document.body.innerText='task complete'">Complete</button></body></html>`)
		}
	}))
	defer server.Close()

	storeDB, err := OpenBoltTaskRunStore(t.TempDir() + "/tasks.db")
	if err != nil {
		t.Fatal(err)
	}
	store := &taskHandoffBarrierStore{BoltTaskRunStore: storeDB, reached: make(chan struct{}, 1), release: make(chan struct{})}
	session, err := NewSession(SessionConfig{Headless: true, AllowPrivateIPs: true, TaskRunStore: store})
	if err != nil {
		_ = storeDB.Close()
		t.Skipf("local Chrome unavailable: %v", err)
	}
	t.Cleanup(func() {
		_ = session.Close()
		_ = storeDB.Close()
	})
	if _, err := session.Navigate(server.URL + "/original"); err != nil {
		t.Fatal(err)
	}

	taskDone := make(chan struct {
		result *TaskRunResult
		err    error
	}, 1)
	go func() {
		result, err := session.RunTaskContext(context.Background(), TaskRunSpec{
			ID:      "target-changed-before-dispatch",
			Timeout: 10 * time.Second,
			Actions: []BatchAction{{Action: "click", Selector: "#complete", ExpectedText: "task complete"}},
		})
		taskDone <- struct {
			result *TaskRunResult
			err    error
		}{result, err}
	}()

	select {
	case <-store.reached:
	case <-time.After(10 * time.Second):
		t.Fatal("task did not reach the durable handoff checkpoint")
	}
	if _, err := session.OpenTab("replacement", server.URL+"/other"); err != nil {
		close(store.release)
		t.Fatalf("open and select replacement tab: %v", err)
	}
	close(store.release)

	select {
	case outcome := <-taskDone:
		if outcome.err == nil {
			t.Fatalf("RunTaskContext succeeded after the active target changed: %+v", outcome.result)
		}
		if outcome.result == nil || outcome.result.Batch != nil {
			t.Fatalf("task result = %+v, want a pre-dispatch failure without a batch", outcome.result)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("task did not finish after releasing the durable handoff")
	}
	select {
	case <-wrongTargetClicked:
		t.Fatal("task dispatched a click to the replacement target")
	case <-time.After(250 * time.Millisecond):
	}
}

func TestRunTaskSerializesBrowserResetBetweenBindingAndDispatch(t *testing.T) {
	clicked := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/task-click" {
			select {
			case clicked <- struct{}{}:
			default:
			}
		}
		_, _ = fmt.Fprint(w, `<!doctype html><html><body><button id="complete" onclick="fetch('/task-click'); document.body.innerText='task complete'">Complete</button></body></html>`)
	}))
	defer server.Close()

	storeDB, err := OpenBoltTaskRunStore(t.TempDir() + "/tasks.db")
	if err != nil {
		t.Fatal(err)
	}
	store := &taskHandoffBarrierStore{BoltTaskRunStore: storeDB, reached: make(chan struct{}, 1), release: make(chan struct{})}
	session, err := NewSession(SessionConfig{Headless: true, AllowPrivateIPs: true, TaskRunStore: store})
	if err != nil {
		_ = storeDB.Close()
		t.Skipf("local Chrome unavailable: %v", err)
	}
	t.Cleanup(func() {
		_ = session.Close()
		_ = storeDB.Close()
	})
	if _, err := session.Navigate(server.URL); err != nil {
		t.Fatal(err)
	}

	taskDone := make(chan error, 1)
	go func() {
		_, err := session.RunTaskContext(context.Background(), TaskRunSpec{
			ID: "browser-reset-before-dispatch", Timeout: 20 * time.Second,
			Actions: []BatchAction{{Action: "click", Selector: "#complete", ExpectedText: "task complete"}},
		})
		taskDone <- err
	}()
	select {
	case <-store.reached:
	case <-time.After(10 * time.Second):
		t.Fatal("task did not reach the durable handoff checkpoint")
	}

	resetDone := make(chan error, 1)
	go func() { resetDone <- session.ResetContext(context.Background()) }()
	select {
	case err := <-resetDone:
		t.Fatalf("browser reset crossed the handoff boundary before task dispatch: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(store.release)

	select {
	case err := <-taskDone:
		if err == nil {
			t.Fatal("task unexpectedly succeeded after browser reset replaced its target")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("task did not finish after releasing the durable handoff")
	}
	select {
	case err := <-resetDone:
		if err != nil {
			t.Fatalf("queued browser reset: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("browser reset did not run after task completion")
	}
	select {
	case <-clicked:
		t.Fatal("task dispatched a click after the browser reset replaced its target")
	case <-time.After(250 * time.Millisecond):
	}
}

func TestRunTaskRejectsExternalCDPNavigationBetweenBindingAndDispatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	browser, err := launcher.LaunchContext(ctx, launcher.Options{Headless: true})
	if err != nil {
		t.Skipf("local Chrome unavailable: %v", err)
	}
	defer browser.Close()
	wrongTargetClicked := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/wrong-click":
			select {
			case wrongTargetClicked <- struct{}{}:
			default:
			}
			_, _ = fmt.Fprint(w, "ok")
		case "/other":
			_, _ = fmt.Fprint(w, `<!doctype html><html><body><button id="complete" onclick="fetch('/wrong-click'); document.body.innerText='wrong complete'">Complete</button></body></html>`)
		default:
			_, _ = fmt.Fprint(w, `<!doctype html><html><body><button id="complete" onclick="document.body.innerText='task complete'">Complete</button></body></html>`)
		}
	}))
	defer server.Close()

	storeDB, err := OpenBoltTaskRunStore(t.TempDir() + "/tasks.db")
	if err != nil {
		t.Fatal(err)
	}
	store := &taskActionStartBarrierStore{BoltTaskRunStore: storeDB, reached: make(chan struct{}, 1), release: make(chan struct{})}
	session, err := NewSessionContext(ctx, SessionConfig{RemoteCDP: browser.WSEndpoint(), AllowPrivateIPs: true, TaskRunStore: store})
	if err != nil {
		_ = storeDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = session.Close()
		_ = storeDB.Close()
	})
	if _, err := session.Navigate(server.URL + "/original"); err != nil {
		t.Fatal(err)
	}
	connection, err := cdpclient.DialContext(ctx, browser.WSEndpoint())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	targetSession, err := connection.AttachToTargetCtx(ctx, session.page.TargetID())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = connection.DetachFromTargetCtx(context.Background(), targetSession) }()

	taskDone := make(chan struct {
		result *TaskRunResult
		err    error
	}, 1)
	go func() {
		result, err := session.RunTaskContext(ctx, TaskRunSpec{
			ID: "external-cdp-navigation-before-dispatch", Timeout: 30 * time.Second,
			Actions: []BatchAction{{Action: "click", Selector: "#complete", ExpectedText: "task complete"}},
		})
		taskDone <- struct {
			result *TaskRunResult
			err    error
		}{result, err}
	}()
	select {
	case <-store.reached:
	case <-ctx.Done():
		t.Fatal("task did not reach the durable action-start checkpoint")
	}
	if _, err := connection.CallSessionCtx(ctx, targetSession, "Page.navigate", map[string]string{"url": server.URL + "/other"}); err != nil {
		close(store.release)
		t.Fatalf("external CDP navigation: %v", err)
	}
	close(store.release)
	select {
	case outcome := <-taskDone:
		if outcome.err == nil {
			t.Fatalf("RunTaskContext succeeded after an external client changed the page: %+v", outcome.result)
		}
		if outcome.result == nil || outcome.result.Batch == nil || outcome.result.Batch.NextActionIndex != 0 {
			t.Fatalf("task result = %+v, want a pre-dispatch failure before action 0", outcome.result)
		}
	case <-ctx.Done():
		t.Fatal("task did not finish after releasing the durable handoff")
	}
	select {
	case <-wrongTargetClicked:
		t.Fatal("task dispatched a click after an external CDP client changed the page")
	case <-time.After(250 * time.Millisecond):
	}
}

func TestRunTaskRejectsSameURLExternalReloadBetweenBindingAndDispatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	browser, err := launcher.LaunchContext(ctx, launcher.Options{Headless: true})
	if err != nil {
		t.Skipf("local Chrome unavailable: %v", err)
	}
	defer browser.Close()
	clicked := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/task-click" {
			select {
			case clicked <- struct{}{}:
			default:
			}
			_, _ = fmt.Fprint(w, "ok")
			return
		}
		_, _ = fmt.Fprint(w, `<!doctype html><html><body><button id="complete" onclick="fetch('/task-click'); document.body.innerText='task complete'">Complete</button></body></html>`)
	}))
	defer server.Close()

	storeDB, err := OpenBoltTaskRunStore(t.TempDir() + "/tasks.db")
	if err != nil {
		t.Fatal(err)
	}
	store := &taskActionStartBarrierStore{BoltTaskRunStore: storeDB, reached: make(chan struct{}, 1), release: make(chan struct{})}
	session, err := NewSessionContext(ctx, SessionConfig{RemoteCDP: browser.WSEndpoint(), AllowPrivateIPs: true, TaskRunStore: store})
	if err != nil {
		_ = storeDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close(); _ = storeDB.Close() })
	if _, err := session.Navigate(server.URL + "/same"); err != nil {
		t.Fatal(err)
	}
	connection, err := cdpclient.DialContext(ctx, browser.WSEndpoint())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	targetSession, err := connection.AttachToTargetCtx(ctx, session.page.TargetID())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = connection.DetachFromTargetCtx(context.Background(), targetSession) }()
	readLoaderID := func() string {
		raw, callErr := connection.CallSessionCtx(ctx, targetSession, "Page.getFrameTree", nil)
		if callErr != nil {
			return ""
		}
		var tree struct {
			FrameTree struct {
				Frame struct {
					LoaderID string `json:"loaderId"`
				} `json:"frame"`
			} `json:"frameTree"`
		}
		if json.Unmarshal(raw, &tree) != nil {
			return ""
		}
		return tree.FrameTree.Frame.LoaderID
	}
	initialLoader := readLoaderID()
	if initialLoader == "" {
		t.Fatal("initial document loader identity unavailable")
	}
	taskDone := make(chan struct {
		result *TaskRunResult
		err    error
	}, 1)
	go func() {
		result, err := session.RunTaskContext(ctx, TaskRunSpec{ID: "external-same-url-reload", Timeout: 30 * time.Second, Actions: []BatchAction{{Action: "click", Selector: "#complete", ExpectedText: "task complete"}}})
		taskDone <- struct {
			result *TaskRunResult
			err    error
		}{result, err}
	}()
	select {
	case <-store.reached:
	case <-ctx.Done():
		t.Fatal("task did not reach durable action-start checkpoint")
	}
	if _, err := connection.CallSessionCtx(ctx, targetSession, "Page.reload", map[string]any{"ignoreCache": true}); err != nil {
		close(store.release)
		t.Fatalf("external same-URL reload: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && readLoaderID() == initialLoader {
		time.Sleep(10 * time.Millisecond)
	}
	if current := readLoaderID(); current == "" || current == initialLoader {
		close(store.release)
		t.Fatal("same-URL reload did not produce a new document loader identity")
	}
	close(store.release)
	select {
	case outcome := <-taskDone:
		var opErr *OperationError
		if outcome.err == nil || !errors.As(outcome.err, &opErr) || opErr.Cause != "document_changed" {
			t.Fatalf("RunTaskContext error = %v, want document_changed", outcome.err)
		}
		if outcome.result == nil || outcome.result.Batch == nil || outcome.result.Batch.NextActionIndex != 0 {
			t.Fatalf("task result = %+v, want pre-dispatch failure before action 0", outcome.result)
		}
	case <-ctx.Done():
		t.Fatal("task did not finish after releasing durable action checkpoint")
	}
	select {
	case <-clicked:
		t.Fatal("task dispatched a click after same-URL document replacement")
	case <-time.After(250 * time.Millisecond):
	}
}
