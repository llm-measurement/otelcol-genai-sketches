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

func nativeLiteLLMConfig(profile string) func(*Config) {
	return func(c *Config) {
		servedFixtureConfig(c)
		c.SummaryExport.ModelUsage.Identity = profile
		if profile == modelIdentityLiteLLMStream {
			c.SummaryExport.ModelUsage.ResponseModels = nil
		}
	}
}

func nativeLiteLLMTrace(profile string, edit func(pcommon.Map)) ptrace.Traces {
	traces := modelTrace(100, func(a pcommon.Map) {
		a.Remove("gen_ai.operation.name")
		a.PutStr("llm.request.type", "acompletion")
		a.PutStr("http.route", "/v1/chat/completions")
		if profile == modelIdentityLiteLLMResponses {
			a.PutStr("llm.request.type", "aresponses")
			a.PutStr("http.route", "/v1/responses")
		}
		if profile == modelIdentityLiteLLMMessages {
			a.PutStr("llm.request.type", "anthropic_messages")
			a.PutStr("http.route", "/v1/messages")
		}
		a.PutBool("gen_ai.request.stream", true)
		a.PutStr("litellm.provider.model", "PRIVATE_DEPLOYMENT")
		a.PutStr("litellm.model_group", "PRIVATE_ROUTE")
		a.PutStr("gen_ai.response.model", "PRIVATE_SNAPSHOT")
		if edit != nil {
			edit(a)
		}
	})
	traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).SetName("chat PRIVATE_ROUTE")
	return traces
}

