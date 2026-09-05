//go:build integration && soak

// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay Erramilli and Codex

package integration

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tracecollectorpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	soakWindowDuration = time.Minute
	soakSampleInterval = 10 * time.Second
	soakMaxSlices      = 1000
	soakModelValues    = 1200
	soakMissingEvery   = 10
	soakLogBytes       = 2 << 20
)

type soakSettings struct {
	Duration             time.Duration
	RatePerSecond        int
	BatchSize            int
	RSSBoundKiB          int64
	MaxRSSSlopeKiBPerMin float64
}

type soakStats struct {
	StartedAt          time.Time
	EndedAt            time.Time
	AttemptedSpans     int64
	AcceptedSpans      int64
	RefusedExports     int64
	CollectorRequests  int64
	MissingTokenSpans  int64
	OverflowSeen       bool
	ExportLatencies    []time.Duration
	RotationLatencies  []time.Duration
	RSSSamples         []rssSample
	RSSMaxKiB          int64
	RSSSlopeKiBPerMin  float64
	TopKSnapshotLines  int
	CollectorPanicSeen bool
	Fleet              fleetBatchStats
}

type rssSample struct {
	At  time.Time
	KiB int64
}

func TestSustainedSoak(t *testing.T) {
	settings := loadSoakSettings(t)
	if settings.BatchSize < soakMaxSlices {
		t.Fatalf("SOAK_BATCH_SIZE=%d must be at least %d so each rotation batch touches all active slices", settings.BatchSize, soakMaxSlices)
	}

	repo := repoRoot(t)
	binaryPath := filepath.Join(repo, "dist", "otelcol-genai-sketches")
	requireExecutable(t, binaryPath)

	otlpPort := freePort(t)
	promPort := freePort(t)
	configPath := writeSoakCollectorConfig(t, otlpPort, promPort)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	output := newSoakOutput(soakLogBytes)
	cmd := startCollectorWithWriter(t, ctx, binaryPath, configPath, output)
	waitForSoakCollectorAlive(t, cmd, output)

	rssCtx, stopRSS := context.WithCancel(context.Background())
	recorder := newRSSRecorder()
	rssDone := sampleRSSUntilDone(rssCtx, cmd.Process.Pid, soakSampleInterval, recorder)

	stats := runSoakTraffic(t, settings, otlpPort)
	stopRSS()
	<-rssDone
	recorder.Sample(cmd.Process.Pid)
	stats.RSSSamples = recorder.Samples()
	stats.RSSMaxKiB = maxRSSKiB(stats.RSSSamples)
	stats.RSSSlopeKiBPerMin = rssSlopeKiBPerMin(stats.RSSSamples, finalRSSWindow(settings.Duration))
	stats.TopKSnapshotLines = output.TopKSnapshotLines()
	stats.CollectorPanicSeen = output.PanicSeen()

	metrics, requests, missing, overflow := waitForSoakMetrics(t, promPort, stats.AcceptedSpans)
	stats.CollectorRequests = requests
	stats.MissingTokenSpans = missing
	stats.OverflowSeen = overflow

	logSoakReport(t, settings, stats)
	assertSoakResults(t, settings, stats, metrics, output.String())
}

