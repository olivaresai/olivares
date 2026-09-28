// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/modules/sourcescope"
)

// publishCedar publishes a Cedar policy source for tenant.
func (e *consentEstate) publishCedar(tenant model.TenantID, source string) {
	e.t.Helper()
	r := e.do("POST", "/v1/m/governance/pdp/publish", e.admin, tenant, map[string]any{"engine": "cedar", "source": source})
	if r.code != http.StatusOK {
		e.t.Fatalf("publish cedar = %d %s", r.code, r.raw)
	}
}

// blockingRefs returns the ids a retirement record lists as blocking it.
func blockingRefs(rec model.TenantExclusion) string { return rec.BlockingRefs }

// wantBlocked fails unless the account's record in tenant is blocked and lists
// every fragment.
func (e *consentEstate) wantBlocked(t *testing.T, user model.ID, tenant model.TenantID, fragments ...string) {
	t.Helper()
	rec, found := e.record(user, tenant)
	if !found || rec.RetirementState != model.RetirementBlocked {
		t.Errorf("the retirement record = %+v (found %t), want blocked", rec, found)
		return
	}
	for _, f := range fragments {
		if !strings.Contains(blockingRefs(rec), f) {
			t.Errorf("the blocked record lists %q, want it to name %q", blockingRefs(rec), f)
		}
	}
}

// wantRetired fails unless the account's record in tenant is retired.
func (e *consentEstate) wantRetired(t *testing.T, user model.ID, tenant model.TenantID) {
	t.Helper()
	if rec, found := e.record(user, tenant); !found || rec.RetirementState != model.RetirementRetired {
		t.Errorf("the retirement record = %+v (found %t), want retired", rec, found)
	}
}

// TestAnAuthoredCedarGrantBlocksReadmissionUntilRevised: an authored permit that
// names the account blocks its retirement until the tenant revises the policy.
func TestAnAuthoredCedarGrantBlocksReadmissionUntilRevised(t *testing.T) {
	onConsentEngines(t, func(t *testing.T, e *consentEstate) {
		const email = "cedar-permit@consent.test"
		user := e.onboard(e.tT, email, "viewer")
		e.publishCedar(e.tT, `permit(principal in User::"`+user.String()+`", action, resource);`)
		e.scimDelete(e.tT, user)
		e.runPump()
		e.wantBlocked(t, user, e.tT, "policy")
		if r := e.readmit(e.tT, email, "viewer"); r.code != http.StatusConflict {
			t.Errorf("re-admission while an authored permit names the account = %d %s, want 409", r.code, r.raw)
		}
		if sess, code := e.tryLogin(email, consentMemberPassword); code == http.StatusOK {
			if got := e.actsIn(sess, e.tT); got != http.StatusForbidden {
				t.Errorf("the blocked account acting through the permit = %d, want 403", got)
			}
		}
		e.publishCedar(e.tT, `permit(principal in Role::"viewer", action == Action::"agent:read", resource);`)
		e.runPump()
		e.wantRetired(t, user, e.tT)
		if r := e.readmit(e.tT, email, "viewer"); r.code != http.StatusCreated {
			t.Errorf("re-admission after the revision = %d %s, want 201", r.code, r.raw)
		}
		sess, code := e.tryLogin(email, consentMemberPassword)
		if code != http.StatusOK {
			t.Fatalf("the re-admitted account cannot sign in: %d", code)
		}
		if got := e.do("POST", "/v1/agents", sess, e.tT, map[string]any{"name": "permit-agent", "kind": "claude-code"}).code; got != http.StatusForbidden {
			t.Errorf("a write the revoked permit allowed = %d, want 403", got)
		}
	})
}

