// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// B1 acceptance rows 2, 3, 4 and 5 (runtime half): the profiled launch path.

// profiledHarness is newRuntimeHarness with a local execution environment and two
// active Claude profiles on two real homes.
func profiledHarness(t *testing.T, opts ...Option) (*Module, store.Store, model.TenantID, ProviderProfile, ProviderProfile) {
	t.Helper()
	m, st, tenant, _ := newRuntimeHarness(t, opts...)
	m.UseExecutionEnvironmentRef(testEnvRef)
	configA, userA, configB, userB := twoHomes(t)
	a := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "claude", ConfigHome: configA, UserHome: userA, DisplayName: "A"})
	b := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "claude", ConfigHome: configB, UserHome: userB, DisplayName: "B"})
	return m, st, tenant, a, b
}

func envValue(spec LaunchSpec, name string) (string, bool) {
	for _, e := range spec.Env {
		if e.Name == name {
			return e.Value, true
		}
	}
	return "", false
}

func launchCount(fr *fakeRunner) int {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	return len(fr.specs)
}

// Row 2: the snapshot is persisted BEFORE the spawn, the child gets the profile's
// homes, the run DTO shows references and no path, and the init frame binds the
// scoped alias without touching the legacy tenant-wide alias.
func TestProfiledLaunch_PersistsSnapshotBeforeSpawnAndBindsScopedAlias(t *testing.T) {
	t.Parallel()
	inner := &fakeRunner{initSID: "sess-profiled-1"}
	runner := &inspectingWorkLaunchRunner{inner: inner}
	m, st, tenant, a, _ := profiledHarness(t, WithRunner(runner), WithCredentialSource(staticCred()))
	ctx := context.Background()

	inspected := false
	runner.before = func() error {
		return st.View(ctx, tenant, func(sc store.Scope) error {
			repo, err := sc.Ext(runKind)
			if err != nil {
				return err
			}
			rows, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{eq(colRunProfileID, a.Ref)}, Limit: 2})
			if err != nil || len(rows) != 1 {
				return errors.New("the run row with the snapshot must exist before the spawn")
			}
			rec := rows[0]
			if rec.String(colState) != statePending || rec.String(colRunProfileConfigHome) != a.ConfigHome ||
				rec.String(colRunProfileUserHome) != a.UserHome || rec.String(colRunProfileDriver) != "claude" ||
				rec.String(colRunProfileEnvRef) != testEnvRef {
				return errors.New("snapshot incomplete before spawn")
			}
			inspected = true
			return nil
		})
	}
	dto, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:u1", ActorKind: model.ActorUser,
		ProviderProfileRef: a.Ref, EnvAllow: []string{"MY_PROJECT_FLAG"},
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	if !inspected {
		t.Fatal("runner did not observe the persisted snapshot")
	}
	if dto.ProviderProfileRef != a.Ref || dto.ProviderDriver != "claude" || dto.ProviderEnvironmentRef != testEnvRef {
		t.Fatalf("run dto = %+v", dto)
	}
	raw, _ := json.Marshal(dto)
	if bytes.Contains(raw, []byte(a.ConfigHome)) || bytes.Contains(raw, []byte(a.UserHome)) || bytes.Contains(raw, []byte("tok-secret")) {
		t.Fatalf("run dto leaks a home or a token: %s", raw)
	}
	spec := inner.lastSpec()
	if v, ok := envValue(spec, "HOME"); !ok || v != a.UserHome {
		t.Fatalf("HOME = %q,%v want %q", v, ok, a.UserHome)
	}
	if v, ok := envValue(spec, "CLAUDE_CONFIG_DIR"); !ok || v != a.ConfigHome {
		t.Fatalf("CLAUDE_CONFIG_DIR = %q,%v want %q", v, ok, a.ConfigHome)
	}
	if v, ok := envValue(spec, "ANTHROPIC_AUTH_TOKEN"); !ok || v != "tok-secret" {
		t.Fatal("the governed inference credential must still reach the child")
	}
	if spec.Dir != "" {
		t.Fatalf("a profile must not become the working directory: %q", spec.Dir)
	}

	// The init frame binds the SCOPED alias inside the run-row transaction.
	waitFor(t, "profiled session id capture", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.ClaudeSessionID == "sess-profiled-1"
	})
	alias, ok, err := m.LookupScopedAlias(ctx, tenant, a.Ref, "claude", "sess-profiled-1")
	if err != nil || !ok || alias.RunRef != dto.RunRef {
		t.Fatalf("scoped alias = %+v %v %v", alias, ok, err)
	}
	rec, _ := m.loadRun(ctx, tenant, dto.RunRef)
	if alias.SID != rec.String(colRunClaimSID) || alias.ClaimFence != rec.Int(colClaimFence) {
		t.Fatalf("alias sid/fence %s/%d != run claim %s/%d", alias.SID, alias.ClaimFence, rec.String(colRunClaimSID), rec.Int(colClaimFence))
	}
	if n := countRows(t, m, tenant, aliasKind, eq(colProvider, "claude")); n != 0 {
		t.Fatalf("a profiled run wrote %d legacy tenant-wide claude aliases", n)
	}
	lr, _ := m.rt.getLive(tenant, dto.RunRef)
	lr.mu.Lock()
	captured := lr.sessionIDCaptured
	lr.mu.Unlock()
	if !captured {
		t.Fatal("memory not marked captured after the committed bind")
	}
	// The token never lands in the row.
	for col, v := range rec {
		if s, ok := v.(string); ok && strings.Contains(s, "tok-secret") {
			t.Fatalf("token persisted in column %s", col)
		}
	}
}

