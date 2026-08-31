// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type allowWorkIdentity struct{}

func (allowWorkIdentity) ResolveParticipant(_ context.Context, _ model.TenantID, _ model.ID, kind, ref string) (Participant, error) {
	return Participant{Kind: kind, CanonicalRef: ref, Active: true, WorkspaceEligible: true}, nil
}

func (allowWorkIdentity) SessionActsForAgent(context.Context, model.TenantID, string, string) (bool, error) {
	return true, nil
}

func (allowWorkIdentity) ObserveAgentWorkAuthority(
	context.Context, model.TenantID, model.ID, string, string,
) (WorkAgentAuthoritySnapshot, error) {
	return WorkAgentAuthoritySnapshot{Eligible: true, Digest: "test-authority", Token: true}, nil
}

func (allowWorkIdentity) LockAgentWorkAuthority(
	context.Context, store.Scope, WorkAgentAuthoritySnapshot,
) error {
	return nil
}

type blockingWorkAuthority struct {
	allowWorkIdentity
	entered chan struct{}
	release chan struct{}
	err     error
	once    sync.Once
}

type claimTouchOrderModuleData struct {
	inner   api.ModuleData
	touches atomic.Int32
}

func (d *claimTouchOrderModuleData) View(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	return d.inner.View(ctx, tenant, fn)
}

func (d *claimTouchOrderModuleData) Mutate(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	return d.inner.Mutate(ctx, tenant, func(raw store.Scope) error {
		clock, ok := raw.(store.TransactionClock)
		if !ok {
			return errors.New("Claim touch order fixture lacks transaction clock")
		}
		locker, ok := raw.(store.TransactionLocker)
		if !ok {
			return errors.New("Claim touch order fixture lacks transaction locker")
		}
		return fn(&claimTouchOrderScope{
			Scope: raw, clock: clock, locker: locker, touches: &d.touches,
		})
	})
}

type claimTouchOrderScope struct {
	store.Scope
	clock   store.TransactionClock
	locker  store.TransactionLocker
	touches *atomic.Int32
}

func (s *claimTouchOrderScope) TransactionNow(ctx context.Context) (model.Timestamp, error) {
	return s.clock.TransactionNow(ctx)
}

func (s *claimTouchOrderScope) LockTransaction(ctx context.Context, key string) error {
	return s.locker.LockTransaction(ctx, key)
}

func (s *claimTouchOrderScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	repo, err := s.Scope.Ext(kind)
	if err != nil || kind != claimKind {
		return repo, err
	}
	return &claimTouchOrderRepo{GenericRepo: repo, touches: s.touches}, nil
}

type claimTouchOrderRepo struct {
	store.GenericRepo
	touches *atomic.Int32
}

func (r *claimTouchOrderRepo) Update(
	ctx context.Context,
	record model.Record,
) (model.Record, error) {
	r.touches.Add(1)
	return r.GenericRepo.Update(ctx, record)
}

func (r *blockingWorkAuthority) LockAgentWorkAuthority(
	ctx context.Context,
	_ store.Scope,
	_ WorkAgentAuthoritySnapshot,
) error {
	r.once.Do(func() { close(r.entered) })
	select {
	case <-r.release:
		return r.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

type allowWorkContent struct{}

func (allowWorkContent) Inspect(context.Context, model.TenantID, model.ID, string, []byte) (ContentDecision, error) {
	return ContentDecision{Allowed: true, Code: "clean"}, nil
}

type recordingWorkSink struct {
	mu       sync.Mutex
	events   []WorkEventEnvelope
	attempts []WorkEventEnvelope
	err      error
}

func (s *recordingWorkSink) IngestDurable(_ context.Context, e WorkEventEnvelope) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempts = append(s.attempts, e)
	if s.err != nil {
		return s.err
	}
	s.events = append(s.events, e)
	return nil
}

type workFixture struct {
	m         *Module
	st        store.Store
	tenant    model.TenantID
	workspace model.ID
	principal WorkPrincipal
}

// confinedWorkData is an explicit capability used by tests that exercise the
// service below HTTP. It deliberately does not depend on request context
// markers: every callback receives a workspace-confined Scope by construction.
type confinedWorkData struct {
	inner     workData
	workspace model.ID
}

type unavailableWorkData struct{ err error }

func (d unavailableWorkData) View(context.Context, func(store.Scope) error) error   { return d.err }
func (d unavailableWorkData) Mutate(context.Context, func(store.Scope) error) error { return d.err }

var errTestReceiptCreate = errors.New("forced command receipt failure")

type failReceiptData struct{ inner workData }

func (d failReceiptData) View(ctx context.Context, fn func(store.Scope) error) error {
	return d.inner.View(ctx, fn)
}

func (d failReceiptData) Mutate(ctx context.Context, fn func(store.Scope) error) error {
	return d.inner.Mutate(ctx, func(sc store.Scope) error {
		clock, ok := sc.(store.TransactionClock)
		if !ok {
			return errors.New("test scope has no transaction clock")
		}
		return fn(failReceiptScope{Scope: sc, clock: clock})
	})
}

type failReceiptScope struct {
	store.Scope
	clock store.TransactionClock
}

func (s failReceiptScope) TransactionNow(ctx context.Context) (model.Timestamp, error) {
	return s.clock.TransactionNow(ctx)
}

func (s failReceiptScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	repo, err := s.Scope.Ext(kind)
	if err != nil || kind != workCommandKind {
		return repo, err
	}
	return failReceiptRepo{GenericRepo: repo}, nil
}

type failReceiptRepo struct{ store.GenericRepo }

func (failReceiptRepo) Create(context.Context, model.Record) (model.Record, error) {
	return nil, errTestReceiptCreate
}

func (d confinedWorkData) View(ctx context.Context, fn func(store.Scope) error) error {
	return d.inner.View(ctx, func(sc store.Scope) error {
		confined, err := store.ConfineWorkspace(ctx, sc, d.workspace)
		if err != nil {
			return err
		}
		return fn(confined)
	})
}

func (d confinedWorkData) Mutate(ctx context.Context, fn func(store.Scope) error) error {
	return d.inner.Mutate(ctx, func(sc store.Scope) error {
		confined, err := store.ConfineWorkspace(ctx, sc, d.workspace)
		if err != nil {
			return err
		}
		return fn(confined)
	})
}

func newWorkFixture(t *testing.T, dsn string, config func(*store.Config)) workFixture {
	t.Helper()
	m := New(WithWorkIdentityResolver(allowWorkIdentity{}), WithWorkContentGuard(allowWorkContent{}))
	cfg := store.Config{Engine: store.EngineSQLite, DSN: dsn, Debug: true}
	if config != nil {
		config(&cfg)
	}
	st, err := engine.Open(context.Background(), cfg, m.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	var tenant model.TenantID
	if err := st.System(context.Background(), func(sc store.SystemScope) error {
		if _, err := sc.EnsureSystemTenant(context.Background()); err != nil {
			return err
		}
		org, err := sc.CreateOrg(context.Background(), model.Org{Name: "work", Slug: "work", Status: model.StatusActive})
		tenant = org.TenantID
		return err
	}); err != nil {
		_ = st.Close()
		t.Fatal(err)
	}
	var workspace model.ID
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		ws, err := sc.DefaultWorkspace(context.Background())
		workspace = ws.ID
		return err
	}); err != nil {
		_ = st.Close()
		t.Fatal(err)
	}
	m.UseData(api.NewModuleData(st))
	return workFixture{
		m: m, st: st, tenant: tenant, workspace: workspace,
		principal: WorkPrincipal{ActorKind: model.ActorUser, ActorRef: model.NewID().String(), Actor: "user:" + model.NewID().String(), Admin: true},
	}
}

