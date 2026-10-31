// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// commitOrderModuleData runs a mutation and then commits it, as the store does, so a test can see
// whether something happened before or after the commit. A non-nil outcome is what the store
// answers after the commit was issued.
type commitOrderModuleData struct {
	committed *bool
	outcome   error
}

func (commitOrderModuleData) View(context.Context, model.TenantID, func(store.Scope) error) error {
	return nil
}

func (d commitOrderModuleData) Mutate(_ context.Context, _ model.TenantID, fn func(store.Scope) error) error {
	if err := fn(nil); err != nil {
		return err
	}
	*d.committed = true
	return d.outcome
}

// joinedStubScope stands in for the scope of an enclosing replay transaction; nothing reads it.
type joinedStubScope struct{ store.Scope }

// insertWorkOutboxRows creates n rows of kind through a communicationTx that reports to m.
func insertWorkOutboxRows(ctx context.Context, m *Module, kind model.Kind, n int) error {
	tx := &communicationTx{onWorkOutboxInsert: m.noteWorkOutboxInsert, resolveRepository: func(model.Kind) (communicationRepository, error) {
		return &nudgeStubRepo{}, nil
	}}
	for range n {
		if _, err := tx.create(ctx, kind, model.Record{}); err != nil {
			return err
		}
	}
	return nil
}

// A work-outbox row is visible to the drain it wakes only once its transaction has committed, so
// the module wakes the pump after the mutation that inserted it, once, and only when that mutation
// may have committed.
func TestTheOutboxPumpIsWokenAfterTheInsertingMutationCommits(t *testing.T) {
	tenant := model.NewTenantID()
	joinedCtx := func() context.Context {
		_, ctx := newProtocolReplayTransactionContext(context.Background(), tenant, joinedStubScope{})
		return ctx
	}
	for _, tc := range []struct {
		name      string
		kind      model.Kind
		inserts   int
		fail      bool
		outcome   error
		ctx       func() context.Context
		wantWakes int
	}{
		{name: "an outbox insert wakes the pump once", kind: workOutboxKind, inserts: 1, wantWakes: 1},
		{name: "two inserts in one mutation wake it once", kind: workOutboxKind, inserts: 2, wantWakes: 1},
		{name: "another kind wakes nothing", kind: workEventKind, inserts: 1, wantWakes: 0},
		{name: "a rolled-back insert wakes nothing", kind: workOutboxKind, inserts: 1, fail: true, wantWakes: 0},
		{name: "a refused commit wakes nothing", kind: workOutboxKind, inserts: 1, outcome: errors.New("serialization failure"), wantWakes: 0},
		{
			name: "an unknown commit outcome still wakes it, the row may be durable", kind: workOutboxKind, inserts: 1,
			outcome:   fmt.Errorf("%w: %w", store.ErrCommitOutcomeUnknown, store.ErrStoreUnavailable),
			wantWakes: 1,
		},
		{name: "a joined mutation leaves the wake to the enclosing commit", kind: workOutboxKind, inserts: 1, ctx: joinedCtx, wantWakes: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			committed := false
			m := &Module{Dependencies: &Dependencies{Data: commitOrderModuleData{committed: &committed, outcome: tc.outcome}}}
			var wakes, wokeBeforeCommit int
			m.SetWorkOutboxNudge(func() {
				wakes++
				if !committed {
					wokeBeforeCommit++
				}
			})
			ctx := context.Background()
			if tc.ctx != nil {
				ctx = tc.ctx()
			}

			err := m.communicationData(tenant).Mutate(ctx, func(store.Scope) error {
				if err := insertWorkOutboxRows(ctx, m, tc.kind, tc.inserts); err != nil {
					return err
				}
				if tc.fail {
					return errors.New("rolled back")
				}
				return nil
			})

			if want := tc.fail || tc.outcome != nil; want != (err != nil) {
				t.Fatalf("mutation error = %v, want an error %t", err, want)
			}
			if wokeBeforeCommit != 0 {
				t.Fatalf("the pump was woken %d time(s) before the inserting mutation committed", wokeBeforeCommit)
			}
			if wakes != tc.wantWakes {
				t.Fatalf("the pump was woken %d time(s), want %d", wakes, tc.wantWakes)
			}
		})
	}
}

// Data built without a module (tests that use the store alone) wakes nothing and does not panic.
func TestCommunicationDataWithoutAModuleWakesNothing(t *testing.T) {
	committed := false
	data := tenantCommunicationData{data: commitOrderModuleData{committed: &committed}, tenant: model.NewTenantID()}
	if err := data.Mutate(context.Background(), func(store.Scope) error { return nil }); err != nil {
		t.Fatalf("mutate: %v", err)
	}
	if !committed {
		t.Fatal("the mutation did not run")
	}
}

