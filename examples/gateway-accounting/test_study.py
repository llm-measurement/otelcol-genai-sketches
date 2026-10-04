# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex

import http.client
import json
from pathlib import Path
import socket
import struct
import sys
import tempfile
import threading
import time
import unittest
from unittest import mock

import analyze
import client
import gateways
import provider


class Fixtures(unittest.TestCase):
    def test_response_contracts_and_explicit_fault_labels(self):
        self.assertEqual(set(provider.FIXTURE_KINDS), set(provider.CASES))
        for protocol in provider.PROTOCOLS:
            for case in provider.CASES:
                with self.subTest(protocol=protocol, case=case):
                    body = provider.response_body(protocol, case, "study-model")
                    self.assertEqual(body["model"], "study-model")
                    if protocol == "openai":
                        self.assertEqual(body["object"], "chat.completion")
                        self.assertEqual(body["choices"][0]["message"]["role"], "assistant")
                        required = {"prompt_tokens", "completion_tokens", "total_tokens"}
                    else:
                        self.assertEqual(body["type"], "message")
                        self.assertEqual(body["stop_reason"], "end_turn")
                        self.assertIsNone(body["stop_sequence"])
                        required = {"input_tokens", "output_tokens"}
                    if case == "missing":
                        self.assertNotIn("usage", body)
                    elif case.startswith("partial_"):
                        self.assertFalse(required <= body["usage"].keys())
                        self.assertEqual(provider.FIXTURE_KINDS[case], "malformed_usage_fault")
                    else:
                        self.assertTrue(required <= body["usage"].keys())
                        self.assertEqual(body["usage"], provider.native_usage(protocol, case))

    def test_openai_stream_usage_contract(self):
        for case in ("stream_complete", "stream_standard", "zero", "cache_only", "partial_input", "partial_output"):
            with self.subTest(case=case):
                events = list(provider.stream_events("openai", case, "study-model"))
                self.assertTrue(all(event is None for event, _ in events))
                self.assertEqual(events[-1][1], "[DONE]")
                chunks = [value for _, value in events[:-1]]
                self.assertEqual(chunks[-1]["choices"], [])
                self.assertEqual(chunks[-1]["usage"], provider.native_usage("openai", case))
                self.assertEqual(chunks[-2]["choices"][0]["finish_reason"], "stop")
                for chunk in chunks[:-1]:
                    self.assertEqual(chunk["object"], "chat.completion.chunk")
                    self.assertIn("usage", chunk)
                    self.assertIsNone(chunk["usage"])

    def test_anthropic_start_and_cumulative_final_contracts(self):
        for case in ("complete", "stream_standard", "stream_complete", "zero", "cache_only"):
            with self.subTest(case=case):
                events = list(provider.stream_events("anthropic", case, "study-model"))
                self.assertTrue(all(event == value["type"] for event, value in events))
                self.assertEqual(events[0][0], "message_start")
                start = events[0][1]["message"]
                self.assertEqual(start["content"], [])
                self.assertIsNone(start["stop_reason"])
                self.assertIsNone(start["stop_sequence"])
                self.assertEqual(events[-3][0], "content_block_stop")
                self.assertEqual(events[-2][0], "message_delta")
                self.assertEqual(events[-1][0], "message_stop")
                final = events[-2][1]
                self.assertEqual(final["delta"], {"stop_reason": "end_turn", "stop_sequence": None})
                if case == "stream_complete":
                    self.assertTrue(all(value == 0 for value in start["usage"].values()))
                    self.assertEqual(final["usage"], provider.native_usage("anthropic", case))
                else:
                    self.assertEqual(set(final["usage"]), {"output_tokens"})
                    self.assertEqual(start["usage"]["input_tokens"],
                                     provider.native_usage("anthropic", case)["input_tokens"])
                    if case in ("complete", "stream_standard"):
                        self.assertEqual(start["usage"]["output_tokens"], 1)
                        self.assertEqual(final["usage"]["output_tokens"], 20)
                merged = {**start["usage"], **final["usage"]}
                self.assertEqual(merged, provider.native_usage("anthropic", case))

    def test_stream_cut_has_no_finish_usage_or_terminal(self):
        for protocol in provider.PROTOCOLS:
            events = list(provider.stream_events(protocol, "stream_cut", "study-model"))
            if protocol == "openai":
                self.assertFalse(any(value == "[DONE]" for _, value in events))
                self.assertTrue(all(value["usage"] is None for _, value in events))
                self.assertTrue(all(value["choices"][0]["finish_reason"] is None for _, value in events))
            else:
                self.assertFalse({"message_delta", "message_stop", "content_block_stop"} &
                                 {event for event, _ in events})
                self.assertEqual(events[0][1]["message"]["usage"], provider.native_usage(protocol, "zero"))

    def test_missing_final_usage_is_not_a_final_zero(self):
        for protocol in provider.PROTOCOLS:
            missing = list(provider.stream_events(protocol, "stream_missing", "study-model"))
            zero = list(provider.stream_events(protocol, "zero", "study-model"))
            if protocol == "openai":
                self.assertFalse(any(isinstance(v, dict) and isinstance(v.get("usage"), dict) for _, v in missing))
                self.assertEqual(zero[-2][1]["usage"]["total_tokens"], 0)
            else:
                self.assertEqual(missing[0][1]["message"]["usage"], zero[0][1]["message"]["usage"])
                self.assertNotIn("usage", missing[-2][1])
                self.assertEqual(zero[-2][1]["usage"], {"output_tokens": 0})
            self.assertEqual(provider.FIXTURE_KINDS["zero"], "synthetic_zero_boundary")

    def test_zero_and_cache_boundaries_emit_no_text(self):
        for protocol in provider.PROTOCOLS:
            for case in ("zero", "cache_only"):
                body = provider.response_body(protocol, case, "study-model")
                if protocol == "openai":
                    self.assertEqual(body["choices"][0]["message"]["content"], "")
                    deltas = [v["choices"][0]["delta"] for _, v in provider.stream_events(protocol, case, "m")
                              if isinstance(v, dict) and v["choices"]]
                    self.assertFalse(any(d.get("content") for d in deltas))
                else:
                    self.assertEqual(body["content"], [])
                    self.assertFalse(any(event == "content_block_delta" for event, _ in
                                         provider.stream_events(protocol, case, "m")))

    def test_protocol_controls_and_cache_accounting(self):
        for protocol in provider.PROTOCOLS:
            self.assertEqual(analyze.normalize(provider.native_usage(protocol, "complete"), protocol)["input"], 100)
            zero = analyze.normalize(provider.native_usage(protocol, "zero"), protocol)
            self.assertEqual((zero["input"], zero["output"]), (0, 0))
            cache = analyze.normalize(provider.native_usage(protocol, "cache_only"), protocol)
            self.assertEqual(cache, {"input": 64, "output": 0, "cache_read": 64})
            for case, present, absent in (("partial_input", "input", "output"), ("partial_output", "output", "input")):
                usage = analyze.normalize(provider.native_usage(protocol, case), protocol)
                self.assertIsNotNone(usage[present])
                self.assertIsNone(usage[absent])

    def test_exact_cache_union_not_double_counted(self):
        openai = {"prompt_tokens": 100, "completion_tokens": 20, "total_tokens": 120,
                  "prompt_tokens_details": {"cached_tokens": 64}}
        anthropic = {"input_tokens": 10, "output_tokens": 20, "cache_read_input_tokens": 64,
                     "cache_creation_input_tokens": 26,
                     "cache_creation": {"ephemeral_5m_input_tokens": 20, "ephemeral_1h_input_tokens": 6}}
        for protocol, value in (("openai", openai), ("anthropic", anthropic)):
            self.assertEqual(analyze.normalize(value, protocol), {"input": 100, "output": 20, "cache_read": 64})
        for read, write in ((64, 0), (0, 64), (32, 32)):
            value = {"input_tokens": 0, "output_tokens": 0,
                     "cache_read_input_tokens": read, "cache_creation_input_tokens": write}
            self.assertEqual(analyze.normalize(value, "anthropic"), {"input": 64, "output": 0, "cache_read": read})

    def test_missing_and_zero_are_different(self):
        reference = analyze.normalize({"prompt_tokens": 100, "completion_tokens": 20})
        self.assertEqual(analyze.compare(reference, analyze.normalize(None))["input"], "absent")
        self.assertEqual(analyze.compare(reference, analyze.normalize({"prompt_tokens": 0}))["input"], "under_counted")
        self.assertEqual(analyze.compare(analyze.normalize(None), analyze.normalize({"prompt_tokens": 0}))["input"], "reference_unknown")

    def test_cumulative_events_are_not_added(self):
        result = analyze.final_usage([{"usage": {"prompt_tokens": 100, "completion_tokens": 0}},
                                      {"usage": {"completion_tokens": 20}}])
        self.assertEqual(result["input"], 100)
        self.assertEqual(result["output"], 20)

    def test_no_provider_egress_or_host_ports(self):
        with tempfile.TemporaryDirectory() as directory:
            for name in gateways.GATEWAYS:
                for protocol in provider.PROTOCOLS:
                    root = Path(directory) / (name + protocol)
                    root.mkdir()
                    value = gateways.compose(root, name, protocol, "synthetic-test-key")
                    self.assertTrue(value["networks"]["study"]["internal"])
                    for service in value["services"].values():
                        self.assertNotIn("ports", service)
                        self.assertIn("@sha256:", service["image"])
                        self.assertEqual(service["cap_drop"], ["ALL"])
                        self.assertTrue(service["read_only"])
                        self.assertEqual(service["networks"], ["study"])

    def test_client_rejects_remote_endpoints(self):
        for url in ("https://api.openai.com/v1/chat/completions", "http://example.com/", "http://169.254.169.254/"):
            with self.assertRaises(ValueError):
                client.request(url, {})

    def test_fixtures_do_not_connect_or_resolve_network_hosts(self):
        with mock.patch("socket.socket.connect", side_effect=AssertionError("fixture egress")), \
                mock.patch("socket.getaddrinfo", side_effect=AssertionError("fixture DNS")):
            for protocol in provider.PROTOCOLS:
                for case in provider.CASES:
                    provider.response_body(protocol, case, "study-model")
                    list(provider.stream_events(protocol, case, "study-model"))

    def test_unknown_fixture_is_not_silently_anthropic(self):
        for protocol, case in (("responses", "complete"), ("openai", "typo")):
            with self.assertRaises(ValueError):
                provider.native_usage(protocol, case)


