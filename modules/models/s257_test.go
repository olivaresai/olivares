// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

import (
	"net/http/httptest"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
)

// ctxWith builds a model-access context for the pure-decision tests, deriving the
// agent-group flags the same way modelAccessContext does (so decide()'s indeterminate
// guards are exercised faithfully). userID="u1", role="admin".
func ctxWith(actor ActorScope, sessionAsserted bool, grants ...modelAccessGrant) *maContext {
	hasAG, hasAGForbid := false, false
	for _, g := range grants {
		if g.subjectKind == subjectAgentGroup {
			hasAG = true
			if g.isForbid() {
				hasAGForbid = true
			}
		}
	}
	return &maContext{
		grants: grants, groups: map[string]modelGroupDef{}, userID: "u1", role: "admin",
		actor: actor, sessionAsserted: sessionAsserted, hasAgentGroupGrant: hasAG, hasAgentGroupForbid: hasAGForbid,
	}
}

func allow(subjectKind, subjectRef, targetRef string) modelAccessGrant {
	return modelAccessGrant{subjectKind: subjectKind, subjectRef: subjectRef, targetKind: targetModel, targetRef: targetRef, effect: effectAllow}
}

func forbid(subjectKind, subjectRef, targetRef string) modelAccessGrant {
	return modelAccessGrant{subjectKind: subjectKind, subjectRef: subjectRef, targetKind: targetModel, targetRef: targetRef, effect: effectForbid}
}

// ---- forbid-overrides-allow (decide) ----------------------------------------

// TestForbidOverridesAllow: a forbid that names the principal and matches the model
// SUBTRACTS access even when an allow would otherwise grant it.
func TestForbidOverridesAllow(t *testing.T) {
	c := ctxWith(ActorScope{}, false,
		allow(subjectRole, "admin", "claude-opus-4-8"),
		forbid(subjectRole, "admin", "claude-opus-4-8"),
	)
	if v := c.decide("claude-opus-4-8", ""); v.Allowed {
		t.Errorf("forbid must override allow on the same model, got allow: %s", v.Reason)
	}
}

// TestForbidOnlySubtracts: a principal named ONLY by a forbid is denied the forbidden
// model but UNRESTRICTED for others (a forbid does not turn into an allow-list).
func TestForbidOnlySubtracts(t *testing.T) {
	c := ctxWith(ActorScope{}, false, forbid(subjectRole, "admin", "claude-opus-4-8"))
	if v := c.decide("claude-opus-4-8", ""); v.Allowed {
		t.Errorf("the forbidden model must be denied, got allow")
	}
	if v := c.decide("claude-sonnet-4-6", ""); !v.Allowed {
		t.Errorf("a model not named by any allow OR forbid must stay unrestricted, got deny: %s", v.Reason)
	}
}

// TestForbidNonMatchingModelLeavesAllow: a forbid on model X does not affect an allow on
// model Y for the same subject.
func TestForbidNonMatchingModelLeavesAllow(t *testing.T) {
	c := ctxWith(ActorScope{}, false,
		allow(subjectRole, "admin", "claude-sonnet-4-6"),
		forbid(subjectRole, "admin", "claude-opus-4-8"),
	)
	if v := c.decide("claude-sonnet-4-6", ""); !v.Allowed {
		t.Errorf("the allowed model must remain allowed when the forbid targets a different model, got deny: %s", v.Reason)
	}
	if v := c.decide("claude-opus-4-8", ""); v.Allowed {
		t.Errorf("the forbidden model must be denied (and is also outside the allow-list), got allow")
	}
}

// TestForbidWorkspaceScoped: a workspace-scoped forbid only bites in that workspace.
func TestForbidWorkspaceScoped(t *testing.T) {
	g := forbid(subjectRole, "admin", "claude-opus-4-8")
	g.workspace = "payments"
	inPay := ctxWith(ActorScope{Workspace: "payments"}, true, g)
	if v := inPay.decide("claude-opus-4-8", ""); v.Allowed {
		t.Errorf("workspace forbid must bite in its workspace, got allow")
	}
	inResearch := ctxWith(ActorScope{Workspace: "research"}, true, g)
	if v := inResearch.decide("claude-opus-4-8", ""); !v.Allowed {
		t.Errorf("workspace forbid must NOT bite in a different workspace, got deny: %s", v.Reason)
	}
}

