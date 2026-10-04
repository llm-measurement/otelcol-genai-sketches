// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex
package charttest

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

type rule struct {
	Alert       string            `yaml:"alert"`
	Expr        string            `yaml:"expr"`
	For         string            `yaml:"for"`
	Labels      map[string]string `yaml:"labels"`
	Annotations map[string]string `yaml:"annotations"`
}

type ruleFile struct {
	Groups []struct {
		Name  string `yaml:"name"`
		Rules []rule `yaml:"rules"`
	} `yaml:"groups"`
}

func render(t *testing.T, settings ...string) ([]byte, error) {
	t.Helper()
	helm := os.Getenv("HELM")
	if helm == "" {
		var err error
		helm, err = exec.LookPath("helm")
		if err != nil {
			t.Skip("Helm is optional; set HELM to run chart tests")
		}
	}
	args := []string{"template", "test", "../otelcol-genai-sketches"}
	for _, setting := range settings {
		flag := "--set"
		if parts := strings.SplitN(setting, "=", 2); len(parts) == 2 && json.Valid([]byte(parts[1])) {
			flag = "--set-json"
		}
		args = append(args, flag, setting)
	}
	return exec.Command(helm, args...).CombinedOutput()
}

func parseRules(t *testing.T, output []byte) ruleFile {
	t.Helper()
	decoder := yaml.NewDecoder(bytes.NewReader(output))
	for {
		var document struct {
			Kind string   `yaml:"kind"`
			Spec ruleFile `yaml:"spec"`
		}
		err := decoder.Decode(&document)
		if err == io.EOF {
			return ruleFile{}
		}
		if err != nil {
			t.Fatal(err)
		}
		if document.Kind == "PrometheusRule" {
			return document.Spec
		}
	}
}

func TestHelmAnomalies(t *testing.T) {
	for _, test := range []struct {
		name     string
		settings []string
		groups   int
	}{
		{"disabled", nil, 0},
		{"parent-disabled", []string{"prometheusRule.anomalies.enabled=true"}, 0},
		{"accounting-only", []string{"prometheusRule.enabled=true"}, 1},
		{"opt-in", []string{"prometheusRule.enabled=true", "prometheusRule.anomalies.enabled=true"}, 2},
		{"dedup", []string{"prometheusRule.enabled=true", "prometheusRule.anomalies.enabled=true", "connector.dedup.enabled=true"}, 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			output, err := render(t, test.settings...)
			if err != nil {
				t.Fatalf("helm: %v\n%s", err, output)
			}
			rules := parseRules(t, output)
			if len(rules.Groups) != test.groups {
				t.Fatalf("got %d groups, want %d", len(rules.Groups), test.groups)
			}
			if test.groups == 0 {
				return
			}
			want := 3
			if test.name == "dedup" {
				want++
			}
			if len(rules.Groups[0].Rules) != want {
				t.Fatal("accounting rules changed")
			}
			if test.groups == 2 {
				group := rules.Groups[1]
				if group.Name != "otelcol-genai-sketches.anomalies" || len(group.Rules) != 3 {
					t.Fatalf("unexpected anomaly group: %+v", group)
				}
				for _, r := range group.Rules {
					if r.For != "10m" || len(r.Labels) != 1 || r.Labels["severity"] != "warning" {
						t.Fatalf("unexpected persistence or labels: %+v", r)
					}
					if !strings.Contains(r.Expr, `[1h] offset 10m`) || strings.Contains(r.Expr, "clamp_min") || strings.Contains(r.Expr, "or vector(0)") {
						t.Fatalf("unsafe baseline or denominator: %s", r.Expr)
					}
				}
			}
		})
	}
	t.Run("overrides", func(t *testing.T) {
		output, err := render(t, "prometheusRule.enabled=true", "prometheusRule.anomalies.enabled=true",
			"prometheusRule.sliceName=by_team_model", "prometheusRule.anomalies.window=5m",
			"prometheusRule.anomalies.baselineWindow=2h", "prometheusRule.anomalies.for=12m",
			"prometheusRule.anomalies.minRecentAttempts=200", "prometheusRule.anomalies.minBaselineAttempts=2000",
			"prometheusRule.anomalies.attemptRateRatio=4", "prometheusRule.anomalies.tokensPerAttemptRatio=5",
			"prometheusRule.anomalies.missingUsageRatio=0.3", "prometheusRule.anomalies.missingUsageIncrease=0.15")
		if err != nil {
			t.Fatalf("helm: %v\n%s", err, output)
		}
		for i, r := range parseRules(t, output).Groups[1].Rules {
			for _, fragment := range []string{`slice="by_team_model"`, `[2h] offset 5m`, ">= 200", ">= 2000"} {
				if !strings.Contains(r.Expr, fragment) {
					t.Fatalf("override %s missing in %s", fragment, r.Expr)
				}
			}
			if r.For != "12m" || strings.Contains(r.Expr, "by_model") {
				t.Fatalf("override ignored: %+v", r)
			}
			if !strings.Contains(r.Expr, []string{"> 4 *", "> 5 *", "> 0.3"}[i]) || (i == 2 && !strings.Contains(r.Expr, "> 0.15")) {
				t.Fatalf("threshold override ignored: %s", r.Expr)
			}
		}
	})
	for _, setting := range []string{
		"enabled=yes", "window=0m", "baselineWindow=invalid", "for=0m",
		"minRecentAttempts=0", "minBaselineAttempts=-1", "attemptRateRatio=1", "tokensPerAttemptRatio=0",
		"missingUsageRatio=1.1", "missingUsageIncrease=0", "groupBy=user_id",
	} {
		t.Run("invalid-"+setting, func(t *testing.T) {
			output, err := render(t, "prometheusRule.enabled=true", "prometheusRule.anomalies."+setting)
			if err == nil {
				t.Fatalf("invalid configuration accepted: %s\n%s", setting, output)
			}
		})
	}
}

