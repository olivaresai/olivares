// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/store"
)

// QA05 — first-boot readiness. The subject of this file is ONE question: does
// GET /readyz report a state the engine actually observed?
//
// The defect it closes was measured on the source: a store Ping and the leader
// predicate both succeed on a Postgres deployment with no --admin-dsn, while
// POST /v1/setup — the only thing such an install can usefully do — refuses with
// 501 cross_tenant_admin_pool_not_configured, because firstOrg resolves the first
// organization through Store.System + SystemScope.ListOrgs and that read is not
// authoritative without a BYPASSRLS pool. Readiness answered
// 200 {"setup_required":true}: "ready to be set up", about an install that could
// not be set up.
//
// These cases go through the REAL router over REAL sqlstore fixtures, because
// that is the only place the answer an operator (or a kubelet) receives exists.
// The Postgres halves of the same journeys are qualified against a real server in
// readiness_firstboot_pg_test.go; a decorator is NOT equated to that.

// readyz is one probe of the readiness surface, decoded.
type readyzResult struct {
	code int
	body map[string]any
	raw  string
}

func probeReadyz(t *testing.T, h *harness) readyzResult {
	t.Helper()
	r := h.do(http.MethodGet, "/readyz", "", nil, nil)
	return readyzResult{code: r.code, body: r.body, raw: r.raw}
}

// requireNoRawStoreText is the disclosure check every 5xx branch owes. The store
// wraps its own sentence into the enumeration error (sqlstore/system.go) and a DSN
// can carry a password, so neither may ride out on a probe that any scraper,
// kubelet or load balancer can call unauthenticated.
func requireNoRawStoreText(t *testing.T, raw string, leaked ...string) {
	t.Helper()
	for _, s := range append([]string{"postgres://", "RLS-limited", "GUC"}, leaked...) {
		if strings.Contains(raw, s) {
			t.Errorf("readiness body leaked internal text %q: %s", s, raw)
		}
	}
}

// TestReadyzFreshSQLiteInstallIsReadyToReceiveSetup is the positive control for
// the whole file: on SQLite the System transaction IS the whole estate, so the
// capability probe succeeds and a fresh install keeps its 200. If this reddened,
// every refusal below would be indistinguishable from "the probe refuses
// everything".
func TestReadyzFreshSQLiteInstallIsReadyToReceiveSetup(t *testing.T) {
	h := newHarness(t)

	r := probeReadyz(t, h)
	if r.code != http.StatusOK {
		t.Fatalf("/readyz on a fresh SQLite install = %d %s, want 200", r.code, r.raw)
	}
	if r.body["status"] != "ok" || r.body["store"] != "up" || r.body["leader"] != true {
		t.Errorf("/readyz body = %s, want status=ok store=up leader=true", r.raw)
	}
	if r.body["setup_required"] != true {
		t.Errorf("/readyz setup_required = %v, want true (no user exists yet)", r.body["setup_required"])
	}
	if _, ok := r.body["code"]; ok {
		t.Errorf("a READY answer carries an error code: %s", r.raw)
	}

	h.adminLogin() // runs POST /v1/setup for real

	after := probeReadyz(t, h)
	if after.code != http.StatusOK || after.body["setup_required"] != false {
		t.Fatalf("/readyz after setup = %d %s, want 200 setup_required=false", after.code, after.raw)
	}
}

