// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay Erramilli and Codex
package genaisketchconnector

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	sketchhash "github.com/llm-measurement/llm-sketchkit/go/sketchkit/hash"
)

const (
	maxWindowDuration       = 24 * time.Hour
	maxRetentionWindows     = 120
	maxConfiguredSlices     = 5000
	maxConfiguredTopK       = 100
	maxConfiguredSliceCount = 32
	maxSliceKeys            = 8
	maxAttributeSources     = 16
	maxAttributeKeyBytes    = 128
	maxOperationFilters     = 64
)

type Config struct {
	WindowDuration   time.Duration          `mapstructure:"window_duration"`
	RetentionWindows int                    `mapstructure:"retention_windows"`
	MaxSlices        int                    `mapstructure:"max_slices"`
	TopK             int                    `mapstructure:"topk"`
	Profiles         ProfilesConfig         `mapstructure:"profiles"`
	Hashing          HashingConfig          `mapstructure:"hashing"`
	OperationFilter  OperationFilterConfig  `mapstructure:"operation_filter"`
	MCP              MCPConfig              `mapstructure:"mcp"`
	Slices           []SliceConfig          `mapstructure:"slices"`
	Fields           map[string]FieldConfig `mapstructure:"fields"`
	Weights          WeightsConfig          `mapstructure:"weights"`
	Dedup            DedupConfig            `mapstructure:"dedup"`
}

type ProfilesConfig struct {
	HLLPP         string `mapstructure:"hllpp"`
	FrequentItems string `mapstructure:"frequent_items"`
	Bloom         string `mapstructure:"bloom"`
}

type HashingConfig struct {
	Algo      string `mapstructure:"algo"`
	SecretEnv string `mapstructure:"secret_env"`
}

type OperationFilterConfig struct {
	LLMOperations []string `mapstructure:"llm_operations"`
}

type MCPConfig struct {
	Enabled    bool             `mapstructure:"enabled"`
	ToolErrors ToolErrorsConfig `mapstructure:"tool_errors"`
}

type ToolErrorsConfig struct {
	Enabled bool `mapstructure:"enabled"`
}

type SliceConfig struct {
	Name                   string   `mapstructure:"name"`
	Keys                   []string `mapstructure:"keys"`
	FromResourceAttributes []string `mapstructure:"from_resource_attributes"`
}

type FieldConfig struct {
	FromAttributes         []string `mapstructure:"from_attributes"`
	FromResourceAttributes []string `mapstructure:"from_resource_attributes"`
	Canonicalization       string   `mapstructure:"canonicalization"`
	Domain                 string   `mapstructure:"domain"`
}

type WeightsConfig struct {
	InputTokensFrom           []string `mapstructure:"input_tokens_from"`
	OutputTokensFrom          []string `mapstructure:"output_tokens_from"`
	CacheReadInputTokensFrom  []string `mapstructure:"cache_read_input_tokens_from"`
	CacheWriteInputTokensFrom []string `mapstructure:"cache_write_input_tokens_from"`
	ReasoningOutputTokensFrom []string `mapstructure:"reasoning_output_tokens_from"`
	FallbackWhenMissing       string   `mapstructure:"fallback_when_missing"`
}

type DedupConfig struct {
	Enabled       bool     `mapstructure:"enabled"`
	RequestIDFrom []string `mapstructure:"request_id_from"`
}

