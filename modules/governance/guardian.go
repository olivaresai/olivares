// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// guardian-agent loops: operator-authored, semi-autonomous containment
// over the product-wide finding rail (the Gartner "guardian agent" pattern,
// 10-15% of the agentic market by 2030 per the currency audit).
//
// A rule matches high-risk findings (by kind allowlist + severity floor) and
// fires ONE containment action: stop_agent (an agent-scoped kill switch),
// quarantine_nhi (block the subject's NHI — the reversible Disable-class
// containment, undone by nhi restore), or stop_estate (the full stop).
// HITL is configurable PER RULE: mode=auto acts immediately and notifies; mode=
// approval queues the action behind a single-human approval (HIGH tier —
// confirming containment is protective, so one human suffices and no AAL3 bar
// delays it) that the guardian sweep executes once approved.
//
// Deny-closed discipline:
//   - only an ENABLED rule ever acts; an unmatched/disabled rule does nothing;
//   - the loop HARD-SKIPS the "killswitch_"/"guardian_" finding kinds it and
//     the kill switch emit, so containment can never re-trigger itself into an
//     escalation spiral (OWASP Agentic T10: a guardian that floods its own HITL
//     queue is an attack on the humans);
//   - one action per (rule, finding identity), enforced by a unique index — a
//     re-reported finding cannot double-fire;
//   - containment executions are idempotent (an already-stopped agent or
//     already-blocked NHI records "already contained", it does not error).
//
// The handler runs on the bus (async, no retry — sdk/event contract): it is
// fast, never blocks, and every state change is guarded so a missed or
// re-delivered event converges.

// Guardian rule vocabulary.
const (
	gaActionStopAgent     = "stop_agent"
	gaActionQuarantineNHI = "quarantine_nhi"
	gaActionStopEstate    = "stop_estate"

	gaModeAuto     = "auto"
	gaModeApproval = "approval"

	gaStatusPending  = "pending"
	gaStatusExecuted = "executed"
	gaStatusRejected = "rejected"
	gaStatusExpired  = "expired"
	gaStatusFailed   = "failed"
)

// guardianApprovalPrefix + action names the HITL confirmation of a queued
// containment. Deliberately OUTSIDE the "security.killswitch." CRITICAL family:
// confirming a protective block is a HIGH action (one human, no AAL3) — the
// risk asymmetry of a containment confirmation is the opposite of a re-enable.
const guardianApprovalPrefix = "security.guardian."

// guardianApprovalExpiry bounds how long a queued containment waits for its
// human: a confirmation older than an hour is stale intelligence (the incident
// moved on); the sweep marks it expired and the trail says so honestly.
const guardianApprovalExpiry = int64(3600)

// guardianApprovalEscalate surfaces an undecided containment to the finding
// rail (approval-escalation sweep) after 15 minutes.
const guardianApprovalEscalate = int64(900)

// Finding kinds the guardian loop emits (hard-skipped by its own matcher).
const (
	findingGuardianExecuted = "guardian_action_executed"
	findingGuardianPending  = "guardian_action_pending"
	findingGuardianFailed   = "guardian_action_failed"
)

// guardianSelfPrefixes are the finding-kind prefixes the loop never matches.
// The invariant is broader than "its own emissions": the guardian reacts to
// THREAT findings from external detectors (security/redteam/forensic guardrails,
// the ssf connector's caep_* signals — none of which carry these prefixes),
// NEVER to the control plane's own OPERATIONAL/lifecycle findings. The governance
// module emits four such families — killswitch_* and guardian_* (the loop's own
// output, the direct escalation spiral) AND governance_* (approval/break-glass
// lifecycle) and nhi_* (NHI lifecycle). Excluding all four closes the
// self-amplification the prefix-only check missed: the guardian's own
// approval-mode containment escalates after 15m → a governance_approval_escalated
// (HIGH) finding → an any-match stop_estate rule would fire an estate stop on its
// own queued action (and on every unrelated benign approval escalation). Rules
// cannot opt out: create/update reject these prefixes in match_kinds too. If a
// governance_*/nhi_* kind is ever genuinely a containment trigger, it must be a
// deliberate, dedicated re-design — never reachable through the any-match default.
var guardianSelfPrefixes = []string{"killswitch_", "guardian_", "governance_", "nhi_"}

// sevRank orders the shared severity scale for the rule floor comparison.
// Unknown severities rank lowest (deny-closed for matching: an unknown
// severity never satisfies a floor).
func sevRank(s string) int {
	switch sdkmodel.Severity(strings.ToLower(strings.TrimSpace(s))) {
	case sdkmodel.SeverityInfo:
		return 1
	case sdkmodel.SeverityLow:
		return 2
	case sdkmodel.SeverityMedium:
		return 3
	case sdkmodel.SeverityHigh:
		return 4
	case sdkmodel.SeverityCritical:
		return 5
	default:
		return 0
	}
}

