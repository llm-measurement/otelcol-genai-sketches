<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Code authors: Vijay and Codex -->

# Helm Chart 0.3.2

Export summary windows from Kubernetes and copy them locally for fleetdiff
investigations and scans. This chart adds private storage, an optional read-only
reader sidecar, and structured JSON logs. The collector stays at **v0.3.0** and
nothing else needs a release.

## Changes

- `summaryExport` configures `/var/lib/genai-sketches/exports` on temporary
  `emptyDir` storage or an existing PVC. Export and reader both default off.
- A non-root initializer prepares private `0700` directories and `0600` files.
  `summaryExport.reader.enabled` adds a read-only sidecar for
  `kubectl exec -c reader -- tar` readback.
- Init and reader share one digest-pinned utility image, defaulting to BusyBox
  1.37.0-musl at `sha256:5cec3fc171c87218698e85a52af7087de727372aae264a787b8112901a5b0092`.
  Override `summaryExport.utilityImage.repository` and `digest` for an internal
  registry. Both containers run non-root, drop all capabilities, and use read-only
  root filesystems. Reader receives no hashing/TLS credentials or service token.
- The copy helper validates completed windows and prints `BEFORE`, `AFTER`, and
  `AS_OF` for a copy, investigate, scan workflow with fleetdiff v0.5.0.
- `telemetry.logs.encoding: json` enables structured collector logs independently
  of export, including workflow rankings for the existing LogQL query.

## Install Or Upgrade

Verify the chart and collector image using [Deployment](../DEPLOYMENT.md), then
follow [Summary Export With Helm](../SUMMARY_EXPORT_HELM.md) to provision storage
and adapt the example values. Preserve your existing settings when upgrading:

```sh
helm upgrade --install genai-sketches \
  oci://ghcr.io/llm-measurement/charts/otelcol-genai-sketches --version 0.3.2 \
  --namespace observability \
  -f deploy/kubernetes/central/summary-values.yaml \
  --set-string image.digest="${DIGEST:?verify the v0.3.0 image first}"
```

## Limits

Use one replica and one writer per directory. A PVC preserves exported files,
not in-memory state; restart creates a new epoch and retention still expires old
files. The copy helper selects complete closed windows from one epoch. Start with
16 retained windows and `scan --baseline 6`, and check memory sizing. Validate
ownership/remount behavior on your storage driver. Full settings, bounds, and
rollback guidance are in [the guide](../SUMMARY_EXPORT_HELM.md#settings-and-limits)
and [Upgrading](../UPGRADING.md#chart-032).

## Validation

The Kubernetes test uses released collector v0.3.0 and fleetdiff v0.5.0. It covers
emptyDir and local-PVC export, forced kubelet fsGroup reconciliation, private file
modes after remount, new process epochs, read-only reader access, and the documented
copy workflow. Its 800 synthetic spans produce 91% user/session shares, scan exits
3/0 for changed/quiet cases, 17 metric series, and one fixed Loki label. Extended
sentinel scans include identities, code snippets, and file paths.

See the [repeatable test](../SUMMARY_EXPORT_HELM.md#validation). These checks cover
the synthetic collector path, not production workload calibration.
