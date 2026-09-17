// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// The work-fenced TEXT route, over HTTP, on a real driver-backed child.
//
// The in-process port is covered next door; this is the half a client actually
// speaks. It matters on its own because the defect the review found was in the
// HTTP contract as much as in the port: the handler refused `text` whenever
// `work_lease_fence` was present, so a work-bound driver run had no door at all.
func TestRuntimeWorkAPIFencedTextReachesTheDriver(t *testing.T) {
	m := New(
		WithRunner(NewProcRunner()),
		WithProviderDriver(NewCodexDriver()),
		WithDriverProgram(providerDriverCodex, os.Args[0]),
		WithProductVersion("test"),
		WithStopWaitDelay(2*time.Second),
		WithDriverTimeouts(20*time.Second, 2*time.Second),
		WithWorkIdentityResolver(allowWorkIdentity{}),
		WithWorkContentGuard(allowWorkContent{}),
	)
	m.UseExecutionEnvironmentRef(testEnvRef)
	m.EnableProfiledLaunches()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "runtime-work-text")

	config, home := t.TempDir(), t.TempDir()
	prof := mustCreateProfile(t, m, tenant, CreateProfileInput{
		Driver: providerDriverCodex, ConfigHome: config, UserHome: home,
		DisplayName: "codex-work-text", AuthSource: AuthSourceAccountHome,
	})
	record := setCodexFixture(t, prof, codexFixture{ThreadID: "thread-http-work", Account: "apikey"})

	created := h.doJSON(http.MethodPost, "/v1/m/sessions/runs", admin, map[string]any{
		"transport": "stream-json", "permission_mode": "default", "isolation": "native",
		"provider_profile_ref": prof.Ref,
	}, tenantHdr(tenant))
	runRef, _ := created.body["run_ref"].(string)
	if created.code != http.StatusCreated || runRef == "" {
		t.Fatalf("create profiled run = %d %s", created.code, created.raw)
	}
	live, ok := m.rt.getLive(tenant, runRef)
	if !ok || live.claim.SID == "" {
		t.Fatalf("run %s has no admission claim", runRef)
	}
	t.Cleanup(func() {
		_ = live.proc.Stop(context.Background())
		select {
		case <-live.finalizedCh:
		case <-time.After(5 * time.Second):
		}
	})

	fence := bindRunToFreshWorkLease(t, m, h, tenant, runRef, live.claim.SID)
	path := "/v1/m/sessions/runs/" + runRef + "/input"
	before := countMethod(readFixtureRecord(t, record).Methods, codexMethodTurnStart)

	// A raw line, fenced or not, is still refused on a driver-backed run.
	rawLine := h.doJSON(http.MethodPost, path, admin, map[string]any{
		"line": "raw", "work_lease_fence": fence,
	}, tenantHdr(tenant))
	if rawLine.code == http.StatusAccepted {
		t.Fatalf("a fenced RAW line was accepted on a driver-backed run: %d %s", rawLine.code, rawLine.raw)
	}
	// Unfenced text is refused: the durable work stamp selects the fenced plane.
	unfenced := h.doJSON(http.MethodPost, path, admin, map[string]any{
		"text": "unfenced",
	}, tenantHdr(tenant))
	if unfenced.code != http.StatusConflict {
		t.Fatalf("unfenced text on a work-bound run = %d %s", unfenced.code, unfenced.raw)
	}
	// A stale fence refuses before the effect.
	stale := h.doJSON(http.MethodPost, path, admin, map[string]any{
		"text": "stale", "work_lease_fence": fence + 1,
	}, tenantHdr(tenant))
	if stale.code != http.StatusConflict || workAPIErrorCode(stale) != "stale_fence" {
		t.Fatalf("stale fenced text = %d %s", stale.code, stale.raw)
	}
	if got := countMethod(readFixtureRecord(t, record).Methods, codexMethodTurnStart); got != before {
		t.Fatalf("a stale fenced text crossed the process boundary: turn/start %d -> %d", before, got)
	}
	// And the exact fence carries the turn to the child.
	accepted := h.doJSON(http.MethodPost, path, admin, map[string]any{
		"text": "hello", "work_lease_fence": fence,
	}, tenantHdr(tenant))
	if accepted.code != http.StatusAccepted || accepted.body["accepted"] != true {
		t.Fatalf("fenced text = %d %s", accepted.code, accepted.raw)
	}
	waitFor(t, "the fenced text started a turn on the child", func() bool {
		return countMethod(readFixtureRecord(t, record).Methods, codexMethodTurnStart) == before+1
	})
}

// bindRunToFreshWorkLease creates a WorkItem, readies it and acquires a lease
// held by this run, which is what makes the run work-bound for control purposes.
// It mirrors newRuntimeWorkAPIFixture; it is separate only because that fixture
// launches its own unprofiled run.
func bindRunToFreshWorkLease(
	t *testing.T,
	m *Module,
	h *harness,
	tenant model.TenantID,
	runRef, sid string,
) int64 {
	t.Helper()
	ctx := context.Background()
	principal := WorkPrincipal{
		ActorKind: "session", ActorRef: sid, Actor: "session:" + sid, SessionID: sid,
	}
	workspace := workAPIWorkspace(t, h, tenant)
	created, err := m.Apply(ctx, tenant, principal, WorkCommand{
		Command: "item.create", WorkspaceID: workspace, WorkKind: "implementation",
		Title: "Fenced driver text", BriefMD: "Exercise the fenced text route on a driver run.",
		ContextRefs: []ContextRef{}, Priority: "p1", OwnerKind: "session", OwnerRef: sid,
		ProvenanceKind: "human", ProvenanceRef: "test:runtime-work-text",
		Acceptance: []AcceptanceInput{{
			Key: "fenced-text", Ordinal: 0, Statement: "Fenced text reaches the driver.", Required: true,
		}},
		IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPost,
	})
	if err != nil {
		t.Fatalf("create WorkItem: %v", err)
	}
	ready, err := m.Apply(ctx, tenant, principal, WorkCommand{
		Command: "item.ready", WorkItemID: created.ResultID,
		ExpectedVersion: created.Version, IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPost,
	})
	if err != nil {
		t.Fatalf("ready WorkItem: %v", err)
	}
	if _, err := m.Apply(ctx, tenant, principal, WorkCommand{
		Command: "lease.acquire", WorkItemID: created.ResultID,
		HolderSID: sid, HolderRunRef: runRef, TTLSeconds: 300,
		ExpectedVersion: ready.Version, IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPost,
	}); err != nil {
		t.Fatalf("acquire WorkLease: %v", err)
	}
	lease, err := m.GetLease(ctx, tenant, principal, created.ResultID)
	if err != nil || lease.Fence < 1 || lease.State != workLeaseActive {
		t.Fatalf("active WorkLease = %#v, %v", lease, err)
	}
	return lease.Fence
}
