// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/finops"
	"github.com/olivaresai/olivares/sdk"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

type fakeSource struct{ costs []sdkmodel.CostSample }

func (f *fakeSource) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: "test.finops-source", Version: "0.0.1", APIVersion: sdk.APIVersion, Type: sdk.TypeSource}
}
func (f *fakeSource) Open(context.Context, sdk.Config) error { return nil }
func (f *fakeSource) Gather(ctx context.Context, sink sdk.Sink) error {
	for _, c := range f.costs {
		if err := sink.Emit(ctx, c); err != nil {
			return err
		}
	}
	return nil
}
func (f *fakeSource) Close(context.Context) error { return nil }

type harness struct {
	t        *testing.T
	srv      *api.Server
	st       store.Store
	setupTok string
}

func newHarness(t *testing.T, m *finops.Module) *harness {
	t.Helper()
	ctx := context.Background()
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, m.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(sys store.SystemScope) error { _, e := sys.EnsureSystemTenant(ctx); return e }); err != nil {
		t.Fatal(err)
	}
	m.UseData(api.NewModuleData(st))

	_, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := audit.NewSigner(priv)
	tok := secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token"))
	plaintext, _, err := tok.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	srv, err := api.New(api.Options{
		Store: st, Authenticator: auth.NewAuthenticator(st, nil), Authorizer: auth.NewAuthorizer(nil),
		Signer: signer, SetupToken: tok, Version: "test", Modules: []api.Module{m},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, srv: srv, st: st, setupTok: plaintext}
}

type resp struct {
	code int
	body map[string]any
	raw  string
}

func (h *harness) do(method, path, token string, body any, hdr map[string]string) resp {
	h.t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.RemoteAddr = "10.0.0.1:1234"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.srv.Handler().ServeHTTP(rec, req)
	out := resp{code: rec.Code, raw: rec.Body.String()}
	_ = json.Unmarshal(rec.Body.Bytes(), &out.body)
	return out
}

func tenantHdr(t model.TenantID) map[string]string {
	return map[string]string{"X-Olivares-Tenant": t.String()}
}

func (h *harness) adminLogin() string {
	h.t.Helper()
	if r := h.do("POST", "/v1/setup", "", map[string]any{"token": h.setupTok, "email": "root@x.io", "password": "supersecret1"}, nil); r.code != http.StatusCreated {
		h.t.Fatalf("setup = %d %s", r.code, r.raw)
	}
	r := h.do("POST", "/v1/auth/login", "", map[string]any{"email": "root@x.io", "password": "supersecret1"}, nil)
	if r.code != http.StatusOK {
		h.t.Fatalf("login = %d %s", r.code, r.raw)
	}
	return r.body["token"].(string)
}

func (h *harness) createOrg(token, slug string) model.TenantID {
	h.t.Helper()
	r := h.do("POST", "/v1/system/orgs", token, map[string]any{"name": slug, "slug": slug}, nil)
	if r.code != http.StatusCreated {
		h.t.Fatalf("create org %s = %d %s", slug, r.code, r.raw)
	}
	return model.TenantID(r.body["tenant_id"].(string))
}

func (h *harness) roleToken(admin string, tenant model.TenantID, email, role string) string {
	h.t.Helper()
	r := h.do("POST", "/v1/users", admin, map[string]any{"email": email, "password": "memberpass1"}, nil)
	if r.code != http.StatusCreated {
		h.t.Fatalf("create user = %d %s", r.code, r.raw)
	}
	uid := r.body["id"].(string)
	if r := h.do("POST", "/v1/memberships", admin, map[string]any{"user_id": uid, "tenant": tenant.String(), "role": role}, nil); r.code != http.StatusCreated {
		h.t.Fatalf("grant = %d %s", r.code, r.raw)
	}
	r = h.do("POST", "/v1/auth/login", "", map[string]any{"email": email, "password": "memberpass1"}, nil)
	if r.code != http.StatusOK {
		h.t.Fatalf("login = %d %s", r.code, r.raw)
	}
	return r.body["token"].(string)
}

