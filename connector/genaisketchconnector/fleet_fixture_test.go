// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay Erramilli and Codex
package genaisketchconnector

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	sketchhash "github.com/llm-measurement/llm-sketchkit/go/sketchkit/hash"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

const fleetVectorSecret = "fleet-domain-vector-secret"

type fleetFixture struct {
	Name       string             `json:"name"`
	Sentinel   string             `json:"sentinel"`
	Placements []fixturePlacement `json:"placements"`
	Resources  []fixtureResource  `json:"resources"`
	Expected   fixtureExpected    `json:"expected"`
}

type fixturePlacement struct {
	Name      string            `json:"name"`
	Resources []fixtureResource `json:"resources"`
}

type fixtureResource struct {
	Attributes map[string]any `json:"attributes"`
	Spans      []fixtureSpan  `json:"spans"`
}

type fixtureSpan struct {
	ID         string         `json:"id"`
	ParentID   string         `json:"parent_id"`
	Name       string         `json:"name"`
	Attributes map[string]any `json:"attributes"`
}

type fixtureExpected struct {
	RequestCount      int64 `json:"request_count"`
	MissingTokenCount int64 `json:"missing_token_count"`
	AgentRunCount     int64 `json:"agent_run_count"`
}

func TestLocalityMatrixSpanAndResourcePlacementsAreEquivalent(t *testing.T) {
	fixture := readFleetFixture(t, "locality_matrix.json")
	if len(fixture.Placements) != 3 {
		t.Fatalf("locality placements = %d, want 3", len(fixture.Placements))
	}

	states := make(map[string]*collectorState, len(fixture.Placements))
	metrics := make(map[string]pmetric.Metrics, len(fixture.Placements))
	for _, placement := range fixture.Placements {
		state := newFleetFixtureState(t, fleetFixtureConfig(false, false))
		states[placement.Name] = state
		metrics[placement.Name] = mustConsumeTraces(t, state, tracesFromFixtureResources(t, placement.Resources))
	}

	spanState := stateFingerprint(t, states["span_attributes"])
	resourceState := stateFingerprint(t, states["resource_attributes"])
	if !bytes.Equal(spanState, resourceState) {
		t.Fatalf("span/resource sketch state differs\nspan: %s\nresource: %s", spanState, resourceState)
	}
	if got, want := metricFingerprint(t, metrics["span_attributes"]), metricFingerprint(t, metrics["resource_attributes"]); !bytes.Equal(got, want) {
		t.Fatalf("span/resource metrics differ\nspan: %s\nresource: %s", got, want)
	}

	requestLabels := map[string]string{
		"slice":       "by_team_model",
		"slice_value": "team.id=team-alpha|gen_ai.request.model=model-a",
		"overflow":    "false",
	}
	if got := intMetricValue(t, metrics["resource_attributes"], requestsMetricName, requestLabels); got != 1 {
		t.Fatalf("resource-placement requests = %d, want 1", got)
	}

	rootOnlyLabels := map[string]string{
		"slice":       "by_team_model",
		"slice_value": "team.id=<missing>|gen_ai.request.model=model-a",
		"overflow":    "false",
	}
	if got := intMetricValue(t, metrics["root_span_only"], requestsMetricName, rootOnlyLabels); got != 1 {
		t.Fatalf("root-only child routing requests = %d, want 1", got)
	}
	if bytes.Equal(spanState, stateFingerprint(t, states["root_span_only"])) {
		t.Fatal("root-only placement unexpectedly matched stateless span/resource placement")
	}
}

func TestResourceExtractionUsesSpanPrecedenceAcrossAllAliases(t *testing.T) {
	state := newFleetFixtureState(t, fleetFixtureConfig(false, false))
	data := spanData{
		spanAttrs:     attrs(map[string]any{"user.id": "span-user"}),
		resourceAttrs: attrs(map[string]any{"enduser.id": "resource-user"}),
	}

	got, err := state.hashDataField(fieldUserKey, data)
	if err != nil || !got.ok {
		t.Fatalf("hashDataField: hash=%016x ok=%t err=%v", got.value, got.ok, err)
	}
	want, err := state.hashField(state.cfg.fields[fieldUserKey], "span-user")
	if err != nil {
		t.Fatalf("hash span winner: %v", err)
	}
	if got.value != want {
		t.Fatalf("resource alias won over span alias: got=%016x want=%016x", got.value, want)
	}
}

