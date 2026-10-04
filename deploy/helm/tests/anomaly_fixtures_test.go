// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex
package charttest

import "strings"

type series struct {
	Series string `yaml:"series"`
	Values string `yaml:"values"`
}

// These table fixtures are serialized to promtool's native unit-test YAML.
// One-minute scrapes: 51 baseline samples, then six recent samples.
func promtoolFixtures(rules []rule) map[string]any {
	const attempt = "GenAISketchModelAttemptRateAnomaly"
	const token = "GenAISketchReportedTokensPerAttemptAnomaly"
	const missing = "GenAISketchMissingUsageShareAnomaly"
	const requestMetric = "gen_ai_sketch_requests_total"
	const tokenMetric = "gen_ai_sketch_total_tokens_total"
	const missingMetric = "gen_ai_sketch_missing_token_usage_total"
	const labels = `{slice="by_model",slice_value="model=a",overflow="false",instance="one"}`
	quiet := []series{
		{requestMetric + labels, "0+60x56"},
		{tokenMetric + labels, "0+6000x56"},
		{missingMetric + labels, "0+0x56"},
	}
	spike := []series{
		quiet[0],
		{tokenMetric + labels, "0+6000x50 324000+24000x5"},
		quiet[2],
	}
	copyWith := func(base []series, index int, values string) []series {
		result := append([]series(nil), base...)
		result[index].Values = values
		return result
	}
	attemptSpike := copyWith(quiet, 0, "0+60x50 3300+300x5")
	attemptSpike[1].Values = "0+6000x50 330000+30000x5"
	missingSpike := copyWith(quiet, 2, "0+0x50 40+40x5")
	partial := append([]series(nil), spike...)
	for _, s := range spike[:2] {
		partial = append(partial, series{strings.Replace(s.Series, `instance="one"`, `instance="two"`, 1), s.Values})
	}
	partialShare := append([]series(nil), missingSpike...)
	partialShare = append(partialShare, series{strings.Replace(quiet[0].Series, `instance="one"`, `instance="two"`, 1), quiet[0].Values})
	extraMissing := append([]series(nil), missingSpike...)
	extraMissing = append(extraMissing, series{strings.Replace(quiet[2].Series, `instance="one"`, `instance="two"`, 1), "0+40x56"})
	partialTokens := append([]series(nil), spike...)
	for _, s := range []series{quiet[0], quiet[2]} {
		partialTokens = append(partialTokens, series{strings.Replace(s.Series, `instance="one"`, `instance="two"`, 1), s.Values})
	}
	offsetSpike := copyWith(attemptSpike, 0, "0+60x50 3240+240x5")
	offsetSpike[1].Values = "0+6000x50 324000+24000x5"
	reset := []series{
		{requestMetric + labels, "0+60x52 60+60x3"},
		{tokenMetric + labels, "0+6000x52 6000+6000x3"},
		quiet[2],
	}
	for _, s := range quiet {
		reset = append(reset, series{strings.Replace(s.Series, `instance="one"`, `instance="two"`, 1), s.Values})
	}
	otherView := append([]series(nil), quiet...)
	for _, s := range attemptSpike {
		otherView = append(otherView, series{strings.Replace(s.Series, `slice="by_model"`, `slice="by_team_model"`, 1), s.Values})
	}
	gap := make([]series, len(quiet))
	for i, s := range quiet {
		gap[i] = series{s.Series, s.Values[:strings.LastIndex(s.Values, "x")+1] + "50 " + strings.Repeat("_ ", 6)}
	}
	cases := []struct {
		name string
		data []series
		want string
	}{
		{"quiet", quiet, ""},
		{"attempt-rate-spike", attemptSpike, attempt},
		{"reported-tokens-per-attempt-spike", spike, token},
		{"missing-usage-spike-from-zero-baseline", missingSpike, missing},
		{"stable-high-missing-share", copyWith(quiet, 2, "0+40x56"), ""},
		{"share-floor-not-reached", copyWith(quiet, 2, "0+0x50 10+10x5"), ""},
		{"share-increase-not-reached", copyWith(quiet, 2, "0+15x50 768+18x5"), ""},
		{"low-recent-volume", copyWith(spike, 0, "0+60x50 3010+10x5"), ""},
		{"low-baseline-volume", copyWith(spike, 0, "0+1x50 110+60x5"), ""},
		{"zero-recent-denominator", copyWith(spike, 0, "0+60x50 3000+0x5"), ""},
		{"zero-baseline-denominator", copyWith(spike, 0, "0+0x51 60+60x4"), ""},
		{"zero-baseline-tokens", copyWith(quiet, 1, "0+0x51 24000+24000x4"), ""},
		{"all-zero-counters", copyWith(copyWith(quiet, 0, "0+0x56"), 1, "0+0x56"), ""},
		{"missing-usage-recent", copyWith(spike, 2, "0+0x50 1+1x5"), ""},
		{"missing-usage-baseline-only", copyWith(spike, 2, "0+1x50 50+0x5"), ""},
		{"missing-counter-absent", spike[:2], ""},
		{"missing-counter-recent-absent", copyWith(spike, 2, "0+0x50 "+strings.Repeat("_ ", 6)), ""},
		{"missing-counter-baseline-absent", copyWith(spike, 2, strings.Repeat("_ ", 51)+"0+0x5"), ""},
		{"missing-counter-recent-gap", copyWith(spike, 2, "0+0x53 _ 0 0"), ""},
		{"missing-counter-baseline-gap", copyWith(spike, 2, "0+0x29 _ 0+0x25"), ""},
		{"one-collector-missing-coverage", partial, ""},
		{"share-with-partial-coverage", partialShare, ""},
		{"extra-missing-counter-without-denominator", extraMissing, ""},
		{"one-collector-token-counter-absent", partialTokens, ""},
		{"token-counter-recent-gap", copyWith(spike, 1, "0+6000x50 324000+24000x2 _ 420000 444000"), ""},
		{"token-counter-baseline-gap", copyWith(spike, 1, "0+6000x29 _ 186000+6000x19 324000+24000x5"), ""},
		{"counter-reset-with-another-growing-instance", reset, ""},
		{"another-slice-view-is-not-double-counted", otherView, ""},
		{"offset-keeps-recent-spike-out-of-baseline", offsetSpike, attempt},
		{"scrape-gap", gap, ""},
		{"no-telemetry", nil, ""},
	}
	var tests []any
	for _, c := range cases {
		var checks []any
		for _, r := range rules {
			expected := []any{}
			if c.want == r.Alert {
				expected = append(expected, map[string]any{"exp_labels": r.Labels, "exp_annotations": r.Annotations})
			}
			checks = append(checks, map[string]any{"eval_time": "56m", "alertname": r.Alert, "exp_alerts": expected})
		}
		if c.want == attempt {
			checks = append(checks, map[string]any{"eval_time": "53m", "alertname": attempt, "exp_alerts": []any{}})
		}
		tests = append(tests, map[string]any{"name": c.name, "interval": "1m", "input_series": c.data, "alert_rule_test": checks})
	}
	return map[string]any{"rule_files": []string{"rules.yaml"}, "evaluation_interval": "1m", "tests": tests}
}
