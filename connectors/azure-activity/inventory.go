// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package azureactivity

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
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
const resourceGraphQuery = "Resources | project id, subscriptionId | order by id asc"

func configuredInventoryScope(subscriptions []string) model.InventoryScope {
	selectors := append([]string(nil), subscriptions...)
	for i := range selectors {
		selectors[i] = strings.ToLower(strings.TrimSpace(selectors[i]))
	}
	slices.Sort(selectors)
	selectors = slices.Compact(selectors)
	return model.InventoryScope{Contract: model.AzureInventoryContract, Family: resResource, Selectors: selectors}
}

func (s *Source) inventoryUnavailable(ctx context.Context, sink sdk.Sink, state, reason string, at time.Time) error {
	scope := configuredInventoryScope(s.cfg.subscriptions)
	report := model.InventoryCollectionReport{State: state, Reason: reason, ObservedUntil: at}
	if scope.Valid() {
		if err := sink.Emit(ctx, model.InventoryCollectionStart{Scope: scope, ObservedAt: at}); err != nil {
			return err
		}
		report.RequestedScope = scope.Fingerprint()
	}
	return sink.Emit(ctx, report)
}

// gatherInventory streams query members, then ordinary tenant/subscription
// topology. The buffered typed result describes only the Resource Graph query.
func (s *Source) gatherInventory(ctx context.Context, sink sdk.Sink, subs []string, at time.Time) error {
	scope := configuredInventoryScope(s.cfg.subscriptions)
	qualifiedScope := scope.Valid()
	if qualifiedScope {
		subs = scope.Selectors
		if err := sink.Emit(ctx, model.InventoryCollectionStart{Scope: scope, ObservedAt: at}); err != nil {
			return err
		}
	}
	var count int64
	invalidMember := false
	state, reason, queryErr := s.queryResourceGraph(ctx, subs, func(r rgResource) error {
		edge := inventoryEdge(originSubscription, strings.ToLower(r.SubscriptionID), resResource, strings.ToLower(r.ID), at)
		if qualifiedScope && scope.Contains(edge) {
			if err := sink.Emit(ctx, model.InventoryCollectionMember{Edge: edge}); err != nil {
				return err
			}
			count++
			return nil
		}
		if qualifiedScope {
			invalidMember = true
		}
		if r.ID != "" && r.SubscriptionID != "" {
			return sink.Emit(ctx, edge)
		}
		return nil
	})
	// Subscription topology remains an ordinary observation, outside query membership.
	if s.cfg.tenantID != "" {
		for _, sub := range subs {
			if err := emit(ctx, sink, inventoryEdge(originTenant, s.cfg.tenantID, resSubscription, sub, at)); err != nil {
				return err
			}
		}
	}
	if invalidMember && state == "complete" {
		state = "partial"
		reason = "scope_mismatch"
	}
	report := model.InventoryCollectionReport{State: state, Reason: reason, Count: count, ObservedUntil: time.Now().UTC()}
	if qualifiedScope {
		report.RequestedScope = scope.Fingerprint()
		if state == "complete" {
			report.FulfilledScope = scope.Fingerprint()
		}
	} else {
		report.State = "unknown"
		report.Reason = "scope_unproven"
		report.Count = 0
	}
	if err := sink.Emit(ctx, report); err != nil {
		return err
	}
	if report.State == "partial" {
		title := "Azure Resource Graph inventory partial"
		if report.Reason == "page_limit" {
			title += " at max_pages"
		}
		if err := sink.Emit(ctx, coverageFinding(subjectInventory, s.tenantRef(), title, at)); err != nil {
			return err
		}
	}
	return queryErr
}

