// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay Erramilli and Codex
package genaisketchconnector

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"time"

	sketchfi "github.com/llm-measurement/llm-sketchkit/go/sketchkit/frequentitems"
	sketchhash "github.com/llm-measurement/llm-sketchkit/go/sketchkit/hash"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
)

const maxSummaryFiles = 512
const maxSummaryDiskBytes = 64 << 20

var summaryFileName = regexp.MustCompile(`^([0-9]{20})-([a-f0-9]{32})\.json$`)
var summaryTemporaryName = regexp.MustCompile(`^(\.[0-9]{20}-[a-f0-9]{32}\.json-[0-9]{1,20}\.tmp|\.write-check-[a-f0-9]{32})$`)

// SummaryExportConfig enables an opt-in, private, bounded file export. Identity
// scope is configured by the operator, never taken from untrusted span labels.
type SummaryExportConfig struct {
	Directory  string        `mapstructure:"directory"`
	ProducerID string        `mapstructure:"producer_id"`
	ScopeID    string        `mapstructure:"scope_id"`
	KeyID      string        `mapstructure:"key_id"`
	Interval   time.Duration `mapstructure:"interval"`
}

func (cfg SummaryExportConfig) Validate(window time.Duration) error {
	if cfg.Directory == "" {
		if cfg.ProducerID != "" || cfg.ScopeID != "" || cfg.KeyID != "" {
			return errors.New("summary_export.directory is required when export identity is set")
		}
		return nil
	}
	if cfg.Interval < time.Second || cfg.Interval > time.Minute || cfg.Interval > window {
		return errors.New("summary_export.interval must be between 1s and 1m and no greater than window_duration")
	}
	return (summary.Envelope{Version: 1, Sequence: 1, ProducerID: cfg.ProducerID, Epoch: "validation", ScopeID: cfg.ScopeID, KeyID: cfg.KeyID, AccountingID: "validation", WindowDuration: 1, Counters: map[string]uint64{}, Sketches: map[string]summary.Payload{}}).Validate()
}

type summaryExporter struct {
	root         *os.Root
	cfg          SummaryExportConfig
	accountingID string
	epoch        string
	started      time.Time
	lastExport   time.Time
	sequence     uint64
}

func newSummaryExporter(cfg *Config, now time.Time) (*summaryExporter, error) {
	if cfg.SummaryExport.Directory == "" {
		return nil, nil
	}
	info, err := os.Lstat(cfg.SummaryExport.Directory)
	if err != nil {
		return nil, errors.New("summary_export.directory must already exist")
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("summary_export.directory must be a private directory (0700), not a symlink")
	}
	root, err := os.OpenRoot(cfg.SummaryExport.Directory)
	if err != nil {
		return nil, err
	}
	epoch := make([]byte, 16)
	if _, err := rand.Read(epoch); err != nil {
		_ = root.Close()
		return nil, err
	}
	probeName := ".write-check-" + hex.EncodeToString(epoch)
	probe, err := root.OpenFile(probeName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		_ = root.Close()
		return nil, errors.New("summary_export.directory is not writable")
	}
	if err := errors.Join(probe.Close(), root.Remove(probeName)); err != nil {
		_ = root.Close()
		return nil, err
	}
	// Fingerprint extraction rules, not locations, keys, slice budgets, or transport.
	rules := struct {
		Operations []string
		Fields     map[string]FieldConfig
		Weights    WeightsConfig
		Dedup      DedupConfig
		MCP        MCPConfig
		TopK       bool
	}{append([]string(nil), cfg.OperationFilter.LLMOperations...), cfg.Fields, cfg.Weights, cfg.Dedup, cfg.MCP, cfg.TopK > 0}
	sort.Strings(rules.Operations)
	encoded, err := json.Marshal(rules)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	digest := sha256.Sum256(encoded)
	return &summaryExporter{root: root, cfg: cfg.SummaryExport, accountingID: "genai-accounting-v1:" + hex.EncodeToString(digest[:]), epoch: hex.EncodeToString(epoch), started: now}, nil
}

func (s *collectorState) summaryDocuments(e *summaryExporter, now time.Time) ([]summary.Envelope, error) {
	if s.summary == nil {
		return nil, errors.New("summary state is disabled")
	}
	if now.Before(e.started) || now.Before(e.lastExport) {
		return nil, errors.New("summary clock moved backwards")
	}
	current := s.windowStart(now)
	start := max(s.cutoffWindowStart(current), s.windowStart(e.started))
	// Rotate even when no spans arrive, so a running idle producer emits evidence.
	for window := range s.summary.windows {
		if window < start {
			delete(s.summary.windows, window)
		}
	}
	e.sequence++
	documents := make([]summary.Envelope, 0, s.cfg.retentionWindows)
	for windowStart := start; windowStart <= current; windowStart += s.cfg.windowDuration.Nanoseconds() {
		window, err := s.summary.window(windowStart, s)
		if err != nil {
			return nil, err
		}
		sketches, err := s.summaryPayloads(window)
		if err != nil {
			return nil, err
		}
		doc := summary.Envelope{Version: 1, Sequence: e.sequence, ProducerID: e.cfg.ProducerID, Epoch: e.epoch, ScopeID: e.cfg.ScopeID, KeyID: e.cfg.KeyID, AccountingID: e.accountingID, WindowStart: windowStart, WindowDuration: s.cfg.windowDuration.Nanoseconds(), ObservedStart: max(windowStart, e.started.UnixNano()), ObservedEnd: min(windowStart+s.cfg.windowDuration.Nanoseconds(), now.UnixNano()), EmittedAt: now.UnixNano(), Counters: window.counters.export(), Sketches: sketches}
		documents = append(documents, doc)
	}
	return documents, nil
}

