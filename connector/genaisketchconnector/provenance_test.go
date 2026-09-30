// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package genaisketchconnector

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/frequentitems"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func TestLiteLLMUsageProvenanceFixtures(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "examples", "integrations", "litellm", "usage-cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Version string `json:"litellm_version"`
		Cases   []struct {
			Name     string           `json:"name"`
			Emitted  map[string]int64 `json:"emitted_usage"`
			Expected [2]string        `json:"expected"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Version != "1.102.1" || len(fixture.Cases) != 6 {
		t.Fatal("unreviewed fixture version")
	}
	output := os.Getenv("USAGE_PROVENANCE_FIXTURE_OUTPUT")
	if output != "" {
		if err := os.Mkdir(output, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for caseIndex, tc := range fixture.Cases {
		for _, annotated := range []bool{false, true} {
			t.Run(tc.Name+map[bool]string{false: "/stock", true: "/source-annotated"}[annotated], func(t *testing.T) {
				cfg := defaultConfig()
				cfg.Slices = []SliceConfig{{Name: "model", Keys: []string{"gen_ai.request.model"}}}
				cfg.SummaryExport = SummaryExportConfig{Directory: t.TempDir(), ProducerID: "app", ScopeID: "usage-test", KeyID: "test", Interval: time.Second}
				if err := os.Chmod(cfg.SummaryExport.Directory, 0700); err != nil {
					t.Fatal(err)
				}
				clock := &fixedClock{now: time.Unix(120+int64(caseIndex)*60, 0)}
				s := newTestStateWithClock(t, clock, cfg)
				traces := ptrace.NewTraces()
				spans := traces.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Spans()
				attrs := spans.AppendEmpty().Attributes()
				attrs.PutStr("gen_ai.request.model", "mock-model")
				attrs.PutStr("gen_ai.provider.name", "openai")
				attrs.PutStr("gen_ai.request.prompt", "PRIVATE_PROMPT_SENTINEL")
				attrs.PutInt("gen_ai.usage.input_tokens", tc.Emitted["prompt_tokens"])
				attrs.PutInt("gen_ai.usage.output_tokens", tc.Emitted["completion_tokens"])
				for field, name := range usageProvenanceFields {
					if annotated {
						attrs.PutStr("gen_ai_sketch.usage."+name+".provenance", tc.Expected[field])
					}
				}
				// The captured proxy wrappers carry no model attribute.
				spans.AppendEmpty().SetName("proxy_pre_call")
				metrics := mustConsumeTraces(t, s, traces)
				wantInput, wantOutput := tc.Emitted["prompt_tokens"], tc.Emitted["completion_tokens"]
				var wantMissing int64
				if annotated && tc.Expected[0] == "unavailable" {
					wantInput, wantMissing = 0, 1
				}
				if annotated && tc.Expected[1] == "unavailable" {
					wantOutput, wantMissing = 0, 1
				}
				if sumMetricValues(metrics, requestsMetricName) != 1 || sumMetricValues(metrics, missingTokenUsageMetricName) != wantMissing {
					t.Fatal("missing usage accounting differs")
				}
				e, err := newSummaryExporter(cfg, clock.Now())
				if err != nil {
					t.Fatal(err)
				}
				defer e.root.Close()
				clock.Set(clock.Now().Add(time.Minute))
				doc := exportDocs(t, s, e, clock.Now())[0]
				if output != "" {
					mode := "stock"
					if annotated {
						mode = "source-annotated"
					}
					dir := filepath.Join(output, mode, tc.Name)
					if err := os.MkdirAll(dir, 0700); err != nil {
						t.Fatal(err)
					}
					encoded, err := doc.MarshalBinary()
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(dir, "app.json"), encoded, 0600); err != nil {
						t.Fatal(err)
					}
				}
				for field, name := range usageProvenanceFields {
					want := "unknown"
					if annotated {
						want = tc.Expected[field]
					}
					for _, source := range usageProvenanceStates {
						var n int64
						if source == want {
							n = 1
						}
						if got := sumMetricValuesWithLabels(metrics, usageProvenanceMetricName, map[string]string{"token_field": name, "source": source}); got != n {
							t.Fatalf("%s/%s: %d != %d", name, source, got, n)
						}
						if doc.Counters["usage_provenance.v1."+name+"."+source] != uint64(n) {
							t.Fatal(doc.Counters)
						}
					}
				}
				if doc.Counters["input_tokens"] != uint64(wantInput) || doc.Counters["output_tokens"] != uint64(wantOutput) || doc.Counters["missing_token_usage"] != uint64(wantMissing) {
					t.Fatal("unavailable counts must not become real zeros")
				}
				prompt, err := frequentitems.Parse(doc.Sketches["top_prompts"].Data)
				if err != nil {
					t.Fatal(err)
				}
				wantWeight := wantInput + wantOutput
				if wantMissing != 0 {
					wantWeight = 0
				}
				if prompt.TotalWeight() != wantWeight {
					t.Fatal("missing usage weighted a prompt")
				}
			})
		}
	}
}

func TestUsageProvenanceDoesNotInferFromNumbers(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		value        any
		want         string
	}{
		{"unknown zero", "", int64(0), "unknown"},
		{"declared zero", "provider_reported", int64(0), "provider_reported"},
		{"estimate", "inferred", int64(3), "inferred"},
		{"zero filled", "unavailable", int64(0), "unavailable"},
		{"unidentified estimate", "unavailable", int64(99), "unavailable"},
		{"missing source count", "provider_reported", nil, "unknown"},
		{"invalid source count", "provider_reported", "not-a-count", "unknown"},
		{"untrusted state", "PRIVATE_SENTINEL", int64(8), "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attrs := pcommon.NewMap()
			attrs.PutInt("gen_ai.usage.output_tokens", 0)
			if tc.source != "" {
				attrs.PutStr("gen_ai_sketch.usage.input.provenance", tc.source)
			}
			if tc.value != nil {
				if err := attrs.PutEmpty("gen_ai.usage.input_tokens").FromRaw(tc.value); err != nil {
					t.Fatal(err)
				}
			}
			cfg := newTestState(t, defaultConfig()).cfg
			totals, err := tokenTotals(attrs, cfg)
			if err != nil {
				t.Fatal(err)
			}
			if tc.source == "unavailable" && (totals.inputTokens != 0 || totals.missingTokens != 1 || totals.tokenObservations[tokenFieldInput][tokenStateMissing] != 1) {
				t.Fatal("declared unavailable must override even a positive placeholder")
			}
			if tc.want == "provider_reported" && totals.missingTokens != 0 {
				t.Fatal("genuine provider zero is not missing")
			}
			for i, source := range usageProvenanceStates {
				if (totals.usageProvenance[0][i] == 1) != (source == tc.want) {
					t.Fatal(totals.usageProvenance)
				}
			}
		})
	}
}

func TestUsageProvenancePrivacyAndOverflow(t *testing.T) {
	s := newTestState(t, defaultConfig())
	traces := ptrace.NewTraces()
	attrs := traces.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Spans().AppendEmpty().Attributes()
	attrs.PutStr("gen_ai.request.model", "mock")
	attrs.PutStr("gen_ai_sketch.usage.input.provenance", "PRIVATE_SENTINEL")
	metrics := mustConsumeTraces(t, s, traces)
	if bytes.Contains(marshalMetrics(t, metrics), []byte("PRIVATE_SENTINEL")) {
		t.Fatal("arbitrary provenance escaped into metrics")
	}
	c := accountingCounters{}
	c.usageProvenance[0][3] = math.MaxUint64
	u := spanUpdate{totals: spanTotals{}}
	u.totals.usageProvenance[0][3] = 1
	before := c
	if c.checkCounters(u, preparedUpdate{}, math.MaxUint64) == nil || c != before {
		t.Fatal("overflow must fail without mutation")
	}
}
