// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The session-cockpit Cedar fixture, run against the REAL scoped-grant engine
// (docs/contracts/COCKPIT-02-authz.md; fixture in testdata/cockpit).
//
// WHY THIS BATTERY EXISTS AND WHAT IT IS FOR. The v3 architecture writes its example
// policies as `resource is Session` / `resource is ShellTarget` and
// `principal in AgentGroup::"terminal-operators"`. NEITHER shape matches this engine,
// and the way they fail is the reason a fixture is worth more than a review:
//
//   - the resource is ALWAYS Resource::"<id>" and its kind travels as an ATTRIBUTE
//     (grants.go resourceUID / baseResourceAttrs; cedar.go says so outright);
//   - a PRINCIPAL is `in` User::, Role:: and Group:: — a directory group — and NEVER
//     `in` an AgentGroup::, which groups agent RESOURCES (buildPrincipalEntity).
//
// A condition Cedar cannot resolve makes the rule ERROR and Cedar SKIPS it. So a
// department `forbid` written the v3's way would not fail loudly: it would confine
// nobody, silently. That is exactly the failure mode baseResourceAttrs already guards
// against by keeping kind/sensitivity unconditionally present.
//
// So the fixture is the TRANSLATED form, and this battery is the proof that the
// translation says what the design meant.

const cockpitFixtureDir = "testdata/cockpit"

// loadCockpitPolicy reads one fixture file and substitutes the group-id placeholders.
// The placeholders exist because this engine references directory groups by ID, not by
// slug: an authored policy really does carry the id, and pretending otherwise in a
// fixture would make it compile here and fail in an operator's tenant.
func loadCockpitPolicy(t *testing.T, name string, subs map[string]string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(cockpitFixtureDir, name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	src := string(b)
	for k, v := range subs {
		src = strings.ReplaceAll(src, k, v)
	}
	if strings.Contains(src, "<<") {
		t.Fatalf("fixture %s still carries an unsubstituted placeholder — the battery and the "+
			"fixture disagree about which placeholders exist:\n%s", name, src)
	}
	return src
}

// cockpitEnv is the world the fixture is evaluated against: two workspaces, two
// agent-groups, agents in each, and one session per agent.
type cockpitEnv struct {
	h        *harness
	admin    string
	tenant   model.TenantID
	finance  model.ID // session run by an agent in AgentGroup "finance-ops"
	outside  model.ID // session run by an agent in AgentGroup "other-ops"
	orphan   model.ID // session with NO agent at all
	deptGrp  model.ID // directory group the confined principal belongs to
	opsGrp   model.ID // directory group the terminal operators belong to
	confined auth.Principal
	operator auth.Principal
	plain    auth.Principal
}

func newCockpitEnv(t *testing.T) *cockpitEnv {
	t.Helper()
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "cockpit-fixture")

	ws := h.createWorkspace(tenant, "sessions-ws")
	finAgent := h.createAgentIn(tenant, "fin-bot", ws)
	othAgent := h.createAgentIn(tenant, "oth-bot", ws)
	h.addAgentToGroup(tenant, finAgent.ID, "finance-ops", ws)
	h.addAgentToGroup(tenant, othAgent.ID, "other-ops", ws)

	env := &cockpitEnv{h: h, admin: admin, tenant: tenant}
	env.finance = createCockpitSession(t, h, tenant, finAgent.ID, ws)
	env.outside = createCockpitSession(t, h, tenant, othAgent.ID, ws)
	env.orphan = createCockpitSession(t, h, tenant, model.ID(""), ws)

	// Two directory groups, so the principal side of the fixture is real membership
	// and not a role shortcut.
	confinedUID, confinedTok := h.roleUser(admin, tenant, "confined@cockpit.io", auth.RoleEditor)
	operatorUID, operatorTok := h.roleUser(admin, tenant, "operator@cockpit.io", auth.RoleAdmin)
	_, plainTok := h.roleUser(admin, tenant, "plain@cockpit.io", auth.RoleViewer)

	env.deptGrp = createCockpitGroup(t, h, tenant, "Finance dept", model.ID(confinedUID))
	env.opsGrp = createCockpitGroup(t, h, tenant, "Terminal operators", model.ID(operatorUID))

	// Re-authenticate AFTER the memberships exist: loadGrants folds the groups into
	// the principal at authentication time, so a principal minted earlier carries none.
	env.confined = authenticateCockpit(t, h, confinedTok)
	env.operator = authenticateCockpit(t, h, operatorTok)
	env.plain = authenticateCockpit(t, h, plainTok)
	return env
}

