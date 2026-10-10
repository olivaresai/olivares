// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/finops"
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/modules/sessions"
)

// countingStops is the real governance module with the stop-history read counted and a
// hook that runs right after a successful read, where a stop can be engaged between
// the read and the use of its answer.
type countingStops struct {
	*governance.Module
	epochReads atomic.Int32
	afterEpoch func()
}

func (c *countingStops) SessionStopEpoch(ctx context.Context, tenant model.TenantID, agentRef string) (string, error) {
	epoch, err := c.Module.SessionStopEpoch(ctx, tenant, agentRef)
	c.epochReads.Add(1)
	if err == nil && c.afterEpoch != nil {
		c.afterEpoch()
	}
	return epoch, err
}

// engageEstateStop engages a real estate stop through the API and returns its id. It
// reports a failure with Errorf: it also runs inside request handlers, where Fatalf is
// not allowed.
func engageEstateStop(t *testing.T, h *harness) model.ID {
	t.Helper()
	code, raw := h.req(http.MethodPost, "/v1/m/governance/killswitch", h.adminToken, h.tenantA, map[string]any{"scope_kind": "estate", "reason": "stop engaged during the launch"})
	if code != http.StatusCreated {
		t.Errorf("engage: %d %s", code, raw)
	}
	var stop struct {
		ID model.ID `json:"id"`
	}
	_ = json.Unmarshal(raw, &stop)
	return stop.ID
}

// One mint lists the stop history once. The use of the bearer lists it again: that read
// is what makes every later hook and MCP call fail after a stop, and it stays.
func TestSessionHookMintReadsStopHistoryOnce(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	tenant := model.TenantID(h.tenantA)
	p, err := h.authr.Authenticate(ctx, h.adminToken)
	if err != nil {
		t.Fatal(err)
	}
	stops := &countingStops{Module: h.set.gov}
	c := newSessionHookCredentials(h.authr, h.st, h.set.sessions, stops)
	intent := claimHookTestSession(t, h, p, tenant, "stop-read-once")
	intent.AgentRef = "session-agent"
	intent.LauncherPrincipal = p
	token, err := c.mint(ctx, tenant, intent)
	if err != nil {
		t.Fatal(err)
	}
	if n := stops.epochReads.Load(); n != 1 {
		t.Fatalf("a mint must read the stop history once, read it %d times", n)
	}
	if _, err := c.Authenticate(ctx, token); err != nil {
		t.Fatal(err)
	}
	if n := stops.epochReads.Load(); n != 2 {
		t.Fatalf("using the bearer must read the stop history again: %d reads in all", n)
	}
}

// A stop engaged after the mint read the history but before the bearer is published must
// leave no usable bearer: the epoch it carries is stale, and the first use refuses it.
func TestSessionHookMintStopEngagedAfterTheReadLeavesNoUsableBearer(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	tenant := model.TenantID(h.tenantA)
	p, err := h.authr.Authenticate(ctx, h.adminToken)
	if err != nil {
		t.Fatal(err)
	}
	stops := &countingStops{Module: h.set.gov}
	stops.afterEpoch = func() { stops.afterEpoch = nil; engageEstateStop(t, h) }
	c := newSessionHookCredentials(h.authr, h.st, h.set.sessions, stops)
	intent := claimHookTestSession(t, h, p, tenant, "stop-after-read")
	intent.AgentRef = "session-agent"
	intent.LauncherPrincipal = p
	token, err := c.mint(ctx, tenant, intent)
	if err != nil {
		// Refused at the mint: no bearer exists. Only a refusal for the stop counts.
		if !errors.Is(err, auth.ErrUnauthenticated) {
			t.Fatalf("the mint failed for another reason: %v", err)
		}
		return
	}
	if _, err := c.Authenticate(ctx, token); err == nil {
		t.Fatal("a bearer minted while a stop engaged authenticated")
	}
}

