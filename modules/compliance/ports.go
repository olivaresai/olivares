// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"context"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// This file defines the SEAMS compliance declares in its OWN terms (the pattern of
// evals.SessionSource / security.Classifier): each is an optional port with a
// fail-open-LESS default — an un-wired seam yields LESS evidence (a gap), NEVER a
// fabricated pass. compliance never imports a sibling module; the composition root
// injects the real adapters. The defaults are sentinels meaning "the handler reads
// whatever the core/ext store already exposes, inline".

// ---- AutonomySource: an agent's scheduling/autonomy signal (module IV /) -----

// AutonomySignal is the MINIMAL-DATA autonomy view of an agent the risk classifier
// folds in: whether it runs on a schedule and whether it acts unattended. It is a
// signal, never a payload.
type AutonomySignal struct {
	// Scheduled reports the agent has one or more governed schedules.
	Scheduled bool
	// Autonomous reports the agent acts event-driven / unattended.
	Autonomous bool
	// Detail is a short, non-sensitive note (e.g. "3 active cron schedules").
	Detail string
}

// AutonomySource yields an agent's autonomy signal. The default returns the zero
// signal (no autonomy known — it never inflates a risk tier); a richer adapter over
// module IV's schedule graph is wired with WithAutonomySource.
type AutonomySource interface {
	Autonomy(ctx context.Context, tenant model.TenantID, agentRef string) (AutonomySignal, error)
}

// coreAutonomy is the default: it knows nothing on its own, so an un-wired autonomy
// seam can only ever LOWER the suggested tier, never raise it on a fabricated signal.
type coreAutonomy struct{}

func (coreAutonomy) Autonomy(_ context.Context, _ model.TenantID, _ string) (AutonomySignal, error) {
	return AutonomySignal{}, nil
}

// ---- LineageSource: perimeter-egress signals for the residency scan (module VIII)-

// EgressSignal is one existing signal that data may have left the perimeter — a
// data-lineage egress record or a residency-related finding. It is a reference, never
// the data itself (docs/SECURITY-HARDENING.md).
type EgressSignal struct {
	// Source names where the signal came from (e.g. "knowledge.lineage", "finding").
	Source string
	// Ref is a non-sensitive reference into the source (e.g. a lineage id).
	Ref string
	// Detail is a short, non-sensitive description.
	Detail string
}

// LineageSource yields perimeter-egress signals. The default returns nothing on its
// own; the residency scan reads the knowledge data-lineage ext entity INLINE when the
// default is wired (degrading honestly to "no signal" if that module is absent). A
// richer adapter is wired with WithLineageSource.
type LineageSource interface {
	EgressSignals(ctx context.Context, tenant model.TenantID) ([]EgressSignal, error)
}

// coreLineage is the default sentinel: it returns no signals, so the scan handler
// reads the knowledge.lineage rows itself via the tenant-pinned scope.
type coreLineage struct{}

func (coreLineage) EgressSignals(_ context.Context, _ model.TenantID) ([]EgressSignal, error) {
	return nil, nil
}

// isDefaultLineage reports whether the wired lineage source is the built-in default
// (so the residency scan reads the knowledge.lineage ext entity inline).
func (m *Module) isDefaultLineage() bool {
	_, ok := m.lineage.(coreLineage)
	return ok
}

// ---- ApprovalGate: the dual-control seam for the dangerous verbs --------

// GateRequest asks the governance gate to authorize one of the module's two
// DANGEROUS actions — "compliance.retention.enable" (turning a purge schedule on)
// and "compliance.hold.release" (lifting a preservation order). PlanHash binds the
// approval to the exact plan the humans see (anti-TOCTOU, §4/§6); the adapter the
// composition root wires runs over gateOnceNoBreakGlass for BOTH actions — neither
// has an emergency path.
type GateRequest struct {
	Action      string
	SubjectKind string
	SubjectRef  string
	PlanHash    string
	Reason      string
	RequestedBy string
}

