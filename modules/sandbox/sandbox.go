// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sandbox

import (
	"context"
	"log/slog"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk"
)

// Name is the module's globally unique identifier (the runtime registry key).
const Name = "olivares.sandbox"

// Namespace is the module's store and API namespace: entities are "sandbox.<entity>"
// and routes mount under /v1/m/sandbox/.
const Namespace = "sandbox"

// The module's permissions. Reading scenarios/runs/comparisons is read-tier; creating
// a scenario or LAUNCHING a run/replay (the privileged, execution-touching action) is
// write-tier; ARCHIVING a scenario and COMPARING two variants (a deploy decision) are
// admin-tier so only a tenant admin/owner can fire them — and every privileged action
// is audited (docs/SECURITY-HARDENING.md).
const (
	permScenarioRead  auth.Permission = "sandbox:scenario:read"
	permScenarioWrite auth.Permission = "sandbox:scenario:write"
	permScenarioAdmin auth.Permission = "sandbox:scenario:admin"
	permRunRead       auth.Permission = "sandbox:run:read"
	permRunWrite      auth.Permission = "sandbox:run:write"
	permRunAdmin      auth.Permission = "sandbox:run:admin"
)

// Option configures a Module at construction.
type Option func(*Module)

// WithClock overrides the module clock (tests inject a deterministic clock).
func WithClock(c model.Clock) Option { return func(m *Module) { m.clock = c } }

// WithRunner wires the isolated execution backend. Without it the module uses the
// in-proc-mock runner: deterministic, in-memory, isolated by construction. An
// OS-level backend (hardened container / microVM) is injected here by the
// composition root (docs/contracts).
func WithRunner(r Runner) Option { return func(m *Module) { m.runner = r } }

// WithScorer wires the XII (evals) scoring adapter. Without it a run is recorded
// "executed, not scored" — never a silent pass (docs/contracts/§4).
func WithScorer(s Scorer) Option { return func(m *Module) { m.scorer = s } }

// WithHistorySource wires a richer replay timeline source (e.g. the sessions
// timeline of module II). Without it the module reads what core exposes; if it
// cannot reconstruct ordered steps a replay is DEGRADED, never fabricated.
func WithHistorySource(h HistorySource) Option { return func(m *Module) { m.history = h } }

// Module is module XVII — testing-sandbox (see doc.go for the bounded context and the
// docs/contracts isolation guarantee). It is request-driven: a scenario or
// replay is executed synchronously inside the launching handler against an isolated,
// ephemeral runner, and the run + outputs are persisted as evidence.
type Module struct {
	log     *slog.Logger
	data    api.ModuleData
	host    sdk.Host
	clock   model.Clock
	runner  Runner
	scorer  Scorer
	history HistorySource
}

// Compile-time proof the module satisfies the SDK lifecycle, the API route/
// permission seam and the data-consumer seam.
var (
	_ sdk.Module       = (*Module)(nil)
	_ api.Module       = (*Module)(nil)
	_ api.DataConsumer = (*Module)(nil)
)

// New returns a sandbox module with a system clock, the in-proc-mock runner
// (isolated, deterministic), the UNSCORED scorer (executed-not-scored until XII is
// wired) and the CORE history source (replay degrades to zero steps until a richer
// timeline is wired). The composition root injects the real adapters via the
// With* options.
func New(opts ...Option) *Module {
	m := &Module{
		clock:   model.SystemClock{},
		runner:  inprocMockRunner{},
		scorer:  unscoredScorer{},
		history: coreHistorySource{},
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
		Title:       "Testing-sandbox",
		Description: "Isolated, ephemeral execution of agent scenarios against mocked MCPs/resources, deterministic replay of historical sessions, and pre/post-deploy comparison of two variants. The default runner is isolated by construction (no store/network/secret handle); a run records its real runner + isolated flag — no faked microVM. Outputs are scored by module XII through a composition-root adapter; without it a run is executed-not-scored, never a silent pass. Append-only output/comparison evidence (docs/contracts).",
	}
}

