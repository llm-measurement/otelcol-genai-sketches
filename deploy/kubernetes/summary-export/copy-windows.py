# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Copy completed summary windows through the chart's read-only reader sidecar."""

import argparse
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import re
import shlex
import shutil
import subprocess
import tarfile
import time

EXPORTS = "/var/lib/genai-sketches/exports"
NAME = re.compile(r"[0-9]{20}-[a-f0-9]{32}\.json")
MAX_BYTES = 64 << 20


def copy_windows(kubectl, namespace, pod, output):
    """Return complete windows only; fail closed and discard partial copies."""
    os.umask(0o077)
    output = Path(output)
    output.mkdir(mode=0o700, parents=True, exist_ok=False)
    temporary = output / ".snapshot.tar"
    command = [kubectl, "--request-timeout=120s", "-n", namespace, "exec", pod, "-c", "reader", "--"]
    try:
        listing = subprocess.run([*command, "ls", "-1", EXPORTS], check=True,
                                 capture_output=True, text=True, timeout=120).stdout
        names = [name for name in listing.splitlines() if NAME.fullmatch(name)]
        if not names or len(names) > 512:
            raise ValueError("No summary files available, or export file limit exceeded")
        # No shell interpolation; tar only receives validated, flat filenames.
        with temporary.open("xb") as file:
            subprocess.run([*command, "tar", "-C", EXPORTS, "-cf", "-", *sorted(names)],
                           stdout=file, stderr=subprocess.PIPE, check=True, timeout=120)
        if temporary.stat().st_size > MAX_BYTES + (2 << 20):
            raise ValueError("Summary archive exceeds the export limit")
        documents = []
        now = time.time_ns()
        seen = set()
        size = 0
        with tarfile.open(temporary, "r:") as archive:
            for member in archive:
                if not member.isfile() or member.name not in names or member.name in seen:
                    raise ValueError("Unexpected summary archive entry")
                size += member.size
                if size > MAX_BYTES:
                    raise ValueError("Summary data exceeds the export limit")
                seen.add(member.name)
                data = archive.extractfile(member).read()
                doc = json.loads(data)
                start, duration = doc["window_start_unix_nano"], doc["window_duration_unix_nano"]
                if start + duration > now or doc["emitted_at_unix_nano"] > now:
                    continue
                if duration > 0 and doc["observed_start_unix_nano"] == start and doc["observed_end_unix_nano"] == start + duration:
                    documents.append((member.name, data, doc))
        if seen != set(names):
            raise ValueError("Incomplete summary archive")
        if not documents:
            raise ValueError("No fully observed completed windows yet")
        if len({(d["producer_id"], d["epoch"]) for _, _, d in documents}) != 1:
            raise ValueError("Multiple producer epochs: copy each process epoch separately")
        documents.sort(key=lambda entry: entry[2]["window_start_unix_nano"])
        if len(documents) < 2:
            raise ValueError("Need at least two completed windows for comparison")
        if len({d["window_start_unix_nano"] for _, _, d in documents}) != len(documents):
            raise ValueError("Duplicate summary window")
        for name, data, _ in documents:
            with (output / name).open("xb") as file:
                file.write(data)
        temporary.unlink(missing_ok=True)
        return [output / name for name, _, _ in documents], datetime.fromtimestamp(now / 1e9, timezone.utc).isoformat()
    except BaseException:
        shutil.rmtree(output)
        raise


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--kubectl", default="kubectl")
    parser.add_argument("--namespace", required=True)
    parser.add_argument("--pod", required=True)
    parser.add_argument("--output", type=Path, required=True, help="New private directory")
    args = parser.parse_args()
    try:
        files, as_of = copy_windows(args.kubectl, args.namespace, args.pod, args.output)
    except (OSError, ValueError, KeyError, TypeError, tarfile.TarError, subprocess.SubprocessError):
        parser.exit(1, "Copy failed: check reader access and completed windows; no partial copy retained.\n")
    print(f"Copied {len(files)} completed windows. Use these assignments for the latest pair:")
    for name, value in (("BEFORE", str(files[-2])), ("AFTER", str(files[-1])), ("AS_OF", as_of)):
        print(f"{name}={shlex.quote(value)}")


if __name__ == "__main__":
    main()
