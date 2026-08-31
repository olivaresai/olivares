// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

type workSessionCredentialSpy struct {
	mu sync.Mutex

	mintCredential WorkSessionCredential
	mintErr        error
	renewedUntil   time.Time
	renewErr       error
	revokeErrs     []error

	mintRequests   []WorkSessionCredentialRequest
	renewIDs       []model.ID
	renewRequests  []WorkSessionCredentialRequest
	revokeIDs      []model.ID
	revokeRequests []WorkSessionCredentialRequest
}

func (s *workSessionCredentialSpy) Mint(
	_ context.Context,
	req WorkSessionCredentialRequest,
) (WorkSessionCredential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mintRequests = append(s.mintRequests, req)
	if s.mintErr != nil {
		return WorkSessionCredential{}, s.mintErr
	}
	cred := s.mintCredential
	if cred.Tenant.IsZero() {
		cred.Tenant = req.Tenant
	}
	if cred.SessionRef == "" {
		cred.SessionRef = req.SessionRef
	}
	if cred.RunRef == "" {
		cred.RunRef = req.RunRef
	}
	if cred.AgentRef == "" {
		cred.AgentRef = req.AgentRef
	}
	if cred.ClaimFence == 0 {
		cred.ClaimFence = req.ClaimFence
	}
	return cred, nil
}

func (s *workSessionCredentialSpy) Renew(
	_ context.Context,
	id model.ID,
	req WorkSessionCredentialRequest,
) (time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.renewIDs = append(s.renewIDs, id)
	s.renewRequests = append(s.renewRequests, req)
	return s.renewedUntil, s.renewErr
}

func (s *workSessionCredentialSpy) Revoke(
	_ context.Context,
	id model.ID,
	req WorkSessionCredentialRequest,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revokeIDs = append(s.revokeIDs, id)
	s.revokeRequests = append(s.revokeRequests, req)
	if len(s.revokeErrs) == 0 {
		return nil
	}
	err := s.revokeErrs[0]
	s.revokeErrs = s.revokeErrs[1:]
	return err
}

func (s *workSessionCredentialSpy) snapshot() workSessionCredentialSpy {
	s.mu.Lock()
	defer s.mu.Unlock()
	return workSessionCredentialSpy{
		mintRequests:   append([]WorkSessionCredentialRequest(nil), s.mintRequests...),
		renewIDs:       append([]model.ID(nil), s.renewIDs...),
		renewRequests:  append([]WorkSessionCredentialRequest(nil), s.renewRequests...),
		revokeIDs:      append([]model.ID(nil), s.revokeIDs...),
		revokeRequests: append([]WorkSessionCredentialRequest(nil), s.revokeRequests...),
	}
}

func TestRuntimeWorkSessionCredentialMintRejectsMismatchedBinding(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	m, _, tenant, clk := newRuntimeHarness(t)
	lease := Lease{SID: "sid-exact", Holder: "agent:driver", Fence: 7}
	wantReq := workSessionCredentialRequest(tenant, "run-exact", "agent:driver", lease)
	spy := &workSessionCredentialSpy{mintCredential: WorkSessionCredential{
		ID: "work-cred-bad", Token: "work-token-bad", SessionRef: "sid-sibling",
		NotAfter: clk.get().Add(30 * time.Minute),
	}}
	m.UseWorkSessionCredentialSource(spy)

	if _, err := m.maybeMintWorkSession(
		ctx, tenant, wantReq.RunRef, wantReq.AgentRef, lease,
	); err == nil {
		t.Fatal("a credential bound to a sibling SID was accepted")
	}
	calls := spy.snapshot()
	if len(calls.revokeIDs) != 1 || calls.revokeIDs[0] != "work-cred-bad" {
		t.Fatalf("invalid credential revocations = %v, want [work-cred-bad]", calls.revokeIDs)
	}
	if len(calls.revokeRequests) != 1 || calls.revokeRequests[0] != wantReq {
		t.Fatalf("revoke binding = %+v, want %+v", calls.revokeRequests, wantReq)
	}

	// Positive control: rejection must be about the mismatched binding, not a source
	// fake that makes every credential unusable.
	spy.mintCredential.SessionRef = wantReq.SessionRef
	got, err := m.maybeMintWorkSession(ctx, tenant, wantReq.RunRef, wantReq.AgentRef, lease)
	if err != nil {
		t.Fatalf("exactly bound credential rejected: %v", err)
	}
	if got.ID != "work-cred-bad" || got.SessionRef != wantReq.SessionRef {
		t.Fatalf("minted credential = %+v", got)
	}
}

