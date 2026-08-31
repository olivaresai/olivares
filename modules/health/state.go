// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package health

import (
	"context"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// Finding kinds this module emits on the bus (one per state transition + the SLA
// breach). They are namespaced "health_*" so a consumer (XV) can tell them from a
// security_*/finops_*/orchestration_* finding (the convention, S02).
const (
	busDegraded  = "health_subject_degraded"
	busDown      = "health_subject_down"
	busRecovered = "health_subject_recovered"
	busSLABreach = "health_sla_breach"
)

// transition is the outcome of applyStateTx: what changed inside the transaction,
// so the caller can emit the bus finding and publish the SSE snapshot AFTER the
// commit (a publish/emit must never run inside the store transaction).
type transition struct {
	happened    bool // a real state change occurred (vs a same-state refresh)
	subjectKind string
	subjectRef  string
	prev        string
	next        string
	severity    sdkmodel.Severity
	kind        string // bus finding kind
	title       string // short, non-sensitive
	detail      string // redaction-safe detail (hashed before it leaves the module)
	snapshot    statusDTO
}

// sevForState grades a state on the shared severity scale: down is high, degraded
// is medium, a recovery is informational. Used for both the emitted finding and
// the persisted incident severity.
func sevForState(state string) sdkmodel.Severity {
	switch state {
	case stateDown:
		return sdkmodel.SeverityHigh
	case stateDegraded:
		return sdkmodel.SeverityMedium
	default: // healthy/unknown
		return sdkmodel.SeverityInfo
	}
}

// busKindForState maps a new state to the bus finding kind for its transition.
func busKindForState(state string) string {
	switch state {
	case stateDown:
		return busDown
	case stateDegraded:
		return busDegraded
	default: // healthy → recovered
		return busRecovered
	}
}

// applyStateTx folds a new observed/derived state for a subject into its check
// row IN PLACE (checkRec is a reference map), INSIDE the caller's transaction. It
// always refreshes the current snapshot (last_state/last_checked/last_latency/
// last_seen); on a real state CHANGE it also appends an immutable health_event,
// opens/escalates or resolves the incident, and mirrors the state into the core
// HealthStatus entity. latencyMS < 0 means "unknown" (the column is left
// unchanged). It does NOT persist the check — the caller does a single repo.Update
// after (so the sweep can also fold an SLA-flag change into the same write). It
// returns the transition so the caller can emit the finding and publish the SSE
// snapshot after commit.
func (m *Module) applyStateTx(ctx context.Context, sc store.Scope, checkRec model.Record, newState, cause string, latencyMS int64, detail string, at time.Time) (transition, error) {
	subjectKind := checkRec.String(colSubjectKind)
	subjectRef := checkRec.String(colSubjectRef)
	checkID := model.ID(checkRec.String(model.ColID))

	prev := checkRec.String(colLastState)
	if prev == "" {
		prev = stateUnknown
	}

	// Always refresh the current snapshot on the check.
	checkRec[colLastState] = newState
	advanceLast(checkRec, colLastChecked, at)
	if latencyMS >= 0 {
		checkRec[colLastLatency] = latencyMS
	}
	// Store only the one-way fingerprint of the (caller-supplied) detail, never the
	// raw text — it may carry a secret/PII (docs/SECURITY-HARDENING.md), exactly as the event/incident
	// ledgers and the bus FindingReport do. Written unconditionally so a recovery with
	// no detail CLEARS it (hashHex("") == ""), never leaving a stale failure hash on a
	// now-healthy subject.
	checkRec[colLastDetailHash] = hashHex(detail)
	if newState == stateHealthy {
		advanceLast(checkRec, colLastSeenAt, at)
	}

	out := transition{
		happened:    prev != newState,
		subjectKind: subjectKind,
		subjectRef:  subjectRef,
		prev:        prev,
		next:        newState,
	}

	if out.happened {
		// Immutable reliability event (the SLA/uptime substrate, docs/SECURITY-HARDENING.md).
		evRepo, err := sc.Ext(eventKind)
		if err != nil {
			return transition{}, err
		}
		evRec := model.Record{
			colEvSubjectKind: subjectKind,
			colEvSubjectRef:  subjectRef,
			colEvState:       newState,
			colEvPrevState:   prev,
			colEvLatency:     latencyMS,
			colEvCause:       cause,
			colEvOccurredAt:  model.NewTimestamp(at).String(),
		}
		if !checkID.IsZero() {
			evRec[colEvCheckRef] = checkID.String()
		}
		if dh := hashHex(detail); dh != "" {
			evRec[colEvDetailHash] = dh
		}
		if _, err := evRepo.Create(ctx, evRec); err != nil {
			return transition{}, err
		}

		if err := m.manageIncidentTx(ctx, sc, checkID, subjectKind, subjectRef, newState, detail, at); err != nil {
			return transition{}, err
		}

		// Mirror into the core HealthStatus entity when the subject ref is a core
		// entity id (best-effort; a natural ref has no core row to mirror to).
		if err := m.mirrorHealthStatusTx(ctx, sc, subjectKind, subjectRef, newState, latencyMS, detail, at); err != nil {
			return transition{}, err
		}

		out.severity = sevForState(newState)
		out.kind = busKindForState(newState)
		out.title = stateTitle(subjectKind, subjectRef, newState, cause)
		out.detail = stateDetail(cause, detail)
	}

	out.snapshot = toStatusDTO(checkRec)
	return out, nil
}

// manageIncidentTx opens, escalates or resolves the subject's incident on a state
// change. There is at most one OPEN incident per subject (enforced here, since a
// UNIQUE index cannot express "many resolved + one open"). A move to down
// escalates an open degraded incident in place rather than opening a second one.
func (m *Module) manageIncidentTx(ctx context.Context, sc store.Scope, checkID model.ID, subjectKind, subjectRef, newState, detail string, at time.Time) error {
	inRepo, err := sc.Ext(incidentKind)
	if err != nil {
		return err
	}
	open, hasOpen, err := findOne(ctx, inRepo,
		eq(colInSubjectKind, subjectKind), eq(colInSubjectRef, subjectRef), eq(colInState, "open"))
	if err != nil {
		return err
	}

	switch newState {
	case stateHealthy:
		if hasOpen {
			open[colInState] = "resolved"
			open[colInResolvedAt] = model.NewTimestamp(at).String()
			if _, err := inRepo.Update(ctx, open); err != nil {
				return err
			}
		}
		return nil
	case stateDegraded, stateDown:
		sev := string(sevForState(newState))
		if !hasOpen {
			rec := model.Record{
				colInSubjectKind: subjectKind,
				colInSubjectRef:  subjectRef,
				colInKind:        newState,
				colInSeverity:    sev,
				colInState:       "open",
				colInOpenedAt:    model.NewTimestamp(at).String(),
				colInSummary:     clamp(stateTitle(subjectKind, subjectRef, newState, ""), maxNameLen),
			}
			if !checkID.IsZero() {
				rec[colInCheckRef] = checkID.String()
			}
			if dh := hashHex(detail); dh != "" {
				rec[colInDetailHash] = dh
			}
			_, err := inRepo.Create(ctx, rec)
			return err
		}
		// Escalate an open degraded incident to down; never downgrade in place.
		if newState == stateDown && open.String(colInKind) != stateDown {
			open[colInKind] = stateDown
			open[colInSeverity] = sev
			_, err := inRepo.Update(ctx, open)
			return err
		}
		return nil
	default:
		return nil
	}
}

// mirrorHealthStatusTx upserts the subject's current state into the core
// HealthStatus entity (the entity reserved for this module, ARCHITECTURE.md) WHEN the
// subject ref is a core entity id — so other planes and the core can read an
// agent/MCP's health via Scope.Health(). A natural-name subject has no core row to
// mirror to and is skipped (the check row holds its state regardless).
func (m *Module) mirrorHealthStatusTx(ctx context.Context, sc store.Scope, subjectKind, subjectRef, newState string, latencyMS int64, detail string, at time.Time) error {
	subjectID := parseIDOrZero(subjectRef)
	if subjectID.IsZero() {
		return nil
	}
	repo := sc.Health()
	existing, _, err := repo.List(ctx, model.Query{
		Filters: []model.Filter{eq("subject_kind", subjectKind), eq("subject_id", subjectID.String())},
		Limit:   1,
	})
	if err != nil {
		return err
	}
	hs := model.HealthStatus{
		SubjectKind: subjectKind,
		SubjectID:   subjectID,
		State:       coreHealthState(newState),
		CheckedAt:   model.NewTimestamp(at),
		Detail:      hashHex(detail), // one-way fingerprint, never the raw detail (docs/SECURITY-HARDENING.md)
	}
	if latencyMS >= 0 {
		hs.LatencyMS = latencyMS
	}
	if len(existing) == 0 {
		_, err := repo.Create(ctx, hs)
		return err
	}
	cur := existing[0]
	cur.State = hs.State
	cur.CheckedAt = hs.CheckedAt
	cur.Detail = hs.Detail
	if latencyMS >= 0 {
		cur.LatencyMS = latencyMS
	}
	_, err = repo.Update(ctx, cur)
	return err
}

// stateTitle builds a short, non-sensitive headline for a transition.
func stateTitle(subjectKind, subjectRef, newState, cause string) string {
	subj := subjectKind + " " + clamp(subjectRef, 120)
	switch newState {
	case stateDown:
		if cause == causeSweep {
			return subj + " is DOWN — no telemetry within expected cadence (possible evasion)"
		}
		return subj + " is DOWN"
	case stateDegraded:
		if cause == causeSweep {
			return subj + " is DEGRADED — telemetry overdue vs expected cadence"
		}
		return subj + " is DEGRADED"
	case stateHealthy:
		return subj + " recovered — healthy"
	default:
		return subj + " state: " + newState
	}
}

// stateDetail composes the redaction-safe detail string that becomes the one-way
// detail_hash. It carries the cause and any already-safe note, never raw content.
func stateDetail(cause, detail string) string {
	if detail == "" {
		return cause
	}
	return cause + ":" + detail
}
