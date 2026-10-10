// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/sdk"
)

const permTenantAdmin = auth.Permission("tenant:admin")

// answeredYes is a caller who is both a run and a tenant administrator.
var answeredYes = callerAsks{
	runUnrestricted: func() bool { return true },
	secretEnv:       func() bool { return true },
}

// B4.10c: a launch or a resume asks the authorizer "is this caller a run
// administrator?" and "may this caller give vault secrets?" only when its effective
// terms make the answer decide something: the full preset, or secret_env/git_read.
// Every question asked is recorded as an authorization decision, so a default launch
// must write neither record.

// questionedAuthorizer answers from allow and keeps what it was asked.
type questionedAuthorizer struct {
	mu    sync.Mutex
	allow map[auth.Permission]bool
	asked []auth.Permission
}

func (a *questionedAuthorizer) Authorize(_ context.Context, req auth.Request) auth.Decision {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.asked = append(a.asked, req.Permission)
	return auth.Decision{Allow: a.allow[req.Permission]}
}

func (a *questionedAuthorizer) count(p auth.Permission) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, got := range a.asked {
		if got == p {
			n++
		}
	}
	return n
}

func (a *questionedAuthorizer) reset() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.asked = nil
}

func callerQuestionsHarness(t *testing.T, allow map[auth.Permission]bool, opts ...Option) (*Module, model.TenantID, *fakeRunner, *questionedAuthorizer) {
	t.Helper()
	fr := &fakeRunner{initSID: "sess-questions"}
	m, _, tenant, _ := newRuntimeHarness(t, append([]Option{WithRunner(fr), WithCredentialSource(staticCred())}, opts...)...)
	az := &questionedAuthorizer{allow: allow}
	WithWorkAuthorizer(az)(m)
	return m, tenant, fr, az
}

func callerContext(tenant model.TenantID) api.ModuleContext {
	return api.ModuleContext{Tenant: tenant, Principal: auth.Principal{UserID: model.NewID()}}
}

func postRun(t *testing.T, m *Module, tenant model.TenantID, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	body["transport"], body["isolation"] = "stream-json", "native"
	body["provider_profile_ref"] = ensureRuntimeTestProfileRef(t, m, tenant)
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	m.handleCreateRun(w, httptest.NewRequest(http.MethodPost, "/v1/m/sessions/runs", bytes.NewReader(raw)), callerContext(tenant))
	return w
}

func postResume(t *testing.T, m *Module, tenant model.TenantID, runRef string) *httptest.ResponseRecorder {
	t.Helper()
	route := chi.NewRouteContext()
	route.URLParams.Add("ref", runRef)
	r := httptest.NewRequest(http.MethodPost, "/v1/m/sessions/runs/"+runRef+"/resume", nil)
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, route))
	w := httptest.NewRecorder()
	m.handleResumeRun(w, r, callerContext(tenant))
	return w
}

func requireStatus(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("status = %d, want %d: %s", w.Code, want, w.Body.String())
	}
}

// A default launch, whatever the non-full preset, writes no question record.
func TestCreateAsksNoCallerQuestionForALaunchThatNeedsNone(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"", "default", "plan", "acceptEdits"} {
		t.Run("mode="+mode, func(t *testing.T) {
			m, tenant, fr, az := callerQuestionsHarness(t, nil)
			requireStatus(t, postRun(t, m, tenant, map[string]any{"permission_mode": mode}), http.StatusCreated)
			if n := az.count(permRunAdmin) + az.count(permTenantAdmin); n != 0 {
				t.Fatalf("a launch in mode %q asked %d caller questions (%v), want none", mode, n, az.asked)
			}
			if launchCount(fr) != 1 {
				t.Fatalf("launches = %d, want 1", launchCount(fr))
			}
		})
	}
}

// The full preset still asks for run administration, once, and the answer still decides.
func TestCreateFullStillAsksRunAdministrationOnce(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		allow  bool
		status int
		starts int
	}{
		"administrator": {true, http.StatusCreated, 1},
		"not":           {false, http.StatusForbidden, 0},
	} {
		t.Run(name, func(t *testing.T) {
			m, tenant, fr, az := callerQuestionsHarness(t, map[auth.Permission]bool{permRunAdmin: tc.allow})
			requireStatus(t, postRun(t, m, tenant, map[string]any{"permission_mode": permModeBypass}), tc.status)
			if got := az.count(permRunAdmin); got != 1 {
				t.Fatalf("run administration asked %d times, want once", got)
			}
			if got := az.count(permTenantAdmin); got != 0 {
				t.Fatalf("tenant administration asked %d times for a launch with no secrets", got)
			}
			if launchCount(fr) != tc.starts {
				t.Fatalf("launches = %d, want %d", launchCount(fr), tc.starts)
			}
		})
	}
}

