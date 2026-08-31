// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// fakeActorScope is an injected actor-scope resolver: it returns a fixed
// workspace/agent-group set, or an error to exercise the deny-closed posture.
type fakeActorScope struct {
	workspace string
	groups    []string
	err       error
}

func (f fakeActorScope) Resolve(context.Context, model.TenantID, auth.Principal, string) (ActorScope, error) {
	if f.err != nil {
		return ActorScope{}, f.err
	}
	return ActorScope{Workspace: f.workspace, AgentGroups: f.groups}, nil
}

func seedModelGroups(t *testing.T, st store.Store, tenant model.TenantID, groups ...modelGroupDTO) {
	t.Helper()
	ctx := context.Background()
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(modelGroupKind)
		if err != nil {
			return err
		}
		for _, g := range groups {
			if _, err := repo.Create(ctx, g.toRecord()); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed model-groups: %v", err)
	}
}

func seedModelAccess(t *testing.T, st store.Store, tenant model.TenantID, grants ...modelAccessDTO) {
	t.Helper()
	ctx := context.Background()
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(modelAccessKind)
		if err != nil {
			return err
		}
		for _, g := range grants {
			if _, err := repo.Create(ctx, g.toRecord()); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed model-access: %v", err)
	}
}

func mcFor(tenant model.TenantID, p auth.Principal) api.ModuleContext {
	return api.ModuleContext{Tenant: tenant, Principal: p}
}

// adminRole is a non-superadmin principal holding role "admin" in tenant.
func adminRole(tenant model.TenantID) auth.Principal {
	return auth.ScopedPrincipal(model.ID("u-admin"), "admin", tenant, "admin")
}

// ---- pure matchers ----------------------------------------------------------

func TestSubjectMatches(t *testing.T) {
	g := func(kind, ref string) modelAccessGrant {
		return modelAccessGrant{subjectKind: kind, subjectRef: ref}
	}
	// subjectMatches(g, userID, role, userGroups, actorGroups)
	if !subjectMatches(g(subjectUser, "u1"), "u1", "admin", nil, nil) {
		t.Error("user subject must match its user id")
	}
	if subjectMatches(g(subjectUser, "u1"), "u2", "admin", nil, nil) {
		t.Error("user subject must not match a different user")
	}
	if subjectMatches(g(subjectUser, "u1"), "", "admin", nil, nil) {
		t.Error("empty user id must never match a user subject")
	}
	if !subjectMatches(g(subjectRole, "admin"), "u1", "admin", nil, nil) {
		t.Error("role subject must match the principal's role")
	}
	if subjectMatches(g(subjectRole, "owner"), "u1", "admin", nil, nil) {
		t.Error("role subject must not match a different role")
	}
	// user_group matches the principal's DIRECTORY groups (4th arg), independent of the
	// agent-group axis (5th arg).
	if !subjectMatches(g(subjectUserGroup, "grp-ds"), "u1", "admin", []string{"grp-x", "grp-ds"}, nil) {
		t.Error("user_group subject must match a directory group the principal is in")
	}
	if subjectMatches(g(subjectUserGroup, "grp-ds"), "u1", "admin", []string{"grp-x"}, nil) {
		t.Error("user_group subject must not match when the principal is not in the group")
	}
	if subjectMatches(g(subjectUserGroup, "grp-ds"), "u1", "admin", nil, []string{"grp-ds"}) {
		t.Error("user_group must match against DIRECTORY groups, never the agent-group axis")
	}
	if !subjectMatches(g(subjectAgentGroup, "bots"), "u1", "admin", nil, []string{"x", "bots"}) {
		t.Error("agent-group subject must match a group the actor is in")
	}
	if subjectMatches(g(subjectAgentGroup, "bots"), "u1", "admin", []string{"bots"}, []string{"x"}) {
		t.Error("agent-group must match against the agent-group axis, never directory groups")
	}
}

