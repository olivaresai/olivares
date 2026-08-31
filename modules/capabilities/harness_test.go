// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package capabilities_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/eventbus"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/capabilities"
	"github.com/olivaresai/olivares/modules/inventory"
	"github.com/olivaresai/olivares/sdk"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// fakeSource emits a scripted batch of observations, exercising the full path
// connector → runtime sink → bus → module → store, so the modules are proven to
// load via the runtime and react to what they consume.
type fakeSource struct {
	obs     []sdkmodel.Observation
	done    chan struct{}
	emitted atomic.Uint64
}

func newFakeSource(obs []sdkmodel.Observation) *fakeSource {
	return &fakeSource{obs: obs, done: make(chan struct{})}
}

func (f *fakeSource) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: "test.cap-source", Version: "0.0.1", APIVersion: sdk.APIVersion, Type: sdk.TypeSource}
}
func (f *fakeSource) Open(context.Context, sdk.Config) error { return nil }
func (f *fakeSource) Gather(ctx context.Context, sink sdk.Sink) error {
	defer close(f.done)
	for _, o := range f.obs {
		if err := sink.Emit(ctx, o); err != nil {
			return err
		}
		f.emitted.Add(1)
	}
	return nil
}
func (f *fakeSource) Close(context.Context) error { return nil }

// harness wires a real store + API server with the capabilities module (and the
// inventory module, whose materialization the capabilities catalog reads).
type harness struct {
	t        *testing.T
	srv      *api.Server
	st       store.Store
	cap      *capabilities.Module
	inv      *inventory.Module
	setupTok string
}

// harnessSecurity holds the security material that belongs to one logical API
// installation. A file-backed fixture that closes and reopens its store must reuse
// these values just as a restarted process would.
type harnessSecurity struct {
	signer       *audit.Signer
	setupToken   *secure.SetupToken
	setupTokText string
}

func newHarnessSecurity(t *testing.T) harnessSecurity {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatal(err)
	}
	setupToken := secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token"))
	setupTokText, _, err := setupToken.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	return harnessSecurity{
		signer: signer, setupToken: setupToken, setupTokText: setupTokText,
	}
}

func newHarness(t *testing.T, opts ...capabilities.Option) *harness {
	return newHarnessWithConfig(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, opts...)
}

// newHarnessWithConfig is newHarness with an explicit store.Config, so a test can
// exercise the audit-spool degrade/block policies (ADR-0024 Q2) that drive the
// evidence-or-refuse path.
func newHarnessWithConfig(t *testing.T, cfg store.Config, opts ...capabilities.Option) *harness {
	t.Helper()
	return openHarness(t, cfg, newHarnessSecurity(t), true, opts...)
}

