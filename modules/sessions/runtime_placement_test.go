// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions/egress"
	"github.com/olivaresai/olivares/modules/skills"
)

type fakeRunPlacement struct {
	runner       Runner
	mu           sync.Mutex
	checks       []string
	principals   []auth.Principal
	requireBound bool
	err          error
}

func (p *fakeRunPlacement) Check(_ context.Context, _ model.TenantID, node string, principal auth.Principal) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.checks = append(p.checks, node)
	p.principals = append(p.principals, principal)
	if _, bound := principal.Ref(); p.requireBound && !bound {
		return ErrRunNodeUnavailable
	}
	return p.err
}

func (p *fakeRunPlacement) Launch(ctx context.Context, spec LaunchSpec) (Process, error) {
	return p.runner.Launch(ctx, spec)
}

func (p *fakeRunPlacement) checked() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.checks...)
}

func TestRunOnACommunityEngineAnswers501AndStartsNothing(t *testing.T) {
	runner := &fakeRunner{}
	m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(runner), WithCredentialSource(staticCred()),
		WithLaunchGate(&spyGate{inner: LaunchDecision{Allowed: true}}))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "community-run-on")
	got := h.doJSON("POST", "/v1/m/sessions/runs", admin, map[string]any{
		"name": "remote", "run_on": "node-remote",
	}, tenantHdr(tenant))
	errBody, _ := got.body["error"].(map[string]any)
	msg, _ := errBody["message"].(string)
	if got.code != http.StatusNotImplemented || errBody["code"] != "business_required" || !strings.Contains(msg, "Business") {
		t.Fatalf("run_on = %d %s, want 501 business_required naming Business", got.code, got.raw)
	}
	profiles, _, err := m.ListProfiles(t.Context(), tenant, "", model.Query{Limit: 50})
	if err != nil || len(profiles) != 0 {
		t.Fatalf("refused remote launch created profiles: %v (%v)", profiles, err)
	}
	if n := launchCount(runner); n != 0 {
		t.Fatalf("a refused remote launch reached the local runner %d times", n)
	}
}

func TestRunOnUsesItsPlacementAndSurvivesResume(t *testing.T) {
	local, remote := &fakeRunner{initSID: "remote-session"}, &fakeRunner{initSID: "remote-session"}
	placement := &fakeRunPlacement{runner: remote}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(local), WithRunPlacement(placement), WithCredentialSource(staticCred()),
		WithLaunchGate(&spyGate{inner: LaunchDecision{Allowed: true}}))
	dto, err := createProfiledTestRun(t, m, t.Context(), tenant, CreateRunParams{
		RunOn: "node-remote", Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("create remote run: %v", err)
	}
	if dto.RunOn != "node-remote" {
		t.Fatalf("run_on = %q, want node-remote", dto.RunOn)
	}
	if n := launchCount(local); n != 0 {
		t.Fatalf("remote launch used the local runner %d times", n)
	}
	spec := remote.lastSpec()
	if spec.RunOn != "node-remote" || spec.RunRef != dto.RunRef || !spec.WorkspaceDirOwned {
		t.Fatalf("remote spec placement = (%q,%q,%v), want (node-remote,%s,true)", spec.RunOn, spec.RunRef, spec.WorkspaceDirOwned, dto.RunRef)
	}
	if spec.TenantID != tenant.String() {
		t.Fatalf("remote spec tenant = %q, want %s", spec.TenantID, tenant)
	}
	if _, err := m.stopRun(t.Context(), tenant, dto.RunRef, "user:u1", "user"); err != nil {
		t.Fatalf("stop remote run: %v", err)
	}
	if _, err := m.resumeRun(t.Context(), tenant, dto.RunRef, "user:u1", "user", ""); err != nil {
		t.Fatalf("resume remote run: %v", err)
	}
	checks := placement.checked()
	if len(checks) != 2 || checks[0] != "node-remote" || checks[1] != "node-remote" {
		t.Fatalf("placement checks = %v, want the stored node on create and resume", checks)
	}
	if n := launchCount(local); n != 0 {
		t.Fatalf("remote resume used the local runner %d times", n)
	}
}

