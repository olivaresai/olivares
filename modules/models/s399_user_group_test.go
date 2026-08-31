// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

import (
	"strings"
	"testing"
)

// `user_group` as a model-access subject: a DIRECTORY group (principal.GroupsIn)
// governs model use, mirroring agent_group but decided from the server-authenticated
// principal. These are pure-decision tests over a hand-built maContext (the same technique
// s257_test.go uses), so they exercise decide()/previewVerdict() and the token
// indeterminate-membership guard directly.

// ugCtx builds a model-access context for the user_group tests, deriving the user_group
// flags exactly as modelAccessContext does. userID="u1", role="editor" — a role NOT named by
// any test grant, so the principal is governed ONLY through its directory groups.
func ugCtx(userGroups []string, tokenUnresolvable bool, grants ...modelAccessGrant) *maContext {
	hasUG := false
	for _, g := range grants {
		if g.subjectKind == subjectUserGroup {
			hasUG = true
		}
	}
	return &maContext{
		grants: grants, groups: map[string]modelGroupDef{}, userID: "u1", role: "editor",
		userGroups: userGroups, hasUserGroupGrant: hasUG, userGroupsUnresolvable: tokenUnresolvable,
	}
}

// A session IN the named directory group is confined to that group's allows.
func TestUserGroupAllowConfinesMember(t *testing.T) {
	c := ugCtx([]string{"grp-ds"}, false, allow(subjectUserGroup, "grp-ds", "claude-opus-4-8"))
	if v := c.decide("claude-opus-4-8", ""); !v.Allowed {
		t.Errorf("a member of grp-ds must be allowed the group's allowed model: %s", v.Reason)
	}
	if v := c.decide("claude-sonnet-4-6", ""); v.Allowed {
		t.Error("a member named by a user_group allow is confined to its allows (sonnet must deny)")
	}
}

// A session NOT in the named group — and named by no other allow — is unrestricted (you
// restrict CERTAIN subjects; an unnamed one is free).
func TestUserGroupAllowLeavesNonMemberUnrestricted(t *testing.T) {
	c := ugCtx([]string{"grp-other"}, false, allow(subjectUserGroup, "grp-ds", "claude-opus-4-8"))
	if v := c.decide("claude-sonnet-4-6", ""); !v.Allowed {
		t.Errorf("a non-member named by no allow must be unrestricted: %s", v.Reason)
	}
}

// A user_group forbid SUBTRACTS for a member, but only the forbidden model.
func TestUserGroupForbidSubtractsForMember(t *testing.T) {
	c := ugCtx([]string{"grp-ds"}, false, forbid(subjectUserGroup, "grp-ds", "claude-opus-4-8"))
	if v := c.decide("claude-opus-4-8", ""); v.Allowed {
		t.Error("a user_group forbid must deny the forbidden model for a member")
	}
	if v := c.decide("claude-sonnet-4-6", ""); !v.Allowed {
		t.Errorf("a forbid subtracts only its model; an otherwise-unnamed member keeps others: %s", v.Reason)
	}
}

// THE bypass guard: a credential that cannot enumerate its directory groups (a token) is
// deny-closed for EVERY model once any user_group grant exists — it cannot prove it is in
// none of the confining groups, and must never launder through the "unnamed ⇒ unrestricted"
// path. This is the mirror of the unbindable agent-group guard.
func TestUserGroupTokenIsDenyClosed(t *testing.T) {
	c := ugCtx(nil, true, allow(subjectUserGroup, "grp-ds", "claude-opus-4-8"))
	if v := c.decide("claude-opus-4-8", ""); v.Allowed {
		t.Error("a token (indeterminate directory-group membership) must be deny-closed even for the group's allowed model")
	}
	if v := c.decide("claude-sonnet-4-6", ""); v.Allowed {
		t.Error("a token with an indeterminate user_group membership must be deny-closed for all models")
	}
	// And a forbid triggers the same guard (hasUserGroupGrant covers allow AND forbid).
	cf := ugCtx(nil, true, forbid(subjectUserGroup, "grp-ds", "claude-opus-4-8"))
	if v := cf.decide("claude-sonnet-4-6", ""); v.Allowed {
		t.Error("a token is deny-closed whenever ANY user_group grant (incl. a forbid) exists")
	}
}

