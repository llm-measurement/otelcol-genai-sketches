# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Local-only native client contracts; no provider credentials or model calls."""

import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import anthropic_protocol
import client
import driver
import gateways
import provider


class NativeOptions(unittest.TestCase):
    def test_endpoints_models_headers_and_default_openai(self):
        paths = {"litellm": "/v1/messages", "bifrost": "/anthropic/v1/messages",
                 "agentgateway": "/v1/messages", "portkey": "/v1/messages"}
        for name in gateways.GATEWAYS:
            with self.subTest(gateway=name):
                default = gateways.request_options(name, "anthropic", "complete", "local-master")
                self.assertEqual(default, gateways.request_options(
                    name, "anthropic", "complete", "local-master", client_protocol="openai"))
                self.assertTrue(default[0].endswith("/chat/completions"))
                url, headers, model = gateways.request_options(
                    name, "anthropic", "stream_standard", "local-master", anthropic_protocol.MODEL, "anthropic")
                self.assertEqual(url, f"http://gateway:{gateways.PORTS[name]}{paths[name]}")
                expected = "study-model" if name == "litellm" else (
                    "anthropic/" if name == "bifrost" else "") + anthropic_protocol.MODEL
                self.assertEqual(model, expected)
                self.assertEqual(headers["anthropic-version"], "2023-06-01")
                if name == "litellm":
                    self.assertEqual(headers["x-api-key"], "local-master")
                    self.assertNotIn("Authorization", headers)
                elif name == "portkey":
                    self.assertEqual(headers["x-portkey-provider"], "anthropic")
                    self.assertEqual(headers["x-portkey-custom-host"], "http://provider:8080/v1")
                    self.assertEqual(headers["x-api-key"], "synthetic-provider-key")

    def test_unsupported_is_explicit_and_unknown_protocol_fails_closed(self):
        for name in ("bifrost", "agentgateway", "portkey"):
            with self.subTest(gateway=name), self.assertRaisesRegex(NotImplementedError, "not_supported_by_study"):
                gateways.request_options(name, "openai", "complete", "local", client_protocol="anthropic")
        with self.assertRaises(ValueError):
            gateways.request_options("litellm", "anthropic", "complete", "local", client_protocol="typo")
        with self.assertRaises(ValueError):
            client.payload("typo", "complete", "model")

    def test_native_mock_body_has_required_limit_without_openai_options(self):
        for case in ("complete", "stream_standard", "stream_cut", "client_disconnect"):
            body = client.payload("anthropic", case, "model")
            self.assertEqual(body["max_tokens"], 128)
            self.assertEqual(body["model"], "model")
            self.assertNotIn("stream_options", body)
            self.assertEqual(body["stream"], case != "complete")
        self.assertEqual(client.payload("openai", "stream_standard", "model")["stream_options"],
                         {"include_usage": True})

    def test_direct_agentgateway_backend_selects_native_route(self):
        config = gateways.configuration("agentgateway", "anthropic", "local")
        route = config["binds"][0]["listeners"][0]["routes"][0]
        self.assertEqual(route["policies"]["ai"]["routes"],
                         {"/v1/messages": "messages", "*": "completions"})
        self.assertEqual(route["backends"][0]["ai"]["pathOverride"], "/v1/messages")

    def test_live_relay_is_the_only_secret_and_egress_recipient(self):
        settings = {"provider": "anthropic", "model": anthropic_protocol.MODEL,
                    "client_protocol": "anthropic", "cut_mode": "drain", "max_estimated_usd": "1",
                    "max_provider_attempts": 12, "key_file": "/nonexistent/test-only-key",
                    "cache_nonce": "a" * 32}
        for name in gateways.GATEWAYS:
            with self.subTest(gateway=name), tempfile.TemporaryDirectory() as directory, patch.dict(
                    os.environ, {"ANTHROPIC_API_KEY": "ENVIRONMENT_MUST_NOT_BE_COPIED"}):
                data = gateways.compose(directory, name, "anthropic", "local-master", settings)
                self.assertEqual(data["services"]["provider"]["command"],
                                 ["python", "-B", "/study/live_anthropic.py"])
                self.assertEqual(data["secrets"]["provider_key"]["file"], settings["key_file"])
                for service_name, service in data["services"].items():
                    self.assertNotIn("ports", service)
                    if service_name == "provider":
                        self.assertEqual(service["secrets"], ["provider_key"])
                        self.assertEqual(service["networks"], ["study", "egress"])
                    else:
                        self.assertNotIn("secrets", service)
                        self.assertEqual(service["networks"], ["study"])
                        self.assertFalse(any(":/trial:" in volume for volume in service["volumes"]))
                self.assertNotIn("ENVIRONMENT_MUST_NOT_BE_COPIED", json.dumps(data))
                config = json.loads((Path(directory) / "live.json").read_text())
                self.assertEqual(config, {k: v for k, v in settings.items() if k != "key_file"})
                if name == "bifrost":
                    catalog = json.loads((Path(directory) / "catalog.json").read_text())
                    self.assertEqual(catalog[anthropic_protocol.MODEL]["provider"], "anthropic")
        with tempfile.TemporaryDirectory() as directory, self.assertRaises(ValueError):
            gateways.compose(directory, "litellm", "openai", "local", settings)


