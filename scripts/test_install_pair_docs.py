# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Offline checks for the combined collector/chart installation instructions."""
from pathlib import Path
import re
import subprocess
import unittest

ROOT = Path(__file__).resolve().parents[1]


class InstallPairDocsTests(unittest.TestCase):
    def test_readme_pair(self):
        source = (ROOT / "README.md").read_text()
        self.assertIn("| Collector image | `v0.6.0` |", source)
        self.assertIn("| Helm chart | `0.6.0` |", source)

    def test_standalone_connector_pin(self):
        source = (ROOT / "connector/genaisketchconnector/README.md").read_text()
        self.assertIn("gomod: github.com/llm-measurement/otelcol-genai-sketches/connector/genaisketchconnector v0.6.0", source)
        self.assertIn("../../docs/UPGRADING.md#sketchkit-030-in-this-source-candidate", source)

    def test_api_verification_requires_signed_in_cli(self):
        source = (ROOT / "docs/DEPLOYMENT.md").read_text()
        self.assertIn("signed in for OCI provenance API checks", source)

    def test_both_artifacts_use_the_runtime_release_identity(self):
        source = (ROOT / "docs/DEPLOYMENT.md").read_text()
        self.assertIn("\nRELEASE=v0.6.0\n", source)
        self.assertIn("\nCHART_RELEASE=v0.6.0\nCHART_VERSION=0.6.0\n", source)
        for variable in ("RELEASE", "CHART_RELEASE"):
            identity = f".github/workflows/release.yml@refs/tags/${{{variable}}}"
            self.assertEqual(source.count(identity), 2)
        self.assertNotIn("chart-v0.6.0", source)
        self.assertNotIn("chart-release.yml", source)

    def test_metadata_is_authenticated_before_checksums(self):
        source = (ROOT / "docs/DEPLOYMENT.md").read_text()
        self.assertIn("for subject in SHA256SUMS sbom-index.json openvex.json; do", source)
        self.assertIn("--bundle release-assets/provenance.jsonl", source)
        self.assertIn('--source-ref "refs/tags/${RELEASE}" --deny-self-hosted-runners', source)
        self.assertLess(source.index('gh attestation verify "release-assets/$subject"'),
                        source.index("shasum -a 256 -c SHA256SUMS"))

    def test_summary_guide_pins_source_chart_and_verified_image(self):
        source = (ROOT / "docs/SUMMARY_EXPORT_HELM.md").read_text()
        install, validation = source.split("## Validation", 1)
        self.assertIn("--branch v0.6.0", install)
        self.assertEqual(install.count("--version 0.6.0"), 2)
        self.assertEqual(install.count('--set-string image.digest="${DIGEST:?'), 2)
        self.assertIn("**32 one-minute windows**", install)
        self.assertIn("**26 complete windows**", install)
        self.assertIn("collector v0.3.2 and fleetdiff v0.5.0", validation)
        self.assertNotIn("collector v0.6.0", validation)

    def test_install_shell_blocks_parse_without_execution(self):
        for name in ("DEPLOYMENT.md", "SUMMARY_EXPORT_HELM.md"):
            source = (ROOT / "docs" / name).read_text()
            for index, command in enumerate(re.findall(r"```(?:bash|sh)\n(.*?)\n```", source, re.S)):
                with self.subTest(document=name, block=index):
                    result = subprocess.run(["bash", "-n"], input=command,
                                            text=True, capture_output=True)
                    self.assertEqual(result.returncode, 0, result.stderr)


if __name__ == "__main__":
    unittest.main()
