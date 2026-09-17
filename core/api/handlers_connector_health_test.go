// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// stubSourceRoster implements api.SourceRoster with a canned list for testing.
type stubSourceRoster struct {
	sources []api.SourceRosterEntry
	err     error
}

func (s *stubSourceRoster) ListSources(context.Context) ([]api.SourceRosterEntry, error) {
	return s.sources, s.err
}

func (s *stubSourceRoster) PutSource(context.Context, auth.Principal, api.SourceRosterInput) (api.SourceApplyResult, error) {
	return api.SourceApplyResult{}, nil
}

func (s *stubSourceRoster) DeleteSource(context.Context, auth.Principal, string) (api.SourceApplyResult, error) {
	return api.SourceApplyResult{}, nil
}

func (s *stubSourceRoster) ReloadSources(context.Context, auth.Principal) (api.SourceReloadReport, error) {
	return api.SourceReloadReport{}, nil
}

// stubConnectorOnboarding implements api.ConnectorOnboarding for testing.
type stubConnectorOnboarding struct {
	connectors []api.ConnectorInfo
}

func (s *stubConnectorOnboarding) ListConnectors(context.Context) ([]api.ConnectorInfo, error) {
	return s.connectors, nil
}

func (s *stubConnectorOnboarding) TestConnector(context.Context, auth.Principal, api.ConnectorOnboardInput) error {
	return nil
}

func (s *stubConnectorOnboarding) PutConnector(context.Context, auth.Principal, api.ConnectorOnboardInput) (api.SourceApplyResult, error) {
	return api.SourceApplyResult{}, nil
}

func (s *stubConnectorOnboarding) DeleteConnector(context.Context, auth.Principal, string) (api.SourceApplyResult, error) {
	return api.SourceApplyResult{}, nil
}

type stubKnowledgeStatus struct {
	status api.KnowledgeStatus
}

func (s stubKnowledgeStatus) KnowledgeStatus(context.Context) api.KnowledgeStatus {
	return s.status
}

func TestPublicStatus_Unauthenticated(t *testing.T) {
	roster := &stubSourceRoster{
		sources: []api.SourceRosterEntry{
			{Name: "aws-prod", Kind: "aws", Status: "running", Enabled: true},
			{Name: "gcp-staging", Kind: "gcp-audit", Status: "running", Enabled: true},
		},
	}
	h := newHarnessOpts(t, func(o *api.Options) {
		o.SourceRoster = roster
		o.KnowledgeStatus = stubKnowledgeStatus{status: api.KnowledgeStatus{
			EmbedderKind: "semantic", RetrievalSemantic: true, Reason: "embeddings_provider_configured",
		}}
	})

	// /status must work without authentication and before setup.
	r := h.do("GET", "/status", "", nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("GET /status = %d %s, want 200", r.code, r.raw)
	}
	status, _ := r.body["status"].(string)
	if status != "operational" {
		t.Errorf("overall status = %q, want operational (all connectors running)", status)
	}
	components, _ := r.body["components"].([]any)
	if len(components) != 5 {
		t.Fatalf("components len = %d, want 5 (api, knowledge, store, connectors, ingest)", len(components))
	}
	for _, c := range components {
		m := c.(map[string]any)
		name := m["name"].(string)
		st := m["status"].(string)
		if name == "api" && st != "operational" {
			t.Errorf("api component status = %q, want operational", st)
		}
	}
}

func TestPublicStatus_DegradedWhenConnectorsFail(t *testing.T) {
	roster := &stubSourceRoster{
		sources: []api.SourceRosterEntry{
			{Name: "aws-prod", Kind: "aws", Status: "running", Enabled: true},
			{Name: "gcp-staging", Kind: "gcp-audit", Status: "failed", Enabled: true},
		},
	}
	h := newHarnessOpts(t, func(o *api.Options) {
		o.SourceRoster = roster
		o.KnowledgeStatus = stubKnowledgeStatus{status: api.KnowledgeStatus{
			EmbedderKind: "semantic", RetrievalSemantic: true, Reason: "embeddings_provider_configured",
		}}
	})

	r := h.do("GET", "/status", "", nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("GET /status = %d %s", r.code, r.raw)
	}
	status, _ := r.body["status"].(string)
	if status != "degraded" {
		t.Errorf("overall status = %q, want degraded (one connector failed)", status)
	}
}

