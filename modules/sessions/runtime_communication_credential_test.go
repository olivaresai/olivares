// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/core/suspension"
)

type dualCredentialProbe struct {
	mu  sync.Mutex
	now func() time.Time

	events         []string
	workMint       []WorkSessionCredentialRequest
	commMint       []CommunicationSessionCredentialRequest
	workRenew      []model.ID
	commRenew      []model.ID
	workRenewReq   []WorkSessionCredentialRequest
	commRenewReq   []CommunicationSessionCredentialRequest
	workRevoke     []model.ID
	commRevoke     []model.ID
	workRevokeReq  []WorkSessionCredentialRequest
	commRevokeReq  []CommunicationSessionCredentialRequest
	workTTL        time.Duration
	commTTL        time.Duration
	workUntil      time.Time
	commUntil      time.Time
	workRenewErr   error
	commRenewErr   error
	workMintHook   func(WorkSessionCredentialRequest)
	workRenewHook  func(WorkSessionCredentialRequest)
	commRenewHook  func(CommunicationSessionCredentialRequest)
	workRevokeErrs []error
	commRevokeErrs []error

	commMintEntered chan struct{}
	commMintRelease <-chan struct{}
}

type dualWorkSource struct{ p *dualCredentialProbe }
type dualCommunicationSource struct{ p *dualCredentialProbe }

func (p *dualCredentialProbe) record(event string) {
	p.mu.Lock()
	p.events = append(p.events, event)
	p.mu.Unlock()
}

func (p *dualCredentialProbe) current() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

func (p *dualCredentialProbe) reset() {
	p.mu.Lock()
	p.events = nil
	p.workMint, p.commMint = nil, nil
	p.workRenew, p.commRenew = nil, nil
	p.workRenewReq, p.commRenewReq = nil, nil
	p.workRevoke, p.commRevoke = nil, nil
	p.workRevokeReq, p.commRevokeReq = nil, nil
	p.mu.Unlock()
}

type dualProbeSnapshot struct {
	events                          []string
	workMint, workRenew, workRevoke int
	commMint, commRenew, commRevoke int
	workMintRequests                []WorkSessionCredentialRequest
	commMintRequests                []CommunicationSessionCredentialRequest
	workRevokeRequests              []WorkSessionCredentialRequest
	commRevokeRequests              []CommunicationSessionCredentialRequest
	workRenewRequests               []WorkSessionCredentialRequest
	commRenewRequests               []CommunicationSessionCredentialRequest
}

func (p *dualCredentialProbe) snapshot() dualProbeSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	return dualProbeSnapshot{
		events:   append([]string(nil), p.events...),
		workMint: len(p.workMint), workRenew: len(p.workRenew), workRevoke: len(p.workRevoke),
		commMint: len(p.commMint), commRenew: len(p.commRenew), commRevoke: len(p.commRevoke),
		workMintRequests:   append([]WorkSessionCredentialRequest(nil), p.workMint...),
		commMintRequests:   append([]CommunicationSessionCredentialRequest(nil), p.commMint...),
		workRevokeRequests: append([]WorkSessionCredentialRequest(nil), p.workRevokeReq...),
		commRevokeRequests: append([]CommunicationSessionCredentialRequest(nil), p.commRevokeReq...),
		workRenewRequests:  append([]WorkSessionCredentialRequest(nil), p.workRenewReq...),
		commRenewRequests:  append([]CommunicationSessionCredentialRequest(nil), p.commRenewReq...),
	}
}

func (s dualWorkSource) Mint(_ context.Context, req WorkSessionCredentialRequest) (WorkSessionCredential, error) {
	s.p.mu.Lock()
	s.p.events = append(s.p.events, "work.mint")
	s.p.workMint = append(s.p.workMint, req)
	ttl, hook := s.p.workTTL, s.p.workMintHook
	s.p.mu.Unlock()
	if hook != nil {
		hook(req)
	}
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	id := model.NewID()
	return WorkSessionCredential{
		ID: id, Token: "work-secret-" + id.String(), Tenant: req.Tenant,
		SessionRef: req.SessionRef, RunRef: req.RunRef, AgentRef: req.AgentRef,
		ClaimFence: req.ClaimFence, NotAfter: s.p.current().Add(ttl),
	}, nil
}

func (s dualWorkSource) Renew(_ context.Context, id model.ID, req WorkSessionCredentialRequest) (time.Time, error) {
	s.p.mu.Lock()
	s.p.events = append(s.p.events, "work.renew")
	s.p.workRenew = append(s.p.workRenew, id)
	s.p.workRenewReq = append(s.p.workRenewReq, req)
	until, err, hook := s.p.workUntil, s.p.workRenewErr, s.p.workRenewHook
	s.p.mu.Unlock()
	if hook != nil {
		hook(req)
	}
	if until.IsZero() {
		until = s.p.current().Add(30 * time.Minute)
	}
	return until, err
}

func (s dualWorkSource) Revoke(_ context.Context, id model.ID, req WorkSessionCredentialRequest) error {
	s.p.mu.Lock()
	defer s.p.mu.Unlock()
	s.p.events = append(s.p.events, "work.revoke")
	s.p.workRevoke = append(s.p.workRevoke, id)
	s.p.workRevokeReq = append(s.p.workRevokeReq, req)
	if len(s.p.workRevokeErrs) == 0 {
		return nil
	}
	err := s.p.workRevokeErrs[0]
	s.p.workRevokeErrs = s.p.workRevokeErrs[1:]
	return err
}

func (s dualCommunicationSource) Mint(_ context.Context, req CommunicationSessionCredentialRequest) (CommunicationSessionCredential, error) {
	s.p.mu.Lock()
	s.p.events = append(s.p.events, "communication.mint")
	s.p.commMint = append(s.p.commMint, req)
	ttl, entered, release := s.p.commTTL, s.p.commMintEntered, s.p.commMintRelease
	s.p.mu.Unlock()
	if entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
	}
	if release != nil {
		<-release
	}
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	id := model.NewID()
	return CommunicationSessionCredential{
		ID: id, Token: "communication-secret-" + id.String(), Tenant: req.Tenant,
		WorkspaceID: req.WorkspaceID, SessionRef: req.SessionRef, RunRef: req.RunRef,
		AgentRef: req.AgentRef, ClaimFence: req.ClaimFence,
		NotAfter: s.p.current().Add(ttl),
	}, nil
}

func (s dualCommunicationSource) Renew(_ context.Context, id model.ID, req CommunicationSessionCredentialRequest) (time.Time, error) {
	s.p.mu.Lock()
	s.p.events = append(s.p.events, "communication.renew")
	s.p.commRenew = append(s.p.commRenew, id)
	s.p.commRenewReq = append(s.p.commRenewReq, req)
	until, err, hook := s.p.commUntil, s.p.commRenewErr, s.p.commRenewHook
	s.p.mu.Unlock()
	if hook != nil {
		hook(req)
	}
	if until.IsZero() {
		until = s.p.current().Add(30 * time.Minute)
	}
	return until, err
}

func (s dualCommunicationSource) Revoke(_ context.Context, id model.ID, req CommunicationSessionCredentialRequest) error {
	s.p.mu.Lock()
	defer s.p.mu.Unlock()
	s.p.events = append(s.p.events, "communication.revoke")
	s.p.commRevoke = append(s.p.commRevoke, id)
	s.p.commRevokeReq = append(s.p.commRevokeReq, req)
	if len(s.p.commRevokeErrs) == 0 {
		return nil
	}
	err := s.p.commRevokeErrs[0]
	s.p.commRevokeErrs = s.p.commRevokeErrs[1:]
	return err
}

func wireDualCredentialProbe(m *Module, p *dualCredentialProbe) {
	m.UseWorkSessionCredentialSource(dualWorkSource{p})
	m.UseCommunicationSessionCredentialSource(dualCommunicationSource{p})
	m.EnableCommunicationSessionCredentials()
}

type hookRunner struct {
	inner *fakeRunner
	hook  func(LaunchSpec) error
}

type secretEchoProcess struct {
	*fakeProc
	mu                   sync.Mutex
	workToken, commToken string
	stopCalls            int
}

func (p *secretEchoProcess) Send(context.Context, []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return fmt.Errorf("send echoed %s and %s", p.workToken, p.commToken)
}

func (p *secretEchoProcess) Stop(ctx context.Context) error {
	p.mu.Lock()
	p.stopCalls++
	call, workToken, commToken := p.stopCalls, p.workToken, p.commToken
	p.mu.Unlock()
	if call == 1 {
		return fmt.Errorf("stop echoed %s and %s", workToken, commToken)
	}
	return p.fakeProc.Stop(ctx)
}

type secretEchoRunner struct{ proc *secretEchoProcess }

func (r *secretEchoRunner) Launch(_ context.Context, spec LaunchSpec) (Process, error) {
	work := envValues(spec.Env, "OLIVARES_WORK_TOKEN")
	communication := envValues(spec.Env, "OLIVARES_COMMUNICATION_TOKEN")
	if len(work) != 1 || len(communication) != 1 {
		return nil, errors.New("runtime token env is incomplete")
	}
	r.proc.mu.Lock()
	r.proc.workToken, r.proc.commToken = work[0], communication[0]
	r.proc.mu.Unlock()
	return r.proc, nil
}

func (r *hookRunner) Launch(ctx context.Context, spec LaunchSpec) (Process, error) {
	if r.hook != nil {
		if err := r.hook(spec); err != nil {
			return nil, err
		}
	}
	return r.inner.Launch(ctx, spec)
}

func envValues(env []EnvVar, name string) []string {
	var values []string
	for _, item := range env {
		if item.Name == name {
			values = append(values, item.Value)
		}
	}
	return values
}

func TestRuntimeDualCredentialsPersistBeforeLaunchAndInjectExactlyOnce(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	innerRunner := &fakeRunner{}
	runner := &hookRunner{inner: innerRunner}
	m, _, tenant, clk := newRuntimeHarness(t, WithRunner(runner), WithCredentialSource(staticCred()))
	probe := &dualCredentialProbe{now: clk.get}
	wireDualCredentialProbe(m, probe)
	checkedBeforeLaunch := false
	runner.hook = func(spec LaunchSpec) error {
		runRefs := envValues(spec.Env, "OLIVARES_WORK_RUN_REF")
		if len(runRefs) != 1 {
			return fmt.Errorf("work run ref env count = %d", len(runRefs))
		}
		record, err := m.loadRun(ctx, tenant, runRefs[0])
		if err != nil {
			return fmt.Errorf("durable run unavailable before Launch: %w", err)
		}
		if record.String(colWorkCredentialID) == "" ||
			record.String(colCommunicationCredentialID) == "" ||
			record.String(colWorkCredentialExpiresAt) == "" ||
			record.String(colCommunicationExpiresAt) == "" ||
			record.String(colRuntimeLaunchID) == "" {
			return fmt.Errorf("dual runtime stamp incomplete before Launch: %v", record)
		}
		if strings.Contains(fmt.Sprint(record), "work-secret-") ||
			strings.Contains(fmt.Sprint(record), "communication-secret-") {
			return errors.New("raw runtime bearer reached durable row")
		}
		checkedBeforeLaunch = true
		return nil
	}

	dto, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "agent:dual", ActorKind: model.ActorAgent, AgentRef: "agent:dual",
	})
	if err != nil {
		t.Fatalf("create dual runtime: %v", err)
	}
	t.Cleanup(func() { _, _ = m.stopRun(ctx, tenant, dto.RunRef, "test", "user") })
	if !checkedBeforeLaunch {
		t.Fatal("Runner.Launch did not observe the durable dual stamp")
	}
	spec := innerRunner.lastSpec()
	workTokens := envValues(spec.Env, "OLIVARES_WORK_TOKEN")
	communicationTokens := envValues(spec.Env, "OLIVARES_COMMUNICATION_TOKEN")
	if len(workTokens) != 1 || len(communicationTokens) != 1 ||
		workTokens[0] == "" || communicationTokens[0] == "" ||
		workTokens[0] == communicationTokens[0] {
		t.Fatalf("runtime token env = work %q communication %q", workTokens, communicationTokens)
	}
	calls := probe.snapshot()
	if len(calls.events) < 2 || calls.events[0] != "communication.mint" || calls.events[1] != "work.mint" {
		t.Fatalf("mint order = %v, want communication then work", calls.events)
	}
}

