// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package health

import (
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Owned entity kinds and their physical tables. The longest, health_dependency
// (17 chars), is within the 40-char module-table cap. Tables are
// "health_<entity>"; kinds are "health.<entity>".
const (
	checkKind       model.Kind = "health.check"
	checkTable                 = "health_check"
	eventKind       model.Kind = "health.event"
	eventTable                 = "health_event"
	incidentKind    model.Kind = "health.incident"
	incidentTable              = "health_incident"
	dependencyKind  model.Kind = "health.dependency"
	dependencyTable            = "health_dependency"
)

// check columns — an operator-declared monitored subject (agent/MCP) with an
// expected cadence and an SLA target. MUTABLE lifecycle. It also carries the
// subject's CURRENT health snapshot (last_state/last_*) so GET /status is a
// single scan and never reconstructs state per request. NO payload/secret column.
const (
	colName           = "name"         // logical label (optional)
	colSubjectKind    = "subject_kind" // "agent" | "mcp"
	colSubjectRef     = "subject_ref"  // natural ref OR a core entity id (then mirrored to HealthStatus)
	colExpectedIvl    = "expected_interval_seconds"
	colGraceFactor    = "grace_factor"   // degraded after interval*grace; down after interval*grace*downMultiple
	colSLATargetPM    = "sla_target_ppm" // uptime target in parts-per-million (999000 = 99.9%)
	colDesiredStat    = "desired_status" // "active" | "paused" | "retired"
	colOwnerActor     = "owner_actor"
	colOwnerActorK    = "owner_actor_kind"
	colLastState      = "last_state"      // current: "healthy"|"degraded"|"down"|"unknown"
	colLastChecked    = "last_checked_at" // last signal/sweep instant
	colLastSeenAt     = "last_seen_at"    // last liveness (a healthy signal)
	colLastLatency    = "last_latency_ms"
	colLastDetailHash = "last_detail_hash" // hex SHA-256 of the redaction-safe detail, never raw (docs/SECURITY-HARDENING.md)
	colSLABreachOpen  = "sla_breach_open"  // true while an SLA-breach alert is outstanding (de-dups re-alerts)
)

// event columns — the APPEND-ONLY reliability transition ledger: one row per
// state change for a subject. SLA/uptime is reconstructed from it (docs/SECURITY-HARDENING.md).
// NO raw error text — only a one-way detail_hash.
const (
	colEvSubjectKind = "subject_kind"
	colEvSubjectRef  = "subject_ref"
	colEvCheckRef    = "check_ref"
	colEvState       = "state"      // new state
	colEvPrevState   = "prev_state" // state before this transition
	colEvLatency     = "latency_ms"
	colEvCause       = "cause"       // "edge" | "report" | "sweep" — what produced the transition
	colEvDetailHash  = "detail_hash" // hex SHA-256 of the redaction-safe detail (nullable)
	colEvOccurredAt  = "occurred_at"
)

// incident columns — the open→resolved lifecycle of a degraded/down period for a
// subject. MUTABLE (resolve updates resolved_at). One OPEN incident per subject
// is enforced in the handler (an UNIQUE index cannot express "many resolved, one
// open"). NO raw detail.
const (
	colInSubjectKind = "subject_kind"
	colInSubjectRef  = "subject_ref"
	colInCheckRef    = "check_ref"
	colInKind        = "kind"     // "degraded" | "down" | "sla_breach"
	colInSeverity    = "severity" // shared scale, persisted as text
	colInState       = "state"    // "open" | "resolved"
	colInOpenedAt    = "opened_at"
	colInResolvedAt  = "resolved_at"
	colInDetailHash  = "detail_hash"
	colInSummary     = "summary" // short, non-sensitive
)

// dependency columns — one auto-discovered dependency edge (the dependency map),
// e.g. an agent/session that uses an MCP server. MUTABLE: counts/recency
// accumulate via idempotent upsert (the AccessEdge merge pattern). NO payload.
const (
	colDepFromKind = "from_kind"
	colDepFromRef  = "from_ref"
	colDepToKind   = "to_kind"
	colDepToRef    = "to_ref"
	colDepRelation = "relation" // "uses_mcp" | "uses_tool" | "delegates_to"
	colDepObserved = "observed_count"
	colDepFirstAt  = "first_seen_at"
	colDepLastAt   = "last_seen_at"
)

// Principal declarations shared by the descriptors below: what a stored column
// says about principals, with the reader lines that show it.
var (
	// The monitored subject kind, copied from the check onto its events and incidents.
	pdeclNoneSubjectKind = model.None("the monitored subject kind, closed set agent|mcp validated on the check (checks.go:50, checks.go:81) and copied from the check onto event and incident rows: state.go:79")
	// The monitored agent or MCP server, never an account.
	pdeclNoneSubjectRef = model.None("the monitored agent or MCP server reference (subject kind agent|mcp: checks.go:50), matched against observed agent origins and MCP server resources (ingest.go:42, ingest.go:45) and mirrored as a core entity id when it parses as one (state.go:222); event and incident rows copy it from the check: state.go:80")
	// The health.check row a transition or an incident came from.
	pdeclNoneCheckRef = model.None("the id of the health.check row that produced the row: state.go:81, state.go:128, state.go:195")
	// A health state.
	pdeclNoneState = model.None("a health state from the closed set healthy|degraded|down|unknown: helpers.go:38, helpers.go:41, checks.go:60")
	// A one-way fingerprint of probe detail, never the detail.
	pdeclNoneDetailHash = model.None("a one-way SHA-256 fingerprint of the probe detail: helpers.go:199, state.go:99")
)

// RegisterSchema declares the module's four owned entities. The engine creates the
// tables, injects the base columns (id/tenant_id/created_at/updated_at/version/
// deleted_at) and attaches the unconditional tenant + append-only guards (S02 §7); a module cannot opt out of isolation. Every UNIQUE index leads with
// model.ColTenantID so it can neither couple tenants nor leak existence.
//
// Minimal data (docs/SECURITY-HARDENING.md): no column can hold a payload, prompt, secret or PII;
// the only sensitive detail (a probe's error text) is reduced to detail_hash
// before it ever reaches a row. The event ledger is APPEND-ONLY so the
// reliability history cannot be silently rewritten (docs/SECURITY-HARDENING.md). None is
// descriptor-Audited: the privileged check/incident mutations append a SEMANTIC
// self-audit attributed to the real principal in their own transaction
// (helpers.go auditEvent); the high-frequency liveness upserts are automated
// ingestion gated by RBAC at read time.
func (m *Module) RegisterSchema(reg store.ExtensionRegistry) error {
	if err := reg.Register(model.EntityDescriptor{
		Kind:  checkKind,
		Table: checkTable,
		Fields: []model.FieldSpec{
			{Name: colName, Kind: model.KindText, Nullable: true, Principal: model.None("an optional operator label, clamped and only rendered: checks.go:103, dto.go:48")},
			{Name: colSubjectKind, Kind: model.KindText, Indexed: true, Principal: pdeclNoneSubjectKind},
			{Name: colSubjectRef, Kind: model.KindText, Indexed: true, Principal: pdeclNoneSubjectRef},
			{Name: colExpectedIvl, Kind: model.KindInt},
			{Name: colGraceFactor, Kind: model.KindInt},
			{Name: colSLATargetPM, Kind: model.KindInt},
			{Name: colDesiredStat, Kind: model.KindText, Indexed: true, Principal: model.None("closed set active|paused|retired: checks.go:54")},
			{Name: colOwnerActor, Kind: model.KindText, Principal: model.Ref(model.EncodeUserRef, model.ClassEvidence)},
			{Name: colOwnerActorK, Kind: model.KindText, Principal: model.None("the creator's actor kind, a closed set of kind labels (user, token or system): checks.go:111, core/auth/principal.go:251, core/model/audit.go:115")},
			{Name: colLastState, Kind: model.KindText, Indexed: true, Principal: pdeclNoneState},
			{Name: colLastChecked, Kind: model.KindTimestamp, Nullable: true},
			{Name: colLastSeenAt, Kind: model.KindTimestamp, Nullable: true},
			{Name: colLastLatency, Kind: model.KindInt},
			{Name: colLastDetailHash, Kind: model.KindText, Nullable: true, Principal: pdeclNoneDetailHash},
			{Name: colSLABreachOpen, Kind: model.KindBool},
		},
		Indexes: []model.IndexSpec{{
			Name:    "health_check_uniq",
			Columns: []string{model.ColTenantID, colSubjectKind, colSubjectRef},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	if err := reg.Register(model.EntityDescriptor{
		Kind:       eventKind,
		Table:      eventTable,
		AppendOnly: true, // immutable reliability transition history (docs/SECURITY-HARDENING.md)
		Fields: []model.FieldSpec{
			{Name: colEvSubjectKind, Kind: model.KindText, Principal: pdeclNoneSubjectKind},
			{Name: colEvSubjectRef, Kind: model.KindText, Indexed: true, Principal: pdeclNoneSubjectRef},
			{Name: colEvCheckRef, Kind: model.KindUUID, Nullable: true, Indexed: true, Principal: pdeclNoneCheckRef},
			{Name: colEvState, Kind: model.KindText, Indexed: true, Principal: pdeclNoneState},
			{Name: colEvPrevState, Kind: model.KindText, Principal: pdeclNoneState},
			{Name: colEvLatency, Kind: model.KindInt},
			{Name: colEvCause, Kind: model.KindText, Principal: model.None("the transition cause, closed set edge|report|sweep: helpers.go:55, helpers.go:57")},
			{Name: colEvDetailHash, Kind: model.KindText, Nullable: true, Principal: pdeclNoneDetailHash},
			{Name: colEvOccurredAt, Kind: model.KindTimestamp, Indexed: true},
		},
	}); err != nil {
		return err
	}

	if err := reg.Register(model.EntityDescriptor{
		Kind:  incidentKind,
		Table: incidentTable,
		Fields: []model.FieldSpec{
			{Name: colInSubjectKind, Kind: model.KindText, Principal: pdeclNoneSubjectKind},
			{Name: colInSubjectRef, Kind: model.KindText, Indexed: true, Principal: pdeclNoneSubjectRef},
			{Name: colInCheckRef, Kind: model.KindUUID, Nullable: true, Indexed: true, Principal: pdeclNoneCheckRef},
			{Name: colInKind, Kind: model.KindText, Indexed: true, Principal: model.None("the degraded or down state that opened or escalated the incident: state.go:188, state.go:205")},
			{Name: colInSeverity, Kind: model.KindText, Principal: model.None("a severity on the shared scale chosen by sevForState: state.go:45, state.go:183")},
			{Name: colInState, Kind: model.KindText, Indexed: true, Principal: model.None("closed set open|resolved: state.go:175, state.go:190")},
			{Name: colInOpenedAt, Kind: model.KindTimestamp, Indexed: true},
			{Name: colInResolvedAt, Kind: model.KindTimestamp, Nullable: true},
			{Name: colInDetailHash, Kind: model.KindText, Nullable: true, Principal: pdeclNoneDetailHash},
			{Name: colInSummary, Kind: model.KindText, Nullable: true, Principal: model.None("a headline built from the subject kind, the agent or MCP subject ref and the state, only rendered: state.go:192, state.go:261, dto.go:91")},
		},
	}); err != nil {
		return err
	}

	return reg.Register(model.EntityDescriptor{
		Kind:  dependencyKind,
		Table: dependencyTable,
		Fields: []model.FieldSpec{
			{Name: colDepFromKind, Kind: model.KindText, Principal: model.None("the origin class of an observed edge (sdk/model/observation.go:55), defaulting to session: ingest.go:68, ingest.go:70; only rendered: dto.go:141")},
			{Name: colDepFromRef, Kind: model.KindText, Indexed: true, Principal: model.None("the origin reference of an observed MCP-server, MCP-tool or delegation edge (ingest.go:73, ingest.go:77); only rendered and compared with check subject refs: dto.go:139, dependencies.go:56")},
			{Name: colDepToKind, Kind: model.KindText, Principal: model.None("closed set mcp|mcp_tool|agent chosen by classifyDependency: ingest.go:74, ingest.go:76, ingest.go:78")},
			{Name: colDepToRef, Kind: model.KindText, Indexed: true, Principal: model.None("the MCP server, MCP tool or delegated agent reference of an observed edge: ingest.go:74, ingest.go:78; only rendered: dto.go:140")},
			{Name: colDepRelation, Kind: model.KindText, Principal: model.None("closed set uses_mcp|uses_tool|delegates_to: ingest.go:20, ingest.go:22")},
			{Name: colDepObserved, Kind: model.KindInt},
			{Name: colDepFirstAt, Kind: model.KindTimestamp},
			{Name: colDepLastAt, Kind: model.KindTimestamp, Indexed: true},
		},
		Indexes: []model.IndexSpec{{
			Name:    "health_dependency_uniq",
			Columns: []string{model.ColTenantID, colDepFromRef, colDepToRef, colDepRelation},
			Unique:  true,
		}},
	})
}
