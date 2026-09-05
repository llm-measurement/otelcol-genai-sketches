// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay Erramilli and Codex
package main

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestUpdateAllCollectorComponents(t *testing.T) {
	data, err := os.ReadFile("../../builder.yaml")
	if err != nil {
		t.Fatal(err)
	}
	version, err := os.ReadFile("../../otel.version")
	if err != nil {
		t.Fatal(err)
	}
	current := strings.TrimSpace(string(version))
	updated, err := updateManifest(data, "v0.999.0")
	if err != nil {
		t.Fatal(err)
	}
	var before, after map[string]any
	if err := yaml.Unmarshal(data, &before); err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(updated, &after); err != nil {
		t.Fatal(err)
	}
	if before["dist"].(map[string]any)["otelcol_version"] != strings.TrimPrefix(current, "v") {
		t.Fatal("builder.yaml and otel.version disagree")
	}
	for _, section := range []string{"receivers", "processors", "extensions", "exporters"} {
		original := before[section].([]any)
		for i, entry := range after[section].([]any) {
			old := original[i].(map[string]any)["gomod"].(string)
			if !strings.HasSuffix(old, " "+current) {
				t.Fatalf("component version differs from otel.version: %s", old)
			}
			want := strings.TrimSuffix(old, current) + "v0.999.0"
			if got := entry.(map[string]any)["gomod"]; got != want {
				t.Fatalf("component = %v, want %s", got, want)
			}
		}
	}
	for _, preserved := range []string{
		"# SPDX-License-Identifier: Apache-2.0",
		"path: ./connector/genaisketchconnector",
		"genaisketchconnector v0.0.0",
	} {
		if !strings.Contains(string(updated), preserved) {
			t.Fatalf("update lost %q", preserved)
		}
	}
	if !reflect.DeepEqual(before["replaces"], after["replaces"]) {
		t.Fatal("update changed dependency replacements")
	}
}

func TestUpdatePreservesOtherModules(t *testing.T) {
	data := []byte(`dist: {otelcol_version: 0.160.0}
processors:
  - gomod: go.opentelemetry.io/collector/processor/newprocessor v0.160.0
  - gomod: go.opentelemetry.io/collector/processor/stableprocessor v1.2.0
  - gomod: example.com/external/processor v0.160.0
`)
	updated, err := updateManifest(data, "v0.161.0")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"newprocessor v0.161.0", "stableprocessor v1.2.0", "example.com/external/processor v0.160.0"} {
		if !strings.Contains(string(updated), want) {
			t.Fatalf("updated manifest missing %q", want)
		}
	}
}

func TestUpdateRejectsInvalidInput(t *testing.T) {
	for _, target := range []string{"latest", "v0.161.0-rc.1", "v1.0.0", "v0.1.0\n"} {
		if _, err := updateManifest(nil, target); err == nil {
			t.Fatalf("accepted target %q", target)
		}
	}
	for _, data := range []string{"[", "{}", "dist: {otelcol_version: 0.160.0}"} {
		if _, err := updateManifest([]byte(data), "v0.161.0"); err == nil {
			t.Fatalf("accepted manifest %q", data)
		}
	}
}
