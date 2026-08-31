// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package notify

import (
	"context"
	"strings"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// SearchKinds contributes notification routes to the federated console search
//. Gated on the SAME read permission as GET /routes, so search can never
// widen what a caller could already list. Results carry name + enabled state
// only — never destinations or match criteria.
func (m *Module) SearchKinds() []api.SearchKind {
	return []api.SearchKind{{
		Kind:       "notify.route",
		Permission: permRouteRead,
		Search:     m.searchRoutes,
	}}
}

func (m *Module) searchRoutes(ctx context.Context, mc api.ModuleContext, q string, limit int) ([]api.SearchResult, error) {
	var out []api.SearchResult
	err := mc.Data.View(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(routeKind)
		if err != nil {
			return err
		}
		recs, _, err := repo.List(ctx, model.Query{Limit: searchScanLimit})
		if err != nil {
			return err
		}
		for _, rec := range recs {
			name := rec.String(colName)
			if !strings.Contains(strings.ToLower(name), q) {
				continue
			}
			detail := "disabled"
			if rec.Bool(colEnabled) {
				detail = "enabled"
			}
			out = append(out, api.SearchResult{
				Kind: "notify.route", ID: rec.String(model.ColID), Name: name, Detail: detail,
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

// searchScanLimit bounds the per-request scan (mirrors core/api search.go).
const searchScanLimit = 500
