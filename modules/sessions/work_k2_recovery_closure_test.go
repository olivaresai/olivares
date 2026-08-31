// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// seedK2RecoveryActiveLeases creates only the durable aggregate rows needed by
// recovery. It deliberately does not use Apply: these are crash-recovery
// fixtures whose initiating command/event may have happened on a dead node.
func seedK2RecoveryActiveLeases(
	t *testing.T,
	f workFixture,
	workspace model.ID,
	count int,
	now time.Time,
	runRef string,
) []model.ID {
	t.Helper()
	ids := make([]model.ID, 0, count)
	sid := sidPrefix + model.NewID().String()
	if err := f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
		items, err := sc.Ext(workItemKind)
		if err != nil {
			return err
		}
		leases, err := sc.Ext(workLeaseKind)
		if err != nil {
			return err
		}
		for i := 0; i < count; i++ {
			item, err := items.Create(context.Background(), model.Record{
				colWorkWorkspaceID:        workspace.String(),
				colWorkKind:               "implementation",
				colWorkTitle:              fmt.Sprintf("recovery candidate %03d", i),
				colWorkBrief:              "Recover the durable lease after its holder stops.",
				colWorkBriefHash:          hashBytes([]byte("Recover the durable lease after its holder stops.")),
				colWorkContextRefs:        "[]",
				colWorkStatus:             "active",
				colWorkPriority:           "p1",
				colWorkOwnerKind:          "agent",
				colWorkOwnerRef:           "agent:" + model.NewID().String(),
				colWorkOwnerEpoch:         int64(1),
				colWorkProvKind:           "system",
				colWorkProvRef:            "test:k2-recovery",
				colWorkProvHash:           nil,
				colWorkParentID:           nil,
				colWorkSupersedesID:       nil,
				colWorkAcceptanceRevision: int64(1),
				colWorkBlockedCode:        nil,
				colWorkBlockedReason:      nil,
				colWorkTerminalCode:       nil,
				colWorkTerminalReason:     nil,
				colWorkDueAt:              nil,
				colWorkReadyAt:            model.NewTimestamp(now.Add(-3 * time.Minute)).String(),
				colWorkStartedAt:          model.NewTimestamp(now.Add(-2 * time.Minute)).String(),
				colWorkReviewAt:           nil,
				colWorkTerminalAt:         nil,
				colWorkArchivedAt:         nil,
				colWorkLastEventSeq:       int64(1),
			})
			if err != nil {
				return err
			}
			itemID := recordID(item)
			if _, err := leases.Create(context.Background(), model.Record{
				colWorkWorkspaceID:     workspace.String(),
				colWorkItemID:          itemID.String(),
				colLeaseHolderSID:      sid,
				colLeaseHolderRunRef:   nullableString(runRef),
				colLeaseHolderAgentRef: nil,
				colLeaseFence:          int64(1),
				colLeaseState:          workLeaseActive,
				colLeaseAcquiredAt:     model.NewTimestamp(now.Add(-2 * time.Minute)).String(),
				colLeaseRenewedAt:      nil,
				colLeaseExpiresAt:      model.NewTimestamp(now.Add(-time.Millisecond)).String(),
				colLeaseEndedAt:        nil,
				colLeaseEndReason:      nil,
				colLeaseRenewalCount:   int64(0),
			}); err != nil {
				return err
			}
			ids = append(ids, itemID)
		}
		return nil
	}); err != nil {
		t.Fatalf("seed active recovery leases: %v", err)
	}
	return ids
}

func seedK2RecoveryClockRollback(
	t *testing.T,
	f workFixture,
	workspace model.ID,
	last time.Time,
) {
	t.Helper()
	if err := f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(workGuardKind)
		if err != nil {
			return err
		}
		_, err = repo.Create(context.Background(), model.Record{
			colWorkWorkspaceID:     workspace.String(),
			colGuardKind:           "lease_clock",
			colGuardEpoch:          int64(1),
			colGuardLastDBTime:     model.NewTimestamp(last).String(),
			colGuardRebaseDecision: nil,
			colGuardRebaseEvidence: nil,
		})
		return err
	}); err != nil {
		t.Fatalf("seed workspace clock rollback: %v", err)
	}
}

