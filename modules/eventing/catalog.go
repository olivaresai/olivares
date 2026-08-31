// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package eventing

import (
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/sdk/event"
)

// Stability tiers for public event types, aligned with the API stability
// policy (stable = 24-month deprecation→sunset window, beta = 12-month; the
// windows bind from GA — docs-site reference/api-stability.md). The hand-
// maintained AsyncAPI document (docs-site/public/asyncapi/asyncapi.yaml) and
// the rendered reference (reference/events.mdx) mirror this table; if either
// changes, both must move together.
const (
	// StabilityStable marks an event type under the 24-month stability window.
	StabilityStable = "stable"
	// StabilityBeta marks an event type under the 12-month window; its payload
	// may still gain fields (never lose them silently).
	StabilityBeta = "beta"
)

// EventTypeInfo is one entry of the public event-type catalog: a subscribable
// type, its stability tier, and the permission a subscription's role must hold
// to RECEIVE it (the deny-closed per-event RBAC filter, evaluated through the
// full RBAC+ABAC pipeline before every delivery attempt).
type EventTypeInfo struct {
	// Type is the subscribable event type.
	Type event.Type
	// Stability is StabilityStable or StabilityBeta.
	Stability string
	// Permission gates receiving this type. The mapping mirrors the type's
	// read surface in the product API (see catalog below).
	Permission auth.Permission
	// Description is a short, non-sensitive summary for the catalog endpoint.
	Description string
	// Internal marks a type that is NOT captured from the in-proc bus and so is
	// never subscribed there: it enters the platform through a durable intake
	// instead (audit.recorded through IngestAudit; work.* through IngestDurable). It
	// is still a first-class cataloged type — subscribable, deny-closed RBAC-gated
	// and replayable — but catalogTypes() omits it from the bus subscription set so
	// the platform never listens for an event no component publishes.
	Internal bool
}

