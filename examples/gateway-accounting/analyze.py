# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Whitelist private mock observations; never infer origin, billing or budget bypass."""

import argparse
from datetime import datetime
import json
from pathlib import Path
import re

from gateways import GATEWAYS
from provider import PROTOCOLS

DIMENSIONS = ("input", "output", "cache_read")
ORIGINS = ("reported", "provider_reported", "estimated", "inferred", "missing", "unavailable", "unknown")
FINISH_REASONS = ("stop", "length", "tool_calls", "function_call", "content_filter", "end_turn",
                  "max_tokens", "stop_sequence", "tool_use", "pause_turn", "refusal", "error")
SURFACES = ("provider_response", "client_response", "gateway_accounting", "exported_telemetry")
MODES = ("deterministic_mock", "real_provider", "live_provider")
REFERENCE_BASES = ("synthetic_configured_usage", "provider_response_metadata")


def count(value):
    return value if type(value) is int and 0 <= value < 2**63 else None


def obj(value):
    return value if isinstance(value, dict) else {}


def enum(value, allowed, default="unknown"):
    return value if isinstance(value, str) and value in allowed else default


def normalize(usage, protocol="openai"):
    usage = obj(usage)
    if protocol == "openai":
        return {"input": count(usage.get("prompt_tokens")),
                "output": count(usage.get("completion_tokens")),
                "cache_read": count(obj(usage.get("prompt_tokens_details")).get("cached_tokens"))}
    inp = count(usage.get("input_tokens"))
    read = count(usage.get("cache_read_input_tokens"))
    write = count(usage.get("cache_creation_input_tokens"))
    # Anthropic input excludes cache reads/writes. Missing uncached input stays
    # unknown, including when a nonzero cache subset is explicitly reported.
    caches_valid = all(k not in usage or count(usage[k]) is not None for k in
                       ("cache_read_input_tokens", "cache_creation_input_tokens"))
    return {"input": inp + (read or 0) + (write or 0) if inp is not None and caches_valid else None,
            "output": count(usage.get("output_tokens")), "cache_read": read}


def compare(reference, observed):
    result = {}
    for key in DIMENSIONS:
        ref, obs = reference.get(key), observed.get(key)
        result[key] = ("absent" if obs is None else "reference_unknown" if ref is None else
                       "equal" if obs == ref else "under_counted" if obs < ref else "over_counted")
    return result


def read_json(path):
    if path.stat().st_size > 20_000_000:
        raise ValueError("private record exceeds analysis limit")
    return json.loads(path.read_text())


def rows(path, *, mixed_logs=False):
    if not path.exists():
        return []
    if path.stat().st_size > 20_000_000:
        raise ValueError("private record exceeds analysis limit")
    result = []
    for line in path.read_text().splitlines():
        if not line.strip():
            continue
        try:
            value = json.loads(line)
        except ValueError:
            if mixed_logs and not line.lstrip().startswith("{"):
                continue
            raise ValueError("unreadable capture row") from None
        if isinstance(value, dict):
            result.append(value)
    return result


def merge_usage(events):
    merged = {}
    for event in events:
        for key, value in obj(event.get("usage")).items():
            if isinstance(value, dict) and isinstance(merged.get(key), dict):
                merged[key] = {**merged[key], **value}
            else:
                merged[key] = value
    return merged


def final_usage(events, protocol="openai"):
    return normalize(merge_usage(events), protocol)


def http_status(value):
    return value if type(value) is int and 100 <= value <= 599 else None


def record(usage, raw=None, *, cache_creation=None):
    raw = obj(raw)
    return {"usage": usage, "usage_details": {"cache_creation": count(cache_creation)},
            "final": raw.get("final") if type(raw.get("final")) is bool else None,
            "status": enum(raw.get("status"),
            ("success", "failure", "failed", "error", "pending", "processing", "cancelled")),
            "http_status": http_status(raw.get("status")),
            "declared_origin": enum(raw.get("usage_origin"), ORIGINS),
            "finish_reasons": sorted({enum(v, FINISH_REASONS) for v in raw.get("finish_reasons", [])
                                      if isinstance(v, str)})}


