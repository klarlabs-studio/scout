"""mitmproxy addon that enforces an exact browser egress origin allowlist.

Set SCOUT_ALLOWED_ORIGINS to comma-separated HTTPS/HTTP origins, then run:
    mitmdump --mode regular --listen-host 127.0.0.1 --listen-port 8080 -s allowlist.py

HTTPS and WSS use CONNECT, so the proxy can enforce destination host and port
without decrypting TLS. For those tunnels, HTTP and HTTPS both map to the
CONNECT authority; use explicit host/port policy and network firewall rules.
Run mitmdump with ``--set ignore_hosts=.*`` so allowed TLS tunnels pass through
without requiring Chrome to trust mitmproxy's local certificate authority.
"""

from __future__ import annotations

import os
import json
import asyncio
import ipaddress
import socket
import sys
import time
import uuid
from urllib.parse import urlsplit


def parse_origins(value: str) -> frozenset[tuple[str, str, int]]:
    """Parse a comma-separated list into normalized (scheme, host, port) tuples."""
    origins: set[tuple[str, str, int]] = set()
    for raw in value.split(","):
        raw = raw.strip()
        if not raw:
            continue
        parsed = urlsplit(raw)
        scheme = parsed.scheme.lower()
        if scheme not in {"http", "https"} or not parsed.hostname:
            raise ValueError(f"expected an http(s) origin, got {raw!r}")
        if parsed.username is not None or parsed.password is not None or parsed.path not in {"", "/"} or parsed.query or parsed.fragment:
            raise ValueError(f"expected an origin without credentials, path, query, or fragment: {raw!r}")
        try:
            port = parsed.port
        except ValueError as exc:
            raise ValueError(f"invalid port in origin {raw!r}") from exc
        if port is None:
            port = 443 if scheme == "https" else 80
        origins.add((scheme, parsed.hostname.rstrip(".").lower(), port))
    if not origins:
        raise ValueError("SCOUT_ALLOWED_ORIGINS must contain at least one origin")
    return frozenset(origins)