func baseCreateCommand(f workFixture, title string) WorkCommand {
	return WorkCommand{
		Command: "item.create", WorkspaceID: f.workspace, WorkKind: "implementation", Title: title,
		BriefMD: "Implement the durable behavior and record its test witness.", ContextRefs: []ContextRef{},
		Priority: "p1", OwnerKind: "user", OwnerRef: f.principal.ActorRef,
		ProvenanceKind: "human", ProvenanceRef: "test:work-k1",
		Acceptance:     []AcceptanceInput{{Key: "tests", Ordinal: 0, Statement: "The mutation test is green", Required: true}},
		IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPost, CommandScope: "POST /work-items",
	}
}

func applyCreate(t *testing.T, f workFixture, title string) CommandResult {
	t.Helper()
	result, err := f.m.Apply(context.Background(), f.tenant, f.principal, baseCreateCommand(f, title))
	if err != nil {
		t.Fatalf("create %q: %v", title, err)
	}
	return result
}

func workCount(t *testing.T, f workFixture, kind model.Kind) int {
	t.Helper()
	count := 0
	if err := f.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(kind)
		if err != nil {
			return err
		}
		rows, err := listAll(context.Background(), repo)
		count = len(rows)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestWorkWireCollectionsNeverEncodeNull(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		value  any
		fields []string
	}{
		{name: "page", value: WorkPage{}, fields: []string{"items"}},
		{name: "snapshot", value: WorkSnapshot{}, fields: []string{"acceptance", "dependencies"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			document, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatal(err)
			}
			var wire map[string]json.RawMessage
			if err := json.Unmarshal(document, &wire); err != nil {
				t.Fatal(err)
			}
			for _, field := range tc.fields {
				if string(wire[field]) != "[]" {
					t.Fatalf("%s = %s in %s, want []", field, wire[field], document)
				}
			}
		})
	}
}

func TestWorkListStoreFailureIsUnknownAndSeededNeighborReturnsRows(t *testing.T) {
	t.Parallel()

	t.Run("store outage is unknown", func(t *testing.T) {
		m := New()
		_, err := m.listWorkWithData(context.Background(), unavailableWorkData{err: store.ErrStoreUnavailable}, WorkQuery{})
		if we := asWorkError(err); we == nil || we.verdict != VerdictUnknown || we.code != "observation_unavailable" {
			t.Fatalf("list store outage = %v, want NO_HE_PODIDO_MIRAR/observation_unavailable", err)
		}
	})

	t.Run("seeded store returns row", func(t *testing.T) {
		f := newWorkFixture(t, ":memory:", nil)
		defer f.st.Close()
		created := applyCreate(t, f, "visible list neighbor")
		page, err := f.m.listWorkWithData(context.Background(), f.m.workData(f.tenant), WorkQuery{})
		if err != nil || len(page.Items) != 1 || page.Items[0].ID != created.ResultID {
			t.Fatalf("seeded list = %#v, %v", page, err)
		}
	})
}

