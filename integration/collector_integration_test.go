//go:build integration

// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay Erramilli and Codex

package integration

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	tracecollectorpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const testSketchSecret = "integration-test-secret-32-bytes"

func TestOTLPTraceProducesPrometheusMetric(t *testing.T) {
	repo := repoRoot(t)
	binary := filepath.Join(repo, "dist", "otelcol-genai-sketches")
	requireExecutable(t, binary)

	otlpPort := freePort(t)
	promPort := freePort(t)
	configPath := writeCollectorConfig(t, otlpPort, promPort)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd, output := startCollector(t, ctx, binary, configPath)
	waitForProcessAlive(t, cmd, output)

	sendTracesEventually(t, otlpPort, 3)
	metrics := scrapeEventually(t, promPort, regexp.MustCompile(`(?m)^gen_ai_sketch_requests_total(?:\{[^}]*\})?\s+3$`))
	if !strings.Contains(metrics, "gen_ai_sketch_requests_total") {
		t.Fatalf("scrape did not contain gen_ai_sketch_requests_total:\n%s", metrics)
	}
}

func TestUnknownConnectorFieldFails(t *testing.T) {
	repo := repoRoot(t)
	binary := filepath.Join(repo, "dist", "otelcol-genai-sketches")
	requireExecutable(t, binary)

	config := `
receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 127.0.0.1:4317
connectors:
  genaisketch:
    unexpected_field: true
exporters:
  prometheus:
    endpoint: 127.0.0.1:8889
service:
  pipelines:
    traces:
      receivers: [otlp]
      exporters: [genaisketch]
    metrics:
      receivers: [genaisketch]
      exporters: [prometheus]
`
	configPath := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "--config", configPath)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	cmd.Env = append(os.Environ(), "GENAI_SKETCH_SECRET="+testSketchSecret)

	err := cmd.Run()
	if err == nil {
		t.Fatal("collector accepted config with unknown connector field")
	}
	if !strings.Contains(output.String(), "unexpected_field") {
		t.Fatalf("collector error did not mention unknown field; output:\n%s", output.String())
	}
}

func TestOTLPSnapshotMetrics(t *testing.T) {
	repo := repoRoot(t)
	binary := filepath.Join(repo, "dist", "otelcol-genai-sketches")
	requireExecutable(t, binary)

	otlpPort := freePort(t)
	promPort := freePort(t)
	configPath := writeCollectorConfig(t, otlpPort, promPort)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd, output := startCollector(t, ctx, binary, configPath)
	waitForProcessAlive(t, cmd, output)

	sendTraceSpecsEventually(t, otlpPort,
		otlpSpanSpec{Model: "snapshot-model", Team: "platform", User: "user-1", Prompt: "prompt-a", Doc: "doc-a", InputTokens: intPtr(10), OutputTokens: intPtr(20)},
		otlpSpanSpec{Model: "snapshot-model", Team: "platform", User: "user-2", Prompt: "prompt-a", Doc: "doc-b", InputTokens: intPtr(7), OutputTokens: intPtr(3)},
		otlpSpanSpec{Model: "snapshot-model", Team: "platform", User: "user-2", Prompt: "prompt-b", Doc: "doc-b"},
	)

	labels := map[string]string{
		"slice":       "by_model",
		"slice_value": "gen_ai.request.model=snapshot-model",
		"overflow":    "false",
	}
	metrics := scrapeEventually(t, promPort, regexp.MustCompile(`(?m)^gen_ai_sketch_requests_total\{[^}]*slice_value="gen_ai\.request\.model=snapshot-model"[^}]*\}\s+3$`))

	assertMetricValue(t, metrics, "gen_ai_sketch_requests_total", labels, 3)
	assertMetricValue(t, metrics, "gen_ai_sketch_input_tokens_total", labels, 17)
	assertMetricValue(t, metrics, "gen_ai_sketch_output_tokens_total", labels, 23)
	assertMetricValue(t, metrics, "gen_ai_sketch_total_tokens_total", labels, 40)
	assertMetricValue(t, metrics, "gen_ai_sketch_missing_token_usage_total", labels, 1)
	assertMetricWithin(t, metrics, "gen_ai_sketch_distinct_users", labels, 1.9, 2.1)
	assertMetricWithin(t, metrics, "gen_ai_sketch_distinct_prompt_signatures", labels, 1.9, 2.1)
	assertMetricWithin(t, metrics, "gen_ai_sketch_distinct_retrieval_docs", labels, 1.9, 2.1)
	assertMetricValue(t, metrics, "gen_ai_sketch_active_slices", nil, 1)
}