// stopCounter wraps the composed stop gate. After its engageAt-th read says "not
// stopped" it engages a real estate stop through the API, which is a stop engaged
// DURING the launch.
type stopCounter struct {
	sessions.StopGate
	reads    atomic.Int32
	engageAt int32
	engage   func()
}

func (g *stopCounter) Check(ctx context.Context, tenant model.TenantID, dims sessions.StopDims) (sessions.StopDecision, error) {
	d, err := g.StopGate.Check(ctx, tenant, dims)
	if g.reads.Add(1) == g.engageAt && g.engage != nil {
		g.engage()
	}
	return d, err
}

type launchCounter struct {
	approvalProjectionRunner
	launches atomic.Int32
}

func (r *launchCounter) Launch(ctx context.Context, spec sessions.LaunchSpec) (sessions.Process, error) {
	r.launches.Add(1)
	return r.approvalProjectionRunner.Launch(ctx, spec)
}

// launchFixture wires the composed session governance over the harness the way boot does,
// with the module's stop gate wrapped so a test can engage a real estate stop through the
// API right after the runtime's first stop read.
type launchFixture struct {
	h       *harness
	stops   *countingStops
	creds   *sessionHookCredentials
	rec     *stopDenyRecorder
	runner  *launchCounter
	stop    *stopCounter
	profile string
	engaged atomic.Value // model.ID of the stop rearm engaged; set inside a request handler
}

func newLaunchFixture(t *testing.T) *launchFixture {
	t.Helper()
	h := newHarness(t)
	m := h.set.sessions
	runner := &launchCounter{}
	sessions.WithRunner(runner)(m)
	m.EnableProfiledLaunches()
	m.UseExecutionEnvironmentRef("stop-read-once")
	stops := &countingStops{Module: h.set.gov}
	credentials := newSessionHookCredentials(h.authr, h.st, m, stops)
	if err := credentials.bindEndpoint("127.0.0.1:8447"); err != nil {
		t.Fatal(err)
	}
	rec := newStopDenyRecorder(h.st, nil)
	wireSessionGovernance(h.set, h.st, rec, nil, func(string) string { return "" }, nil, credentials.provisioner())
	stop := &stopCounter{StopGate: sessionStopGate{guard: h.set.gov, rec: rec}}
	sessions.WithStopGate(stop)(m)

	var profile struct {
		Ref string `json:"profile_ref"`
	}
	if code := h.reqInto(http.MethodPost, "/v1/m/sessions/provider-profiles", h.adminToken, h.tenantA, map[string]any{"driver": "claude", "auth_source": "provider_account_home", "config_home": t.TempDir(), "user_home": t.TempDir()}, &profile); code != http.StatusCreated {
		t.Fatalf("profile=%d", code)
	}
	return &launchFixture{h: h, stops: stops, creds: credentials, rec: rec, runner: runner, stop: stop, profile: profile.Ref}
}

func (f *launchFixture) post(path string, body any) (int, string) {
	code, raw := f.h.req(http.MethodPost, path, f.h.adminToken, f.h.tenantA, body)
	return code, string(raw)
}

// launch starts a default session and returns its run reference.
func (f *launchFixture) launch(t *testing.T) (code int, runRef, body string) {
	t.Helper()
	code, body = f.post("/v1/m/sessions/runs", map[string]any{"transport": "stream-json", "permission_mode": "plan", "isolation": "native", "provider_profile_ref": f.profile})
	var run struct {
		Ref string `json:"run_ref"`
	}
	_ = json.Unmarshal([]byte(body), &run)
	return code, run.Ref, body
}

// rearm forgets the reads so far and engages a real stop right after the next first read.
func (f *launchFixture) rearm(t *testing.T) { f.rearmAt(t, 1) }

// rearmAt engages the stop right after the n-th stop-state read instead.
func (f *launchFixture) rearmAt(t *testing.T, n int32) {
	f.stop.engageAt = n
	f.stop.reads.Store(0)
	f.stops.epochReads.Store(0)
	f.stop.engage = func() { f.engaged.Store(engageEstateStop(t, f.h)) }
}

