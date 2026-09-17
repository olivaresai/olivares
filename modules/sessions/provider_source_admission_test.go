// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package sessions

import (
	"context"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

func TestProfileAdmission_ReplayKeepsBinding(t *testing.T) {
	for _, be := range profileBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			ctx := context.Background()
			m, st, _ := openProfiledRuntime(t, be)
			tenant := ensureTenant(t, st, "review-replay-"+be.name)
			cfg, home, _, _ := twoHomes(t)
			p := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "claude", ConfigHome: cfg, UserHome: home})
			src := model.NewID()
			resolver := &fakeSourceResolver{sources: map[model.ID]SourceRevision{src: {ID: src, Version: 1, Name: "review", Tenant: tenant.String(), Kind: "claude", EnvironmentRef: testEnvRef}}}
			binding := bindDedicated(t, m, tenant, resolver, src, 1, p)
			envelope := admittedStamped(t, m, tenant, regFor(src, 1), sessEdge("review-replayed", "file", "/fixture/replay", sdkmodel.ModeRead, "Read", baseTime))
			envelope.ID = "review-same-envelope"
			if err := m.onEvent(ctx, envelope); err != nil {
				t.Fatal(err)
			}
			before := liveRowsFor(t, st, tenant, "review-replayed")
			if len(before) != 1 || before[0].String(colLiveBindingRef) != binding.Ref {
				t.Fatal("positive binding attribution missing")
			}
			if _, err := m.RevokeBinding(ctx, tenant, binding.Ref); err != nil {
				t.Fatal(err)
			}
			// Identical queued/durable envelope: no new source frame, clock or SourceDef.
			if err := m.onEvent(ctx, envelope); err != nil {
				t.Fatal(err)
			}
			after := liveRowsFor(t, st, tenant, "review-replayed")
			t.Logf("same immutable envelope replayed after binding revocation: live_rows_before=%d after=%d", len(before), len(after))
			if len(after) != 1 {
				t.Error("replay changed observation scope by consulting current binding state")
			}
			fresh := admittedStamped(t, m, tenant, regFor(src, 1), sessEdge("review-replayed", "file", "/fixture/new", sdkmodel.ModeRead, "Read", baseTime.Add(time.Second)))
			if fresh.SourceRegistration.BindingRef != "" {
				t.Fatal("new admission after revoke retained binding")
			}
			if err := m.onEvent(ctx, fresh); err != nil {
				t.Fatal(err)
			}
			rows := liveRowsFor(t, st, tenant, "review-replayed")
			if len(rows) != 2 {
				t.Fatalf("new post-revoke frame rows=%d", len(rows))
			}
			for _, corrupt := range []func(*event.SourceRegistration){
				func(r *event.SourceRegistration) { r.SourceID = model.NewID().String() },
				func(r *event.SourceRegistration) { r.SourceRevision++ },
				func(r *event.SourceRegistration) { r.EnvironmentRef = "other-environment" },
			} {
				altered := envelope
				altered.SourceRegistration = envelope.SourceRegistration.Clone()
				corrupt(altered.SourceRegistration)
				var scope liveScope
				if err := st.View(ctx, tenant, func(sc store.Scope) error {
					var err error
					scope, err = m.scopeForRegistration(ctx, sc, altered.SourceRegistration)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				if scope.profileID != "" || scope.bindingRef != "" {
					t.Fatal("foreign registration reused a binding decision")
				}
			}
			staleInput := *envelope.SourceRegistration
			restamped, err := m.AdmitSourceRegistration(ctx, tenant.String(), staleInput)
			if err != nil || restamped.BindingRef != "" {
				t.Fatal("a prior observation decision bypassed fresh admission")
			}

		})
	}
}

func TestProfileAuthority_AliasRollbackAndCurrentClaimBothEngines(t *testing.T) {
	for _, be := range profileBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			ctx := context.Background()
			fr := &fakeRunner{}
			m, st, _ := openProfiledRuntime(t, be, WithRunner(fr), WithCredentialSource(staticCred()))
			tenant := ensureTenant(t, st, "review-alias-"+be.name)
			cfg, home, _, _ := twoHomes(t)
			p := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "claude", ConfigHome: cfg, UserHome: home})
			launch := func() *liveRun {
				t.Helper()
				dto, err := m.createRun(ctx, tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:u1", ActorKind: model.ActorUser, ProviderProfileRef: p.Ref})
				if err != nil {
					t.Fatal(err)
				}
				lr, _ := m.rt.getLive(tenant, dto.RunRef)
				return lr
			}
			lr := launch()
			m.onStdout(ctx, lr, []byte(`{"type":"system","subtype":"init","session_id":"review-first"}`), m.now())
			_, found, err := m.LookupScopedAlias(ctx, tenant, p.Ref, "claude", "review-first")
			if err != nil || !found {
				t.Fatal("positive alias missing")
			}
			// The binder INSERTS a new alias before discovering that the run already
			// captured another ID. The outer transaction must roll that insert back.
			err = m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
				return m.bindManagedProviderAliasWithin(ctx, sc, managedAliasInput{profileID: p.Ref, provider: "claude", externalID: "review-must-rollback", sid: lr.claim.SID, runRef: lr.runRef, launchID: lr.launchID, claimFence: lr.claim.Fence})
			})
			if err == nil || !isRunConflict(err) {
				t.Fatalf("expected captured-ID conflict, got %v", err)
			}
			_, found, err = m.LookupScopedAlias(ctx, tenant, p.Ref, "claude", "review-must-rollback")
			if err != nil || found {
				t.Fatalf("aborted alias insert survived: found=%t err=%v", found, err)
			}
			rec := runRecord(t, st, tenant, lr.runRef)
			if rec.String(colClaudeSessionID) != "review-first" {
				t.Fatal("rollback changed run id")
			}
			t.Log("PASS actual post-INSERT alias conflict rolls back and preserves confirmed run")
			stale := launch()
			if err := m.Release(ctx, tenant, stale.claim.SID, stale.claim.Holder, stale.claim.Fence); err != nil {
				t.Fatal(err)
			}
			successor, err := m.Claim(ctx, tenant, stale.claim.SID, "review-new-holder", time.Minute)
			if err != nil || successor.Fence <= stale.claim.Fence {
				t.Fatalf("takeover failed: %v", err)
			}
			m.onStdout(ctx, stale, []byte(`{"type":"system","subtype":"init","session_id":"review-claim-fenced"}`), m.now())
			alias, found, err := m.LookupScopedAlias(ctx, tenant, p.Ref, "claude", "review-claim-fenced")
			if err != nil {
				t.Fatal(err)
			}
			stale.mu.Lock()
			captured := stale.sessionIDCaptured
			stale.mu.Unlock()
			t.Logf("current Claim fence=%d alias_found=%t alias_fence=%d memory_captured=%t", successor.Fence, found, alias.ClaimFence, captured)
			if found || captured {
				t.Error("fenced-out Claim confirmed managed authority")
			}
		})
	}
}
