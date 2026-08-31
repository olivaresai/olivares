// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// routine governance policies: operator-authored cadence, concurrency
// and approval controls Claude Code Routines must satisfy. A policy scopes to
// (tenant | workspace | user) and governs whether a routine may be created,
// how frequently it fires, and how many may run concurrently. The posture
// endpoint aggregates the effective policy for a scope lookup.

// Routine policy scope vocabulary.
const (
	rpScopeTenant    = "tenant"
	rpScopeWorkspace = "workspace"
	rpScopeUser      = "user"
)

// minCadenceFloor is the lowest cadence floor an operator may set (60 s).
// A cadence of 0 means "no floor" (any frequency allowed).
const minCadenceFloor = 60

// maxCadenceFloor is the highest cadence floor an operator may set: the longest
// interval a governed schedule can itself declare (module IV bounds
// expected_interval_seconds at ~366 days). A floor above it is unsatisfiable by
// construction — no admissible routine could ever meet it — and, unbounded, a
// large value also overflowed the enforcement comparison into a silent ALLOW.
// The enforcement side is now overflow-safe regardless; this bound stops the
// nonsense policy from being authored at all.
const maxCadenceFloor = 31622400

// routinePolicyDTO is the policy as returned to callers.
type routinePolicyDTO struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	ScopeKind       string `json:"scope_kind"`
	ScopeRef        string `json:"scope_ref,omitempty"`
	Enabled         bool   `json:"enabled"`
	MaxCadenceSec   int64  `json:"max_cadence_seconds"`
	MaxActive       int64  `json:"max_active_routines"`
	RequireApproval bool   `json:"require_approval"`
	// these two are TRI-STATE and must round-trip as such: a JSON null
	// is the documented "any"/"none", while an authored EMPTY array is a
	// deliberate deny-all/... that enforcement honors. `omitempty` used to
	// erase the difference, so an operator could not see which one they had
	// written.
	AllowedCron *[]string `json:"allowed_cron_patterns"`
	BlockedEnvs *[]string `json:"blocked_environments"`
	// UNREADABLE is a FOURTH state the projection above cannot carry. A
	// stored column the engine cannot parse is deliberately rendered as an empty
	// array (recJSONStringsPtr: the read surface must not paint it as
	// unconstrained), which makes an authored deny-all and an unreadable column
	// indistinguishable on the wire. They are not the same fact and enforcement
	// treats them oppositely: an authored empty list composes normally, while an
	// unreadable one makes the whole resolution INDETERMINATE and denies closed
	// (routinepolicy_resolve.go).
	//
	// Without this flag a console cannot tell them apart, and — the reason it
	// exists — an editor that faithfully re-sends what it was shown would turn
	// the projected [] into a REAL empty list. That silently repairs the column
	// and drops the indeterminate: an unreadable blocklist would go from "deny
	// every fire" to "block nothing". A fail-open produced by an honest edit of
	// an unrelated field.
	AllowedCronUnreadable bool   `json:"allowed_cron_patterns_unreadable"`
	BlockedEnvsUnreadable bool   `json:"blocked_environments_unreadable"`
	CreatedBy             string `json:"created_by,omitempty"`
}

func toRoutinePolicyDTO(rec model.Record) routinePolicyDTO {
	return routinePolicyDTO{
		ID:                    rec.String(model.ColID),
		Name:                  rec.String(colRPName),
		ScopeKind:             rec.String(colRPScopeKind),
		ScopeRef:              rec.String(colRPScopeRef),
		Enabled:               rec.Bool(colRPEnabled),
		MaxCadenceSec:         rec.Int(colRPMaxCadenceSec),
		MaxActive:             rec.Int(colRPMaxActive),
		RequireApproval:       rec.Bool(colRPRequireApproval),
		AllowedCron:           recJSONStringsPtr(rec, colRPAllowedCron),
		BlockedEnvs:           recJSONStringsPtr(rec, colRPBlockedEnvs),
		AllowedCronUnreadable: routineListOf(rec, colRPAllowedCron).Corrupt,
		BlockedEnvsUnreadable: routineListOf(rec, colRPBlockedEnvs).Corrupt,
		CreatedBy:             rec.String(colRPCreatedBy),
	}
}

