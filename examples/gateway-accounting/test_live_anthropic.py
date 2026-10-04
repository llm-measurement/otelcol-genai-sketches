# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex

from decimal import Decimal
import io
import json
from pathlib import Path
import tempfile
import threading
import time
import unittest
from unittest.mock import patch

import anthropic_protocol as protocol
import client
import live_anthropic as live
from test_live import SlowStream

NONCE = "a" * 32


def config(mode="drain", cap=12):
    return {"provider": "anthropic", "model": protocol.MODEL, "cache_nonce": NONCE,
            "max_estimated_usd": "10", "max_provider_attempts": cap, "cut_mode": mode}


class AnthropicGuards(unittest.TestCase):
    def test_fixed_synthetic_request_only(self):
        for case in protocol.LIVE_CASES:
            value = protocol.request_body(protocol.MODEL, case, "anthropic", NONCE)
            self.assertEqual(protocol.checked_body(value, case, NONCE), value)
        value = protocol.request_body(protocol.MODEL, "complete", "anthropic", NONCE)
        for change in ({"model": "other"}, {"max_tokens": True}, {"max_tokens": 513},
                       {"tools": []}, {"stream": "yes"}, {"system": "private"},
                       {"messages": [{"role": "user", "content": "private"}]}):
            with self.subTest(change=change), self.assertRaises(ValueError):
                protocol.checked_body({**value, **change}, "complete", NONCE)

    def test_cache_pair_is_identical_and_isolated(self):
        first = protocol.request_body(protocol.MODEL, "cache_write", "anthropic", NONCE)
        second = protocol.request_body(protocol.MODEL, "cache_read", "anthropic", NONCE)
        self.assertEqual(first, second)
        self.assertLess(len(json.dumps(first).encode()), protocol.MAX_BODY)
        self.assertNotEqual(first, protocol.request_body(protocol.MODEL, "cache_read", "anthropic", "b" * 32))

    def test_gateway_empty_system_list_is_preserved_but_content_is_rejected(self):
        body = protocol.request_body(protocol.MODEL, "complete", "anthropic", NONCE)
        self.assertEqual(protocol.checked_body({**body, "system": []}, "complete", NONCE),
                         {**body, "system": []})
        for system in (None, "", "private", [{"type": "text", "text": "private"}], {}):
            with self.subTest(system=system), self.assertRaises(ValueError):
                protocol.checked_body({**body, "system": system}, "complete", NONCE)

    def test_cost_uses_separate_cache_buckets(self):
        usage = {"input_tokens": 100, "output_tokens": 20,
                 "cache_creation_input_tokens": 1000, "cache_read_input_tokens": 2000}
        self.assertEqual(protocol.usage_cost(usage), Decimal("0.00495"))
        self.assertIsNone(protocol.usage_cost({"input_tokens": 100}))
        self.assertIsNone(protocol.usage_cost({**usage, "cache_read_input_tokens": -1}))
        self.assertEqual(protocol.usage_cost({"input_tokens": 0, "output_tokens": 0}), 0)

    def test_cache_guard_accepts_split_messages_and_missing_hint_without_restoring_it(self):
        body = protocol.request_body(protocol.MODEL, "cache_write", "anthropic", NONCE)
        blocks = body["messages"][0]["content"]
        body["messages"] = [{"role": "user", "content": [block]} for block in blocks]
        self.assertEqual(protocol.checked_body(body, "cache_write", NONCE), body)
        del blocks[0]["cache_control"]
        self.assertEqual(protocol.checked_body(body, "cache_write", NONCE), body)
        self.assertNotIn("cache_control", blocks[0])
        for control in ({"type": "ephemeral", "ttl": "1h"}, {"type": "unknown"}):
            blocks[0]["cache_control"] = control
            with self.assertRaises(ValueError):
                protocol.checked_body(body, "cache_write", NONCE)
        blocks[0]["cache_control"] = {"type": "ephemeral"}
        blocks[1]["text"] = "arbitrary data"
        with self.assertRaises(ValueError):
            protocol.checked_body(body, "cache_write", NONCE)

    def test_credential_is_file_only_and_never_echoed(self):
        key = "sk-ant-" + "synthetic" * 5
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "key"
            path.write_text("ANTHROPIC_API_KEY=" + key)
            self.assertEqual(live.credential(path), key)
            for text in (key + "\n" + key + "2", "sk-other-secret", "x" * 65537):
                path.write_text(text)
                with self.assertRaisesRegex(ValueError, "expected one Anthropic credential"):
                    live.credential(path)

    def test_spend_and_attempt_limits_survive_restart(self):
        with tempfile.TemporaryDirectory() as directory:
            server = live.LiveServer(("127.0.0.1", 0), directory, config(cap=1), "synthetic")
            try:
                server.reserve(b"x")
                held = protocol.budget_used(server.history)
                server.observed_usage(1, "complete", 1, {"input_tokens": 12, "output_tokens": 1}, protocol.MODEL, False)
                self.assertEqual(protocol.budget_used(server.history), held)
            finally:
                server.server_close()
            server = live.LiveServer(("127.0.0.1", 0), directory, config(cap=1), "synthetic")
            try:
                self.assertEqual(protocol.budget_used(server.history), held)
                with self.assertRaisesRegex(ValueError, "attempt limit"):
                    server.reserve(b"x")
            finally:
                server.server_close()

    def test_budget_fails_closed(self):
        with tempfile.TemporaryDirectory() as directory:
            for budget in ("0", "NaN", "Infinity", "41"):
                with self.assertRaises(ValueError):
                    live.LiveServer(("127.0.0.1", 0), directory, {**config(), "max_estimated_usd": budget}, "synthetic")
            server = live.LiveServer(("127.0.0.1", 0), directory,
                                     {**config(), "max_estimated_usd": "0.000001"}, "synthetic")
            try:
                with self.assertRaisesRegex(ValueError, "spend guard"):
                    server.reserve(b"x")
            finally:
                server.server_close()

    def test_stream_frames_reject_incomplete_and_overlarge_data(self):
        for raw in (b'data: {}', b'data: ' + b'x' * 65536 + b'\n\n'):
            with self.assertRaises(ValueError):
                list(live.stream_frames(io.BytesIO(raw)))