func TestServedNativeLiteLLMAdmission(t *testing.T) {
	for _, profile := range []string{modelIdentityLiteLLMStream, modelIdentityLiteLLMResponses} {
		t.Run(profile, func(t *testing.T) {
			s, _, _ := modelFixture(t, nativeLiteLLMConfig(profile))
			for _, tc := range []struct {
				name string
				edit func(pcommon.Map)
				want string
			}{
				{"native", func(pcommon.Map) {}, "public-model"},
				{"explicit-chat-operation", func(a pcommon.Map) { a.PutStr("gen_ai.operation.name", "chat") }, "public-model"},
				{"empty-operation", func(a pcommon.Map) { a.PutStr("gen_ai.operation.name", "") }, "__unmapped__"},
				{"non-string-operation", func(a pcommon.Map) { a.PutInt("gen_ai.operation.name", 1) }, "__unmapped__"},
				{"unverified-responses-operation", func(a pcommon.Map) { a.PutStr("gen_ai.operation.name", "responses") }, "__unmapped__"},
				{"different-operation", func(a pcommon.Map) { a.PutStr("gen_ai.operation.name", "text_completion") }, "__unmapped__"},
				{"missing-call", func(a pcommon.Map) { a.Remove("llm.request.type") }, "__unmapped__"},
				{"missing-route", func(a pcommon.Map) { a.Remove("http.route") }, "__unmapped__"},
				{"empty-call", func(a pcommon.Map) { a.PutStr("llm.request.type", "") }, "__unmapped__"},
				{"non-string-route", func(a pcommon.Map) { a.PutInt("http.route", 1) }, "__unmapped__"},
				{"unverified-sync-call", func(a pcommon.Map) { a.PutStr("llm.request.type", "responses") }, "__unmapped__"},
				{"wrong-route-despite-chat", func(a pcommon.Map) {
					a.PutStr("gen_ai.operation.name", "chat")
					a.PutStr("http.route", "/v1/completions")
				}, "__unmapped__"},
				{"other-api", func(a pcommon.Map) {
					call, route := "aresponses", "/v1/responses"
					if profile == modelIdentityLiteLLMResponses {
						call, route = "acompletion", "/v1/chat/completions"
					}
					a.PutStr("llm.request.type", call)
					a.PutStr("http.route", route)
				}, "__unmapped__"},
				{"missing-selected", func(a pcommon.Map) { a.Remove("litellm.provider.model") }, "__missing__"},
				{"empty-selected", func(a pcommon.Map) { a.PutStr("litellm.provider.model", "") }, "__missing__"},
				{"typed-selected", func(a pcommon.Map) { a.PutInt("litellm.provider.model", 1) }, "__unmapped__"},
				{"invalid-utf8-selected", func(a pcommon.Map) { a.PutStr("litellm.provider.model", "\xff") }, "__unmapped__"},
				{"long-selected", func(a pcommon.Map) { a.PutStr("litellm.provider.model", strings.Repeat("x", 257)) }, "__too_long__"},
				{"unknown-selected", func(a pcommon.Map) { a.PutStr("litellm.provider.model", "public-model") }, "__unmapped__"},
				{"no-prefix-stripping", func(a pcommon.Map) { a.PutStr("litellm.provider.model", "openai/PRIVATE_DEPLOYMENT") }, "__unmapped__"},
				{"no-trimming", func(a pcommon.Map) { a.PutStr("litellm.provider.model", " PRIVATE_DEPLOYMENT") }, "__unmapped__"},
				{"absent-response", func(a pcommon.Map) { a.Remove("gen_ai.response.model") }, "public-model"},
				{"recovery", func(a pcommon.Map) {
					a.PutStr("litellm.provider.model", "PRIVATE_OTHER_DEPLOYMENT")
					a.PutStr("gen_ai.response.model", "PRIVATE_OTHER_SNAPSHOT")
				}, "other-model"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					traces := nativeLiteLLMTrace(profile, tc.edit)
					a := traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Attributes()
					if got := s.cfg.modelUsage.servedModelID(a); got != tc.want {
						t.Fatalf("got %s, want %s", got, tc.want)
					}
				})
			}
			for _, response := range []any{"PRIVATE_ROUTE", "PRIVATE_OTHER_SNAPSHOT", "PRIVATE_SNAPSHOT-next-date", "", int64(1), "\xff", strings.Repeat("x", 257)} {
				traces := nativeLiteLLMTrace(profile, func(a pcommon.Map) {
					if err := a.PutEmpty("gen_ai.response.model").FromRaw(response); err != nil {
						t.Fatal(err)
					}
				})
				want := "__unmapped__"
				if profile == modelIdentityLiteLLMStream {
					want = "public-model"
				}
				a := traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Attributes()
				if got := s.cfg.modelUsage.servedModelID(a); got != want {
					t.Fatalf("response verification: got %s, want %s", got, want)
				}
			}
			traces := nativeLiteLLMTrace(profile, func(a pcommon.Map) { a.Remove("litellm.provider.model") })
			traces.ResourceSpans().At(0).Resource().Attributes().PutStr("litellm.provider.model", "PRIVATE_DEPLOYMENT")
			mustConsumeTraces(t, s, traces)
			if s.cfg.modelUsage.servedModelID(traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Attributes()) != "__missing__" {
				t.Fatal("resource model replaced missing span-local selected identity")
			}
		})
	}
}

func TestServedNativeLiteLLMStreamFlags(t *testing.T) {
	for _, profile := range []string{modelIdentityLiteLLMStream, modelIdentityLiteLLMResponses} {
		s, _, _ := modelFixture(t, nativeLiteLLMConfig(profile))
		for _, tc := range []struct {
			name            string
			stream, legacy  any
			chat, responses bool
		}{
			{"absent", nil, nil, false, true},
			{"true", true, nil, true, true},
			{"false", false, nil, false, true},
			{"legacy-true", nil, true, false, true},
			{"legacy-false", nil, false, false, true},
			{"matching-true", true, true, true, true},
			{"matching-false", false, false, false, true},
			{"conflicting-true", true, false, false, false},
			{"conflicting-false", false, true, false, false},
			{"string-true", "true", nil, false, false},
			{"string-false", "false", nil, false, false},
			{"numeric", int64(1), nil, false, false},
			{"string-legacy", true, "false", false, false},
			{"numeric-legacy", true, int64(0), false, false},
			{"nested", map[string]any{"value": true}, nil, false, false},
		} {
			t.Run(profile+"/"+tc.name, func(t *testing.T) {
				traces := nativeLiteLLMTrace(profile, func(a pcommon.Map) {
					a.Remove("gen_ai.request.stream")
					for key, value := range map[string]any{"gen_ai.request.stream": tc.stream, "llm.is_streaming": tc.legacy} {
						if value != nil {
							if err := a.PutEmpty(key).FromRaw(value); err != nil {
								t.Fatal(err)
							}
						}
					}
				})
				want := "__unmapped__"
				if profile == modelIdentityLiteLLMStream && tc.chat || profile == modelIdentityLiteLLMResponses && tc.responses {
					want = "public-model"
				}
				a := traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Attributes()
				if got := s.cfg.modelUsage.servedModelID(a); got != want {
					t.Fatalf("got %s, want %s", got, want)
				}
			})
		}
		for _, key := range []string{"gen_ai.request.stream", "llm.is_streaming"} {
			traces := nativeLiteLLMTrace(profile, func(a pcommon.Map) { a.PutEmpty(key) })
			a := traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Attributes()
			if got := s.cfg.modelUsage.servedModelID(a); got != "__unmapped__" {
				t.Fatal("empty stream value accepted")
			}
		}
	}
}

