// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package accessmap

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// seedIdentityWithAgents creates one identity with the given external ref and
// one agent bound to it (Agent.IdentityID) per external id in agentExts.
func seedIdentityWithAgents(t *testing.T, st store.Store, tenant model.TenantID, ref string, agentExts ...string) {
	t.Helper()
	ctx := context.Background()
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		i, err := sc.Identities().Create(ctx, model.Identity{Name: ref, Kind: "db_role", ExternalID: ref})
		if err != nil {
			return err
		}
		for _, ext := range agentExts {
			if _, err := sc.Agents().Create(ctx, model.Agent{Name: ext, ExternalID: ext, IdentityID: i.ID, Status: model.StatusActive}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed identity %q: %v", ref, err)
	}
}

// TestDiff_ModeSubsumption_RWGrantCoversObservedRead is the mode-subsumption
// regression: a permitted READWRITE grant and an observed READ live on
// DIFFERENT natural keys (mode is part of the key), so a mode-EXACT match would
// report a phantom violation AND a phantom unused grant for a fully-permitted
// access. The grant's mode covers the observed mode, so both must drop.
func TestDiff_ModeSubsumption_RWGrantCoversObservedRead(t *testing.T) {
	st, tenant := newStore(t)
	m := New()
	m.UseData(api.NewModuleData(st))
	ctx := context.Background()

	mustIngest(t, m, tenant, obs("identity", "svc-rw", "postgres.table", "appdb.public.t", sdkmodel.ModeRead, sdkmodel.SignalPGAudit, sdkmodel.ConfidenceAttributed))
	mustIngest(t, m, tenant, obs("identity", "svc-rw", "postgres.table", "appdb.public.t", sdkmodel.ModeReadWrite, sdkmodel.SignalPolicy, sdkmodel.ConfidenceAttributed))

	// Precondition: the raw drift sees both halves (different natural keys).
	var rawCount int
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		raw, e := sc.AccessEdges().Drift(ctx, model.Query{})
		rawCount = len(raw)
		return e
	}); err != nil {
		t.Fatalf("raw drift: %v", err)
	}
	if rawCount != 2 {
		t.Fatalf("precondition failed: raw drift = %d rows, want 2 (observed read + permitted rw on different keys)", rawCount)
	}

	diff, err := m.Diff(ctx, tenant, "user:a", model.ActorUser, model.Query{})
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(diff.UnexpectedAccesses) != 0 || len(diff.UnusedGrants) != 0 || len(diff.InventoryGrants) != 0 {
		t.Errorf("a readwrite grant must cover an observed read on the same (principal, resource); got unexpected=%d unused=%d inventory=%d",
			len(diff.UnexpectedAccesses), len(diff.UnusedGrants), len(diff.InventoryGrants))
	}
}

// TestDiff_OneGrantCoversManyObserved is the 1:N consumption regression:
// ONE identity grant must reconcile EVERY observed edge it covers — two agents
// running as the same identity are both permitted, never a false violation on
// the second (the multi-agent case the reconcileDrift contract promises).
func TestDiff_OneGrantCoversManyObserved(t *testing.T) {
	st, tenant := newStore(t)
	m := New()
	m.UseData(api.NewModuleData(st))
	ctx := context.Background()

	seedIdentityWithAgents(t, st, tenant, "svc-shared", "agent-a", "agent-b")

	// Two agents observed on the same resource+mode, one grant on the identity.
	mustIngest(t, m, tenant, obs("agent", "agent-a", "postgres.table", "appdb.public.t", sdkmodel.ModeRead, sdkmodel.SignalPGAudit, sdkmodel.ConfidenceAttributed))
	mustIngest(t, m, tenant, obs("agent", "agent-b", "postgres.table", "appdb.public.t", sdkmodel.ModeRead, sdkmodel.SignalPGAudit, sdkmodel.ConfidenceAttributed))
	mustIngest(t, m, tenant, obs("identity", "svc-shared", "postgres.table", "appdb.public.t", sdkmodel.ModeRead, sdkmodel.SignalPolicy, sdkmodel.ConfidenceAttributed))

	diff, err := m.Diff(ctx, tenant, "user:a", model.ActorUser, model.Query{})
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(diff.UnexpectedAccesses) != 0 {
		t.Errorf("one grant must reconcile both agents' observed edges; got %d unexpected (the second agent must not be a false violation)", len(diff.UnexpectedAccesses))
	}
	if len(diff.UnusedGrants) != 0 {
		t.Errorf("the covering grant is used; got %d unused grants", len(diff.UnusedGrants))
	}
}

