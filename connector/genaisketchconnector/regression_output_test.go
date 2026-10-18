// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package genaisketchconnector

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

type compatibilityCase struct {
	name                       string
	summary, dedup, modelUsage bool
	extended, noTopK           bool
}

var normalCompatibilityCases = []compatibilityCase{
	{name: "default"},
	{name: "dedup", dedup: true},
	{name: "summary", summary: true},
	{name: "summary_dedup", summary: true, dedup: true},
	{name: "model_usage", summary: true, modelUsage: true},
	{name: "model_usage_dedup", summary: true, dedup: true, modelUsage: true},
	{name: "summary_extended", summary: true, extended: true},
	{name: "summary_no_topk", summary: true, noTopK: true},
}

type compatibilityBytes struct {
	Metrics   []byte
	TopK      []byte
	Summaries [][]byte
}

type compatibilityDigest struct {
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type compatibilityOutput struct {
	Metrics   compatibilityDigest   `json:"metrics"`
	TopK      compatibilityDigest   `json:"topk"`
	Summaries []compatibilityDigest `json:"summaries"`
}

func (output compatibilityBytes) digest() compatibilityOutput {
	hash := func(data []byte) compatibilityDigest {
		return compatibilityDigest{len(data), fmt.Sprintf("%x", sha256.Sum256(data))}
	}
	result := compatibilityOutput{Metrics: hash(output.Metrics), TopK: hash(output.TopK)}
	for _, data := range output.Summaries {
		result.Summaries = append(result.Summaries, hash(data))
	}
	return result
}

// Digests were captured from pristine 11df148, never from the implementation
// under test. Fixed clocks, keys and epochs make every output reproducible.
func TestNormalInputOutputCompatibility(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "compatibility", "normal-output.json"))
	if err != nil {
		t.Fatal(err)
	}
	var golden map[string]map[string]compatibilityOutput
	if err := json.Unmarshal(data, &golden); err != nil {
		t.Fatal(err)
	}
	if len(golden) != len(normalCompatibilityCases) {
		t.Fatal("compatibility configuration set changed")
	}
	for _, test := range normalCompatibilityCases {
		t.Run(test.name, func(t *testing.T) {
			outputs := normalCompatibilityOutputs(t, test)
			if len(outputs) != len(golden[test.name]) {
				t.Fatal("compatibility stage set changed")
			}
			for stage, output := range outputs {
				if got, want := output.digest(), golden[test.name][stage]; !reflect.DeepEqual(got, want) {
					t.Errorf("%s output differs from baseline: got %+v; want %+v", stage, got, want)
				}
			}
		})
	}
}

