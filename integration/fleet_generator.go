//go:build integration

// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay Erramilli and Codex

package integration

import (
	"encoding/binary"
	"fmt"
	"math/rand"
	"time"

	tracecollectorpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

func fixedID(value int64, size int) []byte {
	buf := make([]byte, size)
	if size >= 16 {
		binary.BigEndian.PutUint64(buf[:8], uint64(value)*0x9e3779b185ebca87)
		binary.BigEndian.PutUint64(buf[8:], uint64(value))
		return buf
	}
	binary.BigEndian.PutUint64(buf, uint64(value))
	return buf
}

const (
	fleetTreeSpans       = 10
	fleetLLMSpansPerTree = 3
	fleetMinDepth        = 3
	fleetMaxDepth        = 8
	fleetActiveTenants   = 1000
	fleetOverflowTenants = 200
	fleetBurstBatches    = 4
)

type fleetBatchStats struct {
	Trees            int64
	Spans            int64
	LLMSpans         int64
	MissingLLMSpans  int64
	ActiveLLMSpans   int64
	OverflowLLMSpans int64
	AgentRoots       int64
	MinDepth         int
	MaxDepth         int
}

func (s *fleetBatchStats) add(other fleetBatchStats) {
	s.Trees += other.Trees
	s.Spans += other.Spans
	s.LLMSpans += other.LLMSpans
	s.MissingLLMSpans += other.MissingLLMSpans
	s.ActiveLLMSpans += other.ActiveLLMSpans
	s.OverflowLLMSpans += other.OverflowLLMSpans
	s.AgentRoots += other.AgentRoots
	if s.MinDepth == 0 || other.MinDepth < s.MinDepth {
		s.MinDepth = other.MinDepth
	}
	if other.MaxDepth > s.MaxDepth {
		s.MaxDepth = other.MaxDepth
	}
}

type fleetGenerator struct {
	nextTree int64
	zipf     *rand.Zipf
}

func newFleetGenerator() *fleetGenerator {
	rng := rand.New(rand.NewSource(20260711))
	return &fleetGenerator{
		zipf: rand.NewZipf(rng, 1.2, 1, fleetActiveTenants-1),
	}
}

func (g *fleetGenerator) batch(spanCount int, activeOnly bool) (*tracecollectorpb.ExportTraceServiceRequest, fleetBatchStats) {
	if spanCount%fleetTreeSpans != 0 {
		panic(fmt.Sprintf("fleet batch size %d is not divisible by %d spans/tree", spanCount, fleetTreeSpans))
	}

	treeCount := spanCount / fleetTreeSpans
	resourceSpans := make([]*tracepb.ResourceSpans, 0, treeCount)
	stats := fleetBatchStats{MinDepth: fleetMaxDepth}
	for i := 0; i < treeCount; i++ {
		treeIndex := g.nextTree
		g.nextTree++
		overflow := !activeOnly && treeIndex%20 == 0
		tenantIndex := int(g.zipf.Uint64())
		if activeOnly {
			tenantIndex = int(treeIndex % fleetActiveTenants)
		} else if overflow {
			tenantIndex = fleetActiveTenants + int((treeIndex/20)%fleetOverflowTenants)
		}

		depth := fleetMinDepth + int(treeIndex%int64(fleetMaxDepth-fleetMinDepth+1))
		resourceSpans = append(resourceSpans, fleetTree(treeIndex, tenantIndex, depth))
		stats.Trees++
		stats.Spans += fleetTreeSpans
		stats.LLMSpans += fleetLLMSpansPerTree
		stats.AgentRoots++
		if overflow {
			stats.OverflowLLMSpans += fleetLLMSpansPerTree
		} else {
			stats.ActiveLLMSpans += fleetLLMSpansPerTree
		}
		for llm := 0; llm < fleetLLMSpansPerTree; llm++ {
			if (treeIndex*fleetLLMSpansPerTree+int64(llm))%10 == 0 {
				stats.MissingLLMSpans++
			}
		}
		if depth < stats.MinDepth {
			stats.MinDepth = depth
		}
		if depth > stats.MaxDepth {
			stats.MaxDepth = depth
		}
	}

	return &tracecollectorpb.ExportTraceServiceRequest{ResourceSpans: resourceSpans}, stats
}

func fleetTree(treeIndex int64, tenantIndex int, depth int) *tracepb.ResourceSpans {
	now := uint64(time.Now().UnixNano())
	traceID := fixedID(treeIndex+1, 16)
	spanIDs := make([][]byte, fleetTreeSpans)
	for i := range spanIDs {
		spanIDs[i] = fixedID(treeIndex*fleetTreeSpans+int64(i)+1, 8)
	}

	spans := make([]*tracepb.Span, 0, fleetTreeSpans)
	for i, spec := range fleetSpanSpecs {
		parentIndex := -1
		if i > 0 && i < depth {
			parentIndex = i - 1
		} else if i >= depth {
			parentIndex = (i - depth) % depth
		}
		attrs := []*commonpb.KeyValue{
			stringKV("gen_ai.operation.name", spec.operation),
			stringKV("request.id", fmt.Sprintf("fleet-request-%012d-%02d", treeIndex, i)),
		}
		if spec.llmIndex >= 0 {
			llmOrdinal := treeIndex*fleetLLMSpansPerTree + int64(spec.llmIndex)
			attrs = append(attrs,
				stringKV("gen_ai.request.model", fmt.Sprintf("fleet-model-%02d", llmOrdinal%24)),
				stringKV("gen_ai.request.prompt", fmt.Sprintf("subagent-%02d task-%012d", treeIndex%32, llmOrdinal)),
				stringKV("retrieval.doc_id", fmt.Sprintf("doc-%08d", llmOrdinal%100_000)),
			)
			if llmOrdinal%10 != 0 {
				attrs = append(attrs,
					intKV("gen_ai.usage.input_tokens", 80+llmOrdinal%900),
					intKV("gen_ai.usage.output_tokens", 20+llmOrdinal%450),
				)
			}
		}
		if spec.operation == "execute_tool" {
			attrs = append(attrs, stringKV("gen_ai.tool.name", fmt.Sprintf("fleet-tool-%02d", treeIndex%20)))
		}
		if spec.operation == "mcp" {
			attrs = append(attrs,
				stringKV("mcp.method.name", "tools/call"),
				stringKV("mcp.session.id", fmt.Sprintf("session-%012d", treeIndex)),
			)
		}

		span := &tracepb.Span{
			TraceId:           traceID,
			SpanId:            spanIDs[i],
			Name:              spec.name,
			Kind:              spec.kind,
			StartTimeUnixNano: now + uint64(i)*uint64(time.Millisecond),
			EndTimeUnixNano:   now + uint64(i+1)*uint64(time.Millisecond),
			Attributes:        attrs,
		}
		if parentIndex >= 0 {
			span.ParentSpanId = spanIDs[parentIndex]
		}
		spans = append(spans, span)
	}

	return &tracepb.ResourceSpans{
		Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
			stringKV("service.name", "genaisketch-fleet-soak"),
			stringKV("tenant.id", fmt.Sprintf("tenant-%04d", tenantIndex)),
			stringKV("team.id", fmt.Sprintf("team-%02d", tenantIndex%50)),
			stringKV("enduser.id", fmt.Sprintf("user-%04d-%08d", tenantIndex, treeIndex%100_000)),
		}},
		ScopeSpans: []*tracepb.ScopeSpans{{Spans: spans}},
	}
}

