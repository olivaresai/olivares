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

// Principal declarations of this package's columns (schema.go, revisions.go). A
// declaration shared by several columns cites lines that hold for every column
// using it.
var (
	// pdeclActorRef is the caller principal's audit actor string ("user:<id>" or
	// "token:<id>", core/auth/principal.go:219-227); it records who authored or
	// changed a route and is only rendered (dto.go:44, revisions.go:123).
	pdeclActorRef      = model.Ref(model.EncodeUserRef, model.ClassEvidence)
	pdeclNoneActorKind = model.None("the caller principal's actor kind, a closed set: core/auth/principal.go:243-251, route.go:111, revisions.go:79")
	// pdeclScanFindingText is text copied verbatim from an inbound finding: its
	// subject reference, its title, or the notification rendered from both
	// (deliver.go:198, deliver.go:212, deliver.go:529, deliver.go:556). Emitters
	// put a person's alias there, e.g. an email as subject and in the title
	// (connectors/claude-api/shadowauth.go:53-54), so it is matched against every
	// alias as evidence of what was sent.
	pdeclScanFindingText = model.Scan(model.ClassEvidence)
	pdeclNoneRouteID     = model.None("the id of a notify route, written from the route id and read back only to load or match that route: deliver.go:742, outbox.go:258, revisions.go:177")
	pdeclNoneRouteName   = model.None("an operator-chosen route label and natural key, only displayed, sorted, searched and hashed into dedup keys: dto.go:33, deliver.go:336, deliver.go:784, search.go:40")
	pdeclNoneDestination = model.None("a provisioned destination name for an output connector, checked against the tenant's destinations and resolved only by the dispatcher: route.go:63, ports.go:65")
	pdeclNoneEventType   = model.None("a routed bus event type (or a set of them), a closed set: evaluate.go:24-31, evaluate.go:62, deliver.go:240")
	pdeclNoneKind        = model.None("a finding kind or approval action label (or a set of kind globs), matched only against route kind globs: deliver.go:246, helpers.go:264")
	pdeclNoneSeverity    = model.None("a severity label, read only as a severity: helpers.go:286-298, helpers.go:302-305")
	pdeclNoneSources     = model.None("a set of emitter module names, compared only with an event's source: deliver.go:249, deliver.go:210")
	pdeclNoneSubjectKind = model.None("a subject-kind label (or a set of them), compared only with a route's subject-kind set: deliver.go:252")
	pdeclNoneDedupKey    = model.None("a SHA-256 digest of the route and finding coordinates: deliver.go:336, helpers.go:180-182")
	pdeclNoneDetail      = model.None("a fixed outcome token chosen by this module, never destination text: deliver.go:711-714, deliver.go:723, outbox.go:261, outbox_api.go:122")
	// pdeclRouteSnapshot is a route revision's snapshot, the routeDTO the revision
	// writer marshals (revisions.go:70); restore never re-applies its owner
	// (revisions.go:203-212).
	pdeclRouteSnapshot = model.Nested(routeDTO{}, model.ClassEvidence,
		model.Leaf("id", pdeclNoneRouteID),
		model.Leaf("name", pdeclNoneRouteName),
		model.Leaf("match_types[]", pdeclNoneEventType),
		model.Leaf("match_kinds[]", pdeclNoneKind),
		model.Leaf("min_severity", pdeclNoneSeverity),
		model.Leaf("match_sources[]", pdeclNoneSources),
		model.Leaf("match_subject_kinds[]", pdeclNoneSubjectKind),
		model.Leaf("destination", pdeclNoneDestination),
		model.Leaf("owner_actor", pdeclActorRef),
		model.Leaf("created_at", model.None("the route's creation timestamp text: dto.go:45")),
	)
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
			{Name: colName, Kind: model.KindText, Indexed: true, Principal: pdeclNoneRouteName},
			{Name: colEnabled, Kind: model.KindBool, Indexed: true},
			{Name: colMatchTypes, Kind: model.KindText, Nullable: true, Principal: pdeclNoneEventType},
			{Name: colMatchKinds, Kind: model.KindText, Nullable: true, Principal: pdeclNoneKind},
			{Name: colMinSeverity, Kind: model.KindText, Nullable: true, Principal: pdeclNoneSeverity},
			{Name: colMatchSources, Kind: model.KindText, Nullable: true, Principal: pdeclNoneSources},
			{Name: colMatchSubjects, Kind: model.KindText, Nullable: true, Principal: pdeclNoneSubjectKind},
			{Name: colDestination, Kind: model.KindText, Indexed: true, Principal: pdeclNoneDestination},
			{Name: colDedupWindow, Kind: model.KindInt},
			{Name: colThrottleWin, Kind: model.KindInt},
			{Name: colPriority, Kind: model.KindInt, Indexed: true},
			{Name: colOwnerActor, Kind: model.KindText, Principal: pdeclActorRef},
			{Name: colOwnerActorK, Kind: model.KindText, Principal: pdeclNoneActorKind},
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
			{Name: colDelRouteRef, Kind: model.KindUUID, Nullable: true, Indexed: true, Principal: pdeclNoneRouteID},
			{Name: colDelDestination, Kind: model.KindText, Indexed: true, Principal: pdeclNoneDestination},
			{Name: colDelEventType, Kind: model.KindText, Principal: pdeclNoneEventType},
			{Name: colDelKind, Kind: model.KindText, Indexed: true, Principal: pdeclNoneKind},
			{Name: colDelSeverity, Kind: model.KindText, Principal: pdeclNoneSeverity},
			{Name: colDelSubjectKind, Kind: model.KindText, Principal: pdeclNoneSubjectKind},
			{Name: colDelSubjectRef, Kind: model.KindText, Principal: pdeclScanFindingText},
			{Name: colDelTitle, Kind: model.KindText, Nullable: true, Principal: pdeclScanFindingText},
			{Name: colDelDedupKey, Kind: model.KindText, Indexed: true, Principal: pdeclNoneDedupKey},
			{Name: colDelStatus, Kind: model.KindText, Indexed: true, Principal: model.None("a delivery status, a closed set: deliver.go:37-50")},
			{Name: colDelDetail, Kind: model.KindText, Nullable: true, Principal: pdeclNoneDetail},
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
			{Name: colObStatus, Kind: model.KindText, Indexed: true, Principal: model.None("an outbox lifecycle status, a closed set written only from the queue's own constants: outbox.go:233-245, outbox.go:275, outbox_api.go:111")},
			{Name: colObAttempts, Kind: model.KindInt},
			{Name: colObNextAt, Kind: model.KindTimestamp, Indexed: true},
			{Name: colObLastAt, Kind: model.KindTimestamp, Indexed: true},
			{Name: colObLastDetail, Kind: model.KindText, Nullable: true, Principal: pdeclNoneDetail},
			{Name: colObDestination, Kind: model.KindText, Indexed: true, Principal: pdeclNoneDestination},
			{Name: colObNotifyJSON, Kind: model.KindText, Principal: pdeclScanFindingText},
			{Name: colObRouteRef, Kind: model.KindUUID, Nullable: true, Principal: pdeclNoneRouteID},
			{Name: colObEventType, Kind: model.KindText, Principal: pdeclNoneEventType},
			{Name: colObKind, Kind: model.KindText, Principal: pdeclNoneKind},
			{Name: colObSeverity, Kind: model.KindText, Principal: pdeclNoneSeverity},
			{Name: colObSubjectKind, Kind: model.KindText, Principal: pdeclNoneSubjectKind},
			{Name: colObSubjectRef, Kind: model.KindText, Principal: pdeclScanFindingText},
			{Name: colObTitle, Kind: model.KindText, Nullable: true, Principal: pdeclScanFindingText},
			{Name: colObDedupKey, Kind: model.KindText, Principal: pdeclNoneDedupKey},
			{Name: colObOccurredAt, Kind: model.KindTimestamp},
		},
	})
}
