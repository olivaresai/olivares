// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
)

// Name is the module's globally unique identifier (the runtime registry key).
const Name = "olivares.sessions"

// Namespace is the module's store and API namespace.
const Namespace = "sessions"

// Recency windows for deriving the Claude Code state from last activity. They
// are display heuristics applied at read time, never stored, so the state is
// always accurate to the moment and the module never fabricates a lifecycle.
const (
	defaultActiveWindow = 2 * time.Minute  // events within → active
	defaultIdleWindow   = 30 * time.Minute // within → idle; beyond → ended
)

// Module is the live-operation/sessions module.
type Module struct {
	*Dependencies
	engineDependencies bool

	publishObservation func(context.Context, event.Event) error
	log                *slog.Logger

	clock  model.Clock
	broker *broker

	activeWindow time.Duration
	idleWindow   time.Duration

	// rt is the OPERATE runtime (governed Claude Code session lifecycle):
	// the deny-closed launch seams and the in-memory registry of live processes.
	rt *runtimeState
	// previews are the open browser previews of session ports (preview.go).
	previews previewGrants
	// protocolBindingSpecValidators are server-owned K5 capability witnesses,
	// keyed by protocol. Browser/CLI input is never trusted as validation.
	protocolBindingSpecValidators map[BindingProtocol][]ProtocolBindingSpecValidator
	// accountsRoot is the directory this node creates provider account homes
	// under, late-bound by the composition root (provider_account_home.go).
	// Empty is deny-closed: the home-creation verb refuses and names the missing
	// wiring, because a directory nobody configured is a directory nobody meant.
	accountsRoot          string
	accountHomeCheckpoint func(string) error
	// profileHomesRoot is where this node creates the HOME of a profile that signs
	// in with the tool's own login and names no home (provider_profile.go). Empty
	// refuses such a profile: the engine user's own home is never handed out.
	profileHomesRoot string
	// toolLoginsRoot is where the tools' own logins live on this node
	// (<data-dir>/tool-logins, ToolLoginHome). Empty refuses an own-login profile
	// that names no homes; the engine user's own home is never used.
	toolLoginsRoot string
	// managedStopAdmissionTimeout is the configured T
	// (WithManagedStopAdmissionTimeout); zero selects the module default.
	managedStopAdmissionTimeout time.Duration

	mu     sync.Mutex
	cancel func()
}

var (
	_ sdk.Module       = (*Module)(nil)
	_ api.Module       = (*Module)(nil)
	_ api.DataConsumer = (*Module)(nil)
)

// New returns a sessions module with default windows, a fresh stream broker, and
// a DENY-CLOSED operate runtime (no runner / no credential source ⇒ no session
// launches). The composition root wires the concrete runner + WIF credential
// source via Option late-binds the governance gates. Existing callers that
// pass no options keep the observe-only behavior.
func New(opts ...Option) *Module { return newModule(nil, false, opts...) }

// NewWithDependencies creates an engine-composed sessions module. Boot assembles d
// before runtime Init; recovery ports must be complete before leadership starts.
// Each port is read-only once its first consumer can run; the pointer stays stable.
// Constructor options describe node configuration and standalone test adapters;
// they write the same dependency record, never a second set of runtime ports.
func NewWithDependencies(d *Dependencies, opts ...Option) *Module {
	return newModule(d, true, opts...)
}

func newModule(d *Dependencies, engineDependencies bool, opts ...Option) *Module {
	rt := newRuntimeState(d)
	d = rt.Dependencies
	m := &Module{Dependencies: d, engineDependencies: engineDependencies,
		clock: model.SystemClock{}, broker: newBroker(), activeWindow: defaultActiveWindow, idleWindow: defaultIdleWindow, rt: rt}
	for _, o := range opts {
		o(m)
	}
	d.normalize()
	return m
}

// Descriptor returns the module's self-description.
func (m *Module) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeModule,
		Title:       "Live operation & sessions",
		Description: "Tracks the live operation of each agent session (current action, tokens/cost, state, timeline) and streams it to the API.",
	}
}

// UseData receives the tenant-scoped data handle from the engine boot.
func (m *Module) UseData(d api.ModuleData) { m.Data = d }