// TestAStaleSnapshotCannotHonorARetiredUsersGrant: a process whose grant
// snapshot predates the retirement abstains for the re-admitted account instead
// of honoring the grant the retirement removed.
func TestAStaleSnapshotCannotHonorARetiredUsersGrant(t *testing.T) {
	onConsentEngines(t, func(t *testing.T, e *consentEstate) {
		ctx := context.Background()
		const email = "stale-snapshot@consent.test"
		user := e.onboard(e.tT, email, "viewer")
		e.grantUser(e.tT, user, "editor")

		stale := governance.New()
		stale.UseData(api.NewModuleData(e.eng.store))
		if err := stale.ReloadActivePDP(ctx, e.tT); err != nil {
			t.Fatalf("load the stale snapshot: %v", err)
		}
		staleAuthz := auth.NewAuthorizer(nil, auth.WithScopedGrants(stale.ScopedGrants()))

		e.scimDelete(e.tT, user)
		e.runPump()
		if r := e.readmit(e.tT, email, "viewer"); r.code != http.StatusCreated {
			t.Fatalf("re-admission = %d %s", r.code, r.raw)
		}
		sess, code := e.tryLogin(email, consentMemberPassword)
		if code != http.StatusOK {
			t.Fatalf("sign in: %d", code)
		}
		p, err := e.eng.authr.Authenticate(ctx, sess)
		if err != nil {
			t.Fatal(err)
		}
		if staleAuthz.Allowed(ctx, p, "agent:write", e.tT) {
			t.Errorf("a snapshot older than the retirement honored the removed grant")
		}
		if err := stale.ReloadActivePDP(ctx, e.tT); err != nil {
			t.Fatalf("reload: %v", err)
		}
		if staleAuthz.Allowed(ctx, p, "agent:write", e.tT) {
			t.Errorf("the reloaded snapshot honored the removed grant")
		}
	})
}

// resolveSourceAs resolves an MCP source for email's own session principal in
// tenant, through the source-scope resolver over the composed store.
func (e *consentEstate) resolveSourceAs(tenant model.TenantID, email, source string) sourcescope.Decision {
	e.t.Helper()
	ctx := context.Background()
	p, err := e.eng.authr.Authenticate(ctx, e.login(email, consentMemberPassword))
	if err != nil {
		e.t.Fatalf("authenticate %s: %v", email, err)
	}
	resolver := sourcescope.New()
	resolver.UseData(api.NewModuleData(e.eng.store))
	dec, err := resolver.Resolver().ResolveForSession(ctx, tenant, p, "", "mcp", source)
	if err != nil {
		e.t.Fatalf("resolve %s: %v", source, err)
	}
	return dec
}

// credName is a decision's credential name, or "" for none.
func credName(dec sourcescope.Decision) string {
	if dec.Cred == nil {
		return ""
	}
	return dec.Cred.Name
}

