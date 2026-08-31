// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package accessmap

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// tierOf returns the stamped attribution tier of the persisted edge for resourceURI.
func tierOf(t *testing.T, st store.Store, tenant model.TenantID, resourceURI string) string {
	t.Helper()
	e := findEdge(t, st, tenant, resourceURI)
	tier, _ := e.Metadata["attribution_tier"].(string)
	return tier
}

// seedIdentity creates a core Identity carrying the governance metadata would
// reconcile from a roster (the provenance and principal type that decide firmness),
// and returns its id. provider is the canonical source (e.g. "spiffe", "anthropic").
func seedIdentity(t *testing.T, st store.Store, tenant model.TenantID, ext, kind, provider, principal string, minted bool) model.ID {
	t.Helper()
	var id model.ID
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		meta := map[string]any{}
		if principal != "" {
			meta["principal_type"] = principal
		}
		if minted {
			meta["minted"] = true
		}
		i, err := sc.Identities().Create(context.Background(), model.Identity{
			Name: ext, Kind: kind, ExternalID: ext, Provider: provider, Metadata: meta,
		})
		if err != nil {
			return err
		}
		id = i.ID
		return nil
	}); err != nil {
		t.Fatalf("seed identity %q: %v", ext, err)
	}
	return id
}

// seedAgentBound creates an agent bound to identityID (Agent.IdentityID).
func seedAgentBound(t *testing.T, st store.Store, tenant model.TenantID, ext string, identityID model.ID) {
	t.Helper()
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		_, err := sc.Agents().Create(context.Background(), model.Agent{
			Name: ext, ExternalID: ext, IdentityID: identityID, Status: model.StatusActive,
		})
		return err
	}); err != nil {
		t.Fatalf("seed agent %q: %v", ext, err)
	}
}

// TestAttribution_FirmViaCooperativeAgent: a cooperative (OTEL) session that names
// its agent is a firm per-agent attribution — the agent's own runtime told us, no
// store audit is relied on (G8).
func TestAttribution_FirmViaCooperativeAgent(t *testing.T) {
	st, tenant := newStore(t)
	seedDiscoveredAgent(t, st, tenant)
	m := New()
	m.UseData(api.NewModuleData(st))

	mustIngest(t, m, tenant, obs("session", claudeSessionID, "postgres.table", "appdb.public.coop", sdkmodel.ModeRead, sdkmodel.SignalOTEL, sdkmodel.ConfidenceAttributed))

	if got := tierOf(t, st, tenant, "appdb.public.coop"); got != attribFirm {
		t.Errorf("cooperative agent edge tier = %q, want firm", got)
	}
}

// TestAttribution_FirmViaDedicatedCredentialBinding: a credential bound to EXACTLY
// one agent (binding) bridges to that sole agent — a dedicated per-agent
// credential is firm even without a workload SVID.
func TestAttribution_FirmViaDedicatedCredentialBinding(t *testing.T) {
	st, tenant := newStore(t)
	id := seedIdentity(t, st, tenant, "vault-ent-solo", "db_role", "vault", string(identitysource.PrincipalNHI), false)
	seedAgentBound(t, st, tenant, "agent-solo", id)
	m := New()
	m.UseData(api.NewModuleData(st))

	mustIngest(t, m, tenant, obs("identity", "vault-ent-solo", "postgres.table", "appdb.public.solo", sdkmodel.ModeRead, sdkmodel.SignalPGAudit, sdkmodel.ConfidenceAttributed))

	e := findEdge(t, st, tenant, "appdb.public.solo")
	if e.OriginKind != originAgent {
		t.Fatalf("dedicated credential must bridge to its sole agent, got origin %q", e.OriginKind)
	}
	if tier, _ := e.Metadata["attribution_tier"].(string); tier != attribFirm {
		t.Errorf("sole-agent credential edge tier = %q, want firm", tier)
	}
}