func TestServedNativeLiteLLMDuplicateMarkers(t *testing.T) {
	for _, profile := range []string{modelIdentityLiteLLMStream, modelIdentityLiteLLMResponses, modelIdentityLiteLLMMessages} {
		s, _, _ := modelFixture(t, nativeLiteLLMConfig(profile))
		for _, key := range []string{"gen_ai.operation.name", "llm.request.type", "http.route", "gen_ai.request.stream", "llm.is_streaming", "litellm.provider.model", "gen_ai.response.model"} {
			t.Run(profile+"/"+key, func(t *testing.T) {
				traces := nativeLiteLLMTrace(profile, func(a pcommon.Map) {
					a.PutStr("gen_ai.operation.name", "chat")
					a.PutBool("llm.is_streaming", true)
				})
				a := traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Attributes()
				var attrs []any
				a.Range(func(name string, value pcommon.Value) bool {
					wireValue := map[string]any{"stringValue": value.Str()}
					if value.Type() == pcommon.ValueTypeBool {
						wireValue = map[string]any{"boolValue": value.Bool()}
					}
					entry := map[string]any{"key": name, "value": wireValue}
					attrs = append(attrs, entry)
					if name == key {
						attrs = append(attrs, entry)
					}
					return true
				})
				wire, err := json.Marshal(map[string]any{"resourceSpans": []any{map[string]any{"scopeSpans": []any{map[string]any{"spans": []any{map[string]any{"attributes": attrs}}}}}}})
				if err != nil {
					t.Fatal(err)
				}
				decoder := ptrace.JSONUnmarshaler{}
				decoded, err := decoder.UnmarshalTraces(wire)
				if err != nil {
					t.Fatal(err)
				}
				want := "__unmapped__"
				if (profile == modelIdentityLiteLLMStream || profile == modelIdentityLiteLLMMessages) && key == "gen_ai.response.model" {
					want = "public-model"
				}
				got := s.cfg.modelUsage.servedModelID(decoded.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Attributes())
				if got != want {
					t.Fatalf("duplicate marker: got %s, want %s", got, want)
				}
			})
		}
	}
}

