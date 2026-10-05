# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Positive controls for the integration test's privacy scanner."""

import base64
import io
from pathlib import Path
import subprocess
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from check import SENTINELS, hash_key, no_sentinels, run, verify_readme_reports, workload

INVESTIGATE = (
    "1 of 10 tracked sessions flagged for review: 91.00% of attributed model attempts.\n"
    "Reported tokens: 12000 -> 12000; model attempts: 100 -> 100.\n"
)
SCAN = (
    "1 unusual windows among 1 checked.\n\n2026-10-05T22:41:00Z\n"
    "  session-1 now holds 91% of model attempts (newly prominent in this scan).\n"
    "  user-1 now holds 91% of tokens (newly prominent in this scan).\n"
)


class PrivacyChecks(unittest.TestCase):
    def test_every_sentinel_is_detected(self):
        for sentinel in SENTINELS:
            with self.subTest(sentinel=sentinel):
                self.assertFalse(no_sentinels(b'prefix "' + sentinel.encode() + b'" suffix'))
                encoded = base64.b64encode(b"protobuf fixture " + sentinel.encode())
                self.assertFalse(no_sentinels(base64.b64decode(encoded)))

    def test_pseudonymous_output_passes(self):
        self.assertTrue(no_sentinels(b'{"hash":"abcdef0123456789","lower_bound":91}'))

    def test_domains_and_workflow_namespace_are_distinct(self):
        secret = "only-a-synthetic-test-value"
        self.assertNotEqual(hash_key(secret, "user:v1", "one"), hash_key(secret, "session:v1", "one"))
        self.assertNotEqual(hash_key(secret, "user:v1", "one"), hash_key(secret, "user:v1", "workflow:one"))

    def test_identity_cardinality_changes_without_changing_volume(self):
        self.assertEqual([sum(workload(n)) for n in range(8)], [100] * 8)
        self.assertEqual(sum(bool(n) for n in workload(0)), 1)
        self.assertEqual(sum(bool(n) for n in workload(1)), 10)
        self.assertEqual(workload(7)[0], 91)

    def test_readme_query_is_the_tested_query(self):
        root = Path(__file__).parent
        documented = (root / "README.md").read_text().split("```logql\n", 1)[1].split("```", 1)[0]
        self.assertEqual(documented, (root / "workflows.logql").read_text())


class ReportChecks(unittest.TestCase):
    def test_readme_excerpts_match_saved_text(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "investigate.txt").write_text(INVESTIGATE)
            (root / "scan.txt").write_text(SCAN)
            verify_readme_reports(root)

    def test_changed_report_is_rejected(self):
        for name in ("investigate", "scan"):
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                (root / "investigate.txt").write_text(INVESTIGATE)
                (root / "scan.txt").write_text(SCAN)
                report = root / f"{name}.txt"
                report.write_text(report.read_text().replace("91", "90"))
                with self.assertRaisesRegex(RuntimeError, f"README {name} output differs"):
                    verify_readme_reports(root)


class CleanupChecks(unittest.TestCase):
    def test_cleanup_preserves_original_error_with_missing_credentials(self):
        for down_result in (0, 1, OSError("docker unavailable")):
            with self.subTest(down_result=down_result), tempfile.TemporaryDirectory() as directory:
                def cleanup_command(args, **_):
                    if "down" in args:
                        if isinstance(down_result, OSError):
                            raise down_result
                        return subprocess.CompletedProcess(args, down_result, "", "")
                    return subprocess.CompletedProcess(args, 0, "", "")

                root = Path(directory) / "run"
                args = SimpleNamespace(output=root, fleetdiff="fleetdiff", gateway="agentgateway")
                with patch("check.command", side_effect=RuntimeError("original recipe failure")), \
                        patch("check.subprocess.run", side_effect=cleanup_command), \
                        patch("check.sys.stderr", new_callable=io.StringIO) as stderr:
                    with self.assertRaisesRegex(RuntimeError, "^original recipe failure$"):
                        run(args)
                self.assertEqual("Cleanup incomplete" in stderr.getvalue(), down_result != 0)
                for name in ("jwt-key", "jwks.json", "tokens.json"):
                    self.assertFalse((root / name).exists())


if __name__ == "__main__":
    unittest.main()