// The question is asked on the EFFECTIVE mode: a template that makes the launch full
// asks it, and a template that a named preset narrows does not.
func TestCreateAsksOnTheEffectiveModeAfterTheTemplate(t *testing.T) {
	t.Parallel()
	m, tenant, fr, az := callerQuestionsHarness(t, nil)
	full := seedTemplate(t, m, tenant, "Full", tplBody{Settings: &tplSettings{PermissionMode: permModeBypass}})

	requireStatus(t, postRun(t, m, tenant, map[string]any{"template_id": full}), http.StatusForbidden)
	if got := az.count(permRunAdmin); got != 1 {
		t.Fatalf("a launch the template makes full asked run administration %d times, want once", got)
	}
	az.reset()
	requireStatus(t, postRun(t, m, tenant, map[string]any{"template_id": full, "permission_mode": "plan"}), http.StatusCreated)
	if got := az.count(permRunAdmin); got != 0 {
		t.Fatalf("a named read-only preset under a full template asked run administration %d times, want none", got)
	}
	if launchCount(fr) != 1 {
		t.Fatalf("launches = %d, want 1", launchCount(fr))
	}
}

// secret_env and git_read ask tenant administration, once, and a refusal stands; a
// launch that names neither never asks it.
func TestCreateAsksTenantAdministrationOnlyForSecretsOrGitRead(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]map[string]any{
		"secret_env": {"permission_mode": "default", "secret_env": []SecretEnvRef{{Env: "GITHUB_TOKEN", Secret: "env/github"}}},
		"git_read":   {"permission_mode": "default", "git_read": "binding-1:acme/repo"},
	} {
		t.Run(name, func(t *testing.T) {
			m, tenant, fr, az := callerQuestionsHarness(t, map[auth.Permission]bool{permRunAdmin: true})
			requireStatus(t, postRun(t, m, tenant, body), http.StatusForbidden)
			if got := az.count(permTenantAdmin); got != 1 {
				t.Fatalf("tenant administration asked %d times, want once", got)
			}
			if got := az.count(permRunAdmin); got != 0 {
				t.Fatalf("run administration asked %d times for a non-full launch", got)
			}
			if launchCount(fr) != 0 {
				t.Fatal("a refused secret launch started a child")
			}
		})
	}
}

// A tenant administrator's secret launch is allowed: the one question, answered yes,
// gives the child its secret.
func TestCreateSecretEnvAllowedForATenantAdministrator(t *testing.T) {
	t.Parallel()
	vault := newFakeVault()
	m, tenant, fr, az := callerQuestionsHarness(t, map[auth.Permission]bool{permTenantAdmin: true}, WithProviderSecretVault(vault))
	vault.values[tenant.String()+"|env/github"] = secretCanary

	requireStatus(t, postRun(t, m, tenant, map[string]any{
		"permission_mode": "default", "secret_env": githubSecretEnv,
	}), http.StatusCreated)
	if got, other := az.count(permTenantAdmin), az.count(permRunAdmin); got != 1 || other != 0 {
		t.Fatalf("asked tenant administration %d and run administration %d times, want 1 and 0", got, other)
	}
	if got, _ := specEnvValue(fr.lastSpec(), "GITHUB_TOKEN"); got != secretCanary {
		t.Fatal("an allowed secret launch did not give the child its secret")
	}
}

func stoppedRun(t *testing.T, m *Module, tenant model.TenantID, p CreateRunParams) string {
	t.Helper()
	ctx := context.Background()
	p.Transport, p.Isolation, p.Actor, p.ActorKind = TransportStreamJSON, IsolationNative, actorU, actorKindU
	dto, err := createProfiledTestRun(t, m, ctx, tenant, p)
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	waitFor(t, "the session id", func() bool {
		rec, lerr := m.loadRun(ctx, tenant, dto.RunRef)
		return lerr == nil && rec.String(colClaudeSessionID) != ""
	})
	if _, err := m.stopRun(ctx, tenant, dto.RunRef, actorU, actorKindU); err != nil {
		t.Fatalf("stopRun: %v", err)
	}
	return dto.RunRef
}