func TestRuntimeNeverReportsBearerEchoesFromSendOrStop(t *testing.T) {
	t.Parallel()

	proc := &secretEchoProcess{
		fakeProc: &fakeProc{out: make(chan OutputFrame, 1), stopped: make(chan struct{})},
	}
	runner := &secretEchoRunner{proc: proc}
	m, _, tenant, clk := newRuntimeHarness(
		t, WithRunner(runner), WithCredentialSource(staticCred()),
		WithStopWaitDelay(time.Millisecond),
	)
	var logs bytes.Buffer
	m.log = slog.New(slog.NewTextHandler(&logs, nil))
	probe := &dualCredentialProbe{now: clk.get}
	wireDualCredentialProbe(m, probe)
	created, err := m.createRun(context.Background(), tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "agent:redact", ActorKind: model.ActorAgent, AgentRef: "agent:redact",
	})
	if err != nil {
		t.Fatal(err)
	}
	proc.mu.Lock()
	workToken, communicationToken := proc.workToken, proc.commToken
	proc.mu.Unlock()
	if workToken == "" || communicationToken == "" {
		t.Fatal("test runner did not capture both runtime bearers")
	}
	assertNoBearer := func(label string, values ...string) {
		t.Helper()
		for _, value := range values {
			if strings.Contains(value, workToken) || strings.Contains(value, communicationToken) {
				t.Fatalf("%s exposed a runtime bearer: %q", label, value)
			}
		}
	}
	if err := m.sendInput(context.Background(), tenant, created.RunRef, []byte(`{"type":"user"}`)); err == nil {
		t.Fatal("secret-echo Send unexpectedly succeeded")
	} else {
		assertNoBearer("Send error", err.Error(), logs.String())
	}
	_, stopErr := m.stopRun(context.Background(), tenant, created.RunRef, "test", "user")
	if stopErr == nil {
		t.Fatal("secret-echo first Stop unexpectedly succeeded")
	}
	assertNoBearer("Stop error/log", stopErr.Error(), logs.String())
	if _, err := m.stopRun(context.Background(), tenant, created.RunRef, "test", "user"); err != nil {
		t.Fatalf("retry Stop: %v", err)
	}
}

type countingCredentialSource struct {
	mu    sync.Mutex
	calls int
}

func (s *countingCredentialSource) Mint(context.Context, CredentialRequest) (Credential, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	return Credential{ID: model.NewID().String(), Token: "inference", NotAfter: farFuture}, nil
}

func TestRuntimeK3MissingIssuerReturns503BeforeAnyMintOrSpawn(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{}
	inference := &countingCredentialSource{}
	gate := &recordingLaunchGate{dec: LaunchDecision{Allowed: true}}
	m, st, tenant, _ := newRuntimeHarness(
		t, WithRunner(runner), WithCredentialSource(inference), WithLaunchGate(gate),
	)
	m.UseWorkSessionCredentialSource(&workSessionCredentialSpy{})
	m.UseCommunicationSessionCredentialSource(nil)
	m.EnableCommunicationSessionCredentials()
	_, err := m.createRun(context.Background(), tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "agent:missing", ActorKind: model.ActorAgent, AgentRef: "agent:missing",
	})
	var re *runErr
	if !errors.As(err, &re) || re.status != http.StatusServiceUnavailable {
		t.Fatalf("missing communication issuer = %v, want 503", err)
	}
	inference.mu.Lock()
	inferenceCalls := inference.calls
	inference.mu.Unlock()
	runner.mu.Lock()
	spawns := len(runner.specs)
	runner.mu.Unlock()
	if gate.called || inferenceCalls != 0 || spawns != 0 || countRuns(t, st, tenant) != 0 {
		t.Fatalf("partial wiring effects = gate %v inference %d spawn %d rows %d",
			gate.called, inferenceCalls, spawns, countRuns(t, st, tenant))
	}
}

func TestRuntimeCommunicationSourceBindingRemainsOffUntilExplicitEnable(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{}
	m, _, tenant, clk := newRuntimeHarness(
		t, WithRunner(runner), WithCredentialSource(staticCred()),
	)
	probe := &dualCredentialProbe{now: clk.get}
	// Product composition may bind G before E/F/WP2 exists. Binding alone must
	// preserve the legacy work-only launch and skip the promotion ceremony.
	m.UseWorkSessionCredentialSource(dualWorkSource{probe})
	m.UseCommunicationSessionCredentialSource(dualCommunicationSource{probe})
	created, err := m.createRun(context.Background(), tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "agent:staged", ActorKind: model.ActorAgent, AgentRef: "agent:staged",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = m.stopRun(context.Background(), tenant, created.RunRef, "test", "user") })
	calls := probe.snapshot()
	if calls.workMint != 1 || calls.commMint != 0 {
		t.Fatalf("staged binding mints = work %d communication %d", calls.workMint, calls.commMint)
	}
	spec := runner.lastSpec()
	if values := envValues(spec.Env, "OLIVARES_COMMUNICATION_TOKEN"); len(values) != 0 {
		t.Fatalf("staged binding injected communication bearer: %q", values)
	}
	probe.reset()
	if err := m.RecoverRuntimeCredentials(context.Background(), tenant); err != nil {
		t.Fatalf("K3-OFF recovery: %v", err)
	}
	record, err := m.loadRun(context.Background(), tenant, created.RunRef)
	if err != nil {
		t.Fatal(err)
	}
	if record.String(colState) != stateRunning ||
		probe.snapshot().workRevoke != 0 || probe.snapshot().commRevoke != 0 {
		t.Fatalf("K3-OFF recovery touched legacy launch: row %v calls %+v", record, probe.snapshot())
	}
}

func TestRuntimeRejectsCredentialTTLAboveThirtyMinutesAndCompensates(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name                            string
		workTTL, communicationTTL       time.Duration
		wantWorkMint, wantCommMint      int
		wantWorkRevoke, wantCommRevoke  int
		workRevokeErr, communicationErr error
	}{
		{
			name: "communication", communicationTTL: 30*time.Minute + time.Second,
			wantCommMint: 1, wantCommRevoke: 1,
			communicationErr: errors.New("communication revoke unavailable"),
		},
		{
			name: "work", workTTL: 30*time.Minute + time.Second,
			wantWorkMint: 1, wantCommMint: 1, wantWorkRevoke: 1, wantCommRevoke: 1,
			workRevokeErr:    errors.New("work revoke unavailable"),
			communicationErr: errors.New("communication revoke unavailable"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &fakeRunner{}
			m, _, tenant, clk := newRuntimeHarness(
				t, WithRunner(runner), WithCredentialSource(staticCred()),
			)
			probe := &dualCredentialProbe{
				now: clk.get, workTTL: tc.workTTL, commTTL: tc.communicationTTL,
			}
			if tc.workRevokeErr != nil {
				probe.workRevokeErrs = []error{tc.workRevokeErr}
			}
			if tc.communicationErr != nil {
				probe.commRevokeErrs = []error{tc.communicationErr}
			}
			wireDualCredentialProbe(m, probe)
			_, err := m.createRun(context.Background(), tenant, CreateRunParams{
				Transport: TransportStreamJSON, Isolation: IsolationNative,
				Actor: "agent:ttl", ActorKind: model.ActorAgent, AgentRef: "agent:ttl",
			})
			if err == nil {
				t.Fatal("overlong runtime credential was accepted")
			}
			calls := probe.snapshot()
			if calls.workMint != tc.wantWorkMint || calls.commMint != tc.wantCommMint ||
				calls.workRevoke != tc.wantWorkRevoke || calls.commRevoke != tc.wantCommRevoke {
				t.Fatalf("overlong TTL effects = wm=%d cm=%d wr=%d cr=%d, want %d/%d/%d/%d; events %v",
					calls.workMint, calls.commMint, calls.workRevoke, calls.commRevoke,
					tc.wantWorkMint, tc.wantCommMint, tc.wantWorkRevoke, tc.wantCommRevoke,
					calls.events)
			}
			runner.mu.Lock()
			spawns := len(runner.specs)
			runner.mu.Unlock()
			if spawns != 0 {
				t.Fatalf("overlong TTL spawned %d processes", spawns)
			}
		})
	}
}

func TestRuntimeDualMintPersistFailureRevokesBothAndReleasesClaim(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{}
	m, st, tenant, clk := newRuntimeHarness(
		t, WithRunner(runner), WithCredentialSource(staticCred()),
	)
	probe := &dualCredentialProbe{
		now:            clk.get,
		workRevokeErrs: []error{errors.New("work revoke unavailable")},
		commRevokeErrs: []error{errors.New("communication revoke unavailable")},
	}
	wireDualCredentialProbe(m, probe)
	baseData := m.data
	probe.workMintHook = func(WorkSessionCredentialRequest) {
		// Both issuers have committed by the time WORK returns. Fail the very
		// next mutation: persistCreate must compensate the whole pair and give
		// back the Claim without ever exposing a process.
		m.UseData(&failNthMutateData{
			inner: baseData, failAt: 1, err: errors.New("run persistence unavailable"),
		})
	}
	_, err := m.createRun(context.Background(), tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "agent:persist", ActorKind: model.ActorAgent, AgentRef: "agent:persist",
	})
	m.UseData(baseData)
	if err == nil {
		t.Fatal("dual Mint followed by persistence failure unexpectedly launched")
	}
	calls := probe.snapshot()
	if calls.commMint != 1 || calls.workMint != 1 ||
		calls.workRevoke != 1 || calls.commRevoke != 1 {
		t.Fatalf("persist failure compensation = %+v", calls)
	}
	if len(calls.workMintRequests) != 1 {
		t.Fatalf("work Mint requests = %v", calls.workMintRequests)
	}
	_, live, claimErr := m.ActiveClaim(
		context.Background(), tenant, calls.workMintRequests[0].SessionRef,
	)
	if claimErr != nil || live {
		t.Fatalf("persist failure Claim = live %v err %v", live, claimErr)
	}
	runner.mu.Lock()
	spawns := len(runner.specs)
	runner.mu.Unlock()
	if spawns != 0 || countRuns(t, st, tenant) != 0 {
		t.Fatalf("persist failure effects = spawn %d rows %d", spawns, countRuns(t, st, tenant))
	}
}

func TestRuntimeInvalidLaunchInjectionAbortsResumeBeforeMintAndReleasesClaim(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{initSID: "provider-invalid-injection"}
	gate := &recordingLaunchGate{dec: LaunchDecision{Allowed: true}}
	m, _, tenant, clk := newRuntimeHarness(
		t, WithRunner(runner), WithCredentialSource(staticCred()), WithLaunchGate(gate),
	)
	probe := &dualCredentialProbe{now: clk.get}
	wireDualCredentialProbe(m, probe)
	created, err := m.createRun(context.Background(), tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "agent:injection", ActorKind: model.ActorAgent, AgentRef: "agent:injection",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "invalid-injection fixture provider session capture", func() bool {
		dto, getErr := m.getRun(context.Background(), tenant, created.RunRef)
		return getErr == nil && dto.ClaudeSessionID == "provider-invalid-injection"
	})
	if _, err := m.stopRun(context.Background(), tenant, created.RunRef, "test", "user"); err != nil {
		t.Fatal(err)
	}
	probe.reset()
	gate.dec = LaunchDecision{Allowed: true, InjectEnv: []EnvVar{{
		Name: "OLIVARES_COMMUNICATION_TOKEN", Value: "caller-controlled",
	}}}
	_, err = m.resumeRun(
		context.Background(), tenant, created.RunRef,
		"agent:injection", model.ActorAgent, "agent:injection",
	)
	if err == nil {
		t.Fatal("reserved communication environment was accepted on resume")
	}
	calls := probe.snapshot()
	if calls.workMint != 0 || calls.commMint != 0 ||
		calls.workRevoke != 0 || calls.commRevoke != 0 {
		t.Fatalf("pre-Mint resume refusal touched issuers: %+v", calls)
	}
	record, loadErr := m.loadRun(context.Background(), tenant, created.RunRef)
	if loadErr != nil || record.String(colState) != stateStopped ||
		record.String(colRuntimeLaunchID) != "" {
		t.Fatalf("pre-Mint resume refusal left a reservation: %v / %v", record, loadErr)
	}
	_, live, claimErr := m.ActiveClaim(
		context.Background(), tenant, record.String(colRunClaimSID),
	)
	if claimErr != nil || live {
		t.Fatalf("pre-Mint resume refusal Claim = live %v err %v", live, claimErr)
	}
	runner.mu.Lock()
	spawns := len(runner.specs)
	runner.mu.Unlock()
	if spawns != 1 {
		t.Fatalf("invalid resume injection spawned a successor: total launches %d", spawns)
	}
}