// recJSONStringsPtr projects a JSON string-array column as a TRI-STATE value:
// nil for an absent/NULL column ("any"/"none"), and a non-nil pointer — which
// may address an EMPTY slice — for an authored list. It shares
// routineListPresent with the enforcement path so the API can never describe a
// policy differently from the way it is enforced (routinepolicy_resolve.go).
func recJSONStringsPtr(rec model.Record, col string) *[]string {
	v := routineListOf(rec, col)
	if !v.Present && !v.Corrupt {
		return nil
	}
	// A corrupt column renders as an empty array rather than as "any": the
	// enforcement path refuses such a policy outright (it is indeterminate), and
	// the read surface must not paint it as unconstrained either.
	list := v.Items
	if list == nil {
		list = []string{}
	}
	return &list
}

// recJSONStrings extracts a []string from a JSON column, returning nil for
// null/empty values.
func recJSONStrings(rec model.Record, col string) []string {
	v, ok := rec[col]
	if !ok || v == nil {
		return nil
	}
	switch val := v.(type) {
	case []any:
		out := make([]string, 0, len(val))
		for _, item := range val {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case string:
		if val == "" || val == "null" {
			return nil
		}
		var out []string
		if err := json.Unmarshal([]byte(val), &out); err != nil {
			return nil
		}
		return out
	default:
		return nil
	}
}

// createRoutinePolicyRequest is the POST body.
type createRoutinePolicyRequest struct {
	Name            string   `json:"name"`
	ScopeKind       string   `json:"scope_kind"`
	ScopeRef        string   `json:"scope_ref,omitempty"`
	Enabled         *bool    `json:"enabled,omitempty"`
	MaxCadenceSec   int64    `json:"max_cadence_seconds"`
	MaxActive       int64    `json:"max_active_routines"`
	RequireApproval bool     `json:"require_approval"`
	AllowedCron     []string `json:"allowed_cron_patterns,omitempty"`
	BlockedEnvs     []string `json:"blocked_environments,omitempty"`
}

// updateRoutinePolicyRequest is the PUT body (all fields required on update).
type updateRoutinePolicyRequest struct {
	Enabled         *bool  `json:"enabled,omitempty"`
	MaxCadenceSec   *int64 `json:"max_cadence_seconds,omitempty"`
	MaxActive       *int64 `json:"max_active_routines,omitempty"`
	RequireApproval *bool  `json:"require_approval,omitempty"`
	// raw so the three distinct intents survive the decode: ABSENT
	// ("leave it"), JSON null ("clear it back to any/none") and [] ("an
	// authored deny-all"). With a plain []string, absent and null both decode
	// to nil, so an allowlist could be set but never CLEARED.
	AllowedCron json.RawMessage `json:"allowed_cron_patterns,omitempty"`
	BlockedEnvs json.RawMessage `json:"blocked_environments,omitempty"`
}

// applyJSONListUpdate applies one tri-state list field to rec. It returns an
// error for a body that is neither null nor an array of strings, so a malformed
// update is a 400 rather than a silently-dropped control.
func applyJSONListUpdate(rec model.Record, col string, raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil // absent: leave the stored value alone
	}
	if string(bytes.TrimSpace(raw)) == "null" {
		rec[col] = nil // explicit clear
		return nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err != nil {
		return err
	}
	if list == nil {
		list = []string{}
	}
	rec[col] = jsonStringsVal(list)
	return nil
}

// validateRoutinePolicyScopeKind validates the scope_kind vocabulary.
func validateRoutinePolicyScopeKind(kind string) bool {
	switch kind {
	case rpScopeTenant, rpScopeWorkspace, rpScopeUser:
		return true
	default:
		return false
	}
}

// handleListRoutinePolicies lists routine policies, optionally filtered by
// scope_kind or enabled.
func (m *Module) handleListRoutinePolicies(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if v := r.URL.Query().Get("scope_kind"); v != "" {
		q.Filters = append(q.Filters, eq(colRPScopeKind, v))
	}
	if v := r.URL.Query().Get("enabled"); v == "true" {
		q.Filters = append(q.Filters, eq(colRPEnabled, true))
	} else if v == "false" {
		q.Filters = append(q.Filters, eq(colRPEnabled, false))
	}
	out := listResponse[routinePolicyDTO]{Items: []routinePolicyDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(routinePolicyKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toRoutinePolicyDTO(rec))
		}
		out.Cursor, out.HasMore = page.Cursor, page.HasMore
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleCreateRoutinePolicy creates a routine policy.
func (m *Module) handleCreateRoutinePolicy(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in createRoutinePolicyRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	in.ScopeKind = strings.ToLower(strings.TrimSpace(in.ScopeKind))
	in.ScopeRef = strings.TrimSpace(in.ScopeRef)

	if in.Name == "" || len(in.Name) > maxNoteLen {
		writeJSON(w, http.StatusBadRequest, errorBody("a policy name is required (max 4096 chars)"))
		return
	}
	if containsInlineCredential(in.Name) {
		writeJSON(w, http.StatusBadRequest, errorBody("name must not contain a credential"))
		return
	}
	if !validateRoutinePolicyScopeKind(in.ScopeKind) {
		writeJSON(w, http.StatusBadRequest, errorBody("scope_kind must be one of tenant, workspace, user"))
		return
	}
	if in.ScopeKind == rpScopeTenant && in.ScopeRef != "" {
		writeJSON(w, http.StatusBadRequest, errorBody("scope_ref must be empty for a tenant-scoped policy"))
		return
	}
	if (in.ScopeKind == rpScopeWorkspace || in.ScopeKind == rpScopeUser) && in.ScopeRef == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("scope_ref is required for workspace/user scope"))
		return
	}
	if len(in.ScopeRef) > maxNoteLen {
		writeJSON(w, http.StatusBadRequest, errorBody("scope_ref too long"))
		return
	}
	if containsInlineCredential(in.ScopeRef) {
		writeJSON(w, http.StatusBadRequest, errorBody("scope_ref must not contain a credential"))
		return
	}
	if in.MaxCadenceSec != 0 && (in.MaxCadenceSec < int64(minCadenceFloor) || in.MaxCadenceSec > int64(maxCadenceFloor)) {
		writeJSON(w, http.StatusBadRequest, errorBody("max_cadence_seconds must be 0 (no floor), or between 60 and 31622400"))
		return
	}
	if in.MaxActive < 0 {
		writeJSON(w, http.StatusBadRequest, errorBody("max_active_routines must be >= 0"))
		return
	}

	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}

	var out routinePolicyDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(routinePolicyKind)
		if err != nil {
			return err
		}
		rec := model.Record{
			colRPName:            in.Name,
			colRPScopeKind:       in.ScopeKind,
			colRPScopeRef:        in.ScopeRef,
			colRPEnabled:         enabled,
			colRPMaxCadenceSec:   in.MaxCadenceSec,
			colRPMaxActive:       in.MaxActive,
			colRPRequireApproval: in.RequireApproval,
			colRPCreatedBy:       mc.Principal.Actor(),
		}
		if in.AllowedCron != nil {
			rec[colRPAllowedCron] = jsonStringsVal(in.AllowedCron)
		}
		if in.BlockedEnvs != nil {
			rec[colRPBlockedEnvs] = jsonStringsVal(in.BlockedEnvs)
		}
		created, err := repo.Create(r.Context(), rec)
		if err != nil {
			return err
		}
		out = toRoutinePolicyDTO(created)
		return auditEvent(r.Context(), sc, mc, "governance.routine_policy.create", routinePolicyKind, model.ID(out.ID), map[string]any{
			"name": in.Name, "scope_kind": in.ScopeKind,
		})
	})
	if err != nil {
		if isConflict(err) {
			writeJSON(w, http.StatusConflict, errorBody("a routine policy with this name already exists"))
			return
		}
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// handleGetRoutinePolicy returns one routine policy by ID.
func (m *Module) handleGetRoutinePolicy(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var (
		out   routinePolicyDTO
		found bool
	)
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(routinePolicyKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		found, out = true, toRoutinePolicyDTO(rec)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleUpdateRoutinePolicy updates a routine policy's mutable fields.
// Name and scope are immutable (the operator-facing identity).
func (m *Module) handleUpdateRoutinePolicy(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var in updateRoutinePolicyRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.MaxCadenceSec != nil && *in.MaxCadenceSec != 0 &&
		(*in.MaxCadenceSec < int64(minCadenceFloor) || *in.MaxCadenceSec > int64(maxCadenceFloor)) {
		writeJSON(w, http.StatusBadRequest, errorBody("max_cadence_seconds must be 0 (no floor), or between 60 and 31622400"))
		return
	}
	if in.MaxActive != nil && *in.MaxActive < 0 {
		writeJSON(w, http.StatusBadRequest, errorBody("max_active_routines must be >= 0"))
		return
	}

	var out routinePolicyDTO
	badList := false
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(routinePolicyKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		if in.Enabled != nil {
			rec[colRPEnabled] = *in.Enabled
		}
		if in.MaxCadenceSec != nil {
			rec[colRPMaxCadenceSec] = *in.MaxCadenceSec
		}
		if in.MaxActive != nil {
			rec[colRPMaxActive] = *in.MaxActive
		}
		if in.RequireApproval != nil {
			rec[colRPRequireApproval] = *in.RequireApproval
		}
		if err := applyJSONListUpdate(rec, colRPAllowedCron, in.AllowedCron); err != nil {
			badList = true
			return nil
		}
		if err := applyJSONListUpdate(rec, colRPBlockedEnvs, in.BlockedEnvs); err != nil {
			badList = true
			return nil
		}
		updated, err := repo.Update(r.Context(), rec)
		if err != nil {
			return err
		}
		out = toRoutinePolicyDTO(updated)
		return auditEvent(r.Context(), sc, mc, "governance.routine_policy.update", routinePolicyKind, id, nil)
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if badList {
		writeJSON(w, http.StatusBadRequest, errorBody("allowed_cron_patterns and blocked_environments must be null or an array of strings"))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDeleteRoutinePolicy deletes a routine policy.
func (m *Module) handleDeleteRoutinePolicy(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(routinePolicyKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		if err := repo.Delete(r.Context(), model.ID(rec.String(model.ColID))); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "governance.routine_policy.delete", routinePolicyKind, id, map[string]any{
			"name": rec.String(colRPName),
		})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// routineActiveCapDTO is one scope's cap on active routine declarations. Caps
// stay a VECTOR because a tenant cap of 100 and a user cap of 2 constrain
// different populations and cannot be collapsed into one number
// (routinepolicy_resolve.go, RoutineActiveCap).
type routineActiveCapDTO struct {
	ScopeKind string `json:"scope_kind"`
	ScopeRef  string `json:"scope_ref"`
	Max       int64  `json:"max"`
}

// routineEffectiveDTO is the COMPOSED posture for one resolution scope, as
// produced by the same fold orchestration enforces (composeRoutinePolicies).
// It exists so a console can show what will actually happen to a routine
// without re-deriving monotone composition — a second implementation would
// eventually disagree with enforcement, and the direction it disagrees in is
// "this looks unconstrained" over a policy set that denies.
//
// Every list field is emitted unconditionally (no omitempty): an absent key and
// an empty array are exactly the distinction this subsystem exists to preserve.
type routineEffectiveDTO struct {
	// ScopeWorkspaceRef / ScopeUserRef echo the axes the composition was
	// resolved FOR, after the tenant's default workspace is applied. A reader
	// that does not know which routine it is looking at cannot act on the answer.
	ScopeWorkspaceRef string `json:"scope_workspace_ref"`
	ScopeUserRef      string `json:"scope_user_ref"`
	// ScopeUserKnown echoes whether the user axis was answerable. False is the
	// legacy/unrecognized-owner case orchestration hits, where a user-scoped
	// policy makes the whole resolution indeterminate.
	ScopeUserKnown bool `json:"scope_user_known"`
	// DefaultWorkspaceRef is the tenant default the empty workspace resolved to.
	DefaultWorkspaceRef string `json:"default_workspace_ref"`

	// InForce is true when at least one ENABLED policy matched this scope.
	InForce bool `json:"in_force"`
	// Indeterminate marks a resolution that could not be completed — an enabled
	// policy scopes an axis that could not be supplied, or a stored list is
	// unreadable. Enforcement denies closed on it, so the console must show it
	// as a REFUSAL, never as an absence of controls.
	Indeterminate     bool   `json:"indeterminate"`
	IndeterminateAxis string `json:"indeterminate_axis"`

	MinIntervalSec  int64 `json:"min_interval_seconds"`
	RequireApproval bool  `json:"require_approval"`

	// CronAllowlistInForce distinguishes "no allowlist at all" (any cron
	// admitted) from an authored EMPTY one (every cron denied). With
	// CronAllowed alone the two are indistinguishable.
	CronAllowlistInForce bool     `json:"cron_allowlist_in_force"`
	CronAllowed          []string `json:"cron_allowed"`
	BlockedEnvs          []string `json:"blocked_environments"`

	ActiveCaps []routineActiveCapDTO `json:"active_caps"`

	// PolicyRefs are the matched policy ids — the drill-down from a composed
	// value back to the rows that produced it. Digest fingerprints the composed
	// decision so a change of posture is visible without echoing policy bodies.
	PolicyRefs []string `json:"policy_refs"`
	Digest     string   `json:"digest"`
}

func toRoutineEffectiveDTO(eff EffectiveRoutinePolicy, scope RoutineScope) routineEffectiveDTO {
	out := routineEffectiveDTO{
		ScopeWorkspaceRef:    eff.EffectiveWorkspaceRef,
		ScopeUserRef:         eff.EffectiveUserRef,
		ScopeUserKnown:       scope.UserKnown,
		DefaultWorkspaceRef:  eff.DefaultWorkspaceRef,
		InForce:              eff.InForce,
		Indeterminate:        eff.Indeterminate,
		IndeterminateAxis:    eff.IndeterminateAxis,
		MinIntervalSec:       eff.MinIntervalSec,
		RequireApproval:      eff.RequireApproval,
		CronAllowlistInForce: eff.CronAllowlistInForce,
		CronAllowed:          []string{},
		BlockedEnvs:          []string{},
		ActiveCaps:           []routineActiveCapDTO{},
		PolicyRefs:           []string{},
		Digest:               eff.Digest,
	}
	out.CronAllowed = append(out.CronAllowed, eff.CronAllowed...)
	out.BlockedEnvs = append(out.BlockedEnvs, eff.BlockedEnvs...)
	out.PolicyRefs = append(out.PolicyRefs, eff.PolicyRefs...)
	for _, c := range eff.ActiveCaps {
		out.ActiveCaps = append(out.ActiveCaps, routineActiveCapDTO{
			ScopeKind: c.ScopeKind, ScopeRef: c.ScopeRef, Max: c.Max,
		})
	}
	return out
}

// routinePostureDTO is the aggregated view of routine governance posture.
type routinePostureDTO struct {
	TotalPolicies   int                `json:"total_policies"`
	EnabledPolicies int                `json:"enabled_policies"`
	Policies        []routinePolicyDTO `json:"policies"`
	// Effective is the composed decision for the requested scope. It is a
	// pointer only so a resolution failure can be reported as absent rather than
	// as a zero value that reads like "nothing is in force"; the handler emits
	// it on every success.
	Effective *routineEffectiveDTO `json:"effective"`
}

// handleRoutinePosture returns the routine governance posture: EVERY policy in
// the tenant (enabled or not, with the two counters splitting them), plus the
// COMPOSED decision for one resolution scope.
//
// The scope comes from the caller: ?workspace_ref= and ?user_ref=, both
// optional. Omitting them asks the honest baseline question — what governs a
// routine with no owning user in the tenant's default workspace — which is the
// scope a token-declared routine actually resolves under.
//
// The composition is NOT recomputed here: it is resolveRoutinePolicyIn, the
// same function the exported enforcement seam calls.
func (m *Module) handleRoutinePosture(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	// UserKnown DEFAULTS to true because the caller is supplying the axis: an
	// empty user_ref means "a routine with no owning user", which is a definite
	// non-match against a user-scoped policy, not an unanswerable axis.
	//
	// But ?user_known=false must be reachable, because orchestration genuinely
	// produces that scope for a routine whose owner it cannot recognize
	// (modules/orchestration/routinepolicy.go), and there the resolution goes
	// INDETERMINATE and the fire is refused. An operator debugging exactly that
	// refusal needs the console to reproduce it; with the axis pinned to true
	// the panel could never show the posture that is actually denying.
	scope := RoutineScope{
		UserRef:      strings.TrimSpace(r.URL.Query().Get("user_ref")),
		UserKnown:    r.URL.Query().Get("user_known") != "false",
		WorkspaceRef: strings.TrimSpace(r.URL.Query().Get("workspace_ref")),
	}
	// A named user AND an unanswerable user axis is a contradiction, and the
	// resolver silently resolves it in favor of the name — so the answer would
	// be composed FOR that user while the caller believes it asked about an
	// unknown one. Refusing is the only reading that cannot mislead.
	if !scope.UserKnown && scope.UserRef != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(
			"user_known=false cannot be combined with a user_ref: an unanswerable user axis has no owner"))
		return
	}

	out := routinePostureDTO{Policies: []routinePolicyDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(routinePolicyKind)
		if err != nil {
			return err
		}
		all, err := listAll(r.Context(), repo)
		if err != nil {
			return err
		}
		out.TotalPolicies = len(all)
		for _, rec := range all {
			dto := toRoutinePolicyDTO(rec)
			if dto.Enabled {
				out.EnabledPolicies++
			}
			out.Policies = append(out.Policies, dto)
		}
		eff, rerr := resolveRoutinePolicyIn(r.Context(), sc, scope)
		if rerr != nil {
			return rerr
		}
		dto := toRoutineEffectiveDTO(eff, scope)
		out.Effective = &dto
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// jsonStringsVal encodes a string slice as a JSON column value.
func jsonStringsVal(ss []string) any {
	if ss == nil {
		return nil
	}
	b, err := json.Marshal(ss)
	if err != nil {
		return nil
	}
	return string(b)
}