// TestForbidSurfaceScoped: a surface-scoped forbid bites only on its surface; a different
// surface is unaffected; and — crucially — an UNKNOWN surface (an execute caller that
// omits `surface`) DEFERS the surface-scoped forbid rather than over-blocking everywhere
// (symmetric with the allow side and previewVerdict/§6). An all-surface forbid
// still bites at an unknown surface.
func TestForbidSurfaceScoped(t *testing.T) {
	g := forbid(subjectRole, "admin", "claude-opus-4-8")
	g.surfaces = []string{"direct"}
	c := ctxWith(ActorScope{}, false, g)
	if v := c.decide("claude-opus-4-8", "direct"); v.Allowed {
		t.Errorf("surface forbid must bite on its surface, got allow")
	}
	if v := c.decide("claude-opus-4-8", "bedrock-mantle"); !v.Allowed {
		t.Errorf("surface forbid must NOT bite on a different surface, got deny: %s", v.Reason)
	}
	// Unknown surface (omitted at execute): the surface-scoped forbid is DEFERRED, not
	// escalated to all surfaces — must NOT bite here (it enforces in-band / when declared).
	if v := c.decide("claude-opus-4-8", ""); !v.Allowed {
		t.Errorf("a surface-scoped forbid must be DEFERRED at unknown surface, not over-block, got deny: %s", v.Reason)
	}
	// But an ALL-surface forbid (no constraint) bites even at an unknown surface.
	all := forbid(subjectRole, "admin", "claude-opus-4-8")
	if v := ctxWith(ActorScope{}, false, all).decide("claude-opus-4-8", ""); v.Allowed {
		t.Errorf("an all-surface forbid must bite at an unknown surface, got allow")
	}
}

// TestAgentGroupForbidResolved: an agent-group forbid bites when the acting agent is in
// the group and not otherwise.
func TestAgentGroupForbidResolved(t *testing.T) {
	g := forbid(subjectAgentGroup, "bots", "claude-opus-4-8")
	inGroup := ctxWith(ActorScope{Workspace: "default", AgentGroups: []string{"bots"}}, true,
		allow(subjectRole, "admin", "claude-opus-4-8"), g)
	if v := inGroup.decide("claude-opus-4-8", ""); v.Allowed {
		t.Errorf("agent-group forbid must override the allow for an in-group actor, got allow")
	}
	outGroup := ctxWith(ActorScope{Workspace: "default", AgentGroups: []string{"other"}}, true,
		allow(subjectRole, "admin", "claude-opus-4-8"), g)
	if v := outGroup.decide("claude-opus-4-8", ""); !v.Allowed {
		t.Errorf("agent-group forbid must NOT bite an out-of-group actor (allow stands), got deny: %s", v.Reason)
	}
}

// TestAgentGroupForbidIndeterminateDenyClosed: an asserted-but-unresolved session with an
// agent-group forbid present is deny-closed EVEN when a user/role allow would match — a
// subtraction we cannot evaluate must never be silently dropped.
func TestAgentGroupForbidIndeterminateDenyClosed(t *testing.T) {
	c := ctxWith(ActorScope{ /* unresolved: empty workspace */ }, true,
		allow(subjectRole, "admin", "claude-opus-4-8"),
		forbid(subjectAgentGroup, "bots", "claude-opus-4-8"),
	)
	if v := c.decide("claude-opus-4-8", ""); v.Allowed {
		t.Errorf("an indeterminate agent-group forbid must deny-closed despite a matching allow, got allow")
	}
	// No session asserted (a direct user/role call) ⇒ the allow carries it (agent-group
	// forbids govern acting agents, not direct user calls).
	c2 := ctxWith(ActorScope{}, false,
		allow(subjectRole, "admin", "claude-opus-4-8"),
		forbid(subjectAgentGroup, "bots", "claude-opus-4-8"),
	)
	if v := c2.decide("claude-opus-4-8", ""); !v.Allowed {
		t.Errorf("a direct user/role call (no session) must not be blocked by an agent-group forbid, got deny: %s", v.Reason)
	}
}

// ---- store-backed forbid on the execute route -------------------------------

