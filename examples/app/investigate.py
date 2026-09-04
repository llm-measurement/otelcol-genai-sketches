# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay Erramilli and Codex
"""Compare known synthetic traffic with the running collector's measurements."""

import json
import os
import random
import time
from urllib.parse import urlencode
from urllib.request import urlopen

from app import configure_tracing, emit_span
from opentelemetry import trace

PROMETHEUS = os.getenv("PROMETHEUS_URL", "http://127.0.0.1:9090").rstrip("/")
METRICS = {
    "requests": "gen_ai_sketch_requests_total",
    "tokens": "gen_ai_sketch_total_tokens_total",
    "missing": "gen_ai_sketch_missing_token_usage_total",
}
SELECTOR = '{slice="by_model",slice_value="gen_ai.request.model=investigation-model"}'
SCENARIOS = [
    ("baseline", 1, False),
    ("more-tools", 8, False),
    ("missing-usage", 1, True),
]


def counters():
    values = {}
    for name, metric in METRICS.items():
        query = urlencode({"query": f"sum({metric}{SELECTOR})"})
        with urlopen(f"{PROMETHEUS}/api/v1/query?{query}", timeout=5) as response:
            payload = json.load(response)
        if payload["status"] != "success":
            raise RuntimeError("Prometheus query failed")
        result = payload["data"]["result"]
        values[name] = int(float(result[0]["value"][1])) if result else 0
    return values


def wait_for_counters(expected=None):
    deadline = time.monotonic() + 45
    last = None
    while time.monotonic() < deadline:
        try:
            last = counters()
            if expected is None or last == expected:
                return last
        except (OSError, ValueError, KeyError) as error:
            last = type(error).__name__
        time.sleep(1)
    raise RuntimeError(
        f"collector did not reconcile: expected={expected}, observed={last}"
    )


def main():
    tracer = configure_tracing()
    results = []
    try:
        for scenario, tool_count, omit_half in SCENARIOS:
            before = wait_for_counters()
            expected = dict.fromkeys(["spans", *METRICS], 0)
            rng = random.Random(20260904)
            for ordinal in range(100):
                emitted = emit_span(
                    tracer,
                    rng=rng,
                    tool_count=tool_count,
                    include_usage=not omit_half or ordinal % 2 == 0,
                    model="investigation-model",
                    team="investigation",
                    token_usage=(120, 80),
                )
                for key, value in emitted.items():
                    expected[key] += value
            if not trace.get_tracer_provider().force_flush(timeout_millis=10000):
                raise RuntimeError("span exporter did not flush")
            after = wait_for_counters(
                {key: before[key] + expected[key] for key in METRICS}
            )
            results.append(
                {
                    "scenario": scenario,
                    "emitted_spans": expected["spans"],
                    "emitted_model_requests": expected["requests"],
                    "synthetic_token_truth": 20000,
                    "collector": {key: after[key] - before[key] for key in METRICS},
                }
            )
    finally:
        trace.get_tracer_provider().shutdown()
    print(
        json.dumps(
            {"synthetic_only": True, "seed": 20260904, "results": results}, indent=2
        )
    )


if __name__ == "__main__":
    main()
