#!/usr/bin/env node
// Independent Node.js verifier for scout.recovery-handoff/v1.
// It verifies the signed envelope and compares only receiver-supplied trusted
// bindings. A match permits inspection only; replay is always disabled.
import { createHash, createHmac, createPublicKey, timingSafeEqual, verify as verifySignature } from 'node:crypto';
import { readFile } from 'node:fs/promises';

const [envelopePath, keyPath, expectedOwner, observationPath, observationPublicKeyPath, peerObservationPath, expectedURL] = process.argv.slice(2);
if (!envelopePath || !keyPath || !expectedOwner || !observationPath || !observationPublicKeyPath || !peerObservationPath || !expectedURL) {
  console.error('usage: node receiver.mjs ENVELOPE KEY EXPECTED_OWNER SIGNED_OBSERVATION OBSERVATION_PUBLIC_KEY PEER_OBSERVATION EXPECTED_URL');
  process.exit(2);
}

const fail = (message) => { console.error(message); process.exit(1); };
const rejectDuplicateKeys = (source) => {
  let index = 0;
  const ws = () => { while (/\s/.test(source[index] ?? '') && index < source.length) index++; };
  const string = () => {
    const start = index++;
    while (index < source.length) {
      if (source[index] === '\\') { index += 2; continue; }
      if (source[index++] === '"') return JSON.parse(source.slice(start, index));
    }
    throw new Error('unterminated JSON string');
  };
  const value = () => {
    ws();
    if (source[index] === '{') {
      index++;
      ws();
      const keys = new Set();
      if (source[index] === '}') { index++; return; }
      while (index < source.length) {
        ws();
        const key = string();
        if (keys.has(key)) throw new Error(`duplicate JSON key: ${key}`);
        keys.add(key);
        ws();
        if (source[index++] !== ':') throw new Error('invalid JSON object');
        value();
        ws();
        const delimiter = source[index++];
        if (delimiter === '}') return;
        if (delimiter !== ',') throw new Error('invalid JSON object');
      }
      throw new Error('unterminated JSON object');
    }
    if (source[index] === '[') {
      index++;
      ws();
      if (source[index] === ']') { index++; return; }
      while (index < source.length) {
        value();
        ws();
        const delimiter = source[index++];
        if (delimiter === ']') return;
        if (delimiter !== ',') throw new Error('invalid JSON array');
      }
      throw new Error('unterminated JSON array');
    }
    if (source[index] === '"') { string(); return; }
    const match = /^(?:true|false|null|-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?)/.exec(source.slice(index));
    if (!match) throw new Error('invalid JSON value');
    index += match[0].length;
  };
  value();
  ws();
  if (index !== source.length) throw new Error('trailing JSON content');
};
const envelopeJSON = await readFile(envelopePath, 'utf8');
const observationJSON = await readFile(observationPath, 'utf8');
const peerObservationJSON = await readFile(peerObservationPath, 'utf8');
try { rejectDuplicateKeys(envelopeJSON); rejectDuplicateKeys(observationJSON); rejectDuplicateKeys(peerObservationJSON); } catch (error) { fail(error.message); }
let envelope;
let observation;
let peerObservation;
try {
  envelope = JSON.parse(envelopeJSON);
  observation = JSON.parse(observationJSON);
  peerObservation = JSON.parse(peerObservationJSON);
} catch {
  fail('invalid JSON input');
}
const key = await readFile(keyPath);
const allowed = new Set(['schema', 'receipt', 'owner_scope', 'browser_generation', 'key_id', 'issued_at', 'expires_at', 'signature']);
if (Object.keys(envelope).some((field) => !allowed.has(field))) fail('unknown handoff field');
if (envelope.schema !== 'scout.recovery-handoff/v1') fail('unsupported handoff schema');
if (!envelope.receipt || envelope.receipt.schema !== 'scout.recovery-receipt/v1') fail('invalid embedded receipt');
if (!envelope.owner_scope || !envelope.browser_generation || !envelope.key_id || !envelope.signature) fail('incomplete handoff identity');
if (envelope.owner_scope !== expectedOwner) fail('handoff owner is not authorized');
if (key.length < 32) fail('verification key must contain at least 32 bytes');

const issued = Date.parse(envelope.issued_at);
const expires = Date.parse(envelope.expires_at);
const now = Date.now();
const maxHandoffTtlMs = 15 * 60 * 1000;
if (!Number.isFinite(issued) || !Number.isFinite(expires) || expires <= issued || expires - issued > maxHandoffTtlMs) fail('invalid handoff timestamps or expiry exceeds 15 minutes');
if (issued > now + 30_000) fail('handoff issue time is in the future');
if (now >= expires) fail('handoff has expired');

