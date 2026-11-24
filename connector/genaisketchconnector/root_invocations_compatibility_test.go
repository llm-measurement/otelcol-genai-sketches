// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package genaisketchconnector

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

const rootCompatibilityBase = "a36569b478c8206c9c7031533f513371e79ef42e"
const rootCompatibilitySeed = "fixed-clock-120-fixed-key-fixed-epoch-no-randomness"

type rootCompatibilityInput struct {
	name   string
	traces ptrace.Traces
	edit   func(*Config)
}

type rootCompatibilityOutput struct {
	Consumed bool                `json:"consumed"`
	Error    string              `json:"error,omitempty"`
	Surfaces compatibilityOutput `json:"surfaces"`
	Logs     compatibilityDigest `json:"logs"`
}

type rootCompatibilityGolden struct {
	Base     string                                        `json:"base"`
	Seed     string                                        `json:"seed"`
	Source   string                                        `json:"pristine_source_manifest_sha256"`
	Harness  string                                        `json:"harness_sha256"`
	Fixtures map[string]string                             `json:"fixture_sha256"`
	Outputs  map[string]map[string]rootCompatibilityOutput `json:"outputs"`
}

// Recording is allowed only in a separately verified pristine-base archive.
// Normal tests never update expected output, including when a comparison fails.
func TestRootInvocationsV074OutputCompatibility(t *testing.T) {
	inputs, fixtures := rootCompatibilityInputs(t)
	actual := rootCompatibilityGolden{
		Base: rootCompatibilityBase, Seed: rootCompatibilitySeed,
		Fixtures: fixtures, Outputs: map[string]map[string]rootCompatibilityOutput{},
	}
	for _, profile := range normalCompatibilityCases {
		for _, input := range inputs {
			name := profile.name + "/" + input.name
			t.Run(name, func(t *testing.T) {
				actual.Outputs[name] = rootCompatibilityRun(t, profile, input)
			})
		}
	}
	if output := os.Getenv("ROOT_INVOCATIONS_CAPTURE"); output != "" {
		rootCompatibilityCapture(t, output, &actual)
		return
	}
	data := rootCompatibilityRead(t, filepath.Join("testdata", "root_invocations", "v074-output.json"))
	var expected rootCompatibilityGolden
	if err := json.Unmarshal(data, &expected); err != nil {
		t.Fatal(err)
	}
	if expected.Base != actual.Base || expected.Seed != actual.Seed || len(expected.Source) != 64 || len(expected.Harness) != 64 {
		t.Fatal("pristine v0.7.4 provenance is missing or changed")
	}
	if !reflect.DeepEqual(actual.Fixtures, expected.Fixtures) {
		t.Fatal("fixture inventory changed; review the exact added/removed/modified inputs")
	}
	if len(actual.Outputs) != len(expected.Outputs) {
		t.Fatal("compatibility profile or fixture coverage changed")
	}
	for name, output := range actual.Outputs {
		if !reflect.DeepEqual(output, expected.Outputs[name]) {
			t.Errorf("%s differs from pristine v0.7.4: got %+v; want %+v", name, output, expected.Outputs[name])
		}
	}
	t.Logf("base=%s seed=%s fixtures=%d cases=%d", actual.Base, actual.Seed, len(fixtures), len(actual.Outputs))
}