func defaultConfig() *Config {
	return &Config{
		WindowDuration:   60 * time.Second,
		RetentionWindows: 10,
		MaxSlices:        2000,
		TopK:             20,
		Profiles: ProfilesConfig{
			HLLPP:         "small",
			FrequentItems: "small",
			Bloom:         "micro",
		},
		Hashing: HashingConfig{
			Algo:      "hmac_sha256_64",
			SecretEnv: "GENAI_SKETCH_SECRET",
		},
		OperationFilter: OperationFilterConfig{
			LLMOperations: []string{"chat", "generate_content", "text_completion", "embeddings"},
		},
		Slices: []SliceConfig{
			{Name: "by_model", Keys: []string{"gen_ai.request.model"}, FromResourceAttributes: []string{"gen_ai.request.model"}},
			{Name: "by_team_model", Keys: []string{"team.id", "gen_ai.request.model"}, FromResourceAttributes: []string{"team.id", "gen_ai.request.model"}},
		},
		Fields: map[string]FieldConfig{
			"user_key": {
				FromAttributes:         []string{"enduser.id", "user.id"},
				FromResourceAttributes: []string{"enduser.id", "user.id"},
				Canonicalization:       "text_v1",
				Domain:                 "user:v1",
			},
			"prompt_key": {
				FromAttributes:         []string{"gen_ai.request.prompt"},
				FromResourceAttributes: []string{"gen_ai.request.prompt"},
				Canonicalization:       "text_v1",
				Domain:                 "prompt:v1",
			},
			"doc_key": {
				FromAttributes:         []string{"retrieval.doc_id"},
				FromResourceAttributes: []string{"retrieval.doc_id"},
				Canonicalization:       "text_v1",
				Domain:                 "retrieval-doc:v1",
			},
			"mcp_session_key": {
				FromAttributes:         []string{"mcp.session.id"},
				FromResourceAttributes: []string{"mcp.session.id"},
				Canonicalization:       "text_v1",
				Domain:                 "mcp-session:v1",
			},
			"mcp_method_key": {
				FromAttributes:         []string{"mcp.method.name"},
				FromResourceAttributes: []string{"mcp.method.name"},
				Canonicalization:       "text_v1",
				Domain:                 "mcp-method:v1",
			},
			"mcp_resource_key": {
				FromAttributes:         []string{"mcp.resource.uri"},
				FromResourceAttributes: []string{"mcp.resource.uri"},
				Canonicalization:       "text_v1",
				Domain:                 "retrieval-doc:v1",
			},
		},
		Weights: WeightsConfig{
			InputTokensFrom: []string{
				"gen_ai.usage.input_tokens",
				"gen_ai.usage.prompt_tokens",
			},
			OutputTokensFrom: []string{
				"gen_ai.usage.output_tokens",
				"gen_ai.usage.completion_tokens",
			},
			CacheReadInputTokensFrom: []string{
				"gen_ai.usage.cache_read.input_tokens",
			},
			CacheWriteInputTokensFrom: []string{
				"gen_ai.usage.cache_write.input_tokens",
				"gen_ai.usage.cache_creation.input_tokens",
			},
			ReasoningOutputTokensFrom: []string{
				"gen_ai.usage.reasoning.output_tokens",
			},
			FallbackWhenMissing: "request_count_only",
		},
		Dedup: DedupConfig{
			Enabled:       false,
			RequestIDFrom: []string{"gen_ai.response.id", "request.id"},
		},
	}
}

