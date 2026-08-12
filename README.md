# otelcol-genai-sketches

[![CI](https://github.com/llm-measurement/otelcol-genai-sketches/actions/workflows/ci.yml/badge.svg)](https://github.com/llm-measurement/otelcol-genai-sketches/actions/workflows/ci.yml)

An OpenTelemetry Collector distribution that turns high-cardinality GenAI traces
into bounded Prometheus metrics and keyed, bounded top-k summaries.

It provides the bounded, fleet-level measurement layer of an AI agent observability
stack, complementing tools that inspect, evaluate, or debug individual agent runs.

Use it to find where runaway LLM token volume is accumulating without retaining raw
prompts or turning high-cardinality identities into metric labels.

It uses [llm-sketchkit](https://github.com/llm-measurement/llm-sketchkit) for
canonicalization, keyed hashing, distinct counting, frequent-items estimates, and
deduplication. Raw prompt text, user identifiers, document identifiers, and request
identifiers are not exported as metrics or labels.

## When This Fits

For agent fleets and multi-agent systems, the collector separates LLM requests from
agent, tool, retrieval, workflow, and MCP spans while keeping metric state and label
cardinality bounded.

Use this collector when you need to:

- keep Prometheus label cardinality bounded while monitoring large GenAI workloads;
- estimate distinct users, prompt signatures, or retrieval documents without keeping
  the raw values in metric state;
- separate real zero-token usage from spans that did not report token attributes;
- investigate token maxing or runaway agent consumption by locating where reported
  tokens accumulate across bounded slices and keyed prompt signatures;
- inspect token-heavy prompt signatures without turning them into metric labels; or
- count LLM requests correctly in traces that also contain agent, tool, retrieval,
  workflow, and MCP spans.

These signals show token volume, not task value. The collector does not treat token
consumption as productivity, enforce token budgets, or stop agent loops.

This is not a prompt logger, billing ledger, arbitrary attribute-to-label converter,
anomaly detector, or differential-privacy system.

### Where It Fits

```text
GenAI applications -> OTLP traces -> this collector -> Prometheus metrics + bounded structured logs
```

Use this distribution when source spans already flow through OpenTelemetry. If you
own a custom streaming, batch, or warehouse pipeline and do not need OTLP-to-metrics
conversion, use [llm-sketchkit](https://github.com/llm-measurement/llm-sketchkit)
directly. Prometheus, Grafana, or another Prometheus-compatible observability backend
sits downstream of the collector.

## Investigating Token Consumption

If "token maxing" means unexpected or runaway token consumption in an LLM or agent
workload, this collector provides bounded signals for finding where the reported
volume is accumulating:

- token rate and tokens per request by bounded dimensions such as team, model,
  provider, or route;
- an explicit count of requests whose instrumentation omitted token usage; and
- token-weighted top-k prompt signatures for high-cardinality concentration, emitted
  as structured logs rather than Prometheus labels.

Comparing request rate with tokens per request helps separate traffic growth from
larger reported requests or responses. Slice metrics localize the change, while the
top-k surface shows whether a small number of keyed prompt signatures dominate the
reported volume.

These are investigation signals, not a control plane. They cannot determine whether
tokens were useful, diagnose prompt contents, enforce a budget, stop an agent loop,
or prevent a model context-window error. See [Investigating Token
Consumption](docs/TOKEN_USAGE.md) for the instrumentation requirements, PromQL,
interpretation guide, and operational boundaries.

## What It Produces

| Signal | Meaning |
| --- | --- |
| `gen_ai_sketch_requests_total` | LLM request spans matched by the operation filter |
| `gen_ai_sketch_agent_runs_total` | Root `invoke_agent` spans |
| `gen_ai_sketch_input_tokens_total` | Reported input tokens |
| `gen_ai_sketch_output_tokens_total` | Reported output tokens |
| `gen_ai_sketch_total_tokens_total` | Reported input plus output tokens |
| `gen_ai_sketch_missing_token_usage_total` | Matched requests with neither token field |
| `gen_ai_sketch_active_slices` | Currently retained slice states |
| `gen_ai_sketch_distinct_users` | Estimated distinct keyed user values |
| `gen_ai_sketch_distinct_prompt_signatures` | Estimated distinct keyed prompt values |
| `gen_ai_sketch_distinct_retrieval_docs` | Estimated distinct keyed document values |

Optional MCP metrics estimate distinct sessions, methods, and resources. Weighted
top-k prompt signatures are emitted as structured logs with estimates and lower and
upper bounds; they never become Prometheus labels.

See [Metrics](docs/METRICS.md) for signal semantics and [Investigating Token
Consumption](docs/TOKEN_USAGE.md) for a worked investigation.

## Quick Start

Requirements: Docker with Compose, Go 1.26.5 or newer, and `openssl`.

```bash
git clone https://github.com/llm-measurement/otelcol-genai-sketches.git
cd otelcol-genai-sketches
export GENAI_SKETCH_SECRET="$(openssl rand -hex 32)"
make example-up
```

The example sends mixed agent, tool, retrieval, and LLM spans with optional token
usage and high-cardinality prompt signatures.

- Grafana: [http://localhost:3000](http://localhost:3000)
- Prometheus: [http://localhost:9090](http://localhost:9090)
- Collector metrics: [http://localhost:8889/metrics](http://localhost:8889/metrics)

After the containers have run for at least a minute, use the Grafana dashboard to:

1. Compare **Requests/sec** with **Reported Token Rate**.
2. Check **Reported Tokens / Request** to distinguish traffic growth from larger
   requests or responses.
3. Check **Missing Token Usage** before trusting token totals as complete.
4. Compare the bounded model and team/model slices to localize concentration.

Inspect the high-cardinality prompt-signature surface from the same shell:

```bash
docker compose -f examples/compose.yaml logs collector \
  | grep 'genaisketch topk snapshot'
```

The snapshot contains keyed hashes and bounded estimates, not prompt text. The
[token-consumption playbook](docs/TOKEN_USAGE.md) explains how to interpret it.

Stop the stack with:

```bash
make example-down
```

For a local collector binary instead:

```bash
make dist
GENAI_SKETCH_SECRET="$(openssl rand -hex 32)" make run-local
```

## Configuration

Start with [the example configuration](examples/collector/config.yaml). The connector
requires a secret of at least 16 bytes from `GENAI_SKETCH_SECRET` by default.

```yaml
connectors:
  genaisketch:
    window_duration: 1m
    retention_windows: 10
    max_slices: 2000
    topk: 20
    slices:
      - name: model
        keys: [gen_ai.request.model]
        from_resource_attributes: [gen_ai.request.model]
```

Slice values are exported in cleartext as Prometheus labels. Use only bounded,
low-cardinality, non-sensitive attributes such as model, team, route, or provider.
Configured slice capacity is enforced with deterministic inactive-slice eviction and
a single `__overflow__` value; excess traffic is never silently dropped and never
creates a new label value.

See [Configuration](docs/CONFIGURATION.md) for field mapping, operation filtering,
resource fallback, MCP support, deduplication, and capacity limits.

## Security And Privacy

Keyed hashes are pseudonymous, not anonymous. Values remain linkable while the same
secret is in use, and anyone holding the secret can test candidate values. Rotating
the secret breaks comparison with earlier windows.

The structured top-k surface contains keyed hashes and bounded estimates. Treat
collector logs as sensitive operational data even though raw source values are not
included. The connector rejects known high-cardinality MCP identifiers as slice keys
and rejects overlap between plaintext slice keys and configured hashed fields.

Token attributes are optional. Missing usage is counted explicitly; the connector
does not invent token weights. Bloom-filter deduplication is bounded and may
undercount because false positives are possible.

See [Security](SECURITY.md) to report a vulnerability privately.

## Evidence

Recorded local measurements include:

- 36 million spans accepted and exported over 60 minutes at 10,000 spans/second;
- exact request filtering across a 36 million-span mixed tree workload: 10.8 million
  emitted LLM spans and 10.8 million counted requests;
- exact missing-usage accounting for 1.08 million planted missing-token LLM spans;
- 528,924 spans/second in the mixed in-process benchmark; and
- 1,386.8 MiB maximum collector RSS in the mixed fleet-shaped soak.

These are measurements from one Apple M4 Max system, not universal capacity claims.
Workload definitions, machine details, commands, and non-passing runs are in
[Benchmarks](docs/BENCHMARKS.md).

## Development

```bash
make tidy
make check
make dist
make test-integration
```

The integration suite includes clean OTLP-to-Prometheus behavior, bounded overflow,
deterministic eviction, restart stability, tree locality, and sentinel scans across
metric, label, and structured-log surfaces.

## Status

This project is alpha software. Interfaces and metric semantics may change between
alpha releases. See the [changelog](CHANGELOG.md) for release notes.

Licensed under the [Apache License 2.0](LICENSE).
