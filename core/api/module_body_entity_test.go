// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
)

const (
	bodyEntityKind       model.Kind      = "bodyentity.thing"
	bodyEntityPermission auth.Permission = "bodyentity:thing:write"
	bodyEntityIDField                    = "thing_id"
	bodyEntityWorkspace                  = "workspace_id"
	bodyEntityLabel                      = "label"
)

func registerBodyEntityTestDescriptor(reg store.ExtensionRegistry) error {
	return reg.Register(model.EntityDescriptor{
		Kind:  bodyEntityKind,
		Table: "bodyentity_thing",
		Fields: []model.FieldSpec{
			{Name: bodyEntityWorkspace, Kind: model.KindUUID, Indexed: true},
			{Name: bodyEntityLabel, Kind: model.KindText},
		},
		WorkspaceLineage: model.WorkspaceLineageSpec{
			Column: bodyEntityWorkspace, Encoding: model.WorkspaceLineageID,
			Unset: model.WorkspaceUnsetHidden,
		},
	})
}

type bodyEntityObservation struct {
	body     string
	resource auth.ResourceAttrs
}

type bodyEntityTestModule struct {
	mu           sync.Mutex
	observations []bodyEntityObservation
}

func (*bodyEntityTestModule) APINamespace() string { return "bodyentity" }

func (*bodyEntityTestModule) Permissions() []auth.Permission {
	return []auth.Permission{bodyEntityPermission}
}

func (m *bodyEntityTestModule) APIRoutes(reg api.RouteRegistrar) {
	bodyRef := api.EntityRef{
		Kind: bodyEntityKind, BodyIDField: bodyEntityIDField,
		WorkspaceColumn: bodyEntityWorkspace, ResourceKind: string(bodyEntityKind),
	}
	pathRef := api.EntityRef{
		Kind: bodyEntityKind, IDParam: "id",
		WorkspaceColumn: bodyEntityWorkspace, ResourceKind: string(bodyEntityKind),
		ConcealDeniedAsNotFound: true,
	}
	unknownRef := api.EntityRef{
		Kind: "bodyentity.unregistered", IDParam: "id",
		WorkspaceColumn: bodyEntityWorkspace, ResourceKind: string(bodyEntityKind),
		ConcealDeniedAsNotFound: true,
	}
	reg.HandleEntity("POST", "/body", bodyEntityPermission, bodyRef, m.handle)
	reg.HandleEntity("POST", "/path/{id}", bodyEntityPermission, pathRef, m.handle)
	reg.HandleEntity("POST", "/unknown/{id}", bodyEntityPermission, unknownRef, m.handle)
}

func (m *bodyEntityTestModule) handle(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body", http.StatusInternalServerError)
		return
	}
	m.mu.Lock()
	m.observations = append(m.observations, bodyEntityObservation{
		body: string(raw), resource: mc.Resource,
	})
	m.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"body":               string(raw),
		"resource_kind":      mc.Resource.Kind,
		"resource_id":        mc.Resource.ID,
		"resource_workspace": mc.Resource.WorkspaceID.String(),
	})
}

func (m *bodyEntityTestModule) snapshot() []bodyEntityObservation {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]bodyEntityObservation(nil), m.observations...)
}

// bodyEntityScopedGate makes the test caller depend entirely on the resource
// attributes presented by the route wrapper: viewer RBAC cannot grant :write.
type bodyEntityScopedGate struct {
	mu               sync.Mutex
	allowedWorkspace model.ID
	allowCollection  bool
	requests         []auth.Request
}

func (g *bodyEntityScopedGate) configure(workspace model.ID, allowCollection bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.allowedWorkspace = workspace
	g.allowCollection = allowCollection
}

func (g *bodyEntityScopedGate) resetRequests() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.requests = nil
}

func (g *bodyEntityScopedGate) snapshotRequests() []auth.Request {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]auth.Request(nil), g.requests...)
}

