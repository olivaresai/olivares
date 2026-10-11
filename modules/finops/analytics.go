// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// aggResult is a spend aggregate over a set of read-model rows.
type aggResult struct {
	Cost      int64
	Input     int64
	Output    int64
	Count     int
	Truncated bool
}

// dimensionColumn maps a budget/analytics dimension to its read-model column
// (empty for "global", which has no column — it is the whole set).
func dimensionColumn(dim string) string {
	switch dim {
	case "provider":
		return colProviderRef
	case "model":
		return colModelRef
	case "agent":
		return colAgentRef
	case "session":
		return colSessionRef
	case "team":
		return colTeam
	case "project":
		return colProject
	case "workspace":
		return colWorkspaceRef
	case "api_key":
		return colAPIKeyRef
	case "actor":
		return colActor
	case "service_tier":
		return colServiceTier
	case "context_window":
		return colContextWindow
	case "inference_geo":
		return colInferenceGeo
	case "gateway":
		return colGateway
	case "cost_type":
		return colCostType
	case "identity":
		return colIdentityRef
	case "cost_center":
		return colCostCenterRef
	}
	return ""
}

// estimatedFilter excludes billed rows (provenance="billed") from a default
// aggregation, so the estimated (granular, per-model) stream and the billed
// (cost_report, per-workspace/day) stream never double-count. Every ingested row
// carries a provenance (onCost defaults it to "estimated" via provenanceOf — it is
// never empty/NULL), so the SQL "<>" comparison keeps all estimated rows; only
// billed rows are dropped. Reconciliation reads billed rows explicitly instead.
func estimatedFilter() model.Filter {
	return model.Filter{Column: colProvenance, Op: model.OpNe, Value: provenanceBilled}
}

// windowFilters appends the occurred_at window bounds to extra filters, and
// excludes billed rows so default spend analytics aggregate the estimated stream
// only (no double-count with the billed reconciliation stream).
func windowFilters(extra []model.Filter, since time.Time, hasSince bool, until time.Time, hasUntil bool) []model.Filter {
	f := append([]model.Filter{estimatedFilter()}, extra...)
	if hasSince {
		f = append(f, model.Filter{Column: colOccurredAt, Op: model.OpGte, Value: model.NewTimestamp(since).String()})
	}
	if hasUntil {
		f = append(f, model.Filter{Column: colOccurredAt, Op: model.OpLte, Value: model.NewTimestamp(until).String()})
	}
	return f
}

// scanSamples iterates the read-model rows matching filters, paging by the default
// id keyset cursor, calling fn for each. It returns truncated=true if it hit the
// page cap (an honest signal the aggregate is partial, never a silent under-count).
func scanSamples(ctx context.Context, sc store.Scope, filters []model.Filter, fn func(model.Record)) (bool, error) {
	repo, err := sc.Ext(costSampleKind)
	if err != nil {
		return false, err
	}
	q := model.Query{Filters: filters, Limit: listCap}
	for pages := 0; ; pages++ {
		recs, page, err := repo.List(ctx, q)
		if err != nil {
			return false, err
		}
		for _, r := range recs {
			fn(r)
		}
		if !page.HasMore {
			return false, nil
		}
		if pages+1 >= maxScanPages {
			return true, nil
		}
		q.Cursor = page.Cursor
	}
}

// aggregatePeriod sums spend over the HALF-OPEN period window [pStart, pEnd) when
// bounded, or [pStart, +inf) for an unbounded ("total") period. The upper bound is
// EXCLUSIVE (OpLt) because periodEnd returns the next period's start instant: a
// sample landing exactly there belongs to the next period, not this one, so it
// must not be counted into both. This is what keeps budget evaluation and status
// scoped to a single period even when late/out-of-order or future-dated samples
// from other periods exist in the ledger.
func aggregatePeriod(ctx context.Context, sc store.Scope, extra []model.Filter, pStart time.Time, hasLower bool, pEnd time.Time, bounded bool) (aggResult, error) {
	// Exclude billed rows: budgets and forecasts run on the estimated (granular,
	// real-time) stream, not the billed cost_report stream (which would double-count).
	filters := append([]model.Filter{estimatedFilter()}, extra...)
	if hasLower {
		filters = append(filters, model.Filter{Column: colOccurredAt, Op: model.OpGte, Value: model.NewTimestamp(pStart).String()})
	}
	if bounded {
		filters = append(filters, model.Filter{Column: colOccurredAt, Op: model.OpLt, Value: model.NewTimestamp(pEnd).String()})
	}
	var res aggResult
	trunc, err := scanSamples(ctx, sc, filters, func(r model.Record) {
		res.Cost += r.Int(colCostMicroUSD)
		res.Input += r.Int(colInputTokens)
		res.Output += r.Int(colOutputTokens)
		res.Count++
	})
	res.Truncated = trunc
	return res, err
}

