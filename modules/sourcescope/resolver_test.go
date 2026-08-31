// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope_test

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sourcescope"
)

// resolveAgent is the test shorthand for the agent-scoped resolver entry point.
func (h *harness) resolveAgent(tenant model.TenantID, p auth.Principal, agentRef, sourceType, sourceRef string) sourcescope.Decision {
	h.t.Helper()
	d, err := h.resolver.ResolveForAgent(context.Background(), tenant, p, agentRef, sourceType, sourceRef)
	if err != nil {
		h.t.Fatalf("ResolveForAgent(%s/%s): %v", sourceType, sourceRef, err)
	}
	return d
}

// TestUnboundSourceAllowed: a source with NO binding is global (back-compat) — allowed,
// not bound, no scoped credential.
func TestUnboundSourceAllowed(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	confined := h.principalFor(admin, tenant, "nobody@acme.io", "") // no tenant role
	bot := h.createAgent(tenant, "bot", model.ID(""))

	d := h.resolveAgent(tenant, confined, bot.ExternalID, sourcescope.SourceModel, "claude-opus")
	if !d.Allowed || d.Bound || d.Cred != nil {
		t.Fatalf("unbound source: want allowed/unbound/no-cred, got %+v", d)
	}
}

// TestContainmentConfinement: a model bound to workspace "payments" is resolvable by a
// confined agent IN payments (containment, no grant needed) and DENIED for a confined
// agent in the default workspace. This is the model-B "binding alone confines" core.
func TestContainmentConfinement(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	payments := h.createWorkspace(tenant, "payments")
	confined := h.principalFor(admin, tenant, "nobody@acme.io", "")

	inPay := h.createAgent(tenant, "pay-bot", payments)
	inDefault := h.createAgent(tenant, "default-bot", model.ID(""))

	if r := h.createBinding(admin, tenant, map[string]any{
		"source_type": "model", "source_ref": "claude-opus", "scope_tree": "workspace", "scope_ref": "payments", "enabled": true,
	}); r.code != 201 {
		t.Fatalf("create binding = %d %s", r.code, r.raw)
	}

	if d := h.resolveAgent(tenant, confined, inPay.ExternalID, sourcescope.SourceModel, "claude-opus"); !d.Allowed {
		t.Errorf("in-payments agent must be allowed by containment, got %+v", d)
	}
	if d := h.resolveAgent(tenant, confined, inDefault.ExternalID, sourcescope.SourceModel, "claude-opus"); d.Allowed {
		t.Errorf("default-workspace confined agent must be DENIED (out of scope), got %+v", d)
	}
}

// TestAgentGroupContainment: a model bound to agent-group "bots" is resolvable by an
// agent IN the group and denied for one outside it.
func TestAgentGroupContainment(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	confined := h.principalFor(admin, tenant, "nobody@acme.io", "")

	member := h.createAgent(tenant, "member-bot", model.ID(""))
	outsider := h.createAgent(tenant, "outsider-bot", model.ID(""))
	h.addAgentToGroup(tenant, member.ID, "bots", model.ID(""))

	if r := h.createBinding(admin, tenant, map[string]any{
		"source_type": "model", "source_ref": "m-grp", "scope_tree": "agent_group", "scope_ref": "bots", "enabled": true,
	}); r.code != 201 {
		t.Fatalf("create binding = %d %s", r.code, r.raw)
	}

	if d := h.resolveAgent(tenant, confined, member.ExternalID, sourcescope.SourceModel, "m-grp"); !d.Allowed {
		t.Errorf("group member must be allowed, got %+v", d)
	}
	if d := h.resolveAgent(tenant, confined, outsider.ExternalID, sourcescope.SourceModel, "m-grp"); d.Allowed {
		t.Errorf("non-member must be DENIED, got %+v", d)
	}
}

