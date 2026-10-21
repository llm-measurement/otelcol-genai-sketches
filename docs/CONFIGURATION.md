# Configuration

The complete runnable example is
[examples/collector/config.yaml](../examples/collector/config.yaml). This document
describes the connector-specific fields.

## Minimal Configuration

```yaml
connectors:
  genaisketch:
    window_duration: 1m
    retention_windows: 10
    max_slices: 2000
    topk: 20
    hashing:
      algo: hmac_sha256_64
      secret_env: GENAI_SKETCH_SECRET
    slices:
      - name: model
        keys: [gen_ai.request.model]
        from_resource_attributes: [gen_ai.request.model]
```

`GENAI_SKETCH_SECRET` must contain at least 16 bytes. Generate at least 128 bits of
entropy and keep the value out of configuration files, images, logs, and source
control.

When using the standalone connector with the OpenTelemetry Collector Builder,
configure `genaisketch` as an exporter from the traces pipeline and a receiver in
the metrics pipeline. The `path:` override in this repository's builder manifest
is only for a local checkout; published-module builds use the versioned `gomod`
entry from [Install](../README.md#install).

## Capacity And Windows

| Field | Default | Limit | Meaning |
| --- | ---: | ---: | --- |
| `window_duration` | `1m` | `24h` | Fixed sketch window |
| `retention_windows` | `10` | `120` | Retained windows, including the current one |
| `max_slices` | `2000` | `5000` | Total retained slice states |
| `topk` | `20` | `0`-`100` | Entries per structured top-k summary; `0` disables the surface |

At most 32 slices may be configured, with at most 8 keys per slice. Slice capacity is
global. Inactive slices become eligible for deterministic least-recently-used
eviction only after they have been absent for more than one window. When capacity is
still unavailable, observations are combined into one `__overflow__` value for that
slice. Slice capacity does not silently drop observations or create unconfigured
label values. Invalid span inputs are handled separately, as described below.

Set `topk: 0` when structured top-k logs are not permitted. The connector then
does not allocate or update frequent-items state and emits no top-k snapshots.
Counters and distinct estimates remain enabled.

Metrics and top-k snapshots read only the current window. Retaining more windows
uses additional memory but does not provide a historical query or export API.
Retention defaults are unchanged for configuration compatibility.

## Slices

```yaml
slices:
  - name: team_model
    keys: [team.id, gen_ai.request.model]
    from_resource_attributes: [team.id]
```

For each key, the connector checks the span first. It checks the resource only when
that key is listed in `from_resource_attributes`. Missing parts use the fixed `<missing>`
value; oversized label parts use `<too_long>`.

Slice values are plaintext Prometheus labels. Restrict them to operator-chosen,
bounded, non-sensitive dimensions. `mcp.session.id`, `mcp.resource.uri`, and
`jsonrpc.request.id` are rejected as slice keys. A slice key that overlaps a
configured hashed-field source is also rejected.

## Operation Filter

```yaml
operation_filter:
  llm_operations: [chat, generate_content, text_completion, embeddings]
```

Only matching spans contribute to request, token, missing-usage, user, prompt, and
document signals. When the operation attribute is absent, a span-level
`gen_ai.request.model` provides compatibility with older instrumentation. The
connector does not inherit operation or model attributes from parent spans.

## Field Mapping

The `fields` map selects ordered attribute candidates for user identity, prompt
signature, model conversation/session, retrieval document, and MCP identity.
Each hashed field searches all configured span candidates before all configured
resource candidates. Parent and child spans are evaluated independently.

For example, under `connectors.genaisketch`:

```yaml
fields:
  user_key:
    from_attributes: [enduser.id, user.id]
    from_resource_attributes: [enduser.id, user.id]
    canonicalization: text_v1
    domain: user:v1
```

Only add resource candidates when that resource really identifies one user.
The distinct-user gauge estimates the current window, not all retained windows.
Use a longer `window_duration` or merge compatible summary exports for a longer
period; do not add the gauges. Mapping `user_key` does not enable user top-k.

Configured hashed-field values larger than 8 KiB or containing invalid UTF-8
reject only that span from sketch accounting. Other valid spans in the batch remain
eligible for accounting. `gen_ai_sketch_rejected_spans_total` counts rejections with
only the fixed `reason` values `invalid_utf8` and `attribute_too_large`, never raw
rejected values or slice labels. Accepted values are canonicalized and keyed before
entering sketch state; raw values are not retained by the connector.

## Top-K Keys

`topk_keys` is available from v0.3.0; v0.2.0 images do not support it.
Omitting it preserves the default: `prompt_key` weighted by reported input plus
output tokens. User/session ranking is opt-in. The default `session_key` reads
`gen_ai.conversation.id`, then `session.id`, from the model span and uses
`text_v1` canonicalization. It is separate from MCP session identity.
See [Top-K Keys](TOPK_KEYS.md) for configuration, token versus request weights,
summary compatibility, and limits.

## Sketch Profiles

```yaml
profiles:
  hllpp: default
  frequent_items: default
  bloom: default
```

Each profile accepts `micro`, `small`, or `default`. Smaller profiles reduce bounded
state at the cost of wider error. Profile definitions come from the pinned
`llm-sketchkit` module version.

## Token Weights

Token source lists are ordered. The first valid source wins; a different value in a
later source is counted as a conflict rather than added. Input and output are
independently optional on incoming spans, but both source lists must be configured.
Token-weighted top-k summaries require both aggregate fields. Missing or invalid
fields are counted explicitly and are never replaced with guessed values. Invalid
usage does not reject the request: valid fields still contribute their values,
while unavailable input or output contributes to missing-usage accounting.

Each numeric source value must be an integer from `0` through `1,000,000,000`,
inclusive. This fixed cap applies independently to input, output, cache-read input,
cache-write input, and reasoning output tokens. It is not a combined-token or
cumulative budget, and it is not configurable. An over-cap source records
`invalid`; a later valid configured source can still supply the field. If an
over-cap field has no valid source, it is unavailable and records `missing`, even
for an optional detail field. Merely absent optional detail fields do not emit
`missing`. The request remains counted and the value is not replaced with a
reported zero.

The summary wire format is unchanged, but this cap intentionally changes acceptance
of over-cap inputs.

```yaml
weights:
  input_tokens_from: [gen_ai.usage.input_tokens, gen_ai.usage.prompt_tokens]
  output_tokens_from: [gen_ai.usage.output_tokens, gen_ai.usage.completion_tokens]
  cache_read_input_tokens_from: [gen_ai.usage.cache_read.input_tokens]
  cache_write_input_tokens_from:
    [gen_ai.usage.cache_write.input_tokens, gen_ai.usage.cache_creation.input_tokens]
  reasoning_output_tokens_from: [gen_ai.usage.reasoning.output_tokens]
  fallback_when_missing: request_count_only
```

Cache and reasoning values are reported separately as subsets; they are not added to
input, output, or total tokens. See [Production Accounting Semantics](ACCOUNTING.md)
for precedence, validity, lifecycle, and reconciliation rules.

## Accounting Saturation

Reaching an accounting saturation limit does not by itself reject subsequent trace
batches. Lifetime slice counters clamp at the signed 64-bit maximum
(`9,223,372,036,854,775,807`). Their saturation is independent of summary-window
accounting: a saturated lifetime counter alone does not make an otherwise healthy
summary window unavailable.

`gen_ai_sketch_accounting_saturated` is a gauge with fixed `surface` and `reason`
labels. It emits only nonzero diagnostics:

| `surface` | Value and scope |
| --- | --- |
| `slice_counters` | `1` per saturated lifetime slice-counter reason, with the usual slice labels |
| `slice_window` | `1` per saturation reason in the current slice window, with the usual slice labels |
| `summary_window` | Number of retained unavailable summary windows per reason, without slice labels |
| `rejections` | `1` with `reason="counter_limit"` when an increment would exceed a connector-wide rejection counter's signed 64-bit maximum, without slice labels |

The eight fixed reasons are `counter_limit`, `total_tokens_limit`,
`topk_weight_limit`, `tool_error_weight_limit`, `dedup_count_limit`,
`model_usage_overflow`, `model_count_limit`, and `summary_byte_limit`.
Slice and window flags remain set for the affected slice or window's lifetime.
Slice eviction or restart resets lifetime slice flags; window diagnostics follow
the current-window and retention scopes above. The connector-wide `rejections`
flag stays set until restart; reaching the exact maximum does not set it, and
counts clamp at that maximum. Multiple reasons can describe the same window, so
adding reason counts does not give a distinct-window count.

An affected summary window is unavailable as a whole and is skipped before payload
serialization. Any earlier partial file for that window stays unchanged, including
its `observed_end_unix_nano`, until normal retention removes it. It is not replaced
with a clipped or apparently complete summary. Other windows continue to export; an
earlier partial file is not evidence of complete coverage of the affected window.

A saturated top-k or tool-error ranking stream is omitted from structured
snapshots and sets `truncated: true`. Unsaturated ranking streams continue. Missing
summaries and omitted rankings must not be interpreted as zero activity.

## Batch And Delivery Failures

Accounting uses a transaction for every input batch, whether or not model-usage
summaries are enabled. Rejected spans do not prevent valid peers from being counted.
An unexpected accounting failure before commit rolls back the batch, including its
processed prefix, before returning an error to the trace pipeline.

Once accounting commits, a downstream metrics delivery failure is logged and the
failed snapshot is dropped, not returned as a trace error. This avoids asking the
sender to replay already-counted traces. A later successful snapshot includes
cumulative counters, subject to the existing restart and slice-eviction resets.
Missed window-specific gauges need not be replayed. The warning omits raw downstream
error text, and there is no durable metrics retry queue in the connector.

## Deduplication

Deduplication is optional and requires a stable span-level request-ID source. The
defaults are `gen_ai.response.id` and `request.id`, in that order. Resource
attributes are not used. The special `trace_id` source is available only when one
trace is known to contain at most one model request; it is deliberately not a
default. Deduplication uses a bounded, per-slice, per-window Bloom filter. False
positives can suppress a previously unseen request and therefore undercount; use it
only when duplicate suppression is worth that explicit tradeoff. Suppressed
requests and requests missing a deduplication key have separate counters.

Each filter stops accepting inserts at its profile's design capacity: 10,000
(`micro`), 100,000 (`small`), or 1,000,000 (`default`). Once full, requests with a
deduplication key bypass both lookup and insertion for the rest of that window.
They are counted normally, including possible duplicates, and increment
`gen_ai_sketch_dedup_overflow_total`. The next window starts with a fresh filter.
Overflow is a Prometheus metric only; summary exports keep their existing counter
set so normal and overflowed windows remain compatible.

## MCP

```yaml
mcp:
  enabled: true
  tool_errors:
    enabled: true
```

MCP support is off by default. When enabled, MCP sessions, methods, and resource URIs
are canonicalized and keyed before entering distinct sketches. Tool-error summaries
use the same bounded structured-log surface as other frequent items. MCP identifiers
never become metric label values.

`mcp.resource.uri` shares the retrieval-document hash domain so that one logical
resource has comparable keyed identity whether it is observed through MCP or native
retrieval instrumentation.

## Validation

Unknown fields, unsupported profile names, invalid durations, empty slice names,
duplicate slice names, capacity violations, sensitive slice keys, and insecure or
missing secrets fail collector startup. Validate changes locally with:

```bash
make check
make dist
GENAI_SKETCH_SECRET="$(openssl rand -hex 32)" make run-local
```
