// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

// release-vex assesses only the standard release image, never arbitrary builds.
package main

import (
	"bytes"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	imageName   = "ghcr.io/llm-measurement/otelcol-genai-sketches"
	project     = "https://github.com/llm-measurement/otelcol-genai-sketches"
	advisory    = "GO-2026-5932"
	openpgp     = "golang.org/x/crypto/openpgp"
	mainPackage = "go.opentelemetry.io/collector/cmd/builder"
)

var releaseVersion = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$`)

type dependency struct {
	ImportPath string
	Name       string
	Imports    []string
	Incomplete bool              `json:",omitempty"`
	Error      json.RawMessage   `json:",omitempty"`
	DepsErrors []json.RawMessage `json:",omitempty"`
}

type evidence struct {
	Schema        string            `json:"schema"`
	Version       string            `json:"version"`
	GOOS          string            `json:"goos"`
	GOARCH        string            `json:"goarch"`
	CGOEnabled    string            `json:"cgo_enabled"`
	GoVersion     string            `json:"go_version"`
	BuildTags     string            `json:"build_tags"`
	CryptoVersion string            `json:"crypto_version"`
	BinarySHA256  string            `json:"binary_sha256"`
	Files         map[string]string `json:"file_sha256"`
	Packages      []dependency      `json:"packages"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("expected collect or generate")
	}
	f := flag.NewFlagSet(args[0], flag.ContinueOnError)
	version := f.String("version", "", "release tag")
	out := f.String("out", "", "output JSON file")
	switch args[0] {
	case "collect":
		dist := f.String("dist", "", "generated distribution directory")
		arch := f.String("arch", "", "Linux architecture")
		manifest := f.String("manifest", "", "build manifest")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		if f.NArg() != 0 || *out == "" || *dist == "" || *manifest == "" {
			return fmt.Errorf("collect requires dist, arch, version, manifest, out")
		}
		e, err := collect(*dist, *manifest, *version, *arch)
		if err != nil {
			return err
		}
		return writeJSON(*out, e)
	case "generate":
		dir := f.String("evidence", "", "directory with both architecture evidence files")
		binaries := f.String("binaries", "", "directory with binaries extracted from image digest")
		image := f.String("image", "", "immutable release image reference")
		stamp := f.String("timestamp", "", "issue time (RFC3339)")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		if f.NArg() != 0 || *out == "" || *dir == "" || *binaries == "" {
			return fmt.Errorf("generate requires evidence, binaries, image, version, timestamp, out")
		}
		doc, err := generate(*dir, *binaries, *image, *version, *stamp)
		if err != nil {
			return err
		}
		return writeJSON(*out, doc)
	default:
		return fmt.Errorf("expected collect or generate")
	}
}

func sum(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }

func fileSum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func validHash(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == sha256.Size && strings.ToLower(s) == s
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0644)
}

func validateGraph(packages []dependency) error {
	byPath := map[string]dependency{}
	cryptoPresent := false
	for _, p := range packages {
		if p.ImportPath == "" || p.Name == "" || p.Incomplete || len(p.Error) != 0 || len(p.DepsErrors) != 0 {
			return fmt.Errorf("incomplete dependency graph")
		}
		if _, ok := byPath[p.ImportPath]; ok {
			return fmt.Errorf("duplicate dependency")
		}
		if p.ImportPath == openpgp || strings.HasPrefix(p.ImportPath, openpgp+"/") {
			return fmt.Errorf("%s is imported; refusing not_affected", advisory)
		}
		cryptoPresent = cryptoPresent || strings.HasPrefix(p.ImportPath, "golang.org/x/crypto/")
		byPath[p.ImportPath] = p
	}
	if byPath[mainPackage].Name != "main" || !cryptoPresent {
		return fmt.Errorf("expected collector root and crypto component in graph")
	}
	seen := map[string]bool{}
	var visit func(string) error
	visit = func(path string) error {
		if seen[path] {
			return nil
		}
		p, ok := byPath[path]
		if !ok {
			return fmt.Errorf("dependency graph is not closed: %s", path)
		}
		seen[path] = true
		for _, dep := range p.Imports {
			if err := visit(dep); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(mainPackage); err != nil {
		return err
	}
	if len(seen) != len(byPath) {
		return fmt.Errorf("unexpected disconnected dependency graph")
	}
	return nil
}

func checkGeneratedVersion(path, version string) error {
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		return err
	}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		typ, ok := lit.Type.(*ast.SelectorExpr)
		if !ok || typ.Sel.Name != "BuildInfo" {
			return true
		}
		values := map[string]string{}
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok {
				continue
			}
			value, ok := kv.Value.(*ast.BasicLit)
			if !ok || value.Kind != token.STRING {
				continue
			}
			values[key.Name], _ = strconv.Unquote(value.Value)
		}
		found = found || (values["Command"] == "otelcol-genai-sketches" && values["Version"] == strings.TrimPrefix(version, "v"))
		return true
	})
	if !found {
		return fmt.Errorf("generated collector version does not match release")
	}
	return nil
}

