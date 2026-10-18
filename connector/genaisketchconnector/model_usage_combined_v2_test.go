// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package genaisketchconnector

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func TestCombinedV2LiteLLMReusesFourRules(t *testing.T) {
	m := compiledModelUsage{identity: modelIdentityLiteLLMCombinedV2,
		models:         map[string]string{"PRIVATE_SELECTED": "public-model"},
		responseModels: map[string]string{"PRIVATE_SNAPSHOT": "public-model"}}
	values := []any{nil, false, true, "true", int64(1)}
	for _, api := range []struct{ call, route, profile string }{
		{"acompletion", "/v1/chat/completions", modelIdentityLiteLLM},
		{"aresponses", "/v1/responses", modelIdentityLiteLLMResponses},
		{"anthropic_messages", "/v1/messages", modelIdentityLiteLLMMessages},
	} {
		for _, stream := range values {
			for _, legacy := range values {
				for _, response := range []any{nil, "PRIVATE_SNAPSHOT", "PRIVATE_ROUTE", "", true} {
					for _, operation := range []any{nil, "chat", "", "invoke_agent", true} {
						a := pcommon.NewMap()
						a.PutStr("llm.request.type", api.call)
						a.PutStr("http.route", api.route)
						a.PutStr("litellm.provider.model", "PRIVATE_SELECTED")
						for key, value := range map[string]any{"gen_ai.request.stream": stream,
							"llm.is_streaming": legacy, "gen_ai.response.model": response, "gen_ai.operation.name": operation} {
							if value != nil {
								if err := a.PutEmpty(key).FromRaw(value); err != nil {
									t.Fatal(err)
								}
							}
						}
						path := m
						path.identity = api.profile
						if api.profile == modelIdentityLiteLLM && stream == true {
							path.identity = modelIdentityLiteLLMStream
						}
						if got, want := m.servedModelID(a), path.servedModelID(a); got != want {
							t.Fatalf("v2 differs from %s: stream=%v legacy=%v response=%v operation=%v got=%s want=%s", path.identity, stream, legacy, response, operation, got, want)
						}
						v1 := m
						v1.identity = modelIdentityLiteLLMCombined
						wantV1 := m.servedModelID(a)
						if api.profile == modelIdentityLiteLLMMessages {
							wantV1 = "__unmapped__"
						}
						if got := v1.servedModelID(a); got != wantV1 {
							t.Fatalf("v1 changed: got=%s want=%s", got, wantV1)
						}
					}
				}
			}
		}
	}
}

func TestCombinedV2LiteLLMNativeMarkers(t *testing.T) {
	m := compiledModelUsage{identity: modelIdentityLiteLLMCombinedV2,
		models: map[string]string{"PRIVATE_SELECTED": "public-model"}, responseModels: map[string]string{}}
	for _, call := range []any{nil, "", "acompletion", "aresponses", "anthropic_messages", "PRIVATE_CALL", true} {
		for _, route := range []any{nil, "", "/v1/chat/completions", "/v1/responses", "/v1/messages", "/v1/messages/", true} {
			a := pcommon.NewMap()
			a.PutStr("gen_ai.operation.name", "chat")
			a.PutStr("litellm.provider.model", "PRIVATE_SELECTED")
			for key, value := range map[string]any{"llm.request.type": call, "http.route": route} {
				if value != nil {
					if err := a.PutEmpty(key).FromRaw(value); err != nil {
						t.Fatal(err)
					}
				}
			}
			want := "__unmapped__"
			if call == "acompletion" && route == "/v1/chat/completions" || call == "aresponses" && route == "/v1/responses" || call == "anthropic_messages" && route == "/v1/messages" {
				want = "public-model"
			}
			if got := m.servedModelID(a); got != want {
				t.Fatalf("call=%v route=%v got=%s want=%s", call, route, got, want)
			}
		}
	}
}

