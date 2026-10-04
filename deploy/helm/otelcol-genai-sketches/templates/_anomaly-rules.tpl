{{/* SPDX-License-Identifier: Apache-2.0 */}}
{{/* Code authors: Vijay and Codex */}}
{{/* Match complete scrape label sets before aggregation, including zero-valued peers. */}}
{{- define "otelcol-genai-sketches.anomalyCoverage" -}}
unless count(
  count_over_time({{ .requests }})
  unless (count_over_time({{ .peer }}) == count_over_time({{ .requests }}))
)
unless count(count_over_time({{ .peer }}) unless count_over_time({{ .requests }}))
{{- end }}

{{- define "otelcol-genai-sketches.anomalyRules" -}}
{{- $a := .Values.prometheusRule.anomalies -}}
{{- $selector := printf "{slice=%s}" (.Values.prometheusRule.sliceName | quote) -}}
{{- $recent := printf "%s[%s]" $selector $a.window -}}
{{- $baseline := printf "%s[%s] offset %s" $selector $a.baselineWindow $a.window -}}
{{- $requests := printf "gen_ai_sketch_requests_total%s" $recent -}}
{{- $baseRequests := printf "gen_ai_sketch_requests_total%s" $baseline -}}
{{- $rate := printf "sum(rate(%s))" $requests -}}
{{- $baseRate := printf "sum(rate(%s))" $baseRequests -}}
{{- $tokens := printf "gen_ai_sketch_total_tokens_total%s" $recent -}}
{{- $baseTokens := printf "gen_ai_sketch_total_tokens_total%s" $baseline -}}
{{- $missing := printf "gen_ai_sketch_missing_token_usage_total%s" $recent -}}
{{- $baseMissing := printf "gen_ai_sketch_missing_token_usage_total%s" $baseline -}}
{{- $volume := printf "and (%s > 0)\nand (%s > 0)\nand (sum(increase(%s)) >= %v)\nand (sum(increase(%s)) >= %v)\nunless count(count_over_time(%s) < 2)\nunless count(count_over_time(%s) < 2)" $rate $baseRate $requests $a.minRecentAttempts $baseRequests $a.minBaselineAttempts $requests $baseRequests -}}
{{- $missingCoverage := printf "%s\n%s" (include "otelcol-genai-sketches.anomalyCoverage" (dict "requests" $requests "peer" $missing)) (include "otelcol-genai-sketches.anomalyCoverage" (dict "requests" $baseRequests "peer" $baseMissing)) -}}
- name: otelcol-genai-sketches.anomalies
  rules:
    - alert: GenAISketchModelAttemptRateAnomaly
      expr: |
        {{ $rate }} > {{ $a.attemptRateRatio }} * {{ $baseRate }}
        {{- $volume | nindent 8 }}
      for: {{ $a.for }}
      labels: {severity: warning}
      annotations:
        summary: Observed model-attempt rate increased
        description: The selected slice view exceeds its historical attempt-rate multiplier. Investigate traffic, retries, and instrumentation; this is a heuristic total signal.
    - alert: GenAISketchReportedTokensPerAttemptAnomaly
      expr: |
        (sum(rate({{ $tokens }})) / {{ $rate }})
        > {{ $a.tokensPerAttemptRatio }} * (sum(rate({{ $baseTokens }})) / {{ $baseRate }})
        and (sum(rate({{ $baseTokens }})) > 0)
        and (sum(rate({{ $missing }})) == 0)
        and (sum(rate({{ $baseMissing }})) == 0)
        {{- $volume | nindent 8 }}
        {{- $missingCoverage | nindent 8 }}
        {{- include "otelcol-genai-sketches.anomalyCoverage" (dict "requests" $requests "peer" $tokens) | nindent 8 }}
        {{- include "otelcol-genai-sketches.anomalyCoverage" (dict "requests" $baseRequests "peer" $baseTokens) | nindent 8 }}
      for: {{ $a.for }}
      labels: {severity: warning}
      annotations:
        summary: Reported tokens per observed model attempt increased
        description: Both periods have matching token and missing-usage series with no observed missing usage. Investigate workload mix and provenance; this is not a billing or savings estimate.
    - alert: GenAISketchMissingUsageShareAnomaly
      expr: |
        (sum(rate({{ $missing }})) / {{ $rate }}) > {{ $a.missingUsageRatio }}
        and (
          (sum(rate({{ $missing }})) / {{ $rate }})
          - (sum(rate({{ $baseMissing }})) / {{ $baseRate }})
          > {{ $a.missingUsageIncrease }}
        )
        {{- $volume | nindent 8 }}
        {{- $missingCoverage | nindent 8 }}
      for: {{ $a.for }}
      labels: {severity: warning}
      annotations:
        summary: Missing-usage share increased
        description: Missing aggregate input or output usage exceeds both the share floor and the historical percentage-point increase. This is a coverage change, not token savings.
{{- end }}
