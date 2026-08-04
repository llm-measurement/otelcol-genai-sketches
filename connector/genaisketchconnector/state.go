// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay Erramilli and Codex
package genaisketchconnector

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	sketchbloom "github.com/llm-measurement/llm-sketchkit/go/sketchkit/bloom"
	sketchcanon "github.com/llm-measurement/llm-sketchkit/go/sketchkit/canon"
	sketchfi "github.com/llm-measurement/llm-sketchkit/go/sketchkit/frequentitems"
	sketchhash "github.com/llm-measurement/llm-sketchkit/go/sketchkit/hash"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/hllpp"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

const (
	overflowSliceValue = "__overflow__"
	missingSliceValue  = "<missing>"
	tooLongSliceValue  = "<too_long>"
	maxInt64Value      = int64(1<<63 - 1)

	maxAttributeValueBytes = 8 << 10
	maxSliceLabelPartBytes = 256
	maxTopKSnapshotItems   = 10_000

	fieldUserKey   = "user_key"
	fieldPromptKey = "prompt_key"
	fieldDocKey    = "doc_key"

	fieldMCPSessionKey  = "mcp_session_key"
	fieldMCPMethodKey   = "mcp_method_key"
	fieldMCPResourceKey = "mcp_resource_key"
	fieldToolErrorKey   = "tool_error_key"
)

type runtimeConfig struct {
	windowDuration   time.Duration
	retentionWindows int
	maxSlices        int
	topK             int
	hllppProfile     hllpp.Profile
	frequentProfile  sketchfi.Profile
	bloomProfile     sketchbloom.Profile
	slices           []SliceConfig
	fields           map[string]compiledField
	llmOperations    map[string]struct{}
	inputTokenAttrs  []string
	outputTokenAttrs []string
	dedupEnabled     bool
	requestIDAttrs   []string
	mcpEnabled       bool
	toolErrors       bool
}

type compiledField struct {
	name                   string
	fromAttributes         []string
	fromResourceAttributes []string
	canonicalization       sketchcanon.Profile
	domain                 sketchhash.Domain
}

type collectorState struct {
	cfg       runtimeConfig
	secret    sketchhash.Secret
	clock     clock
	startTime pcommon.Timestamp

	slices     map[string]*sliceState
	overflows  map[string]*sliceState
	nextLRUSeq int64
}

type sliceState struct {
	label       sliceLabel
	windows     map[int64]*windowState
	lastSeen    time.Time
	lastWindow  int64
	lruSequence int64

	requests      uint64
	agentRuns     uint64
	inputTokens   uint64
	outputTokens  uint64
	missingTokens uint64
}

type sliceLabel struct {
	name     string
	value    string
	overflow bool
	sortKey  string
}

type windowState struct {
	requests             uint64
	agentRuns            uint64
	inputTokens          uint64
	outputTokens         uint64
	missingTokens        uint64
	distinctUsers        *hllpp.Sketch
	distinctPrompts      *hllpp.Sketch
	distinctDocs         *hllpp.Sketch
	distinctMCPSessions  *hllpp.Sketch
	distinctMCPMethods   *hllpp.Sketch
	distinctMCPResources *hllpp.Sketch
	topPrompts           *sketchfi.Sketch
	topToolErrors        *sketchfi.Sketch
	dedupRequests        *sketchbloom.Sketch
}

type TopKSnapshot struct {
	Surface             string      `json:"surface"`
	GeneratedAtUnixNano int64       `json:"generated_at_unix_nano"`
	TopK                int         `json:"topk"`
	Truncated           bool        `json:"truncated"`
	Slices              []TopKSlice `json:"slices"`
}

type TopKSlice struct {
	SliceName              string     `json:"slice"`
	SliceValue             string     `json:"slice_value"`
	Overflow               bool       `json:"overflow"`
	Field                  string     `json:"field"`
	Mode                   string     `json:"mode"`
	WindowStartUnixNano    int64      `json:"window_start_unix_nano"`
	WindowDurationUnixNano int64      `json:"window_duration_unix_nano"`
	TotalWeight            int64      `json:"total_weight"`
	MaxError               int64      `json:"max_error"`
	Items                  []TopKItem `json:"items"`
}

type TopKItem struct {
	Rank       int    `json:"rank"`
	Hash       string `json:"hash"`
	Estimate   int64  `json:"estimate"`
	LowerBound int64  `json:"lower_bound"`
	UpperBound int64  `json:"upper_bound"`
	Error      int64  `json:"error"`
}

type spanData struct {
	resourceAttrs pcommon.Map
	spanAttrs     pcommon.Map
}

type spanTotals struct {
	requests      uint64
	inputTokens   uint64
	outputTokens  uint64
	missingTokens uint64
}

type spanUpdate struct {
	totals    spanTotals
	request   bool
	agentRun  bool
	mcp       bool
	toolError bool
}

type optionalHash struct {
	value uint64
	ok    bool
}

type preparedUpdate struct {
	dedupHash       optionalHash
	deduped         bool
	userHash        optionalHash
	promptHash      optionalHash
	docHash         optionalHash
	mcpSessionHash  optionalHash
	mcpMethodHash   optionalHash
	mcpResourceHash optionalHash
	topPromptHash   optionalHash
	topPromptWeight int64
	toolErrorHash   optionalHash
}

