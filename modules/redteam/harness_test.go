// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package redteam

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

// fakeSandbox is a deterministic test sandbox: for any probe whose family is in
// comply, the simulated target ECHOES the forbidden marker (a failure); for every
// other probe it refuses (a pass). It uses the module's own exported Judge so the
// judgement matches production.
type fakeSandbox struct {
	comply map[string]bool
}

func (s fakeSandbox) Execute(_ context.Context, _ model.TenantID, _ Target, p Probe) (ProbeResult, error) {
	if s.comply[p.Family] {
		resp := "Sure, here you go."
		if len(p.MustNotContain) > 0 {
			resp = "Sure: " + p.MustNotContain[0]
		}
		return Judge(p, resp), nil
	}
	return Judge(p, "I'm sorry, I can't help with that."), nil
}

type harness struct {
	t        *testing.T
	srv      *api.Server
	st       store.Store
	setupTok string

	findMu   sync.Mutex
	findings []sdkmodel.FindingReport
}

// newHarness builds a red-team plane. A nil sandbox uses the module default (offline
// → degraded runs); a non-nil sandbox is wired via WithSandbox.
func newHarness(t *testing.T, sb Sandbox) *harness {
	t.Helper()
	ctx := context.Background()
	h := &harness{t: t}

	var opts []Option
	if sb != nil {
		opts = append(opts, WithSandbox(sb))
	}
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

// seedAgent creates an agent in the tenant's inventory so a red-team target's
// agent_ref resolves (R4 ownership). Mirrors how the inventory module materializes
// an agent (inventory/entities.go foAgent): Name+ExternalID = the ref, Kind unknown.
func (h *harness) seedAgent(tenant model.TenantID, externalID string) model.ID {
	h.t.Helper()
	var id model.ID
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		a, err := sc.Agents().Create(context.Background(), model.Agent{
			Name: externalID, Kind: "unknown", ExternalID: externalID, Status: model.StatusActive,
		})
		id = a.ID
		return err
	}); err != nil {
		h.t.Fatalf("seed agent %q: %v", externalID, err)
	}
	return id
}

// deleteAgent removes an agent from the tenant inventory (to test the launch-time
// ownership re-check, R4).
func (h *harness) deleteAgent(tenant model.TenantID, id model.ID) {
	h.t.Helper()
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		return sc.Agents().Delete(context.Background(), id)
	}); err != nil {
		h.t.Fatalf("delete agent %s: %v", id, err)
	}
}

// registerAuthorizedTarget seeds the agent into the tenant inventory (so the
// ownership gate passes), registers a target for it and authorizes it (consent),
// returning the target id.
func (h *harness) registerAuthorizedTarget(admin string, tenant model.TenantID, agentRef string) string {
	h.t.Helper()
	h.seedAgent(tenant, agentRef)
	r := h.do("POST", "/v1/m/redteam/targets", admin, map[string]any{"agent_ref": agentRef, "name": agentRef}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		h.t.Fatalf("register target = %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)
	if ar := h.do("POST", "/v1/m/redteam/targets/"+id+"/authorize", admin, map[string]any{"authorized": true}, tenantHdr(tenant)); ar.code != http.StatusOK {
		h.t.Fatalf("authorize target = %d %s", ar.code, ar.raw)
	}
	return id
}

// coreFindings reads the persisted core findings of a kind directly from the store.
func (h *harness) coreFindings(tenant model.TenantID, kind string) []model.Finding {
	h.t.Helper()
	var out []model.Finding
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		fs, _, err := sc.Findings().List(context.Background(), model.Query{Filters: []model.Filter{{Column: "kind", Op: model.OpEq, Value: kind}}, Limit: 1000})
		out = fs
		return err
	}); err != nil {
		h.t.Fatalf("read findings: %v", err)
	}
	return out
}

func (h *harness) waitFindings() {
	time.Sleep(20 * time.Millisecond)
}
