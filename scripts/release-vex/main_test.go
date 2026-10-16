// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func graph() []dependency {
	return []dependency{
		{ImportPath: mainPackage, Name: "main", Imports: []string{"golang.org/x/crypto/ssh"}},
		{ImportPath: "golang.org/x/crypto/ssh", Name: "ssh", Imports: []string{"runtime"}},
		{ImportPath: "runtime", Name: "runtime"},
	}
}

func TestGraphRefusals(t *testing.T) {
	if err := validateGraph(graph()); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func([]dependency) []dependency{
		"empty":      func(p []dependency) []dependency { return nil },
		"root":       func(p []dependency) []dependency { p[0].Name = "notmain"; return p },
		"missing":    func(p []dependency) []dependency { return p[:2] },
		"duplicate":  func(p []dependency) []dependency { return append(p, p[1]) },
		"incomplete": func(p []dependency) []dependency { p[1].Incomplete = true; return p },
		"error":      func(p []dependency) []dependency { p[1].Error = json.RawMessage(`{"Err":"failed"}`); return p },
		"deps_error": func(p []dependency) []dependency {
			p[1].DepsErrors = []json.RawMessage{json.RawMessage(`{}`)}
			return p
		},
		"disconnected":  func(p []dependency) []dependency { return append(p, dependency{ImportPath: "extra", Name: "extra"}) },
		"crypto_absent": func(p []dependency) []dependency { p[0].Imports = nil; return p[:1] },
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateGraph(mutate(graph())); err == nil {
				t.Fatal("accepted invalid graph")
			}
		})
	}
	for _, suffix := range []string{"", "/packet", "/armor", "/clearsign", "/errors", "/elgamal", "/s2k", "/future"} {
		t.Run("openpgp"+suffix, func(t *testing.T) {
			p := graph()
			p[0].Imports = append(p[0].Imports, openpgp+suffix)
			p = append(p, dependency{ImportPath: openpgp + suffix, Name: "openpgp"})
			if err := validateGraph(p); err == nil || !strings.Contains(err.Error(), "refusing not_affected") {
				t.Fatalf("unsafe graph: %v", err)
			}
		})
	}
}

// Synthetic bytes exercise binding checks only; these are not release evidence.
func fixture(t *testing.T) (string, string) {
	t.Helper()
	dir, bins := t.TempDir(), t.TempDir()
	for _, arch := range []string{"amd64", "arm64"} {
		binary := []byte("synthetic test fixture " + arch)
		if err := os.WriteFile(filepath.Join(bins, "collector-linux-"+arch), binary, 0600); err != nil {
			t.Fatal(err)
		}
		e := evidence{Schema: "collector-vex-evidence/v1", Version: "v0.4.0", GOOS: "linux", GOARCH: arch, CGOEnabled: "0", GoVersion: "go1.26.9", CryptoVersion: "v0.58.0", BinarySHA256: sum(binary), Packages: graph(), Files: map[string]string{}}
		for _, name := range []string{"go.mod", "go.sum", "main.go", "components.go", "builder.yaml"} {
			e.Files[name] = sum([]byte("synthetic " + name))
		}
		if err := writeJSON(filepath.Join(dir, "vex-linux-"+arch+".json"), e); err != nil {
			t.Fatal(err)
		}
	}
	return dir, bins
}

