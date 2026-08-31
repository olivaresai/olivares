// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import "strings"

// 4-tier action risk classification (OWASP AI Agent Security Cheat Sheet)
// with a MANDATORY two-person floor on CRITICAL (NIST SP 800-53 AC-3(2) dual
// authorization SEC-G1).
//
// The tier is an APPROVAL-LAYER classification, deliberately not a PDP decision
// type: the auth.PolicyEvaluator seam is binary deny-only by contract, so risk
// classification rides the approval policy ("approval" kind) where the
// threshold already lives. An explicit `risk_tier` on a matching approval policy
// is authoritative (the operator's audited word — Makes the set
// configurable by policy); with no explicit classification the built-in default
// below applies. The tier is NEVER stored on the approval row: it is re-derived
// from the CURRENT policy + default at every security decision, so a policy
// change (or removal) takes effect immediately and a stale snapshot can never
// hold the bar lower than the live classification (deny-closed).
//
// Engine effect (this module): a CRITICAL action's approval threshold is floored
// at two distinct human approvers — enforced at CREATE (the stored
// required_approvals can never start below the floor) AND re-derived at DECIDE
// (a legacy or downgraded row cannot cross the threshold with one approver).
// Two DISTINCT identities, anti-self-approval and one-decision-per-human are the
// engine's existing invariants (requested_by_user SoD check, the duplicate-
// decider guard and the unique (tenant, approval, decider_user) index), so the
// floor composes with them into real two-person control, not a counter.
// Tiers low/medium/high do not change the engine's behavior (an approval that
// exists already demands ≥1 human); they are the policy vocabulary other
// consumers key on (blocks on CRITICAL steps up auth on CRITICAL).

// ActionRiskTier is the OWASP 4-tier risk classification of a governed action.
// It is a DIFFERENT taxonomy from the EU AI Act compliance.RiskTier
// (unacceptable/high/limited/minimal) — do not conflate the two.
type ActionRiskTier string

// The four tiers, lowest to highest risk.
const (
	RiskTierLow      ActionRiskTier = "low"
	RiskTierMedium   ActionRiskTier = "medium"
	RiskTierHigh     ActionRiskTier = "high"
	RiskTierCritical ActionRiskTier = "critical"
)

// criticalApprovalFloor is the AC-3(2) dual-authorization floor: a CRITICAL
// action needs at least this many distinct human approvals. Neither a request
// nor a matching approval policy can set a CRITICAL action's threshold below it.
const criticalApprovalFloor = 2

// validRiskTier reports whether s names one of the four tiers.
func validRiskTier(s string) bool {
	switch ActionRiskTier(s) {
	case RiskTierLow, RiskTierMedium, RiskTierHigh, RiskTierCritical:
		return true
	default:
		return false
	}
}

