# Investigate A LiteLLM Application Change

Use one application first: did recorded token volume rise because there were
more model requests, or because each request used more tokens? Send LiteLLM's
OTLP spans to this collector, keep your existing backend, and compare its private
summary exports with fleetdiff.

## Connect An Existing Proxy

### Prerequisites

Use a checkout of this repository, Go 1.26.6 or later, Make, OpenSSL, and an
existing LiteLLM proxy matching the [tested scope](#compatibility-scope). Run from the
repository root. Install fleetdiff and prepare a private summary directory before
starting the collector:

```sh
make dist
go install github.com/llm-measurement/fleetdiff/cmd/fleetdiff@v0.3.0
export PATH="$(go env GOPATH)/bin:$PATH"
umask 077
export SUMMARY_DIRECTORY="$PWD/private-summaries/litellm"
mkdir -p "$SUMMARY_DIRECTORY"
chmod 700 "$SUMMARY_DIRECTORY"
export GENAI_SKETCH_SECRET="$(openssl rand -hex 32)"
export SUMMARY_KEY_ID=local-comparison-v1
```

Keep that secret and key ID unchanged across the comparison windows. Re-running
the secret-generation line breaks comparability. Do not put secrets, summary
files, provider responses, or real identities in an issue or source control.
The collector and fleetdiff do not require provider credentials; the proxy's
model calls use its existing provider configuration.

### Host Processes

In the existing LiteLLM configuration, enable its OpenTelemetry callback once:

```yaml
litellm_settings:
  callbacks: [otel]
  turn_off_message_logging: true
```

For the reviewed callback, configure generic OTLP and disable content capture:

```sh
export OTEL_SEMCONV_STABILITY_OPT_IN=gen_ai_latest_experimental
export OTEL_EXPORTER=otlp_http
export OTEL_ENDPOINT=http://127.0.0.1:4318
export OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT=NO_CONTENT
```

These loopback endpoints assume both processes run on the same host. LiteLLM
v1.102.1 registers the callback as `otel`, not `opentelemetry`. Do not enable a
second tracing callback that records the same model operation into
this connector. Preserve unrelated existing callbacks as appropriate, but review
their content-export behavior independently.

### Capture Source Provenance

For **LiteLLM 1.102.1 only**, this directory includes an opt-in callback for
non-streaming OpenAI-compatible chat. Place `provenance_callback.py` and
`usage_provenance.py` together on the proxy's `PYTHONPATH`, for example by mounting
this directory read-only at `/callbacks` and setting `PYTHONPATH=/callbacks`.
Replace `otel` in the callback list; do not register both:

```yaml
litellm_settings:
  callbacks: [provenance_callback.callback]
  turn_off_message_logging: true
```

Keep the OTLP and content-capture environment settings above. The callback uses
the documented [raw-response callback](https://docs.litellm.ai/docs/observability/custom_callback)
and [custom callback registration](https://docs.litellm.ai/docs/proxy/call_hooks).
In the pinned OpenAI path, `log_post_api_call` observes raw JSON before response
normalization. Only four optional integer counts are retained in request-local state;
the existing OpenTelemetry callback exports the declarations on the model span.
No global response interception or extra model span is added.

Streaming, other providers, unsupported response shapes, and oversized responses
stay unknown. The callback does not guess which streaming fields were estimated.
It refuses to start with an unreviewed LiteLLM version. The unit tests use a stub
for the logger base; the separate live test checks real LiteLLM integration.
Review [the validation record](VALIDATION.md) before enabling this in staging.
It includes real-provider checks with pinned GPT-4.1 mini, o4-mini, and GPT-5.4
snapshots, including retries, interrupted streams, cache reads, and reasoning.

The callback also maps `prompt_tokens_details.cached_tokens` and
`completion_tokens_details.reasoning_tokens` from the raw non-streaming response
to cache-read and reasoning subset attributes. A subset must be a non-negative
integer no larger than its parent total. Changed parent totals, conflicting
normalized subsets, and malformed values suppress that subset. Absent subsets
are not filled with zero; genuine provider zeros are preserved. Neither subset
is added to token totals. No count is inferred from a cost attribute.

Stock LiteLLM 1.102.1 omitted both subset token attributes in the tested path,
even when the client response included them. Enabling this mapping can make
previously invisible cache/reasoning usage visible. Treat that as an instrumentation
change, not evidence that a deployment changed model behavior.

Use collector v0.2.0 or later for unavailable-as-missing accounting;
collector v0.1.0 does not read these declarations. Enabling annotations
can change measured coverage, not just add metadata. Treat that boundary as an
instrumentation change, not a token-saving deployment.

### Failures And Retries

In the tested version, one failed attempt followed by a successful retry emits
two model spans. Both count as model attempts in the `requests` counter; the
failure with no usage counts as missing. This is not a count of distinct client
calls. Do not deduplicate different
provider attempts merely because they belong to one user request.

More attempts, lower recorded tokens per attempt, and more missing usage can be
a reason to investigate failures or retries. Check traces and instrumentation
coverage together; fleetdiff splits volume only when usage is complete.

A timed-out attempt may still complete at the provider and incur cost without
delivering usage to LiteLLM. An interrupted stream can also end with LiteLLM's
own estimated counts and a normal-looking completion marker. Streaming provenance
remains unknown in this callback. See [validation limits](VALIDATION.md#limits)
when interpreting completion and coverage.

Start the collector with a strong secret in `GENAI_SKETCH_SECRET`, a non-secret
key-version ID in `SUMMARY_KEY_ID`, and an existing private (0700) directory in
`SUMMARY_DIRECTORY`. Keep the same key and key ID across the comparison windows.

```sh
./dist/otelcol-genai-sketches --config=examples/integrations/litellm/collector.yaml
```

### Collector In Docker

The host configuration deliberately listens on `127.0.0.1`. A receiver bound to
container loopback cannot accept Docker-published traffic. For a container, use
the small endpoint override below and publish only host loopback ports.
First install Docker and cosign, then [verify a release image](../../../docs/DEPLOYMENT.md#verify-an-image)
to set `IMAGE_REF`. The default recipe works with v0.2.0; optional `topk_keys`
require v0.3.0 or later. The Docker path does not need `make dist`, but still
needs fleetdiff, the exports, and the private directory above.

Run as your regular, non-root host user so the private directory remains owned
and writable by that user:

```sh
docker run --rm --name genai-litellm-collector \
  --user "$(id -u):$(id -g)" --read-only --cap-drop=ALL \
  --security-opt=no-new-privileges --pids-limit=256 --memory=512m \
  -p 127.0.0.1:4318:4318 -p 127.0.0.1:8889:8889 \
  -e GENAI_SKETCH_SECRET -e SUMMARY_KEY_ID -e SUMMARY_DIRECTORY=/summaries \
  --mount "type=bind,src=$SUMMARY_DIRECTORY,dst=/summaries" \
  --mount "type=bind,src=$PWD/examples/integrations/litellm/collector.yaml,dst=/etc/collector.yaml,readonly" \
  --mount "type=bind,src=$PWD/examples/integrations/litellm/collector-container.yaml,dst=/etc/container.yaml,readonly" \
  "${IMAGE_REF:?verify the release image first}" \
  --config=/etc/collector.yaml --config=/etc/container.yaml
```

The override binds `0.0.0.0` only inside the container; do not change the published
addresses to `0.0.0.0`. Host LiteLLM still sends to `http://127.0.0.1:4318`.
If LiteLLM is also containerized, use the collector's service name on a private
Docker network instead of the proxy container's own loopback. Do not run both
host and container collectors on the same ports or summary directory.

Only bounded non-sensitive model labels are exported to Prometheus. The optional
application prompt-template annotation is keyed-hashed and never a metric label.
Leave raw-content capture disabled; counters still work without prompt keys.
The collector does not automatically sanitize a separate raw-trace pipeline.
Use the [shadow-mode recipes](../../../docs/SHADOW_MODE.md) to preserve an existing
destination, and the [deployment guide](../../../docs/DEPLOYMENT.md) for TLS,
authentication, resource limits, and private storage outside this local example.

## Compare Before And After

Allow complete collector windows on each side of the deployment, then copy one
window's unmodified summaries into each input directory. Windows follow arrival
time, not span event time: replaying old traces now is not historical backfill.
Do not edit timestamps or accounting IDs to make exports compatible.

With fleetdiff v0.2.0 or later:

```sh
fleetdiff investigate --before ./before --after ./after --expected app
```

The [single-app example](https://github.com/llm-measurement/fleetdiff/tree/main/examples/single-app)
also runs without LiteLLM, Docker, provider credentials, or model calls. It shows
200 -> 600 recorded tokens, 2 -> 3 model requests, and a missing-usage case that
refuses to explain the increase. It uses this collector's fixture exports.

## Add A Second Stack

A separately instrumented application can produce a second export. Use different
producer IDs but matching scope, keys, and extraction rules, and select the same
time windows. The default recipe uses producer `app`; set distinct IDs before
combining independently running collectors.

```sh
fleetdiff investigate --before ./before --after ./after --expected gateway,direct
```

The two producers must own disjoint model requests. Assign each inference to one
observer. The test partitions a single synthetic stream and verifies identical
counters and sketch bytes after merging. Follow the
[summary-exchange trust requirements](../../../docs/SUMMARY_EXCHANGE.md#limits-and-privacy).

## Reproduce The Checks

For real-provider accounting, use the [opt-in synthetic trial](provider-trial/README.md).
It includes the relay, fault injection, request generator, and reconciliation
command behind the GPT-5.4 table. Credentials come from your environment; raw
results stay private. The offline checks below do not make provider calls.

```sh
GOWORK=off go -C connector/genaisketchconnector test -run TestLiteLLMInvestigationFixtures -count=1
make dist
GOWORK=off go test -tags integration ./integration -run TestLiteLLMRecipeOTLPHTTP -count=1
```

## Compatibility Scope

The recipe uses LiteLLM's OpenTelemetry callback at
[revision 96c008f](https://github.com/BerriAI/litellm/tree/96c008f420a21b6561c51494352a68e49043af48),
with experimental GenAI conventions and generic OTLP. Live checks used LiteLLM
1.102.1. Test each new version, tracing mode, or vendor preset against your traffic.

Stock LiteLLM in this path turns absent provider usage into zeros or estimates.
The opt-in callback records what the provider sent for supported **non-streaming
OpenAI-compatible responses**, so absent usage is counted as missing. Streaming
origin remains unknown. See [source provenance](../../../docs/USAGE_PROVENANCE.md).

| Measurement | Required input | Notes |
|---|---|---|
| Model attempts | Supported `gen_ai.operation.name`, or model-attribute fallback | Includes failures and retries; wrappers are excluded |
| Input/output tokens | `gen_ai.usage.input_tokens`, `gen_ai.usage.output_tokens` | Use source provenance to distinguish provider counts from normalized output |
| Cache reads | `gen_ai.usage.cache_read.input_tokens` | Validated non-streaming subset of input |
| Reasoning tokens | `gen_ai.usage.reasoning.output_tokens` | Validated non-streaming subset of output |
| Prompt-template contributors | Optional `app.prompt.template` on model spans | Supply this application annotation |
| Distinct users | Configured user attribute on model spans | Check that your instrumentation supplies it; the fixture uses `enduser.id` |
| Session contributors | Collector v0.3.0 `topk_keys` and a configured session attribute on model spans | Extend the recipe with [session attribution](../../../docs/TOPK_KEYS.md); MCP session IDs alone do not link model usage |

The synthetic `before.json` and `after.json` fixtures exercise the receiver,
accounting, disjoint-partition merge, and privacy scans. Separate live mock and
real-provider runs are recorded in [Validation](VALIDATION.md), with versions,
results, and scope. Canonical attributes pass directly to the connector.
