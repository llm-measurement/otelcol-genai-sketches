# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Opt-in Claude relay; only this process receives the real provider credential."""

from decimal import Decimal
import http.client
import json
import os
from pathlib import Path
import re
import socket
import urllib.error
import urllib.request

import anthropic_protocol as protocol
from live import guards
import provider


def credential(path):
    with Path(path).open() as source:
        raw = source.read(65537)
    matches = re.findall(r"(?<![A-Za-z0-9_-])sk-ant-[A-Za-z0-9_-]{20,}(?![A-Za-z0-9_-])", raw)
    if len(raw) > 65536 or len(set(matches)) != 1:
        raise ValueError("expected one Anthropic credential in the external file")
    return matches[0]


def stream_frames(response):
    frame, total = [], 0
    while True:
        line = response.readline(65537)
        if not line:
            if frame:
                raise ValueError("incomplete provider stream frame")
            return
        total += len(line)
        if len(line) > 65536 or total > 2_000_000:
            raise ValueError("provider stream limit exceeded")
        frame.append(line)
        if line.strip():
            continue
        raw = b"".join(frame)
        data = b"\n".join(part[5:].strip() for part in frame if part.startswith(b"data:"))
        frame = []
        value = json.loads(data) if data else {}
        if not isinstance(value, dict):
            raise ValueError("invalid provider event")
        yield raw, value


class LiveServer(provider.StudyServer):
    def __init__(self, address, root, config, key):
        self.config, self.key = config, key
        self.budget = Decimal(config["max_estimated_usd"])
        if not self.budget.is_finite() or not 0 < self.budget <= 40:
            raise ValueError("positive estimated budget at most 40 USD required")
        if (config.get("provider") != "anthropic" or config["model"] != protocol.MODEL or
                type(config["max_provider_attempts"]) is not int or
                not 1 <= config["max_provider_attempts"] <= 12 or config["cut_mode"] not in ("drain", "cancel")):
            raise ValueError("unsupported live Anthropic configuration")
        protocol.cache_prefix(config["cache_nonce"])
        ledger = Path(root) / "ledger.jsonl"
        self.history = [json.loads(line) for line in ledger.read_text().splitlines()] if ledger.exists() else []
        super().__init__(address, root)
        self.RequestHandlerClass = LiveHandler
        self.active, self.failed = 0, False
        self.cases = protocol.LIVE_CASES

    def ledger(self, row):
        self.history.append(row)
        with (self.root / "ledger.jsonl").open("a") as output:
            output.write(json.dumps(row) + "\n")
            output.flush()
            os.fsync(output.fileno())

    def reserve(self, data):
        with self.lock:
            reserve = protocol.reserve_cost(len(data))
            if protocol.budget_used(self.history) + reserve > self.budget:
                raise ValueError("estimated spend guard reached")
            attempt = sum(row["event"] == "reserved" for row in self.history) + 1
            if attempt > self.config["max_provider_attempts"]:
                raise ValueError("provider attempt limit reached")
            self.ledger({"event": "reserved", "attempt": attempt, "reserve_usd": str(reserve)})
            return attempt

    def observed_usage(self, attempt, case, case_attempt, usage, model, final):
        clean = protocol.usage_only(usage)
        if not clean:
            return
        if final:
            with self.lock:
                self.ledger({"event": "provider_complete", "attempt": attempt,
                             "usage": clean, "model": model})
        self.record(event="reference", attempt=case_attempt, case=case, protocol="anthropic",
                    usage=clean, basis="provider_response_metadata", final=final)


