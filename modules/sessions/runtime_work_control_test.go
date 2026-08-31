// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// workControlProc exposes deterministic hooks at the two external-effect seams.
// Hooks are one-shot so a cleanup Stop cannot repeat the test's injected race.
type workControlProc struct {
	*fakeProc
	mu         sync.Mutex
	afterSend  func()
	beforeStop func()
	sendErr    error
	stopCalls  int
}

func newWorkControlProc() *workControlProc {
	return &workControlProc{
		fakeProc: &fakeProc{out: make(chan OutputFrame, 16), stopped: make(chan struct{})},
	}
}

func (p *workControlProc) Send(ctx context.Context, line []byte) error {
	if err := p.fakeProc.Send(ctx, line); err != nil {
		return err
	}
	p.mu.Lock()
	hook := p.afterSend
	p.afterSend = nil
	err := p.sendErr
	p.sendErr = nil
	p.mu.Unlock()
	if hook != nil {
		hook()
	}
	return err
}

func (p *workControlProc) Stop(ctx context.Context) error {
	p.mu.Lock()
	p.stopCalls++
	hook := p.beforeStop
	p.beforeStop = nil
	p.mu.Unlock()
	if hook != nil {
		hook()
	}
	return p.fakeProc.Stop(ctx)
}

func (p *workControlProc) setAfterSend(hook func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.afterSend = hook
}

func (p *workControlProc) setSendErrorAfterWrite(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sendErr = err
}

func (p *workControlProc) setBeforeStop(hook func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.beforeStop = hook
}

func (p *workControlProc) stopCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stopCalls
}

type workControlRunner struct {
	mu   sync.Mutex
	proc *workControlProc
}

func (r *workControlRunner) Launch(context.Context, LaunchSpec) (Process, error) {
	p := newWorkControlProc()
	r.mu.Lock()
	r.proc = p
	r.mu.Unlock()
	return p, nil
}

func (r *workControlRunner) lastProc(t *testing.T) *workControlProc {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.proc == nil {
		t.Fatal("work-control runner was never launched")
	}
	return r.proc
}

type runtimeWorkControlFixture struct {
	m         *Module
	st        store.Store
	tenant    model.TenantID
	principal WorkPrincipal
	runRef    string
	claim     Lease
	itemID    model.ID
	itemVer   int64
	lease     WorkLease
	proc      *workControlProc
}

func newRuntimeWorkControlFixture(t *testing.T) *runtimeWorkControlFixture {
	t.Helper()
	ctx := context.Background()
	runner := &workControlRunner{}
	m, st, tenant, _ := newRuntimeHarness(t,
		WithRunner(runner),
		WithCredentialSource(staticCred()),
		WithWorkIdentityResolver(allowWorkIdentity{}),
		WithWorkContentGuard(allowWorkContent{}),
	)
	run, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON,
		Isolation: IsolationNative,
		Actor:     "user:runtime-work-control",
		ActorKind: model.ActorUser,
	})
	if err != nil {
		t.Fatalf("create governed run: %v", err)
	}
	proc := runner.lastProc(t)
	live, ok := m.rt.getLive(tenant, run.RunRef)
	if !ok {
		t.Fatal("created run has no live runtime handle")
	}
	claim := live.claim
	if !validCanonicalSID(claim.SID) {
		t.Fatalf("runtime claim SID %q is not canonical", claim.SID)
	}

	var workspace model.ID
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		ws, err := sc.DefaultWorkspace(ctx)
		workspace = ws.ID
		return err
	}); err != nil {
		t.Fatalf("default workspace: %v", err)
	}
	actorRef := model.NewID().String()
	principal := WorkPrincipal{
		ActorKind: model.ActorUser,
		ActorRef:  actorRef,
		Actor:     "user:" + actorRef,
		Admin:     true,
		SessionID: claim.SID,
	}
	created, err := m.Apply(ctx, tenant, principal, WorkCommand{
		Command:        "item.create",
		WorkspaceID:    workspace,
		WorkKind:       "implementation",
		Title:          "runtime work control",
		BriefMD:        "Exercise fenced runtime input, stop, and owner-death settlement.",
		ContextRefs:    []ContextRef{},
		Priority:       "p1",
		OwnerKind:      "session",
		OwnerRef:       claim.SID,
		ProvenanceKind: "human",
		ProvenanceRef:  "test:runtime-work-control",
		Acceptance: []AcceptanceInput{{
			Key: "runtime-control", Ordinal: 0,
			Statement: "The fenced runtime control behavior is proven.", Required: true,
		}},
		IdempotencyKey: model.NewID().String(),
		HTTPMethod:     http.MethodPost,
		CommandScope:   "POST /work-items",
	})
	if err != nil {
		t.Fatalf("create work item: %v", err)
	}
	ready, err := m.Apply(ctx, tenant, principal, WorkCommand{
		Command: "item.ready", WorkItemID: created.ResultID,
		ExpectedVersion: created.Version,
		IdempotencyKey:  model.NewID().String(), HTTPMethod: http.MethodPost,
	})
	if err != nil {
		t.Fatalf("ready work item: %v", err)
	}
	active, err := m.Apply(ctx, tenant, principal, WorkCommand{
		Command: "lease.acquire", WorkItemID: created.ResultID,
		HolderSID: claim.SID, HolderRunRef: run.RunRef,
		ExpectedVersion: ready.Version,
		IdempotencyKey:  model.NewID().String(), HTTPMethod: http.MethodPost,
	})
	if err != nil {
		t.Fatalf("acquire work lease: %v", err)
	}
	lease, err := m.GetLease(ctx, tenant, principal, created.ResultID)
	if err != nil {
		t.Fatalf("get acquired work lease: %v", err)
	}
	if active.Status != "active" || lease.State != workLeaseActive || !lease.Live ||
		lease.Fence < 1 || lease.HolderSID != claim.SID || lease.HolderRunRef != run.RunRef {
		t.Fatalf("fixture did not establish live work authority: active=%+v lease=%+v", active, lease)
	}
	runRec, err := m.loadRun(ctx, tenant, run.RunRef)
	if err != nil {
		t.Fatalf("load bound run: %v", err)
	}
	if runRec.String(colRunWorkItemID) != created.ResultID.String() ||
		runRec.Int(colRunWorkLeaseFence) != lease.Fence {
		t.Fatalf("run was not bound to acquired lease: %#v", runRec)
	}

	fixture := &runtimeWorkControlFixture{
		m: m, st: st, tenant: tenant, principal: principal,
		runRef: run.RunRef, claim: claim, itemID: created.ResultID, itemVer: active.Version,
		lease: lease, proc: proc,
	}
	t.Cleanup(func() {
		lr, exists := m.rt.getLive(tenant, run.RunRef)
		if !exists {
			return
		}
		proc.finish(0)
		select {
		case <-lr.finalizedCh:
		case <-time.After(3 * time.Second):
			t.Errorf("cleanup: run %s did not finalize", run.RunRef)
		}
	})
	return fixture
}

