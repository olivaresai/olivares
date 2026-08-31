// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// meter.go implements COST METERING AROUND THE QUEUE API (G5). fal has no
// usage report, so cost is metered per request from the declared per-output price
// catalog and the queue's own billable signal (metrics.inference_time):
//
//   - Meter is the exported helper the queue-driving caller (a gateway/runtime) uses to
//     price a COMPLETED result with the exact billable units it counted (images,
//     megapixels, seconds). It returns a CostSample (provenance=estimated) so FinOps
//     (module XI) ingests fal spend on the canonical path.
//   - gatherMeteredRequests meters any operator-configured in-flight request ids from
//     their queue STATUS (it never reads the generated media), using inference_time for
//     compute-second-billed models. Per-output models need the output count, which the
//     status does not carry — those meter to 0 from status (the caller uses Meter with
//     the count for precision); this is documented, not a silent under-count.
package fal

import (
	"context"
	"time"

	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// statusCompleted is the fal queue terminal status the connector meters.
const statusCompleted = "COMPLETED"

// Meter prices one fal queue result into a CostSample. inferenceSeconds is the queue's
// reported metrics.inference_time; outputs is the number of billable outputs the caller
// counted from the result (images/clips/megapixels). The model's declared unit selects
// which drives cost: a "second"-billed model uses inferenceSeconds (falling back to the
// operator fallback_second_usd for an uncataloged model); any other unit uses outputs.
// An uncataloged model with no fallback yields cost 0 (honest — never a guessed price).
// The returned bool reports whether a price was found, so a caller can distinguish a
// priced sample from an unpriced (cost-0) record.
func (s *Source) Meter(modelID string, inferenceSeconds float64, outputs int64, at time.Time) (model.CostSample, bool) {
	if at.IsZero() {
		at = s.clock().UTC()
	}
	unit, perUnit, ok := falPricingFor(modelID)
	costUSD := 0.0
	priced := false
	switch {
	case ok && unit == unitSecond:
		costUSD = inferenceSeconds * perUnit
		priced = true
	case ok:
		costUSD = float64(outputs) * perUnit
		priced = true
	case s.fallbackPerSec > 0:
		// Uncataloged model: meter compute-seconds at the operator fallback rate.
		unit = unitSecond
		costUSD = inferenceSeconds * s.fallbackPerSec
		priced = true
	default:
		unit = unitSecond // record the request; cost stays 0 (unpriced)
	}
	cs := model.CostSample{
		ProviderRef:  modelprovider.ProviderFal,
		ModelRef:     modelID,
		CostMicroUSD: dollarsToMicroUSD(costUSD),
		OccurredAt:   at,
		Gateway:      model.GatewayDirect,
		Provenance:   model.ProvenanceEstimated,
		CostType:     unit,
	}
	return cs, priced
}

// gatherMeteredRequests polls each operator-configured in-flight request id's queue
// STATUS and emits a metered CostSample for every COMPLETED one. It reads the status
// endpoint only (never the result/media). A request still in queue/progress is skipped
// (it will settle on a later gather); a 404 (expired/unknown id) is skipped, not fatal.
func (s *Source) gatherMeteredRequests(ctx context.Context, sink sdk.Sink) error {
	for _, id := range s.requestIDs {
		if err := ctx.Err(); err != nil {
			return err
		}
		var st queueStatus
		path := "/" + s.model + "/requests/" + id + "/status"
		if err := s.queueClient.GetJSON(ctx, path, nil, &st); err != nil {
			if isUnavailable(err) {
				continue // expired/unknown request id; not fatal
			}
			return err
		}
		if st.Status != statusCompleted {
			continue
		}
		secs := 0.0
		if st.Metrics != nil {
			secs = st.Metrics.InferenceTime
		}
		cs, _ := s.Meter(s.model, secs, 0, s.clock().UTC())
		cs.SessionRef = id // tie the cost to the queue request for traceability
		if err := sink.Emit(ctx, cs); err != nil {
			return err
		}
	}
	return nil
}
