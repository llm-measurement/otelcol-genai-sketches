# Deployment

For each tagged release, the release workflow publishes images to
`ghcr.io/llm-measurement/otelcol-genai-sketches` for `linux/amd64` and
`linux/arm64`. The production image is non-root, has a read-only-compatible
filesystem, contains no shell, and uses pinned build and runtime base images.

The example Compose stack remains a demonstration. Use the production image or Helm
chart for a persistent environment.

## Verify An Image

Use an immutable digest from the GitHub release, not a mutable tag:

```bash
IMAGE=ghcr.io/llm-measurement/otelcol-genai-sketches
DIGEST=sha256:replace-with-release-digest

cosign verify \
  --certificate-identity-regexp 'https://github.com/llm-measurement/otelcol-genai-sketches/.github/workflows/release.yml@refs/tags/v.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  "$IMAGE@$DIGEST"

gh attestation verify "oci://$IMAGE@$DIGEST" \
  --repo llm-measurement/otelcol-genai-sketches
```

BuildKit attaches a platform-specific SPDX SBOM and build provenance to each image.
Inspect them with:

```bash
docker buildx imagetools inspect "$IMAGE@$DIGEST" --format '{{ json .SBOM }}'
docker buildx imagetools inspect "$IMAGE@$DIGEST" --format '{{ json .Provenance }}'
```

Each GitHub release also carries the image index digest, an SBOM index export, and
SHA-256 checksums for the downloadable metadata.

Successful verification proves that the referenced digest was signed by this
repository's release workflow for the stated tag and has not changed since. It does
not prove that the software is vulnerability-free, that a runtime configuration is
safe, or that the image is suitable for a particular workload. The image includes
the project license and collected third-party license notices under `/licenses`.

## Central Collector With Helm

Create the hash secret without placing it in a values file:

```bash
kubectl create namespace observability
openssl rand -hex 32 | kubectl -n observability create secret generic \
  genai-sketch-secret --from-file=secret=/dev/stdin
```

Install the chart published with the release:

```bash
VERSION=replace-with-release-version
helm upgrade --install genai-sketches \
  oci://ghcr.io/llm-measurement/charts/otelcol-genai-sketches \
  --version "$VERSION" \
  --namespace observability \
  --set-string existingSecret=genai-sketch-secret \
  --set-string image.digest="$DIGEST"
```

The example values file is available in the source archive attached to the same
release. When working from a checkout, replace the OCI chart reference with
`deploy/helm/otelcol-genai-sketches`, omit `--version`, and optionally add
`--values deploy/kubernetes/central/values.yaml`.

Send OTLP/gRPC traffic to
`genai-sketches-otelcol-genai-sketches.observability.svc:4317` or OTLP/HTTP to
port `4318`. Prometheus can scrape port `8889`. The health port is used only for pod
probes and is not exposed by the Service.

The chart enforces one replica. Exact counters from disjoint replicas can be added,
but HLL estimates and top-k state cannot be merged by Prometheus. For scale-out,
route each stable tenant or workload shard to one chart release and keep the shard
identity in the release name and Prometheus external labels.

## Keep An Existing Trace Backend

Enable trace fan-out without changing the original trace payload:

```bash
helm upgrade --install genai-sketches \
  deploy/helm/otelcol-genai-sketches \
  --namespace observability \
  --set shadow.enabled=true \
  --set-string shadow.endpoint=collector.example.net:4317
```

Set `shadow.insecure=true` only for a trusted plaintext network. If the backend
needs an authorization header, put its complete value in a second Secret and set
`shadow.auth.existingSecret` and `shadow.auth.key`. Raw content already present in a
trace is forwarded; hashing inside the connector does not redact the forwarded
copy.

## Receiver TLS

For TLS or mutual TLS, create a Secret containing `tls.crt`, `tls.key`, and,
optionally, a client CA. Set `receiverTLS.enabled=true` and
`receiverTLS.existingSecret`. The chart mounts the files read-only. Network policy,
certificate issuance, and rotation remain cluster responsibilities.

## Sidecar

The sidecar example keeps traffic local to one application pod. Merge
[`sidecar-container.yaml`](../deploy/kubernetes/sidecar/sidecar-container.yaml) into
the workload and point its OTLP exporter at `http://127.0.0.1:4317`. Each pod then
has independent counters and sketches. Keep `service.name`, pod, or workload as a
Prometheus target label; do not add pod IDs to connector slice labels.

## Operational Defaults

- The memory limiter begins refusing work before the pod reaches its memory limit.
- The batch processor precedes the connector.
- The pod runs without a service-account token, Linux capabilities, privilege
  escalation, or a writable root filesystem.
- Configuration changes restart the pod. `Recreate` avoids overlapping aggregation
  replicas during an upgrade.
- `pprof`, `zpages`, and public debug endpoints are absent. Top-k remains a bounded
  structured-log surface and should be routed to restricted log storage.
- NetworkPolicy is opt-in because namespace selectors differ by cluster. Enabling it
  with empty rules denies ingress and egress.

## Accounting Alerts

If Prometheus Operator is installed, set `prometheusRule.enabled=true`. The chart
then provides configurable alerts for incomplete aggregate usage, overflow traffic,
invalid or conflicting token fields, subset violations, and missing deduplication
keys. Set `prometheusRule.sliceName` to exactly one accounting view; metrics from
different slice names describe duplicate views of the same observations and must not
be summed together.

Budget exhaustion, reservation leaks, quota overruns, and settlement drift require
an external budget or exact ledger to compare against. The connector does not emit
those alerts by itself. Use its counters as one reconciliation input, not as the
ledger.

Read [Sizing](SIZING.md) before changing capacity and [Upgrading](UPGRADING.md)
before replacing a running collector.