// guardianRuleDTO is the rule view.
type guardianRuleDTO struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Enabled     bool   `json:"enabled"`
	MatchKinds  string `json:"match_kinds,omitempty"`
	MinSeverity string `json:"min_severity"`
	Action      string `json:"action"`
	Mode        string `json:"mode"`
	AgentTier   string `json:"agent_tier,omitempty"`
	CreatedBy   string `json:"created_by,omitempty"`
	Note        string `json:"note,omitempty"`
}

func toGuardianRuleDTO(rec model.Record) guardianRuleDTO {
	return guardianRuleDTO{
		ID: rec.String(model.ColID), Name: rec.String(colGRName), Enabled: rec.Bool(colGREnabled),
		MatchKinds: rec.String(colGRMatchKinds), MinSeverity: rec.String(colGRMinSeverity),
		Action: rec.String(colGRAction), Mode: rec.String(colGRMode),
		AgentTier: rec.String(colGRAgentTier),
		CreatedBy: rec.String(colGRCreatedBy), Note: rec.String(colGRNote),
	}
}

// guardianActionDTO is one trail entry: what a rule did (or queued) for one finding.
type guardianActionDTO struct {
	ID           string `json:"id"`
	RuleID       string `json:"rule_id"`
	RuleName     string `json:"rule_name"`
	FindingKind  string `json:"finding_kind"`
	FindingRef   string `json:"finding_ref"`
	Severity     string `json:"finding_severity"`
	TargetKind   string `json:"target_kind"`
	TargetRef    string `json:"target_ref,omitempty"`
	Action       string `json:"action"`
	Mode         string `json:"mode"`
	Status       string `json:"status"`
	ApprovalID   string `json:"approval_id,omitempty"`
	KillswitchID string `json:"killswitch_id,omitempty"`
	Detail       string `json:"detail,omitempty"`
	ExecutedAt   string `json:"executed_at,omitempty"`
}

func toGuardianActionDTO(rec model.Record) guardianActionDTO {
	return guardianActionDTO{
		ID: rec.String(model.ColID), RuleID: rec.String(colGARule), RuleName: rec.String(colGARuleName),
		FindingKind: rec.String(colGAFindingKind), FindingRef: rec.String(colGAFindingRef),
		Severity: rec.String(colGASeverity), TargetKind: rec.String(colGATargetKind),
		TargetRef: rec.String(colGATargetRef), Action: rec.String(colGAAction), Mode: rec.String(colGAMode),
		Status: rec.String(colGAStatus), ApprovalID: rec.String(colGAApprovalID),
		KillswitchID: rec.String(colGAKillswitchID), Detail: rec.String(colGADetail),
		ExecutedAt: rec.String(colGAExecutedAt),
	}
}

// --- rule authoring -----------------------------------------------------------

// guardianRuleRequest creates a rule; update takes the same shape with optional
// fields (nil = keep).
type guardianRuleRequest struct {
	Name        string `json:"name"`
	Enabled     *bool  `json:"enabled,omitempty"`
	MatchKinds  string `json:"match_kinds,omitempty"`
	MinSeverity string `json:"min_severity,omitempty"`
	Action      string `json:"action"`
	Mode        string `json:"mode"`
	AgentTier   string `json:"agent_tier,omitempty"`
	Note        string `json:"note,omitempty"`
}

// validateGuardianRuleFields validates the shared rule fields (bounded,
// credential-free, vocabulary-checked, self-loop-proof).
func validateGuardianRuleFields(matchKinds, minSeverity, action, mode, note string) string {
	if len(matchKinds) > maxNoteLen || len(note) > maxNoteLen {
		return "match_kinds or note too long"
	}
	if containsInlineCredential(matchKinds) {
		return "match_kinds must not contain a credential"
	}
	for _, k := range splitKinds(matchKinds) {
		for _, p := range guardianSelfPrefixes {
			if strings.HasPrefix(k, p) {
				return "match_kinds must not include the guardian's own '" + p + "*' findings (feedback loop)"
			}
		}
	}
	if sevRank(minSeverity) == 0 {
		return "min_severity must be one of info, low, medium, high, critical"
	}
	switch action {
	case gaActionStopAgent, gaActionQuarantineNHI, gaActionStopEstate:
	default:
		return "action must be one of stop_agent, quarantine_nhi, stop_estate"
	}
	switch mode {
	case gaModeAuto, gaModeApproval:
	default:
		return "mode must be one of auto, approval"
	}
	return ""
}

// splitKinds parses the comma-separated match_kinds list (lowercased, trimmed,
// empties dropped). An empty list means "any kind" (self-kinds still excluded).
func splitKinds(s string) []string {
	var out []string
	for _, k := range strings.Split(s, ",") {
		if k = strings.ToLower(strings.TrimSpace(k)); k != "" {
			out = append(out, k)
		}
	}
	return out
}

