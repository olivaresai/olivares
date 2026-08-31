// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// runtime_governance_test.go exercises the governance wiring the runtime applies:
// the preflight ORDER (kill-switch before the launch gate, so stop > HITL/break-glass),
// the LaunchDecision instructions (PEP env injection + per-run I/O recording), the
// denied-status mapping (402/429 for budget), and the ACTIVE kill-switch termination
// sweep (the "para" half — sessions terminates running work, not only blocks launches).

// flipStopGate is a kill-switch gate that starts open and can flip to stopped, so a
// test can launch a run and THEN engage an estate stop (the sweep scenario).
type flipStopGate struct {
	mu      sync.Mutex
	stopped bool
	checks  int
}

func (g *flipStopGate) flip() { g.mu.Lock(); g.stopped = true; g.mu.Unlock() }

func (g *flipStopGate) Check(context.Context, model.TenantID, StopDims) (StopDecision, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.checks++
	if g.stopped {
		return StopDecision{Stopped: true, StopRef: "ks-1", Scope: "estate"}, nil
	}
	return StopDecision{}, nil
}

// recordingLaunchGate records whether it was consulted and returns a fixed decision.
type recordingLaunchGate struct {
	called bool
	dec    LaunchDecision
}

func (g *recordingLaunchGate) Authorize(context.Context, model.TenantID, LaunchIntent) (LaunchDecision, error) {
	g.called = true
	return g.dec, nil
}

// capturingRecorder records the frames it is offered (and whether Finalize ran).
type capturingRecorder struct {
	mu        sync.Mutex
	frames    []RecordedFrame
	finalized bool
}

func (r *capturingRecorder) Record(_ context.Context, _ model.TenantID, _ string, f RecordedFrame) error {
	r.mu.Lock()
	r.frames = append(r.frames, f)
	r.mu.Unlock()
	return nil
}

func (r *capturingRecorder) Finalize(context.Context, model.TenantID, string) error {
	r.mu.Lock()
	r.finalized = true
	r.mu.Unlock()
	return nil
}

func (r *capturingRecorder) count() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.frames) }
func (r *capturingRecorder) didFinalize() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.finalized
}

// TestRuntime_StopCheckedBeforeLaunchGate proves the preflight order: an active
// emergency stop denies the launch WITHOUT consulting the launch gate (which holds the
// budget/HITL/break-glass path) — the "stop > break-glass" ordering chose.
func TestRuntime_StopCheckedBeforeLaunchGate(t *testing.T) {
	t.Parallel()

	fr := &fakeRunner{}
	lg := &recordingLaunchGate{dec: LaunchDecision{Allowed: true}}
	m, st, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()),
		WithLaunchGate(lg), WithStopGate(&flipStopGate{stopped: true}))
	_, err := m.createRun(context.Background(), tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:u1", ActorKind: "user",
	})
	if err == nil {
		t.Fatal("expected a kill-switch denial")
	}
	re, ok := err.(*runErr)
	if !ok || re.status != 403 {
		t.Fatalf("want 403 runErr, got %v", err)
	}
	if lg.called {
		t.Fatal("launch gate must NOT be consulted once the kill-switch denies (stop > HITL/break-glass)")
	}
	if n := countRuns(t, st, tenant); n != 0 {
		t.Fatalf("a stopped launch must persist no row (found %d)", n)
	}
}

// TestRuntime_LaunchDecisionStatusMapped maps the gate's DeniedStatus (e.g. budget 402)
// onto the launch error, instead of the default 403.
func TestRuntime_LaunchDecisionStatusMapped(t *testing.T) {
	t.Parallel()

	fr := &fakeRunner{}
	lg := &recordingLaunchGate{dec: LaunchDecision{Allowed: false, Reason: "budget hard cap reached", DeniedStatus: 402}}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()), WithLaunchGate(lg))
	_, err := m.createRun(context.Background(), tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:u1", ActorKind: "user",
	})
	re, ok := err.(*runErr)
	if !ok || re.status != 402 {
		t.Fatalf("want 402 runErr (budget), got %v", err)
	}
}

