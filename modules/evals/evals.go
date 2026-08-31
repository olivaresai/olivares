// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package evals

import (
	"context"
	"log/slog"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk"
)

// Name is the module's globally unique identifier (the runtime registry key).
const Name = "olivares.evals"

// Namespace is the module's store and API namespace: entities are "evals.<entity>"
// and routes mount under /v1/m/evals/.
const Namespace = "evals"

// The module's permissions. Reading suites/cases/runs/scorecards is read-tier;
// creating suites/cases and LAUNCHING a run/A-B/monitor (the privileged,
// production-touching measurement actions, all audited) are write-tier; archiving a
// suite and PINNING a baseline (the decision surface) are admin-tier. Module
// permissions are granted by verb tier, so viewer→read, editor→write, admin/owner→
// admin (docs/SECURITY-HARDENING.md).
const (
	permSuiteRead  auth.Permission = "evals:suite:read"
	permSuiteWrite auth.Permission = "evals:suite:write"
	permSuiteAdmin auth.Permission = "evals:suite:admin"
	permRunRead    auth.Permission = "evals:run:read"
	permRunWrite   auth.Permission = "evals:run:write"
	permRunAdmin   auth.Permission = "evals:run:admin"
)

// Option configures a Module at construction.
type Option func(*Module)

// WithClock overrides the module clock (tests inject a deterministic clock).
func WithClock(c model.Clock) Option { return func(m *Module) { m.clock = c } }

// WithJudge wires the model-invocation port the llm_judge scorer uses. Without it
// the module ships the scorer but every llm_judge case is SKIPPED (degraded) — never
// silently scored as a pass.
func WithJudge(j Judge) Option { return func(m *Module) { m.judge = j } }

// WithPairJudge wires the ordered pairwise-comparison port the bias-mitigated A/B
// uses (order-swapped duals). Without it a requested pairwise comparison is a
// DECLARED skip in the A/B response, never a fabricated winner.
func WithPairJudge(j PairJudge) Option { return func(m *Module) { m.pairJudge = j } }

// WithBudgetGate wires the pre-flight spend admission the regression gate
// consults before invoking the judge. Without it gate judge spend is unbudgeted
// (Start warns once).
func WithBudgetGate(b BudgetGate) Option { return func(m *Module) { m.budget = b } }

// WithSessionSource wires a richer real-session sampler for the monitor (e.g. the
// module-II timeline). Without it the monitor reads core Session+Finding signals
// inline.
func WithSessionSource(s SessionSource) Option { return func(m *Module) { m.sessions = s } }

// WithScorer registers an additional pluggable scorer (overriding a built-in of the
// same id). The deterministic built-ins and llm_judge are always registered first.
func WithScorer(s Scorer) Option {
	return func(m *Module) { m.scorers[s.ID()] = s }
}

// Module is module XII — quality measurement (see doc.go for the bounded context
// and the docs/SECURITY-HARDENING.md minimal-data invariant). It is request-driven: a run is
// launched synchronously against caller-supplied outputs and scored against a
// versioned golden suite; the only model it invokes is the Judge.
type Module struct {
	log       *slog.Logger
	data      api.ModuleData
	host      sdk.Host
	clock     model.Clock
	judge     Judge
	pairJudge PairJudge
	budget    BudgetGate
	sessions  SessionSource
	scorers   map[string]Scorer
}

// Compile-time proof the module satisfies the SDK lifecycle, the API route/
// permission seam and the data-consumer seam.
var (
	_ sdk.Module       = (*Module)(nil)
	_ api.Module       = (*Module)(nil)
	_ api.DataConsumer = (*Module)(nil)
)

// New returns an evals module with a system clock, the OFFLINE judge (llm_judge
// degrades to skipped until is wired), the core session source and the
// deterministic + llm_judge scorers registered. The composition root injects the
// real adapters via the With* options.
func New(opts ...Option) *Module {
	m := &Module{
		clock:     model.SystemClock{},
		judge:     offlineJudge{},
		pairJudge: offlinePairJudge{},
		budget:    allowAllBudget{},
		sessions:  coreSessionSource{},
		scorers:   map[string]Scorer{},
	}
	m.registerBuiltins()
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
		Title:       "Evals — quality measurement",
		Description: "Scores candidate outputs against versioned golden suites with pluggable deterministic and llm_judge scorers, detects regression vs a baseline (a core Finding + bus signal), compares A/B prompt variants, monitors real-session signals, and exposes scorecards. Each run writes a canonical core EvalResult; per-case evidence is append-only and stores only a hash + label, never raw output (docs/08 §3).",
	}
}

// UseData receives the tenant-parameterized data handle (the api.DataConsumer seam).
func (m *Module) UseData(d api.ModuleData) { m.data = d }

// SetLogger attaches a logger (optional).
func (m *Module) SetLogger(l *slog.Logger) { m.log = l }

