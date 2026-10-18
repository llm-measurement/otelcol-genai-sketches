// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package genaisketchconnector

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	sketchhash "github.com/llm-measurement/llm-sketchkit/go/sketchkit/hash"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"
)

func TestAccountingMalformedSpanIsolation(t *testing.T) {
	for _, withModels := range []bool{false, true} {
		for _, kind := range []string{"utf8", "oversize", "mcp-utf8", "tool-utf8", "dedup-oversize", "token-string", "token-double", "token-sum"} {
			t.Run(fmt.Sprintf("models=%t/%s", withModels, kind), func(t *testing.T) {
				s, _, _ := modelFixture(t, func(c *Config) {
					c.MCP.Enabled, c.MCP.ToolErrors.Enabled, c.Dedup.Enabled = true, true, true
					if !withModels {
						c.SummaryExport.ModelUsage = nil
					}
				})
				batch := modelTrace(10, nil)
				spans := batch.ResourceSpans().At(0).ScopeSpans().At(0).Spans()
				for range 100 {
					spans.At(0).CopyTo(spans.AppendEmpty())
				}
				a := spans.At(50).Attributes()
				rejected := strings.Contains(kind, "utf8") || strings.Contains(kind, "oversize")
				switch kind {
				case "utf8":
					a.PutStr("enduser.id", "PRIVATE_SENTINEL\xff")
				case "oversize":
					a.PutStr("gen_ai.request.prompt", strings.Repeat("PRIVATE_SENTINEL", 1000))
				case "mcp-utf8":
					a.PutStr("mcp.method.name", "PRIVATE_SENTINEL\xff")
				case "tool-utf8":
					a.PutStr("gen_ai.tool.name", "tool")
					a.PutStr("error.type", "PRIVATE_SENTINEL\xff")
				case "dedup-oversize":
					a.PutStr("request.id", strings.Repeat("PRIVATE_SENTINEL", 1000))
				case "token-string":
					a.PutStr("gen_ai.usage.input_tokens", "9223372036854775808")
				case "token-double":
					a.PutDouble("gen_ai.usage.input_tokens", 1e300)
				case "token-sum":
					a.PutInt("gen_ai.usage.input_tokens", math.MaxInt64)
				}
				metrics, ok, err := s.ConsumeTraces(context.Background(), batch)
				if err != nil || !ok {
					t.Fatalf("poison span rejected batch: ok=%t err=%v", ok, err)
				}
				wantRequests, wantOutput := uint64(101), uint64(1010)
				if rejected {
					wantRequests, wantOutput = 100, 1000
				}
				w := s.summary.windows[s.windowStart(s.clock.Now())]
				if w.counters.requests != wantRequests || w.counters.inputTokens != 1000 || w.counters.outputTokens != wantOutput {
					t.Fatalf("valid spans/fields lost: %+v", w.counters)
				}
				if withModels && w.modelUsage["public-model"].Requests != wantRequests {
					t.Fatal("model attribution drift")
				}
				if bytes.Contains(marshalMetrics(t, metrics), []byte("PRIVATE_SENTINEL")) {
					t.Fatal("rejection exposed identity")
				}
				wantRejections := map[string]int64{}
				if strings.Contains(kind, "utf8") {
					wantRejections["invalid_utf8"] = 1
				} else if strings.Contains(kind, "oversize") {
					wantRejections["attribute_too_large"] = 1
				}
				if got := rejectionMetricCounts(t, metrics); !reflect.DeepEqual(got, wantRejections) {
					t.Fatalf("fixed rejection diagnostics: got %v, want %v", got, wantRejections)
				}
				mustConsumeTraces(t, s, modelTrace(3, nil))
				if s.summary.windows[s.windowStart(s.clock.Now())].counters.requests != wantRequests+1 {
					t.Fatal("subsequent valid request was blocked")
				}
			})
		}
	}
}

