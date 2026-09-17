// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func openCodeRuntimeOptions(extra ...Option) []Option {
	return append([]Option{
		WithRunner(NewProcRunner()),
		WithProviderDriver(NewOpenCodeDriver()),
		WithDriverProgram(providerDriverOpenCode, os.Args[0]),
		WithProductVersion("test"),
		WithStopWaitDelay(2 * time.Second),
		WithDriverTimeouts(20*time.Second, 2*time.Second),
	}, extra...)
}

func openCodeHarness(t *testing.T, authSource string, opts ...Option) (*Module, store.Store, model.TenantID, ProviderProfile) {
	t.Helper()
	m, st, tenant, _ := newRuntimeHarness(t, openCodeRuntimeOptions(opts...)...)
	m.UseExecutionEnvironmentRef(testEnvRef)
	return m, st, tenant, newOpenCodeProfile(t, m, tenant, "opencode-a", authSource)
}

func newOpenCodeProfile(t *testing.T, m *Module, tenant model.TenantID, name, authSource string) ProviderProfile {
	t.Helper()
	config, home := t.TempDir(), t.TempDir()
	return mustCreateProfile(t, m, tenant, CreateProfileInput{
		Driver: providerDriverOpenCode, ConfigHome: config, UserHome: home,
		DisplayName: name, AuthSource: authSource,
	})
}

func openCodeLaunch(t *testing.T, m *Module, tenant model.TenantID, prof ProviderProfile) (runDTO, error) {
	t.Helper()
	return m.createRun(context.Background(), tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: model.ActorUser,
		ProviderProfileRef: prof.Ref,
		EnvAllow:           []string{"FIXTURE_MARKER"},
	})
}

func openCodeScopedAliasSID(t *testing.T, m *Module, tenant model.TenantID, prof ProviderProfile, external string) (string, bool) {
	t.Helper()
	alias, found, err := m.LookupScopedAlias(context.Background(), tenant, prof.Ref, providerDriverOpenCode, external)
	if err != nil {
		t.Fatalf("lookup scoped alias: %v", err)
	}
	if !found {
		return "", false
	}
	return alias.SID, true
}

func TestOpenCodeRuntimeRegistrationIsIndependentOfTheOtherDrivers(t *testing.T) {
	m, _, tenant, _ := newRuntimeHarness(t, openCodeRuntimeOptions()...)
	m.UseExecutionEnvironmentRef(testEnvRef)
	ctx := context.Background()

	openCodeProf := newOpenCodeProfile(t, m, tenant, "oc-registered", AuthSourceAccountHome)
	codexProf := mustCreateProfile(t, m, tenant, CreateProfileInput{
		Driver: providerDriverCodex, ConfigHome: t.TempDir(), UserHome: t.TempDir(),
		DisplayName: "codex-unregistered", AuthSource: AuthSourceAccountHome,
	})
	setOpenCodeFixture(t, openCodeProf, openCodeFixture{SessionID: "ses-reg"})
	if _, err := openCodeLaunch(t, m, tenant, openCodeProf); err != nil {
		t.Fatalf("registered OpenCode launch: %v", err)
	}
	if _, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: model.ActorUser, ProviderProfileRef: codexProf.Ref,
	}); err == nil {
		t.Fatal("an unregistered Codex profile must stay unlaunchable")
	}
}

