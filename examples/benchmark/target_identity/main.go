// Command target_identity runs only the active-target identity regression case.
// It is kept separate from the full MCP parity harness so the tab lifecycle
// contract can be verified without starting an MCP stdio client.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"time"

	"go.klarlabs.de/scout/agent"
	benchmarkfixture "go.klarlabs.de/scout/examples/benchmark/fixture"
)

type result struct {
	Success      bool             `json:"success"`
	TargetID     string           `json:"target_id"`
	URL          string           `json:"url"`
	Visibility   string           `json:"visibility"`
	Verified     bool             `json:"verified"`
	Outcome      string           `json:"outcome"`
	ForegroundOK bool             `json:"foreground_preserved"`
	Evidence     string           `json:"evidence,omitempty"`
	Error        string           `json:"error,omitempty"`
	FrameScope   frameScopeResult `json:"frame_scope_after_unrelated_tab_close"`
}

type frameScopeResult struct {
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

func run() (retErr error) {
	server := httptest.NewServer(benchmarkfixture.Handler())
	defer server.Close()
	url := server.URL + "/background-tab"
	session, err := agent.NewSession(agent.SessionConfig{Headless: true, Timeout: 15 * time.Second, AllowPrivateIPs: true})
	if err != nil {
		return err
	}
	defer func() {
		if err := session.Close(); err != nil && retErr == nil {
			retErr = err
		}
	}()

	if _, err := session.OpenTab("background-task", url); err != nil {
		return err
	}
	if _, err := session.OpenTab("foreground-guard", server.URL+"/foreground-tab"); err != nil {
		return err
	}
	tabs, err := session.ListTabs()
	if err != nil {
		return err
	}
	var wasHidden bool
	var targetBefore string
	for _, tab := range tabs {
		if tab.Name == "background-task" {
			wasHidden, targetBefore = tab.VisibilityState == "hidden", tab.TargetID
		}
	}
	switched, err := session.SwitchTab("background-task")
	if err != nil {
		return err
	}
	clicked, err := session.ClickWithPostcondition("#background-action", agent.Postcondition{Text: "Background task complete"}, false)
	if err != nil {
		return err
	}
	tabs, err = session.ListTabs()
	if err != nil {
		return err
	}
	out := result{Verified: clicked.Verified, Outcome: clicked.Outcome, Evidence: clicked.Evidence}
	for _, tab := range tabs {
		if tab.Name == "background-task" {
			out.TargetID, out.URL, out.Visibility = tab.TargetID, tab.URL, tab.VisibilityState
		}
		if tab.Name == "foreground-guard" && tab.Title == "Foreground tab guard" {
			out.ForegroundOK = true
		}
	}
	out.Success = wasHidden && targetBefore != "" && out.TargetID == targetBefore && switched.Target == "background-task" && switched.URL == url && out.URL == url && out.Visibility == "visible" && clicked.URL == url && out.Verified && out.Outcome == "postcondition_verified" && out.ForegroundOK
	if !out.Success {
		out.Error = fmt.Sprintf("target transition failed: hidden_before=%t target_before=%q target_after=%q switched_target=%q switched_url=%q final_url=%q visibility=%q foreground_preserved=%t verified=%t outcome=%q", wasHidden, targetBefore, out.TargetID, switched.Target, switched.URL, out.URL, out.Visibility, out.ForegroundOK, out.Verified, out.Outcome)
	}
	frameResult, frameErr := runFrameScopeAfterTabClose(session, server.URL)
	out.FrameScope = frameResult
	if frameErr != nil && out.Error == "" {
		out.Error = frameErr.Error()
	}
	out.Success = out.Success && frameResult.Success
	if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
		return err
	}
	if !out.Success {
		return fmt.Errorf("active-target identity scenario failed")
	}
	return nil
}

func runFrameScopeAfterTabClose(session *agent.Session, baseURL string) (frameScopeResult, error) {
	var result frameScopeResult
	taskURL := baseURL + "/iframe"
	unrelated, err := session.OpenTab("unrelated-background", baseURL+"/foreground-tab")
	if err != nil {
		result.Error = "open unrelated tab: " + err.Error()
		return result, fmt.Errorf("frame scope scenario: %s", result.Error)
	}
	result.UnrelatedTargetID = unrelated.TargetID
	frameTask, err := session.OpenTab("frame-task", taskURL)
	if err != nil {
		result.Error = "open frame task: " + err.Error()
		return result, fmt.Errorf("frame scope scenario: %s", result.Error)
	}
	frameBefore, err := session.SwitchToFrame("#task-frame")
	if err != nil {
		result.Error = "select task frame: " + err.Error()
		return result, fmt.Errorf("frame scope scenario: %s", result.Error)
	}
	result.TargetID, result.FrameIDBefore = frameBefore.TargetID, frameBefore.FrameID
	closed, err := session.CloseTabResult("unrelated-background")
	if err != nil {
		result.Error = "close unrelated background tab: " + err.Error()
		return result, fmt.Errorf("frame scope scenario: %s", result.Error)
	}
	result.UnrelatedCloseMatch = closed.TargetID == result.UnrelatedTargetID && closed.Verified
	result.FrameIDAfter = frameBefore.FrameID
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	clicked, err := session.ClickByHandleWithIdentityContext(ctx, "frame-save", result.TargetID, result.FrameIDBefore, agent.Postcondition{Text: "Frame task complete"})
	if err != nil {
		result.Error = "click task frame after close: " + err.Error()
		return result, fmt.Errorf("frame scope scenario: %s", result.Error)
	}
	result.ActionVerified = clicked.Verified && clicked.Outcome == "postcondition_verified"
	result.Outcome, result.Evidence = clicked.Outcome, clicked.Evidence
	result.Success = result.TargetID != "" && result.TargetID == frameTask.TargetID && result.TargetID == clicked.TargetID && result.FrameIDBefore != "" && result.FrameIDBefore == result.FrameIDAfter && result.FrameIDAfter == clicked.FrameID && result.UnrelatedTargetID != "" && result.UnrelatedTargetID != result.TargetID && result.UnrelatedCloseMatch && result.ActionVerified
	if !result.Success {
		result.Error = fmt.Sprintf("identity contract failed: task_target=%q click_target=%q frame_before=%q frame_after=%q click_frame=%q unrelated_target=%q close_match=%t action_verified=%t", result.TargetID, clicked.TargetID, result.FrameIDBefore, result.FrameIDAfter, clicked.FrameID, result.UnrelatedTargetID, result.UnrelatedCloseMatch, result.ActionVerified)
		return result, fmt.Errorf("frame scope scenario: %s", result.Error)
	}
	return result, nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
