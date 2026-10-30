# Helm Chart 0.7.0

Deploy the collector with default OTLP network isolation, explicit metrics and
backend peers, optional digest-selected images, and hardened non-root pods. Selected-peer
values and mTLS configuration make the allowed traffic and client authentication
choices explicit.

Draft for release review. The `v0.7.0` runtime tag publishes the collector image
and this chart together through `release.yml`; there is no separate
`chart-v0.7.0` tag. Both image tag and digest values are empty, so the default
image is `ghcr.io/llm-measurement/otelcol-genai-sketches:0.7.0` via `appVersion`.
This matches the v0.6.2 release pattern. The tag is mutable; an explicit verified
index digest overrides it. Chart 0.7.1 will make that index pin the default.

Read the collector release's single
[breaking-changes section](v0.7.0.md#breaking-changes) before upgrading, including
the private-key requirement, plain-upgrade behavior, `--reuse-values`, and the
explicit NetworkPolicy opt-out.

## Qualification

Helm qualification ran on linux/arm64. Attempt 9 passed all 49 five-trial flow
cases with exact assertions, the 600-second stability check, an actual shadow
receipt, and both upgrade paths. Owned resources were removed and cleanup was
independently verified. This qualifies the tested collector/chart package, not
an as-yet-unpublished multi-architecture image or a different chart tree.

The release equality check covers every chart file and chart test against the
qualified candidate. Allowed differences are Chart.yaml version/appVersion,
values.yaml image fields, corresponding image/version test expectations, and
removal of the private-candidate NOTES line. Runtime Go sources, module files
and builder inputs must remain identical to the qualified runtime source.

## Install

After publication, follow [Deployment](../DEPLOYMENT.md#central-collector-with-helm)
from the README, explicitly selecting chart version `0.7.0` and, when required,
its verified collector index digest until the public pins are updated. Use an
existing private Secret. Verify anonymous access and installation
from a fresh clone, with no local candidate files or registry credentials.

The order is `v0.7.0` (image plus chart 0.7.0), verified published image index,
reviewed chart 0.7.1 pin, then `chart-v0.7.1` through `chart-release.yml`.
README/Deployment pin updates follow in a separate reviewed PR. Published tags
and chart versions are never reused or replaced.
