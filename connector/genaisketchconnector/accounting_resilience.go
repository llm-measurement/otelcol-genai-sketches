// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package genaisketchconnector

import (
	"errors"
	"maps"
	"time"

	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/modelusage"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"
)

// This is a per-observation plausibility bound, not a cumulative budget. A
// larger value is unavailable usage, never a reported zero or a dropped request.
const maxPlausibleTokenValue = 1_000_000_000

const rejectedSpansMetricName = "gen_ai_sketch_rejected_spans_total"
const accountingSaturatedMetricName = "gen_ai_sketch_accounting_saturated"

type saturationReason uint8

const (
	saturationCounter saturationReason = iota
	saturationTotalTokens
	saturationTopK
	saturationToolError
	saturationDedup
	saturationModelUsage
	saturationModelCount
	saturationSummaryBytes
	saturationReasonCount
)

var saturationReasonNames = [saturationReasonCount]string{
	"counter_limit", "total_tokens_limit", "topk_weight_limit", "tool_error_weight_limit",
	"dedup_count_limit", "model_usage_overflow", "model_count_limit", "summary_byte_limit",
}

var errModelUsageCountBudget = errors.New("summary model_usage model count exceeds slice budget")
var errModelUsageByteBudget = errors.New("summary model_usage exceeds envelope byte budget")

func modelUsageCapacityReason(err error) (saturationReason, bool) {
	switch {
	case errors.Is(err, errModelUsageCountBudget):
		return saturationModelCount, true
	case errors.Is(err, errModelUsageByteBudget):
		return saturationSummaryBytes, true
	}
	var modelErr *modelusage.Error
	if errors.As(err, &modelErr) && modelErr.Reason == "overflow" {
		return saturationModelUsage, true
	}
	return 0, false
}

func (c *accountingCounters) add(value *uint64, delta uint64) {
	if *value > uint64(maxInt64Value) || delta > uint64(maxInt64Value)-*value {
		*value = uint64(maxInt64Value)
		c.saturated = true
		return
	}
	*value += delta
}

func (w *windowState) summarySuppressed() bool {
	for _, saturated := range w.saturation {
		if saturated {
			return true
		}
	}
	return false
}

func (s *collectorState) appendSaturationMetrics(scope pmetric.ScopeMetrics, now pcommon.Timestamp, current int64) {
	appendState := func(surface string, reason saturationReason, count int, labels map[string]string) {
		if count == 0 {
			return
		}
		labels = maps.Clone(labels)
		if labels == nil {
			labels = make(map[string]string, 2)
		}
		labels["surface"], labels["reason"] = surface, saturationReasonNames[reason]
		appendGauge(scope, accountingSaturatedMetricName, "Saturated accounting surfaces; affected summaries and top-k streams are unavailable.", "{surface}", float64(count), labels, now)
	}
	if s.rejections.saturated {
		appendState("rejections", saturationCounter, 1, nil)
	}
	for _, slice := range s.metricSlices() {
		labels := slice.metricLabels()
		if slice.saturated {
			appendState("slice_counters", saturationCounter, 1, labels)
		}
		if slice.totalSaturated {
			appendState("slice_counters", saturationTotalTokens, 1, labels)
		}
		if w := slice.windows[current]; w != nil {
			for reason, saturated := range w.saturation {
				if saturated {
					appendState("slice_window", saturationReason(reason), 1, labels)
				}
			}
		}
	}
	if s.summary != nil {
		var counts [saturationReasonCount]int
		for start, w := range s.summary.windows {
			if start < s.cutoffWindowStart(current) {
				continue
			}
			for reason, saturated := range w.saturation {
				if saturated {
					counts[reason]++
				}
			}
		}
		for reason, count := range counts {
			appendState("summary_window", saturationReason(reason), count, nil)
		}
	}
}

type spanRejection uint8

const (
	spanRejectedUTF8 spanRejection = iota
	spanRejectedAttributeSize
	spanRejectionCount
)

var spanRejectionNames = [spanRejectionCount]string{"invalid_utf8", "attribute_too_large"}

func (r spanRejection) Error() string {
	return "span has a malformed identity attribute"
}

type spanRejections struct {
	counts    [spanRejectionCount]uint64
	start     pcommon.Timestamp
	saturated bool
}

func (r *spanRejections) record(reason spanRejection, now time.Time) {
	if r.start == 0 {
		r.start = pcommon.NewTimestampFromTime(now)
	}
	if r.counts[reason] >= uint64(maxInt64Value) {
		r.counts[reason] = uint64(maxInt64Value)
		r.saturated = true
		return
	}
	r.counts[reason]++
}

func (r spanRejections) appendMetrics(scope pmetric.ScopeMetrics, now pcommon.Timestamp) {
	for reason, count := range r.counts {
		if count != 0 {
			appendSum(scope, rejectedSpansMetricName, "Cumulative malformed spans rejected without rejecting their batches.", "{span}", count, map[string]string{"reason": spanRejectionNames[reason]}, r.start, now)
		}
	}
}
