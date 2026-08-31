// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
)

// Name is the module's globally unique identifier (the runtime registry key).
const Name = "olivares.orchestration"

// Namespace is the module's store and API namespace: its entities are
// "orchestration.<entity>" and its routes mount under /v1/m/orchestration/.
const Namespace = "orchestration"

// Module permissions, granted to the built-in roles by verb tier (viewer→read,
// editor→write, admin/owner→admin). Reading the comm graph and schedules is
// read-tier; declaring/retargeting a schedule is write-tier; FIRING a schedule is
// admin-tier — a second control on top of the mandatory HITL approval gate.
const (
	permGraphRead     auth.Permission = "orchestration:graph:read"
	permScheduleRead  auth.Permission = "orchestration:schedule:read"
	permScheduleWrite auth.Permission = "orchestration:schedule:write"
	permScheduleAdmin auth.Permission = "orchestration:schedule:admin"
	// DAG workflows: declaring/editing a graph is write-tier; RUNNING one
	// is admin-tier AND HITL-gated (the schedule-fire rule, applied to the DAG).
	permWorkflowRead  auth.Permission = "orchestration:workflow:read"
	permWorkflowWrite auth.Permission = "orchestration:workflow:write"
	permWorkflowAdmin auth.Permission = "orchestration:workflow:admin"
)

// Option configures a Module at construction.
type Option func(*Module)

// WithClock overrides the module clock (tests inject a deterministic clock).
func WithClock(c model.Clock) Option { return func(m *Module) { m.clock = c } }

// WithApprovalGate wires the HITL gate. Without it, every fire is denied
// (deny-by-default).
func WithApprovalGate(g ApprovalGate) Option { return func(m *Module) { m.gate = g } }

// WithDispatcher wires the runtime/deploy dispatcher. Without it, an approved fire
// is recorded as declared-not-fired (the control plane never actuates here).
func WithDispatcher(d Dispatcher) Option { return func(m *Module) { m.dispatch = d } }

// WithBudgetGate wires the FinOps (module XI) pre-flight budget gate (FIN-08).
// Without it, no budget ever denies a fire (the opt-in default — see allowBudgetGate).
func WithBudgetGate(g BudgetGate) Option { return func(m *Module) { m.budgetGate = g } }

// WithStopGate wires the estate kill-switch pre-flight. Without it, no stop
// ever freezes a fire (the composition root always wires the governance-backed
// gate; see allowStopGate).
func WithStopGate(g StopGate) Option { return func(m *Module) { m.stopGate = g } }

// WithNotifyTester wires the notify-test workflow-step actuator. Without
// it, a notify-test step is recorded as declared, never pretended sent.
func WithNotifyTester(n NotifyTester) Option { return func(m *Module) { m.notifyTest = n } }

// WithTargetBindingKey wires the dedicated target-binding HMAC key provider
// (D-06). Without it, every workflow acting step BLOCKS (deny-closed) —
// the target fingerprint cannot be anchored, so it can never be verified. The
// composition root wires a shared, KMS/secret-store-backed key (never an
// ephemeral per-node key, which would break HA/restart).
func WithTargetBindingKey(k MACKeyProvider) Option { return func(m *Module) { m.macKey = k } }

// WithDispatcherGeneration wires the operator dispatcher-config generation seam
// (D-06): including the current generation in the target fingerprint makes
// an operator config change (image/command/URL/skill/headers) void an approval.
func WithDispatcherGeneration(g DispatcherGeneration) Option {
	return func(m *Module) { m.dispatchGen = g }
}

// WithRoutinePolicyGate wires the routine-governance policy seam.
// Without it no routine policy constrains anything (openRoutinePolicyGate) —
// the composition root always wires the governance-backed gate.
func WithRoutinePolicyGate(g RoutinePolicyGate) Option {
	return func(m *Module) { m.routineGate = g }
}

// WithTargetEnvironmentResolver wires the authoritative actuation-environment
// resolver, built from the same dispatcher snapshot Fire selects from.
// Without it a blocked-environment policy cannot be satisfied and denies closed.
func WithTargetEnvironmentResolver(r TargetEnvironmentResolver) Option {
	return func(m *Module) { m.targetEnv = r }
}

// WithWorkflowLimits bounds the per-tenant workflow count and the per-workflow
// step count (composition-root env-configurable). Non-positive values
// keep the defaults.
func WithWorkflowLimits(workflows, steps int) Option {
	return func(m *Module) {
		if workflows > 0 {
			m.maxWorkflows = workflows
		}
		if steps > 0 {
			m.maxWorkflowSteps = steps
		}
	}
}