func TestLocalRunDoesNotConsultPlacement(t *testing.T) {
	local := &fakeRunner{initSID: "local-session"}
	placement := &fakeRunPlacement{runner: &fakeRunner{}}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(local), WithRunPlacement(placement), WithCredentialSource(staticCred()),
		WithLaunchGate(&spyGate{inner: LaunchDecision{Allowed: true}}))
	if _, err := createProfiledTestRun(t, m, t.Context(), tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:u1", ActorKind: "user",
	}); err != nil {
		t.Fatalf("create local run: %v", err)
	}
	if n := launchCount(local); n != 1 {
		t.Fatalf("local launches = %d, want 1", n)
	}
	if checks := placement.checked(); len(checks) != 0 {
		t.Fatalf("local launch consulted placement for %v", checks)
	}
}

func TestRunOnRefusesCentralLaunchInputs(t *testing.T) {
	for name, params := range map[string]CreateRunParams{
		"git_read":             {GitRead: "repo-one"},
		"worktree":             {Worktree: true},
		"managed credentials":  {Credentials: []string{"credential-one"}},
		"vault secret env":     {SecretEnv: []SecretEnvRef{{Env: "API_TOKEN", Secret: "env/token"}}},
		"registered workspace": {WorkspaceRef: "workspace-one"},
		"unowned directory":    {WorkspaceDir: "/central/workspace"},
		"repository checkout":  {RepositoryURL: "https://example.com/repo.git"},
	} {
		t.Run(name, func(t *testing.T) {
			local, remote := &fakeRunner{}, &fakeRunner{}
			m, _, tenant, _ := newRuntimeHarness(t, WithRunner(local), WithRunPlacement(&fakeRunPlacement{runner: remote}))
			params.RunOn = "node-remote"
			_, err := m.createRun(t.Context(), tenant, params)
			var re *runErr
			if !errors.As(err, &re) || re.status != http.StatusConflict || !strings.Contains(re.msg, "remote") {
				t.Fatalf("remote %s: %v, want remote-input conflict", name, err)
			}
			if launchCount(local)+launchCount(remote) != 0 {
				t.Fatal("refused launch started a child")
			}
		})
	}
}

func TestRunOnRefusesProfileRegistries(t *testing.T) {
	runner := &fakeRunner{}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunPlacement(&fakeRunPlacement{runner: runner}))
	ref := declareRegistries(t, m, tenant, []SessionRegistry{{Kind: "npm", URL: "https://registry.npmjs.org"}}, nil)
	_, err := m.createRun(t.Context(), tenant, CreateRunParams{RunOn: "node-remote", ProviderProfileRef: ref})
	var re *runErr
	if !errors.As(err, &re) || re.status != http.StatusConflict || !strings.Contains(re.msg, "remote") {
		t.Fatalf("remote registries: %v, want remote-input conflict", err)
	}
	if launchCount(runner) != 0 {
		t.Fatal("refused registry launch started a child")
	}
}

func TestRunOnPlacementStoreFailureIsUnavailable(t *testing.T) {
	for _, failure := range []error{errors.New("store down"), context.Canceled} {
		m := New(WithRunPlacement(&fakeRunPlacement{err: failure}))
		_, err := m.checkRunPlacement(t.Context(), model.TenantID("tenant"), "node-remote", auth.Principal{})
		var re *runErr
		if !errors.As(err, &re) || re.status != http.StatusServiceUnavailable {
			t.Fatalf("placement error %v: %v, want 503", failure, err)
		}
	}
}

func TestRunOnRefusesCentralLaunchHandles(t *testing.T) {
	for name, spec := range map[string]LaunchSpec{
		"scratch home":   {ScratchHome: func(string) error { return nil }},
		"start callback": {OnStart: func(int) {}},
		"open files":     {ConfinementFiles: []*os.File{nil}},
	} {
		t.Run(name, func(t *testing.T) {
			spec.RunOn = "node-remote"
			if err := remoteLaunchSpecRefusal(spec); err == nil || !strings.Contains(err.Error(), "remote") {
				t.Fatalf("%s was not refused: %v", name, err)
			}
		})
	}
}

