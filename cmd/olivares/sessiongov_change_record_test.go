// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/modules/sessions"
)

// A privileged launch whose approval policy names a change record is allowed while the
// record is scheduled and refused, with the approval revoked, once the record is
// canceled: the gate answers before the session module issues anything to the launch.
func TestPrivilegedLaunchIsRefusedOnceItsChangeRecordIsCanceled(t *testing.T) {
	h := newHarness(t)
	h.set.gov.UseApprovalCapacity(h.authr.ApprovalCapacity)
	var (
		mu    sync.Mutex
		state = "Scheduled"
	)
	setState := func(s string) { mu.Lock(); state = s; mu.Unlock() }
	h.set.gov.UseChangeRecordReader(func(context.Context, model.TenantID, string, string, string) (governance.ChangeRecord, error) {
		mu.Lock()
		defer mu.Unlock()
		return governance.ChangeRecord{State: state}, nil
	})
	ctx := context.Background()
	tenant := model.TenantID(h.tenantA)
	if err := h.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, err := sc.Policies().Create(ctx, model.Policy{Name: "launch under CHG0030001", Kind: "approval", Enabled: true, Spec: map[string]any{
			"required_approvals": 1, "match": map[string]any{"action": sessionLaunchAction},
			"change_record": map[string]any{"system": "servicenow", "key": "CHG0030001"},
		}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_, approver := h.createApprover(t, "launch-approver@bridge.test")
	intent := sessions.LaunchIntent{
		Action: sessions.LaunchActionCreate, RunRef: "run-change-1", Transport: sessions.TransportStreamJSON,
		PermissionMode: "default", WorkspaceClassified: true, WorkspaceReadWrite: true,
		WorkspaceRef: "test-folder", Actor: "user:initiator",
	}
	gate := &sessionLaunchGate{bridge: localDefaultBridge(t, h, h.h), log: discardLog()}

	pending, err := gate.Authorize(ctx, tenant, intent)
	if err != nil || pending.Allowed || pending.DeniedStatus != http.StatusAccepted || pending.ApprovalRef == "" {
		t.Fatalf("first launch = %+v (%v), want a pending approval", pending, err)
	}
	if code, body := h.decide(t, approver, pending.ApprovalRef, "approve"); code != http.StatusOK {
		t.Fatalf("approve with the record Scheduled = %d %s", code, body)
	}

	setState("Canceled")
	refused, err := gate.Authorize(ctx, tenant, intent)
	if err != nil || refused.Allowed || refused.DeniedStatus == http.StatusAccepted || !strings.Contains(refused.Reason, "status=canceled") {
		t.Fatalf("launch after the record was canceled = %+v (%v), want a refusal and nothing issued", refused, err)
	}
	read := h.getJSON(h.adminToken, h.tenantA, "/v1/m/governance/approvals/"+pending.ApprovalRef)
	if read["status"] != "canceled" {
		t.Fatalf("approval after the refusal = %v, want canceled", read["status"])
	}
}
