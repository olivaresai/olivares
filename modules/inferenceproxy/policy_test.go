// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inferenceproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestDLPDecideDenyClosed(t *testing.T) {
	cases := []struct {
		name    string
		rules   map[string]string
		classes []string
		want    []string
	}{
		{"inert with no rules", nil, []string{"secret.credential"}, nil},
		{"exact allow", map[string]string{"pii.contact": "allow"}, []string{"pii.contact"}, nil},
		{"exact deny", map[string]string{"secret.credential": "deny"}, []string{"secret.credential"}, []string{"secret.credential"}},
		{"no exact, star allows", map[string]string{"*": "allow"}, []string{"pii.network"}, nil},
		{"no exact, star denies", map[string]string{"*": "deny"}, []string{"pii.network"}, []string{"pii.network"}},
		{"no exact, no star ⇒ deny-closed", map[string]string{"pii.contact": "allow"}, []string{"secret.credential"}, []string{"secret.credential"}},
		{"unknown action denies", map[string]string{"secret.credential": "garbage"}, []string{"secret.credential"}, []string{"secret.credential"}},
		{"clean content allowed", map[string]string{"*": "deny"}, nil, nil},
		{"dedups + sorts", map[string]string{"*": "deny"}, []string{"b", "a", "b"}, []string{"a", "b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := dlpPolicy{rules: tc.rules}
			got := p.decide(tc.classes)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("decide(%v) = %v, want %v", tc.classes, got, tc.want)
			}
		})
	}
}

