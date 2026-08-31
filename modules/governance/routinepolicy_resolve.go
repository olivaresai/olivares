// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/core/cron"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// routinepolicy_resolve.go is the READ seam that makes the routine
// policy enforceable. Until this file existed the five controls were persisted
// intent with no consumer outside their own CRUD — an operator could author a
// cadence floor, a concurrency cap, an approval requirement, a cron allowlist
// and a blocked-environment list, and NOTHING in the plane read them
// (§"Fuera de alcance": *"almacenan intención
// que nada aplica"*).
//
// Governance owns the POLICY; it does not own routines. modules/orchestration
// owns the schedules known as routines
// (§4.1: *"the orchestration budget
// gate passes the schedule ID as RoutineRef"*), so this seam mirrors
// KillSwitchState (killswitch.go): an exported, tenant-scoped read the
// composition root adapts into the consuming module's own port. Modules never
// import each other.
//
// COMPOSITION IS MONOTONE (most-restrictive-wins). The schema deliberately
// allows several policies per tenant — its only UNIQUE index is (tenant, name)
// — so overlapping tenant/workspace/user policies are normal. Folding them by
// specificity would let a narrow policy WEAKEN a tenant-wide one; every other
// composing enforcement point in this repo takes the restrictive fold instead
// (finops budgets.go "the most restrictive enforcing action", the kill switch's
// union of estate and agent stops). So: floors take the MAXIMUM, active caps
// stay a VECTOR keyed by scope (a tenant cap of 100 and a user cap of 2
// constrain different populations and cannot be collapsed into one number),
// approval ORs, cron allowlists INTERSECT and blocked environments UNION.

// RoutineScope is the governance scope of ONE routine, as persisted on the
// routine itself — never derived from whoever happens to be calling. A schedule
// records its owner user and workspace at declaration precisely so a later
// patch/restore/fire resolves the policy of the routine's OWNER rather than the
// policy of an admin or a token that can act on someone else's routine.
type RoutineScope struct {
	// UserRef is the owning user's stable id ("" when the routine was declared
	// by a principal with no user identity, e.g. a standalone system token).
	UserRef string
	// UserKnown distinguishes "this routine definitively has no owning user"
	// (a token-declared routine) from "this routine cannot answer for the user
	// axis" (an unreadable record). Only the second is indeterminate: a routine
	// owned by no user IS provably outside a policy scoped to a named user, and
	// treating it as unknown 503s every token-declared routine in the tenant
	// the moment one user-scoped policy exists.
	UserKnown bool
	// WorkspaceRef is the owning workspace id ("" when unknown/unconfined).
	WorkspaceRef string
}

// RoutineActiveCap is one scope's cap on ACTIVE routine declarations. The
// population it constrains is the routines whose own (ScopeKind, ScopeRef)
// matches — so the caller must count per cap, not once.
type RoutineActiveCap struct {
	ScopeKind string // tenant | workspace | user
	ScopeRef  string // "" for tenant
	Max       int64  // > 0; a zero cap is "no cap" and is never emitted here
}

// EffectiveRoutinePolicy is the resolved, composed posture for one RoutineScope.
type EffectiveRoutinePolicy struct {
	// InForce is true when at least one ENABLED policy matched.
	InForce bool

	// Indeterminate marks a resolution that could NOT be completed: an enabled
	// policy scopes an axis (workspace/user) the caller could not supply, so
	// "no match" would be a silent bypass rather than an answer. The consumer
	// must fail closed. IndeterminateAxis names the axis for the operator.
	Indeterminate     bool
	IndeterminateAxis string

	// MinIntervalSec is the composed cadence FLOOR in seconds (the maximum of
	// every matched floor). 0 = no floor.
	MinIntervalSec int64

	// RequireApproval is the OR of every matched policy's flag.
	RequireApproval bool

	// CronAllowed is the INTERSECTION of every matched non-null allowlist, in
	// canonical spelling. CronAllowlistInForce distinguishes "no allowlist"
	// (nil, any cron admitted) from an authored EMPTY one (deny every cron) —
	// an explicit [] is a deny-all the operator wrote, and collapsing it into
	// "unconstrained" would invert the control.
	CronAllowed          []string
	CronAllowlistInForce bool

	// BlockedEnvs is the UNION of every matched blocked-environment list.
	BlockedEnvs []string

	// ActiveCaps is the per-scope vector of active-declaration caps.
	ActiveCaps []RoutineActiveCap

	// EffectiveUserRef / EffectiveWorkspaceRef are the axis values the MATCH was
	// actually made on, after the workspace default is resolved. The consumer
	// counts an active-cap population with these, not with the raw columns:
	// matching on a derived value while counting on a raw one lets a row be
	// GOVERNED by a cap yet INVISIBLE to it, and the cap silently under-counts.
	EffectiveUserRef      string
	EffectiveWorkspaceRef string
	// DefaultWorkspaceRef is the tenant's default workspace, so the consumer can
	// normalise a candidate row's empty workspace the same way this resolver did.
	DefaultWorkspaceRef string

	// PolicyRefs are the matched policy ids and Digest a stable fingerprint of
	// the composed decision — both are evidence, so a denial can be traced to
	// the exact policy set without echoing its body.
	PolicyRefs []string
	Digest     string
}