// TestDiff_UnknownModeGrantIsPendingNotFirm: a grant whose own mode is UNKNOWN
// cannot PROVE any observed mode, so the observed edge on the same (principal,
// resource) is flagged reconciliation_pending — never silently reconciled, never
// headlined as a firm violation (docs/SECURITY-HARDENING.md). The unproven grant itself stays an
// unused grant (honesty: its use was never demonstrated).
func TestDiff_UnknownModeGrantIsPendingNotFirm(t *testing.T) {
	st, tenant := newStore(t)
	m := New()
	m.UseData(api.NewModuleData(st))
	ctx := context.Background()

	mustIngest(t, m, tenant, obs("identity", "svc-unk", "postgres.table", "appdb.public.t", sdkmodel.ModeRead, sdkmodel.SignalPGAudit, sdkmodel.ConfidenceAttributed))
	mustIngest(t, m, tenant, obs("identity", "svc-unk", "postgres.table", "appdb.public.t", sdkmodel.ModeUnknown, sdkmodel.SignalPolicy, sdkmodel.ConfidenceAttributed))

	diff, err := m.Diff(ctx, tenant, "user:a", model.ActorUser, model.Query{})
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(diff.UnexpectedAccesses) != 1 {
		t.Fatalf("want 1 unexpected access (pending), got %d", len(diff.UnexpectedAccesses))
	}
	if pending, _ := diff.UnexpectedAccesses[0].Edge.Metadata["reconciliation_pending"].(bool); !pending {
		t.Error("an observed access under an unknown-mode grant must be flagged reconciliation_pending, not headlined as a firm violation")
	}
	if len(diff.UnusedGrants) != 1 {
		t.Errorf("the unknown-mode grant proved nothing and must stay an unused grant; got %d", len(diff.UnusedGrants))
	}
}

// TestDiff_InventoryKindGoesToInventoryGrants: a permitted grant on a resource
// kind with NO observed-side collector (permittedInventoryKinds — the
// identity-source inventories) is never evidence of over-provisioning, so it
// lands in InventoryGrants, not the headline UnusedGrants; a grant on a covered
// kind (postgres) stays a headline unused grant.
func TestDiff_InventoryKindGoesToInventoryGrants(t *testing.T) {
	st, tenant := newStore(t)
	m := New()
	m.UseData(api.NewModuleData(st))
	ctx := context.Background()

	mustIngest(t, m, tenant, obs("identity", "cn=ops,dc=corp", "ldap.directory", "dc=corp,dc=example", sdkmodel.ModeReadWrite, sdkmodel.SignalPolicy, sdkmodel.ConfidenceAttributed))
	mustIngest(t, m, tenant, obs("identity", "svc-etl", "postgres.table", "appdb.public.t", sdkmodel.ModeWrite, sdkmodel.SignalPolicy, sdkmodel.ConfidenceAttributed))

	diff, err := m.Diff(ctx, tenant, "user:a", model.ActorUser, model.Query{})
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(diff.InventoryGrants) != 1 {
		t.Fatalf("inventory grants = %d, want 1 (the ldap.directory grant)", len(diff.InventoryGrants))
	}
	if rk, _ := diff.InventoryGrants[0].Edge.Metadata["resource_kind"].(string); rk != "ldap.directory" {
		t.Errorf("inventory grant resource_kind = %q, want ldap.directory", rk)
	}
	if len(diff.UnusedGrants) != 1 {
		t.Fatalf("unused grants = %d, want 1 (the postgres grant — a covered kind stays headline)", len(diff.UnusedGrants))
	}
	if rk, _ := diff.UnusedGrants[0].Edge.Metadata["resource_kind"].(string); rk != "postgres.table" {
		t.Errorf("unused grant resource_kind = %q, want postgres.table", rk)
	}
}

// TestDrainDrift_CrossesStoreCap proves the drift window is DRAINED past the
// store's 1000-row cap: a flood of permitted-only grant edges occupying
// the first page must not hide later rows. 1050 grants plus one violation are
// all visible to the reconciliation, where the raw single-page Drift truncates.
func TestDrainDrift_CrossesStoreCap(t *testing.T) {
	st, tenant := newStore(t)
	ctx := context.Background()

	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		for i := 0; i < 1050; i++ {
			if _, err := sc.AccessEdges().Upsert(ctx, model.AccessEdge{
				OriginKind: originIdentity, OriginID: model.NewID(), ResourceID: model.NewID(),
				Mode: sdkmodel.ModeRead, SignalSource: sdkmodel.SignalPolicy,
				Confidence: sdkmodel.ConfidenceAttributed, Permitted: true,
			}); err != nil {
				return err
			}
		}
		_, err := sc.AccessEdges().Upsert(ctx, model.AccessEdge{
			OriginKind: originIdentity, OriginID: model.NewID(), ResourceID: model.NewID(),
			Mode: sdkmodel.ModeRead, SignalSource: sdkmodel.SignalPGAudit,
			Confidence: sdkmodel.ConfidenceAttributed, Observed: true,
		})
		return err
	}); err != nil {
		t.Fatalf("seed drift rows: %v", err)
	}

	var diff PrivilegeDiff
	var rawCount int
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		raw, e := sc.AccessEdges().Drift(ctx, model.Query{})
		if e != nil {
			return e
		}
		rawCount = len(raw)
		diff, e = ReconciledDrift(ctx, sc, model.Query{})
		return e
	}); err != nil {
		t.Fatalf("view: %v", err)
	}

	// Precondition: the raw store window IS capped — the bug being guarded.
	if rawCount != driftPageCap {
		t.Fatalf("precondition failed: raw Drift = %d rows, want the %d-row cap", rawCount, driftPageCap)
	}
	if got := len(diff.UnusedGrants); got != 1050 {
		t.Errorf("unused grants = %d, want 1050 (the drain must cross the store cap)", got)
	}
	if got := len(diff.UnexpectedAccesses); got != 1 {
		t.Errorf("unexpected accesses = %d, want 1 (a violation behind the permitted flood must not be hidden)", got)
	}
}
