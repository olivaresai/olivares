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

// The Grok driver's ACCEPTANCE, through the ACTUAL product surfaces.
//
// Every row here goes through the REAL native runner spawning a REAL child, the
// REAL claim and fence, the REAL transactional alias, and — where the claim is
// about a client-visible contract — the REAL HTTP server with authentication,
// tenant resolution and RBAC in front of it. That is the difference the contract
// insists on: a standalone handshake script proves what a CLI does, and proves
// nothing about whether Olivares can own, bind, govern and end a session.
//
// ⛔ WHAT THESE ROWS DO NOT CLAIM. The peer is an owned FAKE ACP child. It proves
// the driver, the runtime and the process boundary. It cannot prove that the
// installed official Grok binary accepts an authenticated turn — no account home
// is opened, no network is reachable, and no model is prompted anywhere in this
// file. That acceptance is separate, later and explicitly root-controlled.

// --- harness -----------------------------------------------------------------

// grokRuntimeOptions is the production wiring with the test binary standing in as
// the official program: native runner, registered Grok driver, per-driver program.
func grokRuntimeOptions(extra ...Option) []Option {
	return append([]Option{
		WithRunner(NewProcRunner()),
		WithProviderDriver(NewGrokDriver()),
		WithDriverProgram(providerDriverGrok, os.Args[0]),
		WithProductVersion("test"),
		WithStopWaitDelay(2 * time.Second),
		WithDriverTimeouts(20*time.Second, 2*time.Second),
	}, extra...)
}

// grokHarness is the white-box half: the real runtime and store, no HTTP.
func grokHarness(t *testing.T, authSource string, opts ...Option) (*Module, store.Store, model.TenantID, ProviderProfile) {
	t.Helper()
	m, st, tenant, _ := newRuntimeHarness(t, grokRuntimeOptions(opts...)...)
	m.UseExecutionEnvironmentRef(testEnvRef)
	return m, st, tenant, newGrokProfile(t, m, tenant, "grok-a", authSource)
}

func newGrokProfile(t *testing.T, m *Module, tenant model.TenantID, name, authSource string) ProviderProfile {
	t.Helper()
	config, home := t.TempDir(), t.TempDir()
	return mustCreateProfile(t, m, tenant, CreateProfileInput{
		Driver: providerDriverGrok, ConfigHome: config, UserHome: home,
		DisplayName: name, AuthSource: authSource,
	})
}

func grokLaunch(t *testing.T, m *Module, tenant model.TenantID, prof ProviderProfile) (runDTO, error) {
	t.Helper()
	return m.createRun(context.Background(), tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: model.ActorUser,
		ProviderProfileRef: prof.Ref,
		EnvAllow:           []string{"FIXTURE_MARKER"},
	})
}

// grokHTTPHarness is the black-box half: the same production wiring behind the
// REAL api.Server, which is what a client actually speaks to.
type grokHTTPFixture struct {
	m      *Module
	h      *harness
	admin  string
	tenant model.TenantID
	prof   ProviderProfile
}

func newGrokHTTPFixture(t *testing.T, org, authSource string, opts ...Option) *grokHTTPFixture {
	t.Helper()
	m := New(grokRuntimeOptions(append(opts, WithSessionWorkspaceRoot(t.TempDir()))...)...)
	m.UseExecutionEnvironmentRef(testEnvRef)
	m.EnableProfiledLaunches()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, org)
	return &grokHTTPFixture{
		m: m, h: h, admin: admin, tenant: tenant,
		prof: newGrokProfile(t, m, tenant, "grok-http", authSource),
	}
}

func (f *grokHTTPFixture) createRun(t *testing.T) string {
	t.Helper()
	created := f.h.doJSON(http.MethodPost, "/v1/m/sessions/runs", f.admin, map[string]any{
		"transport": "stream-json", "permission_mode": "default", "isolation": "native",
		"provider_profile_ref": f.prof.Ref,
	}, tenantHdr(f.tenant))
	runRef, _ := created.body["run_ref"].(string)
	if created.code != http.StatusCreated || runRef == "" {
		t.Fatalf("create profiled run = %d %s", created.code, created.raw)
	}
	t.Cleanup(func() {
		_, _ = f.m.stopRun(context.Background(), f.tenant, runRef, "user:u1", model.ActorUser)
	})
	return runRef
}

func (f *grokHTTPFixture) input(text, runRef string) resp {
	return f.h.doJSON(http.MethodPost, "/v1/m/sessions/runs/"+runRef+"/input", f.admin,
		map[string]any{"text": text}, tenantHdr(f.tenant))
}

func grokScopedAliasSID(t *testing.T, m *Module, tenant model.TenantID, prof ProviderProfile, external string) (string, bool) {
	t.Helper()
	alias, found, err := m.LookupScopedAlias(context.Background(), tenant, prof.Ref, providerDriverGrok, external)
	if err != nil {
		t.Fatalf("lookup scoped alias: %v", err)
	}
	if !found {
		return "", false
	}
	return alias.SID, true
}

// --- row 0: registration is PER DRIVER, and the point is what stays refused ---

// Naming one pinned official binary REGISTERS one driver. It does not turn a
// second provider on, and it does not change the historical Claude path: there is
// no shared switch, which is exactly what makes an unregistered provider honestly
// observable and honestly not launchable rather than a launch that resolves some
// executable off the PATH.
func TestGrokRuntimeRegistrationIsIndependentOfTheOtherDrivers(t *testing.T) {
	m, _, tenant, _ := newRuntimeHarness(t, grokRuntimeOptions()...)
	m.UseExecutionEnvironmentRef(testEnvRef)
	ctx := context.Background()

	grokProf := newGrokProfile(t, m, tenant, "grok-registered", AuthSourceAccountHome)
	if !m.toProfileDTO(grokProf).Operable {
		t.Fatal("the registered grok driver must make its profiles operable")
	}
	// Codex is NOT registered on this node. Registering Grok did nothing for it.
	codexConfig, codexHome := t.TempDir(), t.TempDir()
	codexProf := mustCreateProfile(t, m, tenant, CreateProfileInput{
		Driver: providerDriverCodex, ConfigHome: codexConfig, UserHome: codexHome,
		DisplayName: "codex-unregistered", AuthSource: AuthSourceAccountHome,
	})
	if m.toProfileDTO(codexProf).Operable {
		t.Fatal("registering grok must not make a codex profile launchable")
	}
	_, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: model.ActorUser, ProviderProfileRef: codexProf.Ref,
	})
	if err == nil {
		t.Fatal("a profile whose driver nobody registered must not launch")
	}
	if !strings.Contains(err.Error(), "no operated runner") {
		t.Fatalf("the refusal must say the driver has no runner here: %v", err)
	}
	// And the historical Claude path is untouched by any registration: it is the
	// shipped runner, not a registered driver.
	claudeConfig, claudeHome := t.TempDir(), t.TempDir()
	claudeProf := mustCreateProfile(t, m, tenant, CreateProfileInput{
		Driver: providerDriverClaude, ConfigHome: claudeConfig, UserHome: claudeHome,
		DisplayName: "claude-untouched",
	})
	if !m.toProfileDTO(claudeProf).Operable {
		t.Fatal("registering grok must not disturb the historical Claude path")
	}
	if _, ok := m.driverFor(providerDriverClaude); ok {
		t.Fatal("the Claude path is the shipped runner and must never be a registered driver")
	}
}

// --- row 1: the launch, the argv, the child environment, the alias ------------

