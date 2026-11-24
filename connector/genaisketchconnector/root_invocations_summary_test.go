// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package genaisketchconnector

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func rootSummaryFixture(t *testing.T, enabled bool, operations []string) (*collectorState, *summaryExporter, *Config) {
	t.Helper()
	cfg := defaultConfig()
	cfg.RootInvocations.Enabled = enabled
	if operations != nil {
		cfg.RootInvocations.Operations = operations
	}
	cfg.SummaryExport = SummaryExportConfig{Directory: t.TempDir(), ProducerID: "roots", ScopeID: "scope", KeyID: "synthetic-key", Interval: time.Second}
	if err := os.Chmod(cfg.SummaryExport.Directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(120, 0)
	state := newTestStateWithClock(t, &fixedClock{now: now}, cfg)
	exporter, err := newSummaryExporter(cfg, now)
	if err != nil {
		t.Fatal(err)
	}
	exporter.epoch = "0123456789abcdef0123456789abcdef"
	t.Cleanup(func() { _ = exporter.root.Close() })
	return state, exporter, cfg
}

func rootSummaryDocument(t *testing.T, enabled bool, operations []string) summary.Envelope {
	t.Helper()
	state, exporter, _ := rootSummaryFixture(t, enabled, operations)
	traces := ptrace.NewTraces()
	span := traces.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	span.SetKind(ptrace.SpanKindInternal)
	span.Attributes().PutStr("gen_ai.operation.name", "invoke_workflow")
	span.Attributes().PutStr("gen_ai.agent.name", "PRIVATE_AGENT_NAME_SENTINEL")
	span.Attributes().PutStr("gen_ai.conversation.id", "PRIVATE_SESSION_SENTINEL")
	if _, _, err := state.ConsumeTraces(context.Background(), traces); err != nil {
		t.Fatal(err)
	}
	documents, err := state.summaryDocuments(exporter, time.Unix(121, 0))
	if err != nil || len(documents) != 1 {
		t.Fatalf("summary documents: %d, %v", len(documents), err)
	}
	return documents[0]
}

func TestRootInvocationSummaryDefinitionAndRoundTrip(t *testing.T) {
	doc := rootSummaryDocument(t, true, nil)
	if doc.Counters[rootInvocationSummaryCount] != 1 || doc.Counters["agent_runs"] != 0 {
		t.Fatal("root workflow count changed legacy agent-run accounting")
	}
	for _, key := range []string{rootInvocationSummaryRule, rootInvocationOperation + "invoke_agent", rootInvocationOperation + "invoke_workflow"} {
		value, present := doc.Counters[key]
		if !present || value != 0 {
			t.Fatalf("missing zero-valued definition key %s", key)
		}
	}
	encoded, err := doc.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := summary.Parse(encoded)
	if err != nil || !reflect.DeepEqual(decoded.Counters, doc.Counters) {
		t.Fatalf("root measurement did not round-trip: %v", err)
	}
	for _, sentinel := range []string{"PRIVATE_AGENT_NAME_SENTINEL", "PRIVATE_SESSION_SENTINEL"} {
		if bytes.Contains(encoded, []byte(sentinel)) {
			t.Fatal("raw invocation identity reached summary")
		}
	}
	combined, err := summary.Combine([]summary.Envelope{doc, doc}, []string{"roots"})
	if err != nil || combined.Counters[rootInvocationSummaryCount] != 1 {
		t.Fatalf("summary replay doubled the root count: %v", err)
	}
}

func TestRootInvocationSummaryCompatibility(t *testing.T) {
	base := rootSummaryDocument(t, false, nil)
	first := rootSummaryDocument(t, true, []string{"invoke_agent", "invoke_workflow"})
	reordered := rootSummaryDocument(t, true, []string{"invoke_workflow", "invoke_agent"})
	other := rootSummaryDocument(t, true, []string{"invoke_agent", "invoke_runner"})
	if base.AccountingID != first.AccountingID || first.AccountingID != other.AccountingID {
		t.Fatal("optional root definition changed the base accounting fingerprint")
	}
	if err := summary.Compatible(first, reordered); err != nil {
		t.Fatalf("equivalent sets are not compatible: %v", err)
	}
	for _, mismatch := range []summary.Envelope{base, other} {
		if err := summary.Compatible(first, mismatch); err == nil {
			t.Fatal("different root definition silently compares")
		}
		if _, err := summary.Combine([]summary.Envelope{first, mismatch}, []string{"roots"}); err == nil {
			t.Fatal("different root definition silently merges")
		}
	}
	for key := range base.Counters {
		if strings.HasPrefix(key, "root_invocations.") {
			t.Fatal("disabled root group changed existing summary output")
		}
	}
}

func TestRootInvocationSummaryDefinitionIsOwned(t *testing.T) {
	state, exporter, cfg := rootSummaryFixture(t, true, nil)
	cfg.RootInvocations.Operations[0] = "invoke_mutated"
	documents, err := state.summaryDocuments(exporter, time.Unix(121, 0))
	if err != nil {
		t.Fatal(err)
	}
	counters := documents[0].Counters
	if _, ok := counters[rootInvocationOperation+"invoke_agent"]; !ok {
		t.Fatal("caller mutation changed installed root definition")
	}
	if _, ok := counters[rootInvocationOperation+"invoke_mutated"]; ok {
		t.Fatal("caller-owned operation reached summary")
	}
}

func TestRootInvocationSummaryUsesResolvedDefaults(t *testing.T) {
	cfg := defaultConfig()
	cfg.RootInvocations = RootInvocationsConfig{Enabled: true}
	operations, err := rootInvocationSummaryDefinition(cfg)
	if err != nil || !reflect.DeepEqual(operations, []string{"invoke_agent", "invoke_workflow"}) {
		t.Fatalf("summary differs from the runtime's default definition: %v, %v", operations, err)
	}
}

func TestRootInvocationSummaryMaximumDefinitionFitsEnvelope(t *testing.T) {
	operations := make([]string, 32)
	for i := range operations {
		operations[i] = fmt.Sprintf("invoke_%02d_", i) + strings.Repeat("x", 54)
	}
	doc := rootSummaryDocument(t, true, operations)
	if _, err := doc.MarshalBinary(); err != nil {
		t.Fatalf("maximum configured definition exceeds envelope limits: %v", err)
	}
	if len(doc.Counters) > 128 {
		t.Fatal("root definition exceeds the shared counter ceiling")
	}
}

func TestRootInvocationSummaryIncludedInModelUsageByteBudget(t *testing.T) {
	disabled, _, _ := rootSummaryFixture(t, false, nil)
	enabled, _, _ := rootSummaryFixture(t, true, nil)
	base, err := disabled.modelUsageFixedBytes()
	if err != nil {
		t.Fatal(err)
	}
	withRoots, err := enabled.modelUsageFixedBytes()
	if err != nil {
		t.Fatal(err)
	}
	if withRoots <= base {
		t.Fatal("optional root definition was omitted from the fixed summary byte budget")
	}
}
