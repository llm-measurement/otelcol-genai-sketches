# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Compare source-shaped synthetic coding-agent windows on released binaries."""

import argparse
import base64
import copy
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import subprocess
import sys
import time
import urllib.parse

ROOT = Path(__file__).resolve().parent
sys.path.insert(0, str(ROOT.parent / "agentgateway"))
from check import attrs, check, command, hash_key, request, spans, workload  # noqa: E402

WINDOW = 20
SOURCES = {"cli": "copilot-cli", "chat": "copilot-chat"}
SESSIONS = [f"SENTINEL_CODING_SESSION_{i}" for i in range(10)]
DEVICES = [f"SENTINEL_CODING_DEVICE_{i}" for i in range(10)]
CODE = "def SENTINEL_CODE(): return 'private source'"
FILE_PATH = "/synthetic/SENTINEL_PATH/private_source.py"
SENTINELS = ["SENTINEL_", CODE, FILE_PATH]


def no_sentinels(data):
    return not any(value.encode() in data for value in SENTINELS)


def put(attributes, key, value):
    attributes[:] = [a for a in attributes if a["key"] != key]
    if value is not None:
        attributes.append({"key": key, "value": {"stringValue": value}})


def fixture(source, window, start):
    template = json.loads((ROOT / "fixtures" / (SOURCES[source] + ".json")).read_text())
    resources = []
    ordinal = 0
    for i, count in enumerate(workload(window)):
        for _ in range(count):
            record = copy.deepcopy(template["resourceSpans"][0])
            resource = record["resource"]["attributes"]
            span = record["scopeSpans"][0]["spans"][0]
            span.update(traceId=f"{start + ordinal:032x}", spanId=f"{ordinal + 1:016x}",
                        startTimeUnixNano=str(start), endTimeUnixNano=str(start + 1000000))
            put(resource, "device.id", DEVICES[i])
            # Deliberately collapse, vary, and omit instance IDs without changing identities.
            put(resource, "service.instance.id", ("shared-instance" if window < 4 else
                f"instance-{i}") if window != 6 else None)
            a = span["attributes"]
            if source == "cli":
                put(a, "gen_ai.conversation.id", SESSIONS[i])
                put(resource, "session.id", "SENTINEL_RESOURCE_SESSION_MUST_NOT_WIN")
            else:
                put(a, "gen_ai.conversation.id", f"SENTINEL_REQUEST_{start}_{ordinal}")
                put(a, "copilot_chat.session_id", SESSIONS[i] if ordinal % 2 == 0 else None)
                put(resource, "session.id", "SENTINEL_WINDOW_MUST_NOT_WIN" if ordinal % 2 == 0 else SESSIONS[i])
            # Adversarial extras: not a claim that content-off agents emit these fields.
            put(a, "session.id", "SENTINEL_SPAN_SESSION_MUST_NOT_WIN")
            put(a, "device.id", "SENTINEL_SPAN_DEVICE_MUST_NOT_WIN")
            put(a, "gen_ai.input.messages", CODE)
            put(a, "github.copilot.tool.parameters.file_path", FILE_PATH)
            resources.append(record)
            ordinal += 1
    return {"resourceSpans": resources}


def drive(root, window, start):
    for source in SOURCES:
        body = fixture(source, window, start)
        response = json.loads(request(f"http://{source}:4318/v1/traces", body,
                                      {"Content-Type": "application/json"}))
        check(not response.get("partialSuccess"), "collector rejected synthetic spans")
        (root / source / f"metrics-{window}.txt").write_bytes(request(f"http://{source}:8889/metrics"))


def probe_codex(root):
    body = json.loads((ROOT / "fixtures/codex-handle-responses.json").read_text())
    span = body["resourceSpans"][0]["scopeSpans"][0]["spans"][0]
    span.update(traceId="1" * 32, spanId="1" * 16, startTimeUnixNano=str(time.time_ns()),
                endTimeUnixNano=str(time.time_ns() + 1000))
    response = json.loads(request("http://cli:4318/v1/traces", body, {"Content-Type": "application/json"}))
    check(not response.get("partialSuccess"), "collector rejected Codex fixture")
    data = request("http://cli:8889/metrics")
    (root / "codex-metrics.txt").write_bytes(data)
    for line in data.decode().splitlines():
        if line.startswith(("gen_ai_sketch_requests_total", "gen_ai_sketch_input_tokens_total",
                            "gen_ai_sketch_output_tokens_total")):
            check(float(line.rsplit(" ", 1)[-1]) == 0, "Codex probe unexpectedly counted")


