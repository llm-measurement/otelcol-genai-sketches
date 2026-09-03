# Release And Support Policy

`otelcol-genai-sketches` is pre-1.0 software. Pin an exact release, image digest,
and chart version. Test upgrades with representative telemetry before replacing a
running collector.

## Supported Release Line

The latest published release is the only supported release line. Older pre-1.0
releases and untagged commits receive no routine fixes. Support means that accepted
correctness and security defects are assessed against the latest release and fixed
when practical; it does not include a response-time or availability guarantee.

Every tagged release is expected to include:

- release notes and immutable source history;
- amd64 and arm64 image manifests with immutable digests;
- keyless image signatures, build provenance, and an SBOM;
- a versioned Helm chart and checksums; and
- the OpenTelemetry Collector compatibility version in the upgrade guide.

A tag whose release workflow fails is not a supported release. Verify the image
signature and provenance before deployment.

## Compatibility

The accounting contract is versioned separately from the package version. A release
that changes request scope, token arithmetic, alias precedence, missing-usage rules,
deduplication, or attribution must introduce a new contract version and migration
note. Additive metrics may remain within the current contract when existing meanings
do not change.

The current Collector, Kubernetes, image, and configuration compatibility details
are maintained in [Upgrading](docs/UPGRADING.md).

## Security Fixes

Security fixes target the latest supported release. A confirmed high-severity issue
may result in a patched release or a published mitigation, depending on the affected
component and available upstream fix. Reports are coordinated privately as described
in [Security](SECURITY.md).
