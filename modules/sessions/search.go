// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"strings"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// searchKindSession tags the sessions the console's one search box finds. Its ID is the
// session's address in the console (`run:<ref>`), so a hit opens that session.
const searchKindSession = "sessions.run"

// searchScanRuns bounds the runs one search reads, most recent first: the console's
// search is a bounded name scan (core/api/search.go), and the Sessions list, with its
// own paging, is the exhaustive surface.
const searchScanRuns = 500

// SearchKinds puts sessions in the federated console search (GET /v1/search), gated on
// the permission GET /runs requires, so the search never widens what a caller can list.
func (m *Module) SearchKinds() []api.SearchKind {
	return []api.SearchKind{{
		Kind:       searchKindSession,
		Permission: permRunRead,
		Search:     m.searchRuns,
	}}
}

// searchRuns finds a session by its name, by its reference and, for a caller who may
// read the conversation it was cut from, by the title of its first message: the title is
// shown under that same rule on a read of the run (showTitle). q is already lower-cased.
func (m *Module) searchRuns(ctx context.Context, mc api.ModuleContext, q string, limit int) ([]api.SearchResult, error) {
	var out []api.SearchResult
	err := mc.Data.View(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		recs, _, err := repo.List(ctx, model.Query{
			Limit: searchScanRuns,
			Sort:  []model.Sort{{Column: model.ColCreatedAt, Desc: true}},
		})
		if err != nil {
			return err
		}
		for _, rec := range recs {
			ref, name := rec.String(colRunRef), rec.String(colRunName)
			title := rec.String(colRunTitle)
			byTitle := title != "" && strings.Contains(strings.ToLower(title), q)
			if byTitle && !titleReadable(ctx, mc, rec) {
				byTitle, title = false, ""
			}
			if !byTitle && !strings.Contains(strings.ToLower(name), q) && !strings.Contains(strings.ToLower(ref), q) {
				continue
			}
			shown := name
			if shown == "" && title != "" && titleReadable(ctx, mc, rec) {
				shown = title
			}
			if shown == "" {
				shown = ref
			}
			out = append(out, api.SearchResult{
				Kind: searchKindSession, ID: "run:" + ref, Name: shown, Detail: m.toRunDTO(rec).State,
			})
			if limit > 0 && len(out) >= limit {
				return nil
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
