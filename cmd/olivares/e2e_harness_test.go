// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

// e2e_harness_test.go is the reproducible in-process E2E harness. It stands
// up the REAL engine — the same composition root the binary boots (boot.go /
// wire.go), the same 19 modules, the same in-proc event bus, the same API/auth —
// and drives its HTTP surfaces (setup token → login → bearer +
// X-Olivares-Tenant), with real store fixtures for tenant isolation. The demo
// estate enters through the REAL channel: a seed SourceConnector registered with the runtime BEFORE Start, whose
// Emit the runtime lifts onto the bus identically to a live pg-audit/OTEL
// collector, fanning out to every subscribing module (the decoupled-by-events
// integration the product rests on).
//
// Why this mirrors boot() instead of calling it: boot() calls rt.Start()
// internally, after which AddSource returns ErrAlreadyStarted (runtime.go:160).
// To inject the seed source through the bus, the source must be registered
// before Start, AND for an agent-origin edge to attribute firmly the agent must
// already exist — so the sequence is: assemble → API setup/login → store tenant
// fixtures → API agents → AddSource(tenant) → Start. That ordering is only expressible by
// inlining the wiring; it reuses buildModules() (the production module set) so it
// cannot drift in WHICH modules run. TestCompositionRootBootsAndWiresModules
// guards boot()'s own wiring.
//
// Bus delivery is asynchronous. The harness owns the bus, so once the seed
// source returns it waits on the bus's Enqueued/Handled completion barrier
// before asserting derived read models; individual assertions still poll until
// they converge, exactly how a real operator watches data appear.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/seed"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/eventbus"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
)

// seedSourceA is the registration name of tenant A's seeded source.
const seedSourceA = "olivares.seed-demo.A"

// harness is a running in-process engine plus the credentials the E2E suite
// drives it with.
type harness struct {
	t     *testing.T
	h     http.Handler
	rt    *runtime.Runtime
	stats eventbus.StatsProvider
	st    store.Store
	authr *auth.Authenticator
	now   time.Time

	adminToken string // a superadmin session (every tenant permission)
	tenantA    string // the fully-seeded business tenant
	tenantB    string // a second, empty tenant for isolation assertions

	// set is the production module set, for tests that drive a module's
	// exported composition-root surface directly (e.g. the pump).
	set moduleSet
}

// newHarness assembles the engine, bootstraps auth + two tenants, pre-creates the
// estate's agents in tenant A, registers the demo seed source for tenant A, starts
// the runtime, and blocks until the seeded estate has materialized.
func newHarness(t *testing.T) *harness {
	return newHarnessWithRecorder(t, nil)
}

// newHarnessWithRecorder allows a test-only decorator around the production
// recorder before api.New captures it. The underlying recording module and all
// HTTP/store/runtime wiring remain real.
func newHarnessWithRecorder(t *testing.T, wrap func(api.SessionRecorder) api.SessionRecorder, capturedLog ...*slog.Logger) *harness {
	t.Helper()
	return newHarnessWithBus(t, nil, wrap, capturedLog...)
}

