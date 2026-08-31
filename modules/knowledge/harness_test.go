// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/contentsource"
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

// ---- test doubles for the composition-root seams --------------------------------

// fixedGuard is a permissive/controllable RetrievalGuard: it returns the configured
// grants for every request. Tests vary groups/clearance/region/allowed to exercise
// the governance filter.
type fixedGuard struct {
	grants Grants
}

func (g fixedGuard) Resolve(_ context.Context, _ model.TenantID, _, _, _ string) (Grants, error) {
	return g.grants, nil
}

// errorGuard always fails to resolve — to exercise the fail-closed (deny) path.
type errorGuard struct{}

func (errorGuard) Resolve(_ context.Context, _ model.TenantID, _, _, _ string) (Grants, error) {
	return Grants{}, context.DeadlineExceeded
}

// egressEmbedder wraps the local embedder but reports egress (a hosted model), to
// exercise the red-line egress gate. Its vectors are the deterministic local ones,
// which is fine — the tests assert on the GATE, not on semantic quality.
type egressEmbedder struct{ LocalHashEmbedder }

func (egressEmbedder) AllowsEgress() bool { return true }
func (egressEmbedder) ModelRef() string   { return "hosted-embed-model" }

// fakeSource is an in-memory contentsource.Source over a fixed document set.
type fakeSource struct {
	docs []contentsource.Document
	kind contentsource.ContentClass
}

func newFakeSource(docs []contentsource.Document) *fakeSource {
	return &fakeSource{docs: docs, kind: contentsource.ClassDocument}
}

func (s *fakeSource) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: "test.fake-source", Type: sdk.TypeSource, APIVersion: sdk.APIVersion}
}
func (s *fakeSource) Kind() contentsource.ContentClass       { return s.kind }
func (s *fakeSource) Open(context.Context, sdk.Config) error { return nil }
func (s *fakeSource) Close(context.Context) error            { return nil }
func (s *fakeSource) Fetch(_ context.Context, id string) (contentsource.Document, error) {
	for _, d := range s.docs {
		if d.DocID == id {
			return d, nil
		}
	}
	return contentsource.Document{}, errFakeNotFound
}
func (s *fakeSource) List(_ context.Context, _ string) ([]contentsource.DocRef, string, error) {
	refs := make([]contentsource.DocRef, 0, len(s.docs))
	for _, d := range s.docs {
		refs = append(refs, contentsource.DocRef{DocID: d.DocID, Title: d.Title})
	}
	return refs, "", nil
}

// errFakeNotFound is returned by the fake source's Fetch for a missing id.
var errFakeNotFound = errors.New("fake source: not found")

// ---- harness --------------------------------------------------------------------

type harness struct {
	t        *testing.T
	srv      *api.Server
	st       store.Store
	auditPub ed25519.PublicKey
	setupTok string
	mod      *Module

	findMu   sync.Mutex
	findings []sdkmodel.FindingReport
}

// newHarness wires a fully-usable knowledge plane: the LOCAL embedder (zero egress,
// deterministic — the module default) and a permissive guard (all groups, secret
// clearance, no region) so retrieval returns results. Tests that exercise the
// deny-closed defaults or the egress gate use newHarnessWith with explicit options.
func newHarness(t *testing.T) *harness {
	return newHarnessWith(t, WithRetrievalGuard(fixedGuard{grants: Grants{
		Allowed: true, Groups: []string{"engineering", "sre", "product", "hr"}, Clearance: classSecret, Region: "",
	}}))
}

func newHarnessWith(t *testing.T, opts ...Option) *harness {
	t.Helper()
	ctx := context.Background()
	h := &harness{t: t}

	mod := New(opts...)
	h.mod = mod
	pub, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := audit.NewSigner(priv)
	h.auditPub = pub
	st, err := engine.Open(ctx, store.Config{
		Engine: store.EngineSQLite, DSN: ":memory:", Debug: true, SignEvent: signer.SignEvent,
	}, mod.RegisterSchema)
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

// capturedFindings returns the findings published so far (bus delivery is async).
func (h *harness) capturedFindings() []sdkmodel.FindingReport {
	h.t.Helper()
	for i := 0; i < 100; i++ {
		h.findMu.Lock()
		n := len(h.findings)
		h.findMu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.findMu.Lock()
	defer h.findMu.Unlock()
	out := make([]sdkmodel.FindingReport, len(h.findings))
	copy(out, h.findings)
	return out
}

func (h *harness) hasFinding(kind string) bool {
	for _, f := range h.capturedFindings() {
		if f.Kind == kind {
			return true
		}
	}
	return false
}

// ---- request helpers ------------------------------------------------------------

// createKB creates a KB and returns its id. classification/embed_policy default
// when empty.
func (h *harness) createKB(token string, tenant model.TenantID, body map[string]any) resp {
	return h.do("POST", "/v1/m/knowledge/kbs", token, body, tenantHdr(tenant))
}

func (h *harness) mustKB(token string, tenant model.TenantID, body map[string]any) string {
	r := h.createKB(token, tenant, body)
	if r.code != http.StatusCreated {
		h.t.Fatalf("create kb = %d %s", r.code, r.raw)
	}
	return r.body["id"].(string)
}

func (h *harness) module() *Module { return h.mod }

func (h *harness) addSource(name string, src contentsource.Source) {
	h.mod.AddSource(name, src)
}

func (h *harness) scopedPrincipal(tenant model.TenantID) auth.Principal {
	return auth.ScopedPrincipal(model.NewID(), "test-principal", tenant, "editor")
}
