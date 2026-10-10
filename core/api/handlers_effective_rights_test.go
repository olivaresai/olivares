// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// effectiveRightsURL is the read for one (subject, node): the rights the subject holds at
// the node and the path the node sits on.
func effectiveRightsURL(subject, kind, id string) string {
	q := url.Values{"subject_type": {"user"}, "subject_id": {subject}, "kind": {kind}, "id": {id}}
	return "/v1/auth/effective-rights?" + q.Encode()
}

// rightStates returns the rights in the order the response lists them and each one's state.
func rightStates(t *testing.T, r resp) (names []string, state map[string]string) {
	t.Helper()
	state = map[string]string{}
	rights, _ := r.body["rights"].([]any)
	for _, it := range rights {
		m := it.(map[string]any)
		name := m["name"].(string)
		names = append(names, name)
		state[name] = m["state"].(string)
	}
	return names, state
}

// pathRefs flattens the response path into "kind:ref" steps.
func pathRefs(r resp) []string {
	var out []string
	steps, _ := r.body["path"].([]any)
	for _, it := range steps {
		m := it.(map[string]any)
		out = append(out, m["kind"].(string)+":"+m["ref"].(string))
	}
	return out
}

var allRights = []string{"Supervisor", "Browse", "Read", "Write", "Create", "Erase", "Modify", "Access Control"}

// Each built-in role holds a hand-written set of trustee rights at an agent, resource and
// session alike. The matrix is written out here, not read from the table, so a right that
// drifts shows as a diff. Every right the role lacks is "not_held": a plain denial is
// never "unknown".
func TestEffectiveRightsFollowTheRoleAtANode(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	agent := h.mkAgent(admin, tenant, "worker")
	var resource model.Resource
	var session model.Session
	ctx := context.Background()
	if err := h.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var err error
		if resource, err = sc.Resources().Create(ctx, model.Resource{Name: "doc", Kind: "doc", URI: "file:///doc"}); err != nil {
			return err
		}
		session, err = sc.Sessions().Create(ctx, model.Session{AgentID: model.ID(agent)})
		return err
	}); err != nil {
		t.Fatalf("seed nodes: %v", err)
	}
	nodes := map[string]string{"agent": agent, "resource": resource.ID.String(), "session": session.ID.String()}

	write := []string{"Write", "Create", "Erase", "Modify"}
	for _, tc := range []struct {
		role string
		want []string
	}{
		{auth.RoleViewer, []string{"Browse", "Read"}},
		{auth.RoleEditor, slices.Concat([]string{"Browse", "Read"}, write)},
		{auth.RoleAdmin, slices.Concat([]string{"Browse", "Read"}, write, []string{"Access Control"})},
		{auth.RoleOwner, slices.Concat([]string{"Supervisor", "Browse", "Read"}, write, []string{"Access Control"})},
	} {
		uid, _ := h.authzMember(admin, tc.role+"@acme.io", "memberpass1", tc.role, tenant)
		for kind, id := range nodes {
			t.Run(tc.role+"/"+kind, func(t *testing.T) {
				r := h.do("GET", effectiveRightsURL(uid, kind, id), admin, nil, tenantHdr(tenant))
				if r.code != http.StatusOK {
					t.Fatalf("effective-rights = %d %s", r.code, r.raw)
				}
				names, state := rightStates(t, r)
				if !reflect.DeepEqual(names, allRights) {
					t.Fatalf("rights listed = %v, want all eight in table order %v", names, allRights)
				}
				for _, name := range allRights {
					want := "not_held"
					if slices.Contains(tc.want, name) {
						want = "held"
					}
					if state[name] != want {
						t.Errorf("%s at a %s: %s = %q, want %q", tc.role, kind, name, state[name], want)
					}
				}
				if cc := r.hdr.Get("Cache-Control"); !strings.Contains(cc, "no-store") {
					t.Errorf("Cache-Control = %q, want no-store on an authorization answer", cc)
				}
				if r.body["assurance"] != float64(auth.AAL3) {
					t.Errorf("assurance = %v, want %d", r.body["assurance"], auth.AAL3)
				}
				if got := r.body["subject"].(map[string]any); got["kind"] != "user" || got["id"] != uid {
					t.Errorf("subject = %v, want user %s", got, uid)
				}
				if got := r.body["node"].(map[string]any); got["kind"] != kind || got["id"] != id {
					t.Errorf("node = %v, want %s %s", got, kind, id)
				}
			})
		}
	}
}

