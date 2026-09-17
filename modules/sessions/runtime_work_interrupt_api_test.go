// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"net/http"
	"testing"
)

// The TYPED HTTP contract of the fenced interrupt: what the endpoint refuses
// before it reaches the module at all, and what it must never do instead.
//
// The body is OPTIONAL, which is the awkward half. An endpoint that has always
// accepted an empty body and now also accepts a fenced one has two ways to be
// wrong that a happy-path test cannot see: it can start refusing the empty body
// that non-work callers still send, and it can accept a malformed fenced body by
// treating a decode failure as "no fence given" and quietly running the LEGACY
// control on a work-bound run. Both are asserted here.
func TestRuntimeWorkAPIInterruptBodyIsValidatedAndNeverFallsBackToStop(t *testing.T) {
	f := newRuntimeWorkAPIFixture(t)
	path := "/v1/m/sessions/runs/" + f.runRef + "/interrupt"
	proc := f.runner.lastProc()

	for name, response := range map[string]resp{
		"unknown field":  runtimeWorkRawPost(f, path, `{"work_lease_fence":1,"extra":true}`),
		"second payload": runtimeWorkRawPost(f, path, `{"work_lease_fence":1}{"work_lease_fence":2}`),
		"zero fence":     runtimeWorkRawPost(f, path, `{"work_lease_fence":0}`),
		"negative fence": runtimeWorkRawPost(f, path, `{"work_lease_fence":-1}`),
		"not an object":  runtimeWorkRawPost(f, path, `"fence"`),
		// The field that belongs to the TERMINAL control. Accepting it here would be
		// the first half of an interrupt that can be talked into stopping.
		"stop's reason": runtimeWorkRawPost(f, path, `{"work_lease_fence":1,"reason":"stop it"}`),
	} {
		if response.code != http.StatusBadRequest {
			t.Errorf("%s = %d %s", name, response.code, response.raw)
		}
	}

	// An omitted fence on a work-bound run selects the legacy plane, which refuses:
	// a malformed body must land HERE and not on a silent legacy interrupt.
	omitted := f.h.do(http.MethodPost, path, f.admin, tenantHdr(f.tenant))
	if omitted.code != http.StatusConflict {
		t.Fatalf("work-bound interrupt with no fence = %d %s", omitted.code, omitted.raw)
	}
	stale := f.h.doJSON(http.MethodPost, path, f.admin, map[string]any{
		"work_lease_fence": f.fence + 1,
	}, tenantHdr(f.tenant))
	if stale.code != http.StatusConflict || workAPIErrorCode(stale) != "stale_fence" {
		t.Fatalf("stale fenced interrupt = %d %s", stale.code, stale.raw)
	}

	// The exact fence on a run with NO protocol driver: a refusal that names the
	// missing capability, never a stop standing in for it.
	exact := f.h.doJSON(http.MethodPost, path, f.admin, map[string]any{
		"work_lease_fence": f.fence,
	}, tenantHdr(f.tenant))
	if exact.code != http.StatusConflict {
		t.Fatalf("fenced interrupt of a driverless run = %d %s", exact.code, exact.raw)
	}
	proc.mu.Lock()
	stopped := proc.done
	proc.mu.Unlock()
	if stopped {
		t.Fatal("a refused interrupt ended the process; /interrupt never falls back to /stop")
	}
	state := f.h.do(http.MethodGet, "/v1/m/sessions/runs/"+f.runRef, f.admin, tenantHdr(f.tenant))
	if state.code != http.StatusOK || state.body["state"] != stateRunning {
		t.Fatalf("the run must still be running after a refused interrupt: %d %s", state.code, state.raw)
	}
	finishWorkRuntimeRun(t, f.m, f.tenant, f.runRef, proc)
}
