// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

import (
	"context"
	"errors"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk/event"
)

// workflow_ports.go — the seams the workflow engine depends on beyond
// the module's existing gates, plus the FIXED event vocabulary an
// eventing-emit step is allowed to publish.

// TypeWorkflowSignal is the ONLY event type an eventing-emit step publishes.
// The type is fixed by the module, never taken from step config, so an
// editor-tier user can never forge a first-party event (edge.observed,
// finding.reported, …) into another module's ingestion. Consumers subscribe to
// it like any cataloged type; the payload is WorkflowSignal (JSON on the wire,
// the module-defined-type contract of sdk/event).
const TypeWorkflowSignal event.Type = "workflow.signal"

// WorkflowSignal is the minimal-data payload of a workflow.signal event:
// references and a bounded operator label — never step config, never a secret.
type WorkflowSignal struct {
	WorkflowRef string `json:"workflow_ref"`
	RunRef      string `json:"run_ref"`
	StepRef     string `json:"step_ref"`
	Label       string `json:"label"`
}

// NotifyTester is the notify-test actuation seam, expressed in this module's
// own terms (modules never import each other). The real adapter (composition
// root) bridges to the notify module's synthetic route test — the SAME
// claim-then-send, ledger-recorded path as the manual admin verb, so a
// workflow-driven test is never less evidenced than a human-driven one.
// Tier parity: the manual verb is admin-tier; a workflow run is admin-tier
// AND HITL-approved, so the seam never lowers the privilege bar.
type NotifyTester interface {
	// Test sends the synthetic test through the tenant's route routeRef and
	// reports the delivery status/detail.
	Test(ctx context.Context, tenant model.TenantID, routeRef string) (status, detail string, err error)
	// LookupRoute resolves a route reference to its operator-facing name.
	// Authoring uses it so a notify-test step is checked like a schedule-fire
	// step is, instead of accepting any opaque string and only discovering the
	// mistake at run time — and so the dry-run shows a human the route's NAME
	// rather than a UUID they cannot judge.
	//
	// ok=false means the route does not exist. An ERROR means the seam could
	// not answer; the caller then skips validation rather than rejecting, since
	// an unwired or unreachable notifier is not evidence that the operator's
	// reference is wrong.
	LookupRoute(ctx context.Context, tenant model.TenantID, routeRef string) (name string, ok bool, err error)
	// RouteFingerprint returns an OPAQUE digest of the route's effect-bearing
	// target (its destination + any operator-owned delivery config), so a
	// workflow run can freeze it at approval and BLOCK on any change at execution
	// (D-06). ok=false means the route does not exist; an ERROR means the
	// seam could not answer (the acting step then BLOCKS, deny-closed — a target
	// it cannot verify is never actuated). It never returns a raw destination or
	// secret — only the opaque fingerprint.
	RouteFingerprint(ctx context.Context, tenant model.TenantID, routeRef string) (fingerprint string, ok bool, err error)
	// TestBound is the ATOMIC delivery seam (hole c1): it re-reads the route
	// ONCE, refuses with errRouteBindingChanged unless its current fingerprint
	// equals expectedFingerprint (the value frozen at approval), and delivers THAT
	// single verified read — so a route re-pointed between the caller's check and
	// the send can never divert the delivery. operationID rides along for
	// receiver-side dedup. errNoNotifyTester ⇒ declared (unwired).
	TestBound(ctx context.Context, tenant model.TenantID, routeRef, expectedFingerprint, operationID string) (status, detail string, err error)
}

// errNoNotifyTester is the deny-closed default's sentinel: the step is
// recorded as DECLARED (approved, not actuated), never pretended sent.
var errNoNotifyTester = errors.New("orchestration: no notify actuator wired")

// ErrRouteBindingChanged is TestBound's refusal when the route's current
// fingerprint no longer matches the one frozen at approval (a re-point). It is
// exported so the composition-root adapter can surface it across the seam.
var ErrRouteBindingChanged = errors.New("orchestration: notify route changed since approval")

// unwiredNotifyTester is the deny-closed default.
type unwiredNotifyTester struct{}

func (unwiredNotifyTester) Test(context.Context, model.TenantID, string) (string, string, error) {
	return "", "", errNoNotifyTester
}

func (unwiredNotifyTester) LookupRoute(context.Context, model.TenantID, string) (string, bool, error) {
	return "", false, errNoNotifyTester
}

func (unwiredNotifyTester) RouteFingerprint(context.Context, model.TenantID, string) (string, bool, error) {
	return "", false, errNoNotifyTester
}

func (unwiredNotifyTester) TestBound(context.Context, model.TenantID, string, string, string) (string, string, error) {
	return "", "", errNoNotifyTester
}
