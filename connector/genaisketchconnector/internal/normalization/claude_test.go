// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package normalization

import (
	"math"
	"testing"
)

func TestClaudeInput(t *testing.T) {
	for _, tc := range []struct {
		uncached, write, read, want int64
		fail                        bool
	}{
		{6, 100, 200, 306, false}, {0, 0, 0, 0, false}, {-1, 0, 0, 0, true},
		{math.MaxInt64, 1, 0, 0, true}, {1, 1, math.MaxInt64, 0, true},
	} {
		got, err := ClaudeInput(tc.uncached, tc.write, tc.read)
		if (err != nil) != tc.fail || got != tc.want {
			t.Fatalf("normalization=%d, %v", got, err)
		}
	}
}
