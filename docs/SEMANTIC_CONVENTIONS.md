# Semantic-Convention Compatibility

Reviewed 2026-10-20 against OpenTelemetry GenAI semantic conventions revision
[`547934a`](https://github.com/open-telemetry/semantic-conventions-genai/tree/547934afa90a34298441caf80c43b27255c3a292),
dated 2026-10-18. GenAI and MCP definitions now live in that repository; their
development status does not mean existing attribute names have stopped working.

## Requests And Tokens

The default model operations remain `chat`, `generate_content`, `text_completion`,
and `embeddings`. The current registry also defines planning, memory, and
`fetch_response` operations. Fetching a previous response is not new inference.
These operations do not enter the connector's default model-request or
missing-token counts, even when they carry a model name. This is covered by the
`non_inference_operations.json` accounting fixture.
[Operation definitions](https://github.com/open-telemetry/semantic-conventions-genai/blob/547934afa90a34298441caf80c43b27255c3a292/docs/registry/attributes/gen-ai.md#gen-ai-operation-name).

Cache-read and cache-write input tokens belong within input totals; reasoning
tokens belong within output totals. The connector already reports these details
without adding them again. Modality-specific text, audio, and image attributes are
not separate connector counters and do not fill in missing aggregate totals.
Upstream advises instrumentation to report billed counts when a provider exposes
both billed and model-consumed counts. This connector reads the supplied totals;
it cannot determine their billing provenance or calculate an invoice.
[Token definitions](https://github.com/open-telemetry/semantic-conventions-genai/blob/547934afa90a34298441caf80c43b27255c3a292/docs/registry/attributes/gen-ai.md#gen-ai-usage-input-tokens).

See [How It Works](../README.md#how-it-works) for the missing-usage comparison.

The pinned revision replaces `gen_ai.client.token.usage` with
`gen_ai.client.inference.usage.*` counters for token consumption and
`gen_ai.client.inference.operation.input_tokens` and
`gen_ai.client.inference.operation.output_tokens` histograms for
per-operation distributions. The connector's `gen_ai_sketch_*` metrics retain the
[`genai-accounting/v1`](ACCOUNTING.md) contract; they complement those standard
instruments and the agent-call metrics.

The [accounting source table](ACCOUNTING.md#token-fields) marks legacy aliases
that the connector still reads. For prompt attribution, prefer the
application-supplied [`gen_ai.prompt.name`](https://github.com/open-telemetry/semantic-conventions-genai/blob/547934afa90a34298441caf80c43b27255c3a292/docs/registry/attributes/gen-ai.md#gen-ai-prompt-name);
the [field-mapping example](CONFIGURATION.md#field-mapping) is opt-in and leaves
the default unchanged.

## MCP

Upstream MCP conventions define spans, operation and session duration metrics, and
context propagation through `params._meta`. Propagation belongs to instrumentation;
this connector neither reconstructs trace trees nor modifies that context.
MCP tool execution can share a span with GenAI tool instrumentation. Neither form
is a model request under the default operation filter.
[MCP conventions](https://github.com/open-telemetry/semantic-conventions-genai/blob/547934afa90a34298441caf80c43b27255c3a292/docs/gen-ai/mcp.md).

The connector's optional tool-error summaries use `gen_ai.tool.name` and
`error.type`, including upstream's `tool_error` value for an unsuccessful tool
result. It does not parse raw tool results or infer errors from RPC codes.
Instrumentation must supply `error.type` for that summary.

MCP session IDs, resource URIs, and JSON-RPC request IDs remain prohibited slice
keys. Their presence in the conventions is not permission to export them as
Prometheus labels. Existing sentinel tests scan metrics and structured snapshots.