func (h *harness) waitCosts(tenant model.TenantID, n int) {
	h.t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		count := 0
		_ = h.st.View(context.Background(), tenant, func(sc store.Scope) error {
			cs, _, err := sc.Costs().List(context.Background(), model.Query{Limit: 100})
			count = len(cs)
			return err
		})
		if count >= n {
			return
		}
		select {
		case <-deadline:
			h.t.Fatalf("ledger reached %d records, want >= %d", count, n)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// countCosts returns the number of CostRecord ledger rows for the tenant.
func (h *harness) countCosts(tenant model.TenantID) int {
	h.t.Helper()
	count := 0
	_ = h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		cs, _, err := sc.Costs().List(context.Background(), model.Query{Limit: 100})
		count = len(cs)
		return err
	})
	return count
}

// hasAuditAction reports whether the tenant's audit chain contains an event with the
// given action — used to prove a privileged write was audited to the real principal.
func (h *harness) hasAuditAction(tenant model.TenantID, action string) bool {
	h.t.Helper()
	found := false
	_ = h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(context.Background(), 0, func(e model.AuditEvent) error {
			if e.Action == action {
				found = true
			}
			return nil
		})
	})
	return found
}

// TestCostIngestHTTP covers cost ingestion: the POST /cost route ingests a CostSample
// through the SAME onCost path the bus uses (single-provenance ledger, natural-key
// dedup), is deny-closed to non-writers, audits the principal, and ignores a
// reference-less payload exactly as onCost does.
// TestCostCenterUpdateKeepsAnOmittedStatus pins the case an internal adversarial panel found on
// 2026-08-18, AFTER the pointer-receiver fix and BECAUSE of it.
//
// ⛔ THE DEFECT THIS CLOSES, and it is the fix breaking in the opposite direction. `PUT
// /cost-centers/{id}` is a full replace, and `validate()` defaults an omitted status to "active".
// So a plain RENAME — a body with code and name and no status — silently REVIVED an archived cost
// center: it went back to attributing spend (costcenter.go, resolveCostCenter requires exactly
// "active") and back into chargeback statements (statements.go, the generator filters on "active").
//
// Before the receiver fix it stored "" (broken toward silence); with the receiver fix alone it
// activated (broken toward noise). Neither is what a rename must do: THE OMITTED FIELD IS KEPT.
//
// The test drives the real HTTP surface because that is where the defect lives — the console's two
// mutations both send `status` explicitly, so a console-level test cannot see it. The panel entered
// through the API route, and so does this.
func TestCostCenterUpdateKeepsAnOmittedStatus(t *testing.T) {
	m := finops.New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@acme.com", auth.RoleEditor)

	estado := func(r resp) string {
		v, _ := r.body["status"].(string)
		return v
	}

	created := h.do("POST", "/v1/m/finops/cost-centers", editor, map[string]any{
		"code": "OLD-01", "name": "Legacy", "status": "archived",
	}, tenantHdr(tenant))
	if created.code != 201 {
		t.Fatalf("create: got %d, want 201 (%s)", created.code, created.raw)
	}
	if got := estado(created); got != "archived" {
		t.Fatalf("created status = %q, want \"archived\"", got)
	}
	id, _ := created.body["id"].(string)
	if id == "" {
		t.Fatalf("create returned no id: %s", created.raw)
	}

	// El renombrado: SIN `status` en el cuerpo, que es exactamente lo que manda un cliente que sólo
	// quiere corregir el nombre.
	renamed := h.do("PUT", "/v1/m/finops/cost-centers/"+id, editor, map[string]any{
		"code": "OLD-01", "name": "Legacy (renamed)",
	}, tenantHdr(tenant))
	if renamed.code != 200 {
		t.Fatalf("rename: got %d, want 200 (%s)", renamed.code, renamed.raw)
	}
	if got := estado(renamed); got != "archived" {
		t.Fatalf("a rename revived the cost center: status = %q, want \"archived\" — "+
			"an omitted field must be KEPT, not defaulted", got)
	}

	// Y la dirección que NO debe dispararse: un `status` explícito sí manda.
	activated := h.do("PUT", "/v1/m/finops/cost-centers/"+id, editor, map[string]any{
		"code": "OLD-01", "name": "Legacy (renamed)", "status": "active",
	}, tenantHdr(tenant))
	if activated.code != 200 {
		t.Fatalf("activate: got %d, want 200 (%s)", activated.code, activated.raw)
	}
	if got := estado(activated); got != "active" {
		t.Fatalf("an explicit status was ignored: got %q, want \"active\"", got)
	}
}

