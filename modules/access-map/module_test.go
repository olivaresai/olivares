// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package accessmap

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// obs builds an edge observation with a fixed observation time.
func obs(originKind, originRef, resKind, resRef string, mode sdkmodel.AccessMode, src sdkmodel.SignalSource, conf sdkmodel.Confidence) sdkmodel.EdgeObservation {
	return sdkmodel.EdgeObservation{
		OriginKind: originKind, OriginRef: originRef,
		ResourceKind: resKind, ResourceRef: resRef,
		Mode: mode, Source: src, Confidence: conf,
		ObservedAt: time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC),
	}
}

// TestFusion_MultiSignalAccumulatesAndNeverDowngrades feeds two signals that
// reconcile onto the SAME canonical edge: a cooperative attributed signal and a
// later approximate one. The fused edge keeps attributed confidence (no
// downgrade) and records BOTH signal sources (ARCHITECTURE.md).
func TestFusion_MultiSignalAccumulatesAndNeverDowngrades(t *testing.T) {
	st, tenant := newStore(t)
	agentID, _ := seedDiscoveredAgent(t, st, tenant)
	m := New()
	m.UseData(api.NewModuleData(st))

	// 1) attributed cooperative signal.
	if _, err := m.Ingest(context.Background(), tenant.String(),
		obs("agent", claudeAgentExt, "postgres.table", "appdb.public.orders", sdkmodel.ModeRead, sdkmodel.SignalOTEL, sdkmodel.ConfidenceAttributed)); err != nil {
		t.Fatalf("ingest otel: %v", err)
	}
	// 2) later APPROXIMATE signal on the same agent→table→mode key.
	if _, err := m.Ingest(context.Background(), tenant.String(),
		obs("agent", claudeAgentExt, "postgres.table", "appdb.public.orders", sdkmodel.ModeRead, sdkmodel.SignalEBPF, sdkmodel.ConfidenceApproximate)); err != nil {
		t.Fatalf("ingest ebpf: %v", err)
	}

	e := findEdge(t, st, tenant, "appdb.public.orders")
	if e.OriginKind != originAgent || e.OriginID != agentID {
		t.Errorf("fused edge origin = %s/%s, want agent/%s", e.OriginKind, e.OriginID, agentID)
	}
	if e.Confidence != sdkmodel.ConfidenceAttributed {
		t.Errorf("fused confidence = %q, want attributed (a later approximate signal must NOT downgrade)", e.Confidence)
	}
	if e.OccurrenceCount < 2 {
		t.Errorf("occurrence = %d, want >= 2", e.OccurrenceCount)
	}
	sources, _ := e.Metadata["signal_sources"].(string)
	if !strings.Contains(sources, "otel") || !strings.Contains(sources, "ebpf") {
		t.Errorf("signal_sources = %q, want both otel and ebpf", sources)
	}
	if !e.Observed {
		t.Error("fused edge should be observed")
	}
}

