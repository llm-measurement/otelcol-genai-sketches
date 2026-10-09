# Run The Grafana Demo

See request rate, reported tokens, missing usage, and token-heavy prompt
signatures from synthetic GenAI traffic. The demo starts a sample application,
the collector, a Prometheus metrics server, and a provisioned Grafana dashboard.

## Start

Requirements: Docker with Compose v2, Git, and a POSIX shell (macOS, Linux, or WSL).
No host Go, Python, Make, or OpenSSL installation is needed.
From a checkout of this repository:

```sh
sh examples/demo.sh up
```

The first run downloads pinned images and compiles the checked-out collector
inside Docker. Allow several minutes; later runs reuse the build cache.
`make example-up` is an equivalent convenience command.

The sample emits model, agent, tool, and retrieval spans. It includes missing
token fields and enough prompt variety to exercise bounded estimates.
No model account or API key is required.

## Ports And Secrets

Published ports bind only to localhost:

| Service | Default address | Override |
| --- | --- | --- |
| Grafana | [GenAI Sketches dashboard](http://localhost:3000/d/genai-sketches) | `GENAI_DEMO_GRAFANA_PORT` |
| Prometheus | [http://localhost:9090](http://localhost:9090) | `GENAI_DEMO_PROMETHEUS_PORT` |
| Collector metrics | [http://localhost:8889/metrics](http://localhost:8889/metrics) | `GENAI_DEMO_METRICS_PORT` |
| OTLP gRPC | `localhost:4317` | `GENAI_DEMO_OTLP_PORT` |

Set the corresponding environment variable before starting if a port is occupied.

The script generates a random demo secret without displaying it. Each `up`
creates a fresh secret unless `GENAI_SKETCH_SECRET` is already set. Restarting
with a new secret resets pseudonymous comparability. Use the same protected
secret when you need comparisons across restarts; keep it out of source control.
See [Configuration](../docs/CONFIGURATION.md) for the hashing requirements.

## Read The Dashboard

Let the example run for at least one minute, then use the dashboard in this order:

1. Compare **Requests/sec** with **Reported Token Rate**.
2. Check **Reported Tokens / Request** to separate traffic growth from larger
   requests or responses.
3. Check **Missing Token Usage** before treating token totals as complete.
4. Compare model and team/model slices to localize the change.

Then inspect token-heavy prompt signatures:

```sh
sh examples/demo.sh topk
```

The snapshot contains keyed hashes, estimates, and lower and upper bounds.
Prompt text stays out of it, and the hashes stay out of Prometheus labels.
The default is prompt ranking; [Top-K Keys](../docs/TOPK_KEYS.md) explains how to
enable user or session rankings. Distinct-user counts cover the current window.

## Run The Investigations

```sh
sh examples/demo.sh investigate
```

The command reconciles known synthetic request and token counts, then shows:

- [More tool spans, the same model requests](../docs/investigations/TOOL_SPANS.md).
- [Fewer reported tokens, unchanged synthetic consumption](../docs/investigations/MISSING_USAGE.md).

It makes no model or provider calls. Watch the
[90-second walkthrough](../docs/media/README.md), then use the
[token-consumption playbook](../docs/TOKEN_USAGE.md) for the PromQL queries and
interpretation table. The [metrics reference](../docs/METRICS.md) defines each
signal; [collector/config.yaml](collector/config.yaml) is the demo configuration.

## Stop

```sh
sh examples/demo.sh down
```

This removes the demo containers and their disposable data. Use
`sh examples/demo.sh ps` to check services and `sh examples/demo.sh logs` for
collector logs while diagnosing startup problems.

For a persistent environment, follow [Deployment](../docs/DEPLOYMENT.md) to
verify and install the production image or chart. Return to the
[guide table](../README.md#find-your-guide) for source-specific recipes.
