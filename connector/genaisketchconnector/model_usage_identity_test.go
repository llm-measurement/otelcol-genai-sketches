// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package genaisketchconnector

import (
	"bytes"
	"encoding/json"
	"maps"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/modelusage"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func servedFixtureConfig(c *Config) {
	c.SummaryExport.ModelUsage.Identity = modelIdentityLiteLLM
	c.SummaryExport.ModelUsage.Models = map[string]string{"PRIVATE_DEPLOYMENT": "public-model", "PRIVATE_OTHER_DEPLOYMENT": "other-model"}
	c.SummaryExport.ModelUsage.ResponseModels = map[string]string{"PRIVATE_SNAPSHOT": "public-model", "PRIVATE_OTHER_SNAPSHOT": "other-model"}
}

func TestServedModelIdentityMatrix(t *testing.T) {
	cases := []struct {
		name string
		edit func(pcommon.Map)
		want string
	}{
		{"matching", func(a pcommon.Map) {}, "public-model"},
		{"ambiguous-route-echo", func(a pcommon.Map) { a.PutStr("gen_ai.response.model", "PRIVATE_ROUTE") }, "__unmapped__"},
		{"mapped-route-collision", func(a pcommon.Map) {
			a.PutStr("litellm.model_group", "PRIVATE_OTHER_SNAPSHOT")
			a.PutStr("gen_ai.response.model", "PRIVATE_OTHER_SNAPSHOT")
		}, "__unmapped__"},
		{"missing-response", func(a pcommon.Map) { a.Remove("gen_ai.response.model") }, "public-model"},
		{"empty-response", func(a pcommon.Map) { a.PutStr("gen_ai.response.model", "") }, "__unmapped__"},
		{"empty-operation-with-fallback", func(a pcommon.Map) {
			a.PutStr("gen_ai.operation.name", "")
			a.PutStr("llm.request.type", "acompletion")
			a.PutStr("http.route", "/v1/chat/completions")
		}, "__unmapped__"},
		{"missing-primary", func(a pcommon.Map) { a.Remove("litellm.provider.model") }, "__missing__"},
		{"empty-primary", func(a pcommon.Map) { a.PutStr("litellm.provider.model", "") }, "__missing__"},
		{"number-primary", func(a pcommon.Map) { a.PutInt("litellm.provider.model", 123) }, "__unmapped__"},
		{"invalid-utf8", func(a pcommon.Map) { a.PutStr("litellm.provider.model", string([]byte{255})) }, "__unmapped__"},
		{"long-primary", func(a pcommon.Map) { a.PutStr("litellm.provider.model", strings.Repeat("x", 257)) }, "__too_long__"},
		{"unknown-primary", func(a pcommon.Map) { a.PutStr("litellm.provider.model", "public-model") }, "__unmapped__"},
		{"conflict", func(a pcommon.Map) { a.PutStr("gen_ai.response.model", "PRIVATE_OTHER_SNAPSHOT") }, "__unmapped__"},
		{"unknown-snapshot", func(a pcommon.Map) { a.PutStr("gen_ai.response.model", "PRIVATE_SNAPSHOT-next-date") }, "__unmapped__"},
		{"number-response", func(a pcommon.Map) { a.PutInt("gen_ai.response.model", 123) }, "__unmapped__"},
		{"long-response", func(a pcommon.Map) { a.PutStr("gen_ai.response.model", strings.Repeat("x", 257)) }, "__unmapped__"},
		{"no-trimming", func(a pcommon.Map) { a.PutStr("litellm.provider.model", " PRIVATE_DEPLOYMENT") }, "__unmapped__"},
		{"no-provider-stripping", func(a pcommon.Map) { a.PutStr("litellm.provider.model", "openai/PRIVATE_DEPLOYMENT") }, "__unmapped__"},
		{"wrong-operation", func(a pcommon.Map) { a.PutStr("gen_ai.operation.name", "text_completion") }, "__unmapped__"},
		{"native-litellm-operation", func(a pcommon.Map) {
			a.Remove("gen_ai.operation.name")
			a.PutStr("llm.request.type", "acompletion")
			a.PutStr("http.route", "/v1/chat/completions")
		}, "public-model"},
		{"not-chat-route", func(a pcommon.Map) {
			a.Remove("gen_ai.operation.name")
			a.PutStr("llm.request.type", "acompletion")
			a.PutStr("http.route", "/v1/completions")
		}, "__unmapped__"},
		{"streaming", func(a pcommon.Map) { a.PutBool("gen_ai.request.stream", true) }, "__unmapped__"},
		{"stream-string", func(a pcommon.Map) { a.PutStr("llm.is_streaming", "false") }, "__unmapped__"},
		{"explicit-nonstream", func(a pcommon.Map) { a.PutBool("gen_ai.request.stream", false) }, "public-model"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, e, clk := modelFixture(t, servedFixtureConfig)
			mustConsumeTraces(t, s, modelTrace(100, func(a pcommon.Map) {
				a.PutStr("litellm.provider.model", "PRIVATE_DEPLOYMENT")
				a.PutStr("litellm.model_group", "PRIVATE_ROUTE")
				a.PutStr("gen_ai.response.model", "PRIVATE_SNAPSHOT")
				a.PutStr("agent.id", "PRIVATE_AGENT")
				tc.edit(a)
			}))
			doc := exportDocs(t, s, e, clk.now.Add(time.Second))[0]
			row := doc.ModelUsage.Models[tc.want]
			if len(doc.ModelUsage.Models) != 1 || row.Requests != 1 || row.Complete.Requests != 1 || row.Complete.InputTokens != 100 || row.Excluded != (modelusage.Exclusions{}) {
				t.Fatalf("identity refusal changed token quality or totals: %+v", doc.ModelUsage)
			}
			encoded, err := doc.MarshalBinary()
			if err != nil || bytes.Contains(encoded, []byte("PRIVATE_")) {
				t.Fatal("private identity escaped summary", err)
			}
		})
	}
}

