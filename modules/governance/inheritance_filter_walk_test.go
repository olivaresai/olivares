// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The inheritance filter is read by the authorizer: on a container node it stops the
// rights that reach a target from ABOVE the node (a tenant-wide role, the owner's implicit
// grant, a grant anchored higher or tenant-wide) while a grant at or below the node still
// applies, and a forbid keeps its absolute meaning. Every case below runs the REAL
// Authorizer with the real scoped engine, once with no filter and once with the filter, so
// each row of the decision table (inherited, explicit below, forbid) x (filter, none) is a
// pair of answers and not a reading of the code.

const (
	filterWrite = auth.Permission("agent:write")
	// noGrants is a policy that matches nothing, so a case with "no authored grant" still
	// replaces whatever the previous case published.
	noGrants = `permit(principal in Role::"nobody", action == Action::"agent:write", resource);`
)

type filterWorld struct {
	h       *harness
	admin   string
	tenant  model.TenantID
	pay     model.ID // workspace payments
	inPay   model.ID // agent in workspace payments and in agent group pay-ops
	inOther model.ID // agent in workspace other, in no group
	paySess model.ID // session of inPay
}

func newFilterWorld(t *testing.T, slug string) *filterWorld {
	t.Helper()
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, slug)
	pay := h.createWorkspace(tenant, "payments")
	other := h.createWorkspace(tenant, "other")
	inPay := h.createAgentIn(tenant, "pay-bot", pay)
	inOther := h.createAgentIn(tenant, "other-bot", other)
	h.addAgentToGroup(tenant, inPay.ID, "pay-ops", pay)
	return &filterWorld{
		h: h, admin: admin, tenant: tenant, pay: pay, inPay: inPay.ID, inOther: inOther.ID,
		paySess: createCockpitSession(t, h, tenant, inPay.ID, pay),
	}
}

// decide asks the real Authorizer, the way the request path does, for one entity decision.
func (w *filterWorld) decide(role string, perm auth.Permission, kind string, id model.ID) auth.Decision {
	w.h.t.Helper()
	az := auth.NewAuthorizer(w.h.gov.RequestEvaluator(), auth.WithScopedGrants(w.h.gov.ScopedGrants()))
	return az.Authorize(context.Background(), auth.Request{
		Principal:  auth.ScopedPrincipal(model.NewID(), "p", w.tenant, role),
		Permission: perm, Tenant: w.tenant,
		Resource: auth.ResourceAttrs{Kind: kind, ID: id.String()},
	})
}

func (w *filterWorld) setFilter(tree, ref, class string) string {
	w.h.t.Helper()
	r := w.h.rbac("POST", "inheritance-filters", w.admin, w.tenant, filterBody(tree, ref, class))
	if r.code != http.StatusCreated {
		w.h.t.Fatalf("create filter (%s %s %s) = %d %s", tree, ref, class, r.code, r.raw)
	}
	var f filterItem
	if err := json.Unmarshal([]byte(r.raw), &f); err != nil {
		w.h.t.Fatalf("decode filter: %v", err)
	}
	return f.ID
}

func (w *filterWorld) dropFilter(id string) {
	w.h.t.Helper()
	if r := w.h.rbac("DELETE", "inheritance-filters/"+id, w.admin, w.tenant, nil); r.code != http.StatusNoContent {
		w.h.t.Fatalf("delete filter = %d %s", r.code, r.raw)
	}
}

// filterRow is one case of the decision table: who asks, what policy exists, which target,
// and the two answers.
type filterRow struct {
	name                string
	role                string
	policy              string
	target              func(*filterWorld) (kind string, id model.ID)
	allowNone, allowFlt bool
}

func agentInPayments(w *filterWorld) (string, model.ID) { return "agent", w.inPay }
func agentInOther(w *filterWorld) (string, model.ID)    { return "agent", w.inOther }

