// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"

	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/core/model"
)

// adminactiongate.go wires the governed Claude Admin-API actuator's HITL seam
// (claudeapi.AdminActionGate) to the approval bridge — the same pattern erasegate.go
// uses for the RTBF eraser. It is what turns the connector's inert deny-closed actuator
// into a really-governed one: every Admin-API WRITE (deactivate/archive a key, invite or
// deprovision a member, update member roles, revoke an invite, add workspace members,
// grant workspace admin, archive a workspace) now opens a governed approval bound to
// the exact PlanHash the approver sees (anti-TOCTOU).
//
// Three groups, by blast radius (mirrors the connector):
//   - RECOVERABLE single-HITL actions MAY proceed under an break-glass emergency
//     grant (they can be undone: re-activate a key, re-invite a member, change a role
//     back, remove a workspace member). They use gateOnce.
//   - The PRIVILEGE-GRANTING workspace-admin grant is recoverable but CRITICAL, so the
//     engine floors its threshold at TWO distinct human approvers. It uses
//     gateOnceNoBreakGlass and returns approver evidence for the connector's quorum
//     re-check.
//   - The IRREVERSIBLE workspace archive is also CRITICAL and uses the same
//     gateOnceNoBreakGlass + approver-evidence path. A break-glass authorization carries
//     no approvers, so a stray emergency grant could never satisfy the connector's quorum
//     re-check: deny-closed twice.
//
// Deny-closed at every edge: an unmodeled action, a mismatched/invalid tenant, an
// unconfigured credential or any bridge error is a no_gate/error deny, and the zero
// AdminActionDecision already fails Allowed().

// Governed-action capability strings (the action names the engine classifies and the
// operator approval policies match on), over the "<domain>.<entity>.<verb>" convention.
const (
	adminCapKeyDeactivate      = "claude.admin.key.deactivate"
	adminCapKeyArchive         = "claude.admin.key.archive"
	adminCapMemberDeprovision  = "claude.admin.member.deprovision"
	adminCapInviteRevoke       = "claude.admin.invite.revoke"
	adminCapInviteCreate       = "claude.admin.invite.create"
	adminCapMemberRoleUpdate   = "claude.admin.member.role_update"
	adminCapWorkspaceMemberAdd = "claude.admin.workspace.member_add"
	// adminCapWorkspaceAdminGrant is in the default CRITICAL set (risktier.go) → two-person
	// floor. It is recoverable but privilege-critical.
	adminCapWorkspaceAdminGrant = "claude.admin.workspace.admin_grant"
	// adminCapWorkspaceArchive is in the default CRITICAL set (risktier.go) → two-person
	// floor. It is the only irreversible admin action.
	adminCapWorkspaceArchive = "claude.admin.workspace.archive"
)

// adminActionCapability maps a connector AdminAction to its governance capability string.
// An unmodeled action returns ok=false → the adapter denies (deny-closed).
func adminActionCapability(action claudeapi.AdminAction) (string, bool) {
	switch action {
	case claudeapi.ActionDeactivateKey:
		return adminCapKeyDeactivate, true
	case claudeapi.ActionArchiveKey:
		return adminCapKeyArchive, true
	case claudeapi.ActionDeprovisionMember:
		return adminCapMemberDeprovision, true
	case claudeapi.ActionRevokeInvite:
		return adminCapInviteRevoke, true
	case claudeapi.ActionInviteMember:
		return adminCapInviteCreate, true
	case claudeapi.ActionUpdateMemberRole:
		return adminCapMemberRoleUpdate, true
	case claudeapi.ActionAddWorkspaceMember:
		return adminCapWorkspaceMemberAdd, true
	case claudeapi.ActionGrantWorkspaceAdmin:
		return adminCapWorkspaceAdminGrant, true
	case claudeapi.ActionArchiveWorkspace:
		return adminCapWorkspaceArchive, true
	default:
		return "", false
	}
}

// adminActionDualControl reports whether the action must use two-person control with no
// break-glass and approver evidence for the connector's quorum re-check.
func adminActionDualControl(action claudeapi.AdminAction) bool {
	return action == claudeapi.ActionArchiveWorkspace || action == claudeapi.ActionGrantWorkspaceAdmin
}

