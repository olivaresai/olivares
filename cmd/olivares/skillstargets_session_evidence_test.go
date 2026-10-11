// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/modules/governance/testsupport"
	"github.com/olivaresai/olivares/modules/skills"
)

// Fail only typed evidence reads; ordinary bearer authentication and the
// issuer's refresh still read the real credential and directory repositories.
type skillsSessionEvidenceFaultStore struct {
	store.Store
	fault string
	fail  atomic.Bool
	reads atomic.Int32
}

func (s *skillsSessionEvidenceFaultStore) AuthView(ctx context.Context, fn func(store.AuthScope) error) error {
	return s.Store.AuthView(ctx, func(sc store.AuthScope) error {
		return fn(skillsSessionEvidenceFaultScope{AuthScope: sc, AuthPrincipalEvidenceScope: sc.(store.AuthPrincipalEvidenceScope), AuthUserAuthorityEvidenceScope: sc.(store.AuthUserAuthorityEvidenceScope), st: s})
	})
}

type skillsSessionEvidenceFaultScope struct {
	store.AuthScope
	store.AuthPrincipalEvidenceScope
	store.AuthUserAuthorityEvidenceScope
	st *skillsSessionEvidenceFaultStore
}

func (s skillsSessionEvidenceFaultScope) ReadDirectoryEpochFact(ctx context.Context, tenant model.TenantID) (store.AuthorizationFactRef, error) {
	if s.st.fault == "directory" && s.st.fail.Load() {
		s.st.reads.Add(1)
		return store.AuthorizationFactRef{}, store.ErrDirectoryUnavailable
	}
	return s.AuthPrincipalEvidenceScope.ReadDirectoryEpochFact(ctx, tenant)
}

func (s skillsSessionEvidenceFaultScope) ReadUserAuthorityFact(ctx context.Context, id model.ID) (store.UserAuthorityFactRef, error) {
	if s.st.fault == "user" && s.st.fail.Load() {
		s.st.reads.Add(1)
		return store.UserAuthorityFactRef{}, store.ErrDirectoryUnavailable
	}
	return s.AuthUserAuthorityEvidenceScope.ReadUserAuthorityFact(ctx, id)
}

