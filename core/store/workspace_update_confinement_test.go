// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import (
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// workspaceUpdateSpyRepo records Get/Update/UpdateAtTransactionTime so a
// confined decorator can be shown not to invoke the raw writer on a foreign
// stored row. Rows are synthetic in-memory records.
type workspaceUpdateSpyRepo struct {
	workspaceCapabilityTestRepo
	stored  model.Record
	getErr  error
	gets    int
	updates int
	stamped int
}

func (s *workspaceUpdateSpyRepo) Get(_ context.Context, id model.ID) (model.Record, error) {
	s.gets++
	if s.getErr != nil {
		return nil, s.getErr
	}
	if s.stored == nil || s.stored.String(model.ColID) != id.String() {
		return nil, ErrNotFound
	}
	out := model.Record{}
	for k, v := range s.stored {
		out[k] = v
	}
	return out, nil
}

func (s *workspaceUpdateSpyRepo) Update(_ context.Context, rec model.Record) (model.Record, error) {
	s.updates++
	return rec, nil
}

type workspaceUpdateSpyStampedRepo struct {
	workspaceUpdateSpyRepo
}

func (s *workspaceUpdateSpyStampedRepo) UpdateAtTransactionTime(
	_ context.Context, rec model.Record,
) (model.Record, error) {
	s.stamped++
	return rec, nil
}

func (s *workspaceUpdateSpyStampedRepo) CreateAtTransactionTime(
	context.Context, model.Record,
) (model.Record, error) {
	return nil, nil
}

func (s *workspaceUpdateSpyStampedRepo) CreateWithIDAtTransactionTime(
	context.Context, model.ID, model.Record,
) (model.Record, error) {
	return nil, nil
}

type workspaceTypedUpdateSpy struct {
	stored  model.Agent
	getErr  error
	gets    int
	updates int
}

func (s *workspaceTypedUpdateSpy) Get(_ context.Context, id model.ID) (model.Agent, error) {
	s.gets++
	if s.getErr != nil {
		return model.Agent{}, s.getErr
	}
	if s.stored.ID != id {
		return model.Agent{}, ErrNotFound
	}
	return s.stored, nil
}

func (s *workspaceTypedUpdateSpy) List(context.Context, model.Query) ([]model.Agent, model.Page, error) {
	return nil, model.Page{}, nil
}

func (s *workspaceTypedUpdateSpy) Create(_ context.Context, v model.Agent) (model.Agent, error) {
	return v, nil
}

func (s *workspaceTypedUpdateSpy) Update(_ context.Context, v model.Agent) (model.Agent, error) {
	s.updates++
	return v, nil
}

func (s *workspaceTypedUpdateSpy) Delete(context.Context, model.ID) error { return nil }

func workspaceUpdateIDSpec() model.WorkspaceLineageSpec {
	return model.WorkspaceLineageSpec{
		Column:   "workspace_id",
		Encoding: model.WorkspaceLineageID,
		Unset:    model.WorkspaceUnsetMeansDefault,
	}
}

func workspaceUpdateSlugSpec() model.WorkspaceLineageSpec {
	return model.WorkspaceLineageSpec{
		Column:   "workspace_slug",
		Encoding: model.WorkspaceLineageSlug,
		Unset:    model.WorkspaceUnsetHidden,
	}
}

