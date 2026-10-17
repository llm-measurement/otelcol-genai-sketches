// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package genaisketchconnector

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"unicode/utf8"

	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/modelusage"
	"go.opentelemetry.io/collector/pdata/pcommon"
)

const (
	modelIdentityLiteLLM      = "litellm-1.104.0-chat-v1"
	modelIdentityAgentGateway = "agentgateway-1.6.0-chat-v1"
	modelIdentityEnvoy        = "envoy-ai-gateway-1.1.0-chat-v1"
)

func validateServedModelUsage(m *ModelUsageConfig) error {
	switch m.Identity {
	case "", modelIdentityAgentGateway, modelIdentityEnvoy:
		if m.ResponseModels != nil {
			return errors.New("summary_export.model_usage.response_models requires the LiteLLM identity profile")
		}
	case modelIdentityLiteLLM:
		if m.ResponseModels == nil || len(m.ResponseModels) > modelusage.MaxModels {
			return errors.New("summary_export.model_usage LiteLLM identity requires a bounded response_models map")
		}
		for observed, public := range m.ResponseModels {
			if observed == "" || len(observed) > maxSliceLabelPartBytes || !utf8.ValidString(observed) || !modelusage.IsPublicID(public) {
				return errors.New("summary_export.model_usage has an invalid response model mapping")
			}
		}
	default:
		return errors.New("summary_export.model_usage has an unsupported identity profile")
	}
	return nil
}

func servedModelUsageContract(accountingID string, m *ModelUsageConfig, routing modelusage.Routing) (string, error) {
	if err := validateServedModelUsage(m); err != nil {
		return "", err
	}
	// This anchor describes the real request slice, not a fabricated served slice.
	anchor, err := modelusage.ContractID(accountingID, map[string]string{}, routing)
	if err != nil {
		return "", err
	}
	responseModels := m.ResponseModels
	if responseModels == nil {
		responseModels = map[string]string{}
	}
	manifest := struct {
		Domain         string            `json:"domain"`
		RoutingAnchor  string            `json:"routing_anchor"`
		Identity       string            `json:"identity"`
		Models         map[string]string `json:"models"`
		ResponseModels map[string]string `json:"response_models"`
		SourceRule     string            `json:"source_rule"`
		AdmissionRule  string            `json:"admission_rule"`
	}{"collector-served-identity-v1", anchor, m.Identity, m.Models, responseModels,
		"chat-span-strict-exact-v1", "selected-slice-then-retained-first-seen-overflow-v1"}
	data, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return "model-usage-v1:" + hex.EncodeToString(digest[:]), nil
}

// Identity fields are span-local and typed. In particular, a resource model or
// a duplicate/non-string attribute cannot stand in for an attempt's identity.
func servedIdentityValue(attrs pcommon.Map, key string) (string, string) {
	var value string
	count, invalid := 0, false
	attrs.Range(func(k string, v pcommon.Value) bool {
		if k == key {
			count++
			if v.Type() != pcommon.ValueTypeStr || !utf8.ValidString(v.Str()) {
				invalid = true
			} else {
				value = v.Str()
			}
		}
		return true
	})
	switch {
	case count > 1 || invalid:
		return "", "__unmapped__"
	case count == 0 || value == "":
		return "", "__missing__"
	case len(value) > maxSliceLabelPartBytes:
		return "", "__too_long__"
	default:
		return value, ""
	}
}

func (m *compiledModelUsage) servedModelID(attrs pcommon.Map) string {
	operation, quality := servedIdentityValue(attrs, "gen_ai.operation.name")
	_, operationPresent := attrs.Get("gen_ai.operation.name")
	if m.identity == modelIdentityLiteLLM && !operationPresent {
		callType, callQuality := servedIdentityValue(attrs, "llm.request.type")
		route, routeQuality := servedIdentityValue(attrs, "http.route")
		if callQuality == "" && callType == "acompletion" && routeQuality == "" && route == "/v1/chat/completions" {
			operation, quality = "chat", ""
		}
	}
	if quality != "" || operation != "chat" {
		return "__unmapped__"
	}
	streaming, seenStream, seenLegacyStream := false, false, false
	attrs.Range(func(key string, value pcommon.Value) bool {
		if key == "gen_ai.request.stream" || key == "llm.is_streaming" {
			seen := &seenStream
			if key == "llm.is_streaming" {
				seen = &seenLegacyStream
			}
			streaming = streaming || *seen || value.Type() != pcommon.ValueTypeBool || value.Bool()
			*seen = true
		}
		return true
	})
	if streaming {
		return "__unmapped__"
	}
	attribute := "gen_ai.response.model"
	if m.identity == modelIdentityLiteLLM {
		attribute = "litellm.provider.model"
	}
	observed, quality := servedIdentityValue(attrs, attribute)
	if quality != "" {
		return quality
	}
	id, ok := m.models[observed]
	if !ok {
		return "__unmapped__"
	}
	if m.identity != modelIdentityLiteLLM {
		return id
	}
	response, quality := servedIdentityValue(attrs, "gen_ai.response.model")
	_, responsePresent := attrs.Get("gen_ai.response.model")
	if !responsePresent {
		return id // The pinned gateway's selected model is independently available.
	}
	if quality != "" {
		return "__unmapped__"
	}
	// Equality with model_group does not prove an alias rewrite: raw responses
	// and fallbacks can coincidentally return that name. Never bypass verification.
	if public, ok := m.responseModels[response]; !ok || public != id {
		return "__unmapped__"
	}
	return id
}
