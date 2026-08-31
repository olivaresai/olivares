// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package notify

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

// mutableClock is a deterministic, advanceable clock so tests drive dedup/throttle
// windows by moving the clock rather than waiting on the wall clock.
type mutableClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock(t time.Time) *mutableClock { return &mutableClock{t: t} }

func (c *mutableClock) Now() model.Timestamp {
	c.mu.Lock()
	defer c.mu.Unlock()
	return model.NewTimestamp(c.t)
}

func (c *mutableClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *mutableClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// fakeDispatcher is the in-memory transport seam: it records every delivered
// notification (so tests assert what was actually sent) without real network I/O.
// failOn lets a test force a per-destination error (e.g. ErrUnknownDestination).
type fakeDispatcher struct {
	mu        sync.Mutex
	dests     []string
	delivered []sdk.Notification
	failOn    map[string]error
	connFp    map[string]string // optional per-destination connector fingerprint
}

func newFakeDispatcher(dests ...string) *fakeDispatcher {
	return &fakeDispatcher{dests: dests, failOn: map[string]error{}}
}

// forget removes a destination, so a test can exercise what happens to a route
// whose destination the operator withdrew after it was authored.
func (d *fakeDispatcher) forget(name string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := d.dests[:0]
	for _, x := range d.dests {
		if x != name {
			out = append(out, x)
		}
	}
	d.dests = out
}

func (d *fakeDispatcher) Destinations() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, len(d.dests))
	copy(out, d.dests)
	return out
}

// DestinationsFor mirrors the unscoped production case: a destination the operator
// did not restrict is addressable by every tenant.
func (d *fakeDispatcher) DestinationsFor(model.TenantID) []string { return d.Destinations() }

func (d *fakeDispatcher) Deliver(_ context.Context, _ model.TenantID, dest string, n sdk.Notification) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err, ok := d.failOn[dest]; ok {
		return err
	}
	d.delivered = append(d.delivered, n)
	return nil
}

// connFp, if set, is the connector fingerprint returned for a destination (so a
// test can model an operator connector swap under an unchanged route).
func (d *fakeDispatcher) ConnectorFingerprint(dest string) (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.connFp != nil {
		if fp, ok := d.connFp[dest]; ok {
			return fp, true
		}
	}
	return "conn:" + dest, true
}

func (d *fakeDispatcher) all() []sdk.Notification {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]sdk.Notification, len(d.delivered))
	copy(out, d.delivered)
	return out
}

func (d *fakeDispatcher) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.delivered)
}

func (d *fakeDispatcher) reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.delivered = nil
}

// Compile-time proof the fake satisfies the transport seam.
var _ Dispatcher = (*fakeDispatcher)(nil)

type harness struct {
	t        *testing.T
	srv      *api.Server
	st       store.Store
	bus      eventbus.Bus
	clk      *mutableClock
	setupTok string
	mod      *Module // direct handle for white-box concurrency tests
}

// newHarness builds a real notify plane: an in-memory store with the module schema,
// the module wired into a runtime over an in-proc bus (so a published finding drives
// onEvent), and an api.Server fronting the module's routes. A deterministic clock is
// always injected; opts (e.g. WithDispatcher) configure the module.
func newHarness(t *testing.T, opts ...Option) *harness { return buildHarness(t, false, opts...) }

// newHarnessNudged builds a harness with the async nudge worker ENABLED (to exercise
// the enqueue→nudge→deliver path end to end).
func newHarnessNudged(t *testing.T, opts ...Option) *harness { return buildHarness(t, true, opts...) }

func buildHarness(t *testing.T, nudge bool, opts ...Option) *harness {
	t.Helper()
	ctx := context.Background()
	h := &harness{t: t, clk: newClock(time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC))}

	// By default disable the async nudge worker: tests drive the outbox via an explicit,
	// synchronous pumpOutbox so delivery timing is deterministic (no background delivery
	// racing an assertion). The nudge is covered by TestOutbox_NudgeDelivers.
	if !nudge {
		opts = append(opts, withoutNudge())
	}
	opts = append(opts, WithClock(h.clk))
	mod := New(opts...)
	h.mod = mod

	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, mod.RegisterSchema)
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
	h.bus = bus
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

// publishFinding lifts a FindingReport onto the bus for the given tenant/source and
// waits for the async handler to drain. The finding's OccurredAt is stamped from the
// current clock so the delivery ledger's occurred_at is deterministic.
func (h *harness) publishFinding(tenant model.TenantID, source string, f sdkmodel.FindingReport) {
	h.t.Helper()
	if f.OccurredAt.IsZero() {
		f.OccurredAt = h.clk.now()
	}
	if err := h.bus.Publish(context.Background(), event.FromObservation(tenant.String(), source, f)); err != nil {
		h.t.Fatalf("publish finding: %v", err)
	}
	h.waitDelivery()
	h.pumpOutbox(tenant)
}

// pumpOutbox drains the durable outbox for a tenant to completion, so a bus-driven
// test observes delivery outcomes synchronously (delivery is now async: route enqueues,
// the leader-gated pump delivers — pumpOutbox stands in for that pump in tests).
func (h *harness) pumpOutbox(tenant model.TenantID) {
	h.t.Helper()
	if err := h.mod.NotifyDispatchDue(context.Background(), tenant); err != nil {
		h.t.Fatalf("outbox pump: %v", err)
	}
}