func TestSkillsSessionEvidenceOutageRecoversAsConsole(t *testing.T) {
	for _, fault := range []string{"directory", "user"} {
		t.Run(fault, func(t *testing.T) {
			c := bootSkillsComposition(t)
			tenant, err := model.ParseTenantID(c.tenant)
			if err != nil {
				t.Fatal(err)
			}
			revision := c.installPack("outage-recovery")
			st := &skillsSessionEvidenceFaultStore{Store: c.eng.store, fault: fault}
			a := auth.NewAuthenticator(st, nil)
			m := skills.New(skills.Options{Targets: skillsTargetAuthority{principals: a, st: st, authz: c.eng.authz, sessions: c.eng.sessionsMod}})
			srv, err := api.New(api.Options{Store: st, Authenticator: a, Authorizer: c.eng.authz, Signer: c.eng.signer, SetupToken: c.eng.setupTok, CoreEntityResolver: coreEntityResolver{st: st}, Modules: []api.Module{m}})
			if err != nil {
				t.Fatal(err)
			}
			c.h = srv.Handler()
			c.eng.api = srv
			owner, err := a.Authenticate(t.Context(), c.token)
			if err != nil {
				t.Fatal(err)
			}
			var notifications atomic.Int32
			issuer := auth.NewSessionCredentials(a, func(context.Context, auth.SessionScope) error { return nil }, func(context.Context, auth.SessionScope, string) error { notifications.Add(1); return nil })
			bearer, err := issuer.Mint(t.Context(), owner, auth.SessionScope{TenantID: tenant, WorkspaceID: model.ID(c.workspaceID()), FolderRef: "fixture", SessionRef: "osn_" + model.NewID().String(), RunRef: model.NewID().String(), Fence: 1})
			if err != nil {
				t.Fatal(err)
			}
			h := &sessionMCPHandler{authr: issuer, issuedSessionOnly: true, admits: c.eng.admits(), configurer: turnlessConfigurer{c.eng}}
			tool := func(revision string) (int, []byte) {
				t.Helper()
				raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "outage", "method": "tools/call", "params": map[string]any{"name": "olivares_skill_assign", "arguments": map[string]any{"pack_revision_id": revision}}})
				if err != nil {
					t.Fatal(err)
				}
				req := httptest.NewRequest("POST", "/session/mcp", bytes.NewReader(raw))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", "Bearer "+bearer)
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				var out struct {
					Result struct {
						StructuredContent struct {
							Status int             `json:"http_status"`
							Body   json.RawMessage `json:"body"`
						} `json:"structuredContent"`
					} `json:"result"`
				}
				if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out.Result.StructuredContent.Status == 0 {
					t.Fatalf("session tool lost its API answer: HTTP %d %s", rec.Code, rec.Body.String())
				}
				return out.Result.StructuredContent.Status, out.Result.StructuredContent.Body
			}
			compare := func(revision string, want int) {
				t.Helper()
				body := map[string]any{"target_kind": "workspace", "target_id": c.workspaceID(), "pack_revision_id": revision}
				consoleCode, consoleBody := c.do("POST", "/v1/m/skills/assignments", c.token, body)
				if consoleCode == http.StatusCreated {
					// Restore the same unassigned state through the public route;
					// repeating an existing pin correctly conflicts without If-Match.
					var result skills.AssignmentResult
					if err := json.Unmarshal(consoleBody, &result); err != nil {
						t.Fatal(err)
					}
					if code, raw := c.doHeader("DELETE", "/v1/m/skills/assignments/"+result.Assignment.ID, c.token, strconv.FormatInt(result.Assignment.Version, 10), nil); code != http.StatusOK {
						t.Fatalf("reset console assignment = %d %s", code, raw)
					}
				}
				toolCode, toolBody := tool(revision)
				if consoleCode != want || toolCode != want {
					t.Fatalf("console=%d %s session=%d %s; want %d with the same credentials", consoleCode, consoleBody, toolCode, toolBody, want)
				}
				if want >= 400 {
					var ce, se struct {
						Error struct{ Code, Message string }
					}
					if json.Unmarshal(consoleBody, &ce) != nil || json.Unmarshal(toolBody, &se) != nil || ce.Error.Code == "" || ce != se {
						t.Fatalf("console/session refusal differs: %s / %s", consoleBody, toolBody)
					}
				}
			}
			compare(model.NewID().String(), http.StatusNotFound)
			st.fail.Store(true)
			for range 2 {
				compare(revision, http.StatusServiceUnavailable)
			}
			if st.reads.Load() < 4 || notifications.Load() != 0 {
				t.Fatalf("typed failures=%d runtime retirements=%d", st.reads.Load(), notifications.Load())
			}
			st.fail.Store(false)
			code, raw := c.do("GET", "/v1/m/skills/assignments?target_kind=workspace&target_id="+c.workspaceID(), c.token, nil)
			var listed struct {
				Items []skills.Assignment `json:"items"`
			}
			if code != http.StatusOK || json.Unmarshal(raw, &listed) != nil || len(listed.Items) != 0 {
				t.Fatalf("outage created assignments: %d %s", code, raw)
			}
			compare(revision, http.StatusCreated)
			// Recovery does not weaken permanent revocation for known credential loss.
			if err := a.RevokeSession(t.Context(), owner, owner.CredID); err != nil {
				t.Fatal(err)
			}
			if _, err := issuer.AuthenticateLauncher(t.Context(), bearer); !errors.Is(err, auth.ErrSessionAccessEnded) {
				t.Fatalf("known owner revocation = %v", err)
			}
			if notifications.Load() != 1 {
				t.Fatalf("known owner loss notifications=%d, want 1", notifications.Load())
			}
		})
	}
}

func skillsSessionLauncher(t *testing.T, c skillsComposition, token string) auth.Principal {
	t.Helper()
	tenant, err := model.ParseTenantID(c.tenant)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := c.eng.authr.Authenticate(t.Context(), token)
	if err != nil {
		t.Fatal(err)
	}
	issuer := auth.NewSessionCredentials(c.eng.authr, func(context.Context, auth.SessionScope) error { return nil })
	bearer, err := issuer.Mint(t.Context(), owner, auth.SessionScope{TenantID: tenant, WorkspaceID: model.ID(c.workspaceID()), FolderRef: "fixture", SessionRef: "osn_" + model.NewID().String(), RunRef: model.NewID().String(), Fence: 1})
	if err != nil {
		t.Fatal(err)
	}
	p, err := issuer.AuthenticateLauncher(t.Context(), bearer)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.Ref(); ok {
		t.Fatal("session launcher has a native credential reference")
	}
	return p
}