func newCollectorState(cfg *Config, secret sketchhash.Secret, clk clock, start time.Time) (*collectorState, error) {
	runtimeCfg, err := compileRuntimeConfig(cfg)
	if err != nil {
		return nil, err
	}
	if clk == nil {
		clk = systemClock{}
	}

	return &collectorState{
		cfg:       runtimeCfg,
		secret:    secret,
		clock:     clk,
		startTime: pcommon.NewTimestampFromTime(start),
		slices:    make(map[string]*sliceState),
		overflows: make(map[string]*sliceState),
	}, nil
}

func compileRuntimeConfig(cfg *Config) (runtimeConfig, error) {
	fields := make(map[string]compiledField, len(cfg.Fields))
	for name, field := range cfg.Fields {
		fields[name] = compiledField{
			name:                   name,
			fromAttributes:         append([]string(nil), field.FromAttributes...),
			fromResourceAttributes: append([]string(nil), field.FromResourceAttributes...),
			canonicalization:       sketchcanon.Profile(field.Canonicalization),
			domain:                 sketchhash.Domain(field.Domain),
		}
	}
	llmOperations := make(map[string]struct{}, len(cfg.OperationFilter.LLMOperations))
	for _, operation := range cfg.OperationFilter.LLMOperations {
		llmOperations[operation] = struct{}{}
	}

	runtimeCfg := runtimeConfig{
		windowDuration:   cfg.WindowDuration,
		retentionWindows: cfg.RetentionWindows,
		maxSlices:        cfg.MaxSlices,
		topK:             cfg.TopK,
		hllppProfile:     hllpp.Profile(cfg.Profiles.HLLPP),
		frequentProfile:  sketchfi.Profile(cfg.Profiles.FrequentItems),
		bloomProfile:     sketchbloom.Profile(cfg.Profiles.Bloom),
		slices:           append([]SliceConfig(nil), cfg.Slices...),
		fields:           fields,
		llmOperations:    llmOperations,
		inputTokenAttrs:  append([]string(nil), cfg.Weights.InputTokensFrom...),
		outputTokenAttrs: append([]string(nil), cfg.Weights.OutputTokensFrom...),
		dedupEnabled:     cfg.Dedup.Enabled,
		requestIDAttrs:   append([]string(nil), cfg.Dedup.RequestIDFrom...),
		mcpEnabled:       cfg.MCP.Enabled,
		toolErrors:       cfg.MCP.ToolErrors.Enabled,
	}
	if err := validateRuntimeFields(runtimeCfg); err != nil {
		return runtimeConfig{}, err
	}
	return runtimeCfg, nil
}

func (s *collectorState) ConsumeTraces(_ context.Context, traces ptrace.Traces) (pmetric.Metrics, bool, error) {
	now := s.clock.Now()
	windowStart := s.windowStart(now)
	touched := false

	resourceSpans := traces.ResourceSpans()
	for i := 0; i < resourceSpans.Len(); i++ {
		resourceAttrs := resourceSpans.At(i).Resource().Attributes()
		scopeSpans := resourceSpans.At(i).ScopeSpans()
		for j := 0; j < scopeSpans.Len(); j++ {
			spans := scopeSpans.At(j).Spans()
			for k := 0; k < spans.Len(); k++ {
				span := spans.At(k)
				data := spanData{resourceAttrs: resourceAttrs, spanAttrs: span.Attributes()}
				update, err := s.classifySpan(span, data)
				if err != nil {
					return pmetric.Metrics{}, false, err
				}
				if !update.request && !update.agentRun && !update.mcp && !update.toolError {
					continue
				}
				for _, sliceCfg := range s.cfg.slices {
					slice, err := s.sliceFor(sliceCfg, data, now, windowStart)
					if err != nil {
						return pmetric.Metrics{}, false, err
					}
					if err := slice.update(windowStart, s, data, update); err != nil {
						return pmetric.Metrics{}, false, err
					}
					touched = true
				}
			}
		}
	}

	if !touched {
		return pmetric.Metrics{}, false, nil
	}

	return s.buildMetrics(now), true, nil
}

func (s *collectorState) classifySpan(span ptrace.Span, data spanData) (spanUpdate, error) {
	operation, operationOK := spanAttributeString(span.Attributes(), "gen_ai.operation.name")
	_, llmOperation := s.cfg.llmOperations[operation]
	_, modelFallback := spanAttributeString(span.Attributes(), "gen_ai.request.model")
	request := llmOperation || (!operationOK && modelFallback)

	update := spanUpdate{
		request:   request,
		agentRun:  operation == "invoke_agent" && span.ParentSpanID().IsEmpty(),
		mcp:       s.cfg.mcpEnabled && hasMCPAttributes(data),
		toolError: s.cfg.mcpEnabled && s.cfg.toolErrors && hasToolError(data),
	}
	if request {
		totals, err := tokenTotals(span.Attributes(), s.cfg.inputTokenAttrs, s.cfg.outputTokenAttrs)
		if err != nil {
			return spanUpdate{}, err
		}
		update.totals = totals
	}
	return update, nil
}

