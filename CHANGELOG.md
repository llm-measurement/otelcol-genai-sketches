# Changelog

Notable user-visible changes are recorded here.

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
