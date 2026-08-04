//go:build load

// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay Erramilli and Codex

package genaisketchconnector

import (
	"context"
	"fmt"
	"testing"
	"time"

	sketchhash "github.com/llm-measurement/llm-sketchkit/go/sketchkit/hash"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

const (
	loadSpanCount     = 10_000
	loadActiveSlices  = 1_000
	loadMissingEvery  = 10
	loadWindowSeconds = 1
)

func TestLoadBoundsOneThousandActiveSlices(t *testing.T) {
	clk := &fixedClock{now: time.Unix(10_000, 0)}
	secret := loadSecret(t)
	state := newLoadState(t, clk, loadConfig(loadActiveSlices), secret)

	metrics := consumeLoad(t, state, tracesFromSpans(loadSpans(1_200, 1_200)...))
	if got := state.activeSliceCount(); got != loadActiveSlices {
		t.Fatalf("active slices = %d, want %d", got, loadActiveSlices)
	}
	if got := len(state.overflows); got != 1 {
		t.Fatalf("overflow slices = %d, want 1", got)
	}
	if got := sumMetric(metrics, missingTokenUsageMetricName); got == 0 {
		t.Fatal("mixed-token load did not produce missing-token metric")
	}
	snapshot, err := state.TopKSnapshot(clk.Now())
	if err != nil {
		t.Fatalf("TopKSnapshot: %v", err)
	}
	if snapshot.ItemCount() == 0 {
		t.Fatal("load top-k snapshot had no items")
	}
}

func BenchmarkLoadConsume10KSpansMixedTokens(b *testing.B) {
	clk := &fixedClock{now: time.Unix(20_000, 0)}
	secret := loadSecret(b)
	state := newLoadState(b, clk, loadConfig(2_000), secret)
	traces := tracesFromSpans(loadSpans(loadSpanCount, loadActiveSlices)...)

	b.ReportAllocs()
	start := time.Now()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		consumeLoad(b, state, traces)
	}
	elapsed := time.Since(start)
	b.ReportMetric(float64(b.N*loadSpanCount)/elapsed.Seconds(), "spans/sec")
}

func BenchmarkLoadOneThousandActiveSlices(b *testing.B) {
	traces := tracesFromSpans(loadSpans(loadActiveSlices, loadActiveSlices)...)
	secret := loadSecret(b)
	cfg := loadConfig(loadActiveSlices)

	b.ReportAllocs()
	start := time.Now()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		clk := &fixedClock{now: time.Unix(30_000+int64(i), 0)}
		state := newLoadState(b, clk, cfg, secret)
		consumeLoad(b, state, traces)
	}
	elapsed := time.Since(start)
	b.ReportMetric(float64(b.N*loadActiveSlices)/elapsed.Seconds(), "spans/sec")
}

func BenchmarkLoadWindowRotation1000Slices(b *testing.B) {
	clk := &fixedClock{now: time.Unix(40_000, 0)}
	secret := loadSecret(b)
	state := newLoadState(b, clk, loadConfig(loadActiveSlices), secret)
	traces := tracesFromSpans(loadSpans(loadActiveSlices, loadActiveSlices)...)
	consumeLoad(b, state, traces)

	b.ReportAllocs()
	start := time.Now()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		clk.Set(clk.Now().Add(loadWindowSeconds * time.Second))
		consumeLoad(b, state, traces)
	}
	elapsed := time.Since(start)
	b.ReportMetric(float64(b.N), "rotations/sec")
	b.ReportMetric(float64(b.N*loadActiveSlices)/elapsed.Seconds(), "spans/sec")
}

func loadConfig(maxSlices int) *Config {
	return testConfig(func(cfg *Config) {
		cfg.WindowDuration = loadWindowSeconds * time.Second
		cfg.RetentionWindows = 3
		cfg.MaxSlices = maxSlices
		cfg.TopK = 20
		cfg.Slices = []SliceConfig{{Name: "by_model", Keys: []string{"gen_ai.request.model"}}}
	})
}

func loadSecret(tb testing.TB) sketchhash.Secret {
	tb.Helper()

	tb.Setenv("GENAI_SKETCH_SECRET", "test-secret-32-bytes-for-unit-tests")
	secret, err := sketchhash.SecretFromEnv("GENAI_SKETCH_SECRET")
	if err != nil {
		tb.Fatalf("SecretFromEnv: %v", err)
	}
	return secret
}

func newLoadState(tb testing.TB, clk *fixedClock, cfg *Config, secret sketchhash.Secret) *collectorState {
	tb.Helper()

	state, err := newCollectorState(cfg, secret, clk, clk.Now())
	if err != nil {
		tb.Fatalf("newCollectorState: %v", err)
	}
	return state
}

func loadSpans(count int, modelCount int) []testSpan {
	spans := make([]testSpan, 0, count)
	for i := 0; i < count; i++ {
		span := testSpan{
			Model:     fmt.Sprintf("model-%04d", i%modelCount),
			Team:      fmt.Sprintf("team-%02d", i%50),
			User:      fmt.Sprintf("user-%06d", i%2_000),
			Prompt:    fmt.Sprintf("load prompt %04d", i%2_500),
			Doc:       fmt.Sprintf("doc-%05d", i%5_000),
			RequestID: fmt.Sprintf("request-%08d", i),
		}
		if i%loadMissingEvery != 0 {
			span.InputTokens = ptr(int64(80 + i%900))
			span.OutputTokens = ptr(int64(20 + i%450))
		}
		spans = append(spans, span)
	}
	return spans
}

func consumeLoad(tb testing.TB, state *collectorState, traces ptrace.Traces) pmetric.Metrics {
	tb.Helper()

	metrics, ok, err := state.ConsumeTraces(context.Background(), traces)
	if err != nil {
		tb.Fatalf("ConsumeTraces: %v", err)
	}
	if !ok {
		tb.Fatal("expected metrics")
	}
	return metrics
}

func sumMetric(metrics pmetric.Metrics, metricName string) int64 {
	var total int64
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
					total += points.At(l).IntValue()
				}
			}
		}
	}
	return total
}
