// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex
package main

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) == 3 && os.Args[1] == "--release-chart" {
		data, err := os.ReadFile("deploy/helm/otelcol-genai-sketches/Chart.yaml")
		if err != nil {
			return err
		}
		version, err := releaseChartVersion(data, os.Args[2])
		if err != nil {
			return err
		}
		fmt.Println("chart_version=" + version)
		return nil
	}
	if len(os.Args) == 4 && os.Args[1] == "--distribution" {
		data, err := os.ReadFile("builder.yaml")
		if err != nil {
			return err
		}
		updated, err := distributionManifest(data, os.Args[2])
		if err != nil {
			return err
		}
		previous, err := os.ReadFile(os.Args[3])
		if err == nil && bytes.Equal(previous, updated) {
			return nil
		}
		return os.WriteFile(os.Args[3], updated, 0644)
	}
	if len(os.Args) != 2 {
		return fmt.Errorf("usage: go run ./scripts/collector-version v0.MINOR.PATCH | --distribution VERSION OUTPUT | --release-chart TAG")
	}
	target := os.Args[1]
	data, err := os.ReadFile("builder.yaml")
	if err != nil {
		return err
	}
	updated, err := updateManifest(data, target)
	if err != nil {
		return err
	}
	if err := os.WriteFile("builder.yaml", updated, 0644); err != nil {
		return err
	}
	return os.WriteFile("otel.version", []byte(target+"\n"), 0644)
}

// Chart versions advance independently; appVersion must match the runtime tag.
func releaseChartVersion(data []byte, tag string) (string, error) {
	var chart struct {
		Version    string `yaml:"version"`
		AppVersion string `yaml:"appVersion"`
	}
	if err := yaml.Unmarshal(data, &chart); err != nil {
		return "", err
	}
	valid := regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$`)
	if !valid.MatchString(chart.Version) || !valid.MatchString(chart.AppVersion) || tag != "v"+chart.AppVersion {
		return "", fmt.Errorf("release tag must match chart appVersion; both chart versions must be valid release versions")
	}
	return chart.Version, nil
}

// Generate build-only metadata without changing the checked-in source manifest.
func distributionManifest(data []byte, version string) ([]byte, error) {
	var manifest map[string]any
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return nil, err
	}
	dist, ok := manifest["dist"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("manifest must contain dist")
	}
	if version == "" {
		version, _ = dist["version"].(string)
	}
	version = strings.TrimPrefix(version, "v")
	if !regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$`).MatchString(version) {
		return nil, fmt.Errorf("expected a distribution release or prerelease version, got %q", version)
	}
	dist["version"] = version
	const module = "github.com/llm-measurement/otelcol-genai-sketches/connector/genaisketchconnector"
	connectors, _ := manifest["connectors"].([]any)
	found := false
	for _, entry := range connectors {
		connector, ok := entry.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("connector must be a mapping")
		}
		gomod, _ := connector["gomod"].(string)
		parts := strings.Fields(gomod)
		if len(parts) == 2 && parts[0] == module {
			connector["gomod"] = module + " v" + version
			found = true
		}
	}
	if !found {
		return nil, fmt.Errorf("manifest must contain the genaisketch connector")
	}
	return yaml.Marshal(manifest)
}

func updateManifest(data []byte, target string) ([]byte, error) {
	if !regexp.MustCompile(`^v0\.[0-9]+\.[0-9]+$`).MatchString(target) {
		return nil, fmt.Errorf("expected a stable Collector v0 release, got %q", target)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	var versionFound bool
	var components int
	var visit func(*yaml.Node)
	visit = func(node *yaml.Node) {
		if node.Kind == yaml.MappingNode {
			for i := 0; i < len(node.Content); i += 2 {
				key, value := node.Content[i].Value, node.Content[i+1]
				if key == "otelcol_version" && value.Kind == yaml.ScalarNode {
					value.Value = strings.TrimPrefix(target, "v")
					versionFound = true
				}
				if key == "gomod" && value.Kind == yaml.ScalarNode {
					parts := strings.Fields(value.Value)
					if len(parts) == 2 && strings.HasPrefix(parts[1], "v0.") &&
						(strings.HasPrefix(parts[0], "go.opentelemetry.io/collector/") ||
							strings.HasPrefix(parts[0], "github.com/open-telemetry/opentelemetry-collector-contrib/")) {
						value.Value = parts[0] + " " + target
						components++
					}
				}
			}
		}
		for _, child := range node.Content {
			visit(child)
		}
	}
	visit(&doc)
	if !versionFound || components == 0 {
		return nil, fmt.Errorf("manifest must contain otelcol_version and Collector components")
	}
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(&doc); err != nil {
		return nil, err
	}
	return out.Bytes(), encoder.Close()
}
