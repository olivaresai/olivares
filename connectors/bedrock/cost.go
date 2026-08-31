// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// This file reads Amazon Bedrock cost from AWS Cost Explorer (GetCostAndUsage). A
// finalized period is the authoritative, reconcilable figure → Provenance=billed; a
// not-yet-finalized period (AWS marks it Estimated=true — its amount still mutates until
// the bill closes) is preliminary → Provenance=estimated, never billed (ARCHITECTURE.md). It
// NEVER fabricates a cost: no row for the window ⇒ nothing emitted (absence ≠ zero), and
// it does NOT derive cost from list pricing (decision: billed-only — the only
// "estimated" here is AWS's own preliminary figure, not a list-price derivation). A
// negative UnblendedCost is a real billed line (a credit/refund) and is kept so net
// spend reconciles; only an exactly-zero line is skipped.
//
// Cost Explorer has no per-model dimension and no per-request grain — the model is
// embedded in the line_item_usage_type string (e.g. "USE1-Claude4.6Sonnet-input-tokens"),
// so this stream groups by USAGE_TYPE and carries that usage-type as the sample's
// ModelRef. It is therefore a DIFFERENT grain from the token-usage stream (usage.go,
// ModelRef = bare model id): the two are honest, separate lenses that do not double-count
// (the usage stream has cost=0, the cost stream has tokens=0).
package bedrock

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// ceTarget is the X-Amz-Target for Cost Explorer GetCostAndUsage. The service prefix is
// the legacy internal name "AWSInsightsIndexService" (NOT "AWSCostExplorerService").
const ceTarget = "AWSInsightsIndexService.GetCostAndUsage"

// ceMetricUnblended is the cost metric we read: UnblendedCost is what the account pays
// at the public/contracted rate — the right figure for billed spend (Amortized spreads
// RI/SP fees, Blended uses org-averaged rates).
const ceMetricUnblended = "UnblendedCost"

// ceMaxPages bounds GetCostAndUsage pagination defensively.
const ceMaxPages = 50

// dateLayout is the Cost Explorer TimePeriod date format (YYYY-MM-DD).
const dateLayout = "2006-01-02"

// --- Cost Explorer wire shapes (AWS JSON 1.1; PascalCase; only the fields we use) ---

type ceRequest struct {
	TimePeriod    ceDateInterval `json:"TimePeriod"`
	Granularity   string         `json:"Granularity"`
	Metrics       []string       `json:"Metrics"`
	Filter        *ceExpression  `json:"Filter,omitempty"`
	GroupBy       []ceGroupDef   `json:"GroupBy,omitempty"`
	NextPageToken string         `json:"NextPageToken,omitempty"`
}

type ceDateInterval struct {
	Start string `json:"Start"`
	End   string `json:"End"`
}

type ceExpression struct {
	Dimensions *ceDimensionValues `json:"Dimensions,omitempty"`
}

type ceDimensionValues struct {
	Key    string   `json:"Key"`
	Values []string `json:"Values"`
}

type ceGroupDef struct {
	Type string `json:"Type"`
	Key  string `json:"Key"`
}

type ceResponse struct {
	ResultsByTime []ceResultByTime `json:"ResultsByTime"`
	NextPageToken string           `json:"NextPageToken"`
}

type ceResultByTime struct {
	TimePeriod ceDateInterval `json:"TimePeriod"`
	Estimated  bool           `json:"Estimated"`
	Groups     []ceGroup      `json:"Groups"`
}

type ceGroup struct {
	Keys    []string                 `json:"Keys"`
	Metrics map[string]ceMetricValue `json:"Metrics"`
}

type ceMetricValue struct {
	Amount string `json:"Amount"` // a DECIMAL STRING, e.g. "12.4831200000" — never a float
	Unit   string `json:"Unit"`   // "USD"
}

