# Changelog

Notable user-visible changes are recorded here.

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
