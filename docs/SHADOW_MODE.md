# Keep Your Existing Telemetry Backend

Shadow mode sends the same OTLP trace batch to two consumers:

1. your current trace backend; and
2. `genaisketch`, which derives bounded metrics and structured top-k snapshots.

You do not need to replace Datadog, Langfuse, Grafana Alloy, or another
OpenTelemetry destination to try the connector.

## Choose Where To Copy Traces

### Let This Distribution Copy Them

```text
applications -> otelcol-genai-sketches -> current OTLP backend
                                      -> bounded metrics and snapshots
```

Use this layout when applications can point at this distribution and the current
backend accepts OTLP. The distribution includes stable OTLP gRPC and HTTP exporters.

For an OTLP gRPC backend:

```bash
export GENAI_SKETCH_SECRET="$(openssl rand -hex 32)"
export EXISTING_OTLP_GRPC_ENDPOINT="collector.example.net:4317"
export EXISTING_OTLP_GRPC_INSECURE="false"
./dist/otelcol-genai-sketches \
  --config=examples/shadow-mode/collector-grpc.yaml
```

For an OTLP HTTP backend:

```bash
export GENAI_SKETCH_SECRET="$(openssl rand -hex 32)"
export EXISTING_OTLP_HTTP_ENDPOINT="https://collector.example.net:4318"
export EXISTING_OTLP_AUTHORIZATION="Bearer replace-with-a-secret"
./dist/otelcol-genai-sketches \
  --config=examples/shadow-mode/collector-http.yaml
```

The HTTP exporter appends `/v1/traces` to the base endpoint. Keep credentials in
your normal secret manager rather than a checked-in environment file. These examples
bind OTLP and Prometheus to loopback. Change the listeners only when applications run
in another container or pod, and restrict access at that network boundary.

### Let Your Existing Collector Copy Them

```text
applications -> existing Collector or Alloy -> current backend
                                           -> otelcol-genai-sketches sidecar
```

This layout changes the existing path less. Run the sketch distribution with
[`sketches-only.yaml`](../examples/shadow-mode/sketches-only.yaml), then add a second
OTLP exporter to the trace pipeline that already feeds your backend.

The complete upstream Collector example is
[`upstream-collector.yaml`](../examples/shadow-mode/upstream-collector.yaml). Its
important line is:

```yaml
exporters: [otlp_grpc/existing, otlp_grpc/genai_sketches]
```

Keep your current exporter and processors. Add only the exporter that points to the
sketch sidecar. Do not create a path from the sidecar back to the upstream collector;
that would create a trace loop.

## Existing OpenTelemetry Collector

Use the existing-collector layout above when your current distribution has an OTLP
exporter. The sidecar listens on `127.0.0.1:14317` and publishes Prometheus metrics
on `127.0.0.1:18889` in the example.

If the two collectors run in separate containers, change the sidecar receiver to a
container-reachable address and restrict that port with the container network or a
network policy. Do not expose an unauthenticated OTLP receiver to the public network.

## Datadog

Keep the Datadog Agent, DDOT Collector, or Datadog exporter that you already use.
Add the sketch sidecar as a second OTLP destination in the collector or Alloy layer
that currently sends traces to Datadog. This preserves Datadog-specific enrichment
and avoids depending on Datadog's direct OTLP intake preview.

The repository does not ship or configure the Datadog exporter. The tested part of
this path is the OTLP copy to the sketch sidecar. Datadog documents its supported
ingestion choices in [Datadog Agent](https://docs.datadoghq.com/opentelemetry/setup/agent/).

## Langfuse

If Langfuse already receives traces from a Collector, keep that exporter and add the
sketch sidecar as the second destination.

This distribution can also own the fan-out. Langfuse accepts OTLP over HTTP, uses
Basic authentication, and requires the ingestion-version header for real-time v4
ingestion. The supplied configuration includes both headers:

```bash
export GENAI_SKETCH_SECRET="$(openssl rand -hex 32)"
export LANGFUSE_OTLP_ENDPOINT="https://cloud.langfuse.com/api/public/otel"
export LANGFUSE_AUTH_STRING="$(printf '%s' \
  "$LANGFUSE_PUBLIC_KEY:$LANGFUSE_SECRET_KEY" | base64 | tr -d '\n')"
./dist/otelcol-genai-sketches \
  --config=examples/shadow-mode/langfuse.yaml
```

Choose the endpoint for your Langfuse region or self-hosted instance. The config
shape and HTTP headers are exercised locally in integration tests. A Langfuse account
and its credentials are still required to verify ingestion in your project. See
[Langfuse's OpenTelemetry guide](https://langfuse.com/integrations/native/opentelemetry).

## Grafana Alloy

[`alloy-sidecar.alloy`](../examples/shadow-mode/alloy-sidecar.alloy) shows the two
changes:

1. add an `otelcol.exporter.otlp` component pointing at the sketch sidecar; and
2. add that component to the trace outputs of your existing batch processor.

Replace the sample `existing` exporter with the exporter and authentication already
used by your Alloy deployment. Keep Alloy and the sidecar in the same network
boundary. Grafana documents the component syntax in
[`otelcol.exporter.otlp`](https://grafana.com/docs/alloy/latest/reference/components/otelcol/otelcol.exporter.otlp/).

## What Is Copied

| Surface | Contents | Boundary |
| --- | --- | --- |
| Existing trace backend | The original OTLP trace content | May contain prompts and identifiers if instrumentation captured them |
| Prometheus metrics | Counters, token totals, distinct estimates, and configured slice labels | Slice values are cleartext and must be bounded and non-sensitive |
| Top-k snapshots | Keyed hashes, weights, and lower and upper bounds | Pseudonymous and linkable while the same secret is used |
| Connector state | Bounded counters and sketches | Raw hashed-field values do not enter aggregate state |

The connector does not redact the trace copy sent to your existing backend. If raw
prompts or identifiers must not leave the application boundary, disable content
capture at instrumentation or redact before the fan-out. Hashing inside
`genaisketch` does not sanitize another exporter's copy.

## What CI Verifies

The integration suite sends one trace through each built-in forwarding path and
asserts that:

- OTLP gRPC and OTLP HTTP both deliver the original trace fields;
- the connector derives the expected Prometheus request metric from the same batch;
- raw user and prompt sentinels stay out of every Prometheus metric and label; and
- HTTP authentication and Langfuse's ingestion-version header reach the receiver.

The static gRPC, HTTP, Langfuse, sketches-only, and upstream-collector YAML files are
also validated by the generated distribution. CI cannot validate a user's vendor
account, credentials, network policy, or data-region choice.

## Troubleshooting

Validate a configuration without starting listeners:

```bash
make validate-shadow-configs
```

If the existing backend stops receiving traces:

- check the configured protocol, endpoint, TLS mode, and authentication;
- check exporter failure and queue metrics in the Collector's own telemetry; and
- confirm that an HTTP endpoint is a base URL to which `/v1/traces` may be appended.

If the dashboard is empty:

- confirm that applications send traces to this pipeline rather than the old port;
- check that model-call spans contain `gen_ai.operation.name` or
  `gen_ai.request.model`;
- check that the operation appears in `operation_filter.llm_operations`; and
- scrape the Prometheus exporter directly before debugging Grafana.

If token totals are empty but request counts move, inspect
`gen_ai_sketch_missing_token_usage_total`. Token fields are optional and missing
usage is not treated as zero.

Shadow mode adds network and processing load. Exercise backend outages and monitor
export failures before using the pattern on production traffic.
