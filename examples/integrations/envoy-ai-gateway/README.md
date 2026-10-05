# Find The User And Session Behind A Usage Change

On **Envoy AI Gateway v1.1.0**, one user grows from **1,200 to 10,920 tokens** and
one session reaches **91% of model attempts**, while total tokens stay unchanged.
Released fleetdiff v0.5.0 identifies both from the collector's summary files.
The quiet control raises no flags.

This uses the gateway's **standalone `aigw run`** image. No Kubernetes cluster or
provider account is needed.

## Run It

Install fleetdiff v0.5.0 with the verified installer in the
[agentgateway recipe](../agentgateway/README.md#run-it), then run from the repository root:

```sh
python3 -B examples/integrations/agentgateway/check.py \
  --gateway envoy-ai-gateway \
  --fleetdiff .cache/fleetdiff-recipe/fleetdiff \
  --output .cache/envoy-ai-gateway-recipe
```

The eight 20-second windows take about three minutes after the images download.
The shared test checks the same rankings, exact accounting, shadow fan-out,
unchanged metric series and extended sentinel scans as the agentgateway recipe.
It also runs the workflow LogQL query against Loki. Containers are removed after
the run; reports remain in the private output directory.

```sh
.cache/fleetdiff-recipe/fleetdiff investigate \
  --before .cache/envoy-ai-gateway-recipe/before.json \
  --after .cache/envoy-ai-gateway-recipe/after.json --expected app
.cache/fleetdiff-recipe/fleetdiff scan .cache/envoy-ai-gateway-recipe/archive \
  --expected app --baseline 6 --as-of "$(cat .cache/envoy-ai-gateway-recipe/as-of.txt)"
```

The planted scan returns **3**, with both a user and a session at 91%.

## Mapping Your Traffic

[compose.yaml](compose.yaml) changes only the gateway service in the shared recipe.
Its settings come from the released
[standalone implementation](https://github.com/envoyproxy/ai-gateway/blob/v1.1.0/cmd/aigw/run.go)
and [tracing documentation](https://github.com/envoyproxy/ai-gateway/blob/v1.1.0/site/versioned_docs/version-1.1/capabilities/observability/tracing.md).
The test verifies the resulting attributes on actual model spans:

```yaml
AI_GATEWAY_TRACING_SEMCONV: gen_ai
OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT: 'false'
OTEL_AIGW_REQUEST_HEADER_ATTRIBUTES: ''
OTEL_AIGW_SPAN_REQUEST_HEADER_ATTRIBUTES: 'x-user-id:user.id,agent-session-id:session.id,x-workflow:app.workflow'
OTEL_AIGW_METRICS_REQUEST_HEADER_ATTRIBUTES: ''
OTEL_AIGW_LOG_REQUEST_HEADER_ATTRIBUTES: ''
```

Use the **span-only** mapping. A metrics-header mapping would turn identities into
metric labels. The base mapping applies across signals, so it is empty here.
The test also maps device and MCP-resource sentinels to spans to check their absence
from derived outputs. It leaves gateway metric and log exporters off; your existing
telemetry configuration can retain its other signals without identity mappings.

The shared collector maps `user.id` to `user_key`, `session.id` to `session_key`,
and `app.workflow` to `workflow_key`. Send workflow values with the `workflow:`
prefix used by this recipe. Users rank by tokens, sessions by attempts, and
workflows appear in the [snapshot LogQL query](../agentgateway/README.md#workflow-rankings-in-loki).
The same collector forwards the original spans to the existing backend.

## Limits

The synthetic client supplies the user header in this isolated test. For real
traffic, an authenticated trusted edge must remove caller-supplied identity headers
and set the verified value; this recipe does not authenticate Envoy users. Treat
session and workflow values as application metadata. The
[shared recipe's limits](../agentgateway/README.md#limits) apply, including its
private raw shadow copy, released fleetdiff support and render-only Helm mapping.