func TestInputForWorkExactFenceAndStaleFenceDirections(t *testing.T) {
	t.Parallel()

	fx := newRuntimeWorkControlFixture(t)
	ctx := context.Background()

	if err := fx.m.InputForWork(ctx, fx.tenant, fx.runRef, fx.lease.Fence+1, []byte(`{"type":"stale"}`)); workErrorCode(err) != "stale_fence" {
		t.Fatalf("stale input = %v, want stale_fence", err)
	}
	if got := fx.proc.sentCount(); got != 0 {
		t.Fatalf("stale input wrote to the process %d time(s)", got)
	}
	if got := countNamedRunEvents(t, fx.st, fx.tenant, fx.runRef, workInputAccepted); got != 0 {
		t.Fatalf("stale input settled %d accepted event(s)", got)
	}

	if err := fx.m.InputForWork(ctx, fx.tenant, fx.runRef, fx.lease.Fence, []byte(`{"type":"valid"}`)); err != nil {
		t.Fatalf("exact-fence input: %v", err)
	}
	if got := fx.proc.sentCount(); got != 1 {
		t.Fatalf("exact-fence input wrote to the process %d time(s), want 1", got)
	}
	if got := countNamedRunEvents(t, fx.st, fx.tenant, fx.runRef, workInputAccepted); got != 1 {
		t.Fatalf("exact-fence input settled %d accepted event(s), want 1", got)
	}
}

func TestInputForWorkRejectsReleasedRuntimeClaim(t *testing.T) {
	t.Parallel()

	fx := newRuntimeWorkControlFixture(t)
	ctx := context.Background()

	if err := fx.m.Release(
		ctx, fx.tenant, fx.claim.SID, fx.claim.Holder, fx.claim.Fence,
	); err != nil {
		t.Fatalf("release runtime claim: %v", err)
	}
	err := fx.m.InputForWork(
		ctx, fx.tenant, fx.runRef, fx.lease.Fence, []byte(`{"type":"released-claim"}`),
	)
	if workErrorCode(err) != "stale_fence" {
		t.Fatalf("input under released runtime claim = %v, want stale_fence", err)
	}
	if got := fx.proc.sentCount(); got != 0 {
		t.Fatalf("released runtime claim wrote to the process %d time(s)", got)
	}
	if got := countNamedRunEvents(
		t, fx.st, fx.tenant, fx.runRef, workInputAccepted,
	); got != 0 {
		t.Fatalf("released runtime claim settled %d accepted event(s)", got)
	}
}