type resumeBarrierStopGate struct {
	mu      sync.Mutex
	armed   bool
	arrived int
	ready   chan struct{}
	release chan struct{}
}

func (g *resumeBarrierStopGate) Check(context.Context, model.TenantID, StopDims) (StopDecision, error) {
	g.mu.Lock()
	if !g.armed {
		g.mu.Unlock()
		return StopDecision{}, nil
	}
	g.arrived++
	if g.arrived == 2 {
		close(g.ready)
	}
	release := g.release
	g.mu.Unlock()
	<-release
	return StopDecision{}, nil
}

func (g *resumeBarrierStopGate) arm() {
	g.mu.Lock()
	g.armed, g.arrived = true, 0
	g.ready, g.release = make(chan struct{}), make(chan struct{})
	g.mu.Unlock()
}

func TestRuntimeDualResumeReservationIsCrossModuleAtomic(t *testing.T) {
	// ⛔ SIN t.Parallel(), y su subtest /postgres FALLA IGUAL. Lo segundo es lo importante:
	//    este bloque afirmaba hasta 2026-08-25 que la causa era «una colision de recurso
	//    compartido: `backend.open(t)` abre un backend que los otros 28 paralelos de este mismo
	//    fichero tambien usan», y prescribia «el arreglo BUENO es darle tenant/esquema propio en
	//    `backend.open(t)`». LAS DOS COSAS SON FALSAS, y se refutan con tres hechos:
	//
	//    1. core/internal/pgtest/pgtest.go:898  Suffix(tb) = procTag() + 16 BYTES ALEATORIOS
	//                                    :855  database = "olv_t_" + ese sufijo
	//       => cada llamada a backend.open(t) provisiona su PROPIA base de datos, unica por
	//       LLAMADA. El propio fichero lo dice en :468 ("an isolated database is provisioned per
	//       test") y en :482-483, donde la segunda frase va PARTIDA entre dos lineas de comentario
	//       ("Isolation is the" / "DATABASE's job.") y por eso no casa como cadena literal.
	//       La propiedad que se prescribia
	//       anadir YA EXISTE: implementarla es un no-op.
	//    2. Un test sin t.Parallel() corre en la pasada secuencial CON los paralelos pausados,
	//       asi que nunca solapa con los 28 vecinos a los que se culpaba.
	//    3. Empirico: esta serial en `main` y sigue enrojeciendo.
	//
	//    LO QUE EL FALLO DICE DE VERDAD, que nadie habia abierto:
	//        winner = {RunRef: Name: ... todo VACIO} / version conflict
	//    core/store/errors.go:16 documenta ese centinela como "optimistic-concurrency mismatch
	//    OR unique-key collision" (el :17 es la DECLARACION del valor, no el comentario): UN
	//    texto para DOS causas distintas, asi que el mensaje solo
	//    no permite elegir entre ellas. Y la linea que el log rotula (:706) NO es la asercion:
	//    el helper llama a t.Helper(), de modo que Go atribuye sus dieciocho t.Fatal al sitio de
	//    llamada. La asercion real es `winner.err != nil || winner.dto.State != stateRunning`.
	//
	//    Que se vea SOLO con Postgres y SOLO sin -race sigue siendo cierto y sigue sin absolver
	//    al job que no lleva -race: -race separa en el tiempo lo que sin el coincide.
	//
	//    NO SE PRESCRIBE ARREGLO AQUI. Serializarlo fue provisional y no cierra nada; la causa
	//    esta bajo medicion. Este bloque se deja con lo MEDIDO y sin teoria: la version anterior
	//    tenia formato de hallazgo (citaba corrida, job y paso) y por eso se creyo, se repitio y
	//    se convirtio en encargo para otro. Un comentario que diagnostica es una HIPOTESIS con
	//    formato de conclusion; si vuelve a aparecer una, que traiga la linea que la sostiene.
	for _, backend := range backends(t) {
		backend := backend
		t.Run(backend.name, func(t *testing.T) {
			m1, tenant, clk := backend.open(t)
			testRuntimeDualResumeReservationIsCrossModuleAtomic(t, m1, tenant, clk)
		})
	}
}

func TestRuntimeCommunicationCredentialUsesExplicitIdentityWorkspace(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{initSID: "provider-workspace"}
	m, _, tenant, clk := newRuntimeHarness(
		t, WithRunner(runner), WithCredentialSource(staticCred()),
	)
	probe := &dualCredentialProbe{now: clk.get}
	wireDualCredentialProbe(m, probe)
	created, err := m.createRun(context.Background(), tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "agent:workspace", ActorKind: model.ActorAgent, AgentRef: "agent:workspace",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "workspace fixture provider session capture", func() bool {
		dto, getErr := m.getRun(context.Background(), tenant, created.RunRef)
		return getErr == nil && dto.ClaudeSessionID == "provider-workspace"
	})
	if _, err := m.stopRun(context.Background(), tenant, created.RunRef, "test", "user"); err != nil {
		t.Fatal(err)
	}
	record, err := m.loadRun(context.Background(), tenant, created.RunRef)
	if err != nil {
		t.Fatal(err)
	}
	sid := record.String(colRunClaimSID)
	var defaultWorkspace, explicitWorkspace model.ID
	if err := m.data.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		workspace, err := sc.DefaultWorkspace(context.Background())
		if err != nil {
			return err
		}
		defaultWorkspace = workspace.ID
		createdWorkspace, err := sc.Workspaces().Create(context.Background(), model.Workspace{
			Name: "Explicit", Slug: "explicit", Status: model.StatusActive,
		})
		if err != nil {
			return err
		}
		explicitWorkspace = createdWorkspace.ID
		identity, found, err := findIdentity(context.Background(), sc, sid)
		if err != nil {
			return err
		}
		if !found {
			return errors.New("canonical session identity is absent")
		}
		identity[colIDWorkspaceID] = explicitWorkspace.String()
		repo, err := sc.Ext(identityKind)
		if err != nil {
			return err
		}
		_, err = repo.Update(context.Background(), identity)
		return err
	}); err != nil {
		t.Fatalf("scope canonical identity: %v", err)
	}
	probe.reset()
	resumed, err := m.resumeRun(
		context.Background(), tenant, created.RunRef,
		"agent:workspace", model.ActorAgent, "agent:workspace",
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = m.stopRun(context.Background(), tenant, resumed.RunRef, "test", "user") })
	calls := probe.snapshot()
	if len(calls.commMintRequests) != 1 {
		t.Fatalf("communication mint requests = %v", calls.commMintRequests)
	}
	if calls.commMintRequests[0].WorkspaceID != explicitWorkspace ||
		calls.commMintRequests[0].WorkspaceID == defaultWorkspace {
		t.Fatalf("communication workspace = %s, want explicit %s (default %s)",
			calls.commMintRequests[0].WorkspaceID, explicitWorkspace, defaultWorkspace)
	}
}

func TestRuntimeCallbacksFromOldLaunchCannotMutateSuccessor(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{initSID: "provider-incarnation"}
	m, _, tenant, clk := newRuntimeHarness(
		t, WithRunner(runner), WithCredentialSource(staticCred()),
	)
	probe := &dualCredentialProbe{now: clk.get}
	wireDualCredentialProbe(m, probe)
	created, err := m.createRun(context.Background(), tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "agent:incarnation", ActorKind: model.ActorAgent, AgentRef: "agent:incarnation",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "incarnation fixture provider session capture", func() bool {
		dto, getErr := m.getRun(context.Background(), tenant, created.RunRef)
		return getErr == nil && dto.ClaudeSessionID == "provider-incarnation"
	})
	first, ok := m.rt.getLive(tenant, created.RunRef)
	if !ok {
		t.Fatal("first launch has no live handle")
	}
	old := snapshotLiveRuntimeCredentials(first)
	if _, err := m.stopRun(context.Background(), tenant, created.RunRef, "test", "user"); err != nil {
		t.Fatal(err)
	}
	resumed, err := m.resumeRun(
		context.Background(), tenant, created.RunRef,
		"agent:incarnation", model.ActorAgent, "agent:incarnation",
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = m.stopRun(context.Background(), tenant, resumed.RunRef, "test", "user") })
	before, err := m.loadRun(context.Background(), tenant, created.RunRef)
	if err != nil {
		t.Fatal(err)
	}
	stale := &liveRun{
		tenant: tenant, runRef: created.RunRef, runID: first.runID,
		proc: &fakeProc{out: make(chan OutputFrame), stopped: make(chan struct{})},
		ring: newOutputRing(2, 1024), cancel: func() {}, finalizedCh: make(chan struct{}),
		agentRef: old.agentRef, claim: old.claim, launchID: old.launchID,
		workCredentialID: old.workID, workCredentialNotAfter: old.workUntil,
		communicationWorkspaceID:        old.workspaceID,
		communicationCredentialID:       old.communicationID,
		communicationCredentialNotAfter: old.communicationUntil,
	}
	m.mutateRunBest(context.Background(), stale, func(record model.Record) {
		record[colReason] = "stale callback"
	})
	m.finalize(stale, 137)
	after, err := m.loadRun(context.Background(), tenant, created.RunRef)
	if err != nil {
		t.Fatal(err)
	}
	if after.String(colState) != stateRunning ||
		after.String(colRuntimeLaunchID) != before.String(colRuntimeLaunchID) ||
		after.String(colWorkCredentialID) != before.String(colWorkCredentialID) ||
		after.String(colCommunicationCredentialID) != before.String(colCommunicationCredentialID) ||
		after.String(colReason) == "stale callback" ||
		after.Int(model.ColVersion) != before.Int(model.ColVersion) {
		t.Fatalf("old callback mutated successor: before %v after %v", before, after)
	}
	claim, active, err := m.ActiveClaim(
		context.Background(), tenant, after.String(colRunClaimSID),
	)
	if err != nil || !active || claim.Fence != after.Int(colClaimFence) {
		t.Fatalf("old finalize disturbed successor Claim: %+v active=%v err=%v row=%v",
			claim, active, err, after)
	}
}

