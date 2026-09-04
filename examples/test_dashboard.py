# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay Erramilli and Codex
import json
import unittest
from pathlib import Path


class DashboardTest(unittest.TestCase):
    def test_one_slice_and_no_addition_of_distinct_estimates(self):
        dashboard = json.loads(
            (
                Path(__file__).parent / "grafana/dashboards/genai-sketches.json"
            ).read_text()
        )
        queries = [
            target
            for panel in dashboard["panels"]
            for target in panel.get("targets", [])
        ]
        self.assertEqual(len(queries), 6)
        for target in queries:
            with self.subTest(query=target["expr"]):
                self.assertIn('slice="$slice"', target["expr"])
                if "gen_ai_sketch_distinct_" in target["expr"]:
                    self.assertTrue(
                        target["expr"].startswith("gen_ai_sketch_distinct_")
                    )
                    self.assertTrue(target["instant"])
        selector = dashboard["templating"]["list"][0]
        self.assertEqual(selector["query"], "by_model,by_team_model")


if __name__ == "__main__":
    unittest.main()