func TestHashedFieldSentinelsStayOutOfExportedSurfaces(t *testing.T) {
	// Sentinel values belong in hashed-field attributes. Slice-key values are an
	// allowed cleartext metric-label surface and must stay low-cardinality and
	// non-sensitive by configuration.
	repo := repoRoot(t)
	binary := filepath.Join(repo, "dist", "otelcol-genai-sketches")
	requireExecutable(t, binary)

	otlpPort := freePort(t)
	promPort := freePort(t)
	configPath := writeCollectorConfig(t, otlpPort, promPort)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd, output := startCollector(t, ctx, binary, configPath)
	waitForProcessAlive(t, cmd, output)

	sentinels := []string{
		"SENTINEL_USER_6e09d3",
		"SENTINEL_PROMPT_07c8b4",
		"SENTINEL_DOC_4f7c8a",
	}
	sendTraceSpecsEventually(t, otlpPort,
		otlpSpanSpec{
			Model:        "safe-model",
			Team:         "platform",
			User:         sentinels[0],
			Prompt:       sentinels[1],
			Doc:          sentinels[2],
			InputTokens:  intPtr(5),
			OutputTokens: intPtr(8),
		},
	)

	metrics := scrapeEventually(t, promPort, regexp.MustCompile(`(?m)^gen_ai_sketch_requests_total\{[^}]*slice_value="gen_ai\.request\.model=safe-model"[^}]*\}\s+1$`))
	logs := waitForOutputEventually(t, output, regexp.MustCompile(`genaisketch topk snapshot`))
	topKLines := 0
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, "genaisketch topk snapshot") {
			topKLines++
		}
	}
	if topKLines == 0 {
		t.Fatalf("expected at least one top-k snapshot line in collector logs:\n%s", logs)
	}
	for _, sentinel := range sentinels {
		if strings.Contains(metrics, sentinel) {
			t.Fatalf("sentinel %q leaked in metrics:\n%s", sentinel, metrics)
		}
		if strings.Contains(logs, sentinel) {
			t.Fatalf("sentinel %q leaked in collector logs:\n%s", sentinel, logs)
		}
	}
}

func TestMCPResourceURISentinelAbsentFromMetricsAndLabels(t *testing.T) {
	repo := repoRoot(t)
	binary := filepath.Join(repo, "dist", "otelcol-genai-sketches")
	requireExecutable(t, binary)

	otlpPort := freePort(t)
	promPort := freePort(t)
	configPath := writeCollectorConfig(t, otlpPort, promPort)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd, output := startCollector(t, ctx, binary, configPath)
	waitForProcessAlive(t, cmd, output)
	waitForOutputEventually(t, output, regexp.MustCompile(`started genaisketch connector`))

	const sentinel = "MCP_RESOURCE_URI_SENTINEL_DO_NOT_EXPORT"
	sendTraceSpecsEventually(t, otlpPort,
		otlpSpanSpec{
			Operation:    "chat",
			Model:        "mcp-safe-model",
			Prompt:       "safe prompt",
			InputTokens:  intPtr(4),
			OutputTokens: intPtr(2),
		},
		otlpSpanSpec{
			MCPMethod:      "resources/read",
			MCPSession:     "private-mcp-session",
			MCPResourceURI: sentinel,
		},
	)

	metrics := scrapeEventually(t, promPort, regexp.MustCompile(`(?m)^gen_ai_sketch_distinct_mcp_resources\{[^}]*slice_value="gen_ai\.request\.model=<missing>"[^}]*\}\s+`))
	logs := waitForOutputEventually(t, output, regexp.MustCompile(`genaisketch topk snapshot`))
	for _, forbidden := range []string{sentinel, "private-mcp-session"} {
		if strings.Contains(metrics, forbidden) {
			t.Fatalf("MCP value %q leaked in Prometheus metrics or labels:\n%s", forbidden, metrics)
		}
		if strings.Contains(logs, forbidden) {
			t.Fatalf("MCP value %q leaked in collector snapshot logs:\n%s", forbidden, logs)
		}
	}
}

