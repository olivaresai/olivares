// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The first-slice ACCEPTANCE, through the ACTUAL sessions runtime.
//
// Every row here goes through createRun/resumeRun/stopRun with the REAL native
// runner spawning a REAL child, the REAL claim and fence, and the REAL
// transactional alias. That is the difference the contract insists on: a
// standalone handshake script proves what a CLI does, and proves nothing about
// whether Olivares can own, bind, govern and end a session.

// --- harness -----------------------------------------------------------------

// setCodexFixture writes the peer's configuration into the profile's own
// configuration home, which is the one place the runtime guarantees the child can
// read on BOTH a create and a resume.
func setCodexFixture(t *testing.T, prof ProviderProfile, cfg codexFixture) string {
	t.Helper()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("fixture config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(prof.ConfigHome, codexFixtureConfigFile), raw, 0o600); err != nil {
		t.Fatalf("write the fixture configuration: %v", err)
	}
	if cfg.RecordPath != "" {
		return cfg.RecordPath
	}
	return filepath.Join(prof.ConfigHome, codexFixtureRecordFile)
}

// codexHarness wires the runtime the way production does — native runner,
// registered Codex driver — with the test binary standing in as the official
// program. The profile is a codex profile on real (temporary) homes.
func codexHarness(t *testing.T, authSource string, opts ...Option) (*Module, store.Store, model.TenantID, ProviderProfile) {
	t.Helper()
	base := []Option{
		WithRunner(NewProcRunner()),
		WithProviderDriver(NewCodexDriver()),
		WithDriverProgram(providerDriverCodex, os.Args[0]),
		WithProductVersion("test"),
		WithStopWaitDelay(2 * time.Second),
		WithDriverTimeouts(20*time.Second, 2*time.Second),
	}
	m, st, tenant, _ := newRuntimeHarness(t, append(base, opts...)...)
	m.UseExecutionEnvironmentRef(testEnvRef)
	config, home := t.TempDir(), t.TempDir()
	prof := mustCreateProfile(t, m, tenant, CreateProfileInput{
		Driver: providerDriverCodex, ConfigHome: config, UserHome: home,
		DisplayName: "codex-a", AuthSource: authSource,
	})
	return m, st, tenant, prof
}

func codexLaunch(t *testing.T, m *Module, tenant model.TenantID, prof ProviderProfile) (runDTO, error) {
	t.Helper()
	return m.createRun(context.Background(), tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: model.ActorUser,
		ProviderProfileRef: prof.Ref,
		EnvAllow:           []string{"FIXTURE_MARKER"},
	})
}

func readFixtureRecord(t *testing.T, path string) codexFixtureRecord {
	t.Helper()
	var rec codexFixtureRecord
	waitFor(t, "the fixture peer wrote its record", func() bool {
		raw, err := os.ReadFile(path)
		if err != nil || len(raw) == 0 {
			return false
		}
		return json.Unmarshal(raw, &rec) == nil
	})
	return rec
}

// processRunning reports whether a pid names a RUNNING process. A reaped process
// is gone from /proc; a killed grandchild reparented to a non-reaping init stays
// as a ZOMBIE, and a zombie answers kill(pid,0) — so the state field is what
// distinguishes "ended" from "still running", and using the signal alone would
// have made this row pass on a process that was still alive.
func processRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	raw, err := os.ReadFile("/proc/" + itoa(pid) + "/stat")
	if err != nil {
		if os.IsNotExist(err) {
			return false
		}
		return syscall.Kill(pid, 0) == nil // no /proc: fall back to the signal probe
	}
	// … ) S …  — the state letter follows the last ')' of comm.
	if idx := bytes.LastIndexByte(raw, ')'); idx >= 0 && idx+2 < len(raw) {
		return raw[idx+2] != 'Z'
	}
	return true
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

func scopedAliasSID(t *testing.T, m *Module, tenant model.TenantID, prof ProviderProfile, external string) (string, bool) {
	t.Helper()
	alias, found, err := m.LookupScopedAlias(context.Background(), tenant, prof.Ref, providerDriverCodex, external)
	if err != nil {
		t.Fatalf("lookup scoped alias: %v", err)
	}
	if !found {
		return "", false
	}
	return alias.SID, true
}

func codexRunRecord(t *testing.T, m *Module, tenant model.TenantID, runRef string) model.Record {
	t.Helper()
	rec, err := m.loadRun(context.Background(), tenant, runRef)
	if err != nil {
		t.Fatalf("load run: %v", err)
	}
	return rec
}

// --- row 1: the launch, the child environment, the transactional alias --------