// TestAttribution_FirmViaWorkloadSVID: a SPIFFE workload SVID identity (source=
// spiffe, NHI) that no agent is bound to yet is still a FIRM per-NHI attribution —
// the SVID IS the identity. This is the SVID signal the moat depends on.
func TestAttribution_FirmViaWorkloadSVID(t *testing.T) {
	st, tenant := newStore(t)
	spiffeID := "spiffe://corp.example/workload/payments"
	seedIdentity(t, st, tenant, spiffeID, "workload", string(identitysource.SourceSPIFFE), string(identitysource.PrincipalNHI), false)
	m := New()
	m.UseData(api.NewModuleData(st))

	mustIngest(t, m, tenant, obs("identity", spiffeID, "postgres.table", "appdb.public.svid", sdkmodel.ModeRead, sdkmodel.SignalPGAudit, sdkmodel.ConfidenceAttributed))

	e := findEdge(t, st, tenant, "appdb.public.svid")
	if e.OriginKind != originIdentity {
		t.Fatalf("unbound SVID must stay origin=identity (no agent yet), got %q", e.OriginKind)
	}
	if tier, _ := e.Metadata["attribution_tier"].(string); tier != attribFirm {
		t.Errorf("workload SVID edge tier = %q, want firm (the SVID is the NHI)", tier)
	}
}

// TestAttribution_FirmViaWIFAndMintedNHI covers the other two dedicated-NHI signals:
// an Anthropic WIF service account (source=anthropic) and a governance-minted
// per-agent NHI (mint) are both firm even before an agent links.
func TestAttribution_FirmViaWIFAndMintedNHI(t *testing.T) {
	st, tenant := newStore(t)
	seedIdentity(t, st, tenant, "svac_ci_deployer", "service_account", string(identitysource.SourceAnthropic), string(identitysource.PrincipalNHI), false)
	seedIdentity(t, st, tenant, "agent:minted-7", "agent_nhi", "governance", string(identitysource.PrincipalNHI), true)
	m := New()
	m.UseData(api.NewModuleData(st))

	mustIngest(t, m, tenant, obs("identity", "svac_ci_deployer", "s3.bucket", "arn:aws:s3:::wif", sdkmodel.ModeWrite, sdkmodel.SignalPolicy, sdkmodel.ConfidenceAttributed))
	mustIngest(t, m, tenant, obs("identity", "agent:minted-7", "postgres.table", "appdb.public.minted", sdkmodel.ModeRead, sdkmodel.SignalPGAudit, sdkmodel.ConfidenceAttributed))

	if got := tierOf(t, st, tenant, "arn:aws:s3:::wif"); got != attribFirm {
		t.Errorf("WIF service-account edge tier = %q, want firm", got)
	}
	if got := tierOf(t, st, tenant, "appdb.public.minted"); got != attribFirm {
		t.Errorf("minted NHI edge tier = %q, want firm", got)
	}
}