// GateDecision is the gate's effective verdict, carrying the dual-control EVIDENCE from
// append-only decision trail in the two forms a two-person control needs: the
// module re-verifies the quorum independently — a gate that reports approved without
// quorum evidence is DENIED (defense in depth, the erase-gate pattern).
type GateDecision struct {
	Status      string // GateStatus* vocabulary
	ApprovalRef string
	PlanHash    string
	// Approvers are the CREDENTIALS that approved — the provenance every receipt and
	// ledger entry must keep. They are NOT the quorum: an audit-actor string identifies
	// a credential, not a human ("user:<id>" for a session, "token:<id>" for a token),
	// so one human holding both contributes two of them.
	Approvers []string
	// ApproverPersons are the DISTINCT PEOPLE who approved — the ONLY list a quorum may
	// be counted from (Quorum). A credential with no person behind it is absent by
	// construction: it cannot be one of the two humans.
	ApproverPersons []string
	// UnattributedApprovals counts approve decisions the gate could not attribute to a
	// person. They are excluded from ApproverPersons, and carried so an operator can
	// tell "one human short" apart from "an approval I cannot attribute to a human".
	UnattributedApprovals int
}

// Quorum is the number of distinct HUMANS who approved — the only sound answer to "how
// many people signed off", and the value every quorum comparison in this module reads.
// It deliberately cannot see Approvers: counting credentials is exactly the defect this
// type was reshaped to make unwritable.
func (d GateDecision) Quorum() int { return distinctNonEmpty(d.ApproverPersons) }

// The gate status vocabulary (compliance's own words — it never imports
// governance; the composition-root adapter maps onto these).
const (
	GateStatusApproved = "gate_approved"
	GateStatusPending  = "gate_pending"
	GateStatusRejected = "gate_rejected"
	GateStatusExpired  = "gate_expired"
	GateStatusNoGate   = "gate_no_gate"
)

// ApprovalGate authorizes a governed action through the engine. The
// caller treats an error as DENY (fail closed).
type ApprovalGate interface {
	Authorize(ctx context.Context, tenant model.TenantID, req GateRequest) (GateDecision, error)
}

// denyApprovalGate is the deny-closed default: until the composition root wires the
// Bridge, no purge schedule can be enabled and no hold can be released — an
// unconfigured deployment over-preserves, never silently destroys.
type denyApprovalGate struct{}

func (denyApprovalGate) Authorize(_ context.Context, _ model.TenantID, req GateRequest) (GateDecision, error) {
	return GateDecision{Status: GateStatusNoGate, PlanHash: req.PlanHash}, nil
}

// ---- AccountEraser: the seam into the engine's auth partition ---------------

// AccountEraseOutcome is the honest result of the account leg of an erasure. An
// un-wired seam reports Attempted=false — the receipt records the gap, never a
// fabricated "account erased".
type AccountEraseOutcome struct {
	// Attempted reports a real adapter ran (false ⇒ the seam is not wired).
	Attempted bool `json:"attempted"`
	// Erased counts the user accounts anonymized.
	Erased int `json:"erased"`
	// Detail is short, non-sensitive prose (counts and reasons, never identifiers).
	Detail string `json:"detail,omitempty"`
}

// AccountEraser anonymizes engine user accounts (email/display-name/credential —
// the auth-partition PII carve-out of docs/SECURITY-HARDENING.md) for a data subject's identifiers.
// User rows live in the SYSTEM tenant behind store.AuthScope, unreachable from any
// module scope — only the composition root can adapt this (cmd, over
// Store.AuthMutate). The adapter MUST refuse a user holding memberships in OTHER
// tenants (one tenant's DSR cannot erase a principal shared with another) and MUST
// preserve the user id so ledger actors ("user:<id>") stay resolvable as
// tombstones, not dangling references — anonymize, never hard-delete. requestedBy
// and requestedByKind are the requesting principal and its actor kind, for the
// auth-partition self-audit (a token-authenticated principal must not be recorded
// as a human).
type AccountEraser interface {
	EraseAccount(ctx context.Context, tenant model.TenantID, refs []string, requestedBy, requestedByKind string) (AccountEraseOutcome, error)
}

// notWiredAccountEraser is the honest default: no account is touched and the
// receipt says so.
type notWiredAccountEraser struct{}

func (notWiredAccountEraser) EraseAccount(context.Context, model.TenantID, []string, string, string) (AccountEraseOutcome, error) {
	return AccountEraseOutcome{Attempted: false, Detail: "account eraser not wired; engine user accounts untouched"}, nil
}

// ---- ProviderEraser: the Anthropic Compliance DELETE passthrough ------------