func rootCompatibilityRun(t *testing.T, profile compatibilityCase, input rootCompatibilityInput) map[string]rootCompatibilityOutput {
	t.Helper()
	start := time.Unix(120, 0).UTC()
	clock := &fixedClock{now: start.Add(time.Second)}
	cfg := rootCompatibilityConfig(t, profile)
	if input.edit != nil {
		input.edit(cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	state := newTestStateWithClock(t, clock, cfg)
	exporter, err := newSummaryExporter(cfg, start)
	if err != nil {
		t.Fatal(err)
	}
	if exporter != nil {
		exporter.epoch = "0123456789abcdef0123456789abcdef"
		t.Cleanup(func() { _ = exporter.root.Close() })
	}
	outputs := map[string]rootCompatibilityOutput{}
	for _, stage := range []string{"initial", "replay", "next_window"} {
		var metrics pmetric.Metrics
		var consumed bool
		if stage == "next_window" {
			clock.Set(start.Add(65 * time.Second))
			metrics = state.buildMetrics(clock.Now())
		} else {
			traces := ptrace.NewTraces()
			input.traces.CopyTo(traces)
			metrics, consumed, err = state.ConsumeTraces(context.Background(), traces)
			if !consumed {
				// The connector forwards nothing for an ignored or rejected batch.
				// A zero pdata handle is not a valid empty metric document.
				metrics = pmetric.NewMetrics()
			}
		}
		result := rootCompatibilitySnapshot(t, state, cfg, exporter, metrics, clock.Now())
		result.Consumed = consumed
		if err != nil {
			result.Error = err.Error()
		}
		outputs[stage] = result
	}
	return outputs
}

func rootCompatibilityConfig(t *testing.T, profile compatibilityCase) *Config {
	t.Helper()
	cfg := defaultConfig()
	cfg.Dedup.Enabled = profile.dedup
	if profile.noTopK {
		cfg.TopK = 0
	}
	if profile.extended {
		cfg = fleetFixtureConfig(true, true)
		cfg.TopKKeys = []TopKKeyConfig{{Field: fieldPromptKey}, {Field: fieldUserKey}, {Field: fieldSessionKey, Weight: "requests"}, {Field: fieldDocKey}}
	}
	if profile.summary {
		cfg.SummaryExport = SummaryExportConfig{Directory: t.TempDir(), ProducerID: "compatibility-producer", ScopeID: "compatibility-scope", KeyID: "synthetic-key", Interval: time.Second}
		if err := os.Chmod(cfg.SummaryExport.Directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if profile.modelUsage {
		cfg.SummaryExport.ModelUsage = &ModelUsageConfig{Slice: "by_model", Models: map[string]string{"model-a": "public-a", "model-b": "public-b", "mock-model": "public-mock"}}
	}
	return cfg
}

func rootCompatibilitySnapshot(t *testing.T, state *collectorState, cfg *Config, exporter *summaryExporter, metrics pmetric.Metrics, now time.Time) rootCompatibilityOutput {
	t.Helper()
	if err := canonicalCompatibilityAttributes(metrics); err != nil {
		t.Fatal(err)
	}
	metricBytes, err := (&pmetric.JSONMarshaler{}).MarshalMetrics(metrics)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := state.TopKSnapshot(now)
	if err != nil {
		t.Fatal(err)
	}
	surfaces := compatibilityBytes{Metrics: metricBytes, TopK: mustJSON(t, snapshot)}
	core, logs := observer.New(zap.InfoLevel)
	conn := &tracesConnector{cfg: cfg, state: state, logger: zap.New(core)}
	if cfg.TopK > 0 {
		conn.emitTopKSnapshotLocked(now)
	}
	var entries []any
	for _, entry := range logs.All() {
		entries = append(entries, map[string]any{"message": entry.Message, "level": entry.Level.String(), "fields": entry.ContextMap()})
	}
	logBytes := mustJSON(t, entries)
	if exporter != nil {
		documents, err := state.summaryDocuments(exporter, now)
		if err != nil {
			t.Fatal(err)
		}
		for _, doc := range documents {
			for key := range doc.Counters {
				if !rootCompatibilityEnabled(cfg) && strings.HasPrefix(key, "root_invocations.") {
					t.Fatal("default-off root counter reached summary")
				}
			}
			for _, payload := range doc.Sketches {
				rootCompatibilityPrivacy(t, payload.Data)
			}
			encoded, err := doc.MarshalBinary()
			if err != nil {
				t.Fatal(err)
			}
			surfaces.Summaries = append(surfaces.Summaries, encoded)
		}
	}
	for _, data := range append([][]byte{surfaces.Metrics, surfaces.TopK, logBytes}, surfaces.Summaries...) {
		rootCompatibilityPrivacy(t, data)
	}
	return rootCompatibilityOutput{Surfaces: surfaces.digest(), Logs: rootCompatibilityHash(logBytes)}
}

func rootCompatibilityPrivacy(t *testing.T, data []byte) {
	t.Helper()
	for _, sentinel := range []string{"ROOT_PRIVATE_", "PRIVATE_LITELLM", "PRIVATE_PROMPT_SENTINEL", "SYNTHETIC_DEVICE", "SYNTHETIC_SESSION",
		"41000000000000000000000000000000", "0100000000000000", "0200000000000000", "0300000000000000"} {
		if bytes.Contains(data, []byte(sentinel)) {
			t.Fatalf("private sentinel reached exported surface: %s", sentinel)
		}
	}
}

// Reflection keeps the same test-only file compilable in pristine v0.7.4,
// which deliberately has no RootInvocations field.
func rootCompatibilityEnabled(cfg *Config) bool {
	field := reflect.ValueOf(cfg).Elem().FieldByName("RootInvocations")
	return field.IsValid() && field.FieldByName("Enabled").Bool()
}

func TestRootInvocationsNewSurfacePrivacy(t *testing.T) {
	cfg := rootCompatibilityConfig(t, compatibilityCase{summary: true})
	field := reflect.ValueOf(cfg).Elem().FieldByName("RootInvocations")
	if !field.IsValid() {
		t.Skip("v0.7.4 has no opt-in invocation surface")
	}
	field.FieldByName("Enabled").SetBool(true)
	field.FieldByName("Operations").Set(reflect.ValueOf([]string{"invoke_agent", "invoke_workflow", "invoke_runner"}))
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	clock := &fixedClock{now: time.Unix(121, 0).UTC()}
	state := newTestStateWithClock(t, clock, cfg)
	exporter, err := newSummaryExporter(cfg, time.Unix(120, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	defer exporter.root.Close()
	exporter.epoch = "0123456789abcdef0123456789abcdef"
	for _, input := range rootCompatibilityInvocationInputs(t) {
		t.Run(input.name, func(t *testing.T) {
			metrics, ok, err := state.ConsumeTraces(context.Background(), input.traces)
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				metrics = pmetric.NewMetrics()
			}
			rootCompatibilitySnapshot(t, state, cfg, exporter, metrics, clock.Now())
		})
	}
	if got := sumMetricValuesWithLabels(state.buildMetrics(clock.Now()), "gen_ai_sketch_root_invocations_total", map[string]string{"slice": "by_model"}); got != 3 {
		t.Fatalf("privacy corpus must exercise all three enabled roots: got %d", got)
	}
}

func rootCompatibilityInputs(t *testing.T) ([]rootCompatibilityInput, map[string]string) {
	t.Helper()
	var inputs []rootCompatibilityInput
	files := map[string]string{}
	for _, pattern := range []string{
		"testdata/accounting/v1/*.json", "testdata/fleet/*.json", "testdata/inspect-contract/v1/*.json",
		"testdata/identity/*.json", "../../examples/integrations/litellm/*.json",
		"../../examples/integrations/coding-agents/fixtures/*.json",
	} {
		paths, err := filepath.Glob(filepath.FromSlash(pattern))
		if err != nil || len(paths) == 0 {
			t.Fatalf("fixture inventory %s: %v", pattern, err)
		}
		for _, path := range paths {
			data := rootCompatibilityRead(t, path)
			files[filepath.ToSlash(path)] = rootCompatibilityHash(data).SHA256
			inputs = append(inputs, rootCompatibilityLoad(t, path, data)...)
		}
	}
	// Keep the established normal corpus and its released expected output in scope.
	path := filepath.Join("testdata", "compatibility", "normal-output.json")
	files[filepath.ToSlash(path)] = rootCompatibilityHash(rootCompatibilityRead(t, path)).SHA256
	for i := range 3 {
		inputs = append(inputs, rootCompatibilityInput{name: fmt.Sprintf("normal/batch-%d", i), traces: normalCompatibilityTraces(i)})
	}
	inputs = append(inputs, rootCompatibilityInvocationInputs(t)...)
	inputs = append(inputs, rootCompatibilityGatewayInputs(t)...)
	sort.Slice(inputs, func(i, j int) bool { return inputs[i].name < inputs[j].name })
	for i := 1; i < len(inputs); i++ {
		if inputs[i-1].name == inputs[i].name {
			t.Fatal("duplicate fixture name", inputs[i].name)
		}
	}
	return inputs, files
}

func rootCompatibilityLoad(t *testing.T, path string, data []byte) []rootCompatibilityInput {
	t.Helper()
	name := filepath.ToSlash(path)
	switch {
	case strings.Contains(name, "/identity/"):
		return []rootCompatibilityInput{{name: name, traces: rootCompatibilityIdentity(t, data)}}
	case strings.Contains(name, "/accounting/"):
		fixture := readAccountingFixture(t, path)
		return []rootCompatibilityInput{{name: name, traces: tracesFromFixtureResources(t, fixture.Resources), edit: func(c *Config) {
			c.Dedup.Enabled = c.Dedup.Enabled || fixture.Config.DedupEnabled
			if len(fixture.Config.RequestIDFrom) != 0 {
				c.Dedup.RequestIDFrom = fixture.Config.RequestIDFrom
			}
		}}}
	case strings.Contains(name, "/fleet/"):
		fixture := readFleetFixture(t, filepath.Base(path))
		if len(fixture.Placements) == 0 {
			return []rootCompatibilityInput{{name: name, traces: tracesFromFixtureResources(t, fixture.Resources)}}
		}
		var result []rootCompatibilityInput
		for _, placement := range fixture.Placements {
			result = append(result, rootCompatibilityInput{name: name + "/" + placement.Name, traces: tracesFromFixtureResources(t, placement.Resources)})
		}
		return result
	case strings.HasSuffix(name, "usage-cases.json"), strings.HasSuffix(name, "subset-cases.json"):
		return rootCompatibilityUsageCases(t, name, data)
	case strings.HasSuffix(name, "/manifest.json"):
		return nil // Metadata is pinned above; its 36 referenced OTLP inputs run separately.
	default:
		traces, err := (&ptrace.JSONUnmarshaler{}).UnmarshalTraces(data)
		if err != nil {
			t.Fatal(path, err)
		}
		return []rootCompatibilityInput{{name: name, traces: traces}}
	}
}

func rootCompatibilityIdentity(t *testing.T, data []byte) ptrace.Traces {
	t.Helper()
	var corpus identityDriftCorpus
	if err := json.Unmarshal(data, &corpus); err != nil || len(corpus.Cases) < 8192 {
		t.Fatal("incomplete frozen identity corpus", err)
	}
	traces := ptrace.NewTraces()
	spans := traces.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Spans()
	for _, row := range corpus.Cases {
		input, err := hex.DecodeString(row.Input)
		if err != nil {
			t.Fatal(err)
		}
		attrs := spans.AppendEmpty().Attributes()
		attrs.PutStr("gen_ai.operation.name", "chat")
		attrs.PutStr("gen_ai.request.model", "model-a")
		attrs.PutStr("gen_ai.request.prompt", string(input))
		attrs.PutInt("gen_ai.usage.input_tokens", 1)
		attrs.PutInt("gen_ai.usage.output_tokens", 1)
	}
	return traces
}

func rootCompatibilityUsageCases(t *testing.T, name string, data []byte) []rootCompatibilityInput {
	t.Helper()
	var fixture struct {
		Cases []struct {
			Name    string `json:"name"`
			Emitted struct {
				Input  int64 `json:"prompt_tokens"`
				Output int64 `json:"completion_tokens"`
			} `json:"emitted_usage"`
			Expected   [2]string        `json:"expected"`
			Attributes map[string]int64 `json:"expected_attributes"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil || len(fixture.Cases) == 0 {
		t.Fatal("invalid usage corpus", err)
	}
	var result []rootCompatibilityInput
	for _, row := range fixture.Cases {
		for _, annotated := range []bool{false, true} {
			traces := tracesFromSpans(testSpan{Model: "mock-model", Prompt: "PRIVATE_PROMPT_SENTINEL", InputTokens: ptr(row.Emitted.Input), OutputTokens: ptr(row.Emitted.Output)})
			a := traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Attributes()
			a.PutStr("gen_ai.operation.name", "chat")
			if annotated {
				for field, source := range row.Expected {
					if source != "" {
						a.PutStr("gen_ai_sketch.usage."+usageProvenanceFields[field]+".provenance", source)
					}
				}
				for key, value := range row.Attributes {
					a.PutInt(key, value)
				}
			}
			result = append(result, rootCompatibilityInput{name: fmt.Sprintf("%s/%s/annotated-%t", name, row.Name, annotated), traces: traces})
		}
	}
	return result
}

func rootCompatibilityInvocationInputs(t *testing.T) []rootCompatibilityInput {
	t.Helper()
	var result []rootCompatibilityInput
	for _, name := range []string{"legacy", "adk", "workflow", "remote-pair", "remote-client", "remote-internal", "http-parent"} {
		traces := ptrace.NewTraces()
		spans := traces.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Spans()
		add := func(id, parent byte, kind ptrace.SpanKind, operation string) {
			span := spans.AppendEmpty()
			span.SetTraceID(pcommon.TraceID{0x41})
			span.SetSpanID(pcommon.SpanID{id})
			if parent != 0 {
				span.SetParentSpanID(pcommon.SpanID{parent})
			}
			span.SetKind(kind)
			span.SetName("ROOT_PRIVATE_SPAN")
			a := span.Attributes()
			a.PutStr("gen_ai.operation.name", operation)
			a.PutStr("gen_ai.agent.name", "ROOT_PRIVATE_AGENT")
			a.PutStr("gen_ai.request.model", "model-a")
			a.PutStr("gen_ai.request.prompt", "ROOT_PRIVATE_PROMPT")
			a.PutStr("enduser.id", "ROOT_PRIVATE_USER")
			a.PutStr("gen_ai.conversation.id", "ROOT_PRIVATE_SESSION")
			if operation == "chat" {
				a.PutInt("gen_ai.usage.input_tokens", 10)
				a.PutInt("gen_ai.usage.output_tokens", 2)
			}
		}
		switch name {
		case "legacy":
			add(1, 0, ptrace.SpanKindInternal, "invoke_agent")
		case "adk":
			add(1, 0, ptrace.SpanKindInternal, "invoke_runner")
			add(2, 1, ptrace.SpanKindInternal, "invoke_agent")
			add(3, 2, ptrace.SpanKindClient, "chat")
		case "workflow":
			add(1, 0, ptrace.SpanKindInternal, "invoke_workflow")
			add(2, 1, ptrace.SpanKindClient, "chat")
		case "remote-pair":
			add(1, 0, ptrace.SpanKindClient, "invoke_agent")
			add(2, 1, ptrace.SpanKindInternal, "invoke_agent")
		case "remote-client":
			add(1, 0, ptrace.SpanKindClient, "invoke_agent")
		case "remote-internal":
			add(2, 1, ptrace.SpanKindInternal, "invoke_agent")
		case "http-parent":
			add(1, 0, ptrace.SpanKindServer, "http")
			add(2, 1, ptrace.SpanKindInternal, "invoke_workflow")
		}
		result = append(result, rootCompatibilityInput{name: "invocations/" + name, traces: traces})
	}
	return result
}

func rootCompatibilityGatewayInputs(t *testing.T) []rootCompatibilityInput {
	t.Helper()
	var result []rootCompatibilityInput
	for _, profile := range []string{modelIdentityLiteLLM, modelIdentityLiteLLMStream, modelIdentityLiteLLMResponses, modelIdentityLiteLLMMessages} {
		for _, mode := range []string{"valid", "missing", "conflict"} {
			traces := nativeLiteLLMTrace(profile, func(a pcommon.Map) {
				switch mode {
				case "missing":
					a.Remove("litellm.provider.model")
				case "conflict":
					a.PutStr("gen_ai.response.model", "PRIVATE_OTHER_SNAPSHOT")
				}
			})
			result = append(result, rootCompatibilityInput{name: "gateway/" + profile + "/" + mode, traces: traces, edit: func(c *Config) {
				if c.SummaryExport.Directory != "" {
					rootCompatibilityModelSlice(c)
					c.SummaryExport.ModelUsage = &ModelUsageConfig{Slice: "by_model"}
					nativeLiteLLMConfig(modelIdentityLiteLLMCombinedV2)(c)
				}
			}})
		}
	}
	for _, profile := range []string{modelIdentityAgentGateway, modelIdentityEnvoy} {
		for _, source := range []string{"span", "resource", "missing"} {
			traces := modelTrace(100, nil)
			if source == "span" {
				traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Attributes().PutStr("gen_ai.response.model", "PRIVATE_SNAPSHOT")
			}
			if source == "resource" {
				traces.ResourceSpans().At(0).Resource().Attributes().PutStr("gen_ai.response.model", "PRIVATE_SNAPSHOT")
			}
			result = append(result, rootCompatibilityInput{name: "gateway/" + profile + "/" + source, traces: traces, edit: func(c *Config) {
				if c.SummaryExport.Directory != "" {
					rootCompatibilityModelSlice(c)
					c.SummaryExport.ModelUsage = &ModelUsageConfig{Slice: "by_model", Identity: profile, Models: map[string]string{"PRIVATE_SNAPSHOT": "public-model"}}
				}
			}})
		}
	}
	return result
}

func rootCompatibilityModelSlice(c *Config) {
	for _, slice := range c.Slices {
		if slice.Name == "by_model" {
			return
		}
	}
	c.Slices = append(c.Slices, SliceConfig{Name: "by_model", Keys: []string{"gen_ai.request.model"}})
}

func rootCompatibilityCapture(t *testing.T, output string, actual *rootCompatibilityGolden) {
	t.Helper()
	if t.Failed() {
		t.Fatal("refusing to capture an incomplete or failed fixture run")
	}
	manifestPath := os.Getenv("ROOT_INVOCATIONS_PRISTINE_MANIFEST")
	manifestBytes := rootCompatibilityRead(t, manifestPath)
	var manifest struct {
		Base  string            `json:"base"`
		Files map[string]string `json:"files"`
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil || manifest.Base != rootCompatibilityBase || len(manifest.Files) < 100 {
		t.Fatal("capture requires the independently extracted pristine-base source manifest", err)
	}
	for path, digest := range manifest.Files {
		if rootCompatibilityHash(rootCompatibilityRead(t, filepath.Join("..", "..", filepath.FromSlash(path)))).SHA256 != digest {
			t.Fatal("capture source differs from pristine base:", path)
		}
	}
	cwd, err := os.Getwd()
	if err != nil || !strings.Contains(filepath.ToSlash(cwd), "/.cache/collector-semconv-v010-20261124/") {
		t.Fatal("capture must run in the private pristine archive")
	}
	if _, err := os.Stat(filepath.Join("..", "..", ".git")); !os.IsNotExist(err) {
		t.Fatal("capture refuses a git worktree")
	}
	// Reject added production Go files as well as changes to archived files.
	err = filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			if _, ok := manifest.Files["connector/genaisketchconnector/"+filepath.ToSlash(path)]; !ok {
				return fmt.Errorf("added production source %s", path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	actual.Source = rootCompatibilityHash(manifestBytes).SHA256
	actual.Harness = rootCompatibilityHash(rootCompatibilityRead(t, "root_invocations_compatibility_test.go")).SHA256
	data, err := json.MarshalIndent(actual, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal("capture never overwrites a golden:", err)
	}
	defer file.Close()
	if _, err := file.Write(append(data, '\n')); err != nil {
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
	t.Logf("captured pristine base=%s seed=%s fixtures=%d cases=%d manifest=%s harness=%s", actual.Base, actual.Seed, len(actual.Fixtures), len(actual.Outputs), actual.Source, actual.Harness)
}

func rootCompatibilityRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func rootCompatibilityHash(data []byte) compatibilityDigest {
	return compatibilityDigest{Bytes: len(data), SHA256: fmt.Sprintf("%x", sha256.Sum256(data))}
}
