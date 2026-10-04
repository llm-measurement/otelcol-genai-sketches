# Helm Chart 0.3.1

Chart version **0.3.1**, collector `appVersion` **0.3.0**. This is a chart-only
release: it does not build or publish a collector image or connector module.
The default image remains `ghcr.io/llm-measurement/otelcol-genai-sketches:0.3.0`.
Existing image tag/digest overrides remain effective.

## Changes

Opt-in Prometheus companion warnings compare total observed model-attempt rate,
reported tokens per attempt, and missing-usage share with a historical baseline.
Counters are rated before aggregation; the baseline excludes the recent period.
Minimum-volume, positive-denominator, persistence, and usage-coverage guards keep
sparse or incomplete observations from becoming token-per-attempt claims.

`prometheusRule.enabled` and `prometheusRule.anomalies.enabled` both remain false
by default. Read [Alerting](../ALERTING.md) before enabling either. These are
heuristic investigation signals, not calibrated anomaly probabilities or evidence
of token savings. Missing-usage changes describe coverage separately from token
consumption. No identity labels or runtime dependencies are added.

## Upgrade

Verify this chart's checksum, signature, and provenance using the chart-specific
identity in [Chart Releases](../CHART_RELEASE.md). Continue to obtain the collector
image digest and its signature from the existing `v0.3.0` runtime release.

```sh
helm upgrade --install genai-sketches \
  oci://ghcr.io/llm-measurement/charts/otelcol-genai-sketches \
  --version 0.3.1 --namespace observability -f existing-values.yaml
```

Preserve your existing secret and verified image-digest settings in that values
file. No warning is enabled by this upgrade alone. Prometheus Operator is required
only when opting into PrometheusRule resources. Local user/session scan flags are
a separate, unreleased source-checkout workflow; this chart does not release scan.

## Verification Scope

Helm rendering/schema checks and promtool fixtures exercise the rules, including
quiet series, spikes, missing usage, low volume, resets, and scrape gaps. Successful
fixtures establish rule mechanics, not production calibration. Collector behavior
and image artifacts remain those of `v0.3.0`.