// runFilterRows publishes each row's policy and checks its answers: first with no filter
// anywhere, then with the one filter install puts in place, then with it removed again.
func runFilterRows(t *testing.T, w *filterWorld, rows []filterRow, install func() (remove func())) {
	t.Helper()
	check := func(phase string, want func(filterRow) bool) {
		for _, row := range rows {
			w.h.publishGrant(w.admin, w.tenant, row.policy)
			kind, id := row.target(w)
			d := w.decide(row.role, filterWrite, kind, id)
			if d.Allow != want(row) {
				t.Errorf("%s / %s: allow=%v (%s), want %v", phase, row.name, d.Allow, d.Reason, want(row))
			}
		}
	}
	check("no filter", func(r filterRow) bool { return r.allowNone })
	remove := install()
	check("filter", func(r filterRow) bool { return r.allowFlt })
	remove()
	// The filter is a row, not a latch: deleting it restores every answer at once.
	check("filter removed", func(r filterRow) bool { return r.allowNone })
}

// With no authored grant at all the scoped engine used to abstain before any store read.
// A filter must still bite there: it is a row of its own, not a part of a grant policy.

// The tenant owner's implicit scoped grant (RequireScopedGrant routes) is a tenant-wide
// right too, so a filter removes it like any other inherited right.

// A filter changes decisions, so a write must advance the policy authorization epoch like a
// grant write does; otherwise a witness minted before the write would outlive it.

// A collection request that DECLARES a workspace has a lineage (the workspace), so a filter on
// it bites. One that declares none has no lineage and is not filtered: its handler narrows the
// rows, as it does for a scoped grant. That limit is stated in the contract; this pins it.

// A grant on a container BESIDE the filtered node (the target is in two groups) is not a right
// from above it and stands; a filter on the workspace and one on the group nest, and the nearer
// node decides what counts as above. These are decisions, pinned so a change shows as a diff.

// A permit written with a negated hierarchy can match the graph cut above the filtered node
// without being one of the permits that carried the request. The request here is carried by a
// tenant-wide permit (a right from above); the negated permit matches only the cut graph, and
// must not turn that inherited right into an explicit one.

// A filter is a row of its tenant: the same names in another tenant are not under it.

// h2 builds a second world on the SAME harness, so both tenants share one store.
func h2(first *filterWorld, slug string) *filterWorld {
	h := first.h
	tenant := h.createOrg(first.admin, slug)
	pay := h.createWorkspace(tenant, "payments")
	inPay := h.createAgentIn(tenant, "pay-bot", pay)
	return &filterWorld{h: h, admin: first.admin, tenant: tenant, pay: pay, inPay: inPay.ID}
}