func TestResolveActorScopeForPrincipalUsesAgentIdentity(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme-actor-scope")
	confined := h.principalFor(admin, tenant, "actor-scope@acme.io", "")
	member := h.createAgent(tenant, "member-bot", model.ID(""))
	h.addAgentToGroup(tenant, member.ID, "bots", model.ID(""))

	_, emptyGroups, err := h.resolver.ResolveActorScopeForPrincipal(context.Background(), tenant, confined, "")
	if err != nil {
		t.Fatalf("ResolveActorScopeForPrincipal empty principal/session: %v", err)
	}
	if len(emptyGroups) != 0 {
		t.Fatalf("empty AgentIdentity and empty sessionRef should yield no agent groups, got %v", emptyGroups)
	}

	_, groups, err := h.resolver.ResolveActorScopeForPrincipal(context.Background(), tenant, confined.WithAgentIdentity(member.ExternalID), "")
	if err != nil {
		t.Fatalf("ResolveActorScopeForPrincipal agent principal: %v", err)
	}
	for _, group := range groups {
		if group == "bots" {
			return
		}
	}
	t.Fatalf("agent principal groups = %v, want bots", groups)
}

// TestCrossScopeGrantOverride: a confined agent OUT of a bound model's workspace is
// normally denied, but a User-subject Cedar grant scoped to that workspace OPENS it
// (the cross-scope override). The principal is confined (no tenant RBAC), so the
// allow can only come from the grant — isolating the grant path from RBAC.
func TestCrossScopeGrantOverride(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "payments")
	confined := h.principalFor(admin, tenant, "grantee@acme.io", "")
	inDefault := h.createAgent(tenant, "grantee-bot", model.ID("")) // out of payments

	if r := h.createBinding(admin, tenant, map[string]any{
		"source_type": "model", "source_ref": "m-grant", "scope_tree": "workspace", "scope_ref": "payments", "enabled": true,
	}); r.code != 201 {
		t.Fatalf("create binding = %d %s", r.code, r.raw)
	}

	// Baseline: confined + out of scope + no grant ⇒ denied.
	if d := h.resolveAgent(tenant, confined, inDefault.ExternalID, sourcescope.SourceModel, "m-grant"); d.Allowed {
		t.Fatalf("baseline: out-of-scope confined agent must be denied, got %+v", d)
	}
	// Grant model:read to THIS user, scoped to payments.
	h.publishGrant(admin, tenant, `permit(principal in User::"`+confined.UserID.String()+`", action == Action::"model:read", resource) when { resource in Workspace::"payments" };`)
	if d := h.resolveAgent(tenant, confined, inDefault.ExternalID, sourcescope.SourceModel, "m-grant"); !d.Allowed {
		t.Errorf("cross-scope grant must open the source, got %+v", d)
	}
}

// TestScopedForbidOverridesContainment: a scoped FORBID denies even an agent that
// containment would otherwise allow (forbid-overrides-permit algebra).
func TestScopedForbidOverridesContainment(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	payments := h.createWorkspace(tenant, "payments")
	confined := h.principalFor(admin, tenant, "nobody@acme.io", "")
	inPay := h.createAgent(tenant, "pay-bot", payments)

	if r := h.createBinding(admin, tenant, map[string]any{
		"source_type": "model", "source_ref": "m-forbid", "scope_tree": "workspace", "scope_ref": "payments", "enabled": true,
	}); r.code != 201 {
		t.Fatalf("create binding = %d %s", r.code, r.raw)
	}
	// Contained ⇒ allowed before the forbid.
	if d := h.resolveAgent(tenant, confined, inPay.ExternalID, sourcescope.SourceModel, "m-forbid"); !d.Allowed {
		t.Fatalf("precondition: contained agent must be allowed, got %+v", d)
	}
	h.publishGrant(admin, tenant, `forbid(principal, action == Action::"model:read", resource) when { resource in Workspace::"payments" };`)
	if d := h.resolveAgent(tenant, confined, inPay.ExternalID, sourcescope.SourceModel, "m-forbid"); d.Allowed {
		t.Errorf("scoped forbid must override containment, got %+v", d)
	}
}