func TestOpenCodeRuntimeLaunchOwnsTheChildAndBindsItsConversation(t *testing.T) {
	t.Setenv("FIXTURE_MARKER", "marker-value")
	t.Setenv("OPENCODE_SERVER_PASSWORD", "host-secret-never-inherited")
	t.Setenv(envOpenCodeConfigDir, "/host/config/never/inherited")
	t.Setenv(envXDGRuntimeDir, "/run/user/host")

	m, _, tenant, prof := openCodeHarness(t, AuthSourceAccountHome)
	record := setOpenCodeFixture(t, prof, openCodeFixture{SessionID: "ses-alpha"})
	dto, err := openCodeLaunch(t, m, tenant, prof)
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	t.Cleanup(func() { _, _ = m.stopRun(context.Background(), tenant, dto.RunRef, "user:u1", model.ActorUser) })
	if dto.State != stateRunning {
		t.Fatalf("state = %q", dto.State)
	}
	if dto.ProviderDriver != providerDriverOpenCode {
		t.Fatalf("driver = %q", dto.ProviderDriver)
	}
	if dto.ProviderConversationID != "ses-alpha" {
		t.Fatalf("provider_conversation_id = %q", dto.ProviderConversationID)
	}
	waitFor(t, "the provider readiness reached the row", func() bool {
		d, _ := m.getRun(context.Background(), tenant, dto.RunRef)
		return d.ProviderAuthState == AuthStateUnknown
	})
	sid, found := openCodeScopedAliasSID(t, m, tenant, prof, "ses-alpha")
	if !found {
		t.Fatal("the launch did not commit a profile-scoped alias")
	}
	rec := codexRunRecord(t, m, tenant, dto.RunRef)
	if rec.String(colRunClaimSID) != sid {
		t.Fatalf("the alias resolves to %q but the run's claim is %q", sid, rec.String(colRunClaimSID))
	}

	peer := readOpenCodeFixtureRecord(t, record)
	if want := []string{"acp", "--hostname", "127.0.0.1", "--cwd", peer.Cwd}; strings.Join(peer.Argv, " ") != strings.Join(want, " ") {
		if got := strings.Join(peer.Argv, " "); !strings.HasPrefix(got, "acp --hostname 127.0.0.1") {
			t.Fatalf("argv = %v", peer.Argv)
		}
	}
	if peer.Env[envOpenCodeConfigDir] != prof.ConfigHome {
		t.Fatalf("OPENCODE_CONFIG_DIR = %q, want %q", peer.Env[envOpenCodeConfigDir], prof.ConfigHome)
	}
	if peer.Env["HOME"] != prof.UserHome {
		t.Fatalf("HOME = %q, want %q", peer.Env["HOME"], prof.UserHome)
	}
	if peer.Env[envXDGConfigHome] != filepath.Join(prof.UserHome, ".config") {
		t.Fatalf("XDG_CONFIG_HOME = %q", peer.Env[envXDGConfigHome])
	}
	if peer.Env[envXDGDataHome] != filepath.Join(prof.UserHome, ".local", "share") {
		t.Fatalf("XDG_DATA_HOME = %q", peer.Env[envXDGDataHome])
	}
	if peer.Env[envXDGStateHome] != filepath.Join(prof.UserHome, ".local", "state") {
		t.Fatalf("XDG_STATE_HOME = %q", peer.Env[envXDGStateHome])
	}
	if peer.Env[envXDGCacheHome] != filepath.Join(prof.UserHome, ".cache") {
		t.Fatalf("XDG_CACHE_HOME = %q", peer.Env[envXDGCacheHome])
	}
	if _, present := peer.Env[envXDGRuntimeDir]; present {
		t.Fatalf("XDG_RUNTIME_DIR must not be set: %q", peer.Env[envXDGRuntimeDir])
	}
	if peer.Env[envOpenCodeDisableAutoUpdate] != "1" {
		t.Fatalf("version pin missing: %q", peer.Env[envOpenCodeDisableAutoUpdate])
	}
	if peer.Present["OPENCODE_SERVER_PASSWORD"] || peer.Present[envOpenCodeConfig] || peer.Present[envOpenCodeConfigContent] {
		t.Fatalf("ambient OpenCode routing reached the child: %+v", peer.Present)
	}
	if peer.Env["FIXTURE_MARKER"] != "marker-value" {
		t.Fatalf("allowlisted marker missing: %q", peer.Env["FIXTURE_MARKER"])
	}
	if !processRunning(int(*dto.PID)) {
		t.Fatal("the owned child is not running")
	}
	want := []string{openCodeMethodInitialize, openCodeMethodSessionNew}
	if len(peer.Methods) < len(want) {
		t.Fatalf("methods = %v", peer.Methods)
	}
	for i, method := range want {
		if peer.Methods[i] != method {
			t.Fatalf("methods = %v, want handshake %v", peer.Methods, want)
		}
	}
	for _, method := range peer.Methods {
		if method == "authenticate" {
			t.Fatal("authenticate must not be sent")
		}
	}
	for i, envelope := range peer.Envelopes {
		if envelope != "2.0" {
			t.Fatalf("frame %d carried jsonrpc %q", i, envelope)
		}
	}
}