func TestInheritanceFilterDecisionTableWorkspaceNode(t *testing.T) {
	w := newFilterWorld(t, "ftab-ws")
	const (
		tenantWide = `permit(principal in Role::"viewer", action == Action::"agent:write", resource);`
		atWS       = `permit(principal in Role::"viewer", action == Action::"agent:write", resource) when { resource in Workspace::"payments" };`
		atWSAdmin  = `permit(principal in Role::"admin", action == Action::"agent:write", resource) when { resource in Workspace::"payments" };`
		atGroup    = `permit(principal in Role::"viewer", action == Action::"agent:write", resource) when { resource in AgentGroup::"pay-ops" };`
		atOther    = `permit(principal in Role::"viewer", action == Action::"agent:write", resource) when { resource in Workspace::"other" };`
		forbidWS   = `forbid(principal, action == Action::"agent:write", resource) when { resource in Workspace::"payments" };`
		forbidAll  = `forbid(principal, action == Action::"agent:write", resource);`
	)
	rows := []filterRow{
		// inherited
		{"inherited: tenant admin by role", auth.RoleAdmin, noGrants, agentInPayments, true, false},
		{"inherited: tenant admin by role, target outside the node", auth.RoleAdmin, noGrants, agentInOther, true, true},
		{"inherited: tenant-wide grant", auth.RoleViewer, tenantWide, agentInPayments, true, false},
		{"inherited: tenant-wide grant, target outside the node", auth.RoleViewer, tenantWide, agentInOther, true, true},
		// explicit below (at the node, and below it)
		{"explicit: grant at the node", auth.RoleViewer, atWS, agentInPayments, true, true},
		{"explicit: grant below the node (agent group)", auth.RoleViewer, atGroup, agentInPayments, true, true},
		{"explicit: grant at the node rescues a tenant admin", auth.RoleAdmin, atWSAdmin, agentInPayments, true, true},
		{"explicit: grant on another node does not reach the target", auth.RoleViewer, atOther, agentInPayments, false, false},
		{"explicit: grant at the node, target outside it", auth.RoleViewer, atWS, agentInOther, false, false},
		// forbid, absolute with or without the filter
		{"forbid: at the node", auth.RoleAdmin, forbidWS, agentInPayments, false, false},
		{"forbid: at the node beats the grant at the node", auth.RoleViewer, atWS + "\n" + forbidWS, agentInPayments, false, false},
		{"forbid: above the node beats the grant at the node", auth.RoleViewer, atWS + "\n" + forbidAll, agentInPayments, false, false},
	}
	runFilterRows(t, w, rows, func() func() {
		id := w.setFilter("workspace", "payments", "agent")
		return func() { w.dropFilter(id) }
	})

	// A filter names one class: the same node and principal keep session rights.
	w.setFilter("workspace", "payments", "agent")
	w.h.publishGrant(w.admin, w.tenant, noGrants)
	if d := w.decide(auth.RoleAdmin, "session:read", "session", w.paySess); !d.Allow {
		t.Errorf("a filter on class agent must not touch class session: %s", d.Reason)
	}
	if d := w.decide(auth.RoleAdmin, filterWrite, "agent", w.inPay); d.Allow {
		t.Errorf("the class the filter names must still be filtered: %s", d.Reason)
	}
}

func TestInheritanceFilterDecisionTableAgentGroupNode(t *testing.T) {
	w := newFilterWorld(t, "ftab-grp")
	const (
		atWS    = `permit(principal in Role::"viewer", action == Action::"agent:write", resource) when { resource in Workspace::"payments" };`
		atGroup = `permit(principal in Role::"viewer", action == Action::"agent:write", resource) when { resource in AgentGroup::"pay-ops" };`
	)
	// An agent in the same workspace but outside the group is not under the node.
	wsOnly := w.h.createAgentIn(w.tenant, "ws-only", w.pay).ID
	inWSOnly := func(*filterWorld) (string, model.ID) { return "agent", wsOnly }
	rows := []filterRow{
		{"inherited: tenant admin, agent in the group", auth.RoleAdmin, noGrants, agentInPayments, true, false},
		{"inherited: tenant admin, agent in the workspace but not in the group", auth.RoleAdmin, noGrants, inWSOnly, true, true},
		{"inherited: grant anchored at the workspace above the node", auth.RoleViewer, atWS, agentInPayments, true, false},
		{"explicit: grant at the node", auth.RoleViewer, atGroup, agentInPayments, true, true},
	}
	runFilterRows(t, w, rows, func() func() {
		id := w.setFilter("agent_group", "pay-ops", "agent")
		return func() { w.dropFilter(id) }
	})
}