func createCockpitSession(t *testing.T, h *harness, tenant model.TenantID, agent, ws model.ID) model.ID {
	t.Helper()
	var id model.ID
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		s, err := sc.Sessions().Create(context.Background(), model.Session{
			AgentID: agent, WorkspaceID: ws, ExternalID: "ext-" + string(model.NewID()),
			State: model.SessionRunning,
		})
		id = s.ID
		return err
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	return id
}

func createCockpitGroup(t *testing.T, h *harness, tenant model.TenantID, name string, member model.ID) model.ID {
	t.Helper()
	var gid model.ID
	if err := h.st.AuthMutate(context.Background(), func(as store.AuthScope) error {
		g, err := as.Groups().Create(context.Background(), model.UserGroup{TargetTenantID: tenant, DisplayName: name})
		if err != nil {
			return err
		}
		gid = g.ID
		_, err = as.GroupMembers().Create(context.Background(), model.UserGroupMember{GroupID: g.ID, UserID: member})
		return err
	}); err != nil {
		t.Fatalf("create group %s: %v", name, err)
	}
	return gid
}

func authenticateCockpit(t *testing.T, h *harness, token string) auth.Principal {
	t.Helper()
	p, err := h.authr.Authenticate(context.Background(), token)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	return p
}

// scopedOn asks the REAL engine for one decision on one session row, the way a cockpit
// ENTITY ROUTE would: the resource kind is "session", which is what puts the row in the
// Tree at all.
//
// ⛔ IT IS EXPLICIT HERE BECAUSE DERIVING IT SILENTLY UNDER-CONFINES, and this battery
// measured that on itself. auth.ResourceFor derives the kind from the permission's
// middle segment (permission.go Resource()), so `session-cockpit:transcript:read`
// derives kind "transcript" — a kind readScope has no case for, so it falls through to
// declaredScope and the row gets NO workspace, folder or agent-group parents. The
// department forbid of the fixture then matches nothing and confines nobody, silently.
//
// The cure already exists in the engine and is what COCKPIT-02 §4 requires of the
// route: EntityRef.ResourceKind "overrides the auth resource kind for the request"
// (core/api/modules.go), so every cockpit entity route declares ResourceKind "session"
// and authorizes against the SESSION row. TestCockpitTranscriptKindMustBeSession pins
// the failure this replaces.
func (e *cockpitEnv) scopedOn(p auth.Principal, perm auth.Permission, session model.ID) auth.ScopedDecision {
	e.h.t.Helper()
	return e.decide(p, perm, "session", session)
}

// decide is the raw call: caller states the resource kind, as the route's EntityRef does.
func (e *cockpitEnv) decide(p auth.Principal, perm auth.Permission, kind string, id model.ID) auth.ScopedDecision {
	e.h.t.Helper()
	sd, err := e.h.gov.ScopedGrants().Scoped(context.Background(), auth.Request{
		Principal:  p,
		Permission: perm,
		Tenant:     e.tenant,
		Resource:   auth.ResourceAttrs{Kind: kind, ID: id.String()},
		// A cockpit entity route declares the opt-in; without it a Session inherits no
		// AgentGroup, which is exactly what keeps every OTHER route unchanged.
		Route: auth.RouteMetadata{SessionInheritsAgentGroups: true},
	})
	if err != nil {
		e.h.t.Fatalf("Scoped(%s): %v", perm, err)
	}
	return sd
}

// --- 1 · the department forbid actually CONFINES ----------------------------------