// newHarnessWithBus is newHarnessWithRecorder with an injectable event bus; nil
// builds the same in-process bus the runtime would build itself. The harness
// keeps the bus's StatsProvider so it can wait on the Enqueued/Handled
// completion barrier; a bus without it cannot offer that guarantee.
func newHarnessWithBus(t *testing.T, bus eventbus.Bus, wrap func(api.SessionRecorder) api.SessionRecorder, capturedLog ...*slog.Logger) *harness {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	if len(capturedLog) > 0 {
		log = capturedLog[0]
	}
	now := time.Now()
	if bus == nil {
		bus = eventbus.NewInProc(eventbus.Options{Logger: log})
	}
	busStats, ok := bus.(eventbus.StatsProvider)
	if !ok {
		t.Fatalf("harness bus %T does not implement eventbus.StatsProvider", bus)
	}

	// --- assemble (mirrors boot.go:54-153, reusing the production module set) ---
	priv, _, err := secure.LoadOrCreateSigningKey(filepath.Join(dir, "audit-signing.key"))
	if err != nil {
		t.Fatalf("signing key: %v", err)
	}
	signer, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}

	rt := runtime.New(runtime.Options{Logger: log, Bus: bus})
	editionCfg := EditionConfig{DataDir: dir}
	set, err := buildModules(nil, signer, nil, nil, nil, nil, nil, sourcesConfig{}, editionCfg, dir, log)
	if err != nil {
		t.Fatalf("build modules: %v", err)
	}
	for _, m := range set.all {
		if err := rt.AddModule(m.(sdk.Module), sdk.Config{}); err != nil {
			t.Fatalf("add module %q: %v", m.APINamespace(), err)
		}
	}

	st, err := coreengine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, rt.RegisterSchema)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.System(ctx, func(sys store.SystemScope) error {
		_, e := sys.EnsureSystemTenant(ctx)
		return e
	}); err != nil {
		t.Fatalf("system tenant: %v", err)
	}

	data := api.NewModuleData(st)
	for _, m := range set.all {
		if dc, ok := m.(api.DataConsumer); ok {
			dc.UseData(data)
		}
	}
	if set.knowledgeGuard != nil {
		set.knowledgeGuard.useData(data)
	}
	if set.knowledgeEmbedder != nil {
		set.knowledgeEmbedder.UseData(data)
	}
	if set.knowledgeStatus != nil {
		set.knowledgeStatus.useGuardPostureStore(st, set.sourceScopeResolver)
	}

	authr := auth.NewAuthenticator(st, nil)
	// wire the production-equivalent authorizer (deny-overlay + per-tenant scoped
	// grants) so e2e tests exercise the real authorization path, not only the overlay.
	authz := auth.NewAuthorizer(set.gov.RequestEvaluator(), auth.WithScopedGrants(set.gov.ScopedGrants()))
	// Bind the same live edition dependencies as boot before routes or modules start.
	fedSealer, err := newFederationSealer(dir, osGetenv)
	if err != nil {
		t.Fatalf("federation sealer: %v", err)
	}
	fedSvc := auth.NewFederationService(st, fedSealer, newFederationBuilder(), newFederation(osGetenv, log), thisEdition.federationMultiIDP.get())
	secretSealer, err := newSecretSealer(dir, osGetenv)
	if err != nil {
		t.Fatalf("secret sealer: %v", err)
	}
	secretStore := auth.NewSecretStore(st, secretSealer)
	if err := bindManagedSCIMModules(set.all, st, authr, secretStore, authz); err != nil {
		t.Fatalf("bind managed SCIM dependencies: %v", err)
	}
	secretResolver := newSecretResolver(secretStore, osGetenv, log)
	var editionResources []io.Closer
	if thisEdition.bindModuleDependencies != nil {
		editionResources, err = thisEdition.bindModuleDependencies(ctx, editionCfg, set.all, EditionDependencies{
			Store: st, Sessions: set.sessions, RuntimeLaunches: set.sessions, modules: &set,
			Rows: api.NewReadRowAuthorizationPort(authz, authr), Mutations: authz,
			Principals: authr, Governance: set.gov, Secrets: secretResolver,
			Authenticator: authr, FederationService: fedSvc, SecretStore: secretStore,
			ProductSettings: newProductSettings(st, dir),
		}, log)
		if err != nil {
			t.Fatalf("bind edition module dependencies: %v", err)
		}
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
		// The runtime does not own an injected bus; close it here so its
		// subscriber goroutines cannot outlive the store they write through.
		if err := bus.Close(); err != nil {
			t.Errorf("close event bus: %v", err)
		}
		if err := closeEditionResources(editionResources, st.Leader(), log); err != nil {
			t.Errorf("close edition resources: %v", err)
		}
		_ = st.Close()
	})
	// wire the eventing engine's late-bound seams exactly as production
	// boot() does, so e2e tests can exercise webhook and SIEM-sink subscriptions.
	// The sink renderer is wired at construction (buildModules); the authorizer and
	// the secret sealer are late-bound here (the sealer key comes from
	// OLIVARES_EVENTING_SECRET_KEY or a data-dir key file).
	set.eventing.UseAuthorizer(authz)
	if sealer, serr := newEventingSealer(dir, osGetenv); serr == nil {
		set.eventing.UseSecretSealer(sealer)
	}
	// Units G and H: the two STORE-DEPENDENT seams, wired here because they cannot be wired
	// at buildModules time — the store does not exist yet there.
	//
	// They were missing, and the comment above claimed this harness wired eventing's late-bound
	// seams "exactly as production boot() does" while it did not. The cost was concrete rather
	// than cosmetic: without the rollout seam the e2e composition never exercised the destination
	// control at all, and without the fence seam the module built its writer proof against
	// generation ZERO, so every governed write here was refused the moment the fence's triggers
	// started enforcing. Unit H's enforcement is what surfaced it; nothing else would have.
	//
	// boot() treats a store without the capability as a boot FAILURE. Here it is a test failure,
	// for the same reason: a seam that silently goes unwired is how a control ends up present in
	// the code and absent from the binary's behavior.
	rollout, rok := newEventingEgressRollout(st)
	if !rok {
		t.Fatal("the store does not expose durable rollout state, so this harness cannot mirror boot()")
	}
	set.eventing.UseEgressRollout(rollout)
	fence, fok := newEventingWriterFence(st)
	if !fok {
		t.Fatal("the store does not expose durable rollout state, so the writer fence cannot be wired")
	}
	set.eventing.UseEgressWriterFence(fence)
	// open the deferred secret-bearing connectors (notify destinations,
	// knowledge document sources, claude-agents readers) the same way boot() does,
	// now that the store exists. A nil resolver passes literal config through (the
	// e2e provisioning files use non-secret literals); a store: reference would fail
	// closed here (the harness wires no secret store).
	set.deferredSecrets.openAll(ctx, nil, log)
	setupTok := secure.NewSetupToken(filepath.Join(dir, "setup.token"))
	var sessionRecorder api.SessionRecorder = set.recorder
	if wrap != nil {
		sessionRecorder = wrap(sessionRecorder)
		if sessionRecorder == nil {
			t.Fatal("recorder wrapper returned nil")
		}
	}
	apiSrv, err := api.New(api.Options{
		Store: st, Authenticator: authr, Authorizer: authz, Signer: signer,
		PrincipalEvidenceProducer: authr,
		CoreEntityResolver:        coreEntityResolver{st: st},
		SetupToken:                setupTok, Logger: log, Version: "e2e", Modules: set.all,
		KnowledgeStatus: set.knowledgeStatus,
		// mirror production — module routes are recorded through the wired
		// recorder (break-glass e2e depends on the deny-closed gate being real).
		Recorder: sessionRecorder,
	})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}

	set.catalogDeploy.handler = apiSrv.Handler()

	h := &harness{t: t, h: apiSrv.Handler(), rt: rt, stats: busStats, st: st, authr: authr, now: now, set: set}

	// --- API bootstrap over the real handler (setup token → admin → login) ---
	tok, _, err := setupTok.Ensure()
	if err != nil {
		t.Fatalf("setup token: %v", err)
	}
	if code, _ := h.req("POST", "/v1/setup", "", "", map[string]any{
		"token": tok, "email": "admin@e2e.test", "password": "supersecret-e2e",
	}); code != http.StatusCreated {
		t.Fatalf("setup = %d", code)
	}
	var login struct {
		Token string `json:"token"`
	}
	if code := h.reqInto("POST", "/v1/auth/login", "", "", map[string]any{
		"email": "admin@e2e.test", "password": "supersecret-e2e",
	}, &login); code != http.StatusOK || login.Token == "" {
		t.Fatalf("login = %d token=%q", code, login.Token)
	}
	h.adminToken = login.Token
	// CRITICAL decisions, break-glass activation and deploy mutations
	// demand an AAL3 session; the suite's operators are step-up-verified
	// through the real elevation path (the ceremonies themselves are covered
	// by the core/api WebAuthn/PIV tests; the under-assured denials by the
	// governance/deploy step-up tests).
	h.stepUp(h.adminToken)
	h.tenantA = h.createOrg("Acme Robotics", "acme")
	h.tenantB = h.createOrg("Globex", "globex")

	// Pre-create the cooperative agents through the REAL POST /v1/agents channel so
	// agent-origin edges attribute firmly (the bridge resolves by external id). They
	// must exist before the source emits.
	for _, a := range seed.Agents() {
		if code, _ := h.req("POST", "/v1/agents", h.adminToken, h.tenantA, map[string]any{
			"name": a.Name, "kind": a.Kind, "external_id": a.ExternalID, "status": "active",
		}); code != http.StatusCreated {
			t.Fatalf("create agent %q = %d", a.ExternalID, code)
		}
	}

	// --- register the seed source (real bus channel) for tenant A, then Start ---
	if err := rt.AddSource(seed.NewSource(seedSourceA, now), sdk.Config{}, h.tenantA); err != nil {
		t.Fatalf("add seed source: %v", err)
	}
	if err := rt.Start(ctx); err != nil {
		t.Fatalf("runtime start: %v", err)
	}
	// No source failed to open/start.
	for _, cs := range rt.Status() {
		if cs.Status == runtime.StatusFailed {
			t.Fatalf("component %q failed: %s", cs.Name, cs.Err)
		}
	}

	// Block until the estate has settled.
	h.waitForGraph(h.tenantA)
	return h
}