// ProviderEraseRequest asks the provider-side eraser to hard-delete the data
// subject's content at the model provider (CLA-06). SubjectUserIDs are the
// PROVIDER'S user ids (claude.ai user uuids), supplied by the DSR operator — the
// control plane has no mapping of its own. CaseRef is the DSR case id; RequestedBy
// the requesting principal. No content, no credential.
type ProviderEraseRequest struct {
	SubjectUserIDs []string
	CaseRef        string
	RequestedBy    string
}

// ProviderEraseOutcome is the honest provider-leg result. The eraser runs its
// OWN dual-control PEP per deletion (allowlist → PlanHash → CRITICAL gate → quorum
// re-check), so a first execute typically reports Pending deletions whose approvals
// are still gathering — a later execute consumes the approved grants. Wired=false ⇒
// the passthrough is not configured; the receipt records the gap.
type ProviderEraseOutcome struct {
	Wired      bool   `json:"wired"`
	Enumerated int    `json:"enumerated"`
	Erased     int    `json:"erased"`
	Pending    int    `json:"pending"`
	Failed     int    `json:"failed"`
	Detail     string `json:"detail,omitempty"`
}

// ProviderEraser orchestrates the Anthropic-side RTBF DELETE (connectors/claude-compliance.ComplianceEraser). The adapter lives in the
// composition root — the connector's gate needs the bridge and its credential
// needs operator provisioning; compliance only ever sees this seam.
type ProviderEraser interface {
	EraseProviderContent(ctx context.Context, tenant model.TenantID, req ProviderEraseRequest) (ProviderEraseOutcome, error)
}

// notWiredProviderEraser is the honest default: nothing is deleted provider-side
// and the receipt says so.
type notWiredProviderEraser struct{}

func (notWiredProviderEraser) EraseProviderContent(context.Context, model.TenantID, ProviderEraseRequest) (ProviderEraseOutcome, error) {
	return ProviderEraseOutcome{Wired: false, Detail: "provider eraser not wired; provider-side content untouched"}, nil
}

// ---- FileStoreEraser: the seam into Anthropic's persistent Files store ------

