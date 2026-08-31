// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/governance"
)

// Follow-up (ADR-0024 Q1): the offline-trust bound is ASYMMETRIC. Offline, past
// policy_max_staleness, a positive scoped GRANT expires deny-closed (it can no longer
// authorize what RBAC would not), but a FORBID stays enforced (a stale restriction can
// only restrict, never escalate). With no bound configured — the connected-node default
// — nothing expires. These three tests pin exactly that, on the live scoped-grant engine.

const staleGrantSrc = `permit(principal in Role::"viewer", action == Action::"agent:write", resource) when { resource in AgentGroup::"payments-bots" };`

// A positive grant authorizes while the policy is fresh, keeps authorizing right up to
// the bound, expires to deny-closed one tick past it, and is restored by a re-publish
// (the refresh a reconnection / bundle adoption performs).
func TestScopedGrantExpiresPastOfflineStaleness(t *testing.T) {
	h := newHarnessWith(t, harnessOpts{offlineStaleness: 72 * time.Hour})
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "edge")

	member := h.createAgentIn(tenant, "pay-bot", model.ID(""))
	h.addAgentToGroup(tenant, member.ID, "payments-bots", model.ID(""))
	h.publishGrant(admin, tenant, staleGrantSrc)
	alignHarnessClockToDurableFreshness(t, h, tenant)

	viewer := auth.ScopedPrincipal("cred-v", "v", tenant, auth.RoleViewer)

	// Fresh: the positive grant authorizes.
	if sd := h.scoped(tenant, viewer, "agent:write", member.ID); sd.Effect != auth.EffectGrant {
		t.Fatalf("fresh policy must GRANT, got %v (%s)", sd.Effect, sd.Reason)
	}
	// Exactly at the bound is not yet "past" (strict >): the grant still authorizes.
	h.clk.advance(72 * time.Hour)
	if sd := h.scoped(tenant, viewer, "agent:write", member.ID); sd.Effect != auth.EffectGrant {
		t.Fatalf("at exactly the bound the grant must still GRANT, got %v (%s)", sd.Effect, sd.Reason)
	}
	// One tick past the bound: the positive grant expires deny-closed. It abstains (not a
	// hard deny) so the request falls back to RBAC — the viewer, who has no agent:write by
	// role, is now denied, but the node is NOT halted.
	h.clk.advance(time.Second)
	if sd := h.scoped(tenant, viewer, "agent:write", member.ID); sd.Effect != auth.EffectAbstain {
		t.Fatalf("past the staleness bound the grant must EXPIRE to abstain, got %v (%s)", sd.Effect, sd.Reason)
	}
	// A re-publish (an admin authoring, or a DDIL bundle adoption) re-establishes the
	// policy as current and restarts the window: the grant authorizes again.
	h.publishGrant(admin, tenant, staleGrantSrc)
	alignHarnessClockToDurableFreshness(t, h, tenant)
	if sd := h.scoped(tenant, viewer, "agent:write", member.ID); sd.Effect != auth.EffectGrant {
		t.Fatalf("a refresh must restore the grant, got %v (%s)", sd.Effect, sd.Reason)
	}
}

// A forbid rule is never expired by staleness: a stale restriction still restricts, well
// past the bound. This is the asymmetry — deny eternal, allow expires.
func TestScopedForbidSurvivesOfflineStaleness(t *testing.T) {
	h := newHarnessWith(t, harnessOpts{offlineStaleness: 72 * time.Hour})
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "edge2")

	quarantine := h.createWorkspace(tenant, "quarantine")
	inQuarantine := h.createAgentIn(tenant, "q-bot", quarantine)
	h.publishGrant(admin, tenant, `forbid(principal, action, resource) when { resource in Workspace::"quarantine" };`)
	alignHarnessClockToDurableFreshness(t, h, tenant)

	// A tenant admin (agent:write by RBAC) is forbidden in quarantine — fresh...
	adminP := auth.ScopedPrincipal("cred-a", "a", tenant, auth.RoleAdmin)
	if sd := h.scoped(tenant, adminP, "agent:write", inQuarantine.ID); sd.Effect != auth.EffectForbid {
		t.Fatalf("fresh forbid must FORBID, got %v (%s)", sd.Effect, sd.Reason)
	}
	// ...and still forbidden long after the staleness bound would have expired a grant.
	h.clk.advance(100 * time.Hour)
	if sd := h.scoped(tenant, adminP, "agent:write", inQuarantine.ID); sd.Effect != auth.EffectForbid {
		t.Fatalf("a stale restriction must STILL FORBID (deny eternal), got %v (%s)", sd.Effect, sd.Reason)
	}
}