func TestPublicStatus_OutageWhenAllConnectorsFail(t *testing.T) {
	roster := &stubSourceRoster{
		sources: []api.SourceRosterEntry{
			{Name: "aws-prod", Kind: "aws", Status: "failed", Enabled: true},
			{Name: "gcp-staging", Kind: "gcp-audit", Status: "failed", Enabled: true},
		},
	}
	h := newHarnessOpts(t, func(o *api.Options) {
		o.SourceRoster = roster
		o.KnowledgeStatus = stubKnowledgeStatus{status: api.KnowledgeStatus{
			EmbedderKind: "semantic", RetrievalSemantic: true, Reason: "embeddings_provider_configured",
		}}
	})

	r := h.do("GET", "/status", "", nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("GET /status = %d %s", r.code, r.raw)
	}
	connectors := findComponent(r.body, "connectors")
	if connectors != "outage" {
		t.Errorf("connectors component = %q, want outage (all enabled connectors failed)", connectors)
	}
}

func TestPublicStatus_NoRoster(t *testing.T) {
	h := newHarness(t)
	r := h.do("GET", "/status", "", nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("GET /status = %d %s, want 200 even without a roster", r.code, r.raw)
	}
}

// An UNSTATED posture is deny-closed: a source that does not classify itself is
// rendered as a fault, exactly as before the not_configured value existed. This
// is what stops a forgotten field from quietly turning a broken plane green.
func TestPublicStatus_KnowledgeLocalHashWithUnstatedPostureIsDegraded(t *testing.T) {
	h := newHarnessOpts(t, func(o *api.Options) {
		o.KnowledgeStatus = stubKnowledgeStatus{status: api.KnowledgeStatus{
			EmbedderKind: "local-hash", RetrievalSemantic: false, Reason: "embeddings_provider_missing",
		}}
	})
	r := h.do("GET", "/status", "", nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("GET /status = %d %s", r.code, r.raw)
	}
	if r.body["status"] != "degraded" {
		t.Fatalf("overall status = %v, want degraded", r.body["status"])
	}
	if r.body["embedder_kind"] != "local-hash" || r.body["retrieval_semantic"] != false {
		t.Fatalf("knowledge status fields = kind %v semantic %v", r.body["embedder_kind"], r.body["retrieval_semantic"])
	}
	if got := findComponent(r.body, "knowledge"); got != "degraded" {
		t.Fatalf("knowledge component = %q, want degraded", got)
	}
}

// A correct install that was never given the OPTIONAL embeddings provider is
// incomplete, not broken: it must say so with its own word, and it must still
// NAME the capability and the reason (the honesty doctrine) — never claim to be
// fully operational.
func TestPublicStatus_KnowledgeNotConfiguredIsNotAFault(t *testing.T) {
	h := newHarnessOpts(t, func(o *api.Options) {
		o.KnowledgeStatus = stubKnowledgeStatus{status: api.KnowledgeStatus{
			EmbedderKind: "local-hash", RetrievalSemantic: false,
			Reason: "embeddings_provider_missing", Posture: api.PostureNotConfigured,
		}}
	})
	r := h.do("GET", "/status", "", nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("GET /status = %d %s", r.code, r.raw)
	}
	if r.body["status"] != "not_configured" {
		t.Fatalf("overall status = %v, want not_configured (a pristine install is not a fault): %s", r.body["status"], r.raw)
	}
	if got := findComponent(r.body, "knowledge"); got != "not_configured" {
		t.Fatalf("knowledge component = %q, want not_configured", got)
	}
	// Honest, not silent: the unprovisioned capability is still named, with its
	// reason and its lexical-retrieval posture.
	if r.body["knowledge_status_reason"] != "embeddings_provider_missing" {
		t.Fatalf("knowledge_status_reason = %v, want the reason to stay named: %s", r.body["knowledge_status_reason"], r.raw)
	}
	if r.body["embedder_kind"] != "local-hash" || r.body["retrieval_semantic"] != false {
		t.Fatalf("knowledge fields = kind %v semantic %v, want the local-hash posture to stay visible", r.body["embedder_kind"], r.body["retrieval_semantic"])
	}
}

