// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package liveingest

import (
	"context"
	"log/slog"
	"sync"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
)

// Name is the module's globally unique identifier (the runtime registry key).
const Name = "olivares.liveingest"

// Namespace is the module's API namespace. The module exposes NO HTTP routes (it
// is a pure in-process producer); it implements api.Module only to join the
// canonical module set, and reserves /v1/m/liveingest for a future read-only
// status route. Its honest posture is surfaced in the boot log.
const Namespace = "liveingest"

// Option configures a Module at construction.
type Option func(*Module)

// WithObservedRefInspection turns ON the observed-text producer. It is the
// operator's explicit opt-in: with it OFF (the default) the module is deny-closed
// and publishes NO guardrail.observed — the only observed text available in-process
// is the tool-ARGUMENT references the connector already redacts onto edge.observed,
// and inspecting them is a detective control the operator must consciously enable
// (it is never raw content and never widens the connector's capture; docs/SECURITY-HARDENING.md).
// The composition root wires it from OLIVARES_LIVEINGEST_INSPECT_OBSERVED_REFS.
func WithObservedRefInspection(on bool) Option {
	return func(m *Module) { m.inspectRefs = on }
}

// Module is module XXIV's live-tap producer half. See doc.go for the architectural
// reason it exists (the out-of-process connector cannot Host.Publish a detective
// event) and the deny-closed, minimal-data line it holds.
type Module struct {
	log  *slog.Logger
	host sdk.Host

	// inspectRefs gates the observed-text half. Default false (deny-closed).
	inspectRefs bool
	// leader, when set (UseLeadership), gates the observed-ref DERIVATION on HA
	// leadership. The module is a stateless republisher with no store write to
	// fence it: without this gate, every node receiving an edge over the
	// NATS bridge would derive its own guardrail.observed (fresh uuid each) —
	// N× duplicate security findings per edge, cluster-wide. nil = derive
	// always (single-node, tests).
	leader func() bool

	mu     sync.Mutex
	cancel func()
}

// Compile-time proof the module satisfies the SDK lifecycle and the API module
// seam (with no routes). It is NOT a DataConsumer and NOT a SchemaProvider: it
// owns no tables and never touches the store — it only publishes onto the bus.
var (
	_ sdk.Module = (*Module)(nil)
	_ api.Module = (*Module)(nil)
)

// New returns a liveingest module. It is deny-closed by default: with no options
// the observed-text half is OFF (publishes nothing) and the voice probe is
// dormant — both empty halves are logged at Start, never a silent no-op.
func New(opts ...Option) *Module {
	m := &Module{}
	for _, o := range opts {
		o(m)
	}
	return m
}

// UseLeadership late-binds the HA leadership predicate (store.Leader().Active;
// the store opens after modules are constructed). See Module.leader for why
// the stateless derivation must be leader-gated under the distributed bus.
func (m *Module) UseLeadership(active func() bool) { m.leader = active }

// Descriptor returns the module's self-description.
func (m *Module) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeModule,
		Title:       "Live-tap producers",
		Description: "In-process producer of the detective bus events a SourceConnector cannot emit: redacted observed agent text as guardrail.observed and the allow-listed voice telemetry probe. Deny-closed and minimal-data: no raw payload ever reaches the bus, and every empty half is visible, never a silent no-op.",
	}
}

// APINamespace satisfies api.Module. The module exposes no routes.
func (m *Module) APINamespace() string { return Namespace }

// APIRoutes mounts no routes: liveingest is a pure in-process producer. The seam
// exists so the module joins the canonical module set; its posture is surfaced in
// the boot log (Start), the established pattern for an honest empty half.
func (m *Module) APIRoutes(api.RouteRegistrar) {}

// Permissions declares no permissions (no routes).
func (m *Module) Permissions() []auth.Permission { return nil }

// Init keeps the host for Publish and, when observed-text inspection is opted in,
// subscribes to the connector's edge.observed stream (the only in-process source of
// observed tool-argument text). With inspection OFF it subscribes to nothing: there
// is no observed text to produce, and the empty half is logged at Start. It must not
// block (the SDK lifecycle contract).
func (m *Module) Init(_ context.Context, host sdk.Host) error {
	m.log = host.Logger()
	m.host = host
	if !m.inspectRefs {
		return nil
	}
	cancel, err := host.Subscribe([]event.Type{event.TypeEdgeObserved}, m.onEdge)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.cancel = cancel
	m.mu.Unlock()
	return nil
}

// Start has no background work (the module is event-driven). It logs the honest
// posture of every half so a plane that produces nothing on a given tap is VISIBLE,
// never a silent no-op: which detective taps are live vs deny-closed,
// and that the voice probe is dormant without a realtime backend.
func (m *Module) Start(context.Context) error {
	if m.log == nil {
		return nil
	}
	if m.inspectRefs {
		m.log.Info("liveingest: Observed-text producer ON; publishing redacted tool_args references from edge.observed as guardrail.observed for the security detector chain (opt-in; minimal-data — already-redacted refs only, never raw content)")
	} else {
		m.log.Info("liveingest: Observed-text producer OFF (deny-closed default); no guardrail.observed is published — the security detect half is empty until an operator opts in (OLIVARES_LIVEINGEST_INSPECT_OBSERVED_REFS=1). Deeper content surfaces (prompt/output/tool bodies) are not available in-process under the out-of-process connector + frozen wire")
	}
	m.log.Info("liveingest: voice telemetry observe seam is live when the OpenAI Realtime SIP call plane is configured; otherwise no backend publishes voice.telemetry.observed and the observe half stays honestly empty, never fabricated")
	return nil
}

// Stop unsubscribes. Idempotent; the runtime also releases the subscription.
func (m *Module) Stop(context.Context) error {
	m.mu.Lock()
	cancel := m.cancel
	m.cancel = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

// onEdge is the edge.observed subscriber (only registered when observed-text
// inspection is opted in). It dispatches to the producer — on the HA
// leader only (see Module.leader): the leader sees every node's edges over the
// bridge, so deriving there is exactly-once cluster-wide; a standby deriving
// too would publish duplicates with fresh ids that nothing dedupes.
func (m *Module) onEdge(ctx context.Context, e event.Event) error {
	if m.leader != nil && !m.leader() {
		return nil
	}
	edge, ok := event.EdgeOf(e)
	if !ok {
		return nil
	}
	return m.onObservedEdge(ctx, e.Tenant, edge)
}