func TestCodexRuntimeLaunchOwnsTheChildAndBindsItsConversation(t *testing.T) {
	t.Setenv("FIXTURE_MARKER", "marker-value")
	// A host provider secret that a careless allowlist would forward.
	t.Setenv("OPENAI_API_KEY", "host-key-never-inherited")

	m, _, tenant, prof := codexHarness(t, AuthSourceAccountHome)
	record := setCodexFixture(t, prof, codexFixture{ThreadID: "thread-alpha", Account: "apikey"})
	dto, err := codexLaunch(t, m, tenant, prof)
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	if dto.State != stateRunning {
		t.Fatalf("state = %q", dto.State)
	}
	if dto.ProviderDriver != providerDriverCodex {
		t.Fatalf("driver = %q", dto.ProviderDriver)
	}
	if dto.ProviderConversationID != "thread-alpha" {
		t.Fatalf("provider_conversation_id = %q; only the correlated root response may nominate it", dto.ProviderConversationID)
	}
	if dto.ProviderAuthSource != AuthSourceAccountHome {
		t.Fatalf("auth source = %q", dto.ProviderAuthSource)
	}
	waitFor(t, "the provider readiness reached the row", func() bool {
		d, _ := m.getRun(context.Background(), tenant, dto.RunRef)
		return d.ProviderAuthState == AuthStateReady
	})
	// The alias is bound under the PROFILE's scope, in the run row's transaction.
	sid, found := scopedAliasSID(t, m, tenant, prof, "thread-alpha")
	if !found {
		t.Fatal("the launch did not commit a profile-scoped alias")
	}
	rec := codexRunRecord(t, m, tenant, dto.RunRef)
	if rec.String(colRunClaimSID) != sid {
		t.Fatalf("the alias resolves to %q but the run's claim is %q", sid, rec.String(colRunClaimSID))
	}
	if rec.String(colClaudeSessionID) != "thread-alpha" {
		t.Fatalf("the run row did not record the provider conversation: %q", rec.String(colClaudeSessionID))
	}

	// Now what the REAL child actually received.
	peer := readFixtureRecord(t, record)
	if peer.Env[envCodexHome] != prof.ConfigHome {
		t.Fatalf("CODEX_HOME = %q, want the profile's config home %q", peer.Env[envCodexHome], prof.ConfigHome)
	}
	if peer.Env["HOME"] != prof.UserHome {
		t.Fatalf("HOME = %q, want the profile's user home %q", peer.Env["HOME"], prof.UserHome)
	}
	if _, present := peer.Env[envClaudeConfigDir]; present {
		t.Fatal("a codex child must not receive CLAUDE_CONFIG_DIR: that variable belongs to another provider")
	}
	if peer.Present["ANTHROPIC_AUTH_TOKEN"] {
		t.Fatal("the Claude bearer must never be handed to a Codex child")
	}
	if peer.Present["OPENAI_API_KEY"] {
		t.Fatal("an inherited provider secret reached the child")
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
	// The handshake is the documented order and nothing else was attempted.
	want := []string{codexMethodInitialize, codexMethodInitialized, codexMethodAccountRead, codexMethodThreadStart}
	if len(peer.Methods) < len(want) {
		t.Fatalf("methods = %v", peer.Methods)
	}
	for i, method := range want {
		if peer.Methods[i] != method {
			t.Fatalf("methods = %v, want the handshake to begin with %v", peer.Methods, want)
		}
	}
}

// --- row 1b: equal external ids across homes are legal and are TWO sessions ---

func TestCodexRuntimeEqualExternalIdsAcrossHomesAreTwoSessions(t *testing.T) {
	m, _, tenant, profA := codexHarness(t, AuthSourceAccountHome)
	configB, homeB := t.TempDir(), t.TempDir()
	profB := mustCreateProfile(t, m, tenant, CreateProfileInput{
		Driver: providerDriverCodex, ConfigHome: configB, UserHome: homeB,
		DisplayName: "codex-b", AuthSource: AuthSourceAccountHome,
	})
	// The SAME external id from two independent homes: legal, and two sessions.
	forced := codexFixture{ThreadID: "same-id-both-homes", Account: "apikey"}
	setCodexFixture(t, profA, forced)
	setCodexFixture(t, profB, forced)

	first, err := codexLaunch(t, m, tenant, profA)
	if err != nil {
		t.Fatalf("first launch: %v", err)
	}
	second, err := codexLaunch(t, m, tenant, profB)
	if err != nil {
		t.Fatalf("second launch: %v", err)
	}
	if first.RunRef == second.RunRef {
		t.Fatal("two launches produced one run")
	}
	if first.ProviderConversationID != second.ProviderConversationID {
		t.Fatalf("the fixture forced the SAME external id; got %q and %q",
			first.ProviderConversationID, second.ProviderConversationID)
	}
	sidA, okA := scopedAliasSID(t, m, tenant, profA, "same-id-both-homes")
	sidB, okB := scopedAliasSID(t, m, tenant, profB, "same-id-both-homes")
	if !okA || !okB {
		t.Fatalf("both homes must bind their own scoped alias (a=%t b=%t)", okA, okB)
	}
	if sidA == sidB {
		t.Fatalf("one external id under two homes must be TWO canonical sessions, got %q twice", sidA)
	}
	recA := codexRunRecord(t, m, tenant, first.RunRef)
	recB := codexRunRecord(t, m, tenant, second.RunRef)
	if recA.String(colRunClaimSID) == recB.String(colRunClaimSID) {
		t.Fatal("the two runs share a canonical session")
	}
}

// --- row 2: account home versus managed injection, with no fallback ----------

func TestCodexRuntimeManagedInjectionDeniesAndNeverFallsBackToTheAccountHome(t *testing.T) {
	// (a) No governed adapter for this driver at all.
	m, _, tenant, prof := codexHarness(t, AuthSourceManagedInjection)
	record := setCodexFixture(t, prof, codexFixture{ThreadID: "thread-managed", Account: "apikey"})
	_, err := codexLaunch(t, m, tenant, prof)
	if err == nil {
		t.Fatal("a managed launch with no configured adapter must be denied")
	}
	if !strings.Contains(err.Error(), providerDriverCodex) {
		t.Fatalf("the refusal must NAME the driver whose adapter is missing: %v", err)
	}
	if _, statErr := os.Stat(record); statErr == nil {
		t.Fatal("no child may be spawned when the authentication source cannot be resolved")
	}

	// (b) An adapter that refuses. Still denied, and still no account-home launch.
	m2, _, tenant2, prof2 := codexHarness(t, AuthSourceManagedInjection,
		WithProviderCredentialSource(providerDriverCodex, ProviderCredentialSourceFunc(
			func(context.Context, ProviderCredentialRequest) (ProviderCredential, error) {
				return ProviderCredential{}, errors.New("mint refused")
			})))
	record2 := setCodexFixture(t, prof2, codexFixture{ThreadID: "thread-managed", Account: "apikey"})
	if _, err := codexLaunch(t, m2, tenant2, prof2); err == nil {
		t.Fatal("a managed mint failure must deny the launch")
	}
	if _, statErr := os.Stat(record2); statErr == nil {
		t.Fatal("a failed mint must not fall back to the profile's account home")
	}

	// (c) A governed adapter that mints. Its variable — and ONLY its variable —
	// reaches the child; the Claude bearer never does.
	m3, _, tenant3, prof3 := codexHarness(t, AuthSourceManagedInjection,
		WithProviderCredentialSource(providerDriverCodex, ProviderCredentialSourceFunc(
			func(_ context.Context, req ProviderCredentialRequest) (ProviderCredential, error) {
				if req.Driver != providerDriverCodex {
					return ProviderCredential{}, errors.New("wrong driver")
				}
				return ProviderCredential{
					ID: "managed-cred-1", Scheme: "fixture", NotAfter: farFuture,
					Env: []EnvVar{{Name: "OPENAI_API_KEY", Value: "minted-for-this-launch"}},
				}, nil
			})))
	record3 := setCodexFixture(t, prof3, codexFixture{ThreadID: "thread-managed", Account: "apikey"})
	dto, err := codexLaunch(t, m3, tenant3, prof3)
	if err != nil {
		t.Fatalf("an authorized managed launch must succeed: %v", err)
	}
	if dto.CredentialID != "managed-cred-1" {
		t.Fatalf("credential_id = %q, want the adapter's non-secret id", dto.CredentialID)
	}
	peer := readFixtureRecord(t, record3)
	if !peer.Present["OPENAI_API_KEY"] {
		t.Fatal("the governed adapter's credential did not reach the child")
	}
	if peer.Present["ANTHROPIC_AUTH_TOKEN"] {
		t.Fatal("a Codex managed launch must not carry the Claude bearer")
	}

	// (d) An adapter that reaches for a variable it does not own is refused.
	m4, _, tenant4, prof4 := codexHarness(t, AuthSourceManagedInjection,
		WithProviderCredentialSource(providerDriverCodex, ProviderCredentialSourceFunc(
			func(context.Context, ProviderCredentialRequest) (ProviderCredential, error) {
				return ProviderCredential{
					ID: "bad", Scheme: "fixture", NotAfter: farFuture,
					Env: []EnvVar{{Name: envCodexHome, Value: "/somewhere/else"}},
				}, nil
			})))
	setCodexFixture(t, prof4, codexFixture{ThreadID: "thread-managed", Account: "apikey"})
	if _, err := codexLaunch(t, m4, tenant4, prof4); err == nil {
		t.Fatal("an adapter that names the profile's home must be refused")
	}
}

// A profile with NO authorized source cannot launch a driver that needs one, and
// the refusal says what is missing rather than failing later inside a handshake.
func TestCodexRuntimeRefusesAProfileWithNoAuthorizedSource(t *testing.T) {
	m, _, tenant, prof := codexHarness(t, "")
	setCodexFixture(t, prof, codexFixture{ThreadID: "thread-x", Account: "apikey"})
	_, err := codexLaunch(t, m, tenant, prof)
	if err == nil {
		t.Fatal("a codex profile with no authorized authentication source must be refused")
	}
	if !strings.Contains(err.Error(), AuthSourceAccountHome) ||
		!strings.Contains(err.Error(), AuthSourceManagedInjection) {
		t.Fatalf("the refusal must name the two authorized sources: %v", err)
	}
	// And the profile reports itself honestly rather than looking launchable.
	dto := m.toProfileDTO(prof)
	if dto.Operable {
		t.Fatal("a profile with no authorized source must not report itself operable")
	}
}

// --- row 3: inherited secrets and home overrides ------------------------------

func TestCodexRuntimeRefusesHomeOverridesFromEveryDirection(t *testing.T) {
	// From the caller's env_allow — every provider's home, not only this driver's.
	for _, name := range []string{"HOME", envCodexHome, envClaudeConfigDir, envGrokHome} {
		m, _, tenant, prof := codexHarness(t, AuthSourceAccountHome)
		_, err := m.createRun(context.Background(), tenant, CreateRunParams{
			Transport: TransportStreamJSON, Isolation: IsolationNative,
			Actor: "user:u1", ActorKind: model.ActorUser,
			ProviderProfileRef: prof.Ref, EnvAllow: []string{name},
		})
		if err == nil {
			t.Fatalf("env_allow %q must be refused for a profiled launch", name)
		}
	}
	// From the caller's env_allow, for a provider SECRET family.
	for _, name := range []string{"OPENAI_API_KEY", "CODEX_API_KEY", "XAI_API_KEY", "ANTHROPIC_API_KEY"} {
		p := CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, EnvAllow: []string{name}}
		if err := validateCreate(&p); err == nil {
			t.Fatalf("env_allow %q must be refused: a provider secret is never inherited", name)
		}
	}
	// From a launch gate's injection.
	m, _, tenant, prof := codexHarness(t, AuthSourceAccountHome,
		WithLaunchGate(injectingGate{env: []EnvVar{{Name: envCodexHome, Value: "/elsewhere"}}}))
	setCodexFixture(t, prof, codexFixture{ThreadID: "thread-guard", Account: "apikey"})
	if _, err := codexLaunch(t, m, tenant, prof); err == nil {
		t.Fatal("a launch gate that injects a provider home must be refused")
	}
	// A gate cannot mint a provider identity either.
	m2, _, tenant2, prof2 := codexHarness(t, AuthSourceAccountHome,
		WithLaunchGate(injectingGate{env: []EnvVar{{Name: "OPENAI_API_KEY", Value: "gate-key"}}}))
	setCodexFixture(t, prof2, codexFixture{ThreadID: "thread-guard", Account: "apikey"})
	if _, err := codexLaunch(t, m2, tenant2, prof2); err == nil {
		t.Fatal("a launch gate that injects a provider credential must be refused")
	}
}

