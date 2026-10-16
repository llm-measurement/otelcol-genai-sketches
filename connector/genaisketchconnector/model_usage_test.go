// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package genaisketchconnector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	sketchhash "github.com/llm-measurement/llm-sketchkit/go/sketchkit/hash"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/modelusage"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func modelFixture(t *testing.T, edit func(*Config)) (*collectorState, *summaryExporter, *fixedClock) {
	t.Helper()
	cfg := defaultConfig()
	cfg.Slices = []SliceConfig{{Name: "model", Keys: []string{"gen_ai.request.model"}}}
	cfg.SummaryExport = SummaryExportConfig{Directory: t.TempDir(), ProducerID: "producer", ScopeID: "scope", KeyID: "key", Interval: time.Second, ModelUsage: &ModelUsageConfig{Slice: "model", Models: map[string]string{"PRIVATE_MODEL": "public-model"}}}
	if err := os.Chmod(cfg.SummaryExport.Directory, 0700); err != nil {
		t.Fatal(err)
	}
	if edit != nil {
		edit(cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	clk := &fixedClock{now: time.Unix(120, 0)}
	s := newTestStateWithClock(t, clk, cfg)
	e, err := newSummaryExporter(cfg, clk.now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.root.Close() })
	return s, e, clk
}

func modelTrace(input int64, edit func(pcommon.Map)) ptrace.Traces {
	traces := tracesFromSpans(testSpan{Model: "PRIVATE_MODEL", User: "PRIVATE_USER", Prompt: "PRIVATE_PROMPT", InputTokens: ptr(input), OutputTokens: ptr(10)})
	a := traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Attributes()
	a.PutStr("gen_ai.operation.name", "chat")
	a.PutInt("gen_ai.usage.cache_read.input_tokens", 0)
	a.PutInt("gen_ai.usage.cache_write.input_tokens", 0)
	if edit != nil {
		edit(a)
	}
	return traces
}

func TestModelUsageTiersAndLegacyParity(t *testing.T) {
	s, e, clk := modelFixture(t, nil)
	legacy, le, _ := modelFixture(t, func(c *Config) { c.SummaryExport.ModelUsage = nil })
	le.epoch = e.epoch
	for _, input := range []int64{0, 100000, 100001, 200000, 200001, 272000, 272001} {
		traces := modelTrace(input, nil)
		mustConsumeTraces(t, s, traces)
		mustConsumeTraces(t, legacy, traces)
	}
	doc := exportDocs(t, s, e, clk.now.Add(time.Second))[0]
	old := exportDocs(t, legacy, le, clk.now.Add(time.Second))[0]
	row := doc.ModelUsage.Models["public-model"]
	if row.Requests != 7 || row.Complete.Requests != 7 {
		t.Fatal(row)
	}
	for i, n := range []uint64{2, 2, 2, 1} {
		if row.Histogram[i].Complete.Requests != n {
			t.Fatal(row.Histogram)
		}
	}
	encoded, err := doc.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := summary.Parse(encoded); err != nil {
		t.Fatal(err)
	}
	for _, sentinel := range []string{"PRIVATE_MODEL", "PRIVATE_USER", "PRIVATE_PROMPT"} {
		if bytes.Contains(encoded, []byte(sentinel)) {
			t.Fatal("private sentinel escaped")
		}
	}
	doc.ModelUsage = nil
	newBase, err := doc.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	oldBytes, err := old.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(newBase, oldBytes) || e.accountingID != le.accountingID {
		t.Fatal("extension altered legacy bytes or accounting")
	}
}