func TestRunOnLostNodeNamesTheUnavailableNode(t *testing.T) {
	runner := &waitErrRunner{err: errors.Join(ErrChildNotReaped, ErrRemoteNodeUnavailable)}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunPlacement(&fakeRunPlacement{runner: runner}), WithCredentialSource(staticCred()))
	dto, err := createProfiledTestRun(t, m, t.Context(), tenant, CreateRunParams{RunOn: "node-remote", Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:u1", ActorKind: model.ActorUser})
	if err != nil {
		t.Fatal(err)
	}
	live, ok := m.rt.getLive(tenant, dto.RunRef)
	if !ok {
		t.Fatal("missing remote process")
	}
	runner.last().finish()
	select {
	case <-live.finalizedCh:
	case <-time.After(3 * time.Second):
		t.Fatal("remote process did not finalize")
	}
	got, err := m.getRun(t.Context(), tenant, dto.RunRef)
	if err != nil || !strings.Contains(got.Reason, "node node-remote unavailable") {
		t.Fatalf("remote exit reason=%q error=%v", got.Reason, err)
	}
}

func TestRunOnPlacementUnavailableNodeIsConflict(t *testing.T) {
	m := New(WithRunPlacement(&fakeRunPlacement{err: ErrRunNodeUnavailable}))
	_, err := m.checkRunPlacement(t.Context(), model.TenantID("tenant"), "node-remote", auth.Principal{})
	var re *runErr
	if !errors.As(err, &re) || re.status != http.StatusConflict {
		t.Fatalf("offline node = %v, want 409", err)
	}
}

func TestRunOnRefusesDecoysBeforePlanting(t *testing.T) {
	m, _, tenant, _ := newRuntimeHarness(t, WithRunPlacement(&fakeRunPlacement{runner: &fakeRunner{}}))
	source := &decoyFixture{}
	m.rt.SessionDecoys = source
	_, err := m.createRun(t.Context(), tenant, CreateRunParams{RunOn: "node-remote"})
	var re *runErr
	if !errors.As(err, &re) || re.status != http.StatusConflict {
		t.Fatalf("remote decoys = %v, want 409", err)
	}
	if source.calls != 0 {
		t.Fatal("remote refusal planted decoys centrally")
	}
}

func TestRunOnRefusesControlsWithoutNetworkPolicy(t *testing.T) {
	for name, spec := range map[string]LaunchSpec{
		"Codex MCP":      {Args: []string{"-c", `mcp_servers.olivares.url="http://127.0.0.1:9999/session/mcp"`}},
		"session mcp":    {SessionMCPURL: "http://127.0.0.1:9999/session/mcp"},
		"MCP config":     {Args: []string{"--mcp-config", "/central/mcp.json"}},
		"hook helper":    {HelperPath: "/central/olivares"},
		"Claude control": {Env: []EnvVar{{Name: envHookPEPURL, Value: "http://127.0.0.1:9999/permission-prompt"}}},
		"Codex control":  {Env: []EnvVar{{Name: "OLIVARES_CODEX_HOOK_URL", Value: "http://127.0.0.1:9999/permission-prompt"}}},
		"Grok control":   {Env: []EnvVar{{Name: "OLIVARES_GROK_HOOK_URL", Value: "http://127.0.0.1:9999/permission-prompt"}}},
	} {
		t.Run(name, func(t *testing.T) {
			spec.RunOn = "node-remote"
			if err := remoteLaunchSpecRefusal(spec); err == nil {
				t.Fatal("central control without relay policy was accepted")
			}
			spec.NetworkPolicy = &egress.Policy{Controls: []string{"http://127.0.0.1:9999/permission-prompt", "http://127.0.0.1:9999/session/mcp"}}
			if err := remoteLaunchSpecRefusal(spec); err != nil {
				t.Fatalf("relayed remote control refused: %v", err)
			}
		})
	}
}

func TestRunOnQueuedLaunchKeepsAuthenticatedPrincipal(t *testing.T) {
	gate := &controlledLaunchApproval{}
	runner := &fakeRunner{initSID: "remote-approved"}
	placement := &fakeRunPlacement{runner: runner, requireBound: true}
	m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunPlacement(placement), WithCredentialSource(staticCred()), WithLaunchGate(gate))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "remote-approval")
	issuer := auth.NewAuthenticator(h.st, nil)
	m.QueuedCredentialCapture = issuer.BindQueuedCredential
	m.QueuedLaunchAuthorization = func(ctx context.Context, _ model.TenantID, c auth.QueuedCredential, _ string, _ model.ID) (auth.Principal, error) {
		return issuer.RevalidateQueuedCredential(ctx, c)
	}
	got := h.doJSON("POST", "/v1/m/sessions/runs", admin, map[string]any{"provider_profile_ref": ensureRuntimeTestProfileRef(t, m, tenant), "run_on": "node-remote"}, tenantHdr(tenant))
	if got.code != http.StatusAccepted || launchCount(runner) != 0 {
		t.Fatalf("queued remote run = %d %s", got.code, got.raw)
	}
	placement.mu.Lock()
	before := len(placement.principals)
	placement.mu.Unlock()
	gate.approved.Store(true)
	waitFor(t, "approved remote launch", func() bool { return launchCount(runner) > 0 })
	placement.mu.Lock()
	defer placement.mu.Unlock()
	if len(placement.principals) <= before {
		t.Fatal("queued launch did not reauthorize remote placement")
	}
	want, _ := placement.principals[0].Ref()
	for _, principal := range placement.principals {
		if got, bound := principal.Ref(); !bound || got != want {
			t.Fatal("remote placement lost authenticated credential")
		}
	}
}

