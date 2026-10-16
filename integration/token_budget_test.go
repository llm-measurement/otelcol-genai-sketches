//go:build integration

// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package integration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestTokenBudgetRecipeReplay(t *testing.T) {
	repo := repoRoot(t)
	binary := filepath.Join(repo, "dist", "otelcol-genai-sketches")
	requireExecutable(t, binary)
	output := filepath.Join(repo, ".cache", "token-budget")
	if err := os.MkdirAll(output, 0o700); err != nil {
		t.Fatal(err)
	}
	otlpPort, promPort := freePort(t), freePort(t)
	configPath := writeCollectorConfig(t, otlpPort, promPort)
	readYAML := func(path string) map[string]any {
		t.Helper()
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var config map[string]any
		if err := yaml.Unmarshal(body, &config); err != nil {
			t.Fatal(err)
		}
		return config
	}
	config := readYAML(configPath)
	connector := config["connectors"].(map[string]any)["genaisketch"].(map[string]any)
	fragment := readYAML(filepath.Join(repo, "examples", "prometheus", "token-budget", "collector.fragment.yaml"))
	for key, value := range fragment["connectors"].(map[string]any)["genaisketch"].(map[string]any) {
		connector[key] = value
	}
	connector["topk"] = 0
	// A second view verifies that the recipe selects one slice, not all totals.
	connector["slices"] = append(connector["slices"].([]any), map[string]any{
		"name": "by_model", "keys": []string{"gen_ai.request.model"},
	})
	encoded, err := yaml.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(output, "collector.yaml"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd, logs := startCollector(t, ctx, binary, configPath)
	waitForProcessAlive(t, cmd, logs)

	values := map[string][]string{"platform": {}, "inference": {}}
	var finalScrape string
	// Accelerated replay: each acknowledged batch represents one synthetic minute.
	// Only measured collector counters, not invented counters, enter the rule test.
	for minute := 0; minute <= 360; minute++ {
		platform, inference := int64(1200), int64(60)
		if minute == 0 {
			platform, inference = 0, 0
		}
		sendTraceSpecsEventually(t, otlpPort,
			otlpSpanSpec{Operation: "chat", Model: "synthetic", Team: "platform", InputTokens: intPtr(platform), OutputTokens: intPtr(0)},
			otlpSpanSpec{Operation: "chat", Model: "synthetic", Team: "inference", InputTokens: intPtr(inference), OutputTokens: intPtr(0)},
		)
		pattern := regexp.MustCompile(fmt.Sprintf(`(?m)^gen_ai_sketch_total_tokens_total\{[^}]*slice_value="team\.id=platform"[^}]*\}\s+%d$`, minute*1200))
		body := scrapeEventually(t, promPort, pattern)
		for team, step := range map[string]int{"platform": 1200, "inference": 60} {
			labels := map[string]string{"slice": "by_team", "slice_value": "team.id=" + team, "overflow": "false"}
			got := mustMetricValue(t, body, "gen_ai_sketch_total_tokens_total", labels)
			if got != float64(minute*step) {
				t.Fatalf("minute %d team %s: got %v, want %d", minute, team, got, minute*step)
			}
			values[team] = append(values[team], strconv.FormatFloat(got, 'f', -1, 64))
		}
		finalScrape = body
	}
	for _, sample := range prometheusMetricSamples(t, finalScrape, "gen_ai_sketch_total_tokens_total") {
		if _, invented := sample.labels["team"]; invented {
			t.Fatal("recipe must not assume a collector team label")
		}
		t.Logf("emitted total_tokens labels=%v value=%g", sample.labels, sample.value)
	}
	if err := os.WriteFile(filepath.Join(output, "scrape.prom"), []byte(finalScrape), 0o600); err != nil {
		t.Fatal(err)
	}
	series := []map[string]any{}
	for _, team := range []string{"platform", "inference"} {
		series = append(series, map[string]any{
			"series": fmt.Sprintf(`gen_ai_sketch_total_tokens_total{job="otelcol-genai-sketches",slice="by_team",slice_value="team.id=%s",overflow="false"}`, team),
			"values": strings.Join(values[team], " "),
		})
	}
	checks := []map[string]any{}
	for _, window := range []string{"5m", "1h", "30m", "6h"} {
		name := "gen_ai_token_budget:burn_rate" + window
		samples := []map[string]any{}
		for _, team := range []string{"platform", "inference"} {
			want := 1
			if team == "platform" {
				want = 20
			}
			samples = append(samples, map[string]any{
				"labels": fmt.Sprintf(`{__name__="%s",slice="by_team",slice_value="team.id=%s"}`, name, team),
				"value":  want,
			})
		}
		checks = append(checks, map[string]any{"expr": name, "eval_time": "6h", "exp_samples": samples})
	}
	alerts := []map[string]any{}
	for _, tier := range []struct{ name, severity, summary, description string }{
		{"FastBurn", "critical", "Fast token-budget burn for team.id=platform", "Reported token rate exceeds 14.4 times the 30-day allowance in both the 1h and 5m windows."},
		{"SlowBurn", "warning", "Slow token-budget burn for team.id=platform", "Reported token rate exceeds 6 times the 30-day allowance in both the 6h and 30m windows."},
	} {
		alerts = append(alerts, map[string]any{
			"eval_time": "6h", "alertname": "GenAITokenBudget" + tier.name,
			"exp_alerts": []map[string]any{{
				"exp_labels":      map[string]string{"slice": "by_team", "slice_value": "team.id=platform", "severity": tier.severity},
				"exp_annotations": map[string]string{"summary": tier.summary, "description": tier.description},
			}},
		})
	}
	tests := map[string]any{
		"rule_files":          []string{"../../examples/prometheus/token-budget/budgets.example.yml", "../../examples/prometheus/token-budget/rules.yml"},
		"evaluation_interval": "1m", "fuzzy_compare": true,
		"group_eval_order": []string{"genai-token-budget-inputs", "genai-token-budget"},
		"tests":            []map[string]any{{"name": "real OTLP counters on a synthetic minute timeline", "interval": "1m", "input_series": series, "promql_expr_test": checks, "alert_rule_test": alerts}},
	}
	encoded, err = yaml.Marshal(tests)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(output, "replay.test.yml"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Log("361 measured checkpoints: platform=432000 tokens, inference=21600 tokens; replay expects 20x vs 1x burn")
	t.Log("Next: promtool test rules .cache/token-budget/replay.test.yml (separate required rule-engine check)")
}
