// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inferenceproxy

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
const Name = "olivares.inferenceproxy"

// Namespace is the module's store and API namespace. Its registered entities are
// "inferenceproxy.<entity>" and its routes mount under /v1/m/inferenceproxy/.
const Namespace = "inferenceproxy"

// Module owns the inline inference PEP's per-tenant governance config and the
// inference-egress DLP rule set. It is request-driven (no background work) and
// composes nothing: the composition root reads Policy() and decides. See doc.go.
type Module struct {
	log   *slog.Logger
	clock model.Clock

	mu   sync.RWMutex
	data api.ModuleData // tenant-parameterized handle (late-bound via UseData)
}

// Compile-time proof the module satisfies the SDK lifecycle, the API route/permission
// seam and the data-consumer seam (its schema is registered by RegisterSchema, the
// engine-side structural seam).
var (
	_ sdk.Module       = (*Module)(nil)
	_ api.Module       = (*Module)(nil)
	_ api.DataConsumer = (*Module)(nil)
)

// Option configures the module at construction.
type Option func(*Module)

// WithClock injects a deterministic clock (tests).
func WithClock(c model.Clock) Option { return func(m *Module) { m.clock = c } }

// New returns an inference-proxy governance module.
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
		Title:       "Inline inference PEP — governance config & DLP",
		Description: "Per-tenant config and inference-egress DLP policy for the optional, opt-in /v1/messages proxy that governs non-Claude-Code inference in-band (DLP, budget, model-access, residency, recording).",
	}
}

// UseData receives the least-privilege, tenant-parameterized data handle from the
// engine boot (the api.DataConsumer seam), before Start. Policy() reads it under the
// module lock; route handlers use the route-pinned mc.Data instead.
func (m *Module) UseData(d api.ModuleData) {
	m.mu.Lock()
	m.data = d
	m.mu.Unlock()
}

// Init keeps the logger. It subscribes to nothing — the config is request-driven and
// Policy() is call-driven. It must not block.
func (m *Module) Init(_ context.Context, host sdk.Host) error {
	m.log = host.Logger()
	return nil
}

// Start has no background work; it only checks the data handle was wired.
func (m *Module) Start(context.Context) error {
	m.mu.RLock()
	data := m.data
	m.mu.RUnlock()
	if data == nil && m.log != nil {
		m.log.Warn("inferenceproxy: started without a data handle; config/DLP will not persist and Policy() fails closed")
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

// --- permissions -------------------------------------------------------------------

// The module's permissions, granted to the built-in roles by verb tier. Reads are
// viewer-tier; writing the proxy config is ADMIN-tier because fail_open and the gate
// toggles change enforcement. Writing the DLP egress policy is also ADMIN-tier —
// authorizing egress is a privileged governance change, the same tier knowledge's
// DLP rules use.
const (
	permConfigRead  auth.Permission = "inferenceproxy:config:read"
	permConfigAdmin auth.Permission = "inferenceproxy:config:admin"
	permDLPRead     auth.Permission = "inferenceproxy:dlp:read"
	permDLPAdmin    auth.Permission = "inferenceproxy:dlp:admin"
)

// APINamespace returns the module's namespace; it roots routes at /v1/m/inferenceproxy/.
func (m *Module) APINamespace() string { return Namespace }

// Permissions declares the permissions the module's routes require.
func (m *Module) Permissions() []auth.Permission {
	return []auth.Permission{permConfigRead, permConfigAdmin, permDLPRead, permDLPAdmin}
}

// APIRoutes mounts the module's routes. The engine wraps each with authentication,
// tenant resolution and the declared permission check, and pins the data handle to the
// resolved tenant.
func (m *Module) APIRoutes(reg api.RouteRegistrar) {
	reg.Handle("GET", "/config", permConfigRead, m.handleGetConfig)
	reg.Handle("PUT", "/config", permConfigAdmin, m.handlePutConfig)
	reg.Handle("POST", "/device/approve", permConfigAdmin, m.handleApproveDeviceGrant)
	reg.Handle("GET", "/dlp/rules", permDLPRead, m.handleListDLPRules)
	reg.Handle("PUT", "/dlp/rules", permDLPAdmin, m.handlePutDLPRule)
	reg.Handle("DELETE", "/dlp/rules/{id}", permDLPAdmin, m.handleDeleteDLPRule)
}