func (cfg *Config) Validate() error {
	var errs []error

	if cfg.WindowDuration <= 0 {
		errs = append(errs, errors.New("window_duration must be greater than 0"))
	} else if cfg.WindowDuration > maxWindowDuration {
		errs = append(errs, fmt.Errorf("window_duration must be <= %s", maxWindowDuration))
	}
	if cfg.RetentionWindows <= 0 {
		errs = append(errs, errors.New("retention_windows must be greater than 0"))
	} else if cfg.RetentionWindows > maxRetentionWindows {
		errs = append(errs, fmt.Errorf("retention_windows must be <= %d", maxRetentionWindows))
	}
	if cfg.MaxSlices <= 0 {
		errs = append(errs, errors.New("max_slices must be greater than 0"))
	} else if cfg.MaxSlices > maxConfiguredSlices {
		errs = append(errs, fmt.Errorf("max_slices must be <= %d", maxConfiguredSlices))
	}
	if cfg.TopK < 0 {
		errs = append(errs, errors.New("topk must be greater than or equal to 0"))
	} else if cfg.TopK > maxConfiguredTopK {
		errs = append(errs, fmt.Errorf("topk must be <= %d", maxConfiguredTopK))
	}

	errs = append(errs, validateProfile("profiles.hllpp", cfg.Profiles.HLLPP, "micro", "small", "default"))
	errs = append(errs, validateProfile("profiles.frequent_items", cfg.Profiles.FrequentItems, "micro", "small", "default"))
	errs = append(errs, validateProfile("profiles.bloom", cfg.Profiles.Bloom, "micro", "small", "default"))

	if cfg.Hashing.Algo != "hmac_sha256_64" {
		errs = append(errs, fmt.Errorf("hashing.algo must be hmac_sha256_64, got %q", cfg.Hashing.Algo))
	}
	if strings.TrimSpace(cfg.Hashing.SecretEnv) == "" {
		errs = append(errs, errors.New("hashing.secret_env must not be empty"))
	}

	if len(cfg.OperationFilter.LLMOperations) == 0 {
		errs = append(errs, errors.New("operation_filter.llm_operations must contain at least one operation"))
	} else if len(cfg.OperationFilter.LLMOperations) > maxOperationFilters {
		errs = append(errs, fmt.Errorf("operation_filter.llm_operations must contain at most %d operations", maxOperationFilters))
	}
	seenOperations := make(map[string]struct{}, len(cfg.OperationFilter.LLMOperations))
	for i, operation := range cfg.OperationFilter.LLMOperations {
		if strings.TrimSpace(operation) == "" {
			errs = append(errs, fmt.Errorf("operation_filter.llm_operations[%d] must not be empty", i))
			continue
		}
		if _, ok := seenOperations[operation]; ok {
			errs = append(errs, fmt.Errorf("operation_filter.llm_operations[%d] %q is duplicated", i, operation))
		}
		seenOperations[operation] = struct{}{}
	}
	if cfg.MCP.ToolErrors.Enabled && !cfg.MCP.Enabled {
		errs = append(errs, errors.New("mcp.tool_errors.enabled requires mcp.enabled"))
	}

	errs = append(errs, validateSlices(cfg.Slices))
	errs = append(errs, validateFields(cfg.Fields))
	errs = append(errs, validateTokenSources(cfg.Weights))

	if _, err := compileRuntimeConfig(cfg); err != nil {
		errs = append(errs, err)
	}

	if cfg.Weights.FallbackWhenMissing != "" && cfg.Weights.FallbackWhenMissing != "request_count_only" {
		errs = append(errs, fmt.Errorf("weights.fallback_when_missing must be request_count_only, got %q", cfg.Weights.FallbackWhenMissing))
	}
	if cfg.Dedup.Enabled && len(cfg.Dedup.RequestIDFrom) == 0 {
		errs = append(errs, errors.New("dedup.request_id_from must contain at least one attribute key when dedup.enabled is true"))
	}
	errs = append(errs, validateAttributeSources("dedup.request_id_from", cfg.Dedup.RequestIDFrom))
	if overlaps := cfg.SensitiveSliceHashedFieldOverlaps(); len(overlaps) > 0 {
		errs = append(errs, fmt.Errorf("slice keys overlap sensitive hashed-field source attributes: %s", strings.Join(overlaps, ", ")))
	}

	return errors.Join(errs...)
}

func validateSlices(slices []SliceConfig) error {
	var errs []error
	if len(slices) == 0 {
		errs = append(errs, errors.New("slices must contain at least one slice"))
	} else if len(slices) > maxConfiguredSliceCount {
		errs = append(errs, fmt.Errorf("slices must contain at most %d slices", maxConfiguredSliceCount))
	}

	seenSlices := make(map[string]struct{}, len(slices))
	for i, slice := range slices {
		errs = append(errs, validateSlice(i, slice, seenSlices))
	}
	return errors.Join(errs...)
}