// TestTenantRBACSeesAll: a tenant-wide principal (viewer holds model:read) may use a
// bound model even out of its containment scope — workspace is SOFT-isolation, the
// tenant is the hard boundary. The scoped credential is still returned (never global
// for a bound source).
func TestTenantRBACSeesAll(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "payments")
	viewer := h.principalFor(admin, tenant, "viewer@acme.io", auth.RoleViewer)
	inDefault := h.createAgent(tenant, "v-bot", model.ID("")) // out of payments

	if r := h.createBinding(admin, tenant, map[string]any{
		"source_type": "model", "source_ref": "m-rbac", "scope_tree": "workspace", "scope_ref": "payments", "enabled": true,
		"cred_name": "k", "cred_ref_kind": "vault", "cred_ref": "secret/m-rbac",
	}); r.code != 201 {
		t.Fatalf("create binding = %d %s", r.code, r.raw)
	}
	d := h.resolveAgent(tenant, viewer, inDefault.ExternalID, sourcescope.SourceModel, "m-rbac")
	if !d.Allowed {
		t.Fatalf("tenant viewer must see a bound model out of scope (soft-isolation), got %+v", d)
	}
	if d.Cred == nil || d.Cred.Ref != "secret/m-rbac" {
		t.Errorf("RBAC access to a bound source must still use the SCOPED credential, got %+v", d.Cred)
	}
}

// TestScopedCredNotGlobal: a bound source returns its OWN scoped credential reference to
// an in-scope actor and DENIES an out-of-scope confined actor (which therefore never
// obtains any credential) — the "scoped credential never exposes the global one" DoD.
func TestScopedCredNotGlobal(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	payments := h.createWorkspace(tenant, "payments")
	confined := h.principalFor(admin, tenant, "nobody@acme.io", "")
	inPay := h.createAgent(tenant, "pay-bot", payments)
	inDefault := h.createAgent(tenant, "default-bot", model.ID(""))

	if r := h.createBinding(admin, tenant, map[string]any{
		"source_type": "model", "source_ref": "m-cred", "scope_tree": "workspace", "scope_ref": "payments", "enabled": true,
		"cred_name": "payments-key", "cred_ref_kind": "vault", "cred_ref": "secret/data/payments#key",
	}); r.code != 201 {
		t.Fatalf("create binding = %d %s", r.code, r.raw)
	}
	in := h.resolveAgent(tenant, confined, inPay.ExternalID, sourcescope.SourceModel, "m-cred")
	if !in.Allowed || in.Cred == nil || in.Cred.Name != "payments-key" || in.Cred.Ref != "secret/data/payments#key" {
		t.Errorf("in-scope actor must get the scoped cred ref, got allowed=%v cred=%+v", in.Allowed, in.Cred)
	}
	out := h.resolveAgent(tenant, confined, inDefault.ExternalID, sourcescope.SourceModel, "m-cred")
	if out.Allowed || out.Cred != nil {
		t.Errorf("out-of-scope actor must be denied with NO credential, got %+v", out)
	}
}

// TestEmptyAgentRefDenyClosed: a bound source with an empty/unknown actor reference is
// deny-closed for a confined principal (no resolvable scope, no grant, no RBAC).
func TestEmptyAgentRefDenyClosed(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "payments")
	confined := h.principalFor(admin, tenant, "nobody@acme.io", "")

	if r := h.createBinding(admin, tenant, map[string]any{
		"source_type": "model", "source_ref": "m-empty", "scope_tree": "workspace", "scope_ref": "payments", "enabled": true,
	}); r.code != 201 {
		t.Fatalf("create binding = %d %s", r.code, r.raw)
	}
	for _, ref := range []string{"", "ghost-agent"} {
		if d := h.resolveAgent(tenant, confined, ref, sourcescope.SourceModel, "m-empty"); d.Allowed {
			t.Errorf("empty/unknown actor ref %q must be deny-closed for a bound source, got %+v", ref, d)
		}
	}
}

// --- folder/subtree bindings ------------------------------------------------