// TestReadyzRefusesFirstBootWithoutAdministrativeEnumeration is the defect itself.
// The deployment is the one an operator actually has on a first boot: Postgres on
// the application pool alone, whose ListOrgs answers the WRAPPED sentinel
// (enumerationBlindStore, handlers_setup_adminpool_test.go — a decorator, so every
// other path keeps real sqlstore behavior).
//
// Both halves are asserted in one test on purpose: readiness must now refuse, AND
// POST /v1/setup must keep the exact 501 contract it already had. A readiness
// change that quietly moved the setup refusal would be a different product.
func TestReadyzRefusesFirstBootWithoutAdministrativeEnumeration(t *testing.T) {
	h := newHarnessOpts(t, func(o *api.Options) {
		o.Store = enumerationBlindStore{Store: o.Store, blind: blindFromTheStart()}
	})

	r := probeReadyz(t, h)
	if r.code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz on a first boot that cannot enumerate = %d %s, want 503", r.code, r.raw)
	}
	if r.body["status"] != "setup_blocked" {
		t.Errorf("/readyz status = %v, want setup_blocked", r.body["status"])
	}
	if r.body["store"] != "up" || r.body["leader"] != true {
		t.Errorf("/readyz body = %s, want store=up leader=true (neither is what failed)", r.raw)
	}
	if r.body["setup_required"] != true {
		t.Errorf("/readyz setup_required = %v, want true", r.body["setup_required"])
	}
	if r.body["code"] != "cross_tenant_admin_pool_not_configured" {
		t.Errorf("/readyz code = %v, want cross_tenant_admin_pool_not_configured", r.body["code"])
	}
	remedy, _ := r.body["remedy"].(string)
	for _, want := range []string{"BYPASSRLS", "olivares db init", "--admin-role", "--admin-dsn"} {
		if !strings.Contains(remedy, want) {
			t.Errorf("the readiness remedy does not name %q: %q", want, remedy)
		}
	}
	requireNoRawStoreText(t, r.raw)

	// The setup ceremony is UNCHANGED: readiness observes, it does not authorize.
	sr := h.do(http.MethodPost, "/v1/setup", "", map[string]any{
		"token": h.setupTok, "email": "root@x.io", "password": "supersecret1",
	}, nil)
	if sr.code != http.StatusNotImplemented {
		t.Fatalf("POST /v1/setup = %d %s, want 501 (its contract is untouched)", sr.code, sr.raw)
	}
	errObj, _ := sr.body["error"].(map[string]any)
	if errObj == nil || errObj["code"] != "cross_tenant_admin_pool_not_configured" {
		t.Fatalf("POST /v1/setup no longer answers the admin-pool code: %s", sr.raw)
	}

	// And nothing was cached by either call: the condition is a deployment
	// configuration, so the next probe must observe it again and answer the same.
	again := probeReadyz(t, h)
	if again.code != http.StatusServiceUnavailable || again.body["status"] != "setup_blocked" {
		t.Fatalf("second /readyz = %d %s, want the same 503 setup_blocked", again.code, again.raw)
	}
}

// TestReadyzWithWorkingEnumerationCompletesTheFirstBootJourney walks the path the
// operator is supposed to have: ready → setup → ready. The middle step is the real
// POST /v1/setup, so the 200 before it is a claim the very next request redeems.
func TestReadyzWithWorkingEnumerationCompletesTheFirstBootJourney(t *testing.T) {
	blind := &atomic.Bool{} // working admin enumeration
	h := newHarnessOpts(t, func(o *api.Options) {
		o.Store = enumerationBlindStore{Store: o.Store, blind: blind}
	})

	before := probeReadyz(t, h)
	if before.code != http.StatusOK || before.body["setup_required"] != true {
		t.Fatalf("/readyz with a working admin pool = %d %s, want 200 setup_required=true", before.code, before.raw)
	}

	// NO POSITIVE CACHE EITHER. The admin pool is deployment configuration: a
	// restart can remove it between two probes, and a readiness endpoint that
	// remembered the good answer would keep publishing a capability this node no
	// longer has. The same server, the next probe, must observe the loss.
	blind.Store(true)
	lost := probeReadyz(t, h)
	if lost.code != http.StatusServiceUnavailable || lost.body["status"] != "setup_blocked" {
		t.Fatalf("/readyz after the admin pool went away = %d %s, want 503 setup_blocked", lost.code, lost.raw)
	}
	blind.Store(false)

	sr := h.do(http.MethodPost, "/v1/setup", "", map[string]any{
		"token": h.setupTok, "email": "root@x.io", "password": "supersecret1",
	}, nil)
	if sr.code != http.StatusCreated {
		t.Fatalf("POST /v1/setup = %d %s, want 201 — readiness promised this would work", sr.code, sr.raw)
	}

	after := probeReadyz(t, h)
	if after.code != http.StatusOK || after.body["setup_required"] != false {
		t.Fatalf("/readyz after setup = %d %s, want 200 setup_required=false", after.code, after.raw)
	}
}

