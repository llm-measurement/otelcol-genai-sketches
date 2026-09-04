# Metrics

The connector derives a bounded Prometheus surface from traces. Configured slices
provide the only variable label values. Slice-level series carry `slice`,
`slice_value`, and `overflow`; `slice_value` is cleartext and must be
low-cardinality and non-sensitive. `gen_ai_sketch_active_slices` has only its fixed
`slice` label.

## Counters

| Metric | Semantics |
| --- | --- |
| `gen_ai_sketch_requests_total` | Spans matched by the configured LLM operation filter |
| `gen_ai_sketch_agent_runs_total` | Root spans with operation `invoke_agent` |
| `gen_ai_sketch_input_tokens_total` | Sum of reported input-token attributes |
| `gen_ai_sketch_output_tokens_total` | Sum of reported output-token attributes |
| `gen_ai_sketch_total_tokens_total` | Sum of reported input and output tokens |
| `gen_ai_sketch_cache_read_input_tokens_total` | Reported cache-read input tokens; never added to total tokens |
| `gen_ai_sketch_cache_write_input_tokens_total` | Reported cache-write input tokens; never added to total tokens |
| `gen_ai_sketch_reasoning_output_tokens_total` | Reported reasoning output tokens; never added to total tokens |
| `gen_ai_sketch_missing_token_usage_total` | Matched request spans missing either aggregate token attribute |
| `gen_ai_sketch_token_field_observations_total` | Fixed-field completeness and quality states |
| `gen_ai_sketch_dedup_suppressed_total` | Probable duplicates suppressed when optional deduplication is enabled |
| `gen_ai_sketch_dedup_key_missing_total` | Requests counted without a configured deduplication key |

Token totals are not inferred. A present zero is a zero. If input or output is
absent or invalid, the request contributes to the missing-usage counter. Cache-read
and cache-write are subsets of input; reasoning is a subset of output.

Each cumulative series begins when its retained slice is created. Collector restart,
or eviction followed by recreation of the same label set, creates a counter reset.
Use Prometheus `rate()` or `increase()` across those resets.

`gen_ai_sketch_token_field_observations_total` adds only two fixed labels:
`token_field` is one of `input`, `output`, `cache_read_input`,
`cache_write_input`, or `reasoning_output`; `state` is one of `reported`,
`missing`, `invalid`, `conflict`, or `subset_violation`. Optional detail fields do
not emit `missing`. Alias conflicts use the first valid configured value and report
the conflict instead of adding both values.

## Gauges

| Metric | Semantics |
| --- | --- |
| `gen_ai_sketch_active_slices` | Number of retained slice states |
| `gen_ai_sketch_distinct_users` | HLL++ estimate for keyed user values in the current window |
| `gen_ai_sketch_distinct_prompt_signatures` | HLL++ estimate for keyed prompt values in the current window |
| `gen_ai_sketch_distinct_retrieval_docs` | HLL++ estimate for keyed document values in the current window |
| `gen_ai_sketch_distinct_mcp_sessions` | Optional HLL++ estimate for keyed MCP sessions |
| `gen_ai_sketch_distinct_mcp_methods` | Optional HLL++ estimate for keyed MCP methods |
| `gen_ai_sketch_distinct_mcp_resources` | Optional HLL++ estimate for keyed MCP resources |

MCP metrics appear only when MCP support is enabled and the corresponding values are
observed.

## Request Classification

The default request operations are `chat`, `generate_content`, `text_completion`,
and `embeddings`. A span-level `gen_ai.request.model` is the compatibility fallback
when `gen_ai.operation.name` is absent. Tool, retrieval, agent, workflow, and MCP
transport spans do not increment request or missing-usage counters by default.

Only root `invoke_agent` spans increment the agent-run counter. Nested agent spans are
not additional runs.

## Top-K Summaries

Every five seconds, the connector can emit a structured `genaisketch topk snapshot`
log containing keyed prompt signatures, weighted estimates, and lower and upper
bounds. These signatures never appear as Prometheus metric labels.

Set `topk: 0` to disable this log surface. Disabled top-k also avoids constructing
or updating the corresponding frequent-items state; it does not disable counters or
distinct estimates.

The lower and upper bounds bracket the estimate and become no tighter through
presentation or rounding. Treat this log surface as sensitive operational data:
hashes remain linkable under one secret even though raw prompts are absent.

## PromQL Examples

Reported token rate by configured slice:

```promql
sum by (slice, slice_value, overflow) (
  rate(gen_ai_sketch_total_tokens_total[5m])
)
```

Request rate by configured slice:

```promql
sum by (slice, slice_value) (rate(gen_ai_sketch_requests_total[5m]))
```

Fraction of matched requests missing token usage:

```promql
sum(rate(gen_ai_sketch_missing_token_usage_total[5m]))
/
sum(rate(gen_ai_sketch_requests_total[5m]))
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

Overflow traffic by slice:

```promql
sum by (slice) (rate(gen_ai_sketch_requests_total{overflow="true"}[5m]))
```

Current distinct prompt estimate by slice value:

```promql
gen_ai_sketch_distinct_prompt_signatures
```

See [Investigating Token Consumption](TOKEN_USAGE.md) for a worked workflow that
combines request rate, token rate, missing usage, slices, and top-k snapshots.
The complete accounting rules and lifecycle cases are in
[Production Accounting Semantics](ACCOUNTING.md).

## Cardinality Contract

The connector does not derive labels from prompt text, user IDs, document IDs,
request IDs, hashes, top-k entries, or arbitrary span attributes. Capacity excess is
routed to one `__overflow__` label value for each configured slice. It is never
silently dropped and does not mint additional label values.