func TestModelUsageQualityAndZeroOrigin(t *testing.T) {
	cases := []struct {
		name string
		edit func(pcommon.Map)
		want modelusage.Exclusions
	}{
		{"missing-cache", func(a pcommon.Map) { a.Remove("gen_ai.usage.cache_read.input_tokens") }, modelusage.Exclusions{MissingSplit: 1}},
		{"invalid", func(a pcommon.Map) { a.PutStr("gen_ai.usage.cache_read.input_tokens", "invalid") }, modelusage.Exclusions{InvalidUsage: 1}},
		{"conflict", func(a pcommon.Map) { a.PutInt("gen_ai.usage.prompt_tokens", 101) }, modelusage.Exclusions{InvalidUsage: 1}},
		{"subset", func(a pcommon.Map) { a.PutInt("gen_ai.usage.cache_read.input_tokens", 101) }, modelusage.Exclusions{InvalidUsage: 1}},
		{"partition", func(a pcommon.Map) {
			a.PutInt("gen_ai.usage.cache_read.input_tokens", 60)
			a.PutInt("gen_ai.usage.cache_write.input_tokens", 60)
		}, modelusage.Exclusions{InvalidUsage: 1}},
		{"unavailable", func(a pcommon.Map) { a.PutStr("gen_ai_sketch.usage.input.provenance", "unavailable") }, modelusage.Exclusions{MissingSplit: 1}},
		{"zero-unknown", func(a pcommon.Map) {
			a.PutInt("gen_ai.usage.input_tokens", 0)
			a.PutInt("gen_ai.usage.output_tokens", 0)
		}, modelusage.Exclusions{ZeroOriginUnknown: 1}},
		{"zero-known", func(a pcommon.Map) {
			a.PutInt("gen_ai.usage.input_tokens", 0)
			a.PutInt("gen_ai.usage.output_tokens", 0)
			a.PutStr("gen_ai_sketch.usage.input.provenance", "provider_reported")
			a.PutStr("gen_ai_sketch.usage.output.provenance", "inferred")
		}, modelusage.Exclusions{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, e, clk := modelFixture(t, nil)
			mustConsumeTraces(t, s, modelTrace(100, tc.edit))
			doc := exportDocs(t, s, e, clk.now.Add(time.Second))[0]
			row := doc.ModelUsage.Models["public-model"]
			if row.Excluded != tc.want {
				t.Fatalf("got %+v want %+v", row.Excluded, tc.want)
			}
			if _, err := doc.MarshalBinary(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestModelUsageRoutingRetentionAndReplay(t *testing.T) {
	s, e, clk := modelFixture(t, func(c *Config) { c.MaxSlices = 1; c.Dedup.Enabled = true })
	first := modelTrace(100, func(a pcommon.Map) { a.PutStr("request.id", "one") })
	mustConsumeTraces(t, s, first)
	mustConsumeTraces(t, s, first)
	old := exportDocs(t, s, e, clk.now.Add(time.Second))[0]
	mustConsumeTraces(t, s, modelTrace(200, func(a pcommon.Map) { a.PutStr("gen_ai.request.model", "PRIVATE_OTHER"); a.PutStr("request.id", "two") }))
	updated := exportDocs(t, s, e, clk.now.Add(2*time.Second))[0]
	if old.ModelUsage.Models["public-model"].Requests != 1 || len(old.ModelUsage.Models) != 1 {
		t.Fatal("snapshot aliasing")
	}
	if updated.ModelUsage.Models["__overflow__"].Requests != 1 || updated.Counters["requests"] != 2 {
		t.Fatal("overflow/dedup mismatch")
	}
	result, err := summary.Combine([]summary.Envelope{old, updated, old, updated}, []string{"producer"})
	if err != nil || result.Counters["requests"] != 2 || result.ModelUsage.Models["public-model"].Requests != 1 {
		t.Fatal(result, err)
	}
	clk.now = clk.now.Add(3 * time.Minute)
	mustConsumeTraces(t, s, modelTrace(300, func(a pcommon.Map) { a.PutStr("gen_ai.request.model", "PRIVATE_NEW"); a.PutStr("request.id", "three") }))
	docs := exportDocs(t, s, e, clk.now.Add(time.Second))
	if !reflect.DeepEqual(docs[0].ModelUsage, updated.ModelUsage) {
		t.Fatal("metric eviction changed retained rows")
	}
	if docs[len(docs)-1].ModelUsage.Models["__unmapped__"].Requests != 1 {
		t.Fatal("implicit raw mapping")
	}
}

func TestModelUsageGlobalDedupAndSharedSliceBudget(t *testing.T) {
	s, e, clk := modelFixture(t, func(c *Config) { c.Dedup.Enabled = true })
	first := modelTrace(100, func(a pcommon.Map) { a.PutStr("request.id", "same") })
	mustConsumeTraces(t, s, first)
	mustConsumeTraces(t, s, modelTrace(100, func(a pcommon.Map) { a.PutStr("request.id", "same"); a.PutStr("gen_ai.request.model", "PRIVATE_OTHER") }))
	doc := exportDocs(t, s, e, clk.now.Add(time.Second))[0]
	if doc.Counters["requests"] != 1 || len(doc.ModelUsage.Models) != 1 || doc.Counters["dedup_suppressed"] != 1 {
		t.Fatal("model rows did not follow global dedup", doc.Counters)
	}
	for _, w := range s.summary.windows {
		encoded, err := json.Marshal(w.modelUsage)
		if err != nil || len(encoded) != w.modelUsageBytes {
			t.Fatal("incremental model byte count drift", err)
		}
	}
	s, e, clk = modelFixture(t, func(c *Config) {
		c.MaxSlices = 1
		c.Slices = append([]SliceConfig{{Name: "team", Keys: []string{"team.id"}}}, c.Slices...)
	})
	mustConsumeTraces(t, s, modelTrace(100, func(a pcommon.Map) { a.PutStr("team.id", "PRIVATE_TEAM") }))
	doc = exportDocs(t, s, e, clk.now.Add(time.Second))[0]
	if len(doc.ModelUsage.Models) != 1 || doc.ModelUsage.Models["__overflow__"].Requests != 1 {
		t.Fatal("selected model slice ignored shared budget")
	}
}

func TestModelUsageResourceFallbackAndEmptyWindows(t *testing.T) {
	s, e, clk := modelFixture(t, nil)
	docs := exportDocs(t, s, e, clk.now)
	if len(docs) != 1 || docs[0].ModelUsage == nil || len(docs[0].ModelUsage.Models) != 0 {
		t.Fatal("enabled empty block missing")
	}
	traces := modelTrace(10, nil)
	a := traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Attributes()
	a.Remove("gen_ai.request.model")
	traces.ResourceSpans().At(0).Resource().Attributes().PutStr("gen_ai.request.model", "PRIVATE_MODEL")
	mustConsumeTraces(t, s, traces)
	doc := exportDocs(t, s, e, clk.now.Add(time.Second))[0]
	if doc.ModelUsage.Models["public-model"].Requests != 1 {
		t.Fatal("effective resource fallback ignored")
	}
	for _, w := range s.summary.windows {
		encoded, err := json.Marshal(w.modelUsage)
		if err != nil || len(encoded) != w.modelUsageBytes {
			t.Fatal("incremental model byte count drift", err)
		}
	}
}

func TestModelUsageReservedClassificationAndContract(t *testing.T) {
	for _, name := range []string{"", strings.Repeat("X", 257), "PRIVATE_UNKNOWN"} {
		s, e, clk := modelFixture(t, nil)
		mustConsumeTraces(t, s, modelTrace(1, func(a pcommon.Map) { a.PutStr("gen_ai.request.model", name) }))
		doc := exportDocs(t, s, e, clk.now.Add(time.Second))[0]
		want := "__unmapped__"
		if name == "" {
			want = "__missing__"
		} else if len(name) > 256 {
			want = "__too_long__"
		}
		if doc.ModelUsage.Models[want].Requests != 1 {
			t.Fatal(doc.ModelUsage)
		}
	}
	_, a, _ := modelFixture(t, nil)
	_, b, _ := modelFixture(t, func(c *Config) { c.Slices = append(c.Slices, SliceConfig{Name: "team", Keys: []string{"team.id"}}) })
	if a.accountingID != b.accountingID || a.modelUsageContract == b.modelUsageContract {
		t.Fatal("routing contract not separated")
	}
	_, c, _ := modelFixture(t, func(c *Config) { c.SummaryExport.ModelUsage.Models["PRIVATE_MODEL"] = "other-public" })
	if a.modelUsageContract == c.modelUsageContract {
		t.Fatal("mapping not bound")
	}
}

func TestModelUsageRejectsAtomically(t *testing.T) {
	s, _, clk := modelFixture(t, func(c *Config) { c.Dedup.Enabled = true })
	mustConsumeTraces(t, s, modelTrace(1, nil))
	w := s.summary.windows[s.windowStart(clk.now)]
	row := w.modelUsage["public-model"]
	row.Fields.Input.Tokens = modelusage.Max
	w.modelUsage["public-model"] = row
	before := stateFingerprint(t, s)
	rows, _ := json.Marshal(w.modelUsage)
	lru := s.nextLRUSeq
	_, _, err := s.ConsumeTraces(context.Background(), modelTrace(1, nil))
	if err == nil {
		t.Fatal("inconsistent retained row accepted")
	}
	after, _ := json.Marshal(w.modelUsage)
	if !bytes.Equal(rows, after) || !bytes.Equal(before, stateFingerprint(t, s)) || lru != s.nextLRUSeq {
		t.Fatal("failure mutated state")
	}
}

func TestModelUsageConfigErrorsDoNotEcho(t *testing.T) {
	for _, edit := range []func(*Config){
		func(c *Config) { c.SummaryExport.Directory = "" },
		func(c *Config) {
			c.SummaryExport.ModelUsage.Models["PRIVATE_SECRET"] = "https://PRIVATE_HOST/?token=PRIVATE_KEY"
		},
		func(c *Config) { c.SummaryExport.ModelUsage.Slice = "PRIVATE_UNKNOWN" },
		func(c *Config) { c.Slices[0].Keys = append(c.Slices[0].Keys, "team.id") },
	} {
		cfg := defaultConfig()
		cfg.SummaryExport.Directory = "private"
		cfg.SummaryExport.ModelUsage = &ModelUsageConfig{Slice: "by_model", Models: map[string]string{"PRIVATE_MODEL": "public-model"}}
		edit(cfg)
		_, err := compileModelUsage(cfg)
		if err == nil || strings.Contains(err.Error(), "PRIVATE") {
			t.Fatalf("unsafe error: %v", err)
		}
	}
}

func TestModelUsageBatchCounterFailureAtomic(t *testing.T) {
	s, e, clk := modelFixture(t, func(c *Config) { c.TopK = 0 })
	mustConsumeTraces(t, s, modelTrace(maxInt64Value-1, func(a pcommon.Map) { a.PutInt("gen_ai.usage.output_tokens", 0) }))
	before := stateFingerprint(t, s)
	window := s.summary.windows[s.windowStart(clk.now)]
	rows, _ := json.Marshal(window.modelUsage)
	byteCount, lru := window.modelUsageBytes, s.nextLRUSeq
	first := modelTrace(0, func(a pcommon.Map) {
		a.PutInt("gen_ai.usage.output_tokens", 0)
		a.PutStr("gen_ai.request.model", "PRIVATE_NEW")
	})
	second := modelTrace(2, func(a pcommon.Map) { a.PutInt("gen_ai.usage.output_tokens", 0) })
	second.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).CopyTo(first.ResourceSpans().At(0).ScopeSpans().At(0).Spans().AppendEmpty())
	for range 2 {
		if _, _, err := s.ConsumeTraces(context.Background(), first); err == nil {
			t.Fatal("overflowing batch accepted")
		}
		after, _ := json.Marshal(s.summary.windows[s.windowStart(clk.now)].modelUsage)
		if !bytes.Equal(before, stateFingerprint(t, s)) || !bytes.Equal(rows, after) || byteCount != window.modelUsageBytes || lru != s.nextLRUSeq {
			t.Fatal("failed batch or its replay accepted a prefix")
		}
	}
	doc := exportDocs(t, s, e, clk.now.Add(time.Second))[0]
	if doc.Counters["requests"] != 1 {
		t.Fatal("failed batch entered summary")
	}
}

func TestModelUsageByteBudget(t *testing.T) {
	s, _, clk := modelFixture(t, func(c *Config) { c.MaxSlices = 5000; c.TopK = 0 })
	mustConsumeTraces(t, s, modelTrace(100000, nil))
	w := s.summary.windows[s.windowStart(clk.now)]
	row := w.modelUsage["public-model"]
	for range 30 {
		var err error
		row, err = modelusage.Add(row, row)
		if err != nil {
			t.Fatal(err)
		}
	}
	w.modelUsage = map[string]modelusage.Model{}
	for i := 0; i < 5000; i++ {
		name := fmt.Sprintf("model-%04d-%s", i, strings.Repeat("x", 117))
		w.modelUsage[name] = row
	}
	encodedModels, err := json.Marshal(w.modelUsage)
	if err != nil {
		t.Fatal(err)
	}
	w.modelUsageBytes = len(encodedModels)
	s.cfg.modelUsage.models["PRIVATE_MODEL"] = fmt.Sprintf("model-%04d-%s", 0, strings.Repeat("x", 117))
	w.counters.requests = row.Requests * 5000
	w.counters.inputTokens = row.Fields.Input.Tokens * 5000
	w.counters.outputTokens = row.Fields.Output.Tokens * 5000
	before := stateFingerprint(t, s)
	rows, _ := json.Marshal(w.modelUsage)
	_, _, err = s.ConsumeTraces(context.Background(), modelTrace(100000, nil))
	if err == nil || !strings.Contains(err.Error(), "byte budget") {
		t.Fatalf("expected size refusal, got %v; rows=%d", err, len(rows))
	}
	after, _ := json.Marshal(w.modelUsage)
	if !bytes.Equal(before, stateFingerprint(t, s)) || !bytes.Equal(rows, after) {
		t.Fatal("size rejection changed state")
	}
}

func BenchmarkModelUsageApply(b *testing.B) {
	for _, mode := range []string{"apply", "consume", "baseline"} {
		for _, n := range []int{1, 100, 1000} {
			b.Run(fmt.Sprintf("models=%d/%s", n, mode), func(b *testing.B) {
				b.Setenv("GENAI_SKETCH_SECRET", "test-secret-32-bytes-for-unit-tests")
				secret, err := sketchhash.SecretFromEnv("GENAI_SKETCH_SECRET")
				if err != nil {
					b.Fatal(err)
				}
				cfg := defaultConfig()
				cfg.TopK = 0
				cfg.Slices = []SliceConfig{{Name: "model", Keys: []string{"gen_ai.request.model"}}}
				cfg.SummaryExport.Directory = "unused"
				cfg.SummaryExport.ModelUsage = &ModelUsageConfig{Slice: "model", Models: map[string]string{}}
				for i := 0; i < n; i++ {
					id := fmt.Sprintf("model-%d", i)
					cfg.SummaryExport.ModelUsage.Models[id] = id
				}
				if mode == "baseline" {
					cfg.SummaryExport.ModelUsage = nil
				}
				clk := &fixedClock{now: time.Unix(120, 0)}
				s, err := newCollectorState(cfg, secret, clk)
				if err != nil {
					b.Fatal(err)
				}
				traces := modelTrace(100, nil)
				span := traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
				data := spanData{spanAttrs: span.Attributes(), resourceAttrs: traces.ResourceSpans().At(0).Resource().Attributes()}
				update, err := s.classifySpan(span, data)
				if err != nil {
					b.Fatal(err)
				}
				for i := 0; i < n; i++ {
					data.spanAttrs.PutStr("gen_ai.request.model", fmt.Sprintf("model-%d", i))
					if err := s.applySpan(data, update, clk.now, clk.now.UnixNano()); err != nil {
						b.Fatal(err)
					}
				}
				data.spanAttrs.PutStr("gen_ai.request.model", "model-0")
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if mode != "apply" {
						if _, _, err := s.ConsumeTraces(context.Background(), traces); err != nil {
							b.Fatal(err)
						}
						continue
					}
					if err := s.applySpan(data, update, clk.now, clk.now.UnixNano()); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