// TestReadyzConfiguredInstallStaysReadyWithoutAdministrativeEnumeration is the
// limit on the whole change: an administrative pool is OPTIONAL for a deployment
// that never runs a cross-tenant ceremony, so a configured install must not be
// drained out of its Service because that optional capability is absent. The
// cross-tenant APIs keep refusing explicitly on their own (errors.go).
//
// The server is built FRESH over a store that already holds the superadmin, so the
// 200 comes from an actual transactional observation and not from the in-process
// "first true" cache a previous setup would have left behind.
func TestReadyzConfiguredInstallStaysReadyWithoutAdministrativeEnumeration(t *testing.T) {
	first := newHarness(t)
	first.adminLogin()

	restarted := newHarnessOptsFromStoreSource(t, harnessStoreSource{borrowed: first.st},
		func(o *api.Options) {
			o.Store = enumerationBlindStore{Store: o.Store, blind: blindFromTheStart()}
		})

	r := probeReadyz(t, restarted)
	if r.code != http.StatusOK {
		t.Fatalf("/readyz on a configured install with no admin pool = %d %s, want 200", r.code, r.raw)
	}
	if r.body["setup_required"] != false || r.body["status"] != "ok" {
		t.Errorf("/readyz body = %s, want status=ok setup_required=false", r.raw)
	}
	if _, ok := r.body["code"]; ok {
		t.Errorf("a configured install was given a first-boot error code: %s", r.raw)
	}
}

// authBlindStore breaks the GLOBAL authentication read behind HasAnyUser, which is
// how "this node cannot tell whether the install is set up" actually happens: the
// auth partition is a different transaction from the store Ping, so Ping can answer
// while this read does not.
type authBlindStore struct {
	store.Store
	err error
}

func (s authBlindStore) AuthView(context.Context, func(store.AuthScope) error) error { return s.err }

// TestReadyzOmitsSetupRequiredWhenSetupStateIsUnknown pins the honest third state.
// The old handler had no way to express it: setupCompleteNow collapses "no user"
// and "could not look" into the same false, so an unreadable auth partition was
// published as setup_required=true — a fact nobody observed, on the endpoint an
// operator trusts most.
func TestReadyzOmitsSetupRequiredWhenSetupStateIsUnknown(t *testing.T) {
	h := newHarnessOpts(t, func(o *api.Options) {
		blind := authBlindStore{Store: o.Store, err: errors.New("auth partition unreadable: dial tcp 10.0.0.9:5432: connect: connection refused")}
		o.Store = blind
		o.Authenticator = auth.NewAuthenticator(blind, nil)
	})

	r := probeReadyz(t, h)
	if r.code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz with an unreadable setup state = %d %s, want 503", r.code, r.raw)
	}
	if r.body["status"] != "setup_unavailable" || r.body["code"] != "setup_state_unavailable" {
		t.Errorf("/readyz body = %s, want status=setup_unavailable code=setup_state_unavailable", r.raw)
	}
	if _, present := r.body["setup_required"]; present {
		t.Errorf("/readyz published setup_required for a state it did not observe: %s", r.raw)
	}
	requireNoRawStoreText(t, r.raw, "connection refused", "10.0.0.9")
}

// systemFailingStore makes the capability probe fail for a reason that is NOT the
// configured-enumeration refusal — a dead backend, a canceled transaction, a
// timeout. Its error deliberately does not wrap store.ErrEnumerationNotAuthoritative.
type systemFailingStore struct {
	store.Store
	err error
}

func (s systemFailingStore) System(context.Context, func(store.SystemScope) error) error {
	return s.err
}

