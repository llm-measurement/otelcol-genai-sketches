// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package genaisketchconnector

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	sketchfi "github.com/llm-measurement/llm-sketchkit/go/sketchkit/frequentitems"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
	"go.opentelemetry.io/collector/pdata/pmetric"
)

// These digests pin the canonical default export and snapshot before configurable keys.
func TestDefaultTopKBytes(t *testing.T) {
	start := time.Unix(120, 0)
	s, e, _ := exportFixture(t, "baseline", start)
	e.epoch = "baseline"
	in, out := int64(20), int64(5)
	mustConsume(t, s, testSpan{Model: "model", Team: "team", User: "user", Prompt: "prompt", InputTokens: &in, OutputTokens: &out})
	doc := exportDocs(t, s, e, start.Add(30*time.Second))[0]
	encoded, err := doc.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.TopKSnapshot(start.Add(30 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	snapshotBytes, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"summary": encoded, "snapshot": snapshotBytes} {
		want := map[string]string{"summary": "4f8890b5d382b84b2840ea6e5d01d0ae39bc1538c453d00dd282e98f02775bdb", "snapshot": "7bb9328ddb0df2ea192794e8b61bfe84cbe8c885cf5d71b082bc08bc078418b8"}[name]
		if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != want {
			t.Fatalf("%s default bytes changed: %s", name, got)
		}
	}
}

func TestTopKKeysValidation(t *testing.T) {
	for name, keys := range map[string][]TopKKeyConfig{
		"unknown":   {{Field: "unknown"}},
		"duplicate": {{Field: fieldUserKey}, {Field: fieldUserKey, Weight: "requests"}},
		"weight":    {{Field: fieldSessionKey, Weight: "dollars"}},
		"limit":     {{Field: fieldUserKey}, {Field: fieldPromptKey}, {Field: fieldDocKey}, {Field: fieldSessionKey}, {Field: fieldMCPSessionKey}},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := defaultConfig()
			cfg.TopKKeys = keys
			if cfg.Validate() == nil {
				t.Fatal("invalid topk_keys accepted")
			}
		})
	}
	for _, attribute := range []string{"gen_ai.conversation.id", "session.id", "enduser.id", "user.id"} {
		for _, topK := range []int{0, 20} {
			t.Run(fmt.Sprintf("%s/topk=%d", attribute, topK), func(t *testing.T) {
				cfg := defaultConfig()
				cfg.TopK = topK
				cfg.Slices = []SliceConfig{{Name: "unsafe", Keys: []string{attribute}}}
				if cfg.Validate() == nil {
					t.Fatal("sensitive session/user slice accepted")
				}
			})
		}
	}
}

func topKExportFixture(t *testing.T, keys []TopKKeyConfig, edit func(*Config)) (*collectorState, *summaryExporter) {
	t.Helper()
	cfg := defaultConfig()
	cfg.TopKKeys = keys
	cfg.SummaryExport = SummaryExportConfig{Directory: t.TempDir(), ProducerID: "a", ScopeID: "app", KeyID: "key", Interval: time.Second}
	if err := os.Chmod(cfg.SummaryExport.Directory, 0700); err != nil {
		t.Fatal(err)
	}
	if edit != nil {
		edit(cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	start := time.Unix(120, 0)
	s := newTestStateWithClock(t, &fixedClock{now: start}, cfg)
	e, err := newSummaryExporter(cfg, start)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.root.Close() })
	return s, e
}

