// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// scriptedPage is one answer of a scripted listing.
type scriptedPage struct {
	rows []int
	page model.Page
	err  error
}

// scriptedList answers the pages in order and records the cursor of each call.
// A call past the script fails the test instead of looping.
func scriptedList(t *testing.T, pages []scriptedPage, cursors *[]string) func(context.Context, model.Query) ([]int, model.Page, error) {
	t.Helper()
	return func(_ context.Context, q model.Query) ([]int, model.Page, error) {
		i := len(*cursors)
		*cursors = append(*cursors, q.Cursor)
		if i >= len(pages) {
			t.Errorf("list called %d times for a script of %d pages", i+1, len(pages))
			return nil, model.Page{}, errors.New("script exhausted")
		}
		return pages[i].rows, pages[i].page, pages[i].err
	}
}

func TestWalkPagesRefusesAMissingOrRepeatedCursor(t *testing.T) {
	t.Parallel()

	t.Run("a complete listing is visited in order", func(t *testing.T) {
		var cursors []string
		list := scriptedList(t, []scriptedPage{
			{rows: []int{1, 2}, page: model.Page{Cursor: "c1", HasMore: true}},
			{rows: []int{3}, page: model.Page{Cursor: "c2", HasMore: true}},
			{rows: []int{4}, page: model.Page{}},
		}, &cursors)
		var got []int
		err := WalkPages(context.Background(), list, model.Query{Limit: 2}, UnboundedRows, func(rows []int) error {
			got = append(got, rows...)
			return nil
		})
		if err != nil {
			t.Fatalf("WalkPages: %v", err)
		}
		if want := []int{1, 2, 3, 4}; !reflect.DeepEqual(got, want) {
			t.Fatalf("visited %v, want %v", got, want)
		}
		if want := []string{"", "c1", "c2"}; !reflect.DeepEqual(cursors, want) {
			t.Fatalf("cursors %q, want %q", cursors, want)
		}
	})

	t.Run("more rows without a cursor", func(t *testing.T) {
		var cursors []string
		list := scriptedList(t, []scriptedPage{
			{rows: []int{1}, page: model.Page{HasMore: true}},
		}, &cursors)
		visits := 0
		err := WalkPages(context.Background(), list, model.Query{}, UnboundedRows, func([]int) error {
			visits++
			return nil
		})
		if !errors.Is(err, ErrPageContinuation) {
			t.Fatalf("error = %v, want %v", err, ErrPageContinuation)
		}
		if visits != 0 {
			t.Fatalf("visits = %d, want 0: a page without a continuation is never visited", visits)
		}
	})

	t.Run("a repeated cursor", func(t *testing.T) {
		var cursors []string
		list := scriptedList(t, []scriptedPage{
			{rows: []int{1}, page: model.Page{Cursor: "c1", HasMore: true}},
			{rows: []int{2}, page: model.Page{Cursor: "c1", HasMore: true}},
		}, &cursors)
		visits := 0
		err := WalkPages(context.Background(), list, model.Query{}, UnboundedRows, func([]int) error {
			visits++
			return nil
		})
		if !errors.Is(err, ErrPageContinuation) {
			t.Fatalf("error = %v, want %v", err, ErrPageContinuation)
		}
		if visits != 1 {
			t.Fatalf("visits = %d, want 1", visits)
		}
	})

	t.Run("a cursor equal to the one it started from", func(t *testing.T) {
		var cursors []string
		list := scriptedList(t, []scriptedPage{
			{rows: []int{1}, page: model.Page{Cursor: "c0", HasMore: true}},
		}, &cursors)
		err := WalkPages(context.Background(), list, model.Query{Cursor: "c0"}, UnboundedRows, func([]int) error { return nil })
		if !errors.Is(err, ErrPageContinuation) {
			t.Fatalf("error = %v, want %v", err, ErrPageContinuation)
		}
	})

	t.Run("a failed page answers its error", func(t *testing.T) {
		failed := errors.New("page failed")
		var cursors []string
		list := scriptedList(t, []scriptedPage{
			{rows: []int{1}, page: model.Page{Cursor: "c1", HasMore: true}},
			{err: failed},
		}, &cursors)
		err := WalkPages(context.Background(), list, model.Query{}, UnboundedRows, func([]int) error { return nil })
		if !errors.Is(err, failed) {
			t.Fatalf("error = %v, want %v", err, failed)
		}
	})
}