func TestInputForWorkPreEffectFailuresDoNotRecordAmbiguity(t *testing.T) {
	t.Parallel()

	t.Run("newline", func(t *testing.T) {
		fx := newRuntimeWorkControlFixture(t)
		err := fx.m.InputForWork(
			context.Background(), fx.tenant, fx.runRef, fx.lease.Fence, []byte("first\nsecond"),
		)
		var re *runErr
		if !errors.As(err, &re) || re.status != http.StatusBadRequest || asWorkError(err) != nil {
			t.Fatalf("newline input = %v, want legacy 400 taxonomy", err)
		}
		assertNoInputEffectOrAmbiguity(t, fx)
	})

	t.Run("non-running state", func(t *testing.T) {
		fx := newRuntimeWorkControlFixture(t)
		if err := mutateRunForWorkTest(fx.m, fx.tenant, fx.runRef, func(rec model.Record) {
			rec[colState] = stateStopped
		}); err != nil {
			t.Fatalf("put run in pre-effect stopped state: %v", err)
		}
		err := fx.m.InputForWork(
			context.Background(), fx.tenant, fx.runRef, fx.lease.Fence, []byte(`{"type":"state"}`),
		)
		if !isRunConflict(err) || asWorkError(err) != nil {
			t.Fatalf("non-running input = %v, want direct runtime conflict", err)
		}
		assertNoInputEffectOrAmbiguity(t, fx)
	})

	t.Run("live handle disappears", func(t *testing.T) {
		fx := newRuntimeWorkControlFixture(t)
		live, ok := fx.m.rt.getLive(fx.tenant, fx.runRef)
		if !ok {
			t.Fatal("fixture run has no live handle")
		}
		drop := &afterNthViewData{
			inner: fx.m.data, after: 2,
			hook: func() { fx.m.rt.dropLive(fx.tenant, fx.runRef) },
		}
		fx.m.UseData(drop)
		err := fx.m.InputForWork(
			context.Background(), fx.tenant, fx.runRef, fx.lease.Fence, []byte(`{"type":"no-live"}`),
		)
		if !drop.fired() {
			t.Fatal("test did not remove the live handle between durable check and Send")
		}
		if !isRunConflict(err) || asWorkError(err) != nil {
			t.Fatalf("missing-live input = %v, want direct runtime conflict", err)
		}
		assertNoInputEffectOrAmbiguity(t, fx)
		fx.proc.finish(0)
		select {
		case <-live.finalizedCh:
		case <-time.After(3 * time.Second):
			t.Fatal("dropped live run did not finalize")
		}
	})
}

func TestInputForWorkWriteThenErrorRecordsAmbiguity(t *testing.T) {
	t.Parallel()

	fx := newRuntimeWorkControlFixture(t)
	fx.proc.setSendErrorAfterWrite(errors.New("test: process wrote before returning an error"))

	err := fx.m.InputForWork(
		context.Background(), fx.tenant, fx.runRef, fx.lease.Fence, []byte(`{"type":"write-error"}`),
	)
	if we := asWorkError(err); we == nil || we.verdict != VerdictUnknown || we.code != workInputAmbiguous {
		t.Fatalf("write-then-error input = %v, want UNKNOWN/%s", err, workInputAmbiguous)
	}
	if got := fx.proc.sentCount(); got != 1 {
		t.Fatalf("write-then-error effect count = %d, want 1", got)
	}
	if got := countNamedRunEvents(t, fx.st, fx.tenant, fx.runRef, workInputAccepted); got != 0 {
		t.Fatalf("write-then-error falsely settled %d accepted event(s)", got)
	}
	if got := countNamedRunEvents(t, fx.st, fx.tenant, fx.runRef, workInputAmbiguous); got != 1 {
		t.Fatalf("write-then-error settled %d ambiguous event(s), want 1", got)
	}
}

func TestLegacyInputWriteErrorKeepsBadRequestTaxonomy(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	runner := &workControlRunner{}
	m, _, tenant, _ := newRuntimeHarness(t,
		WithRunner(runner), WithCredentialSource(staticCred()),
	)
	run, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:legacy-input", ActorKind: model.ActorUser,
	})
	if err != nil {
		t.Fatalf("create legacy run: %v", err)
	}
	proc := runner.lastProc(t)
	live, ok := m.rt.getLive(tenant, run.RunRef)
	if !ok {
		t.Fatal("legacy run has no live handle")
	}
	t.Cleanup(func() {
		proc.finish(0)
		select {
		case <-live.finalizedCh:
		case <-time.After(3 * time.Second):
			t.Errorf("cleanup: legacy run %s did not finalize", run.RunRef)
		}
	})
	proc.setSendErrorAfterWrite(errors.New("test: legacy process write error"))
	err = m.sendInput(ctx, tenant, run.RunRef, []byte(`{"type":"legacy"}`))
	var re *runErr
	if !errors.As(err, &re) || re.status != http.StatusBadRequest || asWorkError(err) != nil {
		t.Fatalf("legacy write error = %v, want unchanged runtime 400 taxonomy", err)
	}
	if got := proc.sentCount(); got != 1 {
		t.Fatalf("legacy write-error effect count = %d, want 1", got)
	}
}

func TestInputForWorkFenceMoveAfterEffectRecordsAmbiguity(t *testing.T) {
	t.Parallel()

	fx := newRuntimeWorkControlFixture(t)
	ctx := context.Background()
	var hookErr error
	fx.proc.setAfterSend(func() {
		_, hookErr = fx.m.Apply(ctx, fx.tenant, fx.principal, WorkCommand{
			Command: "lease.revoke", WorkItemID: fx.itemID,
			Fence: fx.lease.Fence, Reason: "test fence moved after input effect",
			ExpectedVersion: fx.itemVer,
			IdempotencyKey:  model.NewID().String(), HTTPMethod: http.MethodPost,
		})
	})

	err := fx.m.InputForWork(ctx, fx.tenant, fx.runRef, fx.lease.Fence, []byte(`{"type":"raced"}`))
	if hookErr != nil {
		t.Fatalf("move fence from process hook: %v", hookErr)
	}
	if we := asWorkError(err); we == nil || we.verdict != VerdictUnknown || we.code != workInputAmbiguous {
		t.Fatalf("post-effect fence move = %v, want UNKNOWN/%s", err, workInputAmbiguous)
	}
	if got := fx.proc.sentCount(); got != 1 {
		t.Fatalf("raced input effect count = %d, want 1", got)
	}
	if got := countNamedRunEvents(t, fx.st, fx.tenant, fx.runRef, workInputAccepted); got != 0 {
		t.Fatalf("raced input falsely settled %d accepted event(s)", got)
	}
	if got := countNamedRunEvents(t, fx.st, fx.tenant, fx.runRef, workInputAmbiguous); got != 1 {
		t.Fatalf("raced input settled %d ambiguous event(s), want 1", got)
	}
}

