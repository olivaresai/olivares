// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance"
)

// These regression tests pin the four defects the adversarial review confirmed.

// FIX 1: a LOCAL authoritative write (an admin publish) must NOT renew the offline-trust
// clock of a tenant governed by an adopted signed bundle — otherwise the bounded party
// (a disconnected edge, or a delegate whose authority derives from the expiring policy)
// could hold the window open forever without any new signed material.
func TestLocalWriteDoesNotRenewSignedBundleClock(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t) // no deployment bound; the bound rides in with the bundle
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "ddil-fix1")
	member := h.createAgentIn(tenant, "pay-bot", model.ID(""))
	h.addAgentToGroup(tenant, member.ID, "payments-bots", model.ID(""))

	in := ddilAdoption(adoptedGrantSrc, baseTime, 24*time.Hour)
	if _, err := governance.AdoptBundlePolicy(ctx, h.st, tenant, in, baseTime); err != nil {
		t.Fatal(err)
	}
	if err := h.gov.ReloadActivePDP(ctx, tenant); err != nil {
		t.Fatal(err)
	}

	// 20h in (still fresh), an in-tenant admin publishes a local policy.
	h.clk.advance(20 * time.Hour)
	h.publishGrant(admin, tenant, adoptedGrantSrc)

	// The durable clock must still be the bundle's SIGNED baseTime, not the local write.
	fresh, _, err := governance.PolicyFreshness(ctx, h.st, tenant)
	if err != nil {
		t.Fatal(err)
	}
	if !fresh.RefreshedAt.Equal(baseTime) {
		t.Fatalf("local publish renewed the signed bundle clock: got %s want %s", fresh.RefreshedAt, baseTime)
	}

	// Past the 24h bound from baseTime the grant must expire despite the local write.
	h.clk.advance(5 * time.Hour) // baseTime+25h
	viewer := auth.ScopedPrincipal("cred-v", "v", tenant, auth.RoleViewer)
	if sd := h.scoped(tenant, viewer, "agent:write", member.ID); sd.Effect != auth.EffectAbstain {
		t.Fatalf("bundle grant must expire; a local write must not renew it, got %v (%s)", sd.Effect, sd.Reason)
	}
}

// FIX 3: a re-attestation (identical policy, freshly signed later) must advance the
// freshness clock WITHOUT appending a duplicate revision row, so a stable-policy site
// stays non-expired across a gap by carrying re-signed bundles.
func TestReattestationRenewsClockWithoutNewRevision(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "ddil-reattest")
	member := h.createAgentIn(tenant, "pay-bot", model.ID(""))
	h.addAgentToGroup(tenant, member.ID, "payments-bots", model.ID(""))

	in1 := ddilAdoption(adoptedGrantSrc, baseTime, 24*time.Hour)
	if r1, err := governance.AdoptBundlePolicy(ctx, h.st, tenant, in1, baseTime); err != nil || !r1.Adopted {
		t.Fatalf("initial adopt: report=%+v err=%v", r1, err)
	}

	reCreated := baseTime.Add(20 * time.Hour)
	in2 := ddilAdoption(adoptedGrantSrc, reCreated, 24*time.Hour) // same snapshot ⇒ same revision
	r2, err := governance.AdoptBundlePolicy(ctx, h.st, tenant, in2, reCreated)
	if err != nil {
		t.Fatal(err)
	}
	if !r2.Adopted || !strings.Contains(r2.Reason, "re-attest") {
		t.Fatalf("want a re-attestation, got %+v", r2)
	}
	if r2.SurfaceRevision != 0 {
		t.Fatalf("re-attest must not append a revision row, got surface_revision=%d", r2.SurfaceRevision)
	}
	if rows := ddilRevisionRows(t, h.st, tenant); len(rows) != 1 {
		t.Fatalf("re-attest duplicated the cedar-ddil revision row: %d rows", len(rows))
	}
	fresh, _, _ := governance.PolicyFreshness(ctx, h.st, tenant)
	if !fresh.RefreshedAt.Equal(reCreated) || !fresh.AdoptedCreatedAt.Equal(reCreated) {
		t.Fatalf("re-attest did not advance the clock: %+v", fresh)
	}
	if !auditHasAction(t, h.st, tenant, "governance.ddil.policy_reattest") {
		t.Fatal("re-attest was not audited")
	}

	// The renewed window holds: 20h after the re-attestation (< 24h) the grant still grants.
	if err := h.gov.ReloadActivePDP(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	h.clk.advance(40 * time.Hour) // baseTime+40h == reCreated+20h
	viewer := auth.ScopedPrincipal("cred-v", "v", tenant, auth.RoleViewer)
	if sd := h.scoped(tenant, viewer, "agent:write", member.ID); sd.Effect != auth.EffectGrant {
		t.Fatalf("re-attested policy must still grant within the renewed window, got %v (%s)", sd.Effect, sd.Reason)
	}
}

