//go:build tracelab_replay

// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package genaisketchconnector

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/frequentitems"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
	"github.com/llm-measurement/otelcol-genai-sketches/connector/genaisketchconnector/internal/tracelab"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap"
)

func TestReplayMatchesLiveConnectorBytes(t *testing.T) {
	ctx := context.Background()
	t.Setenv("GENAI_SKETCH_SECRET", "synthetic-test-key-0123456789-abcdef")
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	epoch := "0123456789abcdef0123456789abcdef"
	var input strings.Builder
	// Same-window ties, exact boundary, idle windows, inclusive Claude cache writes,
	// missing usage, and a flagged reasoning subset all pass through both paths.
	for i, seconds := range []int{0, 10, 10, 60, 240} {
		output := "10"
		if i == 2 {
			output = "null"
		}
		fmt.Fprintf(&input, `{"provider":"claude","model":"test","user":"PRIVATE_USER","session_id":"PRIVATE_SESSION","trace_key":"step-%d","input_tokens_total":306,"prefix_tokens":200,"newly_append_tokens":106,"claude_uncached_input_tokens":6,"claude_cache_creation_input_tokens":100,"claude_cache_read_input_tokens":200,"output_tokens":%s,"reasoning_output_tokens":11,"timing_events":[{"event_type":"text","timestamp":%q}]}`+"\n", i, output, start.Add(time.Duration(seconds)*time.Second).Format(time.RFC3339))
	}
	// Exercise pruning too, rather than parity only while frequent-items is exact.
	for i := 0; i < 800; i++ {
		fmt.Fprintf(&input, `{"provider":"codex","model":"test","user":"PRIVATE_USER_%d","session_id":"PRIVATE_SESSION_%d","trace_key":"overflow-%d","input_tokens_total":2,"output_tokens":1,"prefix_tokens":0,"newly_append_tokens":2,"timing_events":[{"event_type":"usage_report","timestamp":%q}]}`+"\n", i, i, i, start.Add(30*time.Second).Format(time.RFC3339))
	}
	rows, err := tracelab.Read(strings.NewReader(input.String()))
	if err != nil {
		t.Fatal(err)
	}
	config := func() *Config {
		cfg := defaultConfig()
		cfg.RetentionWindows = 2
		cfg.MaxSlices = 1
		cfg.TopKKeys = []TopKKeyConfig{{Field: "user_key"}, {Field: "session_key"}}
		cfg.SummaryExport = SummaryExportConfig{Directory: t.TempDir(), ProducerID: "tracelab", ScopeID: "tracelab", KeyID: "test", Interval: time.Second}
		if err := os.Chmod(cfg.SummaryExport.Directory, 0700); err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	got := map[int64][]byte{}
	i := 0
	err = replay(ctx, config(), start, start.Add(5*time.Minute), epoch, func() (time.Time, ptrace.Traces, error) {
		if i == len(rows) {
			return time.Time{}, ptrace.Traces{}, io.EOF
		}
		r := rows[i]
		i++
		return r.At, r.Traces(), nil
	}, func(at time.Time, data []byte) error { got[at.UnixNano()] = data; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatal("lost idle windows")
	}
	cfg := config()
	clk := &fixedClock{now: start}
	sink, err := consumer.NewMetrics(func(context.Context, pmetric.Metrics) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	live := newTracesConnector(component.TelemetrySettings{Logger: zap.NewNop()}, cfg, sink)
	live.clock = clk
	if err := live.Start(ctx, nil); err != nil {
		t.Fatal(err)
	}
	// Stop only the wall-clock ticker; drive the actual live connector and its
	// export method at the identical schedule, with identical process metadata.
	live.debugCancel()
	<-live.debugDone
	live.exporter.epoch = epoch
	defer live.Shutdown(ctx)
	i = 0
	for n := 1; n <= 5; n++ {
		boundary := start.Add(time.Duration(n) * time.Minute)
		for i < len(rows) && rows[i].At.Before(boundary) {
			clk.Set(rows[i].At)
			if err := live.ConsumeTraces(ctx, rows[i].Traces()); err != nil {
				t.Fatal(err)
			}
			i++
		}
		clk.Set(boundary)
		if err := live.emitSummary(true); err != nil {
			t.Fatal(err)
		}
		at := boundary.Add(-time.Minute)
		want, err := os.ReadFile(filepath.Join(cfg.SummaryExport.Directory, fmt.Sprintf("%020d-%s.json", at.UnixNano(), epoch)))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got[at.UnixNano()], want) {
			t.Fatalf("not byte identical at %s", at)
		}
		doc, err := summary.Parse(want)
		if err != nil {
			t.Fatal(err)
		}
		if n == 1 {
			sketch, err := frequentitems.Parse(doc.Sketches["top_sessions"].Data)
			if err != nil || sketch.MaxError() == 0 {
				t.Fatal("parity fixture did not exercise pruning")
			}
		}
		for _, p := range doc.Sketches {
			if bytes.Contains(p.Data, []byte("PRIVATE_")) {
				t.Fatal("identity leaked")
			}
		}
		if bytes.Contains(want, []byte("PRIVATE_")) {
			t.Fatal("identity leaked")
		}
	}
}

func TestReplayRejectsOutOfOrder(t *testing.T) {
	t.Setenv("GENAI_SKETCH_SECRET", "synthetic-test-key-0123456789-abcdef")
	cfg := defaultConfig()
	cfg.SummaryExport = SummaryExportConfig{Directory: t.TempDir(), ProducerID: "a", ScopeID: "b", KeyID: "c", Interval: time.Second}
	if err := os.Chmod(cfg.SummaryExport.Directory, 0700); err != nil {
		t.Fatal(err)
	}
	start := time.Unix(120, 0)
	err := replay(context.Background(), cfg, start, start.Add(time.Minute), strings.Repeat("0", 32), func() (time.Time, ptrace.Traces, error) { return start.Add(-time.Second), ptrace.NewTraces(), nil }, func(time.Time, []byte) error { return nil })
	if err == nil {
		t.Fatal("accepted event before range")
	}
}
