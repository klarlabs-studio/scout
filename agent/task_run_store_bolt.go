package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
	boltErrors "go.etcd.io/bbolt/errors"
)

const (
	taskRunBoltBucket     = "scout_task_runs_v1"
	sessionBoltBucket     = "scout_sessions_v1"
	sessionCleanupBucket  = "scout_session_cleanup_v1"
	MaxBoltTaskRunRecords = 8192
	MaxBoltSessionRecords = 8192
)

// BoltTaskRunStore is a durable, single-process-owner TaskRunStore backed by
// bbolt. Multiple goroutines and Session values may share one instance. The
// bbolt file lock prevents another process from opening the same database at
// the same time; use a server database through TaskRunStore for multi-process
// deployments.
type BoltTaskRunStore struct {
	db     *bolt.DB
	mu     sync.RWMutex
	closed bool
}

// OpenBoltTaskRunStore opens or creates a durable task-run database at path
// and restricts the database file to owner read/write permissions (0600).
// The returned store must be closed when its owning service shuts down.
func OpenBoltTaskRunStore(path string) (*BoltTaskRunStore, error) {
	if path == "" {
		return nil, fmt.Errorf("task run database path is empty")
	}
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open task run database: %w", err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("restrict task run database permissions: %w", err)
	}
	store := &BoltTaskRunStore{db: db}
	if err := db.Update(func(tx *bolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists([]byte(taskRunBoltBucket)); err != nil {
			return err
		}
		_, err := tx.CreateBucketIfNotExists([]byte(sessionBoltBucket))
		if err != nil {
			return err
		}
		_, err = tx.CreateBucketIfNotExists([]byte(sessionCleanupBucket))
		return err
	}); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize task run database: %w", err)
	}
	return store, nil
}

// CheckBoltTaskRunStore opens an existing task store read-only, validates the
// database and required buckets, then closes it. It never creates the file,
// changes permissions, or writes task data. busy is true when another process
// currently owns the database lock; this is not evidence that the owner is
// unhealthy.
func CheckBoltTaskRunStore(path string) (busy bool, err error) {
	if path == "" {
		return false, fmt.Errorf("task run database path is empty")
	}
	db, err := bolt.Open(path, 0600, &bolt.Options{ReadOnly: true, Timeout: 250 * time.Millisecond})
	if err != nil {
		return errors.Is(err, boltErrors.ErrTimeout), fmt.Errorf("open task run database read-only: %w", err)
	}
	defer func() {
		if closeErr := db.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close task run database read-only: %w", closeErr)
		}
	}()
	err = db.View(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{[]byte(taskRunBoltBucket), []byte(sessionBoltBucket), []byte(sessionCleanupBucket)} {
			if tx.Bucket(name) == nil {
				return fmt.Errorf("task run database is missing a required bucket")
			}
		}
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("inspect task run database: %w", err)
	}
	return false, nil
}

