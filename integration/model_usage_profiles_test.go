//go:build integration

// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package integration

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
	tracecollectorpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"go.yaml.in/yaml/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	litellmChatStreamIdentity = "litellm-1.104.0-chat-stream-v1"
	litellmResponsesIdentity  = "litellm-1.104.0-responses-v1"
)

func TestLiteLLMModelUsageProfilesRealOTLP(t *testing.T) {
	binary := filepath.Join(repoRoot(t), "dist", "otelcol-genai-sketches")
	requireExecutable(t, binary)
	contracts := map[string]bool{}
	var documents []summary.Envelope
	for _, identity := range []string{litellmChatStreamIdentity, litellmResponsesIdentity} {
		t.Run(identity, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			profile := startModelUsageProfile(t, ctx, binary, litellmProfileConfig(identity))
			baseline := startModelUsageProfile(t, ctx, binary, nil)
			if profile.idle.ModelUsage == nil || profile.idle.ModelUsage.Models == nil || len(profile.idle.ModelUsage.Models) != 0 || baseline.idle.ModelUsage != nil {
				t.Fatal("opt-in changed idle extension presence")
			}
			contract := profile.idle.ModelUsage.ContractID
			if contracts[contract] || contract == "" {
				t.Fatal("profile contracts are not distinct")
			}
			contracts[contract] = true
			if profile.idle.AccountingID != baseline.idle.AccountingID {
				t.Fatal("profile changed the base accounting contract")
			}

			callType, route := "acompletion", "/v1/chat/completions"
			if identity == litellmResponsesIdentity {
				callType, route = "aresponses", "/v1/responses"
			}
			rows := litellmProfileCases(identity)
			all := traceRequest(make([]otlpSpanSpec, len(rows))...)
			spans := all.ResourceSpans[0].ScopeSpans[0].Spans
			for i, tc := range rows {
				span := spans[i]
				span.Name = "chat PRIVATE_PROFILE_SPAN"
				span.Attributes = []*commonpb.KeyValue{
					stringKV("gen_ai.request.model", "shared-route"),
					stringKV("llm.request.type", callType), stringKV("http.route", route),
					stringKV("litellm.provider.model", "PRIVATE_SELECTED_A"),
					stringKV("gen_ai.response.model", "PRIVATE_SNAPSHOT_A_20261017"),
					profileBoolKV("gen_ai.request.stream", true),
					stringKV("litellm.model_group", "PRIVATE_ROUTE"),
					stringKV("agent.id", "PRIVATE_AGENT"), stringKV("enduser.id", "PRIVATE_USER"),
					stringKV("gen_ai.request.prompt", "PRIVATE_PROMPT"),
					stringKV("gen_ai.conversation.id", "PRIVATE_SESSION"),
					stringKV("request.id", fmt.Sprintf("PRIVATE_REQUEST_%d", i)),
					intKV("gen_ai.usage.input_tokens", 100), intKV("gen_ai.usage.output_tokens", 10),
					intKV("gen_ai.usage.cache_read.input_tokens", 20), intKV("gen_ai.usage.cache_write.input_tokens", 5),
				}
				if tc.edit != nil {
					tc.edit(span)
				}
			}
			// These valid resource identities must never repair a missing span identity.
			all.ResourceSpans[0].Resource.Attributes = append(all.ResourceSpans[0].Resource.Attributes,
				stringKV("litellm.provider.model", "PRIVATE_SELECTED_A"),
				stringKV("gen_ai.response.model", "PRIVATE_SNAPSHOT_A_20261017"))

			type totals struct{ requests, complete, missing uint64 }
			want := map[string]totals{}
			var first, latest summary.Envelope
			var sent []*tracepb.Span
			for i, tc := range rows {
				if !t.Run(tc.name, func(t *testing.T) {
					one := &tracecollectorpb.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{
						Resource:   all.ResourceSpans[0].Resource,
						ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{spans[i]}}},
					}}}
					exportModelUsageProfile(t, ctx, profile.client, one)
					sent = append(sent, spans[i])
					latest = waitSummary(t, profile.dir, uint64(len(sent)))
					if len(sent) == 1 {
						first = latest
					}
					if latest.ModelUsage == nil || latest.ModelUsage.ContractID != contract {
						t.Fatal("profile contract changed after traffic")
					}
					w := want[tc.model]
					w.requests++
					if tc.missingUsage {
						w.missing++
					} else {
						w.complete++
					}
					want[tc.model] = w
					if len(latest.ModelUsage.Models) != len(want) {
						t.Fatal("unexpected identity buckets", latest.ModelUsage.Models)
					}
					for id, w := range want {
						got := latest.ModelUsage.Models[id]
						if got.Requests != w.requests || got.Complete.Requests != w.complete || got.Excluded.MissingSplit != w.missing ||
							got.Complete.InputTokens != 100*w.complete || got.Complete.OutputTokens != 10*w.complete ||
							got.Complete.CacheReadInputTokens != 20*w.complete || got.Complete.CacheWriteInputTokens != 5*w.complete {
							t.Fatalf("identity/usage mismatch for %s: %+v", id, got)
						}
					}
				}) {
					t.FailNow()
				}
			}

			if len(sent) == 0 {
				return
			}
			all.ResourceSpans[0].ScopeSpans[0].Spans = sent
			exportModelUsageProfile(t, ctx, baseline.client, all)
			base := waitSummary(t, baseline.dir, uint64(len(sent)))
			// Only producer runtime metadata and the opt-in block may differ.
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
				t.Fatal("profile changed default summary bytes beyond extension/runtime metadata", err)
			}
			merged, err := summary.Combine([]summary.Envelope{latest, first, latest}, []string{"producer"})
			if err != nil || !reflect.DeepEqual(merged.Counters, latest.Counters) || !reflect.DeepEqual(merged.ModelUsage, latest.ModelUsage) {
				t.Fatal("older/replayed snapshots changed cumulative accounting", err)
			}
			if _, err := summary.Combine([]summary.Envelope{base, latest}, []string{"producer"}); err == nil {
				t.Fatal("mixed default/profile snapshots combined")
			}
			documents = append(documents, latest)
			for _, p := range []*modelUsageProfileProducer{profile, baseline} {
				metrics := scrapeEventually(t, p.prom, regexp.MustCompile(fmt.Sprintf(`(?m)^gen_ai_sketch_requests_total\{[^\n]*\}\s+%d$`, len(sent))))
				logs := waitForOutputEventually(t, p.logs, regexp.MustCompile(`genaisketch topk snapshot`))
				if strings.Contains(metrics, "PRIVATE_") || strings.Contains(logs, "PRIVATE_") {
					t.Fatal("private profile sentinel escaped metrics or logs")
				}
				paths, err := filepath.Glob(filepath.Join(p.dir, "*.json"))
				if err != nil {
					t.Fatal(err)
				}
				for _, path := range paths {
					data, err := os.ReadFile(path)
					if err != nil || bytes.Contains(data, []byte("PRIVATE_")) {
						t.Fatal("private profile sentinel escaped summary", err)
					}
					doc, err := summary.Parse(data)
					if err != nil {
						t.Fatal(err)
					}
					for _, sketch := range doc.Sketches {
						if bytes.Contains(sketch.Data, []byte("PRIVATE_")) {
							t.Fatal("private profile sentinel escaped encoded sketch")
						}
					}
				}
			}
		})
	}
	if len(documents) == 2 {
		if _, err := summary.Combine(documents, []string{"producer"}); err == nil {
			t.Fatal("different profile contracts combined")
		}
	}
}

