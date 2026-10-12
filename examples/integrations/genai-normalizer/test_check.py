# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
import copy
import unittest

import check


class RecipeChecks(unittest.TestCase):
    def test_readme_matches_checked_result(self):
        self.assertIn("```text\n" + check.RESULT + "\n```", (check.HERE / "README.md").read_text())

    def test_missing_rankings_are_rejected(self):
        with self.assertRaisesRegex(AssertionError, "rankings missing"):
            check.verify_snapshots('{"msg":"started"}')

    def test_fixture_has_both_dialects_and_sensitive_sentinels(self):
        rows = check.spans([check.fixture()])
        self.assertEqual(len(rows), 8)
        self.assertIn("openinference.span.kind", check.attrs(rows[0]))
        self.assertIn("llm.request.type", check.attrs(rows[1]))
        self.assertIn("file.path", check.attrs(rows[0]))

    def test_metrics_reject_partial_totals(self):
        text = "\n".join(f'gen_ai_sketch_{name}{{slice="by_model"}} {value}' for name, value in [
            ("requests_total", 5), ("input_tokens_total", 307),
            ("output_tokens_total", 56), ("missing_token_usage_total", 2)])
        check.verify_metrics(text)
        with self.assertRaises(AssertionError):
            check.verify_metrics(text.replace("307", "306"))

    def test_raw_branch_mutation_is_rejected(self):
        sent = check.fixture()
        changed = copy.deepcopy(sent)
        check.spans([changed])[0]["attributes"].append(
            {"key": "gen_ai.operation.name", "value": {"stringValue": "chat"}})
        with self.assertRaisesRegex(AssertionError, "raw branch changed"):
            check.verify_traces(sent, [changed], [])


if __name__ == "__main__":
    unittest.main()
