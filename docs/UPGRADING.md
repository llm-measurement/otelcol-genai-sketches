# Upgrading And Rollback

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
binary. The same secret and hashing configuration preserve pseudonymous identities
across a restart; they do not restore counters, sketch windows, or the optional
deduplication filter. Replayed spans can therefore be counted again. Restart
stability means replaying the same corpus into fresh state gives the same results,
not that aggregate state survives a restart.

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

### Feature Availability

Use collector v0.3.1 and chart 0.3.3 for the current security fixes; see the
[release notes](releases/v0.3.1.md) and [verified installation](DEPLOYMENT.md).
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
also deletes expired files from earlier epochs. Use 16 retained windows with
`scan --baseline 6` as an initial history budget, not a memory-sizing guarantee.
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