// ResolveRoutinePolicy composes every ENABLED routine policy that scopes the
// given routine into one effective posture. It is the exported seam the
// composition root adapts into modules/orchestration's RoutinePolicyGate.
//
// It is tenant-scoped by construction (m.data is the least-privilege,
// tenant-parameterized module handle) and READ-ONLY.
func (m *Module) ResolveRoutinePolicy(ctx context.Context, tenant model.TenantID, scope RoutineScope) (EffectiveRoutinePolicy, error) {
	var out EffectiveRoutinePolicy
	if m.data == nil {
		return out, errNoData
	}
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		var rerr error
		out, rerr = resolveRoutinePolicyIn(ctx, sc, scope)
		return rerr
	})
	if err != nil {
		return EffectiveRoutinePolicy{}, err
	}
	return out, nil
}

// resolveRoutinePolicyIn is the resolution itself, against a tenant scope the
// caller has already opened. Two callers share it and MUST keep sharing it: the
// exported seam above (which opens its own view over the tenant-parameterized
// module handle) and the read API's posture handler (which already holds the
// request's PINNED scope, routines.go). A console that described the posture
// through a second implementation of this fold — in the handler, or worse in
// TypeScript — would eventually describe it differently from the way
// orchestration enforces it, and an operator would read "unconstrained" off a
// policy set that denies. One fold, two readers.
func resolveRoutinePolicyIn(ctx context.Context, sc store.Scope, scope RoutineScope) (EffectiveRoutinePolicy, error) {
	var out EffectiveRoutinePolicy
	err := func() error {
		repo, err := sc.Ext(routinePolicyKind)
		if err != nil {
			return err
		}
		all, err := listAll(ctx, repo, eq(colRPEnabled, true))
		if err != nil {
			return err
		}
		// A routine with no workspace of its own belongs to the tenant's DEFAULT
		// workspace — the engine's own back-compat rule for an entity with an
		// unset WorkspaceID (core/store.Scope.DefaultWorkspace). Resolving it
		// here is what keeps a workspace-scoped policy usable at all: only a
		// workspace-CONFINED session carries a workspace, so without this every
		// token-declared routine, every routine declared by a tenant-wide
		// member, and every routine that predates this session would answer
		// "unknown" for the workspace axis and be refused 503 the moment any
		// workspace policy existed.
		defaultWS := ""
		if def, derr := sc.DefaultWorkspace(ctx); derr != nil {
			if !isNotFound(derr) {
				return derr // unreadable, not absent: fail closed
			}
		} else {
			defaultWS = def.ID.String()
		}
		effectiveWS := scope.WorkspaceRef
		if effectiveWS == "" {
			effectiveWS = defaultWS
		}
		// A workspace scope_ref may be authored as the workspace id OR its
		// operator-facing slug; resolve the routine's workspace to both so an
		// operator's natural spelling still matches.
		wsID, wsSlug, werr := resolveWorkspaceRefs(ctx, sc, effectiveWS)
		if werr != nil {
			return werr
		}
		out = composeRoutinePolicies(all, scope, wsID, wsSlug)
		out.EffectiveUserRef = scope.UserRef
		out.EffectiveWorkspaceRef = wsID
		out.DefaultWorkspaceRef = defaultWS
		return nil
	}()
	if err != nil {
		return EffectiveRoutinePolicy{}, err
	}
	return out, nil
}

