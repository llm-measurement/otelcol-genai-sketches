// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay Erramilli and Codex
//go:build integration

package integration

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

type prometheusMetricSample struct {
	labels map[string]string
	value  float64
}

func parsePrometheusMetricSamples(body, name string) ([]prometheusMetricSample, error) {
	parser := expfmt.NewTextParser(model.LegacyValidation)
	families, err := parser.TextToMetricFamilies(strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	var samples []prometheusMetricSample
	for _, metric := range families[name].GetMetric() {
		var value float64
		switch {
		case metric.Counter != nil:
			value = metric.Counter.GetValue()
		case metric.Gauge != nil:
			value = metric.Gauge.GetValue()
		case metric.Untyped != nil:
			value = metric.Untyped.GetValue()
		default:
			return nil, fmt.Errorf("metric %s is not a scalar", name)
		}
		labels := make(map[string]string, len(metric.Label))
		for _, label := range metric.Label {
			labels[label.GetName()] = label.GetValue()
		}
		samples = append(samples, prometheusMetricSample{labels: labels, value: value})
	}
	return samples, nil
}

func prometheusMetricSamples(t testing.TB, body, name string) []prometheusMetricSample {
	t.Helper()
	samples, err := parsePrometheusMetricSamples(body, name)
	if err != nil {
		t.Fatalf("parse Prometheus metrics: %v", err)
	}
	return samples
}

func metricValue(t testing.TB, body, name string, labels map[string]string) (float64, bool) {
	t.Helper()
	for _, sample := range prometheusMetricSamples(t, body, name) {
		matches := true
		for key, value := range labels {
			if got, ok := sample.labels[key]; !ok || got != value {
				matches = false
				break
			}
		}
		if matches {
			return sample.value, true
		}
	}
	return 0, false
}

func sumPrometheusMetric(t testing.TB, body, name string) int64 {
	t.Helper()
	var total int64
	for _, sample := range prometheusMetricSamples(t, body, name) {
		total += int64(math.Round(sample.value))
	}
	return total
}

func TestPrometheusSamples(t *testing.T) {
	body := "# TYPE requests counter\n" +
		"requests_extra{tenant=\"a\"} 999\n" +
		"requests{other_tenant=\"a\"} 3\n" +
		"requests{tenant=\"a b\\\"c\\\\d\\ne\"} 7 1750000000000\n" +
		"requests 2\n"
	if got, ok := metricValue(t, body, "requests", map[string]string{"tenant": "a b\"c\\d\ne"}); !ok || got != 7 {
		t.Fatalf("escaped labels and timestamp: got %v, %v", got, ok)
	}
	for _, labels := range []map[string]string{{"tenant": "a"}, {"absent": ""}} {
		if _, ok := metricValue(t, body, "requests", labels); ok {
			t.Fatalf("matched absent label: %v", labels)
		}
	}
	if got := sumPrometheusMetric(t, body, "requests"); got != 12 {
		t.Fatalf("sum = %d, want 12", got)
	}
	if got := prometheusMetricSamples(t, body, "request"); len(got) != 0 {
		t.Fatalf("matched metric name prefix: %v", got)
	}
	if _, err := parsePrometheusMetricSamples("requests{tenant=\"broken} 7\n", "requests"); err == nil {
		t.Fatal("malformed labels were silently ignored")
	}
}