// UseData receives the tenant-parameterized data handle (the api.DataConsumer seam).
func (m *Module) UseData(d api.ModuleData) { m.data = d }

// SetLogger attaches a logger (optional).
func (m *Module) SetLogger(l *slog.Logger) { m.log = l }

// Init keeps the host for publishing. The module holds no bus subscription (it is
// request-driven). It must not block.
func (m *Module) Init(_ context.Context, host sdk.Host) error {
	m.log = host.Logger()
	m.host = host
	return nil
}

// Start has no background work. It warns once per un-wired seam so a degraded
// deployment is visible (docs/SECURITY-HARDENING.md): without a data handle nothing persists; a
// non-isolated runner is flagged; without an XII scorer runs are executed-not-scored.
func (m *Module) Start(context.Context) error {
	if m.log == nil {
		return nil
	}
	if m.data == nil {
		m.log.Warn("sandbox: started without a data handle; scenarios, runs and outputs will not persist")
	}
	if !m.runner.Isolated() {
		m.log.Warn("sandbox: runner reports NOT isolated", "runner", m.runner.Name())
	}
	if _, ok := m.scorer.(unscoredScorer); ok {
		m.log.Warn("sandbox: no scorer wired (XII); scored runs will be recorded executed-not-scored, never a silent pass")
	}
	return nil
}

// Stop is a no-op (no background work, no subscription); idempotent.
func (m *Module) Stop(context.Context) error { return nil }

// APINamespace returns the module's namespace; it roots routes at /v1/m/sandbox/.
func (m *Module) APINamespace() string { return Namespace }

// Permissions declares the module's permissions so the built-in roles grant them by
// verb tier (viewer→read, editor→write, admin/owner→admin).
func (m *Module) Permissions() []auth.Permission {
	return []auth.Permission{
		permScenarioRead, permScenarioWrite, permScenarioAdmin,
		permRunRead, permRunWrite, permRunAdmin,
	}
}

// APIRoutes mounts the module's routes. The engine wraps each with authentication,
// tenant resolution and the declared permission check, and pins the data handle to
// the resolved tenant; the privileged actions (create scenario, launch run/replay,
// compare) additionally self-audit (docs/SECURITY-HARDENING.md).
func (m *Module) APIRoutes(reg api.RouteRegistrar) {
	// Scenarios: synthetic, operator-authored fixtures (steps + mocks).
	reg.Handle("GET", "/scenarios", permScenarioRead, m.handleListScenarios)
	reg.Handle("POST", "/scenarios", permScenarioWrite, m.handleCreateScenario)
	reg.Handle("GET", "/scenarios/{id}", permScenarioRead, m.handleGetScenario)
	reg.Handle("POST", "/scenarios/{id}/archive", permScenarioAdmin, m.handleArchiveScenario)

	// Runs: launching a scenario run or a replay is the privileged execution action
	// (write-tier, audited); it runs synchronously in an isolated, ephemeral runner.
	reg.Handle("POST", "/scenarios/{id}/run", permRunWrite, m.handleRunScenario)
	reg.Handle("POST", "/replay", permRunWrite, m.handleReplay)
	reg.Handle("GET", "/runs", permRunRead, m.handleListRuns)
	reg.Handle("GET", "/runs/{id}", permRunRead, m.handleGetRun)
	reg.Handle("GET", "/runs/{id}/outputs", permRunRead, m.handleListOutputs)
	reg.Handle("GET", "/runs/{id}/stream", permRunRead, m.handleStream)

	// Pre/post-deploy comparison: a deploy DECISION (admin-tier, audited) recorded as
	// append-only evidence (docs/SECURITY-HARDENING.md).
	reg.Handle("POST", "/compare", permRunAdmin, m.handleCompare)
	reg.Handle("GET", "/comparisons", permRunRead, m.handleListComparisons)
	reg.Handle("GET", "/comparisons/{id}", permRunRead, m.handleGetComparison)
}

func (m *Module) debugf(msg string, args ...any) {
	if m.log != nil {
		m.log.Debug(msg, args...)
	}
}
