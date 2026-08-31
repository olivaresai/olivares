// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// the automatic incident evidence pack: one privileged, self-audited
// read that reconstructs a kill-switch incident for a regulator or an IR team
// without manual archeology. It composes ONLY tamper-evident or append-only
// sources:
//
//   - the stop row itself (who/when/scope/reason, the full lifecycle);
//   - the hash-chained ledger from the engage anchor (engage_audit_seq) to the
//     chain head — the incident TIMELINE, filtered to incident-relevant
//     actions, each entry carrying its stored canonical meta verbatim;
//   - a LIVE chain-verification of exactly that range (Verify(fromSeq)), so the
//     pack states whether the evidence it carries sits on an intact chain;
//   - the dual-control re-enable approval and its immutable decision trail
//     (the "two distinct humans" proof), plus the approvals the engage revoked;
//   - the guardian actions bound to this stop (when a rule engaged it);
//   - the findings that occurred in the incident window (bounded, minimal
//     fields);
//   - the ROLLBACK PLAN: every deploy-domain ledger action in the window with
//     the pointer to the existing revision-rollback path (POST /v1/m/deploy/
//     definitions/{id}/rollback + the governed re-apply), and the explicit,
//     honest list of domains where rollback does NOT apply.
//
// Minimal data (docs/SECURITY-HARDENING.md): the pack carries identifiers, action verbs,
// canonical (already-redacted) ledger meta and bounded operator prose — never
// payloads, secrets or tool inputs. Exporting it is itself a privileged act:
// the export self-audits BEFORE the response is written (forensic-case
// pattern), so the evidence of reading the evidence is on the same chain.

// ksEvidenceMaxEvents caps the timeline (honest truncation is flagged; the
// full-fidelity export path for an unbounded range is the WORM archive).
const ksEvidenceMaxEvents = 5000

// ksEvidenceMaxFindings caps the incident-window finding list.
const ksEvidenceMaxFindings = 1000

// ksEvidenceActionPrefixes is the incident-relevance filter for the timeline:
// the kill-switch lifecycle, the approval/break-glass/guardian governance
// trail, the stop-mode denial evidence the cmd gates append
// ("security.killswitch.deny", throttled), and the actuation domains whose
// activity during a stop is exactly what a reviewer must see.
var ksEvidenceActionPrefixes = []string{
	"governance.killswitch.",
	"governance.approval.",
	"governance.breakglass.",
	"governance.guardian.",
	"security.killswitch.",
	"deploy.",
	"orchestration.",
	"voice.",
	"models.",
	"mcp.",
}

// ksNonReversibleDomains documents — explicitly, in the pack itself — where
// rollback does NOT apply and what the compensating control is. Chan et al.
// specify agent-action rollback academically; this is the honest commercial
// boundary of it on this plane.
var ksNonReversibleDomains = []ksNonReversible{
	{Domain: "orchestration.schedule.fire", Why: "a dispatched agent run executed; compute and side effects outside the plane cannot be unwound", Control: "preventive: two-phase HITL + budget gate + kill-switch deny at fire time"},
	{Domain: "voice.session.open", Why: "realtime audio happened; spend and the conversation are facts", Control: "preventive: two-phase HITL + budget gate; stop blocks new session mints (provider Terminate is a no-op industry-wide today)"},
	{Domain: "models routed execution", Why: "provider inference spend is consumed on use", Control: "preventive: Governance gate + budget gate + kill-switch deny before routing"},
	{Domain: "notify deliveries", Why: "a sent notification cannot be unsent", Control: "deliberately EXEMPT from the stop: incident alerting must flow"},
	{Domain: "nhi.rotate / nhi.offboard.finalize", Why: "a minted credential exists; a finalized offboarding is definitive (forbids break-glass on finalize)", Control: "preventive: CRITICAL dual-control; quarantine (Disable) is the reversible containment instead"},
	{Domain: "compliance erasure (RTBF crypto-shred)", Why: "irreversible by design and by law", Control: "preventive: CRITICAL dual-control, no break-glass"},
	{Domain: "deploy.retire", Why: "destroyed resources may carry data; re-applying a prior revision recreates topology, not lost state", Control: "preventive: governed two-phase retire + blast-radius gate; revision history allows forward re-declare"},
}