func TestOpenCodeRuntimeResumeUsesTheStoredConversationAndRefusesToFallBack(t *testing.T) {
	m, _, tenant, prof := openCodeHarness(t, AuthSourceAccountHome)
	setOpenCodeFixture(t, prof, openCodeFixture{SessionID: "ses-resume"})
	first, err := openCodeLaunch(t, m, tenant, prof)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := m.stopRun(context.Background(), tenant, first.RunRef, "user:u1", model.ActorUser); err != nil {
		t.Fatalf("stop: %v", err)
	}
	resumeRecord := filepath.Join(t.TempDir(), "resume.json")
	setOpenCodeFixture(t, prof, openCodeFixture{RecordPath: resumeRecord, ResumeEmptyObject: true})
	resumed, err := m.resumeRun(context.Background(), tenant, first.RunRef, "user:u1", model.ActorUser, "")
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	t.Cleanup(func() { _, _ = m.stopRun(context.Background(), tenant, resumed.RunRef, "user:u1", model.ActorUser) })
	if resumed.ProviderConversationID != "ses-resume" {
		t.Fatalf("resumed conversation = %q", resumed.ProviderConversationID)
	}
	peer := readOpenCodeFixtureRecord(t, resumeRecord)
	foundResume := false
	for _, method := range peer.Methods {
		if method == openCodeMethodSessionNew {
			t.Fatal("resume fell back to session/new")
		}
		if method == openCodeMethodSessionResume {
			foundResume = true
		}
	}
	if !foundResume {
		t.Fatalf("methods = %v, want session/resume", peer.Methods)
	}

	failRecord := filepath.Join(t.TempDir(), "fail.json")
	setOpenCodeFixture(t, prof, openCodeFixture{RecordPath: failRecord, FailResume: true})
	if _, err := m.stopRun(context.Background(), tenant, resumed.RunRef, "user:u1", model.ActorUser); err != nil {
		t.Fatalf("stop before failed resume: %v", err)
	}
	if _, err := m.resumeRun(context.Background(), tenant, first.RunRef, "user:u1", model.ActorUser, ""); err == nil {
		t.Fatal("a failed resume must refuse")
	}
	failPeer := readOpenCodeFixtureRecord(t, failRecord)
	for _, method := range failPeer.Methods {
		if method == openCodeMethodSessionNew || method == openCodeMethodSessionLoad {
			t.Fatalf("failed resume fell back to %s", method)
		}
	}
}

func TestOpenCodeRuntimeResumeMissingResultIsNotSuccess(t *testing.T) {
	m, _, tenant, prof := openCodeHarness(t, AuthSourceAccountHome)
	setOpenCodeFixture(t, prof, openCodeFixture{SessionID: "ses-env"})
	first, err := openCodeLaunch(t, m, tenant, prof)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := m.stopRun(context.Background(), tenant, first.RunRef, "user:u1", model.ActorUser); err != nil {
		t.Fatalf("stop: %v", err)
	}
	setOpenCodeFixture(t, prof, openCodeFixture{ResumeMissingResult: true})
	if _, err := m.resumeRun(context.Background(), tenant, first.RunRef, "user:u1", model.ActorUser, ""); err == nil {
		t.Fatal("a missing resume result must not succeed")
	}
}

func TestOpenCodeRuntimeAuthRequiredOnSessionNewFailsHonestly(t *testing.T) {
	m, _, tenant, prof := openCodeHarness(t, AuthSourceAccountHome)
	record := setOpenCodeFixture(t, prof, openCodeFixture{NewSessionAuthRequired: true})
	_, err := openCodeLaunch(t, m, tenant, prof)
	if err == nil {
		t.Fatal("auth-required session/new must fail the launch")
	}
	var re *runErr
	if !errors.As(err, &re) || re.status != http.StatusUnprocessableEntity {
		t.Fatalf("err = %v", err)
	}
	peer := readOpenCodeFixtureRecord(t, record)
	for _, method := range peer.Methods {
		if method == "authenticate" {
			t.Fatal("authenticate must not be sent")
		}
	}
}

