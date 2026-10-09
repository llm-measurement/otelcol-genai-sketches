// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package main

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	connector "github.com/llm-measurement/otelcol-genai-sketches/connector/genaisketchconnector"
	"github.com/llm-measurement/otelcol-genai-sketches/connector/genaisketchconnector/internal/replay"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func TestFactoryReplay(t *testing.T) {
	t.Setenv("GENAI_SKETCH_SECRET", "synthetic-test-key-0123456789-abcdef")
	cfg := connector.NewFactory().CreateDefaultConfig().(*connector.Config)
	cfg.SummaryExport = connector.SummaryExportConfig{
		Directory: t.TempDir(), ProducerID: "tracelab", ScopeID: "test", KeyID: "test", Interval: time.Second,
	}
	if err := os.Chmod(cfg.SummaryExport.Directory, 0700); err != nil {
		t.Fatal(err)
	}
	runner, err := newReplayRunner(cfg)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	saved := 0
	err = runner.Replay(context.Background(), replay.Options{
		Start: start, End: start.Add(cfg.WindowDuration), Epoch: strings.Repeat("0", 32),
		Next: func() (time.Time, ptrace.Traces, error) { return time.Time{}, ptrace.Traces{}, io.EOF },
		Save: func(at time.Time, data []byte) error {
			if !at.Equal(start) || len(data) == 0 {
				t.Fatal("missing idle window")
			}
			saved++
			return nil
		},
	})
	if err != nil || saved != 1 {
		t.Fatalf("replay: saved=%d err=%v", saved, err)
	}
}
