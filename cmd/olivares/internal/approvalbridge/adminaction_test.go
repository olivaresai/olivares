// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package approvalbridge

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	claudecompliance "github.com/olivaresai/olivares/connectors/claude-compliance"
	"github.com/olivaresai/olivares/core/model"
)

// TestAdminActionCapabilityMapping pins every connector AdminAction to its governance
// capability string, and proves an unmodeled action is not mappable (deny-closed).
func TestAdminActionCapabilityMapping(t *testing.T) {
	cases := map[claudeapi.AdminAction]string{
		claudeapi.ActionDeactivateKey:       AdminCapKeyDeactivate,
		claudeapi.ActionArchiveKey:          AdminCapKeyArchive,
		claudeapi.ActionDeprovisionMember:   AdminCapMemberDeprovision,
		claudeapi.ActionRevokeInvite:        AdminCapInviteRevoke,
		claudeapi.ActionInviteMember:        AdminCapInviteCreate,
		claudeapi.ActionUpdateMemberRole:    AdminCapMemberRoleUpdate,
		claudeapi.ActionAddWorkspaceMember:  AdminCapWorkspaceMemberAdd,
		claudeapi.ActionGrantWorkspaceAdmin: AdminCapWorkspaceAdminGrant,
		claudeapi.ActionArchiveWorkspace:    AdminCapWorkspaceArchive,
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
	approved := map[string]bool{Approved: true, BreakGlass: true}
	for _, st := range []string{Approved, BreakGlass} {
		if adminActionStatusOf(st) != claudeapi.AdminApproved {
			t.Errorf("status %q must map to AdminApproved", st)
		}
	}
	for _, st := range []string{Pending, Rejected, Canceled, Expired, NoGate, "weird"} {
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
	b := &Bridge{creds: map[model.TenantID]ServiceCred{}, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), parseTenant: testParseTenant, Clock: time.Now, memo: map[string]string{}}
	tid := mustTenant(t)
	gate := b.AdminGate(tid)
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
	b := &Bridge{creds: map[model.TenantID]ServiceCred{}, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), parseTenant: testParseTenant, Clock: time.Now, memo: map[string]string{}}
	tid := mustTenant(t)
	dec, err := b.AdminGate(tid).Authorize(context.Background(), claudeapi.AdminActionRequest{
		Tenant: tid.String(), Action: claudeapi.AdminAction("frobnicate"), SubjectRef: "x", PlanHash: "p",
	})
	if err != nil || dec.Allowed() || dec.Status != claudeapi.AdminNoGate {
		t.Fatalf("unmodeled action must deny no_gate, got %+v err=%v", dec, err)
	}
}

// TestBridgeWithoutTenantParserDenies proves a bridge built by NewWithCreds, which has
// no business-tenant parser, denies admin and erase requests instead of panicking, even
// for a tenant it holds a credential for.
func TestBridgeWithoutTenantParserDenies(t *testing.T) {
	tid := mustTenant(t)
	b := NewWithCreds(map[model.TenantID]ServiceCred{tid: {Tenant: tid, TenantStr: tid.String(), Token: "svc-token", ExpiresIn: 3600}},
		time.Now, slog.New(slog.NewTextHandler(io.Discard, nil)))
	dec, err := b.AdminGate(tid).Authorize(context.Background(), claudeapi.AdminActionRequest{
		Tenant: tid.String(), Action: claudeapi.ActionDeactivateKey, SubjectKind: "api_key", SubjectRef: "apikey_1", PlanHash: "p",
	})
	if err != nil || dec.Allowed() || dec.Status != claudeapi.AdminNoGate {
		t.Fatalf("admin gate without a tenant parser must deny no_gate, got %+v err=%v", dec, err)
	}
	edec, err := b.EraseGate(tid).Authorize(context.Background(), claudecompliance.EraseRequest{Tenant: tid.String(), PlanHash: "p"})
	if err != nil || edec.Status != claudecompliance.EraseNoGate {
		t.Fatalf("erase gate without a tenant parser must deny no_gate, got %+v err=%v", edec, err)
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

// testParseTenant stands in for the composition root's business-tenant parser.
func testParseTenant(_, raw string) (model.TenantID, bool, error) {
	tid, err := model.ParseTenantID(raw)
	return tid, err == nil, err
}
