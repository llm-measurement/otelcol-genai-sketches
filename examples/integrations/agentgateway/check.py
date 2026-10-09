# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Exercise released gateways and collectors using the existing mock provider."""

import argparse
import base64
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timezone
import hashlib
import hmac
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import subprocess
import sys
import time
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parent
WINDOW = 20
USERS = [f"SENTINEL_USER_{i}@example.invalid" for i in range(10)]
SESSIONS = [f"SENTINEL_SESSION_{i}" for i in range(10)]
WORKFLOWS = [f"workflow:SENTINEL_WORKFLOW_{i}" for i in range(10)]
DEVICE = "SENTINEL_DEVICE_PRIVATE"
RESOURCE = "https://private.invalid/SENTINEL_RESOURCE"
PROMPT = "SENTINEL_PROMPT_DO_NOT_EXPORT"
SPOOF = "SENTINEL_SPOOFED_USER"
SENTINELS = [*USERS, *SESSIONS, *[v.removeprefix("workflow:") for v in WORKFLOWS], DEVICE, RESOURCE, PROMPT, SPOOF]


def workload(window):
    if window == 0:
        return [100] + [0] * 9
    return [91] + [1] * 9 if window == 7 else [10] * 10


def check(ok, message):
    if not ok:
        raise RuntimeError(message)


def b64(data):
    return base64.urlsafe_b64encode(data).rstrip(b"=").decode()


def jwt(key, user):
    header = b64(json.dumps({"alg": "RS256", "kid": "recipe"}).encode())
    claims = b64(json.dumps({"iss": "recipe.invalid", "aud": "recipe", "sub": user,
                             "exp": int(time.time()) + 900}).encode())
    payload = f"{header}.{claims}"
    signed = subprocess.run(["openssl", "dgst", "-sha256", "-sign", str(key)], input=payload.encode(),
                            capture_output=True, check=True)
    return payload + "." + b64(signed.stdout)


def request(url, body=None, headers=None):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(url, data=data, headers=headers or {})
    with urllib.request.urlopen(req, timeout=10) as response:
        return response.read()


def drive(root, gateway, counts):
    """Test traffic runs inside the isolated Docker network, without provider keys."""
    tokens = json.loads((root / "tokens.json").read_text())
    port = 3000 if gateway == "agentgateway" else 1975
    url = f"http://gateway:{port}/v1/chat/completions"
    body = {"model": "recipe-model", "messages": [{"role": "user", "content": PROMPT}]}

    if not counts:
        if gateway == "agentgateway":
            header, claims, signature = tokens[0].split(".")
            signature = ("A" if signature[0] != "A" else "B") + signature[1:]
            for auth in ("", f"Bearer {header}.{claims}.{signature}"):
                try:
                    request(url, body, {"Content-Type": "application/json", "Authorization": auth})
                except urllib.error.HTTPError as error:
                    check(error.code in (401, 403), "unexpected authentication rejection")
                else:
                    raise RuntimeError("missing or invalid JWT accepted")
        return

    def send(i):
        headers = {"Content-Type": "application/json", "Authorization": "Bearer " + tokens[i],
                   "x-user-id": SPOOF if gateway == "agentgateway" else USERS[i],
                   "agent-session-id": SESSIONS[i],
                   "x-workflow": WORKFLOWS[i].removeprefix("workflow:") if gateway == "agentgateway" else WORKFLOWS[i],
                   "x-device-id": DEVICE, "x-resource-uri": RESOURCE}
        result = json.loads(request(url, body, headers))
        check(result.get("usage", {}).get("total_tokens") == 120, "unexpected mock response usage")

    with ThreadPoolExecutor(max_workers=8) as pool:
        list(pool.map(send, [i for i, n in enumerate(counts) for _ in range(n)]))
    # Scrape while the current window is populated, not after rotation to idle.
    metrics = request("http://collector:8889/metrics")
    (root / f"metrics-{int(time.time())}.txt").write_bytes(metrics)


def command(args, env=None, accepted=(0,)):
    result = subprocess.run(args, env=env, capture_output=True, text=True)
    check(result.returncode in accepted, f"command failed ({result.returncode}): {args[0]}")
    return result


def spans(path):
    if not path.exists():
        return []
    result = []
    for line in path.read_text().splitlines():
        for resource in json.loads(line).get("resourceSpans", []):
            for scope in resource.get("scopeSpans", []):
                result.extend(scope.get("spans", []))
    return result


def attrs(span):
    return {a["key"]: next(iter(a["value"].values())) for a in span.get("attributes", [])}


