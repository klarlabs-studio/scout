#!/usr/bin/env python3
"""Independently audit one Scout recovery receipt without importing Scout code.

This validates the v1 envelope shape, semantic state invariants, and the
origin-policy digest. It does not authenticate the receipt producer, browser,
owner, or application outcome; those require trusted host-side evidence.
"""

from __future__ import annotations

import base64
import binascii
import datetime
import hashlib
import ipaddress
import json
import re
import sys
from pathlib import Path
from urllib.parse import urlsplit


FIELDS = {
    "root": {"schema", "task", "runtime", "browser", "target", "policy", "operation", "postcondition", "evidence", "recovery"},
    "task": {"id", "plan_fingerprint", "state"},
    "runtime": {"name", "version", "receipt_schema"},
    "browser": {"instance_id", "generation", "ownership"},
    "target": {"id", "frame_id"},
    "policy": {"id", "digest", "origins", "allow_private_ips", "inputs_complete", "enforcement"},
    "operation": {"id", "ordinal", "name", "dispatch", "outcome", "verification", "deadline_at_ms"},
    "postcondition": {"kind", "captured"},
    "evidence": {"created_at", "observed_at", "trust", "references"},
    "recovery": {"replay_authorized", "next_step"},
}
REQUIRED = {
    "root": {"schema", "task", "runtime", "browser", "target", "operation", "evidence", "recovery"},
    "task": {"id", "plan_fingerprint", "state"},
    "runtime": {"name", "receipt_schema"},
    "browser": {"instance_id", "ownership"},
    "target": {"id"},
    "policy": {"origins", "allow_private_ips", "inputs_complete", "enforcement"},
    "operation": {"id", "ordinal", "name", "dispatch", "outcome", "verification"},
    "postcondition": {"kind", "captured"},
    "evidence": {"created_at", "trust", "references"},
    "recovery": {"replay_authorized", "next_step"},
}


def fail(message: str) -> None:
    raise ValueError(message)


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            fail(f"duplicate JSON key: {key}")
        result[key] = value
    return result


def object_at(value, name, required=True):
    if value is None and not required:
        return None
    if not isinstance(value, dict):
        fail(f"{name} must be an object")
    unknown = set(value) - FIELDS[name]
    missing = REQUIRED[name] - set(value)
    if unknown:
        fail(f"{name} has unknown fields: {', '.join(sorted(unknown))}")
    if missing:
        fail(f"{name} is missing fields: {', '.join(sorted(missing))}")
    return value


def nonempty(value, name):
    if not isinstance(value, str) or not value.strip():
        fail(f"{name} must be a non-empty string")


def canonical_origin(raw):
    if not isinstance(raw, str):
        fail("policy origins must contain strings")
    parsed = urlsplit(raw)
    if parsed.scheme not in {"http", "https"} or not parsed.hostname or parsed.username or parsed.password:
        fail(f"policy origin is not an HTTP(S) origin: {raw!r}")
    if parsed.path not in {"", "/"} or parsed.query or parsed.fragment:
        fail(f"policy origin contains a path, query, or fragment: {raw!r}")
    if parsed.netloc.endswith(":"):
        fail(f"policy origin has an empty port: {raw!r}")
    host = parsed.hostname.lower()
    try:
        address = ipaddress.ip_address(host)
        host = address.compressed
        if address.version == 6:
            host = f"[{host}]"
    except ValueError:
        if "*" in host:
            fail(f"policy origin contains a wildcard host: {raw!r}")
        # Keep parity with the exporter: non-IP hostnames are lowercase text.
    port = parsed.port or (443 if parsed.scheme == "https" else 80)
    if not 1 <= port <= 65535:
        fail(f"policy origin has an invalid port: {raw!r}")
    return f"{parsed.scheme}://{host}:{port}"


def policy_digest(origins, allow_private_ips):
    canonical = sorted({canonical_origin(origin) for origin in origins})
    scope = "scout-origin-policy-v1\npage-target-http-requests\n"
    scope += "private-ip-access=allowed\n" if allow_private_ips else "private-ip-access=blocked\n"
    scope += "unrestricted\n" if not canonical else "exact-allowlist\n"
    digest = hashlib.sha256((scope + "\n".join(canonical)).encode()).hexdigest()
    return "scout-origin-v1:" + digest, "sha256:" + digest


