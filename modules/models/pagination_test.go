// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

import (
	"context"
	"strconv"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// pagedLister is a fake extLister that serves records in fixed-size pages, exercising the
// keyset (cursor) walk that listAllExt performs for the AIBOM / model-card generators.
type pagedLister struct {
	pages [][]model.Record
	calls int
}

type typedPageItem struct {
	value string
}

func (p *pagedLister) List(_ context.Context, q model.Query) ([]model.Record, model.Page, error) {
	p.calls++
	idx := 0
	if q.Cursor != "" {
		idx, _ = strconv.Atoi(q.Cursor)
	}
	if idx >= len(p.pages) {
		return nil, model.Page{}, nil
	}
	recs := p.pages[idx]
	more := idx+1 < len(p.pages)
	cur := ""
	if more {
		cur = strconv.Itoa(idx + 1)
	}
	return recs, model.Page{Cursor: cur, HasMore: more}, nil
}

func full(n int) []model.Record {
	out := make([]model.Record, n)
	for i := range out {
		out[i] = model.Record{}
	}
	return out
}

// A generated AIBOM/model card must cover the WHOLE inventory: listAllExt walks past the
// first listCap page rather than silently truncating (the bug Codex flagged).
func TestListAllExtWalksAllPages(t *testing.T) {
	lister := &pagedLister{pages: [][]model.Record{full(listCap), full(listCap), full(7)}}
	got, err := listAllExt(context.Background(), lister)
	if err != nil {
		t.Fatalf("listAllExt: %v", err)
	}
	if want := 2*listCap + 7; len(got) != want {
		t.Fatalf("walked %d records, want %d — was the list truncated at listCap?", len(got), want)
	}
	if lister.calls != 3 {
		t.Fatalf("made %d List calls, want 3 (one per page)", lister.calls)
	}
}

// A single short page (or any page with no next cursor, e.g. a custom-sort query that
// never paginates) must terminate after one call — never loop forever.
func TestListAllExtStopsWithoutCursor(t *testing.T) {
	lister := &pagedLister{pages: [][]model.Record{{model.Record{}, model.Record{}}}}
	got, err := listAllExt(context.Background(), lister)
	if err != nil {
		t.Fatalf("listAllExt: %v", err)
	}
	if len(got) != 2 || lister.calls != 1 {
		t.Fatalf("got %d recs in %d calls, want 2 in 1", len(got), lister.calls)
	}
}

// Typed core repositories use the same page walker as extension repositories.
func TestListAllPagesWalksTypedPages(t *testing.T) {
	pages := [][]typedPageItem{{{value: "first"}}, {{value: "second"}, {value: "third"}}}
	calls := 0
	got, err := listAllPages(func(q model.Query) ([]typedPageItem, model.Page, error) {
		calls++
		if q.Limit != listCap {
			t.Fatalf("List limit = %d, want %d", q.Limit, listCap)
		}
		if len(q.Sort) != 0 {
			t.Fatalf("List sort = %#v, want default id ordering", q.Sort)
		}
		idx := 0
		if q.Cursor != "" {
			idx, _ = strconv.Atoi(q.Cursor)
		}
		more := idx+1 < len(pages)
		cursor := ""
		if more {
			cursor = strconv.Itoa(idx + 1)
		}
		return pages[idx], model.Page{Cursor: cursor, HasMore: more}, nil
	})
	if err != nil {
		t.Fatalf("listAllPages: %v", err)
	}
	want := []string{"first", "second", "third"}
	if len(got) != len(want) {
		t.Fatalf("walked %d typed records, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].value != want[i] {
			t.Fatalf("typed record %d = %q, want %q", i, got[i].value, want[i])
		}
	}
	if calls != len(pages) {
		t.Fatalf("made %d typed List calls, want %d", calls, len(pages))
	}
}

// The agent BOM walks id-ordered pages, then restores its historical
// artifact_class/name ordering in memory without disturbing equal-key id order.
func TestSortAgentArtifactRecordsAfterPageWalk(t *testing.T) {
	lister := &pagedLister{pages: [][]model.Record{
		{
			{model.ColID: "01", colAAClass: artifactClassMCPB, colAAName: "zeta"},
			{model.ColID: "02", colAAClass: artifactClassSkill, colAAName: "same"},
		},
		{
			{model.ColID: "03", colAAClass: artifactClassAgentsMD, colAAName: "alpha"},
			{model.ColID: "04", colAAClass: artifactClassMCPB, colAAName: "alpha"},
			{model.ColID: "05", colAAClass: artifactClassSkill, colAAName: "same"},
		},
	}}
	recs, err := listAllExt(context.Background(), lister)
	if err != nil {
		t.Fatalf("listAllExt: %v", err)
	}
	sortAgentArtifactRecords(recs)

	want := []string{
		artifactClassAgentsMD + "/alpha#03",
		artifactClassMCPB + "/alpha#04",
		artifactClassMCPB + "/zeta#01",
		artifactClassSkill + "/same#02",
		artifactClassSkill + "/same#05",
	}
	if len(recs) != len(want) {
		t.Fatalf("sorted %d records, want %d", len(recs), len(want))
	}
	for i, rec := range recs {
		got := rec.String(colAAClass) + "/" + rec.String(colAAName) + "#" + rec.String(model.ColID)
		if got != want[i] {
			t.Fatalf("sorted record %d = %q, want %q", i, got, want[i])
		}
	}
	if lister.calls != 2 {
		t.Fatalf("made %d List calls, want 2", lister.calls)
	}
}