def verify_readme_reports(root):
    text = (ROOT / "README.md").read_text()
    for name in ("investigate", "scan"):
        blocks = re.findall(rf"<!-- {name}-output -->\n```text\n(.*?)```", text, re.DOTALL)
        check(len(blocks) == 1 and blocks[0].strip(), "missing README report excerpt")
        check(blocks[0] in (root / f"{name}.txt").read_text(), f"README {name} excerpt differs")


def verify_source(root, source, starts, secret, fleetdiff):
    folder = root / source
    docs = {doc["window_start_unix_nano"]: (path, doc)
            for path in (folder / "summaries").glob("*.json")
            for doc in [json.loads(path.read_text())] if doc["window_start_unix_nano"] in starts}
    check(set(docs) == set(starts), "missing summary windows")
    archive = folder / "archive"
    archive.mkdir()
    for start, (path, doc) in docs.items():
        check((doc["observed_start_unix_nano"], doc["observed_end_unix_nano"]) ==
              (start, start + WINDOW * 10**9), "partial summary")
        counters = doc["counters"]
        check([counters[k] for k in ("requests", "input_tokens", "output_tokens", "missing_token_usage")]
              == [100, 10000, 2000, 0], "flat accounting changed")
        check({"top_sessions_requests", "top_key.device_key.tokens"} <= doc["sketches"].keys(),
              "missing session/device sketch")
        check("top_users" not in doc["sketches"], "device presented as a user")
        shutil.copyfile(path, archive / f"{start}.json")
    before, after = [str(archive / f"{start}.json") for start in starts[-2:]]
    investigate = [fleetdiff, "investigate", "--before", before, "--after", after, "--expected", "app"]
    report = json.loads(command([*investigate, "--format", "json", "--show-hashes"]).stdout)
    question = next(q for q in report["questions"] if q["id"] == "sessions")
    leader = next(c for c in question["contributors"] if c["hash"] == hash_key(secret, "session:v1", SESSIONS[0]))
    check(leader["after_share"] == {"lower": .91, "upper": .91} and
          leader.get("flag") == "runaway_candidate", "wrong flagged session")
    (folder / "investigate.json").write_text(json.dumps(report))
    (folder / "investigate.txt").write_text(command(investigate).stdout)
    as_of = datetime.now(timezone.utc).isoformat()
    (folder / "as-of.txt").write_text(as_of + "\n")
    scan = [fleetdiff, "scan", str(archive), "--expected", "app", "--baseline", "6", "--as-of", as_of]
    result = command([*scan, "--format", "json", "--show-hashes"], accepted=(3,))
    scanned = json.loads(result.stdout)
    check(scanned["unusual_windows"] == 1, "expected one unusual window")
    check(any(f.get("measurement") == "top_sessions_requests" for f in scanned["windows"][0]["findings"]),
          "scan missed session concentration")
    (folder / "scan.json").write_text(result.stdout)
    (folder / "scan.txt").write_text(command(scan, accepted=(3,)).stdout)
    verify_readme_reports(folder)
    quiet = folder / "quiet"
    quiet.mkdir()
    for start in starts[:-1]:
        shutil.copyfile(archive / f"{start}.json", quiet / f"{start}.json")
    result = command([fleetdiff, "scan", str(quiet), "--expected", "app", "--baseline", "6",
                      "--as-of", as_of, "--format", "json"])
    check(json.loads(result.stdout)["unusual_windows"] == 0, "quiet history flagged")
    (folder / "quiet.json").write_text(result.stdout)

    snapshots = [s for line in (root / f"{source}.log").read_text().splitlines()
                 for record in [json.loads(line)] if record.get("msg") == "genaisketch topk snapshot"
                 for s in json.loads(record["payload_json"])["slices"]]
    for field, domain, values, multiplier in (("session_key", "session:v1", SESSIONS, 1),
                                             ("device_key", "user:v1", DEVICES, 120)):
        for index, start in enumerate(starts):
            expected = {hash_key(secret, domain, v): n * multiplier
                        for v, n in zip(values, workload(index)) if n}
            candidates = [s for s in snapshots if s["field"] == field and s["window_start_unix_nano"] == start
                          and s["total_weight"] == 100 * multiplier]
            check(candidates, "populated snapshot missing")
            items = candidates[-1]["items"]
            check({i["hash"] for i in items} == expected.keys(), "wrong identity source or membership")
            check(all(i["lower_bound"] <= expected[i["hash"]] <= i["upper_bound"] for i in items),
                  "bounds fail to bracket fixture truth")
    allowed = {"slice", "slice_value", "overflow", "token_field", "state", "source",
               "otel_scope_name", "otel_scope_version", "otel_scope_schema_url"}
    series = []
    for path in sorted(folder.glob("metrics-*.txt")):
        lines = [s for s in path.read_text().splitlines() if s.startswith("gen_ai_sketch_")]
        check(lines, "empty metrics scrape")
        for line in lines:
            check(set(re.findall(r'(\w+)="(?:[^"\\]|\\.)*"', line)) <= allowed, "unexpected metric label")
            check(not re.search(r'="[0-9a-f]{16}"', line), "hash metric label")
        series.append({s.rsplit(" ", 1)[0] for s in lines})
    check(len(series) == 8 and all(s == series[0] for s in series), "identity-dependent metric series")
    print(f"PASS {SOURCES[source]}: 8 flat windows, 91% flagged session, exact device membership/bounds; "
          f"scan=3, quiet=0; {len(series[0])} fixed metric series; README excerpts match")


