# Production Accounting Semantics

Contract version: `genai-accounting/v1`

Last reviewed: 2026-09-03

This document defines how `otelcol-genai-sketches` turns GenAI spans into request
and token counters. Changes that alter a counter's meaning require a new contract
version and new reconciliation fixtures.

The contract is based on the
[OpenTelemetry GenAI span conventions](https://github.com/open-telemetry/semantic-conventions-genai/blob/main/docs/gen-ai/gen-ai-spans.md)
reviewed on 2026-09-03. Those conventions are still developing, so this repository
pins its own behavior instead of inheriting changes from a moving document.

## Event Scope

One span that matches `operation_filter.llm_operations` is one observed model
request. The defaults are `chat`, `generate_content`, `text_completion`, and
`embeddings`. A span-level `gen_ai.request.model` is used only when
`gen_ai.operation.name` is absent, for compatibility with older instrumentation.

The counting unit is the matching exported span, not an inferred logical call. If
both client-side and server-side instrumentation emit matching spans for one call,
both are counted. Keep one accounting source in the connector pipeline when a
single logical-call total is required.

Agent, tool, retrieval, workflow, and MCP transport spans do not enter the model
request denominator. A root `invoke_agent` span increments the separate agent-run
counter. Attributes are read from the span being classified; parent attributes are
never inherited.

This is observed-span accounting. If instrumentation emits separate model spans for
retries, each span is counted unless optional deduplication suppresses it. Automatic
retries hidden inside one model span remain one observed request.

## Token Fields

| Field | Default ordered sources | Relationship |
| --- | --- | --- |
| Input | `gen_ai.usage.input_tokens`, `gen_ai.usage.prompt_tokens` | Aggregate input total |
| Output | `gen_ai.usage.output_tokens`, `gen_ai.usage.completion_tokens` | Aggregate output total |
| Cache-read input | `gen_ai.usage.cache_read.input_tokens` | Subset of input |
| Cache-write input | `gen_ai.usage.cache_write.input_tokens`, `gen_ai.usage.cache_creation.input_tokens` | Subset of input |
| Reasoning output | `gen_ai.usage.reasoning.output_tokens` | Subset of output |

The first valid configured source wins. A later valid source with a different value
is reported as a conflict but is not added. This makes aliases compatible without
double counting.

Version 1 does not export modality-specific audio or image breakdowns, or
provider-only fields outside the configured source lists. Those attributes remain
on the original trace and are ignored by these accounting counters.

`gen_ai_sketch_total_tokens_total` is input plus output. Cache and reasoning fields
are subsets and are never added to that total. Detail fields are never used to
invent a missing aggregate. When a detail exceeds its reported parent, the value is
still exported and a subset violation is reported.

A present integer zero is reported usage. An absent, negative, fractional, or
non-numeric value is not converted to zero. A request increments
`gen_ai_sketch_missing_token_usage_total` when either aggregate input or aggregate
output is unavailable or invalid. Token values and sums that exceed the connector's
signed 64-bit bound reject the batch before aggregate state is changed.

Cumulative metrics begin when their retained slice is created. A collector restart
or deterministic eviction followed by recreation starts a new cumulative stream for
that label set. Prometheus `rate()` and `increase()` handle the lower value as a
counter reset. Counters saturate at the signed 64-bit maximum on export; an ordinary
reset should occur long before that bound is practical.

The bounded `gen_ai_sketch_token_field_observations_total` metric reports fixed
`token_field` and `state` values. Aggregate fields emit exactly one primary state,
`reported` or `missing`, for every counted request. Optional detail fields emit
`reported` only when present. `invalid`, `conflict`, and `subset_violation` are
additional quality states and can coexist with a primary state.

## Lifecycle Cases

- Success, cancellation, timeout, and error spans follow the same request rule. If
  the matching span and usage exist, they are counted.
- Streaming is counted when the model-request span is exported. Multiple partial
  spans are multiple observations; this connector does not infer that they belong
  to one stream.
- Late spans enter the window current at collector receipt time. The connector does
  not reopen historical windows based on span timestamps.
- Corrected spans are new observations unless deduplicated. There is no subtraction
  or upsert protocol in OTLP traces.
- Provider-estimated usage is indistinguishable from provider-measured usage unless
  instrumentation records that provenance elsewhere. The connector does not claim
  a provenance it cannot observe.

## Deduplication

Deduplication is off by default. Its default ordered sources are the span attributes
`gen_ai.response.id` and `request.id`. The first available value is keyed and placed
in a per-slice, per-window Bloom filter. Resource attributes are never used as
request IDs.

`trace_id` is supported as an explicit special source, but it is unsafe when one
trace can contain more than one model request: every later model request in that
trace may be suppressed. It is therefore not a default. A probable duplicate is
suppressed from request, token, distinct, and top-k state and increments
`gen_ai_sketch_dedup_suppressed_total`. A request without any configured ID is
counted normally and increments `gen_ai_sketch_dedup_key_missing_total`.

Bloom filters can return false positives. Deduplicated counters are therefore not
an exact billing or quota ledger.

## Attribution And Reconciliation

Every configured slice is an independent view over the same observations. Do not
sum metrics across different `slice` names. Within a slice, input, output, detail,
missing, quality, deduplication, and overflow counters use the same bounded labels.
Unknown slice values route to one `__overflow__` value when capacity is full.

Provider quotas, concurrency, queue depth, and capacity are separate signals. They
must carry explicit units and timestamps before comparison with these counters.
Currency, GPU time, energy, and inferred cost are derived values, not source-of-truth
token fields.

Quota reservation, settlement, invoices, and adjustments require an append-only
exact ledger with stable request identity, retention, access control, and audit
history. Sketches and Prometheus counters provide continuous evidence and fast
reconciliation; they do not replace that ledger.

Machine-readable examples live in
`connector/genaisketchconnector/testdata/accounting/v1`. Tests apply each fixture to
the connector and compare every accounting counter and quality state. The corpus
covers current and legacy field names, cache subsets, missing and zero values,
conflicts, lifecycle outcomes, retries, and optional deduplication.

## Versioning

Additive metrics or fixtures may be added within `genai-accounting/v1` when existing
counter meanings do not change. A change to request scope, alias precedence,
missing-usage rules, token arithmetic, deduplication behavior, or attribution
requires a new contract version and a migration note.