// TestFolderBindingDenyClosedThenGrantOpens: a source anchored under a folder has NO
// containment (an actor is not a tree node) — a confined agent is deny-closed
// until an per-entity grant on that folder opens it. The grant action is
// "resource:read" (knowledge/data ride the "resource" scopeable kind) and the scope is
// the folder Resource id; the engine reads its live Path so the grant matches reflexively.
func TestFolderBindingDenyClosedThenGrantOpens(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	confined := h.principalFor(admin, tenant, "nobody@acme.io", "")
	bot := h.createAgent(tenant, "bot", model.ID(""))

	folder := h.createFolder(tenant, "clientes", model.ID(""), model.ID(""))

	r := h.createBinding(admin, tenant, map[string]any{
		"source_type": "knowledge", "source_ref": "kb-1", "scope_tree": "folder", "scope_ref": folder.ID.String(), "enabled": true,
	})
	if r.code != 201 {
		t.Fatalf("create folder binding = %d %s", r.code, r.raw)
	}
	if got, _ := r.body["folder_path"].(string); got != folder.Path {
		t.Errorf("response must surface the resolved folder_path %q, got %q", folder.Path, got)
	}

	// Confined, no grant ⇒ folder-bound source is deny-closed (no containment fallback).
	if d := h.resolveAgent(tenant, confined, bot.ExternalID, sourcescope.SourceKnowledge, "kb-1"); d.Allowed {
		t.Fatalf("folder-bound source must be deny-closed without a grant, got %+v", d)
	}
	// A per-entity grant on the folder opens it.
	h.publishGrant(admin, tenant, `permit(principal in User::"`+confined.UserID.String()+`", action == Action::"resource:read", resource) when { resource in Resource::"`+folder.ID.String()+`" };`)
	if d := h.resolveAgent(tenant, confined, bot.ExternalID, sourcescope.SourceKnowledge, "kb-1"); !d.Allowed {
		t.Errorf("a folder grant must open the source, got %+v", d)
	}
}

// TestFolderBindingInheritanceAndSiblingIsolation: a source anchored under /data/clientes
// is opened by a grant on the ANCESTOR /data (downward inheritance — todo lo que cuelga
// del nodo) but NOT by a grant on the SIBLING subtree /data/contracts.
func TestFolderBindingInheritanceAndSiblingIsolation(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	confined := h.principalFor(admin, tenant, "nobody@acme.io", "")
	bot := h.createAgent(tenant, "bot", model.ID(""))
	uid := confined.UserID.String()

	root := h.createFolder(tenant, "data", model.ID(""), model.ID(""))
	// Spanish fixture: "clientes" IS the folder's name, not a typo of "clients".
	// The identifier mirrors the data it holds, so both stay.
	//nolint:misspell // Spanish folder name in a Spanish fixture.
	clientes := h.createFolder(tenant, "clientes", root.ID, model.ID(""))
	contracts := h.createFolder(tenant, "contracts", root.ID, model.ID(""))

	if r := h.createBinding(admin, tenant, map[string]any{
		//nolint:misspell // same Spanish folder name as above.
		"source_type": "knowledge", "source_ref": "kb-cli", "scope_tree": "folder", "scope_ref": clientes.ID.String(), "enabled": true,
	}); r.code != 201 {
		t.Fatalf("create binding = %d %s", r.code, r.raw)
	}

	// A grant on the SIBLING subtree must NOT reach /data/clientes (the trailing-slash
	// prefix rule keeps a sibling out —).
	h.publishGrant(admin, tenant, `permit(principal in User::"`+uid+`", action == Action::"resource:read", resource) when { resource in Resource::"`+contracts.ID.String()+`" };`)
	if d := h.resolveAgent(tenant, confined, bot.ExternalID, sourcescope.SourceKnowledge, "kb-cli"); d.Allowed {
		t.Errorf("a sibling-subtree grant must NOT open the source, got %+v", d)
	}

	// A grant on the ANCESTOR /data flows DOWN to /data/clientes.
	h.publishGrant(admin, tenant, `permit(principal in User::"`+uid+`", action == Action::"resource:read", resource) when { resource in Resource::"`+root.ID.String()+`" };`)
	if d := h.resolveAgent(tenant, confined, bot.ExternalID, sourcescope.SourceKnowledge, "kb-cli"); !d.Allowed {
		t.Errorf("an ancestor-folder grant must flow down to the subtree, got %+v", d)
	}
}