func testRuntimeDualResumeReservationIsCrossModuleAtomic(
	t *testing.T,
	m1 *Module,
	tenant model.TenantID,
	clk *testClock,
) {
	t.Helper()
	ctx := context.Background()
	gate := &resumeBarrierStopGate{}
	runner1, runner2 := &fakeRunner{initSID: "provider-race"}, &fakeRunner{}
	m1.rt.runner, m1.rt.creds, m1.rt.stopGate = runner1, staticCred(), gate
	probe := &dualCredentialProbe{now: clk.get}
	wireDualCredentialProbe(m1, probe)
	filesystemWorkspace := registerTestWorkspace(t, m1, tenant, t.TempDir())
	created, err := m1.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "agent:race", ActorKind: model.ActorAgent, AgentRef: "agent:race",
		WorkspaceRef: filesystemWorkspace,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "provider session capture", func() bool {
		dto, getErr := m1.getRun(ctx, tenant, created.RunRef)
		return getErr == nil && dto.ClaudeSessionID == "provider-race"
	})
	if _, err := m1.stopRun(ctx, tenant, created.RunRef, "test", "user"); err != nil {
		t.Fatal(err)
	}
	stoppedRecord, err := m1.loadRun(ctx, tenant, created.RunRef)
	if err != nil {
		t.Fatal(err)
	}
	oldFence := stoppedRecord.Int(colClaimFence)

	m2 := New(WithClock(clk), WithRunner(runner2), WithCredentialSource(staticCred()), WithStopGate(gate))
	m2.UseData(m1.data)
	stopModuleAtCleanup(t, m2)
	wireDualCredentialProbe(m2, probe)
	probe.reset()
	runner1.mu.Lock()
	runner1.specs = nil
	runner1.procs = nil
	runner1.mu.Unlock()
	entered, mintRelease := make(chan struct{}, 1), make(chan struct{})
	probe.mu.Lock()
	probe.commMintEntered, probe.commMintRelease = entered, mintRelease
	probe.mu.Unlock()
	gate.arm()

	type result struct {
		dto runDTO
		err error
	}
	results := make(chan result, 2)
	for _, module := range []*Module{m1, m2} {
		go func(module *Module) {
			dto, err := module.resumeRun(
				ctx, tenant, created.RunRef, "agent:race", model.ActorAgent, "agent:race",
			)
			results <- result{dto: dto, err: err}
		}(module)
	}
	<-gate.ready
	close(gate.release)
	<-entered // winner committed pending+launch_id before its first issuer

	var loser result
	select {
	case loser = <-results:
	case <-time.After(2 * time.Second):
		t.Fatal("CAS loser did not return while winner was blocked in communication mint")
	}
	if !isRunConflict(loser.err) {
		t.Fatalf("CAS loser = %+v / %v, want 409", loser.dto, loser.err)
	}
	calls := probe.snapshot()
	if calls.commMint != 1 || calls.workMint != 0 {
		t.Fatalf("while winner blocked: comm mints=%d work mints=%d", calls.commMint, calls.workMint)
	}
	reserved, err := m1.loadRun(ctx, tenant, created.RunRef)
	if err != nil {
		t.Fatal(err)
	}
	runner1.mu.Lock()
	preSpawns1 := len(runner1.specs)
	runner1.mu.Unlock()
	runner2.mu.Lock()
	preSpawns2 := len(runner2.specs)
	runner2.mu.Unlock()
	if reserved.String(colState) != statePending || reserved.String(colRuntimeLaunchID) == "" ||
		reserved.String(colRunClaimSID) == "" || reserved.String(colClaimHolder) != "agent:race" ||
		reserved.Int(colClaimFence) <= oldFence || reserved.String(colWorkCredentialID) != "" ||
		reserved.String(colCommunicationCredentialID) != "" || preSpawns1+preSpawns2 != 0 {
		t.Fatalf("pre-mint reservation = row %v spawn %d", reserved, preSpawns1+preSpawns2)
	}
	close(mintRelease)
	winner := <-results
	if winner.err != nil || winner.dto.State != stateRunning {
		t.Fatalf("winner = %+v / %v", winner.dto, winner.err)
	}
	calls = probe.snapshot()
	runner1.mu.Lock()
	spawns1 := len(runner1.specs)
	runner1.mu.Unlock()
	runner2.mu.Lock()
	spawns2 := len(runner2.specs)
	runner2.mu.Unlock()
	if calls.commMint != 1 || calls.workMint != 1 || spawns1+spawns2 != 1 {
		t.Fatalf("effects = comm %d work %d spawn %d", calls.commMint, calls.workMint, spawns1+spawns2)
	}
	record, err := m1.loadRun(ctx, tenant, created.RunRef)
	if err != nil {
		t.Fatal(err)
	}
	claim, live, err := m1.ActiveClaim(ctx, tenant, record.String(colRunClaimSID))
	if err != nil || !live || claim.Fence != record.Int(colClaimFence) ||
		record.String(colWorkCredentialID) == "" || record.String(colCommunicationCredentialID) == "" {
		t.Fatalf("winner durable pair/claim = live %v claim %+v row %v err %v", live, claim, record, err)
	}
	if len(calls.workMintRequests) != 1 || len(calls.commMintRequests) != 1 {
		t.Fatalf("mint requests = work %v communication %v", calls.workMintRequests, calls.commMintRequests)
	}
	workReq, commReq := calls.workMintRequests[0], calls.commMintRequests[0]
	if workReq.Tenant != tenant || workReq.SessionRef != record.String(colRunClaimSID) ||
		workReq.RunRef != created.RunRef || workReq.AgentRef != "agent:race" ||
		workReq.ClaimFence != record.Int(colClaimFence) ||
		commReq.Tenant != tenant || commReq.SessionRef != workReq.SessionRef ||
		commReq.RunRef != workReq.RunRef || commReq.AgentRef != workReq.AgentRef ||
		commReq.ClaimFence != workReq.ClaimFence ||
		commReq.WorkspaceID.String() != record.String(colCommunicationWorkspaceID) {
		t.Fatalf("mint tuple diverged from row: work %+v communication %+v row %v", workReq, commReq, record)
	}
	var defaultWorkspace model.ID
	if err := m1.data.View(ctx, tenant, func(sc store.Scope) error {
		workspace, err := sc.DefaultWorkspace(ctx)
		if err == nil {
			defaultWorkspace = workspace.ID
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if commReq.WorkspaceID != defaultWorkspace || commReq.WorkspaceID.String() == filesystemWorkspace {
		t.Fatalf("communication workspace %s came from filesystem ref %q, want identity default %s",
			commReq.WorkspaceID, filesystemWorkspace, defaultWorkspace)
	}
	resumingEvents := 0
	if err := m1.data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runEventKind)
		if err != nil {
			return err
		}
		records, _, err := repo.List(ctx, model.Query{
			Filters: []model.Filter{eq(colEvRunRef, created.RunRef)}, Limit: 100,
		})
		if err != nil {
			return err
		}
		for _, event := range records {
			if event.String(colEvEvent) == "resuming" {
				resumingEvents++
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if resumingEvents != 1 {
		t.Fatalf("resuming events = %d, want exactly one durable reservation", resumingEvents)
	}
	if _, ok := m1.rt.getLive(tenant, created.RunRef); ok {
		_, _ = m1.stopRun(ctx, tenant, created.RunRef, "test", "user")
	} else {
		_, _ = m2.stopRun(ctx, tenant, created.RunRef, "test", "user")
	}
}

func mutateRunForCredentialTest(
	t *testing.T,
	data api.ModuleData,
	tenant model.TenantID,
	runRef string,
	mutate func(model.Record),
) {
	t.Helper()
	if err := data.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		record, err := findRunRec(context.Background(), repo, runRef)
		if err != nil {
			return err
		}
		mutate(record)
		_, err = repo.Update(context.Background(), record)
		return err
	}); err != nil {
		t.Fatalf("mutate run %s: %v", runRef, err)
	}
}

func findOnlyRuntimeCredentialTestRun(
	ctx context.Context,
	repo store.GenericRepo,
) (model.Record, error) {
	records, _, err := repo.List(ctx, model.Query{Limit: 2})
	if err != nil {
		return nil, err
	}
	if len(records) != 1 {
		return nil, fmt.Errorf("runtime run rows = %d, want 1", len(records))
	}
	return records[0], nil
}

func onlyRuntimeCredentialTestRun(
	t *testing.T,
	data api.ModuleData,
	tenant model.TenantID,
) model.Record {
	t.Helper()
	var record model.Record
	if err := data.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		record, err = findOnlyRuntimeCredentialTestRun(context.Background(), repo)
		return err
	}); err != nil {
		t.Fatalf("load only runtime run: %v", err)
	}
	return record
}

func prepareRecoveryReservation(
	t *testing.T,
	m *Module,
	tenant model.TenantID,
	probe *dualCredentialProbe,
	handles int,
	incompleteClaim bool,
) (string, Lease) {
	t.Helper()
	ctx := context.Background()
	created, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "agent:recovery", ActorKind: model.ActorAgent, AgentRef: "agent:recovery",
	})
	if err != nil {
		t.Fatalf("create recovery fixture: %v", err)
	}
	if _, err := m.stopRun(ctx, tenant, created.RunRef, "test", "user"); err != nil {
		t.Fatalf("stop recovery fixture: %v", err)
	}
	m.rt.dropLive(tenant, created.RunRef)
	record, err := m.loadRun(ctx, tenant, created.RunRef)
	if err != nil {
		t.Fatal(err)
	}
	sid := record.String(colRunClaimSID)
	lease, err := m.Claim(ctx, tenant, sid, "agent:recovery", 0)
	if err != nil {
		t.Fatalf("claim recovery fixture: %v", err)
	}
	if incompleteClaim {
		// This models the crash immediately after the durable pending reservation
		// and before admitResume acquired/stamped a new Claim.
		if err := m.Release(ctx, tenant, lease.SID, lease.Holder, lease.Fence); err != nil {
			t.Fatalf("release pre-stamp fixture Claim: %v", err)
		}
	}
	launchID := model.NewID()
	if _, err := m.transition(ctx, tenant, created.RunRef, transitionInput{
		event: "resuming", toState: statePending,
		actor: "agent:recovery", actorKind: model.ActorAgent,
		mutate: func(record model.Record) {
			record[colRuntimeLaunchID] = launchID.String()
		},
	}); err != nil {
		t.Fatalf("reserve recovery fixture: %v", err)
	}
	mutateRunForCredentialTest(t, m.data, tenant, created.RunRef, func(record model.Record) {
		setClaimStamp(record, lease)
		setOrNull(record, colRunClaimSID, lease.SID)
		setOrNull(record, colRunAgentRef, "agent:recovery")
		if incompleteClaim {
			record[colRunClaimSID] = nil
		}
		if handles == 1 || handles == 2 {
			setCredentialHandle(record, colWorkCredentialID, colWorkCredentialExpiresAt,
				model.NewID(), probe.current().Add(20*time.Minute))
		}
		if handles == -1 || handles == 2 {
			setCredentialHandle(record, colCommunicationCredentialID, colCommunicationExpiresAt,
				model.NewID(), probe.current().Add(20*time.Minute))
		}
	})
	probe.reset()
	return created.RunRef, lease
}

func TestRecoverRuntimeCredentialsHandlesZeroOneAndTwoAndReleasesClaim(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name            string
		handles         int
		incompleteClaim bool
		wantWork        int
		wantComm        int
	}{
		{name: "pre-stamp zero handles", handles: 0, incompleteClaim: true},
		{name: "zero handles with exact claim", handles: 0},
		{name: "work only", handles: 1, wantWork: 1},
		{name: "communication only", handles: -1, wantComm: 1},
		{name: "dual handles", handles: 2, wantWork: 1, wantComm: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &fakeRunner{}
			m, _, tenant, clk := newRuntimeHarness(t, WithRunner(runner), WithCredentialSource(staticCred()))
			probe := &dualCredentialProbe{now: clk.get}
			wireDualCredentialProbe(m, probe)
			runRef, lease := prepareRecoveryReservation(t, m, tenant, probe, tc.handles, tc.incompleteClaim)
			before, err := m.loadRun(context.Background(), tenant, runRef)
			if err != nil {
				t.Fatal(err)
			}
			if err := m.RecoverRuntimeCredentials(context.Background(), tenant); err != nil {
				t.Fatalf("recover: %v", err)
			}
			record, err := m.loadRun(context.Background(), tenant, runRef)
			if err != nil {
				t.Fatal(err)
			}
			if record.String(colState) != stateStopped || record.String(colRuntimeLaunchID) != "" ||
				record.String(colWorkCredentialID) != "" || record.String(colCommunicationCredentialID) != "" {
				t.Fatalf("recovered row = %v", record)
			}
			_, live, err := m.ActiveClaim(context.Background(), tenant, lease.SID)
			if err != nil || live {
				t.Fatalf("recovered Claim = live %v err %v", live, err)
			}
			calls := probe.snapshot()
			if calls.workRevoke != tc.wantWork || calls.commRevoke != tc.wantComm {
				t.Fatalf("revokes = work %d comm %d, want %d/%d",
					calls.workRevoke, calls.commRevoke, tc.wantWork, tc.wantComm)
			}
			if before.String(colClaimHolder) != lease.Holder ||
				before.Int(colClaimFence) != lease.Fence {
				t.Fatalf("durable recovery Claim stamp = %v, want %+v", before, lease)
			}
			if tc.wantWork == 1 {
				if len(calls.workRevokeRequests) != 1 {
					t.Fatalf("work revoke requests = %v", calls.workRevokeRequests)
				}
				req := calls.workRevokeRequests[0]
				if req.Tenant != tenant || req.SessionRef != lease.SID || req.RunRef != runRef ||
					req.AgentRef != before.String(colRunAgentRef) || req.ClaimFence != lease.Fence {
					t.Fatalf("work recovery binding = %+v, row %v", req, before)
				}
			}
			if tc.wantComm == 1 {
				if len(calls.commRevokeRequests) != 1 {
					t.Fatalf("communication revoke requests = %v", calls.commRevokeRequests)
				}
				req := calls.commRevokeRequests[0]
				if req.WorkspaceID.IsZero() || req.Tenant != tenant ||
					req.WorkspaceID.String() != before.String(colCommunicationWorkspaceID) ||
					req.SessionRef != lease.SID || req.RunRef != runRef ||
					req.AgentRef != before.String(colRunAgentRef) || req.ClaimFence != lease.Fence {
					t.Fatalf("communication recovery binding = %+v, row %v", req, before)
				}
			}
		})
	}
}

