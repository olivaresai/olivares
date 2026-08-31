// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package splunkhec

import "github.com/olivaresai/olivares/sdk"

// ClassifyHECCode maps a Splunk HEC status code to a delivery outcome.
//
// It is EXPORTED because a second consumer needs the same answer: the eventing
// engine's own dispatch path, which ships the audit LEDGER to a SIEM, held a private
// copy of this table and the two had already diverged — the copy read code 17 (a
// health answer) and an empty body as deliveries. One table, one answer; that is the
// same reason sdk/siemwire exists for the wire formats.
//
// The codes are NOT uniformly failures, and treating every non-zero value as a
// rejection is wrong in both directions. Codes 24 and 25 are returned under HTTP
// 200 with the data INDEXED — they warn that a queue is approaching capacity —
// so reporting them as failures makes the engine retry a payload that already
// landed and duplicate it in the operator's index. And codes 8, 9, 18, 19, 20,
// 23, 26 and 27 are server-side conditions where retrying is exactly right, while
// codes 5, 6, 7, 10 to 16 and the token errors describe the request itself and can
// never succeed on a retry of the same bytes.
//
// Source: Splunk's HEC troubleshooting reference (code/text/HTTP status table).
// Verified 2026-07-28; see an internal design note (not shipped)
func ClassifyHECCode(code int) sdk.DeliveryOutcome {
	switch code {
	case 0:
		return sdk.OutcomeDelivered
	case 17:
		// "HEC is healthy" answers a HEALTH probe; it is not Splunk saying it indexed
		// this event. Grouping it with success let an endpoint pointed at the health
		// path report deliveries that never happened. Whether the event landed is
		// unknowable from here, so say that rather than assume.
		return sdk.OutcomeIndeterminate
	case 24, 25:
		// "queue is approaching its capacity limit" / "ACK is approaching its capacity
		// limit": HTTP 200, the event WAS accepted. A capacity warning is not a refusal.
		return sdk.OutcomeDeliveredWithWarning
	case 8, 9, 18, 19, 20, 23, 26, 27:
		// Internal server error, server busy, unhealthy queues/ack, shutting down, and
		// the two at-capacity 429s: transient, retry.
		return sdk.OutcomeUnavailable
	case 1, 2, 3, 4, 5, 6, 7, 10, 11, 12, 13, 14, 15, 16, 21, 22:
		// Token, channel, index and payload-shape errors. Re-sending identical bytes
		// against identical configuration cannot change the answer.
		return sdk.OutcomeRejected
	default:
		// An unknown code is not assumed to be either. Splunk can add codes, and
		// guessing "rejected" would discard evidence while guessing "delivered" would
		// invent an acceptance.
		return sdk.OutcomeIndeterminate
	}
}

// HECVerdict reads a Splunk HEC response body and reports the outcome it states,
// plus whether the body was recognizable as a HEC status document at all.
//
// It exists so that every consumer of a HEC answer draws the SAME conclusion. The
// eventing engine had its own copy and the two disagreed on the cases that matter:
// it accepted a body only when text AND code were both present (rejecting the
// documented submit-with-ack response, which carries an ackID), it read code 17 — a
// health answer — as a delivery, and it read an EMPTY body as one too. That lane
// ships the audit ledger, so each of those was a record saying "delivered" about an
// event nobody confirmed.
//
// ok=false means the body is not a HEC status document: empty, undecodable, or
// carrying none of the members HEC sends. A caller must treat that as indeterminate
// rather than as the 2xx standing.
func HECVerdict(body []byte) (outcome sdk.DeliveryOutcome, code int, ok bool) {
	// The parse error is DELIBERATELY ignored. parseHECResponse returns one whenever
	// the code is non-zero — that is its contract for the connector, which wants the
	// message — so treating it as "not a HEC document" would classify every refusal
	// as unrecognizable. wellFormed is the flag that answers "is this HEC speaking",
	// and it is the only one this function needs.
	resp, _ := parseHECResponse(string(body))
	if !resp.wellFormed {
		return sdk.OutcomeIndeterminate, 0, false
	}
	return ClassifyHECCode(resp.code()), resp.code(), true
}