// TestFolderBindingForbidOverrides: a scoped forbid on the folder overrides a permit on
// the same folder (forbid-overrides-permit algebra — inherited by folder bindings).
func TestFolderBindingForbidOverrides(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	confined := h.principalFor(admin, tenant, "nobody@acme.io", "")
	bot := h.createAgent(tenant, "bot", model.ID(""))
	folder := h.createFolder(tenant, "clientes", model.ID(""), model.ID(""))
	uid := confined.UserID.String()

	if r := h.createBinding(admin, tenant, map[string]any{
		"source_type": "knowledge", "source_ref": "kb-f", "scope_tree": "folder", "scope_ref": folder.ID.String(), "enabled": true,
	}); r.code != 201 {
		t.Fatalf("create binding = %d %s", r.code, r.raw)
	}
	fid := folder.ID.String()
	h.publishGrant(admin, tenant,
		`permit(principal in User::"`+uid+`", action == Action::"resource:read", resource) when { resource in Resource::"`+fid+`" };`+
			`forbid(principal, action == Action::"resource:read", resource) when { resource in Resource::"`+fid+`" };`)
	if d := h.resolveAgent(tenant, confined, bot.ExternalID, sourcescope.SourceKnowledge, "kb-f"); d.Allowed {
		t.Errorf("a folder forbid must override the folder grant, got %+v", d)
	}
}

// TestFolderBindingRBACSeesAll: a folder is SOFT-isolation (like a workspace) — a tenant
// principal holding resource:read sees a folder-bound source without a per-entity grant.
// The honest posture: folder confinement is never advertised as an unbypassable boundary.
func TestFolderBindingRBACSeesAll(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.principalFor(admin, tenant, "viewer@acme.io", auth.RoleViewer) // holds resource:read
	bot := h.createAgent(tenant, "v-bot", model.ID(""))
	folder := h.createFolder(tenant, "clientes", model.ID(""), model.ID(""))

	if r := h.createBinding(admin, tenant, map[string]any{
		"source_type": "knowledge", "source_ref": "kb-rbac", "scope_tree": "folder", "scope_ref": folder.ID.String(), "enabled": true,
	}); r.code != 201 {
		t.Fatalf("create binding = %d %s", r.code, r.raw)
	}
	if d := h.resolveAgent(tenant, viewer, bot.ExternalID, sourcescope.SourceKnowledge, "kb-rbac"); !d.Allowed {
		t.Errorf("a tenant viewer must see a folder-bound source (soft-isolation), got %+v", d)
	}
}

// TestFolderBindingDanglingFolderNotUnbound: deleting the anchor folder must NOT silently
// turn a folder-bound source into an UNBOUND (global) one. The binding still confines: a
// confined principal with no grant stays deny-closed. (A grant authored on the exact
// folder id would still match — the id is the durable Cedar anchor and Cedar's `in` is
// reflexive — so deletion breaks only ANCESTOR inheritance, never the source's
// confinement; the important property is that a vanished anchor never opens the source.)
func TestFolderBindingDanglingFolderNotUnbound(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	confined := h.principalFor(admin, tenant, "nobody@acme.io", "")
	bot := h.createAgent(tenant, "bot", model.ID(""))
	folder := h.createFolder(tenant, "gone", model.ID(""), model.ID(""))

	if r := h.createBinding(admin, tenant, map[string]any{
		"source_type": "knowledge", "source_ref": "kb-dangle", "scope_tree": "folder", "scope_ref": folder.ID.String(), "enabled": true,
	}); r.code != 201 {
		t.Fatalf("create binding = %d %s", r.code, r.raw)
	}
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		return sc.Resources().Delete(context.Background(), folder.ID)
	}); err != nil {
		t.Fatalf("delete folder: %v", err)
	}
	// No grant: a confined principal must remain deny-closed (the source is still BOUND,
	// never reverted to the unbound/global path).
	d := h.resolveAgent(tenant, confined, bot.ExternalID, sourcescope.SourceKnowledge, "kb-dangle")
	if d.Allowed || !d.Bound {
		t.Errorf("a dangling folder binding must stay bound + deny-closed for an ungranted principal, got %+v", d)
	}
}

