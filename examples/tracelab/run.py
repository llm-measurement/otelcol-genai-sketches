# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Replay the pinned release, investigate changes, and reconcile source totals."""

import argparse
import collections
import datetime as dt
import gzip
import hashlib
import hmac
import json
import os
from pathlib import Path
import platform
import secrets
import subprocess
import sys
import unicodedata

from audit import SHA256, audit, timestamp, verify

ROOT = Path(__file__).resolve().parents[2]
URL = "https://github.com/uw-syfi/TraceLab/releases/download/v0.0.2/syfi_coding_trace.jsonl.gz"
ATTRIBUTION = """Derived from TraceLab v0.0.2, SyFI Lab, University of Washington.
https://tracelab.cs.washington.edu/
https://github.com/uw-syfi/TraceLab/releases/tag/v0.0.2
Licensed CC BY 4.0: https://creativecommons.org/licenses/by/4.0/
Changes: event-time windowing, keyed identity hashes, aggregated counts and rankings.
No endorsement is implied. This directory contains derived data, not Apache-licensed code.
"""
COUNTERS = ("requests", "input_tokens", "output_tokens", "cache_read_input_tokens",
            "cache_write_input_tokens", "reasoning_output_tokens", "missing_token_usage")


def require(condition, message):
    if not condition:
        raise AssertionError(message)


def save(path, text):
    with path.open("x", encoding="utf-8") as f:
        os.chmod(path, 0o600)
        f.write(text)


def save_json(path, value):
    save(path, json.dumps(value, indent=2, sort_keys=True) + "\n")


def download(path):
    if path.exists():
        verify(path)
        return
    temporary = path.with_suffix(".download")
    if temporary.exists():
        raise RuntimeError("download temporary file already exists; review it before retrying")
    try:
        with temporary.open("xb") as target:
            os.chmod(temporary, 0o600)
            subprocess.run(["curl", "--fail", "--location", "--silent", "--show-error",
                            "--proto", "=https", "--proto-redir", "=https", "--max-time", "600",
                            URL], stdout=target, check=True)
        verify(temporary)
        temporary.rename(path)
    finally:
        temporary.unlink(missing_ok=True)


def identity_hash(secret, domain, text):
    canonical = unicodedata.normalize("NFC", text).replace("\r\n", "\n").replace("\r", "\n").strip()
    return hmac.new(secret.encode(), domain.encode() + b"\0" + canonical.encode(), hashlib.sha256).digest()[:8].hex()


def source_oracle(data, secret):
    """Exact raw-column sums and keyed counts for assertions only; emits no summaries."""
    days = collections.defaultdict(lambda: {"counters": collections.Counter(),
        "top_users": collections.Counter(), "top_sessions": collections.Counter()})
    with gzip.open(data, "rt", encoding="utf-8") as stream:
        for line in stream:
            r = json.loads(line)
            day = timestamp(r).astimezone(dt.timezone.utc).date().isoformat()
            target = days[day]
            c = target["counters"]
            c["requests"] += 1
            for source, counter in (("input_tokens_total", "input_tokens"), ("output_tokens", "output_tokens"),
                                    ("prefix_tokens", "cache_read_input_tokens"),
                                    ("claude_cache_creation_input_tokens", "cache_write_input_tokens"),
                                    ("reasoning_output_tokens", "reasoning_output_tokens")):
                c[counter] += r[source] or 0
            c["missing_token_usage"] += r["input_tokens_total"] is None or r["output_tokens"] is None
            c["token_observations.reasoning_output.subset_violation"] += (r["reasoning_output_tokens"] or 0) > r["output_tokens"]
            weight = (r["input_tokens_total"] or 0) + (r["output_tokens"] or 0)
            target["top_users"][identity_hash(secret, "user:v1", r["user"])] += weight
            session = json.dumps([r["user"], r["session_id"]], separators=(",", ":"), ensure_ascii=False)
            target["top_sessions"][identity_hash(secret, "session:v1", session)] += weight
    return days


def check_report(report, before, after):
    counters_checked = bounds_checked = 0
    counters = {c["name"]: c for c in report["evidence"]["counters"]}
    for name in COUNTERS:
        require(name in counters, f"missing counter: {name}")
        require(counters[name]["before"] == before["counters"].get(name, 0), name)
        require(counters[name]["after"] == after["counters"].get(name, 0), name)
        counters_checked += 2
    seen = set()
    for ranking in report["evidence"]["concentration"]:
        name = ranking["name"]
        if name not in ("top_users", "top_sessions"):
            continue
        seen.add(name)
        require(ranking["before_weight"] == sum(before[name].values()), f"{name} before total")
        require(ranking["after_weight"] == sum(after[name].values()), f"{name} after total")
        for item in ranking["tracked_movers"]:
            a, b = before[name].get(item["hash"], 0), after[name].get(item["hash"], 0)
            for field, exact in (("before", a), ("after", b), ("delta", b-a)):
                require(item[field]["lower"] <= exact <= item[field]["upper"], f"{name} {field}")
                bounds_checked += 1
    require(seen == {"top_users", "top_sessions"}, "missing rankings")
    return counters_checked, bounds_checked