func TestInheritanceFilterDecisionTableFolderNode(t *testing.T) {
	w := newFilterWorld(t, "ftab-fld")
	// /top -> /top/mid -> /top/mid/leaf, plus /top/sibling and /elsewhere.
	var top, mid, leaf, sibling, elsewhere model.ID
	if err := w.h.st.Mutate(context.Background(), w.tenant, func(sc store.Scope) error {
		ctx := context.Background()
		t0, err := sc.Resources().Create(ctx, model.Resource{Name: "top", Kind: "folder"})
		if err != nil {
			return err
		}
		m0, err := sc.Resources().CreateUnder(ctx, t0.ID, model.Resource{Name: "mid", Kind: "folder"})
		if err != nil {
			return err
		}
		l0, err := sc.Resources().CreateUnder(ctx, m0.ID, model.Resource{Name: "leaf", Kind: "postgres.table"})
		if err != nil {
			return err
		}
		s0, err := sc.Resources().CreateUnder(ctx, t0.ID, model.Resource{Name: "sibling", Kind: "postgres.table"})
		if err != nil {
			return err
		}
		e0, err := sc.Resources().Create(ctx, model.Resource{Name: "elsewhere", Kind: "s3.bucket"})
		top, mid, leaf, sibling, elsewhere = t0.ID, m0.ID, l0.ID, s0.ID, e0.ID
		return err
	}); err != nil {
		t.Fatalf("seed folders: %v", err)
	}
	grantAt := func(folder model.ID) string {
		return `permit(principal in Role::"viewer", action == Action::"resource:write", resource) when { resource in Resource::"` + folder.String() + `" };`
	}
	type row struct {
		name      string
		role      string
		policy    string
		target    model.ID
		none, flt bool
	}
	rows := []row{
		{"inherited: tenant admin, leaf under the node", auth.RoleAdmin, noGrants, leaf, true, false},
		{"inherited: tenant admin, the node itself", auth.RoleAdmin, noGrants, mid, true, false},
		{"inherited: tenant admin, sibling of the node", auth.RoleAdmin, noGrants, sibling, true, true},
		{"inherited: tenant admin, outside the tree", auth.RoleAdmin, noGrants, elsewhere, true, true},
		{"inherited: grant on the parent folder above the node", auth.RoleViewer, grantAt(top), leaf, true, false},
		{"explicit: grant on the node", auth.RoleViewer, grantAt(mid), leaf, true, true},
		{"explicit: grant on the node reaches the node itself", auth.RoleViewer, grantAt(mid), mid, true, true},
		{"explicit: grant below the node", auth.RoleViewer, grantAt(leaf), leaf, true, true},
		{"forbid: on the parent folder beats the grant on the node", auth.RoleViewer, grantAt(mid) + "\n" + `forbid(principal, action == Action::"resource:write", resource) when { resource in Resource::"` + top.String() + `" };`, leaf, false, false},
	}
	for _, phase := range []string{"none", "filter"} {
		var fid string
		if phase == "filter" {
			fid = w.setFilter("folder", mid.String(), "resource")
		}
		for _, r := range rows {
			w.h.publishGrant(w.admin, w.tenant, r.policy)
			want := r.none
			if phase == "filter" {
				want = r.flt
			}
			if d := w.decide(r.role, "resource:write", "resource", r.target); d.Allow != want {
				t.Errorf("%s / %s: allow=%v (%s), want %v", phase, r.name, d.Allow, d.Reason, want)
			}
		}
		if fid != "" {
			w.dropFilter(fid)
		}
	}
}

func TestInheritanceFilterBitesWithoutAnyAuthoredGrant(t *testing.T) {
	w := newFilterWorld(t, "ftab-nogrant")
	if d := w.decide(auth.RoleAdmin, filterWrite, "agent", w.inPay); !d.Allow {
		t.Fatalf("baseline: tenant admin writes an agent: %s", d.Reason)
	}
	id := w.setFilter("workspace", "payments", "agent")
	if d := w.decide(auth.RoleAdmin, filterWrite, "agent", w.inPay); d.Allow {
		t.Errorf("with a filter and no grant policy a tenant admin must be refused: %s", d.Reason)
	}
	if d := w.decide(auth.RoleAdmin, filterWrite, "agent", w.inOther); !d.Allow {
		t.Errorf("a target outside the node is untouched: %s", d.Reason)
	}
	w.dropFilter(id)
	if d := w.decide(auth.RoleAdmin, filterWrite, "agent", w.inPay); !d.Allow {
		t.Errorf("deleting the filter restores the tenant admin: %s", d.Reason)
	}
}