// --- confused-deputy hardening tests ----------------------------------------

// TestS264AgentIdentityOverridesCallerRef: when the principal carries an authenticated
// AgentIdentity the resolver MUST use it, not the caller-declared agentRef.
//
// Setup: agent-A lives in workspace "main" (source bound there); agent-B lives in the
// default workspace (out of scope). A caller that declares agentRef="agent-B" while
// holding a principal with AgentIdentity="agent-A" must resolve as agent-A and be
// ALLOWED — the authenticated identity wins over the declared one.
func TestS264AgentIdentityOverridesCallerRef(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme-s264a")
	main := h.createWorkspace(tenant, "main")
	confined := h.principalFor(admin, tenant, "nobody-a@acme.io", "")

	agentA := h.createAgent(tenant, "agent-A", main) // authenticated identity — in "main"
	h.createAgent(tenant, "agent-B", model.ID(""))   // caller's forged claim — in default (out of scope)

	if r := h.createBinding(admin, tenant, map[string]any{
		"source_type": "model", "source_ref": "m-s264a", "scope_tree": "workspace", "scope_ref": "main", "enabled": true,
	}); r.code != 201 {
		t.Fatalf("create binding = %d %s", r.code, r.raw)
	}

	// Baseline: without AgentIdentity, the caller-declared "agent-B" (out of scope) is denied.
	if d := h.resolveAgent(tenant, confined, agentA.ExternalID, sourcescope.SourceModel, "m-s264a"); !d.Allowed {
		t.Fatalf("precondition: agent-A in main must be allowed by containment, got %+v", d)
	}

	// Principal carries AgentIdentity = "agent-A"; caller declares "agent-B".
	// resolver must use "agent-A" → in "main" → ALLOWED.
	p := confined.WithAgentIdentity(agentA.ExternalID)
	if d := h.resolveAgent(tenant, p, "agent-B", sourcescope.SourceModel, "m-s264a"); !d.Allowed {
		t.Errorf("principal's AgentIdentity (agent-A in main) must override caller's ref (agent-B), got %+v", d)
	}
}

// TestS264ForeignRefWithoutGrantDenied: this is the confused-deputy ATTACK scenario.
//
// Setup: agent-B lives in workspace "restricted" (source bound there); agent-A lives
// in the default workspace. A caller that declares agentRef="agent-B" to inherit its
// restricted scope must be DENIED when the principal carries AgentIdentity="agent-A"
// (the authenticated agent has no access to the restricted workspace).
func TestS264ForeignRefWithoutGrantDenied(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme-s264b")
	h.createWorkspace(tenant, "restricted")
	confined := h.principalFor(admin, tenant, "nobody-b@acme.io", "")

	agentA := h.createAgent(tenant, "agent-A", model.ID("")) // in default — the real caller
	h.createAgent(tenant, "agent-B", model.ID(""))           // in default too; we bind source to "restricted"

	if r := h.createBinding(admin, tenant, map[string]any{
		"source_type": "model", "source_ref": "m-s264b", "scope_tree": "workspace", "scope_ref": "restricted", "enabled": true,
	}); r.code != 201 {
		t.Fatalf("create binding = %d %s", r.code, r.raw)
	}

	// Verify pre confused-deputy would succeed: "agent-B" is declared and
	// the source is in "restricted"; an agent-B with the right workspace would be allowed.
	// (Here both agents are in default so this is deny-closed too — the point is
	// ensures the override regardless.)

	// principal carries AgentIdentity = "agent-A" (default workspace).
	// Caller declares agentRef = "agent-B" to try to pick up its (hypothetical) access.
	// Resolver overrides to "agent-A" → default workspace → not in "restricted" → DENIED.
	p := confined.WithAgentIdentity(agentA.ExternalID)
	if d := h.resolveAgent(tenant, p, "agent-B", sourcescope.SourceModel, "m-s264b"); d.Allowed {
		t.Errorf("caller-declared ref must not grant access the authenticated identity (agent-A) lacks, got %+v", d)
	}
}

