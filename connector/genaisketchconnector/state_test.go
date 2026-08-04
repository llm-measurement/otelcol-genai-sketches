// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay Erramilli and Codex
package genaisketchconnector

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	sketchhash "github.com/llm-measurement/llm-sketchkit/go/sketchkit/hash"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func TestSliceLabelUsesConfiguredAttributes(t *testing.T) {
	data := spanData{
		resourceAttrs: attrs(map[string]any{"team.id": "platform"}),
		spanAttrs:     attrs(map[string]any{"gen_ai.request.model": "gpt-test"}),
	}

	label := sliceLabelFor(SliceConfig{Name: "by_team_model", Keys: []string{"team.id", "gen_ai.request.model"}}, data)

	if label.value != "team.id=platform|gen_ai.request.model=gpt-test" {
		t.Fatalf("slice value = %q", label.value)
	}
}

func TestMissingTokenBehaviorDoesNotInventWeights(t *testing.T) {
	state := newTestState(t, testConfig(func(cfg *Config) {
		cfg.Slices = []SliceConfig{{Name: "by_model", Keys: []string{"gen_ai.request.model"}}}
	}))

	metrics, ok, err := state.ConsumeTraces(context.Background(), tracesFromSpans(
		testSpan{
			Model:  "gpt-test",
			User:   "user-1",
			Prompt: "prompt-1",
			Doc:    "doc-1",
		},
	))
	if err != nil {
		t.Fatalf("ConsumeTraces: %v", err)
	}
	if !ok {
		t.Fatal("expected metrics")
	}

	labels := map[string]string{"slice": "by_model", "slice_value": "gen_ai.request.model=gpt-test", "overflow": "false"}
	if got := intMetricValue(t, metrics, requestsMetricName, labels); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}
	if got := intMetricValue(t, metrics, inputTokensMetricName, labels); got != 0 {
		t.Fatalf("input tokens = %d, want 0", got)
	}
	if got := intMetricValue(t, metrics, outputTokensMetricName, labels); got != 0 {
		t.Fatalf("output tokens = %d, want 0", got)
	}
	if got := intMetricValue(t, metrics, missingTokenUsageMetricName, labels); got != 1 {
		t.Fatalf("missing token usage = %d, want 1", got)
	}
}

func TestTokenOverflowRejectedBeforeStateMutation(t *testing.T) {
	state := newTestState(t, testConfig(func(cfg *Config) {
		cfg.Slices = []SliceConfig{{Name: "by_model", Keys: []string{"gen_ai.request.model"}}}
	}))

	_, _, err := state.ConsumeTraces(context.Background(), traceWithStringTokens("18446744073709551615", "1"))
	if err == nil || !strings.Contains(err.Error(), "token value exceeds") {
		t.Fatalf("ConsumeTraces error = %v, want token bound rejection", err)
	}
	if len(state.slices) != 0 {
		t.Fatalf("slices mutated on rejected token span: %d", len(state.slices))
	}

	_, _, err = state.ConsumeTraces(context.Background(), traceWithStringTokens("9223372036854775807", "1"))
	if err == nil || !strings.Contains(err.Error(), "top-k token weight overflows int64") {
		t.Fatalf("ConsumeTraces error = %v, want top-k overflow rejection", err)
	}
	if len(state.slices) != 0 {
		t.Fatalf("slices mutated on rejected token sum: %d", len(state.slices))
	}
}

