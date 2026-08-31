// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package health

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// ppmFull is one million — uptime is expressed in parts-per-million so 99.9% is
// 999000, avoiding floating point in the stored SLA target.
const ppmFull = 1_000_000

// reliability is the computed reliability of a subject over a trailing window,
// reconstructed from its append-only transition ledger.
type reliability struct {
	windowSeconds   float64
	observedSeconds float64 // span actually covered by transition history (denominator)
	downSeconds     float64
	degradedSeconds float64
	uptimePPM       int64
	currentState    string
}

// reliabilityFromEvents reconstructs how long a subject spent down/degraded inside
// [windowStart, now] from its ordered health_event transitions, then derives the
// uptime in parts-per-million OVER THE OBSERVED span — not the whole window. Each
// event marks the START of a state that holds until the next event (or now);
// intervals are clipped to the window, so a state that began before the window
// still counts its in-window portion. Time before the first ever event is "unknown"
// and is excluded from BOTH numerator and denominator: counting it as uptime would
// inflate the figure and mask a real breach for a low-history subject. With no
// history at all, observedSeconds is 0 and uptimePPM is 0 (undefined — callers must
// gate on observedSeconds > 0, never read it as 0% or 100%). Degraded counts as UP
// for the SLA (the subject still functions) but is reported separately.
func reliabilityFromEvents(events []model.Record, windowStart, now time.Time) reliability {
	r := reliability{windowSeconds: now.Sub(windowStart).Seconds(), currentState: stateUnknown}
	if r.windowSeconds <= 0 {
		return r
	}
	sort.SliceStable(events, func(i, j int) bool {
		return events[i].String(colEvOccurredAt) < events[j].String(colEvOccurredAt)
	})
	add := func(state string, from, to time.Time) {
		s, e := from, to
		if s.Before(windowStart) {
			s = windowStart
		}
		if e.After(now) {
			e = now
		}
		if !e.After(s) {
			return
		}
		dur := e.Sub(s).Seconds()
		switch state {
		case stateDown:
			r.downSeconds += dur
		case stateDegraded:
			r.degradedSeconds += dur
		}
	}
	cur := stateUnknown
	var since, firstEvent time.Time
	have := false
	for _, ev := range events {
		ts, err := model.ParseTimestamp(ev.String(colEvOccurredAt))
		if err != nil {
			continue
		}
		if !have {
			firstEvent = ts.Time()
		}
		if have {
			add(cur, since, ts.Time())
		}
		cur, since, have = ev.String(colEvState), ts.Time(), true
	}
	if !have {
		return r // no history: observedSeconds = 0, uptimePPM = 0 (undefined)
	}
	add(cur, since, now)
	r.currentState = cur
	// The observed span starts at the first knowledge point inside the window (an
	// event before windowStart establishes the state AT windowStart, so the window is
	// fully observed; a later first event leaves the lead-in unknown/excluded).
	observedStart := firstEvent
	if observedStart.Before(windowStart) {
		observedStart = windowStart
	}
	r.observedSeconds = now.Sub(observedStart).Seconds()
	if r.observedSeconds <= 0 {
		return r
	}
	up := r.observedSeconds - r.downSeconds
	if up < 0 {
		up = 0
	}
	r.uptimePPM = int64(float64(ppmFull) * up / r.observedSeconds)
	return r
}

// handleSLA reports the reliability of a subject over a trailing window
// (?subject_kind=&subject_ref=&window_seconds=). It reads the subject's check (for
// the SLA target and the current state) and reconstructs reliability from the
// append-only event ledger. Privileged read (RBAC-gated).
func (m *Module) handleSLA(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	subjectKind := r.URL.Query().Get("subject_kind")
	subjectRef := r.URL.Query().Get("subject_ref")
	if subjectKind == "" || subjectRef == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("subject_kind and subject_ref are required"))
		return
	}
	window := m.slaWindow
	if s := r.URL.Query().Get("window_seconds"); s != "" {
		if secs, err := time.ParseDuration(s + "s"); err == nil && secs > 0 {
			window = secs
		}
	}
	now := m.clock.Now().Time()
	windowStart := now.Add(-window)

	var dto slaDTO
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		checkRepo, err := sc.Ext(checkKind)
		if err != nil {
			return err
		}
		check, hasCheck, err := findOne(r.Context(), checkRepo, eq(colSubjectKind, subjectKind), eq(colSubjectRef, subjectRef))
		if err != nil {
			return err
		}
		evRepo, err := sc.Ext(eventKind)
		if err != nil {
			return err
		}
		events, err := listAll(r.Context(), evRepo, eq(colEvSubjectKind, subjectKind), eq(colEvSubjectRef, subjectRef))
		if err != nil {
			return err
		}
		rel := reliabilityFromEvents(events, windowStart, now)
		dto = toSLADTO(subjectKind, subjectRef, rel, window, check, hasCheck)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

// evaluateSLATx evaluates a check's trailing-window uptime against its SLA target,
// folding an SLA-breach flag onto the (in-memory) check record. It returns whether
// the flag changed (so the caller persists the check) and, on a NEW breach, the
// finding to emit after commit. Re-alerts are de-duplicated by the sticky
// sla_breach_open flag (a finding only fires on the open transition). A check with
// no SLA target configured is skipped.
func (m *Module) evaluateSLATx(ctx context.Context, sc store.Scope, check model.Record, now time.Time) (bool, *slaAlert, error) {
	target := check.Int(colSLATargetPM)
	if target <= 0 {
		return false, nil, nil
	}
	subjectKind := check.String(colSubjectKind)
	subjectRef := check.String(colSubjectRef)
	evRepo, err := sc.Ext(eventKind)
	if err != nil {
		return false, nil, err
	}
	events, err := listAll(ctx, evRepo, eq(colEvSubjectKind, subjectKind), eq(colEvSubjectRef, subjectRef))
	if err != nil {
		return false, nil, err
	}
	rel := reliabilityFromEvents(events, now.Add(-m.slaWindow), now)
	// Only judge against the target when there is observed history; a subject with no
	// reliability data is not a breach (it is undefined, not 0%).
	breaching := rel.observedSeconds > 0 && rel.uptimePPM < target
	open := check.Bool(colSLABreachOpen)
	switch {
	case breaching && !open:
		check[colSLABreachOpen] = true
		return true, &slaAlert{subjectKind: subjectKind, subjectRef: subjectRef, uptimePPM: rel.uptimePPM, targetPPM: target}, nil
	case !breaching && open:
		check[colSLABreachOpen] = false
		return true, nil, nil // cleared — no finding on recovery of the SLA window
	default:
		return false, nil, nil
	}
}

// slaAlert is a pending SLA-breach finding to emit after the sweep transaction
// commits.
type slaAlert struct {
	subjectKind string
	subjectRef  string
	uptimePPM   int64
	targetPPM   int64
}
