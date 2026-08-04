// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay Erramilli and Codex
package genaisketchconnector

import "time"

type clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time {
	return time.Now()
}

type fixedClock struct {
	now time.Time
}

func (c *fixedClock) Now() time.Time {
	return c.now
}

func (c *fixedClock) Set(now time.Time) {
	c.now = now
}
