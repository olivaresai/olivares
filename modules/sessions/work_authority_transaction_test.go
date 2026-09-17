// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// These deterministic barriers pin the ORDER of the agent authority seam: the
// preliminary WorkItem View closes before observation, Plan/Validate validate
// inside their own View, and Apply binds the detached stamp inside Mutate. They
// do not replace the composed boot regression in cmd/olivares, which drives the
// production resolver on SQLite's single connection.

// openWorkViewData counts store transactions the work kernel holds open.
type openWorkViewData struct {
	inner api.ModuleData
	open  atomic.Int32
}

func (d *openWorkViewData) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	d.open.Add(1)
	defer d.open.Add(-1)
	return d.inner.View(ctx, tenant, fn)
}

func (d *openWorkViewData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	d.open.Add(1)
	defer d.open.Add(-1)
	return d.inner.Mutate(ctx, tenant, fn)
}

// authorityTransactionResolver records each seam call and the number of work
// transactions open while observing.
type authorityTransactionResolver struct {
	allowWorkIdentity
	data            *openWorkViewData
	observed        atomic.Int32
	openWhileObsvd  atomic.Int32
	validated       atomic.Int32
	locked          atomic.Int32
	participants    atomic.Int32
	validateErr     error
	afterObserve    func()
	afterObserveOne sync.Once
}

func (r *authorityTransactionResolver) ObserveAgentWorkAuthority(
	ctx context.Context, tenant model.TenantID, workspace model.ID, owner, actor string,
) (WorkAgentAuthoritySnapshot, error) {
	r.observed.Add(1)
	if r.data != nil {
		r.openWhileObsvd.Add(r.data.open.Load())
	}
	snapshot, err := r.allowWorkIdentity.ObserveAgentWorkAuthority(ctx, tenant, workspace, owner, actor)
	if r.afterObserve != nil {
		r.afterObserveOne.Do(r.afterObserve)
	}
	return snapshot, err
}

// ResolveParticipant and SessionActsForAgent count identity preflight: the
// production resolver answers both with its own store read.
func (r *authorityTransactionResolver) ResolveParticipant(
	ctx context.Context, tenant model.TenantID, workspace model.ID, kind, ref string,
) (Participant, error) {
	r.participants.Add(1)
	return r.allowWorkIdentity.ResolveParticipant(ctx, tenant, workspace, kind, ref)
}

func (r *authorityTransactionResolver) SessionActsForAgent(
	ctx context.Context, tenant model.TenantID, sid, agentRef string,
) (bool, error) {
	r.participants.Add(1)
	return r.allowWorkIdentity.SessionActsForAgent(ctx, tenant, sid, agentRef)
}

func (r *authorityTransactionResolver) ValidateAgentWorkAuthorityInScope(
	context.Context, store.Scope, WorkAgentAuthoritySnapshot,
) error {
	r.validated.Add(1)
	return r.validateErr
}

func (r *authorityTransactionResolver) LockAgentWorkAuthority(
	context.Context, store.Scope, WorkAgentAuthoritySnapshot,
) error {
	r.locked.Add(1)
	return nil
}

// observeOnlyAuthorityResolver implements Observe and Lock but not the read
// validator, the shape of a composition that cannot validate inside a View.
type observeOnlyAuthorityResolver struct{ observed atomic.Int32 }

func (*observeOnlyAuthorityResolver) ResolveParticipant(
	ctx context.Context, tenant model.TenantID, workspace model.ID, kind, ref string,
) (Participant, error) {
	return allowWorkIdentity{}.ResolveParticipant(ctx, tenant, workspace, kind, ref)
}

func (*observeOnlyAuthorityResolver) SessionActsForAgent(context.Context, model.TenantID, string, string) (bool, error) {
	return true, nil
}

func (r *observeOnlyAuthorityResolver) ObserveAgentWorkAuthority(
	context.Context, model.TenantID, model.ID, string, string,
) (WorkAgentAuthoritySnapshot, error) {
	r.observed.Add(1)
	return WorkAgentAuthoritySnapshot{Eligible: true, Digest: "observe-only", Token: true}, nil
}

func (*observeOnlyAuthorityResolver) LockAgentWorkAuthority(
	context.Context, store.Scope, WorkAgentAuthoritySnapshot,
) error {
	return nil
}

func newAuthorityTransactionFixture(
	t *testing.T,
	title string,
) (workLeaseDomainFixture, *authorityTransactionResolver) {
	t.Helper()
	f := newWorkLeaseDomainFixture(t, title)
	data := &openWorkViewData{inner: f.m.data}
	f.m.data = data
	resolver := &authorityTransactionResolver{data: data}
	f.m.UseWorkIdentityResolver(resolver)
	return f, resolver
}

