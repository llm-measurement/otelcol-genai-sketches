// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package genaisketchconnector

import (
	"maps"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"
)

const usageProvenanceMetricName = "gen_ai_sketch_usage_provenance_total"

var usageProvenanceFields = [2]string{"input", "output"}
var usageProvenanceStates = [4]string{"provider_reported", "inferred", "unavailable", "unknown"}

type usageProvenanceCounts [2][4]uint64

// These are instrumenter assertions, not authenticated evidence of origin.
// Missing, invalid, and inconsistent assertions must never imply provider usage.
func observeUsageProvenance(attrs pcommon.Map, observations [tokenFieldCount]tokenObservation) usageProvenanceCounts {
	var result usageProvenanceCounts
	for field, name := range usageProvenanceFields {
		state := 3
		value, ok := attrs.Get("gen_ai_sketch.usage." + name + ".provenance")
		if ok && value.Type() == pcommon.ValueTypeStr {
			for i, allowed := range usageProvenanceStates {
				if value.Str() == allowed {
					state = i
					break
				}
			}
		}
		observation := observations[field]
		if state < 2 && (!observation.reported || observation.invalid || observation.conflict) {
			state = 3
		}
		result[field][state] = 1
	}
	return result
}

func appendUsageProvenanceSums(scope pmetric.ScopeMetrics, counts usageProvenanceCounts, labels map[string]string, start, timestamp pcommon.Timestamp) {
	for field, name := range usageProvenanceFields {
		for state, source := range usageProvenanceStates {
			if counts[field][state] == 0 {
				continue
			}
			fixed := maps.Clone(labels)
			fixed["token_field"], fixed["source"] = name, source
			appendSum(scope, usageProvenanceMetricName, "Instrumenter-declared origin of emitted input/output counts; unknown is not provider-reported.", "{observation}", counts[field][state], fixed, start, timestamp)
		}
	}
}