func TestWorkCheckAggregationPreservesThreeOutcomes(t *testing.T) {
	t.Parallel()

	t.Run("unknown is not clean", func(t *testing.T) {
		verdict, code := aggregateChecks([]WorkCheck{
			{Name: "syntax", Verdict: VerdictClean},
			{Name: "clock", Verdict: VerdictUnknown},
		})
		if verdict != VerdictUnknown || code != "clock" {
			t.Fatalf("aggregate = %s/%s, want %s/clock", verdict, code, VerdictUnknown)
		}
	})
	t.Run("all observed checks are clean", func(t *testing.T) {
		verdict, code := aggregateChecks([]WorkCheck{{Name: "syntax", Verdict: VerdictClean}})
		if verdict != VerdictClean || code != "ok" {
			t.Fatalf("aggregate = %s/%s, want %s/ok", verdict, code, VerdictClean)
		}
	})
	t.Run("broken takes precedence", func(t *testing.T) {
		verdict, code := aggregateChecks([]WorkCheck{
			{Name: "clock", Verdict: VerdictUnknown},
			{Name: "policy", Verdict: VerdictBroken},
		})
		if verdict != VerdictBroken || code != "policy" {
			t.Fatalf("aggregate = %s/%s, want %s/policy", verdict, code, VerdictBroken)
		}
	})
}

func TestWorkCreateNormalizesOmittedContextRefsToArray(t *testing.T) {
	t.Parallel()

	f := newWorkFixture(t, ":memory:", nil)
	defer f.st.Close()
	cmd := baseCreateCommand(f, "omitted context refs")
	cmd.ContextRefs = nil
	created, err := f.m.Apply(context.Background(), f.tenant, f.principal, cmd)
	if err != nil {
		t.Fatalf("create with omitted context_refs: %v", err)
	}
	snapshot, err := f.m.Get(context.Background(), f.tenant, f.principal, created.ResultID)
	if err != nil {
		t.Fatal(err)
	}
	if string(snapshot.Item.ContextRefs) != "[]" {
		t.Fatalf("stored context_refs = %s, want []", snapshot.Item.ContextRefs)
	}
}

func TestWorkValidatePlanApplyReplayAndNoWriteDirections(t *testing.T) {
	t.Parallel()

	f := newWorkFixture(t, ":memory:", nil)
	defer f.st.Close()
	cmd := baseCreateCommand(f, "K1")

	assessment, err := f.m.Validate(context.Background(), f.tenant, f.principal, cmd)
	if err != nil || assessment.Verdict != VerdictClean {
		t.Fatalf("validate = %#v, %v", assessment, err)
	}
	plan, err := f.m.Plan(context.Background(), f.tenant, f.principal, cmd)
	if err != nil || plan.Verdict != VerdictClean || len(plan.PlanHash) != 64 {
		t.Fatalf("plan = %#v, %v", plan, err)
	}
	if got := workCount(t, f, workItemKind); got != 0 {
		t.Fatalf("validate/plan wrote %d items, want 0", got)
	}

	cmd.ExpectedPlanHash = plan.PlanHash
	first, err := f.m.Apply(context.Background(), f.tenant, f.principal, cmd)
	if err != nil || first.Code != "applied" {
		t.Fatalf("apply = %#v, %v", first, err)
	}
	replay, err := f.m.Apply(context.Background(), f.tenant, f.principal, cmd)
	if err != nil || !replay.Replayed || replay.CommandID != first.CommandID || replay.EventID != first.EventID {
		t.Fatalf("replay = %#v, %v; first %#v", replay, err, first)
	}
	if got := workCount(t, f, workItemKind); got != 1 {
		t.Fatalf("replay item rows = %d, want 1", got)
	}
	if got := workCount(t, f, workEventKind); got != 1 {
		t.Fatalf("replay event rows = %d, want 1", got)
	}

	changed := cmd
	changed.Title = "different semantic request"
	if _, err := f.m.Apply(context.Background(), f.tenant, f.principal, changed); asWorkError(err) == nil || asWorkError(err).code != "idempotency_key_reused" {
		t.Fatalf("same key with another body = %v, want idempotency_key_reused", err)
	}
	changed.IdempotencyKey = model.NewID().String()
	changed.ExpectedPlanHash = ""
	if _, err := f.m.Apply(context.Background(), f.tenant, f.principal, changed); err != nil {
		t.Fatalf("new key is a legitimate second command: %v", err)
	}
}

func TestWorkApplyIfMatchRejectsStaleAndAcceptsCurrent(t *testing.T) {
	t.Parallel()

	update := func(item CommandResult, title string) WorkCommand {
		return WorkCommand{
			Command: "item.update", WorkItemID: item.ResultID, Title: title,
			ExpectedVersion: item.Version, IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPatch,
		}
	}
	t.Run("stale version is rejected", func(t *testing.T) {
		f := newWorkFixture(t, ":memory:", nil)
		defer f.st.Close()
		created := applyCreate(t, f, "stale If-Match")
		if _, err := f.m.Apply(
			context.Background(), f.tenant, f.principal, update(created, "first update"),
		); err != nil {
			t.Fatal(err)
		}
		if _, err := f.m.Apply(
			context.Background(), f.tenant, f.principal, update(created, "stale update"),
		); err == nil {
			t.Fatal("stale If-Match applied")
		} else if we := asWorkError(err); we == nil || we.code != "version_mismatch" {
			t.Fatalf("stale If-Match = %v, want version_mismatch", err)
		}
	})

	t.Run("current version applies", func(t *testing.T) {
		f := newWorkFixture(t, ":memory:", nil)
		defer f.st.Close()
		created := applyCreate(t, f, "current If-Match")
		result, err := f.m.Apply(
			context.Background(), f.tenant, f.principal, update(created, "current update"),
		)
		if err != nil || result.Version != created.Version+1 {
			t.Fatalf("current If-Match = %#v, %v", result, err)
		}
	})
}

