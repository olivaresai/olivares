// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package deploy

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

// ---- test doubles for the three composition-root seams --------------------------

// fakeGate is an in-memory ApprovalGate. Request opens a request in a configurable
// initial status (pending by default) bound to the plan hash; tests flip the
// status with set() to simulate a human approving / an approval expiring.
type fakeGate struct {
	mu       sync.Mutex
	initial  GateStatus
	requests int
	byRef    map[string]GateDecision
}

func newFakeGate() *fakeGate {
	return &fakeGate{initial: StatusPending, byRef: map[string]GateDecision{}}
}

func (g *fakeGate) Request(_ context.Context, req ApprovalRequest) (GateDecision, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.requests++
	ref := "appr-" + req.PlanHash
	d := GateDecision{ApprovalRef: ref, Status: g.initial, PlanHash: req.PlanHash}
	g.byRef[ref] = d
	return d, nil
}

func (g *fakeGate) Status(_ context.Context, _ model.TenantID, ref, planHash string) (GateDecision, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if d, ok := g.byRef[ref]; ok {
		return d, nil
	}
	return GateDecision{ApprovalRef: ref, Status: StatusNoGate, PlanHash: planHash}, nil
}

// set overrides the status of an existing approval reference (the human decision).
func (g *fakeGate) set(ref string, status GateStatus) {
	g.mu.Lock()
	defer g.mu.Unlock()
	d := g.byRef[ref]
	d.ApprovalRef, d.Status = ref, status
	g.byRef[ref] = d
}

// mockExecutor is an in-memory runtime: it remembers the canonical spec hash last
// applied per (target, subject), so plan reports a diff iff the desired spec
// differs, apply is idempotent, verify reports drift, and retire forgets it.
type mockExecutor struct {
	mu         sync.Mutex
	applied    map[string]string
	applyCalls int
	failApply  bool
}

func newMockExecutor() *mockExecutor { return &mockExecutor{applied: map[string]string{}} }

func (e *mockExecutor) key(req ExecRequest) string {
	return req.Tenant.String() + "|" + req.Target + "|" + req.SubjectKind + "|" + req.SubjectRef
}

func (e *mockExecutor) desiredHash(req ExecRequest) string {
	_, h, _ := req.Spec.canonical()
	return h
}

func (e *mockExecutor) Plan(_ context.Context, req ExecRequest) ([]Change, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.applied[e.key(req)] == e.desiredHash(req) {
		return nil, nil
	}
	return []Change{{Kind: "update", Resource: "deployment", Detail: "reconcile to desired spec"}}, nil
}

func (e *mockExecutor) Apply(_ context.Context, req ExecRequest) (ExecResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.failApply {
		return ExecResult{}, errNoExecutor
	}
	e.applyCalls++
	e.applied[e.key(req)] = e.desiredHash(req)
	return ExecResult{Changes: []Change{{Kind: "update", Resource: "deployment"}}, Detail: "applied"}, nil
}

func (e *mockExecutor) Verify(_ context.Context, req ExecRequest) (ExecResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.applied[e.key(req)] == e.desiredHash(req) {
		return ExecResult{Detail: "in sync"}, nil
	}
	return ExecResult{Changes: []Change{{Kind: "update", Resource: "deployment", Detail: "drift"}}}, nil
}

func (e *mockExecutor) Retire(_ context.Context, req ExecRequest) (ExecResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.applied, e.key(req))
	return ExecResult{Detail: "retired"}, nil
}

func (e *mockExecutor) appliedHash(req ExecRequest) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.applied[e.key(req)]
}

// fakeBinder is an in-memory IdentityBinder: firm returns a bound identity;
// otherwise it returns a degraded result (never faked firm).
type fakeBinder struct {
	mu    sync.Mutex
	firm  bool
	calls int
}

func (b *fakeBinder) EnsureAgentIdentity(_ context.Context, _ model.TenantID, _, identityRef string, _ bool) (BoundIdentity, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	if b.firm {
		ref := identityRef
		if ref == "" {
			ref = "minted-nhi"
		}
		return BoundIdentity{IdentityRef: ref, Firm: true}, nil
	}
	return BoundIdentity{Firm: false, Reason: "binder unavailable"}, nil
}

// ---- harness --------------------------------------------------------------------

type harness struct {
	t        *testing.T
	srv      *api.Server
	st       store.Store
	authr    *auth.Authenticator
	setupTok string

	gate   *fakeGate
	exec   *mockExecutor
	binder *fakeBinder

	edgesMu  sync.Mutex
	captured []sdkmodel.EdgeObservation
}

// newHarness wires the standard, fully-wired deployment: a fake gate (pending), a
// mock executor and a firm identity binder. Tests that exercise the deny-closed
// defaults use newHarnessWith with a subset of options.
func newHarness(t *testing.T) *harness {
	t.Helper()
	g, ex, b := newFakeGate(), newMockExecutor(), &fakeBinder{firm: true}
	h := newHarnessWith(t, WithApprovalGate(g), WithExecutor(ex), WithIdentityBinder(b))
	h.gate, h.exec, h.binder = g, ex, b
	return h
}