// Init subscribes to the observation stream. Sessions cares about all three
// observation types: edges (a session's actions), cost samples (its live spend)
// and findings (its anti-evasion/health signals).
func (m *Module) Init(_ context.Context, host sdk.Host) error {
	m.log = host.Logger()
	m.publishObservation = host.Publish
	cancel, err := host.Subscribe(
		[]event.Type{event.TypeEdgeObserved, event.TypeCostSampled, event.TypeFindingReported},
		m.onEvent,
	)
	if err != nil {
		return err
	}
	m.cancel = cancel
	return nil
}

// Start launches active lifecycle checks when composition binds owner-standing
// checks or an emergency-stop sweep. Idle runs lose access without needing a tool
// call. A standalone observe-only module starts no sweep; the SSE broker stays lazy.
//
// Engine-composed modules refuse missing required ports before lifecycle work.
// Once validated, the sweep starts before best-effort approval recovery: an emergency stop reaches
// running sessions even when the recovery of waiting launches cannot run. Start
// never fails over that recovery (RecoverWaitingLaunchesForAllTenants).
func (m *Module) Start(ctx context.Context) error {
	if err := m.checkDependencies(); err != nil {
		return err
	}
	m.reportOptionalDependencies()

	m.rt.mu.Lock()
	m.rt.approvalWorkersStopped = false
	m.rt.mu.Unlock()
	if m.Data == nil && m.log != nil {
		m.log.Warn("sessions: started without a data handle; live operation will not persist")
	}
	m.startStopSweep()
	m.RecoverWaitingLaunchesForAllTenants(ctx)
	return nil
}

// RecoverWaitingLaunchesForAllTenants resumes watching launches awaiting approval,
// in every tenant the composition names (Dependencies.ApprovalRecoveryTenants). It is best
// effort: a tenant list or a tenant it cannot read is logged, the other tenants
// still recover, and the next start or leader promotion tries again. Composition
// calls it on promotion only after the module has started; Start also calls it.
func (m *Module) RecoverWaitingLaunchesForAllTenants(ctx context.Context) {
	if m.rt.ApprovalRecoveryTenants == nil {
		return
	}
	tenants, err := m.rt.ApprovalRecoveryTenants(ctx)
	if err != nil {
		m.warnf("sessions: cannot list the tenants whose launches wait for an approval; they resume at the next start or leader promotion",
			"err", redactErr(err))
		return
	}
	for _, tenant := range tenants {
		if err := m.RecoverWaitingLaunches(ctx, tenant); err != nil {
			m.warnf("sessions: cannot recover this tenant's launches that wait for an approval; they resume at the next start or leader promotion",
				"tenant", tenant.String(), "err", redactErr(err))
		}
	}
}

// Stop unsubscribes, terminates every supervised Claude Code process (their
// durable rows stay; a later op lazily reconciles an orphan, and resume recovers
// the conversation), and closes the broker, ending any live SSE streams.
func (m *Module) Stop(ctx context.Context) error {
	m.mu.Lock()
	cancel := m.cancel
	m.cancel = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if m.rt != nil {
		m.stopApprovalWorkers(ctx)
		m.stopStopSweep() // end the active kill-switch sweep before tearing down runs
		stopErr := m.stopAllRuns(ctx)
		m.broker.close()
		return stopErr
	}
	m.broker.close()
	return nil
}

// onEvent dispatches a delivered observation to the live-state updaters.
func (m *Module) onEvent(ctx context.Context, e event.Event) error {
	if m.Data == nil || e.SessionProjection {
		return nil
	}
	// B2: the HOST-stamped registration snapshot decides the row an observation
	// folds into (live_scope.go). It is read from the envelope the engine built,
	// never from the payload; a pushed collector envelope arrives with it nil.
	reg := e.SourceRegistration
	switch e.Type {
	case event.TypeEdgeObserved:
		if edge, ok := event.EdgeOf(e); ok {
			return m.foldEdge(ctx, e.Tenant, reg, edge)
		}
	case event.TypeCostSampled:
		if cost, ok := event.CostOf(e); ok {
			return m.foldCost(ctx, e.Tenant, reg, cost)
		}
	case event.TypeFindingReported:
		if f, ok := event.FindingOf(e); ok {
			return m.foldFinding(ctx, e.Tenant, reg, f)
		}
	}
	return nil
}

// tenantOf resolves an event's string tenant reference to a usable business
// TenantID, or false to skip (placeholder label or the system tenant).
func tenantOf(ref string) (model.TenantID, bool) {
	t, err := model.ParseTenantID(ref)
	if err != nil || t.IsZero() || t.IsSystem() {
		return "", false
	}
	return t, true
}
