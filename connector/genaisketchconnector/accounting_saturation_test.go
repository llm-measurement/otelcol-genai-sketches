// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package genaisketchconnector

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/modelusage"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"
)

func TestAccountingEveryCounterUsesEmittedIntLimit(t *testing.T) {
	c := accountingCounters{requests: math.MaxInt64 - 1, agentRuns: math.MaxInt64 - 1,
		inputTokens: math.MaxInt64 - 1, outputTokens: math.MaxInt64 - 1,
		cacheReadInputTokens: math.MaxInt64 - 1, cacheWriteInputTokens: math.MaxInt64 - 1,
		reasoningOutputTokens: math.MaxInt64 - 1, missingTokens: math.MaxInt64 - 1,
		dedupSuppressed: math.MaxInt64 - 1, dedupKeyMissing: math.MaxInt64 - 1}
	u := spanUpdate{request: true, agentRun: true, totals: spanTotals{requests: 1, inputTokens: 1, outputTokens: 1,
		cacheReadInputTokens: 1, cacheWriteInputTokens: 1, reasoningOutputTokens: 1, missingTokens: 1}}
	for f := range c.tokenObservations {
		for q := range c.tokenObservations[f] {
			c.tokenObservations[f][q], u.totals.tokenObservations[f][q] = math.MaxInt64-1, 1
		}
	}
	for f := range c.usageProvenance {
		for q := range c.usageProvenance[f] {
			c.usageProvenance[f][q], u.totals.usageProvenance[f][q] = math.MaxInt64-1, 1
		}
	}
	for iteration := range 3 {
		c.addCounters(u, preparedUpdate{dedupKeyMissing: true})
		c.add(&c.dedupSuppressed, 1)
		if c.saturated != (iteration > 0) {
			t.Fatalf("scalar limit marker at iteration %d: %t", iteration, c.saturated)
		}
		if !c.totalSaturated {
			t.Fatal("derived total exhaustion was not marked")
		}
		for name, value := range c.export() {
			if value != math.MaxInt64 {
				t.Fatalf("%s wrapped or crossed emitted IntValue range: %d", name, value)
			}
		}
	}
}

func TestAccountingLifetimeLimitDoesNotPoisonExactWindows(t *testing.T) {
	s, e, clk := modelFixture(t, func(c *Config) { c.TopK = 0 })
	trace := modelTrace(1, func(a pcommon.Map) { a.PutInt("gen_ai.usage.output_tokens", 0) })
	mustConsumeTraces(t, s, trace)
	key := s.metricSlices()[0].label.sortKey
	s.slices[key].inputTokens, s.slices[key].requests = math.MaxInt64-1, math.MaxInt64-1
	metrics := mustConsumeTraces(t, s, trace)
	if s.slices[key].saturated || diagnosticValue(metrics, "slice_counters", "counter_limit") != 0 {
		t.Fatal("exact MaxInt64 marked as saturated")
	}
	metrics = mustConsumeTraces(t, s, trace)
	if sumMetricValues(metrics, inputTokensMetricName) != math.MaxInt64 || sumMetricValues(metrics, requestsMetricName) != math.MaxInt64 || diagnosticValue(metrics, "slice_counters", "counter_limit") != 1 {
		t.Fatal("emitted counter clamp lacks explicit marker")
	}
	docs := exportDocs(t, s, e, clk.now.Add(time.Second))
	if len(docs) != 1 || docs[0].Counters["requests"] != 3 || docs[0].Counters["input_tokens"] != 3 {
		t.Fatal("lifetime limit suppressed an exact current window")
	}
	clk.now = clk.now.Add(s.cfg.windowDuration)
	mustConsumeTraces(t, s, trace)
	docs = exportDocs(t, s, e, clk.now.Add(time.Second))
	if len(docs) != 2 || docs[1].Counters["requests"] != 1 || docs[1].ModelUsage.Models["public-model"].Requests != 1 {
		t.Fatal("lifetime limit poisoned next summary window")
	}
}

func TestAccountingDerivedTotalLimitIsExplicit(t *testing.T) {
	s, e, clk := modelFixture(t, func(c *Config) { c.TopK = 0; c.SummaryExport.ModelUsage = nil })
	trace := modelTrace(1, func(a pcommon.Map) { a.PutInt("gen_ai.usage.output_tokens", 1) })
	mustConsumeTraces(t, s, trace)
	key, start := s.metricSlices()[0].label.sortKey, s.windowStart(clk.now)
	s.slices[key].inputTokens, s.slices[key].outputTokens = math.MaxInt64-3, 1
	s.summary.windows[start].counters.inputTokens, s.summary.windows[start].counters.outputTokens = math.MaxInt64-3, 1
	metrics := mustConsumeTraces(t, s, trace)
	if s.slices[key].totalSaturated || sumMetricValues(metrics, totalTokensMetricName) != math.MaxInt64 || s.summary.windows[start].summarySuppressed() {
		t.Fatal("exact derived boundary was suppressed")
	}
	metrics = mustConsumeTraces(t, s, trace)
	w := s.summary.windows[start]
	if !s.slices[key].totalSaturated || s.slices[key].saturated || !w.saturation[saturationTotalTokens] || w.saturation[saturationCounter] || diagnosticValue(metrics, "slice_counters", "total_tokens_limit") != 1 || diagnosticValue(metrics, "summary_window", "total_tokens_limit") != 1 {
		t.Fatal("derived total limit was hidden or confused with a scalar limit")
	}
	if sumMetricValues(metrics, totalTokensMetricName) != math.MaxInt64 || len(exportDocs(t, s, e, clk.now.Add(time.Second))) != 0 {
		t.Fatal("derived saturation produced a false exact summary")
	}
}

