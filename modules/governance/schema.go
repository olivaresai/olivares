// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Owned entity kinds and their physical tables. Table names are all within the
// 40-char module-table cap (registry.go): the longest, governance_collection_member
// and governance_approval_decision, are 28 chars.
const (
	collectionKind  model.Kind = "governance.collection"
	collectionTable            = "governance_collection"
	memberKind      model.Kind = "governance.collection_member"
	memberTable                = "governance_collection_member"
	approvalKind    model.Kind = "governance.approval"
	approvalTable              = "governance_approval"
	decisionKind    model.Kind = "governance.approval_decision"
	decisionTable              = "governance_approval_decision"
	// the immutable policy-revision history backing the managed-* authoring
	// console (B) and the Cedar/OPA editor (C). Append-only — a published revision is
	// never rewritten, so rollback/audit always have the exact bytes (docs/SECURITY-HARDENING.md).
	revisionKind  model.Kind = "governance.policy_revision"
	revisionTable string     = "governance_policy_revision" // 26 chars (< 40-char cap)
	// break-glass emergency access — a time-boxed, audited, notified grant
	// (mutable lifecycle) plus its APPEND-ONLY use trail, so every action that
	// proceeded under the emergency window is immutable evidence (docs/SECURITY-HARDENING.md).
	breakGlassKind     model.Kind = "governance.breakglass"
	breakGlassTable               = "governance_breakglass"     // 21 chars
	breakGlassUseKind  model.Kind = "governance.breakglass_use" //
	breakGlassUseTable            = "governance_breakglass_use" // 25 chars
	// NHI lifecycle — the per-identity governance overlay (rotation policy,
	// staleness/enforcement state, owner/sponsor, offboarding cascade) keyed on the
	// roster's external_id convergence anchor, plus its APPEND-ONLY event trail so
	// every rotation/offboarding/escalation is immutable evidence (docs/SECURITY-HARDENING.md SEC-G2). It NEVER stores a credential — only non-secret lifecycle
	// metadata; the minted secret of a rotation is returned once to the caller and
	// recorded nowhere (the WIF MintedToken rule).
	nhiLifecycleKind  model.Kind = "governance.nhi_lifecycle"
	nhiLifecycleTable            = "governance_nhi_lifecycle"       // 24 chars
	nhiEventKind      model.Kind = "governance.nhi_lifecycle_event" //
	nhiEventTable                = "governance_nhi_lifecycle_event" // 30 chars (< 40-char cap)
	// the policy truth loop. distribution is the APPEND-ONLY record of the
	// signed artifact a publish handed to the seam (the exact distributed bytes
	// plus the detached Ed25519 signature an agent pull verifies — docs/SECURITY-HARDENING.md);
	// observed is the mutable latest-state OBSERVED host config per (surface, scope),
	// written by the attested check-in and read by the PERMITTED-vs-OBSERVED drift.
	distributionKind  model.Kind = "governance.policy_distribution"
	distributionTable            = "governance_policy_distribution" // 30 chars (< 40-char cap)
	observedKind      model.Kind = "governance.policy_observed"     //
	observedTable                = "governance_policy_observed"     // 26 chars
	// the estate kill switch — one mutable lifecycle row per emergency stop
	// (active → reenabled, with a forced post-review closing the incident), plus
	// the guardian-agent rule set (operator-authored auto-containment) and the
	// guardian action trail (what a rule did or queued for approval). The stop row
	// is the single source of truth every actuation gate consults (deny-closed);
	// the ledger — anchored by engage_audit_seq — is its incident timeline.
	killSwitchKind      model.Kind = "governance.killswitch"
	killSwitchTable                = "governance_killswitch" // 21 chars
	guardianRuleKind    model.Kind = "governance.guardian_rule"
	guardianRuleTable              = "governance_guardian_rule" // 24 chars
	guardianActionKind  model.Kind = "governance.guardian_action"
	guardianActionTable            = "governance_guardian_action" // 26 chars
	// the per-agent risk/autonomy profile — operator-declared and/or
	// heuristic-suggested tier with the full governance lifecycle (classify →
	// review → override). The effective tier is mirrored onto Agent.RiskTier
	// for hot reads; this entity carries the audit trail and signal evidence.
	agentRiskProfileKind  model.Kind = "governance.agent_risk_profile"
	agentRiskProfileTable            = "governance_agent_risk_prof" // 26 chars (< 40-char cap)
	// per-tenant routine governance policy — the operator-authored cadence,
	// concurrency and approval controls Claude Code Routines must satisfy. One
	// mutable policy row per (tenant, name); no column holds a credential.
	routinePolicyKind  model.Kind = "governance.routine_policy"
	routinePolicyTable            = "governance_routine_policy" // 24 chars (< 40-char cap)
	// D-02: the DURABLE tier-floor signal ledger. One row per (tenant,
	// canonical agent uuid, finding fingerprint) records a HIGH+ finding observed
	// for an agent; the built-in high-tier floor ("TWO high+ findings within the
	// window → auto-stop") counts these rows inside its window instead of an
	// in-memory map. This makes the mandatory floor DURABLE (survives a restart
	// between the two findings), CANONICAL (keyed on the resolved agent UUID, so a
	// UUID reference and an external-id reference of the same agent SUM and two
	// tenants' identical external ids never collide) and IDEMPOTENT (a re-delivered
	// finding does not double-count). No column can hold a credential.
	tierFloorSignalKind  model.Kind = "governance.tier_floor_signal"
	tierFloorSignalTable            = "governance_tier_floor_signal" // 28 chars (< 40-char cap)
	// scoped administration + custom roles. customRole/permGroup are tenant-wide
	// REUSABLE permission bundles (verb+kind); scopedGrant binds a subject to a role
	// within a scope (workspace/agent-group/resource-class). All three project to the
	// per-tenant `cedar-managed` policy the engine enforces (scopedadmin.go). The
	// grant table holds only ACTIVE grants — a revoke deletes the row; the alta/baja
	// history is the immutable audit ledger.
	customRoleKind   model.Kind = "governance.custom_role"
	customRoleTable             = "governance_custom_role" // 22 chars
	permGroupKind    model.Kind = "governance.permission_group"
	permGroupTable              = "governance_permission_group" // 27 chars
	scopedGrantKind  model.Kind = "governance.scoped_grant"
	scopedGrantTable            = "governance_scoped_grant" // 23 chars
)

// custom_role / permission_group columns (a reusable, tenant-wide permission
// bundle of verb+kind permissions; minimal data — no column holds a credential).
const (
	colRBACName        = "name" // tenant-unique operator identity
	colRBACDisplayName = "display_name"
	colRBACDescription = "description"
	colRBACPerms       = "permissions" // JSON array of "<kind>:<verb>" strings (direct perms)
	colRBACGroups      = "groups"      // JSON array of permission-group names a custom role includes
	colRBACCreatedBy   = "created_by"  // audit-actor string — provenance only
	// structured subtraction. A custom role may name a BUILT-IN role as its base
	// and subtract from it, so "editor except models:keys:write" is one row that stays
	// true as the catalog grows, instead of a snapshot enumeration that silently goes
	// stale the day a module declares a new permission.
	colRBACBaseRole = "base_role" // "" | a built-in role name whose perms this role starts from
	colRBACExcludes = "excludes"  // JSON array of "<kind>:<verb>" subtracted LAST
)

