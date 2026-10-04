# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex

from contextlib import nullcontext
import http.client
import io
import json
import os
from pathlib import Path
import tempfile
import threading
import time
import unittest
from unittest.mock import patch

import client
import gateways
import live
import driver
import analyze


def body(stream=False):
    return {"model": live.MODEL, "messages": [{"role": "user", "content":
            "Write the numbers from 1 through 200, one number per line. No other text."}],
            "max_completion_tokens": 512, "reasoning_effort": "none", "stream": stream}


def config(mode="drain", cap=12):
    return {"model": live.MODEL, "max_estimated_usd": "10", "max_provider_attempts": cap, "cut_mode": mode}


class LiveGuards(unittest.TestCase):
    def test_only_fixed_synthetic_requests(self):
        self.assertFalse(live.checked_body(body())["store"])
        for change in ({"model": "unapproved"}, {"max_completion_tokens": 513},
                       {"n": 2}, {"tools": [{}]}, {"service_tier": "priority"},
                       {"messages": [{"role": "user", "content": "private data"}]}):
            with self.subTest(change=change), self.assertRaises(ValueError):
                live.checked_body({**body(), **change})
        value = body()
        value["max_tokens"] = value.pop("max_completion_tokens")
        self.assertEqual(live.checked_body(value)["max_completion_tokens"], 512)
        value["max_completion_tokens"] = 8
        with self.assertRaises(ValueError):
            live.checked_body(value)

    def test_credential_not_echoed_or_left_in_environment(self):
        secret = "sk-" + "synthetic" * 5
        with patch.dict(os.environ, {"OPENAI_API_KEY": secret}):
            self.assertEqual(live.credential("not-read"), secret)
            self.assertNotIn("OPENAI_API_KEY", os.environ)
        with tempfile.TemporaryDirectory() as directory, patch.dict(os.environ, {}, clear=True):
            path = Path(directory) / "key"
            path.write_text("OPENAI_API_KEY=" + secret)
            self.assertEqual(live.credential(path), secret)
            path.write_text(secret + "\n" + secret + "2")
            with self.assertRaisesRegex(ValueError, "expected one provider credential"):
                live.credential(path)

    def test_key_and_egress_only_reach_relay(self):
        with tempfile.TemporaryDirectory() as directory:
            settings = {**config(), "key_file": Path("/external/test-key")}
            data = gateways.compose(Path(directory), "litellm", "openai", "synthetic", settings)
            for name, service in data["services"].items():
                self.assertNotIn("ports", service)
                if name == "provider":
                    self.assertEqual(service["secrets"], ["provider_key"])
                    self.assertIn("egress", service["networks"])
                else:
                    self.assertNotIn("secrets", service)
                    self.assertNotIn("egress", service["networks"])
            self.assertNotIn("key_file", json.loads((Path(directory) / "live.json").read_text()))
        for protocol in ("openai", "anthropic"):
            settings = gateways.configuration("bifrost", protocol, "synthetic")
            self.assertEqual(settings["providers"][protocol]["network_config"]["base_url"], "http://provider:8080")

    def test_empty_bifrost_usage_is_missing_not_a_reader_failure(self):
        row = analyze.bifrost_records({"records": [{"token_usage": "", "prompt_tokens": 0,
                                                   "completion_tokens": 0, "status": "error"}]})[0]
        self.assertIsNone(row["usage"]["input"])
        self.assertEqual(row["column_usage"]["input"], 0)
        self.assertEqual(row["status"], "error")

    def test_driver_waits_for_idle_and_rejects_guard_denial(self):
        with patch("driver.http.client.HTTPConnection") as connection, patch("driver.time.sleep"):
            response = connection.return_value.getresponse.return_value
            response.status = 409
            with self.assertRaisesRegex(RuntimeError, "rejected or failed"):
                driver.wait_live_idle()
            response.status = 200
            driver.wait_live_idle()
            with self.assertRaisesRegex(RuntimeError, "drain deadline"):
                driver.wait_live_idle(timeout=0)

    def test_durable_unknown_reservations_and_attempt_cap(self):
        with tempfile.TemporaryDirectory() as directory:
            server = live.LiveServer(("127.0.0.1", 0), directory, config(cap=1), "synthetic")
            try:
                server.reserve(b"x", body())
                with self.assertRaisesRegex(ValueError, "attempt cap"):
                    server.reserve(b"x", body())
            finally:
                server.server_close()
            second = live.LiveServer(("127.0.0.1", 0), directory, config(cap=1), "synthetic")
            try:
                self.assertGreater(live.guards.budget_used(second.history), 0)
                with self.assertRaisesRegex(ValueError, "attempt cap"):
                    second.reserve(b"x", body())
            finally:
                second.server_close()

    def test_budget_fail_closed(self):
        with tempfile.TemporaryDirectory() as directory:
            for budget in ("0", "NaN", "Infinity", "41"):
                with self.assertRaises(ValueError):
                    live.LiveServer(("127.0.0.1", 0), directory, {**config(), "max_estimated_usd": budget}, "synthetic")
            server = live.LiveServer(("127.0.0.1", 0), directory,
                                     {**config(), "max_estimated_usd": "0.000001"}, "synthetic")
            try:
                with self.assertRaisesRegex(ValueError, "budget reached"):
                    server.reserve(b"x", body())
            finally:
                server.server_close()