func TestPerWindowHLLUpdatesAndRingRotation(t *testing.T) {
	clk := &fixedClock{now: time.Unix(100, 0)}
	state := newTestStateWithClock(t, clk, testConfig(func(cfg *Config) {
		cfg.WindowDuration = time.Second
		cfg.RetentionWindows = 2
		cfg.Slices = []SliceConfig{{Name: "by_model", Keys: []string{"gen_ai.request.model"}}}
	}))

	mustConsume(t, state, testSpan{Model: "gpt-test", User: "user-1", Prompt: "prompt-1", Doc: "doc-1"})
	firstWindow := state.windowStart(clk.Now())
	slice := state.slices["by_model\x00gen_ai.request.model=gpt-test"]
	if got := slice.windows[firstWindow].distinctUsers.Estimate(); got < 0.9 || got > 1.1 {
		t.Fatalf("first window users = %f, want approximately 1", got)
	}

	clk.Set(clk.Now().Add(time.Second))
	mustConsume(t, state, testSpan{Model: "gpt-test", User: "user-2", Prompt: "prompt-2", Doc: "doc-2"})
	secondWindow := state.windowStart(clk.Now())
	if got := slice.windows[secondWindow].distinctUsers.Estimate(); got < 0.9 || got > 1.1 {
		t.Fatalf("second window users = %f, want approximately 1", got)
	}
	if _, ok := slice.windows[firstWindow]; !ok {
		t.Fatal("first retained window was evicted too early")
	}

	clk.Set(clk.Now().Add(time.Second))
	mustConsume(t, state, testSpan{Model: "gpt-test", User: "user-3", Prompt: "prompt-3", Doc: "doc-3"})
	if _, ok := slice.windows[firstWindow]; ok {
		t.Fatal("expired first window still retained")
	}
}

func TestOverflowRoutesToSingleLabeledSlice(t *testing.T) {
	state := newTestState(t, testConfig(func(cfg *Config) {
		cfg.MaxSlices = 1
		cfg.Slices = []SliceConfig{{Name: "by_model", Keys: []string{"gen_ai.request.model"}}}
	}))

	metrics, _, err := state.ConsumeTraces(context.Background(), tracesFromSpans(
		testSpan{Model: "model-a", User: "user-a", Prompt: "prompt-a", Doc: "doc-a", InputTokens: ptr(10), OutputTokens: ptr(1)},
		testSpan{Model: "model-b", User: "user-b", Prompt: "prompt-b", Doc: "doc-b", InputTokens: ptr(20), OutputTokens: ptr(2)},
		testSpan{Model: "model-c", User: "user-c", Prompt: "prompt-c", Doc: "doc-c", InputTokens: ptr(30), OutputTokens: ptr(3)},
	))
	if err != nil {
		t.Fatalf("ConsumeTraces: %v", err)
	}

	if len(state.slices) != 1 {
		t.Fatalf("active real slices = %d, want 1", len(state.slices))
	}
	if len(state.overflows) != 1 {
		t.Fatalf("overflow slices = %d, want 1", len(state.overflows))
	}

	overflowLabels := map[string]string{"slice": "by_model", "slice_value": overflowSliceValue, "overflow": "true"}
	if got := intMetricValue(t, metrics, requestsMetricName, overflowLabels); got != 2 {
		t.Fatalf("overflow requests = %d, want 2", got)
	}
	if got := intMetricValue(t, metrics, inputTokensMetricName, overflowLabels); got != 50 {
		t.Fatalf("overflow input tokens = %d, want 50", got)
	}
}

func TestDeterministicLRUEvictsInactiveSlice(t *testing.T) {
	clk := &fixedClock{now: time.Unix(200, 0)}
	state := newTestStateWithClock(t, clk, testConfig(func(cfg *Config) {
		cfg.WindowDuration = time.Second
		cfg.RetentionWindows = 2
		cfg.MaxSlices = 2
		cfg.Slices = []SliceConfig{{Name: "by_model", Keys: []string{"gen_ai.request.model"}}}
	}))

	mustConsume(t, state, testSpan{Model: "model-a", User: "user-a", Prompt: "prompt-a", Doc: "doc-a"})
	mustConsume(t, state, testSpan{Model: "model-b", User: "user-b", Prompt: "prompt-b", Doc: "doc-b"})
	clk.Set(clk.Now().Add(time.Second))
	mustConsume(t, state, testSpan{Model: "model-b", User: "user-b2", Prompt: "prompt-b2", Doc: "doc-b2"})
	clk.Set(clk.Now().Add(time.Second))
	mustConsume(t, state, testSpan{Model: "model-c", User: "user-c", Prompt: "prompt-c", Doc: "doc-c"})

	if _, ok := state.slices["by_model\x00gen_ai.request.model=model-a"]; ok {
		t.Fatal("least recently used inactive slice model-a was not evicted")
	}
	if _, ok := state.slices["by_model\x00gen_ai.request.model=model-b"]; !ok {
		t.Fatal("recently used slice model-b was evicted")
	}
	if _, ok := state.slices["by_model\x00gen_ai.request.model=model-c"]; !ok {
		t.Fatal("new slice model-c was not admitted")
	}
	if len(state.overflows) != 0 {
		t.Fatalf("unexpected overflow route when inactive slice was evictable")
	}
}

