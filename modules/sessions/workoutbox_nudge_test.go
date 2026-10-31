// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// A successful work-outbox insert is reported to its module, which wakes the outbox pump once the
// mutation has committed (communication_commitnudge_test.go); every other kind inserts silently.
type nudgeStubRepo struct{ created int }

func (r *nudgeStubRepo) Descriptor() model.EntityDescriptor { return model.EntityDescriptor{} }
func (r *nudgeStubRepo) Get(context.Context, model.ID) (model.Record, error) {
	return nil, errors.New("unused")
}
func (r *nudgeStubRepo) List(context.Context, model.Query) ([]model.Record, model.Page, error) {
	return nil, model.Page{}, errors.New("unused")
}
func (r *nudgeStubRepo) Lock(context.Context, model.ID) (model.Record, error) {
	return nil, errors.New("unused")
}
func (r *nudgeStubRepo) CreateAtTransactionTime(_ context.Context, rec model.Record) (model.Record, error) {
	r.created++
	return rec, nil
}
func (r *nudgeStubRepo) CreateWithIDAtTransactionTime(_ context.Context, _ model.ID, rec model.Record) (model.Record, error) {
	return rec, nil
}
func (r *nudgeStubRepo) UpdateAtTransactionTime(_ context.Context, rec model.Record) (model.Record, error) {
	return rec, nil
}

func TestCreateReportsOnlyAWorkOutboxInsert(t *testing.T) {
	called := 0
	tx := &communicationTx{
		onWorkOutboxInsert: func() { called++ },
		resolveRepository: func(model.Kind) (communicationRepository, error) {
			return &nudgeStubRepo{}, nil
		},
	}
	if _, err := tx.create(context.Background(), workOutboxKind, model.Record{}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if called != 1 {
		t.Fatalf("an outbox insert must be reported once, got %d", called)
	}
	if _, err := tx.create(context.Background(), workEventKind, model.Record{}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if called != 1 {
		t.Fatalf("a non-outbox kind must not be reported, got %d", called)
	}

	// A failed insert is never reported.
	txErr := &communicationTx{
		onWorkOutboxInsert: func() { called++ },
		resolveRepository: func(model.Kind) (communicationRepository, error) {
			return nil, errors.New("no repo")
		},
	}
	if _, err := txErr.create(context.Background(), workOutboxKind, model.Record{}); err == nil {
		t.Fatal("the create must fail")
	}
	if called != 1 {
		t.Fatalf("a failed insert must not be reported, got %d", called)
	}
}

func TestWorkOutboxNudgeNilIsSilent(t *testing.T) {
	tx := &communicationTx{
		resolveRepository: func(model.Kind) (communicationRepository, error) {
			return &nudgeStubRepo{}, nil
		},
	}
	if _, err := tx.create(context.Background(), workOutboxKind, model.Record{}); err != nil {
		t.Fatalf("create with no nudge registered must just work: %v", err)
	}
}

// The wake-up belongs to the module. Two modules in one process (two engines, as the cmd/olivares tests run
// them) each wake only their own pump, and one withdrawing its nudge leaves the other's.
func TestEachModuleWakesOnlyItsOwnOutboxPump(t *testing.T) {
	var committedA, committedB bool
	a := &Module{Dependencies: &Dependencies{Data: commitOrderModuleData{committed: &committedA}}}
	b := &Module{Dependencies: &Dependencies{Data: commitOrderModuleData{committed: &committedB}}}
	woke := map[string]int{}
	a.SetWorkOutboxNudge(func() { woke["a"]++ })
	b.SetWorkOutboxNudge(func() { woke["b"]++ })
	insert := func(m *Module) {
		t.Helper()
		err := m.communicationData(model.NewTenantID()).Mutate(context.Background(), func(store.Scope) error {
			return insertWorkOutboxRows(context.Background(), m, workOutboxKind, 1)
		})
		if err != nil {
			t.Fatalf("mutate: %v", err)
		}
	}
	insert(a)
	if woke["a"] != 1 || woke["b"] != 0 {
		t.Fatalf("an insert on module a woke %v, want a once and b never", woke)
	}
	b.SetWorkOutboxNudge(nil)
	if !a.WorkOutboxNudgeRegistered() || b.WorkOutboxNudgeRegistered() {
		t.Fatalf("withdrawing b's nudge changed a's: a=%t b=%t", a.WorkOutboxNudgeRegistered(), b.WorkOutboxNudgeRegistered())
	}
	insert(a)
	if woke["a"] != 2 || woke["b"] != 0 {
		t.Fatalf("after b withdrew its nudge, an insert on a woke %v, want a twice and b never", woke)
	}
	insert(b)
	if woke["a"] != 2 || woke["b"] != 0 {
		t.Fatalf("an insert on module b, whose nudge is withdrawn, woke %v, want no change", woke)
	}
}
