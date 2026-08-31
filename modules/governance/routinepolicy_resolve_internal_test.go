// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// the routine-policy composition fold. These are the pure tests; the
// store-backed resolve and the enforcement itself live in the harness tests and
// in modules/orchestration.

// rp builds one enabled routine-policy record.
func rp(id, kind, ref string, mutate func(model.Record)) model.Record {
	rec := model.Record{
		model.ColID:    id,
		colRPScopeKind: kind,
		colRPScopeRef:  ref,
		colRPEnabled:   true,
	}
	if mutate != nil {
		mutate(rec)
	}
	return rec
}

func TestComposeRoutinePoliciesTakesTheMostRestrictiveFloor(t *testing.T) {
	// The LARGER floor is on the record that sorts FIRST, so a plain last-wins
	// fold would answer 300 and this test would catch it. (With the order
	// reversed the assertion passes under either rule and proves nothing.)
	got := composeRoutinePolicies([]model.Record{
		rp("a", rpScopeTenant, "", func(r model.Record) { r[colRPMaxCadenceSec] = int64(3600) }),
		rp("b", rpScopeUser, "u1", func(r model.Record) { r[colRPMaxCadenceSec] = int64(300) }),
	}, RoutineScope{UserRef: "u1"}, "", "")

	if !got.InForce {
		t.Fatal("two matching policies did not put the posture in force")
	}
	if got.MinIntervalSec != 3600 {
		t.Fatalf("floor = %d, want the MAXIMUM 3600 (a narrower policy must not weaken a wider one)", got.MinIntervalSec)
	}
	if len(got.PolicyRefs) != 2 {
		t.Fatalf("policy refs = %v, want both matched policies as evidence", got.PolicyRefs)
	}
}

// A tenant cap and a user cap constrain DIFFERENT populations, so they must
// survive as separate constraints — collapsing them into one number would
// silently apply the wrong cap to the wrong population.
func TestComposeRoutinePoliciesKeepsPerScopeActiveCaps(t *testing.T) {
	got := composeRoutinePolicies([]model.Record{
		rp("a", rpScopeTenant, "", func(r model.Record) { r[colRPMaxActive] = int64(100) }),
		rp("b", rpScopeUser, "u1", func(r model.Record) { r[colRPMaxActive] = int64(2) }),
	}, RoutineScope{UserRef: "u1"}, "", "")

	if len(got.ActiveCaps) != 2 {
		t.Fatalf("active caps = %+v, want one per scope", got.ActiveCaps)
	}
	for _, c := range got.ActiveCaps {
		switch c.ScopeKind {
		case rpScopeTenant:
			if c.Max != 100 {
				t.Errorf("tenant cap = %d, want 100", c.Max)
			}
		case rpScopeUser:
			if c.Max != 2 || c.ScopeRef != "u1" {
				t.Errorf("user cap = %+v, want max 2 for u1", c)
			}
		}
	}
}

// A zero cap is "no cap" (the adjacent cadence control documents zero the same
// way, and create defaults an omitted value to zero) — reading it as "none
// allowed" would make a policy that only sets require_approval forbid every
// routine.
// Two caps on the SAME scope fold to the smaller value; distinct scopes stay
// distinct (that is the other test).
func TestComposeRoutinePoliciesFoldsSameScopeCapsToTheMinimum(t *testing.T) {
	got := composeRoutinePolicies([]model.Record{
		rp("a", rpScopeTenant, "", func(r model.Record) { r[colRPMaxActive] = int64(2) }),
		rp("b", rpScopeTenant, "", func(r model.Record) { r[colRPMaxActive] = int64(10) }),
	}, RoutineScope{}, "", "")
	if len(got.ActiveCaps) != 1 || got.ActiveCaps[0].Max != 2 {
		t.Fatalf("caps = %+v, want a single tenant cap of 2 (the minimum)", got.ActiveCaps)
	}
}

func TestComposeRoutinePoliciesTreatsZeroActiveCapAsUnlimited(t *testing.T) {
	got := composeRoutinePolicies([]model.Record{
		rp("a", rpScopeTenant, "", func(r model.Record) { r[colRPRequireApproval] = true }),
	}, RoutineScope{}, "", "")

	if len(got.ActiveCaps) != 0 {
		t.Fatalf("active caps = %+v, want none (zero means unlimited)", got.ActiveCaps)
	}
	if !got.RequireApproval {
		t.Fatal("require_approval did not survive the fold")
	}
}

