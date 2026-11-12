# Helm chart 0.7.4

This stable chart pins the published collector v0.7.3 multi-architecture image
index. App version is 0.7.3. No changes to chart configuration or feature defaults.

```text
ghcr.io/llm-measurement/otelcol-genai-sketches@sha256:64a8002d2bd524772d00a60c8482bf76152731bf2792c948d0a25994a6e5487d
```

The [runtime release](v0.7.3.md) keeps Go 1.26.11 for GO-2026-6859 and updates
the effective x/crypto version to v0.59.0 for GO-2026-5932. v0.7.2 was tagged
but never published. Stable chart 0.7.4 uses its own signed `chart-v0.7.4`
tag and `chart-release.yml` identity. It does not rebuild or republish the
runtime image.

Verify chart provenance and the runtime image signature separately, using
their respective workflow identities and exact digests. README and site
pointer updates follow independent verification and an outsider install.