func TestRolloverOverflowDoesNotEvictPreviousWindowActiveSlices(t *testing.T) {
	clk := &fixedClock{now: time.Unix(400, 0)}
	state := newTestStateWithClock(t, clk, testConfig(func(cfg *Config) {
		cfg.WindowDuration = time.Second
		cfg.RetentionWindows = 3
		cfg.MaxSlices = 2
		cfg.Slices = []SliceConfig{{Name: "by_model", Keys: []string{"gen_ai.request.model"}}}
	}))

	mustConsume(t, state, testSpan{Model: "model-a", User: "user-a", Prompt: "prompt-a", Doc: "doc-a"})
	mustConsume(t, state, testSpan{Model: "model-b", User: "user-b", Prompt: "prompt-b", Doc: "doc-b"})
	clk.Set(clk.Now().Add(time.Second))
	metrics := mustConsume(t, state, testSpan{Model: "model-c", User: "user-c", Prompt: "prompt-c", Doc: "doc-c"})

	if _, ok := state.slices["by_model\x00gen_ai.request.model=model-a"]; !ok {
		t.Fatal("previous-window active slice model-a was evicted at rollover")
	}
	if _, ok := state.slices["by_model\x00gen_ai.request.model=model-b"]; !ok {
		t.Fatal("previous-window active slice model-b was evicted at rollover")
	}
	if len(state.overflows) != 1 {
		t.Fatalf("overflow slices = %d, want 1", len(state.overflows))
	}

	overflowLabels := map[string]string{"slice": "by_model", "slice_value": overflowSliceValue, "overflow": "true"}
	if got := intMetricValue(t, metrics, requestsMetricName, overflowLabels); got != 1 {
		t.Fatalf("rollover overflow requests = %d, want 1", got)
	}
	modelALabels := map[string]string{"slice": "by_model", "slice_value": "gen_ai.request.model=model-a", "overflow": "false"}
	if got := intMetricValue(t, metrics, requestsMetricName, modelALabels); got != 1 {
		t.Fatalf("model-a cumulative requests = %d, want 1", got)
	}
}

func TestHashFieldStableAcrossStatesWithSameSecret(t *testing.T) {
	t.Setenv("GENAI_SKETCH_SECRET_A", "same-secret-32-bytes-for-unit-tests")
	t.Setenv("GENAI_SKETCH_SECRET_B", "other-secret-32-bytes-for-unit-tests")

	cfg := testConfig(func(cfg *Config) {
		cfg.Hashing.SecretEnv = "GENAI_SKETCH_SECRET_A"
	})
	secretA, err := sketchhash.SecretFromEnv("GENAI_SKETCH_SECRET_A")
	if err != nil {
		t.Fatalf("SecretFromEnv A: %v", err)
	}
	secretB, err := sketchhash.SecretFromEnv("GENAI_SKETCH_SECRET_B")
	if err != nil {
		t.Fatalf("SecretFromEnv B: %v", err)
	}

	left, err := newCollectorState(cfg, secretA, &fixedClock{now: time.Unix(1, 0)}, time.Unix(1, 0))
	if err != nil {
		t.Fatalf("newCollectorState left: %v", err)
	}
	right, err := newCollectorState(cfg, secretA, &fixedClock{now: time.Unix(2, 0)}, time.Unix(2, 0))
	if err != nil {
		t.Fatalf("newCollectorState right: %v", err)
	}
	other, err := newCollectorState(cfg, secretB, &fixedClock{now: time.Unix(3, 0)}, time.Unix(3, 0))
	if err != nil {
		t.Fatalf("newCollectorState other: %v", err)
	}

	field := left.cfg.fields[fieldPromptKey]
	leftHash, err := left.hashField(field, "  same prompt\r\n")
	if err != nil {
		t.Fatalf("left hashField: %v", err)
	}
	rightHash, err := right.hashField(field, "same prompt")
	if err != nil {
		t.Fatalf("right hashField: %v", err)
	}
	otherHash, err := other.hashField(field, "same prompt")
	if err != nil {
		t.Fatalf("other hashField: %v", err)
	}

	if leftHash != rightHash {
		t.Fatalf("same secret/profile hash mismatch: %x vs %x", leftHash, rightHash)
	}
	if leftHash == otherHash {
		t.Fatalf("different secret produced same prompt hash: %x", leftHash)
	}
}