// TestASourceScopeBindingDoesNotSurviveALift: the retirement deletes the
// account's user bindings, so a re-admitted account reaches the source only
// through the unbound or tenant-wide path and never with the removed binding's
// credential; and while it runs no binding naming the account can be created or
// approved.
func TestASourceScopeBindingDoesNotSurviveALift(t *testing.T) {
	onConsentEngines(t, func(t *testing.T, e *consentEstate) {
		const email = "binding@consent.test"
		user := e.onboard(e.tT, email, "viewer")
		other := e.onboard(e.tT, "binding-other@consent.test", "viewer")
		bind := func(subject model.ID, source, cred string) consentResp {
			return e.do("POST", "/v1/m/sourcescope/bindings", e.admin, e.tT, map[string]any{
				"source_type": "mcp", "source_ref": source, "scope_tree": "user", "scope_ref": subject.String(),
				"cred_name": cred, "cred_ref_kind": "env", "cred_ref": "SOURCE_" + cred, "enabled": true,
			})
		}
		// S's only binding is the account's, with credential C.
		first := bind(user, "binding-S", "C")
		if first.code != http.StatusCreated {
			t.Fatalf("bind = %d %s", first.code, first.raw)
		}
		bindingID, _ := first.body["id"].(string)
		// V is bound to the account with C, and to a named workspace other than the
		// default with C2.
		if r := bind(user, "binding-V", "C"); r.code != http.StatusCreated {
			t.Fatalf("bind V = %d %s", r.code, r.raw)
		}
		e.seedFenced(e.tT, "sourcescope.binding", model.Record{
			"source_type": "mcp", "source_ref": "binding-V", "scope_tree": "workspace", "scope_ref": "binding-w2",
			"cred_name": "C2", "cred_ref_kind": "env", "cred_ref": "SOURCE_C2", "enabled": true, "created_by": "test",
		})
		// A second allow on another source proposes a posture change that names the account.
		if r := bind(other, "binding-P", "C"); r.code != http.StatusCreated {
			t.Fatalf("bind other = %d %s", r.code, r.raw)
		}
		proposal := bind(user, "binding-P", "C")
		if proposal.code != http.StatusAccepted {
			t.Fatalf("a relaxing binding = %d %s, want 202 with a posture request", proposal.code, proposal.raw)
		}
		requestID, _ := proposal.body["id"].(string)

		e.scimDelete(e.tT, user)
		if r := bind(user, "binding-S2", "C"); r.code != http.StatusConflict || r.errorCode() != "subject_retirement_active" {
			t.Errorf("a binding naming the account while it retires = %d %s, want 409 subject_retirement_active", r.code, r.raw)
		}
		e.onboard(e.tT, "binding-decider@consent.test", "admin")
		deciderSess := e.login("binding-decider@consent.test", consentMemberPassword)
		if r := e.do("POST", "/v1/m/sourcescope/posture-requests/"+requestID+"/approve", deciderSess, e.tT, map[string]any{}); r.code != http.StatusConflict {
			t.Errorf("approving a posture request naming the account while it retires = %d %s, want 409", r.code, r.raw)
		}

		e.runPump()
		if g := e.do("GET", "/v1/m/sourcescope/bindings/"+bindingID, e.admin, e.tT, nil); g.code != http.StatusNotFound {
			t.Errorf("the account's user binding after the retirement = %d, want 404", g.code)
		}
		e.wantRetired(t, user, e.tT)
		if r := e.readmit(e.tT, email, "viewer"); r.code != http.StatusCreated {
			t.Fatalf("re-admission at viewer = %d %s", r.code, r.raw)
		}
		if dec := e.resolveSourceAs(e.tT, email, "binding-S"); !dec.Allowed || dec.Bound || dec.Cred != nil {
			t.Errorf("the re-admitted account resolving S = %+v, want allowed, unbound, with no credential", dec)
		}
		if dec := e.resolveSourceAs(e.tT, email, "binding-V"); !dec.Allowed || credName(dec) == "C" || credName(dec) != "C2" {
			t.Errorf("the re-admitted account resolving V = %+v (credential %q), want tenant-wide with C2, never C", dec, credName(dec))
		}

		t.Run("control: with the step skipped the binding survives", func(t *testing.T) {
			const kept = "binding-control@consent.test"
			subject := e.onboard(e.tT, kept, "viewer")
			if r := bind(subject, "binding-K", "C"); r.code != http.StatusCreated {
				t.Fatalf("bind K = %d %s", r.code, r.raw)
			}
			e.scimDelete(e.tT, subject)
			// The sourcescope step still answers, and removes nothing: a missing step
			// would leave the retirement incomplete instead.
			var skipped []declaredModule
			for _, d := range e.pump().modules {
				if d.name == "sourcescope" {
					d.step = inertStep{module: d.name}
				}
				skipped = append(skipped, d)
			}
			worker := &retirementPump{authr: e.pump().authr, modules: skipped}
			advancePumpClock(worker)
			for i := 0; i < 8; i++ {
				n, err := worker.runOnce(context.Background())
				if err != nil {
					t.Fatalf("retirement pump: %v", err)
				}
				if n == 0 {
					break
				}
			}
			if r := e.readmit(e.tT, kept, "viewer"); r.code != http.StatusCreated {
				t.Fatalf("re-admission = %d %s", r.code, r.raw)
			}
			if dec := e.resolveSourceAs(e.tT, kept, "binding-K"); !dec.Allowed || !dec.Bound || credName(dec) != "C" {
				t.Errorf("without the step the account resolves K = %+v, want the containment path with C", dec)
			}
		})
	})
}