// A token is a subject too, and answers as its bound role.
func TestEffectiveRightsAnswerForATokenSubject(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	agent := h.mkAgent(admin, tenant, "worker")
	issued := h.do("POST", "/v1/tokens", admin, map[string]any{"name": "ci", "tenant": tenant.String(), "role": auth.RoleViewer}, nil)
	if issued.code != http.StatusCreated {
		t.Fatalf("issue token = %d %s", issued.code, issued.raw)
	}
	q := url.Values{"subject_type": {"token"}, "subject_id": {issued.body["id"].(string)}, "kind": {"agent"}, "id": {agent}}
	r := h.do("GET", "/v1/auth/effective-rights?"+q.Encode(), admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("token effective-rights = %d %s", r.code, r.raw)
	}
	_, state := rightStates(t, r)
	if state["Read"] != "held" || state["Write"] != "not_held" {
		t.Errorf("viewer-bound token: Read=%q Write=%q, want held and not_held", state["Read"], state["Write"])
	}
	if got := r.body["subject"].(map[string]any); got["kind"] != "token" || got["id"] != issued.body["id"] {
		t.Errorf("subject = %v, want the token", got)
	}
}

// The path is the node's containment read from its stored row: the workspace, the agent
// groups the agent belongs to, then the node. A group of another workspace says so. A
// resource's path runs through its folders, root first, however deep.
func TestEffectiveRightsReturnThePathFromTheStoredRow(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer, _ := h.authzMember(admin, "vw@acme.io", "viewerpass1", auth.RoleViewer, tenant)

	mkWorkspace := func(name, slug string) string {
		ws := h.do("POST", "/v1/workspaces", admin, map[string]any{"name": name, "slug": slug}, tenantHdr(tenant))
		if ws.code != http.StatusCreated {
			t.Fatalf("create workspace = %d %s", ws.code, ws.raw)
		}
		return ws.body["id"].(string)
	}
	wsID, otherWS := mkWorkspace("Team", "team"), mkWorkspace("Other", "other")
	ag := h.do("POST", "/v1/agents", admin, map[string]any{"name": "a", "kind": "test", "workspace_id": wsID}, tenantHdr(tenant))
	if ag.code != http.StatusCreated {
		t.Fatalf("create agent = %d %s", ag.code, ag.raw)
	}
	agentID := ag.body["id"].(string)
	for _, g := range []struct{ slug, ws string }{{"builders", wsID}, {"visitors", otherWS}} {
		grp := h.do("POST", "/v1/agent-groups", admin, map[string]any{"name": g.slug, "slug": g.slug, "workspace_id": g.ws}, tenantHdr(tenant))
		if grp.code != http.StatusCreated {
			t.Fatalf("create group = %d %s", grp.code, grp.raw)
		}
		if r := h.do("PUT", "/v1/agent-groups/"+grp.body["id"].(string)+"/members/"+agentID, admin, nil, tenantHdr(tenant)); r.code >= 300 {
			t.Fatalf("add member = %d %s", r.code, r.raw)
		}
	}

	r := h.do("GET", effectiveRightsURL(viewer, "agent", agentID), admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("agent effective-rights = %d %s", r.code, r.raw)
	}
	want := []string{"workspace:team", "agent_group:builders", "agent_group:visitors", "agent:" + agentID}
	if got := pathRefs(r); !reflect.DeepEqual(got, want) {
		t.Errorf("agent path = %v, want %v", got, want)
	}
	for _, it := range r.body["path"].([]any) {
		step := it.(map[string]any)
		ws, has := step["workspace"]
		switch step["ref"] {
		case "visitors":
			if ws != "other" {
				t.Errorf("a group of another workspace says workspace=%v, want other", ws)
			}
		default:
			if has {
				t.Errorf("step %v names a workspace, which only a foreign group does", step)
			}
		}
	}

	var root, mid, leaf model.Resource
	ctx := context.Background()
	if err := h.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var err error
		if root, err = sc.Resources().Create(ctx, model.Resource{Name: "root", Kind: "folder", URI: "file:///root"}); err != nil {
			return err
		}
		if mid, err = sc.Resources().Create(ctx, model.Resource{Name: "mid", Kind: "folder", URI: "file:///root/mid", ParentID: root.ID}); err != nil {
			return err
		}
		leaf, err = sc.Resources().Create(ctx, model.Resource{Name: "leaf", Kind: "doc", URI: "file:///root/mid/leaf", ParentID: mid.ID})
		return err
	}); err != nil {
		t.Fatalf("seed resources: %v", err)
	}
	r = h.do("GET", effectiveRightsURL(viewer, "resource", leaf.ID.String()), admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("resource effective-rights = %d %s", r.code, r.raw)
	}
	want = []string{"workspace:" + model.DefaultWorkspaceSlug, "folder:" + root.ID.String(), "folder:" + mid.ID.String(), "resource:" + leaf.ID.String()}
	if got := pathRefs(r); !reflect.DeepEqual(got, want) {
		t.Errorf("resource path = %v, want %v", got, want)
	}
}