class LiveHandler(provider.Handler):
    timeout = 75

    def do_GET(self):
        if self.path == "/idle":
            with self.server.lock:
                status = 409 if self.server.failed else 202 if self.server.active else 200
            return self.reply(status, {})
        return super().do_GET()

    def do_POST(self):
        if self.path in ("/control", "/v1/traces"):
            return super().do_POST()
        if self.path not in ("/v1/messages", "/messages"):
            return self.reply(404, {})
        with self.server.lock:
            self.server.active += 1
        self.response_started = False
        try:
            self.forward()
        except (BrokenPipeError, ConnectionResetError):
            self.close_connection = True
        except (ValueError, TypeError, OSError, http.client.HTTPException) as exc:
            self.server.failed = True
            self.server.record(event="relay_error", error_type=type(exc).__name__)
            if not self.response_started:
                try:
                    self.reply(502, {"type": "error", "error": {
                        "type": "api_error", "message": "bounded study relay rejected request"}})
                except OSError:
                    pass
            self.close_connection = True
        finally:
            with self.server.lock:
                self.server.active -= 1

    def forward(self):
        lengths = self.headers.get_all("Content-Length", [])
        if self.headers.get("Transfer-Encoding") or len(lengths) != 1 or not lengths[0].isascii() or not lengths[0].isdecimal():
            raise ValueError("invalid request framing")
        size = int(lengths[0])
        if not 0 < size <= protocol.MAX_BODY:
            raise ValueError("request limit exceeded")
        raw = self.rfile.read(size)
        if len(raw) != size:
            raise ValueError("incomplete request")
        with self.server.lock:
            case = self.server.case
            self.server.attempts += 1
            case_attempt = self.server.attempts
        body = protocol.checked_body(json.loads(raw), case, self.server.config["cache_nonce"])
        if case in ("cache_write", "cache_read"):
            blocks = [block for message in body["messages"] if isinstance(message["content"], list)
                      for block in message["content"]]
            self.server.record(event="provider_request_shape", case=case, attempt=case_attempt,
                               message_count=len(body["messages"]),
                               cache_marker_count=sum("cache_control" in block for block in blocks))
        self.server.record(event="relay_attempt", case=case, attempt=case_attempt, protocol="anthropic",
                           stream=body.get("stream", False), mode="live_provider")
        if case == "retry429" and case_attempt == 1:
            self.server.record(event="injected_429", case=case, attempt=case_attempt, forwarded=False)
            return self.reply(429, {"type": "error", "error": {
                "type": "rate_limit_error", "message": "synthetic retry"}}, {"Retry-After": "0"})
        data = json.dumps(body).encode()
        try:
            attempt = self.server.reserve(data)
        except ValueError:
            self.server.failed = True
            self.server.record(event="guard_rejected", case=case, attempt=case_attempt, forwarded=False)
            return self.reply(403, {"type": "error", "error": {
                "type": "permission_error", "message": "study spend or attempt guard reached"}})
        request = urllib.request.Request("https://api.anthropic.com/v1/messages", data=data, headers={
            "Content-Type": "application/json", "x-api-key": self.server.key,
            "anthropic-version": protocol.API_VERSION})
        self.server.record(event="provider_forward", case=case, attempt=case_attempt, protocol="anthropic")
        try:
            response = urllib.request.build_opener(urllib.request.ProxyHandler({}), guards.NoRedirect()).open(request, timeout=70)
        except urllib.error.HTTPError as exc:
            self.server.failed = True
            self.server.record(event="provider_error", case=case, attempt=case_attempt, status=exc.code)
            exc.close()
            return self.reply(exc.code, {"type": "error", "error": {
                "type": "api_error", "message": "provider rejected study request"}})
        with response:
            if not body.get("stream"):
                raw = response.read(2_000_001)
                if len(raw) > 2_000_000:
                    raise ValueError("response limit exceeded")
                value = json.loads(raw)
                if not isinstance(value, dict) or value.get("type") != "message":
                    raise ValueError("unexpected provider response")
                self.server.observed_usage(attempt, case, case_attempt, value.get("usage"), value.get("model"), True)
                if case == "missing":
                    value.pop("usage", None)
                return self.reply(200, value)
            self.forward_stream(response, attempt, case, case_attempt)

    def forward_stream(self, response, attempt, case, case_attempt):
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Connection", "close")
        self.end_headers()
        self.close_connection = self.response_started = True
        usage, model = {}, None
        cut, deltas, terminal_received, terminal_sent, final_output = False, 0, False, False, False
        for raw, value in stream_frames(response):
            event = value.get("type")
            if event == "error":
                raise ValueError("provider stream error")
            if event == "message_start":
                message = value.get("message", {})
                model = message.get("model")
                usage.update(protocol.usage_only(message.get("usage")))
                self.server.observed_usage(attempt, case, case_attempt, usage, model, False)
            elif event == "message_delta":
                update = protocol.usage_only(value.get("usage"))
                usage.update(update)
                final_output = final_output or "output_tokens" in update
                self.server.observed_usage(attempt, case, case_attempt, usage, model, False)
            elif event == "message_stop":
                terminal_received = True
                if not final_output:
                    usage.pop("output_tokens", None)
                self.server.observed_usage(attempt, case, case_attempt, usage, model, True)
            elif event == "content_block_delta":
                deltas += 1
            if case == "stream_cut" and deltas >= 3 and not cut:
                cut = True
                try:
                    self.connection.shutdown(socket.SHUT_RDWR)
                except OSError:
                    pass
                self.connection.close()
                mode = self.server.config["cut_mode"]
                self.server.record(event="injected_stream_cut", case=case, attempt=case_attempt, mode=mode)
                if mode == "cancel":
                    self.server.record(event="upstream_closed_by_relay", case=case, attempt=case_attempt,
                                       provider_usage="partial", reason="injected_transport_fault")
                    return
            if case == "stream_missing" and event == "message_delta":
                value.pop("usage", None)
                raw = ("event: message_delta\ndata: " + json.dumps(value) + "\n\n").encode()
            if not cut:
                try:
                    self.wfile.write(raw)
                    self.wfile.flush()
                    terminal_sent = terminal_sent or event == "message_stop"
                except (BrokenPipeError, ConnectionResetError):
                    self.server.record(event="downstream_closed", case=case, attempt=case_attempt, protocol="anthropic")
                    self.server.record(event="upstream_closed_by_relay", case=case, attempt=case_attempt,
                                       provider_usage="partial", reason="client_disconnect")
                    return
        self.server.record(event="stream_end", case=case, attempt=case_attempt, protocol="anthropic",
                           terminal_received=terminal_received, terminal_sent=terminal_sent, upstream_drained=True)
        if not terminal_received:
            raise ValueError("provider stream ended without message_stop")


if __name__ == "__main__":
    os.umask(0o077)
    root = Path("/evidence")
    config = json.loads((root / "live.json").read_text())
    try:
        key = credential("/run/secrets/provider_key")
    except (OSError, ValueError):
        raise SystemExit("provider credential unavailable or invalid") from None
    LiveServer(("0.0.0.0", 8080), root, config, key).serve_forever()