func TestServedResponseProfilesAndNoResourceFallback(t *testing.T) {
	for _, profile := range []string{modelIdentityAgentGateway, modelIdentityEnvoy} {
		for _, source := range []string{"span", "resource", "request", "unknown-snapshot"} {
			t.Run(profile+"/"+source, func(t *testing.T) {
				s, e, clk := modelFixture(t, func(c *Config) {
					c.SummaryExport.ModelUsage.Identity = profile
					c.SummaryExport.ModelUsage.Models = map[string]string{"PRIVATE_SNAPSHOT": "public-model", "PRIVATE_MODEL": "wrong-alias-model"}
				})
				traces := modelTrace(100, nil)
				attrs := traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Attributes()
				want := "__missing__"
				switch source {
				case "span":
					attrs.PutStr("gen_ai.response.model", "PRIVATE_SNAPSHOT")
					want = "public-model"
				case "resource":
					traces.ResourceSpans().At(0).Resource().Attributes().PutStr("gen_ai.response.model", "PRIVATE_SNAPSHOT")
				case "unknown-snapshot":
					attrs.PutStr("gen_ai.response.model", "PRIVATE_SNAPSHOT-next-date")
					want = "__unmapped__"
				}
				mustConsumeTraces(t, s, traces)
				doc := exportDocs(t, s, e, clk.now.Add(time.Second))[0]
				if doc.ModelUsage.Models[want].Requests != 1 {
					t.Fatal(doc.ModelUsage)
				}
			})
		}
	}
}

func TestServedModelCapacityDedupAndLegacyParity(t *testing.T) {
	s, e, clk := modelFixture(t, func(c *Config) { servedFixtureConfig(c); c.MaxSlices = 1; c.Dedup.Enabled = true })
	legacy, le, _ := modelFixture(t, func(c *Config) { c.MaxSlices = 1; c.Dedup.Enabled = true })
	le.epoch = e.epoch
	for i, name := range []string{"PRIVATE_DEPLOYMENT", "PRIVATE_OTHER_DEPLOYMENT", "PRIVATE_DEPLOYMENT"} {
		traces := modelTrace(100, func(a pcommon.Map) {
			a.PutStr("litellm.provider.model", name)
			a.PutStr("request.id", strings.Repeat("r", i+1))
		})
		got := mustConsumeTraces(t, s, traces)
		want := mustConsumeTraces(t, legacy, traces)
		if !bytes.Equal(sortedMetricAttributeJSON(t, marshalMetrics(t, got)), sortedMetricAttributeJSON(t, marshalMetrics(t, want))) {
			t.Fatal("served attribution changed metric output")
		}
		mustConsumeTraces(t, s, traces)
		mustConsumeTraces(t, legacy, traces)
	}
	doc := exportDocs(t, s, e, clk.now.Add(time.Second))[0]
	old := exportDocs(t, legacy, le, clk.now.Add(time.Second))[0]
	if doc.ModelUsage.Models["public-model"].Requests != 2 || doc.ModelUsage.Models["__overflow__"].Requests != 1 || len(doc.ModelUsage.Models) != 2 || doc.Counters["requests"] != 3 || doc.Counters["dedup_suppressed"] != 3 {
		t.Fatal("bounded served cohorts or global dedup failed", doc.ModelUsage, doc.Counters)
	}
	for _, w := range s.summary.windows {
		encoded, err := json.Marshal(w.modelUsage)
		if err != nil || len(encoded) != w.modelUsageBytes {
			t.Fatal("overflow byte accounting drift", err)
		}
	}
	combined, err := summary.Combine([]summary.Envelope{doc, doc}, []string{"producer"})
	if err != nil || combined.Counters["requests"] != 3 {
		t.Fatal("snapshot replay doubled totals", err)
	}
	if _, err := summary.Combine([]summary.Envelope{old, doc}, []string{"producer"}); err == nil {
		t.Fatal("mixed attribution contracts accepted")
	}
	doc.ModelUsage, old.ModelUsage = nil, nil
	a, err := doc.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	b, err := old.MarshalBinary()
	if err != nil || !bytes.Equal(a, b) || e.accountingID != le.accountingID {
		t.Fatal("new contract altered base summary bytes", err)
	}
}

