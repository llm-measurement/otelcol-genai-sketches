// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex
package charttest

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func modelUsageSettings(extra ...string) []string {
	settings := append([]string{}, enabledSummary...)
	settings = append(settings, "summaryExport.modelUsage.enabled=true", "summaryExport.modelUsage.slice=by_model")
	return append(settings, extra...)
}

func TestModelUsageDisabledRenderingUnchanged(t *testing.T) {
	for _, base := range [][]string{nil, enabledSummary} {
		original, err := render(t, base...)
		if err != nil {
			t.Fatal(err)
		}
		for _, profile := range [][]string{
			nil,
			{"summaryExport.modelUsage.identity="},
			{"summaryExport.modelUsage.identity=agentgateway-1.6.0-chat-v1"},
			{"summaryExport.modelUsage.identity=envoy-ai-gateway-1.1.0-chat-v1"},
			{"summaryExport.modelUsage.identity=litellm-1.104.0-chat-v1", "summaryExport.modelUsage.responseModels={}"},
			{"summaryExport.modelUsage.identity=litellm-1.104.0-chat-v1", `summaryExport.modelUsage.responseModels={"PRIVATE_RESPONSE":"public-model"}`},
			{"summaryExport.modelUsage.identity=litellm-1.104.0-chat-stream-v1"},
			{"summaryExport.modelUsage.identity=litellm-1.104.0-responses-v1", "summaryExport.modelUsage.responseModels={}"},
			{"summaryExport.modelUsage.identity=litellm-1.104.0-responses-v1", `summaryExport.modelUsage.responseModels={"PRIVATE_RESPONSE":"public-model"}`},
			{"summaryExport.modelUsage.identity=litellm-1.104.0-v1"},
			{"summaryExport.modelUsage.identity=litellm-1.104.0-v1", "summaryExport.modelUsage.responseModels={}"},
			{"summaryExport.modelUsage.identity=litellm-1.104.0-v1", `summaryExport.modelUsage.responseModels={"PRIVATE_RESPONSE":"public-model"}`},
			{"summaryExport.modelUsage.identity=litellm-1.104.0-v2"},
			{"summaryExport.modelUsage.identity=litellm-1.104.0-v2", "summaryExport.modelUsage.responseModels={}"},
			{"summaryExport.modelUsage.identity=litellm-1.104.0-v2", `summaryExport.modelUsage.responseModels={"PRIVATE_RESPONSE":"public-model"}`},
			{"summaryExport.modelUsage.identity=litellm-1.104.0-messages-v1"},
			{"summaryExport.modelUsage.identity=litellm-1.104.0-messages-v1", "summaryExport.modelUsage.responseModels={}"},
			{"summaryExport.modelUsage.identity=litellm-1.104.0-messages-v1", `summaryExport.modelUsage.responseModels={"PRIVATE_RESPONSE":"public-model"}`},
		} {
			settings := append(append([]string{}, base...),
				"summaryExport.modelUsage.enabled=false",
				"summaryExport.modelUsage.slice=not_selected",
				`summaryExport.modelUsage.models={"PRIVATE_ALIAS":"public-model"}`)
			disabled, err := render(t, append(settings, profile...)...)
			if err != nil || !bytes.Equal(original, disabled) {
				t.Fatalf("disabled settings changed rendered manifests for %v: %v", profile, err)
			}
			for _, sentinel := range []string{"model_usage", "PRIVATE_ALIAS", "PRIVATE_RESPONSE", "response_models", "chat-v1", "chat-stream-v1", "responses-v1", "litellm-1.104.0-v1", "litellm-1.104.0-v2", "messages-v1"} {
				if bytes.Contains(disabled, []byte(sentinel)) {
					t.Fatalf("disabled configuration escaped into manifests: %s", sentinel)
				}
			}
		}
	}
}

