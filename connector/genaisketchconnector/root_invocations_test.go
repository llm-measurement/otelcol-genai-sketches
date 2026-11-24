// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package genaisketchconnector

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestRootInvocationsConfiguration(t *testing.T) {
	if defaultConfig().RootInvocations.Enabled {
		t.Fatal("root invocations must be opt-in")
	}
	if got := defaultConfig().RootInvocations.Operations; !reflect.DeepEqual(got, []string{"invoke_agent", "invoke_workflow"}) {
		t.Fatalf("default operations: %v", got)
	}
	compiled, err := compileRootInvocations(RootInvocationsConfig{Enabled: true})
	if err != nil || len(compiled) != 2 {
		t.Fatalf("omitted operations did not select defaults: %v, %v", compiled, err)
	}
	operations := make([]string, maxRootInvocationOperations)
	for i := range operations {
		operations[i] = fmt.Sprintf("invoke_entry_%d", i)
	}
	operations[0] = "invoke_" + strings.Repeat("a", maxRootInvocationOperationBytes-len("invoke_"))
	cfg := defaultConfig()
	cfg.RootInvocations = RootInvocationsConfig{Enabled: true, Operations: operations}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("maximum valid configuration rejected: %v", err)
	}
	runtime, err := compileRuntimeConfig(cfg)
	if err != nil || len(runtime.rootInvocationOperations) != maxRootInvocationOperations {
		t.Fatalf("compiled maximum operation count: %v", err)
	}
	operations[0] = "invoke_changed"
	if _, exists := runtime.rootInvocationOperations[operations[0]]; exists {
		t.Fatal("compiled operation set aliases configuration")
	}
	cfg.RootInvocations = RootInvocationsConfig{}
	runtime, err = compileRuntimeConfig(cfg)
	if err != nil || runtime.rootInvocationOperations != nil {
		t.Fatal("disabled feature allocated or enabled its runtime map", err)
	}
}

func TestRootInvocationsRejectInvalidConfiguration(t *testing.T) {
	tooMany := make([]string, maxRootInvocationOperations+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("invoke_%d", i)
	}
	cases := map[string][]string{
		"empty": {}, "too_many": tooMany,
		"duplicate": {"invoke_agent", "invoke_agent"},
		"blank":     {""}, "prefix_only": {"invoke_"}, "wrong_prefix": {"chat"},
		"too_long":  {"invoke_" + strings.Repeat("a", 58)},
		"uppercase": {"invoke_Agent"}, "unicode": {"invoke_\u00e9"},
		"wildcard": {"invoke_*"}, "client_suffix": {"invoke_agent.client"},
		"whitespace": {"invoke_agent "}, "newline": {"invoke_agent\n"},
		"control": {"invoke_agent\x00"}, "separator": {"invoke_agent-name"},
	}
	for name, operations := range cases {
		t.Run(name, func(t *testing.T) {
			for _, enabled := range []bool{false, true} {
				cfg := defaultConfig()
				cfg.RootInvocations = RootInvocationsConfig{Enabled: enabled, Operations: operations}
				if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "root_invocations.operations") {
					t.Fatalf("enabled=%t accepted invalid configuration: %v", enabled, err)
				}
				if _, err := compileRuntimeConfig(cfg); err == nil {
					t.Fatal("runtime compilation bypassed root validation")
				}
			}
		})
	}
}