// Built-in default CRITICAL classification set:
// production deploy/retire, data deletion, security-policy changes, re-enable
// after a kill-switch, and key custody/rotation changes. Expressed over
// the governed-action naming convention ("<domain>.<entity>.<verb>"): exact
// matches for the gate actions that exist today, domain prefixes for the decided
// families whose actions land later (kill-switch secrets/PKI), and
// destructive trailing verbs for the data-deletion family. An approval policy
// with an explicit risk_tier overrides this default per action (configurable by policy).
var (
	criticalDefaultActions = map[string]struct{}{
		"deploy.apply":                {}, // production deploy
		"deploy.retire":               {}, // production retire
		"security.enforcement.enable": {}, // enforcement-posture change
		"compliance.content.erase":    {}, // RTBF data deletion (claude-compliance)
		// A ledger recovery permanently seals an epoch boundary around corrupt
		// evidence. The rows remain immutable, but the governance decision is
		// irreversible and therefore requires two humans with no break-glass path.
		"audit.ledger.recover": {},
		// An approval for "mcp.tool.call" is ONLY ever opened for a server-classified
		// DESTRUCTIVE MCP tool — the inline MCP PEP consults the gate solely when
		// policy.Destructive (connectors/mcp/rs.go), never for a read-only/benign
		// tool. So a destructive tool-routed mutation on customer infra (db.drop_table,
		// a delete operation) is squarely the "data deletion" CRITICAL
		// class, and dual-control is the secure default. (An operator may downgrade a
		// tenant's MCP gate to HIGH via an explicit risk_tier policy if too strict.)
		"mcp.tool.call": {},
		// NHI lifecycle actuation on customer credential infra. Rotating an NHI
		// key/secret is a "key custody/rotation change" — explicitly in the default
		// CRITICAL set — and finalizing an offboarding definitively
		// revokes a credential (irreversible). Both demand the two-person floor; the
		// soft-delete offboard step ("nhi.offboard") stays HIGH (reversible within the
		// audited recovery window). An operator may retune via an explicit risk_tier.
		"nhi.rotate":            {},
		"nhi.offboard.finalize": {},
		// releasing a legal hold re-enables destruction of records a
		// preservation duty froze — it is the gateway to the data-deletion family
		// (the subsequent purge/erasure verbs are themselves CRITICAL), so the
		// release demands the same two-person floor. Placing a hold is the SAFE
		// direction (preservation) and is deliberately not gated.
		"compliance.hold.release": {},
		// launching/resuming a PRIVILEGED operated Claude Code session — one in
		// bypassPermissions/dontAsk mode, or with read-write access to a classified
		// workspace. An approval is opened ONLY for such a launch (the session LaunchGate
		// gates the privileged set), so the action is CRITICAL: privileged autonomous
		// operation on customer infra demands the two-person floor + AAL3 step-up, like a
		// production deploy. A non-privileged session never reaches this approval.
		"sessions.run.launch": {},
		// archiving an Anthropic workspace as the FinOps defense-in-depth backstop.
		// Archiving IMMEDIATELY revokes EVERY API key in the workspace and CANNOT be
		// undone (claude-api adminactions.go), so it demands the two-person floor — the
		// same posture as the irreversible RTBF erase. The recoverable admin actions
		// (claude.admin.key.deactivate/archive, member.deprovision, invite.revoke) stay
		// HIGH (single approval). The ".archive" verb is deliberately NOT a critical
		// suffix (an api-key archive is recoverable), so workspace archive is named
		// explicitly here.
		"claude.admin.workspace.archive": {},
		// E2: granting workspace-admin is recoverable but privilege-critical. The
		// connector re-checks a ≥2 distinct-human quorum for grant_workspace_admin, so the
		// engine must floor this action at two approvers by default; otherwise a default
		// single-approval request could never satisfy the connector.
		"claude.admin.workspace.admin_grant": {},
		// AgentCore export deletes or downgrades remote ACTIVE policies; that
		// weakens enforcement, so AC-3(2) dual authorization is the safe floor.
		"agentcore.export.apply_weakening": {},
	}
	criticalDefaultPrefixes = []string{
		"security.enforcement.", // enforcement-posture family
		"security.killswitch.",  // re-enable after kill-switch (family)
		"secrets.",              // key custody/rotation families
		"keys.",
		"kms.",
		"pki.",
	}
	criticalDefaultSuffixes = []string{
		".erase", ".delete", ".purge", ".destroy", ".wipe", // data-deletion verbs
	}
)

// defaultActionRiskTier classifies an action when no approval policy speaks: the
// Default CRITICAL set, else HIGH — an action that reaches the
// approval queue was already classified "ask" by the disposition layer, so high
// (single human approval) is its honest default; low/medium are only ever
// assigned explicitly by policy.
func defaultActionRiskTier(action string) ActionRiskTier {
	a := strings.ToLower(strings.TrimSpace(action))
	if _, ok := criticalDefaultActions[a]; ok {
		return RiskTierCritical
	}
	for _, p := range criticalDefaultPrefixes {
		if strings.HasPrefix(a, p) {
			return RiskTierCritical
		}
	}
	for _, s := range criticalDefaultSuffixes {
		if strings.HasSuffix(a, s) {
			return RiskTierCritical
		}
	}
	return RiskTierHigh
}

// resolveRiskTier is the one classification rule: the matched approval policy's
// explicit risk_tier when it names one (the operator's audited, authoritative
// word — it may raise OR lower the default, that is what "configurable by
// policy" means), else the built-in default for the action.
func resolveRiskTier(spec approvalSpec, matched bool, action string) ActionRiskTier {
	if matched && spec.RiskTier != "" {
		return ActionRiskTier(spec.RiskTier)
	}
	return defaultActionRiskTier(action)
}

// floorRequiredApprovals applies the CRITICAL dual-authorization floor to a
// threshold (deny-closed: it can only raise, never lower).
func floorRequiredApprovals(required int64, tier ActionRiskTier) int64 {
	if tier == RiskTierCritical && required < criticalApprovalFloor {
		return criticalApprovalFloor
	}
	return required
}