// TestCockpitDepartmentForbidConfines is the answer to finding 07: a permit does not
// confine an editor, a forbid does. The confined principal holds editor by RBAC, so if
// the fixture were written as a permit this test would pass for the wrong reason —
// which is why the negative case targets a session the forbid does NOT except.
func TestCockpitDepartmentForbidConfines(t *testing.T) {
	e := newCockpitEnv(t)
	src := loadCockpitPolicy(t, "department-forbid.cedar", map[string]string{
		"<<DEPARTMENT_MEMBERS_GROUP_ID>>": e.deptGrp.String(),
	})
	e.h.publishGrant(e.admin, e.tenant, src)

	if sd := e.scopedOn(e.confined, "session-cockpit:session:read", e.outside); sd.Effect != auth.EffectForbid {
		t.Errorf("a confined principal must be FORBIDDEN on a session outside its department, got %v (%s)", sd.Effect, sd.Reason)
	}
	if sd := e.scopedOn(e.confined, "session-cockpit:session:read", e.finance); sd.Effect == auth.EffectForbid {
		t.Errorf("the unless must except the department's own sessions, got %v (%s)", sd.Effect, sd.Reason)
	}
	// A session with no agent has no group lineage at all: the unless cannot be
	// satisfied, so it stays forbidden. Deny-closed, and stated rather than assumed.
	if sd := e.scopedOn(e.confined, "session-cockpit:session:read", e.orphan); sd.Effect != auth.EffectForbid {
		t.Errorf("a session with unknown lineage must stay forbidden for a confined principal, got %v (%s)", sd.Effect, sd.Reason)
	}
	// And the forbid is keyed on the principal's group, not on everyone.
	if sd := e.scopedOn(e.plain, "session-cockpit:session:read", e.outside); sd.Effect == auth.EffectForbid {
		t.Errorf("the department forbid must not reach a principal outside that department, got %v (%s)", sd.Effect, sd.Reason)
	}
}

// TestCockpitSessionInheritsAgentGroup pins the transitive step the confinement rests
// on, on its own, so a regression there is diagnosed here instead of showing up as a
// mysterious forbid. Before this step existed, `resource in AgentGroup::…` matched an
// agent and abstained on that agent's session — the same authored rule, two answers.
func TestCockpitSessionInheritsAgentGroup(t *testing.T) {
	e := newCockpitEnv(t)
	e.h.publishGrant(e.admin, e.tenant,
		`permit(principal, action == Action::"session-cockpit:session:read", resource) when { resource in AgentGroup::"finance-ops" };`)

	if sd := e.scopedOn(e.plain, "session-cockpit:session:read", e.finance); sd.Effect != auth.EffectGrant {
		t.Errorf("a session must inherit its agent's group, got %v (%s)", sd.Effect, sd.Reason)
	}
	if sd := e.scopedOn(e.plain, "session-cockpit:session:read", e.outside); sd.Effect != auth.EffectAbstain {
		t.Errorf("the grant must not reach another group's session, got %v (%s)", sd.Effect, sd.Reason)
	}
	if sd := e.scopedOn(e.plain, "session-cockpit:session:read", e.orphan); sd.Effect != auth.EffectAbstain {
		t.Errorf("an agentless session inherits no group, got %v (%s)", sd.Effect, sd.Reason)
	}
}

