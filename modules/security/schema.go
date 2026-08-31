// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package security

import (
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Owned entity kinds and their physical tables. All stay within the 40-char
// module-table cap: the longest, security_enforcement_policy, is 26 chars.
//
// Findings themselves are the CORE Finding entity (sc.Findings()), not a module
// table — the security module is the first producer/consumer of persisted findings
//. What the module owns is the FORENSIC layer on top: an incident CASE, its
// append-only chain-of-custody LINKS, and the inline-ENFORCEMENT policy. The case
// links are APPEND-ONLY because the set of evidence attached to an incident is
// itself evidence and must not be silently rewritten (docs/SECURITY-HARDENING.md).
const (
	caseKind         model.Kind = "security.case"
	caseTable                   = "security_case"
	caseLinkKind     model.Kind = "security.case_link"
	caseLinkTable               = "security_case_link"
	enforcementKind  model.Kind = "security.enforcement_policy"
	enforcementTable            = "security_enforcement_policy"
)

// security_case columns — one incident/forensic case (mutable lifecycle).
const (
	colTitle           = "title"
	colStatus          = "status"   // "open" | "investigating" | "contained" | "closed"
	colSeverity        = "severity" // core severity vocabulary (low..critical)
	colSubjectKind     = "subject_kind"
	colSubjectRef      = "subject_ref"
	colSummary         = "summary"
	colOpenedBy        = "opened_by"        // audit-actor of the opener (provenance)
	colIntegrityOK     = "integrity_ok"     // last verified chain integrity for the subject window
	colIntegrityReason = "integrity_reason" // "" when ok; else the first break reason
	colAttestedSeq     = "attested_seq"     // highest ledger seq a valid checkpoint attests (0 if none)
	colOpenedAt        = "opened_at"
	colClosedAt        = "closed_at"
)

// security_case_link columns — the append-only chain-of-custody: which finding /
// ledger event / anomaly / note was attached to a case, by whom, when.
const (
	colCaseRef  = "case_ref"
	colLinkKind = "link_kind" // "finding" | "audit_seq" | "anomaly" | "note"
	colLinkRef  = "link_ref"  // finding id / ledger seq / anomaly ref
	colNote     = "note"      // short, non-sensitive operator prose (bounded)
	colLinkedBy = "linked_by" // audit-actor (provenance)
	colLinkedAt = "linked_at"
)

// security_enforcement_policy columns — the per-class inline-enforcement posture.
// Default (no row) = DETECTIVE: a guardrail of that class only flags, never blocks.
const (
	colClass       = "class"        // guardrail class ("pii", "prompt_injection", … or "*" for all)
	colEnabled     = "enabled"      // inline enforcement on for this class
	colMinSeverity = "min_severity" // block only at/above this severity (core vocabulary)
	colGoverned    = "governed"     // whether enabling went through a real gate
	colSetBy       = "set_by"       // audit-actor that set the posture
	colUpdatedAt   = "set_at"       // when the posture was set ("updated_at" is a reserved base column)
)

// RegisterSchema declares the module's three owned entities. It satisfies the
// engine-side runtime.SchemaProvider seam (structural — no runtime import) and is
// called once, at store construction, before any Scope exists: the engine creates
// the tables, injects the base columns and attaches the tenant, audit and
// append-only guards. A module cannot opt out of isolation.
//
// Minimal data (docs/SECURITY-HARDENING.md): no column can hold a usable secret/PII. A case summary
// / link note is bounded operator prose; a subject_ref is a non-sensitive entity
// reference; evidence detail lives only as a hash on the linked Finding. The case
// links are APPEND-ONLY (the chain of custody cannot be rewritten, docs/SECURITY-HARDENING.md).
//
// None is descriptor-Audited: every privileged mutation appends a SEMANTIC
// self-audit attributed to the real principal in its own transaction (helpers.go
// auditEvent) — the who/what the per-row engine audit (ActorSystem) could not
// attribute.
func (m *Module) RegisterSchema(reg store.ExtensionRegistry) error {
	if err := reg.Register(model.EntityDescriptor{
		Kind:  caseKind,
		Table: caseTable,
		Fields: []model.FieldSpec{
			{Name: colTitle, Kind: model.KindText},
			{Name: colStatus, Kind: model.KindText, Indexed: true},
			{Name: colSeverity, Kind: model.KindText, Indexed: true},
			{Name: colSubjectKind, Kind: model.KindText, Indexed: true},
			{Name: colSubjectRef, Kind: model.KindText, Indexed: true},
			{Name: colSummary, Kind: model.KindText, Nullable: true},
			{Name: colOpenedBy, Kind: model.KindText},
			{Name: colIntegrityOK, Kind: model.KindBool},
			{Name: colIntegrityReason, Kind: model.KindText, Nullable: true},
			{Name: colAttestedSeq, Kind: model.KindInt},
			{Name: colOpenedAt, Kind: model.KindTimestamp, Indexed: true},
			{Name: colClosedAt, Kind: model.KindTimestamp, Nullable: true},
		},
	}); err != nil {
		return err
	}

	if err := reg.Register(model.EntityDescriptor{
		Kind:       caseLinkKind,
		Table:      caseLinkTable,
		AppendOnly: true, // immutable chain of custody (docs/SECURITY-HARDENING.md)
		Fields: []model.FieldSpec{
			{Name: colCaseRef, Kind: model.KindUUID, Indexed: true},
			{Name: colLinkKind, Kind: model.KindText, Indexed: true},
			{Name: colLinkRef, Kind: model.KindText, Indexed: true},
			{Name: colNote, Kind: model.KindText, Nullable: true},
			{Name: colLinkedBy, Kind: model.KindText},
			{Name: colLinkedAt, Kind: model.KindTimestamp, Indexed: true},
		},
	}); err != nil {
		return err
	}

	return reg.Register(model.EntityDescriptor{
		Kind:  enforcementKind,
		Table: enforcementTable,
		Fields: []model.FieldSpec{
			{Name: colClass, Kind: model.KindText, Indexed: true},
			{Name: colEnabled, Kind: model.KindBool},
			{Name: colMinSeverity, Kind: model.KindText},
			{Name: colGoverned, Kind: model.KindBool},
			{Name: colSetBy, Kind: model.KindText},
			{Name: colUpdatedAt, Kind: model.KindTimestamp},
		},
		Indexes: []model.IndexSpec{{
			// One posture row per (tenant, class). Unique index leads with
			// tenant_id so it cannot couple tenants or leak existence.
			Name:    "security_enforcement_policy_uniq",
			Columns: []string{model.ColTenantID, colClass},
			Unique:  true,
		}},
	})
}