func TestTopKSnapshotBoundsBracketTrueWeightedPromptCounts(t *testing.T) {
	clk := &fixedClock{now: time.Unix(300, 0)}
	state := newTestStateWithClock(t, clk, testConfig(func(cfg *Config) {
		cfg.TopK = 2
		cfg.Slices = []SliceConfig{{Name: "by_model", Keys: []string{"gen_ai.request.model"}}}
	}))

	mustConsume(t, state,
		testSpan{Model: "top-model", User: "user-a", Prompt: "prompt-a", Doc: "doc-a", InputTokens: ptr(10), OutputTokens: ptr(5)},
		testSpan{Model: "top-model", User: "user-b", Prompt: "prompt-b", Doc: "doc-b", InputTokens: ptr(3), OutputTokens: ptr(2)},
		testSpan{Model: "top-model", User: "user-c", Prompt: "prompt-a", Doc: "doc-c", InputTokens: ptr(7), OutputTokens: ptr(8)},
	)

	snapshot, err := state.TopKSnapshot(clk.Now())
	if err != nil {
		t.Fatalf("TopKSnapshot: %v", err)
	}
	if snapshot.ItemCount() != 2 {
		t.Fatalf("top-k item count = %d, want 2", snapshot.ItemCount())
	}

	trueWeights := map[string]int64{}
	for prompt, weight := range map[string]int64{"prompt-a": 30, "prompt-b": 5} {
		hashValue, err := state.hashField(state.cfg.fields[fieldPromptKey], prompt)
		if err != nil {
			t.Fatalf("hash prompt %q: %v", prompt, err)
		}
		trueWeights[fmt.Sprintf("%016x", hashValue)] = weight
	}

	for _, slice := range snapshot.Slices {
		for _, item := range slice.Items {
			trueWeight, ok := trueWeights[item.Hash]
			if !ok {
				t.Fatalf("unexpected top-k hash %s in snapshot %#v", item.Hash, snapshot)
			}
			if item.LowerBound > item.Estimate || item.Estimate > item.UpperBound {
				t.Fatalf("non-monotonic bounds for %s: lower=%d estimate=%d upper=%d", item.Hash, item.LowerBound, item.Estimate, item.UpperBound)
			}
			if item.LowerBound > trueWeight || trueWeight > item.UpperBound {
				t.Fatalf("bounds for %s do not bracket true weight %d: lower=%d upper=%d", item.Hash, trueWeight, item.LowerBound, item.UpperBound)
			}
		}
	}
}

func TestTopKSkipsMissingTokenWeights(t *testing.T) {
	clk := &fixedClock{now: time.Unix(350, 0)}
	state := newTestStateWithClock(t, clk, testConfig(func(cfg *Config) {
		cfg.Slices = []SliceConfig{{Name: "by_model", Keys: []string{"gen_ai.request.model"}}}
	}))

	mustConsume(t, state,
		testSpan{Model: "missing-model", User: "user-a", Prompt: "prompt-a", Doc: "doc-a", InputTokens: ptr(10)},
	)

	snapshot, err := state.TopKSnapshot(clk.Now())
	if err != nil {
		t.Fatalf("TopKSnapshot: %v", err)
	}
	if snapshot.ItemCount() != 0 {
		t.Fatalf("top-k item count = %d, want 0 for missing token usage", snapshot.ItemCount())
	}
}