// TestRuntime_PEPEnvInjectedAndIORecorded proves the LaunchDecision instructions are
// applied: the governance env reaches the child, and the run's I/O is offered to the
// recorder ONLY when the gate flagged it.
func TestRuntime_PEPEnvInjectedAndIORecorded(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Recorded run: InjectEnv carries the PEP env; RecordIO is set.
	fr := &fakeRunner{initSID: "sess-rec"}
	rec := &capturingRecorder{}
	lg := &recordingLaunchGate{dec: LaunchDecision{
		Allowed:   true,
		InjectEnv: []EnvVar{{Name: "OLIVARES_HOOK_PEP_URL", Value: "http://127.0.0.1:8447/"}},
		RecordIO:  true,
	}}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()),
		WithLaunchGate(lg), WithRecorder(rec))
	dto, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		WorkspaceRef: registerTestWorkspace(t, m, tenant, t.TempDir()), Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	if !hasEnv(fr.lastSpec().Env, "OLIVARES_HOOK_PEP_URL", "http://127.0.0.1:8447/") {
		t.Fatal("the governance PEP env must be injected into the launch spec")
	}
	waitFor(t, "I/O frame recorded", func() bool { return rec.count() >= 1 })
	if _, err := m.stopRun(ctx, tenant, dto.RunRef, "user:u1", "user"); err != nil {
		t.Fatalf("stopRun: %v", err)
	}
	waitFor(t, "recorder finalized", func() bool { return rec.didFinalize() })

	// Non-recorded run: same setup but RecordIO false ⇒ the recorder is never offered a frame.
	fr2 := &fakeRunner{initSID: "sess-norec"}
	rec2 := &capturingRecorder{}
	lg2 := &recordingLaunchGate{dec: LaunchDecision{Allowed: true}}
	m2, _, tenant2, _ := newRuntimeHarness(t, WithRunner(fr2), WithCredentialSource(staticCred()),
		WithLaunchGate(lg2), WithRecorder(rec2))
	d2, err := m2.createRun(ctx, tenant2, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		WorkspaceRef: registerTestWorkspace(t, m2, tenant2, t.TempDir()), Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("createRun (norec): %v", err)
	}
	// Let the init frame flow, then confirm nothing was recorded.
	waitFor(t, "session id captured", func() bool {
		d, _ := m2.getRun(ctx, tenant2, d2.RunRef)
		return d.ClaudeSessionID == "sess-norec"
	})
	if rec2.count() != 0 {
		t.Fatalf("a non-recorded run must not offer frames to the recorder (got %d)", rec2.count())
	}
}

func TestRuntime_RejectsInjectedEnvironmentThatCanOverrideRuntimeAuthority(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		env  []EnvVar
	}{
		{"work token", []EnvVar{{Name: "OLIVARES_WORK_TOKEN", Value: "attacker"}}},
		{"work sid", []EnvVar{{Name: "OLIVARES_WORK_SESSION_ID", Value: "attacker"}}},
		{"inference token", []EnvVar{{Name: "ANTHROPIC_AUTH_TOKEN", Value: "attacker"}}},
		{"duplicate", []EnvVar{{Name: "OLIVARES_HOOK_PEP_URL", Value: "one"}, {Name: "OLIVARES_HOOK_PEP_URL", Value: "two"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fr := &fakeRunner{}
			gate := &recordingLaunchGate{dec: LaunchDecision{Allowed: true, InjectEnv: tc.env}}
			m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()), WithLaunchGate(gate))
			_, err := m.createRun(context.Background(), tenant, CreateRunParams{
				Transport: TransportStreamJSON, Isolation: IsolationNative,
				Actor: "user:u1", ActorKind: model.ActorUser,
			})
			if err == nil {
				t.Fatal("runtime-authority environment override launched")
			}
			fr.mu.Lock()
			launched := len(fr.specs)
			fr.mu.Unlock()
			if launched != 0 {
				t.Fatalf("runner received %d launch specs after invalid injection", launched)
			}
		})
	}

	// NO-FIRE control: the managed hook PEP env is legitimate governance
	// configuration and remains reachable.
	if err := validateLaunchInjectedEnv([]EnvVar{{
		Name: "OLIVARES_HOOK_PEP_URL", Value: "http://127.0.0.1:8447/",
	}}); err != nil {
		t.Fatalf("legitimate PEP env rejected: %v", err)
	}
}