func TestRuntimeWorkSessionCredentialRenewsOnlyNearExpiry(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	m, _, tenant, clk := newRuntimeHarness(t)
	lease, err := m.Claim(ctx, tenant, "sid-renew", "agent:driver", 0)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	spy := &workSessionCredentialSpy{renewedUntil: clk.get().Add(30 * time.Minute)}
	m.UseWorkSessionCredentialSource(spy)
	lr := &liveRun{
		tenant: tenant, runRef: "run-renew", agentRef: "agent:driver", claim: lease,
		workCredentialID:       "work-cred-renew",
		workCredentialNotAfter: clk.get().Add(workSessionCredentialRenewWindow + time.Second),
	}

	// No-trigger control: ordinary output heartbeats must not write auth on every
	// frame while the credential still has more than the renewal window remaining.
	m.renewLaunchClaim(ctx, lr)
	if calls := spy.snapshot(); len(calls.renewIDs) != 0 {
		t.Fatalf("far-from-expiry heartbeat renewed credential: %v", calls.renewIDs)
	}

	lr.mu.Lock()
	lr.workCredentialNotAfter = clk.get().Add(workSessionCredentialRenewWindow)
	lr.mu.Unlock()
	m.renewLaunchClaim(ctx, lr)
	calls := spy.snapshot()
	if len(calls.renewIDs) != 1 || calls.renewIDs[0] != "work-cred-renew" {
		t.Fatalf("near-expiry renewals = %v, want [work-cred-renew]", calls.renewIDs)
	}
	wantReq := workSessionCredentialRequest(tenant, lr.runRef, lr.agentRef, lease)
	if len(calls.renewRequests) != 1 || calls.renewRequests[0] != wantReq {
		t.Fatalf("renew binding = %+v, want %+v", calls.renewRequests, wantReq)
	}

	// The successful renewal moves the deadline out. Another heartbeat is a
	// no-trigger control for renewal throttling, not merely for the initial state.
	m.renewLaunchClaim(ctx, lr)
	if calls := spy.snapshot(); len(calls.renewIDs) != 1 {
		t.Fatalf("credential renewed on consecutive heartbeat: %v", calls.renewIDs)
	}
	lr.mu.Lock()
	gotDeadline := lr.workCredentialNotAfter
	lr.mu.Unlock()
	if !gotDeadline.Equal(spy.renewedUntil) {
		t.Fatalf("renewed deadline = %s, want %s", gotDeadline, spy.renewedUntil)
	}
}

func TestRuntimeWorkSessionCredentialDoesNotRenewAfterClaimHeartbeatFails(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	m, _, tenant, clk := newRuntimeHarness(t)
	stale, err := m.Claim(ctx, tenant, "sid-stale-renew", "agent:driver", 0)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	spy := &workSessionCredentialSpy{renewedUntil: clk.get().Add(30 * time.Minute)}
	m.UseWorkSessionCredentialSource(spy)
	lr := &liveRun{
		tenant: tenant, runRef: "run-stale-renew", agentRef: "agent:driver", claim: stale,
		workCredentialID:       "work-cred-stale-renew",
		workCredentialNotAfter: clk.get().Add(workSessionCredentialRenewWindow),
	}
	if err := m.Release(ctx, tenant, stale.SID, stale.Holder, stale.Fence); err != nil {
		t.Fatalf("release old Claim: %v", err)
	}

	// FIRE: a stale Claim cannot extend its purpose credential even when that
	// bearer is inside the renewal window.
	m.renewLaunchClaim(ctx, lr)
	if calls := spy.snapshot(); len(calls.renewIDs) != 0 {
		t.Fatalf("failed Claim heartbeat renewed credentials: %v", calls.renewIDs)
	}

	// NO-FIRE: the same path with the newly active exact fence renews once. This
	// prevents a test that only rewards denying every renewal.
	current, err := m.Claim(ctx, tenant, stale.SID, stale.Holder, 0)
	if err != nil {
		t.Fatalf("reacquire Claim: %v", err)
	}
	lr.claim = current
	m.renewLaunchClaim(ctx, lr)
	if calls := spy.snapshot(); len(calls.renewIDs) != 1 || calls.renewIDs[0] != lr.workCredentialID {
		t.Fatalf("live Claim renewals = %v, want [%s]", calls.renewIDs, lr.workCredentialID)
	}
}

