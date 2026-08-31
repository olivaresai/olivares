// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package deploy

import (
	"context"
	"log/slog"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk"
)

// Name is the module's globally unique identifier (the runtime registry key).
const Name = "olivares.deploy"

// Namespace is the module's store and API namespace: its entities are
// "deploy.<entity>" and its routes mount under /v1/m/deploy/.
const Namespace = "deploy"

// The module's permissions, granted to the built-in roles by verb tier (viewer→
// read, editor→write, admin/owner→admin). Reading definitions/wirings/operations
// is read-tier; declaring desired state, planning and verifying are write-tier;
// the GOVERNED INFRASTRUCTURE MUTATIONS (apply, retire) are admin-tier — a second
// control on top of the mandatory HITL approval gate.
const (
	permDeploymentRead  auth.Permission = "deploy:deployment:read"
	permDeploymentWrite auth.Permission = "deploy:deployment:write"
	permDeploymentAdmin auth.Permission = "deploy:deployment:admin"
	permWiringRead      auth.Permission = "deploy:wiring:read"
)

// Option configures a Module at construction.
type Option func(*Module)

// WithClock overrides the module clock (tests inject a deterministic clock).
func WithClock(c model.Clock) Option { return func(m *Module) { m.clock = c } }

// WithApprovalGate wires the HITL gate. Without it, every governed mutation
// is denied (deny-by-default).
func WithApprovalGate(g ApprovalGate) Option { return func(m *Module) { m.gate = g } }

// WithExecutor wires the runtime/IaC executor. Without it, plan/apply/verify/
// retire fail closed (no infrastructure can be reconciled).
func WithExecutor(e Executor) Option { return func(m *Module) { m.exec = e } }

// WithIdentityBinder wires the per-agent-identity binder. Without it, a
// wiring's attribution is reported as degraded (marked, never faked).
func WithIdentityBinder(b IdentityBinder) Option { return func(m *Module) { m.binder = b } }

// WithStopGate wires the estate kill-switch pre-flight. Without it, no stop
// ever freezes an apply/retire (the composition root always wires the
// governance-backed gate; see allowStopGate).
func WithStopGate(g StopGate) Option { return func(m *Module) { m.stopGate = g } }

// Module is module VII — deployment & integration. See doc.go for the bounded
// context and the deny-closed defaults of its three composition-root seams.
type Module struct {
	log      *slog.Logger
	data     api.ModuleData
	host     sdk.Host
	clock    model.Clock
	gate     ApprovalGate
	exec     Executor
	binder   IdentityBinder
	stopGate StopGate
}

// Compile-time proof the module satisfies the SDK lifecycle, the API route/
// permission seam and the data-consumer seam. RegisterSchema (the engine-side
// SchemaProvider seam) is structural and verified by the runtime at boot/test.
var (
	_ sdk.Module       = (*Module)(nil)
	_ api.Module       = (*Module)(nil)
	_ api.DataConsumer = (*Module)(nil)
)

// New returns a deploy module with safe, deny-closed defaults for all three
// integration seams (deny gate, fail-closed executor, degraded-attribution
// binder). The composition root replaces them with real adapters via options.
func New(opts ...Option) *Module {
	m := &Module{
		clock:    model.SystemClock{},
		gate:     denyGate{},
		exec:     unwiredExecutor{},
		binder:   unwiredBinder{},
		stopGate: allowStopGate{},
	}
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
		Title:       "Deployment & integration",
		Description: "The only module that acts on customer infrastructure: provisions/updates/retires agents and MCP servers as declarative, versioned, reversible operations and wires them to enterprise resources — every mutation gated by human-in-the-loop approval, least-privilege, secret-by-reference, and recorded to the append-only ledger.",
	}
}

// UseData receives the least-privilege, tenant-parameterized data handle from the
// engine boot (the api.DataConsumer seam), before Start.
func (m *Module) UseData(d api.ModuleData) { m.data = d }

// Init keeps the host for publishing the declared PERMITTED wiring as policy-grant
// edges (the feed) and deployment findings. The module is request-driven; it
// holds no bus subscription in v1. It must not block.
func (m *Module) Init(_ context.Context, host sdk.Host) error {
	m.log = host.Logger()
	m.host = host
	return nil
}

