// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// This file reads BILLED Azure OpenAI / Cognitive Services cost from the Azure Cost
// Management Query API (a POST action that is a READ — the body is a query, no resource is
// mutated). It is MONEY ONLY, never tokens — a different, honest lens from the token-usage
// stream (usage.go): the usage stream has derived cost, the cost stream has no tokens.
//
// Honesty rules, designed in (from the verified Azure semantics):
//   - Map cost rows by COLUMN NAME, never by index — and the cost column may come back as
//     "PreTaxCost" even when "Cost" was requested. UsageDate is an INTEGER yyyymmdd.
//   - There is NO isFinal flag: Azure rerates open/recent periods (up to ~5 days after
//     period end). So a day within cost_finalization_lag of now is Provenance=estimated;
//     an older day is billed. The connector never fabricates a cost: no row ⇒ nothing
//     emitted; a 204 (no data yet, normal lag) is a clean no-op, not an error; a negative
//     cost is a real billed credit and is KEPT so net spend reconciles; only zero is
//     skipped. Money never goes through float (ARCHITECTURE.md): the cost cell is parsed as a
//     decimal string to integer micro-USD.
//   - Azure OpenAI has no dedicated ServiceName — it rolls up under "Cognitive Services";
//     the filter dimension + value are CONFIGURABLE (they differ by account type) so a
//     value mismatch never silently reports zero.
package azureopenai

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// costMaxPages bounds Cost Management nextLink pagination defensively.
const costMaxPages = 50

// costCostNames are the cost-measure column names the response may carry; the cost column
// is the first Number column whose name is one of these (the response column name is not
// guaranteed to equal the requested aggregation alias).
var costCostNames = map[string]struct{}{
	"cost": {}, "costusd": {}, "pretaxcost": {}, "pretaxcostusd": {},
}

// --- Cost Management Query request shapes -------------------------------------------

type costQueryRequest struct {
	Type       string         `json:"type"`
	Timeframe  string         `json:"timeframe"`
	TimePeriod costTimePeriod `json:"timePeriod"`
	Dataset    costDataset    `json:"dataset"`
}

type costTimePeriod struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type costDataset struct {
	Granularity string                `json:"granularity"`
	Aggregation map[string]costAggOp  `json:"aggregation"`
	Grouping    []costGrouping        `json:"grouping"`
	Filter      *costFilterExpression `json:"filter,omitempty"`
}

type costAggOp struct {
	Name     string `json:"name"`
	Function string `json:"function"`
}

type costGrouping struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

type costFilterExpression struct {
	Dimensions *costDimensionFilter `json:"dimensions,omitempty"`
}

type costDimensionFilter struct {
	Name     string   `json:"name"`
	Operator string   `json:"operator"`
	Values   []string `json:"values"`
}

// gatherCost reads billed cost per subscription over the cost_lookback window, day-bucketed
// and grouped by (ResourceId, Meter), filtered to the configured service dimension, and
// emits one CostSample per non-zero (resource, meter, day).
func (s *Source) gatherCost(ctx context.Context, sink sdk.Sink, subs []string, at time.Time) error {
	for _, sub := range subs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.gatherSubscriptionCost(ctx, sink, sub, at); err != nil {
			return err
		}
	}
	return nil
}

// gatherSubscriptionCost runs the Cost Management Query for one subscription, following
// nextLink (POST with the same body) up to the page bound.
func (s *Source) gatherSubscriptionCost(ctx context.Context, sink sdk.Sink, sub string, at time.Time) error {
	day := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.UTC)
	start := day.Add(-s.cfg.costLookback)
	body := costQueryRequest{
		Type:      "ActualCost",
		Timeframe: "Custom",
		TimePeriod: costTimePeriod{
			From: start.Format(time.RFC3339),
			To:   day.AddDate(0, 0, 1).Add(-time.Second).Format(time.RFC3339),
		},
		Dataset: costDataset{
			Granularity: "Daily",
			Aggregation: map[string]costAggOp{"totalCost": {Name: "Cost", Function: "Sum"}},
			Grouping:    []costGrouping{{Type: "Dimension", Name: "ResourceId"}, {Type: "Dimension", Name: "Meter"}},
			Filter: &costFilterExpression{Dimensions: &costDimensionFilter{
				Name: s.cfg.costServiceDimension, Operator: "In", Values: []string{s.cfg.costServiceValue},
			}},
		},
	}

	q := url.Values{"api-version": {s.cfg.costAPIVersion}}
	full := s.armURL("/subscriptions/"+url.PathEscape(sub)+"/providers/Microsoft.CostManagement/query", q)
	for page := 0; page < costMaxPages; page++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		var resp costQueryResponse
		status, err := s.postURL(ctx, full, body, &resp)
		if err != nil {
			return err
		}
		if status == 204 {
			return nil // no cost data for the window yet (normal lag) — not an error
		}
		if err := s.emitCostRows(ctx, sink, resp, at); err != nil {
			return err
		}
		if resp.Properties.NextLink == "" {
			return nil
		}
		full = resp.Properties.NextLink
	}
	return nil
}