// FileRef is the MINIMAL-DATA view of a Files-store object (a reference, never content or
// filename). Anthropic's Files store carries NO data-subject metadata — only id / mime /
// size / created_at / scope — which is exactly why per-subject RTBF cannot SELECT a subject's
// files: the erasure leg DISCLOSES the store honestly rather than fabricate a subject match.
type FileRef struct {
	ID        string `json:"id"`
	MimeType  string `json:"mime_type,omitempty"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	ScopeID   string `json:"scope_id,omitempty"` // session scope, when the object is session-scoped
}

// FileStoreEraser is the seam into the workspace-scoped, shared-across-keys, PERSISTENT,
// NOT-ZDR Files store. The composition root adapts it over the claude-api connector (the
// connector needs operator provisioning; compliance only ever sees this seam, never imports
// it). An un-wired seam reports Wired()=false — the inventory + receipt record the gap, never
// a fabricated "file erased".
type FileStoreEraser interface {
	// Wired reports a real adapter is configured (false ⇒ the governed Files plane is inert).
	Wired() bool
	// ListFiles enumerates the store (minimal-data refs, paginated by the adapter). scopeID
	// "" lists the whole store.
	ListFiles(ctx context.Context, tenant model.TenantID, scopeID string) ([]FileRef, error)
	// DeleteFile deletes one file by id and returns the provider's confirmation id. The
	// GOVERNED decision (hold re-check, dual-control, receipt) is the module's, around this.
	DeleteFile(ctx context.Context, tenant model.TenantID, fileID string) (confirmationID string, err error)
}

// notWiredFileStoreEraser is the honest default: the governed Files plane is inert until the
// composition root wires the connector adapter.
type notWiredFileStoreEraser struct{}

func (notWiredFileStoreEraser) Wired() bool { return false }
func (notWiredFileStoreEraser) ListFiles(context.Context, model.TenantID, string) ([]FileRef, error) {
	return nil, nil
}
func (notWiredFileStoreEraser) DeleteFile(context.Context, model.TenantID, string) (string, error) {
	return "", errFileStoreNotWired
}

// ---- CryptoShredCoordinator: enterprise RTBF depth seam -------------------------

// CryptoShredBlocker is one enterprise-depth condition that blocks a subject
// crypto-shred. It must be minimal-data: structural ids only, never the subject's
// raw identifier.
type CryptoShredBlocker struct {
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
}

// CryptoShredReadiness is the pre-shred verdict returned by the optional
// enterprise RTBF coordinator. Ready=false blocks the local open-core shred.
type CryptoShredReadiness struct {
	Ready         bool                 `json:"ready"`
	Blockers      []CryptoShredBlocker `json:"blockers,omitempty"`
	Warnings      []string             `json:"warnings,omitempty"`
	PolicyApplied string               `json:"policy_applied,omitempty"`
}

// CryptoShredResidualScan is the enterprise-depth residual scan summary.
type CryptoShredResidualScan struct {
	ScanDepth      string   `json:"scan_depth,omitempty"`
	TargetsScanned int      `json:"targets_scanned,omitempty"`
	ResiduesFound  int      `json:"residues_found,omitempty"`
	Residues       []string `json:"residues,omitempty"`
	Clean          bool     `json:"clean"`
}

// CryptoShredVerification is the post-shred verdict returned by the optional
// enterprise RTBF coordinator. Complete=false keeps the receipt honest as a gap.
type CryptoShredVerification struct {
	Complete     bool                    `json:"complete"`
	KeyDestroyed bool                    `json:"key_destroyed"`
	WORMNotified bool                    `json:"worm_notified"`
	ResidualScan CryptoShredResidualScan `json:"residual_scan"`
	// Unverified lists, explicitly, every claim the coordinator could NOT verify
	// (a missing probe, a failed re-check, an unrecorded WORM ack) and why. A
	// non-empty list forces Complete=false — an unverifiable shred is reported as
	// a gap, never rounded up to "complete" (deny-closed contract).
	Unverified    []string `json:"unverified,omitempty"`
	PolicyApplied string   `json:"policy_applied,omitempty"`
}

// CryptoShredProbes carries the EVIDENCE probes the module hands the coordinator
// on each verification call. Key destruction and residues can only be
// verified inside the SAME transaction that shreds the key — the receipt (which
// embeds the verdict) commits atomically with the shred, so a post-commit re-read
// would race it and an out-of-transaction read would see the pre-shred snapshot.
// The module therefore binds these closures over its live store scope and the
// coordinator EXECUTES them; it never fabricates the answers they give.
//
// A nil probe means that claim is unverifiable in this call — the coordinator
// must report it in CryptoShredVerification.Unverified and keep Complete=false.
type CryptoShredProbes struct {
	// KeyGone re-probes the subject key row: true = the row no longer loads (the
	// DEK is destroyed and every token sealed under it is unintelligible).
	KeyGone func(ctx context.Context) (bool, error)
	// ResidualScan re-runs the module's registry scan for the subject's
	// identifiers and reports every surviving occurrence (structural labels,
	// never PII) plus the number of targets actually examined.
	ResidualScan func(ctx context.Context) (residues []string, targetsScanned int, err error)
}

// CryptoShredCoordinator is the typed form of the enterprise RTBF-depth seam. The
// public module also accepts a reflect-adapted coordinator so an overlay build can
// consume one without importing it (rtbf_depth.go).
//
// CONTRACT (v2): ValidateShredReadiness takes the tenant so the coordinator
// can consult the live legal-hold plane through its injected ports;
// VerifyShredCompleteness takes CryptoShredProbes so every field of the verdict
// comes from an executed check. Implementations MUST be deny-closed: whatever
// cannot be verified is reported in Unverified with Complete=false, never assumed.
type CryptoShredCoordinator interface {
	ValidateShredReadiness(ctx context.Context, tenant, subjectKind, subjectRef string) (CryptoShredReadiness, error)
	NotifyWORMSinks(ctx context.Context, keyID string, shredAt time.Time) error
	VerifyShredCompleteness(ctx context.Context, keyID string, targets []string, probes CryptoShredProbes) (CryptoShredVerification, error)
}

// ---- ProviderRetention: the Covered-Models forced-retention floor (§7) ----------

// ProviderRetention reports the maximum retention period the model PROVIDER forces
// on inference I/O (Covered Models: ≥30 days, no ZDR — uplift 2026-06-09), and a
// short source label for provenance. The composition root adapts it over
// models.MaxCoveredRetentionDays. Semantics are ANNOTATE, NOT REJECT: deleting our
// copy before the floor is legitimate; what a tenant cannot do is PROMISE total
// deletion before it — the classes/policies DTOs carry the disclosure.
type ProviderRetention interface {
	MaxForcedRetentionDays(ctx context.Context) (days int, source string)
}
