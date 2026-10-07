# Chart-Only Releases

Use `.github/workflows/chart-release.yml` and a signed `chart-v<version>` tag for
chart-only changes. The existing `release.yml` listens to `v*`, builds an image,
and packages a chart with both version fields overridden from that runtime tag.
Do not rerun `v0.3.0` or create `v0.3.2` for this chart-only release.

For this release, `Chart.yaml` must contain `version: 0.3.2` and
`appVersion: "0.3.0"`. The chart image helper derives its default tag from
`appVersion`, not the chart version. Both alert switches remain off by default.
Summary export and its reader also remain off by default. The chart workflow
packages the checked-in metadata without either version override, validates it
against the tag, and never builds or publishes an image.
Its entire publishing job runs only when repository visibility is explicitly
`public`; private, internal, or missing visibility skips publishing, signing,
and public transparency operations.

Chart and runtime releases share the chart's OCI version namespace. Chart-only
versions 0.3.1 and 0.3.2 are allocated to their chart releases. The unchanged
combined-release workflow must not later publish those runtime versions and
overwrite them: choose a new unused combined version or separate that workflow's
chart version first.

## Maintainer Steps

These are release-owner actions, not commands to run during preparation.

1. Review and merge the signed PR into `main`, including the summary templates,
   values/schema, chart tests, chart version, workflow, and chart release notes.
   Require CI success, including the Kubernetes summary job, on the exact commit
   being released. Do not tag a local working tree containing uncommitted changes.
2. In a clean checkout of that merged commit, run the existing checks and the
   chart-specific fixtures with the repository-pinned Helm and Prometheus tools:

   ```sh
   make helm-check
   make prometheus-rule-check
   HELM="$(command -v helm)" PROMTOOL_DOCKER=1 \
     CHART_RELEASE_TAG=chart-v0.3.2 go test ./deploy/helm/tests -count=1 -v
   ```

3. Create and verify only the chart tag on the reviewed commit, then push that
   one tag. The release owner supplies `RELEASE_COMMIT` as the reviewed full SHA:

   ```sh
   git fetch origin main
   git merge-base --is-ancestor "$RELEASE_COMMIT" origin/main
   git tag -s chart-v0.3.2 "$RELEASE_COMMIT" -m 'Helm chart 0.3.2; collector 0.3.0 unchanged'
   git verify-tag chart-v0.3.2
   git push origin refs/tags/chart-v0.3.2
   ```

4. Watch `chart-release`, not the runtime `release` workflow. It checks main
   ancestry and metadata, runs chart/promtool checks, packages the chart, refuses
   an existing OCI version, publishes/signs/attests the chart, and compares an
   anonymous pull byte-for-byte. It then creates `chart-v0.3.2` with only the chart
   archive, `chart-digest.txt`, and `SHA256SUMS`. `--latest=false` keeps this from
   replacing the runtime release as GitHub's latest release.
5. Complete the public verification below. Keep anomaly warnings disabled until
   accounting scope, scrape health, history, and thresholds have been reviewed.

Never move the tag, overwrite an OCI version, or rerun the runtime release to
recover a chart release. A rerun after chart publication deliberately stops at the
existing-version guard. If a later signing, attestation, visibility, or release
upload step fails, stop and inspect the published digest before deciding on a
controlled recovery or a new chart patch version. The registry preflight is not
an atomic lock against a separate publisher: coordinate one release owner and do
not publish the same chart version through another workflow.

## Public Verification

Use a fresh directory. These commands read already-published artifacts. GitHub
attestation verification needs GitHub authentication and may require registry
read authentication; use approved read-only credentials. The workflow separately
checks anonymous chart access after logging out of the registry.

```sh
gh release download chart-v0.3.2 --repo llm-measurement/otelcol-genai-sketches \
  --pattern 'otelcol-genai-sketches-0.3.2.tgz' --pattern chart-digest.txt --pattern SHA256SUMS
sha256sum -c SHA256SUMS
CHART_REF="$(cat chart-digest.txt)"
cosign verify \
  --certificate-identity 'https://github.com/llm-measurement/otelcol-genai-sketches/.github/workflows/chart-release.yml@refs/tags/chart-v0.3.2' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com "$CHART_REF"
gh attestation verify "oci://$CHART_REF" --repo llm-measurement/otelcol-genai-sketches \
  --signer-workflow llm-measurement/otelcol-genai-sketches/.github/workflows/chart-release.yml \
  --cert-identity 'https://github.com/llm-measurement/otelcol-genai-sketches/.github/workflows/chart-release.yml@refs/tags/chart-v0.3.2' \
  --source-ref refs/tags/chart-v0.3.2
helm show chart ./otelcol-genai-sketches-0.3.2.tgz
helm template verify ./otelcol-genai-sketches-0.3.2.tgz
helm template verify ./otelcol-genai-sketches-0.3.2.tgz \
  --set prometheusRule.enabled=true --set prometheusRule.anomalies.enabled=true
```

On macOS, use `shasum -a 256 -c SHA256SUMS`. Verify the metadata says chart 0.3.2
and app 0.3.0, the rendered default deployment still uses image 0.3.0 (or your
explicit existing digest override), no default PrometheusRule appears, and opting
in adds the companion group. The chart certificate identity differs from the
runtime identity in [Deployment](DEPLOYMENT.md); do not substitute a chart tag
into the runtime-image verification command.
The [attestation verification flags](https://cli.github.com/manual/gh_attestation_verify)
bind both the signing workflow's certificate identity and the attested source ref
to this chart tag. A workflow path alone does not bind the release ref. Also use
`--source-digest "$RELEASE_COMMIT"` when the reviewed full commit SHA is available.

## Permissions And Limits

The publishing job explicitly requests `contents: write`, `packages: write`,
`id-token: write`, `attestations: write`, and `artifact-metadata: write`, plus
`actions: read`, matching the existing signed release path. Repository or
organization policies must permit these grants, and the chart package must allow
this repository's Actions token to write. Package linkage and inherited access are
described in [GitHub's package documentation](https://docs.github.com/en/packages/learn-github-packages/connecting-a-repository-to-a-package).
Public chart visibility is required for the anonymous-pull check. The tag pusher
needs repository/tag permission and a configured signing key; pushing with an
ordinary workflow `GITHUB_TOKEN` must not be assumed to trigger another workflow.

Preparation checks do not establish that publication permissions, OIDC signing,
attestation upload, or anonymous access to the new artifact will succeed. Confirm
the actual workflow result and digest after the release owner authorizes a push.
