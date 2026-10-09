// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package tracelab

import (
	"strings"
	"testing"

	"go.opentelemetry.io/collector/pdata/ptrace"
)

// Invented fixture, not a TraceLab data excerpt.
const fixture = `{"provider":"claude","model":"test-model","user":"PRIVATE_USER","session_id":"PRIVATE_SESSION","trace_key":"step-1","input_tokens_total":306,"prefix_tokens":200,"newly_append_tokens":106,"claude_uncached_input_tokens":6,"claude_cache_creation_input_tokens":100,"claude_cache_read_input_tokens":200,"output_tokens":10,"timing_events":[{"event_type":"text","timestamp":"2026-06-01T01:00:00Z"},{"event_type":"tool_result","timestamp":"2026-06-02T12:00:00Z"}],"prompt":"PRIVATE_PROMPT","file":"/PRIVATE_PATH","tools":[{"command_skeleton":"PRIVATE_CODE"}]}`

func TestMapping(t *testing.T) {
	rows, err := Read(strings.NewReader(fixture))
	if err != nil {
		t.Fatal(err)
	}
	r := rows[0]
	if r.At.Format("2006-01-02") != "2026-06-01" {
		t.Fatal("tool completion shifted the model event")
	}
	traces := r.Traces()
	a := traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Attributes()
	for key, want := range map[string]int64{"gen_ai.usage.input_tokens": 306, "gen_ai.usage.cache_write.input_tokens": 100, "gen_ai.usage.cache_read.input_tokens": 200, "gen_ai.usage.output_tokens": 10} {
		v, ok := a.Get(key)
		if !ok || v.Int() != want {
			t.Fatalf("%s: %v", key, v)
		}
	}
	encoded, err := (&ptrace.JSONMarshaler{}).MarshalTraces(traces)
	if err != nil {
		t.Fatal(err)
	}
	for _, sentinel := range []string{"PRIVATE_PROMPT", "PRIVATE_PATH", "PRIVATE_CODE"} {
		if strings.Contains(string(encoded), sentinel) {
			t.Fatal("copied content into spans")
		}
	}
	// Identity is intentionally passed to the connector's hashing boundary.
	if !strings.Contains(string(encoded), "PRIVATE_SESSION") {
		t.Fatal("missing identity")
	}
}

func TestMissingAndInconsistent(t *testing.T) {
	missing := strings.Replace(fixture, `"output_tokens":10`, `"output_tokens":null`, 1)
	rows, err := Read(strings.NewReader(missing))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := rows[0].Traces().ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Attributes().Get("gen_ai.usage.output_tokens"); ok {
		t.Fatal("missing became zero")
	}
	for _, bad := range []string{fixture + "\n" + fixture, strings.Replace(fixture, `"input_tokens_total":306`, `"input_tokens_total":406`, 1), strings.Replace(fixture, `"event_type":"text"`, `"event_type":"tool_result"`, 1)} {
		if _, err := Read(strings.NewReader(bad)); err == nil {
			t.Fatal("accepted invalid input")
		}
	}
	// Invalid reasoning subsets are retained so the connector can count the defect.
	withReasoning := strings.Replace(fixture, `"output_tokens":10`, `"output_tokens":10,"reasoning_output_tokens":11`, 1)
	rows, err = Read(strings.NewReader(withReasoning))
	if err != nil || *rows[0].Reasoning != 11 {
		t.Fatal("source subset was repaired")
	}
}

func TestCodexAndStableOrder(t *testing.T) {
	codex := `{"provider":"codex","model":"test-model","user":"PRIVATE_USER","session_id":"PRIVATE_SESSION","trace_key":"step-2","input_tokens_total":50,"prefix_tokens":40,"newly_append_tokens":10,"output_tokens":2,"claude_cache_creation_input_tokens":null,"timing_events":[{"event_type":"usage_report","timestamp":"2026-06-01T00:00:00Z"}]}`
	a, err := Read(strings.NewReader(fixture + "\n" + codex))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Read(strings.NewReader(codex + "\n" + fixture))
	if err != nil {
		t.Fatal(err)
	}
	if a[0].Key != b[0].Key || a[0].Key != "step-2" {
		t.Fatal("unstable sorting")
	}
	if _, ok := a[0].Traces().ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Attributes().Get("gen_ai.usage.cache_write.input_tokens"); ok {
		t.Fatal("invented cache write")
	}
}
