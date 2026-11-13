// Package fixture serves deterministic local pages used by Scout's browser
// reliability benchmark and its external comparison adapters.
package fixture

import (
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Handler returns the benchmark routes. It is safe to mount on a local
// httptest.Server or expose through the standalone fixtureserver command.
func Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/delayed-element", func(w http.ResponseWriter, r *http.Request) {
		delay := 1800 * time.Millisecond
		if raw := r.URL.Query().Get("delay_ms"); raw != "" {
			if value, err := strconv.Atoi(raw); err == nil && value > 0 && value <= 30000 {
				delay = time.Duration(value) * time.Millisecond
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprintf(w, `<!doctype html><title>Delayed element</title><main><p id="wait-status">Waiting</p></main><script>setTimeout(()=>{const p=document.querySelector('#wait-status');p.id='delayed-ready';p.textContent='Ready'},%d)</script>`, delay.Milliseconds())
	})
	mux.HandleFunc("/delayed-state", func(w http.ResponseWriter, r *http.Request) {
		delay := 1200 * time.Millisecond
		if raw := r.URL.Query().Get("delay_ms"); raw != "" {
			if value, err := strconv.Atoi(raw); err == nil && value > 0 && value <= 30000 {
				delay = time.Duration(value) * time.Millisecond
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprintf(w, `<!doctype html><title>Delayed task state</title><main><p id="task-waiting">Waiting</p></main><script>setTimeout(()=>{const p=document.querySelector('#task-waiting');p.id='task-ready';p.textContent='Ready'},%d)</script>`, delay.Milliseconds())
	})
	mux.HandleFunc("/concurrency-source", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<!doctype html><title>Concurrency source</title><button id="race-click" onclick="document.querySelector('#source-state').textContent='Source clicked'">Source action</button><p id="source-state">Source untouched</p><script>setTimeout(()=>document.body.insertAdjacentHTML('beforeend','<p id="race-ready">Ready</p>'),1800)</script>`)
	})
	mux.HandleFunc("/concurrency-guard", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<!doctype html><title>Concurrency guard</title><button id="race-click" onclick="document.querySelector('#guard-state').textContent='Guard clicked'">Guard action</button><p id="guard-state">Guard untouched</p>`)
	})
	mux.HandleFunc("/late-action", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<!doctype html><title>Late action fixture</title><button id="late-action" data-privacy-sentinel="SCOUT_PRIVATE_SENTINEL_6b29f0a1" onclick="fetch('/late-action-started',{method:'POST'}).catch(()=>{});const until=Date.now()+1200;while(Date.now()<until){};document.querySelector('#late-status').textContent='Late completion';fetch('/late-action-completed',{method:'POST'}).catch(()=>{})">Start bounded task</button><p id="late-status">Waiting</p>`)
	})
	mux.HandleFunc("/late-type", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<!doctype html><title>Late type fixture</title><label for="late-input">Task</label><input id="late-input" oninput="fetch('/late-type-started',{method:'POST'}).catch(()=>{});const until=Date.now()+1200;while(Date.now()<until){};this.dataset.late='complete';fetch('/late-type-completed',{method:'POST'}).catch(()=>{})">`)
	})
	mux.HandleFunc("/late-submit", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<!doctype html><title>Late submit fixture</title><form id="late-form" onsubmit="event.preventDefault();fetch('/late-submit-started',{method:'POST'}).catch(()=>{});const until=Date.now()+1200;while(Date.now()<until){};document.querySelector('#late-status').textContent='Submission complete';fetch('/late-submit-completed',{method:'POST'}).catch(()=>{})"><button type="submit">Submit task</button></form><p id="late-status">Waiting</p>`)
	})
	mux.HandleFunc("/late-upload", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<!doctype html><title>Late upload fixture</title><label for="late-file">Attachment</label><input id="late-file" type="file" onchange="const until=Date.now()+1200;while(Date.now()<until){};document.querySelector('#late-upload-status').textContent='File selection handled'"><p id="late-upload-status">Waiting</p>`)
	})
	mux.HandleFunc("/late-batch", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<!doctype html><title>Late batch fixture</title><button id="batch-first" onclick="document.querySelector('#batch-first-status').textContent='First completed'">First action</button><p id="batch-first-status">Waiting</p><button id="batch-late" onclick="const until=Date.now()+1200;while(Date.now()<until){};document.querySelector('#batch-late-status').textContent='Batch late completion'">Late action</button><p id="batch-late-status">Waiting</p>`)
	})
	mux.HandleFunc("/large-batch-form", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		var b strings.Builder
		b.WriteString(`<!doctype html><title>Large batch recovery fixture</title><form id="large-batch-form"><div id="large-batch-fields">`)
		for i := 0; i < 23; i++ {
			fmt.Fprintf(&b, `<label for="bulk-%02d">Bulk field %02d</label><input id="bulk-%02d" name="bulk-%02d">`, i, i, i, i)
		}
		b.WriteString(`</div></form><button id="batch-timeout" onclick="const until=Date.now()+5000;while(Date.now()<until){};document.querySelector('#batch-status').textContent='Late batch completion'">Start delayed action</button><p id="batch-status">Waiting</p>`)
		_, _ = w.Write([]byte(b.String()))
	})
	lateClick := `<!doctype html><title>Late click fixture</title><button id="late-action" onclick="const until=Date.now()+1200;while(Date.now()<until){};document.querySelector('#late-status').textContent='Late completion'">Start task</button><p id="late-status">Waiting</p>`
	for _, path := range []string{"/late-label", "/late-handle", "/late-text"} {
		mux.HandleFunc(path, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = fmt.Fprint(w, lateClick)
		})
	}
	mux.HandleFunc("/late-event", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<!doctype html><title>Late event fixture</title><button id="late-event">Dispatch task</button><p id="late-status">Waiting</p><script>document.querySelector('#late-event').addEventListener('late-task',()=>{const until=Date.now()+1200;while(Date.now()<until){};document.querySelector('#late-status').textContent='Late event completion'})</script>`)
	})
	mux.HandleFunc("/late-drag", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<!doctype html><title>Late drag fixture</title><div id="late-source" draggable="true">Drag task</div><div id="late-target">Drop here</div><p id="late-status">Waiting</p><script>document.querySelector('#late-target').addEventListener('drop',event=>{event.preventDefault();const until=Date.now()+1200;while(Date.now()<until){};document.querySelector('#late-status').textContent='Late drop completion'})</script>`)
	})
	mux.HandleFunc("/late-select", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<!doctype html><title>Late select fixture</title><label for="late-select">Status</label><select id="late-select" onchange="const until=Date.now()+1200;while(Date.now()<until){};document.querySelector('#late-select-status').textContent='Selection handled'"><option value="waiting">Waiting</option><option value="ready">Ready</option></select><p id="late-select-status">Waiting</p>`)
	})
	mux.HandleFunc("/late-hover", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<!doctype html><title>Late hover fixture</title><button id="late-hover" onmouseover="const until=Date.now()+1200;while(Date.now()<until){};document.querySelector('#late-hover-status').textContent='Hover menu opened'">Open menu</button><p id="late-hover-status">Waiting</p>`)
	})
	mux.HandleFunc("/disconnect-action", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<!doctype html><title>Disconnect recovery task</title><button id="dispatch" onclick="document.querySelector('#status').textContent='Action dispatched';fetch('/disconnect-dispatched').catch(()=>{})">Dispatch and disconnect</button><p id="status">Waiting</p>`)
	})
	mux.HandleFunc("/large", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		var b strings.Builder
		b.WriteString(`<!doctype html><title>Benchmark large page</title><header><p>Noise header</p></header><aside>`)
		for i := 0; i < 120; i++ {
			fmt.Fprintf(&b, `<p>Decoy record %03d with irrelevant catalog text.</p>`, i)
		}
		b.WriteString(`</aside><main><h1>Target records</h1>`)
		for i := 0; i < 60; i++ {
			fmt.Fprintf(&b, `<p>Target record %03d with scoped detail.</p>`, i)
		}
		b.WriteString(`</main><footer><p>Decoy-only target</p>Noise footer</footer>`)
		_, _ = w.Write([]byte(b.String()))
	})
	mux.HandleFunc("/scope-root-control", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<!doctype html><title>Scope root control</title><label for="task-input">Outside scope label</label><input id="task-input" value="visible value"><button id="outside-scope-decoy">Outside scope decoy</button>`)
	})
	mux.HandleFunc("/action", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<!doctype html><button id="complete" onclick="document.querySelector('#status').textContent='Task complete'">Complete</button><p id="status">Waiting</p>`)
	})
	mux.HandleFunc("/session-owner/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch strings.TrimPrefix(r.URL.Path, "/session-owner/") {
		case "seed":
			_, _ = fmt.Fprint(w, `<!doctype html><title>Session owner seed</title><pre id="state">pending</pre><script>document.cookie='scout_owner=primary; Path=/; SameSite=Lax';localStorage.setItem('scout_owner','primary');document.querySelector('#state').textContent=JSON.stringify([document.cookie,localStorage.getItem('scout_owner')]);</script>`)
		case "probe":
			_, _ = fmt.Fprint(w, `<!doctype html><title>Session owner probe</title><pre id="state">pending</pre><script>document.querySelector('#state').textContent=JSON.stringify([document.cookie,localStorage.getItem('scout_owner')]);document.cookie='scout_owner=secondary; Path=/; SameSite=Lax';localStorage.setItem('scout_owner','secondary');</script>`)
		case "check":
			_, _ = fmt.Fprint(w, `<!doctype html><title>Session owner check</title><pre id="state">pending</pre><script>document.querySelector('#state').textContent=JSON.stringify([document.cookie,localStorage.getItem('scout_owner')]);</script>`)
		default:
			http.NotFound(w, r)
		}
	})
	mux.HandleFunc("/session-owner-delayed/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprintf(w, `<!doctype html><title>Delayed session owner source</title><pre id="state">pending</pre><script>document.cookie='scout_owner=%s; Path=/; SameSite=Lax';localStorage.setItem('scout_owner','%s');setTimeout(()=>{document.querySelector('#state').textContent=JSON.stringify([document.cookie,localStorage.getItem('scout_owner')]);document.body.insertAdjacentHTML('beforeend','<span id="ready">ready</span>')},1400)</script>`, strings.TrimPrefix(r.URL.Path, "/session-owner-delayed/"), strings.TrimPrefix(r.URL.Path, "/session-owner-delayed/"))
	})
	mux.HandleFunc("/type", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<!doctype html><title>Type fixture</title><label for="task">Task</label><input id="task" value="" autocomplete="off">`)
	})
	mux.HandleFunc("/background-tab", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<!doctype html><title>Background tab task</title><script>window.__visibilityTransitions=[document.visibilityState];document.addEventListener('visibilitychange',()=>window.__visibilityTransitions.push(document.visibilityState));</script><button id="background-action" onclick="if(document.visibilityState==='visible'){document.querySelector('#status').textContent='Background task complete'}">Complete only when visible</button><p id="status">Waiting</p>`)
	})
	mux.HandleFunc("/foreground-tab", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<!doctype html><title>Foreground tab guard</title><p id="status">Foreground untouched</p>`)
	})
	mux.HandleFunc("/stale-handle", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<!doctype html><title>Stable handle task</title><button id="target" onclick="document.querySelector('#status').textContent='Target clicked'">Target</button><button id="mutate" onclick="document.body.insertBefore(Object.assign(document.createElement('button'),{textContent:'Inserted'}),document.querySelector('#target'))">Insert before target</button><button id="remove" onclick="document.querySelector('#target').remove()">Remove target</button><p id="status">Waiting</p>`)
	})
	mux.HandleFunc("/shadow", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<!doctype html><title>Shadow fixture</title><main><button id="outside-decoy">Outside decoy</button><p id="shadow-result">Waiting</p><div id="shadow-host"></div></main><script>const root=document.querySelector('#shadow-host').attachShadow({mode:'open'});root.innerHTML='<button id="shadow-action">Complete shadow task</button>';root.querySelector('button').addEventListener('click',()=>document.querySelector('#shadow-result').textContent='Shadow task complete');</script>`)
	})
	mux.HandleFunc("/iframe", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<!doctype html><title>Frame fixture</title><main><iframe id="task-frame" name="task-frame" src="/frame-content"></iframe></main><aside><button id="frame-decoy">Frame decoy</button></aside>`)
	})
	mux.HandleFunc("/frame-content", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<!doctype html><title>Frame task</title><button id="frame-action" data-scout-handle="frame-save" onclick="document.querySelector('#frame-result').textContent='Frame task complete'">Complete frame task</button><p id="frame-result">Waiting</p>`)
	})
	mux.HandleFunc("/network-stress", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<!doctype html><title>Network output stress</title><button id="load">Load response bodies</button><script>document.querySelector('#load').addEventListener('click',async()=>{await Promise.all(Array.from({length:12},(_,i)=>fetch('/network-body?id='+i)))})</script>`)
	})
	mux.HandleFunc("/text-output-stress", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		var b strings.Builder
		b.WriteString(`<!doctype html><title>Text output stress</title><main>`)
		for i := 0; i < 12; i++ {
			fmt.Fprintf(&b, `<div class="large-output">item-%02d-%s</div>`, i, strings.Repeat("x", 30_000))
		}
		b.WriteString(`</main>`)
		_, _ = w.Write([]byte(b.String()))
	})
	mux.HandleFunc("/output-evidence-stress", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		var b strings.Builder
		b.WriteString(`<!doctype html><title>Output evidence stress</title><main>`)
		for i := 0; i < 64; i++ {
			if i == 0 {
				fmt.Fprintf(&b, `<article data-private-note="SCOUT_PRIVATE_SENTINEL_91c4a6e2"><h2>Record %03d</h2><p>%s</p></article>`, i, strings.Repeat(fmt.Sprintf("evidence-%03d-", i), 250))
				continue
			}
			fmt.Fprintf(&b, `<article><h2>Record %03d</h2><p>%s</p></article>`, i, strings.Repeat(fmt.Sprintf("evidence-%03d-", i), 250))
		}
		b.WriteString(`<article><h2>Final record</h2><p id="tail-evidence">SCOUT_TAIL_EVIDENCE_7f3b9d21</p></article></main>`)
		_, _ = w.Write([]byte(b.String()))
	})
	mux.HandleFunc("/mcp-output-limit", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		var b strings.Builder
		b.WriteString(`<!doctype html><title>MCP output limit</title><main>`)
		for i := 0; i < 100; i++ {
			fmt.Fprintf(&b, `<article><h2>Record %03d</h2><p>%s</p></article>`, i, strings.Repeat("x", 8000))
		}
		b.WriteString(`</main>`)
		_, _ = w.Write([]byte(b.String()))
	})
	mux.HandleFunc("/network-body", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		body := strings.Repeat("network-body-0123456789", 1200)
		_, _ = w.Write([]byte(body))
	})
	mux.HandleFunc("/replay", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<!doctype html><button id="advance" onclick="document.querySelector('#status').textContent='Step complete'">Advance</button><button id="repair" onclick="document.body.insertAdjacentHTML('beforeend','<span id=late>Ready for retry</span>')">Repair</button><p id="status">Ready</p>`)
	})
	mux.HandleFunc("/form", formHandler("/submit"))
	mux.HandleFunc("/reject-form", formHandler("/reject"))
	mux.HandleFunc("/submit", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprint(w, "accepted")
	})
	mux.HandleFunc("/reject", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = fmt.Fprint(w, "validation failed")
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprintln(w, "ok")
	})
	return mux
}

// HandlerWithLateActionSignals exposes when a real browser handler begins its
// bounded main-thread task and when its post-timeout mutation completes.
func HandlerWithLateActionSignals() (http.Handler, <-chan struct{}, <-chan struct{}) {
	started, completed := make(chan struct{}, 1), make(chan struct{}, 1)
	var startOnce, completeOnce sync.Once
	var startedCount, completedCount atomic.Uint64
	var typeStartedCount, typeCompletedCount atomic.Uint64
	var submitStartedCount, submitCompletedCount atomic.Uint64
	mux := http.NewServeMux()
	mux.Handle("/", Handler())
	mux.HandleFunc("/late-action-started", func(w http.ResponseWriter, _ *http.Request) {
		startedCount.Add(1)
		startOnce.Do(func() { started <- struct{}{} })
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/late-action-completed", func(w http.ResponseWriter, _ *http.Request) {
		completedCount.Add(1)
		completeOnce.Do(func() { completed <- struct{}{} })
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/late-action-state", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"started_count":%d,"completed_count":%d}`, startedCount.Load(), completedCount.Load())
	})
	for _, signal := range []struct {
		path  string
		count *atomic.Uint64
	}{
		{path: "/late-type-started", count: &typeStartedCount},
		{path: "/late-type-completed", count: &typeCompletedCount},
		{path: "/late-submit-started", count: &submitStartedCount},
		{path: "/late-submit-completed", count: &submitCompletedCount},
	} {
		mux.HandleFunc(signal.path, func(w http.ResponseWriter, _ *http.Request) {
			signal.count.Add(1)
			w.WriteHeader(http.StatusNoContent)
		})
	}
	mux.HandleFunc("/late-type-state", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"started_count":%d,"completed_count":%d}`, typeStartedCount.Load(), typeCompletedCount.Load())
	})
	mux.HandleFunc("/late-submit-state", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"started_count":%d,"completed_count":%d}`, submitStartedCount.Load(), submitCompletedCount.Load())
	})
	return mux, started, completed
}

