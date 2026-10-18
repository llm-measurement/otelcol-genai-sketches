# Production Accounting Semantics

Contract version: `genai-accounting/v1`

Last reviewed: 2026-09-05

This document defines how `otelcol-genai-sketches` turns GenAI spans into request
and token counters. Changes that alter a counter's meaning require a new contract
version and new reconciliation fixtures.

The offline inspection adapter also runs the
[shared OTLP accounting corpus](../connector/genaisketchconnector/testdata/inspect-contract/v1/manifest.json).
Its fixed default mappings, provenance rules, and deduplication-off scope are
versioned separately from configurable collector behavior. This repository and
fleetdiff retain the same cases and checksum manifest. Collector-side expectations
for six extreme-input cases intentionally differ, as described under
[Extreme-Input Hardening](#extreme-input-hardening). Coordinate both copies when
deliberately changing the shared contract; this hardening does not rewrite it.

The contract is based on the
[OpenTelemetry GenAI span conventions](https://github.com/open-telemetry/semantic-conventions-genai/blob/94f432d7126f5884d30a2cdde6f4e89908ebb6fd/docs/gen-ai/gen-ai-spans.md)
reviewed on 2026-09-05. Those conventions are still developing, so this repository
pins its own behavior instead of inheriting changes from a moving document.
See the [semantic-convention compatibility notes](SEMANTIC_CONVENTIONS.md) for
the reviewed revision, supported fields, and deliberate differences.

## Event Scope

One span that matches `operation_filter.llm_operations` is one observed model
request. The defaults are `chat`, `generate_content`, `text_completion`, and
`embeddings`. A span-level `gen_ai.request.model` is used only when
`gen_ai.operation.name` is absent, for compatibility with older instrumentation.

The counting unit is the matching exported span, not an inferred logical call. If
both client-side and server-side instrumentation emit matching spans for one call,
both are counted. Keep one accounting source in the connector pipeline when a
single logical-call total is required.

In user-facing reports, call these **observed model attempts**. A failed attempt
and its successful retry can each produce a matching span, so both count. The
metric and summary field names retain `requests` for compatibility. This does
not establish how many attempts never emitted telemetry or reached the provider.

Agent, tool, retrieval, workflow, planning, memory, response-fetch, and MCP
transport spans do not enter the model request denominator. A root `invoke_agent`
span increments the separate agent-run
counter. Attributes are read from the span being classified; parent attributes are
never inherited.

This is observed-span accounting. If instrumentation emits separate model spans for
retries, each span is counted unless optional deduplication suppresses it. Automatic
retries hidden inside one model span remain one observed request.

Oversized configured hashed-field values (over 8 KiB) and invalid UTF-8 reject only
the affected span from sketch accounting. Valid peers in the batch remain eligible.
`gen_ai_sketch_rejected_spans_total` records the fixed rejection reason without raw
input labels; see [Field Mapping](CONFIGURATION.md#field-mapping).

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

Version 1 does not export modality-specific text, audio, or image breakdowns, or
provider-only fields outside the configured source lists. Those attributes remain
on the original trace and are ignored by these accounting counters.

`gen_ai_sketch_total_tokens_total` is input plus output. Cache and reasoning fields
are subsets and are never added to that total. Detail fields are never used to
invent a missing aggregate. When a detail exceeds its reported parent, the value is
still exported and a subset violation is reported, provided the value is within the
per-field cap below.

A present integer zero is reported usage. An absent, negative, fractional, or
non-numeric value is not converted to zero. A request increments
`gen_ai_sketch_missing_token_usage_total` when either aggregate input or aggregate
output is unavailable after source selection and provenance rules. All five token
fields have a fixed inclusive cap of `1,000,000,000` per source value, not per request
total or cumulative counter.
An over-cap source is invalid; the first valid configured fallback can still supply
the field. With no valid source, an over-cap field is missing, not reported zero.
The request and other valid fields remain counted. This changes over-cap input
acceptance; it does not reject the batch.

Cumulative metrics begin when their retained slice is created. A collector restart
or deterministic eviction followed by recreation starts a new cumulative stream for
that label set. Prometheus `rate()` and `increase()` handle the lower value as a
counter reset. Lifetime counters clamp at the signed 64-bit maximum and emit
`gen_ai_sketch_accounting_saturated`; they do not reject later batches. Their
saturation is independent of summary windows. An unavailable summary window is
skipped before serialization, preserving any earlier partial file and its
`observed_end_unix_nano` until retention cleanup. Other windows continue. Saturated
ranking streams are omitted with `truncated: true`. See
[Accounting Saturation](CONFIGURATION.md#accounting-saturation) for diagnostic scopes.

The bounded `gen_ai_sketch_token_field_observations_total` metric reports fixed
`token_field` and `state` values. Aggregate fields emit exactly one primary state,
`reported` or `missing`, for every counted request. Optional detail fields emit
`reported` only for valid usage. An over-cap detail field with no valid fallback
emits `missing`; merely absent optional detail fields do not. `invalid`, `conflict`,
and `subset_violation` are additional quality states and can coexist with a primary
state.

Additive source-provenance support (2026-09-26) records instrumenter-declared
input/output origin separately: provider-reported, inferred, unavailable, or
unknown. An explicit `unavailable` declaration excludes that field's count and
marks the request missing, even if a gateway supplied a numeric placeholder.
Without that declaration, existing accounting is unchanged: numeric `reported`
does not mean provider-reported. See [Usage Provenance](USAGE_PROVENANCE.md) for
the optional versioned extension and old/new summary compatibility.

## Batch And Delivery Failures

Every input batch is transactional, with or without model-usage summaries. An
unexpected accounting failure before commit rolls back its entire processed prefix.
After commit, downstream metrics delivery failures produce a fixed warning without
raw error text and are not returned as trace errors. The failed snapshot is dropped;
later cumulative counters can recover counts, but missed window-specific gauges
need not be replayed. Restarts and slice eviction can reset those counters.

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
- Gateway-inferred usage is distinguishable only when instrumentation explicitly
  records source provenance before normalization. Provider-reported counts may
  themselves be provider estimates; the declaration does not certify measurement
  accuracy or billing equivalence. Unannotated input remains unknown.

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

### Extreme-Input Hardening

The per-field cap and malformed-identity isolation are a narrow security exception
to the versioning rule above, not an exemption for future semantic changes. The cap
makes even a previously accepted value of `1,000,000,001` invalid; the change is not
limited to integer overflow. Acceptance and missing-usage behavior for over-cap
inputs intentionally change.

Healthy, unsaturated input retains its accounting semantics, summary wire format,
keyed identities, and output-byte compatibility. Mixed-version equivalence is
limited to that unchanged input domain. Unchanged wire format does not imply
collector/fleetdiff accounting parity for over-cap or malformed inputs.

The shared v1 corpus and checksums remain frozen. The collector applies six explicit
[hardened expectations](../connector/genaisketchconnector/accounting_resilience_test.go):
`aggregate-counter-saturation`, `error-per-span-token-sum`,
`error-per-span-string-range`, `error-per-span-double-range`,
`error-aggregate-counter-overflow`, and `error-aggregate-topk-overflow`.
All other shared v1 expectations remain unchanged. Do not assume an offline reader
implements the same extreme-input behavior merely because it uses the frozen v1
corpus or reads the same summary format.

When migrating, monitor rejected spans, invalid/missing token-field observations,
and saturation diagnostics. Over-cap usage is unavailable rather than zero, and
malformed identities exclude their spans from sketch accounting. Earlier partial
summaries and omitted rankings are not evidence of complete or zero activity.