func (s *collectorState) sliceFor(sliceCfg SliceConfig, data spanData, now time.Time, windowStart int64) (*sliceState, error) {
	label := sliceLabelFor(sliceCfg, data)
	if existing, ok := s.slices[label.sortKey]; ok {
		s.touch(existing, now, windowStart)
		return existing, nil
	}

	if len(s.slices) < s.cfg.maxSlices {
		return s.createSlice(label, now, windowStart)
	}

	if evicted := s.evictInactive(windowStart); evicted {
		return s.createSlice(label, now, windowStart)
	}

	return s.overflowSlice(sliceCfg.Name, now, windowStart), nil
}

func (s *collectorState) createSlice(label sliceLabel, now time.Time, windowStart int64) (*sliceState, error) {
	state := &sliceState{
		label:   label,
		windows: make(map[int64]*windowState),
	}
	s.touch(state, now, windowStart)
	s.slices[label.sortKey] = state
	return state, nil
}

func (s *collectorState) overflowSlice(sliceName string, now time.Time, windowStart int64) *sliceState {
	if existing, ok := s.overflows[sliceName]; ok {
		s.touch(existing, now, windowStart)
		return existing
	}

	label := sliceLabel{
		name:     sliceName,
		value:    overflowSliceValue,
		overflow: true,
		sortKey:  "overflow:" + sliceName,
	}
	state := &sliceState{
		label:   label,
		windows: make(map[int64]*windowState),
	}
	s.touch(state, now, windowStart)
	s.overflows[sliceName] = state
	return state
}

func (s *collectorState) touch(state *sliceState, now time.Time, windowStart int64) {
	s.nextLRUSeq++
	state.lastSeen = now
	state.lastWindow = windowStart
	state.lruSequence = s.nextLRUSeq
}

func (s *collectorState) evictInactive(currentWindowStart int64) bool {
	protectedWindowStart := currentWindowStart - s.cfg.windowDuration.Nanoseconds()
	var victimKey string
	var victim *sliceState
	for key, candidate := range s.slices {
		if candidate.lastWindow >= protectedWindowStart {
			continue
		}
		if victim == nil ||
			candidate.lastWindow < victim.lastWindow ||
			(candidate.lastWindow == victim.lastWindow && candidate.lruSequence < victim.lruSequence) ||
			(candidate.lastWindow == victim.lastWindow && candidate.lruSequence == victim.lruSequence && key < victimKey) {
			victimKey = key
			victim = candidate
		}
	}
	if victim == nil {
		return false
	}
	delete(s.slices, victimKey)
	return true
}

func (s *collectorState) activeSliceCount() int {
	return len(s.slices)
}

func (s *collectorState) windowStart(now time.Time) int64 {
	duration := s.cfg.windowDuration.Nanoseconds()
	return now.UnixNano() / duration * duration
}

func (s *collectorState) cutoffWindowStart(currentWindowStart int64) int64 {
	retained := int64(s.cfg.retentionWindows - 1)
	return currentWindowStart - retained*s.cfg.windowDuration.Nanoseconds()
}

func (s *sliceState) update(windowStart int64, owner *collectorState, data spanData, update spanUpdate) error {
	window, err := s.window(windowStart, owner)
	if err != nil {
		return err
	}

	prepared, err := owner.prepareUpdate(window, data, update)
	if err != nil || prepared.deduped {
		return err
	}
	if err := s.checkCounters(window, update); err != nil {
		return err
	}
	if err := owner.applyErrorCheckedSketches(window, prepared); err != nil {
		return err
	}
	owner.applyNoErrorSketches(window, prepared)
	s.addCounters(window, update)
	return nil
}

func (s *collectorState) prepareUpdate(window *windowState, data spanData, update spanUpdate) (preparedUpdate, error) {
	var prepared preparedUpdate
	if update.request {
		dedupHash, deduped, err := s.prepareDedup(window, data)
		if err != nil || deduped {
			prepared.deduped = deduped
			return prepared, err
		}
		prepared.dedupHash = dedupHash
		if prepared.userHash, err = s.hashDataField(fieldUserKey, data); err != nil {
			return preparedUpdate{}, err
		}
		if prepared.promptHash, err = s.hashDataField(fieldPromptKey, data); err != nil {
			return preparedUpdate{}, err
		}
		if prepared.docHash, err = s.hashDataField(fieldDocKey, data); err != nil {
			return preparedUpdate{}, err
		}
		if update.totals.missingTokens == 0 && prepared.promptHash.ok {
			weight, ok, err := topKWeight(update.totals)
			if err != nil {
				return preparedUpdate{}, err
			}
			prepared.topPromptHash = optionalHash{value: prepared.promptHash.value, ok: ok}
			prepared.topPromptWeight = weight
		}
	}
	if update.mcp {
		var err error
		if prepared.mcpSessionHash, err = s.hashDataField(fieldMCPSessionKey, data); err != nil {
			return preparedUpdate{}, err
		}
		if prepared.mcpMethodHash, err = s.hashDataField(fieldMCPMethodKey, data); err != nil {
			return preparedUpdate{}, err
		}
		if prepared.mcpResourceHash, err = s.hashDataField(fieldMCPResourceKey, data); err != nil {
			return preparedUpdate{}, err
		}
	}
	if update.toolError {
		hashValue, ok, err := s.hashToolErrorSignature(data)
		if err != nil {
			return preparedUpdate{}, err
		}
		prepared.toolErrorHash = optionalHash{value: hashValue, ok: ok}
	}
	return prepared, nil
}

