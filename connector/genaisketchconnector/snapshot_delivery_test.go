// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package genaisketchconnector

import (
	"bytes"
	"encoding/json"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestEmptyTruncatedSnapshotIsLogged(t *testing.T) {
	for _, kind := range []string{"empty", "ordinary", "saturated"} {
		t.Run(kind, func(t *testing.T) {
			cfg := testConfig(func(c *Config) {
				c.Slices = []SliceConfig{{Name: "by_model", Keys: []string{"gen_ai.request.model"}}}
			})
			state := newTestState(t, cfg)
			if kind != "empty" {
				mustConsume(t, state, testSpan{Model: "model", Prompt: "private-prompt-sentinel", InputTokens: ptr(5), OutputTokens: ptr(1)})
			}
			now := state.clock.Now()
			if kind == "saturated" {
				for _, slice := range state.metricSlices() {
					slice.windows[state.windowStart(now)].topKeySaturated[0] = true
				}
			}
			core, logs := observer.New(zap.InfoLevel)
			conn := &tracesConnector{cfg: cfg, state: state, logger: zap.New(core)}
			conn.emitTopKSnapshotLocked(now)
			if kind == "empty" {
				if logs.Len() != 0 || !conn.lastDebugLog.IsZero() {
					t.Fatal("healthy empty snapshot changed logging")
				}
				return
			}
			if logs.Len() != 1 || !conn.lastDebugLog.Equal(now) {
				t.Fatal("nonempty or truncated snapshot was not logged")
			}
			entry := logs.All()[0]
			payload, ok := entry.ContextMap()["payload_json"].(string)
			if !ok || entry.Message != "genaisketch topk snapshot" {
				t.Fatal("unexpected snapshot log structure")
			}
			snapshot, err := state.TopKSnapshot(now)
			if err != nil {
				t.Fatal(err)
			}
			want, err := json.Marshal(snapshot)
			if err != nil || !bytes.Equal([]byte(payload), want) || bytes.Contains(want, []byte("private-prompt-sentinel")) {
				t.Fatal("logged snapshot changed bytes or exposed prompt")
			}
			if kind == "saturated" && (!snapshot.Truncated || snapshot.ItemCount() != 0) {
				t.Fatal("saturation marker lost or invalid ranking retained")
			}
		})
	}
}
