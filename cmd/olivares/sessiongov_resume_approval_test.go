// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

// A privileged resume is approved once and spent once. When the same session is resumed
// again, after an engine restart or without one, the earlier approval is spent for good:
// the resume must open a fresh request for a human, not refuse as "already consumed" with
// nothing to approve. Each restart is a new bridge over the same engine, so no in-process
// memory survives it and the approval store alone says which approvals are still unspent.
// It is proved over both ways the bridge reaches that store: in process, and through the
// governed API as a service credential, where the spent flag travels as JSON.
func TestPrivilegedResumeAfterRestartOpensAFreshApproval(t *testing.T) {
	for _, via := range []string{"in-process", "service credential"} {
		t.Run(via, func(t *testing.T) { privilegedResumeAfterRestart(t, via == "service credential") })
	}
}

func privilegedResumeAfterRestart(t *testing.T, viaAPI bool) {
	h := newHarness(t)
	h.set.gov.UseApprovalCapacity(h.authr.ApprovalCapacity)
	ctx := context.Background()
	tenant := model.TenantID(h.tenantA)
	intent := sessions.LaunchIntent{
		Action: sessions.LaunchActionResume, RunRef: "run-resume-1", Transport: sessions.TransportStreamJSON,
		PermissionMode: "default", WorkspaceClassified: true, WorkspaceReadWrite: true,
		WorkspaceRef: "test-folder", Actor: "user:initiator",
	}
	serviceToken, approver := "", h.adminToken
	if viaAPI {
		// The service credential proposes; a person other than its owner decides.
		serviceToken = h.mintBoundToken(t, "editor")
		_, approver = h.createApprover(t, "resume-approver@bridge.test")
	}
	restart := func() *sessionLaunchGate {
		if viaAPI {
			return &sessionLaunchGate{bridge: buildBridge(t, h, serviceToken), log: discardLog()}
		}
		return &sessionLaunchGate{bridge: localDefaultBridge(t, h, h.h), log: discardLog()}
	}
	pending := func() int {
		list := h.getJSON(h.adminToken, h.tenantA, "/v1/m/governance/approvals?status=pending&action="+sessionLaunchAction)
		return len(items(list))
	}
	// asksAHuman resumes and wants exactly one pending request, found again by a repeat.
	asksAHuman := func(step string, gate *sessionLaunchGate) string {
		t.Helper()
		var ref string
		for range 2 {
			decision, err := gate.Authorize(ctx, tenant, intent)
			if err != nil || decision.Allowed || decision.DeniedStatus != http.StatusAccepted || decision.ApprovalRef == "" {
				t.Fatalf("%s: want a pending approval to grant, got %+v (%v)", step, decision, err)
			}
			if ref != "" && decision.ApprovalRef != ref {
				t.Fatalf("%s: a repeat opened %s beside %s", step, decision.ApprovalRef, ref)
			}
			ref = decision.ApprovalRef
		}
		if n := pending(); n != 1 {
			t.Fatalf("%s: %d pending approvals, want exactly 1", step, n)
		}
		return ref
	}
	approve := func(ref string) {
		t.Helper()
		if code, body := h.decide(t, approver, ref, "approve"); code != http.StatusOK {
			t.Fatalf("approve %s = %d %s", ref, code, body)
		}
	}
	resumes := func(step string, gate *sessionLaunchGate) {
		t.Helper()
		if decision, err := gate.Authorize(ctx, tenant, intent); err != nil || !decision.Allowed {
			t.Fatalf("%s = %+v (%v), want allowed on the unspent grant, not refused on a spent one", step, decision, err)
		}
	}
	seen := map[string]bool{}
	fresh := func(step, ref string) {
		t.Helper()
		if seen[ref] {
			t.Fatalf("%s waits on the spent approval %s", step, ref)
		}
		seen[ref] = true
	}

	g := restart()
	first := asksAHuman("first resume", g)
	fresh("first resume", first)
	approve(first)
	resumes("approved resume", g)

	// A tool-call retrying under its own consumer id still finds the grant it spent.
	ref, status, _, err := g.bridge.GateOnceForSession(ctx, tenant, sessionLaunchAction, sessionLaunchSubjectKind,
		launchSubjectRef(intent), sessionLaunchPlanHash(intent), "retry", intent.Actor, "")
	if err != nil || ref != first || status != nbApproved {
		t.Fatalf("a retry under its own consumer id found %q %q (%v), want the spent %s still approved", ref, status, err, first)
	}

	// The same engine process resumes it once more: the memo of the spent approval is dropped.
	second := asksAHuman("resume without a restart", g)
	fresh("resume without a restart", second)
	approve(second)
	resumes("resume after the second approval", g)

	g = restart()
	third := asksAHuman("resume after a restart", g)
	fresh("resume after a restart", third)
	approve(third)
	resumes("resume after the third approval", restart())

	// A launch that names a spent approval is still refused, and opens nothing.
	named := intent
	named.ApprovalRef = first
	if decision, err := restart().Authorize(ctx, tenant, named); err != nil || decision.Allowed ||
		!strings.Contains(decision.Reason, "already consumed") || pending() != 0 {
		t.Fatalf("naming the spent approval = %+v (%v), pending=%d, want a replay denial and no new request", decision, err, pending())
	}

	fresh("resume after another restart", asksAHuman("resume after another restart", restart()))
}
