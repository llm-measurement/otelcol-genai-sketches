// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay Erramilli and Codex
package genaisketchconnector

import (
	"strings"
	"testing"
)

func TestDefaultConfigValidates(t *testing.T) {
	if err := defaultConfig().Validate(); err != nil {
		t.Fatalf("default config should validate: %v", err)
	}
}

func TestDefaultDedupSourcesDoNotUseTraceID(t *testing.T) {
	got := strings.Join(defaultConfig().Dedup.RequestIDFrom, ",")
	if want := "gen_ai.response.id,request.id"; got != want {
		t.Fatalf("default dedup sources = %q, want %q", got, want)
	}
}

func TestValidateAllowsZeroTopKToDisableLogSurface(t *testing.T) {
	cfg := defaultConfig()
	cfg.TopK = 0
	if err := cfg.Validate(); err != nil {
		t.Fatalf("topk: 0 should validate: %v", err)
	}
}

func TestValidateRejectsNegativeTopK(t *testing.T) {
	cfg := defaultConfig()
	cfg.TopK = -1
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "topk must be greater than or equal to 0") {
		t.Fatalf("Validate() error = %v, want negative topk rejection", err)
	}
}

func TestValidateReportsHelpfulPaths(t *testing.T) {
	cfg := defaultConfig()
	cfg.WindowDuration = 0
	cfg.Hashing.Algo = "sha1"
	cfg.Slices = []SliceConfig{{Name: "", Keys: []string{""}}}
	cfg.Fields["prompt_key"] = FieldConfig{Canonicalization: "text_v2"}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error")
	}

	msg := err.Error()
	for _, want := range []string{
		"window_duration",
		"hashing.algo",
		"slices[0].name",
		"slices[0].keys[0]",
		"fields.prompt_key.from_attributes",
		"fields.prompt_key.canonicalization",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("validation error %q did not include %q", msg, want)
		}
	}
}

func TestSliceHashedFieldOverlapsIncludesResourceSources(t *testing.T) {
	cfg := defaultConfig()
	cfg.Slices = []SliceConfig{{Name: "by_team", Keys: []string{"team.id"}}}
	cfg.Fields["user_key"] = FieldConfig{
		FromAttributes:         []string{"enduser.id"},
		FromResourceAttributes: []string{"team.id"},
		Canonicalization:       "text_v1",
		Domain:                 "user:v1",
	}

	overlaps := cfg.SliceHashedFieldOverlaps()
	if len(overlaps) != 1 || overlaps[0] != "by_team:team.id" {
		t.Fatalf("resource-source overlaps = %#v, want [by_team:team.id]", overlaps)
	}
}

func TestValidateRejectsSensitiveHashedFieldOverlap(t *testing.T) {
	cfg := defaultConfig()
	cfg.Slices = []SliceConfig{{Name: "by_user", Keys: []string{"enduser.id"}}}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "slice keys overlap sensitive hashed-field source attributes") {
		t.Fatalf("Validate() error = %v, want sensitive overlap rejection", err)
	}
}

func TestValidateAllowsMCPMethodSliceOverlap(t *testing.T) {
	cfg := defaultConfig()
	cfg.MCP.Enabled = true
	cfg.Slices = []SliceConfig{{Name: "by_mcp_method", Keys: []string{"mcp.method.name"}}}
	err := cfg.Validate()
	if err != nil {
		t.Fatalf("Validate() error = %v, want mcp.method.name overlap allowed", err)
	}
}

func TestValidateRejectsPathologicalBounds(t *testing.T) {
	cfg := defaultConfig()
	cfg.TopK = maxConfiguredTopK + 1
	cfg.MaxSlices = maxConfiguredSlices + 1
	cfg.RetentionWindows = maxRetentionWindows + 1
	cfg.OperationFilter.LLMOperations = make([]string, maxOperationFilters+1)
	for i := range cfg.OperationFilter.LLMOperations {
		cfg.OperationFilter.LLMOperations[i] = "op-" + string(rune('a'+i%26))
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error")
	}
	for _, want := range []string{"topk", "max_slices", "retention_windows", "operation_filter.llm_operations"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("validation error %q did not include %q", err, want)
		}
	}
}

func TestValidateRejectsSensitiveMCPLabelKeys(t *testing.T) {
	for _, key := range []string{"mcp.session.id", "mcp.resource.uri", "jsonrpc.request.id"} {
		t.Run(key, func(t *testing.T) {
			cfg := defaultConfig()
			cfg.Slices = []SliceConfig{{Name: "unsafe", Keys: []string{key}}}
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), "must be hashed, not exported as a slice label") {
				t.Fatalf("Validate() error = %v, want sensitive-label rejection", err)
			}
		})
	}
}

func TestValidateRequiresMCPForToolErrors(t *testing.T) {
	cfg := defaultConfig()
	cfg.MCP.ToolErrors.Enabled = true
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "mcp.tool_errors.enabled requires mcp.enabled") {
		t.Fatalf("Validate() error = %v, want MCP profile dependency", err)
	}
}

func TestValidateRejectsSliceResourceSourceOutsideKeys(t *testing.T) {
	cfg := defaultConfig()
	cfg.Slices = []SliceConfig{
		{Name: "by_team", Keys: []string{"team.id"}, FromResourceAttributes: []string{"tenant.id"}},
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "must also appear in slices[0].keys") {
		t.Fatalf("Validate() error = %v, want slice resource-source rejection", err)
	}
}

func TestValidateRequiresAggregateTokenSources(t *testing.T) {
	for _, field := range []string{"input", "output"} {
		t.Run(field, func(t *testing.T) {
			cfg := defaultConfig()
			if field == "input" {
				cfg.Weights.InputTokensFrom = nil
			} else {
				cfg.Weights.OutputTokensFrom = nil
			}
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), "must contain at least one attribute key") {
				t.Fatalf("Validate() error = %v, want required token-source rejection", err)
			}
		})
	}
}

func TestValidateRejectsDuplicateTokenSource(t *testing.T) {
	cfg := defaultConfig()
	cfg.Weights.InputTokensFrom = []string{"gen_ai.usage.input_tokens", "gen_ai.usage.input_tokens"}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "weights.input_tokens_from[1]") || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("Validate() error = %v, want duplicate token-source rejection", err)
	}
}

func TestValidateRejectsDuplicateDedupSource(t *testing.T) {
	cfg := defaultConfig()
	cfg.Dedup.RequestIDFrom = []string{"request.id", "request.id"}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "dedup.request_id_from[1]") || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("Validate() error = %v, want duplicate dedup-source rejection", err)
	}
}
