// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

type runtimeWorkAPIFixture struct {
	h         *harness
	m         *Module
	runner    *fakeRunner
	admin     string
	tenant    model.TenantID
	runRef    string
	itemID    model.ID
	fence     int64
	principal WorkPrincipal
}

func newRuntimeWorkAPIFixture(t *testing.T) runtimeWorkAPIFixture {
	t.Helper()
	runner := &fakeRunner{}
	m := New(
		WithRunner(runner),
		WithCredentialSource(staticCred()),
		WithWorkIdentityResolver(allowWorkIdentity{}),
		WithWorkContentGuard(allowWorkContent{}),
	)
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "runtime-work-api")
	createdRun := h.doJSON(http.MethodPost, "/v1/m/sessions/runs", admin, map[string]any{
		"transport": "stream-json", "permission_mode": "default", "isolation": "native",
		"name": "fenced-runtime-api",
	}, tenantHdr(tenant))
	runRef, _ := createdRun.body["run_ref"].(string)
	if createdRun.code != http.StatusCreated || runRef == "" {
		t.Fatalf("create runtime run = %d %s", createdRun.code, createdRun.raw)
	}
	live, ok := m.rt.getLive(tenant, runRef)
	if !ok || live.claim.SID == "" {
		t.Fatalf("run %s has no admission claim", runRef)
	}
	if !validCanonicalSID(live.claim.SID) {
		t.Fatalf("runtime claim SID %q is not canonical", live.claim.SID)
	}
	principal := WorkPrincipal{
		ActorKind: "session", ActorRef: live.claim.SID, Actor: "session:" + live.claim.SID,
		SessionID: live.claim.SID,
	}
	workspace := workAPIWorkspace(t, h, tenant)
	createdItem, err := m.Apply(context.Background(), tenant, principal, WorkCommand{
		Command: "item.create", WorkspaceID: workspace, WorkKind: "implementation",
		Title: "Fenced runtime API", BriefMD: "Exercise fenced input and stop over HTTP.",
		ContextRefs: []ContextRef{}, Priority: "p1", OwnerKind: "session", OwnerRef: live.claim.SID,
		ProvenanceKind: "human", ProvenanceRef: "test:runtime-work-api",
		Acceptance: []AcceptanceInput{{
			Key: "runtime-control", Ordinal: 0, Statement: "Fenced runtime control is enforced.", Required: true,
		}},
		IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPost,
	})
	if err != nil {
		t.Fatalf("create runtime WorkItem: %v", err)
	}
	ready, err := m.Apply(context.Background(), tenant, principal, WorkCommand{
		Command: "item.ready", WorkItemID: createdItem.ResultID,
		ExpectedVersion: createdItem.Version, IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPost,
	})
	if err != nil {
		t.Fatalf("ready runtime WorkItem: %v", err)
	}
	acquired, err := m.Apply(context.Background(), tenant, principal, WorkCommand{
		Command: "lease.acquire", WorkItemID: createdItem.ResultID,
		HolderSID: live.claim.SID, HolderRunRef: runRef, TTLSeconds: 300,
		ExpectedVersion: ready.Version, IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPost,
	})
	if err != nil {
		t.Fatalf("acquire runtime WorkLease: %v", err)
	}
	lease, err := m.GetLease(context.Background(), tenant, principal, createdItem.ResultID)
	if err != nil || lease.Fence < 1 || lease.State != workLeaseActive {
		t.Fatalf("active runtime WorkLease = %#v, %v (result=%#v)", lease, err, acquired)
	}
	return runtimeWorkAPIFixture{
		h: h, m: m, runner: runner, admin: admin, tenant: tenant,
		runRef: runRef, itemID: createdItem.ResultID, fence: lease.Fence, principal: principal,
	}
}

func runtimeWorkRawPost(f runtimeWorkAPIFixture, path, raw string) resp {
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(raw))
	req.RemoteAddr = "10.0.0.1:1234"
	req.Header.Set("Authorization", "Bearer "+f.admin)
	req.Header.Set("X-Olivares-Tenant", f.tenant.String())
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.h.srv.Handler().ServeHTTP(rec, req)
	out := resp{code: rec.Code, raw: rec.Body.String(), header: rec.Header().Clone()}
	_ = json.Unmarshal(rec.Body.Bytes(), &out.body)
	return out
}

