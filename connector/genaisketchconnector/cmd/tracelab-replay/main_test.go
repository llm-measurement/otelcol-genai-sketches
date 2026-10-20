//go:build tracelab_replay

// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	connector "github.com/llm-measurement/otelcol-genai-sketches/connector/genaisketchconnector"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func TestFactoryReplay(t *testing.T) {
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GENAI_SKETCH_SECRET", hex.EncodeToString(secret[:]))
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
	err = runner.Replay(context.Background(), start, start.Add(cfg.WindowDuration), strings.Repeat("0", 32),
		func() (time.Time, ptrace.Traces, error) { return time.Time{}, ptrace.Traces{}, io.EOF },
		func(at time.Time, data []byte) error {
			if !at.Equal(start) || len(data) == 0 {
				t.Fatal("missing idle window")
			}
			saved++
			return nil
		})
	if err != nil || saved != 1 {
		t.Fatalf("replay: saved=%d err=%v", saved, err)
	}
}

func TestOpaqueKeyIDRequiredBeforeInput(t *testing.T) {
	oldFlags, oldArgs := flag.CommandLine, os.Args
	t.Cleanup(func() { flag.CommandLine, os.Args = oldFlags, oldArgs })
	for _, id := range []string{"", "not opaque"} {
		flag.CommandLine = flag.NewFlagSet("test", flag.ContinueOnError)
		os.Args = []string{"test", "--input", "missing", "--out", filepath.Join(t.TempDir(), "windows"), "--key-id", id}
		if err := run(); err == nil || !strings.Contains(err.Error(), "opaque --key-id") {
			t.Fatalf("missing/invalid key ID was not refused: %v", err)
		}
	}
}

func TestPrivateOutputRejectsWorkAndSymlink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := filepath.Join(home, "Work")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, "escape")
	if err := os.Symlink(work, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(work, "output"), filepath.Join(link, "output")} {
		if _, err := privateOutput(path); err == nil {
			t.Fatal("Work output accepted")
		}
	}
	if _, err := privateOutput(filepath.Join(home, "private-run")); err != nil {
		t.Fatal(err)
	}
}
