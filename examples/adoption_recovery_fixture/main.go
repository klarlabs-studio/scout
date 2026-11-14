// Command adoption_recovery_fixture serves the shared Scout/Stagehand adoption
// study page and authoritative approval ledger.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type server struct {
	mu            sync.Mutex
	commits       []commit
	attempts      []mutationAttempt
	firstSuccess  []firstSuccessEvent
	firstObserved []firstSuccessObservation
	releases      map[string]chan struct{}
	pageTokens    map[string]string
	ledgerFile    string
}

type commit struct {
	TaskID      string    `json:"task_id"`
	Sequence    int       `json:"sequence"`
	ReceivedAt  time.Time `json:"received_at"`
	CommittedAt time.Time `json:"committed_at"`
}

type mutationAttempt struct {
	TaskID     string    `json:"task_id"`
	ReceivedAt time.Time `json:"received_at"`
	Accepted   bool      `json:"accepted"`
	Rejection  string    `json:"rejection,omitempty"`
}

type ledger struct {
	TaskID                string            `json:"task_id"`
	Count                 int               `json:"count"`
	AttemptCount          int               `json:"attempt_count"`
	AcceptedDispatchCount int               `json:"accepted_dispatch_count"`
	Total                 int               `json:"total"`
	Entries               []commit          `json:"entries"`
	Attempts              []mutationAttempt `json:"attempts"`
}

type persistedLedger struct {
	Commits  []commit          `json:"commits"`
	Attempts []mutationAttempt `json:"attempts"`
}

type firstSuccessEvent struct {
	TaskID string    `json:"task_id"`
	At     time.Time `json:"at"`
}

type firstSuccessObservation struct {
	TaskID string    `json:"task_id"`
	Status string    `json:"status"`
	At     time.Time `json:"at"`
}

func main() {
	listen := flag.String("listen", "127.0.0.1:41990", "listen address")
	ledgerFile := flag.String("ledger-file", "", "optional JSON file for durable study ledger evidence")
	flag.Parse()
	host, _, err := net.SplitHostPort(*listen)
	if err != nil {
		log.Fatalf("invalid listen address: %v", err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		log.Fatal("fixture exposes unauthenticated operator endpoints and must bind to a loopback IP")
	}
	s := &server{releases: make(map[string]chan struct{}), pageTokens: make(map[string]string), ledgerFile: *ledgerFile}
	if *ledgerFile != "" {
		data, readErr := os.ReadFile(*ledgerFile)
		if readErr == nil {
			var saved persistedLedger
			if err := json.Unmarshal(data, &saved); err == nil {
				s.commits, s.attempts = saved.Commits, saved.Attempts
			} else {
				// Read the original v0 file format, which stored only a commit array.
				if legacyErr := json.Unmarshal(data, &s.commits); legacyErr != nil {
					log.Fatalf("decode ledger file: %v", legacyErr)
				}
			}
		} else if !errors.Is(readErr, os.ErrNotExist) {
			log.Fatalf("read ledger file: %v", readErr)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.page)
	mux.HandleFunc("/first-success/complete", s.completeFirstSuccess)
	mux.HandleFunc("/first-success/result", s.readFirstSuccess)
	mux.HandleFunc("/first-success/observed", s.recordFirstSuccessObservation)
	mux.HandleFunc("/first-success/observe", s.observeFirstSuccess)
	mux.HandleFunc("/submit", s.submit)
	mux.HandleFunc("/ledger", s.readLedger)
	mux.HandleFunc("/operator/release", s.release)
	mux.HandleFunc("/operator/reset", s.reset)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	httpServer := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdown)
	}()
	log.Printf("adoption recovery fixture listening on http://%s", *listen)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func (s *server) page(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/first-success" {
		taskID := strings.TrimSpace(r.URL.Query().Get("task"))
		if taskID == "" {
			http.Error(w, "task ID is required", http.StatusBadRequest)
			return
		}
		token := s.issuePageToken(taskID)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>First success</title><body data-task="%s" data-token="%s"><button id="complete" onclick="completeTask()">Complete task</button><p id="status">Waiting</p><script>async function completeTask(){const b=document.querySelector('#complete');b.disabled=true;try{await fetch('/first-success/complete?task='+encodeURIComponent(document.body.dataset.task),{method:'POST',headers:{'X-Study-Page-Token':document.body.dataset.token}});const s=document.querySelector('#status');s.textContent='Complete';await fetch('/first-success/observed?task='+encodeURIComponent(document.body.dataset.task)+'&status='+encodeURIComponent(s.textContent),{method:'POST',headers:{'X-Study-Page-Token':document.body.dataset.token}})}catch(e){b.disabled=false}}</script></body>`, html.EscapeString(taskID), html.EscapeString(token))
		return
	}
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	taskID := strings.TrimSpace(r.URL.Query().Get("task"))
	if taskID == "" {
		taskID = "study-approval-1"
	}
	token := s.issuePageToken(taskID)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>Approval task</title>
<body data-task="%s" data-token="%s"><main><h1>Approval task</h1><p id="task">Task: %s</p><button id="approve" onclick="submitApproval()">Approve once</button><p id="status">Ready</p></main>
<script>async function submitApproval(){const s=document.querySelector('#status');s.textContent='Submitting';try{const r=await fetch('/submit?task='+encodeURIComponent(document.body.dataset.task),{method:'POST',headers:{'X-Study-Page-Token':document.body.dataset.token}});const j=await r.json();s.textContent='Accepted '+j.sequence}catch(e){s.textContent='Response pending'}}</script></body>`, html.EscapeString(taskID), html.EscapeString(token), html.EscapeString(taskID))
}

