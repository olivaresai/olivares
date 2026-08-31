// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"net/http"
	"testing"
	"time"

	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// TestAdminActionCapabilityMapping pins every connector AdminAction to its governance
// capability string, and proves an unmodeled action is not mappable (deny-closed).
func TestAdminActionCapabilityMapping(t *testing.T) {
	cases := map[claudeapi.AdminAction]string{
		claudeapi.ActionDeactivateKey:       adminCapKeyDeactivate,
		claudeapi.ActionArchiveKey:          adminCapKeyArchive,
		claudeapi.ActionDeprovisionMember:   adminCapMemberDeprovision,
		claudeapi.ActionRevokeInvite:        adminCapInviteRevoke,
		claudeapi.ActionInviteMember:        adminCapInviteCreate,
		claudeapi.ActionUpdateMemberRole:    adminCapMemberRoleUpdate,
		claudeapi.ActionAddWorkspaceMember:  adminCapWorkspaceMemberAdd,
		claudeapi.ActionGrantWorkspaceAdmin: adminCapWorkspaceAdminGrant,
		claudeapi.ActionArchiveWorkspace:    adminCapWorkspaceArchive,
	}
	for action, want := range cases {
		got, ok := adminActionCapability(action)
		if !ok || got != want {
			t.Errorf("adminActionCapability(%q) = %q,%v want %q,true", action, got, ok, want)
		}
	}
	if _, ok := adminActionCapability(claudeapi.AdminAction("unknown_action")); ok {
		t.Error("an unmodeled action must not be mappable (deny-closed)")
	}
}

// TestAdminActionDualControl proves only workspace archive and workspace-admin grant use
// dual-control/no-break-glass routing; the rest are recoverable single-HITL actions.
func TestAdminActionDualControl(t *testing.T) {
	if !adminActionDualControl(claudeapi.ActionArchiveWorkspace) {
		t.Error("archive_workspace must require dual-control")
	}
	if !adminActionDualControl(claudeapi.ActionGrantWorkspaceAdmin) {
		t.Error("grant_workspace_admin must require dual-control")
	}
	for _, a := range []claudeapi.AdminAction{
		claudeapi.ActionDeactivateKey, claudeapi.ActionArchiveKey,
		claudeapi.ActionDeprovisionMember, claudeapi.ActionRevokeInvite,
		claudeapi.ActionInviteMember, claudeapi.ActionUpdateMemberRole,
		claudeapi.ActionAddWorkspaceMember,
	} {
		if adminActionDualControl(a) {
			t.Errorf("%q must be single-HITL, not dual-control", a)
		}
	}
}

// TestAdminActionStatusMapping proves every non-approved neutral status denies, and
// break-glass maps to approved (for the recoverable path; the connector's dual-control
// re-check independently denies it for the irreversible one).
func TestAdminActionStatusMapping(t *testing.T) {
	approved := map[string]bool{nbApproved: true, nbBreakGlass: true}
	for _, st := range []string{nbApproved, nbBreakGlass} {
		if adminActionStatusOf(st) != claudeapi.AdminApproved {
			t.Errorf("status %q must map to AdminApproved", st)
		}
	}
	for _, st := range []string{nbPending, nbRejected, nbCanceled, nbExpired, nbNoGate, "weird"} {
		if approved[st] {
			continue
		}
		if adminActionStatusOf(st) == claudeapi.AdminApproved {
			t.Errorf("status %q must NOT authorize", st)
		}
	}
}

// TestAdminGateFailsClosedUnconfiguredTenant proves the adapter denies (no_gate) when the
// approval bridge has no service credential for the tenant — the deny-closed default that
// mirrors the connector's own denyAdminGate.
func TestAdminGateFailsClosedUnconfiguredTenant(t *testing.T) {
	b := &approvalBridge{creds: map[model.TenantID]serviceCred{}, log: discardLog(), clock: time.Now, memo: map[string]string{}}
	tid := mustTenant(t)
	gate := b.adminGate(tid)
	dec, err := gate.Authorize(context.Background(), claudeapi.AdminActionRequest{
		Tenant: tid.String(), Action: claudeapi.ActionDeactivateKey, SubjectKind: "api_key",
		SubjectRef: "apikey_1", PlanHash: "plan-abc",
	})
	if err != nil {
		t.Fatalf("unconfigured tenant must not error, got %v", err)
	}
	if dec.Allowed() || dec.Status != claudeapi.AdminNoGate {
		t.Fatalf("unconfigured tenant must deny no_gate, got %+v", dec)
	}
	if dec.PlanHash != "plan-abc" {
		t.Errorf("no-gate decision must echo the plan, got %q", dec.PlanHash)
	}
}

