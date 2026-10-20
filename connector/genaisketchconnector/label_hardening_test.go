// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package genaisketchconnector

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/ptrace"
)

func TestInvalidSliceUTF8RejectsOnlyItsSpan(t *testing.T) {
	for _, resource := range []bool{false, true} {
		t.Run(map[bool]string{false: "span", true: "resource"}[resource], func(t *testing.T) {
			cfg := defaultConfig()
			cfg.Slices = []SliceConfig{{Name: "team", Keys: []string{"team.id"}}}
			s := newTestStateWithClock(t, &fixedClock{now: time.Unix(120, 0)}, cfg)
			traces := ptrace.NewTraces()
			for _, value := range []string{"valid-team", "invalid\xffteam"} {
				rs := traces.ResourceSpans().AppendEmpty()
				span := rs.ScopeSpans().AppendEmpty().Spans().AppendEmpty()
				span.Attributes().PutStr("gen_ai.operation.name", "chat")
				if resource {
					rs.Resource().Attributes().PutStr("team.id", value)
				} else {
					span.Attributes().PutStr("team.id", value)
				}
			}
			metrics, relevant, err := s.ConsumeTraces(context.Background(), traces)
			if err != nil || !relevant {
				t.Fatal("malformed label rejected the whole batch", err)
			}
			if got := intMetricValue(t, metrics, requestsMetricName, map[string]string{"slice": "team", "slice_value": "team.id=valid-team", "overflow": "false"}); got != 1 {
				t.Fatalf("healthy request count = %d", got)
			}
			if got := intMetricValue(t, metrics, rejectedSpansMetricName, map[string]string{"reason": "invalid_utf8"}); got != 1 {
				t.Fatalf("rejected count = %d", got)
			}
			if len(s.slices) != 1 {
				t.Fatal("malformed label entered slice state")
			}
		})
	}
}