func TestServedNativeLiteLLMLegacyAccountingParity(t *testing.T) {
	for _, profile := range []string{modelIdentityLiteLLMStream, modelIdentityLiteLLMResponses, modelIdentityLiteLLMMessages} {
		s, e, clk := modelFixture(t, nativeLiteLLMConfig(profile))
		legacy, le, _ := modelFixture(t, nil)
		le.epoch = e.epoch
		for _, edit := range []func(pcommon.Map){
			nil,
			func(a pcommon.Map) { a.Remove("gen_ai.usage.cache_read.input_tokens") },
			func(a pcommon.Map) { a.Remove("litellm.provider.model") },
			func(a pcommon.Map) { a.PutStr("litellm.provider.model", "PRIVATE_UNKNOWN") },
			func(a pcommon.Map) {
				for _, key := range []string{"gen_ai.usage.input_tokens", "gen_ai.usage.output_tokens", "gen_ai.usage.cache_read.input_tokens", "gen_ai.usage.cache_write.input_tokens"} {
					a.Remove(key)
				}
			},
		} {
			traces := nativeLiteLLMTrace(profile, edit)
			got := mustConsumeTraces(t, s, traces)
			want := mustConsumeTraces(t, legacy, traces)
			if !bytes.Equal(sortedMetricAttributeJSON(t, marshalMetrics(t, got)), sortedMetricAttributeJSON(t, marshalMetrics(t, want))) {
				t.Fatal("new profile changed metrics")
			}
		}
		doc := exportDocs(t, s, e, clk.now.Add(time.Second))[0]
		old := exportDocs(t, legacy, le, clk.now.Add(time.Second))[0]
		if doc.ModelUsage.Models["public-model"].Requests != 3 || doc.ModelUsage.Models["public-model"].Complete.Requests != 1 || doc.ModelUsage.Models["__missing__"].Requests != 1 || doc.ModelUsage.Models["__unmapped__"].Requests != 1 {
			t.Fatal("identity or usage coverage changed", doc.ModelUsage)
		}
		encoded, err := doc.MarshalBinary()
		if err != nil || bytes.Contains(encoded, []byte("PRIVATE_")) {
			t.Fatal("private identity escaped summary", err)
		}
		if combined, err := summary.Combine([]summary.Envelope{doc, doc}, []string{"producer"}); err != nil || combined.Counters["requests"] != 5 {
			t.Fatal("replay changed totals", err)
		}
		if _, err := summary.Combine([]summary.Envelope{doc, old}, []string{"producer"}); err == nil {
			t.Fatal("mixed legacy and new contracts accepted")
		}
		doc.ModelUsage, old.ModelUsage = nil, nil
		if !bytes.Equal(mustJSON(t, doc), mustJSON(t, old)) || e.accountingID != le.accountingID {
			t.Fatal("new profile changed base summary or accounting contract")
		}
	}
}

