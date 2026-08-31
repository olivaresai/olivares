// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// --- test doubles -----------------------------------------------------------

// fakeProc is a deterministic in-process Process: it emits whatever the runner
// pre-loaded, records sent input, and exits on Stop (or a simulated crash).
type fakeProc struct {
	out     chan OutputFrame
	stopped chan struct{}
	mu      sync.Mutex
	sent    [][]byte
	exit    int
	done    bool
}

func (p *fakeProc) Send(_ context.Context, line []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done {
		return errors.New("closed")
	}
	p.sent = append(p.sent, append([]byte(nil), line...))
	return nil
}
func (p *fakeProc) Output() <-chan OutputFrame { return p.out }
func (p *fakeProc) Wait() (int, error)         { <-p.stopped; return p.exit, nil }
func (p *fakeProc) Stop(context.Context) error { p.finish(143); return nil } // SIGTERM-like
func (p *fakeProc) PID() int                   { return 4242 }

func (p *fakeProc) finish(code int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.done {
		p.done = true
		p.exit = code
		close(p.out)
		close(p.stopped)
	}
}

func (p *fakeProc) sentCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.sent)
}

// fakeRunner hands out fakeProcs, optionally pre-loading an init frame, and
// captures every launch spec so a test can assert the constructed argv.
type fakeRunner struct {
	launchErr error
	initSID   string
	mu        sync.Mutex
	procs     []*fakeProc
	specs     []LaunchSpec
}

func (r *fakeRunner) Launch(_ context.Context, spec LaunchSpec) (Process, error) {
	r.mu.Lock()
	r.specs = append(r.specs, spec)
	lerr := r.launchErr
	r.mu.Unlock()
	if lerr != nil {
		return nil, lerr
	}
	p := &fakeProc{out: make(chan OutputFrame, 16), stopped: make(chan struct{})}
	if r.initSID != "" {
		p.out <- OutputFrame{Stream: streamStdout, Data: []byte(`{"type":"system","subtype":"init","session_id":"` + r.initSID + `"}`)}
	}
	r.mu.Lock()
	r.procs = append(r.procs, p)
	r.mu.Unlock()
	return p, nil
}

// failNextLaunch makes every subsequent spawn fail, so a run that launched cleanly
// can be made to fail on its RESUME — the second of the two paths R3-03 named.
func (r *fakeRunner) failNextLaunch(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.launchErr = err
}

func (r *fakeRunner) lastProc() *fakeProc {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.procs[len(r.procs)-1]
}

func (r *fakeRunner) lastSpec() LaunchSpec {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.specs[len(r.specs)-1]
}

// farFuture keeps the mock credential valid under BOTH the fixed test clock
// (baseTime) and the real wall clock the HTTP harness uses.
var farFuture = time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)

func staticCred() CredentialSource {
	return CredentialSourceFunc(func(context.Context, CredentialRequest) (Credential, error) {
		return Credential{ID: "cred-1", Token: "tok-secret", Scheme: "mock", NotAfter: farFuture}, nil
	})
}

func stopModuleAtCleanup(t *testing.T, m *Module) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// Failure-path tests assert their own compensation errors. Cleanup is
		// best-effort and only guarantees that still-registered runs are stopped.
		_ = m.Stop(ctx)
	})
}

// newRuntimeHarness builds a sessions module with the operate runtime wired and a
// real SQLite store + a business tenant (the live_test newSess pattern + options).
func newRuntimeHarness(t *testing.T, opts ...Option) (*Module, store.Store, model.TenantID, *testClock) {
	t.Helper()
	clk := &testClock{now: baseTime}
	m := New(append([]Option{WithClock(clk)}, opts...)...)
	ctx := context.Background()
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, m.RegisterSchema)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, e := sys.EnsureSystemTenant(ctx); e != nil {
			return e
		}
		org, e := sys.CreateOrg(ctx, model.Org{Name: "acme", Slug: "acme", Status: model.StatusActive})
		tenant = org.TenantID
		return e
	}); err != nil {
		t.Fatalf("tenant: %v", err)
	}
	m.UseData(api.NewModuleData(st))
	stopModuleAtCleanup(t, m)
	return m, st, tenant, clk
}