// injectingGate is a launch gate that allows and injects env.
type injectingGate struct{ env []EnvVar }

func (g injectingGate) Authorize(context.Context, model.TenantID, LaunchIntent) (LaunchDecision, error) {
	return LaunchDecision{Allowed: true, InjectEnv: g.env}, nil
}

// --- rows 4 and 5: the reservation window, rollback and stale generation ------

// While the owned handshake is open the reservation window is open too, so the
// root response the provider has already sent is QUEUED, not written. The run
// row holds no provider id and no alias exists — and the launch that then loses
// its row binds nothing at all.
func TestCodexRuntimeDefersTheAliasUntilTheLaunchCommitsAndRollsBackCleanly(t *testing.T) {
	release := filepath.Join(t.TempDir(), "release")
	m, st, tenant, prof := codexHarness(t, AuthSourceAccountHome)
	setCodexFixture(t, prof, codexFixture{
		ThreadID: "thread-held", Account: "apikey", HoldStartPath: release,
	})
	ctx := context.Background()

	type outcome struct {
		dto runDTO
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		dto, err := codexLaunch(t, m, tenant, prof)
		done <- outcome{dto, err}
	}()

	// The row exists and is PENDING while the handshake is still open.
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
	if _, found := scopedAliasSID(t, m, tenant, prof, "thread-held"); found {
		t.Fatal("no alias may exist before the launch transition commits")
	}

	// A successor incarnation takes the row while the handshake is open. When the
	// provider finally answers, BOTH the launch transition and the queued capture
	// must lose to the guard — the launch rolls back and binds nothing.
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
	if _, found := scopedAliasSID(t, m, tenant, prof, "thread-held"); found {
		t.Fatal("a rolled-back launch bound an alias")
	}
	rec := codexRunRecord(t, m, tenant, runRef)
	if rec.String(colClaudeSessionID) != "" {
		t.Fatalf("a rolled-back launch recorded a provider id: %q", rec.String(colClaudeSessionID))
	}
}

