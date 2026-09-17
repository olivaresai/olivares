// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// Keep this beside the existing mounted Sessions harness: the test needs the
// real fold and broker counters as well as the HTTP bytes, with no product seam.
// Governance uses its real deny overlay and scoped-grant engine, no authored
// policies/grants. Recorder is explicitly not required in this fixture.
type streamConfinementFixture struct {
	*harness
	authr  *auth.Authenticator
	authz  *auth.Authorizer
	server *httptest.Server
}

func newStreamConfinementFixture(t *testing.T, cfg store.Config) *streamConfinementFixture {
	t.Helper()
	m, gov := New(), governance.New()
	ctx := context.Background()
	st, err := engine.Open(ctx, cfg, func(reg store.ExtensionRegistry) error {
		if err := m.RegisterSchema(reg); err != nil {
			return err
		}
		return gov.RegisterSchema(reg)
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(sys store.SystemScope) error {
		_, err := sys.EnsureSystemTenant(ctx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	m.UseData(api.NewModuleData(st))
	gov.UseData(api.NewModuleData(st))
	stopModuleAtCleanup(t, m)
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := audit.NewSigner(key)
	if err != nil {
		t.Fatal(err)
	}
	setup := secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token"))
	plaintext, _, err := setup.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	authr := auth.NewAuthenticator(st, nil)
	authz := auth.NewAuthorizer(gov.RequestEvaluator(), auth.WithScopedGrants(gov.ScopedGrants()))
	srv, err := api.New(api.Options{
		Store: st, Authenticator: authr, Authorizer: authz, Signer: signer,
		SetupToken: setup, Version: "test", Modules: []api.Module{m, gov},
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	t.Log("mounted Sessions + Governance; real auth/RBAC/scoped grants; no authored policies; recorder not required")
	return &streamConfinementFixture{
		harness: &harness{t: t, m: m, srv: srv, st: st, setupTok: plaintext},
		authr:   authr, authz: authz, server: ts,
	}
}

func (f *streamConfinementFixture) member(t *testing.T, admin string, tenant model.TenantID, email string, ws model.ID) string {
	t.Helper()
	r := f.doJSON("POST", "/v1/users", admin, map[string]any{"email": email, "password": "memberpass1"}, nil)
	if r.code != http.StatusCreated {
		t.Fatalf("create user = %d %s", r.code, r.raw)
	}
	r = f.doJSON("POST", "/v1/memberships", admin, map[string]any{
		"user_id": r.body["id"], "tenant": tenant.String(), "role": auth.RoleEditor, "workspace_id": ws.String(),
	}, nil)
	if r.code != http.StatusCreated {
		t.Fatalf("create membership = %d %s", r.code, r.raw)
	}
	r = f.doJSON("POST", "/v1/auth/login", "", map[string]any{"email": email, "password": "memberpass1"}, nil)
	if r.code != http.StatusOK {
		t.Fatalf("login = %d %s", r.code, r.raw)
	}
	return r.body["token"].(string)
}

func (f *streamConfinementFixture) get(t *testing.T, path, token string, tenant model.TenantID) *http.Response {
	t.Helper()
	// Same 3s bound as TestSessionsStream. Cancellation is registered after the
	// server cleanup, so a failed assertion cannot leave an open handler behind.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, "GET", f.server.URL+"/v1/m/sessions/"+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("X-Olivares-Tenant", tenant.String())
	res, err := f.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	return res
}

func streamBrokerCounts(m *Module) (next, subscribers int) {
	m.broker.mu.Lock()
	defer m.broker.mu.Unlock()
	return m.broker.next, len(m.broker.subs)
}

func (f *streamConfinementFixture) publish(t *testing.T, tenant model.TenantID, ref, sentinel string) {
	t.Helper()
	if err := f.m.onEdge(context.Background(), tenant.String(), sessEdge(ref, "file", sentinel, sdkmodel.ModeRead, "Read", time.Now())); err != nil {
		t.Fatal(err)
	}
	t.Logf("fold committed and published tenant=%s ref=%s sentinel=%s", tenant, ref, sentinel)
}

func streamConnected(t *testing.T, res *http.Response) *bufio.Reader {
	t.Helper()
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "text/event-stream" {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("stream = %d headers=%v body=%s", res.StatusCode, res.Header, body)
	}
	r := bufio.NewReader(res.Body)
	for _, want := range []string{": connected\n", "\n"} {
		line, err := r.ReadString('\n')
		if err != nil || line != want {
			t.Fatalf("connected = %q, %v; want %q", line, err, want)
		}
	}
	t.Logf("subscription acknowledged: status=%d headers=%v prelude=: connected", res.StatusCode, res.Header)
	return r
}

func streamSession(t *testing.T, r *bufio.Reader) liveDTO {
	t.Helper()
	line, err := r.ReadString('\n')
	if err != nil || line != "event: session\n" {
		t.Fatalf("event = %q, %v", line, err)
	}
	data, err := r.ReadString('\n')
	if err != nil || !strings.HasPrefix(data, "data: ") {
		t.Fatalf("data = %q, %v", data, err)
	}
	var dto liveDTO
	if err := json.Unmarshal([]byte(strings.TrimPrefix(data, "data: ")), &dto); err != nil {
		t.Fatal(err)
	}
	if line, err := r.ReadString('\n'); err != nil || line != "\n" {
		t.Fatalf("frame terminator = %q, %v", line, err)
	}
	t.Logf("captured event: session\n%s", strings.TrimSpace(data))
	return dto
}

func TestSessionsStreamConfinement(t *testing.T) {
	backends := []struct {
		name   string
		config func(*testing.T) store.Config
	}{{"sqlite", func(*testing.T) store.Config { return store.Config{Engine: store.EngineSQLite, DSN: ":memory:"} }}}
	if enginetest.PostgresAvailable(t) {
		backends = append(backends, struct {
			name   string
			config func(*testing.T) store.Config
		}{
			"postgres", func(t *testing.T) store.Config {
				pg := enginetest.IsolatedPostgres(t)
				return store.Config{Engine: store.EnginePostgres, DSN: pg.App, AdminDSN: pg.Admin}
			},
		})
	} else {
		t.Log("Postgres NOT exercised: no configured disposable server")
	}
	for _, be := range backends {
		t.Run(be.name, func(t *testing.T) {
			f := newStreamConfinementFixture(t, be.config(t))
			admin := f.adminLogin()
			tenant := f.createOrg(admin, "stream-s0")
			var workspace model.ID
			ctx := context.Background()
			if err := f.st.Mutate(ctx, tenant, func(sc store.Scope) error {
				ws, err := sc.Workspaces().Create(ctx, model.Workspace{Name: "Confined", Slug: "confined", Status: model.StatusActive})
				workspace = ws.ID
				return err
			}); err != nil {
				t.Fatal(err)
			}
			token := f.member(t, admin, tenant, "confined@s0.test", workspace)
			principal, err := f.authr.Authenticate(ctx, token)
			if err != nil {
				t.Fatal(err)
			}
			ws, confined := principal.ConfinedWorkspaceIn(tenant)
			if !confined || ws != workspace || principal.Superadmin {
				t.Fatalf("real membership unresolved: workspace=%s confined=%t superadmin=%t", ws, confined, principal.Superadmin)
			}
			decision := f.authz.Authorize(ctx, auth.Request{Principal: principal, Tenant: tenant, Permission: permLiveRead, Resource: auth.ResourceFor(permLiveRead)})
			if !decision.Allow {
				t.Fatalf("collection authorization must reach handler: %+v", decision)
			}
			t.Logf("authenticated user=%s tenant=%s confined=%s collection decision=%+v", principal.Actor(), tenant, ws, decision)
			live := f.get(t, "live", token, tenant)
			body, err := io.ReadAll(live.Body)
			if err != nil || live.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "workspace confined") {
				t.Fatalf("same principal /live = %d %s err=%v", live.StatusCode, body, err)
			}
			t.Logf("same principal /live status=%d headers=%v body=%s", live.StatusCode, live.Header, body)
			f.publish(t, tenant, "s0-public-session", "s0-before-subscription")
			row, ok := getLive(t, f.m, f.st, tenant, "s0-public-session")
			if !ok {
				t.Fatal("seed fold did not create live row")
			}
			liveID := row.String("id")
			selectors := []struct{ name, query string }{
				{"global", ""},
				{"legacy", "?ref=s0-public-session"},
				{"legacy-empty", "?ref="},
				{"exact", "?live_ref=" + liveID},
				{"exact-absent", "?live_ref=" + model.NewID().String()},
				{"exact-malformed", "?live_ref=malformed"},
				{"exact-empty", "?live_ref="},
				{"intersection", "?ref=s0-public-session&live_ref=" + liveID},
				{"intersection-empty", "?ref=another&live_ref=" + liveID},
				{"exact-empty-first", "?live_ref=&live_ref=" + liveID},
				{"exact-valid-first", "?live_ref=" + liveID + "&live_ref="},
				{"exact-malformed-first", "?live_ref=bad&live_ref=" + liveID},
				{"legacy-empty-first", "?ref=&ref=s0-public-session"},
				{"legacy-repeated", "?ref=s0-public-session&ref=another"},
				{"workspace-selectors", "?workspace_id=" + workspace.String() + "&core_workspace_id=" + workspace.String()},
				{"workspace-empty-repeated", "?workspace_id=&workspace_id=anywhere&core_workspace_id="},
			}
			for _, tc := range selectors {
				t.Run("confined/"+tc.name, func(t *testing.T) {
					next, subs := streamBrokerCounts(f.m)
					res := f.get(t, "stream"+tc.query, token, tenant)
					if res.StatusCode == http.StatusOK {
						// Preserve the actual cause when run against the vulnerable
						// source: connected, THEN committed fold, THEN captured DTO.
						r := streamConnected(t, res)
						f.publish(t, tenant, "s0-public-session", "s0-public-sentinel")
						dto := streamSession(t, r)
						if dto.SessionRef != "s0-public-session" || dto.CurrentResource != "s0-public-sentinel" {
							t.Fatalf("unexpected causal DTO: %+v", dto)
						}
						_ = res.Body.Close()
						waitFor(t, "causal subscriber cancellation", func() bool { _, n := streamBrokerCounts(f.m); return n == subs })
						t.Fatal("confined /stream opened and delivered the published DTO despite /live 403")
					}
					assertStreamDenied(t, res, http.StatusForbidden, "workspace confined")
					if after, active := streamBrokerCounts(f.m); after != next || active != subs {
						t.Fatalf("rejection registered a subscriber: before=%d/%d after=%d/%d", next, subs, after, active)
					}
					// A successful committed publication cannot create a subscriber
					// for the completed denial or append any SSE to its EOF body.
					f.publish(t, tenant, "s0-public-session", "s0-after-rejection")
					t.Logf("no subscription: next=%d active=%d; denied response reached EOF before publication", next, subs)
				})
			}

			t.Run("admission-before-data", func(t *testing.T) {
				// A real resolved principal, with no usable descriptor/store at all:
				// the guard must decide before asking whether live has lineage.
				data := &streamFailingData{err: errors.New("fixture store unavailable")}
				rec := httptest.NewRecorder()
				f.m.handleStream(rec, httptest.NewRequest("GET", "/stream?live_ref="+liveID, nil), api.ModuleContext{
					Principal: principal, Tenant: tenant, Data: data,
				})
				assertStreamDenied(t, rec.Result(), http.StatusForbidden, "workspace confined")
				if data.views != 0 {
					t.Fatal("admission depended on the selector's store/descriptor")
				}
			})

			wide := f.member(t, admin, tenant, "wide@s0.test", "")
			other := f.createOrg(admin, "other-s0")
			otherReader := f.member(t, admin, other, "other@s0.test", "")
			// A user confined in T but tenant-wide in U must retain authority in U.
			root, err := f.authr.Authenticate(ctx, admin)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.authr.GrantMembership(ctx, root, principal.UserID, other, auth.RoleEditor, ""); err != nil {
				t.Fatal(err)
			}
			// The exception is tested with a superadmin who really DOES carry a
			// confined membership, so omitting !Superadmin cannot pass unnoticed.
			if _, err := f.authr.GrantMembership(ctx, root, root.UserID, tenant, auth.RoleEditor, workspace); err != nil {
				t.Fatal(err)
			}
			root, err = f.authr.Authenticate(ctx, admin)
			if err != nil {
				t.Fatal(err)
			}
			if w, c := root.ConfinedWorkspaceIn(tenant); !root.Superadmin || !c || w != workspace {
				t.Fatal("superadmin confinement precondition missing")
			}

			t.Run("tenant-wide-selectors-and-close", func(t *testing.T) {
				testStreamAuthorizedSelectors(t, f, tenant, other, liveID, wide, otherReader)
			})
			// The preceding close exercise closes the broker permanently. Replacing
			// it is test-only fixture reset, performed after every handler reached EOF.
			f.m.broker = newBroker()
			for _, tc := range []struct {
				name, token, query string
				tenant             model.TenantID
			}{
				{"superadmin", admin, "", tenant},
				{"superadmin-exact", admin, "?live_ref=" + liveID, tenant},
				{"confinement-in-different-tenant", token, "", other},
			} {
				t.Run(tc.name, func(t *testing.T) {
					before, active := streamBrokerCounts(f.m)
					res := f.get(t, "stream"+tc.query, tc.token, tc.tenant)
					r := streamConnected(t, res)
					f.publish(t, tc.tenant, "s0-public-session", "s0-authorized")
					if dto := streamSession(t, r); dto.CurrentResource != "s0-authorized" {
						t.Fatalf("authorized DTO=%+v", dto)
					}
					_ = res.Body.Close()
					waitFor(t, "HTTP cancellation releases subscriber", func() bool { _, n := streamBrokerCounts(f.m); return n == active })
					if next, _ := streamBrokerCounts(f.m); next != before+1 {
						t.Fatal("missing real subscription")
					}
				})
			}
			for _, tc := range []struct {
				name, token, query string
				tenant             model.TenantID
				status             int
			}{
				{"anonymous", "", "", tenant, http.StatusUnauthorized},
				{"foreign-tenant", wide, "", other, http.StatusForbidden},
				{"exact-foreign-row", otherReader, "?live_ref=" + liveID, other, http.StatusNotFound},
				{"exact-missing", wide, "?live_ref=" + model.NewID().String(), tenant, http.StatusNotFound},
				{"exact-malformed", wide, "?live_ref=bad", tenant, http.StatusNotFound},
			} {
				t.Run("existing-denials/"+tc.name, func(t *testing.T) {
					before, active := streamBrokerCounts(f.m)
					assertStreamDenied(t, f.get(t, "stream"+tc.query, tc.token, tc.tenant), tc.status, "")
					if next, n := streamBrokerCounts(f.m); next != before || n != active {
						t.Fatal("denial subscribed")
					}
				})
			}
			t.Run("backpressure", func(t *testing.T) {
				slow, cancelSlow := f.m.broker.subscribe(tenant, "")
				defer cancelSlow()
				fast, cancelFast := f.m.broker.subscribe(tenant, "")
				defer cancelFast()
				for i := 0; i < cap(slow)+2; i++ {
					f.publish(t, tenant, "s0-backpressure", "s0-progress")
					select {
					case dto := <-fast:
						if dto.EventCount != int64(i+1) {
							t.Fatalf("fast reader lost progress: %+v", dto)
						}
					default:
						t.Fatal("committed fold was not delivered to fast subscriber")
					}
				}
				if len(slow) != cap(slow) {
					t.Fatalf("slow buffer=%d want=%d", len(slow), cap(slow))
				}
				t.Logf("%d committed folds; slow buffer bounded at %d; fast reader received all", cap(slow)+2, cap(slow))
			})

			// An authored deny removes the reader's effective Sessions permission.
			// This is the real policy path, not an Allow=true/false authorization stub.
			policy := f.doJSON("POST", "/v1/m/governance/policies", admin, map[string]any{
				"name": "deny-live-read", "kind": "abac", "enabled": true,
				"spec": map[string]any{"rules": []any{map[string]any{"deny": true, "resource": "live", "verb": "read"}}},
			}, tenantHdr(tenant))
			if policy.code != http.StatusCreated {
				t.Fatalf("deny policy = %d %s", policy.code, policy.raw)
			}
			t.Run("effective-permission-denied", func(t *testing.T) {
				before, active := streamBrokerCounts(f.m)
				assertStreamDenied(t, f.get(t, "stream", wide, tenant), http.StatusForbidden, "")
				if next, n := streamBrokerCounts(f.m); next != before || n != active {
					t.Fatal("policy denial subscribed")
				}
			})
		})
	}
}

func assertStreamDenied(t *testing.T, res *http.Response, status int, message string) {
	t.Helper()
	body, err := io.ReadAll(res.Body)
	if err != nil || res.StatusCode != status {
		t.Fatalf("denial = %d %s err=%v, want %d", res.StatusCode, body, err, status)
	}
	if message != "" && strings.TrimSpace(string(body)) != `{"error":{"message":"`+message+`"}}` {
		t.Fatalf("denial body=%s", body)
	}
	if strings.Contains(res.Header.Get("Content-Type"), "event-stream") || res.Header.Get("X-Accel-Buffering") != "" || res.Header.Get("Cache-Control") == "no-cache" || res.Header.Get("Connection") == "keep-alive" {
		t.Fatalf("denial committed SSE headers: %v", res.Header)
	}
	for _, s := range []string{": connected", ": ping", "event: session", "s0-public-sentinel"} {
		if strings.Contains(string(body), s) {
			t.Fatalf("SSE bytes in denial: %s", body)
		}
	}
	t.Logf("denial completed: status=%d headers=%v body=%s", res.StatusCode, res.Header, body)
}

func testStreamAuthorizedSelectors(t *testing.T, f *streamConfinementFixture, tenant, other model.TenantID, liveID, wide, otherReader string) {
	t.Helper()
	// Sequential publication + stream EOF are the negative-delivery barriers.
	// Each subscribed stream must drain exactly its admitted frames before EOF;
	// a missing publish, missing subscription or delayed reader cannot prove absence.
	type subscription struct {
		name, query, token string
		tenant             model.TenantID
		want               []string
		reader             *bufio.Reader
	}
	subscriptions := []subscription{
		{name: "global", token: wide, tenant: tenant, want: []string{"s0-unrelated", "s0-selected"}},
		{name: "legacy", query: "?ref=s0-public-session", token: wide, tenant: tenant, want: []string{"s0-selected"}},
		{name: "exact", query: "?live_ref=" + liveID, token: wide, tenant: tenant, want: []string{"s0-selected"}},
		{name: "intersection", query: "?ref=s0-public-session&live_ref=" + liveID, token: wide, tenant: tenant, want: []string{"s0-selected"}},
		{name: "empty-intersection", query: "?ref=unrelated&live_ref=" + liveID, token: wide, tenant: tenant},
		{name: "other-tenant", token: otherReader, tenant: other, want: []string{"s0-other-progress"}},
		{name: "ignored-workspace", query: "?workspace_id=ignored&core_workspace_id=ignored", token: wide, tenant: tenant, want: []string{"s0-unrelated", "s0-selected"}},
		{name: "empty-first", query: "?live_ref=&live_ref=" + liveID, token: wide, tenant: tenant, want: []string{"s0-unrelated", "s0-selected"}},
	}
	for i := range subscriptions {
		s := &subscriptions[i]
		s.reader = streamConnected(t, f.get(t, "stream"+s.query, s.token, s.tenant))
	}
	_, n := streamBrokerCounts(f.m)
	if n != len(subscriptions) {
		t.Fatalf("subscribers=%d want %d", n, len(subscriptions))
	}
	f.publish(t, tenant, "unrelated", "s0-unrelated")
	f.publish(t, tenant, "s0-public-session", "s0-selected")
	f.publish(t, other, "s0-public-session", "s0-other-progress")
	// All folds and broker sends returned. Closing drains queued frames and
	// terminates HTTP: absence is checked against EOF, never an idle sleep.
	f.m.broker.close()
	for _, s := range subscriptions {
		t.Run(s.name, func(t *testing.T) {
			for _, sentinel := range s.want {
				if dto := streamSession(t, s.reader); dto.CurrentResource != sentinel {
					t.Fatalf("resource=%s want=%s", dto.CurrentResource, sentinel)
				}
			}
			extra, err := io.ReadAll(s.reader)
			if err != nil || len(extra) != 0 {
				t.Fatalf("unexpected frames before EOF: %s err=%v", extra, err)
			}
			t.Log("publication barrier completed; admitted frames drained; clean HTTP EOF")
		})
	}
	if _, n := streamBrokerCounts(f.m); n != 0 {
		t.Fatalf("broker close left %d subscribers", n)
	}
}

// Supplemental handler checks isolate admission from the descriptor and keep
// unrelated selector-store failures distinguishable. The causal proof above is
// mounted HTTP with real authority; these are not substitutes for that chain.
type streamFailingData struct {
	api.ScopedData
	err   error
	views int
}

func (d *streamFailingData) View(context.Context, func(store.Scope) error) error {
	d.views++
	return d.err
}

func TestSessionsStreamSelectorStoreFailure(t *testing.T) {
	m := New()
	data := &streamFailingData{err: errors.New("fixture store unavailable")}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/stream?live_ref="+model.NewID().String(), nil)
	m.handleStream(rec, req, api.ModuleContext{Data: data})
	assertStreamDenied(t, rec.Result(), http.StatusInternalServerError, "")
	if data.views != 1 {
		t.Fatal("exact selector did not reach store")
	}
	if next, n := streamBrokerCounts(m); next != 0 || n != 0 {
		t.Fatal("store error opened subscription")
	}
}

// Supplemental transport-unit coverage. Fake time exercises the unchanged 25s
// heartbeat and 30s per-write deadline without enlarging any test timeout. Only
// the audit store is unavailable; frame bytes come from handleStream/writeFrame.
type streamAuditUnavailable struct{ api.ScopedData }

func (streamAuditUnavailable) Mutate(context.Context, func(store.Scope) error) error {
	return errors.New("fixture audit unavailable")
}

type streamFrameWriter struct {
	mu                   sync.Mutex
	recorder             *httptest.ResponseRecorder
	deadlines            []time.Time
	failWrite, failFlush bool
}

func (w *streamFrameWriter) Header() http.Header { return w.recorder.Header() }
func (w *streamFrameWriter) WriteHeader(status int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.recorder.WriteHeader(status)
}
func (w *streamFrameWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.failWrite {
		return 0, io.ErrClosedPipe
	}
	return w.recorder.Write(p)
}
func (w *streamFrameWriter) SetWriteDeadline(deadline time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.deadlines = append(w.deadlines, deadline)
	return nil
}
func (w *streamFrameWriter) FlushError() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.failFlush {
		return io.ErrClosedPipe
	}
	w.recorder.Flush()
	return nil
}

func (w *streamFrameWriter) state() (string, []time.Time, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.recorder.Body.String(), append([]time.Time(nil), w.deadlines...), w.recorder.Flushed
}

func (w *streamFrameWriter) fail(write, flush bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.failWrite, w.failFlush = write, flush
}

func TestSessionsStreamHeartbeatAndWriteFailure(t *testing.T) {
	for _, mode := range []string{"heartbeat", "write-failure", "flush-failure"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				m := New()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				req := httptest.NewRequest("GET", "/stream", nil).WithContext(ctx)
				w := &streamFrameWriter{recorder: httptest.NewRecorder()}
				go m.handleStream(w, req, api.ModuleContext{Data: streamAuditUnavailable{}})
				synctest.Wait()
				body, deadlines, _ := w.state()
				if body != ": connected\n\n" || len(deadlines) != 1 {
					t.Fatal("connected frame/deadline missing")
				}
				if _, n := streamBrokerCounts(m); n != 1 {
					t.Fatal("connected stream has no subscriber")
				}
				w.fail(mode == "write-failure", mode == "flush-failure")
				time.Sleep(heartbeatInterval)
				synctest.Wait()
				body, deadlines, flushed := w.state()
				if len(deadlines) != 2 || deadlines[1].Sub(deadlines[0]) != heartbeatInterval || deadlines[1].Sub(time.Now()) != streamWriteTimeout {
					t.Fatalf("per-frame deadlines=%v", deadlines)
				}
				if mode == "heartbeat" {
					if body != ": connected\n\n: ping\n\n" || !flushed {
						t.Fatalf("heartbeat bytes=%q", body)
					}
					cancel()
					synctest.Wait()
				}
				if _, n := streamBrokerCounts(m); n != 0 {
					t.Fatalf("stream left %d subscribers after %s", n, mode)
				}
			})
		})
	}
}
