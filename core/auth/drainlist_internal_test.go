// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

func TestDrainListTerminatesNormally(t *testing.T) {
	t.Parallel()

	var queries []model.Query
	list := func(_ context.Context, q model.Query) ([]int, model.Page, error) {
		queries = append(queries, q)
		if len(queries) == 1 {
			return []int{1, 2}, model.Page{Cursor: "page-2", HasMore: true}, nil
		}
		return []int{3}, model.Page{}, nil
	}

	got, err := drainList(context.Background(), list, model.Query{Limit: 7, Cursor: "ignored"})
	if err != nil {
		t.Fatalf("drainList: %v", err)
	}
	if want := []int{1, 2, 3}; !reflect.DeepEqual(got, want) {
		t.Fatalf("items = %v, want %v", got, want)
	}
	if len(queries) != 2 {
		t.Fatalf("list calls = %d, want 2", len(queries))
	}
	if queries[0].Limit != 1000 || queries[0].Cursor != "" {
		t.Fatalf("first query = %#v, want overridden limit and empty cursor", queries[0])
	}
	if queries[1].Cursor != "page-2" {
		t.Fatalf("second cursor = %q, want page-2", queries[1].Cursor)
	}
}

func TestDrainListRejectsIncompletePagination(t *testing.T) {
	t.Parallel()

	t.Run("page bound exhausted", func(t *testing.T) {
		calls := 0
		list := func(_ context.Context, _ model.Query) ([]int, model.Page, error) {
			calls++
			return []int{calls}, model.Page{
				Cursor:  fmt.Sprintf("page-%d", calls+1),
				HasMore: true,
			}, nil
		}

		got, err := drainList(context.Background(), list, model.Query{})
		if !errors.Is(err, errDrainListIncomplete) {
			t.Fatalf("error = %v, want %v", err, errDrainListIncomplete)
		}
		if got != nil {
			t.Fatalf("items = %v, want nil partial result", got)
		}
		if calls != 100 {
			t.Fatalf("list calls = %d, want 100", calls)
		}
	})

	t.Run("has more without cursor", func(t *testing.T) {
		calls := 0
		list := func(_ context.Context, _ model.Query) ([]int, model.Page, error) {
			calls++
			return []int{1}, model.Page{HasMore: true}, nil
		}

		got, err := drainList(context.Background(), list, model.Query{})
		if !errors.Is(err, errDrainListIncomplete) {
			t.Fatalf("error = %v, want %v", err, errDrainListIncomplete)
		}
		if got != nil {
			t.Fatalf("items = %v, want nil partial result", got)
		}
		if calls != 1 {
			t.Fatalf("list calls = %d, want 1", calls)
		}
	})

	t.Run("cursor does not advance", func(t *testing.T) {
		calls := 0
		list := func(_ context.Context, _ model.Query) ([]int, model.Page, error) {
			calls++
			return []int{calls}, model.Page{Cursor: "same", HasMore: true}, nil
		}

		got, err := drainList(context.Background(), list, model.Query{})
		if !errors.Is(err, errDrainListIncomplete) {
			t.Fatalf("error = %v, want %v", err, errDrainListIncomplete)
		}
		if got != nil {
			t.Fatalf("items = %v, want nil partial result", got)
		}
		if calls != 2 {
			t.Fatalf("list calls = %d, want 2", calls)
		}
	})
}
