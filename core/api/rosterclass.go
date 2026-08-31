// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

// ONE classification of a roster entry, shared by every surface that reports on
// connectors (2026-08-05).
//
// WHY THIS FILE EXISTS. Three surfaces classified roster rows independently, each
// by matching the single literal "failed", and a source the engine had REFUSED TO
// WIRE was therefore invisible in all three at once. Measured end to end: one
// enabled source of an unsupported kind, the engine logging
// `sources wired from the durable roster added=0 rejected=1` and ingesting
// nothing, and then —
//
//	GET /v1/console/health-summary  ->  connectors_error: 0
//	GET /v1/connectors/health       ->  summary.disabled: 1   (with enabled:true)
//	GET /status                     ->  connectors: "operational"
//
// The operator asked for a source, the engine rejected it, and every aggregate
// reported no error. Alerting and support stay green while the product receives no
// data at all. `not_wired` is not the only status this could happen to: the bug is
// matching literals per call site rather than classifying once, so a status added
// tomorrow silently lands in whatever bucket each site's `switch` happens to have.
//
// The rules, and each one is a decision rather than a formatting choice:
//
//   - enabled + not carrying data (failed OR not_wired) is an ERROR. "Wired and
//     broken" and "never wired" are different causes with the SAME consequence for
//     the operator, and the consequence is what an aggregate reports.
//   - enabled + stopped is DEGRADED, not an error: it was halted deliberately, but
//     it is not ingesting and the fleet is not whole.
//   - the "disabled" bucket may only hold rows with enabled == false. It used to
//     absorb not_wired, which is how an enabled source came to be counted as
//     something the operator had switched off.
//   - a status this build does not recognize counts as an ERROR, never as fine.
//     Fail-closed: an unknown state is not evidence of health.
type rosterClass int

const (
	// rosterUnknownStatus is a status string this build does not recognize. It is
	// deliberately the zero value so a future switch that forgets a case cannot
	// default into "healthy".
	rosterUnknownStatus rosterClass = iota
	rosterRunning
	rosterErrored
	rosterHalted
	rosterDisabled
)

// classifyRosterEntry maps (enabled, status) onto the operator-facing class.
func classifyRosterEntry(enabled bool, status string) rosterClass {
	if !enabled {
		return rosterDisabled
	}
	switch status {
	case "running":
		return rosterRunning
	case "failed", "not_wired":
		return rosterErrored
	case "stopped":
		return rosterHalted
	case "disabled":
		// enabled == true with status "disabled" is a contradiction the roster
		// should not produce. It is NOT quietly filed as disabled: a row whose two
		// fields disagree is a state nobody can act on, so it is surfaced.
		return rosterUnknownStatus
	default:
		return rosterUnknownStatus
	}
}

// isRosterError reports whether a class must count towards an error total.
func (c rosterClass) isRosterError() bool {
	return c == rosterErrored || c == rosterUnknownStatus
}

// rosterTally is the shared count every connector surface aggregates from.
type rosterTally struct {
	Total    int
	Running  int
	Errored  int
	Halted   int
	Disabled int
}

// add folds one entry into the tally.
func (t *rosterTally) add(enabled bool, status string) {
	t.Total++
	switch classifyRosterEntry(enabled, status) {
	case rosterRunning:
		t.Running++
	case rosterHalted:
		t.Halted++
	case rosterDisabled:
		t.Disabled++
	default: // rosterErrored and rosterUnknownStatus
		t.Errored++
	}
}

// enabledTotal is the number of rows the operator asked to be running: everything
// except the ones they switched off. It is the denominator for "is the fleet
// whole", which must not be diluted by rows nobody expects to run.
func (t rosterTally) enabledTotal() int { return t.Total - t.Disabled }