// scoped_grant columns (subject ← role @ scope; one active grant per the
// unique tuple). scope_ref/scope_class store "" (not NULL) so the unique index
// deduplicates deterministically on SQLite and Postgres.
const (
	colSGSubjectKind = "subject_kind" // user | role | group (S256)
	colSGSubjectRef  = "subject_ref"  // user id (user) | built-in role name (role) | directory group id (group)
	colSGRole        = "grant_role"   // role conferred: built-in name or custom-role name
	colSGRoleCustom  = "role_custom"  // true ⇒ grant_role names a custom role (else a built-in)
	colSGScopeTree   = "scope_tree"   // tenant | workspace | agent_group
	colSGScopeRef    = "scope_ref"    // workspace/agent-group slug ("" for tenant)
	colSGScopeClass  = "scope_class"  // resource-kind filter ("" = any scopeable kind)
	colSGCreatedBy   = "created_by"   // audit-actor string — provenance only
	colSGNote        = "note"         // optional bounded operator prose
)

// nhi_lifecycle columns (the mutable per-NHI lifecycle overlay).
const (
	colNHIIdentityRef  = "identity_ref" // external_id of the NHI — the convergence anchor
	colNHISource       = "source"       // provider/source (vault, anthropic, spiffe…)
	colNHICriticality  = "criticality"  // OWASP tier vocabulary: low|medium|high|critical (operator word)
	colNHIOwnerRef     = "owner_ref"    // external_id of a roster HUMAN identity accountable for the NHI
	colNHIOwnerActor   = "owner_actor"  // audit-actor that assigned the owner (provenance)
	colNHISponsorRef   = "sponsor_ref"  // external_id of a roster HUMAN identity sponsoring the NHI
	colNHISponsorActor = "sponsor_actor"
	colNHIRotatedAt    = "rotated_at"       // last KNOWN rotation; null = unknown (honest coverage gap)
	colNHIMaxAgeSec    = "max_age_seconds"  // rotation window; 0 = inherit the criticality default
	colNHITargetRef    = "rotation_target"  // operator-declared actuation target (e.g. "approle:ci")
	colNHIStaleStatus  = "staleness_status" // ok|stale|unknown — materialized by the sweep
	colNHIStaleSince   = "stale_since"      // when it first went stale (drives the 30-day escalation)
	colNHIBlockAfter   = "block_after"      // computed escalation deadline (alert → block)
	colNHIEnforce      = "enforcement"      // monitor|alert|blocked — what the PEP consults
	colNHIEnforceWhy   = "enforcement_reason"
	colNHIOrphaned     = "orphaned"       // sponsor disabled/missing OR registry-asserted (lifecycle trigger)
	colNHIOffboard     = "offboard_state" // none|soft_deleted|finalized
	colNHISoftAt       = "soft_deleted_at"
	colNHIRecoverUntil = "recovery_until" // audited soft-delete recovery window
	// colNHIRegistryOrphan is the agent REGISTRY's own orphan assertion
	// (an Entra agent identity whose blueprint is gone), written by the roster
	// federation bridge and ORed into `orphaned` by the sweep — kept as its own
	// column precisely so the sweep's sponsor-liveness recomputation never
	// clobbers a registry-asserted orphan. Nullable: added post-reconciled
	// additively onto existing tables.
	colNHIRegistryOrphan = "registry_orphaned"
	// colNHIKind distinguishes agent NHIs (sponsor-mandatory) from generic
	// NHIs. Nullable for additive reconciliation onto live tables; empty string or
	// NULL = legacy (no agent constraints applied).
	colNHIKind = "kind"
)

// nhi_lifecycle_event columns (the append-only lifecycle trail).
const (
	colNHIEvtIdentity = "identity_ref"
	colNHIEvtKind     = "event" // assigned|policy|rotated|retired|offboard_soft|offboard_finalize|restored|stale|escalated|blocked|unblocked|orphaned
	colNHIEvtActor    = "actor"
	colNHIEvtUser     = "actor_user"
	colNHIEvtDetail   = "detail" // bounded, non-secret (capability detail / approval ref)
	colNHIEvtAt       = "occurred_at"
)

// policy_revision columns (an immutable authored-policy revision).
const (
	colRevSurface   = "surface"   // managed-settings|hooks|managed-mcp|sandbox|cedar|cedar-managed|cedar-ddil|opa
	colRevNumber    = "revision"  // monotonic per (tenant, surface)
	colRevContent   = "content"   // the authored document (JSON / Cedar / Rego source)
	colRevAuthor    = "author"    // audit-actor string — provenance only, never a secret
	colRevValidated = "validated" // passed validation/compilation when stored
	colRevActive    = "active"    // PDP (cedar/opa): the currently-activated revision
	colRevNote      = "note"      // optional, bounded publish note (operator prose)
)

// policy_distribution columns (the immutable signed-artifact record). The
// rendered bytes are stored verbatim so a pull serves the EXACT bytes that were
// signed (never a re-derivation that a later canonicalization change could break).
const (
	colDistSurface  = "surface"         // managed-settings|hooks|managed-mcp|sandbox
	colDistRevision = "revision"        // the policy_revision this artifact distributes
	colDistRendered = "rendered"        // the exact distributed bytes (canonical for managed-settings)
	colDistSHA      = "artifact_sha256" // hex SHA-256 of the rendered bytes
	colDistSig      = "signature"       // base64 detached Ed25519 over the domain-separated hash
	colDistPubKey   = "pubkey"          // base64 verifier public key (not a secret)
	colDistKeyFP    = "key_fp"          // short signer-key fingerprint (display/pinning)
	colDistSignedAt = "signed_at"
)

// policy_observed columns (the latest OBSERVED host config per scope).
// content is an OBSERVED policy document — the check-in handler redacts any
// inline credential before it reaches this row (docs/SECURITY-HARDENING.md).
const (
	colObsSurface     = "surface"
	colObsScope       = "scope"          // host id / org-distribution name (VerifyDriftJSON attribution)
	colObsContent     = "content"        // the observed document (credential-redacted)
	colObsContentSHA  = "content_sha256" // hex SHA-256 of the stored (post-redaction) content
	colObsReportedRev = "reported_revision"
	colObsReportedSHA = "reported_artifact_sha256" // the artifact hash the agent attests it verified+applied
	colObsVerified    = "verified"                 // the attestation matched the distribution record
	colObsReporter    = "reporter"                 // audit-actor of the check-in (provenance)
	colObsCheckedInAt = "checked_in_at"
	colObsDriftCount  = "drift_count"   // findings produced by the LAST drift computation
	colObsDriftAt     = "last_drift_at" // when drift was last computed for this scope
)