// A capture whose transaction did NOT confirm leaves the flag clear, and a later
// frame retries it idempotently until the store agrees.
func TestCodexRuntimeCaptureRetryConvergesAfterTheStoreRefused(t *testing.T) {
	m, st, tenant, prof := codexHarness(t, AuthSourceAccountHome)
	setCodexFixture(t, prof, codexFixture{ThreadID: "thread-retry", Account: "apikey"})
	ctx := context.Background()

	// Another canonical session already owns that (profile, provider, id) key, so
	// the first capture is refused as the discrepancy it is.
	foreign, err := m.ResolveSession(ctx, tenant, SessionBinding{
		Provider: ProviderOperated, ExternalID: "foreign-run-ref", Origin: OriginOperated,
	})
	if err != nil {
		t.Fatalf("mint a foreign session: %v", err)
	}
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(providerAliasKind)
		if err != nil {
			return err
		}
		_, err = repo.Create(ctx, model.Record{
			colPAProfileID: prof.Ref, colPAProvider: providerDriverCodex,
			colPAExternalID: "thread-retry", colPASID: foreign,
			colPARunRef: "foreign-run-ref", colPALaunchID: model.NewID().String(),
			colPAClaimFence: int64(1), colPABoundAt: model.NewTimestamp(time.Now()).String(),
		})
		return err
	}); err != nil {
		t.Fatalf("seed the conflicting alias: %v", err)
	}

	dto, err := codexLaunch(t, m, tenant, prof)
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	rec := codexRunRecord(t, m, tenant, dto.RunRef)
	if rec.String(colClaudeSessionID) != "" {
		t.Fatal("a refused capture must not claim a provider id the database does not hold")
	}
	if sid, _ := scopedAliasSID(t, m, tenant, prof, "thread-retry"); sid != foreign {
		t.Fatalf("the existing alias was rewritten: %q", sid)
	}

	// The discrepancy is resolved out of band; the NEXT frame converges.
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(providerAliasKind)
		if err != nil {
			return err
		}
		existing, ok, err := findScopedAlias(ctx, sc, prof.Ref, providerDriverCodex, "thread-retry")
		if err != nil || !ok {
			return errors.New("the seeded alias vanished")
		}
		id, perr := model.ParseID(existing.String(model.ColID))
		if perr != nil {
			return perr
		}
		return repo.Delete(ctx, id)
	}); err != nil {
		t.Fatalf("remove the conflicting alias: %v", err)
	}
	// Any later frame drives the retry; a turn's own response is one.
	if err := m.sendTextInput(ctx, tenant, dto.RunRef, "hello"); err != nil {
		t.Fatalf("sendTextInput: %v", err)
	}
	waitFor(t, "the capture converged", func() bool {
		sid, found := scopedAliasSID(t, m, tenant, prof, "thread-retry")
		if !found {
			return false
		}
		rec := codexRunRecord(t, m, tenant, dto.RunRef)
		return sid == rec.String(colRunClaimSID) && rec.String(colClaudeSessionID) == "thread-retry"
	})
}