func TestRecoverRuntimeCredentialsPartialRevokeRetainsFailedHandle(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{}
	m, _, tenant, clk := newRuntimeHarness(t, WithRunner(runner), WithCredentialSource(staticCred()))
	probe := &dualCredentialProbe{now: clk.get}
	wireDualCredentialProbe(m, probe)
	runRef, lease := prepareRecoveryReservation(t, m, tenant, probe, 2, false)
	probe.mu.Lock()
	probe.commRevokeErrs = []error{errors.New("communication auth unavailable"), nil}
	probe.mu.Unlock()
	if err := m.RecoverRuntimeCredentials(context.Background(), tenant); err == nil {
		t.Fatal("partial revoke unexpectedly allowed recovery")
	}
	record, err := m.loadRun(context.Background(), tenant, runRef)
	if err != nil {
		t.Fatal(err)
	}
	if record.String(colState) != statePending || record.String(colRuntimeLaunchID) == "" ||
		record.String(colWorkCredentialID) != "" || record.String(colCommunicationCredentialID) == "" {
		t.Fatalf("partial recovery discarded failed handle or terminalized: %v", record)
	}
	_, live, err := m.ActiveClaim(context.Background(), tenant, lease.SID)
	if err != nil || live {
		t.Fatalf("partial recovery left Claim authoritative: live %v err %v", live, err)
	}
	if err := m.RecoverRuntimeCredentials(context.Background(), tenant); err != nil {
		t.Fatalf("retry recovery: %v", err)
	}
	record, _ = m.loadRun(context.Background(), tenant, runRef)
	if record.String(colState) != stateStopped || record.String(colCommunicationCredentialID) != "" {
		t.Fatalf("retry did not settle row: %v", record)
	}
	calls := probe.snapshot()
	if calls.workRevoke != 1 || calls.commRevoke != 2 {
		t.Fatalf("independent retries = work %d comm %d", calls.workRevoke, calls.commRevoke)
	}
}

func TestRecoverRuntimeCredentialsReloadsHandlesPersistedAfterPageSnapshot(t *testing.T) {
	t.Parallel()

	m, _, tenant, clk := newRuntimeHarness(
		t, WithRunner(&fakeRunner{}), WithCredentialSource(staticCred()),
	)
	probe := &dualCredentialProbe{now: clk.get}
	wireDualCredentialProbe(m, probe)
	runRef, lease := prepareRecoveryReservation(t, m, tenant, probe, 0, false)
	var workspaceID model.ID
	if err := m.data.View(context.Background(), tenant, func(sc store.Scope) error {
		workspace, err := sc.DefaultWorkspace(context.Background())
		if err == nil {
			workspaceID = workspace.ID
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	mutateRunForCredentialTest(t, m.data, tenant, runRef, func(record model.Record) {
		record[colCommunicationWorkspaceID] = nil
	})

	base := m.data
	hooked := &afterNthViewData{inner: base, after: 2}
	hooked.hook = func() {
		mutateRunForCredentialTest(t, base, tenant, runRef, func(record model.Record) {
			setRuntimeCredentialStamp(record, lease, runtimeCredentials{
				launchID: model.ID(record.String(colRuntimeLaunchID)),
				work: WorkSessionCredential{
					ID: model.NewID(), Tenant: tenant, SessionRef: lease.SID,
					RunRef: runRef, AgentRef: record.String(colRunAgentRef),
					ClaimFence: lease.Fence, NotAfter: clk.get().Add(20 * time.Minute),
				},
				communication: CommunicationSessionCredential{
					ID: model.NewID(), Tenant: tenant, WorkspaceID: workspaceID,
					SessionRef: lease.SID, RunRef: runRef,
					AgentRef: record.String(colRunAgentRef), ClaimFence: lease.Fence,
					NotAfter: clk.get().Add(20 * time.Minute),
				},
			})
		})
	}
	m.UseRuntimeCredentialRecoveryData(hooked)
	m.UseRuntimeCredentialRecoverySources(dualWorkSource{probe}, dualCommunicationSource{probe})
	if err := m.RecoverRuntimeCredentials(context.Background(), tenant); err != nil {
		t.Fatalf("recover late durable handle: %v", err)
	}
	if !hooked.fired() {
		t.Fatal("late-handle interleaving hook did not fire")
	}
	record, err := m.loadRun(context.Background(), tenant, runRef)
	if err != nil {
		t.Fatal(err)
	}
	if record.String(colState) != stateStopped || record.String(colRuntimeLaunchID) != "" ||
		record.String(colWorkCredentialID) != "" ||
		record.String(colCommunicationCredentialID) != "" {
		t.Fatalf("post-Release reload did not retire late dual stamp: %v", record)
	}
	if calls := probe.snapshot(); calls.workRevoke != 1 || calls.commRevoke != 1 {
		t.Fatalf("late durable dual revokes = work %d communication %d, want 1/1",
			calls.workRevoke, calls.commRevoke)
	}
}

func TestRecoverRuntimeCredentialsRejectsWritesAfterPostReleaseReload(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		lateWrite func(*testing.T, *Module, model.TenantID, string, Lease)
		assert    func(*testing.T, model.Record, Lease)
	}{
		{
			name: "credential handle",
			lateWrite: func(t *testing.T, m *Module, tenant model.TenantID, runRef string, _ Lease) {
				mutateRunForCredentialTest(t, m.data, tenant, runRef, func(record model.Record) {
					setCredentialHandle(record, colWorkCredentialID, colWorkCredentialExpiresAt,
						model.NewID(), time.Now().Add(20*time.Minute))
				})
			},
			assert: func(t *testing.T, record model.Record, _ Lease) {
				if record.String(colWorkCredentialID) == "" {
					t.Fatal("late work credential handle was discarded")
				}
			},
		},
		{
			name: "claim stamp",
			lateWrite: func(t *testing.T, m *Module, tenant model.TenantID, runRef string, old Lease) {
				lease, err := m.Claim(context.Background(), tenant, old.SID, old.Holder, 0)
				if err != nil {
					t.Fatalf("acquire late Claim: %v", err)
				}
				mutateRunForCredentialTest(t, m.data, tenant, runRef, func(record model.Record) {
					setClaimStamp(record, lease)
					setOrNull(record, colRunClaimSID, lease.SID)
				})
			},
			assert: func(t *testing.T, record model.Record, old Lease) {
				if record.String(colRunClaimSID) != old.SID ||
					record.Int(colClaimFence) <= old.Fence {
					t.Fatalf("late Claim stamp was discarded: %v", record)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _, tenant, clk := newRuntimeHarness(
				t, WithRunner(&fakeRunner{}), WithCredentialSource(staticCred()),
			)
			probe := &dualCredentialProbe{now: clk.get}
			wireDualCredentialProbe(m, probe)
			runRef, lease := prepareRecoveryReservation(t, m, tenant, probe, 0, false)

			base := m.data
			hooked := &afterNthViewData{inner: base, after: 3}
			hooked.hook = func() { tc.lateWrite(t, m, tenant, runRef, lease) }
			m.UseRuntimeCredentialRecoveryData(hooked)
			m.UseRuntimeCredentialRecoverySources(
				dualWorkSource{probe}, dualCommunicationSource{probe},
			)
			if err := m.RecoverRuntimeCredentials(context.Background(), tenant); err == nil {
				record, _ := m.loadRun(context.Background(), tenant, runRef)
				t.Fatalf("late generation write unexpectedly allowed terminal recovery: fired=%v row=%v",
					hooked.fired(), record)
			}
			if !hooked.fired() {
				t.Fatal("late-write interleaving hook did not fire")
			}
			record, err := m.loadRun(context.Background(), tenant, runRef)
			if err != nil {
				t.Fatal(err)
			}
			if record.String(colState) != statePending || record.String(colRuntimeLaunchID) == "" {
				t.Fatalf("late generation write was terminalized: %v", record)
			}
			tc.assert(t, record, lease)
		})
	}
}

type retryStopProcess struct {
	*fakeProc
	mu        sync.Mutex
	stopCalls int
	log       *dualCredentialProbe
}

type orderedStopProcess struct {
	*fakeProc
	log *dualCredentialProbe
}

func (p *orderedStopProcess) Stop(ctx context.Context) error {
	p.log.record("process.stop")
	return p.fakeProc.Stop(ctx)
}

func forceRuntimeCredentialExpiry(
	t *testing.T,
	m *Module,
	tenant model.TenantID,
	runRef string,
	until time.Time,
) *liveRun {
	t.Helper()
	lr, ok := m.rt.getLive(tenant, runRef)
	if !ok {
		t.Fatalf("run %s has no live handle", runRef)
	}
	lr.mu.Lock()
	lr.workCredentialNotAfter = until
	lr.communicationCredentialNotAfter = until
	lr.mu.Unlock()
	mutateRunForCredentialTest(t, m.data, tenant, runRef, func(record model.Record) {
		record[colWorkCredentialExpiresAt] = model.NewTimestamp(until).String()
		record[colCommunicationExpiresAt] = model.NewTimestamp(until).String()
	})
	return lr
}

func TestRuntimeDualRenewHeartbeatsClaimBeforeBothAndPersistsDeadlines(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{}
	m, _, tenant, clk := newRuntimeHarness(
		t, WithRunner(runner), WithCredentialSource(staticCred()),
	)
	probe := &dualCredentialProbe{now: clk.get}
	wireDualCredentialProbe(m, probe)
	created, err := m.createRun(context.Background(), tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "agent:renew", ActorKind: model.ActorAgent, AgentRef: "agent:renew",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = m.stopRun(context.Background(), tenant, created.RunRef, "test", "user") })
	record, err := m.loadRun(context.Background(), tenant, created.RunRef)
	if err != nil {
		t.Fatal(err)
	}
	claimBefore, live, err := m.ActiveClaim(
		context.Background(), tenant, record.String(colRunClaimSID),
	)
	if err != nil || !live {
		t.Fatalf("initial Claim = %+v live=%v err=%v", claimBefore, live, err)
	}
	clk.advance(time.Minute)
	lr := forceRuntimeCredentialExpiry(
		t, m, tenant, created.RunRef, clk.get().Add(5*time.Minute),
	)
	claimWasLiveBeforeProvider := false
	probe.mu.Lock()
	probe.workRenewHook = func(req WorkSessionCredentialRequest) {
		claim, active, claimErr := m.ActiveClaim(context.Background(), tenant, req.SessionRef)
		claimWasLiveBeforeProvider = claimErr == nil && active &&
			claim.Fence == req.ClaimFence && claim.ExpiresAt.After(claimBefore.ExpiresAt)
		// Provider time advances after the runtime entered renewal. A validator
		// using a timestamp captured before provider calls rejects this legal +30m.
		clk.advance(time.Millisecond)
	}
	probe.mu.Unlock()
	probe.reset()
	m.renewDualRuntimeCredentials(context.Background(), lr)
	if !claimWasLiveBeforeProvider {
		t.Fatal("work provider ran before the Claim heartbeat committed")
	}
	calls := probe.snapshot()
	if calls.workRenew != 1 || calls.commRenew != 1 ||
		len(calls.workRenewRequests) != 1 || len(calls.commRenewRequests) != 1 {
		t.Fatalf("dual renewal calls = %+v", calls)
	}
	record, err = m.loadRun(context.Background(), tenant, created.RunRef)
	if err != nil {
		t.Fatal(err)
	}
	wantUntil := clk.get().Add(30 * time.Minute)
	workUntil := credentialExpiry(record, colWorkCredentialExpiresAt)
	commUntil := credentialExpiry(record, colCommunicationExpiresAt)
	if !workUntil.Equal(wantUntil) || !commUntil.Equal(wantUntil) {
		t.Fatalf("durable renewed deadlines = work %s communication %s, want %s",
			workUntil, commUntil, wantUntil)
	}
	workReq, commReq := calls.workRenewRequests[0], calls.commRenewRequests[0]
	if workReq.Tenant != tenant || workReq.SessionRef != record.String(colRunClaimSID) ||
		workReq.RunRef != created.RunRef || workReq.AgentRef != "agent:renew" ||
		workReq.ClaimFence != record.Int(colClaimFence) ||
		commReq.Tenant != tenant || commReq.WorkspaceID.String() != record.String(colCommunicationWorkspaceID) ||
		commReq.SessionRef != workReq.SessionRef || commReq.RunRef != workReq.RunRef ||
		commReq.AgentRef != workReq.AgentRef || commReq.ClaimFence != workReq.ClaimFence {
		t.Fatalf("renew binding mismatch: work %+v communication %+v row %v", workReq, commReq, record)
	}
}

func TestRuntimeSilentProcessUsesIndependentCredentialHeartbeat(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{} // emits no Output frames
	m, _, tenant, clk := newRuntimeHarness(
		t, WithRunner(runner), WithCredentialSource(staticCred()),
		WithRuntimeCredentialHeartbeatInterval(5*time.Millisecond),
	)
	probe := &dualCredentialProbe{now: clk.get}
	wireDualCredentialProbe(m, probe)
	created, err := m.createRun(context.Background(), tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "agent:silent", ActorKind: model.ActorAgent, AgentRef: "agent:silent",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = m.stopRun(context.Background(), tenant, created.RunRef, "test", "user") })
	_ = forceRuntimeCredentialExpiry(
		t, m, tenant, created.RunRef, clk.get().Add(5*time.Minute),
	)
	probe.reset()
	waitFor(t, "silent runtime credential heartbeat", func() bool {
		calls := probe.snapshot()
		return calls.workRenew >= 1 && calls.commRenew >= 1
	})
	runner.mu.Lock()
	proc := runner.procs[0]
	runner.mu.Unlock()
	if proc.sentCount() != 0 {
		t.Fatalf("silent heartbeat depended on process I/O: sent=%d", proc.sentCount())
	}
}

func TestRuntimeDualRenewFailureStopsThenRevokesBoth(t *testing.T) {
	t.Parallel()

	probe := &dualCredentialProbe{}
	proc := &orderedStopProcess{
		fakeProc: &fakeProc{out: make(chan OutputFrame, 1), stopped: make(chan struct{})},
		log:      probe,
	}
	m, _, tenant, clk := newRuntimeHarness(
		t, WithRunner(&fixedProcessRunner{proc: proc}), WithCredentialSource(staticCred()),
	)
	probe.now = clk.get
	wireDualCredentialProbe(m, probe)
	created, err := m.createRun(context.Background(), tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "agent:renew-fail", ActorKind: model.ActorAgent, AgentRef: "agent:renew-fail",
	})
	if err != nil {
		t.Fatal(err)
	}
	lr := forceRuntimeCredentialExpiry(
		t, m, tenant, created.RunRef, clk.get().Add(5*time.Minute),
	)
	probe.reset()
	probe.mu.Lock()
	probe.workRenewErr = errors.New("work renewal unavailable")
	probe.mu.Unlock()
	m.renewDualRuntimeCredentials(context.Background(), lr)
	waitFor(t, "dual renewal failure compensation", func() bool {
		calls := probe.snapshot()
		return calls.workRevoke >= 1 && calls.commRevoke >= 1
	})
	calls := probe.snapshot()
	if calls.workRenew != 1 || calls.commRenew != 1 {
		t.Fatalf("renew failure short-circuited pair = work %d communication %d",
			calls.workRenew, calls.commRenew)
	}
	stopAt, firstRevokeAt := -1, -1
	for i, event := range calls.events {
		if event == "process.stop" && stopAt < 0 {
			stopAt = i
		}
		if (event == "work.revoke" || event == "communication.revoke") && firstRevokeAt < 0 {
			firstRevokeAt = i
		}
	}
	if stopAt < 0 || firstRevokeAt < 0 || stopAt > firstRevokeAt {
		t.Fatalf("renew failure compensation order = %v", calls.events)
	}
}

