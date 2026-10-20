// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package genaisketchconnector

import (
	"fmt"
	"testing"

	"go.opentelemetry.io/collector/pdata/pcommon"
)

// Kept compatible with the pre-fix implementation so the same regression
// demonstrates its unbounded inserts and verifies the repaired boundary.
func TestDedupFloodInsertionLimit(t *testing.T) {
	cfg := defaultConfig()
	cfg.Dedup.Enabled = true
	s := newTestState(t, cfg)
	w, err := (&sliceState{}).newWindow(s)
	if err != nil {
		t.Fatal(err)
	}
	data := spanData{spanAttrs: pcommon.NewMap(), resourceAttrs: pcommon.NewMap()}
	for i := 0; i < 50000; i++ {
		data.spanAttrs.PutStr("request.id", fmt.Sprintf("flood-%d", i))
		hash, duplicate, _, err := s.prepareDedup(w, data)
		if err != nil {
			t.Fatal(err)
		}
		if hash.ok && !duplicate {
			if err := w.dedupRequests.AddHash(hash.value); err != nil {
				t.Fatal(err)
			}
		}
	}
	if count, capacity := w.dedupRequests.InsertedCount(), w.dedupRequests.RatedInsertions(); count != capacity {
		t.Errorf("flood insertion count=%d, design limit=%d", count, capacity)
	}
	suppressed := 0
	for i := 0; i < 1000; i++ {
		data.spanAttrs.PutStr("request.id", fmt.Sprintf("unseen-%d", i))
		_, duplicate, _, err := s.prepareDedup(w, data)
		if err != nil {
			t.Fatal(err)
		}
		if duplicate {
			suppressed++
		}
	}
	if suppressed != 0 {
		t.Errorf("after flood, suppressed %d/1000 unseen requests", suppressed)
	}
}