func (g *bodyEntityScopedGate) Scoped(_ context.Context, req auth.Request) (auth.ScopedDecision, error) {
	if req.Permission != bodyEntityPermission {
		return auth.ScopedDecision{Effect: auth.EffectAbstain}, nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.requests = append(g.requests, req)
	if req.Resource.ID == "" {
		if g.allowCollection {
			return auth.ScopedDecision{Effect: auth.EffectGrant, Reason: "test collection grant"}, nil
		}
		return auth.ScopedDecision{Effect: auth.EffectAbstain}, nil
	}
	if req.Resource.WorkspaceID == g.allowedWorkspace {
		return auth.ScopedDecision{Effect: auth.EffectGrant, Reason: "test workspace grant"}, nil
	}
	return auth.ScopedDecision{
		Effect: auth.EffectForbid, Reason: "test foreign workspace",
		Class: auth.ClassInvariant,
	}, nil
}

type bodyEntityFixture struct {
	*harness
	module *bodyEntityTestModule
	gate   *bodyEntityScopedGate
	tenant model.TenantID
	wsA    model.ID
	wsB    model.ID
	idA    model.ID
	idB    model.ID
	viewer string
}

func newBodyEntityFixture(t *testing.T) *bodyEntityFixture {
	t.Helper()
	ctx := context.Background()
	st, err := sqlstore.Open(ctx, store.Config{
		Engine: store.EngineSQLite, DSN: ":memory:", Debug: true,
	}, registerBodyEntityTestDescriptor)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(sys store.SystemScope) error {
		_, ensureErr := sys.EnsureSystemTenant(ctx)
		return ensureErr
	}); err != nil {
		t.Fatal(err)
	}

	_, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := audit.NewSigner(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	setupToken := secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token"))
	plaintext, _, err := setupToken.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	authenticator := auth.NewAuthenticator(st, nil)
	module := &bodyEntityTestModule{}
	gate := &bodyEntityScopedGate{}
	srv, err := api.New(api.Options{
		Store: st, Authenticator: authenticator,
		Authorizer: auth.NewAuthorizer(nil, auth.WithScopedGrants(gate)),
		Signer:     signer, SetupToken: setupToken, Version: "test",
		Modules: []api.Module{module},
	})
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{
		t: t, srv: srv, st: st, authr: authenticator, signer: signer,
		setupTok: plaintext, setupTokFile: setupToken,
	}
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "bodyentity")

	var wsA, wsB, idA, idB model.ID
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		first, createErr := sc.Workspaces().Create(ctx, model.Workspace{
			Name: "Workspace A", Slug: "workspace-a", Status: model.StatusActive,
		})
		if createErr != nil {
			return createErr
		}
		second, createErr := sc.Workspaces().Create(ctx, model.Workspace{
			Name: "Workspace B", Slug: "workspace-b", Status: model.StatusActive,
		})
		if createErr != nil {
			return createErr
		}
		wsA, wsB = first.ID, second.ID
		repo, createErr := sc.Ext(bodyEntityKind)
		if createErr != nil {
			return createErr
		}
		rowA, createErr := repo.Create(ctx, model.Record{
			bodyEntityWorkspace: wsA.String(), bodyEntityLabel: "A",
		})
		if createErr != nil {
			return createErr
		}
		rowB, createErr := repo.Create(ctx, model.Record{
			bodyEntityWorkspace: wsB.String(), bodyEntityLabel: "B",
		})
		if createErr != nil {
			return createErr
		}
		idA, idB = model.ID(rowA.String(model.ColID)), model.ID(rowB.String(model.ColID))
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	created := h.do("POST", "/v1/users", admin, map[string]any{
		"email": "viewer@bodyentity.test", "password": "viewerpass1",
	}, nil)
	if created.code != http.StatusCreated {
		t.Fatalf("create viewer = %d %s", created.code, created.raw)
	}
	member := h.do("POST", "/v1/memberships", admin, map[string]any{
		"user_id": created.body["id"], "tenant": tenant.String(), "role": auth.RoleViewer,
	}, nil)
	if member.code != http.StatusCreated {
		t.Fatalf("create membership = %d %s", member.code, member.raw)
	}
	login := h.do("POST", "/v1/auth/login", "", map[string]any{
		"email": "viewer@bodyentity.test", "password": "viewerpass1",
	}, nil)
	if login.code != http.StatusOK {
		t.Fatalf("viewer login = %d %s", login.code, login.raw)
	}
	gate.configure(wsA, false)
	gate.resetRequests()
	return &bodyEntityFixture{
		harness: h, module: module, gate: gate, tenant: tenant,
		wsA: wsA, wsB: wsB, idA: idA, idB: idB, viewer: login.body["token"].(string),
	}
}

