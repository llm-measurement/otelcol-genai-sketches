// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package genaisketchconnector

import "maps"

// Every batch is detached so an internal failure cannot leave an applied prefix
// for a retry to count again. Only touched slices and the current window are
// copied; retained history remains immutable.
func (s *collectorState) modelUsageBatch() *collectorState {
	c := *s
	c.slices = maps.Clone(s.slices)
	c.overflows = maps.Clone(s.overflows)
	c.batchOwned = make(map[*sliceState]bool)
	return &c
}

func (s *collectorState) ownModelUsageSlice(slice *sliceState, start int64) *sliceState {
	if s.batchOwned == nil || s.batchOwned[slice] {
		return slice
	}
	c := *slice
	c.windows = maps.Clone(slice.windows)
	// A suppressed summary is immutable until expiry. Do not repeatedly copy
	// its potentially full model map or sketches on every later batch.
	if w := c.windows[start]; w != nil && !(slice == s.summary && w.summarySuppressed()) {
		copy := cloneSummaryWindow(w)
		copy.modelUsage = maps.Clone(w.modelUsage)
		c.windows[start] = copy
	}
	if slice == s.summary {
		s.summary = &c
	} else if slice.label.overflow {
		if s.overflows[slice.label.name] == slice {
			s.overflows[slice.label.name] = &c
		}
	} else if s.slices[slice.label.sortKey] == slice {
		s.slices[slice.label.sortKey] = &c
	}
	s.batchOwned[&c] = true
	return &c
}