// registerTestWorkspace registers a workspace rooted at root and returns its ref, so
// a lifecycle test can launch against a GOVERNED workspace (formalized ref→path:
// a launch's workspace_ref must be a registered workspace, no longer a literal dir).
func registerTestWorkspace(t *testing.T, m *Module, tenant model.TenantID, root string) string {
	t.Helper()
	dto, err := m.createWorkspace(context.Background(), tenant, CreateWorkspaceParams{
		RootPath: root, Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("registerTestWorkspace(%q): %v", root, err)
	}
	return dto.WorkspaceRef
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

func listRunEvents(t *testing.T, st store.Store, tenant model.TenantID, ref string) []runEventDTO {
	t.Helper()
	var out []runEventDTO
	err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runEventKind)
		if err != nil {
			return err
		}
		recs, _, err := repo.List(context.Background(), model.Query{
			Filters: []model.Filter{eq(colEvRunRef, ref)},
			Sort:    []model.Sort{{Column: colEvSeq, Desc: false}},
		})
		if err != nil {
			return err
		}
		for _, r := range recs {
			out = append(out, toRunEventDTO(r))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	return out
}

func eventNames(evs []runEventDTO) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.Event
	}
	return out
}

func TestValidateCreateRejectsSecretBearingEnvAllowNames(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		"OLIVARES_WORK_TOKEN", "OLIVARES_AUDIT_SIGNING_KEY", "ANTHROPIC_API_KEY",
		"CLAUDE_CODE_USE_BEDROCK", "DISABLE_AUTOUPDATER", "BAD=NAME", "9BAD", "",
	} {
		t.Run(name, func(t *testing.T) {
			p := CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, EnvAllow: []string{name}}
			if err := validateCreate(&p); err == nil {
				t.Fatalf("env_allow %q accepted", name)
			}
		})
	}
	p := CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		EnvAllow: []string{"MY_PROJECT_FLAG"},
	}
	if err := validateCreate(&p); err != nil {
		t.Fatalf("innocuous env_allow rejected: %v", err)
	}
	dup := CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		EnvAllow: []string{"MY_PROJECT_FLAG", "MY_PROJECT_FLAG"},
	}
	if err := validateCreate(&dup); err == nil {
		t.Fatal("duplicate env_allow accepted")
	}
}

// --- the headline lifecycle test (deterministic, fake runner) ----------------