// handleCreateGuardianRule authors a containment rule. Admin-tier (authoring
// auto-containment is enforcement-posture authoring); self-audited.
func (m *Module) handleCreateGuardianRule(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in guardianRuleRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	in.MatchKinds = strings.TrimSpace(in.MatchKinds)
	in.MinSeverity = strings.ToLower(strings.TrimSpace(in.MinSeverity))
	in.Action = strings.ToLower(strings.TrimSpace(in.Action))
	in.Mode = strings.ToLower(strings.TrimSpace(in.Mode))
	if in.Name == "" || len(in.Name) > maxMatchLen {
		writeJSON(w, http.StatusBadRequest, errorBody("a rule name (short identifier) is required"))
		return
	}
	if in.MinSeverity == "" {
		in.MinSeverity = string(sdkmodel.SeverityHigh) // guardian rules default to HIGH-risk findings
	}
	if msg := validateGuardianRuleFields(in.MatchKinds, in.MinSeverity, in.Action, in.Mode, in.Note); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	agentTier := strings.ToLower(strings.TrimSpace(in.AgentTier))
	if agentTier != "" && !validRiskTier(agentTier) {
		writeJSON(w, http.StatusBadRequest, errorBody("agent_tier must be one of low, medium, high, critical (or empty for any)"))
		return
	}
	var out guardianRuleDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(guardianRuleKind)
		if err != nil {
			return err
		}
		rec := model.Record{
			colGRName: in.Name, colGREnabled: enabled,
			colGRMatchKinds: strings.Join(splitKinds(in.MatchKinds), ","), colGRMinSeverity: in.MinSeverity,
			colGRAction: in.Action, colGRMode: in.Mode,
			colGRCreatedBy: mc.Principal.Actor(), colGRNote: in.Note,
		}
		if agentTier != "" {
			rec[colGRAgentTier] = agentTier
		}
		created, err := repo.Create(r.Context(), rec)
		if err != nil {
			return err // (tenant_id, name) unique conflict -> 409
		}
		out = toGuardianRuleDTO(created)
		return auditEvent(r.Context(), sc, mc, "governance.guardian.rule.create", guardianRuleKind, model.ID(out.ID), map[string]any{
			"name": in.Name, "action": in.Action, "mode": in.Mode, "min_severity": in.MinSeverity, "enabled": enabled,
		})
	})
	if err != nil {
		if isConflict(err) {
			writeJSON(w, http.StatusConflict, errorBody("a guardian rule with this name already exists"))
			return
		}
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// handleUpdateGuardianRule updates a rule's mutable fields (enable/disable,
// matching, action, mode, note). Name is immutable (the operator-facing
// identity). Admin-tier; self-audited.
func (m *Module) handleUpdateGuardianRule(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var in struct {
		Enabled     *bool   `json:"enabled,omitempty"`
		MatchKinds  *string `json:"match_kinds,omitempty"`
		MinSeverity *string `json:"min_severity,omitempty"`
		Action      *string `json:"action,omitempty"`
		Mode        *string `json:"mode,omitempty"`
		AgentTier   *string `json:"agent_tier,omitempty"`
		Note        *string `json:"note,omitempty"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	var (
		out        guardianRuleDTO
		clientErr  string
		clientCode int
	)
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(guardianRuleKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		if in.Enabled != nil {
			rec[colGREnabled] = *in.Enabled
		}
		if in.MatchKinds != nil {
			rec[colGRMatchKinds] = strings.Join(splitKinds(*in.MatchKinds), ",")
		}
		if in.MinSeverity != nil {
			rec[colGRMinSeverity] = strings.ToLower(strings.TrimSpace(*in.MinSeverity))
		}
		if in.Action != nil {
			rec[colGRAction] = strings.ToLower(strings.TrimSpace(*in.Action))
		}
		if in.Mode != nil {
			rec[colGRMode] = strings.ToLower(strings.TrimSpace(*in.Mode))
		}
		if in.AgentTier != nil {
			at := strings.ToLower(strings.TrimSpace(*in.AgentTier))
			if at == "" {
				rec[colGRAgentTier] = nil
			} else {
				if !validRiskTier(at) {
					clientErr, clientCode = "agent_tier must be one of low, medium, high, critical (or empty)", http.StatusBadRequest
					return nil
				}
				rec[colGRAgentTier] = at
			}
		}
		if in.Note != nil {
			rec[colGRNote] = *in.Note
		}
		if msg := validateGuardianRuleFields(rec.String(colGRMatchKinds), rec.String(colGRMinSeverity),
			rec.String(colGRAction), rec.String(colGRMode), rec.String(colGRNote)); msg != "" {
			clientErr, clientCode = msg, http.StatusBadRequest
			return nil
		}
		rec, err = repo.Update(r.Context(), rec)
		if err != nil {
			return err
		}
		out = toGuardianRuleDTO(rec)
		return auditEvent(r.Context(), sc, mc, "governance.guardian.rule.update", guardianRuleKind, id, map[string]any{
			"action": out.Action, "mode": out.Mode, "min_severity": out.MinSeverity, "enabled": out.Enabled,
		})
	})
	if clientErr != "" {
		writeJSON(w, clientCode, errorBody(clientErr))
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDeleteGuardianRule deletes a rule only after terminalizing every queued
// action that could otherwise execute from its denormalized action row. Action
// trail and kill-switch rows remain as evidence. Recreating a rule with the same
// name receives a new id, so the (tenant, rule_id, finding_ref) dedup does not
// suppress findings that the deleted rule already handled.
// Bounded TOCTOU (accepted, documented): a finding matching this rule in the
// window before the delete commits can enqueue a NEW pending action after the
// snapshot below; it is not terminalized here, and an operator approval would
// still execute it from its denormalized fields. The approval lifecycle bounds
// it (expiry) and the delete audit names what WAS canceled.
func (m *Module) handleDeleteGuardianRule(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}

	now := m.clock.Now()
	var resolvedApprovals []approvalDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		resolvedApprovals = resolvedApprovals[:0]

		ruleRepo, err := sc.Ext(guardianRuleKind)
		if err != nil {
			return err
		}
		rule, err := ruleRepo.Get(r.Context(), id)
		if err != nil {
			return err
		}

		actionRepo, err := sc.Ext(guardianActionKind)
		if err != nil {
			return err
		}
		pending, err := listAll(r.Context(), actionRepo, eq(colGARule, id.String()), eq(colGAStatus, gaStatusPending))
		if err != nil {
			return err
		}

		var resolvedRecords []model.Record
		if len(pending) > 0 {
			approvalRepo, err := sc.Ext(approvalKind)
			if err != nil {
				return err
			}
			for _, action := range pending {
				approvalID := model.ID(action.String(colGAApprovalID))
				if !approvalID.IsZero() {
					approval, aerr := approvalRepo.Get(r.Context(), approvalID)
					if aerr != nil && !isNotFound(aerr) {
						return aerr
					}
					if aerr == nil {
						// Mirror the approval cancellation/expiry state machine: only
						// a still-pending row transitions. An approval already decided
						// by a human remains immutable evidence; terminalizing the
						// guardian action below is what prevents its execution.
						switch effectiveStatus(approval, now) {
						case statusPending:
							approval[colStatus] = statusCanceled
							approval[colDecidedAt] = now.String()
						case statusExpired:
							approval[colStatus] = statusExpired
							approval[colDecidedAt] = now.String()
						default:
							approval = nil
						}
						if approval != nil {
							updated, uerr := approvalRepo.Update(r.Context(), approval)
							if uerr != nil {
								return uerr
							}
							resolvedRecords = append(resolvedRecords, updated)
						}
					}
					// A vanished approval is already non-executable. The action is
					// still terminalized below, and its evidence row is retained.
				}

				action[colGAStatus] = gaStatusRejected
				action[colGADetail] = "rule deleted"
				if _, err := actionRepo.Update(r.Context(), action); err != nil {
					return err
				}
			}
		}

		if len(resolvedRecords) > 0 {
			policies, err := loadApprovalPolicies(r.Context(), sc)
			if err != nil {
				return err
			}
			for _, approval := range resolvedRecords {
				resolvedApprovals = append(resolvedApprovals, toApprovalDTO(approval, now, liveRiskTier(policies, approval)))
			}
		}

		if err := ruleRepo.Delete(r.Context(), id); err != nil {
			return err
		}
		// The ledger is the source of truth: name every terminalized action and
		// canceled approval so "who canceled approval X" never needs timestamp
		// correlation (adversarial review m1).
		canceledActions := make([]string, 0, len(pending))
		for _, action := range pending {
			canceledActions = append(canceledActions, action.String(model.ColID))
		}
		canceledApprovals := make([]string, 0, len(resolvedRecords))
		for _, approval := range resolvedRecords {
			canceledApprovals = append(canceledApprovals, approval.String(model.ColID))
		}
		meta := map[string]any{
			"name": rule.String(colGRName), "pending_cancelled": len(pending),
		}
		if len(canceledActions) > 0 {
			meta["cancelled_actions"] = canceledActions
		}
		if len(canceledApprovals) > 0 {
			meta["cancelled_approvals"] = canceledApprovals
		}
		return auditEvent(r.Context(), sc, mc, "governance.guardian.rule.delete", guardianRuleKind, id, meta)
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	for _, approval := range resolvedApprovals {
		m.emitApprovalResolved(r.Context(), mc.Tenant, approval)
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleListGuardianRules lists rules.
func (m *Module) handleListGuardianRules(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	out := listResponse[guardianRuleDTO]{Items: []guardianRuleDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(guardianRuleKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toGuardianRuleDTO(rec))
		}
		out.Cursor, out.HasMore = page.Cursor, page.HasMore
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleListGuardianActions lists the containment trail, optionally by status.
func (m *Module) handleListGuardianActions(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if v := r.URL.Query().Get("status"); v != "" {
		q.Filters = append(q.Filters, eq(colGAStatus, v))
	}
	out := listResponse[guardianActionDTO]{Items: []guardianActionDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(guardianActionKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toGuardianActionDTO(rec))
		}
		out.Cursor, out.HasMore = page.Cursor, page.HasMore
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// --- the loop -------------------------------------------------------------------

// onGuardianFinding is the finding.reported half of the guardian loop (the
// other half is the sweep that executes approved containments). It shares the
// Init subscription with onLifecycleSignal.
func (m *Module) onGuardianFinding(ctx context.Context, e event.Event) {
	if m.data == nil {
		return
	}
	f, ok := event.FindingOf(e)
	if !ok {
		return
	}
	kind := strings.ToLower(strings.TrimSpace(f.Kind))
	for _, p := range guardianSelfPrefixes {
		if strings.HasPrefix(kind, p) {
			return // never react to our own containment's findings
		}
	}
	tenant, ok := tenantOf(e.Tenant)
	if !ok {
		return
	}

	// built-in tier floor check — fires BEFORE any operator-authored
	// guardian rule, so a floor stop applies even with no matching rules.
	m.checkTierFloor(ctx, tenant, f)

	// Load enabled rules in a cheap read; matching is in-memory.
	var rules []model.Record
	if err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(guardianRuleKind)
		if err != nil {
			return err
		}
		var ierr error
		rules, ierr = listAll(ctx, repo, eq(colGREnabled, true))
		return ierr
	}); err != nil {
		m.debugf("governance: guardian rule load failed", "err", err)
		return
	}

	for _, rule := range rules {
		if !guardianRuleMatches(rule, kind, string(f.Severity)) {
			continue
		}
		if tierFilter := rule.String(colGRAgentTier); tierFilter != "" {
			if f.SubjectKind != "agent" || strings.TrimSpace(f.SubjectRef) == "" {
				continue // tier filter set but finding has no agent subject
			}
			agentTier, _ := m.AgentEffectiveTier(ctx, tenant, strings.TrimSpace(f.SubjectRef))
			if tierRank(agentTier) < tierRank(tierFilter) {
				continue // agent's tier is below the rule's filter
			}
		}
		m.fireGuardianRule(ctx, tenant, rule, f)
	}
}

// guardianRuleMatches applies a rule's kind allowlist + severity floor.
func guardianRuleMatches(rule model.Record, kind, severity string) bool {
	if sevRank(severity) < sevRank(rule.String(colGRMinSeverity)) {
		return false
	}
	kinds := splitKinds(rule.String(colGRMatchKinds))
	if len(kinds) == 0 {
		return true
	}
	for _, k := range kinds {
		if k == kind {
			return true
		}
	}
	return false
}

// guardianFindingRef is the dedup identity of a finding: its DetailHash when
// the producer set one, else a stable hash of its identifying fields.
func guardianFindingRef(f sdkmodel.FindingReport) string {
	if ref := strings.TrimSpace(f.DetailHash); ref != "" {
		return ref
	}
	sum := sha256.Sum256([]byte(f.Kind + "|" + f.SubjectKind + "|" + f.SubjectRef + "|" + f.Title))
	return hex.EncodeToString(sum[:])
}

// guardianTarget resolves what a rule's action would contain, from the
// finding's subject. ok=false means the rule does not apply to this finding's
// subject shape (e.g. stop_agent on a finding about a connector) — a non-event,
// not a failure.
func guardianTarget(action string, f sdkmodel.FindingReport) (targetKind, targetRef string, ok bool) {
	subjectKind := strings.ToLower(strings.TrimSpace(f.SubjectKind))
	subjectRef := strings.TrimSpace(f.SubjectRef)
	switch action {
	case gaActionStopEstate:
		return "estate", "", true
	case gaActionStopAgent:
		if subjectKind == "agent" && subjectRef != "" {
			return "agent", subjectRef, true
		}
		return "", "", false
	case gaActionQuarantineNHI:
		if (subjectKind == "identity" || subjectKind == "agent") && subjectRef != "" {
			return subjectKind, subjectRef, true
		}
		return "", "", false
	}
	return "", "", false
}

// pendingGuardianEmit is a post-commit emission collected inside the Mutate.
type pendingGuardianEmit struct {
	findingKind string
	subjectKind string
	subjectRef  string
	severity    sdkmodel.Severity
	title       string
	approval    *approvalDTO // emit approval.requested too (HITL mode)
}

// fireGuardianRule runs one matched rule against one finding: dedup, target
// resolution, then auto-execute or queue-for-approval — one Mutate, idempotent.
func (m *Module) fireGuardianRule(ctx context.Context, tenant model.TenantID, rule model.Record, f sdkmodel.FindingReport) {
	ruleID, ruleName := rule.String(model.ColID), rule.String(colGRName)
	action, mode := rule.String(colGRAction), rule.String(colGRMode)
	targetKind, targetRef, applies := guardianTarget(action, f)
	if !applies {
		m.debugf("governance: guardian rule subject shape mismatch; skipping",
			"rule", ruleName, "action", action, "finding_kind", f.Kind, "subject_kind", f.SubjectKind)
		return
	}
	findingRef := guardianFindingRef(f)
	now := m.clock.Now()

	var emits []pendingGuardianEmit
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		emits = emits[:0] // a conflict-retried closure must not double-collect
		actRepo, err := sc.Ext(guardianActionKind)
		if err != nil {
			return err
		}
		// Dedup: one action per (rule, finding identity). The unique index
		// backstops this read under concurrency.
		if _, dup, err := findOne(ctx, actRepo, eq(colGARule, ruleID), eq(colGAFindingRef, findingRef)); err != nil {
			return err
		} else if dup {
			return nil
		}
		actRec := model.Record{
			colGARule: ruleID, colGARuleName: ruleName,
			colGAFindingKind: f.Kind, colGAFindingRef: findingRef, colGASeverity: string(f.Severity),
			colGATargetKind: targetKind, colGATargetRef: targetRef,
			colGAAction: action, colGAMode: mode,
		}

		switch mode {
		case gaModeAuto:
			outcome, cerr := m.containLocked(ctx, sc, ruleID, ruleName, action, targetKind, targetRef, f, now)
			if cerr != nil {
				return cerr
			}
			actRec[colGAStatus] = outcome.status
			actRec[colGADetail] = outcome.detail
			actRec[colGAExecutedAt] = now.String()
			if outcome.killswitchID != "" {
				actRec[colGAKillswitchID] = outcome.killswitchID
			}
			emits = append(emits, outcome.emits...)
		case gaModeApproval:
			reason := "guardian rule '" + ruleName + "': " + f.Kind + " (" + string(f.Severity) + ") on " + targetKind
			if len(reason) > maxNoteLen {
				reason = reason[:maxNoteLen]
			}
			subjectRef := targetRef
			if subjectRef == "" {
				subjectRef = "estate"
			}
			dto, oerr := m.openApprovalRecord(ctx, sc, model.ActorSystem, model.ActorSystem, "", createApprovalRequest{
				SubjectKind: targetKind, SubjectRef: subjectRef,
				Action: guardianApprovalPrefix + action, Reason: reason,
				ExpiresInSeconds: guardianApprovalExpiry, EscalateInSeconds: guardianApprovalEscalate,
			}, 0, now)
			if oerr != nil {
				return oerr
			}
			actRec[colGAStatus] = gaStatusPending
			actRec[colGAApprovalID] = dto.ID
			emits = append(emits, pendingGuardianEmit{
				findingKind: findingGuardianPending, subjectKind: targetKind, subjectRef: subjectRef,
				severity: sdkmodel.SeverityHigh, approval: &dto,
				title: "Guardian containment QUEUED for approval — rule '" + ruleName + "' wants " + action + " (one human decision executes it)",
			})
		default:
			return validationError("guardian rule has unknown mode " + mode)
		}

		created, err := actRepo.Create(ctx, actRec)
		if err != nil {
			return err // unique (rule, finding) race -> conflict -> dedup'd by the winner
		}
		_, err = sc.Audit().Append(ctx, model.AuditDraft{
			Actor: model.ActorSystem, ActorKind: model.ActorSystem,
			Action: "governance.guardian.fire", TargetKind: guardianActionKind, TargetID: model.ID(created.String(model.ColID)),
			Meta: map[string]any{
				"rule": ruleID, "rule_name": ruleName, "action": action, "mode": mode,
				"finding_kind": f.Kind, "target_kind": targetKind, "status": created.String(colGAStatus),
			},
		})
		return err
	})
	if err != nil {
		if isConflict(err) {
			return // a concurrent delivery won the (rule, finding) race; converged
		}
		m.debugf("governance: guardian rule fire failed", "rule", ruleName, "err", err)
		return
	}
	m.emitGuardianPending(ctx, tenant, emits)
}

// guardianContainOutcome is what a containment execution reports back.
type guardianContainOutcome struct {
	status       string // executed | failed
	detail       string
	killswitchID string
	emits        []pendingGuardianEmit
}

// containLocked executes one containment inside the caller's transaction:
// stop_agent/stop_estate engage the kill switch (idempotent on the scope key),
// quarantine_nhi blocks the subject's NHI (find-or-create lifecycle row, the
// reversible containment). Failures are recorded honestly — a guardian
// that silently fails to contain is worse than none.
func (m *Module) containLocked(ctx context.Context, sc store.Scope, ruleID, ruleName, action, targetKind, targetRef string, f sdkmodel.FindingReport, now model.Timestamp) (guardianContainOutcome, error) {
	switch action {
	case gaActionStopAgent, gaActionStopEstate:
		scopeKind, scopeRef := ksScopeEstate, ""
		if action == gaActionStopAgent {
			scopeKind, scopeRef = ksScopeAgent, targetRef
		}
		reason := "guardian rule '" + ruleName + "': " + f.Kind + " (" + string(f.Severity) + ")"
		if len(reason) > maxNoteLen {
			reason = reason[:maxNoteLen]
		}
		out, err := m.engageKillSwitchLocked(ctx, sc, ksEngageParams{
			ScopeKind: scopeKind, ScopeRef: scopeRef, Reason: reason,
			Source: ksSourceGuardian, RuleRef: ruleID,
			Actor: model.ActorSystem, ActorKind: model.ActorSystem,
		}, now)
		if err != nil {
			return guardianContainOutcome{}, err
		}
		ksID := out.Record.String(model.ColID)
		o := guardianContainOutcome{status: gaStatusExecuted, killswitchID: ksID}
		if out.AlreadyActive {
			o.detail = "already stopped (" + ksID + ")"
			return o, nil
		}
		o.detail = "kill switch engaged"
		sev, title := sdkmodel.SeverityHigh, "Guardian AUTO-BLOCK — agent stopped by rule '"+ruleName+"'; re-enable requires dual-control"
		if scopeKind == ksScopeEstate {
			sev, title = sdkmodel.SeverityCritical, "Guardian AUTO-STOP — estate-wide kill switch engaged by rule '"+ruleName+"'; re-enable requires dual-control"
		}
		o.emits = append(o.emits,
			pendingGuardianEmit{findingKind: findingKillSwitchEngaged, subjectKind: "killswitch", subjectRef: ksID, severity: sev, title: title},
			pendingGuardianEmit{findingKind: findingGuardianExecuted, subjectKind: targetKindOrEstate(targetKind), subjectRef: targetRef,
				severity: sdkmodel.SeverityHigh, title: "Guardian containment executed — rule '" + ruleName + "' (" + action + ")"},
		)
		return o, nil

	case gaActionQuarantineNHI:
		identityRef := targetRef
		if targetKind == "agent" {
			ref, rerr := m.identityRefForAgent(ctx, sc, targetRef)
			if rerr != nil {
				return guardianContainOutcome{}, rerr
			}
			if ref == "" {
				return guardianContainOutcome{
					status: gaStatusFailed, detail: "agent has no resolvable bound NHI identity; quarantine cannot bite — consider a stop_agent rule",
					emits: []pendingGuardianEmit{{findingKind: findingGuardianFailed, subjectKind: "agent", subjectRef: targetRef,
						severity: sdkmodel.SeverityHigh, title: "Guardian containment FAILED — rule '" + ruleName + "' could not resolve the agent's NHI"}},
				}, nil
			}
			identityRef = ref
		}
		repo, rec, err := foLifecycle(ctx, sc, identityRef)
		if err != nil {
			return guardianContainOutcome{}, err
		}
		if rec.String(colNHIOffboard) == offboardFinal {
			return guardianContainOutcome{status: gaStatusExecuted, detail: "NHI already finalized (terminal)"}, nil
		}
		if rec.String(colNHIEnforce) == enforceBlocked {
			return guardianContainOutcome{status: gaStatusExecuted, detail: "NHI already blocked"}, nil
		}
		rec[colNHIEnforce] = enforceBlocked
		rec[colNHIEnforceWhy] = "guardian rule '" + ruleName + "' quarantine (" + f.Kind + ")"
		if _, err := repo.Update(ctx, rec); err != nil && !isConflict(err) {
			return guardianContainOutcome{}, err
		}
		if err := m.recordLifecycleEvent(ctx, sc, identityRef, "blocked", model.ActorSystem, "", "guardian quarantine, rule "+ruleName); err != nil {
			return guardianContainOutcome{}, err
		}
		return guardianContainOutcome{
			status: gaStatusExecuted, detail: "NHI quarantined (reversible via nhi restore)",
			emits: []pendingGuardianEmit{{findingKind: findingGuardianExecuted, subjectKind: "identity", subjectRef: identityRef,
				severity: sdkmodel.SeverityHigh, title: "Guardian AUTO-QUARANTINE — NHI blocked by rule '" + ruleName + "' (reversible via nhi restore)"}},
		}, nil
	}
	return guardianContainOutcome{}, validationError("unknown guardian action " + action)
}

// targetKindOrEstate maps an empty target kind to "estate" for finding subjects.
func targetKindOrEstate(k string) string {
	if k == "" {
		return "estate"
	}
	return k
}

// identityRefForAgent resolves an agent ref (UUID or external id) to its bound
// NHI identity external_id inside the caller's scope, or "" when unresolvable.
func (m *Module) identityRefForAgent(ctx context.Context, sc store.Scope, agentRef string) (string, error) {
	var agent *model.Agent
	if id, perr := model.ParseID(agentRef); perr == nil && !id.IsZero() {
		if a, gerr := sc.Agents().Get(ctx, id); gerr == nil {
			agent = &a
		} else if !isNotFound(gerr) {
			return "", gerr
		}
	}
	if agent == nil {
		agents, _, lerr := sc.Agents().List(ctx, model.Query{Filters: []model.Filter{eq("external_id", agentRef)}, Limit: 1})
		if lerr != nil {
			return "", lerr
		}
		if len(agents) == 0 {
			return "", nil
		}
		agent = &agents[0]
	}
	if agent.IdentityID.IsZero() {
		return "", nil
	}
	identity, err := sc.Identities().Get(ctx, agent.IdentityID)
	if err != nil {
		if isNotFound(err) {
			return "", nil
		}
		return "", err
	}
	return identity.ExternalID, nil
}

// emitGuardianPending publishes the collected post-commit emissions: findings
// on the notification rail and approval.requested for queued containments.
func (m *Module) emitGuardianPending(ctx context.Context, tenant model.TenantID, emits []pendingGuardianEmit) {
	for _, e := range emits {
		if e.findingKind != "" {
			m.emitGuardianFinding(ctx, tenant, e.findingKind, e.subjectKind, e.subjectRef, e.severity, e.title)
		}
		if e.approval != nil {
			m.emitApprovalRequested(ctx, tenant, *e.approval)
		}
	}
}

// emitGuardianFinding publishes one guardian finding (minimal data, fixed title).
func (m *Module) emitGuardianFinding(ctx context.Context, tenant model.TenantID, kind, subjectKind, subjectRef string, sev sdkmodel.Severity, title string) {
	if m.host == nil {
		return
	}
	sum := sha256.Sum256([]byte(kind + "|" + subjectKind + "|" + subjectRef + "|" + title))
	finding := sdkmodel.FindingReport{
		Kind:        kind,
		Severity:    sev,
		SubjectKind: subjectKind,
		SubjectRef:  subjectRef,
		Title:       title,
		DetailHash:  hex.EncodeToString(sum[:]),
		OccurredAt:  m.clock.Now().Time(),
	}
	if err := m.host.Publish(ctx, event.FromObservation(tenant.String(), Name, finding)); err != nil {
		m.debugf("governance: emit guardian finding failed", "err", err)
	}
}

// --- the sweep (executes approved containments; the cmd guardian pump drives it) --

// GuardianSweepResult reports one tenant pass of the guardian sweep.
type GuardianSweepResult struct {
	Executed int
	Rejected int
	Expired  int
	Failed   int
}

// GuardianSweep advances every PENDING guardian action whose approval reached a
// terminal state: approved → execute the containment now; rejected/canceled →
// rejected; expired → expired (stale intelligence is not acted on). Driven
// per-tenant by the cmd guardian pump (a module cannot enumerate tenants).
// Idempotent: every transition is state-change-guarded.
func (m *Module) GuardianSweep(ctx context.Context, tenant model.TenantID) (GuardianSweepResult, error) {
	var res GuardianSweepResult
	if m.data == nil {
		return res, errNoData
	}
	var pending []model.Record
	if err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(guardianActionKind)
		if err != nil {
			return err
		}
		var ierr error
		pending, ierr = listAll(ctx, repo, eq(colGAStatus, gaStatusPending))
		return ierr
	}); err != nil {
		return res, err
	}

	for _, p := range pending {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		actionID := model.ID(p.String(model.ColID))
		now := m.clock.Now()
		var emits []pendingGuardianEmit
		err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
			emits = emits[:0]
			actRepo, err := sc.Ext(guardianActionKind)
			if err != nil {
				return err
			}
			rec, err := actRepo.Get(ctx, actionID)
			if err != nil {
				return err
			}
			if rec.String(colGAStatus) != gaStatusPending {
				return nil // raced with another pass; converged
			}
			apprRepo, err := sc.Ext(approvalKind)
			if err != nil {
				return err
			}
			appr, err := apprRepo.Get(ctx, model.ID(rec.String(colGAApprovalID)))
			if err != nil {
				if isNotFound(err) {
					rec[colGAStatus] = gaStatusFailed
					rec[colGADetail] = "bound approval vanished"
					_, uerr := actRepo.Update(ctx, rec)
					res.Failed++
					return uerr
				}
				return err
			}
			switch effectiveStatus(appr, now) {
			case statusPending:
				return nil // still waiting for its human
			case statusApproved:
				outcome, cerr := m.containLocked(ctx, sc, rec.String(colGARule), rec.String(colGARuleName),
					rec.String(colGAAction), rec.String(colGATargetKind), rec.String(colGATargetRef),
					sdkmodel.FindingReport{Kind: rec.String(colGAFindingKind), Severity: sdkmodel.Severity(rec.String(colGASeverity))}, now)
				if cerr != nil {
					return cerr
				}
				rec[colGAStatus] = outcome.status
				rec[colGADetail] = outcome.detail
				rec[colGAExecutedAt] = now.String()
				if outcome.killswitchID != "" {
					rec[colGAKillswitchID] = outcome.killswitchID
				}
				if _, uerr := actRepo.Update(ctx, rec); uerr != nil {
					return uerr
				}
				emits = append(emits, outcome.emits...)
				if outcome.status == gaStatusExecuted {
					res.Executed++
				} else {
					res.Failed++
				}
				_, aerr := sc.Audit().Append(ctx, model.AuditDraft{
					Actor: model.ActorSystem, ActorKind: model.ActorSystem,
					Action: "governance.guardian.execute", TargetKind: guardianActionKind, TargetID: actionID,
					Meta: map[string]any{
						"rule": rec.String(colGARule), "action": rec.String(colGAAction),
						"approval": appr.String(model.ColID), "status": rec.String(colGAStatus),
					},
				})
				return aerr
			case statusRejected, statusCanceled:
				rec[colGAStatus] = gaStatusRejected
				rec[colGADetail] = "containment declined by human decision"
				if _, uerr := actRepo.Update(ctx, rec); uerr != nil {
					return uerr
				}
				res.Rejected++
				return nil
			case statusExpired:
				rec[colGAStatus] = gaStatusExpired
				rec[colGADetail] = "approval window lapsed before a human decided"
				if _, uerr := actRepo.Update(ctx, rec); uerr != nil {
					return uerr
				}
				res.Expired++
				return nil
			}
			return nil
		})
		if err != nil {
			if isConflict(err) {
				continue // another pass owns this row; converged
			}
			return res, err
		}
		m.emitGuardianPending(ctx, tenant, emits)
	}
	return res, nil
}
