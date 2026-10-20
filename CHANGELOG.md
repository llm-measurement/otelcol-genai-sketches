# Changelog

Notable user-visible changes are recorded here.

## Unreleased - Helm Chart 0.7.0 (Collector 0.6.1)

- Enable NetworkPolicy by default in this next minor chart: allow same-namespace
  OTLP, require explicit metrics peers, and limit egress to cluster DNS plus
  configured rules. Add selected-peer presets and mTLS instructions.
- Pin the published collector 0.6.1 image by digest and warn on an explicit
  mutable-tag override. Enforce pod hardening with or without summary export and
  reject unknown top-level and security-related values.
- Review the [network policy migration](docs/DEPLOYMENT.md#chart-070-network-policy-migration)
  before upgrading; cross-namespace senders, Prometheus and shadow backends need
  explicit rules. No collector behavior or accounting contract changes.

## v0.6.1 / Helm chart 0.6.1 - 2026-10-18

See the [release notes](docs/releases/v0.6.1.md). Keep valid span accounting
available when input is malformed or counters are exhausted.

- Treat token fields above 1,000,000,000 as invalid, missing usage; retain
  requests and other valid fields. Saturate counters with explicit diagnostics.
- Reject malformed identities one span at a time with fixed reason labels,
  instead of rejecting valid peers in the batch.
- Apply every batch transactionally. Do not request trace replay after a
  downstream metric-delivery error once accounting has committed.
- Preserve normal-input output and existing identity and summary contracts.
  Exhausted summary windows and ranking bounds are explicitly unavailable.

## v0.6.0 / Helm chart 0.6.0 - 2026-10-18

See the [release notes](docs/releases/v0.6.0.md). Use one opt-in producer for
LiteLLM gateways serving Chat, Responses and Anthropic Messages traffic.

- Add `litellm-1.104.0-v2`, combining the four tested Chat, streaming Chat,
  Responses and Messages rules under a new model-usage contract.
- Add the standalone `litellm-1.104.0-messages-v1` profile. Nonstreaming Messages
  checks a present response model against the selected model; streaming Messages
  records gateway selection only.
- Preserve the v1 combined profile, all standalone contracts, default output,
  base accounting and summary formats. Chart 0.6.0 and collector v0.6.0 are
  published together; existing profiles do not change unless explicitly selected.

## v0.5.0 / Helm chart 0.5.0 - 2026-10-17

See the [release notes](docs/releases/v0.5.0.md). Pair with fleetdiff v0.11.0
and explicitly approve a new model-usage contract before enabling a profile.

- Attribute per-model summaries through exact approved served-model maps for
  pinned agentgateway, Envoy AI Gateway and LiteLLM versions, keeping unknown or
  conflicting identities visibly unpriced.
- Use one LiteLLM 1.104.0 profile for nonstreaming Chat, streaming Chat and
  Responses. Streaming Chat records gateway selection, not provider confirmation;
  Anthropic Messages remains excluded.
- Preserve existing defaults, metrics, base accounting and summary formats.
  Publish chart 0.5.0 with appVersion 0.5.0 through the runtime release, with no
  separate chart tag.

## v0.4.1 / Helm chart 0.4.1 - 2026-10-16

See the [release notes](docs/releases/v0.4.1.md). Completes the per-model summary
release with signed images, chart and attested OpenVEX evidence. Release tooling
extracts each architecture by its own immutable platform digest, avoiding a
Docker image-store collision. Runtime processing and the model-usage format are
unchanged from v0.4.0. The earlier image and tag are retained; its chart and
release assets were not published.

## v0.4.0 / Helm chart 0.4.0 - 2026-10-16

See the [release notes](docs/releases/v0.4.0.md). Upgrade fleetdiff readers to
v0.10.0 before enabling the optional model-usage export.

- Export bounded per-model input, output and cache components with request
  coverage, joint complete cohorts and versioned per-request input histograms.
- Require an explicit approved public model map and compatible contract ID.
  Preserve atomic batch updates, bounded retention and cumulative replay rules.
- Keep the new block off by default; existing metric and disabled-summary
  behavior remains unchanged.
- Publish a checksummed, attested OpenVEX assessment for GO-2026-5932 without
  suppressing the module-level warning from stripped-binary scans.

## v0.3.3 / Helm chart 0.3.5 - 2026-10-15

See the [release notes](docs/releases/v0.3.3.md). Chart 0.3.5 selects
collector v0.3.3; both are published by the runtime release.

- Verify `SHA256SUMS` and `sbom-index.json` with build-provenance attestations
  and a downloadable `provenance.jsonl` bundle. Check both subjects against the
  release workflow and exact tag before metadata upload.
- Follow deployment, summary export, and LiteLLM guides aligned with fleetdiff
  v0.8.0; retain 32 example windows
  for a 24-window baseline and two recent windows.
- Connect OpenInference / OpenLLMetry trace sources with the normalization
  recipe and source-specific own-traffic guidance. See the release notes
  for the recipe's tested scope.
- Keep the previous valid summary file when repeated export of nonempty
  observations within the same clock tick at startup or a new-window boundary
  fails explicitly; export retained measurements after time advances.
  llm-sketchkit v0.3.0 also rejects protobuf groups without changing canonical
  encodings. Runtime dependencies include Prometheus common v0.72.1.
- Check per-team token-budget burn with multiwindow alert rules. Exclude stale
  instances before team aggregation so a live peer cannot keep a stale peer's
  historical rate in the alert calculation. The recipe includes 23 rule cases
  and a real OTLP replay.
- Set collector and connector build metadata to v0.3.3 and chart 0.3.5,
  whose appVersion selects collector 0.3.3.

### Limits

- Identity change: standalone/custom builds moving from x/text v0.41.0 to
  v0.42.0 change eight of 8,254 pinned identities for U+11F41 combining-mark
  cases. Upgrade affected producers at a window boundary and keep old/new
  windows separate; follow the
  [migration procedure](docs/UPGRADING.md#sketchkit-030-in-this-source-candidate).
  The distribution moves from v0.42.0 to v0.43.0 with all 8,254 unchanged.
  Wire formats and accounting IDs cannot detect this boundary.

## v0.3.2 / Helm chart 0.3.4 - 2026-10-11

See the [release notes](docs/releases/v0.3.2.md). Chart 0.3.4 selects
collector v0.3.2; the connector module is released at v0.3.2 as well.

- Upgrade the connector and distribution to llm-sketchkit v0.2.3 for parser
  allocation limits, more efficient summary combination and safer secret/error
  diagnostics. Preserve each build's existing normalization dependency and
  pinned identities. The v0.3.0 identity migration remains separate; see
  [Upgrading](docs/UPGRADING.md#sketchkit-hardening-in-source-builds).

- Check canonical bytes and keyed hashes against a pinned identity corpus in
  both the connector and distribution dependency graphs. Changes to an existing
  baseline require a new identity-migration entry in this changelog.

- Include reviewed Go dependency updates, including gRPC v1.84.0, and the
  source-only event-time replay example for released coding-agent records.
  Replay requires the `tracelab_replay` build tag and is excluded from the
  default collector image.

## v0.3.1 / Helm chart 0.3.3 - 2026-10-09

- Release collector v0.3.1 and chart 0.3.3 with Go 1.26.9, x/net v0.60.0,
  and x/mod v0.41.0. See the [security release notes](docs/releases/v0.3.1.md)
  for advisory IDs and upgrade checks.
- Update source builds to OpenTelemetry Collector `v0.162.0` / pdata `v1.68.0`.
  Preserve the gRPC and crypto security pins. Review the upstream change to
  single-underscore Prometheus label names in [Upgrading](docs/UPGRADING.md).
  Published v0.3.0 images are unchanged.

## Helm chart 0.3.2 - 2026-10-06

See the [chart notes](docs/releases/chart-v0.3.2.md) and
[summary-export guide](docs/SUMMARY_EXPORT_HELM.md). The collector stays at
v0.3.0 and nothing else needs a release.

- Add opt-in summary export to a private `emptyDir` or operator-owned existing PVC,
  with explicit identities, bounded retention, and non-root initialization of the
  fixed export directory. Keep one replica and `Recreate`.
- Add an optional read-only `reader` sidecar for authenticated `kubectl exec` tar
  readback. It shares the pinned utility image with initialization, receives no
  hashing/TLS credentials or service-account token, and exposes no server.
- Allow internal-registry utility images with a required SHA-256 digest.
- Add independent JSON collector logging, with console logging still the default.
- Add a private copy helper that prints validated BEFORE, AFTER and AS_OF
  assignments for fleetdiff v0.5.0 investigation and scan commands.

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