class SlowStream(io.BytesIO):
    def readline(self, *args):
        time.sleep(0.01)
        return super().readline(*args)


class LiveRelayHTTP(unittest.TestCase):
    def exercise(self, case, mode="drain", shutdown_error=False, early_eof=False):
        streamed = case.startswith("stream_") or case == "client_disconnect"
        result = {"model": live.MODEL, "usage": {"prompt_tokens": 40, "completion_tokens": 80, "total_tokens": 120},
                  "choices": [{"finish_reason": "stop", "message": {"content": "synthetic"}}]}
        raw = json.dumps(result).encode()
        if streamed:
            delta = b'data: {"choices":[{"delta":{"content":"x"}}]}\n\n'
            raw = delta * 25 + b"data: " + raw + b"\n\ndata: [DONE]\n\n"
            if early_eof:
                raw = delta * 4
        response = SlowStream(raw)
        with tempfile.TemporaryDirectory() as directory:
            server = live.LiveServer(("127.0.0.1", 0), directory, config(mode), "synthetic")
            server.case = case
            thread = threading.Thread(target=server.serve_forever, kwargs={"poll_interval": 0.01}, daemon=True)
            thread.start()
            try:
                shutdown = patch("socket.socket.shutdown", side_effect=OSError("already closed")) if shutdown_error else nullcontext()
                with patch("live.urllib.request.build_opener") as opener, shutdown:
                    opener.return_value.open.return_value = response
                    url = "http://127.0.0.1:%d/v1/chat/completions" % server.server_port
                    seen = client.request(url, body(streamed), disconnect=case == "client_disconnect")
                    if case == "stream_cut" and mode == "drain" and not shutdown_error:
                        conn = http.client.HTTPConnection("127.0.0.1", server.server_port)
                        conn.request("GET", "/idle")
                        self.assertEqual(conn.getresponse().status, 202)
                        conn.close()
                    for _ in range(200):
                        if response.closed:
                            break
                        time.sleep(0.01)
                    if early_eof:
                        conn = http.client.HTTPConnection("127.0.0.1", server.server_port)
                        conn.request("GET", "/idle")
                        self.assertEqual(conn.getresponse().status, 409)
                        conn.close()
                    request = opener.return_value.open.call_args.args[0]
                    self.assertEqual(request.full_url, "https://api.openai.com/v1/chat/completions")
                    self.assertIn("Authorization", request.headers)
                    rows = [json.loads(line) for line in (Path(directory) / "provider.jsonl").read_text().splitlines()]
                    self.assertNotIn("synthetic", json.dumps(rows))
                    return seen, rows
            finally:
                server.shutdown()
                server.server_close()
                thread.join(2)

    def test_control_and_removed_usage_keep_reference(self):
        for case in ("complete", "missing", "stream_complete", "stream_missing"):
            with self.subTest(case=case):
                seen, rows = self.exercise(case)
                self.assertEqual(seen["status"], 200)
                self.assertTrue(seen["terminal"])
                self.assertEqual(bool(seen["usage_events"]), "missing" not in case)
                self.assertEqual(len([r for r in rows if r["event"] == "reference"]), 1)

    def test_drain_and_propagated_cancel_are_distinct(self):
        seen, rows = self.exercise("stream_cut", "drain")
        self.assertFalse(seen["terminal"])
        self.assertTrue(any(r["event"] == "reference" for r in rows))
        for case in ("stream_cut", "client_disconnect"):
            with self.subTest(case=case):
                seen, rows = self.exercise(case, "cancel")
                self.assertFalse(any(r["event"] == "reference" for r in rows))
                self.assertTrue(any(r["event"] == "upstream_closed_by_relay" for r in rows))

    def test_drain_continues_when_downstream_already_closed(self):
        _, rows = self.exercise("stream_cut", shutdown_error=True)
        self.assertTrue(any(r["event"] == "reference" for r in rows))
        self.assertTrue(any(r["event"] == "stream_end" and r["upstream_drained"] for r in rows))

    def test_early_eof_is_not_terminal_delivery(self):
        seen, rows = self.exercise("stream_complete", early_eof=True)
        self.assertFalse(seen["terminal"])
        ending = next(r for r in rows if r["event"] == "stream_end")
        self.assertFalse(ending["terminal_sent"])
        self.assertFalse(ending["terminal_received"])

    def test_guard_rejection_is_not_a_provider_attempt_or_completed_pair(self):
        with tempfile.TemporaryDirectory() as directory:
            server = live.LiveServer(("127.0.0.1", 0), directory,
                                     {**config(), "max_estimated_usd": "0.000001"}, "synthetic")
            server.case = "missing"
            thread = threading.Thread(target=server.serve_forever, kwargs={"poll_interval": 0.01}, daemon=True)
            thread.start()
            try:
                with patch("live.urllib.request.build_opener") as opener:
                    result = client.request("http://127.0.0.1:%d/v1/chat/completions" % server.server_port, body())
                    self.assertEqual(result["status"], 403)
                    opener.assert_not_called()
                conn = http.client.HTTPConnection("127.0.0.1", server.server_port)
                conn.request("GET", "/idle")
                self.assertEqual(conn.getresponse().status, 409)
                conn.close()
                rows = [json.loads(line) for line in (Path(directory) / "provider.jsonl").read_text().splitlines()]
                self.assertTrue(any(r["event"] == "guard_rejected" for r in rows))
                self.assertFalse(any(r["event"] == "provider_forward" for r in rows))
            finally:
                server.shutdown()
                server.server_close()
                thread.join(2)

    def test_incomplete_upstream_body_fails_idle(self):
        with tempfile.TemporaryDirectory() as directory:
            server = live.LiveServer(("127.0.0.1", 0), directory, config(), "synthetic")
            thread = threading.Thread(target=server.serve_forever, kwargs={"poll_interval": 0.01}, daemon=True)
            thread.start()
            try:
                with patch("live.urllib.request.build_opener") as opener:
                    response = opener.return_value.open.return_value.__enter__.return_value
                    response.read.side_effect = http.client.IncompleteRead(b"", 10)
                    result = client.request("http://127.0.0.1:%d/v1/chat/completions" % server.server_port, body())
                    self.assertEqual(result["status"], 502)
                    self.assertTrue(server.failed)
                conn = http.client.HTTPConnection("127.0.0.1", server.server_port)
                conn.request("GET", "/idle")
                self.assertEqual(conn.getresponse().status, 409)
                conn.close()
            finally:
                server.shutdown()
                server.server_close()
                thread.join(2)


if __name__ == "__main__":
    unittest.main()