type ksNonReversible struct {
	Domain  string `json:"domain"`
	Why     string `json:"why"`
	Control string `json:"compensating_control"`
}

// ksTimelineEntry is one ledger event in the incident window. Meta is the
// STORED canonical meta string verbatim (already redacted at write time) — the
// exact bytes the chain hash commits to, so the pack loses no fidelity.
type ksTimelineEntry struct {
	Seq        int64           `json:"seq"`
	OccurredAt string          `json:"occurred_at"`
	Actor      string          `json:"actor"`
	ActorKind  string          `json:"actor_kind,omitempty"`
	Action     string          `json:"action"`
	TargetKind string          `json:"target_kind,omitempty"`
	TargetID   string          `json:"target_id,omitempty"`
	Meta       json.RawMessage `json:"meta,omitempty"`
}

// ksEvidenceIntegrity is the pack's tamper-evidence statement.
type ksEvidenceIntegrity struct {
	AnchorSeq         int64  `json:"anchor_seq"`
	HeadSeq           int64  `json:"head_seq"`
	HeadHash          string `json:"head_hash"`
	ChainVerified     bool   `json:"chain_verified"`
	ChainChecked      int64  `json:"chain_checked"`
	ChainBreakAt      int64  `json:"chain_break_at,omitempty"`
	ChainBreakReason  string `json:"chain_break_reason,omitempty"`
	CanonicalMeta     bool   `json:"canonical_meta"`
	TimelineTruncated bool   `json:"timeline_truncated"`
	FindingsTruncated bool   `json:"findings_truncated"`
}

// ksEvidenceFinding is one bounded finding view (minimal fields).
type ksEvidenceFinding struct {
	Kind        string `json:"kind"`
	Severity    string `json:"severity"`
	Status      string `json:"status"`
	Title       string `json:"title"`
	SubjectKind string `json:"subject_kind,omitempty"`
	SubjectRef  string `json:"subject_ref,omitempty"`
	OccurredAt  string `json:"occurred_at"`
}

// ksRollbackPlan lists what IS rollback-eligible (deploy desired-state) and
// what is not (everything else, documented).
type ksRollbackPlan struct {
	DeployOps     []ksTimelineEntry `json:"deploy_operations_in_window"`
	HowTo         string            `json:"how_to"`
	NonReversible []ksNonReversible `json:"non_reversible_domains"`
}

// ksEvidencePack is the export envelope.
type ksEvidencePack struct {
	GeneratedAt       string              `json:"generated_at"`
	Tenant            string              `json:"tenant"`
	KillSwitch        killSwitchDTO       `json:"killswitch"`
	ReenableApproval  *approvalDTO        `json:"reenable_approval,omitempty"`
	ReenableDecisions []decisionDTO       `json:"reenable_decisions,omitempty"`
	RevokedApprovals  []string            `json:"revoked_approval_ids,omitempty"`
	GuardianActions   []guardianActionDTO `json:"guardian_actions,omitempty"`
	Timeline          []ksTimelineEntry   `json:"timeline"`
	Findings          []ksEvidenceFinding `json:"findings"`
	Rollback          ksRollbackPlan      `json:"rollback"`
	Integrity         ksEvidenceIntegrity `json:"integrity"`
	PackSHA256        string              `json:"pack_sha256"`
}

// ksEvidenceRelevant applies the timeline relevance filter: an action-prefix
// match, or any event targeting the stop row or its bound approval directly.
func ksEvidenceRelevant(ev model.AuditEvent, stopID, approvalID string) bool {
	for _, p := range ksEvidenceActionPrefixes {
		if strings.HasPrefix(ev.Action, p) {
			return true
		}
	}
	if t := ev.TargetID.String(); t != "" && (t == stopID || (approvalID != "" && t == approvalID)) {
		return true
	}
	return false
}