func validateSlice(index int, slice SliceConfig, seenSlices map[string]struct{}) error {
	var errs []error
	prefix := fmt.Sprintf("slices[%d]", index)
	if strings.TrimSpace(slice.Name) == "" {
		errs = append(errs, fmt.Errorf("%s.name must not be empty", prefix))
	}
	if _, ok := seenSlices[slice.Name]; slice.Name != "" && ok {
		errs = append(errs, fmt.Errorf("%s.name %q is duplicated", prefix, slice.Name))
	}
	seenSlices[slice.Name] = struct{}{}

	keySet := make(map[string]struct{}, len(slice.Keys))
	if len(slice.Keys) == 0 {
		errs = append(errs, fmt.Errorf("%s.keys must contain at least one attribute key", prefix))
	} else if len(slice.Keys) > maxSliceKeys {
		errs = append(errs, fmt.Errorf("%s.keys must contain at most %d attribute keys", prefix, maxSliceKeys))
	}
	for j, key := range slice.Keys {
		if strings.TrimSpace(key) == "" {
			errs = append(errs, fmt.Errorf("%s.keys[%d] must not be empty", prefix, j))
		} else if len(key) > maxAttributeKeyBytes {
			errs = append(errs, fmt.Errorf("%s.keys[%d] must be <= %d bytes", prefix, j, maxAttributeKeyBytes))
		}
		if isSensitiveMCPSliceKey(key) {
			errs = append(errs, fmt.Errorf("%s.keys[%d] %q is sensitive and must be hashed, not exported as a slice label", prefix, j, key))
		}
		keySet[key] = struct{}{}
	}

	errs = append(errs, validateSliceResourceKeys(prefix, slice.FromResourceAttributes, keySet))
	return errors.Join(errs...)
}

func validateSliceResourceKeys(prefix string, keys []string, sliceKeySet map[string]struct{}) error {
	var errs []error
	if len(keys) > maxAttributeSources {
		errs = append(errs, fmt.Errorf("%s.from_resource_attributes must contain at most %d attribute keys", prefix, maxAttributeSources))
	}
	seen := make(map[string]struct{}, len(keys))
	for j, key := range keys {
		if strings.TrimSpace(key) == "" {
			errs = append(errs, fmt.Errorf("%s.from_resource_attributes[%d] must not be empty", prefix, j))
			continue
		}
		if len(key) > maxAttributeKeyBytes {
			errs = append(errs, fmt.Errorf("%s.from_resource_attributes[%d] must be <= %d bytes", prefix, j, maxAttributeKeyBytes))
		}
		if _, ok := sliceKeySet[key]; !ok {
			errs = append(errs, fmt.Errorf("%s.from_resource_attributes[%d] %q must also appear in %s.keys", prefix, j, key, prefix))
		}
		if _, ok := seen[key]; ok {
			errs = append(errs, fmt.Errorf("%s.from_resource_attributes[%d] %q is duplicated", prefix, j, key))
		}
		seen[key] = struct{}{}
	}
	return errors.Join(errs...)
}

func validateFields(fields map[string]FieldConfig) error {
	var errs []error
	for name, field := range fields {
		errs = append(errs, validateField(name, field))
	}
	return errors.Join(errs...)
}

func validateField(name string, field FieldConfig) error {
	var errs []error
	prefix := fmt.Sprintf("fields.%s", name)
	if len(field.FromAttributes) == 0 && len(field.FromResourceAttributes) == 0 {
		errs = append(errs, fmt.Errorf("%s.from_attributes or %s.from_resource_attributes must contain at least one attribute key", prefix, prefix))
	}
	errs = append(errs, validateAttributeSources(prefix+".from_attributes", field.FromAttributes))
	errs = append(errs, validateAttributeSources(prefix+".from_resource_attributes", field.FromResourceAttributes))
	if field.Canonicalization != "text_v1" {
		errs = append(errs, fmt.Errorf("%s.canonicalization must be text_v1, got %q", prefix, field.Canonicalization))
	}
	if strings.TrimSpace(field.Domain) == "" {
		errs = append(errs, fmt.Errorf("%s.domain must not be empty", prefix))
	} else if !sketchhash.IsRegisteredDomain(sketchhash.Domain(field.Domain)) {
		errs = append(errs, fmt.Errorf("%s.domain must be a registered hash domain, got %q", prefix, field.Domain))
	}
	return errors.Join(errs...)
}