// Existing metric labels are populated from Go maps. Normalize only attribute
// order; retain timestamps, descriptions, types, values and every other field.
func sortedMetricAttributeJSON(t *testing.T, raw []byte) []byte {
	t.Helper()
	var document any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		t.Fatal(err)
	}
	var visit func(any)
	visit = func(value any) {
		switch node := value.(type) {
		case map[string]any:
			for key, child := range node {
				if key == "attributes" {
					attrs := child.([]any)
					sort.Slice(attrs, func(i, j int) bool {
						return attrs[i].(map[string]any)["key"].(string) < attrs[j].(map[string]any)["key"].(string)
					})
				}
				visit(child)
			}
		case []any:
			for _, child := range node {
				visit(child)
			}
		}
	}
	visit(document)
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestServedModelContractAndValidation(t *testing.T) {
	c := defaultConfig()
	c.Slices = []SliceConfig{{Name: "model", Keys: []string{"gen_ai.request.model"}}}
	c.SummaryExport.ModelUsage = &ModelUsageConfig{Slice: "model", Models: map[string]string{"PRIVATE_MODEL": "public-model"}}
	legacy, err := modelUsageContract(c, "validation")
	if err != nil {
		t.Fatal(err)
	}
	servedFixtureConfig(c)
	first, err := modelUsageContract(c, "validation")
	if err != nil || first == legacy {
		t.Fatal("served contract reused request contract", err)
	}
	if legacy != "model-usage-v1:0c9efff1026dc221a7670940b79fdb2b4ea00f6f469d93ddf701d1c328d171b7" || first != "model-usage-v1:9caf9e0fd8bea10d820fb1fd8df57fe6b7dbb93ff739b6fecf995e14c53ba3d7" {
		t.Fatal("frozen legacy or served contract changed", legacy, first)
	}
	for _, mutate := range []func(*Config){
		func(c *Config) { c.MaxSlices++ },
		func(c *Config) { c.SummaryExport.ModelUsage.Models["PRIVATE_DEPLOYMENT"] = "other-model" },
		func(c *Config) { c.SummaryExport.ModelUsage.ResponseModels["PRIVATE_SNAPSHOT"] = "other-model" },
		func(c *Config) { c.Slices = append(c.Slices, SliceConfig{Name: "team", Keys: []string{"team.id"}}) },
		func(c *Config) { c.Slices[0].FromResourceAttributes = []string{"other.key"} },
	} {
		copy := *c
		m := *c.SummaryExport.ModelUsage
		m.Models, m.ResponseModels = maps.Clone(m.Models), maps.Clone(m.ResponseModels)
		copy.SummaryExport.ModelUsage = &m
		copy.Slices = append([]SliceConfig{}, c.Slices...)
		mutate(&copy)
		changed, err := modelUsageContract(&copy, "validation")
		if err != nil || changed == first {
			t.Fatal("contract failed to bind policy", err)
		}
	}
	for _, invalid := range []*ModelUsageConfig{
		{Identity: "PRIVATE_UNKNOWN_PROFILE"},
		{ResponseModels: map[string]string{}},
		{Identity: modelIdentityLiteLLM},
		{Identity: modelIdentityEnvoy, ResponseModels: map[string]string{}},
		{Identity: modelIdentityLiteLLM, ResponseModels: map[string]string{"PRIVATE_SNAPSHOT": "__missing__"}},
	} {
		if err := validateServedModelUsage(invalid); err == nil || strings.Contains(err.Error(), "PRIVATE_") {
			t.Fatal("invalid configuration accepted or reflected", err)
		}
	}
	c.SummaryExport.ModelUsage.Identity = modelIdentityAgentGateway
	c.SummaryExport.ModelUsage.ResponseModels = nil
	agent, err := modelUsageContract(c, "validation")
	if err != nil {
		t.Fatal(err)
	}
	c.SummaryExport.ModelUsage.Identity = modelIdentityEnvoy
	envoy, err := modelUsageContract(c, "validation")
	if err != nil || agent == envoy {
		t.Fatal("recipe profile not bound", err)
	}
	c.Slices[0].Keys = []string{"gen_ai.response.model"}
	if _, err := modelUsageContract(c, "validation"); err == nil {
		t.Fatal("fabricated request routing accepted")
	}
}