// The other direction: a provider block an operator STARTED and left unusable is
// a fault. "Not configured" is reserved for having asked for nothing at all.
func TestPublicStatus_KnowledgeImpairedProviderStaysDegraded(t *testing.T) {
	for _, reason := range []string{"embeddings_config_incomplete", "embedding_model_denied_by_model_access", "embedding_model_access_unreadable"} {
		t.Run(reason, func(t *testing.T) {
			h := newHarnessOpts(t, func(o *api.Options) {
				o.KnowledgeStatus = stubKnowledgeStatus{status: api.KnowledgeStatus{
					EmbedderKind: "local-hash", RetrievalSemantic: false,
					Reason: reason, Posture: api.PostureImpaired,
				}}
			})
			r := h.do("GET", "/status", "", nil, nil)
			if r.body["status"] != "degraded" {
				t.Fatalf("overall status = %v, want degraded for %s", r.body["status"], reason)
			}
			if got := findComponent(r.body, "knowledge"); got != "degraded" {
				t.Fatalf("knowledge component = %q, want degraded for %s", got, reason)
			}
		})
	}
}

// A real fault always dominates the aggregate word: an unprovisioned optional
// capability can never mask a broken component.
func TestPublicStatus_FaultDominatesNotConfigured(t *testing.T) {
	roster := &stubSourceRoster{
		sources: []api.SourceRosterEntry{
			{Name: "aws-prod", Kind: "aws", Status: "running", Enabled: true},
			{Name: "gcp-staging", Kind: "gcp-audit", Status: "failed", Enabled: true},
		},
	}
	h := newHarnessOpts(t, func(o *api.Options) {
		o.SourceRoster = roster
		o.KnowledgeStatus = stubKnowledgeStatus{status: api.KnowledgeStatus{
			EmbedderKind: "local-hash", RetrievalSemantic: false,
			Reason: "embeddings_provider_missing", Posture: api.PostureNotConfigured,
		}}
	})
	r := h.do("GET", "/status", "", nil, nil)
	if r.body["status"] != "degraded" {
		t.Fatalf("overall status = %v, want degraded (a failed connector outranks an unconfigured capability): %s", r.body["status"], r.raw)
	}
	if got := findComponent(r.body, "knowledge"); got != "not_configured" {
		t.Fatalf("knowledge component = %q, want not_configured (each component keeps its own truth)", got)
	}
}

// An operator-driven guard downgrade is a live posture change on a CONFIGURED
// plane. It stays degraded even when the embedder itself is unprovisioned — the
// benign classification must not swallow it.
func TestPublicStatus_GuardDowngradeOutranksNotConfigured(t *testing.T) {
	h := newHarnessOpts(t, func(o *api.Options) {
		o.KnowledgeStatus = stubKnowledgeStatus{status: api.KnowledgeStatus{
			EmbedderKind: "local-hash", RetrievalSemantic: false,
			Reason: "embeddings_provider_missing", Posture: api.PostureNotConfigured,
			GuardProfile: "public_only", GuardWarning: "knowledge_guard_public_only_active",
			GuardDowngradeCount: 1,
		}}
	})
	r := h.do("GET", "/status", "", nil, nil)
	if got := findComponent(r.body, "knowledge"); got != "degraded" {
		t.Fatalf("knowledge component = %q, want degraded (an active guard downgrade is a fault)", got)
	}
	if r.body["status"] != "degraded" {
		t.Fatalf("overall status = %v, want degraded", r.body["status"])
	}
}

func TestKnowledgeGuardDowngradeVisibleInStatusAndHealthSummary(t *testing.T) {
	st := api.KnowledgeStatus{
		EmbedderKind:        "semantic",
		RetrievalSemantic:   true,
		Reason:              "embeddings_provider_configured",
		GuardProfile:        "public_only",
		GuardWarning:        "knowledge_guard_public_only_active",
		GuardDowngradeCount: 1,
		GuardPublicOnlyKBs: []api.KnowledgeGuardDowngrade{{
			TenantID: "tenant-1", TenantSlug: "acme", KBName: "handbook", Profile: "public_only",
		}},
	}
	h := newHarnessOpts(t, func(o *api.Options) {
		o.KnowledgeStatus = stubKnowledgeStatus{status: st}
	})
	pub := h.do("GET", "/status", "", nil, nil)
	if pub.code != http.StatusOK {
		t.Fatalf("GET /status = %d %s", pub.code, pub.raw)
	}
	if pub.body["status"] != "degraded" || pub.body["guard_warning"] != "knowledge_guard_public_only_active" || pub.body["guard_downgrade_count"] != float64(1) {
		t.Fatalf("public status guard fields = %s", pub.raw)
	}
	if _, leaked := pub.body["guard_public_only_kbs"]; leaked {
		t.Fatalf("public status must not list tenant KB names: %s", pub.raw)
	}

	admin := h.adminLogin()
	health := h.do("GET", "/v1/console/health-summary", admin, nil, nil)
	if health.code != http.StatusOK {
		t.Fatalf("GET /v1/console/health-summary = %d %s", health.code, health.raw)
	}
	if health.body["guard_profile"] != "public_only" || health.body["guard_warning"] != "knowledge_guard_public_only_active" {
		t.Fatalf("health summary guard fields = %s", health.raw)
	}
	kbs, _ := health.body["guard_public_only_kbs"].([]any)
	if len(kbs) != 1 || kbs[0].(map[string]any)["kb_name"] != "handbook" {
		t.Fatalf("health summary public-only KB list = %s", health.raw)
	}
}