// A resume of a default session asks nothing; a resume of a full one asks run
// administration once, though the handler judges the stored mode first and the
// launch judges the effective one.
func TestResumeAsksOnlyTheQuestionsTheSessionNeeds(t *testing.T) {
	t.Parallel()
	m, tenant, fr, az := callerQuestionsHarness(t, map[auth.Permission]bool{permRunAdmin: true})
	plain := stoppedRun(t, m, tenant, CreateRunParams{PermissionMode: "default"})
	// Separate runs under one profile must have distinct provider session IDs.
	fr.initSID = "sess-full"
	full := stoppedRun(t, m, tenant, CreateRunParams{PermissionMode: permModeBypass, MayRunUnrestricted: true})

	fr.initSID = "sess-questions"
	requireStatus(t, postResume(t, m, tenant, plain), http.StatusOK)
	if n := az.count(permRunAdmin) + az.count(permTenantAdmin); n != 0 {
		t.Fatalf("resuming a default session asked %d caller questions (%v), want none", n, az.asked)
	}
	fr.initSID = "sess-full"
	requireStatus(t, postResume(t, m, tenant, full), http.StatusOK)
	if got, other := az.count(permRunAdmin), az.count(permTenantAdmin); got != 1 || other != 0 {
		t.Fatalf("resuming a full session asked run administration %d and tenant administration %d times, want 1 and 0", got, other)
	}
}

// A refusal stands on resume too: a caller who is not an administrator is refused a
// full session, and a template widened to full since the launch is judged when the
// resume re-applies it.
func TestResumeFullRefusalStandsAndTemplateWideningIsAsked(t *testing.T) {
	t.Parallel()
	m, tenant, fr, az := callerQuestionsHarness(t, nil)
	full := stoppedRun(t, m, tenant, CreateRunParams{PermissionMode: permModeBypass, MayRunUnrestricted: true})
	id := seedTemplate(t, m, tenant, "Mutable", tplBody{Settings: &tplSettings{PermissionMode: "plan"}})
	fr.initSID = "sess-widened"
	widened := stoppedRun(t, m, tenant, CreateRunParams{PermissionMode: "plan", TemplateID: id})
	retermTemplate(t, m, tenant, id, tplBody{Settings: &tplSettings{PermissionMode: permModeBypass}})
	before := launchCount(fr)

	for name, ref := range map[string]string{"a stored full session": full, "a template widened to full": widened} {
		az.reset()
		requireStatus(t, postResume(t, m, tenant, ref), http.StatusForbidden)
		if got := az.count(permRunAdmin); got != 1 {
			t.Fatalf("%s: run administration asked %d times, want once", name, got)
		}
	}
	if launchCount(fr) != before {
		t.Fatal("a refused resume started a child")
	}
}

// A session that was given vault secrets asks tenant administration on resume; a
// session that was not never does.
func TestResumeAsksTenantAdministrationOnlyForASessionWithSecrets(t *testing.T) {
	t.Parallel()
	vault := newFakeVault()
	m, tenant, fr, az := callerQuestionsHarness(t, nil, WithProviderSecretVault(vault))
	vault.values[tenant.String()+"|env/github"] = secretCanary
	withSecrets := stoppedRun(t, m, tenant, CreateRunParams{PermissionMode: "default", SecretEnv: githubSecretEnv, MayUseSecretEnv: true})
	fr.initSID = "sess-plain"
	plain := stoppedRun(t, m, tenant, CreateRunParams{PermissionMode: "default"})
	before := launchCount(fr)

	requireStatus(t, postResume(t, m, tenant, plain), http.StatusOK)
	if got := az.count(permTenantAdmin); got != 0 {
		t.Fatalf("resuming a session with no secrets asked tenant administration %d times", got)
	}
	requireStatus(t, postResume(t, m, tenant, withSecrets), http.StatusForbidden)
	if got := az.count(permTenantAdmin); got != 1 {
		t.Fatalf("resuming a session with secrets asked tenant administration %d times, want once", got)
	}
	if launchCount(fr) != before+1 {
		t.Fatalf("launches = %d, want only the plain resume (%d)", launchCount(fr), before+1)
	}
}