func TestToolErrorSignatureCanonicalizesTupleComponents(t *testing.T) {
	state := newFleetFixtureState(t, fleetFixtureConfig(true, true))
	data := spanData{
		spanAttrs:     attrs(map[string]any{"gen_ai.tool.name": "  weather.lookup\r\n", "error.type": "\tTimeoutError  "}),
		resourceAttrs: pcommon.NewMap(),
	}
	got, ok, err := state.hashToolErrorSignature(data)
	if err != nil || !ok {
		t.Fatalf("hashToolErrorSignature: hash=%016x ok=%t err=%v", got, ok, err)
	}
	if got != 0xff7ba5c72a1c3ea0 {
		t.Fatalf("tool-error tuple hash = %016x, want ff7ba5c72a1c3ea0", got)
	}
}

func TestDeepTreeMetricSemantics(t *testing.T) {
	fixture := readFleetFixture(t, "deep_tree.json")
	if depth := maxFixtureDepth(t, fixture.Resources); depth < 5 || depth > 8 {
		t.Fatalf("deep-tree fixture depth = %d, want 5..8", depth)
	}
	state := newFleetFixtureState(t, fleetFixtureConfig(false, false))
	metrics := mustConsumeTraces(t, state, tracesFromFixtureResources(t, fixture.Resources))

	if got := sumMetricValues(metrics, requestsMetricName); got != fixture.Expected.RequestCount {
		t.Fatalf("tree requests = %d, want %d", got, fixture.Expected.RequestCount)
	}
	if got := sumMetricValues(metrics, missingTokenUsageMetricName); got != fixture.Expected.MissingTokenCount {
		t.Fatalf("tree missing-token count = %d, want %d", got, fixture.Expected.MissingTokenCount)
	}
	if got := sumMetricValues(metrics, agentRunsMetricName); got != fixture.Expected.AgentRunCount {
		t.Fatalf("tree root agent runs = %d, want %d", got, fixture.Expected.AgentRunCount)
	}
}

func TestMCPFixtureHashesRoutesAndNeverExportsResourceURI(t *testing.T) {
	fixture := readFleetFixture(t, "mcp_tree.json")
	cfg := fleetFixtureConfig(true, true)
	cfg.Slices = []SliceConfig{
		{Name: "by_mcp_method", Keys: []string{"mcp.method.name"}},
		{Name: "by_tool", Keys: []string{"gen_ai.tool.name"}},
	}
	state := newFleetFixtureState(t, cfg)
	traces := tracesFromFixtureResources(t, fixture.Resources)
	metrics := mustConsumeTraces(t, state, traces)
	snapshot, err := state.TopKSnapshot(state.clock.Now())
	if err != nil {
		t.Fatalf("TopKSnapshot: %v", err)
	}

	for _, surface := range [][]byte{marshalMetrics(t, metrics), mustJSON(t, snapshot)} {
		if bytes.Contains(surface, []byte(fixture.Sentinel)) {
			t.Fatalf("mcp.resource.uri sentinel leaked to exported surface: %s", surface)
		}
	}
	assertMetricLabelsExclude(t, metrics, fixture.Sentinel)
	if !metricHasLabels(metrics, distinctMCPMethodsMetricName, map[string]string{
		"slice":       "by_mcp_method",
		"slice_value": "mcp.method.name=tools/call",
		"overflow":    "false",
	}) {
		t.Fatal("mcp.method.name did not route its own MCP metric slice")
	}

	methodData := findFixtureSpanData(t, traces, "mcp.method.name", "tools/call")
	assertFieldHash(t, state, fieldMCPMethodKey, methodData, 0x981d0bc5a27fa555)
	assertFieldHash(t, state, fieldMCPSessionKey, methodData, 0xe794d71ad6dd93fc)
	resourceData := findFixtureSpanData(t, traces, "mcp.resource.uri", fixture.Sentinel)
	assertFieldHash(t, state, fieldMCPResourceKey, resourceData, 0xea13644640bcd671)

	var sawMethodSlice bool
	var sawToolSlice bool
	var sawToolError bool
	for _, slice := range snapshot.Slices {
		sawMethodSlice = sawMethodSlice || (slice.SliceName == "by_mcp_method" && slice.SliceValue == "mcp.method.name=<missing>")
		sawToolSlice = sawToolSlice || (slice.SliceName == "by_tool" && slice.SliceValue == "gen_ai.tool.name=weather.lookup")
		if slice.Field != fieldToolErrorKey {
			continue
		}
		for _, item := range slice.Items {
			if item.Hash != "ff7ba5c72a1c3ea0" {
				continue
			}
			sawToolError = true
			if item.LowerBound > 1 || item.UpperBound < 1 || item.LowerBound > item.Estimate || item.Estimate > item.UpperBound {
				t.Fatalf("tool-error bounds do not bracket truth: %#v", item)
			}
		}
	}
	if !sawMethodSlice || !sawToolSlice {
		t.Fatalf("MCP/tool slice routing missing: method=%t tool=%t snapshot=%#v", sawMethodSlice, sawToolSlice, snapshot)
	}
	if !sawToolError {
		t.Fatalf("tool-error signature missing from snapshot: %#v", snapshot)
	}
	for _, forbidden := range []string{"ff7ba5c72a1c3ea0", fixture.Sentinel, "session-42", "request-private-17"} {
		assertMetricLabelsExclude(t, metrics, forbidden)
	}
}