func TestHealthSummary_IncludesKnowledgeStatus(t *testing.T) {
	h := newHarnessOpts(t, func(o *api.Options) {
		o.KnowledgeStatus = stubKnowledgeStatus{status: api.KnowledgeStatus{
			EmbedderKind: "local-hash", RetrievalSemantic: false, Reason: "embeddings_provider_missing",
		}}
	})
	admin := h.adminLogin()
	r := h.do("GET", "/v1/console/health-summary", admin, nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("GET /v1/console/health-summary = %d %s", r.code, r.raw)
	}
	if r.body["embedder_kind"] != "local-hash" || r.body["retrieval_semantic"] != false {
		t.Fatalf("health summary knowledge fields = kind %v semantic %v", r.body["embedder_kind"], r.body["retrieval_semantic"])
	}
	if r.body["knowledge_status_reason"] != "embeddings_provider_missing" {
		t.Fatalf("health summary knowledge_status_reason = %v", r.body["knowledge_status_reason"])
	}
}

func TestListSourcesExposesSourceMode(t *testing.T) {
	roster := &stubSourceRoster{
		sources: []api.SourceRosterEntry{
			{Name: "wiki-live", Kind: "confluence", Tenant: "acme", Status: "running", Enabled: true, Config: map[string]string{"mode": "live"}},
			{Name: "drive-export", Kind: "gdrive", Tenant: "acme", Status: "running", Enabled: true},
		},
	}
	h := newHarnessOpts(t, func(o *api.Options) {
		o.SourceRoster = roster
	})
	admin := h.adminLogin()

	r := h.do("GET", "/v1/console/sources", admin, nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("GET /v1/console/sources = %d %s", r.code, r.raw)
	}
	sources, _ := r.body["sources"].([]any)
	if len(sources) != 2 {
		t.Fatalf("sources len = %d, want 2", len(sources))
	}
	first := sources[0].(map[string]any)
	if first["source_mode"] != "live" {
		t.Fatalf("live source source_mode = %v, want live", first["source_mode"])
	}
	second := sources[1].(map[string]any)
	if second["source_mode"] != "export" {
		t.Fatalf("default source_mode = %v, want export", second["source_mode"])
	}
}

func TestConnectorHealth_RequiresAuth(t *testing.T) {
	h := newHarness(t)
	_ = h.adminLogin() // complete setup so the setup gate is satisfied
	r := h.do("GET", "/v1/connectors/health", "", nil, nil)
	if r.code != http.StatusUnauthorized {
		t.Errorf("GET /v1/connectors/health without auth = %d, want 401", r.code)
	}
}