// bumpWorkItemVersion is a legitimate concurrent writer: an administrator edits
// the ready item, which advances its version without touching its owner.
func bumpWorkItemVersion(t *testing.T, f workLeaseDomainFixture, expected int64) {
	t.Helper()
	if _, err := f.m.Apply(context.Background(), f.tenant, f.principal, WorkCommand{
		Command: "item.update", WorkItemID: f.ready.ResultID, Title: "edited " + model.NewID().String(),
		ExpectedVersion: expected, IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPatch,
	}); err != nil {
		t.Errorf("concurrent WorkItem edit: %v", err)
	}
}

func TestWorkAuthorityObservationHoldsNoWorkTransaction(t *testing.T) {
	t.Parallel()

	t.Run("plan and validate", func(t *testing.T) {
		f, resolver := newAuthorityTransactionFixture(t, "plan observes outside its View")
		cmd := f.command("lease.acquire", f.ready.Version, 0)
		plan, err := f.m.Plan(context.Background(), f.tenant, f.holder, cmd)
		if err != nil || plan.Verdict != VerdictClean || len(plan.PlanHash) != 64 {
			t.Fatalf("plan = %#v, %v", plan, err)
		}
		assessment, err := f.m.Validate(context.Background(), f.tenant, f.holder, cmd)
		if err != nil || assessment.Verdict != VerdictClean {
			t.Fatalf("validate = %#v, %v", assessment, err)
		}
		if resolver.observed.Load() != 2 || resolver.validated.Load() != 2 || resolver.locked.Load() != 0 {
			t.Fatalf("seam calls observe=%d validate=%d lock=%d, want 2/2/0",
				resolver.observed.Load(), resolver.validated.Load(), resolver.locked.Load())
		}
		if open := resolver.openWhileObsvd.Load(); open != 0 {
			t.Fatalf("work transactions open while observing = %d, want 0", open)
		}
	})

	t.Run("apply", func(t *testing.T) {
		f, resolver := newAuthorityTransactionFixture(t, "apply observes before Mutate")
		result, err := f.m.Apply(context.Background(), f.tenant, f.holder, f.command("lease.acquire", f.ready.Version, 0))
		if err != nil || result.Code != "applied" {
			t.Fatalf("apply = %#v, %v", result, err)
		}
		if resolver.observed.Load() != 1 || resolver.locked.Load() != 1 || resolver.validated.Load() != 0 {
			t.Fatalf("seam calls observe=%d lock=%d validate=%d, want 1/1/0",
				resolver.observed.Load(), resolver.locked.Load(), resolver.validated.Load())
		}
		if open := resolver.openWhileObsvd.Load(); open != 0 {
			t.Fatalf("work transactions open while observing = %d, want 0", open)
		}
	})
}

func TestWorkAuthorityPlanReadValidationClassification(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		err     error
		verdict AssessmentVerdict
		code    string
	}{
		{name: "fact conflict", err: store.ErrConflict, verdict: VerdictBroken, code: "plan_changed"},
		{name: "fact absent", err: store.ErrNotFound, verdict: VerdictBroken, code: "plan_changed"},
		{name: "reader failure", err: errors.New("authority reader unavailable"),
			verdict: VerdictUnknown, code: "evidence_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, resolver := newAuthorityTransactionFixture(t, "plan validation "+tc.name)
			resolver.validateErr = tc.err
			cmd := f.command("lease.acquire", f.ready.Version, 0)
			plan, err := f.m.Plan(context.Background(), f.tenant, f.holder, cmd)
			if err != nil || plan.Verdict != tc.verdict || plan.Code != tc.code || plan.PlanHash != "" {
				t.Fatalf("plan = %#v, %v; want %s/%s without a hash", plan, err, tc.verdict, tc.code)
			}
			assessment, err := f.m.Validate(context.Background(), f.tenant, f.holder, cmd)
			if err != nil || assessment.Verdict != tc.verdict || assessment.Code != tc.code {
				t.Fatalf("validate = %#v, %v; want %s/%s", assessment, err, tc.verdict, tc.code)
			}
		})
	}

	t.Run("missing read validator is unknown", func(t *testing.T) {
		f := newWorkLeaseDomainFixture(t, "plan without read validator")
		resolver := &observeOnlyAuthorityResolver{}
		f.m.UseWorkIdentityResolver(resolver)
		cmd := f.command("lease.acquire", f.ready.Version, 0)
		plan, err := f.m.Plan(context.Background(), f.tenant, f.holder, cmd)
		if err != nil || plan.Verdict != VerdictUnknown || plan.Code != "evidence_unavailable" || plan.PlanHash != "" {
			t.Fatalf("plan without validator = %#v, %v", plan, err)
		}
		assessment, err := f.m.Validate(context.Background(), f.tenant, f.holder, cmd)
		if err != nil || assessment.Verdict != VerdictUnknown || assessment.Code != "evidence_unavailable" {
			t.Fatalf("validate without validator = %#v, %v", assessment, err)
		}
		// Apply locks instead of read-validating; the validator is not its seam.
		result, err := f.m.Apply(context.Background(), f.tenant, f.holder, cmd)
		if err != nil || result.Code != "applied" {
			t.Fatalf("apply without read validator = %#v, %v", result, err)
		}
	})
}