// denialsOfEngagedStop counts the kill-switch denials on the ledger that name the stop
// rearm engaged. Nothing else in these tests is refused for that stop, so each one is
// the refused launch's own event. (A walked event carries no Meta, so it is matched by
// its action and its target.)
func (f *launchFixture) denialsOfEngagedStop(t *testing.T) int {
	t.Helper()
	stopID, _ := f.engaged.Load().(model.ID)
	ctx := context.Background()
	count := 0
	if err := f.h.st.View(ctx, model.TenantID(f.h.tenantA), func(sc store.Scope) error {
		return sc.Audit().Walk(ctx, 1, func(e model.AuditEvent) error {
			if e.Action == "security.killswitch.deny" && e.TargetID == stopID {
				count++
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	return count
}

// wantStopRefusal checks a launch or resume refused for a stop engaged after the first
// state read: 403 naming the stop from the second state read, before admission and the
// mint (no history read), no new process, and the denial on the ledger.
func (f *launchFixture) wantStopRefusal(t *testing.T, what string, code int, body string, processesBefore int32) {
	t.Helper()
	if code != http.StatusForbidden || !strings.Contains(body, "emergency stop active (") || !strings.Contains(body, "dual-control re-enable") {
		t.Fatalf("%s with a stop engaged during it: want 403 naming the stop, got %d %s", what, code, body)
	}
	if n := f.stops.epochReads.Load(); n != 0 {
		t.Fatalf("a launch refused before admission must not reach the mint, read stop history %d times", n)
	}
	if n := f.stop.reads.Load(); n != 2 {
		t.Fatalf("%s must be refused by the second stop-state read, read it %d times", what, n)
	}
	if n := f.runner.launches.Load(); n != processesBefore {
		t.Fatalf("a process started although a stop was engaged during the %s", what)
	}
	if n := f.denialsOfEngagedStop(t); n != 1 {
		t.Fatalf("the refused %s left %d kill-switch denials for the engaged stop on the ledger, want 1", what, n)
	}
}

// A default launch reads the cheap stop state twice (before the claim and before
// admission) and the stop history once, at mint: the bearer's validation inside that
// mint reuses the mint's read. A stop engaged after the first state read is refused by
// the second, with the status a stop has always had (403, a decision, not an outage to
// retry), before any process starts, and the denial still reaches the ledger.
func TestSessionLaunchStopEngagedAfterTheFirstReadIsForbidden(t *testing.T) {
	f := newLaunchFixture(t)

	// Control: with no stop, the same launch succeeds.
	if code, _, body := f.launch(t); code != http.StatusCreated {
		t.Fatalf("control launch=%d %s", code, body)
	}
	if n := f.stops.epochReads.Load(); n != 1 {
		t.Fatalf("a default launch must read the stop history once, at mint, got %d", n)
	}
	if n := f.stop.reads.Load(); n != 2 {
		t.Fatalf("a launch must read the stop state twice, read it %d times", n)
	}
	started := f.runner.launches.Load()

	f.rearm(t)
	code, _, body := f.launch(t)
	f.wantStopRefusal(t, "launch", code, body, started)
}

// A stop engaged after both state reads reaches the gate wired as boot wires it, and its
// mint refuses: 403 naming the stop, no process, and the denial on the ledger through
// the recorder that wiring hands the gate.
func TestSessionLaunchStopEngagedAfterBothStateReadsIsForbiddenAtMint(t *testing.T) {
	f := newLaunchFixture(t)
	f.rearmAt(t, 2)
	code, _, body := f.launch(t)
	if code != http.StatusForbidden || !strings.Contains(body, "emergency stop active (") {
		t.Fatalf("want 403 naming the stop, got %d %s", code, body)
	}
	if n := f.stops.epochReads.Load(); n != 1 {
		t.Fatalf("the mint must read the stop history once, got %d", n)
	}
	if n := f.runner.launches.Load(); n != 0 {
		t.Fatalf("started %d processes after the stop", n)
	}
	if n := f.denialsOfEngagedStop(t); n != 1 {
		t.Fatalf("the refused launch left %d kill-switch denials for the engaged stop, want 1", n)
	}
}

// Resume re-runs the same launch gate, so a stop engaged after its first read is refused
// the same way, and the stopped run is left as it was.
func TestSessionResumeStopEngagedAfterTheFirstReadIsForbidden(t *testing.T) {
	f := newLaunchFixture(t)
	code, runRef, body := f.launch(t)
	if code != http.StatusCreated || runRef == "" {
		t.Fatalf("launch=%d %s", code, body)
	}
	stopRun := func() {
		t.Helper()
		if code, body := f.post("/v1/m/sessions/runs/"+runRef+"/stop", nil); code < 200 || code > 299 {
			t.Fatalf("stop run=%d %s", code, body)
		}
	}
	stopRun()

	// Control: with no stop engaged, a resume reads the stop history once and succeeds.
	f.stop.reads.Store(0)
	f.stops.epochReads.Store(0)
	if code, body := f.post("/v1/m/sessions/runs/"+runRef+"/resume", nil); code != http.StatusOK {
		t.Fatalf("control resume=%d %s", code, body)
	}
	if n := f.stops.epochReads.Load(); n != 1 {
		t.Fatalf("a default resume must read the stop history once, at mint, got %d", n)
	}
	if n := f.stop.reads.Load(); n != 2 {
		t.Fatalf("a resume must read the stop state twice, read it %d times", n)
	}
	stopRun()
	started := f.runner.launches.Load()

	f.rearm(t)
	code, body = f.post("/v1/m/sessions/runs/"+runRef+"/resume", nil)
	f.wantStopRefusal(t, "resume", code, body, started)
}

// The epoch read already identifies the active stop. Its refusal keeps that
// identity for the response and audit instead of consulting mutable state again.
func TestSessionLaunchLateStopPreservesEpochDenial(t *testing.T) {
	h := newHarness(t)
	stopID := model.ID("already-observed-stop")
	g := &sessionLaunchGate{
		pep: &sessionPEPProvisioner{url: "http://127.0.0.1:8447/", mintLaunch: func(context.Context, model.TenantID, sessions.LaunchIntent) (string, error) {
			return "", &governance.SessionStopActiveError{StopID: stopID}
		}},
	}
	dec, err := g.Authorize(context.Background(), model.TenantID(h.tenantA), sessions.LaunchIntent{})
	if err != nil || dec.Allowed || !strings.Contains(dec.Reason, stopID.String()) {
		t.Fatalf("known stop must keep its refusal: %+v %v", dec, err)
	}
}

func TestSessionStopEpochMatchesStopGateAgentAttribution(t *testing.T) {
	h := newHarness(t)
	code, raw := h.req(http.MethodPost, "/v1/m/governance/killswitch", h.adminToken, h.tenantA, map[string]any{"scope_kind": "agent", "scope_ref": "epoch-agent", "reason": "stop attribution"})
	if code != http.StatusCreated {
		t.Fatalf("agent stop=%d %s", code, raw)
	}
	var stop struct {
		ID model.ID `json:"id"`
	}
	if err := json.Unmarshal(raw, &stop); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"epoch-agent", "  epoch-agent  "} {
		_, err := h.set.gov.SessionStopEpoch(context.Background(), model.TenantID(h.tenantA), ref)
		var active *governance.SessionStopActiveError
		if !errors.Is(err, governance.ErrSessionStopActive) || !errors.As(err, &active) || active.StopID != stop.ID {
			t.Fatalf("epoch must match the stop gate's agent attribution for %q: %v", ref, err)
		}
	}
}

func TestSessionStopEpochKeepsEstatePrecedence(t *testing.T) {
	h := newHarness(t)
	code, raw := h.req(http.MethodPost, "/v1/m/governance/killswitch", h.adminToken, h.tenantA, map[string]any{"scope_kind": "agent", "scope_ref": "epoch-agent", "reason": "agent attribution"})
	if code != http.StatusCreated {
		t.Fatalf("agent stop=%d %s", code, raw)
	}
	estate := engageEstateStop(t, h)
	tenant := model.TenantID(h.tenantA)
	_, err := h.set.gov.SessionStopEpoch(context.Background(), tenant, "epoch-agent")
	var active *governance.SessionStopActiveError
	if !errors.As(err, &active) || active.StopID != estate {
		t.Fatalf("epoch must preserve estate precedence: %v", err)
	}
}

func TestSessionLaunchCriticalLateStopPrecedesApproval(t *testing.T) {
	f := newLaunchFixture(t)
	f.rearm(t)
	code, body := f.post("/v1/m/sessions/runs", map[string]any{"transport": "stream-json", "permission_mode": "dontAsk", "isolation": "native", "provider_profile_ref": f.profile})
	f.wantStopRefusal(t, "critical launch", code, body, 0)
	if err := f.h.st.View(context.Background(), model.TenantID(f.h.tenantA), func(sc store.Scope) error {
		repo, err := sc.Ext(model.Kind("governance.approval"))
		if err != nil {
			return err
		}
		rows, _, err := repo.List(context.Background(), model.Query{Limit: 1})
		if err == nil && len(rows) != 0 {
			t.Errorf("late stop opened %d approvals", len(rows))
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// reserveThenStop is the real budget admission with a real estate stop engaged while
// it holds the reservation, so the stop commits after both stop-state reads and
// before Reserve answers Allowed=true.
type reserveThenStop struct {
	budgetChecker
	engage func()
}

func (r *reserveThenStop) Reserve(ctx context.Context, tenant model.TenantID, req finops.AdmissionRequest) (finops.Reservation, error) {
	res, err := r.budgetChecker.Reserve(ctx, tenant, req)
	if r.engage != nil {
		r.engage()
		r.engage = nil
	}
	return res, err
}

// reserveGate replaces the fixture's launch gate with the default composition whose
// budget admission engages a real estate stop once armed.
func (f *launchFixture) reserveGate(t *testing.T) *reserveThenStop {
	t.Helper()
	if f.h.set.finops == nil {
		t.Fatal("the harness must run the budget module")
	}
	reserve := &reserveThenStop{budgetChecker: f.h.set.finops}
	gate := &sessionLaunchGate{fin: reserve, pep: f.creds.provisioner(), stopDeny: f.rec, clock: time.Now}
	sessions.WithLaunchGate(buildSessionLaunchGate(f.h.set, gate))(f.h.set.sessions)
	return reserve
}

// wantReserveStopRefusal checks a launch or resume refused at mint for a stop engaged
// during Reserve: 403 naming the stop, the mint's one history read, both state reads,
// no new process and the denial on the ledger.
func (f *launchFixture) wantReserveStopRefusal(t *testing.T, what string, reserve *reserveThenStop, code int, body string, processesBefore int32) {
	t.Helper()
	if code != http.StatusForbidden || !strings.Contains(body, "emergency stop active (") {
		t.Fatalf("%s with a stop engaged during Reserve: want 403 naming the stop, got %d %s", what, code, body)
	}
	if reserve.engage != nil {
		t.Fatalf("%s: Reserve did not run, so the stop was not engaged during admission", what)
	}
	if n := f.stops.epochReads.Load(); n != 1 {
		t.Fatalf("%s: the mint must read the stop history once after admission, got %d history reads", what, n)
	}
	if n := f.stop.reads.Load(); n != 2 {
		t.Fatalf("%s must read the stop state twice, read it %d times", what, n)
	}
	if n := f.runner.launches.Load(); n != processesBefore {
		t.Fatalf("%s started %d processes after a stop engaged during Reserve", what, n-processesBefore)
	}
	if n := f.denialsOfEngagedStop(t); n != 1 {
		t.Fatalf("the refused %s left %d kill-switch denials for the engaged stop, want 1", what, n)
	}
}

// A stop that commits during budget admission must still refuse the launch at mint.
func TestSessionLaunchStopEngagedDuringBudgetReserveIsForbidden(t *testing.T) {
	f := newLaunchFixture(t)
	reserve := f.reserveGate(t)
	reserve.engage = func() { f.engaged.Store(engageEstateStop(t, f.h)) }
	code, _, body := f.launch(t)
	f.wantReserveStopRefusal(t, "launch", reserve, code, body, 0)
}

// Resume runs the same admission, so a stop committed during its Reserve is refused at
// mint the same way, and the stopped run is left as it was.
func TestSessionResumeStopEngagedDuringBudgetReserveIsForbidden(t *testing.T) {
	f := newLaunchFixture(t)
	reserve := f.reserveGate(t)
	code, runRef, body := f.launch(t)
	if code != http.StatusCreated || runRef == "" {
		t.Fatalf("launch=%d %s", code, body)
	}
	if code, body := f.post("/v1/m/sessions/runs/"+runRef+"/stop", nil); code < 200 || code > 299 {
		t.Fatalf("stop run=%d %s", code, body)
	}
	runState := func() string {
		t.Helper()
		var run struct {
			State string `json:"state"`
		}
		if code := f.h.reqInto(http.MethodGet, "/v1/m/sessions/runs/"+runRef, f.h.adminToken, f.h.tenantA, nil, &run); code != http.StatusOK {
			t.Fatalf("get run=%d", code)
		}
		return run.State
	}
	before := runState()
	started := f.runner.launches.Load()
	f.stop.reads.Store(0)
	f.stops.epochReads.Store(0)

	reserve.engage = func() { f.engaged.Store(engageEstateStop(t, f.h)) }
	code, body = f.post("/v1/m/sessions/runs/"+runRef+"/resume", nil)
	f.wantReserveStopRefusal(t, "resume", reserve, code, body, started)
	if after := runState(); after != before {
		t.Fatalf("the refused resume moved the run from %q to %q", before, after)
	}
}

// A queued relaunch (an approved CRITICAL create) is refused at mint with its run
// reference, so its stop denial keeps the run as subject, as the stop gate attributes it. Only a
// first create, which has no run row at its stop gate, is attributed to the agent.
func TestSessionLaunchStopDenialSubjectMatchesTheStopGate(t *testing.T) {
	h := newHarness(t)
	tenant := model.TenantID(h.tenantA)
	for _, tc := range []struct {
		name, approvalRef, want string
	}{
		{"first create", "", "agent-1"},
		{"queued relaunch", "apr-1", "run-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := newStopDenyRecorder(h.st, nil)
			g := &sessionLaunchGate{stopDeny: rec, pep: &sessionPEPProvisioner{url: "http://127.0.0.1:8447/", mintLaunch: func(context.Context, model.TenantID, sessions.LaunchIntent) (string, error) {
				return "", &governance.SessionStopActiveError{StopID: "stop-1"}
			}}}
			intent := sessions.LaunchIntent{Action: sessions.LaunchActionCreate, RunRef: "run-1", AgentRef: "agent-1", ApprovalRef: tc.approvalRef}
			if dec, err := g.Authorize(context.Background(), tenant, intent); err != nil || dec.Allowed {
				t.Fatalf("want the mint's stop refusal, got %+v %v", dec, err)
			}
			rec.mu.Lock()
			_, ok := rec.last[tenant.String()+"|sessions-launch|"+tc.want]
			rec.mu.Unlock()
			if !ok {
				t.Fatalf("the stop denial was not attributed to %q", tc.want)
			}
		})
	}
}
