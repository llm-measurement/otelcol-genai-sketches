# Release OpenVEX

Collector v0.4.1 adds `openvex.json` and `vex-linux-{amd64,arm64}.json`
to its release assets. Verify the published files before using the assessment.
Earlier releases do not acquire it retroactively.

For [GO-2026-5932](https://vuln.go.dev/ID/GO-2026-5932.json), the generator may
emit `not_affected` / `vulnerable_code_not_present` only when both generated
Linux import graphs omit `golang.org/x/crypto/openpgp` and all its subpackages.
It uses the production Dockerfile's build stage, target platform and build tags,
and requires the resulting binary hashes to match binaries extracted from the
published image digest. Evidence includes graph, source/module/manifest hashes,
Go version and the retained crypto module version. Missing, inconsistent or
incomplete evidence fails the release step; it does not produce a fallback claim.

The module remains in the distribution, so its module-level warning remains
visible. No scanner ignore rule, severity change or suppression is added.
The statement covers only the identified image's Linux amd64/arm64 collector,
not custom builds, additional components or unrelated vulnerabilities.

## Review a module-level finding

A stripped binary may expose module versions but not enough symbols to tell
whether an affected package was linked. This is a reason to investigate, not a
reason to ignore a failed scan. The release job scans the generated distribution
at package level for both Linux targets and scans each exact shipped binary in
binary mode. Both scans block publication on failure.

For a future suspected module-level false positive:

1. Retain the original scan output, advisory version and affected package list.
   Regenerate the distribution for both Linux targets using the same manifest,
   Go version, CGO setting and build tags as the image. Match the generated
   binary hashes to those extracted from the exact staged image digest.
2. Retain both full import graphs and both source package scan results. Reject
   a `not_affected` claim if any affected package is present, the advisory does
   not identify the affected packages, or any required evidence is missing.
3. Submit the advisory, graphs, scanner outputs and digest-bound evidence for
   review. Only a separately reviewed, advisory-specific release change may
   use a generated `not_affected` statement with
   `vulnerable_code_not_present` for that exact product digest. Keep the raw
   finding visible and authenticate the statement and graph receipts with the
   release attestations. Do not hand-edit a statement, ignore a scanner exit,
   or treat the assessment as a reusable waiver.

After the release has published these assets, use its exact tag and verify the
image signature/provenance as described in [Deployment](DEPLOYMENT.md). Then
verify the VEX asset's own attestation, as well as the checksum manifest:

```bash
: "${RELEASE:?set the exact runtime release tag that carries OpenVEX}"
mkdir -p release-assets
gh release download "$RELEASE" --repo llm-measurement/otelcol-genai-sketches \
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

The graph files are authenticated through `SHA256SUMS`; `openvex.json` is also
an attestation subject itself. Check that its product digest matches
`image-digest.txt` before using the assessment. The document uses the
[OpenVEX v0.2.0 specification](https://raw.githubusercontent.com/openvex/spec/main/OPENVEX-SPEC.md)
and a content-addressed document ID for each issued snapshot.