func TestTopKDebugSurfaceAndMetricLabelBoundary(t *testing.T) {
	repo := repoRoot(t)
	binary := filepath.Join(repo, "dist", "otelcol-genai-sketches")
	requireExecutable(t, binary)

	otlpPort := freePort(t)
	promPort := freePort(t)
	configPath := writeCollectorConfig(t, otlpPort, promPort)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd, output := startCollector(t, ctx, binary, configPath)
	waitForProcessAlive(t, cmd, output)

	sendTraceSpecsEventually(t, otlpPort,
		otlpSpanSpec{Model: "debug-model", Team: "platform", User: "user-1", Prompt: "expensive prompt", Doc: "doc-a", InputTokens: intPtr(100), OutputTokens: intPtr(50)},
		otlpSpanSpec{Model: "debug-model", Team: "platform", User: "user-2", Prompt: "small prompt", Doc: "doc-b", InputTokens: intPtr(5), OutputTokens: intPtr(5)},
	)

	metrics := scrapeEventually(t, promPort, regexp.MustCompile(`(?m)^gen_ai_sketch_requests_total\{[^}]*slice_value="gen_ai\.request\.model=debug-model"[^}]*\}\s+2$`))
	for _, forbidden := range []string{"topk", "rank=", "hash=", "lower_bound", "upper_bound"} {
		if strings.Contains(metrics, forbidden) {
			t.Fatalf("top-k debug field %q leaked into metrics:\n%s", forbidden, metrics)
		}
	}

	logs := waitForOutputEventually(t, output, regexp.MustCompile(`\\"surface\\":\\"genaisketch_topk`))
	for _, want := range []string{`\"hash\":`, `\"estimate\":`, `\"lower_bound\":`, `\"upper_bound\":`, `\"rank\":1`} {
		if !strings.Contains(logs, want) {
			t.Fatalf("top-k debug payload missing %s:\n%s", want, logs)
		}
	}
}