func TestListCostCenterMappingsHonorsParentLimitAndCursor(t *testing.T) {
	m := finops.New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "mapping-page")
	editor := h.roleToken(admin, tenant, "mapping-editor@acme.com", auth.RoleEditor)
	hdr := tenantHdr(tenant)

	created := h.do("POST", "/v1/m/finops/cost-centers", editor, map[string]any{
		"code": "ENG-01", "name": "Engineering",
	}, hdr)
	if created.code != http.StatusCreated {
		t.Fatalf("create cost center: got %d, want %d (%s)", created.code, http.StatusCreated, created.raw)
	}
	costCenterID, _ := created.body["id"].(string)
	if costCenterID == "" {
		t.Fatalf("create cost center returned no id: %s", created.raw)
	}

	other := h.do("POST", "/v1/m/finops/cost-centers", editor, map[string]any{
		"code": "OPS-01", "name": "Operations",
	}, hdr)
	if other.code != http.StatusCreated {
		t.Fatalf("create other cost center: got %d, want %d (%s)", other.code, http.StatusCreated, other.raw)
	}
	otherCostCenterID, _ := other.body["id"].(string)
	if otherCostCenterID == "" {
		t.Fatalf("create other cost center returned no id: %s", other.raw)
	}
	otherMapping := h.do("POST", "/v1/m/finops/cost-centers/"+otherCostCenterID+"/mappings", editor, map[string]any{
		"source_dimension": "team",
		"source_key":       "operations",
		"priority":         10,
	}, hdr)
	if otherMapping.code != http.StatusCreated {
		t.Fatalf("create other mapping: got %d, want %d (%s)", otherMapping.code, http.StatusCreated, otherMapping.raw)
	}

	for _, sourceKey := range []string{"platform", "security"} {
		mapping := h.do("POST", "/v1/m/finops/cost-centers/"+costCenterID+"/mappings", editor, map[string]any{
			"source_dimension": "team",
			"source_key":       sourceKey,
			"priority":         10,
		}, hdr)
		if mapping.code != http.StatusCreated {
			t.Fatalf("create mapping %q: got %d, want %d (%s)", sourceKey, mapping.code, http.StatusCreated, mapping.raw)
		}
	}

	type mappingPage struct {
		Items []struct {
			CostCenterID string `json:"cost_center_id"`
			ID           string `json:"id"`
			SourceKey    string `json:"source_key"`
		} `json:"items"`
		Cursor  string `json:"cursor"`
		HasMore bool   `json:"has_more"`
	}
	decodePage := func(label string, got resp) mappingPage {
		t.Helper()
		if got.code != http.StatusOK {
			t.Fatalf("%s: got %d, want %d (%s)", label, got.code, http.StatusOK, got.raw)
		}
		var page mappingPage
		if err := json.Unmarshal([]byte(got.raw), &page); err != nil {
			t.Fatalf("decode %s: %v (%s)", label, err, got.raw)
		}
		return page
	}

	firstResponse := h.do("GET", "/v1/m/finops/cost-centers/"+costCenterID+"/mappings?limit=1", editor, nil, hdr)
	first := decodePage("first mappings page", firstResponse)
	if len(first.Items) != 1 {
		t.Fatalf("first page items = %d, want 1 (%s)", len(first.Items), firstResponse.raw)
	}
	if first.Items[0].CostCenterID != costCenterID {
		t.Errorf("first mapping cost_center_id = %q, want %q", first.Items[0].CostCenterID, costCenterID)
	}
	if !first.HasMore {
		t.Errorf("first page has_more = false, want true (%s)", firstResponse.raw)
	}
	if first.Cursor == "" {
		t.Fatalf("first page cursor is empty, want a continuation cursor (%s)", firstResponse.raw)
	}

	secondResponse := h.do("GET", "/v1/m/finops/cost-centers/"+costCenterID+
		"/mappings?limit=1&cursor="+url.QueryEscape(first.Cursor), editor, nil, hdr)
	second := decodePage("second mappings page", secondResponse)
	if len(second.Items) != 1 {
		t.Fatalf("second page items = %d, want 1 (%s)", len(second.Items), secondResponse.raw)
	}
	if second.Items[0].CostCenterID != costCenterID {
		t.Errorf("second mapping cost_center_id = %q, want %q", second.Items[0].CostCenterID, costCenterID)
	}
	if second.Items[0].ID == first.Items[0].ID {
		t.Errorf("second page repeated mapping id %q; cursor was not honored", second.Items[0].ID)
	}
	if second.Items[0].SourceKey == first.Items[0].SourceKey {
		t.Errorf("second page repeated source_key %q; cursor was not honored", second.Items[0].SourceKey)
	}
	if second.HasMore {
		t.Errorf("second page has_more = true, want false (%s)", secondResponse.raw)
	}
	if second.Cursor != "" {
		t.Errorf("second page cursor = %q, want empty (%s)", second.Cursor, secondResponse.raw)
	}
}

