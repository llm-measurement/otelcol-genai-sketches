// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package genaisketchconnector

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
)

func TestReviewOverflowWindowStaysCompatible(t *testing.T) {
	for _, withModelUsage := range []bool{false, true} {
		t.Run(fmt.Sprintf("model_usage=%t", withModelUsage), func(t *testing.T) {
			fixture := func(producer string) (*collectorState, *summaryExporter, *fixedClock) {
				return modelFixture(t, func(c *Config) {
					c.Dedup.Enabled, c.TopK = true, 0
					c.SummaryExport.ProducerID = producer
					if !withModelUsage {
						c.SummaryExport.ModelUsage = nil
					}
				})
			}
			span := func(id string) testSpan {
				return testSpan{Model: "PRIVATE_MODEL", RequestID: id, InputTokens: ptr(7), OutputTokens: ptr(11)}
			}
			s, e, clk := fixture("flooded")
			mustConsumeTraces(t, s, tracesFromSpans(span("first")))
			early := exportDocs(t, s, e, clk.now.Add(time.Second))[0]
			n, ne, nc := fixture("normal")
			mustConsumeTraces(t, n, tracesFromSpans(span("normal")))
			normal := exportDocs(t, n, ne, nc.now.Add(time.Second))[0]
			for batch := 0; batch < 100; batch++ {
				spans := make([]testSpan, 500)
				for i := range spans {
					spans[i] = span(fmt.Sprintf("flood-%d", batch*500+i))
				}
				mustConsumeTraces(t, s, tracesFromSpans(spans...))
			}
			window := s.summary.windows[s.windowStart(clk.now)]
			if window.counters.dedupOverflow == 0 || n.summary.windows[n.windowStart(nc.now)].counters.dedupOverflow != 0 {
				t.Fatal("fixtures did not exercise different overflow states")
			}
			late := exportDocs(t, s, e, clk.now.Add(2*time.Second))[0]
			if early.WindowStart != late.WindowStart || early.Sequence >= late.Sequence || late.Counters["requests"] <= early.Counters["requests"] {
				t.Fatal("fixtures did not produce distinct cumulative snapshots")
			}
			checkCompatible := func(a, b summary.Envelope) {
				t.Helper()
				for _, doc := range []summary.Envelope{a, b} {
					if _, ok := doc.Counters["dedup_overflow"]; ok {
						t.Fatal("overflow diagnostic leaked into summary counters")
					}
				}
				if err := summary.Compatible(a, b); err != nil {
					t.Fatal("overflow changed summary compatibility", err)
				}
			}
			checkCompatible(early, late)
			checkCompatible(normal, late)
			combined, err := summary.Combine([]summary.Envelope{late, early, early, late}, []string{"flooded"})
			if err != nil {
				t.Fatal("same-window snapshot combine", err)
			}
			if !reflect.DeepEqual(combined.Counters, late.Counters) || !reflect.DeepEqual(combined.ModelUsage, late.ModelUsage) {
				t.Fatal("earlier or repeated snapshots doubled totals")
			}
			if len(combined.Sources) != 1 || combined.Sources[0].Sequence != late.Sequence {
				t.Fatal("combine did not retain the latest snapshot")
			}
			combined, err = summary.Combine([]summary.Envelope{late, normal, early, normal, late}, []string{"flooded", "normal"})
			if err != nil {
				t.Fatal("independent-producer combine", err)
			}
			for key, value := range late.Counters {
				if combined.Counters[key] != value+normal.Counters[key] {
					t.Fatalf("combined %s doubled or omitted a snapshot", key)
				}
			}
			if len(combined.Sources) != 2 || len(combined.Missing) != 0 {
				t.Fatal("independent producer coverage changed")
			}
			if withModelUsage {
				row := combined.ModelUsage.Models["public-model"]
				requests := late.Counters["requests"] + normal.Counters["requests"]
				if row.Requests != requests || row.Fields.Input.Tokens != requests*7 || row.Fields.Output.Tokens != requests*11 {
					t.Fatal("model totals doubled or omitted a snapshot")
				}
			} else if combined.ModelUsage != nil {
				t.Fatal("combine added opt-in model usage")
			}
			clk.Set(clk.now.Add(s.cfg.windowDuration))
			mustConsumeTraces(t, s, tracesFromSpans(span("rollover")))
			docs := exportDocs(t, s, e, clk.now.Add(time.Second))
			next := docs[len(docs)-1]
			if next.WindowStart == late.WindowStart || next.Counters["requests"] != 1 {
				t.Fatal("fixture did not roll over into an ordinary window")
			}
			checkCompatible(late, next)
			checkCompatible(normal, next)
		})
	}
}