// stepUp elevates a session token to AAL3 through the real elevation path
// : the e2e operators act over step-up-verified sessions like a
// production human would after the WebAuthn/PIV ceremony.
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

// createOrg seeds a real tenant for isolation assertions and returns its id.
func (h *harness) createOrg(name, slug string) string {
	h.t.Helper()
	return createOrganizationFixture(h.t, h.st, name, slug).TenantID.String()
}

// req issues one request against the real handler and returns the status + raw
// body. token/tenant are optional (empty → header omitted).
func (h *harness) req(method, path, token, tenant string, body any) (int, []byte) {
	h.t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			h.t.Fatalf("marshal body: %v", err)
		}
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	r := httptest.NewRequest(method, path, rdr)
	r.RemoteAddr = "10.0.0.1:4321" // login throttle keys on the client IP
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if tenant != "" {
		r.Header.Set("X-Olivares-Tenant", tenant)
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.h.ServeHTTP(rec, r)
	return rec.Code, rec.Body.Bytes()
}

// reqInto issues a request and decodes the JSON body into out (ignored on a
// decode error so callers can still inspect the status).
func (h *harness) reqInto(method, path, token, tenant string, body, out any) int {
	code, raw := h.req(method, path, token, tenant, body)
	if out != nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, out)
	}
	return code
}