func TestFleetSoak(t *testing.T) {
	settings := loadSoakSettings(t)
	if settings.BatchSize%fleetTreeSpans != 0 {
		t.Fatalf("SOAK_BATCH_SIZE=%d must be divisible by %d fleet spans/tree", settings.BatchSize, fleetTreeSpans)
	}
	burstPeriod := fleetBurstPeriod(settings)
	t.Logf("FLEET_SOAK_HEADER duration=%s target_rate=%d_spans/sec batch_size=%d active_tenants=%d overflow_tenants=%d max_slices=%d",
		settings.Duration, settings.RatePerSecond, settings.BatchSize, fleetActiveTenants, fleetOverflowTenants, soakMaxSlices)
	t.Logf("FLEET_GENERATOR span_mix=llm:30%%,agent:20%%,tool:20%%,retrieval:10%%,mcp:10%%,workflow:10%% depth=%d..%d_spans fanout=mixed burst=%d_batches/%s identity=resource tenant_skew=zipf(s=1.2) missing_usage=10%%_of_llm",
		fleetMinDepth, fleetMaxDepth, fleetBurstBatches, burstPeriod)

	repo := repoRoot(t)
	binaryPath := filepath.Join(repo, "dist", "otelcol-genai-sketches")
	requireExecutable(t, binaryPath)
	otlpPort := freePort(t)
	promPort := freePort(t)
	configPath := writeFleetSoakCollectorConfig(t, otlpPort, promPort)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output := newSoakOutput(soakLogBytes)
	cmd := startCollectorWithWriter(t, ctx, binaryPath, configPath, output)
	waitForSoakCollectorAlive(t, cmd, output)

	rssCtx, stopRSS := context.WithCancel(context.Background())
	recorder := newRSSRecorder()
	rssDone := sampleRSSUntilDone(rssCtx, cmd.Process.Pid, soakSampleInterval, recorder)

	stats := runFleetSoakTraffic(t, settings, otlpPort)
	stopRSS()
	<-rssDone
	recorder.Sample(cmd.Process.Pid)
	stats.RSSSamples = recorder.Samples()
	stats.RSSMaxKiB = maxRSSKiB(stats.RSSSamples)
	stats.RSSSlopeKiBPerMin = rssSlopeKiBPerMin(stats.RSSSamples, finalRSSWindow(settings.Duration))
	stats.TopKSnapshotLines = output.TopKSnapshotLines()
	stats.CollectorPanicSeen = output.PanicSeen()

	metrics, requests, missing, overflow := waitForSoakMetrics(t, promPort, stats.Fleet.LLMSpans)
	stats.CollectorRequests = requests
	stats.MissingTokenSpans = missing
	stats.OverflowSeen = overflow

	logFleetSoakReport(t, settings, stats)
	assertFleetSoakResults(t, settings, stats, metrics, output.String())
}

func loadSoakSettings(t *testing.T) soakSettings {
	t.Helper()

	return soakSettings{
		Duration:             envDuration(t, "SOAK_DURATION", 60*time.Minute),
		RatePerSecond:        envInt(t, "SOAK_RATE_PER_SEC", 10_000),
		BatchSize:            envInt(t, "SOAK_BATCH_SIZE", 1_000),
		RSSBoundKiB:          int64(envInt(t, "SOAK_RSS_BOUND_MIB", 1024)) * 1024,
		MaxRSSSlopeKiBPerMin: float64(envInt(t, "SOAK_RSS_MAX_SLOPE_KIB_PER_MIN", 1024)),
	}
}

func envDuration(t *testing.T, key string, fallback time.Duration) time.Duration {
	t.Helper()

	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		t.Fatalf("%s=%q is not a duration: %v", key, value, err)
	}
	return duration
}

func envInt(t *testing.T, key string, fallback int) int {
	t.Helper()

	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		t.Fatalf("%s=%q is not an integer: %v", key, value, err)
	}
	if parsed <= 0 {
		t.Fatalf("%s must be greater than zero, got %d", key, parsed)
	}
	return parsed
}

func writeSoakCollectorConfig(t *testing.T, otlpPort int, promPort int) string {
	t.Helper()
	return writeSoakCollectorConfigForSlice(t, otlpPort, promPort, "by_model", "gen_ai.request.model")
}

func writeFleetSoakCollectorConfig(t *testing.T, otlpPort int, promPort int) string {
	t.Helper()
	return writeSoakCollectorConfigForSlice(t, otlpPort, promPort, "by_tenant", "tenant.id")
}

func writeSoakCollectorConfigForSlice(t *testing.T, otlpPort int, promPort int, sliceName string, sliceKey string) string {
	t.Helper()

	config := fmt.Sprintf(`
receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 127.0.0.1:%d
connectors:
  genaisketch:
    window_duration: 60s
    retention_windows: 10
    max_slices: 1000
    topk: 20
    profiles: {hllpp: small, frequent_items: small, bloom: micro}
    hashing: {algo: hmac_sha256_64, secret_env: GENAI_SKETCH_SECRET}
    operation_filter:
      llm_operations: [chat, generate_content, text_completion, embeddings]
    mcp: {enabled: false, tool_errors: {enabled: false}}
    slices:
      - {name: %s, keys: ["%s"], from_resource_attributes: ["%s"]}
    fields:
      user_key: {from_attributes: ["enduser.id", "user.id"], from_resource_attributes: ["enduser.id", "user.id"], canonicalization: text_v1, domain: "user:v1"}
      prompt_key: {from_attributes: ["gen_ai.request.prompt"], from_resource_attributes: ["gen_ai.request.prompt"], canonicalization: text_v1, domain: "prompt:v1"}
      doc_key: {from_attributes: ["retrieval.doc_id"], from_resource_attributes: ["retrieval.doc_id"], canonicalization: text_v1, domain: "retrieval-doc:v1"}
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
      exporters: [genaisketch]
    metrics:
      receivers: [genaisketch]
      exporters: [prometheus]
`, otlpPort, sliceName, sliceKey, sliceKey, promPort)

	path := filepath.Join(t.TempDir(), "soak-collector.yaml")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatalf("write soak config: %v", err)
	}
	return path
}

