//go:build integration

// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package integration

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
	tracecollectorpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"go.yaml.in/yaml/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
)

type rootInvocationProducer struct {
	dir, path  string
	otlp, prom int
	logs       *collectorOutput
	client     tracecollectorpb.TraceServiceClient
}

// This test sends one real OTLP RPC per producer to the built collector.
// The remote pair retains its parent IDs when split; an unseen parent is not
// a parentless root. The legacy agent metric deliberately keeps its old rule.
func TestRootInvocationsRealOTLP(t *testing.T) {
	binary := os.Getenv("ROOT_INVOCATIONS_TEST_BINARY")
	if binary == "" {
		binary = filepath.Join(repoRoot(t), "dist", "otelcol-genai-sketches")
	}
	requireExecutable(t, binary)
	var baseline []summary.Envelope
	for _, mode := range []string{"default-off", "explicit-off", "enabled-default", "enabled-runner"} {
		var logs []*collectorOutput
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			var docs []summary.Envelope
			for i := range 2 {
				p := rootInvocationStart(t, ctx, binary, mode, i)
				logs = append(logs, p.logs)
				exportModelUsageProfile(t, ctx, p.client, rootInvocationRequest(i))
				docs = append(docs, rootInvocationObserve(t, p, mode, i))
			}
			if mode == "default-off" {
				baseline = docs
			} else {
				rootInvocationCompareBase(t, docs, baseline)
			}
			rootInvocationCheckMerge(t, docs, mode)
		})
		// Subtest cleanup has waited for the actual processes, so log reads are
		// race-free rather than concurrent reads of exec's output buffer.
		for _, log := range logs {
			rootInvocationNoSentinel(t, []byte(log.String()))
		}
	}
}

func rootInvocationObserve(t *testing.T, p *rootInvocationProducer, mode string, index int) summary.Envelope {
	t.Helper()
	requests, agents, roots := uint64(3), uint64(2), uint64(0)
	if index == 1 {
		requests, agents = 1, 0
	}
	if strings.HasPrefix(mode, "enabled") && index == 0 {
		roots = 2
		if mode == "enabled-runner" {
			roots = 3
		}
	}
	doc := waitSummary(t, p.dir, requests)
	if doc.Counters["agent_runs"] != agents {
		t.Fatal("legacy agent count changed", doc.Counters)
	}
	rootInvocationCheckDefinition(t, doc, mode, roots)
	metrics := scrapeEventually(t, p.prom, regexp.MustCompile(fmt.Sprintf(`(?m)^gen_ai_sketch_requests_total\{[^\n]*\}\s+%d$`, requests)))
	if got := sumPrometheusMetric(t, metrics, "gen_ai_sketch_agent_runs_total"); got != int64(agents) {
		t.Fatal("legacy metric changed", got)
	}
	if strings.HasPrefix(mode, "enabled") {
		if got := sumPrometheusMetric(t, metrics, "gen_ai_sketch_root_invocations_total"); got != int64(roots) {
			t.Fatalf("roots=%d want %d", got, roots)
		}
	} else if strings.Contains(metrics, "gen_ai_sketch_root_invocations") {
		t.Fatal("default-off metric emitted")
	}
	rootInvocationCheckSurfaces(t, p.dir, metrics)
	return doc
}

func rootInvocationCompareBase(t *testing.T, docs, baseline []summary.Envelope) {
	t.Helper()
	for i, doc := range docs {
		counters := maps.Clone(doc.Counters)
		for key := range counters {
			if strings.HasPrefix(key, "root_invocations.") {
				delete(counters, key)
			}
		}
		if doc.AccountingID != baseline[i].AccountingID || !reflect.DeepEqual(counters, baseline[i].Counters) || !reflect.DeepEqual(doc.Sketches, baseline[i].Sketches) {
			t.Fatal("root opt-in changed base accounting or sketches")
		}
	}
}

