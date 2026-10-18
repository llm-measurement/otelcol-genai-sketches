// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func chartPublishWorkspace(t *testing.T) (string, string) {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, name := range []string{"bin", "release", "scratch"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	return root, dir
}

func executable(t *testing.T, path, source string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(source), 0700); err != nil {
		t.Fatal(err)
	}
}

func runChartPublish(t *testing.T, root, dir, repository, version string, extra ...string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command("bash", filepath.Join(root, "scripts/publish-chart-digest.sh"))
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PATH="+filepath.Join(dir, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
		"CHART_NAME="+repository, "CHART_VERSION="+version, "RUNNER_TEMP="+filepath.Join(dir, "scratch"), "GITHUB_OUTPUT="+filepath.Join(dir, "outputs"))
	cmd.Env = append(cmd.Env, extra...)
	return cmd.CombinedOutput()
}

func TestChartDigestUploadFailsClosed(t *testing.T) {
	for _, failure := range []string{"none", "config", "archive", "manifest", "resolve", "wrong-digest"} {
		t.Run(failure, func(t *testing.T) {
			root, dir := chartPublishWorkspace(t)
			writePipelineFile(t, filepath.Join(dir, "release/otelcol-genai-sketches-1.2.3.tgz"), []byte("packaged-chart"))
			executable(t, filepath.Join(dir, "bin/helm"), "#!/bin/sh\nprintf 'apiVersion: v2\\nname: otelcol-genai-sketches\\nversion: 1.2.3\\n'\n")
			executable(t, filepath.Join(dir, "bin/go"), "#!/bin/sh\ncat >/dev/null\nprintf '%s' '{\"apiVersion\":\"v2\",\"name\":\"otelcol-genai-sketches\",\"version\":\"1.2.3\"}'\n")
			executable(t, filepath.Join(dir, "bin/oras"), `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> calls
case "$1 $2" in
  'blob push')
    stage=archive
    [[ "$4" != */config.json ]] || stage=config
    [[ "$FAILURE" != "$stage" ]] || exit 21
    test "${3##*@}" = "sha256:$(sha256sum "$4" | cut -d ' ' -f 1)"
    cp "$4" "$stage.bytes"
    ;;
  'manifest push')
    [[ "$FAILURE" != manifest ]] || exit 22
    test "${3##*@}" = "sha256:$(sha256sum "$4" | cut -d ' ' -f 1)"
    cp "$4" manifest.json
    ;;
  resolve*)
    [[ "$FAILURE" != resolve ]] || exit 23
    if [[ "$FAILURE" == wrong-digest ]]; then printf 'sha256:wrong\n'; else printf '%s\n' "${2##*@}"; fi
    ;;
  *) exit 99 ;;
esac
`)
			output, err := runChartPublish(t, root, dir, "registry.example/charts/otelcol-genai-sketches", "1.2.3", "FAILURE="+failure)
			if (err == nil) != (failure == "none") {
				t.Fatalf("unexpected upload result: %v\n%s", err, output)
			}
			if failure != "none" {
				for _, path := range []string{"outputs", "release/chart-digest.txt"} {
					if _, err := os.Stat(filepath.Join(dir, path)); !os.IsNotExist(err) {
						t.Fatalf("failed upload emitted approval %s", path)
					}
				}
				return
			}
			manifest, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
			if err != nil {
				t.Fatal(err)
			}
			var parsed struct {
				SchemaVersion int    `json:"schemaVersion"`
				MediaType     string `json:"mediaType"`
				Config        struct {
					MediaType string
					Digest    string
					Size      int64
				}
				Layers []struct {
					MediaType string
					Digest    string
					Size      int64
				}
			}
			if err := json.Unmarshal(manifest, &parsed); err != nil {
				t.Fatal(err)
			}
			config, err := os.ReadFile(filepath.Join(dir, "config.bytes"))
			if err != nil {
				t.Fatal(err)
			}
			archive, err := os.ReadFile(filepath.Join(dir, "archive.bytes"))
			if err != nil {
				t.Fatal(err)
			}
			if parsed.SchemaVersion != 2 || parsed.MediaType != "application/vnd.oci.image.manifest.v1+json" || parsed.Config.MediaType != "application/vnd.cncf.helm.config.v1+json" || parsed.Config.Digest != fmt.Sprintf("sha256:%x", sha256.Sum256(config)) || parsed.Config.Size != int64(len(config)) || len(parsed.Layers) != 1 || parsed.Layers[0].MediaType != "application/vnd.cncf.helm.chart.content.v1.tar+gzip" || parsed.Layers[0].Digest != fmt.Sprintf("sha256:%x", sha256.Sum256(archive)) || parsed.Layers[0].Size != int64(len(archive)) {
				t.Fatalf("invalid Helm OCI manifest: %s", manifest)
			}
			digest := fmt.Sprintf("sha256:%x", sha256.Sum256(manifest))
			approval, err := os.ReadFile(filepath.Join(dir, "outputs"))
			if err != nil {
				t.Fatal(err)
			}
			if string(approval) != "digest="+digest+"\n" {
				t.Fatalf("wrong manifest approval: %s", approval)
			}
			calls, err := os.ReadFile(filepath.Join(dir, "calls"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(calls), " tag ") || !strings.Contains(string(calls), "manifest push registry.example/charts/otelcol-genai-sketches@"+digest) {
				t.Fatalf("tagged or unbound upload: %s", calls)
			}
		})
	}
}

func TestChartDigestRoundTripWithHelmAndORAS(t *testing.T) {
	helm := os.Getenv("HELM")
	if helm == "" {
		helm = "helm"
	}
	helm, helmErr := exec.LookPath(helm)
	oras, orasErr := exec.LookPath("oras")
	if helmErr != nil || orasErr != nil {
		if os.Getenv("RELEASE_OCI_TEST") == "1" {
			t.Fatal("Helm and ORAS are required for the release OCI gate")
		}
		t.Skip("Helm and ORAS are required for the offline OCI round-trip")
	}
	root, dir := chartPublishWorkspace(t)
	for name, target := range map[string]string{"helm": helm, "go": filepath.Join(runtime.GOROOT(), "bin/go")} {
		if err := os.Symlink(target, filepath.Join(dir, "bin", name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"go.mod", "go.sum", "scripts"} {
		if err := os.Symlink(filepath.Join(root, name), filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	// Only a local OCI layout is writable; the registry below serves those bytes read-only.
	executable(t, filepath.Join(dir, "bin/oras"), fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
case "$1" in
  blob|manifest) command=$1; operation=$2; shift 2; exec %q "$command" "$operation" --oci-layout "$@" ;;
  resolve) shift; exec %q resolve --oci-layout "$@" ;;
  *) exit 99 ;;
esac
`, oras, oras))
	cmd := exec.Command(helm, "package", filepath.Join(root, "deploy/helm/otelcol-genai-sketches"), "--destination", filepath.Join(dir, "release"))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("helm package: %v\n%s", err, output)
	}
	packages, err := filepath.Glob(filepath.Join(dir, "release/*.tgz"))
	if err != nil || len(packages) != 1 {
		t.Fatalf("chart package: %v %v", packages, err)
	}
	version := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(packages[0]), "otelcol-genai-sketches-"), ".tgz")
	layout := filepath.Join(dir, "layout")
	if output, err := runChartPublish(t, root, dir, layout, version); err != nil {
		t.Fatalf("local OCI upload: %v\n%s", err, output)
	}
	approval, err := os.ReadFile(filepath.Join(dir, "outputs"))
	if err != nil {
		t.Fatal(err)
	}
	digest := strings.TrimSpace(strings.TrimPrefix(string(approval), "digest="))
	manifest, err := os.ReadFile(filepath.Join(layout, "blobs/sha256", strings.TrimPrefix(digest, "sha256:")))
	if err != nil {
		t.Fatal(err)
	}
	var descriptor struct {
		Config      struct{ Digest string }
		Annotations map[string]string
	}
	if err := json.Unmarshal(manifest, &descriptor); err != nil {
		t.Fatal(err)
	}
	config, err := os.ReadFile(filepath.Join(layout, "blobs/sha256", strings.TrimPrefix(descriptor.Config.Digest, "sha256:")))
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.Annotations["org.opencontainers.image.title"] != "otelcol-genai-sketches" || descriptor.Annotations["org.opencontainers.image.version"] != version || descriptor.Annotations["artifacthub.io/license"] != "Apache-2.0" {
		t.Fatalf("Helm/ArtifactHub OCI annotations lost: %s", manifest)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			t.Errorf("registry mutation: %s", r.Method)
			w.WriteHeader(405)
			return
		}
		w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
		if r.URL.Path == "/v2/" || r.URL.Path == "/v2" {
			w.WriteHeader(200)
			return
		}
		prefix := "/v2/charts/otelcol-genai-sketches/"
		var data []byte
		if r.URL.Path == prefix+"manifests/"+digest {
			data = manifest
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		} else if strings.HasPrefix(r.URL.Path, prefix+"blobs/sha256:") {
			hash := strings.TrimPrefix(r.URL.Path, prefix+"blobs/sha256:")
			if len(hash) != 64 {
				http.NotFound(w, r)
				return
			}
			var err error
			data, err = os.ReadFile(filepath.Join(layout, "blobs/sha256", hash))
			if err != nil {
				http.NotFound(w, r)
				return
			}
		} else {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Docker-Content-Digest", fmt.Sprintf("sha256:%x", sha256.Sum256(data)))
		w.Header().Set("Content-Length", fmt.Sprint(len(data)))
		if r.Method == "GET" {
			_, _ = w.Write(data)
		}
	}))
	defer server.Close()
	ref := "oci://" + strings.TrimPrefix(server.URL, "http://") + "/charts/otelcol-genai-sketches@" + digest
	destination := filepath.Join(dir, "download")
	if err := os.Mkdir(destination, 0700); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command(helm, "pull", ref, "--plain-http", "--destination", destination)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Helm digest pull: %v\n%s", err, output)
	}
	want, err := os.ReadFile(packages[0])
	if err != nil {
		t.Fatal(err)
	}
	downloaded, err := filepath.Glob(filepath.Join(destination, "*.tgz"))
	if err != nil || len(downloaded) != 1 {
		t.Fatalf("digest-addressed chart package: %v %v", downloaded, err)
	}
	got, err := os.ReadFile(downloaded[0])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("Helm OCI round-trip changed chart bytes")
	}
	cmd = exec.Command(helm, "show", "chart", ref, "--plain-http")
	shown, err := cmd.Output()
	if err != nil {
		t.Fatalf("Helm show chart by digest: %v", err)
	}
	// Helm 4.2.4 includes these two registry status records on stdout.
	// Check their binding, then compare every actual metadata field.
	var metadata map[string]any
	if err := yaml.Unmarshal(shown, &metadata); err != nil {
		t.Fatal(err)
	}
	for key, expected := range map[string]string{"Pulled": strings.TrimPrefix(ref, "oci://"), "Digest": digest} {
		if value, exists := metadata[key]; exists && value != expected {
			t.Fatalf("unexpected Helm status %s=%v", key, value)
		}
		delete(metadata, key)
	}
	shownConfig, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(shownConfig, config) {
		t.Fatalf("Helm show changed metadata:\n%s\n%s", shownConfig, config)
	}
}

func TestReleasePublicationRetryNeverOverwrites(t *testing.T) {
	for _, workflow := range []string{"release.yml", "chart-release.yml"} {
		p := readPipeline(t, workflow)
		preflight := pipelineScript(t, p, "publish", "refusing to overwrite")
		create := pipelineScript(t, p, "publish", "gh release create")
		tag := pipelineScript(t, p, "publish", "oras tag")
		for _, forbidden := range []string{"--clobber", "gh release upload", "gh release edit", "gh release delete"} {
			if strings.Contains(create, forbidden) {
				t.Fatalf("publication can overwrite: %s", forbidden)
			}
		}
		for _, scenario := range []string{"fresh", "fresh-wrong-source", "fresh-missing-source", "fresh-tag-lookup-failure", "retry-before-tag", "existing-tag", "partial-create", "wrong-tag", "wrong-source", "missing-source", "tag-lookup-failure", "draft", "changed-asset", "missing-asset", "extra-asset", "download-failure", "view-failure"} {
			t.Run(workflow+"/"+scenario, func(t *testing.T) {
				_, dir := chartPublishWorkspace(t)
				for _, name := range []string{"SHA256SUMS", "chart-digest.txt", "otelcol-genai-sketches-1.2.3.tgz", "provenance.jsonl"} {
					writePipelineFile(t, filepath.Join(dir, "release", name), []byte("approved "+name))
				}
				ref := "chart-v1.2.3"
				if workflow == "release.yml" {
					ref = "v1.2.3"
				}
				commit := strings.Repeat("c", 40)
				// GitHub need not store the requested commitish when the tag exists.
				metadata := map[string]any{"tagName": ref, "targetCommitish": "main", "isDraft": false}
				writePipelineFile(t, filepath.Join(dir, "source-commit"), []byte(commit+"\n"))
				executable(t, filepath.Join(dir, "bin/gh"), `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> gh.calls
case "$1 $2" in
  'api --method')
    [[ "$3" == GET && "$4" == "repos/$GITHUB_REPOSITORY/commits/refs/tags/$GITHUB_REF_NAME" && "$5" == --jq && "$6" == .sha ]] || exit 20
    [[ "$FAILURE" != *tag-lookup-failure ]] || exit 21
    cat source-commit
    ;;
  'release view')
    [[ "$FAILURE" != view-failure ]] || exit 23
    cat remote.json
    ;;
  'release create')
    [[ ! -e remote.json ]] || exit 24
    [[ "$3" == "$GITHUB_REF_NAME" ]] || exit 25
    found=false
    for ((i=1; i<=$#; i++)); do
      if [[ "${!i}" == --target ]]; then
        j=$((i+1)); [[ "${!j}" == "$GITHUB_SHA" ]] || exit 26; found=true
      fi
    done
    [[ "$found" == true ]] || exit 27
    jq -n --arg tag "$GITHUB_REF_NAME" \
      '{tagName:$tag,targetCommitish:"main",isDraft:false}' > remote.json
    mkdir remote
    if [[ "$FAILURE" == partial-create ]]; then cp release/SHA256SUMS remote/; exit 28; fi
    cp release/* remote/
    ;;
  'release download')
    [[ "$FAILURE" != download-failure ]] || exit 29
    [[ "$3" == "$GITHUB_REF_NAME" && "$4" == --dir ]] || exit 30
    cp remote/* "$5/"
    ;;
  *) exit 99 ;;
esac
`)
				executable(t, filepath.Join(dir, "bin/curl"), `#!/usr/bin/env bash
set -euo pipefail
if [[ "$*" == *'https://ghcr.io/token?'* ]]; then
  printf '{"token":"test-token"}'
elif [[ -f chart-tag ]]; then printf 200; else printf 404; fi
`)
				executable(t, filepath.Join(dir, "bin/oras"), `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> oras.calls
case "$1" in
  tag)
    [[ "$FAILURE" != tag ]] || exit 31
    if [[ "$2" == "$CHART_NAME@$CHART_DIGEST" ]]; then
      [[ ! -f chart-tag ]] || exit 32
      printf '%s\n' "$CHART_DIGEST" > chart-tag
    else
      [[ "$2" == "$IMAGE_NAME@$DIGEST" ]] || exit 33
    fi
    ;;
  resolve)
    if [[ "$2" == "$CHART_NAME:"* ]]; then cat chart-tag; else printf '%s\n' "$DIGEST"; fi
    ;;
  *) exit 99 ;;
esac
`)
				run := func(script, failure string) error {
					cmd := exec.Command("bash", "-euo", "pipefail", "-c", script)
					cmd.Dir = dir
					cmd.Env = append(os.Environ(), "PATH="+filepath.Join(dir, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
						"GITHUB_REF_NAME="+ref, "GITHUB_SHA="+commit, "GITHUB_REPOSITORY=example/collector", "RUNNER_TEMP="+filepath.Join(dir, "scratch"),
						"CHART_NAME=ghcr.io/example/charts/collector", "CHART_VERSION=1.2.3", "VERSION=1.2.3", "IMAGE_NAME=ghcr.io/example/collector",
						"IMAGE_TAGS=ghcr.io/example/collector:1.2.3", "DIGEST=sha256:"+strings.Repeat("a", 64),
						"CHART_DIGEST=sha256:"+strings.Repeat("b", 64), "FAILURE="+failure)
					out, err := cmd.CombinedOutput()
					if err != nil {
						return fmt.Errorf("%w: %s", err, out)
					}
					return nil
				}
				publish := func(failure string) error {
					for _, script := range []string{preflight, create, tag} {
						if err := run(script, failure); err != nil {
							return err
						}
					}
					return nil
				}
				if strings.HasPrefix(scenario, "fresh-") {
					if scenario == "fresh-wrong-source" {
						writePipelineFile(t, filepath.Join(dir, "source-commit"), []byte(strings.Repeat("d", 40)))
					}
					if scenario == "fresh-missing-source" {
						writePipelineFile(t, filepath.Join(dir, "source-commit"), nil)
					}
					if err := publish(scenario); err == nil {
						t.Fatal("invalid source permitted publication")
					}
					calls, err := os.ReadFile(filepath.Join(dir, "gh.calls"))
					if err != nil {
						t.Fatal(err)
					}
					if strings.Contains(string(calls), "release ") {
						t.Fatal("invalid source reached release commands")
					}
					if _, err := os.Stat(filepath.Join(dir, "oras.calls")); !os.IsNotExist(err) {
						t.Fatal("invalid source reached tag commands")
					}
					return
				}
				if scenario == "fresh" {
					if err := publish(""); err != nil {
						t.Fatal(err)
					}
					return
				}
				if scenario == "partial-create" {
					if err := publish(scenario); err == nil {
						t.Fatal("partial upload succeeded")
					}
				} else if scenario == "existing-tag" {
					if err := publish(""); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := publish("tag"); err == nil {
						t.Fatal("failed tag accepted")
					}
				}
				if _, err := os.Stat(filepath.Join(dir, "remote.json")); err != nil {
					t.Fatalf("first attempt did not create the release: %v", err)
				}
				switch scenario {
				case "wrong-tag":
					metadata["tagName"] = "chart-v9.9.9"
				case "wrong-source":
					writePipelineFile(t, filepath.Join(dir, "source-commit"), []byte(strings.Repeat("d", 40)))
				case "missing-source":
					writePipelineFile(t, filepath.Join(dir, "source-commit"), nil)
				case "draft":
					metadata["isDraft"] = true
				case "changed-asset":
					writePipelineFile(t, filepath.Join(dir, "remote/SHA256SUMS"), []byte("changed"))
				case "missing-asset":
					if err := os.Remove(filepath.Join(dir, "remote/SHA256SUMS")); err != nil {
						t.Fatal(err)
					}
				case "extra-asset":
					writePipelineFile(t, filepath.Join(dir, "remote/extra"), []byte("extra"))
				}
				if scenario == "wrong-tag" || scenario == "draft" {
					data, err := json.Marshal(metadata)
					if err != nil {
						t.Fatal(err)
					}
					writePipelineFile(t, filepath.Join(dir, "remote.json"), data)
				}
				remoteSnapshot := func() string {
					paths, err := filepath.Glob(filepath.Join(dir, "remote/*"))
					if err != nil {
						t.Fatal(err)
					}
					paths = append(paths, filepath.Join(dir, "remote.json"))
					state := map[string]string{}
					for _, path := range paths {
						data, err := os.ReadFile(path)
						if err != nil {
							t.Fatal(err)
						}
						state[path] = string(data)
					}
					data, err := json.Marshal(state)
					if err != nil {
						t.Fatal(err)
					}
					return string(data)
				}
				publishedBefore := remoteSnapshot()
				before, _ := os.ReadFile(filepath.Join(dir, "oras.calls"))
				err := publish(scenario)
				if (err == nil) != (scenario == "retry-before-tag") {
					t.Fatalf("retry: %v", err)
				}
				if publishedBefore != remoteSnapshot() {
					t.Fatal("retry changed published release assets or metadata")
				}
				after, _ := os.ReadFile(filepath.Join(dir, "oras.calls"))
				if scenario != "retry-before-tag" && !bytes.Equal(before, after) {
					t.Fatal("rejected retry attempted a tag")
				}
				calls, err := os.ReadFile(filepath.Join(dir, "gh.calls"))
				if err != nil {
					t.Fatal(err)
				}
				creates := strings.Count(string(calls), "release create ")
				wantCreates := 1
				if scenario == "view-failure" {
					wantCreates = 2
				} // The server rejects a duplicate create; nothing is overwritten.
				if creates != wantCreates {
					t.Fatalf("create calls: %d\n%s", creates, calls)
				}
			})
		}
	}
}