// handleKillSwitchEvidence builds and returns the incident evidence pack.
// Admin-tier; the export is self-audited BEFORE the response is written. An
// ACTIVE incident can be exported mid-flight (an IR team does not wait for the
// post-review to start reading).
func (m *Module) handleKillSwitchEvidence(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	now := m.clock.Now()
	pack := ksEvidencePack{
		GeneratedAt: now.String(),
		Tenant:      mc.Tenant.String(),
		Timeline:    []ksTimelineEntry{},
		Findings:    []ksEvidenceFinding{},
		Rollback: ksRollbackPlan{
			DeployOps:     []ksTimelineEntry{},
			HowTo:         "deploy actions are rollback-eligible: POST /v1/m/deploy/definitions/{id}/rollback declares a new revision equal to a prior known-good version (append-only history); the subsequent governed apply (two-phase HITL + blast-radius gate, idempotent reconcile) restores real state. Everything else: see non_reversible_domains.",
			NonReversible: ksNonReversibleDomains,
		},
	}
	found := false
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(killSwitchKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		found = true
		pack.KillSwitch = toKillSwitchDTO(rec)

		// The dual-control proof: the bound approval + its immutable decisions.
		approvalID := rec.String(colKSReenableAppr)
		if approvalID != "" {
			apprRepo, aerr := sc.Ext(approvalKind)
			if aerr != nil {
				return aerr
			}
			if appr, gerr := apprRepo.Get(r.Context(), model.ID(approvalID)); gerr == nil {
				pols, perr := loadApprovalPolicies(r.Context(), sc)
				if perr != nil {
					return perr
				}
				dto := toApprovalDTO(appr, now, liveRiskTier(pols, appr))
				pack.ReenableApproval = &dto
				decRepo, derr := sc.Ext(decisionKind)
				if derr != nil {
					return derr
				}
				decs, derr := listAll(r.Context(), decRepo, eq(colApprovalID, approvalID))
				if derr != nil {
					return derr
				}
				for _, d := range decs {
					pack.ReenableDecisions = append(pack.ReenableDecisions, toDecisionDTO(d))
				}
			} else if !isNotFound(gerr) {
				return gerr
			}
		}

		// Guardian actions bound to this stop (a rule engaged it).
		actRepo, gerr := sc.Ext(guardianActionKind)
		if gerr != nil {
			return gerr
		}
		acts, gerr := listAll(r.Context(), actRepo, eq(colGAKillswitchID, id.String()))
		if gerr != nil {
			return gerr
		}
		for _, a := range acts {
			pack.GuardianActions = append(pack.GuardianActions, toGuardianActionDTO(a))
		}

		// The ledger timeline from the engage anchor, with live chain verify of
		// exactly that range.
		anchor := rec.Int(colKSEngageSeq)
		if anchor < 1 {
			anchor = 1
		}
		pack.Integrity.AnchorSeq = anchor
		head, hasHead, herr := sc.Audit().Head(r.Context())
		if herr != nil {
			return herr
		}
		if hasHead {
			pack.Integrity.HeadSeq = head.Seq
			pack.Integrity.HeadHash = hex.EncodeToString(head.Hash)
			report, verr := sc.Audit().Verify(r.Context(), anchor)
			if verr != nil {
				return verr
			}
			pack.Integrity.ChainVerified = report.OK && report.Checked > 0
			pack.Integrity.ChainChecked = report.Checked
			pack.Integrity.ChainBreakAt = report.BreakAt
			pack.Integrity.ChainBreakReason = report.Reason
			if report.Checked == 0 && pack.Integrity.ChainBreakReason == "" {
				pack.Integrity.ChainBreakReason = "no-events"
			}

			collect := func(ev model.AuditEvent, metaCanonical string) {
				if !ksEvidenceRelevant(ev, pack.KillSwitch.ID, approvalID) {
					return
				}
				if len(pack.Timeline) >= ksEvidenceMaxEvents {
					pack.Integrity.TimelineTruncated = true
					return
				}
				entry := ksTimelineEntry{
					Seq: ev.Seq, OccurredAt: ev.OccurredAt.String(),
					Actor: ev.Actor, ActorKind: ev.ActorKind, Action: ev.Action,
					TargetKind: string(ev.TargetKind), TargetID: ev.TargetID.String(),
				}
				if metaCanonical != "" && metaCanonical != "{}" && json.Valid([]byte(metaCanonical)) {
					entry.Meta = json.RawMessage(metaCanonical)
				}
				pack.Timeline = append(pack.Timeline, entry)
				if strings.HasPrefix(ev.Action, "deploy.") {
					pack.Rollback.DeployOps = append(pack.Rollback.DeployOps, entry)
				}
				// The engage event's meta carries the revoked-approval ids.
				if ev.Action == "governance.killswitch.engage" && ev.TargetID.String() == pack.KillSwitch.ID && entry.Meta != nil {
					var meta struct {
						Revoked []string `json:"revoked_approval_ids"`
					}
					if jerr := json.Unmarshal(entry.Meta, &meta); jerr == nil && len(meta.Revoked) > 0 {
						pack.RevokedApprovals = meta.Revoked
					}
				}
			}
			if cw, isCW := sc.Audit().(store.CanonicalWalker); isCW {
				pack.Integrity.CanonicalMeta = true
				if werr := cw.WalkCanonical(r.Context(), anchor, func(ev model.AuditEvent, metaCanonical string, _ []byte) error {
					collect(ev, metaCanonical)
					return nil
				}); werr != nil {
					return werr
				}
			} else {
				// Honest degradation (the observability read-model pattern): the
				// timeline still lists the events, but meta fidelity is absent and
				// the pack says so.
				if werr := sc.Audit().Walk(r.Context(), anchor, func(ev model.AuditEvent) error {
					collect(ev, "")
					return nil
				}); werr != nil {
					return werr
				}
			}
		}

		// Findings in the incident window (bounded, minimal fields).
		fq := model.Query{Filters: []model.Filter{
			{Column: "occurred_at", Op: model.OpGte, Value: rec.String(colKSEngagedAt)},
		}, Limit: listCap}
		for {
			finds, page, ferr := sc.Findings().List(r.Context(), fq)
			if ferr != nil {
				return ferr
			}
			for _, f := range finds {
				if len(pack.Findings) >= ksEvidenceMaxFindings {
					pack.Integrity.FindingsTruncated = true
					break
				}
				ref := f.Metadata["subject_ref"]
				refStr, _ := ref.(string)
				if refStr == "" && !f.SubjectID.IsZero() {
					refStr = f.SubjectID.String()
				}
				pack.Findings = append(pack.Findings, ksEvidenceFinding{
					Kind: f.Kind, Severity: string(f.Severity), Status: string(f.Status),
					Title: f.Title, SubjectKind: f.SubjectKind, SubjectRef: refStr,
					OccurredAt: f.OccurredAt.String(),
				})
			}
			if pack.Integrity.FindingsTruncated || !page.HasMore || page.Cursor == "" {
				break
			}
			fq.Cursor = page.Cursor
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}

	// Seal the pack: a deterministic hash over its content (computed with the
	// hash field empty) so a recipient can prove a copy unaltered.
	pack.PackSHA256 = ""
	canonical, err := json.Marshal(pack)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody("internal error"))
		return
	}
	sum := sha256.Sum256(canonical)
	pack.PackSHA256 = hex.EncodeToString(sum[:])

	// Self-audit the export BEFORE returning it (privileged read, docs/SECURITY-HARDENING.md):
	// the evidence of reading the evidence lands on the same chain. A failed
	// audit fails the export — deny-closed.
	err = mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		return auditEvent(r.Context(), sc, mc, "governance.killswitch.evidence_export", killSwitchKind, id, map[string]any{
			"timeline_events": len(pack.Timeline), "findings": len(pack.Findings),
			"chain_verified": pack.Integrity.ChainVerified, "pack_sha256": pack.PackSHA256,
		})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pack)
}