func TestModelUsageEmptyIdentityPreservesLegacyRendering(t *testing.T) {
	original, err := render(t, modelUsageSettings()...)
	if err != nil {
		t.Fatal(err)
	}
	empty, err := render(t, modelUsageSettings("summaryExport.modelUsage.identity=")...)
	if err != nil || !bytes.Equal(original, empty) {
		t.Fatal("empty identity changed legacy rendering", err)
	}
}

func TestModelUsageIdentityRendering(t *testing.T) {
	for _, identity := range []string{"agentgateway-1.6.0-chat-v1", "envoy-ai-gateway-1.1.0-chat-v1", "litellm-1.104.0-chat-v1", "litellm-1.104.0-chat-stream-v1", "litellm-1.104.0-responses-v1", "litellm-1.104.0-v1", "litellm-1.104.0-v2", "litellm-1.104.0-messages-v1"} {
		t.Run(identity, func(t *testing.T) {
			for _, mapping := range []map[string]string{
				{},
				{"123": "123", "true": "true", "provider/model-2026-10-16": "public-model"},
			} {
				encoded, err := json.Marshal(mapping)
				if err != nil {
					t.Fatal(err)
				}
				settings := modelUsageSettings("summaryExport.modelUsage.identity="+identity,
					"summaryExport.modelUsage.models="+string(encoded))
				want := map[string]any{"slice": "by_model", "identity": identity, "models": map[string]any{}}
				for name, public := range mapping {
					want["models"].(map[string]any)[name] = public
				}
				if identity == "litellm-1.104.0-chat-v1" || identity == "litellm-1.104.0-responses-v1" || identity == "litellm-1.104.0-v1" || identity == "litellm-1.104.0-v2" || identity == "litellm-1.104.0-messages-v1" {
					responses := map[string]string{}
					for name, public := range mapping {
						responses["response-"+name] = public
					}
					responseJSON, err := json.Marshal(responses)
					if err != nil {
						t.Fatal(err)
					}
					settings = append(settings, "summaryExport.modelUsage.responseModels="+string(responseJSON))
					want["response_models"] = map[string]any{}
					for name, public := range responses {
						want["response_models"].(map[string]any)[name] = public
					}
				}
				cfg := summaryConfig(t, summaryDocuments(t, settings...))
				usage := nested(cfg, "connectors", "genaisketch", "summary_export", "model_usage")
				if !reflect.DeepEqual(usage, want) {
					t.Fatalf("identity or map namespace changed: got %#v, want %#v", usage, want)
				}
			}
		})
	}
}