def verify_loki(root):
    now = time.time_ns()
    expected_labels = []
    for source, name in SOURCES.items():
        rows = [line for line in (root / f"{source}.log").read_text().splitlines()
                if json.loads(line).get("msg") == "genaisketch topk snapshot"]
        check(rows and no_sentinels("\n".join(rows).encode()), "missing or unsafe snapshot logs")
        labels = {"service_name": "otelcol-genai-sketches", "source": name}
        expected_labels.append(labels)
        request("http://loki:3100/loki/api/v1/push", {"streams": [
            {"stream": labels, "values": [[str(now + i), line] for i, line in enumerate(rows)]}
        ]}, {"Content-Type": "application/json"})
        query = (ROOT / "devices.logql").read_text().replace('source="copilot-cli"', f'source="{name}"')
        params = {"start": str(now - 10**9), "end": str(now + 10**9)}
        result = json.loads(request("http://loki:3100/loki/api/v1/query_range?" + urllib.parse.urlencode({
            **params, "query": query, "limit": 5000})))
        check(result["status"] == "success", "LogQL failed")
        lines = [line for stream in result["data"]["result"] for _, line in stream["values"]]
        check(any("tokens=10920" in line and "bounds=[10920,10920]" in line for line in lines),
              "device leader absent in tested LogQL")
        (root / source / "loki-query.json").write_text(json.dumps(result))
    series = json.loads(request("http://loki:3100/loki/api/v1/series?" + urllib.parse.urlencode({
        **params, "match[]": '{service_name="otelcol-genai-sketches"}'})))
    check(sorted(series["data"], key=lambda d: d["source"]) == sorted(expected_labels, key=lambda d: d["source"]),
          "unexpected Loki labels or streams")
    (root / "loki-series.json").write_text(json.dumps(series))
    print("PASS: device LogQL on both sources; exactly two fixed-label Loki streams")


def scan_outputs(root, secret):
    surfaces = [p for p in root.rglob("*") if p.is_file() and p.name != "backend.jsonl"]
    for path in surfaces:
        data = path.read_bytes()
        check(no_sentinels(data) and secret.encode() not in data, "sentinel/secret in derived output")
        if path.suffix == ".json":
            for sketch in json.loads(data).get("sketches", {}).values():
                payload = base64.b64decode(sketch["data"], validate=True)
                check(no_sentinels(payload) and secret.encode() not in payload, "sentinel/secret in decoded state")
    print(f"PASS: {len(surfaces)} output surfaces scanned, including decoded sketches, code and file-path sentinels")