// --- rows 6, 7 and 8: turn completion, interrupt, stop ------------------------

func TestCodexRuntimeCompletedTurnLeavesTheProcessLiveForAnotherInput(t *testing.T) {
	m, _, tenant, prof := codexHarness(t, AuthSourceAccountHome)
	record := setCodexFixture(t, prof, codexFixture{
		ThreadID: "thread-turns", Account: "apikey", CompleteTurn: true,
	})
	ctx := context.Background()
	dto, err := codexLaunch(t, m, tenant, prof)
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	pid := int(*dto.PID)
	if err := m.sendTextInput(ctx, tenant, dto.RunRef, "first"); err != nil {
		t.Fatalf("first input: %v", err)
	}
	// turn/completed finishes the TURN. The process, the conversation and the run
	// are all still exactly where they were.
	lr, ok := m.rt.getLive(tenant, dto.RunRef)
	if !ok {
		t.Fatal("the run is not live")
	}
	waitFor(t, "the turn completed", func() bool { return lr.session.ActiveTurn() == "" })
	if !processRunning(pid) {
		t.Fatal("a completed turn must not end the owned process")
	}
	current, _ := m.getRun(ctx, tenant, dto.RunRef)
	if current.State != stateRunning {
		t.Fatalf("state after a completed turn = %q", current.State)
	}
	if err := m.sendTextInput(ctx, tenant, dto.RunRef, "second"); err != nil {
		t.Fatalf("second input on the same child: %v", err)
	}
	waitFor(t, "the peer saw two turn/start", func() bool {
		peer := readFixtureRecord(t, record)
		starts := 0
		for _, method := range peer.Methods {
			if method == codexMethodTurnStart {
				starts++
			}
		}
		return starts == 2
	})
}