func TestOpenCodeRuntimeManagedInjectionDeniesAndNeverFallsBack(t *testing.T) {
	m, _, tenant, prof := openCodeHarness(t, AuthSourceManagedInjection)
	record := setOpenCodeFixture(t, prof, openCodeFixture{SessionID: "ses-managed"})
	_, err := openCodeLaunch(t, m, tenant, prof)
	if err == nil {
		t.Fatal("managed injection without an adapter must be denied")
	}
	if !strings.Contains(err.Error(), providerDriverOpenCode) {
		t.Fatalf("the refusal must name the driver: %v", err)
	}
	if _, ok := tryReadOpenCodeFixtureRecord(record); ok {
		t.Fatal("no child may be spawned")
	}

	m2, _, tenant2, prof2 := openCodeHarness(t, AuthSourceManagedInjection,
		WithProviderCredentialSource(providerDriverOpenCode, ProviderCredentialSourceFunc(
			func(_ context.Context, req ProviderCredentialRequest) (ProviderCredential, error) {
				return ProviderCredential{
					ID: "bad", Scheme: "fixture", NotAfter: farFuture,
					Env: []EnvVar{{Name: envXDGDataHome, Value: "/elsewhere"}},
				}, nil
			})))
	record2 := setOpenCodeFixture(t, prof2, openCodeFixture{SessionID: "ses-managed"})
	if _, err := openCodeLaunch(t, m2, tenant2, prof2); err == nil {
		t.Fatal("an adapter that names reserved XDG must be refused")
	}
	if _, ok := tryReadOpenCodeFixtureRecord(record2); ok {
		t.Fatal("a refused adapter must not spawn")
	}
}

func TestOpenCodeRuntimeRefusesHomeOverridesFromEveryDirection(t *testing.T) {
	for _, name := range []string{
		"HOME", envOpenCodeConfigDir, envOpenCodeConfig, envOpenCodeConfigContent, envOpenCodeTUIConfig,
		envXDGConfigHome, envXDGDataHome, envXDGStateHome, envXDGCacheHome, envXDGRuntimeDir,
	} {
		m, _, tenant, prof := openCodeHarness(t, AuthSourceAccountHome)
		_, err := m.createRun(context.Background(), tenant, CreateRunParams{
			Transport: TransportStreamJSON, Isolation: IsolationNative,
			Actor: "user:u1", ActorKind: model.ActorUser,
			ProviderProfileRef: prof.Ref, EnvAllow: []string{name},
		})
		if err == nil {
			t.Fatalf("env_allow %q must be refused for an OpenCode launch", name)
		}
	}
	m, _, tenant, prof := openCodeHarness(t, AuthSourceAccountHome,
		WithLaunchGate(injectingGate{env: []EnvVar{{Name: envXDGDataHome, Value: "/elsewhere"}}}))
	setOpenCodeFixture(t, prof, openCodeFixture{SessionID: "ses-guard"})
	if _, err := openCodeLaunch(t, m, tenant, prof); err == nil {
		t.Fatal("a launch gate that injects OpenCode XDG must be refused")
	}

	if err := validateOpenCodeEnvAllow(providerDriverGrok, []string{envXDGDataHome, envXDGRuntimeDir}); err != nil {
		t.Fatalf("XDG reservation must not apply to existing drivers: %v", err)
	}
}