func TestAccountingTokenCapIsPerFieldAndNotZero(t *testing.T) {
	keys := []string{"gen_ai.usage.input_tokens", "gen_ai.usage.output_tokens", "gen_ai.usage.cache_read.input_tokens", "gen_ai.usage.cache_write.input_tokens", "gen_ai.usage.reasoning.output_tokens"}
	for field, key := range keys {
		for _, representation := range []string{"int", "string", "double"} {
			for _, fallback := range []bool{false, true} {
				for _, value := range []int64{0, maxPlausibleTokenValue, maxPlausibleTokenValue + 1} {
					t.Run(fmt.Sprintf("%s/%s/%d/fallback=%t", key, representation, value, fallback), func(t *testing.T) {
						cfg := defaultConfig()
						sources := []*[]string{&cfg.Weights.InputTokensFrom, &cfg.Weights.OutputTokensFrom, &cfg.Weights.CacheReadInputTokensFrom, &cfg.Weights.CacheWriteInputTokensFrom, &cfg.Weights.ReasoningOutputTokensFrom}
						*sources[field] = []string{key, "test.valid_alias"}
						s := newTestState(t, cfg)
						baseline := []int64{17, 10, 2, 3, 4}
						traces := modelTrace(17, func(a pcommon.Map) {
							for i, attr := range keys {
								a.PutInt(attr, baseline[i])
							}
							switch representation {
							case "int":
								a.PutInt(key, value)
							case "string":
								a.PutStr(key, strconv.FormatInt(value, 10))
							case "double":
								a.PutDouble(key, float64(value))
							}
							if fallback {
								a.PutInt("test.valid_alias", 7)
							}
						})
						span := traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
						u, err := s.classifySpan(span, spanData{spanAttrs: span.Attributes()})
						if err != nil {
							t.Fatal(err)
						}
						values := []uint64{u.totals.inputTokens, u.totals.outputTokens, u.totals.cacheReadInputTokens, u.totals.cacheWriteInputTokens, u.totals.reasoningOutputTokens}
						counts := u.totals.tokenObservations[field]
						wantValue, reported, invalid, missing := uint64(value), uint64(1), uint64(0), uint64(0)
						if value > maxPlausibleTokenValue {
							invalid = 1
							if fallback {
								wantValue = 7
							} else {
								wantValue, reported, missing = 0, 0, 1
							}
						}
						if values[field] != wantValue || counts[tokenStateReported] != reported || counts[tokenStateInvalid] != invalid || counts[tokenStateMissing] != missing {
							t.Fatalf("value=%d observations=%v, want value=%d reported=%d invalid=%d missing=%d", values[field], counts, wantValue, reported, invalid, missing)
						}
						var conflict uint64
						if fallback && value <= maxPlausibleTokenValue {
							conflict = 1
						}
						if counts[tokenStateConflict] != conflict || u.totals.requests != 1 {
							t.Fatal("cap changed alias conflict or request semantics")
						}
						for i, v := range values {
							if i != field && (v != uint64(baseline[i]) || u.totals.tokenObservations[i][tokenStateReported] != 1 || u.totals.tokenObservations[i][tokenStateInvalid] != 0 || u.totals.tokenObservations[i][tokenStateMissing] != 0) {
								t.Fatal("cap discarded another valid field")
							}
						}
					})
				}
			}
		}
	}
}

func rejectionMetricCounts(t *testing.T, metrics pmetric.Metrics) map[string]int64 {
	t.Helper()
	counts := map[string]int64{}
	for i := 0; i < metrics.ResourceMetrics().Len(); i++ {
		scopes := metrics.ResourceMetrics().At(i).ScopeMetrics()
		for j := 0; j < scopes.Len(); j++ {
			ms := scopes.At(j).Metrics()
			for k := 0; k < ms.Len(); k++ {
				m := ms.At(k)
				if m.Name() != rejectedSpansMetricName {
					continue
				}
				if m.Type() != pmetric.MetricTypeSum || !m.Sum().IsMonotonic() {
					t.Fatal("rejections must be a monotonic sum")
				}
				points := m.Sum().DataPoints()
				for p := 0; p < points.Len(); p++ {
					point := points.At(p)
					reason, ok := point.Attributes().Get("reason")
					if !ok || point.Attributes().Len() != 1 || (reason.Str() != "invalid_utf8" && reason.Str() != "attribute_too_large") {
						t.Fatal("unbounded rejection attributes")
					}
					counts[reason.Str()] += point.IntValue()
				}
			}
		}
	}
	return counts
}

func TestAccountingHealthyHasNoRejectionDiagnostic(t *testing.T) {
	s := newTestState(t, defaultConfig())
	metrics := mustConsumeTraces(t, s, modelTrace(10, nil))
	if len(rejectionMetricCounts(t, metrics)) != 0 || bytes.Contains(marshalMetrics(t, metrics), []byte(accountingSaturatedMetricName)) {
		t.Fatal("healthy traffic acquired a resilience diagnostic")
	}
}

type rejectionAdvancingClock struct{ now time.Time }