func TestGrokRuntimeLaunchOwnsTheChildAndBindsItsConversation(t *testing.T) {
	t.Setenv("FIXTURE_MARKER", "marker-value")
	// Host provider secrets a careless allowlist would forward.
	t.Setenv("XAI_API_KEY", "host-key-never-inherited")
	t.Setenv("GROK_HOME", "/host/home/never/inherited")

	m, _, tenant, prof := grokHarness(t, AuthSourceAccountHome)
	record := setGrokFixture(t, prof, grokFixture{SessionID: "conv-alpha"})
	dto, err := grokLaunch(t, m, tenant, prof)
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	t.Cleanup(func() { _, _ = m.stopRun(context.Background(), tenant, dto.RunRef, "user:u1", model.ActorUser) })
	if dto.State != stateRunning {
		t.Fatalf("state = %q", dto.State)
	}
	if dto.ProviderDriver != providerDriverGrok {
		t.Fatalf("driver = %q", dto.ProviderDriver)
	}
	if dto.ProviderConversationID != "conv-alpha" {
		t.Fatalf("provider_conversation_id = %q; only the correlated root response may nominate it", dto.ProviderConversationID)
	}
	if dto.ProviderAuthSource != AuthSourceAccountHome {
		t.Fatalf("auth source = %q", dto.ProviderAuthSource)
	}
	waitFor(t, "the provider readiness reached the row", func() bool {
		d, _ := m.getRun(context.Background(), tenant, dto.RunRef)
		return d.ProviderAuthState == AuthStateReady
	})
	sid, found := grokScopedAliasSID(t, m, tenant, prof, "conv-alpha")
	if !found {
		t.Fatal("the launch did not commit a profile-scoped alias")
	}
	rec := codexRunRecord(t, m, tenant, dto.RunRef)
	if rec.String(colRunClaimSID) != sid {
		t.Fatalf("the alias resolves to %q but the run's claim is %q", sid, rec.String(colRunClaimSID))
	}

	// Now what the REAL child actually received.
	peer := readGrokFixtureRecord(t, record)
	if want := []string{"agent", "--no-leader", "stdio"}; strings.Join(peer.Argv, " ") != strings.Join(want, " ") {
		t.Fatalf("argv = %v, want %v", peer.Argv, want)
	}
	if peer.Env[envGrokHome] != prof.ConfigHome {
		t.Fatalf("GROK_HOME = %q, want the profile's config home %q", peer.Env[envGrokHome], prof.ConfigHome)
	}
	if peer.Env["HOME"] != prof.UserHome {
		t.Fatalf("HOME = %q, want the profile's user home %q", peer.Env["HOME"], prof.UserHome)
	}
	if peer.Env[envGrokDisableAutoUpdate] != "1" {
		t.Fatalf("the child's version is not pinned: %s=%q", envGrokDisableAutoUpdate, peer.Env[envGrokDisableAutoUpdate])
	}
	for _, foreign := range []string{envCodexHome, envClaudeConfigDir} {
		if _, present := peer.Env[foreign]; present {
			t.Fatalf("a grok child must not receive %s: that variable belongs to another provider", foreign)
		}
	}
	// PRESENCE, never value: the point is which credential families exist on the
	// child, and an evidence file holding one would be the leak this runtime spends
	// its effort preventing.
	for _, name := range []string{"XAI_API_KEY", "GROK_API_KEY", "ANTHROPIC_AUTH_TOKEN", "OPENAI_API_KEY"} {
		if peer.Present[name] {
			t.Fatalf("an account-home launch must inject no credential; %s reached the child", name)
		}
	}
	if peer.Present["DISABLE_AUTOUPDATER"] {
		t.Fatal("DISABLE_AUTOUPDATER is Claude Code's variable and is not invented for another CLI")
	}
	if peer.Env["FIXTURE_MARKER"] != "marker-value" {
		t.Fatalf("an explicitly allowlisted, non-secret variable did not reach the child: %q", peer.Env["FIXTURE_MARKER"])
	}
	if !processRunning(int(*dto.PID)) {
		t.Fatal("the owned child is not running")
	}
	// The handshake is the documented order, every frame carried the ACP envelope,
	// and the client advertised no capability it does not implement.
	want := []string{grokMethodInitialize, grokMethodAuthenticate, grokMethodSessionNew}
	if len(peer.Methods) < len(want) {
		t.Fatalf("methods = %v", peer.Methods)
	}
	for i, method := range want {
		if peer.Methods[i] != method {
			t.Fatalf("methods = %v, want the handshake to begin with %v", peer.Methods, want)
		}
	}
	for i, envelope := range peer.Envelopes {
		if envelope != "2.0" {
			t.Fatalf("frame %d carried jsonrpc %q; ACP requires 2.0", i, envelope)
		}
	}
	if len(peer.AuthMethodIDs) != 1 || peer.AuthMethodIDs[0] != grokAuthMethodCachedToken {
		t.Fatalf("authenticate used %v, want the account home's cached login", peer.AuthMethodIDs)
	}
	var caps struct {
		FS struct {
			ReadTextFile  bool `json:"readTextFile"`
			WriteTextFile bool `json:"writeTextFile"`
		} `json:"fs"`
		Terminal bool `json:"terminal"`
	}
	if err := json.Unmarshal(peer.ClientCaps, &caps); err != nil {
		t.Fatalf("client capabilities: %v (%s)", err, peer.ClientCaps)
	}
	if caps.FS.ReadTextFile || caps.FS.WriteTextFile || caps.Terminal {
		t.Fatalf("the client advertised a capability it does not implement: %s", peer.ClientCaps)
	}
}

// --- row 2: equal external ids across homes are legal and are TWO sessions ----

func TestGrokRuntimeEqualExternalIdsAcrossHomesAreTwoSessions(t *testing.T) {
	m, _, tenant, profA := grokHarness(t, AuthSourceAccountHome)
	profB := newGrokProfile(t, m, tenant, "grok-b", AuthSourceAccountHome)
	// The SAME provider conversation id from two independent homes: legal, and two
	// canonical sessions.
	forced := grokFixture{SessionID: "same-id-both-homes"}
	setGrokFixture(t, profA, forced)
	setGrokFixture(t, profB, forced)

	first, err := grokLaunch(t, m, tenant, profA)
	if err != nil {
		t.Fatalf("first launch: %v", err)
	}
	second, err := grokLaunch(t, m, tenant, profB)
	if err != nil {
		t.Fatalf("second launch: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = m.stopRun(ctx, tenant, first.RunRef, "user:u1", model.ActorUser)
		_, _ = m.stopRun(ctx, tenant, second.RunRef, "user:u1", model.ActorUser)
	})
	if first.RunRef == second.RunRef {
		t.Fatal("two launches produced one run")
	}
	if first.ProviderConversationID != second.ProviderConversationID {
		t.Fatalf("the fixture forced the SAME external id; got %q and %q",
			first.ProviderConversationID, second.ProviderConversationID)
	}
	sidA, okA := grokScopedAliasSID(t, m, tenant, profA, "same-id-both-homes")
	sidB, okB := grokScopedAliasSID(t, m, tenant, profB, "same-id-both-homes")
	if !okA || !okB {
		t.Fatalf("both homes must bind their own scoped alias (a=%t b=%t)", okA, okB)
	}
	if sidA == sidB {
		t.Fatalf("one external id under two homes must be TWO canonical sessions, got %q twice", sidA)
	}
}

// --- row 3: exact resume, and a refusal that never becomes a new conversation --

