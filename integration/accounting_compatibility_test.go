//go:build integration

// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package integration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	tracecollectorpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"go.yaml.in/yaml/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestOTLPMalformedIdentityPreservesGoodSpans(t *testing.T) {
	binary := filepath.Join(repoRoot(t), "dist", "otelcol-genai-sketches")
	requireExecutable(t, binary)
	for _, mode := range []string{"default", "summary", "model_usage"} {
		t.Run(mode, func(t *testing.T) {
			otlp, prom := freePort(t), freePort(t)
			path := writeCollectorConfig(t, otlp, prom)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var config map[string]any
			if err := yaml.Unmarshal(data, &config); err != nil {
				t.Fatal(err)
			}
			pipelines := config["service"].(map[string]any)["pipelines"].(map[string]any)
			// Keep the mixed request synchronous, so an asynchronous batch processor
			// cannot hide a connector rejection behind an already successful RPC.
			delete(pipelines["traces"].(map[string]any), "processors")
			connector := config["connectors"].(map[string]any)["genaisketch"].(map[string]any)
			connector["window_duration"] = "24h"
			if mode != "default" {
				dir := t.TempDir()
				if err := os.Chmod(dir, 0700); err != nil {
					t.Fatal(err)
				}
				export := map[string]any{"directory": dir, "producer_id": "producer", "scope_id": "scope", "key_id": "synthetic", "interval": "1s"}
				if mode == "model_usage" {
					export["model_usage"] = map[string]any{"slice": "by_model", "models": map[string]string{"normal-model": "public-model"}}
				}
				connector["summary_export"] = export
			}
			data, err = yaml.Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			startCollector(t, ctx, binary, path)
			dial, stop := context.WithTimeout(ctx, 15*time.Second)
			defer stop()
			connection, err := grpc.DialContext(dial, fmt.Sprintf("127.0.0.1:%d", otlp), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			client := tracecollectorpb.NewTraceServiceClient(connection)
			good := otlpSpanSpec{Operation: "chat", Model: "normal-model", User: "synthetic-user", Prompt: "synthetic-prompt", InputTokens: intPtr(10), OutputTokens: intPtr(2)}
			poison := good
			poison.Prompt = strings.Repeat("x", (8<<10)+1)
			poison.InputTokens, poison.OutputTokens = intPtr(77), intPtr(13)
			batch := make([]otlpSpanSpec, 0, 101)
			for i := 0; i < 100; i++ {
				if i == 50 {
					batch = append(batch, poison)
				}
				batch = append(batch, good)
			}
			// Send once, never retry an Export: retrying would mask prefix commits.
			call, finish := context.WithTimeout(ctx, 5*time.Second)
			_, err = client.Export(call, traceRequest(batch...))
			finish()
			if err != nil {
				t.Fatal("mixed valid/oversized identity request was rejected")
			}
			labels := map[string]string{"slice": "by_model", "slice_value": "gen_ai.request.model=normal-model", "overflow": "false"}
			check := func(count int) {
				t.Helper()
				pattern := regexp.MustCompile(fmt.Sprintf(`(?m)^gen_ai_sketch_requests_total\{[^}]*slice_value="gen_ai\.request\.model=normal-model"[^}]*\}\s+%d$`, count))
				metrics := scrapeEventually(t, prom, pattern)
				assertMetricValue(t, metrics, "gen_ai_sketch_requests_total", labels, float64(count))
				assertMetricValue(t, metrics, "gen_ai_sketch_input_tokens_total", labels, float64(count*10))
				assertMetricValue(t, metrics, "gen_ai_sketch_output_tokens_total", labels, float64(count*2))
				assertMetricValue(t, metrics, "gen_ai_sketch_rejected_spans_total", map[string]string{"reason": "attribute_too_large"}, 1)
				if strings.Contains(metrics, poison.Prompt) || strings.Contains(metrics, good.User) || strings.Contains(metrics, good.Prompt) {
					t.Fatal("raw identity reached exported metrics")
				}
			}
			check(100)
			call, finish = context.WithTimeout(ctx, 5*time.Second)
			_, err = client.Export(call, traceRequest(good))
			finish()
			if err != nil {
				t.Fatal("valid request after oversized identity was rejected")
			}
			check(101)
		})
	}
}