// TestCockpitAgentGroupIsTenantScoped is the isolation half of the transitive step. It
// exists because the change ADDS parents to a resource, and adding reach is exactly the
// direction that has to be shown not to cross a tenant.
//
// Two tenants each get a workspace, an agent, an AgentGroup with the SAME SLUG
// ("finance-ops") and a session. A grant published in tenant A must reach A's session and
// must not exist at all for B: grant policies are keyed by tenant, and the
// resolver reads every parent inside a View pinned to the request tenant, so a slug
// collision across tenants is not a collision.
//
// ⛔ NO CYCLE TEST, AND THE ABSENCE IS DELIBERATE RATHER THAN AN OVERSIGHT. An AgentGroup
// gets exactly one parent here — its workspace (agentGroupParents) — and a workspace has
// none, so the graph this change extends is two levels deep and cannot contain a cycle.
// Writing a cycle test against it would assert a property of a shape that cannot occur,
// which is worse than no test: it would read as coverage. The nesting that CAN be deep is
// the principal's directory groups, and that is loadGrants' territory, not this one.
func TestCockpitAgentGroupIsTenantScoped(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()

	mk := func(org string) (model.TenantID, model.ID, string) {
		tenant := h.createOrg(admin, org)
		ws := h.createWorkspace(tenant, "sessions-ws")
		agent := h.createAgentIn(tenant, org+"-bot", ws)
		h.addAgentToGroup(tenant, agent.ID, "finance-ops", ws)
		sess := createCockpitSession(t, h, tenant, agent.ID, ws)
		_, tok := h.roleUser(admin, tenant, "viewer@"+org+".io", auth.RoleViewer)
		return tenant, sess, tok
	}
	tenantA, sessA, tokA := mk("alpha")
	tenantB, sessB, tokB := mk("beta")
	pA := authenticateCockpit(t, h, tokA)
	pB := authenticateCockpit(t, h, tokB)

	const src = `permit(principal, action == Action::"session-cockpit:session:read", resource) when { resource in AgentGroup::"finance-ops" };`
	h.publishGrant(admin, tenantA, src)

	decide := func(tenant model.TenantID, p auth.Principal, id model.ID) auth.ScopedDecision {
		t.Helper()
		sd, err := h.gov.ScopedGrants().Scoped(context.Background(), auth.Request{
			Principal: p, Permission: "session-cockpit:session:read", Tenant: tenant,
			Resource: auth.ResourceAttrs{Kind: "session", ID: id.String()},
			Route:    auth.RouteMetadata{SessionInheritsAgentGroups: true},
		})
		if err != nil {
			t.Fatalf("Scoped: %v", err)
		}
		return sd
	}

	if sd := decide(tenantA, pA, sessA); sd.Effect != auth.EffectGrant {
		t.Errorf("tenant A's own grant must reach A's session, got %v (%s)", sd.Effect, sd.Reason)
	}
	// Tenant B has no policy at all: the engine abstains BEFORE any store read.
	if sd := decide(tenantB, pB, sessB); sd.Effect != auth.EffectAbstain {
		t.Errorf("tenant B must not inherit A's grant through an identical group slug, got %v (%s)", sd.Effect, sd.Reason)
	}
	// And the row itself does not cross: asking tenant B about A's session id finds no row,
	// so there is no lineage to match either.
	if sd := decide(tenantB, pB, sessA); sd.Effect == auth.EffectGrant {
		t.Errorf("a session id from another tenant must never resolve to a grant, got %v (%s)", sd.Effect, sd.Reason)
	}
}

// TestSessionAgentGroupInheritanceIsOptIn and the test below it are the two halves of a
// HIGH finding the adversarial contrast raised against the first version of this change,
// which resolved a Session's agent-groups UNCONDITIONALLY.
//
// The contrast was right on both counts, so both are pinned here rather than argued:
// this resolver is wired ONCE for the whole engine — request authorization, AuthZEN per
// row and access-review all consume it — so an unconditional parent moved authorization
// for every caller; and an AgentGroup hangs off its OWN workspace while the membership
// API only checks that both ids exist, so the inheritance reached ACROSS workspaces.
func TestSessionAgentGroupInheritanceIsOptIn(t *testing.T) {
	e := newCockpitEnv(t)
	e.h.publishGrant(e.admin, e.tenant,
		`permit(principal, action == Action::"session-cockpit:session:read", resource) when { resource in AgentGroup::"finance-ops" };`)

	ask := func(optIn bool) auth.ScopedDecision {
		t.Helper()
		sd, err := e.h.gov.ScopedGrants().Scoped(context.Background(), auth.Request{
			Principal:  e.plain,
			Permission: "session-cockpit:session:read",
			Tenant:     e.tenant,
			Resource:   auth.ResourceAttrs{Kind: "session", ID: e.finance.String()},
			Route:      auth.RouteMetadata{SessionInheritsAgentGroups: optIn},
		})
		if err != nil {
			t.Fatalf("Scoped: %v", err)
		}
		return sd
	}

	// A route that does NOT ask decides exactly as it did before this capability
	// existed. This is the assertion that keeps every other route in the tree honest.
	if sd := ask(false); sd.Effect != auth.EffectAbstain {
		t.Errorf("without the opt-in a Session must inherit no AgentGroup, got %v (%s)", sd.Effect, sd.Reason)
	}
	// CONTROL POSITIVO: with it, the capability works.
	if sd := ask(true); sd.Effect != auth.EffectGrant {
		t.Errorf("with the opt-in the grant must reach the session, got %v (%s)", sd.Effect, sd.Reason)
	}
}

