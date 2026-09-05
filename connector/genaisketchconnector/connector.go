// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay Erramilli and Codex
package genaisketchconnector

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	sketchhash "github.com/llm-measurement/llm-sketchkit/go/sketchkit/hash"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap"
)

const (
	scopeName = "github.com/llm-measurement/otelcol-genai-sketches/connector/genaisketchconnector"

	requestsMetricName               = "gen_ai_sketch_requests_total"
	agentRunsMetricName              = "gen_ai_sketch_agent_runs_total"
	inputTokensMetricName            = "gen_ai_sketch_input_tokens_total"
	outputTokensMetricName           = "gen_ai_sketch_output_tokens_total"
	totalTokensMetricName            = "gen_ai_sketch_total_tokens_total"
	cacheReadInputTokensMetricName   = "gen_ai_sketch_cache_read_input_tokens_total"
	cacheWriteInputTokensMetricName  = "gen_ai_sketch_cache_write_input_tokens_total"
	reasoningOutputTokensMetricName  = "gen_ai_sketch_reasoning_output_tokens_total"
	missingTokenUsageMetricName      = "gen_ai_sketch_missing_token_usage_total"
	tokenFieldObservationsMetricName = "gen_ai_sketch_token_field_observations_total"
	dedupSuppressedMetricName        = "gen_ai_sketch_dedup_suppressed_total"
	dedupKeyMissingMetricName        = "gen_ai_sketch_dedup_key_missing_total"
	activeSlicesMetricName           = "gen_ai_sketch_active_slices"
	distinctUsersMetricName          = "gen_ai_sketch_distinct_users"
	distinctPromptsMetricName        = "gen_ai_sketch_distinct_prompt_signatures"
	distinctDocsMetricName           = "gen_ai_sketch_distinct_retrieval_docs"
	distinctMCPSessionsMetricName    = "gen_ai_sketch_distinct_mcp_sessions"
	distinctMCPMethodsMetricName     = "gen_ai_sketch_distinct_mcp_methods"
	distinctMCPResourcesMetricName   = "gen_ai_sketch_distinct_mcp_resources"

	debugLogInterval = 5 * time.Second
)

type tracesConnector struct {
	cfg          *Config
	next         consumer.Metrics
	logger       *zap.Logger
	clock        clock
	mu           sync.Mutex
	state        *collectorState
	debugCancel  context.CancelFunc
	debugDone    chan struct{}
	lastDebugLog time.Time
	exporter     *summaryExporter
}

func newTracesConnector(set component.TelemetrySettings, cfg *Config, next consumer.Metrics) *tracesConnector {
	return &tracesConnector{
		cfg:    cfg,
		next:   next,
		logger: set.Logger,
		clock:  systemClock{},
	}
}

func (c *tracesConnector) Start(context.Context, component.Host) error {
	secret, err := sketchhash.SecretFromEnv(c.cfg.Hashing.SecretEnv)
	if err != nil {
		return err
	}

	state, err := newCollectorState(c.cfg, secret, c.clock)
	if err != nil {
		return err
	}
	exporter, err := newSummaryExporter(c.cfg, c.clock.Now())
	if err != nil {
		return err
	}

	var debugCtx context.Context
	var debugDone chan struct{}
	c.mu.Lock()
	c.state = state
	c.exporter = exporter
	if c.cfg.TopK > 0 || exporter != nil {
		debugCtx, c.debugCancel = context.WithCancel(context.Background())
		c.debugDone = make(chan struct{})
		debugDone = c.debugDone
	}
	c.mu.Unlock()
	if debugCtx != nil {
		go c.debugLogLoop(debugCtx, debugDone)
	}

	c.logger.Info("started genaisketch connector")
	return nil
}

func (c *tracesConnector) Shutdown(ctx context.Context) error {
	c.mu.Lock()
	cancel := c.debugCancel
	done := c.debugDone
	c.mu.Unlock()

	if cancel != nil {
		cancel()
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	err := c.emitSummary(true)
	c.mu.Lock()
	c.debugCancel = nil
	c.debugDone = nil
	if c.exporter != nil {
		err = errors.Join(err, c.exporter.root.Close())
		c.exporter = nil
	}
	c.state = nil
	c.mu.Unlock()
	c.logger.Info("stopped genaisketch connector")
	return err
}

func (c *tracesConnector) Capabilities() consumer.Capabilities {
	return consumer.Capabilities{MutatesData: false}
}

func (c *tracesConnector) ConsumeTraces(ctx context.Context, traces ptrace.Traces) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.state == nil {
		return errors.New("genaisketch connector has not been started")
	}

	metrics, ok, err := c.state.ConsumeTraces(ctx, traces)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	now := c.clock.Now()
	if c.shouldLogTopKLocked(now) {
		c.emitTopKSnapshotLocked(now)
	}
	return c.next.ConsumeMetrics(ctx, metrics)
}

func (c *tracesConnector) debugLogLoop(ctx context.Context, done chan struct{}) {
	defer close(done)

	interval := debugLogInterval
	if c.exporter != nil {
		interval = min(interval, c.exporter.cfg.Interval)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.mu.Lock()
			if c.state != nil {
				now := c.clock.Now()
				if c.shouldLogTopKLocked(now) {
					c.emitTopKSnapshotLocked(now)
				}
			}
			c.mu.Unlock()
			if err := c.emitSummary(false); err != nil {
				c.logger.Error("genaisketch summary export failed; summary files may be stale", zap.Error(err))
			}
		}
	}
}

func (c *tracesConnector) emitSummary(force bool) error {
	c.mu.Lock()
	e := c.exporter
	if e == nil || c.state == nil {
		c.mu.Unlock()
		return nil
	}
	now := c.clock.Now()
	if !force && now.Sub(e.lastExport) < e.cfg.Interval {
		c.mu.Unlock()
		return nil
	}
	documents, err := c.state.summaryDocuments(e, now)
	cutoff := c.state.cutoffWindowStart(c.state.windowStart(now))
	c.mu.Unlock()
	if err != nil {
		return err
	}
	if err := e.write(documents, cutoff); err != nil {
		return err
	}
	e.lastExport = now
	c.logger.Debug("genaisketch summary files written", zap.Int("windows", len(documents)))
	return nil
}

func (c *tracesConnector) shouldLogTopKLocked(now time.Time) bool {
	return c.cfg.TopK > 0 && (c.lastDebugLog.IsZero() || now.Sub(c.lastDebugLog) >= debugLogInterval)
}

func (c *tracesConnector) emitTopKSnapshotLocked(now time.Time) {
	snapshot, err := c.state.TopKSnapshot(now)
	if err != nil {
		c.logger.Warn("failed to build genaisketch topk snapshot", zap.Error(err))
		return
	}
	if snapshot.ItemCount() == 0 {
		return
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		c.logger.Warn("failed to encode genaisketch topk snapshot", zap.Error(err))
		return
	}
	c.lastDebugLog = now
	c.logger.Info("genaisketch topk snapshot", zap.ByteString("payload_json", payload))
}
