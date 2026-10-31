# Deployment

For each tagged release, the release workflow publishes images to
`ghcr.io/llm-measurement/otelcol-genai-sketches` for `linux/amd64` and
`linux/arm64`. The production image is non-root, has a read-only-compatible
filesystem, contains no shell, and uses pinned build and runtime base images.

The example Compose stack remains a demonstration. Use the production image or Helm
chart for a persistent environment.

The commands below select collector **v0.6.2** and chart **0.6.2**.
Chart and collector versions advance independently.

## Verify An Image

Prerequisites: Docker with Buildx, curl, [cosign](https://docs.sigstore.dev/cosign/system_config/installation/),
and the GitHub CLI (`gh`, signed in for OCI provenance API checks and release downloads).
Helm and kubectl are needed only for the Kubernetes path; OpenSSL generates the
example hashing secret. Verification commands require network access to the
release and signature metadata.

Use an immutable digest from the GitHub release, not a mutable tag:

Fetch the references from that release's published metadata:

```bash
RELEASE=v0.6.2
IMAGE_REF="$(curl -fsSL \
  "https://github.com/llm-measurement/otelcol-genai-sketches/releases/download/${RELEASE}/image-digest.txt")"
IMAGE="${IMAGE_REF%@*}"
DIGEST="${IMAGE_REF#*@}"
VERSION="${RELEASE#v}"

cosign verify \
  --certificate-identity "https://github.com/llm-measurement/otelcol-genai-sketches/.github/workflows/release.yml@refs/tags/${RELEASE}" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  "$IMAGE@$DIGEST"

gh attestation verify "oci://$IMAGE@$DIGEST" \
  --repo llm-measurement/otelcol-genai-sketches \
  --cert-identity "https://github.com/llm-measurement/otelcol-genai-sketches/.github/workflows/release.yml@refs/tags/${RELEASE}" \
  --source-ref "refs/tags/${RELEASE}" --deny-self-hosted-runners
```

Chart 0.6.2 includes summary storage and readback alongside opt-in warning rules
and served-model usage accounting, and defaults to collector image 0.6.2. Both artifacts
share `release.yml` at `refs/tags/v0.6.2`; there is no separate chart tag.
Verify the chart's immutable reference too:

```bash
CHART_RELEASE=v0.6.2
CHART_VERSION=0.6.2
CHART_REF="$(curl -fsSL \
  "https://github.com/llm-measurement/otelcol-genai-sketches/releases/download/${CHART_RELEASE}/chart-digest.txt")"
CHART="${CHART_REF%@*}"
CHART_DIGEST="${CHART_REF#*@}"

cosign verify \
  --certificate-identity "https://github.com/llm-measurement/otelcol-genai-sketches/.github/workflows/release.yml@refs/tags/${CHART_RELEASE}" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  "$CHART@$CHART_DIGEST"

gh attestation verify "oci://$CHART@$CHART_DIGEST" \
  --repo llm-measurement/otelcol-genai-sketches \
  --cert-identity "https://github.com/llm-measurement/otelcol-genai-sketches/.github/workflows/release.yml@refs/tags/${CHART_RELEASE}" \
  --source-ref "refs/tags/${CHART_RELEASE}" --deny-self-hosted-runners
```

BuildKit attaches a platform-specific SPDX SBOM and build provenance to each image.
Inspect them with:

```bash
docker buildx imagetools inspect "$IMAGE@$DIGEST" --format '{{ json .SBOM }}'
docker buildx imagetools inspect "$IMAGE@$DIGEST" --format '{{ json .Provenance }}'
```

Runtime releases carry image and chart digests, an SBOM index export, a packaged
chart, and checksums. Collector v0.3.3 introduced `provenance.jsonl` for
`SHA256SUMS` and `sbom-index.json`; v0.4.1 adds `openvex.json`
as a separate attestation subject. Verify those metadata subjects before checking
the downloaded files. The image and chart share this release and signing identity:

```bash
mkdir -p release-assets
gh release download "$RELEASE" \
  --repo llm-measurement/otelcol-genai-sketches \
  --dir release-assets
for subject in SHA256SUMS sbom-index.json openvex.json; do
  gh attestation verify "release-assets/$subject" \
    --bundle release-assets/provenance.jsonl \
    --repo llm-measurement/otelcol-genai-sketches \
    --signer-workflow llm-measurement/otelcol-genai-sketches/.github/workflows/release.yml \
    --source-ref "refs/tags/${RELEASE}" --deny-self-hosted-runners
done
(cd release-assets && shasum -a 256 -c SHA256SUMS)
```

Verification checks the digest's signature and provenance against this repository's
release workflow and tag. Review runtime configuration and workload sizing separately.
The checksum check establishes file integrity; the preceding attestation checks
authenticate the checksum manifest, SBOM index, and OpenVEX document. Keep the image and chart
signature and provenance checks above as well.
Verify the release-specific assessment as described in [Release OpenVEX](OPENVEX.md).
The image includes the project license and collected third-party notices under
`/licenses`.

## Run With Docker

After image verification above, run the production configuration locally:

```bash
export GENAI_SKETCH_SECRET="$(openssl rand -hex 32)"
docker run --rm --name genai-sketches \
  --read-only --cap-drop=ALL --security-opt=no-new-privileges \
  --pids-limit=256 --cpus=2 --memory=2g \
  -e GENAI_SKETCH_SECRET \
  -p 127.0.0.1:4317:4317 -p 127.0.0.1:4318:4318 \
  -p 127.0.0.1:8889:8889 \
  "${IMAGE_REF:?verify the release image first}"
```

Host applications send OTLP to localhost on 4317 (gRPC) or 4318 (HTTP);
Prometheus can scrape `http://127.0.0.1:8889/metrics`. Receivers bind `0.0.0.0`
inside the container so port publishing works, but only host loopback is
published. The health port is not published. Do not expose unauthenticated OTLP
or metrics on a public interface. A host-process collector should keep its
receiver on `127.0.0.1` unless a reviewed TLS/authentication setup requires otherwise.

Keep the hashing secret in your secret manager for a persistent deployment;
re-generating it changes pseudonymous identities. Mount a custom configuration
read-only and pass `--config=/path/in/container.yaml` to override the default.
Summary exports need a private writable volume; see the
[LiteLLM container recipe](../examples/integrations/litellm/README.md#collector-in-docker)
for an example. Ctrl-C stops this foreground container.

## Central Collector With Helm

Create the hash secret without placing it in a values file:

```bash
kubectl create namespace observability
openssl rand -hex 32 | kubectl -n observability create secret generic \
  genai-sketch-secret --from-file=secret=/dev/stdin
```

Install the verified chart with the verified image digest:

```bash
helm upgrade --install genai-sketches \
  oci://ghcr.io/llm-measurement/charts/otelcol-genai-sketches \
  --version "$CHART_VERSION" \
  --namespace observability \
  --set-string existingSecret=genai-sketch-secret \
  --set-string image.digest="$DIGEST"
```

Example values are in the source tree at the chart's tag. When working from a
checkout, replace the OCI chart reference with
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

Release images from v0.1.0 and source builds support
[opt-in summary file export](SUMMARY_EXCHANGE.md).
It exports complete state for local combination across independent collectors;
it does not make Prometheus able to merge sketches. Chart **0.3.2** adds opt-in
summary storage, a read-only reader sidecar, and independent JSON logging.
[Summary Export With Helm](SUMMARY_EXPORT_HELM.md) walks through copy,
investigate, and scan with the v0.6.2 collector image.

The chart's `connector.topKKeys` passes through the optional
key list; its default `[]` omits `topk_keys` and preserves prompt-only ranking.
Pin a verified v0.3.1 or later image before opting in: older v0.2.0 images do not
understand this option. See [Top-K Keys](TOPK_KEYS.md).
Additional keys allocate sketch state per slice and retained window. The chart's
2 GiB limit is not a sizing recommendation for extra keys; consult the
[measured additional-key costs](SIZING.md) and measure the intended workload.

## Source Build Versions

`make dist` and `make production-image` use the development version in
`builder.yaml`. To identify a particular build, pass `VERSION=0.3.0-rc.1` to
either command, or `--build-arg VERSION=0.3.0-rc.1` to Docker. This is a build
label, not a published release. The generated distribution version and connector
component inventory use the same value, while the local connector path remains
unchanged. Build commands ignore a parent Go workspace so local replacements
cannot silently change clean-build dependency resolution.

## Keep An Existing Trace Backend

Enable trace fan-out without changing the original trace payload:

```bash
helm upgrade --install genai-sketches \
  deploy/helm/otelcol-genai-sketches \
  --namespace observability \
  --set shadow.enabled=true \
  --set-string shadow.endpoint=collector.example.net:4317 \
  --set-json 'networkPolicy.egress=[{"to":[{"ipBlock":{"cidr":"192.0.2.10/32"}}],"ports":[{"protocol":"TCP","port":4317}]}]'
```

Replace the documentation address `192.0.2.10/32` with the backend's approved
IP/CIDR; standard NetworkPolicy does not select destinations by DNS name. For
in-cluster backends, use the pod/namespace peers in the
[migration example](#chart-070-network-policy-migration).

Set `shadow.insecure=true` only for a trusted plaintext network. If the backend
needs an authorization header, put its complete value in a second Secret and set
`shadow.auth.existingSecret` and `shadow.auth.key`. Raw content already present in a
trace is forwarded; hashing inside the connector does not redact the forwarded
copy.

## Receiver TLS

For TLS, create a Secret containing `tls.crt` and `tls.key`, then set
`receiverTLS.enabled=true` and `receiverTLS.existingSecret`. For mutual TLS (mTLS),
also include the client CA and set `receiverTLS.clientCAKey` to its Secret key.
mTLS authenticates clients on both OTLP/gRPC and OTLP/HTTP; server TLS alone
encrypts traffic but does not authenticate senders. The chart mounts the files
read-only. Certificate issuance and rotation remain cluster responsibilities.

```yaml
receiverTLS:
  enabled: true
  existingSecret: receiver-tls
  clientCAKey: ca.crt
```

## Chart 0.7.0 Network Policy Migration

**The next minor chart, 0.7.0, enables NetworkPolicy by default.** Collector
appVersion is 0.7.0. Chart 0.6.x leaves NetworkPolicy off by default.
Before upgrading, configure allowed senders, Prometheus peers and shadow-backend
egress. Reusing old values without the new defaults can preserve the old opt-out;
review the merged values and rendered policy before applying the upgrade.

The default allows OTLP on the configured gRPC and HTTP TCP ports from pods in
the release's namespace. Metrics ingress is denied until `metricsFrom` is set.
Egress is limited to TCP/UDP 53 toward pods labeled `k8s-app: kube-dns` in
`kube-system`. Other ingress and egress are denied. The cluster's network plugin
must enforce Kubernetes NetworkPolicy. Node-local DNS and clusters with other DNS
labels need explicit egress rules; set `allowDNS: false` when replacing the preset.

Use the chart's [selected-peers preset](../deploy/helm/otelcol-genai-sketches/presets/selected-peers.yaml)
as a starting point, replacing its namespace and workload labels. A peer's
`namespaceSelector` and `podSelector` are combined with AND; separate peer entries
are alternatives. A `podSelector` alone selects pods in the release's namespace.
Empty selectors deliberately select all matching pods or namespaces.

```bash
helm template genai-sketches deploy/helm/otelcol-genai-sketches \
  --namespace observability \
  --values deploy/helm/otelcol-genai-sketches/presets/selected-peers.yaml
```

`allowSameNamespace: false` removes the same-namespace OTLP rule. With both peer
lists and `ingress` empty, ingress is denied. `allowDNS: false` with empty
`egress` denies egress. Raw `ingress` and `egress` rules are appended to presets;
`{}` is an explicit allow-all rule. Other policies selecting the same pods can
also grant access because Kubernetes combines their allow rules.

Shadow forwarding needs an `egress` rule for the backend's destination and port;
with NetworkPolicy enabled, the chart refuses to render shadow forwarding
without an explicit `networkPolicy.egress` rule. Likewise, `serviceMonitor.enabled`
requires `networkPolicy.metricsFrom` or an explicit `networkPolicy.ingress` rule.
These checks require a rule, not proof that its selectors match the backend or
Prometheus pods. For example:

```yaml
networkPolicy:
  egress:
    - to:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: tracing
          podSelector:
            matchLabels:
              app.kubernetes.io/name: trace-backend
      ports:
        - protocol: TCP
          port: 4317
```

For a deliberate opt-out, set `networkPolicy.enabled: false`. This removes the
chart's policy; it does not remove other cluster policies. NetworkPolicy limits
reachability, not sender identity. Use mTLS when client authentication is needed.

Chart 0.7.0 defaults to collector tag `0.7.0` through appVersion, with empty
`image.tag` and `image.digest`. It enforces non-root execution, dropped capabilities, no privilege escalation
and a read-only root filesystem even when summary export is disabled. Unknown
top-level and security-related values now fail validation instead of being ignored.
When changing the runtime image, set its verified `image.digest`; to deliberately
use `image.tag`, clear `image.digest` as well. A configured digest takes priority.

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
  structured-log surface and should be routed to restricted log storage. Set
  `connector.topK=0` to disable that surface and its frequent-items state.
- Chart 0.7.0 enables NetworkPolicy by default with the allow-list described above.

## Air-Gapped Registry Mirror

On a staging host that can reach both registries, copy the image without changing
its manifest digest:

```bash
MIRROR=registry.example.com/observability/otelcol-genai-sketches
skopeo copy --all --preserve-digests \
  "docker://$IMAGE@$DIGEST" "docker://$MIRROR:$VERSION"
test "$(skopeo inspect --format '{{.Digest}}' "docker://$MIRROR:$VERSION")" = "$DIGEST"
```

Digest equality verifies the image manifest and referenced platform images. OCI
signature and attestation referrers do not follow every registry copy operation;
mirror them with tooling supported by both registries or retain an offline
verification bundle as part of the transfer procedure.

Download the chart and `SHA256SUMS` from the same GitHub release, verify the
checksum, then transfer the chart into the disconnected environment. Push it to a
private OCI registry with `helm push` or install the verified `.tgz` directly.

## FIPS Environments

FIPS 140-3 environments require an approved cryptographic module, toolchain,
operating environment, and validation process. Build and validate against your
organization's requirements; the standard release is not FIPS-validated. Verify the resulting private artifact independently; do not reuse the
standard release's signature or attestation for a rebuilt binary.

## Accounting Alerts

If Prometheus Operator is installed, set `prometheusRule.enabled=true`. The chart
then provides configurable alerts for incomplete aggregate usage, overflow traffic,
invalid or conflicting token fields, subset violations, and missing deduplication
keys. Set `prometheusRule.sliceName` to exactly one accounting view; metrics from
different slice names describe duplicate views of the same observations and must not
be summed together.

For budget exhaustion, reservation leaks, quota overruns, or settlement drift,
combine these counters with an external budget or exact ledger. See
[Acting On Results](TOKEN_USAGE.md#acting-on-results).

Read [Sizing](SIZING.md) before changing capacity and [Upgrading](UPGRADING.md)
before replacing a running collector.