func TestWalkPagesRefusesCapacityBeforeVisit(t *testing.T) {
	t.Parallel()

	t.Run("a listing of exactly the budget completes", func(t *testing.T) {
		var cursors []string
		list := scriptedList(t, []scriptedPage{
			{rows: []int{1, 2, 3}, page: model.Page{Cursor: "c1", HasMore: true}},
			{rows: []int{4, 5, 6}, page: model.Page{}},
		}, &cursors)
		seen := 0
		err := WalkPages(context.Background(), list, model.Query{}, 6, func(rows []int) error {
			seen += len(rows)
			return nil
		})
		if err != nil {
			t.Fatalf("WalkPages: %v", err)
		}
		if seen != 6 {
			t.Fatalf("visited %d rows, want 6", seen)
		}
	})

	t.Run("the page that crosses the budget is never visited", func(t *testing.T) {
		var cursors []string
		list := scriptedList(t, []scriptedPage{
			{rows: []int{1, 2, 3}, page: model.Page{Cursor: "c1", HasMore: true}},
			{rows: []int{4, 5, 6}, page: model.Page{}},
		}, &cursors)
		seen := 0
		err := WalkPages(context.Background(), list, model.Query{}, 5, func(rows []int) error {
			seen += len(rows)
			return nil
		})
		if !errors.Is(err, ErrPageCapacity) {
			t.Fatalf("error = %v, want %v", err, ErrPageCapacity)
		}
		if seen != 3 {
			t.Fatalf("visited %d rows, want 3: the crossing page must be refused before the visit", seen)
		}
	})

	t.Run("an empty listing fits a zero budget", func(t *testing.T) {
		var cursors []string
		list := scriptedList(t, []scriptedPage{{page: model.Page{}}}, &cursors)
		if err := WalkPages(context.Background(), list, model.Query{}, 0, func([]int) error { return nil }); err != nil {
			t.Fatalf("WalkPages: %v", err)
		}
	})

	t.Run("a visitor error stops the walk", func(t *testing.T) {
		stop := errors.New("visitor stops")
		var cursors []string
		list := scriptedList(t, []scriptedPage{
			{rows: []int{1}, page: model.Page{Cursor: "c1", HasMore: true}},
			{rows: []int{2}, page: model.Page{}},
		}, &cursors)
		err := WalkPages(context.Background(), list, model.Query{}, UnboundedRows, func([]int) error { return stop })
		if !errors.Is(err, stop) {
			t.Fatalf("error = %v, want %v", err, stop)
		}
		if len(cursors) != 1 {
			t.Fatalf("list calls = %d, want 1", len(cursors))
		}
	})
}

func TestWalkPagesRefusesAnEmptyPageThatAnnouncesMore(t *testing.T) {
	t.Parallel()

	t.Run("no rows with a fresh cursor", func(t *testing.T) {
		var cursors []string
		list := scriptedList(t, []scriptedPage{
			{rows: []int{1}, page: model.Page{Cursor: "c1", HasMore: true}},
			{rows: nil, page: model.Page{Cursor: "c2", HasMore: true}},
		}, &cursors)
		visits := 0
		err := WalkPages(context.Background(), list, model.Query{}, UnboundedRows, func([]int) error {
			visits++
			return nil
		})
		if !errors.Is(err, ErrPageContinuation) {
			t.Fatalf("error = %v, want %v: a page with no rows that announces more makes no progress", err, ErrPageContinuation)
		}
		if visits != 1 || len(cursors) != 2 {
			t.Fatalf("visits = %d, list calls = %d; want 1 and 2: the empty page is refused before its visit", visits, len(cursors))
		}
	})

	t.Run("an empty final page completes", func(t *testing.T) {
		var cursors []string
		list := scriptedList(t, []scriptedPage{
			{rows: []int{1, 2}, page: model.Page{Cursor: "c1", HasMore: true}},
			{rows: nil, page: model.Page{}},
		}, &cursors)
		var got []int
		err := WalkPages(context.Background(), list, model.Query{}, UnboundedRows, func(rows []int) error {
			got = append(got, rows...)
			return nil
		})
		if err != nil {
			t.Fatalf("WalkPages: %v", err)
		}
		if want := []int{1, 2}; !reflect.DeepEqual(got, want) || len(cursors) != 2 {
			t.Fatalf("visited %v in %d list calls, want %v in 2", got, len(cursors), want)
		}
	})
}

func TestWalkPagesStopsWhenTheContextIsCancelled(t *testing.T) {
	t.Parallel()

	t.Run("between two pages", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var cursors []string
		list := scriptedList(t, []scriptedPage{
			{rows: []int{1}, page: model.Page{Cursor: "c1", HasMore: true}},
			{rows: []int{2}, page: model.Page{}},
		}, &cursors)
		visits := 0
		err := WalkPages(ctx, list, model.Query{}, UnboundedRows, func([]int) error {
			visits++
			cancel()
			return nil
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want %v: a cancelled walk stops before its next page", err, context.Canceled)
		}
		if visits != 1 || len(cursors) != 1 {
			t.Fatalf("visits = %d, list calls = %d; want 1 and 1", visits, len(cursors))
		}
	})

	t.Run("before the first page", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		var cursors []string
		list := scriptedList(t, []scriptedPage{{rows: []int{1}, page: model.Page{}}}, &cursors)
		err := WalkPages(ctx, list, model.Query{}, UnboundedRows, func([]int) error { return nil })
		if !errors.Is(err, context.Canceled) || len(cursors) != 0 {
			t.Fatalf("error = %v after %d list calls, want %v after none", err, len(cursors), context.Canceled)
		}
	})
}
