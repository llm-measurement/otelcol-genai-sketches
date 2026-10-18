// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex
package main

import (
	"encoding/json"
	"testing"
)

func TestChartOCIConfig(t *testing.T) {
	data, err := chartOCIConfig([]byte("apiVersion: v2\nname: example\nversion: 1.2.3\nappVersion: '2.3.4'\nmaintainers:\n  - name: Example\n    email: maintainer@example.invalid\nannotations:\n  artifacthub.io/license: Apache-2.0\ndependencies:\n  - name: child\n    version: 1.0.0\n    repository: https://charts.example.invalid\n    condition: child.enabled\n    tags: [optional]\n"))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got["name"] != "example" || got["version"] != "1.2.3" || got["appVersion"] != "2.3.4" ||
		got["maintainers"].([]any)[0].(map[string]any)["name"] != "Example" ||
		got["maintainers"].([]any)[0].(map[string]any)["email"] != "maintainer@example.invalid" ||
		got["annotations"].(map[string]any)["artifacthub.io/license"] != "Apache-2.0" {
		t.Fatalf("metadata lost in OCI config: %s", data)
	}
	dependency := got["dependencies"].([]any)[0].(map[string]any)
	if dependency["name"] != "child" || dependency["version"] != "1.0.0" || dependency["repository"] != "https://charts.example.invalid" || dependency["condition"] != "child.enabled" || dependency["tags"].([]any)[0] != "optional" {
		t.Fatalf("dependency metadata lost: %s", data)
	}
	for _, invalid := range []string{"", "[]", "apiVersion: v2\nname: example\nversion: 12", "apiVersion: v2\nname: example\nname: duplicate\nversion: 1.2.3"} {
		if _, err := chartOCIConfig([]byte(invalid)); err == nil {
			t.Fatalf("accepted invalid metadata: %q", invalid)
		}
	}
}
