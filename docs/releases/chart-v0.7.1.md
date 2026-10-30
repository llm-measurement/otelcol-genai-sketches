# Helm Chart 0.7.1

Pin the collector v0.7.0 multi-architecture image index by default, so a fresh
installation selects immutable image content instead of the mutable `:0.7.0`
tag used by chart 0.7.0. Runtime behavior, chart templates and policy defaults
are unchanged from chart 0.7.0.

Draft for the later chart-only release; no image digest is available or invented
in this preparation. After v0.7.0 publication, set `image.digest` to the index
digest in its authenticated `image-digest.txt`, keep `image.tag` empty, set chart
version to `0.7.1`, and retain `appVersion: "0.7.0"`. Update the exact image and
chart-version test expectations in that reviewed pin PR.

The signed `chart-v0.7.1` tag uses `chart-release.yml`, which publishes the chart
without rebuilding the collector. Its signature and provenance identity differs
from chart 0.7.0, published by `release.yml` under `v0.7.0`. Verify the chart's
archive, OCI digest and attestation against its own tag and source commit.

Read the collector's [breaking changes](v0.7.0.md#breaking-changes) and
[chart 0.7.0 qualification](chart-v0.7.0.md#qualification) before upgrading.
This pin-only release adds no new breaking change. A retained old `image.digest`
or `image.tag` supplied through reuse-values can override new chart defaults;
inspect effective values and explicitly select the verified image index.

README and Deployment pin updates follow successful publication and artifact
verification in a separate PR. Then verify an anonymous installation following
those updated instructions. Do not overwrite chart 0.7.0 or move either tag.