func TestStopForWorkTransitionFailureIsPreEffect(t *testing.T) {
	t.Parallel()

	fx := newRuntimeWorkControlFixture(t)
	ctx := context.Background()
	transitionErr := errors.New("test: stopping transition never opened")
	fault := &failNthMutateData{inner: fx.m.data, failAt: 2, err: transitionErr}
	fx.m.UseData(fault)

	err := fx.m.StopForWork(ctx, fx.tenant, fx.runRef, fx.lease.Fence, "pre-effect failure")
	if !errors.Is(err, transitionErr) || asWorkError(err) != nil {
		t.Fatalf("pre-effect transition failure = %v, want direct injected error", err)
	}
	if got := fx.proc.stopCount(); got != 0 {
		t.Fatalf("failed stopping transition called Process.Stop %d time(s)", got)
	}
	if got := countNamedRunEvents(t, fx.st, fx.tenant, fx.runRef, workStopAmbiguous); got != 0 {
		t.Fatalf("pre-effect transition failure recorded %d ambiguous event(s)", got)
	}

	if err := fx.m.StopForWork(ctx, fx.tenant, fx.runRef, fx.lease.Fence, "valid neighbor"); err != nil {
		t.Fatalf("valid stop after pre-effect failure: %v", err)
	}
	if got := fx.proc.stopCount(); got != 1 {
		t.Fatalf("valid neighbor called Process.Stop %d time(s), want 1", got)
	}
	if got := countNamedRunEvents(t, fx.st, fx.tenant, fx.runRef, workStopConfirmed); got != 1 {
		t.Fatalf("valid neighbor settled %d confirmed event(s), want 1", got)
	}
}

func TestStopForWorkFencesEffectAndSettlesOwnerDeath(t *testing.T) {
	t.Parallel()

	fx := newRuntimeWorkControlFixture(t)
	ctx := context.Background()

	if err := fx.m.StopForWork(ctx, fx.tenant, fx.runRef, fx.lease.Fence+1, "stale stop"); workErrorCode(err) != "stale_fence" {
		t.Fatalf("stale stop = %v, want stale_fence", err)
	}
	fx.proc.fakeProc.mu.Lock()
	stopped := fx.proc.done
	fx.proc.fakeProc.mu.Unlock()
	if stopped {
		t.Fatal("stale stop killed the supervised process")
	}
	if got := countNamedRunEvents(t, fx.st, fx.tenant, fx.runRef, workStopAmbiguous); got != 0 {
		t.Fatalf("pre-effect stale stop recorded %d ambiguous event(s)", got)
	}

	order := &claimReleaseBeforeOwnerDeathData{inner: fx.m.data, sid: fx.claim.SID}
	fx.m.UseData(order)
	fx.proc.setBeforeStop(order.arm)
	if err := fx.m.StopForWork(ctx, fx.tenant, fx.runRef, fx.lease.Fence, "operator requested"); err != nil {
		t.Fatalf("exact-fence stop: %v", err)
	}
	observed, released, observeErr := order.result()
	if observeErr != nil {
		t.Fatalf("observe claim at owner-death callback: %v", observeErr)
	}
	if !observed || !released {
		t.Fatalf("OwnerDied began before claim release: observed=%v released=%v", observed, released)
	}
	assertOwnerDeathSettlement(t, fx, fx.lease.Fence)
	if _, live, err := fx.m.ActiveClaim(ctx, fx.tenant, fx.claim.SID); err != nil || live {
		t.Fatalf("valid stop left runtime claim live: live=%v err=%v", live, err)
	}
	if got := countNamedRunEvents(t, fx.st, fx.tenant, fx.runRef, workStopConfirmed); got != 1 {
		t.Fatalf("valid stop settled %d confirmed event(s), want 1", got)
	}
	assertSubsequence(t, eventNames(listRunEvents(t, fx.st, fx.tenant, fx.runRef)),
		"stopping", "stopped", workStopConfirmed)
}

