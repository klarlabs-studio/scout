import importlib.util
import asyncio
import contextlib
import io
import json
import os
import pathlib
import sys
import types
from types import SimpleNamespace
import unittest
from unittest.mock import patch


os.environ["SCOUT_ALLOWED_ORIGINS"] = "https://example.com"
spec = importlib.util.spec_from_file_location("scout_proxy", pathlib.Path(__file__).with_name("allowlist.py"))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class OriginAllowlistTest(unittest.TestCase):
    def setUp(self):
        self.audit_messages = []
        fake_mitmproxy = types.ModuleType("mitmproxy")
        fake_mitmproxy.ctx = SimpleNamespace(
            log=SimpleNamespace(info=self.audit_messages.append, error=self.audit_messages.append)
        )
        self.mitmproxy_patch = patch.dict(sys.modules, {"mitmproxy": fake_mitmproxy})
        self.mitmproxy_patch.start()

    def tearDown(self):
        self.mitmproxy_patch.stop()

    def test_exact_origin_and_default_port(self):
        policy = module.OriginAllowlist(module.parse_origins("https://example.com, http://localhost:8080"))
        self.assertTrue(policy.allowed("https", "EXAMPLE.COM", 443))
        self.assertTrue(policy.allowed("wss", "example.com", 443))
        self.assertTrue(policy.allowed("http", "localhost", 8080))
        self.assertFalse(policy.allowed("https", "sub.example.com", 443))
        self.assertFalse(policy.allowed("https", "example.com", 8443))
        self.assertFalse(policy.allowed("http", "example.com", 80))

    def test_explicit_zero_port_does_not_alias_default_port(self):
        zero_port = module.OriginAllowlist(module.parse_origins("https://example.com:0"))
        default_port = module.OriginAllowlist(module.parse_origins("https://example.com"))

        self.assertTrue(zero_port.allowed("https", "example.com", 0))
        self.assertFalse(zero_port.allowed("https", "example.com", 443))
        self.assertFalse(default_port.allowed("https", "example.com", 0))
        self.assertTrue(default_port.allowed("https", "example.com", 443))

    def test_rejects_non_origins(self):
        for value in ("", "https://user:pass@example.com", "https://example.com/path", "file:///tmp"):
            with self.subTest(value=value), self.assertRaises(ValueError):
                module.parse_origins(value)

    def test_startup_emits_policy_summary_without_origin_values(self):
        output = io.StringIO()
        with contextlib.redirect_stderr(output):
            addons = module.load_addons("https://one.example,https://two.example:8443")

        self.assertEqual(len(addons), 1)
        self.assertIsInstance(addons[0], module.OriginAllowlist)
        startup = output.getvalue().strip()
        record = json.loads(startup.removeprefix("SCOUT_EGRESS_STARTUP "))
        self.assertEqual(record["status"], "ok")
        self.assertEqual(record["origin_count"], 2)
        self.assertNotIn("one.example", startup)
        self.assertNotIn("two.example", startup)

    def test_invalid_startup_policy_is_audited_and_never_falls_back(self):
        output = io.StringIO()
        with contextlib.redirect_stderr(output), self.assertRaises(RuntimeError):
            module.load_addons("https://user:secret@example.com/path")

        startup = output.getvalue().strip()
        record = json.loads(startup.removeprefix("SCOUT_EGRESS_STARTUP "))
        self.assertEqual(record["status"], "error")
        self.assertEqual(record["reason"], "invalid_policy")
        self.assertNotIn("secret", startup)
        # Failure returns no addon; module-level `addons` belongs to the valid
        # import performed by the mitmproxy test harness.

    def test_invalid_private_ip_setting_is_audited_and_never_falls_back(self):
        output = io.StringIO()
        with patch.dict(os.environ, {"SCOUT_ALLOW_PRIVATE_IPS": "sometimes"}), \
                contextlib.redirect_stderr(output), self.assertRaises(RuntimeError):
            module.load_addons("https://example.com")

        startup = output.getvalue().strip()
        record = json.loads(startup.removeprefix("SCOUT_EGRESS_STARTUP "))
        self.assertEqual(record["status"], "error")
        self.assertEqual(record["reason"], "invalid_policy")
        self.assertNotIn("sometimes", startup)

    def test_http_request_hook_allows_and_blocks(self):
        policy = module.OriginAllowlist(module.parse_origins("https://example.com, http://localhost:8080"))
        denied = []
        policy._deny = lambda flow, message: denied.append((flow, message))

        allowed = SimpleNamespace(request=SimpleNamespace(method="GET", pretty_url="http://localhost:8080/path"), response=None)
        blocked = SimpleNamespace(request=SimpleNamespace(method="GET", pretty_url="http://example.com.evil.test/"), response=None)
        policy.request(allowed)
        policy.request(blocked)

        self.assertEqual(allowed.response, None)
        self.assertEqual(len(denied), 1)
        self.assertIs(denied[0][0], blocked)
        decision = json.loads(self.audit_messages[-1].removeprefix("SCOUT_EGRESS_DECISION "))
        self.assertEqual(decision["type"], "scout_egress_decision")
        self.assertFalse(decision["allowed"])
        self.assertEqual(decision["host"], "example.com.evil.test")
        self.assertNotIn("url", decision)

    def test_http_decision_records_effective_default_port(self):
        policy = module.OriginAllowlist(module.parse_origins("http://example.com"))
        flow = SimpleNamespace(request=SimpleNamespace(method="GET", pretty_url="http://example.com/health"), response=None)

        policy.request(flow)

        decision = json.loads(self.audit_messages[-1].removeprefix("SCOUT_EGRESS_DECISION "))
        self.assertEqual(decision["port"], 80)
        self.assertTrue(decision["allowed"])

    def test_redirect_destination_is_checked_as_a_new_request(self):
        policy = module.OriginAllowlist(module.parse_origins("http://trusted.test"))
        denied = []
        policy._deny = lambda flow, message: denied.append((flow, message))

        # mitmproxy invokes request() for the redirected request too; this
        # models the destination after following a 302 from trusted.test.
        redirected = SimpleNamespace(
            request=SimpleNamespace(method="GET", pretty_url="http://denied.test/private"),
            response=None,
        )
        policy.request(redirected)

        self.assertEqual(len(denied), 1)
        self.assertIs(denied[0][0], redirected)
        self.assertEqual(denied[0][1], "blocked origin http://denied.test")
        decision = json.loads(self.audit_messages[-1].removeprefix("SCOUT_EGRESS_DECISION "))
        self.assertEqual(decision["host"], "denied.test")
        self.assertTrue(decision["allowed"] is False)

    def test_connect_hook_checks_https_and_wss_authority(self):
        policy = module.OriginAllowlist(module.parse_origins("https://example.com"))
        denied = []
        policy._deny = lambda flow, message: denied.append((flow, message))

        allowed = SimpleNamespace(request=SimpleNamespace(host="example.com", port=443), response=None)
        blocked = SimpleNamespace(request=SimpleNamespace(host="other.test", port=443), response=None)
        wrong_port = SimpleNamespace(request=SimpleNamespace(host="example.com", port=8443), response=None)
        for flow in (allowed, blocked, wrong_port):
            policy.http_connect(flow)

        self.assertEqual(allowed.response, None)
        self.assertEqual(len(denied), 2)
        self.assertIs(denied[0][0], blocked)
        self.assertIs(denied[1][0], wrong_port)

    def test_audit_failure_denies_request_closed(self):
        fake_mitmproxy = sys.modules["mitmproxy"]
        fake_mitmproxy.ctx.log.info = lambda _message: (_ for _ in ()).throw(OSError("disk full"))
        policy = module.OriginAllowlist(module.parse_origins("https://example.com"))
        denials = []
        policy._deny = lambda flow, message, status=403: denials.append((flow, message, status))
        flow = SimpleNamespace(request=SimpleNamespace(method="GET", pretty_url="https://example.com/private?token=secret"), response=None)

        policy.request(flow)

        self.assertEqual(len(denials), 1)
        self.assertEqual(denials[0][2], 503)
        self.assertNotIn("secret", denials[0][1])

    def test_server_connect_pins_public_dns_answer(self):
        policy = module.OriginAllowlist(module.parse_origins("https://example.com"))
        server = SimpleNamespace(address=("example.com", 443), sni=None, error=None)
        with patch("socket.getaddrinfo", return_value=[(2, 1, 6, "", ("93.184.216.34", 443))]):
            asyncio.run(policy.server_connect(SimpleNamespace(server=server)))

        self.assertEqual(server.address, ("93.184.216.34", 443))
        self.assertEqual(server.sni, "example.com")
        self.assertIsNone(server.error)
        decision = json.loads(self.audit_messages[-1].removeprefix("SCOUT_EGRESS_DECISION "))
        self.assertEqual(decision["event"], "server_connect")
        self.assertEqual(decision["resolved_ip"], "93.184.216.34")
        self.assertTrue(decision["allowed"])

    def test_server_connect_audits_missing_destination_and_fails_closed(self):
        policy = module.OriginAllowlist(module.parse_origins("https://example.com"))
        server = SimpleNamespace(address=None, sni=None, error=None)

        asyncio.run(policy.server_connect(SimpleNamespace(server=server)))

        self.assertEqual(server.error, "blocked because destination address is unavailable")
        decision = json.loads(self.audit_messages[-1].removeprefix("SCOUT_EGRESS_DECISION "))
        self.assertEqual(decision["event"], "server_connect")
        self.assertFalse(decision["allowed"])
        self.assertEqual(decision["reason"], "destination_unavailable")

    def test_server_connect_audits_resolution_failure(self):
        policy = module.OriginAllowlist(module.parse_origins("https://example.com"))
        server = SimpleNamespace(address=("unresolvable.invalid", 443), sni=None, error=None)
        with patch("asyncio.BaseEventLoop.getaddrinfo", side_effect=OSError("resolver unavailable")):
            asyncio.run(policy.server_connect(SimpleNamespace(server=server)))

        self.assertEqual(server.error, "blocked because destination resolution failed")
        decision = json.loads(self.audit_messages[-1].removeprefix("SCOUT_EGRESS_DECISION "))
        self.assertEqual(decision["event"], "server_connect")
        self.assertFalse(decision["allowed"])
        self.assertEqual(decision["reason"], "resolution_failed")

    def test_server_connect_fails_closed_when_resolution_denial_cannot_be_audited(self):
        policy = module.OriginAllowlist(module.parse_origins("https://example.com"))
        server = SimpleNamespace(address=("unresolvable.invalid", 443), sni=None, error=None)
        fake_mitmproxy = sys.modules["mitmproxy"]
        fake_mitmproxy.ctx.log.info = lambda _message: (_ for _ in ()).throw(OSError("audit unavailable"))
        with patch("asyncio.BaseEventLoop.getaddrinfo", side_effect=OSError("resolver unavailable")):
            asyncio.run(policy.server_connect(SimpleNamespace(server=server)))

        self.assertEqual(server.error, "blocked because egress audit failed")

    def test_server_connect_rejects_malformed_dns_answer(self):
        policy = module.OriginAllowlist(module.parse_origins("https://example.com"))
        server = SimpleNamespace(address=("example.com", 443), sni=None, error=None)
        with patch("socket.getaddrinfo", return_value=[(2, 1, 6, "", ("not-an-ip", 443))]):
            asyncio.run(policy.server_connect(SimpleNamespace(server=server)))

        self.assertIsNotNone(server.error)
        decision = json.loads(self.audit_messages[-1].removeprefix("SCOUT_EGRESS_DECISION "))
        self.assertFalse(decision["allowed"])
        self.assertEqual(decision["reason"], "resolution_invalid_address")

    def test_server_connect_rejects_private_or_mixed_dns_answers(self):
        for answers in (
            [(2, 1, 6, "", ("127.0.0.1", 443))],
            [(2, 1, 6, "", ("93.184.216.34", 443)), (2, 1, 6, "", ("10.0.0.4", 443))],
        ):
            with self.subTest(answers=answers):
                policy = module.OriginAllowlist(module.parse_origins("https://example.com"))
                server = SimpleNamespace(address=("example.com", 443), sni=None, error=None)
                with patch("socket.getaddrinfo", return_value=answers):
                    asyncio.run(policy.server_connect(SimpleNamespace(server=server)))

                self.assertIsNotNone(server.error)
                self.assertEqual(server.address, ("example.com", 443))
                decision = json.loads(self.audit_messages[-1].removeprefix("SCOUT_EGRESS_DECISION "))
                self.assertFalse(decision["allowed"])
                self.assertEqual(decision["reason"], "private_peer_denied")

    def test_private_dns_answer_requires_explicit_opt_in(self):
        policy = module.OriginAllowlist(module.parse_origins("http://localhost"), allow_private_ips=True)
        server = SimpleNamespace(address=("localhost", 80), sni=None, error=None)
        with patch("socket.getaddrinfo", return_value=[(2, 1, 6, "", ("127.0.0.1", 80))]):
            asyncio.run(policy.server_connect(SimpleNamespace(server=server)))

        self.assertEqual(server.address, ("127.0.0.1", 80))
        self.assertEqual(server.sni, "localhost")
        self.assertIsNone(server.error)
        decision = json.loads(self.audit_messages[-1].removeprefix("SCOUT_EGRESS_DECISION "))
        self.assertTrue(decision["allowed"])
        self.assertEqual(decision["resolved_ip"], "127.0.0.1")


if __name__ == "__main__":
    unittest.main()