func k2RecoveryLease(t *testing.T, f workFixture, itemID model.ID) WorkLease {
	t.Helper()
	lease, err := f.m.GetLease(context.Background(), f.tenant, f.principal, itemID)
	if err != nil {
		t.Fatalf("get recovery lease %s: %v", itemID, err)
	}
	return lease
}

func k2RecoveryItem(t *testing.T, f workFixture, itemID model.ID) WorkItem {
	t.Helper()
	snapshot, err := f.m.Get(context.Background(), f.tenant, f.principal, itemID)
	if err != nil {
		t.Fatalf("get recovery item %s: %v", itemID, err)
	}
	return snapshot.Item
}

func TestWorkK2ReaperPaginatesPastRolledBackWorkspace(t *testing.T) {
	t.Parallel()

	f := newWorkFixture(t, t.TempDir()+"/reaper-pages.db", nil)
	t.Cleanup(func() { _ = f.st.Close() })
	_, healthyWorkspace := workSchemaWorkspaces(t, context.Background(), f.m, f.tenant)
	fixed := time.Now().UTC().Add(5 * time.Minute).Truncate(time.Millisecond)

	bad := seedK2RecoveryActiveLeases(t, f, f.workspace, 201, fixed, "")
	// UUIDv7 carries a millisecond timestamp. Crossing that boundary makes the
	// healthy row provably later than every bad-workspace row in the default
	// keyset ordering, so a one-page mutant cannot accidentally pass.
	time.Sleep(2 * time.Millisecond)
	healthy := seedK2RecoveryActiveLeases(t, f, healthyWorkspace, 1, fixed, "")[0]
	seedK2RecoveryClockRollback(t, f, f.workspace, fixed.Add(time.Hour))

	data := workLeaseFixedClockData{
		inner: f.m.workData(f.tenant), now: model.NewTimestamp(fixed),
	}
	if err := data.View(context.Background(), func(sc store.Scope) error {
		repo, err := sc.Ext(workLeaseKind)
		if err != nil {
			return err
		}
		rows, page, err := repo.List(context.Background(), model.Query{
			Filters: []model.Filter{eq(colLeaseState, workLeaseActive)}, Limit: 200,
		})
		if err != nil {
			return err
		}
		if len(rows) != 200 || !page.HasMore || page.Cursor == "" {
			t.Fatalf("first recovery page = %d, %#v; want 200 and continuation", len(rows), page)
		}
		for _, row := range rows {
			if row.String(colWorkItemID) == healthy.String() {
				t.Fatalf("healthy lease unexpectedly appeared on first page")
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("inspect first recovery page: %v", err)
	}

	reaped, err := f.m.reapWorkLeasesWithData(
		context.Background(), data, f.tenant, 1,
	)
	if reaped != 1 {
		t.Fatalf("paginated reaper = %d, %v; want one healthy recovery", reaped, err)
	}
	assertWorkVerdict(t, err, VerdictUnknown, "clock_rollback")
	if got := k2RecoveryLease(t, f, healthy); got.State != workLeaseExpired || got.Fence != 2 {
		t.Fatalf("healthy lease after bad page = %#v", got)
	}
	if got := k2RecoveryItem(t, f, healthy); got.Status != "blocked" || got.BlockedCode != "lease_expired" {
		t.Fatalf("healthy item after bad page = %#v", got)
	}
	// Non-trigger direction: rollback is workspace-local and cannot be asserted
	// as expiry. Check both ends of the page-spanning bad population.
	for _, itemID := range []model.ID{bad[0], bad[len(bad)-1]} {
		if got := k2RecoveryLease(t, f, itemID); got.State != workLeaseActive || got.Fence != 1 {
			t.Fatalf("rolled-back workspace lease changed: %#v", got)
		}
	}
}

type k2RecoveryFirstMutateHookData struct {
	inner workData
	once  sync.Once
	hook  func() error
	err   error
}

func (d *k2RecoveryFirstMutateHookData) View(
	ctx context.Context,
	fn func(store.Scope) error,
) error {
	return d.inner.View(ctx, fn)
}

func (d *k2RecoveryFirstMutateHookData) Mutate(
	ctx context.Context,
	fn func(store.Scope) error,
) error {
	d.once.Do(func() { d.err = d.hook() })
	if d.err != nil {
		return d.err
	}
	return d.inner.Mutate(ctx, fn)
}

func renewAndVersionRaceK2RecoveryCandidate(
	f workFixture,
	itemID model.ID,
	now time.Time,
) error {
	return f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
		lease, found, err := findWorkLease(context.Background(), sc, itemID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("recovery race lease is missing")
		}
		lease[colLeaseRenewedAt] = model.NewTimestamp(now).String()
		lease[colLeaseExpiresAt] = model.NewTimestamp(now.Add(time.Minute)).String()
		lease[colLeaseRenewalCount] = lease.Int(colLeaseRenewalCount) + 1
		leases, err := sc.Ext(workLeaseKind)
		if err != nil {
			return err
		}
		if _, err := leases.Update(context.Background(), lease); err != nil {
			return err
		}
		items, err := sc.Ext(workItemKind)
		if err != nil {
			return err
		}
		item, err := items.Get(context.Background(), itemID)
		if err != nil {
			return err
		}
		item[colWorkTitle] = item.String(colWorkTitle) + " (raced)"
		_, err = items.Update(context.Background(), item)
		return err
	})
}

func TestWorkK2ReaperRenewedVersionRaceDoesNotConsumeBatchSlot(t *testing.T) {
	t.Parallel()

	f := newWorkFixture(t, t.TempDir()+"/reaper-race.db", nil)
	t.Cleanup(func() { _ = f.st.Close() })
	fixed := time.Now().UTC().Add(5 * time.Minute).Truncate(time.Millisecond)
	first := seedK2RecoveryActiveLeases(t, f, f.workspace, 1, fixed, "")[0]
	time.Sleep(2 * time.Millisecond)
	next := seedK2RecoveryActiveLeases(t, f, f.workspace, 1, fixed, "")[0]

	fixedData := workLeaseFixedClockData{
		inner: f.m.workData(f.tenant), now: model.NewTimestamp(fixed),
	}
	data := &k2RecoveryFirstMutateHookData{
		inner: fixedData,
		hook: func() error {
			return renewAndVersionRaceK2RecoveryCandidate(f, first, fixed)
		},
	}
	reaped, err := f.m.reapWorkLeasesWithData(
		context.Background(), data, f.tenant, 1,
	)
	if err != nil || reaped != 1 {
		t.Fatalf("racing reaper = %d, %v; want the later due candidate", reaped, err)
	}
	if got := k2RecoveryLease(t, f, first); got.State != workLeaseActive ||
		got.Fence != 1 || got.RenewalCount != 1 {
		t.Fatalf("renewed candidate was consumed: %#v", got)
	}
	if got := k2RecoveryItem(t, f, first); got.Status != "active" {
		t.Fatalf("renewed candidate item changed: %#v", got)
	}
	if got := k2RecoveryLease(t, f, next); got.State != workLeaseExpired || got.Fence != 2 {
		t.Fatalf("next due candidate was starved: %#v", got)
	}
	if got := k2RecoveryItem(t, f, next); got.Status != "blocked" || got.BlockedCode != "lease_expired" {
		t.Fatalf("next due item was not recovered: %#v", got)
	}
}

type k2RecoveryFirstMutateHookModuleData struct {
	inner api.ModuleData
	once  sync.Once
	hook  func() error
	err   error
}

func (d *k2RecoveryFirstMutateHookModuleData) View(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	return d.inner.View(ctx, tenant, fn)
}

func (d *k2RecoveryFirstMutateHookModuleData) Mutate(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	d.once.Do(func() { d.err = d.hook() })
	if d.err != nil {
		return d.err
	}
	return d.inner.Mutate(ctx, tenant, fn)
}

func bumpK2RecoveryItemVersion(f workFixture, itemID model.ID) error {
	return f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(workItemKind)
		if err != nil {
			return err
		}
		item, err := repo.Get(context.Background(), itemID)
		if err != nil {
			return err
		}
		item[colWorkTitle] = item.String(colWorkTitle) + " (concurrent observation)"
		_, err = repo.Update(context.Background(), item)
		return err
	})
}