func TestInheritanceFilterRemovesTheOwnersImplicitGrant(t *testing.T) {
	w := newFilterWorld(t, "ftab-owner")
	az := auth.NewAuthorizer(w.h.gov.RequestEvaluator(), auth.WithScopedGrants(w.h.gov.ScopedGrants()))
	ask := func() auth.Decision {
		return az.Authorize(context.Background(), auth.Request{
			Principal:  auth.ScopedPrincipal(model.NewID(), "o", w.tenant, auth.RoleOwner),
			Permission: "session:write", Tenant: w.tenant,
			Resource: auth.ResourceAttrs{Kind: "session", ID: w.paySess.String()},
			Route:    auth.RouteMetadata{RequireScopedGrant: true, SessionInheritsAgentGroups: true},
		})
	}
	if d := ask(); !d.Allow {
		t.Fatalf("baseline: owner on a scoped-grant route: %s", d.Reason)
	}
	w.setFilter("workspace", "payments", "session")
	if d := ask(); d.Allow {
		t.Errorf("a filter must remove the owner's implicit grant on the filtered class: %s", d.Reason)
	}
}

func TestInheritanceFilterWritesAdvanceTheAuthorizationEpoch(t *testing.T) {
	w := newFilterWorld(t, "ftab-epoch")
	epoch := func() int64 {
		var v int64
		if err := w.h.st.View(context.Background(), w.tenant, func(sc store.Scope) error {
			f, err := sc.(store.AuthorizationEpochReader).ReadAuthorizationEpoch(context.Background())
			v = int64(f.Version)
			return err
		}); err != nil {
			w.h.t.Fatalf("read epoch: %v", err)
		}
		return v
	}
	before := epoch()
	id := w.setFilter("workspace", "payments", "agent")
	afterSet := epoch()
	if afterSet <= before {
		t.Errorf("creating a filter must advance the epoch: %d -> %d", before, afterSet)
	}
	w.dropFilter(id)
	if afterDrop := epoch(); afterDrop <= afterSet {
		t.Errorf("deleting a filter must advance the epoch: %d -> %d", afterSet, afterDrop)
	}
}

func TestInheritanceFilterCollectionRequestWithADeclaredWorkspace(t *testing.T) {
	w := newFilterWorld(t, "ftab-coll")
	collection := func(role string, ws model.ID) auth.Decision {
		w.h.t.Helper()
		az := auth.NewAuthorizer(w.h.gov.RequestEvaluator(), auth.WithScopedGrants(w.h.gov.ScopedGrants()))
		return az.Authorize(context.Background(), auth.Request{
			Principal:  auth.ScopedPrincipal(model.NewID(), "p", w.tenant, role),
			Permission: filterWrite, Tenant: w.tenant,
			Resource: auth.ResourceAttrs{Kind: "agent", WorkspaceID: ws},
		})
	}
	const atWS = `permit(principal in Role::"viewer", action == Action::"agent:write", resource) when { resource in Workspace::"payments" };`
	w.h.publishGrant(w.admin, w.tenant, noGrants)
	if !collection(auth.RoleAdmin, w.pay).Allow {
		t.Fatal("baseline: a tenant admin may write agents in payments")
	}
	id := w.setFilter("workspace", "payments", "agent")
	if d := collection(auth.RoleAdmin, w.pay); d.Allow {
		t.Errorf("a declared workspace under the filter must refuse the inherited role: %s", d.Reason)
	}
	if !collection(auth.RoleAdmin, model.ID("")).Allow {
		t.Error("a collection request that declares no workspace has no lineage and is not filtered")
	}
	w.h.publishGrant(w.admin, w.tenant, atWS)
	if !collection(auth.RoleViewer, w.pay).Allow {
		t.Error("a grant at the filtered node stands for a collection request that declares it")
	}
	w.dropFilter(id)
}