// TestAttribution_FirmViaFederatedAgentRegistries: the hyperscaler agent
// registries are dedicated per-agent sources, so their PER-AGENT rows (Entra
// agentIdentity, AgentCore workload identity, Google SPIFFE agent identity) are
// firm — while ancillary rows from the SAME sources (a blueprint principal whose
// credential every child agent shares, a service-account-backed agent) never are.
func TestAttribution_FirmViaFederatedAgentRegistries(t *testing.T) {
	st, tenant := newStore(t)
	seedIdentity(t, st, tenant, "1b7313c4-entra-agent", identitysource.KindAgentIdentity, string(identitysource.SourceEntraAgent), string(identitysource.PrincipalNHI), false)
	seedIdentity(t, st, tenant, "arn:aws:bedrock-agentcore:eu-west-1:1:workload-identity-directory/default/workload-identity/wl1", identitysource.KindWorkloadIdentity, string(identitysource.SourceAgentCore), string(identitysource.PrincipalNHI), false)
	seedIdentity(t, st, tenant, "spiffe://agents.global.org-1.system.id.goog/resources/aiplatform/projects/9/locations/eu/reasoningEngines/a1", identitysource.KindAgentIdentity, string(identitysource.SourceGoogleAgent), string(identitysource.PrincipalNHI), false)
	// Ancillary rows from the same sources: NEVER firm.
	seedIdentity(t, st, tenant, "bp-principal-9", "blueprint_principal", string(identitysource.SourceEntraAgent), string(identitysource.PrincipalNHI), false)
	seedIdentity(t, st, tenant, "sa-agent@p.iam.gserviceaccount.com", "service_account_agent", string(identitysource.SourceGoogleAgent), string(identitysource.PrincipalNHI), false)
	m := New()
	m.UseData(api.NewModuleData(st))

	cases := []struct {
		origin, resource, want string
	}{
		{"1b7313c4-entra-agent", "appdb.public.entra", attribFirm},
		{"arn:aws:bedrock-agentcore:eu-west-1:1:workload-identity-directory/default/workload-identity/wl1", "appdb.public.agentcore", attribFirm},
		{"spiffe://agents.global.org-1.system.id.goog/resources/aiplatform/projects/9/locations/eu/reasoningEngines/a1", "appdb.public.googleagent", attribFirm},
		{"bp-principal-9", "appdb.public.blueprint", attribApproximate},
		{"sa-agent@p.iam.gserviceaccount.com", "appdb.public.saagent", attribApproximate},
	}
	for _, tc := range cases {
		mustIngest(t, m, tenant, obs("identity", tc.origin, "postgres.table", tc.resource, sdkmodel.ModeRead, sdkmodel.SignalPGAudit, sdkmodel.ConfidenceAttributed))
		if got := tierOf(t, st, tenant, tc.resource); got != tc.want {
			t.Errorf("federated registry edge %s tier = %q, want %q", tc.resource, got, tc.want)
		}
	}
}

// TestAttribution_ApproximateOnSharedPool: a connector-declared shared/pooled
// credential collapses to approximate — NEVER a false firm (ARCHITECTURE.md).
func TestAttribution_ApproximateOnSharedPool(t *testing.T) {
	st, tenant := newStore(t)
	m := New()
	m.UseData(api.NewModuleData(st))

	// A shared/pooled credential arrives as approximate from the connector.
	mustIngest(t, m, tenant, obs("identity", "svc_pool", "postgres.table", "appdb.public.pool", sdkmodel.ModeWrite, sdkmodel.SignalPGAudit, sdkmodel.ConfidenceApproximate))

	if got := tierOf(t, st, tenant, "appdb.public.pool"); got != attribApproximate {
		t.Errorf("shared-pool edge tier = %q, want approximate (not a fabricated firm)", got)
	}
}

// TestAttribution_ApproximateOnBareCredential: a per-agent credential the directory
// has NOT enriched (no SVID/WIF source, no mint, not bound to an agent) is honestly
// approximate — we can name the credential, not a firm per-agent NHI.
func TestAttribution_ApproximateOnBareCredential(t *testing.T) {
	st, tenant := newStore(t)
	m := New()
	m.UseData(api.NewModuleData(st))

	// No pre-seeded identity/agent: the bridge find-or-creates a bare credential.
	mustIngest(t, m, tenant, obs("identity", "raw-cred-xyz", "postgres.table", "appdb.public.bare", sdkmodel.ModeRead, sdkmodel.SignalPGAudit, sdkmodel.ConfidenceAttributed))

	e := findEdge(t, st, tenant, "appdb.public.bare")
	if e.OriginKind != originIdentity {
		t.Fatalf("bare credential must stay origin=identity, got %q", e.OriginKind)
	}
	if tier, _ := e.Metadata["attribution_tier"].(string); tier != attribApproximate {
		t.Errorf("bare credential edge tier = %q, want approximate", tier)
	}
}

