// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"net/http"
	"testing"
)

func TestRuntimeWorkSubmittedDriverAcceptsOwnerTextInterruptAndStop(t *testing.T) {
	m, h, admin, tenant, prof := codexHTTPHarness(t, "released-driver-control")
	record := setCodexFixture(t, prof, codexFixture{ThreadID: "released-driver-thread", Account: "apikey"})
	runRef, live := codexHTTPRun(t, m, h, admin, tenant, prof)
	fence := bindRunToFreshWorkLease(t, m, h, tenant, runRef, live.claim.SID)
	run, err := m.getRun(t.Context(), tenant, runRef)
	if err != nil {
		t.Fatal(err)
	}
	submitRuntimeWork(t, runtimeWorkAPIFixture{
		h: h, m: m, admin: admin, tenant: tenant, runRef: runRef,
		itemID: run.WorkItemID, fence: fence,
		principal: WorkPrincipal{ActorKind: "session", ActorRef: live.claim.SID,
			Actor: "session:" + live.claim.SID, SessionID: live.claim.SID},
	})
	path := "/v1/m/sessions/runs/" + runRef
	before := countMethod(readFixtureRecord(t, record).Methods, codexMethodTurnStart)
	input := h.doJSON(http.MethodPost, path+"/input", admin, map[string]any{"text": "next task after submit"}, tenantHdr(tenant))
	if input.code != http.StatusAccepted {
		t.Fatalf("ordinary driver text after submit=%d %s", input.code, input.raw)
	}
	waitFor(t, "a turn reaches the same owned driver after submit", func() bool {
		return countMethod(readFixtureRecord(t, record).Methods, codexMethodTurnStart) == before+1
	})
	interrupt := h.do(http.MethodPost, path+"/interrupt", admin, tenantHdr(tenant))
	if interrupt.code != http.StatusOK || interrupt.body["state"] != stateRunning {
		t.Fatalf("ordinary driver interrupt after submit=%d %s", interrupt.code, interrupt.raw)
	}
	stopped := h.do(http.MethodPost, path+"/stop", admin, tenantHdr(tenant))
	if stopped.code != http.StatusOK || stopped.body["state"] != stateStopped {
		t.Fatalf("ordinary driver Stop after submit=%d %s", stopped.code, stopped.raw)
	}
	if processRunning(live.proc.PID()) {
		t.Fatal("successful Stop left the owned driver running")
	}
	stoppedRun, err := m.getRun(t.Context(), tenant, runRef)
	if err != nil || stoppedRun.WorkItemID != run.WorkItemID || stoppedRun.WorkLeaseFence == nil || *stoppedRun.WorkLeaseFence != fence {
		t.Fatalf("Stop erased work history: %+v, %v", stoppedRun, err)
	}
}