// The allowlist is canonicalised on the STORED side too, so an operator who
// authored a re-spaced pattern still matches a normally-spaced request (and a
// caller cannot slip past by re-spacing either).
func TestComposeRoutinePoliciesCanonicalisesStoredPatterns(t *testing.T) {
	got := composeRoutinePolicies([]model.Record{
		rp("a", rpScopeTenant, "", func(r model.Record) { r[colRPAllowedCron] = `["0    *  * * *"]` }),
	}, RoutineScope{}, "", "")
	if len(got.CronAllowed) != 1 || got.CronAllowed[0] != "0 * * * *" {
		t.Fatalf("stored allowlist = %v, want the canonical spelling [\"0 * * * *\"]", got.CronAllowed)
	}
}

func TestComposeRoutinePoliciesIntersectsCronAllowlists(t *testing.T) {
	got := composeRoutinePolicies([]model.Record{
		rp("a", rpScopeTenant, "", func(r model.Record) { r[colRPAllowedCron] = `["0 * * * *","0 2 * * *"]` }),
		rp("b", rpScopeUser, "u1", func(r model.Record) { r[colRPAllowedCron] = `["0 2 * * *","*/5 * * * *"]` }),
	}, RoutineScope{UserRef: "u1"}, "", "")

	if !got.CronAllowlistInForce {
		t.Fatal("allowlist not in force")
	}
	if len(got.CronAllowed) != 1 || got.CronAllowed[0] != "0 2 * * *" {
		t.Fatalf("allowlist = %v, want only the pattern BOTH operators admit", got.CronAllowed)
	}
}

// An operator-authored EMPTY allowlist is a deny-all the operator wrote: in
// force, admitting nothing.
func TestComposeRoutinePoliciesEmptyAllowlistDeniesEveryCron(t *testing.T) {
	got := composeRoutinePolicies([]model.Record{
		rp("a", rpScopeTenant, "", func(r model.Record) { r[colRPAllowedCron] = `[]` }),
	}, RoutineScope{}, "", "")
	if !got.CronAllowlistInForce {
		t.Fatal("an authored empty allowlist must be IN FORCE, not unconstrained")
	}
	if len(got.CronAllowed) != 0 {
		t.Fatalf("admitted %v, want nothing", got.CronAllowed)
	}
	if got.Indeterminate {
		t.Fatal("an authored empty list is readable — it is a decision, not an unreadable policy")
	}
}

// A CORRUPT column is neither "unconstrained" nor "an authored deny-all": it is
// an unreadable policy, and both controls must say so. Reading it as a deny-all
// happens to fail closed for the allowlist and fails OPEN for the blocklist,
// where an empty union blocks nothing.
func TestComposeRoutinePoliciesCorruptListIsIndeterminate(t *testing.T) {
	for _, col := range []string{colRPAllowedCron, colRPBlockedEnvs} {
		for name, stored := range map[string]any{
			"object":      `{"not":"an array"}`,
			"garbage":     `[[[`,
			"wrong-type":  12345,
			"mixed-array": []any{"ok", 7},
		} {
			got := composeRoutinePolicies([]model.Record{
				rp("a", rpScopeTenant, "", func(r model.Record) { r[col] = stored }),
			}, RoutineScope{}, "", "")
			if !got.Indeterminate || got.IndeterminateAxis != col {
				t.Errorf("%s/%s: got indeterminate=%t axis=%q, want an unreadable %s",
					col, name, got.Indeterminate, got.IndeterminateAxis, col)
			}
		}
	}
}

// The blocklist inversion specifically: a corrupt blocklist must never yield an
// EMPTY union, which would mean "this policy blocks nothing".
func TestComposeRoutinePoliciesCorruptBlocklistNeverBlocksNothing(t *testing.T) {
	got := composeRoutinePolicies([]model.Record{
		rp("a", rpScopeTenant, "", func(r model.Record) { r[colRPBlockedEnvs] = `{"oops":true}` }),
	}, RoutineScope{}, "", "")
	if !got.Indeterminate {
		t.Fatalf("corrupt blocklist folded to BlockedEnvs=%v with no indeterminate flag — that admits every environment", got.BlockedEnvs)
	}
}

