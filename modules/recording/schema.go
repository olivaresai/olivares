// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package recording

import (
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Owned entity kinds and their physical tables (all within the 40-char cap).
const (
	sessionKind  model.Kind = "recording.session"
	sessionTable            = "recording_session" // 17 chars
	frameKind    model.Kind = "recording.frame"
	frameTable              = "recording_frame" // 15 chars
	configKind   model.Kind = "recording.config"
	configTable             = "recording_config" // 16 chars
)

// session columns: one privileged window of one credential in one tenant.
// Mutable lifecycle (active → sealed); its immutable evidence lives in the
// append-only frame trail plus the ledger anchors referenced from here.
const (
	colSubject     = "subject"      // audit-actor of the recorded principal ("user:<id>"/"token:<id>")
	colSubjectKind = "subject_kind" // "user" | "token"
	colSubjectUser = "subject_user" // stable user id ("" for a standalone token)
	colCred        = "cred"         // credential id (login-session id / token id) — the session anchor
	colStatus      = "status"       // active | sealed
	colOpenedAt    = "opened_at"
	colLastAt      = "last_at"    // last recorded activity (idle-seal anchor)
	colReserved    = "reserved"   // frame slots reserved by Gate
	colWritten     = "written"    // frames actually appended; reserved>written = visible gap
	colTipHash     = "tip_hash"   // hex chain tip over the session's frames
	colOpenSeq     = "open_seq"   // ledger seq of the recording.session.open anchor
	colAnchorSeq   = "anchor_seq" // ledger seq of the latest periodic anchor (0 = none yet)
	colSealSeq     = "seal_seq"   // ledger seq of the recording.session.seal anchor (0 = unsealed)
	colSealedAt    = "sealed_at"
	colSealReason  = "seal_reason"      // idle | closed | breakglass_review | sweep | consent_change
	colConsentAt   = "consent_at"       // AC-8 acknowledgement instant
	colConsentMode = "consent_mode"     // required | notice | auto (how consent was satisfied)
	colBGGrant     = "breakglass_grant" // bound break-glass grant id ("" = none)
	colSummary     = "summary"          // DERIVED AI summary (never evidence)
	colSummaryMeta = "summary_meta"     // {"derived":true,"generated_at":...,"source":...}
	colRetention   = "retention_class"  // Retention key (no purge implemented here)
	// colOpenGuard backs "at most one ACTIVE session per credential" at the DB
	// level: it holds openGuard while the session is active and NULL once sealed;
	// the unique (tenant_id, cred, open_guard) index treats NULLs as distinct on
	// both engines, mirroring the active_guard pattern.
	colOpenGuard = "open_guard"
)

// openGuard is the sentinel colOpenGuard holds while a session is active.
const openGuard = "open"

// frame columns: one module-route action on a recorded surface. APPEND-ONLY.
const (
	colFrSession   = "session_id"
	colFrIdx       = "idx" // 1-based, gap-free per session (chain order)
	colFrAt        = "at"
	colFrActor     = "actor"      // immediate audit-actor of the request
	colFrActorKind = "actor_kind" // user | token
	colFrActorUser = "actor_user" // stable user id behind the actor ("" if none)
	colFrActAs     = "act_as"     // delegated subject (token-exchange act-as), "" if none
	colFrNamespace = "namespace"  // module API namespace (the surface)
	colFrMethod    = "method"
	colFrPattern   = "pattern"    // chi route pattern (human-readable shape)
	colFrPerm      = "perm"       // permission the route required
	colFrParams    = "params"     // redacted URL parameters (JSON object, sorted keys)
	colFrQueryKeys = "query_keys" // sorted query parameter NAMES, comma-joined ("" if none)
	colFrStatus    = "http_status"
	colFrOutcome   = "outcome"     // allowed | denied | rejected | error
	colFrBodySHA   = "body_sha256" // hex digest of consumed request body ("" if no body)
	colFrBodyBytes = "body_bytes"
	colFrDurMS     = "dur_ms"
	colFrPrevHash  = "prev_hash"  // hex; zero-hash at idx 1
	colFrHash      = "hash"       // hex chain hash of this frame
	colFrAnchorSeq = "anchor_seq" // ledger seq of the periodic anchor this frame triggered (0 = none)
)

// config columns: the per-tenant recording policy (one row per tenant).
const (
	colCfgKey       = "cfg_key"    // constant "default" — backs the one-row-per-tenant unique index
	colCfgNS        = "namespaces" // JSON array of recorded module namespaces (human operators)
	colCfgConsent   = "consent"    // notice | required
	colCfgIdleSecs  = "idle_seconds"
	colCfgRetention = "retention_days" // Input; this module never purges
	colCfgAI        = "ai_summaries"   // opt-in: the transcript leaves the trust boundary
	colCfgUpdatedBy = "updated_by"
)

// cfgKey is the constant colCfgKey value (one config row per tenant).
const cfgKey = "default"

// RegisterSchema declares the module's owned entities (engine-side
// runtime.SchemaProvider seam; the engine creates the tables and attaches the
// tenant + append-only guards). Minimal data (docs/SECURITY-HARDENING.md): no column can hold a
// usable credential — params are redacted at capture, bodies are one-way
// digests, actors are id strings. The frame trail is AppendOnly so the
// recording can never be silently rewritten (docs/SECURITY-HARDENING.md); none of the entities
// is descriptor-Audited — the module appends SEMANTIC ledger events (open/
// anchor/seal/consent/replay) attributed to the real principal instead.
func (m *Module) RegisterSchema(reg store.ExtensionRegistry) error {
	if err := reg.Register(model.EntityDescriptor{
		Kind:  sessionKind,
		Table: sessionTable,
		Fields: []model.FieldSpec{
			{Name: colSubject, Kind: model.KindText, Indexed: true},
			{Name: colSubjectKind, Kind: model.KindText},
			{Name: colSubjectUser, Kind: model.KindText, Nullable: true, Indexed: true},
			{Name: colCred, Kind: model.KindText, Indexed: true},
			{Name: colStatus, Kind: model.KindText, Indexed: true},
			{Name: colOpenedAt, Kind: model.KindTimestamp},
			{Name: colLastAt, Kind: model.KindTimestamp, Indexed: true},
			{Name: colReserved, Kind: model.KindInt},
			{Name: colWritten, Kind: model.KindInt},
			{Name: colTipHash, Kind: model.KindText},
			{Name: colOpenSeq, Kind: model.KindInt},
			{Name: colAnchorSeq, Kind: model.KindInt},
			{Name: colSealSeq, Kind: model.KindInt},
			{Name: colSealedAt, Kind: model.KindTimestamp, Nullable: true},
			{Name: colSealReason, Kind: model.KindText, Nullable: true},
			{Name: colConsentAt, Kind: model.KindTimestamp, Nullable: true},
			{Name: colConsentMode, Kind: model.KindText},
			{Name: colBGGrant, Kind: model.KindText, Nullable: true, Indexed: true},
			{Name: colSummary, Kind: model.KindText, Nullable: true},
			{Name: colSummaryMeta, Kind: model.KindJSON, Nullable: true},
			{Name: colRetention, Kind: model.KindText},
			{Name: colOpenGuard, Kind: model.KindText, Nullable: true},
		},
		Indexes: []model.IndexSpec{{
			// At most one ACTIVE recording session per credential. Leads with
			// tenant_id; NULLs are distinct, so sealed sessions never collide.
			Name:    "recording_session_open_uniq",
			Columns: []string{model.ColTenantID, colCred, colOpenGuard},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	if err := reg.Register(model.EntityDescriptor{
		Kind:       frameKind,
		Table:      frameTable,
		AppendOnly: true, // immutable evidence: frames are never updated or deleted here
		Fields: []model.FieldSpec{
			{Name: colFrSession, Kind: model.KindUUID, Indexed: true},
			{Name: colFrIdx, Kind: model.KindInt},
			{Name: colFrAt, Kind: model.KindTimestamp},
			{Name: colFrActor, Kind: model.KindText},
			{Name: colFrActorKind, Kind: model.KindText},
			{Name: colFrActorUser, Kind: model.KindText, Nullable: true},
			{Name: colFrActAs, Kind: model.KindText, Nullable: true},
			{Name: colFrNamespace, Kind: model.KindText, Indexed: true},
			{Name: colFrMethod, Kind: model.KindText},
			{Name: colFrPattern, Kind: model.KindText},
			{Name: colFrPerm, Kind: model.KindText},
			{Name: colFrParams, Kind: model.KindJSON, Nullable: true},
			{Name: colFrQueryKeys, Kind: model.KindText, Nullable: true},
			{Name: colFrStatus, Kind: model.KindInt},
			{Name: colFrOutcome, Kind: model.KindText},
			{Name: colFrBodySHA, Kind: model.KindText, Nullable: true},
			{Name: colFrBodyBytes, Kind: model.KindInt},
			{Name: colFrDurMS, Kind: model.KindInt},
			{Name: colFrPrevHash, Kind: model.KindText},
			{Name: colFrHash, Kind: model.KindText},
			{Name: colFrAnchorSeq, Kind: model.KindInt},
		},
		Indexes: []model.IndexSpec{{
			// Gap-free chain order per session; the unique index is the concurrency
			// backstop for the idx assignment (append serializes on the session row's
			// version, this catches anything that slips past).
			Name:    "recording_frame_idx_uniq",
			Columns: []string{model.ColTenantID, colFrSession, colFrIdx},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	return reg.Register(model.EntityDescriptor{
		Kind:  configKind,
		Table: configTable,
		Fields: []model.FieldSpec{
			{Name: colCfgKey, Kind: model.KindText},
			{Name: colCfgNS, Kind: model.KindJSON},
			{Name: colCfgConsent, Kind: model.KindText},
			{Name: colCfgIdleSecs, Kind: model.KindInt},
			{Name: colCfgRetention, Kind: model.KindInt},
			{Name: colCfgAI, Kind: model.KindBool},
			{Name: colCfgUpdatedBy, Kind: model.KindText},
		},
		Indexes: []model.IndexSpec{{
			Name:    "recording_config_uniq",
			Columns: []string{model.ColTenantID, colCfgKey},
			Unique:  true,
		}},
	})
}
