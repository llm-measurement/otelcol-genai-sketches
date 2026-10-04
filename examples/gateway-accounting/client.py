# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""One local synthetic request; deliberately no client retries or credentials."""

import http.client
import json
import time
from urllib.parse import urlsplit

from provider import SENTINEL

MAX_RESPONSE = 2_000_000
EVENTS = ("response", "message", "message_start", "message_delta", "message_stop",
          "content_block_start", "content_block_delta", "content_block_stop", "ping", "error")
FINISH_REASONS = ("stop", "length", "tool_calls", "function_call", "content_filter", "end_turn",
                  "max_tokens", "stop_sequence", "tool_use", "pause_turn", "refusal", "error")
ERROR_TYPES = ("invalid_request_error", "authentication_error", "billing_error", "permission_error",
               "not_found_error", "request_too_large", "rate_limit_error", "api_error", "overloaded_error")
USAGE_FIELDS = {"prompt_tokens": (), "completion_tokens": (), "total_tokens": (),
                "input_tokens": (), "output_tokens": (), "cache_read_input_tokens": (),
                "cache_creation_input_tokens": (), "prompt_tokens_details": ("cached_tokens", "audio_tokens"),
                "completion_tokens_details": ("reasoning_tokens", "audio_tokens",
                    "accepted_prediction_tokens", "rejected_prediction_tokens"),
                "cache_creation": ("ephemeral_5m_input_tokens", "ephemeral_1h_input_tokens")}


def payload(protocol, case, model):
    if protocol not in ("openai", "anthropic"):
        raise ValueError("unknown client protocol")
    body = {"model": model, "messages": [{"role": "user", "content": SENTINEL}],
            "stream": case.startswith("stream_") or case == "client_disconnect"}
    if protocol == "openai":
        body["max_tokens"] = 128
        if body["stream"]:
            body["stream_options"] = {"include_usage": True}
    else:
        body["max_tokens"] = 128
    return body


def request(url, body, headers=None, disconnect=False):
    parsed = urlsplit(url)
    if parsed.scheme != "http" or parsed.hostname not in ("gateway", "provider", "127.0.0.1", "localhost"):
        raise ValueError("study client accepts only its local test endpoints")
    conn = http.client.HTTPConnection(parsed.hostname, parsed.port or 80, timeout=30)
    result = {"status": None, "usage_events": [], "finish_reasons": [], "terminal": False,
              "client_cancelled": False, "transport_error": None}
    started = time.monotonic()
    try:
        conn.request("POST", parsed.path or "/", body=json.dumps(body),
                     headers={"Content-Type": "application/json", **(headers or {})})
        response = conn.getresponse()
        result["status"] = response.status
        if response.status >= 400:
            result["error_present"] = True
        if "text/event-stream" not in response.getheader("Content-Type", ""):
            data = response.read(MAX_RESPONSE + 1)
            if len(data) > MAX_RESPONSE:
                result["transport_error"] = "response_limit_exceeded"
                return result
            try:
                value = json.loads(data)
            except ValueError:
                result["transport_error"] = "non_json_response"
            else:
                observe(value, result)
                result["terminal"] = bool(result["finish_reasons"]) and not result.get("error_present", False)
            return result
        total, chunks = 0, 0
        data_lines, event = [], None
        while True:
            line = response.readline(65537)
            if not line:
                if data_lines:
                    result["transport_error"] = "incomplete_sse_event"
                break
            total += len(line)
            if len(line) > 65536 or total > MAX_RESPONSE:
                result["transport_error"] = "response_limit_exceeded"
                break
            if line.startswith(b"event:"):
                event = line[6:].strip().decode("utf-8", errors="replace")
                continue
            if line.startswith(b"data:"):
                data_lines.append(line[5:].lstrip(b" ").rstrip(b"\r\n"))
                continue
            if line.strip() or not data_lines:
                if not line.strip():
                    event = None
                continue
            data = b"\n".join(data_lines).strip()
            data_lines = []
            if data == b"[DONE]":
                result["terminal"] = not result.get("error_present", False)
                event = None
                continue
            try:
                value = json.loads(data)
            except ValueError:
                result["transport_error"] = "non_json_stream_event"
                break
            if isinstance(value, dict) and "type" not in value and event in EVENTS:
                value["type"] = event
            observe(value, result)
            event = None
            chunks += 1
            if disconnect and chunks >= 3:
                result["client_cancelled"] = True
                # Close the response as well as the connection: HTTP/1.0 EOF
                # responses can own the only remaining socket reference.
                response.close()
                conn.close()
                break
    except (OSError, http.client.HTTPException) as exc:
        result["transport_error"] = type(exc).__name__
    finally:
        conn.close()
        result["elapsed_seconds"] = round(time.monotonic() - started, 3)
    return result


def observe(value, result):
    if not isinstance(value, dict):
        return
    choices = value.get("choices")
    for choice in choices if isinstance(choices, list) else []:
        if isinstance(choice, dict) and choice.get("finish_reason"):
            reason = choice["finish_reason"]
            result["finish_reasons"].append(reason if reason in FINISH_REASONS else "unknown")
    for candidate in (value, value.get("delta", {})):
        if isinstance(candidate, dict) and candidate.get("stop_reason"):
            reason = candidate["stop_reason"]
            result["finish_reasons"].append(reason if reason in FINISH_REASONS else "unknown")
    event = value.get("type", "response")
    event = event if event in EVENTS else "unknown"
    for candidate in (value, value.get("message", {})):
        if not isinstance(candidate, dict) or not isinstance(candidate.get("usage"), dict):
            continue
        usage = {}
        for key, fields in USAGE_FIELDS.items():
            if key not in candidate["usage"]:
                continue
            raw = candidate["usage"][key]
            if fields and isinstance(raw, dict):
                usage[key] = {k: v if type(v) is int and 0 <= v < 2**63 else None
                              for k, v in raw.items() if k in fields}
            else:
                usage[key] = raw if not fields and type(raw) is int and 0 <= raw < 2**63 else None
        # Initial output (often zero) is provisional. A final usage delta still
        # does not prove the stream reached its separate message_stop marker.
        phase = "initial" if event == "message_start" else "final" if result["finish_reasons"] else "partial"
        result["usage_events"].append({"event": event, "phase": phase, "usage": usage})
    if value.get("error") or value.get("type") == "error":
        result["error_present"] = True
        error = value.get("error")
        kind = error.get("type") if isinstance(error, dict) else None
        result["error_type"] = kind if kind in ERROR_TYPES else "unknown"
    if event == "message_stop":
        result["terminal"] = not result.get("error_present", False)
    if result.get("error_present"):
        result["terminal"] = False