// rosterProvider is a fixed identity-source snapshot.
type rosterProvider struct{ graph identitysource.Graph }

func (p rosterProvider) Snapshot(context.Context) (identitysource.Graph, error) { return p.graph, nil }

// sponsoredAgentRoster is an agent registry that declares sponsor as agent's
// sponsor.
func sponsoredAgentRoster(agent, sponsor string) identitysource.Graph {
	return identitysource.Graph{Source: identitysource.SourceEntraAgent, Identities: []identitysource.Identity{{
		Ref: agent, Type: identitysource.PrincipalNHI, Kind: identitysource.KindAgentIdentity,
		DisplayName: agent, Source: identitysource.SourceEntraAgent,
		Attributes: map[string]string{"sponsor_ref": sponsor},
	}}}
}

// bindRoster makes the composition's governance module sync graph for tenant.
func (e *consentEstate) bindRoster(tenant model.TenantID, graph identitysource.Graph) {
	e.t.Helper()
	gov, ok := e.eng.nhiEnforcer.(*governance.Module)
	if !ok {
		e.t.Fatal("the composition's governance module is not reachable")
	}
	gov.UseRosterProviders([]governance.RosterBinding{{Provider: rosterProvider{graph: graph}, TenantRef: tenant.String()}})
}

// TestNHISponsorshipBlocksReadmissionUntilReassigned: an agent the account
// sponsors blocks its retirement until the tenant reassigns the sponsor, and no
// ownership naming the account can be written while it retires, by a route or by
// a roster sync, even one that read the account's standing before the offboard.
func TestNHISponsorshipBlocksReadmissionUntilReassigned(t *testing.T) {
	onConsentEngines(t, func(t *testing.T, e *consentEstate) {
		const email = "sponsor@consent.test"
		c := e.scimCreateExternal(e.tT, email, "ext-sponsor")
		user := model.ID(c)
		e.rosterHuman(e.tT, "ext-sponsor")
		e.rosterHuman(e.tT, "ext-sponsor-heir")
		if r := e.do("POST", "/v1/m/governance/agents", e.admin, e.tT, map[string]any{
			"identity_ref": "agent-sponsor", "sponsor_ref": "ext-sponsor",
		}); r.code != http.StatusCreated && r.code != http.StatusOK {
			t.Fatalf("register agent = %d %s", r.code, r.raw)
		}
		// A roster sync whose registry names the account as a sponsor, parked
		// between its standing read and its barrier while the offboard commits.
		e.bindRoster(e.tT, sponsoredAgentRoster("agent-sponsor-fed", "ext-sponsor"))
		parked := make(chan struct{})
		release := make(chan struct{})
		var once sync.Once
		if e.eng.standing == nil {
			t.Fatal("the composition has no standing port")
		}
		e.eng.standing.afterReadHook = func(tenant model.TenantID, users []model.ID) {
			for _, u := range users {
				if u == user {
					once.Do(func() {
						close(parked)
						<-release
					})
				}
			}
		}
		result := make(chan consentResp, 1)
		go func() { result <- e.do("POST", "/v1/m/governance/roster/sync", e.admin, e.tT, nil) }()
		select {
		case <-parked:
		case r := <-result:
			t.Fatalf("the roster writer never read the sponsor's standing: %d %s", r.code, r.raw)
		case <-time.After(10 * time.Second):
			t.Fatal("the roster writer never read the sponsor's standing")
		}
		e.scimDelete(e.tT, user)
		close(release)
		if r := <-result; r.code != http.StatusConflict || r.errorCode() != "subject_retirement_active" {
			t.Errorf("the parked roster sync after the offboard = %d %s, want 409 subject_retirement_active", r.code, r.raw)
		}
		e.eng.standing.afterReadHook = nil
		if r := e.do("POST", "/v1/m/governance/roster/sync", e.admin, e.tT, nil); r.code != http.StatusConflict || r.errorCode() != "subject_retirement_active" {
			t.Errorf("a roster sync naming the account while it retires = %d %s, want 409 subject_retirement_active", r.code, r.raw)
		}
		if r := e.do("PUT", "/v1/m/governance/nhi/agent-sponsor-b/ownership", e.admin, e.tT, map[string]any{
			"owner_ref": "ext-sponsor",
		}); r.code != http.StatusConflict {
			t.Errorf("ownership naming the account while it retires = %d %s, want 409", r.code, r.raw)
		}
		e.runPump()
		e.wantBlocked(t, user, e.tT, "agent-sponsor")
		if r := e.readmit(e.tT, email, "viewer"); r.code != http.StatusConflict {
			t.Errorf("re-admission while the account sponsors an agent = %d %s, want 409", r.code, r.raw)
		}
		if r := e.do("PUT", "/v1/m/governance/nhi/agent-sponsor/ownership", e.admin, e.tT, map[string]any{
			"sponsor_ref": "ext-sponsor-heir",
		}); r.code != http.StatusNoContent {
			t.Fatalf("reassign the sponsor = %d %s", r.code, r.raw)
		}
		e.runPump()
		e.wantRetired(t, user, e.tT)
	})
}

