# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Exercise released collectors with synthetic vendor-shaped spans only."""
import json
import os
from pathlib import Path
import re
import secrets
import subprocess
import tempfile
import time

HERE = Path(__file__).resolve().parent
SENTINELS = ["sentinel-user-a", "sentinel-session-a", "sentinel-user-b",
             "sentinel-session-b", "sentinel@example.invalid",
             "/synthetic/private/customer.py", "def planted_secret(): return 42"]
RESULT = ("5 model attempts; 307 input + 56 output tokens; 2 missing-usage attempts.\n"
          "Original spans preserved; identities absent from metrics and snapshot logs.")


def attributes(values):
    return [{"key": key, "value": {"intValue": str(value)} if isinstance(value, int)
             else {"stringValue": value}} for key, value in values.items()]


def fixture():
    common = {"user.email": SENTINELS[4], "file.path": SENTINELS[5],
              "llm.input_messages.0.message.content": SENTINELS[6]}
    oi = {"openinference.span.kind": "LLM", "llm.model_name": "synthetic-model"}
    ol = {"llm.request.type": "chat", "llm.request.model": "synthetic-model"}
    rows = [
        {**oi, "llm.token_count.prompt": 100, "llm.token_count.completion": 20,
         "user.id": SENTINELS[0], "session.id": SENTINELS[1]},
        {**ol, "llm.usage.prompt_tokens": 200, "llm.usage.completion_tokens": 30,
         "user.id": SENTINELS[2], "session.id": SENTINELS[3]},
        {**oi, "llm.token_count.prompt": 999, "llm.token_count.completion": 999,
         "gen_ai.usage.input_tokens": 7, "gen_ai.usage.output_tokens": 2},
        {**ol, "llm.usage.prompt_tokens": "malformed", "llm.usage.completion_tokens": 4},
        oi,
        {**oi, "openinference.span.kind": "TOOL", "llm.token_count.prompt": 9000},
        {**oi, "openinference.span.kind": "EMBEDDING", "llm.token_count.prompt": 9000},
        {"traceloop.span.kind": "workflow", "llm.usage.prompt_tokens": 9000},
    ]
    now = time.time_ns()
    return {"resourceSpans": [{"scopeSpans": [{"scope": {"name": "synthetic-normalizer"},
        "spans": [{"traceId": f"{i+1:032x}", "spanId": f"{i+1:016x}",
                   "name": f"synthetic-{i}", "kind": 3,
                   "startTimeUnixNano": str(now), "endTimeUnixNano": str(now+1),
                   "attributes": attributes({**common, **row})}
                  for i, row in enumerate(rows)]}]}]}


def spans(documents):
    return [span for doc in documents for rs in doc.get("resourceSpans", [])
            for scope in rs.get("scopeSpans", []) for span in scope.get("spans", [])]


def attrs(span):
    return {a["key"]: a["value"] for a in span.get("attributes", [])}


def verify_traces(sent, raw, normalized):
    original = {s["spanId"]: attrs(s) for s in spans([sent])}
    assert {s["spanId"]: attrs(s) for s in spans(raw)} == original, "raw branch changed"
    result = {s["name"]: attrs(s) for s in spans(normalized)}
    assert len(result) == 8, "normalized spans missing"
    for name, value in [("synthetic-0", 100), ("synthetic-1", 200), ("synthetic-2", 7)]:
        assert result[name]["gen_ai.usage.input_tokens"] == {"intValue": str(value)}
    assert result["synthetic-0"]["gen_ai.conversation.id"] == {"stringValue": SENTINELS[1]}
    assert result["synthetic-1"]["gen_ai.conversation.id"] == {"stringValue": SENTINELS[3]}
    assert "gen_ai.conversation.id" not in result["synthetic-2"], "identity invented"
    assert "gen_ai.usage.input_tokens" not in result["synthetic-3"], "malformed usage filled"
    for row in result.values():
        assert "gen_ai.usage.cache_read.input_tokens" not in row, "cache detail invented"


def metric(text, name):
    return sum(float(m.group(1)) for line in text.splitlines()
               if (m := re.fullmatch(re.escape(name) + r'(?:\{[^\n]*\})? ([0-9.eE+\-]+)', line)))


def verify_metrics(text):
    for suffix, expected in [("requests_total", 5), ("input_tokens_total", 307),
                             ("output_tokens_total", 56), ("missing_token_usage_total", 2)]:
        actual = metric(text, "gen_ai_sketch_" + suffix)
        assert actual == expected, f"{suffix}: expected {expected}, got {actual}"


