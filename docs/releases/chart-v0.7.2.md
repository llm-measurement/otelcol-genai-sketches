# Security fix: updates two dependencies with published advisories

Helm chart 0.7.2 pins collector v0.7.1 by its published multi-architecture image
index digest. No breaking changes from chart 0.7.1; feature defaults and
configuration are unchanged. App version is 0.7.1.

```text
ghcr.io/llm-measurement/otelcol-genai-sketches@sha256:0bae7fb80c4bd28db7c1a5746961a0f9907eb97975f69c35a866fd2ae0af5e8e
```

The collector image updates `klauspost/compress` to v1.20.2 for
[GO-2026-6865](https://pkg.go.dev/vuln/GO-2026-6865), and `golang.org/x/net` to
v0.62.0 for CVE-2026-78661 /
[GO-2026-6860](https://pkg.go.dev/vuln/GO-2026-6860). The advisory and reachability
limits in the [runtime notes](v0.7.1.md) apply; this is not a claim of demonstrated
exploitation.

This chart does not build or republish the runtime image. Its signed
`chart-v0.7.2` tag runs `chart-release.yml`, with a different signing identity from
the runtime's `release.yml@refs/tags/v0.7.1`. Verify the chart signature,
attestation and digest with the chart workflow identity, and the image signature
with the runtime workflow identity.

Independent verification and an outsider anonymous pull/install check precede
changes to README or site version pointers. The v0.7.0 upgrade requirements still
apply when moving from an earlier minor release.
