// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package health

import (
	"context"
	"errors"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// eventsDefaultLimit is the page of a newest-first read without ?limit, the store's
// own default for the oldest-first one.
const eventsDefaultLimit = 100

// newestFirst orders events by when they happened, newest first, ties by id.
var newestFirst = []model.Sort{{Column: colEvOccurredAt, Desc: true}, {Column: model.ColID, Desc: true}}

// eventCursor is where a newest-first page ended: the occurred_at and id of its last
// event. The store's own cursor walks ids only, so this read keeps its own.
type eventCursor struct {
	at string
	id model.ID
}

var errEventCursor = errors.New("invalid cursor")

func (c eventCursor) String() string { return c.at + "," + c.id.String() }

func parseEventCursor(s string) (eventCursor, error) {
	at, id, ok := strings.Cut(s, ",")
	if !ok {
		return eventCursor{}, errEventCursor
	}
	ts, err := model.ParseTimestamp(at)
	if err != nil {
		return eventCursor{}, errEventCursor
	}
	ref, err := model.ParseID(id)
	if err != nil {
		return eventCursor{}, errEventCursor
	}
	return eventCursor{at: ts.String(), id: ref}, nil
}

// listEventsNewestFirst reads one page of events newest first after cursor (the zero
// cursor starts at the newest). The filters are ANDed, so "after" is two reads: the
// rest of the cursor's own instant (lower ids), then everything older.
func listEventsNewestFirst(ctx context.Context, repo store.GenericRepo, filters []model.Filter, limit int, after eventCursor) ([]model.Record, model.Page, error) {
	if limit <= 0 {
		limit = eventsDefaultLimit
	}
	var out []model.Record
	more := false
	older := filters
	if after.at != "" {
		same, page, err := repo.List(ctx, model.Query{
			Filters: append(append([]model.Filter{}, filters...),
				eq(colEvOccurredAt, after.at), model.Filter{Column: model.ColID, Op: model.OpLt, Value: after.id.String()}),
			Sort: newestFirst, Limit: limit,
		})
		if err != nil {
			return nil, model.Page{}, err
		}
		out, more = same, page.HasMore
		older = append(append([]model.Filter{}, filters...), model.Filter{Column: colEvOccurredAt, Op: model.OpLt, Value: after.at})
	}
	if !more {
		want := limit - len(out)
		rest, page, err := repo.List(ctx, model.Query{Filters: older, Sort: newestFirst, Limit: max(want, 1)})
		if err != nil {
			return nil, model.Page{}, err
		}
		if want == 0 {
			more = len(rest) > 0
		} else {
			out, more = append(out, rest...), page.HasMore
		}
	}
	page := model.Page{HasMore: more}
	if more && len(out) > 0 {
		last := out[len(out)-1]
		page.Cursor = eventCursor{at: last.String(colEvOccurredAt), id: model.ID(last.String(model.ColID))}.String()
	}
	return out, page, nil
}