func aggregateGroupPeriod(ctx context.Context, sc store.Scope, userGroups func(string) ([]string, error), dimension, key string, pStart time.Time, hasLower bool, pEnd time.Time, bounded bool) (aggResult, error) {
	var (
		col  string
		refs []string
		err  error
	)
	switch dimension {
	case "agent_group":
		col = colAgentRef
		refs, err = agentGroupMemberRefs(ctx, sc, key)
	case "user_group":
		col = colActor
		if userGroups == nil {
			return aggResult{}, fmt.Errorf("finops: user_group %q members were not resolved", key)
		}
		refs, err = userGroups(key)
	default:
		return aggResult{}, fmt.Errorf("finops: unsupported group budget dimension %q", dimension)
	}
	if err != nil {
		return aggResult{}, err
	}
	var total aggResult
	for _, ref := range refs {
		part, err := aggregatePeriod(ctx, sc, []model.Filter{eq(col, ref)}, pStart, hasLower, pEnd, bounded)
		if err != nil {
			return aggResult{}, err
		}
		total.Cost += part.Cost
		total.Input += part.Input
		total.Output += part.Output
		total.Count += part.Count
		total.Truncated = total.Truncated || part.Truncated
	}
	return total, nil
}

func agentGroupMemberRefs(ctx context.Context, sc store.Scope, key string) ([]string, error) {
	g, err := agentGroupByKey(ctx, sc, key)
	if err != nil {
		return nil, err
	}
	members, err := listAgentGroupMembersByGroup(ctx, sc, g.ID)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	refs := make([]string, 0, len(members))
	for _, member := range members {
		a, err := sc.Agents().Get(ctx, member.AgentID)
		if err != nil {
			return nil, err
		}
		ref := a.ExternalID
		if ref == "" {
			ref = a.Name
		}
		if ref == "" || seen[ref] {
			continue
		}
		seen[ref] = true
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	return refs, nil
}

func agentGroupByKey(ctx context.Context, sc store.Scope, key string) (model.AgentGroup, error) {
	if g, ok, err := findOne(ctx, sc.AgentGroups(), eq("slug", key)); err != nil {
		return model.AgentGroup{}, err
	} else if ok {
		return g, nil
	}
	if id, err := model.ParseID(key); err == nil {
		return sc.AgentGroups().Get(ctx, id)
	}
	return model.AgentGroup{}, fmt.Errorf("finops: agent_group %q not found", key)
}

func listAgentGroupMembersByGroup(ctx context.Context, sc store.Scope, groupID model.ID) ([]model.AgentGroupMember, error) {
	var out []model.AgentGroupMember
	q := model.Query{Filters: []model.Filter{eq("group_id", groupID.String())}, Limit: listCap}
	for {
		recs, page, err := sc.AgentGroupMembers().List(ctx, q)
		if err != nil {
			return nil, err
		}
		out = append(out, recs...)
		if !page.HasMore || page.Cursor == "" {
			return out, nil
		}
		q.Cursor = page.Cursor
	}
}

func listAgentGroupMembersByAgent(ctx context.Context, sc store.Scope, agentID model.ID) ([]model.AgentGroupMember, error) {
	var out []model.AgentGroupMember
	q := model.Query{Filters: []model.Filter{eq("agent_id", agentID.String())}, Limit: listCap}
	for {
		recs, page, err := sc.AgentGroupMembers().List(ctx, q)
		if err != nil {
			return nil, err
		}
		out = append(out, recs...)
		if !page.HasMore || page.Cursor == "" {
			return out, nil
		}
		q.Cursor = page.Cursor
	}
}
