# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Source mapping, privacy controls and documentation checks without Docker."""

import base64
from collections import Counter
import io
import json
from pathlib import Path
import subprocess
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import run as recipe


class FixtureChecks(unittest.TestCase):
    def test_session_precedence_resource_locality_and_flat_accounting(self):
        for source in recipe.SOURCES:
            for window in range(8):
                with self.subTest(source=source, window=window):
                    records = recipe.fixture(source, window, 10**18)["resourceSpans"]
                    sessions, devices = Counter(), Counter()
                    tokens = 0
                    for record in records:
                        resource = recipe.attrs(record["resource"])
                        span = recipe.attrs(record["scopeSpans"][0]["spans"][0])
                        if source == "cli":
                            session = span["gen_ai.conversation.id"]
                        else:
                            session = span.get("copilot_chat.session_id", resource["session.id"])
                            self.assertNotEqual(session, span["gen_ai.conversation.id"])
                        sessions[session] += 1
                        devices[resource["device.id"]] += 1
                        self.assertNotEqual(resource["device.id"], span["device.id"])
                        self.assertEqual("service.instance.id" in resource, window != 6)
                        self.assertEqual(span["gen_ai.input.messages"], recipe.CODE)
                        self.assertEqual(span["github.copilot.tool.parameters.file_path"], recipe.FILE_PATH)
                        tokens += int(span["gen_ai.usage.input_tokens"]) + int(span["gen_ai.usage.output_tokens"])
                    self.assertEqual(len(records), 100)
                    self.assertEqual(tokens, 12000)
                    expected = recipe.workload(window)
                    self.assertEqual([sessions[s] for s in recipe.SESSIONS], expected)
                    self.assertEqual([devices[d] for d in recipe.DEVICES], expected)

    def test_codex_native_shape_does_not_invent_model_or_identity_fields(self):
        body = json.loads((recipe.ROOT / "fixtures/codex-handle-responses.json").read_text())
        record = body["resourceSpans"][0]
        span = record["scopeSpans"][0]["spans"][0]
        attributes = recipe.attrs(span)
        self.assertEqual(span["name"], "completed")
        self.assertEqual(attributes["gen_ai.usage.input_tokens"], "100")
        self.assertEqual(attributes["gen_ai.usage.output_tokens"], "20")
        for fields in (attributes, recipe.attrs(record["resource"])):
            self.assertTrue({"gen_ai.operation.name", "gen_ai.request.model", "conversation.id", "user.id"}.isdisjoint(fields))


class PrivacyChecks(unittest.TestCase):
    def test_every_sentinel_is_detected(self):
        for sentinel in recipe.SENTINELS:
            with self.subTest(sentinel=sentinel):
                self.assertFalse(recipe.no_sentinels(sentinel.encode()))

    def test_safe_hash_passes(self):
        self.assertTrue(recipe.no_sentinels(b'{"hash":"abcdef0123456789"}'))

    def test_encoded_state_is_scanned(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "summary.json").write_text(json.dumps({"sketches": {"sample": {
                "data": base64.b64encode(recipe.CODE.encode()).decode()}}}))
            with self.assertRaisesRegex(RuntimeError, "decoded state"):
                recipe.scan_outputs(root, "test-only-secret")

    def test_only_raw_shadow_copy_is_exempt(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "backend.jsonl").write_text(recipe.FILE_PATH)
            with patch("sys.stdout", new_callable=io.StringIO):
                recipe.scan_outputs(root, "test-only-secret")
            (root / "collector.log").write_text(recipe.FILE_PATH)
            with self.assertRaisesRegex(RuntimeError, "derived output"):
                recipe.scan_outputs(root, "test-only-secret")


class DocumentationChecks(unittest.TestCase):
    def test_query_is_exactly_the_documented_query(self):
        text = (recipe.ROOT / "README.md").read_text()
        query = text.split("```logql\n", 1)[1].split("```", 1)[0]
        self.assertEqual(query, (recipe.ROOT / "devices.logql").read_text())

    def test_readme_reports_require_saved_output(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            reports = {
                "investigate": "1 of 10 tracked sessions flagged for review: 91.00% of attributed model attempts.\n"
                               "Reported tokens: 12000 -> 12000; model attempts: 100 -> 100.\n",
                "scan": "1 unusual windows among 1 checked.\n",
            }
            for name, text in reports.items():
                (root / f"{name}.txt").write_text(text)
            recipe.verify_readme_reports(root)
            for name, text in reports.items():
                (root / f"{name}.txt").write_text("changed output\n")
                with self.assertRaisesRegex(RuntimeError, f"README {name} excerpt differs"):
                    recipe.verify_readme_reports(root)
                (root / f"{name}.txt").write_text(text)


class CleanupChecks(unittest.TestCase):
    def test_cleanup_does_not_hide_original_failure(self):
        with tempfile.TemporaryDirectory() as directory:
            def commands(args, *_):
                if "--version" in args:
                    return subprocess.CompletedProcess(args, 0, "fleetdiff v0.5.0 (test)\n", "")
                raise RuntimeError("original recipe failure")

            args = SimpleNamespace(output=Path(directory) / "run", fleetdiff="fleetdiff")
            with patch.object(recipe, "command", side_effect=commands), \
                    patch.object(recipe.subprocess, "run", side_effect=OSError("docker unavailable")), \
                    patch("sys.stderr", new_callable=io.StringIO) as stderr:
                with self.assertRaisesRegex(RuntimeError, "^original recipe failure$"):
                    recipe.run(args)
            self.assertIn("Cleanup incomplete", stderr.getvalue())


if __name__ == "__main__":
    unittest.main()