func TestGrokRuntimeResumeUsesTheStoredConversationAndRefusesToFallBack(t *testing.T) {
	m, _, tenant, prof := grokHarness(t, AuthSourceAccountHome)
	setGrokFixture(t, prof, grokFixture{SessionID: "conv-resume"})
	ctx := context.Background()

	dto, err := grokLaunch(t, m, tenant, prof)
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	firstRec := codexRunRecord(t, m, tenant, dto.RunRef)
	firstLaunch := firstRec.String(colRuntimeLaunchID)
	sid := firstRec.String(colRunClaimSID)
	if _, err := m.stopRun(ctx, tenant, dto.RunRef, "user:u1", model.ActorUser); err != nil {
		t.Fatalf("stopRun: %v", err)
	}

	// A resume is a NEW owned process and a NEW generation on the SAME run, SID and
	// conversation — and the provider answers it with a NULL result, which is the
	// protocol's own legal success. Requiring an id back would fail every one.
	resumeRecord := filepath.Join(t.TempDir(), "resume.json")
	setGrokFixture(t, prof, grokFixture{RecordPath: resumeRecord})
	resumed, err := m.resumeRun(ctx, tenant, dto.RunRef, "user:u1", model.ActorUser, "")
	if err != nil {
		t.Fatalf("resumeRun: %v", err)
	}
	t.Cleanup(func() { _, _ = m.stopRun(context.Background(), tenant, dto.RunRef, "user:u1", model.ActorUser) })
	if resumed.RunRef != dto.RunRef {
		t.Fatalf("resume minted another run: %q", resumed.RunRef)
	}
	if resumed.ProviderConversationID != "conv-resume" {
		t.Fatalf("resume conversation = %q", resumed.ProviderConversationID)
	}
	secondRec := codexRunRecord(t, m, tenant, dto.RunRef)
	if secondRec.String(colRuntimeLaunchID) == firstLaunch {
		t.Fatal("a resume must open a NEW launch generation")
	}
	if secondRec.String(colRunClaimSID) != sid {
		t.Fatal("a resume must keep the Olivares canonical session")
	}
	peer := readGrokFixtureRecord(t, resumeRecord)
	if countMethod(peer.Methods, grokMethodSessionResume) != 1 {
		t.Fatalf("the resume did not use session/resume: %v", peer.Methods)
	}
	if countMethod(peer.Methods, grokMethodSessionNew) != 0 {
		t.Fatalf("a resume must never start a conversation: %v", peer.Methods)
	}
	if len(peer.ResumeIDs) != 1 || peer.ResumeIDs[0] != "conv-resume" {
		t.Fatalf("the resume named %v, want the EXACT stored conversation", peer.ResumeIDs)
	}

	// A resume the provider refuses is a REFUSAL, not a new conversation.
	if _, err := m.stopRun(ctx, tenant, dto.RunRef, "user:u1", model.ActorUser); err != nil {
		t.Fatalf("stopRun: %v", err)
	}
	failRecord := filepath.Join(t.TempDir(), "fail.json")
	setGrokFixture(t, prof, grokFixture{RecordPath: failRecord, FailResume: true})
	if _, err := m.resumeRun(ctx, tenant, dto.RunRef, "user:u1", model.ActorUser, ""); err == nil {
		t.Fatal("a refused resume must not report success")
	}
	failed := readGrokFixtureRecord(t, failRecord)
	if countMethod(failed.Methods, grokMethodSessionNew) != 0 {
		t.Fatalf("a refused resume fell back to session/new: %v", failed.Methods)
	}
	if countMethod(failed.Methods, grokMethodSessionLoad) != 0 {
		t.Fatalf("a refused resume fell back to session/load: %v", failed.Methods)
	}
	// The stored conversation is untouched, so a later resume can still try it.
	if codexRunRecord(t, m, tenant, dto.RunRef).String(colClaudeSessionID) != "conv-resume" {
		t.Fatal("a refused resume must not discard the stored conversation")
	}
}

// A provider that answers the resume with ANOTHER conversation is refused: the
// operator asked to continue theirs, and silently handing them a different one is
// the worst possible success.
func TestGrokRuntimeResumeRefusesAConflictingConversation(t *testing.T) {
	m, _, tenant, prof := grokHarness(t, AuthSourceAccountHome)
	setGrokFixture(t, prof, grokFixture{SessionID: "conv-strict"})
	ctx := context.Background()
	dto, err := grokLaunch(t, m, tenant, prof)
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	if _, err := m.stopRun(ctx, tenant, dto.RunRef, "user:u1", model.ActorUser); err != nil {
		t.Fatalf("stopRun: %v", err)
	}
	setGrokFixture(t, prof, grokFixture{
		RecordPath: filepath.Join(t.TempDir(), "conflict.json"), ResumeSessionID: "a-different-conversation",
	})
	if _, err := m.resumeRun(ctx, tenant, dto.RunRef, "user:u1", model.ActorUser, ""); err == nil {
		t.Fatal("a resume that answered with another conversation must be refused")
	}
	if got := codexRunRecord(t, m, tenant, dto.RunRef).String(colClaudeSessionID); got != "conv-strict" {
		t.Fatalf("the stored conversation was overwritten with %q", got)
	}
	if _, found := grokScopedAliasSID(t, m, tenant, prof, "a-different-conversation"); found {
		t.Fatal("a refused resume bound the conversation it was refused for")
	}
}

// Replay is HISTORY. The updates a load or resume pushes before its correlated
// response are the conversation being replayed, not new live output.
func TestGrokRuntimeResumeReplayIsHistoryNotLiveOutput(t *testing.T) {
	m, _, tenant, prof := grokHarness(t, AuthSourceAccountHome)
	setGrokFixture(t, prof, grokFixture{SessionID: "conv-replay"})
	ctx := context.Background()
	dto, err := grokLaunch(t, m, tenant, prof)
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	if _, err := m.stopRun(ctx, tenant, dto.RunRef, "user:u1", model.ActorUser); err != nil {
		t.Fatalf("stopRun: %v", err)
	}
	setGrokFixture(t, prof, grokFixture{
		RecordPath: filepath.Join(t.TempDir(), "replay.json"), ReplayUpdates: 3,
	})
	if _, err := m.resumeRun(ctx, tenant, dto.RunRef, "user:u1", model.ActorUser, ""); err != nil {
		t.Fatalf("resumeRun: %v", err)
	}
	t.Cleanup(func() { _, _ = m.stopRun(context.Background(), tenant, dto.RunRef, "user:u1", model.ActorUser) })
	lr, ok := m.rt.getLive(tenant, dto.RunRef)
	if !ok {
		t.Fatal("the resumed run is not live")
	}
	session := lr.session.(*grokSession)
	waitFor(t, "the replayed history was classified", func() bool {
		return session.updateCounts().history == 3
	})
	if got := session.updateCounts(); got.live != 0 {
		t.Fatalf("replayed history was counted as live output: %+v", got)
	}
}

// --- row 4: authentication is truthful, and never inferred from a home ---------

func TestGrokRuntimeUnauthenticatedProfileIsTruthfulAndRefusesTurns(t *testing.T) {
	m, _, tenant, prof := grokHarness(t, AuthSourceAccountHome)
	// The empty-home case the standalone probe recorded: the agent advertises only
	// the interactive browser sign-in, which this headless launch cannot complete.
	setGrokFixture(t, prof, grokFixture{SessionID: "conv-unauth", AuthMethods: "browser"})
	ctx := context.Background()
	dto, err := grokLaunch(t, m, tenant, prof)
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	t.Cleanup(func() { _, _ = m.stopRun(context.Background(), tenant, dto.RunRef, "user:u1", model.ActorUser) })
	// The conversation exists and the process is owned; authentication is a SEPARATE
	// plane and it says the profile is not ready.
	if dto.ProviderConversationID != "conv-unauth" {
		t.Fatalf("conversation = %q", dto.ProviderConversationID)
	}
	waitFor(t, "the readiness reached the row", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.ProviderAuthState == AuthStateRequired
	})
	if err := m.sendTextInput(ctx, tenant, dto.RunRef, "hello"); err == nil {
		t.Fatal("a turn must be refused while the provider reports auth_required")
	}
	// And no authenticate was ever attempted with a method this launch cannot use.
	peer := readGrokFixtureRecord(t, filepath.Join(prof.ConfigHome, grokFixtureRecordFile))
	if countMethod(peer.Methods, grokMethodAuthenticate) != 0 {
		t.Fatalf("the driver attempted an interactive authentication: %v", peer.Methods)
	}
	// An owned RPC peer never takes a raw frame from a caller: a raw line would hand
	// it the whole method surface, permissions included.
	raw := []byte(`{"jsonrpc":"2.0","id":99,"method":"session/prompt","params":{}}`)
	if err := m.sendInput(ctx, tenant, dto.RunRef, raw); err == nil {
		t.Fatal("a raw protocol line must be refused on a driver-backed run")
	}
}