// TestUntrustedAnnotationCreatesNoAccessEdge proves an MCP annotation (a declared
// capability, origin=mcp_server, UNTRUSTED) never enters the R/RW access graph as
// an access and so cannot, by itself, raise confidence or fabricate an observed
// edge (ARCHITECTURE.md). Capability declarations are catalog, not the access map.
func TestUntrustedAnnotationCreatesNoAccessEdge(t *testing.T) {
	st, tenant := newStore(t)
	m := New()
	m.UseData(api.NewModuleData(st))

	edge, err := m.Ingest(context.Background(), tenant.String(),
		obs("mcp_server", "github", "mcp.tool", "github/create_issue", sdkmodel.ModeReadWrite, sdkmodel.SignalMCPAnnotation, sdkmodel.ConfidenceApproximate))
	if err != nil {
		t.Fatalf("ingest annotation: %v", err)
	}
	if !edge.ID.IsZero() {
		t.Errorf("an MCP annotation must not create an access edge, got %+v", edge)
	}
	// No access edge exists at all.
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		edges, _, err := sc.AccessEdges().List(context.Background(), model.Query{Limit: 10})
		if err != nil {
			return err
		}
		if len(edges) != 0 {
			t.Errorf("annotation produced %d access edges, want 0", len(edges))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestDiff_DetectsUnexpectedAndUnusedAndReconciles exercises the killer feature
// (ARCHITECTURE.md): an observed-but-not-permitted access is an UNEXPECTED access; a
// permitted-but-never-observed grant is an UNUSED grant; and a grant that lands
// on the same canonical key as an observation reconciles to neither.
func TestDiff_DetectsUnexpectedAndUnusedAndReconciles(t *testing.T) {
	st, tenant := newStore(t)
	seedDiscoveredAgent(t, st, tenant)
	m := New()
	m.UseData(api.NewModuleData(st))

	ctx := context.Background()
	// T1: observed only → unexpected access (observed, not permitted).
	mustIngest(t, m, tenant, obs("agent", claudeAgentExt, "postgres.table", "appdb.public.t1", sdkmodel.ModeRead, sdkmodel.SignalPGAudit, sdkmodel.ConfidenceAttributed))
	// T2: granted only → unused grant (permitted, never observed).
	mustIngest(t, m, tenant, obs("agent", claudeAgentExt, "postgres.table", "appdb.public.t2", sdkmodel.ModeWrite, sdkmodel.SignalPolicy, sdkmodel.ConfidenceAttributed))
	// T3: observed AND granted on the same key → reconciles to neither.
	mustIngest(t, m, tenant, obs("agent", claudeAgentExt, "postgres.table", "appdb.public.t3", sdkmodel.ModeRead, sdkmodel.SignalPGAudit, sdkmodel.ConfidenceAttributed))
	mustIngest(t, m, tenant, obs("agent", claudeAgentExt, "postgres.table", "appdb.public.t3", sdkmodel.ModeRead, sdkmodel.SignalPolicy, sdkmodel.ConfidenceAttributed))

	diff, err := m.Diff(ctx, tenant, "user:auditor", model.ActorUser, model.Query{})
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(diff.UnexpectedAccesses) != 1 {
		t.Errorf("unexpected accesses = %d, want 1 (T1)", len(diff.UnexpectedAccesses))
	} else if !diff.UnexpectedAccesses[0].Edge.Observed || diff.UnexpectedAccesses[0].Edge.Permitted {
		t.Error("unexpected access must be observed && !permitted")
	}
	if len(diff.UnusedGrants) != 1 {
		t.Errorf("unused grants = %d, want 1 (T2)", len(diff.UnusedGrants))
	} else if diff.UnusedGrants[0].Edge.Observed || !diff.UnusedGrants[0].Edge.Permitted {
		t.Error("unused grant must be !observed && permitted")
	}
	// T3 reconciled: it must appear in NEITHER list.
	for _, d := range append(diff.UnexpectedAccesses, diff.UnusedGrants...) {
		if d.Edge.Metadata["resource_ref"] == "appdb.public.t3" {
			t.Errorf("T3 (observed+granted) must not be a drift, got kind=%v", d.Kind)
		}
	}
}

func mustIngest(t *testing.T, m *Module, tenant model.TenantID, e sdkmodel.EdgeObservation) {
	t.Helper()
	if _, err := m.Ingest(context.Background(), tenant.String(), e); err != nil {
		t.Fatalf("ingest: %v", err)
	}
}

// TestDiff_CrossOriginReconciliation is the regression for the heterogeneous-origin
// false positive (ARCHITECTURE.md): an access OBSERVED on an agent and a grant PERMITTED
// on the identity that agent runs as land on DIFFERENT natural keys (the bridge
// lifts the observation to the agent; the grant names the credential identity), so
// the raw Drift would report a false unexpected access + false unused grant. The
// diff must reconcile them via Agent.IdentityID and report NEITHER.
func TestDiff_CrossOriginReconciliation(t *testing.T) {
	st, tenant := newStore(t)
	m := New()
	m.UseData(api.NewModuleData(st))
	ctx := context.Background()

	// Identity I with TWO agents running as it: a grant on I therefore does NOT
	// bridge to a single agent (singleAgentForIdentity is not unique) and stays
	// origin=identity — exactly the heterogeneous-origin case.
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		i, err := sc.Identities().Create(ctx, model.Identity{Name: "I", Kind: "db_role", ExternalID: "vault-ent-x"})
		if err != nil {
			return err
		}
		for _, ext := range []string{"agent-a", "agent-b"} {
			if _, err := sc.Agents().Create(ctx, model.Agent{Name: ext, ExternalID: ext, IdentityID: i.ID, Status: model.StatusActive}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Observation bridges to agent-a; grant stays on identity I — different origins,
	// same resource+mode, the access IS permitted via the shared identity.
	mustIngest(t, m, tenant, obs("agent", "agent-a", "postgres.table", "appdb.public.t", sdkmodel.ModeRead, sdkmodel.SignalPGAudit, sdkmodel.ConfidenceAttributed))
	mustIngest(t, m, tenant, obs("identity", "vault-ent-x", "postgres.table", "appdb.public.t", sdkmodel.ModeRead, sdkmodel.SignalPolicy, sdkmodel.ConfidenceAttributed))

	diff, err := m.Diff(ctx, tenant, "user:a", model.ActorUser, model.Query{})
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(diff.UnexpectedAccesses) != 0 || len(diff.UnusedGrants) != 0 {
		t.Errorf("cross-origin permitted access must reconcile to NO drift; got unexpected=%d unused=%d (a permitted access reported as a violation is the worst failure of the killer feature)",
			len(diff.UnexpectedAccesses), len(diff.UnusedGrants))
	}
}

// TestDiff_UnresolvedAgentIsPendingNotFirm proves the honest middle ground: when
// an observed access cannot be tied to an identity (an agent with no resolved
// identity link yet) but a grant exists for the same resource+mode, the access is
// reported as an unexpected access flagged reconciliation_pending — never a firm
// violation (docs/SECURITY-HARDENING.md). It resolves cleanly once links credential→agent.
func TestDiff_UnresolvedAgentIsPendingNotFirm(t *testing.T) {
	st, tenant := newStore(t)
	m := New()
	m.UseData(api.NewModuleData(st))
	ctx := context.Background()

	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		if _, err := sc.Identities().Create(ctx, model.Identity{Name: "J", Kind: "db_role", ExternalID: "vault-ent-y"}); err != nil {
			return err
		}
		_, err := sc.Agents().Create(ctx, model.Agent{Name: "agent-c", ExternalID: "agent-c", Status: model.StatusActive}) // NO IdentityID
		return err
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	mustIngest(t, m, tenant, obs("agent", "agent-c", "postgres.table", "appdb.public.t2", sdkmodel.ModeRead, sdkmodel.SignalPGAudit, sdkmodel.ConfidenceAttributed))
	mustIngest(t, m, tenant, obs("identity", "vault-ent-y", "postgres.table", "appdb.public.t2", sdkmodel.ModeRead, sdkmodel.SignalPolicy, sdkmodel.ConfidenceAttributed))

	diff, err := m.Diff(ctx, tenant, "user:a", model.ActorUser, model.Query{})
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(diff.UnexpectedAccesses) != 1 {
		t.Fatalf("want 1 unexpected access (pending), got %d", len(diff.UnexpectedAccesses))
	}
	if pending, _ := diff.UnexpectedAccesses[0].Edge.Metadata["reconciliation_pending"].(bool); !pending {
		t.Error("an unexpected access with an unresolved agent→identity link and a matching grant must be flagged reconciliation_pending, not headlined as a firm violation")
	}
}

// TestReconciledDrift_SeamMatchesDiff proves the read-only ReconciledDrift seam — the
// one the Terraform provider and the compliance evidence engine now consume (C2) — reconciles the cross-origin false drift away, while the RAW store
// Drift the seam wraps still reports the double-count. This is the exact bug the seam
// exists to stop shipping to IaC and compliance: a permitted cross-origin access seen
// as both a false unexpected access AND a false unused grant.
func TestReconciledDrift_SeamMatchesDiff(t *testing.T) {
	st, tenant := newStore(t)
	m := New()
	m.UseData(api.NewModuleData(st))
	ctx := context.Background()

	// Identity I with two agents (heterogeneous-origin), same as the Diff test.
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		i, err := sc.Identities().Create(ctx, model.Identity{Name: "I", Kind: "db_role", ExternalID: "vault-ent-z"})
		if err != nil {
			return err
		}
		for _, ext := range []string{"agent-a", "agent-b"} {
			if _, err := sc.Agents().Create(ctx, model.Agent{Name: ext, ExternalID: ext, IdentityID: i.ID, Status: model.StatusActive}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	mustIngest(t, m, tenant, obs("agent", "agent-a", "postgres.table", "appdb.public.t", sdkmodel.ModeRead, sdkmodel.SignalPGAudit, sdkmodel.ConfidenceAttributed))
	mustIngest(t, m, tenant, obs("identity", "vault-ent-z", "postgres.table", "appdb.public.t", sdkmodel.ModeRead, sdkmodel.SignalPolicy, sdkmodel.ConfidenceAttributed))

	var reconciled PrivilegeDiff
	var rawCount int
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		var e error
		if reconciled, e = ReconciledDrift(ctx, sc, model.Query{}); e != nil {
			return e
		}
		raw, e := sc.AccessEdges().Drift(ctx, model.Query{})
		rawCount = len(raw)
		return e
	}); err != nil {
		t.Fatalf("view: %v", err)
	}

	// Precondition: the raw store path reports the unreconciled cross-origin rows —
	// the false data the old core route and compliance used to consume.
	if rawCount == 0 {
		t.Fatal("precondition failed: raw store Drift reported 0 cross-origin rows; the scenario is not exercising the bug")
	}
	// The seam reconciles them to nothing — the single source of truth.
	if len(reconciled.UnexpectedAccesses) != 0 || len(reconciled.UnusedGrants) != 0 {
		t.Errorf("ReconciledDrift must cancel the cross-origin false drift the raw path (%d rows) reports; got unexpected=%d unused=%d",
			rawCount, len(reconciled.UnexpectedAccesses), len(reconciled.UnusedGrants))
	}
}

// TestCoverageTierClassification checks the honest, declared capture fidelity per
// resource class (ARCHITECTURE.md): clean (SQL/object), lossy (mongo/vector), opaque
// (redis/sqlite/d1).
func TestCoverageTierClassification(t *testing.T) {
	cases := map[string]string{
		"postgres.table": tierClean, "s3.bucket": tierClean, "file": tierClean,
		"mongo.collection": tierLossy, "vector.index": tierLossy,
		"redis.key": tierOpaque, "sqlite.table": tierOpaque, "d1.table": tierOpaque,
		"mcp.tool": tierMixed, "http.url": tierMixed,
	}
	for kind, want := range cases {
		if got := coverageTier(kind); got != want {
			t.Errorf("coverageTier(%q) = %q, want %q", kind, got, want)
		}
	}
}
