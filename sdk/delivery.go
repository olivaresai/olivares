// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package sdk

import (
	"errors"
	"fmt"
)

// DeliveryOutcome is what a destination actually did with a notification.
//
// It exists because HTTP status is not the answer. Every serious log and SIEM
// destination this SDK targets can answer 200 while refusing the payload: Splunk
// HEC returns a non-zero code in the body, Elasticsearch's _bulk returns
// errors:true with per-item failures, and OTLP returns partial_success with a
// count of rejected records. A connector that reports only "error / no error"
// forces the engine to guess, and the engine's only safe guess is "retry" — which
// is wrong for a deterministic refusal and, per the OTLP specification, actually
// forbidden.
//
// The outcomes are a FIXED, closed set on purpose. They are recorded in the
// delivery ledger and drive automation, so a destination must never be able to
// widen them or write its own text into our state; a remote string belongs in a
// log line, never in an outcome.
type DeliveryOutcome uint8

const (
	// OutcomeIndeterminate is the zero value, and it is deliberately the UNSAFE-TO-
	// ASSUME one for ACCEPTANCE (though it is retryable — see Retryable): it means
	// no trustworthy verdict was obtained — an answer that could
	// not be read whole, a body in a shape the connector does not recognize, a
	// protocol response that contradicts itself. It is not a synonym for failure and
	// must never be treated as success. Making it the zero value means a connector
	// that forgets to classify reports "I do not know" rather than "it worked".
	OutcomeIndeterminate DeliveryOutcome = iota
	// OutcomeDelivered means the destination accepted the payload in full.
	OutcomeDelivered
	// OutcomeDeliveredWithWarning means the destination ACCEPTED the payload and
	// additionally reported a condition worth surfacing — Splunk HEC's codes 24 and
	// 25 ("queue is approaching its capacity limit") are exactly this: HTTP 200, a
	// non-zero code, and the data indexed. Retrying one of these duplicates data that
	// already landed, which is why it cannot be folded into a rejection.
	OutcomeDeliveredWithWarning
	// OutcomePartial means some records were accepted and others refused in the same
	// request. Retrying the whole batch would duplicate the accepted part, so it is
	// never a plain retry; what can be done depends on the protocol's
	// RejectionLocator.
	OutcomePartial
	// OutcomeRejected means the destination refused the payload for a reason that
	// re-sending the same bytes cannot change: a malformed record, an unknown index,
	// a field the mapping forbids. Retrying burns the ladder and then dead-letters
	// anyway, having re-sent bytes the destination already refused.
	OutcomeRejected
	// OutcomeUnavailable means the destination could not take the payload right now:
	// busy, queue full, throttled, restarting, unreachable. Retrying is correct.
	OutcomeUnavailable
	// OutcomeProtocolAnomaly means the answer was readable but self-contradictory —
	// more records rejected than were sent, a negative count, a locator outside the
	// batch. It is kept apart from Indeterminate because the two demand different
	// operator responses: one is a transport or size problem, the other means the
	// destination (or something impersonating it) is not speaking the protocol.
	OutcomeProtocolAnomaly
)

// String renders the outcome for logs and ledger rows. The values are part of the
// operator-facing contract: they appear in delivery records and dashboards, so
// they are stable identifiers, not prose.
func (o DeliveryOutcome) String() string {
	switch o {
	case OutcomeDelivered:
		return "delivered"
	case OutcomeDeliveredWithWarning:
		return "delivered_with_warning"
	case OutcomePartial:
		return "partial"
	case OutcomeRejected:
		return "rejected"
	case OutcomeUnavailable:
		return "unavailable"
	case OutcomeProtocolAnomaly:
		return "protocol_anomaly"
	default:
		return "indeterminate"
	}
}

// Retryable reports whether re-sending the SAME payload can succeed.
//
// Unavailable is the obvious case. Indeterminate is retryable TOO, and that is a
// deliberate asymmetry with Accepted rather than an oversight:
//
//   - It is what an unmodified connector produces. A connector written before this
//     contract returns a plain error for an unreachable host, and refusing to retry
//     that would turn every transient network failure into an immediate
//     dead-letter — a far larger regression than the one this contract fixes.
//   - When the verdict could not be read, the two risks are not symmetric. Retrying
//     may duplicate a payload that did land; not retrying may lose one that did
//     not. For an evidence pipeline a duplicate is a reconciliation problem and a
//     loss is a hole, so the tie breaks toward retry.
//
// Partial is excluded because a retry re-sends the records that already landed,
// and ProtocolAnomaly because a destination that is not speaking the protocol
// needs an operator rather than a timer.
//
// Retryable and Accepted answer DIFFERENT questions and must not be conflated:
// Indeterminate is retryable and is never an acceptance.
func (o DeliveryOutcome) Retryable() bool {
	return o == OutcomeUnavailable || o == OutcomeIndeterminate
}

// Accepted reports whether the payload reached the destination's records. It is
// deliberately distinct from Retryable: a warning is an acceptance, and treating
// it as a failure is what duplicates data.
func (o DeliveryOutcome) Accepted() bool {
	return o == OutcomeDelivered || o == OutcomeDeliveredWithWarning
}

// RejectionLocator declares HOW PRECISELY a protocol can say which records it
// refused. It is a capability of the protocol, not of one response, and the
// asymmetry is real rather than an implementation gap: the three protocols this
// SDK targets genuinely differ, and flattening them to the weakest would discard
// information Elasticsearch does give us.
type RejectionLocator uint8

