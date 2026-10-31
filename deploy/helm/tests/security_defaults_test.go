// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex
package charttest

import (
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestNetworkPolicyDefaults(t *testing.T) {
	docs := summaryDocuments(t)
	spec := nested(docs["NetworkPolicy"], "spec")
	if !reflect.DeepEqual(spec["policyTypes"], []any{"Ingress", "Egress"}) {
		t.Fatal("both directions must be isolated")
	}
	selector := nested(spec, "podSelector", "matchLabels")
	podLabels := nested(docs["Deployment"], "spec", "template", "metadata", "labels")
	for key, value := range selector {
		if podLabels[key] != value {
			t.Fatal("policy does not select the collector")
		}
	}
	ingress := spec["ingress"].([]any)
	if len(ingress) != 1 {
		t.Fatal("defaults must allow only same-namespace OTLP")
	}
	otlp := ingress[0].(map[string]any)
	if !reflect.DeepEqual(otlp["from"], []any{map[string]any{"podSelector": map[string]any{}}}) ||
		!reflect.DeepEqual(otlp["ports"], []any{
			map[string]any{"protocol": "TCP", "port": 4317},
			map[string]any{"protocol": "TCP", "port": 4318},
		}) {
		t.Fatalf("unbounded default ingress: %v", ingress)
	}
	egress := spec["egress"].([]any)
	if len(egress) != 1 {
		t.Fatal("only DNS should be allowed")
	}
	dns := egress[0].(map[string]any)
	if !reflect.DeepEqual(dns["to"], []any{map[string]any{
		"namespaceSelector": map[string]any{"matchLabels": map[string]any{"kubernetes.io/metadata.name": "kube-system"}},
		"podSelector":       map[string]any{"matchLabels": map[string]any{"k8s-app": "kube-dns"}},
	}}) || !reflect.DeepEqual(dns["ports"], []any{
		map[string]any{"protocol": "UDP", "port": 53},
		map[string]any{"protocol": "TCP", "port": 53},
	}) {
		t.Fatalf("unbounded DNS egress: %v", egress)
	}
}

func TestNetworkPolicyOverrides(t *testing.T) {
	if _, ok := summaryDocuments(t, "networkPolicy.enabled=false")["NetworkPolicy"]; ok {
		t.Fatal("explicit opt-out still renders a policy")
	}
	spec := nested(summaryDocuments(t, "networkPolicy.allowSameNamespace=false", "networkPolicy.allowDNS=false")["NetworkPolicy"], "spec")
	if len(spec["ingress"].([]any)) != 0 || len(spec["egress"].([]any)) != 0 {
		t.Fatal("empty allow-lists must deny both directions")
	}
	peer := `[{"namespaceSelector":{"matchLabels":{"kubernetes.io/metadata.name":"applications"}},"podSelector":{"matchLabels":{"app":"sender"}}}]`
	spec = nested(summaryDocuments(t,
		"networkPolicy.allowSameNamespace=false", "networkPolicy.otlpFrom="+peer,
		"networkPolicy.metricsFrom="+peer, "service.otlpGrpcPort=14317",
		"service.otlpHTTPPort=14318", "service.metricsPort=18889",
		`networkPolicy.ingress=[{"from":[{"ipBlock":{"cidr":"192.0.2.0/24"}}],"ports":[{"port":9000}]}]`,
		`networkPolicy.egress=[{"to":[{"ipBlock":{"cidr":"192.0.2.1/32"}}],"ports":[{"port":4317}]}]`,
	)["NetworkPolicy"], "spec")
	rules := spec["ingress"].([]any)
	if len(rules) != 3 || len(spec["egress"].([]any)) != 2 {
		t.Fatal("raw rules must be retained alongside presets")
	}
	for i, ports := range [][]any{
		{map[string]any{"protocol": "TCP", "port": 14317}, map[string]any{"protocol": "TCP", "port": 14318}},
		{map[string]any{"protocol": "TCP", "port": 18889}},
	} {
		rule := rules[i+1].(map[string]any)
		peers := rule["from"].([]any)
		if len(peers) != 1 || len(peers[0].(map[string]any)) != 2 || !reflect.DeepEqual(rule["ports"], ports) {
			t.Fatal("peer selectors must stay combined; ports must follow the Service")
		}
	}
}

func TestReceiverMutualTLS(t *testing.T) {
	docs := summaryDocuments(t, "receiverTLS.enabled=true", "receiverTLS.existingSecret=receiver-tls",
		"receiverTLS.certKey=server.pem", "receiverTLS.privateKeyKey=server-key.pem", "receiverTLS.clientCAKey=clients.pem")
	protocols := nested(summaryConfig(t, docs), "receivers", "otlp", "protocols")
	for _, protocol := range []string{"grpc", "http"} {
		tls := nested(protocols, protocol, "tls")
		if tls["client_ca_file"] != "/var/run/genai-sketches/tls/ca.crt" ||
			tls["cert_file"] != "/var/run/genai-sketches/tls/tls.crt" ||
			tls["key_file"] != "/var/run/genai-sketches/tls/tls.key" {
			t.Fatal("mutual TLS missing for " + protocol)
		}
	}
	pod := nested(docs["Deployment"], "spec", "template", "spec")
	found := false
	for _, v := range pod["volumes"].([]any) {
		volume := v.(map[string]any)
		if secret, ok := volume["secret"].(map[string]any); ok && secret["secretName"] == "receiver-tls" {
			found = reflect.DeepEqual(secret["items"], []any{
				map[string]any{"key": "server.pem", "path": "tls.crt"},
				map[string]any{"key": "server-key.pem", "path": "tls.key"},
				map[string]any{"key": "clients.pem", "path": "ca.crt"},
			})
		}
	}
	if !found {
		t.Fatal("client CA and server keys must be projected from the selected Secret")
	}
}

func TestSecurityDefaultsRejectUnsafeValues(t *testing.T) {
	for _, setting := range []string{
		"securityContexts.privileged=true", "securityContext.privileged=true",
		"securityContext.allowPrivilegeEscalation=true", "securityContext.readOnlyRootFilesystem=false",
		"securityContext.capabilities.drop=[]", "securityContext.capabilities.add=[\"NET_ADMIN\"]",
		"podSecurityContext.runAsNonRoot=false", "podSecurityContext.runAsUser=0",
		"podSecurityContext.seccompProfile.type=Unconfined", "securityContext.runAsUser=0",
		"image.digset=typo", "service.otlpGrpcPort=0", "service.metricsPort=65536",
		"connector.maxSlice=10", "receiverTLS.clientCAKeey=typo", "shadow.insecur=true",
		"networkPolicy.enable=true", "networkPolicy.allowSameNamespace=yes",
		"networkPolicy.otlpFrom=[{}]", "networkPolicy.metricsFrom=[{\"podSelektor\":{}}]",
		"networkPolicy.otlpFrom=[{\"podSelector\":{\"matchLabel\":{\"app\":\"sender\"}}}]",
		"networkPolicy.ingress=[{\"form\":[{\"podSelector\":{}}]}]",
		"networkPolicy.egress=[{\"ports\":[{\"port\":65536}]}]",
		"networkPolicy.otlpFrom=[{\"ipBlock\":{\"cidr\":\"192.0.2.0/24\"},\"podSelector\":{}}]",
		"receiverTLS.enabled=true",
	} {
		t.Run(setting, func(t *testing.T) {
			if output, err := render(t, setting); err == nil {
				t.Fatalf("unsafe or misspelled setting accepted: %s\n%s", setting, output)
			}
		})
	}
}

func TestImageDigestOverrides(t *testing.T) {
	for _, tc := range []struct {
		settings []string
		want     string
	}{
		{nil, "ghcr.io/llm-measurement/otelcol-genai-sketches:0.7.0"},
		{[]string{"image.repository=registry.example/collector", "image.digest=sha256:" + strings.Repeat("a", 64)}, "registry.example/collector@sha256:" + strings.Repeat("a", 64)},
		{[]string{"image.digest=", "image.tag=custom"}, "ghcr.io/llm-measurement/otelcol-genai-sketches:custom"},
	} {
		docs := summaryDocuments(t, tc.settings...)
		pod := nested(docs["Deployment"], "spec", "template", "spec")
		if got := pod["containers"].([]any)[0].(map[string]any)["image"]; got != tc.want {
			t.Fatalf("image override got %s, want %s", got, tc.want)
		}
	}
}

func TestSelectedPeersPreset(t *testing.T) {
	helm := os.Getenv("HELM")
	if helm == "" {
		var err error
		helm, err = exec.LookPath("helm")
		if err != nil {
			t.Skip("set HELM to run chart tests")
		}
	}
	output, err := exec.Command(helm, "template", "test", "../otelcol-genai-sketches",
		"-f", "../otelcol-genai-sketches/presets/selected-peers.yaml").CombinedOutput()
	if err != nil || !strings.Contains(string(output), "telemetry-sender") || !strings.Contains(string(output), "prometheus") {
		t.Fatalf("selected-peers preset: %v\n%s", err, output)
	}
}
