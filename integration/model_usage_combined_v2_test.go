//go:build integration

// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package integration

import (
	"strings"
	"testing"
)

const litellmCombinedV2Identity = "litellm-1.104.0-v2"

func TestLiteLLMCombinedV2ModelUsageRealOTLP(t *testing.T) {
	model := litellmCombinedConfig()
	model["identity"] = litellmCombinedV2Identity
	model["models"].(map[string]string)["anthropic/PRIVATE_AUTO"] = "model-a"
	model["models"].(map[string]string)["anthropic/PRIVATE_RECOVERY"] = "model-b"
	batches := litellmCombinedCases()
	for _, batch := range batches {
		for i := range batch {
			if strings.HasSuffix(batch[i].name, "/messages-still-unsupported") {
				batch[i].name = strings.TrimSuffix(batch[i].name, "messages-still-unsupported") + "messages-supported"
				batch[i].model = "model-a"
			}
		}
	}
	// Interleave both Messages modes with Chat/Responses in one producer and
	// contract; retain each standalone case's identity and usage expectations.
	next := map[string]int{}
	for _, tc := range litellmMessagesCases() {
		mode := strings.SplitN(tc.name, "/", 2)[0]
		i := next[mode]
		next[mode]++
		if i == len(batches) {
			batches = append(batches, nil)
		}
		tc.name = "messages-" + tc.name
		batches[i] = append(batches[i], tc)
	}
	spanCount := 0
	for _, batch := range batches {
		spanCount += len(batch)
	}
	t.Logf("mixed v2 OTLP: %d spans in %d batches", spanCount, len(batches))
	testLiteLLMCombinedModelUsageRealOTLP(t, model, batches, []string{
		"", "agentgateway-1.6.0-chat-v1", "envoy-ai-gateway-1.1.0-chat-v1",
		"litellm-1.104.0-chat-v1", litellmChatStreamIdentity, litellmResponsesIdentity,
		litellmMessagesIdentity, litellmCombinedIdentity,
	})
}

func TestLiteLLMCombinedV2ModelUsageConfigValidation(t *testing.T) {
	testLiteLLMCombinedModelUsageConfigValidation(t, litellmCombinedV2Identity)
}
