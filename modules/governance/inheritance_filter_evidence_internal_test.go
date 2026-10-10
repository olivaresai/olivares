// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"errors"
	"testing"
	"time"

	cedar "github.com/cedar-policy/cedar-go"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
)

// The typed-evidence path (governed routes) and the retained replay implement the same
// algebra as Authorize. A filter that only Authorize honored would let a governed route admit
// by role what the plain path refuses, and let a replay answer allow for a decision that was
// denied. Each case here asks the three for the same request.

// filterNode is where the filter sits and how a Cedar permit anchors at that same node.
type filterNode struct {
	name, tree, ref, anchor string
}

var filterNodes = []filterNode{
	{"workspace node", scopeWorkspace, "payments", `Workspace::"payments"`},
	{"agent group node", scopeAgentGroup, "pay-ops", `AgentGroup::"pay-ops"`},
}

type filterEvidenceCase struct {
	name   string
	source string // Cedar grant policy; "" is a tenant with none
	// coreDenied says the typed core-permission term is established as denied, without and
	// with the filter (a clean term stays unproven in this fixture: its principal carries no
	// authority evidence, so only a denial is established); allow is what the plain path, and
	// so the replay, answers.
	coreDeniedNone, coreDeniedFilter bool
	allowNone, allowFilter           bool
}

func filterEvidenceCases(n filterNode) []filterEvidenceCase {
	atNode := `permit(principal in Role::"editor", action == Action::"agent:write", resource) when { resource in ` + n.anchor + ` };`
	forbidAtNode := `forbid(principal, action == Action::"agent:write", resource) when { resource in ` + n.anchor + ` };`
	tenantWide := `permit(principal in Role::"editor", action == Action::"agent:write", resource);`
	cases := []filterEvidenceCase{
		{"no grant policy: the editor's role is inherited", "", false, true, true, false},
		{"tenant-wide grant is inherited", tenantWide, false, true, true, false},
		{"excluding the probe is inherited", `permit(principal, action, resource) unless { resource == Resource::"inheritance-filter:no-lineage" };`, false, true, true, false},
		{"resource inequality is inherited", `permit(principal, action, resource) when { resource != Resource::"inheritance-filter:no-lineage" };`, false, true, true, false},
		{"conditional probe exclusion is inherited", `permit(principal, action, resource) when { if resource == Resource::"inheritance-filter:no-lineage" then false else true };`, false, true, true, false},
		{"unanchored disjunction is inherited", `permit(principal, action, resource) when { resource in ` + n.anchor + ` || resource != Resource::"inheritance-filter:no-lineage" };`, false, true, true, false},
		{"negated higher anchor is inherited", `permit(principal, action, resource) unless { resource in Workspace::"other" || resource == Resource::"inheritance-filter:no-lineage" };`, false, true, true, false},
		{"positive anchor with probe exclusion stands", atNode[:len(atNode)-1] + ` unless { resource == Resource::"inheritance-filter:no-lineage" };`, false, false, true, true},
		{"resource head anchor stands", `permit(principal, action, resource in ` + n.anchor + `);`, false, false, true, true},
		{"grant at the filtered node stands", atNode, false, false, true, true},
		{"forbid at the node stays a deny", atNode + "\n" + forbidAtNode, false, true, false, false},
	}
	if n.tree == scopeAgentGroup {
		cases = append(cases, filterEvidenceCase{
			"cut graph must not switch from a higher branch to a lower branch",
			`permit(principal, action, resource) when { resource in Workspace::"payments" || (resource in ` + n.anchor + ` && !(resource in Workspace::"payments")) };`,
			false, true, true, false,
		})
	}
	return cases

}

// filterEvidenceWorld seeds the world and returns the request an editor would make.
func filterEvidenceWorld(t *testing.T, f *typedEvidenceFixture, filtered bool, n filterNode) (auth.Request, model.Agent) {
	t.Helper()
	return filterEvidenceRequest(t, f, filtered, n, f.confinedPrincipal(t, ""))
}

