// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Stored cost-center mappings still attribute spend to legacy budget caps.
func mappingDimValue(attr *attribution, dim string) string {
	switch dim {
	case "team":
		return attr.Team
	case "workspace":
		return attr.WorkspaceRef
	case "project":
		return attr.Project
	case "agent":
		return attr.AgentRef
	case "provider":
		return attr.ProviderRef
	case "identity":
		return attr.IdentityRef
	}
	return ""
}

// resolveCostCenter resolves the cost center for a cost sample by querying the
// mapping rules. It checks each dimension that has a value in attr and picks
// the matching rule with the highest priority. If no rule matches, CostCenterRef
// is left empty (unmapped traffic).
func resolveCostCenter(ctx context.Context, sc store.Scope, attr *attribution) error {
	repo, err := sc.Ext(costCenterMappingKind)
	if err != nil {
		return err
	}

	// Build candidate lookups: for each dimension that has a value in the
	// attribution, check if a mapping rule exists.
	dims := []string{"team", "workspace", "project", "agent", "provider", "identity"}
	bestPriority := int64(-1)
	bestCCID := ""

	for _, dim := range dims {
		val := mappingDimValue(attr, dim)
		if val == "" {
			continue
		}
		recs, _, err := repo.List(ctx, model.Query{
			Filters: []model.Filter{
				eq(colCCMappingDimension, dim),
				eq(colCCMappingKey, val),
			},
			Limit: 1,
		})
		if err != nil {
			return err
		}
		if len(recs) == 0 {
			continue
		}
		prio := recs[0].Int(colCCMappingPriority)
		if prio > bestPriority {
			bestPriority = prio
			bestCCID = recs[0].String(colCCMappingCostCenterID)
		}
	}

	if bestCCID == "" {
		return nil
	}

	// Resolve the cost center code from the CC entity.
	ccRepo, err := sc.Ext(costCenterKind)
	if err != nil {
		return err
	}
	ccRec, err := ccRepo.Get(ctx, model.ID(bestCCID))
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return err
	}
	if ccRec.String(colCCStatus) != "active" {
		return nil
	}
	attr.CostCenterRef = ccRec.String(colCCCode)
	return nil
}

// isNotFound checks if an error is a store not-found error.
func isNotFound(err error) bool {
	return err != nil && err.Error() == "not found"
}

func resolveAgentID(ctx context.Context, sc store.Scope, agentRef string) (model.ID, bool, error) {
	if a, ok, err := findOne(ctx, sc.Agents(), eq("external_id", agentRef)); err != nil {
		return "", false, err
	} else if ok {
		return a.ID, true, nil
	}
	if a, ok, err := findOne(ctx, sc.Agents(), eq("name", agentRef)); err != nil {
		return "", false, err
	} else if ok {
		return a.ID, true, nil
	}
	return "", false, nil
}
