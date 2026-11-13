package cdp

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// CreateTarget creates a new browser tab/page and returns its target ID.
func (c *Conn) CreateTarget(url string) (string, error) {
	return c.CreateTargetCtx(context.Background(), url)
}

// CreateTargetCtx creates a browser target using the caller's deadline.
func (c *Conn) CreateTargetCtx(ctx context.Context, url string) (string, error) {
	return c.CreateTargetInContextCtx(ctx, url, "")
}

func (c *Conn) CreateTargetInContextCtx(ctx context.Context, url, browserContextID string) (string, error) {
	params := map[string]string{"url": url}
	if browserContextID != "" {
		params["browserContextId"] = browserContextID
	}
	result, err := c.CallSessionCtx(ctx, "", "Target.createTarget", params)
	if err != nil {
		return "", err
	}

	var resp struct {
		TargetID string `json:"targetId"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return "", err
	}
	return resp.TargetID, nil
}

func (c *Conn) CreateBrowserContext(ctx context.Context) (string, error) {
	result, err := c.CallSessionCtx(ctx, "", "Target.createBrowserContext", map[string]any{"disposeOnDetach": false})
	if err != nil {
		return "", err
	}
	var resp struct {
		BrowserContextID string `json:"browserContextId"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return "", err
	}
	if resp.BrowserContextID == "" {
		return "", errors.New("Target.createBrowserContext returned an empty browserContextId")
	}
	return resp.BrowserContextID, nil
}

func (c *Conn) DisposeBrowserContext(ctx context.Context, browserContextID string) error {
	_, err := c.CallSessionCtx(ctx, "", "Target.disposeBrowserContext", map[string]string{"browserContextId": browserContextID})
	return err
}

// ActivateTargetContext asks the browser process to activate a page target.
// This browser-level command can wake a discarded renderer without sending a
// renderer-bound command to the sleeping page session.
func (c *Conn) ActivateTargetContext(ctx context.Context, targetID string) error {
	_, err := c.CallSessionCtx(ctx, "", "Target.activateTarget", map[string]string{"targetId": targetID})
	return err
}

func (c *Conn) BrowserContextExists(ctx context.Context, browserContextID string) (bool, error) {
	result, err := c.CallSessionCtx(ctx, "", "Target.getBrowserContexts", map[string]any{})
	if err != nil {
		return false, err
	}
	var response struct {
		BrowserContextIDs []string `json:"browserContextIds"`
	}
	if err := json.Unmarshal(result, &response); err != nil {
		return false, err
	}
	for _, id := range response.BrowserContextIDs {
		if id == browserContextID {
			return true, nil
		}
	}
	return false, nil
}

// AttachBrowserContext makes an existing remote BrowserContext owned by this
// CDP connection, so it will be disposed explicitly on Close rather than on
// detach. Use only for a context identity loaded from trusted recovery state.
func (c *Conn) AttachBrowserContext(ctx context.Context, browserContextID string) error {
	if browserContextID == "" {
		return errors.New("browser context ID is required")
	}
	targets, err := c.GetTargetsCtx(ctx)
	if err != nil {
		return err
	}
	for _, target := range targets {
		if target.BrowserContextID == browserContextID {
			return nil
		}
	}
	return errors.New("browser context is unavailable")
}

// AttachToTarget attaches to a target and returns the session ID.
func (c *Conn) AttachToTarget(targetID string) (string, error) {
	return c.AttachToTargetCtx(context.Background(), targetID)
}

// AttachToTargetCtx attaches to a target using the caller's deadline.
func (c *Conn) AttachToTargetCtx(ctx context.Context, targetID string) (string, error) {
	params := map[string]any{
		"targetId": targetID,
		"flatten":  true,
	}
	result, err := c.CallSessionCtx(ctx, "", "Target.attachToTarget", params)
	if err != nil {
		return "", err
	}

	var resp struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return "", err
	}
	return resp.SessionID, nil
}

// AttachToTargetLateResultCtx returns a session ID for a prompt attach or a
// late response channel when ctx expires after dispatch.
func (c *Conn) AttachToTargetLateResultCtx(ctx context.Context, targetID string, cleanupWindow time.Duration) (string, <-chan *Message, error) {
	params := map[string]any{"targetId": targetID, "flatten": true}
	result, late, err := c.CallSessionLateResultCtx(ctx, "", "Target.attachToTarget", params, cleanupWindow)
	if err != nil {
		return "", late, err
	}
	var resp struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return "", nil, err
	}
	return resp.SessionID, nil, nil
}

// DetachFromTargetCtx detaches this CDP client from a target session without
// closing the underlying browser target.
func (c *Conn) DetachFromTargetCtx(ctx context.Context, sessionID string) error {
	_, err := c.CallSessionCtx(ctx, "", "Target.detachFromTarget", map[string]string{"sessionId": sessionID})
	return err
}

// GetTargets returns the list of available targets (pages/tabs).
func (c *Conn) GetTargets() ([]TargetInfo, error) {
	return c.GetTargetsCtx(context.Background())
}

// GetTargetsCtx lists browser targets using the caller's deadline.
func (c *Conn) GetTargetsCtx(ctx context.Context) ([]TargetInfo, error) {
	result, err := c.CallSessionCtx(ctx, "", "Target.getTargets", nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		TargetInfos []TargetInfo `json:"targetInfos"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return nil, err
	}
	return resp.TargetInfos, nil
}

// TargetInfo describes a CDP target.
type TargetInfo struct {
	TargetID         string `json:"targetId"`
	Type             string `json:"type"`
	Title            string `json:"title"`
	URL              string `json:"url"`
	BrowserContextID string `json:"browserContextId"`
	ParentFrameID    string `json:"parentFrameId,omitempty"`
	ParentID         string `json:"parentId,omitempty"`
}

// CloseTarget closes a browser tab/page.
func (c *Conn) CloseTarget(targetID string) error {
	return c.CloseTargetCtx(context.Background(), targetID)
}

// CloseTargetCtx closes a browser target using the caller's deadline.
func (c *Conn) CloseTargetCtx(ctx context.Context, targetID string) error {
	params := map[string]string{"targetId": targetID}
	_, err := c.CallSessionCtx(ctx, "", "Target.closeTarget", params)
	return err
}