class NativeDriver(unittest.TestCase):
    def exercise(self, name="litellm", case="complete", live=None, client_protocol="anthropic", protocol="anthropic"):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "master-key").write_text("local-master")
            if live:
                (root / "live.json").write_text(json.dumps(live))
            args = ["driver.py", name, protocol, case, "--client-protocol", client_protocol, "--settle-seconds", "0"]
            response = {"status": 200, "terminal": True, "usage_events": [], "finish_reasons": []}
            with patch("sys.argv", args), patch("driver.Path", return_value=root), \
                    patch("driver.client.request", return_value=response) as request, \
                    patch("driver.wait_live_idle") as idle, patch("driver.time.sleep"), \
                    patch("builtins.print"), patch("driver.PortkeyLogs"), \
                    patch("driver.bifrost_records", return_value=[]):
                driver.main()
            return json.loads((root / (case + ".json")).read_text()), request.call_args_list, idle.call_count

    def test_mock_native_protocol_reaches_request_and_record(self):
        result, calls, idle = self.exercise(case="stream_standard")
        self.assertEqual(result["client_protocol"], "anthropic")
        self.assertEqual(calls[0].args, ("http://provider:8080/control", {"case": "stream_standard"}))
        self.assertEqual(calls[1].args[0], "http://gateway:4000/v1/messages")
        self.assertNotIn("stream_options", calls[1].args[1])
        self.assertEqual(idle, 0)

    def test_live_bodies_delegate_to_shared_contract_for_both_clients(self):
        for protocol in ("openai", "anthropic"):
            settings = {"provider": "anthropic", "model": anthropic_protocol.MODEL,
                        "client_protocol": protocol, "cache_nonce": "a" * 32}
            bodies = []
            for case in ("stream_standard", "cache_write", "cache_read"):
                with self.subTest(protocol=protocol, case=case):
                    result, calls, idle = self.exercise(case=case, live=settings, client_protocol=protocol)
                    body = calls[1].args[1]
                    self.assertEqual(body, anthropic_protocol.request_body("study-model", case, protocol, "a" * 32))
                    self.assertEqual(body["max_tokens"], 512)
                    self.assertNotIn("max_completion_tokens", body)
                    self.assertNotIn("reasoning_effort", body)
                    self.assertEqual(result["client_protocol"], protocol)
                    self.assertEqual(idle, 1)
                    if case.startswith("cache_"):
                        bodies.append(body)
            self.assertEqual(bodies[0], bodies[1])
            self.assertEqual(bodies[0]["messages"][0]["content"][0]["cache_control"], {"type": "ephemeral"})
        self.assertNotIn("cache_write", provider.CASES)
        self.assertNotIn("cache_read", provider.CASES)

    def test_existing_live_openai_body_is_unchanged(self):
        _, calls, idle = self.exercise(live={"model": "test-openai-model"},
                                       client_protocol="openai", protocol="openai")
        body = calls[1].args[1]
        self.assertEqual(body["max_completion_tokens"], 512)
        self.assertEqual(body["reasoning_effort"], "none")
        self.assertEqual(body["service_tier"], "default")
        self.assertFalse(body["store"])
        self.assertEqual(idle, 1)

    def test_agentgateway_uses_its_supported_translated_cache_hint(self):
        for protocol in ("openai", "anthropic"):
            for case in ("cache_write", "cache_read"):
                settings = {"provider": "anthropic", "model": anthropic_protocol.MODEL,
                            "client_protocol": protocol, "cache_nonce": "b" * 32}
                _, calls, _ = self.exercise(name="agentgateway", case=case, live=settings,
                                             client_protocol=protocol)
                prefix = calls[1].args[1]["messages"][0]["content"][0]
                expected = anthropic_protocol.request_body("study-model", case, protocol, "b" * 32)
                self.assertEqual(prefix["text"], expected["messages"][0]["content"][0]["text"])
                if protocol == "openai":
                    self.assertEqual(prefix["prompt_cache_breakpoint"], {"mode": "explicit"})
                    self.assertNotIn("cache_control", prefix)
                else:
                    self.assertEqual(prefix["cache_control"], {"type": "ephemeral"})
                    self.assertNotIn("prompt_cache_breakpoint", prefix)

    def test_unsupported_records_no_request_or_gateway_fault(self):
        result, calls, idle = self.exercise(name="portkey", protocol="openai")
        self.assertEqual(result["status"], "unsupported")
        self.assertFalse(result["gateway_fault"])
        self.assertEqual(result["logical_requests"], 0)
        self.assertEqual(calls, [])
        self.assertEqual(idle, 0)

    def test_cache_mock_and_mismatched_live_protocols_fail_before_requests(self):
        with patch("sys.stderr", new_callable=io.StringIO):
            with self.assertRaises(SystemExit):
                self.exercise(case="cache_write")
            with self.assertRaises(SystemExit):
                self.exercise(live={"provider": "anthropic", "client_protocol": "openai"})