func TestModelUsageIdentityRejectsInvalidValues(t *testing.T) {
	for _, tc := range []struct {
		name     string
		settings []string
	}{
		{"unknown-identity", []string{"summaryExport.modelUsage.identity=unknown"}},
		{"wrong-version", []string{"summaryExport.modelUsage.identity=agentgateway-1.7.0-chat-v1"}},
		{"non-string-identity", []string{"summaryExport.modelUsage.identity=123"}},
		{"null-identity", []string{"summaryExport.modelUsage.identity=null"}},
		{"legacy-response-map", []string{"summaryExport.modelUsage.responseModels={}"}},
		{"empty-identity-response-map", []string{"summaryExport.modelUsage.identity=", "summaryExport.modelUsage.responseModels={}"}},
		{"agentgateway-response-map", []string{"summaryExport.modelUsage.identity=agentgateway-1.6.0-chat-v1", "summaryExport.modelUsage.responseModels={}"}},
		{"envoy-response-map", []string{"summaryExport.modelUsage.identity=envoy-ai-gateway-1.1.0-chat-v1", "summaryExport.modelUsage.responseModels={}"}},
		{"litellm-missing-response-map", []string{"summaryExport.modelUsage.identity=litellm-1.104.0-chat-v1"}},
		{"litellm-null-response-map", []string{"summaryExport.modelUsage.identity=litellm-1.104.0-chat-v1", "summaryExport.modelUsage.responseModels=null"}},
		{"litellm-stream-empty-response-map", []string{"summaryExport.modelUsage.identity=litellm-1.104.0-chat-stream-v1", "summaryExport.modelUsage.responseModels={}"}},
		{"litellm-stream-response-map", []string{"summaryExport.modelUsage.identity=litellm-1.104.0-chat-stream-v1", `summaryExport.modelUsage.responseModels={"PRIVATE_RESPONSE":"public-model"}`}},
		{"litellm-responses-missing-response-map", []string{"summaryExport.modelUsage.identity=litellm-1.104.0-responses-v1"}},
		{"litellm-responses-null-response-map", []string{"summaryExport.modelUsage.identity=litellm-1.104.0-responses-v1", "summaryExport.modelUsage.responseModels=null"}},
		{"litellm-combined-missing-response-map", []string{"summaryExport.modelUsage.identity=litellm-1.104.0-v1"}},
		{"litellm-combined-null-response-map", []string{"summaryExport.modelUsage.identity=litellm-1.104.0-v1", "summaryExport.modelUsage.responseModels=null"}},
		{"litellm-combined-wrong-version", []string{"summaryExport.modelUsage.identity=litellm-1.104.1-v1", "summaryExport.modelUsage.responseModels={}"}},
		{"litellm-v2-missing-response-map", []string{"summaryExport.modelUsage.identity=litellm-1.104.0-v2"}},
		{"litellm-v2-null-response-map", []string{"summaryExport.modelUsage.identity=litellm-1.104.0-v2", "summaryExport.modelUsage.responseModels=null"}},
		{"litellm-v2-wrong-version", []string{"summaryExport.modelUsage.identity=litellm-1.104.1-v2", "summaryExport.modelUsage.responseModels={}"}},
		{"litellm-messages-missing-response-map", []string{"summaryExport.modelUsage.identity=litellm-1.104.0-messages-v1"}},
		{"litellm-messages-null-response-map", []string{"summaryExport.modelUsage.identity=litellm-1.104.0-messages-v1", "summaryExport.modelUsage.responseModels=null"}},
		{"litellm-messages-wrong-version", []string{"summaryExport.modelUsage.identity=litellm-1.104.1-messages-v1", "summaryExport.modelUsage.responseModels={}"}},
		{"litellm-stream-wrong-version", []string{"summaryExport.modelUsage.identity=litellm-1.104.1-chat-stream-v1"}},
		{"litellm-responses-wrong-version", []string{"summaryExport.modelUsage.identity=litellm-1.104.1-responses-v1"}},
		{"response-slice", []string{"summaryExport.modelUsage.identity=agentgateway-1.6.0-chat-v1", `connector.slices=[{"name":"by_model","keys":["gen_ai.response.model"]}]`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if output, err := render(t, modelUsageSettings(tc.settings...)...); err == nil {
				t.Fatalf("invalid identity configuration accepted: %v\n%s", tc.settings, output)
			}
		})
	}
}

func TestModelUsageResponseMapRejectsInvalidValues(t *testing.T) {
	tooMany := map[string]string{}
	for i := range 5001 {
		key, _ := json.Marshal(i)
		tooMany[string(key)] = "public-model"
	}
	oversize, _ := json.Marshal(tooMany)
	for _, tc := range []struct{ name, mapping string }{
		{"list", "[]"},
		{"non-object", "123"},
		{"empty-name", `{"":"public-model"}`},
		{"long-name", `{"` + strings.Repeat("a", 257) + `":"public-model"}`},
		{"long-utf8-name", `{"` + strings.Repeat("\u00e9", 129) + `":"public-model"}`},
		{"too-many-names", string(oversize)},
		{"non-string-id", `{"response":123}`},
		{"empty-id", `{"response":""}`},
		{"reserved-id", `{"response":"__unmapped__"}`},
		{"nonpublic-id", `{"response":"https://host/?secret=value"}`},
		{"long-id", `{"response":"` + strings.Repeat("a", 129) + `"}`},
	} {
		for _, identity := range []string{"litellm-1.104.0-chat-v1", "litellm-1.104.0-responses-v1", "litellm-1.104.0-v1", "litellm-1.104.0-v2", "litellm-1.104.0-messages-v1"} {
			t.Run(identity+"/"+tc.name, func(t *testing.T) {
				if output, err := render(t, modelUsageSettings("summaryExport.modelUsage.identity="+identity,
					"summaryExport.modelUsage.responseModels="+tc.mapping)...); err == nil {
					t.Fatalf("invalid response map accepted\n%s", output)
				}
			})
		}
	}
}

