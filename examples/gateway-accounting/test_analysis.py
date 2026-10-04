# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex

import json
from pathlib import Path
import tempfile
import unittest

import analyze


class Analysis(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.start = 1_791_126_291_000_000_000
        self.end = self.start + 8_000_000_000

    def write(self, path, value):
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(value))

    def jsonl(self, path, values):
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text("".join(json.dumps(v) + "\n" for v in values))

    def case(self, gateway="litellm", case="complete", **extra):
        return {"gateway": gateway, "case": case, "upstream_protocol": "openai",
                "client_protocol": "openai", "started_ns": self.start, "deadline_ns": self.end,
                "settle_seconds": 8, "status": 200, "terminal": True, "client_cancelled": False,
                "transport_error": None, "usage_events": [], "finish_reasons": ["stop"], **extra}

    def event(self, event, **extra):
        return {"event": event, "case": "complete", "time_ns": self.start + 1,
                "attempt": 1, **extra}

    def span(self, sid, parent=None, usage=True, snake=False, **extra):
        span = {"traceId": "PRIVATE_TRACE_ID", "spanId": sid, "parentSpanId": parent,
                "startTimeUnixNano": str(self.start + 1), "status": {"code": 1, "message": "PRIVATE_ERROR"},
                "name": "/private/request/path", "attributes": []}
        if usage:
            span["attributes"] = [{"key": "gen_ai.usage.input_tokens", "value": {"intValue": "100"}},
                                  {"key": "gen_ai.usage.output_tokens", "value": {"intValue": "20"}}]
        span.update(extra)
        if snake:
            span = {analyze.canonical_key(k): v for k, v in span.items()}
            for attr in span["attributes"]:
                attr["value"] = {analyze.canonical_key(k): v for k, v in attr["value"].items()}
        return span

    def telemetry(self, spans, snake=False, time_ns=None):
        body = {"resource_spans" if snake else "resourceSpans": [
            {"scope_spans" if snake else "scopeSpans": [{"spans": spans}]}]}
        self.jsonl(self.root / "telemetry.jsonl", [{"time_ns": time_ns or self.start + 2, "body": body}])

    def test_native_litellm_zero_partial_nested_and_window(self):
        self.jsonl(self.root / "native.jsonl", [
            {"time_ns": self.start - 1, "prompt_tokens": 999},
            {"time_ns": self.start + 1, "prompt_tokens": 0, "status": "success", "callback_success": True},
            {"time_ns": self.start + 2, "standard_logging_object": {"completion_tokens": 0}},
            {"time_ns": self.end + 1, "prompt_tokens": 999}])
        records = analyze.litellm_records(self.root / "native.jsonl", self.start, self.end)
        self.assertEqual(len(records), 2)
        self.assertEqual(records[0]["usage"], {"input": 0, "output": None, "cache_read": None})
        self.assertEqual(records[1]["usage"]["output"], 0)
        self.assertTrue(records[0]["callback_success"])

    def test_native_bifrost_json_takes_precedence_over_default_zero_columns(self):
        records = analyze.bifrost_records({"records": [
            {"prompt_tokens": 0, "completion_tokens": 0, "cached_read_tokens": 0,
             "token_usage": json.dumps({"prompt_tokens": 0}), "status": "success", "stop_reason": "stop"},
            {"prompt_tokens": 0, "completion_tokens": 0, "token_usage": None},
            {"prompt_tokens": 0, "completion_tokens": 0},
            {"token_usage": {"prompt_tokens": 64, "completion_tokens": 0,
                             "prompt_tokens_details": {"cached_tokens": 64}}} ]})
        self.assertEqual(records[0]["usage"], {"input": 0, "output": None, "cache_read": None})
        self.assertEqual(records[0]["column_usage"]["output"], 0)
        self.assertEqual(records[1]["usage"], analyze.normalize(None))
        self.assertEqual(records[2]["usage"]["output"], 0)
        self.assertEqual(records[3]["usage"], {"input": 64, "output": 0, "cache_read": 64})

    def test_litellm_cache_capture_deduplicates_aliases_without_changing_input(self):
        self.jsonl(self.root / "native.jsonl", [{"time_ns": self.start + 1, "prompt_tokens": 100,
            "completion_tokens": 20, "cache_fields": {"cache_read_input_tokens": 0,
                "cache_creation_input_tokens": 64, "PRIVATE": "PRIVATE", "prompt_tokens_details": {
                    "cached_tokens": 0, "cache_write_tokens": 64, "cache_creation_tokens": 64}}}])
        item = analyze.litellm_records(self.root / "native.jsonl", self.start, self.end)[0]
        self.assertEqual(item["usage"], {"input": 100, "output": 20, "cache_read": 0})
        self.assertEqual(item["usage_details"], {"cache_creation": 64})
        self.assertEqual(item["cache_capture"], "captured")
        self.assertEqual(item["cache_conflicts"], [])
        self.assertNotIn("PRIVATE", json.dumps(item))

    def test_litellm_conflicting_cache_aliases_stay_unknown(self):
        self.jsonl(self.root / "native.jsonl", [{"time_ns": self.start + 1, "cache_fields": {
            "cache_read_input_tokens": 4, "cache_creation_input_tokens": 6,
            "prompt_tokens_details": {"cached_tokens": 5, "cache_write_tokens": 7}}},
            {"time_ns": self.start + 2}, {"time_ns": self.start + 3, "cache_fields": {}}])
        items = analyze.litellm_records(self.root / "native.jsonl", self.start, self.end)
        self.assertIsNone(items[0]["usage"]["cache_read"])
        self.assertIsNone(items[0]["usage_details"]["cache_creation"])
        self.assertEqual(items[0]["cache_conflicts"], ["cache_read", "cache_creation"])
        self.assertEqual(items[1]["cache_capture"], "not_captured")
        self.assertEqual(items[2]["cache_capture"], "captured")

    def test_portkey_response_not_original_duplicate_or_request_usage(self):
        raw = {"status": 200, "requestOptions": [{
            "requestParams": {"usage": {"prompt_tokens": 999}, "messages": ["PRIVATE_PROMPT"]},
            "providerOptions": {"apiKey": "PRIVATE_KEY"},
            "response": {"usage": {"prompt_tokens": 0}, "choices": [{"finish_reason": "stop"}]},
            "originalResponse": {"body": {"usage": {"prompt_tokens": 100, "completion_tokens": 20}}}}]}
        result = analyze.portkey_records({"records": [1791126458495, raw]})
        self.assertEqual(len(result), 1)
        self.assertEqual(result[0]["usage"]["input"], 0)
        self.assertIsNone(result[0]["usage"]["output"])
        self.assertEqual(result[0]["finish_reasons"], ["stop"])
        self.assertNotIn("PRIVATE", json.dumps(result))

    def test_portkey_stream_record_without_usage_is_observed_missing(self):
        case = self.case("portkey", native={"status": 200, "records": [123, {
            "status": 200, "requestOptions": [{"originalResponse": {}, "response": {"message": "PRIVATE"}}]}]})
        result = analyze.native_surface(self.root, case)
        self.assertEqual((result["reader_status"], result["usage_state"]), ("observed", "missing"))

    def test_portkey_original_anthropic_cache_normalization(self):
        result = analyze.portkey_records({"records": [{"status": 200, "requestOptions": [{
            "originalResponse": {"body": {"usage": {"input_tokens": 0, "cache_read_input_tokens": 64,
                                                       "cache_creation_input_tokens": 4, "output_tokens": 0}}}}]}]})
        self.assertEqual(result[0]["usage"], {"input": 68, "output": 0, "cache_read": 64})
        self.assertEqual(result[0]["usage_details"], {"cache_creation": 4})

    def test_portkey_native_anthropic_response_wins_over_original(self):
        result = analyze.portkey_records({"records": [{"status": 200, "requestOptions": [{
            "response": {"usage": {"input_tokens": 2, "output_tokens": 3,
                         "cache_read_input_tokens": 64, "cache_creation_input_tokens": 8}, "stop_reason": "end_turn"},
            "originalResponse": {"body": {"usage": {"input_tokens": 999, "output_tokens": 999}}}}, {
            "response": {"usage": {"cache_creation_input_tokens": 4}}}]}]})
        self.assertEqual(result[0]["usage"], {"input": 74, "output": 3, "cache_read": 64})
        self.assertEqual(result[0]["usage_details"], {"cache_creation": 8})
        self.assertEqual(result[0]["usage_location"], "response")
        self.assertEqual(result[0]["finish_reasons"], ["end_turn"])
        self.assertEqual(result[1]["usage"], analyze.normalize(None))
        self.assertEqual(result[1]["usage_details"], {"cache_creation": 4})

    def test_openai_cache_extensions_do_not_change_token_field_semantics(self):
        usage = {"prompt_tokens": 7, "completion_tokens": 5, "total_tokens": 10439,
                 "cache_read_input_tokens": 10427, "cache_creation_input_tokens": 0,
                 "prompt_tokens_details": {"cached_tokens": 10427}}
        result = analyze.portkey_records({"records": [{"requestOptions": [
            {"response": {"usage": usage}}]}]})
        self.assertEqual(result[0]["usage"], {"input": 7, "output": 5, "cache_read": 10427})
        self.assertEqual(result[0]["usage_details"]["cache_creation"], 0)

    def test_client_keeps_cache_creation_extension_without_repairing_prompt_total(self):
        value = self.case("portkey", usage_events=[{"event": "response", "phase": "final",
            "usage": {"prompt_tokens": 7, "completion_tokens": 5,
                      "cache_creation_input_tokens": 10427}}])
        result = analyze.analyze_case(self.root, value)["surfaces"]["client_response"]
        self.assertEqual(result["usage"]["input"], 7)
        self.assertEqual(result["usage_details"]["cache_creation"], 10427)

    def test_agentgateway_request_logs_filter_and_whitelist(self):
        row = {"scope": "request", "time": "2026-10-04T15:04:51.100000Z", "http.status": 200,
               "gen_ai.usage.input_tokens": 0, "gen_ai.usage.cache_read.input_tokens": 64,
               "trace.id": "PRIVATE_TRACE", "http.path": "/private/path", "src.addr": "PRIVATE_IP"}
        self.jsonl(self.root / "complete.logs", [dict(row, scope="startup"), row,
                                                  dict(row, time="2026-10-03T15:04:51Z")])
        result = analyze.native_surface(self.root, self.case("agentgateway"))
        self.assertEqual(result["record_count"], 1)
        self.assertEqual(result["usage"], {"input": 0, "output": None, "cache_read": 64})
        self.assertNotIn("PRIVATE", json.dumps(result))

    def test_missing_record_unsupported_missing_capture_and_reader_error_differ(self):
        self.jsonl(self.root / "native.jsonl", [])
        self.assertEqual(analyze.native_surface(self.root, self.case())["reader_status"], "no_record_by_deadline")
        self.assertEqual(analyze.native_surface(self.root, self.case("other"))["reader_status"], "reader_unsupported")
        self.assertEqual(analyze.telemetry_surface(self.root, self.case("bifrost"))["reader_status"], "reader_unsupported")
        self.assertEqual(analyze.telemetry_surface(self.root, self.case())["reader_status"], "capture_missing")
        self.assertEqual(analyze.native_surface(self.root, self.case("portkey", native={"status": 503}))["reader_status"], "reader_error")
        (self.root / "native.jsonl").write_text("{invalid\n")
        self.assertEqual(analyze.native_surface(self.root, self.case())["reader_status"], "reader_error")

    def test_otlp_camel_and_snake_usage_status_and_provenance(self):
        for snake in (False, True):
            with self.subTest(snake=snake):
                span = self.span("child", snake=snake)
                span["attributes"] += [
                    {"key": "gen_ai.usage.cache_read.inputTokens", "value": {"int_value": "64"}},
                    {"key": "gen_ai.usage.cache_creation.inputTokens", "value": {"int_value": "8"}},
                    {"key": "gen_ai_sketch.usage.input.provenance", "value": {"stringValue": "inferred"}},
                    {"key": "gen_ai.usage.incomplete", "value": {"bool_value": True}},
                    {"key": "private_key", "value": {"stringValue": "PRIVATE_SECRET"}}]
                self.telemetry([span], snake)
                result = analyze.telemetry_surface(self.root, self.case())
                analyze.add_comparisons(result, analyze.normalize({"prompt_tokens": 101}))
                self.assertEqual(result["usage"], {"input": 100, "output": 20, "cache_read": 64})
                self.assertEqual(result["records"][0]["status"], "ok")
                self.assertTrue(result["records"][0]["incomplete"])
                self.assertEqual(result["usage_details"], {"cache_creation": 8})
                self.assertEqual(result["dimensions"]["input"]["declared_origin"], "inferred")
                self.assertNotIn("PRIVATE", json.dumps(result))
                self.assertNotIn("/private", json.dumps(result))

    def test_otlp_duplicate_export_and_parent_child_not_double_counted(self):
        parent = self.span("parent")
        bridge = self.span("bridge", "parent", usage=False)
        child = self.span("child", "bridge")
        self.telemetry([parent, bridge, child, child])
        result = analyze.telemetry_surface(self.root, self.case())
        self.assertEqual(result["usage"]["input"], 100)
        self.assertEqual(result["selected_count"], 1)
        self.assertEqual(result["record_count"], 3)
        self.assertEqual(result["records"][0]["selection_role"], "usage_ancestor")

    def test_otlp_independent_attempts_not_blindly_added(self):
        self.telemetry([self.span("parent"), self.span("first", "parent"), self.span("second", "parent")])
        result = analyze.telemetry_surface(self.root, self.case())
        analyze.add_comparisons(result, analyze.normalize({"prompt_tokens": 100}))
        self.assertEqual(result["usage_state"], "ambiguous")
        self.assertIsNone(result["usage"]["input"])
        self.assertEqual(result["dimensions"]["input"]["comparison"], "unknown")
        self.assertEqual(result["selected_count"], 2)

    def test_otlp_status_only_and_late_previous_request(self):
        old = self.span("old", startTimeUnixNano=str(self.start - 100))
        self.telemetry([old, self.span("current", usage=False, status={"code": 2, "message": "PRIVATE"})])
        result = analyze.telemetry_surface(self.root, self.case())
        self.assertEqual((result["reader_status"], result["usage_state"]), ("observed", "missing"))
        self.assertEqual(result["record_count"], 1)
        self.assertEqual(result["records"][0]["status"], "error")
        self.telemetry([self.span("late")], time_ns=self.end + 1)
        self.assertEqual(analyze.telemetry_surface(self.root, self.case())["reader_status"], "no_record_by_deadline")

    def test_allowlisted_spans_without_identity_remain_separate(self):
        span = {"attributes": [{"key": "gen_ai.usage.input_tokens", "value": {"intValue": "100"}}], "status": {}}
        self.telemetry([span, span])
        result = analyze.telemetry_surface(self.root, self.case())
        self.assertEqual(result["usage_state"], "ambiguous")
        self.assertEqual([r["label"] for r in result["records"]], ["span-1", "span-2"])
        self.assertFalse(any(r["hierarchy_available"] for r in result["records"]))

    def test_missing_usage_preserves_explicit_unavailable_origin(self):
        span = self.span("missing", usage=False)
        span["attributes"] = [{"key": "gen_ai_sketch.usage.input.provenance", "value": {"stringValue": "unavailable"}}]
        self.telemetry([span])
        result = analyze.telemetry_surface(self.root, self.case())
        analyze.add_comparisons(result, analyze.normalize(None))
        self.assertEqual(result["dimensions"]["input"]["declared_origin"], "unavailable")
        self.assertEqual(result["usage_state"], "missing")

    def test_cache_is_subset_openai_but_disjoint_anthropic(self):
        self.assertEqual(analyze.normalize({"prompt_tokens": 64, "completion_tokens": 0,
                                            "prompt_tokens_details": {"cached_tokens": 64}})["input"], 64)
        self.assertEqual(analyze.normalize({"input_tokens": 0, "cache_read_input_tokens": 64,
                                            "cache_creation_input_tokens": 8}, "anthropic")["input"], 72)
        self.assertIsNone(analyze.normalize({"cache_read_input_tokens": 64}, "anthropic")["input"])
        for invalid in (True, -1, 1.2, "1", 2**63):
            self.assertIsNone(analyze.normalize({"prompt_tokens": invalid})["input"])

    def test_cumulative_updates_and_nested_cache_details_are_not_added(self):
        result = analyze.final_usage([{"usage": {"prompt_tokens": 64, "prompt_tokens_details": {"cached_tokens": 64}}},
                                      {"usage": {"completion_tokens": 0, "prompt_tokens_details": {"other": 9}}}])
        self.assertEqual(result, {"input": 64, "output": 0, "cache_read": 64})

    def test_configured_reference_never_substitutes_for_delivered_usage(self):
        self.jsonl(self.root / "provider.jsonl", [self.event("attempt"), self.event("reference",
                   basis="synthetic_configured_usage", usage={"prompt_tokens": 100, "completion_tokens": 20}),
                   self.event("delivered_usage", usage=None)])
        value = analyze.analyze_case(self.root, self.case(usage_events=[{"usage": {"prompt_tokens": 0}}]))
        self.assertEqual(value["configured_mock_reference"]["usage"]["input"], 100)
        self.assertEqual(value["surfaces"]["provider_response"]["reader_status"], "observed")
        self.assertIsNone(value["surfaces"]["provider_response"]["usage"]["input"])
        dimension = value["surfaces"]["client_response"]["dimensions"]["input"]
        self.assertEqual((dimension["comparison"], dimension["declared_origin"]), ("reference_unknown", "unknown"))

    def test_metadata_reference_and_cancellation_with_no_reference(self):
        event = self.event("reference", basis="provider_response_metadata", protocol="openai",
                           usage={"prompt_tokens": 7, "completion_tokens": 0})
        self.jsonl(self.root / "provider.jsonl", [self.event("attempt"), event, event])
        result = analyze.analyze_case(self.root, self.case(upstream_protocol="anthropic"), "real_provider")
        self.assertEqual(result["reference_basis"], "provider_response_metadata")
        self.assertEqual(result["surfaces"]["provider_response"]["usage"]["input"], 7)
        self.assertTrue(result["surfaces"]["provider_response"]["final"])
        self.assertIsNone(result["configured_mock_reference"]["usage"]["input"])
        self.jsonl(self.root / "provider.jsonl", [self.event("attempt", case="stream_abort")])
        result = analyze.analyze_case(self.root, self.case(case="stream_abort"), "real_provider")
        self.assertEqual(result["reference_basis"], "real_provider")
        self.assertIsNone(result["surfaces"]["provider_response"]["usage"]["input"])

    def test_anthropic_metadata_protocol_and_upstream_fallback(self):
        usage = {"input_tokens": 2, "output_tokens": 0,
                 "cache_read_input_tokens": 64, "cache_creation_input_tokens": 8}
        for upstream, metadata in (("openai", {"protocol": "anthropic"}), ("anthropic", {}),
                                   ("anthropic", {"protocol": "PRIVATE_INVALID"})):
            with self.subTest(upstream=upstream, metadata=metadata):
                events = [self.event("reference", basis="provider_response_metadata", usage=usage, final=True, **metadata)]
                result = analyze.provider_surface(events, upstream, "reference")
                self.assertEqual(result["usage"], {"input": 74, "output": 0, "cache_read": 64})
                self.assertEqual(result["usage_details"], {"cache_creation": 8})
                self.assertEqual(result["final_usage"], result["usage"])
                self.assertTrue(result["final"])
                self.assertEqual(result["records"][0]["protocol"], "anthropic")
                self.assertNotIn("PRIVATE", json.dumps(result))

    def test_anthropic_cancel_retains_provisional_counts_without_final_comparisons(self):
        case = self.case(case="stream_cut", upstream_protocol="anthropic", client_protocol="anthropic",
                         terminal=False, client_cancelled=True, usage_events=[{"usage": {
                             "input_tokens": 2, "output_tokens": 0, "cache_creation_input_tokens": 64}}])
        start = self.event("reference", case="stream_cut", protocol="anthropic",
                           basis="provider_response_metadata", final=False,
                           usage={"input_tokens": 2, "output_tokens": 0, "cache_creation_input_tokens": 64})
        delta = {**start, "usage": {"output_tokens": 3}}
        terminal = {**delta, "final": True, "usage": {"output_tokens": 9}}
        events = [self.event("relay_attempt", case="stream_cut"), self.event("provider_forward", case="stream_cut"),
                  start, delta, self.event("upstream_closed_by_relay", case="stream_cut"),
                  {**terminal, "case": "stream_complete"}, {**terminal, "time_ns": self.end + 1}]
        self.jsonl(self.root / "provider.jsonl", events)
        result = analyze.analyze_case(self.root, case, "live_provider")
        provider = result["surfaces"]["provider_response"]
        self.assertEqual(provider["usage"], {"input": 66, "output": 3, "cache_read": None})
        self.assertEqual(provider["usage_state"], "complete")
        self.assertFalse(provider["final"])
        self.assertFalse(provider["records"][0]["final"])
        self.assertEqual(provider["final_usage"], analyze.normalize(None))
        self.assertEqual(provider["records"][0]["final_usage"], analyze.normalize(None))
        self.assertEqual(provider["usage_details"], {"cache_creation": 64})
        client = result["surfaces"]["client_response"]
        self.assertEqual(client["usage_details"], {"cache_creation": 64})
        self.assertFalse(client["final"])
        self.assertEqual(client["dimensions"]["output"]["comparison"], "reference_unknown")
        self.assertEqual(result["provider_attempts"], 1)

    def test_anthropic_final_counts_are_cumulative_not_summed(self):
        event = self.event("reference", basis="provider_response_metadata", protocol="anthropic", final=False,
                           usage={"input_tokens": 2, "output_tokens": 0, "cache_creation_input_tokens": 64})
        events = [event, {**event, "usage": {"output_tokens": 3}},
                  {**event, "usage": {"output_tokens": 9}, "final": True}]
        result = analyze.provider_surface(events, "anthropic", "reference")
        self.assertEqual(result["final_usage"], {"input": 66, "output": 9, "cache_read": None})
        self.assertTrue(result["final"])
        self.assertTrue(result["records"][0]["final"])
        self.assertEqual(result["records"][0]["usage_event_count"], 3)
        self.assertEqual(result["usage_details"], {"cache_creation": 64})

    def test_anthropic_client_requires_final_output_delta_and_terminal(self):
        start = {"event": "message_start", "phase": "initial", "usage": {
            "input_tokens": 2, "output_tokens": 1, "cache_creation_input_tokens": 64}}
        delta = {"event": "message_delta", "phase": "final", "usage": {"output_tokens": 9}}
        for events, terminal, expected in (([start], True, False), ([start, delta], False, False),
                ([start, delta], True, True), ([start, {**delta, "usage": {}}], True, False),
                ([start, {**delta, "usage": {"output_tokens": None}}], True, False),
                ([start, {**delta, "phase": "partial"}], True, False),
                ([start, {"event": "message_delta", "usage": {"output_tokens": 0}}], True, True)):
            with self.subTest(events=events, terminal=terminal):
                result = analyze.analyze_case(self.root, self.case(case="stream_missing", client_protocol="anthropic",
                    usage_events=events, terminal=terminal))["surfaces"]["client_response"]
                self.assertIs(result["final"], expected)
                self.assertIs(result["records"][0]["final"], expected)
                self.assertEqual(result["usage_details"], {"cache_creation": 64})
                self.assertEqual(result["final_usage"], result["usage"] if expected else analyze.normalize(None))
        result = analyze.analyze_case(self.root, self.case(case="stream_missing", client_protocol="anthropic",
            usage_events=[start], terminal=True))["surfaces"]["client_response"]
        self.assertEqual(result["usage"]["output"], 1)
        self.assertEqual(result["usage_state"], "complete")
        self.assertTrue(result["records"][0]["terminal"])

    def test_native_nonstream_client_final_usage_does_not_require_delta(self):
        result = analyze.analyze_case(self.root, self.case(client_protocol="anthropic", usage_events=[{
            "event": "message", "phase": "final", "usage": {"input_tokens": 2, "output_tokens": 0}}]))
        self.assertTrue(result["surfaces"]["client_response"]["final"])
        self.assertEqual(result["surfaces"]["client_response"]["final_usage"]["output"], 0)

    def test_finality_does_not_follow_numeric_presence(self):
        for protocol in ("anthropic", "openai"):
            usage = {"input_tokens": 2, "output_tokens": 0} if protocol == "anthropic" else {
                "prompt_tokens": 2, "completion_tokens": 0}
            for flags, expected in (({}, True if protocol == "openai" else None),
                                    ({"final": False}, False), ({"final": "true"}, None),
                                    ({"final": True}, True)):
                with self.subTest(protocol=protocol, flags=flags):
                    result = analyze.provider_surface([self.event("reference", basis="provider_response_metadata",
                        usage=usage, **flags)], protocol, "reference")
                    self.assertIs(result["final"], expected)
                    self.assertEqual(result["usage_state"], "complete")
        result = analyze.provider_surface([self.event("reference", basis="provider_response_metadata",
            usage={"input_tokens": 2}, final=True)], "anthropic", "reference")
        self.assertTrue(result["final"])
        self.assertEqual(result["usage_state"], "partial")
        self.assertIsNone(result["final_usage"]["output"])

    def test_anthropic_mock_finality_requires_terminal_or_nonstream_write(self):
        usage = {"input_tokens": 2, "output_tokens": 0}
        start = self.event("delivered_usage", usage=usage, usage_event="message_start")
        delta = {**start, "usage": {"output_tokens": 3}, "usage_event": "message_delta"}
        terminal = self.event("stream_end", terminal_sent=True)
        for events, expected in (([start, delta], False), ([start, delta, terminal], True),
                                 ([start, delta, {**terminal, "attempt": 2}], False),
                                 ([{**start, "final": False}, terminal], False),
                                 ([self.event("delivered_usage", usage=usage)], True)):
            with self.subTest(events=events):
                self.assertIs(analyze.provider_surface(events, "anthropic", "delivered_usage")["final"], expected)

    def test_cache_creation_details_keep_zero_and_reject_invalid_counts(self):
        for creation in (0, 12, None, True, -1, 2**63, "PRIVATE", {"secret": "PRIVATE"}):
            with self.subTest(creation=creation):
                result = analyze.provider_surface([self.event("reference", final=True,
                    basis="provider_response_metadata", usage={"input_tokens": 2, "output_tokens": 0,
                    "cache_creation_input_tokens": creation})], "anthropic", "reference")
                self.assertEqual(result["usage_details"]["cache_creation"], analyze.count(creation))
                self.assertNotIn("PRIVATE", json.dumps(result))
                self.assertEqual(tuple(result["usage"]), analyze.DIMENSIONS)

    def test_multiple_reference_attempts_remain_separate(self):
        self.jsonl(self.root / "provider.jsonl", [self.event("reference", attempt=i,
                   basis="provider_response_metadata", usage={"prompt_tokens": 7}) for i in (1, 2)])
        result = analyze.analyze_case(self.root, self.case(), "real_provider")["surfaces"]["provider_response"]
        self.assertEqual(result["usage_state"], "ambiguous")
        self.assertEqual([r["usage"]["input"] for r in result["records"]], [7, 7])
        self.assertIsNone(result["final"])
        self.assertEqual(result["final_usage"], analyze.normalize(None))

    def test_unattributed_references_and_manifest_cut_mode(self):
        events = [self.event("reference", attempt=None, basis="provider_response_metadata",
                             usage={"prompt_tokens": 7})] * 2
        self.assertEqual(analyze.provider_surface(events, "openai", "reference")["usage_state"], "ambiguous")
        self.run_fixture(cases=["stream_cut"])
        manifest = json.loads((self.root / "manifest.json").read_text())
        manifest.update(mode="live_provider", live={"cut_mode": "cancel", "key_file": "/private/key"})
        self.write(self.root / "manifest.json", manifest)
        report = analyze.analyze_run(self.root)
        self.assertEqual(report["cut_mode"], "cancel")
        self.assertIn("Live cut mode: cancel", analyze.render_report(report))
        self.assertIn("pre-fault provider response metadata", analyze.render_report(report))
        self.assertIn("Mock comparisons use captured wire usage", analyze.render_report(report))
        self.assertNotIn("/private", json.dumps(report))

    def run_fixture(self, status="completed", cases=None, gateway="portkey", protocol="openai", client_protocol="openai"):
        cases = cases or ["complete"]
        pair = self.root / (gateway + "-" + protocol)
        self.write(self.root / "manifest.json", {"mode": "deterministic_mock", "cases": cases,
                   "images": {gateway: {"version": "1.15.2+exact.7", "image": "private/repository@sha256:" + "a" * 64}},
                   "machine": "PRIVATE_HOST", "sources": {"/private/path": "PRIVATE_HASH"}})
        self.write(pair / "outcome.json", {"status": status, "cases": cases, "project": "PRIVATE_PROJECT"})
        for case in cases:
            self.write(pair / (case + ".json"), self.case(gateway, case, client_protocol=client_protocol))
        return pair

    def test_live_anthropic_stream_complete_is_control_but_mock_probe_is_not(self):
        pair = self.run_fixture(cases=["stream_complete"], protocol="anthropic", client_protocol="anthropic")
        self.write(pair / "stream_complete.json", self.case("portkey", "stream_complete",
                   client_protocol="anthropic", terminal=False))
        self.assertEqual(len(analyze.analyze_run(self.root)["observations"]), 1)
        manifest = json.loads((self.root / "manifest.json").read_text())
        manifest.update(mode="live_provider", client_protocol="anthropic", live={"provider": "anthropic"})
        self.write(self.root / "manifest.json", manifest)
        report = analyze.analyze_run(self.root)
        self.assertFalse(report["observations"])
        self.assertEqual(report["excluded_runs"][0]["status"], "control_failed")
        self.write(pair / "stream_complete.json", self.case("portkey", "stream_complete", client_protocol="anthropic"))
        report = analyze.analyze_run(self.root)
        self.assertEqual(len(report["observations"]), 1)
        self.assertEqual(report["client_protocol"], "anthropic")
        self.assertEqual(report["live_provider"], "anthropic")
        self.assertEqual(report["observations"][0]["client_protocol"], "anthropic")
        table = analyze.render_report(report)
        self.assertIn("final=unknown", table)
        self.assertIn("Cache creation", table)
        self.assertIn("Usage state describes numeric presence, not finality", table)

    def test_client_protocol_must_match_explicit_manifest(self):
        self.run_fixture()
        self.write(self.root / "manifest.json", {"cases": ["complete"], "client_protocol": "anthropic"})
        report = analyze.analyze_run(self.root)
        self.assertFalse(report["observations"])
        self.assertEqual(report["excluded_runs"][0]["status"], "incomplete_or_unreadable")

    def test_incomplete_and_control_failed_runs_are_separate(self):
        pair = self.run_fixture(status="setup_or_control_failed")
        report = analyze.analyze_run(self.root)
        self.assertFalse(report["observations"])
        self.assertFalse(report["excluded_runs"][0]["gateway_fault"])
        self.write(pair / "outcome.json", {"status": "completed", "cases": ["complete"]})
        self.write(pair / "complete.json", self.case("portkey", status=500))
        self.assertEqual(analyze.analyze_run(self.root)["excluded_runs"][0]["status"], "control_failed")
        (pair / "outcome.json").unlink()
        self.assertEqual(analyze.analyze_run(self.root)["excluded_runs"][0]["status"], "incomplete_or_unreadable")

    def test_unfinished_matrix_and_unknown_case_path_are_not_analyzed(self):
        pair = self.run_fixture(cases=["complete", "stream_complete"])
        (pair / "stream_complete.json").unlink()
        self.assertFalse(analyze.analyze_run(self.root)["observations"])
        self.write(pair / "outcome.json", {"status": "completed", "cases": ["../private"]})
        self.assertFalse(analyze.analyze_run(self.root)["observations"])

    def test_generic_case_and_exact_manifest_version_four_surface_table(self):
        self.run_fixture(cases=["stream_abort"])
        data = analyze.write_report(self.root)
        self.assertEqual(len(data), 1)
        table = (self.root / "table.md").read_text()
        self.assertIn("1.15.2+exact.7", table)
        for name in analyze.SURFACES:
            self.assertEqual(table.count("| " + name + " |"), 1)
        report = json.loads((self.root / "analysis.json").read_text())
        self.assertEqual(report["schema_version"], 2)
        self.assertEqual(report["versions"]["portkey"]["image_digest"], "sha256:" + "a" * 64)

    def test_generated_outputs_never_include_raw_identifiers_paths_or_secrets(self):
        pair = self.run_fixture()
        secret = "PRIVATE_SECRET_SENTINEL"
        value = self.case("portkey", model=secret, request_id=secret, native_source="/private/source",
                          transport_error="/private/error " + secret, finish_reasons=[secret],
                          native={"status": 200, "records": [111, {"status": 200, "endpoint": "/private/api",
                          "requestOptions": [{"requestParams": {"messages": [secret]},
                          "providerOptions": {"apiKey": secret}, "transformedRequest": {"headers": {"authorization": secret}},
                          "response": {"id": secret, "usage": {"prompt_tokens": 0, "private": secret},
                                       "choices": [{"finish_reason": secret, "message": {"content": secret}}]}}]}]})
        self.write(pair / "complete.json", value)
        self.jsonl(pair / "provider.jsonl", [self.event("reference", basis="synthetic_configured_usage",
                   usage={"prompt_tokens": 100, "private": secret}, request_id=secret)])
        self.telemetry([self.span(secret)])
        (self.root / "telemetry.jsonl").rename(pair / "telemetry.jsonl")
        analyze.write_report(self.root)
        for path in (self.root / "analysis.json", self.root / "table.md"):
            output = path.read_text()
            for banned in (secret, "PRIVATE", "/private", "apiKey", "requestParams", "traceId", "spanId", "authorization"):
                self.assertNotIn(banned, output)


if __name__ == "__main__":
    unittest.main()
