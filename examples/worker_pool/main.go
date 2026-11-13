// Example: worker_pool runs independent browser sessions in a bounded Go
// worker pool. Each worker owns one session and closes it when the pool stops.
//
// Usage:
//
//	go run ./examples/worker_pool -workers 2 -jobs 4
//
// Set SCOUT_REMOTE_CDP to attach to a separately managed browser instead of
// launching one browser process per worker. The remote browser must support
// isolated browser contexts for concurrent workers.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"go.klarlabs.de/scout"
	"go.klarlabs.de/scout/agent"
)

type jobResult struct {
	JobID     int                         `json:"job_id"`
	Worker    int                         `json:"worker,omitempty"`
	Status    string                      `json:"status"`
	Phase     string                      `json:"phase,omitempty"`
	ErrorCode string                      `json:"error_code,omitempty"`
	Action    *agent.PageResult           `json:"action,omitempty"`
	Recovery  *agent.ActionRecoveryRecord `json:"recovery,omitempty"`
	Error     string                      `json:"error,omitempty"`
}

func main() {
	workers := flag.Int("workers", 2, "number of independent browser sessions")
	jobs := flag.Int("jobs", 4, "number of deterministic fixture jobs")
	isolateRemote := flag.Bool("isolate-remote-contexts", false, "create an isolated browser context per worker when SCOUT_REMOTE_CDP is set")
	fixtureURL := flag.String("fixture-url", "", "base URL for a running deterministic worker fixture (starts an in-process fixture when omitted)")
	allowPrivateIPs := flag.Bool("allow-private-ips", false, "allow private-address fixture URLs; only use with a trusted fixture you control")
	flag.Parse()
	if flag.NArg() != 0 || *workers < 1 || *jobs < 1 {
		fmt.Fprintln(os.Stderr, "Usage: go run ./examples/worker_pool [-workers N] [-jobs N]")
		os.Exit(2)
	}
	if os.Getenv("SCOUT_REMOTE_CDP") != "" && *workers > 1 && !*isolateRemote {
		fmt.Fprintln(os.Stderr, "remote CDP worker pools with multiple workers require -isolate-remote-contexts")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	workCtx, cancelWork := context.WithTimeout(ctx, 3*time.Minute)
	defer cancelWork()
	if err := run(workCtx, *workers, *jobs, *isolateRemote, *fixtureURL, *allowPrivateIPs); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, workerCount, jobCount int, isolateRemote bool, fixtureURL string, allowPrivateIPs bool) (runErr error) {
	var server *http.Server
	ownsFixture := fixtureURL == ""
	if fixtureURL == "" {
		listenAddress := "127.0.0.1:0"
		if os.Getenv("SCOUT_REMOTE_CDP") != "" && isolateRemote {
			listenAddress = "0.0.0.0:0"
		}
		listener, err := net.Listen("tcp", listenAddress)
		if err != nil {
			return fmt.Errorf("start local fixture listener: %w", err)
		}
		server = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, err := strconv.Atoi(r.URL.Query().Get("id"))
			if err != nil || id < 1 {
				http.Error(w, "invalid job id", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(w, `<!doctype html><title>Scout worker job</title><p id="status">waiting</p><button id="complete" onclick="document.querySelector('#status').textContent='job %d complete'">Complete</button>`, id)
		})}
		serveErr := make(chan error, 1)
		go func() { serveErr <- server.Serve(listener) }()
		defer func() {
			if err := server.Close(); err != nil {
				runErr = errors.Join(runErr, fmt.Errorf("close local fixture server: %w", err))
			}
			if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
				runErr = errors.Join(runErr, fmt.Errorf("serve local fixture: %w", err))
			}
		}()
		_, port, err := net.SplitHostPort(listener.Addr().String())
		if err != nil {
			return fmt.Errorf("read local fixture address: %w", err)
		}
		fixtureURL = "http://127.0.0.1:" + port
	} else if !strings.HasPrefix(fixtureURL, "http://") && !strings.HasPrefix(fixtureURL, "https://") {
		return fmt.Errorf("fixture URL must use http or https")
	}

	poolCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan int)
	// Bound queued result memory by concurrency. A slow stdout consumer should
	// apply backpressure instead of buffering one full batch of receipts.
	results := make(chan jobResult, workerCount)
	var errorMu sync.Mutex
	var workerErrors []error
	reportError := func(err error) {
		if err == nil {
			return
		}
		errorMu.Lock()
		workerErrors = append(workerErrors, err)
		errorMu.Unlock()
	}
	var workers sync.WaitGroup
	for workerID := 1; workerID <= workerCount; workerID++ {
		workers.Add(1)
		go func(id int) {
			defer workers.Done()
			session, err := agent.NewSessionContext(poolCtx, agent.SessionConfig{
				Headless: true, Timeout: 20 * time.Second,
				AllowedOrigins: []string{fixtureURL}, AllowPrivateIPs: ownsFixture || allowPrivateIPs,
				RemoteCDP: os.Getenv("SCOUT_REMOTE_CDP"), IsolateRemoteBrowserContext: isolateRemote,
			})
			if err != nil {
				if poolCtx.Err() != nil {
					return
				}
				reportError(fmt.Errorf("start worker %d session: %w", id, err))
				cancel()
				return
			}
			defer func() {
				closeWorkerSession(id, session.CloseContext, reportError)
			}()
			for {
				select {
				case <-poolCtx.Done():
					return
				case jobID, ok := <-jobs:
					if !ok {
						return
					}
					result := jobResult{JobID: jobID, Worker: id}
					url := fmt.Sprintf("%s/?id=%d", strings.TrimRight(fixtureURL, "/"), jobID)
					if _, err := session.NavigateWithPostconditionContext(poolCtx, url, agent.Postcondition{Text: "waiting"}); err != nil {
						publishJobFailure(results, &result, err, "navigate")
						if result.Status == "needs_reconciliation" {
							return
						}
						continue
					}
					action, err := session.ClickWithPostconditionContext(poolCtx, "#complete", agent.Postcondition{Text: fmt.Sprintf("job %d complete", jobID)}, false)
					if err != nil {
						publishJobFailure(results, &result, err, "click")
						if result.Status == "needs_reconciliation" {
							return
						}
						continue
					}
					if action == nil || action.Receipt == nil || action.Receipt.SchemaVersion != 1 || action.Receipt.Verification != agent.VerificationVerified || action.Receipt.Outcome != agent.OutcomePostconditionVerified || action.Receipt.TargetID == "" || action.Receipt.Action != "clicked" || action.Receipt.OperationOrdinal != action.OperationOrdinal || action.Receipt.RunID == "" || action.Receipt.PolicyID == "" {
						verificationErr := fmt.Errorf("worker %d job %d did not satisfy the action receipt contract", id, jobID)
						result.Status = "failed"
						result.Phase = "verify_receipt"
						result.ErrorCode = "SCOUT_RECEIPT_INVALID"
						result.Error = "action receipt failed the worker completion contract"
						results <- result
						reportError(verificationErr)
						cancel()
						return
					}
					results <- jobResult{JobID: jobID, Worker: id, Status: "completed", Phase: "complete", Action: action}
				}
			}
		}(workerID)
	}

	go func() {
		defer close(jobs)
		for id := 1; id <= jobCount; id++ {
			select {
			case jobs <- id:
			case <-poolCtx.Done():
				return
			}
		}
	}()
	go func() {
		workers.Wait()
		close(results)
	}()

	completed := 0
	reportedJobs := make(map[int]struct{}, jobCount)
	var outputErr error
	for result := range results {
		reportedJobs[result.JobID] = struct{}{}
		if outputErr == nil {
			if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
				outputErr = fmt.Errorf("write job result: %w", err)
				cancel()
			}
		} else {
			continue
		}
		if outputErr != nil {
			cancel()
			continue
		}
		if result.Status == "completed" {
			completed++
		}
	}
	if outputErr != nil {
		return joinWorkerPoolErrors(outputErr, ctx.Err(), workerErrors)
	}
	for id := 1; id <= jobCount; id++ {
		if _, reported := reportedJobs[id]; reported {
			continue
		}
		if err := json.NewEncoder(os.Stdout).Encode(jobResult{
			JobID: id, Status: "not_started", Phase: "dispatch",
			ErrorCode: "SCOUT_JOB_NOT_STARTED", Error: "worker pool stopped before this job started",
		}); err != nil {
			return joinWorkerPoolErrors(fmt.Errorf("write unstarted job result: %w", err), ctx.Err(), workerErrors)
		}
	}
	if err := joinWorkerPoolErrors(nil, ctx.Err(), workerErrors); err != nil {
		return err
	}
	if completed != jobCount {
		return fmt.Errorf("worker pool completed %d of %d jobs", completed, jobCount)
	}
	return nil
}

