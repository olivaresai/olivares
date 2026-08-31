// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package redteam

import (
	"context"
	"log/slog"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk"
)

// Name is the module's globally unique identifier (the runtime registry key).
const Name = "olivares.redteam"

// Namespace is the module's store and API namespace: entities are "redteam.<entity>"
// and routes mount under /v1/m/redteam/.
const Namespace = "redteam"

// The module's permissions. Reading the catalog, targets and runs is read-tier;
// REGISTERING/AUTHORIZING a target (granting consent) and LAUNCHING a run (the
// privileged, production-touching adversarial action) are admin-tier so only a
// tenant admin/owner can fire them — and every one is audited (docs/SECURITY-HARDENING.md).
const (
	permTargetRead  auth.Permission = "redteam:target:read"
	permTargetAdmin auth.Permission = "redteam:target:admin"
	permRunRead     auth.Permission = "redteam:run:read"
	permScanAdmin   auth.Permission = "redteam:scan:admin"
)

// Option configures a Module at construction.
type Option func(*Module)

// WithClock overrides the module clock (tests inject a deterministic clock).
func WithClock(c model.Clock) Option { return func(m *Module) { m.clock = c } }

// WithSandbox wires the execution environment. Without it the module ships the
// full battery + scoring but a run is DEGRADED (every probe skipped) — never
// silently scored as a pass.
func WithSandbox(s Sandbox) Option { return func(m *Module) { m.sandbox = s } }

// Module is module XVIII — red-teaming & adversarial testing (see doc.go for the
// bounded context and the docs/SECURITY-HARDENING.md dual-use RED LINE). It is request-driven: a
// run is launched against a registered, authorized target and executed in the
// sandbox.
type Module struct {
	log     *slog.Logger
	data    api.ModuleData
	host    sdk.Host
	clock   model.Clock
	sandbox Sandbox
}

// Compile-time proof the module satisfies the SDK lifecycle, the API route/
// permission seam and the data-consumer seam.
var (
	_ sdk.Module       = (*Module)(nil)
	_ api.Module       = (*Module)(nil)
	_ api.DataConsumer = (*Module)(nil)
)

// New returns a red-team module with a system clock and the OFFLINE sandbox (no
// execution until is wired). The composition root injects the real sandbox via
// WithSandbox.
func New(opts ...Option) *Module {
	m := &Module{clock: model.SystemClock{}, sandbox: offlineSandbox{}}
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
		Title:       "Red-teaming & adversarial testing",
		Description: "A consent-gated, defensive robustness battery (prompt injection, jailbreak, exfiltration, tool poisoning) run ONLY against the client's own authorized agents in the sandbox, scored against the OWASP Top 10 for Agentic Applications and MITRE ATLAS. Append-only run/result evidence; failures become findings. Not a C2 (docs/08 §8).",
	}
}

// UseData receives the tenant-parameterized data handle (the api.DataConsumer seam).
func (m *Module) UseData(d api.ModuleData) { m.data = d }

// SetLogger attaches a logger (optional).
func (m *Module) SetLogger(l *slog.Logger) { m.log = l }

// Init keeps the host for publishing findings. The module holds no bus subscription
// (it is request-driven). It must not block.
func (m *Module) Init(_ context.Context, host sdk.Host) error {
	m.log = host.Logger()
	m.host = host
	return nil
}

// Start has no background work. It warns once per un-wired seam so a degraded
// deployment is visible (docs/SECURITY-HARDENING.md): without a data handle nothing persists;
// without an sandbox every run is degraded (skipped), never a false pass.
func (m *Module) Start(context.Context) error {
	if m.log == nil {
		return nil
	}
	if m.data == nil {
		m.log.Warn("redteam: started without a data handle; targets and runs will not persist")
	}
	if _, ok := m.sandbox.(offlineSandbox); ok {
		// The run answers DEGRADED with every probe declared skipped; nothing refuses.
		m.log.Info("redteam: no sandbox wired; runs will be DEGRADED — every probe is skipped, never scored as a pass")
	}
	return nil
}

// Stop is a no-op (no background work, no subscription); idempotent.
func (m *Module) Stop(context.Context) error { return nil }

// APINamespace returns the module's namespace; it roots routes at /v1/m/redteam/.
func (m *Module) APINamespace() string { return Namespace }

// Permissions declares the module's permissions so the built-in roles grant them by
// verb tier.
func (m *Module) Permissions() []auth.Permission {
	return []auth.Permission{permTargetRead, permTargetAdmin, permRunRead, permScanAdmin}
}

// APIRoutes mounts the module's routes. The engine wraps each with authentication,
// tenant resolution and the declared permission check; the privileged actions
// (register/authorize a target, launch a run) additionally self-audit (docs/SECURITY-HARDENING.md).
func (m *Module) APIRoutes(reg api.RouteRegistrar) {
	// The battery catalog (the test taxonomy — metadata, NOT weaponized payloads).
	reg.Handle("GET", "/catalog", permRunRead, m.handleCatalog)

	// Targets: the CONSENT surface. Registering and authorizing a target are
	// admin-tier (granting permission to test) and audited (docs/SECURITY-HARDENING.md).
	reg.Handle("GET", "/targets", permTargetRead, m.handleListTargets)
	reg.Handle("POST", "/targets", permTargetAdmin, m.handleRegisterTarget)
	reg.Handle("GET", "/targets/{id}", permTargetRead, m.handleGetTarget)
	reg.Handle("POST", "/targets/{id}/authorize", permTargetAdmin, m.handleAuthorizeTarget)

	// Runs: launching a run is the privileged adversarial action (admin-tier,
	// audited), and only against an AUTHORIZED target (docs/SECURITY-HARDENING.md).
	reg.Handle("GET", "/runs", permRunRead, m.handleListRuns)
	reg.Handle("POST", "/runs", permScanAdmin, m.handleLaunchRun)
	reg.Handle("GET", "/runs/{id}", permRunRead, m.handleGetRun)
	reg.Handle("GET", "/runs/{id}/results", permRunRead, m.handleListResults)
}

func (m *Module) debugf(msg string, args ...any) {
	if m.log != nil {
		m.log.Debug(msg, args...)
	}
}
