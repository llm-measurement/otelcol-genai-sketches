# LiteLLM Compatibility Check

Date: 2026-09-26. Local Apple Silicon test, native Linux ARM64 containers.

This is a small reproducible compatibility experiment with synthetic responses,
not production certification or a statistical accuracy study.

## Versions And Method

- LiteLLM 1.102.1, image manifest
  `sha256:87f34979b9f8cb274fac90ca8a4fdda07d8480de22755562a26adeb95ce20d02`.
  Its upstream signature was verified before use.
- Collector 0.1.0, image
  `sha256:ef2d3d455682a403c24668b77466b3e35375e576e64e6a7b994d6c4a4c46a875`.
- OpenAI-compatible local mock, no external provider access or API credentials.
- One request per complete 15-second collector window, with an OTLP relay
  recording the actual spans before forwarding them unchanged to the collector.
- `callbacks: [otel]`, experimental GenAI semconv opt-in, message logging off,
  `NO_CONTENT`, non-streaming JSON and streaming SSE with `include_usage: true`.
- Successful streams were consumed through `[DONE]`. Interrupted streams,
  cancellations, retries, tool calls, and provider-specific implementations were
  not covered.

## Results

Each case produced exactly one counted model request, excluding proxy wrappers.
Collector input/output totals matched the integer attributes in the captured OTLP
model span. All six cases had a collector missing-usage count of **zero**.

| Response mode | What the mock provider sent | Collector input / output tokens |
|---|---|---|
| Non-streaming | Complete usage: 80 input, 20 output | 80 / 20 |
| Non-streaming | No usage object | 0 / 0 |
| Non-streaming | 80 input; output and total absent | 80 / 0 |
| Streaming | Complete usage: 80 input, 20 output | 80 / 20 |
| Streaming | No usage chunk | 18 / 3 |
| Streaming | 80 input; output and total absent | 80 / 3 |

The 18 / 3 numbers describe this synthetic request, not a general conversion
factor or an accuracy claim. They were supplied by LiteLLM, not reported by the
mock provider. The responses returned to the client already contained the zeros
or inferred counts, so this information loss precedes the collector.

Captured model spans lacked `gen_ai.operation.name` despite the opt-in. The
collector counted them through its documented `gen_ai.request.model` fallback.
This test does not establish correct operation classification for embeddings,
tools, retrieval, or all LiteLLM routes. The synthetic OTLP fixtures in this
directory explicitly include operation names and exercise a different input case.

## What This Means

- Complete reported usage is transported correctly for the two tested response
  modes. This supports a local before/after investigation of emitted counts.
- Collector `reported` observations mean a numeric attribute reached the
  collector, not that its original source was the provider.
- Missing-usage counters detect missing or invalid collector inputs. They cannot
  recover fields a gateway has replaced with plausible numbers.
- Fleetdiff can compare those emitted numbers, but cannot certify provider usage
  completeness, invoice accuracy, or savings. Its provenance warning still applies
  when observed windows and fields are complete.
- Do not reinterpret all zeros as missing: a provider may legitimately report
  zero. Reliable source accounting requires explicit provenance or preservation
  of missingness before normalization.

For example, comparing the complete non-streaming window with the no-usage
window produced 100 -> 0 emitted tokens and 1 -> 1 requests in fleetdiff, with
complete observed field coverage. That is a reporting change, not evidence of
100 tokens saved. No summary-only calculation can recover the missing source fact.

## Privacy Scope

The synthetic prompt sentinel was absent from captured OTLP, collector summaries,
Prometheus metrics and labels, and stack logs. The capture showed that LiteLLM
still emits other request and identity metadata with content capture disabled.
`NO_CONTENT` is not a general metadata sanitizer. Independently review any raw
trace destination; the connector's bounded metrics do not sanitize another path.
No prompt-template annotation was supplied, so this run did not exercise a
populated prompt top-k snapshot. The separate sentinel fixtures test that surface.

## Opt-In Callback Check

A second run on the same date used `provenance_callback.callback` in place of
`otel` and the collector rebuilt from this source, using Collector Builder
v0.161.0. The Linux ARM64 binary ran in the same restricted container. All six
cases again produced exactly one counted model span per complete window.

| Mode | Provider usage | Input / output provenance | Missing requests |
|---|---|---|---|
| Non-streaming | Complete | provider_reported / provider_reported | 0 |
| Non-streaming | Absent | unavailable / unavailable | 1 |
| Non-streaming | Input only | provider_reported / unavailable | 1 |
| Streaming | Complete | unknown / unknown | 0 |
| Streaming | Absent | unknown / unknown | 0 |
| Streaming | Input only | unknown / unknown | 0 |

Counts remained 80/20, 0/0, and 80/0 for non-streaming and 80/20, 18/3, and 80/3
for streaming. The difference is that non-streaming absent fields are now marked
missing, not reported zeros. Streaming numeric presence still does not establish
provider coverage. All eight versioned provenance counters reconciled with the
captured model spans; metrics, summaries, captured OTLP, and stack logs passed the
prompt-sentinel scan. No populated prompt top-k was claimed for this live run.

The old and rebuilt collector exports had identical base accounting fingerprints.
Current-source fleetdiff compared an old complete window with a new complete
window without rewriting either file, with provenance `cannot_determine` because
the old window had no source evidence. Comparing the new complete and no-usage
windows returned `cannot_determine` for the volume explanation, with missing
coverage visible. This is not a savings claim.

Ten offline Python tests cover helper behavior, request isolation, retry reset,
unsupported paths, version pinning, and absence of retained response content.
Collector race tests and the receiver integration suite passed, including fixed
provenance labels, unavailable-as-missing, genuine zero, token-weight exclusion,
and privacy checks. Fleetdiff race tests cover old/new compatibility and unknown
answers. These are compatibility checks, not production certification.

## Next Checks

Test interrupted streaming, retries through the live proxy, and a real provider
separately. Ask LiteLLM maintainers for a supported streaming source/estimator
boundary. Do not generalize this successful-completion matrix to other paths.
