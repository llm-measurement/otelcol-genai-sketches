# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Fixed, bounded Claude study requests and a conservative local spend guard."""

from decimal import Decimal
import re

MODEL = "claude-sonnet-4-6"
API_VERSION = "2023-06-01"
PROMPT = "Write the numbers from 1 through 200, one number per line. No other text."
LIVE_CASES = ("complete", "stream_standard", "stream_complete", "stream_cut",
              "client_disconnect", "missing", "stream_missing", "retry429",
              "cache_write", "cache_read")
MAX_OUTPUT = 512
MAX_BODY = 65536
RATES = {"input": "3", "output": "15", "cache_creation": "3.75", "cache_read": "0.30"}
USAGE_FIELDS = ("input_tokens", "output_tokens", "cache_creation_input_tokens", "cache_read_input_tokens")


def cache_prefix(nonce):
    if not isinstance(nonce, str) or not re.fullmatch(r"[a-f0-9]{32}", nonce):
        raise ValueError("invalid synthetic cache nonce")
    return "Synthetic cache study " + nonce + ".\n" + "\n".join(
        f"Record {i:04d}: violet harbor rectangle copper meadow."
        for i in range(800))


def request_body(model, case, client_protocol, cache_nonce):
    if case not in LIVE_CASES or client_protocol not in ("openai", "anthropic"):
        raise ValueError("unsupported live Anthropic request")
    content = PROMPT
    if case in ("cache_write", "cache_read"):
        content = [{"type": "text", "text": cache_prefix(cache_nonce),
                    "cache_control": {"type": "ephemeral"}},
                   {"type": "text", "text": "Reply only OK."}]
    body = {"model": model, "max_tokens": MAX_OUTPUT,
            "messages": [{"role": "user", "content": content}],
            "stream": case.startswith("stream_") or case == "client_disconnect"}
    if client_protocol == "openai" and body["stream"]:
        body["stream_options"] = {"include_usage": True}
    return body


def checked_body(value, case, nonce):
    if not isinstance(value, dict) or set(value) - {"model", "max_tokens", "messages", "stream", "system"}:
        raise ValueError("unsupported Anthropic request fields")
    if value.get("system", []) != []:
        raise ValueError("no system content is permitted")
    expected = request_body(MODEL, case, "anthropic", nonce)
    body = dict(value)
    if body.get("model") != MODEL or type(body.get("max_tokens")) is not int or body["max_tokens"] != MAX_OUTPUT:
        raise ValueError("fixed model and output limit required")
    if type(body.get("stream", False)) is not bool or body.get("stream", False) != expected["stream"]:
        raise ValueError("unexpected streaming mode")
    messages = body.get("messages")
    if not isinstance(messages, list) or not 1 <= len(messages) <= 2 or any(not isinstance(m, dict) for m in messages):
        raise ValueError("only the fixed synthetic message is permitted")
    if case in ("cache_write", "cache_read"):
        blocks = []
        for message in messages:
            if set(message) != {"role", "content"} or message["role"] != "user":
                raise ValueError("only synthetic user messages are permitted")
            content = message["content"]
            blocks.extend(content if isinstance(content, list) else [{"type": "text", "text": content}])
        expected_blocks = expected["messages"][0]["content"]
        if len(blocks) != len(expected_blocks):
            raise ValueError("unexpected synthetic cache content")
        for block, wanted in zip(blocks, expected_blocks):
            if (not isinstance(block, dict) or set(block) - {"type", "text", "cache_control"} or
                    block.get("type") != "text" or block.get("text") != wanted["text"]):
                raise ValueError("unexpected synthetic cache content")
            # A gateway may split adjacent user blocks or drop an unsupported
            # cache hint. Observe that behavior instead of manufacturing a hit.
            if "cache_control" in block and block["cache_control"] not in (
                    {"type": "ephemeral"}, {"type": "ephemeral", "ttl": "5m"}):
                raise ValueError("only five-minute synthetic cache hints are permitted")
        return body
    if len(messages) != 1:
        raise ValueError("only the fixed synthetic message is permitted")
    message = messages[0]
    content = message.get("content")
    if case not in ("cache_write", "cache_read") and content == [{"type": "text", "text": PROMPT}]:
        content = PROMPT
    if set(message) != {"role", "content"} or message.get("role") != "user" or content != expected["messages"][0]["content"]:
        raise ValueError("only the fixed synthetic content is permitted")
    return body


def usage_only(value):
    if not isinstance(value, dict):
        return {}
    return {key: value[key] for key in USAGE_FIELDS
            if type(value.get(key)) is int and 0 <= value[key] < 2**63}


def usage_cost(value):
    usage = usage_only(value)
    if "input_tokens" not in usage or "output_tokens" not in usage:
        return None
    if any(key in value and key not in usage for key in USAGE_FIELDS):
        return None
    return sum(Decimal(usage.get(field, 0)) * Decimal(RATES[rate]) for field, rate in (
        ("input_tokens", "input"), ("output_tokens", "output"),
        ("cache_creation_input_tokens", "cache_creation"), ("cache_read_input_tokens", "cache_read"))) / 1_000_000


def reserve_cost(byte_count):
    # Reserve all text bytes at the more expensive cache-write rate plus wrapper
    # allowance; no cache hit or token compression is assumed before a response.
    return ((byte_count + 4096) * Decimal(RATES["cache_creation"]) +
            MAX_OUTPUT * Decimal(RATES["output"])) / 1_000_000


def budget_used(history):
    completed = {row["attempt"]: row for row in history if row.get("event") == "provider_complete"}
    total = Decimal(0)
    for row in history:
        if row.get("event") != "reserved":
            continue
        result = completed.get(row["attempt"], {})
        cost = usage_cost(result.get("usage")) if result.get("model") == MODEL else None
        total += cost if cost is not None else Decimal(row["reserve_usd"])
    return total