func TestAnomalyPromtool(t *testing.T) {
	output, err := render(t, "prometheusRule.enabled=true", "prometheusRule.anomalies.enabled=true",
		"prometheusRule.anomalies.window=5m", "prometheusRule.anomalies.baselineWindow=30m",
		"prometheusRule.anomalies.for=2m", "prometheusRule.anomalies.minBaselineAttempts=600")
	if err != nil {
		t.Fatalf("helm: %v\n%s", err, output)
	}
	rules := parseRules(t, output)
	// Use the actual chart expressions and metadata, not a second rule implementation.
	rules.Groups = rules.Groups[1:]
	dir := t.TempDir()
	// The non-root container must be able to traverse this synthetic-fixture mount.
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	writeYAML := func(name string, value any) {
		t.Helper()
		data, err := yaml.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	writeYAML("rules.yaml", rules)
	writeYAML("anomalies.test.yaml", promtoolFixtures(rules.Groups[0].Rules))

	promtool := os.Getenv("PROMTOOL")
	if promtool == "" && os.Getenv("PROMTOOL_DOCKER") != "1" {
		promtool, err = exec.LookPath("promtool")
		if err != nil {
			t.Skip("set PROMTOOL or PROMTOOL_DOCKER=1 to run semantic fixtures")
		}
	}
	run := func(args ...string) string {
		t.Helper()
		command := promtool
		if command == "" {
			// Reuse the production rule-check image pin without introducing another pin.
			makefile, err := os.ReadFile("../../../Makefile")
			if err != nil {
				t.Fatal(err)
			}
			var image string
			for _, line := range strings.Split(string(makefile), "\n") {
				if strings.HasPrefix(line, "PROMETHEUS_IMAGE := ") {
					image = strings.TrimPrefix(line, "PROMETHEUS_IMAGE := ")
				}
			}
			if image == "" {
				t.Fatal("missing repository Prometheus image pin")
			}
			command = "docker"
			args = append([]string{"run", "--rm", "--network=none", "--read-only", "--cap-drop=ALL",
				"--security-opt=no-new-privileges", "--user=65534:65534", "--entrypoint=/bin/promtool",
				"--tmpfs", "/tmp:rw,noexec,nosuid,size=128m,mode=1777",
				"-v", dir + ":/fixtures:ro", "-w", "/fixtures", image}, args...)
		}
		cmd := exec.Command(command, args...)
		cmd.Dir = dir
		result, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("promtool %v: %v\n%s", args, err, result)
		}
		return string(result)
	}
	t.Log(run("--version"))
	t.Log(run("check", "rules", "rules.yaml"))
	t.Log(run("test", "rules", "anomalies.test.yaml"))
}
