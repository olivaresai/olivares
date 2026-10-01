// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type orchestrationTestScope struct{ h *harness }

func (s orchestrationTestScope) WithScope(ctx context.Context, p auth.Principal, tenant model.TenantID, mutate bool, fn func(store.Scope) error) error {
	op := s.h.st.View
	if mutate {
		op = s.h.st.Mutate
	}
	return op(ctx, tenant, func(sc store.Scope) error { return s.h.m.WithOrchestrationWorkScope(ctx, sc, p, tenant, mutate, fn) })
}

type orchestrationTestCredentials struct {
	m     *Module
	a     *auth.Authenticator
	actor auth.Principal
	last  WorkSessionCredential
}

func (s *orchestrationTestCredentials) Mint(ctx context.Context, req WorkSessionCredentialRequest) (WorkSessionCredential, error) {
	spec, err := s.m.OrchestrationCredentialSpec(ctx, req, true)
	if err != nil {
		return WorkSessionCredential{}, err
	}
	var c WorkSessionCredential
	if spec != nil {
		issued, err := s.a.IssueOrchestrationSessionCredential(ctx, s.actor, *spec)
		if err != nil {
			return c, err
		}
		c = WorkSessionCredential{ID: issued.ID, Token: issued.Token, Tenant: issued.Tenant, SessionRef: issued.SessionRef, RunRef: issued.RunRef, AgentRef: issued.AgentRef, ClaimFence: issued.ClaimFence, NotAfter: issued.ExpiresAt}
	} else {
		issued, err := s.a.IssueWorkSessionCredential(ctx, s.actor, auth.WorkSessionCredentialSpec{Tenant: req.Tenant, SessionRef: req.SessionRef, RunRef: req.RunRef, AgentRef: req.AgentRef, ClaimFence: req.ClaimFence})
		if err != nil {
			return c, err
		}
		c = WorkSessionCredential{ID: issued.ID, Token: issued.Token, Tenant: issued.Tenant, SessionRef: issued.SessionRef, RunRef: issued.RunRef, AgentRef: issued.AgentRef, ClaimFence: issued.ClaimFence, NotAfter: issued.ExpiresAt}
	}
	s.last = c
	return c, nil
}
func (s *orchestrationTestCredentials) Renew(ctx context.Context, id model.ID, req WorkSessionCredentialRequest) (time.Time, error) {
	spec, err := s.m.OrchestrationCredentialSpec(ctx, req, false)
	if err != nil {
		return time.Time{}, err
	}
	if spec != nil {
		return s.a.RenewOrchestrationSessionCredential(ctx, s.actor, id, *spec)
	}
	return s.a.RenewWorkSessionCredential(ctx, s.actor, id, auth.WorkSessionCredentialSpec{Tenant: req.Tenant, SessionRef: req.SessionRef, RunRef: req.RunRef, AgentRef: req.AgentRef, ClaimFence: req.ClaimFence})
}
func (s *orchestrationTestCredentials) Revoke(ctx context.Context, id model.ID, req WorkSessionCredentialRequest) error {
	spec := auth.WorkSessionCredentialSpec{Tenant: req.Tenant, SessionRef: req.SessionRef, RunRef: req.RunRef, AgentRef: req.AgentRef, ClaimFence: req.ClaimFence}
	err := s.a.RevokeWorkSessionCredential(ctx, s.actor, id, spec)
	if errors.Is(err, auth.ErrUnauthenticated) {
		return s.a.RevokeOrchestrationRuntimeCredential(ctx, s.actor, id, spec)
	}
	return err
}

type orchestrationFixture struct {
	h                               *harness
	resolver                        *k2WorkIdentity
	source                          *orchestrationTestCredentials
	tenant                          model.TenantID
	workspace                       model.ID
	profile, admin, token, run, sid string
}

