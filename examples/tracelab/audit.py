# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Audit the pinned normalized release; never execute upstream collection code."""

import argparse
import collections
import datetime as dt
import gzip
import hashlib
import json
from pathlib import Path

SHA256 = "11ce51ec0a25e3d1d95b025bca2f7d1647e47571eb7cc968acd5fc64d4b4fb65"
MODEL_EVENTS = {"text", "reasoning", "tool_call", "usage_report"}
FIELDS = ("input_tokens_total", "output_tokens", "prefix_tokens", "newly_append_tokens",
          "claude_cache_creation_input_tokens", "reasoning_output_tokens")


def verify(path):
    with path.open("rb") as stream:
        if hashlib.file_digest(stream, "sha256").hexdigest() != SHA256:
            raise ValueError("TraceLab v0.0.2 checksum mismatch")


def timestamp(row):
    events = [dt.datetime.fromisoformat(e["timestamp"].replace("Z", "+00:00"))
              for e in row["timing_events"] if e["event_type"] in MODEL_EVENTS]
    return max(events) if events else None


def audit(path):
    verify(path)
    totals, issues, providers, event_types = (collections.Counter() for _ in range(4))
    models = collections.defaultdict(collections.Counter)
    users, sessions, keys = set(), set(), set()
    model_users, model_dates = (collections.defaultdict(set) for _ in range(2))
    days = collections.defaultdict(collections.Counter)
    with gzip.open(path, "rt", encoding="utf-8") as stream:
        for line in stream:
            row = json.loads(line)
            totals["steps"] += 1
            totals["tools"] += len(row["tools"])
            users.add(row["user"])
            sessions.add((row["user"], row["session_id"]))
            key = (row["user"], row["trace_key"])
            issues["duplicate_trace_key"] += key in keys
            keys.add(key)
            providers[row["provider"]] += 1
            model = row["model"]
            models[model]["steps"] += 1
            model_users[model].add(row["user"])
            time = timestamp(row)
            if time is None:
                issues["no_model_timestamp"] += 1
            else:
                day = time.astimezone(dt.timezone.utc).date().isoformat()
                days[day]["steps"] += 1
                model_dates[model].add(day)
            for event in row["timing_events"]:
                event_types[event["event_type"]] += 1
            for field in FIELDS:
                value = row.get(field)
                if value is None:
                    issues["null_" + field] += 1
                    continue
                if not isinstance(value, int) or value < 0:
                    issues["invalid_" + field] += 1
                    continue
                totals[field] += value
                models[model][field] += value
                if time is not None:
                    days[day][field] += value
            inp, cached, new = (row.get(k) for k in
                                ("input_tokens_total", "prefix_tokens", "newly_append_tokens"))
            if all(v is not None for v in (inp, cached, new)):
                issues["input_split_mismatch"] += inp != cached + new
            if row["provider"] == "claude":
                parts = [row.get(k) for k in ("claude_uncached_input_tokens",
                         "claude_cache_creation_input_tokens", "claude_cache_read_input_tokens")]
                if all(v is not None for v in parts):
                    issues["claude_input_mismatch"] += inp != sum(parts)
                    issues["claude_read_mismatch"] += cached != parts[2]
            if (row.get("reasoning_output_tokens") or 0) > (row.get("output_tokens") or 0):
                issues["reasoning_subset_violation"] += 1
    for model, counts in models.items():
        counts["users"] = len(model_users[model])
        dates = sorted(model_dates[model])
        counts["first_date"] = dates[0] if dates else None
        counts["last_date"] = dates[-1] if dates else None
        counts["dates"] = len(dates)
    return {"release": "v0.0.2", "sha256": SHA256,
            "timestamp_rule": "latest text/reasoning/tool_call/usage_report event, UTC",
            "totals": dict(totals, users=len(users), sessions=len(sessions)),
            "issues": dict(issues), "providers": dict(providers), "event_types": dict(event_types),
            "models": dict(sorted(models.items())), "days": dict(sorted(days.items()))}


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("data", type=Path)
    args = parser.parse_args()
    print(json.dumps(audit(args.data), indent=2, sort_keys=True))