def run(args):
    os.umask(0o077)
    cache = args.cache.resolve()
    cache.mkdir(mode=0o700, parents=True, exist_ok=True)
    data = cache / "syfi_coding_trace.jsonl.gz"
    download(data)
    out = args.out.resolve()
    out.mkdir(mode=0o700)
    save(out / "ATTRIBUTION.txt", ATTRIBUTION)
    secret = secrets.token_hex(32)
    save(out / "hash-key.txt", secret)
    env = dict(os.environ, GENAI_SKETCH_SECRET=secret, GOWORK="off",
               GOCACHE=str(ROOT / ".cache/go-build"), GOMODCACHE=str(ROOT / ".cache/go-mod"))
    commands = []

    def execute(cmd, acceptable=(0,)):
        commands.append([str(x) for x in cmd])
        result = subprocess.run(cmd, cwd=ROOT, env=env, capture_output=True, text=True, timeout=900)
        if result.returncode not in acceptable:
            raise RuntimeError(f"command failed ({result.returncode}): {result.stderr}")
        return result.stdout, result.returncode

    version, _ = execute([str(args.fleetdiff.resolve()), "--version"])
    if not version.startswith("fleetdiff v0.5.0 "):
        raise RuntimeError("use the verified fleetdiff v0.5.0 binary for this regression")
    print("Auditing TraceLab v0.0.2...", flush=True)
    release_audit = audit(data)
    save_json(out / "audit.json", release_audit)
    oracle = source_oracle(data, secret)
    save_json(out / "source-oracle.json", oracle)
    binary = out / "tracelab-replay"
    execute(["go", "-C", "connector/genaisketchconnector", "build", "-tags", "tracelab_replay",
             "-o", str(binary), "./cmd/tracelab-replay"])
    replay, _ = execute([str(binary), "--input", str(data), "--out", str(out / "windows")])
    save(out / "replay.txt", replay)
    print(replay.strip(), flush=True)
    files = sorted((out / "windows").glob("*.json"))
    for path in files:
        doc = json.loads(path.read_text())
        for name in (*COUNTERS, "token_observations.reasoning_output.subset_violation"):
            exact = oracle[path.stem]["counters"].get(name, 0)
            require(doc["counters"][name] == exact, f"{path.stem} {name}")
    # Fixed adjacent dates, all contributors, no choice based on flag strength.
    base = [str(args.fleetdiff.resolve()), "investigate", "--expected", "tracelab",
            "--before", str(out / "windows/2026-06-01.json"),
            "--after", str(out / "windows/2026-06-02.json")]
    text, _ = execute(base)
    save(out / "investigate.txt", text)
    print(text.splitlines()[0], flush=True)
    reports = out / "regression"
    reports.mkdir(mode=0o700)
    counts = collections.Counter()
    for before, after in zip(files, files[1:]):
        cmd = [str(args.fleetdiff.resolve()), "investigate", "--expected", "tracelab",
               "--before", str(before), "--after", str(after), "--format", "json", "--show-hashes", "--top", "100"]
        raw, _ = execute(cmd)
        report = json.loads(raw)
        c, b = check_report(report, oracle[before.stem], oracle[after.stem])
        counts["counter_values_checked"] += c
        counts["returned_bounds_checked"] += b
        counts["adjacent_comparisons"] += 1
        save(reports / f"{before.stem}_{after.stem}.json", raw)
    revision, _ = execute(["git", "rev-parse", "HEAD"])
    diff, _ = execute(["git", "diff", "--", "connector/genaisketchconnector"])
    source_digests = {str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest()
                      for base in (ROOT / "connector/genaisketchconnector", Path(__file__).parent)
                      for p in sorted(base.rglob("*")) if p.is_file() and p.suffix in (".go", ".py", ".mod", ".sum")}
    result = {"dataset_sha256": SHA256, "fleetdiff": version.strip(), "platform": platform.platform(),
              "collector_base_commit": revision.strip(), "tracked_diff_sha256": hashlib.sha256(diff.encode()).hexdigest(),
              "source_sha256": source_digests, "replay_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
              "fleetdiff_sha256": hashlib.sha256(args.fleetdiff.read_bytes()).hexdigest(),
              "checks": dict(counts), "commands": commands}
    save_json(out / "run.json", result)
    print(json.dumps({"checks": counts}, indent=2))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--fleetdiff", type=Path, required=True)
    parser.add_argument("--cache", type=Path, default=ROOT / ".cache/tracelab-v0.0.2")
    parser.add_argument("--out", type=Path, default=ROOT / ".cache/tracelab-v0.0.2/run")
    try:
        run(parser.parse_args())
    except (ValueError, RuntimeError, OSError, AssertionError) as error:
        print(f"TraceLab example failed: {error}", file=sys.stderr)
        sys.exit(1)