// queryResourceGraph preserves observations page by page. All terminal fields
// are decoded independently so malformed metadata cannot erase useful rows.
// No paging sequence is an atomic provider snapshot.
func (s *Source) queryResourceGraph(ctx context.Context, subs []string, consume func(rgResource) error) (string, string, error) {
	skip := ""
	seen := map[string]bool{}
	lastID := ""
	var total, accumulated int64
	total = -1
	reason := ""
	pages := s.cfg.maxPages
	if pages > maximumPages {
		pages = maximumPages
	}
	q := url.Values{"api-version": {resourceGraphAPIVersion}}
	for page := 0; page < pages; page++ {
		if err := ctx.Err(); err != nil {
			return "partial", "canceled", err
		}
		req := resourceGraphRequest{Subscriptions: subs, Query: resourceGraphQuery, Options: resourceGraphOpts{Top: 1000, ResultFormat: "objectArray", SkipToken: skip}}
		var resp struct {
			Data      json.RawMessage `json:"data"`
			Count     json.RawMessage `json:"count"`
			Total     json.RawMessage `json:"totalRecords"`
			Truncated json.RawMessage `json:"resultTruncated"`
			Cursor    json.RawMessage `json:"$skipToken"`
		}
		if err := s.postJSON(ctx, "/providers/Microsoft.ResourceGraph/resources", q, req, &resp); err != nil {
			if accumulated == 0 {
				return "unavailable", "provider_error", err
			}
			return "partial", "provider_error", err
		}
		var rows []json.RawMessage
		if len(resp.Data) == 0 || string(resp.Data) == "null" || json.Unmarshal(resp.Data, &rows) != nil {
			return "partial", "invalid_response", nil
		}
		var pageCount, pageTotal int64
		var truncated, cursor string
		valid := len(resp.Count) > 0 && string(resp.Count) != "null" && json.Unmarshal(resp.Count, &pageCount) == nil && pageCount >= 0 && len(resp.Total) > 0 && string(resp.Total) != "null" && json.Unmarshal(resp.Total, &pageTotal) == nil && pageTotal >= 0 && json.Unmarshal(resp.Truncated, &truncated) == nil && (truncated == "true" || truncated == "false")
		if len(resp.Cursor) > 0 && (string(resp.Cursor) == "null" || json.Unmarshal(resp.Cursor, &cursor) != nil) {
			valid = false
		}
		if pageCount != int64(len(rows)) {
			valid = false
		}
		if total < 0 {
			total = pageTotal
		} else if total != pageTotal {
			valid = false
		}
		if !valid {
			reason = "invalid_response"
		}
		for _, raw := range rows {
			if accumulated >= model.MaxInventoryMembers {
				return "partial", "member_limit", nil
			}
			var r rgResource
			if err := json.Unmarshal(raw, &r); err != nil {
				reason = "invalid_response"
				accumulated++
				continue
			}
			accumulated++
			id := strings.ToLower(r.ID)
			sub := strings.ToLower(r.SubscriptionID)
			if id == "" || sub == "" || len(id) > 4096 || !strings.HasPrefix(id, "/subscriptions/"+sub+"/") || !slices.Contains(subs, sub) {
				reason = "scope_mismatch"
			}
			if id <= lastID {
				reason = "invalid_response"
			}
			lastID = id
			if err := consume(r); err != nil {
				return "partial", "sink_error", err
			}
		}
		if cursor != "" {
			if strings.TrimSpace(cursor) == "" {
				return "partial", "invalid_response", nil
			}
			if len(cursor) > 8192 || seen[cursor] {
				return "partial", "repeated_cursor", nil
			}
			seen[cursor] = true
			skip = cursor
			if page == pages-1 {
				return "partial", "page_limit", nil
			}
			continue // resultTruncated=false can still accompany a documented cursor.
		}
		if truncated != "false" || !valid || accumulated != total || reason != "" {
			if reason == "" {
				reason = "invalid_response"
			}
			return "partial", reason, nil
		}
		return "complete", "exhausted", nil
	}
	return "partial", "page_limit", errors.New("azure-activity: inventory page limit")
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
