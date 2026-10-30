# Upgrading And Rollback

For chart 0.7.0, see the [NetworkPolicy migration](DEPLOYMENT.md#chart-070-network-policy-migration)
before upgrading: the default changes to deny unlisted traffic.

The distribution currently follows pre-1.0 semantic versioning. Read release notes
and the [accounting contract](ACCOUNTING.md) for every upgrade. A change to request
scope, missing-usage meaning, alias precedence, token arithmetic, deduplication, or
attribution requires a new accounting-contract version.

## Before An Upgrade

1. Record the current image digest, chart version, values, and hash-secret version.
2. Verify the new image signature, attestation, and digest as described in
   [Deployment](DEPLOYMENT.md).
3. Render and inspect the new configuration:

   ```bash
   helm lint deploy/helm/otelcol-genai-sketches
   helm template genai-sketches deploy/helm/otelcol-genai-sketches \
     --namespace observability --values your-values.yaml > rendered.yaml
   ```

4. Run the image's configuration validation against any custom collector file.
5. Compare reconciliation fixtures and metric names before changing alerts or
   recording rules.

## Rollout Behavior

The chart uses `Recreate` because aggregation state is local to one process. An
upgrade therefore creates a short ingestion interruption and resets cumulative
counters and current sketch windows. Prometheus `rate()` handles a counter reset,
but alerts should tolerate the restart interval. Use upstream buffering or a durable
OTLP tier when uninterrupted receipt is required.

The chart does not persist sketch state. A rollback starts empty state under the old
binary. Within the same build, the same secret and hashing configuration preserve
pseudonymous identities across a restart; they do not restore counters, sketch
windows, or the optional deduplication filter. Replayed spans can therefore be
counted again. Restart stability means replaying the same corpus into fresh state
gives the same results, not that aggregate state survives a restart.

With [summary file export](SUMMARY_EXCHANGE.md) configured from `0.1.0`,
previously written files can survive on a persistent private volume. Each restart
uses a new producer epoch. Those files are inputs for external combination, not
checkpoints loaded into the restarted connector. Retention still expires old files,
and a crash can lose observations since the last successful export.

Roll back with the previously recorded image digest and values:

```bash
helm rollback genai-sketches REVISION --namespace observability
```

Do not rotate `GENAI_SKETCH_SECRET` in the same change as a binary upgrade. Rotation
changes every keyed identity and ends comparison with older windows. Rotate at a
known window boundary, record the event, and allow old windows to expire.

## Compatibility

### Hash-Key And Label Hardening In v0.7.0

Startup now requires a private hash key of at least 32 bytes and refuses the
repository's published example keys. Keep an existing private key that meets
this requirement: its keyed identities and healthy accounting output do not
change. If an older key is shorter or public, generate a replacement with
`openssl rand -hex 32` and begin a new comparison period at a window boundary.

Known user, session, request, response, prompt and document identities cannot be
raw slice labels, even after field mappings change. Custom deduplication sources
are also protected. Replace such labels with non-sensitive model, team or route
dimensions. Invalid UTF-8 label values reject only their span under the existing
bounded `invalid_utf8` rejection reason.

### Mixed LiteLLM Traffic In v0.6.0

Collector v0.6.0 and chart 0.6.0 add `litellm-1.104.0-v2` for gateways serving
Chat, streaming Chat, Responses and Anthropic Messages together. Pair it with
fleetdiff v0.11.0 and approve its exact model maps and new contract identifier
before enabling it. Native call-type and route markers select one of the four
existing rules; streaming Messages and streaming Chat identify gateway selection,
not an independently confirmed provider model.

The existing `litellm-1.104.0-v1` contract remains unchanged and still excludes
Messages. Do not combine v1 and v2 windows. Change profiles at a window boundary
and retain each contract's exports separately. To roll back to v0.5.0, restore a
profile supported by that version before restoring the binary; keep v2 exports
with their compatible reader. Default request-model attribution is unchanged.

### Served-Model Profiles In v0.5.0

Pair collector v0.5.0 and chart 0.5.0 with fleetdiff v0.11.0. Existing
request-model attribution remains the default. To opt in, review a pinned
[served-model profile](MODEL_USAGE_SUMMARIES.md#served-identity-opt-in-in-v050)
and its exact approved maps, then generate and explicitly approve its new
`model_usage.contract_id`. Pass that ID to the reader. Keep legacy, standalone
and combined-profile windows separate; switching a profile or map changes the
contract, not the summary format or base accounting identity.

For mixed LiteLLM 1.104.0 Chat and Responses traffic, use `litellm-1.104.0-v1`
with the stock `otel` callback in latest-experimental GenAI mode. Streaming
Chat identifies the gateway-selected model, not a confirmed provider snapshot.
Messages is excluded. To roll back to v0.4.1, remove the identity and response-map
options before restoring the binary, and retain each contract's exports separately.

### Per-Model Summaries In v0.4.1

Upgrade every fleetdiff reader to v0.10.0 or later before enabling
`summary_export.model_usage` (Helm `summaryExport.modelUsage.enabled`). The option
is absent/off by default. Disabled exports retain their bytes and base accounting
fingerprint. Enabled exports carry a separate contract binding the approved model
map and routing; incompatible contracts or mixed enabled/disabled inputs fail.
See [Per-Model Summary Splits](MODEL_USAGE_SUMMARIES.md) for the version matrix,
coverage rules and expected-contract setup.

Collector v0.4.1 uses sketchkit v0.4.0. This does not add an identity migration
to the v0.3.3 distribution or standalone connector: x/text v0.42.0 and v0.43.0
normalization sources are identical, and the identity corpora remain unchanged.
Builds upgrading from earlier normalization versions still follow the migration
instructions below.

To roll back, disable the producer option before restoring an older binary and
keep the new files with a compatible reader. Do not remove the extension from
files to force compatibility. Keep enabled and disabled collection windows
separate; enabling the option does not create historical per-model splits.

### Sketchkit 0.3.0 In This Source Candidate

Collector v0.3.3 adopts llm-sketchkit v0.3.0 in both modules. Published v0.3.2
artifacts are unchanged. The normalization transitions differ by build:

| Build | Previous x/text | Candidate x/text | Pinned identity result |
| --- | --- | --- | --- |
| Standalone connector | v0.41.0 | v0.42.0 | Eight of 8,254 cases change canonical bytes and hashes |
| Root tests and built distribution | v0.42.0 | v0.43.0 | All 8,254 distribution identities remain unchanged |

The eight cases involve U+11F41 with acute/grave accents, deprecated acute,
combining-mark reordering and long combining runs. Their exact IDs and old/new
hashes are recorded in the [identity corpus notes](../connector/genaisketchconnector/testdata/identity/README.md#reviewed-standalone-migration).
The released standalone corpus is retained byte-for-byte as
`connector-xtext-0.41.json`; the distribution corpus is untouched. The active
corpora now agree. These sampled checks do not exhaust every Unicode string.
Downloaded x/text v0.42.0 and v0.43.0 normalization sources are identical;
the correction occurred between v0.41.0 and v0.42.0, not in Go's sketchkit
canonicalizer or hash function.

Standalone/custom builds previously resolving v0.41.0 must assess retained
windows for this identity migration. Upgrade affected producers together at a
new window boundary, record resolved dependency versions, and keep affected
old/new windows separate for merges and comparisons. Rebuild only from
legitimately available original inputs using one corrected build and the original
hash key; otherwise retain a separate historical series. Do not rewrite old
summaries or rotate keys as a substitute. Wire formats, profile names and the
accounting fingerprint are unchanged and cannot detect this boundary. The
released distribution already using v0.42.0 needs no additional identity rebuild
for this update within the tested corpus. Mixed Python producers must separately
follow sketchkit's [identity migration guide](https://github.com/llm-measurement/llm-sketchkit/blob/v0.3.0/docs/IDENTITY_MIGRATION.md).

Acceptance is stricter even where identities are unchanged: nonzero counters or
nonempty sketches require a positive observation interval. Configured empty
sketches remain valid at zero length. With a fixed clock, nonempty export at
startup or an exact new-window boundary fails explicitly; the exporter does not
invent elapsed time. An existing valid file remains intact, and retained counts
can be exported after time advances. A shutdown before time advances can fail
its final export; preserve that failure rather than claiming the window was
exported. The existing backward-clock rejection remains. Readers also reject
protobuf groups, which the wire schema never defined. Validate retained/external
inputs before adoption; do not alter interval timestamps or payloads just to
make them pass. Canonical summary/sketch encodings are unchanged.

### Sketchkit Hardening In Source Builds

Collector v0.3.2 and its connector module use llm-sketchkit v0.2.3 for parser
allocation limits, summary combination improvements and safer diagnostics. This backport preserves the
existing summary wire format, hashing and accounting contract. It retains each
build's normalization dependency: x/text v0.41.0 in the standalone connector
module, and v0.42.0 in the distribution. Their pinned identity tests remain
unchanged. No additional identity migration or window reset is introduced by
this dependency update; the distribution's earlier normalization change is
covered below.

The stricter readers reject encodings that exceed sketchkit's documented
[resource limits](https://github.com/llm-measurement/llm-sketchkit/blob/v0.2.3/docs/OPERATIONS.md).
Chart 0.3.4 selects collector v0.3.2. Adoption of
sketchkit v0.3.0 is a separate upgrade requiring the
[identity migration checks](https://github.com/llm-measurement/llm-sketchkit/blob/v0.3.0/docs/IDENTITY_MIGRATION.md).

### Feature Availability

Use collector v0.5.0 and chart 0.5.0 for the current profiles; see the
[release notes](releases/v0.5.0.md) and [installation verification](DEPLOYMENT.md).
The table records when each capability first became available, not a
recommendation to deploy an older release.

| Capability | First available | Guide |
| --- | --- | --- |
| Summary exchange in images and the connector module, using llm-sketchkit v0.2.0 | Collector v0.1.0 | [Summary exchange](SUMMARY_EXCHANGE.md) |
| Source-usage provenance accounting | Collector v0.2.0 | [Usage provenance](USAGE_PROVENANCE.md) |
| Question-oriented before/after investigation | fleetdiff v0.2.0 | [LiteLLM recipe](../examples/integrations/litellm/README.md) |
| Opt-in user/session ranking via `topk_keys` | Collector and fleetdiff v0.3.0 | [Top-K Keys](TOPK_KEYS.md) |
| Optional warning rules for unusual attempts, tokens per attempt, and coverage | Chart 0.3.1 | [Alerts](ALERTING.md) |
| Summary storage, read-only reader, and JSON logging values | Chart 0.3.2 | [Summary export](SUMMARY_EXPORT_HELM.md) |
| Local `scan` over retained windows and static config `diagnose` | fleetdiff v0.5.0 | [Scan and history retention](https://github.com/llm-measurement/fleetdiff/blob/main/docs/SCAN.md) |
| Opt-in per-model component summaries and local repricing | Collector v0.4.1, fleetdiff v0.10.0 | [Per-model summaries](MODEL_USAGE_SUMMARIES.md) |
| Opt-in served-model profiles, including mixed LiteLLM Chat and Responses | Collector v0.5.0, chart 0.5.0 | [Served-model profiles](MODEL_USAGE_SUMMARIES.md#served-identity-opt-in-in-v050) |

Distinct gauges describe the current window. For a longer period, merge compatible
summary state rather than adding gauges. Collect new ranking fields for complete
windows before comparison; enabling a field does not create historical data.

### Chart 0.3.2

Chart 0.3.2 originally shipped with `appVersion` and the default collector image
at v0.3.0. Chart 0.3.3 supersedes it with the patched v0.3.1 image; use
[Deployment](DEPLOYMENT.md) to verify the current artifacts. The original
chart-only release remains identified by tag `chart-v0.3.2`.

The new `summaryExport` values are opt-in. Defaults add no export volume,
initialization container, or reader sidecar. `telemetry.logs.encoding` defaults to
`console`; `json` can be enabled independently. Existing image-digest, hashing
Secret, TLS, and shadow settings remain in effect. Start with the
[summary guide and values](SUMMARY_EXPORT_HELM.md), preserving your existing
deployment settings when rendering and reviewing the upgrade.

Enabling export requires explicit producer/scope/key-version IDs, at least two
retained windows, and an interval no longer than the window. IDs must match
`^[A-Za-z0-9._:-]{1,128}$`. Intervals accept whole seconds `1s` through `60s` or
`1m`; export windows use a positive integer with `s`, `m`, or `h`. For an existing
PVC, set `summaryExport.storage.type=existingClaim`, name the claim, and explicitly
set `summaryExport.storage.sizeLimit=""` to clear the `emptyDir` default of `128Mi`.
The chart rejects conflicting storage choices. The reader requires export enabled.

Use one writer, one replica, and `Recreate`. A PVC retains files, not sketch state;
every restart begins a new epoch and may leave partial windows. Export retention
also deletes expired files from earlier epochs. Use 32 retained windows with
the scan's default 24-window baseline as an initial history budget, not a memory-sizing guarantee.
Archive needed closed windows before rollout or expiration; do not mix epochs or
partial windows in the simple readback workflow.

Export enables `fsGroupChangePolicy: OnRootMismatch` automatically. The non-root
initializer restores `0700` on its owned `exports` directory and `0600` on its
recognized owned files, including after a volume remount. It refuses symlinks,
wrong-owner entries, nonregular files, hard links, and unknown names inside that
directory. It does not recursively repair the PVC. Storage must support these
ownership and permission requirements; verify them on the intended storage driver
before rollout. Do not work around failure with a root initializer or public modes.

Before rollback, copy required summaries while the reader is still available.
Rollback to chart 0.3.1 removes the chart-managed export and reader, and returns
to that chart's log configuration. An operator-owned PVC is not deleted, but
`emptyDir` files disappear when the pod is replaced. Restore the old values and
recorded image digest; remaining files are not restart checkpoints.

### Text Identities In v0.3.1

The v0.3.1 distribution updates `golang.org/x/text` from v0.41.0 to v0.42.0.
Its upstream NFC composition fix changes `text_v1` canonical bytes and keyed
identities for some supplementary-plane characters with combining marks,
including U+11F41 followed by U+0300 or U+0301: the old normalizer could replace
them with unrelated characters. Upgrade at a window boundary and keep affected
pre-upgrade and post-upgrade windows separate for merges and comparisons, or
rebuild them from original inputs using one normalizer version/build and the
same hash secret. The accounting fingerprint does not identify this normalization
change, so operators must keep affected windows separate explicitly. Custom builds
follow their resolved `x/text` version; check the build's module graph, not just
the connector module's declared minimum.

### Collector v0.162.0 In Source Builds

The source checkout and collector v0.3.1 use Collector `v0.162.0` / pdata `v1.68.0`.
Published collector v0.3.0 images remain on `v0.161.0` / pdata `v1.67.0`.

Collector Contrib v0.162.0 changes Prometheus label sanitization: names starting
with one underscore no longer receive a `key_` prefix by default. Check custom
label names and matching queries before upgrading to v0.3.1 or a source build. The previous
behavior can be selected with
`--feature-gates=-pkg.translator.prometheus.PermissiveLabelSanitization`.
See the [upstream release notes](https://github.com/open-telemetry/opentelemetry-collector-contrib/releases/tag/v0.162.0).

### Session Slice Labels In v0.3.0

Version v0.3.0 adds a default hashed `session_key` reading
`gen_ai.conversation.id` and `session.id`. Configurations that use either attribute
in `slices.keys` will fail validation after upgrading, even when session top-k is
not selected or `topk` is zero. Slice keys cannot overlap hashed-field sources.

Remove these session identifiers from metric slices before upgrading. Keep slice
keys bounded and non-sensitive; use [session top-k](TOPK_KEYS.md) for hashed session
attribution instead of a metric label per session. Validate the revised config
with the new binary before rollout. This validation change does not change the
base accounting fingerprint.

### Optional Rankings In v0.3.0

Upgrade summary readers to fleetdiff v0.3.0 or later before enabling user/session
rankings. The default prompt-only export remains byte-compatible and the base
accounting fingerprint is unchanged. New rankings carry separate extraction
contracts; do not remove their markers to force incompatible inputs to combine.
Fleetdiff v0.3.0 reports unavailable attribution as `cannot_determine` when an
optional ranking is absent from either window or any producer. Older readers do
not answer the new questions and may reject mismatched optional measurements.

Each extra key allocates frequent-items state per retained window and slice. Check
[Sizing](SIZING.md) before enabling several keys. New fields contain no historical
data: collect complete windows with the new configuration before drawing
conclusions. A high-share session is a review candidate, not a loop diagnosis.

To roll back to v0.2.0, restore its configuration as well as its image: that version
does not accept `topk_keys`. Keep exported files unchanged and use a compatible
reader. Rollback still has the empty-state behavior described above.

### Usage Provenance

Source-usage provenance adds eight optional `usage_provenance.v1` counters without
changing the base accounting fingerprint. Fleetdiff v0.2.0 or later can compare
older exports with new ones, treating absent provenance as unknown; older consumers
may require matching counter sets. Do not rewrite old files or fingerprints.
Unannotated traffic retains its arithmetic. Enabling explicit `unavailable`
declarations excludes those fields from totals and marks requests missing, so a
change across that instrumentation boundary is not proof of workload savings.
See [Usage Provenance](USAGE_PROVENANCE.md).

| Surface | Current support |
| --- | --- |
| Collector component APIs (source) | OpenTelemetry Collector `v0.162.0` / pdata `v1.68.0` |
| Kubernetes | Chart declares Kubernetes 1.27 or newer |
| Images | Linux amd64 and arm64 |
| Configuration | Unknown or invalid connector fields fail startup |
| Wire sketches | Defined by the pinned `llm-sketchkit` release |

Only the latest published pre-1.0 release receives routine fixes. A supported
release with a confirmed high-severity vulnerability will receive a patched release
when feasible; mitigations and affected configurations will be published in the
security advisory. Report suspected vulnerabilities through the private route in
[Security](../SECURITY.md), not a public issue.
