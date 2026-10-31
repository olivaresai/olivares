// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

type guardianRepoFault struct {
	store.GenericRepo
	remaining   int
	err         error
	createErr   error
	updates     int
	onUpdate    func()
	onCreate    func()
	listResults []model.Record
	listCalls   int
	createCalls int
	emptyFirst  bool
}

func (r *guardianRepoFault) Create(ctx context.Context, rec model.Record) (model.Record, error) {
	r.createCalls++
	if r.onCreate != nil {
		r.onCreate()
	}
	if r.createErr != nil {
		return nil, r.createErr
	}
	return r.GenericRepo.Create(ctx, rec)
}

func (r *guardianRepoFault) List(ctx context.Context, q model.Query) ([]model.Record, model.Page, error) {
	if r.emptyFirst && r.listCalls == 0 {
		r.listCalls++
		return nil, model.Page{}, nil
	}
	if len(r.listResults) > 0 {
		i := r.listCalls
		if i >= len(r.listResults) {
			i = len(r.listResults) - 1
		}
		r.listCalls++
		return []model.Record{r.listResults[i]}, model.Page{}, nil
	}
	return r.GenericRepo.List(ctx, q)
}

func (r *guardianRepoFault) Update(ctx context.Context, rec model.Record) (model.Record, error) {
	r.updates++
	if r.onUpdate != nil {
		r.onUpdate()
	}
	if r.remaining != 0 {
		if r.remaining > 0 {
			r.remaining--
		}
		return nil, r.err
	}
	return r.GenericRepo.Update(ctx, rec)
}

type guardianTransitionScope struct {
	store.Scope
	repo      *guardianRepoFault
	audit     *guardianAuditFault
	faultKind model.Kind
	advance   time.Duration
	extErr    error
}

type guardianAuditFault struct {
	store.AuditLog
	remaining int
}

func (a *guardianAuditFault) Append(ctx context.Context, draft model.AuditDraft) (model.AuditEvent, error) {
	if a.remaining > 0 && draft.Action == "governance.guardian.execute" {
		a.remaining--
		return model.AuditEvent{}, store.ErrConflict
	}
	return a.AuditLog.Append(ctx, draft)
}

func (s *guardianTransitionScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	if s.extErr != nil {
		return nil, s.extErr
	}
	repo, err := s.Scope.Ext(kind)
	if (s.faultKind == "" && kind == guardianActionKind) || s.faultKind == kind {
		s.repo.GenericRepo = repo
		return s.repo, err
	}
	return repo, err
}

func (s *guardianTransitionScope) Audit() store.AuditLog {
	if s.audit != nil {
		s.audit.AuditLog = s.Scope.Audit()
		return s.audit
	}
	return s.Scope.Audit()
}

func (s *guardianTransitionScope) LockTransaction(ctx context.Context, key string) error {
	return s.Scope.(store.TransactionLocker).LockTransaction(ctx, key)
}

type advancingGuardianScope struct {
	*guardianTransitionScope
	clock *intClock
}

func (s *advancingGuardianScope) LockTransaction(ctx context.Context, key string) error {
	s.clock.mu.Lock()
	s.clock.t = s.clock.t.Add(s.advance)
	s.clock.mu.Unlock()
	return s.guardianTransitionScope.LockTransaction(ctx, key)
}

func useGuardianTransitionScope(f *guardianChangeRecordFixture, fault *guardianRepoFault, advance time.Duration) {
	f.m.UseData(cedarEpochModuleData{st: f.st, mutateWrap: func(sc store.Scope) store.Scope {
		base := &guardianTransitionScope{Scope: sc, repo: fault, advance: advance}
		if advance != 0 {
			return &advancingGuardianScope{guardianTransitionScope: base, clock: f.clock}
		}
		return base
	}})
}

func useGuardianAuditConflict(f *guardianChangeRecordFixture) {
	fault := &guardianAuditFault{remaining: 1}
	f.m.UseData(cedarEpochModuleData{st: f.st, mutateWrap: func(sc store.Scope) store.Scope {
		return &guardianTransitionScope{Scope: sc, repo: &guardianRepoFault{}, audit: fault}
	}})
}