func TestInheritanceFilterSiblingAndNestedNodes(t *testing.T) {
	w := newFilterWorld(t, "ftab-nest")
	w.h.addAgentToGroup(w.tenant, w.inPay, "pay-risk", w.pay)
	const (
		atOps  = `permit(principal in Role::"viewer", action == Action::"agent:write", resource) when { resource in AgentGroup::"pay-ops" };`
		atRisk = `permit(principal in Role::"viewer", action == Action::"agent:write", resource) when { resource in AgentGroup::"pay-risk" };`
		atWS   = `permit(principal in Role::"viewer", action == Action::"agent:write", resource) when { resource in Workspace::"payments" };`
	)
	rows := []filterRow{
		{"grant at the filtered group", auth.RoleViewer, atOps, agentInPayments, true, true},
		{"grant at a sibling group of the same agent", auth.RoleViewer, atRisk, agentInPayments, true, true},
		{"grant at the workspace above the group", auth.RoleViewer, atWS, agentInPayments, true, false},
		{"role is inherited", auth.RoleAdmin, noGrants, agentInPayments, true, false},
	}
	runFilterRows(t, w, rows, func() func() {
		id := w.setFilter("agent_group", "pay-ops", "agent")
		return func() { w.dropFilter(id) }
	})

	// A filter on the workspace as well: the nearer node (the group) decides what is above, so
	// the workspace grant is dropped, the group grants stand.
	wsFilter := w.setFilter("workspace", "payments", "agent")
	grpFilter := w.setFilter("agent_group", "pay-ops", "agent")
	for _, c := range []struct {
		name, policy string
		want         bool
	}{{"group grant", atOps, true}, {"sibling group grant", atRisk, true}, {"workspace grant", atWS, false}} {
		w.h.publishGrant(w.admin, w.tenant, c.policy)
		if d := w.decide(auth.RoleViewer, filterWrite, "agent", w.inPay); d.Allow != c.want {
			t.Errorf("nested filters / %s: allow=%v (%s), want %v", c.name, d.Allow, d.Reason, c.want)
		}
	}
	w.dropFilter(grpFilter)
	// With the workspace filter alone the workspace grant is AT the node and stands.
	w.h.publishGrant(w.admin, w.tenant, atWS)
	if d := w.decide(auth.RoleViewer, filterWrite, "agent", w.inPay); !d.Allow {
		t.Errorf("a grant at the only filtered node (the workspace) must stand: %s", d.Reason)
	}
	w.dropFilter(wsFilter)
}

func TestInheritanceFilterNegatedPermitDoesNotLaunderAnInheritedGrant(t *testing.T) {
	w := newFilterWorld(t, "ftab-neg")
	const policy = `permit(principal in Role::"viewer", action == Action::"agent:write", resource);
permit(principal in Role::"viewer", action == Action::"agent:write", resource) when { resource in AgentGroup::"pay-ops" } unless { resource in Workspace::"payments" };`
	w.h.publishGrant(w.admin, w.tenant, policy)
	if d := w.decide(auth.RoleViewer, filterWrite, "agent", w.inPay); !d.Allow {
		t.Fatalf("baseline: the tenant-wide permit allows: %s", d.Reason)
	}
	w.setFilter("agent_group", "pay-ops", "agent")
	if d := w.decide(auth.RoleViewer, filterWrite, "agent", w.inPay); d.Allow {
		t.Errorf("an inherited permit must stay inherited when another permit matches only the cut graph: %s", d.Reason)
	}
}

func TestInheritanceFilterIsPerTenant(t *testing.T) {
	a := newFilterWorld(t, "ftab-t1")
	b := h2(a, "ftab-t2")
	a.setFilter("workspace", "payments", "agent")
	if d := a.decide(auth.RoleAdmin, filterWrite, "agent", a.inPay); d.Allow {
		t.Errorf("tenant A is filtered: %s", d.Reason)
	}
	if d := b.decide(auth.RoleAdmin, filterWrite, "agent", b.inPay); !d.Allow {
		t.Errorf("tenant B has the same workspace and class and must not be filtered: %s", d.Reason)
	}
}