func TestOrphanedMCPSpanUsesOnlyItsOwnAndResourceAttributes(t *testing.T) {
	fixture := readFleetFixture(t, "orphan_mcp.json")
	cfg := fleetFixtureConfig(true, true)
	cfg.Slices = []SliceConfig{
		{Name: "by_team", Keys: []string{"team.id"}},
		{Name: "by_mcp_method", Keys: []string{"mcp.method.name"}},
	}
	state := newFleetFixtureState(t, cfg)
	metrics := mustConsumeTraces(t, state, tracesFromFixtureResources(t, fixture.Resources))

	if got := sumMetricValues(metrics, requestsMetricName); got != 0 {
		t.Fatalf("orphan MCP request count = %d, want 0", got)
	}
	if got := sumMetricValues(metrics, missingTokenUsageMetricName); got != 0 {
		t.Fatalf("orphan MCP missing-token count = %d, want 0", got)
	}
	snapshot, err := state.TopKSnapshot(state.clock.Now())
	if err != nil {
		t.Fatalf("TopKSnapshot: %v", err)
	}
	if !snapshotHasSlice(snapshot, "by_team", "team.id=team-orphan") || !snapshotHasSlice(snapshot, "by_mcp_method", "mcp.method.name=tools/call") {
		t.Fatalf("orphan MCP routing did not use own/resource attributes: %#v", snapshot)
	}
}

func TestNoMCPCorpusMatchesProfileAbsent(t *testing.T) {
	fixture := readFleetFixture(t, "no_mcp.json")
	traces := tracesFromFixtureResources(t, fixture.Resources)

	absent := newFleetFixtureState(t, fleetFixtureConfig(false, false))
	enabled := newFleetFixtureState(t, fleetFixtureConfig(true, false))
	absentMetrics := mustConsumeTraces(t, absent, traces)
	enabledMetrics := mustConsumeTraces(t, enabled, traces)

	if got, want := metricFingerprint(t, enabledMetrics), metricFingerprint(t, absentMetrics); !bytes.Equal(got, want) {
		t.Fatalf("no-MCP metrics changed when MCP profile enabled\nenabled: %s\nabsent: %s", got, want)
	}
	if got, want := stateFingerprint(t, enabled), stateFingerprint(t, absent); !bytes.Equal(got, want) {
		t.Fatalf("no-MCP sketch state changed when MCP profile enabled\nenabled: %s\nabsent: %s", got, want)
	}
}

