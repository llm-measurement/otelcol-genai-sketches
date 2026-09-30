// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex
package otelcolgenaisketches

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
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

func TestExampleNetworkBoundaries(t *testing.T) {
	readYAML := func(path string) map[string]any {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var config map[string]any
		if err := yaml.Unmarshal(data, &config); err != nil {
			t.Fatal(err)
		}
		return config
	}
	compose := readYAML("examples/compose.yaml")
	if compose["name"] != "otelcol-genai-sketches-demo" {
		t.Fatal("demo must have an explicit project name")
	}
	for name, raw := range compose["services"].(map[string]any) {
		service := raw.(map[string]any)
		ports, _ := service["ports"].([]any)
		for _, port := range ports {
			if !strings.HasPrefix(port.(string), "127.0.0.1:") {
				t.Fatalf("%s publishes a non-loopback port: %s", name, port)
			}
		}
	}
	for path, host := range map[string]string{
		"examples/integrations/litellm/collector.yaml":           "127.0.0.1",
		"examples/integrations/litellm/collector-container.yaml": "0.0.0.0",
	} {
		config := readYAML(path)
		receivers := config["receivers"].(map[string]any)
		otlp := receivers["otlp"].(map[string]any)
		http := otlp["protocols"].(map[string]any)["http"].(map[string]any)
		prom := config["exporters"].(map[string]any)["prometheus"].(map[string]any)
		if http["endpoint"] != host+":4318" || prom["endpoint"] != host+":8889" {
			t.Fatalf("unexpected endpoints in %s", path)
		}
	}
}

func TestHelmTopKKeys(t *testing.T) {
	helm := os.Getenv("HELM")
	if helm == "" {
		var err error
		helm, err = exec.LookPath("helm")
		if err != nil {
			t.Skip("Helm is optional; set HELM or run make helm-check")
		}
	}
	for _, test := range []struct {
		name, keys string
		valid      bool
	}{
		{"default", "", true},
		{"empty", "[]", true},
		{"selected", `[{"field":"prompt_key"},{"field":"user_key"},{"field":"session_key","weight":"requests"}]`, true},
		{"bad-weight", `[{"field":"session_key","weight":"bytes"}]`, false},
		{"missing-field", `[{"weight":"tokens"}]`, false},
		{"too-many", `[{"field":"a"},{"field":"b"},{"field":"c"},{"field":"d"},{"field":"e"}]`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := []string{"template", "test", "deploy/helm/otelcol-genai-sketches", "--show-only", "templates/configmap.yaml"}
			if test.keys != "" {
				args = append(args, "--set-json", "connector.topKKeys="+test.keys)
			}
			output, err := exec.Command(helm, args...).CombinedOutput()
			if !test.valid {
				if err == nil {
					t.Fatal("invalid keys passed chart schema validation")
				}
				return
			}
			if err != nil {
				t.Fatalf("helm: %v\n%s", err, output)
			}
			var configMap struct {
				Data map[string]string `yaml:"data"`
			}
			if err := yaml.Unmarshal(output, &configMap); err != nil {
				t.Fatal(err)
			}
			var config struct {
				Connectors map[string]map[string]any `yaml:"connectors"`
			}
			if err := yaml.Unmarshal([]byte(configMap.Data["collector.yaml"]), &config); err != nil {
				t.Fatal(err)
			}
			keys, present := config.Connectors["genaisketch"]["topk_keys"]
			if test.name != "selected" {
				if present {
					t.Fatal("default must omit topk_keys")
				}
				return
			}
			entries := keys.([]any)
			if len(entries) != 3 || entries[2].(map[string]any)["weight"] != "requests" {
				t.Fatalf("keys were not passed through: %v", keys)
			}
		})
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