// Row 4 (last item) and row 2: two homes, the SAME external id, two runs, two
// scoped aliases to two different canonical sessions.
func TestProfiledLaunch_TwoHomesSameExternalID(t *testing.T) {
	t.Parallel()
	fr := &fakeRunner{initSID: "sess-shared"}
	m, _, tenant, a, b := profiledHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	ctx := context.Background()
	launch := func(ref string) runDTO {
		t.Helper()
		dto, err := m.createRun(ctx, tenant, CreateRunParams{
			Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:u1", ActorKind: model.ActorUser,
			ProviderProfileRef: ref,
		})
		if err != nil {
			t.Fatalf("createRun %s: %v", ref, err)
		}
		waitFor(t, "capture "+ref, func() bool {
			d, _ := m.getRun(ctx, tenant, dto.RunRef)
			return d.ClaudeSessionID == "sess-shared"
		})
		return dto
	}
	ra, rb := launch(a.Ref), launch(b.Ref)
	specA, specB := fr.specs[0], fr.specs[1]
	if ha, _ := envValue(specA, "HOME"); ha != a.UserHome {
		t.Fatalf("run A HOME = %q", ha)
	}
	if hb, _ := envValue(specB, "HOME"); hb != b.UserHome {
		t.Fatalf("run B HOME = %q", hb)
	}
	al, okA, _ := m.LookupScopedAlias(ctx, tenant, a.Ref, "claude", "sess-shared")
	bl, okB, _ := m.LookupScopedAlias(ctx, tenant, b.Ref, "claude", "sess-shared")
	if !okA || !okB || al.SID == bl.SID || al.RunRef != ra.RunRef || bl.RunRef != rb.RunRef {
		t.Fatalf("aliases: %+v / %+v", al, bl)
	}
	if n := countRows(t, m, tenant, providerAliasKind); n != 2 {
		t.Fatalf("scoped alias rows = %d, want 2", n)
	}
}

