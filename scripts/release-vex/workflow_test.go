// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestReleaseWorkflowSelectsUniquePlatformDigests(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is required for the release workflow selector regression")
	}
	data, err := os.ReadFile("../../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	_, selection, ok := strings.Cut(source, `child_digest="$(jq -er --arg arch "$arch" '`)
	if !ok {
		t.Fatal("missing strict platform selector")
	}
	filter, _, ok := strings.Cut(selection, `' <<<"$image_index")"`)
	if !ok {
		t.Fatal("missing index input to platform selector")
	}
	for _, want := range []string{
		`image_index="$(docker buildx imagetools inspect "${IMAGE_NAME}@${DIGEST}" --raw)"`,
		`child_ref="${IMAGE_NAME}@${child_digest}"`,
		`docker pull --platform "linux/$arch" "$child_ref"`,
		`container="$(docker create --platform "linux/$arch" "$child_ref")"`,
		`--image "${IMAGE_NAME}@${DIGEST}"`,
		`--binaries .cache/vex-binaries`,
	} {
		if !strings.Contains(source, want) {
			t.Errorf("missing digest binding: %s", want)
		}
	}
	for _, command := range []string{"pull", "create"} {
		if strings.Contains(source, `docker `+command+` --platform "linux/$arch" "${IMAGE_NAME}@${DIGEST}"`) {
			t.Fatal("runtime extraction still uses shared index digest")
		}
	}
	manifest := func(osName, arch string, digest any) map[string]any {
		return map[string]any{"platform": map[string]string{"os": osName, "architecture": arch}, "digest": digest}
	}
	amd64, arm64 := "sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64)
	valid := []any{manifest("linux", "amd64", amd64), manifest("linux", "arm64", arm64), manifest("unknown", "unknown", "sha256:"+strings.Repeat("c", 64))}
	for _, tc := range []struct {
		name      string
		arch      string
		manifests any
		want      string
	}{
		{"amd64", "amd64", valid, amd64},
		{"arm64", "arm64", valid, arm64},
		{"missing", "arm64", valid[:1], ""},
		{"duplicate", "amd64", append(append([]any{}, valid...), valid[0]), ""},
		{"wrong_os", "amd64", []any{manifest("windows", "amd64", amd64)}, ""},
		{"null_digest", "amd64", []any{manifest("linux", "amd64", nil)}, ""},
		{"numeric_digest", "amd64", []any{manifest("linux", "amd64", 12)}, ""},
		{"tag_digest", "amd64", []any{manifest("linux", "amd64", "latest")}, ""},
		{"short_digest", "amd64", []any{manifest("linux", "amd64", "sha256:abc")}, ""},
		{"trailing_newline", "amd64", []any{manifest("linux", "amd64", amd64+"\n")}, ""},
		{"uppercase_digest", "amd64", []any{manifest("linux", "amd64", "sha256:"+strings.Repeat("A", 64))}, ""},
		{"non_sha256", "amd64", []any{manifest("linux", "amd64", "sha512:"+strings.Repeat("a", 128))}, ""},
		{"missing_manifests", "amd64", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input, err := json.Marshal(map[string]any{"manifests": tc.manifests})
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("jq", "-er", "--arg", "arch", tc.arch, filter)
			cmd.Stdin = strings.NewReader(string(input))
			output, err := cmd.Output()
			if tc.want == "" {
				if err == nil || len(output) != 0 {
					t.Fatalf("accepted invalid selection: %q, %v", output, err)
				}
			} else if err != nil || strings.TrimSpace(string(output)) != tc.want {
				t.Fatalf("selected %q, %v; want %s", output, err, tc.want)
			}
		})
	}
}