func TestRestartStability(t *testing.T) {
	repo := repoRoot(t)
	binary := filepath.Join(repo, "dist", "otelcol-genai-sketches")
	requireExecutable(t, binary)

	first := runRestartCorpus(t, binary)
	second := runRestartCorpus(t, binary)

	for metric, firstValue := range first {
		secondValue, ok := second[metric]
		if !ok {
			t.Fatalf("second run missing metric %s", metric)
		}
		if firstValue != secondValue {
			t.Fatalf("%s changed across restart with same secret: first=%f second=%f", metric, firstValue, secondValue)
		}
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Clean(filepath.Join(wd, ".."))
}

func requireExecutable(t *testing.T, path string) {
	t.Helper()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("collector binary %s is missing; run make dist first: %v", path, err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("collector binary %s is not executable", path)
	}
}

func writeCollectorConfig(t *testing.T, otlpPort int, promPort int) string {
	t.Helper()

	config := fmt.Sprintf(`
receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 127.0.0.1:%d
processors:
  batch:
    timeout: 100ms
connectors:
  genaisketch:
    window_duration: 60s
    retention_windows: 10
    max_slices: 2000
    topk: 20
    profiles: {hllpp: small, frequent_items: small, bloom: micro}
    hashing: {algo: hmac_sha256_64, secret_env: GENAI_SKETCH_SECRET}
    mcp: {enabled: true, tool_errors: {enabled: true}}
    slices:
      - {name: by_model, keys: ["gen_ai.request.model"], from_resource_attributes: ["gen_ai.request.model"]}
    fields:
      user_key: {from_attributes: ["enduser.id", "user.id"], from_resource_attributes: ["enduser.id", "user.id"], canonicalization: text_v1, domain: "user:v1"}
      prompt_key: {from_attributes: ["gen_ai.request.prompt"], from_resource_attributes: ["gen_ai.request.prompt"], canonicalization: text_v1, domain: "prompt:v1"}
      doc_key: {from_attributes: ["retrieval.doc_id"], from_resource_attributes: ["retrieval.doc_id"], canonicalization: text_v1, domain: "retrieval-doc:v1"}
      mcp_session_key: {from_attributes: ["mcp.session.id"], from_resource_attributes: ["mcp.session.id"], canonicalization: text_v1, domain: "mcp-session:v1"}
      mcp_method_key: {from_attributes: ["mcp.method.name"], from_resource_attributes: ["mcp.method.name"], canonicalization: text_v1, domain: "mcp-method:v1"}
      mcp_resource_key: {from_attributes: ["mcp.resource.uri"], from_resource_attributes: ["mcp.resource.uri"], canonicalization: text_v1, domain: "retrieval-doc:v1"}
    weights:
      input_tokens_from: ["gen_ai.usage.input_tokens"]
      output_tokens_from: ["gen_ai.usage.output_tokens"]
      fallback_when_missing: request_count_only
    dedup: {enabled: false, request_id_from: ["gen_ai.response.id", "request.id"]}
exporters:
  prometheus:
    endpoint: 127.0.0.1:%d
    translation_strategy: UnderscoreEscapingWithoutSuffixes
service:
  pipelines:
    traces:
      receivers: [otlp]
      processors: [batch]
      exporters: [genaisketch]
    metrics:
      receivers: [genaisketch]
      exporters: [prometheus]
`, otlpPort, promPort)

	path := filepath.Join(t.TempDir(), "collector.yaml")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func startCollector(t *testing.T, ctx context.Context, binary string, configPath string) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()

	cmd := exec.CommandContext(ctx, binary, "--config", configPath)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	cmd.Env = append(os.Environ(), "GENAI_SKETCH_SECRET="+testSketchSecret)

	if err := cmd.Start(); err != nil {
		t.Fatalf("start collector: %v", err)
	}

	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		done := make(chan error, 1)
		go func() {
			done <- cmd.Wait()
		}()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	})

	return cmd, &output
}

func waitForProcessAlive(t *testing.T, cmd *exec.Cmd, output *bytes.Buffer) {
	t.Helper()

	time.Sleep(250 * time.Millisecond)
	if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
		t.Fatalf("collector exited early: %s", output.String())
	}
}

func sendTracesEventually(t *testing.T, port int, count int) {
	t.Helper()

	specs := make([]otlpSpanSpec, 0, count)
	for i := 0; i < count; i++ {
		specs = append(specs, otlpSpanSpec{Model: "test-model", Team: "integration"})
	}
	sendTraceSpecsEventually(t, port, specs...)
}

type otlpSpanSpec struct {
	Operation      string
	Model          string
	Team           string
	User           string
	Prompt         string
	Doc            string
	InputTokens    *int64
	OutputTokens   *int64
	MCPMethod      string
	MCPSession     string
	MCPResourceURI string
	Tool           string
	ErrorType      string
}