def verify_snapshots(logs):
    found = set()
    for line in logs.splitlines():
        record = json.loads(line)
        if record.get("msg") != "genaisketch topk snapshot":
            continue
        for row in json.loads(record["payload_json"])["slices"]:
            if row["field"] not in {"user_key", "session_key"}:
                continue
            if row["total_weight"] == 350:
                assert sorted((i["lower_bound"], i["upper_bound"]) for i in row["items"]) == [
                    (120, 120), (230, 230)], "ranking bounds differ from synthetic truth"
                found.add(row["field"])
    assert found == {"user_key", "session_key"}, "user/session rankings missing"


def main():
    env = dict(os.environ, GENAI_SKETCH_SECRET=secrets.token_hex(32))
    project = "normalizer-" + secrets.token_hex(6)
    command = ["docker", "compose", "-p", project, "-f", str(HERE / "compose.yaml")]

    def compose(*args, check=True):
        return subprocess.run(command + list(args), env=env, cwd=HERE, check=check,
                              capture_output=True, text=True)

    def request(url, payload=None):
        script = ("import json,sys,urllib.request; x=json.load(sys.stdin); "
                  "r=urllib.request.Request(x['url'], "
                  "data=json.dumps(x['payload']).encode() if x['payload'] else None, "
                  "headers={'Content-Type':'application/json'}); "
                  "print(urllib.request.urlopen(r,timeout=5).read().decode())")
        return subprocess.run(command + ["exec", "-T", "client", "python", "-c", script],
            env=env, cwd=HERE, check=True, capture_output=True, text=True,
            input=json.dumps({"url": url, "payload": payload})).stdout

    output = Path(tempfile.mkdtemp(prefix="normalizer-evidence-", dir="/tmp")).resolve()
    start = time.monotonic()
    try:
        compose("up", "-d", "--wait", "--wait-timeout", "60")
        sent = fixture()
        # Wait for listeners before sending exactly one batch (no retry after dispatch).
        deadline = time.monotonic() + 30
        while True:
            try:
                request("http://sketches:8889/metrics")
                compose("exec", "-T", "client", "python", "-c",
                        'import socket; socket.create_connection(("normalizer", 4318), 5).close()')
                break
            except subprocess.CalledProcessError:
                if time.monotonic() > deadline:
                    raise RuntimeError("collector did not become ready") from None
                time.sleep(0.2)
        body = json.loads(request("http://normalizer:4318/v1/traces", sent))
        assert not body.get("partialSuccess"), "OTLP partially rejected the batch"
        deadline = time.monotonic() + 30
        while True:
            text = request("http://sketches:8889/metrics")
            if metric(text, "gen_ai_sketch_requests_total") == 5:
                break
            if time.monotonic() > deadline:
                raise RuntimeError("expected five model attempts")
            time.sleep(0.2)
        (output / "metrics.txt").write_text(text)
        verify_metrics(text)
        time.sleep(6)  # Allow the periodic snapshot export to run.
        logs = compose("logs", "--no-log-prefix", "sketches").stdout
        (output / "sketches.log").write_text(logs)
        verify_snapshots(logs)
        for sentinel in SENTINELS + [env["GENAI_SKETCH_SECRET"]]:
            assert sentinel not in text + logs, "sensitive value in sketch output"
        for name in ["raw", "normalized"]:
            compose("cp", f"normalizer:/tmp/{name}.json", str(output / f"{name}.json"))
        def read(name):
            return [json.loads(line) for line in (output / f"{name}.json").read_text().splitlines()]
        verify_traces(sent, read("raw"), read("normalized"))
        print(RESULT)
        print(f"PASS in {time.monotonic()-start:.1f}s; synthetic evidence: {output}")
    except subprocess.CalledProcessError as error:
        (output / "command-error.txt").write_text(error.stderr or "")
        raise RuntimeError(f"Recipe command failed; evidence: {output}") from None
    finally:
        for service in ["normalizer", "sketches"]:
            captured = compose("logs", "--no-log-prefix", service, check=False)
            (output / f"{service}.log").write_text(captured.stdout + captured.stderr)
        cleanup = compose("down", "--volumes", "--remove-orphans", check=False)
        if cleanup.returncode:
            print(f"Cleanup failed; remove Docker Compose project {project}.")


if __name__ == "__main__":
    main()
