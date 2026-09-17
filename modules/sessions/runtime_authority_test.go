// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// The DURABLE-AUTHORITY regressions, through the real store, the native Runner
// and a real owned child.
//
// They are the independent review's discriminators kept permanent. Where the
// typed fix changed a signature the adaptation is recorded on the test itself;
// everything else is the reviewer's shape, because a probe rewritten by the
// author it caught is no longer a probe.

type blockingApprovalGate struct {
	entered chan ProviderApprovalRequest
	release chan struct{}
}

func (g blockingApprovalGate) Approve(_ context.Context, _ model.TenantID, req ProviderApprovalRequest) (ProviderApprovalDecision, error) {
	g.entered <- req
	<-g.release
	return ProviderApprovalDecision{Allow: true}, nil
}

type countingAllowGate struct{ calls atomic.Int32 }

func (g *countingAllowGate) Approve(context.Context, model.TenantID, ProviderApprovalRequest) (ProviderApprovalDecision, error) {
	g.calls.Add(1)
	return ProviderApprovalDecision{Allow: true}, nil
}

// takeOverClaim releases the launch's claim and acquires a strictly newer one for
// somebody else, which is what a real successor incarnation does.
func takeOverClaim(t *testing.T, m *Module, tenant model.TenantID, lr *liveRun, holder string) Lease {
	t.Helper()
	ctx := context.Background()
	if err := m.Release(ctx, tenant, lr.claim.SID, lr.claim.Holder, lr.claim.Fence); err != nil {
		t.Fatalf("release the original claim: %v", err)
	}
	successor, err := m.Claim(ctx, tenant, lr.claim.SID, holder, time.Minute)
	if err != nil || successor.Fence <= lr.claim.Fence {
		t.Fatalf("successor claim = %#v, %v", successor, err)
	}
	return successor
}

// reapOwnedChild ends the child directly, WITHOUT going through the operator
// stop. Once a test has moved the claim, the operator path correctly refuses —
// and the runtime must still be able to reap what it owns. That asymmetry is the
// behaviour under test, so the cleanup has to respect it.
func reapOwnedChild(t *testing.T, lr *liveRun) {
	t.Helper()
	t.Cleanup(func() {
		_ = lr.proc.Stop(context.Background())
		select {
		case <-lr.finalizedCh:
		case <-time.After(5 * time.Second):
		}
	})
}