// Row 3 (first half) and the refusal order: every refusal happens before a run
// row, a claim, a credential or a spawn.
func TestProfiledLaunch_RefusesBeforeAnythingDurable(t *testing.T) {
	t.Parallel()
	fr := &fakeRunner{}
	var mints atomic.Int32
	counting := CredentialSourceFunc(func(ctx context.Context, req CredentialRequest) (Credential, error) {
		mints.Add(1)
		return staticCred().Mint(ctx, req)
	})
	m, _, tenant, a, _ := profiledHarness(t, WithRunner(fr), WithCredentialSource(counting))
	ctx := context.Background()
	base := func(ref string) CreateRunParams {
		return CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:u1", ActorKind: model.ActorUser, ProviderProfileRef: ref}
	}
	expect := func(name string, p CreateRunParams, want int) {
		t.Helper()
		_, err := m.createRun(ctx, tenant, p)
		if statusOf(err) != want {
			t.Fatalf("%s: err=%v status=%d want %d", name, err, statusOf(err), want)
		}
	}
	codex := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "codex", ConfigHome: a.UserHome, UserHome: a.UserHome})
	expect("unknown profile", base("ppf_"+model.NewID().String()), http.StatusNotFound)
	expect("malformed ref", base("not-a-ref"), http.StatusNotFound)
	expect("codex profile", base(codex.Ref), http.StatusUnprocessableEntity)
	withHome := base(a.Ref)
	withHome.EnvAllow = []string{"HOME"}
	expect("env_allow HOME", withHome, http.StatusBadRequest)
	withCfg := base(a.Ref)
	withCfg.EnvAllow = []string{"CLAUDE_CONFIG_DIR"}
	expect("env_allow CLAUDE_CONFIG_DIR", withCfg, http.StatusBadRequest)
	disabled := ProfileDisabled
	if _, err := m.PatchProfile(ctx, tenant, a.Ref, ProfilePatch{State: &disabled}); err != nil {
		t.Fatal(err)
	}
	expect("disabled profile", base(a.Ref), http.StatusConflict)
	active := ProfileActive
	if _, err := m.PatchProfile(ctx, tenant, a.Ref, ProfilePatch{State: &active}); err != nil {
		t.Fatal(err)
	}
	m.rt.environmentRef = "env-other-node"
	expect("foreign environment", base(a.Ref), http.StatusConflict)
	m.rt.environmentRef = ""
	expect("no environment identity", base(a.Ref), http.StatusServiceUnavailable)
	m.rt.environmentRef = testEnvRef
	// A caller-supplied snapshot is overwritten, never trusted: an in-process
	// caller cannot smuggle another home under a valid ref.
	smuggled := base(a.Ref)
	smuggled.ProviderHome = &ProviderHomeSnapshot{ProfileID: a.Ref, Driver: "claude", EnvironmentRef: testEnvRef, ConfigHome: "/nope", UserHome: "/nope"}
	dto, err := m.createRun(ctx, tenant, smuggled)
	if err != nil {
		t.Fatalf("valid ref with a smuggled snapshot: %v", err)
	}
	if v, _ := envValue(fr.lastSpec(), "HOME"); v != a.UserHome {
		t.Fatalf("smuggled snapshot reached the child: HOME=%q", v)
	}
	if n := countRows(t, m, tenant, runKind); n != 1 {
		t.Fatalf("refused launches left run rows: %d", n)
	}
	if launches := launchCount(fr); launches != 1 {
		t.Fatalf("refused launches spawned: %d launches", launches)
	}
	if got := mints.Load(); got != 1 {
		t.Fatalf("refused launches minted credentials: %d mints", got)
	}
	_ = dto

	// A gate that injects HOME for a profiled launch is refused deny-closed, after
	// the claim was taken and before the row exists; the claim is given back.
	m.UseLaunchGate(launchGateFunc(func(context.Context, model.TenantID, LaunchIntent) (LaunchDecision, error) {
		return LaunchDecision{Allowed: true, InjectEnv: []EnvVar{{Name: "HOME", Value: "/elsewhere"}}}, nil
	}))
	if _, err := m.createRun(ctx, tenant, base(a.Ref)); statusOf(err) != http.StatusForbidden {
		t.Fatalf("gate-injected HOME = %v (status %d), want 403", err, statusOf(err))
	}
	if n := countRows(t, m, tenant, runKind); n != 1 {
		t.Fatalf("a refused gate injection left a run row")
	}
	// The same gate is fine for an UNPROFILED launch: legacy behaviour untouched.
	if _, err := m.createRun(ctx, tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:u1", ActorKind: model.ActorUser}); err != nil {
		t.Fatalf("unprofiled launch under the same gate: %v", err)
	}
}

