# Semantic-Convention Compatibility

The connector counts observed model attempts and tokens, with an optional count of
root in-process agent, workflow, and framework invocations. Existing request,
token, and agent-run counters keep their accounting definitions.

Reviewed 2026-11-24 against OpenTelemetry GenAI semantic conventions
[v0.1.0](https://github.com/open-telemetry/semantic-conventions-genai/blob/d0e74ba6c48bcc4ded473b1828a8212f06b23753/CHANGELOG.md#v010),
commit [`d0e74ba`](https://github.com/open-telemetry/semantic-conventions-genai/tree/d0e74ba6c48bcc4ded473b1828a8212f06b23753).
GenAI and MCP definitions live in that repository; these conventions remain in
development, so this connector pins its own accounting behavior.

## Root Invocations

V0.1.0 folds workflows and framework entrypoints into the
`gen_ai.invoke_agent.internal` span type. Their operation names remain
`invoke_workflow` and `invoke_{framework_entrypoint}`; for example, ADK and OpenAI
Agents use `invoke_runner`. Workflows now record their application-supplied name
in `gen_ai.agent.name`, replacing `gen_ai.workflow.name`.
[Agent and framework spans](https://github.com/open-telemetry/semantic-conventions-genai/blob/d0e74ba6c48bcc4ded473b1828a8212f06b23753/docs/gen-ai/gen-ai-agent-spans.md#invoke-agent-internal-span).

Enable `root_invocations` to emit `gen_ai_sketch_root_invocations_total`. It counts
only spans of kind `INTERNAL` with an empty parent span ID and an operation in the
configured list. That list defaults to `invoke_agent` and `invoke_workflow`;
add `invoke_runner` explicitly when the framework documents that entrypoint.
An entrypoint above nested agents counts once. The metric adds no labels: it uses
the existing bounded slice labels, not operation names or IDs.
[Configuration](CONFIGURATION.md#root-invocations).

The existing `gen_ai_sketch_agent_runs_total` remains the legacy count of
parentless `invoke_agent` spans, without a span-kind restriction. It is not added
to the new count. With `root_invocations.enabled: false`, the new metric and all
of its optional summary keys are absent. The checked-in compatibility corpus
verifies v0.7.4 output bytes across the existing fixtures and configuration profiles.

Remote invocation instrumentation records a caller `CLIENT` span and a framework
`INTERNAL` span ([#524](https://github.com/open-telemetry/semantic-conventions-genai/pull/524)).
The new rule excludes `CLIENT` spans. A parented `INTERNAL` span is also excluded,
even if its parent is exported to another collector: root detection uses the
span's parent ID, not whether a parent happens to be present in a batch. This
does not reconstruct a logical invocation across services.

Summary exports use a namespaced group inside the existing `counters` map
(`Envelope.Counters`), not a new top-level envelope block. The count and its
literal operation definition travel together:

```json
{
  "root_invocations.v1.count": 17,
  "root_invocations.v1.operation.invoke_agent": 0,
  "root_invocations.v1.operation.invoke_workflow": 0,
  "root_invocations.v1.rule.root_internal_parentless": 0
}
```

These are additional counter entries, not a complete summary. The operation and
rule entries are zero-valued contract markers, not measurements. Canonical JSON
sorts the keys; changing list order alone does not change the definition. The base
accounting ID is unchanged. Existing `Compatible` and `Combine` checks reject
different counter-key sets, including different root-operation definitions and
enabled versus disabled exports. Readers must not silently discard these keys
to combine otherwise incompatible summaries.

## Requests And Tokens

The default model operations remain `chat`, `generate_content`, `text_completion`,
and `embeddings`. V0.1.0 classes embeddings separately from client inference;
the connector retains them in `llm_operations` because they consume billed tokens
and existing totals must not change. To omit embeddings, set
`llm_operations: [chat, generate_content, text_completion]`.
[Client inference](https://github.com/open-telemetry/semantic-conventions-genai/blob/d0e74ba6c48bcc4ded473b1828a8212f06b23753/docs/gen-ai/client-inference.md),
[token aggregation](https://github.com/open-telemetry/semantic-conventions-genai/blob/d0e74ba6c48bcc4ded473b1828a8212f06b23753/docs/gen-ai/gen-ai-token-metrics.md#how-to-aggregate-token-usage).

Planning, memory, and `fetch_response` operations are outside that default filter.
Fetching a previous response is not new inference. These spans do not enter the
default model-request or missing-token counts, even when they carry a model name.
The `non_inference_operations.json` fixture covers this separation.
[Operation definitions](https://github.com/open-telemetry/semantic-conventions-genai/blob/d0e74ba6c48bcc4ded473b1828a8212f06b23753/docs/registry/attributes/gen-ai.md#gen-ai-operation-name).

For aggregation, upstream recommends `gen_ai.client.inference.usage.*` counters
and warns against summing `gen_ai.usage.*` over unfiltered spans
([#736](https://github.com/open-telemetry/semantic-conventions-genai/pull/736)).
This connector filters incoming spans by operation and produces its own
`gen_ai_sketch_*` usage counters; it does not consume the upstream metric counters.
Agent and workflow roll-ups therefore do not add their usage to model-attempt
usage. See [Event Scope](ACCOUNTING.md#event-scope) for the counting unit.

Cache reads and writes are subsets of input; reasoning is a subset of output.
The canonical names are `gen_ai.usage.cache_read.input_tokens`,
`gen_ai.usage.cache_write.input_tokens`, and
`gen_ai.usage.reasoning.output_tokens`. The pinned release renames
`cache_creation` to `cache_write`. The [token source table](ACCOUNTING.md#token-fields)
retains `cache_creation`, `prompt_tokens`, and `completion_tokens` as ordered
legacy fallbacks; none is added to its canonical equivalent. Modality-specific
text, audio, and image details do not fill missing aggregate totals.
[Token definitions](https://github.com/open-telemetry/semantic-conventions-genai/blob/d0e74ba6c48bcc4ded473b1828a8212f06b23753/docs/registry/attributes/gen-ai.md#gen-ai-usage-input-tokens).

For prompt attribution, prefer the application-supplied
[`gen_ai.prompt.name`](https://github.com/open-telemetry/semantic-conventions-genai/blob/d0e74ba6c48bcc4ded473b1828a8212f06b23753/docs/registry/attributes/gen-ai.md#gen-ai-prompt-name).
The [field-mapping example](CONFIGURATION.md#field-mapping) is opt-in. The
compatibility default `gen_ai.request.prompt` is not a v0.1.0 registry attribute;
it remains readable and is keyed rather than exported as a raw label.

## MCP

Upstream MCP conventions define spans, operation and session duration metrics, and
context propagation through `params._meta`. Propagation belongs to instrumentation;
this connector neither reconstructs trace trees nor modifies that context.
MCP tool execution can share a span with GenAI tool instrumentation. Neither form
is a model request under the default operation filter.
[MCP conventions](https://github.com/open-telemetry/semantic-conventions-genai/blob/d0e74ba6c48bcc4ded473b1828a8212f06b23753/docs/gen-ai/mcp.md).

The connector's optional tool-error summaries use `gen_ai.tool.name` and
`error.type`, including upstream's `tool_error` value for an unsuccessful tool
result. It does not parse raw tool results or infer errors from RPC codes.
Instrumentation must supply `error.type` for that summary.

## Limits

- Missing usage remains visible: the connector counts matching calls without
  available aggregate usage separately as missing. Upstream usage counters record
  only positive contributions ([#602](https://github.com/open-telemetry/semantic-conventions-genai/pull/602)),
  while per-operation histograms record zero when a response or its usage is
  missing ([#536](https://github.com/open-telemetry/semantic-conventions-genai/pull/536)).
  Neither standard instrument identifies those calls as missing usage; see the
  pinned [usage counters and histograms](https://github.com/open-telemetry/semantic-conventions-genai/blob/d0e74ba6c48bcc4ded473b1828a8212f06b23753/docs/gen-ai/gen-ai-token-metrics.md#inference-token-metrics).
- An invocation under an HTTP or other parent is not parentless, so the new count
  excludes it. A semantic conversation-root marker remains a separate upstream
  question ([#356](https://github.com/open-telemetry/semantic-conventions-genai/issues/356)).
- Counts describe exported spans and remain subject to sampling. If both a
  gateway and provider emit inference spans with the same operation, the default
  filter counts both; it does not deduplicate across services. Select one
  accounting source, or keep services separate with bounded slices based on
  `service.name`, following the pinned
  [aggregation guidance](https://github.com/open-telemetry/semantic-conventions-genai/blob/d0e74ba6c48bcc4ded473b1828a8212f06b23753/docs/gen-ai/gen-ai-token-metrics.md#how-to-aggregate-token-usage).
- Supplied token totals do not establish billing provenance or calculate an
  invoice; [usage provenance](USAGE_PROVENANCE.md) is an instrumenter declaration.
- MCP session IDs, resource URIs, and JSON-RPC request IDs remain prohibited slice
  keys. Their presence in the conventions is not permission to export them as
  Prometheus labels. Existing sentinel tests scan metrics and structured snapshots.
