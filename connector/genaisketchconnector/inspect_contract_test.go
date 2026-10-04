// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package genaisketchconnector

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

// The identical versioned corpus is also consumed by fleetdiff. Expectations are
// hand-specified, not recorded from either implementation during a test run.
func TestInspectAccountingContract(t *testing.T) {
	dir := filepath.Join("testdata", "inspect-contract", "v1")
	files := verifyInspectContractChecksums(t, dir)
	var manifest struct {
		Schema       string `json:"schema"`
		AccountingID string `json:"accounting_id"`
		Source       struct {
			Repository string `json:"repository"`
			Revision   string `json:"revision"`
		} `json:"source"`
		Cases []struct {
			Name     string `json:"name"`
			Input    string `json:"input"`
			Expected struct {
				Metrics           map[string]uint64 `json:"metrics"`
				ObservedCounters  map[string]uint64 `json:"observed_counters"`
				TokenObservations map[string]uint64 `json:"token_observations"`
				UsageProvenance   map[string]uint64 `json:"usage_provenance"`
			} `json:"expected"`
			Error bool `json:"error"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(files["manifest.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Schema != "genai-inspect-accounting/v1" || manifest.AccountingID != "genai-default-accounting/v1+usage-provenance/v1" {
		t.Fatal("unexpected inspect contract version")
	}
	if manifest.Source.Repository != "https://github.com/llm-measurement/otelcol-genai-sketches" || len(manifest.Source.Revision) != 40 {
		t.Fatal("inspect contract must name its reference repository and revision")
	}
	if len(manifest.Cases) != 36 || len(files) != len(manifest.Cases)+1 {
		t.Fatal("missing cases or unreferenced contract inputs")
	}
	names, inputs := map[string]bool{}, map[string]bool{}
	for _, tc := range manifest.Cases {
		if tc.Name == "" || names[tc.Name] || inputs[tc.Input] || tc.Input == "manifest.json" || files[tc.Input] == nil {
			t.Fatal("duplicate case or invalid input reference")
		}
		names[tc.Name], inputs[tc.Input] = true, true
		t.Run(tc.Name, func(t *testing.T) {
			traces, err := (&ptrace.JSONUnmarshaler{}).UnmarshalTraces(files[tc.Input])
			if err != nil {
				t.Fatalf("fixture is not an OTLP ExportTraceServiceRequest: %v", err)
			}
			cfg := defaultConfig()
			// An absent, fixed key gives every span the same global slice without
			// changing the default operation, field, token, or provenance mappings.
			cfg.Slices = []SliceConfig{{Name: "global", Keys: []string{"inspect.contract.global"}}}
			cfg.MaxSlices = 1
			if cfg.Dedup.Enabled || cfg.MCP.Enabled || cfg.TopK != 20 {
				t.Fatal("inspect contract config no longer matches collector defaults")
			}
			if err := cfg.Validate(); err != nil {
				t.Fatal(err)
			}
			state := newFleetFixtureState(t, cfg)
			metrics, ok, err := state.ConsumeTraces(context.Background(), traces)
			if tc.Error {
				if len(tc.Expected.Metrics)+len(tc.Expected.ObservedCounters)+len(tc.Expected.TokenObservations)+len(tc.Expected.UsageProvenance) != 0 {
					t.Fatal("error cases must not promise partial-success counters")
				}
				if err == nil || ok {
					t.Fatal("expected accounting error, not a partial success")
				}
				if strings.Contains(err.Error(), "INSPECT_CONTRACT_PRIVATE") {
					t.Fatal("sentinel in accounting error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				metrics = pmetric.NewMetrics()
			}
			gotMetrics := make(map[string]uint64, len(accountingMetricNames))
			for _, name := range accountingMetricNames {
				gotMetrics[name] = uint64(sumMetricValues(metrics, name))
			}
			if !reflect.DeepEqual(gotMetrics, tc.Expected.Metrics) {
				t.Errorf("metrics:\n got %v\nwant %v", gotMetrics, tc.Expected.Metrics)
			}
			wantObserved := tc.Expected.ObservedCounters
			if wantObserved == nil {
				wantObserved = tc.Expected.Metrics
			}
			gotObserved := make(map[string]uint64, len(accountingMetricNames))
			for _, name := range accountingMetricNames {
				gotObserved[name] = 0
			}
			// Inspect the stored uint64 counters before appendSum clamps them.
			// The global slice and its empty overflow slice must not lose weight.
			for _, slice := range state.metricSlices() {
				total, ok := addUint64(slice.inputTokens, slice.outputTokens)
				if !ok {
					t.Fatal("successful contract counter total exceeds uint64")
				}
				for name, value := range map[string]uint64{
					requestsMetricName:              slice.requests,
					agentRunsMetricName:             slice.agentRuns,
					inputTokensMetricName:           slice.inputTokens,
					outputTokensMetricName:          slice.outputTokens,
					totalTokensMetricName:           total,
					cacheReadInputTokensMetricName:  slice.cacheReadInputTokens,
					cacheWriteInputTokensMetricName: slice.cacheWriteInputTokens,
					reasoningOutputTokensMetricName: slice.reasoningOutputTokens,
					missingTokenUsageMetricName:     slice.missingTokens,
					dedupSuppressedMetricName:       slice.dedupSuppressed,
					dedupKeyMissingMetricName:       slice.dedupKeyMissing,
				} {
					gotObserved[name], ok = addUint64(gotObserved[name], value)
					if !ok {
						t.Fatalf("successful contract counter exceeds uint64: %s", name)
					}
				}
			}
			if !reflect.DeepEqual(gotObserved, wantObserved) {
				t.Errorf("observed counters:\n got %v\nwant %v", gotObserved, wantObserved)
			}
			gotObservations := make(map[string]uint64)
			for _, field := range tokenFieldNames {
				for _, quality := range tokenObservationStateNames {
					labels := map[string]string{"token_field": field, "state": quality}
					gotObservations[field+"/"+quality] = uint64(sumMetricValuesWithLabels(metrics, tokenFieldObservationsMetricName, labels))
				}
			}
			if !reflect.DeepEqual(gotObservations, tc.Expected.TokenObservations) {
				t.Errorf("token observations:\n got %v\nwant %v", gotObservations, tc.Expected.TokenObservations)
			}
			gotProvenance := make(map[string]uint64)
			for _, field := range usageProvenanceFields {
				for _, origin := range usageProvenanceStates {
					labels := map[string]string{"token_field": field, "source": origin}
					gotProvenance[field+"/"+origin] = uint64(sumMetricValuesWithLabels(metrics, usageProvenanceMetricName, labels))
				}
			}
			if !reflect.DeepEqual(gotProvenance, tc.Expected.UsageProvenance) {
				t.Errorf("usage provenance:\n got %v\nwant %v", gotProvenance, tc.Expected.UsageProvenance)
			}
			if bytes.Contains(marshalMetrics(t, metrics), []byte("INSPECT_CONTRACT_PRIVATE")) {
				t.Fatal("sentinel leaked into a metric or label")
			}
		})
	}
}

func verifyInspectContractChecksums(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	sums, err := os.ReadFile(filepath.Join(dir, "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, line := range strings.Split(strings.TrimSpace(string(sums)), "\n") {
		want, name, ok := strings.Cut(line, "  ")
		if !ok || filepath.Base(name) != name || filepath.Ext(name) != ".json" || files[name] != nil {
			t.Fatal("invalid or duplicate checksum entry")
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(data)
		if hex.EncodeToString(digest[:]) != want {
			t.Fatalf("contract checksum mismatch: %s", name)
		}
		files[name] = data
	}
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(paths) != len(files) {
		t.Fatal("every JSON contract file must have exactly one checksum")
	}
	return files
}