type litellmProfileCase struct {
	name, model  string
	edit         func(*tracepb.Span)
	missingUsage bool
}

func litellmProfileCases(identity string) []litellmProfileCase {
	selectedOnly := identity == litellmChatStreamIdentity
	responseFailure, nonstream := "__unmapped__", "model-a"
	if selectedOnly {
		responseFailure, nonstream = "model-a", "__unmapped__"
	}
	set := func(kv *commonpb.KeyValue) func(*tracepb.Span) {
		return func(s *tracepb.Span) { replaceProfileAttribute(s, kv.Key, kv) }
	}
	drop := func(key string) func(*tracepb.Span) {
		return func(s *tracepb.Span) { replaceProfileAttribute(s, key) }
	}
	duplicate := func(kv *commonpb.KeyValue) func(*tracepb.Span) {
		return func(s *tracepb.Span) { s.Attributes = append(s.Attributes, kv) }
	}
	return []litellmProfileCase{
		{name: "selected", model: "model-a"},
		{name: "recovery-selected-model", model: "model-b", edit: func(s *tracepb.Span) {
			replaceProfileAttribute(s, "litellm.provider.model", stringKV("litellm.provider.model", "PRIVATE_SELECTED_B"))
			replaceProfileAttribute(s, "gen_ai.response.model", stringKV("gen_ai.response.model", "PRIVATE_SNAPSHOT_B_20261017"))
		}},
		{name: "explicit-chat-operation", model: "model-a", edit: set(stringKV("gen_ai.operation.name", "chat"))},
		{name: "empty-operation", model: "__unmapped__", edit: set(stringKV("gen_ai.operation.name", ""))},
		{name: "malformed-operation", model: "__unmapped__", edit: set(intKV("gen_ai.operation.name", 1))},
		{name: "wrong-operation", model: "__unmapped__", edit: set(stringKV("gen_ai.operation.name", "embeddings"))},
		{name: "absent-response", model: "model-a", edit: drop("gen_ai.response.model")},
		{name: "unapproved-snapshot-no-prefix-match", model: responseFailure, edit: set(stringKV("gen_ai.response.model", "PRIVATE_SNAPSHOT_A_20261018"))},
		{name: "alias-echo", model: responseFailure, edit: set(stringKV("gen_ai.response.model", "PRIVATE_ROUTE"))},
		{name: "conflicting-response", model: responseFailure, edit: set(stringKV("gen_ai.response.model", "PRIVATE_SNAPSHOT_B_20261017"))},
		{name: "empty-response", model: responseFailure, edit: set(stringKV("gen_ai.response.model", ""))},
		{name: "malformed-response", model: responseFailure, edit: set(intKV("gen_ai.response.model", 1))},
		{name: "duplicate-response", model: responseFailure, edit: duplicate(stringKV("gen_ai.response.model", "PRIVATE_SNAPSHOT_A_20261017"))},
		{name: "missing-selected-no-resource-fallback", model: "__missing__", edit: drop("litellm.provider.model")},
		{name: "empty-selected", model: "__missing__", edit: set(stringKV("litellm.provider.model", ""))},
		{name: "unmapped-selected", model: "__unmapped__", edit: set(stringKV("litellm.provider.model", "PRIVATE_UNKNOWN_SELECTED"))},
		{name: "malformed-selected", model: "__unmapped__", edit: set(intKV("litellm.provider.model", 1))},
		{name: "duplicate-selected", model: "__unmapped__", edit: duplicate(stringKV("litellm.provider.model", "PRIVATE_SELECTED_A"))},
		{name: "oversized-selected", model: "__too_long__", edit: set(stringKV("litellm.provider.model", strings.Repeat("PRIVATE_", 40)))},
		{name: "stream-false", model: nonstream, edit: set(profileBoolKV("gen_ai.request.stream", false))},
		{name: "stream-absent", model: nonstream, edit: drop("gen_ai.request.stream")},
		{name: "stream-string", model: "__unmapped__", edit: set(stringKV("gen_ai.request.stream", "true"))},
		{name: "stream-string-false", model: "__unmapped__", edit: set(stringKV("gen_ai.request.stream", "false"))},
		{name: "stream-int", model: "__unmapped__", edit: set(intKV("gen_ai.request.stream", 1))},
		{name: "stream-duplicate-same", model: "__unmapped__", edit: duplicate(profileBoolKV("gen_ai.request.stream", true))},
		{name: "stream-duplicate-conflict", model: "__unmapped__", edit: duplicate(profileBoolKV("gen_ai.request.stream", false))},
		{name: "stream-cross-flag-conflict", model: "__unmapped__", edit: set(profileBoolKV("llm.is_streaming", false))},
		{name: "stream-cross-flag-agreement", model: "model-a", edit: set(profileBoolKV("llm.is_streaming", true))},
		{name: "legacy-stream-string", model: "__unmapped__", edit: set(stringKV("llm.is_streaming", "false"))},
		{name: "legacy-stream-duplicate", model: "__unmapped__", edit: func(s *tracepb.Span) {
			s.Attributes = append(s.Attributes, profileBoolKV("llm.is_streaming", true), profileBoolKV("llm.is_streaming", true))
		}},
		{name: "missing-call-type", model: "__unmapped__", edit: drop("llm.request.type")},
		{name: "wrong-call-type", model: "__unmapped__", edit: set(stringKV("llm.request.type", "PRIVATE_CALL_TYPE"))},
		{name: "malformed-call-type", model: "__unmapped__", edit: set(intKV("llm.request.type", 1))},
		{name: "duplicate-call-type", model: "__unmapped__", edit: duplicate(stringKV("llm.request.type", "PRIVATE_CALL_TYPE"))},
		{name: "missing-route", model: "__unmapped__", edit: drop("http.route")},
		{name: "wrong-route-with-chat-operation", model: "__unmapped__", edit: func(s *tracepb.Span) {
			s.Attributes = append(s.Attributes, stringKV("gen_ai.operation.name", "chat"))
			replaceProfileAttribute(s, "http.route", stringKV("http.route", "/PRIVATE_ROUTE"))
		}},
		{name: "malformed-route", model: "__unmapped__", edit: set(intKV("http.route", 1))},
		{name: "duplicate-route", model: "__unmapped__", edit: duplicate(stringKV("http.route", "/PRIVATE_ROUTE"))},
		{name: "identity-does-not-supply-missing-usage", model: "model-a", missingUsage: true, edit: func(s *tracepb.Span) {
			s.Attributes = slices.DeleteFunc(s.Attributes, func(kv *commonpb.KeyValue) bool { return strings.HasPrefix(kv.Key, "gen_ai.usage.") })
		}},
	}
}

