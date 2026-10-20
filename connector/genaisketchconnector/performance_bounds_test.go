// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package genaisketchconnector

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	sketchbloom "github.com/llm-measurement/llm-sketchkit/go/sketchkit/bloom"
	sketchfi "github.com/llm-measurement/llm-sketchkit/go/sketchkit/frequentitems"
	sketchhash "github.com/llm-measurement/llm-sketchkit/go/sketchkit/hash"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/hllpp"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/modelusage"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
	"go.opentelemetry.io/collector/pdata/pcommon"
)

func TestModelUsageFixedBound(t *testing.T) {
	for _, profile := range []string{"micro", "small", "default"} {
		t.Run(profile, func(t *testing.T) {
			s, _, _ := modelFixture(t, func(c *Config) {
				c.Profiles.HLLPP, c.Profiles.FrequentItems = profile, profile
				c.MCP.Enabled, c.MCP.ToolErrors.Enabled = true, true
				c.TopKKeys = []TopKKeyConfig{{Field: fieldPromptKey}, {Field: fieldUserKey},
					{Field: fieldSessionKey, Weight: "requests"}, {Field: fieldDocKey}}
			})
			w, err := s.summary.newWindow(s)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.summaryPayloads(w); err != nil {
				t.Fatal(err)
			}
			hlls := []*hllpp.Sketch{w.distinctUsers, w.distinctPrompts, w.distinctDocs,
				w.distinctMCPSessions, w.distinctMCPMethods, w.distinctMCPResources}
			frequent := append(append([]*sketchfi.Sketch{}, w.topKeys...), w.topToolErrors)
			check := func() {
				t.Helper()
				payloads, err := s.summaryPayloads(w)
				if err != nil {
					t.Fatal(err)
				}
				counters := w.counters.export()
				counters["dedup_overflow"] = 0
				for k := range counters {
					counters[k] = uint64(maxInt64Value)
				}
				encoded, err := json.Marshal(struct {
					Counters   map[string]uint64          `json:"counters"`
					ModelUsage *modelusage.Block          `json:"model_usage"`
					Sketches   map[string]summary.Payload `json:"sketches"`
				}{counters, modelUsageBlock("model-usage-v1:"+strings.Repeat("0", 64), map[string]modelusage.Model{}), payloads})
				if err != nil || len(encoded) > s.cfg.modelUsage.fixedBytes {
					t.Fatalf("actual=%d bound=%d error=%v", len(encoded), s.cfg.modelUsage.fixedBytes, err)
				}
			}
			check()
			// Exercise sparse promotion and fully occupied frequent-item maps,
			// including nonzero error estimates with wide integer encodings.
			for i := 1; i <= 4096; i++ {
				hash := uint64(i) * 0x9e3779b97f4a7c15
				for _, h := range hlls {
					h.AddHash(hash)
				}
				for _, f := range frequent {
					if err := f.AddHash(hash, maxInt64Value/8192); err != nil {
						t.Fatal(err)
					}
				}
				if i == 255 || i == 256 || i == 1023 || i == 1024 || i == 2047 || i == 2048 {
					check()
				}
			}
			check()
			for _, h := range hlls {
				h.ForceDense()
			}
			check()
		})
	}
}

func TestModelUsageCapacityBoundaryAndNoPreview(t *testing.T) {
	s, _, clk := modelFixture(t, nil)
	mustConsumeTraces(t, s, modelTrace(10, nil))
	w := s.summary.windows[s.windowStart(clk.now)]
	before := cloneSummaryWindow(w)
	before.modelUsage = w.modelUsage
	pending := []pendingSliceUpdate{{slice: s.summary, window: w}}
	w.modelUsageBytes = summary.MaxBytes - 4096 - s.cfg.modelUsage.fixedBytes + 2
	if _, err := s.prepareModelUsage(spanData{}, spanUpdate{}, pending); err != nil {
		t.Fatal("exact boundary rejected", err)
	}
	w.modelUsageBytes++
	if _, err := s.prepareModelUsage(spanData{}, spanUpdate{}, pending); err != errModelUsageByteBudget {
		t.Fatal("over-budget summary accepted", err)
	}
	after, err := s.summaryPayloads(w)
	if err != nil {
		t.Fatal(err)
	}
	original, err := s.summaryPayloads(before)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(mustJSON(t, original), mustJSON(t, after)) || *w.counters != *before.counters {
		t.Fatal("capacity check mutated live sketches or counters")
	}
	// The check itself must be allocation-free and independent of sketch size.
	allocs := testing.AllocsPerRun(100, func() {
		_, _ = s.prepareModelUsage(spanData{}, spanUpdate{}, pending)
	})
	if allocs != 0 {
		t.Fatalf("capacity check allocated: %g", allocs)
	}
}

