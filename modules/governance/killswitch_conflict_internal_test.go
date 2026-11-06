// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type cancellationConflictScope struct {
	store.Scope
	conflicts *int
	terminal  bool
}

func (s cancellationConflictScope) LockTransaction(ctx context.Context, key string) error {
	return s.Scope.(store.TransactionLocker).LockTransaction(ctx, key)
}

func (s cancellationConflictScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	repo, err := s.Scope.Ext(kind)
	if err == nil && kind == approvalKind {
		return cancellationConflictRepo{repo, s.conflicts, s.terminal}, nil
	}
	return repo, err
}

type cancellationConflictRepo struct {
	store.GenericRepo
	conflicts *int
	terminal  bool
}

func (r cancellationConflictRepo) Update(ctx context.Context, rec model.Record) (model.Record, error) {
	if *r.conflicts > 0 && rec.String(colStatus) == statusCanceled {
		*r.conflicts--
		latest, err := r.GenericRepo.Get(ctx, model.ID(rec.String(model.ColID)))
		if err != nil {
			return nil, err
		}
		latest[colEscalatedAt] = rec.String(colDecidedAt)
		if r.terminal {
			latest[colStatus] = statusRejected
		}
		if _, err := r.GenericRepo.Update(ctx, latest); err != nil {
			return nil, err
		}
		// The real store detects the stale version after the intervening metadata write.
	}
	return r.GenericRepo.Update(ctx, rec)
}

func TestKillSwitchCancellationConflict(t *testing.T) {
	for _, backend := range []store.Engine{store.EngineSQLite, store.EnginePostgres} {
		t.Run(string(backend), func(t *testing.T) {
			for _, tc := range []struct {
				name      string
				conflicts int
				terminal  bool
			}{
				{"escalation", 1, false}, {"decision", 1, true}, {"persistent", 10, false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					ctx := t.Context()
					m := New(WithClock(&intClock{t: intBase}))
					st, tenant := openRevisionStore(t, backend)
					var id model.ID
					if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
						dto, err := m.openApprovalRecord(ctx, sc, "operator", "human", "", createApprovalRequest{
							Action: "mcp.tool.call", SubjectKind: "agent", SubjectRef: "agent-1",
						}, 1, m.clock.Now())
						id = model.ID(dto.ID)
						return err
					}); err != nil {
						t.Fatal(err)
					}
					remaining := tc.conflicts
					var outcome ksEngageOutcome
					err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
						var err error
						outcome, err = m.engageKillSwitchLocked(ctx, cancellationConflictScope{sc, &remaining, tc.terminal}, ksEngageParams{
							ScopeKind: ksScopeEstate, Source: ksSourceOperator, Actor: "operator", ActorKind: "human", Reason: "contain incident",
						}, m.clock.Now())
						return err
					})
					if tc.name == "persistent" {
						if !errors.Is(err, store.ErrConflict) {
							t.Fatalf("persistent conflict silently accepted: %v", err)
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					wantStatus, wantCount := statusCanceled, int64(1)
					if tc.terminal {
						wantStatus, wantCount = statusRejected, 0
					}
					if err := st.View(ctx, tenant, func(sc store.Scope) error {
						repo, err := sc.Ext(approvalKind)
						if err != nil {
							return err
						}
						rec, err := repo.Get(ctx, id)
						if err == nil && rec.String(colStatus) != wantStatus {
							t.Errorf("pre-stop approval status = %s, want %s", rec.String(colStatus), wantStatus)
						}
						return err
					}); err != nil {
						t.Fatal(err)
					}
					if got := outcome.Record.Int(colKSRevokedCount); got != wantCount {
						t.Errorf("revoked = %d, want %d", got, wantCount)
					}
					if outcome.Record.String(colKSStatus) != ksStatusActive {
						t.Fatal("immediate stop not active")
					}
				})
			}
		})
	}
}
