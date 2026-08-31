// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

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

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// baseTime is a fixed instant so approval expiry/escalation math is deterministic.
var baseTime = time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)

// fakeClock is a controllable model.Clock the tests advance to exercise approval
// expiry and escalation without sleeping.
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

// fakeHost captures published events so the finding-emission paths (shared
// identity, approval escalation/expiry) can be asserted.
type fakeHost struct {
	mu     sync.Mutex
	events []event.Event
}

func (h *fakeHost) Publish(_ context.Context, e event.Event) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, e)
	return nil
}
func (h *fakeHost) Subscribe([]event.Type, event.Handler) (func(), error) { return func() {}, nil }
func (h *fakeHost) Logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
func (h *fakeHost) Config() sdk.Config { return sdk.Config{} }

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

// fakeRecordingGate satisfies governance.RecordingGate in tests: the
// module harness has no engine-level recorder, so activation tests wire this
// permissive fake; the deny-closed default (no gate => activation refused) has
// its own dedicated test. It captures BindGrant calls for assertion.
type fakeRecordingGate struct {
	mu     sync.Mutex
	denied bool
	binds  map[string]string // grant id -> session id
}

func (g *fakeRecordingGate) EnsureActive(_ context.Context, _ model.TenantID, _ auth.Principal) (model.ID, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.denied {
		return "", context.DeadlineExceeded // any error: the gate is deny-closed
	}
	return "rec-session-1", nil
}

func (g *fakeRecordingGate) BindGrant(_ context.Context, _ model.TenantID, session, grant model.ID, _ auth.Principal) error {
	return g.bind(session, grant)
}

func (g *fakeRecordingGate) BindGrantInScope(_ context.Context, _ store.Scope, session, grant model.ID, _ auth.Principal) error {
	return g.bind(session, grant)
}

func (g *fakeRecordingGate) bind(session, grant model.ID) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.denied {
		return api.ErrRecordingSessionPrecondition
	}
	if g.binds == nil {
		g.binds = map[string]string{}
	}
	g.binds[grant.String()] = session.String()
	return nil
}

func (*fakeRecordingGate) SealGrantInScope(context.Context, store.Scope, model.ID, auth.Principal) error {
	return nil
}

// Gate/Record make the same fake the engine-level SessionRecorder. This carries
// the exact witness through ModuleContext instead of giving governance tests a
// re-resolution-only shortcut that production no longer uses.
func (*fakeRecordingGate) Gate(context.Context, api.RecordedCall) (api.RecordingDecision, error) {
	return api.RecordingDecision{Record: true, Session: model.ID("rec-session-1")}, nil
}

func (*fakeRecordingGate) Record(context.Context, api.RecordedCall, api.RecordingDecision, api.RecordedResult) error {
	return nil
}

// fakeProvider is an in-test identitysource.GraphProvider returning a scripted
// snapshot (no network), so the roster/sync path is exercised without a real
// directory connector.
type fakeProvider struct{ graph identitysource.Graph }

func (p *fakeProvider) Snapshot(context.Context) (identitysource.Graph, error) { return p.graph, nil }

// harness wires a real store + API server with the governance module, the ABAC
// evaluator wired into the Authorizer (so an authored deny policy actually gates a
// request), a capturing host and a controllable clock.
type harness struct {
	t        *testing.T
	srv      *api.Server
	st       store.Store
	authr    *auth.Authenticator
	gov      *governance.Module
	host     *fakeHost
	clk      *fakeClock
	setupTok string
	recGate  *fakeRecordingGate
	// Authoring consoles + their fake seams (configurable per test).
	policy   *governance.PolicyConsole
	agents   *governance.AgentsConsole
	identity *governance.IdentityConsole
	observed *fakeObserved
	threads  *fakeThreads
	wif      *fakeWif
}