// Go encoding/json emits struct fields in declaration order and compact form.
// Keep this explicit field projection in sync with RecoveryHandoffEnvelope.
const signed = {
  schema: envelope.schema,
  receipt: envelope.receipt,
  owner_scope: envelope.owner_scope,
  browser_generation: envelope.browser_generation,
  key_id: envelope.key_id,
  issued_at: envelope.issued_at,
  expires_at: envelope.expires_at,
  signature: '',
};
// Go's encoding/json escapes HTML-sensitive characters and U+2028/U+2029.
const goJSONStringify = (value) => JSON.stringify(value)
  .replace(/</g, '\\u003c').replace(/>/g, '\\u003e').replace(/&/g, '\\u0026')
  .replace(/\u2028/g, '\\u2028').replace(/\u2029/g, '\\u2029');
const expected = createHmac('sha256', key).update(goJSONStringify(signed)).digest();
let provided;
try { provided = Buffer.from(envelope.signature, 'base64url'); } catch { fail('invalid signature encoding'); }
if (provided.length !== expected.length || !timingSafeEqual(provided, expected)) fail('invalid handoff signature');

const observationAllowed = new Set(['schema', 'binding', 'key_id', 'observed_at', 'expires_at', 'signature']);
if (Object.keys(observation).some((field) => !observationAllowed.has(field))) fail('unknown recovery observation field');
if (observation.schema !== 'scout.recovery-observation/v1' || !observation.binding || !observation.key_id || !observation.signature) fail('incomplete recovery observation');
const observationIssued = Date.parse(observation.observed_at);
const observationExpires = Date.parse(observation.expires_at);
if (!Number.isFinite(observationIssued) || !Number.isFinite(observationExpires) || observationExpires <= observationIssued || observationExpires - observationIssued > 120_000) fail('invalid recovery observation timestamps');
if (observationIssued > now + 30_000 || now >= observationExpires) fail('recovery observation is not current');
const observationBinding = observation.binding;
if (!observationBinding.owner_scope || observationBinding.owner_scope !== expectedOwner || !observationBinding.browser_generation || !observationBinding.target_id || !observationBinding.policy_id || !/^sha256:[0-9a-f]{64}$/.test(observationBinding.policy_digest ?? '')) fail('incomplete recovery observation binding');
if (observationBinding.frame_binding_known !== true && observationBinding.frame_id) fail('frame identity is present but not confirmed');
if (observationBinding.frame_id && /[\r\n\0]/.test(observationBinding.frame_id)) fail('invalid recovery observation frame identity');
const signedObservation = {
  schema: observation.schema,
  binding: {
    owner_scope: observationBinding.owner_scope,
    browser_generation: observationBinding.browser_generation,
    target_id: observationBinding.target_id,
    ...(observationBinding.frame_id ? { frame_id: observationBinding.frame_id } : {}),
    frame_binding_known: observationBinding.frame_binding_known,
    policy_id: observationBinding.policy_id,
    policy_digest: observationBinding.policy_digest,
  },
  key_id: observation.key_id,
  observed_at: observation.observed_at,
  expires_at: observation.expires_at,
  signature: '',
};
let observationSignature;
try { observationSignature = Buffer.from(observation.signature, 'base64url'); } catch { fail('invalid recovery observation signature encoding'); }
let observationPublicKey;
try { observationPublicKey = createPublicKey(await readFile(observationPublicKeyPath)); } catch { fail('invalid recovery observation public key'); }
if (!verifySignature(null, Buffer.from(goJSONStringify(signedObservation)), observationPublicKey, observationSignature)) fail('invalid recovery observation signature');