func TestTargetMatchesModelAndPrefix(t *testing.T) {
	g := modelAccessGrant{targetKind: targetModel, targetRef: "claude-opus-4-8"}
	if !targetMatches(g, "claude-opus-4-8", nil) {
		t.Error("exact model ref must match")
	}
	if !targetMatches(g, "claude-opus-4-8-20260201", nil) {
		t.Error("dated suffix must match by prefix")
	}
	if targetMatches(g, "claude-sonnet-4-6", nil) {
		t.Error("a different model must not match")
	}
}

func TestGroupContainsHybrid(t *testing.T) {
	groups := map[string]modelGroupDef{
		"frontier": {members: []string{"claude-opus-4-8"}, families: []string{"claude-sonnet"}},
		"glass":    {tiers: []string{accessTierGlasswing}},
	}
	g := func(name string) modelAccessGrant {
		return modelAccessGrant{targetKind: targetModelGroup, targetRef: name}
	}
	// explicit member ref (exact + prefix).
	if !targetMatches(g("frontier"), "claude-opus-4-8-20260201", groups) {
		t.Error("explicit member ref (prefix) must be in the group")
	}
	// family selector resolves via the reference catalog (claude-sonnet-4-6 → family claude-sonnet).
	if !targetMatches(g("frontier"), "claude-sonnet-4-6", groups) {
		t.Error("family selector must include a model of that family")
	}
	if targetMatches(g("frontier"), "claude-haiku-4-5", groups) {
		t.Error("a model neither listed nor selected must not be in the group")
	}
	// tier selector: a Glasswing model (mythos) has AccessTier=glasswing.
	if !targetMatches(g("glass"), "claude-mythos-5", groups) {
		t.Error("tier selector must include a model of that access tier")
	}
	if targetMatches(g("glass"), "claude-opus-4-8", groups) {
		t.Error("a GA model must not match a glasswing tier selector")
	}
	// a grant naming a non-existent group never matches.
	if targetMatches(g("ghost"), "claude-opus-4-8", groups) {
		t.Error("a target naming an unknown group must not match")
	}
}

func TestGroupContainsGLMFamilyReference(t *testing.T) {
	ref, ok := lookupReference("glm-4.6-20260708")
	if !ok {
		t.Fatal("glm-4.6 dated ref must resolve through the reference catalog")
	}
	if ref.Family != "glm-4.6" {
		t.Fatalf("glm-4.6 dated ref resolved to family %q, want glm-4.6", ref.Family)
	}

	if !groupContains(modelGroupDef{families: []string{"glm-4.6"}}, "glm-4.6-20260708") {
		t.Error("model-group family glm-4.6 must include a dated GLM 4.6 ref")
	}
	if groupContains(modelGroupDef{families: []string{"glm-4.6"}}, "glm-4.5") {
		t.Error("model-group family glm-4.6 must not include a different GLM family")
	}
	if !modelRefMatches("glm-4.6", "glm-4.6-20260708") {
		t.Error("explicit model grant glm-4.6 must match a dated GLM 4.6 ref by component prefix")
	}
}

func TestSurfaceMatches(t *testing.T) {
	apiOnly := modelAccessGrant{surfaces: []string{"direct"}}
	if !surfaceMatches(apiOnly, "direct") {
		t.Error("an allowed surface must match")
	}
	if surfaceMatches(apiOnly, "bedrock-mantle") {
		t.Error("a surface outside an api-only grant must be denied")
	}
	if !surfaceMatches(apiOnly, "") {
		t.Error("an unknown surface (selection-time) is deferred to the in-band caller (allowed here)")
	}
	if !surfaceMatches(modelAccessGrant{}, "foundry") {
		t.Error("no surface constraint must permit any surface")
	}
}

