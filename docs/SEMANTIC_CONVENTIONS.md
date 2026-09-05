# Semantic-Convention Compatibility

Reviewed 2026-09-05 against OpenTelemetry GenAI semantic conventions revision
[`94f432d`](https://github.com/open-telemetry/semantic-conventions-genai/tree/94f432d7126f5884d30a2cdde6f4e89908ebb6fd),
dated 2026-09-03. GenAI and MCP definitions now live in that repository; their
development status does not mean existing attribute names have stopped working.

## Requests And Tokens

The default model operations remain `chat`, `generate_content`, `text_completion`,
and `embeddings`. The current registry also defines planning, memory, and
`fetch_response` operations. Fetching a previous response is not new inference.
These operations do not enter the connector's default model-request or
missing-token counts, even when they carry a model name. This is covered by the
`non_inference_operations.json` accounting fixture.
[Operation definitions](https://github.com/open-telemetry/semantic-conventions-genai/blob/94f432d7126f5884d30a2cdde6f4e89908ebb6fd/docs/registry/attributes/gen-ai.md#gen-ai-operation-name).

Cache-read and cache-write input tokens belong within input totals; reasoning
tokens belong within output totals. The connector already reports these details
without adding them again. Modality-specific text, audio, and image attributes are
not separate connector counters and do not fill in missing aggregate totals.
Upstream advises instrumentation to report billed counts when a provider exposes
both billed and model-consumed counts. This connector reads the supplied totals;
it cannot determine their billing provenance or calculate an invoice.
[Token definitions](https://github.com/open-telemetry/semantic-conventions-genai/blob/94f432d7126f5884d30a2cdde6f4e89908ebb6fd/docs/registry/attributes/gen-ai.md#gen-ai-usage-input-tokens).

The `gen_ai_sketch_*` metrics are this connector's metrics. They are not replacements
for the standard `gen_ai.client.token.usage` histogram or the agent-call metrics.
The accounting contract remains [`genai-accounting/v1`](ACCOUNTING.md).

## MCP

Upstream MCP conventions define spans, operation and session duration metrics, and
context propagation through `params._meta`. Propagation belongs to instrumentation;
this connector neither reconstructs trace trees nor modifies that context.
MCP tool execution can share a span with GenAI tool instrumentation. Neither form
is a model request under the default operation filter.
[MCP conventions](https://github.com/open-telemetry/semantic-conventions-genai/blob/94f432d7126f5884d30a2cdde6f4e89908ebb6fd/docs/gen-ai/mcp.md).

The connector's optional tool-error summaries use `gen_ai.tool.name` and
`error.type`, including upstream's `tool_error` value for an unsuccessful tool
result. It does not parse raw tool results or infer errors from RPC codes.
Instrumentation must supply `error.type` for that summary.

MCP session IDs, resource URIs, and JSON-RPC request IDs remain prohibited slice
keys. Their presence in the conventions is not permission to export them as
Prometheus labels. Existing sentinel tests scan metrics and structured snapshots.