func TestAccountingSummarySuppressionStickyAndExpires(t *testing.T) {
	for _, reason := range []saturationReason{saturationCounter, saturationTotalTokens, saturationModelUsage, saturationModelCount} {
		t.Run(saturationReasonNames[reason], func(t *testing.T) {
			s, e, clk := modelFixture(t, func(c *Config) {
				c.TopK = 0
				c.MaxSlices = 2
				c.SummaryExport.ModelUsage.Models["PRIVATE_NEW"] = "new-model"
			})
			mustConsumeTraces(t, s, modelTrace(1, nil))
			start := s.windowStart(clk.now)
			partial := exportDocs(t, s, e, clk.now.Add(time.Second))[0]
			path := filepath.Join(e.cfg.Directory, fmt.Sprintf("%020d-%s.json", start, e.epoch))
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			w := s.summary.windows[start]
			trace := modelTrace(5, nil)
			switch reason {
			case saturationCounter:
				w.counters.requests = math.MaxInt64
			case saturationTotalTokens:
				w.counters.inputTokens, w.counters.outputTokens = math.MaxInt64-20, 10
			case saturationModelUsage:
				limit := uint64(math.MaxInt64)
				row, err := modelusage.AddAttempt(modelusage.Model{}, modelusage.Attempt{Input: modelusage.Observation{Value: &limit}})
				if err != nil {
					t.Fatal(err)
				}
				w.modelUsage["public-model"] = row
			case saturationModelCount:
				w.modelUsage["other-model"] = w.modelUsage["public-model"]
				trace = modelTrace(5, func(a pcommon.Map) { a.PutStr("gen_ai.request.model", "PRIVATE_NEW") })
			}
			w.modelUsageBytes = len(mustJSON(t, w.modelUsage))
			metrics := mustConsumeTraces(t, s, trace)
			w = s.summary.windows[start]
			if !w.saturation[reason] || diagnosticValue(metrics, "summary_window", saturationReasonNames[reason]) != 1 || sumMetricValues(metrics, requestsMetricName) != 2 {
				t.Fatal("capacity did not isolate and diagnose the summary window")
			}
			frozen := mustJSON(t, w.modelUsage)
			for range 2 {
				mustConsumeTraces(t, s, modelTrace(2, nil))
				if s.summary.windows[start] != w || !bytes.Equal(frozen, mustJSON(t, w.modelUsage)) {
					t.Fatal("suppressed summary was copied or changed")
				}
				if len(exportDocs(t, s, e, clk.now.Add(2*time.Second))) != 0 {
					t.Fatal("suppressed summary emitted")
				}
				after, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatal("previous partial file or its ObservedEnd changed")
				}
			}
			clk.now = clk.now.Add(s.cfg.windowDuration)
			mustConsumeTraces(t, s, modelTrace(3, nil))
			docs := exportDocs(t, s, e, clk.now.Add(time.Second))
			if len(docs) != 1 || docs[0].WindowStart == start || docs[0].Counters["requests"] != 1 || docs[0].ModelUsage.Models["public-model"].Requests != 1 || partial.ObservedEnd != start+int64(time.Second) {
				t.Fatal("fresh summary window did not recover independently")
			}
			clk.now = clk.now.Add(time.Duration(s.cfg.retentionWindows) * s.cfg.windowDuration)
			metrics = mustConsumeTraces(t, s, modelTrace(1, nil))
			if s.summary.windows[start] != nil || diagnosticValue(metrics, "summary_window", saturationReasonNames[reason]) != 0 {
				t.Fatal("suppression outlived normal retention")
			}
		})
	}
}

func TestAccountingModelErrorClassificationIsNarrow(t *testing.T) {
	for _, reason := range []string{"overflow", "conservation", "structure", "range", "model_count", "serialization"} {
		_, capacity := modelUsageCapacityReason(fmt.Errorf("wrapped: %w", &modelusage.Error{Reason: reason}))
		if capacity != (reason == "overflow") {
			t.Fatalf("wrong capacity classification: %s", reason)
		}
	}
	for _, err := range []error{errors.New("overflow"), errors.New("summary model_usage exceeds envelope byte budget"), errors.New("serialization")} {
		if _, capacity := modelUsageCapacityReason(err); capacity {
			t.Fatal("untyped error treated as capacity")
		}
	}
	for err, want := range map[error]saturationReason{errModelUsageCountBudget: saturationModelCount, errModelUsageByteBudget: saturationSummaryBytes} {
		if got, ok := modelUsageCapacityReason(fmt.Errorf("wrapped: %w", err)); !ok || got != want {
			t.Fatal("explicit budget lost classification")
		}
	}
}