func replaceK2RecoveryLeaseRun(
	f workFixture,
	itemID model.ID,
	runRef string,
	now time.Time,
) error {
	return f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(workLeaseKind)
		if err != nil {
			return err
		}
		lease, found, err := findWorkLease(context.Background(), sc, itemID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("successor lease is missing")
		}
		lease[colLeaseHolderRunRef] = runRef
		lease[colLeaseFence] = lease.Int(colLeaseFence) + 1
		lease[colLeaseAcquiredAt] = model.NewTimestamp(now).String()
		lease[colLeaseRenewedAt] = nil
		lease[colLeaseExpiresAt] = model.NewTimestamp(now.Add(time.Minute)).String()
		lease[colLeaseRenewalCount] = int64(0)
		_, err = repo.Update(context.Background(), lease)
		return err
	})
}

func TestWorkK2OwnerDiedRetriesEmptyRunRefAndPreservesSuccessor(t *testing.T) {
	t.Parallel()

	t.Run("empty run ref retries a raced WorkItem version", func(t *testing.T) {
		f := newWorkFixture(t, t.TempDir()+"/owner-died-empty.db", nil)
		t.Cleanup(func() { _ = f.st.Close() })
		now := time.Now().UTC()
		itemID := seedK2RecoveryActiveLeases(t, f, f.workspace, 1, now.Add(time.Minute), "")[0]
		lease := k2RecoveryLease(t, f, itemID)
		inner := f.m.data
		f.m.UseData(&k2RecoveryFirstMutateHookModuleData{
			inner: inner,
			hook:  func() error { return bumpK2RecoveryItemVersion(f, itemID) },
		})

		if err := f.m.OwnerDied(
			context.Background(), f.tenant, lease.HolderSID, "dead-run", "runtime exited",
		); err != nil {
			t.Fatalf("owner death with empty stored run_ref: %v", err)
		}
		if got := k2RecoveryLease(t, f, itemID); got.State != workLeaseRevoked ||
			got.Fence != lease.Fence+1 || got.HolderRunRef != "" {
			t.Fatalf("empty-run owner death did not revoke after version retry: %#v", got)
		}
		if got := k2RecoveryItem(t, f, itemID); got.Status != "blocked" ||
			got.BlockedCode != "owner_session_died" || got.Version != 3 {
			t.Fatalf("empty-run owner death item = %#v", got)
		}
	})

	t.Run("newer run generation is not the dead owner", func(t *testing.T) {
		f := newWorkFixture(t, t.TempDir()+"/owner-died-successor.db", nil)
		t.Cleanup(func() { _ = f.st.Close() })
		now := time.Now().UTC()
		itemID := seedK2RecoveryActiveLeases(
			t, f, f.workspace, 1, now.Add(time.Minute), "dead-run",
		)[0]
		before := k2RecoveryLease(t, f, itemID)
		inner := f.m.data
		f.m.UseData(&k2RecoveryFirstMutateHookModuleData{
			inner: inner,
			hook: func() error {
				return replaceK2RecoveryLeaseRun(f, itemID, "successor-run", now)
			},
		})

		if err := f.m.OwnerDied(
			context.Background(), f.tenant, before.HolderSID, "dead-run", "runtime exited",
		); err != nil {
			t.Fatalf("owner death racing successor: %v", err)
		}
		if got := k2RecoveryLease(t, f, itemID); got.State != workLeaseActive ||
			got.Fence != before.Fence+1 || got.HolderRunRef != "successor-run" {
			t.Fatalf("dead predecessor revoked its successor: %#v", got)
		}
		if got := k2RecoveryItem(t, f, itemID); got.Status != "active" || got.Version != 1 {
			t.Fatalf("successor's WorkItem changed: %#v", got)
		}
	})
}

