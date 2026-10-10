// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package skills_test

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/skills"
)

type bindingAuthority struct {
	skills.TargetAuthority
	skills.AssignmentAuthority
	refs  map[string]api.EntityRef
	calls int
}

func (a *bindingAuthority) AssignmentTargetRefs() map[string]api.EntityRef {
	a.calls++
	return a.refs
}

type assignmentRecorder struct {
	count  int
	entity bool
	ref    api.EntityRef
	perm   auth.Permission
}

func (r *assignmentRecorder) Handle(method, path string, perm auth.Permission, _ api.ModuleHandler) {
	if method == "POST" && path == "/assignments" {
		r.count++
		r.perm = perm
	}
}

func (r *assignmentRecorder) HandleEntity(method, path string, perm auth.Permission, ref api.EntityRef, h api.ModuleHandler) {
	r.Handle(method, path, perm, h)
	if method == "POST" && path == "/assignments" {
		r.entity, r.ref = true, ref
	}
}

func TestAssignmentReferencesBoundBeforeRouteRegistration(t *testing.T) {
	for _, viaNew := range []bool{true, false} {
		t.Run(map[bool]string{true: "New", false: "UseTargets"}[viaNew], func(t *testing.T) {
			refs := map[string]api.EntityRef{"workspace": {CoreKind: api.CoreKindWorkspace, BodyIDField: "target_id", ConcealDeniedAsNotFound: true}}
			a := &bindingAuthority{refs: refs}
			m := skills.New(skills.Options{})
			if viaNew {
				m = skills.New(skills.Options{Targets: a})
			} else {
				m.UseTargets(a, nil)
			}
			if a.calls != 1 {
				t.Fatalf("binding getter calls = %d, want 1 before registration", a.calls)
			}
			for i := 0; i < 2; i++ {
				r := &assignmentRecorder{}
				m.APIRoutes(r)
				if r.count != 1 || !r.entity || r.perm != "skills:assignment:write" || r.ref.BodyKindField != "target_kind" || !r.ref.ConcealDeniedAsNotFound || !reflect.DeepEqual(r.ref.BodyKinds, refs) {
					t.Fatalf("entity assignment registration: %+v", r)
				}
			}
			if a.calls != 1 {
				t.Fatalf("registration invoked getter: %d calls", a.calls)
			}
			m.UseTargets(workspaceAuthority{}, nil)
			r := &assignmentRecorder{}
			m.APIRoutes(r)
			if r.count != 1 || r.entity || r.perm != "skills:assignment:write" {
				t.Fatalf("collection fallback after rebinding: %+v", r)
			}
		})
	}
}

func TestEmptyAssignmentReferencesKeepEntityDoor(t *testing.T) {
	m := skills.New(skills.Options{Targets: &bindingAuthority{}})
	r := &assignmentRecorder{}
	m.APIRoutes(r)
	if r.count != 1 || !r.entity || r.ref.BodyKindField != "target_kind" || !r.ref.ConcealDeniedAsNotFound {
		t.Fatalf("empty authority allowlist must retain entity authorization: %+v", r)
	}
}

type assignmentWorkspaceResolver struct{ store.Store }

type assignmentWriteForbid struct{}

func (assignmentWriteForbid) Scoped(_ context.Context, req auth.Request) (auth.ScopedDecision, error) {
	if req.Permission == "skills:assignment:write" {
		return auth.ScopedDecision{Effect: auth.EffectForbid, Class: auth.ClassPolicy}, nil
	}
	return auth.ScopedDecision{}, nil
}

func (r assignmentWorkspaceResolver) ResolveCoreEntity(ctx context.Context, tenant model.TenantID, kind api.CoreKind, id model.ID) (api.CoreEntityFacts, error) {
	if kind != api.CoreKindWorkspace {
		return api.CoreEntityFacts{}, store.ErrNotFound
	}
	facts := api.CoreEntityFacts{ID: id, Tenant: tenant}
	err := r.View(ctx, tenant, func(sc store.Scope) error {
		row, err := sc.Workspaces().Get(ctx, id)
		if err == nil {
			facts.WorkspaceID, facts.Exists = row.ID, true
		}
		return err
	})
	if errors.Is(err, store.ErrNotFound) {
		err = nil
	}
	return facts, err
}

func TestAssignmentAuthorityRegistersThroughAPI(t *testing.T) {
	concealed := api.EntityRef{CoreKind: api.CoreKindWorkspace, BodyIDField: "target_id", ConcealDeniedAsNotFound: true}
	disclosed := concealed
	disclosed.DeniedReadPermission = "tenant:read"
	for _, tc := range []struct {
		name string
		refs map[string]api.EntityRef
	}{
		{"fully_concealed", map[string]api.EntityRef{"workspace": concealed}},
		{"readable", map[string]api.EntityRef{"workspace": disclosed}},
		{"mixed", map[string]api.EntityRef{"workspace": concealed, "readable_workspace": disclosed}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := catalog(t)
			m := skills.New(skills.Options{Targets: &bindingAuthority{refs: tc.refs}})
			m.UseData(api.NewModuleData(h.store))
			_, key, err := ed25519.GenerateKey(nil)
			if err != nil {
				t.Fatal(err)
			}
			signer, err := audit.NewSigner(key)
			if err != nil {
				t.Fatal(err)
			}
			srv, err := api.New(api.Options{Store: h.store, Authenticator: auth.NewAuthenticator(h.store, nil), Authorizer: auth.NewAuthorizer(nil, auth.WithScopedGrants(assignmentWriteForbid{})), Signer: signer, SetupToken: secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token")), Modules: []api.Module{m}, CoreEntityResolver: assignmentWorkspaceResolver{h.store}})
			if err != nil {
				t.Fatal(err)
			}
			h.server = srv.Handler()
			created := h.request("POST", "/v1/users", map[string]string{"email": "viewer@example.test", "password": "fixture-viewer1", "tenant": h.tenant.String(), "role": "viewer"}, "")
			if created.Code != http.StatusCreated {
				t.Fatalf("viewer: %d %s", created.Code, created.Body.String())
			}
			login := h.request("POST", "/v1/auth/login", map[string]string{"email": "viewer@example.test", "password": "fixture-viewer1"}, "")
			var session struct {
				Token string `json:"token"`
			}
			if err := json.Unmarshal(login.Body.Bytes(), &session); err != nil || login.Code != http.StatusOK || session.Token == "" {
				t.Fatalf("viewer login: status=%d err=%v", login.Code, err)
			}
			viewer := h
			viewer.token = session.Token
			target := h.workspace(t, false)
			for kind := range tc.refs {
				body := map[string]string{"target_kind": kind, "target_id": model.NewID().String(), "pack_revision_id": model.NewID().String()}
				response := viewer.request("POST", "/v1/m/skills/assignments", body, "")
				if response.Code != http.StatusNotFound {
					t.Fatalf("missing %s target: %d %s", kind, response.Code, response.Body.String())
				}
				body["target_id"] = target.ID
				response = viewer.request("POST", "/v1/m/skills/assignments", body, "")
				want := http.StatusNotFound
				if tc.refs[kind].DeniedReadPermission != "" {
					want = http.StatusForbidden
				}
				if response.Code != want {
					t.Fatalf("readable %s target: %d %s, want %d", kind, response.Code, response.Body.String(), want)
				}
				response = h.request("POST", "/v1/m/skills/assignments", body, "")
				if response.Code != http.StatusNotFound {
					t.Fatalf("role-granted %s target write forbid: %d %s, want concealed 404", kind, response.Code, response.Body.String())
				}
			}
		})
	}
}