func TestOwnerDiedDirectlyRevokesMatchingRunGeneration(t *testing.T) {
	t.Parallel()

	fx := newRuntimeWorkControlFixture(t)
	ctx := context.Background()

	if err := fx.m.OwnerDied(ctx, fx.tenant, fx.claim.SID, fx.runRef, "direct owner death"); err != nil {
		t.Fatalf("OwnerDied: %v", err)
	}
	assertOwnerDeathSettlement(t, fx, fx.lease.Fence)
	fx.proc.fakeProc.mu.Lock()
	stopped := fx.proc.done
	fx.proc.fakeProc.mu.Unlock()
	if stopped {
		t.Fatal("OwnerDied directly stopped the process instead of settling durable authority")
	}
	if _, live, err := fx.m.ActiveClaim(ctx, fx.tenant, fx.claim.SID); err != nil || !live {
		t.Fatalf("direct OwnerDied unexpectedly released the runtime claim: live=%v err=%v", live, err)
	}
}

func TestNaturalRuntimeDeathInvokesOwnerDiedCallback(t *testing.T) {
	t.Parallel()

	fx := newRuntimeWorkControlFixture(t)
	ctx := context.Background()
	live, ok := fx.m.rt.getLive(fx.tenant, fx.runRef)
	if !ok {
		t.Fatal("work-bound run has no live handle")
	}
	fx.proc.finish(17)
	select {
	case <-live.finalizedCh:
	case <-time.After(3 * time.Second):
		t.Fatal("natural process death did not finalize")
	}
	run, err := fx.m.getRun(ctx, fx.tenant, fx.runRef)
	if err != nil || run.State != stateFailed {
		t.Fatalf("natural death run = %+v, %v; want failed", run, err)
	}
	assertOwnerDeathSettlement(t, fx, fx.lease.Fence)
	if _, claimLive, err := fx.m.ActiveClaim(ctx, fx.tenant, fx.claim.SID); err != nil || claimLive {
		t.Fatalf("natural death left runtime claim live: live=%v err=%v", claimLive, err)
	}
}

func assertOwnerDeathSettlement(t *testing.T, fx *runtimeWorkControlFixture, oldFence int64) {
	t.Helper()
	ctx := context.Background()
	lease, err := fx.m.GetLease(ctx, fx.tenant, fx.principal, fx.itemID)
	if err != nil {
		t.Fatalf("get owner-death lease: %v", err)
	}
	if lease.State != workLeaseRevoked || lease.Fence != oldFence+1 || lease.Live {
		t.Fatalf("owner-death lease = %+v, want revoked fence %d", lease, oldFence+1)
	}
	snapshot, err := fx.m.Get(ctx, fx.tenant, fx.principal, fx.itemID)
	if err != nil {
		t.Fatalf("get owner-death work item: %v", err)
	}
	if snapshot.Item.Status != "blocked" || snapshot.Item.BlockedCode != "owner_session_died" {
		t.Fatalf("owner-death work item = %+v, want blocked/owner_session_died", snapshot.Item)
	}
}

func countNamedRunEvents(
	t *testing.T,
	st store.Store,
	tenant model.TenantID,
	runRef string,
	name string,
) int {
	t.Helper()
	count := 0
	for _, event := range listRunEvents(t, st, tenant, runRef) {
		if event.Event == name {
			count++
		}
	}
	return count
}

func requireRunEventWorkFence(
	t *testing.T,
	st store.Store,
	tenant model.TenantID,
	runRef string,
	name string,
	wantItem model.ID,
	wantSID string,
	want int64,
) {
	t.Helper()
	for _, event := range listRunEvents(t, st, tenant, runRef) {
		if event.Event != name {
			continue
		}
		if event.WorkLeaseFence == nil || *event.WorkLeaseFence != want {
			t.Fatalf("%s work_lease_fence = %v, want %d", name, event.WorkLeaseFence, want)
		}
		if event.WorkItemID != wantItem.String() || event.WorkHolderSID != wantSID {
			t.Fatalf("%s generation = item %q sid %q, want %q/%q", name,
				event.WorkItemID, event.WorkHolderSID, wantItem, wantSID)
		}
		return
	}
	t.Fatalf("run event %s was not recorded", name)
}

func assertNoInputEffectOrAmbiguity(t *testing.T, fx *runtimeWorkControlFixture) {
	t.Helper()
	if got := fx.proc.sentCount(); got != 0 {
		t.Fatalf("pre-effect input wrote to the process %d time(s)", got)
	}
	if got := countNamedRunEvents(t, fx.st, fx.tenant, fx.runRef, workInputAccepted); got != 0 {
		t.Fatalf("pre-effect input settled %d accepted event(s)", got)
	}
	if got := countNamedRunEvents(t, fx.st, fx.tenant, fx.runRef, workInputAmbiguous); got != 0 {
		t.Fatalf("pre-effect input settled %d ambiguous event(s)", got)
	}
}

func workErrorCode(err error) string {
	if we := asWorkError(err); we != nil {
		return we.code
	}
	return ""
}

type afterNthViewData struct {
	inner api.ModuleData
	after int
	hook  func()

	mu      sync.Mutex
	views   int
	didFire bool
}

func (d *afterNthViewData) View(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	err := d.inner.View(ctx, tenant, fn)
	d.mu.Lock()
	d.views++
	fire := !d.didFire && d.views == d.after
	if fire {
		d.didFire = true
	}
	hook := d.hook
	d.mu.Unlock()
	if fire && hook != nil {
		hook()
	}
	return err
}

func (d *afterNthViewData) Mutate(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	return d.inner.Mutate(ctx, tenant, fn)
}

