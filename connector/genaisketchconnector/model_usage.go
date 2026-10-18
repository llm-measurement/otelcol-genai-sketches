// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package genaisketchconnector

import (
	"encoding/json"
	"errors"
	"maps"
	"strings"
	"unicode/utf8"

	sketchfi "github.com/llm-measurement/llm-sketchkit/go/sketchkit/frequentitems"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/modelusage"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
)

// ModelUsageConfig maps observed names to operator-approved public model IDs.
// Its absence preserves the original export and accounting fingerprint.
type ModelUsageConfig struct {
	Slice          string            `mapstructure:"slice"`
	Models         map[string]string `mapstructure:"models"`
	Identity       string            `mapstructure:"identity"`
	ResponseModels map[string]string `mapstructure:"response_models"`
}

type compiledModelUsage struct {
	slice          int
	models         map[string]string
	identity       string
	responseModels map[string]string
}

type pendingModelUsage struct {
	id    string
	row   modelusage.Model
	bytes int
}

func modelUsageContract(cfg *Config, accountingID string) (string, error) {
	routing := modelusage.Routing{Algorithm: "inactive-lru-v1", MaxLabelBytes: maxSliceLabelPartBytes, MaxSlices: cfg.MaxSlices, SelectedSlice: cfg.SummaryExport.ModelUsage.Slice, Slices: []modelusage.Slice{}}
	for _, s := range cfg.Slices {
		routing.Slices = append(routing.Slices, modelusage.Slice{Name: s.Name, Keys: s.Keys, FromResourceAttributes: s.FromResourceAttributes})
	}
	m := cfg.SummaryExport.ModelUsage
	if m.Identity == "" {
		return modelusage.ContractID(accountingID, m.Models, routing)
	}
	return servedModelUsageContract(accountingID, m, routing)
}

func compileModelUsage(cfg *Config) (*compiledModelUsage, error) {
	m := cfg.SummaryExport.ModelUsage
	if m == nil {
		return nil, nil
	}
	if cfg.SummaryExport.Directory == "" || m.Models == nil || len(m.Models) > modelusage.MaxModels {
		return nil, errors.New("summary_export.model_usage requires export and a bounded public model map")
	}
	for observed, public := range m.Models {
		if observed == "" || len(observed) > maxSliceLabelPartBytes || !utf8.ValidString(observed) || !modelusage.IsPublicID(public) {
			return nil, errors.New("summary_export.model_usage has an invalid model mapping")
		}
	}
	if err := validateServedModelUsage(m); err != nil {
		return nil, err
	}
	if _, err := modelUsageContract(cfg, "validation"); err != nil {
		return nil, errors.New("summary_export.model_usage requires a model-only slice and valid routing")
	}
	for i, s := range cfg.Slices {
		if s.Name == m.Slice {
			return &compiledModelUsage{slice: i, models: maps.Clone(m.Models), identity: m.Identity, responseModels: maps.Clone(m.ResponseModels)}, nil
		}
	}
	return nil, errors.New("summary_export.model_usage selected slice is missing")
}

func modelUsageBlock(contract string, rows map[string]modelusage.Model) *modelusage.Block {
	return &modelusage.Block{Version: 1, HistogramID: modelusage.HistogramID, ContractID: contract, Models: maps.Clone(rows)}
}

func modelUsageAttempt(t spanTotals) modelusage.Attempt {
	values := [4]uint64{t.inputTokens, t.outputTokens, t.cacheReadInputTokens, t.cacheWriteInputTokens}
	var observations [4]modelusage.Observation
	for i := range observations {
		counts := t.tokenObservations[i]
		o := &observations[i]
		if counts[tokenStateReported] != 0 {
			o.Value = &values[i]
		}
		o.Invalid = counts[tokenStateInvalid] != 0
		o.Conflict = counts[tokenStateConflict] != 0
		o.SubsetViolation = counts[tokenStateSubsetViolation] != 0
	}
	origin := func(i int) string {
		for j, count := range t.usageProvenance[i] {
			if count != 0 {
				return usageProvenanceStates[j]
			}
		}
		return "unknown"
	}
	return modelusage.Attempt{Input: observations[0], Output: observations[1], CacheRead: observations[2], CacheWrite: observations[3], InputOrigin: origin(0), OutputOrigin: origin(1)}
}

