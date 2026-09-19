// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestProfileAuthority_RequiresProfileForNewAndUnprovenResume(t *testing.T) {
	fr := &fakeRunner{initSID: "legacy-without-home"}
	m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(fr), WithCredentialSource(staticCred()))
	m.UseExecutionEnvironmentRef(testEnvRef)
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "profile-required")
	body := map[string]any{"transport": "stream-json", "isolation": "native"}
	legacy := h.doJSON("POST", "/v1/m/sessions/runs", admin, body, tenantHdr(tenant))
	if legacy.code != http.StatusCreated {
		t.Fatalf("B1 fixture create=%d", legacy.code)
	}
	ref := legacy.body["run_ref"].(string)
	waitFor(t, "legacy init", func() bool {
		d, _ := m.getRun(context.Background(), model.TenantID(tenant), ref)
		return d.ClaudeSessionID != ""
	})
	m.EnableProfiledLaunches()
	before := countRows(t, m, model.TenantID(tenant), runKind)
	refused := h.doJSON("POST", "/v1/m/sessions/runs", admin, body, tenantHdr(tenant))
	if refused.code != http.StatusBadRequest || launchCount(fr) != 1 || countRows(t, m, model.TenantID(tenant), runKind) != before {
		t.Fatalf("unprofiled B2 launch status=%d effects=%d", refused.code, launchCount(fr))
	}
	if got := h.do("GET", "/v1/m/sessions/runs/"+ref, admin, tenantHdr(tenant)); got.code != http.StatusOK {
		t.Fatalf("legacy read=%d", got.code)
	}
	if got := h.doJSON("POST", "/v1/m/sessions/runs/"+ref+"/stop", admin, nil, tenantHdr(tenant)); got.code != http.StatusOK {
		t.Fatalf("legacy stop=%d", got.code)
	}
	got := h.doJSON("POST", "/v1/m/sessions/runs/"+ref+"/resume", admin, nil, tenantHdr(tenant))
	if got.code != http.StatusConflict || launchCount(fr) != 1 {
		t.Fatalf("unproven resume status=%d launches=%d", got.code, launchCount(fr))
	}
}

