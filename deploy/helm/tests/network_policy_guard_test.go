// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex
package charttest

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNetworkPolicyFeatureGuards(t *testing.T) {
	for _, tc := range []struct {
		name     string
		settings []string
		missing  string
	}{
		{"shadow", []string{"shadow.enabled=true", "shadow.endpoint=backend:4317"}, "networkPolicy.egress"},
		{"metrics", []string{"serviceMonitor.enabled=true"}, "networkPolicy.metricsFrom"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := render(t, tc.settings...)
			if err == nil || !strings.Contains(string(out), tc.missing) || !strings.Contains(string(out), "docs/DEPLOYMENT.md#chart-070-network-policy-migration") {
				t.Fatalf("missing actionable guard: %v\n%s", err, out)
			}
			settings := append(append([]string{}, tc.settings...), "networkPolicy.enabled=false")
			if out, err := render(t, settings...); err != nil {
				t.Fatalf("explicit policy opt-out refused: %v\n%s", err, out)
			}
		})
	}
	for _, settings := range [][]string{
		{"shadow.enabled=true", "shadow.endpoint=backend:4317", `networkPolicy.egress=[{"ports":[{"port":4317}]}]`},
		{"serviceMonitor.enabled=true", `networkPolicy.metricsFrom=[{"podSelector":{"matchLabels":{"app":"prometheus"}}}]`},
		{"serviceMonitor.enabled=true", `networkPolicy.ingress=[{"ports":[{"port":8889}]}]`},
	} {
		if out, err := render(t, settings...); err != nil {
			t.Fatalf("explicit allowance refused: %v\n%s", err, out)
		}
	}
}

func TestParentChartGlobalValues(t *testing.T) {
	schemaBytes, err := os.ReadFile("../otelcol-genai-sketches/values.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schemaBytes, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Properties["global"].Type != "object" {
		t.Fatal("global schema must accept objects only")
	}
	helm := os.Getenv("HELM")
	if helm == "" {
		var err error
		helm, err = exec.LookPath("helm")
		if err != nil {
			t.Fatal("Helm required for parent-chart regression")
		}
	}
	parent := t.TempDir()
	metadata := "apiVersion: v2\nname: parent\nversion: 0.1.0\ndependencies:\n  - name: otelcol-genai-sketches\n    version: 0.7.0\n"
	if err := os.WriteFile(filepath.Join(parent, "Chart.yaml"), []byte(metadata), 0600); err != nil {
		t.Fatal(err)
	}
	charts := filepath.Join(parent, "charts")
	if err := os.Mkdir(charts, 0700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(helm, "package", "../otelcol-genai-sketches", "--destination", charts).CombinedOutput(); err != nil {
		t.Fatalf("package dependency: %v\n%s", err, out)
	}
	out, err := exec.Command(helm, "template", "parent", parent, "--set", "global.region=test").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "kind: NetworkPolicy") {
		t.Fatalf("global values rejected by child: %v\n%s", err, out)
	}
}
