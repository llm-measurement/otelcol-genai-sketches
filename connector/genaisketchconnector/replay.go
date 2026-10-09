//go:build tracelab_replay

// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package genaisketchconnector

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap"
)

// Keep this entry point free of tool-package imports: the production builder's
// go mod tidy resolves tagged imports even though the tool is not compiled.
func (c *tracesConnector) Replay(ctx context.Context, start, end time.Time, epoch string,
	next func() (time.Time, ptrace.Traces, error), save func(time.Time, []byte) error) error {
	return replay(ctx, c.cfg, start, end, epoch, next, save)
}

// replay closes windows on an explicit event clock. next must supply batches in
// nondecreasing time order within [start,end); all spans in a batch share its time.
// save receives the unchanged bytes of each closed live-exporter file. cfg must
// enable summary export to a private scratch directory, separate from the archive.
// epoch is a 16-byte hex replay identity, stable only for the same input/config/key.
// Full observations describe traversal of the supplied file, not source capture completeness.
func replay(ctx context.Context, cfg *Config, start, end time.Time, epoch string,
	next func() (time.Time, ptrace.Traces, error), save func(time.Time, []byte) error) error {
	if cfg == nil || next == nil || save == nil {
		return errors.New("replay requires config, input and output")
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	decoded, err := hex.DecodeString(epoch)
	if err != nil || len(decoded) != 16 || hex.EncodeToString(decoded) != epoch {
		return errors.New("replay epoch must be 32 lowercase hex characters")
	}
	w := cfg.WindowDuration
	if cfg.SummaryExport.Directory == "" || start.UnixNano() < 0 || !end.After(start) ||
		start.UnixNano()%w.Nanoseconds() != 0 || end.UnixNano()%w.Nanoseconds() != 0 || end.Sub(start)/w > 4096 {
		return errors.New("replay requires export and 1-4096 aligned windows after the Unix epoch")
	}
	clk := &fixedClock{now: start}
	sink, err := consumer.NewMetrics(func(context.Context, pmetric.Metrics) error { return nil })
	if err != nil {
		return err
	}
	c := newTracesConnector(component.TelemetrySettings{Logger: zap.NewNop()}, cfg, sink)
	c.clock = clk
	if err := c.start(false); err != nil {
		return err
	}
	// The live path generates a random process epoch. A replay uses its input identity.
	c.exporter.epoch = epoch
	defer c.exporter.root.Close()
	boundary := start.Add(w)
	closeWindow := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		clk.Set(boundary)
		if err := c.emitSummary(true); err != nil {
			return err
		}
		closed := boundary.Add(-w)
		name := fmt.Sprintf("%020d-%s.json", closed.UnixNano(), epoch)
		data, err := c.exporter.root.ReadFile(name)
		if err != nil {
			return err
		}
		if err := save(closed, data); err != nil {
			return err
		}
		boundary = boundary.Add(w)
		return nil
	}
	previous := start
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		at, spans, err := next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if at.Before(previous) || !at.Before(end) {
			return errors.New("replay event outside range or out of order")
		}
		for !at.Before(boundary) {
			if err := closeWindow(); err != nil {
				return err
			}
		}
		clk.Set(at)
		if err := c.ConsumeTraces(ctx, spans); err != nil {
			return err
		}
		previous = at
	}
	for !boundary.After(end) {
		if err := closeWindow(); err != nil {
			return err
		}
	}
	return nil
}