func TestRootInvocationsSpanRule(t *testing.T) {
	cases := []struct {
		name, operation     string
		kind                ptrace.SpanKind
		parent              bool
		wantRoot, wantAgent bool
	}{
		{"agent", "invoke_agent", ptrace.SpanKindInternal, false, true, true},
		{"workflow", "invoke_workflow", ptrace.SpanKindInternal, false, true, false},
		{"runner", "invoke_runner", ptrace.SpanKindInternal, false, true, false},
		{"unconfigured", "invoke_other", ptrace.SpanKindInternal, false, false, false},
		{"remote_client", "invoke_agent", ptrace.SpanKindClient, false, false, true},
		{"workflow_client", "invoke_workflow", ptrace.SpanKindClient, false, false, false},
		{"server", "invoke_agent", ptrace.SpanKindServer, false, false, true},
		{"producer", "invoke_workflow", ptrace.SpanKindProducer, false, false, false},
		{"consumer", "invoke_workflow", ptrace.SpanKindConsumer, false, false, false},
		{"unspecified", "invoke_agent", ptrace.SpanKindUnspecified, false, false, true},
		{"http_parent", "invoke_workflow", ptrace.SpanKindInternal, true, false, false},
		{"nested_agent", "invoke_agent", ptrace.SpanKindInternal, true, false, false},
		{"chat", "chat", ptrace.SpanKindInternal, false, false, false},
		{"missing_operation", "", ptrace.SpanKindInternal, false, false, false},
	}
	state := newTestState(t, rootInvocationTestConfig())
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			trace := rootInvocationTrace(tc.operation, tc.kind, tc.parent, "one")
			span := trace.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
			update, err := state.classifySpan(span, spanData{spanAttrs: span.Attributes()})
			if err != nil || update.rootInvocation != tc.wantRoot || update.agentRun != tc.wantAgent {
				t.Fatalf("root=%t agent=%t err=%v", update.rootInvocation, update.agentRun, err)
			}
		})
	}
}

func TestRootInvocationsEntrypointTree(t *testing.T) {
	for _, operation := range []string{"invoke_runner", "invoke_workflow", "invoke_agent"} {
		t.Run(operation, func(t *testing.T) {
			state := newTestState(t, rootInvocationTestConfig())
			trace := rootInvocationTrace(operation, ptrace.SpanKindInternal, false, "one")
			child := rootInvocationTrace("invoke_agent", ptrace.SpanKindInternal, true, "one")
			childSpan := child.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
			childSpan.SetSpanID(pcommon.SpanID{3})
			childSpan.SetParentSpanID(pcommon.SpanID{2})
			child.ResourceSpans().At(0).CopyTo(trace.ResourceSpans().AppendEmpty())
			chat := tracesFromSpans(testSpan{Team: "one", Model: "model", InputTokens: ptr(11), OutputTokens: ptr(3)})
			span := chat.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
			span.SetSpanID(pcommon.SpanID{4})
			span.SetParentSpanID(pcommon.SpanID{3})
			span.Attributes().PutStr("gen_ai.operation.name", "chat")
			chat.ResourceSpans().At(0).CopyTo(trace.ResourceSpans().AppendEmpty())
			metrics := mustConsumeTraces(t, state, trace)
			if sumMetricValues(metrics, rootInvocationsMetricName) != 1 || sumMetricValues(metrics, requestsMetricName) != 1 || sumMetricValues(metrics, inputTokensMetricName) != 11 {
				t.Fatal("entrypoint or aggregate usage was double counted")
			}
			wantAgent := int64(0)
			if operation == "invoke_agent" {
				wantAgent = 1
			}
			if sumMetricValues(metrics, agentRunsMetricName) != wantAgent {
				t.Fatal("legacy agent run definition changed")
			}
		})
	}
}