func TestWorkspaceMatches(t *testing.T) {
	if !workspaceMatches(modelAccessGrant{workspace: ""}, "anything") {
		t.Error("an empty grant workspace is tenant-wide")
	}
	if !workspaceMatches(modelAccessGrant{workspace: "payments"}, "payments") {
		t.Error("a workspace-scoped grant must match the actor's workspace")
	}
	if workspaceMatches(modelAccessGrant{workspace: "payments"}, "research") {
		t.Error("a workspace-scoped grant must not match a different workspace")
	}
	if workspaceMatches(modelAccessGrant{workspace: "payments"}, "") {
		t.Error("an unresolved actor must not match a workspace-scoped grant")
	}
}

// ---- subject-scoped decision (maContext.decide) -----------------------------

func TestDecideSubjectScoped(t *testing.T) {
	ctxOf := func(grants ...modelAccessGrant) *maContext {
		return &maContext{grants: grants, groups: map[string]modelGroupDef{}, userID: "u1", role: "admin"}
	}
	// A principal NAMED by no grant is unrestricted (subject-scoped, back-compat).
	c := ctxOf(modelAccessGrant{subjectKind: subjectRole, subjectRef: "viewer", targetKind: targetModel, targetRef: "claude-opus-4-8"})
	if v := c.decide("claude-opus-4-8", ""); !v.Allowed {
		t.Errorf("a principal named by no grant must be unrestricted, got deny: %s", v.Reason)
	}
	// A principal NAMED by a grant is confined to it: a granted model is allowed...
	c = ctxOf(modelAccessGrant{subjectKind: subjectRole, subjectRef: "admin", targetKind: targetModel, targetRef: "claude-opus-4-8"})
	if v := c.decide("claude-opus-4-8", ""); !v.Allowed {
		t.Errorf("the granted model must be allowed, got deny: %s", v.Reason)
	}
	// ...and a non-granted model is DENIED (deny-closed once governed).
	if v := c.decide("claude-sonnet-4-6", ""); v.Allowed {
		t.Error("a governed principal must be denied a model it has no grant for")
	}
}

// ---- modelAccessDeniesRoute (store-backed enforcement) ----------------------

func newModelAccessModule(t *testing.T, actor ActorScopeResolver) (*Module, store.Store, model.TenantID) {
	t.Helper()
	m, st, tenant := newMod(t)
	if actor != nil {
		m.actorScope = actor
	}
	return m, st, tenant
}

// TestModelAccessUngovernedIsNoop: a tenant with no grants does not govern model use.
func TestModelAccessUngovernedIsNoop(t *testing.T) {
	m, _, tenant := newModelAccessModule(t, fakeActorScope{})
	dec := chainOf("claude-opus-4-8", "claude-sonnet-4-6")
	r := httptest.NewRequest("POST", "/x", nil)
	if status, denied := m.modelAccessDeniesRoute(r, mcFor(tenant, adminRole(tenant)), &dec, "sess-1", ""); denied || status != 0 {
		t.Fatalf("ungoverned tenant: want (0,false), got (%d,%v)", status, denied)
	}
	if len(dec.Chain) != 2 {
		t.Errorf("chain must be untouched, got %+v", dec)
	}
}

// TestModelAccessSuperadminBypass: superadmin is never confined by model-access grants.
func TestModelAccessSuperadminBypass(t *testing.T) {
	m, st, tenant := newModelAccessModule(t, fakeActorScope{})
	seedModelAccess(t, st, tenant, modelAccessDTO{
		SubjectKind: subjectRole, SubjectRef: "admin", TargetKind: targetModel, TargetRef: "claude-sonnet-4-6",
	})
	dec := chainOf("claude-opus-4-8")
	r := httptest.NewRequest("POST", "/x", nil)
	if status, denied := m.modelAccessDeniesRoute(r, mcFor(tenant, auth.Principal{Superadmin: true}), &dec, "sess-1", ""); denied || status != 0 {
		t.Fatalf("superadmin must bypass: want (0,false), got (%d,%v)", status, denied)
	}
}