func newOrchestrationFixture(t *testing.T, capabilities []string) *orchestrationFixture {
	return newOrchestrationFixtureInWorkspace(t, capabilities, false)
}

func newOrchestrationFixtureInWorkspace(t *testing.T, capabilities []string, nondefault bool) *orchestrationFixture {
	t.Helper()
	resolver := newK2WorkIdentity()
	m := New(WithRunner(&fakeRunner{initSID: "orchestration-provider-fixture"}), WithCredentialSource(staticCred()), WithWorkIdentityResolver(resolver), WithWorkContentGuard(allowWorkContent{}))
	m.UseExecutionEnvironmentRef(testEnvRef)
	m.UseSessionWorkspaceRoot(t.TempDir())
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "orchestration-loop")
	workspace := workAPIWorkspace(t, h, tenant)
	if nondefault {
		if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
			ws, err := sc.Workspaces().Create(context.Background(), model.Workspace{Name: "Project", Slug: "project", Status: model.StatusActive})
			workspace = ws.ID
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	actor, err := auth.NewSystemOperator("test:orchestration-runtime", "exercise profile-bound orchestration credentials")
	if err != nil {
		t.Fatal(err)
	}
	source := &orchestrationTestCredentials{m: m, a: auth.NewAuthenticator(h.st, nil), actor: actor}
	m.UseWorkSessionCredentialSource(source)
	m.UseOrchestrationWorkScopeSource(orchestrationTestScope{h})
	created := h.doJSON(http.MethodPost, "/v1/m/sessions/provider-profiles", admin, map[string]any{"driver": "claude", "config_home": t.TempDir(), "user_home": t.TempDir(), "session_work_grant": map[string]any{"role": "orchestrator", "workspace_id": workspace, "capabilities": capabilities}}, tenantHdr(tenant))
	if created.code != http.StatusCreated {
		t.Fatalf("profile = %d %s", created.code, created.raw)
	}
	profile := created.body["profile_ref"].(string)
	run, err := m.createRun(context.Background(), tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: actor.Actor(), ActorKind: model.ActorSystem, AgentRef: "orchestrator:" + model.NewID().String(), ProviderProfileRef: profile})
	if err != nil {
		t.Fatalf("orchestrator launch: %v", err)
	}
	credential := source.last
	if credential.Token == "" {
		t.Fatal("orchestrator launch did not inject a credential")
	}
	resolver.admit("session", credential.SessionRef)
	f := &orchestrationFixture{h: h, resolver: resolver, source: source, tenant: tenant, workspace: workspace, profile: profile, admin: admin, token: credential.Token, run: run.RunRef, sid: credential.SessionRef}
	return f
}
func (f *orchestrationFixture) patchGrant(t *testing.T, grant any) {
	t.Helper()
	r := f.h.doJSON(http.MethodPatch, "/v1/m/sessions/provider-profiles/"+f.profile, f.admin, map[string]any{"session_work_grant": grant}, tenantHdr(f.tenant))
	if r.code != http.StatusOK {
		t.Fatalf("patch grant = %d %s", r.code, r.raw)
	}
}