// scimCreateExternal provisions an address with an external id through the
// tenant's SCIM connection and returns the account id.
func (e *consentEstate) scimCreateExternal(tenant model.TenantID, email, externalID string) string {
	e.t.Helper()
	r := e.scim("POST", "/v1/scim/v2/Users", e.scimToken(tenant),
		`{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":"`+email+`","externalId":"`+externalID+`","active":true}`)
	if r.code != http.StatusCreated {
		e.t.Fatalf("SCIM create %s = %d %s", email, r.code, r.raw)
	}
	id, _ := r.body["id"].(string)
	return id
}

// rosterHuman writes a human identity with externalID into tenant's roster.
func (e *consentEstate) rosterHuman(tenant model.TenantID, externalID string) {
	e.t.Helper()
	ctx := context.Background()
	if err := e.eng.store.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, err := sc.Identities().Create(ctx, model.Identity{
			Name: externalID, Kind: "human", ExternalID: externalID,
			Metadata: map[string]any{"principal_type": "human"},
		})
		return err
	}); err != nil {
		e.t.Fatalf("roster identity: %v", err)
	}
}

// TestAPermitNamingTheUsersCredentialBlocksRetirement: a permit naming one of
// the account's credentials blocks its retirement like one naming the account.
func TestAPermitNamingTheUsersCredentialBlocksRetirement(t *testing.T) {
	onConsentEngines(t, func(t *testing.T, e *consentEstate) {
		ctx := context.Background()
		const email = "credential-permit@consent.test"
		user := e.onboard(e.tT, email, "viewer")
		sess := e.login(email, consentMemberPassword)
		p, err := e.eng.authr.Authenticate(ctx, sess)
		if err != nil {
			t.Fatal(err)
		}
		e.publishCedar(e.tT, `permit(principal == Principal::"`+p.CredID.String()+`", action, resource);`)
		e.scimDelete(e.tT, user)
		// While it retires, a permit naming one of its credentials is refused like
		// one naming the account.
		if r := e.do("POST", "/v1/m/governance/pdp/publish", e.admin, e.tT, map[string]any{
			"engine": "cedar", "source": `permit(principal == Principal::"` + p.CredID.String() + `", action == Action::"agent:read", resource);`,
		}); r.code != http.StatusConflict || r.errorCode() != "subject_retirement_active" {
			t.Errorf("a permit naming the retiring account's credential = %d %s, want 409 subject_retirement_active", r.code, r.raw)
		}
		e.runPump()
		e.wantBlocked(t, user, e.tT, "policy")
		if r := e.readmit(e.tT, email, "viewer"); r.code != http.StatusConflict || r.errorCode() != "retirement_pending" {
			t.Errorf("re-admission while a permit names the account's credential = %d %s, want 409 retirement_pending", r.code, r.raw)
		}
	})
}

