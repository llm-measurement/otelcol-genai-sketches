# LiteLLM Compatibility Check

Date: 2026-09-26. Local Apple Silicon test, native Linux ARM64 containers.
Real-provider and regression follow-up: 2026-09-27.

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

## Real OpenAI Follow-Up

On 2026-09-27, a second experiment used real OpenAI Chat Completions through
LiteLLM 1.102.1, the updated opt-in callback in this directory, and the released
collector v0.2.0 image:
`sha256:a990c08f29dda3b33b11771fb87788a5c4ba2a2ea3dbf991e94876c5d274800b`.
Fleetdiff v0.2.0 read the resulting summary files without changing their contents
or timestamps. The local machine was an M4 Mac; containers ran native Linux ARM64.

The model snapshots were `gpt-4.1-mini-2025-04-14` and `o4-mini-2025-04-16`.
Prompts were synthetic; the tool example executed only an inert local function.
A relay retained numeric provider usage, captured OTLP, and forwarded telemetry
unchanged. Cases ran sequentially in complete 30-second arrival-time windows.
This was a compatibility check, not a load test or invoice reconciliation.

The first run exposed two missing mappings: nonzero cached-input and reasoning
counts appeared in provider and client responses but not in model spans. The
callback was then extended to preserve validated subsets. The entire 11-case
suite was rerun after that change, making 12 real upstream calls. Both runs are
retained in private evidence; the following table describes the post-fix run.

| Case | Provider calls | Collector requests | Provider input / output | Collector input / output | Missing requests |
|---|---:|---:|---|---|---:|
| Baseline | 1 | 1 | 25 / 1 | 25 / 1 | 0 |
| Injected 429, retries off | 0 | 1 | none | 0 / 0 | 1 |
| Withheld response, retries off | 1 | 1 | 25 / 1 | 0 / 0 | 1 |
| Complete stream | 1 | 1 | 34 / 256 | 34 / 256 | 0 |
| Stream cut before usage | 1 | 1 | 34 / 256 | 34 / 1 | 0 |
| Cache warm-up | 1 | 1 | 3269 / 1 | 3269 / 1 | 0 |
| Cache hit | 1 | 1 | 3269 / 1 | 3269 / 1 | 0 |
| Reasoning | 1 | 1 | 40 / 147 | 40 / 147 | 0 |
| Tool call and follow-up | 2 | 2 | 141 / 2 | 141 / 2 | 0 |
| Injected 429, one retry | 1 | 2 | 25 / 1 | 25 / 1 | 1 |
| Withheld response, one retry | 2 | 2 | 50 / 2 | 25 / 1 | 1 |

The cache-hit case preserved **3072 cached tokens within 3269 input tokens**.
The reasoning case preserved **128 reasoning tokens within 147 output tokens**.
Both subsets matched from the provider response through the model span,
Prometheus metrics, collector summary, and Fleetdiff report. Neither was added
to its parent total again. Non-streaming provenance remained provider-reported.

OpenAI usage and cost exports subsequently matched the combined provider ledger
from both runs exactly: 24 requests, 13824 input tokens (including 6144 cached),
1272 output tokens, and USD 0.0064216. This is reconciliation of provider usage
and aggregate exported cost, not a claim that the collector recovered the
unreported work described below or that a per-request invoice was checked.

All 14 observed model spans reconciled with collector request counters. Input,
output, missing-usage, and subset counters matched captured model spans and
cumulative Prometheus metrics. All eight provenance counters matched emitted
declarations. This establishes accounting of received telemetry, not recovery of
unreported provider work.

### Faults And Remaining Limits

- The 429 was injected before forwarding to OpenAI. The timeout relay obtained
  the real completion, then withheld the response for 14 seconds, beyond the
  configured eight-second timeout. These are controlled transport faults, not
  observations of organic provider throttling or server timeouts.
- A failed attempt followed by a successful retry produced two model spans and
  two counted requests, one missing usage. The timeout retry paid for two real
  completions but delivered only one completion's usage to the collector.
- For the interrupted stream, the relay closed its downstream connection after
  three chunks but drained the upstream stream to record full provider usage.
  This tests downstream loss, not provider-side cancellation. LiteLLM returned
  a normal-looking completion marker and estimated output usage of 1 rather
  than the provider's 256. Provenance stayed unknown; Fleetdiff returned
  `cannot_determine` for provider coverage. Numeric presence is not completeness.
- Fleetdiff returned `cannot_determine` for the timeout volume explanation.
  It could compare the emitted stream counts, but did not certify their origin.