func TestRuntime_FullLifecycle(t *testing.T) {
	t.Parallel()

	fr := &fakeRunner{initSID: "sess-9"}
	m, st, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	ctx := context.Background()

	dto, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, PermissionMode: "default", Effort: "high",
		Model: "claude-opus-4-8", Isolation: IsolationNative, WorkspaceRef: registerTestWorkspace(t, m, tenant, t.TempDir()),
		Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	if dto.State != stateRunning || dto.RunRef == "" {
		t.Fatalf("after create: state=%s ref=%q", dto.State, dto.RunRef)
	}
	if dto.CredentialID != "cred-1" {
		t.Fatalf("credential id not recorded: %q", dto.CredentialID)
	}
	ref := dto.RunRef

	// The launch argv carries the governed stream-json flags + the run params, and
	// the env carries the minted token (never the row).
	spec := fr.lastSpec()
	assertArgs(t, spec.Args, "--input-format", "stream-json")
	assertArgs(t, spec.Args, "--permission-mode", "default")
	assertArgs(t, spec.Args, "--model", "claude-opus-4-8")
	if !hasEnv(spec.Env, "ANTHROPIC_AUTH_TOKEN", "tok-secret") {
		t.Fatalf("minted token not injected into launch env")
	}
	// A conducted session is pinned: the auto-updater is disabled for the child so it
	// cannot self-mutate mid-session (the env allowlist would otherwise strip it).
	if !hasEnv(spec.Env, "DISABLE_AUTOUPDATER", "1") {
		t.Fatalf("conducted claude must have DISABLE_AUTOUPDATER=1 in its launch env")
	}

	// The init frame is bridged → the resumable session id is captured.
	waitFor(t, "session id capture", func() bool {
		d, _ := m.getRun(ctx, tenant, ref)
		return d.ClaudeSessionID == "sess-9"
	})

	// Input reaches the process stdin.
	if err := m.sendInput(ctx, tenant, ref, []byte(`{"type":"user"}`)); err != nil {
		t.Fatalf("sendInput: %v", err)
	}
	waitFor(t, "input delivered", func() bool { return fr.lastProc().sentCount() == 1 })

	// Stop → the bridge finalizes the row to stopped (NOT failed: it was requested).
	sd, err := m.stopRun(ctx, tenant, ref, "user:u1", "user")
	if err != nil {
		t.Fatalf("stopRun: %v", err)
	}
	if sd.State != stateStopped {
		t.Fatalf("after stop: state=%s want stopped", sd.State)
	}
	if sd.ExitCode == nil || *sd.ExitCode != 143 {
		t.Fatalf("after stop: exit=%v want 143", sd.ExitCode)
	}

	// The lifecycle ledger records the transitions in order.
	names := eventNames(listRunEvents(t, st, tenant, ref))
	assertSubsequence(t, names, "created", "launched", "stopping", "stopped")

	// The global audit chain is intact (the lifecycle events were sealed in it).
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		rep, e := sc.Audit().Verify(ctx, 0)
		if e != nil {
			return e
		}
		if !rep.OK {
			t.Fatalf("audit chain broken at seq %d: %s", rep.BreakAt, rep.Reason)
		}
		return nil
	}); err != nil {
		t.Fatalf("audit verify: %v", err)
	}

	// Resume relaunches against the captured session id (--resume on the argv).
	rd, err := m.resumeRun(ctx, tenant, ref, "user:u1", "user", "")
	if err != nil {
		t.Fatalf("resumeRun: %v", err)
	}
	if rd.State != stateRunning {
		t.Fatalf("after resume: state=%s want running", rd.State)
	}
	assertArgs(t, fr.lastSpec().Args, "--resume", "sess-9")

	// Stop again, then clean up and delete.
	if _, err := m.stopRun(ctx, tenant, ref, "user:u1", "user"); err != nil {
		t.Fatalf("stop after resume: %v", err)
	}
	cd, err := m.cleanupRun(ctx, tenant, ref, "user:u1", "user")
	if err != nil {
		t.Fatalf("cleanupRun: %v", err)
	}
	if cd.State != stateCleaned || cd.ClaudeSessionID != "" {
		t.Fatalf("after cleanup: state=%s claudeID=%q", cd.State, cd.ClaudeSessionID)
	}
	if err := m.deleteRun(ctx, tenant, ref); err != nil {
		t.Fatalf("deleteRun: %v", err)
	}
	if _, err := m.getRun(ctx, tenant, ref); err == nil {
		t.Fatalf("expected not-found after delete")
	}
}

func TestRuntime_LaunchGateDenies(t *testing.T) {
	t.Parallel()

	fr := &fakeRunner{}
	m, st, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()),
		WithLaunchGate(denyGate{reason: "over budget"}))
	_, err := m.createRun(context.Background(), tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:u1", ActorKind: "user",
	})
	if err == nil {
		t.Fatalf("expected a gate denial")
	}
	if re, ok := err.(*runErr); !ok || re.status != 403 {
		t.Fatalf("want 403 runErr, got %v", err)
	}
	// No row was persisted (the gate runs before any write).
	if n := countRuns(t, st, tenant); n != 0 {
		t.Fatalf("a denied launch must not persist a row (found %d)", n)
	}
}