func WithWorkflowWorkControl(control WorkflowWorkControl) Option {
	return func(m *Module) { m.workflowWork = control }
}

func WithWorkflowRuntimeControl(control WorkflowRuntimeControl) Option {
	return func(m *Module) { m.workflowRuntime = control }
}

func WithWorkflowMessageControl(control WorkflowMessageControl) Option {
	return func(m *Module) { m.workflowMessage = control }
}

func WithWorkflowHandoffControl(control WorkflowHandoffControl) Option {
	return func(m *Module) { m.workflowHandoff = control }
}

func WithWorkflowAckReader(reader WorkflowAckReader) Option {
	return func(m *Module) { m.workflowAck = reader }
}

func WithWorkflowBindingControl(control WorkflowBindingControl) Option {
	return func(m *Module) { m.workflowBinding = control }
}

// WithRemoteWorkExecutor wires the K5 governed remote-work lifecycle. Unlike
// the K4 binding placeholder, this seam owns the complete plan/test/start/
// observe/cancel contract and its durable ProtocolBinding receipts.
func WithRemoteWorkExecutor(executor RemoteWorkExecutor) Option {
	return func(m *Module) { m.remoteWork = executor }
}

// Module is module IV — communication & orchestration. See doc.go for the bounded
// context, the minimal-data red line and the deny-closed defaults of its seams.
type Module struct {
	log             *slog.Logger
	data            api.ModuleData
	host            sdk.Host
	clock           model.Clock
	gate            ApprovalGate
	dispatch        Dispatcher
	budgetGate      BudgetGate
	stopGate        StopGate
	notifyTest      NotifyTester
	macKey          MACKeyProvider
	dispatchGen     DispatcherGeneration
	routineGate     RoutinePolicyGate
	targetEnv       TargetEnvironmentResolver
	workflowWork    WorkflowWorkControl
	workflowRuntime WorkflowRuntimeControl
	workflowMessage WorkflowMessageControl
	workflowHandoff WorkflowHandoffControl
	workflowAck     WorkflowAckReader
	workflowBinding WorkflowBindingControl
	remoteWork      RemoteWorkExecutor
	broker          *broker

	activeWindow     time.Duration
	idleWindow       time.Duration
	maxWorkflows     int
	maxWorkflowSteps int

	mu     sync.Mutex
	cancel func()
}

// Compile-time proof the module satisfies the SDK lifecycle, the API route/
// permission seam and the data-consumer seam. RegisterSchema (the engine-side
// SchemaProvider seam) is structural and verified by the runtime at boot/test.
var (
	_ sdk.Module       = (*Module)(nil)
	_ api.Module       = (*Module)(nil)
	_ api.DataConsumer = (*Module)(nil)
)

// New returns an orchestration module with safe, deny-closed defaults: a deny gate
// and a fail-closed dispatcher. The composition root replaces them with real
// adapters via options.
func New(opts ...Option) *Module {
	m := &Module{
		clock:            model.SystemClock{},
		gate:             denyGate{},
		dispatch:         unwiredDispatcher{},
		budgetGate:       allowBudgetGate{},
		stopGate:         allowStopGate{},
		notifyTest:       unwiredNotifyTester{},
		macKey:           unwiredMACKey{},
		dispatchGen:      unwiredDispatcherGeneration{},
		routineGate:      openRoutinePolicyGate{},
		targetEnv:        unwiredTargetEnvironment{},
		workflowWork:     unwiredWorkflowWorkControl{},
		workflowRuntime:  unwiredWorkflowRuntimeControl{},
		workflowMessage:  unwiredWorkflowMessageControl{},
		workflowHandoff:  unwiredWorkflowHandoffControl{},
		workflowAck:      unwiredWorkflowAckReader{},
		workflowBinding:  unwiredWorkflowBindingControl{},
		remoteWork:       unwiredRemoteWorkExecutor{},
		broker:           newBroker(),
		activeWindow:     defaultActiveWindow,
		idleWindow:       defaultIdleWindow,
		maxWorkflows:     defaultMaxWfs,
		maxWorkflowSteps: defaultMaxSteps,
	}
	for _, o := range opts {
		o(m)
	}
	if m.workflowWork == nil {
		m.workflowWork = unwiredWorkflowWorkControl{}
	}
	if m.workflowRuntime == nil {
		m.workflowRuntime = unwiredWorkflowRuntimeControl{}
	}
	if m.workflowMessage == nil {
		m.workflowMessage = unwiredWorkflowMessageControl{}
	}
	if m.workflowHandoff == nil {
		m.workflowHandoff = unwiredWorkflowHandoffControl{}
	}
	if m.workflowAck == nil {
		m.workflowAck = unwiredWorkflowAckReader{}
	}
	if m.workflowBinding == nil {
		m.workflowBinding = unwiredWorkflowBindingControl{}
	}
	if m.remoteWork == nil {
		m.remoteWork = unwiredRemoteWorkExecutor{}
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
		Title:       "Communication & orchestration",
		Description: "Observes and governs how agents coordinate: derives the live communication/delegation graph (A2A, supervisor-worker, swarms) from observed signals, and governs scheduled/autonomous agents — firing is a two-phase, HITL-gated, audited, append-only-evidenced privileged action that never actuates in-module. Minimal data: relations and metadata, never message payloads.",
	}
}

