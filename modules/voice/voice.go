// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package voice

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
const Name = "olivares.voice"

// Namespace is the module's store and API namespace.
const Namespace = "voice"

// Module permissions, granted by verb tier. Reading an internal design note (not shipped) is
// read-tier; setting a voice policy and OPENING a session are admin-tier (the open
// is additionally policy-gated default-deny and identity-governed).
const (
	permSessionRead  auth.Permission = "voice:session:read"
	permPolicyAdmin  auth.Permission = "voice:policy:admin"
	permSessionAdmin auth.Permission = "voice:session:admin"
)

// Option configures a Module at construction.
type Option func(*Module)

// WithClock overrides the module clock (tests inject a deterministic clock).
func WithClock(c model.Clock) Option { return func(m *Module) { m.clock = c } }

// WithApprovalGate wires the HITL gate. Without it, every open is denied.
func WithApprovalGate(g ApprovalGate) Option { return func(m *Module) { m.gate = g } }

// WithDispatcher wires the provider Realtime dispatcher. Without it, an
// approved open is recorded as declared-not-opened (the module never actuates).
func WithDispatcher(d Dispatcher) Option { return func(m *Module) { m.dispatch = d } }

// WithBudgetGate wires the FinOps (module XI) pre-flight budget gate (FIN-08).
// Without it, no budget ever denies an open (the opt-in default — see allowBudgetGate).
func WithBudgetGate(g BudgetGate) Option { return func(m *Module) { m.budgetGate = g } }

// WithStopGate wires the estate kill-switch pre-flight. Without it, no stop
// ever freezes an open (the composition root always wires the governance-backed
// gate; see allowStopGate).
func WithStopGate(g StopGate) Option { return func(m *Module) { m.stopGate = g } }

// WithCallController wires the realtime SIP call-control port. Without it, incoming
// calls cannot be accepted or rejected by this module (fail-closed).
func WithCallController(c CallController) Option { return func(m *Module) { m.callController = c } }

// WithSidebandAttacher wires the live-call observer sideband. Nil means accepted
// calls are governed but no sideband telemetry/cost/DLP observer is started.
func WithSidebandAttacher(a SidebandAttacher) Option {
	return func(m *Module) { m.sidebandAttacher = a }
}

// WithTranscriptClassifier wires the in-memory transcript DLP classifier.
func WithTranscriptClassifier(c TranscriptClassifier) Option {
	return func(m *Module) { m.transcriptClassifier = c }
}

// WithCallConfig sets the inbound call-plane tenant, cost attribution and observer
// lifecycle options.
func WithCallConfig(c CallConfig) Option { return func(m *Module) { m.callConfig = c } }

// Module is module XVI — voice & realtime agents. See doc.go for the bounded context
// and the hard minimal-data ban on audio/transcript content.
type Module struct {
	log                  *slog.Logger
	data                 api.ModuleData
	host                 sdk.Host
	clock                model.Clock
	gate                 ApprovalGate
	dispatch             Dispatcher
	budgetGate           BudgetGate
	stopGate             StopGate
	callController       CallController
	sidebandAttacher     SidebandAttacher
	transcriptClassifier TranscriptClassifier
	callConfig           CallConfig
	broker               *broker

	activeWindow time.Duration
	idleWindow   time.Duration

	mu           sync.Mutex
	cancel       func()
	callCancel   func()
	callWG       sync.WaitGroup
	replay       map[string]time.Time
	ungoverned   map[string]struct{}
	liveCalls    map[string]*liveCall
	unclassified map[string]struct{}
}

var (
	_ sdk.Module       = (*Module)(nil)
	_ api.Module       = (*Module)(nil)
	_ api.DataConsumer = (*Module)(nil)
)

// New returns a voice module with safe, deny-closed defaults: a deny gate and a
// fail-closed dispatcher. The composition root replaces them via options.
func New(opts ...Option) *Module {
	m := &Module{
		clock:          model.SystemClock{},
		gate:           denyGate{},
		dispatch:       unwiredDispatcher{},
		budgetGate:     allowBudgetGate{},
		stopGate:       allowStopGate{},
		callController: unwiredCallController{},
		broker:         newBroker(),
		activeWindow:   defaultActiveWindow,
		idleWindow:     defaultIdleWindow,
		replay:         map[string]time.Time{},
		ungoverned:     map[string]struct{}{},
		liveCalls:      map[string]*liveCall{},
		unclassified:   map[string]struct{}{},
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
		Title:       "Voice & realtime agents",
		Description: "Governs voice/realtime agents: who may open a voice session with which model under which policy (HITL-gated, audited, append-only-evidenced), and tracks session metadata (state, latency, turns, transcription metadata). Hard minimal-data line: never audio or transcript text. Never opens a media stream itself.",
	}
}

// UseData receives the least-privilege, tenant-parameterized data handle.
func (m *Module) UseData(d api.ModuleData) { m.data = d }

// Init subscribes to the module-owned voice telemetry stream (the deny-closed ingest
// seam) and keeps the host for publishing findings. It must not block.
func (m *Module) Init(_ context.Context, host sdk.Host) error {
	m.log = host.Logger()
	m.host = host
	cancel, err := host.Subscribe([]event.Type{TypeVoiceTelemetry}, m.onEvent)
	if err != nil {
		return err
	}
	m.cancel = cancel
	return nil
}

// Start has no background work. It warns once per un-wired seam so a plane that can
// declare but never govern-and-open — or that observes nothing — is VISIBLE.
func (m *Module) Start(context.Context) error {
	if m.log == nil {
		return nil
	}
	if m.data == nil {
		m.log.Warn("voice: started without a data handle; sessions, policies and decisions will not persist")
	}
	if _, ok := m.gate.(denyGate); ok {
		m.log.Warn("voice: no approval gate wired; every voice-session open will be DENIED by default")
	}
	if _, ok := m.dispatch.(unwiredDispatcher); ok {
		m.log.Warn("voice: no dispatcher wired (runtime); an approved open is declared, not actuated")
	}
	if _, ok := m.callController.(unwiredCallController); ok {
		m.log.Warn("voice: no realtime call controller wired; OpenAI SIP webhooks, if mounted, will refuse fail-closed")
	}
	m.startCallSweep()
	m.log.Info("voice: govern half (policy + open ledger) usable; the observe half is live when the OpenAI Realtime SIP call plane is configured, and otherwise stays honestly empty, never fabricated")
	return nil
}

// Stop unsubscribes and closes the broker. Idempotent.
func (m *Module) Stop(context.Context) error {
	m.mu.Lock()
	cancel := m.cancel
	m.cancel = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	m.stopCallObservers()
	m.broker.close()
	return nil
}

// onEvent dispatches a delivered voice-telemetry event to the session updater.
func (m *Module) onEvent(ctx context.Context, e event.Event) error {
	if m.data == nil {
		return nil
	}
	if vt, ok := parseTelemetry(e); ok {
		return m.onTelemetry(ctx, e.Tenant, vt)
	}
	return nil
}

func (m *Module) debugf(msg string, args ...any) {
	if m.log != nil {
		m.log.Debug(msg, args...)
	}
}

func (m *Module) errorf(msg string, args ...any) {
	if m.log != nil {
		m.log.Error(msg, args...)
	}
}
