// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package main

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	connector "github.com/llm-measurement/otelcol-genai-sketches/connector/genaisketchconnector"
	"github.com/llm-measurement/otelcol-genai-sketches/connector/genaisketchconnector/internal/replay"
	"github.com/llm-measurement/otelcol-genai-sketches/connector/genaisketchconnector/internal/tracelab"
	"go.opentelemetry.io/collector/component"
	otelconnector "go.opentelemetry.io/collector/connector"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	input := flag.String("input", "", "verified v0.0.2 JSONL.gz")
	out := flag.String("out", "", "new private output directory")
	startText := flag.String("start", "2025-09-23", "first UTC day, inclusive")
	endText := flag.String("end", "2026-07-25", "last UTC boundary, exclusive")
	flag.Parse()
	start, e1 := time.Parse("2006-01-02", *startText)
	end, e2 := time.Parse("2006-01-02", *endText)
	if *input == "" || *out == "" || e1 != nil || e2 != nil || !end.After(start) || flag.NArg() != 0 {
		return errors.New("provide --input, --out and an increasing UTC date range")
	}
	secret := os.Getenv("GENAI_SKETCH_SECRET")
	if len(secret) < 32 {
		return errors.New("set GENAI_SKETCH_SECRET to a private random key of at least 32 bytes")
	}
	f, err := os.Open(*input)
	if err != nil {
		return errors.New("cannot open dataset")
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return errors.New("cannot hash dataset")
	}
	if hex.EncodeToString(h.Sum(nil)) != tracelab.SHA256 {
		return errors.New("TraceLab v0.0.2 checksum mismatch")
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		return errors.New("cannot open compressed dataset")
	}
	rows, readErr := tracelab.Read(io.LimitReader(gz, 4<<30))
	if err = errors.Join(readErr, gz.Close()); err != nil {
		return err
	}
	if err = os.Mkdir(*out, 0700); err != nil {
		return errors.New("output must be a new directory under an existing parent")
	}
	root, err := os.OpenRoot(*out)
	if err != nil {
		return err
	}
	defer root.Close()
	scratch, err := os.MkdirTemp(filepath.Dir(*out), ".tracelab-replay-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	cfg := connector.NewFactory().CreateDefaultConfig().(*connector.Config)
	cfg.WindowDuration = 24 * time.Hour
	cfg.RetentionWindows = 2
	cfg.MaxSlices = 1
	cfg.Slices = []connector.SliceConfig{{Name: "app", Keys: []string{"service.name"}, FromResourceAttributes: []string{"service.name"}}}
	cfg.TopK = 100
	cfg.TopKKeys = []connector.TopKKeyConfig{{Field: "user_key", Weight: "tokens"}, {Field: "session_key", Weight: "tokens"}}
	keyID := sha256.Sum256([]byte(secret))
	cfg.SummaryExport = connector.SummaryExportConfig{Directory: scratch, ProducerID: "tracelab", ScopeID: "tracelab-v0.0.2", KeyID: hex.EncodeToString(keyID[:16]), Interval: time.Second}
	epoch := sha256.Sum256([]byte(tracelab.SHA256 + tracelab.MappingVersion + *startText + *endText + cfg.SummaryExport.KeyID))
	i, consumed, windows := 0, 0, 0
	for i < len(rows) && rows[i].At.Before(start) {
		i++
	}
	ctx := context.Background()
	runner, err := newReplayRunner(cfg)
	if err != nil {
		return err
	}
	err = runner.Replay(ctx, replay.Options{
		Start: start, End: end, Epoch: hex.EncodeToString(epoch[:16]),
		Next: func() (time.Time, ptrace.Traces, error) {
			if i == len(rows) || !rows[i].At.Before(end) {
				return time.Time{}, ptrace.Traces{}, io.EOF
			}
			r := rows[i]
			i++
			consumed++
			return r.At, r.Traces(), nil
		}, Save: func(at time.Time, data []byte) error {
			f, err := root.OpenFile(at.Format("2006-01-02")+".json", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				return err
			}
			_, err = f.Write(data)
			err = errors.Join(err, f.Close())
			if err == nil {
				windows++
			}
			return err
		},
	})
	if err != nil {
		return err
	}
	fmt.Printf("Replayed %d steps into %d daily windows.\n", consumed, windows)
	return nil
}

func newReplayRunner(cfg *connector.Config) (replay.Runner, error) {
	sink, err := consumer.NewMetrics(func(context.Context, pmetric.Metrics) error { return nil })
	if err != nil {
		return nil, err
	}
	factory := connector.NewFactory()
	instance, err := factory.CreateTracesToMetrics(context.Background(), otelconnector.Settings{
		ID:                component.NewID(factory.Type()),
		TelemetrySettings: component.TelemetrySettings{Logger: zap.NewNop()},
	}, cfg, sink)
	if err != nil {
		return nil, err
	}
	runner, ok := instance.(replay.Runner)
	if !ok {
		return nil, errors.New("connector does not support internal replay")
	}
	return runner, nil
}