func TestRuntime_ContextPolicySummaryRecordedInLaunchLedger(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	const ctxSummary = "ctx max=2048 strategy=summarize scope=agent:agent:a1"
	fr := &fakeRunner{initSID: "sess-ctx"}
	lg := &recordingLaunchGate{dec: LaunchDecision{
		Allowed: true,
		InjectEnv: []EnvVar{
			{Name: "OLIVARES_CONTEXT_MAX_TOKENS", Value: "2048"},
			{Name: "OLIVARES_CONTEXT_STRATEGY", Value: "summarize"},
		},
		ContextPolicySummary: ctxSummary,
	}}
	m, st, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()), WithLaunchGate(lg))
	dto, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		WorkspaceRef: registerTestWorkspace(t, m, tenant, t.TempDir()),
		Actor:        "agent:a1", ActorKind: "agent",
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	if !hasEnv(fr.lastSpec().Env, "OLIVARES_CONTEXT_MAX_TOKENS", "2048") ||
		!hasEnv(fr.lastSpec().Env, "OLIVARES_CONTEXT_STRATEGY", "summarize") {
		t.Fatalf("context policy env was not injected into the launch spec: %+v", fr.lastSpec().Env)
	}
	if dto.PEPProvisioned {
		t.Fatal("context-only env must not mark the PEP hook as provisioned")
	}
	var launchedDetail string
	for _, ev := range listRunEvents(t, st, tenant, dto.RunRef) {
		if ev.Event == "launched" {
			launchedDetail = ev.Detail
			break
		}
	}
	if launchedDetail != ctxSummary {
		t.Fatalf("launched event detail = %q, want %q", launchedDetail, ctxSummary)
	}
}

// TestRuntime_KillSwitchSweepTerminatesRunning proves the ACTIVE half: a session that is
// running when an estate stop engages is TERMINATED by the sweep (not merely blocked at
// the next launch). This is the differentiator — sessions owns the process.
func TestRuntime_KillSwitchSweepTerminatesRunning(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fr := &fakeRunner{initSID: "sess-sweep"}
	gate := &flipStopGate{} // open at launch
	m, st, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()),
		WithStopGate(gate), WithKillSwitchSweep(20*time.Millisecond))
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = m.Stop(context.Background()) })

	dto, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		WorkspaceRef: registerTestWorkspace(t, m, tenant, t.TempDir()), Actor: "agent:a1", ActorKind: "agent",
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	if dto.State != stateRunning {
		t.Fatalf("session should be running, got %s", dto.State)
	}

	// Engage the emergency stop: the running session must be terminated by the sweep.
	gate.flip()
	waitFor(t, "running session terminated by the kill-switch sweep", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.State == stateStopped
	})

	// The termination is attributed to the kill-switch in the lifecycle ledger (system
	// actor, a 'stopping' event before the terminal 'stopped').
	names := eventNames(listRunEvents(t, st, tenant, dto.RunRef))
	if !contains(names, "stopping") || !contains(names, "stopped") {
		t.Fatalf("kill-switch termination should record stopping→stopped, got %v", names)
	}
}

// TestRuntime_GovernanceFactsPersisted proves the per-session governance posture the
// portal renders is persisted on the run from the launch verdict + intent, and surfaced
// on the DTO (create return AND a fresh reload): the agent dimension, whether PEP env was
// provisioned (tool-calls governed in line), whether I/O is recorded, the CRITICAL posture
// and the HITL approval ref. These are references/flags only — never the secrets that
// decided them (minimal-data).
func TestRuntime_GovernanceFactsPersisted(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fr := &fakeRunner{initSID: "sess-gov"}
	lg := &recordingLaunchGate{dec: LaunchDecision{
		Allowed:     true,
		InjectEnv:   []EnvVar{{Name: "OLIVARES_HOOK_PEP_URL", Value: "http://127.0.0.1:8447/"}},
		RecordIO:    true,
		Critical:    true,
		ApprovalRef: "appr-1",
	}}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()),
		WithLaunchGate(lg), WithRecorder(&capturingRecorder{}))
	dto, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		WorkspaceRef: registerTestWorkspace(t, m, tenant, t.TempDir()),
		Actor:        "agent:a1", ActorKind: "agent",
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	assertGov := func(label string, d runDTO) {
		t.Helper()
		if d.AgentRef != "agent:a1" {
			t.Fatalf("%s: agent_ref = %q, want agent:a1", label, d.AgentRef)
		}
		if !d.PEPProvisioned {
			t.Fatalf("%s: pep_provisioned should be true (PEP env injected)", label)
		}
		if !d.RecordIO {
			t.Fatalf("%s: record_io should be true", label)
		}
		if !d.Critical {
			t.Fatalf("%s: critical should be true", label)
		}
		if d.ApprovalRef != "appr-1" {
			t.Fatalf("%s: approval_ref = %q, want appr-1", label, d.ApprovalRef)
		}
	}
	assertGov("create", dto)
	got, err := m.getRun(ctx, tenant, dto.RunRef) // read back fresh, not the create return value
	if err != nil {
		t.Fatalf("getRun: %v", err)
	}
	assertGov("reload", got)
}