func TestCodexRuntimeInterruptCancelsTheTurnAndKeepsTheProcessUsable(t *testing.T) {
	m, st, tenant, prof := codexHarness(t, AuthSourceAccountHome)
	record := setCodexFixture(t, prof, codexFixture{ThreadID: "thread-interrupt", Account: "apikey"})
	ctx := context.Background()
	dto, err := codexLaunch(t, m, tenant, prof)
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	pid := int(*dto.PID)
	lr, _ := m.rt.getLive(tenant, dto.RunRef)

	// Interrupting an idle conversation refuses rather than inventing a turn.
	if _, err := m.interruptRun(ctx, tenant, dto.RunRef, "user:u1", model.ActorUser); err == nil {
		t.Fatal("interrupting with no active turn must refuse")
	}
	if err := m.sendTextInput(ctx, tenant, dto.RunRef, "long task"); err != nil {
		t.Fatalf("input: %v", err)
	}
	waitFor(t, "the turn is active", func() bool { return lr.session.ActiveTurn() != "" })

	after, err := m.interruptRun(ctx, tenant, dto.RunRef, "user:u1", model.ActorUser)
	if err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	if after.State != stateRunning {
		t.Fatalf("interrupt is NOT terminal; state = %q", after.State)
	}
	if !processRunning(pid) {
		t.Fatal("interrupt must leave the owned process alive")
	}
	if lr.session.ActiveTurn() != "" {
		t.Fatal("the interrupted turn is still the active one")
	}
	events := eventNames(listRunEvents(t, st, tenant, dto.RunRef))
	if !containsAll(events, "interrupting", "interrupted") {
		t.Fatalf("the interrupt must be audited on both sides: %v", events)
	}
	// And the session is USABLE: the next input opens a new turn on the same
	// conversation and the same child.
	if err := m.sendTextInput(ctx, tenant, dto.RunRef, "next"); err != nil {
		t.Fatalf("input after interrupt: %v", err)
	}
	waitFor(t, "a new turn started on the same child", func() bool {
		return lr.session.ActiveTurn() != ""
	})
	peer := readFixtureRecord(t, record)
	if countMethod(peer.Methods, codexMethodTurnInterrupt) != 1 {
		t.Fatalf("the child saw %d turn/interrupt", countMethod(peer.Methods, codexMethodTurnInterrupt))
	}
	if countMethod(peer.Methods, codexMethodThreadStart) != 1 {
		t.Fatal("an interrupt must not start another conversation")
	}
}

func TestCodexRuntimeStopReapsTheOwnedProcessGroup(t *testing.T) {
	m, _, tenant, prof := codexHarness(t, AuthSourceAccountHome)
	record := setCodexFixture(t, prof, codexFixture{
		ThreadID: "thread-stop", Account: "apikey", SpawnChild: true,
	})
	ctx := context.Background()
	dto, err := codexLaunch(t, m, tenant, prof)
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	pid := int(*dto.PID)
	peer := readFixtureRecord(t, record)
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
	// The graceful half ran first: the child was asked to release its thread.
	final := readFixtureRecord(t, record)
	if countMethod(final.Methods, codexMethodThreadUnsubscribe) != 1 {
		t.Fatalf("a terminal stop must first release the conversation: %v", final.Methods)
	}
}

// --- row 9: exact resume, and a refusal that never becomes a new thread -------

func TestCodexRuntimeResumeUsesTheStoredConversationAndRefusesToFallBack(t *testing.T) {
	m, _, tenant, prof := codexHarness(t, AuthSourceAccountHome)
	setCodexFixture(t, prof, codexFixture{ThreadID: "thread-resume", Account: "apikey"})
	ctx := context.Background()

	dto, err := codexLaunch(t, m, tenant, prof)
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	firstRec := codexRunRecord(t, m, tenant, dto.RunRef)
	firstLaunch := firstRec.String(colRuntimeLaunchID)
	sid := firstRec.String(colRunClaimSID)
	if _, err := m.stopRun(ctx, tenant, dto.RunRef, "user:u1", model.ActorUser); err != nil {
		t.Fatalf("stopRun: %v", err)
	}

	// A resume is a NEW owned process and a NEW generation on the SAME run, SID
	// and conversation.
	resumeRecord := filepath.Join(t.TempDir(), "resume.json")
	setCodexFixture(t, prof, codexFixture{Account: "apikey", RecordPath: resumeRecord})
	resumed, err := m.resumeRun(ctx, tenant, dto.RunRef, "user:u1", model.ActorUser, "")
	if err != nil {
		t.Fatalf("resumeRun: %v", err)
	}
	if resumed.RunRef != dto.RunRef {
		t.Fatalf("resume minted another run: %q", resumed.RunRef)
	}
	if resumed.ProviderConversationID != "thread-resume" {
		t.Fatalf("resume conversation = %q", resumed.ProviderConversationID)
	}
	secondRec := codexRunRecord(t, m, tenant, dto.RunRef)
	if secondRec.String(colRuntimeLaunchID) == firstLaunch {
		t.Fatal("a resume must open a NEW launch generation")
	}
	if secondRec.String(colRunClaimSID) != sid {
		t.Fatal("a resume must keep the Olivares canonical session")
	}
	peer := readFixtureRecord(t, resumeRecord)
	if countMethod(peer.Methods, codexMethodThreadResume) != 1 {
		t.Fatalf("the resume did not use thread/resume: %v", peer.Methods)
	}
	if countMethod(peer.Methods, codexMethodThreadStart) != 0 {
		t.Fatalf("a resume must never start a conversation: %v", peer.Methods)
	}

	// A resume the provider refuses is a REFUSAL, not a new thread.
	if _, err := m.stopRun(ctx, tenant, dto.RunRef, "user:u1", model.ActorUser); err != nil {
		t.Fatalf("stopRun: %v", err)
	}
	failRecord := filepath.Join(t.TempDir(), "fail.json")
	setCodexFixture(t, prof, codexFixture{Account: "apikey", RecordPath: failRecord, FailResume: true})
	if _, err := m.resumeRun(ctx, tenant, dto.RunRef, "user:u1", model.ActorUser, ""); err == nil {
		t.Fatal("a refused resume must not report success")
	}
	failed := readFixtureRecord(t, failRecord)
	if countMethod(failed.Methods, codexMethodThreadStart) != 0 {
		t.Fatalf("a refused resume fell back to thread/start: %v", failed.Methods)
	}
	// The stored conversation is untouched, so a later resume can still try it.
	if codexRunRecord(t, m, tenant, dto.RunRef).String(colClaudeSessionID) != "thread-resume" {
		t.Fatal("a refused resume must not discard the stored conversation")
	}
}

