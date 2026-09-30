// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package genaisketchconnector

import (
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
)

type pendingSliceUpdate struct {
	slice    *sliceState
	window   *windowState
	prepared preparedUpdate
	created  bool
	evicted  *sliceState
}

func (s *collectorState) applySpan(data spanData, update spanUpdate, now time.Time, start int64) error {
	// Check every destination before changing sketches, counters, or slice routing.
	var pending [maxConfiguredSliceCount + 1]pendingSliceUpdate
	n := len(s.cfg.slices)
	for i, cfg := range s.cfg.slices {
		pending[i] = s.planSlice(cfg, data, now, start, pending[:i])
	}
	if s.summary != nil {
		pending[n].slice = s.summary
		n++
	}
	for i := range pending[:n] {
		p := &pending[i]
		p.window = p.slice.windows[start]
		var err error
		if p.window == nil {
			p.window, err = p.slice.newWindow(s)
			if err != nil {
				return err
			}
		}
		p.prepared, err = s.prepareUpdate(p.window, data, update)
		if err != nil {
			return err
		}
		if err := p.slice.checkUpdate(p.window, update, p.prepared); err != nil {
			return err
		}
	}
	for _, p := range pending[:n] {
		if p.evicted != nil {
			delete(s.slices, p.evicted.label.sortKey)
		}
		if p.created {
			if p.slice.label.overflow {
				s.overflows[p.slice.label.name] = p.slice
			} else {
				s.slices[p.slice.label.sortKey] = p.slice
			}
		}
		if p.slice != s.summary {
			s.touch(p.slice, start)
		}
		for old := range p.slice.windows {
			if old < s.cutoffWindowStart(start) {
				delete(p.slice.windows, old)
			}
		}
		p.slice.windows[start] = p.window
		if p.prepared.deduped {
			p.slice.dedupSuppressed++
			if p.window.counters != nil {
				p.window.counters.dedupSuppressed++
			}
			continue
		}
		if err := s.applyErrorCheckedSketches(p.window, p.prepared); err != nil {
			return err
		}
		s.applyNoErrorSketches(p.window, p.prepared)
		p.slice.addCounters(update, p.prepared)
		if p.window.counters != nil {
			p.window.counters.addCounters(update, p.prepared)
		}
	}
	return nil
}

func (s *collectorState) planSlice(cfg SliceConfig, data spanData, now time.Time, start int64, pending []pendingSliceUpdate) pendingSliceUpdate {
	label := sliceLabelFor(cfg, data)
	existing := s.slices[label.sortKey]
	count := len(s.slices)
	for _, p := range pending {
		if p.created && !p.slice.label.overflow {
			count++
		}
		if p.evicted != nil {
			count--
			if p.evicted == existing {
				existing = nil
			}
		}
	}
	if existing != nil {
		return pendingSliceUpdate{slice: existing}
	}
	var victim *sliceState
	if count >= s.cfg.maxSlices {
	candidates:
		for _, candidate := range s.slices {
			if candidate.lastWindow >= start-s.cfg.windowDuration.Nanoseconds() {
				continue
			}
			for _, p := range pending {
				if p.slice == candidate || p.evicted == candidate {
					continue candidates
				}
			}
			if victim == nil || candidate.lastWindow < victim.lastWindow ||
				(candidate.lastWindow == victim.lastWindow && candidate.lruSequence < victim.lruSequence) ||
				(candidate.lastWindow == victim.lastWindow && candidate.lruSequence == victim.lruSequence && candidate.label.sortKey < victim.label.sortKey) {
				victim = candidate
			}
		}
		if victim == nil {
			if existing := s.overflows[cfg.Name]; existing != nil {
				return pendingSliceUpdate{slice: existing}
			}
			label = sliceLabel{name: cfg.Name, value: overflowSliceValue, overflow: true, sortKey: "overflow:" + cfg.Name}
		}
	}
	return pendingSliceUpdate{
		slice:   &sliceState{label: label, windows: make(map[int64]*windowState), startTime: pcommon.NewTimestampFromTime(now)},
		created: true,
		evicted: victim,
	}
}
