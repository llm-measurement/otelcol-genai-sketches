# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Render the latest structured top-k snapshot from collector logs on stdin."""

import argparse
from datetime import datetime, timezone
import json
import sys


def latest_snapshot(lines):
    latest = None
    for line in lines:
        if "genaisketch topk snapshot" not in line:
            continue
        try:
            record = json.loads(line[line.index("{"):])
            payload = record["payload_json"]
            if isinstance(payload, str):
                payload = json.loads(payload)
            if payload.get("surface") == "genaisketch_topk":
                latest = payload
        except (ValueError, KeyError, TypeError, AttributeError):
            continue
    return latest


def render(snapshot, field="", output=sys.stdout):
    slices = [s for s in snapshot["slices"] if not field or s["field"] == field]
    if not slices:
        raise ValueError("No matching key in the latest snapshot; check topk_keys and incoming attributes.")
    generated = datetime.fromtimestamp(
        snapshot["generated_at_unix_nano"] / 1e9, timezone.utc
    ).isoformat()
    print(f"Latest snapshot: {generated} (not a cumulative total)", file=output)
    if snapshot.get("truncated"):
        print("Snapshot truncated at the global item limit.", file=output)
    for entry in slices:
        default_unit = "tool-error events" if entry["field"] == "tool_error_key" else "tokens"
        unit = entry.get("weight", default_unit)
        print(f"\n{entry['slice']} | {entry['slice_value']} | {entry['field']} | {unit}", file=output)
        print(f"Total weight: {entry['total_weight']} | Max error: {entry['max_error']}", file=output)
        print(f"{'Rank':>4}  {'Keyed hash':<16}  {'Estimate':>12}  {'Lower':>12}  {'Upper':>12}", file=output)
        for item in entry["items"]:
            print(
                f"{item['rank']:>4}  {item['hash']:<16}  {item['estimate']:>12}  "
                f"{item['lower_bound']:>12}  {item['upper_bound']:>12}",
                file=output,
            )


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("field", nargs="?", default="")
    args = parser.parse_args()
    snapshot = latest_snapshot(sys.stdin)
    if snapshot is None:
        parser.exit(1, "No top-k snapshot yet; allow one minute of traffic and check topk is nonzero.\n")
    try:
        render(snapshot, args.field)
    except (ValueError, KeyError, TypeError) as error:
        parser.exit(1, f"Cannot display snapshot: {error}\n")


if __name__ == "__main__":
    main()
