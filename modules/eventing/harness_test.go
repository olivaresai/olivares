// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package eventing

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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

// mutableClock is a deterministic, advanceable clock so tests drive backoff and
// retention windows by moving the clock rather than waiting on the wall clock.
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

func (c *mutableClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// fakeSealer is an in-memory SecretSealer: reversible and tenant-bound, so
// tests can assert that the cleartext never lands in the store while the
// dispatcher still signs with the real secret.
type fakeSealer struct{}

func (fakeSealer) Seal(_ context.Context, tenant model.TenantID, pt []byte) (string, error) {
	return "sealed:" + tenant.String() + ":" + base64.StdEncoding.EncodeToString(pt), nil
}

func (fakeSealer) Open(_ context.Context, tenant model.TenantID, sealed string) ([]byte, error) {
	rest, ok := strings.CutPrefix(sealed, "sealed:"+tenant.String()+":")
	if !ok {
		return nil, fmt.Errorf("sealed for another tenant or malformed")
	}
	return base64.StdEncoding.DecodeString(rest)
}

var _ SecretSealer = fakeSealer{}

// capturedReq is one request the receiver saw.
type capturedReq struct {
	header http.Header
	body   []byte
}

// receiver is the consumer endpoint: an httptest server that records every
// delivery and answers with a programmable status code.
type receiver struct {
	mu     sync.Mutex
	status int
	reqs   []capturedReq
	srv    *httptest.Server
}

func newReceiver(t *testing.T) *receiver {
	rc := &receiver{status: http.StatusOK}
	rc.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := new(bytes.Buffer)
		_, _ = body.ReadFrom(r.Body)
		rc.mu.Lock()
		rc.reqs = append(rc.reqs, capturedReq{header: r.Header.Clone(), body: body.Bytes()})
		status := rc.status
		rc.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(rc.srv.Close)
	return rc
}

func (rc *receiver) setStatus(s int) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.status = s
}

func (rc *receiver) count() int {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return len(rc.reqs)
}

func (rc *receiver) all() []capturedReq {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	out := make([]capturedReq, len(rc.reqs))
	copy(out, rc.reqs)
	return out
}

// waitFor polls cond with a deadline — never a fixed sleep (the notify 30ms
// idiom is the known source of the race-gate flake).
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

type harness struct {
	t        *testing.T
	srv      *api.Server
	st       store.Store
	bus      eventbus.Bus
	mod      *Module
	clk      *mutableClock
	setupTok string
}

// newHarness builds a real eventing plane: an in-memory store with the module
// schema, the module wired into a runtime over an in-proc bus (so a published
// event drives capture + the nudge worker), and an api.Server fronting the
// module's routes. Defaults: deterministic clock, loopback endpoints allowed
// (the receiver is an httptest server), the fake sealer, the REAL RBAC
// authorizer (no ABAC), and a millisecond retry ladder. Caller opts win.
func newHarness(t *testing.T, opts ...Option) *harness {
	t.Helper()
	ctx := context.Background()
	h := &harness{t: t, clk: newClock(time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC))}

	all := []Option{
		WithClock(h.clk),
		WithAllowLoopback(true),
		WithSecretSealer(fakeSealer{}),
		WithAuthorizer(auth.NewAuthorizer(nil)),
		WithRetrySchedule([]time.Duration{5 * time.Millisecond, 5 * time.Millisecond}),
	}
	all = append(all, opts...)
	mod := New(all...)
	h.mod = mod

	// The module clock ALSO drives the store, so created_at-based pruning is
	// deterministic under the advanceable clock.
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true, Clock: h.clk}, mod.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	h.st = st
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(sys store.SystemScope) error { _, e := sys.EnsureSystemTenant(ctx); return e }); err != nil {
		t.Fatal(err)
	}
	mod.UseData(api.NewModuleData(st))
	// Unit H: wire the writer fence off the store, exactly as the first-party composition root
	// does — boot.go treats a store without the capability as a boot failure. A harness that left
	// the seam nil was modeling the ONE configuration in which reading the fence costs nothing, and
	// that is precisely how a governed writer came to read it from inside its own transaction
	// without any test noticing (TestTheFenceIsReadBeforeTheTransactionNotInsideIt).
	//
	// It also makes every writer stamp against the generation the DATABASE holds rather than zero,
	// which is what the enforcing triggers compare.
	mod.UseEgressWriterFence(fenceOf(st))

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

// createSubscription POSTs a subscription and asserts 201, returning (id, secret).
func (h *harness) createSubscription(token string, tenant model.TenantID, in map[string]any) (string, string) {
	h.t.Helper()
	r := h.do("POST", "/v1/m/eventing/subscriptions", token, in, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		h.t.Fatalf("create subscription = %d %s", r.code, r.raw)
	}
	return r.body["id"].(string), r.body["secret"].(string)
}