func TestOpenCodeRuntimeResumeRefusesATemplateTightenedAfterCreate(t *testing.T) {
	ctx := context.Background()
	inference := &countingCredentialSource{}
	gate := &recordingLaunchGate{dec: LaunchDecision{Allowed: true}}
	adapterCalls := 0
	m, _, tenant, prof := openCodeHarness(t, AuthSourceAccountHome,
		WithCredentialSource(inference),
		WithLaunchGate(gate),
		WithProviderCredentialSource(providerDriverOpenCode, ProviderCredentialSourceFunc(
			func(context.Context, ProviderCredentialRequest) (ProviderCredential, error) {
				adapterCalls++
				return ProviderCredential{}, errors.New("adapter must not run on this refusal")
			},
		)),
	)
	id := seedTemplate(t, m, tenant, "Bounded", tplBody{Policies: &tplPolicies{MaxSessionDurationMinutes: 60}})
	setOpenCodeFixture(t, prof, openCodeFixture{SessionID: "ses-tpl"})
	first, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: model.ActorUser,
		ProviderProfileRef: prof.Ref, TemplateID: id,
	})
	if err != nil {
		t.Fatalf("create under a template with no unsupported control: %v", err)
	}
	if _, err := m.stopRun(ctx, tenant, first.RunRef, "user:u1", model.ActorUser); err != nil {
		t.Fatalf("stop: %v", err)
	}
	retermTemplate(t, m, tenant, id, tplBody{
		Settings: &tplSettings{CustomInstructions: "never discard this"},
		Policies: &tplPolicies{MaxSessionDurationMinutes: 60, AllowedTools: []string{"Read"}},
	})
	if _, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: model.ActorUser,
		ProviderProfileRef: prof.Ref, TemplateID: id,
	}); err == nil {
		t.Fatal("create under the tightened template must be refused")
	}

	gate.called = false
	inference.mu.Lock()
	beforeMint := inference.calls
	inference.mu.Unlock()
	resumeRecord := filepath.Join(t.TempDir(), "resume.json")
	setOpenCodeFixture(t, prof, openCodeFixture{RecordPath: resumeRecord, ResumeEmptyObject: true})
	_, resumeErr := m.resumeRun(ctx, tenant, first.RunRef, "user:u1", model.ActorUser, "")
	if resumeErr == nil {
		t.Fatal("resume under the tightened template must be refused")
	}
	if !strings.Contains(resumeErr.Error(), "template") &&
		!strings.Contains(resumeErr.Error(), "tool restrictions") &&
		!strings.Contains(resumeErr.Error(), "instructions") {
		t.Fatalf("resume err = %v, want the unsupported-control refusal", resumeErr)
	}
	if _, ok := tryReadOpenCodeFixtureRecord(resumeRecord); ok {
		t.Fatal("a refused resume must not spawn a child")
	}
	if gate.called {
		t.Fatal("a refused resume must not reach the launch gate")
	}
	inference.mu.Lock()
	afterMint := inference.calls
	inference.mu.Unlock()
	if afterMint != beforeMint {
		t.Fatalf("a refused resume minted credentials: before=%d after=%d", beforeMint, afterMint)
	}
	if adapterCalls != 0 {
		t.Fatalf("a refused resume reached the provider adapter: %d", adapterCalls)
	}
	stopped, err := m.getRun(ctx, tenant, first.RunRef)
	if err != nil {
		t.Fatalf("getRun: %v", err)
	}
	if stopped.State != stateStopped {
		t.Fatalf("state after refused resume = %q, want stopped", stopped.State)
	}
}

func TestOpenCodeRuntimeRefusesUnsupportedTemplateControls(t *testing.T) {
	m, _, tenant, prof := openCodeHarness(t, AuthSourceAccountHome)
	setOpenCodeFixture(t, prof, openCodeFixture{SessionID: "ses-tpl"})
	if _, err := m.createRun(context.Background(), tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: model.ActorUser,
		ProviderProfileRef: prof.Ref, Instructions: "never discard this",
	}); err == nil {
		t.Fatal("template instructions must be refused")
	}
	if _, err := m.createRun(context.Background(), tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: model.ActorUser,
		ProviderProfileRef: prof.Ref, AllowedTools: []string{"Bash"},
	}); err == nil {
		t.Fatal("template tool restrictions must be refused")
	}
}

func TestOpenCodeRuntimeApprovalSelectsOnceAndNeverAlways(t *testing.T) {
	gate := &grokGate{
		decision: ProviderApprovalDecision{Allow: true, Granted: []string{"once", "always"}, SessionScope: true},
		seen:     make(chan ProviderApprovalRequest, 4),
	}
	m, _, tenant, prof := openCodeHarness(t, AuthSourceAccountHome, WithProviderApprovalGate(gate))
	record := setOpenCodeFixture(t, prof, openCodeFixture{
		SessionID: "ses-approve", PromptMode: "hold",
		PromptReleasePath: filepath.Join(t.TempDir(), "never"), PermissionOnTurn: "all",
	})
	ctx := context.Background()
	dto, err := openCodeLaunch(t, m, tenant, prof)
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	t.Cleanup(func() { _, _ = m.stopRun(context.Background(), tenant, dto.RunRef, "user:u1", model.ActorUser) })
	if err := m.sendTextInput(ctx, tenant, dto.RunRef, "run something"); err != nil {
		t.Fatalf("input: %v", err)
	}
	waitFor(t, "the child received the approval answer", func() bool {
		return len(readOpenCodeFixtureRecord(t, record).Replies) > 0
	})
	peer := readOpenCodeFixtureRecord(t, record)
	var reply openCodeRequestPermissionResponse
	if err := json.Unmarshal(peer.Replies[0], &reply); err != nil {
		t.Fatalf("the approval answer is not a permission response: %s", peer.Replies[0])
	}
	if reply.Outcome.Outcome != openCodeOutcomeSelected || reply.Outcome.OptionID != "once" {
		t.Fatalf("outcome = %+v, want once", reply.Outcome)
	}
	select {
	case req := <-gate.seen:
		if req.Driver != providerDriverOpenCode || req.ConversationID != "ses-approve" || req.TurnID == "" {
			t.Fatalf("authority saw %+v", req)
		}
	default:
		t.Fatal("the authority was never consulted")
	}
}