func TestOrchestrationWorkDenyDefaultCapabilitiesRevocationAndFence(t *testing.T) {
	for _, scenario := range []string{"withdrawn", "replaced", "disabled", "stopped", "claim moved", "missing composition"} {
		t.Run(scenario, func(t *testing.T) {
			f := newOrchestrationFixture(t, []string{"work.read"})
			if r := f.h.do(http.MethodGet, "/v1/m/sessions/work-items", f.token, tenantHdr(f.tenant)); r.code != http.StatusOK {
				t.Fatalf("valid grant list = %d %s", r.code, r.raw)
			}
			switch scenario {
			case "withdrawn":
				f.patchGrant(t, nil)
			case "replaced":
				f.patchGrant(t, map[string]any{"role": "orchestrator", "workspace_id": f.workspace, "capabilities": []string{"work.read"}})
			case "disabled":
				r := f.h.doJSON(http.MethodPatch, "/v1/m/sessions/provider-profiles/"+f.profile, f.admin, map[string]any{"state": "disabled"}, tenantHdr(f.tenant))
				if r.code != http.StatusOK {
					t.Fatal(r.raw)
				}
			case "stopped":
				if _, err := f.h.m.stopRun(context.Background(), f.tenant, f.run, "system", "system"); err != nil {
					t.Fatal(err)
				}
			case "claim moved":
				claim, ok, err := f.h.m.ActiveClaim(context.Background(), f.tenant, f.sid)
				if err != nil || !ok {
					t.Fatal("claim missing")
				}
				if err := f.h.m.Release(context.Background(), f.tenant, f.sid, claim.Holder, claim.Fence); err != nil {
					t.Fatal(err)
				}
				if _, err := f.h.m.Claim(context.Background(), f.tenant, f.sid, "successor", 0); err != nil {
					t.Fatal(err)
				}
			case "missing composition":
				f.h.m.UseOrchestrationWorkScopeSource(nil)
			}
			r := f.h.do(http.MethodGet, "/v1/m/sessions/work-items", f.token, tenantHdr(f.tenant))
			if r.code != http.StatusForbidden && r.code != http.StatusUnauthorized {
				t.Fatalf("stale authority = %d %s", r.code, r.raw)
			}
		})
	}
	t.Run("permission overlap does not grant review", func(t *testing.T) {
		f := newOrchestrationFixture(t, []string{"work.create", "work.read"})
		body := workAPICreateBody(f.workspace, "unused", "bounded authoring")
		body["owner_kind"], body["owner_ref"] = "session", f.sid
		r := f.h.doJSON(http.MethodPost, "/v1/m/sessions/work-items?mode=apply", f.token, body, workAPIHeaders(f.tenant, map[string]string{"Idempotency-Key": model.NewID().String()}))
		if r.code != http.StatusOK {
			t.Fatalf("create capability = %d %s", r.code, r.raw)
		}
		id := r.body["result_id"].(string)
		r = f.h.doJSON(http.MethodPost, "/v1/m/sessions/work-items/"+id+"/transitions?mode=apply", f.token, map[string]any{"command": "item.cancel", "terminal_code": "operator_cancelled", "reason": "scope denial witness"}, workAPIHeaders(f.tenant, map[string]string{"Idempotency-Key": model.NewID().String(), "If-Match": etag(1)}))
		if r.code != http.StatusForbidden {
			t.Fatalf("create capability reviewed = %d %s", r.code, r.raw)
		}
		for _, path := range []string{"/v1/m/sessions/provider-profiles", "/v1/m/sessions/leases", "/v1/m/sessions/runs"} {
			if r := f.h.do(http.MethodGet, path, f.token, tenantHdr(f.tenant)); r.code != http.StatusForbidden {
				t.Fatalf("authority escaped %s: %d", path, r.code)
			}
		}
	})
}

