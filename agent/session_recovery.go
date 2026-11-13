package agent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	browse "go.klarlabs.de/scout"
)

// ErrSessionRecoveryUnavailable is returned for unknown, foreign, expired, or
// policy-mismatched handles so callers cannot probe another owner's registry.
var ErrSessionRecoveryUnavailable = errors.New("session recovery handle is unavailable")

// NewSessionRecoveryHandle creates a server-side opaque 192-bit handle. The
// handle identifies a record but is not authorization; callers must supply an
// authenticated owner scope when saving, resuming, or revoking it.
func NewSessionRecoveryHandle() (string, error) {
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate session recovery handle: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

// SessionRecoveryPolicyDigest hashes the trusted policy used to resume a
// remote session. The endpoint itself is never stored in the recovery record.
func SessionRecoveryPolicyDigest(cfg SessionConfig) (string, error) {
	if cfg.RemoteCDP == "" || cfg.ProxyServer != "" {
		return "", errors.New("session recovery requires a remote CDP endpoint and no Scout-managed proxy")
	}
	origins := append([]string(nil), cfg.AllowedOrigins...)
	sort.Strings(origins)
	endpointDigest := sha256.Sum256([]byte(cfg.RemoteCDP))
	policy := struct {
		EndpointDigest string   `json:"endpoint_digest"`
		Origins        []string `json:"allowed_origins"`
		AllowPrivate   bool     `json:"allow_private_ips"`
		Isolated       bool     `json:"isolated_browser_context"`
	}{EndpointDigest: hex.EncodeToString(endpointDigest[:]), Origins: origins, AllowPrivate: cfg.AllowPrivateIPs, Isolated: cfg.IsolateRemoteBrowserContext || cfg.RemoteBrowserContextID != ""}
	encoded, err := json.Marshal(policy)
	if err != nil {
		return "", fmt.Errorf("encode session recovery policy: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// PersistRecoveryRecord binds a new opaque handle to this live session's
// browser, context, and exact target. Only remote sessions without a
// Scout-managed proxy can be resumed after process restart.
func (s *Session) PersistRecoveryRecord(ctx context.Context, store SessionRecoveryStore, handle, ownerScope, sessionName, policyDigest string, expiresAt time.Time) (SessionRecoveryRecord, error) {
	if store == nil {
		return SessionRecoveryRecord{}, errors.New("session recovery store is required")
	}
	if len(handle) < 32 || ownerScope == "" || sessionName == "" || policyDigest == "" {
		return SessionRecoveryRecord{}, errors.New("session recovery identity is incomplete")
	}
	if err := s.mu.LockContext(ctx); err != nil {
		return SessionRecoveryRecord{}, err
	}
	defer s.mu.Unlock()
	if s.closed || s.browserOwnership != "remote_cdp" || s.page == nil {
		return SessionRecoveryRecord{}, errors.New("only a live remote browser target can be persisted")
	}
	instanceID := s.browserInstanceID()
	targetID := s.page.TargetID()
	contextID := ""
	if identified, ok := s.browser.(interface{ RemoteBrowserContextID() string }); ok {
		contextID = identified.RemoteBrowserContextID()
	}
	if instanceID == "" || targetID == "" {
		return SessionRecoveryRecord{}, errors.New("live browser and target identity are required")
	}
	actualPolicyDigest, err := SessionRecoveryPolicyDigest(SessionConfig{
		RemoteCDP: s.remoteBrowserURL, AllowedOrigins: s.allowedOrigins, AllowPrivateIPs: s.allowPrivateIPs,
		RemoteBrowserContextID: contextID,
	})
	if err != nil || actualPolicyDigest != policyDigest {
		return SessionRecoveryRecord{}, errors.New("session recovery policy does not match the live session")
	}
	now := time.Now().UTC()
	record := SessionRecoveryRecord{
		SchemaVersion: 1, Handle: handle, OwnerScope: ownerScope, SessionName: sessionName,
		BrowserInstanceID: instanceID, BrowserContextID: contextID, TargetID: targetID, PolicyDigest: policyDigest,
		CreatedAt: now, UpdatedAt: now, ExpiresAt: expiresAt.UTC(),
	}
	if err := validateSessionRecoveryRecord(record); err != nil {
		return SessionRecoveryRecord{}, err
	}
	if err := store.SaveSessionRecoveryRecord(ctx, record); err != nil {
		return SessionRecoveryRecord{}, err
	}
	if contextID != "" {
		preserver, ok := s.browser.(interface{ PreserveRemoteBrowserContext(string) error })
		if !ok {
			cause := errors.New("browser cannot transfer isolated context ownership to recovery store")
			return SessionRecoveryRecord{}, rollbackPersistedRecoveryRecord(ctx, store, handle, ownerScope, cause)
		}
		if err := preserver.PreserveRemoteBrowserContext(contextID); err != nil {
			return SessionRecoveryRecord{}, rollbackPersistedRecoveryRecord(ctx, store, handle, ownerScope, err)
		}
	}
	return record, nil
}

func rollbackPersistedRecoveryRecord(ctx context.Context, store SessionRecoveryStore, handle, ownerScope string, cause error) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	deleted, err := store.DeleteSessionRecoveryRecord(cleanupCtx, handle, ownerScope)
	if err != nil {
		return errors.Join(cause, fmt.Errorf("remove recovery record after setup failure: %w", err))
	}
	if !deleted {
		return errors.Join(cause, errors.New("recovery record was not removed after setup failure"))
	}
	return cause
}

// ResumeSessionContext reattaches to the exact live target named by an
// owner-scoped handle. It never launches a replacement browser. Missing,
// expired, foreign, or policy-mismatched handles return the same error.
func ResumeSessionContext(ctx context.Context, cfg SessionConfig, store SessionRecoveryStore, handle, ownerScope, expectedPolicyDigest string) (*Session, SessionRecoveryRecord, error) {
	if store == nil || ownerScope == "" || expectedPolicyDigest == "" {
		return nil, SessionRecoveryRecord{}, ErrSessionRecoveryUnavailable
	}
	currentDigest, err := SessionRecoveryPolicyDigest(cfg)
	if err != nil || currentDigest != expectedPolicyDigest {
		return nil, SessionRecoveryRecord{}, ErrSessionRecoveryUnavailable
	}
	record, found, err := store.GetSessionRecoveryRecord(ctx, handle, ownerScope)
	if err != nil {
		return nil, SessionRecoveryRecord{}, err
	}
	if !found || record.Handle != handle || record.OwnerScope != ownerScope ||
		validateSessionRecoveryRecord(record) != nil || !time.Now().Before(record.ExpiresAt) ||
		record.PolicyDigest != expectedPolicyDigest {
		return nil, SessionRecoveryRecord{}, ErrSessionRecoveryUnavailable
	}
	cfg.IsolateRemoteBrowserContext = false
	cfg.RemoteBrowserContextID = record.BrowserContextID
	cfg.TargetID = record.TargetID
	session, err := NewSessionContext(ctx, cfg)
	if err != nil {
		return nil, SessionRecoveryRecord{}, ErrSessionRecoveryUnavailable
	}
	if session.browserInstanceID() != record.BrowserInstanceID {
		_ = session.Close()
		return nil, SessionRecoveryRecord{}, ErrSessionRecoveryUnavailable
	}
	if record.BrowserContextID != "" {
		provider, ok := session.browser.(interface{ RemoteBrowserContextID() string })
		if !ok || provider.RemoteBrowserContextID() != record.BrowserContextID {
			_ = session.Close()
			return nil, SessionRecoveryRecord{}, ErrSessionRecoveryUnavailable
		}
	}
	status, err := session.StatusContext(ctx)
	if err != nil || status == nil || status.ActiveTargetID != record.TargetID {
		_ = session.Close()
		return nil, SessionRecoveryRecord{}, ErrSessionRecoveryUnavailable
	}
	return session, record, nil
}

// RevokeSessionRecoveryHandle revokes the handle for its authenticated owner.
// A foreign or unknown handle is an indistinguishable no-op.
func RevokeSessionRecoveryHandle(ctx context.Context, store SessionRecoveryStore, handle, ownerScope string) (bool, error) {
	if store == nil || ownerScope == "" {
		return false, errors.New("session recovery store and owner scope are required")
	}
	return store.DeleteSessionRecoveryRecord(ctx, handle, ownerScope)
}

// RevokeSessionRecoveryHandleWithConfig revokes an owner's handle and, when it
// refers to an isolated remote BrowserContext, disposes that context through
// the host's current trusted endpoint. The endpoint is never read from the
// persisted record. Shared-browser contexts are left untouched.
func RevokeSessionRecoveryHandleWithConfig(ctx context.Context, cfg SessionConfig, store SessionRecoveryStore, handle, ownerScope string) (bool, error) {
	if store == nil || ownerScope == "" {
		return false, errors.New("session recovery store and owner scope are required")
	}
	record, found, err := store.GetSessionRecoveryRecord(ctx, handle, ownerScope)
	if err != nil || !found {
		return false, err
	}
	if record.Handle != handle || record.OwnerScope != ownerScope {
		return false, nil
	}
	if err := validateSessionRecoveryRecord(record); err != nil {
		return false, fmt.Errorf("invalid session recovery record: %w", err)
	}
	if record.BrowserContextID != "" {
		if err := DisposeSessionRecoveryContext(ctx, cfg, record); err != nil {
			return false, fmt.Errorf("recovery handle remains valid but browser context disposal failed: %w", err)
		}
	}
	return store.DeleteSessionRecoveryRecord(ctx, handle, ownerScope)
}

// DisposeSessionRecoveryContext disposes the isolated browser context
// referenced by a recovery record. The endpoint must come from the
// host's current trusted configuration; it is never read from the record.
func DisposeSessionRecoveryContext(ctx context.Context, cfg SessionConfig, record SessionRecoveryRecord) error {
	if err := validateSessionRecoveryRecord(record); err != nil {
		return err
	}
	if record.BrowserContextID == "" {
		return nil
	}
	if cfg.RemoteCDP == "" {
		return errors.New("trusted remote CDP endpoint is unavailable for expired context disposal")
	}
	engine := browse.New(browse.WithRemoteCDP(cfg.RemoteCDP))
	if err := engine.LaunchContext(ctx); err != nil {
		return fmt.Errorf("connect to dispose expired browser context: %w", err)
	}
	defer func() { _ = engine.Close() }()
	if engine.BrowserInstanceID() != record.BrowserInstanceID {
		return errors.New("trusted endpoint browser identity does not match expired recovery record")
	}
	exists, err := engine.RemoteBrowserContextExists(ctx, record.BrowserContextID)
	if err != nil {
		return fmt.Errorf("inspect browser contexts before disposal: %w", err)
	}
	if !exists {
		return nil
	}
	if err := engine.DisposeRemoteBrowserContext(ctx, record.BrowserContextID); err != nil {
		// Concurrent cleanup or a lost CDP response may have already disposed
		// the context. Confirm absence before treating the operation as complete.
		stillExists, inspectErr := engine.RemoteBrowserContextExists(ctx, record.BrowserContextID)
		if inspectErr == nil && !stillExists {
			return nil
		}
		return fmt.Errorf("dispose recovery browser context: %w", err)
	}
	return nil
}

// RevokeSessionRecoveryRecordsForSessionWithConfig disposes each distinct
// isolated context and then removes all recovery handles for an owner/session.
// Failed context disposal leaves its handles intact so the host can retry.
func RevokeSessionRecoveryRecordsForSessionWithConfig(ctx context.Context, cfg SessionConfig, store SessionRecoveryStore, ownerScope, sessionName string) (int, error) {
	if store == nil || ownerScope == "" || sessionName == "" {
		return 0, errors.New("session recovery store, owner scope, and session name are required")
	}
	lister, ok := store.(SessionRecoveryRecordLister)
	if !ok {
		return 0, errors.New("session recovery store cannot list records for resource cleanup")
	}
	records, err := lister.ListSessionRecoveryRecords(ctx, ownerScope, sessionName)
	if err != nil {
		return 0, err
	}
	for _, record := range records {
		if record.OwnerScope != ownerScope || record.SessionName != sessionName {
			return 0, errors.New("session recovery store returned a record outside the requested owner and session")
		}
		if err := validateSessionRecoveryRecord(record); err != nil {
			return 0, fmt.Errorf("session recovery store returned an invalid record: %w", err)
		}
	}
	seenContexts := make(map[string]bool)
	failedContexts := make(map[string]bool)
	var cleanupErr error
	for _, record := range records {
		if record.BrowserContextID == "" || seenContexts[record.BrowserContextID] {
			continue
		}
		seenContexts[record.BrowserContextID] = true
		if err := DisposeSessionRecoveryContext(ctx, cfg, record); err != nil {
			failedContexts[record.BrowserContextID] = true
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("dispose recovery context for session %q: %w", sessionName, err))
		}
	}
	deleted, revokeErr := revokeRecoveryRecords(ctx, store, records, failedContexts)
	return deleted, errors.Join(cleanupErr, revokeErr)
}

func revokeRecoveryRecords(ctx context.Context, store SessionRecoveryStore, records []SessionRecoveryRecord, failedContexts map[string]bool) (int, error) {
	deleted := 0
	var resultErr error
	for _, record := range records {
		if record.BrowserContextID != "" && failedContexts[record.BrowserContextID] {
			continue
		}
		removed, err := store.DeleteSessionRecoveryRecord(ctx, record.Handle, record.OwnerScope)
		if err != nil {
			resultErr = errors.Join(resultErr, err)
			continue
		}
		if removed {
			deleted++
		}
	}
	return deleted, resultErr
}
