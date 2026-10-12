# Read OpenInference And OpenLLMetry Traces

Turn vendor-shaped model spans into token totals and keyed user/session rankings,
while keeping the original spans on your existing backend branch. This synthetic
check sends eight spans through released collectors and expects:

```text
5 model attempts; 307 input + 56 output tokens; 2 missing-usage attempts.
Original spans preserved; identities absent from metrics and snapshot logs.
```

## Run It

Requires Python 3.10+, Docker and Docker Compose. No model API key or paid calls.

```sh
git clone https://github.com/llm-measurement/otelcol-genai-sketches.git
cd otelcol-genai-sketches
python3 examples/integrations/genai-normalizer/check.py
```

The check uses an isolated Docker network with no published ports. It generates a
fresh hash secret, verifies the totals and original-span branch, scans metrics and
snapshot logs for planted identities, a code snippet and a file path, and removes
its containers and named volume. Synthetic evidence is copied to a new temporary
directory. The raw and normalized trace evidence deliberately contains sentinels.

## Put It In Your Pipeline

```text
                         -> existing backend (original spans)
OTLP receiver -> fan-out
                         -> gen_ai_normalizer -> genaisketch -> metrics / rankings
```

Use the processor name `gen_ai_normalizer` and this configuration on the sketch
branch in a contrib collector:

```yaml
processors:
  gen_ai_normalizer:
    sources:
      - name: openinference
      - name: openllmetry
```

[`normalizer.yaml`](normalizer.yaml) shows the two pipelines and OTLP forwarding.
For real traffic, replace the test-only `file/raw` exporter with your existing
backend, and remove `file/normalized`. Keep identity attributes off metric labels,
protect the collector-to-collector connection with your normal TLS/authentication,
and take the sketch branch [before sampling](../../../docs/SHADOW_MODE.md#before-sampling).

[`collector.yaml`](collector.yaml) accepts `chat` model operations only. The check
also sends tool, workflow and embedding spans to verify they do not inflate model
attempts. Review your operation allowlist before extending it: the normalizer maps
OpenInference `PROMPT` spans to `text_completion`, so enabling that operation
without checking your instrumentation can count prompt-building work as a call.

| Input | Pinned built-in mapping | Checked behavior |
| --- | --- | --- |
| OpenInference | `LLM` to `chat`; `llm.model_name`; prompt/completion token counts | Input/output totals; `session.id` copied to `gen_ai.conversation.id` |
| OpenLLMetry | `llm.request.type`; `llm.request.model`; prompt/completion usage | Input/output totals; `session.id` also mapped by the preceding OpenInference source |
| Existing standard attributes | Default `overwrite: false` | Standard token counts win over conflicting vendor counts |
| Missing or malformed usage | Missing remains missing | Two attempts reported with missing token usage |

Every configured source processes every span, in order; `name` selects a mapping
table, not an instrumentation-scope filter. With these two sources enabled, the
OpenInference `session.id` mapping applies to both dialects. Missing identities
stay missing. Use this combined configuration only where the shared attributes
have the same meaning, or separate source pipelines when they differ.

## Versions And Distribution Choice

The [Compose file](compose.yaml) pins multi-platform image digests for contrib
**v0.162.0** and this collector **v0.3.2**. The normalizer is **alpha, traces only**.
Its [mapping source](https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/ae8c507510f48f433ab47dd1c6b01a59d6c388b5/processor/genainormalizerprocessor)
is pinned to the matching contrib release.

**Recommendation: use the upstream processor on the sketch branch first.** This
recipe leaves `builder.yaml` and the released distribution unchanged. A future
custom-build option is
`github.com/open-telemetry/opentelemetry-collector-contrib/processor/genainormalizerprocessor v0.162.0`;
adding it to the distribution should be a separately reviewed dependency change.

## Limits

The test checks synthetic attribute mappings through the released images, not a
live Phoenix or Traceloop SDK deployment. Normalization renames span attributes;
it does not invent missing users, sessions, cache details or usage provenance.
The default mappings preserve vendor attributes and may set the sketch branch's
scope schema URL; only the separate original branch stays unchanged. Confirm
your source's input-token semantics before mapping provider-native cache fields:
renaming alone does not turn cache-exclusive input into total input.
