//go:build integration

// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay Erramilli and Codex

package integration

import (
	"strings"
	"testing"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

func TestFleetGeneratorShapeAndAccounting(t *testing.T) {
	generator := newFleetGenerator()
	request, stats := generator.batch(1000, true)

	if stats.Spans != 1000 || stats.Trees != 100 || stats.LLMSpans != 300 {
		t.Fatalf("unexpected shape stats: %+v", stats)
	}
	if stats.MissingLLMSpans != 30 || stats.ActiveLLMSpans != 300 || stats.OverflowLLMSpans != 0 {
		t.Fatalf("unexpected accounting stats: %+v", stats)
	}
	if stats.MinDepth != fleetMinDepth || stats.MaxDepth != fleetMaxDepth {
		t.Fatalf("depth range = %d..%d, want %d..%d", stats.MinDepth, stats.MaxDepth, fleetMinDepth, fleetMaxDepth)
	}
	if len(request.ResourceSpans) != 100 {
		t.Fatalf("resource trees = %d, want 100", len(request.ResourceSpans))
	}

	for _, tree := range request.ResourceSpans {
		if len(tree.ScopeSpans) != 1 || len(tree.ScopeSpans[0].Spans) != fleetTreeSpans {
			t.Fatalf("tree does not contain %d spans", fleetTreeSpans)
		}
		if !hasAttributePrefix(tree.Resource.Attributes, "tenant.id", "tenant-") ||
			!hasAttributePrefix(tree.Resource.Attributes, "enduser.id", "user-") {
			t.Fatal("tree identity is not carried on the resource")
		}
		for _, span := range tree.ScopeSpans[0].Spans {
			if hasAttributePrefix(span.Attributes, "tenant.id", "") || hasAttributePrefix(span.Attributes, "enduser.id", "") {
				t.Fatal("identity leaked from resource placement onto a span")
			}
		}
	}
}

func TestFleetGeneratorPlantsOnlyDeclaredOverflowTenants(t *testing.T) {
	generator := newFleetGenerator()
	_, warmup := generator.batch(1000, true)
	request, stats := generator.batch(1000, false)

	if warmup.OverflowLLMSpans != 0 || stats.OverflowLLMSpans != 15 {
		t.Fatalf("overflow LLM accounting: warmup=%d normal=%d, want 0 and 15", warmup.OverflowLLMSpans, stats.OverflowLLMSpans)
	}
	for _, tree := range request.ResourceSpans {
		tenant := attributeString(tree.Resource.Attributes, "tenant.id")
		if tenant < "tenant-0000" || tenant > "tenant-1199" {
			t.Fatalf("unexpected overflow tenant %q", tenant)
		}
	}
}

func hasAttributePrefix(attrs []*commonpb.KeyValue, key string, prefix string) bool {
	value := attributeString(attrs, key)
	return value != "" && strings.HasPrefix(value, prefix)
}

func attributeString(attrs []*commonpb.KeyValue, key string) string {
	for _, attr := range attrs {
		if attr.Key == key {
			return attr.Value.GetStringValue()
		}
	}
	return ""
}