func TestRuntimeWorkAPIInputSelectsFenceAndRejectsFallback(t *testing.T) {
	f := newRuntimeWorkAPIFixture(t)
	path := "/v1/m/sessions/runs/" + f.runRef + "/input"
	proc := f.runner.lastProc()

	for name, response := range map[string]resp{
		"unknown field":  runtimeWorkRawPost(f, path, `{"line":"unknown","extra":true}`),
		"second payload": runtimeWorkRawPost(f, path, `{"line":"first"}{"line":"second"}`),
		"zero fence":     runtimeWorkRawPost(f, path, `{"line":"zero","work_lease_fence":0}`),
		"negative fence": runtimeWorkRawPost(f, path, `{"line":"negative","work_lease_fence":-1}`),
	} {
		if response.code != http.StatusBadRequest {
			t.Errorf("%s = %d %s", name, response.code, response.raw)
		}
	}
	if got := proc.sentCount(); got != 0 {
		t.Fatalf("invalid input reached process %d time(s)", got)
	}

	omitted := f.h.doJSON(http.MethodPost, path, f.admin, map[string]any{
		"line": "legacy-fallback",
	}, tenantHdr(f.tenant))
	if omitted.code != http.StatusConflict || proc.sentCount() != 0 {
		t.Fatalf("omitted fence = %d %s, sends=%d", omitted.code, omitted.raw, proc.sentCount())
	}
	stale := f.h.doJSON(http.MethodPost, path, f.admin, map[string]any{
		"line": "stale", "work_lease_fence": f.fence + 1,
	}, tenantHdr(f.tenant))
	if stale.code != http.StatusConflict || workAPIErrorCode(stale) != "stale_fence" || proc.sentCount() != 0 {
		t.Fatalf("stale fence = %d %s, sends=%d", stale.code, stale.raw, proc.sentCount())
	}
	accepted := f.h.doJSON(http.MethodPost, path, f.admin, map[string]any{
		"line": "fenced-input", "work_lease_fence": f.fence,
	}, tenantHdr(f.tenant))
	if accepted.code != http.StatusAccepted || accepted.body["accepted"] != true || proc.sentCount() != 1 {
		t.Fatalf("fenced input = %d %s, sends=%d", accepted.code, accepted.raw, proc.sentCount())
	}
	finishWorkRuntimeRun(t, f.m, f.tenant, f.runRef, proc)
}

func TestRuntimeWorkAPIStopIsFencedAndEmptyBodyStaysLegacy(t *testing.T) {
	t.Run("legacy empty body", func(t *testing.T) {
		runner := &fakeRunner{}
		m := New(WithRunner(runner), WithCredentialSource(staticCred()))
		h := newHarness(t, m)
		admin := h.adminLogin()
		tenant := h.createOrg(admin, "runtime-stop-legacy")
		created := h.doJSON(http.MethodPost, "/v1/m/sessions/runs", admin, map[string]any{
			"transport": "stream-json", "permission_mode": "default", "isolation": "native",
		}, tenantHdr(tenant))
		ref, _ := created.body["run_ref"].(string)
		stopped := h.do(http.MethodPost, "/v1/m/sessions/runs/"+ref+"/stop", admin, tenantHdr(tenant))
		if stopped.code != http.StatusOK || stopped.body["state"] != stateStopped {
			t.Fatalf("legacy empty stop = %d %s", stopped.code, stopped.raw)
		}
	})

	t.Run("work bound", func(t *testing.T) {
		f := newRuntimeWorkAPIFixture(t)
		path := "/v1/m/sessions/runs/" + f.runRef + "/stop"
		proc := f.runner.lastProc()
		for name, response := range map[string]resp{
			"unknown field":  runtimeWorkRawPost(f, path, `{"work_lease_fence":1,"extra":true}`),
			"second payload": runtimeWorkRawPost(f, path, `{"work_lease_fence":1}{"reason":"second"}`),
			"orphan reason":  runtimeWorkRawPost(f, path, `{"reason":"not fenced"}`),
			"zero fence":     runtimeWorkRawPost(f, path, `{"work_lease_fence":0}`),
			"negative fence": runtimeWorkRawPost(f, path, `{"work_lease_fence":-1}`),
		} {
			if response.code != http.StatusBadRequest {
				t.Errorf("%s = %d %s", name, response.code, response.raw)
			}
		}
		omitted := f.h.do(http.MethodPost, path, f.admin, tenantHdr(f.tenant))
		if omitted.code != http.StatusConflict {
			t.Fatalf("work-bound empty stop = %d %s", omitted.code, omitted.raw)
		}
		proc.mu.Lock()
		stoppedEarly := proc.done
		proc.mu.Unlock()
		if stoppedEarly {
			t.Fatal("unfenced or invalid stop reached the work-bound process")
		}

		const reason = "operator requested fenced shutdown"
		stopped := f.h.doJSON(http.MethodPost, path, f.admin, map[string]any{
			"work_lease_fence": f.fence, "reason": reason,
		}, tenantHdr(f.tenant))
		if stopped.code != http.StatusOK || stopped.body["state"] != stateStopped {
			t.Fatalf("fenced stop = %d %s", stopped.code, stopped.raw)
		}
		lease, err := f.m.GetLease(context.Background(), f.tenant, f.principal, f.itemID)
		if err != nil || lease.State != workLeaseRevoked || lease.Fence != f.fence+1 || lease.EndReason != reason {
			t.Fatalf("stopped WorkLease = %#v, %v", lease, err)
		}
	})
}