func TestSkillsSessionConfiguredOverlayAnswersAsConsole(t *testing.T) {
	for _, engine := range []string{"cedar", "opa"} {
		t.Run(engine, func(t *testing.T) {
			t.Setenv("OLIVARES_PDP_ENGINE", engine)
			t.Setenv("OLIVARES_PDP_CEDAR_FILE", "")
			if engine == "opa" {
				// Exercise the configured HTTP adapter with a permissive protocol response.
				pdp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{"result": true})
				}))
				defer pdp.Close()
				t.Setenv("OLIVARES_PDP_OPA_URL", pdp.URL)
				t.Setenv("OLIVARES_PDP_OPA_PATH", "olivares/allow")
				t.Setenv("OLIVARES_PDP_OPA_TOKEN", "")
			}
			c := bootSkillsComposition(t)
			p := skillsSessionLauncher(t, c, c.token)
			target := skills.Target{Kind: "workspace", ID: c.workspaceID()}
			installed := c.installPack("overlay-parity-pack")
			for _, revision := range []string{model.NewID().String(), installed} {
				body := map[string]any{"target_kind": target.Kind, "target_id": target.ID, "pack_revision_id": revision}
				consoleCode, consoleBody := c.do("POST", "/v1/m/skills/assignments", c.token, body)
				raw, err := json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				// Use the same JSON through the session API used by its configure tool.
				req := httptest.NewRequest("POST", "/v1/m/skills/assignments", bytes.NewReader(raw))
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()
				c.eng.api.ServeSession(rec, req, p)
				var consoleResult, sessionResult map[string]any
				if err := json.Unmarshal(consoleBody, &consoleResult); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &sessionResult); err != nil {
					t.Fatal(err)
				}
				consoleError, _ := consoleResult["error"].(map[string]any)
				sessionError, _ := sessionResult["error"].(map[string]any)
				if consoleError == nil || sessionError == nil || consoleCode != http.StatusServiceUnavailable || rec.Code != consoleCode || sessionError["code"] != consoleError["code"] || sessionError["message"] != consoleError["message"] {
					t.Errorf("configured %s: console=%d %s session=%d %s; want identical 503", engine, consoleCode, consoleBody, rec.Code, rec.Body.String())
				}
			}

		})
	}
}

// Delay the real mutation's complete authority barrier, retaining its database checks.
type skillsSessionExpiryStore struct {
	store.Store
	until   time.Time
	entered *bool
}

func (s skillsSessionExpiryStore) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return s.Store.Mutate(ctx, tenant, func(sc store.Scope) error {
		return fn(skillsSessionExpiryScope{Scope: sc, until: s.until, entered: s.entered})
	})
}

// Simulate a barrier that finishes acquiring after cancellation. The real store
// barrier still checks every fact; the retained window must stop the callback.
type skillsSessionExpiryScope struct {
	store.Scope
	until   time.Time
	entered *bool
}

func (s skillsSessionExpiryScope) LockDirectoryAuthoritySnapshot(ctx context.Context, bundle store.AuthoritySnapshotBundle) error {
	if err := s.Scope.(store.DirectoryAuthoritySnapshotLocker).LockDirectoryAuthoritySnapshot(ctx, bundle); err != nil {
		return err
	}
	*s.entered = true
	timer := time.NewTimer(time.Until(s.until) + time.Millisecond)
	defer timer.Stop()
	<-timer.C
	return nil
}

func TestSkillsSessionScopedGrantCannotOutlivePolicyDeadline(t *testing.T) {
	t.Setenv("OLIVARES_POLICY_MAX_STALENESS", "3s")
	c := bootSkillsComposition(t)
	viewer := c.member("session-policy-viewer@x.io", auth.RoleViewer)
	p := skillsSessionLauncher(t, c, viewer.token)
	tenant, err := model.ParseTenantID(c.tenant)
	if err != nil {
		t.Fatal(err)
	}
	target := skills.Target{Kind: "workspace", ID: c.workspaceID()}
	revision := c.installPack("scoped-deadline-pack")
	// Both writes need a scoped permit; catalog read remains available through viewer RBAC.
	source := `permit(principal, action, resource) when { context.permission == "skills:assignment:write" || context.permission == "tenant:admin" };`
	testsupport.SeedCedar(t, c.eng.store, model.TenantID(c.tenant), source, c.eng.nhiEnforcer)
	fresh, found, err := governance.PolicyFreshness(t.Context(), c.eng.store, tenant)
	if err != nil || !found {
		t.Fatalf("freshness: found=%t err=%v", found, err)
	}
	expiry := fresh.RefreshedAt.Add(3 * time.Second)
	a := skillsTargetAuthority{principals: c.eng.authr, st: c.eng.store, authz: c.eng.authz, sessions: c.eng.sessionsMod}
	mc := api.ModuleContext{Tenant: tenant, Principal: p, Resource: auth.ResourceAttrs{Kind: "workspace", ID: target.ID, WorkspaceID: model.ID(target.ID)}}
	prepared, release, err := a.PrepareSkillsTarget(t.Context(), mc, target, true)
	defer release()
	if err != nil {
		t.Fatal(err)
	}
	// A fresh positive control reaches the genuine target and immutable pack fences.
	reached := false
	if err := a.MutateSkillsAssignment(prepared, mc, revision, func(sc store.Scope, read skills.AssignmentRevisionReader) error {
		reached = true
		_, err := read(prepared, revision)
		return err
	}); err != nil || !reached {
		t.Fatalf("fresh scoped grant: err=%v reached=%t", err, reached)
	}
	if _, err := a.authz.AuthorizeRouteMutation(prepared, auth.Request{Principal: p, Tenant: tenant, Permission: "tenant:admin", Resource: mc.Resource}); err == nil {
		t.Fatal("session launcher acquired a native mutation authority")
	}
	// Reuse the actual preparation with a longer finite parent too: retained evidence
	// itself must refuse after a wait, even if the caller loses its context deadline.
	value := prepared.Value(skillsPreparedTargetKey{}).(skillsPreparedTarget)
	parent, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	delayed := context.WithValue(parent, skillsPreparedTargetKey{}, value)
	entered := false
	a.st = skillsSessionExpiryStore{Store: c.eng.store, until: expiry, entered: &entered}
	reached = false
	err = a.MutateSkillsAssignment(delayed, mc, revision, func(sc store.Scope, read skills.AssignmentRevisionReader) error { reached = true; return nil })
	if err == nil || reached || !entered {
		t.Fatalf("scoped grant after policy deadline: err=%v mutation reached=%t barrier entered=%t; want refusal after barrier and before write", err, reached, entered)
	}
	if deadline, ok := prepared.Deadline(); !ok || deadline.After(expiry) {
		t.Errorf("request deadline=%s present=%t, want no later than policy %s", deadline, ok, expiry)
	}
}