// ⛔ AN EMPTY ADVERTISEMENT OFFERS NO USABLE METHOD, AND THAT IS `required`.
//
// The binding contract is explicit: a profile offering no usable advertised
// method is auth_required. An advertisement that is EMPTY offers none — it is
// the same fact as "only the interactive sign-in", reached by a shorter road —
// and calling it `unknown` was not caution but a hole: `unknown` is neither ready
// nor required, and the runtime blocks only `required`, so a typed prompt
// CROSSED to an owned child that had authenticated nothing.
//
// This row measures the bytes, not the row text: the fixture peer records every
// method it received, so "no prompt crossed" is what the child saw and not what
// the driver believes it sent. The second arm is the control that keeps the
// correction from becoming "Grok is never ready".
func TestGrokRuntimeAnEmptyAuthAdvertisementIsRequiredAndNoPromptCrosses(t *testing.T) {
	for _, tc := range []struct {
		name         string
		advertised   string
		conversation string
		want         string
		wantPrompts  int
	}{
		{
			"an empty advertisement offers no usable method",
			"none", "conv-advert-none", AuthStateRequired, 0,
		},
		{
			"a compatible advertisement authenticates and the turn crosses",
			"", "conv-advert-ok", AuthStateReady, 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _, tenant, prof := grokHarness(t, AuthSourceAccountHome)
			record := setGrokFixture(t, prof, grokFixture{
				SessionID: tc.conversation, AuthMethods: tc.advertised,
				// A turn that is never answered, so the only thing that can put a
				// session/prompt in the child's record is the driver deciding to send one.
				PromptMode: "hold", PromptReleasePath: filepath.Join(t.TempDir(), "never"),
			})
			ctx := context.Background()
			dto, err := grokLaunch(t, m, tenant, prof)
			if err != nil {
				t.Fatalf("createRun: %v", err)
			}
			t.Cleanup(func() {
				_, _ = m.stopRun(context.Background(), tenant, dto.RunRef, "user:u1", model.ActorUser)
			})
			// The readiness the PROVIDER established, on the run's own row.
			waitFor(t, "the readiness reached the row", func() bool {
				d, _ := m.getRun(ctx, tenant, dto.RunRef)
				return d.ProviderAuthState == tc.want
			})

			inputErr := m.sendTextInput(ctx, tenant, dto.RunRef, "a typed operator turn")
			switch {
			case tc.wantPrompts == 0 && inputErr == nil:
				t.Fatal("a turn was accepted for a profile with no usable advertised method")
			case tc.wantPrompts > 0 && inputErr != nil:
				t.Fatalf("input: %v", inputErr)
			}
			if tc.wantPrompts > 0 {
				waitFor(t, "the turn reached the child", func() bool {
					return countMethod(readGrokFixtureRecord(t, record).Methods, grokMethodSessionPrompt) == tc.wantPrompts
				})
			}

			peer := readGrokFixtureRecord(t, record)
			// The record is CURRENT — the handshake it already holds is what proves the
			// peer has been writing it — so a zero below is an absence, not a stale read.
			if countMethod(peer.Methods, grokMethodSessionNew) != 1 {
				t.Fatalf("the child never opened its conversation: %v", peer.Methods)
			}
			if got := countMethod(peer.Methods, grokMethodSessionPrompt); got != tc.wantPrompts {
				t.Fatalf("session/prompt reached the child %d time(s), want %d: %v", got, tc.wantPrompts, peer.Methods)
			}
			// And an advertisement with nothing usable in it is never authenticated
			// against: there is no method to name.
			wantAuth := 0
			if tc.want == AuthStateReady {
				wantAuth = 1
			}
			if got := countMethod(peer.Methods, grokMethodAuthenticate); got != wantAuth {
				t.Fatalf("authenticate attempts = %d, want %d: %v", got, wantAuth, peer.Methods)
			}
		})
	}
}

// An agent that refuses to OPEN a conversation because the profile is not
// authenticated fails the launch — and says which of the two it was, because an
// operator sent to fix the protocol when the answer is "sign this profile in"
// fixes nothing.
func TestGrokRuntimeAuthRequiredOnSessionNewFailsTheLaunchHonestly(t *testing.T) {
	m, _, tenant, prof := grokHarness(t, AuthSourceAccountHome)
	setGrokFixture(t, prof, grokFixture{AuthMethods: "browser", NewSessionAuthRequired: true})
	_, err := grokLaunch(t, m, tenant, prof)
	if err == nil {
		t.Fatal("a launch whose conversation was refused must not report success")
	}
	if !strings.Contains(err.Error(), "auth_required") {
		t.Fatalf("the refusal must name the authentication plane: %v", err)
	}
	if _, found := grokScopedAliasSID(t, m, tenant, prof, "fixture-session"); found {
		t.Fatal("a failed launch bound an alias")
	}
}

// A peer speaking a protocol version this client does not implement fails the
// launch rather than being driven with a dialect nobody verified.
func TestGrokRuntimeRefusesAMismatchedProtocolVersion(t *testing.T) {
	m, _, tenant, prof := grokHarness(t, AuthSourceAccountHome)
	record := setGrokFixture(t, prof, grokFixture{SessionID: "conv-v9", ProtocolVersion: 9})
	if _, err := grokLaunch(t, m, tenant, prof); err == nil {
		t.Fatal("a mismatched protocol version must fail the launch")
	}
	peer := readGrokFixtureRecord(t, record)
	if countMethod(peer.Methods, grokMethodSessionNew) != 0 {
		t.Fatalf("the driver kept talking after a version mismatch: %v", peer.Methods)
	}
	if countMethod(peer.Methods, grokMethodAuthenticate) != 0 {
		t.Fatalf("the driver authenticated against an unsupported peer: %v", peer.Methods)
	}
}

// --- row 5: the delayed prompt result, over real HTTP -------------------------

