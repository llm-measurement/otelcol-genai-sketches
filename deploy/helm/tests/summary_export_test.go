// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex
package charttest

import (
	"bytes"
	"io"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func summaryDocuments(t *testing.T, settings ...string) map[string]map[string]any {
	t.Helper()
	output, err := render(t, settings...)
	if err != nil {
		t.Fatalf("render: %v\n%s", err, output)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(output))
	docs := map[string]map[string]any{}
	for {
		var doc map[string]any
		if err := decoder.Decode(&doc); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		docs[doc["kind"].(string)] = doc
	}
	return docs
}

func nested(m map[string]any, keys ...string) map[string]any {
	for _, key := range keys {
		m = m[key].(map[string]any)
	}
	return m
}

func summaryConfig(t *testing.T, docs map[string]map[string]any) map[string]any {
	t.Helper()
	var config map[string]any
	err := yaml.Unmarshal([]byte(nested(docs["ConfigMap"], "data")["collector.yaml"].(string)), &config)
	if err != nil {
		t.Fatal(err)
	}
	return config
}

var enabledSummary = []string{"summaryExport.enabled=true", "summaryExport.producerID=app", "summaryExport.scopeID=team", "summaryExport.keyID=v1"}

func TestSummaryExportRendering(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		settings                  []string
		export, reader, pvc, json bool
		utility                   string
	}{
		{name: "defaults"},
		{name: "json-only", settings: []string{"telemetry.logs.encoding=json"}, json: true},
		{name: "emptyDir", settings: enabledSummary, export: true},
		{name: "reader", settings: append(append([]string{}, enabledSummary...), "summaryExport.reader.enabled=true", "telemetry.logs.encoding=json"), export: true, reader: true, json: true},
		{name: "mirror", settings: append(append([]string{}, enabledSummary...), "summaryExport.reader.enabled=true", "summaryExport.utilityImage.repository=registry.example:5000/mirrors/busybox", "summaryExport.utilityImage.digest=sha256:"+strings.Repeat("b", 64)), export: true, reader: true, utility: "registry.example:5000/mirrors/busybox@sha256:" + strings.Repeat("b", 64)},
		{name: "claim", settings: append(append([]string{}, enabledSummary...), "summaryExport.reader.enabled=true", "summaryExport.storage.type=existingClaim", "summaryExport.storage.existingClaim=summaries", "summaryExport.storage.sizeLimit=", "receiverTLS.enabled=true", "receiverTLS.existingSecret=tls", "shadow.enabled=true", "shadow.endpoint=existing:4317", "shadow.auth.existingSecret=backend", "image.digest=sha256:"+strings.Repeat("a", 64)), export: true, reader: true, pvc: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			docs := summaryDocuments(t, tc.settings...)
			deployment := nested(docs["Deployment"], "spec")
			if deployment["replicas"] != 1 || nested(deployment, "strategy")["type"] != "Recreate" {
				t.Fatal("one-writer strategy changed")
			}
			pod := nested(deployment, "template", "spec")
			if pod["automountServiceAccountToken"] != false {
				t.Fatal("credential automount enabled")
			}
			config := summaryConfig(t, docs)
			_, exported := nested(config, "connectors", "genaisketch")["summary_export"]
			if exported != tc.export {
				t.Fatal("export default changed")
			}
			_, logged := nested(config, "service")["telemetry"]
			if logged != tc.json {
				t.Fatal("log encoding not independent")
			}
			if logged && nested(config, "service", "telemetry", "logs")["encoding"] != "json" {
				t.Fatal("wrong encoding")
			}
			containers := pod["containers"].([]any)
			want := 1
			if tc.reader {
				want++
			}
			if len(containers) != want {
				t.Fatal("unexpected reader")
			}
			_, init := pod["initContainers"]
			if init != tc.export {
				t.Fatal("init/export pairing")
			}
			if !tc.export {
				if len(pod["volumes"].([]any)) != 1 {
					t.Fatal("default volume changed")
				}
				return
			}
			export := nested(config, "connectors", "genaisketch", "summary_export")
			if !reflect.DeepEqual(export, map[string]any{"directory": "/var/lib/genai-sketches/exports", "producer_id": "app", "scope_id": "team", "key_id": "v1", "interval": "5s"}) {
				t.Fatalf("wrong export config: %v", export)
			}
			if nested(pod, "securityContext")["fsGroupChangePolicy"] != "OnRootMismatch" {
				t.Fatal("remount policy missing")
			}
			volume := pod["volumes"].([]any)[0].(map[string]any)
			if tc.pvc {
				if nested(volume, "persistentVolumeClaim")["claimName"] != "summaries" {
					t.Fatal("wrong claim")
				}
			} else if nested(volume, "emptyDir")["sizeLimit"] != "128Mi" {
				t.Fatal("wrong trial size")
			}
			initializer := pod["initContainers"].([]any)[0].(map[string]any)
			image := initializer["image"].(string)
			wantImage := tc.utility
			if wantImage == "" {
				wantImage = "docker.io/library/busybox@sha256:5cec3fc171c87218698e85a52af7087de727372aae264a787b8112901a5b0092"
			}
			if image != wantImage {
				t.Fatal("utility image not pinned")
			}
			utilities := []map[string]any{initializer}
			if tc.reader {
				utilities = append(utilities, containers[1].(map[string]any))
			}
			for i, c := range utilities {
				if c["image"] != image || c["env"] != nil || c["envFrom"] != nil || c["ports"] != nil {
					t.Fatal("utility received credentials/ports or different image")
				}
				sc := nested(c, "securityContext")
				if sc["runAsNonRoot"] != true || sc["runAsUser"] != 65532 || sc["runAsGroup"] != 65532 || sc["allowPrivilegeEscalation"] != false || sc["readOnlyRootFilesystem"] != true || !reflect.DeepEqual(nested(sc, "capabilities")["drop"], []any{"ALL"}) {
					t.Fatalf("unsafe utility: %v", sc)
				}
				mounts := c["volumeMounts"].([]any)
				if len(mounts) != 1 {
					t.Fatal("utility credential mount")
				}
				m := mounts[0].(map[string]any)
				if m["name"] != "summary-export" || m["mountPath"] != "/var/lib/genai-sketches" || (i == 1 && m["readOnly"] != true) {
					t.Fatal("wrong utility mount")
				}
			}
			if tc.pvc {
				collector := containers[0].(map[string]any)
				if !strings.HasSuffix(collector["image"].(string), "@sha256:"+strings.Repeat("a", 64)) {
					t.Fatal("image digest lost")
				}
				if len(collector["env"].([]any)) != 2 || len(collector["volumeMounts"].([]any)) != 3 {
					t.Fatal("TLS/shadow config lost")
				}
			}
		})
	}
}

