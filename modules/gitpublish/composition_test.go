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
	"time"

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
	st     store.Store
	authr  *auth.Authenticator
}

// Ordinary HTTP requests have no deadline. Exercise all admissions with the
// serving authenticator and production authority, rather than fakeAuthority.
func TestProductionIntentAdmissionWithoutRequestDeadline(t *testing.T) {
	s := newServer(t)
	requirePasskeyStepUp(t, s.st)
	if code, _, raw := s.do("POST", "/v1/setup", "", map[string]any{"token": s.setup, "email": "root@x.io", "password": "supersecret1"}, ""); code != http.StatusCreated {
		t.Fatalf("setup = %d %s", code, raw)
	}
	root := s.login("root@x.io", "supersecret1")
	s.stepUp(root)
	code, out, raw := s.do("POST", "/v1/system/orgs", root, map[string]any{"name": "acme", "slug": "acme"}, "")
	if code != http.StatusCreated {
		t.Fatalf("org = %d %s", code, raw)
	}
	s.tenant = model.TenantID(out["tenant_id"].(string))
	admin, viewer := s.member(root, "admin"), s.member(root, "viewer")
	s.stepUp(admin)
	code, out, raw = s.do("POST", "/v1/m/gitpublish/targets", admin, map[string]any{
		"workspace_id": model.NewID().String(), "credential_binding_id": "cb1", "repository_binding_id": "rb1", "push_prefix": "olivares/", "merge_bases": []string{"main"},
	}, s.tenant)
	if code != http.StatusCreated {
		t.Fatalf("target = %d %s", code, raw)
	}
	target := out["id"].(string)
	base := "/v1/m/gitpublish/"
	push := func(op string) string {
		t.Helper()
		code, out, raw := s.do("POST", base+"targets/"+target+"/pushes", admin, map[string]any{
			"operation_id": op, "ref": "refs/heads/olivares/" + op, "commit": shaCommit, "tree": shaTree,
		}, s.tenant)
		if code != http.StatusOK && code != http.StatusAccepted {
			t.Fatalf("push = %d %s", code, raw)
		}
		return out["id"].(string)
	}
	id := push("applied")
	readPaths := []string{"intents?target_id=" + target, "intents/" + id, "intents/" + id + "/observations"}
	for _, path := range readPaths {
		t.Run(path, func(t *testing.T) {
			code, out, raw := s.do("GET", base+path, admin, nil, s.tenant)
			if code != http.StatusOK {
				t.Fatalf("read = %d %s", code, raw)
			}
			if path == "intents/"+id {
				if out["id"] != id || out["state"] != StateApplied {
					t.Fatalf("intent = %s", raw)
				}
			} else if items, ok := out["items"].([]any); !ok || len(items) == 0 {
				t.Fatalf("stored records missing: %s", raw)
			}
		})
	}
	t.Run("admin binding IDs", func(t *testing.T) {
		code, out, raw := s.do("GET", base+"targets/"+target, admin, nil, s.tenant)
		if code != http.StatusOK || out["credential_binding_id"] != "cb1" || out["repository_binding_id"] != "rb1" {
			t.Fatalf("admin target = %d %s", code, raw)
		}
	})
	t.Run("viewer binding IDs concealed", func(t *testing.T) {
		code, out, raw := s.do("GET", base+"targets/"+target, viewer, nil, s.tenant)
		if code != http.StatusOK || out["credential_binding_id"] != nil || out["repository_binding_id"] != nil {
			t.Fatalf("viewer target = %d %s", code, raw)
		}
	})
	t.Run("settled state", func(t *testing.T) {
		if code, out, raw := s.do("POST", base+"intents/"+id+"/reconcile", admin, nil, s.tenant); code != http.StatusOK || out["state"] != StateApplied {
			t.Errorf("settled reconcile = %d %s", code, raw)
		}
		if code, _, raw := s.do("POST", base+"intents/"+id+"/abandon", admin, map[string]any{"reason": "settled"}, s.tenant); code != http.StatusConflict {
			t.Errorf("settled abandon = %d %s", code, raw)
		}
	})
	// A timed-out write stays uncertain while the host has not observed it.
	s.m.opts.DispatchTimeout = 20 * time.Millisecond
	s.git.hold = make(chan struct{})
	defer close(s.git.hold)
	unresolved := push("unresolved")
	t.Run("uncertain reconcile and abandon", func(t *testing.T) {
		path := base + "intents/" + unresolved
		if code, out, raw := s.do("POST", path+"/reconcile", admin, nil, s.tenant); code != http.StatusAccepted || out["state"] != StateUncertain {
			t.Errorf("uncertain reconcile = %d %s", code, raw)
		}
		if code, out, raw := s.do("POST", path+"/abandon", admin, map[string]any{"reason": "host outage"}, s.tenant); code != http.StatusOK || out["state"] != StateAbandoned || out["reason"] != "host outage" {
			t.Errorf("uncertain abandon = %d %s", code, raw)
		}
	})
	t.Run("viewer cannot reconcile or abandon", func(t *testing.T) {
		for _, action := range []string{"reconcile", "abandon"} {
			code, _, raw := s.do("POST", base+"intents/"+unresolved+"/"+action, viewer, map[string]any{}, s.tenant)
			if code != http.StatusForbidden && code != http.StatusNotFound {
				t.Errorf("viewer %s = %d %s", action, code, raw)
			}
		}
	})
	t.Run("token cannot step up", func(t *testing.T) {
		code, out, raw := s.do("POST", "/v1/tokens", root, map[string]any{"name": "publication", "tenant": s.tenant.String(), "role": "admin"}, "")
		if code != http.StatusCreated {
			t.Fatalf("token = %d %s", code, raw)
		}
		code, _, raw = s.do("POST", base+"intents/"+unresolved+"/abandon", out["token"].(string), map[string]any{}, s.tenant)
		if code != http.StatusForbidden {
			t.Fatalf("token abandon = %d %s", code, raw)
		}
	})
	t.Run("wrong tenant", func(t *testing.T) {
		code, out, raw := s.do("POST", "/v1/system/orgs", root, map[string]any{"name": "other", "slug": "other"}, "")
		if code != http.StatusCreated {
			t.Fatalf("other org = %d %s", code, raw)
		}
		other := model.TenantID(out["tenant_id"].(string))
		for _, path := range append(readPaths, "targets/"+target) {
			code, _, raw := s.do("GET", base+path, root, nil, other)
			if code != http.StatusNotFound {
				t.Errorf("wrong tenant %s = %d %s", path, code, raw)
			}
		}
	})
	if code, _, raw := s.do("POST", "/v1/auth/logout", admin, map[string]any{}, ""); code >= 300 {
		t.Fatalf("logout = %d %s", code, raw)
	}
	t.Run("revoked credential", func(t *testing.T) {
		for _, path := range append(readPaths, "targets/"+target) {
			if code, _, raw := s.do("GET", base+path, admin, nil, s.tenant); code != http.StatusUnauthorized {
				t.Errorf("revoked %s = %d %s", path, code, raw)
			}
		}
	})
	if s.git.count() != 2 || s.host.mints != s.host.releases {
		t.Fatalf("unexpected effects: dispatches=%d mints=%d releases=%d", s.git.count(), s.host.mints, s.host.releases)
	}
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
	return &server{t: t, srv: srv, m: m, host: host, git: g, setup: plaintext, st: st, authr: authr}
}