func TestRuntime_CredentialDenyClosed(t *testing.T) {
	t.Parallel()

	fr := &fakeRunner{}
	// No credential source wired → stream-json launch is deny-closed.
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr))
	_, err := m.createRun(context.Background(), tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:u1", ActorKind: "user",
	})
	if err == nil {
		t.Fatalf("expected deny-closed (no credential source)")
	}
}

// Renamed to what it checks. It asserts that ONE run row survives a launch that could
// not spawn — the count, and nothing else: not its state, not its ledger, not its
// claim. The old name said "LaunchFailureRecorded", which is how it came to be
// credited with a guarantee it never checked (R3-03); it stayed green with both
// releases deleted. The two tests below are that guarantee.
func TestRuntime_ALaunchThatCannotSpawnStillLeavesOneRow(t *testing.T) {
	t.Parallel()

	fr := &fakeRunner{launchErr: errors.New("exec: \"claude\": not found")}
	m, st, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	_, err := m.createRun(context.Background(), tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, WorkspaceRef: registerTestWorkspace(t, m, tenant, t.TempDir()),
		Actor: "user:u1", ActorKind: "user",
	})
	if err == nil {
		t.Fatalf("expected a launch failure")
	}
	// EXACTLY what is asserted: one row survives. Not its state, not its ledger.
	if n := countRuns(t, st, tenant); n != 1 {
		t.Fatalf("a failed launch must leave exactly one run row behind (found %d)", n)
	}
}

// R3-03, the CREATE path. There is no process, so there is no bridge and no finalize
// to give the claim back; without an explicit release the session stays held for the
// rest of its TTL by a launch that never happened.
func TestRuntime_ACreateThatCannotSpawnGivesTheClaimBack(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gate := &spyGate{inner: LaunchDecision{Allowed: true}}
	fr := &fakeRunner{launchErr: errors.New("exec: \"claude\": not found")}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()),
		WithLaunchGate(gate))

	if _, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: "user",
	}); err == nil {
		t.Fatal("a create whose runner could not spawn returned no error")
	}
	sid := gate.last(t).ClaimSID
	if sid == "" {
		t.Fatal("the gate never saw a claim to release")
	}
	if _, live, err := m.ActiveClaim(ctx, tenant, sid); err != nil || live {
		t.Errorf("a launch that never spawned left the session claimed (live=%v err=%v): "+
			"nothing else will ever release it", live, err)
	}
}

// R3-03, the RESUME path. Separate from the create deliberately — one test asserting
// both releases would also die to either deletion, so this is about isolation and
// attribution rather than about detection: each mutant names exactly one call site.
// This one also proves the release matches the claim the RESUME took — a takeover at
// a moved fence, not the one the create was holding.
func TestRuntime_AResumeThatCannotSpawnGivesTheClaimBack(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gate := &spyGate{inner: LaunchDecision{Allowed: true}}
	fr := &fakeRunner{initSID: "sess-resume-nospawn"}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()),
		WithLaunchGate(gate))

	dto, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	waitFor(t, "claude session id captured", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.ClaudeSessionID == "sess-resume-nospawn"
	})
	if _, err := m.stopRun(ctx, tenant, dto.RunRef, "user:u1", "user"); err != nil {
		t.Fatalf("stopRun: %v", err)
	}
	sid := gate.last(t).ClaimSID
	createFence := gate.last(t).Fence

	fr.failNextLaunch(errors.New("exec: \"claude\": not found"))
	if _, err := m.resumeRun(ctx, tenant, dto.RunRef, "user:u2", "user", ""); err == nil {
		t.Fatal("a resume whose runner could not spawn returned no error")
	}
	if got := gate.last(t); got.Action != LaunchActionResume || got.Fence <= createFence {
		t.Fatalf("the resume did not take the session over (action %q, fence %d against %d), "+
			"so this test would prove nothing", got.Action, got.Fence, createFence)
	}
	if _, live, err := m.ActiveClaim(ctx, tenant, sid); err != nil || live {
		t.Errorf("a resume that never spawned left the session claimed (live=%v err=%v)", live, err)
	}
}

