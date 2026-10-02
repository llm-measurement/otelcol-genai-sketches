# LiteLLM Compatibility Check

Date: 2026-09-26. Local Apple Silicon test, native Linux ARM64 containers.
Real-provider and regression follow-up: 2026-09-27.

Across six mock-provider cases and 11 real GPT-5.4 cases, collector counters
matched the model spans LiteLLM emitted. The opt-in callback preserved validated
non-streaming cache and reasoning subsets. The tables show where emitted usage
differed from the provider's counts.

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
The synthetic OTLP fixtures in this directory explicitly include operation names
and exercise that input path separately.

## What This Means

- Complete reported usage is transported correctly for the two tested response
  modes. This supports a local before/after investigation of emitted counts.
- Collector `reported` observations mean a numeric attribute reached the
  collector, not that its original source was the provider.
- Missing-usage counters detect missing or invalid collector inputs. They cannot
  recover fields a gateway has replaced with plausible numbers.
- Fleetdiff compares the emitted numbers and shows their declared origin alongside
  numeric field coverage.
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
coverage visible.

Ten offline Python tests cover helper behavior, request isolation, retry reset,
unsupported paths, version pinning, and absence of retained response content.
Collector race tests and the receiver integration suite passed, including fixed
provenance labels, unavailable-as-missing, genuine zero, token-weight exclusion,
and privacy checks. Fleetdiff race tests cover old/new compatibility and unknown
answers.

## Real OpenAI Follow-Up

On 2026-09-27, real OpenAI Chat Completions were tested through LiteLLM 1.102.1
and the opt-in callback, using collector v0.2.0:
`sha256:a990c08f29dda3b33b11771fb87788a5c4ba2a2ea3dbf991e94876c5d274800b`.
Fleetdiff v0.2.0 read unchanged summary exports. Containers ran native Linux
ARM64 on an M4 Mac.

Earlier trials with pinned GPT-4.1 mini and o4-mini snapshots exposed missing
cache-read and reasoning mappings. The callback was extended to preserve valid
non-streaming subsets, then the suite was repeated. Their numeric tables and
billing reconciliation remain in the private experiment record: the public
runner below pins GPT-5.4, not those older experiments.

The findings were consistent across the tested models:

- A failed attempt followed by a successful retry produced separate model
  spans. Both were counted, with the failed attempt missing usage.
- A completion whose response was withheld could incur provider cost without
  its usage reaching the collector. This is a controlled fault, not an
  observation of organic provider throttling or timeouts.
- An interrupted stream could return inferred counts and a normal-looking
  completion marker. Its source provenance remained unknown.
- Validated cache-read and reasoning subsets reached metrics and summaries
  without being added to their parent totals again.

The prompt sentinel and API key were absent from retained OTLP, metrics and
labels, summaries, and stack logs. Raw OTLP still carries provider metadata
and stays private. The live trial did not populate prompt top-k; the separate
receiver integration test checks that surface.

## GPT-5.4 Snapshot Check

On 2026-09-27, the same 11-case real-provider suite was repeated with
`gpt-5.4-2026-03-05`, using the same LiteLLM image, callback source, released
collector, and Fleetdiff binary. No additional product-code changes were needed.
Requests explicitly selected the default service tier and `reasoning_effort:
none`, except the reasoning case, which used `low`. Output limits remained 128
tokens for ordinary calls, 256 for streams, and 2048 for reasoning. This checks
Chat Completions, not the Responses API.

The [public trial harness](provider-trial/README.md) contains the relay, faults,
synthetic request generator, pinned images/model, and table-generation checks.
With `OPENAI_API_KEY` set privately in the environment and Fleetdiff v0.2.0 on
PATH, run from the repository root:

```sh
python3 examples/integrations/litellm/provider-trial/run.py \
  --run-paid-trial --max-estimated-usd 2
```

This spends real money. The limit is caller-selected estimated USD, not a
provider-enforced cap. See the harness README before running. `verify.py`
produces `table.md` from the current run's observations; the table below is the
historical result, not a promise of identical outputs or cache hits.

| Case | Provider input / output | Collector input / output | Model attempts | Missing |
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
in LiteLLM's interrupted-stream estimate. The test verifies that the report
preserves those accounting differences and marks unknown origin.

The GPT-5.4 usage-price estimate was USD 0.0204815 at the recorded standard rates:
USD 2.50 input, USD 0.25 cached input, and USD 15 output per million tokens.
This run has not yet been reconciled with a fresh
OpenAI cost export. [Model and pricing](https://developers.openai.com/api/docs/models/gpt-5.4).

Reproducibility records include the exact model snapshot, container digests,
callback/configuration hashes, synthetic request generator, reasoning settings,
retry policy, fault settings, and unchanged collector windows. All pinned source
hashes stayed unchanged during the run. Repetition checks accounting against
each run's observed usage; it does not require identical generated lengths,
reasoning counts, latency, or cache hits. A nonzero cache/reasoning subset must
actually occur before its check can pass. The harness source and offline tests
are public; raw captures remain private. CI runs the offline checks.

## Limits

These are small, sequential compatibility checks on the pinned versions, not
production certification or a statistical accuracy study. Numeric field presence
and completion markers do not prove complete provider usage. Usage-price estimates
are not invoices; see [accounting and reconciliation](../../../docs/ACCOUNTING.md#attribution-and-reconciliation).
The injected faults cover relay-controlled failures, not organic rate limiting or
provider-side cancellation. Streaming provenance remains unknown. Raw traces
still need independent metadata review, and the paid suite runs only by opt-in.

## Next Checks

Ask LiteLLM maintainers for a supported streaming source/estimator boundary and
explicit incomplete-stream evidence. Test Anthropic's different cache accounting
and the Responses API separately. Provider-side cancellation and production
concurrency also need separate checks; the results above do not establish them.
