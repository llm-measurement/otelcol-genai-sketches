//go:build integration

// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/frequentitems"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
	"go.yaml.in/yaml/v3"
)

func TestSessionTopKPrivateSummaryPipeline(t *testing.T) {
	const windowDuration = 5 * time.Second
	binary := filepath.Join(repoRoot(t), "dist", "otelcol-genai-sketches")
	image := os.Getenv("GENAI_TEST_IMAGE")
	if image == "" {
		requireExecutable(t, binary)
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
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
	c := config["connectors"].(map[string]any)["genaisketch"].(map[string]any)
	c["window_duration"] = windowDuration.String()
	c["retention_windows"] = 10
	c["topk_keys"] = []any{map[string]any{"field": "prompt_key"}, map[string]any{"field": "user_key"}, map[string]any{"field": "session_key", "weight": "requests"}}
	c["summary_export"] = map[string]any{"directory": dir, "producer_id": "app", "scope_id": "app", "key_id": "synthetic", "interval": "1s"}
	config["service"].(map[string]any)["telemetry"] = map[string]any{"metrics": map[string]any{"level": "none"}}
	if image != "" {
		grpc := config["receivers"].(map[string]any)["otlp"].(map[string]any)["protocols"].(map[string]any)["grpc"].(map[string]any)
		grpc["endpoint"] = fmt.Sprintf("0.0.0.0:%d", otlp)
		config["exporters"].(map[string]any)["prometheus"].(map[string]any)["endpoint"] = fmt.Sprintf("0.0.0.0:%d", prom)
	}
	data, err = yaml.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var logs *bytes.Buffer
	if image == "" {
		_, logs = startCollector(t, ctx, binary, path)
	} else {
		logs = startSummaryContainer(t, ctx, image, path, dir, otlp, prom)
	}
	waitForOutputEventually(t, logs, regexp.MustCompile(`started genaisketch connector`))
	documents := make([]summary.Envelope, 0, 2)
	for side := 0; side < 2; side++ {
		spans := make([]otlpSpanSpec, 10)
		for i := range spans {
			session := "PRIVATE_SESSION_background"
			if i < 2+side*6 {
				session = "PRIVATE_SESSION_repeated"
			}
			spans[i] = otlpSpanSpec{Operation: "chat", Model: "demo", User: "PRIVATE_USER", Session: session, Prompt: "PRIVATE_PROMPT", InputTokens: intPtr(2), OutputTokens: intPtr(3)}
			if i%3 == 0 {
				spans[i].InputTokens, spans[i].OutputTokens = nil, nil
			}
		}
		// Export polling can consume the next window. Start each batch in a fresh,
		// fully observed window with room for transport and batch-processor delay.
		start := time.Now().Truncate(windowDuration).Add(windowDuration)
		time.Sleep(time.Until(start.Add(100 * time.Millisecond)))
		sendTraceSpecsEventually(t, otlp, spans...)
		documents = append(documents, waitClosedTopKWindow(t, dir, start.UnixNano()))
	}
	metrics := scrapeEventually(t, prom, regexp.MustCompile(`gen_ai_sketch_requests_total`))
	logText := waitForOutputEventually(t, logs, regexp.MustCompile(`session_key`))
	if !strings.Contains(logText, "genaisketch topk snapshot") {
		t.Fatal("no snapshot inside privacy scan window")
	}
	paths := make([]string, 2)
	for i, doc := range documents {
		encoded, err := doc.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		if doc.Counters["requests"] != 10 || doc.Counters["missing_token_usage"] != 4 {
			t.Fatalf("window %d: requests=%d missing_token_usage=%d; want 10 and 4", i, doc.Counters["requests"], doc.Counters["missing_token_usage"])
		}
		f, err := frequentitems.Parse(doc.Sketches["top_sessions_requests"].Data)
		if err != nil {
			t.Fatal(err)
		}
		if f.TotalWeight() != 10 {
			t.Fatal("missing usage suppressed request-weight sessions")
		}
		users, err := frequentitems.Parse(doc.Sketches["top_users"].Data)
		if err != nil {
			t.Fatal(err)
		}
		userItems, err := users.FrequentItems(frequentitems.NoFalseNegatives)
		if err != nil || users.TotalWeight() != 30 || len(userItems) != 1 || userItems[0].LowerBound != 30 || userItems[0].UpperBound != 30 {
			t.Fatal("user token bounds differ from exact fixture", err)
		}
		for _, sentinel := range []string{"PRIVATE_SESSION", "PRIVATE_USER", "PRIVATE_PROMPT"} {
			for _, surface := range []string{metrics, logText, string(encoded)} {
				if strings.Contains(surface, sentinel) {
					t.Fatal("raw identifier leaked")
				}
			}
			for _, p := range doc.Sketches {
				if bytes.Contains(p.Data, []byte(sentinel)) {
					t.Fatal("raw identifier in sketch payload")
				}
			}
		}
		items, _ := f.FrequentItems(frequentitems.NoFalseNegatives)
		if len(items) != 2 || items[0].LowerBound != 8 {
			t.Fatal("session bounds differ from exact fixture")
		}
		paths[i] = filepath.Join(t.TempDir(), "window.json")
		if err := os.WriteFile(paths[i], encoded, 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Optional cross-repository check; CI still verifies the export above without fleetdiff.
	if fleetdiff := os.Getenv("FLEETDIFF_BIN"); fleetdiff != "" {
		output, err := exec.CommandContext(ctx, fleetdiff, "investigate", "--before", paths[0], "--after", paths[1], "--expected", "app", "--format", "json").CombinedOutput()
		if err != nil {
			t.Fatal("fleetdiff could not investigate collector exports", err, string(output))
		}
		var report struct {
			Questions []struct {
				ID, Status   string
				Contributors []struct{ Flag string }
			}
		}
		if err := json.Unmarshal(output, &report); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, q := range report.Questions {
			if q.ID == "sessions" && q.Status == "observed" {
				for _, candidate := range q.Contributors {
					if candidate.Flag == "runaway_candidate" {
						found = true
					}
				}
			}
		}
		if !found {
			t.Fatal("fleetdiff did not flag a bounded high-share session", string(output))
		}
	} else {
		t.Log("Set FLEETDIFF_BIN to also verify the report across repositories.")
	}
}

func waitClosedTopKWindow(t *testing.T, dir string, start int64) summary.Envelope {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
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
			if doc.WindowStart == start && doc.ObservedStart == start && doc.ObservedEnd == start+doc.WindowDuration {
				return doc
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("no closed session test window")
	return summary.Envelope{}
}
