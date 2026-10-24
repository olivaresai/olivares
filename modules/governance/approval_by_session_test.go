// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/modules/governance"
)

// The session facts count what a person approved per session, in one read: a pending
// request and another session's approval do not count.
func TestEngineApprovedBySessionCountsOnlyApprovedRequestsOfTheSession(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "approved-by-session")
	first := cancellationSession(t, h, tenant, admin, "first")
	second := cancellationSession(t, h, tenant, admin, "second")
	service := h.gov.EngineApprovals()
	ctx := context.Background()
	request := func(session auth.Principal, subject string) governance.Approval {
		t.Helper()
		a, err := service.Request(ctx, tenant, session, governance.ApprovalRequest{Action: "claude.tool.use", SubjectKind: "claude.tool", SubjectRef: subject, SessionRef: session.SessionIdentity})
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	approved := request(first, "a")
	request(first, "b") // stays pending
	elsewhere := request(second, "c")
	for _, a := range []governance.Approval{approved, elsewhere} {
		if r := h.decide(admin, tenant, a.ID, "approve"); r.code != 200 || r.body["status"] != "approved" {
			t.Fatalf("approve = %d %s", r.code, r.raw)
		}
	}
	got, err := service.ApprovedBySession(ctx, tenant, []string{first.SessionIdentity, "unknown-session"})
	if err != nil {
		t.Fatal(err)
	}
	if got[first.SessionIdentity] != 1 || len(got) != 1 {
		t.Fatalf("ApprovedBySession = %v, want exactly 1 for %q", got, first.SessionIdentity)
	}
	if none, err := service.ApprovedBySession(ctx, tenant, nil); err != nil || len(none) != 0 {
		t.Fatalf("empty ask = %v, %v", none, err)
	}
}
