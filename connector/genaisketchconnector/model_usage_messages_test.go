// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package genaisketchconnector

import (
	"encoding/json"
	"strings"
	"testing"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func TestMessagesStreamAndResponseMatrix(t *testing.T) {
	m := compiledModelUsage{identity: modelIdentityLiteLLMMessages,
		models:         map[string]string{"PRIVATE_DEPLOYMENT": "public-model"},
		responseModels: map[string]string{"PRIVATE_SNAPSHOT": "public-model"},
	}
	flags := []any{nil, false, true, "true", int64(1)}
	for _, stream := range flags {
		for _, legacy := range flags {
			for _, response := range []any{nil, "PRIVATE_SNAPSHOT", "PRIVATE_DEPLOYMENT", "PRIVATE_UNKNOWN", "", true} {
				for _, operation := range []any{nil, "chat", "", "invoke_agent", true} {
					a := pcommon.NewMap()
					a.PutStr("llm.request.type", "anthropic_messages")
					a.PutStr("http.route", "/v1/messages")
					a.PutStr("litellm.provider.model", "PRIVATE_DEPLOYMENT")
					for key, value := range map[string]any{"gen_ai.request.stream": stream,
						"llm.is_streaming": legacy, "gen_ai.response.model": response, "gen_ai.operation.name": operation} {
						if value != nil {
							if err := a.PutEmpty(key).FromRaw(value); err != nil {
								t.Fatal(err)
							}
						}
					}
					validFlag := func(v any) bool { return v == nil || v == false || v == true }
					valid := validFlag(stream) && validFlag(legacy) && (operation == nil || operation == "chat")
					valid = valid && !(stream != nil && legacy != nil && stream != legacy)
					valid = valid && !(legacy == true && stream != true)
					if stream != true {
						valid = valid && (response == nil || response == "PRIVATE_SNAPSHOT")
					}
					want := "__unmapped__"
					if valid {
						want = "public-model"
					}
					if got := m.servedModelID(a); got != want {
						t.Fatalf("stream=%v legacy=%v response=%v operation=%v: got %s want %s", stream, legacy, response, operation, got, want)
					}
				}
			}
		}
	}
}

func TestMessagesNativeMarkersAndSelectedIdentity(t *testing.T) {
	s, _, _ := modelFixture(t, nativeLiteLLMConfig(modelIdentityLiteLLMMessages))
	for _, stream := range []bool{false, true} {
		for _, tc := range []struct {
			name string
			edit func(pcommon.Map)
			want string
		}{
			{"valid", func(a pcommon.Map) {}, "public-model"},
			{"missing-call", func(a pcommon.Map) { a.Remove("llm.request.type") }, "__unmapped__"},
			{"wrong-call", func(a pcommon.Map) { a.PutStr("llm.request.type", "acompletion") }, "__unmapped__"},
			{"typed-call", func(a pcommon.Map) { a.PutBool("llm.request.type", true) }, "__unmapped__"},
			{"missing-route", func(a pcommon.Map) { a.Remove("http.route") }, "__unmapped__"},
			{"wrong-route", func(a pcommon.Map) { a.PutStr("http.route", "/v1/responses") }, "__unmapped__"},
			{"typed-route", func(a pcommon.Map) { a.PutBool("http.route", true) }, "__unmapped__"},
			{"missing-selected", func(a pcommon.Map) { a.Remove("litellm.provider.model") }, "__missing__"},
			{"empty-selected", func(a pcommon.Map) { a.PutStr("litellm.provider.model", "") }, "__missing__"},
			{"typed-selected", func(a pcommon.Map) { a.PutInt("litellm.provider.model", 1) }, "__unmapped__"},
			{"unknown-selected", func(a pcommon.Map) { a.PutStr("litellm.provider.model", "PRIVATE_UNKNOWN") }, "__unmapped__"},
			{"long-selected", func(a pcommon.Map) { a.PutStr("litellm.provider.model", strings.Repeat("x", 257)) }, "__too_long__"},
			{"invalid-utf8", func(a pcommon.Map) { a.PutStr("litellm.provider.model", string([]byte{255})) }, "__unmapped__"},
			{"no-trim", func(a pcommon.Map) { a.PutStr("litellm.provider.model", " PRIVATE_DEPLOYMENT") }, "__unmapped__"},
			{"no-prefix-strip", func(a pcommon.Map) { a.PutStr("litellm.provider.model", "anthropic/PRIVATE_DEPLOYMENT") }, "__unmapped__"},
			{"auto-route-not-identity", func(a pcommon.Map) {
				a.PutStr("litellm.model_group", "PRIVATE_AUTO_ROUTE")
				a.PutStr("gen_ai.request.model", "PRIVATE_ROUTE_ECHO")
			}, "public-model"},
			{"fallback", func(a pcommon.Map) {
				a.PutStr("litellm.provider.model", "PRIVATE_OTHER_DEPLOYMENT")
				a.PutStr("gen_ai.response.model", "PRIVATE_OTHER_SNAPSHOT")
			}, "other-model"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				traces := nativeLiteLLMTrace(modelIdentityLiteLLMMessages, func(a pcommon.Map) {
					a.PutBool("gen_ai.request.stream", stream)
					tc.edit(a)
				})
				a := traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Attributes()
				if got := s.cfg.modelUsage.servedModelID(a); got != tc.want {
					t.Fatalf("stream=%v: got %s want %s", stream, got, tc.want)
				}
				combined := *s.cfg.modelUsage
				combined.identity = modelIdentityLiteLLMCombined
				if combined.servedModelID(a) != "__unmapped__" {
					t.Fatal("existing combined contract admitted Messages")
				}
			})
		}
		traces := nativeLiteLLMTrace(modelIdentityLiteLLMMessages, func(a pcommon.Map) { a.Remove("litellm.provider.model") })
		traces.ResourceSpans().At(0).Resource().Attributes().PutStr("litellm.provider.model", "PRIVATE_DEPLOYMENT")
		if s.cfg.modelUsage.servedModelID(traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Attributes()) != "__missing__" {
			t.Fatal("resource identity used for attempt")
		}
	}
}