// FIX 3 (negative): a strictly-older or equal signed created_at with DIFFERENT content is
// a replay/rollback and must be refused; the same bundle re-delivered is an idempotent no-op.
func TestReplayIsRefusedAndSameBundleIsNoop(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	tenant := h.createOrg(h.adminLogin(), "ddil-replay")

	newer := ddilAdoption(adoptedGrantSrc, baseTime.Add(time.Hour), 24*time.Hour)
	if _, err := governance.AdoptBundlePolicy(ctx, h.st, tenant, newer, baseTime.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// A different policy signed EARLIER is a rollback: refuse.
	older := ddilAdoption(adoptedGrantSrc+"\n", baseTime, 24*time.Hour)
	if _, err := governance.AdoptBundlePolicy(ctx, h.st, tenant, older, baseTime); err == nil {
		t.Fatal("rollback to an older-signed different policy must be refused")
	}
	if rows := ddilRevisionRows(t, h.st, tenant); len(rows) != 1 {
		t.Fatalf("refused rollback wrote a revision row: %d", len(rows))
	}
	// Re-delivering the exact same current bundle is a no-op, not an error.
	r, err := governance.AdoptBundlePolicy(ctx, h.st, tenant, newer, baseTime.Add(2*time.Hour))
	if err != nil || r.Adopted || !strings.Contains(r.Reason, "already adopted") {
		t.Fatalf("same-bundle re-delivery must be a no-op, got report=%+v err=%v", r, err)
	}
}

// FIX 4/5: a tenant whose policy predates this feature has NO freshness row. Boot must
// backfill the anchor once (only under a deployment bound), so a later reboot RESTORES it
// instead of re-stamping — closing the residual reboot-evasion for the whole pre estate.
func TestBootBackfillClosesRebootEvasionForPreExistingTenant(t *testing.T) {
	ctx := context.Background()
	h := newHarnessWith(t, harnessOpts{offlineStaleness: 72 * time.Hour})
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "ddil-backfill")
	member := h.createAgentIn(tenant, "pay-bot", model.ID(""))
	h.addAgentToGroup(tenant, member.ID, "payments-bots", model.ID(""))

	// Establish an active local policy, then delete its freshness row to model a tenant
	// whose policy was published before this feature existed.
	h.publishGrant(admin, tenant, adoptedGrantSrc)
	deleteFreshnessRow(t, h.st, tenant)
	if _, found, _ := governance.PolicyFreshness(ctx, h.st, tenant); found {
		t.Fatal("precondition: the freshness row should be absent")
	}

	// First boot backfills from the store transaction clock, not the module's
	// controllable process clock. Capture that durable anchor for the reboot check.
	if err := h.gov.ReloadActivePDP(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	fresh, found, err := governance.PolicyFreshness(ctx, h.st, tenant)
	if err != nil || !found || fresh.RefreshedAt.IsZero() {
		t.Fatalf("boot did not backfill the anchor: found=%t rec=%+v err=%v", found, fresh, err)
	}
	backfilledAt := fresh.RefreshedAt
	if backfilledAt.Equal(baseTime) {
		t.Fatal("boot backfill borrowed the process test clock instead of the DB transaction clock")
	}
	if !auditHasAction(t, h.st, tenant, "governance.policy_freshness_backfill") {
		t.Fatal("boot freshness backfill was not audited in the ledger")
	}

	// A reboot 73h later (past the 72h deployment bound) must RESTORE the backfilled
	// anchor, not re-stamp now — so the grant expires. A fresh module models the restart.
	h.clk.advance(backfilledAt.Add(73 * time.Hour).Sub(h.clk.Now().Time()))
	rebooted := governance.New(governance.WithClock(h.clk), governance.WithOfflinePolicyStaleness(72*time.Hour))
	rebooted.UseData(api.NewModuleData(h.st))
	if err := rebooted.ReloadActivePDP(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	after, found, err := governance.PolicyFreshness(ctx, h.st, tenant)
	if err != nil || !found || !after.RefreshedAt.Equal(backfilledAt) {
		t.Fatalf("reboot re-stamped the DB freshness anchor: found=%t record=%+v err=%v want=%s", found, after, err, backfilledAt)
	}
	res := auth.ResourceFor("agent:write")
	res.ID = member.ID.String()
	viewer := auth.ScopedPrincipal("cred-v", "v", tenant, auth.RoleViewer)
	sd, err := rebooted.ScopedGrants().Scoped(ctx, auth.Request{
		Principal: viewer, Permission: "agent:write", Tenant: tenant, Resource: res,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sd.Effect != auth.EffectAbstain {
		t.Fatalf("reboot must not reset the clock for a backfilled pre-existing tenant, got %v (%s)", sd.Effect, sd.Reason)
	}
}

// deleteFreshnessRow removes a tenant's durable policy-freshness record (test-only, to
// model a pre-feature tenant). It addresses the Ext kind by its stable string name.
func deleteFreshnessRow(t *testing.T, st store.Store, tenant model.TenantID) {
	t.Helper()
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(model.Kind("governance.policy_freshness"))
		if err != nil {
			return err
		}
		recs, _, err := repo.List(context.Background(), model.Query{Limit: 10})
		if err != nil {
			return err
		}
		for _, rec := range recs {
			if err := repo.Delete(context.Background(), model.ID(rec.String(model.ColID))); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("delete freshness row: %v", err)
	}
}
