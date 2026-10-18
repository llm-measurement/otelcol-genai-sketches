//go:build integration

// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package integration

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/modelusage"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
	tracecollectorpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

const litellmMessagesIdentity = "litellm-1.104.0-messages-v1"

func TestLiteLLMMessagesModelUsageRealOTLP(t *testing.T) {
	binary := filepath.Join(repoRoot(t), "dist", "otelcol-genai-sketches")
	requireExecutable(t, binary)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	model := litellmProfileConfig(litellmResponsesIdentity)
	model["identity"] = litellmMessagesIdentity
	model["models"].(map[string]string)["anthropic/PRIVATE_AUTO"] = "model-a"
	model["models"].(map[string]string)["anthropic/PRIVATE_RECOVERY"] = "model-b"
	profile := startModelUsageProfile(t, ctx, binary, model)
	baseline := startModelUsageProfile(t, ctx, binary, nil)
	if profile.idle.ModelUsage == nil || profile.idle.AccountingID != baseline.idle.AccountingID {
		t.Fatal("Messages extension missing or base accounting contract changed")
	}
	rows := litellmMessagesCases()
	all := traceRequest(make([]otlpSpanSpec, len(rows))...)
	spans := all.ResourceSpans[0].ScopeSpans[0].Spans
	// Valid resource values must not repair missing span-local identity or markers.
	all.ResourceSpans[0].Resource.Attributes = append(all.ResourceSpans[0].Resource.Attributes,
		stringKV("litellm.provider.model", "PRIVATE_SELECTED_A"),
		stringKV("llm.request.type", "anthropic_messages"), stringKV("http.route", "/v1/messages"),
		profileBoolKV("gen_ai.request.stream", true))
	type totals struct{ requests, complete, missing uint64 }
	want := map[string]totals{}
	var first, latest summary.Envelope
	var native []*tracepb.Span
	for i, tc := range rows {
		if !t.Run(tc.name, func(t *testing.T) {
			span := spans[i]
			span.Name = "chat PRIVATE_MESSAGES_SPAN"
			span.Attributes = []*commonpb.KeyValue{
				stringKV("gen_ai.request.model", "shared-route"),
				stringKV("litellm.provider.model", "PRIVATE_SELECTED_A"),
				stringKV("gen_ai.response.model", "PRIVATE_SNAPSHOT_A_20261017"),
				stringKV("litellm.model_group", "PRIVATE_ROUTE"),
				stringKV("agent.id", "PRIVATE_AGENT"), stringKV("enduser.id", "PRIVATE_USER"),
				stringKV("gen_ai.request.prompt", "PRIVATE_PROMPT"),
				stringKV("gen_ai.conversation.id", "PRIVATE_SESSION"),
				intKV("gen_ai.usage.input_tokens", 100), intKV("gen_ai.usage.output_tokens", 10),
				intKV("gen_ai.usage.cache_read.input_tokens", 20), intKV("gen_ai.usage.cache_write.input_tokens", 5),
			}
			tc.edit(span)
			if strings.HasSuffix(tc.name, "/selected") {
				native = append(native, span)
			}
			one := &tracecollectorpb.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{
				Resource:   all.ResourceSpans[0].Resource,
				ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{span}}},
			}}}
			exportModelUsageProfile(t, ctx, profile.client, one)
			latest = waitSummary(t, profile.dir, uint64(i+1))
			if i == 0 {
				first = latest
			}
			w := want[tc.model]
			w.requests++
			if tc.missingUsage {
				w.missing++
			} else {
				w.complete++
			}
			want[tc.model] = w
			if latest.ModelUsage == nil || latest.ModelUsage.ContractID != profile.idle.ModelUsage.ContractID || len(latest.ModelUsage.Models) != len(want) {
				t.Fatal("Messages contract or identity buckets changed", latest.ModelUsage)
			}
			for id, w := range want {
				got := latest.ModelUsage.Models[id]
				if got.Requests != w.requests || got.Complete.Requests != w.complete || got.Excluded.MissingSplit != w.missing ||
					got.Excluded.InvalidUsage != 0 || got.Excluded.ZeroOriginUnknown != 0 ||
					got.Complete.InputTokens != 100*w.complete || got.Complete.OutputTokens != 10*w.complete ||
					got.Complete.CacheReadInputTokens != 20*w.complete || got.Complete.CacheWriteInputTokens != 5*w.complete {
					t.Fatalf("Messages identity/usage mismatch for %s: got %+v, want %+v", id, got, w)
				}
			}
		}) {
			t.FailNow()
		}
	}

	exportModelUsageProfile(t, ctx, baseline.client, all)
	base := waitSummary(t, baseline.dir, uint64(len(spans)))
	plain := latest
	plain.ModelUsage = nil
	plain.Epoch, plain.Sequence, plain.EmittedAt = base.Epoch, base.Sequence, base.EmittedAt
	plain.ObservedStart, plain.ObservedEnd = base.ObservedStart, base.ObservedEnd
	baseBytes, err := base.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	plainBytes, err := plain.MarshalBinary()
	if err != nil || !bytes.Equal(baseBytes, plainBytes) {
		t.Fatal("Messages changed base summary bytes beyond extension/runtime metadata", err)
	}

	model["identity"] = litellmCombinedIdentity
	combined := startModelUsageProfile(t, ctx, binary, model)
	all.ResourceSpans[0].ScopeSpans[0].Spans = native
	exportModelUsageProfile(t, ctx, combined.client, all)
	rejected := waitSummary(t, combined.dir, uint64(len(native)))
	if rejected.ModelUsage == nil || len(rejected.ModelUsage.Models) != 1 || rejected.ModelUsage.Models["__unmapped__"].Requests != 2 ||
		rejected.ModelUsage.ContractID == latest.ModelUsage.ContractID {
		t.Fatal("combined v1 admitted Messages or reused its contract", rejected.ModelUsage)
	}
	merged, err := summary.Combine([]summary.Envelope{latest, first, latest}, []string{"producer"})
	if err != nil || !reflect.DeepEqual(merged.Counters, latest.Counters) || !reflect.DeepEqual(merged.ModelUsage, latest.ModelUsage) {
		t.Fatal("older/replayed snapshots changed Messages cumulative accounting", err)
	}
	for _, docs := range [][]summary.Envelope{{latest, rejected}, {rejected, latest}} {
		var incompatibility *modelusage.Error
		if _, err := summary.Combine(docs, []string{"producer"}); !errors.As(err, &incompatibility) || incompatibility.Reason != "contract" {
			t.Fatal("Messages/combined snapshots did not fail contract check", err)
		}
	}

	for _, p := range []*modelUsageProfileProducer{profile, baseline} {
		metrics := scrapeEventually(t, p.prom, regexp.MustCompile(fmt.Sprintf(`(?m)^gen_ai_sketch_requests_total\{[^\n]*\}\s+%d$`, len(spans))))
		logs := waitForOutputEventually(t, p.logs, regexp.MustCompile(`genaisketch topk snapshot`))
		if strings.Contains(metrics, "PRIVATE_") || strings.Contains(logs, "PRIVATE_") {
			t.Fatal("private Messages sentinel escaped metrics or logs")
		}
		paths, err := filepath.Glob(filepath.Join(p.dir, "*.json"))
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range paths {
			data, err := os.ReadFile(path)
			if err != nil || bytes.Contains(data, []byte("PRIVATE_")) {
				t.Fatal("private Messages sentinel escaped summary", err)
			}
			doc, err := summary.Parse(data)
			if err != nil {
				t.Fatal(err)
			}
			for _, sketch := range doc.Sketches {
				if bytes.Contains(sketch.Data, []byte("PRIVATE_")) {
					t.Fatal("private Messages sentinel escaped encoded sketch")
				}
			}
		}
	}
}

