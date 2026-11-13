package agent

import (
	"bytes"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const RecoveryHandoffSchema = "scout.recovery-handoff/v1"
const RecoveryHandoffObservationSchema = "scout.recovery-observation/v1"
const maxRecoveryHandoffTTL = 15 * time.Minute
const maxRecoveryHandoffObservationTTL = 2 * time.Minute

// RecoveryHandoffEnvelope binds a portable action receipt to host-authenticated
// principal and browser-generation claims. It is metadata for safe inspection,
// not permission to dispatch or replay browser actions.
type RecoveryHandoffEnvelope struct {
	Schema            string          `json:"schema"`
	Receipt           RecoveryReceipt `json:"receipt"`
	OwnerScope        string          `json:"owner_scope"`
	BrowserGeneration string          `json:"browser_generation"`
	KeyID             string          `json:"key_id"`
	IssuedAt          string          `json:"issued_at"`
	ExpiresAt         string          `json:"expires_at"`
	Signature         string          `json:"signature"`
}

// RecoveryHandoffObservedBinding is the receiving host's trusted view of the
// principal, browser generation, target, frame, and effective Scout policy.
// BrowserGeneration must come from the browser owner/broker, not the receipt.
type RecoveryHandoffObservedBinding struct {
	OwnerScope        string `json:"owner_scope"`
	BrowserGeneration string `json:"browser_generation"`
	TargetID          string `json:"target_id"`
	FrameID           string `json:"frame_id,omitempty"`
	// FrameBindingKnown distinguishes a confirmed top-level frame (empty
	// FrameID) from an adapter that did not observe frame identity.
	FrameBindingKnown bool   `json:"frame_binding_known"`
	PolicyID          string `json:"policy_id"`
	PolicyDigest      string `json:"policy_digest"`
}

// RecoveryHandoffObservationEnvelope carries a broker's short-lived,
// independently observed browser binding. The broker signs with Ed25519 so
// receivers need only a public key. It is evidence for context comparison;
// it never authorizes browser mutation or replay.
type RecoveryHandoffObservationEnvelope struct {
	Schema     string                         `json:"schema"`
	Binding    RecoveryHandoffObservedBinding `json:"binding"`
	KeyID      string                         `json:"key_id"`
	ObservedAt string                         `json:"observed_at"`
	ExpiresAt  string                         `json:"expires_at"`
	Signature  string                         `json:"signature"`
}

// RecoveryHandoffBindingComparison records which signed handoff claims match
// trusted receiver state. SameContext never authorizes action dispatch.
type RecoveryHandoffBindingComparison struct {
	SameOwner     bool
	SameBrowser   bool
	SameTarget    bool
	SameFrame     bool
	SamePolicy    bool
	SameContext   bool
	ReplayAllowed bool
}

// NewRecoveryHandoffEnvelope signs a receipt with a host-held HMAC key. Use a
// distinct key per host trust domain and keep key material outside Scout's
// task store. BrowserGeneration is a host/broker token tied to the actual
// browser context; it is not inferred from Scout's vendor-local fingerprint.
func NewRecoveryHandoffEnvelope(receipt RecoveryReceipt, ownerScope, browserGeneration, keyID string, key []byte, issuedAt, expiresAt time.Time) (RecoveryHandoffEnvelope, error) {
	if err := receipt.Validate(); err != nil {
		return RecoveryHandoffEnvelope{}, fmt.Errorf("create recovery handoff: %w", err)
	}
	if strings.TrimSpace(ownerScope) == "" || strings.TrimSpace(browserGeneration) == "" || strings.TrimSpace(keyID) == "" {
		return RecoveryHandoffEnvelope{}, errors.New("recovery handoff requires owner, browser generation, and key ID")
	}
	if len(key) < 32 {
		return RecoveryHandoffEnvelope{}, errors.New("recovery handoff signing key must contain at least 32 bytes")
	}
	issuedAt = issuedAt.UTC()
	expiresAt = expiresAt.UTC()
	if issuedAt.IsZero() || expiresAt.IsZero() || !expiresAt.After(issuedAt) || expiresAt.Sub(issuedAt) > maxRecoveryHandoffTTL {
		return RecoveryHandoffEnvelope{}, fmt.Errorf("recovery handoff expiry must be after its issue time and no more than %s later", maxRecoveryHandoffTTL)
	}
	envelope := RecoveryHandoffEnvelope{
		Schema: RecoveryHandoffSchema, Receipt: receipt,
		OwnerScope: strings.TrimSpace(ownerScope), BrowserGeneration: strings.TrimSpace(browserGeneration),
		KeyID: strings.TrimSpace(keyID), IssuedAt: issuedAt.Format(time.RFC3339Nano),
		ExpiresAt: expiresAt.Format(time.RFC3339Nano),
	}
	signature, err := recoveryHandoffSignature(envelope, key)
	if err != nil {
		return RecoveryHandoffEnvelope{}, err
	}
	envelope.Signature = signature
	return envelope, nil
}

// VerifyRecoveryHandoffEnvelope checks the signature, expiry, embedded
// receipt, and authenticated owner scope. The caller must still compare the
// browser generation, target, frame, and policy with its live trusted state.
func VerifyRecoveryHandoffEnvelope(envelope RecoveryHandoffEnvelope, trustedKeys map[string][]byte, expectedOwnerScope string, now time.Time) error {
	if envelope.Schema != RecoveryHandoffSchema {
		return fmt.Errorf("unsupported recovery handoff schema %q", envelope.Schema)
	}
	if err := envelope.Receipt.Validate(); err != nil {
		return fmt.Errorf("verify recovery handoff receipt: %w", err)
	}
	if envelope.OwnerScope == "" || envelope.BrowserGeneration == "" || envelope.KeyID == "" || envelope.Signature == "" {
		return errors.New("recovery handoff identity or signature is incomplete")
	}
	if expectedOwnerScope == "" || !hmac.Equal([]byte(envelope.OwnerScope), []byte(expectedOwnerScope)) {
		return errors.New("recovery handoff owner is not authorized")
	}
	issuedAt, err := time.Parse(time.RFC3339Nano, envelope.IssuedAt)
	if err != nil {
		return fmt.Errorf("recovery handoff issue time is invalid: %w", err)
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, envelope.ExpiresAt)
	if err != nil || !expiresAt.After(issuedAt) || expiresAt.Sub(issuedAt) > maxRecoveryHandoffTTL {
		return errors.New("recovery handoff expiry is invalid")
	}
	if now.IsZero() {
		return errors.New("recovery handoff verification requires current time")
	}
	if issuedAt.After(now.Add(30 * time.Second)) {
		return errors.New("recovery handoff issue time is in the future")
	}
	if !now.Before(expiresAt) {
		return errors.New("recovery handoff has expired")
	}
	key, ok := trustedKeys[envelope.KeyID]
	if !ok || len(key) < 32 {
		return errors.New("recovery handoff signing key is unavailable")
	}
	expectedSignature, err := recoveryHandoffSignature(envelope, key)
	if err != nil {
		return err
	}
	provided, err := base64.RawURLEncoding.DecodeString(envelope.Signature)
	if err != nil {
		return errors.New("recovery handoff signature encoding is invalid")
	}
	expected, _ := base64.RawURLEncoding.DecodeString(expectedSignature)
	if !hmac.Equal(provided, expected) {
		return errors.New("recovery handoff signature is invalid")
	}
	return nil
}

// CompareRecoveryHandoffBinding verifies a handoff and compares it with
// independently observed receiver state. A complete match allows only the
// host to consider read-only page inspection; the result always keeps
// ReplayAllowed false.
func CompareRecoveryHandoffBinding(envelope RecoveryHandoffEnvelope, trustedKeys map[string][]byte, expectedOwnerScope string, now time.Time, observed RecoveryHandoffObservedBinding) (RecoveryHandoffBindingComparison, error) {
	if err := VerifyRecoveryHandoffEnvelope(envelope, trustedKeys, expectedOwnerScope, now); err != nil {
		return RecoveryHandoffBindingComparison{}, err
	}
	receipt := envelope.Receipt
	comparison := RecoveryHandoffBindingComparison{
		SameOwner:     envelope.OwnerScope != "" && envelope.OwnerScope == observed.OwnerScope,
		SameBrowser:   envelope.BrowserGeneration != "" && envelope.BrowserGeneration == observed.BrowserGeneration,
		SameTarget:    receipt.Target.ID != "" && receipt.Target.ID == observed.TargetID,
		SameFrame:     observed.FrameBindingKnown && receipt.Target.FrameID == observed.FrameID,
		SamePolicy:    receipt.Policy != nil && receipt.Policy.ID != "" && receipt.Policy.Digest != "" && receipt.Policy.ID == observed.PolicyID && receipt.Policy.Digest == observed.PolicyDigest,
		ReplayAllowed: false,
	}
	comparison.SameContext = comparison.SameOwner && comparison.SameBrowser && comparison.SameTarget && comparison.SameFrame && comparison.SamePolicy
	return comparison, nil
}

// NewRecoveryHandoffObservation signs a receiver-side browser binding with a
// broker-owned Ed25519 key. Keep the private key in the broker; distribute
// only its public key to receiving runtimes. Observation validity is bounded
// by expiresAt and must be checked again at the receiver.
func NewRecoveryHandoffObservation(binding RecoveryHandoffObservedBinding, keyID string, privateKey ed25519.PrivateKey, observedAt, expiresAt time.Time) (RecoveryHandoffObservationEnvelope, error) {
	if strings.TrimSpace(keyID) == "" || len(privateKey) != ed25519.PrivateKeySize {
		return RecoveryHandoffObservationEnvelope{}, errors.New("recovery observation requires a key ID and valid Ed25519 private key")
	}
	bindingJSON, err := json.Marshal(binding)
	if err != nil {
		return RecoveryHandoffObservationEnvelope{}, fmt.Errorf("encode recovery observation binding: %w", err)
	}
	if _, err := DecodeRecoveryHandoffObservedBinding(bindingJSON); err != nil {
		return RecoveryHandoffObservationEnvelope{}, err
	}
	observedAt = observedAt.UTC()
	expiresAt = expiresAt.UTC()
	if observedAt.IsZero() || expiresAt.IsZero() || !expiresAt.After(observedAt) || expiresAt.Sub(observedAt) > maxRecoveryHandoffObservationTTL {
		return RecoveryHandoffObservationEnvelope{}, fmt.Errorf("recovery observation expiry must be after its observation time and no more than %s later", maxRecoveryHandoffObservationTTL)
	}
	envelope := RecoveryHandoffObservationEnvelope{
		Schema: RecoveryHandoffObservationSchema, Binding: binding,
		KeyID: strings.TrimSpace(keyID), ObservedAt: observedAt.Format(time.RFC3339Nano),
		ExpiresAt: expiresAt.Format(time.RFC3339Nano),
	}
	signature, err := recoveryHandoffObservationSignature(envelope, privateKey)
	if err != nil {
		return RecoveryHandoffObservationEnvelope{}, err
	}
	envelope.Signature = signature
	return envelope, nil
}

// VerifyRecoveryHandoffObservation validates broker provenance, owner scope,
// signature, and freshness for a receiver-side binding.
func VerifyRecoveryHandoffObservation(envelope RecoveryHandoffObservationEnvelope, trustedKeys map[string]ed25519.PublicKey, expectedOwnerScope string, now time.Time) error {
	if envelope.Schema != RecoveryHandoffObservationSchema {
		return fmt.Errorf("unsupported recovery observation schema %q", envelope.Schema)
	}
	bindingJSON, err := json.Marshal(envelope.Binding)
	if err != nil {
		return fmt.Errorf("encode recovery observation binding: %w", err)
	}
	if _, err := DecodeRecoveryHandoffObservedBinding(bindingJSON); err != nil {
		return err
	}
	if expectedOwnerScope == "" || !hmac.Equal([]byte(envelope.Binding.OwnerScope), []byte(expectedOwnerScope)) {
		return errors.New("recovery observation owner is not authorized")
	}
	if envelope.KeyID == "" || envelope.Signature == "" {
		return errors.New("recovery observation key ID or signature is incomplete")
	}
	observedAt, err := time.Parse(time.RFC3339Nano, envelope.ObservedAt)
	if err != nil {
		return fmt.Errorf("recovery observation timestamp is invalid: %w", err)
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, envelope.ExpiresAt)
	if err != nil || !expiresAt.After(observedAt) || expiresAt.Sub(observedAt) > maxRecoveryHandoffObservationTTL {
		return errors.New("recovery observation expiry is invalid")
	}
	if now.IsZero() {
		return errors.New("recovery observation verification requires current time")
	}
	if observedAt.After(now.Add(30 * time.Second)) {
		return errors.New("recovery observation timestamp is in the future")
	}
	if !now.Before(expiresAt) {
		return errors.New("recovery observation has expired")
	}
	key, ok := trustedKeys[envelope.KeyID]
	if !ok || len(key) != ed25519.PublicKeySize {
		return errors.New("recovery observation signing key is unavailable")
	}
	provided, err := base64.RawURLEncoding.DecodeString(envelope.Signature)
	if err != nil || len(provided) != ed25519.SignatureSize {
		return errors.New("recovery observation signature encoding is invalid")
	}
	expectedBytes, err := recoveryHandoffObservationMessage(envelope)
	if err != nil {
		return err
	}
	if !ed25519.Verify(key, expectedBytes, provided) {
		return errors.New("recovery observation signature is invalid")
	}
	return nil
}

// CompareRecoveryHandoffWithObservation verifies both the sender's signed
// receipt and the broker's signed live observation before comparing context.
// A full match remains inspection evidence only; ReplayAllowed is always false.
func CompareRecoveryHandoffWithObservation(handoff RecoveryHandoffEnvelope, handoffKeys map[string][]byte, observation RecoveryHandoffObservationEnvelope, observationKeys map[string]ed25519.PublicKey, expectedOwnerScope string, now time.Time) (RecoveryHandoffBindingComparison, error) {
	if err := VerifyRecoveryHandoffObservation(observation, observationKeys, expectedOwnerScope, now); err != nil {
		return RecoveryHandoffBindingComparison{}, err
	}
	return CompareRecoveryHandoffBinding(handoff, handoffKeys, expectedOwnerScope, now, observation.Binding)
}

// DecodeRecoveryHandoffObservedBinding strictly decodes the receiver adapter's
// independent browser/broker observation. The host must authenticate and
// validate the source before calling this function; JSON shape alone does not
// make these claims trusted.
func DecodeRecoveryHandoffObservedBinding(data []byte) (RecoveryHandoffObservedBinding, error) {
	var observed RecoveryHandoffObservedBinding
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return RecoveryHandoffObservedBinding{}, fmt.Errorf("decode recovery handoff observation: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&observed); err != nil {
		return RecoveryHandoffObservedBinding{}, fmt.Errorf("decode recovery handoff observation: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return RecoveryHandoffObservedBinding{}, errors.New("decode recovery handoff observation: trailing JSON value")
		}
		return RecoveryHandoffObservedBinding{}, fmt.Errorf("decode recovery handoff observation: trailing content: %w", err)
	}
	if strings.TrimSpace(observed.OwnerScope) == "" || strings.TrimSpace(observed.BrowserGeneration) == "" || strings.TrimSpace(observed.TargetID) == "" || strings.TrimSpace(observed.PolicyID) == "" || !validRecoveryReceiptDigest(observed.PolicyDigest) {
		return RecoveryHandoffObservedBinding{}, errors.New("recovery handoff observation is missing a valid owner, browser generation, target, or policy binding")
	}
	if strings.ContainsAny(observed.FrameID, "\r\n\x00") || !observed.FrameBindingKnown && observed.FrameID != "" {
		return RecoveryHandoffObservedBinding{}, errors.New("recovery handoff observation has invalid frame identity")
	}
	return observed, nil
}

// DecodeRecoveryHandoffEnvelope strictly decodes one handoff and rejects
// unknown fields or trailing JSON. Signature and expiry checks are performed
// separately by VerifyRecoveryHandoffEnvelope.
func DecodeRecoveryHandoffEnvelope(data []byte) (RecoveryHandoffEnvelope, error) {
	var envelope RecoveryHandoffEnvelope
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return RecoveryHandoffEnvelope{}, fmt.Errorf("decode recovery handoff: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return RecoveryHandoffEnvelope{}, fmt.Errorf("decode recovery handoff: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return RecoveryHandoffEnvelope{}, errors.New("decode recovery handoff: trailing JSON value")
		}
		return RecoveryHandoffEnvelope{}, fmt.Errorf("decode recovery handoff: trailing content: %w", err)
	}
	if envelope.Schema != RecoveryHandoffSchema {
		return RecoveryHandoffEnvelope{}, fmt.Errorf("unsupported recovery handoff schema %q", envelope.Schema)
	}
	if err := envelope.Receipt.Validate(); err != nil {
		return RecoveryHandoffEnvelope{}, fmt.Errorf("decode recovery handoff receipt: %w", err)
	}
	return envelope, nil
}