type fleetSpanSpec struct {
	name      string
	operation string
	kind      tracepb.Span_SpanKind
	llmIndex  int
}

var fleetSpanSpecs = [...]fleetSpanSpec{
	{name: "agent.run", operation: "invoke_agent", kind: tracepb.Span_SPAN_KIND_INTERNAL, llmIndex: -1},
	{name: "subagent.run", operation: "invoke_agent", kind: tracepb.Span_SPAN_KIND_INTERNAL, llmIndex: -1},
	{name: "llm.chat", operation: "chat", kind: tracepb.Span_SPAN_KIND_CLIENT, llmIndex: 0},
	{name: "tool.execute", operation: "execute_tool", kind: tracepb.Span_SPAN_KIND_INTERNAL, llmIndex: -1},
	{name: "mcp.tools.call", operation: "mcp", kind: tracepb.Span_SPAN_KIND_CLIENT, llmIndex: -1},
	{name: "retrieval.search", operation: "retrieval", kind: tracepb.Span_SPAN_KIND_CLIENT, llmIndex: -1},
	{name: "llm.embeddings", operation: "embeddings", kind: tracepb.Span_SPAN_KIND_CLIENT, llmIndex: 1},
	{name: "tool.execute", operation: "execute_tool", kind: tracepb.Span_SPAN_KIND_INTERNAL, llmIndex: -1},
	{name: "llm.generate", operation: "generate_content", kind: tracepb.Span_SPAN_KIND_CLIENT, llmIndex: 2},
	{name: "workflow.step", operation: "workflow", kind: tracepb.Span_SPAN_KIND_INTERNAL, llmIndex: -1},
}
