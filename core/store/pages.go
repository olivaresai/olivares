// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/olivaresai/olivares/core/model"
)

// ErrPageContinuation refuses a listing that announced more rows without a fresh
// cursor, that repeated a cursor, or that announced more rows on a page with
// none. The listing is incomplete: a caller may run it again, and must never
// read what it saw as the complete set.
var ErrPageContinuation = errors.New("store: a listing did not continue")

// ErrPageCapacity refuses a listing that would read more rows than its budget.
// Running it again does not help until the data or the budget changes.
var ErrPageCapacity = errors.New("store: a listing exceeded its row budget")

// UnboundedRows is the budget of a listing that keeps no row limit. The
// continuation rule still applies to it.
const UnboundedRows = math.MaxInt

// WalkPages reads every page of a listing, in order, and hands each page to
// visit. It is the one continuation rule for callers that must see a complete
// listing. q.Limit is the page size and q.Cursor is where the walk starts.
//
// WalkPages holds one page at a time and keeps no reference to a page after
// visit returns, so visit copies only what it retains. A page that would take
// the rows read above maxRows answers ErrPageCapacity before visit sees it. A
// page that announces more rows with no rows of its own, or without a fresh
// cursor, makes no progress and answers ErrPageContinuation before visit sees
// it; an empty last page completes the listing. A failed page answers its
// error, and a cancelled ctx stops the walk before its next page.
func WalkPages[T any](ctx context.Context, list func(context.Context, model.Query) ([]T, model.Page, error),
	q model.Query, maxRows int, visit func([]T) error) error {
	seen := make(map[string]struct{})
	read := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		rows, page, err := list(ctx, q)
		if err != nil {
			return err
		}
		if len(rows) > maxRows-read {
			return fmt.Errorf("%w: %d rows read, a page of %d, a budget of %d", ErrPageCapacity, read, len(rows), maxRows)
		}
		read += len(rows)
		if page.HasMore {
			if len(rows) == 0 {
				return fmt.Errorf("%w: more rows announced after a page with none", ErrPageContinuation)
			}
			if page.Cursor == "" {
				return fmt.Errorf("%w: more rows announced without a cursor", ErrPageContinuation)
			}
			if _, repeated := seen[page.Cursor]; repeated || page.Cursor == q.Cursor {
				return fmt.Errorf("%w: a cursor repeated", ErrPageContinuation)
			}
			seen[page.Cursor] = struct{}{}
		}
		if err := visit(rows); err != nil {
			return err
		}
		if !page.HasMore {
			return nil
		}
		q.Cursor = page.Cursor
	}
}
