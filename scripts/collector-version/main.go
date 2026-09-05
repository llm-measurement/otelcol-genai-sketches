// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay Erramilli and Codex
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
	if len(os.Args) != 2 {
		return fmt.Errorf("usage: go run ./scripts/collector-version v0.MINOR.PATCH")
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
