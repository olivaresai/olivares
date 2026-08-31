// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package observability

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
)

// Name is the module's globally unique identifier (the runtime registry key).
const Name = "olivares.observability"

// Namespace is the module's API namespace: its routes mount under
// /v1/m/observability/. It owns no store entities, so there is no store
// namespace (route-only precedent: modules/governance/claudepolicy.go).
const Namespace = "observability"

// Module permissions, granted to the built-in roles by verb tier (viewer→read).
// All three routes are reads; none is in the privileged-read set, so a viewer
// can consult ingestion health, the trace correlation view and the binary
// attestation — they carry counters, correlation ids and build metadata, never
// payloads or secrets (docs/SECURITY-HARDENING.md).
const (
	permHealthRead      auth.Permission = "observability:health:read"
	permTracesRead      auth.Permission = "observability:traces:read"
	permAttestationRead auth.Permission = "observability:attestation:read"
)

// BuildInfo is the ldflags build metadata the composition root plumbs in
// (cmd/olivares/main.go:28-32 defines -X main.{version,commit,date}). The
// zero-value defaults mirror the ldflags defaults so an unplumbed module
// reports exactly what an unstamped binary would: dev/none/unknown — never a
// fabricated version.
type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

// Option configures a Module at construction.
type Option func(*Module)

// WithClock overrides the module clock (tests inject a deterministic clock).
func WithClock(now func() time.Time) Option {
	return func(m *Module) {
		if now != nil {
			m.now = now
		}
	}
}

// WithBuildInfo plumbs the ldflags build metadata from the composition root
// (package main is the only place the -X vars exist). Empty fields keep the
// unstamped defaults rather than erasing them.
func WithBuildInfo(bi BuildInfo) Option {
	return func(m *Module) {
		if bi.Version != "" {
			m.build.Version = bi.Version
		}
		if bi.Commit != "" {
			m.build.Commit = bi.Commit
		}
		if bi.Date != "" {
			m.build.Date = bi.Date
		}
	}
}

// Module is the observability read-model module (see doc.go). It holds only
// in-memory, process-global ingestion counters and the lazily computed
// self-hash of the running executable — no store entities, no background work.
type Module struct {
	log   *slog.Logger
	host  sdk.Host
	now   func() time.Time
	build BuildInfo

	stats ingestStats

	// selfHash is computed at most once per process (the executable does not
	// change underneath a running binary; re-hashing per request would be waste).
	selfHashOnce sync.Once
	selfHash     string
	selfHashNote string

	// walkerWarnOnce bounds the "ledger has no canonical walker" warning to one
	// log line per process (the condition is a deployment property, not per-request).
	walkerWarnOnce sync.Once

	mu        sync.Mutex
	cancelSub func() // event-bus subscription cancel
}

// Compile-time proof the module satisfies the SDK lifecycle and the API
// route/permission seam. It deliberately does NOT implement api.DataConsumer:
// the ingestion read-model is in-memory and the trace read-model uses the
// request-pinned mc.Data handle only.
var (
	_ sdk.Module = (*Module)(nil)
	_ api.Module = (*Module)(nil)
)

// New returns an observability module. The since anchor is taken at
// construction so the response can state honestly when the process-global
// counters started accumulating (they reset on restart, like /metrics).
func New(opts ...Option) *Module {
	m := &Module{now: time.Now, build: BuildInfo{Version: "dev", Commit: "none", Date: "unknown"}}
	for _, o := range opts {
		o(m)
	}
	m.stats.since = m.now()
	m.stats.sources = make(map[string]*sourceStats)
	return m
}

// Descriptor returns the module's self-description.
func (m *Module) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeModule,
		Title:       "Observability read-models",
		Description: "Read-only observability surfaces: per-standard ingestion health with live per-source bus counters (process-global), a W3C trace-correlation view derived from the audit ledger's trace_id/span_id stamps, and a measured supply-chain attestation of the running binary. Owns no entities, persists nothing, fabricates nothing.",
	}
}

// Init subscribes to the three first-party observation streams — the module
// counts EVERY bus fact per source, regardless of which module consumes it.
// It must not block.
func (m *Module) Init(_ context.Context, host sdk.Host) error {
	m.log = host.Logger()
	m.host = host
	cancel, err := host.Subscribe(
		[]event.Type{event.TypeEdgeObserved, event.TypeCostSampled, event.TypeFindingReported},
		m.onEvent,
	)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.cancelSub = cancel
	m.mu.Unlock()
	return nil
}

// Start is a near no-op: the subscription is live since Init and the module
// runs no background work (the read-models are computed on read).
func (m *Module) Start(context.Context) error { return nil }

// Stop cancels the bus subscription. Idempotent.
func (m *Module) Stop(context.Context) error {
	m.mu.Lock()
	cancel := m.cancelSub
	m.cancelSub = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}