// HandlerWithDispatchSignal serves the regular benchmark routes plus a
// one-shot signal for the disconnect-after-dispatch fixture.
func HandlerWithDispatchSignal() (http.Handler, <-chan struct{}) {
	dispatched := make(chan struct{}, 1)
	var once sync.Once
	mux := http.NewServeMux()
	mux.Handle("/", Handler())
	mux.HandleFunc("/disconnect-dispatched", func(w http.ResponseWriter, _ *http.Request) {
		once.Do(func() { dispatched <- struct{}{} })
		w.WriteHeader(http.StatusNoContent)
	})
	return mux, dispatched
}

// HandlerWithProcessRecovery serves a mutation endpoint that commits its
// server-side effect before blocking the response. The caller can kill the
// browser while the page still observes an unresolved request, then inspect
// the authoritative count from a fresh process.
func HandlerWithProcessRecovery() http.Handler {
	var committed atomic.Uint64
	var keyedCommitted atomic.Uint64
	var keyedRequests atomic.Uint64
	var keyedMu sync.Mutex
	keyedResults := make(map[string]int)
	flowMu := sync.Mutex{}
	flowResults := make(map[string]int)
	flowRequests := make(map[string]int)
	mux := http.NewServeMux()
	mux.Handle("/", Handler())
	mux.HandleFunc("/process-recovery", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<!doctype html><title>Process recovery fixture</title>
<p id="marker">pending</p><p id="status">Waiting</p><button id="seed-marker" onclick="localStorage.setItem('scout-recovery-marker','saved');document.querySelector('#marker').textContent='saved'">Save recovery marker</button>
<button id="mutate" onclick="fetch('/process-recovery-mutate',{method:'POST'}).then(()=>document.querySelector('#status').textContent='Mutation response received').catch(()=>document.querySelector('#status').textContent='Mutation response lost')">Perform mutation</button>
<script>document.querySelector('#marker').textContent=localStorage.getItem('scout-recovery-marker')||'missing'</script>`)
	})
	mux.HandleFunc("/process-recovery-mutate", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		committed.Add(1)
		<-r.Context().Done()
	})
	mux.HandleFunc("/process-recovery-state", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"committed_mutations":%d}`, committed.Load())
	})
	// Saved-flow routes provide a deterministic non-idempotent write. Operation
	// IDs prefixed with uncertain- commit the effect and hold the response until
	// the client disconnects, modeling a lost acknowledgment.
	mux.HandleFunc("/saved-flow-task", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		operationID := html.EscapeString(r.URL.Query().Get("operation_id"))
		frame := fmt.Sprintf(`<p id="status">Ready</p><p id="step-one-status">Pending</p><button id="step-one-button" onclick="document.querySelector('#step-one-status').textContent='First step complete'">Complete first step</button><button id="submit" onclick="fetch('/saved-flow-mutate?operation_id='+encodeURIComponent(document.querySelector('#operation-id').value),{method:'POST'}).then(()=>document.querySelector('#status').textContent='Acknowledged').catch(()=>document.querySelector('#status').textContent='Acknowledgment lost')">Commit task</button><input id="operation-id" value="%s">`, operationID)
		_, _ = fmt.Fprintf(w, `<!doctype html><title>Saved flow recovery fixture</title><iframe id="task-frame" name="task-frame" srcdoc="%s"></iframe>`, html.EscapeString(frame))
	})
	mux.HandleFunc("/saved-flow-mutate", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		operationID := strings.TrimSpace(r.URL.Query().Get("operation_id"))
		if operationID == "" {
			http.Error(w, "operation_id is required", http.StatusBadRequest)
			return
		}
		flowMu.Lock()
		flowRequests[operationID]++
		flowResults[operationID]++
		count := flowResults[operationID]
		flowMu.Unlock()
		if strings.HasPrefix(operationID, "uncertain-") {
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"operation_id":%q,"committed":%d}`, operationID, count)
	})
	mux.HandleFunc("/saved-flow-state", func(w http.ResponseWriter, r *http.Request) {
		operationID := strings.TrimSpace(r.URL.Query().Get("operation_id"))
		flowMu.Lock()
		count := flowResults[operationID]
		requests := flowRequests[operationID]
		flowMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"operation_id":%q,"committed":%d,"requests":%d}`, operationID, count, requests)
	})
	mux.HandleFunc("/idempotency-task", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<!doctype html><title>Idempotent task fixture</title><p id="status">Ready</p><button id="submit" onclick="fetch('/idempotent-mutation',{method:'POST',headers:{'Idempotency-Key':'benchmark-task-1'}}).then(r=>r.json()).then(x=>document.querySelector('#status').textContent='Committed '+x.committed_mutations+' replayed '+x.replayed)">Submit task</button>`)
	})
	mux.HandleFunc("/idempotent-mutation", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
		keyedRequests.Add(1)
		if key == "" || len(key) > 128 {
			http.Error(w, "valid Idempotency-Key required", http.StatusBadRequest)
			return
		}
		keyedMu.Lock()
		count, exists := keyedResults[key]
		if !exists {
			count = int(keyedCommitted.Add(1))
			keyedResults[key] = count
		}
		keyedMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if exists {
			w.Header().Set("Idempotency-Replayed", "true")
		}
		_, _ = fmt.Fprintf(w, `{"idempotency_key":%q,"committed_mutations":%d,"replayed":%t}`, key, count, exists)
	})
	mux.HandleFunc("/idempotent-mutation-state", func(w http.ResponseWriter, _ *http.Request) {
		keyedMu.Lock()
		defer keyedMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"committed_mutations":%d,"keys":%d,"requests":%d}`, keyedCommitted.Load(), len(keyedResults), keyedRequests.Load())
	})
	return mux
}

func formHandler(endpoint string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprintf(w, `<!doctype html><form id="checkout" onsubmit="event.preventDefault();fetch('%s',{method:'POST'}).then(()=>document.querySelector('#status').textContent='Order accepted')"><button>Place order</button></form><p id="status"></p>`, endpoint)
	}
}