// TestSessionAgentGroupInheritanceDoesNotCrossWorkspaces reproduces the exact shape the
// contrast built: a Session and its Agent in workspace A, the AgentGroup in workspace B,
// and the membership joining them. That membership is ACCEPTED by the platform — the
// scoping handler only requires both ids to exist — so this is a reachable state and not
// a hypothetical.
//
// Before the fix that shape produced Scoped=EffectGrant on a permit scoped to workspace
// B, for a row living in A.
func TestSessionAgentGroupInheritanceDoesNotCrossWorkspaces(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "crossws")

	wsA := h.createWorkspace(tenant, "ws-a")
	wsB := h.createWorkspace(tenant, "ws-b")
	agent := h.createAgentIn(tenant, "a-bot", wsA)
	// The group lives in B while its member's session lives in A.
	h.addAgentToGroup(tenant, agent.ID, "cross-group", wsB)
	sess := createCockpitSession(t, h, tenant, agent.ID, wsA)
	_, tok := h.roleUser(admin, tenant, "viewer@crossws.io", auth.RoleViewer)
	p := authenticateCockpit(t, h, tok)

	h.publishGrant(admin, tenant,
		`permit(principal, action == Action::"session-cockpit:session:read", resource) when { resource in AgentGroup::"cross-group" };`)

	sd, err := h.gov.ScopedGrants().Scoped(context.Background(), auth.Request{
		Principal:  p,
		Permission: "session-cockpit:session:read",
		Tenant:     tenant,
		Resource:   auth.ResourceAttrs{Kind: "session", ID: sess.String()},
		Route:      auth.RouteMetadata{SessionInheritsAgentGroups: true},
	})
	if err != nil {
		t.Fatalf("Scoped: %v", err)
	}
	if sd.Effect == auth.EffectGrant {
		t.Errorf("a group in another workspace must not become a parent of this session: "+
			"got %v (%s). A department that genuinely spans workspaces is a decision to raise, "+
			"not one to inherit from a membership row", sd.Effect, sd.Reason)
	}

	// CONTROL POSITIVO in the same world: a group in the session's OWN workspace does
	// reach it, so the test above is not passing merely because nothing resolves.
	h.addAgentToGroup(tenant, agent.ID, "same-group", wsA)
	h.publishGrant(admin, tenant,
		`permit(principal, action == Action::"session-cockpit:session:read", resource) when { resource in AgentGroup::"same-group" };`)
	sd2, err := h.gov.ScopedGrants().Scoped(context.Background(), auth.Request{
		Principal:  p,
		Permission: "session-cockpit:session:read",
		Tenant:     tenant,
		Resource:   auth.ResourceAttrs{Kind: "session", ID: sess.String()},
		Route:      auth.RouteMetadata{SessionInheritsAgentGroups: true},
	})
	if err != nil {
		t.Fatalf("Scoped (control): %v", err)
	}
	if sd2.Effect != auth.EffectGrant {
		t.Errorf("a group in the session's OWN workspace must reach it, got %v (%s)", sd2.Effect, sd2.Reason)
	}
}

// --- 2 · the delegated permit GRANTS, and does not narrow -------------------------