// A delayed approval whose launch lost the durable Claim must be cancelled before
// its answer crosses the child-process boundary. The v2 request carries its own
// exact turn, so this isolates the OTHER required scope.
func TestCodexRuntimeApprovalRefusesAfterClaimTakeover(t *testing.T) {
	gate := blockingApprovalGate{entered: make(chan ProviderApprovalRequest, 1), release: make(chan struct{})}
	m, _, tenant, prof := codexHarness(t, AuthSourceAccountHome, WithProviderApprovalGate(gate))
	record := setCodexFixture(t, prof, codexFixture{ThreadID: "thread-approval-claim", Account: "apikey"})
	dto, err := codexLaunch(t, m, tenant, prof)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	lr, _ := m.rt.getLive(tenant, dto.RunRef)
	reapOwnedChild(t, lr)
	if err := m.sendTextInput(context.Background(), tenant, dto.RunRef, "request approval"); err != nil {
		t.Fatalf("start turn: %v", err)
	}
	raw, err := json.Marshal(map[string]any{
		"id": "review-approval", "method": codexReqCommandApproval,
		"params": map[string]any{
			"itemId": "item-1", "startedAtMs": 1,
			"threadId": "thread-approval-claim", "turnId": "turn-1",
		},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	lr.session.Deliver(OutputFrame{Stream: streamStdout, Data: raw})
	select {
	case req := <-gate.entered:
		if req.TurnID != "turn-1" {
			t.Fatalf("v2 approval TurnID = %q, want turn-1", req.TurnID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the approval authority was never reached")
	}
	successor := takeOverClaim(t, m, tenant, lr, "review-approval-successor")
	close(gate.release)

	var decision string
	waitFor(t, "the child records the delayed approval reply", func() bool {
		rec := readFixtureRecord(t, record)
		if len(rec.Replies) == 0 {
			return false
		}
		var reply struct {
			Decision string `json:"decision"`
		}
		if json.Unmarshal(rec.Replies[0], &reply) != nil {
			return false
		}
		decision = reply.Decision
		return true
	})
	if decision != "cancel" {
		t.Fatalf("approval after the claim moved from fence %d to %d = %q, want cancel",
			lr.claim.Fence, successor.Fence, decision)
	}
}

// The peer is entitled to send a request immediately after its correlated
// turn/start response. Processing the next stdout line before the caller
// goroutine records that response must not turn a valid same-turn request into a
// stale one — and must not be repaired by sleeping, either.
func TestCodexRuntimeImmediateV2ApprovalBelongsToTheStartedTurn(t *testing.T) {
	gate := &countingAllowGate{}
	m, _, tenant, prof := codexHarness(t, AuthSourceAccountHome, WithProviderApprovalGate(gate))
	record := setCodexFixture(t, prof, codexFixture{
		ThreadID: "thread-immediate-approval", Account: "apikey",
		ApprovalOnTurn: codexReqCommandApproval,
	})
	dto, err := codexLaunch(t, m, tenant, prof)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	t.Cleanup(func() { _, _ = m.stopRun(context.Background(), tenant, dto.RunRef, "review", model.ActorUser) })
	if err := m.sendTextInput(context.Background(), tenant, dto.RunRef, "run command"); err != nil {
		t.Fatalf("turn/start: %v", err)
	}
	var decision string
	waitFor(t, "the child records the immediate approval reply", func() bool {
		rec := readFixtureRecord(t, record)
		if len(rec.Replies) == 0 {
			return false
		}
		var reply struct {
			Decision string `json:"decision"`
		}
		if json.Unmarshal(rec.Replies[0], &reply) != nil {
			return false
		}
		decision = reply.Decision
		return true
	})
	if got := gate.calls.Load(); got != 1 {
		t.Errorf("approval authority calls = %d, want 1 for a valid turn-1 request", got)
	}
	if decision != "accept" {
		t.Errorf("immediate turn-1 approval reply = %q, want accept", decision)
	}
}

// Once a successor owns the durable Claim, the superseded launch must not write a
// turn, interrupt one, or end the process through its stale live handle.
func TestCodexRuntimeControlsRefuseAfterClaimTakeover(t *testing.T) {
	m, _, tenant, prof := codexHarness(t, AuthSourceAccountHome)
	record := setCodexFixture(t, prof, codexFixture{ThreadID: "thread-claim-fence", Account: "apikey"})
	ctx := context.Background()
	dto, err := codexLaunch(t, m, tenant, prof)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	lr, ok := m.rt.getLive(tenant, dto.RunRef)
	if !ok {
		t.Fatal("the owned child is not registered")
	}
	reapOwnedChild(t, lr)
	successor := takeOverClaim(t, m, tenant, lr, "review-successor")

	before := readFixtureRecord(t, record)
	beforeStarts := countMethod(before.Methods, codexMethodTurnStart)
	inputErr := m.sendTextInput(ctx, tenant, dto.RunRef, "must be fenced")
	afterInput := readFixtureRecord(t, record)
	if inputErr == nil {
		t.Errorf("input succeeded after the claim moved from fence %d to %d", lr.claim.Fence, successor.Fence)
	}
	if got := countMethod(afterInput.Methods, codexMethodTurnStart); got != beforeStarts {
		t.Errorf("a stale input crossed the process boundary: turn/start count %d -> %d", beforeStarts, got)
	}

	beforeInterrupts := countMethod(afterInput.Methods, codexMethodTurnInterrupt)
	interrupted, interruptErr := m.interruptRun(ctx, tenant, dto.RunRef, "user:u1", model.ActorUser)
	afterInterrupt := readFixtureRecord(t, record)
	if interruptErr == nil {
		t.Errorf("interrupt succeeded after the claim takeover; state = %q", interrupted.State)
	}
	if got := countMethod(afterInterrupt.Methods, codexMethodTurnInterrupt); got != beforeInterrupts {
		t.Errorf("a stale interrupt crossed the process boundary: turn/interrupt count %d -> %d", beforeInterrupts, got)
	}

	stopped, stopErr := m.stopRun(ctx, tenant, dto.RunRef, "user:u1", model.ActorUser)
	if stopErr == nil {
		t.Errorf("a terminal stop succeeded after the claim takeover; state = %q", stopped.State)
	}
	if !processRunning(int(*dto.PID)) {
		t.Error("a stale stop ended the owned process after its claim had moved")
	}
}

// The refusal above is an OPERATOR refusal, and it must not become an inability
// to reap. The runtime's own teardown of a child it owns is a different power and
// still works once the claim has moved.
func TestCodexRuntimeStillReapsItsOwnChildAfterClaimTakeover(t *testing.T) {
	m, _, tenant, prof := codexHarness(t, AuthSourceAccountHome)
	setCodexFixture(t, prof, codexFixture{ThreadID: "thread-reap", Account: "apikey", SpawnChild: true})
	dto, err := codexLaunch(t, m, tenant, prof)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	lr, _ := m.rt.getLive(tenant, dto.RunRef)
	takeOverClaim(t, m, tenant, lr, "review-reaper")
	if _, err := m.stopRun(context.Background(), tenant, dto.RunRef, "user:u1", model.ActorUser); err == nil {
		t.Fatal("the operator stop must refuse once the claim has moved")
	}
	// The runtime's own shutdown owes nothing to a holder: it must still end the
	// process group it created.
	stopCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := m.Stop(stopCtx); err != nil {
		t.Fatalf("runtime shutdown: %v", err)
	}
	waitFor(t, "the runtime reaped its own child", func() bool { return !processRunning(int(*dto.PID)) })
}

// A work-bound Codex run must have exactly one text route, and it must carry the
// exact WorkLease fence.
//
// ADAPTATION, recorded: the reviewer's probe asserted the DEFECT — that both
// candidate routes refused the same run. With the typed port in place the same
// setup asserts the contract instead: the fenced text route reaches the child,
// and a stale fence does not.
func TestCodexRuntimeWorkBoundRunHasFencedTextInput(t *testing.T) {
	m, st, tenant, prof := codexHarness(t, AuthSourceAccountHome,
		WithWorkIdentityResolver(allowWorkIdentity{}),
		WithWorkContentGuard(allowWorkContent{}),
	)
	itemID, _, agentRef := readyWorkLaunchItem(t, m, st, tenant)
	m.UseWorkIdentityResolver(durableWorkLaunchIdentity{m: m, st: st})
	record := setCodexFixture(t, prof, codexFixture{ThreadID: "thread-work-bound", Account: "apikey"})
	spec := workLaunchSpec(itemID, agentRef)
	spec.Runtime.ProviderProfileRef = prof.Ref

	managed, err := m.LaunchForWork(context.Background(), tenant, spec)
	if err != nil {
		t.Fatalf("LaunchForWork Codex: %v", err)
	}
	t.Cleanup(func() {
		_ = m.StopForWork(context.Background(), tenant, managed.RunRef, managed.WorkLeaseFence, "review cleanup")
	})

	// The unfenced text route stays refused: a durable work stamp selects the
	// fenced control plane for the life of the run.
	if err := m.sendTextInput(context.Background(), tenant, managed.RunRef, "unfenced"); err == nil {
		t.Fatal("the unfenced text route must refuse a work-bound run")
	}
	// A raw line is still refused for a driver run, whatever fence carries it: an
	// owned RPC peer never reads an operator's bytes as a frame.
	if err := m.InputForWork(context.Background(), tenant, managed.RunRef, managed.WorkLeaseFence, []byte("raw")); err == nil {
		t.Fatal("a raw fenced line must stay refused on a driver-backed run")
	}
	// A stale fence refuses BEFORE the effect.
	before := countMethod(readFixtureRecord(t, record).Methods, codexMethodTurnStart)
	if err := m.TextForWork(context.Background(), tenant, managed.RunRef, managed.WorkLeaseFence+1, "stale"); err == nil {
		t.Fatal("a stale work fence must refuse the text route")
	}
	if got := countMethod(readFixtureRecord(t, record).Methods, codexMethodTurnStart); got != before {
		t.Fatalf("a stale fence crossed the process boundary: turn/start %d -> %d", before, got)
	}
	// And the exact fence reaches the child as a TURN.
	if err := m.TextForWork(context.Background(), tenant, managed.RunRef, managed.WorkLeaseFence, "hello"); err != nil {
		t.Fatalf("the exact work fence must carry text to the driver: %v", err)
	}
	waitFor(t, "the fenced text started a turn on the child", func() bool {
		return countMethod(readFixtureRecord(t, record).Methods, codexMethodTurnStart) == before+1
	})
}

// The durable authority boundary under CONCURRENCY, not sequential reads.
//
// The takeover and the input race deliberately. The claim row is in the write set
// of the transaction that authorises the input, so the store serialises them, and
// exactly one of two outcomes is legal every time: the input was authorised and
// the frame reached the child, or it was refused and nothing reached it. A
// refusal with a frame on the wire — or a success with none — would be the
// authority boundary leaking, and neither may ever be observed.
func TestCodexRuntimeClaimTakeoverRacesInputAtTheDurableBoundary(t *testing.T) {
	const rounds = 10
	for round := 0; round < rounds; round++ {
		m, _, tenant, prof := codexHarness(t, AuthSourceAccountHome)
		record := setCodexFixture(t, prof, codexFixture{ThreadID: "thread-race", Account: "apikey"})
		dto, err := codexLaunch(t, m, tenant, prof)
		if err != nil {
			t.Fatalf("round %d launch: %v", round, err)
		}
		lr, _ := m.rt.getLive(tenant, dto.RunRef)
		before := countMethod(readFixtureRecord(t, record).Methods, codexMethodTurnStart)

		var wg sync.WaitGroup
		start := make(chan struct{})
		var inputErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			inputErr = m.sendTextInput(context.Background(), tenant, dto.RunRef, "raced")
		}()
		go func() {
			defer wg.Done()
			<-start
			ctx := context.Background()
			if err := m.Release(ctx, tenant, lr.claim.SID, lr.claim.Holder, lr.claim.Fence); err != nil {
				return // the input won the row; that outcome is legal and is asserted below
			}
			_, _ = m.Claim(ctx, tenant, lr.claim.SID, "race-successor", time.Minute)
		}()
		close(start)
		wg.Wait()

		after := countMethod(readFixtureRecord(t, record).Methods, codexMethodTurnStart)
		crossed := after > before
		switch {
		case inputErr == nil && !crossed:
			t.Fatalf("round %d: input reported success but no frame reached the child", round)
		case inputErr != nil && crossed:
			t.Fatalf("round %d: input was refused (%v) yet a frame reached the child", round, inputErr)
		}
		_ = lr.proc.Stop(context.Background())
		select {
		case <-lr.finalizedCh:
		case <-time.After(5 * time.Second):
		}
	}
}