func TestTopKKeysWeightsPrivacyAndMerging(t *testing.T) {
	keys := []TopKKeyConfig{{Field: fieldPromptKey}, {Field: fieldUserKey}, {Field: fieldSessionKey, Weight: "requests"}, {Field: fieldDocKey}}
	s, e := topKExportFixture(t, keys, nil)
	in, out := int64(20), int64(5)
	traces := tracesFromSpans(
		testSpan{Model: "model", User: "PRIVATE_USER", Prompt: "PRIVATE_PROMPT", Doc: "PRIVATE_DOC", InputTokens: &in, OutputTokens: &out},
		testSpan{Model: "model", User: "PRIVATE_USER", Prompt: "PRIVATE_PROMPT", Doc: "PRIVATE_DOC"},
		testSpan{Model: "model", User: "PRIVATE_USER", Prompt: "PRIVATE_PROMPT", InputTokens: &in, OutputTokens: &out},
	)
	spans := traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans()
	for i := 0; i < spans.Len(); i++ {
		// Same response ID models retry observations, not unique logical requests.
		spans.At(i).Attributes().PutStr("gen_ai.response.id", "PRIVATE_RESPONSE")
		spans.At(i).Attributes().PutStr("session.id", "PRIVATE_SESSION")
	}
	metrics, _, err := s.ConsumeTraces(context.Background(), traces)
	if err != nil {
		t.Fatal(err)
	}
	doc := exportDocs(t, s, e, time.Unix(180, 0))[0]
	for name, want := range map[string]int64{"top_prompts": 50, "top_users": 50, "top_sessions_requests": 3, "top_docs": 25} {
		f, err := sketchfi.Parse(doc.Sketches[name].Data)
		if err != nil {
			t.Fatal(err)
		}
		items, err := f.FrequentItems(sketchfi.NoFalseNegatives)
		if err != nil || f.TotalWeight() != want || len(items) != 1 || items[0].LowerBound != want || items[0].UpperBound != want {
			t.Fatalf("%s: %+v total %d", name, items, f.TotalWeight())
		}
	}
	other := doc
	other.ProducerID = "b"
	r, err := summary.Combine([]summary.Envelope{doc, other}, []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	f, _ := sketchfi.Parse(r.Sketches["top_users"].Data)
	if f.TotalWeight() != 100 || r.Counters["requests"] != 6 {
		t.Fatal("independent producers did not merge")
	}
	encoded, err := doc.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := summary.Parse(encoded); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.TopKSnapshot(time.Unix(150, 0))
	if err != nil {
		t.Fatal(err)
	}
	snapshotBytes, _ := json.Marshal(snapshot)
	metricBytes, err := (&pmetric.JSONMarshaler{}).MarshalMetrics(metrics)
	if err != nil {
		t.Fatal(err)
	}
	for _, sentinel := range []string{"PRIVATE_USER", "PRIVATE_PROMPT", "PRIVATE_DOC", "PRIVATE_SESSION", "PRIVATE_RESPONSE"} {
		for _, surface := range [][]byte{encoded, snapshotBytes, metricBytes} {
			if bytes.Contains(surface, []byte(sentinel)) {
				t.Fatal("private value in output")
			}
		}
		for _, payload := range doc.Sketches {
			if bytes.Contains(payload.Data, []byte(sentinel)) {
				t.Fatal("private value inside payload")
			}
		}
	}
	for _, item := range snapshot.Slices {
		for _, candidate := range item.Items {
			if bytes.Contains(metricBytes, []byte(candidate.Hash)) {
				t.Fatal("top-k hash entered metric surface")
			}
		}
		if item.Field == fieldSessionKey && item.Weight != "requests" {
			t.Fatal("request unit omitted")
		}
	}
}

func TestTopKKeysContractsAndDisable(t *testing.T) {
	_, baseline := topKExportFixture(t, nil, nil)
	for _, weight := range []string{"tokens", "requests"} {
		s, e := topKExportFixture(t, []TopKKeyConfig{{Field: fieldSessionKey, Weight: weight}}, nil)
		if e.accountingID != baseline.accountingID {
			t.Fatal("optional top-k changed base accounting")
		}
		in, out := int64(1), int64(2)
		traces := tracesFromSpans(testSpan{Model: "model", InputTokens: &in, OutputTokens: &out}, testSpan{Model: "model"})
		for i := 0; i < 2; i++ {
			traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(i).Attributes().PutStr("gen_ai.conversation.id", "PRIVATE_SESSION")
		}
		mustConsumeTraces(t, s, traces)
		doc := exportDocs(t, s, e, time.Unix(180, 0))[0]
		want := int64(3)
		if weight == "requests" {
			want = 2
		}
		f, err := sketchfi.Parse(doc.Sketches[(TopKKeyConfig{Field: fieldSessionKey, Weight: weight}).measurement()].Data)
		if err != nil || f.TotalWeight() != want {
			t.Fatal("wrong session weight", err)
		}
		for _, mutation := range []string{"domain", "source"} {
			x, xe := topKExportFixture(t, []TopKKeyConfig{{Field: fieldSessionKey, Weight: weight}}, func(c *Config) {
				f := c.Fields[fieldSessionKey]
				if mutation == "domain" {
					f.Domain = "user:v1"
				} else {
					f.FromAttributes = []string{"different.session"}
				}
				c.Fields[fieldSessionKey] = f
			})
			changed := exportDocs(t, x, xe, time.Unix(180, 0))[0]
			changed.ProducerID = "b"
			if xe.accountingID != baseline.accountingID {
				t.Fatal("optional extraction rules changed accounting ID")
			}
			if _, err := summary.Combine([]summary.Envelope{doc, changed}, []string{"a", "b"}); err == nil {
				t.Fatal("incompatible top-k extraction accepted")
			}
		}
	}
	s, e := topKExportFixture(t, []TopKKeyConfig{{Field: fieldUserKey}, {Field: fieldSessionKey}}, func(c *Config) { c.TopK = 0; c.MCP.Enabled = true; c.MCP.ToolErrors.Enabled = true })
	doc := exportDocs(t, s, e, time.Unix(180, 0))[0]
	for _, p := range doc.Sketches {
		if p.Kind == "frequent_items" {
			t.Fatal("topk: 0 allocated FI state")
		}
	}
	if len(e.topKContracts) != 0 {
		t.Fatal("disabled keys exported contracts")
	}
}

func TestTopKKeysOversizedSessionDoesNotMutate(t *testing.T) {
	s, _ := topKExportFixture(t, []TopKKeyConfig{{Field: fieldSessionKey, Weight: "requests"}}, nil)
	traces := tracesFromSpans(testSpan{Model: "model"})
	private := strings.Repeat("PRIVATE_SESSION", maxAttributeValueBytes)
	traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Attributes().PutStr("session.id", private)
	_, _, err := s.ConsumeTraces(context.Background(), traces)
	if err == nil || strings.Contains(err.Error(), "PRIVATE_SESSION") || len(s.slices) != 0 {
		t.Fatal("oversized session accepted, echoed, or mutated state")
	}
}

func TestExplicitDefaultTopKAndCustomKeyPrivacy(t *testing.T) {
	var baseline []byte
	for _, keys := range [][]TopKKeyConfig{nil, {{Field: fieldPromptKey, Weight: "tokens"}}} {
		s, e := topKExportFixture(t, keys, nil)
		e.epoch = "fixed"
		mustConsume(t, s, testSpan{Model: "model", Prompt: "prompt", InputTokens: ptr(int64(2)), OutputTokens: ptr(int64(3))})
		doc := exportDocs(t, s, e, time.Unix(180, 0))[0]
		data, err := doc.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		if baseline == nil {
			baseline = data
		} else if !bytes.Equal(baseline, data) {
			t.Fatal("explicit prompt default changed bytes")
		}
	}
	s, e := topKExportFixture(t, []TopKKeyConfig{{Field: "virtual_key", Weight: "requests"}}, func(c *Config) {
		c.Dedup.Enabled = true
		c.Fields["virtual_key"] = FieldConfig{FromAttributes: []string{"app.virtual_key"}, Canonicalization: "text_v1", Domain: "user:v1"}
	})
	traces := tracesFromSpans(testSpan{Model: "model", RequestID: "same-attempt"}, testSpan{Model: "model", RequestID: "same-attempt"})
	for i := 0; i < 2; i++ {
		traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(i).Attributes().PutStr("app.virtual_key", "PRIVATE_VIRTUAL_KEY")
	}
	metrics := mustConsumeTraces(t, s, traces)
	doc := exportDocs(t, s, e, time.Unix(180, 0))[0]
	f, err := sketchfi.Parse(doc.Sketches["top_key.virtual_key.requests"].Data)
	if err != nil {
		t.Fatal(err)
	}
	if f.TotalWeight() != 1 || doc.Counters["requests"] != 1 || doc.Counters["dedup_suppressed"] != 1 {
		t.Fatal("top-k ignored deduplication")
	}
	snapshot, err := s.TopKSnapshot(time.Unix(150, 0))
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := doc.MarshalBinary()
	snapshotBytes, _ := json.Marshal(snapshot)
	metricBytes, _ := (&pmetric.JSONMarshaler{}).MarshalMetrics(metrics)
	for _, surface := range [][]byte{encoded, snapshotBytes, metricBytes, doc.Sketches["top_key.virtual_key.requests"].Data} {
		if bytes.Contains(surface, []byte("PRIVATE_VIRTUAL_KEY")) {
			t.Fatal("custom key leaked")
		}
	}
}

func TestTopKKeysSnapshotCapIsGlobal(t *testing.T) {
	s, _ := topKExportFixture(t, []TopKKeyConfig{{Field: fieldPromptKey}, {Field: fieldUserKey}, {Field: fieldSessionKey, Weight: "requests"}, {Field: fieldDocKey}}, func(c *Config) {
		c.TopK = 100
		c.Slices = []SliceConfig{{Name: "by_model", Keys: []string{"gen_ai.request.model"}}}
	})
	for model := 0; model < 30; model++ {
		spans := make([]testSpan, 100)
		for i := range spans {
			key := fmt.Sprint(i)
			spans[i] = testSpan{Model: fmt.Sprint(model), User: key, Prompt: key, Doc: key, InputTokens: ptr(int64(1)), OutputTokens: ptr(int64(1))}
		}
		traces := tracesFromSpans(spans...)
		for i := range spans {
			traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(i).Attributes().PutStr("session.id", fmt.Sprint(i))
		}
		mustConsumeTraces(t, s, traces)
	}
	snapshot, err := s.TopKSnapshot(time.Unix(150, 0))
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Truncated || snapshot.ItemCount() != maxTopKSnapshotItems {
		t.Fatalf("cap not global: %d", snapshot.ItemCount())
	}
}
