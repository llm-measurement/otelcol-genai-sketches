//go:build integration

// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay Erramilli and Codex
package integration

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/frequentitems"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/hllpp"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
	"go.yaml.in/yaml/v3"
)

func TestIndependentCollectorsExportMergeablePrivateSummaries(t *testing.T) {
	binary := filepath.Join(repoRoot(t), "dist", "otelcol-genai-sketches")
	requireExecutable(t, binary)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type producer struct {
		id, dir    string
		otlp, prom int
		logs       *bytes.Buffer
	}
	producers := []producer{}
	for _, id := range []string{"platform", "data"} {
		p := producer{id: id, dir: t.TempDir(), otlp: freePort(t), prom: freePort(t)}
		if err := os.Chmod(p.dir, 0700); err != nil {
			t.Fatal(err)
		}
		path := writeCollectorConfig(t, p.otlp, p.prom)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var config map[string]any
		if err := yaml.Unmarshal(data, &config); err != nil {
			t.Fatal(err)
		}
		connector := config["connectors"].(map[string]any)["genaisketch"].(map[string]any)
		connector["window_duration"] = "24h"
		connector["max_slices"] = 1
		connector["summary_export"] = map[string]any{"directory": p.dir, "producer_id": p.id, "scope_id": "independent-systems", "key_id": "integration-key-v1", "interval": "1s"}
		config["service"].(map[string]any)["telemetry"] = map[string]any{"metrics": map[string]any{"level": "none"}}
		data, err = yaml.Marshal(config)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		_, p.logs = startCollector(t, ctx, binary, path)
		waitForOutputEventually(t, p.logs, regexp.MustCompile(`started genaisketch connector`))
		producers = append(producers, p)
	}
	sentinels := []string{"SUMMARY_PRIVATE_USER", "SUMMARY_PRIVATE_PROMPT", "SUMMARY_PRIVATE_DOC", "SUMMARY_PRIVATE_MCP_URI"}
	for _, p := range producers {
		sendTraceSpecsEventually(t, p.otlp,
			otlpSpanSpec{Operation: "chat", Model: "model-a", User: sentinels[0], Prompt: sentinels[1], Doc: sentinels[2], InputTokens: intPtr(5), OutputTokens: intPtr(8)},
			otlpSpanSpec{Operation: "chat", Model: "model-b", User: sentinels[0], Prompt: sentinels[1]},
			otlpSpanSpec{MCPMethod: "resources/read", MCPSession: "SUMMARY_PRIVATE_SESSION", MCPResourceURI: sentinels[3]},
		)
	}
	docs := []summary.Envelope{}
	for _, p := range producers {
		doc := waitSummary(t, p.dir, 2)
		docs = append(docs, doc, doc)
		metrics := scrapeEventually(t, p.prom, regexp.MustCompile(`gen_ai_sketch_requests_total`))
		logs := waitForOutputEventually(t, p.logs, regexp.MustCompile(`genaisketch topk snapshot`))
		encoded, err := doc.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		for _, sentinel := range append(sentinels, "SUMMARY_PRIVATE_SESSION") {
			if strings.Contains(metrics, sentinel) || strings.Contains(logs, sentinel) || bytes.Contains(encoded, []byte(sentinel)) {
				t.Fatal("raw sentinel leaked to exported surface")
			}
			for _, payload := range doc.Sketches {
				if bytes.Contains(payload.Data, []byte(sentinel)) {
					t.Fatal("raw sentinel concealed inside base64 payload")
				}
			}
		}
	}
	result, err := summary.Combine(docs, []string{"platform", "data", "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Counters["requests"] != 4 || result.Counters["input_tokens"] != 10 || result.Counters["output_tokens"] != 16 || result.Counters["missing_token_usage"] != 2 || len(result.Missing) != 1 {
		t.Fatalf("unexpected combined result: %+v", result.Counters)
	}
	h, err := hllpp.Parse(result.Sketches["distinct_users"].Data)
	if err != nil {
		t.Fatal(err)
	}
	if h.Estimate() < 0.99 || h.Estimate() > 1.01 {
		t.Fatal("shared identity did not combine")
	}
	f, err := frequentitems.Parse(result.Sketches["top_prompts"].Data)
	if err != nil {
		t.Fatal(err)
	}
	if f.TotalWeight() != 26 {
		t.Fatalf("weighted summary total=%d want 26", f.TotalWeight())
	}
	changed := docs[0]
	changed.KeyID = "different-key"
	if _, err := summary.Combine([]summary.Envelope{changed, docs[2]}, []string{"platform", "data"}); err == nil {
		t.Fatal("incompatible keys combined")
	}
}

func waitSummary(t *testing.T, dir string, requests uint64) summary.Envelope {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range paths {
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			doc, err := summary.Parse(data)
			if err != nil {
				t.Fatal(err)
			}
			if doc.Counters["requests"] == requests {
				return doc
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("collector did not export a populated summary")
	return summary.Envelope{}
}