// TestAttribution_UnknownOnOpaqueStore: an opaque store (Redis/SQLite/D1) has no
// passive per-identity audit. A non-cooperative backstop (eBPF) edge there is
// honestly UNKNOWN — never a shared-account guess dressed up as approximate, never a
// fabricated agent (G8, ARCHITECTURE.md tiered coverage).
func TestAttribution_UnknownOnOpaqueStore(t *testing.T) {
	st, tenant := newStore(t)
	m := New()
	m.UseData(api.NewModuleData(st))

	// eBPF kernel backstop sees a Redis access but cannot attribute per identity.
	mustIngest(t, m, tenant, obs("identity", "redis-conn", "redis.key", "session:cache", sdkmodel.ModeReadWrite, sdkmodel.SignalEBPF, sdkmodel.ConfidenceApproximate))
	// Even an "attributed"-confidence signal on an opaque store cannot be firm when
	// no per-agent identity backs it: the store cannot be audited per identity.
	mustIngest(t, m, tenant, obs("identity", "sqlite-file-cred", "sqlite.table", "local.t", sdkmodel.ModeRead, sdkmodel.SignalPGAudit, sdkmodel.ConfidenceAttributed))

	if got := tierOf(t, st, tenant, "session:cache"); got != attribUnknown {
		t.Errorf("opaque-store eBPF edge tier = %q, want unknown", got)
	}
	if got := tierOf(t, st, tenant, "local.t"); got != attribUnknown {
		t.Errorf("opaque-store unenriched credential edge tier = %q, want unknown", got)
	}
}

// TestAttribution_FirmSurvivesOpaqueWhenCooperative proves the two honesty axes are
// INDEPENDENT: attribution firmness (origin-side) and coverage fidelity
// (resource-side). A cooperative agent firmly named for a Redis access stays FIRM
// (the agent told us) even though the resource coverage is opaque — we do not lie in
// either direction.
func TestAttribution_FirmSurvivesOpaqueWhenCooperative(t *testing.T) {
	st, tenant := newStore(t)
	seedDiscoveredAgent(t, st, tenant)
	m := New()
	m.UseData(api.NewModuleData(st))

	mustIngest(t, m, tenant, obs("session", claudeSessionID, "redis.key", "agent:state", sdkmodel.ModeReadWrite, sdkmodel.SignalOTEL, sdkmodel.ConfidenceAttributed))

	e := findEdge(t, st, tenant, "agent:state")
	if tier, _ := e.Metadata["attribution_tier"].(string); tier != attribFirm {
		t.Errorf("cooperative agent on opaque store: attribution tier = %q, want firm", tier)
	}
	if cov, _ := e.Metadata["coverage_tier"].(string); cov != tierOpaque {
		t.Errorf("coverage tier = %q, want opaque (the resource fidelity axis is unchanged)", cov)
	}
}