func TestServedModelConfigDoesNotAliasMaps(t *testing.T) {
	var cfg *Config
	s, _, _ := modelFixture(t, func(c *Config) { servedFixtureConfig(c); cfg = c })
	want := maps.Clone(s.cfg.modelUsage.responseModels)
	cfg.SummaryExport.ModelUsage.ResponseModels["PRIVATE_SNAPSHOT"] = "other-model"
	if !reflect.DeepEqual(want, s.cfg.modelUsage.responseModels) {
		t.Fatal("mutable verifier map")
	}
}

func TestServedIdentityDuplicateAttributes(t *testing.T) {
	decoder := ptrace.JSONUnmarshaler{}
	traces, err := decoder.UnmarshalTraces([]byte(`{"resourceSpans":[{"scopeSpans":[{"spans":[{"attributes":[{"key":"gen_ai.response.model","value":{"stringValue":"a"}},{"key":"gen_ai.response.model","value":{"stringValue":"b"}}]}]}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	attrs := traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Attributes()
	if _, quality := servedIdentityValue(attrs, "gen_ai.response.model"); quality != "__unmapped__" {
		t.Fatal("duplicate identity accepted")
	}
	for _, key := range []string{"gen_ai.response.model", "gen_ai.operation.name", "gen_ai.request.stream", "llm.is_streaming"} {
		var value any = "chat"
		if key == "gen_ai.response.model" {
			value = "PRIVATE_SNAPSHOT"
		}
		wireValue := map[string]any{"stringValue": value}
		if strings.Contains(key, "stream") {
			wireValue = map[string]any{"boolValue": false}
		}
		entry := map[string]any{"key": key, "value": wireValue}
		wire, err := json.Marshal(map[string]any{"resourceSpans": []any{map[string]any{"scopeSpans": []any{map[string]any{"spans": []any{map[string]any{"attributes": []any{entry, entry}}}}}}}})
		if err != nil {
			t.Fatal(err)
		}
		traces, err := decoder.UnmarshalTraces(wire)
		if err != nil {
			t.Fatal(err)
		}
		a := traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Attributes()
		if key != "gen_ai.operation.name" {
			a.PutStr("gen_ai.operation.name", "chat")
		}
		if key != "gen_ai.response.model" {
			a.PutStr("gen_ai.response.model", "PRIVATE_SNAPSHOT")
		}
		m := compiledModelUsage{identity: modelIdentityEnvoy, models: map[string]string{"PRIVATE_SNAPSHOT": "public-model"}}
		if m.servedModelID(a) != "__unmapped__" {
			t.Fatal("duplicate attribute accepted", key)
		}
	}
}

func TestServedIdentityExactMapOrderAndLength(t *testing.T) {
	c := defaultConfig()
	c.Slices = []SliceConfig{{Name: "model", Keys: []string{"gen_ai.request.model"}}}
	c.SummaryExport.ModelUsage = &ModelUsageConfig{Slice: "model", Identity: modelIdentityEnvoy, Models: map[string]string{}}
	long := strings.Repeat("x", 256)
	c.SummaryExport.ModelUsage.Models[long] = "public-model"
	c.SummaryExport.ModelUsage.Models["other"] = "other-model"
	a, err := modelUsageContract(c, "validation")
	if err != nil {
		t.Fatal(err)
	}
	c.SummaryExport.ModelUsage.Models = map[string]string{"other": "other-model", long: "public-model"}
	b, err := modelUsageContract(c, "validation")
	if err != nil || a != b {
		t.Fatal("map insertion order changed contract", err)
	}
	s, e, clk := modelFixture(t, func(cfg *Config) { cfg.SummaryExport.ModelUsage = c.SummaryExport.ModelUsage })
	for _, name := range []string{long, long + "x"} {
		mustConsumeTraces(t, s, modelTrace(100, func(attrs pcommon.Map) { attrs.PutStr("gen_ai.response.model", name) }))
	}
	doc := exportDocs(t, s, e, clk.now.Add(time.Second))[0]
	if doc.ModelUsage.Models["public-model"].Requests != 1 || doc.ModelUsage.Models["__too_long__"].Requests != 1 {
		t.Fatal(doc.ModelUsage)
	}
}
