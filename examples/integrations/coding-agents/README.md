# Find The Coding Session Behind A Usage Change

See one session take 91% of model attempts while total tokens stay flat. Compare
the windows with fleetdiff, then query device rankings in snapshot logs. Your
existing trace backend still receives the original spans.

The synthetic example runs Copilot CLI and Copilot Chat mappings through released
collector **v0.3.0**, then fleetdiff **v0.5.0**. No agent account or provider key is
needed. The images are pinned by digest; nothing is built from local product code.

`investigate`:

<!-- investigate-output -->
```text
1 of 10 tracked sessions flagged for review: 91.00% of attributed model attempts.
Reported tokens: 12000 -> 12000; model attempts: 100 -> 100.
```

`scan`:

<!-- scan-output -->
```text
1 unusual windows among 1 checked.
```

## Run It

Requirements: Docker Compose, Python 3.10+, `curl`, `shasum`, and the
[GitHub CLI](https://cli.github.com/). From this repository's root, install
fleetdiff v0.5.0 with checksum and attestation verification, then run the example:

```sh
mkdir -p .cache
curl --fail --location --proto '=https' --tlsv1.2 \
  https://raw.githubusercontent.com/llm-measurement/fleetdiff/v0.5.0/scripts/install.sh \
  -o .cache/install-fleetdiff.sh
printf '%s\n' 'f561e95b7599485f32c2d07f1a5288a4c836ca4b9a816938991ba7e08b24a70e  .cache/install-fleetdiff.sh' | shasum -a 256 -c -
sh .cache/install-fleetdiff.sh v0.5.0 .cache/fleetdiff-recipe
python3 -B examples/integrations/coding-agents/run.py \
  --fleetdiff .cache/fleetdiff-recipe/fleetdiff \
  --output .cache/coding-agent-recipe
```

Measured run time: **3 minutes 11 seconds** on an M4 Mac with images already
downloaded. It runs eight 20-second windows per source, checks a quiet control,
and removes its containers. Reports and summary
archives remain under `.cache/coding-agent-recipe/cli/` and `chat/`.
The run directory must be new; use another path to repeat the test.

To read the saved CLI windows yourself:

```sh
cat .cache/coding-agent-recipe/cli/investigate.txt
cat .cache/coding-agent-recipe/cli/scan.txt
.cache/fleetdiff-recipe/fleetdiff scan .cache/coding-agent-recipe/cli/archive --expected app --baseline 6 \
  --as-of "$(cat .cache/coding-agent-recipe/cli/as-of.txt)"
```

The last command returns **3** for the planted change. The same commands work for
the `chat` directory. The test compares each README excerpt with the saved output.

## Source Settings And Mappings

These are separate input paths; choose the config for the actual source. The
fixture shapes and source links are in [SOURCES.md](SOURCES.md).

| Source pin | Model usage | Selected session | Device |
| --- | --- | --- | --- |
| Copilot CLI v1.0.92 | `gen_ai.usage.input_tokens` / `output_tokens` | Span `gen_ai.conversation.id` | Resource `device.id` |
| Copilot Chat v0.43.0 | Same GenAI usage fields | Span `copilot_chat.session_id`, then resource `session.id` | Resource `device.id` |

Copilot Chat's resource fallback groups a VS Code window. Its span session field
groups a conversation when available. This config deliberately excludes
`gen_ai.conversation.id`: in the pinned version that field holds a request ID.
The test exercises both selection paths, including conflicting fields, with shared,
varying and absent `service.instance.id` values.

For Copilot CLI, point telemetry at your existing receiver and keep content off:

```sh
export COPILOT_OTEL_ENABLED=true
export COPILOT_OTEL_EXPORTER_TYPE=otlp-http
export OTEL_EXPORTER_OTLP_ENDPOINT=https://your-existing-collector.example
export OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
export OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT=false
```

For Copilot Chat, use the local-extension settings and ensure no environment
override enables content capture:

```json
{
  "github.copilot.chat.otel.enabled": true,
  "github.copilot.chat.otel.exporterType": "otlp-http",
  "github.copilot.chat.otel.otlpEndpoint": "https://your-existing-collector.example",
  "github.copilot.chat.otel.captureContent": false
}
```

An operator may add `device.id` to `OTEL_RESOURCE_ATTRIBUTES`, preserving existing
attributes. Treat it as an identifier, not a metric label. No built-in device ID
is assumed, and a device is not a person.

Follow the [shadow-mode guide](../../../docs/SHADOW_MODE.md) to keep your backend
and forward a second copy to the sketch collector. Merge [collector.yaml](collector.yaml)
with either [copilot-cli.yaml](copilot-cli.yaml) or [copilot-chat.yaml](copilot-chat.yaml).
Replace the demo backend endpoint and export directory for your deployment; use
the [deployment guide](../../../docs/DEPLOYMENT.md) for authentication and TLS.
The Compose example publishes no host ports and runs only on its private network.

## Device Rankings In Loki

`device_key` reads **resource** `device.id` only. It is a token-weighted custom
field, not `user_key`. On collector v0.3.0 it uses the registered `user:v1` hashing
domain; its measurement name and extraction contract still identify it as a device.

This uses Recipe A's snapshot-query pattern. The test pushes actual snapshot log
lines to local Loki and runs this exact query from [devices.logql](devices.logql):

```logql
# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
{service_name="otelcol-genai-sketches",source="copilot-cli"}
| json
| msg="genaisketch topk snapshot"
| line_format `{{.payload_json}}`
| json slices="slices"
| line_format `{{range $s := fromJson .slices}}{{if eq $s.field "device_key"}}window={{$s.window_start_unix_nano}} total={{$s.total_weight}} {{range $i := $s.items}}rank={{$i.rank}} hash={{$i.hash}} tokens={{$i.estimate}} bounds=[{{$i.lower_bound}},{{$i.upper_bound}}] {{end}}{{end}}{{end}}`
|~ "hash="
```

Change the selector to `source="copilot-chat"` for Chat. The planted device has
10,920 tokens, with bounds `[10920,10920]`, in the final window. Loki stores only
two fixed stream labels: `service_name` and `source`; identities and hashes stay
in the log body. Test results are saved as `loki-query.json` and `loki-series.json`.

## Limits

- This is synthetic, source-shaped end-to-end testing, not a live coding-agent capture.
- fleetdiff v0.5.0 reads session identity from span attributes only in `inspect`
  (user identity already supports resource fallback); this recipe instead uses
  collector-produced summaries with `investigate` and `scan`.
- fleetdiff v0.5.0 does not read custom device keys; use the snapshot query above.
- Content capture remains off. The test deliberately injects a code snippet, file
  path and identities as privacy probes. The original shadow copy contains them;
  never publish `.cache/` captures. All derived surfaces must pass sentinel scans.
- This recipe covers Copilot CLI and Copilot Chat. Codex CLI, Claude Code and
  Cursor need changes first; [SOURCES.md](SOURCES.md) has the details.

Questions or feedback: [open an issue](https://github.com/llm-measurement/otelcol-genai-sketches/issues).
