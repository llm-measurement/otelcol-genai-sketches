//go:build tracelab_replay

// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

// Package replay defines the private entry point used by offline tools.
package replay

import (
	"context"
	"time"

	"go.opentelemetry.io/collector/pdata/ptrace"
)

// Runner is implemented by the connector's private implementation, not its
// public factory interface, and is available only with the replay build tag.
type Runner interface {
	Replay(ctx context.Context, start, end time.Time, epoch string,
		next func() (time.Time, ptrace.Traces, error), save func(time.Time, []byte) error) error
}
