// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package serverhandover provides the zero-downtime handover primitive for
// `olivares serve` restarts and OTA upgrades: a TCP listener with
// SO_REUSEPORT so a NEW process can bind the SAME address while the OLD one drains
// its in-flight requests. During the overlap the kernel load-balances new
// connections across both listeners, so there is no window where nothing is
// accepting. This is an accept-gap guarantee, not a zero-request-loss guarantee:
// Linux's default listener teardown may reset connections still assigned to the
// old accept queue. Use HA behind a load balancer when every request must survive.
//
// This is the HONEST mechanism for "zero-downtime OTA": a Go binary is NOT
// live-patched (docs/UPGRADE-AND-ROLLBACK.md). The sequence is: install the new
// binary (verified, atomic — see `olivares upgrade`), start the new process (it
// binds the same port via SO_REUSEPORT and passes its health check), then send the
// old process SIGTERM (it stops accepting, finishes in-flight, exits). The HA path
// is the same idea across nodes: a rolling restart behind a load balancer.
//
// Where SO_REUSEPORT is unavailable (e.g. Windows) Listen returns a plain listener
// and Supported() is false; the caller then falls back to a drain-then-restart
// (a brief accept gap) instead of an overlap.
package serverhandover

import (
	"context"
	"net"
)

// Supported reports whether SO_REUSEPORT overlap is available on this platform.
func Supported() bool { return reuseSupported }

// Listen returns a listener for network/addr with SO_REUSEPORT set where supported,
// so a second process may bind the same addr for an overlapping handover.
func Listen(ctx context.Context, network, addr string) (net.Listener, error) {
	lc := net.ListenConfig{Control: controlReusePort}
	return lc.Listen(ctx, network, addr)
}
