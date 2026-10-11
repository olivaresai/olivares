// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

func submitRuntimeWork(t *testing.T, f runtimeWorkAPIFixture) CommandResult {
	t.Helper()
	ctx := t.Context()
	snapshot, err := f.m.Get(ctx, f.tenant, f.principal, f.itemID)
	if err != nil || len(snapshot.Acceptance) != 1 {
		t.Fatalf("read assigned work: %+v, %v", snapshot, err)
	}
	var criterion struct {
		ID model.ID `json:"id"`
	}
	if err := json.Unmarshal(snapshot.Acceptance[0], &criterion); err != nil {
		t.Fatal(err)
	}
	evaluated, err := f.m.Apply(ctx, f.tenant, f.principal, WorkCommand{
		Command: "acceptance.evaluate", WorkItemID: f.itemID, CriterionID: criterion.ID,
		ExpectedVersion: snapshot.Item.Version, Fence: f.fence,
		HolderSID: f.principal.SessionID, HolderRunRef: f.runRef,
		Acceptance: []AcceptanceInput{{State: "passed", EvidenceRef: "test:released-runtime-control",
			EvidenceHash: hexHash(hashBytes([]byte("released-runtime-control")))}},
		IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPost,
	})
	if err != nil {
		t.Fatalf("evaluate assigned work: %v", err)
	}
	submitted, err := f.m.Apply(ctx, f.tenant, f.principal, WorkCommand{
		Command: "item.submit", WorkItemID: f.itemID, Fence: f.fence,
		HolderSID: f.principal.SessionID, HolderRunRef: f.runRef,
		ExpectedVersion: evaluated.Version, IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPost,
	})
	if err != nil {
		t.Fatalf("submit assigned work: %v", err)
	}
	lease, err := f.m.GetLease(ctx, f.tenant, f.principal, f.itemID)
	if err != nil || lease.State != workLeaseReleased || lease.Live {
		t.Fatalf("submitted lease = %+v, %v", lease, err)
	}
	return submitted
}

func TestRuntimeWorkSubmittedSessionAcceptsOwnerInputAndStop(t *testing.T) {
	t.Run("submitted", func(t *testing.T) { runtimeWorkEndedOwnerControls(t, false) })
	t.Run("completed", func(t *testing.T) { runtimeWorkEndedOwnerControls(t, true) })
}

func runtimeWorkEndedOwnerControls(t *testing.T, complete bool) {
	t.Helper()
	f := newRuntimeWorkAPIFixture(t)
	proc := f.runner.lastProc()
	t.Cleanup(func() {
		if _, live := f.m.rt.getLive(f.tenant, f.runRef); live {
			finishWorkRuntimeRun(t, f.m, f.tenant, f.runRef, proc)
		}
	})
	submitted := submitRuntimeWork(t, f)
	if complete {
		if _, err := f.m.Apply(t.Context(), f.tenant, f.principal, WorkCommand{
			Command: "item.complete", WorkItemID: f.itemID, ExpectedVersion: submitted.Version,
			IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPost,
		}); err != nil {
			t.Fatalf("complete submitted work: %v", err)
		}
	}
	path := "/v1/m/sessions/runs/" + f.runRef
	input := f.h.doJSON(http.MethodPost, path+"/input", f.admin, map[string]any{
		"line": `{"type":"user","message":{"role":"user","content":"next task"}}`,
	}, tenantHdr(f.tenant))
	if input.code != http.StatusAccepted || proc.sentCount() != 1 {
		t.Errorf("owner input after submit = %d %s, writes=%d; want 202 and one write", input.code, input.raw, proc.sentCount())
	}
	stopped := f.h.do(http.MethodPost, path+"/stop", f.admin, tenantHdr(f.tenant))
	if stopped.code != http.StatusOK || stopped.body["state"] != stateStopped {
		t.Errorf("owner Stop after submit = %d %s; want 200 stopped", stopped.code, stopped.raw)
	}
}

