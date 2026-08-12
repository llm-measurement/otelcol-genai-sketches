# Frequently Asked Questions

## How Does This Fit Into An Agent Observability Stack?

Trace and evaluation systems explain individual agent runs. This connector derives
bounded fleet-level signals across many runs: LLM request and agent-run counts,
reported token concentration and completeness, distinct keyed populations, and
controlled slices for low-cardinality operational dimensions.

It is the measurement layer between OTLP traces and an observability backend. It
complements trace explorers, evaluation systems, behavior or anomaly detectors, and
control planes; it does not replace them or diagnose individual agent decisions.

## How do I keep Prometheus cardinality bounded for high-volume LLM traces?

Configure only a small set of operator-chosen slices such as model, provider, team,
or route. `max_slices` bounds retained state. Once capacity cannot be reclaimed from
inactive entries, all additional values for a configured slice go to one
`__overflow__` label value. The connector does not create labels from prompts,
users, documents, requests, or top-k hashes.

## Can I estimate distinct prompts or users without exporting the raw values?

Yes. The connector canonicalizes configured attributes, applies a keyed hash, and
updates bounded HLL++ sketches. Prometheus receives only the resulting distinct-count
estimate. Raw values and keyed hashes do not appear in metrics or labels.

This is pseudonymization, not anonymization. A party holding the secret can test
candidate values, and the same value remains linkable while the secret is unchanged.

## Can This Detect Or Stop Token Maxing?

It can detect and localize unexpected reported token consumption. Compare request
rate with token rate and tokens per request, use bounded slices to identify an
affected team, model, provider, or route, and inspect token-weighted top-k prompt
signatures for high-cardinality concentration.

It cannot decide whether tokens were useful, infer unreported usage, recover prompt
text, enforce a budget, stop an agent loop, or prevent a context-window error. Those
actions require an application or control component downstream of the telemetry.
The [token-consumption playbook](TOKEN_USAGE.md) gives a complete investigation
workflow and PromQL examples.

## What happens when token usage is missing?

The connector does not substitute zero or estimate a token count. A matched request
with neither configured usage field increments
`gen_ai_sketch_missing_token_usage_total`. Tool and agent spans without usage do not
inflate that denominator.

## How are requests counted in agent and tool traces?

Requests are operation-filtered. By default, only `chat`, `generate_content`,
`text_completion`, and `embeddings` spans count as LLM requests. Agent, tool,
retrieval, workflow, and MCP transport spans do not count. Root `invoke_agent` spans
have a separate agent-run counter.

Resource attributes can supply identity and configured slice values, but attributes
are never inherited from parent spans. This keeps the result independent of which
collector instance receives a particular child span.

## Why are top-k prompt signatures logs instead of metric labels?

Heavy-hitter identities are high-cardinality and change over time. Putting them in
labels would defeat the bounded metric surface. The connector emits a bounded
structured snapshot containing keyed signatures, weighted estimates, and lower and
upper bounds. Operators can route that debug surface separately from Prometheus.

## Are slice values encrypted or hashed?

No. Slice values are readable Prometheus labels by design. They must be bounded,
low-cardinality, non-sensitive attributes. Use hashed fields and sketch estimates for
user IDs, prompts, documents, request IDs, MCP resource URIs, and similar values.

## Does secret rotation affect the metrics?

Counters and token totals are not keyed. Distinct sketches and top-k signatures are.
After rotation, the same raw value maps to a different keyed value, so cross-secret
comparison and linkability stop. Rotate at a window boundary when continuity matters.

## Can deduplication undercount?

Yes. Optional request deduplication uses a Bloom filter, which can return false
positives. That bounded-memory tradeoff can suppress a new request. The feature is
off unless configured with a stable request-ID source.

## What happens when a configured attribute is absent or too large?

Missing slice parts use the fixed `<missing>` label value. Slice parts longer than
the accepted limit use `<too_long>`. Raw attributes larger than 8 KiB are ignored
before canonicalization or sketch updates.

## Does the connector support MCP telemetry?

Yes, behind an off-by-default configuration switch. It can estimate distinct MCP
sessions, methods, and resources, and optionally summarize tool errors. MCP session,
resource, and JSON-RPC request identifiers are forbidden as plaintext slice keys.

## How much traffic has this implementation been exercised with?

The recorded hour-long runs each accepted and exported 36 million spans at 10,000
spans/second on an Apple M4 Max. One workload used mixed tree-shaped traces and
verified exact request and missing-token accounting. See
[Benchmarks](BENCHMARKS.md) for the complete workload and machine details.

## Is this production-stable?

No. The repository is alpha software and the connector stability level is
development. Pin an exact release, review metric semantics, validate memory against
your own slice distribution, and rehearse secret rotation before production use.