func TestRuntimeDualRenewPersistenceFailureStopsThenRevokesBoth(t *testing.T) {
	t.Parallel()

	probe := &dualCredentialProbe{}
	proc := &orderedStopProcess{
		fakeProc: &fakeProc{out: make(chan OutputFrame, 1), stopped: make(chan struct{})},
		log:      probe,
	}
	m, _, tenant, clk := newRuntimeHarness(
		t, WithRunner(&fixedProcessRunner{proc: proc}), WithCredentialSource(staticCred()),
	)
	probe.now = clk.get
	wireDualCredentialProbe(m, probe)
	created, err := m.createRun(context.Background(), tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "agent:persist-fail", ActorKind: model.ActorAgent, AgentRef: "agent:persist-fail",
	})
	if err != nil {
		t.Fatal(err)
	}
	oldUntil := clk.get().Add(5 * time.Minute)
	lr := forceRuntimeCredentialExpiry(t, m, tenant, created.RunRef, oldUntil)
	m.UseData(&failNthMutateData{
		inner: m.data, failAt: 2, err: errors.New("renewal persistence unavailable"),
	})
	probe.reset()
	m.renewDualRuntimeCredentials(context.Background(), lr)
	waitFor(t, "renewal persistence compensation", func() bool {
		calls := probe.snapshot()
		return calls.workRevoke >= 1 && calls.commRevoke >= 1
	})
	calls := probe.snapshot()
	if calls.workRenew != 1 || calls.commRenew != 1 {
		t.Fatalf("persistence failure renewal calls = work %d communication %d",
			calls.workRenew, calls.commRenew)
	}
	stopAt, firstRevokeAt := -1, -1
	for i, event := range calls.events {
		if event == "process.stop" && stopAt < 0 {
			stopAt = i
		}
		if (event == "work.revoke" || event == "communication.revoke") && firstRevokeAt < 0 {
			firstRevokeAt = i
		}
	}
	if stopAt < 0 || firstRevokeAt < 0 || stopAt > firstRevokeAt {
		t.Fatalf("persistence failure compensation order = %v", calls.events)
	}
}