// ⛔ THE ROW THE ADJUDICATION DEMANDED. The ACP prompt response is the turn's
// COMPLETION. A driver that waited for it would hold the per-run operation lock
// for the whole turn, and this endpoint — and /interrupt, and /stop — would stop
// answering. Here the peer NEVER answers the prompt, and the API stays live.
func TestGrokRuntimeDelayedPromptResultLeavesTheAPIResponsive(t *testing.T) {
	fx := newGrokHTTPFixture(t, "grok-delayed", AuthSourceAccountHome)
	record := setGrokFixture(t, fx.prof, grokFixture{SessionID: "conv-delayed", PromptMode: "never"})
	runRef := fx.createRun(t)

	accepted := fx.input("start a long task", runRef)
	if accepted.code != http.StatusAccepted || accepted.body["accepted"] != true {
		t.Fatalf("the first turn = %d %s", accepted.code, accepted.raw)
	}
	waitFor(t, "the prompt reached the child", func() bool {
		return countMethod(readGrokFixtureRecord(t, record).Methods, grokMethodSessionPrompt) == 1
	})
	// The turn is still in flight — nobody answered it — and the endpoint answers
	// anyway, with the refusal that says why.
	second := fx.input("and another", runRef)
	if second.code != http.StatusConflict {
		t.Fatalf("a second turn while one is in flight = %d %s, want 409", second.code, second.raw)
	}
	if got := countMethod(readGrokFixtureRecord(t, record).Methods, grokMethodSessionPrompt); got != 1 {
		t.Fatalf("the refused second turn reached the child: %d prompts", got)
	}
	// And the read plane is untouched: the run is running, with its conversation.
	got := fx.h.do(http.MethodGet, "/v1/m/sessions/runs/"+runRef, fx.admin, tenantHdr(fx.tenant))
	if got.code != http.StatusOK || got.body["state"] != stateRunning {
		t.Fatalf("get run = %d %s", got.code, got.raw)
	}
	if got.body["provider_conversation_id"] != "conv-delayed" {
		t.Fatalf("conversation = %v", got.body["provider_conversation_id"])
	}
	// The stop still works, which is the other half of "the lock was never held".
	stopped := fx.h.doJSON(http.MethodPost, "/v1/m/sessions/runs/"+runRef+"/stop", fx.admin, nil, tenantHdr(fx.tenant))
	if stopped.code != http.StatusOK || stopped.body["state"] != stateStopped {
		t.Fatalf("stop = %d %s", stopped.code, stopped.raw)
	}
}

// --- row 6: interrupt, and a later turn on the SAME live child ----------------

// ⛔ INTERRUPT DOES NOT END THE TURN — the correlated prompt result does.
// `session/cancel` is a notification with no id and no acknowledgement, so
// clearing the turn locally would let the next input open a second concurrent
// turn while the agent is still winding the first one down.
func TestGrokRuntimeInterruptCancelsTheTurnAndTheSameChildTakesANewOne(t *testing.T) {
	release := filepath.Join(t.TempDir(), "release")
	fx := newGrokHTTPFixture(t, "grok-interrupt", AuthSourceAccountHome)
	record := setGrokFixture(t, fx.prof, grokFixture{
		SessionID: "conv-interrupt", PromptMode: "hold", PromptReleasePath: release,
	})
	runRef := fx.createRun(t)
	lr, ok := fx.m.rt.getLive(fx.tenant, runRef)
	if !ok {
		t.Fatal("the run is not live")
	}
	pid := lr.proc.PID()

	// Interrupting an idle conversation refuses rather than inventing a turn.
	idle := fx.h.doJSON(http.MethodPost, "/v1/m/sessions/runs/"+runRef+"/interrupt", fx.admin, nil, tenantHdr(fx.tenant))
	if idle.code != http.StatusConflict {
		t.Fatalf("interrupt with no active turn = %d %s, want 409", idle.code, idle.raw)
	}
	if accepted := fx.input("long task", runRef); accepted.code != http.StatusAccepted {
		t.Fatalf("input = %d %s", accepted.code, accepted.raw)
	}
	waitFor(t, "the turn is active", func() bool { return lr.session.ActiveTurn() != "" })

	after := fx.h.doJSON(http.MethodPost, "/v1/m/sessions/runs/"+runRef+"/interrupt", fx.admin, nil, tenantHdr(fx.tenant))
	if after.code != http.StatusOK {
		t.Fatalf("interrupt = %d %s", after.code, after.raw)
	}
	if after.body["state"] != stateRunning {
		t.Fatalf("interrupt is NOT terminal; state = %v", after.body["state"])
	}
	if !processRunning(pid) {
		t.Fatal("interrupt must leave the owned process alive")
	}
	// The cancellation reached the child as a NOTIFICATION, and the prompt's own
	// correlated result is what ends the turn.
	waitFor(t, "the child received the cancellation", func() bool {
		return countMethod(readGrokFixtureRecord(t, record).Methods, grokMethodSessionCancel) == 1
	})
	waitFor(t, "the correlated prompt result ended the turn", func() bool {
		return lr.session.ActiveTurn() == ""
	})
	// And the session is USABLE: the next input opens a new turn on the same
	// conversation and the same child.
	if accepted := fx.input("next", runRef); accepted.code != http.StatusAccepted {
		t.Fatalf("input after interrupt = %d %s", accepted.code, accepted.raw)
	}
	waitFor(t, "a new turn started on the same child", func() bool {
		return countMethod(readGrokFixtureRecord(t, record).Methods, grokMethodSessionPrompt) == 2
	})
	if !processRunning(pid) {
		t.Fatal("the second turn ran on another process")
	}
	peer := readGrokFixtureRecord(t, record)
	if countMethod(peer.Methods, grokMethodSessionNew) != 1 {
		t.Fatalf("an interrupt must not start another conversation: %v", peer.Methods)
	}
}

// --- row 7: approvals reach the real child, bounded and never widened ---------

// grokGate is a test authority. It records what it was SHOWN, which is how a row
// proves that a grant is intersected with the options the agent actually offered.
type grokGate struct {
	decision ProviderApprovalDecision
	err      error
	seen     chan ProviderApprovalRequest
	// before runs before the decision is returned, so a row can move the durable
	// authority WHILE the gate is deciding.
	before func()
	// block, when non-nil, holds the decision until it is closed.
	block chan struct{}
}

func (g *grokGate) Approve(_ context.Context, _ model.TenantID, req ProviderApprovalRequest) (ProviderApprovalDecision, error) {
	if g.seen != nil {
		select {
		case g.seen <- req:
		default:
		}
	}
	if g.before != nil {
		g.before()
	}
	if g.block != nil {
		<-g.block
	}
	return g.decision, g.err
}

func TestGrokRuntimeApprovalSelectsOnlyAnOfferedOptionTheAuthorityNamed(t *testing.T) {
	gate := &grokGate{
		decision: ProviderApprovalDecision{Allow: true, Granted: []string{"allow-once"}},
		seen:     make(chan ProviderApprovalRequest, 4),
	}
	m, _, tenant, prof := grokHarness(t, AuthSourceAccountHome, WithProviderApprovalGate(gate))
	record := setGrokFixture(t, prof, grokFixture{
		SessionID: "conv-approve", PromptMode: "hold",
		PromptReleasePath: filepath.Join(t.TempDir(), "never"), PermissionOnTurn: "all",
	})
	ctx := context.Background()
	dto, err := grokLaunch(t, m, tenant, prof)
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	t.Cleanup(func() { _, _ = m.stopRun(context.Background(), tenant, dto.RunRef, "user:u1", model.ActorUser) })
	if err := m.sendTextInput(ctx, tenant, dto.RunRef, "run something"); err != nil {
		t.Fatalf("input: %v", err)
	}
	waitFor(t, "the child received the approval answer", func() bool {
		return len(readGrokFixtureRecord(t, record).Replies) > 0
	})
	peer := readGrokFixtureRecord(t, record)
	var reply grokRequestPermissionResponse
	if err := json.Unmarshal(peer.Replies[0], &reply); err != nil {
		t.Fatalf("the approval answer is not a permission response: %s", peer.Replies[0])
	}
	if reply.Outcome.Outcome != grokOutcomeSelected || reply.Outcome.OptionID != "allow-once" {
		t.Fatalf("outcome = %+v, want the one-shot option the authority named", reply.Outcome)
	}
	// The authority saw exactly what the agent offered, bound to THIS conversation
	// and to the turn that was in flight.
	select {
	case req := <-gate.seen:
		if req.Driver != providerDriverGrok || req.ConversationID != "conv-approve" {
			t.Fatalf("the authority was shown %+v", req)
		}
		if req.TurnID == "" {
			t.Fatal("the authority was shown no turn: a decision it cannot scope is a decision it cannot take")
		}
		want := []string{"allow-once", "allow-always", "reject-once", "reject-always"}
		if strings.Join(req.Requested, ",") != strings.Join(want, ",") {
			t.Fatalf("the authority was shown %v, want exactly the offered options %v", req.Requested, want)
		}
		if req.Kind != grokKindToolCallPermission || req.Method != grokReqRequestPermission {
			t.Fatalf("the authority was shown method %q kind %q", req.Method, req.Kind)
		}
	default:
		t.Fatal("the authority was never consulted")
	}
}