// catalog is the single in-code table of PUBLIC event types (the routeDeprecations
// pattern from core/api/stability.go: one auditable declaration drives the API,
// the validation and the docs). A type absent from this table cannot be
// subscribed to, is never captured, and is never delivered — deny-closed.
//
// The permission mapping mirrors each type's existing read surface:
//   - edge.observed → accessgraph:read — the R/RW map is a privileged read
//     (editor+, core/auth privilegedReadPerms), so its event stream is too.
//   - cost.sampled → finops:spend:read — the de-facto cost read surface
//     (module verb tier, viewer+).
//   - metric.sampled → adoption:developer:read — the raw sample names its
//     subject (a developer ref in the Claude Code adoption path), so it
//     rides the privileged drill-down surface (editor+), never the viewer-tier
//     aggregate.
//   - finding.reported → security:finding:read (viewer+).
//   - guardrail.observed → security:observed:read — privileged (editor+):
//     the payload is redacted OBSERVED AGENT TEXT, the most content-like fact
//     on the bus.
//   - approval.requested → governance:approval:read (viewer+).
//   - approval.resolved → governance:approval:read (viewer+).
//   - policy.changed → governance:policy:read (viewer+).
//   - work.message.*, work.protocol.reply.available,
//     work.protocol.message.received and work.handoff.carrier.available →
//     sessions:message:read — these facts identify Message/Delivery carriers.
//   - work.decision.request.* → sessions:decision-request:read.
//   - work.handoff.* → sessions:handoff:read.
//   - work.binding.* → sessions:work:read — these are ordinary WorkItem
//     aggregate facts; ProtocolBinding has no separate public read surface.
var catalog = []EventTypeInfo{
	{
		Type:        event.TypeEdgeObserved,
		Stability:   StabilityStable,
		Permission:  "accessgraph:read",
		Description: "An origin (agent/identity/session) was observed touching a resource, classified read/write.",
	},
	{
		Type:        event.TypeCostSampled,
		Stability:   StabilityStable,
		Permission:  "finops:spend:read",
		Description: "A model/provider usage-cost fact: token counts plus a billed-or-estimated money figure.",
	},
	{
		// connectors emit metric.sampled (claude, claude-api,
		// claude-apps-gateway, vertex) but the type was never cataloged, so it
		// was silently unsubscribable (deny-closed). Its read surface is the
		// Adoption drill-down: the raw sample names its subject, which in
		// the Claude Code path is a developer ref — privileged (editor+ via
		// privilegedReadPerms), NOT the viewer-tier team/org aggregate.
		Type:        event.TypeMetricSampled,
		Stability:   StabilityBeta,
		Permission:  "adoption:developer:read",
		Description: "A usage/productivity metric sample (name, integer value, unit, subject). The subject may be a developer ref — privileged: editor+ only.",
	},
	{
		Type:        event.TypeFindingReported,
		Stability:   StabilityStable,
		Permission:  "security:finding:read",
		Description: "A guardrail/red-team/forensic finding (hash of redacted detail, never the raw detail).",
	},
	{
		Type:        event.TypeGuardrailObserved,
		Stability:   StabilityBeta,
		Permission:  "security:observed:read",
		Description: "A redacted, bounded excerpt of observed agent text (detective input). Privileged: editor+ only.",
	},
	{
		Type:        event.TypeApprovalRequested,
		Stability:   StabilityBeta,
		Permission:  "governance:approval:read",
		Description: "A pending approval was opened and awaits decision (identifiers and decision parameters only).",
	},
	{
		Type:        event.TypeApprovalResolved,
		Stability:   StabilityBeta,
		Permission:  "governance:approval:read",
		Description: "A pending approval reached a terminal outcome (identifiers and decision parameters only).",
	},
	{
		Type:        event.TypePolicyChanged,
		Stability:   StabilityBeta,
		Permission:  "governance:policy:read",
		Description: "A governance policy (abac/approval) was created, updated or deleted (id, kind, op and enabled flag only).",
	},
	{
		// the FIXED signal a DAG-workflow eventing-emit step publishes
		// (modules/orchestration). The TYPE is module-fixed — step config only
		// carries a bounded label — so a workflow author can never forge a
		// first-party event into another module's ingestion. Payload: workflow/
		// run/step refs + the label (module-defined JSON, minimal data). Read
		// surface: the workflow timeline (orchestration:workflow:read, viewer+).
		Type:        typeWorkflowSignal,
		Stability:   StabilityBeta,
		Permission:  "orchestration:workflow:read",
		Description: "A DAG-workflow eventing-emit step ran: workflow/run/step references plus a bounded operator label.",
	},
	{
		Type:        typeWorkItemCreated,
		Stability:   StabilityBeta,
		Permission:  "sessions:work:read",
		Description: "A durable work item was created (command/result references and resulting aggregate state only).",
		Internal:    true,
	},
	{
		Type:        typeWorkItemTransitioned,
		Stability:   StabilityBeta,
		Permission:  "sessions:work:read",
		Description: "A durable work item was updated, archived or completed a governed state transition.",
		Internal:    true,
	},
	{
		Type:        typeWorkOwnerChanged,
		Stability:   StabilityBeta,
		Permission:  "sessions:work:read",
		Description: "A work item's canonical owner or ownership epoch changed.",
		Internal:    true,
	},
	{
		Type:        typeWorkDependencyChanged,
		Stability:   StabilityBeta,
		Permission:  "sessions:work:read",
		Description: "A durable work dependency was added, reactivated or tombstoned.",
		Internal:    true,
	},
	{
		Type:        typeWorkAcceptanceChanged,
		Stability:   StabilityBeta,
		Permission:  "sessions:work:read",
		Description: "A work acceptance criterion was created, updated, evaluated or waived.",
		Internal:    true,
	},
	{
		Type:        typeWorkMessageAvailable,
		Stability:   StabilityBeta,
		Permission:  "sessions:message:read",
		Description: "A Message carrier became available; its bounded fact identifies the Message and initial delivery/ack state without content.",
		Internal:    true,
	},
	{
		Type:        typeWorkMessageAcknowledged,
		Stability:   StabilityBeta,
		Permission:  "sessions:message:read",
		Description: "A Message Delivery was explicitly acknowledged, including whether the append-only Ack was late.",
		Internal:    true,
	},
	{
		Type:        typeWorkMessageRetracted,
		Stability:   StabilityBeta,
		Permission:  "sessions:message:read",
		Description: "A Message was retracted and its affected carrier lifecycle was updated atomically.",
		Internal:    true,
	},
	{
		Type:        typeWorkMessageExpired,
		Stability:   StabilityBeta,
		Permission:  "sessions:message:read",
		Description: "A Message reached its expiry boundary and its affected carrier lifecycle was updated atomically.",
		Internal:    true,
	},
	{
		Type:        typeWorkMessageOverdue,
		Stability:   StabilityBeta,
		Permission:  "sessions:message:read",
		Description: "A Message crossed its acknowledgement deadline and overdue Deliveries were materialized.",
		Internal:    true,
	},
	{
		Type:        typeWorkMessageRerouted,
		Stability:   StabilityBeta,
		Permission:  "sessions:message:read",
		Description: "A governed reroute derived a new Message carrier from an existing Message.",
		Internal:    true,
	},
	{
		Type:        typeWorkMessageEscalated,
		Stability:   StabilityBeta,
		Permission:  "sessions:message:read",
		Description: "An overdue Message produced a bounded governed escalation Message.",
		Internal:    true,
	},
	{
		Type:        typeWorkProtocolReplyAvailable,
		Stability:   StabilityBeta,
		Permission:  "sessions:message:read",
		Description: "An authenticated protocol Message or artifact reference was projected into one bounded local Message carrier.",
		Internal:    true,
	},
	{
		Type:        typeWorkProtocolMessageReceived,
		Stability:   StabilityBeta,
		Permission:  "sessions:message:read",
		Description: "An authenticated inbound protocol Message was projected into one bounded local Message carrier.",
		Internal:    true,
	},
	{
		Type:        typeWorkHandoffCarrierAvailable,
		Stability:   StabilityBeta,
		Permission:  "sessions:message:read",
		Description: "A workflow created the Message/Delivery carrier from which a Handoff can be offered.",
		Internal:    true,
	},
	{
		Type:        typeWorkDecisionRecorded,
		Stability:   StabilityBeta,
		Permission:  "sessions:decision:read",
		Description: "An append-only work decision was recorded and its current head projection was resolved.",
		Internal:    true,
	},
	{
		Type:        typeWorkDecisionRequestResponded,
		Stability:   StabilityBeta,
		Permission:  "sessions:decision-request:read",
		Description: "A DecisionRequest received an explicit governed response and reached its resulting state.",
		Internal:    true,
	},
	{
		Type:        typeWorkDecisionRequestExpired,
		Stability:   StabilityBeta,
		Permission:  "sessions:decision-request:read",
		Description: "A DecisionRequest crossed its deadline and was expired by the system.",
		Internal:    true,
	},
	{
		Type:        typeWorkHandoffOffered,
		Stability:   StabilityBeta,
		Permission:  "sessions:handoff:read",
		Description: "A governed Handoff was offered against its exact Message, Delivery and WorkItem carrier.",
		Internal:    true,
	},
	{
		Type:        typeWorkHandoffAccepted,
		Stability:   StabilityBeta,
		Permission:  "sessions:handoff:read",
		Description: "A Handoff target accepted the offer and the ownership/lease transition completed.",
		Internal:    true,
	},
	{
		Type:        typeWorkHandoffRejected,
		Stability:   StabilityBeta,
		Permission:  "sessions:handoff:read",
		Description: "A Handoff target rejected the offer.",
		Internal:    true,
	},
	{
		Type:        typeWorkHandoffWithdrawn,
		Stability:   StabilityBeta,
		Permission:  "sessions:handoff:read",
		Description: "A Handoff owner withdrew an outstanding offer.",
		Internal:    true,
	},
	{
		Type:        typeWorkHandoffExpired,
		Stability:   StabilityBeta,
		Permission:  "sessions:handoff:read",
		Description: "A Handoff crossed its acknowledgement deadline and expired.",
		Internal:    true,
	},
	{
		Type:        typeWorkLeaseAcquired,
		Stability:   StabilityBeta,
		Permission:  "sessions:lease:read",
		Description: "A durable WorkItem lease was acquired, taken over or renewed with its current fencing generation.",
		Internal:    true,
	},
	{
		Type:        typeWorkLeaseEnded,
		Stability:   StabilityBeta,
		Permission:  "sessions:lease:read",
		Description: "A durable WorkItem lease was released, expired or revoked after an administrative or holder-death action, invalidating its previous fencing generation.",
		Internal:    true,
	},
	{
		Type:        typeWorkBindingReserved,
		Stability:   StabilityBeta,
		Permission:  "sessions:work:read",
		Description: "A protocol binding and its fenced WorkItem authority were durably reserved before any remote transmission.",
		Internal:    true,
	},
	{
		Type:        typeWorkBindingObserved,
		Stability:   StabilityBeta,
		Permission:  "sessions:work:read",
		Description: "A protocol binding observation produced a clean or broken outcome and reconciled its WorkItem state.",
		Internal:    true,
	},
	{
		Type:        typeWorkBindingAmbiguous,
		Stability:   StabilityBeta,
		Permission:  "sessions:work:read",
		Description: "A protocol binding observation remained explicitly unknown without being treated as successful.",
		Internal:    true,
	},
	{
		Type:        typeWorkBindingCancelRequested,
		Stability:   StabilityBeta,
		Permission:  "sessions:work:read",
		Description: "A protocol binding cancellation intent was durably claimed before the remote cancellation side effect.",
		Internal:    true,
	},
	{
		// a sealed tamper-evident audit-ledger record, forwarded to a SIEM
		// control tower. It does NOT ride the in-proc bus (Internal): the ledger is
		// fed straight into the durable delivery via IngestAudit off a leader-gated
		// cursor walk, preserving the hash chain (Seq/PrevHash/Hash/Sig verbatim).
		// Gated by audit:read, the ledger's own read surface (core handlers_audit).
		Type:        typeAuditRecorded,
		Stability:   StabilityStable,
		Permission:  "audit:read",
		Description: "A sealed audit-ledger record (the tamper-evident chain), forwarded to a SIEM. Integrity fields ride verbatim.",
		Internal:    true,
	},
}

