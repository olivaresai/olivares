// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/modules/sourcescope"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// fakeHost captures published events so the access-map permitted-edge projection can
// be asserted, and satisfies sdk.Host for the modules' Init.
type fakeHost struct {
	mu     sync.Mutex
	events []event.Event
}

func (h *fakeHost) Publish(_ context.Context, e event.Event) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, e)
	return nil
}
func (h *fakeHost) Subscribe([]event.Type, event.Handler) (func(), error) { return func() {}, nil }
func (h *fakeHost) Logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
func (h *fakeHost) Config() sdk.Config { return sdk.Config{} }

// edges returns the EdgeObservations published so far.
func (h *fakeHost) edges() []sdkmodel.EdgeObservation {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []sdkmodel.EdgeObservation
	for _, e := range h.events {
		if o, ok := event.EdgeOf(e); ok {
			out = append(out, o)
		}
	}
	return out
}

// harness wires a real store + API server with the governance module (for the
// scoped-grant engine + the pdp publish surface) and the sourcescope module (the
// binding write API), plus the production-equivalent Authorizer (deny-overlay + scoped
// grants) so cross-scope grants/forbids are exercised on the real path.
type harness struct {
	t        *testing.T
	srv      *api.Server
	st       store.Store
	authr    *auth.Authenticator
	gov      *governance.Module
	ss       *sourcescope.Module
	resolver *sourcescope.Resolver
	host     *fakeHost
	setupTok string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	return newHarnessOn(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true})
}

// newHarnessOn is newHarness against an arbitrary store config, so the same end-to-end HTTP
// path can be exercised on Postgres. The SQLite harness has a single writer, which is exactly
// what hides a READ COMMITTED race (E-5) — a concurrency defect measured only on the
// in-memory engine is a defect measured on the engine that cannot have it.
func newHarnessOn(t *testing.T, cfg store.Config) *harness {
	t.Helper()
	ctx := context.Background()
	host := &fakeHost{}
	gov := governance.New()
	ss := sourcescope.New(sourcescope.WithScopedAuthorizer(gov.ScopedGrants()))

	register := func(reg store.ExtensionRegistry) error {
		if err := gov.RegisterSchema(reg); err != nil {
			return err
		}
		return ss.RegisterSchema(reg)
	}
	st, err := engine.Open(ctx, cfg, register)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(sys store.SystemScope) error { _, e := sys.EnsureSystemTenant(ctx); return e }); err != nil {
		t.Fatal(err)
	}
	gov.UseData(api.NewModuleData(st))
	ss.UseData(api.NewModuleData(st))
	if err := gov.Init(ctx, host); err != nil {
		t.Fatal(err)
	}
	if err := ss.Init(ctx, host); err != nil {
		t.Fatal(err)
	}

	_, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := audit.NewSigner(priv)
	tok := secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token"))
	plaintext, _, err := tok.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	authr := auth.NewAuthenticator(st, nil)
	srv, err := api.New(api.Options{
		Store: st, Authenticator: authr,
		Authorizer: auth.NewAuthorizer(gov.RequestEvaluator(), auth.WithScopedGrants(gov.ScopedGrants())),
		Signer:     signer, SetupToken: tok, Version: "test",
		Modules: []api.Module{gov, ss},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, srv: srv, st: st, authr: authr, gov: gov, ss: ss, resolver: ss.Resolver(), host: host, setupTok: plaintext}
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

// principalFor logs in a user (creating it with an optional tenant membership) and
// returns its authenticated Principal. role=="" creates a CONFINED user — no membership
// in tenant — so RoleIn(tenant) is false and only the source-scope binding governs it.
func (h *harness) principalFor(admin string, tenant model.TenantID, email, role string) auth.Principal {
	h.t.Helper()
	r := h.do("POST", "/v1/users", admin, map[string]any{"email": email, "password": "memberpass1"}, nil)
	if r.code != http.StatusCreated {
		h.t.Fatalf("create user = %d %s", r.code, r.raw)
	}
	if role != "" {
		uid := r.body["id"].(string)
		if r := h.do("POST", "/v1/memberships", admin, map[string]any{"user_id": uid, "tenant": tenant.String(), "role": role}, nil); r.code != http.StatusCreated {
			h.t.Fatalf("grant membership = %d %s", r.code, r.raw)
		}
	}
	lr := h.do("POST", "/v1/auth/login", "", map[string]any{"email": email, "password": "memberpass1"}, nil)
	if lr.code != http.StatusOK {
		h.t.Fatalf("login %s = %d %s", email, lr.code, lr.raw)
	}
	p, err := h.authr.Authenticate(context.Background(), lr.body["token"].(string))
	if err != nil {
		h.t.Fatalf("authenticate %s: %v", email, err)
	}
	return p
}

// --- store seeding (scope entities + bindings) -------------------------------

func (h *harness) createWorkspace(tenant model.TenantID, slug string) model.ID {
	h.t.Helper()
	var id model.ID
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		ws, err := sc.Workspaces().Create(context.Background(), model.Workspace{Name: slug, Slug: slug, Status: model.StatusActive})
		id = ws.ID
		return err
	}); err != nil {
		h.t.Fatalf("create workspace %s: %v", slug, err)
	}
	return id
}

