# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Synthetic provider. No credentials, billing, or outbound network connections.

Counts are configured evidence, not tokenization of the synthetic text. Partial
usage objects deliberately violate required response fields. Zero/cache-only
counts are schema-shaped numeric boundary controls, not claims about live model
behavior (Claude documents nonzero output usage even for empty content).
stream_standard models the documented Claude start-input/output-final flow;
stream_complete retains the compatible-provider deferred-usage control.

Protocol references:
https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create
https://platform.claude.com/docs/en/build-with-claude/streaming
https://platform.claude.com/docs/en/api/messages/create
Anthropic-compatible deferred usage reproduction, credited to danielzarioiu:
https://github.com/agentgateway/agentgateway/issues/3739
"""

import argparse
import hmac
import json
import secrets
import socket
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import urlsplit

CASES = (
    "complete", "stream_complete", "stream_cut", "client_disconnect",
    "missing", "stream_missing", "retry429", "zero", "partial_input",
    "partial_output", "cache_only", "stream_standard",
)
PROTOCOLS = ("openai", "anthropic")
MAX_BODY = 65536
MAX_TELEMETRY_BODY = 4_000_000
SENTINEL = "SYNTHETIC_STUDY_PROMPT_NOT_CUSTOMER_DATA"
FIXTURE_KINDS = {
    "complete": "protocol_control", "stream_complete": "protocol_control",
    "stream_standard": "protocol_control",
    "stream_cut": "transport_fault", "client_disconnect": "cancellation_probe",
    "missing": "missing_usage_fault", "stream_missing": "missing_usage_fault",
    "retry429": "retry_control", "zero": "synthetic_zero_boundary",
    "cache_only": "synthetic_cache_boundary",
    "partial_input": "malformed_usage_fault", "partial_output": "malformed_usage_fault",
}
DEFERRED_USAGE_CASES = ("stream_complete", "stream_cut", "stream_missing", "client_disconnect")
TOKEN_ATTRIBUTES = {
    "gen_ai.usage." + name for name in (
        "input_tokens", "output_tokens", "prompt_tokens", "completion_tokens",
        "cache_read.input_tokens", "cache_creation.input_tokens",
        "cache_read_input_tokens", "cache_creation_input_tokens",
    )
}
PROVENANCE_ATTRIBUTES = {f"gen_ai_sketch.usage.{name}.provenance" for name in ("input", "output")}


def native_usage(protocol, case):
    if protocol not in PROTOCOLS or case not in CASES:
        raise ValueError("unsupported study protocol or case")
    inp, out = 100, 20
    if case == "zero":
        inp, out = 0, 0
    if protocol == "openai":
        usage = {"prompt_tokens": inp, "completion_tokens": out, "total_tokens": inp + out}
        if case == "cache_only":
            usage = {"prompt_tokens": 64, "completion_tokens": 0, "total_tokens": 64,
                     "prompt_tokens_details": {"cached_tokens": 64}}
        if case == "partial_input":
            usage = {"prompt_tokens": inp}
        if case == "partial_output":
            usage = {"completion_tokens": out}
    else:
        usage = {"input_tokens": inp, "output_tokens": out,
                 "cache_read_input_tokens": 0, "cache_creation_input_tokens": 0}
        if case == "cache_only":
            usage.update(input_tokens=0, output_tokens=0, cache_read_input_tokens=64)
        if case == "partial_input":
            usage.pop("output_tokens")
        if case == "partial_output":
            usage = {"output_tokens": out}
    return usage


def response_body(protocol, case, model):
    usage = native_usage(protocol, case)
    text = "" if case in ("zero", "cache_only") else "OK"
    if protocol == "openai":
        body = {"id": "chatcmpl-study", "object": "chat.completion", "created": 1791072000,
                "model": model, "choices": [{"index": 0, "message": {"role": "assistant", "content": text},
                                               "finish_reason": "stop"}]}
    else:
        body = {"id": "msg-study", "type": "message", "role": "assistant", "model": model,
                "content": [{"type": "text", "text": text}] if text else [],
                "stop_reason": "end_turn", "stop_sequence": None}
    if case != "missing":
        body["usage"] = usage
    return body


def stream_events(protocol, case, model):
    usage = native_usage(protocol, case)
    count = 0 if case in ("zero", "cache_only") else 40 if case == "client_disconnect" else 3
    if protocol == "openai":
        def chunk(delta, finish=None):
            return {"id": "chatcmpl-study", "object": "chat.completion.chunk", "created": 1791072000,
                    "model": model, "choices": [{"index": 0, "delta": delta, "finish_reason": finish}],
                    "usage": None}
        yield None, chunk({"role": "assistant", "content": ""})
        for _ in range(count):
            yield None, chunk({"content": "OK "})
        if case == "stream_cut":
            return
        yield None, chunk({}, "stop")
        if case not in ("missing", "stream_missing"):
            yield None, dict(chunk({}), choices=[], usage=usage)
        yield None, "[DONE]"
    else:
        body = response_body(protocol, case, model)
        body.update(content=[], stop_reason=None)
        # Preserve the compatible-provider placeholder reproduction separately
        # from Claude's usual input-at-start, output-only-final sequence.
        if case in DEFERRED_USAGE_CASES:
            body["usage"] = native_usage(protocol, "zero")
        elif "usage" in body and "output_tokens" in body["usage"]:
            body["usage"]["output_tokens"] = min(1, usage["output_tokens"])
        yield "message_start", {"type": "message_start", "message": body}
        yield "content_block_start", {"type": "content_block_start", "index": 0,
                                      "content_block": {"type": "text", "text": ""}}
        for _ in range(count):
            yield "content_block_delta", {"type": "content_block_delta", "index": 0,
                                          "delta": {"type": "text_delta", "text": "OK "}}
        if case == "stream_cut":
            return
        yield "content_block_stop", {"type": "content_block_stop", "index": 0}
        final = {"type": "message_delta", "delta": {"stop_reason": "end_turn", "stop_sequence": None}}
        if case not in ("missing", "stream_missing"):
            final["usage"] = (usage if case in DEFERRED_USAGE_CASES else
                              {"output_tokens": usage["output_tokens"]} if "output_tokens" in usage else {})
        yield "message_delta", final
        yield "message_stop", {"type": "message_stop"}


class StudyServer(ThreadingHTTPServer):
    daemon_threads = True

    def __init__(self, address, root):
        super().__init__(address, Handler)
        self.root = Path(root)
        self.root.mkdir(mode=0o700, parents=True, exist_ok=True)
        self.lock = threading.Lock()
        self.lineage_key = secrets.token_bytes(32)
        self.case = "complete"
        self.attempts = 0

    def record(self, **fields):
        with self.lock, (self.root / "provider.jsonl").open("a") as output:
            output.write(json.dumps({"time_ns": time.time_ns(), **fields}) + "\n")


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    timeout = 20

    def handle(self):
        # A reset while waiting for the next keep-alive request happens outside
        # do_POST. It is ordinary peer shutdown, not an unhandled server error.
        try:
            super().handle()
        except (BrokenPipeError, ConnectionResetError):
            self.close_connection = True

    def log_message(self, *_):
        pass

    def reply(self, status, body, headers=None):
        encoded = json.dumps(body).encode()
        if status >= 400:
            self.close_connection = True
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(encoded)))
        if self.close_connection:
            self.send_header("Connection", "close")
        for key, value in (headers or {}).items():
            self.send_header(key, value)
        self.end_headers()
        self.wfile.write(encoded)

    def do_GET(self):
        self.reply(200 if self.path == "/health" else 404, {})

    def do_POST(self):
        case, attempt, protocol = None, None, None
        terminal_sent = False
        try:
            path = urlsplit(self.path).path
            lengths = self.headers.get_all("Content-Length", [])
            if self.headers.get("Transfer-Encoding") or len(lengths) != 1:
                return self.reply(400, {})
            if not lengths[0].isascii() or not lengths[0].isdecimal():
                return self.reply(400, {})
            size = int(lengths[0])
            if not 0 < size <= (MAX_TELEMETRY_BODY if path == "/v1/traces" else MAX_BODY):
                return self.reply(413, {})
            raw = self.rfile.read(size)
            if len(raw) != size:
                return self.reply(400, {})
            body = json.loads(raw)
            if not isinstance(body, dict):
                return self.reply(400, {})
            if path == "/v1/traces":
                # Retain the OTLP envelope consumed by analysis, but never raw
                # resource, prompt, response, event, or status-message content.
                spans = []
                for resource in body.get("resourceSpans", []):
                    for scope in resource.get("scopeSpans", []):
                        for span in scope.get("spans", []):
                            attrs = []
                            for attr in span.get("attributes", []):
                                key, value = attr.get("key"), attr.get("value", {})
                                if key in TOKEN_ATTRIBUTES:
                                    number = value.get("intValue")
                                    if type(number) is int or (isinstance(number, str) and
                                            len(number) <= 20 and number.lstrip("-").isascii() and
                                            number.lstrip("-").isdecimal()):
                                        attrs.append({"key": key, "value": {"intValue": number}})
                                elif key in PROVENANCE_ATTRIBUTES:
                                    origin = value.get("stringValue")
                                    if origin in ("provider_reported", "inferred", "unavailable", "unknown"):
                                        attrs.append({"key": key, "value": {"stringValue": origin}})
                            code = span.get("status", {}).get("code")
                            status = {"code": code} if type(code) is int and code in (0, 1, 2) else {}
                            captured_span = {"attributes": attrs, "status": status}
                            # Parent IDs share the span-ID domain, so equality
                            # survives across batches without persisting raw IDs.
                            for field, domain in (("traceId", "trace"), ("spanId", "span"),
                                                  ("parentSpanId", "span")):
                                identifier = span.get(field)
                                if isinstance(identifier, str) and 0 < len(identifier) <= 256 and identifier.strip("0"):
                                    captured_span[field] = hmac.new(
                                        self.server.lineage_key, (domain + ":" + identifier).encode(), "sha256"
                                    ).hexdigest()
                            for field in ("startTimeUnixNano", "endTimeUnixNano"):
                                stamp = span.get(field)
                                if (type(stamp) is int or (isinstance(stamp, str) and
                                        0 < len(stamp) <= 20 and stamp.isascii() and stamp.isdecimal())) and \
                                        0 <= int(stamp) <= 2**64 - 1:
                                    captured_span[field] = stamp
                            spans.append(captured_span)
                captured = {"resourceSpans": [{"scopeSpans": [{"spans": spans}]}]}
                with self.server.lock, (self.server.root / "telemetry.jsonl").open("a") as output:
                    output.write(json.dumps({"time_ns": time.time_ns(), "body": captured,
                                             "capture": "allowlisted_usage_and_hashed_lineage"}) + "\n")
                return self.reply(200, {})
            if path == "/control":
                if body.get("case") not in getattr(self.server, "cases", CASES):
                    return self.reply(400, {})
                with self.server.lock:
                    self.server.case, self.server.attempts = body["case"], 0
                return self.reply(200, {})
            if path not in ("/messages", "/v1/messages", "/chat/completions", "/v1/chat/completions"):
                return self.reply(404, {})
            protocol = "anthropic" if path.endswith("/messages") else "openai"
            model, stream = body.get("model", "study-model"), body.get("stream", False)
            options = body.get("stream_options")
            if options is None:
                options = {}
            if (not isinstance(model, str) or not 0 < len(model) <= 256 or type(stream) is not bool or
                    not isinstance(options, dict) or type(options.get("include_usage", False)) is not bool):
                return self.reply(400, {})
            with self.server.lock:
                case = self.server.case
                self.server.attempts += 1
                attempt = self.server.attempts
            self.server.record(event="attempt", case=case, attempt=attempt, protocol=protocol,
                               fixture_kind=FIXTURE_KINDS[case],
                               usage_profile=("anthropic_compatible_deferred" if case in DEFERRED_USAGE_CASES else
                                              "anthropic_standard") if protocol == "anthropic" and stream else protocol,
                               stream=stream, usage_requested=options.get("include_usage", False))
            if case == "retry429" and attempt == 1:
                error = {"error": {"type": "rate_limit_error", "message": "synthetic retry"}}
                if protocol == "anthropic":
                    error.update(type="error", request_id="req-study")
                else:
                    error["error"].update(code="rate_limit_exceeded", param=None)
                return self.reply(429, error, {"Retry-After": "0"})
            self.server.record(event="reference", case=case, attempt=attempt, protocol=protocol,
                               basis="synthetic_configured_usage", usage=native_usage(protocol, case))
            if not stream:
                result = response_body(protocol, case, model)
                self.reply(200, result)
                self.server.record(event="delivered_usage", case=case, attempt=attempt,
                                   protocol=protocol, usage=result.get("usage"))
                return
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.send_header("Cache-Control", "no-cache")
            self.send_header("Connection", "close")
            self.end_headers()
            self.close_connection = True
            for event, value in stream_events(protocol, case, model):
                if protocol == "openai" and not options.get("include_usage", False) and isinstance(value, dict):
                    if value["choices"] == []:
                        continue
                    value.pop("usage", None)
                text = value if isinstance(value, str) else json.dumps(value)
                if event:
                    self.wfile.write(f"event: {event}\n".encode())
                self.wfile.write(f"data: {text}\n\n".encode())
                self.wfile.flush()
                terminal_sent = terminal_sent or value == "[DONE]" or event == "message_stop"
                if event == "message_start" and case not in DEFERRED_USAGE_CASES:
                    # Standard start events report input/cache evidence, but
                    # their output count is provisional until message_delta.
                    start_usage = {key: number for key, number in value["message"].get("usage", {}).items()
                                   if key != "output_tokens"}
                    if start_usage:
                        self.server.record(event="delivered_usage", case=case, attempt=attempt,
                                           protocol=protocol, usage=start_usage, usage_event="message_start")
                if isinstance(value, dict) and isinstance(value.get("usage"), dict):
                    self.server.record(event="delivered_usage", case=case, attempt=attempt,
                                       protocol=protocol, usage=value["usage"], usage_event=event or "usage_chunk")
                if case == "client_disconnect":
                    time.sleep(0.1)
            self.server.record(event="stream_end", case=case, attempt=attempt, protocol=protocol,
                               terminal_sent=terminal_sent)
        except (BrokenPipeError, ConnectionResetError, socket.timeout):
            self.close_connection = True
            if attempt is not None:
                self.server.record(event="downstream_closed", case=case, attempt=attempt,
                                   protocol=protocol, terminal_sent=terminal_sent)
        except (ValueError, TypeError, AttributeError, RecursionError):
            self.reply(400, {})


if __name__ == "__main__":
    import os
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=Path("/evidence"))
    parser.add_argument("--port", type=int, default=8080)
    args = parser.parse_args()
    StudyServer(("0.0.0.0", args.port), args.root).serve_forever()