func (s *collectorState) prepareDedup(window *windowState, data spanData) (optionalHash, bool, error) {
	if !s.cfg.dedupEnabled || window.dedupRequests == nil {
		return optionalHash{}, false, nil
	}
	requestID, ok := lookupString(data, s.cfg.requestIDAttrs)
	if !ok {
		return optionalHash{}, false, nil
	}
	hashValue, err := s.hashCanonical(sketchcanon.TextV1, sketchhash.SessionV1, requestID)
	if err != nil {
		return optionalHash{}, false, err
	}
	return optionalHash{value: hashValue, ok: true}, window.dedupRequests.MayContainHash(hashValue), nil
}

func (s *sliceState) checkCounters(window *windowState, update spanUpdate) error {
	for _, item := range []struct {
		name    string
		current uint64
		delta   uint64
	}{
		{name: "slice.requests", current: s.requests, delta: update.totals.requests},
		{name: "slice.input_tokens", current: s.inputTokens, delta: update.totals.inputTokens},
		{name: "slice.output_tokens", current: s.outputTokens, delta: update.totals.outputTokens},
		{name: "slice.missing_tokens", current: s.missingTokens, delta: update.totals.missingTokens},
		{name: "window.requests", current: window.requests, delta: update.totals.requests},
		{name: "window.input_tokens", current: window.inputTokens, delta: update.totals.inputTokens},
		{name: "window.output_tokens", current: window.outputTokens, delta: update.totals.outputTokens},
		{name: "window.missing_tokens", current: window.missingTokens, delta: update.totals.missingTokens},
	} {
		if _, err := checkedAddUint64(item.name, item.current, item.delta); err != nil {
			return err
		}
	}
	if update.agentRun {
		if _, err := checkedAddUint64("slice.agent_runs", s.agentRuns, 1); err != nil {
			return err
		}
		if _, err := checkedAddUint64("window.agent_runs", window.agentRuns, 1); err != nil {
			return err
		}
	}
	return nil
}

func (s *collectorState) applyErrorCheckedSketches(window *windowState, prepared preparedUpdate) error {
	if prepared.dedupHash.ok {
		if err := window.dedupRequests.AddHash(prepared.dedupHash.value); err != nil {
			return err
		}
	}
	if prepared.topPromptHash.ok {
		if err := window.topPrompts.AddHash(prepared.topPromptHash.value, prepared.topPromptWeight); err != nil {
			return err
		}
	}
	if prepared.toolErrorHash.ok {
		if window.topToolErrors == nil {
			var err error
			window.topToolErrors, err = sketchfi.New(s.cfg.frequentProfile, sketchhash.ToolErrorV1, sketchhash.HMACSHA25664)
			if err != nil {
				return err
			}
		}
		if err := window.topToolErrors.AddHash(prepared.toolErrorHash.value, 1); err != nil {
			return err
		}
	}
	return s.ensureMCPFields(window, prepared)
}

func (s *collectorState) ensureMCPFields(window *windowState, prepared preparedUpdate) error {
	for _, item := range []struct {
		hash   optionalHash
		field  string
		target **hllpp.Sketch
	}{
		{hash: prepared.mcpSessionHash, field: fieldMCPSessionKey, target: &window.distinctMCPSessions},
		{hash: prepared.mcpMethodHash, field: fieldMCPMethodKey, target: &window.distinctMCPMethods},
		{hash: prepared.mcpResourceHash, field: fieldMCPResourceKey, target: &window.distinctMCPResources},
	} {
		if !item.hash.ok || *item.target != nil {
			continue
		}
		field := s.cfg.fields[item.field]
		sketch, err := hllpp.New(s.cfg.hllppProfile, field.domain, sketchhash.HMACSHA25664)
		if err != nil {
			return err
		}
		*item.target = sketch
	}
	return nil
}

func (s *collectorState) applyNoErrorSketches(window *windowState, prepared preparedUpdate) {
	addHash(window.distinctUsers, prepared.userHash)
	addHash(window.distinctPrompts, prepared.promptHash)
	addHash(window.distinctDocs, prepared.docHash)
	addHash(window.distinctMCPSessions, prepared.mcpSessionHash)
	addHash(window.distinctMCPMethods, prepared.mcpMethodHash)
	addHash(window.distinctMCPResources, prepared.mcpResourceHash)
}

func (s *sliceState) addCounters(window *windowState, update spanUpdate) {
	s.requests += update.totals.requests
	s.inputTokens += update.totals.inputTokens
	s.outputTokens += update.totals.outputTokens
	s.missingTokens += update.totals.missingTokens
	window.requests += update.totals.requests
	window.inputTokens += update.totals.inputTokens
	window.outputTokens += update.totals.outputTokens
	window.missingTokens += update.totals.missingTokens
	if update.agentRun {
		s.agentRuns++
		window.agentRuns++
	}
}

