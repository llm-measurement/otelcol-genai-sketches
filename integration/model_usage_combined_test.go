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
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/modelusage"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
	tracecollectorpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

const litellmCombinedIdentity = "litellm-1.104.0-v1"

func TestLiteLLMCombinedModelUsageRealOTLP(t *testing.T) {
	binary := filepath.Join(repoRoot(t), "dist", "otelcol-genai-sketches")
	requireExecutable(t, binary)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	profile := startModelUsageProfile(t, ctx, binary, litellmCombinedConfig())
	baseline := startModelUsageProfile(t, ctx, binary, nil)
	if profile.idle.ModelUsage == nil || profile.idle.ModelUsage.Models == nil || len(profile.idle.ModelUsage.Models) != 0 || baseline.idle.ModelUsage != nil {
		t.Fatal("combined opt-in changed idle extension presence")
	}
	contract := profile.idle.ModelUsage.ContractID
	if contract == "" || profile.idle.AccountingID != baseline.idle.AccountingID {
		t.Fatal("combined contract missing or base accounting contract changed")
	}

	batches := litellmCombinedCases()
	all := traceRequest(make([]otlpSpanSpec, 4*len(batches))...)
	spans := all.ResourceSpans[0].ScopeSpans[0].Spans
	// Resource identities and markers must not repair absent span-local values.
	all.ResourceSpans[0].Resource.Attributes = append(all.ResourceSpans[0].Resource.Attributes,
		stringKV("litellm.provider.model", "PRIVATE_SELECTED_A"),
		stringKV("gen_ai.response.model", "PRIVATE_SNAPSHOT_A_20261017"),
		stringKV("llm.request.type", "acompletion"), stringKV("http.route", "/v1/chat/completions"),
		profileBoolKV("gen_ai.request.stream", true))
	type totals struct{ requests, complete, missing, tokenUnits uint64 }
	want := map[string]totals{}
	var first, latest summary.Envelope
	for batchIndex, batch := range batches {
		if !t.Run(strings.SplitN(batch[0].name, "/", 2)[1], func(t *testing.T) {
			for pathIndex, tc := range batch {
				i := 4*batchIndex + pathIndex
				span := spans[i]
				// Distinct token amounts expose a path swap even if request counts agree.
				units := int64(1 << pathIndex)
				span.Name = "chat PRIVATE_COMBINED_SPAN"
				span.Attributes = []*commonpb.KeyValue{
					stringKV("gen_ai.request.model", "shared-route"),
					stringKV("litellm.provider.model", "PRIVATE_SELECTED_A"),
					stringKV("gen_ai.response.model", "PRIVATE_SNAPSHOT_A_20261017"),
					stringKV("litellm.model_group", "PRIVATE_ROUTE"),
					stringKV("agent.id", "PRIVATE_AGENT"), stringKV("enduser.id", "PRIVATE_USER"),
					stringKV("gen_ai.request.prompt", "PRIVATE_PROMPT"),
					stringKV("gen_ai.conversation.id", "PRIVATE_SESSION"),
					stringKV("retrieval.doc_id", "PRIVATE_DOCUMENT"),
					stringKV("request.id", fmt.Sprintf("PRIVATE_REQUEST_%d", i)),
					intKV("gen_ai.usage.input_tokens", 100*units), intKV("gen_ai.usage.output_tokens", 10*units),
					intKV("gen_ai.usage.cache_read.input_tokens", 20*units), intKV("gen_ai.usage.cache_write.input_tokens", 5*units),
				}
				tc.edit(span)
				w := want[tc.model]
				w.requests++
				if tc.missingUsage {
					w.missing++
				} else {
					w.complete++
					w.tokenUnits += uint64(units)
				}
				want[tc.model] = w
			}
			mixed := &tracecollectorpb.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{
				Resource: all.ResourceSpans[0].Resource,
				ScopeSpans: []*tracepb.ScopeSpans{{
					Spans: spans[4*batchIndex : 4*(batchIndex+1)],
				}},
			}}}
			exportModelUsageProfile(t, ctx, profile.client, mixed)
			latest = waitSummary(t, profile.dir, uint64(4*(batchIndex+1)))
			if batchIndex == 0 {
				first = latest
			}
			if latest.ProducerID != "producer" || latest.ModelUsage == nil || latest.ModelUsage.ContractID != contract {
				t.Fatal("mixed traffic changed producer or combined contract")
			}
			if len(latest.ModelUsage.Models) != len(want) {
				t.Fatal("unexpected combined identity buckets", latest.ModelUsage.Models)
			}
			for id, w := range want {
				got := latest.ModelUsage.Models[id]
				if got.Requests != w.requests || got.Complete.Requests != w.complete || got.Excluded.MissingSplit != w.missing ||
					got.Excluded.InvalidUsage != 0 || got.Excluded.ZeroOriginUnknown != 0 ||
					got.Complete.InputTokens != 100*w.tokenUnits || got.Complete.OutputTokens != 10*w.tokenUnits ||
					got.Complete.CacheReadInputTokens != 20*w.tokenUnits || got.Complete.CacheWriteInputTokens != 5*w.tokenUnits {
					t.Fatalf("combined identity/usage mismatch for %s: got %+v, want %+v", id, got, w)
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
		t.Fatal("combined profile changed default summary bytes beyond extension/runtime metadata", err)
	}
	merged, err := summary.Combine([]summary.Envelope{latest, first, latest}, []string{"producer"})
	if err != nil || !reflect.DeepEqual(merged.Counters, latest.Counters) || !reflect.DeepEqual(merged.ModelUsage, latest.ModelUsage) {
		t.Fatal("older/replayed snapshots changed combined cumulative accounting", err)
	}
	var incompatibility *modelusage.Error
	if _, err := summary.Combine([]summary.Envelope{base, latest}, []string{"producer"}); !errors.As(err, &incompatibility) || incompatibility.Reason != "presence" {
		t.Fatal("mixed default/combined snapshots did not fail extension presence check", err)
	}

	for _, identity := range []string{"", "agentgateway-1.6.0-chat-v1", "envoy-ai-gateway-1.1.0-chat-v1", "litellm-1.104.0-chat-v1", litellmChatStreamIdentity, litellmResponsesIdentity} {
		name := identity
		if name == "" {
			name = "legacy-request-model"
		}
		t.Run("reject-contract-mix/"+name, func(t *testing.T) {
			otherCtx, stop := context.WithTimeout(ctx, 20*time.Second)
			defer stop()
			model := litellmProfileConfig(identity)
			if identity == "litellm-1.104.0-chat-v1" {
				model["response_models"] = litellmCombinedConfig()["response_models"]
			}
			other := startModelUsageProfile(t, otherCtx, binary, model)
			if other.idle.ModelUsage == nil || other.idle.ModelUsage.ContractID == contract || other.idle.AccountingID != latest.AccountingID {
				t.Fatal("combined contract not distinct from legacy/profile contract")
			}
			for _, docs := range [][]summary.Envelope{{latest, other.idle}, {other.idle, latest}} {
				var incompatibility *modelusage.Error
				if _, err := summary.Combine(docs, []string{"producer"}); !errors.As(err, &incompatibility) || incompatibility.Reason != "contract" {
					t.Fatal("combined and legacy/profile mix did not fail contract check", err)
				}
			}
		})
	}

	for _, p := range []*modelUsageProfileProducer{profile, baseline} {
		metrics := scrapeEventually(t, p.prom, regexp.MustCompile(fmt.Sprintf(`(?m)^gen_ai_sketch_requests_total\{[^\n]*\}\s+%d$`, len(spans))))
		logs := waitForOutputEventually(t, p.logs, regexp.MustCompile(`genaisketch topk snapshot`))
		if strings.Contains(metrics, "PRIVATE_") || strings.Contains(logs, "PRIVATE_") {
			t.Fatal("private combined sentinel escaped metrics or logs")
		}
		paths, err := filepath.Glob(filepath.Join(p.dir, "*.json"))
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range paths {
			data, err := os.ReadFile(path)
			if err != nil || bytes.Contains(data, []byte("PRIVATE_")) {
				t.Fatal("private combined sentinel escaped summary", err)
			}
			doc, err := summary.Parse(data)
			if err != nil {
				t.Fatal(err)
			}
			for _, sketch := range doc.Sketches {
				if bytes.Contains(sketch.Data, []byte("PRIVATE_")) {
					t.Fatal("private combined sentinel escaped encoded sketch")
				}
			}
		}
	}
}

func litellmCombinedConfig() map[string]any {
	model := litellmProfileConfig(litellmResponsesIdentity)
	model["identity"] = litellmCombinedIdentity
	return model
}

func litellmCombinedCases() [][]litellmProfileCase {
	var batches [][]litellmProfileCase
	for _, path := range []struct {
		name, source, call, route string
		stream                    bool
	}{
		{"nonstream-chat", litellmResponsesIdentity, "acompletion", "/v1/chat/completions", false},
		{"stream-chat", litellmChatStreamIdentity, "acompletion", "/v1/chat/completions", true},
		{"nonstream-responses", litellmResponsesIdentity, "aresponses", "/v1/responses", false},
		{"stream-responses", litellmResponsesIdentity, "aresponses", "/v1/responses", true},
	} {
		cases := litellmProfileCases(path.source)
		for i := range cases {
			switch cases[i].name {
			case "stream-false", "stream-absent":
				cases[i].model = "model-a"
			case "stream-cross-flag-conflict":
				if !path.stream {
					cases[i].model = "model-a" // Both flags are false in this path.
				}
			case "stream-cross-flag-agreement":
				if !path.stream {
					cases[i].model = "__unmapped__" // The legacy flag alone becomes true.
				}
			}
		}
		cases = append(cases,
			litellmProfileCase{name: "duplicate-call-type-same", model: "__unmapped__", edit: func(s *tracepb.Span) {
				s.Attributes = append(s.Attributes, stringKV("llm.request.type", path.call))
			}},
			litellmProfileCase{name: "duplicate-route-same", model: "__unmapped__", edit: func(s *tracepb.Span) {
				s.Attributes = append(s.Attributes, stringKV("http.route", path.route))
			}},
			litellmProfileCase{name: "duplicate-operation", model: "__unmapped__", edit: func(s *tracepb.Span) {
				s.Attributes = append(s.Attributes, stringKV("gen_ai.operation.name", "chat"), stringKV("gen_ai.operation.name", "chat"))
			}},
			litellmProfileCase{name: "chat-operation-cannot-repair-missing-markers", model: "__unmapped__", edit: func(s *tracepb.Span) {
				replaceProfileAttribute(s, "llm.request.type")
				replaceProfileAttribute(s, "http.route")
				s.Attributes = append(s.Attributes, stringKV("gen_ai.operation.name", "chat"))
			}},
			litellmProfileCase{name: "crossed-native-markers", model: "__unmapped__", edit: func(s *tracepb.Span) {
				other := "aresponses"
				if path.call == other {
					other = "acompletion"
				}
				replaceProfileAttribute(s, "llm.request.type", stringKV("llm.request.type", other))
			}},
			litellmProfileCase{name: "native-route-must-be-exact", model: "__unmapped__", edit: func(s *tracepb.Span) {
				replaceProfileAttribute(s, "http.route", stringKV("http.route", path.route+"/"))
			}},
			litellmProfileCase{name: "messages-still-unsupported", model: "__unmapped__", edit: func(s *tracepb.Span) {
				replaceProfileAttribute(s, "llm.request.type", stringKV("llm.request.type", "anthropic_messages"))
				replaceProfileAttribute(s, "http.route", stringKV("http.route", "/v1/messages"))
			}},
		)
		for i, tc := range cases {
			edit := tc.edit
			tc.name = path.name + "/" + tc.name
			tc.edit = func(s *tracepb.Span) {
				replaceProfileAttribute(s, "llm.request.type", stringKV("llm.request.type", path.call))
				replaceProfileAttribute(s, "http.route", stringKV("http.route", path.route))
				replaceProfileAttribute(s, "gen_ai.request.stream", profileBoolKV("gen_ai.request.stream", path.stream))
				if edit != nil {
					edit(s)
				}
				if strings.HasSuffix(tc.name, "/recovery-selected-model") {
					replaceProfileAttribute(s, "litellm.model_group", stringKV("litellm.model_group", "PRIVATE_RECOVERY_ROUTE"))
				}
			}
			if i == len(batches) {
				batches = append(batches, nil)
			}
			batches[i] = append(batches[i], tc)
		}
	}
	return batches
}

func TestLiteLLMCombinedModelUsageConfigValidation(t *testing.T) {
	binary := filepath.Join(repoRoot(t), "dist", "otelcol-genai-sketches")
	requireExecutable(t, binary)
	for _, tc := range []struct {
		name        string
		responses   any
		omit, valid bool
	}{
		{"missing-map", nil, true, false},
		{"null-map", nil, false, false},
		{"empty-map", map[string]string{}, false, true},
		{"approved-map", map[string]string{"PRIVATE_SNAPSHOT_A_20261017": "model-a"}, false, true},
		{"nonpublic-id", map[string]string{"PRIVATE_RESPONSE": "https://PRIVATE_HOST/?secret=PRIVATE_SECRET"}, false, false},
		{"empty-name", map[string]string{"": "model-a"}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := litellmCombinedConfig()
			delete(model, "response_models")
			if !tc.omit {
				model["response_models"] = tc.responses
			}
			path := writeModelUsageProfileConfig(t, freePort(t), freePort(t), t.TempDir(), model)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "validate", "--config", path)
			cmd.Env = append(os.Environ(), "GENAI_SKETCH_SECRET="+testSketchSecret)
			output, err := cmd.CombinedOutput()
			if ctx.Err() != nil || (err == nil) != tc.valid {
				t.Fatalf("combined validation valid=%t err=%v output=%s", tc.valid, err, output)
			}
			if bytes.Contains(output, []byte("PRIVATE_")) {
				t.Fatal("private combined model map escaped validation output")
			}
		})
	}
}