func (s *collectorState) modelUsageID(data spanData, pending []pendingSliceUpdate) (string, error) {
	i := s.cfg.modelUsage.slice
	if pending[i].slice.label.overflow {
		return "__overflow__", nil
	}
	if s.cfg.modelUsage.identity != "" {
		return s.cfg.modelUsage.servedModelID(data.spanAttrs), nil
	}
	cfg := s.cfg.slices[i]
	observed, ok := lookupStringInMap(data.spanAttrs, cfg.Keys)
	if !ok && sliceAllowsResourceKey(cfg, cfg.Keys[0]) {
		observed, ok = lookupStringInMap(data.resourceAttrs, cfg.Keys)
	}
	classification := ""
	if !ok || observed == "" {
		classification = "__missing__"
	} else if len(observed) > maxSliceLabelPartBytes {
		classification = "__too_long__"
	}
	return modelusage.ClassifyModel(observed, s.cfg.modelUsage.models, classification)
}

// Prepare both attribution and byte capacity before any live slice, dedup or
// summary mutation. Retained rows are independent of evictable metric slices.
func (s *collectorState) prepareModelUsage(data spanData, update spanUpdate, pending []pendingSliceUpdate) (*pendingModelUsage, error) {
	if s.cfg.modelUsage == nil {
		return nil, nil
	}
	p := pending[len(pending)-1]
	if p.window.summarySuppressed() {
		return nil, nil
	}
	rows := p.window.modelUsage
	modelBytes := p.window.modelUsageBytes
	var result *pendingModelUsage
	if update.request && !p.prepared.deduped {
		id, err := s.modelUsageID(data, pending)
		if err != nil {
			return nil, err
		}
		previous, exists := rows[id]
		if !exists && !modelusage.IsReserved(id) {
			named := 0
			for name := range rows {
				if !modelusage.IsReserved(name) {
					named++
				}
			}
			if named >= s.cfg.maxSlices {
				if s.cfg.modelUsage.identity == "" {
					return nil, errModelUsageCountBudget
				}
				id = "__overflow__"
				previous, exists = rows[id]
			}
		}
		row, err := modelusage.AddAttempt(previous, modelUsageAttempt(update.totals))
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(row)
		if err != nil {
			return nil, err
		}
		modelBytes += len(encoded)
		if exists {
			old, err := json.Marshal(previous)
			if err != nil {
				return nil, err
			}
			modelBytes -= len(old)
		} else {
			key, err := json.Marshal(id)
			if err != nil {
				return nil, err
			}
			modelBytes += len(key) + 1
			if len(rows) != 0 {
				modelBytes++
			}
		}
		result = &pendingModelUsage{id: id, row: row, bytes: modelBytes}
	}
	block := modelUsageBlock("model-usage-v1:"+strings.Repeat("0", 64), map[string]modelusage.Model{})
	preview := cloneSummaryWindow(p.window)
	if p.prepared.deduped {
		preview.counters.dedupSuppressed++
	} else {
		if err := s.applyErrorCheckedSketches(preview, p.prepared); err != nil {
			return nil, err
		}
		s.applyNoErrorSketches(preview, p.prepared)
		preview.counters.addCounters(update, p.prepared)
	}
	counters := preview.counters.export()
	sketches, err := s.summaryPayloads(preview)
	if err != nil {
		return nil, err
	}
	// The bounded 128-byte identities, six int64 timestamps/counters, contract
	// counters and all fixed member names fit this reserved metadata allowance.
	// Actual sketch and model payloads are measured after the staged update.
	encoded, err := json.Marshal(struct {
		Counters   map[string]uint64          `json:"counters"`
		ModelUsage *modelusage.Block          `json:"model_usage"`
		Sketches   map[string]summary.Payload `json:"sketches"`
	}{counters, block, sketches})
	if err != nil {
		return nil, err
	}
	if len(encoded)+modelBytes-2 > summary.MaxBytes-4096 {
		return nil, errModelUsageByteBudget
	}
	return result, nil
}

func cloneSummaryWindow(w *windowState) *windowState {
	c := *w
	if w.counters != nil {
		counters := *w.counters
		c.counters = &counters
	}
	c.distinctUsers, c.distinctPrompts, c.distinctDocs = w.distinctUsers.Clone(), w.distinctPrompts.Clone(), w.distinctDocs.Clone()
	if w.distinctMCPSessions != nil {
		c.distinctMCPSessions = w.distinctMCPSessions.Clone()
	}
	if w.distinctMCPMethods != nil {
		c.distinctMCPMethods = w.distinctMCPMethods.Clone()
	}
	if w.distinctMCPResources != nil {
		c.distinctMCPResources = w.distinctMCPResources.Clone()
	}
	if w.dedupRequests != nil {
		c.dedupRequests = w.dedupRequests.Clone()
	}
	if w.topToolErrors != nil {
		c.topToolErrors = w.topToolErrors.Clone()
	}
	c.topKeys = make([]*sketchfi.Sketch, len(w.topKeys))
	for i, sketch := range w.topKeys {
		c.topKeys[i] = sketch.Clone()
	}
	return &c
}