func readK2RecoveryClaim(t *testing.T, f workFixture, sid string) model.Record {
	t.Helper()
	var out model.Record
	if err := f.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
		var found bool
		var err error
		out, found, err = findClaim(context.Background(), sc, sid)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("claim %s is missing", sid)
		}
		return nil
	}); err != nil {
		t.Fatalf("read claim: %v", err)
	}
	return out
}

func TestWorkK2PlanValidateAreReadOnlyButApplyTouchesClaim(t *testing.T) {
	t.Parallel()

	f := newWorkLeaseDomainFixture(t, "claim row effects")
	cmd := f.command("lease.acquire", f.ready.Version, 0)
	before := readK2RecoveryClaim(t, f.workFixture, f.sid)

	plan, err := f.m.Plan(context.Background(), f.tenant, f.holder, cmd)
	if err != nil || plan.Verdict != VerdictClean {
		t.Fatalf("plan lease acquire = %#v, %v", plan, err)
	}
	wantEffects := []string{
		"sessions.work_item:update",
		"sessions.work_event:append",
		"sessions.work_outbox:insert",
		"sessions.work_command:append",
		"sessions.work_guard:cas",
		"sessions.claim:cas",
		"sessions.work_lease:cas",
	}
	if !reflect.DeepEqual(plan.RowEffects, wantEffects) {
		t.Fatalf("planned acquire effects = %#v, want %#v", plan.RowEffects, wantEffects)
	}
	assessment, err := f.m.Validate(context.Background(), f.tenant, f.holder, cmd)
	if err != nil || assessment.Verdict != VerdictClean {
		t.Fatalf("validate lease acquire = %#v, %v", assessment, err)
	}
	afterReads := readK2RecoveryClaim(t, f.workFixture, f.sid)
	if afterReads.Int(model.ColVersion) != before.Int(model.ColVersion) {
		t.Fatalf("Plan/Validate wrote Claim: before v%d after v%d",
			before.Int(model.ColVersion), afterReads.Int(model.ColVersion))
	}

	if _, err := f.m.Apply(context.Background(), f.tenant, f.holder, cmd); err != nil {
		t.Fatalf("apply lease acquire: %v", err)
	}
	afterApply := readK2RecoveryClaim(t, f.workFixture, f.sid)
	if afterApply.Int(model.ColVersion) != before.Int(model.ColVersion)+1 {
		t.Fatalf("Apply Claim version = %d, want %d",
			afterApply.Int(model.ColVersion), before.Int(model.ColVersion)+1)
	}
	if afterApply.String(colClaimState) != claimActive || afterApply.Int(colFence) != before.Int(colFence) {
		t.Fatalf("Apply's Claim CAS changed authority: before=%#v after=%#v", before, afterApply)
	}
}