func TestRuntimeIncompleteLivePairUsesDurableHandlesBeforeStopping(t *testing.T) {
	t.Parallel()

	probe := &dualCredentialProbe{}
	proc := &orderedStopProcess{
		fakeProc: &fakeProc{out: make(chan OutputFrame, 1), stopped: make(chan struct{})},
		log:      probe,
	}
	m, _, tenant, clk := newRuntimeHarness(
		t, WithRunner(&fixedProcessRunner{proc: proc}), WithCredentialSource(staticCred()),
	)
	probe.now = clk.get
	wireDualCredentialProbe(m, probe)
	created, err := m.createRun(context.Background(), tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "agent:partial", ActorKind: model.ActorAgent, AgentRef: "agent:partial",
	})
	if err != nil {
		t.Fatal(err)
	}
	lr, ok := m.rt.getLive(tenant, created.RunRef)
	if !ok {
		t.Fatal("created run is not live")
	}
	lr.mu.Lock()
	lr.communicationCredentialID = ""
	lr.mu.Unlock()
	probe.reset()
	m.renewDualRuntimeCredentials(context.Background(), lr)
	// ⛔ SE ESPERA AL EFECTO QUE SE COMPRUEBA, NO A SU PROXY. Esto esperaba a que el PROBE
	// registrara las llamadas (`workRevoke >= 1 && commRevoke >= 1`) y comprobaba a continuacion un
	// hecho DISTINTO: que el registro DURABLE estuviera limpio. `revokeLiveRuntimeCredentials` corre
	// dentro de un `go func()` (`runtime_communication_credential.go:983`), asi que la escritura
	// durable ocurre DESPUES de la llamada que el probe cuenta — y entre las dos cabe el test.
	//
	// No es teoria: medido bajo `-race`, **1 fallo de 6 corridas**, con el volcado
	// `incomplete live pair left durable authority: map[… claim_fence:1 claim_holder:agent:partial
	// state:running …]`. Sin `-race` pasa siempre, porque no da tiempo a que se note. Es una carrera
	// de TIEMPO, no de datos: las corridas rojas no traen ni un `WARNING: DATA RACE`.
	//
	// Se usa un bucle propio en vez de `waitFor` por una razon concreta: su `Fatalf` dice
	// «timed out waiting for: …» y **pierde el volcado del registro**, que es justo la evidencia que
	// hace util a este testigo — de ese volcado sale saber QUE autoridad quedo viva.
	var record model.Record
	deadline := time.Now().Add(3 * time.Second)
	for {
		calls := probe.snapshot()
		if calls.workRevoke >= 1 && calls.commRevoke >= 1 {
			rec, err := m.loadRun(context.Background(), tenant, created.RunRef)
			if err != nil {
				t.Fatal(err)
			}
			record = rec
			if rec.String(colWorkCredentialID) == "" &&
				rec.String(colCommunicationCredentialID) == "" {
				break
			}
		}
		if !time.Now().Before(deadline) {
			// Los DOS modos de fallo se escriben distinto, y no es cosmetica: si las revocaciones
			// no llegan nunca, `record` sigue vacio y un volcado de `map[]` no dice POR QUE. El
			// contraste lo nombro — «record queda vacio si falta una revocacion»— y sin esto el
			// mensaje confundiria «no se revoco» con «se revoco y quedo autoridad», que mandan a
			// mirar sitios distintos.
			if record == nil {
				calls := probe.snapshot()
				t.Fatalf("compensation never called: workRevoke=%d commRevoke=%d (esperaba >=1 y >=1)",
					calls.workRevoke, calls.commRevoke)
			}
			t.Fatalf("incomplete live pair left durable authority: %v", record)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRuntimeDurationExpiryRevokesBothAfterUnconfirmedStop(t *testing.T) {
	t.Parallel()

	probe := &dualCredentialProbe{}
	proc := &retryStopProcess{
		fakeProc: &fakeProc{out: make(chan OutputFrame, 1), stopped: make(chan struct{})},
		log:      probe,
	}
	m, _, tenant, clk := newRuntimeHarness(
		t, WithRunner(&fixedProcessRunner{proc: proc}), WithCredentialSource(staticCred()),
	)
	probe.now = clk.get
	wireDualCredentialProbe(m, probe)
	created, err := m.createRun(context.Background(), tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "agent:expiry", ActorKind: model.ActorAgent, AgentRef: "agent:expiry",
	})
	if err != nil {
		t.Fatal(err)
	}
	lr, ok := m.rt.getLive(tenant, created.RunRef)
	if !ok {
		t.Fatal("created run is not live")
	}
	probe.reset()
	m.expireRun(lr, time.Hour)
	calls := probe.snapshot()
	if len(calls.events) < 3 || calls.events[0] != "process.stop" ||
		calls.events[1] != "work.revoke" || calls.events[2] != "communication.revoke" ||
		calls.workRevoke != 1 || calls.commRevoke != 1 {
		t.Fatalf("duration failure compensation = %+v", calls)
	}
	record, err := m.loadRun(context.Background(), tenant, created.RunRef)
	if err != nil {
		t.Fatal(err)
	}
	if record.String(colWorkCredentialID) != "" ||
		record.String(colCommunicationCredentialID) != "" {
		t.Fatalf("duration failure left runtime authority durable: %v", record)
	}
	// The first Stop deliberately left Output open. Discharge the retained child
	// so the test also proves the ordinary Stop path can retry it.
	if _, err := m.stopRun(context.Background(), tenant, created.RunRef, "test", "user"); err != nil {
		t.Fatalf("retry duration Stop: %v", err)
	}
}

func TestRuntimeLifecycleRevokesDualCredentialsAtEveryProcessBoundary(t *testing.T) {
	t.Parallel()

	for _, boundary := range []string{"operator stop", "observed exit", "shutdown", "kill switch"} {
		boundary := boundary
		t.Run(boundary, func(t *testing.T) {
			probe := &dualCredentialProbe{}
			proc := &orderedStopProcess{
				fakeProc: &fakeProc{out: make(chan OutputFrame, 1), stopped: make(chan struct{})},
				log:      probe,
			}
			stopGate := &flipStopGate{}
			m, _, tenant, clk := newRuntimeHarness(
				t,
				WithRunner(&fixedProcessRunner{proc: proc}),
				WithCredentialSource(staticCred()),
				WithStopGate(stopGate),
			)
			probe.now = clk.get
			wireDualCredentialProbe(m, probe)
			created, err := m.createRun(context.Background(), tenant, CreateRunParams{
				Transport: TransportStreamJSON, Isolation: IsolationNative,
				Actor: "agent:boundary", ActorKind: model.ActorAgent, AgentRef: "agent:boundary",
			})
			if err != nil {
				t.Fatal(err)
			}
			probe.reset()
			switch boundary {
			case "operator stop":
				_, err = m.stopRun(context.Background(), tenant, created.RunRef, "test", "user")
			case "observed exit":
				proc.finish(0)
			case "shutdown":
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				err = m.stopAllRuns(shutdownCtx)
			case "kill switch":
				stopGate.flip()
				m.sweepKillSwitch(context.Background())
			}
			if err != nil {
				t.Fatalf("%s: %v", boundary, err)
			}
			waitFor(t, boundary+" dual revocation", func() bool {
				calls := probe.snapshot()
				return calls.workRevoke >= 1 && calls.commRevoke >= 1
			})
			waitFor(t, boundary+" durable terminal", func() bool {
				record, loadErr := m.loadRun(context.Background(), tenant, created.RunRef)
				state := record.String(colState)
				return loadErr == nil && (state == stateStopped || state == stateFailed) &&
					record.String(colWorkCredentialID) == "" &&
					record.String(colCommunicationCredentialID) == ""
			})
			calls := probe.snapshot()
			if boundary == "observed exit" {
				if len(calls.events) < 2 || calls.events[0] != "work.revoke" ||
					calls.events[1] != "communication.revoke" {
					t.Fatalf("observed-exit revocation order = %v", calls.events)
				}
				return
			}
			stopAt, revokeAt := -1, -1
			for i, event := range calls.events {
				if event == "process.stop" && stopAt < 0 {
					stopAt = i
				}
				if (event == "work.revoke" || event == "communication.revoke") && revokeAt < 0 {
					revokeAt = i
				}
			}
			if stopAt < 0 || revokeAt < 0 || stopAt > revokeAt {
				t.Fatalf("%s compensation order = %v", boundary, calls.events)
			}
		})
	}
}

func TestRuntimeCleanupRetriesResidualDualCredentialHandlesBeforeCleaned(t *testing.T) {
	t.Parallel()

	m, _, tenant, clk := newRuntimeHarness(
		t, WithRunner(&fakeRunner{}), WithCredentialSource(staticCred()),
	)
	probe := &dualCredentialProbe{now: clk.get}
	wireDualCredentialProbe(m, probe)
	created, err := m.createRun(context.Background(), tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "agent:cleanup", ActorKind: model.ActorAgent, AgentRef: "agent:cleanup",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.stopRun(context.Background(), tenant, created.RunRef, "test", "user"); err != nil {
		t.Fatal(err)
	}
	workID, communicationID := model.NewID(), model.NewID()
	mutateRunForCredentialTest(t, m.data, tenant, created.RunRef, func(record model.Record) {
		setCredentialHandle(record, colWorkCredentialID, colWorkCredentialExpiresAt,
			workID, clk.get().Add(20*time.Minute))
		setCredentialHandle(record, colCommunicationCredentialID, colCommunicationExpiresAt,
			communicationID, clk.get().Add(20*time.Minute))
	})
	probe.reset()
	probe.mu.Lock()
	probe.commRevokeErrs = []error{errors.New("communication revoke unavailable"), nil}
	probe.mu.Unlock()
	if _, err := m.cleanupRun(
		context.Background(), tenant, created.RunRef, "test", "user",
	); err == nil {
		t.Fatal("cleanup published cleaned while one credential revoke failed")
	}
	record, err := m.loadRun(context.Background(), tenant, created.RunRef)
	if err != nil {
		t.Fatal(err)
	}
	if record.String(colState) != stateStopped || record.String(colWorkCredentialID) != "" ||
		record.String(colCommunicationCredentialID) != communicationID.String() {
		t.Fatalf("partial cleanup lost recovery obligation: %v", record)
	}
	if calls := probe.snapshot(); calls.workRevoke != 1 || calls.commRevoke != 1 {
		t.Fatalf("partial cleanup did not attempt both handles: %+v", calls)
	}
	cleaned, err := m.cleanupRun(
		context.Background(), tenant, created.RunRef, "test", "user",
	)
	if err != nil {
		t.Fatalf("cleanup retry: %v", err)
	}
	if cleaned.State != stateCleaned {
		t.Fatalf("cleanup retry state = %s", cleaned.State)
	}
	record, err = m.loadRun(context.Background(), tenant, created.RunRef)
	if err != nil || record.String(colWorkCredentialID) != "" ||
		record.String(colCommunicationCredentialID) != "" {
		t.Fatalf("cleanup retry retained authority: %v / %v", record, err)
	}
	if calls := probe.snapshot(); calls.workRevoke != 1 || calls.commRevoke != 2 {
		t.Fatalf("cleanup retry calls = %+v", calls)
	}
}

func (p *retryStopProcess) Stop(ctx context.Context) error {
	p.log.record("process.stop")
	p.mu.Lock()
	p.stopCalls++
	call := p.stopCalls
	p.mu.Unlock()
	if call == 1 {
		return errors.New("first stop not confirmed")
	}
	return p.fakeProc.Stop(ctx)
}

type fixedProcessRunner struct {
	proc  Process
	mu    sync.Mutex
	specs []LaunchSpec
}

func (r *fixedProcessRunner) Launch(_ context.Context, spec LaunchSpec) (Process, error) {
	r.mu.Lock()
	r.specs = append(r.specs, spec)
	r.mu.Unlock()
	return r.proc, nil
}

type launchBoundaryRunner struct {
	proc Process
	err  error
	hook func()

	mu    sync.Mutex
	specs []LaunchSpec
}

func (r *launchBoundaryRunner) Launch(_ context.Context, spec LaunchSpec) (Process, error) {
	r.mu.Lock()
	r.specs = append(r.specs, spec)
	r.mu.Unlock()
	if r.hook != nil {
		r.hook()
	}
	return r.proc, r.err
}

type recoveryCursorFaultData struct {
	inner api.ModuleData
	mode  string
}

type standbyRuntimeData struct {
	inner  api.ModuleData
	mu     sync.Mutex
	writes int
}

func (d *standbyRuntimeData) View(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	return d.inner.View(ctx, tenant, fn)
}

func (d *standbyRuntimeData) Mutate(
	context.Context,
	model.TenantID,
	func(store.Scope) error,
) error {
	d.mu.Lock()
	d.writes++
	d.mu.Unlock()
	return store.ErrNotLeader
}

func (d *standbyRuntimeData) writeCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.writes
}

func (d recoveryCursorFaultData) View(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	return d.inner.View(ctx, tenant, func(sc store.Scope) error {
		return fn(recoveryCursorFaultScope{Scope: sc, mode: d.mode})
	})
}

func (d recoveryCursorFaultData) Mutate(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	return d.inner.Mutate(ctx, tenant, fn)
}

type recoveryCursorFaultScope struct {
	store.Scope
	mode string
}

func (s recoveryCursorFaultScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	repo, err := s.Scope.Ext(kind)
	if err != nil || kind != runKind {
		return repo, err
	}
	return recoveryCursorFaultRepo{GenericRepo: repo, mode: s.mode}, nil
}

type recoveryCursorFaultRepo struct {
	store.GenericRepo
	mode string
}

func (r recoveryCursorFaultRepo) List(
	ctx context.Context,
	query model.Query,
) ([]model.Record, model.Page, error) {
	if r.mode == "repeat" && query.Cursor != "" {
		return nil, model.Page{HasMore: true, Cursor: "repeat"}, nil
	}
	records, page, err := r.GenericRepo.List(ctx, query)
	if err != nil {
		return nil, model.Page{}, err
	}
	switch r.mode {
	case "empty":
		page.HasMore, page.Cursor = true, ""
	case "repeat":
		page.HasMore, page.Cursor = true, "repeat"
	}
	return records, page, nil
}

func TestRecoverRuntimeCredentialsPaginatesAndRejectsMalformedCursors(t *testing.T) {
	t.Parallel()

	t.Run("all pages", func(t *testing.T) {
		m, _, tenant, clk := newRuntimeHarness(
			t, WithRunner(&fakeRunner{}), WithCredentialSource(staticCred()),
		)
		probe := &dualCredentialProbe{now: clk.get}
		wireDualCredentialProbe(m, probe)
		const rows = 205
		if err := m.data.Mutate(context.Background(), tenant, func(sc store.Scope) error {
			repo, err := sc.Ext(runKind)
			if err != nil {
				return err
			}
			for i := 0; i < rows; i++ {
				runRef, sid := model.NewID().String(), newSID()
				_, err = repo.Create(context.Background(), model.Record{
					colRunRef: runRef, colTransport: string(TransportStreamJSON),
					colPermissionMode: "default", colIsolation: string(IsolationNative),
					colState: stateStopped, colLastEventSeq: int64(0),
					colRunClaimSID: sid, colClaimHolder: "agent:pages", colClaimFence: int64(1),
					colRunAgentRef: "agent:pages", colWorkCredentialID: model.NewID().String(),
					colWorkCredentialExpiresAt: model.NewTimestamp(clk.get().Add(20 * time.Minute)).String(),
				})
				if err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatalf("seed paginated recovery rows: %v", err)
		}
		if err := m.RecoverRuntimeCredentials(context.Background(), tenant); err != nil {
			t.Fatalf("paginated recovery: %v", err)
		}
		if calls := probe.snapshot(); calls.workRevoke != rows {
			t.Fatalf("paginated work revokes = %d, want %d", calls.workRevoke, rows)
		}
	})

	for _, mode := range []string{"empty", "repeat"} {
		t.Run(mode+" cursor", func(t *testing.T) {
			m, _, tenant, clk := newRuntimeHarness(
				t, WithRunner(&fakeRunner{}), WithCredentialSource(staticCred()),
			)
			probe := &dualCredentialProbe{now: clk.get}
			wireDualCredentialProbe(m, probe)
			m.UseRuntimeCredentialRecoveryData(recoveryCursorFaultData{inner: m.data, mode: mode})
			m.UseRuntimeCredentialRecoverySources(
				dualWorkSource{probe}, dualCommunicationSource{probe},
			)
			err := m.RecoverRuntimeCredentials(context.Background(), tenant)
			if err == nil || !strings.Contains(err.Error(), "continuation cursor") {
				t.Fatalf("%s cursor recovery = %v", mode, err)
			}
		})
	}
}

func TestRuntimeStandbyStopDoesNotTouchRemoteK3Generation(t *testing.T) {
	t.Parallel()

	for _, state := range []string{statePending, stateRunning} {
		t.Run(state, func(t *testing.T) {
			m, _, tenant, clk := newRuntimeHarness(
				t, WithRunner(&fakeRunner{}), WithCredentialSource(staticCred()),
			)
			probe := &dualCredentialProbe{now: clk.get}
			wireDualCredentialProbe(m, probe)
			created, err := m.createRun(context.Background(), tenant, CreateRunParams{
				Transport: TransportStreamJSON, Isolation: IsolationNative,
				Actor: "agent:standby", ActorKind: model.ActorAgent, AgentRef: "agent:standby",
			})
			if err != nil {
				t.Fatal(err)
			}
			live, ok := m.rt.getLive(tenant, created.RunRef)
			if !ok {
				t.Fatal("created run is not live")
			}
			base := m.data
			if state == statePending {
				mutateRunForCredentialTest(t, base, tenant, created.RunRef, func(record model.Record) {
					record[colState] = statePending
				})
			}
			before, err := m.loadRun(context.Background(), tenant, created.RunRef)
			if err != nil {
				t.Fatal(err)
			}
			m.rt.dropLive(tenant, created.RunRef)
			standby := &standbyRuntimeData{inner: base}
			m.UseData(standby)
			probe.reset()
			_, err = m.stopRun(context.Background(), tenant, created.RunRef, "test", "user")
			if !isRunConflict(err) {
				t.Fatalf("standby %s Stop = %v, want 409", state, err)
			}
			if standby.writeCount() != 0 {
				t.Fatalf("standby %s Stop attempted %d writes before recovery",
					state, standby.writeCount())
			}
			calls := probe.snapshot()
			if calls.workRevoke != 0 || calls.commRevoke != 0 {
				t.Fatalf("standby %s Stop revoked authority: %+v", state, calls)
			}
			var after model.Record
			if err := base.View(context.Background(), tenant, func(sc store.Scope) error {
				repo, err := sc.Ext(runKind)
				if err != nil {
					return err
				}
				after, err = findRunRec(context.Background(), repo, created.RunRef)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if after.String(colState) != before.String(colState) ||
				after.String(colRuntimeLaunchID) != before.String(colRuntimeLaunchID) ||
				after.String(colWorkCredentialID) != before.String(colWorkCredentialID) ||
				after.String(colCommunicationCredentialID) != before.String(colCommunicationCredentialID) {
				t.Fatalf("standby %s Stop changed durable generation: before %v after %v",
					state, before, after)
			}
			m.UseData(base)
			m.rt.putLive(live)
			if _, err := m.stopRun(context.Background(), tenant, created.RunRef, "test", "user"); err != nil {
				t.Fatalf("cleanup Stop: %v", err)
			}
		})
	}
}

func TestRuntimePostLaunchStopFailureRetainsCustodyUntilRetry(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name                 string
		returnLaunchError    bool
		invalidateTransition bool
	}{
		{name: "process returned with launch error", returnLaunchError: true},
		{name: "post-launch transition refused", invalidateTransition: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probe := &dualCredentialProbe{}
			proc := &retryStopProcess{
				fakeProc: &fakeProc{out: make(chan OutputFrame, 1), stopped: make(chan struct{})},
				log:      probe,
			}
			runner := &launchBoundaryRunner{proc: proc}
			if tc.returnLaunchError {
				runner.err = errors.New("runner reported an uncertain launch")
			}
			m, _, tenant, clk := newRuntimeHarness(
				t, WithRunner(runner), WithCredentialSource(staticCred()),
			)
			probe.now = clk.get
			wireDualCredentialProbe(m, probe)
			if tc.invalidateTransition {
				runner.hook = func() {
					record := onlyRuntimeCredentialTestRun(t, m.data, tenant)
					mutateRunForCredentialTest(t, m.data, tenant,
						record.String(colRunRef), func(current model.Record) {
							current[colState] = stateRunning
						})
				}
			}

			_, err := m.createRun(context.Background(), tenant, CreateRunParams{
				Transport: TransportStreamJSON, Isolation: IsolationNative,
				Actor: "agent:custody", ActorKind: model.ActorAgent, AgentRef: "agent:custody",
			})
			if err == nil {
				t.Fatal("launch boundary unexpectedly succeeded")
			}
			record := onlyRuntimeCredentialTestRun(t, m.data, tenant)
			runRef := record.String(colRunRef)
			if (record.String(colState) != statePending && record.String(colState) != stateRunning) ||
				record.String(colRuntimeLaunchID) == "" {
				t.Fatalf("failed Stop published a terminal generation: %v", record)
			}
			if _, ok := m.rt.getLive(tenant, runRef); !ok {
				t.Fatal("failed Stop discarded the only process handle")
			}
			if calls := probe.snapshot(); calls.workMint != 1 || calls.commMint != 1 ||
				calls.workRevoke != 1 || calls.commRevoke != 1 || len(calls.events) < 3 ||
				calls.events[len(calls.events)-3] != "process.stop" ||
				calls.events[len(calls.events)-2] != "work.revoke" ||
				calls.events[len(calls.events)-1] != "communication.revoke" {
				t.Fatalf("failed Stop compensation order = %v", calls.events)
			}
			if _, resumeErr := m.resumeRun(
				context.Background(), tenant, runRef,
				"agent:custody", model.ActorAgent, "agent:custody",
			); !isRunConflict(resumeErr) {
				t.Fatalf("uncertain child admitted a successor: %v", resumeErr)
			}
			dto, stopErr := m.stopRun(
				context.Background(), tenant, runRef, "test", model.ActorUser,
			)
			if stopErr != nil {
				t.Fatalf("retry Stop: %v", stopErr)
			}
			if dto.State != stateStopped && dto.State != stateFailed {
				t.Fatalf("retry Stop state = %s", dto.State)
			}
			proc.mu.Lock()
			stopCalls := proc.stopCalls
			proc.mu.Unlock()
			if stopCalls != 2 {
				t.Fatalf("process Stop calls = %d, want original attempt + retry", stopCalls)
			}
			record, err = m.loadRun(context.Background(), tenant, runRef)
			if err != nil || record.String(colRuntimeLaunchID) != "" {
				t.Fatalf("confirmed retry did not clear incarnation: %v / %v", record, err)
			}
		})
	}
}

func TestRuntimeResumeProcessReturnedWithErrorRetainsCustodyUntilRetry(t *testing.T) {
	t.Parallel()

	initialRunner := &fakeRunner{initSID: "provider-resume-custody"}
	m, _, tenant, clk := newRuntimeHarness(
		t, WithRunner(initialRunner), WithCredentialSource(staticCred()),
	)
	probe := &dualCredentialProbe{now: clk.get}
	wireDualCredentialProbe(m, probe)
	created, err := m.createRun(context.Background(), tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "agent:resume-custody", ActorKind: model.ActorAgent,
		AgentRef: "agent:resume-custody",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "resume-custody provider session capture", func() bool {
		dto, getErr := m.getRun(context.Background(), tenant, created.RunRef)
		return getErr == nil && dto.ClaudeSessionID == "provider-resume-custody"
	})
	if _, err := m.stopRun(context.Background(), tenant, created.RunRef, "test", "user"); err != nil {
		t.Fatal(err)
	}
	proc := &retryStopProcess{
		fakeProc: &fakeProc{out: make(chan OutputFrame, 1), stopped: make(chan struct{})},
		log:      probe,
	}
	m.rt.runner = &launchBoundaryRunner{
		proc: proc, err: errors.New("runner returned an uncertain resumed process"),
	}
	probe.reset()
	_, err = m.resumeRun(
		context.Background(), tenant, created.RunRef,
		"agent:resume-custody", model.ActorAgent, "agent:resume-custody",
	)
	if err == nil {
		t.Fatal("uncertain resume unexpectedly succeeded")
	}
	record, err := m.loadRun(context.Background(), tenant, created.RunRef)
	if err != nil || record.String(colState) != statePending ||
		record.String(colRuntimeLaunchID) == "" {
		t.Fatalf("uncertain resume discarded generation: %v / %v", record, err)
	}
	if _, ok := m.rt.getLive(tenant, created.RunRef); !ok {
		t.Fatal("uncertain resume discarded process handle")
	}
	calls := probe.snapshot()
	if calls.workMint != 1 || calls.commMint != 1 ||
		calls.workRevoke != 1 || calls.commRevoke != 1 || len(calls.events) < 5 ||
		calls.events[len(calls.events)-3] != "process.stop" ||
		calls.events[len(calls.events)-2] != "work.revoke" ||
		calls.events[len(calls.events)-1] != "communication.revoke" {
		t.Fatalf("uncertain resume compensation = %+v", calls)
	}
	if _, err := m.resumeRun(
		context.Background(), tenant, created.RunRef,
		"agent:resume-custody", model.ActorAgent, "agent:resume-custody",
	); !isRunConflict(err) {
		t.Fatalf("uncertain resume admitted successor: %v", err)
	}
	if _, err := m.stopRun(context.Background(), tenant, created.RunRef, "test", "user"); err != nil {
		t.Fatalf("retry uncertain resumed process Stop: %v", err)
	}
	proc.mu.Lock()
	stopCalls := proc.stopCalls
	proc.mu.Unlock()
	if stopCalls != 2 {
		t.Fatalf("resumed process Stop calls = %d, want 2", stopCalls)
	}
}

func TestRuntimeLaunchErrorWithoutProcessRevokesBothAndReleasesClaim(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{launchErr: errors.New("spawn unavailable")}
	m, _, tenant, clk := newRuntimeHarness(
		t, WithRunner(runner), WithCredentialSource(staticCred()),
	)
	probe := &dualCredentialProbe{now: clk.get}
	wireDualCredentialProbe(m, probe)
	_, err := m.createRun(context.Background(), tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "agent:launch-error", ActorKind: model.ActorAgent, AgentRef: "agent:launch-error",
	})
	if err == nil {
		t.Fatal("launch error unexpectedly succeeded")
	}
	calls := probe.snapshot()
	if calls.workMint != 1 || calls.commMint != 1 ||
		calls.workRevoke != 1 || calls.commRevoke != 1 {
		t.Fatalf("launch error compensation = %+v", calls)
	}
	if len(calls.workMintRequests) != 1 {
		t.Fatalf("launch-error Mint requests = %v", calls.workMintRequests)
	}
	_, live, claimErr := m.ActiveClaim(
		context.Background(), tenant, calls.workMintRequests[0].SessionRef,
	)
	if claimErr != nil || live {
		t.Fatalf("launch-error Claim = live %v err %v", live, claimErr)
	}
	record := onlyRuntimeCredentialTestRun(t, m.data, tenant)
	if record.String(colState) != stateFailed || record.String(colRuntimeLaunchID) != "" ||
		record.String(colWorkCredentialID) != "" ||
		record.String(colCommunicationCredentialID) != "" {
		t.Fatalf("launch-error durable row = %v", record)
	}
	if _, ok := m.rt.getLive(tenant, record.String(colRunRef)); ok {
		t.Fatal("nil-process launch error left a live handle")
	}
}

func TestRecoverRuntimeCredentialsRetriesUnconfirmedLocalStop(t *testing.T) {
	t.Parallel()

	base := &fakeProc{out: make(chan OutputFrame, 1), stopped: make(chan struct{})}
	probe := &dualCredentialProbe{}
	proc := &retryStopProcess{fakeProc: base, log: probe}
	runner := &fixedProcessRunner{proc: proc}
	m, _, tenant, clk := newRuntimeHarness(t, WithRunner(runner), WithCredentialSource(staticCred()))
	probe.now = clk.get
	wireDualCredentialProbe(m, probe)
	created, err := m.createRun(context.Background(), tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "agent:local-recovery", ActorKind: model.ActorAgent, AgentRef: "agent:local-recovery",
	})
	if err != nil {
		t.Fatal(err)
	}
	probe.reset()
	if err := m.RecoverRuntimeCredentials(context.Background(), tenant); err == nil {
		t.Fatal("unconfirmed local Stop allowed promotion recovery")
	}
	if _, ok := m.rt.getLive(tenant, created.RunRef); !ok {
		t.Fatal("failed Stop discarded the live process handle")
	}
	record, err := m.loadRun(context.Background(), tenant, created.RunRef)
	if err != nil || record.String(colState) != stateRunning || record.String(colRuntimeLaunchID) == "" {
		t.Fatalf("failed Stop lost durable incarnation: %v / %v", record, err)
	}
	calls := probe.snapshot()
	if len(calls.events) < 3 || calls.events[0] != "process.stop" ||
		calls.events[1] != "work.revoke" || calls.events[2] != "communication.revoke" {
		t.Fatalf("first recovery order = %v", calls.events)
	}
	probe.reset()
	if err := m.RecoverRuntimeCredentials(context.Background(), tenant); err != nil {
		t.Fatalf("second recovery: %v", err)
	}
	proc.mu.Lock()
	stopCalls := proc.stopCalls
	proc.mu.Unlock()
	if stopCalls != 2 {
		t.Fatalf("Stop calls = %d, want retry of same child", stopCalls)
	}
	if _, ok := m.rt.getLive(tenant, created.RunRef); ok {
		t.Fatal("successfully stopped recovery child remains attachable")
	}
	record, _ = m.loadRun(context.Background(), tenant, created.RunRef)
	if record.String(colState) != stateStopped || record.String(colRuntimeLaunchID) != "" {
		t.Fatalf("second recovery row = %v", record)
	}
}

func TestRecoverRuntimeCredentialsBypassesSuspensionOnlyThroughRecoverySources(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{}
	m, st, tenant, clk := newRuntimeHarness(t, WithRunner(runner), WithCredentialSource(staticCred()))
	normal := &dualCredentialProbe{now: clk.get}
	wireDualCredentialProbe(m, normal)
	runRef, _ := prepareRecoveryReservation(t, m, tenant, normal, 2, false)
	recovery := &dualCredentialProbe{now: clk.get}
	m.UseRuntimeCredentialRecoveryData(api.NewModuleData(st))
	m.UseRuntimeCredentialRecoverySources(dualWorkSource{recovery}, dualCommunicationSource{recovery})
	m.UseData(api.NewModuleData(suspension.Guard(st, nil)))
	if err := st.System(context.Background(), func(sys store.SystemScope) error {
		_, err := sys.SetOrgStatus(context.Background(), tenant, model.StatusSuspended)
		return err
	}); err != nil {
		t.Fatalf("suspend tenant: %v", err)
	}
	if err := m.RecoverRuntimeCredentials(context.Background(), tenant); err != nil {
		t.Fatalf("suspended tenant recovery: %v", err)
	}
	if calls := normal.snapshot(); calls.workRevoke != 0 || calls.commRevoke != 0 {
		t.Fatalf("recovery used suspension-gated ordinary sources: %+v", calls)
	}
	if calls := recovery.snapshot(); calls.workRevoke != 1 || calls.commRevoke != 1 {
		t.Fatalf("custody recovery revokes = %+v", calls)
	}
	var record model.Record
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		record, err = findRunRec(context.Background(), repo, runRef)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if record.String(colState) != stateStopped || record.String(colRuntimeLaunchID) != "" {
		t.Fatalf("suspended recovery row = %v", record)
	}
}