func TestWorkAuthorityStampChangeBetweenObservationAndUse(t *testing.T) {
	t.Parallel()

	t.Run("plan refuses a changed item generation", func(t *testing.T) {
		f, resolver := newAuthorityTransactionFixture(t, "plan stamp version")
		resolver.afterObserve = func() { bumpWorkItemVersion(t, f, f.ready.Version) }
		plan, err := f.m.Plan(context.Background(), f.tenant, f.holder, f.command("lease.acquire", f.ready.Version, 0))
		if err != nil || plan.Verdict != VerdictBroken || plan.Code != "plan_changed" || plan.PlanHash != "" {
			t.Fatalf("plan across item change = %#v, %v; want plan_changed", plan, err)
		}
		if resolver.validated.Load() != 0 {
			t.Fatalf("stale stamp validated authority %d times", resolver.validated.Load())
		}
	})

	for _, tc := range []struct {
		name     string
		version  func(f workLeaseDomainFixture) int64
		planHash string
		status   int
		code     string
	}{
		{name: "expected version still wins", version: func(f workLeaseDomainFixture) int64 { return f.ready.Version },
			status: http.StatusPreconditionFailed, code: "version_mismatch"},
		{name: "future version without plan", version: func(f workLeaseDomainFixture) int64 { return f.ready.Version + 1 },
			status: http.StatusUnprocessableEntity, code: "owner_ineligible"},
		{name: "future version with plan", version: func(f workLeaseDomainFixture) int64 { return f.ready.Version + 1 },
			planHash: strings.Repeat("a", 64), status: http.StatusPreconditionFailed, code: "plan_changed"},
	} {
		t.Run("apply "+tc.name, func(t *testing.T) {
			f, resolver := newAuthorityTransactionFixture(t, "apply stamp "+tc.name)
			resolver.afterObserve = func() { bumpWorkItemVersion(t, f, f.ready.Version) }
			before := getWorkLease(t, f)
			cmd := f.command("lease.acquire", tc.version(f), 0)
			cmd.ExpectedPlanHash = tc.planHash
			_, err := f.m.Apply(context.Background(), f.tenant, f.holder, cmd)
			we := asWorkError(err)
			if we == nil || we.status != tc.status || we.code != tc.code {
				t.Fatalf("apply across item change = %v, want %d %s", err, tc.status, tc.code)
			}
			if resolver.locked.Load() != 0 {
				t.Fatalf("stale stamp locked authority %d times", resolver.locked.Load())
			}
			if after := getWorkLease(t, f); after.Version != before.Version || after.State != before.State {
				t.Fatalf("refused apply changed the WorkLease: %#v -> %#v", before, after)
			}
		})
	}
}

func TestWorkAuthorityStampComparesEveryItemField(t *testing.T) {
	t.Parallel()

	item := model.Record{
		model.ColID: model.NewID().String(), model.ColVersion: int64(3),
		colWorkWorkspaceID: model.NewID().String(), colWorkOwnerKind: "agent",
		colWorkOwnerRef: model.NewID().String(), colWorkStatus: "ready",
	}
	principal := WorkPrincipal{ActorKind: model.ActorAgent, ActorRef: "agent:stamp", Actor: "agent:stamp"}
	cmd := WorkCommand{Command: "lease.acquire", authorityStamp: workAuthorityStampOf(item),
		agentAuthority: WorkAgentAuthoritySnapshot{Eligible: true, Digest: "d", Token: true}}
	if !agentWorkAuthorityStampCurrent(cmd, principal, item) {
		t.Fatal("identical WorkItem does not match its own stamp")
	}
	for column, value := range map[string]any{
		model.ColID: model.NewID().String(), model.ColVersion: int64(4),
		colWorkWorkspaceID: model.NewID().String(), colWorkOwnerKind: "session",
		colWorkOwnerRef: model.NewID().String(),
	} {
		changed := model.Record{}
		for k, v := range item {
			changed[k] = v
		}
		changed[column] = value
		if agentWorkAuthorityStampCurrent(cmd, principal, changed) {
			t.Errorf("stamp ignored a changed %s", column)
		}
	}

	// A command that neither observed nor needs authority is not bound.
	release := WorkCommand{Command: "lease.release", authorityStamp: workAuthorityStampOf(item)}
	moved := model.Record{}
	for k, v := range item {
		moved[k] = v
	}
	moved[model.ColVersion] = int64(9)
	if !agentWorkAuthorityStampCurrent(release, principal, moved) {
		t.Fatal("authority-reducing command was bound to an authority stamp")
	}
	// A command that needs authority now is bound even without a snapshot.
	unobserved := WorkCommand{Command: "lease.acquire", authorityStamp: workAuthorityStampOf(item)}
	if agentWorkAuthorityStampCurrent(unobserved, principal, moved) {
		t.Fatal("authority-requiring command accepted a changed item without an observation")
	}
}