// TestReadyzProbeFailureIsNotReportedAsTheAdminPoolCondition is the negative
// control for the sentinel branch. "The read is unauthoritative by configuration"
// never self-heals and names a provisioning remedy; "the read did not complete"
// may heal on the next probe and names nothing. Answering the first for the second
// would send an operator to provision a role they already have.
func TestReadyzProbeFailureIsNotReportedAsTheAdminPoolCondition(t *testing.T) {
	h := newHarnessOpts(t, func(o *api.Options) {
		o.Store = systemFailingStore{Store: o.Store, err: errors.New("begin system transaction: dial tcp 10.0.0.9:5432: connect: connection refused")}
	})

	r := probeReadyz(t, h)
	if r.code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz with a failing capability probe = %d %s, want 503", r.code, r.raw)
	}
	if r.body["status"] != "setup_unavailable" || r.body["code"] != "setup_probe_unavailable" {
		t.Errorf("/readyz body = %s, want status=setup_unavailable code=setup_probe_unavailable", r.raw)
	}
	if r.body["setup_required"] != true {
		t.Errorf("/readyz setup_required = %v, want true (no user exists; only the probe failed)", r.body["setup_required"])
	}
	if _, ok := r.body["remedy"]; ok {
		t.Errorf("a probe failure was given the provisioning remedy: %s", r.raw)
	}
	requireNoRawStoreText(t, r.raw, "connection refused", "10.0.0.9")
}

// TestReadyzKeepsPingThenLeaderBeforeAnySetupRead pins the ORDER. Both new reads
// are strictly after the two that already existed, so a store outage still reads as
// a store outage and a standby still drains as a standby — even on a deployment
// whose enumeration is also broken, which is exactly when a misordered probe would
// report the wrong cause.
func TestReadyzKeepsPingThenLeaderBeforeAnySetupRead(t *testing.T) {
	t.Run("store down outranks the setup read", func(t *testing.T) {
		h := newHarnessOpts(t, func(o *api.Options) {
			o.Store = downStore{Store: enumerationBlindStore{Store: o.Store, blind: blindFromTheStart()}}
		})
		r := probeReadyz(t, h)
		if r.code != http.StatusServiceUnavailable || r.body["status"] != "unavailable" || r.body["store"] != "down" {
			t.Fatalf("/readyz with the store down = %d %s, want 503 status=unavailable store=down", r.code, r.raw)
		}
		if _, ok := r.body["setup_required"]; ok {
			t.Errorf("a store outage answered with a setup verdict: %s", r.raw)
		}
	})

	t.Run("standby outranks the setup read", func(t *testing.T) {
		h := newHarnessOpts(t, func(o *api.Options) {
			o.Store = standbyStore{Store: enumerationBlindStore{Store: o.Store, blind: blindFromTheStart()}}
		})
		r := probeReadyz(t, h)
		if r.code != http.StatusServiceUnavailable || r.body["status"] != "standby" || r.body["leader"] != false {
			t.Fatalf("/readyz on a standby = %d %s, want 503 status=standby leader=false", r.code, r.raw)
		}
		if _, ok := r.body["code"]; ok {
			t.Errorf("a standby was given a first-boot error code: %s", r.raw)
		}
	})
}

// deadlineSpyStore records the deadline each readiness read is given, so a test can
// prove they SHARE one budget instead of each starting its own.
type deadlineSpyStore struct {
	store.Store
	mu                sync.Mutex
	authView, system  time.Time
	authOK, systemOK  bool
	authSeen, sysSeen int
}

func (s *deadlineSpyStore) AuthView(ctx context.Context, fn func(store.AuthScope) error) error {
	s.record(ctx, true)
	return s.Store.AuthView(ctx, fn)
}

func (s *deadlineSpyStore) System(ctx context.Context, fn func(store.SystemScope) error) error {
	s.record(ctx, false)
	return s.Store.System(ctx, fn)
}