func TestGuardianContainmentRollsBackASpendWhenTheActFails(t *testing.T) {
	f := newGuardianChangeRecordFixture(t, "security.guardian.stop_agent")
	useGuardianTransitionScope(f, &guardianRepoFault{remaining: -1, err: errors.New("injected guardian update failure")}, 0)
	res, err := f.m.GuardianSweep(context.Background(), f.tenant)
	if err == nil || !strings.Contains(err.Error(), "injected guardian update failure") {
		t.Fatalf("sweep with a failing act = %+v %v", res, err)
	}
	if got := f.approval().String(colConsumedBy); got != "" {
		t.Fatalf("approval was spent despite the rolled-back act: %q", got)
	}
	if got := f.actions()[0].String(colGAStatus); got != gaStatusPending {
		t.Fatalf("action after rollback = %q, want pending", got)
	}
	if got := len(f.stops()); got != 0 {
		t.Fatalf("rollback left %d stops", got)
	}

	f.m.UseData(api.NewModuleData(f.st))
	res, err = f.m.GuardianSweep(context.Background(), f.tenant)
	if err != nil || res.Executed != 1 {
		t.Fatalf("sweep after removing the injected failure = %+v %v", res, err)
	}
}

func TestGuardianContainmentRollsBackASpendWhenQuarantineConflicts(t *testing.T) {
	f := newGuardianChangeRecordFixtureForAction(t, "security.guardian.quarantine_nhi", gaActionQuarantineNHI)
	f.m.UseData(cedarEpochModuleData{st: f.st, mutateWrap: func(sc store.Scope) store.Scope {
		return &guardianTransitionScope{Scope: sc, repo: &guardianRepoFault{remaining: -1, err: store.ErrConflict}, faultKind: nhiLifecycleKind}
	}})
	res, err := f.m.GuardianSweep(context.Background(), f.tenant)
	if !isConflict(err) || res.Executed != 0 {
		t.Fatalf("sweep with a quarantine conflict = %+v %v, want no execution and the conflict", res, err)
	}
	if got := f.approval().String(colConsumedBy); got != "" {
		t.Fatalf("approval was spent despite the quarantine conflict: %q", got)
	}
	if got := f.actions()[0].String(colGAStatus); got != gaStatusPending {
		t.Fatalf("action after the quarantine conflict = %q, want pending", got)
	}
	if got := f.findingKinds()[findingGuardianExecuted]; got != 0 {
		t.Fatalf("execution findings after the rolled-back quarantine = %d, want none", got)
	}

	f.m.UseData(api.NewModuleData(f.st))
	res, err = f.m.GuardianSweep(context.Background(), f.tenant)
	if err != nil || res.Executed != 1 {
		t.Fatalf("quarantine after removing the conflict = %+v %v, want one execution", res, err)
	}
}

func TestGuardianContainmentPublishesOnlyTheCommittedAttempt(t *testing.T) {
	f := newGuardianChangeRecordFixture(t, "security.guardian.stop_agent")
	useGuardianAuditConflict(f)
	res, err := f.m.GuardianSweep(context.Background(), f.tenant)
	if err != nil || res.Executed != 1 {
		t.Fatalf("sweep after one conflicting attempt = %+v %v, want one execution", res, err)
	}
	if got := f.findingKinds()[findingGuardianExecuted]; got != 1 {
		t.Fatalf("execution findings = %d, want one for the committed attempt", got)
	}
	if got := len(f.stops()); got != 1 {
		t.Fatalf("stops = %d, want one", got)
	}
}

func TestGuardianContainmentChecksApprovalExpiryAfterTheFence(t *testing.T) {
	f := newGuardianChangeRecordFixture(t, "security.guardian.stop_agent")
	approvalID := f.actions()[0].String(colGAApprovalID)
	if err := f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(approvalKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(context.Background(), model.ID(approvalID))
		if err != nil {
			return err
		}
		rec[colExpiresAt] = model.NewTimestamp(intBase.Add(2 * time.Minute)).String()
		_, err = repo.Update(context.Background(), rec)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	useGuardianTransitionScope(f, &guardianRepoFault{}, time.Minute)
	res, err := f.m.GuardianSweep(context.Background(), f.tenant)
	if err != nil || res.Expired != 1 || f.actions()[0].String(colGAStatus) != gaStatusExpired {
		t.Fatalf("sweep with expiry crossing the fence = %+v %v action=%+v, want one terminal expiry", res, err, f.actions()[0])
	}
	if got := len(f.stops()); got != 0 {
		t.Fatalf("expired containment executed: %d stops", got)
	}
}

func TestGuardianContainmentRefreshesTheChangeWindowAfterTheFence(t *testing.T) {
	f := newGuardianChangeRecordFixture(t, "security.guardian.stop_agent")
	f.end = intBase.Add(3 * time.Minute)
	useGuardianTransitionScope(f, &guardianRepoFault{remaining: 1, err: store.ErrConflict}, time.Minute)
	res, err := f.m.GuardianSweep(context.Background(), f.tenant)
	if err == nil || !strings.Contains(err.Error(), "window") || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("sweep with the change window crossing the fence = %+v %v, want a closed-window refusal", res, err)
	}
	if res.Executed != 0 || len(f.stops()) != 0 {
		t.Fatalf("containment executed outside the change window: %+v stops=%+v", res, f.stops())
	}
	if got := f.approval().String(colStatus); got != statusCanceled {
		t.Fatalf("approval after the closed-window refusal = %q, want canceled", got)
	}
}