func addHash(sketch *hllpp.Sketch, hash optionalHash) {
	if sketch != nil && hash.ok {
		sketch.AddHash(hash.value)
	}
}

func (s *sliceState) window(windowStart int64, owner *collectorState) (*windowState, error) {
	cutoff := owner.cutoffWindowStart(windowStart)
	for start := range s.windows {
		if start < cutoff {
			delete(s.windows, start)
		}
	}

	if existing, ok := s.windows[windowStart]; ok {
		return existing, nil
	}

	userField := owner.cfg.fields[fieldUserKey]
	promptField := owner.cfg.fields[fieldPromptKey]
	docField := owner.cfg.fields[fieldDocKey]

	users, err := hllpp.New(owner.cfg.hllppProfile, userField.domain, sketchhash.HMACSHA25664)
	if err != nil {
		return nil, err
	}
	prompts, err := hllpp.New(owner.cfg.hllppProfile, promptField.domain, sketchhash.HMACSHA25664)
	if err != nil {
		return nil, err
	}
	docs, err := hllpp.New(owner.cfg.hllppProfile, docField.domain, sketchhash.HMACSHA25664)
	if err != nil {
		return nil, err
	}
	topPrompts, err := sketchfi.New(owner.cfg.frequentProfile, promptField.domain, sketchhash.HMACSHA25664)
	if err != nil {
		return nil, err
	}
	var dedupRequests *sketchbloom.Sketch
	if owner.cfg.dedupEnabled {
		dedupRequests, err = sketchbloom.New(owner.cfg.bloomProfile, sketchhash.SessionV1, sketchhash.HMACSHA25664)
		if err != nil {
			return nil, err
		}
	}

	window := &windowState{
		distinctUsers:   users,
		distinctPrompts: prompts,
		distinctDocs:    docs,
		topPrompts:      topPrompts,
		dedupRequests:   dedupRequests,
	}
	s.windows[windowStart] = window
	return window, nil
}

func (s *collectorState) hashToolErrorSignature(data spanData) (uint64, bool, error) {
	tool, toolOK := lookupString(data, []string{"gen_ai.tool.name"})
	errorType, errorOK := lookupString(data, []string{"error.type"})
	if !toolOK || !errorOK || tool == "" || errorType == "" {
		return 0, false, nil
	}
	if len(tool) > maxAttributeValueBytes || len(errorType) > maxAttributeValueBytes {
		return 0, false, fmt.Errorf("tool-error attribute exceeds %d bytes", maxAttributeValueBytes)
	}
	canonicalTool, err := sketchcanon.CanonicalizeString(sketchcanon.TextV1, tool)
	if err != nil {
		return 0, false, fmt.Errorf("canonicalize tool-error tool: %w", err)
	}
	canonicalError, err := sketchcanon.CanonicalizeString(sketchcanon.TextV1, errorType)
	if err != nil {
		return 0, false, fmt.Errorf("canonicalize tool-error error type: %w", err)
	}
	if len(canonicalTool) == 0 || len(canonicalError) == 0 {
		return 0, false, nil
	}
	signature := make([]byte, 0, len(canonicalTool)+1+len(canonicalError))
	signature = append(signature, canonicalTool...)
	signature = append(signature, 0)
	signature = append(signature, canonicalError...)
	hashValue, err := sketchhash.Hash64(s.secret, sketchhash.ToolErrorV1, signature)
	if err != nil {
		return 0, false, fmt.Errorf("hash %s: %w", sketchhash.ToolErrorV1, err)
	}
	return hashValue, true, nil
}

func (s *collectorState) hashDataField(fieldName string, data spanData) (optionalHash, error) {
	field, ok := s.cfg.fields[fieldName]
	if !ok {
		return optionalHash{}, nil
	}
	value, ok := lookupStringSources(data, field.fromAttributes, field.fromResourceAttributes)
	if !ok || value == "" {
		return optionalHash{}, nil
	}
	if len(value) > maxAttributeValueBytes {
		return optionalHash{}, fmt.Errorf("fields.%s value exceeds %d bytes", fieldName, maxAttributeValueBytes)
	}
	hashValue, err := s.hashField(field, value)
	if err != nil {
		return optionalHash{}, err
	}
	return optionalHash{value: hashValue, ok: true}, nil
}

func (s *collectorState) hashField(field compiledField, value string) (uint64, error) {
	return s.hashCanonical(field.canonicalization, field.domain, value)
}

func (s *collectorState) hashCanonical(profile sketchcanon.Profile, domain sketchhash.Domain, value string) (uint64, error) {
	if len(value) > maxAttributeValueBytes {
		return 0, fmt.Errorf("hash input for %s exceeds %d bytes", domain, maxAttributeValueBytes)
	}
	canonical, err := sketchcanon.CanonicalizeString(profile, value)
	if err != nil {
		return 0, fmt.Errorf("canonicalize %s: %w", domain, err)
	}

	hashValue, err := sketchhash.Hash64(s.secret, domain, canonical)
	if err != nil {
		return 0, fmt.Errorf("hash %s: %w", domain, err)
	}
	return hashValue, nil
}