// publishFinding lifts a FindingReport onto the bus for the tenant and source.
// busBarrier blocks until every event published so far has had its handlers
// RETURN. The bus documents this pair as the real completion barrier
// (core/eventbus/stats.go): Depth alone drops to 0 when a handler STARTS, so a
// drained queue says nothing about the invocation in flight — snapshot Enqueued
// after Publish returns, wait for Handled to reach it, and every one of those
// handlers has finished.
//
// It matters wherever a test publishes BEFORE changing the state the module
// consults while handling. Publish is asynchronous by contract ("Publish does
// not wait for handlers to run", core/eventbus/bus.go:31), so without this the
// pre-change event can be handled AFTER the change and be treated as if it came
// after it.
func (h *harness) busBarrier() {
	h.t.Helper()
	sp, ok := h.bus.(eventbus.StatsProvider)
	if !ok {
		h.t.Fatalf("the harness bus does not expose BusStats; the barrier cannot be honest about completion")
	}
	target := sp.BusStats().Enqueued
	waitFor(h.t, "published events to finish being handled", func() bool {
		return sp.BusStats().Handled >= target
	})
}

func (h *harness) publishFinding(tenant model.TenantID, source, kind, title string) {
	h.t.Helper()
	f := sdkmodel.FindingReport{
		Kind: kind, Severity: sdkmodel.SeverityHigh, SubjectKind: "agent", SubjectRef: "agent-1",
		Title: title, OccurredAt: time.Date(2026, 6, 11, 11, 0, 0, 0, time.UTC),
	}
	if err := h.bus.Publish(context.Background(), event.FromObservation(tenant.String(), source, f)); err != nil {
		h.t.Fatalf("publish finding: %v", err)
	}
}

// publishEdge lifts an EdgeObservation onto the bus.
func (h *harness) publishEdge(tenant model.TenantID, source string) {
	h.t.Helper()
	e := sdkmodel.EdgeObservation{
		OriginKind: "agent", OriginRef: "agent-1",
		ResourceKind: "postgres.table", ResourceRef: "public.t",
		Mode: sdkmodel.ModeRead, Source: sdkmodel.SignalOTEL, Confidence: sdkmodel.ConfidenceAttributed,
		ObservedAt: time.Date(2026, 6, 11, 11, 0, 0, 0, time.UTC),
	}
	if err := h.bus.Publish(context.Background(), event.FromObservation(tenant.String(), source, e)); err != nil {
		h.t.Fatalf("publish edge: %v", err)
	}
}

// publishMetric lifts a MetricSample onto the bus (metric.sampled joined
// the catalog).
func (h *harness) publishMetric(tenant model.TenantID, source string) {
	h.t.Helper()
	s := sdkmodel.MetricSample{
		Name: "claude_code.lines_of_code.count", Value: 42, Unit: "lines",
		SubjectKind: "developer", SubjectRef: "dev@x.io",
		OccurredAt: time.Date(2026, 6, 11, 11, 0, 0, 0, time.UTC),
	}
	if err := h.bus.Publish(context.Background(), event.FromObservation(tenant.String(), source, s)); err != nil {
		h.t.Fatalf("publish metric: %v", err)
	}
}

// deliveries lists the delivery rows via the API.
func (h *harness) deliveries(token string, tenant model.TenantID, query string) []map[string]any {
	h.t.Helper()
	path := "/v1/m/eventing/deliveries"
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

// dispatch runs a manual dispatch pass for tenant (what the pump does).
func (h *harness) dispatch(tenant model.TenantID) {
	h.t.Helper()
	if err := h.mod.DispatchDue(context.Background(), tenant); err != nil {
		h.t.Fatalf("dispatch: %v", err)
	}
}

// waitAttempts waits until the single delivery row's attempts counter AND a
// committed outcome (status no longer "delivering") reach n — i.e. the async
// worker finished its outcome write, not merely the receiver saw the request.
func (h *harness) waitAttempts(token string, tenant model.TenantID, n float64) {
	h.t.Helper()
	waitFor(h.t, "outcome write to commit", func() bool {
		ds := h.deliveries(token, tenant, "")
		return len(ds) == 1 && ds[0]["attempts"].(float64) == n && ds[0]["status"] != "delivering"
	})
}

// firstAttemptCommitted reports whether these delivery rows show one whose first attempt has
// COMMITTED ITS OUTCOME: it is queued again for the next rung of the ladder, or terminal.
//
// A row that is merely "delivering" does NOT satisfy it, and that single exclusion is the
// whole point. "delivering" is a lease a worker holds and has not resolved; scanDue takes
// queued rows that are due plus leases that have gone stale, never a fresh one, so a dispatch
// pass issued against it does nothing and the clock advance that carried it is wasted.
//
// It is a named function rather than a closure so the rule can be pinned deterministically —
// see TestFirstAttemptCommittedRejectsALeaseInFlight. waitAttempts is the same rule for a
// tenant holding exactly one row; this one serves the tests that hold more, where "some row
// finished an attempt" would also be answered by an unrelated row that succeeded earlier.
func firstAttemptCommitted(ds []map[string]any) bool {
	for _, d := range ds {
		attempts, _ := d["attempts"].(float64)
		switch {
		case d["status"] == "queued" && attempts >= 1:
			return true
		case d["status"] == "dead":
			return true
		}
	}
	return false
}

// deliveryRows reads the raw delivery rows straight from the store — for tests
// whose authorizer setup also (correctly) hides rows from the LIST endpoint's
// caller-side RBAC filter.
func (h *harness) deliveryRows(tenant model.TenantID) []model.Record {
	h.t.Helper()
	var out []model.Record
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(deliveryKind)
		if err != nil {
			return err
		}
		recs, _, err := repo.List(context.Background(), model.Query{Limit: listCap})
		out = recs
		return err
	}); err != nil {
		h.t.Fatal(err)
	}
	return out
}

// auditActions returns the audit actions recorded for a tenant.
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