def audit_policy_vectors(path):
    """Check this independent implementation against shared Go contract vectors."""
    vectors = json.loads(path.read_text(encoding="utf-8"), object_pairs_hook=unique_object)
    if not isinstance(vectors, list) or not vectors:
        fail("origin policy vector file must contain a non-empty array")
    for vector in vectors:
        if not isinstance(vector, dict):
            fail("origin policy vector must be an object")
        policy_id, _ = policy_digest(vector["origins"], vector["allow_private_ips"])
        canonical = sorted({canonical_origin(origin) for origin in vector["origins"]})
        if policy_id != vector["policy_id"] or canonical != vector["canonical_origins"]:
            fail(f"origin policy vector mismatch: {vector['name']}")


def audit(receipt):
    root = object_at(receipt, "root")
    if root["schema"] != "scout.recovery-receipt/v1":
        fail("unsupported receipt schema")
    task = object_at(root["task"], "task")
    runtime = object_at(root["runtime"], "runtime")
    browser = object_at(root["browser"], "browser")
    target = object_at(root["target"], "target")
    policy = object_at(root.get("policy"), "policy", required=False)
    operation = object_at(root["operation"], "operation")
    postcondition = object_at(root.get("postcondition"), "postcondition", required=False)
    if "policy" in root and policy is None:
        fail("policy must be an object when present")
    if "postcondition" in root and postcondition is None:
        fail("postcondition must be an object when present")
    evidence = object_at(root["evidence"], "evidence")
    recovery = object_at(root["recovery"], "recovery")

    for name, value in (("task.id", task["id"]), ("task.plan_fingerprint", task["plan_fingerprint"]),
                        ("runtime.name", runtime["name"]), ("browser.instance_id", browser["instance_id"]),
                        ("target.id", target["id"]), ("operation.id", operation["id"]),
                        ("operation.name", operation["name"])):
        nonempty(value, name)
    if task["state"] not in {"completed", "interrupted", "rejected", "needs_reconciliation"}:
        fail("invalid task.state")
    if isinstance(runtime["receipt_schema"], bool) or not isinstance(runtime["receipt_schema"], int) or runtime["receipt_schema"] < 1:
        fail("runtime.receipt_schema must be a positive integer")
    if browser["ownership"] not in {"owned", "shared", "attached", "unknown"}:
        fail("invalid browser.ownership")
    ordinal = operation["ordinal"]
    if isinstance(ordinal, bool) or not isinstance(ordinal, int) or ordinal < 1:
        fail("operation.ordinal must be a positive integer")
    operation_id = re.fullmatch(r"scout-op-v1\.([A-Za-z0-9_-]+)\.([1-9][0-9]*)", operation["id"])
    if operation_id is None:
        fail("operation.id must use the canonical scout-op-v1 format")
    encoded_task_id, encoded_ordinal = operation_id.groups()
    try:
        decoded_task_id = base64.urlsafe_b64decode(encoded_task_id + "=" * (-len(encoded_task_id) % 4)).decode("utf-8")
    except (binascii.Error, UnicodeDecodeError):
        fail("operation.id has an invalid task ID encoding")
    if base64.urlsafe_b64encode(decoded_task_id.encode("utf-8")).decode("ascii").rstrip("=") != encoded_task_id:
        fail("operation.id task ID encoding is not canonical")
    if decoded_task_id != task["id"] or encoded_ordinal != str(ordinal):
        fail("operation.id must match task ID and task-local ordinal")
    if operation["dispatch"] not in {"not_started", "may_have_dispatched", "dispatched", "unknown"}:
        fail("invalid operation.dispatch")
    states = {"verified", "rejected", "unknown", "unverified"}
    if operation["outcome"] not in states or operation["verification"] not in states:
        fail("invalid operation outcome or verification")
    if operation["outcome"] != operation["verification"]:
        fail("operation.outcome and operation.verification disagree")
    if operation["outcome"] == "unknown" and operation["dispatch"] not in {"may_have_dispatched", "unknown"}:
        fail("unknown outcome contradicts dispatch state")
    if "deadline_at_ms" in operation and (isinstance(operation["deadline_at_ms"], bool) or not isinstance(operation["deadline_at_ms"], int) or operation["deadline_at_ms"] < 0):
        fail("operation.deadline_at_ms must be a non-negative integer")
    if evidence["trust"] != "runtime_metadata" or not isinstance(evidence["references"], list):
        fail("evidence must be runtime_metadata with a references array")
    for key in ("created_at", "observed_at"):
        value = evidence.get(key)
        if value is None and key == "observed_at":
            continue
        if not isinstance(value, str):
            fail(f"evidence.{key} must be an RFC3339 timestamp")
        try:
            timestamp = re.fullmatch(r"(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(\d+))?(Z|[+-]\d{2}:\d{2})", value)
            if timestamp is None:
                fail(f"evidence.{key} must be an RFC3339 timestamp")
            base, fraction, zone = timestamp.groups()
            normalized = base + ("." + fraction[:6].ljust(6, "0") if fraction else "") + zone.replace("Z", "+00:00")
            parsed_time = datetime.datetime.fromisoformat(normalized)
            if parsed_time.tzinfo is None:
                fail(f"evidence.{key} must include a timezone")
        except ValueError:
            fail(f"evidence.{key} must be an RFC3339 timestamp")
    if recovery["replay_authorized"] is not False or recovery["next_step"] != "reconcile_with_authoritative_application_state":
        fail("receipt must prohibit replay and require authoritative reconciliation")

    if policy is not None:
        if not isinstance(policy["origins"], list) or not isinstance(policy["allow_private_ips"], bool) or not isinstance(policy["inputs_complete"], bool):
            fail("policy inputs have invalid types")
        if policy["enforcement"] not in {"session_page_policy", "proxy", "combined", "none", "unknown"}:
            fail("invalid policy.enforcement")
        digest = policy.get("digest", "")
        policy_id = policy.get("id", "")
        if digest and not re.fullmatch(r"sha256:[0-9a-f]{64}", digest):
            fail("invalid policy.digest")
        if policy["inputs_complete"]:
            if not digest or not policy_id:
                fail("complete policy inputs require id and digest")
            normalized = [canonical_origin(origin) for origin in policy["origins"]]
            if normalized != policy["origins"] or normalized != sorted(set(normalized)):
                fail("policy.origins must be a sorted, unique canonical origin list")
            expected_id, expected_digest = policy_digest(policy["origins"], policy["allow_private_ips"])
            if policy_id != expected_id or digest != expected_digest:
                fail("policy id or digest does not match canonical inputs")
        elif digest and policy_id != "scout-origin-v1:" + digest.removeprefix("sha256:"):
            fail("policy id and digest disagree")

    # Report the evidence boundary explicitly; valid syntax does not authenticate claims.
    return {
        "valid": True,
        "schema": root["schema"],
        "task_id": task["id"],
        "operation_id": operation["id"],
        "outcome": operation["outcome"],
        "replay_authorized": False,
        "producer_authenticated": False,
        "browser_binding_independently_observed": False,
        "application_outcome_independently_observed": False,
        "note": "Structural and semantic checks passed; supply authenticated host and application evidence separately.",
    }


def main():
    if len(sys.argv) != 2:
        print("usage: verify_recovery_receipt.py RECEIPT.json", file=sys.stderr)
        return 2
    try:
        vectors_path = Path(__file__).resolve().parents[2] / "docs" / "schemas" / "scout-origin-policy-v1-vectors.json"
        audit_policy_vectors(vectors_path)
        raw = Path(sys.argv[1]).read_text(encoding="utf-8")
        receipt = json.loads(raw, object_pairs_hook=unique_object)
        result = audit(receipt)
    except (OSError, UnicodeError, json.JSONDecodeError, ValueError) as exc:
        print(json.dumps({"valid": False, "error": str(exc)}, separators=(",", ":")), file=sys.stderr)
        return 1
    print(json.dumps(result, sort_keys=True, separators=(",", ":")))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