// A JSON null (and an absent column) is the documented "any" — that one IS
// unconstrained, and must stay distinguishable from [].
func TestComposeRoutinePoliciesNullAllowlistIsUnconstrained(t *testing.T) {
	for _, stored := range []any{nil, "null", ""} {
		got := composeRoutinePolicies([]model.Record{
			rp("a", rpScopeTenant, "", func(r model.Record) { r[colRPAllowedCron] = stored }),
		}, RoutineScope{}, "", "")
		if got.CronAllowlistInForce {
			t.Fatalf("stored %v put an allowlist in force; null/absent means any", stored)
		}
	}
}

func TestComposeRoutinePoliciesUnionsBlockedEnvironments(t *testing.T) {
	got := composeRoutinePolicies([]model.Record{
		rp("a", rpScopeTenant, "", func(r model.Record) { r[colRPBlockedEnvs] = `["prod"]` }),
		rp("b", rpScopeUser, "u1", func(r model.Record) { r[colRPBlockedEnvs] = `["staging"]` }),
	}, RoutineScope{UserRef: "u1"}, "", "")

	if len(got.BlockedEnvs) != 2 || got.BlockedEnvs[0] != "prod" || got.BlockedEnvs[1] != "staging" {
		t.Fatalf("blocked = %v, want the UNION [prod staging]", got.BlockedEnvs)
	}
}

// A policy scoped to an axis the routine cannot answer for is INDETERMINATE —
// reporting "no match" would be the silent bypass this seam exists to close.
func TestComposeRoutinePoliciesIndeterminateOnUnknownAxis(t *testing.T) {
	got := composeRoutinePolicies([]model.Record{
		rp("a", rpScopeWorkspace, "ws-1", func(r model.Record) { r[colRPMaxCadenceSec] = int64(3600) }),
	}, RoutineScope{UserRef: "u1"}, "", "") // no workspace known

	if !got.Indeterminate || got.IndeterminateAxis != rpScopeWorkspace {
		t.Fatalf("got %+v, want indeterminate on the workspace axis", got)
	}
}

// A policy for ANOTHER user/workspace must not apply.
func TestComposeRoutinePoliciesIgnoresForeignScopes(t *testing.T) {
	got := composeRoutinePolicies([]model.Record{
		rp("a", rpScopeUser, "someone-else", func(r model.Record) { r[colRPMaxCadenceSec] = int64(3600) }),
	}, RoutineScope{UserRef: "u1"}, "", "")

	if got.InForce || got.MinIntervalSec != 0 {
		t.Fatalf("got %+v, want no policy in force for a different user", got)
	}
	if got.Indeterminate {
		t.Fatal("a KNOWN axis that simply does not match must not be indeterminate")
	}
}

// A workspace scope_ref may be authored as the id or the operator-facing slug.
func TestComposeRoutinePoliciesMatchesWorkspaceBySlug(t *testing.T) {
	for _, ref := range []string{"ws-id-1", "engineering"} {
		got := composeRoutinePolicies([]model.Record{
			rp("a", rpScopeWorkspace, ref, func(r model.Record) { r[colRPMaxCadenceSec] = int64(600) }),
		}, RoutineScope{WorkspaceRef: "ws-id-1"}, "ws-id-1", "engineering")
		if !got.InForce || got.MinIntervalSec != 600 {
			t.Fatalf("scope_ref %q did not match: %+v", ref, got)
		}
	}
}

// The digest is evidence, so it must be stable across read order and must move
// when the posture moves.
func TestComposeRoutinePolicyDigestIsStableAndSensitive(t *testing.T) {
	a := rp("a", rpScopeTenant, "", func(r model.Record) { r[colRPMaxCadenceSec] = int64(300) })
	b := rp("b", rpScopeUser, "u1", func(r model.Record) { r[colRPRequireApproval] = true })

	one := composeRoutinePolicies([]model.Record{a, b}, RoutineScope{UserRef: "u1"}, "", "")
	two := composeRoutinePolicies([]model.Record{b, a}, RoutineScope{UserRef: "u1"}, "", "")
	if one.Digest != two.Digest {
		t.Fatal("digest is read-order dependent")
	}
	if one.Digest == "" {
		t.Fatal("empty digest")
	}
	tighter := composeRoutinePolicies([]model.Record{
		a, b, rp("c", rpScopeTenant, "", func(r model.Record) { r[colRPMaxCadenceSec] = int64(7200) }),
	}, RoutineScope{UserRef: "u1"}, "", "")
	if tighter.Digest == one.Digest {
		t.Fatal("digest did not change when the composed floor changed")
	}
}