// DecodeRecoveryHandoffObservationEnvelope strictly decodes one broker
// observation and rejects unknown fields or trailing JSON. Signature and
// freshness checks are performed by VerifyRecoveryHandoffObservation.
func DecodeRecoveryHandoffObservationEnvelope(data []byte) (RecoveryHandoffObservationEnvelope, error) {
	var envelope RecoveryHandoffObservationEnvelope
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return RecoveryHandoffObservationEnvelope{}, fmt.Errorf("decode recovery observation: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return RecoveryHandoffObservationEnvelope{}, fmt.Errorf("decode recovery observation: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return RecoveryHandoffObservationEnvelope{}, errors.New("decode recovery observation: trailing JSON value")
		}
		return RecoveryHandoffObservationEnvelope{}, fmt.Errorf("decode recovery observation: trailing content: %w", err)
	}
	if envelope.Schema != RecoveryHandoffObservationSchema {
		return RecoveryHandoffObservationEnvelope{}, fmt.Errorf("unsupported recovery observation schema %q", envelope.Schema)
	}
	bindingJSON, err := json.Marshal(envelope.Binding)
	if err != nil {
		return RecoveryHandoffObservationEnvelope{}, fmt.Errorf("encode recovery observation binding: %w", err)
	}
	if _, err := DecodeRecoveryHandoffObservedBinding(bindingJSON); err != nil {
		return RecoveryHandoffObservationEnvelope{}, err
	}
	return envelope, nil
}

func recoveryHandoffSignature(envelope RecoveryHandoffEnvelope, key []byte) (string, error) {
	envelope.Signature = ""
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return "", fmt.Errorf("encode recovery handoff: %w", err)
	}
	mac := hmac.New(sha256.New, key)
	if _, err := mac.Write(encoded); err != nil {
		return "", fmt.Errorf("sign recovery handoff: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func recoveryHandoffObservationSignature(envelope RecoveryHandoffObservationEnvelope, key ed25519.PrivateKey) (string, error) {
	message, err := recoveryHandoffObservationMessage(envelope)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, message)), nil
}

func recoveryHandoffObservationMessage(envelope RecoveryHandoffObservationEnvelope) ([]byte, error) {
	envelope.Signature = ""
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("encode recovery observation: %w", err)
	}
	return encoded, nil
}