// adminGate returns the AdminActionGate adapter for one business tenant (the actuator is
// constructed per tenant deployment; the gate seam carries the tenant in the request,
// which the adapter validates against its pinned tenant).
func (b *approvalBridge) adminGate(tenant model.TenantID) claudeapi.AdminActionGate {
	return adminActionApprovalAdapter{b: b, tenant: tenant}
}

var _ claudeapi.AdminActionGate = adminActionApprovalAdapter{}

type adminActionApprovalAdapter struct {
	b      *approvalBridge
	tenant model.TenantID
}

// Authorize opens/finds the governed approval for this exact admin action and reports the
// decision (with dual-control evidence for actions that require it). Deny-closed at every
// edge — the zero AdminActionDecision fails Allowed().
func (a adminActionApprovalAdapter) Authorize(ctx context.Context, req claudeapi.AdminActionRequest) (claudeapi.AdminActionDecision, error) {
	capability, ok := adminActionCapability(req.Action)
	if !ok {
		// An action this adapter does not model is a provisioning error, not an
		// authorization: mirror the connector's denyAdminGate shape.
		return adminNoGate(req.PlanHash), nil
	}
	tid, present, err := parseBusinessTenant("admin action request: tenant", req.Tenant)
	if err != nil || !present || tid != a.tenant {
		return adminNoGate(req.PlanHash), nil
	}
	subjectKind := "claude_admin." + req.SubjectKind
	reason := "governed Anthropic admin action (" + string(req.Action) + ")"
	dualControl := adminActionDualControl(req.Action)

	var ref, status, boundHash string
	if dualControl {
		ref, status, boundHash, err = a.b.gateOnceNoBreakGlass(ctx, tid, capability, subjectKind, req.SubjectRef, req.PlanHash, reason, req.RequestedBy)
	} else {
		ref, status, boundHash, err = a.b.gateOnce(ctx, tid, capability, subjectKind, req.SubjectRef, req.PlanHash, reason, req.RequestedBy)
	}
	if err != nil {
		return claudeapi.AdminActionDecision{}, err
	}
	dec := claudeapi.AdminActionDecision{ApprovalRef: ref, Status: adminActionStatusOf(status), PlanHash: boundHash}
	if dualControl && status == nbApproved {
		// The dual-control evidence from immutable decision trail: the credentials
		// for provenance and the distinct PEOPLE for the quorum the connector re-checks.
		// A read failure degrades to zero of both — the connector's quorum re-check then
		// denies (deny-closed, never a fabricated approver).
		if cred, ok := a.b.cred(tid); ok {
			ev := a.b.approvalApproverEvidence(ctx, cred, ref)
			dec.Approvers, dec.ApproverPersons = ev.Actors, ev.Persons
		}
	}
	return dec, nil
}

// adminNoGate is the connector's denyAdminGate-shaped deny (an unmodeled action or a
// tenant this gate was not built for).
func adminNoGate(planHash string) claudeapi.AdminActionDecision {
	return claudeapi.AdminActionDecision{ApprovalRef: noGateRefPrefix + planHash, Status: claudeapi.AdminNoGate, PlanHash: planHash}
}

// adminActionStatusOf maps the bridge's neutral status onto the admin-action vocabulary;
// every non-approved value is a deny. A break-glass authorization maps to approved for a
// RECOVERABLE action; for dual-control actions it is unreachable (gateOnceNoBreakGlass
// never consults break-glass) and, even if it were, carries no approvers, so the
// connector's quorum re-check denies it.
func adminActionStatusOf(neutral string) claudeapi.AdminActionStatus {
	switch neutral {
	case nbApproved, nbBreakGlass:
		return claudeapi.AdminApproved
	case nbPending:
		return claudeapi.AdminPending
	case nbRejected, nbCanceled:
		return claudeapi.AdminRejected
	case nbExpired:
		return claudeapi.AdminExpired
	default:
		return claudeapi.AdminNoGate
	}
}
