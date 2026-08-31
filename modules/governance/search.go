// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"strings"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// SearchKinds contributes governance policies to the federated console search
//. Gated on the SAME read permission as GET /policies, so search can
// never widen what a caller could already list. Results carry name, policy kind
// and enabled state only — never the policy spec.
func (m *Module) SearchKinds() []api.SearchKind {
	return []api.SearchKind{{
		Kind:       "governance.policy",
		Permission: permPolicyRead,
		Search:     m.searchPolicies,
	}}
}

func (m *Module) searchPolicies(ctx context.Context, mc api.ModuleContext, q string, limit int) ([]api.SearchResult, error) {
	var out []api.SearchResult
	err := mc.Data.View(ctx, func(sc store.Scope) error {
		list, _, err := sc.Policies().List(ctx, model.Query{Limit: searchScanLimit})
		if err != nil {
			return err
		}
		for _, p := range list {
			// Only the two governance kinds — other modules store their own
			// Policy rows (finops budgets, models routing), which this view
			// must not surface (same rule as handleListPolicies).
			if p.Kind != policyKindABAC && p.Kind != policyKindApproval {
				continue
			}
			if !strings.Contains(strings.ToLower(p.Name), q) {
				continue
			}
			detail := p.Kind + " · disabled"
			if p.Enabled {
				detail = p.Kind + " · enabled"
			}
			out = append(out, api.SearchResult{
				Kind: "governance.policy", ID: p.ID.String(), Name: p.Name, Detail: detail,
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