func TestOpenCodeRuntimeInterruptResolvesAPendingApproval(t *testing.T) {
	gate := &grokGate{
		decision: ProviderApprovalDecision{Allow: true, Granted: []string{"once"}},
		block:    make(chan struct{}),
		seen:     make(chan ProviderApprovalRequest, 1),
	}
	m, _, tenant, prof := openCodeHarness(t, AuthSourceAccountHome, WithProviderApprovalGate(gate))
	record := setOpenCodeFixture(t, prof, openCodeFixture{
		SessionID: "ses-int", PromptMode: "hold",
		PromptReleasePath: filepath.Join(t.TempDir(), "never"), PermissionOnTurn: "all",
	})
	ctx := context.Background()
	dto, err := openCodeLaunch(t, m, tenant, prof)
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	t.Cleanup(func() { _, _ = m.stopRun(context.Background(), tenant, dto.RunRef, "user:u1", model.ActorUser) })
	if err := m.sendTextInput(ctx, tenant, dto.RunRef, "run something"); err != nil {
		t.Fatalf("input: %v", err)
	}
	waitFor(t, "the authority was entered", func() bool { return len(gate.seen) > 0 })
	if _, err := m.interruptRun(ctx, tenant, dto.RunRef, "user:u1", model.ActorUser); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	close(gate.block)
	waitFor(t, "the child received a refusal", func() bool {
		return len(readOpenCodeFixtureRecord(t, record).Replies) > 0
	})
	peer := readOpenCodeFixtureRecord(t, record)
	var reply openCodeRequestPermissionResponse
	if err := json.Unmarshal(peer.Replies[0], &reply); err != nil {
		t.Fatalf("reply: %v", err)
	}
	if reply.Outcome.OptionID == "once" {
		t.Fatalf("interrupt granted once: %+v", reply.Outcome)
	}
}

func TestOpenCodeRuntimeApprovalRefusesWhenTheCurrentAuthorityIsGone(t *testing.T) {
	var m *Module
	var tenant model.TenantID
	var prof ProviderProfile
	gate := &grokGate{decision: ProviderApprovalDecision{Allow: true, Granted: []string{"once"}}}
	gate.before = func() {
		retired := ProfileRetired
		if _, err := m.PatchProfile(context.Background(), tenant, prof.Ref, ProfilePatch{State: &retired}); err != nil {
			panic("retire the profile: " + err.Error())
		}
	}
	m, _, tenant, prof = openCodeHarness(t, AuthSourceAccountHome, WithProviderApprovalGate(gate))
	record := setOpenCodeFixture(t, prof, openCodeFixture{
		SessionID: "ses-revoked", PromptMode: "hold",
		PromptReleasePath: filepath.Join(t.TempDir(), "never"), PermissionOnTurn: "all",
	})
	ctx := context.Background()
	dto, err := openCodeLaunch(t, m, tenant, prof)
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	if err := m.sendTextInput(ctx, tenant, dto.RunRef, "run something"); err != nil {
		t.Fatalf("input: %v", err)
	}
	waitFor(t, "the child received the approval answer", func() bool {
		return len(readOpenCodeFixtureRecord(t, record).Replies) > 0
	})
	var reply openCodeRequestPermissionResponse
	if err := json.Unmarshal(readOpenCodeFixtureRecord(t, record).Replies[0], &reply); err != nil {
		t.Fatalf("reply: %v", err)
	}
	if reply.Outcome.Outcome != openCodeOutcomeCancelled {
		t.Fatalf("outcome = %+v, want cancelled", reply.Outcome)
	}
	lr, _ := m.rt.getLive(tenant, dto.RunRef)
	stopCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := m.Stop(stopCtx); err != nil {
		t.Fatalf("runtime shutdown: %v", err)
	}
	waitFor(t, "the child is gone", func() bool { return !processRunning(lr.proc.PID()) })
}

