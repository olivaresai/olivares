// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// This file reads BILLED Vertex AI cost from an operator-wired billing-export result.
// Google has NO real-time cost API: cloudbilling.googleapis.com exposes only rate cards
// (the Catalog/Pricing API) and budget thresholds, never incurred spend — actual Vertex
// cost lives ONLY in the BigQuery billing export. So, mirroring the gemini connector's
// usage_url, the operator materializes their billing-export query into a small JSON shape
// and points cost_export_url at it; the connector GETs it read-only and emits one BILLED
// CostSample per row. With no cost_export_url the billed stream is ABSENT (never
// fabricated) and the derived-cost usage stream (usage.go) stands alone.
//
// It NEVER fabricates a cost: a zero-cost row is skipped (absence ≠ zero), a negative row
// is a real billed credit/refund and is KEPT so net spend reconciles. Money never goes
// through float (ARCHITECTURE.md): the row's cost is a DECIMAL STRING parsed digit-wise to
// integer micro-USD.
package vertex

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

// costExportResponse is the operator-wired billing-export shape. It is intentionally
// minimal — the operator projects their BigQuery billing-export query (service.description,
// sku.description, cost, currency, usage_start_time, project.id) into these rows. cost is a
// DECIMAL STRING so money never round-trips through a float.
type costExportResponse struct {
	Rows []costExportRow `json:"rows"`
}

// costExportRow is one billing-export line. Model carries the model ref when the operator
// projects it; otherwise SKUDescription (the SKU embeds the model, like the AWS
// line_item_usage_type) is used — a DIFFERENT grain from the token-usage stream.
type costExportRow struct {
	Model              string `json:"model"`
	SKUDescription     string `json:"sku_description"`
	ServiceDescription string `json:"service_description"`
	Cost               string `json:"cost"`     // decimal string, e.g. "12.4831" — never a float
	Currency           string `json:"currency"` // "USD"
	UsageStartTime     string `json:"usage_start_time"`
	ProjectID          string `json:"project_id"`
	Location           string `json:"location"`
}

// gatherCost reads the operator billing-export result and emits one billed CostSample per
// non-zero row. It is a batch read: one GET, then drain.
func (s *Source) gatherCost(ctx context.Context, sink sdk.Sink, at time.Time) error {
	var resp costExportResponse
	if err := s.getURL(ctx, s.cfg.costExportURL, &resp); err != nil {
		return err
	}
	for _, r := range resp.Rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		cs, ok := costSampleFromRow(r, at)
		if !ok {
			continue
		}
		if err := emit(ctx, sink, cs); err != nil {
			return err
		}
	}
	return nil
}

// costSampleFromRow turns one export row into a billed CostSample, or ok=false when the
// row has no model attribution or an exactly-zero/unparsable cost (a negative credit row
// IS kept so net spend reconciles).
func costSampleFromRow(r costExportRow, fallback time.Time) (model.CostSample, bool) {
	ref := strings.TrimSpace(r.Model)
	if ref == "" {
		ref = strings.TrimSpace(r.SKUDescription)
	}
	if ref == "" {
		return model.CostSample{}, false // no attribution: never fabricate a model ref
	}
	micros, err := parseDecimalUSDToMicros(r.Cost)
	if err != nil || micros == 0 {
		return model.CostSample{}, false
	}
	occurred := fallback
	if t, ok := parseRFC3339(r.UsageStartTime); ok {
		occurred = t
	}
	cs := model.CostSample{
		ProviderRef:  providerRef,
		ModelRef:     ref,
		CostMicroUSD: micros,
		OccurredAt:   occurred,
		Gateway:      model.GatewayVertex,
		Provenance:   model.ProvenanceBilled,
		InferenceGeo: strings.TrimSpace(r.Location),
	}
	if p := strings.TrimSpace(r.ProjectID); p != "" {
		cs.WorkspaceRef = p
	}
	return cs, true
}

// parseDecimalUSDToMicros converts a decimal-string USD amount (e.g. "12.4831200000") to
// integer micro-USD, rounding half-up at the 6th fractional digit. Money never goes
// through float (ARCHITECTURE.md): the string is parsed digit-wise. A leading '-' is a real
// billed credit/refund and is preserved.
func parseDecimalUSDToMicros(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("vertex: empty cost amount")
	}
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	intStr, fracStr, _ := strings.Cut(s, ".")
	if intStr == "" {
		intStr = "0"
	}
	ip, err := strconv.ParseInt(intStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("vertex: bad cost amount %q: %w", s, err)
	}
	if ip > math.MaxInt64/1_000_000 {
		return 0, fmt.Errorf("vertex: cost amount out of range: %q", s)
	}
	for _, r := range fracStr {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("vertex: non-digit in cost fraction %q", fracStr)
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
			return 0, fmt.Errorf("vertex: bad cost fraction %q: %w", fracStr, err)
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