// emitCostRows turns one Cost Management page into CostSamples: one per (resource, meter,
// day) with a non-zero cost. Columns are mapped by NAME; the day's Provenance is estimated
// while it is within the finalization window, else billed.
func (s *Source) emitCostRows(ctx context.Context, sink sdk.Sink, resp costQueryResponse, at time.Time) error {
	idx := indexColumns(resp.Properties.Columns)
	if idx.cost < 0 {
		return nil // no cost column in the response: nothing to attribute
	}
	for _, row := range resp.Properties.Rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		micros, err := parseDecimalUSDToMicros(rawNumber(row, idx.cost))
		if err != nil || micros == 0 {
			continue // unparsable or exactly-zero (a negative credit IS kept, below)
		}
		occurred := costDay(rawString(row, idx.date), at)
		prov := model.ProvenanceBilled
		if at.Sub(occurred) < s.cfg.costFinalizationLag {
			prov = model.ProvenanceEstimated // open/recent period — Azure rerates until invoiced
		}
		cs := model.CostSample{
			ProviderRef:  providerRef,
			ModelRef:     rawString(row, idx.meter), // the meter embeds the model (a different grain)
			CostMicroUSD: micros,
			OccurredAt:   occurred,
			Gateway:      model.GatewayFoundry,
			Provenance:   prov,
			WorkspaceRef: rawString(row, idx.resource),
		}
		if err := emit(ctx, sink, cs); err != nil {
			return err
		}
	}
	return nil
}

// columnIndex holds the positional indices of the columns the connector reads, resolved by
// NAME (the column order/set is not contractual).
type columnIndex struct {
	cost, date, resource, meter int
}

// indexColumns resolves the cost/date/resource/meter column indices by name. cost is the
// first Number column whose name is a known cost-measure name.
func indexColumns(cols []costColumn) columnIndex {
	idx := columnIndex{cost: -1, date: -1, resource: -1, meter: -1}
	for i, c := range cols {
		name := strings.ToLower(strings.TrimSpace(c.Name))
		if _, isCost := costCostNames[name]; idx.cost < 0 && isCost && strings.EqualFold(c.Type, "Number") {
			idx.cost = i
		}
		switch name {
		case "usagedate":
			idx.date = i
		case "resourceid":
			idx.resource = i
		case "meter":
			idx.meter = i
		}
	}
	return idx
}

// rawNumber returns the literal JSON token at row[i] as a string (a number literal like
// "12.34" or "0"), or "" when the index is invalid/absent.
func rawNumber(row []json.RawMessage, i int) string {
	if i < 0 || i >= len(row) {
		return ""
	}
	return strings.TrimSpace(string(row[i]))
}

// rawString returns row[i] decoded as a JSON string (a String column), or "" when the index
// is invalid or the cell is not a string.
func rawString(row []json.RawMessage, i int) string {
	if i < 0 || i >= len(row) {
		return ""
	}
	var s string
	if err := json.Unmarshal(row[i], &s); err != nil {
		return strings.Trim(strings.TrimSpace(string(row[i])), `"`)
	}
	return s
}

// costDay turns a Cost Management UsageDate (an integer yyyymmdd, e.g. 20260601) into a UTC
// day, falling back to the pass time when it is absent/unparsable.
func costDay(usageDate string, fallback time.Time) time.Time {
	usageDate = strings.TrimSpace(usageDate)
	if usageDate == "" {
		return fallback
	}
	n, err := strconv.Atoi(usageDate)
	if err != nil || n < 19700101 {
		return fallback
	}
	y, m, d := n/10000, (n/100)%100, n%100
	if m < 1 || m > 12 || d < 1 || d > 31 {
		return fallback
	}
	return time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
}

// parseDecimalUSDToMicros converts a decimal-string USD amount (e.g. "12.4831") to integer
// micro-USD, rounding half-up at the 6th fractional digit. Money never goes through float
// (ARCHITECTURE.md): the string is parsed digit-wise. A leading '-' is a real billed credit and
// is preserved.
func parseDecimalUSDToMicros(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("azure-openai: empty cost amount")
	}
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	intStr, fracStr, _ := strings.Cut(s, ".")
	if intStr == "" {
		intStr = "0"
	}
	ip, err := strconv.ParseInt(intStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("azure-openai: bad cost amount %q: %w", s, err)
	}
	if ip > math.MaxInt64/1_000_000 {
		return 0, fmt.Errorf("azure-openai: cost amount out of range: %q", s)
	}
	for _, r := range fracStr {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("azure-openai: non-digit in cost fraction %q", fracStr)
		}
	}
	micros := ip * 1_000_000
	if fracStr != "" {
		six := fracStr
		if len(six) >= 6 {
			six = fracStr[:6]
		} else {
			six += strings.Repeat("0", 6-len(six))
		}
		f, err := strconv.ParseInt(six, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("azure-openai: bad cost fraction %q: %w", fracStr, err)
		}
		micros += f
		if len(fracStr) > 6 && fracStr[6] >= '5' {
			micros++
		}
	}
	if neg {
		micros = -micros
	}
	return micros, nil
}
