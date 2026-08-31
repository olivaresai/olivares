// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

import (
	"time"
)

// This file evaluates the declared model-governance dimensions (reference.go) for
// one model ref at a point in time — lifecycle state, days to retirement, the
// published replacement, the retention class and the access tier — and derives
// the per-candidate routing-policy verdict. The Anthropic lifecycle
// vocabulary is Active/Legacy/Deprecated/Retired; the module collapses
// Active/Legacy into "active" (both serve requests with no scheduled cutoff).
// Requests to a RETIRED model fail at the provider, so routing one is a
// governance miss the policy can deny pre-flight.
//
// Deny boundaries (ARCHITECTURE.md):
//   - An UNKNOWN model (no reference family) is "active" for LIFECYCLE purposes:
//     lifecycle denies are driven by published schedules, and absence of data must
//     not block an operator's own/unlisted models.
//   - The DENY-CLOSED dimensions are RetentionClass ("" — not verified — denies
//     under require_zdr exactly like "covered") and AccessTier (a non-empty tier
//     denies unless the policy enrolls it, regardless of any policy flag).

// Lifecycle states, per the Anthropic-operated schedule in the reference table.
const (
	lifecycleActive     = "active"
	lifecycleDeprecated = "deprecated"
	lifecycleRetired    = "retired"
)

// lifecycleDateFormat is the ISO date layout of the table's lifecycle stamps.
const lifecycleDateFormat = "2006-01-02"

// lifecycleStatus is the governance evaluation for one model ref at one instant.
type lifecycleStatus struct {
	// State is active|deprecated|retired per the Anthropic-operated schedule.
	State string
	// DaysToRetirement counts the full days from now until RetiredOn (0 when no
	// retirement is scheduled or the date has passed — check State for retired).
	DaysToRetirement int
	// Replacement is the published successor model id ("" if none named).
	Replacement string
	// RetentionClass and AccessTier mirror the reference fields ("" when unknown).
	RetentionClass string
	AccessTier     string
	// Known reports whether a reference family matched the ref at all.
	Known bool
}

// lifecycleOf evaluates the declared governance for modelRef at now. A ref with
// no reference family yields the zero dimensions and State active (see the deny
// boundaries above). Date comparison is on the UTC calendar day: a model is
// deprecated/retired ON its published date, not the day after.
func lifecycleOf(modelRef string, now time.Time) lifecycleStatus {
	ref, ok := lookupReference(modelRef)
	if !ok {
		return lifecycleStatus{State: lifecycleActive}
	}
	st := lifecycleStatus{
		State: lifecycleActive, Replacement: ref.ReplacementRef,
		RetentionClass: ref.RetentionClass, AccessTier: ref.AccessTier, Known: true,
	}
	day := now.UTC().Truncate(24 * time.Hour)
	if d, ok := lifecycleDate(ref.DeprecatedOn); ok && !day.Before(d) {
		st.State = lifecycleDeprecated
	}
	if d, ok := lifecycleDate(ref.RetiredOn); ok {
		if !day.Before(d) {
			st.State = lifecycleRetired
		} else {
			st.DaysToRetirement = int(d.Sub(day) / (24 * time.Hour))
		}
	}
	return st
}

// lifecycleDate parses an ISO lifecycle stamp; a missing stamp yields ok=false. A
// malformed stamp is treated as absent — the table is repo-versioned and
// test-pinned, so a malformed date is a defect the tests catch, never a runtime
// condition to deny on.
func lifecycleDate(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	d, err := time.Parse(lifecycleDateFormat, s)
	if err != nil {
		return time.Time{}, false
	}
	return d, true
}

// Governance deny kinds the routing policy reports (decisionDTO.GovernanceDeny).
const (
	govDenyRetired     = "retired"
	govDenyDeprecated  = "deprecated"
	govDenyZDR         = "zdr"
	govDenyAccessTier  = "access_tier"
	govDenyEntitlement = "entitlement"
)

// governanceDeny evaluates the model-governance checks for one candidate
// model under this policy: access tier (always-on, deny-closed), provider
// entitlement suspension (operator-attested, only after enrollment), lifecycle
// (opt-in deny_retired/deny_deprecated) and retention (require_zdr, deny-closed on
// the class). Reasons are generic and money-free — /resolve is read-tier — and only
// the published replacement ref (non-sensitive, actionable) rides along.
func (s routingSpec) governanceDeny(modelRef string, now time.Time, suspendedTiers []string) (kind, reason, replacement string, denied bool) {
	ls := lifecycleOf(modelRef, now)
	// Access tier is DENY-CLOSED REGARDLESS of policy flags: a restricted-access
	// model (Project Glasswing) is never routable unless the policy in effect
	// enrolls its tier in access_tiers. /resolve always evaluates a stored policy,
	// so "no policy at all" degenerates to "tier not enrolled": denied.
	if ls.AccessTier != "" && !containsString(s.AccessTiers, ls.AccessTier) {
		return govDenyAccessTier, "routing denied: model requires restricted access tier enrollment", "", true
	}
	// A provider entitlement can be suspended outside this control plane even when
	// the policy remains enrolled. The operator-attested suspension only narrows an
	// already-enrolled tier; an unknown/absent/granted tier leaves behavior unchanged.
	if ls.AccessTier != "" && containsString(suspendedTiers, ls.AccessTier) {
		return govDenyEntitlement, "routing denied: provider entitlement for the restricted access tier is suspended (operator-attested)", "", true
	}
	// deny_retired / deny_deprecated are OPT-IN (absent flags deny nothing).
	// deny_deprecated is strictly stronger: a retired model is past deprecation,
	// so it denies under either flag — reported as retired, the actionable truth.
	if ls.State == lifecycleRetired && (s.DenyRetired || s.DenyDeprecated) {
		return govDenyRetired, "routing denied: model is retired", ls.Replacement, true
	}
	if ls.State == lifecycleDeprecated && s.DenyDeprecated {
		return govDenyDeprecated, "routing denied: model is deprecated", ls.Replacement, true
	}
	// require_zdr is DENY-CLOSED on the retention class: "covered" (forced
	// retention) and "" (not verified) BOTH deny — only a verified zdr_eligible
	// model may serve a zero-data-retention workload.
	if s.RequireZDR && ls.RetentionClass != retentionZDREligible {
		return govDenyZDR, "routing denied: workload requires zero-data-retention and the model is not ZDR-eligible", "", true
	}
	return "", "", "", false
}

// containsString reports whether list contains v.
func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