// publishApproval lifts an approval.requested event onto the bus for the given
// tenant/source and waits for the async handler to drain — the origination path
// Routes into an interactive approve/deny card.
func (h *harness) publishApproval(tenant model.TenantID, source string, a event.ApprovalRequest) {
	h.t.Helper()
	if err := h.bus.Publish(context.Background(), event.ApprovalRequested(tenant.String(), source, h.clk.now(), a)); err != nil {
		h.t.Fatalf("publish approval: %v", err)
	}
	h.waitDelivery()
	h.pumpOutbox(tenant)
}

// waitDelivery is the COMPLETION BARRIER every bus-driven assertion in this
// package rests on: after it returns, every event published so far has had its
// handler run to completion, so both positive and negative assertions read a
// settled world.
//
// It must not be written against queue depth. Depth counts events WAITING; the
// in-proc bus takes an event off the channel and only then calls the handler
// (core/eventbus/inproc.go run/invoke), so depth hits 0 when the last handler
// STARTS. The previous version waited for depth==0 and then slept 50ms as
// "grace for the one in-flight handler" — a bet that notify's three write
// transactions (claim-then-send) finish within 50ms. On a saturated box
// they do not: with 32 busy loops on 16 cores, TestTenantIsolation lost that
// bet in 3 of 8 runs (and 9 of 12 under a heavier mix), always on the same
// assertion, "tenant A finding must deliver; got 0". The same 50ms also made
// the NEGATIVE assertions ("tenant B must have 0") pass for the wrong reason:
// with isolation deliberately broken, the dispatcher-count assertion caught the
// breach 0 times out of 8 before this change and 8 out of 8 after it.
//
// Enqueued/Handled state the real thing: snapshot Enqueued once Publish has
// returned (so every fan-out send is already counted), then wait for Handled to
// reach it. No sleep, no grace, no guess — and it returns as soon as the work is
// actually done rather than always paying the fixed 50ms.
func (h *harness) waitDelivery() {
	h.t.Helper()
	sp, ok := h.bus.(eventbus.StatsProvider)
	if !ok {
		h.t.Fatal("waitDelivery needs eventbus.StatsProvider to know when handlers finished")
	}
	target := sp.BusStats().Enqueued
	deadline := time.Now().Add(30 * time.Second)
	// Backoff rather than a tight poll: BusStats builds a subscriber slice per
	// call, and on the CPU-starved gate this loop is competing with the very
	// handler it is waiting for. Starting at 100µs keeps the fast case fast; the
	// 5ms cap keeps a slow one from spinning.
	for wait := 100 * time.Microsecond; ; {
		if st := sp.BusStats(); st.Handled >= target {
			return
		} else if time.Now().After(deadline) {
			h.t.Fatalf("waitDelivery: handlers never finished: handled=%d, want >=%d", st.Handled, target)
		}
		time.Sleep(wait)
		if wait < 5*time.Millisecond {
			wait *= 2
		}
	}
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

// auditActions returns the audit actions recorded for a tenant (for self-audit asserts).
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

// --- HTTP convenience helpers over the route/delivery endpoints ----------------

// createRoute POSTs a route and returns its decoded body + status code.
func (h *harness) createRoute(token string, tenant model.TenantID, in map[string]any) resp {
	h.t.Helper()
	return h.do("POST", "/v1/m/notify/routes", token, in, tenantHdr(tenant))
}

// mustCreateRoute POSTs a route, asserts 201, and returns its id.
func (h *harness) mustCreateRoute(token string, tenant model.TenantID, in map[string]any) string {
	h.t.Helper()
	r := h.createRoute(token, tenant, in)
	if r.code != http.StatusCreated {
		h.t.Fatalf("create route = %d %s", r.code, r.raw)
	}
	return r.body["id"].(string)
}

// deliveries returns the delivery ledger rows for a tenant, newest-store-order.
func (h *harness) deliveries(token string, tenant model.TenantID, query string) []map[string]any {
	h.t.Helper()
	path := "/v1/m/notify/deliveries"
	if query != "" {
		path += "?" + query
	}
	r := h.do("GET", path, token, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		h.t.Fatalf("list deliveries = %d %s", r.code, r.raw)
	}
	items, _ := r.body["items"].([]any)
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		if m, ok := it.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// terminalDeliveries filters the ledger to OUTCOME rows. Since claim-then-send
// every delivery is TWO appends — a "claimed" reservation before the
// external send and a terminal outcome after — so assertions about outcomes
// look only at the latter (and claim rows are asserted where the reservation
// itself is the point).
func (h *harness) terminalDeliveries(token string, tenant model.TenantID, query string) []map[string]any {
	all := h.deliveries(token, tenant, query)
	out := make([]map[string]any, 0, len(all))
	for _, d := range all {
		if d["status"] != "claimed" {
			out = append(out, d)
		}
	}
	return out
}

// finding builds a FindingReport with sensible defaults for routing tests.
func finding(kind string, sev sdkmodel.Severity, subjectKind, subjectRef, title string) sdkmodel.FindingReport {
	return sdkmodel.FindingReport{
		Kind: kind, Severity: sev, SubjectKind: subjectKind, SubjectRef: subjectRef, Title: title,
	}
}
