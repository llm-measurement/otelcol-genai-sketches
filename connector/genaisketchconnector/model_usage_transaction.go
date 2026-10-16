// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package genaisketchconnector

import "maps"

// Opt-in limits may reject a later otherwise valid span. A detached batch
// prevents retries from counting an already-applied prefix. Only touched slices
// and the current window are copied; retained history remains immutable.
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
	if w := c.windows[start]; w != nil {
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