// participantSteps is a workflow graph that names who in one participant role
// only: as the work's owner, as the target of an assignment, or as the
// recipient of a message. Every other participant is other.
func participantSteps(t *testing.T, role string, who, other model.ID) []map[string]any {
	t.Helper()
	owner := other
	if role == "work owner" {
		owner = who
	}
	var steps []map[string]any
	if err := json.Unmarshal([]byte(workCreateSteps(t, owner)), &steps); err != nil {
		t.Fatal(err)
	}
	participant := map[string]any{"kind": "user", "ref": who.String()}
	switch role {
	case "work owner":
	case "assign target":
		steps = append(steps, map[string]any{
			"ref": "assign", "kind": "work-assign", "depends_on": []string{"create"},
			"config": map[string]any{
				"work_item_step_ref": "create", "expected_owner_epoch": 1, "target": participant, "require_ack": false,
			},
		})
	case "message recipient":
		steps = append(steps, map[string]any{
			"ref": "message", "kind": "work-message", "depends_on": []string{"create"},
			"config": map[string]any{
				"work_item_step_ref": "create", "channel_id": model.NewID().String(), "recipient": participant,
				"body": "please review",
			},
		})
	default:
		t.Fatalf("no participant role %q", role)
	}
	return steps
}

// TestAWorkflowNamingTheUserBlocksReadmissionUntilEdited: a workflow whose steps
// name the account, as the work's owner, as an assignment's target or as a
// message's recipient, each alone, blocks its retirement until the tenant edits
// the steps, and no workflow naming it in that role can be created while it
// retires.
func TestAWorkflowNamingTheUserBlocksReadmissionUntilEdited(t *testing.T) {
	onConsentEngines(t, func(t *testing.T, e *consentEstate) {
		heir := e.onboard(e.tT, "workflow-heir@consent.test", "viewer")
		for i, role := range []string{"work owner", "assign target", "message recipient"} {
			t.Run(role, func(t *testing.T) {
				e := e.forSubtest(t)
				email := fmt.Sprintf("workflow-%d@consent.test", i)
				user := e.onboard(e.tT, email, "viewer")
				created := e.do("POST", "/v1/m/orchestration/workflows", e.admin, e.tT, map[string]any{
					"name": fmt.Sprintf("names-%d", i), "steps": participantSteps(t, role, user, heir),
				})
				if created.code != http.StatusCreated {
					t.Fatalf("create workflow = %d %s", created.code, created.raw)
				}
				wfID, _ := created.body["id"].(string)
				e.scimDelete(e.tT, user)
				if r := e.do("POST", "/v1/m/orchestration/workflows", e.admin, e.tT, map[string]any{
					"name": fmt.Sprintf("late-%d", i), "steps": participantSteps(t, role, user, heir),
				}); r.code != http.StatusConflict || r.errorCode() != "subject_retirement_active" {
					t.Errorf("a workflow naming the retiring account as %s = %d %s, want 409 subject_retirement_active", role, r.code, r.raw)
				}
				e.runPump()
				e.wantBlocked(t, user, e.tT, wfID)
				if r := e.readmit(e.tT, email, "viewer"); r.code != http.StatusConflict {
					t.Errorf("re-admission while a workflow names the account as %s = %d %s, want 409", role, r.code, r.raw)
				}
				if r := e.do("PUT", "/v1/m/orchestration/workflows/"+wfID+"/steps", e.admin, e.tT, map[string]any{
					"steps": participantSteps(t, role, heir, heir),
				}); r.code != http.StatusOK {
					t.Fatalf("edit the steps = %d %s", r.code, r.raw)
				}
				e.runPump()
				e.wantRetired(t, user, e.tT)
			})
		}
	})
}

// publishManaged publishes content on a managed surface of tenant.
func (e *consentEstate) publishManaged(tenant model.TenantID, surface, content string) consentResp {
	e.t.Helper()
	return e.do("POST", "/v1/m/claude-policy/"+surface+"/publish", e.admin, tenant, map[string]any{"content": content})
}