class AnthropicRelayHTTP(unittest.TestCase):
    def test_injected_retry_never_reaches_provider(self):
        with tempfile.TemporaryDirectory() as directory:
            server = live.LiveServer(("127.0.0.1", 0), directory, config(), "synthetic-secret")
            server.case = "retry429"
            thread = threading.Thread(target=server.serve_forever, kwargs={"poll_interval": 0.01}, daemon=True)
            thread.start()
            try:
                with patch("live_anthropic.urllib.request.build_opener") as opener:
                    result = client.request(f"http://127.0.0.1:{server.server_port}/v1/messages",
                                            protocol.request_body(protocol.MODEL, "retry429", "anthropic", NONCE))
                    self.assertEqual(result["status"], 429)
                    opener.assert_not_called()
                    self.assertFalse(server.history)
            finally:
                server.shutdown()
                server.server_close()
                thread.join(2)

    def test_rejected_request_cannot_reach_provider(self):
        with tempfile.TemporaryDirectory() as directory:
            server = live.LiveServer(("127.0.0.1", 0), directory, config(), "synthetic-secret")
            thread = threading.Thread(target=server.serve_forever, kwargs={"poll_interval": 0.01}, daemon=True)
            thread.start()
            try:
                with patch("live_anthropic.urllib.request.build_opener") as opener:
                    result = client.request(f"http://127.0.0.1:{server.server_port}/v1/messages",
                                            {"model": protocol.MODEL, "max_tokens": 1000000})
                    self.assertEqual(result["status"], 502)
                    opener.assert_not_called()
                    self.assertFalse(server.history)
                    self.assertTrue(server.failed)
            finally:
                server.shutdown()
                server.server_close()
                thread.join(2)

    def exercise(self, case, mode="drain", early_eof=False):
        body = protocol.request_body(protocol.MODEL, case, "anthropic", NONCE)
        final_usage = {"input_tokens": 40, "output_tokens": 80,
                       "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0}
        message = {"type": "message", "model": protocol.MODEL, "usage": final_usage,
                   "content": [{"type": "text", "text": "synthetic"}], "stop_reason": "end_turn"}
        raw = json.dumps(message).encode()
        if body["stream"]:
            events = [{"type": "message_start", "message": {**message,
                       "usage": {**final_usage, "output_tokens": 1}, "stop_reason": None}}]
            events += [{"type": "content_block_delta", "delta": {"type": "text_delta", "text": "x"}}] * 20
            if not early_eof:
                events += [{"type": "message_delta", "delta": {"stop_reason": "end_turn"},
                            "usage": {"output_tokens": 80}}, {"type": "message_stop"}]
            raw = b"".join(("event: " + e["type"] + "\ndata: " + json.dumps(e) + "\n\n").encode() for e in events)
        response = SlowStream(raw)
        with tempfile.TemporaryDirectory() as directory:
            server = live.LiveServer(("127.0.0.1", 0), directory, config(mode), "synthetic-secret")
            server.case = case
            thread = threading.Thread(target=server.serve_forever, kwargs={"poll_interval": 0.01}, daemon=True)
            thread.start()
            try:
                with patch("live_anthropic.urllib.request.build_opener") as opener:
                    opener.return_value.open.return_value = response
                    result = client.request(f"http://127.0.0.1:{server.server_port}/v1/messages", body,
                                            disconnect=case == "client_disconnect")
                    for _ in range(300):
                        if server.active == 0:
                            break
                        time.sleep(0.01)
                    self.assertEqual(server.active, 0)
                    self.assertEqual(server.failed, early_eof)
                    request = opener.return_value.open.call_args.args[0]
                    self.assertEqual(request.full_url, "https://api.anthropic.com/v1/messages")
                    self.assertEqual(request.get_header("Anthropic-version"), protocol.API_VERSION)
                    rows = [json.loads(line) for line in (Path(directory) / "provider.jsonl").read_text().splitlines()]
                    self.assertNotIn("synthetic-secret", json.dumps(rows))
                    return result, rows, list(server.history)
            finally:
                server.shutdown()
                server.server_close()
                thread.join(2)

    def test_controls_capture_final_usage(self):
        for case in ("complete", "stream_standard", "stream_complete"):
            with self.subTest(case=case):
                result, rows, ledger = self.exercise(case)
                self.assertTrue(result["terminal"])
                final = [r for r in rows if r["event"] == "reference" and r["final"]]
                self.assertEqual(len(final), 1)
                self.assertEqual(final[0]["usage"]["output_tokens"], 80)
                self.assertEqual(sum(r["event"] == "provider_complete" for r in ledger), 1)

    def test_removed_usage_keeps_early_counts_distinct_from_final(self):
        result, rows, _ = self.exercise("missing")
        self.assertFalse(result["usage_events"])
        self.assertTrue(any(r.get("final") for r in rows))
        result, rows, _ = self.exercise("stream_missing")
        self.assertTrue(result["terminal"])
        self.assertEqual(len(result["usage_events"]), 1)
        self.assertEqual(result["usage_events"][0]["usage"]["output_tokens"], 1)
        self.assertEqual(next(r for r in rows if r.get("final"))["usage"]["output_tokens"], 80)

    def test_drain_and_cancel_keep_different_evidence(self):
        result, rows, ledger = self.exercise("stream_cut")
        self.assertFalse(result["terminal"])
        self.assertTrue(any(r.get("final") for r in rows))
        self.assertTrue(any(r["event"] == "provider_complete" for r in ledger))
        for case in ("stream_cut", "client_disconnect"):
            with self.subTest(case=case):
                result, rows, ledger = self.exercise(case, "cancel")
                self.assertFalse(result["terminal"])
                self.assertTrue(any(r["event"] == "reference" and not r["final"] for r in rows))
                self.assertFalse(any(r.get("final") for r in rows))
                self.assertFalse(any(r["event"] == "provider_complete" for r in ledger))
                self.assertGreater(protocol.budget_used(ledger), 0)

    def test_early_eof_is_not_final(self):
        result, rows, ledger = self.exercise("stream_complete", early_eof=True)
        self.assertFalse(result["terminal"])
        self.assertFalse(any(r.get("final") for r in rows))
        self.assertFalse(any(r["event"] == "provider_complete" for r in ledger))


if __name__ == "__main__":
    unittest.main()