// Init keeps the host for publishing regression findings. The module holds no bus
// subscription (it is request-driven). It must not block.
func (m *Module) Init(_ context.Context, host sdk.Host) error {
	m.log = host.Logger()
	m.host = host
	return nil
}

// Start has no background work. It warns once per un-wired seam so a degraded
// deployment is visible (docs/SECURITY-HARDENING.md): without a data handle nothing persists;
// without a judge every llm_judge case is skipped, never a false pass.
func (m *Module) Start(context.Context) error {
	if m.log == nil {
		return nil
	}
	if m.data == nil {
		m.log.Warn("evals: started without a data handle; suites and runs will not persist")
	}
	if _, ok := m.judge.(offlineJudge); ok {
		// The case is SKIPPED and declared skipped — it answers, and it is never a false pass.
		m.log.Info("evals: no judge wired; llm_judge cases will be SKIPPED — degraded, never scored as a pass")
	}
	if _, ok := m.budget.(allowAllBudget); ok {
		m.log.Warn("evals: no budget gate wired; regression-gate judge spend is unbudgeted")
	}
	return nil
}

// Stop is a no-op (no background work, no subscription); idempotent.
func (m *Module) Stop(context.Context) error { return nil }

// APINamespace returns the module's namespace; it roots routes at /v1/m/evals/.
func (m *Module) APINamespace() string { return Namespace }

// Permissions declares the module's permissions so the built-in roles grant them by
// verb tier.
func (m *Module) Permissions() []auth.Permission {
	return []auth.Permission{
		permSuiteRead, permSuiteWrite, permSuiteAdmin,
		permRunRead, permRunWrite, permRunAdmin,
	}
}

// APIRoutes mounts the module's routes (docs §2.6). The engine wraps each with
// authentication, tenant resolution and the declared permission check; the
// privileged actions (create suite/cases, launch a run/A-B/monitor, pin a baseline,
// open a stream) additionally self-audit (docs/SECURITY-HARDENING.md).
func (m *Module) APIRoutes(reg api.RouteRegistrar) {
	// Suites + their cases (the versioned golden datasets).
	reg.Handle("GET", "/suites", permSuiteRead, m.handleListSuites)
	reg.Handle("POST", "/suites", permSuiteWrite, m.handleCreateSuite)
	reg.Handle("GET", "/suites/{id}", permSuiteRead, m.handleGetSuite)
	reg.Handle("GET", "/suites/{id}/cases", permSuiteRead, m.handleListCases)
	reg.Handle("POST", "/suites/{id}/cases", permSuiteWrite, m.handleAddCase)
	reg.Handle("POST", "/suites/{id}/archive", permSuiteAdmin, m.handleArchiveSuite)

	// Runs: launching a run is the privileged measurement action (write-tier,
	// audited). Reads are read-tier; the SSE replay audits its open.
	reg.Handle("GET", "/runs", permRunRead, m.handleListRuns)
	reg.Handle("POST", "/runs", permRunWrite, m.handleLaunchRun)
	reg.Handle("GET", "/runs/{id}", permRunRead, m.handleGetRun)
	reg.Handle("GET", "/runs/{id}/results", permRunRead, m.handleListResults)
	reg.Handle("GET", "/runs/{id}/stream", permRunRead, m.handleStreamRun)

	// A/B comparison and real-session monitoring (both write-tier, audited).
	reg.Handle("POST", "/ab", permRunWrite, m.handleAB)
	reg.Handle("POST", "/monitor", permRunWrite, m.handleMonitor)

	// Pinning a baseline is the decision surface (admin-tier, audited).
	reg.Handle("POST", "/baselines", permRunAdmin, m.handlePinBaseline)

	// Scorecards: the on-read quality aggregate for (read-tier; csv|json).
	reg.Handle("GET", "/scorecards", permRunRead, m.handleScorecards)

	// Judge↔human calibration: the human-labeled reference items, the
	// measured calibration runs and their immutable reports. Labeling and running a
	// calibration are write-tier + audited (a calibration run invokes the judge).
	reg.Handle("GET", "/calibration/items", permRunRead, m.handleListCalibItems)
	reg.Handle("POST", "/calibration/items", permRunWrite, m.handleAddCalibItems)
	reg.Handle("GET", "/calibration/reports", permRunRead, m.handleListCalibReports)
	reg.Handle("POST", "/calibration/run", permRunWrite, m.handleRunCalibration)

	// The CI regression gate: launching a gate evaluation is write-tier +
	// audited; OVERRIDING a failed gate is the governed decision surface
	// (admin-tier, requires a reason, audited).
	reg.Handle("GET", "/gate", permRunRead, m.handleListGates)
	reg.Handle("POST", "/gate", permRunWrite, m.handleGate)
	reg.Handle("GET", "/gate/{id}", permRunRead, m.handleGetGate)
	reg.Handle("POST", "/gate/{id}/override", permRunAdmin, m.handleOverrideGate)
}

func (m *Module) debugf(msg string, args ...any) {
	if m.log != nil {
		m.log.Debug(msg, args...)
	}
}