func sendTraceSpecsEventually(t *testing.T, port int, specs ...otlpSpanSpec) {
	t.Helper()

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	eventually(t, 15*time.Second, func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		conn, err := grpc.DialContext(ctx, addr, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
		if err != nil {
			return err
		}
		defer conn.Close()

		client := tracecollectorpb.NewTraceServiceClient(conn)
		_, err = client.Export(ctx, traceRequest(specs...))
		return err
	})
}

func traceRequest(specs ...otlpSpanSpec) *tracecollectorpb.ExportTraceServiceRequest {
	spans := make([]*tracepb.Span, 0, len(specs))
	now := uint64(time.Now().UnixNano())
	for i, spec := range specs {
		traceID := bytes.Repeat([]byte{byte(i + 1)}, 16)
		spanID := bytes.Repeat([]byte{byte(i + 1)}, 8)
		attrs := []*commonpb.KeyValue{
			stringKV("gen_ai.request.model", spec.Model),
		}
		if spec.Operation != "" {
			attrs = append(attrs, stringKV("gen_ai.operation.name", spec.Operation))
		}
		if spec.Team != "" {
			attrs = append(attrs, stringKV("team.id", spec.Team))
		}
		if spec.User != "" {
			attrs = append(attrs, stringKV("enduser.id", spec.User))
		}
		if spec.Prompt != "" {
			attrs = append(attrs, stringKV("gen_ai.request.prompt", spec.Prompt))
		}
		if spec.Doc != "" {
			attrs = append(attrs, stringKV("retrieval.doc_id", spec.Doc))
		}
		if spec.InputTokens != nil {
			attrs = append(attrs, intKV("gen_ai.usage.input_tokens", *spec.InputTokens))
		}
		if spec.OutputTokens != nil {
			attrs = append(attrs, intKV("gen_ai.usage.output_tokens", *spec.OutputTokens))
		}
		if spec.MCPMethod != "" {
			attrs = append(attrs, stringKV("mcp.method.name", spec.MCPMethod))
		}
		if spec.MCPSession != "" {
			attrs = append(attrs, stringKV("mcp.session.id", spec.MCPSession))
		}
		if spec.MCPResourceURI != "" {
			attrs = append(attrs, stringKV("mcp.resource.uri", spec.MCPResourceURI))
		}
		if spec.Tool != "" {
			attrs = append(attrs, stringKV("gen_ai.tool.name", spec.Tool))
		}
		if spec.ErrorType != "" {
			attrs = append(attrs, stringKV("error.type", spec.ErrorType))
		}
		spans = append(spans, &tracepb.Span{
			TraceId:           traceID,
			SpanId:            spanID,
			Name:              "genai.request",
			Kind:              tracepb.Span_SPAN_KIND_CLIENT,
			StartTimeUnixNano: now,
			EndTimeUnixNano:   now + uint64(time.Millisecond),
			Attributes:        attrs,
		})
	}

	return &tracecollectorpb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{
			{
				Resource: &resourcepb.Resource{
					Attributes: []*commonpb.KeyValue{
						stringKV("service.name", "genaisketch-integration"),
					},
				},
				ScopeSpans: []*tracepb.ScopeSpans{
					{Spans: spans},
				},
			},
		},
	}
}

func stringKV(key string, value string) *commonpb.KeyValue {
	return &commonpb.KeyValue{
		Key: key,
		Value: &commonpb.AnyValue{
			Value: &commonpb.AnyValue_StringValue{StringValue: value},
		},
	}
}

func intKV(key string, value int64) *commonpb.KeyValue {
	return &commonpb.KeyValue{
		Key: key,
		Value: &commonpb.AnyValue{
			Value: &commonpb.AnyValue_IntValue{IntValue: value},
		},
	}
}

func scrapeEventually(t *testing.T, port int, pattern *regexp.Regexp) string {
	t.Helper()

	url := fmt.Sprintf("http://127.0.0.1:%d/metrics", port)
	var body string
	eventually(t, 15*time.Second, func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		body = string(data)
		if !pattern.MatchString(body) {
			return fmt.Errorf("metric pattern not found in scrape body:\n%s", body)
		}
		return nil
	})
	return body
}