func fleetBurstPeriod(settings soakSettings) time.Duration {
	return time.Duration(int64(time.Second) * int64(settings.BatchSize) * fleetBurstBatches / int64(settings.RatePerSecond))
}

func runFleetSoakTraffic(t *testing.T, settings soakSettings, otlpPort int) soakStats {
	t.Helper()

	addr := fmt.Sprintf("127.0.0.1:%d", otlpPort)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := grpc.DialContext(ctx, addr, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
	if err != nil {
		t.Fatalf("dial collector OTLP endpoint: %v", err)
	}
	defer conn.Close()
	client := tracecollectorpb.NewTraceServiceClient(conn)

	burstPeriod := fleetBurstPeriod(settings)
	if burstPeriod <= 0 {
		t.Fatalf("invalid burst period for batch_size=%d rate=%d", settings.BatchSize, settings.RatePerSecond)
	}
	bursts := int(settings.Duration / burstPeriod)
	if bursts <= 0 {
		t.Fatalf("SOAK_DURATION=%s is shorter than one fleet burst %s", settings.Duration, burstPeriod)
	}
	stats := soakStats{
		StartedAt:       time.Now(),
		ExportLatencies: make([]time.Duration, 0, bursts*fleetBurstBatches),
	}
	generator := newFleetGenerator()
	lastWindow := windowStart(stats.StartedAt, soakWindowDuration)
	warmupTrees := fleetActiveTenants
	nextProgress := stats.StartedAt.Add(time.Minute)

	for burst := 0; burst < bursts; burst++ {
		target := stats.StartedAt.Add(time.Duration(burst) * burstPeriod)
		if sleep := time.Until(target); sleep > 0 {
			time.Sleep(sleep)
		}
		for batch := 0; batch < fleetBurstBatches; batch++ {
			now := time.Now()
			currentWindow := windowStart(now, soakWindowDuration)
			isNewWindow := currentWindow != lastWindow
			if isNewWindow {
				warmupTrees = fleetActiveTenants
			}
			batchTrees := settings.BatchSize / fleetTreeSpans
			activeOnly := warmupTrees > 0
			request, expected := generator.batch(settings.BatchSize, activeOnly)
			if activeOnly {
				warmupTrees -= batchTrees
				if warmupTrees < 0 {
					warmupTrees = 0
				}
			}

			exportCtx, exportCancel := context.WithTimeout(context.Background(), 2*time.Second)
			exportStarted := time.Now()
			_, err := client.Export(exportCtx, request)
			elapsed := time.Since(exportStarted)
			exportCancel()

			stats.AttemptedSpans += int64(settings.BatchSize)
			stats.ExportLatencies = append(stats.ExportLatencies, elapsed)
			if err != nil {
				stats.RefusedExports++
			} else {
				stats.AcceptedSpans += int64(settings.BatchSize)
				stats.Fleet.add(expected)
				if isNewWindow {
					stats.RotationLatencies = append(stats.RotationLatencies, elapsed)
				}
			}
			if isNewWindow {
				lastWindow = currentWindow
			}
		}
		if !time.Now().Before(nextProgress) {
			t.Logf("FLEET_SOAK_PROGRESS elapsed=%s accepted=%d llm=%d refused_exports=%d",
				time.Since(stats.StartedAt).Round(time.Second), stats.AcceptedSpans, stats.Fleet.LLMSpans, stats.RefusedExports)
			nextProgress = nextProgress.Add(time.Minute)
		}
	}

	if sleep := time.Until(stats.StartedAt.Add(settings.Duration)); sleep > 0 {
		time.Sleep(sleep)
	}
	stats.EndedAt = time.Now()
	return stats
}

func startCollectorWithWriter(t *testing.T, ctx context.Context, binaryPath string, configPath string, output io.Writer) *exec.Cmd {
	t.Helper()

	cmd := exec.CommandContext(ctx, binaryPath, "--config", configPath)
	cmd.Stdout = output
	cmd.Stderr = output
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

	return cmd
}

func waitForSoakCollectorAlive(t *testing.T, cmd *exec.Cmd, output fmt.Stringer) {
	t.Helper()

	time.Sleep(250 * time.Millisecond)
	if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
		t.Fatalf("collector exited early: %s", output.String())
	}
}

