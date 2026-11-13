package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	cdpclient "go.klarlabs.de/scout/internal/cdp"
)

func TestSessionMutexLockContextCancellation(t *testing.T) {
	var mu sessionMutex
	if err := mu.LockContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := mu.LockContext(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("LockContext error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Fatalf("LockContext returned after %s despite deadline", elapsed)
	}
	mu.Unlock()
	canceled, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	if err := mu.LockContext(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("LockContext with an already canceled context = %v, want context.Canceled", err)
	}
	if err := mu.LockContext(context.Background()); err != nil {
		t.Fatalf("lock gate was not released: %v", err)
	}
	mu.Unlock()
}

func TestContextAwareSessionReadsRejectCanceledContext(t *testing.T) {
	s := &Session{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if got, err := s.CapturedRequestsContext(ctx, ""); !errors.Is(err, context.Canceled) || got != nil {
		t.Fatalf("CapturedRequestsContext() = (%v, %v), want (nil, context.Canceled)", got, err)
	}
	if got, err := s.IsNetworkCaptureEnabledContext(ctx); !errors.Is(err, context.Canceled) || got {
		t.Fatalf("IsNetworkCaptureEnabledContext() = (%v, %v), want (false, context.Canceled)", got, err)
	}
	if got, err := s.NetworkSummaryContext(ctx, ""); !errors.Is(err, context.Canceled) || got != nil {
		t.Fatalf("NetworkSummaryContext() = (%v, %v), want (nil, context.Canceled)", got, err)
	}
	if got, err := s.SessionHistoryContext(ctx, 5); !errors.Is(err, context.Canceled) || got != nil {
		t.Fatalf("SessionHistoryContext() = (%v, %v), want (nil, context.Canceled)", got, err)
	}
	if got, err := s.StatusContext(ctx); !errors.Is(err, context.Canceled) || got != nil {
		t.Fatalf("StatusContext() = (%v, %v), want (nil, context.Canceled)", got, err)
	}
	if err := s.StartRecordingPlaybookContext(ctx, "test"); !errors.Is(err, context.Canceled) {
		t.Fatalf("StartRecordingPlaybookContext() = %v, want context.Canceled", err)
	}
	if _, err := s.StopRecordingPlaybookContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("StopRecordingPlaybookContext() = %v, want context.Canceled", err)
	}
	if err := s.ExpectRecordedActionContext(ctx, ActionExpect{Text: "done"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("ExpectRecordedActionContext() = %v, want context.Canceled", err)
	}
}

func TestContextAwareSessionReadsHonorLockDeadline(t *testing.T) {
	s := &Session{}
	if err := s.mu.LockContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	start := time.Now()
	if got, err := s.CapturedRequestsContext(ctx, ""); !errors.Is(err, context.DeadlineExceeded) || got != nil {
		t.Fatalf("CapturedRequestsContext() = (%v, %v), want (nil, context.DeadlineExceeded)", got, err)
	}
	if got, err := s.StatusContext(ctx); !errors.Is(err, context.DeadlineExceeded) || got != nil {
		t.Fatalf("StatusContext() = (%v, %v), want (nil, context.DeadlineExceeded)", got, err)
	}
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Fatalf("context-aware read waited %s for the session lock", elapsed)
	}
	s.mu.Unlock()
}

func TestIndependentSessionLockRemainsAvailableWhenPeerWaitExpires(t *testing.T) {
	blocked := &Session{}
	independent := &Session{}
	if err := blocked.mu.LockContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer blocked.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	queuedDone := make(chan error, 1)
	go func() { queuedDone <- blocked.mu.LockContext(ctx) }()

	started := time.Now()
	err := independent.mu.LockContext(context.Background())
	independentDuration := time.Since(started)
	if err != nil {
		t.Fatalf("independent session lock was affected by peer wait: %v", err)
	}
	independent.mu.Unlock()
	if independentDuration > 100*time.Millisecond {
		t.Fatalf("independent session lock waited %s for peer session", independentDuration)
	}

	select {
	case err := <-queuedDone:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("blocked session lock = %v, want context deadline", err)
		}
	case <-time.After(time.Second):
		t.Fatal("queued lock did not return after caller deadline")
	}
}

func TestMutationOutcomeMayBeUnknownMethodClassification(t *testing.T) {
	tests := []struct {
		method string
		want   bool
	}{
		{method: "Page.addScriptToEvaluateOnNewDocument", want: true},
		{method: "Page.createIsolatedWorld", want: true},
		{method: "Target.attachToTarget", want: true},
		{method: "Target.activateTarget", want: true},
		{method: "Target.detachFromTarget", want: true},
		{method: "Network.setUserAgentOverride", want: true},
		{method: "Network.enable", want: true},
		{method: "Input.dispatchMouseEvent", want: true},
		{method: "DOM.getDocument", want: false},
		{method: "Page.captureScreenshot", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			err := &cdpclient.DispatchOutcomeUnknownError{Method: tt.method, Err: context.Canceled}
			if got := mutationOutcomeMayBeUnknown(err); got != tt.want {
				t.Errorf("mutationOutcomeMayBeUnknown(%q) = %v, want %v", tt.method, got, tt.want)
			}
		})
	}
	if mutationOutcomeMayBeUnknown(context.Canceled) {
		t.Fatal("non-CDP cancellation must not be classified as a dispatched mutation")
	}
}
