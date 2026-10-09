# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex

import copy
import datetime as dt
import gzip
import io
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from audit import audit, timestamp, verify
from run import COUNTERS, check_report, download, identity_hash, run


class ExampleTests(unittest.TestCase):
    def test_checksum_fails_closed(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "data.gz"
            path.write_bytes(b"not-the-pinned-release")
            with self.assertRaisesRegex(ValueError, "checksum mismatch"):
                verify(path)
            with patch("run.subprocess.run") as request:
                with self.assertRaises(ValueError):
                    download(path)
                request.assert_not_called()

    def test_bad_download_not_kept(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "data.gz"
            with patch("run.subprocess.run") as request:
                def response(*args, **kwargs):
                    kwargs["stdout"].write(b"bad-release")
                request.side_effect = response
                with self.assertRaises(ValueError):
                    download(path)
                self.assertFalse(path.exists())
                self.assertFalse(path.with_suffix(".download").exists())

    def test_time_does_not_follow_tools(self):
        row = {"timing_events": [
            {"event_type": "usage_report", "timestamp": "2026-06-01T00:00:00Z"},
            {"event_type": "tool_result", "timestamp": "2026-06-02T00:00:00Z"}]}
        self.assertEqual(timestamp(row), dt.datetime(2026, 6, 1, tzinfo=dt.timezone.utc))

    def test_exact_oracle_catches_missing_and_wrong_counters_and_bounds(self):
        truth = {"counters": dict.fromkeys(COUNTERS, 0), "top_users": {"hash": 10}, "top_sessions": {"hash": 10}}
        report = {"evidence": {"counters": [{"name": k, "before": 0, "after": 0} for k in COUNTERS],
            "concentration": [{"name": k, "before_weight": 10, "after_weight": 10,
                "tracked_movers": [{"hash": "hash", "before": {"lower": 10, "upper": 10},
                                    "after": {"lower": 10, "upper": 10}, "delta": {"lower": 0, "upper": 0}}]}
                              for k in ("top_users", "top_sessions")]}}
        self.assertEqual(check_report(report, truth, truth), (14, 6))
        bad = copy.deepcopy(report)
        bad["evidence"]["counters"].pop()
        with self.assertRaises(AssertionError):
            check_report(bad, truth, truth)
        bad = copy.deepcopy(report)
        bad["evidence"]["concentration"][0]["tracked_movers"][0]["after"]["upper"] = 9
        with self.assertRaises(AssertionError):
            check_report(bad, truth, truth)
        bad = copy.deepcopy(report)
        bad["evidence"]["counters"][0]["after"] = 1
        with self.assertRaises(AssertionError):
            check_report(bad, truth, truth)

    def test_hash_separation(self):
        secret = "test-key-at-least-32-bytes-123456789"
        self.assertNotEqual(identity_hash(secret, "user:v1", "id"), identity_hash(secret, "session:v1", "id"))
        self.assertEqual(identity_hash(secret, "user:v1", "  e\u0301\r\n"), identity_hash(secret, "user:v1", "\u00e9"))

    def test_audit_covers_rows_without_pair_analysis(self):
        row = {"user": "label-a", "session_id": "session-a", "trace_key": "round-a",
               "provider": "codex", "model": "model-a", "tools": [{"tool_name": "exec_command"}],
               "timing_events": [{"event_type": "usage_report", "timestamp": "2026-06-01T00:00:00Z"}],
               "input_tokens_total": 100, "output_tokens": 10, "prefix_tokens": 80,
               "newly_append_tokens": 20, "reasoning_output_tokens": 11}
        second = dict(row, trace_key="round-b", output_tokens=20, reasoning_output_tokens=0)
        with tempfile.TemporaryDirectory() as tmp:
            data = Path(tmp) / "fixture.jsonl.gz"
            with gzip.open(data, "wt", encoding="utf-8") as stream:
                for value in (row, second):
                    stream.write(json.dumps(value) + "\n")
            with patch("audit.verify") as checked:
                result = audit(data)
            checked.assert_called_once_with(data)
        self.assertEqual(set(result), {"release", "sha256", "timestamp_rule", "totals",
                                      "issues", "providers", "event_types", "models", "days"})
        self.assertEqual(result["totals"]["steps"], 2)
        self.assertEqual(result["totals"]["input_tokens_total"], 200)
        self.assertEqual(result["totals"]["output_tokens"], 30)
        self.assertEqual(result["totals"]["sessions"], 1)
        self.assertEqual(result["issues"]["reasoning_subset_violation"], 1)
        self.assertEqual(result["models"]["model-a"]["users"], 1)
        self.assertEqual(result["days"]["2026-06-01"]["steps"], 2)

    def test_runner_only_investigates_and_reconciles(self):
        truth = {"counters": dict.fromkeys(COUNTERS, 0), "top_users": {}, "top_sessions": {}}
        report = {"evidence": {"counters": [{"name": k, "before": 0, "after": 0} for k in COUNTERS],
            "concentration": [{"name": k, "before_weight": 0, "after_weight": 0, "tracked_movers": []}
                              for k in ("top_users", "top_sessions")]}}
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp).resolve()
            binary = root / "fleetdiff"
            binary.write_bytes(b"test binary; never executed")
            args = SimpleNamespace(fleetdiff=binary, cache=root / "cache", out=root / "out")

            def execute(cmd, **kwargs):
                if cmd[0] == "go":
                    Path(cmd[cmd.index("-o") + 1]).write_bytes(b"test replay; never executed")
                    output = ""
                elif Path(cmd[0]).name == "tracelab-replay":
                    windows = args.out / "windows"
                    windows.mkdir()
                    counters = dict(truth["counters"], **{"token_observations.reasoning_output.subset_violation": 0})
                    for day in ("2026-06-01", "2026-06-02"):
                        (windows / f"{day}.json").write_text(json.dumps({"counters": counters}))
                    output = "replayed test fixtures"
                elif cmd == [str(binary), "--version"]:
                    output = "fleetdiff v0.5.0 (fixture)"
                elif cmd[:2] == [str(binary), "investigate"]:
                    output = json.dumps(report) if "--format" in cmd else "Recorded tokens unchanged"
                elif cmd == ["git", "rev-parse", "HEAD"]:
                    output = "fixture-revision"
                elif cmd[:2] == ["git", "diff"]:
                    output = ""
                else:
                    self.fail(f"unexpected command: {cmd}")
                return SimpleNamespace(returncode=0, stdout=output, stderr="")

            with patch("run.ROOT", root), patch("run.__file__", str(root / "run.py")), \
                 patch("run.platform.platform", return_value="fixture"), \
                 patch("run.os.umask"), patch("run.download"), \
                 patch("run.audit", return_value={"totals": {"steps": 0}}), \
                 patch("run.source_oracle", return_value={"2026-06-01": truth, "2026-06-02": truth}), \
                 patch("run.subprocess.run", side_effect=execute), patch("sys.stdout", new_callable=io.StringIO):
                run(args)
            result = json.loads((args.out / "run.json").read_text())
            self.assertEqual(result["checks"], {"adjacent_comparisons": 1,
                "counter_values_checked": 14, "returned_bounds_checked": 0})
            self.assertNotIn("scan", result)
            self.assertNotIn("rolling_scan", result)
            self.assertEqual({p.name for p in args.out.iterdir()}, {"ATTRIBUTION.txt", "hash-key.txt",
                "audit.json", "source-oracle.json", "tracelab-replay", "replay.txt", "windows",
                "investigate.txt", "regression", "run.json"})


if __name__ == "__main__":
    unittest.main()
