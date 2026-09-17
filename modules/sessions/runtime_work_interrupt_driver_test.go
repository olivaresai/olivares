// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// The fenced turn interruption against a REAL owned child, over the official
// Codex app-server protocol.
//
// The port's refusal directions and its uncertainty classification are pinned
// next door with a driver the test controls. What only a real child can show is
// the thing the control exists for: the turn ends, the PROCESS does not, and the
// same conversation takes the next input.

// TestCodexRuntimeWorkBoundRunHasFencedTurnInterrupt is the missing third
// control, end to end. Before it, a work-bound driver run could be spoken to and
// killed, and taking back a turn meant killing it.
func TestCodexRuntimeWorkBoundRunHasFencedTurnInterrupt(t *testing.T) {
	m, st, tenant, prof := codexHarness(t, AuthSourceAccountHome,
		WithWorkIdentityResolver(allowWorkIdentity{}),
		WithWorkContentGuard(allowWorkContent{}),
	)
	ctx := context.Background()
	itemID, _, agentRef := readyWorkLaunchItem(t, m, st, tenant)
	m.UseWorkIdentityResolver(durableWorkLaunchIdentity{m: m, st: st})
	record := setCodexFixture(t, prof, codexFixture{ThreadID: "thread-work-interrupt", Account: "apikey"})
	spec := workLaunchSpec(itemID, agentRef)
	spec.Runtime.ProviderProfileRef = prof.Ref

	managed, err := m.LaunchForWork(ctx, tenant, spec)
	if err != nil {
		t.Fatalf("LaunchForWork Codex: %v", err)
	}
	lr, ok := m.rt.getLive(tenant, managed.RunRef)
	if !ok {
		t.Fatal("the work-launched Codex run has no live handle")
	}
	pid := lr.proc.PID()
	t.Cleanup(func() {
		_ = m.StopForWork(context.Background(), tenant, managed.RunRef, managed.WorkLeaseFence, "test cleanup")
	})

	if err := m.TextForWork(ctx, tenant, managed.RunRef, managed.WorkLeaseFence, "long task"); err != nil {
		t.Fatalf("fenced text: %v", err)
	}
	waitFor(t, "the turn is active on the child", func() bool { return lr.session.ActiveTurn() != "" })
	starts := countMethod(readFixtureRecord(t, record).Methods, codexMethodTurnStart)

	// A stale fence refuses BEFORE the child hears anything.
	if err := m.InterruptForWork(ctx, tenant, managed.RunRef, managed.WorkLeaseFence+1); err == nil {
		t.Fatal("a stale work fence must refuse the interrupt")
	}
	if got := countMethod(readFixtureRecord(t, record).Methods, codexMethodTurnInterrupt); got != 0 {
		t.Fatalf("a stale fence crossed the process boundary: turn/interrupt = %d", got)
	}
	// And so does the unfenced legacy route: the durable stamp owns this run.
	if _, err := m.interruptRun(ctx, tenant, managed.RunRef, "user:operator", model.ActorUser); err == nil {
		t.Fatal("the unfenced interrupt must refuse a work-bound run")
	}
	if got := countMethod(readFixtureRecord(t, record).Methods, codexMethodTurnInterrupt); got != 0 {
		t.Fatalf("the unfenced route crossed the process boundary: turn/interrupt = %d", got)
	}

	if err := m.InterruptForWork(ctx, tenant, managed.RunRef, managed.WorkLeaseFence); err != nil {
		t.Fatalf("the exact work fence must cancel the turn: %v", err)
	}
	waitFor(t, "the child cancelled its turn", func() bool {
		return countMethod(readFixtureRecord(t, record).Methods, codexMethodTurnInterrupt) == 1
	})
	if lr.session.ActiveTurn() != "" {
		t.Fatal("the interrupted turn is still the active one")
	}
	if !processRunning(pid) {
		t.Fatal("the fenced interrupt ended the owned process; it is not a stop")
	}
	rec, err := m.loadRun(ctx, tenant, managed.RunRef)
	if err != nil {
		t.Fatalf("re-read the run: %v", err)
	}
	if rec.String(colState) != stateRunning {
		t.Fatalf("interrupt is NOT terminal; state = %q", rec.String(colState))
	}
	if got := countNamedRunEvents(t, st, tenant, managed.RunRef, workInterruptAccepted); got != 1 {
		t.Fatalf("settled %d %s event(s), want 1", got, workInterruptAccepted)
	}

	// The run is still SPEAKABLE, on the same conversation and the same child.
	if err := m.TextForWork(ctx, tenant, managed.RunRef, managed.WorkLeaseFence, "next"); err != nil {
		t.Fatalf("fenced text after the interrupt: %v", err)
	}
	waitFor(t, "a new turn started on the same child", func() bool {
		return countMethod(readFixtureRecord(t, record).Methods, codexMethodTurnStart) == starts+1
	})
	if got := countMethod(readFixtureRecord(t, record).Methods, codexMethodThreadStart); got != 1 {
		t.Fatalf("the interrupt started %d conversations; it must start none", got-1)
	}
}