// TestAdminGateUnmodeledActionDenies proves an action the adapter does not model is a
// deny-closed no_gate (never an authorization), independent of the bridge.
func TestAdminGateUnmodeledActionDenies(t *testing.T) {
	b := &approvalBridge{creds: map[model.TenantID]serviceCred{}, log: discardLog(), clock: time.Now, memo: map[string]string{}}
	tid := mustTenant(t)
	dec, err := b.adminGate(tid).Authorize(context.Background(), claudeapi.AdminActionRequest{
		Tenant: tid.String(), Action: claudeapi.AdminAction("frobnicate"), SubjectRef: "x", PlanHash: "p",
	})
	if err != nil || dec.Allowed() || dec.Status != claudeapi.AdminNoGate {
		t.Fatalf("unmodeled action must deny no_gate, got %+v err=%v", dec, err)
	}
}

// TestAdminGateWorkspaceAdminGrantNoBreakGlassAndEvidence proves the privilege-critical
// workspace-admin grant routes through gateOnceNoBreakGlass and returns approver evidence
// only after the two-human quorum is satisfied.
func TestAdminGateWorkspaceAdminGrantNoBreakGlassAndEvidence(t *testing.T) {
	h := newHarness(t)
	_, approverB := h.createApprover(t, "adm-grant-b@bridge.test")
	_, approverC := h.createApprover(t, "adm-grant-c@bridge.test")
	br := buildBridge(t, h, h.mintBoundToken(t, auth.RoleEditor))
	tid := tenantAID(t, h)
	ctx := context.Background()
	gate := br.adminGate(tid)

	req := claudeapi.AdminActionRequest{
		Tenant: tid.String(), Action: claudeapi.ActionGrantWorkspaceAdmin,
		SubjectKind: "workspace_member", SubjectRef: "wrkspc_e2e:user_e2e",
		PlanHash: "plan-ws-admin-1", RequestedBy: "tester",
	}
	dec, err := gate.Authorize(ctx, req)
	if err != nil || dec.Status != claudeapi.AdminPending {
		t.Fatalf("first authorize = %q err=%v, want pending", dec.Status, err)
	}
	m := h.getJSON(h.adminToken, h.tenantA, "/v1/m/governance/approvals/"+dec.ApprovalRef)
	if m["required_approvals"] != float64(2) || m["risk_tier"] != "critical" {
		t.Fatalf("workspace-admin grant must be floored at 2 (critical): required=%v tier=%v", m["required_approvals"], m["risk_tier"])
	}

	h.activateBreakGlassE2E(t, "", "workspace admin emergency path must not apply")
	if d, _ := gate.Authorize(ctx, req); d.Status != claudeapi.AdminPending || d.Allowed() {
		t.Fatalf("break-glass must not release workspace-admin grant, got %+v", d)
	}

	if code, body := h.decide(t, approverB, dec.ApprovalRef, "approve"); code != http.StatusOK {
		t.Fatalf("first approve = %d: %s", code, body)
	}
	if d, _ := gate.Authorize(ctx, req); d.Status != claudeapi.AdminPending || d.HasDualControl() {
		t.Fatalf("one approver must not release workspace-admin grant, got %+v", d)
	}
	if code, body := h.decide(t, approverC, dec.ApprovalRef, "approve"); code != http.StatusOK {
		t.Fatalf("second approve = %d: %s", code, body)
	}
	d, err := gate.Authorize(ctx, req)
	if err != nil || d.Status != claudeapi.AdminApproved || !d.Allowed() {
		t.Fatalf("two approvers must release workspace-admin grant, got %+v err=%v", d, err)
	}
	if d.PlanHash != req.PlanHash || !d.HasDualControl() {
		t.Fatalf("approved grant must carry bound plan and dual-control evidence, got %+v", d)
	}
}

func mustTenant(t *testing.T) model.TenantID {
	t.Helper()
	tid, err := model.ParseTenantID("11111111-1111-1111-1111-111111111111")
	if err != nil {
		t.Fatalf("parse tenant: %v", err)
	}
	return tid
}