func TestRuntime_OrphanReconciledOnStop(t *testing.T) {
	t.Parallel()

	fr := &fakeRunner{initSID: "sess-x"}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	ctx := context.Background()
	dto, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, WorkspaceRef: registerTestWorkspace(t, m, tenant, t.TempDir()),
		Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	lr, ok := m.rt.getLive(tenant, dto.RunRef)
	if !ok {
		t.Fatal("created run has no live handle")
	}
	t.Cleanup(func() {
		_ = lr.proc.Stop(context.Background())
		select {
		case <-lr.finalizedCh:
		case <-time.After(3 * time.Second):
			t.Error("dropped orphan process did not finalize")
		}
	})
	// Simulate a runtime restart: the durable row says running but the live handle
	// is gone.
	m.rt.dropLive(tenant, dto.RunRef)
	sd, err := m.stopRun(ctx, tenant, dto.RunRef, "user:u1", "user")
	if err != nil {
		t.Fatalf("stop orphan: %v", err)
	}
	if sd.State != stateStopped {
		t.Fatalf("orphan reconcile: state=%s want stopped", sd.State)
	}
}

func TestRuntime_IdleDerivedAtReadTime(t *testing.T) {
	t.Parallel()

	fr := &fakeRunner{initSID: "sess-i"}
	m, _, tenant, clk := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()),
		WithRuntimeIdleWindow(time.Minute))
	ctx := context.Background()
	dto, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, WorkspaceRef: registerTestWorkspace(t, m, tenant, t.TempDir()),
		Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	// Now: still running.
	if d, _ := m.getRun(ctx, tenant, dto.RunRef); d.State != stateRunning {
		t.Fatalf("fresh session should be running, got %s", d.State)
	}
	// Advance past the idle window: the STORED state stays running, the DERIVED
	// state is idle.
	clk.set(baseTime.Add(2 * time.Minute))
	d, _ := m.getRun(ctx, tenant, dto.RunRef)
	if d.State != stateIdle {
		t.Fatalf("stale running session should derive idle, got %s", d.State)
	}
}

func TestRuntime_RemoteControlRejectsInput(t *testing.T) {
	t.Parallel()

	fr := &fakeRunner{}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr))
	ctx := context.Background()
	// remote-control needs no minted credential (operator OAuth); launch succeeds.
	dto, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportRemoteControl, Isolation: IsolationNative, Name: "mobile",
		Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("createRun remote-control: %v", err)
	}
	if err := m.sendInput(ctx, tenant, dto.RunRef, []byte("x")); err == nil {
		t.Fatalf("remote-control input must be rejected (I/O not bridged)")
	}
}

// --- small assertion helpers ------------------------------------------------

type denyGate struct{ reason string }

func (g denyGate) Authorize(context.Context, model.TenantID, LaunchIntent) (LaunchDecision, error) {
	return LaunchDecision{Allowed: false, Reason: g.reason}, nil
}

func countRuns(t *testing.T, st store.Store, tenant model.TenantID) int {
	t.Helper()
	n := 0
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		recs, _, err := repo.List(context.Background(), model.Query{})
		n = len(recs)
		return err
	}); err != nil {
		t.Fatalf("count runs: %v", err)
	}
	return n
}

func assertArgs(t *testing.T, args []string, want ...string) {
	t.Helper()
	for i := 0; i+len(want) <= len(args); i++ {
		match := true
		for j := range want {
			if args[i+j] != want[j] {
				match = false
				break
			}
		}
		if match {
			return
		}
	}
	t.Fatalf("argv %v missing subsequence %v", args, want)
}

func hasEnv(env []EnvVar, name, val string) bool {
	for _, e := range env {
		if e.Name == name && e.Value == val {
			return true
		}
	}
	return false
}

func assertSubsequence(t *testing.T, got []string, want ...string) {
	t.Helper()
	i := 0
	for _, g := range got {
		if i < len(want) && g == want[i] {
			i++
		}
	}
	if i != len(want) {
		t.Fatalf("event sequence %v does not contain subsequence %v", got, want)
	}
}