def finish_reasons(response):
    values = [obj(response).get("stop_reason")]
    values += [obj(c).get("finish_reason") for c in obj(response).get("choices", [])]
    return [enum(v, FINISH_REASONS) for v in values if v is not None]


def usage_state(usage):
    if all(usage[k] is None for k in DIMENSIONS):
        return "missing"
    return "complete" if all(usage[k] is not None for k in ("input", "output")) else "partial"


def surface(records, reader_status="observed", selection="single_record"):
    selected = [r for r in records if r.get("selected_for_usage", True)]
    usage = selected[0]["usage"].copy() if len(selected) == 1 else normalize(None)
    state = "ambiguous" if len(selected) > 1 else usage_state(usage)
    return {"reader_status": reader_status, "usage": usage, "usage_state": state,
            "final": selected[0].get("final") if len(selected) == 1 else None,
            "usage_details": selected[0]["usage_details"].copy() if len(selected) == 1 else {"cache_creation": None},
            "selection": "multiple_records_not_aggregated" if len(selected) > 1 else selection,
            "record_count": len(records), "selected_count": len(selected), "records": records}


def in_window(row, start, end):
    stamp = count(row.get("time_ns"))
    return stamp is not None and start <= stamp <= end


def litellm_records(path, start, end):
    result = []
    for row in rows(path):
        if not in_window(row, start, end):
            continue
        raw = obj(row.get("standard_logging_object", row))
        cache = obj(raw.get("cache_fields"))
        details = obj(cache.get("prompt_tokens_details"))
        evidence = {key: count(cache[key]) for key in ("cache_read_input_tokens", "cache_creation_input_tokens")
                    if count(cache.get(key)) is not None}
        evidence.update({"prompt_tokens_details." + key: count(details[key]) for key in
                         ("cached_tokens", "cache_write_tokens", "cache_creation_tokens")
                         if count(details.get(key)) is not None})
        aliases = {"cache_read": ("cache_read_input_tokens", "prompt_tokens_details.cached_tokens"),
                   "cache_creation": ("cache_creation_input_tokens", "prompt_tokens_details.cache_write_tokens",
                                      "prompt_tokens_details.cache_creation_tokens")}
        resolved, conflicts = {}, []
        for dimension, keys in aliases.items():
            values = {evidence[key] for key in keys if key in evidence}
            resolved[dimension] = next(iter(values)) if len(values) == 1 else None
            if len(values) > 1:
                conflicts.append(dimension)
        usage = normalize(raw)
        if "cache_fields" in raw:
            usage["cache_read"] = resolved["cache_read"]
        item = record(usage, raw, cache_creation=resolved["cache_creation"])
        item.update(cache_capture="captured" if "cache_fields" in raw else "not_captured",
                    cache_evidence=evidence, cache_conflicts=conflicts)
        item["callback_success"] = row.get("callback_success") if type(row.get("callback_success")) is bool else None
        result.append(item)
    return result


def bifrost_records(native):
    result = []
    for raw in native.get("records", []):
        raw = obj(raw)
        columns = normalize({**raw, "prompt_tokens_details": {"cached_tokens": raw.get("cached_read_tokens")}})
        token_usage = raw.get("token_usage")
        if isinstance(token_usage, str):
            token_usage = json.loads(token_usage) if token_usage.strip() else None
        # SQLite numeric columns may be default zero. An absent token_usage JSON
        # field is not evidence that those defaults were reported by the provider.
        usage = normalize(token_usage) if "token_usage" in raw else columns
        item = record(usage, {**raw, "finish_reasons": [raw.get("stop_reason")]})
        item.update(column_usage=columns, number_of_retries=count(raw.get("number_of_retries")),
                    usage_location="token_usage" if "token_usage" in raw else "selected_columns")
        result.append(item)
    return result