func TestConnectorHealth_ReturnsItems(t *testing.T) {
	roster := &stubSourceRoster{}
	onboarding := &stubConnectorOnboarding{
		connectors: []api.ConnectorInfo{
			{Kind: "aws", Title: "AWS CloudTrail"},
			{Kind: "gcp-audit", Title: "GCP Audit Logs"},
			{Kind: "slack", Title: "Slack"},
		},
	}
	h := newHarnessOpts(t, func(o *api.Options) {
		o.SourceRoster = roster
		o.ConnectorOnboarding = onboarding
	})
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	roster.sources = []api.SourceRosterEntry{
		{Name: "aws-prod", Kind: "aws", Tenant: tenant.String(), Status: "running", Enabled: true, PollSeconds: 300, SourceMode: "live"},
		{Name: "gcp-staging", Kind: "gcp-audit", Tenant: tenant.String(), Status: "failed", Enabled: true},
		{Name: "disabled-one", Kind: "slack", Tenant: tenant.String(), Status: "disabled", Enabled: false},
	}

	r := h.do("GET", "/v1/connectors/health", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("GET /v1/connectors/health = %d %s", r.code, r.raw)
	}
	items, _ := r.body["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("items len = %d, want 3", len(items))
	}

	first := items[0].(map[string]any)
	if first["name"] != "aws-prod" {
		t.Errorf("first item name = %v, want aws-prod", first["name"])
	}
	if first["title"] != "AWS CloudTrail" {
		t.Errorf("first item title = %v, want AWS CloudTrail (enriched from catalog)", first["title"])
	}
	if first["health_state"] != "healthy" {
		t.Errorf("running connector health_state = %v, want healthy", first["health_state"])
	}
	if first["source_mode"] != "live" {
		t.Errorf("first source_mode = %v, want live", first["source_mode"])
	}

	failed := items[1].(map[string]any)
	if failed["health_state"] != "down" {
		t.Errorf("failed connector health_state = %v, want down", failed["health_state"])
	}
	if failed["source_mode"] != "export" {
		t.Errorf("failed default source_mode = %v, want export", failed["source_mode"])
	}
	if failed["trend"] != "down" {
		t.Errorf("failed connector trend = %v, want down", failed["trend"])
	}

	summary, _ := r.body["summary"].(map[string]any)
	if summary["total"] != float64(3) {
		t.Errorf("summary total = %v, want 3", summary["total"])
	}
	if summary["running"] != float64(1) {
		t.Errorf("summary running = %v, want 1", summary["running"])
	}
	if summary["failed"] != float64(1) {
		t.Errorf("summary failed = %v, want 1", summary["failed"])
	}
}

// The durable source roster is deployment-global storage whose rows name their
// business tenant. This tenant-facing projection must use only the canonical
// tenant resolved by authzTenant: foreign and legacy malformed rows are neither
// metadata nor aggregate input for the response.
func TestConnectorHealth_TenantScopesItemsAndSummary(t *testing.T) {
	roster := &stubSourceRoster{}
	onboarding := &stubConnectorOnboarding{connectors: []api.ConnectorInfo{
		{Kind: "t1-kind", Title: "Tenant One Connector"},
		{Kind: "t2-private-kind", Title: "Tenant Two Private Connector"},
		{Kind: "legacy-kind", Title: "Legacy Unassigned Connector"},
		{Kind: "unknown-kind", Title: "Unknown Tenant Connector"},
	}}
	h := newHarnessOpts(t, func(o *api.Options) {
		o.SourceRoster = roster
		o.ConnectorOnboarding = onboarding
	})
	admin := h.adminLogin()
	tenantOne := h.createOrg(admin, "tenant-one")
	tenantTwo := h.createOrg(admin, "tenant-two")
	emptyTenant := h.createOrg(admin, "empty-tenant")
	unknownTenant := model.NewTenantID()

	roster.sources = []api.SourceRosterEntry{
		{Name: "t1-running", Kind: "t1-kind", Tenant: tenantOne.String(), Status: "running", Enabled: true},
		{Name: "t1-disabled", Kind: "t1-kind", Tenant: tenantOne.String(), Status: "disabled", Enabled: false},
		{Name: "t2-private-failure", Kind: "t2-private-kind", Tenant: tenantTwo.String(), Status: "failed", Enabled: true},
		{Name: "legacy-unassigned", Kind: "legacy-kind", Tenant: "", Status: "failed", Enabled: true},
		{Name: "unknown-tenant-row", Kind: "unknown-kind", Tenant: unknownTenant.String(), Status: "stopped", Enabled: true},
	}

	multi := h.tenantTokenMulti(admin, "multi@tenants.example", tenantOne, tenantTwo)
	oneOnly := h.mkMember(admin, "one@tenant.example", "memberpass1", auth.RoleViewer, tenantOne)

	t.Run("anonymous", func(t *testing.T) {
		got := h.do("GET", "/v1/connectors/health", "", nil, tenantHdr(tenantOne))
		if got.code != http.StatusUnauthorized {
			t.Fatalf("status = %d %s, want 401", got.code, got.raw)
		}
	})
	t.Run("multi membership requires active tenant", func(t *testing.T) {
		got := h.do("GET", "/v1/connectors/health", multi, nil, nil)
		if got.code != http.StatusBadRequest {
			t.Fatalf("status = %d %s, want 400", got.code, got.raw)
		}
	})
	t.Run("unknown active tenant is denied", func(t *testing.T) {
		got := h.do("GET", "/v1/connectors/health", multi, nil, tenantHdr(unknownTenant))
		if got.code != http.StatusForbidden {
			t.Fatalf("status = %d %s, want 403", got.code, got.raw)
		}
	})
	t.Run("single membership defaults", func(t *testing.T) {
		assertConnectorHealth(t, h.do("GET", "/v1/connectors/health", oneOnly, nil, nil),
			[]string{"t1-running", "t1-disabled"}, connectorSummaryWant{total: 2, running: 1, disabled: 1})
	})
	t.Run("tenant one selected from multi membership", func(t *testing.T) {
		got := h.do("GET", "/v1/connectors/health", multi, nil, tenantHdr(tenantOne))
		assertConnectorHealth(t, got, []string{"t1-running", "t1-disabled"},
			connectorSummaryWant{total: 2, running: 1, disabled: 1})
		for _, forbidden := range []string{
			"t2-private-failure", "Tenant Two Private Connector", tenantTwo.String(),
			"legacy-unassigned", "Legacy Unassigned Connector",
			"unknown-tenant-row", "Unknown Tenant Connector", unknownTenant.String(),
		} {
			if strings.Contains(got.raw, forbidden) {
				t.Errorf("response exposes foreign metadata %q: %s", forbidden, got.raw)
			}
		}
	})
	t.Run("tenant two selected from multi membership", func(t *testing.T) {
		assertConnectorHealth(t, h.do("GET", "/v1/connectors/health", multi, nil, tenantHdr(tenantTwo)),
			[]string{"t2-private-failure"}, connectorSummaryWant{total: 1, failed: 1})
	})
	t.Run("authorized tenant with no rows is empty", func(t *testing.T) {
		assertConnectorHealth(t, h.do("GET", "/v1/connectors/health", admin, nil, tenantHdr(emptyTenant)),
			[]string{}, connectorSummaryWant{})
	})
	t.Run("superadmin is scoped by selected business tenant", func(t *testing.T) {
		withoutTenant := h.do("GET", "/v1/connectors/health", admin, nil, nil)
		if withoutTenant.code != http.StatusBadRequest {
			t.Fatalf("without tenant status = %d %s, want 400", withoutTenant.code, withoutTenant.raw)
		}
		assertConnectorHealth(t, h.do("GET", "/v1/connectors/health", admin, nil, tenantHdr(tenantOne)),
			[]string{"t1-running", "t1-disabled"}, connectorSummaryWant{total: 2, running: 1, disabled: 1})
	})
	t.Run("roster error is preserved", func(t *testing.T) {
		roster.err = errors.New("roster unavailable")
		t.Cleanup(func() { roster.err = nil })
		got := h.do("GET", "/v1/connectors/health", multi, nil, tenantHdr(tenantOne))
		if got.code != http.StatusInternalServerError {
			t.Fatalf("status = %d %s, want 500", got.code, got.raw)
		}
	})
}

