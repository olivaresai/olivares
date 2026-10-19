// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// liveAndTimelineRefs lists the session_ref of every live row and every timeline
// row, sorted, so a test can state exactly which sessions the list still holds.
func liveAndTimelineRefs(t *testing.T, st store.Store, tenant model.TenantID) (live, timeline []string) {
	t.Helper()
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		for _, k := range []struct {
			kind model.Kind
			col  string
			out  *[]string
		}{{liveKind, colSessionRef, &live}, {timelineKind, colTLSessionRef, &timeline}} {
			repo, err := sc.Ext(k.kind)
			if err != nil {
				return err
			}
			recs, _, err := repo.List(context.Background(), model.Query{Limit: 500})
			if err != nil {
				return err
			}
			for _, r := range recs {
				*k.out = append(*k.out, r.String(k.col))
			}
			slices.Sort(*k.out)
		}
		return nil
	}); err != nil {
		t.Fatalf("list live and timeline rows: %v", err)
	}
	return live, timeline
}

// seedCleanedRun stores a cleaned run of a workspace, ready to be deleted.
func seedCleanedRun(t *testing.T, st store.Store, tenant model.TenantID, runRef string, workspace model.ID, claimSID string) {
	t.Helper()
	ctx := context.Background()
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		runs, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		rec := model.Record{colRunRef: runRef, colState: stateCleaned,
			colTransport: string(TransportStreamJSON), colPermissionMode: "default",
			colIsolation: string(IsolationNative), colLastEventSeq: int64(0),
			colRunAuthzWorkspaceID: workspace.String()}
		if claimSID != "" {
			rec[colRunClaimSID] = claimSID
		}
		_, err = runs.Create(ctx, rec)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// A member confined to one workspace removes a session of their workspace through the
// route, with the live rows the plane keeps for it. Live and timeline rows carry no
// workspace lineage, so a confined scope refuses them: the removal answered 403 and
// rolled the run's delete back.
func TestDeleteRun_WorkspaceConfinedAdminRemovesTheirSessionAndItsLiveRows(t *testing.T) {
	ctx := context.Background()
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "delete-confined")
	workspaceA := workAPICreateWorkspace(t, h, tenant, "delete-a")
	confined := workAPIRoleTokenIn(t, h, admin, tenant, auth.RoleAdmin, "confined@delete.io", workspaceA)
	const runRef, sid = "run-cleaned", "osn_01a14509-fee7-7000-8000-000000000001"
	seedCleanedRun(t, h.st, tenant, runRef, workspaceA, "")
	now := model.NewTimestamp(time.Now()).String()
	if err := h.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		live, err := sc.Ext(liveKind)
		if err != nil {
			return err
		}
		_, err = live.Create(ctx, model.Record{colSessionRef: "ses_confined", colObservationScope: scopeManagedPrefix + sid,
			colLiveCanonicalSID: sid, colLiveRunRef: runRef, colInputTokens: int64(0), colOutputTokens: int64(0),
			colCostMicroUSD: int64(0), colEventCount: int64(0), colToolCalls: int64(0),
			colFirstEventAt: now, colLastEventAt: now})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	if r := h.do("DELETE", "/v1/m/sessions/runs/"+runRef, confined, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("confined delete of their own cleaned session = %d %s, want 200", r.code, r.raw)
	}
	if live, _ := liveAndTimelineRefs(t, h.st, tenant); len(live) != 0 {
		t.Errorf("live rows after the confined delete = %v, want none", live)
	}
}

// A run that never captured the tool's session id has no managed row, and its hooks
// and cost sink still fold rows under the references the plane minted for it.
func TestDeleteRun_RemovesLegacyRowsOfARunWithNoManagedRow(t *testing.T) {
	ctx := context.Background()
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "delete-unbound")
	workspace := workAPICreateWorkspace(t, h, tenant, "delete-unbound")
	const runRef, claim = "run-unbound", "osn_01a14509-fee7-7000-8000-000000000002"
	seedCleanedRun(t, h.st, tenant, runRef, workspace, claim)
	at := time.Now()
	for _, ref := range []string{runRef, claim, "ses_observed_elsewhere"} {
		if err := m.onEdge(ctx, tenant.String(), sessEdge(ref, "file", "/a", sdkmodel.ModeRead, "Read", at)); err != nil {
			t.Fatal(err)
		}
	}

	if err := m.deleteRun(ctx, tenant, runRef); err != nil {
		t.Fatal(err)
	}
	live, timeline := liveAndTimelineRefs(t, h.st, tenant)
	want := []string{"ses_observed_elsewhere"}
	if !slices.Equal(live, want) || !slices.Equal(timeline, want) {
		t.Errorf("after the delete live = %v, timeline = %v, want only %v", live, timeline, want)
	}
}