func TestUnscannedDenyClosed(t *testing.T) {
	cases := []struct {
		name  string
		rules map[string]string
		want  bool
	}{
		{"inert with no rules", nil, false},
		{"enabled, no unscanned rule ⇒ denied", map[string]string{"pii.contact": "allow"}, true},
		{"star does NOT cover unscanned", map[string]string{"*": "allow"}, true},
		{"explicit unscanned allow", map[string]string{"pii.contact": "deny", "unscanned": "allow"}, false},
		{"explicit unscanned deny", map[string]string{"unscanned": "deny"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := (dlpPolicy{rules: tc.rules}).unscannedDenied(); got != tc.want {
				t.Fatalf("unscannedDenied = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestDefaultProxyPolicyIsSafe pins the safe posture applied when a tenant has no config
// row: every gate ON, fail-CLOSED, preventive response DLP, recording MANDATORY.
//
// The recording line used to read "best-effort" and asserting RecordMandatory==false. That
// assertion was correct about the code and wrong about the product: a tenant with no config
// row is precisely the tenant nobody has reasoned about, and shipping it best-effort made the
// evidence guarantee opt-in for everyone who had not opted into anything. The doctrine that
// was supposed to flip it never landed, and a test pinning the old value is what let the gap
// look deliberate. It is flipped here on purpose and with its consequence stated: an
// unconfigured tenant whose pre-forward ledger write fails now gets a 503 instead of an
// ungoverned forward.
func TestDefaultProxyPolicyIsSafe(t *testing.T) {
	p := defaultProxyPolicy()
	if p.FailOpen {
		t.Error("default must be fail-CLOSED (FailOpen=false)")
	}
	if p.ResponseDLPMode != ResponseDLPBuffer {
		t.Errorf("default response mode = %q, want buffer", p.ResponseDLPMode)
	}
	if !p.RecordMandatory {
		t.Error("default recording must be MANDATORY (RecordMandatory=true): no evidence, no forward")
	}
	if p.Ceilings.Any() {
		t.Errorf("default ceilings = %+v, want none", p.Ceilings)
	}
	for name, on := range map[string]bool{
		"model_access": p.GateModelAccess, "budget": p.GateBudget, "residency": p.GateResidency,
		"context_window": p.GateContextWindow, "dlp_request": p.GateDLPRequest, "dlp_response": p.GateDLPResponse,
	} {
		if !on {
			t.Errorf("default gate %q must be ON", name)
		}
	}
}

// TestDefaultDLPPostureDeniesSecretsAndUnscanned attacks a stock deployment with no
// tenant-authored rule. Secret-shaped and unclassifiable egress must be denied by the
// seeded policy; an administrator can still override each class explicitly.
func TestDefaultDLPPostureDeniesSecretsAndUnscanned(t *testing.T) {
	p := defaultProxyPolicy()
	if !p.DLPEnabled() {
		t.Fatal("stock policy left DLP disabled")
	}
	if got := p.DLPDecide([]string{"secret.credential"}); !reflect.DeepEqual(got, []string{"secret.credential"}) {
		t.Fatalf("stock secret decision = %v, want deny", got)
	}
	if !p.DLPUnscannedDenied() {
		t.Fatal("stock policy allowed unscanned egress")
	}
	if got := p.DLPDecide([]string{"pii.contact"}); len(got) != 0 {
		t.Fatalf("stock policy blocked non-secret class without an explicit tenant deny: %v", got)
	}
	custom := PolicyWithDLPRules(p, map[string]string{"pii.contact": dlpAllow})
	if got := custom.DLPDecide([]string{"pii.network"}); !reflect.DeepEqual(got, []string{"pii.network"}) {
		t.Fatalf("tenant-authored policy lost deny-closed unmatched-class behavior: %v", got)
	}

	overridden := PolicyWithDLPRules(p, map[string]string{
		"secret.credential": dlpAllow,
		dlpClassUnscanned:   dlpAllow,
	})
	if got := overridden.DLPDecide([]string{"secret.credential"}); len(got) != 0 {
		t.Fatalf("explicit operator allow did not override seeded secret rule: %v", got)
	}
	if overridden.DLPUnscannedDenied() {
		t.Fatal("explicit operator allow did not override seeded unscanned rule")
	}
}

// TestStoredRuleOverridesSeededDefault proves the public admin write path, rather than
// an in-process-only escape hatch, can tune the secure default.
func TestStoredRuleOverridesSeededDefault(t *testing.T) {
	m, st, tenant := newPolicyHarness(t)
	for _, body := range []string{
		`{"class":"secret.credential","action":"allow","note":"approved test fixture"}`,
		`{"class":"unscanned","action":"allow","note":"approved test fixture"}`,
	} {
		req := httptest.NewRequest(http.MethodPut, "/dlp/rules", bytes.NewBufferString(body))
		rec := httptest.NewRecorder()
		m.handlePutDLPRule(rec, req, policyModuleContextRole(st, tenant, auth.RoleAdmin))
		if rec.Code != http.StatusCreated {
			t.Fatalf("PUT rule = %d %s, want 201", rec.Code, rec.Body.String())
		}
	}

	pol, err := m.Policy(context.Background(), tenant)
	if err != nil {
		t.Fatalf("Policy: %v", err)
	}
	if denied := pol.DLPDecide([]string{"secret.credential"}); len(denied) != 0 {
		t.Fatalf("stored exact allow did not override seeded secret deny: %v", denied)
	}
	if pol.DLPUnscannedDenied() {
		t.Fatal("stored exact allow did not override seeded unscanned deny")
	}
}

type recordedInferenceRoute struct {
	method, pattern string
	perm            auth.Permission
}

type recordingInferenceRoutes struct{ routes []recordedInferenceRoute }

func (r *recordingInferenceRoutes) HandleEntity(method, pattern string, perm auth.Permission, _ api.EntityRef, h api.ModuleHandler) {
	r.Handle(method, pattern, perm, h)
}

func (r *recordingInferenceRoutes) Handle(method, pattern string, perm auth.Permission, _ api.ModuleHandler) {
	r.routes = append(r.routes, recordedInferenceRoute{method: method, pattern: pattern, perm: perm})
}

// TestFailOpenMutationRequiresTenantAdminAndIsAudited attacks the per-tenant
// kill-switch at its write boundary. The route permission must exclude editors,
// include tenant admins, and the successful mutation must enter the ledger.
func TestFailOpenMutationRequiresTenantAdminAndIsAudited(t *testing.T) {
	var routes recordingInferenceRoutes
	New().APIRoutes(&routes)
	var putPerm auth.Permission
	for _, route := range routes.routes {
		if route.method == http.MethodPut && route.pattern == "/config" {
			putPerm = route.perm
			break
		}
	}
	if putPerm == "" {
		t.Fatal("missing PUT /config route")
	}
	if auth.RoleGrants(auth.RoleEditor, putPerm) {
		t.Fatalf("editor unexpectedly holds fail_open mutation permission %q", putPerm)
	}
	if !auth.RoleGrants(auth.RoleAdmin, putPerm) {
		t.Fatalf("tenant admin does not hold fail_open mutation permission %q", putPerm)
	}

	m, st, tenant := newPolicyHarness(t)
	req := httptest.NewRequest(http.MethodPut, "/config", bytes.NewBufferString(`{"fail_open":true}`))
	rec := httptest.NewRecorder()
	m.handlePutConfig(rec, req, policyModuleContextRole(st, tenant, auth.RoleAdmin))
	if rec.Code != http.StatusOK {
		t.Fatalf("tenant-admin PUT fail_open = %d %s", rec.Code, rec.Body.String())
	}

	found := false
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		walker, ok := sc.Audit().(store.CanonicalWalker)
		if !ok {
			return errors.New("audit log does not expose canonical walk")
		}
		return walker.WalkCanonical(context.Background(), 0, func(ev model.AuditEvent, meta string, _ []byte) error {
			if ev.Action == "inferenceproxy.config.put" {
				found = ev.Actor == "user:u1" && ev.ActorKind == "user" && strings.Contains(meta, `"fail_open":true`)
			}
			return nil
		})
	}); err != nil {
		t.Fatalf("walk audit: %v", err)
	}
	if !found {
		t.Fatal("fail_open mutation did not emit an attributable audit event with fail_open=true")
	}
}

func TestConfigCeilingsRoundTripAndPolicy(t *testing.T) {
	m, st, tenant := newPolicyHarness(t)
	body := `{"ceilings_enforce":true,"ceiling_max_tokens":1000,"ceiling_max_tool_uses":3,"ceiling_task_budget_tokens":30000}`
	var putOut configDTO
	if code := putConfig(t, m, st, tenant, body, &putOut); code != http.StatusOK {
		t.Fatalf("PUT config status = %d, want 200", code)
	}
	if putOut.CeilingsEnforce == nil || !*putOut.CeilingsEnforce ||
		putOut.CeilingMaxTokens != 1000 ||
		putOut.CeilingMaxToolUses != 3 ||
		putOut.CeilingTaskBudgetTokens != 30000 {
		t.Fatalf("PUT response ceilings = %+v", putOut)
	}

	var getOut configDTO
	if code := getConfig(t, m, st, tenant, &getOut); code != http.StatusOK {
		t.Fatalf("GET config status = %d, want 200", code)
	}
	if getOut.CeilingsEnforce == nil || !*getOut.CeilingsEnforce ||
		getOut.CeilingMaxTokens != 1000 ||
		getOut.CeilingMaxToolUses != 3 ||
		getOut.CeilingTaskBudgetTokens != 30000 {
		t.Fatalf("GET response ceilings = %+v", getOut)
	}

	pol, err := m.Policy(context.Background(), tenant)
	if err != nil {
		t.Fatalf("Policy: %v", err)
	}
	if pol.Ceilings != (RequestCeilings{Enforce: true, MaxTokens: 1000, MaxToolUses: 3, TaskBudgetTokens: 30000}) {
		t.Fatalf("Policy ceilings = %+v", pol.Ceilings)
	}
}

func TestConfigCeilingsDefaultsAndLegacyRows(t *testing.T) {
	m, st, tenant := newPolicyHarness(t)
	pol, err := m.Policy(context.Background(), tenant)
	if err != nil {
		t.Fatalf("Policy with no config row: %v", err)
	}
	if pol.Ceilings.Any() || pol.Ceilings.Enforce {
		t.Fatalf("no-row ceilings = %+v, want zero observe", pol.Ceilings)
	}

	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(configKind)
		if err != nil {
			return err
		}
		_, err = repo.Create(context.Background(), legacyConfigFields("legacy"))
		return err
	}); err != nil {
		t.Fatalf("create legacy config row: %v", err)
	}
	pol, err = m.Policy(context.Background(), tenant)
	if err != nil {
		t.Fatalf("Policy with legacy config row: %v", err)
	}
	if pol.Ceilings.Any() || pol.Ceilings.Enforce {
		t.Fatalf("legacy-row ceilings = %+v, want zero observe", pol.Ceilings)
	}
}

func TestConfigCeilingValidation(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
		msg  string
	}{
		{"negative ceiling", `{"ceiling_max_tokens":-1}`, http.StatusBadRequest, "greater than or equal to zero"},
		{"enforce without ceilings", `{"ceilings_enforce":true}`, http.StatusBadRequest, "requires at least one ceiling"},
		{"task budget below floor", `{"ceiling_task_budget_tokens":19999}`, http.StatusBadRequest, "at least 20000"},
		{"task budget at floor", `{"ceiling_task_budget_tokens":20000}`, http.StatusOK, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, st, tenant := newPolicyHarness(t)
			var raw map[string]any
			code, body := putConfigRaw(t, m, st, tenant, tc.body)
			if code != tc.want {
				t.Fatalf("status = %d body=%s, want %d", code, body, tc.want)
			}
			if code == http.StatusBadRequest && tc.msg != "" && !strings.Contains(body, tc.msg) {
				t.Fatalf("body %q does not contain %q", body, tc.msg)
			}
			if code == http.StatusOK {
				if err := json.Unmarshal([]byte(body), &raw); err != nil {
					t.Fatalf("decode accepted body: %v", err)
				}
			}
		})
	}
}

