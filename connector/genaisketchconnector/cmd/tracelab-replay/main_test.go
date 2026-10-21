//go:build tracelab_replay

// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
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

func TestPrivateOutputRejectsCheckoutAndSymlink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", "")
	checkout := filepath.Join(home, "checkout")
	if err := os.Mkdir(checkout, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "go.mod"), []byte("// Repository fixture.\n\nmodule \"github.com/llm-measurement/otelcol-genai-sketches\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, "escape")
	if err := os.Symlink(checkout, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{checkout, filepath.Join(checkout, "output"), filepath.Join(link, "new", "output")} {
		if _, err := privateOutput(path); err == nil {
			t.Fatalf("checkout output accepted: %s", path)
		}
	}
	if _, err := privateOutput(filepath.Join(home, "Work", "private-run")); err != nil {
		t.Fatal(err)
	}
}

func TestPrivateOutputCacheRoots(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", "")
	defaultCache := filepath.Join(home, ".cache")
	want, err := resolvePath(defaultCache)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := cacheDirectory(); err != nil || got != want {
		t.Fatalf("default cache: got %s, err %v", got, err)
	}
	xdg := filepath.Join(home, "custom-cache")
	if err := os.Mkdir(xdg, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(home, "cache-link")
	if err := os.Symlink(xdg, alias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CACHE_HOME", alias)
	want, err = resolvePath(xdg)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := cacheDirectory(); err != nil || got != want {
		t.Fatalf("XDG cache: got %s, err %v", got, err)
	}
	for _, path := range []string{defaultCache, xdg, alias} {
		if _, err := privateOutput(path); err == nil {
			t.Fatalf("cache root accepted: %s", path)
		}
	}
	if _, err := privateOutput(filepath.Join(alias, "new", "run")); err != nil {
		t.Fatal(err)
	}
}

func TestOutputDefaultsToUserCache(t *testing.T) {
	oldFlags, oldArgs := flag.CommandLine, os.Args
	t.Cleanup(func() { flag.CommandLine, os.Args = oldFlags, oldArgs })
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GENAI_SKETCH_SECRET", strings.Repeat("x", 32))
	for _, configured := range []string{"", filepath.Join(home, "custom-cache")} {
		t.Setenv("XDG_CACHE_HOME", configured)
		flag.CommandLine = flag.NewFlagSet("test", flag.ContinueOnError)
		os.Args = []string{"test", "--input", filepath.Join(home, "missing"), "--key-id", "test"}
		if err := run(); err == nil || err.Error() != "cannot open dataset" {
			t.Fatalf("default output rejected before input: %v", err)
		}
		cache, err := cacheDirectory()
		if err != nil {
			t.Fatal(err)
		}
		out := flag.CommandLine.Lookup("out").Value.String()
		if filepath.Dir(out) != cache || !strings.HasPrefix(filepath.Base(out), "tracelab-replay-") {
			t.Fatalf("default output %s is not a run under %s", out, cache)
		}
		if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("invalid input created output: %v", err)
		}
	}
}