def portkey_records(native):
    result = []
    for raw in native.get("records", []):
        # /log/stream includes numeric heartbeat frames. Never walk raw request
        # objects recursively: they contain prompts, headers and provider keys.
        if not isinstance(raw, dict) or not isinstance(raw.get("requestOptions"), list):
            continue
        for option in raw["requestOptions"] or [{}]:
            option = obj(option)
            response = obj(option.get("response"))
            location = "response"
            if not isinstance(response.get("usage"), dict):
                original = obj(obj(option.get("originalResponse")).get("body"))
                if isinstance(original.get("usage"), dict):
                    response, location = original, "original_response"
            usage = obj(response.get("usage"))
            openai_counts = any(k in usage for k in ("prompt_tokens", "completion_tokens"))
            protocol = "anthropic" if not openai_counts and any(k in usage for k in (
                "input_tokens", "output_tokens", "cache_read_input_tokens", "cache_creation_input_tokens")) else "openai"
            item = record(normalize(usage, protocol),
                          {"status": raw.get("status"), "finish_reasons": finish_reasons(response)},
                          cache_creation=usage.get("cache_creation_input_tokens"))
            item["usage_location"] = location
            result.append(item)
    return result


def timestamp_ns(value):
    if not isinstance(value, str):
        return None
    try:
        dt = datetime.fromisoformat(value.replace("Z", "+00:00"))
        return int(dt.timestamp() * 1_000_000_000) if dt.tzinfo else None
    except (ValueError, OverflowError):
        return None


def canonical_key(key):
    return re.sub(r"([a-z0-9])([A-Z])", r"\1_\2", key).lower()


def usage_attributes(raw):
    attrs = {canonical_key(k): v for k, v in raw.items() if isinstance(k, str)}

    def first(*keys):
        return next((attrs[k] for k in keys if k in attrs), None)

    usage = {"input": count(first("gen_ai.usage.input_tokens", "gen_ai.usage.prompt_tokens")),
             "output": count(first("gen_ai.usage.output_tokens", "gen_ai.usage.completion_tokens")),
             "cache_read": count(first("gen_ai.usage.cache_read.input_tokens", "gen_ai.usage.cache_read_input_tokens"))}
    item = record(usage, cache_creation=first("gen_ai.usage.cache_write.input_tokens",
                  "gen_ai.usage.cache_creation.input_tokens", "gen_ai.usage.cache_creation_input_tokens"))
    item["http_status"] = http_status(first("http.response.status_code", "http.status_code", "http.status"))
    item["origins"] = {d: enum(first(f"gen_ai_sketch.usage.{d}.provenance",
                                    f"gen_ai.usage.{d}.provenance"), ORIGINS) for d in DIMENSIONS}
    incomplete = first("gen_ai.usage.incomplete", "gen_ai_sketch.usage.incomplete")
    item["incomplete"] = incomplete if type(incomplete) is bool else None
    return item


def agentgateway_records(path, start, end):
    result = []
    for row in rows(path, mixed_logs=True):
        if row.get("scope") != "request":
            continue
        stamp = timestamp_ns(row.get("time"))
        if stamp is None or not start <= stamp <= end:
            continue
        result.append(usage_attributes(row))
    return result


def native_surface(pair, value):
    name, start, end = value["gateway"], value["started_ns"], value["deadline_ns"]
    native = obj(value.get("native"))
    try:
        if name == "litellm":
            path = pair / "native.jsonl"
            records = litellm_records(path, start, end)
            captured = path.exists()
        elif name == "agentgateway":
            path = pair / (value["case"] + ".logs")
            records = agentgateway_records(path, start, end)
            captured = path.exists()
        elif name in ("bifrost", "portkey"):
            if native.get("reader_error") or ("status" in native and native["status"] not in (200, "read_only_sqlite")):
                return surface([], "reader_error")
            captured = "records" in native
            records = bifrost_records(native) if name == "bifrost" else portkey_records(native)
        else:
            return surface([], "reader_unsupported")
    except (OSError, ValueError, TypeError, AttributeError):
        return surface([], "reader_error")
    return surface(records, "observed" if records else "no_record_by_deadline" if captured else "capture_missing")


def otlp_attributes(attributes):
    result = {}
    for attr in attributes:
        attr = obj(attr)
        value = obj(attr.get("value"))
        for key in ("intValue", "int_value", "stringValue", "string_value", "boolValue", "bool_value"):
            if key not in value:
                continue
            scalar = value[key]
            if key in ("intValue", "int_value") and isinstance(scalar, str) and re.fullmatch(r"[0-9]{1,19}", scalar):
                scalar = int(scalar)
            result[attr.get("key", "")] = scalar
            break
    return result