func normalCompatibilityOutputs(t *testing.T, test compatibilityCase) map[string]compatibilityBytes {
	t.Helper()
	start := time.Unix(120, 0).UTC()
	clock := &fixedClock{now: start}
	cfg := defaultConfig()
	cfg.Dedup.Enabled = test.dedup
	if test.noTopK {
		cfg.TopK = 0
	}
	if test.extended {
		cfg.MCP.Enabled = true
		cfg.MCP.ToolErrors.Enabled = true
		cfg.TopKKeys = []TopKKeyConfig{{Field: fieldPromptKey}, {Field: fieldUserKey}, {Field: fieldSessionKey, Weight: "requests"}, {Field: fieldDocKey}}
	}
	if test.summary {
		cfg.SummaryExport = SummaryExportConfig{Directory: t.TempDir(), ProducerID: "compatibility-producer", ScopeID: "compatibility-scope", KeyID: "synthetic-key", Interval: time.Second}
		if err := os.Chmod(cfg.SummaryExport.Directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if test.modelUsage {
		cfg.SummaryExport.ModelUsage = &ModelUsageConfig{Slice: "by_model", Models: map[string]string{"model-a": "public-a", "model-b": "public-b"}}
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
	outputs := make(map[string]compatibilityBytes)
	for i, stage := range []string{"initial", "same_window", "next_window", "idle_window"} {
		clock.Set(start.Add([]time.Duration{time.Second, 15 * time.Second, 65 * time.Second, 125 * time.Second}[i]))
		var metrics pmetric.Metrics
		if i < 3 {
			var ok bool
			metrics, ok, err = state.ConsumeTraces(context.Background(), normalCompatibilityTraces(i))
			if err != nil || !ok {
				t.Fatalf("%s consume: ok=%v, error=%v", stage, ok, err)
			}
		} else {
			metrics = state.buildMetrics(clock.Now())
		}
		if err := canonicalCompatibilityAttributes(metrics); err != nil {
			t.Fatal(err)
		}
		metricBytes, err := (&pmetric.JSONMarshaler{}).MarshalMetrics(metrics)
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := state.TopKSnapshot(clock.Now())
		if err != nil {
			t.Fatal(err)
		}
		topKBytes, err := json.Marshal(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		output := compatibilityBytes{Metrics: metricBytes, TopK: topKBytes}
		if exporter != nil {
			docs, err := state.summaryDocuments(exporter, clock.Now())
			if err != nil {
				t.Fatal(err)
			}
			for _, doc := range docs {
				encoded, err := doc.MarshalBinary()
				if err != nil {
					t.Fatal(err)
				}
				output.Summaries = append(output.Summaries, encoded)
			}
		}
		for _, data := range append([][]byte{output.Metrics, output.TopK}, output.Summaries...) {
			for _, sentinel := range []string{"synthetic-user-", "synthetic-prompt-", "synthetic-doc-", "synthetic-session-", "synthetic-request-"} {
				if bytes.Contains(data, []byte(sentinel)) {
					t.Fatal("raw identity reached compatibility output")
				}
			}
		}
		outputs[stage] = output
	}
	return outputs
}

func normalCompatibilityTraces(batch int) ptrace.Traces {
	a := testSpan{Model: "model-a", Team: "team-a", User: "synthetic-user-a", Prompt: "synthetic-prompt-a", Doc: "synthetic-doc-a", RequestID: "synthetic-request-a", InputTokens: ptr(100), OutputTokens: ptr(20)}
	b := testSpan{Model: "model-a", Team: "team-a", User: "synthetic-user-b", Prompt: "synthetic-prompt-b", Doc: "synthetic-doc-b", RequestID: "synthetic-request-b", InputTokens: ptr(50), OutputTokens: ptr(10)}
	c := testSpan{Model: "model-b", Team: "team-b", User: "synthetic-user-a", Prompt: "synthetic-prompt-b", Doc: "synthetic-doc-b", RequestID: "synthetic-request-c", InputTokens: ptr(250), OutputTokens: ptr(30)}
	d := testSpan{Model: "model-b", Team: "team-b", User: "synthetic-user-c", Prompt: "synthetic-prompt-c", RequestID: "synthetic-request-d"}
	z := testSpan{Model: "model-b", Team: "team-b", User: "synthetic-user-c", RequestID: "synthetic-request-zero", InputTokens: ptr(0), OutputTokens: ptr(0)}
	other := testSpan{Model: "model-other", Team: "team-c", User: "synthetic-user-d", Prompt: "synthetic-prompt-d", InputTokens: ptr(17), OutputTokens: ptr(3)}
	specs := [][]testSpan{{a, b, c, d, z, a}, {b, other}, {a, c}}[batch]
	traces := tracesFromSpans(specs...)
	spans := traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans()
	for i := 0; i < spans.Len(); i++ {
		span := spans.At(i)
		span.SetStartTimestamp(pcommon.Timestamp(120_000_000_000 + uint64(batch)*60_000_000_000 + uint64(i)*1_000))
		span.SetEndTimestamp(span.StartTimestamp() + 500)
		attributes := span.Attributes()
		attributes.PutStr("gen_ai.operation.name", "chat")
		attributes.PutStr("session.id", "synthetic-session-a")
		if specs[i].InputTokens != nil {
			attributes.PutInt("gen_ai.usage.cache_read.input_tokens", *specs[i].InputTokens/4)
			attributes.PutInt("gen_ai.usage.cache_write.input_tokens", *specs[i].InputTokens/10)
			attributes.PutInt("gen_ai.usage.reasoning.output_tokens", *specs[i].OutputTokens/4)
		}
	}
	if batch == 0 {
		ordinary := spans.AppendEmpty()
		ordinary.SetName("http.health")
		ordinary.Attributes().PutStr("http.route", "/health")
		agent := spans.AppendEmpty()
		agent.SetName("agent.run")
		agent.Attributes().PutStr("gen_ai.operation.name", "invoke_agent")
		agent.Attributes().PutStr("gen_ai.request.model", "model-a")
		tool := spans.AppendEmpty()
		tool.SetName("tool.call")
		tool.Attributes().PutStr("gen_ai.operation.name", "execute_tool")
		tool.Attributes().PutStr("gen_ai.request.model", "model-a")
		tool.Attributes().PutStr("mcp.session.id", "synthetic-session-tool")
		tool.Attributes().PutStr("mcp.method.name", "tools/call")
		tool.Attributes().PutStr("mcp.resource.uri", "synthetic-doc-tool")
		tool.Attributes().PutStr("gen_ai.tool.name", "synthetic-tool")
		tool.Attributes().PutStr("error.type", "synthetic-error")
	}
	return traces
}

// Label maps have no defined iteration order. Reject duplicates before sorting
// so normalization cannot hide malformed output, then preserve all fields.
func canonicalCompatibilityAttributes(metrics pmetric.Metrics) error {
	sortAttributes := func(attributes pcommon.Map) error {
		keys := make([]string, 0, attributes.Len())
		seen := make(map[string]struct{}, attributes.Len())
		var err error
		attributes.Range(func(key string, _ pcommon.Value) bool {
			if _, exists := seen[key]; exists {
				err = fmt.Errorf("duplicate metric attribute key %q", key)
				return false
			}
			seen[key] = struct{}{}
			keys = append(keys, key)
			return true
		})
		if err != nil {
			return err
		}
		sort.Strings(keys)
		sorted := pcommon.NewMap()
		for _, key := range keys {
			value, _ := attributes.Get(key)
			value.CopyTo(sorted.PutEmpty(key))
		}
		sorted.CopyTo(attributes)
		return nil
	}
	resources := metrics.ResourceMetrics()
	for i := 0; i < resources.Len(); i++ {
		if err := sortAttributes(resources.At(i).Resource().Attributes()); err != nil {
			return err
		}
		scopes := resources.At(i).ScopeMetrics()
		for j := 0; j < scopes.Len(); j++ {
			if err := sortAttributes(scopes.At(j).Scope().Attributes()); err != nil {
				return err
			}
			values := scopes.At(j).Metrics()
			for k := 0; k < values.Len(); k++ {
				metric := values.At(k)
				var points pmetric.NumberDataPointSlice
				switch metric.Type() {
				case pmetric.MetricTypeSum:
					points = metric.Sum().DataPoints()
				case pmetric.MetricTypeGauge:
					points = metric.Gauge().DataPoints()
				default:
					return fmt.Errorf("unhandled compatibility metric type %s", metric.Type())
				}
				for point := 0; point < points.Len(); point++ {
					if err := sortAttributes(points.At(point).Attributes()); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func TestCompatibilityNormalizationRejectsDuplicateAttributes(t *testing.T) {
	for _, metricType := range []string{"sum", "gauge"} {
		for location, name := range []string{"resource", "scope", "point"} {
			for _, secondValue := range []string{"first", "different"} {
				t.Run(metricType+"/"+name+"/"+secondValue, func(t *testing.T) {
					attributes := []string{"[]", "[]", "[]"}
					// Decode repeated OTLP key/value entries: PutStr would replace
					// the first value and could not construct this negative case.
					attributes[location] = fmt.Sprintf(`[
						{"key":"z","value":{"stringValue":"before"}},
						{"key":"label","value":{"stringValue":"first"}},
						{"key":"label","value":{"stringValue":%q}},
						{"key":"a","value":{"stringValue":"after"}}
					]`, secondValue)
					wire := fmt.Sprintf(`{"resourceMetrics":[{
						"resource":{"attributes":%s},
						"scopeMetrics":[{"scope":{"attributes":%s},"metrics":[{
							"name":"compatibility","%s":{"dataPoints":[{"attributes":%s,"asInt":"1"}]}
						}]}]
					}]}`, attributes[0], attributes[1], metricType, attributes[2])
					metrics, err := (&pmetric.JSONUnmarshaler{}).UnmarshalMetrics([]byte(wire))
					if err != nil {
						t.Fatal(err)
					}
					resource := metrics.ResourceMetrics().At(0)
					scope := resource.ScopeMetrics().At(0)
					metric := scope.Metrics().At(0)
					var points pmetric.NumberDataPointSlice
					if metricType == "sum" {
						points = metric.Sum().DataPoints()
					} else {
						points = metric.Gauge().DataPoints()
					}
					maps := []pcommon.Map{resource.Resource().Attributes(), scope.Scope().Attributes(), points.At(0).Attributes()}
					if maps[location].Len() != 4 {
						t.Fatal("decoder did not preserve duplicate attribute entries")
					}
					before, err := (&pmetric.JSONMarshaler{}).MarshalMetrics(metrics)
					if err != nil {
						t.Fatal(err)
					}
					if err := canonicalCompatibilityAttributes(metrics); err == nil {
						t.Fatal("normalization accepted duplicate attribute keys")
					}
					after, err := (&pmetric.JSONMarshaler{}).MarshalMetrics(metrics)
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(before, after) {
						t.Fatal("duplicate attributes were modified before rejection")
					}
				})
			}
		}
	}
}