func runSoakTraffic(t *testing.T, settings soakSettings, otlpPort int) soakStats {
	t.Helper()

	addr := fmt.Sprintf("127.0.0.1:%d", otlpPort)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := grpc.DialContext(ctx, addr, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
	if err != nil {
		t.Fatalf("dial collector OTLP endpoint: %v", err)
	}
	defer conn.Close()
	client := tracecollectorpb.NewTraceServiceClient(conn)

	interval := time.Duration(int64(time.Second) * int64(settings.BatchSize) / int64(settings.RatePerSecond))
	if interval <= 0 {
		t.Fatalf("invalid pacing interval for batch_size=%d rate=%d", settings.BatchSize, settings.RatePerSecond)
	}
	batches := int(settings.Duration / interval)
	if batches <= 0 {
		t.Fatalf("SOAK_DURATION=%s is shorter than one pacing interval %s", settings.Duration, interval)
	}

	stats := soakStats{
		StartedAt:       time.Now(),
		ExportLatencies: make([]time.Duration, 0, batches),
	}
	lastWindow := windowStart(stats.StartedAt, soakWindowDuration)
	var spanStart int64

	for batch := 0; batch < batches; batch++ {
		target := stats.StartedAt.Add(time.Duration(batch) * interval)
		if sleep := time.Until(target); sleep > 0 {
			time.Sleep(sleep)
		}

		now := time.Now()
		currentWindow := windowStart(now, soakWindowDuration)
		isNewWindow := currentWindow != lastWindow
		activeOnly := batch == 0 || isNewWindow
		req := soakTraceRequest(spanStart, settings.BatchSize, activeOnly)

		exportCtx, exportCancel := context.WithTimeout(context.Background(), 2*time.Second)
		exportStarted := time.Now()
		_, err := client.Export(exportCtx, req)
		elapsed := time.Since(exportStarted)
		exportCancel()

		stats.AttemptedSpans += int64(settings.BatchSize)
		stats.ExportLatencies = append(stats.ExportLatencies, elapsed)
		if err != nil {
			stats.RefusedExports++
		} else {
			stats.AcceptedSpans += int64(settings.BatchSize)
			if isNewWindow && batch > 0 {
				stats.RotationLatencies = append(stats.RotationLatencies, elapsed)
			}
		}
		if isNewWindow {
			lastWindow = currentWindow
		}
		spanStart += int64(settings.BatchSize)
	}

	if sleep := time.Until(stats.StartedAt.Add(settings.Duration)); sleep > 0 {
		time.Sleep(sleep)
	}
	stats.EndedAt = time.Now()
	return stats
}

func soakTraceRequest(start int64, count int, activeOnly bool) *tracecollectorpb.ExportTraceServiceRequest {
	spans := make([]*tracepb.Span, 0, count)
	now := uint64(time.Now().UnixNano())
	for i := 0; i < count; i++ {
		index := start + int64(i)
		modelIndex := int(index % soakModelValues)
		if activeOnly {
			modelIndex = int(index % soakMaxSlices)
		}
		attrs := []*commonpb.KeyValue{
			stringKV("gen_ai.request.model", fmt.Sprintf("model-%04d", modelIndex)),
			stringKV("team.id", fmt.Sprintf("team-%02d", index%50)),
			stringKV("enduser.id", fmt.Sprintf("user-%06d", index%5_000)),
			stringKV("gen_ai.request.prompt", fmt.Sprintf("soak prompt #%03d", zipfSuffix(index))),
			stringKV("retrieval.doc_id", fmt.Sprintf("doc-%05d", index%8_000)),
			stringKV("request.id", fmt.Sprintf("soak-request-%012d", index)),
		}
		if index%soakMissingEvery != 0 {
			attrs = append(attrs,
				intKV("gen_ai.usage.input_tokens", int64(80+index%900)),
				intKV("gen_ai.usage.output_tokens", int64(20+index%450)),
			)
		}
		spans = append(spans, &tracepb.Span{
			TraceId:           fixedID(index, 16),
			SpanId:            fixedID(index, 8),
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
						stringKV("service.name", "genaisketch-soak"),
					},
				},
				ScopeSpans: []*tracepb.ScopeSpans{
					{Spans: spans},
				},
			},
		},
	}
}

