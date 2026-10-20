// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package genaisketchconnector

import (
	"context"
	"fmt"
	"testing"
	"time"

	sketchhash "github.com/llm-measurement/llm-sketchkit/go/sketchkit/hash"
)

// BenchmarkAccountingPerformance uses identical inputs for the default path,
// export, and model_usage. One operation is a batch of 100 spans.
func BenchmarkAccountingPerformance(b *testing.B) {
	for _, profile := range []string{"small", "default"} {
		for _, mode := range []string{"default", "export", "model_usage", "dedup_full"} {
			b.Run(profile+"/"+mode, func(b *testing.B) {
				cfg := defaultConfig()
				cfg.Profiles.HLLPP, cfg.Profiles.FrequentItems = profile, profile
				if mode == "export" || mode == "model_usage" {
					cfg.SummaryExport.Directory = "unused"
				}
				if mode == "model_usage" {
					cfg.SummaryExport.ModelUsage = &ModelUsageConfig{Slice: "by_model", Models: map[string]string{"model": "public-model"}}
				}
				cfg.Dedup.Enabled = mode == "dedup_full"
				b.Setenv("GENAI_SKETCH_SECRET", "test-secret-32-bytes-for-unit-tests")
				secret, err := sketchhash.SecretFromEnv("GENAI_SKETCH_SECRET")
				if err != nil {
					b.Fatal(err)
				}
				clk := &fixedClock{now: time.Unix(120, 0)}
				s, err := newCollectorState(cfg, secret, clk)
				if err != nil {
					b.Fatal(err)
				}
				spans := make([]testSpan, 100)
				for i := range spans {
					spans[i] = testSpan{Model: "model", Team: "team", User: fmt.Sprintf("user-%d", i),
						Prompt: fmt.Sprintf("prompt-%d", i), InputTokens: ptr(10), OutputTokens: ptr(10)}
				}
				traces := tracesFromSpans(spans...)
				if _, _, err := s.ConsumeTraces(context.Background(), traces); err != nil {
					b.Fatal(err)
				}
				// Dense HLLs and occupied FI maps keep warm-up identical and
				// outside the timed region, without thousands of preview calls.
				for _, slice := range s.slices {
					for _, w := range slice.windows {
						w.distinctUsers.ForceDense()
						w.distinctPrompts.ForceDense()
						w.distinctDocs.ForceDense()
						for _, sketch := range w.topKeys {
							for i := 0; i < sketch.MapSize(); i++ {
								if err := sketch.AddHash(uint64(i), 1); err != nil {
									b.Fatal(err)
								}
							}
						}
						if w.dedupRequests != nil {
							for i := uint64(0); i < w.dedupRequests.RatedInsertions(); i++ {
								if err := w.dedupRequests.AddHash(i); err != nil {
									b.Fatal(err)
								}
							}
						}
					}
				}
				if s.summary != nil {
					for _, w := range s.summary.windows {
						w.distinctUsers.ForceDense()
						w.distinctPrompts.ForceDense()
						w.distinctDocs.ForceDense()
						for _, sketch := range w.topKeys {
							for i := 0; i < sketch.MapSize(); i++ {
								if err := sketch.AddHash(uint64(i), 1); err != nil {
									b.Fatal(err)
								}
							}
						}
					}
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if mode == "dedup_full" {
						for j := range spans {
							traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(j).Attributes().
								PutStr("request.id", fmt.Sprintf("request-%d-%d", i, j))
						}
					}
					if _, _, err := s.ConsumeTraces(context.Background(), traces); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
