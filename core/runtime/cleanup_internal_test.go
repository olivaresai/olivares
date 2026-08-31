// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package runtime

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"

	goplugin "github.com/hashicorp/go-plugin"

	"github.com/olivaresai/olivares/sdk"
)

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// nopOutput is a minimal in-package sdk.OutputConnector for the tracking tests. The id
// field makes it non-zero-size so two instances are distinct interface values (a
// zero-size struct compares equal to another and may even share an address).
type nopOutput struct{ id int }

func (nopOutput) Descriptor() sdk.Descriptor                     { return sdk.Descriptor{Type: sdk.TypeOutput} }
func (nopOutput) Open(context.Context, sdk.Config) error         { return nil }
func (nopOutput) Notify(context.Context, sdk.Notification) error { return nil }
func (nopOutput) Close(context.Context) error                    { return nil }

// TestRunPluginCleanupRunsUnregistersIdempotent: RunPluginCleanup runs the
// confinement cleanup keyed by a client exactly once, unregisters it, and is a no-op
// on a second call or a nil client — the live-teardown primitive for external output
// reload/remove and source live-remove.
func TestRunPluginCleanupRunsUnregistersIdempotent(t *testing.T) {
	r := New(Options{Logger: discardLog()})
	var ran int32
	client := &goplugin.Client{}
	r.mu.Lock()
	r.pluginCleanupByClient[client] = func() { atomic.AddInt32(&ran, 1) }
	r.mu.Unlock()

	r.RunPluginCleanup(client)
	if got := atomic.LoadInt32(&ran); got != 1 {
		t.Fatalf("cleanup ran %d times, want 1", got)
	}
	r.mu.Lock()
	_, still := r.pluginCleanupByClient[client]
	r.mu.Unlock()
	if still {
		t.Fatal("cleanup must be unregistered after it runs")
	}
	r.RunPluginCleanup(client) // idempotent: already gone
	r.RunPluginCleanup(nil)    // nil-safe
	if got := atomic.LoadInt32(&ran); got != 1 {
		t.Fatalf("cleanup ran %d times total, want 1 (idempotent)", got)
	}
}

// TestTrackOutputPluginRefusesWhenStopped: once the runtime is stopping (its
// teardown set is snapshotted), TrackOutputPlugin must NOT append a late plugin — that
// would orphan it past Stop — and must instead release its confinement. Guards the
// SIGHUP-reconcile-racing-shutdown window.
func TestTrackOutputPluginRefusesWhenStopped(t *testing.T) {
	r := New(Options{Logger: discardLog()})
	var cleaned int32
	client := &goplugin.Client{}
	r.mu.Lock()
	r.stopped = true
	r.pluginCleanupByClient[client] = func() { atomic.AddInt32(&cleaned, 1) }
	before := len(r.standaloneOutputs)
	beforeClients := len(r.clients)
	r.mu.Unlock()

	// conn non-nil, client nil (a real *goplugin.Client.Kill on the zero value is out of
	// scope here): the append must still be refused because the runtime is stopped.
	r.TrackOutputPlugin(nopOutput{}, nil)
	r.mu.Lock()
	after := len(r.standaloneOutputs)
	afterClients := len(r.clients)
	r.mu.Unlock()
	if after != before || afterClients != beforeClients {
		t.Fatalf("TrackOutputPlugin appended on a stopped runtime (standalone %d->%d, clients %d->%d)", before, after, beforeClients, afterClients)
	}

	// With a tracked client, the stopped path releases its confinement.
	r.TrackOutputPlugin(nil, client)
	if got := atomic.LoadInt32(&cleaned); got != 1 {
		t.Fatalf("stopped TrackOutputPlugin released confinement %d times, want 1", got)
	}
}

// TestUntrackOutputPluginRemovesExactly: UntrackOutputPlugin removes exactly the
// given conn+client from the Stop teardown slices and is nil-safe.
func TestUntrackOutputPluginRemovesExactly(t *testing.T) {
	r := New(Options{Logger: discardLog()})
	// Distinct instances (distinct id): equal-valued connectors would make
	// UntrackOutputPlugin unable to tell them apart.
	keep := nopOutput{id: 1}
	drop := nopOutput{id: 2}
	keepClient := &goplugin.Client{}
	dropClient := &goplugin.Client{}
	r.TrackOutputPlugin(keep, keepClient)
	r.TrackOutputPlugin(drop, dropClient)

	r.UntrackOutputPlugin(drop, dropClient)
	r.UntrackOutputPlugin(nil, nil) // nil-safe

	r.mu.Lock()
	defer r.mu.Unlock()
	for _, o := range r.standaloneOutputs {
		if o == drop {
			t.Fatal("dropped output still tracked")
		}
	}
	for _, c := range r.clients {
		if c == dropClient {
			t.Fatal("dropped client still tracked")
		}
	}
	if len(r.standaloneOutputs) != 1 || len(r.clients) != 1 {
		t.Fatalf("tracking slices = %d outputs / %d clients, want 1/1", len(r.standaloneOutputs), len(r.clients))
	}
}