func TestRootInvocationsRemotePairCombinedAndSplit(t *testing.T) {
	for _, includeRoot := range []bool{false, true} {
		caller := rootInvocationTrace("invoke_agent", ptrace.SpanKindClient, includeRoot, "one")
		remote := rootInvocationTrace("invoke_agent", ptrace.SpanKindInternal, true, "one")
		remoteSpan := remote.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
		remoteSpan.SetSpanID(pcommon.SpanID{3})
		remoteSpan.SetParentSpanID(pcommon.SpanID{2})
		combined := ptrace.NewTraces()
		if includeRoot {
			root := rootInvocationTrace("invoke_workflow", ptrace.SpanKindInternal, false, "one")
			root.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).SetSpanID(pcommon.SpanID{1})
			root.ResourceSpans().At(0).CopyTo(caller.ResourceSpans().AppendEmpty())
		}
		caller.ResourceSpans().CopyTo(combined.ResourceSpans())
		remote.ResourceSpans().At(0).CopyTo(combined.ResourceSpans().AppendEmpty())
		all := newTestState(t, rootInvocationTestConfig())
		left, right := newTestState(t, rootInvocationTestConfig()), newTestState(t, rootInvocationTestConfig())
		allMetrics, _, err := all.ConsumeTraces(context.Background(), combined)
		if err != nil {
			t.Fatal(err)
		}
		leftMetrics, _, err := left.ConsumeTraces(context.Background(), caller)
		if err != nil {
			t.Fatal(err)
		}
		_, relevant, err := right.ConsumeTraces(context.Background(), remote)
		if err != nil || relevant {
			t.Fatal("remote child was counted independently", err)
		}
		want := int64(0)
		if includeRoot {
			want = 1
		}
		if sumMetricValues(allMetrics, rootInvocationsMetricName) != want || sumMetricValues(leftMetrics, rootInvocationsMetricName)+sumMetricValues(right.buildMetrics(right.clock.Now()), rootInvocationsMetricName) != want {
			t.Fatal("remote CLIENT/INTERNAL pair double counted, including split collectors")
		}
	}
}

func TestRootInvocationsHTTPParentIsNotRoot(t *testing.T) {
	state := newTestState(t, rootInvocationTestConfig())
	trace := rootInvocationTrace("invoke_workflow", ptrace.SpanKindInternal, true, "one")
	http := trace.ResourceSpans().At(0).ScopeSpans().At(0).Spans().AppendEmpty()
	http.SetKind(ptrace.SpanKindServer)
	http.SetName("HTTP POST")
	http.SetSpanID(pcommon.SpanID{1})
	http.Attributes().PutStr("http.request.method", "POST")
	_, relevant, err := state.ConsumeTraces(context.Background(), trace)
	if err != nil || relevant || len(state.slices) != 0 {
		t.Fatal("HTTP child was promoted to a root", err)
	}
}

func TestRootInvocationsBoundedOverflow(t *testing.T) {
	cfg := rootInvocationTestConfig()
	cfg.MaxSlices = 1
	state := newTestState(t, cfg)
	for _, team := range []string{"one", "two", "three"} {
		mustConsumeTraces(t, state, rootInvocationTrace("invoke_workflow", ptrace.SpanKindInternal, false, team))
	}
	if len(state.slices) != 1 || len(state.overflows) != 1 || state.overflows["team"].rootInvocations != 2 {
		t.Fatal("root counts escaped existing overflow routing")
	}
	metrics := state.buildMetrics(state.clock.Now())
	if sumMetricValues(metrics, rootInvocationsMetricName) != 3 {
		t.Fatal("overflow lost counts")
	}
	labels := map[string]string{"slice": "team", "slice_value": overflowSliceValue, "overflow": "true"}
	if intMetricValue(t, metrics, rootInvocationsMetricName, labels) != 2 {
		t.Fatal("overflow root count used a new label dimension")
	}
}

func TestRootInvocationsDeterministicEviction(t *testing.T) {
	for range 10 {
		cfg := rootInvocationTestConfig()
		cfg.MaxSlices, cfg.WindowDuration = 2, time.Second
		clk := &fixedClock{now: time.Unix(120, 0)}
		state := newTestStateWithClock(t, clk, cfg)
		for _, team := range []string{"one", "two"} {
			mustConsumeTraces(t, state, rootInvocationTrace("invoke_workflow", ptrace.SpanKindInternal, false, team))
		}
		for _, slice := range state.slices {
			slice.lruSequence = 7
		}
		clk.now = clk.now.Add(3 * time.Second)
		mustConsumeTraces(t, state, rootInvocationTrace("invoke_workflow", ptrace.SpanKindInternal, false, "three"))
		if state.slices["team\x00team.id=one"] != nil || len(state.slices) != 2 || len(state.overflows) != 0 || state.slices["team\x00team.id=three"].rootInvocations != 1 {
			t.Fatal("root routing changed deterministic eviction")
		}
	}
}