func (c *rejectionAdvancingClock) Now() time.Time {
	now := c.now
	c.now = c.now.Add(time.Millisecond)
	return now
}

func TestAccountingRejectionUsesBatchTimestamp(t *testing.T) {
	clk := &rejectionAdvancingClock{now: time.Unix(120, 0)}
	s := newTestState(t, defaultConfig())
	s.clock = clk
	batch := modelTrace(1, func(a pcommon.Map) { a.PutStr("enduser.id", "bad\xff") })
	start := pcommon.NewTimestampFromTime(clk.now)
	for iteration := range 2 {
		wantEnd := pcommon.NewTimestampFromTime(clk.now)
		metrics := mustConsumeTraces(t, s, batch)
		ms := metrics.ResourceMetrics().At(0).ScopeMetrics().At(0).Metrics()
		found := false
		for i := 0; i < ms.Len(); i++ {
			if ms.At(i).Name() != rejectedSpansMetricName {
				continue
			}
			point := ms.At(i).Sum().DataPoints().At(0)
			if point.StartTimestamp() != start || point.Timestamp() != wantEnd || point.StartTimestamp() > point.Timestamp() || point.IntValue() != int64(iteration+1) {
				t.Fatalf("rejection timestamps/count: start=%d end=%d count=%d", point.StartTimestamp(), point.Timestamp(), point.IntValue())
			}
			found = true
		}
		if !found {
			t.Fatal("missing rejection metric")
		}
	}
}

func TestAccountingRejectionCounterSaturation(t *testing.T) {
	s := newTestState(t, defaultConfig())
	s.rejections.counts[spanRejectedUTF8] = math.MaxInt64 - 1
	batch := modelTrace(1, func(a pcommon.Map) { a.PutStr("enduser.id", "bad\xff") })
	for iteration := range 3 {
		metrics := mustConsumeTraces(t, s, batch)
		if rejectionMetricCounts(t, metrics)["invalid_utf8"] != math.MaxInt64 || s.rejections.saturated != (iteration > 0) {
			t.Fatal("rejection counter crossed wire bound or incorrectly marked exact boundary")
		}
		want := float64(0)
		if iteration > 0 {
			want = 1
		}
		if diagnosticValue(metrics, "rejections", "counter_limit") != want {
			t.Fatal("rejection saturation is not explicit")
		}
	}
}

func TestAccountingRejectionSaturationRollsBack(t *testing.T) {
	s, _, _ := modelFixture(t, func(c *Config) { c.MCP.Enabled = true })
	s.rejections.counts[spanRejectedUTF8] = math.MaxInt64
	before := modelUsageReviewState(t, s)
	batch := modelTrace(1, func(a pcommon.Map) { a.PutStr("enduser.id", "bad\xff") })
	bad := modelTrace(1, func(a pcommon.Map) { a.PutStr("mcp.method.name", "method") })
	bad.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).CopyTo(batch.ResourceSpans().At(0).ScopeSpans().At(0).Spans().AppendEmpty())
	field := s.cfg.fields[fieldMCPMethodKey]
	field.domain = sketchhash.Domain("invalid-domain")
	s.cfg.fields[fieldMCPMethodKey] = field
	if _, ok, err := s.ConsumeTraces(context.Background(), batch); err == nil || ok || !bytes.Equal(before, modelUsageReviewState(t, s)) {
		t.Fatal("failed batch committed rejection saturation flag or start timestamp")
	}
}