func TestCockpitDelegatedTranscriptPermit(t *testing.T) {
	e := newCockpitEnv(t)
	e.h.publishGrant(e.admin, e.tenant, loadCockpitPolicy(t, "delegated-transcript.cedar", nil))

	if sd := e.scopedOn(e.plain, "session-cockpit:transcript:read", e.finance); sd.Effect != auth.EffectGrant {
		t.Errorf("the delegated permit must grant on the delegated group, got %v (%s)", sd.Effect, sd.Reason)
	}
	if sd := e.scopedOn(e.plain, "session-cockpit:transcript:read", e.outside); sd.Effect != auth.EffectAbstain {
		t.Errorf("the delegated permit must not reach outside its group, got %v (%s)", sd.Effect, sd.Reason)
	}
	// A permit is POSITIVE: it must not narrow anyone. An editor who is not the
	// delegate still ABSTAINS here (its authority, if any, comes from RBAC upstream),
	// never FORBID — a permit that produced a forbid would be the inversion the
	// contract forbids.
	if sd := e.scopedOn(e.confined, "session-cockpit:transcript:read", e.outside); sd.Effect == auth.EffectForbid {
		t.Errorf("a permit must never produce a forbid, got %v (%s)", sd.Effect, sd.Reason)
	}
}

// TestCockpitForbidOverridesDelegatedPermit is the composition the matrix of
// COCKPIT-02 §7 requires: permit + forbid on the same row ⇒ forbid wins.
func TestCockpitForbidOverridesDelegatedPermit(t *testing.T) {
	e := newCockpitEnv(t)
	src := loadCockpitPolicy(t, "department-forbid.cedar", map[string]string{
		"<<DEPARTMENT_MEMBERS_GROUP_ID>>": e.deptGrp.String(),
	}) + "\n" + loadCockpitPolicy(t, "delegated-transcript.cedar", nil)
	e.h.publishGrant(e.admin, e.tenant, src)

	if sd := e.scopedOn(e.confined, "session-cockpit:transcript:read", e.outside); sd.Effect != auth.EffectForbid {
		t.Errorf("forbid must override the delegated permit, got %v (%s)", sd.Effect, sd.Reason)
	}
	if sd := e.scopedOn(e.confined, "session-cockpit:transcript:read", e.finance); sd.Effect != auth.EffectGrant {
		t.Errorf("inside the department the delegated permit must still grant, got %v (%s)", sd.Effect, sd.Reason)
	}
}

// --- 3 · the terminal permits: exact target and AAL3 ------------------------------

// terminalReq builds the request the server would build for a materialized target.
// The attributes are Extra because the server materializes them from administrative
// bindings; the caller never supplies them (COCKPIT-02 §5, I-4).
func (e *cockpitEnv) terminalDecision(p auth.Principal, perm auth.Permission, kind string, extra map[string]string, aal int) auth.ScopedDecision {
	e.h.t.Helper()
	res := auth.ResourceAttrs{Kind: kind, Extra: extra}
	pp := p
	pp.AAL = aal
	sd, err := e.h.gov.ScopedGrants().Scoped(context.Background(), auth.Request{
		Principal: pp, Permission: perm, Tenant: e.tenant, Resource: res,
	})
	if err != nil {
		e.h.t.Fatalf("Scoped(%s): %v", perm, err)
	}
	return sd
}

func shellTarget() map[string]string {
	return map[string]string{
		"host_id": "host-prod-01", "unix_uid": "1007",
		"classification": "internal", "shell_enum": "bash",
	}
}

func inputTarget() map[string]string {
	return map[string]string{
		"host_id": "host-prod-01", "unix_uid": "1007",
		"pane_generation": "pg_opaque_42", "classification": "internal",
	}
}