// managedNaming is a valid document of a managed surface that names alias in a
// value no type fixes.
func managedNaming(surface, alias string) string {
	switch surface {
	case "managed-settings":
		return `{"x-policy-owner":"` + alias + `"}`
	case "hooks":
		return `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"notify ` + alias + `"}]}]}}`
	case "managed-mcp":
		return `{"allowedMcpServers":[{"serverName":"` + alias + `"}]}`
	case "sandbox":
		return `{"network":{"allowedDomains":["` + alias + `"]}}`
	}
	return ""
}

// managedSurfaces are the four managed surfaces, each matched against every
// alias of an account.
var managedSurfaces = []string{"managed-settings", "hooks", "managed-mcp", "sandbox"}

// TestAManagedDocumentNamingTheUserBlocksReadmissionUntilRevised: on every
// managed surface, the published document naming the account, by id or by
// email, blocks its retirement until the tenant publishes one that no longer
// names it; while it retires no document naming it by either alias can be
// published; and a publish that read the account's standing before the
// offboard is refused at its barrier.
func TestAManagedDocumentNamingTheUserBlocksReadmissionUntilRevised(t *testing.T) {
	onConsentEngines(t, func(t *testing.T, e *consentEstate) {
		for i, surface := range managedSurfaces {
			t.Run(surface, func(t *testing.T) {
				e := e.forSubtest(t)
				email := fmt.Sprintf("managed-%d@consent.test", i)
				user := e.onboard(e.tT, email, "viewer")
				alias := user.String()
				if i%2 == 1 {
					alias = email
				}
				if r := e.publishManaged(e.tT, surface, managedNaming(surface, alias)); r.code != http.StatusOK {
					t.Fatalf("publish %s = %d %s", surface, r.code, r.raw)
				}
				e.scimDelete(e.tT, user)
				for _, a := range []string{user.String(), strings.ToUpper(email)} {
					if r := e.publishManaged(e.tT, surface, managedNaming(surface, a)); r.code != http.StatusConflict ||
						r.errorCode() != "subject_retirement_active" {
						t.Errorf("a %s document naming the retiring account as %q = %d %s, want 409 subject_retirement_active",
							surface, a, r.code, r.raw)
					}
				}
				e.runPump()
				e.wantBlocked(t, user, e.tT, "governance.policy_revision:")
				if r := e.readmit(e.tT, email, "viewer"); r.code != http.StatusConflict {
					t.Errorf("re-admission while a %s document names the account = %d %s, want 409", surface, r.code, r.raw)
				}
				// A newer publish puts the older document out of force.
				if r := e.publishManaged(e.tT, surface, managedNaming(surface, "nobody@elsewhere.test")); r.code != http.StatusOK {
					t.Fatalf("revise %s = %d %s", surface, r.code, r.raw)
				}
				e.runPump()
				e.wantRetired(t, user, e.tT)
			})
		}

		t.Run("a publish parked between its standing read and its barrier", func(t *testing.T) {
			e := e.forSubtest(t)
			const email = "managed-parked@consent.test"
			user := e.onboard(e.tT, email, "viewer")
			if e.eng.standing == nil {
				t.Fatal("the composition has no standing port")
			}
			parked := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			e.eng.standing.afterReadHook = func(_ model.TenantID, users []model.ID) {
				for _, u := range users {
					if u == user {
						once.Do(func() {
							close(parked)
							<-release
						})
					}
				}
			}
			defer func() { e.eng.standing.afterReadHook = nil }()
			result := make(chan consentResp, 1)
			go func() { result <- e.publishManaged(e.tT, "managed-settings", managedNaming("managed-settings", email)) }()
			select {
			case <-parked:
			case r := <-result:
				t.Fatalf("the publish never read the account's standing: %d %s", r.code, r.raw)
			case <-time.After(10 * time.Second):
				t.Fatal("the publish never read the account's standing")
			}
			e.scimDelete(e.tT, user)
			close(release)
			if r := <-result; r.code != http.StatusConflict || r.errorCode() != "subject_retirement_active" {
				t.Errorf("the parked publish after the offboard = %d %s, want 409 subject_retirement_active", r.code, r.raw)
			}
			e.runPump()
			e.wantRetired(t, user, e.tT)
		})

		t.Run("values that name no account do not count against the bound", func(t *testing.T) {
			e := e.forSubtest(t)
			var ids []string
			for i := 0; i < 3*auth.MaxFencedSubjects; i++ {
				ids = append(ids, model.NewID().String())
			}
			doc := `{"allowedMcpServers":[{"serverName":"` + strings.Join(ids, `"},{"serverName":"`) + `"}]}`
			if r := e.publishManaged(e.tT, "managed-mcp", doc); r.code != http.StatusOK {
				t.Errorf("a document naming %d ids that are no account = %d %s, want 200", len(ids), r.code, r.raw)
			}
		})
	})
}

