// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import "github.com/olivaresai/olivares/core/model"

// This file is the DATA-CLASS REGISTRY: a fixed, in-code catalog (versioned
// in the binary, like the capability catalog) of what the control plane stores and
// how it may be disposed of. Every ext kind string and column below was VERIFIED
// against the owning module's schema before being pinned (file references inline);
// the registry probes by KIND string only — compliance never imports a sibling
// module. The engine is INERT until a tenant authors a policy (the "inert until
// first rule" posture): recommended_days are advisory, never an active default.

// Registry data-class ids. They travel as plain strings in the cross-module hold
// contract (knowledge quote them without importing this package).
const (
	classAgentMemory      = "agent.memory"
	classSessionTimeline  = "session.timeline"
	classVoiceSession     = "voice.session"
	classCostSample       = "finops.cost_sample"
	classKnowledgeContent = "knowledge.content"
	classAuditLedger      = "audit.ledger"
	classEvidenceAppend   = "evidence.append_only"
)

// Hold subject-kind vocabulary referenced by the registry (the §4 open vocabulary;
// these are the kinds the registry relates to classes).
const (
	subjectKindAgent    = "agent"
	subjectKindUser     = "user"
	subjectKindSession  = "session"
	subjectKindKB       = "kb"
	subjectKindDocument = "document"
)

// retentionDaysMax bounds a policy's retention window (§2: 1 ≤ days ≤ 36500).
const retentionDaysMax = 36500

// dataClass declares one disposable (or deliberately non-disposable) class of
// stored data: the ext kinds it covers, the age column the sweep cuts on, whether
// it may be purged at all, whether it carries model I/O (⇒ the provider retention
// floor of §7 applies), the hold subject kinds RELATED to it and — where a column
// maps a subject to its rows — the fine-grained exclusion mapping. A related
// subject kind WITHOUT a column mapping forces a conservative whole-class skip
// under a subject hold (over-preservation is the safe direction, §6).
type dataClass struct {
	ID        string
	ExtKinds  []model.Kind
	AgeColumn string // "" ⇒ no age predicate (non-purgeable in v1)
	Purgeable bool
	ModelIO   bool
	// RecommendedDays is ADVISORY only (exposed by GET /retention/classes); the
	// engine never applies it without an explicit tenant policy. 0 = no number.
	RecommendedDays int
	// SubjectKinds are the hold subject kinds related to this class: a subject hold
	// of one of these kinds affects the class (fine exclusion if mapped, whole-class
	// skip if not). Kinds outside this list are unrelated and never block the class.
	SubjectKinds []string
	// SubjectColumns maps a related subject kind to the row column carrying its ref,
	// enabling fine-grained exclusion instead of a whole-class skip.
	SubjectColumns map[string]string
	Note           string
}