func TestWorkK2LocksAgentAuthorityBeforeClaimTouch(t *testing.T) {
	t.Parallel()

	t.Run("stale If-Match precedes authority and Claim", func(t *testing.T) {
		f := newWorkLeaseDomainFixture(t, "If-Match before authority and Claim")
		release := make(chan struct{})
		close(release)
		resolver := &blockingWorkAuthority{
			entered: make(chan struct{}), release: release,
		}
		f.m.UseWorkIdentityResolver(resolver)
		orderData := &claimTouchOrderModuleData{inner: f.m.data}
		f.m.data = orderData
		beforeClaim := readK2RecoveryClaim(t, f.workFixture, f.sid)
		beforeLease := getWorkLease(t, f)
		cmd := f.command("lease.acquire", f.ready.Version-1, 0)
		_, err := f.m.Apply(context.Background(), f.tenant, f.holder, cmd)
		if workErr := asWorkError(err); workErr == nil || workErr.code != "version_mismatch" {
			t.Fatalf("stale lease If-Match = %v, want version_mismatch", err)
		}
		select {
		case <-resolver.entered:
			t.Fatal("stale If-Match acquired agent authority")
		default:
		}
		afterClaim := readK2RecoveryClaim(t, f.workFixture, f.sid)
		afterLease := getWorkLease(t, f)
		if orderData.touches.Load() != 0 ||
			afterClaim.Int(model.ColVersion) != beforeClaim.Int(model.ColVersion) ||
			afterLease.Version != beforeLease.Version || afterLease.State != beforeLease.State {
			t.Fatalf("stale If-Match changed authority/Claim/WorkLease: touches=%d claim v%d->v%d lease %#v->%#v",
				orderData.touches.Load(), beforeClaim.Int(model.ColVersion),
				afterClaim.Int(model.ColVersion), beforeLease, afterLease)
		}
	})

	t.Run("barrier keeps Claim untouched until authority lock completes", func(t *testing.T) {
		f := newWorkLeaseDomainFixture(t, "authority before Claim barrier")
		resolver := &blockingWorkAuthority{
			entered: make(chan struct{}), release: make(chan struct{}),
		}
		f.m.UseWorkIdentityResolver(resolver)
		orderData := &claimTouchOrderModuleData{inner: f.m.data}
		f.m.data = orderData
		before := readK2RecoveryClaim(t, f.workFixture, f.sid)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		type outcome struct {
			result CommandResult
			err    error
		}
		done := make(chan outcome, 1)
		go func() {
			result, err := f.m.Apply(
				ctx, f.tenant, f.holder, f.command("lease.acquire", f.ready.Version, 0),
			)
			done <- outcome{result: result, err: err}
		}()
		select {
		case <-resolver.entered:
		case got := <-done:
			t.Fatalf("Apply crossed authority barrier: result=%#v err=%v", got.result, got.err)
		case <-ctx.Done():
			t.Fatalf("authority barrier was not reached: %v", ctx.Err())
		}
		if got := orderData.touches.Load(); got != 0 {
			t.Fatalf("Claim touches before agent authority lock completed = %d, want 0", got)
		}
		close(resolver.release)
		select {
		case got := <-done:
			if got.err != nil || got.result.Code != "applied" {
				t.Fatalf("Apply after authority release = %#v, %v", got.result, got.err)
			}
		case <-ctx.Done():
			t.Fatalf("Apply did not finish after authority release: %v", ctx.Err())
		}
		after := readK2RecoveryClaim(t, f.workFixture, f.sid)
		if got := orderData.touches.Load(); got != 1 ||
			after.Int(model.ColVersion) != before.Int(model.ColVersion)+1 {
			t.Fatalf("Claim touch calls/version after authority lock = %d/v%d, want 1/v%d",
				got, after.Int(model.ColVersion), before.Int(model.ColVersion)+1)
		}
	})

	t.Run("authority refusal rolls back Claim and WorkLease", func(t *testing.T) {
		f := newWorkLeaseDomainFixture(t, "authority refusal rollback")
		release := make(chan struct{})
		close(release)
		resolver := &blockingWorkAuthority{
			entered: make(chan struct{}), release: release, err: store.ErrConflict,
		}
		f.m.UseWorkIdentityResolver(resolver)
		orderData := &claimTouchOrderModuleData{inner: f.m.data}
		f.m.data = orderData
		beforeClaim := readK2RecoveryClaim(t, f.workFixture, f.sid)
		beforeLease := getWorkLease(t, f)
		_, err := f.m.Apply(
			context.Background(), f.tenant, f.holder,
			f.command("lease.acquire", f.ready.Version, 0),
		)
		if workErr := asWorkError(err); workErr == nil || workErr.code != "owner_ineligible" {
			t.Fatalf("authority refusal = %v, want owner_ineligible", err)
		}
		afterClaim := readK2RecoveryClaim(t, f.workFixture, f.sid)
		afterLease := getWorkLease(t, f)
		if afterClaim.Int(model.ColVersion) != beforeClaim.Int(model.ColVersion) ||
			afterLease.Version != beforeLease.Version || afterLease.State != beforeLease.State ||
			afterLease.Fence != beforeLease.Fence || orderData.touches.Load() != 0 {
			t.Fatalf("authority refusal changed Claim/WorkLease: claim v%d->v%d lease %#v->%#v",
				beforeClaim.Int(model.ColVersion), afterClaim.Int(model.ColVersion),
				beforeLease, afterLease)
		}
	})
}

