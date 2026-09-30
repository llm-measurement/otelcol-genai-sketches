// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package genaisketchconnector

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func TestTopKOverflowDoesNotPartiallyApplySpan(t *testing.T) {
	for _, scenario := range []string{"summary", "later_slice", "overflow_slice"} {
		t.Run(scenario, func(t *testing.T) {
			s, _ := topKExportFixture(t, []TopKKeyConfig{{Field: fieldSessionKey}}, func(c *Config) {
				c.Slices = []SliceConfig{{Name: "model", Keys: []string{"gen_ai.request.model"}}, {Name: "team", Keys: []string{"team.id"}}}
				c.Dedup.Enabled = true
				c.Dedup.RequestIDFrom = []string{"request.id"}
				if scenario == "overflow_slice" {
					c.MaxSlices = 1
				}
			})
			if scenario != "summary" {
				s.summary = nil
			}
			seed := tracesFromSpans(testSpan{Model: "old", Team: "same", RequestID: "first", InputTokens: ptr(maxInt64Value - 1), OutputTokens: ptr(0)})
			seed.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Attributes().PutStr("session.id", "PRIVATE_SESSION")
			mustConsumeTraces(t, s, seed)
			before := stateFingerprint(t, s)
			lru := s.nextLRUSeq
			var summaryWeight int64
			if s.summary != nil {
				for _, w := range s.summary.windows {
					summaryWeight = w.topKeys[0].TotalWeight()
				}
			}
			team := "same"
			if scenario == "summary" {
				team = "new"
			}
			rejected := tracesFromSpans(testSpan{Model: "new", Team: team, RequestID: "second", InputTokens: ptr(2), OutputTokens: ptr(0)})
			rejected.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Attributes().PutStr("session.id", "PRIVATE_SESSION")
			if _, _, err := s.ConsumeTraces(context.Background(), rejected); err == nil {
				t.Fatal("overflow accepted")
			}
			if !bytes.Equal(before, stateFingerprint(t, s)) || s.nextLRUSeq != lru {
				t.Fatal("rejected span changed metrics, sketches, or slice routing")
			}
			for _, slice := range s.metricSlices() {
				for _, w := range slice.windows {
					if w.dedupRequests.InsertedCount() != 1 {
						t.Fatal("rejected span entered dedup state")
					}
				}
			}
			if s.summary != nil {
				for _, w := range s.summary.windows {
					if w.topKeys[0].TotalWeight() != summaryWeight || w.counters.requests != 1 || w.dedupRequests.InsertedCount() != 1 {
						t.Fatal("rejected span changed summary")
					}
				}
			}
		})
	}
}

func TestSummaryWireLimitRejectsBeforePlannedEviction(t *testing.T) {
	s, e := topKExportFixture(t, []TopKKeyConfig{{Field: fieldSessionKey, Weight: "requests"}}, func(c *Config) {
		c.MaxSlices = 1
	})
	clk := s.clock.(*fixedClock)
	mustConsume(t, s, testSpan{Model: "old", InputTokens: ptr(1), OutputTokens: ptr(0)})
	clk.now = clk.now.Add(3 * s.cfg.windowDuration)
	start := s.windowStart(clk.now)
	w, err := s.summary.window(start, s)
	if err != nil {
		t.Fatal(err)
	}
	w.counters.inputTokens = uint64(maxInt64Value)
	before := stateFingerprint(t, s)
	lru := s.nextLRUSeq
	traces := tracesFromSpans(testSpan{Model: "new", InputTokens: ptr(1), OutputTokens: ptr(0)})
	traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Attributes().PutStr("session.id", "PRIVATE_SESSION")
	if _, _, err := s.ConsumeTraces(context.Background(), traces); err == nil {
		t.Fatal("summary wire counter overflow accepted")
	}
	if !bytes.Equal(before, stateFingerprint(t, s)) || s.nextLRUSeq != lru || w.counters.inputTokens != uint64(maxInt64Value) || w.topKeys[0].TotalWeight() != 0 {
		t.Fatal("rejected span evicted a slice or changed summary state")
	}
	for _, doc := range exportDocs(t, s, e, clk.now.Add(time.Second)) {
		if _, err := doc.MarshalBinary(); err != nil {
			t.Fatal("rejected overflow left an unexportable summary", err)
		}
	}
}

func TestSummaryWireLimitWithRequestWeight(t *testing.T) {
	s, e := topKExportFixture(t, []TopKKeyConfig{{Field: fieldSessionKey, Weight: "requests"}}, nil)
	seed := tracesFromSpans(testSpan{Model: "model", InputTokens: ptr(maxInt64Value), OutputTokens: ptr(0)})
	seed.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Attributes().PutStr("session.id", "session")
	mustConsumeTraces(t, s, seed)
	before := stateFingerprint(t, s)
	additional := tracesFromSpans(testSpan{Model: "model", InputTokens: ptr(1), OutputTokens: ptr(0)})
	additional.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Attributes().PutStr("session.id", "session")
	if _, _, err := s.ConsumeTraces(context.Background(), additional); err == nil {
		t.Fatal("wire counter overflow accepted without token-weighted sketch")
	}
	if !bytes.Equal(before, stateFingerprint(t, s)) {
		t.Fatal("wire overflow changed metric state")
	}
	for _, doc := range exportDocs(t, s, e, s.clock.Now().Add(time.Second)) {
		if _, err := doc.MarshalBinary(); err != nil {
			t.Fatal("valid summary became unexportable", err)
		}
		if doc.Counters["requests"] != 1 {
			t.Fatal("rejected request entered summary")
		}
	}
}