func TestDefaultOperationFilterClassifiesLLMRequestsOnly(t *testing.T) {
	state := newTestState(t, defaultConfig())
	for _, test := range []struct {
		name      string
		operation string
		model     string
		request   bool
	}{
		{name: "chat", operation: "chat", request: true},
		{name: "generate content", operation: "generate_content", request: true},
		{name: "text completion", operation: "text_completion", request: true},
		{name: "embeddings", operation: "embeddings", request: true},
		{name: "legacy model fallback", model: "legacy-model", request: true},
		{name: "retrieval", operation: "retrieval"},
		{name: "execute tool", operation: "execute_tool"},
		{name: "execute tool with model", operation: "execute_tool", model: "orchestrator-model"},
		{name: "invoke agent", operation: "invoke_agent"},
		{name: "invoke workflow", operation: "invoke_workflow"},
		{name: "unknown", operation: "custom_orchestration"},
	} {
		t.Run(test.name, func(t *testing.T) {
			span := ptrace.NewSpan()
			if test.operation != "" {
				span.Attributes().PutStr("gen_ai.operation.name", test.operation)
			}
			if test.model != "" {
				span.Attributes().PutStr("gen_ai.request.model", test.model)
			}
			data := spanData{spanAttrs: span.Attributes(), resourceAttrs: pcommon.NewMap()}
			update, err := state.classifySpan(span, data)
			if err != nil {
				t.Fatalf("classifySpan: %v", err)
			}
			if got := update.request; got != test.request {
				t.Fatalf("request classification = %t, want %t", got, test.request)
			}
		})
	}
}

func TestSliceKeySpanValueOverridesResourceValue(t *testing.T) {
	label := sliceLabelFor(
		SliceConfig{Name: "by_team", Keys: []string{"team.id"}},
		spanData{
			spanAttrs:     attrs(map[string]any{"team.id": "span-team"}),
			resourceAttrs: attrs(map[string]any{"team.id": "resource-team"}),
		},
	)
	if label.value != "team.id=span-team" {
		t.Fatalf("slice label = %q, want span value", label.value)
	}
}

func TestSliceResourceFallbackCanBeScopedToConfiguredKeys(t *testing.T) {
	label := sliceLabelFor(
		SliceConfig{
			Name:                   "by_team_model",
			Keys:                   []string{"team.id", "gen_ai.request.model"},
			FromResourceAttributes: []string{"team.id"},
		},
		spanData{
			spanAttrs: pcommon.NewMap(),
			resourceAttrs: attrs(map[string]any{
				"team.id":              "resource-team",
				"gen_ai.request.model": "resource-model",
			}),
		},
	)
	if label.value != "team.id=resource-team|gen_ai.request.model=<missing>" {
		t.Fatalf("slice label = %q, want scoped resource fallback", label.value)
	}
}

func TestSliceLabelUsesFixedPlaceholderForLongValue(t *testing.T) {
	label := sliceLabelFor(
		SliceConfig{Name: "by_model", Keys: []string{"gen_ai.request.model"}},
		spanData{
			spanAttrs: attrs(map[string]any{"gen_ai.request.model": strings.Repeat("x", maxSliceLabelPartBytes+1)}),
		},
	)
	if label.value != "gen_ai.request.model="+tooLongSliceValue {
		t.Fatalf("slice label = %q, want fixed too-long placeholder", label.value)
	}
}

func TestDedupSuppressesRepeatedRequestID(t *testing.T) {
	state := newTestState(t, testConfig(func(cfg *Config) {
		cfg.Dedup.Enabled = true
		cfg.Dedup.RequestIDFrom = []string{"request.id"}
		cfg.Slices = []SliceConfig{{Name: "by_model", Keys: []string{"gen_ai.request.model"}}}
	}))

	metrics := mustConsume(t, state,
		testSpan{Model: "dedup-model", User: "user-a", Prompt: "prompt-a", Doc: "doc-a", RequestID: "request-1", InputTokens: ptr(10), OutputTokens: ptr(1)},
		testSpan{Model: "dedup-model", User: "user-a", Prompt: "prompt-a", Doc: "doc-a", RequestID: "request-1", InputTokens: ptr(10), OutputTokens: ptr(1)},
	)

	labels := map[string]string{"slice": "by_model", "slice_value": "gen_ai.request.model=dedup-model", "overflow": "false"}
	if got := intMetricValue(t, metrics, requestsMetricName, labels); got != 1 {
		t.Fatalf("deduped requests = %d, want 1", got)
	}
	if got := intMetricValue(t, metrics, totalTokensMetricName, labels); got != 11 {
		t.Fatalf("deduped total tokens = %d, want 11", got)
	}
}