// TestModelAccessDeniesUngrantedModel: a governed principal is denied a model it has no
// grant for; the chain is mutated to a 403 with no usable target.
func TestModelAccessDeniesUngrantedModel(t *testing.T) {
	m, st, tenant := newModelAccessModule(t, fakeActorScope{})
	seedModelAccess(t, st, tenant, modelAccessDTO{
		SubjectKind: subjectRole, SubjectRef: "admin", TargetKind: targetModel, TargetRef: "claude-sonnet-4-6",
	})
	dec := chainOf("claude-opus-4-8")
	r := httptest.NewRequest("POST", "/x", nil)
	status, denied := m.modelAccessDeniesRoute(r, mcFor(tenant, adminRole(tenant)), &dec, "sess-1", "")
	if !denied || status != 403 {
		t.Fatalf("ungranted model: want (403,true), got (%d,%v)", status, denied)
	}
	if dec.Resolved || dec.Primary != nil || len(dec.Chain) != 0 {
		t.Errorf("denied decision must carry no usable target, got %+v", dec)
	}
}

// TestModelAccessDropsAndPromotes: a governed principal granted only one candidate keeps
// it (the ungranted primary is dropped, the granted one promoted — never a fallback).
func TestModelAccessDropsAndPromotes(t *testing.T) {
	m, st, tenant := newModelAccessModule(t, fakeActorScope{})
	seedModelAccess(t, st, tenant, modelAccessDTO{
		SubjectKind: subjectRole, SubjectRef: "admin", TargetKind: targetModel, TargetRef: "claude-sonnet-4-6",
	})
	dec := chainOf("claude-opus-4-8", "claude-sonnet-4-6")
	r := httptest.NewRequest("POST", "/x", nil)
	if status, denied := m.modelAccessDeniesRoute(r, mcFor(tenant, adminRole(tenant)), &dec, "sess-1", ""); denied || status != 0 {
		t.Fatalf("partial filter: want (0,false), got (%d,%v)", status, denied)
	}
	if dec.Primary == nil || dec.Primary.ModelRef != "claude-sonnet-4-6" || len(dec.Chain) != 1 {
		t.Errorf("ungranted opus must be dropped and sonnet promoted, got %+v", dec)
	}
}

// TestModelAccessModelGroupGrants: a model-group grant concedes the whole set.
func TestModelAccessModelGroupGrants(t *testing.T) {
	m, st, tenant := newModelAccessModule(t, fakeActorScope{})
	seedModelGroups(t, st, tenant, modelGroupDTO{Name: "frontier", Members: []string{"claude-opus-4-8"}, Families: []string{"claude-sonnet"}})
	seedModelAccess(t, st, tenant, modelAccessDTO{
		SubjectKind: subjectRole, SubjectRef: "admin", TargetKind: targetModelGroup, TargetRef: "frontier",
	})
	r := httptest.NewRequest("POST", "/x", nil)
	// opus (explicit member) and sonnet (family selector) are both in the group.
	dec := chainOf("claude-opus-4-8", "claude-sonnet-4-6")
	if status, denied := m.modelAccessDeniesRoute(r, mcFor(tenant, adminRole(tenant)), &dec, "sess-1", ""); denied || status != 0 {
		t.Fatalf("group members must be granted: want (0,false), got (%d,%v)", status, denied)
	}
	if len(dec.Chain) != 2 {
		t.Errorf("both group members must survive, got %+v", dec)
	}
	// haiku is not in the group ⇒ denied.
	dec = chainOf("claude-haiku-4-5")
	if status, _ := m.modelAccessDeniesRoute(r, mcFor(tenant, adminRole(tenant)), &dec, "sess-1", ""); status != 403 {
		t.Errorf("a model outside the group must be denied, got status %d", status)
	}
}

