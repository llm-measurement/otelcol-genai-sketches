# Check Gateway Usage Accounting

Check what your gateway records when a request doesn't end cleanly. Run the same
requests through LiteLLM, Bifrost, agentgateway, and Portkey, then inspect what
reaches the client, local gateway records, and exported traces.

This folder contains the harness, not a gateway ranking. Results stay in private
run directories for review before publication.

## Free Test

Requirements: Python 3.10 or later, Docker, and Docker Compose v2. No provider key
or Python packages are needed. Run from the repository root:

```sh
python3 -B -m unittest discover -s examples/gateway-accounting -p 'test_*.py'
RUN=".cache/gateway-study-$(date -u +%Y%m%dT%H%M%SZ)-$$"
python3 -B examples/gateway-accounting/run.py --output "$RUN"
python3 -B examples/gateway-accounting/verify.py "$RUN"
```

The runner creates a fresh private directory, runs the cases, and writes
`analysis.json` and `table.md` there. `verify.py` checks that every selected case
ran, readers worked, and normal-response controls captured usable usage. It does
not freeze fault behavior: a gateway fixing a fault should not break this test.

Use `--gateways litellm` for one gateway or `--cases complete stream_standard stream_complete`
for controls only. Controls run before faults. The default observation wait is
8 seconds after each request; `--settle-seconds 30` extends it. A missing record
means it was not observed by that deadline. Repeat with a longer wait where the
gateway documents delayed settlement.

The `gateway-accounting` CI job runs the free matrix without credentials. It does
not upload captures. A local pass is not a claim that hosted CI has run.

## What Is Pinned

[images.json](images.json) records exact image digests for LiteLLM 1.104.0,
Bifrost 2.2.5, agentgateway 1.6.0, Portkey 1.15.2, Python, and the capture collector.
These are study versions, not promises of current/latest releases. Configurations
are in [gateways.py](gateways.py). The capture collector only forwards OTLP; it
does not run the sketch connector or change token counters.

By default requests use an **OpenAI Chat Completions client**. `--protocols openai`
uses an OpenAI-format upstream; `--protocols anthropic` tests translation from an
Anthropic Messages-format upstream. Add `--client-protocol anthropic` to test a
native Messages client against an Anthropic upstream. These are separate runs
with their client protocol recorded in the manifest and report.

The cases are:

| Case | Input to the gateway |
|---|---|
| `complete` | Completed response with usage |
| `stream_complete` | Completed OpenAI stream; separate Anthropic-compatible deferred-usage probe |
| `stream_standard` | Completed stream; standard Anthropic input-at-start/output-at-end |
| `stream_cut` | Stream ends before final usage |
| `client_disconnect` | Client closes mid-stream |
| `missing` | Completed response omits usage |
| `stream_missing` | Completed stream omits final usage |
| `retry429` | First attempt returns 429, subsequent attempt succeeds |
| `zero`, `cache_only` | Explicit numeric boundary controls |
| `partial_input`, `partial_output` | Deliberately malformed, one-sided usage |

A partial-usage rejection can be correct behavior. Preserve valid reported zeros
separately from provisional zero placeholders.