func TestRunOnAPIRefusesCentralInputsBeforeDefaultProfile(t *testing.T) {
	t.Run("provider account home", func(t *testing.T) {
		m := New()
		err := m.refuseRemoteInputs(CreateRunParams{RunOn: "node-remote", ProviderHome: &ProviderHomeSnapshot{AuthSource: AuthSourceAccountHome, ConfigHome: "/central/.claude", UserHome: "/central/home"}})
		if err == nil {
			t.Fatal("remote launch accepted a provider login held only in the central account home")
		}
	})

	m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunPlacement(&fakeRunPlacement{runner: &fakeRunner{}}))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "remote-input-preflight")
	for name, body := range map[string]map[string]any{
		"git credential":      {"git_read": "repo-one"},
		"worktree":            {"worktree": true},
		"workspace":           {"workspace_ref": "workspace-one"},
		"registry credential": {"credentials": []string{"credential-one"}},
		"checkout":            {"repository_url": "https://example.com/repo.git"},
	} {
		t.Run(name, func(t *testing.T) {
			body["run_on"] = "node-remote"
			got := h.doJSON("POST", "/v1/m/sessions/runs", admin, body, tenantHdr(tenant))
			if got.code != http.StatusConflict || !strings.Contains(got.raw, "remote") {
				t.Fatalf("remote input preflight = %d %s", got.code, got.raw)
			}
			profiles, _, err := m.ListProfiles(t.Context(), tenant, "", model.Query{Limit: 50})
			if err != nil || len(profiles) != 0 {
				t.Fatalf("refusal created default profiles: %v (%v)", profiles, err)
			}
		})
	}
}

func TestRunOnRejectsUnsupportedExecutionContexts(t *testing.T) {
	cases := map[string]CreateRunParams{
		"container": {Isolation: IsolationContainer}, "sandbox": {Isolation: IsolationSandbox},
		"cloud remote control": {Transport: TransportRemoteControl},
	}
	for _, driver := range []string{providerDriverCodex, providerDriverOpenCode, providerDriverGemini, providerDriverGrok, providerDriverCursor} {
		cases[driver] = CreateRunParams{ProviderHome: &ProviderHomeSnapshot{Driver: driver, AuthSource: AuthSourceManagedInjection}}
	}
	for name, params := range cases {
		t.Run(name, func(t *testing.T) {
			m := New()
			params.RunOn = "node-remote"
			if err := m.refuseRemoteInputs(params); err == nil {
				t.Fatal("remote launch accepted central execution context")
			}
			params.RunOn = ""
			if err := m.refuseRemoteInputs(params); err != nil {
				t.Fatalf("local context was changed: %v", err)
			}
		})
	}
}

func TestRunOnRefusesAssignedSkillFilesBeforeDelivery(t *testing.T) {
	runner := &fakeRunner{}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunPlacement(&fakeRunPlacement{runner: runner}), WithCredentialSource(staticCred()), WithConfinement([]string{t.TempDir()}, true))
	root := t.TempDir()
	selection := skills.Selection{Digest: "selection", Members: []skills.SelectedMember{{Name: "research", PackID: model.NewID().String(), RevisionID: model.NewID().String(), ManifestDigest: "pack-sha", ContentDigest: "skill-sha"}}}
	source := sessionSkillsFixture{selection: selection, files: []skills.DeliveryFile{{Path: "research/SKILL.md", Bytes: []byte("---\nname: research\ndescription: Research.\n---\nRead primary sources.\n"), Mode: 0444}}}
	if err := m.UseSessionSkills(source, root); err != nil {
		t.Fatal(err)
	}
	_, err := createProfiledTestRun(t, m, t.Context(), tenant, CreateRunParams{RunOn: "node-remote", Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:u1", ActorKind: model.ActorUser})
	var re *runErr
	if !errors.As(err, &re) || re.status != http.StatusConflict || !strings.Contains(re.msg, "skill") {
		t.Fatalf("remote skill delivery = %v, want explicit conflict", err)
	}
	if launchCount(runner) != 0 {
		t.Fatal("unsupported skill launch started a remote child")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("remote refusal wrote central skill files: %v %v", entries, err)
	}
}