// The module's real transaction constructor reports an outbox insert to the module instead of waking
// the pump itself: a wake-up at the insert is the one this change removes.
func TestCommunicationTxFromTheModuleReportsInsertsAndWakesNothingYet(t *testing.T) {
	fixture := newWorkFixture(t, filepath.Join(t.TempDir(), "wiring.db"), nil)
	m := fixture.m
	wakes := 0
	m.SetWorkOutboxNudge(func() { wakes++ })

	err := m.communicationData(fixture.tenant).Mutate(context.Background(), func(sc store.Scope) error {
		tx, err := m.communicationTxWithAuthority(context.Background(), sc,
			communicationRequestAuthoritySnapshot{}, CommunicationClaimAuthoritySnapshot{})
		if err != nil {
			return err
		}
		if tx.onWorkOutboxInsert == nil {
			return errors.New("the module's transaction does not report outbox inserts")
		}
		tx.onWorkOutboxInsert()
		if wakes != 0 {
			return fmt.Errorf("the pump was woken at the insert, before the commit (%d)", wakes)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("mutate: %v", err)
	}
	if wakes != 1 {
		t.Fatalf("the pump was woken %d time(s) after the commit, want 1", wakes)
	}
}

// The protocol replay path commits through the work data, not the communication data, and its
// communication writes join that transaction. Its owning attempt wakes the pump once, after the
// commit that made the rows visible; an exact replay, a failed mutation and a joined inner replay
// add no wake-up of their own.
func TestAProtocolReplayWakesTheOutboxPumpOnceAfterItCommits(t *testing.T) {
	fixture := newWorkFixture(t, filepath.Join(t.TempDir(), "replay-wake.db"), nil)
	m := fixture.m
	claim := func(id string) ProtocolReplayClaim { return wakeReplayClaim(fixture, id) }
	wakes, guardsAtWake := 0, -1
	m.SetWorkOutboxNudge(func() {
		wakes++
		guardsAtWake = replayGuardCount(t, fixture)
	})
	insert := func(ctx context.Context) (ProtocolReplaySettlement, error) {
		m.noteWorkOutboxInsert()
		return ProtocolReplaySettlement{}, nil
	}

	if _, err := m.ApplyProtocolReplay(context.Background(), fixture.tenant, claim("first"), insert); err != nil {
		t.Fatalf("first replay: %v", err)
	}
	if wakes != 1 || guardsAtWake != 1 {
		t.Fatalf("after the first commit: %d wake-up(s) saw %d guard row(s), want 1 and 1", wakes, guardsAtWake)
	}

	again, err := m.ApplyProtocolReplay(context.Background(), fixture.tenant, claim("first"), insert)
	if err != nil || !again.Replayed {
		t.Fatalf("exact replay = %+v, %v, want a replay", again, err)
	}
	if wakes != 1 {
		t.Fatalf("an exact replay woke the pump (%d wake-ups)", wakes)
	}

	failed := errors.New("mutation failed")
	if _, err := m.ApplyProtocolReplay(context.Background(), fixture.tenant, claim("failing"),
		func(ctx context.Context) (ProtocolReplaySettlement, error) {
			m.noteWorkOutboxInsert()
			return ProtocolReplaySettlement{}, failed
		}); err == nil {
		t.Fatal("a failing mutation succeeded")
	}
	if wakes != 1 {
		t.Fatalf("a rolled-back replay woke the pump (%d wake-ups)", wakes)
	}

	if _, err := m.ApplyProtocolReplay(context.Background(), fixture.tenant, claim("outer"),
		func(ctx context.Context) (ProtocolReplaySettlement, error) {
			m.noteWorkOutboxInsert()
			if _, err := m.ApplyProtocolReplay(ctx, fixture.tenant, claim("inner"), insert); err != nil {
				return ProtocolReplaySettlement{}, err
			}
			if wakes != 1 {
				return ProtocolReplaySettlement{}, fmt.Errorf("the pump was woken inside the enclosing transaction (%d)", wakes)
			}
			return ProtocolReplaySettlement{}, nil
		}); err != nil {
		t.Fatalf("nested replay: %v", err)
	}
	if wakes != 2 || guardsAtWake != 3 {
		t.Fatalf("after the nested commit: %d wake-up(s) saw %d guard row(s), want 2 and 3", wakes, guardsAtWake)
	}
}

func wakeReplayClaim(fixture workFixture, id string) ProtocolReplayClaim {
	return ProtocolReplayClaim{
		WorkspaceID: fixture.workspace, Protocol: BindingProtocolA2A, PeerAuthority: "https://peer.example",
		Kind: ProtocolReplayMessageID, ReplayID: id, ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
}

// commitSawData runs the real mutation and commit, then records that the commit returned, so a
// wake-up can be observed on one side of it.
type commitSawData struct {
	api.ModuleData
	committed *bool
}

func (d commitSawData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	err := d.ModuleData.Mutate(ctx, tenant, fn)
	if err == nil {
		*d.committed = true
	}
	return err
}

// commitAnswersData runs the real mutation and commit, then answers err, as a store does whose COMMIT
// acknowledgement was lost or whose commit was refused.
type commitAnswersData struct {
	api.ModuleData
	err error
}

func (d commitAnswersData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	if err := d.ModuleData.Mutate(ctx, tenant, fn); err != nil {
		return err
	}
	return d.err
}

// A replay whose commit outcome is unknown may be durable, so it wakes the pump like a committed one;
// a refused commit wakes nothing.
func TestAProtocolReplayWithAnUnknownCommitOutcomeStillWakesThePump(t *testing.T) {
	fixture := newWorkFixture(t, filepath.Join(t.TempDir(), "replay-unknown.db"), nil)
	m := fixture.m
	wakes := 0
	m.SetWorkOutboxNudge(func() { wakes++ })
	for _, tc := range []struct {
		name      string
		answer    error
		wantWakes int
	}{
		{name: "unknown", answer: fmt.Errorf("%w: %w", store.ErrCommitOutcomeUnknown, store.ErrStoreUnavailable), wantWakes: 1},
		{name: "refused", answer: errors.New("serialization failure"), wantWakes: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wakes = 0
			real := m.Dependencies.Data
			m.Dependencies.Data = commitAnswersData{ModuleData: real, err: tc.answer}
			t.Cleanup(func() { m.Dependencies.Data = real })

			_, err := m.ApplyProtocolReplay(context.Background(), fixture.tenant, wakeReplayClaim(fixture, tc.name),
				func(context.Context) (ProtocolReplaySettlement, error) {
					m.noteWorkOutboxInsert()
					return ProtocolReplaySettlement{}, nil
				})

			if err == nil {
				t.Fatal("the replay did not surface the commit answer")
			}
			if wakes != tc.wantWakes {
				t.Fatalf("the pump was woken %d time(s), want %d", wakes, tc.wantWakes)
			}
		})
	}
}

// A DecisionResponse that carries a work command inserts its outbox row through the work-event
// writer, not through communicationTx.create. The insert must still be counted, so the communication
// mutation that encloses it wakes the pump, once, after the commit that made the row durable.
func TestADecisionResponseWakesTheOutboxPumpAfterItCommits(t *testing.T) {
	t.Parallel()

	fixture := newDecisionResponseFixture(t)
	ctx := decisionResponseTestContext(t)
	command := decisionResolveCommand(fixture.request.Version, model.NewID(), "authorized release")

	committed := false
	real := fixture.m.Dependencies.Data
	fixture.m.Dependencies.Data = commitSawData{ModuleData: real, committed: &committed}
	t.Cleanup(func() { fixture.m.Dependencies.Data = real })
	var wakes, wokeBeforeCommit int
	fixture.m.SetWorkOutboxNudge(func() {
		wakes++
		if !committed {
			wokeBeforeCommit++
		}
	})

	result, err := fixture.m.respondDecisionRequestWithAuthority(
		ctx, fixture.scope, fixture.ref, fixture.request.ID, command,
	)
	if err != nil {
		t.Fatalf("resolve DecisionRequest: %v", err)
	}
	rows := communicationRowsForTest(t, fixture.directNoticeFixture, workOutboxKind)
	if result.EventID.IsZero() || len(rows) != 1 {
		t.Fatal("the DecisionResponse did not insert its work-outbox row")
	}
	if state := rows[0].String(colOutboxState); state != "pending" {
		t.Fatalf("the work-outbox row state = %q, want pending until the pump drains it", state)
	}
	if wokeBeforeCommit != 0 {
		t.Fatalf("the pump was woken %d time(s) before the DecisionResponse committed", wokeBeforeCommit)
	}
	if wakes != 1 {
		t.Fatalf("the pump was woken %d time(s) by the DecisionResponse, want 1", wakes)
	}
}

// A protocol binding mutation inserts its outbox row the same way. Reserve, settle and a replay write
// through the work data: each real insert wakes the pump once after its commit, and the replay that
// writes nothing wakes nothing.
func TestAProtocolBindingMutationWakesTheOutboxPumpAfterItCommits(t *testing.T) {
	t.Parallel()

	f := newWorkFixture(t, filepath.Join(t.TempDir(), "binding-wake.db"), nil)
	work := addWorkLeaseDomainItem(t, f, "protocol binding wake")
	active := applyProtocolSpecForTest(t, f.m, f.tenant,
		protocolSpecInputForTest(f.workspace, BindingProtocolA2A, "remote-agent", 1, model.ID("")))

	committed := false
	real := f.m.Dependencies.Data
	f.m.Dependencies.Data = commitSawData{ModuleData: real, committed: &committed}
	t.Cleanup(func() { f.m.Dependencies.Data = real })
	var wakes, wokeBeforeCommit int
	f.m.SetWorkOutboxNudge(func() {
		wakes++
		if !committed {
			wokeBeforeCommit++
		}
	})

	reservation := ProtocolBindingReservation{
		WorkspaceID: f.workspace, BindingSpecID: active.ID,
		BindingSpecGeneration: active.Generation, ExpectedDirection: BindingOutbound,
		WorkItemID:  work.ready.ResultID,
		DispatchKey: "dispatch:wake", ExpectedExternalKind: string(ProtocolBindingResultTaskOrMessage),
		Generation: 1, OwnerKind: "agent", OwnerRef: work.agentRef,
		OwnerEpoch: 1, LeaseFence: 0,
	}
	committed = false
	reserved, err := f.m.ReserveProtocolBinding(context.Background(), f.tenant, reservation)
	if err != nil {
		t.Fatalf("reserve protocol binding: %v", err)
	}
	if !committed {
		t.Fatal("the reservation transaction did not commit")
	}
	if row, found := workOutboxRowForEventTest(t, f, reserved.LastEventID); !found {
		t.Fatalf("after the reservation: no work-outbox row for event %s", reserved.LastEventID)
	} else if state := row.String(colOutboxState); state != "pending" {
		t.Fatalf("after the reservation: work-outbox row state = %q, want pending", state)
	}
	if wakes != 1 {
		t.Fatalf("the pump was woken %d time(s) by the reservation, want 1", wakes)
	}

	if replayed, err := f.m.ReserveProtocolBinding(context.Background(), f.tenant, reservation); err != nil {
		t.Fatalf("replay protocol binding: %v", err)
	} else if !replayed.Replayed {
		t.Fatal("the reservation replay was not reported as a replay")
	}
	if wakes != 1 {
		t.Fatalf("a reservation replay woke the pump (%d wake-ups)", wakes)
	}

	committed = false
	if _, err := f.m.SettleProtocolBinding(context.Background(), f.tenant, ProtocolBindingSettlement{
		BindingID: reserved.ID, Generation: reserved.Generation, ExpectedVersion: reserved.Version,
		DispatchKey: reservation.DispatchKey, ResultKind: ProtocolBindingResultTask,
		ExternalID: "task:remote-wake", ContextID: "context:remote-wake",
		LocalState: "delegated", RemoteState: "working", RemoteRevision: "rev:1",
		Verdict: ProtocolObservationClean, Code: "task_accepted", Observed: true,
	}); err != nil {
		t.Fatalf("settle protocol binding: %v", err)
	}
	if !committed {
		t.Fatal("the settlement transaction did not commit")
	}
	if wokeBeforeCommit != 0 {
		t.Fatalf("the pump was woken %d time(s) before its binding mutation committed", wokeBeforeCommit)
	}
	if wakes != 2 {
		t.Fatalf("the pump was woken %d time(s) by reserve and settle, want 2", wakes)
	}
}

// workOutboxRowForEventTest reads the committed work-outbox row an event owns through the store,
// as another connection would. The fixture's setup commands leave their own pending rows (it
// wires no event sink), so the reservation's row is named, never inferred from a count.
func workOutboxRowForEventTest(t *testing.T, fixture workFixture, eventID model.ID) (model.Record, bool) {
	t.Helper()
	var rows []model.Record
	if err := fixture.st.View(context.Background(), fixture.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(workOutboxKind)
		if err != nil {
			return err
		}
		rows, _, err = repo.List(context.Background(), model.Query{
			Filters: []model.Filter{{Column: colOutboxEventID, Op: model.OpEq, Value: eventID.String()}},
			Limit:   2,
		})
		return err
	}); err != nil {
		t.Fatalf("read work-outbox row for event %s: %v", eventID, err)
	}
	if len(rows) == 0 {
		return nil, false
	}
	if len(rows) != 1 {
		t.Fatalf("work-outbox rows for event %s = %d, want 1", eventID, len(rows))
	}
	return rows[0], true
}

// replayGuardCount reads the committed replay guard rows through the store, as another connection would.
func replayGuardCount(t *testing.T, fixture workFixture) int {
	t.Helper()
	var guards []model.Record
	if err := fixture.st.View(context.Background(), fixture.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(protocolReplayGuardKind)
		if err != nil {
			return err
		}
		guards, _, err = repo.List(context.Background(), model.Query{Limit: 10})
		return err
	}); err != nil {
		t.Errorf("read replay guards: %v", err)
	}
	return len(guards)
}