class ProviderHTTP(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        original_connect = socket.socket.connect

        def loopback_only(sock, address):
            if address[0] not in ("127.0.0.1", "::1"):
                raise AssertionError("tests may only connect to loopback")
            return original_connect(sock, address)

        guard = mock.patch("socket.socket.connect", loopback_only)
        guard.start()
        self.addCleanup(guard.stop)
        self.server = provider.StudyServer(("127.0.0.1", 0), self.tmp.name)
        self.errors = []
        self.server.handle_error = lambda *_: self.errors.append(sys.exc_info()[1])
        self.thread = threading.Thread(target=self.server.serve_forever, kwargs={"poll_interval": 0.01}, daemon=True)
        self.thread.start()
        self.url = f"http://127.0.0.1:{self.server.server_port}"

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join()
        self.assertEqual(self.errors, [], "unhandled provider errors")

    def call(self, protocol, case, **options):
        self.assertEqual(client.request(self.url + "/control", {"case": case})["status"], 200)
        return client.request(self.url + ("/v1/messages" if protocol == "anthropic" else "/v1/chat/completions"),
                              client.payload(protocol, case, "study-model"), **options)

    def records(self, filename="provider.jsonl"):
        with self.server.lock:
            return analyze.rows(Path(self.tmp.name) / filename)

    def wait_for(self, event, protocol=None, case=None):
        deadline = time.monotonic() + 3
        while time.monotonic() < deadline:
            rows = [row for row in self.records() if row["event"] == event and
                    (protocol is None or row.get("protocol") == protocol) and
                    (case is None or row.get("case") == case)]
            if rows:
                return rows[-1]
            if self.errors:
                self.fail(f"unhandled provider errors: {self.errors}")
            time.sleep(0.01)
        self.fail(f"no {event} observed by the 3-second test deadline")

    def exchange(self, path, body, headers=None, method="POST"):
        conn = http.client.HTTPConnection("127.0.0.1", self.server.server_port, timeout=3)
        try:
            conn.request(method, path, body, headers or {"Content-Type": "application/json"})
            response = conn.getresponse()
            raw = response.read()
            return response.status, dict(response.getheaders()), raw
        finally:
            conn.close()

    def raw_exchange(self, request):
        with socket.create_connection(("127.0.0.1", self.server.server_port), timeout=3) as conn:
            conn.sendall(request)
            conn.shutdown(socket.SHUT_WR)
            with http.client.HTTPResponse(conn) as response:
                response.begin()
                raw = response.read()
                self.assertEqual(int(response.getheader("Content-Length")), len(raw))
                self.assertEqual(response.getheader("Connection"), "close")
                return response.status, raw

    def test_complete_streams_and_cut_streams(self):
        for protocol in provider.PROTOCOLS:
            for case in ("complete", "stream_complete", "stream_standard", "stream_cut", "stream_missing", "missing", "zero",
                         "cache_only", "partial_input", "partial_output"):
                with self.subTest(protocol=protocol, case=case):
                    result = self.call(protocol, case)
                    self.assertEqual(result["status"], 200)
                    self.assertEqual(result["terminal"], case != "stream_cut")
                    self.assertNotIn(provider.SENTINEL, json.dumps(result))
                    self.assertIsNone(result["transport_error"])
                    if case == "missing":
                        self.assertFalse(result["usage_events"])
                    if case in ("stream_cut", "stream_missing"):
                        if protocol == "openai":
                            self.assertFalse(result["usage_events"])
                        else:
                            self.assertEqual(len(result["usage_events"]), 1)
                            self.assertEqual(result["usage_events"][0]["event"], "message_start")

    def test_control_and_health_are_framed_and_reset_without_erasing_evidence(self):
        conn = http.client.HTTPConnection("127.0.0.1", self.server.server_port, timeout=3)
        try:
            for method, path, body, status in (
                ("GET", "/health", None, 200),
                ("POST", "/control", {"case": "retry429"}, 200),
                ("POST", "/v1/chat/completions", {}, 429),
                ("POST", "/v1/chat/completions", {}, 200),
                ("POST", "/control", {"case": "retry429"}, 200),
                ("POST", "/v1/chat/completions", {}, 429),
            ):
                conn.request(method, path, json.dumps(body) if body is not None else None)
                response = conn.getresponse()
                raw = response.read()
                self.assertEqual(response.status, status)
                self.assertEqual(response.getheader("Content-Type"), "application/json")
                self.assertEqual(int(response.getheader("Content-Length")), len(raw))
                self.assertIsInstance(json.loads(raw), dict)
        finally:
            conn.close()
        attempts = [row["attempt"] for row in self.records() if row["event"] == "attempt"]
        self.assertEqual(attempts, [1, 2, 1])

    def test_retry_attempts_are_not_client_retries(self):
        for protocol in provider.PROTOCOLS:
            with self.subTest(protocol=protocol):
                self.assertEqual(self.call(protocol, "retry429")["status"], 429)
                events = [r for r in self.records() if r.get("protocol") == protocol]
                self.assertEqual([e["event"] for e in events], ["attempt"])
                path = "/v1/messages" if protocol == "anthropic" else "/v1/chat/completions"
                result = client.request(self.url + path, client.payload(protocol, "retry429", "study-model"))
                self.assertEqual(result["status"], 200)
                self.wait_for("delivered_usage", protocol)
                events = [r for r in self.records() if r.get("protocol") == protocol]
                self.assertEqual([e["attempt"] for e in events if e["event"] == "attempt"], [1, 2])
                self.assertEqual([e["attempt"] for e in events if e["event"] == "reference"], [2])

    def test_retry_error_envelopes_are_native_and_framed(self):
        for protocol in provider.PROTOCOLS:
            client.request(self.url + "/control", {"case": "retry429"})
            path = "/v1/messages" if protocol == "anthropic" else "/v1/chat/completions"
            status, headers, raw = self.exchange(path, b"{}")
            self.assertEqual(status, 429)
            self.assertEqual(headers["Retry-After"], "0")
            self.assertEqual(int(headers["Content-Length"]), len(raw))
            value = json.loads(raw)
            self.assertEqual(value["error"]["type"], "rate_limit_error")
            if protocol == "anthropic":
                self.assertEqual(value["type"], "error")
            else:
                self.assertEqual(value["error"]["code"], "rate_limit_exceeded")

    def test_client_disconnect_reaches_mock_provider_without_final_usage(self):
        # Direct client -> mock endpoint only; gateway/real-provider propagation
        # requires the separate development matrix, not this test's inference.
        for protocol in provider.PROTOCOLS:
            result = self.call(protocol, "client_disconnect", disconnect=True)
            self.assertTrue(result["client_cancelled"])
            self.assertFalse(result["terminal"])
            self.assertFalse(result["finish_reasons"])
            closed = self.wait_for("downstream_closed", protocol)
            self.assertEqual(closed["case"], "client_disconnect")
            self.assertEqual(closed["attempt"], 1)
            self.assertFalse(closed["terminal_sent"])
            events = [r for r in self.records() if r.get("protocol") == protocol]
            self.assertEqual([r["event"] for r in events], ["attempt", "reference", "downstream_closed"])

    def test_cut_and_missing_record_no_final_usage(self):
        for protocol in provider.PROTOCOLS:
            for case in ("stream_cut", "stream_missing"):
                self.call(protocol, case)
                self.wait_for("stream_end", protocol, case)
                rows = [r for r in self.records() if r.get("protocol") == protocol and r["case"] == case]
                self.assertFalse(any(r["event"] == "delivered_usage" for r in rows))
                self.assertEqual(rows[-1]["event"], "stream_end")
                self.assertEqual(rows[-1]["terminal_sent"], case == "stream_missing")

    def test_anthropic_standard_control_is_distinct_on_wire_and_in_records(self):
        for case, profile in (("stream_standard", "anthropic_standard"),
                              ("stream_complete", "anthropic_compatible_deferred")):
            result = self.call("anthropic", case)
            self.assertEqual(result["status"], 200)
            self.assertTrue(result["terminal"])
            start, final = result["usage_events"]
            self.assertEqual(start["event"], "message_start")
            self.assertEqual(final["event"], "message_delta")
            if case == "stream_standard":
                self.assertEqual(start["usage"]["input_tokens"], 100)
                self.assertEqual(start["usage"]["output_tokens"], 1)
                self.assertEqual(final["usage"], {"output_tokens": 20})
            else:
                self.assertTrue(all(v == 0 for v in start["usage"].values()))
                self.assertEqual(final["usage"]["input_tokens"], 100)
            self.assertEqual({**start["usage"], **final["usage"]}, provider.native_usage("anthropic", case))
            self.wait_for("stream_end", "anthropic", case)
            row = next(r for r in self.records() if r["event"] == "attempt" and r["case"] == case)
            self.assertEqual(row["usage_profile"], profile)
            delivered = [r for r in self.records() if r["event"] == "delivered_usage" and r["case"] == case]
            self.assertEqual(analyze.final_usage(delivered, "anthropic"),
                             analyze.normalize(provider.native_usage("anthropic", case), "anthropic"))
            if case == "stream_standard":
                self.assertEqual(delivered[0]["usage_event"], "message_start")
                self.assertNotIn("output_tokens", delivered[0]["usage"])

    def test_streamed_zero_cache_and_partial_controls_keep_evidence(self):
        for protocol in provider.PROTOCOLS:
            for case in ("zero", "cache_only", "partial_input", "partial_output"):
                with self.subTest(protocol=protocol, case=case):
                    client.request(self.url + "/control", {"case": case})
                    body = client.payload(protocol, case, "study-model")
                    body.update(stream=True, stream_options={"include_usage": True})
                    path = "/v1/messages" if protocol == "anthropic" else "/v1/chat/completions"
                    result = client.request(self.url + path, body)
                    self.assertTrue(result["terminal"])
                    merged = {}
                    for event in result["usage_events"]:
                        merged.update(event["usage"])
                    self.assertEqual(merged, provider.native_usage(protocol, case))
                    self.wait_for("stream_end", protocol, case)
                    attempts = [r for r in self.records() if r["event"] == "attempt" and
                                r["case"] == case and r["protocol"] == protocol]
                    self.assertEqual(attempts[-1]["fixture_kind"], provider.FIXTURE_KINDS[case])
                    if case == "cache_only" and protocol == "anthropic":
                        self.assertEqual(result["usage_events"][0]["usage"]["cache_read_input_tokens"], 64)
                        self.assertEqual(result["usage_events"][-1]["usage"], {"output_tokens": 0})

    def test_openai_include_usage_opt_in_is_honored(self):
        for options in (None, {"include_usage": False}, {"include_usage": True}):
            client.request(self.url + "/control", {"case": "stream_complete"})
            body = client.payload("openai", "stream_complete", "study-model")
            if options is None:
                body.pop("stream_options")
            else:
                body["stream_options"] = options
            status, headers, raw = self.exchange("/v1/chat/completions", json.dumps(body))
            self.assertEqual(status, 200)
            self.assertEqual(headers["Content-Type"], "text/event-stream")
            self.assertEqual(headers["Connection"], "close")
            self.assertNotIn("Content-Length", headers)
            self.assertTrue(raw.endswith(b"data: [DONE]\n\n"))
            chunks = [json.loads(line[6:]) for line in raw.splitlines() if line.startswith(b"data: {")]
            self.assertEqual(sum(isinstance(v.get("usage"), dict) for v in chunks), bool(options and options["include_usage"]))
            if not options or not options["include_usage"]:
                self.assertTrue(all("usage" not in v for v in chunks))

    def test_http_body_bounds_and_bad_framing_close_connection(self):
        cases = [
            (b"", b"", 400),
            (b"Content-Length: -1\r\n", b"", 400),
            (b"Content-Length: x\r\n", b"", 400),
            (b"Content-Length: 0\r\n", b"", 413),
            (f"Content-Length: {provider.MAX_BODY + 1}\r\n".encode(), b"", 413),
            (b"Content-Length: 2\r\nContent-Length: 2\r\n", b"{}", 400),
            (b"Content-Length: 2\r\nTransfer-Encoding: chunked\r\n", b"{}", 400),
            (b"Content-Length: 3\r\n", b"{}", 400),
            (b"Content-Length: 1\r\n", b"{", 400),
        ]
        for headers, body, expected in cases:
            with self.subTest(headers=headers):
                request = b"POST /control HTTP/1.1\r\nHost: localhost\r\n" + headers + b"\r\n" + body
                status, raw = self.raw_exchange(request)
                self.assertEqual(status, expected)
                self.assertEqual(json.loads(raw), {})
        self.assertEqual(self.records(), [])
        self.assertEqual(self.exchange("/v1/chat/completions", b"{}" + b" " * (provider.MAX_BODY - 2))[0], 200)

    def test_invalid_requests_do_not_consume_attempts_or_reset_control(self):
        client.request(self.url + "/control", {"case": "retry429"})
        for path, body, expected in (
            ("/control", {"case": "unknown"}, 400),
            ("/control", [], 400),
            ("/v1/chat/completions", {"model": "x" * 257}, 400),
            ("/v1/chat/completions", {"model": 5}, 400),
            ("/v1/messages", {"stream": "yes"}, 400),
            ("/v1/chat/completions", {"stream_options": {"include_usage": "yes"}}, 400),
            ("/v1/chat/completions", {"stream_options": []}, 400),
            ("/v1/chat/completions", None, 400),
            ("/unexpected/chat/completions", {}, 404),
        ):
            with self.subTest(path=path, body=body):
                self.assertEqual(self.exchange(path, json.dumps(body))[0], expected)
        self.assertEqual(self.records(), [])
        self.assertEqual(self.exchange("/v1/chat/completions", b"{}")[0], 429)
        self.assertEqual([r["attempt"] for r in self.records()], [1])

    def test_no_request_or_response_content_in_provider_records(self):
        body = client.payload("openai", "complete", provider.SENTINEL)
        body.update(api_base="https://example.com", api_key=provider.SENTINEL)
        body["messages"][0]["content"] = provider.SENTINEL
        status, _, raw = self.exchange("/v1/chat/completions", json.dumps(body),
                                       {"Authorization": provider.SENTINEL})
        self.assertEqual(status, 200)
        self.assertIn(provider.SENTINEL, raw.decode())  # Model echo only, never persisted.
        self.wait_for("delivered_usage")
        rows = self.records()
        self.assertNotIn(provider.SENTINEL, json.dumps(rows))
        self.assertEqual(rows[0]["fixture_kind"], "protocol_control")
        self.assertFalse(any({"body", "model", "messages", "content", "headers"} & r.keys() for r in rows))

    def test_telemetry_capture_is_bounded_and_content_free(self):
        secret = provider.SENTINEL
        body = {"resourceSpans": [{"resource": {"attributes": [{"key": secret}]}, "scopeSpans": [{
            "scope": {"name": secret}, "spans": [{"name": secret, "events": [{"name": secret}],
                "status": {"code": 2, "message": secret}, "attributes": [
                    {"key": "gen_ai.prompt", "value": {"stringValue": secret}},
                    {"key": "gen_ai.usage.input_tokens", "value": {"intValue": "0", "stringValue": secret}},
                    {"key": "gen_ai.usage.output_tokens", "value": {"stringValue": secret}},
                    {"key": "gen_ai_sketch.usage.input.provenance", "value": {"stringValue": "provider_reported"}},
                    {"key": "gen_ai_sketch.usage.output.provenance", "value": {"stringValue": secret}},
                ]}]}]}]}
        self.assertEqual(self.exchange("/v1/traces", json.dumps(body))[0], 200)
        rows = self.records("telemetry.jsonl")
        self.assertNotIn(secret, json.dumps(rows))
        span = rows[0]["body"]["resourceSpans"][0]["scopeSpans"][0]["spans"][0]
        self.assertEqual(span, {"status": {"code": 2}, "attributes": [
            {"key": "gen_ai.usage.input_tokens", "value": {"intValue": "0"}},
            {"key": "gen_ai_sketch.usage.input.provenance", "value": {"stringValue": "provider_reported"}},
        ]})
        request = (f"POST /v1/traces HTTP/1.1\r\nHost: localhost\r\n"
                   f"Content-Length: {provider.MAX_TELEMETRY_BODY + 1}\r\n\r\n").encode()
        self.assertEqual(self.raw_exchange(request)[0], 413)
        for malformed in ([], {"resourceSpans": None}, {"resourceSpans": [1]}):
            self.assertEqual(self.exchange("/v1/traces", json.dumps(malformed))[0], 400)
        self.assertEqual(len(self.records("telemetry.jsonl")), 1)

    def test_idle_keepalive_reset_does_not_raise_traceback(self):
        closed = threading.Event()
        close_request = self.server.close_request

        def observe_close(request):
            close_request(request)
            closed.set()

        with mock.patch.object(self.server, "close_request", side_effect=observe_close):
            with socket.create_connection(("127.0.0.1", self.server.server_port), timeout=3) as conn:
                conn.sendall(b"GET /health HTTP/1.1\r\nHost: localhost\r\n\r\n")
                with http.client.HTTPResponse(conn) as response:
                    response.begin()
                    self.assertEqual(response.read(), b"{}")
                conn.setsockopt(socket.SOL_SOCKET, socket.SO_LINGER, struct.pack("ii", 1, 0))
            self.assertTrue(closed.wait(3), "reset connection handler did not exit")
        self.assertEqual(self.exchange("/health", None, method="GET")[0], 200)
        self.assertEqual(self.errors, [])

    def test_telemetry_lineage_is_keyed_private_and_supports_leaf_selection(self):
        trace, parent, child, old = "a" * 32, "b" * 16, "c" * 16, "d" * 16
        start = time.time_ns()
        usage = [{"key": "gen_ai.usage.input_tokens", "value": {"intValue": "100"}},
                 {"key": "gen_ai.usage.output_tokens", "value": {"intValue": "20"}}]
        spans = [
            {"traceId": trace, "spanId": parent, "startTimeUnixNano": str(start),
             "endTimeUnixNano": start + 1, "attributes": usage},
            {"traceId": trace, "spanId": old, "startTimeUnixNano": str(start - 1), "attributes": usage},
            {"traceId": trace, "spanId": child, "parentSpanId": parent,
             "startTimeUnixNano": str(start), "endTimeUnixNano": start + 1, "attributes": usage},
        ]
        # A parent and child can arrive in different collector batches.
        for batch in (spans[:2], spans[2:]):
            body = {"resourceSpans": [{"scopeSpans": [{"spans": batch}]}]}
            self.assertEqual(self.exchange("/v1/traces", json.dumps(body))[0], 200)
        rows = self.records("telemetry.jsonl")
        serialized = json.dumps(rows)
        for identifier in (trace, parent, child, old):
            self.assertNotIn(identifier, serialized)
        first = rows[0]["body"]["resourceSpans"][0]["scopeSpans"][0]["spans"][0]
        last = rows[1]["body"]["resourceSpans"][0]["scopeSpans"][0]["spans"][0]
        self.assertEqual(first["traceId"], last["traceId"])
        self.assertEqual(first["spanId"], last["parentSpanId"])
        self.assertNotEqual(first["spanId"], last["spanId"])
        self.assertRegex(first["traceId"], r"^[0-9a-f]{64}$")
        self.assertEqual(first["startTimeUnixNano"], str(start))
        self.assertEqual(first["endTimeUnixNano"], start + 1)
        records = analyze.span_records(Path(self.tmp.name) / "telemetry.jsonl", start, time.time_ns())
        self.assertEqual(len(records), 2)  # The older span is excluded by start time.
        self.assertEqual([r["selection_role"] for r in records], ["usage_ancestor", "usage_leaf"])
        self.assertEqual([r["selected_for_usage"] for r in records], [False, True])
        with provider.StudyServer(("127.0.0.1", 0), Path(self.tmp.name) / "another-server") as other:
            self.assertNotEqual(self.server.lineage_key, other.lineage_key)
        self.assertNotIn(self.server.lineage_key.hex(), serialized)

    def test_telemetry_timestamps_validate_uint64_without_bool_or_text(self):
        valid = [0, "0", 2**64 - 1, str(2**64 - 1)]
        invalid = [True, False, -1, "-1", "+1", 1.5, "1e9", "x", "", None, 2**64, "9" * 21]
        spans = [{"startTimeUnixNano": stamp, "endTimeUnixNano": stamp} for stamp in valid + invalid]
        body = {"resourceSpans": [{"scopeSpans": [{"spans": spans}]}]}
        self.assertEqual(self.exchange("/v1/traces", json.dumps(body))[0], 200)
        captured = self.records("telemetry.jsonl")[0]["body"]["resourceSpans"][0]["scopeSpans"][0]["spans"]
        for expected, span in zip(valid, captured):
            self.assertEqual(span["startTimeUnixNano"], expected)
            self.assertEqual(span["endTimeUnixNano"], expected)
        for span in captured[len(valid):]:
            self.assertNotIn("startTimeUnixNano", span)
            self.assertNotIn("endTimeUnixNano", span)


if __name__ == "__main__":
    unittest.main()