func fleetFixtureConfig(mcpEnabled bool, toolErrors bool) *Config {
	cfg := defaultConfig()
	cfg.Slices = []SliceConfig{{
		Name:                   "by_team_model",
		Keys:                   []string{"team.id", "gen_ai.request.model"},
		FromResourceAttributes: []string{"team.id", "gen_ai.request.model"},
	}}
	for name, field := range cfg.Fields {
		field.FromResourceAttributes = append([]string(nil), field.FromAttributes...)
		cfg.Fields[name] = field
	}
	cfg.MCP.Enabled = mcpEnabled
	cfg.MCP.ToolErrors.Enabled = toolErrors
	if mcpEnabled {
		cfg.Fields[fieldMCPSessionKey] = FieldConfig{
			FromAttributes:         []string{"mcp.session.id"},
			FromResourceAttributes: []string{"mcp.session.id"},
			Canonicalization:       "text_v1",
			Domain:                 "mcp-session:v1",
		}
		cfg.Fields[fieldMCPMethodKey] = FieldConfig{
			FromAttributes:         []string{"mcp.method.name"},
			FromResourceAttributes: []string{"mcp.method.name"},
			Canonicalization:       "text_v1",
			Domain:                 "mcp-method:v1",
		}
		cfg.Fields[fieldMCPResourceKey] = FieldConfig{
			FromAttributes:         []string{"mcp.resource.uri"},
			FromResourceAttributes: []string{"mcp.resource.uri"},
			Canonicalization:       "text_v1",
			Domain:                 "retrieval-doc:v1",
		}
	}
	if err := cfg.Validate(); err != nil {
		panic(err)
	}
	return cfg
}

func newFleetFixtureState(t *testing.T, cfg *Config) *collectorState {
	t.Helper()
	t.Setenv("GENAI_SKETCH_SECRET", fleetVectorSecret)
	secret, err := sketchhash.SecretFromEnv("GENAI_SKETCH_SECRET")
	if err != nil {
		t.Fatalf("SecretFromEnv: %v", err)
	}
	clk := &fixedClock{now: time.Unix(5_000, 0)}
	state, err := newCollectorState(cfg, secret, clk)
	if err != nil {
		t.Fatalf("newCollectorState: %v", err)
	}
	return state
}

func readFleetFixture(t *testing.T, name string) fleetFixture {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "fleet", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	var fixture fleetFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("decode fixture %s: %v", name, err)
	}
	return fixture
}

func tracesFromFixtureResources(t *testing.T, resources []fixtureResource) ptrace.Traces {
	t.Helper()
	traces := ptrace.NewTraces()
	traceID := pcommon.TraceID{0xf1, 0x01}
	for _, resourceSpec := range resources {
		resource := traces.ResourceSpans().AppendEmpty()
		putFixtureAttributes(t, resource.Resource().Attributes(), resourceSpec.Attributes)
		spans := resource.ScopeSpans().AppendEmpty().Spans()
		for _, spanSpec := range resourceSpec.Spans {
			span := spans.AppendEmpty()
			span.SetName(spanSpec.Name)
			span.SetTraceID(traceID)
			span.SetSpanID(parseFixtureSpanID(t, spanSpec.ID))
			if spanSpec.ParentID != "" {
				span.SetParentSpanID(parseFixtureSpanID(t, spanSpec.ParentID))
			}
			putFixtureAttributes(t, span.Attributes(), spanSpec.Attributes)
		}
	}
	return traces
}

func maxFixtureDepth(t *testing.T, resources []fixtureResource) int {
	t.Helper()
	parents := make(map[string]string)
	for _, resource := range resources {
		for _, span := range resource.Spans {
			parents[span.ID] = span.ParentID
		}
	}
	maxDepth := 0
	for id := range parents {
		depth := 0
		seen := make(map[string]struct{})
		for id != "" {
			if _, ok := seen[id]; ok {
				t.Fatalf("fixture contains a parent cycle at span %s", id)
			}
			seen[id] = struct{}{}
			depth++
			parent, ok := parents[id]
			if !ok {
				t.Fatalf("fixture references missing parent span %s", id)
			}
			id = parent
		}
		if depth > maxDepth {
			maxDepth = depth
		}
	}
	return maxDepth
}

func parseFixtureSpanID(t *testing.T, value string) pcommon.SpanID {
	t.Helper()
	raw, err := hex.DecodeString(value)
	if err != nil || len(raw) > len(pcommon.SpanID{}) {
		t.Fatalf("invalid fixture span id %q", value)
	}
	var id pcommon.SpanID
	copy(id[len(id)-len(raw):], raw)
	return id
}

