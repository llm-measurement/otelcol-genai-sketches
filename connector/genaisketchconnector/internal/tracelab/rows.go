// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

// Package tracelab reads only the normalized v0.0.2 release, not local agent logs.
package tracelab

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/llm-measurement/otelcol-genai-sketches/connector/genaisketchconnector/internal/normalization"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

const SHA256 = "11ce51ec0a25e3d1d95b025bca2f7d1647e47571eb7cc968acd5fc64d4b4fb65"
const MappingVersion = "tracelab-v0.0.2-model-event-v1"

type Row struct {
	Provider   string    `json:"provider"`
	Model      string    `json:"model"`
	User       string    `json:"user"`
	Session    string    `json:"session_id"`
	Key        string    `json:"trace_key"`
	Input      *int64    `json:"input_tokens_total"`
	Output     *int64    `json:"output_tokens"`
	Cached     *int64    `json:"prefix_tokens"`
	New        *int64    `json:"newly_append_tokens"`
	Uncached   *int64    `json:"claude_uncached_input_tokens"`
	CacheWrite *int64    `json:"claude_cache_creation_input_tokens"`
	CacheRead  *int64    `json:"claude_cache_read_input_tokens"`
	Reasoning  *int64    `json:"reasoning_output_tokens"`
	Events     []Event   `json:"timing_events"`
	At         time.Time `json:"-"`
}

type Event struct {
	Type      string `json:"event_type"`
	Timestamp string `json:"timestamp"`
}

// Prepare verifies normalization without repairing inconsistent subset values.
// The connector, not this importer, decides whether a subset contributes to totals.
func (r *Row) Prepare() error {
	if (r.Provider != "claude" && r.Provider != "codex") || r.User == "" || r.Session == "" || r.Key == "" || r.Model == "" {
		return errors.New("missing identity or unsupported source")
	}
	for _, s := range []string{r.User, r.Session, r.Key, r.Model} {
		if len(s) > 1024 {
			return errors.New("source identifier too long")
		}
	}
	for _, value := range []*int64{r.Input, r.Output, r.Cached, r.New, r.Uncached, r.CacheWrite, r.CacheRead, r.Reasoning} {
		if value != nil && *value < 0 {
			return errors.New("negative source count")
		}
	}
	if r.Input != nil && r.Cached != nil && r.New != nil && (*r.Cached > *r.Input || *r.New != *r.Input-*r.Cached) {
		return errors.New("inconsistent normalized input split")
	}
	if r.Provider == "claude" && r.Uncached != nil && r.CacheWrite != nil && r.CacheRead != nil {
		total, err := normalization.ClaudeInput(*r.Uncached, *r.CacheWrite, *r.CacheRead)
		if err != nil {
			return err
		}
		if r.Input == nil || *r.Input != total || r.Cached == nil || *r.Cached != *r.CacheRead {
			return errors.New("inconsistent Claude normalized input")
		}
	}
	r.At = time.Time{}
	for _, event := range r.Events {
		switch event.Type {
		case "text", "reasoning", "tool_call", "usage_report":
			at, err := time.Parse(time.RFC3339Nano, event.Timestamp)
			if err != nil || at.Year() < 2020 || at.Year() > 2100 {
				return errors.New("invalid model event timestamp")
			}
			if at.After(r.At) {
				r.At = at.UTC()
			}
		}
	}
	if r.At.IsZero() {
		return errors.New("no model event timestamp")
	}
	r.Events = nil
	return nil
}

// Read sorts compact rows by model-event timestamp, then source identity. It
// discards content, paths, project names and all tool payloads during decoding.
func Read(input io.Reader) ([]Row, error) {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	rows := make([]Row, 0)
	seen := map[[32]byte]bool{}
	for line := 1; scanner.Scan(); line++ {
		if line > 1_000_000 {
			return nil, errors.New("release exceeds one million rows")
		}
		var row Row
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return nil, fmt.Errorf("invalid JSON at row %d", line)
		}
		if err := row.Prepare(); err != nil {
			return nil, fmt.Errorf("row %d: %w", line, err)
		}
		key := sha256.Sum256([]byte(row.User + "\x00" + row.Key))
		if seen[key] {
			return nil, fmt.Errorf("duplicate source identity at row %d", line)
		}
		seen[key] = true
		rows = append(rows, row)
	}
	if err := scanner.Err(); err != nil {
		return nil, errors.New("cannot read release rows")
	}
	sort.Slice(rows, func(i, j int) bool {
		if !rows[i].At.Equal(rows[j].At) {
			return rows[i].At.Before(rows[j].At)
		}
		if rows[i].User != rows[j].User {
			return rows[i].User < rows[j].User
		}
		return rows[i].Key < rows[j].Key
	})
	return rows, nil
}

func (r Row) Traces() ptrace.Traces {
	traces := ptrace.NewTraces()
	resource := traces.ResourceSpans().AppendEmpty()
	resource.Resource().Attributes().PutStr("service.name", "tracelab")
	span := resource.ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	span.SetName("chat")
	span.SetKind(ptrace.SpanKindClient)
	span.SetStartTimestamp(pcommon.NewTimestampFromTime(r.At))
	span.SetEndTimestamp(pcommon.NewTimestampFromTime(r.At))
	attrs := span.Attributes()
	attrs.PutStr("gen_ai.operation.name", "chat")
	attrs.PutStr("gen_ai.request.model", r.Model)
	attrs.PutStr("enduser.id", r.User)
	// JSON tuple avoids ambiguous cross-user session concatenation.
	session, _ := json.Marshal([]string{r.User, r.Session})
	attrs.PutStr("gen_ai.conversation.id", string(session))
	for key, value := range map[string]*int64{
		"gen_ai.usage.input_tokens": r.Input, "gen_ai.usage.output_tokens": r.Output,
		"gen_ai.usage.cache_read.input_tokens":  r.Cached,
		"gen_ai.usage.cache_write.input_tokens": r.CacheWrite,
		"gen_ai.usage.reasoning.output_tokens":  r.Reasoning,
	} {
		if value != nil {
			attrs.PutInt(key, *value)
		}
	}
	return traces
}