// openHarness builds one server generation with caller-owned security material.
// cleanupStore is false only for a fixture that closes the store before reopening
// the same file in a second generation.
func openHarness(
	t *testing.T,
	cfg store.Config,
	security harnessSecurity,
	cleanupStore bool,
	opts ...capabilities.Option,
) *harness {
	t.Helper()
	ctx := context.Background()
	cap := capabilities.New(opts...)
	inv := inventory.New()
	register := func(reg store.ExtensionRegistry) error {
		if err := cap.RegisterSchema(reg); err != nil {
			return err
		}
		return inv.RegisterSchema(reg)
	}
	st, err := engine.Open(ctx, cfg, register)
	if err != nil {
		t.Fatal(err)
	}
	closeOnFailure := !cleanupStore
	defer func() {
		if closeOnFailure {
			_ = st.Close()
		}
	}()
	if cleanupStore {
		t.Cleanup(func() { _ = st.Close() })
	}
	if err := st.System(ctx, func(sys store.SystemScope) error { _, e := sys.EnsureSystemTenant(ctx); return e }); err != nil {
		t.Fatal(err)
	}
	cap.UseData(api.NewModuleData(st))
	inv.UseData(api.NewModuleData(st))

	srv, err := api.New(api.Options{
		Store: st, Authenticator: auth.NewAuthenticator(st, nil), Authorizer: auth.NewAuthorizer(nil),
		Signer: security.signer, SetupToken: security.setupToken, Version: "test", Modules: []api.Module{cap},
	})
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{
		t: t, srv: srv, st: st, cap: cap, inv: inv,
		setupTok: security.setupTokText,
	}
	closeOnFailure = false
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

// errorCode pulls the STABLE machine code out of the error envelope, or "" when the
// response carries none. Tests assert on this rather than on the message: a message is
// prose that may be reworded or localized, and a status alone collapses answers the
// caller must tell apart (three different 409s on the pin actuator).
func errorCode(r resp) string {
	env, _ := r.body["error"].(map[string]any)
	code, _ := env["code"].(string)
	return code
}

func tenantHdr(t model.TenantID) map[string]string {
	return map[string]string{"X-Olivares-Tenant": t.String()}
}

// tenantIdem is tenantHdr plus an Idempotency-Key (required by the D-09 pin actuators).
func tenantIdem(t model.TenantID, key string) map[string]string {
	return map[string]string{"X-Olivares-Tenant": t.String(), "Idempotency-Key": key}
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

// roleToken creates a user with role of tenant and returns its session token.
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

func newTrackedBus(t *testing.T) (eventbus.Bus, eventbus.StatsProvider) {
	t.Helper()
	bus := eventbus.NewInProc(eventbus.Options{})
	stats, ok := bus.(eventbus.StatsProvider)
	if !ok {
		t.Fatal("in-process event bus does not expose completion statistics")
	}
	return bus, stats
}

// waitObservations is the delivery barrier for scripted integration tests. A
// source finishing proves every scripted observation reached Publish; Handled
// catching Enqueued proves every matching module handler then returned. Neither
// condition assumes the catalog state that the caller will assert.
func (h *harness) waitObservations(src *fakeSource, stats eventbus.StatsProvider, want int) {
	h.t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()

	select {
	case <-src.done:
	case <-deadline.C:
		st := stats.BusStats()
		emitted := src.emitted.Load()
		h.t.Fatalf(
			"source emitted %d/%d observations before deadline; bus processed %d/%d enqueued deliveries",
			emitted, want, st.Handled, st.Enqueued,
		)
	}
	if emitted := src.emitted.Load(); emitted != uint64(want) {
		h.t.Fatalf("source stopped after emitting %d/%d observations", emitted, want)
	}

	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		st := stats.BusStats()
		if st.Handled == st.Enqueued {
			if st.HandlerErrors != 0 {
				h.t.Fatalf(
					"all %d bus deliveries for %d observations completed, but %d handlers failed",
					st.Handled, want, st.HandlerErrors,
				)
			}
			return
		}
		select {
		case <-deadline.C:
			h.t.Fatalf(
				"bus processed %d/%d enqueued deliveries for %d observations; %d deliveries still pending",
				st.Handled, st.Enqueued, want, st.Enqueued-st.Handled,
			)
		case <-poll.C:
		}
	}
}

// waitServers blocks until at least n MCP servers have been materialized in the
// tenant (by the inventory module reacting to the emitted edges). It checks an
// output expectation; it is not a delivery barrier for unrelated observations.
func (h *harness) waitServers(tenant model.TenantID, n int) {
	h.t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		count := 0
		_ = h.st.View(context.Background(), tenant, func(sc store.Scope) error {
			ms, _, err := sc.MCPServers().List(context.Background(), model.Query{Limit: 100})
			count = len(ms)
			return err
		})
		if count >= n {
			return
		}
		select {
		case <-deadline:
			h.t.Fatalf("estate reached %d MCP servers, want >= %d", count, n)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// waitWiring blocks until at least n wiring edges exist for the tenant (the
// capabilities module reacting to the same edges). It checks an output
// expectation; duplicate or irrelevant observations do not increase this count.
func (h *harness) waitWiring(tenant model.TenantID, n int) {
	h.t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		count := 0
		_ = h.st.View(context.Background(), tenant, func(sc store.Scope) error {
			repo, err := sc.Ext("capabilities.wiring")
			if err != nil {
				return err
			}
			recs, _, err := repo.List(context.Background(), model.Query{Limit: 200})
			count = len(recs)
			return err
		})
		if count >= n {
			return
		}
		select {
		case <-deadline:
			h.t.Fatalf("reached %d wiring edges, want >= %d", count, n)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// edge builds an EdgeObservation for the fake source.
func edge(originKind, originRef, resKind, resRef string, mode sdkmodel.AccessMode, src sdkmodel.SignalSource, conf sdkmodel.Confidence, toolRef string, at time.Time) sdkmodel.EdgeObservation {
	return sdkmodel.EdgeObservation{
		OriginKind: originKind, OriginRef: originRef,
		ResourceKind: resKind, ResourceRef: resRef,
		Mode: mode, Source: src, Confidence: conf, ToolRef: toolRef, ObservedAt: at,
	}
}

// healthFinding builds a health FindingReport for the fake source.
func healthFinding(subjectKind, subjectRef, title string, sev sdkmodel.Severity, at time.Time) sdkmodel.FindingReport {
	return sdkmodel.FindingReport{
		Kind: "health", Severity: sev, SubjectKind: subjectKind, SubjectRef: subjectRef,
		Title: title, OccurredAt: at,
	}
}

// items returns the items array of a list response body.
func items(r resp) []any {
	if r.body == nil {
		return nil
	}
	out, _ := r.body["items"].([]any)
	return out
}
