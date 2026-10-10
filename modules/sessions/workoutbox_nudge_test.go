// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// C2 item 5: a successful work-outbox insert wakes the outbox pump through the
// registered nudge; every other kind inserts silently.
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

func TestCreateNudgesOnlyOnWorkOutboxInsert(t *testing.T) {
	called := 0
	SetWorkOutboxNudge(func() { called++ })
	t.Cleanup(func() { SetWorkOutboxNudge(nil) })

	tx := &communicationTx{
		resolveRepository: func(model.Kind) (communicationRepository, error) {
			return &nudgeStubRepo{}, nil
		},
	}
	if _, err := tx.create(context.Background(), workOutboxKind, model.Record{}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if called != 1 {
		t.Fatalf("an outbox insert must nudge once, got %d", called)
	}
	if _, err := tx.create(context.Background(), workEventKind, model.Record{}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if called != 1 {
		t.Fatalf("a non-outbox kind must not nudge, got %d", called)
	}

	// A failed insert never nudges.
	txErr := &communicationTx{
		resolveRepository: func(model.Kind) (communicationRepository, error) {
			return nil, errors.New("no repo")
		},
	}
	if _, err := txErr.create(context.Background(), workOutboxKind, model.Record{}); err == nil {
		t.Fatal("the create must fail")
	}
	if called != 1 {
		t.Fatalf("a failed insert must not nudge, got %d", called)
	}
}

func TestWorkOutboxNudgeNilIsSilent(t *testing.T) {
	SetWorkOutboxNudge(nil)
	tx := &communicationTx{
		resolveRepository: func(model.Kind) (communicationRepository, error) {
			return &nudgeStubRepo{}, nil
		},
	}
	if _, err := tx.create(context.Background(), workOutboxKind, model.Record{}); err != nil {
		t.Fatalf("create with no nudge registered must just work: %v", err)
	}
}