func TestCostIngestHTTP(t *testing.T) {
	m := finops.New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@acme.com", auth.RoleEditor)
	viewer := h.roleToken(admin, tenant, "v@acme.com", auth.RoleViewer)

	// A fixed instant so the two dedup POSTs share a natural key (SystemClock would
	// otherwise stamp distinct OccurredAt and they would be two buckets).
	occurred := time.Now().UTC().Format(time.RFC3339Nano)
	body := map[string]any{
		"provider_ref": "anthropic", "model_ref": "claude-opus-4-8",
		"input_tokens": 100, "output_tokens": 50, "cost_micro_usd": 400, "occurred_at": occurred,
	}

	// Deny-closed: a viewer holds no finops:cost:write → 403.
	if r := h.do("POST", "/v1/m/finops/cost", viewer, body, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("viewer ingest cost = %d, want 403", r.code)
	}

	// A payload with neither provider_ref nor model_ref is rejected (mirrors onCost's
	// ignore rule) and writes nothing.
	if r := h.do("POST", "/v1/m/finops/cost", editor, map[string]any{"cost_micro_usd": 400}, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Fatalf("ingest without refs = %d, want 400", r.code)
	}
	if n := h.countCosts(tenant); n != 0 {
		t.Fatalf("reference-less ingest wrote %d cost records, want 0", n)
	}

	// A valid editor ingest is accepted and lands in the ledger. Spend analytics reads
	// the finops cost_sample read-model (written alongside the CostRecord ledger inside
	// onCost), and countCosts() below checks the canonical CostRecord ledger itself —
	// together they prove the HTTP path went through onCost, not a divergent writer.
	if r := h.do("POST", "/v1/m/finops/cost", editor, body, tenantHdr(tenant)); r.code != http.StatusAccepted {
		t.Fatalf("editor ingest cost = %d %s, want 202", r.code, r.raw)
	}
	if n := h.countCosts(tenant); n != 1 {
		t.Fatalf("valid ingest wrote %d cost records, want 1", n)
	}
	r := h.do("GET", "/v1/m/finops/spend?dimension=model", editor, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("spend = %d %s", r.code, r.raw)
	}
	if total, _ := r.body["total_micro_usd"].(float64); total != 400 {
		t.Fatalf("spend total after ingest = %v, want 400", r.body["total_micro_usd"])
	}

	// The privileged write is audited to the real principal.
	if !h.hasAuditAction(tenant, "finops.cost.ingest") {
		t.Errorf("expected a finops.cost.ingest audit event for the privileged ingest")
	}

	// Idempotent: an identical second POST (same natural key) does not double-count.
	if r := h.do("POST", "/v1/m/finops/cost", editor, body, tenantHdr(tenant)); r.code != http.StatusAccepted {
		t.Fatalf("second identical ingest = %d %s, want 202", r.code, r.raw)
	}
	if n := h.countCosts(tenant); n != 1 {
		t.Fatalf("double-POST wrote %d cost records, want 1 (dedup by natural key)", n)
	}
	r = h.do("GET", "/v1/m/finops/spend?dimension=model", editor, nil, tenantHdr(tenant))
	if total, _ := r.body["total_micro_usd"].(float64); total != 400 {
		t.Fatalf("spend total after double-POST = %v, want 400 (not 800)", r.body["total_micro_usd"])
	}
}