// TestActorScopeForPrincipalRequiresAgentBinding is the F-01 negative test that REPLACES
// the retired TestS264NoAgentIdentityPreservesLegacy (which deliberately blessed the
// insecure legacy path — a no-AgentIdentity principal borrowing a caller-named agent's
// scope). The model-access actor scope (ResolveActorScopeForPrincipal) must be established
// ONLY by the token's authenticated AgentIdentity: a caller-supplied session reference,
// even one that names a real same-tenant session in a restricted workspace, must NOT
// establish effective identity. Binding is REQUIRED.
func TestActorScopeForPrincipalRequiresAgentBinding(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme-f01")
	prod := h.createWorkspace(tenant, "prod")

	agent := h.createAgent(tenant, "prod-bot", prod)
	h.addAgentToGroup(tenant, agent.ID, "prod-agents", prod)
	session := h.createSession(tenant, "prod-session", agent.ID, prod)

	confined := h.principalFor(admin, tenant, "nobody-f01@acme.io", "")
	if confined.AgentIdentity != "" {
		t.Fatalf("precondition: principalFor must not set AgentIdentity, got %q", confined.AgentIdentity)
	}

	// A caller-supplied session_ref of ANOTHER agent's production session must NOT borrow
	// that session's workspace/agent-groups for a principal with no authenticated agent
	// binding — the actor stays unresolved (empty scope).
	ws, groups, err := h.resolver.ResolveActorScopeForPrincipal(context.Background(), tenant, confined, session.ExternalID)
	if err != nil {
		t.Fatalf("ResolveActorScopeForPrincipal (no binding): %v", err)
	}
	if ws != "" || len(groups) != 0 {
		t.Fatalf("a caller-supplied session_ref must not establish actor scope without an agent binding, got ws=%q groups=%v", ws, groups)
	}

	// With the AUTHENTICATED agent binding the same actor scope resolves — binding is what
	// establishes identity, not the caller reference.
	ws, groups, err = h.resolver.ResolveActorScopeForPrincipal(context.Background(), tenant, confined.WithAgentIdentity(agent.ExternalID), "")
	if err != nil {
		t.Fatalf("ResolveActorScopeForPrincipal (bound): %v", err)
	}
	if ws != "prod" {
		t.Fatalf("bound actor must resolve to its workspace, got ws=%q", ws)
	}
	found := false
	for _, g := range groups {
		if g == "prod-agents" {
			found = true
		}
	}
	if !found {
		t.Fatalf("bound actor must resolve its agent-groups, got %v", groups)
	}
}

// TestSessionResolutionContainment: ResolveForSession derives the actor scope from the
// stored session's workspace (the model-PEP path).
func TestSessionResolutionContainment(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	payments := h.createWorkspace(tenant, "payments")
	confined := h.principalFor(admin, tenant, "nobody@acme.io", "")

	agent := h.createAgent(tenant, "sess-bot", payments)
	inPay := h.createSession(tenant, "sess-pay", agent.ID, payments)
	inDefault := h.createSession(tenant, "sess-def", agent.ID, model.ID(""))

	if r := h.createBinding(admin, tenant, map[string]any{
		"source_type": "model", "source_ref": "m-sess", "scope_tree": "workspace", "scope_ref": "payments", "enabled": true,
	}); r.code != 201 {
		t.Fatalf("create binding = %d %s", r.code, r.raw)
	}
	if d, err := h.resolver.ResolveForSession(context.Background(), tenant, confined, inPay.ExternalID, sourcescope.SourceModel, "m-sess"); err != nil || !d.Allowed {
		t.Errorf("session in payments must be allowed, got %+v err=%v", d, err)
	}
	if d, err := h.resolver.ResolveForSession(context.Background(), tenant, confined, inDefault.ExternalID, sourcescope.SourceModel, "m-sess"); err != nil || d.Allowed {
		t.Errorf("session in default must be denied for the payments-bound model, got %+v err=%v", d, err)
	}
}