func eventually(t *testing.T, timeout time.Duration, fn func() error) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := fn(); err != nil {
			lastErr = err
			time.Sleep(200 * time.Millisecond)
			continue
		}
		return
	}
	t.Fatalf("condition not met within %s: %v", timeout, lastErr)
}

func waitForOutputEventually(t *testing.T, output *bytes.Buffer, pattern *regexp.Regexp) string {
	t.Helper()

	var body string
	eventually(t, 15*time.Second, func() error {
		body = output.String()
		if !pattern.MatchString(body) {
			return fmt.Errorf("output pattern %q not found in:\n%s", pattern.String(), body)
		}
		return nil
	})
	return body
}

func freePort(t *testing.T) int {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on free port: %v", err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func runRestartCorpus(t *testing.T, binary string) map[string]float64 {
	t.Helper()

	otlpPort := freePort(t)
	promPort := freePort(t)
	configPath := writeCollectorConfig(t, otlpPort, promPort)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd, output := startCollector(t, ctx, binary, configPath)
	waitForProcessAlive(t, cmd, output)

	sendTraceSpecsEventually(t, otlpPort,
		otlpSpanSpec{Model: "restart-model", Team: "platform", User: "user-1", Prompt: "prompt-a", Doc: "doc-a", InputTokens: intPtr(11), OutputTokens: intPtr(13)},
		otlpSpanSpec{Model: "restart-model", Team: "platform", User: "user-2", Prompt: "prompt-b", Doc: "doc-b", InputTokens: intPtr(17), OutputTokens: intPtr(19)},
	)

	labels := map[string]string{
		"slice":       "by_model",
		"slice_value": "gen_ai.request.model=restart-model",
		"overflow":    "false",
	}
	metrics := scrapeEventually(t, promPort, regexp.MustCompile(`(?m)^gen_ai_sketch_distinct_users\{[^}]*slice_value="gen_ai\.request\.model=restart-model"[^}]*\}\s+`))

	return map[string]float64{
		"gen_ai_sketch_distinct_users":             mustMetricValue(t, metrics, "gen_ai_sketch_distinct_users", labels),
		"gen_ai_sketch_distinct_prompt_signatures": mustMetricValue(t, metrics, "gen_ai_sketch_distinct_prompt_signatures", labels),
		"gen_ai_sketch_distinct_retrieval_docs":    mustMetricValue(t, metrics, "gen_ai_sketch_distinct_retrieval_docs", labels),
		"gen_ai_sketch_input_tokens_total":         mustMetricValue(t, metrics, "gen_ai_sketch_input_tokens_total", labels),
		"gen_ai_sketch_output_tokens_total":        mustMetricValue(t, metrics, "gen_ai_sketch_output_tokens_total", labels),
		"gen_ai_sketch_missing_token_usage_total":  mustMetricValue(t, metrics, "gen_ai_sketch_missing_token_usage_total", labels),
	}
}

func assertMetricValue(t *testing.T, body string, name string, labels map[string]string, want float64) {
	t.Helper()

	got := mustMetricValue(t, body, name, labels)
	if got != want {
		t.Fatalf("%s = %f, want %f", name, got, want)
	}
}

func assertMetricWithin(t *testing.T, body string, name string, labels map[string]string, min float64, max float64) {
	t.Helper()

	got := mustMetricValue(t, body, name, labels)
	if got < min || got > max {
		t.Fatalf("%s = %f, want within [%f, %f]", name, got, min, max)
	}
}

func mustMetricValue(t *testing.T, body string, name string, labels map[string]string) float64 {
	t.Helper()

	if got, ok := metricValue(t, body, name, labels); ok {
		return got
	}
	t.Fatalf("metric %s with labels %#v not found in:\n%s", name, labels, body)
	return 0
}

func intPtr(value int64) *int64 {
	return &value
}
