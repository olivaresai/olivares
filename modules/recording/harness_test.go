// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package recording_test

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
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/recording"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// baseTime is a fixed instant so idle/seal math is deterministic.
var baseTime = time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)

// fakeClock is a controllable model.Clock.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() model.Timestamp {
	c.mu.Lock()
	defer c.mu.Unlock()
	return model.NewTimestamp(c.t)
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// fakeHost captures published events AND the module's finding subscription, so
// tests can deliver a governance_breakglass_reviewed finding by hand.
type fakeHost struct {
	mu      sync.Mutex
	events  []event.Event
	handler event.Handler
}

func (h *fakeHost) Publish(_ context.Context, e event.Event) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, e)
	return nil
}

func (h *fakeHost) Subscribe(_ []event.Type, fn event.Handler) (func(), error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.handler = fn
	return func() {}, nil
}

func (h *fakeHost) Logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func (h *fakeHost) Config() sdk.Config { return sdk.Config{} }

// deliver hands a finding event to the module's subscription handler.
func (h *fakeHost) deliver(t *testing.T, tenant model.TenantID, f sdkmodel.FindingReport) {
	t.Helper()
	h.mu.Lock()
	fn := h.handler
	h.mu.Unlock()
	if fn == nil {
		t.Fatal("module never subscribed")
	}
	if err := fn(context.Background(), event.FromObservation(tenant.String(), "test", f)); err != nil {
		t.Fatalf("deliver finding: %v", err)
	}
}

func (h *fakeHost) findings() []sdkmodel.FindingReport {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []sdkmodel.FindingReport
	for _, e := range h.events {
		if f, ok := event.FindingOf(e); ok {
			out = append(out, f)
		}
	}
	return out
}

// victimModule mounts privileged-shaped routes on the "governance" namespace
// (free in this harness) so tests drive the engine's recording wrapper through
// real surfaces: an ordinary read/write pair and a break-glass-permission route
// (the mandatory floor).
type victimModule struct{}

func (victimModule) APINamespace() string { return "governance" }

func (victimModule) Permissions() []auth.Permission {
	return []auth.Permission{"governance:identity:read", "governance:identity:write", "governance:breakglass:write"}
}

func (victimModule) APIRoutes(reg api.RouteRegistrar) {
	ok := func(w http.ResponseWriter, r *http.Request, _ api.ModuleContext) {
		// Real handlers read their body (decodeJSON); the frame digests exactly
		// the bytes the handler consumed, so the victim consumes them too.
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}
	reg.Handle("GET", "/things", "governance:identity:read", ok)
	reg.Handle("GET", "/things/{id}", "governance:identity:read", ok)
	reg.Handle("POST", "/things", "governance:identity:write", ok)
	reg.Handle("POST", "/breakglass/consume", "governance:breakglass:write", ok)
	// A handler that panics: the engine recoverer writes the 500, and the
	// recording wrapper must still append the frame (the deferred Record).
	reg.Handle("GET", "/explode", "governance:identity:read", func(http.ResponseWriter, *http.Request, api.ModuleContext) {
		panic("boom")
	})
}

// harness wires a real store + API server with the recording module as BOTH a
// mounted module and the engine's Options.Recorder, plus the victim surface.
type harness struct {
	t        *testing.T
	srv      *api.Server
	st       store.Store
	rec      *recording.Module
	host     *fakeHost
	clk      *fakeClock
	setupTok string
}

