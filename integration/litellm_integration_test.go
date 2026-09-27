//go:build integration

// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package integration

import (
	"bytes"
	"context"
	"encoding/json"
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

	"go.yaml.in/yaml/v3"
)

func TestLiteLLMRecipeOTLPHTTP(t *testing.T) {
	root := repoRoot(t)
	binary := filepath.Join(root, "dist", "otelcol-genai-sketches")
	requireExecutable(t, binary)
	for i, name := range []string{"before", "after", "subsets"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Chmod(dir, 0700); err != nil {
				t.Fatal(err)
			}
			otlp, prom := freePort(t), freePort(t)
			base := filepath.Join(root, "examples", "integrations", "litellm")
			data, err := os.ReadFile(filepath.Join(base, "collector.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			var cfg map[string]any
			if err := yaml.Unmarshal(data, &cfg); err != nil {
				t.Fatal(err)
			}
			cfg["receivers"].(map[string]any)["otlp"].(map[string]any)["protocols"].(map[string]any)["http"].(map[string]any)["endpoint"] = fmt.Sprintf("127.0.0.1:%d", otlp)
			cfg["exporters"].(map[string]any)["prometheus"].(map[string]any)["endpoint"] = fmt.Sprintf("127.0.0.1:%d", prom)
			connector := cfg["connectors"].(map[string]any)["genaisketch"].(map[string]any)
			connector["window_duration"] = "24h"
			connector["summary_export"] = map[string]any{"directory": dir, "producer_id": "app", "scope_id": "app-investigation", "key_id": "synthetic", "interval": "1s"}
			data, err = yaml.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_, logs := startCollector(t, ctx, binary, path)
			address := fmt.Sprintf("127.0.0.1:%d", otlp)
			for {
				conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
				if err == nil {
					_ = conn.Close()
					break
				}
				if ctx.Err() != nil {
					t.Fatal("collector receiver unavailable")
				}
				time.Sleep(50 * time.Millisecond)
			}
			if name == "subsets" {
				// Actual Python callback output; only its LiteLLM base logger is stubbed.
				data, err = exec.CommandContext(ctx, "python3", filepath.Join(base, "test_provenance_callback.py"), "--subset-otlp").Output()
			} else {
				data, err = os.ReadFile(filepath.Join(base, name+".json"))
			}
			if err != nil {
				t.Fatal(err)
			}
			if name == "after" {
				// Explicit source-boundary declarations, not stock LiteLLM attributes.
				var traces map[string]any
				if err := json.Unmarshal(data, &traces); err != nil {
					t.Fatal(err)
				}
				resource := traces["resourceSpans"].([]any)[0].(map[string]any)
				scope := resource["scopeSpans"].([]any)[0].(map[string]any)
				spans := scope["spans"].([]any)
				for j, source := range []string{"inferred", "unavailable", "PRIVATE_LITELLM_provenance"} {
					span := spans[j+1].(map[string]any)
					span["attributes"] = append(span["attributes"].([]any),
						map[string]any{"key": "gen_ai_sketch.usage.input.provenance", "value": map[string]string{"stringValue": "provider_reported"}},
						map[string]any{"key": "gen_ai_sketch.usage.output.provenance", "value": map[string]string{"stringValue": source}})
				}
				data, err = json.Marshal(traces)
				if err != nil {
					t.Fatal(err)
				}
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+address+"/v1/traces", bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			transport := &http.Transport{Proxy: nil}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			response, err := client.Do(req) // Send once; retrying writes could double-count.
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(io.LimitReader(response.Body, 65537))
			_ = response.Body.Close()
			if err != nil || len(body) > 65536 || response.StatusCode != http.StatusOK {
				t.Fatal("OTLP request rejected")
			}
			var accepted struct {
				Partial json.RawMessage `json:"partialSuccess"`
			}
			if json.Unmarshal(body, &accepted) != nil || len(accepted.Partial) > 0 && string(accepted.Partial) != "{}" {
				t.Fatal("OTLP partial acceptance")
			}
			doc := waitSummary(t, dir, []uint64{2, 3, 8}[i])
			if doc.Counters["input_tokens"] != []uint64{160, 480, 3710}[i] || doc.Counters["output_tokens"] != []uint64{40, 80, 185}[i] || doc.Counters["missing_token_usage"] != []uint64{0, 1, 0}[i] {
				t.Fatal(doc.Counters)
			}
			metrics := scrapeEventually(t, prom, regexp.MustCompile(`gen_ai_sketch_requests_total`))
			if name == "before" {
				if doc.Counters["usage_provenance.v1.input.unknown"] != 2 || doc.Counters["usage_provenance.v1.output.unknown"] != 2 {
					t.Fatal(doc.Counters)
				}
			} else if name == "after" {
				if doc.Counters["usage_provenance.v1.input.provider_reported"] != 3 {
					t.Fatal(doc.Counters)
				}
				for _, source := range []string{"inferred", "unavailable", "unknown"} {
					if doc.Counters["usage_provenance.v1.output."+source] != 1 {
						t.Fatal(doc.Counters)
					}
					pattern := regexp.MustCompile(`gen_ai_sketch_usage_provenance_total\{[^}]*source="` + source + `"[^}]*token_field="output"[^}]*\} 1(?:\n|$)`)
					if !pattern.MatchString(metrics) {
						t.Fatalf("missing fixed provenance series: %s", source)
					}
				}
			} else {
				if doc.Counters["cache_read_input_tokens"] != 3132 || doc.Counters["reasoning_output_tokens"] != 72 {
					t.Fatal("subsets must be exported without adding to totals", doc.Counters)
				}
				for _, field := range []string{"input", "output"} {
					if doc.Counters["usage_provenance.v1."+field+".provider_reported"] != 7 || doc.Counters["usage_provenance.v1."+field+".unknown"] != 1 {
						t.Fatal("changed totals must lose provider provenance", doc.Counters)
					}
				}
				for metric, value := range map[string]int{"cache_read_input_tokens": 3132, "reasoning_output_tokens": 72} {
					pattern := regexp.MustCompile(fmt.Sprintf(`gen_ai_sketch_%s_total\{[^}]*\} %d(?:\n|$)`, metric, value))
					if !pattern.MatchString(metrics) {
						t.Fatalf("missing subset metric: %s", metric)
					}
				}
			}
			logText := waitForOutputEventually(t, logs, regexp.MustCompile(`genaisketch topk snapshot`))
			encoded, err := doc.MarshalBinary()
			if err != nil {
				t.Fatal(err)
			}
			for _, surface := range []string{metrics, logText, string(encoded)} {
				if strings.Contains(surface, "PRIVATE_LITELLM") {
					t.Fatal("sentinel leaked")
				}
			}
			for _, payload := range doc.Sketches {
				if bytes.Contains(payload.Data, []byte("PRIVATE_LITELLM")) {
					t.Fatal("sentinel in sketch bytes")
				}
			}
		})
	}
}