func TestProfileAuthority_ClaimTakeoverMustBlockInit(t *testing.T) {
	fr := &fakeRunner{}
	m, _, tenant, a, _ := profiledHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	ctx := context.Background()
	dto, err := m.createRun(ctx, tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:u1", ActorKind: model.ActorUser, ProviderProfileRef: a.Ref})
	if err != nil {
		t.Fatal(err)
	}
	lr, _ := m.rt.getLive(tenant, dto.RunRef)
	if err := m.Release(ctx, tenant, lr.claim.SID, lr.claim.Holder, lr.claim.Fence); err != nil {
		t.Fatal(err)
	}
	successor, err := m.Claim(ctx, tenant, lr.claim.SID, "review-new-holder", time.Minute)
	if err != nil || successor.Fence <= lr.claim.Fence {
		t.Fatalf("takeover failed: %v", err)
	}
	m.onStdout(ctx, lr, []byte(`{"type":"system","subtype":"init","session_id":"review-fenced-init"}`), m.now())
	alias, found, err := m.LookupScopedAlias(ctx, tenant, a.Ref, "claude", "review-fenced-init")
	if err != nil {
		t.Fatal(err)
	}
	lr.mu.Lock()
	captured := lr.sessionIDCaptured
	lr.mu.Unlock()
	t.Logf("run fence=%d current Claim fence=%d alias_created=%t captured=%t alias_fence=%d", lr.claim.Fence, successor.Fence, found, captured, alias.ClaimFence)
	if found || captured {
		t.Error("fenced-out Claim still created and confirmed managed alias")
	}
}

func TestProfileAuthority_StaleBridgeCannotPublish(t *testing.T) {
	fr := &fakeRunner{initSID: "review-stale-live"}
	m, st, tenant, a, _ := profiledHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	ctx := context.Background()
	dto, err := m.createRun(ctx, tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:u1", ActorKind: model.ActorUser, ProviderProfileRef: a.Ref})
	if err != nil {
		t.Fatal(err)
	}
	old, _ := m.rt.getLive(tenant, dto.RunRef)
	waitFor(t, "initial managed row", func() bool { d, _ := m.getRun(ctx, tenant, dto.RunRef); return d.LiveRef != "" })
	if _, err := m.stopRun(ctx, tenant, dto.RunRef, "user:u1", "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.resumeRun(ctx, tenant, dto.RunRef, "user:u1", "user", ""); err != nil {
		t.Fatal(err)
	}
	current, _ := m.rt.getLive(tenant, dto.RunRef)
	if current == old || current.launchID == old.launchID {
		t.Fatal("resume did not make new registered generation")
	}
	waitFor(t, "resumed init confirmed", func() bool { current.mu.Lock(); defer current.mu.Unlock(); return current.sessionIDCaptured })
	var before model.Record
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		var e error
		before, _, e = findManagedLive(ctx, sc, current.claim.SID)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	runBefore := runRecord(t, st, tenant, dto.RunRef)
	ch, unsubscribe := m.broker.subscribeTo(tenant, "", before.String(model.ColID))
	defer unsubscribe()
	m.onStdout(ctx, old, []byte(`{"type":"assistant"}`), m.now().Add(10*time.Minute))
	var after model.Record
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		var e error
		after, _, e = findManagedLive(ctx, sc, current.claim.SID)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	runAfter := runRecord(t, st, tenant, dto.RunRef)
	sse := false
	select {
	case <-ch:
		sse = true
	default:
	}
	changed := before.String(colLastEventAt) != after.String(colLastEventAt)
	t.Logf("old registered generation displaced: run_timestamp_changed=%t managed_timestamp_changed=%t SSE_published=%t", runBefore.String(colLastActivityAt) != runAfter.String(colLastActivityAt), changed, sse)
	if changed || sse {
		t.Error("stale bridge changed successor managed liveness/SSE despite run generation guard")
	}
}

func TestProfileAuthority_UnregisteredProcessAndLateExpiry(t *testing.T) {
	ctx := context.Background()
	clk := &testClock{now: baseTime}
	m, st, tenant, p, _ := profiledHarness(t, WithClock(clk), WithRunner(&fakeRunner{}), WithCredentialSource(staticCred()))
	dto, err := m.createRun(ctx, tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:u1", ActorKind: model.ActorUser, ProviderProfileRef: p.Ref})
	if err != nil {
		t.Fatal(err)
	}
	lr, _ := m.rt.getLive(tenant, dto.RunRef)
	clone := &liveRun{tenant: tenant, runRef: lr.runRef, launchID: lr.launchID, claim: lr.claim, profile: lr.profile, proc: lr.proc}
	m.captureProfiledSessionID(ctx, clone, "unregistered", m.now())
	if clone.sessionIDCaptured || countRows(t, m, tenant, providerAliasKind) != 0 {
		t.Fatal("unregistered copy confirmed an alias")
	}
	before := runRecord(t, st, tenant, lr.runRef)
	err = m.withRegisteredProfiledRun(lr, func() error {
		return m.mutateProfiledRun(ctx, lr, func(sc store.Scope, _ model.Record) error {
			if err := m.bindManagedProviderAliasWithin(ctx, sc, managedAliasInput{profileID: p.Ref, provider: p.Driver, externalID: "late-expiry", sid: lr.claim.SID, runRef: lr.runRef, launchID: lr.launchID, claimFence: lr.claim.Fence}); err != nil {
				return err
			}
			clk.set(lr.claim.ExpiresAt.Add(time.Second))
			return nil
		})
	})
	if err == nil || countRows(t, m, tenant, providerAliasKind) != 0 {
		t.Fatalf("late expiry left alias: %v", err)
	}
	after := runRecord(t, st, tenant, lr.runRef)
	if after.String(colClaudeSessionID) != before.String(colClaudeSessionID) {
		t.Fatal("late expiry left captured run")
	}
	clk.set(baseTime)
	m.captureProfiledSessionID(ctx, lr, "after-clock-rollback", m.now())
	if lr.sessionIDCaptured || countRows(t, m, tenant, providerAliasKind) != 0 {
		t.Fatal("clock rollback revived observed expired authority")
	}
}
