// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package voice

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/eventbus"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// fakeGate is a programmable ApprovalGate for the open tests.
type fakeGate struct {
	status    GateStatus
	planHash  string
	emptyHash bool // if set, Status echoes an EMPTY plan hash (a partial/buggy gate)
}

func (g fakeGate) Request(_ context.Context, req ApprovalRequest) (GateDecision, error) {
	return GateDecision{ApprovalRef: "appr-1", Status: g.status, PlanHash: req.PlanHash}, nil
}

func (g fakeGate) Status(_ context.Context, _ model.TenantID, approvalRef, planHash string) (GateDecision, error) {
	ph := planHash
	switch {
	case g.emptyHash:
		ph = ""
	case g.planHash != "":
		ph = g.planHash
	}
	return GateDecision{ApprovalRef: approvalRef, Status: g.status, PlanHash: ph}, nil
}

// fakeDispatcher is a programmable Dispatcher for the open tests.
type fakeDispatcher struct {
	ref string
	err error
}

func (d fakeDispatcher) Open(_ context.Context, _ OpenRequest) (OpenResult, error) {
	if d.err != nil {
		return OpenResult{}, d.err
	}
	return OpenResult{Ref: d.ref}, nil
}

// fakeBudgetGate is a programmable BudgetGate for the FIN-08 enforcement tests.
type fakeBudgetGate struct {
	decision BudgetDecision
	err      error
}

func (g fakeBudgetGate) Check(_ context.Context, _ model.TenantID, _ BudgetDims) (BudgetDecision, error) {
	return g.decision, g.err
}

// recordingDispatcher records whether Open was ever called, so a test can prove the
// budget pre-flight denied an open BEFORE it reached the dispatcher.
type recordingDispatcher struct{ opened *bool }

func (d recordingDispatcher) Open(_ context.Context, _ OpenRequest) (OpenResult, error) {
	*d.opened = true
	return OpenResult{Ref: "should-not-actuate"}, nil
}

type harness struct {
	t        *testing.T
	srv      *api.Server
	st       store.Store
	bus      eventbus.Bus
	setupTok string

	findMu   sync.Mutex
	findings []sdkmodel.FindingReport
}

func newHarness(t *testing.T, opts ...Option) (*harness, *Module) {
	t.Helper()
	ctx := context.Background()
	h := &harness{t: t}

	mod := New(opts...)
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, mod.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	h.st = st
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(sys store.SystemScope) error { _, e := sys.EnsureSystemTenant(ctx); return e }); err != nil {
		t.Fatal(err)
	}
	mod.UseData(api.NewModuleData(st))

	bus := eventbus.NewInProc(eventbus.Options{})
	h.bus = bus
	if _, err := bus.Subscribe([]event.Type{event.TypeFindingReported}, func(_ context.Context, e event.Event) error {
		if f, ok := event.FindingOf(e); ok {
			h.findMu.Lock()
			h.findings = append(h.findings, f)
			h.findMu.Unlock()
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rt := runtime.New(runtime.Options{Bus: bus})
	if err := rt.AddModule(mod, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Stop(ctx); _ = bus.Close() })

	_, priv, _ := ed25519.GenerateKey(nil)
	signer, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatal(err)
	}
	tok := secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token"))
	plaintext, _, err := tok.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	srv, err := api.New(api.Options{
		Store: st, Authenticator: auth.NewAuthenticator(st, nil), Authorizer: auth.NewAuthorizer(nil),
		Signer: signer, SetupToken: tok, Version: "test", Modules: []api.Module{mod},
	})
	if err != nil {
		t.Fatal(err)
	}
	h.srv, h.setupTok = srv, plaintext
	return h, mod
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

// publishTelemetry emits a voice.telemetry.observed event with an arbitrary payload
// (a map, so a test can include a forbidden key to prove it is dropped).
func (h *harness) publishTelemetry(tenant model.TenantID, payload map[string]any) {
	h.t.Helper()
	_ = h.bus.Publish(context.Background(), event.Event{
		Type: TypeVoiceTelemetry, Tenant: tenant.String(), Source: "probe:test", Time: time.Now(), Payload: payload,
	})
}

func (h *harness) setPolicy(token string, tenant model.TenantID, agent, mdl, prov string, maxLatency int64) {
	h.t.Helper()
	body := map[string]any{"agent_ref": agent, "allowed_model_ref": mdl, "allowed_provider_ref": prov, "max_latency_ms": maxLatency}
	if r := h.do("PUT", "/v1/m/voice/policies", token, body, tenantHdr(tenant)); r.code != http.StatusOK {
		h.t.Fatalf("set policy = %d %s", r.code, r.raw)
	}
}

func (h *harness) getSession(token string, tenant model.TenantID, ref string) (sessionDTO, int) {
	h.t.Helper()
	r := h.do("GET", "/v1/m/voice/sessions/"+ref, token, nil, tenantHdr(tenant))
	var s sessionDTO
	if r.code == http.StatusOK {
		_ = json.Unmarshal([]byte(r.raw), &s)
	}
	return s, r.code
}

func (h *harness) waitForSession(token string, tenant model.TenantID, ref string) sessionDTO {
	h.t.Helper()
	for i := 0; i < 200; i++ {
		if s, code := h.getSession(token, tenant, ref); code == http.StatusOK {
			return s
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.t.Fatalf("session %s never appeared", ref)
	return sessionDTO{}
}

func (h *harness) waitForFinding(kind string) bool {
	h.t.Helper()
	for i := 0; i < 200; i++ {
		h.findMu.Lock()
		for _, f := range h.findings {
			if f.Kind == kind {
				h.findMu.Unlock()
				return true
			}
		}
		h.findMu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	return false
}
