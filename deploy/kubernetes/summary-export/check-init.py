# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Run the chart's exact initialization script against private synthetic volumes."""
from pathlib import Path
import secrets
import subprocess

REPO = Path(__file__).resolve().parents[3]
IMAGE = "docker.io/library/busybox:1.37.0-musl@sha256:5cec3fc171c87218698e85a52af7087de727372aae264a787b8112901a5b0092"
SCRIPT = REPO / "deploy/helm/otelcol-genai-sketches/files/init-summary.sh"
NAME = "00000000000000000020-" + "a" * 32 + ".json"


def run():
    cases = {
        "fresh": ("", True),
        "remount": (f"mkdir exports; touch exports/{NAME}; chmod 770 exports; chmod 660 exports/{NAME}; chown -R 65532:65532 exports", True),
        "foreign-owner": ("mkdir exports; chmod 777 exports", False),
        "directory-link": ("mkdir other; ln -s other exports", False),
        "file-link": (f"mkdir exports; ln -s /etc/passwd exports/{NAME}; chown 65532:65532 exports", False),
        "hard-link": (f"mkdir exports; touch unrelated; ln unrelated exports/{NAME}; chown 65532:65532 exports exports/{NAME}", False),
        "unknown-file": ("mkdir exports; touch exports/unrelated; chown -R 65532:65532 exports", False),
        "foreign-file": (f"mkdir exports; touch exports/{NAME}; chown 65532:65532 exports", False),
        "nested-directory": ("mkdir -p exports/nested; chown -R 65532:65532 exports", False),
    }
    for name, (setup, passing) in cases.items():
        volume = "summary-init-" + secrets.token_hex(4)
        try:
            subprocess.run(["docker", "volume", "create", volume], check=True, capture_output=True)
            base = ["docker", "run", "--rm", "--network=none", "--read-only", "--cap-drop=ALL",
                    "--security-opt=no-new-privileges", "-v", volume + ":/var/lib/genai-sketches", "-w", "/var/lib/genai-sketches"]
            subprocess.run([*base, "--user=0:0", "--cap-add=CHOWN", IMAGE, "sh", "-ec",
                            "chmod 777 .; touch keep; chmod 640 keep; " + setup], check=True, capture_output=True)
            result = subprocess.run([*base, "--user=65532:65532", "-v", str(SCRIPT) + ":/init.sh:ro", IMAGE,
                                     "sh", "/init.sh"], capture_output=True)
            if (result.returncode == 0) != passing:
                raise RuntimeError("Unexpected init result: " + name)
            verify = 'test "$(stat -c %a keep)" = 640'
            if passing:
                verify += '; test "$(stat -c %a exports)" = 700; for f in exports/*.json; do [ ! -f "$f" ] || test "$(stat -c %a "$f")" = 600; done'
            subprocess.run([*base, "--user=65532:65532", IMAGE, "sh", "-ec", verify], check=True, capture_output=True)
            print("PASS init:", name)
        finally:
            subprocess.run(["docker", "volume", "rm", volume], check=True, capture_output=True)


if __name__ == "__main__":
    run()