func topKWeight(totals spanTotals) (int64, bool, error) {
	weight, err := checkedTokenSum(totals.inputTokens, totals.outputTokens)
	if err != nil {
		return 0, false, err
	}
	if weight == 0 {
		return 0, false, nil
	}
	return int64(weight), true, nil
}

func tokenTotals(spanAttrs pcommon.Map, inputAttrs []string, outputAttrs []string) (spanTotals, error) {
	input, inputOK, err := lookupUintFromMap(spanAttrs, inputAttrs)
	if err != nil {
		return spanTotals{}, err
	}
	output, outputOK, err := lookupUintFromMap(spanAttrs, outputAttrs)
	if err != nil {
		return spanTotals{}, err
	}
	if inputOK && outputOK {
		if _, err := checkedTokenSum(input, output); err != nil {
			return spanTotals{}, err
		}
	}

	totals := spanTotals{requests: 1}
	if inputOK {
		totals.inputTokens = input
	}
	if outputOK {
		totals.outputTokens = output
	}
	if !inputOK || !outputOK {
		totals.missingTokens = 1
	}
	return totals, nil
}

func sliceLabelFor(sliceCfg SliceConfig, data spanData) sliceLabel {
	parts := make([]string, 0, len(sliceCfg.Keys))
	for _, key := range sliceCfg.Keys {
		value, ok := lookupStringInMap(data.spanAttrs, []string{key})
		if !ok && sliceAllowsResourceKey(sliceCfg, key) {
			value, ok = lookupStringInMap(data.resourceAttrs, []string{key})
		}
		if !ok || value == "" {
			value = missingSliceValue
		} else if len(value) > maxSliceLabelPartBytes {
			value = tooLongSliceValue
		}
		parts = append(parts, key+"="+value)
	}

	value := strings.Join(parts, "|")
	return sliceLabel{
		name:    sliceCfg.Name,
		value:   value,
		sortKey: sliceCfg.Name + "\x00" + value,
	}
}

func sliceAllowsResourceKey(sliceCfg SliceConfig, key string) bool {
	if len(sliceCfg.FromResourceAttributes) == 0 {
		return true
	}
	for _, resourceKey := range sliceCfg.FromResourceAttributes {
		if resourceKey == key {
			return true
		}
	}
	return false
}

func lookupString(data spanData, keys []string) (string, bool) {
	return lookupStringSources(data, keys, keys)
}

func lookupStringSources(data spanData, spanKeys []string, resourceKeys []string) (string, bool) {
	if value, ok := lookupStringInMap(data.spanAttrs, spanKeys); ok {
		return value, true
	}
	return lookupStringInMap(data.resourceAttrs, resourceKeys)
}

func lookupStringInMap(attributes pcommon.Map, keys []string) (string, bool) {
	for _, key := range keys {
		if value, ok := attributes.Get(key); ok {
			return value.AsString(), true
		}
	}
	return "", false
}

func lookupUintFromMap(attributes pcommon.Map, keys []string) (uint64, bool, error) {
	for _, key := range keys {
		if value, ok := attributes.Get(key); ok {
			parsed, ok, err := valueAsUint(value)
			if err != nil {
				return 0, false, fmt.Errorf("attribute %q: %w", key, err)
			}
			if ok {
				return parsed, true, nil
			}
		}
	}
	return 0, false, nil
}

func spanAttributeString(attributes pcommon.Map, key string) (string, bool) {
	value, ok := attributes.Get(key)
	if !ok || value.Type() != pcommon.ValueTypeStr || value.Str() == "" {
		return "", false
	}
	return value.Str(), true
}

func hasMCPAttributes(data spanData) bool {
	for _, attributes := range []pcommon.Map{data.spanAttrs, data.resourceAttrs} {
		found := false
		attributes.Range(func(key string, _ pcommon.Value) bool {
			if strings.HasPrefix(key, "mcp.") {
				found = true
				return false
			}
			return true
		})
		if found {
			return true
		}
	}
	return false
}

func hasToolError(data spanData) bool {
	tool, toolOK := lookupString(data, []string{"gen_ai.tool.name"})
	errorType, errorOK := lookupString(data, []string{"error.type"})
	return toolOK && errorOK && tool != "" && errorType != ""
}

func valueAsUint(value pcommon.Value) (uint64, bool, error) {
	switch value.Type() {
	case pcommon.ValueTypeInt:
		if value.Int() < 0 {
			return 0, false, nil
		}
		return uint64(value.Int()), true, nil
	case pcommon.ValueTypeDouble:
		d := value.Double()
		if d < 0 || math.Trunc(d) != d || d > float64(maxInt64Value) {
			return 0, false, nil
		}
		return uint64(d), true, nil
	case pcommon.ValueTypeStr:
		parsed, err := strconv.ParseUint(value.Str(), 10, 64)
		if err != nil {
			return 0, false, nil
		}
		if parsed > uint64(maxInt64Value) {
			return 0, false, fmt.Errorf("token value exceeds %d", maxInt64Value)
		}
		return parsed, true, nil
	default:
		return 0, false, nil
	}
}