func TestRuntimeWorkEndedFenceCannotStopReacquiredSession(t *testing.T) {
	f := newRuntimeWorkAPIFixture(t)
	proc := f.runner.lastProc()
	t.Cleanup(func() {
		if _, live := f.m.rt.getLive(f.tenant, f.runRef); live {
			finishWorkRuntimeRun(t, f.m, f.tenant, f.runRef, proc)
		}
	})
	snapshot, err := f.m.Get(t.Context(), f.tenant, f.principal, f.itemID)
	if err != nil {
		t.Fatal(err)
	}
	released, err := f.m.Apply(t.Context(), f.tenant, f.principal, WorkCommand{
		Command: "lease.release", WorkItemID: f.itemID, ExpectedVersion: snapshot.Item.Version,
		HolderSID: f.principal.SessionID, HolderRunRef: f.runRef, Fence: f.fence,
		Reason:         "finished generation before another assignment",
		IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPost,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.Apply(t.Context(), f.tenant, f.principal, WorkCommand{
		Command: "lease.acquire", WorkItemID: f.itemID, ExpectedVersion: released.Version, Unblock: true,
		HolderSID: f.principal.SessionID, HolderRunRef: f.runRef,
		IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPost,
	}); err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	lease, err := f.m.GetLease(t.Context(), f.tenant, f.principal, f.itemID)
	if err != nil || !lease.Live || lease.Fence <= f.fence {
		t.Fatalf("successor lease: %+v, %v", lease, err)
	}
	path := "/v1/m/sessions/runs/" + f.runRef
	for _, fence := range []*int64{nil, &f.fence} {
		inputBody := map[string]any{"line": "must not reach successor"}
		stopBody := map[string]any{}
		if fence != nil {
			inputBody["work_lease_fence"], stopBody["work_lease_fence"] = *fence, *fence
		}
		input := f.h.doJSON(http.MethodPost, path+"/input", f.admin, inputBody, tenantHdr(f.tenant))
		stop := f.h.doJSON(http.MethodPost, path+"/stop", f.admin, stopBody, tenantHdr(f.tenant))
		if input.code != http.StatusConflict || stop.code != http.StatusConflict || proc.sentCount() != 0 {
			t.Fatalf("unfenced/old fence crossed successor: input=%d %s stop=%d %s writes=%d", input.code, input.raw, stop.code, stop.raw, proc.sentCount())
		}
		if fence != nil && (workAPIErrorCode(input) != "stale_fence" || workAPIErrorCode(stop) != "stale_fence") {
			t.Fatalf("explicit old fence lost refusal: input=%s stop=%s", input.raw, stop.raw)
		}
	}
	if _, live := f.m.rt.getLive(f.tenant, f.runRef); !live {
		t.Fatal("stale control stopped the successor")
	}
	input := f.h.doJSON(http.MethodPost, path+"/input", f.admin, map[string]any{"line": "new exact fence", "work_lease_fence": lease.Fence}, tenantHdr(f.tenant))
	if input.code != http.StatusAccepted || proc.sentCount() != 1 {
		t.Fatalf("new fence input=%d %s, writes=%d", input.code, input.raw, proc.sentCount())
	}
	stop := f.h.doJSON(http.MethodPost, path+"/stop", f.admin, map[string]any{"work_lease_fence": lease.Fence}, tenantHdr(f.tenant))
	if stop.code != http.StatusOK || stop.body["state"] != stateStopped {
		t.Fatalf("new fence Stop=%d %s", stop.code, stop.raw)
	}
}

func TestRuntimeWorkReacquireBetweenSnapshotAndAdmissionRefusesOrdinaryControl(t *testing.T) {
	for _, action := range []string{"input", "stop"} {
		t.Run(action, func(t *testing.T) {
			fx := newRuntimeWorkControlFixture(t)
			fx.releaseWorkLease(t)
			unblocked, err := fx.m.Apply(t.Context(), fx.tenant, fx.principal, WorkCommand{
				Command: "item.unblock", WorkItemID: fx.itemID, ExpectedVersion: fx.itemVer,
				IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPost,
			})
			if err != nil {
				t.Fatal(err)
			}
			var acquireErr error
			race := &afterNthViewData{inner: fx.m.Data, after: 1, hook: func() {
				_, acquireErr = fx.m.Apply(t.Context(), fx.tenant, fx.principal, WorkCommand{
					Command: "lease.acquire", WorkItemID: fx.itemID, ExpectedVersion: unblocked.Version,
					HolderSID: fx.claim.SID, HolderRunRef: fx.runRef,
					IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPost,
				})
			}}
			fx.m.UseData(race)
			if action == "input" {
				err = fx.m.sendInput(t.Context(), fx.tenant, fx.runRef, []byte("stale snapshot"))
			} else {
				_, err = fx.m.stopRun(t.Context(), fx.tenant, fx.runRef, fx.principal.Actor, model.ActorUser)
			}
			if !race.fired() || acquireErr != nil {
				t.Fatalf("re-acquire race fired=%v err=%v", race.fired(), acquireErr)
			}
			if !isRunConflict(err) || fx.proc.sentCount() != 0 || fx.proc.stopCount() != 0 {
				t.Fatalf("ordinary %s admitted stale snapshot: err=%v writes=%d stops=%d", action, err, fx.proc.sentCount(), fx.proc.stopCount())
			}
		})
	}
}

func TestRuntimeWorkEndedLeaseDoesNotBypassSessionClaimSuccessor(t *testing.T) {
	fx := newRuntimeWorkControlFixture(t)
	fx.releaseWorkLease(t)
	if err := fx.m.Release(t.Context(), fx.tenant, fx.claim.SID, fx.claim.Holder, fx.claim.Fence); err != nil {
		t.Fatal(err)
	}
	successor, err := fx.m.Claim(t.Context(), fx.tenant, fx.claim.SID, "successor-after-work", time.Minute)
	if err != nil || successor.Fence <= fx.claim.Fence {
		t.Fatalf("successor Claim=%+v err=%v", successor, err)
	}
	if err := fx.m.sendInput(t.Context(), fx.tenant, fx.runRef, []byte("must not cross successor")); err == nil {
		t.Error("ordinary input bypassed moved session Claim")
	}
	if _, err := fx.m.stopRun(t.Context(), fx.tenant, fx.runRef, fx.principal.Actor, model.ActorUser); err == nil {
		t.Error("ordinary Stop bypassed moved session Claim")
	}
	if fx.proc.sentCount() != 0 || fx.proc.stopCount() != 0 {
		t.Fatalf("stale Claim effects: writes=%d stops=%d", fx.proc.sentCount(), fx.proc.stopCount())
	}
}