func TestWorkIllegalDirectCompleteAndLegitimateReviewComplete(t *testing.T) {
	t.Parallel()

	f := newWorkFixture(t, ":memory:", nil)
	defer f.st.Close()
	created := applyCreate(t, f, "FSM")
	illegal := WorkCommand{
		Command: "item.complete", WorkItemID: created.ResultID, ExpectedVersion: created.Version,
		IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPost,
	}
	if _, err := f.m.Apply(context.Background(), f.tenant, f.principal, illegal); asWorkError(err) == nil || asWorkError(err).code != "illegal_transition" {
		t.Fatalf("draft -> completed = %v, want illegal_transition", err)
	}

	var review model.Record
	if err := f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
		items, _ := sc.Ext(workItemKind)
		item, err := items.Get(context.Background(), created.ResultID)
		if err != nil {
			return err
		}
		criteria, _ := sc.Ext(workAcceptanceKind)
		rows, err := listAll(context.Background(), criteria, model.Filter{Column: colWorkItemID, Op: model.OpEq, Value: created.ResultID.String()})
		if err != nil {
			return err
		}
		rows[0][colAccState] = "passed"
		rows[0][colAccEvidenceRef] = "job:test-work-fsm"
		rows[0][colAccEvidenceHash] = hashBytes([]byte("green"))
		rows[0][colAccVerifiedByKind], rows[0][colAccVerifiedByRef] = model.ActorUser, f.principal.ActorRef
		rows[0][colAccVerifiedAt] = model.SystemClock{}.Now().String()
		if _, err := criteria.Update(context.Background(), rows[0]); err != nil {
			return err
		}
		item[colWorkStatus], item[colWorkReadyAt] = "ready", model.SystemClock{}.Now().String()
		item, err = items.Update(context.Background(), item)
		if err != nil {
			return err
		}
		item[colWorkStatus], item[colWorkStartedAt] = "active", model.SystemClock{}.Now().String()
		item, err = items.Update(context.Background(), item)
		if err != nil {
			return err
		}
		item[colWorkStatus], item[colWorkReviewAt] = "review", model.SystemClock{}.Now().String()
		review, err = items.Update(context.Background(), item)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	legitimate := illegal
	legitimate.ExpectedVersion = review.Int(model.ColVersion)
	legitimate.IdempotencyKey = model.NewID().String()
	result, err := f.m.Apply(context.Background(), f.tenant, f.principal, legitimate)
	if err != nil || result.Status != "completed" {
		t.Fatalf("review -> completed with required evidence = %#v, %v", result, err)
	}
}

func TestWorkOwnerEpochChangesOnlyWithOwner(t *testing.T) {
	t.Parallel()

	t.Run("assignment increments", func(t *testing.T) {
		f := newWorkFixture(t, ":memory:", nil)
		defer f.st.Close()
		created := applyCreate(t, f, "owner assignment")
		_, err := f.m.Apply(context.Background(), f.tenant, f.principal, WorkCommand{
			Command: "item.assign", WorkItemID: created.ResultID,
			OwnerKind: "agent", OwnerRef: "agent:next",
			ExpectedVersion: created.Version, IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPost,
		})
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := f.m.Get(context.Background(), f.tenant, f.principal, created.ResultID)
		if err != nil || snapshot.Item.OwnerEpoch != 2 {
			t.Fatalf("assigned owner epoch = %d, %v; want 2", snapshot.Item.OwnerEpoch, err)
		}
	})

	t.Run("non-owner update preserves epoch", func(t *testing.T) {
		f := newWorkFixture(t, ":memory:", nil)
		defer f.st.Close()
		created := applyCreate(t, f, "owner-preserving update")
		_, err := f.m.Apply(context.Background(), f.tenant, f.principal, WorkCommand{
			Command: "item.update", WorkItemID: created.ResultID, Title: "owner title only",
			ExpectedVersion: created.Version, IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPatch,
		})
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := f.m.Get(context.Background(), f.tenant, f.principal, created.ResultID)
		if err != nil || snapshot.Item.OwnerEpoch != 1 {
			t.Fatalf("title update owner epoch = %d, %v; want unchanged 1", snapshot.Item.OwnerEpoch, err)
		}
	})
}

func TestWorkDependencyCycleAndIndependentNoTrigger(t *testing.T) {
	t.Parallel()

	f := newWorkFixture(t, ":memory:", nil)
	defer f.st.Close()
	a, b := applyCreate(t, f, "A"), applyCreate(t, f, "B")
	c, d := applyCreate(t, f, "C"), applyCreate(t, f, "D")
	add := func(from CommandResult, to model.ID) error {
		_, err := f.m.Apply(context.Background(), f.tenant, f.principal, WorkCommand{
			Command: "dependency.add", WorkItemID: from.ResultID, DependsOnID: to,
			ExpectedVersion: from.Version, IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPost,
		})
		return err
	}
	if err := add(a, b.ResultID); err != nil {
		t.Fatal(err)
	}
	if err := add(b, a.ResultID); asWorkError(err) == nil || asWorkError(err).code != "dependency_cycle" {
		t.Fatalf("B -> A cycle = %v, want dependency_cycle", err)
	}
	if err := add(c, d.ResultID); err != nil {
		t.Fatalf("independent C -> D must remain legal: %v", err)
	}
}

