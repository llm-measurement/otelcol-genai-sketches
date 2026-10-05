# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Verify the documented query against real collector snapshot logs in local Loki."""

import json
from pathlib import Path
import time
import urllib.parse

from check import check, no_sentinels, request


def run(root):
    rows = []
    for line in (root / "collector.log").read_text().splitlines():
        try:
            record = json.loads(line)
        except json.JSONDecodeError:
            continue
        if record.get("msg") == "genaisketch topk snapshot":
            check(no_sentinels(line.encode()), "raw sentinel in a snapshot before Loki export")
            rows.append(line)
    check(rows, "no snapshots to query")
    now = time.time_ns()
    labels = {"service_name": "otelcol-genai-sketches"}
    request("http://loki:3100/loki/api/v1/push", {
        "streams": [{"stream": labels, "values": [[str(now + i), line] for i, line in enumerate(rows)]}]
    }, {"Content-Type": "application/json"})
    params = {"start": str(now - 10**9), "end": str(now + 10**9)}
    query = (Path(__file__).parent / "workflows.logql").read_text()
    result = json.loads(request("http://loki:3100/loki/api/v1/query_range?" + urllib.parse.urlencode({
        **params, "query": query, "limit": 5000
    })))
    check(result["status"] == "success", "LogQL query failed")
    lines = [line for stream in result["data"]["result"] for _, line in stream["values"]]
    check(any("tokens=10920" in line and "bounds=[10920,10920]" in line for line in lines),
          "workflow leader missing from LogQL results")
    series = json.loads(request("http://loki:3100/loki/api/v1/series?" + urllib.parse.urlencode({
        **params, "match[]": '{service_name="otelcol-genai-sketches"}'
    })))
    check(series["data"] == [labels], "unexpected Loki stream labels")
    for name, doc in (("loki-query.json", result), ("loki-series.json", series)):
        data = json.dumps(doc)
        check(no_sentinels(data.encode()), "raw sentinel in Loki results or labels")
        (root / name).write_text(data)
    print("PASS: workflow LogQL query; one fixed Loki stream label; no raw sentinels")


if __name__ == "__main__":
    run(Path("/evidence"))
