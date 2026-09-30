// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package genaisketchconnector

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
)

const fieldSessionKey = "session_key"

// TopKKeyConfig selects a hashed identity and the unit accumulated for model attempts.
type TopKKeyConfig struct {
	Field  string `mapstructure:"field"`
	Weight string `mapstructure:"weight"`
}

var topKFieldName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

func configuredTopKKeys(cfg *Config) ([]TopKKeyConfig, error) {
	keys := append([]TopKKeyConfig(nil), cfg.TopKKeys...)
	if len(keys) == 0 {
		keys = []TopKKeyConfig{{Field: fieldPromptKey}}
	}
	if len(keys) > 4 {
		return nil, errors.New("topk_keys accepts at most four keys")
	}
	seen := make(map[string]bool, len(keys))
	for i := range keys {
		key := &keys[i]
		if !topKFieldName.MatchString(key.Field) {
			return nil, errors.New("topk_keys field must be a lowercase identifier of at most 32 characters")
		}
		if _, ok := cfg.Fields[key.Field]; !ok {
			return nil, errors.New("topk_keys field must name a configured hashed field")
		}
		if key.Field == fieldToolErrorKey {
			return nil, errors.New("tool_error_key is reserved for MCP tool error accounting")
		}
		if seen[key.Field] {
			return nil, errors.New("topk_keys must not contain duplicate fields")
		}
		seen[key.Field] = true
		if key.Weight == "" {
			key.Weight = "tokens"
		}
		if key.Weight != "tokens" && key.Weight != "requests" {
			return nil, errors.New("topk_keys weight must be tokens or requests")
		}
	}
	return keys, nil
}

func (key TopKKeyConfig) measurement() string {
	names := map[string]string{fieldPromptKey: "top_prompts", fieldUserKey: "top_users", fieldSessionKey: "top_sessions", fieldDocKey: "top_docs", fieldMCPSessionKey: "top_mcp_sessions", fieldMCPMethodKey: "top_mcp_methods", fieldMCPResourceKey: "top_mcp_resources"}
	name, ok := names[key.Field]
	if !ok {
		return "top_key." + key.Field + "." + key.Weight
	}
	if key.Weight == "requests" {
		name += "_requests"
	}
	return name
}

func (key TopKKeyConfig) legacy() bool { return key.Field == fieldPromptKey && key.Weight == "tokens" }

// A zero-valued counter records the optional measurement's extraction contract.
// It is not a count. Distinct names make incompatible contracts fail closed in
// summary.Combine without changing the base accounting ID or the envelope format.
func (key TopKKeyConfig) contract(field FieldConfig) string {
	data, _ := json.Marshal(struct {
		Field  FieldConfig
		Weight string
	}{field, key.Weight})
	digest := sha256.Sum256(data)
	return "topk_contract.v1." + key.measurement() + "." + hex.EncodeToString(digest[:16])
}

func baseMeasurementField(name string) bool {
	switch name {
	case fieldUserKey, fieldPromptKey, fieldDocKey, fieldMCPSessionKey, fieldMCPMethodKey, fieldMCPResourceKey:
		return true
	default:
		return false
	}
}