// collection columns (a directory group/role/policy mirrored from a source).
const (
	colSource      = "source"
	colColRef      = "col_ref"
	colColKind     = "col_kind"
	colDisplayName = "display_name"
	colAttributes  = "attributes"
)

// collection_member columns (a membership edge: member ∈ collection).
const (
	colMemberRef     = "member_ref"
	colMemberKind    = "member_kind"
	colCollectionRef = "collection_ref"
)

// approval columns (a human-in-the-loop request; mutable lifecycle).
const (
	colSubjectKind      = "subject_kind"
	colSubjectRef       = "subject_ref"
	colAction           = "action"
	colRequestedBy      = "requested_by"      // audit-actor string (user:<id>/token:<id>) — provenance only
	colRequestedByUser  = "requested_by_user" // stable user id — the separation-of-duty identity
	colStatus           = "status"
	colRequiredApproval = "required_approvals"
	colApproveCount     = "approve_count"
	colRejectCount      = "reject_count"
	colReason           = "reason"
	colPolicyRef        = "policy_ref"
	colExpiresAt        = "expires_at"
	colEscalateAt       = "escalate_at"
	colEscalatedAt      = "escalated_at" // set once when escalation emits — gates the finding against double-emit
	colDecidedAt        = "decided_at"
	// (F-02) single-use consume of an APPROVED request. colConsumedBy is the
	// stable id of the one caller (the Claude Code tool_use_id) that spent the
	// approval; colConsumedAt is when. An approved request is a ONE-SHOT token: the
	// first consumer wins and is recorded here, a re-consume by the SAME consumer is
	// idempotent (a legitimate transport retry re-obtains the grant), and a consume
	// by ANY OTHER caller is a would-replay denial. Both are NULL until first consume,
	// so a never-consumed approval is unambiguous.
	colConsumedBy = "consumed_by"
	colConsumedAt = "consumed_at"
)

// approval_decision columns (the append-only, immutable human-decision trail).
const (
	colApprovalID  = "approval_id"
	colDecision    = "decision"
	colDecider     = "decider"      // audit-actor string — provenance in the trail
	colDeciderUser = "decider_user" // stable user id — SoD + duplicate-decider key
	colLevel       = "level"
	colNote        = "note"
)

// breakglass grant columns (a time-boxed emergency-access window; mutable
// lifecycle active → revoked|expired, with a one-shot post-review record).
const (
	colBGMatchAction     = "match_action"      // action scope: "" all, exact, or trailing-* prefix
	colBGReason          = "reason"            // the operator's justification (required, bounded prose)
	colBGActivatedBy     = "activated_by"      // audit-actor string — provenance only
	colBGActivatedByUser = "activated_by_user" // stable user id — the review SoD identity
	colBGStatus          = "status"
	colBGActivatedAt     = "activated_at"
	colBGExpiresAt       = "expires_at"
	colBGRevokedAt       = "revoked_at"
	colBGUseCount        = "use_count"
	colBGReviewed        = "reviewed" // false until the forced post-review lands
	colBGReviewedAt      = "reviewed_at"
	colBGReviewedBy      = "reviewed_by"
	colBGReviewedByUser  = "reviewed_by_user"
	colBGReviewNote      = "review_note"
	// activeGuard is a nullable sentinel that backs the "at most one UNREVIEWED grant
	// per tenant" invariant at the DB level (the friendly app-level check is the fast
	// path; this is the hard backstop against a concurrent-activation race on
	// Postgres, mirroring the decision table's unique index). It holds the constant
	// bgActiveGuard while the grant blocks new activations (from activation until the
	// post-review), and NULL once reviewed — and a unique (tenant_id, active_guard)
	// index treats NULLs as distinct on both SQLite and Postgres, so reviewed grants
	// never collide while a second unreviewed one in the same tenant does.
	colBGActiveGuard = "active_guard"
)

// breakglass_use columns (the append-only trail of every action authorized under
// an emergency grant — the "never a silent bypass" evidence).
const (
	colBGUseGrant   = "grant_id"
	colBGUsedBy     = "used_by"
	colBGUsedByUser = "used_by_user"
	colBGUsedAt     = "used_at"
)

// killswitch columns (one emergency-stop row; mutable lifecycle active →
// reenabled, with a one-shot forced post-review closing the incident).
const (
	colKSScopeKind     = "scope_kind" // estate | agent
	colKSScopeRef      = "scope_ref"  // operator-given agent ref ("" for estate)
	colKSAgentID       = "agent_id"   // resolved core agent UUID (when resolvable)
	colKSAgentExternal = "agent_external_id"
	colKSStatus        = "status" // active | reenabled
	colKSReason        = "reason" // the operator's justification (required, bounded prose)
	colKSSource        = "source" // operator | guardian
	colKSRuleRef       = "rule_ref"
	colKSEngagedBy     = "engaged_by"      // audit-actor string — provenance only
	colKSEngagedByUser = "engaged_by_user" // stable user id ("" for a guardian engage)
	colKSEngagedAAL    = "engaged_aal"     // the engaging session's recorded assurance level
	colKSEngagedAt     = "engaged_at"
	colKSEngageSeq     = "engage_audit_seq" // ledger anchor: the engage event's Seq (evidence pack start)
	colKSRevokedCount  = "revoked_approvals"
	colKSReenableAppr  = "reenable_approval" // the dual-control approval bound to THIS stop
	colKSReenableReqBy = "reenable_requested_by_user"
	colKSReenabledBy   = "reenabled_by"
	colKSReenabledUser = "reenabled_by_user"
	colKSReenabledAt   = "reenabled_at"
	colKSReenableSeq   = "reenable_audit_seq"
	colKSReviewed      = "reviewed" // false until the forced post-review lands
	colKSReviewedAt    = "reviewed_at"
	colKSReviewedBy    = "reviewed_by"
	colKSReviewedUser  = "reviewed_by_user"
	colKSReviewNote    = "review_note"
	// activeGuard backs two invariants with one nullable sentinel and the unique
	// (tenant_id, active_guard) index (the break-glass pattern, NULLs distinct on
	// SQLite AND Postgres): "stop:<scopeKey>" while the stop is active permits at
	// most ONE active stop per scope, and "review:<scopeKey>" from re-enable until
	// the post-review blocks a SECOND re-enable of the same scope while a prior
	// incident remains unexamined — engaging a new stop is never blocked (an
	// emergency must not queue behind paperwork), only re-enabling is.
	colKSActiveGuard = "active_guard"
)

// guardian_rule columns (an operator-authored auto-containment rule the
// finding.reported loop evaluates; deny-closed — only an enabled rule ever acts).
const (
	colGRName        = "name"
	colGREnabled     = "enabled"
	colGRMatchKinds  = "match_kinds"  // comma-separated exact finding kinds; "" = any (self-kinds always excluded)
	colGRMinSeverity = "min_severity" // low|medium|high|critical floor a finding must reach
	colGRAction      = "action"       // stop_agent | quarantine_nhi | stop_estate
	colGRMode        = "mode"         // auto | approval (per-rule configurable HITL)
	colGRCreatedBy   = "created_by"   // audit-actor string — provenance only
	colGRNote        = "note"
	// optional agent-tier filter — the rule fires only for findings about
	// agents at or above this tier. Empty = no tier filter (any agent).
	colGRAgentTier = "agent_tier"
)