func (s *deadlineSpyStore) record(ctx context.Context, isAuth bool) {
	dl, ok := ctx.Deadline()
	s.mu.Lock()
	defer s.mu.Unlock()
	if isAuth {
		s.authView, s.authOK, s.authSeen = dl, ok, s.authSeen+1
		return
	}
	s.system, s.systemOK, s.sysSeen = dl, ok, s.sysSeen+1
}

// TestReadyzReadsShareOneBoundedBudget proves the budget contract. A kubelet probe
// carries a timeout of its own (3s in the shipped chart), so two sequential
// two-second budgets would let this handler spend four seconds inside a probe that
// was abandoned after three — and an abandoned probe is a failed probe.
//
// Identical deadlines are the observable form of "the same child context": a second
// context.WithTimeout call cannot produce the same instant.
func TestReadyzReadsShareOneBoundedBudget(t *testing.T) {
	var spy *deadlineSpyStore
	h := newHarnessOpts(t, func(o *api.Options) {
		spy = &deadlineSpyStore{Store: o.Store}
		o.Store = spy
		o.Authenticator = auth.NewAuthenticator(spy, nil)
	})

	start := time.Now()
	if r := probeReadyz(t, h); r.code != http.StatusOK {
		t.Fatalf("/readyz = %d %s, want 200", r.code, r.raw)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()
	if spy.authSeen == 0 || spy.sysSeen == 0 {
		t.Fatalf("readiness did not run both reads (auth=%d system=%d)", spy.authSeen, spy.sysSeen)
	}
	if !spy.authOK || !spy.systemOK {
		t.Fatalf("a readiness read ran with NO deadline (auth ok=%t system ok=%t)", spy.authOK, spy.systemOK)
	}
	if !spy.authView.Equal(spy.system) {
		t.Errorf("the setup read and the capability probe ran on different budgets: auth=%s system=%s",
			spy.authView, spy.system)
	}
	if budget := spy.system.Sub(start); budget > 2*time.Second+250*time.Millisecond {
		t.Errorf("readiness budget = %s, want the single 2s bound", budget)
	}
}

// blockingSystemStore holds the capability probe open until its context ends, which
// is what a wedged backend looks like from inside the handler.
type blockingSystemStore struct {
	store.Store
	entered chan struct{}
	once    sync.Once
}

func (s *blockingSystemStore) System(ctx context.Context, _ func(store.SystemScope) error) error {
	s.once.Do(func() { close(s.entered) })
	<-ctx.Done()
	return ctx.Err()
}

// TestReadyzCanceledProbeIsNotReportedAsAnEmptyEstate is the "do not infer absent
// data from a timeout" rule, at the one place it could be violated: a probe that
// never answered must not be reported as a capability that works, nor as the
// configured-enumeration refusal. It answers the retryable third state instead.
func TestReadyzCanceledProbeIsNotReportedAsAnEmptyEstate(t *testing.T) {
	blocking := &blockingSystemStore{entered: make(chan struct{})}
	h := newHarnessOpts(t, func(o *api.Options) {
		blocking.Store = o.Store
		o.Store = blocking
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil).WithContext(ctx)
	req.RemoteAddr = "10.0.0.1:1234"
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.srv.Handler().ServeHTTP(rec, req)
	}()

	select {
	case <-blocking.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the capability probe was never reached")
	}
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("readiness did not answer after its context was canceled")
	}

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz with a canceled probe = %d %s, want 503", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"code":"setup_probe_unavailable"`) {
		t.Errorf("a canceled probe was not reported as unavailable: %s", body)
	}
	if strings.Contains(body, "cross_tenant_admin_pool_not_configured") {
		t.Errorf("a canceled probe was reported as the configured-enumeration refusal: %s", body)
	}
}