func TestRootInvocationsCounterSaturation(t *testing.T) {
	c := accountingCounters{rootInvocations: math.MaxInt64 - 1}
	u := spanUpdate{rootInvocation: true}
	if err := c.checkCounters(u, preparedUpdate{}, math.MaxInt64); err != nil {
		t.Fatal(err)
	}
	c.addCounters(u, preparedUpdate{})
	if c.rootInvocations != math.MaxInt64 || c.saturated {
		t.Fatal("exact boundary must fit")
	}
	if c.checkCounters(u, preparedUpdate{}, math.MaxInt64) == nil {
		t.Fatal("extra root counter missing from capacity guard")
	}
	c.addCounters(u, preparedUpdate{})
	if c.rootInvocations != math.MaxInt64 || !c.saturated {
		t.Fatal("root counter overflow was not saturated")
	}
	if err := c.checkCounters(spanUpdate{}, preparedUpdate{}, math.MaxInt64); err != nil {
		t.Fatal("unrelated update refused", err)
	}
}

func TestRootInvocationsWindowSaturationAndCopy(t *testing.T) {
	state, _, clk := modelFixture(t, func(c *Config) {
		c.RootInvocations.Enabled = true
		c.SummaryExport.ModelUsage = nil
	})
	trace := rootInvocationTrace("invoke_agent", ptrace.SpanKindInternal, false, "one")
	mustConsumeTraces(t, state, trace)
	start := state.windowStart(clk.now)
	key := state.metricSlices()[0].label.sortKey
	state.slices[key].rootInvocations = math.MaxInt64 - 1
	state.summary.windows[start].counters.rootInvocations = math.MaxInt64 - 1
	oldSlice, oldWindow := state.slices[key], state.summary.windows[start]
	metrics := mustConsumeTraces(t, state, trace)
	if sumMetricValues(metrics, rootInvocationsMetricName) != math.MaxInt64 || state.summary.windows[start].summarySuppressed() {
		t.Fatal("exact root boundary suppressed")
	}
	if oldSlice.rootInvocations != math.MaxInt64-1 || oldWindow.counters.rootInvocations != math.MaxInt64-1 {
		t.Fatal("atomic copy aliased root counters")
	}
	metrics = mustConsumeTraces(t, state, trace)
	if !state.slices[key].saturated || !state.summary.windows[start].saturation[saturationCounter] || sumMetricValues(metrics, rootInvocationsMetricName) != math.MaxInt64 {
		t.Fatal("root saturation did not mark lifetime and window counters")
	}
	if state.summary.windows[start].counters.agentRuns != 2 || state.slices[key].agentRuns != 3 {
		t.Fatal("joint agent/root update partially applied to the saturated summary")
	}
	clk.now = clk.now.Add(state.cfg.windowDuration)
	mustConsumeTraces(t, state, trace)
	window := state.summary.windows[state.windowStart(clk.now)]
	if window.summarySuppressed() || window.counters.rootInvocations != 1 || window.counters.agentRuns != 1 {
		t.Fatal("lifetime root saturation poisoned a fresh window")
	}
}

func TestRootInvocationsAtomicBatchFailure(t *testing.T) {
	state := newTestState(t, rootInvocationTestConfig())
	trace := rootInvocationTrace("invoke_workflow", ptrace.SpanKindInternal, false, "one")
	mustConsumeTraces(t, state, trace)
	before := state.metricSlices()[0]
	field := state.cfg.fields[fieldUserKey]
	field.canonicalization = "invalid-test-profile"
	state.cfg.fields[fieldUserKey] = field
	chat := tracesFromSpans(testSpan{Model: "model", Team: "one", User: "private-user", InputTokens: ptr(1), OutputTokens: ptr(1)})
	chat.ResourceSpans().At(0).CopyTo(trace.ResourceSpans().AppendEmpty())
	_, _, err := state.ConsumeTraces(context.Background(), trace)
	if err == nil {
		t.Fatal("positive control did not fail after root prefix")
	}
	if state.metricSlices()[0] != before || before.rootInvocations != 1 || before.requests != 0 {
		t.Fatal("failed batch leaked a root counter prefix")
	}
}