// guardian_action columns (what a rule did — or queued for human approval
// — in response to one finding; the guardian loop's evidence trail).
const (
	colGARule         = "rule_id"
	colGARuleName     = "rule_name"
	colGAFindingKind  = "finding_kind"
	colGAFindingRef   = "finding_ref" // the finding's DetailHash hex — the dedup identity
	colGASeverity     = "finding_severity"
	colGATargetKind   = "target_kind" // agent | identity | estate
	colGATargetRef    = "target_ref"
	colGAAction       = "action"
	colGAMode         = "mode"
	colGAStatus       = "status" // pending | executed | rejected | expired | failed
	colGAApprovalID   = "approval_id"
	colGAKillswitchID = "killswitch_id"
	colGADetail       = "detail" // bounded, non-secret outcome detail
	colGAExecutedAt   = "executed_at"
)

// agent_risk_profile columns (the per-agent governance risk/autonomy tier
// with full lifecycle — operator declaration, heuristic suggestion, review).
const (
	colARPAgentID       = "agent_id"       // core agent UUID — the subject
	colARPOperatorTier  = "operator_tier"  // nullable — operator's authoritative declaration
	colARPSuggestedTier = "suggested_tier" // nullable — heuristic output
	colARPEffectiveTier = "effective_tier" // = coalesce(operator_tier, suggested_tier)
	colARPState         = "state"          // unclassified | suggested | reviewed
	colARPSignals       = "signals"        // JSON — the evidence that drove suggested_tier
	colARPReviewedBy    = "reviewed_by"    // audit-actor who reviewed
	colARPReviewedAt    = "reviewed_at"    // when
)

// routine_policy columns (per-tenant routine governance policy — cadence,
// concurrency and approval controls for Claude Code Routines).
const (
	colRPName            = "name"                  // operator-facing policy name
	colRPScopeKind       = "scope_kind"            // tenant | workspace | user
	colRPScopeRef        = "scope_ref"             // workspace/user ref ("" for tenant)
	colRPEnabled         = "enabled"               // policy active
	colRPMaxCadenceSec   = "max_cadence_seconds"   // minimum interval floor
	colRPMaxActive       = "max_active_routines"   // cap on concurrent active routines
	colRPRequireApproval = "require_approval"      // creation requires HITL
	colRPAllowedCron     = "allowed_cron_patterns" // JSON array of allowed cron patterns (null = any)
	colRPBlockedEnvs     = "blocked_environments"  // JSON array of blocked environment IDs (null = none)
	colRPCreatedBy       = "created_by"            // audit-actor string — provenance only
)

// tier_floor_signal columns (D-02: the durable high-tier-floor counter).
// observed_at is the SERVER instant the signal was recorded (m.clock.Now), not
// the finding's self-reported OccurredAt — a producer cannot backdate a finding
// out of the window to evade the floor, and a clock rollback only widens the
// window (counts MORE, deny-closed), never loses a persisted signal.
const (
	colTFSAgentID     = "agent_id"     // resolved core agent UUID — the count key (with tenant_id)
	colTFSFingerprint = "fingerprint"  // finding dedup identity (guardianFindingRef) — idempotent insert
	colTFSSeverity    = "severity"     // the finding's severity (evidence)
	colTFSFindingKind = "finding_kind" // the finding's kind (evidence)
	colTFSObservedAt  = "observed_at"  // server instant the signal was recorded — the window boundary
)

