// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package notify

import (
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Owned entity kinds and their physical tables. Tables are "notify_<entity>";
// kinds are "notify.<entity>". The longest, notify_delivery (15 chars), is within
// the 40-char module-table cap.
const (
	routeKind     model.Kind = "notify.route"
	routeTable               = "notify_route"
	deliveryKind  model.Kind = "notify.delivery"
	deliveryTable            = "notify_delivery"
	outboxKind    model.Kind = "notify.outbox"
	outboxTable              = "notify_outbox"
)

// route columns — a tenant routing rule: a predicate over the finding stream → a
// named destination, with dedup/throttle windows. MUTABLE lifecycle. The match_*
// columns are comma-separated value sets ("" = match-all for that dimension). NO
// destination credential is stored here — only the non-secret destination NAME.
const (
	colName          = "name"
	colEnabled       = "enabled"
	colMatchTypes    = "match_types"         // csv of event.Type (e.g. "finding.reported"); "" = any
	colMatchKinds    = "match_kinds"         // csv of finding-kind globs (e.g. "health_*,security_*"); "" = any
	colMinSeverity   = "min_severity"        // info|low|medium|high|critical; "" = any
	colMatchSources  = "match_sources"       // csv of emitter module names; "" = any
	colMatchSubjects = "match_subject_kinds" // csv of subject kinds; "" = any
	colDestination   = "destination"         // the provisioned destination NAME
	colDedupWindow   = "dedup_window_seconds"
	colThrottleWin   = "throttle_window_seconds"
	colPriority      = "priority" // lower fires first
	colOwnerActor    = "owner_actor"
	colOwnerActorK   = "owner_actor_kind"
)

// delivery columns — the APPEND-ONLY evidence ledger of every delivery ATTEMPT
// (the "what was sent, to whom, why, outcome" trail). NO payload/secret — only the
// already-safe finding metadata and a one-way correlation dedup_key.
const (
	colDelRouteRef    = "route_ref"
	colDelDestination = "destination"
	colDelEventType   = "event_type"
	colDelKind        = "finding_kind"
	colDelSeverity    = "severity"
	colDelSubjectKind = "subject_kind"
	colDelSubjectRef  = "subject_ref"
	colDelTitle       = "title" // short, non-sensitive
	colDelDedupKey    = "dedup_key"
	// colDelStatus: claimed (in-flight reservation) then
	// delivered|failed|rejected|no_dispatcher|unknown_destination. "rejected" is the
	// destination having READ the payload and refused it (or accepted only part of
	// it), which is distinct from "failed" — we could not reach it — because the two
	// call for opposite handling and an operator triaging the ledger needs to tell
	// them apart. The column is free text with no CHECK, so the set grows without a
	// migration; this comment is the contract.
	colDelStatus     = "status"
	colDelDetail     = "detail" // short, non-sensitive outcome class
	colDelOccurredAt = "occurred_at"
)

// outbox columns — the MUTABLE durable work queue (one row per claimed delivery,
// distinct from the append-only evidence ledger). It carries the state machine
// (status/attempts/next_attempt_at/last_attempt_at) plus the destination, the
// rendered notification JSON to (re)deliver, and the denormalized ledger-display
// fields so the terminal outcome can be appended to notify_delivery without a second
// read. NO secret/payload — the notification is already minimal-data displayable
// content and the connectors hold their own credentials (docs/SECURITY-HARDENING.md).
const (
	colObStatus      = "ob_status" // queued → delivering → delivered | dead (DLQ)
	colObAttempts    = "ob_attempts"
	colObNextAt      = "ob_next_attempt_at"
	colObLastAt      = "ob_last_attempt_at" // stamped at each claim; drives stale-claim rescue
	colObLastDetail  = "ob_last_detail"     // short, non-sensitive last outcome class
	colObDestination = "ob_destination"
	colObNotifyJSON  = "ob_notification" // marshaled sdk.Notification (minimal-data)
	// Denormalized ledger-outcome fields (mirror the notify_delivery columns).
	colObRouteRef    = "ob_route_ref"
	colObEventType   = "ob_event_type"
	colObKind        = "ob_finding_kind"
	colObSeverity    = "ob_severity"
	colObSubjectKind = "ob_subject_kind"
	colObSubjectRef  = "ob_subject_ref"
	colObTitle       = "ob_title"
	colObDedupKey    = "ob_dedup_key"
	colObOccurredAt  = "ob_occurred_at"
)

// Outbox lifecycle statuses.
const (
	obStatusQueued     = "queued"
	obStatusDelivering = "delivering"
	obStatusDelivered  = "delivered"
	obStatusDead       = "dead" // dead-letter: exhausted retries or a deterministic reject
)

// RegisterSchema declares the module's two owned entities. The engine creates the
// tables, injects the base columns and attaches the tenant + append-only guards
// (S02 §7); a module cannot opt out of isolation. The route UNIQUE index
// leads with model.ColTenantID so it can neither couple tenants nor leak existence.
//
// Minimal data (docs/SECURITY-HARDENING.md): a route stores a non-secret destination NAME, never a
// webhook URL or token; a delivery row stores only displayable finding metadata
// and a correlation hash. The delivery ledger is APPEND-ONLY so the notification
// evidence trail cannot be silently rewritten (docs/SECURITY-HARDENING.md). Routes are NOT
// descriptor-Audited: their privileged mutations append a SEMANTIC self-audit
// attributed to the real principal in their own transaction (helpers.go
// auditEvent, docs/SECURITY-HARDENING.md).
func (m *Module) RegisterSchema(reg store.ExtensionRegistry) error {
	// the append-only route revision ledger (change history + restore).
	if err := reg.Register(routeRevisionDescriptor()); err != nil {
		return err
	}
	if err := reg.Register(model.EntityDescriptor{
		Kind:  routeKind,
		Table: routeTable,
		Fields: []model.FieldSpec{
			{Name: colName, Kind: model.KindText, Indexed: true},
			{Name: colEnabled, Kind: model.KindBool, Indexed: true},
			{Name: colMatchTypes, Kind: model.KindText, Nullable: true},
			{Name: colMatchKinds, Kind: model.KindText, Nullable: true},
			{Name: colMinSeverity, Kind: model.KindText, Nullable: true},
			{Name: colMatchSources, Kind: model.KindText, Nullable: true},
			{Name: colMatchSubjects, Kind: model.KindText, Nullable: true},
			{Name: colDestination, Kind: model.KindText, Indexed: true},
			{Name: colDedupWindow, Kind: model.KindInt},
			{Name: colThrottleWin, Kind: model.KindInt},
			{Name: colPriority, Kind: model.KindInt, Indexed: true},
			{Name: colOwnerActor, Kind: model.KindText},
			{Name: colOwnerActorK, Kind: model.KindText},
		},
		Indexes: []model.IndexSpec{{
			Name:    "notify_route_uniq",
			Columns: []string{model.ColTenantID, colName},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	if err := reg.Register(model.EntityDescriptor{
		Kind:       deliveryKind,
		Table:      deliveryTable,
		AppendOnly: true, // immutable notification evidence trail (docs/SECURITY-HARDENING.md)
		Fields: []model.FieldSpec{
			{Name: colDelRouteRef, Kind: model.KindUUID, Nullable: true, Indexed: true},
			{Name: colDelDestination, Kind: model.KindText, Indexed: true},
			{Name: colDelEventType, Kind: model.KindText},
			{Name: colDelKind, Kind: model.KindText, Indexed: true},
			{Name: colDelSeverity, Kind: model.KindText},
			{Name: colDelSubjectKind, Kind: model.KindText},
			{Name: colDelSubjectRef, Kind: model.KindText},
			{Name: colDelTitle, Kind: model.KindText, Nullable: true},
			{Name: colDelDedupKey, Kind: model.KindText, Indexed: true},
			{Name: colDelStatus, Kind: model.KindText, Indexed: true},
			{Name: colDelDetail, Kind: model.KindText, Nullable: true},
			{Name: colDelOccurredAt, Kind: model.KindTimestamp, Indexed: true},
		},
	}); err != nil {
		return err
	}

	// The MUTABLE durable outbox: a row is claimed (queued→delivering), retried with
	// backoff, and either delivered or dead-lettered. Indexes on status and
	// next_attempt_at back the two due-scan queries (queued&&due, delivering&&stale).
	return reg.Register(model.EntityDescriptor{
		Kind:  outboxKind,
		Table: outboxTable,
		Fields: []model.FieldSpec{
			{Name: colObStatus, Kind: model.KindText, Indexed: true},
			{Name: colObAttempts, Kind: model.KindInt},
			{Name: colObNextAt, Kind: model.KindTimestamp, Indexed: true},
			{Name: colObLastAt, Kind: model.KindTimestamp, Indexed: true},
			{Name: colObLastDetail, Kind: model.KindText, Nullable: true},
			{Name: colObDestination, Kind: model.KindText, Indexed: true},
			{Name: colObNotifyJSON, Kind: model.KindText},
			{Name: colObRouteRef, Kind: model.KindUUID, Nullable: true},
			{Name: colObEventType, Kind: model.KindText},
			{Name: colObKind, Kind: model.KindText},
			{Name: colObSeverity, Kind: model.KindText},
			{Name: colObSubjectKind, Kind: model.KindText},
			{Name: colObSubjectRef, Kind: model.KindText},
			{Name: colObTitle, Kind: model.KindText, Nullable: true},
			{Name: colObDedupKey, Kind: model.KindText},
			{Name: colObOccurredAt, Kind: model.KindTimestamp},
		},
	})
}
