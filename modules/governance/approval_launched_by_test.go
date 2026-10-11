// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/modules/governance"
)

// An administrator's privileged launch waited for approval and the queue said
// "Asked by: The session itself", a session that did not exist yet. The launch approval
// now names the person who launched it (launched_by, display only) while the session
// stays the proposer, so that administrator can still approve their own launch on an
// install with one administrator. Anything but a person's actor is dropped, never refused.
func TestEngineLaunchApprovalNamesTheLauncherAndKeepsSeparationOfDuty(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "launched-by")
	principal := cancellationSession(t, h, tenant, admin, "launch")
	launcher, err := h.authr.Authenticate(context.Background(), admin)
	if err != nil {
		t.Fatal(err)
	}
	service := h.gov.EngineApprovals()
	request := func(launchedBy string) governance.Approval {
		t.Helper()
		out, err := service.Request(context.Background(), tenant, principal, governance.ApprovalRequest{
			Action: "sessions.run.launch", SubjectKind: "sessions.run", SubjectRef: "run-launch-" + launchedBy,
			SessionRef: principal.SessionIdentity, LaunchedBy: launchedBy,
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	pending := request(launcher.Actor())
	if pending.LaunchedBy != launcher.Actor() || pending.RequestedBy != "session:"+principal.SessionIdentity {
		t.Fatalf("launch approval launched_by=%q requested_by=%q, want the launcher beside the session", pending.LaunchedBy, pending.RequestedBy)
	}
	if r := h.do("GET", govPath+"/approvals/"+pending.ID, admin, nil, tenantHdr(tenant)); r.code != http.StatusOK || r.body["launched_by"] != launcher.Actor() {
		t.Fatalf("approval read = %d %s, want launched_by", r.code, r.raw)
	}
	if r := h.do("POST", govPath+"/approvals/"+pending.ID+"/decisions", admin, map[string]any{"decision": "approve"}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("the launcher approving their own launch = %d %s, want separation of duty unchanged", r.code, r.raw)
	}
	if other := request("agent:somebody"); other.LaunchedBy != "" {
		t.Fatalf("a non-person launched_by = %q, want it dropped", other.LaunchedBy)
	}
}