func newHarness(t *testing.T, opts ...recording.Option) *harness {
	t.Helper()
	ctx := context.Background()
	clk := &fakeClock{t: baseTime}
	host := &fakeHost{}
	rec := recording.New(append([]recording.Option{recording.WithClock(clk)}, opts...)...)
	rec.UseKnownNamespaces([]string{"governance", "recording"})

	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, rec.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(sys store.SystemScope) error { _, e := sys.EnsureSystemTenant(ctx); return e }); err != nil {
		t.Fatal(err)
	}
	rec.UseData(api.NewModuleData(st))
	if err := rec.Init(ctx, host); err != nil {
		t.Fatal(err)
	}

	_, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := audit.NewSigner(priv)
	tok := secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token"))
	plaintext, _, err := tok.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	srv, err := api.New(api.Options{
		Store: st, Authenticator: auth.NewAuthenticator(st, nil), Authorizer: auth.NewAuthorizer(nil),
		Signer: signer, SetupToken: tok, Version: "test", Clock: clk,
		Modules:  []api.Module{rec, victimModule{}},
		Recorder: rec,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, srv: srv, st: st, rec: rec, host: host, clk: clk, setupTok: plaintext}
}

type resp struct {
	code   int
	body   map[string]any
	raw    string
	header http.Header
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
	out := resp{code: rec.Code, raw: rec.Body.String(), header: rec.Header()}
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

// roleUser creates a user with role in tenant and returns (userID, sessionToken).
func (h *harness) roleUser(admin string, tenant model.TenantID, email, role string) (string, string) {
	h.t.Helper()
	r := h.do("POST", "/v1/users", admin, map[string]any{"email": email, "password": "memberpass1"}, nil)
	if r.code != http.StatusCreated {
		h.t.Fatalf("create user %s = %d %s", email, r.code, r.raw)
	}
	uid := r.body["id"].(string)
	if r := h.do("POST", "/v1/memberships", admin, map[string]any{"user_id": uid, "tenant": tenant.String(), "role": role}, nil); r.code != http.StatusCreated {
		h.t.Fatalf("grant %s = %d %s", email, r.code, r.raw)
	}
	r = h.do("POST", "/v1/auth/login", "", map[string]any{"email": email, "password": "memberpass1"}, nil)
	if r.code != http.StatusOK {
		h.t.Fatalf("login %s = %d %s", email, r.code, r.raw)
	}
	return uid, r.body["token"].(string)
}

// mintToken creates a tenant-bound API token (a service principal).
func (h *harness) mintToken(admin string, tenant model.TenantID, role string) string {
	h.t.Helper()
	r := h.do("POST", "/v1/tokens", admin, map[string]any{"name": "svc", "tenant": tenant.String(), "role": role}, nil)
	if r.code != http.StatusCreated {
		h.t.Fatalf("mint token = %d %s", r.code, r.raw)
	}
	return r.body["token"].(string)
}

// sessions lists the tenant's recording sessions as token.
func (h *harness) sessions(token string, tenant model.TenantID, query string) []map[string]any {
	h.t.Helper()
	r := h.do("GET", "/v1/m/recording/sessions"+query, token, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		h.t.Fatalf("list sessions = %d %s", r.code, r.raw)
	}
	items, _ := r.body["items"].([]any)
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		out = append(out, it.(map[string]any))
	}
	return out
}

// mustSigner builds a throwaway audit signer (secondary servers in tests).
func mustSigner(t *testing.T) *audit.Signer {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(nil)
	s, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// mustSetupToken builds a consumed-once setup token for a secondary server.
func mustSetupToken(t *testing.T) *secure.SetupToken {
	t.Helper()
	tok := secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token"))
	if _, _, err := tok.Ensure(); err != nil {
		t.Fatal(err)
	}
	return tok
}

// doAgainst issues a request against an arbitrary server (not the harness one).
func doAgainst(t *testing.T, srv *api.Server, method, path, token string, body any, hdr map[string]string) resp {
	t.Helper()
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
	srv.Handler().ServeHTTP(rec, req)
	out := resp{code: rec.Code, raw: rec.Body.String()}
	_ = json.Unmarshal(rec.Body.Bytes(), &out.body)
	return out
}

// errorCode extracts the error envelope code (core API errors).
func errorCode(r resp) string {
	if e, ok := r.body["error"].(map[string]any); ok {
		c, _ := e["code"].(string)
		return c
	}
	return ""
}
