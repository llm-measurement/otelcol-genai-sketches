# Helm chart 0.7.5

This stable chart pins the published collector v0.7.4 multi-architecture image
index. App version is 0.7.4. No changes to chart configuration or feature defaults.

```text
ghcr.io/llm-measurement/otelcol-genai-sketches@sha256:c295dfdd4174c2d0c86954f979295a80f4f96fc24a993ed333c848ccaa5c6cf6
```

The [runtime release](v0.7.4.md) updates the OpenTelemetry Go modules to v1.47.0
for CVE-2026-128085 / GO-2026-6974 and uses the digest-pinned Debian 13 nonroot
runtime. Stable chart 0.7.5 uses its own signed `chart-v0.7.5` tag and
`chart-release.yml` identity. It does not rebuild or republish the runtime image.

Verify chart provenance and the runtime image signature separately, using
their respective workflow identities and exact digests. README and site
pointer updates follow independent verification and an outsider install.
