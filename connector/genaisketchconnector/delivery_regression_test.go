// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex
package genaisketchconnector

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type failingMetrics struct {
	captureMetrics
	err error
}

func (s *failingMetrics) ConsumeMetrics(ctx context.Context, metrics pmetric.Metrics) error {
	_ = s.captureMetrics.ConsumeMetrics(ctx, metrics)
	return s.err
}

func TestCommittedTracesDoNotRetryAfterMetricsFailure(t *testing.T) {
	const sentinel = "private-downstream-user-and-request-sentinel"
	for name, failure := range map[string]error{
		"transient": errors.New(sentinel),
		"wrapped":   fmt.Errorf("delivery: %w", errors.New(sentinel)),
		"canceled":  context.Canceled,
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("GENAI_SKETCH_SECRET", "test-secret-32-bytes-for-unit-tests")
			cfg := defaultConfig()
			cfg.TopK = 0
			logsCore, logs := observer.New(zap.WarnLevel)
			telemetry := componentTelemetry()
			telemetry.Logger = zap.New(logsCore)
			sink := &failingMetrics{err: failure}
			conn := newTracesConnector(telemetry, cfg, sink)
			startConnector(t, conn)

			if err := conn.ConsumeTraces(context.Background(), tracesWithGenAISpans(2)); err != nil {
				t.Fatalf("committed traces returned a retryable error: %v", err)
			}
			if logs.Len() != 1 {
				t.Fatalf("delivery failure warnings = %d, want 1", logs.Len())
			}
			entry := logs.All()[0]
			if strings.Contains(entry.Message, sentinel) || len(entry.Context) != 0 {
				t.Fatal("delivery warning exposed downstream error details")
			}
			if !strings.Contains(entry.Message, "after accounting commit") {
				t.Fatal("delivery warning does not distinguish committed accounting")
			}

			sink.err = nil
			if err := conn.ConsumeTraces(context.Background(), tracesWithGenAISpans(1)); err != nil {
				t.Fatal(err)
			}
			if len(sink.metrics) != 2 || logs.Len() != 1 {
				t.Fatal("expected one failed emission and one successful emission")
			}
			labels := map[string]string{"slice": "by_model", "slice_value": "gen_ai.request.model=test-model", "overflow": "false"}
			if got := intMetricValue(t, sink.metrics[1], requestsMetricName, labels); got != 3 {
				t.Fatalf("next cumulative requests = %d, want 3", got)
			}
		})
	}
}