func TestRuntimeWorkSessionCredentialRevokeRetainsHandleUntilSuccess(t *testing.T) {
	t.Parallel()

	m := New()
	spy := &workSessionCredentialSpy{revokeErrs: []error{errors.New("auth store unavailable"), nil}}
	m.UseWorkSessionCredentialSource(spy)
	lr := &liveRun{
		tenant: "tenant-revoke", runRef: "run-revoke", agentRef: "agent:driver",
		claim:            Lease{SID: "sid-revoke", Holder: "agent:driver", Fence: 11},
		workCredentialID: "work-cred-revoke", workCredentialNotAfter: farFuture,
	}

	if err := m.revokeLiveWorkSessionCredential(context.Background(), lr); err == nil {
		t.Fatal("first revoke unexpectedly succeeded")
	}
	lr.mu.Lock()
	afterFailure := lr.workCredentialID
	lr.mu.Unlock()
	if afterFailure != "work-cred-revoke" {
		t.Fatalf("failed revoke discarded retry handle: %q", afterFailure)
	}

	if err := m.revokeLiveWorkSessionCredential(context.Background(), lr); err != nil {
		t.Fatalf("second revoke: %v", err)
	}
	lr.mu.Lock()
	afterSuccess, afterDeadline := lr.workCredentialID, lr.workCredentialNotAfter
	lr.mu.Unlock()
	if !afterSuccess.IsZero() || !afterDeadline.IsZero() {
		t.Fatalf("successful revoke retained handle/deadline: %q / %s", afterSuccess, afterDeadline)
	}
	calls := spy.snapshot()
	if len(calls.revokeIDs) != 2 || calls.revokeIDs[0] != "work-cred-revoke" ||
		calls.revokeIDs[1] != "work-cred-revoke" {
		t.Fatalf("revoke attempts = %v, want same handle twice", calls.revokeIDs)
	}
	if len(calls.revokeRequests) != 2 || calls.revokeRequests[0] != calls.revokeRequests[1] {
		t.Fatalf("revoke binding changed across retry: %+v", calls.revokeRequests)
	}
}

func TestRuntimeWorkSessionCredentialTokenIsLaunchOnly(t *testing.T) {
	t.Parallel()

	const rawToken = "work-secret-never-persist"
	ctx := context.Background()
	runner := &fakeRunner{}
	m, st, tenant, clk := newRuntimeHarness(t, WithRunner(runner), WithCredentialSource(staticCred()))
	spy := &workSessionCredentialSpy{mintCredential: WorkSessionCredential{
		ID: "work-cred-launch", Token: rawToken, NotAfter: clk.get().Add(30 * time.Minute),
	}}
	m.UseWorkSessionCredentialSource(spy)

	dto, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "agent:driver", ActorKind: model.ActorAgent, AgentRef: "agent:driver",
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	t.Cleanup(func() { _, _ = m.stopRun(ctx, tenant, dto.RunRef, "user:cleanup", "user") })
	if !hasEnv(runner.lastSpec().Env, "OLIVARES_WORK_TOKEN", rawToken) {
		t.Fatal("work token did not reach LaunchSpec")
	}

	rec, err := m.loadRun(ctx, tenant, dto.RunRef)
	if err != nil {
		t.Fatalf("load run: %v", err)
	}
	if strings.Contains(fmt.Sprint(rec), rawToken) {
		t.Fatalf("work token persisted in run row: %v", rec)
	}
	for _, event := range listRunEvents(t, st, tenant, dto.RunRef) {
		if strings.Contains(fmt.Sprint(event), rawToken) {
			t.Fatalf("work token persisted in lifecycle ledger: %+v", event)
		}
	}
	if strings.Contains(fmt.Sprint(dto), rawToken) {
		t.Fatalf("work token returned in run DTO: %+v", dto)
	}
}

