// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package elastic

import "github.com/olivaresai/olivares/sdk"

// itemRetryable reports whether a per-item _bulk status can succeed on a retry.
//
// This distinction is the reason the whole verdict is computed item by item
// instead of from the top-level errors flag. Elasticsearch's own guidance is that
// a rejection is not uniformly terminal: "You can retry HTTP 429 errors... Rejected
// requests do not always mean that all documents were unsuccessful. Make sure you
// inspect the full response and retry the appropriate documents." A 429 is a full
// bulk queue — a capacity condition that clears on its own — while a 400
// mapper_parsing_exception is a property of the document and will refuse forever.
//
// Collapsing both into "rejected, do not retry" would dead-letter evidence that
// Elasticsearch explicitly asks us to resend: a data-loss regression introduced by
// the very change meant to stop retrying the undeliverable.
func itemRetryable(status int) bool {
	switch status {
	case 429:
		// The documented one: a full bulk queue, which Elasticsearch's own guidance
		// says to retry with backoff.
		return true
	case 503:
		// Reachable per item through UnavailableShardsException.
		return true
	default:
		// 502 and 504 were listed here on the assumption that they behave like the
		// request-level gateway statuses. No official source shows Elasticsearch
		// producing them as a PER-ITEM status, so claiming they are retryable items
		// would be an invented contract. They remain retryable at the REQUEST level,
		// where they are real (classifyESStatus).
		return false
	}
}

// classifyBulk turns a decoded _bulk response into a delivery verdict.
//
// It reports the outcome, how many items were refused, the ordinal of the first
// refusal (Elasticsearch is the one destination here that can attribute a failure
// to an exact position) and that item's status.
func classifyBulk(items []bulkItem) (sdk.DeliveryOutcome, int, int, int, []int) {
	sent := len(items)
	rejected, firstRejected, firstStatus := 0, -1, 0
	anyRetryable := false
	var ordinals []int
	for i, it := range items {
		r := it.Create
		if r == nil {
			r = it.Index
		}
		if r == nil || r.Error == nil {
			continue
		}
		if r.Status == 409 {
			// version_conflict on a "create" whose _id is our stable delivery key means
			// THIS EXACT delivery already landed — the previous attempt succeeded and the
			// answer was lost, which is the case at-least-once delivery exists to cover.
			// Counting it as a refusal would dead-letter an event that IS in the index.
			continue
		}
		rejected++
		ordinals = append(ordinals, i)
		if firstRejected < 0 {
			firstRejected, firstStatus = i, r.Status
		}
		if itemRetryable(r.Status) {
			anyRetryable = true
		}
	}
	if rejected == 0 {
		return sdk.OutcomeDelivered, 0, -1, 0, nil
	}
	outcome := sdk.ClassifyCount(sent, rejected)
	// A retryable item only makes the WHOLE request retryable when nothing in it
	// landed. If part of the batch was accepted, re-sending it would duplicate that
	// part, so the request stays partial and the ordinals say what to resubmit —
	// which is exactly what Elasticsearch means by "retry the appropriate documents"
	// rather than "retry the request".
	if anyRetryable && outcome == sdk.OutcomeRejected {
		outcome = sdk.OutcomeUnavailable
	}
	return outcome, rejected, firstRejected, firstStatus, ordinals
}