// requirePasskeyStepUp turns on the strictest administrative step-up policy
// (passkey), the behavior before the policy existed. The default (none) is
// covered in core/api.
func requirePasskeyStepUp(t *testing.T, st store.Store) {
	t.Helper()
	ctx := context.Background()
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		_, err := as.AuthPolicy().Create(ctx, model.AuthPolicy{AdminStepUp: auth.StepUpPasskey})
		return err
	}); err != nil {
		t.Fatalf("require passkey step-up: %v", err)
	}
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

// stepUp elevates a session as a verified passkey ceremony would.
func (s *server) stepUp(token string) {
	s.t.Helper()
	p, err := s.authr.Authenticate(context.Background(), token)
	if err != nil {
		s.t.Fatalf("authenticate for step-up: %v", err)
	}
	if _, err := s.authr.ElevateSession(context.Background(), p, "webauthn", auth.AAL3); err != nil {
		s.t.Fatalf("elevate session: %v", err)
	}
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
	requirePasskeyStepUp(t, s.st)
	if code, _, raw := s.do("POST", "/v1/setup", "", map[string]any{"token": s.setup, "email": "root@x.io", "password": "supersecret1"}, ""); code != http.StatusCreated {
		t.Fatalf("setup = %d %s", code, raw)
	}
	root := s.login("root@x.io", "supersecret1")
	// Adding a person asks for the deployment's step-up: the
	// administrator who adds the members steps up; the members under test do not.
	s.stepUp(root)
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
