<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Code authors: Vijay and Codex -->

# Summary Export With Helm

Keep summary windows in Kubernetes, copy them locally, and use fleetdiff to find
the users and sessions behind a change. Chart **0.3.2** adds private storage, a
read-only reader sidecar, and JSON log encoding. The collector stays at **v0.3.0**
and nothing else needs a release.

## Install

Use Helm, kubectl with pod-exec permission, local Python **3.10+**, and
[verified fleetdiff v0.5.0](../examples/integrations/agentgateway/README.md#run-it).
Clone this repository for the example values and copy helper. Follow
[Deployment](DEPLOYMENT.md) to verify the chart and collector image, obtain
`DIGEST`, and create the `observability` namespace and hashing Secret.

Provision a filesystem PVC named `genai-sketch-summaries` in that namespace.
The [example values](../deploy/kubernetes/central/summary-values.yaml) select it,
enable export and the reader, and retain **16 one-minute windows**. They rank
prompts and users by tokens, and sessions by model attempts. Adapt producer `app`,
scope `app-observations`, and key version `app-key-v1` to your inventory.

Render the chart and review it before installing. Preserve your existing Secret,
image digest, TLS, shadow, and resource settings when upgrading:

```sh
umask 077
helm template genai-sketches \
  oci://ghcr.io/llm-measurement/charts/otelcol-genai-sketches --version 0.3.2 \
  --namespace observability \
  -f deploy/kubernetes/central/summary-values.yaml \
  --set-string image.digest="${DIGEST:?verify the released v0.3.0 image first}"

helm upgrade --install genai-sketches \
  oci://ghcr.io/llm-measurement/charts/otelcol-genai-sketches --version 0.3.2 \
  --namespace observability \
  -f deploy/kubernetes/central/summary-values.yaml \
  --set-string image.digest="${DIGEST:?verify the release image first}"
kubectl -n observability rollout status \
  deployment/genai-sketches-otelcol-genai-sketches --timeout=180s
```

For a temporary emptyDir trial, add all three overrides to both Helm commands:
`--set summaryExport.storage.type=emptyDir
--set-string summaryExport.storage.existingClaim=
--set-string summaryExport.storage.sizeLimit=128Mi`.

### Internal Registries

Mirror the default BusyBox 1.37.0-musl image, then set its repository and verified
digest in your values. Init and reader both use this image; a digest is required.
The default is `docker.io/library/busybox` at
`sha256:5cec3fc171c87218698e85a52af7087de727372aae264a787b8112901a5b0092`.

```yaml
summaryExport:
  utilityImage:
    repository: registry.example.com/mirrors/busybox
    digest: sha256:5cec3fc171c87218698e85a52af7087de727372aae264a787b8112901a5b0092
imagePullSecrets:
  - name: registry-credentials
```

Use the mirror's digest if your registry changes the manifest. Keep the BusyBox
shell, tar, and filesystem utilities when substituting an image, and verify its
operation with the configured non-root UID/GID.

## Copy, Investigate, Scan

Wait for at least seven complete windows: six baseline plus one judged window.
Confirm one ready pod in the listing, then select it:

```sh
kubectl -n observability get pods \
  -l app.kubernetes.io/instance=genai-sketches,app.kubernetes.io/name=otelcol-genai-sketches
POD="$(kubectl -n observability get pods \
  -l app.kubernetes.io/instance=genai-sketches,app.kubernetes.io/name=otelcol-genai-sketches \
  -o jsonpath='{.items[0].metadata.name}')"
test -n "$POD"
python3 deploy/kubernetes/summary-export/copy-windows.py \
  --namespace observability --pod "$POD" --output ./summaries-NEW
```

Use a fresh output directory each time. The helper copies through
`kubectl exec -c reader -- tar`, selects complete closed windows, and prints
shell-quoted **`BEFORE`**, **`AFTER`**, and **`AS_OF`** assignments for the latest
pair. Paste those three assignments into your shell, then run:

```sh
umask 077
fleetdiff investigate \
  --before "${BEFORE:?run the copy helper first}" \
  --after "${AFTER:?run the copy helper first}" --expected app

scan_status=0
fleetdiff scan ./summaries-NEW --expected app --baseline 6 \
  --as-of "${AS_OF:?run the copy helper first}" || scan_status=$?
printf 'scan exit: %s\n' "$scan_status"
```

Keep `--expected app` tied to your configured producer inventory. `investigate`
exits 0 for a report; `scan` exits **3** for unusual findings, **0** for a quiet
evaluated window, **4** for insufficient history or coverage, and **1/2** for
input/option errors. Keep `--as-of` for the copied snapshot; omit it for live
freshness checks. Add `--format json` to save structured reports outside the
summary directory.

## JSON Logs And Workflow Rankings

Set `telemetry.logs.encoding: json` for your existing log shipper, independently
of summary export. For the [workflow LogQL query](../examples/integrations/agentgateway/workflows.logql),
use `workflow:`-prefixed `app.workflow` values and these ranking settings:

```yaml
connector:
  topKKeys:
    - {field: user_key, weight: tokens}
    - {field: session_key, weight: requests}
    - {field: workflow_key, weight: tokens}
  fields:
    user_key: {from_attributes: [user.id], canonicalization: text_v1, domain: "user:v1"}
    session_key: {from_attributes: [session.id], canonicalization: text_v1, domain: "session:v1"}
    workflow_key: {from_attributes: [app.workflow], canonicalization: text_v1, domain: "user:v1"}
telemetry: {logs: {encoding: json}}
```

Keep your summary/storage, shadow, and TLS settings. Forward JSON stdout with the
fixed Loki label `service_name="otelcol-genai-sketches"`; hashes stay in log bodies.
Fleetdiff v0.5.0 reads user/session rankings; custom workflows stay in snapshots.

## Settings And Limits

- Export and reader default off; reader requires export. IDs must be non-sensitive
  `^[A-Za-z0-9._:-]{1,128}$` strings; `keyID` identifies the secret version, not its
  value. Export interval accepts `1s`..`60s` or `1m`, no longer than the window.
  Export windows accept whole `s`, `m`, or `h` up to 24h; retention is 2..120.
- One replica and `Recreate` keep one writer per directory. Init and reader share
  the collector UID/GID (default `65532:65532`), drop all capabilities, and use
  read-only root filesystems. Only init and collector write the export volume.
  Reader has no hashing/TLS credentials or service-account token.
- A PVC preserves files, not sketch state. Restart begins a new epoch; the copy
  helper rejects mixed epochs and partial windows. Wait for prior epochs to expire
  and enough new windows to complete. Pod replacement loses emptyDir data.
- Existing claims use `storage.type: existingClaim`, `existingClaim`, and
  `sizeLimit: ""`; the chart leaves PVC lifecycle to you. Init restores owned
  exports to `0700`/`0600` and rejects unsafe entries. Validate fsGroup/remount
  behavior on your storage driver; resolve ownership failures with its administrator.
- Retention expires files even on a PVC. Export limits remain 512 files / 64 MiB
  plus an 8 MiB temporary file. For longer history, use a private
  [archive](https://github.com/llm-measurement/fleetdiff/blob/v0.5.0/docs/SUMMARY_ARCHIVE.md).
  Fleetdiff accepts up to 512 files, 1,024 entries, and 32 MiB per input. Size memory
  for retained windows and ranking keys using [Sizing](SIZING.md).
- Summaries are pseudonymous. Keep copies private; complete collector windows
  describe observed traffic, not upstream delivery guarantees. Findings guide
  investigation rather than establish a loop's cause.

## Validation

The checked-in Kubernetes test uses collector v0.3.0 and fleetdiff v0.5.0 with
800 synthetic spans. It checks private storage and PVC remounts, finds the planted
91% user/session shares, verifies scan exits 3/0 for changed/quiet cases, and scans
metrics, logs, summaries, decoded sketches, reports, and Loki for planted identities,
code, and file paths. Metric cardinality stays at 17 series with one fixed Loki label.

```sh
python3 -B deploy/kubernetes/summary-export/check.py \
  --kind kind --helm helm --fleetdiff /path/to/v0.5/fleetdiff \
  --output .cache/summary-export-NEW
```

Local validation passed on Kubernetes 1.35.8 / kind 0.33.0 on Apple M4 arm64 with
a local PVC. This validates the synthetic collector path and that storage setup.