func TestCockpitTerminalPermitsAreExact(t *testing.T) {
	e := newCockpitEnv(t)
	e.h.publishGrant(e.admin, e.tenant, loadCockpitPolicy(t, "terminal-operators.cedar", map[string]string{
		"<<TERMINAL_OPERATORS_GROUP_ID>>": e.opsGrp.String(),
	}))

	// CONTROL POSITIVO — without it this whole battery is satisfied by an engine that
	// denies everything.
	if sd := e.terminalDecision(e.operator, "session-cockpit:shell:write", "shell_target", shellTarget(), 3); sd.Effect != auth.EffectGrant {
		t.Fatalf("exact shell target at AAL3 must GRANT, got %v (%s)", sd.Effect, sd.Reason)
	}
	if sd := e.terminalDecision(e.operator, "session-cockpit:input:write", "input_target", inputTarget(), 3); sd.Effect != auth.EffectGrant {
		t.Fatalf("exact input target at AAL3 must GRANT, got %v (%s)", sd.Effect, sd.Reason)
	}

	// One attribute changed at a time — a differential, so a pass cannot come from the
	// whole record being wrong.
	for _, tc := range []struct {
		name   string
		perm   auth.Permission
		kind   string
		mutate func(map[string]string)
	}{
		{"another host", "session-cockpit:input:write", "input_target", func(m map[string]string) { m["host_id"] = "host-prod-02" }},
		{"another uid", "session-cockpit:input:write", "input_target", func(m map[string]string) { m["unix_uid"] = "1008" }},
		{"another pane generation", "session-cockpit:input:write", "input_target", func(m map[string]string) { m["pane_generation"] = "pg_opaque_43" }},
		{"another classification", "session-cockpit:input:write", "input_target", func(m map[string]string) { m["classification"] = "restricted" }},
		{"missing pane generation", "session-cockpit:input:write", "input_target", func(m map[string]string) { delete(m, "pane_generation") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attrs := inputTarget()
			tc.mutate(attrs)
			if sd := e.terminalDecision(e.operator, tc.perm, tc.kind, attrs, 3); sd.Effect != auth.EffectAbstain {
				t.Errorf("%s must not match the permit, got %v (%s)", tc.name, sd.Effect, sd.Reason)
			}
		})
	}

	// AAL2 does not reach a permit that requires AAL3.
	if sd := e.terminalDecision(e.operator, "session-cockpit:input:write", "input_target", inputTarget(), 2); sd.Effect != auth.EffectAbstain {
		t.Errorf("AAL2 must not satisfy an AAL3 permit, got %v (%s)", sd.Effect, sd.Reason)
	}
	// A principal outside the operators group gets nothing, whatever its RBAC role.
	if sd := e.terminalDecision(e.confined, "session-cockpit:input:write", "input_target", inputTarget(), 3); sd.Effect != auth.EffectAbstain {
		t.Errorf("a non-operator must not match the terminal permit, got %v (%s)", sd.Effect, sd.Reason)
	}
	// And a tenant ADMIN with no terminal permit gets nothing either: breadth of RBAC
	// is not a terminal. e.operator holds admin, so this asserts the property with the
	// permit REMOVED from the equation by asking for a target it does not name.
	other := inputTarget()
	other["host_id"] = "host-prod-99"
	if sd := e.terminalDecision(e.operator, "session-cockpit:input:write", "input_target", other, 3); sd.Effect != auth.EffectAbstain {
		t.Errorf("tenant admin breadth must not open an unnamed target, got %v (%s)", sd.Effect, sd.Reason)
	}
}

// TestCockpitTranscriptKindMustBeSession pins a finding this fixture produced about the
// ENGINE, not about the policy: a cockpit route that lets the resource kind be derived
// from its permission authorizes OUTSIDE the tree, and the department confinement
// then reaches it not at all.
//
// `session-cockpit:transcript:read` derives kind "transcript" (permission.go Resource()
// takes the middle segment). readScope switches on that kind and has cases for agent,
// session and resource only, so a "transcript" request falls to declaredScope and the
// row carries no workspace, no folder chain and no agent-group parents.
//
// The consequence is the expensive direction: the forbid does not fail loudly, it
// matches nothing. Hence COCKPIT-02 §4: every cockpit entity route declares
// EntityRef.ResourceKind "session" and carries the SESSION id.
func TestCockpitTranscriptKindMustBeSession(t *testing.T) {
	e := newCockpitEnv(t)
	src := loadCockpitPolicy(t, "department-forbid.cedar", map[string]string{
		"<<DEPARTMENT_MEMBERS_GROUP_ID>>": e.deptGrp.String(),
	})
	e.h.publishGrant(e.admin, e.tenant, src)

	// Declared as the route must declare it: the forbid reaches the row.
	if sd := e.decide(e.confined, "session-cockpit:transcript:read", "session", e.outside); sd.Effect != auth.EffectForbid {
		t.Errorf("with ResourceKind=session the department forbid must reach a transcript read, got %v (%s)", sd.Effect, sd.Reason)
	}
	// Left to derive from the permission: it does NOT. This is the finding, asserted so
	// that a future change to Resource() or to readScope shows up here as a diff rather
	// than as a confinement that quietly stopped confining.
	derived := auth.ResourceFor("session-cockpit:transcript:read").Kind
	if derived != "transcript" {
		t.Fatalf("this test is built on Resource() deriving %q; it derived %q — re-read the finding before editing", "transcript", derived)
	}
	if sd := e.decide(e.confined, "session-cockpit:transcript:read", derived, e.outside); sd.Effect == auth.EffectForbid {
		t.Errorf("EXPECTED the derived kind %q to escape the scope tree; it did not, so the finding "+
			"COCKPIT-02 §4 records may be stale — check readScope before relaxing the route metadata", derived)
	}
}