func TestModelUsageRendering(t *testing.T) {
	for _, mapping := range []map[string]string{
		{},
		{"alias-a": "claude-sonnet-5-5", "alias-b": "gpt-6.1-sol", "alias-c": "gpt-6.1-sol"},
		{"123": "123", "true": "true", "alias: with space": "vendor/model:1"},
	} {
		encoded, err := json.Marshal(mapping)
		if err != nil {
			t.Fatal(err)
		}
		cfg := summaryConfig(t, summaryDocuments(t, modelUsageSettings("summaryExport.modelUsage.models="+string(encoded))...))
		usage := nested(cfg, "connectors", "genaisketch", "summary_export", "model_usage")
		want := map[string]any{}
		for alias, public := range mapping {
			want[alias] = public
		}
		if !reflect.DeepEqual(usage, map[string]any{"slice": "by_model", "models": want}) {
			t.Fatalf("model map lost types or gained connector-only settings: %#v", usage)
		}
	}
}

func TestModelUsageRejectsInvalidValues(t *testing.T) {
	tooMany := map[string]string{}
	for i := range 5001 {
		key, _ := json.Marshal(i)
		tooMany[string(key)] = "public-model"
	}
	oversize, _ := json.Marshal(tooMany)
	for _, tc := range []struct{ name, setting string }{
		{"export-disabled", "summaryExport.enabled=false"},
		{"missing-slice", "summaryExport.modelUsage.slice="},
		{"unknown-slice", "summaryExport.modelUsage.slice=not_selected"},
		{"intersection", `connector.slices=[{"name":"by_model","keys":["gen_ai.request.model","team.id"]}]`},
		{"wrong-key", `connector.slices=[{"name":"by_model","keys":["team.id"]}]`},
		{"empty-keys", `connector.slices=[{"name":"by_model","keys":[]}]`},
		{"duplicate-slice", `connector.slices=[{"name":"by_model","keys":["gen_ai.request.model"]},{"name":"by_model","keys":["gen_ai.request.model"]}]`},
		{"null-config", "summaryExport.modelUsage=null"},
		{"unknown-field", "summaryExport.modelUsage.contract=arbitrary"},
		{"not-boolean", "summaryExport.modelUsage.enabled=yes"},
		{"null-map", "summaryExport.modelUsage.models=null"},
		{"list-map", "summaryExport.modelUsage.models=[]"},
		{"empty-alias", `summaryExport.modelUsage.models={"":"public-model"}`},
		{"long-alias", `summaryExport.modelUsage.models={"` + strings.Repeat("a", 257) + `":"public-model"}`},
		{"long-utf8-alias", `summaryExport.modelUsage.models={"` + strings.Repeat("\u00e9", 129) + `":"public-model"}`},
		{"too-many-aliases", "summaryExport.modelUsage.models=" + string(oversize)},
		{"non-string-id", `summaryExport.modelUsage.models={"alias":123}`},
		{"empty-id", `summaryExport.modelUsage.models={"alias":""}`},
		{"reserved-id", `summaryExport.modelUsage.models={"alias":"__unmapped__"}`},
		{"nonpublic-id", `summaryExport.modelUsage.models={"alias":"https://host/?secret=value"}`},
		{"long-id", `summaryExport.modelUsage.models={"alias":"` + strings.Repeat("a", 129) + `"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := render(t, modelUsageSettings(tc.setting)...); err == nil {
				t.Fatal("invalid model usage configuration accepted")
			}
		})
	}
}