def run(args):
    os.umask(0o077)
    root = args.output.resolve()
    root.mkdir(parents=True, exist_ok=False)
    for source in SOURCES:
        (root / source / "summaries").mkdir(parents=True)
    fleetdiff = str(Path(shutil.which(args.fleetdiff) or args.fleetdiff).resolve())
    check(command([fleetdiff, "--version"]).stdout.startswith("fleetdiff v0.5.0 "), "use fleetdiff v0.5.0")
    secret = secrets.token_hex(32)
    env = {**os.environ, "RECIPE_OUTPUT": str(root), "RECIPE_UID": str(os.getuid()),
           "RECIPE_GID": str(os.getgid()), "GENAI_SKETCH_SECRET": secret}
    compose = ["docker", "compose", "-p", "coding-" + secrets.token_hex(4), "-f", str(ROOT / "compose.yaml")]

    def dc(*argv):
        return command([*compose, *argv], env)

    def logs():
        for service in (*SOURCES, "driver", "backend", "loki"):
            result = subprocess.run([*compose, "logs", "--no-color", "--no-log-prefix", service],
                                    env=env, capture_output=True, text=True)
            (root / f"{service}.log").write_text(result.stdout + result.stderr)

    try:
        dc("up", "-d", "--wait", "--wait-timeout", "120")
        time.sleep(8)
        for source in SOURCES:
            check("version 0.3.0" in dc("exec", "-T", source, "/otelcol-genai-sketches", "--version").stdout,
                  "collector release mismatch")
        dc("exec", "-T", "driver", "python", "-B", "/integrations/coding-agents/run.py", "--codex-probe")
        for _ in range(10):
            if len(spans(root / "backend.jsonl")) == 1:
                break
            time.sleep(1)
        else:
            raise RuntimeError("Codex fixture missing from shadow capture")
        codex = command([fleetdiff, "inspect", "--format", "json", str(ROOT / "fixtures/codex-handle-responses.json")])
        report = json.loads(codex.stdout)
        check(report["capture"]["spans"] == 1 and all(report["metrics"][k] == 0 for k in (
                  "gen_ai_sketch_requests_total", "gen_ai_sketch_input_tokens_total", "gen_ai_sketch_output_tokens_total")),
              "inspect unexpectedly recognized the native Codex span")
        (root / "codex-inspect.json").write_text(codex.stdout)
        print("PASS: source-shaped Codex completion span is not a model attempt in either released reader", flush=True)
        first = (int(time.time()) // WINDOW + 1) * WINDOW
        starts = []
        for n in range(8):
            start = first + n * WINDOW
            time.sleep(max(0, start + 1 - time.time()))
            starts.append(start * 10**9)
            dc("exec", "-T", "driver", "python", "-B", "/integrations/coding-agents/run.py",
               "--traffic", "--window", str(n), "--start", str(start * 10**9))
            check(time.time() < start + WINDOW - 3, "traffic crossed window boundary")
            time.sleep(max(0, start + WINDOW + 2 - time.time()))
            print(f"Collected window {n + 1}/8 for both Copilot sources", flush=True)
        time.sleep(6)
        logs()
        raw = spans(root / "backend.jsonl")
        check(len(raw) == 1601, "shadow backend lost or duplicated spans")
        check(sum(attrs(s).get("gen_ai.operation.name") == "chat" for s in raw) == 1600,
              "wrong source-shaped model count")
        native = [s for s in raw if s["name"] == "completed"]
        original = json.loads((ROOT / "fixtures/codex-handle-responses.json").read_text())
        original_span = original["resourceSpans"][0]["scopeSpans"][0]["spans"][0]
        check(len(native) == 1 and attrs(native[0]) == attrs(original_span),
              "Codex negative check did not reach the shadow backend")
        check(all(s in (root / "backend.jsonl").read_text() for s in ("SENTINEL_CODE", "SENTINEL_PATH")),
              "adversarial content did not reach input/shadow path")
        for source in SOURCES:
            verify_source(root, source, starts, secret, fleetdiff)
        print(dc("exec", "-T", "driver", "python", "-B", "/integrations/coding-agents/run.py", "--loki").stdout, end="")
        scan_outputs(root, secret)
    finally:
        failed = sys.exc_info()[0] is not None
        errors = []
        try:
            logs()
        except OSError:
            errors.append("could not save container logs")
        try:
            result = subprocess.run([*compose, "down", "-v", "--remove-orphans"], env=env, capture_output=True)
            if result.returncode:
                errors.append("compose down failed")
        except OSError:
            errors.append("could not stop containers")
        if errors:
            message = "Cleanup incomplete: " + "; ".join(errors)
            if failed:
                print(message, file=sys.stderr)
            else:
                raise RuntimeError(message)
    scan_outputs(root, secret)
    print("PASS: released-build coding-agent recipe; private synthetic evidence:", root)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--fleetdiff", default="fleetdiff")
    parser.add_argument("--output", type=Path, default=Path(".cache/coding-agent-recipe"))
    parser.add_argument("--traffic", action="store_true", help=argparse.SUPPRESS)
    parser.add_argument("--window", type=int, help=argparse.SUPPRESS)
    parser.add_argument("--start", type=int, help=argparse.SUPPRESS)
    parser.add_argument("--codex-probe", action="store_true", help=argparse.SUPPRESS)
    parser.add_argument("--loki", action="store_true", help=argparse.SUPPRESS)
    args = parser.parse_args()
    if args.traffic:
        drive(Path("/evidence"), args.window, args.start)
    elif args.codex_probe:
        probe_codex(Path("/evidence"))
    elif args.loki:
        verify_loki(Path("/evidence"))
    else:
        run(args)