func (d *afterNthViewData) fired() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.didFire
}

type failNthMutateData struct {
	inner  api.ModuleData
	failAt int
	err    error

	mu    sync.Mutex
	calls int
}

func (d *failNthMutateData) View(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	return d.inner.View(ctx, tenant, fn)
}

func (d *failNthMutateData) Mutate(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	d.mu.Lock()
	d.calls++
	fail := d.calls == d.failAt
	d.mu.Unlock()
	if fail {
		return d.err
	}
	return d.inner.Mutate(ctx, tenant, fn)
}

// claimReleaseBeforeOwnerDeathData is armed by the process Stop effect. Runtime
// incarnation and credential cleanup also read through ModuleData after Stop, and
// their ordering is scheduler-dependent. The scope wrapper therefore ignores
// those run-row reads and observes the claim only when OwnerDied opens WorkLease.
// That pins finalize's required release-then-callback ordering semantically.
type claimReleaseBeforeOwnerDeathData struct {
	inner api.ModuleData
	sid   string

	mu                      sync.Mutex
	armed                   bool
	capturing               bool
	captured                bool
	releasedBeforeOwnerDied bool
	err                     error
}

func (d *claimReleaseBeforeOwnerDeathData) arm() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.armed = true
}

func (d *claimReleaseBeforeOwnerDeathData) View(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	d.mu.Lock()
	armed := d.armed
	d.mu.Unlock()
	if !armed {
		return d.inner.View(ctx, tenant, fn)
	}
	return d.inner.View(ctx, tenant, func(sc store.Scope) error {
		return fn(&claimReleaseObservationScope{Scope: sc, owner: d, ctx: ctx})
	})
}

func (d *claimReleaseBeforeOwnerDeathData) Mutate(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	return d.inner.Mutate(ctx, tenant, fn)
}

func (d *claimReleaseBeforeOwnerDeathData) result() (bool, bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.captured, d.releasedBeforeOwnerDied, d.err
}

func (d *claimReleaseBeforeOwnerDeathData) observeOwnerDeath(
	ctx context.Context,
	sc store.Scope,
) {
	d.mu.Lock()
	if !d.armed || d.capturing || d.captured {
		d.mu.Unlock()
		return
	}
	d.capturing = true
	d.mu.Unlock()

	rec, found, err := findClaim(ctx, sc, d.sid)
	released := found && err == nil && rec.String(colClaimState) == claimReleased

	d.mu.Lock()
	d.capturing = false
	d.captured = true
	d.releasedBeforeOwnerDied = released
	d.err = err
	d.mu.Unlock()
}

type claimReleaseObservationScope struct {
	store.Scope
	owner *claimReleaseBeforeOwnerDeathData
	ctx   context.Context
}

func (s *claimReleaseObservationScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	if kind == workLeaseKind {
		s.owner.observeOwnerDeath(s.ctx, s.Scope)
	}
	return s.Scope.Ext(kind)
}

var _ api.ModuleData = (*claimReleaseBeforeOwnerDeathData)(nil)
var _ store.Scope = (*claimReleaseObservationScope)(nil)
var _ api.ModuleData = (*afterNthViewData)(nil)
var _ api.ModuleData = (*failNthMutateData)(nil)
var _ Process = (*workControlProc)(nil)

// releaseWorkLease ends the fixture's generation the way a finished session
// does, leaving the supervised process alive.
func (fx *runtimeWorkControlFixture) releaseWorkLease(t *testing.T) {
	t.Helper()
	released, err := fx.m.Apply(context.Background(), fx.tenant, fx.principal, WorkCommand{
		Command: "lease.release", WorkItemID: fx.itemID,
		HolderSID: fx.claim.SID, HolderRunRef: fx.runRef, Fence: fx.lease.Fence,
		Reason:          "work finished, process still supervised",
		ExpectedVersion: fx.itemVer,
		IdempotencyKey:  model.NewID().String(), HTTPMethod: http.MethodPost,
	})
	if err != nil {
		t.Fatalf("release work lease: %v", err)
	}
	fx.itemVer = released.Version
}