// TestCockpitCedarActionIsTheRegisteredID pins the route-metadata indirection of
// COCKPIT-02 §5 against the real engine.
//
// `shell:open` cannot be spelled as a PERMISSION: the parser is a grammar whose last
// segment must be read, write or admin, and it grants by verb tier
// (core/auth/permission.go). So the route carries the permission
// `session-cockpit:shell:write` for RBAC and names its Cedar Action ID in
// RouteMetadata; actionUID uses the registered id when there is one.
//
// The two directions are both asserted, because either alone is satisfied by a bug: a
// policy on the ACTION ID must match only when the route declares it, and the same
// request without the declaration must NOT match.
func TestCockpitCedarActionIsTheRegisteredID(t *testing.T) {
	e := newCockpitEnv(t)
	e.h.publishGrant(e.admin, e.tenant,
		`permit(principal, action == Action::"shell:open", resource) when { resource.kind == "shell_target" };`)

	ask := func(action string) auth.ScopedDecision {
		t.Helper()
		sd, err := e.h.gov.ScopedGrants().Scoped(context.Background(), auth.Request{
			Principal:  e.operator,
			Permission: "session-cockpit:shell:write",
			Tenant:     e.tenant,
			Resource:   auth.ResourceAttrs{Kind: "shell_target", Extra: shellTarget()},
			Route:      auth.RouteMetadata{CedarAction: action},
		})
		if err != nil {
			t.Fatalf("Scoped: %v", err)
		}
		return sd
	}

	if sd := ask("shell:open"); sd.Effect != auth.EffectGrant {
		t.Errorf("a route declaring CedarAction shell:open must match a policy on that action, got %v (%s)", sd.Effect, sd.Reason)
	}
	// Without the declaration the action falls back to the permission, which this
	// policy does not name. This is the case that kills the mutant "actionUID ignores
	// CedarAction": with the indirection removed, BOTH calls take the fallback and the
	// first assertion above goes red.
	if sd := ask(""); sd.Effect != auth.EffectAbstain {
		t.Errorf("without CedarAction the action is the permission, which this policy does not name; got %v (%s)", sd.Effect, sd.Reason)
	}
}

// --- 4 · the fixture compiles as authored ------------------------------------------

// TestCockpitFixtureCompiles publishes each file on its own through the REAL authoring
// surface, which is what compiles it. A fixture that only ever runs concatenated with
// another could hide a syntax error the combined text happens to make legal.
func TestCockpitFixtureCompiles(t *testing.T) {
	e := newCockpitEnv(t)
	subs := map[string]string{
		"<<DEPARTMENT_MEMBERS_GROUP_ID>>": e.deptGrp.String(),
		"<<TERMINAL_OPERATORS_GROUP_ID>>": e.opsGrp.String(),
	}
	entries, err := os.ReadDir(cockpitFixtureDir)
	if err != nil {
		t.Fatalf("read fixture dir: %v", err)
	}
	seen := 0
	for _, ent := range entries {
		if !strings.HasSuffix(ent.Name(), ".cedar") {
			continue
		}
		seen++
		e.h.publishGrant(e.admin, e.tenant, loadCockpitPolicy(t, ent.Name(), subs))
	}
	// CERO = ROJO: a battery that examined no file is not a battery that passed.
	if seen == 0 {
		t.Fatalf("no .cedar fixture found under %s — this test examined nothing", cockpitFixtureDir)
	}
	t.Logf("examinadas %d políticas del fixture del cockpit", seen)
}