func TestOpenCodeRuntimeRefusesAnUnadvertisedCapabilityRequest(t *testing.T) {
	m, _, tenant, prof := openCodeHarness(t, AuthSourceAccountHome)
	record := setOpenCodeFixture(t, prof, openCodeFixture{
		SessionID: "ses-cap", PromptMode: "hold",
		PromptReleasePath: filepath.Join(t.TempDir(), "never"), UnsupportedRequest: "fs/write_text_file",
	})
	dto, err := openCodeLaunch(t, m, tenant, prof)
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	t.Cleanup(func() { _, _ = m.stopRun(context.Background(), tenant, dto.RunRef, "user:u1", model.ActorUser) })
	if err := m.sendTextInput(context.Background(), tenant, dto.RunRef, "write"); err != nil {
		t.Fatalf("input: %v", err)
	}
	waitFor(t, "the protocol error was answered", func() bool {
		return len(readOpenCodeFixtureRecord(t, record).Replies) > 0
	})
	var refusal rpcError
	if err := json.Unmarshal(readOpenCodeFixtureRecord(t, record).Replies[0], &refusal); err != nil {
		t.Fatalf("the answer is not a protocol error: %v", err)
	}
	if refusal.Code != openCodeErrMethodNotSupported {
		t.Fatalf("code = %d", refusal.Code)
	}
}

func TestOpenCodeRuntimeAForeignNotificationBindsNothing(t *testing.T) {
	m, _, tenant, prof := openCodeHarness(t, AuthSourceAccountHome)
	setOpenCodeFixture(t, prof, openCodeFixture{SessionID: "ses-ours", ForeignUpdate: true})
	dto, err := openCodeLaunch(t, m, tenant, prof)
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	t.Cleanup(func() { _, _ = m.stopRun(context.Background(), tenant, dto.RunRef, "user:u1", model.ActorUser) })
	if err := m.sendTextInput(context.Background(), tenant, dto.RunRef, "hello"); err != nil {
		t.Fatalf("input: %v", err)
	}
	if _, found := openCodeScopedAliasSID(t, m, tenant, prof, "some-other-conversation"); found {
		t.Fatal("a foreign notification bound an alias")
	}
	if dto.ProviderConversationID != "ses-ours" {
		t.Fatalf("conversation = %q", dto.ProviderConversationID)
	}
}

func TestOpenCodeRuntimeStopReapsTheOwnedProcessGroup(t *testing.T) {
	m, _, tenant, prof := openCodeHarness(t, AuthSourceAccountHome)
	record := setOpenCodeFixture(t, prof, openCodeFixture{
		SessionID: "ses-stop", SpawnChild: true, PromptMode: "hold",
		PromptReleasePath: filepath.Join(t.TempDir(), "never"),
	})
	dto, err := openCodeLaunch(t, m, tenant, prof)
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	peer := readOpenCodeFixtureRecord(t, record)
	if peer.ChildPID == 0 {
		t.Fatal("the fixture did not spawn a grandchild")
	}
	if _, err := m.stopRun(context.Background(), tenant, dto.RunRef, "user:u1", model.ActorUser); err != nil {
		t.Fatalf("stop: %v", err)
	}
	waitFor(t, "the child is gone", func() bool { return !processRunning(int(*dto.PID)) })
	waitFor(t, "the grandchild is gone", func() bool { return !processRunning(peer.ChildPID) })
}

func TestOpenCodeRuntimeModelSettingReachesTheChild(t *testing.T) {
	m, _, tenant, prof := openCodeHarness(t, AuthSourceAccountHome)
	record := setOpenCodeFixture(t, prof, openCodeFixture{SessionID: "ses-model", OfferEffort: true})
	dto, err := m.createRun(context.Background(), tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: model.ActorUser,
		ProviderProfileRef: prof.Ref, Model: "anthropic/claude-sonnet-4", Effort: "high",
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	t.Cleanup(func() { _, _ = m.stopRun(context.Background(), tenant, dto.RunRef, "user:u1", model.ActorUser) })
	peer := readOpenCodeFixtureRecord(t, record)
	if strings.Join(peer.ConfigIDs, ",") != "model,effort" {
		t.Fatalf("config ids = %v", peer.ConfigIDs)
	}
	if strings.Join(peer.ConfigValues, ",") != "anthropic/claude-sonnet-4,high" {
		t.Fatalf("config values = %v", peer.ConfigValues)
	}
}
