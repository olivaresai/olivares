// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package observability

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

	oteltrace "go.opentelemetry.io/otel/trace"

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

// mutableClock is a deterministic, advanceable clock shared by the module
// (func() time.Time) and the store (model.Clock), so OccurredAt windows and
// the module's since/last_seen stamps move only under test control. Mutex-
// guarded: the bus delivers the module's onEvent on its own goroutine.
type mutableClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock(t time.Time) *mutableClock { return &mutableClock{t: t} }

// Now satisfies model.Clock (the store side).
func (c *mutableClock) Now() model.Timestamp {
	c.mu.Lock()
	defer c.mu.Unlock()
	return model.NewTimestamp(c.t)
}

// nowTime is the module-side clock function.
func (c *mutableClock) nowTime() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

// advance moves the clock forward by d.
func (c *mutableClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type harness struct {
	t        *testing.T
	srv      *api.Server
	st       store.Store
	mod      *Module
	clk      *mutableClock
	setupTok string
}

// newHarness builds an observability plane backed by an in-memory store, a
// real runtime + event bus and a real api.Server. The module owns no schema,
// so the store opens with no extension registrar.
func newHarness(t *testing.T, opts ...Option) *harness {
	t.Helper()
	ctx := context.Background()
	clk := newClock(time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC))
	h := &harness{t: t, clk: clk}

	opts = append(opts, WithClock(clk.nowTime))
	mod := New(opts...)
	h.mod = mod

	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true, Clock: clk}, nil)
	if err != nil {
		t.Fatal(err)
	}
	h.st = st
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(sys store.SystemScope) error { _, e := sys.EnsureSystemTenant(ctx); return e }); err != nil {
		t.Fatal(err)
	}

	bus := eventbus.NewInProc(eventbus.Options{})
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

// publish emits a first-party observation event on the bus exactly as the
// engine's busSink would (source = the emitting connector/module name).
func (h *harness) publish(tenant model.TenantID, source string, obs sdkmodel.Observation) {
	h.t.Helper()
	if err := h.mod.host.Publish(context.Background(), event.FromObservation(tenant.String(), source, obs)); err != nil {
		h.t.Fatalf("publish: %v", err)
	}
}

// appendTraced appends one audit event to the tenant's ledger chain with a W3C
// span context on the context — the store's Append chokepoint stamps
// trace_id/span_id into Meta (core/internal/store/sqlstore/audit.go:56-63),
// exactly as a real traced API request would.
func (h *harness) appendTraced(tenant model.TenantID, traceID, spanID, action string, targetKind model.Kind, targetID model.ID) {
	h.t.Helper()
	h.appendTracedAs(tenant, traceID, spanID, action, "user:test", model.ActorUser, targetKind, targetID)
}

// appendTracedAs is appendTraced with explicit actor identity — for
// multi-agent test scenarios where different callers produce spans.
func (h *harness) appendTracedAs(tenant model.TenantID, traceID, spanID, action, actor, actorKind string, targetKind model.Kind, targetID model.ID) {
	h.t.Helper()
	tid, err := oteltrace.TraceIDFromHex(traceID)
	if err != nil {
		h.t.Fatalf("trace id: %v", err)
	}
	sid, err := oteltrace.SpanIDFromHex(spanID)
	if err != nil {
		h.t.Fatalf("span id: %v", err)
	}
	sc := oteltrace.NewSpanContext(oteltrace.SpanContextConfig{TraceID: tid, SpanID: sid, TraceFlags: oteltrace.FlagsSampled})
	ctx := oteltrace.ContextWithSpanContext(context.Background(), sc)
	if err := h.st.Mutate(ctx, tenant, func(scope store.Scope) error {
		_, aerr := scope.Audit().Append(ctx, model.AuditDraft{
			Actor: actor, ActorKind: actorKind, Action: action,
			TargetKind: targetKind, TargetID: targetID,
		})
		return aerr
	}); err != nil {
		h.t.Fatalf("append traced: %v", err)
	}
}

// waitUntil polls cond until it is true or the deadline elapses, failing the
// test on timeout — async bus delivery is awaited deterministically, no sleeps.
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

// itemsOf returns a list response's items as maps.
func itemsOf(r resp) []map[string]any {
	raw, _ := r.body["items"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, it := range raw {
		if m, ok := it.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// mapOf returns a nested object field as a map.
func mapOf(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// listOf returns a nested array field as maps.
func listOf(v any) []map[string]any {
	raw, _ := v.([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, it := range raw {
		if m, ok := it.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}