// harnessOpts customizes the console wiring per test: extra policy-console
// options are appended AFTER the defaults (a later WithObservedConfig/
// WithManagedDistributor overrides the default fake), and dataConsumers are
// late-bound with the harness store's data handle — the boot() pattern for the
// store-backed truth-loop seams.
type harnessOpts struct {
	policyOpts    []governance.PolicyConsoleOption
	dataConsumers []api.DataConsumer
	// offlineStaleness, when > 0, engages the ADR-0024 Q1 offline-trust bound on the
	// scoped-grant engine (positive grants expire deny-closed past it). Zero ⇒ the
	// connected-node default (grants never expire).
	offlineStaleness time.Duration
	// engine/dsn select the storage backend (cross-backend authz parity). Zero
	// ⇒ the SQLite :memory: default, so every existing caller is unchanged; a test
	// sets EnginePostgres + a DSN to run the SAME authz assertions against Postgres.
	engine store.Engine
	dsn    string
	// adminDSN is the BYPASSRLS (NOSUPERUSER) role a Postgres engine needs to answer a
	// cross-tenant System read authoritatively. Empty is correct for SQLite and was correct
	// for Postgres too until (#608) made store.SystemScope.ListOrgs REFUSE rather than
	// return an unauthoritative empty list — after which an engine opened with only the app
	// pool answers 500 on /v1/setup, because setup performs exactly that read. Measured on
	// this batch: TestCrossBackend_AuthzParity/postgres went from green on origin/main to
	// "setup = 500" once #608 was integrated. The refusal is right; booting an engine
	// without the pool it now requires is what was wrong.
	adminDSN string
	// identityOpts are appended AFTER the default WIF wiring, so a test can wire the
	// Admin-posture provider (external-keys/residency) or deliberately leave it
	// unwired to exercise the honest available=false answer.
	identityOpts []governance.IdentityConsoleOption
}

func newHarness(t *testing.T) *harness { return newHarnessWith(t, harnessOpts{}) }