func TestGenerateBoundStatement(t *testing.T) {
	dir, bins := fixture(t)
	image := imageName + "@sha256:" + strings.Repeat("a", 64)
	doc, err := generate(dir, bins, image, "v0.4.0", "2026-10-16T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(doc)
	for _, want := range []string{`"@context":"https://openvex.dev/ns/v0.2.0"`, `"version":1`, `"name":"GO-2026-5932"`, `"status":"not_affected"`, `"justification":"vulnerable_code_not_present"`, "module-level warning is retained", "not cover custom builds", "vex-linux-amd64.json", "vex-linux-arm64.json", "sha256%3A" + strings.Repeat("a", 64)} {
		if !strings.Contains(string(data), want) {
			t.Errorf("missing %s", want)
		}
	}
	for _, arch := range []string{"amd64", "arm64"} {
		b, _ := os.ReadFile(filepath.Join(dir, "vex-linux-"+arch+".json"))
		if !strings.Contains(string(data), sum(b)) {
			t.Error("missing evidence hash")
		}
	}
	again, err := generate(dir, bins, image, "v0.4.0", "2026-10-16T00:00:00Z")
	if err != nil || doc["@id"] != again["@id"] {
		t.Fatal("not deterministic")
	}
	changed, err := generate(dir, bins, image, "v0.4.0", "2026-10-17T00:00:00Z")
	if err != nil || changed["@id"] == doc["@id"] {
		t.Fatal("changed snapshot reused document ID")
	}
}

func TestGenerateRejectsInvalidEvidence(t *testing.T) {
	for name, mutate := range map[string]func(*evidence){
		"schema":             func(e *evidence) { e.Schema = "other" },
		"version":            func(e *evidence) { e.Version = "v0.3.3" },
		"os":                 func(e *evidence) { e.GOOS = "darwin" },
		"arch":               func(e *evidence) { e.GOARCH = "amd64" },
		"cgo":                func(e *evidence) { e.CGOEnabled = "1" },
		"toolchain":          func(e *evidence) { e.GoVersion = "" },
		"crypto":             func(e *evidence) { e.CryptoVersion = "" },
		"hash":               func(e *evidence) { e.BinarySHA256 = "bad" },
		"binary_mismatch":    func(e *evidence) { e.BinarySHA256 = strings.Repeat("f", 64) },
		"missing_hash":       func(e *evidence) { delete(e.Files, "go.sum") },
		"empty_graph":        func(e *evidence) { e.Packages = nil },
		"unsafe_second_arch": func(e *evidence) { e.Packages = append(e.Packages, dependency{ImportPath: openpgp, Name: "openpgp"}) },
	} {
		t.Run(name, func(t *testing.T) {
			dir, bins := fixture(t)
			path := filepath.Join(dir, "vex-linux-arm64.json")
			data, _ := os.ReadFile(path)
			var e evidence
			if err := json.Unmarshal(data, &e); err != nil {
				t.Fatal(err)
			}
			mutate(&e)
			if err := writeJSON(path, e); err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(t.TempDir(), "openvex.json")
			err := run([]string{"generate", "--evidence", dir, "--binaries", bins, "--version", "v0.4.0", "--image", imageName + "@sha256:" + strings.Repeat("a", 64), "--timestamp", "2026-10-16T00:00:00Z", "--out", out})
			if err == nil {
				t.Fatal("accepted invalid evidence")
			}
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Fatal("emitted claim on failure")
			}
		})
	}
}

func TestMissingAndMalformedInputs(t *testing.T) {
	for _, name := range []string{"missing_arch", "malformed", "missing_binary", "tag", "digest", "other_image", "timestamp"} {
		t.Run(name, func(t *testing.T) {
			dir, bins := fixture(t)
			version, stamp := "v0.4.0", "2026-10-16T00:00:00Z"
			image := imageName + "@sha256:" + strings.Repeat("a", 64)
			switch name {
			case "missing_arch":
				if err := os.Remove(filepath.Join(dir, "vex-linux-arm64.json")); err != nil {
					t.Fatal(err)
				}
			case "malformed":
				if err := os.WriteFile(filepath.Join(dir, "vex-linux-arm64.json"), []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing_binary":
				if err := os.Remove(filepath.Join(bins, "collector-linux-arm64")); err != nil {
					t.Fatal(err)
				}
			case "tag":
				version = "latest"
			case "digest":
				image = imageName + ":0.4.0"
			case "other_image":
				image = "ghcr.io/other/collector@sha256:" + strings.Repeat("a", 64)
			case "timestamp":
				stamp = "tomorrow"
			}
			if _, err := generate(dir, bins, image, version, stamp); err == nil {
				t.Fatal("accepted invalid inputs")
			}
		})
	}
}

func TestGeneratedVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "main.go")
	if err := os.WriteFile(path, []byte(`package main; var info = component.BuildInfo{Command:"otelcol-genai-sketches",Version:"0.4.0"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkGeneratedVersion(path, "v0.4.0"); err != nil {
		t.Fatal(err)
	}
	if err := checkGeneratedVersion(path, "v0.4.1"); err == nil {
		t.Fatal("accepted wrong release")
	}
	if _, err := collect("missing", "missing", "v0.4.0", "s390x"); err == nil {
		t.Fatal("accepted unsupported platform")
	}
	if _, err := collect("missing", "missing", "latest", "amd64"); err == nil {
		t.Fatal("accepted nonrelease")
	}
}

func TestReleaseWorkflowBindsAndAuthenticatesVEX(t *testing.T) {
	data, err := os.ReadFile("../../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	for _, want := range []string{
		"cp packaging/docker/Dockerfile .cache/vex.Dockerfile", "FROM build AS vex-evidence", "for arch in amd64 arm64; do",
		"--target vex-export", "docker cp", "--binaries .cache/vex-binaries", "--image \"${IMAGE_NAME}@${DIGEST}\"",
		"sbom-index.json openvex.json vex-linux-*.json *.tgz > SHA256SUMS", "            release/openvex.json\n",
		"for subject in release/SHA256SUMS release/sbom-index.json release/openvex.json; do", "--source-ref \"refs/tags/$TAG\" --deny-self-hosted-runners",
		"ignore-unfixed: false", "go -C dist run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...",
	} {
		if !strings.Contains(source, want) {
			t.Errorf("missing release gate: %s", want)
		}
	}
	if strings.Index(source, "go run ./scripts/release-vex generate") > strings.Index(source, " > SHA256SUMS)") {
		t.Fatal("VEX generated after checksums")
	}
	if strings.Contains(source, "--ignorefile") || strings.Contains(source, "--vex") {
		t.Fatal("scanner suppression added")
	}
}