def no_sentinels(data):
    return not any(s.encode() in data for s in SENTINELS)


def hash_key(secret, domain, value):
    # Fixtures are ASCII with no whitespace; text_v1 is the identity here.
    return hmac.digest(secret.encode(), (domain + "\0" + value).encode(), "sha256")[:8].hex()


def verify_readme_reports(root):
    readme = (ROOT / "README.md").read_text()
    for name in ("investigate", "scan"):
        blocks = re.findall(rf"<!-- {name}-output -->\n```text\n(.*?)```", readme, re.DOTALL)
        check(len(blocks) == 1 and blocks[0].strip(), f"missing README {name} output block")
        check(blocks[0] in (root / f"{name}.txt").read_text(), f"README {name} output differs from saved report")


def verify(root, secret, starts, fleetdiff, *, span_source="real gateway"):
    raw = [attrs(s) for s in spans(root / "backend.jsonl")]
    models = [s for s in raw if s.get("gen_ai.operation.name") == "chat"]
    check(len(models) == 800, "shadow backend did not receive exactly 800 model spans")
    check({s.get("user.id") for s in models} == set(USERS), "user mapping differs from authenticated identity")
    check({s.get("session.id") for s in models} == set(SESSIONS), "session mapping missing")
    check({s.get("app.workflow") for s in models} == set(WORKFLOWS), "workflow mapping missing")
    check(all(s.get("device.id") == DEVICE and s.get("mcp.resource.uri") == RESOURCE for s in models),
          "sentinels did not reach the raw shadow copy")
    check(all(int(s["gen_ai.usage.input_tokens"]) == 100 and int(s["gen_ai.usage.output_tokens"]) == 20
              for s in models), "token fields differ from provider fixture")
    check(PROMPT not in (root / "backend.jsonl").read_text(), "gateway captured prompt content")
    print(f"PASS: 800 {span_source} model spans; identity, session, workflow and usage mapping; shadow copy")

    docs = {}
    for path in (root / "summaries").glob("*.json"):
        doc = json.loads(path.read_text())
        if doc["window_start_unix_nano"] in starts:
            docs[doc["window_start_unix_nano"]] = (path, doc)
    check(set(docs) == set(starts), "missing selected summary windows")
    archive = root / "archive"
    archive.mkdir()
    for start, (path, doc) in docs.items():
        check(doc["observed_start_unix_nano"] == start and doc["observed_end_unix_nano"] == start + WINDOW * 10**9,
              "partial summary window")
        check(doc["counters"]["requests"] == 100 and doc["counters"]["input_tokens"] == 10000
              and doc["counters"]["output_tokens"] == 2000 and doc["counters"]["missing_token_usage"] == 0,
              "summary accounting mismatch")
        check({"top_users", "top_sessions_requests", "top_key.workflow_key.tokens"} <= doc["sketches"].keys(),
              "missing requested top-k sketches")
        shutil.copyfile(path, archive / f"{start}.json")
    before, after = [str(archive / f"{start}.json") for start in starts[-2:]]
    shutil.copyfile(before, root / "before.json")
    shutil.copyfile(after, root / "after.json")
    investigate = [fleetdiff, "investigate", "--before", before, "--after", after, "--expected", "app"]
    report = json.loads(command([*investigate, "--format", "json", "--show-hashes"]).stdout)
    questions = {q["id"]: q for q in report["questions"]}
    for name, domain, value in (("users", "user:v1", USERS[0]), ("sessions", "session:v1", SESSIONS[0])):
        q = questions[name]
        check(q["status"] == "observed", "user/session investigation not observed")
        leader = next(c for c in q["contributors"] if c["hash"] == hash_key(secret, domain, value))
        check(leader["after_share"] == {"lower": .91, "upper": .91}, "wrong contributor share")
        if name == "sessions":
            check(leader.get("flag") == "runaway_candidate", "session not flagged")
    (root / "investigate.json").write_text(json.dumps(report))
    (root / "investigate.txt").write_text(command(investigate).stdout)
    as_of = datetime.now(timezone.utc).isoformat()
    (root / "as-of.txt").write_text(as_of + "\n")
    scan = [fleetdiff, "scan", str(archive), "--expected", "app", "--baseline", "6", "--as-of", as_of]
    result = command([*scan, "--format", "json", "--show-hashes"], accepted=(3,))
    scanned = json.loads(result.stdout)
    check(scanned["unusual_windows"] == 1, "expected one unusual window")
    findings = scanned["windows"][0]["findings"]
    check({"top_users", "top_sessions_requests"} <= {f.get("measurement") for f in findings},
          "scan missed user or session concentration")
    (root / "scan.json").write_text(result.stdout)
    (root / "scan.txt").write_text(command(scan, accepted=(3,)).stdout)
    verify_readme_reports(root)
    print("PASS: separate README investigate and scan excerpts match saved reports")
    quiet_dir = root / "quiet"
    quiet_dir.mkdir()
    for start in starts[:-1]:
        shutil.copyfile(archive / f"{start}.json", quiet_dir / f"{start}.json")
    quiet = command([fleetdiff, "scan", str(quiet_dir), "--expected", "app", "--baseline", "6",
                     "--as-of", as_of, "--format", "json"])
    check(json.loads(quiet.stdout)["unusual_windows"] == 0, "quiet control flagged")
    print("PASS: fleetdiff v0.5.0 investigate (91% user/session share); scan exit 3; quiet control exit 0")

    snapshots = []
    for line in (root / "collector.log").read_text().splitlines():
        try:
            record = json.loads(line)
        except json.JSONDecodeError:
            continue
        if record.get("msg") == "genaisketch topk snapshot":
            snapshots.extend(json.loads(record["payload_json"])["slices"])
    for field, domain, values, multiplier in (("user_key", "user:v1", USERS, 120),
                                             ("session_key", "session:v1", SESSIONS, 1),
                                             ("workflow_key", "user:v1", WORKFLOWS, 120)):
        for n, start in enumerate(starts):
            counts = workload(n)
            expected = {hash_key(secret, domain, v): c * multiplier for v, c in zip(values, counts) if c}
            candidates = [s for s in snapshots if s["field"] == field and s["window_start_unix_nano"] == start
                          and s["total_weight"] == 100 * multiplier]
            check(candidates, "missing populated snapshot for a selected field/window")
            s = candidates[-1]
            check({i["hash"] for i in s["items"]} == expected.keys(), "top-N membership mismatch")
            check(all(i["lower_bound"] <= expected[i["hash"]] <= i["upper_bound"] for i in s["items"]),
                  "snapshot bounds do not bracket ground truth")
    print("PASS: populated snapshots for all three keys in all eight windows; membership and bounds")

    allowed = {"slice", "slice_value", "overflow", "token_field", "state", "source",
               "otel_scope_name", "otel_scope_version", "otel_scope_schema_url"}
    series = []
    for path in root.glob("metrics-*.txt"):
        lines = [s for s in path.read_text().splitlines() if s.startswith("gen_ai_sketch_")]
        check(lines, "empty metrics scrape")
        for line in lines:
            labels = re.findall(r'(\w+)="(?:[^"\\]|\\.)*"', line)
            check(set(labels) <= allowed, "unexpected metric label")
            check(not re.search(r'="[0-9a-f]{16}"', line), "hash became a metric label")
        series.append({line.rsplit(" ", 1)[0] for line in lines})
    check(len(series) == 8 and all(s == series[0] for s in series), "series changed with identity distribution")
    surfaces = [*root.glob("*.log"), *root.glob("metrics-*.txt"), root / "investigate.json", root / "investigate.txt",
                root / "scan.json", root / "scan.txt", root / "before.json", root / "after.json",
                *root.glob("loki-*.json"), *archive.glob("*.json"), *(root / "summaries").glob("*.json")]
    for path in surfaces:
        data = path.read_bytes()
        check(no_sentinels(data), "raw sentinel on a derived output surface")
        check(secret.encode() not in data, "hashing secret on a derived output surface")
        if path.suffix == ".json" and "sketches" in json.loads(data):
            for payload in json.loads(data)["sketches"].values():
                check(no_sentinels(base64.b64decode(payload["data"])), "sentinel in decoded sketch state")
    print(f"PASS: {len(series[0])} unchanged metric series; allowed labels only; extended sentinel scans")