// typeAuditRecorded is the ledger-forward event type. It is NOT an sdk/event
// bus type (no component publishes it on the bus); it enters the platform only
// through IngestAudit, so it is declared locally and flagged Internal in the
// catalog. Tenants subscribe to it like any other type; the deny-closed RBAC filter
// and replay apply unchanged.
const typeAuditRecorded event.Type = "audit.recorded"

// The work-kernel types enter Eventing only after their source transaction has
// committed a WorkEvent and source outbox. They stay local to Eventing (the
// typeWorkflowSignal convention): the sessions module owns the same wire values
// without importing this module, and the composition root converts its envelope
// to sdk/event.Event before calling IngestDurable.
const (
	typeWorkItemCreated              event.Type = "work.item.created"
	typeWorkItemTransitioned         event.Type = "work.item.transitioned"
	typeWorkOwnerChanged             event.Type = "work.owner.changed"
	typeWorkDependencyChanged        event.Type = "work.dependency.changed"
	typeWorkAcceptanceChanged        event.Type = "work.acceptance.changed"
	typeWorkMessageAvailable         event.Type = "work.message.available"
	typeWorkMessageAcknowledged      event.Type = "work.message.acknowledged"
	typeWorkMessageRetracted         event.Type = "work.message.retracted"
	typeWorkMessageExpired           event.Type = "work.message.expired"
	typeWorkMessageOverdue           event.Type = "work.message.overdue"
	typeWorkMessageRerouted          event.Type = "work.message.rerouted"
	typeWorkMessageEscalated         event.Type = "work.message.escalated"
	typeWorkProtocolReplyAvailable   event.Type = "work.protocol.reply.available"
	typeWorkProtocolMessageReceived  event.Type = "work.protocol.message.received"
	typeWorkHandoffCarrierAvailable  event.Type = "work.handoff.carrier.available"
	typeWorkDecisionRecorded         event.Type = "work.decision.recorded"
	typeWorkDecisionRequestResponded event.Type = "work.decision.request.responded"
	typeWorkDecisionRequestExpired   event.Type = "work.decision.request.expired"
	typeWorkHandoffOffered           event.Type = "work.handoff.offered"
	typeWorkHandoffAccepted          event.Type = "work.handoff.accepted"
	typeWorkHandoffRejected          event.Type = "work.handoff.rejected"
	typeWorkHandoffWithdrawn         event.Type = "work.handoff.withdrawn"
	typeWorkHandoffExpired           event.Type = "work.handoff.expired"
	typeWorkLeaseAcquired            event.Type = "work.lease.acquired"
	typeWorkLeaseEnded               event.Type = "work.lease.ended"
	typeWorkBindingReserved          event.Type = "work.binding.reserved"
	typeWorkBindingObserved          event.Type = "work.binding.observed"
	typeWorkBindingAmbiguous         event.Type = "work.binding.ambiguous"
	typeWorkBindingCancelRequested   event.Type = "work.binding.cancel_requested"
)

