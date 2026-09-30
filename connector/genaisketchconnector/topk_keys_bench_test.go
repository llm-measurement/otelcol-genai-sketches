//go:build load

// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package genaisketchconnector

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func benchmarkTopKConfig(count int) *Config {
	cfg := defaultConfig()
	cfg.Slices = []SliceConfig{{Name: "by_model", Keys: []string{"gen_ai.request.model"}}}
	keys := []TopKKeyConfig{{Field: fieldPromptKey}, {Field: fieldUserKey}, {Field: fieldSessionKey}, {Field: fieldDocKey}}
	if count == 0 {
		cfg.TopK = 0
	} else {
		cfg.TopKKeys = keys[:count]
	}
	return cfg
}

// Allocation per new slice/window, including the three unchanged HLL sketches.
// Subtract adjacent key counts to estimate each additional FI allocation, not RSS.
func BenchmarkTopKKeysWindow(b *testing.B) {
	for count := 0; count <= 4; count++ {
		b.Run(fmt.Sprintf("keys-%d", count), func(b *testing.B) {
			s := newLoadState(b, &fixedClock{now: time.Unix(120, 0)}, benchmarkTopKConfig(count), loadSecret(b))
			slice := &sliceState{windows: make(map[int64]*windowState)}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				delete(slice.windows, 120)
				if _, err := slice.window(120, s); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// Same 1,000 synthetic attempts, 10% missing usage, 100 identities, one slice.
// Includes hashing, counter updates, metric construction and top-k updates.
func BenchmarkTopKKeysConsume(b *testing.B) {
	for count := 1; count <= 4; count++ {
		b.Run(fmt.Sprintf("keys-%d", count), func(b *testing.B) {
			s := newLoadState(b, &fixedClock{now: time.Unix(120, 0)}, benchmarkTopKConfig(count), loadSecret(b))
			spans := loadSpans(1000, 1)
			for i := range spans {
				spans[i].User, spans[i].Prompt, spans[i].Doc = fmt.Sprint(i%100), fmt.Sprint(i%100), fmt.Sprint(i%100)
			}
			traces := tracesFromSpans(spans...)
			for i := 0; i < 1000; i++ {
				traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(i).Attributes().PutStr("session.id", fmt.Sprint(i%100))
			}
			if _, _, err := s.ConsumeTraces(context.Background(), traces); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, _, err := s.ConsumeTraces(context.Background(), traces); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
