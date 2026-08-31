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

// activityEvent is the subset of an Azure Monitor Activity Log management event
// the connector reads. Only the fields needed to build the access edge are
// declared; the http request/response, properties, authorization scope/role,
// claims beyond the caller identifiers, and correlation ids are deliberately NOT
// read — the connector emits the edge, never the body (docs/SECURITY-HARDENING.md).
type activityEvent struct {
	EventTimestamp       string            `json:"eventTimestamp"`
	OperationName        localizedValue    `json:"operationName"`
	ResourceProviderName localizedValue    `json:"resourceProviderName"`
	Status               localizedValue    `json:"status"`
	Caller               string            `json:"caller"`
	Claims               map[string]string `json:"claims"`
}

// localizedValue is Azure's {value, localizedValue} pair; only the stable
// machine value is read (never the localized display string).
type localizedValue struct {
	Value string `json:"value"`
}

// oidClaim is the Entra object-identifier claim key. Preferring it makes the
// caller ref converge with the entra-agent roster, which keys on the Entra object
// id. appid and the caller string are fallbacks.
const oidClaim = "http://schemas.microsoft.com/identity/claims/objectidentifier"

// callerOf resolves the acting principal reference: the Entra object id (oid)
// when present (converges with entra-agent), else the application id (appid),
// else the event's caller string (a UPN or principal id). Only identifiers are
// read — never a token or any other claim.
func callerOf(e activityEvent) string {
	if v := strings.TrimSpace(e.Claims[oidClaim]); v != "" {
		return v
	}
	if v := strings.TrimSpace(e.Claims["appid"]); v != "" {
		return v
	}
	return strings.TrimSpace(e.Caller)
}

// gatherActivity reads the Activity Log management events for each subscription
// over the lookback window, mapping each completed (status=Succeeded) operation
// into one control-plane activity edge. It accumulates up to max_events across
// all subscriptions, emits in a deterministic order, and returns the first error
// so the caller records a single health finding. ctx is honored between pages.
func (s *Source) gatherActivity(ctx context.Context, sink sdk.Sink, subs []string, at time.Time) error {
	start := at.Add(-s.cfg.lookback)
	filter := "eventTimestamp ge '" + start.UTC().Format(time.RFC3339) +
		"' and eventTimestamp le '" + at.Format(time.RFC3339) + "'"

	var edges []model.EdgeObservation
	truncated := false
	for _, sub := range subs {
		if len(edges) >= s.cfg.maxEvents {
			break
		}
		subEdges, subTrunc, err := s.activityForSubscription(ctx, sub, filter, s.cfg.maxEvents-len(edges))
		if err != nil {
			return err
		}
		truncated = truncated || subTrunc
		edges = append(edges, subEdges...)
	}

	// Deterministic emit order: by resource, then principal, then observed time.
	sort.SliceStable(edges, func(i, j int) bool {
		if edges[i].ResourceRef != edges[j].ResourceRef {
			return edges[i].ResourceRef < edges[j].ResourceRef
		}
		if edges[i].OriginRef != edges[j].OriginRef {
			return edges[i].OriginRef < edges[j].OriginRef
		}
		return edges[i].ObservedAt.Before(edges[j].ObservedAt)
	})
	for _, e := range edges {
		if err := emit(ctx, sink, e); err != nil {
			return err
		}
	}
	if truncated {
		if err := emit(ctx, sink, coverageFinding(subjectActivity, s.tenantRef(),
			"Azure Activity Log partial: a subscription stopped at max_pages — raise max_pages or narrow lookback for full coverage", at)); err != nil {
			return err
		}
	}
	return nil
}

// activityForSubscription pages one subscription's Activity Log (following
// nextLink up to max_pages), mapping events to edges up to limit. truncated is
// true when the page budget was exhausted with a nextLink still pending (more
// events remain), as opposed to reaching the limit (the configured event budget,
// which the caller treats as an intentional cap, not truncation).
func (s *Source) activityForSubscription(ctx context.Context, sub, filter string, limit int) (edges []model.EdgeObservation, truncated bool, err error) {
	q := url.Values{"api-version": {activityLogAPIVersion}, "$filter": {filter}}
	full := strings.TrimRight(s.cfg.managementEndpoint, "/") +
		"/subscriptions/" + url.PathEscape(sub) + "/providers/microsoft.insights/eventtypes/management/values?" + q.Encode()

	for page := 0; page < s.cfg.maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		if len(edges) >= limit {
			break // event budget reached (intentional cap, not truncation).
		}
		var resp struct {
			Value    []activityEvent `json:"value"`
			NextLink string          `json:"nextLink"`
		}
		if err := s.getURL(ctx, full, &resp); err != nil {
			return nil, false, err
		}
		for _, ev := range resp.Value {
			edge, ok := s.mapEvent(ev)
			if !ok {
				continue
			}
			edges = append(edges, edge)
			if len(edges) >= limit {
				break
			}
		}
		if resp.NextLink == "" || len(edges) >= limit {
			break
		}
		full = resp.NextLink
		if page == s.cfg.maxPages-1 {
			truncated = true // page budget exhausted with more events pending.
		}
	}
	return edges, truncated, nil
}

// mapEvent maps one Activity Log event to an activity edge, or ok=false to skip
// it: a non-completed operation (Started/Failed/Accepted — only Succeeded is an
// effective, non-duplicated access), an unattributable event (no caller), an
// empty operation, or an unparseable timestamp. The mode comes verbatim from the
// operation's RBAC action; the confidence drops to approximate for a declared
// shared caller.
func (s *Source) mapEvent(ev activityEvent) (model.EdgeObservation, bool) {
	if !statusSucceeded(ev.Status.Value) {
		return model.EdgeObservation{}, false
	}
	caller := callerOf(ev)
	if caller == "" {
		return model.EdgeObservation{}, false
	}
	op := strings.TrimSpace(ev.OperationName.Value)
	if op == "" {
		return model.EdgeObservation{}, false
	}
	ts, ok := parseTime(ev.EventTimestamp)
	if !ok {
		return model.EdgeObservation{}, false
	}
	mode := classifyOperation(op)
	tool := providerOf(ev.ResourceProviderName.Value, op)
	return activityEdge(caller, op, tool, mode, confidenceFor(s.cfg.shared, caller), ts), true
}

// parseTime parses an Activity Log RFC3339 timestamp (with or without fractional
// seconds) and normalizes it to UTC, returning ok=false if no layout matches.
func parseTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, l := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
