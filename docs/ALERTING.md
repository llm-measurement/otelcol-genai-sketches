# Prometheus Alerting

The Helm chart has optional accounting alerts and a separately opt-in anomaly
companion group. Both are disabled by default. These are conservative operational
heuristics, not statistically calibrated anomaly probabilities, confidence levels,
or evidence of token savings. Enabling them changes only the PrometheusRule; it
does not change the collector, add dependencies, or export identities.

## Enable From A Checkout

Use a values file so fractional thresholds retain their numeric types:

```yaml
prometheusRule:
  enabled: true
  sliceName: by_model
  anomalies:
    enabled: true
    window: 10m
    baselineWindow: 1h
    for: 10m
    minRecentAttempts: 100
    minBaselineAttempts: 1000
    attemptRateRatio: 3
    tokensPerAttemptRatio: 2
    missingUsageRatio: 0.20
    missingUsageIncrease: 0.10
```

```sh
helm upgrade --install genai-sketches deploy/helm/otelcol-genai-sketches \
  --namespace observability -f alerts-values.yaml
```

The Prometheus Operator CRDs must already exist, and its rule selector must select
this resource. Use `prometheusRule.labels` for any required resource-selection
labels. `serviceMonitor.enabled` remains a separate opt-in. Setting only
`anomalies.enabled` does nothing while `prometheusRule.enabled` is false. Keep the
companion disabled until you have checked the selected accounting population and
have enough history. These settings are included in chart 0.3.1, which continues
to use collector image 0.3.0. See [installation and verification](DEPLOYMENT.md).

## Signals And Guards

All companions use exactly one `prometheusRule.sliceName`, as the existing
accounting alerts do. They sum all its values, including overflow, across all
scraped collectors visible to the evaluating Prometheus. They do not produce
per-model, per-user, or per-session flags. Do not sum multiple slice views of the
same traffic. Use a single accounting source per observed attempt, avoid duplicate
scrapes, and install this total-view rule group once per accounting domain. A
chart release name does not isolate its metric selectors from other deployments.

The counter denominators are observed **model attempts**, not logical requests,
agent runs, users, or sessions. Failed attempts and retries can each count when
they emit matching spans. See [Accounting](ACCOUNTING.md) and [Metrics](METRICS.md).

| Alert | Default condition, sustained for `anomalies.for` |
| --- | --- |
| `GenAISketchModelAttemptRateAnomaly` | Recent attempt rate exceeds 3 times the baseline rate. |
| `GenAISketchReportedTokensPerAttemptAnomaly` | Recent reported tokens per attempt exceeds 2 times its positive baseline, with complete relevant usage coverage in both periods. |
| `GenAISketchMissingUsageShareAnomaly` | Recent missing-usage share exceeds 20%, and exceeds baseline by more than 10 percentage points. |

`window` is the recent range. `baselineWindow` is a separate historical range
with `offset` always equal to `window`: the defaults compare the last 10 minutes
with the hour ending 10 minutes ago. The current range is excluded, even after
changing the durations. Baselines move forward with time; sustained changes can
eventually become the baseline. No seasonality correction is implied.

Each counter uses `rate()` or `increase()` **before** aggregation, preserving
per-series reset handling on restart or slice recreation. Every rule requires
positive attempt rates and at least `minRecentAttempts` / `minBaselineAttempts`
in the two ranges. Every observed request series must have at least two samples.
Zero denominators and absent ranges suppress the companions; they are not clamped
to a fictitious request rate. The minimum-volume estimates use Prometheus's
extrapolated counter increases, not exact integer event counts.

For usage-share and token-per-attempt comparisons, missing-usage series must
match every request series' complete scrape label set and sample count, separately
in **both** ranges. Unmatched extra usage series also suppress the comparison.
One healthy collector's zero cannot stand in for another collector's absent
missing-usage counter. The token rule additionally requires matching token series
and zero missing-usage increase in both ranges. A missing or partially sampled
counter is unknown, never zero. Missing usage in the baseline blocks the token
signal even after recent coverage recovers.

These checks compare the observed series and sample counts; they cannot establish
coverage of a collector or attempts that never emitted telemetry, nor reconstruct
events lost between scrapes. A whole-population outage can suppress alerts.
Use separate scrape-health and telemetry-availability alerts. Long gaps, new
series, deployment changes, sparse data, and instrumentation changes warrant
inspection before acting; the volume guards do not certify a full history.

Reported tokens are input plus output. Cache-read, cache-write, and reasoning
subsets are not added again. Complete numeric usage does not mean provider-reported
or billing-equivalent usage: examine [provenance](USAGE_PROVENANCE.md) and the
existing token-quality alerts. Model mix, retries, and workload changes can explain
these total signals without abnormal behavior.

## Respond And Tune

Start with warning-only routing and inspect known quiet periods and known load
changes. Tune all thresholds, windows, minimum volumes, and persistence for the
accounting population before routing pages. Multipliers must exceed one; minimum
volumes and missing-share thresholds must be positive. The missing-share increase
is an absolute fraction, so `0.10` means ten percentage points, not ten percent
relative growth. A zero historical missing share is valid for this difference.

Check missing usage and scrape health first. A reduction in reported tokens while
coverage deteriorates is not savings; a recovery in coverage is not increased
consumption by itself. Keep coverage findings separate from token-efficiency or
savings claims. These companions signal increases only and do not certify normal
behavior when silent. Preserve the existing accounting alerts as separate guards.

For attribution, use bounded local inspection and the collector's opt-in private
top-k summaries. Local `fleetdiff scan` user/session flags, available in
**fleetdiff v0.5.0**, are a separate workflow, not labels or outputs of these
Prometheus rules. See [scan and retained history](https://github.com/llm-measurement/fleetdiff/blob/main/docs/SCAN.md).
No user/session/request IDs, prompt text, hashes, or top-k entries should be
added to chart metric or alert labels.

## Validation

The repository pins Helm 4.2.4 in CI and Prometheus 3.7.3 by image digest in the
root Makefile. Chart tests reuse Go's test runner and the existing YAML dependency.
The table fixtures in `deploy/helm/tests/anomaly_fixtures_test.go` are serialized to
native [promtool unit-test YAML](https://prometheus.io/docs/prometheus/latest/configuration/unit_testing_rules/)
and run against the actual rendered expressions, not a duplicate rules file.

```sh
helm lint deploy/helm/otelcol-genai-sketches
HELM="$(command -v helm)" PROMTOOL_DOCKER=1 \
  go test ./deploy/helm/tests -count=1 -v
```

`PROMTOOL_DOCKER=1` reuses the Makefile's exact image pin with networking disabled,
read-only mounts, and temporary in-memory test storage. Alternatively set
`PROMTOOL` to a local promtool executable. `HELM` can point to a cached executable.
Tests explicitly skip unavailable optional tools; a skip is not a semantic pass.
The existing `make helm-check` and `make prometheus-rule-check` continue to check
the default accounting configuration; run the command above for the opt-in group.

Fixtures cover quiet traffic, sustained spikes, persistence, zero and low volumes,
missing-usage changes, absent/partial coverage in either period, multiple
collectors, counter resets, scrape gaps, and isolation from another slice view.
Successful fixtures verify the configured mechanics, not calibration or detection
quality on production traffic. Validate locally first and leave the feature off
where the accounting and scrape-health assumptions cannot be established.