// Reading another subject's rights reconstructs the access matrix, so the route is
// admin-tier (authz:admin) and a workspace-confined admin, who has no tenant-wide view of
// it, is refused like the other access-matrix reads.
func TestEffectiveRightsAreAdminGated(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.elevate(admin)
	agent := h.mkAgent(admin, tenant, "worker")
	subject, _ := h.authzMember(admin, "vw@acme.io", "viewerpass1", auth.RoleViewer, tenant)
	_, editorTok := h.authzMember(admin, "ed@acme.io", "editorpass1", auth.RoleEditor, tenant)
	_, adminTok := h.authzMember(admin, "ad@acme.io", "adminpass12", auth.RoleAdmin, tenant)

	var defaultID string
	for _, it := range h.do("GET", "/v1/workspaces", admin, nil, tenantHdr(tenant)).body["items"].([]any) {
		if ws := it.(map[string]any); ws["slug"] == model.DefaultWorkspaceSlug {
			defaultID = ws["id"].(string)
		}
	}
	if r := h.do("POST", "/v1/users", admin, map[string]any{
		"email": "confined@acme.io", "password": "confinedpass1", "tenant": tenant.String(),
		"role": auth.RoleAdmin, "workspace_id": defaultID,
	}, nil); r.code != http.StatusCreated {
		t.Fatalf("create confined admin = %d %s", r.code, r.raw)
	}
	confinedTok := h.login("confined@acme.io", "confinedpass1")
	issue := func(role string) string {
		r := h.do("POST", "/v1/tokens", admin, map[string]any{"name": role, "tenant": tenant.String(), "role": role}, nil)
		if r.code != http.StatusCreated {
			t.Fatalf("issue %s token = %d %s", role, r.code, r.raw)
		}
		return r.body["token"].(string)
	}

	target := effectiveRightsURL(subject, "agent", agent)
	for _, tc := range []struct {
		name, token string
		want        int
	}{
		{"anonymous", "", http.StatusUnauthorized},
		{"editor", editorTok, http.StatusForbidden},
		{"confined admin", confinedTok, http.StatusForbidden},
		{"admin", adminTok, http.StatusOK},
		{"viewer token", issue(auth.RoleViewer), http.StatusForbidden},
		{"admin token", issue(auth.RoleAdmin), http.StatusOK},
	} {
		if r := h.do("GET", target, tc.token, nil, tenantHdr(tenant)); r.code != tc.want {
			t.Errorf("%s = %d %s, want %d", tc.name, r.code, r.raw, tc.want)
		}
	}
	// A read: the route is registered for GET alone, so a write verb is a 405, not a 404.
	if r := h.do("POST", target, adminTok, map[string]any{}, tenantHdr(tenant)); r.code != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d %s, want 405", r.code, r.raw)
	}
}

