# Configuration

The complete runnable example is
[examples/collector/config.yaml](../examples/collector/config.yaml). This document
describes the connector-specific fields.

## Minimal Configuration

```yaml
connectors:
  genaisketch:
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

## Capacity And Windows

| Field | Default | Limit | Meaning |
| --- | ---: | ---: | --- |
| `window_duration` | `1m` | `24h` | Fixed sketch window |
| `retention_windows` | `10` | `120` | Retained completed windows |
| `max_slices` | `2000` | `5000` | Total retained slice states |
| `topk` | `20` | `100` | Entries per structured top-k summary |

At most 32 slices may be configured, with at most 8 keys per slice. Slice capacity is
global. Inactive slices become eligible for deterministic least-recently-used
eviction only after they have been absent for more than one window. When capacity is
still unavailable, observations are combined into one `__overflow__` value for that
slice. No observation is silently dropped and no unconfigured label value is created.

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
signature, retrieval document, and MCP identity.
Each hashed field searches all configured span candidates before all configured
resource candidates. Parent and child spans are evaluated independently.

Raw attribute values larger than 8 KiB are ignored. Values are canonicalized and
keyed before entering sketch state; raw values are not retained by the connector.

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
Weighted prompt top-k summaries require both aggregate fields. Missing or invalid
fields are counted explicitly and are never replaced with guessed values.

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

## Deduplication

Deduplication is optional and requires a stable span-level request-ID source. The
defaults are `gen_ai.response.id` and `request.id`, in that order. Resource
attributes are not used. The special `trace_id` source is available only when one
trace is known to contain at most one model request; it is deliberately not a
default. Deduplication uses a bounded, per-slice, per-window Bloom filter. False
positives can suppress a previously unseen request and therefore undercount; use it
only when duplicate suppression is worth that explicit tradeoff. Suppressed
requests and requests missing a deduplication key have separate counters.

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