func zipfSuffix(index int64) int {
	bucket := index % 100
	switch {
	case bucket < 45:
		return int(index%10) + 1
	case bucket < 75:
		return int(index%40) + 11
	case bucket < 90:
		return int(index%100) + 51
	default:
		return int(index%250) + 151
	}
}

func waitForSoakMetrics(t *testing.T, promPort int, wantRequests int64) (string, int64, int64, bool) {
	t.Helper()

	url := fmt.Sprintf("http://127.0.0.1:%d/metrics", promPort)
	var body string
	var requests int64
	var missing int64
	var overflow bool
	eventuallyWithTimeout(t, time.Minute, func() error {
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
		requests = sumPrometheusMetric(t, body, "gen_ai_sketch_requests_total")
		missing = sumPrometheusMetric(t, body, "gen_ai_sketch_missing_token_usage_total")
		value, ok := metricValue(t, body, "gen_ai_sketch_requests_total", map[string]string{"overflow": "true", "slice_value": "__overflow__"})
		overflow = ok && value > 0
		if requests < wantRequests {
			return fmt.Errorf("collector has exported %d requests, want %d", requests, wantRequests)
		}
		return nil
	})
	return body, requests, missing, overflow
}

func assertSoakResults(t *testing.T, settings soakSettings, stats soakStats, metrics string, logs string) {
	t.Helper()

	if stats.RefusedExports != 0 {
		t.Fatalf("refused exports = %d, want 0", stats.RefusedExports)
	}
	if stats.CollectorPanicSeen || strings.Contains(strings.ToLower(logs), "panic") {
		t.Fatalf("collector panic detected in logs:\n%s", logs)
	}
	if stats.CollectorRequests != stats.AcceptedSpans {
		t.Fatalf("collector requests = %d, accepted spans = %d\n%s", stats.CollectorRequests, stats.AcceptedSpans, metrics)
	}
	if stats.MissingTokenSpans == 0 {
		t.Fatalf("missing-token metric stayed at zero:\n%s", metrics)
	}
	if !stats.OverflowSeen {
		t.Fatalf("overflow slice was not observed in metrics:\n%s", metrics)
	}
	if stats.RSSMaxKiB > settings.RSSBoundKiB {
		t.Fatalf("max RSS %.1f MiB exceeds bound %.1f MiB", float64(stats.RSSMaxKiB)/1024, float64(settings.RSSBoundKiB)/1024)
	}
	if settings.Duration >= 60*time.Minute && math.Abs(stats.RSSSlopeKiBPerMin) > settings.MaxRSSSlopeKiBPerMin {
		t.Fatalf("final-window RSS slope %.1f KiB/min exceeds bound %.1f KiB/min", stats.RSSSlopeKiBPerMin, settings.MaxRSSSlopeKiBPerMin)
	}
	if settings.Duration >= 2*soakWindowDuration && len(stats.RotationLatencies) == 0 {
		t.Fatal("no window rotations were observed during the soak")
	}
	if len(stats.RotationLatencies) > 0 && percentileDuration(stats.RotationLatencies, 0.99) > 100*time.Millisecond {
		t.Fatalf("window rotation p99 = %s, want <= 100ms", percentileDuration(stats.RotationLatencies, 0.99))
	}
	if stats.TopKSnapshotLines == 0 {
		t.Fatal("no top-k snapshot logs were observed during the soak")
	}
}