// Round-2 MF-2 — a routine declared by a TOKEN has no owning user, and
// that is an ANSWER: it is provably outside a policy scoped to a named user.
// Treating it as an unanswerable axis made one user-scoped policy 503 every
// token-declared routine in the tenant — an operator self-DoS from a
// governance action.
func TestComposeRoutinePoliciesTokenDeclaredRoutineIsADefiniteNonMatch(t *testing.T) {
	policies := []model.Record{
		rp("a", rpScopeUser, "alice", func(r model.Record) { r[colRPMaxCadenceSec] = int64(3600) }),
	}

	// Knows it has no user (token-declared): a definite non-match.
	got := composeRoutinePolicies(policies, RoutineScope{UserKnown: true}, "", "")
	if got.Indeterminate {
		t.Fatalf("a token-declared routine went indeterminate on the user axis: %+v", got)
	}
	if got.InForce {
		t.Fatalf("a policy scoped to alice applied to a routine with no owning user: %+v", got)
	}

	// Cannot answer at all (unreadable record): still indeterminate.
	unknown := composeRoutinePolicies(policies, RoutineScope{}, "", "")
	if !unknown.Indeterminate || unknown.IndeterminateAxis != rpScopeUser {
		t.Fatalf("an unanswerable user axis must stay indeterminate: %+v", unknown)
	}

	// The named owner still matches.
	alice := composeRoutinePolicies(policies, RoutineScope{UserKnown: true, UserRef: "alice"}, "", "")
	if !alice.InForce || alice.MinIntervalSec != 3600 {
		t.Fatalf("the policy did not apply to its own subject: %+v", alice)
	}
}

// the read DTO must expose the fourth state. A column the engine cannot
// parse is projected as an EMPTY array (so the read surface never paints it as
// unconstrained), which makes it indistinguishable from an operator-authored
// deny-all unless the projection says so out loud.
//
// This is not cosmetic. A console that faithfully re-sends what it was shown
// would turn the projected [] into a real empty list, silently REPAIRING the
// column and dropping the indeterminate the resolver raises above — an
// unreadable blocklist would go from "refuse every fire" to "block nothing".
func TestToRoutinePolicyDTODistinguishesUnreadableFromAuthoredEmpty(t *testing.T) {
	unreadable := toRoutinePolicyDTO(
		rp("a", rpScopeTenant, "", func(r model.Record) {
			r[colRPAllowedCron] = `{"not":"an array"}`
			r[colRPBlockedEnvs] = `[[[`
		}),
	)
	if unreadable.AllowedCron == nil || len(*unreadable.AllowedCron) != 0 {
		t.Fatalf("unreadable allowlist projected as %v, want an empty array (never null/any)",
			unreadable.AllowedCron)
	}
	if !unreadable.AllowedCronUnreadable || !unreadable.BlockedEnvsUnreadable {
		t.Fatalf("unreadable columns not marked: cron=%t envs=%t",
			unreadable.AllowedCronUnreadable, unreadable.BlockedEnvsUnreadable)
	}

	authored := toRoutinePolicyDTO(
		rp("b", rpScopeTenant, "", func(r model.Record) {
			r[colRPAllowedCron] = `[]`
			r[colRPBlockedEnvs] = `[]`
		}),
	)
	if authored.AllowedCron == nil || len(*authored.AllowedCron) != 0 {
		t.Fatalf("authored empty allowlist projected as %v, want an empty array", authored.AllowedCron)
	}
	if authored.AllowedCronUnreadable || authored.BlockedEnvsUnreadable {
		t.Fatalf("an authored empty list was marked unreadable: cron=%t envs=%t",
			authored.AllowedCronUnreadable, authored.BlockedEnvsUnreadable)
	}

	// And the documented "any": null stays null, and is not unreadable either.
	any := toRoutinePolicyDTO(rp("c", rpScopeTenant, "", func(r model.Record) {
		r[colRPAllowedCron] = nil
	}))
	if any.AllowedCron != nil || any.AllowedCronUnreadable {
		t.Fatalf("null allowlist projected as %v (unreadable=%t), want null and not unreadable",
			any.AllowedCron, any.AllowedCronUnreadable)
	}
}
