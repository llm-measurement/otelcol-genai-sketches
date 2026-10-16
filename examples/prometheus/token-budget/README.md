# Per-Team Token-Budget Burn Alerts

**Result:** the synthetic replay produces 20x burn for `platform` and 1x for
`inference`; only `platform` fires the two burn alerts. Twenty-two positive and
negative promtool cases cover the rule logic. The recipe uses reported-token
counters and a user-supplied allowance.

## Setup

### Configure The Team Slice

The collector emits `gen_ai_sketch_total_tokens_total` with `slice`, `slice_value`,
and `overflow`, plus exporter scope labels. Prometheus adds `job` and `instance`.
It does **not** emit a `team` label. For the single-key slice in
[collector.fragment.yaml](collector.fragment.yaml), a team appears as:

```promql
gen_ai_sketch_total_tokens_total{slice="by_team",slice_value="team.id=platform",overflow="false"}
```

Merge the fragment into your existing connector configuration, retaining the
existing receiver, hashing configuration, pipelines, and exporter. Supply a
bounded, non-sensitive `team.id` on model spans or their resources. The span value
takes precedence. Keep `max_slices` within the existing capacity limit. The default
demo's `by_team_model` slice is a different composite view; this recipe does not
parse that string or infer a team from model, user, session, or service identity.

Use `translation_strategy: UnderscoreEscapingWithoutSuffixes` in the Prometheus
exporter, as in the existing examples, to preserve these exact metric names.
Confirm the scrape before installing the rules. The selected job must represent
one budget domain. Its instances must process disjoint traffic; deduplicate HA
replicas upstream rather than summing two copies of the same workload. The query
applies `rate()` before summing instances, handles counter resets, and selects only
`by_team`, never adding the same tokens again from another slice.

### Supply Your Budget

Copy [budgets.example.yml](budgets.example.yml) to your Prometheus rule directory
as `budgets.yml`. Replace both synthetic teams and amounts with your own
**30-day token allowances**. Keep exactly one finite positive number per team.
These budget series are user-configured recording rules, not collector output.
Do not put user IDs, account IDs, or other unbounded identities in this file.

For example, `vector(2592000)` means 2,592,000 tokens per 30 days, or 1 token/sec.
Match `slice="by_team"` and the complete emitted `slice_value`, including `team.id=`.
Copy [rules.yml](rules.yml) beside it and add both files to your existing
Prometheus configuration:

```yaml
rule_files:
  - /etc/prometheus/budgets.yml
  - /etc/prometheus/rules.yml
```

The selectors use the existing example scrape job `otelcol-genai-sketches`.
Change that selector consistently if your scrape job differs; do not broaden it
to unrelated environments. Keep the rule group's one-minute evaluation interval
and scrape at least once per minute. Allow a full six hours of usable history
before interpreting both tiers: startup gaps affect range-rate extrapolation.
Budget-group changes may take an evaluation interval to reach the burn group.

## Alert Policy

`burn(window) = reported tokens/sec over window / (30-day allowance / 2592000)`.
Input plus output tokens already make up the total; cache and reasoning subsets
are not added again.

| Alert | Both windows must exceed | Meaning with full history |
| --- | --- | --- |
| `GenAITokenBudgetFastBurn` / critical | 14.4x over 1h AND 5m | Above 2% of the allowance per hour |
| `GenAITokenBudgetSlowBurn` / warning | 6x over 6h AND 30m | Above 5% of the allowance per six hours |

Threshold equality does not fire. The short window confirms ongoing burn; no
extra `for` delay is added. Both tiers can fire together. In Alertmanager, group
by `slice` and `slice_value` and inhibit the warning when the matching critical
alert is firing. Notification routing is intentionally left to the operator.

An active team with a missing, nonpositive, nonfinite, or duplicate budget raises
`GenAITokenBudgetMissing` instead of a fabricated burn rate. Traffic in overflow
or the `<missing>` / `<too_long>` team buckets raises
`GenAITokenBudgetUnattributed`; it is never allocated to a named team. Stale
counter series are removed from burn calculations by the current-series guard.
Absent telemetry stays absent, not zero.

The window-pair structure follows the
[Google SRE multiwindow, multi-burn-rate method](https://sre.google/workbook/alerting-on-slos/#6-multiwindow-multi-burn-rate-alerts),
with a token allowance as the denominator rather than an error-budget SLO.
See [Metrics](../../../docs/METRICS.md) and
[Accounting](../../../docs/ACCOUNTING.md) for the underlying counter semantics.

## Reproduce

From the repository root, using the existing Go and Docker toolchains:

```sh
make token-budget-check
make token-budget-replay
```

The first target uses the repository-pinned Prometheus image to validate both
rule files and run 22 deterministic cases without network access inside the
container. Docker may fetch the pinned image if it is not already cached.
The second builds the checked-out collector, runs the real OTLP/gRPC test, and
runs promtool against its measured counter timeline. No provider calls or API
keys are used; the collector uses the existing public integration-test hashing
placeholder, not an operator secret.

The replay records 361 checkpoints with 1,200 versus 60 tokens per synthetic
minute. Final team totals are 432,000 and 21,600. `.cache/token-budget/` contains
the effective test configuration, final scrape, and generated `replay.test.yml`.
Promtool asserts all four window rates and the exact firing team/alert labels.
The collector-side Go test alone is not the complete rule-engine check.

The static cases cover fast/slow burn, normal/idle traffic, short bursts,
recovery, exact thresholds, resets, disjoint instances, independent team budgets,
missing/zero/negative/infinite/NaN/duplicate budgets, wrong jobs/slices, overflow,
missing/oversized teams, absent telemetry, and staleness. See
[Prometheus rule testing](https://prometheus.io/docs/prometheus/latest/configuration/unit_testing_rules/)
for the fixture format.

## Limits

This measures budget pace, not remaining balance, calendar-month spend, invoice
cost, enforcement, or task value. Missing usage, sampling, ingestion loss,
deduplication, and slice overflow can undercount or lose attribution. No burn
alert does not prove a team is within budget; sustained burn below these
thresholds also does not alert. Keep collector/scrape health and the existing
accounting-quality alerts enabled.

The replay uses real OTLP/gRPC and measured counters on an accelerated, synthetic
six-hour timeline, not six hours of production traffic. Trial user-supplied
budgets with existing accounting and health alerts before connecting notification
routes; production policy calibration remains operator-owned.