// A subject the tenant does not know, a subject of another tenant and a node with no row
// all answer 404, and the first two answer alike, so the read is no oracle for which ids
// exist elsewhere. A malformed question is a 400.
func TestEffectiveRightsRefuseWhatTheyCannotAnswer(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	other := h.createOrg(admin, "other")
	agent := h.mkAgent(admin, tenant, "worker")
	foreignAgent := h.mkAgent(admin, other, "foreign-worker")
	member, _ := h.authzMember(admin, "vw@acme.io", "viewerpass1", auth.RoleViewer, tenant)
	stranger, _ := h.authzMember(admin, "st@other.io", "strangerpass1", auth.RoleViewer, other)

	zero := "00000000-0000-0000-0000-000000000000"
	var bodies []string
	for _, tc := range []struct {
		name, target string
		want         int
	}{
		{"unknown subject", effectiveRightsURL(model.NewID().String(), "agent", agent), http.StatusNotFound},
		{"subject of another tenant", effectiveRightsURL(stranger, "agent", agent), http.StatusNotFound},
		{"node without a row", effectiveRightsURL(member, "agent", model.NewID().String()), http.StatusNotFound},
		{"node of another tenant", effectiveRightsURL(member, "agent", foreignAgent), http.StatusNotFound},
		{"subject given by email", effectiveRightsURL("vw@acme.io", "agent", agent), http.StatusBadRequest},
		{"kind without a stored lineage", effectiveRightsURL(member, "provider", agent), http.StatusBadRequest},
		{"no subject", effectiveRightsURL("", "agent", agent), http.StatusBadRequest},
		{"no node id", effectiveRightsURL(member, "agent", ""), http.StatusBadRequest},
		{"malformed node id", effectiveRightsURL(member, "agent", strings.Repeat("x", 3)), http.StatusBadRequest},
		{"zero node id", effectiveRightsURL(member, "agent", zero), http.StatusBadRequest},
		{"unknown subject type", strings.Replace(effectiveRightsURL(member, "agent", agent), "subject_type=user", "subject_type=robot", 1), http.StatusBadRequest},
	} {
		r := h.do("GET", tc.target, admin, nil, tenantHdr(tenant))
		if r.code != tc.want {
			t.Errorf("%s = %d %s, want %d", tc.name, r.code, r.raw, tc.want)
		}
		// An unregistered route is also a 404; only the handler's own refusal counts.
		if strings.Contains(r.raw, "No route is registered") {
			t.Errorf("%s: the route is not registered: %s", tc.name, r.raw)
		}
		if tc.name == "unknown subject" || tc.name == "subject of another tenant" {
			bodies = append(bodies, r.raw)
		}
	}
	if len(bodies) == 2 && bodies[0] != bodies[1] {
		t.Errorf("an unknown subject and one of another tenant answer differently: %s vs %s", bodies[0], bodies[1])
	}
}

// failingScoped is a scoped-grant engine that cannot evaluate for one user: the case where
// Authorize denies because the engine failed, not because policy says no.
type failingScoped struct{ failFor atomic.Value }