func assertFleetSoakResults(t *testing.T, settings soakSettings, stats soakStats, metrics string, logs string) {
	t.Helper()

	if stats.RefusedExports != 0 {
		t.Fatalf("refused exports = %d, want 0", stats.RefusedExports)
	}
	if stats.CollectorPanicSeen || strings.Contains(strings.ToLower(logs), "panic") {
		t.Fatalf("collector panic detected in logs:\n%s", logs)
	}
	if stats.CollectorRequests != stats.Fleet.LLMSpans {
		t.Fatalf("collector requests = %d, emitted LLM spans = %d\n%s", stats.CollectorRequests, stats.Fleet.LLMSpans, metrics)
	}
	if stats.MissingTokenSpans != stats.Fleet.MissingLLMSpans {
		t.Fatalf("missing-token metric = %d, planted missing LLM spans = %d\n%s", stats.MissingTokenSpans, stats.Fleet.MissingLLMSpans, metrics)
	}
	if got := sumPrometheusMetric(t, metrics, "gen_ai_sketch_agent_runs_total"); got != stats.Fleet.AgentRoots {
		t.Fatalf("agent runs = %d, emitted root agents = %d\n%s", got, stats.Fleet.AgentRoots, metrics)
	}
	assertFleetTenantAttribution(t, metrics, stats.Fleet)
	if !stats.OverflowSeen {
		t.Fatalf("overflow slice was not observed in metrics:\n%s", metrics)
	}
	if stats.RSSMaxKiB > settings.RSSBoundKiB {
		t.Fatalf("max RSS %.1f MiB exceeds bound %.1f MiB", float64(stats.RSSMaxKiB)/1024, float64(settings.RSSBoundKiB)/1024)
	}
	if settings.Duration >= 60*time.Minute && math.Abs(stats.RSSSlopeKiBPerMin) > settings.MaxRSSSlopeKiBPerMin {
		t.Fatalf("final-window RSS slope %.1f KiB/min exceeds bound %.1f KiB/min", stats.RSSSlopeKiBPerMin, settings.MaxRSSSlopeKiBPerMin)
	}
	if settings.Duration >= 2*soakWindowDuration && len(stats.RotationLatencies) == 0 {
		t.Fatal("no window rotations were observed during the fleet soak")
	}
	if len(stats.RotationLatencies) > 0 && percentileDuration(stats.RotationLatencies, 0.99) > 100*time.Millisecond {
		t.Fatalf("window rotation p99 = %s, want <= 100ms", percentileDuration(stats.RotationLatencies, 0.99))
	}
	if stats.TopKSnapshotLines == 0 {
		t.Fatal("no top-k snapshot logs were observed during the fleet soak")
	}
	if stats.Fleet.MinDepth != fleetMinDepth || stats.Fleet.MaxDepth != fleetMaxDepth {
		t.Fatalf("generated depth range = %d..%d, want %d..%d", stats.Fleet.MinDepth, stats.Fleet.MaxDepth, fleetMinDepth, fleetMaxDepth)
	}
}

func assertFleetTenantAttribution(t *testing.T, metrics string, expected fleetBatchStats) {
	t.Helper()

	var active int64
	var overflow int64
	activeTenants := make(map[string]struct{})
	for _, sample := range prometheusMetricSamples(t, metrics, "gen_ai_sketch_requests_total") {
		if sample.labels["slice"] != "by_tenant" {
			t.Fatalf("request metric routed to unexpected slice %q", sample.labels["slice"])
		}
		if sample.labels["overflow"] == "true" {
			if sample.labels["slice_value"] != "__overflow__" {
				t.Fatalf("overflow request metric has slice_value=%q", sample.labels["slice_value"])
			}
			overflow += int64(math.Round(sample.value))
			continue
		}
		value := sample.labels["slice_value"]
		if !strings.HasPrefix(value, "tenant.id=tenant-") {
			t.Fatalf("request metric did not use resource tenant identity: %q", value)
		}
		index, err := strconv.Atoi(strings.TrimPrefix(value, "tenant.id=tenant-"))
		if err != nil || index < 0 || index >= fleetActiveTenants {
			t.Fatalf("undeclared non-overflow tenant slice %q", value)
		}
		activeTenants[value] = struct{}{}
		active += int64(math.Round(sample.value))
	}
	if active != expected.ActiveLLMSpans {
		t.Fatalf("active tenant requests = %d, planted active LLM spans = %d", active, expected.ActiveLLMSpans)
	}
	if overflow != expected.OverflowLLMSpans {
		t.Fatalf("overflow requests = %d, planted overflow LLM spans = %d", overflow, expected.OverflowLLMSpans)
	}
	if len(activeTenants) != fleetActiveTenants {
		t.Fatalf("active tenant slices = %d, want %d", len(activeTenants), fleetActiveTenants)
	}
}