def run(args):
    os.umask(0o077)
    root = args.output.resolve()
    root.mkdir(parents=True, exist_ok=False)
    (root / "summaries").mkdir()
    secret = secrets.token_hex(32)
    key = root / "jwt-key"
    fleetdiff = str(Path(shutil.which(args.fleetdiff) or args.fleetdiff).resolve())
    env = {**os.environ, "RECIPE_OUTPUT": str(root), "RECIPE_UID": str(os.getuid()), "RECIPE_GID": str(os.getgid()),
           "GENAI_SKETCH_SECRET": secret}
    compose = ["docker", "compose", "-p", "recipe-" + secrets.token_hex(4), "-f", str(ROOT / "compose.yaml")]
    if args.gateway == "envoy-ai-gateway":
        compose += ["-f", str(ROOT.parent / "envoy-ai-gateway/compose.yaml")]

    def dc(*arguments, accepted=(0,)):
        return command([*compose, *arguments], env, accepted)

    try:
        command(["openssl", "genrsa", "-out", str(key), "2048"])
        modulus = command(["openssl", "rsa", "-in", str(key), "-noout", "-modulus"]).stdout.strip().split("=", 1)[1]
        (root / "jwks.json").write_text(json.dumps({"keys": [{"kty": "RSA", "n": b64(bytes.fromhex(modulus)),
                                                            "e": "AQAB", "kid": "recipe", "alg": "RS256"}]}))
        (root / "tokens.json").write_text(json.dumps([jwt(key, user) for user in USERS]))
        check(command([fleetdiff, "--version"]).stdout.startswith("fleetdiff v0.5.0 "), "use released fleetdiff v0.5.0")
        dc("up", "-d", "--wait", "--wait-timeout", "120")
        time.sleep(8)
        version = dc("exec", "-T", "collector", "/otelcol-genai-sketches", "--version").stdout
        check("version 0.3.1" in version, "collector release mismatch")
        dc("exec", "-T", "provider", "python", "-B", "/recipe/check.py", "--traffic", "--gateway", args.gateway,
           "--counts", "[]")
        starts = []
        first = (int(time.time()) // WINDOW + 1) * WINDOW
        for n in range(8):
            start = first + n * WINDOW
            time.sleep(max(0, start + 1 - time.time()))
            starts.append(start * 10**9)
            counts = workload(n)
            dc("exec", "-T", "provider", "python", "-B", "/recipe/check.py", "--traffic", "--gateway", args.gateway,
               "--counts", json.dumps(counts))
            check(time.time() < start + WINDOW - 3, "traffic ran across a window boundary")
            time.sleep(max(0, start + WINDOW + 2 - time.time()))
            print(f"Collected window {n + 1}/8", flush=True)
        time.sleep(6)
        for service in ("collector", "gateway", "backend", "provider", "loki"):
            (root / f"{service}.log").write_text(dc("logs", "--no-color", "--no-log-prefix", service).stdout)
        print(dc("exec", "-T", "provider", "python", "-B", "/recipe/loki_check.py").stdout, end="")
        verify(root, secret, starts, fleetdiff)
        print((root / "investigate.txt").read_text().splitlines()[0])
        print((root / "scan.txt").read_text().splitlines()[0])
    finally:
        original_error = sys.exc_info()[0] is not None
        cleanup_errors = []
        for service in ("collector", "gateway", "backend", "provider", "loki"):
            try:
                result = subprocess.run([*compose, "logs", "--no-color", "--no-log-prefix", service], env=env,
                                        capture_output=True, text=True)
                (root / f"{service}.log").write_text(result.stdout + result.stderr)
                if result.returncode:
                    cleanup_errors.append(f"{service} logs unavailable")
            except OSError:
                cleanup_errors.append(f"{service} logs unavailable")
        try:
            result = subprocess.run([*compose, "down", "-v", "--remove-orphans"], env=env, capture_output=True)
            if result.returncode:
                cleanup_errors.append("compose down failed; check recipe containers")
        except OSError:
            cleanup_errors.append("compose down could not run; check recipe containers")
        for name in ("jwt-key", "jwks.json", "tokens.json"):
            try:
                (root / name).unlink(missing_ok=True)
            except OSError:
                cleanup_errors.append(f"could not remove generated {name}")
        if cleanup_errors:
            message = "Cleanup incomplete: " + "; ".join(cleanup_errors)
            if original_error:
                print(message, file=sys.stderr)
            else:
                raise RuntimeError(message)
    print("PASS: recipe complete; reports and private synthetic capture in", root)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--gateway", choices=("agentgateway", "envoy-ai-gateway"), default="agentgateway")
    parser.add_argument("--fleetdiff", default="fleetdiff")
    parser.add_argument("--output", type=Path, default=Path(".cache/agentgateway-recipe"))
    parser.add_argument("--traffic", action="store_true", help=argparse.SUPPRESS)
    parser.add_argument("--counts", help=argparse.SUPPRESS)
    args = parser.parse_args()
    if args.traffic:
        drive(Path("/evidence"), args.gateway, json.loads(args.counts))
    else:
        run(args)