func TestDedupDesignCapacity(t *testing.T) {
	for _, profile := range []sketchbloom.Profile{sketchbloom.ProfileMicro, sketchbloom.ProfileSmall, sketchbloom.ProfileDefault} {
		t.Run(string(profile), func(t *testing.T) {
			s := newTestState(t, defaultConfig())
			s.cfg.dedupEnabled = true
			bloom, err := sketchbloom.New(profile, sketchhash.SessionV1, sketchhash.HMACSHA25664)
			if err != nil {
				t.Fatal(err)
			}
			// Repeated direct inserts reach each real profile boundary without
			// spending the test's time on a million complete collector spans.
			for i := uint64(0); i < bloom.RatedInsertions()-1; i++ {
				if err := bloom.AddHash(1); err != nil {
					t.Fatal(err)
				}
			}
			w := &windowState{dedupRequests: bloom}
			attrs := pcommon.NewMap()
			attrs.PutStr("request.id", "capacity-boundary")
			data := spanData{spanAttrs: attrs, resourceAttrs: pcommon.NewMap()}
			hash, duplicate, missing, err := s.prepareDedup(w, data)
			if err != nil || !hash.ok || duplicate || missing {
				t.Fatalf("last allowed insert: %+v %v %v %v", hash, duplicate, missing, err)
			}
			if err := bloom.AddHash(hash.value); err != nil {
				t.Fatal(err)
			}
			prepared, err := s.prepareUpdate(w, data, spanUpdate{request: true})
			if err != nil || !prepared.dedupOverflow || prepared.dedupHash.ok || prepared.deduped {
				t.Fatalf("full filter not bypassed: %+v %v", prepared, err)
			}
			attrs.Clear()
			prepared, err = s.prepareUpdate(w, data, spanUpdate{request: true})
			if err != nil || !prepared.dedupKeyMissing || prepared.dedupOverflow {
				t.Fatalf("missing key misclassified as overflow: %+v %v", prepared, err)
			}
			if bloom.InsertedCount() != bloom.RatedInsertions() || w.summarySuppressed() {
				t.Fatal("design capacity changed insertion count or suppressed summary")
			}
		})
	}
}

func TestDedupFloodDoesNotSuppressLaterRequests(t *testing.T) {
	s, e, clk := modelFixture(t, func(c *Config) { c.Dedup.Enabled = true; c.TopK = 0 })
	// Reproduction: 50,000 distinct IDs followed by 1,000 unseen IDs.
	for batch := 0; batch < 100; batch++ {
		spans := make([]testSpan, 500)
		for i := range spans {
			spans[i] = testSpan{Model: "PRIVATE_MODEL", RequestID: fmt.Sprintf("flood-%d", batch*500+i)}
		}
		mustConsumeTraces(t, s, tracesFromSpans(spans...))
	}
	start := s.windowStart(clk.now)
	beforeRequests := s.summary.windows[start].counters.requests
	beforeSuppressed := s.summary.windows[start].counters.dedupSuppressed
	beforeOverflow := s.summary.windows[start].counters.dedupOverflow
	spans := make([]testSpan, 1000)
	for i := range spans {
		spans[i] = testSpan{Model: "PRIVATE_MODEL", RequestID: fmt.Sprintf("later-%d", i)}
	}
	metrics := mustConsumeTraces(t, s, tracesFromSpans(spans...))
	w := s.summary.windows[start]
	if w.counters.requests != beforeRequests+1000 || w.counters.dedupSuppressed != beforeSuppressed ||
		w.counters.dedupOverflow != beforeOverflow+1000 || w.dedupRequests.InsertedCount() != w.dedupRequests.RatedInsertions() {
		t.Fatal("flood affected later request accounting")
	}
	if sumMetricValues(metrics, dedupOverflowMetricName) == 0 || w.summarySuppressed() {
		t.Fatal("overflow not reported or summary suppressed")
	}
	doc := exportDocs(t, s, e, clk.now.Add(time.Second))[0]
	if doc.Counters["dedup_overflow"] != w.counters.dedupOverflow ||
		doc.ModelUsage.Models["public-model"].Requests != w.counters.requests {
		t.Fatal("summary/model usage did not count bypassed requests")
	}
	clk.Set(clk.now.Add(s.cfg.windowDuration))
	mustConsumeTraces(t, s, tracesFromSpans(spans[0], spans[0]))
	w = s.summary.windows[s.windowStart(clk.now)]
	if w.counters.requests != 1 || w.counters.dedupSuppressed != 1 || w.counters.dedupOverflow != 0 {
		t.Fatal("new window did not resume deduplication")
	}
	if _, ok := w.counters.export()["dedup_overflow"]; ok {
		t.Fatal("zero overflow changed ordinary output")
	}
}

func TestDedupOverflowCounterSaturates(t *testing.T) {
	c := accountingCounters{dedupOverflow: math.MaxInt64}
	c.addCounters(spanUpdate{}, preparedUpdate{dedupOverflow: true})
	if c.dedupOverflow != math.MaxInt64 || !c.saturated {
		t.Fatal("overflow counter wrapped")
	}
}
