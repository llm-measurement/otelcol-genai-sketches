// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package genaisketchconnector

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

// Synthetic, source-derived OTLP, not a captured LiteLLM proxy session. The same
// spans are partitioned, never duplicated, in the two-producer equivalence check.
func TestLiteLLMInvestigationFixtures(t *testing.T) {
	base := filepath.Join("..", "..", "examples", "integrations", "litellm")
	output := os.Getenv("LITELLM_FIXTURE_OUTPUT")
	if output != "" {
		if err := os.Mkdir(output, 0700); err != nil {
			t.Fatal("fixture output must be a new directory:", err)
		}
	}
	for wi, name := range []string{"before", "after"} {
		data, err := os.ReadFile(filepath.Join(base, name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		traces, err := (&ptrace.JSONUnmarshaler{}).UnmarshalTraces(data)
		if err != nil {
			t.Fatal(err)
		}
		start := time.Unix(120+int64(wi)*60, 0)
		run := func(producer, epoch string, spans ptrace.Traces) summary.Envelope {
			cfg := defaultConfig()
			cfg.MaxSlices = 1
			cfg.MCP.Enabled = true
			cfg.Fields[fieldPromptKey] = FieldConfig{FromAttributes: []string{"app.prompt.template"}, Canonicalization: "text_v1", Domain: "prompt:v1"}
			cfg.SummaryExport = SummaryExportConfig{Directory: t.TempDir(), ProducerID: producer, ScopeID: "app-investigation", KeyID: "synthetic-key", Interval: time.Second}
			if err := os.Chmod(cfg.SummaryExport.Directory, 0700); err != nil {
				t.Fatal(err)
			}
			clock := &fixedClock{now: start}
			s := newTestStateWithClock(t, clock, cfg)
			e, err := newSummaryExporter(cfg, start)
			if err != nil {
				t.Fatal(err)
			}
			defer e.root.Close()
			e.epoch = epoch // Deterministic synthetic provenance, never production metadata.
			metrics := mustConsumeTraces(t, s, spans)
			top, err := s.TopKSnapshot(clock.Now())
			if err != nil || top.ItemCount() == 0 {
				t.Fatal("top-k scan did not exercise its surface", err)
			}
			metricBytes := marshalMetrics(t, metrics)
			for _, slice := range top.Slices {
				for _, item := range slice.Items {
					if bytes.Contains(metricBytes, []byte(item.Hash)) {
						t.Fatal("top-k hash leaked into metrics")
					}
				}
			}
			clock.Set(start.Add(time.Minute))
			doc := exportDocs(t, s, e, clock.Now())[0]
			for _, surface := range [][]byte{marshalMetrics(t, metrics), mustJSON(t, top), mustJSON(t, doc)} {
				if bytes.Contains(surface, []byte("PRIVATE_LITELLM")) {
					t.Fatal("sentinel leaked")
				}
			}
			for _, payload := range doc.Sketches {
				if bytes.Contains(payload.Data, []byte("PRIVATE_LITELLM")) {
					t.Fatal("sentinel in sketch bytes")
				}
			}
			return doc
		}
		whole := run("app", "00000000000000000000000000000001", traces)
		wantRequests, wantInput, wantOutput := uint64(2), uint64(160), uint64(40)
		if wi == 1 {
			wantRequests, wantInput, wantOutput = 3, 480, 120
		}
		if whole.Counters["requests"] != wantRequests || whole.Counters["input_tokens"] != wantInput || whole.Counters["output_tokens"] != wantOutput || whole.Counters["missing_token_usage"] != 0 || whole.Counters["agent_runs"] != 0 || whole.Counters["cache_read_input_tokens"] != 10 {
			t.Fatal(whole.Counters)
		}
		gateway, direct := ptrace.NewTraces(), ptrace.NewTraces()
		traces.CopyTo(gateway)
		traces.CopyTo(direct)
		keep := func(ts ptrace.Traces, first bool) {
			spans := ts.ResourceSpans().At(0).ScopeSpans().At(0).Spans()
			seen := 0
			spans.RemoveIf(func(span ptrace.Span) bool {
				_, model := span.Attributes().Get("gen_ai.operation.name")
				if !model {
					return !first
				}
				seen++
				return (seen == 1) != first
			})
		}
		keep(gateway, true)
		keep(direct, false)
		a := run("gateway", "00000000000000000000000000000002", gateway)
		b := run("direct", "00000000000000000000000000000003", direct)
		combined, err := summary.Combine([]summary.Envelope{a, b}, []string{"gateway", "direct"})
		if err != nil || !reflect.DeepEqual(combined.Counters, whole.Counters) || !reflect.DeepEqual(combined.Sketches, whole.Sketches) {
			t.Fatal("partitioned scope differs from single app", err)
		}
		write := func(path string, doc summary.Envelope) {
			if output == "" {
				return
			}
			dir := filepath.Join(output, path, name)
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			encoded, err := doc.MarshalBinary()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, doc.ProducerID+".json"), encoded, 0600); err != nil {
				t.Fatal(err)
			}
		}
		write("single", whole)
		write("two-stacks", a)
		write("two-stacks", b)
		if wi == 1 {
			spans := traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans()
			spans.At(1).Attributes().Remove("gen_ai.usage.output_tokens")
			missing := run("app", "00000000000000000000000000000001", traces)
			if missing.Counters["missing_token_usage"] != 1 || missing.Counters["input_tokens"] != 480 || missing.Counters["output_tokens"] != 80 {
				t.Fatal(missing.Counters)
			}
			write("missing-usage", missing)
		}
	}
}
