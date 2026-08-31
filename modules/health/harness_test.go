// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package health

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

// mutableClock is a deterministic, advanceable clock. The staleness-sweep and SLA
// engine paths need to move time forward under test control without touching the
// wall clock, so the clock is mutable behind a mutex (the sweep goroutine and the
// test both read it).
type mutableClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock(t time.Time) *mutableClock { return &mutableClock{t: t} }

// Now satisfies model.Clock.
func (c *mutableClock) Now() model.Timestamp {
	c.mu.Lock()
	defer c.mu.Unlock()
	return model.NewTimestamp(c.t)
}

// advance moves the clock forward by d.
func (c *mutableClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// now returns the current clock instant (UTC) for building expectations.
func (c *mutableClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t.UTC()
}

type harness struct {
	t        *testing.T
	srv      *api.Server
	st       store.Store
	mod      *Module
	clk      *mutableClock
	setupTok string

	findMu   sync.Mutex
	findings []sdkmodel.FindingReport
}

// newHarness builds a health plane backed by an in-memory store, a real runtime +
// event bus and a real api.Server. The sweep interval is set to one hour so the
// background sweep goroutine never fires inside a test — every sweep-driven test
// calls mod.sweepTenant directly under the mutable clock. opts are extra module
// options (the clock and a long sweep interval are always injected).
func newHarness(t *testing.T, opts ...Option) *harness {
	t.Helper()
	ctx := context.Background()
	clk := newClock(time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC))
	h := &harness{t: t, clk: clk}

	opts = append(opts, WithClock(clk), WithSweepInterval(time.Hour))
	mod := New(opts...)
	h.mod = mod

	// Inject the SAME mutable clock into the store so created_at/updated_at and the
	// module's derived staleness share one controlled time source — otherwise a
	// fresh check would read as stale the instant the store's wall-clock created_at
	// diverges from the fixed module clock.
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true, Clock: clk}, mod.RegisterSchema)
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

// view runs fn in a tenant-scoped read transaction directly against the store.
func (h *harness) view(tenant model.TenantID, fn func(store.Scope) error) {
	h.t.Helper()
	if err := h.st.View(context.Background(), tenant, fn); err != nil {
		h.t.Fatalf("view: %v", err)
	}
}

// auditActions returns the audit actions recorded for a tenant (for self-audit
// assertions), in ledger order.
func (h *harness) auditActions(tenant model.TenantID) []string {
	h.t.Helper()
	var actions []string
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(context.Background(), 0, func(e model.AuditEvent) error {
			actions = append(actions, e.Action)
			return nil
		})
	}); err != nil {
		h.t.Fatalf("walk audit: %v", err)
	}
	return actions
}

func (h *harness) deliveredFindings() []sdkmodel.FindingReport {
	h.findMu.Lock()
	defer h.findMu.Unlock()
	out := make([]sdkmodel.FindingReport, len(h.findings))
	copy(out, h.findings)
	return out
}

// countFindings counts delivered findings of a given bus kind.
func (h *harness) countFindings(kind string) int {
	n := 0
	for _, f := range h.deliveredFindings() {
		if f.Kind == kind {
			n++
		}
	}
	return n
}

// publishEdge emits an edge.observed event for a tenant on the bus, exactly as a
// source connector would, and waits for the module's async subscriber to drain.
func (h *harness) publishEdge(tenant model.TenantID, edge sdkmodel.EdgeObservation) {
	h.t.Helper()
	if err := h.mod.host.Publish(context.Background(), event.FromObservation(tenant.String(), "test", edge)); err != nil {
		h.t.Fatalf("publish edge: %v", err)
	}
}

// waitBus yields long enough for asynchronous bus delivery to complete. The
// in-proc bus delivers on a per-subscriber goroutine; tests that depend on a
// delivered finding or a handled edge poll instead of sleeping where they can, but
// the simple cases reuse this small settle.
func (h *harness) waitBus() { time.Sleep(30 * time.Millisecond) }

// waitUntil polls cond until it is true or the deadline elapses, failing the test
// on timeout. Used to await asynchronous edge handling deterministically.
func (h *harness) waitUntil(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.t.Fatalf("timed out waiting for %s", what)
}

func intOf(v any) int {
	if f, ok := v.(float64); ok {
		return int(f)
	}
	return 0
}

func boolOf(v any) bool {
	b, _ := v.(bool)
	return b
}

func strOf(v any) string {
	s, _ := v.(string)
	return s
}
