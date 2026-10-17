// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package genaisketchconnector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/modelusage"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func TestModelUsageBatchReviewByteBudgetRollback(t *testing.T) {
	for _, tc := range []struct {
		identity string
		dedup    bool
	}{
		{"", false}, {"", true}, {modelIdentityAgentGateway, false}, {modelIdentityAgentGateway, true},
	} {
		identity, dedup := tc.identity, tc.dedup
		t.Run(fmt.Sprintf("identity=%s/dedup=%t", identity, dedup), func(t *testing.T) {
			publicID := func(i int) string {
				return fmt.Sprintf("model-%04d-%s", i, strings.Repeat("x", 117))
			}
			observedID := func(i int) string {
				if i == 0 {
					return "PRIVATE_MODEL"
				}
				return fmt.Sprintf("PRIVATE_MODEL_%04d", i)
			}
			s, e, clk := modelFixture(t, func(c *Config) {
				c.MaxSlices = modelusage.MaxModels
				c.Dedup.Enabled = dedup
				c.SummaryExport.ModelUsage.Identity = identity
				c.SummaryExport.ModelUsage.Models = make(map[string]string)
				for i := range modelusage.MaxModels {
					c.SummaryExport.ModelUsage.Models[observedID(i)] = publicID(i)
				}
			})
			trace := func(model int, request string, input int64) ptrace.Traces {
				return modelTrace(input, func(a pcommon.Map) {
					a.PutStr("gen_ai.request.model", observedID(model))
					a.PutStr("gen_ai.response.model", observedID(model))
					a.PutStr("request.id", request)
				})
			}
			mustConsumeTraces(t, s, trace(0, "retained-seed", 100000))
			clk.now = clk.now.Add(s.cfg.windowDuration)
			seed := trace(0, "current-seed", 100000)
			mustConsumeTraces(t, s, seed)
			start := s.windowStart(clk.now)
			w := s.summary.windows[start]
			row := w.modelUsage[publicID(0)]
			entryBytes := len(mustJSON(t, row)) + len(mustJSON(t, publicID(0))) + 2
			// Reserve legacy payload space and two rows. The probe below verifies
			// this fixture is near the real guard, without duplicating its logic.
			n := (summary.MaxBytes-4096-10000)/entryBytes - 2
			const newRows = 16
			if n < 2 || n+newRows > modelusage.MaxModels {
				t.Fatalf("fixture needs %d retained rows, outside configured budget", n)
			}
			span := seed.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
			data := spanData{spanAttrs: span.Attributes(), resourceAttrs: seed.ResourceSpans().At(0).Resource().Attributes()}
			update, err := s.classifySpan(span, data)
			if err != nil {
				t.Fatal(err)
			}
			// Build a synthetic retained aggregate with valid scalar accounting
			// and legacy sketch weights, not an already-oversize or invalid row.
			span.Attributes().Remove("request.id")
			prepared, err := s.prepareUpdate(w, data, update)
			if err != nil {
				t.Fatal(err)
			}
			for i := 1; i < n; i++ {
				w.modelUsage[publicID(i)] = row
				if err := s.applyErrorCheckedSketches(w, prepared); err != nil {
					t.Fatal(err)
				}
				s.applyNoErrorSketches(w, prepared)
				w.counters.addCounters(update, prepared)
				s.summary.addCounters(update, prepared)
			}
			w.modelUsageBytes = len(mustJSON(t, w.modelUsage))
			docs, err := s.summaryDocuments(e, clk.now.Add(time.Second))
			if err != nil || len(docs) != 2 {
				t.Fatalf("retained summaries: count=%d err=%v", len(docs), err)
			}
			var baselineBytes int
			for _, doc := range docs {
				legacy := doc
				legacy.ModelUsage = nil
				if _, err := legacy.MarshalBinary(); err != nil {
					t.Fatalf("full legacy snapshot must marshal before refusal test: %v", err)
				}
				encoded, err := doc.MarshalBinary()
				if err != nil {
					t.Fatalf("full extended snapshot must marshal before refusal test: %v", err)
				}
				if err := doc.ModelUsage.CheckNames(s.cfg.maxSlices); err != nil {
					t.Fatal(err)
				}
				if doc.WindowStart == start {
					baselineBytes = len(encoded)
				}
			}
			if baselineBytes < summary.MaxBytes-32*1024 || baselineBytes > summary.MaxBytes-4096 {
				t.Fatalf("fixture is not safely near the byte limit: %d bytes", baselineBytes)
			}

			attempts := []ptrace.Traces{trace(0, "batch-prefix", 1)}
			for i := range newRows {
				attempts = append(attempts, trace(n+i, fmt.Sprintf("batch-new-%d", i), 100000))
			}
			before := modelUsageReviewState(t, s)
			probe := modelUsageReviewClone(s)
			refusedAt := -1
			for i, attempt := range attempts {
				_, ok, err := probe.ConsumeTraces(context.Background(), attempt)
				if err != nil {
					if !strings.Contains(err.Error(), "byte budget") || ok {
						t.Fatalf("probe refused for wrong reason at %d: ok=%t err=%v", i, ok, err)
					}
					refusedAt = i
					break
				}
				if !ok {
					t.Fatalf("probe ignored relevant span %d", i)
				}
			}
			if refusedAt < 2 {
				t.Fatalf("need accepted prefix and new row before refusal, got index %d", refusedAt)
			}
			if !bytes.Equal(before, modelUsageReviewState(t, s)) {
				t.Fatal("detached probe changed original state")
			}
			if got := probe.summary.windows[start].counters.requests; got != w.counters.requests+uint64(refusedAt) {
				t.Fatalf("probe prefix did not count every accepted span: got %d", got)
			}
			batch := ptrace.NewTraces()
			for _, attempt := range attempts[:refusedAt+1] {
				rs := attempt.ResourceSpans()
				for i := 0; i < rs.Len(); i++ {
					rs.At(i).CopyTo(batch.ResourceSpans().AppendEmpty())
				}
			}
			originalSummary := s.summary
			originalSlices, originalOverflows := maps.Clone(s.slices), maps.Clone(s.overflows)
			for retry := range 2 {
				_, ok, err := s.ConsumeTraces(context.Background(), batch)
				if err == nil || !strings.Contains(err.Error(), "byte budget") || ok {
					t.Fatalf("attempt %d: expected batch byte refusal, ok=%t err=%v", retry, ok, err)
				}
				if !bytes.Equal(before, modelUsageReviewState(t, s)) {
					t.Fatalf("attempt %d changed counters, summary, cached bytes, sketches, dedup, routing, or retention", retry)
				}
				if s.summary != originalSummary || !maps.Equal(s.slices, originalSlices) || !maps.Equal(s.overflows, originalOverflows) || s.batchOwned != nil {
					t.Fatalf("attempt %d published staged state", retry)
				}
			}
			t.Logf("valid envelope=%d/%d bytes; retained rows=%d; accepted probe prefix=%d; refusal and retry unchanged", baselineBytes, summary.MaxBytes, n, refusedAt)
		})
	}
}