// TestModelAccessSurfaceConstraint: an api-only grant denies a Bedrock request and
// permits a direct one; an unknown surface is deferred (allowed at selection).
func TestModelAccessSurfaceConstraint(t *testing.T) {
	m, st, tenant := newModelAccessModule(t, fakeActorScope{})
	seedModelAccess(t, st, tenant, modelAccessDTO{
		SubjectKind: subjectRole, SubjectRef: "admin", TargetKind: targetModel, TargetRef: "claude-opus-4-8",
		Surfaces: []string{"direct"},
	})
	r := httptest.NewRequest("POST", "/x", nil)
	for _, tc := range []struct {
		surface string
		denied  bool
	}{
		{"direct", false},
		{"bedrock-mantle", true},
		{"", false}, // unknown at selection → deferred to the in-band proxy
	} {
		dec := chainOf("claude-opus-4-8")
		status, denied := m.modelAccessDeniesRoute(r, mcFor(tenant, adminRole(tenant)), &dec, "sess-1", tc.surface)
		if denied != tc.denied {
			t.Errorf("surface %q: denied=%v, want %v (status %d)", tc.surface, denied, tc.denied, status)
		}
	}
}

// boundAgent is a non-superadmin admin principal carrying an AUTHENTICATED agent identity
// (the agent-OBO path). Agent-group model-access grants govern such a principal
// because its NHI binding is proven server-side — unlike a raw token, whose caller-supplied
// session_ref must never launder it into an agent-group subject (F-01).
func boundAgent(tenant model.TenantID, agentExternalID string) auth.Principal {
	return adminRole(tenant).WithAgentIdentity(agentExternalID)
}

// TestModelAccessWorkspaceAndAgentGroup: workspace- and agent-group-scoped grants match
// against the resolved actor scope of an AUTHENTICATED agent (F-01: agent-group grants
// require a real NHI binding, not a caller-supplied session_ref).
func TestModelAccessWorkspaceAndAgentGroup(t *testing.T) {
	m, st, tenant := newModelAccessModule(t, fakeActorScope{workspace: "payments", groups: []string{"bots"}})
	seedModelAccess(t, st, tenant,
		// workspace-scoped: only in "payments".
		modelAccessDTO{SubjectKind: subjectRole, SubjectRef: "admin", TargetKind: targetModel, TargetRef: "claude-opus-4-8", WorkspaceRef: "payments"},
		// agent-group subject: the acting agent's group "bots".
		modelAccessDTO{SubjectKind: subjectAgentGroup, SubjectRef: "bots", TargetKind: targetModel, TargetRef: "claude-sonnet-4-6"},
	)
	r := httptest.NewRequest("POST", "/x", nil)
	dec := chainOf("claude-opus-4-8", "claude-sonnet-4-6")
	if status, denied := m.modelAccessDeniesRoute(r, mcFor(tenant, boundAgent(tenant, "acting-agent")), &dec, "sess-1", ""); denied || status != 0 {
		t.Fatalf("in-workspace + in-group grants must allow: got (%d,%v)", status, denied)
	}
	if len(dec.Chain) != 2 {
		t.Errorf("both grants must authorize their model, got %+v", dec)
	}

	// A DIFFERENT workspace must not satisfy the workspace-scoped opus grant.
	m2, st2, tenant2 := newModelAccessModule(t, fakeActorScope{workspace: "research", groups: nil})
	seedModelAccess(t, st2, tenant2, modelAccessDTO{
		SubjectKind: subjectRole, SubjectRef: "admin", TargetKind: targetModel, TargetRef: "claude-opus-4-8", WorkspaceRef: "payments",
	})
	dec2 := chainOf("claude-opus-4-8")
	if status, _ := m2.modelAccessDeniesRoute(r, mcFor(tenant2, adminRole(tenant2)), &dec2, "sess-1", ""); status != 403 {
		t.Errorf("a workspace-scoped grant must not authorize from another workspace, status %d", status)
	}
}