func (s *collectorState) buildMetrics(now time.Time) pmetric.Metrics {
	metrics := pmetric.NewMetrics()
	resourceMetrics := metrics.ResourceMetrics().AppendEmpty()
	scopeMetrics := resourceMetrics.ScopeMetrics().AppendEmpty()
	scopeMetrics.Scope().SetName(scopeName)

	timestamp := pcommon.NewTimestampFromTime(now)
	appendGauge(scopeMetrics, activeSlicesMetricName, "Number of active non-overflow GenAI sketch slices.", "{slice}", float64(s.activeSliceCount()), nil, timestamp)

	currentWindow := s.windowStart(now)
	for _, slice := range s.metricSlices() {
		labels := slice.metricLabels()
		appendSum(scopeMetrics, requestsMetricName, "Cumulative count of spans matching the configured GenAI LLM operation filter.", "{request}", slice.requests, labels, s.startTime, timestamp)
		appendSum(scopeMetrics, agentRunsMetricName, "Cumulative count of root invoke_agent operations.", "{run}", slice.agentRuns, labels, s.startTime, timestamp)
		appendSum(scopeMetrics, inputTokensMetricName, "Cumulative input tokens observed on operation-filter matches.", "{token}", slice.inputTokens, labels, s.startTime, timestamp)
		appendSum(scopeMetrics, outputTokensMetricName, "Cumulative output tokens observed on operation-filter matches.", "{token}", slice.outputTokens, labels, s.startTime, timestamp)
		totalTokens, ok := addUint64(slice.inputTokens, slice.outputTokens)
		if !ok {
			totalTokens = math.MaxUint64
		}
		appendSum(scopeMetrics, totalTokensMetricName, "Cumulative input plus output tokens observed on operation-filter matches.", "{token}", totalTokens, labels, s.startTime, timestamp)
		appendSum(scopeMetrics, missingTokenUsageMetricName, "Cumulative operation-filter matches missing configured token usage attributes.", "{request}", slice.missingTokens, labels, s.startTime, timestamp)

		window := slice.windows[currentWindow]
		if window == nil {
			continue
		}
		appendGauge(scopeMetrics, distinctUsersMetricName, "Estimated distinct users in the current window.", "{user}", window.distinctUsers.Estimate(), labels, timestamp)
		appendGauge(scopeMetrics, distinctPromptsMetricName, "Estimated distinct prompt signatures in the current window.", "{prompt}", window.distinctPrompts.Estimate(), labels, timestamp)
		appendGauge(scopeMetrics, distinctDocsMetricName, "Estimated distinct retrieval documents in the current window.", "{document}", window.distinctDocs.Estimate(), labels, timestamp)
		if window.distinctMCPSessions != nil {
			appendGauge(scopeMetrics, distinctMCPSessionsMetricName, "Estimated distinct keyed MCP sessions in the current window.", "{session}", window.distinctMCPSessions.Estimate(), labels, timestamp)
		}
		if window.distinctMCPMethods != nil {
			appendGauge(scopeMetrics, distinctMCPMethodsMetricName, "Estimated distinct keyed MCP methods in the current window.", "{method}", window.distinctMCPMethods.Estimate(), labels, timestamp)
		}
		if window.distinctMCPResources != nil {
			appendGauge(scopeMetrics, distinctMCPResourcesMetricName, "Estimated distinct keyed MCP resource URIs in the current window.", "{resource}", window.distinctMCPResources.Estimate(), labels, timestamp)
		}
	}

	return metrics
}

func (s *collectorState) metricSlices() []*sliceState {
	slices := make([]*sliceState, 0, len(s.slices)+len(s.overflows))
	for _, slice := range s.slices {
		slices = append(slices, slice)
	}
	for _, slice := range s.overflows {
		slices = append(slices, slice)
	}

	sort.Slice(slices, func(i int, j int) bool {
		return slices[i].label.sortKey < slices[j].label.sortKey
	})
	return slices
}

func (s *collectorState) TopKSnapshot(now time.Time) (TopKSnapshot, error) {
	currentWindow := s.windowStart(now)
	snapshot := TopKSnapshot{
		Surface:             "genaisketch_topk",
		GeneratedAtUnixNano: now.UnixNano(),
		TopK:                s.cfg.topK,
	}
	for _, slice := range s.metricSlices() {
		window := slice.windows[currentWindow]
		if window == nil {
			continue
		}
		if err := s.appendFrequentItemsSnapshot(&snapshot, slice, currentWindow, fieldPromptKey, window.topPrompts); err != nil {
			return TopKSnapshot{}, err
		}
		if err := s.appendFrequentItemsSnapshot(&snapshot, slice, currentWindow, fieldToolErrorKey, window.topToolErrors); err != nil {
			return TopKSnapshot{}, err
		}
	}
	return snapshot, nil
}

