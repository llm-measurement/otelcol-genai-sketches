// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex
package genaisketchconnector

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/frequentitems"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/hllpp"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
	"go.uber.org/zap"
)

func exportFixture(t *testing.T, producer string, now time.Time) (*collectorState, *summaryExporter, *fixedClock) {
	t.Helper()
	cfg := defaultConfig()
	cfg.SummaryExport = SummaryExportConfig{Directory: t.TempDir(), ProducerID: producer, ScopeID: "research-fleet", KeyID: "shared-key-1", Interval: time.Second}
	if err := os.Chmod(cfg.SummaryExport.Directory, 0700); err != nil {
		t.Fatal(err)
	}
	cfg.MaxSlices = 1
	cfg.MCP.Enabled = true
	cfg.MCP.ToolErrors.Enabled = true
	clk := &fixedClock{now: now}
	state := newTestStateWithClock(t, clk, cfg)
	e, err := newSummaryExporter(cfg, now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.root.Close() })
	return state, e, clk
}

func exportDocs(t *testing.T, s *collectorState, e *summaryExporter, now time.Time) []summary.Envelope {
	t.Helper()
	docs, err := s.summaryDocuments(e, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.write(docs, s.cutoffWindowStart(s.windowStart(now))); err != nil {
		t.Fatal(err)
	}
	return docs
}

func TestIndependentProducerSummaryExchange(t *testing.T) {
	start := time.Unix(120, 0)
	a, ae, ac := exportFixture(t, "cluster-a", start)
	b, be, bc := exportFixture(t, "cluster-b", start)
	for _, s := range []*collectorState{a, b} {
		mustConsumeTraces(t, s, tracesFromSpans(testSpan{Model: "model", User: "PRIVATE_USER_SENTINEL", Prompt: "PRIVATE_PROMPT_SENTINEL", Doc: "PRIVATE_DOC_SENTINEL"}))
	}
	first := exportDocs(t, a, ae, start.Add(10*time.Second))[0]
	// Enough model churn to overflow the label budget; scope totals still count once.
	for i := 0; i < 4; i++ {
		mustConsumeTraces(t, a, tracesFromSpans(testSpan{Model: fmt.Sprintf("model-%d", i), User: "PRIVATE_USER_SENTINEL", Prompt: "PRIVATE_PROMPT_SENTINEL"}))
	}
	ac.Set(start.Add(time.Minute))
	bc.Set(start.Add(time.Minute))
	adocs := exportDocs(t, a, ae, ac.Now())
	bdocs := exportDocs(t, b, be, bc.Now())
	result, err := summary.Combine([]summary.Envelope{first, adocs[0], bdocs[0], first, bdocs[0]}, []string{"cluster-a", "cluster-b", "offline"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Counters["requests"] != 6 || result.Counters["missing_token_usage"] != 6 || len(result.Missing) != 1 || result.Missing[0] != "offline" || len(result.Partial) != 0 {
		t.Fatalf("wrong combined accounting: %+v", result)
	}
	h, err := hllpp.Parse(result.Sketches["distinct_users"].Data)
	if err != nil {
		t.Fatal(err)
	}
	if h.Estimate() < 0.99 || h.Estimate() > 1.01 {
		t.Fatalf("cross-system shared user estimate = %v", h.Estimate())
	}
	if adocs[1].Counters["requests"] != 0 {
		t.Fatal("window counter reused cumulative metric count")
	}
	for _, doc := range append(adocs, bdocs...) {
		encoded, err := doc.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := summary.Parse(encoded); err != nil {
			t.Fatal(err)
		}
		for _, sentinel := range []string{"PRIVATE_USER_SENTINEL", "PRIVATE_PROMPT_SENTINEL", "PRIVATE_DOC_SENTINEL"} {
			if bytes.Contains(encoded, []byte(sentinel)) {
				t.Fatal("raw field in summary")
			}
			for _, p := range doc.Sketches {
				if bytes.Contains(p.Data, []byte(sentinel)) {
					t.Fatal("raw field inside encoded payload")
				}
			}
		}
	}
	for _, e := range []*summaryExporter{ae, be} {
		entries, err := os.ReadDir(e.cfg.Directory)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			info, err := entry.Info()
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0600 {
				t.Fatal("non-private summary file")
			}
		}
	}
}

func TestSummaryRestartRetentionAndMCPPrivacy(t *testing.T) {
	start := time.Unix(120, 0)
	s, e, clk := exportFixture(t, "a", start)
	fixture := readFleetFixture(t, "mcp_tree.json")
	mustConsumeTraces(t, s, tracesFromFixtureResources(t, fixture.Resources))
	before := exportDocs(t, s, e, start.Add(20*time.Second))[0]
	s2, e2, _ := exportFixture(t, "a", start.Add(20*time.Second))
	if err := e2.root.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	e2.root, err = os.OpenRoot(e.cfg.Directory)
	if err != nil {
		t.Fatal(err)
	}
	e2.cfg.Directory = e.cfg.Directory
	after := exportDocs(t, s2, e2, start.Add(time.Minute))[0]
	if e.epoch == e2.epoch {
		t.Fatal("epoch reused")
	}
	r, err := summary.Combine([]summary.Envelope{before, after, before}, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Counters["requests"] != 1 || r.Counters["input_tokens"] != 16 || r.Counters["output_tokens"] != 9 || len(r.Partial) != 0 {
		t.Fatal("restart erased earlier observations")
	}
	for _, p := range r.Sketches {
		if bytes.Contains(p.Data, []byte(fixture.Sentinel)) {
			t.Fatal("MCP URI sentinel in summary state")
		}
	}
	clk.Set(start.Add(20 * time.Minute))
	exportDocs(t, s, e, clk.Now())
	entries, err := os.ReadDir(e.cfg.Directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != s.cfg.retentionWindows || len(s.summary.windows) != s.cfg.retentionWindows {
		t.Fatal("unbounded file or memory retention")
	}
}

func TestSummaryDirectoryAndConfigSafety(t *testing.T) {
	cfg := defaultConfig()
	cfg.SummaryExport = SummaryExportConfig{Directory: t.TempDir(), ProducerID: "a", ScopeID: "fleet", KeyID: "v1", Interval: time.Second}
	if err := os.Chmod(cfg.SummaryExport.Directory, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := newSummaryExporter(cfg, time.Now()); err == nil {
		t.Fatal("accepted public directory")
	}
	if err := os.Chmod(cfg.SummaryExport.Directory, 0700); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []SummaryExportConfig{{ProducerID: "a"}, {Directory: cfg.SummaryExport.Directory, ProducerID: "../a", ScopeID: "fleet", KeyID: "v1", Interval: time.Second}, {Directory: cfg.SummaryExport.Directory, ProducerID: "a", ScopeID: "fleet", KeyID: "v1", Interval: 0}} {
		if err := invalid.Validate(time.Minute); err == nil {
			t.Fatal("invalid export config accepted")
		}
	}
	s, e, _ := exportFixture(t, "a", time.Unix(120, 0))
	outside := filepath.Join(t.TempDir(), "private")
	if err := os.WriteFile(outside, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("%020d-%s.json", int64(120e9), e.epoch)
	if err := os.Symlink(outside, filepath.Join(e.cfg.Directory, name)); err != nil {
		t.Fatal(err)
	}
	docs, err := s.summaryDocuments(e, time.Unix(121, 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.write(docs, 0); err == nil {
		t.Fatal("symlink export accepted")
	}
	data, err := os.ReadFile(outside)
	if err != nil || string(data) != "unchanged" {
		t.Fatal("outside file modified")
	}
	if err := os.Remove(filepath.Join(e.cfg.Directory, name)); err != nil {
		t.Fatal(err)
	}
	temporary := "." + name + "-99.tmp"
	if err := os.WriteFile(filepath.Join(e.cfg.Directory, temporary), []byte("interrupted"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := e.write(docs, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.cfg.Directory, temporary)); !os.IsNotExist(err) {
		t.Fatal("interrupted temporary file retained")
	}
}

func TestSummaryWeightedBoundsAndContractFingerprint(t *testing.T) {
	s, e, _ := exportFixture(t, "a", time.Unix(120, 0))
	for i := 0; i < 900; i++ {
		traces := tracesWithGenAISpans(1)
		attrs := traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Attributes()
		attrs.PutStr("gen_ai.request.prompt", fmt.Sprintf("prompt-%d", i))
		attrs.PutInt("gen_ai.usage.input_tokens", 3)
		attrs.PutInt("gen_ai.usage.output_tokens", 2)
		mustConsumeTraces(t, s, traces)
	}
	doc := exportDocs(t, s, e, time.Unix(150, 0))[0]
	sketch, err := frequentitems.Parse(doc.Sketches["top_prompts"].Data)
	if err != nil {
		t.Fatal(err)
	}
	if sketch.TotalWeight() != 4500 || sketch.MaxError() == 0 {
		t.Fatal("full nontrivial frequent-items state not exported")
	}
	for i := 0; i < 900; i++ {
		hash, err := s.hashField(s.cfg.fields[fieldPromptKey], fmt.Sprintf("prompt-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		if sketch.LowerBoundHash(hash) > 5 || sketch.UpperBoundHash(hash) < 5 {
			t.Fatal("bounds fail exact reference")
		}
	}
	if !strings.HasPrefix(doc.AccountingID, "genai-accounting-v1:") {
		t.Fatal("missing accounting fingerprint")
	}
}

func TestSummaryDiskBudgetAndBackwardClock(t *testing.T) {
	s, e, _ := exportFixture(t, "a", time.Unix(120, 0))
	docs, err := s.summaryDocuments(e, time.Unix(121, 0))
	if err != nil {
		t.Fatal(err)
	}
	e.lastExport = time.Unix(122, 0)
	if _, err := s.summaryDocuments(e, time.Unix(121, 0)); err == nil {
		t.Fatal("backward clock accepted")
	}
	file, err := e.root.OpenFile("00000000000000000000-00000000000000000000000000000000.json", os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxSummaryDiskBytes); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := e.write(docs, 0); err == nil {
		t.Fatal("disk budget exceeded silently")
	}
}

func TestSummaryZeroLengthObservationAcceptance(t *testing.T) {
	for _, kind := range []string{"empty", "counters_only", "sketches_only", "counters_and_sketches"} {
		t.Run(kind, func(t *testing.T) {
			start := time.Unix(120, 0)
			s, e, clk := exportFixture(t, "a", start)
			if kind != "empty" {
				mustConsume(t, s, testSpan{Model: "model", Prompt: "prompt", User: "user"})
			}
			docs, err := s.summaryDocuments(e, clk.Now())
			if err != nil || len(docs) != 1 {
				t.Fatalf("documents: %v, count=%d", err, len(docs))
			}
			doc := docs[0]
			if doc.ObservedStart != start.UnixNano() || doc.ObservedEnd != start.UnixNano() {
				t.Fatal("exporter fabricated a positive observation interval")
			}
			if kind == "counters_only" {
				doc.Sketches = map[string]summary.Payload{}
			} else if kind == "sketches_only" {
				doc.Counters = map[string]uint64{}
			}
			if err := doc.Validate(); (err == nil) != (kind == "empty") {
				t.Fatalf("zero-length acceptance for %s: %v", kind, err)
			}
			if err := e.write([]summary.Envelope{doc}, 0); (err == nil) != (kind == "empty") {
				t.Fatalf("zero-length write for %s: %v", kind, err)
			}
			// Positive intervals must still accept the same retained measurements.
			doc.ObservedEnd++
			doc.EmittedAt++
			encoded, err := doc.MarshalBinary()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := summary.Parse(encoded); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSummaryFixedClockExportRetry(t *testing.T) {
	start := time.Unix(120, 0)
	s, e, clk := exportFixture(t, "a", start)
	c := &tracesConnector{state: s, exporter: e, clock: clk, logger: zap.NewNop()}
	if err := c.emitSummary(true); err != nil {
		t.Fatal("empty startup export rejected:", err)
	}
	name := filepath.Join(e.cfg.Directory, fmt.Sprintf("%020d-%s.json", start.UnixNano(), e.epoch))
	before, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	mustConsume(t, s, testSpan{Model: "model", Prompt: "prompt", InputTokens: ptr(int64(3)), OutputTokens: ptr(int64(2))})
	for i := 0; i < 2; i++ {
		if err := c.emitSummary(true); err == nil || !strings.Contains(err.Error(), "nonempty zero-length") {
			t.Fatalf("nonempty fixed-clock export must fail explicitly: %v", err)
		}
		after, err := os.ReadFile(name)
		if err != nil || !bytes.Equal(before, after) || !e.lastExport.Equal(start) {
			t.Fatal("failed export replaced the last valid file or advanced lastExport")
		}
	}
	clk.Set(start.Add(time.Nanosecond))
	if err := c.emitSummary(true); err != nil {
		t.Fatal("positive-interval retry failed:", err)
	}
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := summary.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Counters["requests"] != 1 || doc.Counters["input_tokens"] != 3 || doc.Counters["output_tokens"] != 2 ||
		doc.ObservedEnd-doc.ObservedStart != 1 || !e.lastExport.Equal(clk.Now()) {
		t.Fatal("retry lost measurements or misstated the observed interval")
	}
}