func rootInvocationCheckMerge(t *testing.T, docs []summary.Envelope, mode string) {
	t.Helper()
	combined, err := summary.Combine(docs, []string{"root-0", "root-1"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]uint64{"enabled-default": 2, "enabled-runner": 3}[mode]
	if combined.Counters["root_invocations.v1.count"] != want || combined.Counters["requests"] != 4 {
		t.Fatal("split collectors changed root or inference counts", combined.Counters)
	}
}

func rootInvocationStart(t *testing.T, ctx context.Context, binary, mode string, index int) *rootInvocationProducer {
	t.Helper()
	p := &rootInvocationProducer{dir: t.TempDir(), otlp: freePort(t), prom: freePort(t)}
	p.path = writeModelUsageProfileConfig(t, p.otlp, p.prom, p.dir, nil)
	data, err := os.ReadFile(p.path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	c := cfg["connectors"].(map[string]any)["genaisketch"].(map[string]any)
	if mode != "default-off" {
		option := map[string]any{"enabled": strings.HasPrefix(mode, "enabled")}
		if mode == "enabled-runner" || mode == "explicit-off" {
			option["operations"] = []string{"invoke_agent", "invoke_workflow", "invoke_runner"}
		}
		c["root_invocations"] = option
	}
	c["summary_export"].(map[string]any)["producer_id"] = fmt.Sprintf("root-%d", index)
	// No asynchronous processor can acknowledge before connector ingestion.
	delete(cfg["service"].(map[string]any)["pipelines"].(map[string]any)["traces"].(map[string]any), "processors")
	data, err = yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.path, data, 0600); err != nil {
		t.Fatal(err)
	}
	_, p.logs = startCollector(t, ctx, binary, p.path)
	dial, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	conn, err := grpc.NewClient(fmt.Sprintf("127.0.0.1:%d", p.otlp), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	conn.Connect()
	for state := conn.GetState(); state != connectivity.Ready; state = conn.GetState() {
		if !conn.WaitForStateChange(dial, state) {
			t.Fatalf("collector gRPC connection did not become ready: %v", dial.Err())
		}
	}
	p.client = tracecollectorpb.NewTraceServiceClient(conn)
	return p
}

func rootInvocationRequest(part int) *tracecollectorpb.ExportTraceServiceRequest {
	var specs []struct {
		operation  string
		id, parent byte
		kind       tracepb.Span_SpanKind
	}
	add := func(op string, id, parent byte, kind tracepb.Span_SpanKind) {
		specs = append(specs, struct {
			operation  string
			id, parent byte
			kind       tracepb.Span_SpanKind
		}{op, id, parent, kind})
	}
	if part == 0 {
		add("invoke_agent", 1, 0, tracepb.Span_SPAN_KIND_INTERNAL)
		add("invoke_runner", 2, 0, tracepb.Span_SPAN_KIND_INTERNAL)
		add("invoke_agent", 3, 2, tracepb.Span_SPAN_KIND_INTERNAL)
		add("chat", 4, 3, tracepb.Span_SPAN_KIND_CLIENT)
		add("invoke_workflow", 5, 0, tracepb.Span_SPAN_KIND_INTERNAL)
		add("chat", 6, 5, tracepb.Span_SPAN_KIND_CLIENT)
		add("invoke_agent", 7, 0, tracepb.Span_SPAN_KIND_CLIENT)
		add("invoke_agent", 8, 7, tracepb.Span_SPAN_KIND_INTERNAL)
		add("http", 9, 0, tracepb.Span_SPAN_KIND_SERVER)
		add("invoke_workflow", 10, 9, tracepb.Span_SPAN_KIND_INTERNAL)
		add("chat", 11, 10, tracepb.Span_SPAN_KIND_CLIENT)
		// The remote caller lives here; its framework span goes to producer 1.
		add("invoke_workflow", 12, 0, tracepb.Span_SPAN_KIND_CLIENT)
	} else {
		add("invoke_workflow", 13, 12, tracepb.Span_SPAN_KIND_INTERNAL)
		add("chat", 14, 13, tracepb.Span_SPAN_KIND_CLIENT)
	}
	request := traceRequest(make([]otlpSpanSpec, len(specs))...)
	spans := request.ResourceSpans[0].ScopeSpans[0].Spans
	for i, spec := range specs {
		span := spans[i]
		span.TraceId, span.SpanId = make([]byte, 16), make([]byte, 8)
		span.TraceId[0], span.SpanId[0] = 0x41, spec.id
		if spec.parent != 0 {
			span.ParentSpanId = make([]byte, 8)
			span.ParentSpanId[0] = spec.parent
		}
		span.Name, span.Kind = "ROOT_PRIVATE_SPAN", spec.kind
		span.Attributes = []*commonpb.KeyValue{
			stringKV("gen_ai.operation.name", spec.operation), stringKV("gen_ai.request.model", "root-model"),
			stringKV("gen_ai.agent.name", "ROOT_PRIVATE_AGENT"), stringKV("enduser.id", "ROOT_PRIVATE_USER"),
			stringKV("gen_ai.request.prompt", "ROOT_PRIVATE_PROMPT"), stringKV("gen_ai.conversation.id", "ROOT_PRIVATE_SESSION"),
		}
		if spec.operation == "chat" {
			span.Attributes = append(span.Attributes, intKV("gen_ai.usage.input_tokens", 10), intKV("gen_ai.usage.output_tokens", 2))
		}
	}
	return request
}

func rootInvocationCheckDefinition(t *testing.T, doc summary.Envelope, mode string, count uint64) {
	t.Helper()
	expected := map[string]uint64{}
	if strings.HasPrefix(mode, "enabled") {
		expected = map[string]uint64{"root_invocations.v1.count": count, "root_invocations.v1.rule.root_internal_parentless": 0, "root_invocations.v1.operation.invoke_agent": 0, "root_invocations.v1.operation.invoke_workflow": 0}
		if mode == "enabled-runner" {
			expected["root_invocations.v1.operation.invoke_runner"] = 0
		}
	}
	actual := map[string]uint64{}
	for key, value := range doc.Counters {
		if strings.HasPrefix(key, "root_invocations.") {
			actual[key] = value
		}
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("optional counter definition: got %v want %v", actual, expected)
	}
}

func rootInvocationCheckSurfaces(t *testing.T, dir, metrics string) {
	t.Helper()
	rootInvocationNoSentinel(t, []byte(metrics))
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatal("summary files missing", err)
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		rootInvocationNoSentinel(t, data)
		doc, err := summary.Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		for _, payload := range doc.Sketches {
			rootInvocationNoSentinel(t, payload.Data)
		}
	}
}

func rootInvocationNoSentinel(t *testing.T, data []byte) {
	t.Helper()
	if bytes.Contains(data, []byte("ROOT_PRIVATE_")) {
		t.Fatal("private invocation identity or content reached an exported surface")
	}
}