func (s *collectorState) summaryPayloads(w *windowState) (map[string]summary.Payload, error) {
	if s.cfg.mcpEnabled {
		if err := s.ensureMCPFields(w, preparedUpdate{mcpSessionHash: optionalHash{ok: true}, mcpMethodHash: optionalHash{ok: true}, mcpResourceHash: optionalHash{ok: true}}); err != nil {
			return nil, err
		}
	}
	if s.cfg.toolErrors && w.topToolErrors == nil {
		var err error
		w.topToolErrors, err = sketchfi.New(s.cfg.frequentProfile, sketchhash.ToolErrorV1, sketchhash.HMACSHA25664)
		if err != nil {
			return nil, err
		}
	}
	type binarySketch interface{ MarshalBinary() ([]byte, error) }
	type sketchEntry struct {
		name, kind string
		sketch     binarySketch
	}
	items := []sketchEntry{
		{"distinct_users", "hllpp", w.distinctUsers},
		{"distinct_prompts", "hllpp", w.distinctPrompts},
		{"distinct_docs", "hllpp", w.distinctDocs},
	}
	if w.topPrompts != nil {
		items = append(items, sketchEntry{"top_prompts", "frequent_items", w.topPrompts})
	}
	if s.cfg.mcpEnabled {
		items = append(items, sketchEntry{"distinct_mcp_sessions", "hllpp", w.distinctMCPSessions}, sketchEntry{"distinct_mcp_methods", "hllpp", w.distinctMCPMethods}, sketchEntry{"distinct_mcp_resources", "hllpp", w.distinctMCPResources})
	}
	if w.topToolErrors != nil {
		items = append(items, sketchEntry{"top_tool_errors", "frequent_items", w.topToolErrors})
	}
	result := make(map[string]summary.Payload, len(items))
	for _, item := range items {
		data, err := item.sketch.MarshalBinary()
		if err != nil {
			return nil, err
		}
		result[item.name] = summary.Payload{Kind: item.kind, Data: data}
	}
	return result, nil
}

func (c *accountingCounters) export() map[string]uint64 {
	result := map[string]uint64{"requests": c.requests, "agent_runs": c.agentRuns, "input_tokens": c.inputTokens, "output_tokens": c.outputTokens, "cache_read_input_tokens": c.cacheReadInputTokens, "cache_write_input_tokens": c.cacheWriteInputTokens, "reasoning_output_tokens": c.reasoningOutputTokens, "missing_token_usage": c.missingTokens, "dedup_suppressed": c.dedupSuppressed, "dedup_key_missing": c.dedupKeyMissing}
	for field := tokenField(0); field < tokenFieldCount; field++ {
		for state := tokenObservationState(0); state < tokenStateCount; state++ {
			result["token_observations."+tokenFieldNames[field]+"."+tokenObservationStateNames[state]] = c.tokenObservations[field][state]
		}
	}
	return result
}

func (e *summaryExporter) write(documents []summary.Envelope, cutoff int64) error {
	directory, err := e.root.Open(".")
	if err != nil {
		return err
	}
	entries, err := directory.ReadDir(1025)
	closeErr := directory.Close()
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if len(entries) > 1024 {
		return errors.New("summary directory contains too many entries")
	}
	sizes := map[string]int64{}
	total := int64(0)
	for _, entry := range entries {
		// A directory has one writer; recognized temporary files left here are
		// from interrupted writes. Reclaim them before applying the disk budget.
		if summaryTemporaryName.MatchString(entry.Name()) {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return errors.New("summary directory contains a nonregular temporary file")
			}
			if err := e.root.Remove(entry.Name()); err != nil {
				return err
			}
			continue
		}
		match := summaryFileName.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("summary directory contains a nonregular export file")
		}
		start, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil {
			return err
		}
		if start < cutoff {
			if err := e.root.Remove(entry.Name()); err != nil {
				return err
			}
			continue
		}
		sizes[entry.Name()] = info.Size()
		total += info.Size()
	}
	for _, doc := range documents {
		data, err := doc.MarshalBinary()
		if err != nil {
			return err
		}
		name := fmt.Sprintf("%020d-%s.json", doc.WindowStart, doc.Epoch)
		previous, exists := sizes[name]
		if (!exists && len(sizes) >= maxSummaryFiles) || total-previous+int64(len(data)) > maxSummaryDiskBytes {
			return errors.New("summary disk budget exceeded; export incomplete")
		}
		temporary := fmt.Sprintf(".%s-%d.tmp", name, doc.Sequence)
		if err := e.writeAtomic(temporary, name, data); err != nil {
			return err
		}
		total += int64(len(data)) - previous
		sizes[name] = int64(len(data))
	}
	return nil
}

func (e *summaryExporter) writeAtomic(temporary, name string, data []byte) error {
	file, err := e.root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = e.root.Remove(temporary) }()
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	return e.root.Rename(temporary, name)
}
