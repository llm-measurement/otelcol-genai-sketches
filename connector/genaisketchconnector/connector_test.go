// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay Erramilli and Codex
package genaisketchconnector

import (
	"context"
	"strings"
	"testing"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap"
)

type captureMetrics struct {
	metrics []pmetric.Metrics
}

func (c *captureMetrics) Capabilities() consumer.Capabilities {
	return consumer.Capabilities{}
}

func (c *captureMetrics) ConsumeMetrics(_ context.Context, metrics pmetric.Metrics) error {
	c.metrics = append(c.metrics, metrics)
	return nil
}

func TestConsumeTracesEmitsCumulativeRequestMetric(t *testing.T) {
	t.Setenv("GENAI_SKETCH_SECRET", "test-secret-32-bytes-for-unit-tests")

	sink := &captureMetrics{}
	conn := newTracesConnector(componentTelemetry(), defaultConfig(), sink)

	if err := conn.ConsumeTraces(context.Background(), tracesWithGenAISpans(2)); err != nil {
		t.Fatalf("first ConsumeTraces returned error: %v", err)
	}
	if err := conn.ConsumeTraces(context.Background(), tracesWithGenAISpans(3)); err != nil {
		t.Fatalf("second ConsumeTraces returned error: %v", err)
	}

	if len(sink.metrics) != 2 {
		t.Fatalf("expected 2 metric batches, got %d", len(sink.metrics))
	}
	got := intMetricValue(t, sink.metrics[1], requestsMetricName, map[string]string{
		"slice":       "by_model",
		"slice_value": "gen_ai.request.model=test-model",
		"overflow":    "false",
	})
	if got != 5 {
		t.Fatalf("request metric = %d, want 5", got)
	}
}

func TestConsumeTracesIgnoresNonGenAISpans(t *testing.T) {
	t.Setenv("GENAI_SKETCH_SECRET", "test-secret-32-bytes-for-unit-tests")

	sink := &captureMetrics{}
	conn := newTracesConnector(componentTelemetry(), defaultConfig(), sink)

	traces := ptrace.NewTraces()
	span := traces.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	span.SetName("ordinary-http")
	span.Attributes().PutStr("http.route", "/healthz")

	if err := conn.ConsumeTraces(context.Background(), traces); err != nil {
		t.Fatalf("ConsumeTraces returned error: %v", err)
	}
	if len(sink.metrics) != 0 {
		t.Fatalf("expected no metric batches for non-GenAI span, got %d", len(sink.metrics))
	}
}

func TestTopKZeroDoesNotStartLogLoop(t *testing.T) {
	t.Setenv("GENAI_SKETCH_SECRET", "test-secret-32-bytes-for-unit-tests")
	cfg := defaultConfig()
	cfg.TopK = 0
	conn := newTracesConnector(componentTelemetry(), cfg, &captureMetrics{})

	if err := conn.Start(context.Background(), nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		if err := conn.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})

	conn.mu.Lock()
	defer conn.mu.Unlock()
	if conn.debugCancel != nil || conn.debugDone != nil {
		t.Fatal("topk: 0 started the structured-log loop")
	}
}

func TestValidateRejectsSensitiveSliceHashedSourceOverlap(t *testing.T) {
	cfg := defaultConfig()
	cfg.Slices = []SliceConfig{{Name: "by_team", Keys: []string{"team.id"}}}
	field := cfg.Fields[fieldUserKey]
	field.FromResourceAttributes = append(field.FromResourceAttributes, "team.id")
	cfg.Fields[fieldUserKey] = field

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "slice keys overlap sensitive hashed-field source attributes") {
		t.Fatalf("Validate() error = %v, want sensitive overlap rejection", err)
	}
}

func componentTelemetry() component.TelemetrySettings {
	return component.TelemetrySettings{Logger: zap.NewNop()}
}

func tracesWithGenAISpans(n int) ptrace.Traces {
	traces := ptrace.NewTraces()
	spans := traces.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Spans()
	for i := 0; i < n; i++ {
		span := spans.AppendEmpty()
		span.SetName("genai.request")
		span.Attributes().PutStr("gen_ai.request.model", "test-model")
	}
	return traces
}

func intMetricValue(t *testing.T, metrics pmetric.Metrics, metricName string, labels map[string]string) int64 {
	t.Helper()

	resourceMetrics := metrics.ResourceMetrics()
	for i := 0; i < resourceMetrics.Len(); i++ {
		scopeMetrics := resourceMetrics.At(i).ScopeMetrics()
		for j := 0; j < scopeMetrics.Len(); j++ {
			ms := scopeMetrics.At(j).Metrics()
			for k := 0; k < ms.Len(); k++ {
				metric := ms.At(k)
				if metric.Name() != metricName {
					continue
				}
				points := metric.Sum().DataPoints()
				for l := 0; l < points.Len(); l++ {
					point := points.At(l)
					if !pointHasLabels(point.Attributes(), labels) {
						continue
					}
					return point.IntValue()
				}
			}
		}
	}
	t.Fatalf("metric %q with labels %#v not found", metricName, labels)
	return 0
}

func pointHasLabels(attrs pcommon.Map, labels map[string]string) bool {
	for key, want := range labels {
		got, ok := attrs.Get(key)
		if !ok || got.AsString() != want {
			return false
		}
	}
	return true
}
