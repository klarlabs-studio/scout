package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	cdpclient "go.klarlabs.de/scout/internal/cdp"
)

func TestEnsureRendererResponsiveContext(t *testing.T) {
	t.Run("responsive renderer does not activate", func(t *testing.T) {
		activations := 0
		recovery, err := ensureRendererResponsiveContext(context.Background(), func(context.Context) error { return nil }, func(context.Context) error {
			activations++
			return nil
		})
		if err != nil || recovery != rendererAlreadyResponsive || activations != 0 {
			t.Fatalf("recovery=%v err=%v activations=%d", recovery, err, activations)
		}
	})

	t.Run("unresponsive renderer activates and is confirmed", func(t *testing.T) {
		probes, activations := 0, 0
		recovery, err := ensureRendererResponsiveContext(context.Background(), func(context.Context) error {
			probes++
			if probes == 1 {
				return context.DeadlineExceeded
			}
			return nil
		}, func(context.Context) error { activations++; return nil })
		if err != nil || recovery != rendererRecovered || probes != 2 || activations != 1 {
			t.Fatalf("recovery=%v err=%v probes=%d activations=%d", recovery, err, probes, activations)
		}
	})

	t.Run("activation dispatch with missing response remains unknown", func(t *testing.T) {
		wantErr := &cdpclient.DispatchOutcomeUnknownError{Method: "Target.activateTarget", Err: context.DeadlineExceeded}
		recovery, err := ensureRendererResponsiveContext(context.Background(), func(context.Context) error {
			return context.DeadlineExceeded
		}, func(context.Context) error { return wantErr })
		if recovery != rendererActivationUnknown || !errors.Is(err, wantErr) {
			t.Fatalf("recovery=%v err=%v, want unknown dispatched activation", recovery, err)
		}
	})

	t.Run("activation failure is returned", func(t *testing.T) {
		wantErr := errors.New("activation failed")
		_, err := ensureRendererResponsiveContext(context.Background(), func(context.Context) error { return context.DeadlineExceeded }, func(context.Context) error { return wantErr })
		if !errors.Is(err, wantErr) {
			t.Fatalf("error = %v, want wrapped activation failure", err)
		}
	})

	t.Run("caller deadline stops before activation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		var activations int
		_, err := ensureRendererResponsiveContext(ctx, func(context.Context) error { cancel(); return context.Canceled }, func(context.Context) error { activations++; return nil })
		if !errors.Is(err, context.Canceled) || activations != 0 {
			t.Fatalf("err=%v activations=%d", err, activations)
		}
	})

	t.Run("probe context is bounded", func(t *testing.T) {
		var deadline time.Time
		_, err := ensureRendererResponsiveContext(context.Background(), func(ctx context.Context) error {
			var ok bool
			deadline, ok = ctx.Deadline()
			if !ok {
				t.Fatal("probe has no deadline")
			}
			return nil
		}, func(context.Context) error { return nil })
		if err != nil || time.Until(deadline) > 300*time.Millisecond {
			t.Fatalf("err=%v probe deadline=%s", err, deadline)
		}
	})

	t.Run("activation context is bounded", func(t *testing.T) {
		var deadline time.Time
		_, err := ensureRendererResponsiveContext(context.Background(), func(context.Context) error {
			return context.DeadlineExceeded
		}, func(ctx context.Context) error {
			var ok bool
			deadline, ok = ctx.Deadline()
			if !ok {
				t.Fatal("activation has no deadline")
			}
			return errors.New("activation failed")
		})
		if err == nil || time.Until(deadline) > time.Second {
			t.Fatalf("err=%v activation deadline=%s", err, deadline)
		}
	})
}

func TestNewTabManager(t *testing.T) {
	tm := newTabManager()
	if tm == nil {
		t.Fatal("newTabManager returned nil")
	}
	if tm.tabs == nil {
		t.Error("tabs map should be initialized")
	}
	if len(tm.tabs) != 0 {
		t.Errorf("tabs should be empty, got %d", len(tm.tabs))
	}
	if tm.active != "" {
		t.Errorf("active should be empty, got %q", tm.active)
	}
}

func TestTabInfo_JSON(t *testing.T) {
	ti := TabInfo{
		Name:   "main",
		URL:    "https://example.com",
		Title:  "Example",
		Active: true,
	}
	data, err := json.Marshal(ti)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got TabInfo
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got != ti {
		t.Errorf("roundtrip: got %+v, want %+v", got, ti)
	}
}

func TestTabInfo_JSON_Inactive(t *testing.T) {
	ti := TabInfo{
		Name:   "background",
		URL:    "https://other.com",
		Active: false,
	}
	data, err := json.Marshal(ti)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got TabInfo
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Active != false {
		t.Error("Active should be false")
	}
}