func TestAccountingEveryBatchIsTransactional(t *testing.T) {
	for _, withModels := range []bool{false, true} {
		for _, suppress := range []bool{false, true} {
			t.Run(fmt.Sprintf("models=%t/suppression=%t", withModels, suppress), func(t *testing.T) {
				s, _, _ := modelFixture(t, func(c *Config) {
					c.MCP.Enabled = true
					if !withModels {
						c.SummaryExport.ModelUsage = nil
					}
				})
				mustConsumeTraces(t, s, modelTrace(2, nil))
				start := s.windowStart(s.clock.Now())
				if suppress {
					s.summary.windows[start].counters.requests = math.MaxInt64
				}
				before := modelUsageReviewState(t, s)
				batch := modelTrace(3, func(a pcommon.Map) { a.PutStr("enduser.id", "bad\xff") })
				good := modelTrace(3, nil)
				good.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).CopyTo(batch.ResourceSpans().At(0).ScopeSpans().At(0).Spans().AppendEmpty())
				bad := modelTrace(4, func(a pcommon.Map) { a.PutStr("mcp.method.name", "method") })
				bad.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).CopyTo(batch.ResourceSpans().At(0).ScopeSpans().At(0).Spans().AppendEmpty())
				// Simulate an internal configuration failure reached only after the
				// valid prefix. This must roll back, unlike rejected sender input.
				originalField := s.cfg.fields[fieldMCPMethodKey]
				field := originalField
				field.domain = sketchhash.Domain("invalid-domain")
				s.cfg.fields[fieldMCPMethodKey] = field
				for range 3 {
					if _, ok, err := s.ConsumeTraces(context.Background(), batch); err == nil || ok {
						t.Fatal("internal error was swallowed")
					}
					if !bytes.Equal(before, modelUsageReviewState(t, s)) || s.batchOwned != nil {
						t.Fatal("failed batch or retry committed an applied prefix")
					}
				}
				s.cfg.fields[fieldMCPMethodKey] = originalField
				metrics := mustConsumeTraces(t, s, good)
				if len(rejectionMetricCounts(t, metrics)) != 0 || s.summary.windows[start].summarySuppressed() != suppress {
					t.Fatal("rejection diagnostic leaked from failed batch or later valid batch failed to apply")
				}
			})
		}
	}
}

func TestAccountingDefaultBatchRollback(t *testing.T) {
	cfg := defaultConfig()
	cfg.MCP.Enabled = true
	s := newTestState(t, cfg)
	mustConsumeTraces(t, s, modelTrace(1, nil))
	before := stateFingerprint(t, s)
	batch := modelTrace(2, nil)
	bad := modelTrace(3, func(a pcommon.Map) { a.PutStr("mcp.method.name", "method") })
	bad.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).CopyTo(batch.ResourceSpans().At(0).ScopeSpans().At(0).Spans().AppendEmpty())
	field := s.cfg.fields[fieldMCPMethodKey]
	field.domain = sketchhash.Domain("invalid-domain")
	s.cfg.fields[fieldMCPMethodKey] = field
	for range 3 {
		if _, ok, err := s.ConsumeTraces(context.Background(), batch); err == nil || ok || !bytes.Equal(before, stateFingerprint(t, s)) || s.batchOwned != nil {
			t.Fatal("default mode committed a failed prefix")
		}
	}
}

// The shared v1 corpus and checksums remain immutable. These six named cases
// intentionally differ after the security cap: their extreme input values are
// invalid/missing, requests and valid output survive, and no reported zero is
// invented. All other v1 expectations are compared without an overlay.
func hardenedInspectExpectation(name string) (inspectAccountingExpected, bool) {
	type counts struct{ requests, invalidInput, input, output uint64 }
	c, ok := map[string]counts{
		"aggregate-counter-saturation":     {2, 2, 0, 0},
		"error-per-span-token-sum":         {1, 1, 0, 1},
		"error-per-span-string-range":      {1, 1, 0, 0},
		"error-per-span-double-range":      {1, 1, 0, 0},
		"error-aggregate-counter-overflow": {3, 3, 0, 0},
		"error-aggregate-topk-overflow":    {2, 1, 1, 0},
	}[name]
	if !ok {
		return inspectAccountingExpected{}, false
	}
	e := inspectAccountingExpected{Metrics: map[string]uint64{}, TokenObservations: map[string]uint64{}, UsageProvenance: map[string]uint64{}}
	for _, metric := range accountingMetricNames {
		e.Metrics[metric] = 0
	}
	e.Metrics[requestsMetricName] = c.requests
	e.Metrics[inputTokensMetricName] = c.input
	e.Metrics[outputTokensMetricName] = c.output
	e.Metrics[totalTokensMetricName] = c.input + c.output
	e.Metrics[missingTokenUsageMetricName] = c.invalidInput
	for _, field := range tokenFieldNames {
		for _, state := range tokenObservationStateNames {
			e.TokenObservations[field+"/"+state] = 0
		}
	}
	e.TokenObservations["input/reported"] = c.requests - c.invalidInput
	e.TokenObservations["input/invalid"] = c.invalidInput
	e.TokenObservations["input/missing"] = c.invalidInput
	e.TokenObservations["output/reported"] = c.requests
	for _, field := range usageProvenanceFields {
		for _, state := range usageProvenanceStates {
			e.UsageProvenance[field+"/"+state] = 0
		}
		e.UsageProvenance[field+"/unknown"] = c.requests
	}
	return e, true
}