// A SESSION in zero directory groups is authoritatively-empty (not indeterminate): named by
// no allow, it stays unrestricted. This is the crucial difference from a token — the guard
// must key on the credential kind, never on an empty group set alone.
func TestUserGroupSessionInZeroGroupsIsNotDenyClosed(t *testing.T) {
	c := ugCtx(nil, false, allow(subjectUserGroup, "grp-ds", "claude-opus-4-8"))
	if v := c.decide("claude-sonnet-4-6", ""); !v.Allowed {
		t.Errorf("a session in zero groups is authoritatively unnamed ⇒ unrestricted, not deny-closed: %s", v.Reason)
	}
	if v := c.decide("claude-opus-4-8", ""); !v.Allowed {
		t.Errorf("a session named by no allow is unrestricted for every model: %s", v.Reason)
	}
}

// C03-26: the unrestricted path NAMES the user-group hole. It must stay an ALLOW
// and must never call group-mapping. The signal is the reason.
func TestC0326UnnamedPathSignalsUserGroupGrantsWithoutGating(t *testing.T) {
	withUG := ugCtx(nil, false, allow(subjectUserGroup, "grp-ds", "claude-opus-4-8"))
	v := withUG.decide("claude-sonnet-4-6", "")
	if !v.Allowed {
		t.Fatalf("C03-26 must not turn the unnamed path into a deny: %s", v.Reason)
	}
	if v.Reason != reasonUnnamedPrincipalUserGroupGrantsPresent {
		t.Fatalf("signal missing: got %q", v.Reason)
	}
	if !strings.Contains(v.Reason, "group_mapping_is_not_a_gate") {
		t.Fatal("the reason must state that group-mapping is not a gate")
	}

	withoutUG := ugCtx(nil, false, allow(subjectRole, "owner", "claude-opus-4-8"))
	v2 := withoutUG.decide("claude-sonnet-4-6", "")
	if !v2.Allowed {
		t.Fatalf("unnamed without user_group grants must stay allowed: %s", v2.Reason)
	}
	if v2.Reason != reasonUnnamedPrincipal {
		t.Fatalf("without user_group grants the old reason must stay: got %q", v2.Reason)
	}
	if strings.Contains(v2.Reason, "group_mapping_is_not_a_gate") {
		t.Fatal("must not emit the user-group signal when no user_group grant exists")
	}
}

// A token is UNAFFECTED by the guard when no user_group grant exists (pure back-compat): the
// guard is additive, biting only once a directory-group grant is authored.
func TestUserGroupTokenUnaffectedWithoutUserGroupGrants(t *testing.T) {
	c := ugCtx(nil, true, allow(subjectRole, "owner", "claude-opus-4-8")) // role owner ≠ u1's role
	if v := c.decide("claude-sonnet-4-6", ""); !v.Allowed {
		t.Errorf("with no user_group grants a token is governed exactly as before (unnamed ⇒ allowed): %s", v.Reason)
	}
}

// Preview: a tenant-wide user_group forbid on a member's group is a DEFINITE forbid (directory
// groups travel on the principal, so preview can decide them) — the model is dropped. A
// workspace-scoped one is deferred (kept).
func TestUserGroupPreviewDefiniteForbidForMember(t *testing.T) {
	c := ugCtx([]string{"grp-ds"}, false, forbid(subjectUserGroup, "grp-ds", "claude-opus-4-8"))
	if v := c.previewVerdict("claude-opus-4-8"); v.Allowed {
		t.Error("a tenant-wide user_group forbid on the member's group is a definite forbid at preview")
	}
	// A workspace-scoped forbid bites only in some worlds — deferred, so KEEP at preview.
	ws := forbid(subjectUserGroup, "grp-ds", "claude-opus-4-8")
	ws.workspace = "payments"
	cw := ugCtx([]string{"grp-ds"}, false, ws)
	if v := cw.previewVerdict("claude-opus-4-8"); !v.Allowed {
		t.Error("a workspace-scoped user_group forbid must be deferred to execute (kept at preview)")
	}
}