func TestLiteLLMModelUsageProfileConfigValidation(t *testing.T) {
	binary := filepath.Join(repoRoot(t), "dist", "otelcol-genai-sketches")
	requireExecutable(t, binary)
	for _, tc := range []struct {
		name, identity string
		responses      any
		omit, valid    bool
	}{
		{"chat-stream-no-response-map", litellmChatStreamIdentity, nil, true, true},
		{"chat-stream-empty-response-map", litellmChatStreamIdentity, map[string]string{}, false, false},
		{"chat-stream-response-map", litellmChatStreamIdentity, map[string]string{"PRIVATE_RESPONSE": "model-a"}, false, false},
		{"responses-missing-map", litellmResponsesIdentity, nil, true, false},
		{"responses-null-map", litellmResponsesIdentity, nil, false, false},
		{"responses-empty-map", litellmResponsesIdentity, map[string]string{}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := litellmProfileConfig(tc.identity)
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
				t.Fatalf("validation valid=%t err=%v output=%s", tc.valid, err, output)
			}
			if bytes.Contains(output, []byte("PRIVATE_")) {
				t.Fatal("private model map escaped validation output")
			}
		})
	}
}

func litellmProfileConfig(identity string) map[string]any {
	m := map[string]any{"slice": "model", "identity": identity,
		"models": map[string]string{"PRIVATE_SELECTED_A": "model-a", "PRIVATE_SELECTED_B": "model-b"}}
	if identity == litellmResponsesIdentity {
		m["response_models"] = map[string]string{"PRIVATE_SNAPSHOT_A_20261017": "model-a", "PRIVATE_SNAPSHOT_B_20261017": "model-b"}
	}
	return m
}