type connectorSummaryWant struct {
	total    int
	running  int
	failed   int
	stopped  int
	disabled int
}

func assertConnectorHealth(t *testing.T, got resp, names []string, want connectorSummaryWant) {
	t.Helper()
	if got.code != http.StatusOK {
		t.Fatalf("status = %d %s, want 200", got.code, got.raw)
	}
	items, ok := got.body["items"].([]any)
	if !ok {
		t.Fatalf("items = %#v, want JSON array", got.body["items"])
	}
	if len(items) != len(names) {
		t.Fatalf("items = %#v, want names %v", items, names)
	}
	for i, name := range names {
		item, ok := items[i].(map[string]any)
		if !ok || item["name"] != name {
			t.Fatalf("item[%d] = %#v, want name %q", i, items[i], name)
		}
	}
	summary, ok := got.body["summary"].(map[string]any)
	if !ok {
		t.Fatalf("summary = %#v, want object", got.body["summary"])
	}
	for field, value := range map[string]int{
		"total": want.total, "running": want.running, "failed": want.failed,
		"stopped": want.stopped, "disabled": want.disabled,
	} {
		if summary[field] != float64(value) {
			t.Errorf("summary.%s = %v, want %d", field, summary[field], value)
		}
	}
}

func TestConnectorHealth_NoRoster(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	r := h.do("GET", "/v1/connectors/health", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("GET /v1/connectors/health = %d %s, want 200 with empty items", r.code, r.raw)
	}
	items, _ := r.body["items"].([]any)
	if len(items) != 0 {
		t.Errorf("items len = %d, want 0 (no roster)", len(items))
	}
}

func findComponent(body map[string]any, name string) string {
	components, _ := body["components"].([]any)
	for _, c := range components {
		m := c.(map[string]any)
		if m["name"] == name {
			return m["status"].(string)
		}
	}
	return ""
}
