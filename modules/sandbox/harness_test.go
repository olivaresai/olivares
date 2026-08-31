// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sandbox

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

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
)

// fakeScorer is a LOCAL, deterministic test scorer (it does NOT import the evals
// module — the cross-module adapter lives in the composition root). It passes
// a step iff its output contains the wanted marker; the suite score is the pass-rate.
// It is the test-only stand-in for the XII-backed adapter the parent wires in
// production.
type fakeScorer struct {
	want string // an output containing this marker passes
}

func (s fakeScorer) Score(_ context.Context, _ model.TenantID, req ScoreRequest) (ScoreVerdict, error) {
	v := ScoreVerdict{Total: len(req.Outputs)}
	for _, out := range req.Outputs {
		if strings.Contains(out, s.want) {
			v.PassedN++
		} else {
			v.FailedN++
		}
	}
	if v.Total > 0 {
		v.PassRate = float64(v.PassedN) / float64(v.Total)
	}
	v.Score = v.PassRate
	v.Passed = v.FailedN == 0 && v.Total > 0
	return v, nil
}

// fakeHistory is a LOCAL, deterministic timeline source: it returns a fixed ordered
// step list for any session ref, so replay determinism is testable without a real
// sessions-timeline adapter.
type fakeHistory struct {
	steps []ReplayStep
}

func (h fakeHistory) Timeline(_ context.Context, _ model.TenantID, _ string) ([]ReplayStep, error) {
	return h.steps, nil
}

type harness struct {
	t        *testing.T
	srv      *api.Server
	st       store.Store
	setupTok string
}

// newHarness builds a sandbox plane. Any non-nil option (runner/scorer/history) is
// wired; nil leaves the module default (in-proc-mock runner, unscored scorer, core
// history that yields zero steps).
func newHarness(t *testing.T, opts ...Option) *harness {
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
	rt := runtime.New(runtime.Options{Bus: bus})
	if err := rt.AddModule(mod, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Stop(ctx); _ = bus.Close() })

	_, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := audit.NewSigner(priv)
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
	return h
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

// createScenario registers a scenario with steps + mocks and returns its id.
func (h *harness) createScenario(token string, tenant model.TenantID, name string, steps []map[string]any, mocks []map[string]any) string {
	h.t.Helper()
	r := h.do("POST", "/v1/m/sandbox/scenarios", token, map[string]any{
		"name": name, "subject_kind": "agent", "steps": steps, "mocks": mocks,
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		h.t.Fatalf("create scenario = %d %s", r.code, r.raw)
	}
	return r.body["id"].(string)
}

func intOf(v any) int {
	if f, ok := v.(float64); ok {
		return int(f)
	}
	return 0
}