func (f *bodyEntityFixture) rawRequest(method, path, token, raw string) resp {
	f.t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(raw))
	req.RemoteAddr = "10.0.0.1:1234"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Olivares-Tenant", f.tenant.String())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(recorder, req)
	out := resp{code: recorder.Code, raw: recorder.Body.String()}
	_ = json.Unmarshal(recorder.Body.Bytes(), &out.body)
	return out
}

func TestBodyEntityRouteAuthorizesStoredWorkspace(t *testing.T) {
	f := newBodyEntityFixture(t)

	// A caller-supplied workspace is irrelevant. The row says A, so the scoped
	// grant for A admits the viewer even though viewer RBAC does not carry write.
	ownRaw := `{"thing_id":"` + f.idA.String() + `","workspace_id":"` + f.wsB.String() + `"}`
	own := f.rawRequest(http.MethodPost, "/v1/m/bodyentity/body", f.viewer, ownRaw)
	if own.code != http.StatusOK {
		t.Fatalf("own body entity = %d %s", own.code, own.raw)
	}
	if own.body["resource_id"] != f.idA.String() ||
		own.body["resource_workspace"] != f.wsA.String() ||
		own.body["resource_kind"] != string(bodyEntityKind) {
		t.Fatalf("authorized resource = %#v", own.body)
	}

	// The inverse lie also fails: the body says A, but the stored row belongs to B.
	foreignRaw := `{"thing_id":"` + f.idB.String() + `","workspace_id":"` + f.wsA.String() + `"}`
	foreign := f.rawRequest(http.MethodPost, "/v1/m/bodyentity/body", f.viewer, foreignRaw)
	if foreign.code != http.StatusForbidden {
		t.Fatalf("foreign body entity = %d %s, want 403", foreign.code, foreign.raw)
	}
	if got := len(f.module.snapshot()); got != 1 {
		t.Fatalf("handler calls = %d, want only the authorized request", got)
	}
}

func TestConcealedEntityRouteUnifiesAbsenceForeignAndDenial(t *testing.T) {
	f := newBodyEntityFixture(t)
	before := len(f.module.snapshot())

	// A row in another workspace and a clean authorization denial are concealed
	// alike. The ordinary body route above intentionally retains its default 403,
	// proving the option does not change the rest of HandleEntity.
	foreign := f.rawRequest(http.MethodPost,
		"/v1/m/bodyentity/path/"+f.idB.String(), f.viewer, "")
	if foreign.code != http.StatusNotFound {
		t.Fatalf("foreign concealed entity = %d %s, want 404", foreign.code, foreign.raw)
	}

	f.gate.configure(model.NewID(), false)
	denied := f.rawRequest(http.MethodPost,
		"/v1/m/bodyentity/path/"+f.idA.String(), f.viewer, "")
	if denied.code != http.StatusNotFound {
		t.Fatalf("denied concealed entity = %d %s, want 404", denied.code, denied.raw)
	}

	// Grant the zero-workspace resource presented by an absent row. Without the
	// explicit found witness this request would reach the deliberately oblivious
	// test handler and return 200, so the assertion kills that mutation.
	f.gate.configure("", false)
	absent := f.rawRequest(http.MethodPost,
		"/v1/m/bodyentity/path/"+model.NewID().String(), f.viewer, "")
	if absent.code != http.StatusNotFound {
		t.Fatalf("absent concealed entity = %d %s, want 404", absent.code, absent.raw)
	}
	for name, got := range map[string]resp{
		"foreign": foreign, "denied": denied, "absent": absent,
	} {
		errBody, _ := got.body["error"].(map[string]any)
		if errBody["code"] != "not_found" {
			t.Fatalf("%s concealed code = %#v, want not_found", name, errBody["code"])
		}
	}
	if after := len(f.module.snapshot()); after != before {
		t.Fatalf("concealed requests reached handler: calls %d -> %d", before, after)
	}
}

