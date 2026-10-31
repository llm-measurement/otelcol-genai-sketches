<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Code authors: Vijay and Codex -->

# Helm Chart 0.7.1

Chart 0.7.1 pins collector v0.7.0 by its multi-architecture image index digest,
`sha256:FILL_FROM_v0.7.0_image-digest.txt`, so default installations select immutable
image content. The templates, policy defaults and collector are the same as in chart
0.7.0. If you're upgrading from an earlier chart, read the
[v0.7.0 breaking changes](v0.7.0.md#breaking-changes) first.

## Upgrade

A plain `helm upgrade` selects the pinned digest. With `--reuse-values`, prior image values are retained: a nonempty
`image.digest` wins; an empty digest uses `image.tag`, or `appVersion` when the
tag is also empty. Check `helm get values --all` and set `image.digest` explicitly
to select the published index.

## Verify

Verify the chart's OCI digest, signature and provenance against the
`chart-v0.7.1` tag, as described in
[Deployment](../DEPLOYMENT.md#verify-an-image).
