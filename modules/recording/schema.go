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

// Principal declarations of the module's text, JSON and UUID columns
// (core/model/principal_decl.go). Every None cites the writer or reader lines
// that show the value names no account.
var (
	// pdeclActor is the audit-actor string of the calling principal, recorded as
	// evidence (recorder.go:299, recorder.go:404, handlers.go:1262).
	pdeclActor = model.Ref(model.EncodeUserRef, model.ClassEvidence)
	// pdeclUserID is a bare account id: the recorded principal's stable user id or
	// the user a delegated credential acts for (recorder.go:299, recorder.go:394-397,
	// recorder.go:404, core/auth/principal.go:247-252).
	pdeclUserID = model.Ref(model.EncodeUserID, model.ClassEvidence)
	// pdeclSummary is derived prose generated from a transcript that names the
	// session subject and every frame's actor (handlers.go:1106-1113), so it may
	// repeat an account reference.
	pdeclSummary = model.Scan(model.ClassEvidence)

	// Values that name no account.
	pdeclNoneSubjectKind = model.None("the recorded principal's kind, user or token: recorder.go:299, core/auth/principal.go:17-22")
	pdeclNoneActorKind   = model.None("the audit actor kind, a closed set: recorder.go:404, core/auth/principal.go:242-251")
	pdeclNoneCred        = model.None("the caller's own credential anchor, compared only with the caller's credential: recorder.go:286-288, recorder.go:551")
	pdeclNoneStatus      = model.None("the session lifecycle state, active or sealed: recording.go:78-79, recorder.go:487")
	pdeclNoneHash        = model.None("a hex SHA-256 chain hash or request-body digest: recorder.go:398-401, recorder.go:419, recorder.go:444")
	pdeclNoneSealReason  = model.None("a closed seal reason: recording.go:84-88, recorder.go:489")
	pdeclNoneConsentMode = model.None("how consent was satisfied, auto, notice or required: recorder.go:278-283, handlers.go:211")
	pdeclNoneGrant       = model.None("the id of the bound break-glass grant row, joined on by the review seal: recorder.go:568, recorder.go:613")
	pdeclNoneSummaryMeta = model.None("a fixed derivation stamp of flag, generation time, source label, frame count and chain tip: handlers.go:1071-1073")
	pdeclNoneRetention   = model.None("a constant retention tag: recording.go:93, recorder.go:304")
	pdeclNoneOpenGuard   = model.None("a constant sentinel while active, NULL once sealed: recorder.go:305, recorder.go:491")
	pdeclNoneSessionID   = model.None("the id of this module's own recording session row: recorder.go:413")
	pdeclNoneRoute       = model.None("the route's module namespace, HTTP method, route pattern or required permission: recorder.go:405, core/api/recording.go:118-119")
	pdeclNoneQueryKeys   = model.None("redacted query parameter names; values are never captured: recorder.go:406, redact.go:84-107")
	pdeclNoneOutcome     = model.None("a closed outcome classification of the HTTP status: recorder.go:451-461")
	pdeclNoneCfgKey      = model.None("the constant one-row-per-tenant key: recorder.go:75, handlers.go:1255")
	pdeclNoneConsent     = model.None("the tenant consent policy, notice or required: handlers.go:1208, recorder.go:119")

	// pdeclParams is the redacted route-parameter map (recorder.go:406, chain.go:101-111).
	// Route parameters are identifiers and may name an account on an account route;
	// email-shaped values are redacted before they persist (redact.go:31).
	pdeclParams = model.Nested(map[string]string(nil), model.ClassEvidence,
		model.Leaf("{key}", model.None("a route parameter name taken from the matched route: core/api/recording.go:122-136")),
		model.Leaf("{}", model.Scan(model.ClassEvidence)),
	)
	// pdeclNamespaces is the JSON list of recorded module namespaces (handlers.go:1244).
	pdeclNamespaces = model.Nested([]string(nil), model.ClassEvidence,
		model.Leaf("[]", model.None("a mounted module namespace, validated before write: handlers.go:1228, handlers.go:1244")),
	)
)

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
			{Name: colSubject, Kind: model.KindText, Indexed: true, Principal: pdeclActor},
			{Name: colSubjectKind, Kind: model.KindText, Principal: pdeclNoneSubjectKind},
			{Name: colSubjectUser, Kind: model.KindText, Nullable: true, Indexed: true, Principal: pdeclUserID},
			{Name: colCred, Kind: model.KindText, Indexed: true, Principal: pdeclNoneCred},
			{Name: colStatus, Kind: model.KindText, Indexed: true, Principal: pdeclNoneStatus},
			{Name: colOpenedAt, Kind: model.KindTimestamp},
			{Name: colLastAt, Kind: model.KindTimestamp, Indexed: true},
			{Name: colReserved, Kind: model.KindInt},
			{Name: colWritten, Kind: model.KindInt},
			{Name: colTipHash, Kind: model.KindText, Principal: pdeclNoneHash},
			{Name: colOpenSeq, Kind: model.KindInt},
			{Name: colAnchorSeq, Kind: model.KindInt},
			{Name: colSealSeq, Kind: model.KindInt},
			{Name: colSealedAt, Kind: model.KindTimestamp, Nullable: true},
			{Name: colSealReason, Kind: model.KindText, Nullable: true, Principal: pdeclNoneSealReason},
			{Name: colConsentAt, Kind: model.KindTimestamp, Nullable: true},
			{Name: colConsentMode, Kind: model.KindText, Principal: pdeclNoneConsentMode},
			{Name: colBGGrant, Kind: model.KindText, Nullable: true, Indexed: true, Principal: pdeclNoneGrant},
			{Name: colSummary, Kind: model.KindText, Nullable: true, Principal: pdeclSummary},
			{Name: colSummaryMeta, Kind: model.KindJSON, Nullable: true, Principal: pdeclNoneSummaryMeta},
			{Name: colRetention, Kind: model.KindText, Principal: pdeclNoneRetention},
			{Name: colOpenGuard, Kind: model.KindText, Nullable: true, Principal: pdeclNoneOpenGuard},
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
			{Name: colFrSession, Kind: model.KindUUID, Indexed: true, Principal: pdeclNoneSessionID},
			{Name: colFrIdx, Kind: model.KindInt},
			{Name: colFrAt, Kind: model.KindTimestamp},
			{Name: colFrActor, Kind: model.KindText, Principal: pdeclActor},
			{Name: colFrActorKind, Kind: model.KindText, Principal: pdeclNoneActorKind},
			{Name: colFrActorUser, Kind: model.KindText, Nullable: true, Principal: pdeclUserID},
			{Name: colFrActAs, Kind: model.KindText, Nullable: true, Principal: pdeclUserID},
			{Name: colFrNamespace, Kind: model.KindText, Indexed: true, Principal: pdeclNoneRoute},
			{Name: colFrMethod, Kind: model.KindText, Principal: pdeclNoneRoute},
			{Name: colFrPattern, Kind: model.KindText, Principal: pdeclNoneRoute},
			{Name: colFrPerm, Kind: model.KindText, Principal: pdeclNoneRoute},
			{Name: colFrParams, Kind: model.KindJSON, Nullable: true, Principal: pdeclParams},
			{Name: colFrQueryKeys, Kind: model.KindText, Nullable: true, Principal: pdeclNoneQueryKeys},
			{Name: colFrStatus, Kind: model.KindInt},
			{Name: colFrOutcome, Kind: model.KindText, Principal: pdeclNoneOutcome},
			{Name: colFrBodySHA, Kind: model.KindText, Nullable: true, Principal: pdeclNoneHash},
			{Name: colFrBodyBytes, Kind: model.KindInt},
			{Name: colFrDurMS, Kind: model.KindInt},
			{Name: colFrPrevHash, Kind: model.KindText, Principal: pdeclNoneHash},
			{Name: colFrHash, Kind: model.KindText, Principal: pdeclNoneHash},
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
			{Name: colCfgKey, Kind: model.KindText, Principal: pdeclNoneCfgKey},
			{Name: colCfgNS, Kind: model.KindJSON, Principal: pdeclNamespaces},
			{Name: colCfgConsent, Kind: model.KindText, Principal: pdeclNoneConsent},
			{Name: colCfgIdleSecs, Kind: model.KindInt},
			{Name: colCfgRetention, Kind: model.KindInt},
			{Name: colCfgAI, Kind: model.KindBool},
			{Name: colCfgUpdatedBy, Kind: model.KindText, Principal: pdeclActor},
		},
		Indexes: []model.IndexSpec{{
			Name:    "recording_config_uniq",
			Columns: []string{model.ColTenantID, colCfgKey},
			Unique:  true,
		}},
	})
}