// SessionRecoveryRecord contains only identifiers and a policy digest needed
// to reattach to a still-running remote browser. It deliberately has no fields
// for CDP endpoints, credentials, cookies, or page content. OwnerScope should
// be an authenticated, non-secret scope key (prefer a keyed hash).
type SessionRecoveryRecord struct {
	SchemaVersion     int       `json:"schema_version"`
	Handle            string    `json:"handle"`
	OwnerScope        string    `json:"owner_scope"`
	SessionName       string    `json:"session_name"`
	BrowserInstanceID string    `json:"browser_instance_id"`
	BrowserContextID  string    `json:"browser_context_id,omitempty"`
	TargetID          string    `json:"target_id"`
	PolicyDigest      string    `json:"policy_digest"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
	ExpiresAt         time.Time `json:"expires_at"`
}

// SessionRecoveryStore persists owner-scoped handles without requiring a
// particular database. GetSessionRecoveryRecord should return found=false for
// expired or foreign records and enforce owner scope in the same lookup.
// ResumeSessionContext also validates the returned identity and expiry before
// attaching to a browser. Implementations used by clustered hosts should make
// each operation transactional.
type SessionRecoveryStore interface {
	SaveSessionRecoveryRecord(context.Context, SessionRecoveryRecord) error
	GetSessionRecoveryRecord(context.Context, string, string) (SessionRecoveryRecord, bool, error)
	DeleteSessionRecoveryRecord(context.Context, string, string) (bool, error)
	DeleteSessionRecoveryRecordsForSession(context.Context, string, string) (int, error)
	DeleteExpiredSessionRecoveryRecords(context.Context, time.Time) (int, error)
}

// ExpiredSessionRecoveryRecordStore exposes expired records before deletion so
// hosts can clean up browser resources referenced by those records. Records
// should be deleted only after resource cleanup succeeds, allowing retries.
type ExpiredSessionRecoveryRecordStore interface {
	ListExpiredSessionRecoveryRecords(context.Context, time.Time) ([]SessionRecoveryRecord, error)
}

// ExpiredSessionRecoveryClaimStore provides an exclusive, lease-based claim
// for cleanup across store clients. A claimed record remains expired and
// unavailable for resume; completing removes the record and lease atomically.
type ExpiredSessionRecoveryClaimStore interface {
	ClaimExpiredSessionRecoveryRecord(context.Context, time.Time, string, time.Duration) (SessionRecoveryRecord, bool, error)
	CompleteExpiredSessionRecoveryRecord(context.Context, string, string) (bool, error)
	ReleaseExpiredSessionRecoveryRecord(context.Context, string, string) error
}

// ExpiredSessionRecoveryBatchClaimStore lets one cleanup pass skip records it
// already attempted, even if a claim lease expires while the pass is running.
type ExpiredSessionRecoveryBatchClaimStore interface {
	ClaimExpiredSessionRecoveryRecordExcluding(context.Context, time.Time, string, time.Duration, []string) (SessionRecoveryRecord, bool, error)
}

// SessionRecoveryRecordLister retrieves an owner's durable records for one
// logical session so hosts can clean up browser contexts before revocation.
type SessionRecoveryRecordLister interface {
	ListSessionRecoveryRecords(context.Context, string, string) ([]SessionRecoveryRecord, error)
}

type sessionCleanupLease struct {
	Owner     string    `json:"owner"`
	ExpiresAt time.Time `json:"expires_at"`
}

// SaveSessionRecoveryRecord atomically creates an owner's recovery record.
// Handles are immutable: callers must mint a new handle to persist a changed
// session binding, even when the existing handle has the same owner.
func (s *BoltTaskRunStore) SaveSessionRecoveryRecord(ctx context.Context, record SessionRecoveryRecord) error {
	if err := validateSessionRecoveryRecord(record); err != nil {
		return err
	}
	return s.updateNamedBucket(ctx, sessionBoltBucket, func(bucket *bolt.Bucket) error {
		if existing := bucket.Get([]byte(record.Handle)); existing != nil {
			return fmt.Errorf("session recovery handle is unavailable")
		}
		if bucket.Stats().KeyN >= MaxBoltSessionRecords {
			return fmt.Errorf("durable session recovery capacity reached: limit %d", MaxBoltSessionRecords)
		}
		data, err := json.Marshal(record)
		if err != nil {
			return fmt.Errorf("encode session recovery record: %w", err)
		}
		return bucket.Put([]byte(record.Handle), data)
	})
}

// GetSessionRecoveryRecord returns found=false for an unknown handle or a
// handle owned by another scope, avoiding an ownership existence oracle.
func (s *BoltTaskRunStore) GetSessionRecoveryRecord(ctx context.Context, handle, ownerScope string) (SessionRecoveryRecord, bool, error) {
	var record SessionRecoveryRecord
	found := false
	err := s.viewNamedBucket(ctx, sessionBoltBucket, func(bucket *bolt.Bucket) error {
		value := bucket.Get([]byte(handle))
		if value == nil {
			return nil
		}
		if err := json.Unmarshal(value, &record); err != nil {
			// A corrupt record has no trustworthy owner field. Treat it as
			// unavailable so malformed handles do not become an existence oracle.
			record = SessionRecoveryRecord{}
			//nolint:nilerr // Corrupt and unknown handles deliberately share the unavailable result.
			return nil
		}
		if record.Handle != handle {
			record = SessionRecoveryRecord{}
			return nil
		}
		if record.OwnerScope != ownerScope || !time.Now().Before(record.ExpiresAt) {
			record = SessionRecoveryRecord{}
			return nil
		}
		if err := validateSessionRecoveryRecord(record); err != nil {
			return err
		}
		found = true
		return nil
	})
	return record, found, err
}

// DeleteSessionRecoveryRecord revokes a handle only for its authenticated
// owner. Unknown and foreign handles are indistinguishable.
func (s *BoltTaskRunStore) DeleteSessionRecoveryRecord(ctx context.Context, handle, ownerScope string) (bool, error) {
	deleted := false
	err := s.updateNamedBucket(ctx, sessionBoltBucket, func(bucket *bolt.Bucket) error {
		value := bucket.Get([]byte(handle))
		if value == nil {
			return nil
		}
		var record SessionRecoveryRecord
		if err := json.Unmarshal(value, &record); err != nil {
			// Without a decodable owner, revoke must remain an indistinguishable
			// no-op rather than exposing corruption to an arbitrary caller.
			//nolint:nilerr // Corrupt and unknown handles deliberately share the no-op result.
			return nil
		}
		if record.Handle != handle || record.OwnerScope != ownerScope {
			return nil
		}
		if err := bucket.Delete([]byte(handle)); err != nil {
			return err
		}
		if leases := bucket.Tx().Bucket([]byte(sessionCleanupBucket)); leases != nil {
			if err := leases.Delete([]byte(handle)); err != nil {
				return err
			}
		}
		deleted = true
		return nil
	})
	return deleted, err
}

// DeleteSessionRecoveryRecordsForSession revokes every recovery handle owned
// by ownerScope for sessionName. This lets an explicit session close revoke
// handles created before a transport reconnect, when the process-local handle
// index no longer knows which handles were attached to the live session.
func (s *BoltTaskRunStore) DeleteSessionRecoveryRecordsForSession(ctx context.Context, ownerScope, sessionName string) (int, error) {
	if ownerScope == "" || sessionName == "" {
		return 0, errors.New("session recovery owner and name are required")
	}
	deleted := 0
	err := s.updateNamedBucket(ctx, sessionBoltBucket, func(bucket *bolt.Bucket) error {
		cursor := bucket.Cursor()
		for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
			if err := ctx.Err(); err != nil {
				return err
			}
			var record SessionRecoveryRecord
			if err := json.Unmarshal(value, &record); err != nil {
				return fmt.Errorf("decode session recovery record %q: %w", key, err)
			}
			if record.Handle != string(key) {
				return fmt.Errorf("session recovery record %q has a mismatched handle", key)
			}
			if err := validateSessionRecoveryRecord(record); err != nil {
				return fmt.Errorf("validate session recovery record %q: %w", key, err)
			}
			if record.OwnerScope != ownerScope || record.SessionName != sessionName {
				continue
			}
			if err := cursor.Delete(); err != nil {
				return err
			}
			if leases := bucket.Tx().Bucket([]byte(sessionCleanupBucket)); leases != nil {
				if err := leases.Delete(key); err != nil {
					return err
				}
			}
			deleted++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return deleted, nil
}

// ListSessionRecoveryRecords returns validated recovery records for one owner
// and logical session without revealing other owners' records.
func (s *BoltTaskRunStore) ListSessionRecoveryRecords(ctx context.Context, ownerScope, sessionName string) ([]SessionRecoveryRecord, error) {
	if ownerScope == "" || sessionName == "" {
		return nil, errors.New("session recovery owner and name are required")
	}
	var records []SessionRecoveryRecord
	err := s.viewNamedBucket(ctx, sessionBoltBucket, func(bucket *bolt.Bucket) error {
		cursor := bucket.Cursor()
		for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
			if err := ctx.Err(); err != nil {
				return err
			}
			var record SessionRecoveryRecord
			if err := json.Unmarshal(value, &record); err != nil {
				return fmt.Errorf("decode session recovery record %q: %w", key, err)
			}
			if record.Handle != string(key) {
				return fmt.Errorf("session recovery record %q has a mismatched handle", key)
			}
			if record.OwnerScope != ownerScope || record.SessionName != sessionName {
				continue
			}
			if err := validateSessionRecoveryRecord(record); err != nil {
				return fmt.Errorf("validate session recovery record %q: %w", key, err)
			}
			records = append(records, record)
		}
		return nil
	})
	return records, err
}

// DeleteExpiredSessionRecoveryRecords reclaims expired handles in one write
// transaction and returns the number removed.
func (s *BoltTaskRunStore) DeleteExpiredSessionRecoveryRecords(ctx context.Context, now time.Time) (int, error) {
	deleted := 0
	err := s.updateNamedBucket(ctx, sessionBoltBucket, func(bucket *bolt.Bucket) error {
		cursor := bucket.Cursor()
		for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
			if err := ctx.Err(); err != nil {
				return err
			}
			var record SessionRecoveryRecord
			if err := json.Unmarshal(value, &record); err != nil {
				return fmt.Errorf("decode session recovery record %q: %w", key, err)
			}
			if record.Handle != string(key) {
				return fmt.Errorf("session recovery record %q has a mismatched handle", key)
			}
			if now.Before(record.ExpiresAt) {
				continue
			}
			if err := validateSessionRecoveryRecord(record); err != nil {
				return fmt.Errorf("validate expired session recovery record %q: %w", key, err)
			}
			if err := cursor.Delete(); err != nil {
				return err
			}
			if leases := bucket.Tx().Bucket([]byte(sessionCleanupBucket)); leases != nil {
				if err := leases.Delete(key); err != nil {
					return err
				}
			}
			deleted++
		}
		return nil
	})
	return deleted, err
}

// ListExpiredSessionRecoveryRecords returns expired records without deleting
// them, enabling retryable cleanup of their referenced remote contexts.
func (s *BoltTaskRunStore) ListExpiredSessionRecoveryRecords(ctx context.Context, now time.Time) ([]SessionRecoveryRecord, error) {
	var records []SessionRecoveryRecord
	err := s.viewNamedBucket(ctx, sessionBoltBucket, func(bucket *bolt.Bucket) error {
		cursor := bucket.Cursor()
		for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
			if err := ctx.Err(); err != nil {
				return err
			}
			var record SessionRecoveryRecord
			if err := json.Unmarshal(value, &record); err != nil {
				return fmt.Errorf("decode session recovery record %q: %w", key, err)
			}
			if record.Handle != string(key) {
				return fmt.Errorf("session recovery record %q has a mismatched handle", key)
			}
			if !now.Before(record.ExpiresAt) {
				if err := validateSessionRecoveryRecord(record); err != nil {
					return fmt.Errorf("validate expired session recovery record %q: %w", key, err)
				}
				records = append(records, record)
			}
		}
		return nil
	})
	return records, err
}

// ClaimExpiredSessionRecoveryRecord atomically leases one expired record so
// only one cleanup worker at a time can dispose its browser context.
func (s *BoltTaskRunStore) ClaimExpiredSessionRecoveryRecord(ctx context.Context, now time.Time, claimant string, lease time.Duration) (SessionRecoveryRecord, bool, error) {
	return s.ClaimExpiredSessionRecoveryRecordExcluding(ctx, now, claimant, lease, nil)
}

// ClaimExpiredSessionRecoveryRecordExcluding atomically leases the next
// expired record not already attempted in this cleanup pass.
func (s *BoltTaskRunStore) ClaimExpiredSessionRecoveryRecordExcluding(ctx context.Context, now time.Time, claimant string, lease time.Duration, excludedHandles []string) (SessionRecoveryRecord, bool, error) {
	if claimant == "" || lease <= 0 {
		return SessionRecoveryRecord{}, false, errors.New("cleanup claimant and positive lease are required")
	}
	excluded := make(map[string]struct{}, len(excludedHandles))
	for _, handle := range excludedHandles {
		excluded[handle] = struct{}{}
	}
	var claimed SessionRecoveryRecord
	err := s.updateNamedBucket(ctx, sessionBoltBucket, func(bucket *bolt.Bucket) error {
		leases := bucket.Tx().Bucket([]byte(sessionCleanupBucket))
		if leases == nil {
			return errors.New("session cleanup lease bucket is unavailable")
		}
		cursor := bucket.Cursor()
		for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
			if err := ctx.Err(); err != nil {
				return err
			}
			if _, skip := excluded[string(key)]; skip {
				continue
			}
			var record SessionRecoveryRecord
			if err := json.Unmarshal(value, &record); err != nil {
				return fmt.Errorf("decode session recovery record %q: %w", key, err)
			}
			if record.Handle != string(key) {
				return fmt.Errorf("session recovery record %q has a mismatched handle", key)
			}
			if now.Before(record.ExpiresAt) {
				continue
			}
			if err := validateSessionRecoveryRecord(record); err != nil {
				return fmt.Errorf("validate expired session recovery record %q: %w", key, err)
			}
			leaseValue := leases.Get(key)
			if leaseValue != nil {
				var current sessionCleanupLease
				if err := json.Unmarshal(leaseValue, &current); err != nil {
					return fmt.Errorf("decode session cleanup lease %q: %w", key, err)
				}
				if now.Before(current.ExpiresAt) {
					continue
				}
			}
			data, err := json.Marshal(sessionCleanupLease{Owner: claimant, ExpiresAt: now.Add(lease).UTC()})
			if err != nil {
				return err
			}
			if err := leases.Put(key, data); err != nil {
				return err
			}
			claimed = record
			return nil
		}
		return nil
	})
	return claimed, claimed.Handle != "", err
}

// CompleteExpiredSessionRecoveryRecord removes the expired record and its
// cleanup lease only if claimant still holds the lease.
func (s *BoltTaskRunStore) CompleteExpiredSessionRecoveryRecord(ctx context.Context, handle, claimant string) (bool, error) {
	completed := false
	err := s.updateNamedBucket(ctx, sessionBoltBucket, func(bucket *bolt.Bucket) error {
		leases := bucket.Tx().Bucket([]byte(sessionCleanupBucket))
		if leases == nil {
			return errors.New("session cleanup lease bucket is unavailable")
		}
		value := leases.Get([]byte(handle))
		if value == nil {
			return nil
		}
		var current sessionCleanupLease
		if err := json.Unmarshal(value, &current); err != nil {
			return fmt.Errorf("decode session cleanup lease: %w", err)
		}
		if current.Owner != claimant {
			return nil
		}
		if err := bucket.Delete([]byte(handle)); err != nil {
			return err
		}
		if err := leases.Delete([]byte(handle)); err != nil {
			return err
		}
		completed = true
		return nil
	})
	return completed, err
}

// ReleaseExpiredSessionRecoveryRecord releases a failed cleanup attempt only
// when the caller still owns the lease.
func (s *BoltTaskRunStore) ReleaseExpiredSessionRecoveryRecord(ctx context.Context, handle, claimant string) error {
	return s.updateNamedBucket(ctx, sessionBoltBucket, func(bucket *bolt.Bucket) error {
		leases := bucket.Tx().Bucket([]byte(sessionCleanupBucket))
		if leases == nil {
			return errors.New("session cleanup lease bucket is unavailable")
		}
		value := leases.Get([]byte(handle))
		if value == nil {
			return nil
		}
		var current sessionCleanupLease
		if err := json.Unmarshal(value, &current); err != nil {
			return fmt.Errorf("decode session cleanup lease: %w", err)
		}
		if current.Owner != claimant {
			return nil
		}
		return leases.Delete([]byte(handle))
	})
}

func validateSessionRecoveryRecord(record SessionRecoveryRecord) error {
	if record.SchemaVersion != 1 || len(record.Handle) < 32 || record.OwnerScope == "" || record.SessionName == "" || record.BrowserInstanceID == "" || record.TargetID == "" || record.PolicyDigest == "" {
		return fmt.Errorf("session recovery record is missing required identity fields")
	}
	if record.CreatedAt.IsZero() || record.UpdatedAt.IsZero() || record.ExpiresAt.IsZero() || record.UpdatedAt.Before(record.CreatedAt) || !record.ExpiresAt.After(record.UpdatedAt) {
		return fmt.Errorf("session recovery record has invalid timestamps")
	}
	return nil
}

// Close closes the database. Calls are safe to repeat. The store should not be
// used after Close.
func (s *BoltTaskRunStore) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.db.Close()
}

// ReserveTaskRun atomically creates an in-flight record or returns the record
// already bound to the supplied ID.
func (s *BoltTaskRunStore) ReserveTaskRun(ctx context.Context, proposed TaskRunRecord) (TaskRunRecord, bool, error) {
	if err := validateTaskRunIdentity(proposed.ID, proposed.Fingerprint); err != nil {
		return TaskRunRecord{}, false, err
	}
	if !proposed.InFlight || proposed.Result != nil || proposed.Error != "" {
		return TaskRunRecord{}, false, fmt.Errorf("new task reservation must be in-flight and have no result")
	}
	var existing TaskRunRecord
	created := false
	err := s.update(ctx, func(bucket *bolt.Bucket) error {
		if value := bucket.Get([]byte(proposed.ID)); value != nil {
			if err := json.Unmarshal(value, &existing); err != nil {
				return fmt.Errorf("decode task run %q: %w", proposed.ID, err)
			}
			if existing.ID != proposed.ID || existing.Fingerprint != proposed.Fingerprint {
				return &TaskRunConflictError{TaskID: proposed.ID, Reason: "the ID is bound to a different plan"}
			}
			return nil
		}
		if bucket.Stats().KeyN >= MaxBoltTaskRunRecords {
			return &TaskRunStoreCapacityError{Limit: MaxBoltTaskRunRecords}
		}
		data, err := json.Marshal(proposed)
		if err != nil {
			return fmt.Errorf("encode task reservation: %w", err)
		}
		if err := bucket.Put([]byte(proposed.ID), data); err != nil {
			return err
		}
		existing = proposed
		created = true
		return nil
	})
	if err != nil {
		return TaskRunRecord{}, false, err
	}
	return existing, created, nil
}

// BindTaskRunTarget durably attaches a task reservation to the browser and
// target that will receive its actions.
func (s *BoltTaskRunStore) BindTaskRunTarget(ctx context.Context, id, fingerprint, targetID, browserInstanceID string) error {
	if targetID == "" || browserInstanceID == "" {
		return fmt.Errorf("task target binding requires target and browser instance IDs")
	}
	return s.update(ctx, func(bucket *bolt.Bucket) error {
		record, err := readTaskRunRecord(bucket, id, fingerprint)
		if err != nil {
			return err
		}
		if !record.InFlight {
			return &TaskRunConflictError{TaskID: id, Reason: "task reservation is no longer active"}
		}
		if record.TargetID != "" && (record.TargetID != targetID || record.BrowserInstanceID != browserInstanceID) {
			return &TaskRunConflictError{TaskID: id, Reason: "task reservation is already bound to another browser target"}
		}
		record.TargetID = targetID
		record.BrowserInstanceID = browserInstanceID
		return putTaskRunRecord(bucket, record)
	})
}

// BindTaskRunHandoff atomically persists target/browser identity, sanitized
// plan, and an initial not-yet-dispatched cursor before actions begin.
func (s *BoltTaskRunStore) BindTaskRunHandoff(ctx context.Context, id, fingerprint, targetID, browserInstanceID string, plan TaskRunPlan, recovery TaskRunRecovery) error {
	if targetID == "" || browserInstanceID == "" {
		return fmt.Errorf("task handoff requires target and browser instance IDs")
	}
	if err := ValidateTaskRunPlan(plan); err != nil {
		return err
	}
	if recovery.SchemaVersion != 1 || len(recovery.Steps) != len(plan.Actions) || recovery.Phase != "actions" || recovery.Current != nil || recovery.NextAction != 0 {
		return fmt.Errorf("initial task recovery cursor is invalid")
	}
	return s.update(ctx, func(bucket *bolt.Bucket) error {
		record, err := readTaskRunRecord(bucket, id, fingerprint)
		if err != nil {
			return err
		}
		if !record.InFlight || record.Result != nil || record.Resolution != nil {
			return &TaskRunConflictError{TaskID: id, Reason: "task reservation is no longer active"}
		}
		if (record.TargetID != "" && record.TargetID != targetID) || (record.BrowserInstanceID != "" && record.BrowserInstanceID != browserInstanceID) {
			return &TaskRunConflictError{TaskID: id, Reason: "task reservation is already bound to another browser target"}
		}
		if plan.TargetID != targetID {
			return &TaskRunConflictError{TaskID: id, Reason: "task recovery plan target does not match reservation"}
		}
		record.TargetID = targetID
		record.BrowserInstanceID = browserInstanceID
		record.Plan = cloneTaskRunPlan(&plan)
		record.Recovery = cloneTaskRunRecovery(&recovery)
		return putTaskRunRecord(bucket, record)
	})
}

// UpdateTaskRunRecovery persists a task manifest or action boundary while the
// matching reservation remains in flight.
func (s *BoltTaskRunStore) UpdateTaskRunRecovery(ctx context.Context, id, fingerprint string, recovery TaskRunRecovery) error {
	if recovery.SchemaVersion != 1 || len(recovery.Steps) == 0 || recovery.NextAction < 0 || recovery.NextAction > len(recovery.Steps) || (recovery.Phase != "actions" && recovery.Phase != "final_verification" && recovery.Phase != "result_persisting") {
		return fmt.Errorf("task recovery state is invalid")
	}
	if recovery.Current != nil && (recovery.Current.Index < 0 || recovery.Current.Index >= len(recovery.Steps) || (recovery.Current.Status != "started" && recovery.Current.Status != "finished")) {
		return fmt.Errorf("task recovery cursor is invalid")
	}
	return s.update(ctx, func(bucket *bolt.Bucket) error {
		record, err := readTaskRunRecord(bucket, id, fingerprint)
		if err != nil {
			return err
		}
		if !record.InFlight || record.Result != nil {
			return &TaskRunConflictError{TaskID: id, Reason: "task reservation is no longer active"}
		}
		record.Recovery = cloneTaskRunRecovery(&recovery)
		return putTaskRunRecord(bucket, record)
	})
}

// UpdateTaskRunPlan persists the sanitized plan before browser dispatch so a
// recovery host can inspect the interrupted task without input values.
func (s *BoltTaskRunStore) UpdateTaskRunPlan(ctx context.Context, id, fingerprint string, plan TaskRunPlan) error {
	if plan.SchemaVersion != 1 || len(plan.Actions) == 0 {
		return fmt.Errorf("task recovery plan is invalid")
	}
	return s.update(ctx, func(bucket *bolt.Bucket) error {
		record, err := readTaskRunRecord(bucket, id, fingerprint)
		if err != nil {
			return err
		}
		if !record.InFlight || record.Result != nil || record.Resolution != nil {
			return &TaskRunConflictError{TaskID: id, Reason: "task reservation is no longer active"}
		}
		if record.TargetID != "" && plan.TargetID != record.TargetID {
			return &TaskRunConflictError{TaskID: id, Reason: "task recovery plan target does not match reservation"}
		}
		if err := ValidateTaskRunPlan(plan); err != nil {
			return err
		}
		record.Plan = cloneTaskRunPlan(&plan)
		return putTaskRunRecord(bucket, record)
	})
}

// CompleteTaskRun persists the terminal result for a matching, target-bound
// reservation. The result and task ID/fingerprint must agree.
func (s *BoltTaskRunStore) CompleteTaskRun(ctx context.Context, completed TaskRunRecord) error {
	if completed.InFlight || completed.Result == nil || completed.Result.ID != completed.ID || completed.Result.PlanID != completed.Fingerprint {
		return fmt.Errorf("completed task record has inconsistent identity or state")
	}
	if completed.TargetID == "" || completed.BrowserInstanceID == "" || completed.Result.TargetID != completed.TargetID || completed.Result.BrowserInstanceID != completed.BrowserInstanceID {
		return fmt.Errorf("completed task record has inconsistent target binding")
	}
	return s.update(ctx, func(bucket *bolt.Bucket) error {
		record, err := readTaskRunRecord(bucket, completed.ID, completed.Fingerprint)
		if err != nil {
			return err
		}
		if !record.InFlight {
			return &TaskRunConflictError{TaskID: completed.ID, Reason: "task reservation is no longer active"}
		}
		if record.TargetID != completed.TargetID || record.BrowserInstanceID != completed.BrowserInstanceID {
			return &TaskRunConflictError{TaskID: completed.ID, Reason: "task completion target does not match its reservation"}
		}
		if completed.Recovery == nil {
			completed.Recovery = cloneTaskRunRecovery(record.Recovery)
		}
		if completed.Plan == nil {
			completed.Plan = cloneTaskRunPlan(record.Plan)
		}
		return putTaskRunRecord(bucket, completed)
	})
}

// RetainUncertainTaskRun persists an observed result without completing the
// reservation. Same-ID execution remains blocked until host reconciliation.
func (s *BoltTaskRunStore) RetainUncertainTaskRun(ctx context.Context, id, fingerprint string, result TaskRunResult, failure string) error {
	if result.ID != id || result.PlanID != fingerprint || result.Status != "needs_reconciliation" || result.TargetID == "" || result.BrowserInstanceID == "" {
		return fmt.Errorf("uncertain task result has inconsistent identity or state")
	}
	return s.update(ctx, func(bucket *bolt.Bucket) error {
		record, err := readTaskRunRecord(bucket, id, fingerprint)
		if err != nil {
			return err
		}
		if !record.InFlight || record.Resolution != nil && record.Resolution.Status != "unresolved" {
			return &TaskRunConflictError{TaskID: id, Reason: "uncertain task reservation is no longer active"}
		}
		if record.TargetID != result.TargetID || record.BrowserInstanceID != result.BrowserInstanceID || record.Plan == nil {
			return &TaskRunConflictError{TaskID: id, Reason: "uncertain task result does not match its persisted handoff"}
		}
		record.Result = cloneTaskRunResult(&result)
		record.Error = failure
		return putTaskRunRecord(bucket, record)
	})
}

// ResolveTaskRun records a host-side reconciliation conclusion while keeping
// the plan fingerprint as a permanent replay guard for this task ID.
func (s *BoltTaskRunStore) ResolveTaskRun(ctx context.Context, id, fingerprint string, resolution TaskRunResolution) error {
	if err := validateTaskRunResolution(resolution); err != nil {
		return err
	}
	return s.update(ctx, func(bucket *bolt.Bucket) error {
		record, err := readTaskRunRecord(bucket, id, fingerprint)
		if err != nil {
			return err
		}
		if !record.InFlight || record.TargetID == "" || record.BrowserInstanceID == "" || record.Resolution != nil && record.Resolution.Status != "unresolved" {
			return &TaskRunConflictError{TaskID: id, Reason: "only a bound interrupted reservation can be resolved"}
		}
		record.InFlight = resolution.Status == "unresolved"
		record.Resolution = cloneTaskRunResolution(&resolution)
		record.ResolutionHistory = append(record.ResolutionHistory, resolution)
		record.Result = nil
		return putTaskRunRecord(bucket, record)
	})
}

// ReleaseTaskRun removes only an in-flight reservation. RunTaskContext calls
// it only when execution did not reach action dispatch.
func (s *BoltTaskRunStore) ReleaseTaskRun(ctx context.Context, id, fingerprint string) error {
	return s.update(ctx, func(bucket *bolt.Bucket) error {
		if bucket.Get([]byte(id)) == nil {
			return nil
		}
		record, err := readTaskRunRecord(bucket, id, fingerprint)
		if err != nil {
			return err
		}
		if record.InFlight {
			return bucket.Delete([]byte(id))
		}
		return nil
	})
}

// GetTaskRun returns a retained record so a host can inspect an interrupted
// task after restart. It does not change or release an in-flight reservation.
func (s *BoltTaskRunStore) GetTaskRun(ctx context.Context, id string) (TaskRunRecord, bool, error) {
	if id == "" {
		return TaskRunRecord{}, false, fmt.Errorf("task run ID is required")
	}
	var record TaskRunRecord
	found := false
	err := s.view(ctx, func(bucket *bolt.Bucket) error {
		value := bucket.Get([]byte(id))
		if value == nil {
			return nil
		}
		if err := json.Unmarshal(value, &record); err != nil {
			return fmt.Errorf("decode task run %q: %w", id, err)
		}
		if record.ID != id || record.Fingerprint == "" {
			return fmt.Errorf("task run %q has invalid stored identity", id)
		}
		found = true
		return nil
	})
	if err != nil {
		return TaskRunRecord{}, false, err
	}
	return record, found, nil
}

func (s *BoltTaskRunStore) update(ctx context.Context, apply func(*bolt.Bucket) error) error {
	return s.updateNamedBucket(ctx, taskRunBoltBucket, apply)
}

func (s *BoltTaskRunStore) updateNamedBucket(ctx context.Context, name string, apply func(*bolt.Bucket) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil {
		return fmt.Errorf("task run database is nil")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return boltErrors.ErrDatabaseNotOpen
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		bucket := tx.Bucket([]byte(name))
		if bucket == nil {
			return fmt.Errorf("database bucket %q is missing", name)
		}
		return apply(bucket)
	})
}

func (s *BoltTaskRunStore) view(ctx context.Context, inspect func(*bolt.Bucket) error) error {
	return s.viewNamedBucket(ctx, taskRunBoltBucket, inspect)
}

func (s *BoltTaskRunStore) viewNamedBucket(ctx context.Context, name string, inspect func(*bolt.Bucket) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil {
		return fmt.Errorf("task run database is nil")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return boltErrors.ErrDatabaseNotOpen
	}
	return s.db.View(func(tx *bolt.Tx) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		bucket := tx.Bucket([]byte(name))
		if bucket == nil {
			return fmt.Errorf("database bucket %q is missing", name)
		}
		return inspect(bucket)
	})
}

func readTaskRunRecord(bucket *bolt.Bucket, id, fingerprint string) (TaskRunRecord, error) {
	if err := validateTaskRunIdentity(id, fingerprint); err != nil {
		return TaskRunRecord{}, err
	}
	value := bucket.Get([]byte(id))
	if value == nil {
		return TaskRunRecord{}, &TaskRunConflictError{TaskID: id, Reason: "task reservation does not exist"}
	}
	var record TaskRunRecord
	if err := json.Unmarshal(value, &record); err != nil {
		return TaskRunRecord{}, fmt.Errorf("decode task run %q: %w", id, err)
	}
	if record.ID != id || record.Fingerprint != fingerprint {
		return TaskRunRecord{}, &TaskRunConflictError{TaskID: id, Reason: "the ID is bound to a different plan"}
	}
	return record, nil
}

func putTaskRunRecord(bucket *bolt.Bucket, record TaskRunRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode task run record: %w", err)
	}
	return bucket.Put([]byte(record.ID), data)
}

func validateTaskRunIdentity(id, fingerprint string) error {
	if id == "" || fingerprint == "" {
		return fmt.Errorf("task run ID and fingerprint are required")
	}
	return nil
}

// TaskRunStoreCapacityError indicates that the durable store retained its
// maximum number of IDs and cannot safely evict a record that may be retried.
type TaskRunStoreCapacityError struct {
	Limit int
}

func (e *TaskRunStoreCapacityError) Error() string {
	return fmt.Sprintf("durable task run store capacity reached: limit %d", e.Limit)
}

var _ TaskRunStore = (*BoltTaskRunStore)(nil)
var _ SessionRecoveryStore = (*BoltTaskRunStore)(nil)