// TestModelAccessDenyClosedOnActorError: an actor-scope resolution error denies the whole
// chain (an unreadable governance state must never authorize a model).
func TestModelAccessDenyClosedOnActorError(t *testing.T) {
	m, st, tenant := newModelAccessModule(t, fakeActorScope{err: errors.New("scope unreadable")})
	seedModelAccess(t, st, tenant, modelAccessDTO{
		SubjectKind: subjectRole, SubjectRef: "admin", TargetKind: targetModel, TargetRef: "claude-opus-4-8",
	})
	dec := chainOf("claude-opus-4-8")
	r := httptest.NewRequest("POST", "/x", nil)
	status, denied := m.modelAccessDeniesRoute(r, mcFor(tenant, adminRole(tenant)), &dec, "sess-1", "")
	if !denied || status != 403 {
		t.Fatalf("actor-scope error must be deny-closed (403,true), got (%d,%v)", status, denied)
	}
}

// TestModelRefMatchesBoundary: the prefix match is component-boundary-aware — it covers a
// dated suffix but does NOT over-grant a sibling family or a shorter ref.
func TestModelRefMatchesBoundary(t *testing.T) {
	cases := []struct {
		grant, model string
		want         bool
	}{
		{"claude-opus-4-8", "claude-opus-4-8", true},          // exact
		{"claude-opus-4-8", "claude-opus-4-8-20260201", true}, // dated suffix (boundary "-")
		{"claude-opus-4-8", "claude-opus-4-80", false},        // sibling, no boundary ⇒ no over-grant
		{"claude-opus-4", "claude-opus-4-8", true},            // deliberate short prefix at boundary
		{"claude-opus", "claude-opus-4-8", true},              // family-ish prefix at boundary
		{"claude-opus-4-8", "claude-sonnet-4-6", false},       // different model
	}
	for _, c := range cases {
		if got := modelRefMatches(c.grant, c.model); got != c.want {
			t.Errorf("modelRefMatches(%q, %q) = %v, want %v", c.grant, c.model, got, c.want)
		}
	}
}

// TestModelAccessAgentGroupUnresolvedDenyClosed: the agent-group SUBJECT dimension fails
// CLOSED when the caller ASSERTS a session that does not resolve (it must not flip to the
// "unrestricted" open path), but a request that asserts NO session is a direct call not
// governed by agent-group grants (documented residual), and a resolved actor outside
// the group is unrestricted.
func TestModelAccessAgentGroupUnresolvedDenyClosed(t *testing.T) {
	grant := modelAccessDTO{SubjectKind: subjectAgentGroup, SubjectRef: "bots", TargetKind: targetModel, TargetRef: "claude-opus-4-8"}
	r := httptest.NewRequest("POST", "/x", nil)

	// (a) a BOUND agent whose session is unresolvable (empty actor scope) ⇒ deny-closed:
	// we cannot prove the authenticated agent is in none of the confining groups. (F-01: a
	// raw token with no binding is denied earlier by the unbindable-token guard — see the
	// laundering regression and the F-01 borrow repro.)
	m, st, tenant := newModelAccessModule(t, fakeActorScope{}) // unresolved: empty workspace
	seedModelAccess(t, st, tenant, grant)
	dec := chainOf("claude-opus-4-8")
	if status, denied := m.modelAccessDeniesRoute(r, mcFor(tenant, boundAgent(tenant, "acting-agent")), &dec, "sess-unknown", ""); !denied || status != 403 {
		t.Fatalf("asserted-but-unresolved bound actor + agent-group grant: want (403,true), got (%d,%v)", status, denied)
	}

	// (b) A HUMAN session with no acting agent remains a direct call; agent-group grants
	// do not govern it. Raw API tokens are covered by the laundering regression below.
	dec = chainOf("claude-opus-4-8")
	human := adminRole(tenant)
	human.Kind = auth.KindUser
	if status, denied := m.modelAccessDeniesRoute(r, mcFor(tenant, human), &dec, "", ""); denied || status != 0 {
		t.Fatalf("no session asserted: want (0,false) (not governed by agent-group), got (%d,%v)", status, denied)
	}

	// (c) a BOUND agent whose resolved actor is OUTSIDE the named group ⇒ unrestricted
	// (subject-scoped). Binding proves the agent's groups, so "named by no grant" is safe.
	m2, st2, tenant2 := newModelAccessModule(t, fakeActorScope{workspace: "payments", groups: []string{"other"}})
	seedModelAccess(t, st2, tenant2, grant)
	dec = chainOf("claude-opus-4-8")
	if status, denied := m2.modelAccessDeniesRoute(r, mcFor(tenant2, boundAgent(tenant2, "acting-agent")), &dec, "sess-1", ""); denied || status != 0 {
		t.Fatalf("resolved bound actor outside the group: want (0,false) (unrestricted), got (%d,%v)", status, denied)
	}
}

