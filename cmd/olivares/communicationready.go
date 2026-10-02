// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"sync"
	"time"
)

// communicationReadyTTL bounds how often server-info samples the communication
// plane's readiness: every console page reads server-info, and a readiness pass
// samples the store and pump witnesses.
const communicationReadyTTL = 15 * time.Second

// communicationReady answers server-info's communication_ready: whether the
// session communication plane is effective on this node, sampled at most once per
// ttl. An evaluation error is "not ready".
type communicationReady struct {
	eval  func(context.Context) (bool, error)
	ttl   time.Duration
	now   func() time.Time
	mu    sync.Mutex
	at    time.Time
	ready bool
	valid bool
}

func newCommunicationReady(eval func(context.Context) (bool, error)) *communicationReady {
	return &communicationReady{eval: eval, ttl: communicationReadyTTL, now: time.Now}
}

// Ready reports the cached answer, sampling again when it is older than the ttl.
func (c *communicationReady) Ready(ctx context.Context) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.valid && c.now().Sub(c.at) < c.ttl {
		return c.ready
	}
	ready, err := c.eval(ctx)
	c.ready, c.at, c.valid = err == nil && ready, c.now(), true
	return c.ready
}
