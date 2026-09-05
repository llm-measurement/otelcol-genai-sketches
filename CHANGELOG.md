# Changelog

Notable user-visible changes are recorded here.

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