// Removing a session removes what the live list shows for it. An OpenCode launch
// writes the plane's own live row (keyed by the tool's ses_ id) beside the run, and a
// Claude launch also folds rows under the run reference and the canonical id. The
// run's name lives on the run, so a row left behind after `session rm` painted as
// "Untitled session ses_…" and no route removed it (#2328).
func TestDeleteRun_TakesTheLiveRowsOfItsSessionAndNoOtherSession(t *testing.T) {
	ctx := context.Background()
	m, st, tenant, profA := openCodeHarness(t, AuthSourceAccountHome)
	setOpenCodeFixture(t, profA, openCodeFixture{SessionID: "ses_removed"})
	profB := newOpenCodeProfile(t, m, tenant, "opencode-b", AuthSourceAccountHome)
	setOpenCodeFixture(t, profB, openCodeFixture{SessionID: "ses_kept"})

	removed, err := openCodeLaunch(t, m, tenant, profA)
	if err != nil {
		t.Fatal(err)
	}
	kept, err := openCodeLaunch(t, m, tenant, profB)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.stopRun(ctx, tenant, kept.RunRef, "user:u1", model.ActorUser) }()

	// What the plane's hooks and cost sink write for a launched session, on the
	// legacy channel under the references the plane minted for the run.
	managed := liveRowsFor(t, st, tenant, "ses_removed")
	if len(managed) != 1 {
		t.Fatalf("the launch wrote %d live rows for its ses_ id, want its one managed row", len(managed))
	}
	canonical := managed[0].String(colLiveCanonicalSID)
	at := time.Now()
	// Timeline events on the managed rows: more than one page for the removed session.
	for ref, events := range map[string]int{"ses_removed": dropBatch + 5, "ses_kept": 1} {
		row := liveRowsFor(t, st, tenant, ref)[0]
		scope := liveScope{scope: row.String(colObservationScope)}
		if err := m.Data.Mutate(ctx, tenant, func(sc store.Scope) error {
			for range events {
				if err := m.appendTimelineScoped(ctx, sc, ref, row, scope, at, tlTool, "Read", "/a", "read", "test", "a tool call"); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.onEdge(ctx, tenant.String(), sessEdge(removed.RunRef, "file", "/a", sdkmodel.ModeRead, "Read", at)); err != nil {
		t.Fatal(err)
	}
	if err := m.onCost(ctx, tenant.String(), sdkmodel.CostSample{SessionRef: canonical, InputTokens: 4, OutputTokens: 1, OccurredAt: at}); err != nil {
		t.Fatal(err)
	}
	// A session somebody else observes, which this run never owned, and a legacy row
	// that announces the SAME tool session id: two homes may both announce one, so the
	// removed run's own id never keys a legacy delete.
	for _, ref := range []string{"ses_observed_elsewhere", "ses_removed"} {
		if err := m.onEdge(ctx, tenant.String(), sessEdge(ref, "file", "/b", sdkmodel.ModeRead, "Read", at)); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := m.stopRun(ctx, tenant, removed.RunRef, "user:u1", model.ActorUser); err != nil {
		t.Fatal(err)
	}
	if _, err := m.cleanupRun(ctx, tenant, removed.RunRef, "user:u1", model.ActorUser); err != nil {
		t.Fatal(err)
	}
	if err := m.deleteRun(ctx, tenant, removed.RunRef); err != nil {
		t.Fatal(err)
	}

	live, timeline := liveAndTimelineRefs(t, st, tenant)
	// "ses_removed" stays once: the OTHER home's legacy row and its one timeline event.
	if want := []string{"ses_kept", "ses_observed_elsewhere", "ses_removed"}; !slices.Equal(live, want) {
		t.Errorf("live rows after the delete = %v, want %v (the removed session's own rows go, its neighbours stay)", live, want)
	}
	if want := []string{"ses_kept", "ses_observed_elsewhere", "ses_removed"}; !slices.Equal(timeline, want) {
		t.Errorf("timeline rows after the delete = %v, want %v", timeline, want)
	}
}