func TestFinOpsEndToEnd(t *testing.T) {
	m := finops.New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@acme.com", auth.RoleEditor)
	viewer := h.roleToken(admin, tenant, "v@acme.com", auth.RoleViewer)

	// A viewer cannot create a budget; an editor can.
	if r := h.do("POST", "/v1/m/finops/budgets", viewer, map[string]any{"name": "x", "limit_micro_usd": 1000}, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Errorf("viewer create budget = %d, want 403", r.code)
	}
	r := h.do("POST", "/v1/m/finops/budgets", editor, map[string]any{
		"name": "monthly-cap", "enabled": true, "dimension": "global",
		"period": "monthly", "limit_micro_usd": 1000, "thresholds": []float64{0.5},
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("create budget = %d %s", r.code, r.raw)
	}
	budgetID := r.body["id"].(string)

	// An invalid budget is rejected.
	if r := h.do("POST", "/v1/m/finops/budgets", editor, map[string]any{"name": "bad", "dimension": "model", "limit_micro_usd": 10}, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Errorf("budget without key for model dimension = %d, want 400", r.code)
	}

	// Drive the cost stream through the real runtime + bus. Total 600 crosses 50%.
	rt := runtime.New(runtime.Options{})
	if err := rt.AddModule(m, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	src := &fakeSource{costs: []sdkmodel.CostSample{
		{ProviderRef: "anthropic", ModelRef: "claude-opus-4-8", InputTokens: 100, OutputTokens: 50, CostMicroUSD: 400, OccurredAt: now},
		{ProviderRef: "google", ModelRef: "gemini-1.5-flash", InputTokens: 80, OutputTokens: 40, CostMicroUSD: 200, OccurredAt: now.Add(time.Second)},
	}}
	if err := rt.AddSource(src, sdk.Config{}, tenant.String()); err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(ctx)
	})
	h.waitCosts(tenant, 2)

	// Spend by model: opus is the top bucket (400 of 600).
	r = h.do("GET", "/v1/m/finops/spend?dimension=model", viewer, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("spend = %d %s", r.code, r.raw)
	}
	if total, _ := r.body["total_micro_usd"].(float64); total != 600 {
		t.Errorf("spend total = %v, want 600", r.body["total_micro_usd"])
	}

	// An invalid since/until is rejected (not silently widened to all-time).
	if r := h.do("GET", "/v1/m/finops/spend?since=not-a-date", viewer, nil, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Errorf("invalid since = %d, want 400", r.code)
	}

	// Summary totals.
	if r := h.do("GET", "/v1/m/finops/spend/summary", viewer, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("summary = %d %s", r.code, r.raw)
	}

	// Forecast for the current month projects at or above spend.
	r = h.do("GET", "/v1/m/finops/forecast?period=monthly", viewer, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("forecast = %d %s", r.code, r.raw)
	}
	if sp, _ := r.body["spend_micro_usd"].(float64); sp != 600 {
		t.Errorf("forecast spend = %v, want 600", r.body["spend_micro_usd"])
	}

	// Budget status: 600/1000 = 60%.
	r = h.do("GET", "/v1/m/finops/budgets/"+budgetID+"/status", viewer, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("status = %d %s", r.code, r.raw)
	}
	if pct, _ := r.body["consumed_pct"].(float64); pct != 60 {
		t.Errorf("consumed_pct = %v, want 60", r.body["consumed_pct"])
	}

	// The 50% crossing was recorded as an alert.
	r = h.do("GET", "/v1/m/finops/alerts", viewer, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("alerts = %d %s", r.code, r.raw)
	}
	if items, _ := r.body["items"].([]any); len(items) != 1 {
		t.Errorf("alerts = %d, want 1 (50%% crossing)", len(items))
	}

	// Recommendations are served (at least the honest cache disclosure).
	if r := h.do("GET", "/v1/m/finops/recommendations", viewer, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("recommendations = %d %s", r.code, r.raw)
	}

	// Tenant isolation: a non-member is rejected.
	other := h.createOrg(admin, "globex")
	if r := h.do("GET", "/v1/m/finops/spend", viewer, nil, tenantHdr(other)); r.code != http.StatusForbidden {
		t.Errorf("cross-tenant spend = %d, want 403", r.code)
	}
}