// createAgent creates an agent with ExternalID=name (the resolver loads agents by
// external id) scoped to ws (zero ⇒ default workspace).
func (h *harness) createAgent(tenant model.TenantID, name string, ws model.ID) model.Agent {
	h.t.Helper()
	var out model.Agent
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		a, err := sc.Agents().Create(context.Background(), model.Agent{
			Name: name, ExternalID: name, Kind: "claude-code", Status: model.StatusActive, WorkspaceID: ws,
		})
		out = a
		return err
	}); err != nil {
		h.t.Fatalf("create agent %s: %v", name, err)
	}
	return out
}

// createSession creates a session with ExternalID=ref scoped to ws and owned by agent.
func (h *harness) createSession(tenant model.TenantID, ref string, agentID, ws model.ID) model.Session {
	h.t.Helper()
	var out model.Session
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		s, err := sc.Sessions().Create(context.Background(), model.Session{
			ExternalID: ref, AgentID: agentID, WorkspaceID: ws,
		})
		out = s
		return err
	}); err != nil {
		h.t.Fatalf("create session %s: %v", ref, err)
	}
	return out
}

// createFolder creates a Resource folder (Kind "folder") under parent (zero ⇒ a tree
// root) in workspace ws, returning it. The materialized Path is store-computed (the
// tree), so a folder binding's subtree query and the ancestor walk are real.
func (h *harness) createFolder(tenant model.TenantID, name string, parent, ws model.ID) model.Resource {
	h.t.Helper()
	var out model.Resource
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		res, err := sc.Resources().CreateUnder(context.Background(), parent, model.Resource{
			Name: name, Kind: "folder", WorkspaceID: ws,
		})
		out = res
		return err
	}); err != nil {
		h.t.Fatalf("create folder %s: %v", name, err)
	}
	return out
}

// addAgentToGroup creates an agent-group (slug) and binds the agent to it.
func (h *harness) addAgentToGroup(tenant model.TenantID, agentID model.ID, groupSlug string, ws model.ID) {
	h.t.Helper()
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		grp, err := sc.AgentGroups().Create(context.Background(), model.AgentGroup{
			Name: groupSlug, Slug: groupSlug, Status: model.StatusActive, WorkspaceID: ws,
		})
		if err != nil {
			return err
		}
		_, err = sc.AgentGroupMembers().Create(context.Background(), model.AgentGroupMember{GroupID: grp.ID, AgentID: agentID})
		return err
	}); err != nil {
		h.t.Fatalf("add agent to group %s: %v", groupSlug, err)
	}
}

// publishGrant activates a tenant's authored Cedar grant policy via the real authoring
// surface (the admin holds governance:policy:admin in the tenant).
func (h *harness) publishGrant(admin string, tenant model.TenantID, src string) {
	h.t.Helper()
	r := h.do("POST", "/v1/m/governance/pdp/publish", admin, map[string]any{"engine": "cedar", "source": src}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		h.t.Fatalf("publish grant = %d %s", r.code, r.raw)
	}
}

// createBinding POSTs a source→scope binding through the module's write API.
func (h *harness) createBinding(token string, tenant model.TenantID, body map[string]any) resp {
	return h.do("POST", "/v1/m/sourcescope/bindings", token, body, tenantHdr(tenant))
}