func litellmMessagesCases() []litellmProfileCase {
	missingUsage := func(s *tracepb.Span) {
		s.Status = &tracepb.Status{Code: tracepb.Status_STATUS_CODE_ERROR}
		replaceProfileAttribute(s, "gen_ai.response.model")
		s.Attributes = slices.DeleteFunc(s.Attributes, func(kv *commonpb.KeyValue) bool { return strings.HasPrefix(kv.Key, "gen_ai.usage.") })
	}
	var rows []litellmProfileCase
	for _, stream := range []bool{false, true} {
		identity, prefix := litellmResponsesIdentity, "nonstream/"
		if stream {
			identity, prefix = litellmChatStreamIdentity, "stream/"
		}
		var cases []litellmProfileCase
		for _, tc := range litellmProfileCases(identity) {
			switch tc.name {
			case "selected", "absent-response", "alias-echo", "conflicting-response", "malformed-response", "duplicate-response",
				"missing-selected-no-resource-fallback", "malformed-selected", "duplicate-selected",
				"stream-string", "legacy-stream-string", "legacy-stream-duplicate",
				"missing-call-type", "duplicate-call-type", "missing-route", "duplicate-route", "wrong-operation":
				cases = append(cases, tc)
			}
		}
		cases = append(cases,
			litellmProfileCase{name: "native-auto-selected", model: "model-a", edit: func(s *tracepb.Span) {
				replaceProfileAttribute(s, "litellm.provider.model", stringKV("litellm.provider.model", "anthropic/PRIVATE_AUTO"))
				if stream {
					replaceProfileAttribute(s, "gen_ai.response.model", stringKV("gen_ai.response.model", "PRIVATE_AUTO"))
				}
			}},
			litellmProfileCase{name: "failed-primary-missing-usage", model: "model-a", missingUsage: true, edit: missingUsage},
			litellmProfileCase{name: "recovery-selected", model: "model-b", edit: func(s *tracepb.Span) {
				replaceProfileAttribute(s, "litellm.provider.model", stringKV("litellm.provider.model", "anthropic/PRIVATE_RECOVERY"))
				replaceProfileAttribute(s, "litellm.model_group", stringKV("litellm.model_group", "PRIVATE_RECOVERY_GROUP"))
				response := "PRIVATE_SNAPSHOT_B_20261017"
				if stream {
					response = "PRIVATE_RECOVERY"
				}
				replaceProfileAttribute(s, "gen_ai.response.model", stringKV("gen_ai.response.model", response))
			}},
			litellmProfileCase{name: "pre-provider-acompletion", model: "__unmapped__", missingUsage: true, edit: func(s *tracepb.Span) {
				missingUsage(s)
				replaceProfileAttribute(s, "llm.request.type", stringKV("llm.request.type", "acompletion"))
			}},
			litellmProfileCase{name: "duplicate-native-stream", model: "__unmapped__", edit: func(s *tracepb.Span) {
				s.Attributes = append(s.Attributes, profileBoolKV("gen_ai.request.stream", stream))
			}},
			litellmProfileCase{name: "conflicting-stream-flags", model: "__unmapped__", edit: func(s *tracepb.Span) {
				s.Attributes = append(s.Attributes, profileBoolKV("llm.is_streaming", !stream))
			}},
			litellmProfileCase{name: "legacy-only-true", model: "__unmapped__", edit: func(s *tracepb.Span) {
				replaceProfileAttribute(s, "gen_ai.request.stream")
				s.Attributes = append(s.Attributes, profileBoolKV("llm.is_streaming", true))
			}},
		)
		if stream {
			cases = append(cases, litellmProfileCase{name: "interrupted-missing-usage", model: "model-a", missingUsage: true, edit: missingUsage})
		} else {
			cases = append(cases, litellmProfileCase{name: "native-stream-marker-absent", model: "model-a", edit: func(s *tracepb.Span) {
				replaceProfileAttribute(s, "gen_ai.request.stream")
			}})
		}
		for _, tc := range cases {
			edit := tc.edit
			tc.name = prefix + tc.name
			tc.edit = func(s *tracepb.Span) {
				s.Attributes = append(s.Attributes, stringKV("llm.request.type", "anthropic_messages"),
					stringKV("http.route", "/v1/messages"), profileBoolKV("gen_ai.request.stream", stream))
				if edit != nil {
					edit(s)
				}
			}
			rows = append(rows, tc)
		}
	}
	return rows
}