// dataClassRegistry is the fixed catalog, in presentation order. Verification notes
// (2026-06-10, this tree):
//   - knowledge.memory: agent_ref column at modules/knowledge/schema.go:117,322;
//     age = the engine-injected updated_at base column (model.ColUpdatedAt).
//   - sessions.live / sessions.timeline: kinds at modules/sessions/schema.go:13-14;
//     live carries agent_ref/session_ref, timeline only session_ref — no uniform
//     subject column across the class, so agent/session holds skip conservatively.
//   - voice.session: kind at modules/voice/schema.go:13; agent_ref/session_ref exist
//     but the class keeps the conservative posture (no mapping declared in §2).
//   - finops.cost_sample: kind at modules/finops/schema.go:16; age = created_at base
//     column (samples are write-once; occurred_at is a payload timestamp, not the
//     storage age the schedule governs).
var dataClassRegistry = []dataClass{
	{
		ID: classAgentMemory,
		// knowledge.memory_scoped is the per-user/per-session namespace
		// twin (modules/knowledge/schema.go scopedMemoryKind). user/session join
		// as MAPPED subject kinds: only the scoped kind carries user_ref/
		// session_ref, and subjectHeld reads the column off each row in code, so
		// an agent-global row (no such column) reads "" and is simply not
		// user/session-attributable — a user/session hold finely excludes
		// exactly the scoped rows it names and never freezes the whole class.
		ExtKinds:     []model.Kind{"knowledge.memory", "knowledge.memory_scoped"},
		AgeColumn:    model.ColUpdatedAt,
		Purgeable:    true,
		ModelIO:      true,
		SubjectKinds: []string{subjectKindAgent, subjectKindUser, subjectKindSession},
		SubjectColumns: map[string]string{
			subjectKindAgent:   "agent_ref",
			subjectKindUser:    "user_ref",
			subjectKindSession: "session_ref",
		},
		Note: "Governed agent memory (agent-global + user/session-scoped). Carries its own TTL (expires_at, lazy + explicit purge); age-based purge is opt-in on top.",
	},
	{
		ID:              classSessionTimeline,
		ExtKinds:        []model.Kind{"sessions.live", "sessions.timeline"},
		AgeColumn:       model.ColUpdatedAt,
		Purgeable:       true,
		ModelIO:         true,
		RecommendedDays: 365,
		SubjectKinds:    []string{subjectKindAgent, subjectKindSession},
		Note:            "Session live state + replayable timelines. Related agent/session subject holds skip the whole class (no uniform subject column).",
	},
	{
		ID:              classVoiceSession,
		ExtKinds:        []model.Kind{"voice.session"},
		AgeColumn:       model.ColUpdatedAt,
		Purgeable:       true,
		ModelIO:         true,
		RecommendedDays: 365,
		SubjectKinds:    []string{subjectKindAgent, subjectKindSession},
		Note:            "Voice/realtime session metadata. Related agent/session subject holds skip the whole class.",
	},
	{
		ID:              classCostSample,
		ExtKinds:        []model.Kind{"finops.cost_sample"},
		AgeColumn:       model.ColCreatedAt,
		Purgeable:       true,
		ModelIO:         false,
		RecommendedDays: 730,
		Note:            "FinOps cost read-model samples (counts and refs, no content).",
	},
	{
		ID:           classKnowledgeContent,
		ExtKinds:     []model.Kind{"knowledge.base", "knowledge.document", "knowledge.chunk"},
		Purgeable:    false,
		SubjectKinds: []string{subjectKindKB, subjectKindDocument},
		Note:         "KBs/documents/chunks. Not age-purgeable in v1: deletion goes through KB delete / erasure, where the hold-gate is enforced in the knowledge module (§5). A policy here documents the schedule (evidence).",
	},
	{
		ID:              classAuditLedger,
		Purgeable:       false,
		RecommendedDays: 2555,
		Note:            "The append-only audit ledger (audit_events, a core table — no ext kind). Never purged in v1: the multi-year story is continuous WORM archival (§8), 'you archive it, you don't change it'.",
	},
	{
		ID:              classEvidenceAppend,
		Purgeable:       false,
		RecommendedDays: 2555,
		Note:            "Every AppendOnly module table (decision trails, custody, certificates). Retention ≥ audit.ledger. Privileged append-only purge is the documented DropTenant seam (core/internal/store/sqlstore/system.go), future work.",
	},
}

// dataClassByID indexes the registry for validation and the sweep.
var dataClassByID = func() map[string]dataClass {
	m := make(map[string]dataClass, len(dataClassRegistry))
	for _, dc := range dataClassRegistry {
		m[dc.ID] = dc
	}
	return m
}()

// IsKnownDataClass reports whether id is a registered data class. It lets an out-of-tree
// policy provider (the retention governor) validate its class references against the
// authoritative registry instead of silently floor-ing a non-existent class. It is a
// pure, additive read accessor — the open binary's behavior is unchanged.
func IsKnownDataClass(id string) bool {
	_, ok := dataClassByID[id]
	return ok
}

// validateRetentionPolicy applies the §2 policy validation: a known class, a bounded
// window, a closed disposition vocabulary, and purge ONLY where the class is
// purgeable (non-purgeable classes still accept retain — the documented schedule is
// itself evidence). It returns a client-facing message, or "" when valid.
func validateRetentionPolicy(classID, disposition string, days int) string {
	dc, ok := dataClassByID[classID]
	if !ok {
		return "unknown data_class " + classID + " (see GET /retention/classes)"
	}
	if days < 1 || days > retentionDaysMax {
		return "retention_days must be between 1 and 36500"
	}
	switch disposition {
	case dispositionRetain:
		return ""
	case dispositionPurge:
		if !dc.Purgeable {
			return "data_class " + classID + " is not purgeable; only disposition=retain is accepted"
		}
		return ""
	default:
		return "disposition must be retain or purge"
	}
}
