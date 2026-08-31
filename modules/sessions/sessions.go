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
	log          *slog.Logger
	data         api.ModuleData
	recoveryData api.ModuleData
	clock        model.Clock
	broker       *broker

	activeWindow time.Duration
	idleWindow   time.Duration

	// rt is the OPERATE runtime (governed Claude Code session lifecycle):
	// the deny-closed launch seams and the in-memory registry of live processes.
	rt *runtimeState

	// K1 work-kernel ports are late-bound by the composition root. Nil is a
	// meaningful deny-closed state: identity/content cannot be asserted and an
	// outbox event cannot be called published.
	workIdentity  WorkIdentityResolver
	workContent   WorkContentGuard
	workEventSink WorkEventSink
	workAuthz     WorkAuthorizer
	// protocolBindingReconciler is the K5 composition seam for authenticated
	// peer reads. Nil is an explicit OFF state: REST reconciliation fails
	// closed instead of accepting a client-supplied remote observation.
	protocolBindingReconciler ProtocolBindingRemoteReconciler
	// protocolBindingSpecValidators are server-owned K5 capability witnesses,
	// keyed by protocol. Browser/CLI input is never trusted as validation.
	protocolBindingSpecValidators map[BindingProtocol][]ProtocolBindingSpecValidator
	protocolLocalResourceResolver ProtocolLocalResourceResolver

	// K3 communication ports are late-bound after Store.Open and core/auth
	// composition. Nil readiness ports are meaningful OFF witnesses and the
	// effective gate in communication_readiness.go requires its complete set
	// simultaneously. The preparatory authority-source bundle remains outside
	// that gate until the legacy service paths are migrated to consume it.
	communicationSealer              CommunicationContentSealer
	communicationAuthoritySources    *communicationRequestAuthoritySources
	communicationDirectoryResolver   DirectorySnapshotResolver
	communicationAudienceAttestor    PublicationAudienceAttestor
	communicationGrantClosure        ChannelGrantSubjectClosureResolver
	communicationReadAuthorizer      CoreEntityReadAuthorizer
	communicationOperationAuthorizer CoreEntityOperationAuthorizer
	communicationGuardData           communicationGuardReconciliationData
	communicationStoreReadiness      CommunicationStoreReadinessWitness
	communicationPumpReadiness       CommunicationPumpReadinessWitness

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
func New(opts ...Option) *Module {
	m := &Module{
		clock:        model.SystemClock{},
		broker:       newBroker(),
		activeWindow: defaultActiveWindow,
		idleWindow:   defaultIdleWindow,
		rt:           newRuntimeState(),
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
		Title:       "Live operation & sessions",
		Description: "Tracks the live operation of each agent session (current action, tokens/cost, state, timeline) and streams it to the API.",
	}
}

// UseData receives the tenant-scoped data handle from the engine boot.
func (m *Module) UseData(d api.ModuleData) { m.data = d }

// UseRuntimeCredentialRecoveryData binds the custody-only data path used by
// leader promotion to withdraw durable process credentials. Composition gives
// it the residency-guarded store before service suspension is wrapped: a
// suspended tenant must still lose authority, while a foreign-region tenant
// must remain inaccessible. Ordinary runtime/API work always uses UseData.
func (m *Module) UseRuntimeCredentialRecoveryData(d api.ModuleData) {
	m.recoveryData = d
}

// Init subscribes to the observation stream. Sessions cares about all three
// observation types: edges (a session's actions), cost samples (its live spend)
// and findings (its anti-evasion/health signals).
func (m *Module) Init(_ context.Context, host sdk.Host) error {
	m.log = host.Logger()
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

// Start launches the OPERATE background work: the active kill-switch sweep,
// which terminates running sessions that fall under an emergency stop. It is enabled
// only when the composition root wired a sweep interval (WithKillSwitchSweep) — the
// observe-only / standalone module starts no goroutine. The SSE broker stays lazy.
func (m *Module) Start(context.Context) error {
	if m.data == nil && m.log != nil {
		m.log.Warn("sessions: started without a data handle; live operation will not persist")
	}
	m.startStopSweep()
	return nil
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
	if m.data == nil {
		return nil
	}
	switch e.Type {
	case event.TypeEdgeObserved:
		if edge, ok := event.EdgeOf(e); ok {
			return m.onEdge(ctx, e.Tenant, edge)
		}
	case event.TypeCostSampled:
		if cost, ok := event.CostOf(e); ok {
			return m.onCost(ctx, e.Tenant, cost)
		}
	case event.TypeFindingReported:
		if f, ok := event.FindingOf(e); ok {
			return m.onFinding(ctx, e.Tenant, f)
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
