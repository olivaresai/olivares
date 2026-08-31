// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

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

// manualClock is a deterministic clock for the cadence/anti-evasion tests.
type manualClock struct {
	mu sync.Mutex
	t  time.Time
}

func newManualClock() *manualClock { return &manualClock{t: time.Now()} }

func (c *manualClock) Now() model.Timestamp {
	c.mu.Lock()
	defer c.mu.Unlock()
	return model.NewTimestamp(c.t)
}

func (c *manualClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// fakeGate is a programmable ApprovalGate for the fire tests.
type fakeGate struct {
	status    GateStatus
	planHash  string // if non-empty, Status echoes this (to simulate a stale-plan approval)
	emptyHash bool   // if set, Status echoes an EMPTY plan hash (a partial/buggy gate)
}

func (g fakeGate) Request(_ context.Context, req ApprovalRequest) (GateDecision, error) {
	return GateDecision{ApprovalRef: "appr-1", Status: g.status, PlanHash: req.PlanHash}, nil
}

func (g fakeGate) Status(_ context.Context, chk ApprovalCheck) (GateDecision, error) {
	ph := chk.PlanHash
	switch {
	case g.emptyHash:
		ph = ""
	case g.planHash != "":
		ph = g.planHash
	}
	return GateDecision{ApprovalRef: chk.ApprovalRef, Status: g.status, PlanHash: ph}, nil
}

// fakeDispatcher is a programmable Dispatcher for the fire tests.
type fakeDispatcher struct {
	ref string
	err error
}

func (d fakeDispatcher) Fire(_ context.Context, _ FireRequest) (DispatchResult, error) {
	if d.err != nil {
		return DispatchResult{}, d.err
	}
	return DispatchResult{Ref: d.ref}, nil
}

// fakeBudgetGate is a programmable BudgetGate for the FIN-08 enforcement tests.
type fakeBudgetGate struct {
	decision BudgetDecision
	err      error
}

func (g fakeBudgetGate) Check(_ context.Context, _ model.TenantID, _ BudgetDims) (BudgetDecision, error) {
	return g.decision, g.err
}

// recordingDispatcher records whether Fire was ever called, so a test can prove the
// budget pre-flight denied a fire BEFORE it reached the dispatcher.
type recordingDispatcher struct{ fired *bool }

func (d recordingDispatcher) Fire(_ context.Context, _ FireRequest) (DispatchResult, error) {
	*d.fired = true
	return DispatchResult{Ref: "should-not-actuate"}, nil
}

// harness wires a fully-usable orchestration plane: a real in-memory store, the
// module bound to a bus, and an HTTP server.
type harness struct {
	t        *testing.T
	srv      *api.Server
	st       store.Store
	bus      eventbus.Bus
	setupTok string

	findMu   sync.Mutex
	findings []sdkmodel.FindingReport
}

func newHarness(t *testing.T, opts ...Option) (*harness, *Module) {
	t.Helper()
	ctx := context.Background()
	h := &harness{t: t}

	// D-06: all orchestration tests get a working target-binding key so
	// acting steps can freeze+verify their target; a test may still override.
	opts = append([]Option{WithTargetBindingKey(NewStaticMACKey([]byte("s467-test-target-binding-key"), "test-key-1"))}, opts...)
	mod := New(opts...)
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
	signer, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatal(err)
	}
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
	return h, mod
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

// publishEdge emits an edge.observed on the bus from a connector source.
func (h *harness) publishEdge(tenant model.TenantID, e sdkmodel.EdgeObservation) {
	h.t.Helper()
	_ = h.bus.Publish(context.Background(), event.FromObservation(tenant.String(), "connector:test", e))
}

// delegationEdge is a Claude Code Task delegation edge (supervisor session → worker).
func delegationEdge(session, subagent string, at time.Time) sdkmodel.EdgeObservation {
	return sdkmodel.EdgeObservation{
		OriginKind: "session", OriginRef: session, ResourceKind: "agent.task", ResourceRef: subagent,
		Mode: sdkmodel.ModeUnknown, Source: sdkmodel.SignalOTEL, Confidence: sdkmodel.ConfidenceAttributed,
		ToolRef: "Task", ObservedAt: at,
	}
}

// waitForGraphEdges polls GET /graph until at least n edges are present (the bus is
// async), returning the final response.
func (h *harness) waitForGraphEdges(token string, tenant model.TenantID, n int) graphResponse {
	h.t.Helper()
	for i := 0; i < 200; i++ {
		r := h.do("GET", "/v1/m/orchestration/graph", token, nil, tenantHdr(tenant))
		if r.code == http.StatusOK {
			var g graphResponse
			_ = json.Unmarshal([]byte(r.raw), &g)
			if len(g.Edges) >= n {
				return g
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.t.Fatalf("graph never reached %d edges", n)
	return graphResponse{}
}

// waitForFinding polls the captured bus findings for one of the given kind.
func (h *harness) waitForFinding(kind string) bool {
	h.t.Helper()
	for i := 0; i < 200; i++ {
		h.findMu.Lock()
		for _, f := range h.findings {
			if f.Kind == kind {
				h.findMu.Unlock()
				return true
			}
		}
		h.findMu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	return false
}