func putFixtureAttributes(t *testing.T, destination pcommon.Map, values map[string]any) {
	t.Helper()
	for key, value := range values {
		switch typed := value.(type) {
		case string:
			destination.PutStr(key, typed)
		case float64:
			destination.PutInt(key, int64(typed))
		case bool:
			destination.PutBool(key, typed)
		default:
			t.Fatalf("unsupported fixture attribute %s=%T", key, value)
		}
	}
}

func mustConsumeTraces(t *testing.T, state *collectorState, traces ptrace.Traces) pmetric.Metrics {
	t.Helper()
	metrics, ok, err := state.ConsumeTraces(context.Background(), traces)
	if err != nil {
		t.Fatalf("ConsumeTraces: %v", err)
	}
	if !ok {
		t.Fatal("fixture produced no metrics")
	}
	return metrics
}

func findFixtureSpanData(t *testing.T, traces ptrace.Traces, key string, want string) spanData {
	t.Helper()
	resources := traces.ResourceSpans()
	for i := 0; i < resources.Len(); i++ {
		resourceAttrs := resources.At(i).Resource().Attributes()
		scopes := resources.At(i).ScopeSpans()
		for j := 0; j < scopes.Len(); j++ {
			spans := scopes.At(j).Spans()
			for k := 0; k < spans.Len(); k++ {
				spanAttrs := spans.At(k).Attributes()
				if value, ok := spanAttrs.Get(key); ok && value.AsString() == want {
					return spanData{resourceAttrs: resourceAttrs, spanAttrs: spanAttrs}
				}
			}
		}
	}
	t.Fatalf("fixture span %s=%q not found", key, want)
	return spanData{}
}

func assertFieldHash(t *testing.T, state *collectorState, field string, data spanData, want uint64) {
	t.Helper()
	got, err := state.hashDataField(field, data)
	if err != nil || !got.ok {
		t.Fatalf("hash %s: hash=%016x ok=%t err=%v", field, got.value, got.ok, err)
	}
	if got.value != want {
		t.Fatalf("hash %s = %016x, want %016x", field, got.value, want)
	}
}

func assertMetricLabelsExclude(t *testing.T, metrics pmetric.Metrics, forbidden string) {
	t.Helper()
	resourceMetrics := metrics.ResourceMetrics()
	for i := 0; i < resourceMetrics.Len(); i++ {
		scopeMetrics := resourceMetrics.At(i).ScopeMetrics()
		for j := 0; j < scopeMetrics.Len(); j++ {
			metricSlice := scopeMetrics.At(j).Metrics()
			for k := 0; k < metricSlice.Len(); k++ {
				metric := metricSlice.At(k)
				if strings.Contains(metric.Name(), forbidden) || strings.Contains(metric.Description(), forbidden) {
					t.Fatalf("forbidden value %q appeared in metric metadata", forbidden)
				}
				switch metric.Type() {
				case pmetric.MetricTypeSum:
					points := metric.Sum().DataPoints()
					for l := 0; l < points.Len(); l++ {
						assertAttributesExclude(t, points.At(l).Attributes(), forbidden)
					}
				case pmetric.MetricTypeGauge:
					points := metric.Gauge().DataPoints()
					for l := 0; l < points.Len(); l++ {
						assertAttributesExclude(t, points.At(l).Attributes(), forbidden)
					}
				}
			}
		}
	}
}

func metricHasLabels(metrics pmetric.Metrics, metricName string, labels map[string]string) bool {
	resourceMetrics := metrics.ResourceMetrics()
	for i := 0; i < resourceMetrics.Len(); i++ {
		scopeMetrics := resourceMetrics.At(i).ScopeMetrics()
		for j := 0; j < scopeMetrics.Len(); j++ {
			metricSlice := scopeMetrics.At(j).Metrics()
			for k := 0; k < metricSlice.Len(); k++ {
				metric := metricSlice.At(k)
				if metric.Name() != metricName {
					continue
				}
				if metric.Type() == pmetric.MetricTypeGauge {
					points := metric.Gauge().DataPoints()
					for l := 0; l < points.Len(); l++ {
						if pointHasLabels(points.At(l).Attributes(), labels) {
							return true
						}
					}
				}
			}
		}
	}
	return false
}