func newHarnessWith(t *testing.T, hopts harnessOpts) *harness {
	t.Helper()
	ctx := context.Background()
	clk := &fakeClock{t: baseTime}
	host := &fakeHost{}
	govOpts := []governance.Option{governance.WithClock(clk)}
	if hopts.offlineStaleness > 0 {
		govOpts = append(govOpts, governance.WithOfflinePolicyStaleness(hopts.offlineStaleness))
	}
	gov := governance.New(govOpts...)
	// break-glass demands an active recording session (deny-closed). The
	// module harness records nothing engine-side, so tests wire a permissive
	// fake; TestBreakGlassRequiresRecording proves the deny-closed default.
	recGate := &fakeRecordingGate{}
	gov.UseRecordingGate(recGate)

	engKind, dsn := hopts.engine, hopts.dsn
	if engKind == "" {
		engKind, dsn = store.EngineSQLite, ":memory:"
	}
	st, err := engine.Open(ctx, store.Config{Engine: engKind, DSN: dsn, AdminDSN: hopts.adminDSN, Debug: true}, gov.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(sys store.SystemScope) error { _, e := sys.EnsureSystemTenant(ctx); return e }); err != nil {
		t.Fatal(err)
	}
	gov.UseData(api.NewModuleData(st))
	if err := gov.Init(ctx, host); err != nil {
		t.Fatal(err)
	}

	// wire the authoring consoles over the SAME store (they reuse gov's
	// policy_revision/approval tables). Fake seams default to empty/unwired and are
	// configured per test.
	observed := &fakeObserved{}
	threads := &fakeThreads{}
	wifp := &fakeWif{}
	policyOpts := append([]governance.PolicyConsoleOption{
		governance.WithObservedConfig(observed), governance.WithPolicyConsoleClock(clk),
	}, hopts.policyOpts...)
	policy := governance.NewPolicyConsole(policyOpts...)
	agents := governance.NewAgentsConsole(governance.WithThreadEventProvider(threads), governance.WithAgentsConsoleClock(clk))
	identity := governance.NewIdentityConsole(append(
		[]governance.IdentityConsoleOption{governance.WithWifGraphProvider(wifp)},
		hopts.identityOpts...)...)
	for _, dc := range []api.DataConsumer{policy, agents, identity} {
		dc.UseData(api.NewModuleData(st))
	}
	for _, dc := range hopts.dataConsumers {
		dc.UseData(api.NewModuleData(st))
	}
	for _, m := range []sdk.Module{policy, agents, identity} {
		if err := m.Init(ctx, host); err != nil {
			t.Fatal(err)
		}
	}

	_, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := audit.NewSigner(priv)
	tok := secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token"))
	plaintext, _, err := tok.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	authr := auth.NewAuthenticator(st, nil)
	// wire the production-equivalent authorizer — the deny-overlay (RequestEvaluator)
	// PLUS the per-tenant scoped-grant engine — so the e2e tests exercise positive grants
	// and scoped forbids on the real request path, not only the deny-overlay.
	srv, err := api.New(api.Options{
		Store: st, Authenticator: authr,
		Authorizer: auth.NewAuthorizer(gov.RequestEvaluator(), auth.WithScopedGrants(gov.ScopedGrants())),
		Signer:     signer, SetupToken: tok, Version: "test",
		Modules:  []api.Module{gov, policy, agents, identity},
		Recorder: recGate,
		// the same module, in its REPORTING capacity — whoami must be able to tell
		// the console about authority a tenant-scoped grant confers. Wired here (not only
		// in the composition root) because the tests that pin the boundary between what
		// whoami reports and what the engine allows are worthless against a server that
		// cannot report it at all.
		UnconditionalGrants: gov.UnconditionalGrants(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return &harness{
		t: t, srv: srv, st: st, authr: authr, gov: gov, host: host, clk: clk, setupTok: plaintext, recGate: recGate,
		policy: policy, agents: agents, identity: identity,
		observed: observed, threads: threads, wif: wifp,
	}
}

// stepUp elevates a session token to AAL3 through the real elevation path —
// CRITICAL decisions and break-glass activation are step-up-gated since
// (the WebAuthn/PIV ceremonies have their own core/api tests). The harness
// operators are step-up-verified so each test exercises what it tests; the
// under-assured denials have their own tests (TestStepUpRequired*).
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
	token := r.body["token"].(string)
	h.stepUp(token)
	return token
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
	token := r.body["token"].(string)
	h.stepUp(token)
	return uid, token
}

// createAgent inserts an agent directly into the store and returns it.
func (h *harness) createAgent(tenant model.TenantID, name, externalID string) model.Agent {
	h.t.Helper()
	var out model.Agent
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		a, err := sc.Agents().Create(context.Background(), model.Agent{Name: name, Kind: "claude-code", ExternalID: externalID, Status: model.StatusActive})
		out = a
		return err
	}); err != nil {
		h.t.Fatalf("create agent: %v", err)
	}
	return out
}

// getAgent reloads an agent from the store.
func (h *harness) getAgent(tenant model.TenantID, id model.ID) model.Agent {
	h.t.Helper()
	var out model.Agent
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		a, err := sc.Agents().Get(context.Background(), id)
		out = a
		return err
	}); err != nil {
		h.t.Fatalf("get agent: %v", err)
	}
	return out
}

// auditActions returns the audit actions recorded in the tenant chain (for
// self-audit assertions).
func (h *harness) auditActions(tenant model.TenantID) []string {
	h.t.Helper()
	var actions []string
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(context.Background(), 1, func(e model.AuditEvent) error {
			actions = append(actions, e.Action)
			return nil
		})
	}); err != nil {
		h.t.Fatalf("walk audit: %v", err)
	}
	return actions
}

func items(r resp) []any {
	if r.body == nil {
		return nil
	}
	out, _ := r.body["items"].([]any)
	return out
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
