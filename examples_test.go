// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay Erramilli and Codex
package otelcolgenaisketches

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExampleAppLineBudget(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("examples", "app", "app.py"))
	if err != nil {
		t.Fatalf("read example app: %v", err)
	}
	lines := strings.Count(string(data), "\n")
	if len(data) > 0 && data[len(data)-1] != '\n' {
		lines++
	}
	if lines > 150 {
		t.Fatalf("example app has %d lines, want <= 150", lines)
	}
}

func TestExampleStackArtifactsExist(t *testing.T) {
	for _, path := range []string{
		filepath.Join("examples", "compose.yaml"),
		filepath.Join("examples", "collector", "config.yaml"),
		filepath.Join("packaging", "docker", "Dockerfile"),
		filepath.Join("examples", "prometheus", "prometheus.yml"),
		filepath.Join("examples", "grafana", "dashboards", "genai-sketches.json"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing example artifact %s: %v", path, err)
		}
	}
}