// --- approvals reach the real child with the method's own codec ---------------

func TestCodexRuntimeApprovalIsAnsweredWithTheMethodCodecDenyClosed(t *testing.T) {
	m, _, tenant, prof := codexHarness(t, AuthSourceAccountHome)
	record := setCodexFixture(t, prof, codexFixture{
		ThreadID: "thread-approval", Account: "apikey",
		ApprovalOnTurn: codexReqLegacyExecApproval,
	})
	ctx := context.Background()
	dto, err := codexLaunch(t, m, tenant, prof)
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	if err := m.sendTextInput(ctx, tenant, dto.RunRef, "run something"); err != nil {
		t.Fatalf("input: %v", err)
	}
	waitFor(t, "the child received the approval answer", func() bool {
		return len(readFixtureRecord(t, record).Replies) > 0
	})
	peer := readFixtureRecord(t, record)
	// No authority is wired, so the answer is the legacy contract's STRUCTURED
	// denial: the agent may continue the turn, and nothing was granted.
	var reply struct {
		Decision struct {
			Denied struct {
				Rejection string `json:"rejection"`
			} `json:"denied"`
		} `json:"decision"`
	}
	if err := json.Unmarshal(peer.Replies[0], &reply); err != nil {
		t.Fatalf("the approval answer is not the legacy denial shape: %s", peer.Replies[0])
	}
	if reply.Decision.Denied.Rejection == "" {
		t.Fatalf("approval answer = %s, want a structured denial", peer.Replies[0])
	}
}

// --- the raw-line and readiness refusals --------------------------------------

func TestCodexRuntimeRefusesARawProtocolLineAndAnUnauthenticatedTurn(t *testing.T) {
	m, _, tenant, prof := codexHarness(t, AuthSourceAccountHome)
	setCodexFixture(t, prof, codexFixture{ThreadID: "thread-raw", Account: "", RequiresAuth: true})
	ctx := context.Background()
	dto, err := codexLaunch(t, m, tenant, prof)
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	// The conversation exists and the process is owned; authentication is a
	// SEPARATE plane and it says the profile is not ready.
	if dto.ProviderConversationID != "thread-raw" {
		t.Fatalf("conversation = %q", dto.ProviderConversationID)
	}
	waitFor(t, "the readiness reached the row", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.ProviderAuthState == AuthStateRequired
	})
	if err := m.sendTextInput(ctx, tenant, dto.RunRef, "hello"); err == nil {
		t.Fatal("a turn must be refused while the provider reports auth_required")
	}
	// And an owned RPC peer never takes a raw frame from a caller.
	raw := []byte(`{"id":99,"method":"turn/start","params":{}}`)
	if err := m.sendInput(ctx, tenant, dto.RunRef, raw); err == nil {
		t.Fatal("a raw protocol line must be refused on a driver-backed run")
	}
}

// --- the provider's own vocabulary, not Claude's ------------------------------

// Codex advertises its OWN reasoning efforts per model — `ultra` among them in
// the installed model/list — and its schema types the field as an open
// "non-empty value advertised by the model". Holding it to Claude's GA enum would
// refuse a value the provider itself offers, which is the invented common enum
// the adjudication forbids arriving as a validation instead of as a mapping.
func TestCodexRuntimePassesAProviderAdvertisedEffortThroughUntouched(t *testing.T) {
	m, _, tenant, prof := codexHarness(t, AuthSourceAccountHome)
	record := setCodexFixture(t, prof, codexFixture{ThreadID: "thread-effort", Account: "apikey"})
	ctx := context.Background()
	dto, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: model.ActorUser,
		ProviderProfileRef: prof.Ref, Effort: "ultra",
	})
	if err != nil {
		t.Fatalf("a provider-advertised effort must be accepted: %v", err)
	}
	if err := m.sendTextInput(ctx, tenant, dto.RunRef, "go"); err != nil {
		t.Fatalf("input: %v", err)
	}
	waitFor(t, "the turn carried the effort", func() bool {
		peer := readFixtureRecord(t, record)
		return len(peer.Efforts) == 1 && peer.Efforts[0] == "ultra"
	})

	// The Claude enum still governs the CLAUDE driver, and an unprofiled launch is
	// the Claude path.
	p := CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Effort: "ultra"}
	if err := validateCreate(&p); err == nil {
		t.Fatal("an unprofiled launch must still be held to Claude's effort set")
	}
	claudeConfig, claudeHome := t.TempDir(), t.TempDir()
	claudeProfile := mustCreateProfile(t, m, tenant, CreateProfileInput{
		Driver: providerDriverClaude, ConfigHome: claudeConfig, UserHome: claudeHome,
		DisplayName: "claude-effort",
	})
	if _, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: model.ActorUser,
		ProviderProfileRef: claudeProfile.Ref, Effort: "ultra",
	}); err == nil {
		t.Fatal("a profiled CLAUDE launch must still be held to Claude's effort set")
	}
}