// Row 3: resume keeps the home across a rename and refuses, before Runner and
// before any credential, after a retire, a vanished home or an environment change.
func TestProfiledResume_RevalidatesTheStoredHome(t *testing.T) {
	t.Parallel()
	fr := &fakeRunner{initSID: "sess-r"}
	var mints atomic.Int32
	counting := CredentialSourceFunc(func(ctx context.Context, req CredentialRequest) (Credential, error) {
		mints.Add(1)
		return staticCred().Mint(ctx, req)
	})
	m, _, tenant, a, _ := profiledHarness(t, WithRunner(fr), WithCredentialSource(counting))
	ctx := context.Background()
	dto, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:u1", ActorKind: model.ActorUser,
		ProviderProfileRef: a.Ref,
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	waitFor(t, "capture", func() bool { d, _ := m.getRun(ctx, tenant, dto.RunRef); return d.ClaudeSessionID == "sess-r" })
	stop := func() {
		t.Helper()
		if _, err := m.stopRun(ctx, tenant, dto.RunRef, "user:u1", "user"); err != nil {
			t.Fatalf("stop: %v", err)
		}
	}
	stop()

	// Rename in between: same id, same home, resume proceeds with the same HOME.
	name := "A renamed"
	if _, err := m.PatchProfile(ctx, tenant, a.Ref, ProfilePatch{DisplayName: &name}); err != nil {
		t.Fatal(err)
	}
	rd, err := m.resumeRun(ctx, tenant, dto.RunRef, "user:u1", "user", "")
	if err != nil || rd.State != stateRunning || rd.ProviderProfileRef != a.Ref {
		t.Fatalf("resume after rename: %+v %v", rd, err)
	}
	if v, _ := envValue(fr.lastSpec(), "HOME"); v != a.UserHome {
		t.Fatalf("resume HOME = %q", v)
	}
	if v, _ := envValue(fr.lastSpec(), "CLAUDE_CONFIG_DIR"); v != a.ConfigHome {
		t.Fatalf("resume CLAUDE_CONFIG_DIR = %q", v)
	}
	waitFor(t, "resumed capture", func() bool { d, _ := m.getRun(ctx, tenant, dto.RunRef); return d.State == stateRunning })
	stop()
	launchesBefore, mintsBefore := launchCount(fr), mints.Load()
	refuse := func(name string, want int) {
		t.Helper()
		_, err := m.resumeRun(ctx, tenant, dto.RunRef, "user:u1", "user", "")
		if statusOf(err) != want {
			t.Fatalf("%s: err=%v status=%d want %d", name, err, statusOf(err), want)
		}
		if launchCount(fr) != launchesBefore || mints.Load() != mintsBefore {
			t.Fatalf("%s: refusal reached the runner (%d launches) or the credential source (%d mints)", name, launchCount(fr), mints.Load())
		}
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		if d.State != stateStopped {
			t.Fatalf("%s: refusal changed the run state to %s", name, d.State)
		}
	}
	disabled := ProfileDisabled
	if _, err := m.PatchProfile(ctx, tenant, a.Ref, ProfilePatch{State: &disabled}); err != nil {
		t.Fatal(err)
	}
	refuse("disabled profile", http.StatusConflict)
	active := ProfileActive
	if _, err := m.PatchProfile(ctx, tenant, a.Ref, ProfilePatch{State: &active}); err != nil {
		t.Fatal(err)
	}
	m.rt.environmentRef = "env-other-node"
	refuse("environment changed", http.StatusConflict)
	m.rt.environmentRef = testEnvRef
	// Stop of a run whose profile is disabled is still allowed (its own authority).
	if _, err := m.PatchProfile(ctx, tenant, a.Ref, ProfilePatch{State: &disabled}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.PatchProfile(ctx, tenant, a.Ref, ProfilePatch{State: &active}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(a.ConfigHome); err != nil {
		t.Fatal(err)
	}
	refuse("home vanished", http.StatusUnprocessableEntity)
	if err := os.MkdirAll(a.ConfigHome, 0o700); err != nil {
		t.Fatal(err)
	}
	retired := ProfileRetired
	if _, err := m.PatchProfile(ctx, tenant, a.Ref, ProfilePatch{State: &retired}); err != nil {
		t.Fatal(err)
	}
	refuse("retired profile", http.StatusConflict)

	// A legacy run (no snapshot) resumes as before and is never assigned a home.
	legacy, err := m.createRun(ctx, tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:u1", ActorKind: model.ActorUser})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "legacy capture", func() bool { d, _ := m.getRun(ctx, tenant, legacy.RunRef); return d.ClaudeSessionID == "sess-r" })
	if _, err := m.stopRun(ctx, tenant, legacy.RunRef, "user:u1", "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.resumeRun(ctx, tenant, legacy.RunRef, "user:u1", "user", ""); err != nil {
		t.Fatalf("legacy resume: %v", err)
	}
	if _, ok := envValue(fr.lastSpec(), "CLAUDE_CONFIG_DIR"); ok {
		t.Fatal("a legacy run was assigned a config home on resume")
	}
}

// Row 4: a frame from an old incarnation or a foreign object binds nothing; a
// conflicting id is reported and never rewritten; a store failure leaves memory
// uncaptured and a later frame converges to exactly one alias.
func TestProfiledCapture_StaleFrameConflictAndStoreFailure(t *testing.T) {
	t.Parallel()
	fr := &fakeRunner{} // no init frame: the test drives capture by hand
	m, _, tenant, a, _ := profiledHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	ctx := context.Background()
	launch := func() *liveRun {
		t.Helper()
		dto, err := m.createRun(ctx, tenant, CreateRunParams{
			Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:u1", ActorKind: model.ActorUser,
			ProviderProfileRef: a.Ref,
		})
		if err != nil {
			t.Fatal(err)
		}
		lr, _ := m.rt.getLive(tenant, dto.RunRef)
		return lr
	}
	captured := func(lr *liveRun) bool {
		lr.mu.Lock()
		defer lr.mu.Unlock()
		return lr.sessionIDCaptured
	}
	ra := launch()
	rb := launch()

	// A frame from an OLD incarnation (stale launch id) binds nothing.
	stale := &liveRun{tenant: ra.tenant, runRef: ra.runRef, launchID: model.NewID(), claim: ra.claim, profile: ra.profile}
	m.captureProfiledSessionID(ctx, stale, "sess-stale", m.now())
	if captured(stale) || countRows(t, m, tenant, providerAliasKind) != 0 {
		t.Fatal("a stale incarnation bound an alias")
	}
	// A foreign object with the right run but a made-up claim binds nothing.
	foreign := &liveRun{tenant: ra.tenant, runRef: ra.runRef, launchID: ra.launchID, claim: Lease{SID: "osn_forged", Holder: "x", Fence: 1}, profile: ra.profile}
	m.captureProfiledSessionID(ctx, foreign, "sess-forged", m.now())
	if captured(foreign) || countRows(t, m, tenant, providerAliasKind) != 0 {
		t.Fatal("a foreign claim bound an alias")
	}

	// Store failure during capture: memory stays uncaptured; a retry converges.
	real := m.data
	m.data = failingData{ModuleData: real, fail: 1}
	m.captureProfiledSessionID(ctx, ra, "sess-a", m.now())
	if captured(ra) {
		t.Fatal("marked captured while the store failed")
	}
	m.data = real
	m.captureProfiledSessionID(ctx, ra, "sess-a", m.now())
	if !captured(ra) || countRows(t, m, tenant, providerAliasKind) != 1 {
		t.Fatalf("retry did not converge: captured=%v rows=%d", captured(ra), countRows(t, m, tenant, providerAliasKind))
	}
	m.captureProfiledSessionID(ctx, ra, "sess-a", m.now()) // idempotent
	if countRows(t, m, tenant, providerAliasKind) != 1 {
		t.Fatal("re-capture duplicated the alias")
	}

	// The SAME external id announced by another run of the same profile: a conflict,
	// reported, never rewritten, never captured.
	m.captureProfiledSessionID(ctx, rb, "sess-a", m.now())
	if captured(rb) {
		t.Fatal("a conflicting id was marked captured")
	}
	rb.mu.Lock()
	reported := rb.sessionIDConflictReported
	rb.mu.Unlock()
	if !reported {
		t.Fatal("conflict not reported")
	}
	alias, _, _ := m.LookupScopedAlias(ctx, tenant, a.Ref, "claude", "sess-a")
	if alias.RunRef != ra.runRef || alias.SID != ra.claim.SID {
		t.Fatalf("conflict rewrote the alias: %+v", alias)
	}
	recB, _ := m.loadRun(ctx, tenant, rb.runRef)
	if recB.String(colClaudeSessionID) != "" {
		t.Fatal("a conflicting id was recorded on the run")
	}
	if countRows(t, m, tenant, providerAliasKind) != 1 {
		t.Fatal("conflict added a row")
	}
}

// failingData fails the next `fail` Mutate calls, then delegates.
type failingData struct {
	api.ModuleData
	fail int
}

func (f failingData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	if f.fail > 0 {
		f.fail--
		return errors.New("simulated store outage")
	}
	return f.ModuleData.Mutate(ctx, tenant, fn)
}

// Row 3 (K4 half): an unprofiled dispatch keeps the EXACT legacy digest bytes; a
// profiled dispatch is replayed exactly and refuses another profile under its key.
func TestProfiledLaunch_K4DigestCompatibilityAndConflict(t *testing.T) {
	t.Parallel()
	legacy := CreateRunParams{Name: "k4", Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "a", ActorKind: model.ActorAgent, AgentRef: "a"}
	encoded, err := canonicalJSON(legacy)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"ProviderProfileRef", "ProviderHome"} {
		if bytes.Contains(encoded, []byte(key)) {
			t.Fatalf("an unprofiled dispatch digests %q; the legacy hash bytes changed: %s", key, encoded)
		}
	}
	profiled := legacy
	profiled.ProviderProfileRef = "ppf_x"
	profiled.ProviderHome = &ProviderHomeSnapshot{ProfileID: "ppf_x", Driver: "claude", EnvironmentRef: "env", ConfigHome: "/c", UserHome: "/u"}
	pe, _ := canonicalJSON(profiled)
	if !bytes.Contains(pe, []byte(`"ProviderProfileRef":"ppf_x"`)) || !bytes.Contains(pe, []byte(`"config_home":"/c"`)) {
		t.Fatalf("a profiled dispatch must digest the profile and the effective snapshot: %s", pe)
	}

	runner := &fakeRunner{initSID: "sess-k4"}
	m, st, tenant, a, b := profiledHarness(t,
		WithRunner(runner), WithCredentialSource(staticCred()),
		WithWorkIdentityResolver(allowWorkIdentity{}), WithWorkContentGuard(allowWorkContent{}),
	)
	itemID, _, agentRef := readyWorkLaunchItem(t, m, st, tenant)
	spec := workLaunchSpec(itemID, agentRef)
	spec.Runtime.ProviderProfileRef = a.Ref
	first, err := m.LaunchForWork(context.Background(), tenant, spec)
	if err != nil {
		t.Fatalf("profiled LaunchForWork: %v", err)
	}
	rec, _ := m.loadRun(context.Background(), tenant, first.RunRef)
	if rec.String(colRunProfileID) != a.Ref || rec.String(colRunProfileConfigHome) != a.ConfigHome {
		t.Fatalf("work run did not persist the snapshot: %v", rec)
	}
	if v, _ := envValue(runner.lastSpec(), "CLAUDE_CONFIG_DIR"); v != a.ConfigHome {
		t.Fatalf("work launch CLAUDE_CONFIG_DIR = %q", v)
	}
	second, err := m.LaunchForWork(context.Background(), tenant, spec)
	if err != nil || !second.Replayed || second.RunRef != first.RunRef {
		t.Fatalf("exact profiled replay = %+v %v", second, err)
	}
	if launchCount(runner) != 1 {
		t.Fatalf("replay spawned again: %d", launchCount(runner))
	}
	other := spec
	other.Runtime.ProviderProfileRef = b.Ref
	other.OwnerEpoch, other.WorkLeaseFence, other.AttemptKind = first.OwnerEpoch, first.WorkLeaseFence, WorkLaunchAttemptLeaseBind
	decoded, _ := hex.DecodeString(first.DispatchKey)
	copy(other.DispatchKey[:], decoded)
	_, err = m.LaunchForWork(context.Background(), tenant, other)
	if err == nil || !strings.Contains(err.Error(), "dispatch_conflict") {
		t.Fatalf("another profile under the same dispatch key = %v, want dispatch_conflict", err)
	}
	if launchCount(runner) != 1 {
		t.Fatalf("conflict spawned: %d launches", launchCount(runner))
	}
}

// Row 2 with a REAL OS child: the fixture writes ONLY its location variables into
// its own config home; each child sees its profile's homes, the parent is intact,
// and the governed token reaches the child without ever landing in the file, the
// row or the API view.
func TestProfiledLaunch_RealChildFixtureReceivesHomes(t *testing.T) {
	t.Parallel()
	script := filepath.Join(t.TempDir(), "fixture-claude.sh")
	body := "#!/bin/sh\n" +
		"trap 'exit 0' TERM\n" +
		"printf 'HOME=%s\\nCLAUDE_CONFIG_DIR=%s\\n' \"$HOME\" \"$CLAUDE_CONFIG_DIR\" > \"$CLAUDE_CONFIG_DIR/seen-env\"\n" +
		"printf '{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"sess-fixture\"}\\n'\n" +
		"while IFS= read -r line; do :; done\n" +
		"exit 0\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	m, _, tenant, a, b := profiledHarness(t,
		WithRunner(NewProcRunner()), WithProgram(script),
		WithCredentialSource(staticCred()), WithStopWaitDelay(2*time.Second))
	ctx := context.Background()
	parentHome, parentConfigDir := os.Getenv("HOME"), os.Getenv("CLAUDE_CONFIG_DIR")
	for _, prof := range []ProviderProfile{a, b} {
		dto, err := m.createRun(ctx, tenant, CreateRunParams{
			Transport: TransportStreamJSON, PermissionMode: "default", Isolation: IsolationNative,
			Actor: "user:u1", ActorKind: model.ActorUser, ProviderProfileRef: prof.Ref,
		})
		if err != nil {
			t.Fatalf("createRun %s: %v", prof.DisplayName, err)
		}
		waitFor(t, "fixture init capture", func() bool {
			d, _ := m.getRun(ctx, tenant, dto.RunRef)
			return d.ClaudeSessionID == "sess-fixture"
		})
		seen, err := os.ReadFile(filepath.Join(prof.ConfigHome, "seen-env"))
		if err != nil {
			t.Fatalf("fixture %s wrote nothing: %v", prof.DisplayName, err)
		}
		if !bytes.Contains(seen, []byte("HOME="+prof.UserHome+"\n")) || !bytes.Contains(seen, []byte("CLAUDE_CONFIG_DIR="+prof.ConfigHome+"\n")) {
			t.Fatalf("fixture %s saw %q", prof.DisplayName, seen)
		}
		if bytes.Contains(seen, []byte("tok-secret")) {
			t.Fatal("the fixture must not persist the token")
		}
		if _, err := m.stopRun(ctx, tenant, dto.RunRef, "user:u1", "user"); err != nil {
			t.Fatalf("stop: %v", err)
		}
	}
	// The parent's own environment is untouched: the homes are explicit CHILD
	// values, never a global os.Setenv. (The test process may itself run under a
	// CLAUDE_CONFIG_DIR — this session's does — so the check is before/after.)
	if os.Getenv("HOME") != parentHome || os.Getenv("CLAUDE_CONFIG_DIR") != parentConfigDir {
		t.Fatal("the parent's HOME or CLAUDE_CONFIG_DIR changed")
	}
	if _, err := os.Stat(filepath.Join(a.ConfigHome, "seen-env")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(b.UserHome, "seen-env")); err == nil {
		t.Fatal("a child wrote into another profile's home")
	}
}

// The productive endpoint refuses provider_profile_ref until profiled launches are
// enabled by composition; once enabled it launches and reports the references.
func TestProfiledLaunch_HTTPEndpointGate(t *testing.T) {
	fr := &fakeRunner{initSID: "sess-http"}
	m := New(WithRunner(fr), WithCredentialSource(staticCred()))
	m.UseExecutionEnvironmentRef(testEnvRef)
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	configA, userA, _, _ := twoHomes(t)
	r := h.doJSON("POST", "/v1/m/sessions/provider-profiles", admin, map[string]any{"driver": "claude", "config_home": configA, "user_home": userA}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("profile = %d %s", r.code, r.raw)
	}
	ref := r.body["profile_ref"].(string)
	launch := map[string]any{"transport": "stream-json", "permission_mode": "default", "isolation": "native", "provider_profile_ref": ref}
	if r := h.doJSON("POST", "/v1/m/sessions/runs", admin, launch, tenantHdr(tenant)); r.code != http.StatusUnprocessableEntity {
		t.Fatalf("profiled launch while disabled = %d %s", r.code, r.raw)
	}
	if launchCount(fr) != 0 {
		t.Fatal("a refused profiled launch spawned")
	}
	// A client cannot post a home or a snapshot: unknown keys are rejected.
	if r := h.doJSON("POST", "/v1/m/sessions/runs", admin, map[string]any{"transport": "stream-json", "provider_home": map[string]any{"config_home": "/x"}}, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Fatalf("smuggled snapshot = %d %s", r.code, r.raw)
	}
	m.EnableProfiledLaunches()
	r = h.doJSON("POST", "/v1/m/sessions/runs", admin, launch, tenantHdr(tenant))
	if r.code != http.StatusCreated || r.body["provider_profile_ref"] != ref || r.body["provider_driver"] != "claude" {
		t.Fatalf("profiled launch = %d %s", r.code, r.raw)
	}
	if _, leaked := r.body["provider_config_home"]; leaked || strings.Contains(r.raw, configA) {
		t.Fatalf("run response leaks a home: %s", r.raw)
	}
	m.rt.environmentRef = ""
	if r := h.doJSON("POST", "/v1/m/sessions/runs", admin, launch, tenantHdr(tenant)); r.code != http.StatusServiceUnavailable {
		t.Fatalf("profiled launch without environment = %d %s", r.code, r.raw)
	}
}
