// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// costs.go implements the billed-cost stream: POST /teams/filtered-usage-events paginated
// across the lookback window → one model.CostSample per CHARGEABLE Cursor agent event.
// The monetary amount is the provider's OWN authoritative chargedCents (model cost +
// Cursor token rate; provenance=billed), never re-derived from token prices; the token
// breakdown is carried for FinOps, and the developer/service account + model are attributed
// for per-developer/per-team chargeback. CostType="cursor" keeps Cursor agent spend distinct
// from the underlying model providers it routes to.
package cursor

import (
	"context"
	"net/url"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// gatherUsage paginates the filtered usage events across the lookback window and emits one
// billed CostSample per chargeable event. byEmail attributes each event to a stable member
// id. On a plan-gated (403/404) surface it degrades to a posture finding and returns nil.
func (s *Source) gatherUsage(ctx context.Context, sink sdk.Sink, byEmail map[string]memberEntry) error {
	for page := 1; page <= s.maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		var resp usageResponse
		body := usageRequest{
			StartDate: s.startMillis(),
			EndDate:   s.nowMillis(),
			Page:      page,
			PageSize:  s.pageSize,
		}
		if err := s.client.postJSON(ctx, usagePath, body, &resp); err != nil {
			if isUnavailable(err) {
				return sink.Emit(ctx, s.unavailableFinding("Usage Events", usagePath))
			}
			return err
		}
		for _, e := range resp.UsageEvents {
			cs, ok := s.usageCostSample(e, byEmail)
			if !ok {
				continue
			}
			if err := sink.Emit(ctx, cs); err != nil {
				return err
			}
		}
		if len(resp.UsageEvents) == 0 || !resp.Pagination.HasNextPage {
			return nil
		}
	}
	// Loop exhausted max_pages while the API still reports a next page: emit a partial-
	// coverage health finding rather than silently dropping billed spend.
	return sink.Emit(ctx, s.coverageFinding("Usage Events", usagePath))
}

// usageCostSample turns one usage event into a billed CostSample. ok is false for an event
// that is not chargeable (nothing to attribute) — a non-charged event carries no spend. The
// amount is chargedCents (authoritative billed), converted cents→micro-USD; the token
// breakdown comes from tokenUsage when the call was token-based (guarded nil).
func (s *Source) usageCostSample(e usageEvent, byEmail map[string]memberEntry) (model.CostSample, bool) {
	if !e.IsChargeable {
		return model.CostSample{}, false
	}
	u := modelprovider.Usage{
		ProviderRef: modelprovider.ProviderCursor,
		ModelRef:    e.Model,
		OccurredAt:  millisTime(e.Timestamp),
		Actor:       s.actorRef(e, byEmail),
		Gateway:     model.GatewayDirect,
		Provenance:  model.ProvenanceBilled,
		CostType:    costTypeCursor,
	}
	if e.TokenUsage != nil {
		u.InputTokens = e.TokenUsage.InputTokens
		u.OutputTokens = e.TokenUsage.OutputTokens
		u.CacheReadTokens = e.TokenUsage.CacheReadTokens
		u.CacheWriteTokens = e.TokenUsage.CacheWriteTokens
	}
	return modelprovider.ToCostSampleWithCost(u, centsToMicroUSD(e.ChargedCents)), true
}

// actorRef resolves the "who" dimension for an event, preferring a stable non-PII id:
//   - a service-account event → "svc:<serviceAccountId>" (already non-PII);
//   - attribute_email set → the raw developer email (operator opt-in to PII attribution);
//   - else the member id resolved from the email (stable, non-PII);
//   - else a stable pseudonym (hash of the email) so an unmapped developer still carries an
//     id, never PII, and never an empty actor (docs/SECURITY-HARDENING.md).
func (s *Source) actorRef(e usageEvent, byEmail map[string]memberEntry) string {
	if e.ServiceAccountID != "" {
		return "svc:" + e.ServiceAccountID
	}
	if e.UserEmail == "" {
		return ""
	}
	if s.attributeEmail {
		return e.UserEmail
	}
	if m, ok := byEmail[lowered(e.UserEmail)]; ok && m.ID != "" {
		return m.ID
	}
	return "u:" + redact.Hash(e.UserEmail)
}

// startQuery is the audit lookback window as Unix-millis query params (the audit endpoint
// takes the same epoch-millis convention as the usage endpoint).
func (s *Source) auditWindow(q url.Values) {
	q.Set("startTime", millisString(s.startMillis()))
	q.Set("endTime", millisString(s.nowMillis()))
}