// TestAModelForbidOutlivesTheRetirementAndStillRestricts: the retirement deletes
// the account's model-access allows and keeps the forbids that name it; while it
// retires, a forbid naming the account may still be written and an allow may
// not; and the forbid still names the account once it is admitted again.
func TestAModelForbidOutlivesTheRetirementAndStillRestricts(t *testing.T) {
	onConsentEngines(t, func(t *testing.T, e *consentEstate) {
		const email = "model-forbid@consent.test"
		user := e.onboard(e.tT, email, "viewer")
		rule := func(target, effect string) consentResp {
			return e.do("POST", "/v1/m/models/model-access", e.admin, e.tT, map[string]any{
				"subject_kind": "user", "subject_ref": user.String(),
				"target_kind": "model", "target_ref": target, "effect": effect,
			})
		}
		allow := rule("claude-sonnet-4-6", "allow")
		forbid := rule("claude-opus-4-8", "forbid")
		allowID, _ := allow.body["id"].(string)
		forbidID, _ := forbid.body["id"].(string)
		if allow.code != http.StatusCreated || forbid.code != http.StatusCreated || allowID == "" || forbidID == "" {
			t.Fatalf("create the rules = %d %s / %d %s", allow.code, allow.raw, forbid.code, forbid.raw)
		}

		e.scimDelete(e.tT, user)
		if r := rule("claude-haiku-4-5", "forbid"); r.code != http.StatusCreated {
			t.Errorf("a forbid naming the retiring account = %d %s, want 201: a restriction is never refused", r.code, r.raw)
		}
		if r := rule("claude-3-5-haiku", "allow"); r.code != http.StatusConflict || r.errorCode() != "subject_retirement_active" {
			t.Errorf("an allow naming the retiring account = %d %s, want 409 subject_retirement_active", r.code, r.raw)
		}

		e.runPump()
		e.wantRetired(t, user, e.tT)
		if e.rowExists(e.tT, "models.model_access", model.ID(allowID)) {
			t.Errorf("the retirement kept the account's model-access allow")
		}
		if !e.rowExists(e.tT, "models.model_access", model.ID(forbidID)) {
			t.Errorf("the retirement deleted a forbid that only restricted the account")
		}
		if r := e.readmit(e.tT, email, "viewer"); r.code != http.StatusCreated {
			t.Fatalf("re-admission = %d %s", r.code, r.raw)
		}
		if got := e.rowColumn(e.tT, "models.model_access", model.ID(forbidID), "subject_ref"); got != user.String() {
			t.Errorf("the forbid after the re-admission names %q, want the re-admitted account", got)
		}
	})
}

// inertStep is a declared module's step that finds nothing and removes nothing.
type inertStep struct{ module string }

// Module implements auth.RetirementStep.
func (s inertStep) Module() string { return s.module }

// RetireUser implements auth.RetirementStep.
func (s inertStep) RetireUser(context.Context, auth.RetirementRequest) (auth.RetirementOutcome, error) {
	return auth.RetirementOutcome{}, nil
}