// An authority that allows something the agent did not offer — or a persistent
// option under a turn-scoped decision — grants nothing.
func TestGrokRuntimeApprovalNeverWidensBeyondTheOfferedOptions(t *testing.T) {
	for _, tc := range []struct {
		name     string
		options  string
		decision ProviderApprovalDecision
		wantID   string
		wantKind string
	}{
		{
			"an id nobody offered",
			"all",
			ProviderApprovalDecision{Allow: true, Granted: []string{"allow-everything"}},
			"reject-once", grokOutcomeSelected,
		},
		{
			"a persistent option under a turn-scoped decision",
			"persistent-only",
			ProviderApprovalDecision{Allow: true, Granted: []string{"allow-always"}},
			"", grokOutcomeCancelled,
		},
		{
			"a plain denial",
			"all",
			ProviderApprovalDecision{},
			"reject-once", grokOutcomeSelected,
		},
		{
			"a denial with no one-shot rejection on offer",
			"allow-only",
			ProviderApprovalDecision{},
			"", grokOutcomeCancelled,
		},
		{
			"two options with the same id",
			"duplicate",
			ProviderApprovalDecision{Allow: true, Granted: []string{"allow-once"}},
			"", grokOutcomeCancelled,
		},
		{
			"no option of a kind this client understands",
			"unknown-kinds",
			ProviderApprovalDecision{Allow: true, Granted: []string{"mystery"}},
			"", grokOutcomeCancelled,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gate := &grokGate{decision: tc.decision}
			m, _, tenant, prof := grokHarness(t, AuthSourceAccountHome, WithProviderApprovalGate(gate))
			record := setGrokFixture(t, prof, grokFixture{
				SessionID: "conv-widen", PromptMode: "hold",
				PromptReleasePath: filepath.Join(t.TempDir(), "never"), PermissionOnTurn: tc.options,
			})
			ctx := context.Background()
			dto, err := grokLaunch(t, m, tenant, prof)
			if err != nil {
				t.Fatalf("createRun: %v", err)
			}
			t.Cleanup(func() { _, _ = m.stopRun(context.Background(), tenant, dto.RunRef, "user:u1", model.ActorUser) })
			if err := m.sendTextInput(ctx, tenant, dto.RunRef, "run something"); err != nil {
				t.Fatalf("input: %v", err)
			}
			waitFor(t, "the child received the approval answer", func() bool {
				return len(readGrokFixtureRecord(t, record).Replies) > 0
			})
			var reply grokRequestPermissionResponse
			if err := json.Unmarshal(readGrokFixtureRecord(t, record).Replies[0], &reply); err != nil {
				t.Fatalf("the approval answer is not a permission response: %v", err)
			}
			if reply.Outcome.Outcome != tc.wantKind || reply.Outcome.OptionID != tc.wantID {
				t.Fatalf("outcome = %+v, want %s %q", reply.Outcome, tc.wantKind, tc.wantID)
			}
		})
	}
}

// The authority takes time, and the durable authority can move while it thinks. A
// grant written after the profile was retired would be an authorization decided
// by an owner who no longer exists.
func TestGrokRuntimeApprovalRefusesWhenTheCurrentAuthorityIsGone(t *testing.T) {
	var m *Module
	var tenant model.TenantID
	var prof ProviderProfile
	gate := &grokGate{decision: ProviderApprovalDecision{Allow: true, Granted: []string{"allow-once"}}}
	gate.before = func() {
		retired := ProfileRetired
		if _, err := m.PatchProfile(context.Background(), tenant, prof.Ref, ProfilePatch{State: &retired}); err != nil {
			panic("retire the profile: " + err.Error())
		}
	}
	m, _, tenant, prof = grokHarness(t, AuthSourceAccountHome, WithProviderApprovalGate(gate))
	record := setGrokFixture(t, prof, grokFixture{
		SessionID: "conv-authority", PromptMode: "hold",
		PromptReleasePath: filepath.Join(t.TempDir(), "never"), PermissionOnTurn: "all",
	})
	ctx := context.Background()
	dto, err := grokLaunch(t, m, tenant, prof)
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	if err := m.sendTextInput(ctx, tenant, dto.RunRef, "run something"); err != nil {
		t.Fatalf("input: %v", err)
	}
	waitFor(t, "the child received the approval answer", func() bool {
		return len(readGrokFixtureRecord(t, record).Replies) > 0
	})
	var reply grokRequestPermissionResponse
	if err := json.Unmarshal(readGrokFixtureRecord(t, record).Replies[0], &reply); err != nil {
		t.Fatalf("the approval answer is not a permission response: %v", err)
	}
	if reply.Outcome.Outcome != grokOutcomeCancelled {
		t.Fatalf("outcome = %+v, want cancelled: the authority behind this launch is gone", reply.Outcome)
	}
	// The runtime can still reap its own child — reaping is not a holder's power.
	lr, _ := m.rt.getLive(tenant, dto.RunRef)
	stopCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := m.Stop(stopCtx); err != nil {
		t.Fatalf("runtime shutdown: %v", err)
	}
	waitFor(t, "the child is gone", func() bool { return !processRunning(lr.proc.PID()) })
}

// An interrupt resolves the approvals a turn left pending, so the agent is never
// waiting on a request whose turn is being torn down.
func TestGrokRuntimeInterruptResolvesAPendingApproval(t *testing.T) {
	gate := &grokGate{
		decision: ProviderApprovalDecision{Allow: true, Granted: []string{"allow-once"}},
		block:    make(chan struct{}),
	}
	m, _, tenant, prof := grokHarness(t, AuthSourceAccountHome, WithProviderApprovalGate(gate))
	record := setGrokFixture(t, prof, grokFixture{
		SessionID: "conv-pending", PromptMode: "hold",
		PromptReleasePath: filepath.Join(t.TempDir(), "never"), PermissionOnTurn: "all",
	})
	ctx := context.Background()
	dto, err := grokLaunch(t, m, tenant, prof)
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	t.Cleanup(func() {
		close(gate.block)
		_, _ = m.stopRun(context.Background(), tenant, dto.RunRef, "user:u1", model.ActorUser)
	})
	if err := m.sendTextInput(ctx, tenant, dto.RunRef, "run something"); err != nil {
		t.Fatalf("input: %v", err)
	}
	waitFor(t, "the approval is pending in the authority", func() bool {
		lr, ok := m.rt.getLive(tenant, dto.RunRef)
		if !ok {
			return false
		}
		session := lr.session.(*grokSession)
		session.mu.Lock()
		defer session.mu.Unlock()
		return len(session.pending) == 1
	})
	if _, err := m.interruptRun(ctx, tenant, dto.RunRef, "user:u1", model.ActorUser); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	waitFor(t, "the pending approval was resolved by the interrupt", func() bool {
		return len(readGrokFixtureRecord(t, record).Replies) > 0
	})
	var reply grokRequestPermissionResponse
	if err := json.Unmarshal(readGrokFixtureRecord(t, record).Replies[0], &reply); err != nil {
		t.Fatalf("the approval answer is not a permission response: %v", err)
	}
	if reply.Outcome.Outcome != grokOutcomeCancelled {
		t.Fatalf("outcome = %+v, want the protocol's cancellation", reply.Outcome)
	}
}

