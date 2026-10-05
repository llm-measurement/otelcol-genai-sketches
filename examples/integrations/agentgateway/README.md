# Who Used The Tokens? Which Session Took Over?

One user goes from **1,200 to 10,920 tokens**. One session goes from **10% to 91%
of model attempts**. The application total stays at 12,000 tokens per window, so
a totals chart alone would miss the change.

This recipe sends synthetic requests through **agentgateway v1.6.0**, then uses
**collector v0.3.0** and **fleetdiff v0.5.0** to find the change:

`fleetdiff investigate`, opening lines:

<!-- investigate-output -->
```text
1 of 10 tracked sessions flagged for review: 91.00% of attributed model attempts.
Reported tokens: 12000 -> 12000; model attempts: 100 -> 100.
```

`fleetdiff scan`, findings for the changed window:

<!-- scan-output -->
```text
  session-1 now holds 91% of model attempts (newly prominent in this scan).
  user-1 now holds 91% of tokens (newly prominent in this scan).
```

Users rank by reported tokens; sessions rank by model attempts, including attempts
without usage. A custom workflow ranking appears in the collector's snapshot logs.
The same recipe also runs through [standalone Envoy AI Gateway](../envoy-ai-gateway/README.md).

## Run It

From the repository root, with Docker Compose, Python 3.10+, OpenSSL, curl and
[GitHub CLI](https://cli.github.com/) installed. Install the released fleetdiff
binary using its checksum-pinned installer; the installer verifies the release
attestation and archive checksum:

```sh
mkdir -p .cache
curl --fail --location --proto '=https' --tlsv1.2 \
  https://raw.githubusercontent.com/llm-measurement/fleetdiff/v0.5.0/scripts/install.sh \
  -o .cache/install-fleetdiff.sh
printf '%s\n' 'f561e95b7599485f32c2d07f1a5288a4c836ca4b9a816938991ba7e08b24a70e  .cache/install-fleetdiff.sh' | shasum -a 256 -c -
sh .cache/install-fleetdiff.sh v0.5.0 .cache/fleetdiff-recipe
python3 -B examples/integrations/agentgateway/check.py \
  --fleetdiff .cache/fleetdiff-recipe/fleetdiff \
  --output .cache/agentgateway-recipe
```

Allow about three minutes after the images download: the test collects eight real
20-second windows. No provider account or key is needed. Images are pinned by
digest, no host ports are opened, and the test removes its containers and temporary
signing keys when it finishes. Use a new output directory on each run.

Read the saved reports, or rerun the released commands yourself:

```sh
.cache/fleetdiff-recipe/fleetdiff investigate \
  --before .cache/agentgateway-recipe/before.json \
  --after .cache/agentgateway-recipe/after.json --expected app
.cache/fleetdiff-recipe/fleetdiff scan .cache/agentgateway-recipe/archive \
  --expected app --baseline 6 --as-of "$(cat .cache/agentgateway-recipe/as-of.txt)"
```

`scan` returns **3** for this planted change. The quiet control returns **0**.
The saved reference time makes the scan reproducible after the demo finishes.

## Use It With Your Gateway

Keep your current trace destination. The recipe fans out the same spans:

```text
agentgateway -> collector -> existing trace backend
                         -> metrics, snapshot logs and summary files
```

[gateway.yaml](gateway.yaml) obtains `user.id` from the verified JWT subject,
not from a caller's user header. JWT authentication is strict; the test rejects
missing and tampered credentials and attempts to spoof the user header. Replace
the synthetic issuer, audience and JWKS with your own authentication settings.
The access-log configuration removes the raw `jwt.sub` field.

These mappings are checked against both the
[v1.6.0 tracing implementation](https://github.com/agentgateway/agentgateway/blob/v1.6.0/crates/agentgateway/src/telemetry/trc.rs)
and captured spans:

| Request source | Span attribute | Ranking |
| --- | --- | --- |
| Verified `jwt.sub` | `user.id` | User tokens |
| `agent-session-id` header | `session.id` | Session model attempts |
| `x-workflow` header, prefixed with `workflow:` | `app.workflow` | Workflow tokens in snapshot logs |

Use your application's stable session and workflow identifiers. The device and
MCP-resource mappings in the example are privacy test inputs; omit them when they
are not needed. Content capture remains off.

[collector.yaml](collector.yaml) hashes the mapped fields and requests these rankings:

```yaml
topk_keys:
  - {field: user_key, weight: tokens}
  - {field: session_key, weight: requests}
  - {field: workflow_key, weight: tokens}
```

Keep identity attributes out of `slices`. This test's **17 metric series stay the
same as the number of active identities rises from one to ten**; series depend on
the slice configuration, not the ranked identities. See the
[metric cardinality contract](../../../docs/METRICS.md).

Point `otlp/existing` at your own destination and follow the
[shadow-mode configuration](../../../docs/SHADOW_MODE.md) for TLS and production
export queues. Use a persistent secret from your secret manager, and archive
summary files before their retention expires, as described in
[summary exchange](../../../docs/SUMMARY_EXCHANGE.md) and
[fleetdiff scan](https://github.com/llm-measurement/fleetdiff/blob/v0.5.0/docs/SCAN.md).

## Workflow Rankings In Loki

Forward the collector's JSON stdout to your existing Loki destination with the
fixed stream label `service_name="otelcol-genai-sketches"`. Keep hashes in the log
body, never as stream labels. Paste [workflows.logql](workflows.logql) into Grafana
Explore:

```logql
# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
{service_name="otelcol-genai-sketches"}
| json
| msg="genaisketch topk snapshot"
| line_format `{{.payload_json}}`
| json slices="slices"
| line_format `{{range $s := fromJson .slices}}{{if eq $s.field "workflow_key"}}window={{$s.window_start_unix_nano}} total={{$s.total_weight}} {{range $i := $s.items}}rank={{$i.rank}} hash={{$i.hash}} tokens={{$i.estimate}} bounds=[{{$i.lower_bound}},{{$i.upper_bound}}] {{end}}{{end}}{{end}}`
|~ "hash="
```

The leading workflow has `tokens=10920 bounds=[10920,10920]` in the changed window.
The test loads actual collector snapshots into isolated **Loki 3.7.8**, runs this
query, and checks that the only stored label is the fixed service name.

## Helm Mapping

[chart-values.yaml](chart-values.yaml) applies the same field mappings and shadow
destination to released **chart 0.3.1**, using the collector v0.3.0 image digest:

```sh
helm template rankings oci://ghcr.io/llm-measurement/charts/otelcol-genai-sketches \
  --version 0.3.1 -f examples/integrations/agentgateway/chart-values.yaml
```

Set up the hashing secret and receiver security using the
[deployment guide](../../../docs/DEPLOYMENT.md) before deploying.

## What The Test Checks

The `gateway-rankings` CI matrix runs both released gateways without credentials.
Each run checks 800 model spans, exact counters, the preserved shadow copy, top-N
membership and bounds in populated snapshots for all three fields in all eight
windows, user/session investigation, an unusual scan, and a quiet control. It also
checks label names and unchanged metric-series identities.

User, session, workflow, device, MCP-resource and prompt sentinels are checked
against metrics, all container logs, summary files and decoded sketch state,
fleetdiff text/JSON, and Loki results and labels. Positive controls confirm that
the scanner detects each sentinel and the mapped identities reached the gateway's
real spans. **`backend.jsonl` is the private raw shadow copy and intentionally
contains the synthetic identities. Do not share it as a sanitized report.**

## Limits

This is a synthetic functional test, not a load or billing study. Hashes are
pseudonymous; the existing trace backend keeps its own data policy. Workflow values
use the existing `user:v1` hash domain with a `workflow:` prefix in their own field,
not a newly registered domain. Session and workflow headers describe activity;
they are not authorization decisions. **fleetdiff v0.5.0 does not read custom keys
yet; `workflow_key` is shown in snapshot logs only.** The Helm values are
render-tested, not deployed to Kubernetes; chart 0.3.1 configures the rankings but
does not expose summary-file export or JSON log encoding. The Docker configuration
supplies both. The Loki test verifies
the query and labels by replaying logs, not a production log-shipping deployment.
