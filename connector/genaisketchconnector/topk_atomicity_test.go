// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package genaisketchconnector

import (
	"testing"
	"time"
)

func TestTopKOverflowIsolatesOnlyExhaustedStream(t *testing.T) {
	for _, scenario := range []string{"summary", "later_slice", "overflow_slice"} {
		t.Run(scenario, func(t *testing.T) {
			s, e := topKExportFixture(t, []TopKKeyConfig{{Field: fieldSessionKey}, {Field: fieldUserKey, Weight: "requests"}}, func(c *Config) {
				c.Slices = []SliceConfig{{Name: "model", Keys: []string{"gen_ai.request.model"}}, {Name: "team", Keys: []string{"team.id"}}}
				c.Dedup.Enabled = true
				c.Dedup.RequestIDFrom = []string{"request.id"}
				if scenario == "overflow_slice" {
					c.MaxSlices = 1
				}
			})
			trace := tracesFromSpans(testSpan{Model: "old", Team: "same", User: "user", RequestID: "first", InputTokens: ptr(1), OutputTokens: ptr(0)})
			a := trace.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Attributes()
			a.PutStr("session.id", "PRIVATE_SESSION")
			mustConsumeTraces(t, s, trace)
			start := s.windowStart(s.clock.Now())
			target := s.summary
			if scenario == "later_slice" {
				target = s.slices["team\x00team.id=same"]
			}
			if scenario == "overflow_slice" {
				target = s.overflows["team"]
			}
			if target == nil {
				t.Fatal("missing seeded target")
			}
			key, isSummary := target.label.sortKey, target == s.summary
			w := target.windows[start]
			if err := w.topKeys[0].AddHash(1, maxInt64Value-1-w.topKeys[0].TotalWeight()); err != nil {
				t.Fatal(err)
			}
			a.PutInt("gen_ai.usage.input_tokens", 2)
			a.PutStr("request.id", "second")
			metrics := mustConsumeTraces(t, s, trace)
			if sumMetricValues(metrics, requestsMetricName) != 4 {
				t.Fatal("capacity dropped ordinary requests")
			}
			if isSummary {
				target = s.summary
			} else if scenario == "overflow_slice" {
				target = s.overflows["team"]
			} else {
				target = s.slices[key]
			}
			w = target.windows[start]
			if !w.topKeySaturated[0] || !w.saturation[saturationTopK] || w.topKeys[0].TotalWeight() != maxInt64Value-1 {
				t.Fatal("exhausted stream was mutated or unmarked")
			}
			if isSummary {
				if w.counters.requests != 1 || w.dedupRequests.InsertedCount() != 1 || len(exportDocs(t, s, e, s.clock.Now().Add(time.Second))) != 0 {
					t.Fatal("summary not frozen and omitted")
				}
			} else if w.topKeys[1].TotalWeight() != 2 || target.requests != 2 || w.dedupRequests.InsertedCount() != 2 {
				t.Fatal("unrelated stream, metrics, or dedup stopped")
			}
			snapshot, err := s.TopKSnapshot(s.clock.Now())
			if err != nil || snapshot.Truncated == isSummary {
				t.Fatalf("snapshot truncation: %+v err=%v", snapshot, err)
			}
			for _, slice := range snapshot.Slices {
				if !isSummary && slice.SliceName == target.label.name && slice.SliceValue == target.label.value && slice.Field == fieldSessionKey {
					t.Fatal("saturated top-k presented as exact")
				}
			}
		})
	}
}

func TestSummaryWireLimitDoesNotBlockPlannedEviction(t *testing.T) {
	s, e := topKExportFixture(t, []TopKKeyConfig{{Field: fieldSessionKey, Weight: "requests"}}, func(c *Config) { c.MaxSlices = 1 })
	clk := s.clock.(*fixedClock)
	mustConsume(t, s, testSpan{Model: "old", InputTokens: ptr(1), OutputTokens: ptr(0)})
	clk.now = clk.now.Add(3 * s.cfg.windowDuration)
	start := s.windowStart(clk.now)
	w, err := s.summary.window(start, s)
	if err != nil {
		t.Fatal(err)
	}
	w.counters.inputTokens = uint64(maxInt64Value)
	traces := tracesFromSpans(testSpan{Model: "new", InputTokens: ptr(1), OutputTokens: ptr(0)})
	traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Attributes().PutStr("session.id", "PRIVATE_SESSION")
	mustConsumeTraces(t, s, traces)
	w = s.summary.windows[start]
	if !w.saturation[saturationCounter] || w.counters.inputTokens != uint64(maxInt64Value) || w.topKeys[0].TotalWeight() != 0 || len(s.slices) != 1 || s.slices["by_model\x00gen_ai.request.model=new"] == nil {
		t.Fatal("capacity blocked normal routing or changed frozen summary")
	}
	for _, doc := range exportDocs(t, s, e, clk.now.Add(time.Second)) {
		if doc.WindowStart == start {
			t.Fatal("saturated window exported")
		}
		if _, err := doc.MarshalBinary(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSummaryWireLimitWithRequestWeight(t *testing.T) {
	s, e := topKExportFixture(t, []TopKKeyConfig{{Field: fieldSessionKey, Weight: "requests"}}, nil)
	trace := tracesFromSpans(testSpan{Model: "model", InputTokens: ptr(1), OutputTokens: ptr(0)})
	trace.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Attributes().PutStr("session.id", "session")
	mustConsumeTraces(t, s, trace)
	start := s.windowStart(s.clock.Now())
	s.summary.windows[start].counters.inputTokens = uint64(maxInt64Value)
	metrics := mustConsumeTraces(t, s, trace)
	if sumMetricValues(metrics, requestsMetricName) != int64(2*len(s.cfg.slices)) || len(exportDocs(t, s, e, s.clock.Now().Add(time.Second))) != 0 {
		t.Fatal("summary limit blocked metrics or emitted false exact document")
	}
	for _, slice := range s.metricSlices() {
		if slice.windows[start].topKeys[0].TotalWeight() != 2 {
			t.Fatal("healthy request-weighted stream stopped")
		}
	}
}
