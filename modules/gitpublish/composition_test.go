// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package gitpublish

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
)

// server mounts the module in a real api.Server with the serving
// Authenticator as the evidence producer and the production authority: the
// same pair the composition root will bind. Only the host, git and custody
// are fakes.
type server struct {
	t      *testing.T
	srv    *api.Server
	m      *Module
	host   *fakeHost
	git    *fakeGit
	setup  string
	tenant model.TenantID
}

func newServer(t *testing.T) *server {
	t.Helper()
	ctx := context.Background()
	host := newFakeHost()
	g := &fakeGit{host: host, treeFor: map[string]string{shaCommit: shaTree, shaBase: shaTree}}
	custody := &fakeCustody{host: host, cbVer: 1, rbVer: 1, owners: []string{"acme"}, approved: map[string]bool{"cb1": true, "rb1": true}}
	m := New(Options{Custody: custody, Git: g})
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, m.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(sys store.SystemScope) error { _, e := sys.EnsureSystemTenant(ctx); return e }); err != nil {
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
	authz := auth.NewAuthorizer(nil)
	srv, err := api.New(api.Options{
		Store: st, Authenticator: authr, Authorizer: authz, PrincipalEvidenceProducer: authr,
		Signer: signer, SetupToken: tok, Version: "test", Modules: []api.Module{m},
	})
	if err != nil {
		t.Fatalf("api.New with the module's governed routes: %v", err)
	}
	m.UseData(api.NewModuleData(st))
	m.UseAuthority(authr, authz)
	return &server{t: t, srv: srv, m: m, host: host, git: g, setup: plaintext}
}

func (s *server) do(method, path, token string, body any, tenant model.TenantID) (int, map[string]any, string) {
	s.t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(method, path, bytes.NewReader(b))
	req.RemoteAddr = "10.0.0.1:1234"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if tenant != "" {
		req.Header.Set("X-Olivares-Tenant", tenant.String())
	}
	rec := httptest.NewRecorder()
	s.srv.Handler().ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec.Body.String()
}

func (s *server) login(email, password string) string {
	s.t.Helper()
	code, out, raw := s.do("POST", "/v1/auth/login", "", map[string]any{"email": email, "password": password}, "")
	if code != http.StatusOK {
		s.t.Fatalf("login = %d %s", code, raw)
	}
	return out["token"].(string)
}

func (s *server) member(root string, role string) string {
	s.t.Helper()
	email := role + "@acme.io"
	code, _, raw := s.do("POST", "/v1/users", root, map[string]any{"email": email, "password": "memberpass1", "tenant": s.tenant.String(), "role": role}, "")
	if code != http.StatusCreated {
		s.t.Fatalf("create user = %d %s", code, raw)
	}
	return s.login(email, "memberpass1")
}

func TestProductionCompositionThroughTheSealedDoor(t *testing.T) {
	s := newServer(t)
	if code, _, raw := s.do("POST", "/v1/setup", "", map[string]any{"token": s.setup, "email": "root@x.io", "password": "supersecret1"}, ""); code != http.StatusCreated {
		t.Fatalf("setup = %d %s", code, raw)
	}
	root := s.login("root@x.io", "supersecret1")
	code, out, raw := s.do("POST", "/v1/system/orgs", root, map[string]any{"name": "acme", "slug": "acme"}, "")
	if code != http.StatusCreated {
		t.Fatalf("org = %d %s", code, raw)
	}
	s.tenant = model.TenantID(out["tenant_id"].(string))
	admin := s.member(root, "admin")
	viewer := s.member(root, "viewer")

	// A target, seeded directly: creating one through the route needs AAL3.
	var target model.ID
	if err := s.m.data.Mutate(context.Background(), s.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(kindTarget)
		if err != nil {
			return err
		}
		rec, err := repo.Create(context.Background(), model.Record{"workspace_id": model.NewID().String(), "credential_binding": "cb1", "repository_binding": "rb1", "push_prefix": "olivares/", "merge_bases": "main", "created_by": "user:seed"})
		target = model.ID(rec.String(model.ColID))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	base := "/v1/m/gitpublish/targets/" + target.String()
	push := map[string]any{"operation_id": "op-1", "ref": "refs/heads/olivares/a", "expected_old": "", "commit": shaCommit, "tree": shaTree}

	// The door: target writes and merge need AAL3; a viewer cannot push.
	if code, _, raw := s.do("POST", "/v1/m/gitpublish/targets", admin, map[string]any{"workspace_id": model.NewID().String(), "credential_binding_id": "cb1", "repository_binding_id": "rb1", "push_prefix": "olivares/"}, s.tenant); code != http.StatusForbidden {
		t.Fatalf("target create at AAL1 = %d %s", code, raw)
	}
	if code, _, raw := s.do("POST", base+"/merges", admin, map[string]any{"operation_id": "m", "number": 1, "expected_head": shaCommit, "method": "merge"}, s.tenant); code != http.StatusForbidden {
		t.Fatalf("merge at AAL1 = %d %s", code, raw)
	}
	if code, _, raw := s.do("POST", base+"/pushes", viewer, push, s.tenant); code != http.StatusForbidden && code != http.StatusNotFound {
		t.Fatalf("viewer push = %d %s", code, raw)
	}
	if s.git.count() != 0 || s.host.mints != 0 {
		t.Fatal("a refused request reached the host")
	}
	// The exact admin session publishes through the production authority.
	code, out, raw = s.do("POST", base+"/pushes", admin, push, s.tenant)
	if code != http.StatusOK || out["state"] != StateApplied {
		t.Fatalf("admin push = %d %s", code, raw)
	}
	if s.git.count() != 1 || s.host.mints != s.host.releases {
		t.Fatalf("dispatches = %d mints = %d releases = %d", s.git.count(), s.host.mints, s.host.releases)
	}
	// The exact credential is revoked between admission and the claim: the
	// locked or re-validated authority refuses, and nothing is dispatched.
	s.m.beforeClaim = func() {
		s.m.beforeClaim = nil
		if code, _, raw := s.do("POST", "/v1/auth/logout", admin, map[string]any{}, ""); code >= 300 {
			t.Fatalf("logout = %d %s", code, raw)
		}
	}
	push["operation_id"], push["ref"] = "op-2", "refs/heads/olivares/b"
	code, _, raw = s.do("POST", base+"/pushes", admin, push, s.tenant)
	if code == http.StatusOK || s.git.count() != 1 {
		t.Fatalf("revoked credential = %d %s, dispatches = %d", code, raw, s.git.count())
	}
	if s.host.mints != s.host.releases {
		t.Fatalf("mints = %d releases = %d", s.host.mints, s.host.releases)
	}
}