// TestAttribution_DenyClosedNeverFabricates: an observation with no origin reference
// produces NO edge and invents no agent — the deny-closed floor (docs/SECURITY-HARDENING.md).
func TestAttribution_DenyClosedNeverFabricates(t *testing.T) {
	st, tenant := newStore(t)
	m := New()
	m.UseData(api.NewModuleData(st))

	edge, err := m.Ingest(context.Background(), tenant.String(),
		obs("identity", "" /* no origin ref */, "redis.key", "k", sdkmodel.ModeRead, sdkmodel.SignalEBPF, sdkmodel.ConfidenceApproximate))
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if !edge.ID.IsZero() {
		t.Errorf("an origin-less observation must not create an edge, got %+v", edge)
	}
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		edges, _, err := sc.AccessEdges().List(context.Background(), model.Query{Limit: 10})
		if err != nil {
			return err
		}
		if len(edges) != 0 {
			t.Errorf("origin-less observation produced %d edges, want 0", len(edges))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestAttribution_TierNeverDowngradesOnFusion: a firm cooperative attribution
// followed by a weaker eBPF backstop on the SAME edge keeps firm — a later weaker
// signal must not lower attribution, mirroring the confidence rule (ARCHITECTURE.md).
func TestAttribution_TierNeverDowngradesOnFusion(t *testing.T) {
	st, tenant := newStore(t)
	seedDiscoveredAgent(t, st, tenant)
	m := New()
	m.UseData(api.NewModuleData(st))

	// 1) firm cooperative signal → firm.
	mustIngest(t, m, tenant, obs("agent", claudeAgentExt, "postgres.table", "appdb.public.fuse", sdkmodel.ModeRead, sdkmodel.SignalOTEL, sdkmodel.ConfidenceAttributed))
	if got := tierOf(t, st, tenant, "appdb.public.fuse"); got != attribFirm {
		t.Fatalf("after cooperative signal tier = %q, want firm", got)
	}
	// 2) later weaker eBPF (approximate) on the same key must NOT downgrade.
	mustIngest(t, m, tenant, obs("agent", claudeAgentExt, "postgres.table", "appdb.public.fuse", sdkmodel.ModeRead, sdkmodel.SignalEBPF, sdkmodel.ConfidenceApproximate))
	if got := tierOf(t, st, tenant, "appdb.public.fuse"); got != attribFirm {
		t.Errorf("after weaker eBPF signal tier = %q, want firm (no downgrade)", got)
	}
}

// TestAttribution_DiffSurfacesTier proves the PERMITTED-vs-OBSERVED diff no longer
// degrades silently: every drift edge carries its attribution tier through the wire
// DTO, so the UI can show an honest badge instead of treating all findings as equally
// firm (G8).
func TestAttribution_DiffSurfacesTier(t *testing.T) {
	st, tenant := newStore(t)
	m := New()
	m.UseData(api.NewModuleData(st))
	ctx := context.Background()

	// A shared-pool unexpected access (observed, not permitted) → approximate tier.
	mustIngest(t, m, tenant, obs("identity", "svc_pool", "postgres.table", "appdb.public.diff", sdkmodel.ModeWrite, sdkmodel.SignalPGAudit, sdkmodel.ConfidenceApproximate))

	diff, err := m.Diff(ctx, tenant, "user:auditor", model.ActorUser, model.Query{})
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(diff.UnexpectedAccesses) != 1 {
		t.Fatalf("want 1 unexpected access, got %d", len(diff.UnexpectedAccesses))
	}
	if tier, _ := diff.UnexpectedAccesses[0].Edge.Metadata["attribution_tier"].(string); tier != attribApproximate {
		t.Errorf("diff edge attribution_tier = %q, want approximate", tier)
	}
	// The wire DTO carries it too (API surface for the UI badge).
	resp := toDiffResponse(diff)
	if got := resp.UnexpectedAccesses[0].Edge.AttributionTier; got != attribApproximate {
		t.Errorf("diffResponse edge attribution_tier = %q, want approximate", got)
	}
}

// TestAttribution_HumanIdentityIsNotFirm: an agent must never be firmly attributed to
// a HUMAN identity — a human principal is governance-excluded from per-agent NHI
// firmness (bindGate refuses it; the tier refuses it too).
func TestAttribution_HumanIdentityIsNotFirm(t *testing.T) {
	st, tenant := newStore(t)
	// A human identity, even from a federation source, is never a firm NHI.
	seedIdentity(t, st, tenant, "okta-user-jane", "user", string(identitysource.SourceOkta), string(identitysource.PrincipalHuman), false)
	m := New()
	m.UseData(api.NewModuleData(st))

	mustIngest(t, m, tenant, obs("identity", "okta-user-jane", "postgres.table", "appdb.public.human", sdkmodel.ModeRead, sdkmodel.SignalPGAudit, sdkmodel.ConfidenceAttributed))

	if got := tierOf(t, st, tenant, "appdb.public.human"); got != attribApproximate {
		t.Errorf("human-identity edge tier = %q, want approximate (never firm)", got)
	}
}

// TestStrongerTier is the unit guard for the fusion ordering (firm > approximate >
// unknown), including empty/unknown values ranking lowest.
func TestStrongerTier(t *testing.T) {
	cases := []struct{ a, b, want string }{
		{attribFirm, attribApproximate, attribFirm},
		{attribApproximate, attribFirm, attribFirm},
		{attribApproximate, attribUnknown, attribApproximate},
		{attribUnknown, attribApproximate, attribApproximate},
		{attribFirm, attribUnknown, attribFirm},
		{"", attribUnknown, attribUnknown},
		{attribFirm, "", attribFirm},
	}
	for _, c := range cases {
		if got := strongerTier(c.a, c.b); got != c.want {
			t.Errorf("strongerTier(%q,%q) = %q, want %q", c.a, c.b, got, c.want)
		}
	}
}
