// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// billing.go implements xAI cost observation + FinOps posture from the read-only (GET)
// Management billing endpoints:
//
//   - Invoices (GET .../invoices): finalized historical spend → one BILLED CostSample per
//     invoice line (provenance=billed, CostType="xai"). Only finalized (non-PENDING)
//     invoices are emitted, so the in-progress cycle is not double-counted with the preview.
//   - Invoice preview (GET .../postpaid/invoice/preview): the current (unfinalized) cycle's
//     spend so far for a postpaid team → ESTIMATED CostSamples. 404 on a prepaid team → skip.
//   - Prepaid balance (GET .../prepaid/balance): remaining credit → an inventory finding,
//     and a low-balance posture when below the configured threshold. 404 on postpaid → skip.
//   - Spending limits (GET .../postpaid/spending-limits): the effective monthly ceiling →
//     an inventory finding, or a posture finding when NO limit is set (unexpected-charge
//     risk). 404 on a prepaid team → skip.
//
// Money is on the wire as decimal STRINGS; it is parsed to integer micro-USD, never via a
// guessed token price (the cost is the provider's own billed figure). Setting a spend limit
// is a MUTATION (out of scope; HITL-gated) — this connector only reads the posture.
package xai

import (
	"context"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// gatherBilling pulls billed cost + FinOps posture for the team. Each sub-surface degrades
// independently: a 403/404 (wrong billing mode / not entitled) is skipped, not fatal, so a
// prepaid team still gets its balance and a postpaid team still gets its preview/limits.
func (s *Source) gatherBilling(ctx context.Context, sink sdk.Sink, team string) error {
	if err := s.gatherInvoices(ctx, sink, team); err != nil {
		return err
	}
	if err := s.gatherPreview(ctx, sink, team); err != nil {
		return err
	}
	if err := s.gatherBalance(ctx, sink, team); err != nil {
		return err
	}
	return s.gatherSpendingLimits(ctx, sink, team)
}

// gatherInvoices emits one BILLED CostSample per line of each finalized (non-PENDING)
// invoice in the lookback window. The lookback is expressed as a `since` billing cycle
// (year/month) derived from the connector clock.
func (s *Source) gatherInvoices(ctx context.Context, sink sdk.Sink, team string) error {
	now := s.clock().UTC()
	// Normalize to the first of the month BEFORE shifting so a month-end clock cannot
	// overflow into the next month: AddDate would turn e.g. Mar-31 minus 1 month into
	// Mar-03 (Feb-31 normalized forward), silently skipping February. time.Date normalizes
	// the month arithmetic (incl. underflow across the year boundary) correctly with day=1.
	since := time.Date(now.Year(), now.Month()-time.Month(s.lookbackMonth), 1, 0, 0, 0, 0, time.UTC)
	q := url.Values{}
	q.Set("since.year", strconv.Itoa(since.Year()))
	q.Set("since.month", strconv.Itoa(int(since.Month())))
	var resp invoicesResponse
	if err := s.mgmtClient.GetJSON(ctx, "/v1/billing/teams/"+url.PathEscape(team)+"/invoices", q, &resp); err != nil {
		if isUnavailable(err) {
			return nil // not entitled / no invoices surface: skip honestly
		}
		return err
	}
	for _, inv := range resp.Invoices {
		if strings.EqualFold(inv.InvoiceStatus, "PENDING") || inv.InvoiceStatus == "" {
			continue // the in-progress cycle is covered by the preview; avoid double count
		}
		occurred := parseTime(inv.CreateTime)
		if occurred.IsZero() {
			occurred = s.clock().UTC()
		}
		for _, line := range inv.Lines {
			micro := decimalStringToMicroUSD(line.Amount)
			if micro == 0 {
				continue // a zero/credit line carries no spend to attribute
			}
			if err := sink.Emit(ctx, s.costSample(team, inv.InvoiceNumber, micro, occurred, model.ProvenanceBilled)); err != nil {
				return err
			}
		}
	}
	return nil
}

// gatherPreview emits one ESTIMATED CostSample per line of the current cycle's preview (a
// postpaid team's spend so far, not yet finalized). 404 (prepaid team) is skipped honestly.
func (s *Source) gatherPreview(ctx context.Context, sink sdk.Sink, team string) error {
	var resp previewResponse
	if err := s.mgmtClient.GetJSON(ctx, "/v1/billing/teams/"+url.PathEscape(team)+"/postpaid/invoice/preview", nil, &resp); err != nil {
		if isUnavailable(err) {
			return nil
		}
		return err
	}
	occurred := s.clock().UTC()
	for _, line := range resp.CoreInvoice.Lines {
		micro := decimalStringToMicroUSD(line.Amount)
		if micro == 0 {
			continue
		}
		if err := sink.Emit(ctx, s.costSample(team, "preview", micro, occurred, model.ProvenanceEstimated)); err != nil {
			return err
		}
	}
	return nil
}

// gatherBalance emits the prepaid credit-balance inventory finding and, when low_balance_usd
// is configured, a low-balance posture finding. 404 (postpaid team) is skipped honestly.
func (s *Source) gatherBalance(ctx context.Context, sink sdk.Sink, team string) error {
	var resp balanceResponse
	if err := s.mgmtClient.GetJSON(ctx, "/v1/billing/teams/"+url.PathEscape(team)+"/prepaid/balance", nil, &resp); err != nil {
		if isUnavailable(err) {
			return nil
		}
		return err
	}
	balanceUSD, ok := decimalStringToFloat(resp.Total.Val)
	if !ok {
		return nil // no parseable balance reported
	}
	now := s.clock().UTC()
	sev := model.SeverityInfo
	title := "xAI prepaid credit balance"
	kind := "inventory"
	if s.lowBalanceUSD > 0 && balanceUSD < s.lowBalanceUSD {
		sev = model.SeverityMedium
		title = "xAI prepaid credit balance below threshold (top-up to avoid request rejection)"
		kind = "posture"
	}
	return sink.Emit(ctx, model.FindingReport{
		Kind:        kind,
		Severity:    sev,
		SubjectKind: subjectBalance,
		SubjectRef:  team,
		Title:       title,
		DetailHash:  redact.Hash("xai team=" + team + " prepaid_balance_usd=" + resp.Total.Val + " threshold_usd=" + strconv.FormatFloat(s.lowBalanceUSD, 'f', -1, 64)),
		OccurredAt:  now,
	})
}

// gatherSpendingLimits emits the effective monthly spend ceiling as an inventory finding,
// or a posture finding when NO effective limit is set (an unexpected-charge risk — the
// FinOps backstop). 404 (prepaid team) is skipped honestly.
func (s *Source) gatherSpendingLimits(ctx context.Context, sink sdk.Sink, team string) error {
	var resp spendingLimitsResponse
	if err := s.mgmtClient.GetJSON(ctx, "/v1/billing/teams/"+url.PathEscape(team)+"/postpaid/spending-limits", nil, &resp); err != nil {
		if isUnavailable(err) {
			return nil
		}
		return err
	}
	soft, softOK := decimalStringToFloat(resp.SpendingLimits.EffectiveSl.Val)
	hard, hardOK := decimalStringToFloat(resp.SpendingLimits.EffectiveHardSl.Val)
	now := s.clock().UTC()
	if (!softOK || soft <= 0) && (!hardOK || hard <= 0) {
		return sink.Emit(ctx, model.FindingReport{
			Kind:        "posture",
			Severity:    model.SeverityLow,
			SubjectKind: subjectSpendCap,
			SubjectRef:  team,
			Title:       "xAI team has no effective monthly spending limit (unexpected-charge risk)",
			DetailHash:  redact.Hash("xai team=" + team + " effective_sl=" + resp.SpendingLimits.EffectiveSl.Val + " effective_hard_sl=" + resp.SpendingLimits.EffectiveHardSl.Val + "; set a monthly spending limit as a FinOps backstop"),
			OccurredAt:  now,
		})
	}
	return sink.Emit(ctx, model.FindingReport{
		Kind:        "inventory",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectSpendCap,
		SubjectRef:  team,
		Title:       "xAI team monthly spending limit configured",
		DetailHash:  redact.Hash("xai team=" + team + " effective_sl=" + resp.SpendingLimits.EffectiveSl.Val + " effective_hard_sl=" + resp.SpendingLimits.EffectiveHardSl.Val),
		OccurredAt:  now,
	})
}

// costSample builds a CostSample for a billed/estimated xAI cost (money only — no token
// counts: the billing endpoints report money, not usage). The invoice number / "preview"
// ties the sample to its source for traceability (SessionRef), the team is the WorkspaceRef.
func (s *Source) costSample(team, ref string, micro int64, occurred time.Time, prov model.CostProvenance) model.CostSample {
	u := modelprovider.Usage{
		ProviderRef:  modelprovider.ProviderXAI,
		SessionRef:   ref,
		WorkspaceRef: team,
		OccurredAt:   occurred,
		Gateway:      model.GatewayDirect,
		Provenance:   prov,
		CostType:     costTypeXAI,
	}
	return modelprovider.ToCostSampleWithCost(u, micro)
}

// decimalStringToFloat parses a decimal money string ("12.34") to a float, reporting
// whether it parsed. A negative or NaN value reports ok=false (treated as unreported).
func decimalStringToFloat(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 || f != f {
		return 0, false
	}
	return f, true
}

// decimalStringToMicroUSD parses a decimal USD money string to integer micro-USD, rounding
// to the nearest micro-USD. An unparseable / negative value yields 0 (never a guessed cost).
func decimalStringToMicroUSD(s string) int64 {
	f, ok := decimalStringToFloat(s)
	if !ok {
		return 0
	}
	return int64(math.Round(f * 1_000_000))
}