func newTestState(t *testing.T, cfg *Config) *collectorState {
	t.Helper()
	return newTestStateWithClock(t, &fixedClock{now: time.Unix(1000, 0)}, cfg)
}

func newTestStateWithClock(t *testing.T, clk *fixedClock, cfg *Config) *collectorState {
	t.Helper()

	t.Setenv("GENAI_SKETCH_SECRET", "test-secret-32-bytes-for-unit-tests")
	secret, err := sketchhash.SecretFromEnv("GENAI_SKETCH_SECRET")
	if err != nil {
		t.Fatalf("SecretFromEnv: %v", err)
	}
	state, err := newCollectorState(cfg, secret, clk, clk.Now())
	if err != nil {
		t.Fatalf("newCollectorState: %v", err)
	}
	return state
}

func testConfig(edit func(*Config)) *Config {
	cfg := defaultConfig()
	edit(cfg)
	if err := cfg.Validate(); err != nil {
		panic(err)
	}
	return cfg
}

func mustConsume(t *testing.T, state *collectorState, spans ...testSpan) pmetric.Metrics {
	t.Helper()
	metrics, ok, err := state.ConsumeTraces(context.Background(), tracesFromSpans(spans...))
	if err != nil {
		t.Fatalf("ConsumeTraces: %v", err)
	}
	if !ok {
		t.Fatal("expected metrics")
	}
	return metrics
}

type testSpan struct {
	Model        string
	Team         string
	User         string
	Prompt       string
	Doc          string
	RequestID    string
	InputTokens  *int64
	OutputTokens *int64
}

func tracesFromSpans(spans ...testSpan) ptrace.Traces {
	traces := ptrace.NewTraces()
	resource := traces.ResourceSpans().AppendEmpty()
	resource.Resource().Attributes().PutStr("service.name", "unit-test")
	pdataSpans := resource.ScopeSpans().AppendEmpty().Spans()
	for _, spec := range spans {
		span := pdataSpans.AppendEmpty()
		span.SetName("genai.request")
		attrs := span.Attributes()
		attrs.PutStr("gen_ai.request.model", spec.Model)
		if spec.Team != "" {
			attrs.PutStr("team.id", spec.Team)
		}
		if spec.User != "" {
			attrs.PutStr("enduser.id", spec.User)
		}
		if spec.Prompt != "" {
			attrs.PutStr("gen_ai.request.prompt", spec.Prompt)
		}
		if spec.Doc != "" {
			attrs.PutStr("retrieval.doc_id", spec.Doc)
		}
		if spec.RequestID != "" {
			attrs.PutStr("request.id", spec.RequestID)
		}
		if spec.InputTokens != nil {
			attrs.PutInt("gen_ai.usage.input_tokens", *spec.InputTokens)
		}
		if spec.OutputTokens != nil {
			attrs.PutInt("gen_ai.usage.output_tokens", *spec.OutputTokens)
		}
	}
	return traces
}

func traceWithStringTokens(input string, output string) ptrace.Traces {
	traces := ptrace.NewTraces()
	resource := traces.ResourceSpans().AppendEmpty()
	resource.Resource().Attributes().PutStr("service.name", "unit-test")
	span := resource.ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	span.SetName("genai.request")
	attrs := span.Attributes()
	attrs.PutStr("gen_ai.operation.name", "chat")
	attrs.PutStr("gen_ai.request.model", "overflow-model")
	attrs.PutStr("enduser.id", "user-overflow")
	attrs.PutStr("gen_ai.request.prompt", "prompt-overflow")
	attrs.PutStr("retrieval.doc_id", "doc-overflow")
	attrs.PutStr("gen_ai.usage.input_tokens", input)
	attrs.PutStr("gen_ai.usage.output_tokens", output)
	return traces
}

func attrs(values map[string]any) pcommon.Map {
	out := pcommon.NewMap()
	for key, value := range values {
		switch typed := value.(type) {
		case string:
			out.PutStr(key, typed)
		case int64:
			out.PutInt(key, typed)
		}
	}
	return out
}

func ptr(value int64) *int64 {
	return &value
}