func logSoakReport(t *testing.T, settings soakSettings, stats soakStats) {
	t.Helper()

	elapsed := stats.EndedAt.Sub(stats.StartedAt)
	t.Logf("SOAK_RESULT duration=%s elapsed=%s target_rate=%d spans/sec attempted=%d accepted=%d refused_exports=%d collector_requests=%d missing_token=%d overflow_seen=%t",
		settings.Duration, elapsed.Round(time.Millisecond), settings.RatePerSecond, stats.AttemptedSpans, stats.AcceptedSpans, stats.RefusedExports, stats.CollectorRequests, stats.MissingTokenSpans, stats.OverflowSeen)
	t.Logf("SOAK_RSS samples=%d max=%.1fMiB bound=%.1fMiB final_window=%s slope=%.1fKiB/min slope_bound=%.1fKiB/min",
		len(stats.RSSSamples), float64(stats.RSSMaxKiB)/1024, float64(settings.RSSBoundKiB)/1024, finalRSSWindow(settings.Duration), stats.RSSSlopeKiBPerMin, settings.MaxRSSSlopeKiBPerMin)
	t.Logf("SOAK_EXPORT_LATENCY count=%d p50=%s p95=%s p99=%s max=%s",
		len(stats.ExportLatencies), percentileDuration(stats.ExportLatencies, 0.50), percentileDuration(stats.ExportLatencies, 0.95), percentileDuration(stats.ExportLatencies, 0.99), maxDuration(stats.ExportLatencies))
	t.Logf("SOAK_ROTATION_LATENCY count=%d p50=%s p95=%s p99=%s max=%s",
		len(stats.RotationLatencies), percentileDuration(stats.RotationLatencies, 0.50), percentileDuration(stats.RotationLatencies, 0.95), percentileDuration(stats.RotationLatencies, 0.99), maxDuration(stats.RotationLatencies))
	t.Logf("SOAK_DEBUG topk_snapshot_lines=%d", stats.TopKSnapshotLines)
}

func logFleetSoakReport(t *testing.T, settings soakSettings, stats soakStats) {
	t.Helper()

	elapsed := stats.EndedAt.Sub(stats.StartedAt)
	t.Logf("FLEET_SOAK_RESULT duration=%s elapsed=%s target_rate=%d spans/sec attempted=%d accepted=%d refused_exports=%d emitted_llm=%d collector_requests=%d planted_missing_llm=%d missing_token=%d active_llm=%d overflow_llm=%d",
		settings.Duration, elapsed.Round(time.Millisecond), settings.RatePerSecond, stats.AttemptedSpans, stats.AcceptedSpans, stats.RefusedExports, stats.Fleet.LLMSpans, stats.CollectorRequests, stats.Fleet.MissingLLMSpans, stats.MissingTokenSpans, stats.Fleet.ActiveLLMSpans, stats.Fleet.OverflowLLMSpans)
	t.Logf("FLEET_SOAK_SHAPE trees=%d depth=%d..%d agent_roots=%d llm_fraction=%.1f%% missing_llm_fraction=%.1f%%",
		stats.Fleet.Trees, stats.Fleet.MinDepth, stats.Fleet.MaxDepth, stats.Fleet.AgentRoots, 100*float64(stats.Fleet.LLMSpans)/float64(stats.Fleet.Spans), 100*float64(stats.Fleet.MissingLLMSpans)/float64(stats.Fleet.LLMSpans))
	t.Logf("FLEET_SOAK_RSS samples=%d max=%.1fMiB bound=%.1fMiB final_window=%s slope=%.1fKiB/min slope_bound=%.1fKiB/min",
		len(stats.RSSSamples), float64(stats.RSSMaxKiB)/1024, float64(settings.RSSBoundKiB)/1024, finalRSSWindow(settings.Duration), stats.RSSSlopeKiBPerMin, settings.MaxRSSSlopeKiBPerMin)
	t.Logf("FLEET_SOAK_EXPORT_LATENCY count=%d p50=%s p95=%s p99=%s max=%s",
		len(stats.ExportLatencies), percentileDuration(stats.ExportLatencies, 0.50), percentileDuration(stats.ExportLatencies, 0.95), percentileDuration(stats.ExportLatencies, 0.99), maxDuration(stats.ExportLatencies))
	t.Logf("FLEET_SOAK_ROTATION_LATENCY count=%d p50=%s p95=%s p99=%s max=%s",
		len(stats.RotationLatencies), percentileDuration(stats.RotationLatencies, 0.50), percentileDuration(stats.RotationLatencies, 0.95), percentileDuration(stats.RotationLatencies, 0.99), maxDuration(stats.RotationLatencies))
	t.Logf("FLEET_SOAK_DEBUG topk_snapshot_lines=%d", stats.TopKSnapshotLines)
}