The deferred Anthropic-compatible fixture credits danielzarioiu's
[issue #3739](https://github.com/agentgateway/agentgateway/issues/3739) and
[proposed fix #3740](https://github.com/agentgateway/agentgateway/pull/3740).
It is a separate profile from normal Claude streaming, not a claim about all
Anthropic upstreams.

## Four Separate Surfaces

The report keeps provider evidence, client responses, gateway records, and OTLP
spans separate. Input, output, and cache-read counts each retain missing, zero,
partial, or ambiguous states. Parent and child spans are not added together;
multiple independent records stay separate when the correct aggregation is unknown.

| Gateway | Local record used | OTLP configured here |
|---|---|---|
| LiteLLM | Supported `standard_logging_object` callback | Yes |
| Bifrost | Read-only SQLite `logs` rows | No |
| agentgateway | Structured request logs | Yes |
| Portkey | Local `/log/stream` records | No |

An unconfigured exporter is a harness limitation, not a product limitation.
Portkey stream log placeholders may contain no usage. Bifrost uses a local,
synthetic price catalog so startup works offline; its monetary fields are not
cost measurements. None of these readers is an enforced-budget ledger.

The LiteLLM callback adapter captures cache counts only from the known numeric
fields in `standard_logging_object.metadata.usage_object`. Matching aliases count
once; conflicting aliases remain unknown and are listed in `cache_conflicts`.
Older captures without `cache_fields` are marked `not_captured`, not retroactively
treated as missing provider data. The adapter never copies the surrounding metadata.
Provider-specific cache extensions in OpenAI-format responses stay visible, but
the analyzer does not silently add them to `prompt_tokens` to repair a mismatch.

The analysis separately records configured mock counts and usage actually written
by the mock provider. Live reference counts come from provider response metadata
before fault injection. Origin stays unknown unless explicitly declared.

## Optional Live OpenAI Pass

The opt-in relay uses the existing [provider-trial](../integrations/litellm/provider-trial/)
validation and spend guards. It sends only the fixed synthetic prompt to
`api.openai.com`, using the pinned `gpt-5.4-2026-03-05` model. Each selected gateway
gets at most 12 provider attempts, each capped at 512 output tokens. The total
estimated-spend guard is divided across gateways; unknown usage keeps its full
reservation. This estimate is not a provider-enforced billing cap.

Keep the credential in a file outside the checkout. It may contain only the key
or an `OPENAI_API_KEY=...` assignment; the file is parsed, never executed.
Use a dedicated project/key with no unrelated traffic, and revoke it afterwards.

```sh
RUN=".cache/gateway-live-$(date -u +%Y%m%dT%H%M%SZ)-$$"
python3 -B examples/gateway-accounting/run.py --live \
  --key-file "$HOME/.config/gateway-study/openai-key" \
  --max-estimated-usd 10 --protocols openai \
  --cases complete stream_complete stream_cut client_disconnect missing stream_missing retry429 \
  --cut-mode drain --output "$RUN"
python3 -B examples/gateway-accounting/verify.py "$RUN"
```

For `stream_cut`, `--cut-mode drain` closes the gateway-facing stream but keeps
reading the provider response, so final provider metadata can be compared with
lost downstream evidence. A separate run with `--cut-mode cancel` closes the
upstream connection too. Client disconnects propagate whenever the relay observes
the gateway closing its connection. These are distinct experiments.

A canceled request may have no final metadata. Its eventual provider usage and
billing remain unknown without an independent provider usage report. Do not
substitute a drained response or a matching completed request for that evidence.
Enforced-budget tests are not implemented here.

## Optional Live Anthropic Pass

The Anthropic relay uses the same isolated capture pipeline. It sends only fixed
synthetic requests to `api.anthropic.com/v1/messages`, using `claude-sonnet-4-6`
and API version `2023-06-01`. Its separate price table includes uncached input,
five-minute cache writes, cache reads, and output. Prices and model availability
are documented on [Anthropic's model page](https://platform.claude.com/docs/en/models/sonnet-4-6/overview).

```sh
RUN=".cache/gateway-claude-$(date -u +%Y%m%dT%H%M%SZ)-$$"
python3 -B examples/gateway-accounting/run.py --live \
  --key-file "$HOME/.config/gateway-study/anthropic-key" \
  --max-estimated-usd 4 --protocols anthropic --client-protocol openai \
  --cases complete stream_standard stream_complete stream_cut client_disconnect missing stream_missing retry429 cache_write cache_read \
  --cut-mode drain --output "$RUN"
python3 -B examples/gateway-accounting/verify.py "$RUN"
```

Repeat in a new directory with `--client-protocol anthropic` to exercise native
Messages requests. The real-provider `stream_complete` and `stream_standard`
cases both use Anthropic's actual event sequence; the mock deferred-usage profile
does not apply to live calls. Use a separate cancel-mode run for `stream_cut`.

`cache_write` and `cache_read` run consecutively with identical synthetic content
and a fresh per-pair nonce. Verification requires provider evidence of a write
and a subsequent read; naming a case does not prove a cache hit. Each request is
text-only with at most 512 output tokens, and each gateway has at most 12 forwards.
The local estimated-spend guard includes the more expensive cache-write rate;
partial or unknown final usage retains its reservation.

The agentgateway OpenAI-format request uses its supported
`prompt_cache_breakpoint: {"mode": "explicit"}` text-part extension, which the
[pinned translator](https://github.com/agentgateway/agentgateway/blob/v1.6.0/crates/llm/src/conversion/messages.rs)
maps to Anthropic's cache hint. Other adapters use `cache_control`. The relay
accepts unchanged synthetic text split across adjacent user messages, but never
adds a missing cache hint on a gateway's behalf.

Anthropic reports early input/cache counts and provisional output counts in
`message_start`, then cumulative updates. The report keeps finality separate from
numeric presence, so an early output value is not treated as a final total after
cancellation. The relay records cache-write and cache-read fields separately;
neither is added twice to total input.

## Isolation And Reproduction

Mock containers run on an internal network with no host ports or provider keys.
Live mode gives **only the relay** a read-only credential mount and outbound
network. Gateway containers still have dummy keys and no external network.
The relay's credential is sent only to the fixed HTTPS provider endpoint;
redirects and environment proxies are disabled.

Containers have read-only root filesystems, dropped capabilities and resource
limits. Run folders are private (`0700`, `umask 077`) and must stay under ignored
`.cache/`. Raw gateway diagnostics may contain synthetic prompts and dummy keys;
do not publish or attach raw folders. The analysis uses allowlisted fields.

Manifests record image digests, cases, deadlines, machine, protocol, and source
hashes. Stop editing harness files while a retained run is underway. Changed-source
runs are development evidence only. Failed setup/controls are recorded separately
and excluded from fault comparisons. Cleanup removes only that run's containers.

## Reading The Results

Use the harness to check a documented failure or a problem observed in your
deployment. The free run uses mock counts, not tokenization of the test text.
Fault cases test behavior, not failure frequency or production impact. Live runs
use provider-reported usage as the reference, not an invoice; capturing a value
upstream does not prove a gateway received it. A mismatch alone does not prove
estimation, significant cost loss, or a budget bypass.

Review each surface and offer maintainers a chance to correct configuration or
interpretation before publishing observations. Share working settings and fixes
per gateway, not a ranking. Investigate possible security implications privately.
