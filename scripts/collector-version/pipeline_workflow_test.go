// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex
package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

type pipelineStep struct {
	Name     string            `yaml:"name"`
	Uses     string            `yaml:"uses"`
	Run      string            `yaml:"run"`
	If       string            `yaml:"if"`
	Continue bool              `yaml:"continue-on-error"`
	With     map[string]string `yaml:"with"`
	Env      map[string]string `yaml:"env"`
}

type pipelineJob struct {
	Needs       []string          `yaml:"needs"`
	If          string            `yaml:"if"`
	Continue    bool              `yaml:"continue-on-error"`
	Permissions map[string]string `yaml:"permissions"`
	Steps       []pipelineStep    `yaml:"steps"`
	Env         map[string]string `yaml:"env"`
	Outputs     map[string]string `yaml:"outputs"`
}

type pipeline struct {
	Jobs map[string]pipelineJob `yaml:"jobs"`
}

func readPipeline(t *testing.T, name string) pipeline {
	t.Helper()
	data, err := os.ReadFile("../../.github/workflows/" + name)
	if err != nil {
		t.Fatal(err)
	}
	// The reusable public-install job has a scalar dependency and no publishing steps.
	var document map[string]any
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if name == "release.yml" {
		public, ok := document["jobs"].(map[string]any)["public-install"].(map[string]any)
		if !ok || public["needs"] != "publish" || public["uses"] != "./.github/workflows/public-install.yml" {
			t.Fatal("public installation must follow approved publication")
		}
	}
	delete(document["jobs"].(map[string]any), "public-install")
	data, err = yaml.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	var result pipeline
	if err := yaml.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func checkPipeline(p pipeline, chart bool) error {
	build := "image"
	if chart {
		build = "chart"
	}
	wantNeeds := map[string][]string{
		"verify": nil, build: {"verify"}, "scan": {"verify", build},
		"sign": {build, "scan"}, "publish": {"verify", build, "scan", "sign"},
	}
	if chart {
		wantNeeds["sign"] = []string{"verify", build, "scan"}
	}
	if len(p.Jobs) != len(wantNeeds) {
		return fmt.Errorf("unexpected job graph")
	}
	if len(p.Jobs["scan"].Outputs) != 0 {
		return fmt.Errorf("scanner must contribute status only")
	}
	for name, needs := range wantNeeds {
		job, exists := p.Jobs[name]
		if !exists || !reflect.DeepEqual(job.Needs, needs) {
			return fmt.Errorf("%s bypasses a prerequisite", name)
		}
		if job.Continue || (job.If != "" && !(chart && name == "verify" && job.If == "github.event.repository.visibility == 'public'")) {
			return fmt.Errorf("%s can bypass failure", name)
		}
		allowed := map[string]string{"contents": "read"}
		switch name {
		case "image", "chart":
			allowed["packages"] = "write"
		case "scan":
			allowed["packages"] = "read"
		case "sign":
			allowed = map[string]string{"actions": "read", "artifact-metadata": "write", "attestations": "write", "contents": "read", "id-token": "write", "packages": "write"}
		case "publish":
			allowed = map[string]string{"actions": "read", "attestations": "read", "contents": "write", "packages": "write"}
		}
		if !(name == "verify" && len(job.Permissions) == 0) && !reflect.DeepEqual(job.Permissions, allowed) {
			return fmt.Errorf("%s permissions changed", name)
		}
		for _, step := range job.Steps {
			if strings.Contains(step.Run+fmt.Sprint(step.With)+fmt.Sprint(job.Env)+fmt.Sprint(step.Env), "needs.scan.outputs") {
				return fmt.Errorf("scanner-controlled publication input")
			}
			if name == "scan" && (strings.HasPrefix(step.Uses, "actions/upload-artifact@") || strings.Contains(step.Run, "release-vex generate") || strings.Contains(step.Run, ".SBOM")) {
				return fmt.Errorf("scanner is producing trusted evidence")
			}
			if strings.HasPrefix(step.Uses, "aquasecurity/trivy-action@") && name != "scan" {
				return fmt.Errorf("scanner executed in trusted producer")
			}
			if step.Continue || step.If != "" {
				return fmt.Errorf("conditional or nonblocking release step")
			}
			if step.Uses != "" && !regexp.MustCompile(`^[\w./-]+@[0-9a-f]{40}$`).MatchString(step.Uses) {
				return fmt.Errorf("unpinned action")
			}
			if strings.HasPrefix(step.Uses, "actions/cache@") || strings.Contains(step.Run, "--cache-from") || strings.Contains(step.Run, "--cache-to") {
				return fmt.Errorf("restored release cache")
			}
			for _, key := range []string{"cache-from", "cache-to"} {
				if step.With[key] != "" {
					return fmt.Errorf("build cache export/import")
				}
			}
			for _, action := range []string{"actions/setup-go@", "aquasecurity/trivy-action@"} {
				if strings.HasPrefix(step.Uses, action) && step.With["cache"] != "false" {
					return fmt.Errorf("action cache enabled")
				}
			}
			if strings.HasPrefix(step.Uses, "docker/setup-buildx-action@") && (step.With["cache-binary"] != "false" || step.With["driver-opts"] != "image=moby/buildkit:v0.34.0@sha256:b059f8d7226d0b326bc871af489a5dddf65e36af4d20d14251b072974d9ee87c") {
				return fmt.Errorf("unapproved BuildKit")
			}
			if strings.HasPrefix(step.Uses, "docker/setup-qemu-action@") && (step.With["cache-image"] != "false" || step.With["image"] != "tonistiigi/binfmt@sha256:400a4873b838d1b89194d982c45e5fb3cda4593fbfd7e08a02e76b03b21166f0") {
				return fmt.Errorf("unapproved binfmt")
			}
			if strings.HasPrefix(step.Uses, "docker/build-push-action@") && (name != build || step.With["tags"] != "" || step.With["outputs"] != "type=image,name=${{ env.IMAGE_NAME }},push-by-digest=true,name-canonical=true,push=true" || step.With["no-cache"] != "true" || step.With["provenance"] != "mode=max" || step.With["sbom"] != "generator=docker/buildkit-syft-scanner@sha256:ae4f3b554449e7e25548e7d8ccc029d17357348e30c6e3df01b92bc93654d6a9") {
				return fmt.Errorf("build can publish an unapproved tag/input")
			}
			if strings.Contains(step.Run, "helm push") || strings.Contains(step.Run, "imagetools create") || (strings.Contains(step.Run, "oras tag") && name != "publish") {
				return fmt.Errorf("early release tag")
			}
			if strings.HasPrefix(step.Uses, "actions/download-artifact@") && (step.With["artifact-ids"] == "" || step.With["name"] != "" || step.With["run-id"] != "" || step.With["digest-mismatch"] != "error") {
				return fmt.Errorf("unbound artifact handoff")
			}
			if strings.HasPrefix(step.Uses, "actions/download-artifact@") {
				producer := build
				if name == "publish" {
					producer = "sign"
				}
				if step.With["artifact-ids"] != "${{ needs."+producer+".outputs.artifact_id }}" {
					return fmt.Errorf("artifact not from independent producer")
				}
			}
			if strings.HasPrefix(step.Uses, "actions/attest@") && (name != "sign" || (step.With["subject-name"] != "" && !strings.Contains(step.With["subject-digest"], "needs."+build+".outputs."))) {
				return fmt.Errorf("unapproved attestation subject")
			}
			if name == "scan" && strings.Contains(step.Uses, "login-action") {
				return fmt.Errorf("scan has registry credentials")
			}
		}
	}
	for _, name := range []string{"scan", "sign", "publish"} {
		sums := build
		chartOutput := "chart_digest"
		if chart {
			chartOutput = "digest"
		}
		if p.Jobs[name].Env["CHART_DIGEST"] != "${{ needs."+build+".outputs."+chartOutput+" }}" ||
			p.Jobs[name].Env["SUMS"] != "${{ needs."+sums+".outputs.sums }}" ||
			(!chart && p.Jobs[name].Env["DIGEST"] != "${{ needs.image.outputs.digest }}") {
			return fmt.Errorf("%s is not bound to the approved build/scan outputs", name)
		}
		steps := p.Jobs[name].Steps
		guard := slices.IndexFunc(steps, func(s pipelineStep) bool { return s.Name == "Verify approved digest handoff" })
		if guard < 0 {
			return fmt.Errorf("missing digest gate")
		}
		for i, s := range steps {
			if (strings.Contains(s.Run, "cosign sign") || strings.Contains(s.Run, "oras tag") || strings.Contains(s.Uses, "login-action")) && i < guard {
				return fmt.Errorf("mutation before digest gate")
			}
		}
	}
	joined := func(name string) string {
		var result string
		for _, s := range p.Jobs[name].Steps {
			result += s.Run + "\n"
		}
		return result
	}
	for _, name := range []string{"sign", "publish"} {
		source := joined(name)
		for _, required := range []string{"cosign verify", "--certificate-identity", "--certificate-oidc-issuer https://token.actions.githubusercontent.com", "gh attestation verify", "--source-ref", "--source-digest \"$GITHUB_SHA\"", "--deny-self-hosted-runners"} {
			if !strings.Contains(source, required) {
				return fmt.Errorf("%s lacks %s", name, required)
			}
		}
	}
	publication := joined("publish")
	if strings.Index(publication, "oras tag") < strings.Index(publication, "gh release create") || strings.Index(publication, "gh release create") < strings.Index(publication, "gh attestation verify") {
		return fmt.Errorf("publication before verification")
	}
	if !chart {
		var platforms []string
		for _, s := range p.Jobs["scan"].Steps {
			if strings.HasPrefix(s.Uses, "aquasecurity/trivy-action@") {
				if len(s.With) != 8 {
					return fmt.Errorf("unexpected scanner override")
				}
				for key, want := range map[string]string{"version": "v0.74.0", "image-ref": "${{ env.IMAGE_NAME }}@${{ needs.image.outputs.digest }}", "scanners": "vuln,secret", "severity": "HIGH,CRITICAL", "ignore-unfixed": "false", "exit-code": "1"} {
					if s.With[key] != want {
						return fmt.Errorf("scan policy changed: %s", key)
					}
				}
				platforms = append(platforms, s.Env["TRIVY_PLATFORM"])
			}
		}
		if !reflect.DeepEqual(platforms, []string{"linux/amd64", "linux/arm64"}) {
			return fmt.Errorf("missing platform scan")
		}
		if !strings.Contains(joined("image"), "docker buildx build --no-cache --platform") || !strings.Contains(joined("image"), "release-vex generate") || !strings.Contains(joined("image"), ".SBOM") {
			return fmt.Errorf("cached VEX rebuild")
		}
		for _, name := range []string{"image"} {
			if !slices.ContainsFunc(p.Jobs[name].Steps, func(s pipelineStep) bool { return strings.HasPrefix(s.Uses, "docker/setup-qemu-action@") }) {
				return fmt.Errorf("cross-architecture go run needs QEMU in %s", name)
			}
		}
	}
	return nil
}

func TestReleasePipelineBoundaries(t *testing.T) {
	for _, name := range []string{"release.yml", "chart-release.yml"} {
		t.Run(name, func(t *testing.T) {
			if err := checkPipeline(readPipeline(t, name), name == "chart-release.yml"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReleasePipelineRejectsGateRegressions(t *testing.T) {
	for _, mutation := range []string{"skip-scan", "always-publish", "scan-write", "cache", "early-tag", "unpinned-buildkit", "ignore-findings", "different-subject", "missing-digest-gate", "missing-qemu", "different-approved-digest", "scanner-checksums", "scanner-outputs", "scanner-upload", "scanner-artifact", "missing-source-commit"} {
		t.Run(mutation, func(t *testing.T) {
			p := readPipeline(t, "release.yml")
			switch mutation {
			case "skip-scan":
				j := p.Jobs["sign"]
				j.Needs = []string{"image"}
				p.Jobs["sign"] = j
			case "always-publish":
				j := p.Jobs["publish"]
				j.If = "always()"
				p.Jobs["publish"] = j
			case "scan-write":
				p.Jobs["scan"].Permissions["packages"] = "write"
			case "different-approved-digest":
				p.Jobs["publish"].Env["DIGEST"] = "sha256:other"
			case "scanner-checksums":
				p.Jobs["sign"].Env["SUMS"] = "${{ needs.scan.outputs.sums }}"
			case "scanner-outputs":
				j := p.Jobs["scan"]
				j.Outputs = map[string]string{"sums": "scanner controlled"}
				p.Jobs["scan"] = j
			case "scanner-upload":
				j := p.Jobs["scan"]
				j.Steps = append(j.Steps, pipelineStep{Uses: "actions/upload-artifact@" + strings.Repeat("a", 40)})
				p.Jobs["scan"] = j
			default:
				for name, j := range p.Jobs {
					for i, s := range j.Steps {
						switch {
						case mutation == "scanner-artifact" && name == "sign" && strings.HasPrefix(s.Uses, "actions/download-artifact@"):
							s.With["artifact-ids"] = "${{ needs.scan.outputs.artifact_id }}"
						case mutation == "missing-source-commit":
							s.Run = strings.ReplaceAll(s.Run, `--source-digest "$GITHUB_SHA"`, "")
						case mutation == "cache" && strings.HasPrefix(s.Uses, "actions/setup-go@"):
							s.With["cache"] = "true"
						case mutation == "early-tag" && strings.HasPrefix(s.Uses, "docker/build-push-action@"):
							s.With["tags"] = "example:latest"
						case mutation == "unpinned-buildkit" && strings.HasPrefix(s.Uses, "docker/setup-buildx-action@"):
							s.With["driver-opts"] = "image=moby/buildkit:latest"
						case mutation == "ignore-findings" && strings.HasPrefix(s.Uses, "aquasecurity/trivy-action@"):
							s.Continue = true
						case mutation == "different-subject" && strings.HasPrefix(s.Uses, "actions/attest@") && s.With["subject-name"] != "":
							s.With["subject-digest"] = "sha256:other"
						case mutation == "missing-digest-gate" && s.Name == "Verify approved digest handoff":
							s.Name = "removed"
						case mutation == "missing-qemu" && name == "image" && strings.HasPrefix(s.Uses, "docker/setup-qemu-action@"):
							s.Uses = ""
						}
						j.Steps[i] = s
					}
					p.Jobs[name] = j
				}
			}
			if err := checkPipeline(p, false); err == nil {
				t.Fatal("accepted unsafe pipeline")
			}
		})
	}
}

func writePipelineFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestDigestHandoffRejectsTampering(t *testing.T) {
	for _, workflow := range []string{"release.yml", "chart-release.yml"} {
		p := readPipeline(t, workflow)
		for _, job := range []string{"scan", "sign", "publish"} {
			var script string
			for _, s := range p.Jobs[job].Steps {
				if s.Name == "Verify approved digest handoff" {
					script = s.Run
				}
			}
			for _, mutation := range []string{"valid", "empty", "tag", "uppercase", "newline", "other-image", "other-chart", "corrupt-package", "corrupt-sums", "wrong-approval"} {
				t.Run(workflow+"/"+job+"/"+mutation, func(t *testing.T) {
					dir := t.TempDir()
					if err := os.Mkdir(filepath.Join(dir, "release"), 0700); err != nil {
						t.Fatal(err)
					}
					image, chart := "registry.example/collector", "registry.example/charts/collector"
					digest, chartDigest := "sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64)
					files := map[string][]byte{"chart-digest.txt": []byte(chart + "@" + chartDigest + "\n"), "otelcol-genai-sketches-1.2.3.tgz": []byte("chart")}
					if workflow == "release.yml" {
						files["image-digest.txt"] = []byte(image + "@" + digest + "\n")
						for _, name := range []string{"sbom-index.json", "openvex.json", "vex-linux-amd64.json", "vex-linux-arm64.json"} {
							files[name] = []byte("{}")
						}
					}
					if mutation == "other-image" {
						image = "registry.example/other"
					}
					if mutation == "other-chart" {
						chart = "registry.example/charts/other"
					}
					var sums string
					for name, data := range files {
						writePipelineFile(t, filepath.Join(dir, "release", name), data)
						sums += fmt.Sprintf("%x  %s\n", sha256.Sum256(data), name)
					}
					approval := fmt.Sprintf("%x", sha256.Sum256([]byte(sums)))
					if job == "publish" {
						writePipelineFile(t, filepath.Join(dir, "release/provenance.jsonl"), []byte("bundle"))
					}
					switch mutation {
					case "empty":
						chartDigest = ""
					case "tag":
						chartDigest = "latest"
					case "uppercase":
						chartDigest = strings.ToUpper(chartDigest)
					case "newline":
						chartDigest += "\n"
					case "corrupt-package":
						writePipelineFile(t, filepath.Join(dir, "release/otelcol-genai-sketches-1.2.3.tgz"), []byte("changed"))
					case "corrupt-sums":
						sums += "changed\n"
					case "wrong-approval":
						approval = strings.Repeat("0", 64)
					}
					writePipelineFile(t, filepath.Join(dir, "release/SHA256SUMS"), []byte(sums))
					cmd := exec.Command("bash", "-euo", "pipefail", "-c", script)
					cmd.Dir = dir
					cmd.Env = append(os.Environ(), "DIGEST="+digest, "CHART_DIGEST="+chartDigest, "SUMS="+approval, "IMAGE_NAME="+image, "CHART_NAME="+chart)
					output, err := cmd.CombinedOutput()
					want := mutation == "valid" || (mutation == "other-image" && workflow == "chart-release.yml")
					if (err == nil) != want {
						t.Fatalf("guard result: %v\n%s", err, output)
					}
				})
			}
		}
	}
}

func TestReleaseTagsUseOnlyApprovedDigests(t *testing.T) {
	for _, workflow := range []string{"release.yml", "chart-release.yml"} {
		p := readPipeline(t, workflow)
		var script string
		for _, s := range p.Jobs["publish"].Steps {
			if strings.Contains(s.Run, "oras tag") {
				script = s.Run
			}
		}
		for _, failure := range []string{"none", "tag", "resolve", "invalid-version", "invalid-second-tag"} {
			t.Run(workflow+"/"+failure, func(t *testing.T) {
				dir := t.TempDir()
				stub := `
oras() {
  case "$1" in
    tag)
      [[ "$2" == "$IMAGE_NAME@$DIGEST" || "$2" == "$CHART_NAME@$CHART_DIGEST" ]]
      [[ "$FAILURE" != tag ]] || return 21
      printf '%s\n' "$*" >> calls
      ;;
    resolve)
      [[ "$FAILURE" != resolve ]] || return 22
      if [[ "$2" == "$CHART_NAME:"* ]]; then printf '%s\n' "$CHART_DIGEST"; else printf '%s\n' "$DIGEST"; fi
      ;;
    *) return 99 ;;
  esac
}

`
				version := "1.2.3"
				if failure == "invalid-version" {
					version = "latest"
				}
				tags := "registry.example/collector:1.2.3\nregistry.example/collector:latest"
				if failure == "invalid-second-tag" {
					tags = "registry.example/collector:1.2.3\nregistry.example/other:latest"
				}
				cmd := exec.Command("bash", "-euo", "pipefail", "-c", stub+script)
				cmd.Dir = dir
				cmd.Env = append(os.Environ(), "FAILURE="+failure, "CHART_VERSION="+version, "IMAGE_TAGS="+tags,
					"IMAGE_NAME=registry.example/collector", "CHART_NAME=registry.example/charts/collector",
					"DIGEST=sha256:"+strings.Repeat("a", 64), "CHART_DIGEST=sha256:"+strings.Repeat("b", 64))
				output, err := cmd.CombinedOutput()
				want := failure == "none" || (workflow == "chart-release.yml" && failure == "invalid-second-tag")
				if (err == nil) != want {
					t.Fatalf("publication result: %v\n%s", err, output)
				}
				if failure == "invalid-version" || (workflow == "release.yml" && failure == "invalid-second-tag") {
					if _, err := os.Stat(filepath.Join(dir, "calls")); !os.IsNotExist(err) {
						t.Fatal("invalid tags caused a mutation")
					}
				}
			})
		}
	}
}

func pipelineScript(t *testing.T, p pipeline, job, contains string) string {
	t.Helper()
	for _, step := range p.Jobs[job].Steps {
		if strings.Contains(step.Run, contains) {
			return step.Run
		}
	}
	t.Fatalf("missing %s script containing %q", job, contains)
	return ""
}

func TestBuildOwnedSealRejectsScannerSubstitution(t *testing.T) {
	for _, workflow := range []string{"release.yml", "chart-release.yml"} {
		p := readPipeline(t, workflow)
		build := "chart"
		files := []string{"chart-digest.txt", "otelcol-genai-sketches-1.2.3.tgz"}
		if workflow == "release.yml" {
			build = "image"
			files = append(files, "image-digest.txt", "sbom-index.json", "openvex.json", "vex-linux-amd64.json", "vex-linux-arm64.json")
		}
		mutations := append([]string{"valid", "extra", "hidden", "nested", "linked"}, files...)
		for _, job := range []string{"sign", "publish"} {
			for _, mutation := range mutations {
				t.Run(workflow+"/"+job+"/"+mutation, func(t *testing.T) {
					_, dir := chartPublishWorkspace(t)
					for _, file := range files {
						writePipelineFile(t, filepath.Join(dir, "release", file), []byte("build-owned "+file))
					}
					image, chart := "registry.example/collector", "registry.example/charts/collector"
					digest, chartDigest := "sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64)
					writePipelineFile(t, filepath.Join(dir, "release/chart-digest.txt"), []byte(chart+"@"+chartDigest+"\n"))
					if build == "image" {
						writePipelineFile(t, filepath.Join(dir, "release/image-digest.txt"), []byte(image+"@"+digest+"\n"))
					}
					seal := pipelineScript(t, p, build, "> SHA256SUMS")
					run := func(script string, extra ...string) ([]byte, error) {
						cmd := exec.Command("bash", "-euo", "pipefail", "-c", script)
						cmd.Dir = dir
						cmd.Env = append(os.Environ(), "GITHUB_OUTPUT="+filepath.Join(dir, "outputs"))
						cmd.Env = append(cmd.Env, extra...)
						return cmd.CombinedOutput()
					}
					if out, err := run(seal); err != nil {
						t.Fatalf("producer seal: %v\n%s", err, out)
					}
					approved, err := os.ReadFile(filepath.Join(dir, "release/SHA256SUMS"))
					if err != nil {
						t.Fatal(err)
					}
					switch mutation {
					case "valid":
					case "extra", "hidden":
						file := "unexpected-install.sh"
						if mutation == "hidden" {
							file = ".unexpected"
						}
						writePipelineFile(t, filepath.Join(dir, "release", file), []byte("unexpected"))
					case "nested":
						if err := os.Mkdir(filepath.Join(dir, "release/nested"), 0700); err != nil {
							t.Fatal(err)
						}
					case "linked":
						archive := filepath.Join(dir, "release/otelcol-genai-sketches-1.2.3.tgz")
						if err := os.Rename(archive, filepath.Join(dir, "archive")); err != nil {
							t.Fatal(err)
						}
						if err := os.Symlink("../archive", archive); err != nil {
							t.Fatal(err)
						}
					default:
						writePipelineFile(t, filepath.Join(dir, "release", mutation), []byte("scanner substitution"))
						// Run the actual producer seal against altered bytes: a scanner's
						// internally consistent checksum set must not become approval.
						if out, err := run(seal); err != nil {
							t.Fatalf("reseal: %v\n%s", err, out)
						}
					}
					if job == "publish" {
						writePipelineFile(t, filepath.Join(dir, "release/provenance.jsonl"), []byte("signed bundle"))
					}
					out, err := run(pipelineScript(t, p, job, "sha256sum -c SHA256SUMS"),
						"SUMS="+fmt.Sprintf("%x", sha256.Sum256(approved)), "DIGEST="+digest,
						"CHART_DIGEST="+chartDigest, "IMAGE_NAME="+image, "CHART_NAME="+chart)
					if (err == nil) != (mutation == "valid") {
						t.Fatalf("substitution guard: %v\n%s", err, out)
					}
				})
			}
		}
	}
}