// TestRunOutlivesItsWorkLeaseGenerationWithoutReopeningLegacyControl pins the
// permanent control-plane selection made by the immutable run stamp. A released
// generation may be re-acquired and controlled with its new fence, but legacy
// input/stop/resume never reopen: doing so would race a concurrent acquire.
func TestRunOutlivesItsWorkLeaseGenerationWithoutReopeningLegacyControl(t *testing.T) {
	t.Parallel()

	t.Run("no-fire: a LIVE generation still refuses the legacy paths", func(t *testing.T) {
		fx := newRuntimeWorkControlFixture(t)
		ctx := context.Background()
		var re *runErr
		if err := fx.m.sendInput(ctx, fx.tenant, fx.runRef, []byte(`{"type":"legacy"}`)); !errors.As(err, &re) ||
			re.status != http.StatusConflict {
			t.Fatalf("legacy input under a live generation = %v, want 409", err)
		}
		if _, err := fx.m.stopRun(ctx, fx.tenant, fx.runRef, "user:operator", model.ActorUser); !errors.As(err, &re) ||
			re.status != http.StatusConflict {
			t.Fatalf("legacy stop under a live generation = %v, want 409", err)
		}
		if got := fx.proc.sentCount(); got != 0 {
			t.Fatalf("refused legacy input still wrote %d time(s)", got)
		}
		if got := fx.proc.stopCount(); got != 0 {
			t.Fatalf("refused legacy stop still stopped the process %d time(s)", got)
		}
	})

	t.Run("release still refuses legacy stop", func(t *testing.T) {
		fx := newRuntimeWorkControlFixture(t)
		ctx := context.Background()
		fx.releaseWorkLease(t)

		if _, ok := fx.m.rt.getLive(fx.tenant, fx.runRef); !ok {
			t.Fatal("release must not kill the supervised process")
		}
		var re *runErr
		if _, err := fx.m.stopRun(ctx, fx.tenant, fx.runRef, "user:operator", model.ActorUser); !errors.As(err, &re) || re.status != http.StatusConflict {
			t.Fatalf("legacy stop after release = %v, want 409", err)
		}
		if got := fx.proc.stopCount(); got != 0 {
			t.Fatalf("refused legacy stop after release stopped the process %d time(s)", got)
		}
	})

	t.Run("release then re-acquire", func(t *testing.T) {
		fx := newRuntimeWorkControlFixture(t)
		ctx := context.Background()
		fx.releaseWorkLease(t)

		unblocked, err := fx.m.Apply(ctx, fx.tenant, fx.principal, WorkCommand{
			Command: "item.unblock", WorkItemID: fx.itemID,
			ExpectedVersion: fx.itemVer,
			IdempotencyKey:  model.NewID().String(), HTTPMethod: http.MethodPost,
		})
		if err != nil {
			t.Fatalf("unblock after release: %v", err)
		}
		again, err := fx.m.Apply(ctx, fx.tenant, fx.principal, WorkCommand{
			Command: "lease.acquire", WorkItemID: fx.itemID,
			HolderSID: fx.claim.SID, HolderRunRef: fx.runRef,
			ExpectedVersion: unblocked.Version,
			IdempotencyKey:  model.NewID().String(), HTTPMethod: http.MethodPost,
		})
		if err != nil {
			t.Fatalf("re-acquire the same run = %v, want the run to be re-adoptable", err)
		}
		fx.itemVer = again.Version
		lease, err := fx.m.GetLease(ctx, fx.tenant, fx.principal, fx.itemID)
		if err != nil {
			t.Fatalf("get re-acquired lease: %v", err)
		}
		// The release already bumped the fence invalidatingly, so the new
		// generation is strictly above the released one, not merely +1.
		if lease.State != workLeaseActive || lease.Fence <= fx.lease.Fence ||
			lease.HolderRunRef != fx.runRef {
			t.Fatalf("re-acquired lease = %#v", lease)
		}
		runRec, err := fx.m.loadRun(ctx, fx.tenant, fx.runRef)
		if err != nil {
			t.Fatalf("load re-bound run: %v", err)
		}
		if runRec.Int(colRunWorkLeaseFence) != lease.Fence {
			t.Fatalf("run stamp was not re-bound to the new generation: %#v", runRec)
		}
		// And the new generation governs again: the legacy path closes.
		var re *runErr
		if err := fx.m.sendInput(ctx, fx.tenant, fx.runRef, []byte(`{"type":"legacy"}`)); !errors.As(err, &re) ||
			re.status != http.StatusConflict {
			t.Fatalf("legacy input under the re-acquired generation = %v, want 409", err)
		}
		if err := fx.m.InputForWork(ctx, fx.tenant, fx.runRef, lease.Fence, []byte(`{"type":"fenced"}`)); err != nil {
			t.Fatalf("fenced input under the re-acquired generation: %v", err)
		}
	})
}

// TestReleaseDuringInputStillRecordsTheAmbiguity pins the reason the run stamp
// is never erased, which the B3 tests alone do not: erasing it on a generation's
// end is a tempting and WRONG repair, and this is the direction that catches it.
//
// The stamp is how settleRunWorkAction identifies the generation an outcome
// belongs to (runtime_work_lease.go), so it is read back after that generation
// is over. A holder that releases while its own input is still in flight must
// still leave a durable AMBIGUOUS event: the effect crossed the process boundary
// and nobody can say afterwards whether it landed under the old authority. An
// eraser turns that UNKNOWN into no event at all — the third answer collapsed
// into silence, which is worse than either of the other two.
func TestReleaseDuringInputStillRecordsTheAmbiguity(t *testing.T) {
	t.Parallel()

	fx := newRuntimeWorkControlFixture(t)
	ctx := context.Background()
	var hookErr error
	fx.proc.setAfterSend(func() { hookErr = releaseWorkLeaseErr(ctx, fx) })

	err := fx.m.InputForWork(ctx, fx.tenant, fx.runRef, fx.lease.Fence, []byte(`{"type":"raced"}`))
	if hookErr != nil {
		t.Fatalf("release from the process hook: %v", hookErr)
	}
	if we := asWorkError(err); we == nil || we.verdict != VerdictUnknown || we.code != workInputAmbiguous {
		t.Fatalf("input raced by a release = %v, want UNKNOWN/%s", err, workInputAmbiguous)
	}
	if got := countNamedRunEvents(t, fx.st, fx.tenant, fx.runRef, workInputAccepted); got != 0 {
		t.Fatalf("raced input falsely settled %d accepted event(s)", got)
	}
	if got := countNamedRunEvents(t, fx.st, fx.tenant, fx.runRef, workInputAmbiguous); got != 1 {
		t.Fatalf("raced input settled %d ambiguous event(s), want 1", got)
	}
	requireRunEventWorkFence(t, fx.st, fx.tenant, fx.runRef, workInputAmbiguous,
		fx.itemID, fx.claim.SID, fx.lease.Fence)
}