func TestRootInvocationsPrivacyAndDefaultOff(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		cfg := rootInvocationTestConfig()
		cfg.RootInvocations.Enabled = enabled
		state := newTestState(t, cfg)
		trace := rootInvocationTrace("invoke_agent", ptrace.SpanKindInternal, false, "one")
		span := trace.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
		span.SetName("PRIVATE_SPAN_NAME")
		for _, key := range []string{"gen_ai.agent.id", "gen_ai.agent.name", "enduser.id", "session.id", "gen_ai.conversation.id", "gen_ai.request.prompt", "gen_ai.input.messages", "request.id"} {
			span.Attributes().PutStr(key, "PRIVATE_ROOT_SENTINEL")
		}
		chat := tracesFromSpans(testSpan{Model: "model", Team: "one", User: "PRIVATE_USER", Prompt: "PRIVATE_PROMPT", InputTokens: ptr(1), OutputTokens: ptr(1)})
		chat.ResourceSpans().At(0).CopyTo(trace.ResourceSpans().AppendEmpty())
		metrics := mustConsumeTraces(t, state, trace)
		snapshot, err := state.TopKSnapshot(state.clock.Now())
		if err != nil {
			t.Fatal(err)
		}
		core, logs := observer.New(zap.InfoLevel)
		conn := &tracesConnector{cfg: cfg, state: state, logger: zap.New(core)}
		conn.emitTopKSnapshotLocked(state.clock.Now())
		for _, surface := range [][]byte{marshalMetrics(t, metrics), mustJSON(t, snapshot), mustJSON(t, logs.All())} {
			if bytes.Contains(surface, []byte("PRIVATE_")) {
				t.Fatal("root content or identity leaked")
			}
			if !enabled && bytes.Contains(surface, []byte("root_invocations")) {
				t.Fatal("disabled feature changed an export surface")
			}
		}
		if enabled && sumMetricValues(metrics, rootInvocationsMetricName) != 1 {
			t.Fatal("root was not counted")
		}
		if !enabled && state.metricSlices()[0].rootInvocations != 0 {
			t.Fatal("default-off feature changed counters")
		}
		metricSlice := metrics.ResourceMetrics().At(0).ScopeMetrics().At(0).Metrics()
		for i := 0; i < metricSlice.Len(); i++ {
			metric := metricSlice.At(i)
			if metric.Name() != rootInvocationsMetricName {
				continue
			}
			for j := 0; j < metric.Sum().DataPoints().Len(); j++ {
				if metric.Sum().DataPoints().At(j).Attributes().Len() != 3 {
					t.Fatal("new root metric added labels")
				}
			}
		}
	}
}

func rootInvocationTestConfig() *Config {
	return testConfig(func(c *Config) {
		c.RootInvocations = RootInvocationsConfig{Enabled: true, Operations: []string{"invoke_agent", "invoke_workflow", "invoke_runner"}}
		c.Slices = []SliceConfig{{Name: "team", Keys: []string{"team.id"}}}
	})
}

func rootInvocationTrace(operation string, kind ptrace.SpanKind, parent bool, team string) ptrace.Traces {
	trace := ptrace.NewTraces()
	span := trace.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	span.SetTraceID(pcommon.TraceID{1})
	span.SetSpanID(pcommon.SpanID{2})
	span.SetKind(kind)
	span.SetName(operation)
	if parent {
		span.SetParentSpanID(pcommon.SpanID{1})
	}
	if operation != "" {
		span.Attributes().PutStr("gen_ai.operation.name", operation)
	}
	span.Attributes().PutStr("team.id", team)
	return trace
}