func validateProfile(path string, value string, allowed ...string) error {
	for _, candidate := range allowed {
		if value == candidate {
			return nil
		}
	}
	return fmt.Errorf("%s must be one of %s, got %q", path, strings.Join(allowed, ", "), value)
}

func validateAttributeSources(path string, attributes []string) error {
	var errs []error
	if len(attributes) > maxAttributeSources {
		errs = append(errs, fmt.Errorf("%s must contain at most %d attribute keys", path, maxAttributeSources))
	}
	seen := make(map[string]struct{}, len(attributes))
	for i, attr := range attributes {
		if strings.TrimSpace(attr) == "" {
			errs = append(errs, fmt.Errorf("%s[%d] must not be empty", path, i))
		} else if len(attr) > maxAttributeKeyBytes {
			errs = append(errs, fmt.Errorf("%s[%d] must be <= %d bytes", path, i, maxAttributeKeyBytes))
		}
		if _, ok := seen[attr]; ok && attr != "" {
			errs = append(errs, fmt.Errorf("%s[%d] %q is duplicated", path, i, attr))
		}
		seen[attr] = struct{}{}
	}
	return errors.Join(errs...)
}

func validateTokenSources(weights WeightsConfig) error {
	var errs []error
	for _, item := range []struct {
		path     string
		sources  []string
		required bool
	}{
		{path: "weights.input_tokens_from", sources: weights.InputTokensFrom, required: true},
		{path: "weights.output_tokens_from", sources: weights.OutputTokensFrom, required: true},
		{path: "weights.cache_read_input_tokens_from", sources: weights.CacheReadInputTokensFrom},
		{path: "weights.cache_write_input_tokens_from", sources: weights.CacheWriteInputTokensFrom},
		{path: "weights.reasoning_output_tokens_from", sources: weights.ReasoningOutputTokensFrom},
	} {
		if item.required && len(item.sources) == 0 {
			errs = append(errs, fmt.Errorf("%s must contain at least one attribute key", item.path))
		}
		errs = append(errs, validateAttributeSources(item.path, item.sources))
	}
	return errors.Join(errs...)
}

func isSensitiveMCPSliceKey(key string) bool {
	switch key {
	case "mcp.session.id", "mcp.resource.uri", "jsonrpc.request.id":
		return true
	default:
		return false
	}
}

func (cfg *Config) SliceHashedFieldOverlaps() []string {
	return cfg.sliceHashedFieldOverlaps(false)
}

func (cfg *Config) SensitiveSliceHashedFieldOverlaps() []string {
	return cfg.sliceHashedFieldOverlaps(true)
}

func (cfg *Config) sliceHashedFieldOverlaps(sensitiveOnly bool) []string {
	hashedSources := make(map[string]struct{})
	for name, field := range cfg.Fields {
		if sensitiveOnly && !isSensitiveHashedField(name) {
			continue
		}
		for _, attr := range field.FromAttributes {
			hashedSources[attr] = struct{}{}
		}
		for _, attr := range field.FromResourceAttributes {
			hashedSources[attr] = struct{}{}
		}
	}

	var overlaps []string
	for _, slice := range cfg.Slices {
		for _, key := range slice.Keys {
			if _, ok := hashedSources[key]; ok {
				overlaps = append(overlaps, slice.Name+":"+key)
			}
		}
	}
	sort.Strings(overlaps)
	return overlaps
}

func isSensitiveHashedField(name string) bool {
	switch name {
	case fieldMCPMethodKey:
		return false
	default:
		return true
	}
}