// RegisterSchema declares the module's owned entities. It satisfies the
// engine-side runtime.SchemaProvider seam (structural — no runtime import) and is
// called once, at store construction, before any Scope exists (S02 §7 /):
// the engine creates the tables, injects the base columns and attaches the tenant,
// audit and append-only guards. A module cannot opt out of isolation.
//
// Minimal data (docs/SECURITY-HARDENING.md): no column can hold a usable credential. The
// collection attributes are an allow-listed, PII-stripped JSON map (roster.go);
// an approval reason / decision note is bounded operator prose, never echoed into
// audit Meta or a Finding title. The decision trail is APPEND-ONLY so the
// action→human evidence cannot be silently rewritten (docs/SECURITY-HARDENING.md).
//
// None of the entities is descriptor-Audited: roster/sync writes are high-
// frequency and the privileged mutations (sync, bind, policy, decision, sweep,
// break-glass activate/use/revoke/review) each append a SEMANTIC self-audit
// attributed to the real principal in their own transaction (helpers.go
// auditEvent), which the per-row engine audit could not attribute.
func (m *Module) RegisterSchema(reg store.ExtensionRegistry) error {
	if err := reg.Register(model.EntityDescriptor{
		Kind:  collectionKind,
		Table: collectionTable,
		Fields: []model.FieldSpec{
			{Name: colSource, Kind: model.KindText, Indexed: true},
			{Name: colColRef, Kind: model.KindText, Indexed: true},
			{Name: colColKind, Kind: model.KindText, Indexed: true},
			{Name: colDisplayName, Kind: model.KindText, Nullable: true},
			{Name: colAttributes, Kind: model.KindJSON, Nullable: true},
		},
		Indexes: []model.IndexSpec{{
			// One collection row per (source, ref). Unique index leads with
			// tenant_id so it cannot couple tenants or leak existence.
			Name:    "governance_collection_uniq",
			Columns: []string{model.ColTenantID, colSource, colColRef},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	if err := reg.Register(model.EntityDescriptor{
		Kind:  memberKind,
		Table: memberTable,
		Fields: []model.FieldSpec{
			{Name: colSource, Kind: model.KindText, Indexed: true},
			{Name: colCollectionRef, Kind: model.KindText, Indexed: true},
			{Name: colMemberRef, Kind: model.KindText, Indexed: true},
			{Name: colMemberKind, Kind: model.KindText},
		},
		Indexes: []model.IndexSpec{{
			// One membership edge per (source, collection, member).
			Name:    "governance_collection_member_uniq",
			Columns: []string{model.ColTenantID, colSource, colCollectionRef, colMemberRef},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	if err := reg.Register(model.EntityDescriptor{
		Kind:  approvalKind,
		Table: approvalTable,
		Fields: []model.FieldSpec{
			{Name: colSubjectKind, Kind: model.KindText, Indexed: true},
			{Name: colSubjectRef, Kind: model.KindText, Indexed: true},
			{Name: colAction, Kind: model.KindText, Indexed: true},
			{Name: colRequestedBy, Kind: model.KindText},
			{Name: colRequestedByUser, Kind: model.KindText, Indexed: true},
			{Name: colStatus, Kind: model.KindText, Indexed: true},
			{Name: colRequiredApproval, Kind: model.KindInt},
			{Name: colApproveCount, Kind: model.KindInt},
			{Name: colRejectCount, Kind: model.KindInt},
			{Name: colReason, Kind: model.KindText, Nullable: true},
			{Name: colPolicyRef, Kind: model.KindText, Nullable: true},
			{Name: colExpiresAt, Kind: model.KindTimestamp, Nullable: true, Indexed: true},
			{Name: colEscalateAt, Kind: model.KindTimestamp, Nullable: true},
			{Name: colEscalatedAt, Kind: model.KindTimestamp, Nullable: true},
			{Name: colDecidedAt, Kind: model.KindTimestamp, Nullable: true},
			// (F-02): single-use consume markers, NULL until first consume. Added
			// nullable so the store's additive reconcile materializes them on an existing
			// table without a hand-authored migration (sqlstore reconcileColumns).
			{Name: colConsumedBy, Kind: model.KindText, Nullable: true, Indexed: true},
			{Name: colConsumedAt, Kind: model.KindTimestamp, Nullable: true},
		},
	}); err != nil {
		return err
	}

	if err := reg.Register(model.EntityDescriptor{
		Kind:       decisionKind,
		Table:      decisionTable,
		AppendOnly: true, // immutable action→human evidence (docs/SECURITY-HARDENING.md)
		Fields: []model.FieldSpec{
			{Name: colApprovalID, Kind: model.KindUUID, Indexed: true},
			{Name: colDecision, Kind: model.KindText},
			{Name: colDecider, Kind: model.KindText},
			{Name: colDeciderUser, Kind: model.KindText, Indexed: true},
			{Name: colLevel, Kind: model.KindInt, Nullable: true},
			{Name: colNote, Kind: model.KindText, Nullable: true},
			{Name: colDecidedAt, Kind: model.KindTimestamp},
		},
		Indexes: []model.IndexSpec{{
			// One decision per (approval, decider user): the hard, DB-level
			// duplicate-decider guard backing the app-level check (requires
			// the unique index to lead with tenant_id).
			Name:    "governance_approval_decision_uniq",
			Columns: []string{model.ColTenantID, colApprovalID, colDeciderUser},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	// the break-glass grant — a mutable lifecycle row (active → revoked |
	// expired) carrying the time-box, the activator identity (the post-review SoD
	// key) and the one-shot review record. Reason/review_note are bounded operator
	// prose (mirroring an approval reason), never echoed into audit Meta.
	if err := reg.Register(model.EntityDescriptor{
		Kind:  breakGlassKind,
		Table: breakGlassTable,
		Fields: []model.FieldSpec{
			{Name: colBGMatchAction, Kind: model.KindText, Nullable: true},
			{Name: colBGReason, Kind: model.KindText},
			{Name: colBGActivatedBy, Kind: model.KindText},
			{Name: colBGActivatedByUser, Kind: model.KindText, Indexed: true},
			{Name: colBGStatus, Kind: model.KindText, Indexed: true},
			{Name: colBGActivatedAt, Kind: model.KindTimestamp},
			{Name: colBGExpiresAt, Kind: model.KindTimestamp, Indexed: true},
			{Name: colBGRevokedAt, Kind: model.KindTimestamp, Nullable: true},
			{Name: colBGUseCount, Kind: model.KindInt},
			{Name: colBGReviewed, Kind: model.KindBool, Indexed: true},
			{Name: colBGReviewedAt, Kind: model.KindTimestamp, Nullable: true},
			{Name: colBGReviewedBy, Kind: model.KindText, Nullable: true},
			{Name: colBGReviewedByUser, Kind: model.KindText, Nullable: true},
			{Name: colBGReviewNote, Kind: model.KindText, Nullable: true},
			{Name: colBGActiveGuard, Kind: model.KindText, Nullable: true},
		},
		Indexes: []model.IndexSpec{{
			// At most one UNREVIEWED grant per tenant: the hard backstop the app-level
			// check (and handleConsumeBreakGlass's "one active at a time" assumption)
			// rely on under concurrency. Leads with tenant_id per NULLs are
			// distinct so reviewed grants (active_guard NULL) never collide.
			Name:    "governance_breakglass_active_uniq",
			Columns: []string{model.ColTenantID, colBGActiveGuard},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	// the break-glass use trail. APPEND-ONLY: every action that proceeded
	// under an emergency grant is immutable evidence (docs/SECURITY-HARDENING.md) — the use can
	// never be silently rewritten, which is what makes break-glass an audited
	// path instead of a bypass. subject_ref is bounded at the handler (it may
	// carry the plan binding); no column can hold a usable credential.
	if err := reg.Register(model.EntityDescriptor{
		Kind:       breakGlassUseKind,
		Table:      breakGlassUseTable,
		AppendOnly: true,
		Fields: []model.FieldSpec{
			{Name: colBGUseGrant, Kind: model.KindUUID, Indexed: true},
			{Name: colAction, Kind: model.KindText, Indexed: true},
			{Name: colSubjectKind, Kind: model.KindText, Nullable: true},
			{Name: colSubjectRef, Kind: model.KindText, Nullable: true},
			{Name: colBGUsedBy, Kind: model.KindText},
			{Name: colBGUsedByUser, Kind: model.KindText, Nullable: true},
			{Name: colBGUsedAt, Kind: model.KindTimestamp},
		},
	}); err != nil {
		return err
	}

	// the NHI lifecycle overlay — a mutable per-identity row (staleness,
	// enforcement, owner/sponsor, offboarding state) keyed on identity_ref ==
	// roster external_id so it converges on the SAME NHI the roster and access-map
	// bridge resolve (never a second taxonomy). No column can hold a credential.
	if err := reg.Register(model.EntityDescriptor{
		Kind:                   nhiLifecycleKind,
		Table:                  nhiLifecycleTable,
		AuthorizationFact:      true,
		AuthorizationLockOrder: 30,
		Fields: []model.FieldSpec{
			{Name: colNHIIdentityRef, Kind: model.KindText, Indexed: true},
			{Name: colNHISource, Kind: model.KindText, Nullable: true, Indexed: true},
			{Name: colNHICriticality, Kind: model.KindText},
			{Name: colNHIOwnerRef, Kind: model.KindText, Nullable: true},
			{Name: colNHIOwnerActor, Kind: model.KindText, Nullable: true},
			{Name: colNHISponsorRef, Kind: model.KindText, Nullable: true, Indexed: true},
			{Name: colNHISponsorActor, Kind: model.KindText, Nullable: true},
			{Name: colNHIRotatedAt, Kind: model.KindTimestamp, Nullable: true},
			{Name: colNHIMaxAgeSec, Kind: model.KindInt},
			{Name: colNHITargetRef, Kind: model.KindText, Nullable: true},
			{Name: colNHIStaleStatus, Kind: model.KindText, Indexed: true},
			{Name: colNHIStaleSince, Kind: model.KindTimestamp, Nullable: true},
			{Name: colNHIBlockAfter, Kind: model.KindTimestamp, Nullable: true},
			{Name: colNHIEnforce, Kind: model.KindText, Indexed: true},
			{Name: colNHIEnforceWhy, Kind: model.KindText, Nullable: true},
			{Name: colNHIOrphaned, Kind: model.KindBool, Indexed: true},
			{Name: colNHIOffboard, Kind: model.KindText, Indexed: true},
			{Name: colNHISoftAt, Kind: model.KindTimestamp, Nullable: true},
			{Name: colNHIRecoverUntil, Kind: model.KindTimestamp, Nullable: true},
			// (post-nullable => reconciled additively onto live tables).
			{Name: colNHIRegistryOrphan, Kind: model.KindBool, Nullable: true},
			// (post-nullable => reconciled additively onto live tables).
			{Name: colNHIKind, Kind: model.KindText, Nullable: true},
		},
		Indexes: []model.IndexSpec{{
			// One lifecycle row per NHI (identity_ref). Leads with tenant_id;
			// the same single-writer convergence discipline as the roster (no second
			// concurrent writer to a given identity_ref).
			Name:    "governance_nhi_lifecycle_uniq",
			Columns: []string{model.ColTenantID, colNHIIdentityRef},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	// the NHI lifecycle event trail. APPEND-ONLY: every rotation, retirement,
	// offboarding, staleness escalation and block is immutable evidence — the
	// action→outcome record that makes lifecycle auditable, never silently rewritten
	// (docs/SECURITY-HARDENING.md). No column can hold a usable credential.
	if err := reg.Register(model.EntityDescriptor{
		Kind:       nhiEventKind,
		Table:      nhiEventTable,
		AppendOnly: true,
		Fields: []model.FieldSpec{
			{Name: colNHIEvtIdentity, Kind: model.KindText, Indexed: true},
			{Name: colNHIEvtKind, Kind: model.KindText, Indexed: true},
			{Name: colNHIEvtActor, Kind: model.KindText},
			{Name: colNHIEvtUser, Kind: model.KindText, Nullable: true},
			{Name: colNHIEvtDetail, Kind: model.KindText, Nullable: true},
			{Name: colNHIEvtAt, Kind: model.KindTimestamp},
		},
	}); err != nil {
		return err
	}

	// the immutable policy-revision history. Append-only so a published
	// revision can never be silently rewritten — rollback and audit always read the
	// exact bytes that were distributed. Minimal data (docs/SECURITY-HARDENING.md): `content` is a
	// POLICY artifact (managed-settings.json / hooks / Cedar / Rego), never a
	// credential — the authoring handlers reject a document carrying an inline key.
	if err := reg.Register(model.EntityDescriptor{
		Kind:       revisionKind,
		Table:      revisionTable,
		AppendOnly: true,
		Fields: []model.FieldSpec{
			{Name: colRevSurface, Kind: model.KindText, Indexed: true},
			{Name: colRevNumber, Kind: model.KindInt, Indexed: true},
			{Name: colRevContent, Kind: model.KindText},
			{Name: colRevAuthor, Kind: model.KindText},
			{Name: colRevValidated, Kind: model.KindBool},
			{Name: colRevActive, Kind: model.KindBool, Nullable: true},
			{Name: colRevNote, Kind: model.KindText, Nullable: true},
		},
		Indexes: []model.IndexSpec{{
			// One revision number per (surface): the DB-level guard that serializes the
			// monotonic counter (a concurrent publish loses the unique-index race and
			// retries). Leads with tenant_id per.
			Name:    "governance_policy_revision_uniq",
			Columns: []string{model.ColTenantID, colRevSurface, colRevNumber},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	// one durable policy-freshness anchor per tenant. Boot normally restores it
	// unchanged; the C3 legacy backfill may create a missing local anchor once, with
	// epoch + audit evidence in the same transaction, never re-stamping an existing
	// signed or administratively-published offline-trust window.
	if err := reg.Register(model.EntityDescriptor{
		Kind:  policyFreshnessKind,
		Table: policyFreshnessTable,
		Fields: []model.FieldSpec{
			{Name: colFreshRefreshedAt, Kind: model.KindTimestamp},
			{Name: colFreshMaxStaleness, Kind: model.KindText},
			{Name: colFreshAdoptedRevision, Kind: model.KindText},
			{Name: colFreshAdoptedCreated, Kind: model.KindTimestamp, Nullable: true},
		},
		Indexes: []model.IndexSpec{{
			Name:    "governance_policy_freshness_tenant_uniq",
			Columns: []string{model.ColTenantID},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	// the signed-artifact distribution record. Append-only — the record IS
	// the distribution evidence ("distributed" is claimed only after this row
	// committed), and a pull serves these exact bytes (docs/SECURITY-HARDENING.md).
	if err := reg.Register(model.EntityDescriptor{
		Kind:       distributionKind,
		Table:      distributionTable,
		AppendOnly: true,
		Fields: []model.FieldSpec{
			{Name: colDistSurface, Kind: model.KindText, Indexed: true},
			{Name: colDistRevision, Kind: model.KindInt, Indexed: true},
			{Name: colDistRendered, Kind: model.KindText},
			{Name: colDistSHA, Kind: model.KindText},
			{Name: colDistSig, Kind: model.KindText},
			{Name: colDistPubKey, Kind: model.KindText},
			{Name: colDistKeyFP, Kind: model.KindText},
			{Name: colDistSignedAt, Kind: model.KindTimestamp},
		},
		Indexes: []model.IndexSpec{{
			// One signed artifact per (surface, revision) — a revision's distributed
			// bytes are minted exactly once.
			Name:    "governance_policy_dist_uniq",
			Columns: []string{model.ColTenantID, colDistSurface, colDistRevision},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	// the latest OBSERVED host config per (surface, scope) — the check-in
	// upserts it; PERMITTED-vs-OBSERVED drift reads it. Latest-state by design
	// (the chronology is the audit trail of governance.claude_policy.checkin).
	if err := reg.Register(model.EntityDescriptor{
		Kind:  observedKind,
		Table: observedTable,
		Fields: []model.FieldSpec{
			{Name: colObsSurface, Kind: model.KindText, Indexed: true},
			{Name: colObsScope, Kind: model.KindText, Indexed: true},
			{Name: colObsContent, Kind: model.KindText},
			{Name: colObsContentSHA, Kind: model.KindText},
			{Name: colObsReportedRev, Kind: model.KindInt, Nullable: true},
			{Name: colObsReportedSHA, Kind: model.KindText, Nullable: true},
			{Name: colObsVerified, Kind: model.KindBool},
			{Name: colObsReporter, Kind: model.KindText},
			{Name: colObsCheckedInAt, Kind: model.KindTimestamp},
			{Name: colObsDriftCount, Kind: model.KindInt},
			{Name: colObsDriftAt, Kind: model.KindTimestamp, Nullable: true},
		},
		Indexes: []model.IndexSpec{{
			// One observed row per (surface, scope): check-ins upsert the latest state.
			Name:    "governance_policy_observed_uniq",
			Columns: []string{model.ColTenantID, colObsSurface, colObsScope},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	// the kill-switch stop row — a mutable lifecycle row (active →
	// reenabled → reviewed) that is the SINGLE source of truth for "is this
	// estate/agent stopped". reason/review_note are bounded operator prose
	// (mirroring an approval reason), never echoed into audit Meta.
	if err := reg.Register(model.EntityDescriptor{
		Kind:  killSwitchKind,
		Table: killSwitchTable,
		Fields: []model.FieldSpec{
			{Name: colKSScopeKind, Kind: model.KindText, Indexed: true},
			{Name: colKSScopeRef, Kind: model.KindText, Nullable: true, Indexed: true},
			{Name: colKSAgentID, Kind: model.KindUUID, Nullable: true},
			{Name: colKSAgentExternal, Kind: model.KindText, Nullable: true},
			{Name: colKSStatus, Kind: model.KindText, Indexed: true},
			{Name: colKSReason, Kind: model.KindText},
			{Name: colKSSource, Kind: model.KindText},
			{Name: colKSRuleRef, Kind: model.KindUUID, Nullable: true},
			{Name: colKSEngagedBy, Kind: model.KindText},
			{Name: colKSEngagedByUser, Kind: model.KindText, Nullable: true},
			{Name: colKSEngagedAAL, Kind: model.KindInt},
			{Name: colKSEngagedAt, Kind: model.KindTimestamp},
			{Name: colKSEngageSeq, Kind: model.KindInt},
			{Name: colKSRevokedCount, Kind: model.KindInt},
			{Name: colKSReenableAppr, Kind: model.KindUUID, Nullable: true},
			{Name: colKSReenableReqBy, Kind: model.KindText, Nullable: true},
			{Name: colKSReenabledBy, Kind: model.KindText, Nullable: true},
			{Name: colKSReenabledUser, Kind: model.KindText, Nullable: true},
			{Name: colKSReenabledAt, Kind: model.KindTimestamp, Nullable: true},
			{Name: colKSReenableSeq, Kind: model.KindInt, Nullable: true},
			{Name: colKSReviewed, Kind: model.KindBool, Indexed: true},
			{Name: colKSReviewedAt, Kind: model.KindTimestamp, Nullable: true},
			{Name: colKSReviewedBy, Kind: model.KindText, Nullable: true},
			{Name: colKSReviewedUser, Kind: model.KindText, Nullable: true},
			{Name: colKSReviewNote, Kind: model.KindText, Nullable: true},
			{Name: colKSActiveGuard, Kind: model.KindText, Nullable: true},
		},
		Indexes: []model.IndexSpec{{
			// At most one ACTIVE stop per scope ("stop:<scopeKey>") and at most one
			// UNREVIEWED re-enabled incident per scope ("review:<scopeKey>"): the hard
			// backstop the app-level checks rely on under concurrency. Leads with
			// tenant_id per NULLs are distinct so reviewed rows never collide.
			Name:    "governance_killswitch_active_uniq",
			Columns: []string{model.ColTenantID, colKSActiveGuard},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	// the guardian rule set — operator-authored auto-containment. Mutable
	// (enable/disable is an operational act); every mutation is self-audited.
	if err := reg.Register(model.EntityDescriptor{
		Kind:  guardianRuleKind,
		Table: guardianRuleTable,
		Fields: []model.FieldSpec{
			{Name: colGRName, Kind: model.KindText, Indexed: true},
			{Name: colGREnabled, Kind: model.KindBool, Indexed: true},
			{Name: colGRMatchKinds, Kind: model.KindText, Nullable: true},
			{Name: colGRMinSeverity, Kind: model.KindText},
			{Name: colGRAction, Kind: model.KindText},
			{Name: colGRMode, Kind: model.KindText},
			{Name: colGRCreatedBy, Kind: model.KindText},
			{Name: colGRNote, Kind: model.KindText, Nullable: true},
			// agent-tier filter (nullable, reconciled additively).
			{Name: colGRAgentTier, Kind: model.KindText, Nullable: true},
		},
		Indexes: []model.IndexSpec{{
			// One rule per (tenant, name): names are the operator-facing identity.
			Name:    "governance_guardian_rule_uniq",
			Columns: []string{model.ColTenantID, colGRName},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	// a custom role — a tenant-wide, reusable bundle of verb+kind permissions
	// (optionally including permission-groups). Mutable (an operator edits the bundle);
	// every mutation is self-audited. No column holds a credential (the permissions are
	// "<kind>:<verb>" catalog strings).
	if err := reg.Register(model.EntityDescriptor{
		Kind:  customRoleKind,
		Table: customRoleTable,
		Fields: []model.FieldSpec{
			{Name: colRBACName, Kind: model.KindText, Indexed: true},
			{Name: colRBACDisplayName, Kind: model.KindText, Nullable: true},
			{Name: colRBACDescription, Kind: model.KindText, Nullable: true},
			{Name: colRBACPerms, Kind: model.KindJSON},
			{Name: colRBACGroups, Kind: model.KindJSON, Nullable: true},
			// additive nullable columns — an existing row reads back Base "" and
			// Excludes nil, which is exactly the pre semantics (no base, no subtraction).
			{Name: colRBACBaseRole, Kind: model.KindText, Nullable: true},
			{Name: colRBACExcludes, Kind: model.KindJSON, Nullable: true},
			{Name: colRBACCreatedBy, Kind: model.KindText},
		},
		Indexes: []model.IndexSpec{{
			// One role per (tenant, name): the operator-facing identity. Leads with
			// tenant_id per.
			Name:    "governance_custom_role_uniq",
			Columns: []string{model.ColTenantID, colRBACName},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	// a permission-group — a tenant-wide, reusable bundle of verb+kind permissions
	// a custom role can include. Same shape/audit/minimal-data discipline as custom_role.
	if err := reg.Register(model.EntityDescriptor{
		Kind:  permGroupKind,
		Table: permGroupTable,
		Fields: []model.FieldSpec{
			{Name: colRBACName, Kind: model.KindText, Indexed: true},
			{Name: colRBACDisplayName, Kind: model.KindText, Nullable: true},
			{Name: colRBACDescription, Kind: model.KindText, Nullable: true},
			{Name: colRBACPerms, Kind: model.KindJSON},
			{Name: colRBACCreatedBy, Kind: model.KindText},
		},
		Indexes: []model.IndexSpec{{
			Name:    "governance_permission_group_uniq",
			Columns: []string{model.ColTenantID, colRBACName},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	// a scoped grant — subject ← role @ scope, the unit projects to the
	// `cedar-managed` policy. The table holds only ACTIVE grants (revoke deletes the
	// row); the unique tuple dedupes identical grants. Mutable only by create/delete;
	// every alta/baja is self-audited. No column holds a credential.
	if err := reg.Register(model.EntityDescriptor{
		Kind:  scopedGrantKind,
		Table: scopedGrantTable,
		Fields: []model.FieldSpec{
			{Name: colSGSubjectKind, Kind: model.KindText, Indexed: true},
			{Name: colSGSubjectRef, Kind: model.KindText, Indexed: true},
			{Name: colSGRole, Kind: model.KindText, Indexed: true},
			{Name: colSGRoleCustom, Kind: model.KindBool},
			{Name: colSGScopeTree, Kind: model.KindText, Indexed: true},
			{Name: colSGScopeRef, Kind: model.KindText},
			{Name: colSGScopeClass, Kind: model.KindText},
			{Name: colSGCreatedBy, Kind: model.KindText},
			{Name: colSGNote, Kind: model.KindText, Nullable: true},
		},
		Indexes: []model.IndexSpec{{
			// One grant per (subject, role, scope): the dedup backstop. scope_ref/
			// scope_class are stored "" (never NULL) so the tuple is deterministic on
			// both engines. Leads with tenant_id per.
			Name:    "governance_scoped_grant_uniq",
			Columns: []string{model.ColTenantID, colSGSubjectKind, colSGSubjectRef, colSGRole, colSGScopeTree, colSGScopeRef, colSGScopeClass},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	// the per-agent risk/autonomy governance profile. One row per agent;
	// the effective tier is mirrored onto Agent.RiskTier for hot reads but the
	// full lifecycle (signals, operator vs suggested, review) lives here.
	if err := reg.Register(model.EntityDescriptor{
		Kind:  agentRiskProfileKind,
		Table: agentRiskProfileTable,
		Fields: []model.FieldSpec{
			{Name: colARPAgentID, Kind: model.KindUUID, Indexed: true},
			{Name: colARPOperatorTier, Kind: model.KindText, Nullable: true},
			{Name: colARPSuggestedTier, Kind: model.KindText, Nullable: true},
			{Name: colARPEffectiveTier, Kind: model.KindText, Nullable: true, Indexed: true},
			{Name: colARPState, Kind: model.KindText, Indexed: true},
			{Name: colARPSignals, Kind: model.KindJSON, Nullable: true},
			{Name: colARPReviewedBy, Kind: model.KindText, Nullable: true},
			{Name: colARPReviewedAt, Kind: model.KindTimestamp, Nullable: true},
		},
		Indexes: []model.IndexSpec{{
			Name:    "governance_agent_risk_prof_uniq",
			Columns: []string{model.ColTenantID, colARPAgentID},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	// the guardian action trail — one row per (rule, finding) the loop
	// acted on or queued. Mutable status (pending → executed/rejected/expired/
	// failed); the unique index is the dedup backstop so a re-reported finding
	// never double-fires a rule.
	if err := reg.Register(model.EntityDescriptor{
		Kind:  guardianActionKind,
		Table: guardianActionTable,
		Fields: []model.FieldSpec{
			{Name: colGARule, Kind: model.KindUUID, Indexed: true},
			{Name: colGARuleName, Kind: model.KindText},
			{Name: colGAFindingKind, Kind: model.KindText, Indexed: true},
			{Name: colGAFindingRef, Kind: model.KindText, Indexed: true},
			{Name: colGASeverity, Kind: model.KindText},
			{Name: colGATargetKind, Kind: model.KindText},
			{Name: colGATargetRef, Kind: model.KindText, Nullable: true},
			{Name: colGAAction, Kind: model.KindText},
			{Name: colGAMode, Kind: model.KindText},
			{Name: colGAStatus, Kind: model.KindText, Indexed: true},
			{Name: colGAApprovalID, Kind: model.KindUUID, Nullable: true},
			{Name: colGAKillswitchID, Kind: model.KindUUID, Nullable: true},
			{Name: colGADetail, Kind: model.KindText, Nullable: true},
			{Name: colGAExecutedAt, Kind: model.KindTimestamp, Nullable: true},
		},
		Indexes: []model.IndexSpec{{
			// One action per (rule, finding identity): the dedup backstop against
			// re-published/re-scanned findings double-firing containment.
			Name:    "governance_guardian_action_uniq",
			Columns: []string{model.ColTenantID, colGARule, colGAFindingRef},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	// routine governance policy — operator-authored cadence, concurrency and
	// approval controls for Claude Code Routines. Mutable (an operator edits the
	// policy); every mutation is self-audited. No column holds a credential.
	if err := reg.Register(model.EntityDescriptor{
		Kind:  routinePolicyKind,
		Table: routinePolicyTable,
		Fields: []model.FieldSpec{
			{Name: colRPName, Kind: model.KindText, Indexed: true},
			{Name: colRPScopeKind, Kind: model.KindText, Indexed: true},
			{Name: colRPScopeRef, Kind: model.KindText},
			{Name: colRPEnabled, Kind: model.KindBool, Indexed: true},
			{Name: colRPMaxCadenceSec, Kind: model.KindInt},
			{Name: colRPMaxActive, Kind: model.KindInt},
			{Name: colRPRequireApproval, Kind: model.KindBool},
			{Name: colRPAllowedCron, Kind: model.KindJSON, Nullable: true},
			{Name: colRPBlockedEnvs, Kind: model.KindJSON, Nullable: true},
			{Name: colRPCreatedBy, Kind: model.KindText},
		},
		Indexes: []model.IndexSpec{{
			// One policy per (tenant, name): the operator-facing identity.
			Name:    "governance_routine_policy_uniq",
			Columns: []string{model.ColTenantID, colRPName},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	// D-02: the durable tier-floor signal ledger — the count backing the
	// built-in high-tier floor. Counting persisted rows (not an in-memory map)
	// makes the mandatory "TWO high+ findings within the window → auto-stop"
	// survive a process restart. Keyed on the CANONICAL agent uuid so a finding by
	// UUID and one by external id of the same agent SUM, and two tenants' identical
	// external ids never collide. Idempotent by (tenant, agent_id, fingerprint): a
	// re-delivered finding does not double-count. Minimal data (docs/SECURITY-HARDENING.md): the
	// fingerprint is an opaque one-way hash, never the raw finding detail.
	return reg.Register(model.EntityDescriptor{
		Kind:  tierFloorSignalKind,
		Table: tierFloorSignalTable,
		Fields: []model.FieldSpec{
			{Name: colTFSAgentID, Kind: model.KindUUID, Indexed: true},
			{Name: colTFSFingerprint, Kind: model.KindText, Indexed: true},
			{Name: colTFSSeverity, Kind: model.KindText},
			{Name: colTFSFindingKind, Kind: model.KindText},
			{Name: colTFSObservedAt, Kind: model.KindTimestamp, Indexed: true},
		},
		Indexes: []model.IndexSpec{{
			// One signal per (agent, finding fingerprint): the idempotent-insert
			// backstop so a re-delivered/re-scanned finding never double-counts the
			// mandatory 2-in-window floor. Leads with tenant_id per which also
			// makes the count physically per-tenant (no cross-tenant collision).
			Name:    "governance_tier_floor_signal_uniq",
			Columns: []string{model.ColTenantID, colTFSAgentID, colTFSFingerprint},
			Unique:  true,
		}},
	})
}