func (s *server) issuePageToken(taskID string) string {
	bytes := make([]byte, 24)
	if _, err := rand.Read(bytes); err != nil {
		panic("secure random unavailable: " + err.Error())
	}
	token := hex.EncodeToString(bytes)
	s.mu.Lock()
	s.pageTokens[taskID] = token
	s.mu.Unlock()
	return token
}

func (s *server) requirePageRequest(w http.ResponseWriter, r *http.Request, taskID string) bool {
	origin := "http://" + r.Host
	s.mu.Lock()
	token := s.pageTokens[taskID]
	s.mu.Unlock()
	if token == "" || r.Header.Get("Origin") != origin ||
		r.Header.Get("Sec-Fetch-Site") != "same-origin" ||
		r.Header.Get("Sec-Fetch-Mode") != "cors" ||
		r.Header.Get("X-Study-Page-Token") != token {
		http.Error(w, "request must originate from the active study page", http.StatusForbidden)
		return false
	}
	return true
}

func (s *server) completeFirstSuccess(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	taskID := strings.TrimSpace(r.URL.Query().Get("task"))
	if taskID == "" || len(taskID) > 128 {
		http.Error(w, "valid task ID is required", http.StatusBadRequest)
		return
	}
	if !s.requirePageRequest(w, r, taskID) {
		return
	}
	event := firstSuccessEvent{TaskID: taskID, At: time.Now().UTC()}
	s.mu.Lock()
	s.firstSuccess = append(s.firstSuccess, event)
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(event)
}

func (s *server) readFirstSuccess(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	taskID := strings.TrimSpace(r.URL.Query().Get("task"))
	s.mu.Lock()
	events := make([]firstSuccessEvent, 0)
	for _, event := range s.firstSuccess {
		if event.TaskID == taskID {
			events = append(events, event)
		}
	}
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"task_id": taskID, "count": len(events), "events": events})
}

// recordFirstSuccessObservation records the status rendered by the fixture
// page after its button handler updates the DOM. It is stronger evidence than
// the click event alone, but source review is still needed to rule out a direct
// call to this endpoint by a participant adapter.
func (s *server) recordFirstSuccessObservation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	taskID := strings.TrimSpace(r.URL.Query().Get("task"))
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if taskID == "" || status != "Complete" {
		http.Error(w, "task ID and Complete status are required", http.StatusBadRequest)
		return
	}
	if !s.requirePageRequest(w, r, taskID) {
		return
	}
	observation := firstSuccessObservation{TaskID: taskID, Status: status, At: time.Now().UTC()}
	s.mu.Lock()
	s.firstObserved = append(s.firstObserved, observation)
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(observation)
}