// TestModelAccessRawTokenCannotLaunderAgentGroupPolicy proves that an API token without
// an authenticated NHI binding cannot fall through the subject-scoped "unnamed means
// unrestricted" path while agent-group governance exists. A human session remains a
// direct user/role call; only the unbound programmatic credential is indeterminate.
func TestModelAccessRawTokenCannotLaunderAgentGroupPolicy(t *testing.T) {
	grant := modelAccessDTO{SubjectKind: subjectAgentGroup, SubjectRef: "bots", TargetKind: targetModel, TargetRef: "claude-opus-4-8"}
	m, st, tenant := newModelAccessModule(t, fakeActorScope{})
	seedModelAccess(t, st, tenant, grant)

	raw := adminRole(tenant) // ScopedPrincipal is an API-token principal with no AgentIdentity.
	verdict, err := m.EvaluateModelAccess(context.Background(), tenant, raw, "", "", "claude-opus-4-8", "direct")
	if err != nil {
		t.Fatalf("EvaluateModelAccess(raw token): %v", err)
	}
	if verdict.Allowed {
		t.Fatal("an unbound raw token must not bypass agent-group model-access governance")
	}

	human := adminRole(tenant)
	human.Kind = auth.KindUser
	verdict, err = m.EvaluateModelAccess(context.Background(), tenant, human, "", "", "claude-opus-4-8", "direct")
	if err != nil {
		t.Fatalf("EvaluateModelAccess(human session): %v", err)
	}
	if !verdict.Allowed {
		t.Fatalf("a human direct call must remain outside agent-group governance: %s", verdict.Reason)
	}
}

// TestModelAccessAdminTokenCannotBorrowCallerSessionScope reproduces F-01: an
// admin API token (therefore authorized for models:routing:admin) has no
// authenticated AgentIdentity, but caller-supplied session_ref currently lets it
// borrow another agent's production workspace and agent-group model grant.
func TestModelAccessAdminTokenCannotBorrowCallerSessionScope(t *testing.T) {
	const modelRef = "claude-opus-4-8"
	m, st, tenant := newModelAccessModule(t, fakeActorScope{
		workspace: "production",
		groups:    []string{"production-agents"},
	})
	seedModelAccess(t, st, tenant, modelAccessDTO{
		SubjectKind:  subjectAgentGroup,
		SubjectRef:   "production-agents",
		TargetKind:   targetModel,
		TargetRef:    modelRef,
		WorkspaceRef: "production",
	})

	principal := adminRole(tenant)
	if !auth.RoleGrants(auth.RoleAdmin, auth.Permission("models:routing:admin")) {
		t.Fatal("precondition: admin role must hold models:routing:admin")
	}
	if principal.AgentIdentity != "" {
		t.Fatalf("precondition: raw admin token must be unbound, got AgentIdentity %q", principal.AgentIdentity)
	}

	verdict, err := m.EvaluateModelAccess(
		context.Background(), tenant, principal, "other-agent-session", "anthropic", modelRef, "direct",
	)
	if err != nil {
		t.Fatalf("EvaluateModelAccess: %v", err)
	}
	if verdict.Allowed {
		t.Fatal("caller-supplied session_ref must not borrow another agent's workspace/group model grant")
	}
}

