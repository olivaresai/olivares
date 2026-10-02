// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestRunPendingApprovalIsVisibleOnlyForItsCurrentWait(t *testing.T) {
	fr := &fakeRunner{initSID: dupID}
	clk := &testClock{now: baseTime}
	h, admin, tenant, profile, _ := profiledHTTP(t, fr, clk)
	run, _ := launchProfiledHTTP(t, h, admin, tenant, profile)
	a := auth.NewAuthenticator(h.st, nil)
	human, err := a.Authenticate(t.Context(), admin)
	if err != nil {
		t.Fatal(err)
	}
	issuer := auth.NewSessionCredentials(a, func(ctx context.Context, scope auth.SessionScope) error {
		return h.m.Authority(ctx, scope.TenantID, scope.SessionRef, scope.Holder, scope.Fence)
	})
	principalForCurrentRun := func() auth.Principal {
		lr, ok := h.m.rt.getLive(tenant, run)
		if !ok {
			t.Fatal("launched run is not supervised")
		}
		var workspace model.ID
		if err := h.st.View(t.Context(), tenant, func(sc store.Scope) error {
			ws, err := sc.DefaultWorkspace(t.Context())
			workspace = ws.ID
			return err
		}); err != nil {
			t.Fatal(err)
		}
		token, err := issuer.Mint(t.Context(), human, auth.SessionScope{TenantID: tenant, WorkspaceID: workspace, FolderRef: run, SessionRef: lr.claim.SID, RunRef: run, Holder: lr.claim.Holder, Fence: lr.claim.Fence})
		if err != nil {
			t.Fatal(err)
		}
		p, err := issuer.Authenticate(t.Context(), token)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	assertWaiting := func(want string) {
		t.Helper()
		detail := h.do(http.MethodGet, "/v1/m/sessions/runs/"+run, admin, tenantHdr(tenant))
		if detail.code != http.StatusOK {
			t.Fatalf("detail=%d %s", detail.code, detail.raw)
		}
		rows := itemsOf(t, h.do(http.MethodGet, "/v1/m/sessions/runs", admin, tenantHdr(tenant)))
		if len(rows) != 1 {
			t.Fatalf("runs=%d, want=1", len(rows))
		}
		for _, dto := range []map[string]any{detail.body, rows[0]} {
			got, exists := dto["pending_approval_ref"]
			if want == "" {
				if exists {
					t.Fatalf("completed wait retained pending_approval_ref=%v", got)
				}
			} else if got != want {
				t.Fatalf("pending_approval_ref=%v, want=%q", got, want)
			}
			if dto["process_state"] != "running" {
				t.Fatalf("approval wait changed process state: %v", dto)
			}
		}
	}
	p := principalForCurrentRun()
	assertWaiting("")
	staleFence, wrongWorkspace, wrongSession := p, p, p
	staleFence.SessionFence++
	wrongWorkspace.SessionWorkspaceID = model.NewID()
	wrongSession.SessionIdentity = "another-session"
	for name, candidate := range map[string]auth.Principal{"human": human, "stale_fence": staleFence, "wrong_workspace": wrongWorkspace, "wrong_session": wrongSession} {
		if end, err := h.m.BeginApprovalWait(t.Context(), candidate, model.NewID().String(), clk.get().Add(time.Minute)); err == nil {
			if end != nil {
				end()
			}
			t.Fatalf("%s attached a wait without current session authority", name)
		}
	}
	assertWaiting("")
	ref := model.NewID().String()
	end, err := h.m.BeginApprovalWait(t.Context(), p, ref, clk.get().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	assertWaiting(ref)
	end() // A decision, cancellation or effective expiry ends the same wait.
	end() // Cleanup is idempotent.
	assertWaiting("")
	first, second := model.NewID().String(), model.NewID().String()
	endFirst, err := h.m.BeginApprovalWait(t.Context(), p, first, clk.get().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	endSecond, err := h.m.BeginApprovalWait(t.Context(), p, second, clk.get().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	assertWaiting(first)
	endFirst()
	assertWaiting(second)
	endSecond()
	assertWaiting("")
	expired := model.NewID().String()
	endExpired, err := h.m.BeginApprovalWait(t.Context(), p, expired, clk.get().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	clk.advance(2 * time.Second)
	assertWaiting("")
	endExpired()
	// Old cleanup may be delayed by cancellation; it cannot clear a resumed wait.
	endOld, err := h.m.BeginApprovalWait(t.Context(), p, model.NewID().String(), clk.get().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if got := h.doJSON(http.MethodPost, "/v1/m/sessions/runs/"+run+"/stop", admin, nil, tenantHdr(tenant)); got.code != http.StatusOK {
		t.Fatalf("stop=%d %s", got.code, got.raw)
	}
	if got := h.doJSON(http.MethodPost, "/v1/m/sessions/runs/"+run+"/resume", admin, nil, tenantHdr(tenant)); got.code != http.StatusOK {
		t.Fatalf("resume=%d %s", got.code, got.raw)
	}
	old := p
	p = principalForCurrentRun()
	if end, err := h.m.BeginApprovalWait(t.Context(), old, model.NewID().String(), clk.get().Add(time.Minute)); err == nil {
		if end != nil {
			end()
		}
		t.Fatal("retired session generation attached a wait to its successor")
	}
	nextRef := model.NewID().String()
	endNext, err := h.m.BeginApprovalWait(t.Context(), p, nextRef, clk.get().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	defer endNext()
	endOld()
	assertWaiting(nextRef)
}
