# Helm Chart 0.8.0

This stable chart pins the published collector v0.8.0 multi-architecture image
index. App version is 0.8.0. Chart configuration and feature defaults remain
unchanged from intermediate chart 0.8.0-rc.1.

```text
ghcr.io/llm-measurement/otelcol-genai-sketches@sha256:0c46145b2d0924010c49659013ba02eddc61fb82bb3476ffd52514e5b7f5522a
```

The [runtime release](v0.8.0.md) adds opt-in root-invocation accounting for
workflow and framework entrypoints and updates the GenAI semantic-convention
guidance to v0.1.0. The distribution retains the security dependencies and
Debian 13 nonroot runtime from v0.7.4.

Stable chart 0.8.0 uses its own signed `chart-v0.8.0` tag and
`chart-release.yml` identity. It does not rebuild or republish the runtime image.
Verify chart provenance and the runtime image signature separately, using
their respective workflow identities and exact digests.
