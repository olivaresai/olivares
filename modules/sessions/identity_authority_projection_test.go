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
)

func TestSessionAuthorityProjectionCommitsExpiryBeforeClockRollback(t *testing.T) {
	m, _, tenant, clk := newRuntimeHarness(t, WithRunner(&fakeRunner{}), WithCredentialSource(staticCred()))
	ctx := context.Background()
	created, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "test:projection", ActorKind: model.ActorUser})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = m.stopRun(ctx, tenant, created.RunRef, "test", "user") })
	lr, _ := m.rt.getLive(tenant, created.RunRef)
	var workspace model.ID
	if err := m.Data.View(ctx, tenant, func(sc store.Scope) error { ws, err := sc.DefaultWorkspace(ctx); workspace = ws.ID; return err }); err != nil {
		t.Fatal(err)
	}
	base := clk.get()
	clk.set(base.Add(6 * time.Minute))
	if err := m.Data.Mutate(ctx, tenant, func(sc store.Scope) error {
		out, err := m.CommunicationSessionRecipientWithin(ctx, sc, workspace, lr.claim.SID)
		if err == nil && (!out.Found || out.Active || out.Fence != 0) {
			t.Fatalf("expired witness: %+v", out)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	clk.set(base)
	if _, live, err := m.ActiveClaim(ctx, tenant, lr.claim.SID); err != nil || live {
		t.Fatalf("observed expiry revived after clock rollback: %v, %v", live, err)
	}
}