- This callback still does not establish streaming provenance. Anthropic,
  other model snapshots, production concurrency, and invoice totals were not
  tested. Do not extend these results to those paths.

### Regression And Privacy Checks

The six-case local mock matrix above was rerun against the same updated callback
and released collector. Every count, missing-usage result, and provenance state
matched the opt-in results. All three mock streams completed.

Fourteen Python tests passed. Eight subset fixtures cover nonzero, zero, absent,
invalid, changed-parent, and conflicting values. Receiver integration sends the
actual callback output into the collector, checking totals, subset metrics,
summaries, and a populated top-k snapshot. Only the LiteLLM parent logger is
stubbed in that offline fixture. The full collector integration suite, collector
and Fleetdiff race tests, and their vet checks passed.

The prompt sentinel and API key were absent from retained real-trial OTLP,
metrics and labels, summaries, and stack logs. No prompt-template annotation was
supplied in the live trial, so its top-k surface was not populated; the separate
receiver integration test covers that case. Raw OTLP still carries provider
request metadata and stays private. This is not general raw-trace sanitization.

## GPT-5.4 Snapshot Check

On 2026-09-27, the same 11-case real-provider suite was repeated with
`gpt-5.4-2026-03-05`, using the same LiteLLM image, callback source, released
collector, and Fleetdiff binary. No additional product-code changes were needed.
Requests explicitly selected the default service tier and `reasoning_effort:
none`, except the reasoning case, which used `low`. Output limits remained 128
tokens for ordinary calls, 256 for streams, and 2048 for reasoning. This checks
Chat Completions, not the Responses API.

| Case | Provider input / output | Collector input / output | Collector requests | Missing |
|---|---|---|---:|---:|
| Baseline | 24 / 4 | 24 / 4 | 1 | 0 |
| Injected 429, retries off | none | 0 / 0 | 1 | 1 |
| Withheld response, retries off | 24 / 4 | 0 / 0 | 1 | 1 |
| Complete stream | 33 / 256 | 33 / 256 | 1 | 0 |
| Stream cut before usage | 33 / 256 | 34 / 1 | 1 | 0 |
| Cache warm-up | 3268 / 4 | 3268 / 4 | 1 | 0 |
| Cache hit | 3268 / 4 | 3268 / 4 | 1 | 0 |
| Reasoning | 40 / 54 | 40 / 54 | 1 | 0 |
| Tool call and follow-up | 299 / 17 | 299 / 17 | 2 | 0 |
| Injected 429, one retry | 24 / 4 | 24 / 4 | 2 | 1 |
| Withheld response, one retry | 48 / 8 | 24 / 4 | 2 | 1 |

The cache hit preserved **2816 cached tokens within 3268 input tokens**. The
reasoning case preserved **44 reasoning tokens within 54 output tokens**. Both
subsets reached Prometheus, summaries, and Fleetdiff unchanged, without adding
them to their parent totals. The same request/provenance reconciliation and
secret/prompt-sentinel scans passed across all cases. Fleetdiff correctly left
timeout volume and timeout/interrupted-stream provider coverage undetermined.

There were 12 real provider calls with 7061 input and 611 output tokens. The
collector counted 14 model attempts, 7014 input and 348 output tokens, with four
missing-usage requests. The two injected 429s did not reach the provider. The
input difference includes both unreported timeout work and a one-token overcount
in LiteLLM's interrupted-stream estimate. This successful test verifies the
documented accounting and uncertainty, not complete recovery of provider work.

The GPT-5.4 usage-price estimate was USD 0.0204815 at the recorded standard rates:
USD 2.50 input, USD 0.25 cached input, and USD 15 output per million tokens.
Unlike the earlier runs above, this run has not yet been reconciled with a fresh
OpenAI cost export. [Model and pricing](https://developers.openai.com/api/docs/models/gpt-5.4).

Reproducibility records include the exact model snapshot, container digests,
callback/configuration hashes, synthetic request generator, reasoning settings,
retry policy, fault settings, and unchanged collector windows. All pinned source
hashes stayed unchanged during the run. Repetition checks accounting against
each run's observed usage; it does not require identical generated lengths,
reasoning counts, latency, or cache hits. A nonzero cache/reasoning subset must
actually occur before its check can pass. The private harness is not a public CI
job, and this small sequential trial is not enterprise load certification.

## Next Checks

Ask LiteLLM maintainers for a supported streaming source/estimator boundary and
explicit incomplete-stream evidence. Test Anthropic's different cache accounting
and the Responses API separately. Provider-side cancellation and production
concurrency also need separate checks; the results above do not establish them.