// A capability this client never advertised gets a protocol error from the real
// child, not a synthesized answer.
func TestGrokRuntimeRefusesAnUnadvertisedCapabilityRequest(t *testing.T) {
	m, _, tenant, prof := grokHarness(t, AuthSourceAccountHome)
	record := setGrokFixture(t, prof, grokFixture{
		SessionID: "conv-caps", PromptMode: "hold",
		PromptReleasePath: filepath.Join(t.TempDir(), "never"), UnsupportedRequest: "fs/read_text_file",
	})
	ctx := context.Background()
	dto, err := grokLaunch(t, m, tenant, prof)
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	t.Cleanup(func() { _, _ = m.stopRun(context.Background(), tenant, dto.RunRef, "user:u1", model.ActorUser) })
	if err := m.sendTextInput(ctx, tenant, dto.RunRef, "read a file"); err != nil {
		t.Fatalf("input: %v", err)
	}
	waitFor(t, "the child received the refusal", func() bool {
		return len(readGrokFixtureRecord(t, record).Replies) > 0
	})
	var refusal struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(readGrokFixtureRecord(t, record).Replies[0], &refusal); err != nil {
		t.Fatalf("the answer is not a protocol error: %v", err)
	}
	if refusal.Code != grokErrMethodNotSupported {
		t.Fatalf("code = %d, want %d", refusal.Code, grokErrMethodNotSupported)
	}
}

// --- row 8: authentication sources, with no fallback between them -------------

func TestGrokRuntimeManagedInjectionDeniesAndNeverFallsBackToTheAccountHome(t *testing.T) {
	// (a) No governed adapter for this driver at all.
	m, _, tenant, prof := grokHarness(t, AuthSourceManagedInjection)
	record := setGrokFixture(t, prof, grokFixture{SessionID: "conv-managed"})
	_, err := grokLaunch(t, m, tenant, prof)
	if err == nil {
		t.Fatal("a managed launch with no configured adapter must be denied")
	}
	if !strings.Contains(err.Error(), providerDriverGrok) {
		t.Fatalf("the refusal must NAME the driver whose adapter is missing: %v", err)
	}
	if _, ok := tryReadGrokFixtureRecord(record); ok {
		t.Fatal("no child may be spawned when the authentication source cannot be resolved")
	}

	// (b) An adapter that refuses. Still denied, and still no account-home launch.
	m2, _, tenant2, prof2 := grokHarness(t, AuthSourceManagedInjection,
		WithProviderCredentialSource(providerDriverGrok, ProviderCredentialSourceFunc(
			func(context.Context, ProviderCredentialRequest) (ProviderCredential, error) {
				return ProviderCredential{}, errors.New("mint refused")
			})))
	record2 := setGrokFixture(t, prof2, grokFixture{SessionID: "conv-managed"})
	if _, err := grokLaunch(t, m2, tenant2, prof2); err == nil {
		t.Fatal("a managed mint failure must deny the launch")
	}
	if _, ok := tryReadGrokFixtureRecord(record2); ok {
		t.Fatal("a failed mint must not fall back to the profile's account home")
	}

	// (c) A governed adapter that mints. Its variable — and ONLY its variable —
	// reaches the child, and the driver authenticates with the method that source
	// implies rather than with the cached login.
	m3, _, tenant3, prof3 := grokHarness(t, AuthSourceManagedInjection,
		WithProviderCredentialSource(providerDriverGrok, ProviderCredentialSourceFunc(
			func(_ context.Context, req ProviderCredentialRequest) (ProviderCredential, error) {
				if req.Driver != providerDriverGrok {
					return ProviderCredential{}, errors.New("wrong driver")
				}
				return ProviderCredential{
					ID: "managed-cred-1", Scheme: "fixture", NotAfter: farFuture,
					Env: []EnvVar{{Name: "XAI_API_KEY", Value: "minted-for-this-launch"}},
				}, nil
			})))
	record3 := setGrokFixture(t, prof3, grokFixture{SessionID: "conv-managed"})
	dto, err := grokLaunch(t, m3, tenant3, prof3)
	if err != nil {
		t.Fatalf("an authorized managed launch must succeed: %v", err)
	}
	t.Cleanup(func() { _, _ = m3.stopRun(context.Background(), tenant3, dto.RunRef, "user:u1", model.ActorUser) })
	if dto.CredentialID != "managed-cred-1" {
		t.Fatalf("credential_id = %q, want the adapter's non-secret id", dto.CredentialID)
	}
	peer := readGrokFixtureRecord(t, record3)
	if !peer.Present["XAI_API_KEY"] {
		t.Fatal("the governed adapter's credential did not reach the child")
	}
	if peer.Present["ANTHROPIC_AUTH_TOKEN"] {
		t.Fatal("a Grok managed launch must not carry the Claude bearer")
	}
	if len(peer.AuthMethodIDs) != 1 || peer.AuthMethodIDs[0] != grokAuthMethodAPIKey {
		t.Fatalf("authenticate used %v, want the injected key's method", peer.AuthMethodIDs)
	}

	// (d) An adapter that reaches for a variable it does not own is refused.
	m4, _, tenant4, prof4 := grokHarness(t, AuthSourceManagedInjection,
		WithProviderCredentialSource(providerDriverGrok, ProviderCredentialSourceFunc(
			func(context.Context, ProviderCredentialRequest) (ProviderCredential, error) {
				return ProviderCredential{
					ID: "bad", Scheme: "fixture", NotAfter: farFuture,
					Env: []EnvVar{{Name: envGrokHome, Value: "/somewhere/else"}},
				}, nil
			})))
	setGrokFixture(t, prof4, grokFixture{SessionID: "conv-managed"})
	if _, err := grokLaunch(t, m4, tenant4, prof4); err == nil {
		t.Fatal("an adapter that names the profile's home must be refused")
	}
}

// A profile with NO authorized source cannot launch a driver that needs one, and
// the refusal says what is missing rather than failing later inside a handshake.
func TestGrokRuntimeRefusesAProfileWithNoAuthorizedSource(t *testing.T) {
	m, _, tenant, prof := grokHarness(t, "")
	record := setGrokFixture(t, prof, grokFixture{SessionID: "conv-x"})
	_, err := grokLaunch(t, m, tenant, prof)
	if err == nil {
		t.Fatal("a grok profile with no authorized authentication source must be refused")
	}
	if !strings.Contains(err.Error(), AuthSourceAccountHome) ||
		!strings.Contains(err.Error(), AuthSourceManagedInjection) {
		t.Fatalf("the refusal must name the two authorized sources: %v", err)
	}
	if _, ok := tryReadGrokFixtureRecord(record); ok {
		t.Fatal("a refused launch spawned a child")
	}
}

// Home overrides are refused from every direction, for a grok profile as for any
// other: the set is the whole family, not just this driver's own variable.
func TestGrokRuntimeRefusesHomeOverridesFromEveryDirection(t *testing.T) {
	for _, name := range []string{"HOME", envGrokHome, envCodexHome, envClaudeConfigDir} {
		m, _, tenant, prof := grokHarness(t, AuthSourceAccountHome)
		_, err := m.createRun(context.Background(), tenant, CreateRunParams{
			Transport: TransportStreamJSON, Isolation: IsolationNative,
			Actor: "user:u1", ActorKind: model.ActorUser,
			ProviderProfileRef: prof.Ref, EnvAllow: []string{name},
		})
		if err == nil {
			t.Fatalf("env_allow %q must be refused for a profiled launch", name)
		}
	}
	m, _, tenant, prof := grokHarness(t, AuthSourceAccountHome,
		WithLaunchGate(injectingGate{env: []EnvVar{{Name: envGrokHome, Value: "/elsewhere"}}}))
	setGrokFixture(t, prof, grokFixture{SessionID: "conv-guard"})
	if _, err := grokLaunch(t, m, tenant, prof); err == nil {
		t.Fatal("a launch gate that injects a provider home must be refused")
	}
}