func (f *failingScoped) Scoped(_ context.Context, req auth.Request) (auth.ScopedDecision, error) {
	if id, _ := f.failFor.Load().(string); id != "" && req.Principal.UserID.String() == id {
		return auth.ScopedDecision{}, errors.New("scope store unavailable")
	}
	return auth.ScopedDecision{}, nil
}

// An engine that cannot decide is not a denial. Authorize fails closed with Allow:false in
// that case, so a boolean alone would tell an administrator "no access" about a subject
// whose rights simply could not be evaluated; the read says "unknown".
func TestEffectiveRightsSayUnknownWhenTheEngineCannotDecide(t *testing.T) {
	engine := &failingScoped{}
	h := newHarnessOpts(t, func(o *api.Options) {
		o.Authorizer = auth.NewAuthorizer(nil, auth.WithScopedGrants(engine))
	})
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	agent := h.mkAgent(admin, tenant, "worker")
	broken, _ := h.authzMember(admin, "broken@acme.io", "brokenpass1", auth.RoleEditor, tenant)
	healthy, _ := h.authzMember(admin, "healthy@acme.io", "healthypass1", auth.RoleEditor, tenant)

	r := h.do("GET", effectiveRightsURL(healthy, "agent", agent), admin, nil, tenantHdr(tenant))
	if _, state := rightStates(t, r); r.code != http.StatusOK || state["Read"] != "held" || state["Write"] != "held" {
		t.Fatalf("control: a healthy editor must hold Read and Write; got %d %s", r.code, r.raw)
	}

	engine.failFor.Store(broken)
	r = h.do("GET", effectiveRightsURL(broken, "agent", agent), admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("effective-rights = %d %s", r.code, r.raw)
	}
	_, state := rightStates(t, r)
	for _, name := range allRights {
		if state[name] != "unknown" {
			t.Errorf("%s = %q while the engine fails for the subject, want unknown", name, state[name])
		}
	}
}

// The operator's AuthZEN exposure controls cut the reverse queries that reconstruct the
// access matrix; this read is one, so they cut it too.
func TestEffectiveRightsFollowTheAuthZenExposureControls(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config api.AuthZenConfig
		want   int
	}{
		{"open", api.AuthZenConfig{}, http.StatusOK},
		{"search disabled", api.AuthZenConfig{SearchDisabled: true}, http.StatusNotFound},
		{"surface disabled", api.AuthZenConfig{Disabled: true}, http.StatusNotFound},
		{"outside the allowed network", api.AuthZenConfig{AllowedCIDRs: []string{"192.0.2.0/24"}}, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarnessOpts(t, func(o *api.Options) { o.AuthZen = &tc.config })
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "acme")
			agent := h.mkAgent(admin, tenant, "worker")
			subject, _ := h.authzMember(admin, "vw@acme.io", "viewerpass1", auth.RoleViewer, tenant)
			r := h.do("GET", effectiveRightsURL(subject, "agent", agent), admin, nil, tenantHdr(tenant))
			if r.code != tc.want {
				t.Errorf("%s = %d %s, want %d", tc.name, r.code, r.raw, tc.want)
			}
		})
	}
}

// A superadmin holds every right without any membership, and the read says so instead of
// answering 404 about the account with the broadest standing entitlement.
func TestEffectiveRightsReportASuperadminSubjectHoldingEverything(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	agent := h.mkAgent(admin, tenant, "worker")
	who := h.do("GET", "/v1/auth/whoami", admin, nil, nil)
	rootID, _ := who.body["user_id"].(string)
	if who.code != http.StatusOK || rootID == "" {
		t.Fatalf("whoami = %d %s, want the superadmin's user id", who.code, who.raw)
	}
	r := h.do("GET", effectiveRightsURL(rootID, "agent", agent), admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("superadmin subject = %d %s, want 200", r.code, r.raw)
	}
	_, state := rightStates(t, r)
	for _, name := range allRights {
		if state[name] != "held" {
			t.Errorf("superadmin: %s = %q, want held", name, state[name])
		}
	}
}