func TestConcealedEntityRoutePreservesAuthenticationAndUnknown(t *testing.T) {
	f := newBodyEntityFixture(t)

	unauthenticated := f.rawRequest(http.MethodPost,
		"/v1/m/bodyentity/path/"+f.idA.String(), "", "")
	if unauthenticated.code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated concealed entity = %d %s, want 401",
			unauthenticated.code, unauthenticated.raw)
	}
	badTenantRequest := httptest.NewRequest(http.MethodPost,
		"/v1/m/bodyentity/path/"+f.idA.String(), nil)
	badTenantRequest.RemoteAddr = "10.0.0.1:1234"
	badTenantRequest.Header.Set("Authorization", "Bearer "+f.viewer)
	badTenantRequest.Header.Set("X-Olivares-Tenant", "not-a-tenant")
	badTenantRecorder := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(badTenantRecorder, badTenantRequest)
	if badTenantRecorder.Code != http.StatusBadRequest {
		t.Fatalf("concealed route tenant resolution = %d %s, want unchanged 400",
			badTenantRecorder.Code, badTenantRecorder.Body.String())
	}

	unknown := f.rawRequest(http.MethodPost,
		"/v1/m/bodyentity/unknown/"+f.idA.String(), f.viewer, "")
	if unknown.code != http.StatusServiceUnavailable {
		t.Fatalf("unknown concealed evidence = %d %s, want 503", unknown.code, unknown.raw)
	}
	errBody, _ := unknown.body["error"].(map[string]any)
	if errBody["code"] != "entity_authorization_unavailable" {
		t.Fatalf("unknown concealed code = %#v, want entity_authorization_unavailable",
			errBody["code"])
	}
	if got := len(f.module.snapshot()); got != 0 {
		t.Fatalf("authentication/unknown requests reached handler %d times", got)
	}
}

func TestBodyEntityRouteRestoresExactBody(t *testing.T) {
	f := newBodyEntityFixture(t)
	raw := "{\n  \"payload\": {\"text\":\"á\\nβ\"},\n  \"thing_id\" : \"" +
		f.idA.String() + "\",\n  \"array\": [1, 2, 3]\n}\n"
	got := f.rawRequest(http.MethodPost, "/v1/m/bodyentity/body", f.viewer, raw)
	if got.code != http.StatusOK {
		t.Fatalf("body entity = %d %s", got.code, got.raw)
	}
	if got.body["body"] != raw {
		t.Fatalf("restored body differs:\n got: %q\nwant: %q", got.body["body"], raw)
	}
	observations := f.module.snapshot()
	if len(observations) != 1 || observations[0].body != raw ||
		observations[0].resource.WorkspaceID != f.wsA {
		t.Fatalf("handler observation = %#v", observations)
	}
}