// typeWorkflowSignal is the workflow-step signal, declared locally (the
// typeAuditRecorded convention: modules never import each other). The value
// MUST match orchestration.TypeWorkflowSignal — the orchestration module is
// the only publisher.
const typeWorkflowSignal event.Type = "workflow.signal"

// Catalog returns the public event-type catalog (a copy; callers cannot mutate
// the source of truth).
func Catalog() []EventTypeInfo {
	out := make([]EventTypeInfo, len(catalog))
	copy(out, catalog)
	return out
}

// catalogTypes returns the BUS-subscribable types, in catalog order — the exact
// allowlist the module subscribes to on the in-proc bus. Internal types (fed by a
// durable intake, not captured from the bus) are omitted: the
// platform must not listen for an event no component publishes.
func catalogTypes() []event.Type {
	out := make([]event.Type, 0, len(catalog))
	for _, e := range catalog {
		if e.Internal {
			continue
		}
		out = append(out, e.Type)
	}
	return out
}

// typeInfo returns the catalog entry for t, or false for an uncataloged type
// (which is therefore unsubscribable and undeliverable).
func typeInfo(t event.Type) (EventTypeInfo, bool) {
	for _, e := range catalog {
		if e.Type == t {
			return e, true
		}
	}
	return EventTypeInfo{}, false
}