// resolveWorkspaceRefs maps a workspace id to (id, slug). A ref that is not a
// resolvable workspace — not a UUID at all, or a workspace row that genuinely
// does not exist — yields itself as the id and an empty slug: matching is then
// by literal equality only, never by a fabricated slug.
//
// A read that FAILED is different from a workspace that is absent, and the
// difference is load-bearing. Swallowing a transient read error here would
// return slug="" and silently make every workspace-scoped policy authored with
// the operator-facing SLUG stop matching — no floor, no cap, no blocked
// environment, no approval requirement — with nothing marked indeterminate.
// That is a fail-OPEN, so a genuine error propagates and the caller (which
// already denies closed on any error) refuses.
func resolveWorkspaceRefs(ctx context.Context, sc store.Scope, ref string) (id, slug string, err error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", "", nil
	}
	wid, perr := model.ParseID(ref)
	if perr != nil {
		return ref, "", nil // not an engine id: literal matching only
	}
	ws, gerr := sc.Workspaces().Get(ctx, wid)
	if gerr != nil {
		if isNotFound(gerr) {
			return ref, "", nil // the workspace is genuinely gone
		}
		return "", "", gerr // an unreadable workspace must not read as "no slug"
	}
	return ref, ws.Slug, nil
}

// composeRoutinePolicies is the pure fold — separated from the store read so
// the composition rules are unit-testable without a harness.
func composeRoutinePolicies(all []model.Record, scope RoutineScope, wsID, wsSlug string) EffectiveRoutinePolicy {
	var out EffectiveRoutinePolicy
	blocked := map[string]struct{}{}
	var allowed map[string]struct{} // nil until the first allowlist intersects in

	// Deterministic order so PolicyRefs and Digest are stable across reads.
	recs := append([]model.Record(nil), all...)
	sort.Slice(recs, func(i, j int) bool {
		return recs[i].String(model.ColID) < recs[j].String(model.ColID)
	})

	for _, rec := range recs {
		kind := rec.String(colRPScopeKind)
		ref := strings.TrimSpace(rec.String(colRPScopeRef))
		match, axisUnknown := routinePolicyMatches(kind, ref, scope, wsID, wsSlug)
		if axisUnknown {
			// An enabled policy scopes an axis this routine cannot supply.
			// Reporting "no match" here is exactly the silent bypass this
			// session exists to remove, so the resolution is INDETERMINATE and
			// the consumer denies closed.
			out.Indeterminate, out.IndeterminateAxis = true, kind
			continue
		}
		if !match {
			continue
		}
		out.InForce = true
		out.PolicyRefs = append(out.PolicyRefs, rec.String(model.ColID))

		if floor := rec.Int(colRPMaxCadenceSec); floor > out.MinIntervalSec {
			out.MinIntervalSec = floor // most restrictive floor wins
		}
		if rec.Bool(colRPRequireApproval) {
			out.RequireApproval = true
		}
		if cap := rec.Int(colRPMaxActive); cap > 0 {
			out.ActiveCaps = appendActiveCap(out.ActiveCaps, RoutineActiveCap{
				ScopeKind: kind, ScopeRef: ref, Max: cap,
			})
		}
		if envs := routineListOf(rec, colRPBlockedEnvs); envs.Corrupt {
			// An unreadable blocklist must NOT contribute zero entries — for a
			// UNION control that reads as "this policy blocks nothing", the
			// exact inverse of fail-closed. It is an unreadable policy.
			out.Indeterminate, out.IndeterminateAxis = true, colRPBlockedEnvs
		} else {
			for _, e := range envs.Items {
				blocked[e] = struct{}{}
			}
		}
		crons := routineListOf(rec, colRPAllowedCron)
		if crons.Corrupt {
			out.Indeterminate, out.IndeterminateAxis = true, colRPAllowedCron
		} else if crons.Present {
			out.CronAllowlistInForce = true
			canon := map[string]struct{}{}
			for _, p := range crons.Items {
				canon[cron.Canonical(p)] = struct{}{}
			}
			if allowed == nil {
				allowed = canon
				continue
			}
			for k := range allowed {
				if _, ok := canon[k]; !ok {
					delete(allowed, k) // intersection: both operators must admit it
				}
			}
		}
	}

	out.BlockedEnvs = sortedKeys(blocked)
	if out.CronAllowlistInForce {
		out.CronAllowed = sortedKeys(allowed)
	}
	out.Digest = routinePolicyDigest(out)
	return out
}