func sumMetricValues(metrics pmetric.Metrics, metricName string) int64 {
	var total int64
	resourceMetrics := metrics.ResourceMetrics()
	for i := 0; i < resourceMetrics.Len(); i++ {
		scopeMetrics := resourceMetrics.At(i).ScopeMetrics()
		for j := 0; j < scopeMetrics.Len(); j++ {
			metricSlice := scopeMetrics.At(j).Metrics()
			for k := 0; k < metricSlice.Len(); k++ {
				metric := metricSlice.At(k)
				if metric.Name() != metricName || metric.Type() != pmetric.MetricTypeSum {
					continue
				}
				points := metric.Sum().DataPoints()
				for l := 0; l < points.Len(); l++ {
					total += points.At(l).IntValue()
				}
			}
		}
	}
	return total
}

func assertAttributesExclude(t *testing.T, attributes pcommon.Map, forbidden string) {
	t.Helper()
	attributes.Range(func(key string, value pcommon.Value) bool {
		if strings.Contains(key, forbidden) || strings.Contains(value.AsString(), forbidden) {
			t.Fatalf("forbidden value %q appeared in metric label %s=%q", forbidden, key, value.AsString())
		}
		return true
	})
}

func snapshotHasSlice(snapshot TopKSnapshot, name string, value string) bool {
	for _, slice := range snapshot.Slices {
		if slice.SliceName == name && slice.SliceValue == value && len(slice.Items) > 0 {
			return true
		}
	}
	return false
}

func marshalMetrics(t *testing.T, metrics pmetric.Metrics) []byte {
	t.Helper()
	data, err := (&pmetric.JSONMarshaler{}).MarshalMetrics(metrics)
	if err != nil {
		t.Fatalf("marshal metrics: %v", err)
	}
	return data
}

type metricFingerprintPoint struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Labels      []string `json:"labels"`
	IntValue    int64    `json:"int_value,omitempty"`
	DoubleValue float64  `json:"double_value,omitempty"`
}

func metricFingerprint(t *testing.T, metrics pmetric.Metrics) []byte {
	t.Helper()
	var points []metricFingerprintPoint
	resourceMetrics := metrics.ResourceMetrics()
	for i := 0; i < resourceMetrics.Len(); i++ {
		scopeMetrics := resourceMetrics.At(i).ScopeMetrics()
		for j := 0; j < scopeMetrics.Len(); j++ {
			metricSlice := scopeMetrics.At(j).Metrics()
			for k := 0; k < metricSlice.Len(); k++ {
				metric := metricSlice.At(k)
				switch metric.Type() {
				case pmetric.MetricTypeSum:
					dataPoints := metric.Sum().DataPoints()
					for l := 0; l < dataPoints.Len(); l++ {
						point := dataPoints.At(l)
						points = append(points, metricFingerprintPoint{
							Name:     metric.Name(),
							Type:     "sum",
							Labels:   sortedAttributes(point.Attributes()),
							IntValue: point.IntValue(),
						})
					}
				case pmetric.MetricTypeGauge:
					dataPoints := metric.Gauge().DataPoints()
					for l := 0; l < dataPoints.Len(); l++ {
						point := dataPoints.At(l)
						points = append(points, metricFingerprintPoint{
							Name:        metric.Name(),
							Type:        "gauge",
							Labels:      sortedAttributes(point.Attributes()),
							DoubleValue: point.DoubleValue(),
						})
					}
				}
			}
		}
	}
	sort.Slice(points, func(i, j int) bool {
		left := points[i].Name + "\x00" + strings.Join(points[i].Labels, "\x00")
		right := points[j].Name + "\x00" + strings.Join(points[j].Labels, "\x00")
		return left < right
	})
	return mustJSON(t, points)
}

func sortedAttributes(attributes pcommon.Map) []string {
	labels := make([]string, 0, attributes.Len())
	attributes.Range(func(key string, value pcommon.Value) bool {
		labels = append(labels, key+"="+value.AsString())
		return true
	})
	sort.Strings(labels)
	return labels
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}
	return data
}