// Preview honesty for a token: its directory-group membership is indeterminate, so a
// user_group forbid is NOT definite (kept at preview) and a user_group allow "could apply"
// (kept) — the execute path is what fails the token closed.
func TestUserGroupPreviewTokenKeepsOptimistically(t *testing.T) {
	cf := ugCtx(nil, true, forbid(subjectUserGroup, "grp-ds", "claude-opus-4-8"))
	if v := cf.previewVerdict("claude-opus-4-8"); !v.Allowed {
		t.Error("a token's user_group forbid is indeterminate at preview — must KEEP (execute denies)")
	}
	ca := ugCtx(nil, true, allow(subjectUserGroup, "grp-ds", "claude-opus-4-8"))
	if v := ca.previewVerdict("claude-opus-4-8"); !v.Allowed {
		t.Error("a token's user_group allow could apply — preview must KEEP it (honest, execute enforces)")
	}
}

// Preview allow-list confinement for a member: governed by a user_group allow, only the
// allowed model is kept; others are dropped.
func TestUserGroupPreviewConfinesMember(t *testing.T) {
	c := ugCtx([]string{"grp-ds"}, false, allow(subjectUserGroup, "grp-ds", "claude-opus-4-8"))
	if v := c.previewVerdict("claude-opus-4-8"); !v.Allowed {
		t.Errorf("the group's allowed model must be kept at preview: %s", v.Reason)
	}
	if v := c.previewVerdict("claude-sonnet-4-6"); v.Allowed {
		t.Error("a member governed by a user_group allow must have non-allowed models dropped at preview")
	}
}

// Core algebra: a user_group FORBID subtracts even when a user/role ALLOW would grant
// (forbid-overrides-allow ACROSS subject kinds — the invariant a partition/order regression
// would silently break).
func TestUserGroupForbidOverridesCrossKindAllow(t *testing.T) {
	// u1 (in grp-ds) has a user allow on opus AND a user_group forbid on opus → DENY.
	c := ugCtx([]string{"grp-ds"}, false,
		allow(subjectUser, "u1", "claude-opus-4-8"),
		forbid(subjectUserGroup, "grp-ds", "claude-opus-4-8"),
	)
	if v := c.decide("claude-opus-4-8", ""); v.Allowed {
		t.Error("a user_group forbid must override a user allow on the same model")
	}
	// The reverse: a user_group allow + a role forbid → the role forbid wins.
	c2 := ugCtx([]string{"grp-ds"}, false,
		allow(subjectUserGroup, "grp-ds", "claude-opus-4-8"),
		forbid(subjectRole, "editor", "claude-opus-4-8"), // ugCtx role = editor
	)
	if v := c2.decide("claude-opus-4-8", ""); v.Allowed {
		t.Error("a role forbid must override a user_group allow on the same model")
	}
}

// The honesty invariant of pinned in ONE test: the SAME token maContext KEEPS a
// user_group-governed model at preview but DENIES it at execute.
func TestUserGroupTokenPreviewKeepsButExecuteDenies(t *testing.T) {
	c := ugCtx(nil, true, allow(subjectUserGroup, "grp-ds", "claude-opus-4-8"))
	if v := c.previewVerdict("claude-opus-4-8"); !v.Allowed {
		t.Error("preview must KEEP the model for a token (optimistic, never hides a usable model)")
	}
	if v := c.decide("claude-opus-4-8", ""); v.Allowed {
		t.Error("execute must DENY the same token (deny-closed) — the honest preview/execute split")
	}
}

// The authoring DTO accepts user_group and rejects an unknown subject kind with the updated
// message.
func TestModelAccessValidateUserGroupSubject(t *testing.T) {
	d := modelAccessDTO{SubjectKind: "user_group", SubjectRef: "grp-ds", TargetKind: "model", TargetRef: "claude-opus-4-8"}
	if msg := d.validate(); msg != "" {
		t.Errorf("user_group must be a valid subject_kind, got: %q", msg)
	}
	bad := modelAccessDTO{SubjectKind: "team", SubjectRef: "x", TargetKind: "model", TargetRef: "claude-opus-4-8"}
	if msg := bad.validate(); msg == "" {
		t.Error("an unknown subject_kind must be rejected")
	}
}