func TestGuardianSweepContinuesAfterATransientChangeRecordFailure(t *testing.T) {
	f := newGuardianChangeRecordFixture(t, "security.guardian.stop_agent")
	ctx := context.Background()
	if err := f.st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
		_, err := sc.Policies().Create(ctx, model.Policy{
			Name: "estate containment record", Kind: "approval", Enabled: true,
			Spec: map[string]any{
				"required_approvals": 1,
				"match":              map[string]any{"action": "security.guardian.stop_estate"},
				"change_record":      map[string]any{"system": "servicenow", "key": "CHG-OK"},
			},
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	f.createRule("estate containment", "estate_detected", "high", gaActionStopEstate, gaModeApproval)
	f.fire("estate_detected", sdkmodel.SeverityCritical, "agent", "estate-agent", "d-estate")
	var estate model.Record
	for _, action := range f.actions() {
		if action.String(colGAAction) == gaActionStopEstate {
			estate = action
		}
	}
	if estate == nil {
		t.Fatal("estate action was not queued")
	}
	if err := f.st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(approvalKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(ctx, model.ID(estate.String(colGAApprovalID)))
		if err != nil {
			return err
		}
		rec[colStatus] = statusApproved
		rec[colApproveCount] = int64(1)
		_, err = repo.Update(ctx, rec)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	f.errs = map[string]error{"CHG0030001": errors.New("change system down")}

	res, err := f.m.GuardianSweep(ctx, f.tenant)
	var refusal approvalDecisionError
	if !errors.As(err, &refusal) || refusal.Code != codeChangeRecordUnavailable || res.Executed != 1 {
		t.Fatalf("sweep with the first record down = %+v %v, want the later containment and the outage refusal", res, err)
	}
	for _, action := range f.actions() {
		switch action.String(colGAAction) {
		case gaActionStopAgent:
			if got := action.String(colGAStatus); got != gaStatusPending {
				t.Fatalf("unavailable action = %q, want pending", got)
			}
			// An outage refuses this sweep only: a revoked or spent grant would also
			// leave the action pending, so the grant itself is what the test checks.
			if approval := f.approvalByID(action.String(colGAApprovalID)); approval.String(colStatus) != statusApproved || approval.String(colConsumedBy) != "" {
				t.Fatalf("approval after the outage = %q consumed_by=%q, want approved and unspent", approval.String(colStatus), approval.String(colConsumedBy))
			}
		case gaActionStopEstate:
			if got := action.String(colGAStatus); got != gaStatusExecuted {
				t.Fatalf("later independent action = %q, want executed", got)
			}
		}
	}
	if got := len(f.stops()); got != 1 {
		t.Fatalf("later containment created %d stops, want one", got)
	}
}

func TestGuardianSweepReportsAnUnconvergedConflict(t *testing.T) {
	f := newGuardianChangeRecordFixture(t, "security.guardian.stop_agent")
	approvalID := model.ID(f.actions()[0].String(colGAApprovalID))
	if err := f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(approvalKind)
		if err != nil {
			return err
		}
		return repo.Delete(context.Background(), approvalID)
	}); err != nil {
		t.Fatal(err)
	}
	useGuardianTransitionScope(f, &guardianRepoFault{remaining: 1, err: store.ErrConflict}, 0)
	res, err := f.m.GuardianSweep(context.Background(), f.tenant)
	if err == nil || res.Failed != 0 || f.actions()[0].String(colGAStatus) != gaStatusPending {
		t.Fatalf("sweep with an unconverged conflict = %+v %v action=%+v, want an error without a committed counter", res, err, f.actions()[0])
	}

	f.m.UseData(api.NewModuleData(f.st))
	res, err = f.m.GuardianSweep(context.Background(), f.tenant)
	if err != nil || res.Failed != 1 || f.actions()[0].String(colGAStatus) != gaStatusFailed {
		t.Fatalf("sweep after the conflict clears = %+v %v action=%+v, want one committed failure", res, err, f.actions()[0])
	}
	if got := f.findingKinds()[findingGuardianFailed]; got != 1 {
		t.Fatalf("missing-approval failure findings = %d, want one", got)
	}
	executions := 0
	if err := f.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
		walker, ok := sc.Audit().(store.CanonicalWalker)
		if !ok {
			f.t.Fatal("the audit store has no canonical walker")
		}
		return walker.WalkCanonical(context.Background(), 1, func(e model.AuditEvent, meta string, _ []byte) error {
			if e.Action == "governance.guardian.execute" {
				executions++
				if !strings.Contains(meta, `"approval":"`+approvalID.String()+`"`) {
					f.t.Errorf("missing-approval audit meta = %s, want approval %s", meta, approvalID)
				}
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	if executions != 1 {
		t.Fatalf("missing-approval execution audits = %d, want one", executions)
	}
}

func TestGuardianContainmentRefusesMovedArmBetweenDiscoveryAndSpend(t *testing.T) {
	for _, tc := range []struct{ from, to string }{
		{from: gaActionStopAgent, to: gaActionQuarantineNHI},
		{from: gaActionQuarantineNHI, to: gaActionStopAgent},
	} {
		t.Run(tc.from+"_to_"+tc.to, func(t *testing.T) {
			f := newGuardianChangeRecordFixture(t, "")
			actionID := f.actions()[0].String(model.ColID)
			approvalID := f.actions()[0].String(colGAApprovalID)
			setArm := func(arm string) {
				t.Helper()
				if err := f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
					actionRepo, err := sc.Ext(guardianActionKind)
					if err != nil {
						return err
					}
					action, err := actionRepo.Get(context.Background(), model.ID(actionID))
					if err != nil {
						return err
					}
					action[colGAAction] = arm
					if _, err := actionRepo.Update(context.Background(), action); err != nil {
						return err
					}
					approvalRepo, err := sc.Ext(approvalKind)
					if err != nil {
						return err
					}
					approval, err := approvalRepo.Get(context.Background(), model.ID(approvalID))
					if err != nil {
						return err
					}
					approval[colAction] = guardianApprovalPrefix + arm
					_, err = approvalRepo.Update(context.Background(), approval)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			setArm(tc.from)
			f.onRead = func() { setArm(tc.to) }
			res, err := f.m.GuardianSweep(context.Background(), f.tenant)
			if err == nil || res.Executed != 0 || len(f.stops()) != 0 {
				t.Fatalf("sweep with a moved arm = %+v %v stops=%+v, want refusal and no containment", res, err, f.stops())
			}
			if got := f.approval().String(colConsumedBy); got != "" {
				t.Fatalf("approval was spent under a moved arm: %q", got)
			}
			if got := f.actions()[0].String(colGAStatus); got != gaStatusPending {
				t.Fatalf("action after the moved-arm refusal = %q, want pending", got)
			}
		})
	}
}

func TestGuardianMissingActionIsNotConvergence(t *testing.T) {
	f := newGuardianFixture(t)
	converged, err := f.m.guardianActionConverged(context.Background(), f.tenant, model.NewID())
	if err == nil || converged {
		t.Fatalf("missing action = %v %v, want an integrity error, not convergence", converged, err)
	}
}

func TestChangeRecordDecisionConflictRetryRefreshesTheState(t *testing.T) {
	f := newGuardianFixture(t)
	var approvalID model.ID
	if err := f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
		if _, err := sc.Policies().Create(context.Background(), model.Policy{
			Name: "decision change record", Kind: "approval", Enabled: true,
			Spec: map[string]any{
				"required_approvals": 1,
				"match":              map[string]any{"action": "gitpublish.push"},
				"change_record":      map[string]any{"system": "servicenow", "key": "CHG0030001"},
			},
		}); err != nil {
			return err
		}
		dto, err := f.m.openApprovalRecord(context.Background(), sc, model.ActorSystem, model.ActorSystem, "", createApprovalRequest{
			Action: "gitpublish.push", SubjectKind: "gitpublish.target", SubjectRef: "prod", Reason: "conflict refresh",
		}, 0, model.NewTimestamp(intBase))
		approvalID = model.ID(dto.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	state, first := "Scheduled", true
	f.m.UseChangeRecordReader(func(context.Context, model.TenantID, string, string, string) (ChangeRecord, error) {
		current := state
		if first {
			first = false
			state = "Canceled"
		}
		return ChangeRecord{State: current}, nil
	})
	fault := &guardianRepoFault{remaining: 1, err: store.ErrConflict}
	data := cedarEpochModuleData{st: f.st, mutateWrap: func(sc store.Scope) store.Scope {
		return &guardianTransitionScope{Scope: sc, repo: fault, faultKind: approvalKind}
	}}
	f.m.UseData(data)
	mc := api.ModuleContext{
		Tenant: f.tenant, Principal: auth.Principal{Kind: auth.KindUser, UserID: model.NewID(), CredID: model.NewID()},
		Data: api.NewScopedData(f.st, f.tenant),
	}
	_, err := f.m.decideApproval(context.Background(), approvalTenantData{data: data, tenant: f.tenant}, mc, approvalID, ApprovalDecisionRequest{Decision: "approve"})
	if err == nil || !strings.Contains(err.Error(), "Canceled") {
		t.Fatalf("decision after a Scheduled-to-Canceled conflict = %v, want the current refusal", err)
	}
	if fault.remaining != 0 {
		t.Fatal("the decision conflict was not injected")
	}
}

func TestChangeRecordCoversRechecksObservationWindow(t *testing.T) {
	spec := changeRecordSpec{System: "servicenow", Key: "CHG0030001"}
	seen := &changeRecordObservation{spec: spec, state: "Scheduled", end: intBase.Add(time.Minute)}
	if !changeRecordCovers(&spec, seen, model.NewTimestamp(intBase)) {
		t.Fatal("a valid observation is not covered")
	}
	if changeRecordCovers(&spec, seen, model.NewTimestamp(intBase.Add(2*time.Minute))) {
		t.Fatal("an observation whose window closed is still covered")
	}
}

func TestChangeRecordDecisionReanchorsTimeOnTransactionEntry(t *testing.T) {
	f := newGuardianFixture(t)
	var approvalID model.ID
	if err := f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
		if _, err := sc.Policies().Create(context.Background(), model.Policy{
			Name: "decision entry clock", Kind: "approval", Enabled: true,
			Spec: map[string]any{
				"required_approvals": 1,
				"match":              map[string]any{"action": "gitpublish.push"},
				"change_record":      map[string]any{"system": "servicenow", "key": "CHG0030001"},
			},
		}); err != nil {
			return err
		}
		dto, err := f.m.openApprovalRecord(context.Background(), sc, model.ActorSystem, model.ActorSystem, "", createApprovalRequest{
			Action: "gitpublish.push", SubjectKind: "gitpublish.target", SubjectRef: "prod", Reason: "entry clock",
		}, 0, model.NewTimestamp(intBase))
		approvalID = model.ID(dto.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	f.m.UseChangeRecordReader(func(context.Context, model.TenantID, string, string, string) (ChangeRecord, error) {
		return ChangeRecord{State: "Scheduled", End: intBase.Add(time.Hour)}, nil
	})
	calls := 0
	data := cedarEpochModuleData{st: f.st, mutateWrap: func(sc store.Scope) store.Scope {
		calls++
		if calls == 2 {
			f.clock.mu.Lock()
			f.clock.t = f.clock.t.Add(2 * time.Hour)
			f.clock.mu.Unlock()
		}
		return sc
	}}
	f.m.UseData(data)
	mc := api.ModuleContext{
		Tenant: f.tenant, Principal: auth.Principal{Kind: auth.KindUser, UserID: model.NewID(), CredID: model.NewID()},
		Data: api.NewScopedData(f.st, f.tenant),
	}
	_, err := f.m.decideApproval(context.Background(), approvalTenantData{data: data, tenant: f.tenant}, mc, approvalID, ApprovalDecisionRequest{Decision: "approve"})
	if err == nil || !strings.Contains(err.Error(), "window") || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("decision after the transaction-entry window closed = %v, want a closed-window refusal", err)
	}
}

func TestChangeRecordDecisionRefreshesAClosedWindow(t *testing.T) {
	f := newGuardianFixture(t)
	var approvalID model.ID
	if err := f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
		if _, err := sc.Policies().Create(context.Background(), model.Policy{
			Name: "decision change window", Kind: "approval", Enabled: true,
			Spec: map[string]any{
				"required_approvals": 1,
				"match":              map[string]any{"action": "gitpublish.push"},
				"change_record":      map[string]any{"system": "servicenow", "key": "CHG0030001"},
			},
		}); err != nil {
			return err
		}
		dto, err := f.m.openApprovalRecord(context.Background(), sc, model.ActorSystem, model.ActorSystem, "", createApprovalRequest{
			Action: "gitpublish.push", SubjectKind: "gitpublish.target", SubjectRef: "prod", Reason: "closed window",
		}, 0, model.NewTimestamp(intBase))
		approvalID = model.ID(dto.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	f.m.UseChangeRecordReader(func(context.Context, model.TenantID, string, string, string) (ChangeRecord, error) {
		return ChangeRecord{State: "Scheduled", End: intBase.Add(3 * time.Minute)}, nil
	})
	fault := &guardianRepoFault{remaining: 1, err: store.ErrConflict, onUpdate: func() {
		f.clock.mu.Lock()
		f.clock.t = f.clock.t.Add(3 * time.Minute)
		f.clock.mu.Unlock()
	}}
	data := cedarEpochModuleData{st: f.st, mutateWrap: func(sc store.Scope) store.Scope {
		return &guardianTransitionScope{Scope: sc, repo: fault, faultKind: approvalKind}
	}}
	f.m.UseData(data)
	mc := api.ModuleContext{
		Tenant: f.tenant, Principal: auth.Principal{Kind: auth.KindUser, UserID: model.NewID(), CredID: model.NewID()},
		Data: api.NewScopedData(f.st, f.tenant),
	}
	_, err := f.m.decideApproval(context.Background(), approvalTenantData{data: data, tenant: f.tenant}, mc, approvalID, ApprovalDecisionRequest{Decision: "approve"})
	if err == nil || !strings.Contains(err.Error(), "window") || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("decision after the change window closed = %v, want a closed-window refusal", err)
	}
	var approval model.Record
	if err := f.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(approvalKind)
		if err != nil {
			return err
		}
		approval, err = repo.Get(context.Background(), approvalID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if approval.String(colStatus) != statusPending {
		t.Fatalf("approval after the closed-window refusal = %q, want pending", approval.String(colStatus))
	}
	decisions := 0
	if err := f.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(context.Background(), 1, func(e model.AuditEvent) error {
			if e.Action == "governance.approval.decision" {
				decisions++
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	if decisions != 0 {
		t.Fatalf("decision ledger entries = %d, want none", decisions)
	}
}

func TestGuardianContainmentRefusesMovedTargetBetweenDiscoveryAndSpend(t *testing.T) {
	f := newGuardianChangeRecordFixture(t, "")
	actionID := f.actions()[0].String(model.ColID)
	f.onRead = func() {
		if err := f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
			repo, err := sc.Ext(guardianActionKind)
			if err != nil {
				return err
			}
			rec, err := repo.Get(context.Background(), model.ID(actionID))
			if err != nil {
				return err
			}
			rec[colGATargetRef] = "other-agent"
			_, err = repo.Update(context.Background(), rec)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	res, err := f.m.GuardianSweep(context.Background(), f.tenant)
	if err == nil || res.Executed != 0 || len(f.stops()) != 0 {
		t.Fatalf("sweep with a moved target = %+v %v stops=%+v, want refusal and no containment", res, err, f.stops())
	}
	if got := f.approval().String(colConsumedBy); got != "" {
		t.Fatalf("approval was spent under a moved target: %q", got)
	}
}

func TestGuardianContainmentRefusesMovedApprovalBindingBetweenDiscoveryAndSpend(t *testing.T) {
	f := newGuardianChangeRecordFixture(t, "")
	actionID := f.actions()[0].String(model.ColID)
	approvalID := f.actions()[0].String(colGAApprovalID)
	f.onRead = func() {
		if err := f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
			repo, err := sc.Ext(guardianActionKind)
			if err != nil {
				return err
			}
			rec, err := repo.Get(context.Background(), model.ID(actionID))
			if err != nil {
				return err
			}
			rec[colGAApprovalID] = model.NewID().String()
			_, err = repo.Update(context.Background(), rec)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	res, err := f.m.GuardianSweep(context.Background(), f.tenant)
	if err == nil || res.Executed != 0 || len(f.stops()) != 0 {
		t.Fatalf("sweep with a moved approval binding = %+v %v stops=%+v, want refusal and no containment", res, err, f.stops())
	}
	if got := f.approvalByID(approvalID).String(colConsumedBy); got != "" {
		t.Fatalf("approval was spent under a moved binding: %q", got)
	}
}