// TestDecideMultipleRelevantGrantsOR: several grants naming the same subject compose by OR
// — a workspace-tenant-wide grant authorizes anywhere while a workspace-scoped grant for a
// different model binds only inside its workspace, within ONE decide context.
func TestDecideMultipleRelevantGrantsOR(t *testing.T) {
	c := &maContext{
		grants: []modelAccessGrant{
			{subjectKind: subjectRole, subjectRef: "admin", targetKind: targetModel, targetRef: "claude-opus-4-8"},                          // tenant-wide
			{subjectKind: subjectRole, subjectRef: "admin", targetKind: targetModel, targetRef: "claude-sonnet-4-6", workspace: "payments"}, // scoped
		},
		groups: map[string]modelGroupDef{}, role: "admin",
		actor: ActorScope{Workspace: "research"},
	}
	// In "research": the tenant-wide opus grant authorizes; the payments-scoped sonnet does not.
	if v := c.decide("claude-opus-4-8", ""); !v.Allowed {
		t.Error("tenant-wide grant must authorize opus in any workspace")
	}
	if v := c.decide("claude-sonnet-4-6", ""); v.Allowed {
		t.Error("a payments-scoped grant must NOT authorize sonnet from research")
	}
	// In "payments": both authorize.
	c.actor.Workspace = "payments"
	if v := c.decide("claude-sonnet-4-6", ""); !v.Allowed {
		t.Error("the payments-scoped grant must authorize sonnet in payments")
	}
}

// TestModelAccessDenyClosedOnStoreError: a store-read failure (not just an actor-resolve
// failure) is deny-closed — EvaluateModelAccess surfaces the error for the caller to deny.
func TestModelAccessDenyClosedOnStoreError(t *testing.T) {
	m, st, tenant := newModelAccessModule(t, fakeActorScope{})
	st.Close() // force every subsequent View to fail
	if _, err := m.EvaluateModelAccess(context.Background(), tenant, adminRole(tenant), "sess-1", "anthropic", "claude-opus-4-8", ""); err == nil {
		t.Fatal("a store-read error must be returned (deny-closed), got nil")
	}
}

// TestEvaluateModelAccessInBandSeam exercises the reusable, identity-parameterized seam an
// In-line proxy consults in-band (with the real surface and the resolved identity).
func TestEvaluateModelAccessInBandSeam(t *testing.T) {
	m, st, tenant := newModelAccessModule(t, fakeActorScope{})
	seedModelAccess(t, st, tenant, modelAccessDTO{
		SubjectKind: subjectRole, SubjectRef: "admin", TargetKind: targetModel, TargetRef: "claude-opus-4-8",
		Surfaces: []string{"direct"},
	})
	ctx := context.Background()
	p := adminRole(tenant)
	// In-band, the proxy knows the REAL surface: a Bedrock call is denied, a direct one allowed.
	if v, err := m.EvaluateModelAccess(ctx, tenant, p, "sess-1", "anthropic", "claude-opus-4-8", "bedrock-mantle"); err != nil || v.Allowed {
		t.Errorf("in-band Bedrock call must be denied for an api-only grant, got allowed=%v err=%v", v.Allowed, err)
	}
	if v, err := m.EvaluateModelAccess(ctx, tenant, p, "sess-1", "anthropic", "claude-opus-4-8", "direct"); err != nil || !v.Allowed {
		t.Errorf("in-band direct call must be allowed, got allowed=%v err=%v reason=%q", v.Allowed, err, v.Reason)
	}
	// A model the principal is not granted is denied in-band too.
	if v, _ := m.EvaluateModelAccess(ctx, tenant, p, "sess-1", "anthropic", "claude-sonnet-4-6", "direct"); v.Allowed {
		t.Error("in-band call for an ungranted model must be denied")
	}
	// Superadmin bypasses in-band as well.
	if v, _ := m.EvaluateModelAccess(ctx, tenant, auth.Principal{Superadmin: true}, "sess-1", "anthropic", "claude-sonnet-4-6", "bedrock-mantle"); !v.Allowed {
		t.Error("superadmin must bypass in-band")
	}
}
