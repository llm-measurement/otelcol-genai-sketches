#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Package only the dependency updater's files, without modifying Git history."""

import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys

FILES = (
    "otel.version",
    "builder.yaml",
    "go.mod",
    "go.sum",
    "connector/genaisketchconnector/go.mod",
    "connector/genaisketchconnector/go.sum",
)


def git(*args: str) -> bytes:
    return subprocess.check_output(["git", *args])


def prepare(destination: Path) -> bool:
    changed = git("diff", "--name-only", "-z", "HEAD").decode().split("\0")
    changed = [name for name in changed if name]
    if set(changed) - set(FILES):
        raise ValueError("Unexpected modified files; start from a clean main checkout.")
    if git("diff", "--cached", "--name-only") or git(
        "ls-files", "--others", "--exclude-standard"
    ):
        raise ValueError(
            "Staged or untracked files found; start from a clean main checkout."
        )
    if not changed:
        return False
    if any(Path(name).is_symlink() or not Path(name).is_file() for name in changed):
        raise ValueError("Dependency updates must preserve regular files.")
    version = Path("otel.version").read_text().strip()
    if not re.fullmatch(r"v0\.[0-9]+\.[0-9]+", version):
        raise ValueError("Expected a stable Collector v0 release.")
    patch = git(
        "diff",
        "--binary",
        "--full-index",
        "--no-ext-diff",
        "--no-textconv",
        "HEAD",
        "--",
        *FILES,
    )
    manifest = {
        "base_sha": git("rev-parse", "HEAD").decode().strip(),
        "collector_version": version,
        "files": sorted(changed),
        "patch_sha256": hashlib.sha256(patch).hexdigest(),
    }
    destination.mkdir(mode=0o700)
    (destination / "update.patch").write_bytes(patch)
    (destination / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    return True


def main() -> int:
    if len(sys.argv) != 2:
        print("Usage: prepare_otel_update.py NEW_OUTPUT_DIRECTORY", file=sys.stderr)
        return 2
    try:
        changed = prepare(Path(sys.argv[1]))
    except (OSError, ValueError, subprocess.CalledProcessError) as exc:
        print(f"Could not prepare dependency patch: {exc}", file=sys.stderr)
        return 1
    if output := os.environ.get("GITHUB_OUTPUT"):
        with open(output, "a", encoding="utf-8") as handle:
            handle.write(f"changed={str(changed).lower()}\n")
    message = (
        "Dependency patch prepared. Review the artifact, rerun checks, and open a signed, signed-off PR."
        if changed
        else "Collector dependencies are current; no patch to review."
    )
    print(message)
    if summary := os.environ.get("GITHUB_STEP_SUMMARY"):
        with open(summary, "a", encoding="utf-8") as handle:
            handle.write(
                message
                + "\n\nSee .github/SECURITY_MAINTENANCE.md for the review steps.\n"
            )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
