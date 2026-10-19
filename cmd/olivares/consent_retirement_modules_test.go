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
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance/testsupport"
	"github.com/olivaresai/olivares/modules/sourcescope"
)

// publishCedar publishes a Cedar policy source for tenant.
func (e *consentEstate) publishCedar(tenant model.TenantID, source string) {
	testsupport.SeedCedar(e.t, e.eng.store, tenant, source, e.eng.nhiEnforcer)
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
		e.requireRetirementCompletion(t, "revising an authored grant requires a blocked verdict and later readmission")
		const email = "cedar-permit@consent.test"
		user := e.onboard(e.tT, email, "viewer")
		e.publishCedar(e.tT, `permit(principal in User::"`+user.String()+`", action, resource);`)
		e.deprovision(e.tT, user)
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
		e.requireRetirementCompletion(t, "source resolution after a lift requires completed retirement and readmission")
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

		e.deprovision(e.tT, user)
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
			e.deprovision(e.tT, subject)
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

// provisionExternal provisions an address with an external id into the tenant through the
// account engine and returns the account id.
func (e *consentEstate) provisionExternal(tenant model.TenantID, email, externalID string) string {
	e.t.Helper()
	u, created, err := e.eng.authr.SCIMProvisionUser(context.Background(), e.directoryActor(tenant), tenant,
		auth.SCIMUserInput{UserName: email, ExternalID: externalID, Active: true})
	if err != nil || !created {
		e.t.Fatalf("provision %s = %v created=%v", email, err, created)
	}
	return u.ID.String()
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
		e.requireRetirementCompletion(t, "editing workflow participants requires a blocked verdict and later completion")
		heir := e.onboard(e.tT, "workflow-heir@consent.test", "viewer")
		for i, role := range []string{"work owner", "assign target", "message recipient"} {
			t.Run(role, func(t *testing.T) {
				e := e.forSubtest(t)
				email := fmt.Sprintf("workflow-%d@consent.test", i)
				user := e.onboard(e.tT, email, "viewer")
				wfID := e.createRetirementWorkflow(e.tT, fmt.Sprintf("names-%d", i), participantSteps(t, role, user, heir), user, heir).String()
				e.deprovision(e.tT, user)
				late := e.do("POST", "/v1/m/orchestration/workflows", e.admin, e.tT, map[string]any{
					"name": fmt.Sprintf("late-%d", i), "steps": participantSteps(t, role, user, heir),
				})
				if editionOrchestrationAvailable {
					if late.code != http.StatusConflict || late.errorCode() != "subject_retirement_active" {
						t.Errorf("a workflow naming the retiring account as %s = %d %s, want 409 subject_retirement_active", role, late.code, late.raw)
					}
				} else if late.code != http.StatusNotImplemented || late.body["error"] != "orchestration_unavailable" {
					t.Errorf("Community workflow creation = %d %s, want 501 orchestration_unavailable", late.code, late.raw)
				}
				e.runPump()
				e.wantBlocked(t, user, e.tT, wfID)
				if r := e.readmit(e.tT, email, "viewer"); r.code != http.StatusConflict {
					t.Errorf("re-admission while a workflow names the account as %s = %d %s, want 409", role, r.code, r.raw)
				}
				stored := e.rowColumn(e.tT, "orchestration.workflow", model.ID(wfID), "steps")
				steps := participantSteps(t, role, heir, heir)
				edited := e.do("PUT", "/v1/m/orchestration/workflows/"+wfID+"/steps", e.admin, e.tT, map[string]any{"steps": steps})
				if editionOrchestrationAvailable {
					if edited.code != http.StatusOK {
						t.Fatalf("edit the steps = %d %s", edited.code, edited.raw)
					}
				} else {
					if edited.code != http.StatusNotImplemented || edited.body["error"] != "orchestration_unavailable" {
						t.Fatalf("Community workflow edit = %d %s, want 501 orchestration_unavailable", edited.code, edited.raw)
					}
					if got := e.rowColumn(e.tT, "orchestration.workflow", model.ID(wfID), "steps"); got != stored {
						t.Fatal("the refused edit changed the stored graph")
					}
					// Community has no authoring route. Model a resolved retained
					// graph through the guarded fixture seam before running retirement.
					b, err := json.Marshal(steps)
					if err != nil {
						t.Fatal(err)
					}
					ctx := context.Background()
					mutate := func(fn func(store.Scope) error) error { return e.eng.store.Mutate(ctx, e.tT, fn) }
					if err := auth.FencedWrite(ctx, e.eng.authr, e.tT, []model.ID{heir}, auth.FenceDirectory, mutate, func(sc store.Scope, _ bool) error {
						repo, err := sc.Ext("orchestration.workflow")
						if err != nil {
							return err
						}
						rec, err := repo.Get(ctx, model.ID(wfID))
						if err != nil {
							return err
						}
						rec["steps"] = string(b)
						_, err = repo.Update(ctx, rec)
						return err
					}); err != nil {
						t.Fatalf("resolve the stored workflow: %v", err)
					}
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

// TestAModelForbidOutlivesTheRetirementAndStillRestricts: the retirement deletes
// the account's model-access allows and keeps the forbids that name it; while it
// retires, a forbid naming the account may still be written and an allow may
// not; and the forbid still names the account once it is admitted again.
func TestAModelForbidOutlivesTheRetirementAndStillRestricts(t *testing.T) {
	onConsentEngines(t, func(t *testing.T, e *consentEstate) {
		e.requireRetirementCompletion(t, "checking a forbid after readmission requires completed retirement")
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

		e.deprovision(e.tT, user)
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

// TestNHISponsorshipBlocksReadmissionUntilReassigned proves durable sponsorship
// remains an obligation after directory inventory moves editions.
func TestNHISponsorshipBlocksReadmissionUntilReassigned(t *testing.T) {
	onConsentEngines(t, func(t *testing.T, e *consentEstate) {
		e.requireRetirementCompletion(t, "reassigning sponsorship requires a blocked verdict and later completion")
		const email = "sponsor@consent.test"
		user := model.ID(e.provisionExternal(e.tT, email, "ext-sponsor"))
		heir := model.ID(e.provisionExternal(e.tT, "sponsor-heir@consent.test", "ext-sponsor-heir"))
		e.rosterHuman(e.tT, "ext-sponsor")
		e.rosterHuman(e.tT, "ext-sponsor-heir")
		id := e.seedFenced(e.tT, "governance.nhi_lifecycle", nhiRecord("agent-sponsor", "sponsor_ref", "ext-sponsor"), user)
		e.deprovision(e.tT, user)
		e.runPump()
		e.wantBlocked(t, user, e.tT, "agent-sponsor")
		if r := e.readmit(e.tT, email, "viewer"); r.code != http.StatusConflict {
			t.Errorf("re-admission while the account sponsors an agent = %d %s, want 409", r.code, r.raw)
		}
		ctx := context.Background()
		refs := e.authorityRefs(heir)
		if err := e.eng.store.Mutate(ctx, e.tT, func(sc store.Scope) error {
			fact, err := directoryEpochFact(ctx, sc)
			if err != nil {
				return err
			}
			locker, ok := sc.(store.DirectoryAuthoritySnapshotLocker)
			if !ok {
				return fmt.Errorf("scope has no directory authority barrier")
			}
			if err := locker.LockDirectoryAuthoritySnapshot(ctx, store.AuthoritySnapshotBundle{Facts: []store.AuthorizationFactRef{fact}, UserAuthorities: refs}); err != nil {
				return err
			}
			repo, err := sc.Ext("governance.nhi_lifecycle")
			if err != nil {
				return err
			}
			rec, err := repo.Get(ctx, id)
			if err != nil {
				return err
			}
			rec["sponsor_ref"] = "ext-sponsor-heir"
			_, err = repo.Update(ctx, rec)
			return err
		}); err != nil {
			t.Fatalf("reassign durable sponsorship: %v", err)
		}
		e.runPump()
		e.wantRetired(t, user, e.tT)
	})
}