func TestBodyEntityRouteRejectsMalformedDuplicateTrailingAndOversize(t *testing.T) {
	f := newBodyEntityFixture(t)
	f.gate.configure(f.wsA, true) // admit the deferred collection-level question

	tests := []struct {
		name     string
		raw      string
		wantCode int
		wireCode string
	}{
		{name: "absent", raw: `{}`, wantCode: http.StatusBadRequest, wireCode: "bad_request"},
		{name: "empty", raw: `{"thing_id":""}`, wantCode: http.StatusBadRequest, wireCode: "bad_request"},
		{name: "non object", raw: `[]`, wantCode: http.StatusBadRequest, wireCode: "bad_request"},
		{name: "non string", raw: `{"thing_id":123}`, wantCode: http.StatusBadRequest, wireCode: "bad_request"},
		{name: "duplicate", raw: `{"thing_id":"a","thing_id":"b"}`, wantCode: http.StatusBadRequest, wireCode: "bad_request"},
		{name: "malformed", raw: `{"thing_id":"x"`, wantCode: http.StatusBadRequest, wireCode: "bad_request"},
		{name: "trailing", raw: `{"thing_id":"x"} {}`, wantCode: http.StatusBadRequest, wireCode: "bad_request"},
		{name: "oversize", raw: `{"thing_id":"` + strings.Repeat("x", (1<<20)+1) + `"}`,
			wantCode: http.StatusRequestEntityTooLarge, wireCode: "request_body_too_large"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := len(f.module.snapshot())
			got := f.rawRequest(http.MethodPost, "/v1/m/bodyentity/body", f.viewer, test.raw)
			if got.code != test.wantCode {
				t.Fatalf("response = %d %s, want %d", got.code, got.raw, test.wantCode)
			}
			errBody, _ := got.body["error"].(map[string]any)
			if errBody["code"] != test.wireCode {
				t.Fatalf("error code = %#v, want %q", errBody["code"], test.wireCode)
			}
			if after := len(f.module.snapshot()); after != before {
				t.Fatalf("malformed request reached handler: calls %d -> %d", before, after)
			}
		})
	}
}

func TestBodyEntityRouteDoesNotExposeParseOracleBeforeAuthorization(t *testing.T) {
	f := newBodyEntityFixture(t)
	f.gate.configure(f.wsA, false)
	f.gate.resetRequests()

	for _, raw := range []string{
		`{}`,
		`{"thing_id":"a","thing_id":"b"}`,
		`{"thing_id":"x"} {}`,
		`{"thing_id":"` + strings.Repeat("x", (1<<20)+1) + `"}`,
	} {
		got := f.rawRequest(http.MethodPost, "/v1/m/bodyentity/body", f.viewer, raw)
		if got.code != http.StatusForbidden {
			t.Fatalf("unauthorized malformed body = %d %s, want 403", got.code, got.raw)
		}
	}
	unauthenticated := f.rawRequest(http.MethodPost, "/v1/m/bodyentity/body", "", `{}`)
	if unauthenticated.code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated malformed body = %d %s, want 401",
			unauthenticated.code, unauthenticated.raw)
	}
	for _, req := range f.gate.snapshotRequests() {
		if req.Resource.ID != "" || !req.Resource.WorkspaceID.IsZero() {
			t.Fatalf("parse failure authorization resource = %#v, want collection-level", req.Resource)
		}
	}
	if got := len(f.module.snapshot()); got != 0 {
		t.Fatalf("parse-oracle requests reached handler %d times", got)
	}
}

func TestExistingPathEntityRouteRemainsBodyTransparent(t *testing.T) {
	f := newBodyEntityFixture(t)
	raw := "not-json\x00and a trailing document {}"
	got := f.rawRequest(http.MethodPost,
		"/v1/m/bodyentity/path/"+f.idA.String(), f.viewer, raw)
	if got.code != http.StatusOK {
		t.Fatalf("path entity = %d %s", got.code, got.raw)
	}
	if got.body["body"] != raw || got.body["resource_id"] != f.idA.String() ||
		got.body["resource_workspace"] != f.wsA.String() {
		t.Fatalf("path entity response = %#v", got.body)
	}
}