const (
	// LocatorNone: the protocol reports that something was refused but nothing about
	// which record. Nothing can be resubmitted selectively.
	LocatorNone RejectionLocator = iota
	// LocatorAggregateCount: the protocol reports HOW MANY records were refused but
	// not which ones. OTLP's partial_success is this shape.
	LocatorAggregateCount
	// LocatorOrdinal: the protocol reports the position of each refused record, so a
	// caller can resubmit exactly those. Elasticsearch's _bulk items are this shape.
	LocatorOrdinal
	// LocatorPrefixBoundary: the protocol reports one boundary and guarantees that
	// everything before it was accepted and everything from it was dropped. Splunk
	// HEC's invalid-event-number is this shape.
	LocatorPrefixBoundary
)

// DeliveryReport is the verdict a connector draws from one destination response.
//
// Sent is the number of records the request carried, and it is what makes the
// rejected count meaningful: "1 record rejected" is a total refusal in a batch of
// one and a partial one in a batch of a hundred. Classifying on the rejected
// count alone only appears to work while every batch holds a single record.
type DeliveryReport struct {
	// Outcome is the verdict. The zero value is OutcomeIndeterminate.
	Outcome DeliveryOutcome
	// Sent is how many records the request carried (>= 1 when known, 0 when the
	// connector does not batch and does not count).
	Sent int
	// Rejected is how many records the destination refused, or -1 when the protocol
	// reports a refusal without a count.
	Rejected int
	// Locator says how precisely Rejected can be attributed to records.
	Locator RejectionLocator
	// FirstRejected is the position of the first refused record when Locator is
	// Ordinal or PrefixBoundary, otherwise -1.
	FirstRejected int
	// RejectedOrdinals lists EVERY refused position when Locator is Ordinal.
	//
	// It exists because the locator would otherwise promise more than the report
	// delivers: declaring ordinal precision while carrying only the first position
	// makes a selective resubmit impossible for every rejection after the first, and
	// an operator reading "ordinal" would reasonably assume otherwise. It is nil for
	// the other locators, which genuinely cannot attribute per record.
	RejectedOrdinals []int
	// Code is the destination's own numeric status when the protocol has one
	// (Splunk HEC's code, an Elasticsearch item status). It is a NUMBER on purpose:
	// it is safe to record, unlike the remote text that accompanies it.
	Code int
}

// ClassifyCount applies the cardinality rule to a rejected count.
//
// The rule is relative to what was sent, which is the part that is easy to get
// wrong: a fixed "one rejection is terminal, more than one is an anomaly" test
// happens to behave while every request carries exactly one record, and silently
// misclassifies the moment batching arrives.
//
// A negative count, or one larger than the batch, is an anomaly rather than a
// clamped value: a destination that claims to have rejected more records than it
// received is not speaking the protocol, and quietly rounding that off would hide
// the only evidence of it.
func ClassifyCount(sent, rejected int) DeliveryOutcome {
	switch {
	case rejected < 0 || (sent > 0 && rejected > sent):
		return OutcomeProtocolAnomaly
	case rejected == 0:
		return OutcomeDelivered
	case sent > 0 && rejected == sent:
		return OutcomeRejected
	case sent > 0:
		return OutcomePartial
	default:
		// Nothing to compare against: a refusal was reported for a request whose
		// cardinality the connector does not know. Refusing to guess between "all of
		// it" and "some of it" is the honest answer.
		return OutcomeIndeterminate
	}
}

// DeliveryError carries a DeliveryReport through OutputConnector.Notify, whose
// signature returns only error.
//
// It is an error rather than a second return value so that the contract can be
// adopted without breaking a connector written against the existing SDK: an
// author who returns a plain error keeps working and is treated as
// OutcomeIndeterminate — the safe reading — while one who wraps a report gets the
// engine's precise handling. The engine unwraps it with errors.As.
type DeliveryError struct {
	// Report is the verdict.
	Report DeliveryReport
	// Err is the underlying cause, for logs. It never reaches the delivery ledger.
	Err error
}

func (e *DeliveryError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("delivery %s: %v", e.Report.Outcome, e.Err)
	}
	return fmt.Sprintf("delivery %s", e.Report.Outcome)
}

func (e *DeliveryError) Unwrap() error { return e.Err }

// NewDeliveryError builds a DeliveryError for an outcome that is not a plain
// success, filling the locator defaults so callers cannot leave FirstRejected as
// a meaningless zero.
func NewDeliveryError(report DeliveryReport, err error) *DeliveryError {
	if report.Locator == LocatorNone || report.Locator == LocatorAggregateCount {
		report.FirstRejected = -1
	}
	return &DeliveryError{Report: report, Err: err}
}

// ReportFor extracts the verdict from an error returned by Notify. An error that
// carries no report is OutcomeIndeterminate: the connector told us the delivery
// did not succeed but not what the destination did, so nothing may be assumed
// about whether the payload landed.
//
// A nil error is NOT interpreted here. Absence of an error is the connector's
// statement that the delivery succeeded, and callers already have that.
func ReportFor(err error) DeliveryReport {
	var de *DeliveryError
	if errors.As(err, &de) {
		return de.Report
	}
	return DeliveryReport{Outcome: OutcomeIndeterminate, Rejected: -1, FirstRejected: -1}
}
