// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package azureactivity

import (
	"context"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// rgResource is the subset of a Resource Graph row we read: the ARM resource id
// (the natural ref) and its subscription. The query projects only these two
// columns, so no name, tags or properties are ever returned to the connector.
type rgResource struct {
	ID             string `json:"id"`
	SubscriptionID string `json:"subscriptionId"`
}

// resourceGraphRequest is the Resource Graph query body. The query is fixed and
// projects only id + subscriptionId (minimal data); options page via $skipToken
// and return objects (not the default table format).
type resourceGraphRequest struct {
	Subscriptions []string          `json:"subscriptions"`
	Query         string            `json:"query"`
	Options       resourceGraphOpts `json:"options"`
}

// resourceGraphOpts uses the dollar-prefixed field names the Resource Graph API
// requires per OData convention ($top, $skipToken) — not typos.
type resourceGraphOpts struct {
	Top          int    `json:"$top"`
	ResultFormat string `json:"resultFormat"`
	SkipToken    string `json:"$skipToken,omitempty"`
}

// resourceGraphQuery projects the minimum: the ARM id and its subscription.
const resourceGraphQuery = "Resources | project id, subscriptionId"

// gatherInventory emits the estate topology: tenant ⊳ subscription (when the
// tenant id is known) and subscription ⊳ resource (every resource the Resource
// Graph returns for the scoped subscriptions). Edges are emitted in a
// deterministic order; ctx is honored between pages.
func (s *Source) gatherInventory(ctx context.Context, sink sdk.Sink, subs []string, at time.Time) error {
	var edges []model.EdgeObservation

	if s.cfg.tenantID != "" {
		for _, sub := range subs {
			edges = append(edges, inventoryEdge(originTenant, s.cfg.tenantID, resSubscription, sub, at))
		}
	}

	resources, truncated, err := s.queryResourceGraph(ctx, subs)
	if err != nil {
		return err
	}
	for _, r := range resources {
		if r.ID == "" || r.SubscriptionID == "" {
			continue
		}
		edges = append(edges, inventoryEdge(originSubscription, r.SubscriptionID, resResource, strings.ToLower(r.ID), at))
	}

	sortEdges(edges)
	for _, e := range edges {
		if err := emit(ctx, sink, e); err != nil {
			return err
		}
	}
	// A Resource Graph that stopped at max_pages left resources undiscovered:
	// signal the partial coverage honestly (never a silent cap), after emitting
	// what we have.
	if truncated {
		if err := emit(ctx, sink, coverageFinding(subjectInventory, s.tenantRef(),
			"Azure Resource Graph inventory partial: stopped at max_pages — raise max_pages for full coverage", at)); err != nil {
			return err
		}
	}
	return nil
}

// queryResourceGraph runs the projection query across the scoped subscriptions,
// paginating by $skipToken up to max_pages. It returns the rows and a truncated
// flag: a $skipToken still present at max_pages means the estate exceeded the
// page budget, which the caller surfaces as a coverage finding (raise max_pages)
// — never a silent cap.
func (s *Source) queryResourceGraph(ctx context.Context, subs []string) (out []rgResource, truncated bool, err error) {
	skip := ""
	q := url.Values{"api-version": {resourceGraphAPIVersion}}
	for page := 0; page < s.cfg.maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		req := resourceGraphRequest{
			Subscriptions: subs,
			Query:         resourceGraphQuery,
			Options:       resourceGraphOpts{Top: 1000, ResultFormat: "objectArray", SkipToken: skip},
		}
		var resp struct {
			Data      []rgResource `json:"data"`
			SkipToken string       `json:"$skipToken"`
		}
		if err := s.postJSON(ctx, "/providers/Microsoft.ResourceGraph/resources", q, req, &resp); err != nil {
			return nil, false, err
		}
		out = append(out, resp.Data...)
		if resp.SkipToken == "" {
			break
		}
		skip = resp.SkipToken
		if page == s.cfg.maxPages-1 {
			truncated = true // more resources remain beyond the page budget.
		}
	}
	return out, truncated, nil
}

// sortEdges orders edges by resource kind, resource ref, then origin ref for a
// deterministic emit order, so golden tests are stable regardless of API page or
// map iteration ordering.
func sortEdges(edges []model.EdgeObservation) {
	sort.SliceStable(edges, func(i, j int) bool {
		if edges[i].ResourceKind != edges[j].ResourceKind {
			return edges[i].ResourceKind < edges[j].ResourceKind
		}
		if edges[i].ResourceRef != edges[j].ResourceRef {
			return edges[i].ResourceRef < edges[j].ResourceRef
		}
		return edges[i].OriginRef < edges[j].OriginRef
	})
}