func TestWorkAuthorityJoinedReplayNeverObservesBesideItsTransaction(t *testing.T) {
	t.Parallel()

	f, resolver := newAuthorityTransactionFixture(t, "joined replay authority")
	ctx := context.Background()
	acquire := f.command("lease.acquire", f.ready.Version, 0)
	first, err := f.m.Apply(ctx, f.tenant, f.holder, acquire)
	if err != nil || first.Code != "applied" {
		t.Fatalf("acquire before joined replay = %#v, %v", first, err)
	}
	observedBefore := resolver.observed.Load()
	participantsBefore := resolver.participants.Load()

	var replay CommandResult
	var replayErr, renewErr, internalErr error
	var internal WorkCommand
	if err := f.st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
		joined, joinedCtx := newProtocolReplayTransactionContext(ctx, f.tenant, sc)
		defer joined.active.Store(false)
		// Exact durable replay is answered before any observer, joined or not.
		replay, replayErr = f.m.Apply(joinedCtx, f.tenant, f.holder, acquire)
		// A new authority-consuming command has no lawful observation here, and
		// is refused before identity preflight would read beside the transaction.
		renew := f.command("lease.renew", first.Version, first.LeaseFence)
		_, renewErr = f.m.Apply(joinedCtx, f.tenant, f.holder, renew)
		// Internal commands keep their existing exclusion from agent authority.
		internal = f.command("lease.renew", first.Version, first.LeaseFence)
		internal.internal = true
		internalErr = f.m.prepareAgentWorkAuthority(joinedCtx, f.m.workData(f.tenant), f.tenant, f.holder, &internal)
		return nil
	}); err != nil {
		t.Fatalf("joined replay transaction: %v", err)
	}
	if replayErr != nil || !replay.Replayed || replay.CommandID != first.CommandID {
		t.Fatalf("joined exact replay = %#v, %v; want the durable result", replay, replayErr)
	}
	if we := asWorkError(renewErr); we == nil || we.verdict != VerdictUnknown || we.code != "evidence_unavailable" {
		t.Fatalf("joined new authority command = %v, want UNKNOWN evidence_unavailable", renewErr)
	}
	if internalErr != nil || internal.agentAuthority.Token != nil || internal.authorityStamp.itemID != f.ready.ResultID {
		t.Fatalf("joined internal preparation = %v stamp=%#v snapshot=%#v", internalErr, internal.authorityStamp, internal.agentAuthority)
	}
	if got := resolver.observed.Load(); got != observedBefore {
		t.Fatalf("joined transaction observed authority %d more times, want 0", got-observedBefore)
	}
	if got := resolver.participants.Load(); got != participantsBefore {
		t.Fatalf("joined transaction ran identity preflight %d times, want 0", got-participantsBefore)
	}
}

func TestWorkAuthorityPreparationResetsPrivateObservationState(t *testing.T) {
	t.Parallel()

	f, resolver := newAuthorityTransactionFixture(t, "reset private observation")
	cmd := f.command("lease.release", f.ready.Version, 0)
	cmd.agentAuthority = WorkAgentAuthoritySnapshot{Eligible: true, Digest: "carried", Token: true}
	cmd.authorityStamp = workAuthorityItemStamp{itemID: model.NewID(), version: 99}
	if err := f.m.prepareAgentWorkAuthority(
		context.Background(), f.m.workData(f.tenant), f.tenant, f.holder, &cmd,
	); err != nil {
		t.Fatalf("prepare authority-reducing command: %v", err)
	}
	if cmd.agentAuthority.Token != nil || cmd.agentAuthority.Digest != "" ||
		cmd.authorityStamp.itemID != f.ready.ResultID || cmd.authorityStamp.version != f.ready.Version {
		t.Fatalf("carried observation survived preparation: snapshot=%#v stamp=%#v", cmd.agentAuthority, cmd.authorityStamp)
	}
	if resolver.observed.Load() != 0 {
		t.Fatalf("authority-reducing command observed authority %d times", resolver.observed.Load())
	}
}