func replaceProfileAttribute(span *tracepb.Span, key string, values ...*commonpb.KeyValue) {
	span.Attributes = slices.DeleteFunc(span.Attributes, func(kv *commonpb.KeyValue) bool { return kv.Key == key })
	span.Attributes = append(span.Attributes, values...)
}

func profileBoolKV(key string, value bool) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: key, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_BoolValue{BoolValue: value}}}
}

type modelUsageProfileProducer struct {
	dir    string
	prom   int
	logs   *bytes.Buffer
	idle   summary.Envelope
	client tracecollectorpb.TraceServiceClient
}

func startModelUsageProfile(t *testing.T, ctx context.Context, binary string, model map[string]any) *modelUsageProfileProducer {
	t.Helper()
	p := &modelUsageProfileProducer{dir: t.TempDir(), prom: freePort(t)}
	otlp := freePort(t)
	path := writeModelUsageProfileConfig(t, otlp, p.prom, p.dir, model)
	_, p.logs = startCollector(t, ctx, binary, path)
	waitForOutputEventually(t, p.logs, regexp.MustCompile(`started genaisketch connector`))
	p.idle = waitSummary(t, p.dir, 0)
	call, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	conn, err := grpc.DialContext(call, fmt.Sprintf("127.0.0.1:%d", otlp), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	p.client = tracecollectorpb.NewTraceServiceClient(conn)
	return p
}

func writeModelUsageProfileConfig(t *testing.T, otlp, prom int, dir string, model map[string]any) string {
	t.Helper()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := writeCollectorConfig(t, otlp, prom)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	c := cfg["connectors"].(map[string]any)["genaisketch"].(map[string]any)
	c["window_duration"] = "24h"
	c["slices"] = []any{map[string]any{"name": "model", "keys": []string{"gen_ai.request.model"}}}
	export := map[string]any{"directory": dir, "producer_id": "producer", "scope_id": "scope", "key_id": "synthetic", "interval": "1s"}
	if model != nil {
		export["model_usage"] = model
	}
	c["summary_export"] = export
	cfg["service"].(map[string]any)["telemetry"] = map[string]any{"metrics": map[string]any{"level": "none"}}
	data, err = yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func exportModelUsageProfile(t *testing.T, ctx context.Context, client tracecollectorpb.TraceServiceClient, request *tracecollectorpb.ExportTraceServiceRequest) {
	t.Helper()
	call, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	response, err := client.Export(call, request)
	if err != nil {
		t.Fatal(err)
	}
	if partial := response.GetPartialSuccess(); partial.GetRejectedSpans() != 0 || partial.GetErrorMessage() != "" {
		t.Fatal("collector partially rejected synthetic profile batch")
	}
}
