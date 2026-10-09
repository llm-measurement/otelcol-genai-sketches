//go:build tracelab_replay

// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

// Package normalization translates source token units without performing accounting.
package normalization

import (
	"errors"
	"math"
)

// ClaudeInput converts native Claude counts to GenAI's inclusive input total.
// A future Claude Code span recipe can use the same function. Call only when all
// three source fields are present; an absent source value is not a measured zero.
func ClaudeInput(uncached, cacheWrite, cacheRead int64) (int64, error) {
	if uncached < 0 || cacheWrite < 0 || cacheRead < 0 ||
		uncached > math.MaxInt64-cacheWrite || uncached+cacheWrite > math.MaxInt64-cacheRead {
		return 0, errors.New("invalid Claude input counts")
	}
	return uncached + cacheWrite + cacheRead, nil
}
