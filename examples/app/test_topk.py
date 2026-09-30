# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
import io
import json
import unittest

from topk import latest_snapshot, render


def snapshot(field="prompt_key", **extra):
    return {
        "surface": "genaisketch_topk",
        "generated_at_unix_nano": 1_800_000_000_000_000_000,
        "truncated": False,
        "slices": [{
            "slice": "by_model", "slice_value": "model=demo", "field": field,
            "total_weight": 100, "max_error": 5,
            "items": [{"rank": 1, "hash": "0123456789abcdef", "estimate": 80,
                       "lower_bound": 75, "upper_bound": 80}],
            **extra,
        }],
    }


class TopKTest(unittest.TestCase):
    def test_console_and_json_logs_use_latest(self):
        first, second = snapshot(), snapshot("session_key", weight="requests")
        lines = [
            "unrelated log\n",
            'time\tinfo\tgenaisketch topk snapshot\t' + json.dumps({"payload_json": json.dumps(first)}),
            "genaisketch topk snapshot {bad json",
            json.dumps({"msg": "genaisketch topk snapshot", "payload_json": json.dumps(second)}),
        ]
        self.assertEqual(latest_snapshot(lines[:2]), first)
        self.assertEqual(latest_snapshot(lines), second)
        self.assertIsNone(latest_snapshot(["no snapshot"]))

    def test_units_bounds_and_field_selection(self):
        for field, extra, unit in [
            ("prompt_key", {}, "tokens"), ("user_key", {}, "tokens"),
            ("session_key", {"weight": "requests"}, "requests"),
            ("tool_error_key", {}, "tool-error events"),
        ]:
            with self.subTest(field=field):
                output = io.StringIO()
                render(snapshot(field, **extra), field, output)
                self.assertIn(f"{field} | {unit}", output.getvalue())
                self.assertIn("75", output.getvalue())
                self.assertIn("80", output.getvalue())
                self.assertIn("0123456789abcdef", output.getvalue())
        with self.assertRaisesRegex(ValueError, "No matching key"):
            render(snapshot(), "user_key", io.StringIO())

    def test_truncation_is_visible(self):
        data = snapshot()
        data["truncated"] = True
        output = io.StringIO()
        render(data, output=output)
        self.assertIn("truncated", output.getvalue())


if __name__ == "__main__":
    unittest.main()