// Start has no background work. It warns once per un-wired seam so a deployment
// that cannot actually act — or that would deny every mutation — is VISIBLE
// rather than a silent surprise at apply time (the honest Fase C posture).
func (m *Module) Start(context.Context) error {
	if m.log == nil {
		return nil
	}
	if m.data == nil {
		m.log.Warn("deploy: started without a data handle; definitions, wirings and operations will not persist")
	}
	if _, ok := m.gate.(denyGate); ok {
		m.log.Warn("deploy: no approval gate wired; every governed infrastructure mutation will be DENIED by default")
	}
	if _, ok := m.exec.(unwiredExecutor); ok {
		m.log.Warn("deploy: no runtime executor wired (IaC); desired state can be declared but not reconciled to infrastructure")
	}
	if _, ok := m.binder.(unwiredBinder); ok {
		// Attribution is REPORTED as degraded — the route answers. Rule: Info.
		m.log.Info("deploy: no identity binder wired; wiring attribution will be reported as degraded")
	}
	return nil
}

// Stop is a no-op (no background work, no live subscription in v1); idempotent.
func (m *Module) Stop(context.Context) error { return nil }

// APINamespace returns the module's namespace; it roots routes at /v1/m/deploy/.
func (m *Module) APINamespace() string { return Namespace }

// Permissions declares the permissions the module's routes require so the
// built-in roles grant them by verb tier.
func (m *Module) Permissions() []auth.Permission {
	return []auth.Permission{permDeploymentRead, permDeploymentWrite, permDeploymentAdmin, permWiringRead}
}

// APIRoutes mounts the module's routes. The engine wraps each with authentication,
// tenant resolution and the declared permission check before the handler runs, and
// pins the data handle to the resolved tenant.
func (m *Module) APIRoutes(reg api.RouteRegistrar) {
	// Desired-state declaration (control-plane only — NOT an infra mutation).
	reg.Handle("GET", "/definitions", permDeploymentRead, m.handleListDefinitions)
	reg.Handle("POST", "/definitions", permDeploymentWrite, m.handleCreateDefinition)
	reg.Handle("GET", "/definitions/{id}", permDeploymentRead, m.handleGetDefinition)
	reg.Handle("PUT", "/definitions/{id}", permDeploymentWrite, m.handleUpdateDefinition)
	reg.Handle("DELETE", "/definitions/{id}", permDeploymentWrite, m.handleDeleteDefinition)
	reg.Handle("GET", "/definitions/{id}/revisions", permDeploymentRead, m.handleListRevisions)
	reg.Handle("POST", "/definitions/{id}/rollback", permDeploymentWrite, m.handleRollback)

	// Reconciliation lifecycle. plan is a dry-run that REQUESTS approval; verify is
	// read-only on infra; apply and retire MUTATE infra and are admin-tier AND
	// gated by the HITL approval (deny-by-default).
	reg.Handle("POST", "/definitions/{id}/plan", permDeploymentWrite, m.handlePlan)
	reg.Handle("POST", "/definitions/{id}/verify", permDeploymentWrite, m.handleVerify)
	reg.Handle("POST", "/definitions/{id}/apply", permDeploymentAdmin, m.handleApply)
	reg.Handle("POST", "/definitions/{id}/retire", permDeploymentAdmin, m.handleRetire)

	// The declared PERMITTED wiring contract and the change-management
	// ledger.
	reg.Handle("GET", "/wirings", permWiringRead, m.handleListWirings)
	reg.Handle("GET", "/operations", permDeploymentRead, m.handleListOperations)
}

// debugf logs at debug level if a logger is set.
func (m *Module) debugf(msg string, args ...any) {
	if m.log != nil {
		m.log.Debug(msg, args...)
	}
}

// errorf logs at error level if a logger is set. It is used where a best-effort
// ledger write fails: the primary outcome (deny/failure) still returns to the
// client, but a lost audit/operation record is an integrity event worth surfacing
// rather than swallowing (docs/SECURITY-HARDENING.md — never a silent gap).
func (m *Module) errorf(msg string, args ...any) {
	if m.log != nil {
		m.log.Error(msg, args...)
	}
}
