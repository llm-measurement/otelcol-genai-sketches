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
	modelIdentityLiteLLM          = "litellm-1.104.0-chat-v1"
	modelIdentityLiteLLMStream    = "litellm-1.104.0-chat-stream-v1"
	modelIdentityLiteLLMResponses = "litellm-1.104.0-responses-v1"
	modelIdentityAgentGateway     = "agentgateway-1.6.0-chat-v1"
	modelIdentityEnvoy            = "envoy-ai-gateway-1.1.0-chat-v1"
)

func validateServedModelUsage(m *ModelUsageConfig) error {
	switch m.Identity {
	case "", modelIdentityAgentGateway, modelIdentityEnvoy:
		if m.ResponseModels != nil {
			return errors.New("summary_export.model_usage.response_models requires the LiteLLM identity profile")
		}
	case modelIdentityLiteLLMStream:
		if m.ResponseModels != nil {
			return errors.New("summary_export.model_usage.response_models is not supported by the LiteLLM streaming Chat identity profile")
		}
	case modelIdentityLiteLLM, modelIdentityLiteLLMResponses:
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
	sourceRule := "chat-span-strict-exact-v1"
	switch m.Identity {
	case modelIdentityLiteLLMStream:
		sourceRule = "chat-stream-selected-exact-v1"
	case modelIdentityLiteLLMResponses:
		sourceRule = "responses-span-strict-exact-v1"
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
		sourceRule, "selected-slice-then-retained-first-seen-overflow-v1"}
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
	nativeMarkers := m.identity == modelIdentityLiteLLMStream || m.identity == modelIdentityLiteLLMResponses
	if nativeMarkers || m.identity == modelIdentityLiteLLM && !operationPresent {
		callType, callQuality := servedIdentityValue(attrs, "llm.request.type")
		route, routeQuality := servedIdentityValue(attrs, "http.route")
		wantCall, wantRoute := "acompletion", "/v1/chat/completions"
		if m.identity == modelIdentityLiteLLMResponses {
			wantCall, wantRoute = "aresponses", "/v1/responses"
		}
		matched := callQuality == "" && callType == wantCall && routeQuality == "" && route == wantRoute
		if nativeMarkers && !matched {
			return "__unmapped__"
		}
		if matched && !operationPresent {
			operation, quality = "chat", ""
		}
	}
	if quality != "" || operation != "chat" {
		return "__unmapped__"
	}
	streaming, malformedStream := false, false
	seenStream, seenLegacyStream, stream, legacyStream := false, false, false, false
	attrs.Range(func(key string, value pcommon.Value) bool {
		if key == "gen_ai.request.stream" || key == "llm.is_streaming" {
			seen, flag := &seenStream, &stream
			if key == "llm.is_streaming" {
				seen, flag = &seenLegacyStream, &legacyStream
			}
			malformedStream = malformedStream || *seen || value.Type() != pcommon.ValueTypeBool
			if value.Type() == pcommon.ValueTypeBool {
				*flag = value.Bool()
				streaming = streaming || *flag
			}
			*seen = true
		}
		return true
	})
	if malformedStream || !nativeMarkers && streaming {
		return "__unmapped__"
	}
	if nativeMarkers && seenStream && seenLegacyStream && stream != legacyStream {
		return "__unmapped__"
	}
	if m.identity == modelIdentityLiteLLMStream && (!seenStream || !stream) {
		return "__unmapped__"
	}
	attribute := "gen_ai.response.model"
	if m.identity == modelIdentityLiteLLM || nativeMarkers {
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
	// Streaming Chat's response field echoes dispatch, not an independent
	// provider snapshot. Only chat-v1 and Responses verify response identity.
	if m.identity != modelIdentityLiteLLM && m.identity != modelIdentityLiteLLMResponses {
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