func TestWorkOutboxSettlesOnlyAfterDurableSink(t *testing.T) {
	t.Parallel()

	f := newWorkFixture(t, ":memory:", nil)
	defer f.st.Close()
	sink := &recordingWorkSink{err: errors.New("offline")}
	f.m.UseWorkEventSink(sink)
	created := applyCreate(t, f, "outbox")
	if created.EventID.IsZero() {
		t.Fatal("apply returned no durable event id")
	}
	assertOutboxState(t, f, "pending")
	sink.mu.Lock()
	sink.err = nil
	sink.mu.Unlock()
	// The failed delivery schedules a real retry backoff. Move that durable
	// schedule forward explicitly so this test does not sleep while still
	// exercising a second claim and delivery.
	if err := f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(workOutboxKind)
		if err != nil {
			return err
		}
		rows, err := listAll(context.Background(), repo)
		if err != nil {
			return err
		}
		rows[0][colOutboxNextAttemptAt] = model.NewTimestamp(time.Unix(0, 0).UTC()).String()
		_, err = repo.Update(context.Background(), rows[0])
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.m.DrainWorkOutbox(context.Background(), f.tenant, 10); err != nil {
		t.Fatal(err)
	}
	assertOutboxState(t, f, "published")
	if len(sink.events) != 1 || sink.events[0].EventID != created.EventID {
		t.Fatalf("sink events = %#v, want stable %s", sink.events, created.EventID)
	}
	if len(sink.attempts) != 2 || sink.attempts[0].EventID != created.EventID ||
		sink.attempts[1].EventID != created.EventID {
		t.Fatalf("sink attempts did not retain stable event id %s: %#v", created.EventID, sink.attempts)
	}
}

func TestWorkEventIsNotPublishedBeforeTheSourceCommit(t *testing.T) {
	t.Parallel()

	f := newWorkFixture(t, ":memory:", nil)
	defer f.st.Close()
	sink := &recordingWorkSink{}
	f.m.UseWorkEventSink(sink)
	cmd := baseCreateCommand(f, "transaction that must roll back")

	_, err := f.m.applyWithData(
		context.Background(), failReceiptData{inner: f.m.workData(f.tenant)},
		f.tenant, f.principal, cmd,
	)
	if !errors.Is(err, errTestReceiptCreate) {
		t.Fatalf("forced receipt failure = %v", err)
	}
	if len(sink.attempts) != 0 {
		t.Fatalf("sink observed rolled-back event: %#v", sink.attempts)
	}
	for _, kind := range []model.Kind{workItemKind, workCommandKind, workEventKind, workOutboxKind} {
		if got := workCount(t, f, kind); got != 0 {
			t.Fatalf("rolled-back %s rows = %d", kind, got)
		}
	}

	cmd.IdempotencyKey = model.NewID().String()
	committed, err := f.m.Apply(context.Background(), f.tenant, f.principal, cmd)
	if err != nil || committed.EventID.IsZero() || len(sink.events) != 1 || sink.events[0].EventID != committed.EventID {
		t.Fatalf("committed neighbor = %#v, events=%#v, err=%v", committed, sink.events, err)
	}
}