// Preserve genuine governance verdicts and facts, shortening only one question's
// window to exercise retention independently from the other two decisions.
type skillsQuestionDeadlinePolicy struct {
	auth.PolicyEvaluator
	permission auth.Permission
	until      time.Time
}

func (e skillsQuestionDeadlinePolicy) EvaluateEvidence(ctx context.Context, req auth.Request) (auth.PolicyEvidenceDecision, error) {
	decision, err := e.PolicyEvaluator.(auth.PolicyEvidenceEvaluator).EvaluateEvidence(ctx, req)
	if err == nil && req.Permission == e.permission && e.until.Before(decision.FreshUntil) {
		decision.FreshUntil = e.until
	}
	return decision, err
}

func TestSkillsSessionRetainsEachQuestionDeadline(t *testing.T) {
	for _, permission := range []auth.Permission{"skills:assignment:write", "tenant:admin", "skills:catalog:read"} {
		t.Run(string(permission), func(t *testing.T) {
			c := bootSkillsComposition(t)
			tenant, err := model.ParseTenantID(c.tenant)
			if err != nil {
				t.Fatal(err)
			}
			p := skillsSessionLauncher(t, c, c.token)
			target := skills.Target{Kind: "workspace", ID: c.workspaceID()}
			revision := c.installPack("question-deadline-pack")
			until := time.Now().Add(time.Second)
			az := auth.NewAuthorizer(skillsQuestionDeadlinePolicy{PolicyEvaluator: c.eng.policyEval, permission: permission, until: until})
			a := skillsTargetAuthority{principals: c.eng.authr, st: c.eng.store, authz: az, sessions: c.eng.sessionsMod}
			mc := api.ModuleContext{Tenant: tenant, Principal: p, Resource: auth.ResourceAttrs{Kind: "workspace", ID: target.ID, WorkspaceID: model.ID(target.ID)}}
			prepared, release, err := a.PrepareSkillsTarget(t.Context(), mc, target, true)
			defer release()
			if err != nil {
				t.Fatal(err)
			}
			reached := false
			if err := a.MutateSkillsAssignment(prepared, mc, revision, func(sc store.Scope, read skills.AssignmentRevisionReader) error {
				reached = true
				_, err := read(prepared, revision)
				return err
			}); err != nil || !reached {
				t.Fatalf("fresh %s: err=%v reached=%t", permission, err, reached)
			}
			value := prepared.Value(skillsPreparedTargetKey{}).(skillsPreparedTarget)
			parent, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			delayed := context.WithValue(parent, skillsPreparedTargetKey{}, value)
			entered := false
			a.st = skillsSessionExpiryStore{Store: c.eng.store, until: until, entered: &entered}
			reached = false
			err = a.MutateSkillsAssignment(delayed, mc, revision, func(store.Scope, skills.AssignmentRevisionReader) error { reached = true; return nil })
			if err == nil || reached || !entered {
				t.Fatalf("expired %s: err=%v mutation reached=%t barrier entered=%t", permission, err, reached, entered)
			}
		})
	}
}