// TestRuntime_GovernanceFactsDefault proves the safe defaults: a non-CRITICAL launch with
// no PEP provisioning by a user actor records no agent dimension, PEP NOT provisioned
// (its tool-calls are deny-closed per-tool), no recording, not critical, no approval.
func TestRuntime_GovernanceFactsDefault(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fr := &fakeRunner{initSID: "sess-gov2"}
	lg := &recordingLaunchGate{dec: LaunchDecision{Allowed: true}} // no env, no record, not critical
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()), WithLaunchGate(lg))
	dto, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		WorkspaceRef: registerTestWorkspace(t, m, tenant, t.TempDir()),
		Actor:        "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	if dto.AgentRef != "" || dto.PEPProvisioned || dto.RecordIO || dto.Critical || dto.ApprovalRef != "" {
		t.Fatalf("non-critical/no-PEP run must default governance facts off, got %+v", dto)
	}
}

// TestRuntime_GovernanceFactsRederivedOnResume proves a resume re-derives the posture
// and CLEARS stale references: a run first launched CRITICAL (approval_ref + agent_ref
// set), then resumed under a now-non-critical, PEP-less posture by a user actor, must
// not keep a dangling approval_ref/agent_ref (the panel always shows the CURRENT posture).
func TestRuntime_GovernanceFactsRederivedOnResume(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fr := &fakeRunner{initSID: "sess-resume-gov"}
	lg := &recordingLaunchGate{dec: LaunchDecision{
		Allowed:     true,
		InjectEnv:   []EnvVar{{Name: "OLIVARES_HOOK_PEP_URL", Value: "http://127.0.0.1:8447/"}},
		RecordIO:    true,
		Critical:    true,
		ApprovalRef: "appr-1",
	}}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()),
		WithLaunchGate(lg), WithRecorder(&capturingRecorder{}))
	ws := registerTestWorkspace(t, m, tenant, t.TempDir())
	dto, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, WorkspaceRef: ws,
		Actor: "agent:a1", ActorKind: "agent",
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	if !dto.Critical || dto.ApprovalRef != "appr-1" || dto.AgentRef != "agent:a1" {
		t.Fatalf("create should be CRITICAL with refs set, got %+v", dto)
	}
	// The session id is captured from the init frame; a stream-json resume needs it.
	waitFor(t, "claude session id captured", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.ClaudeSessionID == "sess-resume-gov"
	})
	if _, err := m.stopRun(ctx, tenant, dto.RunRef, "agent:a1", "agent"); err != nil {
		t.Fatalf("stopRun: %v", err)
	}

	// Downgrade the posture: a now NON-critical, non-recorded, PEP-less resume by a USER
	// actor. The re-derivation must flip every flag off AND clear the stale text refs.
	lg.dec = LaunchDecision{Allowed: true}
	resumed, err := m.resumeRun(ctx, tenant, dto.RunRef, "user:u1", "user", "")
	if err != nil {
		t.Fatalf("resumeRun: %v", err)
	}
	if resumed.Critical || resumed.RecordIO || resumed.PEPProvisioned {
		t.Fatalf("resume must re-derive flags off, got %+v", resumed)
	}
	if resumed.ApprovalRef != "" {
		t.Fatalf("resume must CLEAR the stale approval_ref, got %q", resumed.ApprovalRef)
	}
	if resumed.AgentRef != "" {
		t.Fatalf("resume by a user actor must clear agent_ref, got %q", resumed.AgentRef)
	}
	// Persisted (fresh read, not the resume return value).
	got, err := m.getRun(ctx, tenant, dto.RunRef)
	if err != nil {
		t.Fatalf("getRun: %v", err)
	}
	if got.ApprovalRef != "" || got.Critical || got.AgentRef != "" {
		t.Fatalf("stale governance facts persisted after resume: %+v", got)
	}
}