class OriginAllowlist:
    def __init__(self, origins: frozenset[tuple[str, str, int]], allow_private_ips: bool = False):
        self.origins = origins
        self.allow_private_ips = allow_private_ips

    @staticmethod
    def _record(flow, event: str, scheme: str, host: str, port: int | None, method: str, allowed: bool, reason: str, resolved_ip: str | None = None) -> bool:
        """Emit a path-free, machine-readable policy decision; fail closed on audit failure."""
        from mitmproxy import ctx

        record = {
            "type": "scout_egress_decision",
            "decision_id": uuid.uuid4().hex,
            "timestamp_ms": int(time.time() * 1000),
            "event": event,
            "scheme": scheme,
            "host": host.rstrip(".").lower(),
            "port": port,
            "method": method,
            "allowed": allowed,
            "reason": reason,
        }
        if resolved_ip is not None:
            record["resolved_ip"] = resolved_ip
        try:
            ctx.log.info("SCOUT_EGRESS_DECISION " + json.dumps(record, separators=(",", ":"), sort_keys=True))
            return True
        except Exception as exc:
            try:
                ctx.log.error(f"SCOUT_EGRESS_AUDIT_FAILURE {type(exc).__name__}")
            except Exception:
                pass
            return False

    def allowed(self, scheme: str, host: str, port: int | None) -> bool:
        scheme = scheme.lower()
        # CONNECT is used for HTTPS and WSS. Policy is by secure origin authority.
        if scheme == "wss":
            scheme = "https"
        elif scheme == "ws":
            scheme = "http"
        default_port = 443 if scheme == "https" else 80
        if port is None:
            port = default_port
        return (scheme, host.rstrip(".").lower(), port) in self.origins

    def request(self, flow) -> None:
        request = flow.request
        # Cleartext HTTP and ws:// expose their full URL to a regular proxy.
        if request.method.upper() == "CONNECT":
            return
        try:
            parsed = urlsplit(request.pretty_url)
            scheme = parsed.scheme.lower()
            host = parsed.hostname or ""
            port = parsed.port
        except ValueError:
            self._deny(flow, "blocked malformed proxy request URL")
            return
        if scheme not in {"http", "https", "ws", "wss"}:
            reason = f"unsupported proxy scheme {scheme or '[missing]'}"
            if not self._record(flow, "request", scheme, host, None, request.method, False, reason):
                self._deny(flow, "blocked because egress audit failed", 503)
                return
            self._deny(flow, f"blocked {reason}")
            return
        display_port = port
        if port is None:
            port = 443 if scheme in {"https", "wss"} else 80
        allowed = self.allowed(scheme, host, port)
        if not self._record(flow, "request", scheme, host, port, request.method, allowed, "origin_allowed" if allowed else "origin_denied"):
            self._deny(flow, "blocked because egress audit failed", 503)
            return
        if not allowed:
            # Do not copy URL userinfo into proxy logs or the denial response.
            display_host = f"[{host}]" if ":" in host else host
            authority = display_host if display_port is None else f"{display_host}:{display_port}"
            self._deny(flow, f"blocked origin {scheme}://{authority}")

    def http_connect(self, flow) -> None:
        request = flow.request
        # CONNECT authorities have no scheme; Chromium uses this for https:// and wss://.
        allowed = self.allowed("https", request.host, request.port)
        if not self._record(flow, "connect", "https_or_wss", request.host, request.port, "CONNECT", allowed, "authority_allowed" if allowed else "authority_denied"):
            self._deny(flow, "blocked because egress audit failed", 503)
            return
        if not allowed:
            self._deny(flow, f"blocked CONNECT authority {request.host}:{request.port}")

    async def server_connect(self, data) -> None:
        """Resolve and pin the selected peer before mitmproxy opens a socket."""
        server = data.server
        if not server.address:
            if not self._record(None, "server_connect", "tcp", "", None, "CONNECT", False, "destination_unavailable"):
                server.error = "blocked because egress audit failed"
                return
            server.error = "blocked because destination address is unavailable"
            return
        host, port = server.address
        try:
            literal = ipaddress.ip_address(host)
            addresses = [str(literal)]
        except ValueError:
            try:
                loop = asyncio.get_running_loop()
                records = await asyncio.wait_for(
                    loop.getaddrinfo(host, port, type=socket.SOCK_STREAM), timeout=5
                )
                addresses = list(dict.fromkeys(record[4][0].split("%", 1)[0] for record in records))
            except Exception:
                audited = self._record(None, "server_connect", "tcp", host, port, "CONNECT", False, "resolution_failed")
                server.error = "blocked because egress audit failed" if not audited else "blocked because destination resolution failed"
                return
        if not addresses:
            audited = self._record(None, "server_connect", "tcp", host, port, "CONNECT", False, "resolution_empty")
            server.error = "blocked because egress audit failed" if not audited else "blocked because destination resolution returned no addresses"
            return

        # Reject the hostname if any answer is non-global, rather than picking
        # a public answer while another resolver result can reach a private peer.
        try:
            non_global = [address for address in addresses if not ipaddress.ip_address(address).is_global]
        except ValueError:
            audited = self._record(None, "server_connect", "tcp", host, port, "CONNECT", False, "resolution_invalid_address")
            server.error = "blocked because egress audit failed" if not audited else "blocked because destination resolution returned an invalid address"
            return
        allowed = self.allow_private_ips or not non_global
        selected_ip = addresses[0]
        reason = "peer_address_allowed" if allowed else "private_peer_denied"
        if not self._record(None, "server_connect", "tcp", host, port, "CONNECT", allowed, reason, selected_ip):
            server.error = "blocked because egress audit failed"
            return
        if not allowed:
            server.error = "blocked because destination resolved to a non-public address"
            return

        # Pin the selected answer for every connection so mitmproxy does not
        # perform a second DNS lookup after policy evaluation. The private-IP
        # opt-in changes which answers are allowed, not which peer is dialed.
        server.address = (selected_ip, port)
        if server.sni is None:
            try:
                ipaddress.ip_address(host)
            except ValueError:
                server.sni = host

    @staticmethod
    def _deny(flow, message: str, status: int = 403) -> None:
        from mitmproxy import ctx, http

        ctx.log.info(f"scout egress denied: {message}")
        flow.response = http.Response.make(status, message.encode("utf-8"), {"content-type": "text/plain"})


def load_addons(value: str | None = None):
    """Load a configured addon and emit a path-free startup audit record.

    Invalid or missing policy raises and prevents mitmproxy from accepting
    traffic. There is deliberately no permissive fallback.
    """
    configured = os.environ.get("SCOUT_ALLOWED_ORIGINS", "") if value is None else value
    private_setting = os.environ.get("SCOUT_ALLOW_PRIVATE_IPS", "").strip().lower()
    try:
        if private_setting not in {"", "0", "false", "1", "true"}:
            raise ValueError("SCOUT_ALLOW_PRIVATE_IPS must be true or false")
        allow_private_ips = private_setting in {"1", "true"}
        origins = parse_origins(configured)
    except Exception as exc:
        failure = (
            "SCOUT_EGRESS_STARTUP "
            + json.dumps(
                {"type": "scout_egress_startup", "status": "error", "reason": "invalid_policy", "error_type": "PolicyConfigurationError"},
                separators=(",", ":"), sort_keys=True,
            )
        )
        print(failure, file=sys.stderr, flush=True)
        # Parser exceptions include the raw origin value. Do not let mitmproxy's
        # startup traceback echo credentials or paths from the configuration.
        raise RuntimeError("egress proxy policy configuration is invalid") from None
    print(
        "SCOUT_EGRESS_STARTUP "
        + json.dumps(
            {"type": "scout_egress_startup", "status": "ok", "origin_count": len(origins)},
            separators=(",", ":"), sort_keys=True,
        ),
        file=sys.stderr,
        flush=True,
    )
    return [OriginAllowlist(origins, allow_private_ips=allow_private_ips)]


addons = load_addons()
