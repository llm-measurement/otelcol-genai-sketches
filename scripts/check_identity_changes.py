# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex

"""Require a new changelog entry when an existing identity baseline changes."""

import argparse
import re
import subprocess
import sys


def git_diff(base: str, head: str, *args: str, paths: tuple[str, ...] = ()) -> str:
    return subprocess.run(
        [
            "git", "diff", "--no-ext-diff", "--no-textconv", "--no-color",
            *args, base, head, "--", *paths,
        ],
        check=True,
        capture_output=True,
        text=True,
    ).stdout


def check_changes(changed_paths: str, changelog_diff: str, paths: list[str]) -> bool:
    changed = set(changed_paths.split("\0"))
    if not changed.intersection(paths):
        return True
    added: set[str] = set()
    removed: set[str] = set()
    for line in changelog_diff.splitlines():
        if line.startswith(("+- Identity change: ", "-- Identity change: ")):
            note = line[1:].strip()
            if note.removeprefix("- Identity change:").strip():
                (added if line[0] == "+" else removed).add(note)
    # Moving or removing an old note does not document a new identity change.
    return bool(added - removed)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base", required=True)
    parser.add_argument("--head", required=True)
    parser.add_argument("--golden", action="append", required=True)
    args = parser.parse_args(argv)
    if any(
        not re.fullmatch(r"[0-9a-fA-F]{40}|[0-9a-fA-F]{64}", ref)
        for ref in (args.base, args.head)
    ):
        parser.error("base and head must be full commit IDs")
    try:
        # New fixtures need no migration. Existing fixtures cannot be rewritten
        # or removed without a new entry describing the identity change.
        changed = git_diff(
            args.base, args.head, "--name-only", "-z", "--no-renames",
            "--diff-filter=MDT",
        )
        notes = git_diff(args.base, args.head, "--unified=0", paths=("CHANGELOG.md",))
    except (subprocess.CalledProcessError, OSError):
        print(
            "Could not compare the identity baseline with the base commit.",
            file=sys.stderr,
        )
        return 1
    if not check_changes(changed, notes, args.golden):
        print(
            "Identity baseline changed. Add a new '- Identity change: ' "
            "entry to CHANGELOG.md "
            "naming the affected inputs, cause and handling of older summaries.",
            file=sys.stderr,
        )
        return 1
    print("Identity baseline changelog check passed.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