func TestConfinedGenericUpdateRequiresStoredAndProposedLineage(t *testing.T) {
	ctx := context.Background()
	home := model.NewID()
	foreign := model.NewID()
	def := model.NewID()
	rowID := model.NewID()
	spec := workspaceUpdateIDSpec()
	homeBound := workspaceBoundary{id: home, defaultID: def}
	foreignBound := workspaceBoundary{id: foreign, defaultID: def}

	t.Run("inward relabel conceals foreign stored row", func(t *testing.T) {
		spy := &workspaceUpdateSpyRepo{stored: model.Record{
			model.ColID:      rowID.String(),
			model.ColVersion: int64(3),
			"workspace_id":   foreign.String(),
			"label":          "foreign",
		}}
		repo := confinedGenericRepo{raw: spy, b: homeBound, spec: spec}
		_, err := repo.Update(ctx, model.Record{
			model.ColID:      rowID.String(),
			model.ColVersion: int64(3),
			"workspace_id":   home.String(),
			"label":          "stolen",
		})
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("inward relabel = %v, want ErrNotFound", err)
		}
		if spy.updates != 0 {
			t.Fatalf("raw Update ran %d times on a foreign stored row", spy.updates)
		}
		if spy.gets == 0 {
			t.Fatal("stored row was not read before refusing")
		}
	})

	t.Run("stamped inward relabel conceals foreign stored row", func(t *testing.T) {
		spy := &workspaceUpdateSpyStampedRepo{workspaceUpdateSpyRepo: workspaceUpdateSpyRepo{stored: model.Record{
			model.ColID:      rowID.String(),
			model.ColVersion: int64(3),
			"workspace_id":   foreign.String(),
		}}}
		repo := confinedTransactionStampedGenericRepo{
			confinedGenericRepo: confinedGenericRepo{raw: spy, b: homeBound, spec: spec},
			stamped:             spy,
		}
		_, err := repo.UpdateAtTransactionTime(ctx, model.Record{
			model.ColID:      rowID.String(),
			model.ColVersion: int64(3),
			"workspace_id":   home.String(),
		})
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("stamped inward relabel = %v, want ErrNotFound", err)
		}
		if spy.stamped != 0 || spy.updates != 0 {
			t.Fatalf("raw stamped/plain writer ran stamped=%d updates=%d", spy.stamped, spy.updates)
		}
	})

	t.Run("outgoing reassignment is refused", func(t *testing.T) {
		spy := &workspaceUpdateSpyRepo{stored: model.Record{
			model.ColID:      rowID.String(),
			model.ColVersion: int64(1),
			"workspace_id":   home.String(),
		}}
		repo := confinedGenericRepo{raw: spy, b: homeBound, spec: spec}
		_, err := repo.Update(ctx, model.Record{
			model.ColID:      rowID.String(),
			model.ColVersion: int64(1),
			"workspace_id":   foreign.String(),
		})
		if !errors.Is(err, ErrWorkspaceConfinement) {
			t.Fatalf("outgoing reassignment = %v, want ErrWorkspaceConfinement", err)
		}
		if spy.updates != 0 {
			t.Fatalf("raw Update ran %d times on outgoing reassignment", spy.updates)
		}
	})

	t.Run("same-workspace update reaches the raw writer", func(t *testing.T) {
		spy := &workspaceUpdateSpyRepo{stored: model.Record{
			model.ColID:      rowID.String(),
			model.ColVersion: int64(2),
			"workspace_id":   home.String(),
			"label":          "home",
		}}
		repo := confinedGenericRepo{raw: spy, b: homeBound, spec: spec}
		got, err := repo.Update(ctx, model.Record{
			model.ColID:      rowID.String(),
			model.ColVersion: int64(2),
			"workspace_id":   home.String(),
			"label":          "renamed",
		})
		if err != nil {
			t.Fatalf("same-workspace update: %v", err)
		}
		if spy.updates != 1 {
			t.Fatalf("raw Update calls = %d, want 1", spy.updates)
		}
		if got.String("label") != "renamed" {
			t.Fatalf("updated label = %q", got.String("label"))
		}
	})

	t.Run("unset stored row is visible only on the default workspace", func(t *testing.T) {
		unsetID := model.NewID()
		spy := &workspaceUpdateSpyRepo{stored: model.Record{
			model.ColID: unsetID.String(), model.ColVersion: int64(1), "label": "legacy",
		}}
		defaultBound := workspaceBoundary{id: def, defaultID: def}
		onDefault := confinedGenericRepo{raw: spy, b: defaultBound, spec: spec}
		if _, err := onDefault.Update(ctx, model.Record{
			model.ColID: unsetID.String(), model.ColVersion: int64(1), "label": "kept",
		}); err != nil {
			t.Fatalf("default confined update of unset row: %v", err)
		}
		if spy.updates != 1 {
			t.Fatalf("default confined raw Update calls = %d, want 1", spy.updates)
		}
		spy.updates = 0
		onForeign := confinedGenericRepo{raw: spy, b: foreignBound, spec: spec}
		_, err := onForeign.Update(ctx, model.Record{
			model.ColID: unsetID.String(), model.ColVersion: int64(1),
			"workspace_id": foreign.String(), "label": "stolen-unset",
		})
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("non-default inward relabel of unset row = %v, want ErrNotFound", err)
		}
		if spy.updates != 0 {
			t.Fatalf("raw Update ran on unset row from a non-default confinement")
		}
	})

	t.Run("slug encoding uses stored slug not the incoming one", func(t *testing.T) {
		spec := workspaceUpdateSlugSpec()
		bound := workspaceBoundary{id: home, slug: "alpha", defaultID: def}
		spy := &workspaceUpdateSpyRepo{stored: model.Record{
			model.ColID: rowID.String(), model.ColVersion: int64(1),
			"workspace_slug": "bravo",
		}}
		repo := confinedGenericRepo{raw: spy, b: bound, spec: spec}
		_, err := repo.Update(ctx, model.Record{
			model.ColID: rowID.String(), model.ColVersion: int64(1),
			"workspace_slug": "alpha",
		})
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("slug inward relabel = %v, want ErrNotFound", err)
		}
		if spy.updates != 0 {
			t.Fatalf("raw Update ran on a foreign slug row")
		}
	})

	t.Run("backend get error does not run the writer", func(t *testing.T) {
		backend := errors.New("backend unavailable")
		spy := &workspaceUpdateSpyRepo{getErr: backend}
		repo := confinedGenericRepo{raw: spy, b: homeBound, spec: spec}
		_, err := repo.Update(ctx, model.Record{
			model.ColID: rowID.String(), "workspace_id": home.String(),
		})
		if !errors.Is(err, backend) {
			t.Fatalf("backend get = %v, want the backend error", err)
		}
		if spy.updates != 0 {
			t.Fatalf("raw Update ran after a backend Get error")
		}
	})

	t.Run("cancelled context does not run the writer", func(t *testing.T) {
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		spy := &workspaceUpdateSpyRepo{getErr: canceled.Err(), stored: model.Record{
			model.ColID: rowID.String(), "workspace_id": home.String(),
		}}
		repo := confinedGenericRepo{raw: spy, b: homeBound, spec: spec}
		_, err := repo.Update(canceled, model.Record{
			model.ColID: rowID.String(), "workspace_id": home.String(),
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled update = %v, want context.Canceled", err)
		}
		if spy.updates != 0 {
			t.Fatalf("raw Update ran on a cancelled context")
		}
	})
}

