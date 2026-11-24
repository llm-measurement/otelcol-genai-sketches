// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package genaisketchconnector

import (
	"fmt"
	"strings"

	"go.opentelemetry.io/collector/pdata/ptrace"
)

const (
	rootInvocationsMetricName       = "gen_ai_sketch_root_invocations_total"
	maxRootInvocationOperations     = 32
	maxRootInvocationOperationBytes = 64
)

// RootInvocationsConfig selects parentless in-process invocation operations.
type RootInvocationsConfig struct {
	Enabled    bool     `mapstructure:"enabled"`
	Operations []string `mapstructure:"operations"`
}

func compileRootInvocations(cfg RootInvocationsConfig) (map[string]struct{}, error) {
	operations := cfg.Operations
	if operations == nil {
		operations = []string{"invoke_agent", "invoke_workflow"}
	}
	if len(operations) == 0 || len(operations) > maxRootInvocationOperations {
		return nil, fmt.Errorf("root_invocations.operations must contain 1 to %d operations", maxRootInvocationOperations)
	}
	compiled := make(map[string]struct{}, len(operations))
	for i, operation := range operations {
		if !validRootInvocationOperation(operation) {
			return nil, fmt.Errorf("root_invocations.operations[%d] must be an invoke_ name with a nonempty lowercase ASCII letter, digit or underscore suffix, at most %d bytes", i, maxRootInvocationOperationBytes)
		}
		if _, exists := compiled[operation]; exists {
			return nil, fmt.Errorf("root_invocations.operations[%d] is duplicated", i)
		}
		compiled[operation] = struct{}{}
	}
	if !cfg.Enabled {
		return nil, nil
	}
	return compiled, nil
}

func validRootInvocationOperation(operation string) bool {
	const prefix = "invoke_"
	if len(operation) > maxRootInvocationOperationBytes || !strings.HasPrefix(operation, prefix) || len(operation) == len(prefix) {
		return false
	}
	for _, c := range []byte(operation[len(prefix):]) {
		if c != '_' && (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

func (s *collectorState) isRootInvocation(span ptrace.Span, operation string) bool {
	if span.Kind() != ptrace.SpanKindInternal || !span.ParentSpanID().IsEmpty() {
		return false
	}
	_, selected := s.cfg.rootInvocationOperations[operation]
	return selected
}

func (u spanUpdate) rootInvocationCount() uint64 {
	if u.rootInvocation {
		return 1
	}
	return 0
}
