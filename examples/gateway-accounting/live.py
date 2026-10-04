# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Opt-in OpenAI relay, reusing the provider trial's validation and spend guards."""

from decimal import Decimal
import importlib.util
import http.client
import json
import os
from pathlib import Path
import re
import socket
import urllib.error
import urllib.request

import provider


def trial_module():
    path = Path("/trial/relay.py")
    if not path.exists():
        path = Path(__file__).resolve().parents[1] / "integrations/litellm/provider-trial/relay.py"
    spec = importlib.util.spec_from_file_location("provider_trial_guards", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


guards = trial_module()
MODEL = guards.MODEL
LIVE_CASES = ("complete", "stream_complete", "stream_cut", "client_disconnect", "missing", "stream_missing", "retry429")


def credential(path):
    value = os.environ.pop("OPENAI_API_KEY", None)
    if value is None:
        raw = Path(path).read_text()
        if len(raw) > 65536:
            raise ValueError("credential file too large")
        matches = re.findall(r"(?<![A-Za-z0-9_-])sk-[A-Za-z0-9_-]{20,}(?![A-Za-z0-9_-])", raw)
        if len(set(matches)) != 1:
            raise ValueError("expected one provider credential")
        value = matches[0]
    if not re.fullmatch(r"sk-[A-Za-z0-9_-]{20,}", value):
        raise ValueError("invalid provider credential format")
    return value


def checked_body(value):
    # Gateways may rename max_tokens while translating the same bounded request.
    body = dict(value)
    if "max_tokens" in body:
        old = body.pop("max_tokens")
        if "max_completion_tokens" in body and old != body["max_completion_tokens"]:
            raise ValueError("conflicting output limits")
        body["max_completion_tokens"] = old
    guards.validate(body)
    if body["max_completion_tokens"] > 512 or len(body["messages"]) != 1 or body.get("tools"):
        raise ValueError("only the bounded synthetic study request is permitted")
    message = body["messages"][0]
    if message != {"role": "user", "content": "Write the numbers from 1 through 200, one number per line. No other text."}:
        raise ValueError("only the fixed synthetic prompt is permitted")
    body.update(store=False, service_tier="default")
    if body.get("stream"):
        body["stream_options"] = {"include_usage": True}
    return body


class LiveServer(provider.StudyServer):
    def __init__(self, address, root, config, key):
        self.config, self.key = config, key
        self.budget = Decimal(config["max_estimated_usd"])
        if not self.budget.is_finite() or not 0 < self.budget <= 40:
            raise ValueError("positive authorized budget at most 40 USD required")
        if (config["model"] != MODEL or type(config["max_provider_attempts"]) is not int or
                not 1 <= config["max_provider_attempts"] <= 12 or config["cut_mode"] not in ("drain", "cancel")):
            raise ValueError("unsupported live study configuration")
        ledger = Path(root) / "ledger.jsonl"
        self.history = [json.loads(line) for line in ledger.read_text().splitlines()] if ledger.exists() else []
        super().__init__(address, root)
        self.RequestHandlerClass = LiveHandler
        self.active = 0
        self.failed = False

    def reserve(self, data, body):
        with self.lock:
            reserve = guards.reserve_cost(len(data), body["max_completion_tokens"])
            if guards.budget_used(self.history) + reserve > self.budget:
                raise ValueError("estimated budget reached")
            attempt = sum(row["event"] == "reserved" for row in self.history) + 1
            if attempt > self.config["max_provider_attempts"]:
                raise ValueError("provider attempt cap reached")
            row = {"event": "reserved", "attempt": attempt, "reserve_usd": str(reserve)}
            self.history.append(row)
            # Persist before forwarding. Unknown work retains its full reservation.
            with (self.root / "ledger.jsonl").open("a") as output:
                output.write(json.dumps(row) + "\n")
                output.flush()
                os.fsync(output.fileno())
            return attempt

    def observed_usage(self, attempt, case, case_attempt, value):
        usage = guards.usage_only(value.get("usage"))
        if not usage:
            return
        row = {"event": "provider_complete", "attempt": attempt, "usage": usage,
               "model": value.get("model")}
        with self.lock:
            self.history.append(row)
            with (self.root / "ledger.jsonl").open("a") as output:
                output.write(json.dumps(row) + "\n")
        self.record(event="reference", attempt=case_attempt, case=case, protocol="openai",
                    usage=usage, basis="provider_response_metadata")


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
        if self.path != "/v1/chat/completions":
            return self.reply(404, {})
        with self.server.lock:
            self.server.active += 1
        self.response_started = False
        try:
            self.forward()
        except socket.timeout:
            self.server.failed = True
            self.server.record(event="provider_transport_error", error_type="timeout")
            self.close_connection = True
        except (BrokenPipeError, ConnectionResetError):
            self.close_connection = True
        except (ValueError, TypeError, OSError, http.client.HTTPException) as exc:
            self.server.failed = True
            self.server.record(event="relay_error", error_type=type(exc).__name__)
            try:
                if not self.response_started:
                    self.reply(502, {"error": {"message": "bounded study relay rejected request"}})
                self.close_connection = True
            except OSError:
                pass
        finally:
            with self.server.lock:
                self.server.active -= 1

    def forward(self):
        lengths = self.headers.get_all("Content-Length", [])
        if self.headers.get("Transfer-Encoding") or len(lengths) != 1 or not lengths[0].isdecimal():
            return self.reply(400, {})
        size = int(lengths[0])
        if not 0 < size <= guards.MAX_INPUT_BYTES:
            return self.reply(413, {})
        body = checked_body(json.loads(self.rfile.read(size)))
        with self.server.lock:
            case = self.server.case
            self.server.attempts += 1
            case_attempt = self.server.attempts
        if case not in LIVE_CASES:
            return self.reply(400, {})
        self.server.record(event="relay_attempt", case=case, attempt=case_attempt, protocol="openai",
                           stream=body.get("stream", False), mode="live_provider")
        if case == "retry429" and case_attempt == 1:
            self.server.record(event="injected_429", case=case, attempt=case_attempt, forwarded=False)
            return self.reply(429, {"error": {"type": "rate_limit_error", "message": "synthetic retry"}},
                              {"Retry-After": "0"})
        data = json.dumps(body).encode()
        try:
            attempt = self.server.reserve(data, body)
        except ValueError:
            self.server.failed = True
            self.server.record(event="guard_rejected", case=case, attempt=case_attempt, forwarded=False)
            return self.reply(403, {"error": {"message": "study spend or attempt guard reached"}})
        request = urllib.request.Request("https://api.openai.com/v1/chat/completions", data=data,
                    headers={"Content-Type": "application/json", "Authorization": "Bearer " + self.server.key})
        self.server.record(event="provider_forward", case=case, attempt=case_attempt, protocol="openai")
        try:
            response = urllib.request.build_opener(urllib.request.ProxyHandler({}), guards.NoRedirect()).open(request, timeout=70)
        except urllib.error.HTTPError as exc:
            self.server.failed = True
            self.server.record(event="provider_error", case=case, attempt=case_attempt, status=exc.code)
            exc.close()
            return self.reply(exc.code, {"error": {"message": "provider rejected study request"}})
        with response:
            if not body.get("stream"):
                raw = response.read(2_000_001)
                if len(raw) > 2_000_000:
                    raise ValueError("response limit exceeded")
                value = json.loads(raw)
                self.server.observed_usage(attempt, case, case_attempt, value)
                if case == "missing":
                    value.pop("usage", None)
                return self.reply(200, value)
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.send_header("Connection", "close")
            self.end_headers()
            self.close_connection = True
            self.response_started = True
            cut, count, total = False, 0, 0
            terminal_received, terminal_sent = False, False
            while True:
                line = response.readline(65537)
                if not line:
                    break
                total += len(line)
                if len(line) > 65536 or total > 2_000_000:
                    raise ValueError("stream limit exceeded")
                terminal = line.startswith(b"data:") and line[5:].strip() == b"[DONE]"
                terminal_received = terminal_received or terminal
                if line.startswith(b"data:") and line[5:].strip() != b"[DONE]":
                    value = json.loads(line[5:])
                    count += 1
                    self.server.observed_usage(attempt, case, case_attempt, value)
                    if case == "stream_missing" and value.get("usage"):
                        continue
                if case == "stream_cut" and count >= 3 and not cut:
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
                                           provider_usage="unknown", reason="injected_transport_fault")
                        return
                if not cut:
                    try:
                        self.wfile.write(line)
                        self.wfile.flush()
                        terminal_sent = terminal_sent or terminal
                    except (BrokenPipeError, ConnectionResetError):
                        self.server.record(event="downstream_closed", case=case, attempt=case_attempt, protocol="openai")
                        self.server.record(event="upstream_closed_by_relay", case=case, attempt=case_attempt,
                                           provider_usage="unknown", reason="client_disconnect")
                        return
            self.server.record(event="stream_end", case=case, attempt=case_attempt,
                               protocol="openai", terminal_received=terminal_received,
                               terminal_sent=terminal_sent, upstream_drained=True)
            if not terminal_received:
                raise ValueError("provider stream ended without its terminal event")


if __name__ == "__main__":
    os.umask(0o077)
    root = Path("/evidence")
    config = json.loads((root / "live.json").read_text())
    try:
        key = credential("/run/secrets/provider_key")
        LiveServer(("0.0.0.0", 8080), root, config, key).serve_forever()
    except Exception as exc:
        raise SystemExit("live relay stopped: " + type(exc).__name__) from None