func TestConfinedTypedUpdateRequiresStoredAndProposedLineage(t *testing.T) {
	ctx := context.Background()
	home := model.NewID()
	foreign := model.NewID()
	def := model.NewID()
	agentID := model.NewID()
	bound := workspaceBoundary{id: home, defaultID: def}
	spy := &workspaceTypedUpdateSpy{stored: model.Agent{
		BaseFields:  model.BaseFields{ID: agentID, Version: 4},
		WorkspaceID: foreign,
		Name:        "other-ws",
	}}
	repo := confinedRepo[model.Agent]{
		raw: spy, b: bound, spec: agentLineage,
		workspaceOf: func(a model.Agent) model.ID { return a.WorkspaceID },
		idOf:        func(a model.Agent) model.ID { return a.ID },
	}
	_, err := repo.Update(ctx, model.Agent{
		BaseFields:  model.BaseFields{ID: agentID, Version: 4},
		WorkspaceID: home,
		Name:        "stolen",
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("typed inward relabel = %v, want ErrNotFound", err)
	}
	if spy.updates != 0 {
		t.Fatalf("typed raw Update ran %d times on a foreign stored agent", spy.updates)
	}

	spy.stored.WorkspaceID = home
	got, err := repo.Update(ctx, model.Agent{
		BaseFields:  model.BaseFields{ID: agentID, Version: 4},
		WorkspaceID: home,
		Name:        "renamed",
	})
	if err != nil {
		t.Fatalf("typed same-workspace update: %v", err)
	}
	if spy.updates != 1 || got.Name != "renamed" {
		t.Fatalf("typed success updates=%d name=%q", spy.updates, got.Name)
	}

	spy.updates = 0
	_, err = repo.Update(ctx, model.Agent{
		BaseFields:  model.BaseFields{ID: agentID, Version: 4},
		WorkspaceID: foreign,
		Name:        "moved-out",
	})
	if !errors.Is(err, ErrWorkspaceConfinement) {
		t.Fatalf("typed outgoing = %v, want ErrWorkspaceConfinement", err)
	}
	if spy.updates != 0 {
		t.Fatalf("typed raw Update ran on outgoing reassignment")
	}
}

func TestConfinedGenericUpdatePreservesOptionalCapabilities(t *testing.T) {
	spec := workspaceUpdateIDSpec()
	bound := workspaceBoundary{id: model.NewID(), defaultID: model.NewID()}
	base := confinedGenericRepo{
		raw: workspaceCapabilityTestRepo{}, b: bound, spec: spec,
	}
	stamped := confinedTransactionStampedGenericRepo{
		confinedGenericRepo: base, stamped: workspaceCapabilityStampedRepo{},
	}
	both := confinedTransactionStampedRowLockingGenericRepo{
		confinedTransactionStampedGenericRepo: stamped,
		locker:                                workspaceCapabilityRowLocker{},
	}
	if _, ok := any(base).(TransactionStampedGenericRepo); ok {
		t.Fatal("base confined repo fabricated TransactionStampedGenericRepo")
	}
	if _, ok := any(stamped).(TransactionStampedGenericRepo); !ok {
		t.Fatal("stamped confined repo lost TransactionStampedGenericRepo")
	}
	if _, ok := any(stamped).(RowLocker[model.Record]); ok {
		t.Fatal("stamped confined repo fabricated RowLocker")
	}
	if _, ok := any(both).(TransactionStampedGenericRepo); !ok {
		t.Fatal("combined confined repo lost TransactionStampedGenericRepo")
	}
	if _, ok := any(both).(RowLocker[model.Record]); !ok {
		t.Fatal("combined confined repo lost RowLocker")
	}
}