func (s *server) observeFirstSuccess(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	taskID := strings.TrimSpace(r.URL.Query().Get("task"))
	s.mu.Lock()
	observations := make([]firstSuccessObservation, 0)
	for _, observation := range s.firstObserved {
		if observation.TaskID == taskID {
			observations = append(observations, observation)
		}
	}
	eventCount := 0
	for _, event := range s.firstSuccess {
		if event.TaskID == taskID {
			eventCount++
		}
	}
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"task_id": taskID, "event_count": eventCount, "observations": observations,
		"status": lastObservedStatus(observations), "complete_click_count": len(observations),
	})
}

func lastObservedStatus(observations []firstSuccessObservation) string {
	if len(observations) == 0 {
		return ""
	}
	return observations[len(observations)-1].Status
}

func (s *server) submit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	taskID := strings.TrimSpace(r.URL.Query().Get("task"))
	if taskID == "" || len(taskID) > 128 {
		http.Error(w, "valid task ID is required", http.StatusBadRequest)
		return
	}
	attempt := mutationAttempt{TaskID: taskID, ReceivedAt: time.Now().UTC()}
	if !s.requirePageRequest(w, r, taskID) {
		attempt.Rejection = "page_request_validation"
		s.mu.Lock()
		s.attempts = append(s.attempts, attempt)
		if err := s.persistLocked(); err != nil {
			s.attempts = s.attempts[:len(s.attempts)-1]
			s.mu.Unlock()
			http.Error(w, "could not persist rejected attempt", http.StatusInternalServerError)
			return
		}
		s.mu.Unlock()
		return
	}
	s.mu.Lock()
	attempt.Accepted = true
	s.attempts = append(s.attempts, attempt)
	entry := commit{TaskID: taskID, Sequence: len(s.commits) + 1, ReceivedAt: attempt.ReceivedAt, CommittedAt: time.Now().UTC()}
	s.commits = append(s.commits, entry)
	if err := s.persistLocked(); err != nil {
		s.commits = s.commits[:len(s.commits)-1]
		s.attempts = s.attempts[:len(s.attempts)-1]
		s.mu.Unlock()
		http.Error(w, "could not persist fixture ledger", http.StatusInternalServerError)
		return
	}
	gate := s.releases[taskID]
	if gate == nil {
		gate = make(chan struct{})
		s.releases[taskID] = gate
	}
	s.mu.Unlock()
	// The commit is durable in the fixture process before the response is held.
	select {
	case <-gate:
	case <-r.Context().Done():
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(entry)
}

func (s *server) readLedger(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	taskID := strings.TrimSpace(r.URL.Query().Get("task"))
	s.mu.Lock()
	entries := make([]commit, 0)
	for _, item := range s.commits {
		if taskID == "" || item.TaskID == taskID {
			entries = append(entries, item)
		}
	}
	attempts := make([]mutationAttempt, 0)
	for _, item := range s.attempts {
		if taskID == "" || item.TaskID == taskID {
			attempts = append(attempts, item)
		}
	}
	total := len(s.commits)
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	accepted := 0
	for _, item := range attempts {
		if item.Accepted {
			accepted++
		}
	}
	_ = json.NewEncoder(w).Encode(ledger{TaskID: taskID, Count: len(entries), AttemptCount: len(attempts), AcceptedDispatchCount: accepted, Total: total, Entries: entries, Attempts: attempts})
}

func (s *server) release(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	taskID := strings.TrimSpace(r.URL.Query().Get("task"))
	if taskID == "" {
		http.Error(w, "task ID is required", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	gate := s.releases[taskID]
	if gate == nil {
		gate = make(chan struct{})
		s.releases[taskID] = gate
	}
	select {
	case <-gate:
	default:
		close(gate)
	}
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) reset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.mu.Lock()
	for _, gate := range s.releases {
		select {
		case <-gate:
		default:
			close(gate)
		}
	}
	s.commits = nil
	s.attempts = nil
	s.firstSuccess = nil
	s.firstObserved = nil
	s.releases = make(map[string]chan struct{})
	s.pageTokens = make(map[string]string)
	if err := s.persistLocked(); err != nil {
		s.mu.Unlock()
		http.Error(w, "could not persist fixture reset", http.StatusInternalServerError)
		return
	}
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) persistLocked() error {
	if s.ledgerFile == "" {
		return nil
	}
	data, err := json.MarshalIndent(persistedLedger{Commits: s.commits, Attempts: s.attempts}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.ledgerFile), ".adoption-ledger-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, s.ledgerFile); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(s.ledgerFile))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