func TestWorkEventPayloadUsesTheDocumentedBoundedProjection(t *testing.T) {
	t.Parallel()

	f := newWorkFixture(t, ":memory:", nil)
	defer f.st.Close()
	sink := &recordingWorkSink{}
	f.m.UseWorkEventSink(sink)
	created := applyCreate(t, f, "content that must not enter the event")
	if len(sink.events) != 1 {
		t.Fatalf("durable events = %d, want 1", len(sink.events))
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(sink.events[0].Payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	wantKeys := []string{
		"command", "result_kind", "result_id", "workspace_id",
		"work_item_id", "status", "owner_epoch", "event_seq",
	}
	if len(payload) != len(wantKeys) {
		t.Fatalf("payload keys = %v, want exactly %v", payload, wantKeys)
	}
	for _, key := range wantKeys {
		if _, ok := payload[key]; !ok {
			t.Errorf("payload is missing documented key %q: %s", key, sink.events[0].Payload)
		}
	}
	var fact struct {
		Command     string   `json:"command"`
		ResultKind  string   `json:"result_kind"`
		ResultID    model.ID `json:"result_id"`
		WorkspaceID model.ID `json:"workspace_id"`
		WorkItemID  model.ID `json:"work_item_id"`
		Status      string   `json:"status"`
		OwnerEpoch  int64    `json:"owner_epoch"`
		EventSeq    int64    `json:"event_seq"`
	}
	if err := json.Unmarshal(sink.events[0].Payload, &fact); err != nil {
		t.Fatalf("decode typed payload: %v", err)
	}
	if fact.Command != "item.create" || fact.ResultKind != string(workItemKind) ||
		fact.ResultID != created.ResultID || fact.WorkspaceID != f.workspace ||
		fact.WorkItemID != created.ResultID || fact.Status != "draft" ||
		fact.OwnerEpoch != 1 || fact.EventSeq != 1 {
		t.Fatalf("payload fact = %#v", fact)
	}
}

func TestWorkDistinctCommandsHaveDistinctStoredEventIDs(t *testing.T) {
	t.Parallel()

	f := newWorkFixture(t, ":memory:", nil)
	defer f.st.Close()
	first := applyCreate(t, f, "first distinct event")
	second := applyCreate(t, f, "second distinct event")
	if first.EventID.IsZero() || second.EventID.IsZero() || first.EventID == second.EventID {
		t.Fatalf("real command event ids = %s and %s", first.EventID, second.EventID)
	}
}

func TestWorkCommandEventTypesCoverEveryK1Mutation(t *testing.T) {
	t.Parallel()

	tests := map[string][]string{
		"work.item.created":       {"item.create"},
		"work.item.transitioned":  {"item.update", "item.ready", "item.block", "item.unblock", "item.submit", "item.complete", "item.fail", "item.cancel", "item.archive"},
		"work.owner.changed":      {"item.assign"},
		"work.dependency.changed": {"dependency.add", "dependency.remove"},
		"work.acceptance.changed": {"acceptance.add", "acceptance.update", "acceptance.evaluate"},
		"work.decision.recorded":  {"decision.set", "decision.supersede", "decision.revoke"},
	}
	seen := map[string]bool{}
	for want, commands := range tests {
		for _, command := range commands {
			if seen[command] {
				t.Fatalf("command %q appears twice", command)
			}
			seen[command] = true
			if got := workCommandEvent(command); got != want {
				t.Errorf("workCommandEvent(%q) = %q, want %q", command, got, want)
			}
		}
	}
	if len(seen) != 19 {
		t.Fatalf("covered %d K1 mutations, want 19", len(seen))
	}
}

func TestWorkApplyOutboxNudgePreservesExplicitWorkspaceHandle(t *testing.T) {
	t.Parallel()

	f := newWorkFixture(t, ":memory:", nil)
	defer f.st.Close()
	defaultWorkspace, otherWorkspace := workSchemaWorkspaces(t, context.Background(), f.m, f.tenant)
	if defaultWorkspace != f.workspace {
		t.Fatalf("fixture workspace = %s, default = %s", f.workspace, defaultWorkspace)
	}

	// Leave an older pending event in the other workspace. A tenant-wide nudge
	// would claim it first because the generic repository orders by stable id.
	otherCommand := baseCreateCommand(f, "other workspace first")
	otherCommand.WorkspaceID = otherWorkspace
	if _, err := f.m.Apply(context.Background(), f.tenant, f.principal, otherCommand); err != nil {
		t.Fatalf("seed other workspace: %v", err)
	}
	local := applyCreate(t, f, "confined workspace second")

	sink := &recordingWorkSink{}
	f.m.UseWorkEventSink(sink)
	data := confinedWorkData{inner: f.m.workData(f.tenant), workspace: f.workspace}
	_, err := f.m.applyWithData(context.Background(), data, f.tenant, f.principal, WorkCommand{
		Command: "item.assign", WorkItemID: local.ResultID,
		OwnerKind: "agent", OwnerRef: "agent:confined",
		ExpectedVersion: local.Version, IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPost,
	})
	if err != nil {
		t.Fatalf("confined apply: %v", err)
	}
	if len(sink.events) != 1 || sink.events[0].WorkspaceID != f.workspace {
		t.Fatalf("nudge events = %#v, want one event from workspace %s", sink.events, f.workspace)
	}
}

func TestWorkOutboxRecoversOnlyExpiredDeliveries(t *testing.T) {
	t.Parallel()

	f := newWorkFixture(t, ":memory:", nil)
	defer f.st.Close()
	expired := applyCreate(t, f, "expired outbox claim")
	live := applyCreate(t, f, "live outbox claim")
	if err := f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(workOutboxKind)
		if err != nil {
			return err
		}
		rows, err := listAll(context.Background(), repo)
		if err != nil {
			return err
		}
		for _, row := range rows {
			row[colOutboxState], row[colOutboxAttempts] = "delivering", int64(1)
			row[colOutboxClaimOwner] = "node-before-restart"
			if row.String(colOutboxEventID) == expired.EventID.String() {
				row[colOutboxClaimUntil] = model.NewTimestamp(time.Unix(0, 0).UTC()).String()
			} else {
				row[colOutboxClaimUntil] = model.NewTimestamp(time.Now().UTC().Add(time.Hour)).String()
			}
			if _, err := repo.Update(context.Background(), row); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sink := &recordingWorkSink{}
	f.m.UseWorkEventSink(sink)
	if err := f.m.DrainWorkOutbox(context.Background(), f.tenant, 10); err != nil {
		t.Fatal(err)
	}
	if len(sink.events) != 1 || sink.events[0].EventID != expired.EventID {
		t.Fatalf("recovered events = %#v, want only expired %s (live %s)", sink.events, expired.EventID, live.EventID)
	}
	if err := f.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(workOutboxKind)
		if err != nil {
			return err
		}
		rows, err := listAll(context.Background(), repo)
		if err != nil {
			return err
		}
		for _, row := range rows {
			want := "delivering"
			if row.String(colOutboxEventID) == expired.EventID.String() {
				want = "published"
			}
			if row.String(colOutboxState) != want {
				t.Fatalf("event %s state = %s, want %s", row.String(colOutboxEventID), row.String(colOutboxState), want)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkOutboxFailureIsReturnedAndExhaustionCreatesFinding(t *testing.T) {
	t.Parallel()

	f := newWorkFixture(t, ":memory:", nil)
	defer f.st.Close()
	sinkErr := errors.New("durable intake offline")
	sink := &recordingWorkSink{err: sinkErr}
	f.m.UseWorkEventSink(sink)
	created := applyCreate(t, f, "dead-letter evidence")
	if err := f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(workOutboxKind)
		if err != nil {
			return err
		}
		rows, err := listAll(context.Background(), repo)
		if err != nil {
			return err
		}
		rows[0][colOutboxAttempts] = int64(9)
		rows[0][colOutboxNextAttemptAt] = model.NewTimestamp(time.Unix(0, 0).UTC()).String()
		_, err = repo.Update(context.Background(), rows[0])
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.m.DrainWorkOutbox(context.Background(), f.tenant, 1); !errors.Is(err, sinkErr) {
		t.Fatalf("failed drain = %v, want sink error", err)
	}
	assertOutboxState(t, f, "dead_letter")
	if err := f.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
		findings, _, err := sc.Findings().List(context.Background(), model.Query{Limit: 100})
		if err != nil {
			return err
		}
		if len(findings) != 1 || findings[0].Kind != "delivery" ||
			findings[0].SubjectID != created.EventID || findings[0].Severity != model.SeverityHigh {
			t.Fatalf("dead-letter findings = %#v", findings)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func assertOutboxState(t *testing.T, f workFixture, want string) {
	t.Helper()
	if err := f.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
		repo, _ := sc.Ext(workOutboxKind)
		rows, err := listAll(context.Background(), repo)
		if err != nil {
			return err
		}
		if len(rows) != 1 || rows[0].String(colOutboxState) != want {
			t.Fatalf("outbox = %#v, want one %s", rows, want)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkPersistsAcrossRestart(t *testing.T) {
	t.Parallel()

	dsn := filepath.Join(t.TempDir(), "work.db")
	f := newWorkFixture(t, dsn, nil)
	created := applyCreate(t, f, "restart")
	predecessor := applyCreate(t, f, "restart predecessor")
	added, err := f.m.Apply(context.Background(), f.tenant, f.principal, WorkCommand{
		Command: "dependency.add", WorkItemID: created.ResultID, DependsOnID: predecessor.ResultID,
		ExpectedVersion: created.Version, IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPost,
	})
	if err != nil {
		t.Fatal(err)
	}
	removed, err := f.m.Apply(context.Background(), f.tenant, f.principal, WorkCommand{
		Command: "dependency.remove", WorkItemID: created.ResultID, TargetID: added.ResultID,
		ExpectedVersion: added.Version, IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodDelete,
	})
	if err != nil {
		t.Fatal(err)
	}
	decision := decisionCommand(created.ResultID, "decision.set", "restart-proof")
	decision.ExpectedVersion = removed.Version
	decided, err := f.m.Apply(context.Background(), f.tenant, f.principal, decision)
	if err != nil {
		t.Fatal(err)
	}
	active := forceWorkActive(t, f, CommandResult{ResultID: created.ResultID, Version: decided.Version})
	criterion := workCriterion(t, f, created.ResultID, "tests")
	principal, evaluate := withWorkExecutionLease(t, f, f.principal, WorkCommand{
		Command: "acceptance.evaluate", WorkItemID: created.ResultID, CriterionID: recordID(criterion),
		Acceptance: []AcceptanceInput{{
			State: "passed", EvidenceRef: "job:restart-green", EvidenceHash: hexHash(hashBytes([]byte("restart-green"))),
		}},
		ExpectedVersion: active.Int(model.ColVersion), IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPatch,
	})
	if _, err := f.m.Apply(context.Background(), f.tenant, principal, evaluate); err != nil {
		t.Fatal(err)
	}
	if err := f.st.Close(); err != nil {
		t.Fatal(err)
	}

	m2 := New(WithWorkIdentityResolver(allowWorkIdentity{}), WithWorkContentGuard(allowWorkContent{}))
	st2, err := engine.Open(context.Background(), store.Config{Engine: store.EngineSQLite, DSN: dsn, Debug: true}, m2.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	m2.UseData(api.NewModuleData(st2))
	snapshot, err := m2.Get(context.Background(), f.tenant, f.principal, created.ResultID)
	if err != nil || snapshot.Item.Title != "restart" || snapshot.Item.OwnerRef != f.principal.ActorRef ||
		snapshot.Item.Status != "active" || snapshot.Item.BriefHash != hexHash(hashBytes([]byte(snapshot.Item.BriefMD))) ||
		len(snapshot.Acceptance) != 1 || len(snapshot.Dependencies) != 1 {
		t.Fatalf("after restart = %#v, %v", snapshot, err)
	}
	var acceptance, dependency map[string]any
	if err := json.Unmarshal(snapshot.Acceptance[0], &acceptance); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(snapshot.Dependencies[0], &dependency); err != nil {
		t.Fatal(err)
	}
	if acceptance[colAccState] != "passed" || acceptance[colAccEvidenceRef] != "job:restart-green" ||
		dependency[colDepActive] != false {
		t.Fatalf("restart children: acceptance=%#v dependency=%#v", acceptance, dependency)
	}
	page, err := m2.List(context.Background(), f.tenant, f.principal, WorkQuery{Filters: map[string]string{
		"owner_ref": f.principal.ActorRef, "status": "active",
	}})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != created.ResultID {
		t.Fatalf("restart list = %#v, %v", page, err)
	}
	if err := st2.View(context.Background(), f.tenant, func(sc store.Scope) error {
		decisions, err := sc.Ext(workDecisionKind)
		if err != nil {
			return err
		}
		rows, err := listAll(context.Background(), decisions,
			model.Filter{Column: colWorkItemID, Op: model.OpEq, Value: created.ResultID.String()},
			model.Filter{Column: colDecisionKey, Op: model.OpEq, Value: "restart-proof"},
		)
		if err != nil {
			return err
		}
		heads, err := sc.Ext(workDecisionHeadKind)
		if err != nil {
			return err
		}
		headRows, err := listAll(context.Background(), heads,
			model.Filter{Column: colWorkItemID, Op: model.OpEq, Value: created.ResultID.String()},
			model.Filter{Column: colDecisionKey, Op: model.OpEq, Value: "restart-proof"},
		)
		if err != nil {
			return err
		}
		if len(rows) != 1 || len(headRows) != 1 ||
			headRows[0].String(colDecisionCurrentID) != rows[0].String(model.ColID) {
			t.Fatalf("restart decision/head: decisions=%#v heads=%#v", rows, headRows)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
