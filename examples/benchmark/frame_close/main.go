// Command frame_close runs the frame-scope-after-unrelated-tab-close case
// against the same shared fixture server used by external comparison tools.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"go.klarlabs.de/scout/agent"
)

type result struct {
	Success             bool   `json:"success"`
	TargetID            string `json:"target_id"`
	FrameIDBefore       string `json:"frame_id_before"`
	FrameIDAfter        string `json:"frame_id_after"`
	UnrelatedTargetID   string `json:"unrelated_target_id"`
	UnrelatedCloseMatch bool   `json:"unrelated_close_target_matches"`
	ActionVerified      bool   `json:"action_verified"`
	Outcome             string `json:"outcome"`
	Evidence            string `json:"evidence,omitempty"`
	Error               string `json:"error,omitempty"`
}

func run() (out result, retErr error) {
	baseURL := os.Getenv("SCOUT_BENCHMARK_BASE_URL")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:41731"
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(baseURL + "/healthz")
	if err != nil {
		return out, fmt.Errorf("check fixture server: %w", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("fixture server health check returned %s", resp.Status)
	}

	session, err := agent.NewSession(agent.SessionConfig{Headless: true, Timeout: 15 * time.Second, AllowPrivateIPs: true})
	if err != nil {
		return out, fmt.Errorf("start Scout session: %w", err)
	}
	defer func() {
		if err := session.Close(); err != nil && retErr == nil {
			retErr = err
		}
	}()

	unrelated, err := session.OpenTab("unrelated-background", baseURL+"/foreground-tab")
	if err != nil {
		return out, fmt.Errorf("open unrelated tab: %w", err)
	}
	out.UnrelatedTargetID = unrelated.TargetID
	frameTask, err := session.OpenTab("frame-task", baseURL+"/iframe")
	if err != nil {
		return out, fmt.Errorf("open frame task: %w", err)
	}
	frame, err := session.SwitchToFrame("#task-frame")
	if err != nil {
		return out, fmt.Errorf("select task frame: %w", err)
	}
	out.TargetID, out.FrameIDBefore = frame.TargetID, frame.FrameID
	closed, err := session.CloseTabResult("unrelated-background")
	if err != nil {
		return out, fmt.Errorf("close unrelated background tab: %w", err)
	}
	out.UnrelatedCloseMatch = closed.TargetID == out.UnrelatedTargetID && closed.Verified
	out.FrameIDAfter = frame.FrameID
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	clicked, err := session.ClickByHandleWithIdentityContext(ctx, "frame-save", out.TargetID, out.FrameIDBefore, agent.Postcondition{Text: "Frame task complete"})
	if err != nil {
		return out, fmt.Errorf("click task frame after close: %w", err)
	}
	out.ActionVerified = clicked.Verified && clicked.Outcome == string(agent.OutcomePostconditionVerified)
	out.Outcome, out.Evidence = clicked.Outcome, clicked.Evidence
	out.Success = out.TargetID != "" && out.TargetID == frameTask.TargetID && out.TargetID == clicked.TargetID && out.FrameIDBefore != "" && out.FrameIDBefore == out.FrameIDAfter && out.FrameIDAfter == clicked.FrameID && out.UnrelatedTargetID != "" && out.UnrelatedTargetID != out.TargetID && out.UnrelatedCloseMatch && out.ActionVerified
	if !out.Success {
		out.Error = fmt.Sprintf("identity contract failed: task_target=%q click_target=%q frame_before=%q click_frame=%q unrelated_target=%q close_match=%t action_verified=%t", out.TargetID, clicked.TargetID, out.FrameIDBefore, clicked.FrameID, out.UnrelatedTargetID, out.UnrelatedCloseMatch, out.ActionVerified)
	}
	return out, nil
}

func main() {
	out, err := run()
	if err != nil {
		out.Error = err.Error()
	}
	if encodeErr := json.NewEncoder(os.Stdout).Encode(out); encodeErr != nil {
		fmt.Fprintln(os.Stderr, encodeErr)
		os.Exit(1)
	}
	if err != nil || !out.Success {
		os.Exit(1)
	}
}
