// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudemanagedagents

import (
	"fmt"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// outcomeFinding maps a TERMINAL outcome-grader verdict to a governance finding. A
// satisfied outcome is normal (Info); max_iterations_reached is a budget exhaustion the
// operator may want to see (Low); failed means the rubric fundamentally did not match
// the task (Medium — a misconfigured outcome wastes work); interrupted is the
// operator's own action (Info). Non-terminal states (pending/running/evaluating — the
// live result enum, verified 2026-06-10) emit nothing: a verdict finding must record a
// decision, not a snapshot of progress. ok is false for a non-terminal evaluation.
//
// OccurredAt is the evaluation's own completed_at (falling back to the session
// timestamp), so re-emission across polls and webhook GET-backs de-dups downstream.
// The explanation is non-sensitive grader feedback but is hashed defensively, never
// surfaced (docs/SECURITY-HARDENING.md).
//
// Correction to the model: the live outcome_evaluations[] schema carries NO
// usage field, so the grader's separate context-window cost is NOT attributable from
// this resource — the former outcomeCostSample was built on a fabricated shape and was
// removed rather than kept emitting unverifiable samples.
func outcomeFinding(sessionRef string, ev OutcomeEvaluation, fallbackAt time.Time) (model.FindingReport, bool) {
	if !ev.terminal() {
		return model.FindingReport{}, false
	}
	at := parseTime(ev.CompletedAt)
	if at.IsZero() {
		at = fallbackAt
	}
	sev, label := outcomeVerdict(ev.Result)
	return model.FindingReport{
		Kind:        findingGovernance,
		Severity:    sev,
		SubjectKind: kindOutcome,
		SubjectRef:  labelRef(ev.OutcomeID, "outcome"),
		Title:       "CMA outcome evaluation " + label,
		DetailHash:  redact.Hash(fmt.Sprintf("outcome=%s session=%s result=%s iteration=%d explanation=%s", ev.OutcomeID, sessionRef, ev.Result, ev.Iteration, ev.Explanation)),
		OccurredAt:  at,
	}, true
}

// outcomeVerdict maps a terminal outcome result to a severity + display label. An
// unrecognized terminal result degrades to Info with the raw label (forward-tolerant:
// a new verdict state is recorded, never dropped).
func outcomeVerdict(result string) (model.Severity, string) {
	switch result {
	case "failed":
		return model.SeverityMedium, "failed (rubric does not match the task)"
	case "max_iterations_reached":
		return model.SeverityLow, "reached its iteration budget"
	case "interrupted":
		return model.SeverityInfo, "interrupted"
	case "satisfied":
		return model.SeverityInfo, "satisfied"
	default:
		return model.SeverityInfo, "completed (" + result + ")"
	}
}