// appendActiveCap folds a second cap for the SAME (scope_kind, scope_ref) to
// the smaller value; distinct scopes stay distinct constraints.
func appendActiveCap(caps []RoutineActiveCap, c RoutineActiveCap) []RoutineActiveCap {
	for i := range caps {
		if caps[i].ScopeKind == c.ScopeKind && caps[i].ScopeRef == c.ScopeRef {
			if c.Max < caps[i].Max {
				caps[i].Max = c.Max
			}
			return caps
		}
	}
	return append(caps, c)
}

// routinePolicyMatches reports whether a policy scopes this routine, and
// whether the routine simply cannot answer for that axis (axisUnknown).
func routinePolicyMatches(kind, ref string, scope RoutineScope, wsID, wsSlug string) (match, axisUnknown bool) {
	switch kind {
	case rpScopeTenant:
		return true, false
	case rpScopeWorkspace:
		if wsID == "" && wsSlug == "" {
			return false, true
		}
		return ref != "" && (ref == wsID || ref == wsSlug), false
	case rpScopeUser:
		if scope.UserRef == "" {
			// A routine that is KNOWN to have no owning user is provably
			// outside a policy scoped to a named user — a definite non-match,
			// not an unanswerable axis.
			return false, !scope.UserKnown
		}
		return ref != "" && ref == scope.UserRef, false
	default:
		// An unknown scope_kind cannot be proven inapplicable.
		return false, true
	}
}

// routineListValue is the parsed state of one JSON string-array policy column.
// The three states are deliberately distinct because each control reads them
// differently and collapsing any pair produces a fail-open:
//
//	Present=false            absent/NULL — the documented "any"/"none"
//	Present=true, len 0      an operator-AUTHORED empty list (deny-all for an
//	                         allowlist; blocks nothing for a blocklist)
//	Corrupt=true             the stored value could not be parsed at all
//
// Corrupt is NOT "an authored deny-all". That reading happens to fail closed
// for an allowlist and fails OPEN for a blocklist (an empty union blocks
// nothing), so the caller must treat it as what it is — an unreadable policy —
// and refuse.
type routineListValue struct {
	Present bool
	Corrupt bool
	Items   []string
}

// routineListOf parses a JSON string-array column into its three states.
func routineListOf(rec model.Record, col string) routineListValue {
	v, ok := rec[col]
	if !ok || v == nil {
		return routineListValue{}
	}
	switch val := v.(type) {
	case []any:
		out := make([]string, 0, len(val))
		for _, item := range val {
			s, ok := item.(string)
			if !ok {
				return routineListValue{Corrupt: true} // a non-string element
			}
			out = append(out, s)
		}
		return routineListValue{Present: true, Items: out}
	case []string:
		return routineListValue{Present: true, Items: val}
	case string:
		s := strings.TrimSpace(val)
		if s == "" || s == "null" {
			return routineListValue{}
		}
		var out []string
		if err := json.Unmarshal([]byte(s), &out); err != nil {
			return routineListValue{Corrupt: true}
		}
		return routineListValue{Present: true, Items: out}
	default:
		return routineListValue{Corrupt: true}
	}
}

func sortedKeys(m map[string]struct{}) []string {
	if m == nil {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// routinePolicyDigest fingerprints the COMPOSED decision (not the policy
// bodies), so a denial can cite exactly which posture refused it and a change
// of posture is visible in evidence. Fields are length-prefixed so no value can
// shift a boundary into another (the canonicalHash convention item 1).
func routinePolicyDigest(p EffectiveRoutinePolicy) string {
	parts := []string{
		"floor:" + strconv.FormatInt(p.MinIntervalSec, 10),
		"approval:" + strconv.FormatBool(p.RequireApproval),
		"cronlist:" + strconv.FormatBool(p.CronAllowlistInForce),
	}
	for _, c := range p.CronAllowed {
		parts = append(parts, "cron:"+c)
	}
	for _, e := range p.BlockedEnvs {
		parts = append(parts, "env:"+e)
	}
	for _, c := range p.ActiveCaps {
		parts = append(parts, "cap:"+c.ScopeKind+"/"+c.ScopeRef+"="+strconv.FormatInt(c.Max, 10))
	}
	for _, r := range p.PolicyRefs {
		parts = append(parts, "policy:"+r)
	}
	var b strings.Builder
	for _, s := range parts {
		fmt.Fprintf(&b, "%d:%s", len(s), s)
	}
	return hashHexOf("governance.routine_policy.v1|" + b.String())
}