// Include state omitted by the older metric-only fingerprint: global summary,
// all window counters, provenance, model bytes, Bloom, and router/LRU metadata.
func modelUsageReviewState(t *testing.T, s *collectorState) []byte {
	t.Helper()
	type windowSnapshot struct {
		Counters   map[string]uint64
		Sketches   []string
		Models     json.RawMessage
		ModelBytes int
	}
	type sliceSnapshot struct {
		Counters             map[string]uint64
		Name, Value, SortKey string
		Overflow             bool
		StartTime            pcommon.Timestamp
		LastWindow, LRU      int64
		Windows              map[int64]windowSnapshot
	}
	snapshotSlice := func(slice *sliceState) sliceSnapshot {
		out := sliceSnapshot{
			Counters: slice.accountingCounters.export(), Name: slice.label.name,
			Value: slice.label.value, SortKey: slice.label.sortKey, Overflow: slice.label.overflow,
			StartTime: slice.startTime, LastWindow: slice.lastWindow, LRU: slice.lruSequence,
			Windows: make(map[int64]windowSnapshot),
		}
		for start, w := range slice.windows {
			v := windowSnapshot{Models: mustJSON(t, w.modelUsage), ModelBytes: w.modelUsageBytes}
			if w.counters != nil {
				v.Counters = w.counters.export()
			}
			for _, sketch := range []binaryMarshaler{
				w.distinctUsers, w.distinctPrompts, w.distinctDocs,
				w.distinctMCPSessions, w.distinctMCPMethods, w.distinctMCPResources,
				w.topToolErrors, w.dedupRequests,
			} {
				v.Sketches = append(v.Sketches, marshalSketchHex(t, sketch))
			}
			for _, sketch := range w.topKeys {
				v.Sketches = append(v.Sketches, marshalSketchHex(t, sketch))
			}
			if w.modelUsage != nil && len(v.Models) != w.modelUsageBytes {
				t.Fatalf("cached model byte count drift at window %d: %d != %d", start, w.modelUsageBytes, len(v.Models))
			}
			out.Windows[start] = v
		}
		return out
	}
	out := struct {
		NextLRU   int64
		Batch     bool
		Slices    map[string]sliceSnapshot
		Overflows map[string]sliceSnapshot
		Summary   sliceSnapshot
	}{s.nextLRUSeq, s.batchOwned != nil, make(map[string]sliceSnapshot), make(map[string]sliceSnapshot), snapshotSlice(s.summary)}
	for key, slice := range s.slices {
		out.Slices[key] = snapshotSlice(slice)
	}
	for key, slice := range s.overflows {
		out.Overflows[key] = snapshotSlice(slice)
	}
	return mustJSON(t, out)
}

func modelUsageReviewClone(s *collectorState) *collectorState {
	cloneSlice := func(slice *sliceState) *sliceState {
		c := *slice
		c.windows = make(map[int64]*windowState, len(slice.windows))
		for start, w := range slice.windows {
			copy := cloneSummaryWindow(w)
			copy.modelUsage = maps.Clone(w.modelUsage)
			c.windows[start] = copy
		}
		return &c
	}
	c := *s
	c.slices, c.overflows = maps.Clone(s.slices), maps.Clone(s.overflows)
	for key, slice := range s.slices {
		c.slices[key] = cloneSlice(slice)
	}
	for key, slice := range s.overflows {
		c.overflows[key] = cloneSlice(slice)
	}
	c.summary = cloneSlice(s.summary)
	return &c
}