// TestReadinessProbesDoNotMutateFirstBootState is the read-only contract. The probe
// runs the SAME door as POST /v1/setup, and that door provisions; a kubelet calls it
// every ten seconds forever, so "it only reads" has to be measured rather than
// intended. Two independent witnesses: the estate is unchanged, and the one-time
// setup token still works afterwards.
func TestReadinessProbesDoNotMutateFirstBootState(t *testing.T) {
	h := newHarness(t)

	countOrgs := func() int {
		t.Helper()
		var n int
		if err := h.st.System(context.Background(), func(sys store.SystemScope) error {
			orgs, err := sys.ListOrgs(context.Background())
			n = len(orgs)
			return err
		}); err != nil {
			t.Fatalf("count orgs: %v", err)
		}
		return n
	}

	before := countOrgs()
	for i := 0; i < 5; i++ {
		if r := probeReadyz(t, h); r.code != http.StatusOK {
			t.Fatalf("/readyz probe %d = %d %s, want 200", i, r.code, r.raw)
		}
	}
	if after := countOrgs(); after != before {
		t.Errorf("readiness probes changed the estate: %d org rows before, %d after", before, after)
	}

	// The token is single-use and readiness never consumes it: setup still runs.
	sr := h.do(http.MethodPost, "/v1/setup", "", map[string]any{
		"token": h.setupTok, "email": "root@x.io", "password": "supersecret1",
	}, nil)
	if sr.code != http.StatusCreated {
		t.Fatalf("POST /v1/setup after five readiness probes = %d %s, want 201", sr.code, sr.raw)
	}
	if after := countOrgs(); after != before+1 {
		t.Errorf("setup created %d org row(s), want exactly 1", after-before)
	}
}

// TestPodHealthAndLivenessSurviveAMissingAdminPool is the Kubernetes boundary. A
// missing OPTIONAL administrative pool must not restart a pod (/livez) and must not
// make a healthy replica fail the StatefulSet rollout barrier (/pod-readyz): those
// answer pod health, while /readyz answers "route client traffic here".
func TestPodHealthAndLivenessSurviveAMissingAdminPool(t *testing.T) {
	h := newHarnessOpts(t, func(o *api.Options) {
		o.Store = enumerationBlindStore{Store: o.Store, blind: blindFromTheStart()}
	})

	pod := h.do(http.MethodGet, "/pod-readyz", "", nil, nil)
	if pod.code != http.StatusOK {
		t.Fatalf("/pod-readyz without an admin pool = %d %s, want 200 (pod health is not writer routing)", pod.code, pod.raw)
	}
	if pod.body["store"] != "up" || pod.body["setup_required"] != true {
		t.Errorf("/pod-readyz body = %s, want store=up setup_required=true", pod.raw)
	}

	live := h.do(http.MethodGet, "/livez", "", nil, nil)
	if live.code != http.StatusOK || live.body["status"] != "ok" {
		t.Fatalf("/livez without an admin pool = %d %s, want 200 status=ok (a dependency must not restart the process)", live.code, live.raw)
	}
}

// TestReadyzUnderConcurrentSetup is the race case. Readiness now reads the same
// setup state the setup ceremony writes, and a kubelet does not stop probing while
// an operator is posting the form. Every answer must be one of the two legitimate
// states, and the install must end READY exactly once.
func TestReadyzUnderConcurrentSetup(t *testing.T) {
	h := newHarness(t)

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				r := h.do(http.MethodGet, "/readyz", "", nil, nil)
				if r.code != http.StatusOK {
					t.Errorf("/readyz during setup = %d %s, want 200 on SQLite", r.code, r.raw)
					return
				}
				switch r.body["setup_required"] {
				case true, false:
				default:
					t.Errorf("/readyz reported an unobserved setup state: %s", r.raw)
					return
				}
			}
		}()
	}

	sr := h.do(http.MethodPost, "/v1/setup", "", map[string]any{
		"token": h.setupTok, "email": "root@x.io", "password": "supersecret1",
	}, nil)
	close(stop)
	wg.Wait()

	if sr.code != http.StatusCreated {
		t.Fatalf("POST /v1/setup under concurrent probes = %d %s, want 201", sr.code, sr.raw)
	}
	final := probeReadyz(t, h)
	if final.code != http.StatusOK || final.body["setup_required"] != false {
		t.Fatalf("/readyz after a concurrent setup = %d %s, want 200 setup_required=false", final.code, final.raw)
	}
}