def span_records(path, start, end):
    nodes = {}
    for row in rows(path):
        if not in_window(row, start, end):
            continue
        body = obj(row.get("body"))
        for resource in body.get("resourceSpans", body.get("resource_spans", [])):
            for scope in resource.get("scopeSpans", resource.get("scope_spans", [])):
                for span in scope.get("spans", []):
                    stamp = span.get("startTimeUnixNano", span.get("start_time_unix_nano"))
                    if stamp is not None and not start <= int(stamp) <= end:
                        continue
                    trace = span.get("traceId", span.get("trace_id"))
                    sid = span.get("spanId", span.get("span_id"))
                    parent = span.get("parentSpanId", span.get("parent_span_id"))
                    key = (trace, sid) if trace and sid else (None, len(nodes))
                    item = usage_attributes(otlp_attributes(span.get("attributes", [])))
                    code = obj(span.get("status")).get("code", 0)
                    item["status"] = {0: "unset", 1: "ok", 2: "error", "STATUS_CODE_UNSET": "unset",
                                      "STATUS_CODE_OK": "ok", "STATUS_CODE_ERROR": "error"}.get(code, "unknown")
                    nodes[key] = ((trace, parent), item)
    # Suppress usage ancestors even across intermediate non-usage spans. Keep
    # all sanitized status rows; IDs are used only for selection, never exported.
    candidates = {key for key, (_, r) in nodes.items() if usage_state(r["usage"]) != "missing" or
                  any(origin != "unknown" for origin in r["origins"].values()) or r["incomplete"] is not None}
    ancestors = set()
    for key in candidates:
        visited = {key}
        parent = nodes[key][0]
        while parent in nodes and parent not in visited:
            visited.add(parent)
            ancestors.add(parent)
            parent = nodes[parent][0]
    result = []
    for key, (_, item) in nodes.items():
        item["label"] = f"span-{len(result) + 1}"
        item["hierarchy_available"] = key[0] is not None
        item["selected_for_usage"] = key in candidates - ancestors
        item["selection_role"] = ("usage_leaf" if item["selected_for_usage"] else
                                  "usage_ancestor" if key in candidates else "status_only")
        result.append(item)
    return result


def telemetry_surface(pair, value):
    path = pair / "telemetry.jsonl"
    # Only these two harness adapters configure an OTLP exporter. File absence
    # for other adapters says nothing about the gateway product's capabilities.
    if not path.exists() and value["gateway"] not in ("litellm", "agentgateway"):
        return surface([], "reader_unsupported", "export_not_configured_in_harness")
    try:
        records = span_records(path, value["started_ns"], value["deadline_ns"])
    except (OSError, ValueError, TypeError, AttributeError):
        return surface([], "reader_error")
    return surface(records, "observed" if records else "no_record_by_deadline" if path.exists() else "capture_missing",
                   "usage_leaves_no_hierarchy_sum")


def add_comparisons(value, reference):
    for item in [value, *value["records"]]:
        comparison = compare(reference, item["usage"])
        if item is value and value["usage_state"] == "ambiguous":
            comparison = dict.fromkeys(DIMENSIONS, "unknown")
        selected = [r for r in value["records"] if r.get("selected_for_usage", True)]
        origin_record = selected[0] if item is value and len(selected) == 1 else item
        item["dimensions"] = {d: {"value": item["usage"][d], "comparison": comparison[d],
                                  "declared_origin": origin_record.get("origins", {}).get(
                                      d, origin_record.get("declared_origin", "unknown"))} for d in DIMENSIONS}
    return value


