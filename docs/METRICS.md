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
| `gen_ai_sketch_missing_token_usage_total` | Matched request spans with neither token attribute |

Token totals are not inferred. A present zero is a zero; an absent field contributes
to the missing-usage counter instead.

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

The lower and upper bounds bracket the estimate and become no tighter through
presentation or rounding. Treat this log surface as sensitive operational data:
hashes remain linkable under one secret even though raw prompts are absent.

## PromQL Examples

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
sum(rate(gen_ai_sketch_total_tokens_total[5m]))
/
sum(rate(gen_ai_sketch_requests_total[5m]))
```

Overflow traffic by slice:

```promql
sum by (slice) (rate(gen_ai_sketch_requests_total{overflow="true"}[5m]))
```

Current distinct prompt estimate by slice value:

```promql
gen_ai_sketch_distinct_prompt_signatures
```

## Cardinality Contract

The connector does not derive labels from prompt text, user IDs, document IDs,
request IDs, hashes, top-k entries, or arbitrary span attributes. Capacity excess is
routed to one `__overflow__` label value for each configured slice. It is never
silently dropped and does not mint additional label values.
