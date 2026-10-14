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

type storedNHIScope struct {
	store.Scope
	repo store.GenericRepo
	err  error
}

func (s storedNHIScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	if kind != nhiLifecycleKind {
		panic("unexpected kind")
	}
	return s.repo, s.err
}

type storedNHIRepo struct {
	store.GenericRepo
	calls int
	late  bool
	err   error
}

func (r *storedNHIRepo) List(_ context.Context, q model.Query) ([]model.Record, model.Page, error) {
	r.calls++
	if q.Cursor == "" {
		return []model.Record{newLifecycleRecord("ordinary", "test", "")}, model.Page{HasMore: true, Cursor: "next"}, nil
	}
	if q.Cursor != "next" {
		panic("unexpected cursor")
	}
	if r.err != nil {
		return nil, model.Page{}, r.err
	}
	rec := newLifecycleRecord("later", "test", "")
	if r.late {
		rec[colNHIRotatedAt] = "malformed but present protection metadata"
	}
	return []model.Record{rec}, model.Page{}, nil
}
func TestStoredNHIRequiresBusinessDrainsPagesAndPropagatesReadErrors(t *testing.T) {
	cause := errors.New("fixture read failure")
	for _, tc := range []struct {
		name string
		late bool
		err  error
		want bool
	}{
		{"protection after first page", true, nil, true},
		{"ordinary rows", false, nil, false},
		{"later page read failure", false, cause, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &storedNHIRepo{late: tc.late, err: tc.err}
			got, err := StoredNHIRequiresBusiness(context.Background(), storedNHIScope{repo: repo})
			if got != tc.want || !errors.Is(err, tc.err) || repo.calls != 2 {
				t.Fatalf("required=%v err=%v pages=%d", got, err, repo.calls)
			}
		})
	}
	if _, err := StoredNHIRequiresBusiness(context.Background(), storedNHIScope{err: cause}); !errors.Is(err, cause) {
		t.Fatalf("repository error lost: %v", err)
	}
}
