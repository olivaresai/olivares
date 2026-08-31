// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope

import (
	"context"
	"log/slog"
	"sync"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk"
)

// Name is the module's globally unique identifier (the runtime registry key).
const Name = "olivares.sourcescope"

// Namespace is the module's store and API namespace. Its registered entities are
// "sourcescope.<entity>" and its routes mount under /v1/m/sourcescope/.
const Namespace = "sourcescope"

// Module is the source-scoping plane (+): it owns the source→scope
// binding table, the connector→workspace assignment table, and the workspace-
// scoped connector definition table. It exposes the write APIs the console
// consumes, and serves the Resolver the runtime PEPs (models, knowledge) call to
// decide whether an agent/session may resolve a source and which credential reference
// applies. It DECIDES nothing itself: it composes containment, the
// ScopedAuthorizer and the built-in RBAC (see resolver.go).
type Module struct {
	log   *slog.Logger
	clock model.Clock

	// scoped is the three-valued grant engine (auth.ScopedAuthorizer =
	// governance.ScopedGrants); it provides the cross-scope GRANT override and the
	// scoped FORBID. Injected once at construction; nil reduces the resolver to
	// containment ∨ RBAC (no cross-scope grant, no scoped forbid).
	scoped auth.ScopedAuthorizer

	// wsSealer seals inline secrets for workspace-scoped connectors. Nil means
	// workspace connector inline secrets are unavailable (reference-only mode).
	wsSealer WorkspaceConnectorSealer

	mu   sync.RWMutex
	data api.ModuleData // tenant-parameterized handle (late-bound via UseData)
	host sdk.Host       // kept for Publish (the access-map permitted-edge projection)
}

// Compile-time proof the module satisfies the SDK lifecycle, the API route/permission
// seam, the engine-side schema seam and the data-consumer seam.
var (
	_ sdk.Module       = (*Module)(nil)
	_ api.Module       = (*Module)(nil)
	_ api.DataConsumer = (*Module)(nil)
)

// Option configures the module at construction.
type Option func(*Module)

// WithScopedAuthorizer wires the grant engine (governance.ScopedGrants) so the
// resolver can honor cross-scope grants and scoped forbids. Without it the resolver
// still enforces containment ∨ RBAC (deny-closed), but cannot open a foreign-scope
// source by grant nor be narrowed by a scoped forbid.
func WithScopedAuthorizer(s auth.ScopedAuthorizer) Option {
	return func(m *Module) { m.scoped = s }
}

// WithWorkspaceSealer wires the workspace connector secret sealer. Without it
// workspace connectors operate in reference-only mode (inline secrets are rejected).
func WithWorkspaceSealer(s WorkspaceConnectorSealer) Option {
	return func(m *Module) { m.wsSealer = s }
}

// New returns a source-scoping module.
func New(opts ...Option) *Module {
	m := &Module{clock: model.SystemClock{}}
	for _, o := range opts {
		o(m)
	}
	return m
}

// Descriptor returns the module's self-description.
func (m *Module) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeModule,
		Title:       "Source scoping & scoped credentials",
		Description: "Binds connected sources (MCP servers, models, providers, knowledge bases, data sources) to a workspace or agent-group and resolves scoped credentials, so an out-of-scope agent cannot resolve a source and uses the scoped reference, never the global one.",
	}
}

// UseData receives the least-privilege, tenant-parameterized data handle from the
// engine boot (the api.DataConsumer seam), before Start. The resolver reads it under
// the module lock; route handlers use the route-pinned mc.Data instead.
func (m *Module) UseData(d api.ModuleData) {
	m.mu.Lock()
	m.data = d
	m.mu.Unlock()
}

// Init keeps the host (for the access-map permitted-edge projection) and logger. It
// subscribes to nothing — the binding table is request-driven and the resolver is
// call-driven. It must not block.
func (m *Module) Init(_ context.Context, host sdk.Host) error {
	m.mu.Lock()
	m.host = host
	m.mu.Unlock()
	m.log = host.Logger()
	return nil
}

// Start has no background work (the binding store is request-driven and the resolver
// is call-driven); it only checks the data handle was wired.
func (m *Module) Start(context.Context) error {
	m.mu.RLock()
	data := m.data
	m.mu.RUnlock()
	if data == nil && m.log != nil {
		m.log.Warn("sourcescope: started without a data handle; bindings will not persist and the resolver fails closed")
	}
	return nil
}

// Stop releases nothing (no subscriptions, no goroutines). It is idempotent.
func (m *Module) Stop(context.Context) error { return nil }

// moduleData returns the late-bound tenant-parameterized handle under the lock.
func (m *Module) moduleData() api.ModuleData {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.data
}

// host0 returns the bus host under the lock (nil before Init).
func (m *Module) host0() sdk.Host {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.host
}

// Resolver returns the runtime resolver the composition root injects into the model
// ScopeGate and the knowledge RetrievalGuard. It reads the module's late-bound data
// handle on every call, so it is safe to hand out before the store exists (boot
// order), exactly like governance.ScopedGrants.
func (m *Module) Resolver() *Resolver {
	return &Resolver{m: m}
}
