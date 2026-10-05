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

### Collector v0.162.0 In Source Builds

The source checkout uses Collector `v0.162.0` / pdata `v1.68.0`. Published
collector v0.3.0 images remain on `v0.161.0` / pdata `v1.67.0`.

Collector Contrib v0.162.0 changes Prometheus label sanitization: names starting
with one underscore no longer receive a `key_` prefix by default. Check custom
label names and matching queries before deploying a source build. The previous
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