func TestWorkK2RowEffectsAreExact(t *testing.T) {
	t.Parallel()

	base := []string{
		"sessions.work_item:update",
		"sessions.work_event:append",
		"sessions.work_outbox:insert",
		"sessions.work_command:append",
	}
	clockBase := append(append([]string{}, base...), "sessions.work_guard:cas")
	leaseBase := append(append([]string{}, base...),
		"sessions.work_guard:cas", "sessions.claim:cas", "sessions.work_lease:cas",
	)
	active := model.Record{colWorkStatus: "active"}
	ready := model.Record{colWorkStatus: "ready"}
	review := model.Record{colWorkStatus: "review"}
	holder := WorkPrincipal{Admin: false}
	admin := WorkPrincipal{Admin: true}

	tests := []struct {
		name      string
		cmd       WorkCommand
		current   model.Record
		principal WorkPrincipal
		want      []string
	}{
		{
			name: "create without acceptance projection",
			cmd:  WorkCommand{Command: "item.create"},
			want: []string{
				"sessions.work_item:insert", "sessions.work_lease:insert",
				"sessions.work_event:append", "sessions.work_outbox:insert",
				"sessions.work_command:append",
			},
		},
		{
			name: "create with acceptance projection",
			cmd: WorkCommand{Command: "item.create", Acceptance: []AcceptanceInput{{
				Key: "tests", Statement: "green", Required: true,
			}}},
			want: []string{
				"sessions.work_item:insert", "sessions.work_lease:insert",
				"sessions.work_event:append", "sessions.work_outbox:insert",
				"sessions.work_command:append", "sessions.work_acceptance:insert",
			},
		},
		{
			name: "acquire without run",
			cmd:  WorkCommand{Command: "lease.acquire"}, current: ready,
			want: leaseBase,
		},
		{
			name: "acquire binds run",
			cmd:  WorkCommand{Command: "lease.acquire", HolderRunRef: "run-1"}, current: ready,
			want: append(append([]string{}, leaseBase...), "sessions.run:bind"),
		},
		{
			name: "acquire from review resets acceptance",
			cmd:  WorkCommand{Command: "lease.acquire"}, current: review,
			want: append(append([]string{}, leaseBase...), "sessions.work_acceptance:mutate"),
		},
		{
			name: "holder block touches Claim",
			cmd:  WorkCommand{Command: "item.block"}, current: active, principal: holder,
			want: append(append([]string{}, clockBase...), "sessions.work_lease:cas", "sessions.claim:cas"),
		},
		{
			name: "admin block does not pretend to touch Claim",
			cmd:  WorkCommand{Command: "item.block"}, current: active, principal: admin,
			want: append(append([]string{}, clockBase...), "sessions.work_lease:cas"),
		},
		{
			name: "holder fail touches Claim",
			cmd:  WorkCommand{Command: "item.fail"}, current: active, principal: holder,
			want: append(append([]string{}, clockBase...), "sessions.work_lease:cas", "sessions.claim:cas"),
		},
		{
			name: "admin fail does not pretend to touch Claim",
			cmd:  WorkCommand{Command: "item.fail"}, current: active, principal: admin,
			want: append(append([]string{}, clockBase...), "sessions.work_lease:cas"),
		},
		{
			name: "active acceptance evaluation touches Claim",
			cmd:  WorkCommand{Command: "acceptance.evaluate"}, current: active, principal: holder,
			want: append(append([]string{}, clockBase...), "sessions.work_acceptance:mutate", "sessions.claim:cas"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := workRowEffects(tc.cmd, tc.current, tc.principal)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("row effects = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestWorkK2AgentOwnedLeaseRequiresLiveClaim(t *testing.T) {
	t.Parallel()

	t.Run("released Claim is rejected without a WorkLease write", func(t *testing.T) {
		f := newWorkLeaseDomainFixture(t, "dead agent-owned claim")
		claim := readK2RecoveryClaim(t, f.workFixture, f.sid)
		if err := f.m.Release(
			context.Background(), f.tenant, f.sid, claim.String(colHolder), claim.Int(colFence),
		); err != nil {
			t.Fatalf("release holder Claim: %v", err)
		}

		_, err := f.m.Apply(
			context.Background(), f.tenant, f.holder,
			f.command("lease.acquire", f.ready.Version, 0),
		)
		assertWorkVerdict(t, err, VerdictBroken, "owner_ineligible")
		if got := getWorkLease(t, f); got.State != workLeaseVacant || got.Fence != 0 {
			t.Fatalf("dead Claim acquired WorkLease: %#v", got)
		}
		if got := getWorkSnapshot(t, f).Item; got.Status != "ready" || got.Version != f.ready.Version {
			t.Fatalf("dead Claim changed WorkItem: %#v", got)
		}
	})

	t.Run("live Claim acquires", func(t *testing.T) {
		f := newWorkLeaseDomainFixture(t, "live agent-owned claim")
		result := applyWorkLeaseCommand(
			t, f, f.holder, f.command("lease.acquire", f.ready.Version, 0),
		)
		if got := getWorkLease(t, f); result.Status != "active" ||
			got.State != workLeaseActive || got.Fence != 1 || got.HolderSID != f.sid {
			t.Fatalf("live Claim control = result %#v lease %#v", result, got)
		}
	})
}
