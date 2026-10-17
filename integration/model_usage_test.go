//go:build integration

// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package integration

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/modelusage"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
	tracecollectorpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"go.yaml.in/yaml/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestModelUsageRealOTLP(t *testing.T) {
	binary := filepath.Join(repoRoot(t), "dist", "otelcol-genai-sketches")
	requireExecutable(t, binary)
	dir, otlp, prom := t.TempDir(), freePort(t), freePort(t)
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := writeCollectorConfig(t, otlp, prom)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	c := cfg["connectors"].(map[string]any)["genaisketch"].(map[string]any)
	c["window_duration"] = "24h"
	c["slices"] = []any{map[string]any{"name": "model", "keys": []string{"gen_ai.request.model"}}}
	c["summary_export"] = map[string]any{"directory": dir, "producer_id": "producer", "scope_id": "scope", "key_id": "synthetic", "interval": "1s", "model_usage": map[string]any{"slice": "model", "models": map[string]string{"PRIVATE_MODEL": "public-model"}}}
	data, err = yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, logs := startCollector(t, ctx, binary, path)
	waitForOutputEventually(t, logs, regexp.MustCompile(`started genaisketch connector`))
	empty := waitSummary(t, dir, 0)
	if empty.ModelUsage == nil || empty.ModelUsage.Models == nil || len(empty.ModelUsage.Models) != 0 {
		t.Fatal("idle producer must export a nonnull empty model map")
	}
	request := traceRequest(
		otlpSpanSpec{Operation: "chat", Model: "PRIVATE_MODEL", User: "MODEL_USAGE_PRIVATE_USER", Prompt: "MODEL_USAGE_PRIVATE_PROMPT", InputTokens: intPtr(100000), OutputTokens: intPtr(20)},
		otlpSpanSpec{Operation: "chat", Model: "PRIVATE_MODEL", InputTokens: intPtr(100001), OutputTokens: intPtr(30)},
	)
	first := request.ResourceSpans[0].ScopeSpans[0].Spans[0]
	first.Attributes = append(first.Attributes, intKV("gen_ai.usage.cache_read.input_tokens", 90000), intKV("gen_ai.usage.cache_write.input_tokens", 0))
	// The second attempt intentionally has no cache detail.
	eventually(t, 15*time.Second, func() error {
		call, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		conn, err := grpc.DialContext(call, fmt.Sprintf("127.0.0.1:%d", otlp), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
		if err != nil {
			return err
		}
		defer conn.Close()
		_, err = tracecollectorpb.NewTraceServiceClient(conn).Export(call, request)
		return err
	})
	doc := waitSummary(t, dir, 2)
	if doc.ModelUsage == nil {
		t.Fatal("model usage absent")
	}
	// The expected map comes from this test's controlled configuration, not the
	// incoming summary. The base accounting ID is from our own isolated producer.
	contract, err := modelusage.ContractID(doc.AccountingID, map[string]string{"PRIVATE_MODEL": "public-model"}, modelusage.Routing{
		Algorithm: "inactive-lru-v1", MaxLabelBytes: 256, MaxSlices: c["max_slices"].(int), SelectedSlice: "model",
		Slices: []modelusage.Slice{{Name: "model", Keys: []string{"gen_ai.request.model"}}},
	})
	if err != nil || doc.ModelUsage.ContractID != contract {
		t.Fatal("exported contract differs from the controlled configuration", err)
	}
	if empty.ModelUsage.ContractID != contract {
		t.Fatal("idle export cannot establish the approved configuration contract")
	}
	row := doc.ModelUsage.Models["public-model"]
	if row.Requests != 2 || row.Complete.Requests != 1 || row.Excluded.MissingSplit != 1 || row.Complete.InputTokens != 100000 || row.Complete.CacheReadInputTokens != 90000 || row.Histogram[0].Reported.Requests != 1 || row.Histogram[1].Reported.Requests != 1 {
		t.Fatalf("wrong model split: %+v", row)
	}
	encoded, err := doc.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	for _, sentinel := range []string{"PRIVATE_MODEL", "MODEL_USAGE_PRIVATE_USER", "MODEL_USAGE_PRIVATE_PROMPT"} {
		if bytes.Contains(encoded, []byte(sentinel)) {
			t.Fatal("private sentinel escaped summary")
		}
	}
	result, err := summary.Combine([]summary.Envelope{doc, doc}, []string{"producer"})
	if err != nil || result.Counters["requests"] != 2 || result.ModelUsage.Models["public-model"].Requests != 2 {
		t.Fatal("replayed snapshot doubled totals", err)
	}
}