const receipt = envelope.receipt;
if (!receipt.task?.id || !receipt.task?.plan_fingerprint || !receipt.browser?.instance_id || !receipt.target?.id || !receipt.operation?.id || !receipt.operation?.name || !receipt.evidence?.created_at || !Array.isArray(receipt.evidence.references) || receipt.recovery?.replay_authorized !== false || receipt.recovery?.next_step !== 'reconcile_with_authoritative_application_state') fail('incomplete or unsafe embedded receipt');
if (!Number.isSafeInteger(receipt.operation.ordinal) || receipt.operation.ordinal < 1) fail('invalid receipt operation ordinal');
const taskIDFromOperation = /^scout-op-v1\.([A-Za-z0-9_-]+)\.(0|[1-9][0-9]*)$/.exec(receipt.operation.id);
if (!taskIDFromOperation) fail('invalid receipt operation ID');
let decodedTaskID;
try { decodedTaskID = Buffer.from(taskIDFromOperation[1], 'base64url').toString('utf8'); } catch { fail('invalid receipt operation ID task encoding'); }
if (Buffer.from(decodedTaskID, 'utf8').toString('base64url') !== taskIDFromOperation[1] || decodedTaskID !== receipt.task.id || Number(taskIDFromOperation[2]) !== receipt.operation.ordinal || taskIDFromOperation[2] === '0') fail('receipt operation ID does not match task-local ordinal');
if (!['not_started', 'may_have_dispatched', 'dispatched', 'unknown'].includes(receipt.operation.dispatch)) fail('invalid receipt dispatch state');
const outcomes = new Set(['verified', 'rejected', 'unknown', 'unverified']);
if (!outcomes.has(receipt.operation.outcome) || !outcomes.has(receipt.operation.verification)) fail('invalid receipt outcome or verification');
if (receipt.operation.outcome !== receipt.operation.verification) fail('receipt outcome and verification disagree');
if (receipt.operation.outcome === 'unknown' && !['may_have_dispatched', 'unknown'].includes(receipt.operation.dispatch)) fail('unknown receipt outcome contradicts dispatch state');
const policy = receipt.policy;
if (policy && policy.inputs_complete === true) {
  if (!Array.isArray(policy.origins) || typeof policy.allow_private_ips !== 'boolean' || !policy.id || !policy.digest) fail('incomplete receipt policy inputs');
  const origins = policy.origins.map((origin) => {
    let parsed;
    try { parsed = new URL(origin); } catch { fail('invalid receipt policy origin'); }
    if (!['http:', 'https:'].includes(parsed.protocol) || parsed.username || parsed.password || !['', '/'].includes(parsed.pathname) || parsed.search || parsed.hash) fail('invalid receipt policy origin');
    const host = parsed.hostname.toLowerCase();
    const port = parsed.port || (parsed.protocol === 'https:' ? '443' : '80');
    return `${parsed.protocol}//${host}:${port}`;
  });
  const canonical = [...new Set(origins)].sort();
  if (JSON.stringify(origins) !== JSON.stringify(canonical)) fail('receipt policy origins are not canonical');
  const scope = 'scout-origin-policy-v1\npage-target-http-requests\n'
    + (policy.allow_private_ips ? 'private-ip-access=allowed\n' : 'private-ip-access=blocked\n')
    + (canonical.length === 0 ? 'unrestricted\n' : 'exact-allowlist\n')
    + canonical.join('\n');
  const policyDigest = createHash('sha256').update(scope).digest('hex');
  if (policy.digest !== `sha256:${policyDigest}` || policy.id !== `scout-origin-v1:${policyDigest}`) fail('receipt policy digest does not match its inputs');
}
if (!peerObservation.target_id || !peerObservation.url || !/^https?:\/\//.test(peerObservation.url)) fail('incomplete agent-browser target observation');
const sameOwner = envelope.owner_scope === observationBinding.owner_scope;
const sameBrowser = envelope.browser_generation !== '' && envelope.browser_generation === observationBinding.browser_generation;
const peerTargetMatches = peerObservation.target_id === receipt.target.id;
let peerURLMatches = false;
try { peerURLMatches = new URL(peerObservation.url).href === new URL(expectedURL).href; } catch { fail('invalid peer or expected URL'); }
const peerObservationMatches = peerTargetMatches && peerURLMatches;
const sameTarget = peerObservationMatches && Boolean(receipt.target?.id) && receipt.target.id === observationBinding.target_id;
const sameFrame = observationBinding.frame_binding_known === true && (receipt.target?.frame_id ?? '') === (observationBinding.frame_id ?? '');
const samePolicy = Boolean(policy?.id && policy?.digest) && policy.id === observationBinding.policy_id && policy.digest === observationBinding.policy_digest;
const sameContext = sameOwner && sameBrowser && sameTarget && sameFrame && samePolicy;
process.stdout.write(JSON.stringify({ same_owner: sameOwner, same_browser: sameBrowser, same_target: sameTarget, peer_target_matches: peerTargetMatches, peer_url_matches: peerURLMatches, peer_observation_matches: peerObservationMatches, same_frame: sameFrame, same_policy: samePolicy, same_context: sameContext, replay_allowed: false }) + '\n');