// Exercise the real PDP and its recording context, then flush through the same
// governance writer as the API after the handler's store callbacks have returned.
// Counting a test authorizer's calls alone cannot catch a lost recording context.
func TestLaunchCallerQuestionsRetainTheirAnswers(t *testing.T) {
	for _, resume := range []bool{false, true} {
		for _, allow := range []bool{false, true} {
			for _, kind := range []string{"plain", "full", "secret", "template-full", "template-secret"} {
				name := fmt.Sprintf("resume=%t/allow=%t/%s", resume, allow, kind)
				t.Run(name, func(t *testing.T) {
					vault := newFakeVault()
					fr := &fakeRunner{initSID: "sess-retained"}
					m, st, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()), WithProviderSecretVault(vault))
					WithWorkAuthorizer(auth.NewAuthorizer(nil))(m)
					vault.values[tenant.String()+"|env/github"] = secretCanary
					principal := auth.Principal{Kind: auth.KindUser, UserID: model.NewID(), Superadmin: allow}
					mc := api.ModuleContext{Tenant: tenant, Principal: principal}
					p := CreateRunParams{PermissionMode: "default", MayRunUnrestricted: true, MayUseSecretEnv: true}
					body := map[string]any{"transport": "stream-json", "isolation": "native", "permission_mode": "default", "provider_profile_ref": ensureRuntimeTestProfileRef(t, m, tenant)}
					var permission auth.Permission
					switch kind {
					case "full":
						p.PermissionMode, body["permission_mode"], permission = permModeBypass, permModeBypass, permRunAdmin
					case "secret":
						p.SecretEnv, body["secret_env"], permission = githubSecretEnv, githubSecretEnv, permTenantAdmin
					case "template-full":
						p.TemplateID = seedTemplate(t, m, tenant, "Full", tplBody{Settings: &tplSettings{PermissionMode: permModeBypass}})
						p.PermissionMode = ""
						body["permission_mode"], body["template_id"], permission = "", p.TemplateID, permRunAdmin
					case "template-secret":
						p.TemplateID = seedTemplate(t, m, tenant, "Secret", tplBody{Settings: &tplSettings{SecretEnv: githubSecretEnv}})
						body["template_id"], permission = p.TemplateID, permTenantAdmin
					}
					var records []auth.AuthorizationRecord
					ctx := auth.WithAuthorizationRecording(t.Context(), func(record auth.AuthorizationRecord) { records = append(records, record) })
					w := httptest.NewRecorder()
					wantStatus := http.StatusCreated
					if resume {
						ref := stoppedRun(t, m, tenant, p)
						route := chi.NewRouteContext()
						route.URLParams.Add("ref", ref)
						ctx = context.WithValue(ctx, chi.RouteCtxKey, route)
						m.handleResumeRun(w, httptest.NewRequest(http.MethodPost, "/v1/m/sessions/runs/"+ref+"/resume", nil).WithContext(ctx), mc)
						wantStatus = http.StatusOK
					} else {
						raw, err := json.Marshal(body)
						if err != nil {
							t.Fatal(err)
						}
						m.handleCreateRun(w, httptest.NewRequest(http.MethodPost, "/v1/m/sessions/runs", bytes.NewReader(raw)).WithContext(ctx), mc)
					}
					if permission != "" && !allow {
						wantStatus = http.StatusForbidden
					}
					requireStatus(t, w, wantStatus)
					wantRecords := 1
					if permission == "" {
						wantRecords = 0
					}
					if len(records) != wantRecords {
						t.Fatalf("retained %d answers, want %d", len(records), wantRecords)
					}
					writer := governance.New()
					writer.UseData(api.NewModuleData(st))
					for _, record := range records {
						wantOutcome := auth.EvidenceDeny
						if allow {
							wantOutcome = auth.EvidenceAllow
						}
						if record.Snapshot.Permission != permission || record.Snapshot.Tenant != tenant || record.Snapshot.UserID != principal.UserID || record.PrincipalRef != principal.UserID.String() || record.Outcome != wantOutcome {
							t.Fatalf("record lost permission, tenant, caller or outcome: %+v", record)
						}
						if err := writer.RecordAuthorization(t.Context(), record); err != nil {
							t.Fatal(err)
						}
					}
					for _, perm := range []auth.Permission{permRunAdmin, permTenantAdmin} {
						question := governance.QuestionForReplay(principal.UserID.String(), "olivares", perm.Resource(), "", string(perm), "olivares.permission.v1")
						digest, err := question.Digest()
						if err != nil {
							t.Fatal(err)
						}
						if err := st.View(t.Context(), tenant, func(sc store.Scope) error {
							rows, err := sc.AccessEvidence().AuthorizationDecisionsForQuestion(t.Context(), digest)
							if err != nil {
								return err
							}
							want := 0
							if perm == permission {
								want = 1
							}
							if len(rows) != want {
								t.Fatalf("persisted %d %s answers, want %d", len(rows), perm, want)
							}
							if want == 1 {
								outcome := sdk.AccessOutcomeDeny
								if allow {
									outcome = sdk.AccessOutcomeAllow
								}
								if rows[0].Decision.Outcome != outcome {
									t.Fatalf("persisted outcome = %s, want %s", rows[0].Decision.Outcome, outcome)
								}
							}
							return nil
						}); err != nil {
							t.Fatal(err)
						}
					}
				})
			}
		}
	}
}