// newHarnessWith builds a harness with exactly the options given (so a test can
// omit the gate to exercise deny-by-default, etc.). It runs the module through a
// real runtime so Init wires a bus-backed host, and subscribes a capture handler
// to that bus to observe the published PERMITTED edges (the feed).
func newHarnessWith(t *testing.T, opts ...Option) *harness {
	t.Helper()
	ctx := context.Background()
	h := &harness{t: t}

	dep := New(opts...)
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, dep.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	h.st = st
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(sys store.SystemScope) error { _, e := sys.EnsureSystemTenant(ctx); return e }); err != nil {
		t.Fatal(err)
	}
	dep.UseData(api.NewModuleData(st))

	bus := eventbus.NewInProc(eventbus.Options{})
	if _, err := bus.Subscribe([]event.Type{event.TypeEdgeObserved}, func(_ context.Context, e event.Event) error {
		if edge, ok := event.EdgeOf(e); ok {
			h.edgesMu.Lock()
			h.captured = append(h.captured, edge)
			h.edgesMu.Unlock()
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rt := runtime.New(runtime.Options{Bus: bus})
	if err := rt.AddModule(dep, sdk.Config{}); err != nil {
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
	authr := auth.NewAuthenticator(st, nil)
	srv, err := api.New(api.Options{
		Store: st, Authenticator: authr, Authorizer: auth.NewAuthorizer(nil),
		Signer: signer, SetupToken: tok, Version: "test", Modules: []api.Module{dep},
	})
	if err != nil {
		t.Fatal(err)
	}
	h.srv, h.authr, h.setupTok = srv, authr, plaintext
	return h
}

// stepUp elevates a session token to AAL3 through the real elevation path —
// deploy apply/retire are step-up-gated for human sessions since (the
// ceremony itself is covered by the core/api WebAuthn tests).
func (h *harness) stepUp(token string) {
	h.t.Helper()
	p, err := h.authr.Authenticate(context.Background(), token)
	if err != nil {
		h.t.Fatalf("authenticate for step-up: %v", err)
	}
	if _, err := h.authr.ElevateSession(context.Background(), p, "webauthn", auth.AAL3); err != nil {
		h.t.Fatalf("elevate session: %v", err)
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
	// deploy mutations demand an AAL3 session; the harness operators are
	// step-up-verified so the lifecycle tests exercise what they test. The
	// under-assured deny has its own test (TestApplyRequiresStepUp).
	token := r.body["token"].(string)
	h.stepUp(token)
	return token
}

// capturedEdges returns a copy of the PERMITTED edges published so far, polling
// briefly for at least n (bus delivery is asynchronous).
func (h *harness) capturedEdges(n int) []sdkmodel.EdgeObservation {
	h.t.Helper()
	for i := 0; i < 100; i++ {
		h.edgesMu.Lock()
		got := len(h.captured)
		h.edgesMu.Unlock()
		if got >= n {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.edgesMu.Lock()
	defer h.edgesMu.Unlock()
	out := make([]sdkmodel.EdgeObservation, len(h.captured))
	copy(out, h.captured)
	return out
}

// ---- request helpers ------------------------------------------------------------

// agentSpec returns a minimal valid spec with one readwrite wiring (secret by
// reference) and a declared per-agent identity.
func agentSpec(image, identityRef string) map[string]any {
	return map[string]any{
		"image":    image,
		"identity": map[string]any{"identity_ref": identityRef},
		"wirings": []map[string]any{
			{"resource_kind": "postgres.table", "resource_ref": "public.customers", "mode": "readwrite", "secret_ref": "vault:secret/data/pg#dsn"},
		},
	}
}

// createDef declares a deployment definition and returns its id.
func (h *harness) createDef(token string, tenant model.TenantID, name string, spec map[string]any) string {
	h.t.Helper()
	body := map[string]any{
		"subject_kind": "agent", "subject_ref": "acme-bot", "name": name,
		"environment": "prod", "target": "docker.host/node1", "runtime": "docker", "spec": spec,
	}
	r := h.do("POST", "/v1/m/deploy/definitions", token, body, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		h.t.Fatalf("create definition = %d %s", r.code, r.raw)
	}
	return r.body["id"].(string)
}

// applyGoverned drives the two-phase governed apply: phase 1 to obtain the
// approval ref, then (after the test sets the gate decision) phase 2.
func (h *harness) applyPhase1(token string, tenant model.TenantID, defID string) resp {
	return h.do("POST", "/v1/m/deploy/definitions/"+defID+"/apply", token, map[string]any{}, tenantHdr(tenant))
}

func (h *harness) applyPhase2(token string, tenant model.TenantID, defID, ref string) resp {
	return h.do("POST", "/v1/m/deploy/definitions/"+defID+"/apply", token, map[string]any{"approval_ref": ref}, tenantHdr(tenant))
}
