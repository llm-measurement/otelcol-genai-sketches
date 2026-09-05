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

Roll back with the previously recorded image digest and values:

```bash
helm rollback genai-sketches REVISION --namespace observability
```

Do not rotate `GENAI_SKETCH_SECRET` in the same change as a binary upgrade. Rotation
changes every keyed identity and ends comparison with older windows. Rotate at a
known window boundary, record the event, and allow old windows to expire.

## Compatibility

| Surface | Current support |
| --- | --- |
| Collector component APIs | OpenTelemetry Collector `v0.160.0` / pdata `v1.66.0` |
| Kubernetes | Chart declares Kubernetes 1.27 or newer |
| Images | Linux amd64 and arm64 |
| Configuration | Unknown or invalid connector fields fail startup |
| Wire sketches | Defined by the pinned `llm-sketchkit` release |

Only the latest published pre-1.0 release receives routine fixes. A supported
release with a confirmed high-severity vulnerability will receive a patched release
when feasible; mitigations and affected configurations will be published in the
security advisory. Report suspected vulnerabilities through the private route in
[Security](../SECURITY.md), not a public issue.