func TestSummaryExportRejectsUnsafeValues(t *testing.T) {
	for _, setting := range []string{
		"summaryExport.producerID=", "summaryExport.scopeID=", "summaryExport.keyID=", "summaryExport.keyID=has space",
		"summaryExport.producerID=" + strings.Repeat("a", 129), "summaryExport.interval=0s", "summaryExport.interval=61s", "summaryExport.interval=100ms", "summaryExport.interval=2m",
		"connector.retentionWindows=1", "connector.windowDuration=1s", "connector.windowDuration=25h", "connector.windowDuration=86401s", "connector.windowDuration=9999999999999999999h", "summaryExport.storage.type=hostPath", "summaryExport.storage.existingClaim=unexpected",
		"summaryExport.storage.sizeLimit=0Mi", "summaryExport.storage.type=existingClaim", "podSecurityContext.runAsUser=0", "podSecurityContext.runAsGroup=0", "podSecurityContext.fsGroup=0",
		"podSecurityContext.runAsNonRoot=false", "podSecurityContext.fsGroupChangePolicy=Always", "securityContext.runAsUser=0", "securityContext.runAsGroup=0", "securityContext.runAsNonRoot=false",
		"securityContext.readOnlyRootFilesystem=false", "securityContext.privileged=true", "securityContext.allowPrivilegeEscalation=true", "securityContext.capabilities.add=[\"CHOWN\"]",
		"replicaCount=2", "strategy.type=RollingUpdate", "telemetry.logs.encoding=xml", "summaryExport.directory=/tmp",
		"summaryExport.utilityImage=null", "summaryExport.utilityImage.repository=", "summaryExport.utilityImage.repository=busybox:latest",
		"summaryExport.utilityImage.repository=https://registry.example/busybox", "summaryExport.utilityImage.digest=", "summaryExport.utilityImage.digest=null",
		"summaryExport.utilityImage.digest=sha256:short", "summaryExport.utilityImage.digest=latest", "summaryExport.utilityImage.tag=latest",
	} {
		t.Run(setting, func(t *testing.T) {
			settings := append(append([]string{}, enabledSummary...), setting)
			if output, err := render(t, settings...); err == nil {
				t.Fatalf("accepted %s\n%s", setting, output)
			}
		})
	}
	if output, err := render(t, "summaryExport.reader.enabled=true"); err == nil {
		t.Fatalf("reader without export accepted\n%s", output)
	}
	if _, err := render(t, append(append([]string{}, enabledSummary...), "summaryExport.interval=1m", "connector.windowDuration=2m")...); err != nil {
		t.Fatal(err)
	}
}
