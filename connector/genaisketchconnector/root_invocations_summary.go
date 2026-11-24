// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package genaisketchconnector

import (
	"maps"
	"slices"
)

const (
	rootInvocationSummaryCount = "root_invocations.v1.count"
	rootInvocationSummaryRule  = "root_invocations.v1.rule.root_internal_parentless"
	rootInvocationOperation    = "root_invocations.v1.operation."
)

func rootInvocationSummaryDefinition(cfg *Config) ([]string, error) {
	operations, err := compileRootInvocations(cfg.RootInvocations)
	if err != nil {
		return nil, err
	}
	return slices.Sorted(maps.Keys(operations)), nil
}

func (e *summaryExporter) configureOptionalMeasurements(cfg *Config) error {
	var err error
	e.rootOperations, err = rootInvocationSummaryDefinition(cfg)
	if err != nil {
		return err
	}
	if cfg.SummaryExport.ModelUsage != nil {
		e.modelUsageContract, err = modelUsageContract(cfg, e.accountingID)
	}
	return err
}

// Zero-valued definition keys compose the existing optional measurement
// contract: summary.Compatible rejects a different operation set or rule.
func addRootInvocationSummary(counters map[string]uint64, operations []string, count uint64) {
	if len(operations) == 0 {
		return
	}
	counters[rootInvocationSummaryCount] = count
	counters[rootInvocationSummaryRule] = 0
	for _, operation := range operations {
		counters[rootInvocationOperation+operation] = 0
	}
}
