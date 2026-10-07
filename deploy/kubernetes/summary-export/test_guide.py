# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
import os
from pathlib import Path
import re
import subprocess
import unittest


GUIDE = Path(__file__).resolve().parents[3] / "docs" / "SUMMARY_EXPORT_HELM.md"
STUB = r'''
fleetdiff() {
  case "$1" in
    investigate)
      [[ "$#" == 7 && "$2" == --before && "$3" == "$BEFORE" &&
         "$4" == --after && "$5" == "$AFTER" &&
         "$6" == --expected && "$7" == app ]] || return 97
      ;;
    scan)
      [[ "$#" == 8 && "$2" == ./summaries-NEW &&
         "$3" == --expected && "$4" == app &&
         "$5" == --baseline && "$6" == 6 &&
         "$7" == --as-of && "$8" == "$AS_OF" ]] || return 97
      ;;
    *) return 97 ;;
  esac
  printf '%s arguments verified\n' "$1"
  if [[ "$1" == scan ]]; then return "$SCAN_EXIT"; fi
}
'''


class GuideTests(unittest.TestCase):
    def setUp(self):
        section = GUIDE.read_text().split("## Copy, Investigate, Scan\n", 1)[1]
        section = section.split("\n## ", 1)[0]
        blocks = [block for block in re.findall(r"```sh\n(.*?)\n```", section, re.S)
                  if "fleetdiff investigate" in block and "fleetdiff scan" in block]
        self.assertEqual(len(blocks), 1, "Expected one investigate/scan shell block")
        self.script = STUB + blocks[0]
        self.env = {key: value for key, value in os.environ.items()
                    if key not in {"BASH_ENV", "ENV", "BEFORE", "AFTER", "AS_OF"}}
        self.env.update(
            BEFORE="./summaries-NEW/before window's $(not-a-command).json",
            AFTER="./summaries-NEW/after window's $(not-a-command).json",
            AS_OF="2026-10-07T22:00:00+00:00",
            SCAN_EXIT="0",
        )

    def run_block(self):
        return subprocess.run(
            [os.environ.get("BASH_UNDER_TEST", "/bin/bash"),
             "--noprofile", "--norc", "-e", "-s"],
            input=self.script, text=True, capture_output=True, env=self.env, timeout=10,
        )

    def test_guide_preserves_arguments_and_scan_status(self):
        for status in (0, 3, 4):
            with self.subTest(scan_exit=status):
                self.env["SCAN_EXIT"] = str(status)
                result = self.run_block()
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(result.stderr, "")
                self.assertEqual(result.stdout, (
                    "investigate arguments verified\n"
                    "scan arguments verified\n"
                    f"scan exit: {status}\n"
                ))

    def test_guide_requires_each_copy_assignment(self):
        for name in ("BEFORE", "AFTER", "AS_OF"):
            with self.subTest(missing=name):
                value = self.env.pop(name)
                try:
                    result = self.run_block()
                finally:
                    self.env[name] = value
                self.assertNotEqual(result.returncode, 0)
                self.assertIn(f"{name}: run the copy helper first", result.stderr)
                self.assertNotIn("scan arguments verified", result.stdout)


if __name__ == "__main__":
    unittest.main()