func closeWorkerSession(workerID int, closeSession func(context.Context) error, reportError func(error)) {
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer closeCancel()
	if err := closeSession(closeCtx); err != nil {
		reportError(fmt.Errorf("close worker %d session: %w", workerID, err))
	}
}

func joinWorkerPoolErrors(outputErr, contextErr error, workerErrors []error) error {
	errs := make([]error, 0, 2+len(workerErrors))
	if outputErr != nil {
		errs = append(errs, outputErr)
	}
	if contextErr != nil {
		errs = append(errs, contextErr)
	}
	errs = append(errs, workerErrors...)
	return errors.Join(errs...)
}

func publishJobFailure(results chan<- jobResult, result *jobResult, err error, phase string) {
	result.Phase = phase
	var unknown *agent.ActionOutcomeError
	if errors.As(err, &unknown) {
		result.Status = "needs_reconciliation"
		result.ErrorCode = "SCOUT_ACTION_OUTCOME_UNKNOWN"
		record, recordErr := agent.NewActionRecoveryRecord(unknown)
		if recordErr != nil {
			result.Error = fmt.Sprintf("%s outcome is unknown and recovery record creation failed: %v", phase, recordErr)
		} else {
			result.Recovery = &record
		}
	} else {
		result.Status = "failed"
		result.ErrorCode = workerErrorCode(err)
		result.Error = fmt.Sprintf("%s: %v", phase, err)
	}
	results <- *result
}

func workerErrorCode(err error) string {
	var policyErr *browse.URLPolicyError
	if errors.As(err, &policyErr) && policyErr.Code != "" {
		return policyErr.Code
	}
	var operationErr *agent.OperationError
	if errors.As(err, &operationErr) && operationErr.Phase != "" {
		return "SCOUT_OPERATION_" + strings.ToUpper(strings.ReplaceAll(operationErr.Phase, " ", "_"))
	}
	var navigationErr *browse.NavigationError
	if errors.As(err, &navigationErr) {
		return "SCOUT_NAVIGATION_FAILED"
	}
	return "SCOUT_JOB_FAILED"
}