// TestRuntimeWorkAPIFencedInterruptKeepsTheRunSpeakable is the same contract over
// the half a client actually speaks. The endpoint answers with the LIVE row, not
// a terminal one, and the run takes its next input through the same fence.
func TestRuntimeWorkAPIFencedInterruptKeepsTheRunSpeakable(t *testing.T) {
	m, h, admin, tenant, prof := codexHTTPHarness(t, "runtime-work-interrupt")
	record := setCodexFixture(t, prof, codexFixture{ThreadID: "thread-http-interrupt", Account: "apikey"})
	runRef, live := codexHTTPRun(t, m, h, admin, tenant, prof)

	fence := bindRunToFreshWorkLease(t, m, h, tenant, runRef, live.claim.SID)
	inputPath := "/v1/m/sessions/runs/" + runRef + "/input"
	interruptPath := "/v1/m/sessions/runs/" + runRef + "/interrupt"

	opened := h.doJSON(http.MethodPost, inputPath, admin, map[string]any{
		"text": "long task", "work_lease_fence": fence,
	}, tenantHdr(tenant))
	if opened.code != http.StatusAccepted {
		t.Fatalf("fenced text = %d %s", opened.code, opened.raw)
	}
	waitFor(t, "the turn is active on the child", func() bool { return live.session.ActiveTurn() != "" })
	starts := countMethod(readFixtureRecord(t, record).Methods, codexMethodTurnStart)

	interrupted := h.doJSON(http.MethodPost, interruptPath, admin, map[string]any{
		"work_lease_fence": fence,
	}, tenantHdr(tenant))
	if interrupted.code != http.StatusOK {
		t.Fatalf("fenced interrupt = %d %s", interrupted.code, interrupted.raw)
	}
	if interrupted.body["state"] != stateRunning {
		t.Fatalf("the fenced interrupt reported state %v; it is not terminal", interrupted.body["state"])
	}
	waitFor(t, "the child cancelled its turn", func() bool {
		return countMethod(readFixtureRecord(t, record).Methods, codexMethodTurnInterrupt) == 1
	})
	if !processRunning(live.proc.PID()) {
		t.Fatal("the fenced interrupt ended the owned process")
	}
	who := h.do(http.MethodGet, "/v1/auth/whoami", admin, tenantHdr(tenant))
	userID, _ := who.body["user_id"].(string)
	if who.code != http.StatusOK || userID == "" {
		t.Fatalf("authenticated interrupt caller unavailable: %d", who.code)
	}
	seenAudit := 0
	for _, event := range listRunEvents(t, h.st, tenant, runRef) {
		if event.Event != "interrupting" && event.Event != "interrupted" {
			continue
		}
		seenAudit++
		if event.Actor != "user:"+userID || event.ActorKind != model.ActorUser {
			t.Fatalf("fenced HTTP interrupt lost its authenticated caller: %+v", event)
		}
	}
	if seenAudit != 2 {
		t.Fatalf("fenced HTTP interrupt audit events = %d, want intent and outcome", seenAudit)
	}

	next := h.doJSON(http.MethodPost, inputPath, admin, map[string]any{
		"text": "next", "work_lease_fence": fence,
	}, tenantHdr(tenant))
	if next.code != http.StatusAccepted {
		t.Fatalf("fenced text after the interrupt = %d %s", next.code, next.raw)
	}
	waitFor(t, "a new turn started on the same child", func() bool {
		return countMethod(readFixtureRecord(t, record).Methods, codexMethodTurnStart) == starts+1
	})
}

