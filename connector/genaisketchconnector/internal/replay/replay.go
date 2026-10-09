// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

// Package replay defines the private entry point used by offline tools.
package replay

import (
	"context"
	"time"

	"go.opentelemetry.io/collector/pdata/ptrace"
)

type Options struct {
	Start, End time.Time
	Epoch      string
	Next       func() (time.Time, ptrace.Traces, error)
	Save       func(time.Time, []byte) error
}

// Runner is implemented by the connector's private implementation, not its
// public factory interface. Options cannot be imported outside this module.
type Runner interface {
	Replay(context.Context, Options) error
}
