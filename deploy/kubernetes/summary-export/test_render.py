# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Render the runtime fixture's actual commands without starting Kubernetes."""
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import unittest

SPEC = importlib.util.spec_from_file_location("summary_check", Path(__file__).with_name("check.py"))
CHECK = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(CHECK)


class RenderTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.helm = os.environ.get("HELM") or shutil.which("helm")
        if not cls.helm:
            raise RuntimeError("Helm is required for summary fixture render tests")

    def render(self, cmd):
        return subprocess.run([str(arg) for arg in cmd], capture_output=True, text=True, timeout=30)

    def test_empty_dir_keeps_shadow_disabled(self):
        cmd = CHECK.collector_command(self.helm, "ephemeral")
        self.assertIn("shadow.enabled=false", cmd)
        self.assertNotIn("--set-json", cmd)
        result = self.render(cmd)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("kind: NetworkPolicy", result.stdout)

    def test_pvc_allows_only_backend_peer_and_port(self):
        cmd = CHECK.collector_command(self.helm, "persistent", pvc=True)
        self.assertIn("shadow.endpoint=backend:4317", cmd)
        self.assertIn("shadow.enabled=true", cmd)
        assignment = cmd[cmd.index("--set-json") + 1]
        name, value = assignment.split("=", 1)
        self.assertEqual(name, "networkPolicy.egress")
        self.assertEqual(json.loads(value), [{"to": [{"podSelector": {"matchLabels": {"app": "backend"}}}],
                                             "ports": [{"protocol": "TCP", "port": 4317}]}])
        result = self.render(cmd)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("kind: NetworkPolicy", result.stdout)
        self.assertIn('claimName: "summary-data"', result.stdout)

    def test_pvc_without_egress_still_refused(self):
        cmd = CHECK.collector_command(self.helm, "persistent", pvc=True)
        index = cmd.index("--set-json")
        del cmd[index:index + 2]
        result = self.render(cmd)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("shadow.enabled requires networkPolicy.egress for the backend", result.stderr)


if __name__ == "__main__":
    unittest.main()