// --- row 9: a retired profile, and a moved launch generation ------------------

// A retired profile revokes the HOLDER's powers on a running child, and none of
// them reaches the process.
func TestGrokRuntimeRetiredProfileHasNoChildEffect(t *testing.T) {
	m, _, tenant, prof := grokHarness(t, AuthSourceAccountHome)
	record := setGrokFixture(t, prof, grokFixture{
		SessionID: "conv-retired", PromptMode: "hold",
		PromptReleasePath: filepath.Join(t.TempDir(), "never"),
	})
	ctx := context.Background()
	dto, err := grokLaunch(t, m, tenant, prof)
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	before := readGrokFixtureRecord(t, record)
	retired := ProfileRetired
	if _, err := m.PatchProfile(ctx, tenant, prof.Ref, ProfilePatch{State: &retired}); err != nil {
		t.Fatalf("retire the profile: %v", err)
	}
	if err := m.sendTextInput(ctx, tenant, dto.RunRef, "after retirement"); err == nil {
		t.Fatal("a turn under a retired profile must be refused")
	}
	if _, err := m.interruptRun(ctx, tenant, dto.RunRef, "user:u1", model.ActorUser); err == nil {
		t.Fatal("an interrupt under a retired profile must be refused")
	}
	if _, err := m.stopRun(ctx, tenant, dto.RunRef, "user:u1", model.ActorUser); err == nil {
		t.Fatal("an operator stop under a retired profile must be refused")
	}
	after := readGrokFixtureRecord(t, record)
	if len(after.Methods) != len(before.Methods) {
		t.Fatalf("a revoked control crossed the process boundary: %v -> %v", before.Methods, after.Methods)
	}
	// The runtime's own teardown is a different power and still works.
	lr, _ := m.rt.getLive(tenant, dto.RunRef)
	stopCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := m.Stop(stopCtx); err != nil {
		t.Fatalf("runtime shutdown after revoking the holder: %v", err)
	}
	waitFor(t, "the child is gone", func() bool { return !processRunning(lr.proc.PID()) })
}

// While the owned handshake is open the reservation window is open too, so the
// root response the provider has already sent is QUEUED, not written — and a
// launch that then loses its row binds nothing at all.
func TestGrokRuntimeDefersTheAliasUntilTheLaunchCommitsAndRollsBackCleanly(t *testing.T) {
	release := filepath.Join(t.TempDir(), "release")
	m, st, tenant, prof := grokHarness(t, AuthSourceAccountHome)
	setGrokFixture(t, prof, grokFixture{SessionID: "conv-held", HoldNewPath: release})
	ctx := context.Background()

	type outcome struct {
		dto runDTO
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		dto, err := grokLaunch(t, m, tenant, prof)
		done <- outcome{dto, err}
	}()

	var runRef string
	waitFor(t, "the pending run row exists", func() bool {
		return st.View(ctx, tenant, func(sc store.Scope) error {
			repo, err := sc.Ext(runKind)
			if err != nil {
				return err
			}
			rows, _, err := repo.List(ctx, model.Query{
				Filters: []model.Filter{eq(colRunProfileID, prof.Ref)}, Limit: 2,
			})
			if err != nil || len(rows) != 1 || rows[0].String(colState) != statePending {
				return errors.New("not yet")
			}
			runRef = rows[0].String(colRunRef)
			return nil
		}) == nil
	})
	if _, found := grokScopedAliasSID(t, m, tenant, prof, "conv-held"); found {
		t.Fatal("no alias may exist before the launch transition commits")
	}

	// A successor incarnation takes the row while the handshake is open.
	successor := model.NewID()
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		rec, err := findRunRec(ctx, repo, runRef)
		if err != nil {
			return err
		}
		rec[colRuntimeLaunchID] = successor.String()
		_, err = repo.Update(ctx, rec)
		return err
	}); err != nil {
		t.Fatalf("stamp a successor generation: %v", err)
	}
	if err := os.WriteFile(release, []byte("go"), 0o600); err != nil {
		t.Fatalf("release the handshake: %v", err)
	}

	res := <-done
	if res.err == nil {
		t.Fatal("a launch whose generation was superseded must not report success")
	}
	if _, found := grokScopedAliasSID(t, m, tenant, prof, "conv-held"); found {
		t.Fatal("a rolled-back launch bound an alias")
	}
	if got := codexRunRecord(t, m, tenant, runRef).String(colClaudeSessionID); got != "" {
		t.Fatalf("a rolled-back launch recorded a provider id: %q", got)
	}
}

// A notification naming another conversation binds nothing and mutates nothing:
// only the correlated root response may nominate, and it already did.
func TestGrokRuntimeAForeignNotificationBindsNothing(t *testing.T) {
	m, _, tenant, prof := grokHarness(t, AuthSourceAccountHome)
	setGrokFixture(t, prof, grokFixture{
		SessionID: "conv-ours", PromptMode: "hold",
		PromptReleasePath: filepath.Join(t.TempDir(), "never"), ForeignUpdate: true,
	})
	ctx := context.Background()
	dto, err := grokLaunch(t, m, tenant, prof)
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	t.Cleanup(func() { _, _ = m.stopRun(context.Background(), tenant, dto.RunRef, "user:u1", model.ActorUser) })
	if err := m.sendTextInput(ctx, tenant, dto.RunRef, "go"); err != nil {
		t.Fatalf("input: %v", err)
	}
	lr, _ := m.rt.getLive(tenant, dto.RunRef)
	session := lr.session.(*grokSession)
	waitFor(t, "the foreign update arrived", func() bool { return session.updateCounts().foreign == 1 })
	if _, found := grokScopedAliasSID(t, m, tenant, prof, "some-other-conversation"); found {
		t.Fatal("a notification bound a conversation")
	}
	if got := codexRunRecord(t, m, tenant, dto.RunRef).String(colClaudeSessionID); got != "conv-ours" {
		t.Fatalf("a notification changed the run's conversation to %q", got)
	}
	if session.ActiveTurn() == "" {
		t.Fatal("a foreign notification ended this conversation's turn")
	}
}

// --- row 10: the terminal stop reaps the whole process group ------------------

func TestGrokRuntimeStopReapsTheOwnedProcessGroup(t *testing.T) {
	m, _, tenant, prof := grokHarness(t, AuthSourceAccountHome)
	record := setGrokFixture(t, prof, grokFixture{SessionID: "conv-stop", SpawnChild: true})
	ctx := context.Background()
	dto, err := grokLaunch(t, m, tenant, prof)
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	pid := int(*dto.PID)
	peer := readGrokFixtureRecord(t, record)
	if peer.ChildPID <= 0 {
		t.Fatal("the fixture did not spawn the grandchild this row exists to reap")
	}
	if !processRunning(peer.ChildPID) {
		t.Fatal("the grandchild is not running")
	}
	stopped, err := m.stopRun(ctx, tenant, dto.RunRef, "user:u1", model.ActorUser)
	if err != nil {
		t.Fatalf("stopRun: %v", err)
	}
	if stopped.State != stateStopped {
		t.Fatalf("state after stop = %q", stopped.State)
	}
	waitFor(t, "the owned child is gone", func() bool { return !processRunning(pid) })
	// The GROUP, not just the child: a grandchild holding the inherited stdout is
	// exactly what a per-process signal would have left behind.
	waitFor(t, "the grandchild is gone", func() bool { return !processRunning(peer.ChildPID) })
	// The graceful half ran first, and session/close is NOT what ended the process.
	final := readGrokFixtureRecord(t, record)
	if countMethod(final.Methods, grokMethodSessionClose) != 1 {
		t.Fatalf("a terminal stop must first release the conversation: %v", final.Methods)
	}
}