def provider_surface(events, protocol, event_name):
    attempts = {}
    for event in events:
        if event.get("event") == event_name:
            attempt = count(event.get("attempt"))
            key = attempt if attempt is not None else ("unattributed", len(attempts))
            attempts.setdefault(key, []).append(event)
    records = []
    for attempt, values in attempts.items():
        wire_protocol = enum(values[-1].get("protocol"), PROTOCOLS, protocol)
        usage = merge_usage(values)
        item = record(normalize(usage, wire_protocol),
                      cache_creation=usage.get("cache_creation_input_tokens") if wire_protocol == "anthropic" else None)
        final = None
        if any("final" in v for v in values):
            flag = next(v["final"] for v in reversed(values) if "final" in v)
            final = flag if type(flag) is bool else None
        elif wire_protocol == "openai" and usage and (event_name == "delivered_usage" or
                values[-1].get("basis") == "provider_response_metadata"):
            # Legacy OpenAI usage packets were emitted only as final metadata.
            final = True
        elif wire_protocol == "anthropic" and event_name == "delivered_usage" and usage:
            # Mock streams record their terminal write separately. A delta alone
            # is not a terminal event; nonstream writes have no usage_event.
            final = all("usage_event" not in v for v in values) or any(
                e.get("event") == "stream_end" and e.get("attempt") == attempt and
                e.get("terminal_sent") is True for e in events)
        item.update(attempt=count(attempt), protocol=wire_protocol, usage_event_count=len(values), final=final,
                    final_usage=item["usage"].copy() if final is True else normalize(None),
                    declared_origin="provider_reported" if event_name == "delivered_usage" or
                    values[-1].get("basis") == "provider_response_metadata" else "unknown")
        records.append(item)
    result = surface(records, "observed" if records else "no_record_by_deadline", "per_attempt_cumulative_not_sum")
    result["final_usage"] = result["usage"].copy() if result["final"] is True else normalize(None)
    return result


def is_control(case, protocol, manifest):
    return case in ("complete", "stream_standard") or case == "stream_complete" and (
        protocol == "openai" or obj(manifest.get("live")).get("provider") == "anthropic")


def manifest_versions(manifest):
    versions = {}
    for name in GATEWAYS:
        metadata = obj(obj(manifest.get("images")).get(name))
        version = metadata.get("version")
        image = metadata.get("image", "")
        digest = re.search(r"@sha256:([0-9a-f]{64})$", image) if isinstance(image, str) else None
        versions[name] = {"version": version if isinstance(version, str) and
                          re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9.+_-]{0,127}", version) else None,
                          "image_digest": "sha256:" + digest[1] if digest else None}
    return versions


def analyze_case(pair, value, mode="unknown"):
    start, end = value["started_ns"], value["deadline_ns"]
    if count(start) is None or count(end) is None or start > end:
        raise ValueError("invalid observation window")
    events = [r for r in rows(pair / "provider.jsonl") if r.get("case") == value["case"] and in_window(r, start, end)]
    refs = [r for r in events if r.get("event") == "reference"]
    reference_basis = enum(refs[-1].get("basis"), REFERENCE_BASES, mode) if refs else mode
    metadata = [r for r in refs if r.get("basis") == "provider_response_metadata"]
    delivered = provider_surface(metadata or events, value["upstream_protocol"],
                                 "reference" if metadata else "delivered_usage")
    if not (pair / "provider.jsonl").exists():
        delivered["reader_status"] = "capture_missing"
    configured = provider_surface([r for r in refs if r.get("basis") == "synthetic_configured_usage"],
                                  value["upstream_protocol"], "reference")
    client_events = value.get("usage_events", [])
    client_usage = merge_usage(client_events)
    client_record = record(normalize(client_usage, value["client_protocol"]), value,
                           cache_creation=client_usage.get("cache_creation_input_tokens"))
    client_record.update(terminal=value.get("terminal") is True, client_cancelled=value.get("client_cancelled") is True,
                         error_present=value.get("error_present") is True,
                         transport_error_present=value.get("transport_error") is not None)
    client_record["final"] = client_record["terminal"] and not any(client_record[k] for k in (
        "client_cancelled", "error_present", "transport_error_present"))
    if value["client_protocol"] == "anthropic" and (value["case"].startswith("stream_") or
            value["case"] == "client_disconnect" or any(e.get("event") in ("message_start", "message_delta")
                                                        for e in client_events)):
        output_event = next((e for e in reversed(client_events) if "output_tokens" in obj(e.get("usage"))), {})
        # message_stop completes the transport, not missing usage. A retained
        # message_start output count is still provisional after stream_missing.
        client_record["final"] = client_record["final"] and output_event.get("event") == "message_delta" and (
            output_event.get("phase", "final") == "final" and count(obj(output_event.get("usage")).get("output_tokens")) is not None)
    client_record["final_usage"] = client_record["usage"].copy() if client_record["final"] else normalize(None)
    surfaces = dict(zip(SURFACES, (delivered, surface([client_record]), native_surface(pair, value), telemetry_surface(pair, value))))
    surfaces["client_response"]["final_usage"] = client_record["final_usage"].copy()
    reference = delivered["final_usage"]
    live = any(r.get("event") == "relay_attempt" for r in events)
    attempts = {count(r.get("attempt")) for r in events if r.get("event") == ("provider_forward" if live else "attempt")}
    return {"gateway": value["gateway"], "upstream_protocol": value["upstream_protocol"],
            "client_protocol": value["client_protocol"], "case": value["case"],
            "logical_requests": 1, "provider_attempts": len(attempts),
            "relay_attempts": sum(r.get("event") == "relay_attempt" for r in events) if live else None,
            "injected_rejections": sum(r.get("event") == "injected_429" for r in events),
            "reference_basis": reference_basis,
            "comparison_basis": "provider_response_metadata" if metadata else "delivered_usage_capture",
            "configured_mock_reference": configured, "settle_seconds": value.get("settle_seconds")
            if type(value.get("settle_seconds")) in (int, float) and 0 <= value["settle_seconds"] <= 86400 else None,
            "cancellation_observed_upstream": any(r.get("event") == "downstream_closed" for r in events),
            "budget_enforcement_tested": False,
            "surfaces": {name: add_comparisons(s, reference) for name, s in surfaces.items()}}


