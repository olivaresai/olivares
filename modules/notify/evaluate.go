// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package notify

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
)

// routedTypes is the single source of truth for the event types notify can
// route: Init subscribes to exactly this set, a route's match_types may
// only name members of it (validateMatchTypes — a typo used to persist a route
// that could never fire), and GET /match-types exposes it to the console (the
// eventing GET /event-types pattern). Adding a type here is what makes it
// routable — nowhere else.
var routedTypes = []struct {
	Type        event.Type
	Description string
}{
	{event.TypeFindingReported, "A guardrail/red-team/forensic finding — the product-wide alert stream every module emits on."},
	{event.TypeApprovalRequested, "A governance approval was opened and awaits a human decision (the HITL origination card)."},
	{event.TypeApprovalResolved, "A governance approval reached a terminal outcome (approved, denied or canceled)."},
}

// busTypes returns the subscription allowlist in catalog order (consumed by
// Init so the subscription set and the validation set cannot drift).
func busTypes() []event.Type {
	out := make([]event.Type, 0, len(routedTypes))
	for _, e := range routedTypes {
		out = append(out, e.Type)
	}
	return out
}

// validMatchType reports whether t names a routed type.
func validMatchType(t string) bool {
	for _, e := range routedTypes {
		if string(e.Type) == t {
			return true
		}
	}
	return false
}

// validateMatchTypes returns "" when every entry names a routed type, or the
// 400 message for the first unroutable one. Empty entries are ignored (csvJoin
// drops them) and an empty set stays legal (match-all).
func validateMatchTypes(types []string) string {
	for _, t := range types {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if !validMatchType(t) {
			return fmt.Sprintf("unknown match type %q (see GET /match-types)", t)
		}
	}
	return ""
}

// matchTypeDTO is one catalog row of GET /match-types.
type matchTypeDTO struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

type matchTypesResponse struct {
	MatchTypes []matchTypeDTO `json:"match_types"`
}

// handleMatchTypes returns the routable event types a route's match_types may
// name — the console's multiselect source and the reference the validation
// errors point at.
func (m *Module) handleMatchTypes(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	out := make([]matchTypeDTO, 0, len(routedTypes))
	for _, e := range routedTypes {
		out = append(out, matchTypeDTO{Type: string(e.Type), Description: e.Description})
	}
	writeJSON(w, http.StatusOK, matchTypesResponse{MatchTypes: out})
}

// evaluateInput is a synthetic signal: the predicate dimensions only, never a
// payload — nothing is delivered, recorded or claimed.
type evaluateInput struct {
	EventType   string `json:"event_type"`
	Kind        string `json:"kind"`
	Severity    string `json:"severity"`
	Source      string `json:"source"`
	SubjectKind string `json:"subject_kind"`
}

// evaluateVerdict is one route's verdict: matched, or the predicate dimensions
// that rejected the signal ("disabled" marks a route whose predicate may select
// but which cannot fire).
type evaluateVerdict struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Enabled    bool     `json:"enabled"`
	Matched    bool     `json:"matched"`
	Mismatches []string `json:"mismatches"`
}

type evaluateResponse struct {
	Items        api.JSONArray[evaluateVerdict] `json:"items"`
	MatchedCount int                            `json:"matched_count"`
}

// handleEvaluateRoutes answers "which routes would this signal select?" without
// delivering anything. Predicate-only by design: dedup, throttle and the claim
// phase depend on ledger history, not the rule, so they are deliberately NOT
// simulated. Read-tier: probing a predicate reveals no more than GET /routes.
func (m *Module) handleEvaluateRoutes(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in evaluateInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if !validMatchType(in.EventType) {
		writeJSON(w, http.StatusBadRequest, errorBody(fmt.Sprintf("unknown event_type %q (see GET /match-types)", in.EventType)))
		return
	}
	if !validSeverity(in.Severity) {
		writeJSON(w, http.StatusBadRequest, errorBody("severity must be empty or info|low|medium|high|critical"))
		return
	}
	s := signal{
		eventType:   in.EventType,
		kind:        in.Kind,
		severity:    parseSeverity(in.Severity),
		source:      in.Source,
		subjectKind: in.SubjectKind,
	}
	out := evaluateResponse{Items: []evaluateVerdict{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(routeKind)
		if err != nil {
			return err
		}
		routes, err := listAll(r.Context(), repo)
		if err != nil {
			return err
		}
		for _, rec := range routes {
			pred := evaluateRoute(rec, s)
			enabled := rec.Bool(colEnabled)
			matched := enabled && len(pred) == 0
			if !enabled {
				pred = append(pred, "disabled")
			}
			if pred == nil {
				pred = []string{}
			}
			out.Items = append(out.Items, evaluateVerdict{
				ID:         rec.String(model.ColID),
				Name:       rec.String(colName),
				Enabled:    enabled,
				Matched:    matched,
				Mismatches: pred,
			})
			if matched {
				out.MatchedCount++
			}
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