func collect(dist, manifest, version, arch string) (evidence, error) {
	e := evidence{Schema: "collector-vex-evidence/v1", Version: version, GOOS: "linux", GOARCH: arch, CGOEnabled: "0", Files: map[string]string{}}
	if !releaseVersion.MatchString(version) || (arch != "amd64" && arch != "arm64") {
		return e, fmt.Errorf("expected release tag and amd64 or arm64")
	}
	binary := filepath.Join(dist, "otelcol-genai-sketches")
	info, err := buildinfo.ReadFile(binary)
	if err != nil {
		return e, err
	}
	settings := map[string]string{}
	for _, s := range info.Settings {
		settings[s.Key] = s.Value
	}
	if info.Path != mainPackage || settings["GOOS"] != "linux" || settings["GOARCH"] != arch || settings["CGO_ENABLED"] != "0" {
		return e, fmt.Errorf("binary build context does not match Linux target")
	}
	for _, flag := range []string{"-race", "-msan", "-asan"} {
		if settings[flag] == "true" {
			return e, fmt.Errorf("unsupported instrumented build")
		}
	}
	e.GoVersion, e.BuildTags = info.GoVersion, settings["-tags"]
	tool := exec.Command("go", "env", "GOVERSION")
	tool.Env = append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local", "GOFLAGS=")
	toolVersion, err := tool.Output()
	if err != nil {
		return e, err
	}
	if strings.TrimSpace(string(toolVersion)) != e.GoVersion {
		return e, fmt.Errorf("graph toolchain differs from built binary")
	}
	for _, dep := range info.Deps {
		if dep.Path == "golang.org/x/crypto" {
			if dep.Replace != nil {
				dep = dep.Replace
			}
			if dep.Path != "golang.org/x/crypto" || dep.Version == "" {
				return e, fmt.Errorf("unexpected crypto replacement")
			}
			e.CryptoVersion = dep.Version
		}
	}
	if e.CryptoVersion == "" {
		return e, fmt.Errorf("crypto module absent; reassess justification")
	}
	if err := checkGeneratedVersion(filepath.Join(dist, "main.go"), version); err != nil {
		return e, err
	}
	e.BinarySHA256, err = fileSum(binary)
	if err != nil {
		return e, err
	}
	sources, err := filepath.Glob(filepath.Join(dist, "*.go"))
	if err != nil {
		return e, err
	}
	sources = append(sources, filepath.Join(dist, "go.mod"), filepath.Join(dist, "go.sum"), manifest)
	for _, path := range sources {
		h, err := fileSum(path)
		if err != nil {
			return e, err
		}
		name := filepath.Base(path)
		if path == manifest {
			name = "builder.yaml"
		}
		e.Files[name] = h
	}
	// Use the built binary's platform, feature level and tags, not host defaults.
	cmd := exec.Command("go", "list", "-mod=readonly", "-deps", "-json", "-tags="+e.BuildTags, ".")
	cmd.Dir = dist
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local", "GOFLAGS=", "GOEXPERIMENT="+settings["GOEXPERIMENT"], "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+arch)
	for _, key := range []string{"GOAMD64", "GOARM64"} {
		if settings[key] != "" {
			cmd.Env = append(cmd.Env, key+"="+settings[key])
		}
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	data, err := cmd.Output()
	if err != nil {
		return e, fmt.Errorf("go list failed: %w: %s", err, &stderr)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	for {
		var p dependency
		err := decoder.Decode(&p)
		if err == io.EOF {
			break
		}
		if err != nil {
			return e, err
		}
		sort.Strings(p.Imports)
		e.Packages = append(e.Packages, p)
	}
	sort.Slice(e.Packages, func(i, j int) bool { return e.Packages[i].ImportPath < e.Packages[j].ImportPath })
	return e, validateGraph(e.Packages)
}

func generate(dir, binaries, image, version, stamp string) (map[string]any, error) {
	digest, ok := strings.CutPrefix(image, imageName+"@sha256:")
	if !ok || !validHash(digest) || !releaseVersion.MatchString(version) {
		return nil, fmt.Errorf("expected release version and immutable standard image digest")
	}
	timestamp, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return nil, fmt.Errorf("expected RFC3339 issue timestamp")
	}
	var notes []string
	for _, arch := range []string{"amd64", "arm64"} {
		name := "vex-linux-" + arch + ".json"
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		var e evidence
		if err := json.Unmarshal(data, &e); err != nil {
			return nil, err
		}
		if e.Schema != "collector-vex-evidence/v1" || e.Version != version || e.GOOS != "linux" || e.GOARCH != arch || e.CGOEnabled != "0" || e.GoVersion == "" || e.CryptoVersion == "" || !validHash(e.BinarySHA256) {
			return nil, fmt.Errorf("evidence build context mismatch for %s", arch)
		}
		for _, name := range []string{"go.mod", "go.sum", "main.go", "components.go", "builder.yaml"} {
			if !validHash(e.Files[name]) {
				return nil, fmt.Errorf("missing build evidence hash: %s", name)
			}
		}
		if err := validateGraph(e.Packages); err != nil {
			return nil, err
		}
		h, err := fileSum(filepath.Join(binaries, "collector-linux-"+arch))
		if err != nil {
			return nil, err
		}
		if h != e.BinarySHA256 {
			return nil, fmt.Errorf("published %s binary differs from verified graph build", arch)
		}
		notes = append(notes, fmt.Sprintf("linux/%s: %s sha256:%s; binary sha256:%s; %s; golang.org/x/crypto %s", arch, name, sum(data), h, e.GoVersion, e.CryptoVersion))
	}
	purl := "pkg:oci/otelcol-genai-sketches@" + url.QueryEscape("sha256:"+digest) + "?repository_url=" + url.QueryEscape(imageName) + "&tag=" + url.QueryEscape(strings.TrimPrefix(version, "v"))
	doc := map[string]any{
		"@context": "https://openvex.dev/ns/v0.2.0", "author": "https://github.com/llm-measurement", "role": "Project Maintainer",
		"timestamp": timestamp.UTC().Format(time.RFC3339), "version": 1,
		"tooling": project + "/tree/" + version + "/scripts/release-vex",
		"statements": []any{map[string]any{
			"vulnerability": map[string]string{"@id": "https://pkg.go.dev/vuln/" + advisory, "name": advisory},
			"products":      []any{map[string]any{"@id": purl, "identifiers": map[string]string{"purl": purl}, "hashes": map[string]string{"sha-256": digest}}},
			"status":        "not_affected", "justification": "vulnerable_code_not_present",
			"impact_statement": "The generated collector import graphs for this image's Linux amd64 and arm64 binaries exclude golang.org/x/crypto/openpgp and every subpackage. The golang.org/x/crypto module remains present; its module-level warning is retained. This assessment does not cover custom builds, added components, other images or other vulnerabilities.",
			"status_notes":     strings.Join(notes, "; "),
		}},
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	// Content-addressed snapshots avoid silently revising an existing VEX document.
	doc["@id"] = "urn:sha256:" + sum(data)
	return doc, nil
}