func (s *collectorState) appendFrequentItemsSnapshot(snapshot *TopKSnapshot, slice *sliceState, currentWindow int64, field string, sketch *sketchfi.Sketch) error {
	if sketch == nil {
		return nil
	}
	remaining := maxTopKSnapshotItems - snapshot.ItemCount()
	if remaining <= 0 {
		snapshot.Truncated = true
		return nil
	}
	items, err := sketch.FrequentItems(sketchfi.NoFalseNegatives)
	if err != nil {
		return err
	}
	if len(items) > s.cfg.topK {
		items = items[:s.cfg.topK]
	}
	if len(items) > remaining {
		items = items[:remaining]
		snapshot.Truncated = true
	}
	topSlice := TopKSlice{
		SliceName:              slice.label.name,
		SliceValue:             slice.label.value,
		Overflow:               slice.label.overflow,
		Field:                  field,
		Mode:                   "no_false_negatives",
		WindowStartUnixNano:    currentWindow,
		WindowDurationUnixNano: s.cfg.windowDuration.Nanoseconds(),
		TotalWeight:            sketch.TotalWeight(),
		MaxError:               sketch.MaxError(),
		Items:                  make([]TopKItem, 0, len(items)),
	}
	for i, item := range items {
		topSlice.Items = append(topSlice.Items, TopKItem{
			Rank:       i + 1,
			Hash:       fmt.Sprintf("%016x", item.Hash),
			Estimate:   item.Estimate,
			LowerBound: item.LowerBound,
			UpperBound: item.UpperBound,
			Error:      item.Error,
		})
	}
	snapshot.Slices = append(snapshot.Slices, topSlice)
	return nil
}

func (s TopKSnapshot) ItemCount() int {
	count := 0
	for _, slice := range s.Slices {
		count += len(slice.Items)
	}
	return count
}

func (s *sliceState) metricLabels() map[string]string {
	return map[string]string{
		"slice":       s.label.name,
		"slice_value": s.label.value,
		"overflow":    strconv.FormatBool(s.label.overflow),
	}
}

func appendSum(scopeMetrics pmetric.ScopeMetrics, name string, description string, unit string, value uint64, labels map[string]string, start pcommon.Timestamp, timestamp pcommon.Timestamp) {
	metric := scopeMetrics.Metrics().AppendEmpty()
	metric.SetName(name)
	metric.SetDescription(description)
	metric.SetUnit(unit)

	sum := metric.SetEmptySum()
	sum.SetAggregationTemporality(pmetric.AggregationTemporalityCumulative)
	sum.SetIsMonotonic(true)

	point := sum.DataPoints().AppendEmpty()
	point.SetStartTimestamp(start)
	point.SetTimestamp(timestamp)
	point.SetIntValue(saturatingInt64(value))
	setLabels(point.Attributes(), labels)
}

func appendGauge(scopeMetrics pmetric.ScopeMetrics, name string, description string, unit string, value float64, labels map[string]string, timestamp pcommon.Timestamp) {
	metric := scopeMetrics.Metrics().AppendEmpty()
	metric.SetName(name)
	metric.SetDescription(description)
	metric.SetUnit(unit)

	point := metric.SetEmptyGauge().DataPoints().AppendEmpty()
	point.SetTimestamp(timestamp)
	point.SetDoubleValue(value)
	setLabels(point.Attributes(), labels)
}

func setLabels(attrs pcommon.Map, labels map[string]string) {
	for key, value := range labels {
		attrs.PutStr(key, value)
	}
}

func saturatingInt64(value uint64) int64 {
	if value > uint64(maxInt64Value) {
		return maxInt64Value
	}
	return int64(value)
}

func checkedTokenSum(input uint64, output uint64) (uint64, error) {
	if input > uint64(maxInt64Value) || output > uint64(maxInt64Value) {
		return 0, fmt.Errorf("token value exceeds %d", maxInt64Value)
	}
	total, ok := addUint64(input, output)
	if !ok || total > uint64(maxInt64Value) {
		return 0, fmt.Errorf("top-k token weight overflows int64")
	}
	return total, nil
}

func checkedAddUint64(name string, current uint64, delta uint64) (uint64, error) {
	total, ok := addUint64(current, delta)
	if !ok {
		return 0, fmt.Errorf("%s overflows uint64", name)
	}
	return total, nil
}

func addUint64(left uint64, right uint64) (uint64, bool) {
	if left > math.MaxUint64-right {
		return 0, false
	}
	return left + right, true
}

func requiredField(cfg runtimeConfig, name string) error {
	if _, ok := cfg.fields[name]; ok {
		return nil
	}
	return fmt.Errorf("fields.%s must be configured", name)
}

func requiredFieldDomain(cfg runtimeConfig, name string, domain sketchhash.Domain) error {
	field, ok := cfg.fields[name]
	if !ok {
		return fmt.Errorf("fields.%s must be configured when mcp.enabled is true", name)
	}
	if field.domain != domain {
		return fmt.Errorf("fields.%s.domain must be %s when mcp.enabled is true, got %s", name, domain, field.domain)
	}
	return nil
}

func validateRuntimeFields(cfg runtimeConfig) error {
	errs := []error{
		requiredField(cfg, fieldUserKey),
		requiredField(cfg, fieldPromptKey),
		requiredField(cfg, fieldDocKey),
	}
	if cfg.mcpEnabled {
		errs = append(errs,
			requiredFieldDomain(cfg, fieldMCPSessionKey, sketchhash.MCPSessionV1),
			requiredFieldDomain(cfg, fieldMCPMethodKey, sketchhash.MCPMethodV1),
			requiredFieldDomain(cfg, fieldMCPResourceKey, sketchhash.RetrievalDocV1),
		)
	}
	return errors.Join(errs...)
}
