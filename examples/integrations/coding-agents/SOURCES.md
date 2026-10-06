# Coding Agent Signal Checks

Checked October 6, 2026. Fixtures are invented values with attribute shapes taken
from released sources and official documentation. No real capture is included.

## Copilot CLI

Pin: [v1.0.92](https://github.com/github/copilot-cli/releases/tag/v1.0.92).
Its packaged monitoring help enables traces and metrics using
`COPILOT_OTEL_ENABLED=true`, an OTLP HTTP endpoint and
`OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT=false`.
The [official reference](https://docs.github.com/en/copilot/reference/copilot-cli-reference/cli-command-reference#opentelemetry-monitoring)
documents `chat` spans with GenAI input/output tokens and span conversation IDs.
`device.id` is an operator-supplied resource attribute, not an assumed default.
`fixtures/copilot-cli.json` represents one completed model call.

## Copilot Chat

Pin: [v0.43.0](https://github.com/microsoft/vscode-copilot-chat/releases/tag/v0.43.0),
VS Code ^1.115.0. This is the checked release, not a claim about the latest
Marketplace version. The local extension exports traces, metrics and events.

- [Exporter resource](https://github.com/microsoft/vscode-copilot-chat/blob/v0.43.0/src/platform/otel/node/otelServiceImpl.ts#L115): resource `session.id` is window-scoped.
- [Model span construction](https://github.com/microsoft/vscode-copilot-chat/blob/v0.43.0/src/extension/prompt/node/chatMLFetcher.ts#L931): `gen_ai.conversation.id` receives `requestId`; a separate Copilot field receives `conversationId`.
- [Attribute constants](https://github.com/microsoft/vscode-copilot-chat/blob/v0.43.0/src/platform/otel/common/genAiAttributes.ts#L113): that separate field is `copilot_chat.session_id`.
- [Usage assignment](https://github.com/microsoft/vscode-copilot-chat/blob/v0.43.0/src/extension/prompt/node/chatMLFetcher.ts#L379): input/output fields are on the model span.
- [Configuration](https://github.com/microsoft/vscode-copilot-chat/blob/v0.43.0/src/platform/otel/common/otelConfig.ts): enabled/exporter/endpoint settings and content-off override.

`fixtures/copilot-chat.json` retains the request ID so the test catches accidentally
selecting it. Runtime variations remove the preferred session to exercise resource
fallback, or supply a conflicting resource value to prove span precedence.

## Codex CLI

Pin: [rust-v0.160.1](https://github.com/openai/codex/releases/tag/rust-v0.160.1).
**Not included as a working source.** The native-shaped fixture is a negative
compatibility check against both released readers, not a converted Codex trace.

- [Default configuration](https://github.com/openai/codex/blob/rust-v0.160.1/codex-rs/core/src/config/otel.rs#L20) sets `trace_exporter` to `None`; default settings do not export the span. `log_user_prompt` and `log_agent_responses` default to false.
- [Span construction](https://github.com/openai/codex/blob/rust-v0.160.1/codex-rs/core/src/session/turn.rs#L2606) uses `trace_span!` inside the stream-event loop. It is not itself a one-span-per-model-attempt contract. It declares usage fields, but neither `gen_ai.operation.name` nor `gen_ai.request.model`, nor `conversation.id` or `user.id`.
- [Completion handling](https://github.com/openai/codex/blob/rust-v0.160.1/codex-rs/otel/src/events/session_telemetry.rs#L601) changes `otel.name` to the event kind and records usage on completion. The fixture's exported name is `completed`.
- [Export filter](https://github.com/openai/codex/blob/rust-v0.160.1/codex-rs/otel/src/provider.rs#L341) does not reject TRACE-level spans by level once a trace exporter is configured; it excludes `h2` spans. Merely changing log verbosity does not supply model markers or identity.
- [Event metadata](https://github.com/openai/codex/blob/rust-v0.160.1/codex-rs/otel/src/events/shared.rs#L14) places `conversation.id` on log events and trace-safe events. These are not attributes of `handle_responses`. The log-only identity fields here are `user.account_id` and `user.email`, not `user.id`; the trace-safe event omits those account fields.

The [official configuration reference](https://developers.openai.com/codex/config-reference/)
documents `otel.trace_exporter`; the [telemetry guide](https://developers.openai.com/codex/config-advanced/)
describes token-bearing events separately. No synthetic identity or model marker
is added to `fixtures/codex-handle-responses.json`. Token attributes alone do not
make this span a model attempt in collector v0.3.0 or fleetdiff v0.5.0.

The recipe sends this fixture through the released collector and verifies that
the shadow backend receives it unchanged. It also runs the released `inspect`:
one span is read, but both readers count **zero model attempts** and zero tokens.
This is a source-shaped compatibility test, not a live Codex capture.

To include Codex later, establish an exported per-attempt span with an explicit
operation/model marker and identity on that span or resource. Do not convert every
stream-event span into a model attempt or promote event attributes implicitly.

## Excluded Sources

Claude Code v2.1.292 has beta traces but native usage fields and cache semantics
that need separate normalization; it is not run here.
[Claude monitoring](https://code.claude.com/docs/en/monitoring-usage).

Cursor's documented Enterprise export is server-side metrics and logs, scope
`cursor.telemetry/0.1.0`, with `/v1/metrics` and `/v1/logs` endpoints. Conversation
content is opt-in; keep it off. No spans-to-connector recipe is supplied and Cursor
is outside this work. [Wire reference](https://cursor.com/docs/enterprise/opentelemetry-export/wire).
