# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex

import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import verify


class Verification(unittest.TestCase):
    def test_standard_controls_required_but_compatible_probe_keeps_observation(self):
        def surface(comparison="equal"):
            return {"reader_status": "observed", "records": [{}], "usage_state": "complete", "final": True,
                    "dimensions": {d: {"comparison": comparison} for d in ("input", "output")}}
        def row(case, comparison="equal"):
            return {"gateway": "portkey", "upstream_protocol": "anthropic", "case": case,
                    "provider_attempts": 1, "surfaces": {"client_response": surface(comparison),
                    "provider_response": {**surface(), "final": True},
                    "gateway_accounting": surface(), "exported_telemetry": {"reader_status": "reader_unsupported"}}}
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "manifest.json").write_text(json.dumps({"cases": ["complete", "stream_standard", "stream_complete"]}))
            (root / "outcomes.json").write_text(json.dumps([{"status": "completed"}]))
            report = {"excluded_runs": [], "observations": [row("complete"), row("stream_standard"),
                                                          row("stream_complete", "under_counted")]}
            with patch("verify.analyze_run", return_value=report):
                self.assertEqual(verify.verify(root), [])
                report["observations"][1] = row("stream_standard", "under_counted")
                self.assertTrue(any("control disagrees" in e for e in verify.verify(root)))
                report["observations"][1] = row("stream_standard")
                (root / "manifest.json").write_text(json.dumps({
                    "cases": ["complete", "stream_standard", "stream_complete"], "live": {"provider": "anthropic"}}))
                self.assertTrue(any("stream_complete: client control disagrees" in e for e in verify.verify(root)))
                report["observations"] = []
                self.assertTrue(any("every requested case" in e for e in verify.verify(root)))

    def run_fixture(self, client_protocol="anthropic"):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        root = Path(tmp.name)
        pair = root / "portkey-anthropic"
        pair.mkdir()
        cases = ["complete", "stream_complete", "cache_write", "cache_read"]
        (root / "manifest.json").write_text(json.dumps({"cases": cases, "mode": "live_provider",
            "client_protocol": client_protocol, "live": {"provider": "anthropic", "cut_mode": "cancel"}}))
        outcome = {"status": "completed", "cases": cases}
        (root / "outcomes.json").write_text(json.dumps([outcome]))
        (pair / "outcome.json").write_text(json.dumps(outcome))
        events = []
        for index, case in enumerate(cases):
            start = 100 + index * 100
            usage = {"input_tokens": 2, "output_tokens": 3,
                     "cache_creation_input_tokens": 64 if case == "cache_write" else 0,
                     "cache_read_input_tokens": 64 if case == "cache_read" else 0}
            # Cache loss is deliberately observable, including an input-total
            # mismatch; cache scenarios are not gateway-equivalence controls.
            response_usage = {"input_tokens": 2, "output_tokens": 3} if client_protocol == "anthropic" else {
                "prompt_tokens": 2, "completion_tokens": 3}
            value = {"client_protocol": client_protocol, "started_ns": start, "deadline_ns": start + 10,
                     "status": 200, "terminal": True, "usage_events": [{"usage": response_usage}],
                     "native": {"status": 200, "records": [{"status": 200, "requestOptions": [{
                         "response": {"usage": response_usage}}]}]}}
            if case == "stream_complete" and client_protocol == "anthropic":
                value["usage_events"][0].update(event="message_delta", phase="final")
            (pair / (case + ".json")).write_text(json.dumps(value))
            common = {"case": case, "attempt": 1, "protocol": "anthropic", "time_ns": start + 1}
            events += [{**common, "event": "relay_attempt"}, {**common, "event": "provider_forward"},
                       {**common, "event": "reference", "basis": "provider_response_metadata", "usage": usage, "final": True}]
        (pair / "provider.jsonl").write_text("".join(json.dumps(e) + "\n" for e in events))
        return root, pair, events

    def test_live_cache_evidence_passes_without_gateway_cache_mirroring(self):
        for client_protocol in ("anthropic", "openai"):
            with self.subTest(client_protocol=client_protocol):
                root, _, _ = self.run_fixture(client_protocol)
                self.assertEqual(verify.verify(root), [])
                report = verify.analyze_run(root)
                self.assertFalse(report["excluded_runs"])
                cache = next(row for row in report["observations"] if row["case"] == "cache_read")
                self.assertEqual(cache["surfaces"]["client_response"]["dimensions"]["input"]["comparison"], "under_counted")
                self.assertEqual(cache["surfaces"]["gateway_accounting"]["dimensions"]["cache_read"]["comparison"], "absent")

    def test_cache_cases_require_positive_provider_evidence(self):
        for case, field in (("cache_write", "cache_creation_input_tokens"), ("cache_read", "cache_read_input_tokens")):
            for value in (None, 0, -1, True, "64"):
                with self.subTest(case=case, value=value):
                    root, pair, events = self.run_fixture()
                    reference = next(e for e in events if e["case"] == case and e["event"] == "reference")
                    if value is None:
                        reference["usage"].pop(field)
                    else:
                        reference["usage"][field] = value
                    (pair / "provider.jsonl").write_text("".join(json.dumps(e) + "\n" for e in events))
                    errors = verify.verify(root)
                    self.assertTrue(any("does not prove a positive " + case in e for e in errors), errors)
                    self.assertFalse(verify.analyze_run(root)["excluded_runs"])

    def test_controls_and_cache_cases_require_final_numeric_provider_counts(self):
        for case in ("complete", "stream_complete", "cache_write", "cache_read"):
            for mutation in ("provisional", "missing_final", "missing_output"):
                with self.subTest(case=case, mutation=mutation):
                    root, pair, events = self.run_fixture()
                    reference = next(e for e in events if e["case"] == case and e["event"] == "reference")
                    if mutation == "provisional":
                        reference["final"] = False
                    elif mutation == "missing_final":
                        reference.pop("final")
                    else:
                        reference["usage"].pop("output_tokens")
                    (pair / "provider.jsonl").write_text("".join(json.dumps(e) + "\n" for e in events))
                    errors = verify.verify(root)
                    self.assertTrue(any(case + ": provider reference lacks final" in e for e in errors), errors)

    def test_configured_cache_counts_do_not_prove_live_cache_events(self):
        root, pair, events = self.run_fixture()
        for event in events:
            if event["event"] == "reference" and event["case"] in ("cache_write", "cache_read"):
                event["basis"] = "synthetic_configured_usage"
        (pair / "provider.jsonl").write_text("".join(json.dumps(e) + "\n" for e in events))
        errors = verify.verify(root)
        for case in ("cache_write", "cache_read"):
            self.assertTrue(any("does not prove a positive " + case in e for e in errors), errors)

    def test_stream_control_needs_final_delta_even_when_initial_counts_match(self):
        root, pair, _ = self.run_fixture()
        path = pair / "stream_complete.json"
        value = json.loads(path.read_text())
        value["usage_events"][0].update(event="message_start", phase="initial")
        path.write_text(json.dumps(value))
        errors = verify.verify(root)
        self.assertEqual(errors, ["portkey/anthropic/stream_complete: client control lacks final usage"])


if __name__ == "__main__":
    unittest.main()