// TestRuntimeAPIUnfencedInterruptStillServesANonWorkRun is the compatibility
// control, and it is the reason the body had to stay OPTIONAL. A non-work run
// keeps the contract it has always had: no body, no fence, and the turn ends.
func TestRuntimeAPIUnfencedInterruptStillServesANonWorkRun(t *testing.T) {
	m, h, admin, tenant, prof := codexHTTPHarness(t, "runtime-legacy-interrupt")
	record := setCodexFixture(t, prof, codexFixture{ThreadID: "thread-http-legacy", Account: "apikey"})
	runRef, live := codexHTTPRun(t, m, h, admin, tenant, prof)

	opened := h.doJSON(http.MethodPost, "/v1/m/sessions/runs/"+runRef+"/input", admin, map[string]any{
		"text": "long task",
	}, tenantHdr(tenant))
	if opened.code != http.StatusAccepted {
		t.Fatalf("unfenced text on a non-work run = %d %s", opened.code, opened.raw)
	}
	waitFor(t, "the turn is active on the child", func() bool { return live.session.ActiveTurn() != "" })

	// No body at all: exactly the request a pre-existing client sends.
	interrupted := h.do(http.MethodPost, "/v1/m/sessions/runs/"+runRef+"/interrupt", admin, tenantHdr(tenant))
	if interrupted.code != http.StatusOK || interrupted.body["state"] != stateRunning {
		t.Fatalf("legacy empty-body interrupt = %d %s", interrupted.code, interrupted.raw)
	}
	waitFor(t, "the child cancelled its turn", func() bool {
		return countMethod(readFixtureRecord(t, record).Methods, codexMethodTurnInterrupt) == 1
	})
	if !processRunning(live.proc.PID()) {
		t.Fatal("the legacy interrupt ended the owned process")
	}
	// A legacy interrupt settles nothing on the work plane: this run has none.
	if got := countNamedRunEvents(t, h.st, tenant, runRef, workInterruptAccepted); got != 0 {
		t.Fatalf("a non-work interrupt settled %d fenced event(s)", got)
	}
}

// codexHTTPHarness builds the Codex runtime behind the real HTTP surface. It is
// the shape runtime_work_text_api_test.go established, lifted so the three
// interrupt cases share one launch path instead of three copies of it.
func codexHTTPHarness(
	t *testing.T,
	org string,
	opts ...Option,
) (*Module, *harness, string, model.TenantID, ProviderProfile) {
	t.Helper()
	m := New(append([]Option{
		WithRunner(NewProcRunner()),
		WithProviderDriver(NewCodexDriver()),
		WithDriverProgram(providerDriverCodex, os.Args[0]),
		WithProductVersion("test"),
		WithStopWaitDelay(2 * time.Second),
		WithDriverTimeouts(20*time.Second, 2*time.Second),
		WithWorkIdentityResolver(allowWorkIdentity{}),
		WithWorkContentGuard(allowWorkContent{}),
	}, opts...)...)
	m.UseExecutionEnvironmentRef(testEnvRef)
	m.EnableProfiledLaunches()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, org)
	prof := mustCreateProfile(t, m, tenant, CreateProfileInput{
		Driver: providerDriverCodex, ConfigHome: t.TempDir(), UserHome: t.TempDir(),
		DisplayName: org, AuthSource: AuthSourceAccountHome,
	})
	return m, h, admin, tenant, prof
}

// codexHTTPRun creates one profiled Codex run over HTTP and reaps its child at
// cleanup WITHOUT the operator stop, so a test that moves authority still tidies.
func codexHTTPRun(
	t *testing.T,
	m *Module,
	h *harness,
	admin string,
	tenant model.TenantID,
	prof ProviderProfile,
) (string, *liveRun) {
	t.Helper()
	created := h.doJSON(http.MethodPost, "/v1/m/sessions/runs", admin, map[string]any{
		"transport": "stream-json", "permission_mode": "default", "isolation": "native",
		"provider_profile_ref": prof.Ref,
	}, tenantHdr(tenant))
	runRef, _ := created.body["run_ref"].(string)
	if created.code != http.StatusCreated || runRef == "" {
		t.Fatalf("create profiled run = %d %s", created.code, created.raw)
	}
	live, ok := m.rt.getLive(tenant, runRef)
	if !ok || live.claim.SID == "" {
		t.Fatalf("run %s has no admission claim", runRef)
	}
	t.Cleanup(func() {
		_ = live.proc.Stop(context.Background())
		select {
		case <-live.finalizedCh:
		case <-time.After(5 * time.Second):
		}
	})
	return runRef, live
}
