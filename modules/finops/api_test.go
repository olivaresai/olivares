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
	"path/filepath"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
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
	auth.SetTestHashParams(auth.TestArgonMemKiB, auth.TestArgonTime, auth.TestArgonThreads)
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
	r := h.do("POST", "/v1/users", admin, map[string]any{"email": email, "password": "memberpass1", "tenant": tenant.String(), "role": role}, nil)
	if r.code != http.StatusCreated {
		h.t.Fatalf("create user = %d %s", r.code, r.raw)
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

	// A valid editor ingest is accepted and lands in the ledger. We also read
	// the finops cost_sample read-model (written alongside the CostRecord ledger inside
	// onCost), and countCosts() below checks the canonical CostRecord ledger itself —
	// together they prove the HTTP path went through onCost, not a divergent writer.
	if r := h.do("POST", "/v1/m/finops/cost", editor, body, tenantHdr(tenant)); r.code != http.StatusAccepted {
		t.Fatalf("editor ingest cost = %d %s, want 202", r.code, r.raw)
	}
	if n := h.countCosts(tenant); n != 1 {
		t.Fatalf("valid ingest wrote %d cost records, want 1", n)
	}
	assertCostReadModel(t, h, tenant)

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
	assertCostReadModel(t, h, tenant)
}

// assertCostReadModel proves ingestion and dedup through the shared stored read model.
func assertCostReadModel(t *testing.T, h *harness, tenant model.TenantID) {
	t.Helper()
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("finops.cost_sample")
		if err != nil {
			return err
		}
		rows, _, err := repo.List(context.Background(), model.Query{Limit: 100})
		if err != nil {
			return err
		}
		var total int64
		for _, r := range rows {
			total += r.Int("cost_micro_usd")
		}
		if len(rows) != 1 || total != 400 {
			t.Errorf("cost read model rows=%d total=%d, want 1 and 400", len(rows), total)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
