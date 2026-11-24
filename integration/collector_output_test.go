//go:build integration

// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package integration

import (
	"bytes"
	"strings"
	"sync"
	"testing"
)

// Collector logs are written by exec's copy goroutine while tests poll them.
type collectorOutput struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *collectorOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}

func (b *collectorOutput) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

func TestCollectorOutputConcurrentSnapshots(t *testing.T) {
	var output collectorOutput
	var workers sync.WaitGroup
	const count = 1000
	for range 4 {
		workers.Go(func() {
			for range count {
				if _, err := output.Write([]byte("entry\n")); err != nil {
					t.Error(err)
				}
			}
		})
		workers.Go(func() {
			for range count {
				if snapshot := output.String(); strings.Count(snapshot, "entry\n")*6 != len(snapshot) {
					t.Error("snapshot contains an incomplete write")
				}
			}
		})
	}
	workers.Wait()
	if got := strings.Count(output.String(), "entry\n"); got != 4*count {
		t.Fatalf("entries=%d want %d", got, 4*count)
	}
}