func TestCombinedV2LiteLLMDuplicateAttributes(t *testing.T) {
	m := compiledModelUsage{identity: modelIdentityLiteLLMCombinedV2,
		models: map[string]string{"PRIVATE_DEPLOYMENT": "public-model"}, responseModels: map[string]string{"PRIVATE_SNAPSHOT": "public-model"}}
	for _, profile := range []string{modelIdentityLiteLLM, modelIdentityLiteLLMStream, modelIdentityLiteLLMResponses, modelIdentityLiteLLMMessages} {
		for _, stream := range []bool{false, true} {
			if profile == modelIdentityLiteLLM && stream || profile == modelIdentityLiteLLMStream && !stream {
				continue
			}
			for _, key := range []string{"llm.request.type", "http.route", "gen_ai.operation.name", "gen_ai.request.stream", "llm.is_streaming", "litellm.provider.model", "gen_ai.response.model"} {
				traces := nativeLiteLLMTrace(profile, func(a pcommon.Map) {
					a.PutStr("gen_ai.operation.name", "chat")
					a.PutBool("gen_ai.request.stream", stream)
					a.PutBool("llm.is_streaming", stream)
				})
				var attrs []any
				traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Attributes().Range(func(name string, value pcommon.Value) bool {
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
				decoded, err := (&ptrace.JSONUnmarshaler{}).UnmarshalTraces(wire)
				if err != nil {
					t.Fatal(err)
				}
				a := decoded.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Attributes()
				want := "__unmapped__"
				if key == "gen_ai.response.model" && stream && (profile == modelIdentityLiteLLMStream || profile == modelIdentityLiteLLMMessages) {
					want = "public-model"
				}
				path := m
				path.identity = profile
				if got := m.servedModelID(a); got != want {
					t.Fatalf("profile=%s stream=%v duplicate=%s got=%s want=%s", profile, stream, key, got, want)
				}
				// The union always requires unique native markers; standalone
				// nonstreaming Chat can instead use an explicit operation name.
				if key != "llm.request.type" && key != "http.route" && m.servedModelID(a) != path.servedModelID(a) {
					t.Fatal("v2 changed a standalone duplicate-attribute rule", profile, key)
				}
			}
		}
	}
}

func TestCombinedV2LiteLLMContractAndConfig(t *testing.T) {
	c := defaultConfig()
	c.Slices = []SliceConfig{{Name: "model", Keys: []string{"gen_ai.request.model"}}}
	c.SummaryExport.ModelUsage = &ModelUsageConfig{Slice: "model"}
	nativeLiteLLMConfig(modelIdentityLiteLLMCombinedV2)(c)
	contract, err := modelUsageContract(c, "validation")
	if err != nil {
		t.Fatal(err)
	}
	if contract != "model-usage-v1:beeb9c770c43cab86a350b70eb11178ddbda13bb47dc5f550cb96c1d8e37a747" {
		t.Fatal("v2 contract changed", contract)
	}
	for _, profile := range []string{modelIdentityLiteLLM, modelIdentityLiteLLMStream, modelIdentityLiteLLMResponses, modelIdentityLiteLLMMessages, modelIdentityLiteLLMCombined} {
		nativeLiteLLMConfig(profile)(c)
		other, err := modelUsageContract(c, "validation")
		if err != nil || other == contract {
			t.Fatal("v2 reused existing contract", profile, err)
		}
	}
	for _, change := range []func(*ModelUsageConfig){
		func(m *ModelUsageConfig) { m.Models["PRIVATE_DEPLOYMENT"] = "other-model" },
		func(m *ModelUsageConfig) { m.ResponseModels["PRIVATE_SNAPSHOT"] = "other-model" },
	} {
		nativeLiteLLMConfig(modelIdentityLiteLLMCombinedV2)(c)
		change(c.SummaryExport.ModelUsage)
		other, err := modelUsageContract(c, "validation")
		if err != nil || other == contract {
			t.Fatal("v2 contract did not bind both maps", err)
		}
	}
	tooMany := map[string]string{}
	for i := range 5001 {
		tooMany[fmt.Sprint(i)] = "public-model"
	}
	for _, response := range []map[string]string{nil, {"PRIVATE_RESPONSE": "__missing__"}, {"": "public-model"}, {strings.Repeat("x", 257): "public-model"}, tooMany} {
		if err := validateServedModelUsage(&ModelUsageConfig{Identity: modelIdentityLiteLLMCombinedV2, ResponseModels: response}); err == nil || strings.Contains(err.Error(), "PRIVATE_") {
			t.Fatal("invalid v2 map accepted or echoed", err)
		}
	}
	if err := validateServedModelUsage(&ModelUsageConfig{Identity: modelIdentityLiteLLMCombinedV2, ResponseModels: map[string]string{}}); err != nil {
		t.Fatal("empty response map rejected", err)
	}
}
