# Check Your LiteLLM Token Accounting

Run synthetic requests through a pinned LiteLLM proxy and this collector, then
compare provider usage with exported spans, metrics, summaries, and Fleetdiff.
The suite covers complete responses, failures and retries, interrupted streams,
cache reads, reasoning tokens, and an inert tool call with a follow-up.

This starts an isolated test stack. It does not connect to or certify your
existing production proxy. Use the results to review the same callback and
accounting settings in your own deployment.

## Run Locally

Requirements: Python 3.10+, Docker with Compose v2.6+ and Linux containers, and
[Fleetdiff v0.2.0](https://github.com/llm-measurement/fleetdiff/releases/tag/v0.2.0)
on your PATH. The original run used native ARM64 containers on an M4 Mac.

Set `OPENAI_API_KEY` in your shell environment using your secret manager or a
private environment file **outside the checkout**. Use a dedicated key with access
to `gpt-5.4-2026-03-05`. Do not put the key in the command, Compose file, or output
directory. Then, from the repository root:

```sh
python3 examples/integrations/litellm/provider-trial/run.py \
  --run-paid-trial --max-estimated-usd 2
```

The amount is your chosen limit, not a required spend. The historical run's
usage-price estimate was about two cents, **not a guaranteed price**. Review the
[model rates](https://developers.openai.com/api/docs/models/gpt-5.4) and the recorded
rates in `manifest.json` before running. The local guard uses those rates and
conservative reservations for unknown work; it is not a provider-enforced billing
cap. The suite also limits upstream attempts and completion sizes. Do not reset
the ledger to bypass a limit. No paid job runs in CI.

Allow roughly 10-15 minutes: cases wait for complete arrival-time windows, even
when generation is fast. The runner prints progress, always attempts to stop its
own Compose project on exit, and retains failures as well as successes. It never
reuses an existing output directory. An optional `--fleetdiff PATH` selects the
pinned binary; `--output .cache/my-trial` selects a new private directory.

No ports are published to the host. Only the relay has provider network access
and receives the key through its environment. The proxy has a dummy
credential; the collector uses a public synthetic hashing secret unsuitable for
customer traffic. Containers run without root or Linux capabilities. As with
other local containers, anyone controlling the Docker daemon can inspect the
container environment. Do not share `docker inspect` or expanded Compose output.
The key is never written into the source or evidence files.

## Read The Result

Each run writes to a new ignored `.cache/provider-trial-*` directory:

- `source-hashes.json`: source hashes, pinned versions, platform, chosen limit,
  Fleetdiff binary hash, and the exact Compose project name.
- `analysis.json` and `verification.json`: accounting and privacy-check results.
- `table.md`: the observed per-case numeric table, produced after reconciliation.
- `results/`, `evidence/`, `summaries/`, `fleetdiff/`: private raw evidence and
  unchanged collector windows. Raw OTLP can contain provider request metadata.

**Do not upload the evidence directory.** Review even the numeric table before
sharing. The secret and synthetic prompt sentinel are scanned across captured
OTLP, metrics and labels, summaries, stack logs, and Fleetdiff reports. The live
trial does not populate prompt top-k; the receiver integration tests cover that
separate surface. No generic raw-trace sanitization is claimed.

`verify.py` reconciles each observed model span with exact collector counters,
cumulative Prometheus samples, all eight provenance counters, and Fleetdiff.
It checks nonzero cache/reasoning subsets without adding them to parent totals.
Missing usage must keep the volume answer undetermined. The generated table uses
the current run's counts, not hardcoded expected model outputs.

## What Repeats

`manifest.json`, `client.py`, `litellm.yaml`, and `compose.yaml` pin the model,
synthetic request shapes, retry settings, faults, and images behind the
[GPT-5.4 result table](../VALIDATION.md#gpt-54-snapshot-check). This is the
sanitized version of that experiment, with environment-based credentials,
portable paths, explicit opt-in, and tighter request/response limits. The relay
now also sends `store: false`. Historical captures are not public fixtures.

Repeated runs check **accounting relations**, not identical generated lengths,
reasoning counts, latency, or cache hits. A cache miss or zero reasoning count
does not pass a nonzero-subset check. A slow request spanning several windows
cannot be relabeled into one window; the comparison check reports that limit.
The runner stops on unexpected statuses instead of spending more automatically.

- The 429 is injected before forwarding, not an organic provider rate limit.
- The timeout obtains a real completion, then withholds it beyond the proxy
  timeout. A retry can incur additional provider work whose usage never arrives.
- The cut stream closes downstream but drains upstream for independent usage
  evidence. It is not a provider-cancellation experiment.
- Streaming source provenance stays unknown. This covers Chat Completions, not
  Responses, Anthropic, arbitrary LiteLLM versions, or concurrent production load.
- Usage-price arithmetic is not invoice reconciliation or proof of savings.

## Free Checks

```sh
python3 -m unittest discover \
  -s examples/integrations/litellm/provider-trial -p 'test_*.py' -v
python3 -m unittest discover \
  -s examples/integrations/litellm -p 'test_*.py' -v
```

These use local loopback sockets and mocked provider transport. They need no key
and make no paid calls. CI runs both commands. They check the relay and callback,
not current provider availability. This is a compatibility experiment, not a
production certification or a general-purpose gateway.