func TestServedModelUsageRealOTLP(t *testing.T) {
	binary := filepath.Join(repoRoot(t), "dist", "otelcol-genai-sketches")
	requireExecutable(t, binary)
	dir, otlp, prom := t.TempDir(), freePort(t), freePort(t)
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := writeCollectorConfig(t, otlp, prom)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	c := cfg["connectors"].(map[string]any)["genaisketch"].(map[string]any)
	c["window_duration"], c["max_slices"] = "24h", 1
	c["slices"] = []any{map[string]any{"name": "model", "keys": []string{"gen_ai.request.model"}}}
	c["summary_export"] = map[string]any{"directory": dir, "producer_id": "producer", "scope_id": "scope", "key_id": "synthetic", "interval": "1s", "model_usage": map[string]any{
		"slice": "model", "identity": "envoy-ai-gateway-1.1.0-chat-v1",
		"models": map[string]string{"PRIVATE_RESPONSE_A": "model-a", "PRIVATE_RESPONSE_B": "model-b"},
	}}
	data, err = yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, logs := startCollector(t, ctx, binary, path)
	waitForOutputEventually(t, logs, regexp.MustCompile(`started genaisketch connector`))
	idle := waitSummary(t, dir, 0)
	request := traceRequest(
		otlpSpanSpec{Operation: "chat", Model: "shared-alias", InputTokens: intPtr(100), OutputTokens: intPtr(10)},
		otlpSpanSpec{Operation: "chat", Model: "shared-alias", InputTokens: intPtr(100), OutputTokens: intPtr(10)},
		otlpSpanSpec{Operation: "chat", Model: "shared-alias", InputTokens: intPtr(100), OutputTokens: intPtr(10)},
		otlpSpanSpec{Operation: "chat", Model: "shared-alias", InputTokens: intPtr(100), OutputTokens: intPtr(10)},
	)
	spans := request.ResourceSpans[0].ScopeSpans[0].Spans
	for i, response := range []string{"PRIVATE_RESPONSE_A", "PRIVATE_RESPONSE_B", "PRIVATE_RESPONSE_A", "PRIVATE_RESPONSE_UNKNOWN"} {
		spans[i].Attributes = append(spans[i].Attributes, stringKV("gen_ai.response.model", response),
			stringKV("agent.id", "PRIVATE_AGENT"), stringKV("litellm.provider.model", "PRIVATE_DEPLOYMENT"),
			intKV("gen_ai.usage.cache_read.input_tokens", 0), intKV("gen_ai.usage.cache_write.input_tokens", 0))
	}
	call, stop := context.WithTimeout(ctx, 15*time.Second)
	defer stop()
	conn, err := grpc.DialContext(call, fmt.Sprintf("127.0.0.1:%d", otlp), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := tracecollectorpb.NewTraceServiceClient(conn).Export(call, request); err != nil {
		t.Fatal(err)
	}
	doc := waitSummary(t, dir, 4)
	if doc.ModelUsage == nil || idle.ModelUsage == nil || doc.ModelUsage.ContractID != idle.ModelUsage.ContractID {
		t.Fatal("served contract not stable from controlled idle export")
	}
	if doc.ModelUsage.Models["model-a"].Requests != 2 || doc.ModelUsage.Models["__overflow__"].Requests != 1 || doc.ModelUsage.Models["__unmapped__"].Requests != 1 || len(doc.ModelUsage.Models) != 3 {
		t.Fatal("one alias did not retain bounded served cohorts", doc.ModelUsage)
	}
	var requests, inputs uint64
	for _, row := range doc.ModelUsage.Models {
		requests += row.Complete.Requests
		inputs += row.Complete.InputTokens
	}
	if requests != 4 || inputs != 400 {
		t.Fatal("identity exclusion lost recorded usage", requests, inputs)
	}
	encoded, err := doc.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("PRIVATE_")) || bytes.Contains(encoded, []byte("shared-alias")) {
		t.Fatal("raw identity escaped summary")
	}
	if bytes.Contains([]byte(logs.String()), []byte("PRIVATE_")) {
		t.Fatal("new identity escaped collector log")
	}
	result, err := summary.Combine([]summary.Envelope{doc, doc}, []string{"producer"})
	if err != nil || result.Counters["requests"] != 4 {
		t.Fatal("snapshot replay doubled totals", err)
	}
}