// UseData receives the least-privilege, tenant-parameterized data handle from the
// engine boot (the api.DataConsumer seam), before Start.
func (m *Module) UseData(d api.ModuleData) { m.data = d }

// Init subscribes to the observation stream. Orchestration derives the whole
// comm/delegation graph from edge.observed (the only live signal today) and uses
// those same edges as the liveness that clears a schedule's cadence-miss. It does
// not consume findings: node health is derived from the cadence-miss it owns and
// can prove, not from re-indexing another module's findings. It must not block.
func (m *Module) Init(_ context.Context, host sdk.Host) error {
	m.log = host.Logger()
	m.host = host
	cancel, err := host.Subscribe([]event.Type{event.TypeEdgeObserved}, m.onEvent)
	if err != nil {
		return err
	}
	m.cancel = cancel
	return nil
}

// Start has no background work (the module is event-driven and the SSE broker is
// lazy). It warns once per un-wired seam so a plane that can declare but never
// govern-and-fire — or that would deny every fire — is VISIBLE.
func (m *Module) Start(context.Context) error {
	if m.log == nil {
		return nil
	}
	if m.data == nil {
		m.log.Warn("orchestration: started without a data handle; relations, schedules and decisions will not persist")
	}
	if _, ok := m.gate.(denyGate); ok {
		m.log.Warn("orchestration: no approval gate wired; every scheduled-agent fire will be DENIED by default")
	}
	if _, ok := m.dispatch.(unwiredDispatcher); ok {
		m.log.Warn("orchestration: no dispatcher wired (runtime); an approved fire is declared, not actuated")
	}
	if _, ok := m.workflowWork.(unwiredWorkflowWorkControl); ok {
		m.log.Warn("orchestration: workflow work control is unwired; K4 work steps fail closed")
	}
	if _, ok := m.workflowRuntime.(unwiredWorkflowRuntimeControl); ok {
		m.log.Warn("orchestration: workflow runtime control is unwired; session-launch fails closed")
	}
	if _, ok := m.workflowMessage.(unwiredWorkflowMessageControl); ok {
		m.log.Warn("orchestration: workflow message control is unwired; work-message fails closed")
	}
	if _, ok := m.workflowHandoff.(unwiredWorkflowHandoffControl); ok {
		m.log.Warn("orchestration: workflow handoff control is unwired; acknowledged assignment and work-handoff fail closed")
	}
	if _, ok := m.workflowAck.(unwiredWorkflowAckReader); ok {
		m.log.Warn("orchestration: workflow ack reader is unwired; work-wait-ack fails closed")
	}
	if _, ok := m.remoteWork.(unwiredRemoteWorkExecutor); ok {
		m.log.Warn("orchestration: remote work executor is unwired; bound cancel and work-reconcile fail closed")
	}
	return nil
}

// Stop unsubscribes and closes the broker, ending any live SSE streams. Idempotent.
func (m *Module) Stop(context.Context) error {
	m.mu.Lock()
	cancel := m.cancel
	m.cancel = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	m.broker.close()
	return nil
}

// onEvent dispatches a delivered observation to the comm-graph updaters.
func (m *Module) onEvent(ctx context.Context, e event.Event) error {
	if m.data == nil {
		return nil
	}
	if e.Type == event.TypeEdgeObserved {
		if edge, ok := event.EdgeOf(e); ok {
			return m.onEdge(ctx, e.Tenant, edge)
		}
	}
	return nil
}

// debugf logs at debug level if a logger is set.
func (m *Module) debugf(msg string, args ...any) {
	if m.log != nil {
		m.log.Debug(msg, args...)
	}
}

// errorf logs at error level if a logger is set. Used where a best-effort ledger
// write fails: the primary outcome still returns, but a lost audit/decision record
// is an integrity event worth surfacing rather than swallowing (docs/SECURITY-HARDENING.md).
func (m *Module) errorf(msg string, args ...any) {
	if m.log != nil {
		m.log.Error(msg, args...)
	}
}