// TestModelAccessForbidRoute: a forbid drops the forbidden model from the resolved chain
// (never served nor tried as a fallback), even though an allow names the broader family.
func TestModelAccessForbidRoute(t *testing.T) {
	m, st, tenant := newModelAccessModule(t, fakeActorScope{workspace: "default"})
	seedModelGroups(t, st, tenant, modelGroupDTO{Name: "frontier", Members: []string{"claude-opus-4-8", "claude-sonnet-4-6"}})
	seedModelAccess(t, st, tenant,
		modelAccessDTO{SubjectKind: subjectRole, SubjectRef: "admin", TargetKind: targetModelGroup, TargetRef: "frontier", Effect: effectAllow},
		modelAccessDTO{SubjectKind: subjectRole, SubjectRef: "admin", TargetKind: targetModel, TargetRef: "claude-opus-4-8", Effect: effectForbid},
	)
	r := httptest.NewRequest("POST", "/x", nil)
	dec := chainOf("claude-opus-4-8", "claude-sonnet-4-6")
	if status, denied := m.modelAccessDeniesRoute(r, mcFor(tenant, adminRole(tenant)), &dec, "sess-1", ""); denied || status != 0 {
		t.Fatalf("partial forbid: want (0,false), got (%d,%v)", status, denied)
	}
	if dec.Primary == nil || dec.Primary.ModelRef != "claude-sonnet-4-6" || len(dec.Chain) != 1 {
		t.Errorf("forbidden opus must be dropped and sonnet promoted, got %+v", dec)
	}
}

// ---- /resolve preview (no acting session) -----------------------------------

// TestPreviewVerdictForbid: a tenant-wide, all-surface forbid on the principal's role
// drops the model from the preview; a workspace- or surface-scoped forbid is DEFERRED
// (kept — it bites only in some worlds).
func TestPreviewVerdictForbid(t *testing.T) {
	// Tenant-wide all-surface forbid ⇒ dropped.
	c := ctxWith(ActorScope{}, false, forbid(subjectRole, "admin", "claude-opus-4-8"))
	if v := c.previewVerdict("claude-opus-4-8"); v.Allowed {
		t.Errorf("a tenant-wide forbid must drop the model from the preview, got allow")
	}
	// Workspace-scoped forbid ⇒ deferred (kept).
	gw := forbid(subjectRole, "admin", "claude-opus-4-8")
	gw.workspace = "payments"
	if v := ctxWith(ActorScope{}, false, gw).previewVerdict("claude-opus-4-8"); !v.Allowed {
		t.Errorf("a workspace-scoped forbid must be DEFERRED at preview (kept), got deny: %s", v.Reason)
	}
	// Surface-scoped forbid ⇒ deferred (kept).
	gs := forbid(subjectRole, "admin", "claude-opus-4-8")
	gs.surfaces = []string{"direct"}
	if v := ctxWith(ActorScope{}, false, gs).previewVerdict("claude-opus-4-8"); !v.Allowed {
		t.Errorf("a surface-scoped forbid must be DEFERRED at preview (kept), got deny: %s", v.Reason)
	}
	// Agent-group forbid ⇒ not decidable without a session ⇒ deferred (kept).
	if v := ctxWith(ActorScope{}, false, forbid(subjectAgentGroup, "bots", "claude-opus-4-8")).previewVerdict("claude-opus-4-8"); !v.Allowed {
		t.Errorf("an agent-group forbid must be DEFERRED at preview (kept), got deny: %s", v.Reason)
	}
}

// TestPreviewVerdictAllowList: a principal confined by a user/role allow-list keeps the
// allowed model and drops a non-allowed one; an agent-group allow keeps a model
// optimistically (the actor MIGHT be in the group at execute).
func TestPreviewVerdictAllowList(t *testing.T) {
	c := ctxWith(ActorScope{}, false, allow(subjectRole, "admin", "claude-sonnet-4-6"))
	if v := c.previewVerdict("claude-sonnet-4-6"); !v.Allowed {
		t.Errorf("the allow-listed model must survive the preview, got deny: %s", v.Reason)
	}
	if v := c.previewVerdict("claude-opus-4-8"); v.Allowed {
		t.Errorf("a non-allow-listed model must be dropped for a confined principal, got allow")
	}
	// Governed by a user/role allow, but an agent-group allow covers opus ⇒ keep opus
	// (the principal's acting agent might be in that group at execute).
	c2 := ctxWith(ActorScope{}, false,
		allow(subjectRole, "admin", "claude-sonnet-4-6"),
		allow(subjectAgentGroup, "bots", "claude-opus-4-8"),
	)
	if v := c2.previewVerdict("claude-opus-4-8"); !v.Allowed {
		t.Errorf("an agent-group allow must keep the model optimistically at preview, got deny: %s", v.Reason)
	}
}