// Local Cedar mutations take their freshness from the store's transaction clock, not
// harness/model time. Move the deterministic evaluator clock to the durable anchor
// before exercising an elapsed window; otherwise a fake time from a different epoch
// would test neither the fresh nor stale boundary.
func alignHarnessClockToDurableFreshness(t *testing.T, h *harness, tenant model.TenantID) {
	t.Helper()
	fresh, found, err := governance.PolicyFreshness(context.Background(), h.st, tenant)
	if err != nil || !found || fresh.RefreshedAt.IsZero() {
		t.Fatalf("local publish did not persist a durable DB freshness anchor: found=%t record=%+v err=%v", found, fresh, err)
	}
	h.clk.advance(fresh.RefreshedAt.Sub(h.clk.Now().Time()))
}

// With no bound configured (the connected-node default), a positive grant never expires —
// zero behavior change for a normal deployment even after an arbitrarily long time.
func TestScopedGrantNeverExpiresWithoutBound(t *testing.T) {
	h := newHarness(t) // no offlineStaleness ⇒ no bound
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "central")

	member := h.createAgentIn(tenant, "bot", model.ID(""))
	h.addAgentToGroup(tenant, member.ID, "payments-bots", model.ID(""))
	h.publishGrant(admin, tenant, staleGrantSrc)

	viewer := auth.ScopedPrincipal("cred-v", "v", tenant, auth.RoleViewer)
	h.clk.advance(1000 * time.Hour)
	if sd := h.scoped(tenant, viewer, "agent:write", member.ID); sd.Effect != auth.EffectGrant {
		t.Fatalf("with no bound a grant must NEVER expire, got %v (%s)", sd.Effect, sd.Reason)
	}
}

func TestScopedGrantTenantBoundOverridesDeploymentDefault(t *testing.T) {
	h := newHarnessWith(t, harnessOpts{offlineStaleness: 72 * time.Hour})
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "tenant-bound")
	member := h.createAgentIn(tenant, "pay-bot", model.ID(""))
	h.addAgentToGroup(tenant, member.ID, "payments-bots", model.ID(""))
	adoptStalenessPolicy(t, h, tenant, staleGrantSrc, baseTime, 24*time.Hour)

	viewer := auth.ScopedPrincipal("cred-v", "v", tenant, auth.RoleViewer)
	h.clk.advance(24*time.Hour + time.Second)
	if sd := h.scoped(tenant, viewer, "agent:write", member.ID); sd.Effect != auth.EffectAbstain {
		t.Fatalf("24h tenant override must win over 72h default, got %v (%s)", sd.Effect, sd.Reason)
	}
}

func TestScopedGrantTenantBoundWorksWithoutDeploymentDefault(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "tenant-bound-only")
	member := h.createAgentIn(tenant, "pay-bot", model.ID(""))
	h.addAgentToGroup(tenant, member.ID, "payments-bots", model.ID(""))
	adoptStalenessPolicy(t, h, tenant, staleGrantSrc, baseTime, 24*time.Hour)

	viewer := auth.ScopedPrincipal("cred-v", "v", tenant, auth.RoleViewer)
	h.clk.advance(24*time.Hour + time.Second)
	if sd := h.scoped(tenant, viewer, "agent:write", member.ID); sd.Effect != auth.EffectAbstain {
		t.Fatalf("tenant override must expire without a deployment default, got %v (%s)", sd.Effect, sd.Reason)
	}
}

func TestScopedGrantClearedTenantBoundFallsBackToDeploymentDefault(t *testing.T) {
	h := newHarnessWith(t, harnessOpts{offlineStaleness: 72 * time.Hour})
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "tenant-bound-cleared")
	member := h.createAgentIn(tenant, "pay-bot", model.ID(""))
	h.addAgentToGroup(tenant, member.ID, "payments-bots", model.ID(""))
	adoptStalenessPolicy(t, h, tenant, staleGrantSrc, baseTime, 24*time.Hour)
	h.clk.advance(30 * time.Hour)

	// A later signed adoption carrying zero explicitly removes the tenant override.
	adoptStalenessPolicy(t, h, tenant, staleGrantSrc+"\n", baseTime.Add(time.Hour), 0)
	viewer := auth.ScopedPrincipal("cred-v", "v", tenant, auth.RoleViewer)
	if sd := h.scoped(tenant, viewer, "agent:write", member.ID); sd.Effect != auth.EffectGrant {
		t.Fatalf("cleared override must fall back to the 72h default, got %v (%s)", sd.Effect, sd.Reason)
	}
}

func adoptStalenessPolicy(t *testing.T, h *harness, tenant model.TenantID, source string, createdAt time.Time, bound time.Duration) {
	t.Helper()
	snapshot := []byte(source)
	sum := sha256.Sum256(snapshot)
	revision := "sha256:" + hex.EncodeToString(sum[:])
	if _, err := governance.AdoptBundlePolicy(context.Background(), h.st, tenant, governance.PolicyAdoption{
		Snapshot: snapshot, Revision: revision, BundleCreatedAt: createdAt,
		MaxStaleness: bound, Actor: "ddil-test",
	}, h.clk.Now().Time()); err != nil {
		t.Fatalf("adopt staleness policy: %v", err)
	}
	if err := h.gov.ReloadActivePDP(context.Background(), tenant); err != nil {
		t.Fatalf("reload adopted staleness policy: %v", err)
	}
}