type stateFingerprintSlice struct {
	SliceName             string                   `json:"slice"`
	SliceValue            string                   `json:"slice_value"`
	Overflow              bool                     `json:"overflow"`
	Requests              uint64                   `json:"requests"`
	AgentRuns             uint64                   `json:"agent_runs"`
	InputTokens           uint64                   `json:"input_tokens"`
	OutputTokens          uint64                   `json:"output_tokens"`
	CacheReadInputTokens  uint64                   `json:"cache_read_input_tokens"`
	CacheWriteInputTokens uint64                   `json:"cache_write_input_tokens"`
	ReasoningOutputTokens uint64                   `json:"reasoning_output_tokens"`
	MissingTokens         uint64                   `json:"missing_tokens"`
	TokenObservations     tokenObservationCounts   `json:"token_observations"`
	DedupSuppressed       uint64                   `json:"dedup_suppressed"`
	DedupKeyMissing       uint64                   `json:"dedup_key_missing"`
	Windows               []stateFingerprintWindow `json:"windows"`
}

type stateFingerprintWindow struct {
	Start                int64  `json:"start"`
	DistinctUsers        string `json:"distinct_users"`
	DistinctPrompts      string `json:"distinct_prompts"`
	DistinctDocs         string `json:"distinct_docs"`
	DistinctMCPSessions  string `json:"distinct_mcp_sessions,omitempty"`
	DistinctMCPMethods   string `json:"distinct_mcp_methods,omitempty"`
	DistinctMCPResources string `json:"distinct_mcp_resources,omitempty"`
	TopPrompts           string `json:"top_prompts"`
	TopToolErrors        string `json:"top_tool_errors,omitempty"`
}

func stateFingerprint(t *testing.T, state *collectorState) []byte {
	t.Helper()
	all := state.metricSlices()
	out := make([]stateFingerprintSlice, 0, len(all))
	for _, slice := range all {
		fingerprint := stateFingerprintSlice{
			SliceName:             slice.label.name,
			SliceValue:            slice.label.value,
			Overflow:              slice.label.overflow,
			Requests:              slice.requests,
			AgentRuns:             slice.agentRuns,
			InputTokens:           slice.inputTokens,
			OutputTokens:          slice.outputTokens,
			CacheReadInputTokens:  slice.cacheReadInputTokens,
			CacheWriteInputTokens: slice.cacheWriteInputTokens,
			ReasoningOutputTokens: slice.reasoningOutputTokens,
			MissingTokens:         slice.missingTokens,
			TokenObservations:     slice.tokenObservations,
			DedupSuppressed:       slice.dedupSuppressed,
			DedupKeyMissing:       slice.dedupKeyMissing,
		}
		starts := make([]int64, 0, len(slice.windows))
		for start := range slice.windows {
			starts = append(starts, start)
		}
		sort.Slice(starts, func(i, j int) bool { return starts[i] < starts[j] })
		for _, start := range starts {
			window := slice.windows[start]
			fingerprint.Windows = append(fingerprint.Windows, stateFingerprintWindow{
				Start:                start,
				DistinctUsers:        marshalSketchHex(t, window.distinctUsers),
				DistinctPrompts:      marshalSketchHex(t, window.distinctPrompts),
				DistinctDocs:         marshalSketchHex(t, window.distinctDocs),
				DistinctMCPSessions:  marshalSketchHex(t, window.distinctMCPSessions),
				DistinctMCPMethods:   marshalSketchHex(t, window.distinctMCPMethods),
				DistinctMCPResources: marshalSketchHex(t, window.distinctMCPResources),
				TopPrompts:           marshalSketchHex(t, window.topPrompts),
				TopToolErrors:        marshalSketchHex(t, window.topToolErrors),
			})
		}
		out = append(out, fingerprint)
	}
	return mustJSON(t, out)
}

type binaryMarshaler interface {
	MarshalBinary() ([]byte, error)
}

func marshalSketchHex(t *testing.T, sketch binaryMarshaler) string {
	t.Helper()
	if sketch == nil || reflect.ValueOf(sketch).IsNil() {
		return ""
	}
	data, err := sketch.MarshalBinary()
	if err != nil {
		t.Fatalf("marshal sketch: %v", err)
	}
	return fmt.Sprintf("%x", data)
}
