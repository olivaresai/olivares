// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// openStore opens a real SQLite store with the module's schema registered.
func openStore(ctx context.Context, m *Module) (store.Store, error) {
	return engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, m.RegisterSchema)
}

// harness wires a real store, the api server with the sessions module mounted,
// and the auth bootstrap. It shares the module instance, so a test can drive the
// module's event handlers (white-box) and observe the result over the HTTP API.
type harness struct {
	t        *testing.T
	m        *Module
	srv      *api.Server
	st       store.Store
	setupTok string
}

func newHarness(t *testing.T, m *Module) *harness {
	t.Helper()
	ctx := context.Background()
	st, err := openStore(ctx, m)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(sys store.SystemScope) error { _, e := sys.EnsureSystemTenant(ctx); return e }); err != nil {
		t.Fatal(err)
	}
	m.UseData(api.NewModuleData(st))
	stopModuleAtCleanup(t, m)
	_, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := audit.NewSigner(priv)
	tok := secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token"))
	plaintext, _, err := tok.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	authorizer := auth.NewAuthorizer(nil)
	m.UseWorkAuthorizer(authorizer)
	srv, err := api.New(api.Options{
		Store: st, Authenticator: auth.NewAuthenticator(st, nil), Authorizer: authorizer,
		Signer: signer, SetupToken: tok, Version: "test", Modules: []api.Module{m},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, m: m, srv: srv, st: st, setupTok: plaintext}
}

type resp struct {
	code   int
	body   map[string]any
	raw    string
	header http.Header
}

func (h *harness) do(method, path, token string, hdr map[string]string) resp {
	h.t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(nil))
	req.RemoteAddr = "10.0.0.1:1234"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.srv.Handler().ServeHTTP(rec, req)
	out := resp{code: rec.Code, raw: rec.Body.String(), header: rec.Header().Clone()}
	_ = json.Unmarshal(rec.Body.Bytes(), &out.body)
	return out
}

func (h *harness) doJSON(method, path, token string, body any, hdr map[string]string) resp {
	h.t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(method, path, bytes.NewReader(b))
	req.RemoteAddr = "10.0.0.1:1234"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.srv.Handler().ServeHTTP(rec, req)
	out := resp{code: rec.Code, raw: rec.Body.String(), header: rec.Header().Clone()}
	_ = json.Unmarshal(rec.Body.Bytes(), &out.body)
	return out
}

func tenantHdr(t model.TenantID) map[string]string {
	return map[string]string{"X-Olivares-Tenant": t.String()}
}

func (h *harness) adminLogin() string {
	h.t.Helper()
	if r := h.doJSON("POST", "/v1/setup", "", map[string]any{"token": h.setupTok, "email": "root@x.io", "password": "supersecret1"}, nil); r.code != http.StatusCreated {
		h.t.Fatalf("setup = %d %s", r.code, r.raw)
	}
	r := h.doJSON("POST", "/v1/auth/login", "", map[string]any{"email": "root@x.io", "password": "supersecret1"}, nil)
	if r.code != http.StatusOK {
		h.t.Fatalf("login = %d %s", r.code, r.raw)
	}
	return r.body["token"].(string)
}

func (h *harness) createOrg(token, slug string) model.TenantID {
	h.t.Helper()
	r := h.doJSON("POST", "/v1/system/orgs", token, map[string]any{"name": slug, "slug": slug}, nil)
	if r.code != http.StatusCreated {
		h.t.Fatalf("create org = %d %s", r.code, r.raw)
	}
	return model.TenantID(r.body["tenant_id"].(string))
}

func (h *harness) viewerToken(admin string, tenant model.TenantID, email string) string {
	h.t.Helper()
	r := h.doJSON("POST", "/v1/users", admin, map[string]any{"email": email, "password": "viewerpass1"}, nil)
	if r.code != http.StatusCreated {
		h.t.Fatalf("user = %d %s", r.code, r.raw)
	}
	uid := r.body["id"].(string)
	if r := h.doJSON("POST", "/v1/memberships", admin, map[string]any{"user_id": uid, "tenant": tenant.String(), "role": auth.RoleViewer}, nil); r.code != http.StatusCreated {
		h.t.Fatalf("grant = %d %s", r.code, r.raw)
	}
	r = h.doJSON("POST", "/v1/auth/login", "", map[string]any{"email": email, "password": "viewerpass1"}, nil)
	return r.body["token"].(string)
}