func TestMessagesContractAndValidation(t *testing.T) {
	c := defaultConfig()
	c.Slices = []SliceConfig{{Name: "model", Keys: []string{"gen_ai.request.model"}}}
	c.SummaryExport.ModelUsage = &ModelUsageConfig{Slice: "model"}
	nativeLiteLLMConfig(modelIdentityLiteLLMMessages)(c)
	contract, err := modelUsageContract(c, "validation")
	if err != nil {
		t.Fatal(err)
	}
	if contract != "model-usage-v1:a1e4a5bc584cceb74ca2072aebf2c6a69376b303c9f707ccb80bfcde47ef3571" {
		t.Fatal("Messages contract changed", contract)
	}
	for _, profile := range []string{modelIdentityLiteLLM, modelIdentityLiteLLMResponses, modelIdentityLiteLLMCombined} {
		c.SummaryExport.ModelUsage.Identity = profile
		other, err := modelUsageContract(c, "validation")
		if err != nil || other == contract {
			t.Fatal("Messages reused an existing profile contract", err)
		}
	}
	for _, change := range []func(*ModelUsageConfig){
		func(m *ModelUsageConfig) { m.Models["PRIVATE_DEPLOYMENT"] = "other-model" },
		func(m *ModelUsageConfig) { m.ResponseModels["PRIVATE_SNAPSHOT"] = "other-model" },
	} {
		nativeLiteLLMConfig(modelIdentityLiteLLMMessages)(c)
		change(c.SummaryExport.ModelUsage)
		other, err := modelUsageContract(c, "validation")
		if err != nil || other == contract {
			t.Fatal("Messages contract did not bind both maps", err)
		}
	}
	for _, response := range []map[string]string{nil, {"PRIVATE_RESPONSE": "__missing__"}, {"": "public-model"}, {strings.Repeat("x", 257): "public-model"}} {
		if err := validateServedModelUsage(&ModelUsageConfig{Identity: modelIdentityLiteLLMMessages, ResponseModels: response}); err == nil || strings.Contains(err.Error(), "PRIVATE_") {
			t.Fatal("invalid Messages map accepted or echoed", err)
		}
	}
	if err := validateServedModelUsage(&ModelUsageConfig{Identity: modelIdentityLiteLLMMessages, ResponseModels: map[string]string{}}); err != nil {
		t.Fatal("empty response map rejected", err)
	}
}

func TestMessagesDuplicateResponseByStreamMode(t *testing.T) {
	s, _, _ := modelFixture(t, nativeLiteLLMConfig(modelIdentityLiteLLMMessages))
	for _, stream := range []bool{false, true} {
		traces := nativeLiteLLMTrace(modelIdentityLiteLLMMessages, func(a pcommon.Map) { a.PutBool("gen_ai.request.stream", stream) })
		encoded, err := (&ptrace.JSONMarshaler{}).MarshalTraces(traces)
		if err != nil {
			t.Fatal(err)
		}
		var wire map[string]any
		if err := json.Unmarshal(encoded, &wire); err != nil {
			t.Fatal(err)
		}
		resource := wire["resourceSpans"].([]any)[0].(map[string]any)
		scope := resource["scopeSpans"].([]any)[0].(map[string]any)
		span := scope["spans"].([]any)[0].(map[string]any)
		span["attributes"] = append(span["attributes"].([]any), map[string]any{
			"key": "gen_ai.response.model", "value": map[string]any{"stringValue": "PRIVATE_SNAPSHOT"},
		})
		encoded, err = json.Marshal(wire)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := (&ptrace.JSONUnmarshaler{}).UnmarshalTraces(encoded)
		if err != nil {
			t.Fatal(err)
		}
		want := "__unmapped__"
		if stream {
			want = "public-model"
		}
		if got := s.cfg.modelUsage.servedModelID(decoded.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Attributes()); got != want {
			t.Fatalf("stream=%v duplicate response: got %s want %s", stream, got, want)
		}
	}
}