def analyze_run(root):
    root = Path(root)
    manifest = obj(read_json(root / "manifest.json")) if (root / "manifest.json").exists() else {}
    report = {"schema_version": 2, "mode": enum(manifest.get("mode"), MODES),
              "client_protocol": enum(manifest.get("client_protocol"), PROTOCOLS),
              "live_provider": enum(obj(manifest.get("live")).get("provider"), PROTOCOLS),
              "cut_mode": enum(obj(manifest.get("live")).get("cut_mode"), ("drain", "cancel"), "not_applicable"),
              "versions": manifest_versions(manifest), "observations": [], "excluded_runs": []}
    for name in GATEWAYS:
        for protocol in PROTOCOLS:
            pair = root / (name + "-" + protocol)
            if not pair.is_dir():
                continue
            diagnostic = {"gateway": name, "upstream_protocol": protocol, "gateway_fault": False}
            try:
                outcome = obj(read_json(pair / "outcome.json"))
                status = enum(outcome.get("status"), ("completed", "running", "setup_or_control_failed"), "incomplete")
                case_names = outcome.get("cases", [])
                requested = manifest.get("cases", case_names)
                if status != "completed":
                    diagnostic.update(status=status, observed_case_count=len(case_names))
                    report["excluded_runs"].append(diagnostic)
                    continue
                if (not case_names or any(not isinstance(c, str) or not re.fullmatch(r"[a-z][a-z0-9_]{0,63}", c)
                                          for c in case_names) or set(requested) - set(case_names)):
                    raise ValueError("incomplete case list")
                values = []
                for case in case_names:
                    value = obj(read_json(pair / (case + ".json")))
                    if value.get("client_protocol") not in PROTOCOLS:
                        raise ValueError("unknown client protocol")
                    if "client_protocol" in manifest and value["client_protocol"] != manifest["client_protocol"]:
                        raise ValueError("client protocol disagrees with manifest")
                    value.update(gateway=name, upstream_protocol=protocol, case=case)
                    control = is_control(case, protocol, manifest)
                    if control and (value.get("status") != 200 or value.get("terminal") is not True):
                        diagnostic["status"] = "control_failed"
                        break
                    values.append(value)
                if "status" in diagnostic:
                    report["excluded_runs"].append(diagnostic)
                    continue
                observations = [analyze_case(pair, value, report["mode"]) for value in values]
            except (OSError, ValueError, TypeError, KeyError, AttributeError):
                diagnostic["status"] = "incomplete_or_unreadable"
                report["excluded_runs"].append(diagnostic)
                continue
            report["observations"].extend(observations)
    return report


