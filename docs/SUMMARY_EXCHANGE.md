# Combine Measurements Across Independently Operated Systems

Two teams keep their own traces and still get one shared, mergeable measurement.
Each collector exports HLL++ and frequent-items state plus window counters for an
agreed scope. A sketch-aware reader combines those files.

This opt-in feature is available from collector `0.1.0`, using `llm-sketchkit
0.2.0`. Use the [published image](DEPLOYMENT.md) or build the tagged source with
`make dist`.

## Configure Each Producer

Agree on a scope, a hashing key and its version identifier, accounting rules,
window duration, and a list of participating producers. Each producer must own
a disjoint stream of observations. Two collectors must not count the same request.
The same user or prompt can occur in both streams and be deduplicated by HLL++.

Create a private directory owned by the collector's operating-system user:

```sh
install -d -m 700 ./private-summaries
```

Add this block under the existing `connectors.genaisketch` configuration:

```yaml
summary_export:
  directory: ./private-summaries
  producer_id: platform
  scope_id: customer-support
  key_id: support-key-v1
  interval: 5s
```

The other team uses its own directory and `producer_id: data`. A producer ID must
be unique and stable across restarts. Each directory has one writer. Use a
writable private volume with suitable ownership in a container. The directory
must exist, must not be a symlink, and must not grant group or other permissions.
Export is disabled unless configured. `retention_windows` must be at least two;
the export interval must be between one second and one minute, and no longer
than a window.

The scope is set by the operator, not taken from span attributes. It covers all
relevant input seen by that connector instance. It is independent of metric
slices: label overflow and slice eviction cannot erase exported scope totals.
Use separately routed connector instances and separate keys/scopes for tenant
isolation and permitted linkage.

## Read And Combine

Transfer the files using an authenticated, access-controlled mechanism of your
choice. Keep permissions private at the destination. No hashing secret needs to
leave the producing systems for combination to work.

The llm-sketchkit repository includes a
[Go/Python API and local file example](https://github.com/llm-measurement/llm-sketchkit/tree/main/examples/summary-exchange).
For example, from that checkout after `python -m pip install llm-sketchkit==0.2.0`:

```sh
python examples/summary-exchange/combine.py \
  --expected platform data \
  --window-start 120000000000 \
  -- exports/platform/*.json exports/data/*.json
```

Use a `window_start_unix_nano` from the actual files. Results include counters,
distinct estimates, tracked heavy items and their bounds, selected source epochs,
missing producers, and partial observation intervals. Producer names must come
from a trusted inventory, not from whatever files happen to arrive.

Compare different windows by combining each separately. The accounting identifier
includes a fingerprint of extraction sources, operation filtering, MCP, weighting,
and deduplication configuration. Sketch parameters are checked inside each payload.
Incompatible scopes, rules, keys, durations, or payload metadata are rejected.

## Compare Before And After

[fleetdiff](https://github.com/llm-measurement/fleetdiff) compares these exports
locally without a hashing secret or raw traces. After installing it, put all
expected producers' files for one completed window in `before/` and a later window
in `after/`, then run:

```sh
fleetdiff compare --before ./before --after ./after --expected platform,data
```

Use the producer IDs agreed above. The report includes request and reported-token
changes, token coverage, distinct estimates, and tracked-item change bounds. A
missing producer is an error unless explicitly allowed as a partial comparison;
it is not treated as zero usage. The same compatibility and disjoint-observation
requirements apply. Summary files still require authorized sharing.

Try the [two-collector demo](https://github.com/llm-measurement/fleetdiff#run-it-through-real-collectors)
and its [example files](https://github.com/llm-measurement/fleetdiff/tree/main/examples/two-operators),
or follow the [two-operator trial checklist](https://github.com/llm-measurement/fleetdiff/blob/main/docs/TWO_OPERATOR_TRIAL.md)
with approved exports, then use the report to select a trace investigation.

## Files, Restarts, And Coverage

- Files use `<window-start>-<random-process-epoch>.json`. A newer cumulative
  snapshot atomically replaces the same epoch/window file. File permissions are
  `0600`; existing sketch protobuf bytes are unchanged inside the JSON envelope.
- A restarted process gets a new epoch. Its files do not overwrite the preceding
  epoch. Combine the latest snapshot of each epoch; repeated snapshots do not add
  counters or sketch update counts again. Conflicting sequences are errors.
- Counters are per-window, not the process-cumulative values exposed to Prometheus.
  Completed and current windows follow the [accounting rules](ACCOUNTING.md#token-fields).
- Idle producers continue exporting empty windows. Observation intervals describe
  the collector's running time, not proof that upstream instrumentation, delivery,
  or sampling was complete. Gaps and late starts are reported as partial coverage.
- Every export expires files outside the configured retention, including files
  left by older epochs. Copy needed files before expiration. Persistent output
  is capped at 512 files and 64 MiB; one temporary file can use up to another 8 MiB
  during replacement. Write, size, and clock errors are logged explicitly.
- A successful orderly shutdown attempts a final export. A crash can lose updates
  since the last successful export. This is not crash-durable or exactly-once
  request accounting. Check file freshness; stale files must not look current.

## Limits And Privacy

Only configured HLL++ and frequent-items measurements are exported. `topk: 0`
disables all frequent-items state. MCP measurements require `mcp.enabled`.
The internal Bloom deduplication filter is not exported. Its suppression and
missing-key counters are included; suppression remains approximate when enabled.

From v0.3.0, optional `topk_keys` add `top_users` and `top_sessions`
token-weighted measurements alongside default `top_prompts`. Request-weighted
measurements use a separate `_requests` suffix, for example
`top_sessions_requests`. The snapshot selector remains `field: session_key`,
with `weight: requests`; it is not the summary measurement name.
Use matching key selection, extraction rules, and units across producers and
comparison windows. Changing the configured ranking is a measurement-contract
change, not evidence that traffic changed. Released v0.2.0 images do not support
this option. See [Top-K Keys](TOPK_KEYS.md) for extraction-contract compatibility.

Prompts, user identifiers, document identifiers, and MCP resource URIs remain
keyed hashes inside sketch state. Scope, producer, accounting, and key-version
identifiers are cleartext, operator-chosen metadata and must be non-sensitive.
The files are pseudonymous, not anonymous; they are not signed or encrypted by
this component. Matching metadata declares compatibility; it does not authenticate
senders or prove equal secrets. File-based combination provides neither cross-tenant
access control nor automatic fleet discovery, and does not deduplicate overlapping
spans. Only exported sketch state can be merged; Prometheus estimates and top-k
log rows cannot replace it. For interpreting changes, see
[Token Usage](TOKEN_USAGE.md#limits).

The integration test `TestIndependentCollectorsExportMergeablePrivateSummaries`
runs two collector processes, sends OTLP traffic, combines exported files, verifies
counters and shared identity estimates, and scans metrics, logs, JSON, and decoded
sketch state for sentinels. Unit tests cover restart, replay, retention, label
overflow, malformed files, incompatible metadata, and non-trivial weighted bounds.
The release workflow runs the same integration test against two published
containers on each supported architecture after verifying anonymous image access.
The `public-install` workflow can also verify an existing release without
rebuilding images or moving tags.