// getJSON GETs path as a tenant-scoped map; it fails the test on a non-200.
func (h *harness) getJSON(token, tenant, path string) map[string]any {
	h.t.Helper()
	code, raw := h.req("GET", path, token, tenant, nil)
	if code != http.StatusOK {
		h.t.Fatalf("GET %s = %d: %s", path, code, raw)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		h.t.Fatalf("GET %s decode: %v (%s)", path, err, raw)
	}
	return m
}

// servesFinOpsReads reports whether this edition serves FinOps reads. FinOps is
// a Business feature: Community mounts its routes and answers them 501, which is
// asserted here on each path, so a Community build that serves them again fails.
func (h *harness) servesFinOpsReads(paths ...string) bool {
	h.t.Helper()
	if thisEdition.name != "community" {
		return true
	}
	for _, path := range paths {
		if code, raw := h.req("GET", path, h.adminToken, h.tenantA, nil); code != http.StatusNotImplemented {
			h.t.Errorf("Community GET %s = %d, want 501: %s", path, code, raw)
		}
	}
	return false
}

// items returns the "items" array of a list response as []map[string]any.
func items(m map[string]any) []map[string]any {
	raw, _ := m["items"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, it := range raw {
		if obj, ok := it.(map[string]any); ok {
			out = append(out, obj)
		}
	}
	return out
}

// eventually polls fn every 25ms until it returns nil or the deadline passes,
// failing with the last error. It is a liveness poll, not a completion barrier:
// waitForBusDrain owns the async-bus barrier.
func (h *harness) eventually(what string, timeout time.Duration, fn func() error) {
	h.t.Helper()
	deadline := time.Now().Add(timeout)
	var last error
	for {
		if last = fn(); last == nil {
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("eventually(%s) timed out after %s: %v", what, timeout, last)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// busDrainTimeout bounds the liveness wait for the seed batch, not the batch
// itself: the bus completion barrier decides when delivery is done. It is
// generous because a loaded host can take tens of seconds to run the handlers;
// it turns a wedged handler into a clear failure instead of a package timeout.
const busDrainTimeout = 2 * time.Minute

// waitForBusDrain waits until the one-shot seed source has returned from Gather
// and the bus has no event queued or in flight. Stats.Enqueued/Handled are the
// bus's completion barrier (core/eventbus/stats.go): the source status proves
// every seed event has been placed, then a single snapshot with Handled >=
// Enqueued proves none of the events placed so far is still outstanding.
// BusStats loads Handled before Enqueued, so handler completions for events
// published after the Handled read cannot satisfy the snapshot: derived events
// published by handlers are covered too. A fixed sleep or a fixed deadline on a
// derived read model is not that proof.
func (h *harness) waitForBusDrain() {
	h.t.Helper()
	h.eventually("seed source finished gathering", busDrainTimeout, func() error {
		for _, cs := range h.rt.Status() {
			if cs.Type != sdk.TypeSource || cs.Name != seedSourceA {
				continue
			}
			switch cs.Status {
			case runtime.StatusFailed:
				return fmt.Errorf("seed source failed: %s", cs.Err)
			case runtime.StatusStopped:
				return nil
			default:
				return fmt.Errorf("seed source is %s", cs.Status)
			}
		}
		return fmt.Errorf("seed source %q is not registered", seedSourceA)
	})
	h.eventually("seed events handled to completion", busDrainTimeout, func() error {
		s := h.stats.BusStats()
		if s.Handled < s.Enqueued {
			return fmt.Errorf("handled %d of %d events placed on the bus (handler errors %d)", s.Handled, s.Enqueued, s.HandlerErrors)
		}
		return nil
	})
	if errs := h.stats.BusStats().HandlerErrors; errs > 0 {
		h.t.Fatalf("seed drain: %d handler invocation(s) returned an error or panicked", errs)
	}
}

// waitForGraph blocks until the seed source's derived read-models needed by the E2E
// suite are materialized. It avoids exact counts, but does require the access graph,
// inventory model/provider rows, and both seeded live-session rows to have drained.
func (h *harness) waitForGraph(tenant string) {
	h.t.Helper()
	// Wait for delivery to finish instead of guessing how long it takes: the
	// read-model polls below assert the end state the handlers produce, not the
	// timing of the async pipeline (the regression: a fixed 10s grace period expired on
	// a loaded CI host while every event was still in flight or queued).
	h.waitForBusDrain()
	h.eventually("estate settled (access graph + inventory catalog)", 10*time.Second, func() error {
		if err := h.editionGraphReady(tenant); err != nil {
			return err
		}
		// (b) The inventory catalog reflects the model/provider the cost samples
		// materialize. Those CostSample observations are emitted AFTER all the edges
		// in the seed batch, so the archive edge alone is NOT a full-drain signal
		// (it left an intermittent model/provider/cost=0 race); requiring the catalog
		// too is the strictly-later "batch drained" condition.
		icode, iraw := h.req("GET", "/v1/m/inventory/summary", h.adminToken, tenant, nil)
		if icode != http.StatusOK {
			return fmt.Errorf("inventory = %d: %s", icode, iraw)
		}
		var sum struct {
			ByKind map[string]struct {
				Total int `json:"total"`
			} `json:"by_kind"`
		}
		_ = json.Unmarshal(iraw, &sum)
		if sum.ByKind["model"].Total < 1 || sum.ByKind["provider"].Total < 1 {
			return fmt.Errorf("model/provider catalog not yet materialized (cost samples not drained)")
		}
		// (c) The sessions read-model drains independently from access-map/inventory. The
		// live-operation tests need both seeded rows and the anti-evasion finding applied.
		lcode, lraw := h.req("GET", "/v1/m/sessions/live?limit=50", h.adminToken, tenant, nil)
		if lcode != http.StatusOK {
			return fmt.Errorf("live sessions = %d: %s", lcode, lraw)
		}
		var live struct {
			Items []map[string]any `json:"items"`
		}
		_ = json.Unmarshal(lraw, &live)
		foundLive := false
		foundEvade := false
		for _, item := range live.Items {
			switch item["session_ref"] {
			case seed.SessionLive:
				state, _ := item["cc_state"].(string)
				foundLive = state == "active" || state == "idle"
			case seed.SessionEvade:
				foundEvade = item["cc_state"] == "silent_evasion"
			}
		}
		if !foundLive || !foundEvade {
			return fmt.Errorf("seeded live sessions not yet materialized (live=%v evade=%v items=%d)", foundLive, foundEvade, len(live.Items))
		}
		return nil
	})
}