func TestRuntimeWorkSessionCredentialResumeRotatesResidualClaimBeforeEffects(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	runner := &fakeRunner{initSID: "provider-session-residual-claim"}
	gate := &spyGate{inner: LaunchDecision{Allowed: true}}
	m, _, tenant, clk := newRuntimeHarness(t,
		WithRunner(runner), WithCredentialSource(staticCred()), WithLaunchGate(gate),
	)
	spy := &workSessionCredentialSpy{mintCredential: WorkSessionCredential{
		ID: "work-cred-residual", Token: "work-token-residual",
		NotAfter: clk.get().Add(30 * time.Minute),
	}}
	m.UseWorkSessionCredentialSource(spy)

	created, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "agent:driver", ActorKind: model.ActorAgent, AgentRef: "agent:driver",
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	t.Cleanup(func() {
		_, _ = m.stopRun(ctx, tenant, created.RunRef, "user:cleanup", "user")
	})
	waitFor(t, "provider session id capture", func() bool {
		dto, getErr := m.getRun(ctx, tenant, created.RunRef)
		return getErr == nil && dto.ClaudeSessionID == runner.initSID
	})

	oldLive, ok := m.rt.getLive(tenant, created.RunRef)
	if !ok {
		t.Fatal("created run has no live handle")
	}
	oldLease := oldLive.claim
	if oldLease.SID == "" || oldLease.Fence <= 0 {
		t.Fatalf("created run has no exact claim: %+v", oldLease)
	}

	// Model the failure mode directly: the durable run became terminal, but both
	// release and credential revocation were missed. The same holder/fence therefore
	// remains active when the row first becomes resumable.
	if _, err := m.transition(ctx, tenant, created.RunRef, transitionInput{
		event: "stopped", toState: stateStopped, actor: "runtime", actorKind: "system",
	}); err != nil {
		t.Fatalf("persist residual terminal state: %v", err)
	}
	m.rt.dropLive(tenant, created.RunRef)
	oldLive.mu.Lock()
	oldLive.finalized = true // let the abandoned bridge exit without repairing the fixture
	oldLive.mu.Unlock()
	runner.lastProc().finish(0)

	residual, live, err := m.ActiveClaim(ctx, tenant, oldLease.SID)
	if err != nil || !live {
		t.Fatalf("fixture lost residual claim: live=%v err=%v", live, err)
	}
	if residual.Holder != oldLease.Holder || residual.Fence != oldLease.Fence {
		t.Fatalf("residual claim = %+v, want holder/fence from %+v", residual, oldLease)
	}

	resumed, err := m.resumeRun(
		ctx, tenant, created.RunRef, oldLease.Holder, model.ActorAgent, oldLease.Holder,
	)
	if err != nil {
		t.Fatalf("resume under residual same-holder claim: %v", err)
	}
	if resumed.State != stateRunning {
		t.Fatalf("resumed state = %q, want %q", resumed.State, stateRunning)
	}
	resumeIntent := gate.last(t)
	if resumeIntent.Action != LaunchActionResume || resumeIntent.Fence <= oldLease.Fence {
		t.Fatalf("gate saw action/fence %q/%d, want resume above old fence %d",
			resumeIntent.Action, resumeIntent.Fence, oldLease.Fence)
	}
	calls := spy.snapshot()
	if len(calls.mintRequests) != 2 {
		t.Fatalf("mint requests = %d, want create + resume", len(calls.mintRequests))
	}
	resumeMint := calls.mintRequests[1]
	if resumeMint.ClaimFence != resumeIntent.Fence || resumeMint.ClaimFence <= oldLease.Fence {
		t.Fatalf("resume mint fence = %d, gate=%d old=%d",
			resumeMint.ClaimFence, resumeIntent.Fence, oldLease.Fence)
	}

	current, live, err := m.ActiveClaim(ctx, tenant, oldLease.SID)
	if err != nil || !live {
		t.Fatalf("resume left no current claim: live=%v err=%v", live, err)
	}
	if current.Fence != resumeIntent.Fence || current.Fence <= oldLease.Fence {
		t.Fatalf("current fence = %d, gate=%d old=%d",
			current.Fence, resumeIntent.Fence, oldLease.Fence)
	}
	if _, err := m.Heartbeat(
		ctx, tenant, oldLease.SID, oldLease.Holder, oldLease.Fence, 0,
	); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("old fence retained authority after resume: %v", err)
	}
	runner.mu.Lock()
	launchesAfterResidual := len(runner.specs)
	runner.mu.Unlock()
	if launchesAfterResidual != 2 {
		t.Fatalf("launches after residual resume = %d, want 2", launchesAfterResidual)
	}

	// Positive control: the ordinary path releases during stop and remains resumable.
	// This catches an implementation that solves the stale generation by rejecting
	// every resume instead of rotating only the authority generation.
	if _, err := m.stopRun(ctx, tenant, created.RunRef, oldLease.Holder, model.ActorAgent); err != nil {
		t.Fatalf("normal stop before control resume: %v", err)
	}
	control, err := m.resumeRun(
		ctx, tenant, created.RunRef, oldLease.Holder, model.ActorAgent, oldLease.Holder,
	)
	if err != nil {
		t.Fatalf("ordinary resume control: %v", err)
	}
	if control.State != stateRunning {
		t.Fatalf("ordinary resume state = %q, want %q", control.State, stateRunning)
	}
	runner.mu.Lock()
	launchesAfterControl := len(runner.specs)
	runner.mu.Unlock()
	if launchesAfterControl != 3 {
		t.Fatalf("launches after ordinary resume = %d, want 3", launchesAfterControl)
	}
}
