// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex
package charttest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

type chartMetadata struct {
	Version    string `yaml:"version"`
	AppVersion string `yaml:"appVersion"`
}

func TestChartReleaseMetadata(t *testing.T) {
	data, err := os.ReadFile("../otelcol-genai-sketches/Chart.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var chart chartMetadata
	if err := yaml.Unmarshal(data, &chart); err != nil {
		t.Fatal(err)
	}
	if chart.Version != "0.3.2" || chart.AppVersion != "0.3.0" {
		t.Fatalf("chart-only release metadata changed: %+v", chart)
	}
	if tag := os.Getenv("CHART_RELEASE_TAG"); tag != "" && tag != "chart-v"+chart.Version {
		t.Fatalf("tag %q does not match chart version %q", tag, chart.Version)
	}
	data, err = os.ReadFile("../otelcol-genai-sketches/values.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var values struct {
		PrometheusRule struct {
			Enabled   *bool `yaml:"enabled"`
			Anomalies struct {
				Enabled *bool `yaml:"enabled"`
			} `yaml:"anomalies"`
		} `yaml:"prometheusRule"`
	}
	if err := yaml.Unmarshal(data, &values); err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []*bool{values.PrometheusRule.Enabled, values.PrometheusRule.Anomalies.Enabled} {
		if enabled == nil || *enabled {
			t.Fatal("chart-only release must explicitly leave both warning switches off")
		}
	}
}

func TestChartReleasePackage(t *testing.T) {
	output, err := render(t)
	if err != nil {
		t.Fatalf("render: %v\n%s", err, output)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(output))
	found := false
	for {
		var doc struct {
			Kind string `yaml:"kind"`
			Spec struct {
				Template struct {
					Spec struct {
						Containers []struct {
							Image string `yaml:"image"`
						} `yaml:"containers"`
					} `yaml:"spec"`
				} `yaml:"template"`
			} `yaml:"spec"`
		}
		if err := decoder.Decode(&doc); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if doc.Kind == "PrometheusRule" {
			t.Fatal("default chart unexpectedly renders warnings")
		}
		if doc.Kind == "Deployment" {
			found = true
			containers := doc.Spec.Template.Spec.Containers
			if len(containers) != 1 || containers[0].Image != "ghcr.io/llm-measurement/otelcol-genai-sketches:0.3.0" {
				t.Fatalf("default runtime image changed: %+v", containers)
			}
		}
	}
	if !found {
		t.Fatal("default deployment missing")
	}
	helm := os.Getenv("HELM")
	if helm == "" {
		helm = "helm" // render already checked availability.
	}
	dir := t.TempDir()
	output, err = exec.Command(helm, "package", "../otelcol-genai-sketches", "--destination", dir).CombinedOutput()
	if err != nil {
		t.Fatalf("package: %v\n%s", err, output)
	}
	file, err := os.Open(filepath.Join(dir, "otelcol-genai-sketches-0.3.2.tgz"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer compressed.Close()
	archive := tar.NewReader(compressed)
	for {
		header, err := archive.Next()
		if err == io.EOF {
			t.Fatal("packaged Chart.yaml missing")
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Name == "otelcol-genai-sketches/Chart.yaml" {
			var chart chartMetadata
			if err := yaml.NewDecoder(archive).Decode(&chart); err != nil {
				t.Fatal(err)
			}
			if chart.Version != "0.3.2" || chart.AppVersion != "0.3.0" {
				t.Fatalf("packaging changed chart or runtime version: %+v", chart)
			}
			break
		}
	}
}

func TestChartAttestationInstructions(t *testing.T) {
	data, err := os.ReadFile("../../../docs/CHART_RELEASE.md")
	if err != nil {
		t.Fatal(err)
	}
	guide := string(data)
	start := strings.Index(guide, "gh attestation verify ")
	if start < 0 {
		t.Fatal("missing attestation verification instructions")
	}
	end := strings.Index(guide[start:], "\nhelm show chart")
	if end < 0 {
		t.Fatal("missing post-verification chart inspection")
	}
	command := guide[start : start+end]
	for _, required := range []string{
		"--source-ref refs/tags/chart-v0.3.2",
		"--signer-workflow llm-measurement/otelcol-genai-sketches/.github/workflows/chart-release.yml",
		"--cert-identity 'https://github.com/llm-measurement/otelcol-genai-sketches/.github/workflows/chart-release.yml@refs/tags/chart-v0.3.2'",
	} {
		if !strings.Contains(command, required) {
			t.Fatalf("attestation verification lost binding: %s", required)
		}
	}
}

func TestChartReleaseWorkflow(t *testing.T) {
	type step struct {
		Name string `yaml:"name"`
		Uses string `yaml:"uses"`
		Run  string `yaml:"run"`
	}
	type workflow struct {
		On struct {
			Push struct {
				Tags []string `yaml:"tags"`
			} `yaml:"push"`
		} `yaml:"on"`
		Jobs map[string]struct {
			If    string `yaml:"if"`
			Steps []step `yaml:"steps"`
		} `yaml:"jobs"`
	}
	read := func(name string) workflow {
		t.Helper()
		data, err := os.ReadFile("../../../.github/workflows/" + name)
		if err != nil {
			t.Fatal(err)
		}
		var result workflow
		if err := yaml.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	chart, runtime := read("chart-release.yml"), read("release.yml")
	for _, tc := range []struct {
		workflow workflow
		tag      string
		want     bool
	}{
		{chart, "chart-v0.3.2", true}, {chart, "v0.3.0", false},
		{runtime, "v0.3.0", true}, {runtime, "chart-v0.3.2", false},
	} {
		matched := false
		for _, pattern := range tc.workflow.On.Push.Tags {
			match, err := path.Match(pattern, tc.tag)
			if err != nil {
				t.Fatal(err)
			}
			matched = matched || match
		}
		if matched != tc.want {
			t.Fatalf("workflow tag boundary failed for %s", tc.tag)
		}
	}
	if len(chart.Jobs) != 1 || len(chart.Jobs["chart"].Steps) == 0 {
		t.Fatal("expected only a chart publishing job")
	}
	if chart.Jobs["chart"].If != "github.event.repository.visibility == 'public'" {
		t.Fatal("chart publication and public transparency must be guarded by public repository visibility")
	}
	pins := map[string]bool{}
	for _, job := range runtime.Jobs {
		for _, s := range job.Steps {
			pins[s.Uses] = true
		}
	}
	var preflight, publish, packageScript string
	for _, s := range chart.Jobs["chart"].Steps {
		if s.Uses != "" && !pins[s.Uses] {
			t.Fatalf("chart release added an action not pinned by the runtime release: %s", s.Uses)
		}
		for _, forbidden := range []string{"build-push-action", "buildx-action", "metadata-action", "public-install", "make dist", "production-image", "--app-version", "--clobber", "IMAGE_NAME"} {
			if strings.Contains(s.Uses+"\n"+s.Run, forbidden) {
				t.Fatalf("runtime build or metadata override in chart step %q: %s", s.Name, forbidden)
			}
		}
		switch s.Name {
		case "Refuse an existing OCI chart version":
			preflight = s.Run
		case "Create chart-only GitHub release":
			publish = s.Run
		case "Package checked-in chart metadata":
			packageScript = s.Run
		}
	}
	if !strings.Contains(packageScript, "helm package") || strings.Contains(packageScript, "--version") {
		t.Fatal("package must use checked-in versions")
	}
	if !strings.Contains(publish, "--latest=false") || !strings.Contains(publish, "--verify-tag") {
		t.Fatal("chart release must require its tag and preserve the latest runtime release")
	}
	if preflight == "" {
		t.Fatal("existing-version guard missing")
	}
	// Execute only the read-only preflight with mocked HTTP functions, never publish steps.
	stub := `
curl() {
  if [[ "$*" == *"https://ghcr.io/token?"* ]]; then
    printf '%s' '{"token":"test-token"}'
  elif [[ "$*" == *"https://ghcr.io/v2/llm-measurement/charts/otelcol-genai-sketches/manifests/0.3.2"* ]]; then
    printf '%s' "$TEST_HTTP_STATUS"
  else
    return 22
  fi
}
jq() { cat >/dev/null; printf '%s' 'test-token'; }
`
	for _, status := range []string{"404", "200", "401", "403", "429", "500"} {
		t.Run("registry-status-"+status, func(t *testing.T) {
			cmd := exec.Command("bash", "-euo", "pipefail", "-c", stub+preflight)
			cmd.Env = append(os.Environ(), "TEST_HTTP_STATUS="+status, "GITHUB_REF_NAME=chart-v0.3.2",
				"CHART_NAME=ghcr.io/llm-measurement/charts/otelcol-genai-sketches")
			output, err := cmd.CombinedOutput()
			if (err == nil) != (status == "404") {
				t.Fatalf("preflight HTTP %s: %v\n%s", status, err, output)
			}
		})
	}
}