func TestAccountingUnexpectedModelErrorRollsBackBatch(t *testing.T) {
	for _, reason := range []string{"conservation", "range"} {
		t.Run(reason, func(t *testing.T) {
			s, _, clk := modelFixture(t, nil)
			mustConsumeTraces(t, s, modelTrace(1, nil))
			w := s.summary.windows[s.windowStart(clk.now)]
			row := w.modelUsage["public-model"]
			if reason == "conservation" {
				row.Requests++
			} else {
				row.Fields.Input.Tokens = math.MaxInt64 + 1
			}
			w.modelUsage["public-model"] = row
			w.modelUsageBytes = len(mustJSON(t, w.modelUsage))
			before := modelUsageReviewState(t, s)
			batch := modelTrace(1, func(a pcommon.Map) { a.PutStr("gen_ai.request.model", "PRIVATE_NEW") })
			bad := modelTrace(1, nil)
			bad.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).CopyTo(batch.ResourceSpans().At(0).ScopeSpans().At(0).Spans().AppendEmpty())
			for range 2 {
				_, ok, err := s.ConsumeTraces(context.Background(), batch)
				var modelErr *modelusage.Error
				if ok || !errors.As(err, &modelErr) || modelErr.Reason != reason || !bytes.Equal(before, modelUsageReviewState(t, s)) {
					t.Fatalf("unexpected %s error did not roll back: %v", reason, err)
				}
			}
		})
	}
}

func TestAccountingDedupAndToolErrorExhaustion(t *testing.T) {
	for _, reason := range []saturationReason{saturationDedup, saturationToolError} {
		t.Run(saturationReasonNames[reason], func(t *testing.T) {
			s, e, clk := modelFixture(t, func(c *Config) { c.Dedup.Enabled = true; c.MCP.Enabled = true; c.MCP.ToolErrors.Enabled = true })
			trace := modelTrace(1, func(a pcommon.Map) {
				a.PutStr("request.id", "first")
				a.PutStr("gen_ai.tool.name", "tool")
				a.PutStr("error.type", "error")
			})
			mustConsumeTraces(t, s, trace)
			start, key := s.windowStart(clk.now), s.metricSlices()[0].label.sortKey
			for _, w := range []*windowState{s.summary.windows[start], s.slices[key].windows[start]} {
				if reason == saturationToolError {
					if err := w.topToolErrors.AddHash(1, maxInt64Value-1); err != nil {
						t.Fatal(err)
					}
				} else {
					one := w.dedupRequests.Clone()
					for range 63 {
						if err := w.dedupRequests.Merge(w.dedupRequests.Clone()); err != nil {
							t.Fatal(err)
						}
						if err := w.dedupRequests.Merge(one); err != nil {
							t.Fatal(err)
						}
					}
					if w.dedupRequests.InsertedCount() != math.MaxUint64 {
						t.Fatal("fixture missed Bloom capacity")
					}
				}
			}
			trace.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Attributes().PutStr("request.id", "second")
			metrics := mustConsumeTraces(t, s, trace)
			if diagnosticValue(metrics, "slice_window", saturationReasonNames[reason]) != 1 || diagnosticValue(metrics, "summary_window", saturationReasonNames[reason]) != 1 || sumMetricValues(metrics, requestsMetricName) != 2 || len(exportDocs(t, s, e, clk.now.Add(time.Second))) != 0 {
				t.Fatal("sketch capacity was not isolated")
			}
			snapshot, err := s.TopKSnapshot(clk.now)
			if err != nil {
				t.Fatal(err)
			}
			if reason == saturationToolError {
				if !snapshot.Truncated {
					t.Fatal("exhausted tool errors not marked incomplete")
				}
				for _, stream := range snapshot.Slices {
					if stream.Field == fieldToolErrorKey {
						t.Fatal("exhausted tool error stream exposed")
					}
				}
			}
			clk.now = clk.now.Add(s.cfg.windowDuration)
			mustConsumeTraces(t, s, trace)
			if s.summary.windows[s.windowStart(clk.now)].summarySuppressed() {
				t.Fatal("sketch saturation poisoned fresh window")
			}
		})
	}
}

func diagnosticValue(metrics pmetric.Metrics, surface, reason string) float64 {
	var total float64
	for i := 0; i < metrics.ResourceMetrics().Len(); i++ {
		scopes := metrics.ResourceMetrics().At(i).ScopeMetrics()
		for j := 0; j < scopes.Len(); j++ {
			ms := scopes.At(j).Metrics()
			for k := 0; k < ms.Len(); k++ {
				m := ms.At(k)
				if m.Name() != accountingSaturatedMetricName {
					continue
				}
				for p := 0; p < m.Gauge().DataPoints().Len(); p++ {
					point := m.Gauge().DataPoints().At(p)
					if pointHasLabels(point.Attributes(), map[string]string{"surface": surface, "reason": reason}) {
						total += point.DoubleValue()
					}
				}
			}
		}
	}
	return total
}