// Readiness is revocable. When the provider says it lost its model-provider
// authentication, a launch-time "ready" stops being the answer — on the row and
// for the next turn.
func TestCodexRuntimeLaterAuthFailureInvalidatesReadiness(t *testing.T) {
	m, _, tenant, prof := codexHarness(t, AuthSourceAccountHome)
	setCodexFixture(t, prof, codexFixture{
		ThreadID: "thread-authloss", Account: "apikey",
		AuthRecovery: codexNotifyAuthRecoveryStarted,
	})
	ctx := context.Background()
	dto, err := codexLaunch(t, m, tenant, prof)
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	waitFor(t, "the launch readiness reached the row", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.ProviderAuthState == AuthStateReady
	})
	if err := m.sendTextInput(ctx, tenant, dto.RunRef, "go"); err != nil {
		t.Fatalf("input: %v", err)
	}
	waitFor(t, "the row records the invalidated readiness", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.ProviderAuthState == AuthStateRequired
	})
	if err := m.sendTextInput(ctx, tenant, dto.RunRef, "again"); err == nil {
		t.Fatal("a turn must be refused once readiness has been invalidated")
	}
	// The process is untouched: losing authentication is not losing the child.
	if !processRunning(int(*dto.PID)) {
		t.Fatal("an authentication failure must not end the owned process")
	}
}

// --- one runtime integration check against the OFFICIAL binary ---------------

// The single justified use of the real CLI: an owned official app-server child,
// started by the actual runtime into a FRESH EMPTY home, reports auth_required
// cleanly and still yields an Olivares run row and a committed alias. It sends no
// turn, so no inference and no account access occur. The standalone handshake
// evidence is reused, not repeated: what this adds is the part a probe cannot
// show — that the RUNTIME owns it.
func TestCodexRuntimeAgainstTheOfficialEmptyHomeSubprocess(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: the official-binary integration check is skipped")
	}
	bin, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("the official codex CLI is not installed on this node")
	}
	m, _, tenant, _ := newRuntimeHarness(t,
		WithRunner(NewProcRunner()),
		WithProviderDriver(NewCodexDriver()),
		WithDriverProgram(providerDriverCodex, bin),
		WithProductVersion("test"),
		WithStopWaitDelay(5*time.Second),
		WithDriverTimeouts(60*time.Second, 5*time.Second),
	)
	m.UseExecutionEnvironmentRef(testEnvRef)
	// FRESH, ISOLATED and EMPTY: never a real account home, and nothing is copied
	// into it. An empty home is precisely what makes the answer auth_required.
	config, home := t.TempDir(), t.TempDir()
	prof := mustCreateProfile(t, m, tenant, CreateProfileInput{
		Driver: providerDriverCodex, ConfigHome: config, UserHome: home,
		DisplayName: "official-empty-home", AuthSource: AuthSourceAccountHome,
	})
	ctx := context.Background()
	dto, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: model.ActorUser, ProviderProfileRef: prof.Ref,
	})
	if err != nil {
		t.Fatalf("createRun against the official app-server: %v", err)
	}
	t.Cleanup(func() { _, _ = m.stopRun(context.Background(), tenant, dto.RunRef, "user:u1", model.ActorUser) })
	if dto.ProviderConversationID == "" {
		t.Fatal("the official app-server nominated no conversation")
	}
	waitFor(t, "the official readiness reached the row", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.ProviderAuthState == AuthStateRequired
	})
	if _, found := scopedAliasSID(t, m, tenant, prof, dto.ProviderConversationID); !found {
		t.Fatal("the official launch did not commit a profile-scoped alias")
	}
	// A turn is refused, not attempted: this check never reaches a model.
	if err := m.sendTextInput(ctx, tenant, dto.RunRef, "hello"); err == nil {
		t.Fatal("an unauthenticated official child must refuse a turn")
	}
	stopped, err := m.stopRun(ctx, tenant, dto.RunRef, "user:u1", model.ActorUser)
	if err != nil {
		t.Fatalf("stopRun: %v", err)
	}
	if stopped.State != stateStopped {
		t.Fatalf("state after stop = %q", stopped.State)
	}
	waitFor(t, "the official child is gone", func() bool { return !processRunning(int(*dto.PID)) })
}

func containsAll(haystack []string, needles ...string) bool {
	for _, needle := range needles {
		found := false
		for _, item := range haystack {
			if item == needle {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func countMethod(methods []string, want string) int {
	n := 0
	for _, method := range methods {
		if method == want {
			n++
		}
	}
	return n
}