func TestOrchestrationWorkPositiveCreateAssignWorkerReviewDecisionAndResume(t *testing.T) {
	f := newOrchestrationFixture(t, []string{"decision.read", "decision.write", "work.assign", "work.create", "work.read", "work.review"})
	ctx := context.Background()
	profile := f.h.doJSON(http.MethodPost, "/v1/m/sessions/provider-profiles", f.admin, map[string]any{"driver": "claude", "config_home": t.TempDir(), "user_home": t.TempDir()}, tenantHdr(f.tenant))
	if profile.code != http.StatusCreated {
		t.Fatal(profile.raw)
	}
	worker, err := f.h.m.createRun(ctx, f.tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "system", ActorKind: model.ActorSystem, ProviderProfileRef: profile.body["profile_ref"].(string)})
	if err != nil {
		t.Fatalf("worker launch: %v", err)
	}
	workerCredential := f.source.last
	f.resolver.admit("session", workerCredential.SessionRef)
	workerPrincipal, err := f.source.a.Authenticate(ctx, workerCredential.Token)
	if err != nil || !workerPrincipal.IsWorkSessionCredential() {
		t.Fatal("default profile did not retain the worker ceiling")
	}
	if r := f.h.do(http.MethodGet, "/v1/m/sessions/work-items", workerCredential.Token, tenantHdr(f.tenant)); r.code != http.StatusForbidden {
		t.Fatal("worker read the backlog")
	}
	body := workAPICreateBody(f.workspace, "unused", "product-native coordinated work")
	body["owner_kind"], body["owner_ref"] = "session", f.sid
	created := f.h.doJSON(http.MethodPost, "/v1/m/sessions/work-items?mode=apply", f.token, body, workAPIHeaders(f.tenant, map[string]string{"Idempotency-Key": model.NewID().String()}))
	if created.code != http.StatusOK {
		t.Fatalf("orchestrator create = %d %s", created.code, created.raw)
	}
	id := created.body["result_id"].(string)
	version := int64(created.body["version"].(float64))
	apply := func(method, suffix, token string, command map[string]any) resp {
		t.Helper()
		r := f.h.doJSON(method, "/v1/m/sessions/work-items/"+id+suffix+"?mode=apply", token, command, workAPIHeaders(f.tenant, map[string]string{"Idempotency-Key": model.NewID().String(), "If-Match": etag(version)}))
		if r.code != http.StatusOK {
			t.Fatalf("%s = %d %s", suffix, r.code, r.raw)
		}
		version = int64(r.body["version"].(float64))
		return r
	}
	apply(http.MethodPost, "/assignments", f.token, map[string]any{"owner_kind": "session", "owner_ref": workerCredential.SessionRef})
	apply(http.MethodPost, "/transitions", f.token, map[string]any{"command": "item.ready"})
	apply(http.MethodPost, "/lease/acquire", workerCredential.Token, map[string]any{"holder_sid": workerCredential.SessionRef, "ttl_seconds": 60})
	lease := f.h.do(http.MethodGet, "/v1/m/sessions/work-items/"+id+"/lease", f.admin, tenantHdr(f.tenant))
	fence := int64(lease.body["fence"].(float64))
	criteria := f.h.do(http.MethodGet, "/v1/m/sessions/work-items/"+id+"/acceptance", f.token, tenantHdr(f.tenant))
	if criteria.code != http.StatusOK {
		t.Fatal(criteria.raw)
	}
	criterion := criteria.body["items"].([]any)[0].(map[string]any)["id"].(string)
	evaluate := map[string]any{"holder_sid": workerCredential.SessionRef, "fence": fence, "acceptance": []any{map[string]any{"state": "failed", "evidence_ref": "job:orchestration-loop", "evidence_hash": hexHash(hashBytes([]byte("orchestration-loop")))}}}
	apply(http.MethodPatch, "/acceptance/"+criterion, workerCredential.Token, evaluate)
	apply(http.MethodPost, "/transitions", workerCredential.Token, map[string]any{"command": "item.submit", "holder_sid": workerCredential.SessionRef, "fence": fence})
	review := []any{map[string]any{"state": "passed", "evidence_ref": "job:orchestrator-review", "evidence_hash": hexHash(hashBytes([]byte("orchestrator-review")))}}
	apply(http.MethodPatch, "/acceptance/"+criterion, f.token, map[string]any{"acceptance": review})
	decision := f.h.doJSON(http.MethodPost, "/v1/m/sessions/decisions?mode=apply", f.token, map[string]any{"command": "decision.set", "workspace_id": f.workspace, "work_item_id": id, "decision_key": "acceptance", "subject_kind": "work.scope", "subject_ref": id, "statement_md": "Accept the worker result.", "rationale_md": "The required criterion passed with recorded evidence.", "authority_ref": "approval:orchestration-loop"}, workAPIHeaders(f.tenant, map[string]string{"Idempotency-Key": model.NewID().String(), "If-Match": etag(version)}))
	if decision.code != http.StatusOK {
		t.Fatalf("orchestrator decision = %d %s", decision.code, decision.raw)
	}
	version = int64(decision.body["version"].(float64))
	apply(http.MethodPost, "/transitions", f.token, map[string]any{"command": "item.complete"})
	snapshot := f.h.do(http.MethodGet, "/v1/m/sessions/work-items/"+id, f.token, tenantHdr(f.tenant))
	if snapshot.code != http.StatusOK {
		t.Fatal(snapshot.raw)
	}
	decisions := f.h.do(http.MethodGet, "/v1/m/sessions/decisions?work_item_id="+id, f.token, tenantHdr(f.tenant))
	if decisions.code != http.StatusOK || len(decisions.body["items"].([]any)) != 1 {
		t.Fatal("decision read did not return the recorded decision")
	}
	orchestratorPrincipal, err := f.source.a.Authenticate(ctx, f.token)
	if err != nil {
		t.Fatal(err)
	}
	decisionsAudited := 0
	if err := f.h.st.View(ctx, f.tenant, func(sc store.Scope) error {
		walker, ok := sc.Audit().(store.CanonicalWalker)
		if !ok {
			t.Fatal("canonical audit read unavailable")
		}
		return walker.WalkCanonical(ctx, 0, func(event model.AuditEvent, meta string, _ []byte) error {
			if err := json.Unmarshal([]byte(meta), &event.Meta); err != nil {
				return err
			}
			if event.Action == "sessions.work.decision.set" {
				decisionsAudited++
			}
			if event.Meta["sid"] == f.sid {
				if event.Meta["run_ref"] != f.run || event.Meta["agent_ref"] != orchestratorPrincipal.AgentIdentity || event.ActorKind != model.ActorAgent {
					t.Fatal("work audit lost its agent, SID or run attribution")
				}
			}
			if event.Action == "sessions.work.decision.set" && event.Meta["sid"] != f.sid {
				t.Fatalf("decision audit lost its orchestrator session: actor_kind=%s metadata=%v", event.ActorKind, event.Meta)
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	if decisionsAudited != 1 {
		t.Fatal("decision audit was not persisted")
	}
	if _, err := f.h.m.stopRun(ctx, f.tenant, f.run, "system", model.ActorSystem); err != nil {
		t.Fatal(err)
	}
	if _, err := f.h.m.resumeRun(ctx, f.tenant, f.run, "system", model.ActorSystem, orchestratorPrincipal.AgentIdentity); err != nil {
		t.Fatal(err)
	}
	fresh := f.source.last
	if fresh.ClaimFence <= 1 || fresh.Token == f.token {
		t.Fatal("resume did not rotate orchestration authority")
	}
	if r := f.h.do(http.MethodGet, "/v1/m/sessions/work-items", f.token, tenantHdr(f.tenant)); r.code != http.StatusUnauthorized {
		t.Fatal("old bearer survived resume")
	}
	if r := f.h.do(http.MethodGet, "/v1/m/sessions/work-items", fresh.Token, tenantHdr(f.tenant)); r.code != http.StatusOK {
		t.Fatalf("successor bearer = %d %s", r.code, r.raw)
	}
	if _, err := f.h.m.stopRun(ctx, f.tenant, worker.RunRef, "system", model.ActorSystem); err != nil {
		t.Fatal(err)
	}
}

func TestOrchestrationWorkNonDefaultWorkspaceCannotReadOrWriteForeignRows(t *testing.T) {
	f := newOrchestrationFixtureInWorkspace(t, []string{"work.create", "work.read", "work.review"}, true)
	foreign := workAPIWorkspace(t, f.h, f.tenant)
	create := func(workspace model.ID, token string) resp {
		body := workAPICreateBody(workspace, "unused", "workspace confinement")
		body["owner_kind"], body["owner_ref"] = "session", f.sid
		return f.h.doJSON(http.MethodPost, "/v1/m/sessions/work-items?mode=apply", token, body, workAPIHeaders(f.tenant, map[string]string{"Idempotency-Key": model.NewID().String()}))
	}
	own := create(f.workspace, f.token)
	if own.code != http.StatusOK {
		t.Fatalf("scoped create = %d %s", own.code, own.raw)
	}
	other := create(foreign, f.admin)
	if other.code != http.StatusOK {
		t.Fatal(other.raw)
	}
	list := f.h.do(http.MethodGet, "/v1/m/sessions/work-items", f.token, tenantHdr(f.tenant))
	if list.code != http.StatusOK || len(list.body["items"].([]any)) != 1 {
		t.Fatalf("workspace list leaked or lost rows: %d %s", list.code, list.raw)
	}
	if r := create(foreign, f.token); r.code != http.StatusForbidden && r.code != http.StatusNotFound {
		t.Fatalf("cross-workspace create = %d %s", r.code, r.raw)
	}
	id := other.body["result_id"].(string)
	if r := f.h.do(http.MethodGet, "/v1/m/sessions/work-items/"+id, f.token, tenantHdr(f.tenant)); r.code != http.StatusNotFound && r.code != http.StatusForbidden {
		t.Fatalf("foreign point read = %d %s", r.code, r.raw)
	}
	if r := f.h.doJSON(http.MethodPost, "/v1/m/sessions/work-items/"+id+"/transitions?mode=apply", f.token, map[string]any{"command": "item.cancel", "terminal_code": "operator_cancelled", "reason": "scope denial witness"}, workAPIHeaders(f.tenant, map[string]string{"Idempotency-Key": model.NewID().String(), "If-Match": etag(1)})); r.code != http.StatusNotFound && r.code != http.StatusForbidden {
		t.Fatalf("foreign write = %d %s", r.code, r.raw)
	}
}

// A changed Claim at the commit boundary must roll back all audit/domain effects
// authored through the confined callback, even if its admission was initially valid.
func TestOrchestrationWorkClaimChangeRollsBackCallback(t *testing.T) {
	f := newOrchestrationFixture(t, []string{"work.create", "work.read"})
	ctx := context.Background()
	p, err := f.source.a.Authenticate(ctx, f.token)
	if err != nil {
		t.Fatal(err)
	}
	before, found, err := f.h.m.ActiveClaim(ctx, f.tenant, f.sid)
	if err != nil || !found {
		t.Fatal("fixture claim missing")
	}
	err = f.h.st.Mutate(ctx, f.tenant, func(raw store.Scope) error {
		return f.h.m.WithOrchestrationWorkScope(ctx, raw, p, f.tenant, true, func(confined store.Scope) error {
			if _, err := confined.Audit().Append(ctx, model.AuditDraft{Actor: p.Actor(), ActorKind: model.ActorAgent, Action: "test.orchestration.rollback"}); err != nil {
				return err
			}
			claim, found, err := findClaim(ctx, raw, f.sid)
			if err != nil || !found {
				return err
			}
			claim.Set(colHolder, "changed-after-admission")
			repo, err := raw.Ext(claimKind)
			if err != nil {
				return err
			}
			_, err = repo.Update(ctx, claim)
			return err
		})
	})
	if !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("changed Claim was not refused: %v", err)
	}
	after, found, err := f.h.m.ActiveClaim(ctx, f.tenant, f.sid)
	if err != nil || !found || after.Holder != before.Holder {
		t.Fatal("refused transaction left Claim effects")
	}
	if err := f.h.st.View(ctx, f.tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(ctx, 0, func(e model.AuditEvent) error {
			if e.Action == "test.orchestration.rollback" {
				t.Error("refused transaction left audit effects")
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
}