class NativeUsage(unittest.TestCase):
    def receive(self, raw, status=200, stream=True, disconnect=False):
        response = io.BytesIO(raw)
        response.status = status
        response.getheader = lambda *_: "text/event-stream" if stream else "application/json"
        with patch("client.http.client.HTTPConnection") as connection:
            connection.return_value.getresponse.return_value = response
            return client.request("http://gateway:4000/v1/messages", {}, disconnect=disconnect)

    def events(self, values, **options):
        raw = b"".join(b"data: " + json.dumps(value).encode() + b"\n\n" for value in values)
        return self.receive(raw, **options)

    def test_initial_final_usage_and_terminal_marker_are_separate(self):
        start = {"type": "message_start", "message": {"content": [{"text": "PRIVATE"}],
                 "usage": {"input_tokens": 10, "output_tokens": 0, "cache_read_input_tokens": 50,
                           "cache_creation_input_tokens": 20}}}
        delta = {"type": "message_delta", "delta": {"stop_reason": "end_turn"}, "usage": {"output_tokens": 7}}
        for values, terminal, phases in (([start], False, ["initial"]),
                ([start, delta], False, ["initial", "final"]),
                ([start, delta, {"type": "message_stop"}], True, ["initial", "final"])):
            result = self.events(values)
            self.assertEqual(result["terminal"], terminal)
            self.assertEqual([e["phase"] for e in result["usage_events"]], phases)
            self.assertEqual(result["usage_events"][0]["usage"], start["message"]["usage"])
            self.assertNotIn("PRIVATE", json.dumps(result))

    def test_native_nonstream_zero_partial_and_cache_counts(self):
        for usage in ({"input_tokens": 0, "output_tokens": 0}, {"input_tokens": 10},
                      {"output_tokens": 5}, {"input_tokens": 0, "output_tokens": 0,
                       "cache_read_input_tokens": 100, "cache_creation_input_tokens": 0}):
            result = self.receive(json.dumps({"type": "message", "usage": usage,
                                             "stop_reason": "end_turn"}).encode(), stream=False)
            self.assertTrue(result["terminal"])
            self.assertEqual(result["usage_events"], [{"event": "message", "phase": "final", "usage": usage}])

    def test_errors_preserve_status_but_not_error_text(self):
        error = {"type": "error", "error": {"type": "rate_limit_error", "message": "PRIVATE"}}
        for stream, status in ((True, 200), (False, 429)):
            result = self.events([error, {"type": "message_stop"}]) if stream else self.receive(
                json.dumps(error).encode(), status=status, stream=False)
            self.assertEqual(result["status"], status)
            self.assertTrue(result["error_present"])
            self.assertEqual(result["error_type"], "rate_limit_error")
            self.assertFalse(result["terminal"])
            self.assertNotIn("PRIVATE", json.dumps(result))

    def test_unknown_fields_and_malformed_usage_are_redacted_not_zero(self):
        result = self.events([{"type": "PRIVATE", "choices": None,
            "stop_reason": "PRIVATE", "usage": {"output_tokens": "PRIVATE", "input_tokens": True,
                "cache_creation": {"ephemeral_5m_input_tokens": 3, "secret": "PRIVATE"},
                "extra": "PRIVATE"}, "error": {"type": "PRIVATE", "message": "PRIVATE"}}])
        self.assertNotIn("PRIVATE", json.dumps(result))
        usage = result["usage_events"][0]["usage"]
        self.assertIsNone(usage["input_tokens"])
        self.assertIsNone(usage["output_tokens"])
        self.assertEqual(usage["cache_creation"], {"ephemeral_5m_input_tokens": 3})

    def test_sse_event_name_multiline_data_and_incomplete_json(self):
        result = self.receive(b'event: message_delta\ndata: {"usage":{"output_tokens":3},\n'
                              b'data: "delta":{"stop_reason":"end_turn"}}\n\n'
                              b'event: message_stop\ndata: {}\n\n')
        self.assertTrue(result["terminal"])
        self.assertEqual(result["usage_events"][0]["event"], "message_delta")
        for raw, error in ((b'data: PRIVATE\n\n', "non_json_stream_event"),
                           (b'data: {"type":"message_stop"}\n', "incomplete_sse_event")):
            result = self.receive(raw)
            self.assertFalse(result["terminal"])
            self.assertEqual(result["transport_error"], error)
            self.assertNotIn("PRIVATE", json.dumps(result))

    def test_client_disconnect_keeps_initial_usage_and_is_not_final(self):
        values = [{"type": "message_start", "message": {"usage": {"input_tokens": 10, "output_tokens": 0}}},
                  {"type": "content_block_start"}, {"type": "content_block_delta"},
                  {"type": "message_delta", "usage": {"output_tokens": 8}, "delta": {"stop_reason": "end_turn"}},
                  {"type": "message_stop"}]
        result = self.events(values, disconnect=True)
        self.assertTrue(result["client_cancelled"])
        self.assertFalse(result["terminal"])
        self.assertEqual([event["phase"] for event in result["usage_events"]], ["initial"])


if __name__ == "__main__":
    unittest.main()
