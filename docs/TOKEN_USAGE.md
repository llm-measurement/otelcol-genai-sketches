# Investigating Token Consumption

This guide shows how to use `otelcol-genai-sketches` to investigate unexpected or
runaway LLM token consumption, sometimes called token maxing. The collector detects
and localizes reported token volume. It does not enforce budgets or stop workloads.

## What You Can Answer

The exported signals can help answer:

- Did token volume rise because request volume rose?
- Did the average reported tokens per request increase?
- Which bounded team, model, provider, or route slice carries the increase?
- Is reported token volume concentrated in a small number of prompt signatures?
- How much of the request stream omitted token usage entirely?

The signals cannot establish whether token use was productive, recover prompt text,
identify an agent-loop root cause, or determine remaining model context capacity.

## Instrumentation Requirements

Matched LLM request spans should carry:

- `gen_ai.operation.name`, or a span-level `gen_ai.request.model` for compatibility;
- `gen_ai.usage.input_tokens` when input usage is available; and
- `gen_ai.usage.output_tokens` when output usage is available.

The attribute names are configurable in `fields` and `weights`. Input and output
usage are independently optional. The collector sums only reported values and never
invents missing token counts.

Configure a small number of bounded, non-sensitive slices for dimensions operators
can act on, such as team, model, provider, or route. Slice values are exported as
plaintext Prometheus labels. Do not use user IDs, prompt text, request IDs, session
IDs, or other sensitive or high-cardinality values as slices.

## Run The Example

```bash
git clone https://github.com/llm-measurement/otelcol-genai-sketches.git
cd otelcol-genai-sketches
export GENAI_SKETCH_SECRET="$(openssl rand -hex 32)"
make example-up
```

The command returns after the containers start. After at least a minute, open the
provisioned dashboard at [http://localhost:3000](http://localhost:3000). The example
deliberately emits uneven model traffic, high-cardinality prompt signatures, and
some requests without usage so each investigation surface is visible.

## PromQL Workflow

Reported token rate by bounded slice:

```promql
sum by (slice, slice_value, overflow) (
  rate(gen_ai_sketch_total_tokens_total[5m])
)
```

Request rate over the same dimensions:

```promql
sum by (slice, slice_value, overflow) (
  rate(gen_ai_sketch_requests_total[5m])
)
```

Average reported tokens per matched request:

```promql
sum by (slice, slice_value, overflow) (
  rate(gen_ai_sketch_total_tokens_total[5m])
)
/
sum by (slice, slice_value, overflow) (
  rate(gen_ai_sketch_requests_total[5m])
)
```

The denominator includes all matched requests, including requests with no reported
usage. Read this ratio alongside the missing-usage fraction below.

Input and output token rates can be compared independently:

```promql
sum by (slice, slice_value, overflow) (
  rate(gen_ai_sketch_input_tokens_total[5m])
)
```

```promql
sum by (slice, slice_value, overflow) (
  rate(gen_ai_sketch_output_tokens_total[5m])
)
```

Fraction of matched requests with no reported token usage:

```promql
sum(rate(gen_ai_sketch_missing_token_usage_total[5m]))
/
sum(rate(gen_ai_sketch_requests_total[5m]))
```

Overflow traffic by slice:

```promql
sum by (slice) (
  rate(gen_ai_sketch_requests_total{overflow="true"}[5m])
)
```

A non-zero overflow result means the configured slice capacity was exceeded. The
traffic remains counted under one `__overflow__` value, but that portion cannot be
localized to a newly observed slice value.

## Interpreting The Signals

| Observation | Supported interpretation | Check next |
| --- | --- | --- |
| Token rate and request rate rise together while tokens/request stays stable | More matched request traffic is carrying more reported tokens | Slice by team, model, provider, or route |
| Token rate and tokens/request rise while request rate is stable | Matched requests or responses report more tokens on average | Compare input and output rates; inspect top-k signatures |
| One bounded slice rises while peers remain stable | The reported increase is localized to that configured dimension | Investigate the owning service or team |
| A few top-k signatures carry much of the window weight | Reported token volume is concentrated in recurring keyed prompt signatures | Correlate hashes in a controlled application-owned mapping |
| Missing-usage fraction rises | Token totals cover a shrinking portion of matched requests | Repair instrumentation before drawing completeness conclusions |
| Overflow traffic rises | Slice cardinality exceeded the configured retained-state capacity | Revisit slice choice or capacity; do not promote identifiers into labels |

These observations narrow an investigation. They do not by themselves prove a loop,
an inefficient prompt, abuse, or waste.

## Inspecting Token-Weighted Top-K

The collector emits periodic `genaisketch topk snapshot` structured log records. In
the example stack:

```bash
docker compose -f examples/compose.yaml logs collector \
  | grep 'genaisketch topk snapshot'
```

Each slice entry includes the window, total reported weight, maximum error, and
ranked keyed hashes with `estimate`, `lower_bound`, and `upper_bound`. The snapshot
uses no-false-negative mode, favoring recall when selecting candidate heavy hitters.
The bounds, rather than the estimate alone, describe what the sketch guarantees.

Prompt hashes are pseudonymous and stable only while the same secret and domain are
in use. They are not Prometheus labels and do not reveal prompt text. An application
owner may recompute hashes for a controlled set of known prompt templates using
`llm-sketchkit`, the same secret, and the `prompt:v1` domain. Keep that mapping and
the secret inside the same restricted trust boundary; do not export them to a broad
dashboard or log index.

## Acting On Results

Prometheus-compatible backends can alert on token rate, tokens per request,
missing-usage fraction, or overflow. Structured-log tooling can route top-k snapshots
to a restricted investigation surface. Budget enforcement, loop termination, model
routing, and context management belong in an application or control component that
consumes these signals and has authority to act.

For complete signal semantics, see [Metrics](METRICS.md). For field mappings and
capacity controls, see [Configuration](CONFIGURATION.md).