func TestNormalizeResponseModeDefaultsToBuffer(t *testing.T) {
	for _, in := range []string{"", "weird", "FLAG"} {
		if got := normalizeResponseMode(in); got != ResponseDLPBuffer {
			t.Errorf("normalizeResponseMode(%q) = %q, want buffer", in, got)
		}
	}
	for _, in := range []string{ResponseDLPOff, ResponseDLPFlag, ResponseDLPBuffer} {
		if got := normalizeResponseMode(in); got != in {
			t.Errorf("normalizeResponseMode(%q) = %q, want unchanged", in, got)
		}
	}
}

func newPolicyHarness(t *testing.T) (*Module, store.Store, model.TenantID) {
	t.Helper()
	m := New()
	ctx := context.Background()
	st, err := coreengine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, m.RegisterSchema)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, e := sys.EnsureSystemTenant(ctx); e != nil {
			return e
		}
		org, e := sys.CreateOrg(ctx, model.Org{Name: "acme", Slug: "acme", Status: model.StatusActive})
		if e != nil {
			return e
		}
		tenant = org.TenantID
		return nil
	}); err != nil {
		t.Fatalf("provision tenant: %v", err)
	}
	m.UseData(api.NewModuleData(st))
	return m, st, tenant
}

func putConfig(t *testing.T, m *Module, st store.Store, tenant model.TenantID, body string, out *configDTO) int {
	t.Helper()
	code, raw := putConfigRaw(t, m, st, tenant, body)
	if code == http.StatusOK && out != nil {
		if err := json.Unmarshal([]byte(raw), out); err != nil {
			t.Fatalf("decode PUT response: %v", err)
		}
	}
	return code
}

