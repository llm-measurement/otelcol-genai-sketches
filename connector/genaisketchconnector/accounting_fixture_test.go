// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay Erramilli and Codex
package genaisketchconnector

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.opentelemetry.io/collector/pdata/pmetric"
)

const accountingFixtureSchema = "genai-accounting-reconciliation/v1"

var accountingMetricNames = []string{
	requestsMetricName,
	agentRunsMetricName,
	inputTokensMetricName,
	outputTokensMetricName,
	totalTokensMetricName,
	cacheReadInputTokensMetricName,
	cacheWriteInputTokensMetricName,
	reasoningOutputTokensMetricName,
	missingTokenUsageMetricName,
	dedupSuppressedMetricName,
	dedupKeyMissingMetricName,
}

type accountingFixture struct {
	Schema    string                  `json:"schema"`
	Name      string                  `json:"name"`
	Source    accountingFixtureSource `json:"source"`
	Config    accountingFixtureConfig `json:"config"`
	Resources []fixtureResource       `json:"resources"`
	Expected  accountingExpected      `json:"expected"`
}

type accountingFixtureSource struct {
	Provider            string `json:"provider"`
	Instrumentation     string `json:"instrumentation"`
	SemanticConventions string `json:"semantic_conventions"`
}

type accountingFixtureConfig struct {
	DedupEnabled  bool     `json:"dedup_enabled"`
	RequestIDFrom []string `json:"request_id_from"`
}

type accountingExpected struct {
	Metrics           map[string]int64 `json:"metrics"`
	TokenObservations map[string]int64 `json:"token_observations"`
}

func TestAccountingReconciliationFixtures(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("testdata", "accounting", "v1", "*.json"))
	if err != nil {
		t.Fatalf("list accounting fixtures: %v", err)
	}
	if len(paths) < 6 {
		t.Fatalf("accounting fixture count = %d, want at least 6", len(paths))
	}

	for _, path := range paths {
		path := path
		t.Run(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), func(t *testing.T) {
			fixture := readAccountingFixture(t, path)
			cfg := fleetFixtureConfig(false, false)
			cfg.Slices = []SliceConfig{{
				Name:                   "by_provider",
				Keys:                   []string{"gen_ai.provider.name"},
				FromResourceAttributes: []string{"gen_ai.provider.name"},
			}}
			cfg.Dedup.Enabled = fixture.Config.DedupEnabled
			if len(fixture.Config.RequestIDFrom) > 0 {
				cfg.Dedup.RequestIDFrom = fixture.Config.RequestIDFrom
			}
			if err := cfg.Validate(); err != nil {
				t.Fatalf("fixture config: %v", err)
			}

			state := newFleetFixtureState(t, cfg)
			metrics := mustConsumeTraces(t, state, tracesFromFixtureResources(t, fixture.Resources))
			for _, metric := range accountingMetricNames {
				want := fixture.Expected.Metrics[metric]
				if got := sumMetricValues(metrics, metric); got != want {
					t.Errorf("%s = %d, want %d", metric, got, want)
				}
			}
			for field := tokenField(0); field < tokenFieldCount; field++ {
				for state := tokenObservationState(0); state < tokenStateCount; state++ {
					key := tokenFieldNames[field] + "/" + tokenObservationStateNames[state]
					want := fixture.Expected.TokenObservations[key]
					labels := map[string]string{
						"token_field": tokenFieldNames[field],
						"state":       tokenObservationStateNames[state],
					}
					if got := sumMetricValuesWithLabels(metrics, tokenFieldObservationsMetricName, labels); got != want {
						t.Errorf("token observation %s = %d, want %d", key, got, want)
					}
				}
			}
		})
	}
}

func readAccountingFixture(t *testing.T, path string) accountingFixture {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fixture accountingFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	if fixture.Schema != accountingFixtureSchema {
		t.Fatalf("schema = %q, want %q", fixture.Schema, accountingFixtureSchema)
	}
	if fixture.Name == "" || fixture.Source.Provider == "" || fixture.Source.Instrumentation == "" || fixture.Source.SemanticConventions == "" {
		t.Fatal("fixture source metadata must be complete")
	}
	validMetrics := make(map[string]struct{}, len(accountingMetricNames))
	for _, name := range accountingMetricNames {
		validMetrics[name] = struct{}{}
	}
	for name := range fixture.Expected.Metrics {
		if _, ok := validMetrics[name]; !ok {
			t.Fatalf("unknown accounting metric %q", name)
		}
	}
	for key := range fixture.Expected.TokenObservations {
		fieldName, stateName, ok := strings.Cut(key, "/")
		if !ok || !containsString(tokenFieldNames[:], fieldName) || !containsString(tokenObservationStateNames[:], stateName) {
			t.Fatalf("unknown token observation %q", key)
		}
	}
	return fixture
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func sumMetricValuesWithLabels(metrics pmetric.Metrics, metricName string, labels map[string]string) int64 {
	var total int64
	resourceMetrics := metrics.ResourceMetrics()
	for i := 0; i < resourceMetrics.Len(); i++ {
		scopeMetrics := resourceMetrics.At(i).ScopeMetrics()
		for j := 0; j < scopeMetrics.Len(); j++ {
			metricSlice := scopeMetrics.At(j).Metrics()
			for k := 0; k < metricSlice.Len(); k++ {
				metric := metricSlice.At(k)
				if metric.Name() != metricName || metric.Type() != pmetric.MetricTypeSum {
					continue
				}
				points := metric.Sum().DataPoints()
				for l := 0; l < points.Len(); l++ {
					point := points.At(l)
					if pointHasLabels(point.Attributes(), labels) {
						total += point.IntValue()
					}
				}
			}
		}
	}
	return total
}