// gatherCost reads billed Bedrock cost over the cost_lookback window, day-bucketed and
// grouped by USAGE_TYPE, and emits one billed CostSample per (day, usage-type) with a
// non-zero cost. It paginates by NextPageToken (resending identical params, as the API
// requires) up to ceMaxPages. Cost Explorer is GLOBAL: it is always signed for
// us-east-1 regardless of the operating region.
func (s *Source) gatherCost(ctx context.Context, sink sdk.Sink, at time.Time) error {
	day := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.UTC)
	start := day.Add(-s.cfg.costLookback)
	start = time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
	end := day.AddDate(0, 0, 1) // End is EXCLUSIVE; +1 day includes today's (preliminary) spend

	base := ceRequest{
		TimePeriod:  ceDateInterval{Start: start.Format(dateLayout), End: end.Format(dateLayout)},
		Granularity: "DAILY",
		Metrics:     []string{ceMetricUnblended},
		Filter: &ceExpression{
			Dimensions: &ceDimensionValues{Key: "SERVICE", Values: []string{s.cfg.costService}},
		},
		GroupBy: []ceGroupDef{{Type: "DIMENSION", Key: "USAGE_TYPE"}},
	}

	token := ""
	for page := 0; page < ceMaxPages; page++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		req := base
		req.NextPageToken = token

		var resp ceResponse
		if err := s.awsJSONPost(ctx, s.cfg.costEndpoint, ceTarget, costSigningService, costSigningRegion, req, &resp); err != nil {
			return err
		}
		if err := s.emitCostResults(ctx, sink, resp, at); err != nil {
			return err
		}
		if resp.NextPageToken == "" {
			return nil
		}
		token = resp.NextPageToken
	}

	// Stopped at the page bound with a cursor still pending — say so (no silent caps).
	return emit(ctx, sink, postureFinding(model.SeverityLow, subjectCost, s.cfg.accountScope(),
		"Bedrock cost is PARTIAL — Cost Explorer pagination bound reached",
		fmt.Sprintf("bedrock.cost account=%s coverage=partial pages=%d cursor_pending=true", s.cfg.accountScope(), ceMaxPages), at))
}

// emitCostResults turns one GetCostAndUsage page into cost CostSamples: one per
// (day, usage-type) group with a non-zero UnblendedCost. A negative amount is a real
// billed line (a credit/refund — Cost Explorer reports those as negative UnblendedCost),
// so it is KEPT so net spend reconciles; only an exactly-zero row (no spend) is skipped.
// Provenance is per PERIOD: AWS marks a not-yet-finalized period Estimated=true and that
// figure mutates until the bill closes, so it is NOT the authoritative billed amount —
// it is emitted as estimated, never billed (ARCHITECTURE.md); a finalized period is billed.
func (s *Source) emitCostResults(ctx context.Context, sink sdk.Sink, resp ceResponse, at time.Time) error {
	for _, rbt := range resp.ResultsByTime {
		bucket := at
		if t, err := time.Parse(dateLayout, rbt.TimePeriod.Start); err == nil {
			bucket = t.UTC()
		}
		prov := model.ProvenanceBilled
		if rbt.Estimated {
			prov = model.ProvenanceEstimated // preliminary period — not yet reconcilable
		}
		for _, g := range rbt.Groups {
			mv, ok := g.Metrics[ceMetricUnblended]
			if !ok {
				continue
			}
			micros, err := parseUSDToMicros(mv.Amount)
			if err != nil {
				continue // unparsable ⇒ nothing to report (never fabricate)
			}
			if micros == 0 {
				continue // no spend on this line (a negative credit row IS kept, below)
			}
			usageType := ""
			if len(g.Keys) > 0 {
				usageType = g.Keys[0]
			}
			cs := model.CostSample{
				ProviderRef:  ProviderBedrock,
				ModelRef:     usageType, // AWS line_item_usage_type (model embedded); see file header
				CostMicroUSD: micros,
				OccurredAt:   bucket,
				Provenance:   prov,
			}
			if err := emit(ctx, sink, cs); err != nil {
				return err
			}
		}
	}
	return nil
}

// parseUSDToMicros converts a Cost Explorer decimal-string USD amount (e.g.
// "12.4831200000") to integer micro-USD, rounding half-up at the 6th fractional digit.
// Money never goes through float (ARCHITECTURE.md): the string is parsed digit-wise so a
// billed dollar is exact to the micro-USD the CostSample stores.
func parseUSDToMicros(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("bedrock: empty cost amount")
	}
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	intStr, fracStr, _ := strings.Cut(s, ".")
	if intStr == "" {
		intStr = "0"
	}
	ip, err := strconv.ParseInt(intStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("bedrock: bad cost amount %q: %w", s, err)
	}
	if ip > math.MaxInt64/1_000_000 {
		return 0, fmt.Errorf("bedrock: cost amount out of range: %q", s)
	}
	for _, r := range fracStr {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("bedrock: non-digit in cost fraction %q", fracStr)
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
			return 0, fmt.Errorf("bedrock: bad cost fraction %q: %w", fracStr, err)
		}
		micros += f
		if len(fracStr) > 6 && fracStr[6] >= '5' { // round half-up on the 7th digit
			micros++
		}
	}
	if neg {
		micros = -micros
	}
	return micros, nil
}
