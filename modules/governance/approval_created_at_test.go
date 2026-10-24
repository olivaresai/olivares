// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/modules/governance"
)

// The console's last-hour swimlane draws a waiting approval from the moment it was asked:
// the queue and the single read say when the request was made.
func TestApprovalSaysWhenItWasAsked(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "asked-at")
	session := cancellationSession(t, h, tenant, admin, "asked")
	before := time.Now().Add(-time.Second)
	a, err := h.gov.EngineApprovals().Request(context.Background(), tenant, session, governance.ApprovalRequest{
		Action: "claude.tool.use", SubjectKind: "claude.tool", SubjectRef: "asked", SessionRef: session.SessionIdentity,
	})
	if err != nil {
		t.Fatal(err)
	}
	after := time.Now().Add(time.Second)
	asked := func(where string, v any) {
		t.Helper()
		s, _ := v.(string)
		at, err := time.Parse(time.RFC3339Nano, s)
		if err != nil || at.Before(before) || at.After(after) {
			t.Fatalf("%s created_at = %q (%v), want the moment the request was made", where, s, err)
		}
	}
	r := h.do("GET", govPath+"/approvals/"+a.ID, admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("approval read = %d %s", r.code, r.raw)
	}
	asked("read", r.body["created_at"])
	r = h.do("GET", govPath+"/approvals?status=pending", admin, nil, tenantHdr(tenant))
	items, _ := r.body["items"].([]any)
	if r.code != http.StatusOK || len(items) != 1 {
		t.Fatalf("queue = %d %s", r.code, r.raw)
	}
	asked("queue", items[0].(map[string]any)["created_at"])
}