func putConfigRaw(t *testing.T, m *Module, st store.Store, tenant model.TenantID, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/config", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	m.handlePutConfig(rec, req, policyModuleContext(st, tenant))
	res := rec.Result()
	return res.StatusCode, rec.Body.String()
}

func getConfig(t *testing.T, m *Module, st store.Store, tenant model.TenantID, out *configDTO) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/config", nil)
	rec := httptest.NewRecorder()
	m.handleGetConfig(rec, req, policyModuleContext(st, tenant))
	res := rec.Result()
	if res.StatusCode == http.StatusOK && out != nil {
		if err := json.NewDecoder(rec.Body).Decode(out); err != nil {
			t.Fatalf("decode GET response: %v", err)
		}
	}
	return res.StatusCode
}

func policyModuleContext(st store.Store, tenant model.TenantID) api.ModuleContext {
	return policyModuleContextRole(st, tenant, auth.RoleEditor)
}

func policyModuleContextRole(st store.Store, tenant model.TenantID, role string) api.ModuleContext {
	return policyModuleContextAAL(st, tenant, role, auth.AAL3)
}

func policyModuleContextAAL(st store.Store, tenant model.TenantID, role string, aal int) api.ModuleContext {
	principal := auth.ScopedPrincipal(model.ID("u1"), "user one", tenant, role)
	principal.Kind = auth.KindUser
	principal.UserID = model.ID("u1")
	principal.AAL = aal
	return api.ModuleContext{
		Principal: principal,
		Tenant:    tenant,
		Data:      api.NewScopedData(st, tenant),
	}
}

func legacyConfigFields(updatedBy string) model.Record {
	return model.Record{
		colFailOpen:        false,
		colResponseDLPMode: ResponseDLPFlag,
		colRecordMandatory: false,
		colGateModelAccess: true,
		colGateBudget:      true,
		colGateResidency:   true,
		colGateContextWin:  true,
		colGateDLPRequest:  true,
		colGateDLPResponse: true,
		colUpdatedBy:       updatedBy,
	}
}
