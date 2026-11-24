// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package main

import (
	"os"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestDistributionOTelSecurityPins(t *testing.T) {
	data, err := os.ReadFile("../../builder.yaml")
	if err != nil {
		t.Fatal(err)
	}
	generated, err := distributionManifest(data, "v0.7.4")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Replaces []string `yaml:"replaces"`
	}
	if err := yaml.Unmarshal(generated, &manifest); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "/trace", "/metric", "/log", "/sdk", "/sdk/metric", "/sdk/log"} {
		module := "go.opentelemetry.io/otel" + suffix
		want := module + " => " + module + " v1.47.0"
		count := 0
		for _, replace := range manifest.Replaces {
			if strings.HasPrefix(replace, module+" => ") {
				count++
				if replace != want {
					t.Errorf("inconsistent OTel security pin: %s; want %s", replace, want)
				}
			}
		}
		if count != 1 {
			t.Errorf("want one security pin for %s, got %d", module, count)
		}
	}
}

func TestSupportedDistrolessRuntimePin(t *testing.T) {
	data, err := os.ReadFile("../../packaging/docker/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	const pin = "ARG RUNTIME_IMAGE=gcr.io/distroless/static-debian13:nonroot@sha256:8ce6dd87e9823c5b48dcfb2c8604714ccb351ce59607327bcc4957448da279b5"
	if strings.Count(string(data), pin+"\n") != 1 {
		t.Fatal("runtime must use the reviewed, supported distroless digest")
	}
	if !strings.Contains(string(data), "FROM ${RUNTIME_IMAGE}\n") || !strings.Contains(string(data), "USER 65532:65532\n") {
		t.Fatal("runtime base or nonroot user changed")
	}
}