def summarize(root):
    return analyze_run(root)["observations"]


def render_report(report):
    lines = ["# Private Gateway Study: Usage Observations", "",
             f'Evidence mode: {report["mode"]}. Usage captures are not invoices; no budget-enforcement claim.',
             f'Live cut mode: {report["cut_mode"]}. Drain and propagated cancellation are different experiments.',
             "Mock comparisons use captured wire usage, never configured mock counts.",
             "Live comparisons use pre-fault provider response metadata; it may not have been delivered to the gateway.",
             "Usage state describes numeric presence, not finality. Comparisons require final provider counts; provisional evidence stays visible.",
             "Neither capture proves gateway receipt. Unequal counts do not establish estimation.",
             "Absent is not zero; partial and ambiguous values remain explicit. No record is deadline-bounded.",
             "Telemetry reader_unsupported means this harness did not configure an exporter, not a product limitation.", "",
             "| Gateway | Exact version | Image digest |", "|---|---|---|"]
    for name, version in report["versions"].items():
        lines.append(f'| {name} | {version["version"] or "unknown"} | {version["image_digest"] or "unknown"} |')
    for row in report["observations"]:
        configured = row["configured_mock_reference"]["usage"]
        lines += ["", f'## {row["gateway"]}: {row["case"]} ({row["client_protocol"]} client / {row["upstream_protocol"]} upstream)',
                  "", f'Logical requests: 1; provider attempts: {row["provider_attempts"]}; settling seconds: {row["settle_seconds"]}.',
                  f'Reference basis: {row["reference_basis"]}; comparison basis: {row["comparison_basis"]}.',
                  "Configured mock input/output/cache-read (never substituted): " + "/".join(str(configured[k]) if configured[k] is not None else "absent" for k in DIMENSIONS) + ".",
                  "", "| Surface | Reader / numeric usage | Input | Output | Cache read | Cache creation | Status / origin |",
                  "|---|---|---|---|---|---|---|"]
        for name in SURFACES:
            s = row["surfaces"][name]
            cells = [f'{s["usage"][d] if s["usage"][d] is not None else "absent"} ({s["dimensions"][d]["comparison"]})' for d in DIMENSIONS]
            statuses = sorted({str(r["http_status"]) if r.get("http_status") else r["status"] for r in s["records"]})
            origins = sorted({s["dimensions"][d]["declared_origin"] for d in DIMENSIONS})
            detail = ",".join(statuses) or "unknown"
            detail += "; final=" + ("unknown" if s["final"] is None else str(s["final"]).lower())
            if s["final"] is False:
                detail += " (provisional)"
            if name == "client_response":
                client = s["records"][0]
                detail += f'; terminal={client["terminal"]}; cancelled={client["client_cancelled"]}'
                detail += f'; error={client["error_present"] or client["transport_error_present"]}'
                detail += "; finish=" + (",".join(client["finish_reasons"]) or "unknown")
            creation = s["usage_details"]["cache_creation"]
            lines.append(f'| {name} | {s["reader_status"]} / {s["usage_state"]} | ' + " | ".join(cells) +
                         f' | {creation if creation is not None else "absent"}' +
                         f' | {detail}; origin={",".join(origins)} |')
    lines += ["", "## Incomplete Or Control-Failed Runs", "", "Excluded from accounting comparisons; not classified as gateway faults.", "",
              "| Gateway | Upstream | Run status |", "|---|---|---|"]
    for row in report["excluded_runs"]:
        lines.append(f'| {row["gateway"]} | {row["upstream_protocol"]} | {row["status"]} |')
    lines += ["", "Next steps: repair excluded controls or missing captures, then rerun the deterministic matrix before live-provider validation.",
              "Judgment: neutral for billing or budget conclusions; usage observations alone do not establish either."]
    return "\n".join(lines) + "\n"


def write_report(root):
    root = Path(root)
    report = analyze_run(root)
    (root / "analysis.json").write_text(json.dumps(report, indent=2, allow_nan=False) + "\n")
    (root / "table.md").write_text(render_report(report))
    return report["observations"]


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("run_directory", type=Path)
    args = parser.parse_args()
    print(f"Wrote {len(write_report(args.run_directory))} private observations")