func TestSessionsAPI(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenantA := h.createOrg(admin, "acme")
	tenantB := h.createOrg(admin, "globex")
	viewer := h.viewerToken(admin, tenantA, "v@acme.com")

	// Drive live operation into tenant A.
	ctx := context.Background()
	_ = m.onEdge(ctx, tenantA.String(), sessEdge("sess-1", "file", "/a", sdkmodel.ModeRead, "Read", baseTime))
	_ = m.onCost(ctx, tenantA.String(), sdkmodel.CostSample{SessionRef: "sess-1", InputTokens: 10, OutputTokens: 3, CostMicroUSD: 5, OccurredAt: baseTime})

	// List live sessions.
	r := h.do("GET", "/v1/m/sessions/live", viewer, tenantHdr(tenantA))
	if r.code != http.StatusOK {
		t.Fatalf("list live = %d %s", r.code, r.raw)
	}
	items, _ := r.body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("live items = %d, want 1: %s", len(items), r.raw)
	}

	// Get one live session.
	r = h.do("GET", "/v1/m/sessions/live/sess-1", viewer, tenantHdr(tenantA))
	if r.code != http.StatusOK || r.body["current_action"] != "Read" {
		t.Fatalf("get live = %d %s", r.code, r.raw)
	}

	// Timeline.
	r = h.do("GET", "/v1/m/sessions/live/sess-1/timeline", viewer, tenantHdr(tenantA))
	if r.code != http.StatusOK {
		t.Fatalf("timeline = %d %s", r.code, r.raw)
	}
	if tl, _ := r.body["items"].([]any); len(tl) != 2 {
		t.Errorf("timeline items = %d, want 2", len(tl))
	}

	// A client-supplied cursor on the recency-sorted list must not 500 (the
	// custom sort forbids a keyset cursor; the handler ignores it).
	if r := h.do("GET", "/v1/m/sessions/live?cursor=whatever", viewer, tenantHdr(tenantA)); r.code != http.StatusOK {
		t.Errorf("list live with cursor = %d, want 200 (cursor ignored, not a 500)", r.code)
	}

	// Tenant isolation: B has no sessions, and the viewer of A cannot read B.
	if r := h.do("GET", "/v1/m/sessions/live", viewer, tenantHdr(tenantB)); r.code != http.StatusForbidden {
		t.Errorf("cross-tenant = %d, want 403", r.code)
	}
	// Unauthenticated.
	if r := h.do("GET", "/v1/m/sessions/live", "", tenantHdr(tenantA)); r.code != http.StatusUnauthorized {
		t.Errorf("no-auth = %d, want 401", r.code)
	}
}

// TestSessionsStream proves the SSE channel delivers a live update to a connected
// client (and only for its own tenant). It uses a real HTTP server so the
// response actually streams.
func TestSessionsStream(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenantA := h.createOrg(admin, "acme")
	viewer := h.viewerToken(admin, tenantA, "v@acme.com")

	ts := httptest.NewServer(h.srv.Handler())
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/v1/m/sessions/stream", nil)
	req.Header.Set("Authorization", "Bearer "+viewer)
	req.Header.Set("X-Olivares-Tenant", tenantA.String())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}

	reader := bufio.NewReader(res.Body)
	// The ": connected" prelude is written after the subscription is registered,
	// so once we read it, a subsequent publish is guaranteed to reach us.
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatalf("read prelude: %v", err)
	}

	// Push a live update for this tenant.
	go func() {
		time.Sleep(20 * time.Millisecond)
		_ = m.onEdge(context.Background(), tenantA.String(), sessEdge("sess-stream", "file", "/x", sdkmodel.ModeRead, "Read", time.Now()))
	}()

	got := make(chan string, 1)
	go func() {
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			if data, ok := strings.CutPrefix(line, "data: "); ok {
				got <- strings.TrimSpace(data)
				return
			}
		}
	}()

	select {
	case payload := <-got:
		var dto liveDTO
		if err := json.Unmarshal([]byte(payload), &dto); err != nil {
			t.Fatalf("bad stream payload %q: %v", payload, err)
		}
		if dto.SessionRef != "sess-stream" {
			t.Errorf("stream session_ref = %q, want sess-stream", dto.SessionRef)
		}
		if dto.CurrentAction != "Read" {
			t.Errorf("stream current_action = %q", dto.CurrentAction)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("did not receive a stream event within 3s")
	}
}