func TestServedNativeLiteLLMContractsAndConfig(t *testing.T) {
	c := defaultConfig()
	c.Slices = []SliceConfig{{Name: "model", Keys: []string{"gen_ai.request.model"}}}
	c.SummaryExport.ModelUsage = &ModelUsageConfig{Slice: "model"}
	servedFixtureConfig(c)
	seen := map[string]bool{}
	wantContracts := map[string]string{
		modelIdentityLiteLLM:          "model-usage-v1:9caf9e0fd8bea10d820fb1fd8df57fe6b7dbb93ff739b6fecf995e14c53ba3d7",
		modelIdentityAgentGateway:     "model-usage-v1:6d31240a04e9f2bcad4d31c02284d8468a9ed7af3cb9e95bf44670574b40f7cb",
		modelIdentityEnvoy:            "model-usage-v1:47e989ca24b71d6f8bdb55b8b55c17486c4ef0a79c9acaf5db7fe1c4a9a90ac8",
		modelIdentityLiteLLMStream:    "model-usage-v1:e49e83fdd03403f81e921cfd56a63a8ad6216721f6e1ba01312ef965a8bdb475",
		modelIdentityLiteLLMResponses: "model-usage-v1:ae51e62a4bcf591456f70c85ca9cebbfb3e621a93f53053ada117c21a2153dbd",
	}
	for _, profile := range []string{modelIdentityLiteLLM, modelIdentityAgentGateway, modelIdentityEnvoy, modelIdentityLiteLLMStream, modelIdentityLiteLLMResponses} {
		servedFixtureConfig(c)
		c.SummaryExport.ModelUsage.Identity = profile
		if profile != modelIdentityLiteLLM && profile != modelIdentityLiteLLMResponses {
			c.SummaryExport.ModelUsage.ResponseModels = nil
		}
		contract, err := modelUsageContract(c, "validation")
		if err != nil || seen[contract] {
			t.Fatal("profile contract missing or reused", err)
		}
		seen[contract] = true
		t.Logf("%s: %s", profile, contract)
		if contract != wantContracts[profile] {
			t.Fatal("frozen profile contract changed", profile, contract)
		}
		c.SummaryExport.ModelUsage.Models["PRIVATE_DEPLOYMENT"] = "other-model"
		changed, err := modelUsageContract(c, "validation")
		if err != nil || changed == contract {
			t.Fatal("profile contract failed to bind selected-model map", err)
		}
		if profile == modelIdentityLiteLLMResponses {
			c.SummaryExport.ModelUsage.Models["PRIVATE_DEPLOYMENT"] = "public-model"
			c.SummaryExport.ModelUsage.ResponseModels["PRIVATE_SNAPSHOT"] = "other-model"
			changed, err = modelUsageContract(c, "validation")
			if err != nil || changed == contract {
				t.Fatal("Responses contract failed to bind response-model map", err)
			}
		}
	}
	for _, invalid := range []*ModelUsageConfig{
		{Identity: modelIdentityLiteLLMStream, ResponseModels: map[string]string{}},
		{Identity: modelIdentityLiteLLMStream, ResponseModels: map[string]string{"PRIVATE_RESPONSE": "public-model"}},
		{Identity: modelIdentityLiteLLMResponses},
		{Identity: modelIdentityLiteLLMResponses, ResponseModels: map[string]string{"PRIVATE_RESPONSE": "__missing__"}},
		{Identity: modelIdentityLiteLLMResponses, ResponseModels: map[string]string{"": "public-model"}},
		{Identity: modelIdentityLiteLLMResponses, ResponseModels: map[string]string{strings.Repeat("x", 257): "public-model"}},
	} {
		if err := validateServedModelUsage(invalid); err == nil || strings.Contains(err.Error(), "PRIVATE_") {
			t.Fatal("invalid new-profile config accepted or reflected", err)
		}
	}
	var cfg *Config
	s, _, _ := modelFixture(t, func(c *Config) { nativeLiteLLMConfig(modelIdentityLiteLLMResponses)(c); cfg = c })
	want := maps.Clone(s.cfg.modelUsage.responseModels)
	cfg.SummaryExport.ModelUsage.ResponseModels["PRIVATE_SNAPSHOT"] = "other-model"
	if !reflect.DeepEqual(want, s.cfg.modelUsage.responseModels) {
		t.Fatal("Responses verifier map was not immutable")
	}
}

func TestServedLegacyProfileAdmissionUnchanged(t *testing.T) {
	for _, profile := range []string{modelIdentityLiteLLM, modelIdentityAgentGateway, modelIdentityEnvoy} {
		m := compiledModelUsage{identity: profile,
			models:         map[string]string{"PRIVATE_DEPLOYMENT": "public-model", "PRIVATE_SNAPSHOT": "public-model"},
			responseModels: map[string]string{"PRIVATE_SNAPSHOT": "public-model"},
		}
		values := []any{nil, false, true, "false", "true", int64(0), map[string]any{"flag": false}}
		for _, stream := range values {
			for _, legacy := range values {
				a := pcommon.NewMap()
				a.PutStr("gen_ai.operation.name", "chat")
				a.PutStr("litellm.provider.model", "PRIVATE_DEPLOYMENT")
				a.PutStr("gen_ai.response.model", "PRIVATE_SNAPSHOT")
				// Old profiles do not gate a valid explicit operation on these markers.
				a.PutStr("llm.request.type", "aresponses")
				a.PutStr("http.route", "/v1/responses")
				want := "public-model"
				for key, value := range map[string]any{"gen_ai.request.stream": stream, "llm.is_streaming": legacy} {
					if value == nil {
						continue
					}
					if err := a.PutEmpty(key).FromRaw(value); err != nil {
						t.Fatal(err)
					}
					if flag, ok := value.(bool); !ok || flag {
						want = "__unmapped__"
					}
				}
				if got := m.servedModelID(a); got != want {
					t.Fatalf("%s legacy stream admission changed: got %s, want %s", profile, got, want)
				}
			}
		}
	}
}