// TestPreviewVerdictUngoverned: a principal named by no user/role allow is unrestricted
// at preview (even if workspace-scoped allows exist — those are deferred).
func TestPreviewVerdictUngoverned(t *testing.T) {
	g := allow(subjectRole, "viewer", "claude-sonnet-4-6") // names a DIFFERENT role
	if v := ctxWith(ActorScope{}, false, g).previewVerdict("claude-opus-4-8"); !v.Allowed {
		t.Errorf("a principal named by no user/role allow must be unrestricted at preview, got deny: %s", v.Reason)
	}
}

// TestModelAccessPreviewDeniesRoute: the store-backed /resolve filter drops a tenant-wide
// forbidden model and promotes the survivor; an all-forbidden chain returns 403.
func TestModelAccessPreviewDeniesRoute(t *testing.T) {
	m, st, tenant := newModelAccessModule(t, fakeActorScope{})
	seedModelAccess(t, st, tenant,
		modelAccessDTO{SubjectKind: subjectRole, SubjectRef: "admin", TargetKind: targetModel, TargetRef: "claude-opus-4-8", Effect: effectForbid},
	)
	r := httptest.NewRequest("POST", "/x", nil)
	// Partial: opus forbidden tenant-wide, sonnet not ⇒ sonnet survives, promoted.
	dec := chainOf("claude-opus-4-8", "claude-sonnet-4-6")
	if status, denied := m.modelAccessPreviewDeniesRoute(r, mcFor(tenant, adminRole(tenant)), &dec); denied || status != 0 {
		t.Fatalf("partial preview filter: want (0,false), got (%d,%v)", status, denied)
	}
	if dec.Primary == nil || dec.Primary.ModelRef != "claude-sonnet-4-6" || len(dec.Chain) != 1 {
		t.Errorf("tenant-wide forbidden opus must be dropped from the preview, got %+v", dec)
	}
	// All forbidden ⇒ 403 with no usable target.
	dec2 := chainOf("claude-opus-4-8")
	status, denied := m.modelAccessPreviewDeniesRoute(r, mcFor(tenant, adminRole(tenant)), &dec2)
	if !denied || status != 403 {
		t.Fatalf("all-forbidden preview: want (403,true), got (%d,%v)", status, denied)
	}
	if dec2.Resolved || dec2.Primary != nil || len(dec2.Chain) != 0 {
		t.Errorf("a fully-denied preview must carry no usable target, got %+v", dec2)
	}
}

// TestModelAccessPreviewUngovernedNoop: a tenant with no grants (or a superadmin) is not
// governed; the preview filter is a no-op.
func TestModelAccessPreviewUngovernedNoop(t *testing.T) {
	m, _, tenant := newModelAccessModule(t, fakeActorScope{})
	r := httptest.NewRequest("POST", "/x", nil)
	dec := chainOf("claude-opus-4-8", "claude-sonnet-4-6")
	if status, denied := m.modelAccessPreviewDeniesRoute(r, mcFor(tenant, adminRole(tenant)), &dec); denied || status != 0 {
		t.Fatalf("ungoverned preview: want (0,false), got (%d,%v)", status, denied)
	}
	if len(dec.Chain) != 2 {
		t.Errorf("chain must be untouched when ungoverned, got %+v", dec)
	}
	// Superadmin is never governed.
	dec = chainOf("claude-opus-4-8")
	if status, denied := m.modelAccessPreviewDeniesRoute(r, mcFor(tenant, auth.Principal{Superadmin: true}), &dec); denied || status != 0 {
		t.Fatalf("superadmin preview must be a no-op: want (0,false), got (%d,%v)", status, denied)
	}
}
