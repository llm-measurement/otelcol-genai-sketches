# Usage Provenance

Numeric usage and source coverage are different measurements. A gateway may
replace missing provider counts with zeros or local estimates. Provenance records
the instrumenter's declaration of where each input/output count came from.
Explicitly unavailable counts are excluded from totals and count as missing usage.
Unannotated traffic keeps its existing accounting behavior.

## Input Contract

These project-specific span attributes are not OpenTelemetry semantic conventions:

- `gen_ai_sketch.usage.input.provenance`
- `gen_ai_sketch.usage.output.provenance`

| Value | Meaning |
|---|---|
| `provider_reported` | A valid count was observed before normalization and emitted unchanged |
| `inferred` | An instrumented estimator supplied the emitted count |
| `unavailable` | Source usage was inspected but no usable provider count or explicitly identified estimate was available |
| `unknown` | Source provenance was not captured, was invalid, or cannot be established |

Absent or unrecognized values become `unknown`, never a new metric label.
Provider/inferred declarations with missing, invalid, or conflicting numeric
observations also become unknown. A genuine provider-reported zero remains valid.
Do not infer provenance from count size, model name, or SDK version.

Only span attributes are consulted. Instrumentation must overwrite untrusted
caller declarations at the source boundary. The collector does not authenticate
these assertions; they are not independent evidence for billing.

## Before Normalizing

The standard-library [Python helper](../examples/integrations/litellm/usage_provenance.py)
retains two optional integer counts, not the response, prompt, or identity. Use it
where the raw provider response is still available:

```python
from usage_provenance import capture

source = capture(raw_response.get("usage"))  # before defaults or estimates
# Run the application's existing normalization here.
attributes = source.attributes(normalized_usage, inferred_fields=estimated_fields)
for key, value in attributes.items():
    model_span.set_attribute(key, value)
```

`estimated_fields` must come from the estimator that actually supplied each value;
the helper does not guess. Use an empty set when no estimation occurred. Captured
values survive in-place mutation of the original response. Changed provider
counts become unknown. For streaming, capture the final provider usage object
before stream aggregation fills fields. Interrupted or uninspected streams remain
unknown; do not call `capture(None)` unless absence was actually observed.

The opt-in [LiteLLM callback](../examples/integrations/litellm/README.md#capture-source-provenance)
connects this helper to `CustomLogger.log_post_api_call`. In the pinned 1.102.1
OpenAI chat path, that callback receives raw response JSON before transformation.
It retains two counts in request-local state, then annotates the existing model
span. It does not patch LiteLLM or create a second tracing callback.

Only non-streaming OpenAI-compatible chat is supported. Streaming, other providers,
unsupported response shapes, and responses larger than 1,048,576 characters remain
unknown. No supported estimator hook is wired yet. Stock LiteLLM remains unknown;
passing its normalized usage into `capture()` would make a false claim.

## Output And Compatibility

`gen_ai_sketch_usage_provenance_total` adds fixed `token_field` (`input`, `output`)
and `source` labels to the usual configured slice labels. Each counted request
contributes once per field: at most eight additional series per slice. Only
nonzero states are emitted. Deduplicated requests do not increment these counters;
slice eviction and restart follow existing reset rules.

Summary files include all eight `usage_provenance.v1.<field>.<source>` counters,
including zeros. This optional extension has its own version; it does not change
the base accounting fingerprint. Fleetdiff v0.2.0 or later fills absent provenance
with unknown observations in memory when comparing old and new summaries. It does
not rewrite files, repair partial declarations, or relax any other compatibility
check. Older consumers that require identical counter sets may reject mixed exports.

An explicit `unavailable` declaration overrides the corresponding numeric field,
even a positive placeholder: that field is excluded from totals, its observation
is missing, and the request increments `missing_token_usage` once. Such requests
are excluded from token-weighted prompt top-k, as other missing-usage requests are.
Genuine provider zeros remain reported zeros. Unknown origin alone does not discard
a count. Enabling this annotation can therefore change measured totals and coverage;
the difference is an instrumentation change, not evidence of savings.

Fleetdiff adds a provider-origin question. Older summaries without these counters
return `cannot_determine`, as do unknown-origin observations. Known inferred or
unavailable origin means limited source coverage. Complete declarations still do not prove complete delivery,
unsampled traffic, or invoice accuracy.

## Regression Coverage

The shared [version-pinned cases](../examples/integrations/litellm/usage-cases.json)
are an allowlisted projection of the LiteLLM 1.102.1 mock-provider capture. Raw
usage is controlled mock input; emitted counts come from observed spans. Estimator
flags are test-scenario knowledge, not attributes stock LiteLLM provided.
Python source-helper and Go collector tests use these same six cases, with both
stock-unknown and annotated inputs. CI runs both without LiteLLM or network access.
Other versions and real providers require separate verification.