// filterEvidenceRequest seeds workspace payments, an agent in it and in agent group pay-ops,
// and (when filtered) the filter row on the node, then returns p's write request on that agent.
func filterEvidenceRequest(t *testing.T, f *typedEvidenceFixture, filtered bool, n filterNode, p auth.Principal) (auth.Request, model.Agent) {
	t.Helper()
	var agent model.Agent
	if err := f.st.Mutate(t.Context(), f.tenant, func(sc store.Scope) error {
		ws, err := sc.Workspaces().Create(t.Context(), model.Workspace{Name: "payments", Slug: "payments", Status: model.StatusActive})
		if err != nil {
			return err
		}
		agent, err = sc.Agents().Create(t.Context(), model.Agent{Name: "pay-bot", Kind: "test", Status: model.StatusActive, WorkspaceID: ws.ID})
		if err != nil {
			return err
		}
		grp, err := sc.AgentGroups().Create(t.Context(), model.AgentGroup{Name: "pay-ops", Slug: "pay-ops", Status: model.StatusActive, WorkspaceID: ws.ID})
		if err != nil {
			return err
		}
		if _, err = sc.AgentGroupMembers().Create(t.Context(), model.AgentGroupMember{GroupID: grp.ID, AgentID: agent.ID}); err != nil || !filtered {
			return err
		}
		repo, err := sc.Ext(inheritanceFilterKind)
		if err != nil {
			return err
		}
		_, err = repo.Create(t.Context(), inheritanceFilterRecord(scopeSpec{Tree: n.tree, Ref: n.ref, Class: "agent"}, "test"))
		return err
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	req := typedEvidenceRequest(f.tenant)
	req.Principal = p
	req.Permission = "agent:write"
	req.Resource = auth.ResourceAttrs{Kind: "agent", ID: agent.ID.String()}
	return req, agent
}

func filterEvidenceAuthorizer(engine *scopedEngine) *auth.Authorizer {
	return auth.NewAuthorizer(nil, auth.WithScopedGrants(engine), auth.WithClock(func() time.Time { return typedEvidenceNow.Add(time.Second) }))
}

// The typed path (governed routes) answers the same core-permission question as Authorize:
// under a filter the editor's role and a tenant-wide grant no longer carry the request, and
// a grant at the node still does. Without the filter every case is unchanged.
func TestInheritanceFilterTypedEvidenceHonorsTheFilter(t *testing.T) {
	for _, n := range filterNodes {
		for _, tc := range filterEvidenceCases(n) {
			t.Run(n.name+"/"+tc.name, func(t *testing.T) {
				ctx := typedEvidenceContext(t, typedEvidenceNow.Add(time.Hour))

				plain := newTypedEvidenceFixture(t)
				req, _ := filterEvidenceWorld(t, plain, false, n)
				engine, _ := typedEvidenceScopedEngine(t, plain, plain.data(typedEvidenceNow, nil), tc.source, 0, FreshnessRecord{})
				az := filterEvidenceAuthorizer(engine)
				if got := az.AuthorizeEvidence(ctx, req).CorePermission.Verdict == auth.CheckBroken; got != tc.coreDeniedNone {
					t.Fatalf("no filter: typed core permission denied=%v, want %v", got, tc.coreDeniedNone)
				}
				if got := az.Authorize(ctx, req).Allow; got != tc.allowNone {
					t.Fatalf("no filter: Authorize allow=%v, want %v", got, tc.allowNone)
				}

				filtered := newTypedEvidenceFixture(t)
				freq, _ := filterEvidenceWorld(t, filtered, true, n)
				fengine, _ := typedEvidenceScopedEngine(t, filtered, filtered.data(typedEvidenceNow, nil), tc.source, 0, FreshnessRecord{})
				faz := filterEvidenceAuthorizer(fengine)
				ev := faz.AuthorizeEvidence(ctx, freq)
				if got := ev.CorePermission.Verdict == auth.CheckBroken; got != tc.coreDeniedFilter {
					t.Errorf("with the filter: typed core permission denied=%v (%s), want %v", got, ev.CorePermission.Code, tc.coreDeniedFilter)
				}
				if !tc.allowFilter && ev.Outcome == auth.EvidenceAllow {
					t.Errorf("with the filter: the typed path allowed what Authorize refuses")
				}
				if got := faz.Authorize(ctx, freq).Allow; got != tc.allowFilter {
					t.Errorf("with the filter: Authorize allow=%v, want %v", got, tc.allowFilter)
				}
			})
		}
	}
}

// A stored decision replays to the answer it gave live, from its retained inputs alone: the
// retained Cedar inputs carry the marked nodes, and the replay classifies the permits the
// way the live decision did.
func TestInheritanceFilterRetainedReplayMatchesTheLiveDecision(t *testing.T) {
	for _, n := range filterNodes {
		for _, tc := range filterEvidenceCases(n) {
			t.Run(n.name+"/"+tc.name, func(t *testing.T) {
				f := newTypedEvidenceFixture(t)
				req, _ := filterEvidenceWorld(t, f, true, n)
				engine, _ := typedEvidenceScopedEngine(t, f, f.data(typedEvidenceNow, nil), tc.source, 0, FreshnessRecord{})
				var records []auth.AuthorizationRecord
				ctx := auth.WithAuthorizationRecording(typedEvidenceContext(t, typedEvidenceNow.Add(time.Hour)), func(r auth.AuthorizationRecord) { records = append(records, r) })
				if got := filterEvidenceAuthorizer(engine).Authorize(ctx, req).Allow; got != tc.allowFilter {
					t.Errorf("live Authorize allow=%v, want %v", got, tc.allowFilter)
				}
				replayMatches(t, f, records, req, tc.allowFilter, true)
			})
		}
	}
}

// Collection requests share Resource::"*" across workspaces. Selecting that UID
// alone is inherited; additionally requiring a real workspace anchor is explicit.
func TestInheritanceFilterCollectionAnchorsAcrossDecisionPaths(t *testing.T) {
	cases := []struct {
		name, constraint string
		allowFiltered    bool
	}{
		{"head equality", `resource == Resource::"*")`, false},
		{"head membership", `resource in Resource::"*")`, false},
		{"head typed membership", `resource is Resource in Resource::"*")`, false},
		{"when equality", `resource) when { resource == Resource::"*" }`, false},
		{"when reversed equality", `resource) when { Resource::"*" == resource }`, false},
		{"when membership", `resource) when { resource in Resource::"*" }`, false},
		{"when typed membership", `resource) when { resource is Resource in Resource::"*" }`, false},
		{"head plus workspace", `resource == Resource::"*") when { resource in Workspace::"payments" }`, true},
		{"when plus workspace", `resource) when { resource == Resource::"*" && resource in Workspace::"payments" }`, true},
		{"collection or workspace", `resource) when { resource == Resource::"*" || resource in Workspace::"payments" }`, false},
	}
	for _, tc := range cases {
		for _, filtered := range []bool{false, true} {
			phase := "unfiltered"
			if filtered {
				phase = "filtered"
			}
			t.Run(tc.name+"/"+phase, func(t *testing.T) {
				f := newTypedEvidenceFixture(t)
				req, agent := filterEvidenceWorld(t, f, filtered, filterNodes[0])
				req.Resource = auth.ResourceAttrs{Kind: "agent", WorkspaceID: agent.WorkspaceID}
				source := `permit(principal, action == Action::"agent:write", ` + tc.constraint + ";"
				engine, _ := typedEvidenceScopedEngine(t, f, f.data(typedEvidenceNow, nil), source, 0, FreshnessRecord{})
				az := filterEvidenceAuthorizer(engine)
				want := !filtered || tc.allowFiltered
				var records []auth.AuthorizationRecord
				ctx := typedEvidenceContext(t, typedEvidenceNow.Add(time.Hour))
				liveCtx := auth.WithAuthorizationRecording(ctx, func(r auth.AuthorizationRecord) { records = append(records, r) })
				if d := az.Authorize(liveCtx, req); d.Allow != want {
					t.Errorf("live collection allow=%v (%s), want %v", d.Allow, d.Reason, want)
				}
				if ev := az.AuthorizeEvidence(ctx, req); (ev.CorePermission.Verdict == auth.CheckBroken) != !want {
					t.Errorf("typed collection permission=%v (%s), want denied=%v", ev.CorePermission.Verdict, ev.CorePermission.Code, !want)
				}
				replayMatches(t, f, records, req, want, filtered)
			})
		}
	}
}

// replayMatches records one retained decision and reconstructs it from the stored inputs alone.
func replayMatches(t *testing.T, f *typedEvidenceFixture, records []auth.AuthorizationRecord, req auth.Request, wantAllow, filtered bool) {
	t.Helper()
	if len(records) != 1 || !records[0].Snapshot.Complete {
		t.Fatalf("want one complete record, got %d (complete=%v)", len(records), len(records) == 1 && records[0].Snapshot.Complete)
	}
	// A record an inheritance filter shaped carries the v2 engine tag, so a reader that predates
	// the filter reports it unsupported instead of replaying it unfiltered.
	if got := records[0].Snapshot.Scoped; filtered && (len(got) != 1 || got[0].Engine != retainedCedarScopeV2) {
		t.Fatalf("scoped inputs = %+v, want one %s", got, retainedCedarScopeV2)
	}
	writer := New()
	writer.UseData(api.NewModuleData(f.st))
	if err := writer.RecordAuthorization(t.Context(), records[0]); err != nil {
		t.Fatal(err)
	}
	reader := New()
	reader.UseData(api.NewModuleData(f.st))
	got, err := reader.Reconstruct(t.Context(), f.tenant, ReconstructRequest{
		At: typedEvidenceNow.Add(2 * time.Second), Principal: req.Principal.UserID.String(), Resource: req.Resource.ID, ResourceKind: req.Resource.Kind,
		Action: string(req.Permission), SourceInstance: "olivares", ActionVocabulary: "olivares.permission.v1",
	})
	want := sdk.AccessOutcomeDeny
	if wantAllow {
		want = sdk.AccessOutcomeAllow
	}
	if err != nil || got.Status != ReconstructReconstructed || got.Outcome != want || got.UsedLivePolicy {
		t.Fatalf("replay = %+v, %v; want reconstructed %v from retained inputs", got, err, want)
	}
}

// ownerPrincipal is a real user holding the owner role, authenticated the way the request path
// does. Its fixture must not also call confinedPrincipal: the evidence superadmin is bootstrapped once.
func ownerPrincipal(t *testing.T, f *typedEvidenceFixture) auth.Principal {
	t.Helper()
	ctx := t.Context()
	authn := auth.NewAuthenticator(f.st, nil)
	if _, err := authn.BootstrapSuperadmin(ctx, "typed-evidence-admin@example.test", "strong-password-1"); err != nil {
		t.Fatalf("bootstrap evidence superadmin: %v", err)
	}
	token, _, err := authn.Login(ctx, "typed-evidence-admin@example.test", "strong-password-1", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := authn.Authenticate(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	user, err := authn.CreateUser(ctx, admin, auth.NewUser{
		Email: "typed-evidence-owner@example.test", DisplayName: "Owner", Password: "strong-password-2",
		Tenant: f.tenant, Role: auth.RoleOwner,
	})
	if err != nil {
		t.Fatal(err)
	}
	p, found, err := authn.PrincipalForUser(ctx, user.ID.String(), auth.AAL3)
	if err != nil || !found {
		t.Fatalf("owner principal = found:%v err:%v", found, err)
	}
	return p
}

// The owner's implicit scoped grant (a RequireScopedGrant route) is a tenant-wide right, so a
// filter removes it from all three copies of the algebra: Authorize, the typed path and the replay.
func TestInheritanceFilterRemovesTheOwnerImplicitGrantEverywhere(t *testing.T) {
	n := filterNodes[0]
	route := auth.RouteMetadata{RequireScopedGrant: true}
	for _, filtered := range []bool{false, true} {
		t.Run(map[bool]string{false: "no filter", true: "filter"}[filtered], func(t *testing.T) {
			f := newTypedEvidenceFixture(t)
			req, _ := filterEvidenceRequest(t, f, filtered, n, ownerPrincipal(t, f))
			req.Route = route
			engine, _ := typedEvidenceScopedEngine(t, f, f.data(typedEvidenceNow, nil), "", 0, FreshnessRecord{})
			az := filterEvidenceAuthorizer(engine)
			var records []auth.AuthorizationRecord
			ctx := auth.WithAuthorizationRecording(typedEvidenceContext(t, typedEvidenceNow.Add(time.Hour)), func(r auth.AuthorizationRecord) { records = append(records, r) })
			live := az.Authorize(ctx, req).Allow
			if live == filtered {
				t.Fatalf("Authorize allow=%v with filter=%v", live, filtered)
			}
			if denied := az.AuthorizeEvidence(ctx, req).CorePermission.Verdict == auth.CheckBroken; denied != filtered {
				t.Errorf("typed core permission denied=%v with filter=%v", denied, filtered)
			}
			// AuthorizeEvidence recorded too; keep the first (Authorize) record for the replay.
			replayMatches(t, f, records[:1], req, !filtered, filtered)
		})
	}
}

// failingFilterScope fails the one read the filters need, so the test can ask what an
// unreadable restriction does: it must never read as "no filter".
type failingFilterScope struct{ typedEvidenceScope }

func (s failingFilterScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	if kind == inheritanceFilterKind {
		return nil, errors.New("filter table unavailable")
	}
	return s.typedEvidenceScope.Ext(kind)
}

func TestInheritanceFilterReadFailureFailsClosed(t *testing.T) {
	for _, source := range []string{"", `permit(principal in Role::"editor", action == Action::"agent:write", resource);`} {
		name := "no grant policy"
		if source != "" {
			name = "grant policy"
		}
		t.Run(name, func(t *testing.T) {
			f := newTypedEvidenceFixture(t)
			req, _ := filterEvidenceWorld(t, f, false, filterNodes[0])
			data := f.data(typedEvidenceNow, nil)
			inner := data.wrap
			data.wrap = func(sc store.Scope) store.Scope { return failingFilterScope{inner(sc).(typedEvidenceScope)} }
			engine, _ := typedEvidenceScopedEngine(t, f, data, source, 0, FreshnessRecord{})
			az := filterEvidenceAuthorizer(engine)
			ctx := typedEvidenceContext(t, typedEvidenceNow.Add(time.Hour))
			if d := az.Authorize(ctx, req); d.Allow {
				t.Errorf("an unreadable filter must deny, the editor's role must not carry it: %s", d.Reason)
			}
			if out := az.AuthorizeEvidence(ctx, req).Outcome; out == auth.EvidenceAllow {
				t.Error("an unreadable filter must not produce a typed allow")
			}
		})
	}
}

// A row names no node when its tree is not a container tree or its ref or class is empty (a
// missing column reads empty), so such a row can never filter anything.
func TestInheritanceFilterNodeIgnoresRowsThatNameNoNode(t *testing.T) {
	for _, f := range []inheritanceFilterDTO{
		{ScopeTree: "", ScopeRef: "payments", ScopeClass: "agent"},
		{ScopeTree: scopeTenant, ScopeRef: "payments", ScopeClass: "agent"},
		{ScopeTree: scopeWorkspace, ScopeRef: "", ScopeClass: "agent"},
		{ScopeTree: scopeWorkspace, ScopeRef: "payments", ScopeClass: ""},
	} {
		if _, ok := inheritanceFilterNode(f); ok {
			t.Errorf("%+v must name no node", f)
		}
	}
	if _, ok := inheritanceFilterNode(inheritanceFilterDTO{ScopeTree: scopeFolder, ScopeRef: "01J0", ScopeClass: "resource"}); !ok {
		t.Error("a folder row with a ref and a class names a node")
	}
}

// A permit that errors on the lineage-free probe but not on the real resource is not shown to be
// anchored at or below the node: the doubt reads as "from above". The policy errors on the probe
// only because `resource.missing` is reached when the first operand is false, and the real
// resource satisfies that operand.
func TestGrantAtOrBelowTreatsAProbeErrorAsInherited(t *testing.T) {
	set, err := cedar.NewPolicySetFromBytes("probe.cedar", []byte(
		`permit(principal, action, resource) when { (resource == Resource::"real" || resource.missing == 1) && resource in AgentGroup::"g" };`))
	if err != nil {
		t.Fatal(err)
	}
	ws := cedar.NewEntityUID(cedarTypeWorkspace, "w")
	grp := cedar.NewEntityUID(cedarTypeAgentGroup, "g")
	res := cedar.NewEntityUID(cedarTypeResource, "real")
	princ := cedar.NewEntityUID(cedarTypePrincipal, "p")
	em := cedar.EntityMap{
		ws:    {UID: ws},
		grp:   {UID: grp, Parents: cedar.NewEntityUIDSet(ws)},
		res:   {UID: res, Parents: cedar.NewEntityUIDSet(grp, ws)},
		princ: {UID: princ},
	}
	creq := cedar.Request{Principal: princ, Action: cedar.NewEntityUID("Action", "agent:write"), Resource: res}
	if _, diag := cedar.Authorize(set, em, creq); len(diag.Reasons) != 1 {
		t.Fatalf("setup: the permit must carry the real request, got %+v", diag)
	}
	if grantAtOrBelow(set, em, creq, []cedar.EntityUID{ws}) {
		t.Error("a permit that errors on the lineage-free probe must not count as anchored below the node")
	}
}

// An engine that cannot read the filters (no resolver) must refuse, never abstain as if there
// were none, whether or not the tenant has a grant policy.
func TestScopedWithoutAResolverFailsClosed(t *testing.T) {
	e := &scopedEngine{}
	_, err := e.Scoped(t.Context(), auth.Request{
		Tenant: model.TenantID(model.NewID().String()), Permission: "agent:write",
		Resource: auth.ResourceAttrs{Kind: "agent", ID: model.NewID().String()},
	})
	if err == nil {
		t.Fatal("with no resolver the filters are unreadable: Scoped must return an error, not abstain")
	}
}
