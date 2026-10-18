// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package genaisketchconnector

import (
	"strings"
	"testing"

	"go.opentelemetry.io/collector/pdata/pcommon"
)

func TestCombinedLiteLLMReusesPathRules(t *testing.T) {
	m := compiledModelUsage{identity: modelIdentityLiteLLMCombined,
		models:         map[string]string{"PRIVATE_SELECTED": "public-model"},
		responseModels: map[string]string{"PRIVATE_SNAPSHOT": "public-model"},
	}
	values := []any{nil, false, true, "true", int64(1)}
	for _, api := range []struct{ call, route string }{
		{"acompletion", "/v1/chat/completions"}, {"aresponses", "/v1/responses"},
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
						path.identity = modelIdentityLiteLLM
						if api.call == "aresponses" {
							path.identity = modelIdentityLiteLLMResponses
						} else if stream == true {
							path.identity = modelIdentityLiteLLMStream
						}
						if got, want := m.servedModelID(a), path.servedModelID(a); got != want {
							t.Fatalf("combined rule differs from %s: got %s want %s", path.identity, got, want)
						}
					}
				}
			}
		}
	}
}

func TestCombinedLiteLLMRequiresNativeMarkers(t *testing.T) {
	m := compiledModelUsage{identity: modelIdentityLiteLLMCombined,
		models: map[string]string{"PRIVATE_SELECTED": "public-model"}, responseModels: map[string]string{}}
	for _, call := range []any{nil, "acompletion", "aresponses", "anthropic_messages", true} {
		for _, route := range []any{nil, "/v1/chat/completions", "/v1/responses", "/v1/messages", true} {
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
			if call == "acompletion" && route == "/v1/chat/completions" || call == "aresponses" && route == "/v1/responses" {
				want = "public-model"
			}
			if got := m.servedModelID(a); got != want {
				t.Fatalf("got %s want %s", got, want)
			}
		}
	}
}

func TestCombinedLiteLLMContractAndConfig(t *testing.T) {
	c := defaultConfig()
	c.Slices = []SliceConfig{{Name: "model", Keys: []string{"gen_ai.request.model"}}}
	c.SummaryExport.ModelUsage = &ModelUsageConfig{Slice: "model"}
	servedFixtureConfig(c)
	c.SummaryExport.ModelUsage.Identity = modelIdentityLiteLLMCombined
	contract, err := modelUsageContract(c, "validation")
	if err != nil {
		t.Fatal(err)
	}
	if contract != "model-usage-v1:26946c959781c51467458fba1ef12b303b4beb39899911abaade120dbbd2512a" {
		t.Fatal("combined contract changed", contract)
	}
	for _, profile := range []string{modelIdentityLiteLLM, modelIdentityLiteLLMStream, modelIdentityLiteLLMResponses} {
		servedFixtureConfig(c)
		c.SummaryExport.ModelUsage.Identity = profile
		if profile == modelIdentityLiteLLMStream {
			c.SummaryExport.ModelUsage.ResponseModels = nil
		}
		other, err := modelUsageContract(c, "validation")
		if err != nil || other == contract {
			t.Fatal("combined contract reused path contract", err)
		}
	}
	servedFixtureConfig(c)
	c.SummaryExport.ModelUsage.Identity = modelIdentityLiteLLMCombined
	c.SummaryExport.ModelUsage.ResponseModels["PRIVATE_SNAPSHOT"] = "other-model"
	other, err := modelUsageContract(c, "validation")
	if err != nil || other == contract {
		t.Fatal("response map not bound", err)
	}
	for _, invalid := range []*ModelUsageConfig{
		{Identity: modelIdentityLiteLLMCombined},
		{Identity: modelIdentityLiteLLMCombined, ResponseModels: map[string]string{"PRIVATE_SNAPSHOT": "__missing__"}},
		{Identity: "litellm-1.104.1-v1", ResponseModels: map[string]string{}},
	} {
		if err := validateServedModelUsage(invalid); err == nil || strings.Contains(err.Error(), "PRIVATE_") {
			t.Fatal("invalid combined config accepted or echoed", err)
		}
	}
	if err := validateServedModelUsage(&ModelUsageConfig{Identity: modelIdentityLiteLLMCombined, ResponseModels: map[string]string{}}); err != nil {
		t.Fatal("empty verifier map is valid", err)
	}
}