type soakOutput struct {
	mu                sync.Mutex
	max               int
	buf               []byte
	topKSnapshotLines int
	panicSeen         bool
}

func newSoakOutput(max int) *soakOutput {
	return &soakOutput{max: max}
}

func (o *soakOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	text := string(p)
	o.topKSnapshotLines += strings.Count(text, "genaisketch topk snapshot")
	if strings.Contains(strings.ToLower(text), "panic") {
		o.panicSeen = true
	}
	o.buf = append(o.buf, p...)
	if len(o.buf) > o.max {
		o.buf = append([]byte(nil), o.buf[len(o.buf)-o.max:]...)
	}
	return len(p), nil
}

func (o *soakOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return string(append([]byte(nil), o.buf...))
}

func (o *soakOutput) TopKSnapshotLines() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.topKSnapshotLines
}

func (o *soakOutput) PanicSeen() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.panicSeen
}

type rssRecorder struct {
	mu      sync.Mutex
	samples []rssSample
}

func newRSSRecorder() *rssRecorder {
	return &rssRecorder{}
}

func (r *rssRecorder) Sample(pid int) {
	kiB, err := sampleRSSKiB(pid)
	if err != nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.samples = append(r.samples, rssSample{At: time.Now(), KiB: kiB})
}

func (r *rssRecorder) Samples() []rssSample {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]rssSample(nil), r.samples...)
}

func sampleRSSUntilDone(ctx context.Context, pid int, interval time.Duration, recorder *rssRecorder) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		recorder.Sample(pid)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				recorder.Sample(pid)
			}
		}
	}()
	return done
}

func sampleRSSKiB(pid int) (int64, error) {
	output, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(string(output)), 10, 64)
}

func maxRSSKiB(samples []rssSample) int64 {
	var max int64
	for _, sample := range samples {
		if sample.KiB > max {
			max = sample.KiB
		}
	}
	return max
}

func rssSlopeKiBPerMin(samples []rssSample, window time.Duration) float64 {
	if len(samples) < 2 {
		return 0
	}
	cutoff := samples[len(samples)-1].At.Add(-window)
	filtered := make([]rssSample, 0, len(samples))
	for _, sample := range samples {
		if !sample.At.Before(cutoff) {
			filtered = append(filtered, sample)
		}
	}
	if len(filtered) < 2 {
		return 0
	}

	start := filtered[0].At
	var sumX, sumY, sumXY, sumXX float64
	for _, sample := range filtered {
		x := sample.At.Sub(start).Minutes()
		y := float64(sample.KiB)
		sumX += x
		sumY += y
		sumXY += x * y
		sumXX += x * x
	}
	n := float64(len(filtered))
	denominator := n*sumXX - sumX*sumX
	if denominator == 0 {
		return 0
	}
	return (n*sumXY - sumX*sumY) / denominator
}

func finalRSSWindow(duration time.Duration) time.Duration {
	if duration >= 30*time.Minute {
		return 30 * time.Minute
	}
	if duration <= 2*soakSampleInterval {
		return duration
	}
	return duration / 2
}

func percentileDuration(values []time.Duration, p float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(i int, j int) bool {
		return sorted[i] < sorted[j]
	})
	index := int(math.Ceil(float64(len(sorted))*p)) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}

func maxDuration(values []time.Duration) time.Duration {
	var max time.Duration
	for _, value := range values {
		if value > max {
			max = value
		}
	}
	return max
}

func windowStart(now time.Time, duration time.Duration) int64 {
	return now.UnixNano() / duration.Nanoseconds() * duration.Nanoseconds()
}

func eventuallyWithTimeout(t *testing.T, timeout time.Duration, fn func() error) {
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

var _ fmt.Stringer = (*soakOutput)(nil)
var _ io.Writer = (*soakOutput)(nil)
