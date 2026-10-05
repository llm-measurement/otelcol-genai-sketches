# Changelog

Notable user-visible changes are recorded here.

## Unreleased

- Update source builds to OpenTelemetry Collector `v0.162.0` / pdata `v1.68.0`.
  Preserve the gRPC and crypto security pins. Review the upstream change to
  single-underscore Prometheus label names in [Upgrading](docs/UPGRADING.md).
  Published v0.3.0 images are unchanged.

## Helm chart 0.3.1 - 2026-10-04

See the [chart release notes](docs/releases/chart-v0.3.1.md). The collector runtime
and default image remain v0.3.0; no connector or accounting change is included.

- Add three opt-in Helm alert rules for model-attempt rate, reported tokens per
  attempt, and missing-usage share. Compare against an earlier baseline with
  volume guards, persistence, and matched usage coverage. Defaults remain off.
- Exercise the rendered alert rules against quiet, unusual, incomplete, reset,
  and scrape-gap fixtures with Prometheus in CI.
- Publish chart-only patches independently, with signed OCI artifacts and
  provenance, without rebuilding or retagging the collector image.

## v0.3.0 - 2026-10-01

See the [release notes](docs/releases/v0.3.0.md) for configuration and compatibility.

- Add opt-in user, session, and other hashed-key rankings with token or model-attempt
  weights. Preserve default export bytes and base accounting compatibility; new
  extraction-contract markers prevent incompatible rankings from combining.
  Keep all keys out of metric labels and retain the global snapshot limit.
- Configurations using `session.id` or `gen_ai.conversation.id` as slice keys now
  fail validation because they overlap the new default hashed session field,
  even when session top-k is disabled. Remove those identifiers from metric slices
  before upgrading; see [Upgrading](docs/UPGRADING.md#session-slice-labels-in-v030).
- Preflight cumulative counter and sketch limits across every destination before
  applying a span, including summary wire limits and slice eviction.
- Report the connector module version in component inventory and metric scope
  metadata. Add demo top-k output, configurable Helm rankings, and deployment
  and field-mapping examples.
- Preserve validated cached-input and reasoning-output subsets in the opt-in
  LiteLLM 1.102.1 callback for non-streaming OpenAI-compatible chat. Missing,
  invalid, or conflicting subsets are omitted; token totals are unchanged.
  Add callback-to-receiver regression fixtures and clarify retry accounting.
- Record full real-provider compatibility checks with a pinned GPT-5.4 snapshot,
  including preserved cache/reasoning subsets and interrupted-stream limitations.

## v0.2.0 - 2026-09-26

- Add a single-application LiteLLM investigation recipe and a version-pinned
  raw-response callback for non-streaming OpenAI-compatible chat. Streaming
  provenance remains unknown. Include source-boundary fixtures and privacy tests.
- Track declared input/output usage provenance with fixed labels and independently
  versioned summary counters. Explicitly unavailable fields count as missing and
  do not contribute tokens; genuine reported zeros remain valid. Unannotated
  traffic and the base summary accounting fingerprint are unchanged.
- Verify each published image by its platform manifest digest, supporting Docker's
  classic image store. Verification can be rerun without rebuilding or republishing
  release artifacts. This changes release automation, not collector behavior.
- Build on OpenTelemetry Collector `v0.161.0` / pdata `v1.67.0`.

The base accounting fingerprint is unchanged. Use fleetdiff v0.2.0 or later to
compare exports across the optional provenance extension. Enabling explicit
unavailable declarations can reduce counted tokens and increase missing coverage;
that instrumentation change is not evidence of savings. Streaming provenance
remains unknown. The OpenTelemetry connector component remains Alpha.

## v0.1.0 - 2026-09-05

- Added opt-in, bounded summary-file export for combining measurements across
  independently operated collectors. Files contain full sketch state and window
  counters, with producer epochs, compatibility metadata, and private permissions.
- Added two-collector reconciliation and privacy tests, including replay handling,
  missing producers, shared identities, and scans of decoded sketch payloads.
- Run the same summary-exchange test against published amd64 and arm64 containers.
- Updated the connector dependency to `llm-sketchkit v0.2.0` and aligned standalone
  connector, image, Helm, and deployment references at `0.1.0`.
- Updated Go runtime and gRPC dependencies while retaining OpenTelemetry Collector
  `v0.160.0`, and simplified connector lifecycle and slice handling.
- Added a Docker-only demo path and worked investigations for tool-span inflation
  and missing reported token usage.

The `genai-accounting/v1` contract and existing sketch encodings are unchanged.
Summary export is disabled by default and is not a recovery checkpoint or event
deduplication service. OpenTelemetry component stability remains Alpha; the version
number does not change the documented evaluation scope or promise 1.0 stability.

## v0.1.0-alpha.2 - 2026-09-03

- Added a production image, Helm chart, central and sidecar Kubernetes examples,
  accounting alerts, sizing guidance, upgrade guidance, and a support policy.
- Added multi-architecture release automation with image and chart signatures,
  SBOMs, provenance, checksums, license notices, license checks, and vulnerability
  scans.
- Added an anonymous-access check so a release fails if its image, chart, or release
  metadata cannot be fetched without repository credentials.

## connector/genaisketchconnector/v0.1.0-alpha.2 - 2026-09-03

- Defined the versioned `genai-accounting/v1` request and token contract, including
  ordered aliases, missing and invalid values, cache and reasoning subsets, and
  optional per-window request deduplication.
- Added exact reconciliation fixtures for current and legacy provider fields,
  lifecycle cases, conflicting values, retries, and deduplication.
- Added fixed-cardinality token quality metrics and separate cache-read,
  cache-write, reasoning, and deduplication counters.
- Added `topk: 0` to disable structured top-k logs and avoid allocating their
  frequent-items state while preserving counters and distinct estimates.

## connector/genaisketchconnector/v0.1.0-alpha.1 - 2026-08-21

- Published `genaisketchconnector` as an independently versioned Go module for use
  in custom OpenTelemetry Collector Builder distributions.
- Updated the connector and example distribution to OpenTelemetry Collector
  v0.159.0.

## v0.1.0-alpha.1 - 2026-08-04

- Added the `genaisketch` traces-to-metrics connector and custom collector
  distribution.
- Added bounded slice labels with deterministic inactive-slice eviction and a single
  overflow value.
- Added token totals, explicit missing-usage accounting, and operation-filtered
  request semantics for tree-shaped traces.
- Added keyed HLL++ distinct estimates for users, prompts, retrieval documents, and
  optional MCP fields.
- Added bounded weighted top-k structured summaries with lower and upper bounds.
- Added optional Bloom-filter request deduplication.
- Added a runnable OpenTelemetry, Prometheus, Grafana, and example-app stack.
- Added integration, privacy-sentinel, restart, locality, load, and sustained-run
  tests.