func releaseWorkLeaseErr(ctx context.Context, fx *runtimeWorkControlFixture) error {
	released, err := fx.m.Apply(ctx, fx.tenant, fx.principal, WorkCommand{
		Command: "lease.release", WorkItemID: fx.itemID,
		HolderSID: fx.claim.SID, HolderRunRef: fx.runRef, Fence: fx.lease.Fence,
		Reason:          "test release during an in-flight input",
		ExpectedVersion: fx.itemVer,
		IdempotencyKey:  model.NewID().String(), HTTPMethod: http.MethodPost,
	})
	if err == nil {
		fx.itemVer = released.Version
	}
	return err
}

// TestRebindDoesNotSwallowThePreviousGenerationsAmbiguity closes the window that
// re-adoption opens. Before B3 the run stamp was write-once in practice —
// bindRunToWorkLease demanded fence equality, so no run could ever be re-bound —
// and the ambiguous-settlement path leaned on that: it required the stamp to
// name the exact generation being settled.
//
// Once a later generation of the same item can re-adopt the run, that
// requirement means an effect that crossed the process boundary under generation
// N leaves NO durable trace if somebody re-acquires before the caller records
// its uncertainty. That is the same UNKNOWN-downgraded-to-nothing as the eraser,
// arriving by a different door, and it was measured, not hypothesized.
func TestRebindDoesNotSwallowThePreviousGenerationsAmbiguity(t *testing.T) {
	t.Parallel()

	fx := newRuntimeWorkControlFixture(t)
	ctx := context.Background()
	oldFence := fx.lease.Fence
	oldGeneration := runtimeWorkGenerationFromLease(fx.lease)

	if err := releaseWorkLeaseErr(ctx, fx); err != nil {
		t.Fatalf("release: %v", err)
	}
	unblocked, err := fx.m.Apply(ctx, fx.tenant, fx.principal, WorkCommand{
		Command: "item.unblock", WorkItemID: fx.itemID,
		ExpectedVersion: fx.itemVer,
		IdempotencyKey:  model.NewID().String(), HTTPMethod: http.MethodPost,
	})
	if err != nil {
		t.Fatalf("unblock: %v", err)
	}
	again, err := fx.m.Apply(ctx, fx.tenant, fx.principal, WorkCommand{
		Command: "lease.acquire", WorkItemID: fx.itemID,
		HolderSID: fx.claim.SID, HolderRunRef: fx.runRef,
		ExpectedVersion: unblocked.Version,
		IdempotencyKey:  model.NewID().String(), HTTPMethod: http.MethodPost,
	})
	if err != nil {
		t.Fatalf("re-acquire: %v", err)
	}
	fx.itemVer = again.Version

	if err := fx.m.settleRunWorkAction(ctx, fx.tenant, fx.runRef, oldGeneration, workInputAmbiguous); err != nil {
		t.Fatalf("ambiguity about the superseded generation = %v, want it recorded", err)
	}
	if got := countNamedRunEvents(t, fx.st, fx.tenant, fx.runRef, workInputAmbiguous); got != 1 {
		t.Fatalf("ambiguous event(s) after re-bind = %d, want 1", got)
	}
	requireRunEventWorkFence(t, fx.st, fx.tenant, fx.runRef, workInputAmbiguous,
		fx.itemID, fx.claim.SID, oldFence)

	// NO-FIRE: the relaxation is for the ambiguous events and for a stamp that
	// moved FORWARD only. A CONFIRMED outcome about a superseded generation, and
	// any settlement about a fence ahead of the stamp, still refuse.
	if err := fx.m.settleRunWorkAction(ctx, fx.tenant, fx.runRef, oldGeneration, workInputAccepted); workErrorCode(err) != "stale_fence" {
		t.Fatalf("confirmed input about a superseded generation = %v, want stale_fence", err)
	}
	newFence := fx.lease.Fence
	if lease, err := fx.m.GetLease(ctx, fx.tenant, fx.principal, fx.itemID); err == nil {
		newFence = lease.Fence
	}
	futureGeneration := oldGeneration
	futureGeneration.fence = newFence + 1
	if err := fx.m.settleRunWorkAction(ctx, fx.tenant, fx.runRef, futureGeneration, workInputAmbiguous); workErrorCode(err) != "stale_fence" {
		t.Fatalf("ambiguity about a FUTURE generation = %v, want stale_fence", err)
	}
	if got := countNamedRunEvents(t, fx.st, fx.tenant, fx.runRef, workInputAccepted); got != 0 {
		t.Fatalf("refused settlement still wrote %d accepted event(s)", got)
	}
}
